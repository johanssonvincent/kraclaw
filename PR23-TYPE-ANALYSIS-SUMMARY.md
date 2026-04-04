# PR #23 Type Design Analysis — Executive Summary

## Analysis Scope
- **IPCMessage**: Wire format for server↔agent bidirectional messaging
- **IPCBroker** interface + **NATSBroker**: NATS JetStream IPC implementation
- **QueueMessage**: Wire format for per-group message persistence
- **NATSQueue**: NATS JetStream queue implementation

---

## Overall Assessment

| Type | Score | Quality |
|------|-------|---------|
| IPCBroker | **8.75/10** | ✅ Excellent |
| NATSBroker | **8.0/10** | ✅ Excellent |
| NATSQueue | **7.75/10** | ✅ Good |
| IPCMessage | **4.75/10** | ⚠️ Weak (lacks validation) |
| QueueMessage | **3.25/10** | ⚠️ Weak (lacks validation) |

---

## Key Strengths

### 1. IPCBroker Interface — Excellent Encapsulation & Invariants (9/10, 8/10)
- **Subject Injection Prevention**: SanitizeGroupID/SanitizeAgentID prevent NATS subject injection
- **Group Isolation**: Each group gets its own stream; messages never leak cross-group
- **Durable Consumers**: Output (kraclaw-server, wildcard) and Input (per-agent) are durable with explicit ack/nak
- **Clean API**: 4 operations (SendInput, PublishOutput, SubscribeOutput, ReadInput) cover bidirectional messaging
- **Strong Test Coverage**: Comprehensive tests verify subject names, consumer behavior, shutdown

### 2. NATSBroker — Sophisticated Goroutine Lifecycle (8/10, 8/10)
- **Dual-Signal Shutdown**: closedCh + context cancellation ensure coordinated cleanup
- **Watcher Goroutine Pattern**: Prevents deadlock if consumer exits before ctx.Done
- **Proper Error Handling**: Malformed JSON is logged and ack'd (discarded), not redelivered
- **Iterator Tracking**: All iterators tracked in b.iters for cleanup

### 3. NATSQueue — Pragmatic Queue Semantics (8/10, 7/10)
- **Exclusive Dequeue**: WorkQueuePolicy + durable consumer ensure FIFO, no duplicates
- **Non-Destructive Peek**: Uses stream.GetMsg(), doesn't lock messages
- **Self-Healing**: Removes legacy peek consumer on startup (thoughtful!)
- **Good Separation**: Delegates activity tracking to groupActiveStore (MySQL)

---

## Critical Weaknesses

### 1. IPCMessage — No Validation (4/10 encapsulation, 2/10 enforcement)
- **Risk**: Invalid messages can be constructed anywhere
  ```go
  msg := &IPCMessage{
      Group: "",           // Empty! No validation
      AgentID: "",         // Empty! No validation
      Type: "invalid_type", // Invalid! No type checking
      Payload: nil,
  }
  ```
- **Impact**: Downstream errors instead of construction-time failures
- **Recommendation**: Add constructor with validation:
  ```go
  func NewIPCMessage(group, agentID string, typ IPCMessageType, payload json.RawMessage) (*IPCMessage, error)
  ```

### 2. QueueMessage — Anemic Type (3/10 encapsulation, 1/10 enforcement)
- **Risk**: IsTask + TaskID relationship is implicit; no validation enforced
  ```go
  msg := &QueueMessage{
      IsTask: true,
      TaskID: "",     // Should require non-empty TaskID
      Timestamp: time.Time{}, // Zero! Convention not enforced
  }
  ```
- **Impact**: Invalid messages queued; handlers must validate
- **Recommendation**: Add constructors:
  ```go
  func NewQueueMessage(groupJID, content, sender string) *QueueMessage
  func NewQueueTaskMessage(groupJID, taskID string) *QueueMessage
  ```

---

## Moderate Concerns

### 1. Double SubscribeOutput Distributes Messages (NATSBroker)
- **Risk**: If orchestrator calls SubscribeOutput(group) twice, durable consumer `kraclaw-server` is reused
  - First call gets subscription ch1
  - Second call gets subscription ch2
  - Messages alternate between ch1 and ch2 (load-balancing, usually undesired)
- **Mitigation**: Document "call SubscribeOutput once per group and cache result"
- **Evidence**: Test TestNATSWildcardConsumerReceivesMultipleAgents verifies this behavior

### 2. Single-Instance Design Assumption (NATSBroker)
- **Risk**: If multiple server instances call SubscribeOutput, messages distribute across instances
- **Mitigation**: Document assumption; design assumes single server instance
- **Recommendation**: Add comment explaining scaling limitation

### 3. Message Buffer (NATSBroker)
- **Risk**: Buffer size = 64. If orchestrator stops reading and broker has 64+ messages, Ack blocks
- **Mitigation**: Appropriate for single-instance design; low risk

---

## Subject Naming & Topology — Strong Design

### IPC Subjects (Explicit, Enforced)
```
Stream: KRACLAW_IPC_{SANITIZED_GROUP_HASH}  (16 bytes hex)
Input:  kraclaw.ipc.{sanitized}.{agent_id}.input
Output: kraclaw.ipc.{sanitized}.{agent_id}.output
Wildcard: kraclaw.ipc.{sanitized}.*.output

Consumers:
- Server output: kraclaw-server (durable, wildcard)
- Agent input: agent-{sanitized_agent_id} (durable, filtered)
```

### Queue Subjects (Explicit, Enforced)
```
Stream: KRACLAW_QUEUE_{SANITIZED_GROUP_HASH}
Subject: kraclaw.queue.{sanitized}
Consumer: dequeue-{sanitized} (durable, WorkQueue)
```

**Why Strong**:
- Sanitization (SanitizeGroupID, SanitizeAgentID) prevents injection
- Subject helpers encode topology; all public callers use them
- CLAUDE.md documents schema explicitly
- Tests verify subject names end-to-end

---

## Recommended Actions (Priority Order)

### 🔴 High Priority
1. **Add IPCMessage constructor** with validation:
   - Validate Group, AgentID non-empty
   - Validate Type ∈ {IPCMessageText, IPCSessionUpdate, ...}
   - Guard against invalid instances in production
   ```go
   func NewIPCMessage(group, agentID string, typ IPCMessageType, payload json.RawMessage) (*IPCMessage, error)
   ```

2. **Document SubscribeOutput single-group contract**:
   ```go
   // SubscribeOutput returns a channel receiving output from all agents in a group.
   // NOTE: Callers MUST call SubscribeOutput exactly once per group and cache the result.
   // Multiple calls for the same group will share a durable consumer, causing messages
   // to be distributed across returned channels.
   func (b *NATSBroker) SubscribeOutput(ctx context.Context, group string) (<-chan *IPCMessage, error)
   ```

### 🟡 Medium Priority
3. **Add QueueMessage constructors**:
   ```go
   func NewQueueMessage(groupJID, content, sender string) *QueueMessage
   func NewQueueTaskMessage(groupJID, taskID string) *QueueMessage
   ```

4. **Document NATSBroker single-instance design**:
   ```go
   // NATSBroker is designed for a single server instance reading SubscribeOutput.
   // Multiple instances will cause message distribution (load-balancing).
   type NATSBroker struct {
   ```

### 🟢 Low Priority
5. **Add HealthCheck methods** (future, useful for operator observability):
   - NATSBroker.HealthCheck()
   - NATSQueue.HealthCheck()

6. **Document Len() eventual-consistency**:
   - Len() may lag actual queue size
   - Do not use for synchronization

---

## Invariant Summary

| Invariant | Expression | Enforcement | Risk |
|-----------|------------|-------------|------|
| Group Isolation | ✅ Via stream naming | ✅ ensureStream() | Low |
| Subject Injection Prevention | ✅ Via SanitizeXXX | ✅ Pre-publish | Low |
| Durable Consumers | ✅ Via ConsumerConfig | ✅ createOrUpdateConsumer() | Low |
| Message Validity (IPC) | ⚠️ Via constants | ❌ No constructor | **Medium** |
| Message Validity (Queue) | ⚠️ Via IsTask flag | ❌ No constructor | **Medium** |
| Exclusive Dequeue | ✅ Via WorkQueuePolicy | ✅ jstream.Consumer | Low |
| FIFO Ordering | ✅ Via stream sequence | ✅ jstream stream | Low |
| No Goroutine Leaks | ✅ Via tracker slices | ✅ Close() + iter.Stop() | Low |
| Single-Instance Reads | ⚠️ Via durable consumer | ⚠️ Implicit assumption | **Medium** |

---

## Verdict

**Accept with minor improvements.**

The NATS JetStream migration in PR #23 demonstrates excellent architectural thinking around subject safety, consumer durability, and group isolation. The IPCBroker and NATSBroker are well-designed with strong invariants and comprehensive tests.

The message types (IPCMessage, QueueMessage) are anemic but pragmatic for wire formats. Adding optional constructors would future-proof against bugs without breaking existing code.

**Next steps**:
1. Add IPCMessage constructor (high priority)
2. Document SubscribeOutput single-group contract (high priority)
3. Add QueueMessage constructors (medium priority)
4. Document NATSBroker single-instance design (medium priority)

---

## Files Analyzed
- `/home/vince/repos/johanssonvincent/kraclaw/internal/ipc/ipc.go`
- `/home/vince/repos/johanssonvincent/kraclaw/internal/ipc/nats_broker.go`
- `/home/vince/repos/johanssonvincent/kraclaw/internal/ipc/nats_broker_test.go`
- `/home/vince/repos/johanssonvincent/kraclaw/internal/queue/queue.go`
- `/home/vince/repos/johanssonvincent/kraclaw/internal/queue/nats_queue.go`
- `/home/vince/repos/johanssonvincent/kraclaw/internal/queue/nats_queue_test.go`

See **PR23-TYPE-ANALYSIS.md** for the full detailed analysis.
