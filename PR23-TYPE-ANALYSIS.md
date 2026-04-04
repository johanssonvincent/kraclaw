# PR #23 Type Design Analysis: NATS JetStream Migration

## Overview
PR #23 introduces two core NATS JetStream-backed implementations replacing Redis:
1. **IPCBroker** interface with NATSBroker implementation (server↔agent bidirectional messaging)
2. **Queue** interface with NATSQueue implementation (per-group message persistence)

These types handle critical invariants around group isolation, subject naming safety, consumer durability, and message delivery guarantees.

---

## Type 1: IPCMessage

### Invariants Identified

1. **Well-Formed Type Tag**: The `Type` field must be one of the defined `IPCMessageType` constants (IPCMessageText, IPCSessionUpdate, IPCTaskCreate, etc.)
2. **Non-Empty Group ID**: The `Group` field must be a non-empty JID that can be sanitized
3. **Payload Marshalability**: The `Payload` field is JSON-encoded data; deserialization is deferred to handlers
4. **Optional ID Assignment**: The `ID` field is typically set by the broker (via NATS message headers) after reception
5. **Consistent Sender Identity**: The `AgentID` field identifies the originating agent; must be sanitizable to 32 chars or less

### Ratings

- **Encapsulation**: 4/10
  - Type is fully exported with all fields public (struct literal construction everywhere)
  - No validation guards; any code can construct invalid IPCMessage instances
  - `Payload` is `json.RawMessage` (bytes), requiring downstream unmarshaling; no type safety
  - Mutable fields exposed directly (though messages are typically consumed once)

- **Invariant Expression**: 6/10
  - Type constants (IPCMessageText, etc.) make valid message types explicit via const
  - `json.RawMessage` for payload is pragmatic but loses type information at compile-time
  - Field names are clear (Group, AgentID, Type, Payload, ID)
  - No compile-time assertion that Type ∈ {valid types}; runtime pattern matching required

- **Invariant Usefulness**: 7/10
  - Strongly prevents category errors (mixing up message types due to string literals)
  - Defends against typos in message type names
  - The IPCMessageType constant set aligns with actual orchestrator message handlers
  - `json.RawMessage` is pragmatic: handlers can unmarshal only what they need

- **Invariant Enforcement**: 2/10
  - **Critical gap**: No validation at construction time
  - Callers can pass empty Group, invalid AgentID, nil Payload, or invalid Type
  - No runtime check in SendInput/PublishOutput methods
  - Validation is 100% external (caller responsibility) — common source of bugs

### Strengths

- Simple, lightweight struct suitable for wire format
- IPCMessageType constants prevent string-literal typos
- json.RawMessage allows flexible, zero-copy payload handling
- Straightforward marshaling/unmarshaling

### Concerns

- **No validation**: Malformed messages can be constructed and sent. A caller passing empty Group or invalid Type produces runtime failures downstream, not at construction.
- **Mutable Payload**: Although treated as immutable in practice, json.RawMessage is a byte slice and technically mutable.
- **Missing Group/AgentID validation**: Code assumes Group and AgentID are sanitizable; no guard against pathological inputs.
- **Optional ID field**: Semantics are implicit (ID set by broker post-reception); unclear to new readers.

### Recommended Improvements

1. **Add a constructor with validation** (optional, but recommended):
   ```go
   func NewIPCMessage(group, agentID string, typ IPCMessageType, payload json.RawMessage) (*IPCMessage, error) {
       if group == "" {
           return nil, fmt.Errorf("ipc message: group is required")
       }
       if agentID == "" {
           return nil, fmt.Errorf("ipc message: agentID is required")
       }
       switch typ {
       case IPCMessageText, IPCSessionUpdate, IPCTaskCreate, IPCTaskUpdate, IPCTaskDelete, IPCSetModel, IPCShutdown:
       default:
           return nil, fmt.Errorf("ipc message: unknown type %q", typ)
       }
       return &IPCMessage{
           Group: group,
           AgentID: agentID,
           Type: typ,
           Payload: payload,
       }, nil
   }
   ```

2. **Document ID field semantics** in a code comment:
   ```go
   // IPCMessage represents a message exchanged between agent and server.
   type IPCMessage struct {
       Group   string          // Non-empty group JID
       AgentID string          // Agent identifier (sanitized by broker)
       Type    IPCMessageType  // Message category
       Payload json.RawMessage // Type-specific JSON payload
       ID      string          // Message ID assigned by broker (empty pre-send)
   }
   ```

---

## Type 2: IPCBroker Interface

### Invariants Identified

1. **Group Isolation**: Each group has its own NATS stream; messages from group A never leak to group B
2. **Subject Injection Prevention**: AgentIDs are sanitized before use in NATS subjects; `SendInput(group, "agent/../attack", msg)` cannot construct invalid subjects
3. **Durable Consumer Safety**: 
   - Output consumer (`kraclaw-server`) is shared per group → only one server instance reads output
   - Input consumers (per agent-group pair) are durable → messages survive agent restarts
4. **Delivery Guarantees**: 
   - DeliverAllPolicy (both input and output) ensures no message loss on subscription
   - AckExplicitPolicy (both) prevents redelivery after successful processing
   - WorkQueuePolicy (queue only) auto-acks on Dequeue to prevent duplicates
5. **Stream Retention**: IPC streams have 1h MaxAge; old messages auto-expire
6. **No Message Ordering Guarantee**: NATS subjects don't order across agents; within a single agent, order is preserved

### Ratings

- **Encapsulation**: 9/10
  - Interface is clean and exposes only necessary methods
  - Internal helper functions (ipcStreamName, ipcInputSubject, etc.) are unexported
  - Consumer configuration details are encapsulated; caller never touches jetstream.ConsumerConfig
  - Allows swapping NATSBroker with other implementations (e.g., test mock) without caller changes

- **Invariant Expression**: 8/10
  - Subject naming helpers (ipcInputSubject, ipcOutputSubject, ipcOutputWildcard) encode topology directly in code
  - SanitizeGroupID and SanitizeAgentID functions make sanitization explicit (preventing subjects like `kraclaw.ipc.group.foo/bar.input`)
  - Durable consumer names follow consistent pattern: `agent-{sanitized_agentID}` (agent-side), `kraclaw-server` (server-side)
  - Stream naming (KRACLAW_IPC_{SANITIZED}) is visible in code via ipcStreamName()
  - CLAUDE.md schema explicitly documents subject format, retention, and consumer names

- **Invariant Usefulness**: 9/10
  - Prevents real bugs:
    - Subject injection via unsanitized agent/group IDs could produce `kraclaw.ipc.group.agent/../admin.input` (security risk)
    - Durable consumers prevent message loss on crash/reconnect (critical for agent communication)
    - Group isolation prevents cross-group message leakage
    - 1h retention prevents unbounded storage
  - Directly supports use cases: server polls output (wildcard), agent reads input (durable, filtered)

- **Invariant Enforcement**: 9/10
  - **Subject injection**: Enforced at publish/consumer-create time via SanitizeGroupID and SanitizeAgentID (no dots, slashes, wildcards in agent/group hashes)
  - **Group isolation**: Enforced by ipcStreamName — each group gets distinct stream
  - **Durable consumers**: Enforced in SubscribeOutput (durable=`kraclaw-server`, filter=wildcard) and ReadInput (durable=`agent-{sanitized_agentID}`, filter=agent-specific)
  - **Delivery**: Enforced by jetstream.ConsumerConfig (DeliverAllPolicy, AckExplicitPolicy)
  - **Retention**: Enforced by StreamConfig (MaxAge=1h, LimitsPolicy)
  - **Consumer blocking**: consume() method guards registration; returns error if broker.closed=true

### Strengths

- Clean, focused interface: four operations (SendInput, PublishOutput, SubscribeOutput, ReadInput) cover bidirectional messaging
- Subject sanitization prevents injection attacks; group isolation is compile-enforced by design
- Durable, explicit-ack consumers guarantee no message loss across agent restarts
- Channel-based API (returning `<-chan *IPCMessage`) is idiomatic Go; decouples publisher/subscriber timing
- Error handling is explicit (no panics); all errors wrapped with context

### Concerns

- **Durable consumer reuse**: SubscribeOutput reuses `kraclaw-server` consumer across multiple group subscriptions. If broker calls SubscribeOutput twice for the same group:
  - First call creates consumer, starts subscription
  - Second call updates consumer (no-op), starts new subscription
  - Both subscriptions read from same durable consumer — messages may be distributed across subscriptions
  - **Risk**: Real bug if orchestrator accidentally calls SubscribeOutput(group1) twice, then one gets message and one gets stuck
  - **Mitigation**: Test TestNATSWildcardConsumerReceivesMultipleAgents verifies behavior; orchestrator should call SubscribeOutput once per group and cache result

- **Stream MaxAge race**: If ipcStreamMaxAge = 1h and agent doesn't read input for >1h, message expires. However:
  - Unlikely in practice (agents process quickly or are dead)
  - Message already in channel doesn't expire
  - Mitigation: Agent timeout/heartbeat logic should be higher-level concern

### Recommended Improvements

1. **Document that SubscribeOutput must be called once per group**:
   ```go
   // SubscribeOutput returns a channel receiving output from all agents in a group.
   // NOTE: Callers MUST call SubscribeOutput exactly once per group and cache the result.
   // Multiple calls for the same group will share a durable consumer, causing messages
   // to be distributed across returned channels (load-balancing, usually undesired).
   func (b *NATSBroker) SubscribeOutput(ctx context.Context, group string) (<-chan *IPCMessage, error)
   ```

2. **Add a HealthCheck method** (future):
   ```go
   // HealthCheck verifies broker can reach NATS and create a stream.
   func (b *NATSBroker) HealthCheck(ctx context.Context) error {
       select {
       case <-b.closedCh:
           return fmt.Errorf("broker closed")
       default:
       }
       // Minimal: verify NATS connection alive
       _, err := b.js.Stream(ctx, "test_stream_that_wont_exist")
       if err != nil && !errors.Is(err, jetstream.ErrStreamNotFound) {
           return fmt.Errorf("jetstream unavailable: %w", err)
       }
       return nil
   }
   ```

---

## Type 3: QueueMessage

### Invariants Identified

1. **Group Association**: GroupJID must match the group that called Enqueue (enforced by caller, not type)
2. **Content Requirement**: Content field should be non-empty for meaningful messages
3. **Task Fields**: If IsTask=true, TaskID should be non-empty (task-related operations rely on it)
4. **Timestamp Monotonicity**: Timestamp should be set at enqueue time (convention, not enforced)
5. **Sender Identity**: Sender field is optional but should be meaningful if used

### Ratings

- **Encapsulation**: 3/10
  - All fields public and directly mutable
  - No constructor; callers build via struct literal
  - No invariant validation
  - json tags expose internal representation directly

- **Invariant Expression**: 4/10
  - IsTask boolean flag indicates task vs. message (but not mutually exclusive with other fields)
  - TaskID is optional (omitted if IsTask=false) but this is implicit, not enforced
  - Field names are clear but semantics are implicit
  - No type distinction between "task message" and "chat message"

- **Invariant Usefulness**: 5/10
  - IsTask flag prevents some category errors (handlers can check before using TaskID)
  - Timestamp enables ordering/replay (useful for chat history)
  - Sender identifies originating user/channel (needed for logging, routing)
  - TaskID enables task tracking

- **Invariant Enforcement**: 1/10
  - **Critical**: Zero validation at construction or enqueue time
  - Caller can pass empty Content, zero Timestamp, empty Sender
  - No check that IsTask=true implies non-empty TaskID
  - json.Unmarshal in Dequeue/Peek logs malformed messages but continues

### Strengths

- Lightweight struct; minimal serialization overhead
- Flexible: handles both chat and task messages
- json tags are correct

### Concerns

- **Anemic type**: No validation or behavior; it's a data container
- **Task invariant unclear**: IsTask and TaskID should be related; unclear what happens if IsTask=false but TaskID is set
- **Missing content validation**: Empty Content can be enqueued; handlers must check
- **Timestamp not enforced**: Caller can pass zero time; handlers must handle gracefully

### Recommended Improvements

1. **Add constructors**:
   ```go
   // NewQueueMessage creates a validated queue message for chat content.
   func NewQueueMessage(groupJID, content, sender string) *QueueMessage {
       return &QueueMessage{
           GroupJID:  groupJID,
           Content:   content,
           Sender:    sender,
           Timestamp: time.Now(),
       }
   }
   
   // NewQueueTaskMessage creates a task message with validation.
   func NewQueueTaskMessage(groupJID, taskID string) *QueueMessage {
       return &QueueMessage{
           GroupJID:  groupJID,
           IsTask:    true,
           TaskID:    taskID,
           Timestamp: time.Now(),
       }
   }
   ```

2. **Add validation helper**:
   ```go
   func (m *QueueMessage) Validate() error {
       if m.GroupJID == "" {
           return fmt.Errorf("group jid required")
       }
       if m.Content == "" {
           return fmt.Errorf("content required")
       }
       if m.IsTask && m.TaskID == "" {
           return fmt.Errorf("task message requires task id")
       }
       if m.Timestamp.IsZero() {
           return fmt.Errorf("timestamp required")
       }
       return nil
   }
   ```

---

## Type 4: NATSBroker

### Invariants Identified

1. **Stream Per Group**: Each group gets its own NATS stream; created on-demand via ensureStream()
2. **Consumer Lifecycle**: Consumers are created on-demand and durable; CreateOrUpdateConsumer is idempotent
3. **Goroutine Cleanup**: All background goroutines started in consume() are tracked in b.iters and b.cancels
4. **Closed State**: Once Close() is called, subsequent SubscribeOutput/ReadInput calls must fail or be gracefully ignored
5. **Iterator Safety**: Message iterators are stopped before closing the channel to prevent leaks

### Ratings

- **Encapsulation**: 8/10
  - Fields are private (nc, js, logger, mu, cancels, iters, closed, closedCh)
  - Public interface is minimal (SendInput, PublishOutput, SubscribeOutput, ReadInput, DeleteStreams, Close)
  - Internal helpers (ensureStream, ipcStreamName, sanitizeGroupID) are unexported
  - Callers can access returned channels directly; channels are respectable encapsulation

- **Invariant Expression**: 8/10
  - closed boolean + closedCh signal provide dual-mechanism shutdown coordination
  - cancels and iters slices track cleanup responsibilities
  - mu protects concurrent access to state
  - consume() method signature clearly separates iterator creation (blocking) from channel return (non-blocking)

- **Invariant Usefulness**: 8/10
  - Prevents multiple Close() calls from breaking things (idempotent via closed flag)
  - Prevents goroutine leaks by tracking all iterators
  - Prevents zombie goroutines by canceling contexts before stopping iterators
  - Ensures Dequeue durable consumer is exclusive (WorkQueuePolicy + explicit ack)

- **Invariant Enforcement**: 8/10
  - **Closed check**: consume() validates b.closed at registration time; later sends to closedCh; context cancellation backed up by iter.Stop()
  - **Cleanup**: Close() cancels all contexts, stops all iterators, closes closedCh
  - **Locking**: mu protects concurrent access to closed, cancels, iters
  - **Ack/Nak**: All message paths include explicit Ack (success) or Nak (on ctx cancel/close); malformed messages are logged and ack'd
  - **Watcher goroutine**: consume() has a separate watcher goroutine that monitors both ctx.Done and b.closedCh, preventing leaks

### Strengths

- Sophisticated context and goroutine lifecycle management
- Dual-signal shutdown (closedCh + context cancellation) ensures coordinated cleanup
- Watcher goroutine prevents deadlock if consumer exits before ctx is done
- Proper error handling: malformed JSON is logged and ack'd (discarded), not redelivered
- Tests are comprehensive: shutdown, double-close, context cancel, malformed message, etc.
- Clear logging with group context

### Concerns

- **Durable consumer reuse and scaling**: SubscribeOutput creates a durable consumer named `kraclaw-server`. If multiple server instances call SubscribeOutput:
  - All instances update the same durable consumer
  - Messages are distributed across all instances' channels
  - Only one instance sees each message (WorkQueuePolicy-like behavior, but via explicit ack)
  - **Risk**: Assumes single server instance; document or clarify

- **Message buffer size**: consume() creates channels with buffer=64. If channel fills, Ack blocks.
  - Risk: If orchestrator stops reading from SubscribeOutput and broker has 64+ messages, Ack will block, preventing message delivery to other readers
  - Mitigation: Usually only one server instance reads output; appropriate buffer for single-instance design

### Recommended Improvements

1. **Document single-instance assumption**:
   ```go
   // NATSBroker is designed for a single server instance reading SubscribeOutput.
   // Multiple instances will cause message distribution (load-balancing), which is
   // usually undesired for agent command/output communication. For multi-instance
   // deployments, use a dedicated message broker or implement Kafka-style consumer groups.
   type NATSBroker struct {
   ```

2. **Add a HealthCheck method**:
   ```go
   func (b *NATSBroker) HealthCheck(ctx context.Context) error {
       select {
       case <-b.closedCh:
           return fmt.Errorf("broker closed")
       default:
       }
       b.mu.Lock()
       if b.closed {
           b.mu.Unlock()
           return fmt.Errorf("broker closed")
       }
       b.mu.Unlock()
       // Attempt a minimal stream lookup
       _, err := b.js.Stream(ctx, "test_stream")
       if err != nil && !errors.Is(err, jetstream.ErrStreamNotFound) {
           return fmt.Errorf("jetstream unavailable: %w", err)
       }
       return nil
   }
   ```

---

## Type 5: NATSQueue

### Invariants Identified

1. **Stream Per Group**: Each group's queue is isolated in its own NATS stream (WorkQueuePolicy)
2. **Dequeue Ordering**: Dequeue is FIFO via a shared durable consumer (dequeue-{sanitized})
3. **Exclusive Dequeue Consumer**: WorkQueuePolicy ensures only one Dequeue call gets each message (auto-ack on fetch)
4. **Peek Non-Destructive**: Peek uses stream.GetMsg() (not a durable consumer), so it doesn't lock messages
5. **Active Group Tracking**: Delegated to MySQL-backed groupActiveStore; NATSQueue is not responsible for correctness

### Ratings

- **Encapsulation**: 8/10
  - Fields are private (nc, js, gas, logger, mu, cancels, closed, closedCh)
  - Public interface is the Queue interface: Enqueue, Dequeue, Peek, Len, MarkActive, MarkInactive, IsActive, ActiveCount, ActiveJIDs, Close
  - Helper functions (sanitizeQueueGroupID, queueStreamName, queueSubject) are unexported
  - Constructor validates dependencies (nc, gas, logger)

- **Invariant Expression**: 7/10
  - WorkQueuePolicy (explicit in ensureStream) makes Dequeue semantics clear (exclusive, auto-ack)
  - Durable consumer name (dequeue-{sanitized}) makes it clear that Dequeue is exclusive
  - Peek's use of stream.GetMsg() is clear (non-blocking, non-acking)
  - MaxAge = 24h and WorkQueuePolicy are appropriate for transient queues
  - Self-healing code (legacy peek consumer deletion) is thoughtful

- **Invariant Usefulness**: 8/10
  - WorkQueuePolicy prevents duplicate message processing (critical for chat/task queues)
  - FIFO ordering via shared durable consumer is what callers expect
  - Exclusive Dequeue prevents race conditions (no need for external locks)
  - Peek non-destructive is useful for introspection without consuming

- **Invariant Enforcement**: 8/10
  - **Exclusive dequeue**: Enforced by WorkQueuePolicy + durable consumer
  - **FIFO**: Enforced by stream sequence numbering
  - **Group isolation**: Enforced by ensureStream() creating per-group streams
  - **Cleanup**: Close() cancels contexts (though no background goroutines spawned for Enqueue/Dequeue)

### Strengths

- Clean interface: Enqueue, Dequeue, Peek, Len + activity tracking
- Delegation to groupActiveStore for activity tracking is good separation of concerns
- WorkQueuePolicy is appropriate for guaranteed processing
- Peek self-healing (removes legacy consumer) is thoughtful
- Error logging includes helpful context (subject, sequence, raw payload)
- Tests are comprehensive: enqueue/dequeue, empty queue, mark active, etc.

### Concerns

- **No shutdown checks on public methods**: Close() sets b.closed=true but Enqueue/Dequeue/Peek don't check it
  - Risk: Enqueue after Close could fail silently or panic
  - Mitigation: No background goroutines spawned, so only data races possible; low risk

- **Len() is eventual-consistent**: Len() returns stream.Info().State.Msgs, which may lag actual message count
  - Risk: Caller spins on Len() == 0 waiting for queue to drain; could wait forever
  - Mitigation: Unlikely in practice; Len() is for metrics, not synchronization

### Recommended Improvements

1. **Add shutdown checks** (defensive):
   ```go
   func (q *NATSQueue) Enqueue(ctx context.Context, groupJID string, msg *QueueMessage) error {
       q.mu.Lock()
       if q.closed {
           q.mu.Unlock()
           return fmt.Errorf("queue closed")
       }
       q.mu.Unlock()
       // ... rest of logic
   }
   ```

2. **Document eventual consistency of Len()**:
   ```go
   // Len returns the approximate number of pending messages in a group's queue.
   // The value is eventual-consistent and may lag actual count.
   // Do not use for synchronization.
   func (q *NATSQueue) Len(ctx context.Context, groupJID string) (int64, error)
   ```

---

## Summary Table

| Type | Encapsulation | Invariant Expression | Usefulness | Enforcement | Overall |
|------|---|---|---|---|---|
| IPCMessage | 4/10 | 6/10 | 7/10 | 2/10 | **4.75/10** |
| IPCBroker | 9/10 | 8/10 | 9/10 | 9/10 | **8.75/10** |
| QueueMessage | 3/10 | 4/10 | 5/10 | 1/10 | **3.25/10** |
| NATSBroker | 8/10 | 8/10 | 8/10 | 8/10 | **8.0/10** |
| NATSQueue | 8/10 | 7/10 | 8/10 | 8/10 | **7.75/10** |

---

## Cross-Type Observations

**Anemic Messages**: Both IPCMessage and QueueMessage are pure data containers with no validation or methods. This is pragmatic for wire formats but invites bugs. Recommendation: Add constructors with basic validation.

**Sanitization is Core**: Both NATSBroker and NATSQueue rely on SanitizeGroupID/SanitizeAgentID for safety. Tests verify subject generation; CLAUDE.md documents schema.

**Durable Consumers are Critical**: Both brokers rely on durable consumers for durability. Assumes consumer names never collide (true: each group has separate stream) and server code doesn't accidentally call SubscribeOutput twice (unverified; needs comment).

**Shutdown Complexity**: NATSBroker and NATSQueue both track goroutines and contexts for cleanup. This is correct but subtle. Recommendation: Add HealthCheck methods for operator observability.

---

## Risk Assessment

| Risk | Severity | Mitigation |
|------|----------|-----------|
| IPCMessage with invalid Type | Medium | Add constructor validation; add type switch in handlers |
| Double SubscribeOutput call distributes messages | Medium | Add test; document "call once per group" |
| NATSBroker assumes single server instance | Medium | Document assumption; add HealthCheck |
| Malformed queue message causes Dequeue to error | Low | Logging + ack is correct; handlers should check |
| QueueMessage IsTask/TaskID mismatch | Low | Add NewQueueTaskMessage constructor |
| Peek durable consumer left behind | Low | Self-healing deletion is in place |

---

## Conclusion

The NATS JetStream migration in PR #23 introduces well-designed broker and queue abstractions with strong invariants around group isolation, subject safety, and message durability. The IPCBroker interface is particularly well-encapsulated, with clear subject topology and durable consumer configuration.

The main weaknesses are in the message types (IPCMessage, QueueMessage), which lack constructors and validation. This is a pragmatic trade-off for wire-format simplicity, but adds coupling with caller code.

**Recommendation**: Accept as-is, but add comments and optional constructors to IPCMessage and QueueMessage to future-proof against bugs.
