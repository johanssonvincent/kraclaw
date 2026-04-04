# PR #23 Type Design — Actionable Improvements

This document contains concrete code suggestions to address the weaknesses identified in the type design analysis.

---

## 1. IPCMessage Constructor (HIGH PRIORITY)

**Location**: `internal/ipc/ipc.go`

**Problem**: IPCMessage can be constructed with invalid values (empty Group, invalid Type).

**Solution**: Add a constructor function that validates inputs.

```go
// NewIPCMessage creates a validated IPCMessage.
// Returns error if group or agentID is empty, or if typ is not a recognized type.
func NewIPCMessage(group, agentID string, typ IPCMessageType, payload json.RawMessage) (*IPCMessage, error) {
    if group == "" {
        return nil, fmt.Errorf("ipc message: group is required")
    }
    if agentID == "" {
        return nil, fmt.Errorf("ipc message: agentID is required")
    }
    
    // Validate Type is one of known constants
    switch typ {
    case IPCMessageText, IPCSessionUpdate, IPCTaskCreate, IPCTaskUpdate, IPCTaskDelete, IPCSetModel, IPCShutdown:
        // Valid
    default:
        return nil, fmt.Errorf("ipc message: unknown type %q", typ)
    }
    
    return &IPCMessage{
        Group:   group,
        AgentID: agentID,
        Type:    typ,
        Payload: payload,
    }, nil
}
```

**Usage**:
```go
// Instead of:
msg := &IPCMessage{Group: group, AgentID: agentID, Type: ipc.IPCMessageText, Payload: payload}

// Use:
msg, err := ipc.NewIPCMessage(group, agentID, ipc.IPCMessageText, payload)
if err != nil {
    return fmt.Errorf("create ipc message: %w", err)
}
```

**Non-Breaking**: Existing struct literal usage continues to work. Constructor is optional.

**Test Suggestion**:
```go
func TestNewIPCMessage_Validation(t *testing.T) {
    tests := []struct {
        name    string
        group   string
        agentID string
        typ     IPCMessageType
        payload json.RawMessage
        wantErr bool
    }{
        {
            name:    "valid message",
            group:   "group@example.com",
            agentID: "agent1",
            typ:     IPCMessageText,
            payload: []byte(`{"text":"hello"}`),
            wantErr: false,
        },
        {
            name:    "empty group",
            group:   "",
            agentID: "agent1",
            typ:     IPCMessageText,
            wantErr: true,
        },
        {
            name:    "empty agentID",
            group:   "group@example.com",
            agentID: "",
            typ:     IPCMessageText,
            wantErr: true,
        },
        {
            name:    "invalid type",
            group:   "group@example.com",
            agentID: "agent1",
            typ:     IPCMessageType("invalid"),
            wantErr: true,
        },
    }
    
    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            msg, err := NewIPCMessage(tt.group, tt.agentID, tt.typ, tt.payload)
            if (err != nil) != tt.wantErr {
                t.Errorf("NewIPCMessage() error = %v, wantErr %v", err, tt.wantErr)
            }
            if !tt.wantErr && msg == nil {
                t.Errorf("NewIPCMessage() msg = nil, want non-nil")
            }
        })
    }
}
```

---

## 2. IPCMessage Documentation Update (HIGH PRIORITY)

**Location**: `internal/ipc/ipc.go`, IPCMessage struct definition

**Problem**: The ID field's purpose is implicit; it's set by the broker after reception, but callers don't know this.

**Solution**: Add clarifying comments.

```go
// IPCMessage represents a message exchanged between agent and server.
type IPCMessage struct {
    Group   string          `json:"group"`     // Non-empty group JID (usually WhatsApp JID like "123456789012@g.us")
    AgentID string          `json:"agent_id"`  // Agent identifier (will be sanitized by broker before use in NATS subjects)
    Type    IPCMessageType  `json:"type"`      // Message category (must be one of the IPCMessage* constants)
    Payload json.RawMessage `json:"payload"`   // Type-specific JSON payload (raw bytes; handlers unmarshal as needed)
    ID      string          `json:"id"`        // Message ID set by broker on receipt (empty before sending, populated by NATSBroker)
}
```

---

## 3. SubscribeOutput Documentation (HIGH PRIORITY)

**Location**: `internal/ipc/nats_broker.go`, SubscribeOutput method

**Problem**: Calling SubscribeOutput twice for the same group causes message distribution across returned channels (load-balancing), which is usually undesired. This is not documented.

**Solution**: Add a clarifying comment.

```go
// SubscribeOutput returns a channel that receives output from all agents in a group
// via a durable wildcard push consumer named "kraclaw-server".
//
// IMPORTANT: Callers MUST call SubscribeOutput exactly once per group and cache
// the returned channel. Multiple calls for the same group will reuse the same
// durable consumer, causing messages to be distributed across the returned channels
// (load-balancing effect). In a single-server deployment, this is undesired.
//
// Example (correct):
//   ch, err := broker.SubscribeOutput(ctx, group1)
//   // Reuse ch for all messages from group1
//
// Example (incorrect — messages will be split):
//   ch1, _ := broker.SubscribeOutput(ctx, group1)
//   ch2, _ := broker.SubscribeOutput(ctx, group1)
//   // ch1 and ch2 will receive alternating messages
func (b *NATSBroker) SubscribeOutput(ctx context.Context, group string) (<-chan *IPCMessage, error) {
    // ... implementation ...
}
```

---

## 4. QueueMessage Constructors (MEDIUM PRIORITY)

**Location**: `internal/queue/queue.go`

**Problem**: QueueMessage can be constructed with invalid state (IsTask=true but TaskID empty, zero Timestamp).

**Solution**: Add constructor functions.

```go
// NewQueueMessage creates a message from chat content.
// Automatically sets Timestamp to now.
func NewQueueMessage(groupJID, content, sender string) *QueueMessage {
    return &QueueMessage{
        GroupJID:  groupJID,
        Content:   content,
        Sender:    sender,
        Timestamp: time.Now(),
        IsTask:    false,
    }
}

// NewQueueTaskMessage creates a message for a task.
// Automatically sets Timestamp to now.
func NewQueueTaskMessage(groupJID, taskID string) *QueueMessage {
    if taskID == "" {
        // Log warning or panic? For now, let the caller handle validation.
        // Alternatively: return nil, error
    }
    return &QueueMessage{
        GroupJID:  groupJID,
        TaskID:    taskID,
        IsTask:    true,
        Timestamp: time.Now(),
    }
}

// Validate checks if the message has required fields set correctly.
// Useful for defensive programming before enqueue.
func (m *QueueMessage) Validate() error {
    if m.GroupJID == "" {
        return fmt.Errorf("queue message: group jid required")
    }
    if m.IsTask {
        if m.TaskID == "" {
            return fmt.Errorf("queue message: task id required when IsTask=true")
        }
        if m.Content != "" {
            return fmt.Errorf("queue message: content should be empty when IsTask=true")
        }
    } else {
        if m.Content == "" {
            return fmt.Errorf("queue message: content required when IsTask=false")
        }
    }
    if m.Timestamp.IsZero() {
        return fmt.Errorf("queue message: timestamp required")
    }
    return nil
}
```

**Usage**:
```go
// Chat message:
msg := queue.NewQueueMessage(groupJID, "Hello world", "user123")
if err := msg.Validate(); err != nil {
    return err
}
if err := q.Enqueue(ctx, msg.GroupJID, msg); err != nil {
    return err
}

// Task message:
msg := queue.NewQueueTaskMessage(groupJID, "task-abc-123")
if err := msg.Validate(); err != nil {
    return err
}
if err := q.Enqueue(ctx, msg.GroupJID, msg); err != nil {
    return err
}
```

**Non-Breaking**: Existing struct literal usage continues to work.

**Test Suggestion**:
```go
func TestNewQueueMessage(t *testing.T) {
    groupJID := "123456789012@g.us"
    
    msg := NewQueueMessage(groupJID, "Hello", "user1")
    
    if msg.GroupJID != groupJID {
        t.Errorf("GroupJID = %q, want %q", msg.GroupJID, groupJID)
    }
    if msg.Content != "Hello" {
        t.Errorf("Content = %q, want %q", msg.Content, "Hello")
    }
    if msg.IsTask != false {
        t.Errorf("IsTask = %v, want false", msg.IsTask)
    }
    if msg.Timestamp.IsZero() {
        t.Errorf("Timestamp is zero, want non-zero")
    }
}

func TestQueueMessage_Validate(t *testing.T) {
    tests := []struct {
        name    string
        msg     *QueueMessage
        wantErr bool
    }{
        {
            name:    "valid chat message",
            msg:     NewQueueMessage("group@test", "hello", "user"),
            wantErr: false,
        },
        {
            name:    "valid task message",
            msg:     NewQueueTaskMessage("group@test", "task123"),
            wantErr: false,
        },
        {
            name: "task message with empty TaskID",
            msg: &QueueMessage{
                GroupJID:  "group@test",
                IsTask:    true,
                TaskID:    "",
                Timestamp: time.Now(),
            },
            wantErr: true,
        },
        {
            name: "chat message with empty Content",
            msg: &QueueMessage{
                GroupJID:  "group@test",
                IsTask:    false,
                Content:   "",
                Timestamp: time.Now(),
            },
            wantErr: true,
        },
    }
    
    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            err := tt.msg.Validate()
            if (err != nil) != tt.wantErr {
                t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
            }
        })
    }
}
```

---

## 5. NATSBroker Documentation (MEDIUM PRIORITY)

**Location**: `internal/ipc/nats_broker.go`, NATSBroker type definition

**Problem**: NATSBroker is designed for a single server instance reading SubscribeOutput. Multiple instances will cause message distribution, which is not documented.

**Solution**: Add a clarifying comment.

```go
// NATSBroker implements IPCBroker using NATS JetStream.
//
// Design assumptions:
// - Single server instance: NATSBroker is designed for a single Kraclaw server
//   instance per NATS cluster. If multiple server instances call SubscribeOutput
//   for the same group, messages will be distributed across instances (load-balancing
//   effect), which is usually undesired for agent command/output communication.
//   For multi-instance deployments, implement Kafka-style consumer groups or use
//   a dedicated message broker (e.g., RabbitMQ with exclusive queues).
//
// Per-group stream topology:
//   - Stream name: KRACLAW_IPC_{sanitized_group_hash}
//   - Input subject:  kraclaw.ipc.{sanitized}.{agent_id}.input
//   - Output subject: kraclaw.ipc.{sanitized}.{agent_id}.output
//   - Server subscribes to wildcard: kraclaw.ipc.{sanitized}.*.output
//
// Consumer configuration:
//   - Output (server): durable="kraclaw-server", filter=wildcard, DeliverAll, AckExplicit
//   - Input (agent): durable="agent-{sanitized_agent_id}", filter=agent-specific, DeliverAll, AckExplicit
//
// Message flow:
//   - SendInput: server -> agent (routed to agent-specific input subject)
//   - PublishOutput: agent -> server (routed to agent-specific output subject)
//   - SubscribeOutput: server receives all agent outputs via wildcard consumer
//   - ReadInput: agent receives messages directed to it (agent-specific consumer)
type NATSBroker struct {
    nc     *nats.Conn
    js     jetstream.JetStream
    logger *slog.Logger

    mu       sync.Mutex
    cancels  []context.CancelFunc
    iters    []jetstream.MessagesContext
    closed   bool
    closedCh chan struct{}
}
```

---

## 6. NATSQueue Shutdown Guard (LOW PRIORITY)

**Location**: `internal/queue/nats_queue.go`

**Problem**: Enqueue/Dequeue/Peek do not check if the queue is closed. Could produce cryptic errors.

**Solution**: Add defensive checks (optional).

```go
// Enqueue adds a message to the group's JetStream queue.
func (q *NATSQueue) Enqueue(ctx context.Context, groupJID string, msg *QueueMessage) error {
    q.mu.Lock()
    if q.closed {
        q.mu.Unlock()
        return fmt.Errorf("nats queue: closed")
    }
    q.mu.Unlock()
    
    sanitized, err := q.ensureStream(ctx, groupJID)
    if err != nil {
        return fmt.Errorf("enqueue ensure stream: %w", err)
    }
    // ... rest of implementation ...
}

// Dequeue removes and returns the oldest message from the group's queue.
func (q *NATSQueue) Dequeue(ctx context.Context, groupJID string) (*QueueMessage, error) {
    q.mu.Lock()
    if q.closed {
        q.mu.Unlock()
        return nil, fmt.Errorf("nats queue: closed")
    }
    q.mu.Unlock()
    
    // ... rest of implementation ...
}
```

**Non-Breaking**: Adds error cases that would have failed anyway; improves error messages.

---

## 7. NATSBroker/NATSQueue HealthCheck (LOW PRIORITY, FUTURE)

**Location**: `internal/ipc/nats_broker.go` and `internal/queue/nats_queue.go`

**Problem**: No way to verify that broker/queue is healthy and can reach NATS.

**Solution**: Add optional HealthCheck methods (useful for readiness/liveness probes).

```go
// NATSBroker.HealthCheck verifies the broker is operational.
// Returns error if broker is closed or NATS is unreachable.
func (b *NATSBroker) HealthCheck(ctx context.Context) error {
    select {
    case <-b.closedCh:
        return fmt.Errorf("nats broker: closed")
    default:
    }
    
    b.mu.Lock()
    if b.closed {
        b.mu.Unlock()
        return fmt.Errorf("nats broker: closed")
    }
    b.mu.Unlock()
    
    // Attempt a minimal stream lookup to verify NATS connectivity.
    // This will fail with ErrStreamNotFound if NATS is up, which is OK.
    _, err := b.js.Stream(ctx, "nonexistent_test_stream")
    if err != nil && !errors.Is(err, jetstream.ErrStreamNotFound) {
        return fmt.Errorf("nats broker: jetstream unavailable: %w", err)
    }
    return nil
}

// NATSQueue.HealthCheck verifies the queue is operational.
func (q *NATSQueue) HealthCheck(ctx context.Context) error {
    q.mu.Lock()
    if q.closed {
        q.mu.Unlock()
        return fmt.Errorf("nats queue: closed")
    }
    q.mu.Unlock()
    
    // Attempt a minimal stream lookup.
    _, err := q.js.Stream(ctx, "nonexistent_test_stream")
    if err != nil && !errors.Is(err, jetstream.ErrStreamNotFound) {
        return fmt.Errorf("nats queue: jetstream unavailable: %w", err)
    }
    return nil
}
```

**Usage** (in server startup or liveness probe):
```go
if err := broker.HealthCheck(ctx); err != nil {
    return fmt.Errorf("ipc broker not ready: %w", err)
}
if err := queue.HealthCheck(ctx); err != nil {
    return fmt.Errorf("queue not ready: %w", err)
}
```

---

## 8. QueueMessage.Len() Documentation (LOW PRIORITY)

**Location**: `internal/queue/nats_queue.go`, Len() method

**Problem**: Len() is eventual-consistent but callers might use it for synchronization.

**Solution**: Add documentation.

```go
// Len returns the approximate number of pending messages in a group's queue.
// The value is eventual-consistent and may lag the actual message count.
// Do not use for synchronization (e.g., spinning on Len() == 0).
// Suitable for metrics and observability.
func (q *NATSQueue) Len(ctx context.Context, groupJID string) (int64, error) {
    // ... implementation ...
}
```

---

## Summary of Changes

| Priority | File | Change | LOC | Breaking? |
|----------|------|--------|-----|-----------|
| 🔴 HIGH | `ipc/ipc.go` | Add NewIPCMessage() constructor | +25 | No |
| 🔴 HIGH | `ipc/ipc.go` | Document IPCMessage.ID field | +5 | No |
| 🔴 HIGH | `ipc/nats_broker.go` | Document SubscribeOutput contract | +10 | No |
| 🟡 MEDIUM | `queue/queue.go` | Add NewQueueMessage() + NewQueueTaskMessage() | +30 | No |
| 🟡 MEDIUM | `queue/queue.go` | Add Validate() method | +15 | No |
| 🟡 MEDIUM | `ipc/nats_broker.go` | Document NATSBroker design assumptions | +15 | No |
| 🟢 LOW | `queue/nats_queue.go` | Add shutdown checks to Enqueue/Dequeue | +15 | No |
| 🟢 LOW | `ipc/nats_broker.go` | Add HealthCheck() method | +15 | No |
| 🟢 LOW | `queue/nats_queue.go` | Add HealthCheck() method | +10 | No |
| 🟢 LOW | `queue/nats_queue.go` | Document Len() eventual-consistency | +3 | No |

**Total LOC**: ~143 lines of improvements, all non-breaking.

---

## Testing Strategy

After implementing the above changes, ensure:

1. Existing tests continue to pass (all changes are backward-compatible)
2. Add tests for new constructors (see suggestions above)
3. Add test for NewIPCMessage validation
4. Add test for QueueMessage.Validate()
5. Existing code continues to use struct literals without modification (optional; constructors are additive)

Example test run:
```bash
make test-short  # Should pass with 0 failures
```

---

## Implementation Order

1. **Start with HIGH priority** (IPCMessage constructor + documentation)
   - Low complexity, high value
   - No test changes required
   
2. **Add MEDIUM priority** (QueueMessage constructors + documentation)
   - Similar complexity to HIGH
   - Requires new tests
   
3. **Add LOW priority features** (HealthCheck, defensive checks)
   - Nice-to-have
   - Can be deferred to future PR if time is constrained

---

## Notes

- All changes are **non-breaking**: existing struct literal code continues to work
- Constructors are **optional**: code can mix constructor calls and struct literals
- Documentation changes are **critical**: even without constructor implementation, documenting SubscribeOutput contract and NATSBroker design is essential

See **PR23-TYPE-ANALYSIS.md** for full analysis and rationale.
