# Error Handling Audit: PR #23 Critical Findings

## Executive Summary

Three **CRITICAL** error handling defects have been identified in the NATS JetStream migration that will cause silent message loss, agent hangs, and orchestrator deadlocks in production.

**All three must be fixed before merging PR #23.**

---

## CRITICAL ISSUE #1: Shared Consumer Name Breaks Multi-Group Deployments

**File:** `/home/vince/repos/johanssonvincent/kraclaw/internal/ipc/nats_broker.go` lines 143-149  
**Severity:** CRITICAL - Causes silent message loss  
**Impact:** Groups beyond the first will never receive agent output messages

### The Problem

The `SubscribeOutput()` method uses the SAME durable consumer name (`"kraclaw-server"`) for ALL groups:

```go
cons, err := b.js.CreateOrUpdateConsumer(ctx, streamName, jetstream.ConsumerConfig{
    Durable:       ipcServerConsumer, // "kraclaw-server" - SAME for all groups!
    FilterSubject: ipcOutputWildcard(sanitized),
    DeliverPolicy: jetstream.DeliverAllPolicy,
    AckPolicy:     jetstream.AckExplicitPolicy,
})
```

When group "bar" calls `SubscribeOutput()` after group "foo":
1. Creates/updates consumer `"kraclaw-server"` with group "bar"'s filter
2. NATS updates the existing consumer from group "foo" to use group "bar"'s filter
3. Group "foo"'s `watchGroupOutput` goroutine holds old consumer handle → never receives "foo" messages
4. Messages from group "foo" agents are lost silently

### The Fix

Use unique consumer names per group:

```go
func ipcServerConsumerName(sanitized string) string {
    return "kraclaw-server-" + sanitized // Unique per group
}

cons, err := b.js.CreateOrUpdateConsumer(ctx, streamName, jetstream.ConsumerConfig{
    Durable:       ipcServerConsumerName(sanitized), // "kraclaw-server-{hash}"
    FilterSubject: ipcOutputWildcard(sanitized),
    DeliverPolicy: jetstream.DeliverAllPolicy,
    AckPolicy:     jetstream.AckExplicitPolicy,
})
```

---

## CRITICAL ISSUE #2: Promise.race() Abandons Message Processing

**File:** `/home/vince/repos/johanssonvincent/kraclaw/agent/src/ipc.ts` lines 188-217  
**Severity:** CRITICAL - Causes agent hangs and memory leaks  
**Impact:** Agent becomes unresponsive after several timeout cycles

### The Problem

The `readInput()` method uses `Promise.race()` with a 5-second timeout:

```typescript
const msgPromise = (async () => {
  for await (const jmsg of sub) {
    // ... parse and ack ...
    return msg;
  }
})();

return await Promise.race([msgPromise, timeout]); // Timeout wins, returns null
// BUT: msgPromise's for-await loop is STILL RUNNING!
```

When timeout fires:
1. Function returns `null` immediately
2. The `for-await` loop in `msgPromise` continues running in background
3. The `finally` block runs `await sub.unsubscribe()` WHILE loop is mid-iteration
4. Race condition: Loop tries to iterate, unsubscribe fires, subscription state corrupts
5. Next call to `readInput()` has corrupted iterator/subscription state

### The Fix

Remove `Promise.race()`. Let the for-await loop naturally handle timeouts or use explicit cleanup:

```typescript
async readInput(): Promise<IPCMessage | null> {
  // ... setup ...
  const sub = await this.jsClient.subscribe(this.inputSubject(), {
    config: { durable_name: consumerName },
  });

  try {
    for await (const jmsg of sub) {
      try {
        // Parse and ACK
        // ... 
        return msg; // Return immediately on first message
      } catch (parseErr) {
        // NAK and continue
      }
    }
    return null; // No messages (subscription closed)
  } finally {
    await sub.unsubscribe(); // Cleanup AFTER loop exits
  }
}
```

---

## CRITICAL ISSUE #3: Consumer Recreated on Every Dequeue

**File:** `/home/vince/repos/johanssonvincent/kraclaw/internal/queue/nats_queue.go` lines 112-125  
**Severity:** CRITICAL - Causes message loss  
**Impact:** Queued messages (tasks, retries) never execute

### The Problem

Each `Dequeue()` call creates/updates the same durable consumer:

```go
cons, err := q.js.CreateOrUpdateConsumer(ctx, streamName, jetstream.ConsumerConfig{
    Durable:   "dequeue-" + sanitized,
    AckPolicy: jetstream.AckExplicitPolicy,
    // NO DeliverPolicy → defaults to DeliverNewPolicy
})
```

Issues:
1. **No DeliverPolicy**: Consumer defaults to `DeliverNewPolicy` (only new messages)
2. **Messages between polls**: If Dequeue isn't called for 30 seconds and 5 messages are queued, they're skipped
3. **Inefficient**: Every Dequeue adds a network roundtrip to NATS for CreateOrUpdateConsumer

### The Fix

Create consumer once during queue initialization:

```go
type NATSQueue struct {
    // ...
    dequeueMu       sync.Mutex
    dequeueConsumers map[string]jetstream.Consumer
}

// Create consumer once per group
func (q *NATSQueue) ensureDequeueConsumer(ctx context.Context, sanitized string) (jetstream.Consumer, error) {
    q.dequeueMu.Lock()
    defer q.dequeueMu.Unlock()
    
    if cons, ok := q.dequeueConsumers[sanitized]; ok {
        return cons, nil
    }
    
    cons, err := q.js.CreateOrUpdateConsumer(ctx, queueStreamName(sanitized), jetstream.ConsumerConfig{
        Durable:       "dequeue-" + sanitized,
        DeliverPolicy: jetstream.DeliverAllPolicy, // Deliver pending messages too
        AckPolicy:     jetstream.AckExplicitPolicy,
    })
    if err != nil {
        return nil, err
    }
    
    q.dequeueConsumers[sanitized] = cons
    return cons, nil
}

// In Dequeue, reuse consumer:
func (q *NATSQueue) Dequeue(ctx context.Context, groupJID string) (*QueueMessage, error) {
    sanitized, err := q.ensureStream(ctx, groupJID)
    if err != nil {
        return nil, fmt.Errorf("dequeue ensure stream: %w", err)
    }
    
    cons, err := q.ensureDequeueConsumer(ctx, sanitized) // Reuse, don't recreate
    if err != nil {
        return nil, fmt.Errorf("get dequeue consumer: %w", err)
    }
    
    // Fetch from consumer (no CreateOrUpdateConsumer here)
    msgs, err := cons.Fetch(1, jetstream.FetchMaxWait(queueFetchTimeout))
    // ...
}
```

---

## Additional High-Priority Issues

See `/home/vince/repos/johanssonvincent/kraclaw/PR23-ERROR-HANDLING-AUDIT-NEW.md` for:

- **HIGH-A1**: Orphaned consumers when `consume()` fails
- **HIGH-A2**: Agent timeout handling opaque to main loop
- **HIGH-A3**: No retry on `SendInput()` failure (transient errors abort agent)

---

## Verification Checklist

Before merging PR #23, verify:

- [ ] `SubscribeOutput()` uses unique consumer names per group
- [ ] `Dequeue()` doesn't call `CreateOrUpdateConsumer` on every call
- [ ] Agent `readInput()` uses `for await` without `Promise.race()`
- [ ] Add integration test with 3+ concurrent groups
- [ ] Add integration test for Dequeue message persistence
- [ ] Add integration test for agent timeout behavior
- [ ] Load test with many Dequeue calls to verify performance

---

## Files Affected

| File | Lines | Issue | Severity |
|------|-------|-------|----------|
| `internal/ipc/nats_broker.go` | 143-149 | Shared consumer name | CRITICAL |
| `agent/src/ipc.ts` | 188-217 | Promise.race() | CRITICAL |
| `internal/queue/nats_queue.go` | 112-125 | Consumer per Dequeue | CRITICAL |
| `internal/ipc/nats_broker.go` | 137-151 | Orphaned consumer | HIGH |
| `agent/src/ipc.ts` | 188-224 | Timeout handling | HIGH |
| `internal/orchestrator/orchestrator.go` | 785-807 | SendInput retry | HIGH |

---

## References

- Full audit: `PR23-ERROR-HANDLING-AUDIT-NEW.md`
- Original audit: `PR23-ERROR-HANDLING-AUDIT.md`
- Related commits:
  - `5f11b8a` fix(ipc): cancel contexts before stopping iterators
  - `cf73d1b` fix(ipc): switch SubscribeOutput to DeliverAllPolicy
  - `f3dfa19` fix(quick-260404-cqn): fix important issues I1-I4
