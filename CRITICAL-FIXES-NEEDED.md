# Critical Fixes Required for PR #23

## Fix 1: Unique Consumer Names per Group (CRITICAL)

### File: `internal/ipc/nats_broker.go`

**Current Code (Line 19):**
```go
const (
	ipcStreamMaxAge = time.Hour
	ipcServerConsumer = "kraclaw-server"
)
```

**Change to:**
```go
const (
	ipcStreamMaxAge = time.Hour
	ipcServerConsumer = "kraclaw-server" // Keep for backwards compatibility
)

// NEW: Unique consumer name per group
func ipcServerConsumerName(sanitized string) string {
	return "kraclaw-server-" + sanitized
}
```

---

**Current Code (Lines 143-149):**
```go
cons, err := b.js.CreateOrUpdateConsumer(ctx, streamName, jetstream.ConsumerConfig{
	Durable:       ipcServerConsumer,
	FilterSubject: ipcOutputWildcard(sanitized),
	DeliverPolicy: jetstream.DeliverAllPolicy,
	AckPolicy:     jetstream.AckExplicitPolicy,
})
```

**Change to:**
```go
cons, err := b.js.CreateOrUpdateConsumer(ctx, streamName, jetstream.ConsumerConfig{
	Durable:       ipcServerConsumerName(sanitized), // Use unique name per group
	FilterSubject: ipcOutputWildcard(sanitized),
	DeliverPolicy: jetstream.DeliverAllPolicy,
	AckPolicy:     jetstream.AckExplicitPolicy,
})
```

---

**Also update ReadInput (Lines 167-171) for consistency:**
```go
// Current (leave agent name as-is):
Durable:       "agent-" + SanitizeAgentID(agentID),
// This is already per-agent, so no change needed
```

---

## Fix 2: Remove Promise.race() from Agent IPC (CRITICAL)

### File: `agent/src/ipc.ts`

**Replace lines 142-224 with:**

```typescript
// readInput reads the next input message
async readInput(): Promise<IPCMessage | null> {
  if (!this.jsClient || !this.jsManager) {
    throw new Error("jetstream client not initialized - call connect() first");
  }

  await this.ensureStream();

  const streamName = this.streamName();
  const consumerName = "agent-" + sanitizeAgentID(this.agentID);

  // Create or update durable consumer with DeliverAllPolicy
  try {
    // Try to get existing consumer
    await this.jsManager.consumers.info(streamName, consumerName);
  } catch {
    // Create new consumer if it doesn't exist
    try {
      await this.jsManager.consumers.add(streamName, {
        durable_name: consumerName,
        filter_subject: this.inputSubject(),
        deliver_policy: DeliverPolicy.All,
        ack_policy: AckPolicy.Explicit,
      });
    } catch (createErr) {
      // Race condition: consumer was created by another agent instance
      if (
        !(
          String(createErr).includes("consumer already exists") ||
          String(createErr).includes("CONSUMER_EXISTS")
        )
      ) {
        throw createErr;
      }
    }
  }

  const sub = await this.jsClient.subscribe(this.inputSubject(), {
    config: {
      durable_name: consumerName,
    },
  });

  try {
    // Use for-await loop (no Promise.race)
    for await (const jmsg of sub) {
      try {
        // Parse the message
        const data = new TextDecoder().decode(jmsg.data);
        const parsed = JSON.parse(data);

        const msg: IPCMessage = {
          group: this.groupJID,
          type: parsed.type,
          payload: parsed.payload || {},
        };

        // Explicitly acknowledge the message
        if (jmsg.ack) {
          await jmsg.ack();
        }

        return msg; // Return immediately on first message
      } catch (parseErr) {
        // Log and skip malformed messages
        console.error(`Failed to parse message: ${parseErr}`);
        if (jmsg.nak) {
          try {
            await jmsg.nak();
          } catch (nakErr) {
            console.error(`Failed to NAK unparseable message: ${nakErr}`);
          }
        }
        // Continue to next message
      }
    }
    // Iterator ended (subscription closed)
    return null;
  } finally {
    try {
      await sub.unsubscribe();
    } catch (err) {
      console.error(`Failed to unsubscribe: ${err}`);
    }
  }
}
```

---

## Fix 3: Reuse Consumer in Queue Dequeue (CRITICAL)

### File: `internal/queue/nats_queue.go`

**Add to NATSQueue struct (after `mu` field, line ~41):**
```go
	mu       sync.Mutex
	cancels  []context.CancelFunc
	closed   bool
	closedCh chan struct{}

	// NEW: Cache dequeue consumers per group
	dequeueMu           sync.Mutex
	dequeueConsumers    map[string]jetstream.Consumer // sanitized -> consumer
}
```

---

**Update NewNATSQueue (line ~68):**
```go
	return &NATSQueue{
		nc:                  nc,
		js:                  js,
		gas:                 gas,
		logger:              logger,
		closedCh:            make(chan struct{}),
		dequeueConsumers:    make(map[string]jetstream.Consumer), // NEW
	}, nil
```

---

**Add new method after ensureStream (around line ~87):**
```go
// getOrCreateDequeueConsumer returns the durable dequeue consumer for a group.
// The consumer is created once and reused for all Dequeue calls.
func (q *NATSQueue) getOrCreateDequeueConsumer(ctx context.Context, sanitized string) (jetstream.Consumer, error) {
	q.dequeueMu.Lock()
	defer q.dequeueMu.Unlock()

	// Return cached consumer if it exists
	if cons, ok := q.dequeueConsumers[sanitized]; ok {
		return cons, nil
	}

	// Create consumer (only once per group)
	cons, err := q.js.CreateOrUpdateConsumer(ctx, queueStreamName(sanitized), jetstream.ConsumerConfig{
		Durable:       "dequeue-" + sanitized,
		DeliverPolicy: jetstream.DeliverAllPolicy, // Deliver all pending, not just new
		AckPolicy:     jetstream.AckExplicitPolicy,
	})
	if err != nil {
		return nil, fmt.Errorf("create dequeue consumer: %w", err)
	}

	q.dequeueConsumers[sanitized] = cons
	return cons, nil
}
```

---

**Replace Dequeue method (lines 106-153) with:**
```go
// Dequeue removes and returns the oldest message from the group's queue.
// Returns nil, nil when the queue is empty.
func (q *NATSQueue) Dequeue(ctx context.Context, groupJID string) (*QueueMessage, error) {
	sanitized, err := q.ensureStream(ctx, groupJID)
	if err != nil {
		return nil, fmt.Errorf("dequeue ensure stream: %w", err)
	}
	
	// Get or create the durable consumer (reused for all Dequeue calls)
	cons, err := q.getOrCreateDequeueConsumer(ctx, sanitized)
	if err != nil {
		return nil, fmt.Errorf("get dequeue consumer: %w", err)
	}

	msgs, err := cons.Fetch(1, jetstream.FetchMaxWait(queueFetchTimeout))
	if err != nil {
		return nil, fmt.Errorf("fetch queue message: %w", err)
	}
	for msg := range msgs.Messages() {
		var qm QueueMessage
		if err := json.Unmarshal(msg.Data(), &qm); err != nil {
			meta, _ := msg.Metadata()
			var seq uint64
			if meta != nil {
				seq = meta.Sequence.Stream
			}
			q.logger.Warn("dequeue: malformed message payload — acking to discard",
				"subject", msg.Subject(),
				"sequence", seq,
				"raw", string(msg.Data()))
			if err := msg.Ack(); err != nil {
				q.logger.Error("ack malformed queue message", "subject", msg.Subject(), "sequence", seq, "error", err)
			}
			return nil, fmt.Errorf("unmarshal queue message: %w", err)
		}
		if err := msg.Ack(); err != nil {
			return nil, fmt.Errorf("dequeue: ack: %w", err)
		}
		return &qm, nil
	}
	if err := msgs.Error(); err != nil && !errors.Is(err, jetstream.ErrMsgIteratorClosed) {
		// Timeout and no-messages are expected for empty queue — don't error.
		if !errors.Is(err, nats.ErrTimeout) && !errors.Is(err, jetstream.ErrNoMessages) {
			return nil, fmt.Errorf("fetch error: %w", err)
		}
	}
	return nil, nil // empty queue
}
```

---

**Update Close() method (line ~263):**
```go
// Close stops all background goroutines.
func (q *NATSQueue) Close() error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return nil
	}
	q.closed = true
	for _, cancel := range q.cancels {
		cancel()
	}
	q.cancels = nil
	
	// NEW: Clear cached consumers
	q.dequeueMu.Lock()
	q.dequeueConsumers = make(map[string]jetstream.Consumer)
	q.dequeueMu.Unlock()
	
	close(q.closedCh)
	return nil
}
```

---

## Fix 4: Cleanup on consume() Failure (HIGH)

### File: `internal/ipc/nats_broker.go`

**Update SubscribeOutput (lines 133-156):**
```go
// SubscribeOutput returns a channel that receives output from all agents in
// the group via a durable wildcard push consumer.
func (b *NATSBroker) SubscribeOutput(ctx context.Context, group string) (<-chan *IPCMessage, error) {
	sanitized, err := b.ensureStream(ctx, group)
	if err != nil {
		return nil, fmt.Errorf("subscribe output: %w", err)
	}
	streamName := ipcStreamName(sanitized)
	cons, err := b.js.CreateOrUpdateConsumer(ctx, streamName, jetstream.ConsumerConfig{
		Durable:       ipcServerConsumerName(sanitized), // Use unique name
		FilterSubject: ipcOutputWildcard(sanitized),
		DeliverPolicy: jetstream.DeliverAllPolicy,
		AckPolicy:     jetstream.AckExplicitPolicy,
	})
	if err != nil {
		return nil, fmt.Errorf("create output consumer: %w", err)
	}
	ch, err := b.consume(ctx, cons, group)
	if err != nil {
		// NEW: Clean up the consumer if consume() fails
		if delErr := b.js.DeleteConsumer(ctx, streamName, ipcServerConsumerName(sanitized)); delErr != nil && !errors.Is(delErr, jetstream.ErrConsumerNotFound) {
			b.logger.Warn("failed to delete consumer after consume() failure", "error", delErr)
		}
		return nil, fmt.Errorf("consume output: %w", err)
	}
	return ch, nil
}
```

---

**Update ReadInput (lines 161-178) similarly:**
```go
// ReadInput returns a channel that receives input messages for agent.
func (b *NATSBroker) ReadInput(ctx context.Context, group, agentID string) (<-chan *IPCMessage, error) {
	sanitized, err := b.ensureStream(ctx, group)
	if err != nil {
		return nil, fmt.Errorf("read input: %w", err)
	}
	streamName := ipcStreamName(sanitized)
	cons, err := b.js.CreateOrUpdateConsumer(ctx, streamName, jetstream.ConsumerConfig{
		Durable:       "agent-" + SanitizeAgentID(agentID),
		FilterSubject: ipcInputSubject(sanitized, agentID),
		DeliverPolicy: jetstream.DeliverAllPolicy,
		AckPolicy:     jetstream.AckExplicitPolicy,
	})
	if err != nil {
		return nil, fmt.Errorf("create input consumer: %w", err)
	}

	ch, err := b.consume(ctx, cons, group)
	if err != nil {
		// NEW: Clean up the consumer if consume() fails
		if delErr := b.js.DeleteConsumer(ctx, streamName, "agent-"+SanitizeAgentID(agentID)); delErr != nil && !errors.Is(delErr, jetstream.ErrConsumerNotFound) {
			b.logger.Warn("failed to delete consumer after consume() failure", "error", delErr)
		}
		return nil, fmt.Errorf("consume input: %w", err)
	}
	return ch, nil
}
```

---

## Testing Requirements

Add to `internal/ipc/nats_broker_test.go`:

```go
func TestMultiGroupSubscribeOutput(t *testing.T) {
	// Test that multiple groups don't interfere with each other's consumers
	broker, nc := setupBroker(t)
	defer broker.Close()
	defer nc.Close()

	ctx := context.Background()

	// Subscribe group 1
	ch1, err := broker.SubscribeOutput(ctx, "group1")
	if err != nil {
		t.Fatalf("SubscribeOutput group1: %v", err)
	}

	// Subscribe group 2
	ch2, err := broker.SubscribeOutput(ctx, "group2")
	if err != nil {
		t.Fatalf("SubscribeOutput group2: %v", err)
	}

	// Send message to group 1
	msg1 := &IPCMessage{
		Type:    IPCMessageText,
		AgentID: "agent1",
		Payload: json.RawMessage(`{"text":"hello1"}`),
	}
	if err := broker.PublishOutput(ctx, "group1", "agent1", msg1); err != nil {
		t.Fatalf("PublishOutput group1: %v", err)
	}

	// Send message to group 2
	msg2 := &IPCMessage{
		Type:    IPCMessageText,
		AgentID: "agent2",
		Payload: json.RawMessage(`{"text":"hello2"}`),
	}
	if err := broker.PublishOutput(ctx, "group2", "agent2", msg2); err != nil {
		t.Fatalf("PublishOutput group2: %v", err)
	}

	// Verify group 1 receives its message
	select {
	case got := <-ch1:
		if got.AgentID != "agent1" {
			t.Errorf("ch1 got agent_id=%q, want agent1", got.AgentID)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ch1 timeout - group1 message not received!")
	}

	// Verify group 2 receives its message (not group 1's)
	select {
	case got := <-ch2:
		if got.AgentID != "agent2" {
			t.Errorf("ch2 got agent_id=%q, want agent2", got.AgentID)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ch2 timeout - group2 message not received!")
	}
}
```

---

## Verification Steps

1. **Fix 1**: Verify with `TestMultiGroupSubscribeOutput` test
2. **Fix 2**: Run agent against local NATS for 10+ readInput cycles, verify no hangs
3. **Fix 3**: Enqueue 10 messages, Dequeue once, wait 30s, Dequeue again, verify all 10 received
4. **Fix 4**: Inject error into consume(), verify no orphaned consumers in NATS

---

## PR Merge Criteria

- [ ] All 4 fixes applied
- [ ] All existing tests pass
- [ ] New MultiGroupSubscribeOutput test passes
- [ ] Load test with 5+ concurrent groups for 5 minutes shows no message loss
- [ ] Agent test with 100 readInput cycles shows no hangs
- [ ] NATS metrics show consumer count = number of active groups (no orphans)
