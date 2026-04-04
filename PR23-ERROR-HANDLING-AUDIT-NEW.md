# PR #23 Error Handling Audit (Supplemental)

## Overview

This audit supplements the existing `PR23-ERROR-HANDLING-AUDIT.md` with additional issues discovered during systematic review of the NATS JetStream migration. This focuses on issues NOT yet documented in the original audit.

---

## New Critical Issues

### CRITICAL-A1: Promise.race() Leaves Cleanup Tasks Running in Agent IPC

**Location:** `/home/vince/repos/johanssonvincent/kraclaw/agent/src/ipc.ts` lines 188-217

**Severity:** CRITICAL

**Issue:**
The `readInput()` method uses `Promise.race([msgPromise, timeout])` where timeout resolves to `null` after 5 seconds. When timeout wins the race, the `msgPromise` async iterator continues running in the background even though the function returns.

```typescript
// Lines 188-217 - PROBLEMATIC
const timeout = new Promise<null>((resolve) => {
  setTimeout(() => resolve(null), 5000);
});

const msgPromise = (async () => {
  for await (const jmsg of sub) {
    // ... parse and ack ...
    return msg;
  }
  return null;
})();

// Race between message arrival and timeout
return await Promise.race([msgPromise, timeout]); // returns null on timeout
// BUT msgPromise is still running! The for-await loop didn't exit.
```

After the race resolves, the `finally` block runs (`await sub.unsubscribe()`), but the `msgPromise` async iterator's `for-await` loop is still blocked on `sub` trying to read the next message. Unsubscribe happens WHILE the iterator is potentially in the middle of processing.

**Hidden Errors:**
1. **Subscription leak on timeout**: When timeout fires and function returns, the for-await loop is abandoned but still holds the subscription reference
2. **Race condition on unsubscribe**: The `sub.unsubscribe()` in finally races with the for-await loop still trying to iterate
3. **Message ACK loss**: If a message arrives between timeout firing and unsubscribe, that ACK is never sent (message will be redelivered)
4. **Iterator termination error**: The for-await loop may throw when unsubscribed mid-iteration, but that error is lost (msgPromise is unobserved)

**User Impact:**
- Agent appears responsive (5s timeout returns null), but internally subscription/iterator state is corrupted
- Next call to `readInput()` may hang or behave unpredictably due to iterator state corruption
- Messages could be duplicated (delivered again because ACK was lost)
- Over time, the NATS connection accumulates orphaned subscriptions
- Agent becomes unresponsive after several timeout cycles

**Root Cause:**
The async for-await loop in `msgPromise` cannot be "cancelled" from outside. Once started, it runs to completion or until an error. Using `Promise.race()` abandons the promise without stopping its background work.

**Recommendation:**
Implement explicit subscription management with timeout handling:

```typescript
async readInput(): Promise<IPCMessage | null> {
  if (!this.jsClient || !this.jsManager) {
    throw new Error("jetstream client not initialized - call connect() first");
  }

  await this.ensureStream();

  const streamName = this.streamName();
  const consumerName = "agent-" + sanitizeAgentID(this.agentID);

  // Create or update consumer
  try {
    await this.jsManager.consumers.info(streamName, consumerName);
  } catch {
    try {
      await this.jsManager.consumers.add(streamName, {
        durable_name: consumerName,
        filter_subject: this.inputSubject(),
        deliver_policy: DeliverPolicy.All,
        ack_policy: AckPolicy.Explicit,
      });
    } catch (createErr) {
      if (
        !String(createErr).includes("consumer already exists") &&
        !String(createErr).includes("CONSUMER_EXISTS")
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
    let firstMessage = true;
    
    for await (const jmsg of sub) {
      try {
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

        return msg;
      } catch (parseErr) {
        console.error(`Failed to parse message: ${parseErr}`);
        // NAK to skip malformed message
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
    // Iterator ended without a message (shouldn't happen with durable consumer)
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

This implementation:
- Does NOT use `Promise.race()` - avoids abandoning the iterator
- Processes messages from the for-await loop until one is successfully parsed and ACK'd
- Guarantees cleanup via try-finally with explicit unsubscribe
- Returns null only after the iterator naturally ends (no forced timeout)

---

### CRITICAL-A2: Consumer Created Twice Per Dequeue Call

**Location:** `/home/vince/repos/johanssonvincent/kraclaw/internal/queue/nats_queue.go` lines 106-125

**Severity:** CRITICAL

**Issue:**
Every `Dequeue()` call creates or updates the same durable consumer `dequeue-{sanitized}`. This is inefficient and creates a subtle race condition:

```go
func (q *NATSQueue) Dequeue(ctx context.Context, groupJID string) (*QueueMessage, error) {
	sanitized, err := q.ensureStream(ctx, groupJID)
	if err != nil {
		return nil, fmt.Errorf("dequeue ensure stream: %w", err)
	}
	streamName := queueStreamName(sanitized)
	
	// LINE 112-115: CreateOrUpdateConsumer called on EVERY Dequeue
	cons, err := q.js.CreateOrUpdateConsumer(ctx, streamName, jetstream.ConsumerConfig{
		Durable:   "dequeue-" + sanitized,
		AckPolicy: jetstream.AckExplicitPolicy,
	})
	if err != nil {
		return nil, fmt.Errorf("create dequeue consumer: %w", err)
	}
	
	// LINE 120: Fetch 1 message with 200ms timeout
	msgs, err := cons.Fetch(1, jetstream.FetchMaxWait(queueFetchTimeout))
	if err != nil {
		return nil, fmt.Errorf("fetch queue message: %w", err)
	}
	// ... process message ...
}
```

Problems:

1. **CreateOrUpdateConsumer on every call**: The consumer already exists from the previous Dequeue call. This causes NATS to verify/update the consumer on every dequeue operation.
2. **No DeliverPolicy specified**: The consumer config doesn't specify `DeliverPolicy`. For a durable consumer with no existing state, NATS defaults to `DeliverNewPolicy`, which means messages missed between Dequeue calls are skipped.
3. **Fetch timeout only 200ms**: The fetch has a hardcoded 200ms timeout. If there's network jitter or NATS load, valid messages are missed.
4. **Race between Dequeue and group start**: If multiple `Dequeue()` calls race to create the consumer, they may interfere with each other's delivery state.

**Hidden Errors:**
1. **Message loss**: Messages enqueued between Dequeue calls with no active consumer attachment could be skipped
2. **Performance regression**: Repeated CreateOrUpdateConsumer calls on every Dequeue adds network roundtrips to NATS
3. **Inconsistent consumer state**: If two Dequeue goroutines run concurrently, they compete for the same consumer, causing unpredictable delivery

**User Impact:**
- Queued messages disappear silently without being processed
- Scheduled tasks never execute if no Dequeue call is active
- Performance degrades with many Dequeue calls (each adds latency)
- Race conditions in concurrent scenarios (though orchestrator typically calls Dequeue single-threaded)

**Root Cause:**
Dequeue was migrated from Redis where each operation was independent. In NATS JetStream, durable consumers should be created once and reused, not recreated on every fetch.

**Recommendation:**
Create the consumer once during queue initialization, not on every Dequeue:

```go
// In NATSQueue struct, add:
type NATSQueue struct {
	nc     *nats.Conn
	js     jetstream.JetStream
	gas    groupActiveStore
	logger *slog.Logger

	// NEW: Per-group dequeue consumers (once per group)
	dequeueMu       sync.Mutex
	dequeueConsumers map[string]jetstream.Consumer

	mu       sync.Mutex
	cancels  []context.CancelFunc
	closed   bool
	closedCh chan struct{}
}

// NEW method: getOrCreateDequeueConsumer
func (q *NATSQueue) getOrCreateDequeueConsumer(ctx context.Context, sanitized string) (jetstream.Consumer, error) {
	q.dequeueMu.Lock()
	defer q.dequeueMu.Unlock()

	// Return existing consumer
	if cons, ok := q.dequeueConsumers[sanitized]; ok {
		return cons, nil
	}

	// Create consumer (only once per group)
	cons, err := q.js.CreateOrUpdateConsumer(ctx, queueStreamName(sanitized), jetstream.ConsumerConfig{
		Durable:       "dequeue-" + sanitized,
		DeliverPolicy: jetstream.DeliverAllPolicy, // Deliver all pending messages
		AckPolicy:     jetstream.AckExplicitPolicy,
	})
	if err != nil {
		return nil, fmt.Errorf("create dequeue consumer: %w", err)
	}

	q.dequeueConsumers[sanitized] = cons
	return cons, nil
}

// UPDATED Dequeue method
func (q *NATSQueue) Dequeue(ctx context.Context, groupJID string) (*QueueMessage, error) {
	sanitized, err := q.ensureStream(ctx, groupJID)
	if err != nil {
		return nil, fmt.Errorf("dequeue ensure stream: %w", err)
	}

	// Get or create consumer (only creates once per group)
	cons, err := q.getOrCreateDequeueConsumer(ctx, sanitized)
	if err != nil {
		return nil, fmt.Errorf("get dequeue consumer: %w", err)
	}

	// Now fetch from the consumer (no CreateOrUpdateConsumer here)
	msgs, err := cons.Fetch(1, jetstream.FetchMaxWait(queueFetchTimeout))
	if err != nil {
		return nil, fmt.Errorf("fetch queue message: %w", err)
	}
	// ... rest of method unchanged ...
}

// Update Close() to clean up consumers
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

	// Clean up dequeue consumers
	q.dequeueMu.Lock()
	q.dequeueConsumers = make(map[string]jetstream.Consumer)
	q.dequeueMu.Unlock()

	close(q.closedCh)
	return nil
}
```

---

### CRITICAL-A3: SubscribeOutput Consumer Created on Every Call Without Durable Check

**Location:** `/home/vince/repos/johanssonvincent/kraclaw/internal/ipc/nats_broker.go` lines 133-156

**Severity:** CRITICAL

**Issue:**
The `SubscribeOutput()` method is called once per group during orchestrator startup, creating a durable consumer named `kraclaw-server`. However, if the orchestrator restarts while a watchGroupOutput goroutine is still holding the channel, the new SubscribeOutput call will attempt to create/update the SAME durable consumer while the old one is still active.

```go
func (b *NATSBroker) SubscribeOutput(ctx context.Context, group string) (<-chan *IPCMessage, error) {
	sanitized, err := b.ensureStream(ctx, group)
	if err != nil {
		return nil, fmt.Errorf("subscribe output: %w", err)
	}
	streamName := ipcStreamName(sanitized)
	
	// LINE 143-149: CreateOrUpdateConsumer with FIXED durable name
	cons, err := b.js.CreateOrUpdateConsumer(ctx, streamName, jetstream.ConsumerConfig{
		Durable:       ipcServerConsumer, // "kraclaw-server" - FIXED for all groups!
		FilterSubject: ipcOutputWildcard(sanitized),
		DeliverPolicy: jetstream.DeliverAllPolicy,
		AckPolicy:     jetstream.AckExplicitPolicy,
	})
	if err != nil {
		return nil, fmt.Errorf("create output consumer: %w", err)
	}
	ch, err := b.consume(ctx, cons, group)
	if err != nil {
		return nil, fmt.Errorf("consume output: %w", err)
	}
	return ch, nil
}
```

**The Fatal Flaw:**
The durable consumer name `ipcServerConsumer` ("kraclaw-server") is THE SAME for all groups! This means:

1. Group "foo" calls SubscribeOutput → creates consumer "kraclaw-server" with filter `kraclaw.ipc.{foo_sanitized}.*.output`
2. Group "bar" calls SubscribeOutput → calls CreateOrUpdateConsumer with same name "kraclaw-server" BUT different filter `kraclaw.ipc.{bar_sanitized}.*.output`
3. NATS updates the existing consumer's filter, now it only watches "bar" output
4. Group "foo"'s watchGroupOutput goroutine is still holding the old consumer handle, but its filter was just changed
5. Group "foo" messages go undelivered; watchGroupOutput hangs waiting for messages that will never arrive on that consumer

**Hidden Errors:**
1. **Silent message loss**: Messages from groups other than the most-recently subscribed group are lost
2. **Consumer state corruption**: Multiple groups competing for the same durable consumer
3. **Subscription blockage**: watchGroupOutput goroutines hang forever waiting for messages that won't arrive
4. **Deadlock cascade**: Groups starve for messages, orchestrator marks them inactive, operations fail silently

**Example Failure Scenario:**
```
1. Start orchestrator, group "sales" subscribes
   Consumer "kraclaw-server" watches kraclaw.ipc.{sales_hash}.*.output ✓
2. Start orchestrator, group "support" subscribes
   CreateOrUpdateConsumer updates "kraclaw-server" to watch kraclaw.ipc.{support_hash}.*.output
   "sales" consumer filter is now WRONG ✗
3. Agent in "sales" group sends message
   Message goes to kraclaw.ipc.{sales_hash}.agent1.output
   Consumer "kraclaw-server" is filtering for {support_hash} ✗
   watchGroupOutput("sales") hangs forever
4. "sales" group times out, marked inactive, user sees hung group
```

**Recommendation:**
Use per-group durable consumer names:

```go
const ipcServerConsumer = "kraclaw-server" // Keep for backwards compatibility

// NEW: Create unique consumer name per group
func ipcServerConsumerName(sanitized string) string {
	return "kraclaw-server-" + sanitized
}

func (b *NATSBroker) SubscribeOutput(ctx context.Context, group string) (<-chan *IPCMessage, error) {
	sanitized, err := b.ensureStream(ctx, group)
	if err != nil {
		return nil, fmt.Errorf("subscribe output: %w", err)
	}
	streamName := ipcStreamName(sanitized)
	
	cons, err := b.js.CreateOrUpdateConsumer(ctx, streamName, jetstream.ConsumerConfig{
		Durable:       ipcServerConsumerName(sanitized), // UNIQUE per group!
		FilterSubject: ipcOutputWildcard(sanitized),
		DeliverPolicy: jetstream.DeliverAllPolicy,
		AckPolicy:     jetstream.AckExplicitPolicy,
	})
	if err != nil {
		return nil, fmt.Errorf("create output consumer: %w", err)
	}
	ch, err := b.consume(ctx, cons, group)
	if err != nil {
		return nil, fmt.Errorf("consume output: %w", err)
	}
	return ch, nil
}
```

---

## High-Priority Issues

### HIGH-A1: Orphaned IPC Consumer When consume() Fails After CreateOrUpdateConsumer

**Location:** `/home/vince/repos/johanssonvincent/kraclaw/internal/ipc/nats_broker.go` lines 137-151 and 161-178

**Severity:** HIGH

**Issue:**
When `SubscribeOutput()` or `ReadInput()` succeeds in creating a consumer but fails in the subsequent `consume()` call, the consumer is left behind in NATS with no goroutine consuming it.

```go
func (b *NATSBroker) SubscribeOutput(ctx context.Context, group string) (<-chan *IPCMessage, error) {
	// ...
	cons, err := b.js.CreateOrUpdateConsumer(ctx, streamName, jetstream.ConsumerConfig{
		Durable:       ipcServerConsumer,
		FilterSubject: ipcOutputWildcard(sanitized),
		DeliverPolicy: jetstream.DeliverAllPolicy,
		AckPolicy:     jetstream.AckExplicitPolicy,
	})
	if err != nil {
		return nil, fmt.Errorf("create output consumer: %w", err)
	}
	
	// LINE 149-151: If consume() fails here, consumer is orphaned
	ch, err := b.consume(ctx, cons, group)
	if err != nil {
		return nil, fmt.Errorf("consume output: %w", err)  // Consumer left behind!
	}
	return ch, nil
}
```

The `consume()` function can fail at line 240 when calling `iter.Stop()` or at line 242 if `b.closed` is true. In both cases, the created consumer is never deleted.

**Hidden Errors:**
- Orphaned consumers accumulate in NATS, consuming memory
- Stale consumers may interfere with future subscribe attempts
- On orchestrator restart with same groups, old consumers from crashed attempts may deliver stale messages

**User Impact:**
- Memory leak in NATS JetStream infrastructure
- Over time, NATS performance degrades
- Potential message delivery anomalies from stale consumers

**Recommendation:**
Delete the consumer if consume() fails:

```go
func (b *NATSBroker) SubscribeOutput(ctx context.Context, group string) (<-chan *IPCMessage, error) {
	sanitized, err := b.ensureStream(ctx, group)
	if err != nil {
		return nil, fmt.Errorf("subscribe output: %w", err)
	}
	streamName := ipcStreamName(sanitized)
	cons, err := b.js.CreateOrUpdateConsumer(ctx, streamName, jetstream.ConsumerConfig{
		Durable:       ipcServerConsumerName(sanitized), // Use per-group name
		FilterSubject: ipcOutputWildcard(sanitized),
		DeliverPolicy: jetstream.DeliverAllPolicy,
		AckPolicy:     jetstream.AckExplicitPolicy,
	})
	if err != nil {
		return nil, fmt.Errorf("create output consumer: %w", err)
	}
	
	ch, err := b.consume(ctx, cons, group)
	if err != nil {
		// Clean up the consumer if consume() fails
		if delErr := b.js.DeleteConsumer(ctx, streamName, ipcServerConsumerName(sanitized)); delErr != nil {
			b.logger.Warn("failed to cleanup consumer after consume() failure", 
				"group", group, "error", delErr)
		}
		return nil, fmt.Errorf("consume output: %w", err)
	}
	return ch, nil
}
```

Apply the same fix to `ReadInput()`.

---

### HIGH-A2: Agent doesn't distinguish between timeout and real I/O errors in readInput()

**Location:** `/home/vince/repos/johanssonvincent/kraclaw/agent/src/ipc.ts` lines 188-224

**Severity:** HIGH

**Issue:**
The `readInput()` method returns `null` on timeout, but the main loop in `index.ts` doesn't distinguish this from a real error condition. It treats both the same way.

In `/home/vince/repos/johanssonvincent/kraclaw/agent/src/index.ts`:
- Line ~143: Calls `readInput()` in a loop
- If it returns `null`, the code treats it as "no message" and continues
- If it throws, the catch block is unclear about whether it's fatal or recoverable

The agent's main loop doesn't have proper timeout handling logic. A 5-second timeout on every readInput call means the agent spins checking for messages every 5 seconds, logging nothing.

**User Impact:**
- Agent logs show nothing for 5-second blocks between message checks
- Hard to distinguish agent working state from hung state
- No insight into why agent isn't responding

**Recommendation:**
Explicitly handle timeout vs. error cases in main loop.

---

### HIGH-A3: No Error Recovery When SendInput Fails in orchestrator.go

**Location:** `/home/vince/repos/johanssonvincent/kraclaw/internal/orchestrator/orchestrator.go` lines 785-807

**Severity:** HIGH

**Issue:**
When `SendInput()` fails after the sandbox is created and marked active, the orchestrator tries to clean up but doesn't retry or provide recovery path. The message is lost.

```go
if err := o.ipc.SendInput(ctx, group.Folder, ipc.DefaultAgentID, &ipc.IPCMessage{
	Group:   group.Folder,
	Type:    ipc.IPCMessageText,
	Payload: payload,
}); err != nil {
	o.log.Error("failed to send initial input to agent, tearing down sandbox", ...)
	// Stop sandbox, mark inactive
	// BUT: messages queued in o.queue are now lost! Agent never gets them.
	return fmt.Errorf("send initial input: %w", err)
}
```

If `SendInput()` fails due to a transient error (network blip, NATS momentary unavailability), the entire message delivery operation is aborted. The queued messages are never sent.

**Hidden Errors:**
- Initial message delivery failures are not retried
- Messages enqueued in `o.queue` before agent creation are lost if SendInput fails
- Sandbox is destroyed on every transient IPC error

**Recommendation:**
Implement retry logic or queue the initial message:

```go
// Option 1: Retry SendInput a few times
const maxSendRetries = 3
const sendRetryDelay = 100 * time.Millisecond

var sendErr error
for attempt := 0; attempt < maxSendRetries; attempt++ {
	sendErr = o.ipc.SendInput(ctx, group.Folder, ipc.DefaultAgentID, &ipc.IPCMessage{
		Group:   group.Folder,
		Type:    ipc.IPCMessageText,
		Payload: payload,
	})
	if sendErr == nil {
		break
	}
	if attempt < maxSendRetries-1 {
		o.log.Warn("SendInput failed, retrying", 
			"group", group.Name, 
			"attempt", attempt+1,
			"error", sendErr)
		select {
		case <-time.After(sendRetryDelay):
		case <-ctx.Done():
			return fmt.Errorf("context cancelled while retrying SendInput: %w", ctx.Err())
		}
	}
}

if sendErr != nil {
	o.log.Error("failed to send initial input to agent after retries, tearing down sandbox", ...)
	// cleanup...
	return fmt.Errorf("send initial input: %w", sendErr)
}
```

---

## Summary Table

| Issue ID | Component | Severity | Error Handling Problem | Status |
|----------|-----------|----------|------------------------|--------|
| CRITICAL-A1 | Agent IPC (TS) | CRITICAL | Promise.race() abandons cleanup | NEW |
| CRITICAL-A2 | Queue (Go) | CRITICAL | Consumer recreated every Dequeue | NEW |
| CRITICAL-A3 | IPC Broker (Go) | CRITICAL | Shared consumer name across groups | NEW |
| HIGH-A1 | IPC Broker (Go) | HIGH | Orphaned consumer on consume() failure | NEW |
| HIGH-A2 | Agent IPC (TS) | HIGH | Timeout vs error indistinguishable | NEW |
| HIGH-A3 | Orchestrator (Go) | HIGH | No retry on SendInput failure | NEW |

---

## Implementation Priority

1. **CRITICAL-A3** (Shared consumer name) - Fix immediately, blocks entire group message delivery
2. **CRITICAL-A1** (Promise.race()) - Fix immediately, causes agent hang/loop
3. **CRITICAL-A2** (Consumer per Dequeue) - Fix soon, causes message loss
4. **HIGH-A1** (Orphaned consumer cleanup) - Fix soon, prevents resource leak
5. **HIGH-A3** (SendInput retry) - Fix soon, prevents transient error cascades
6. **HIGH-A2** (Timeout logging) - Fix for observability
