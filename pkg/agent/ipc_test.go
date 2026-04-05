package agent

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	natserver "github.com/nats-io/nats-server/v2/server"
	nats "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

func startTestNATS(t *testing.T) *nats.Conn {
	t.Helper()
	opts := &natserver.Options{
		JetStream: true,
		StoreDir:  t.TempDir(),
		Port:      -1,
		NoLog:     true,
		NoSigs:    true,
	}
	s, err := natserver.NewServer(opts)
	if err != nil {
		t.Fatalf("new nats server: %v", err)
	}
	go s.Start()
	if !s.ReadyForConnections(5 * time.Second) {
		t.Fatal("nats server not ready")
	}
	t.Cleanup(s.Shutdown)
	nc, err := nats.Connect(s.ClientURL())
	if err != nil {
		t.Fatalf("nats connect: %v", err)
	}
	t.Cleanup(nc.Close)
	return nc
}

func TestIPCClient_SendOutput(t *testing.T) {
	nc := startTestNATS(t)
	groupJID := "send-test@g.us"
	client, err := NewIPCClient(nc, groupJID, "main", nil)
	if err != nil {
		t.Fatalf("NewIPCClient: %v", err)
	}

	ctx := context.Background()
	// Create the stream first (as the server normally would).
	js, _ := jetstream.New(nc)
	sanitized := sanitizeGroupID(groupJID)
	_, err = js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:      "KRACLAW_IPC_" + strings.ToUpper(sanitized),
		Subjects:  []string{"kraclaw.ipc." + sanitized + ".*.input", "kraclaw.ipc." + sanitized + ".*.output"},
		Retention: jetstream.LimitsPolicy,
		MaxAge:    time.Hour,
	})
	if err != nil {
		t.Fatalf("create stream: %v", err)
	}

	if err := client.SendOutput(ctx, &OutboundMessage{Type: "message", Text: "hello"}); err != nil {
		t.Fatalf("SendOutput: %v", err)
	}
}

func TestIPCClient_ReadInput(t *testing.T) {
	nc := startTestNATS(t)
	groupJID := "read-test@g.us"
	client, err := NewIPCClient(nc, groupJID, "main", nil)
	if err != nil {
		t.Fatalf("NewIPCClient: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Create the stream and publish an input message (as the server normally would).
	js, _ := jetstream.New(nc)
	sanitized := sanitizeGroupID(groupJID)
	_, err = js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:      "KRACLAW_IPC_" + strings.ToUpper(sanitized),
		Subjects:  []string{"kraclaw.ipc." + sanitized + ".*.input", "kraclaw.ipc." + sanitized + ".*.output"},
		Retention: jetstream.LimitsPolicy,
		MaxAge:    time.Hour,
	})
	if err != nil {
		t.Fatalf("create stream: %v", err)
	}

	inputSubject := "kraclaw.ipc." + sanitized + ".main.input"

	// Create the consumer (ReadInput), then publish.
	ch, errCh, err := client.ReadInput(ctx)
	if err != nil {
		t.Fatalf("ReadInput: %v", err)
	}

	payload, _ := json.Marshal(map[string]interface{}{
		"group":   groupJID,
		"type":    "message",
		"payload": json.RawMessage(`{"text":"hi"}`),
	})
	if _, err := js.Publish(ctx, inputSubject, payload); err != nil {
		t.Fatalf("publish input: %v", err)
	}

	select {
	case msg := <-ch:
		if msg.Type != "message" {
			t.Errorf("type = %q, want %q", msg.Type, "message")
		}
	case err := <-errCh:
		t.Fatalf("ipc error: %v", err)
	case <-ctx.Done():
		t.Fatal("timed out waiting for input message")
	}
}

// TestIPCClient_ReadInput_ContextCancel verifies that cancelling the context
// passed to ReadInput causes both the message channel and the error channel to
// close (gap 11).
func TestIPCClient_ReadInput_ContextCancel(t *testing.T) {
	nc := startTestNATS(t)
	groupJID := "ctx-cancel-readinput@g.us"
	client, err := NewIPCClient(nc, groupJID, "main", nil)
	if err != nil {
		t.Fatalf("NewIPCClient: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())

	ch, errCh, err := client.ReadInput(ctx)
	if err != nil {
		t.Fatalf("ReadInput: %v", err)
	}

	cancel()

	timeout := time.After(5 * time.Second)
	chClosed, errChClosed := false, false
	for !chClosed || !errChClosed {
		select {
		case _, ok := <-ch:
			if !ok {
				chClosed = true
			}
		case _, ok := <-errCh:
			if !ok {
				errChClosed = true
			}
		case <-timeout:
			t.Errorf("timed out: ch closed=%v, errCh closed=%v", chClosed, errChClosed)
			return
		}
	}
}

func TestIPCClient_EnsureStreamError_Wrapped(t *testing.T) {
	nc := startTestNATS(t)
	client, err := NewIPCClient(nc, "ensure-wrap@g.us", "main", nil)
	if err != nil {
		t.Fatalf("NewIPCClient: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err = client.ensureStream(ctx)
	if err == nil {
		t.Fatal("ensureStream() error = nil, want wrapped context error")
	}
	if !strings.Contains(err.Error(), "ensure ipc stream") {
		t.Fatalf("error = %q, want context %q", err.Error(), "ensure ipc stream")
	}
}

func TestIPCClient_SendOutput_EnsureStreamError_Wrapped(t *testing.T) {
	nc := startTestNATS(t)
	client, err := NewIPCClient(nc, "send-ensure-wrap@g.us", "main", nil)
	if err != nil {
		t.Fatalf("NewIPCClient: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err = client.SendOutput(ctx, &OutboundMessage{Type: "message", Text: "hello"})
	if err == nil {
		t.Fatal("SendOutput() error = nil, want ensure stream error")
	}
	if !strings.Contains(err.Error(), "ensure ipc stream") {
		t.Fatalf("error = %q, want context %q", err.Error(), "ensure ipc stream")
	}
}

// TestIPCClientSyncOnce verifies the behavioral idempotency contract of
// ReadInput: concurrent callers must receive the SAME channel references so
// that only a single consumer is created (preventing message loss or
// duplication from multiple consumer instances).
func TestIPCClientSyncOnce(t *testing.T) {
	nc := startTestNATS(t)
	groupJID := "sync-once@g.us"
	client, err := NewIPCClient(nc, groupJID, "main", nil)
	if err != nil {
		t.Fatalf("NewIPCClient: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Pre-create the stream.
	js, _ := jetstream.New(nc)
	sanitized := sanitizeGroupID(groupJID)
	if _, err := js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:      "KRACLAW_IPC_" + strings.ToUpper(sanitized),
		Subjects:  []string{"kraclaw.ipc." + sanitized + ".*.input", "kraclaw.ipc." + sanitized + ".*.output"},
		Retention: jetstream.LimitsPolicy,
		MaxAge:    time.Hour,
	}); err != nil {
		t.Fatalf("create stream: %v", err)
	}

	type result struct {
		msgCh <-chan *InboundMessage
		errCh <-chan error
		err   error
	}

	var wg sync.WaitGroup
	results := make([]result, 2)
	for i := range results {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			msgCh, errCh, err := client.ReadInput(ctx)
			results[idx] = result{msgCh: msgCh, errCh: errCh, err: err}
		}(i)
	}
	wg.Wait()

	for i, r := range results {
		if r.err != nil {
			t.Fatalf("ReadInput call %d: %v", i, r.err)
		}
	}

	// Both goroutines must observe the same channel references — proof that
	// sync.Once ran the initializer exactly once.
	if results[0].msgCh != results[1].msgCh {
		t.Errorf("ReadInput returned different msgCh pointers: %p vs %p", results[0].msgCh, results[1].msgCh)
	}
	if results[0].errCh != results[1].errCh {
		t.Errorf("ReadInput returned different errCh pointers: %p vs %p", results[0].errCh, results[1].errCh)
	}
}

// startTestNATSServer is a variant of startTestNATS that also returns the
// underlying server handle so tests can forcibly shut it down to simulate
// connection-loss iterator errors.
func startTestNATSServer(t *testing.T) (*nats.Conn, *natserver.Server) {
	t.Helper()
	opts := &natserver.Options{
		JetStream: true,
		StoreDir:  t.TempDir(),
		Port:      -1,
		NoLog:     true,
		NoSigs:    true,
	}
	s, err := natserver.NewServer(opts)
	if err != nil {
		t.Fatalf("new nats server: %v", err)
	}
	go s.Start()
	if !s.ReadyForConnections(5 * time.Second) {
		t.Fatal("nats server not ready")
	}
	t.Cleanup(func() {
		if s.Running() {
			s.Shutdown()
		}
	})
	nc, err := nats.Connect(s.ClientURL(), nats.NoReconnect())
	if err != nil {
		t.Fatalf("nats connect: %v", err)
	}
	t.Cleanup(nc.Close)
	return nc, s
}

// TestIPCClient_ReadInput_IteratorError verifies that when the underlying
// NATS connection/iterator fails with a non-context error, ReadInput
// surfaces the error on errCh and closes both channels so callers unblock.
func TestIPCClient_ReadInput_IteratorError(t *testing.T) {
	nc, server := startTestNATSServer(t)
	groupJID := "iterator-error@g.us"
	client, err := NewIPCClient(nc, groupJID, "main", nil)
	if err != nil {
		t.Fatalf("NewIPCClient: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Pre-create the stream.
	js, _ := jetstream.New(nc)
	sanitized := sanitizeGroupID(groupJID)
	if _, err := js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:      "KRACLAW_IPC_" + strings.ToUpper(sanitized),
		Subjects:  []string{"kraclaw.ipc." + sanitized + ".*.input", "kraclaw.ipc." + sanitized + ".*.output"},
		Retention: jetstream.LimitsPolicy,
		MaxAge:    time.Hour,
	}); err != nil {
		t.Fatalf("create stream: %v", err)
	}

	msgCh, errCh, err := client.ReadInput(ctx)
	if err != nil {
		t.Fatalf("ReadInput: %v", err)
	}

	// Kill the server underneath the consumer to force a non-context
	// iterator error.
	server.Shutdown()

	// Expect an error on errCh and subsequent closure of both channels.
	timeout := time.After(10 * time.Second)
	gotErr := false
	msgChClosed := false
	errChClosed := false

	for !gotErr || !msgChClosed || !errChClosed {
		select {
		case _, ok := <-msgCh:
			if !ok {
				msgChClosed = true
			}
		case e, ok := <-errCh:
			if !ok {
				errChClosed = true
				continue
			}
			if e == nil {
				continue
			}
			// Any non-nil, non-context error is a valid iterator error for
			// this test. Context errors would indicate the test itself
			// timed out waiting, not the code under test.
			if strings.Contains(e.Error(), "context") {
				t.Fatalf("got context error, expected non-context iterator error: %v", e)
			}
			gotErr = true
		case <-timeout:
			t.Fatalf("timed out: gotErr=%v msgChClosed=%v errChClosed=%v",
				gotErr, msgChClosed, errChClosed)
		}
	}
}

// TestIPCClient_ReadInput_MultiGroupIsolation verifies that two IPCClients
// bound to different groupJIDs do not cross-deliver input messages, even when
// they share a single NATS connection.
func TestIPCClient_ReadInput_MultiGroupIsolation(t *testing.T) {
	nc := startTestNATS(t)

	groupA := "iso-group-a@g.us"
	groupB := "iso-group-b@g.us"

	clientA, err := NewIPCClient(nc, groupA, "main", nil)
	if err != nil {
		t.Fatalf("NewIPCClient A: %v", err)
	}
	clientB, err := NewIPCClient(nc, groupB, "main", nil)
	if err != nil {
		t.Fatalf("NewIPCClient B: %v", err)
	}

	ctxA, cancelA := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelA()
	ctxB, cancelB := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancelB()

	// Pre-create both streams (as the server normally would).
	js, _ := jetstream.New(nc)
	for _, g := range []string{groupA, groupB} {
		sanitized := sanitizeGroupID(g)
		if _, err := js.CreateOrUpdateStream(ctxA, jetstream.StreamConfig{
			Name:      "KRACLAW_IPC_" + strings.ToUpper(sanitized),
			Subjects:  []string{"kraclaw.ipc." + sanitized + ".*.input", "kraclaw.ipc." + sanitized + ".*.output"},
			Retention: jetstream.LimitsPolicy,
			MaxAge:    time.Hour,
		}); err != nil {
			t.Fatalf("create stream %s: %v", g, err)
		}
	}

	// Subscribe both clients first so their consumers exist before publishing.
	chA, errChA, err := clientA.ReadInput(ctxA)
	if err != nil {
		t.Fatalf("ReadInput A: %v", err)
	}
	chB, errChB, err := clientB.ReadInput(ctxB)
	if err != nil {
		t.Fatalf("ReadInput B: %v", err)
	}

	// Publish an input message to group A only.
	sanitizedA := sanitizeGroupID(groupA)
	inputSubjectA := "kraclaw.ipc." + sanitizedA + ".main.input"
	payload, _ := json.Marshal(map[string]interface{}{
		"group":   groupA,
		"type":    "message",
		"payload": json.RawMessage(`{"text":"for-A"}`),
	})
	if _, err := js.Publish(ctxA, inputSubjectA, payload); err != nil {
		t.Fatalf("publish: %v", err)
	}

	// Group A must receive the message.
	select {
	case msg, ok := <-chA:
		if !ok {
			t.Fatal("group A channel closed unexpectedly")
		}
		if msg.Type != "message" {
			t.Errorf("group A msg.Type = %q, want %q", msg.Type, "message")
		}
	case err := <-errChA:
		t.Fatalf("group A error: %v", err)
	case <-ctxA.Done():
		t.Fatal("timed out waiting for group A delivery")
	}

	// Group B must NOT receive it. Wait for B's short-lived context to expire
	// and verify no message arrived in the meantime.
	select {
	case msg, ok := <-chB:
		if ok {
			t.Fatalf("group B received cross-delivered message: %+v", msg)
		}
		// Channel closed due to ctxB expiring is acceptable.
	case err, ok := <-errChB:
		if ok && err != nil {
			// Context-cancel errors are acceptable here; only surface unexpected errors.
			if !strings.Contains(err.Error(), "context") {
				t.Fatalf("group B error: %v", err)
			}
		}
	case <-ctxB.Done():
		// ctxB expired with no cross-delivered message — the isolation invariant holds.
	}
}

// TestIPCClient_ReadInput_MalformedMessage verifies that malformed (non-JSON)
// input messages are ACK'd and skipped without panicking or breaking the
// subscription.
func TestIPCClient_ReadInput_MalformedMessage(t *testing.T) {
	nc := startTestNATS(t)

	groupJID := "malformed-input@g.us"
	client, err := NewIPCClient(nc, groupJID, "main", nil)
	if err != nil {
		t.Fatalf("NewIPCClient: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Pre-create the stream.
	js, _ := jetstream.New(nc)
	sanitized := sanitizeGroupID(groupJID)
	if _, err := js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:      "KRACLAW_IPC_" + strings.ToUpper(sanitized),
		Subjects:  []string{"kraclaw.ipc." + sanitized + ".*.input", "kraclaw.ipc." + sanitized + ".*.output"},
		Retention: jetstream.LimitsPolicy,
		MaxAge:    time.Hour,
	}); err != nil {
		t.Fatalf("create stream: %v", err)
	}

	// Subscribe first so the consumer exists before the malformed publish.
	ch, errCh, err := client.ReadInput(ctx)
	if err != nil {
		t.Fatalf("ReadInput: %v", err)
	}

	inputSubject := "kraclaw.ipc." + sanitized + ".main.input"
	if _, err := js.Publish(ctx, inputSubject, []byte("not-json")); err != nil {
		t.Fatalf("publish malformed: %v", err)
	}

	// Now publish a valid message; if the malformed message was correctly ACK'd
	// and skipped, the consume loop should still deliver this one.
	validPayload, _ := json.Marshal(map[string]interface{}{
		"group":   groupJID,
		"type":    "message",
		"payload": json.RawMessage(`{"text":"after-malformed"}`),
	})
	if _, err := js.Publish(ctx, inputSubject, validPayload); err != nil {
		t.Fatalf("publish valid: %v", err)
	}

	select {
	case msg, ok := <-ch:
		if !ok {
			t.Fatal("channel closed before valid message delivered")
		}
		if msg.Type != "message" {
			t.Errorf("msg.Type = %q, want %q", msg.Type, "message")
		}
	case err := <-errCh:
		t.Fatalf("ipc error: %v", err)
	case <-ctx.Done():
		t.Fatal("timed out: malformed message may have broken subscription")
	}
}
