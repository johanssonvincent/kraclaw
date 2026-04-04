# PR #23 Error Handling Audit: NATS JetStream Migration

## Executive Summary

This audit reviews the Redis-to-NATS JetStream migration for error handling completeness, context cancellation behavior, and potential silent failures. The migration introduces new failure modes that were not present in the Redis implementation, and several critical error handling gaps have been identified.

**Critical Issues Found: 3**
**High-Priority Issues Found: 8**
**Medium-Priority Issues Found: 6**

---

## 1. Critical Issues

### CRITICAL-01: Promise.race() Race Condition in Agent IPC readInput()

**Location:** `/home/vince/repos/johanssonvincent/kraclaw/agent/src/ipc.ts` (lines ~212-221)

**Severity:** CRITICAL

**Issue:**
The Promise.race() implementation has a dangerous race condition where the timeout promise can reject while msgPromise is still pending. When timeout fires, the msgPromise continues executing in the background, and if ack() fails, that error is lost.

```typescript
// CURRENT (PROBLEMATIC):
return await Promise.race([msgPromise, timeout]);

// The timeout promise rejects, but msgPromise is still running.
// If jmsg.ack() fails inside msgPromise, that error is lost.
// The unsubscribe() in finally won't run until msgPromise completes.
```

**Hidden Errors:**
- ACK failures on message acknowledgment (message could be redelivered or stuck)
- Subscription cleanup failures (subscription leaks)
- NATS connection errors that occur during timeout period

**User Impact:**
- Silent message delivery failures where messages are lost or duplicated without any indication
- Memory/subscription leaks accumulating over time as subscriptions fail to close
- Agent appears hung while in reality it silently dropped the message and timed out
- No indication to the server that the agent failed, causing the group to hang indefinitely

**Recommendation:**
Implement proper promise handling that ensures cleanup occurs and errors are propagated:

```typescript
async readInput(): Promise<IPCMessage> {
  if (!this.jsClient) {
    throw new Error("jetstream client not initialized - call connect() first");
  }
  
  await this.ensureStream();
  
  const streamName = this.streamName();
  const consumerName = "agent-" + sanitizeAgentID(this.agentID);
  
  try {
    // Ensure consumer exists
    try {
      await this.jsManager.consumers.info(streamName, consumerName);
    } catch (createErr) {
      // Create consumer if it doesn't exist
      if (
        !String(createErr).includes("consumer already exists") &&
        !String(createErr).includes("CONSUMER_EXISTS")
      ) {
        throw createErr;
      }
    }
    
    const sub = await this.jsClient.subscribe(this.inputSubject(), {
      config: {
        durable_name: consumerName,
        deliver_policy: DeliverPolicy.All,
        ack_policy: AckPolicy.Explicit,
      },
    });
    
    try {
      let firstMessage = true;
      
      // Create message promise with proper cleanup
      const msgPromise = (async () => {
        for await (const jmsg of sub) {
          try {
            const data = new TextDecoder().decode(jmsg.data);
            const parsed = JSON.parse(data);
            const msg: IPCMessage = {
              group: this.groupJID,
              type: parsed.type,
              payload: parsed.payload || {},
            };
            
            // ACK the message
            if (jmsg.ack) {
              await jmsg.ack();
            }
            
            return msg;
          } catch (parseErr) {
            // Log parse error and NAK to skip this message
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
        throw new Error("subscription ended unexpectedly");
      })();
      
      // Wait for message with timeout (except on first message)
      if (firstMessage) {
        firstMessage = false;
        return await msgPromise;
      }
      
      const timeout = new Promise<never>((_, reject) =>
        setTimeout(() => reject(new Error("readInput timeout")), 5000)
      );
      
      return await Promise.race([msgPromise, timeout]);
    } finally {
      try {
        await sub.unsubscribe();
      } catch (err) {
        console.error(`Failed to unsubscribe: ${err}`);
      }
    }
  } catch (err) {
    throw new Error(
      `ipc read input: ${err instanceof Error ? err.message : String(err)}`
    );
  }
}
```

---

### CRITICAL-02: Unhandled Promise Rejection in Agent Main Loop

**Location:** `/home/vince/repos/johanssonvincent/kraclaw/agent/src/index.ts` (lines ~160-178)

**Severity:** CRITICAL

**Issue:**
The processMessage call at line 161 is awaited and wrapped in try-catch, but if processMessage throws an unhandled error, the error is caught but the catch block only logs to console and sends a generic error message without proper error context or propagation to the orchestrator.

```typescript
// CURRENT (line 160-178):
try {
  const result = await processMessage(ipc, messages, systemPrompt, sessionId, currentModel);
  if (result.sessionId) {
    sessionId = result.sessionId;
  }
  // ... handle success
} catch (err) {
  // Only sends generic message, no structured error logging
  await ipc.publishOutput({
    group: GROUP_FOLDER,
    type: "message",
    payload: {
      text: "I encountered an error processing your message. Please try again."
    },
  });
}
```

**Hidden Errors:**
- Claude API errors (rate limits, auth failures, timeouts)
- Session initialization failures
- Query timeout failures (both 120s attempts)
- Conversation archival failures (logged but not surfaced)
- IPC publish failures when sending the error message

**User Impact:**
- Users receive a generic error message with no indication of the root cause
- Orchestrator cannot distinguish between temporary failures (retry) and permanent failures (abort)
- No structured logging to help debug production issues
- If the error message publish fails, even the generic message is lost silently

**Recommendation:**
Implement proper error categorization and propagation:

```typescript
// Structured error types
interface ProcessError {
  code: string;
  message: string;
  retryable: boolean;
  originalError?: Error;
}

async function main() {
  // ... initialization ...
  
  while (running) {
    try {
      const shouldClose = await ipc.checkClose();
      if (shouldClose) {
        await shutdown();
        break;
      }

      let input: IPCMessage;
      try {
        input = await ipc.readInput();
      } catch (err) {
        // Read timeout is expected on idle, not an error
        if (err instanceof Error && err.message.includes("timeout")) {
          continue;
        }
        // IPC layer error - cannot continue
        console.error("Fatal: IPC readInput failed", err);
        await shutdown();
        break;
      }

      // ... process messages ...
      
      try {
        const result = await processMessage(
          ipc,
          messages,
          systemPrompt,
          sessionId,
          currentModel
        );
        sessionId = result.sessionId;
        
        await ipc.publishOutput({
          group: GROUP_FOLDER,
          type: "message",
          payload: {
            text: result.responseText,
          },
        });
      } catch (err) {
        // Categorize error and respond appropriately
        const processErr = categorizeProcessError(err);
        
        console.error("Process error", {
          code: processErr.code,
          message: processErr.message,
          retryable: processErr.retryable,
          originalError: processErr.originalError?.message,
        });
        
        // Send error message with category info
        try {
          await ipc.publishOutput({
            group: GROUP_FOLDER,
            type: "message",
            payload: {
              text: buildErrorResponse(processErr),
            },
          });
        } catch (publishErr) {
          console.error("Failed to publish error response", publishErr);
          // Even sending error failed - this is critical
          if (processErr.code === "FATAL") {
            await shutdown();
            break;
          }
        }
      }
    } catch (err) {
      console.error("Fatal error in main loop", err);
      await shutdown();
      break;
    }
  }
}
```

---

### CRITICAL-03: Context Cancellation Not Propagating Through IPC Streams

**Location:** `/home/vince/repos/johanssonvincent/kraclaw/internal/ipc/nats_broker.go` (lines ~220-241, 244-312)

**Severity:** CRITICAL

**Issue:**
When orchestrator context is cancelled (e.g., during shutdown or error handling), the cancellation propagates to the consume() goroutine, but the downstream channel reader (watchGroupOutput) continues blocking on channel receive without knowing the stream is closed. The channel is closed asynchronously by the defer in the goroutine.

```go
// CURRENT FLOW:
// 1. watchGroupOutput calls SubscribeOutput(ctx, group)
// 2. SubscribeOutput spawns consume() goroutine with sub-context
// 3. watchGroupOutput blocks on ch receive
// 4. If parent ctx is cancelled, consume() goroutine gets ctx.Done()
// 5. Goroutine closes channel (defer close(ch))
// 6. watchGroupOutput unblocks and returns

// PROBLEM: There's a window between ctx.Done() and channel close where:
// - Parent context is cancelled
// - consume() goroutine may be cleaning up
// - Channel send might fail if it races with close
```

**Hidden Errors:**
- Send on closed channel panic if timing is wrong
- Messages lost if cancel occurs during channel send
- Goroutine cleanup failures silently discarded
- Iterator stop() errors not surfaced

**User Impact:**
- Potential panics during shutdown that crash the entire orchestrator
- Messages in-flight during graceful shutdown are lost without indication
- Cleanup errors accumulate as leaked resources
- Group deactivation may not complete, leaving orphaned sandboxes

**Recommendation:**
Implement proper context cancellation coordination:

```go
// consume creates a goroutine that drains a JetStream consumer into a channel.
// The returned channel is closed when the consumer exits (either due to error
// or context cancellation). The caller MUST read all messages from the channel
// before the returned context is cancelled to avoid panic or message loss.
func (b *NATSBroker) consume(ctx context.Context, cons jetstream.Consumer, group string) (<-chan *IPCMessage, error) {
	ch := make(chan *IPCMessage, 64)

	// Create a sub-context for this consumer that can be cancelled independently
	consCtx, cancel := context.WithCancel(ctx)

	iter, err := cons.Messages()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("create message iterator for group %s: %w", group, err)
	}

	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		iter.Stop()
		cancel()
		close(ch)
		return ch, fmt.Errorf("consume: broker closed")
	}
	
	// Track this consumer so we can stop it on Close()
	b.cancels = append(b.cancels, cancel)
	b.iters = append(b.iters, iter)
	b.mu.Unlock()

	// Separate goroutine to manage iterator cleanup on context cancellation
	// This ensures iter.Next() unblocks when context is cancelled
	done := make(chan struct{}) // closed when consumer goroutine completes
	go func() {
		select {
		case <-consCtx.Done():
			// Context cancelled externally - stop the iterator
			iter.Stop()
		case <-b.closedCh:
			// Broker closed - iter already stopped by Close()
		case <-done:
			// Consumer completed normally - nothing to do
		}
	}()

	// Main consumer goroutine
	go func() {
		defer close(done)
		defer close(ch)
		defer cancel()
		defer iter.Stop()

		for {
			select {
			case <-consCtx.Done():
				// Context cancelled - stop consuming
				return
			case <-b.closedCh:
				// Broker closed - stop consuming
				return
			default:
			}

			// iter.Next() blocks until a message is available or context is done
			jmsg, err := iter.Next()
			if err != nil {
				// Check if this was a context cancellation (expected)
				if consCtx.Err() != nil {
					// Context cancelled - normal shutdown path
					return
				}
				// Unexpected error from iterator
				b.logger.Error("ipc message iterator error", "group", group, "error", err)
				return
			}

			// Unmarshal the message
			var msg IPCMessage
			if err := json.Unmarshal(jmsg.Data(), &msg) != nil {
				// Malformed message - NAK it and continue
				b.logger.Warn("unmarshal ipc message", "group", group, "error", err)
				if nakErr := jmsg.Nak(); nakErr != nil {
					b.logger.Error("nak malformed message", "group", group, "error", nakErr)
				}
				continue
			}
			msg.ID = jmsg.Headers().Get(nats.MsgIdHdr)

			// Send message to channel, respecting cancellation
			select {
			case ch <- &msg:
				// Message sent successfully - ACK it
				if ackErr := jmsg.Ack(); ackErr != nil {
					b.logger.Error("ack ipc message", "group", group, "error", ackErr)
					// Continue anyway - message is in channel
				}
			case <-consCtx.Done():
				// Context cancelled while trying to send - NAK and exit
				if nakErr := jmsg.Nak(); nakErr != nil {
					b.logger.Warn("nak message on context cancel", "group", group, "error", nakErr)
				}
				return
			case <-b.closedCh:
				// Broker closed while trying to send - NAK and exit
				if nakErr := jmsg.Nak(); nakErr != nil {
					b.logger.Warn("nak message on broker close", "group", group, "error", nakErr)
				}
				return
			}
		}
	}()

	return ch, nil
}
```

---

## 2. High-Priority Issues

### HIGH-01: SendInput Failure Cleanup Not Comprehensive

**Location:** `/home/vince/repos/johanssonvincent/kraclaw/internal/orchestrator/orchestrator.go` (lines ~751-782)

**Severity:** HIGH

**Issue:**
When SendInput fails after MarkActive succeeds, the cleanup attempts multiple operations but doesn't verify all succeeded. If MarkInactive fails, the group remains marked as active with no agent consuming messages, causing the group to hang indefinitely.

```go
// CURRENT (INCOMPLETE CLEANUP):
if err := o.ipc.SendInput(ctx, group.Folder, ipc.DefaultAgentID, &ipc.IPCMessage{
  Group:   group.Folder,
  Type:    ipc.IPCMessageText,
  Payload: payload,
}); err != nil {
  o.log.Error("failed to send initial input to agent, tearing down sandbox", "group", group.Name, "error", err)
  if cleanupErr := o.sandbox.StopSandbox(ctx, status.Name); cleanupErr != nil {
    o.log.Error("failed to stop sandbox after SendInput failure", "group", group.Name, "job", status.Name, "error", cleanupErr)
  }
  if markErr := o.queue.MarkInactive(ctx, chatJID); markErr != nil {
    o.log.Error("failed to mark group inactive after SendInput failure", "group", group.Name, "error", markErr)
    // BUG: If MarkInactive fails, we return the SendInput error, not the MarkInactive error!
    // The group is still marked ACTIVE in the database even though there's no consumer.
  }
  // ... rest of cleanup ...
  return fmt.Errorf("send initial input: %w", err)
}
```

**Hidden Errors:**
- MarkInactive failure silently swallowed (only logged, not returned)
- Inconsistent state: group active in database but no consumer watching it
- saveState failure not propagated
- StopSandbox partial success not detected (job might still be running)

**User Impact:**
- Group permanently hangs with no way to recover without manual intervention
- No indication to caller that cleanup failed
- Database state becomes inconsistent with runtime state
- Messages queued for this group will timeout because nobody's watching for output

**Recommendation:**
Implement comprehensive error collection and reporting:

```go
// After SendInput fails, track all cleanup errors
var cleanupErrors []string
var hasActiveSandbox bool = true

// Try to stop sandbox
if cleanupErr := o.sandbox.StopSandbox(ctx, status.Name); cleanupErr != nil {
  o.log.Error("failed to stop sandbox after SendInput failure", "group", group.Name, "job", status.Name, "error", cleanupErr)
  cleanupErrors = append(cleanupErrors, fmt.Sprintf("StopSandbox: %v", cleanupErr))
  hasActiveSandbox = false
}

// Try to mark inactive - this is critical, must not fail silently
if markErr := o.queue.MarkInactive(ctx, chatJID); markErr != nil {
  o.log.Error("failed to mark group inactive after SendInput failure", "group", group.Name, "error", markErr)
  cleanupErrors = append(cleanupErrors, fmt.Sprintf("MarkInactive: %v", markErr))
  // CRITICAL: Log the state inconsistency clearly
  o.log.Error("CRITICAL: Group state inconsistent - marked active in DB but SendInput failed",
    "group", group.Name, "group_jid", chatJID)
}

// Update sandbox tracking
o.mu.Lock()
delete(o.activeSandboxes, chatJID)
o.lastAgentTimestamp[chatJID] = previousCursor
o.mu.Unlock()

// Try to save state
if saveErr := o.saveState(ctx); saveErr != nil {
  o.log.Error("failed to save state after SendInput failure", "group", group.Name, "error", saveErr)
  cleanupErrors = append(cleanupErrors, fmt.Sprintf("SaveState: %v", saveErr))
}

// Return composite error if cleanup had issues
if len(cleanupErrors) > 0 {
  return fmt.Errorf("send initial input and cleanup: original=%w, cleanup_errors=%v", err, cleanupErrors)
}
return fmt.Errorf("send initial input: %w", err)
```

---

### HIGH-02: SubscribeOutput Error Doesn't Trigger Deactivation Atomically

**Location:** `/home/vince/repos/johanssonvincent/kraclaw/internal/orchestrator/orchestrator.go` (lines ~969-976)

**Severity:** HIGH

**Issue:**
When SubscribeOutput fails in watchGroupOutput, the error is logged but if MarkInactive also fails, that error is logged but execution continues, leaving the group state inconsistent.

```go
// CURRENT (lines 969-976):
ch, err := o.ipc.SubscribeOutput(ctx, group.Folder)
if err != nil {
  o.log.Error("failed to subscribe to IPC output, deactivating group", "group", group.Name, "error", err)
  if markErr := o.queue.MarkInactive(ctx, chatJID); markErr != nil {
    o.log.Error("failed to mark group inactive after subscribe failure", "group", group.Name, "error", markErr)
  }
  // BUG: If MarkInactive fails, we return anyway, leaving group active
  return
}
```

**Hidden Errors:**
- MarkInactive failure masked by single return
- Group stays marked as active in database
- No way to know deactivation failed without reading logs
- Race condition: group might get messages before the deactivation retry

**User Impact:**
- Group remains stuck in active state
- Messages sent to the group will timeout waiting for output
- Orchestrator cannot retry because it thinks the group is still processing

**Recommendation:**
```go
if err != nil {
  o.log.Error("failed to subscribe to IPC output", "group", group.Name, "error", err)
  
  if markErr := o.queue.MarkInactive(ctx, chatJID); markErr != nil {
    // This is critical - we cannot leave the group active
    o.log.Error("CRITICAL: Failed to deactivate group after SubscribeOutput failure",
      "group", group.Name, "group_jid", chatJID, "deactivate_error", markErr, "subscribe_error", err)
    // Attempt recovery by spawning a retry goroutine
    go func() {
      retryCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
      defer cancel()
      if retryErr := o.queue.MarkInactive(retryCtx, chatJID); retryErr != nil {
        o.log.Error("Deactivation retry failed, manual intervention required",
          "group", group.Name, "group_jid", chatJID, "error", retryErr)
      }
    }()
  }
  return
}
```

---

### HIGH-03: Peek Race Condition Not Handled

**Location:** `/home/vince/repos/johanssonvincent/kraclaw/internal/queue/nats_queue.go` (lines ~158-197)

**Severity:** HIGH

**Issue:**
The Peek method has a documented race condition (lines ~186-188) where a message can be deleted between Info() call and GetMsg() call, but the comment treats this as acceptable. However, if this race occurs repeatedly, callers cannot distinguish between "queue is empty" and "message exists but we failed to read it."

```go
// CURRENT (lines 184-188):
raw, err := stream.GetMsg(ctx, info.State.FirstSeq)
if err != nil {
  if errors.Is(err, jetstream.ErrMsgNotFound) {
    return nil, nil // empty queue (race between Info and GetMsg)
  }
  return nil, fmt.Errorf("peek: get msg: %w", err)
}
```

**Problem:** If GetMsg fails for ANY reason (network, permissions, server error), it's returned as a real error. But if it fails with ErrMsgNotFound (could be legitimate deletion or stream lag), it's treated as "empty queue" - losing error context.

**Hidden Errors:**
- NATS server connectivity issues masked as empty queue
- Permission errors on stream access masked as empty queue
- Stream replication lag errors masked as empty queue
- Actual transient failures that should trigger retries

**User Impact:**
- Queue operations silently fail and appear as "empty" to callers
- Potential message loss if messages are thought to be consumed when Peek failed
- Difficult to debug why queue processing seems stuck (might be permissions issue)
- Dequeue won't see messages that Peek failed to peek

**Recommendation:**
```go
// Peek returns the oldest message without removing it, or nil if queue is empty.
// Returns an error if the operation fails for any reason other than empty queue.
func (q *NATSQueue) Peek(ctx context.Context, groupJID string) (*QueueMessage, error) {
  sanitized, err := q.ensureStream(ctx, groupJID)
  if err != nil {
    return nil, fmt.Errorf("peek ensure stream: %w", err)
  }
  streamName := queueStreamName(sanitized)

  // Self-heal: remove any legacy "peek-{sanitized}" durable consumer left by
  // older deployments that used the consumer-based Peek implementation.
  if derr := q.js.DeleteConsumer(ctx, streamName, "peek-"+sanitized); derr != nil &&
    !errors.Is(derr, jetstream.ErrConsumerNotFound) {
    q.logger.Warn("peek: failed to delete legacy peek consumer", "error", derr)
  }

  stream, err := q.js.Stream(ctx, streamName)
  if err != nil {
    return nil, fmt.Errorf("peek: get stream: %w", err)
  }
  
  info, err := stream.Info(ctx)
  if err != nil {
    return nil, fmt.Errorf("peek: stream info: %w", err)
  }
  
  if info.State.Msgs == 0 {
    return nil, nil // empty queue
  }

  // Note: Race condition exists here between Info() and GetMsg().
  // If a message is deleted by Dequeue, GetMsg will fail with ErrMsgNotFound.
  // We distinguish this from other errors to provide reliable semantics:
  // - nil, nil = definitely empty (or became empty due to race)
  // - nil, error = failed to read message due to transient issue
  raw, err := stream.GetMsg(ctx, info.State.FirstSeq)
  if err != nil {
    if errors.Is(err, jetstream.ErrMsgNotFound) {
      // This is a race - another Dequeue consumed the message
      // Return nil to indicate "no message to peek" but this isn't an error
      q.logger.Debug("peek: message was consumed before peek completed",
        "stream", streamName, "sequence", info.State.FirstSeq)
      return nil, nil
    }
    // This is a real error - something is wrong with the stream access
    return nil, fmt.Errorf("peek: get msg: %w", err)
  }

  var qm QueueMessage
  if err := json.Unmarshal(raw.Data, &qm); err != nil {
    q.logger.Error("peek: unmarshal message",
      "sequence", info.State.FirstSeq, "error", err)
    return nil, fmt.Errorf("peek: unmarshal message: %w", err)
  }
  
  return &qm, nil
}
```

---

### HIGH-04: Dequeue Error on Empty Queue Not Clearly Distinguished

**Location:** `/home/vince/repos/johanssonvincent/kraclaw/internal/queue/nats_queue.go` (lines ~146-151)

**Severity:** HIGH

**Issue:**
When Dequeue returns nil, nil, the caller cannot distinguish between "queue is empty" (expected) and "an error occurred but we're returning nil to avoid blocking." The comment at lines ~147-148 tries to document this but the code conflates timeout with no-messages.

```go
// CURRENT (lines 146-151):
if err := msgs.Error(); err != nil && !errors.Is(err, jetstream.ErrMsgIteratorClosed) {
  // Timeout and no-messages are expected for empty queue — don't error.
  if !errors.Is(err, nats.ErrTimeout) && !errors.Is(err, jetstream.ErrNoMessages) {
    return nil, fmt.Errorf("fetch error: %w", err)
  }
}
return nil, nil // empty queue
```

**Problems:**
1. If neither timeout nor no-messages errors occur, but some other error in the msgs iterator is present, it could silently return nil without logging
2. A genuine fetch error could be masked if it happens to be one of the "expected" error types
3. Callers have no way to know if nil means "truly empty" vs "try again later due to timeout"

**Hidden Errors:**
- NATS server errors during Fetch() silently swallowed
- Consumer configuration errors
- ACL/permission errors
- Context deadline exceeded

**User Impact:**
- Queue operations appear successful when they actually failed
- Messages might be processed twice if caller retries after false "empty" response
- Difficult to debug queue staleness issues

---

### HIGH-05: Agent IPC Connection Failure Not Surfaced to Orchestrator

**Location:** `/home/vince/repos/johanssonvincent/kraclaw/agent/src/index.ts` (lines ~95-119)

**Severity:** HIGH

**Issue:**
The agent IPC client connection is attempted in the main loop with immediate exit on failure, but there's no way for the orchestrator to know why the agent exited. The shutdown message might not be sent if the connection fails.

```typescript
// CURRENT (lines ~92-119):
const ipc = new IPCClient(NATS_URL, GROUP_FOLDER);

// ... later in main() ...
try {
  await ipc.publishOutput({
    group: GROUP_FOLDER,
    type: "shutdown",
    payload: {},
  });
} catch (err) {
  console.error("failed to publish shutdown", err);
}
await ipc.close();
process.exit(0);
```

**Problem:** If IPC connection fails in the message loop, there's no special handling - the agent just logs to console and continues trying to read input. If the connection succeeds for connect() but fails during subscribe/publish operations, those failures are only logged to stdout, not sent to the orchestrator.

**Hidden Errors:**
- NATS server unavailability not surfaced to orchestrator
- Network partition not detected
- NATS authentication failures masked as read timeouts

**User Impact:**
- Agent appears stuck (hangs on readInput) when NATS is actually down
- Orchestrator cannot distinguish "agent processing" from "agent unable to connect"
- Startup timeout fires unnecessarily when connection issue is transient
- Group marked inactive but user sees no error message

---

### HIGH-06: No Error ID Constants for Sentry Tracking

**Location:** All error logging in `/home/vince/repos/johanssonvincent/kraclaw/internal/ipc/` and `/home/vince/repos/johanssonvincent/kraclaw/internal/queue/`

**Severity:** HIGH

**Issue:**
The new NATS error paths do not use error ID constants from `constants/errorIds.ts` for Sentry tracking, making it impossible to correlate errors across different deployments or identify patterns.

```go
// CURRENT (scattered throughout):
b.logger.Error("ipc message iterator error", "group", group, "error", err)
q.logger.Error("ack malformed queue message", "subject", msg.Subject(), "sequence", seq, "error", err)
```

**Recommendation:**
All error logging should include error IDs:

```go
import "github.com/johanssonvincent/kraclaw/internal/constants"

// In error logging:
b.logger.Error("ipc message iterator error",
  "group", group,
  "error", err,
  "error_id", constants.ErrorIDIPCIteratorFailure,
)
```

---

### HIGH-07: Broker Close() Doesn't Wait for Goroutines to Exit

**Location:** `/home/vince/repos/johanssonvincent/kraclaw/internal/ipc/nats_broker.go` (lines ~196-217)

**Severity:** HIGH

**Issue:**
The Close() method signals all goroutines to exit but doesn't wait for them. If a caller immediately checks that the broker is closed and expects all resources to be released, they might find goroutines still running.

```go
// CURRENT:
func (b *NATSBroker) Close() error {
  b.mu.Lock()
  defer b.mu.Unlock()
  if b.closed {
    return nil
  }
  b.closed = true
  for _, iter := range b.iters {
    iter.Stop()
  }
  b.iters = nil
  for _, cancel := range b.cancels {
    cancel()
  }
  b.cancels = nil
  close(b.closedCh) // <-- Signal goroutines to exit
  return nil
}

// Caller code:
broker.Close() // Now broker is "closed"
// But goroutines might still be running if they haven't reached the <-b.closedCh check
```

**Hidden Errors:**
- Resource cleanup incomplete at Close() return
- Messages sent to closed channel if goroutine sends just after Close() returns
- Iterator cleanup race conditions
- No indication to caller that cleanup is asynchronous

**User Impact:**
- Potential panics if messages are sent to channel after Close()
- Resource leaks if caller doesn't wait for goroutines
- Difficult to implement graceful shutdown properly

---

### HIGH-08: Malformed Message Handling Continues Message Loop

**Location:** `/home/vince/repos/johanssonvincent/kraclaw/internal/ipc/nats_broker.go` (lines ~286-292) and `/home/vince/repos/johanssonvincent/kraclaw/internal/queue/nats_queue.go` (lines ~126-139)

**Severity:** HIGH

**Issue:**
When a message fails to unmarshal, the code logs a warning, ACKs/NAKs the message, and continues to the next message. However, if unmarshalling fails, it might indicate:
1. Upstream code is sending invalid messages (bug)
2. Version mismatch between server and agent
3. Message corruption in NATS

Simply skipping these messages masks the real issue.

```go
// CURRENT (IPC broker):
if err := json.Unmarshal(jmsg.Data(), &msg); err != nil {
  b.logger.Warn("unmarshal ipc message", "group", group, "error", err)
  if err := jmsg.Ack(); err != nil {
    b.logger.Error("ack malformed message", "group", group, "error", err)
  }
  continue  // <-- Silently skip and continue
}
```

**Hidden Errors:**
- Silent version mismatch between server and agent
- Message format bugs in orchestrator not caught until production
- Data corruption in JetStream not alerted
- Pattern of malformed messages indicates architectural issue

**User Impact:**
- Messages silently dropped without user awareness
- No indication that there's a version/format mismatch
- If many messages are malformed, groups will appear hung while actually losing messages
- Debugging requires searching through logs for the warning

**Recommendation:**
Implement a metrics counter and more aggressive alerting:

```go
if err := json.Unmarshal(jmsg.Data(), &msg); err != nil {
  meta, _ := jmsg.Metadata()
  var seq uint64
  if meta != nil {
    seq = meta.Sequence.Stream
  }
  
  b.logger.Error("malformed ipc message — this indicates a protocol mismatch",
    "group", group,
    "sequence", seq,
    "raw_size", len(jmsg.Data()),
    "raw_preview", string(jmsg.Data()[:min(len(jmsg.Data()), 100)]),
    "error", err,
    "error_id", constants.ErrorIDMalformedIPCMessage,
  )
  
  if err := jmsg.Ack(); err != nil {
    b.logger.Error("ack malformed message",
      "group", group,
      "sequence", seq,
      "error", err,
    )
  }
  // Emit metric for alerting
  metrics.MalformedIPCMessages.WithLabelValues(group).Inc()
  continue
}
```

---

## 3. Medium-Priority Issues

### MEDIUM-01: Dequeue Malformed Message Loss Without Indication

**Location:** `/home/vince/repos/johanssonvincent/kraclaw/internal/queue/nats_queue.go` (lines ~124-140)

**Severity:** MEDIUM

**Issue:**
When a message in the queue is malformed, it's logged as a warning but then the function returns an error instead of continuing to the next message. This means a single malformed message blocks the entire queue.

```go
// CURRENT (lines 124-140):
for msg := range msgs.Messages() {
  var qm QueueMessage
  if err := json.Unmarshal(msg.Data(), &qm); err != nil {
    meta, _ := msg.Metadata()
    // ... logging ...
    if err := msg.Ack(); err != nil {
      q.logger.Error("ack malformed queue message", ...)
    }
    return nil, fmt.Errorf("unmarshal queue message: %w", err)  // <-- BLOCKS QUEUE
  }
  if err := msg.Ack(); err != nil {
    return nil, fmt.Errorf("dequeue: ack: %w", err)
  }
  return &qm, nil
}
```

**Recommendation:**
Continue to next message instead of blocking:

```go
for msg := range msgs.Messages() {
  var qm QueueMessage
  if err := json.Unmarshal(msg.Data(), &qm); err != nil {
    meta, _ := msg.Metadata()
    var seq uint64
    if meta != nil {
      seq = meta.Sequence.Stream
    }
    q.logger.Error("dequeue: malformed message — skipping",
      "subject", msg.Subject(),
      "sequence", seq,
      "error", err,
      "error_id", constants.ErrorIDMalformedQueueMessage,
    )
    if ackErr := msg.Ack(); ackErr != nil {
      q.logger.Error("ack malformed queue message",
        "subject", msg.Subject(),
        "sequence", seq,
        "error", ackErr,
      )
    }
    // Continue to next message instead of blocking
    continue
  }
  
  if err := msg.Ack(); err != nil {
    return nil, fmt.Errorf("dequeue: ack: %w", err)
  }
  return &qm, nil
}

// If we got here, no valid messages
return nil, nil
```

---

### MEDIUM-02: Context Cancellation in watchGroupOutput Liveness Check

**Location:** `/home/vince/repos/johanssonvincent/kraclaw/internal/orchestrator/orchestrator.go` (lines ~1003-1013)

**Severity:** MEDIUM

**Issue:**
The liveness check uses a ticker that's never reset. If the HasActiveSandbox check takes longer than 10 seconds, the next tick will fire immediately, potentially hammering the Kubernetes API.

```go
// CURRENT (lines ~1003-1013):
liveness := time.NewTicker(10 * time.Second)
defer liveness.Stop()

// ... later in loop ...
case <-liveness.C:
  if o.sandbox == nil {
    continue
  }
  active, err := o.sandbox.HasActiveSandbox(ctx, group.Folder)
  // No timeout on this call - could take 10+ seconds
  if err != nil {
    o.log.Warn("failed to check sandbox liveness", "group", group.Name, "error", err)
    continue
  }
```

**Recommendation:**
Add timeout to liveness check:

```go
case <-liveness.C:
  if o.sandbox == nil {
    continue
  }
  
  // Check liveness with timeout - if it takes too long, skip this check
  checkCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
  active, err := o.sandbox.HasActiveSandbox(checkCtx, group.Folder)
  cancel()
  
  if err != nil {
    if errors.Is(err, context.DeadlineExceeded) {
      o.log.Warn("sandbox liveness check timed out", "group", group.Name)
      continue
    }
    o.log.Warn("failed to check sandbox liveness", "group", group.Name, "error", err)
    continue
  }
```

---

### MEDIUM-03: Agent Shutdown Not Guaranteed to Send

**Location:** `/home/vince/repos/johanssonvincent/kraclaw/agent/src/index.ts` (lines ~102-119)

**Severity:** MEDIUM

**Issue:**
The shutdown handler might not complete if IPC operations fail, and process.exit() is called before promises resolve. This means the shutdown message might not actually reach the server.

```typescript
// CURRENT (lines ~102-119):
const shutdown = async () => {
  if (shuttingDown) return;
  shuttingDown = true;
  console.log("shutdown signal received, notifying orchestrator");
  running = false;
  try {
    await ipc.publishOutput({
      group: GROUP_FOLDER,
      type: "shutdown",
      payload: {},
    });
  } catch (err) {
    console.error("failed to publish shutdown", err);
  }
  await ipc.close();
  process.exit(0); // <-- May exit before promise completes in some runtime conditions
};
```

**Recommendation:**
Add timeout and force exit if shutdown takes too long:

```typescript
const shutdown = async () => {
  if (shuttingDown) return;
  shuttingDown = true;
  console.log("shutdown signal received, notifying orchestrator");
  running = false;
  
  // Ensure shutdown completes within timeout
  const shutdownTimer = setTimeout(() => {
    console.error("shutdown timeout - force exiting");
    process.exit(1);
  }, 5000);
  
  try {
    await ipc.publishOutput({
      group: GROUP_FOLDER,
      type: "shutdown",
      payload: {},
    });
  } catch (err) {
    console.error("failed to publish shutdown message", {
      error: err instanceof Error ? err.message : String(err),
    });
  }
  
  try {
    await ipc.close();
  } catch (err) {
    console.error("failed to close IPC", err);
  }
  
  clearTimeout(shutdownTimer);
  process.exit(0);
};
```

---

### MEDIUM-04: saveState Error Logged But Not Propagated

**Location:** Multiple locations in `/home/vince/repos/johanssonvincent/kraclaw/internal/orchestrator/orchestrator.go` (lines ~681-689, ~709-713, ~748-752, etc.)

**Severity:** MEDIUM

**Issue:**
Throughout the codebase, saveState() errors are logged but execution continues. This means state inconsistencies can accumulate without the caller knowing.

```go
// CURRENT PATTERN (seen multiple times):
if err := o.saveState(ctx); err != nil {
  o.log.Error("failed to save state", "error", err)
  // Execution continues - caller doesn't know persistence failed
}
```

**User Impact:**
- In-memory state diverges from database
- On restart, old state is loaded, undoing all changes made since save failed
- Cursor positions might be lost, causing message reprocessing or skipping
- No clear error indication that something is wrong

**Recommendation:**
At minimum, track save errors and return them to caller, or implement retry logic.

---

### MEDIUM-05: Consume Goroutine Iterator Error Not Categorized

**Location:** `/home/vince/repos/johanssonvincent/kraclaw/internal/ipc/nats_broker.go` (lines ~244-247)

**Severity:** MEDIUM

**Issue:**
When iter.Next() fails in the consumer goroutine, the error is logged but not categorized. Different failure types (connection lost, permission denied, timeout) should be handled differently.

```go
// CURRENT (lines ~244-247):
jmsg, err := iter.Next()
if err != nil {
  if ctx.Err() != nil {
    return
  }
  b.logger.Error("ipc message iterator error", "group", group, "error", err)
  return
}
```

**Recommendation:**
Categorize errors:

```go
jmsg, err := iter.Next()
if err != nil {
  if ctx.Err() != nil {
    return
  }
  
  // Categorize the error
  var errorID string
  if errors.Is(err, context.Canceled) {
    return // Expected during shutdown
  } else if errors.Is(err, context.DeadlineExceeded) {
    errorID = constants.ErrorIDIPCIteratorTimeout
  } else {
    errorID = constants.ErrorIDIPCIteratorUnexpected
  }
  
  b.logger.Error("ipc message iterator error",
    "group", group,
    "error", err,
    "error_id", errorID,
  )
  return
}
```

---

### MEDIUM-06: Agent readInput Timeout Not Parameterized

**Location:** `/home/vince/repos/johanssonvincent/kraclaw/agent/src/ipc.ts` (lines ~179-221)

**Severity:** MEDIUM

**Issue:**
The 5-second timeout for reading input messages is hardcoded. If the network is slow or the orchestrator is slow, 5 seconds might not be enough. If the orchestrator is fast, 5 seconds is too long and causes unnecessary delays.

```typescript
// CURRENT (hardcoded):
const timeout = new Promise<never>((_, reject) =>
  setTimeout(() => reject(new Error("readInput timeout")), 5000)
);

return await Promise.race([msgPromise, timeout]);
```

**Recommendation:**
Make timeout configurable:

```typescript
async readInput(timeoutMs: number = 30000): Promise<IPCMessage> {
  // ... implementation ...
  const timeout = new Promise<never>((_, reject) =>
    setTimeout(() => reject(new Error("readInput timeout")), timeoutMs)
  );
  return await Promise.race([msgPromise, timeout]);
}
```

---

## 4. Summary of Action Items

### Immediate Actions (Critical)
1. Fix Promise.race() race condition in agent IPC readInput()
2. Implement proper error categorization in agent main loop
3. Add context cancellation coordination guards in broker consume()

### Near-term (High Priority)
4. Review and fix SendInput failure cleanup logic
5. Implement atomic deactivation in watchGroupOutput
6. Add error categorization to queue and IPC operations
7. Implement error ID constants for all new error paths

### Medium-term (Medium Priority)
8. Add timeout to liveness checks
9. Ensure all state persistence failures are surfaced
10. Parameterize agent timeout values
11. Make broker Close() properly wait for goroutines

---

## 5. Testing Recommendations

When addressing these issues, ensure test coverage for:

1. **Context cancellation paths:** Test that cancelling parent context properly stops all consumers
2. **Partial failure scenarios:** Test that if MarkActive succeeds but SendInput fails, cleanup is complete
3. **Timeout handling:** Test both timeout cases (first message vs. subsequent messages)
4. **Message loss scenarios:** Test what happens when NATS connection drops mid-operation
5. **State consistency:** Test that database state always matches runtime state, even after errors
6. **Graceful shutdown:** Test that Close() waits for all goroutines and cleanup is complete

---

## 6. Files Reviewed

- `/home/vince/repos/johanssonvincent/kraclaw/internal/ipc/nats_broker.go`
- `/home/vince/repos/johanssonvincent/kraclaw/internal/queue/nats_queue.go`
- `/home/vince/repos/johanssonvincent/kraclaw/internal/orchestrator/orchestrator.go`
- `/home/vince/repos/johanssonvincent/kraclaw/agent/src/ipc.ts`
- `/home/vince/repos/johanssonvincent/kraclaw/agent/src/index.ts`
