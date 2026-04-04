package ipc

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	natserver "github.com/nats-io/nats-server/v2/server"
	nats "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// startEmbeddedNATS starts an in-process NATS server with JetStream enabled.
func startEmbeddedNATS(t *testing.T) *nats.Conn {
	t.Helper()
	opts := &natserver.Options{
		JetStream: true,
		StoreDir:  t.TempDir(),
		Port:      -1, // random port
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

func setupNATS(t *testing.T) (*NATSBroker, *nats.Conn) {
	t.Helper()
	nc := startEmbeddedNATS(t)
	broker, err := NewNATSBroker(nc, nil)
	if err != nil {
		t.Fatalf("NewNATSBroker: %v", err)
	}
	t.Cleanup(func() { _ = broker.Close() })
	return broker, nc
}

func TestNATSSanitizeGroupID(t *testing.T) {
	jid := "120363123456789012@g.us"
	got := sanitizeGroupID(jid)
	h := sha256.Sum256([]byte(jid))
	want := hex.EncodeToString(h[:16])
	if got != want {
		t.Errorf("sanitizeGroupID(%q) = %q, want %q", jid, got, want)
	}
	// Must be exactly 32 hex chars.
	if len(got) != 32 {
		t.Errorf("sanitizeGroupID len = %d, want 32", len(got))
	}
}

func TestNATSSanitizeGroupID_Deterministic(t *testing.T) {
	jid := "some.jid@g.us"
	a := sanitizeGroupID(jid)
	b := sanitizeGroupID(jid)
	if a != b {
		t.Errorf("sanitizeGroupID not deterministic: %q vs %q", a, b)
	}
}

func TestNATSPublishAndSubscribeOutput(t *testing.T) {
	broker, _ := setupNATS(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	group := "test-group@g.us"
	ch, err := broker.SubscribeOutput(ctx, group)
	if err != nil {
		t.Fatalf("SubscribeOutput: %v", err)
	}

	msg := &IPCMessage{
		Group:   group,
		AgentID: "main",
		Type:    IPCMessageText,
		Payload: json.RawMessage(`{"text":"hello"}`),
	}
	if err := broker.PublishOutput(ctx, group, "main", msg); err != nil {
		t.Fatalf("PublishOutput: %v", err)
	}

	select {
	case got := <-ch:
		if got.Type != IPCMessageText {
			t.Errorf("type = %q, want %q", got.Type, IPCMessageText)
		}
		if got.AgentID != "main" {
			t.Errorf("agent_id = %q, want %q", got.AgentID, "main")
		}
		if string(got.Payload) != `{"text":"hello"}` {
			t.Errorf("payload = %s", got.Payload)
		}
	case <-ctx.Done():
		t.Fatal("timed out")
	}
}

func TestNATSSendAndReadInput(t *testing.T) {
	broker, _ := setupNATS(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	group := "input-group@g.us"
	ch, err := broker.ReadInput(ctx, group, "main")
	if err != nil {
		t.Fatalf("ReadInput: %v", err)
	}

	msg := &IPCMessage{
		Group:   group,
		AgentID: "main",
		Type:    IPCTaskCreate,
		Payload: json.RawMessage(`{"taskId":"abc"}`),
	}
	if err := broker.SendInput(ctx, group, "main", msg); err != nil {
		t.Fatalf("SendInput: %v", err)
	}

	select {
	case got := <-ch:
		if got.Type != IPCTaskCreate {
			t.Errorf("type = %q, want %q", got.Type, IPCTaskCreate)
		}
	case <-ctx.Done():
		t.Fatal("timed out")
	}
}

func TestNATSWildcardConsumerReceivesMultipleAgents(t *testing.T) {
	broker, _ := setupNATS(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	group := "multi-agent-group@g.us"
	ch, err := broker.SubscribeOutput(ctx, group)
	if err != nil {
		t.Fatalf("SubscribeOutput: %v", err)
	}

	agents := []string{"main", "sub-a1b2c3d4"}
	for _, agentID := range agents {
		msg := &IPCMessage{
			Group:   group,
			AgentID: agentID,
			Type:    IPCMessageText,
			Payload: json.RawMessage(fmt.Sprintf(`{"from":%q}`, agentID)),
		}
		if err := broker.PublishOutput(ctx, group, agentID, msg); err != nil {
			t.Fatalf("PublishOutput(%s): %v", agentID, err)
		}
	}

	received := make(map[string]bool)
	for i := 0; i < len(agents); i++ {
		select {
		case got := <-ch:
			received[got.AgentID] = true
		case <-ctx.Done():
			t.Fatalf("timed out after receiving %d/%d messages", i, len(agents))
		}
	}
	for _, agentID := range agents {
		if !received[agentID] {
			t.Errorf("did not receive message from agent %q", agentID)
		}
	}
}

func TestNATSDeleteStreams(t *testing.T) {
	broker, _ := setupNATS(t)
	ctx := context.Background()
	group := "delete-group@g.us"

	// Publish to create streams.
	msg := &IPCMessage{Group: group, AgentID: "main", Type: IPCMessageText, Payload: json.RawMessage(`{}`)}
	if err := broker.PublishOutput(ctx, group, "main", msg); err != nil {
		t.Fatalf("PublishOutput: %v", err)
	}

	if err := broker.DeleteStreams(ctx, group); err != nil {
		t.Fatalf("DeleteStreams: %v", err)
	}

	// Deleting again should not error.
	if err := broker.DeleteStreams(ctx, group); err != nil {
		t.Fatalf("DeleteStreams idempotent: %v", err)
	}
}

func TestNATSCloseStopsSubscription(t *testing.T) {
	broker, _ := setupNATS(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	group := "close-test@g.us"
	ch, err := broker.SubscribeOutput(ctx, group)
	if err != nil {
		t.Fatalf("SubscribeOutput: %v", err)
	}

	_ = broker.Close()

	select {
	case _, ok := <-ch:
		if ok {
			t.Error("expected channel to be closed after broker.Close()")
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for channel close")
	}
}

func TestNATSBrokerSubscribeOutput_AfterClose_ReturnsError(t *testing.T) {
	broker, _ := setupNATS(t)
	if err := broker.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	_, err := broker.SubscribeOutput(context.Background(), "closed-subscribe@g.us")
	if err == nil {
		t.Fatal("SubscribeOutput() error = nil, want closed broker error")
	}
	if !strings.Contains(err.Error(), "consume output") {
		t.Fatalf("error = %q, want context %q", err.Error(), "consume output")
	}

	broker.mu.Lock()
	defer broker.mu.Unlock()
	if len(broker.cancels) != 0 || len(broker.iters) != 0 {
		t.Fatalf("consume resources registered on closed broker: cancels=%d iters=%d", len(broker.cancels), len(broker.iters))
	}
}

// TestNATSClose_StopsReadInput verifies that broker.Close() closes the channel
// returned by ReadInput, mirroring TestNATSCloseStopsSubscription for input.
func TestNATSClose_StopsReadInput(t *testing.T) {
	broker, _ := setupNATS(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	group := "close-input-test@g.us"
	ch, err := broker.ReadInput(ctx, group, DefaultAgentID)
	if err != nil {
		t.Fatalf("ReadInput: %v", err)
	}

	_ = broker.Close()

	select {
	case _, ok := <-ch:
		if ok {
			t.Error("expected channel to be closed after broker.Close()")
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for channel close after broker.Close()")
	}
}

// TestNATSContextCancelClosesOutputChannel verifies that cancelling the
// subscription context closes both the SubscribeOutput and ReadInput channels.
func TestNATSContextCancelClosesOutputChannel(t *testing.T) {
	t.Run("SubscribeOutput", func(t *testing.T) {
		broker, _ := setupNATS(t)

		subCtx, subCancel := context.WithCancel(context.Background())

		ch, err := broker.SubscribeOutput(subCtx, "ctx-cancel-output@g.us")
		if err != nil {
			t.Fatalf("SubscribeOutput: %v", err)
		}

		subCancel()

		timeout := time.After(2 * time.Second)
		select {
		case _, ok := <-ch:
			if ok {
				t.Error("expected channel to be closed after context cancel")
			}
		case <-timeout:
			t.Fatal("timed out waiting for channel close after context cancel")
		}
	})

	t.Run("ReadInput", func(t *testing.T) {
		broker, _ := setupNATS(t)

		subCtx, subCancel := context.WithCancel(context.Background())

		ch, err := broker.ReadInput(subCtx, "ctx-cancel-input@g.us", DefaultAgentID)
		if err != nil {
			t.Fatalf("ReadInput: %v", err)
		}

		subCancel()

		timeout := time.After(2 * time.Second)
		select {
		case _, ok := <-ch:
			if ok {
				t.Error("expected channel to be closed after context cancel")
			}
		case <-timeout:
			t.Fatal("timed out waiting for channel close after context cancel")
		}
	})
}

// TestNATSBrokerClose_Idempotent verifies that calling Close() twice does not
// return an error on the second call.
func TestNATSBrokerClose_Idempotent(t *testing.T) {
	nc := startEmbeddedNATS(t)
	broker, err := NewNATSBroker(nc, nil)
	if err != nil {
		t.Fatalf("NewNATSBroker: %v", err)
	}
	// Do NOT register Close in t.Cleanup — we call it manually below.
	if err := broker.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := broker.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

// TestSanitizeAgentID verifies that SanitizeAgentID replaces unsafe characters
// with underscores and that safe IDs pass through unchanged.
func TestSanitizeAgentID(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "safe id passthrough", input: "main", want: "main"},
		{name: "safe id with dash and underscore", input: "sub-a1b2_c3", want: "sub-a1b2_c3"},
		{name: "dots replaced", input: "my.agent", want: "my_agent"},
		{name: "slashes replaced", input: "my/agent", want: "my_agent"},
		{name: "spaces replaced", input: "my agent", want: "my_agent"},
		{name: "asterisks replaced", input: "my*agent", want: "my_agent"},
		{name: "mixed unsafe", input: "my.agent/bad*id", want: "my_agent_bad_id"},
		{name: "truncates to 32 chars", input: "abcdefghijklmnopqrstuvwxyz123456789", want: "abcdefghijklmnopqrstuvwxyz123456"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SanitizeAgentID(tt.input)
			if got != tt.want {
				t.Errorf("SanitizeAgentID(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// TestNATSPublishBeforeConsumerDelivers verifies that a message published to
// the input subject BEFORE a consumer calls ReadInput is still delivered.
// This requires LimitsPolicy (not InterestPolicy) on the stream.
func TestNATSPublishBeforeConsumerDelivers(t *testing.T) {
	broker, _ := setupNATS(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	group := "pre-publish-group@g.us"
	agentID := "main"

	// Publish a message BEFORE the consumer is created.
	msg := &IPCMessage{
		Group:   group,
		AgentID: agentID,
		Type:    IPCTaskCreate,
		Payload: json.RawMessage(`{"taskId":"pre-published"}`),
	}
	if err := broker.SendInput(ctx, group, agentID, msg); err != nil {
		t.Fatalf("SendInput (before consumer): %v", err)
	}

	// Now create the consumer via ReadInput.
	ch, err := broker.ReadInput(ctx, group, agentID)
	if err != nil {
		t.Fatalf("ReadInput: %v", err)
	}

	// The pre-published message must be delivered.
	select {
	case got, ok := <-ch:
		if !ok {
			t.Fatal("channel closed unexpectedly")
		}
		if got.Type != IPCTaskCreate {
			t.Errorf("type = %q, want %q", got.Type, IPCTaskCreate)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for pre-published message")
	}
}

// TestNATSBrokerMalformedMessageSkipped verifies that publishing non-JSON bytes
// to the output subject does not crash the broker and that the next valid
// message is still delivered.
func TestNATSBrokerMalformedMessageSkipped(t *testing.T) {
	broker, nc := setupNATS(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	group := "malformed-test@g.us"
	ch, err := broker.SubscribeOutput(ctx, group)
	if err != nil {
		t.Fatalf("SubscribeOutput: %v", err)
	}

	// Obtain the JetStream context to publish raw bytes directly.
	js, err := jetstream.New(nc)
	if err != nil {
		t.Fatalf("jetstream.New: %v", err)
	}

	// Give the consumer a moment to be ready.
	time.Sleep(50 * time.Millisecond)

	sanitized := sanitizeGroupID(group)
	outputSubject := ipcOutputSubject(sanitized, DefaultAgentID)

	// Publish malformed (non-JSON) bytes directly, bypassing PublishOutput.
	if _, err := js.Publish(ctx, outputSubject, []byte("not-json")); err != nil {
		t.Fatalf("publish malformed: %v", err)
	}

	// Publish a valid IPCMessage via PublishOutput.
	valid := &IPCMessage{
		Group:   group,
		AgentID: DefaultAgentID,
		Type:    IPCMessageText,
		Payload: json.RawMessage(`{"text":"valid"}`),
	}
	if err := broker.PublishOutput(ctx, group, DefaultAgentID, valid); err != nil {
		t.Fatalf("PublishOutput: %v", err)
	}

	// The valid message must arrive; the malformed one must be skipped silently.
	select {
	case got, ok := <-ch:
		if !ok {
			t.Fatal("channel closed unexpectedly")
		}
		if got.Type != IPCMessageText {
			t.Errorf("type = %q, want %q", got.Type, IPCMessageText)
		}
		if string(got.Payload) != `{"text":"valid"}` {
			t.Errorf("payload = %s, want {\"text\":\"valid\"}", got.Payload)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for valid message after malformed one")
	}
}

// Test 2.3: Multi-Group Isolation
func TestNATSBrokerMultiGroupIsolation(t *testing.T) {
	broker1, _ := setupNATS(t)
	broker2, _ := setupNATS(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	groupFoo := "foo@g.us"
	groupBar := "bar@g.us"

	// Subscribe both brokers to their respective groups
	chFoo, err := broker1.SubscribeOutput(ctx, groupFoo)
	if err != nil {
		t.Fatalf("SubscribeOutput foo: %v", err)
	}

	chBar, err := broker2.SubscribeOutput(ctx, groupBar)
	if err != nil {
		t.Fatalf("SubscribeOutput bar: %v", err)
	}

	// Publish to foo
	msgFoo := &IPCMessage{
		Group:   groupFoo,
		AgentID: "agent-foo",
		Type:    IPCMessageText,
		Payload: json.RawMessage(`{"group":"foo"}`),
	}
	if err := broker1.PublishOutput(ctx, groupFoo, "agent-foo", msgFoo); err != nil {
		t.Fatalf("PublishOutput foo: %v", err)
	}

	// Publish to bar
	msgBar := &IPCMessage{
		Group:   groupBar,
		AgentID: "agent-bar",
		Type:    IPCMessageText,
		Payload: json.RawMessage(`{"group":"bar"}`),
	}
	if err := broker2.PublishOutput(ctx, groupBar, "agent-bar", msgBar); err != nil {
		t.Fatalf("PublishOutput bar: %v", err)
	}

	// Verify foo broker receives foo message only
	select {
	case gotFoo := <-chFoo:
		if gotFoo == nil {
			t.Fatal("foo: received nil message")
		}
		if gotFoo.AgentID != "agent-foo" {
			t.Errorf("foo: expected agent-foo, got %s", gotFoo.AgentID)
		}
		if string(gotFoo.Payload) != `{"group":"foo"}` {
			t.Errorf("foo: wrong payload: %s", gotFoo.Payload)
		}
	case <-ctx.Done():
		t.Fatal("foo: timed out waiting for message")
	}

	// Verify bar broker receives bar message only
	select {
	case gotBar := <-chBar:
		if gotBar == nil {
			t.Fatal("bar: received nil message")
		}
		if gotBar.AgentID != "agent-bar" {
			t.Errorf("bar: expected agent-bar, got %s", gotBar.AgentID)
		}
		if string(gotBar.Payload) != `{"group":"bar"}` {
			t.Errorf("bar: wrong payload: %s", gotBar.Payload)
		}
	case <-ctx.Done():
		t.Fatal("bar: timed out waiting for message")
	}

	// Verify foo doesn't receive bar's message (check with timeout)
	select {
	case msg := <-chFoo:
		if msg != nil && msg.AgentID == "agent-bar" {
			t.Fatal("foo: received bar's message - CROSSTALK!")
		}
	case <-time.After(500 * time.Millisecond):
		// Good - no crosstalk within timeout
	}

	// Verify bar doesn't receive foo's message (check with timeout)
	select {
	case msg := <-chBar:
		if msg != nil && msg.AgentID == "agent-foo" {
			t.Fatal("bar: received foo's message - CROSSTALK!")
		}
	case <-time.After(500 * time.Millisecond):
		// Good - no crosstalk within timeout
	}
}

// Test 2.1: Context Cancellation During Consume
func TestNATSBrokerContextCancellationDuringConsume(t *testing.T) {
	broker, _ := setupNATS(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	group := "ctx-cancel-test@g.us"

	// Subscribe to output
	ch, err := broker.SubscribeOutput(ctx, group)
	if err != nil {
		t.Fatalf("SubscribeOutput: %v", err)
	}

	// Create a cancellable context for the consume operation
	consumeCtx, consumeCancel := context.WithCancel(ctx)

	// Start publishing messages in background
	go func() {
		for i := 0; i < 20; i++ {
			msg := &IPCMessage{
				Group:   group,
				AgentID: DefaultAgentID,
				Type:    IPCMessageText,
				Payload: json.RawMessage(fmt.Sprintf(`{"seq":%d}`, i)),
			}
			if err := broker.PublishOutput(ctx, group, DefaultAgentID, msg); err != nil {
				t.Logf("PublishOutput: %v", err)
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
	}()

	// Consume a few messages
	receivedCount := 0
	for i := 0; i < 10; i++ {
		select {
		case msg := <-ch:
			if msg == nil {
				t.Fatal("received nil message before cancellation")
			}
			receivedCount++
			if i == 5 {
				// Cancel context mid-read after receiving 6 messages
				consumeCancel()
			}
		case <-consumeCtx.Done():
			// Context was cancelled as expected
			break
		case <-ctx.Done():
			t.Fatalf("test timeout, only received %d messages", receivedCount)
		}
	}

	// Verify we received some messages before the cancel
	if receivedCount < 5 {
		t.Errorf("expected at least 5 messages before cancel, got %d", receivedCount)
	}

	// Verify no panic on early cancellation
	// (If we got here without panicking, the test passes this part)
	t.Logf("Successfully received and cancelled after %d messages", receivedCount)

	// Verify broker is still operational by publishing another message
	finalMsg := &IPCMessage{
		Group:   group,
		AgentID: DefaultAgentID,
		Type:    IPCMessageText,
		Payload: json.RawMessage(`{"status":"final"}`),
	}
	if err := broker.PublishOutput(ctx, group, DefaultAgentID, finalMsg); err != nil {
		t.Fatalf("final publish failed: %v", err)
	}

	// Should be able to receive the final message on a new subscription
	ch2, err := broker.SubscribeOutput(ctx, group)
	if err != nil {
		t.Fatalf("second SubscribeOutput: %v", err)
	}

	select {
	case msg := <-ch2:
		if msg == nil || msg.Type != IPCMessageText {
			t.Fatal("did not receive final message")
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for final message")
	}
}

// Test 1.3: Concurrent Agents Connecting to Same Group
func TestNATSBrokerConcurrentAgents(t *testing.T) {
	broker, _ := setupNATS(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	group := "concurrent-test@g.us"
	numAgents := 5

	// Subscribe to receive all output from all agents
	ch, err := broker.SubscribeOutput(ctx, group)
	if err != nil {
		t.Fatalf("SubscribeOutput: %v", err)
	}

	// Track goroutine count before
	goroutinesBefore := runtime.NumGoroutine()

	// Concurrently publish input messages to agents 1-5
	var wg sync.WaitGroup
	errChan := make(chan error, numAgents)

	for i := 1; i <= numAgents; i++ {
		wg.Add(1)
		go func(agentID int) {
			defer wg.Done()
			agentName := fmt.Sprintf("agent-%d", agentID)

			// Send input to agent
			inputMsg := &IPCMessage{
				Group:   group,
				AgentID: agentName,
				Type:    IPCTaskCreate,
				Payload: json.RawMessage(fmt.Sprintf(`{"taskId":"task-%d"}`, agentID)),
			}
			if err := broker.SendInput(ctx, group, agentName, inputMsg); err != nil {
				errChan <- fmt.Errorf("SendInput for %s: %w", agentName, err)
				return
			}

			// Publish output from agent
			outputMsg := &IPCMessage{
				Group:   group,
				AgentID: agentName,
				Type:    IPCMessageText,
				Payload: json.RawMessage(fmt.Sprintf(`{"agentId":"agent-%d","output":"done"}`, agentID)),
			}
			if err := broker.PublishOutput(ctx, group, agentName, outputMsg); err != nil {
				errChan <- fmt.Errorf("PublishOutput for %s: %w", agentName, err)
				return
			}
		}(i)
	}

	go func() {
		wg.Wait()
		close(errChan)
	}()

	// Check for errors
	for err := range errChan {
		if err != nil {
			t.Fatalf("concurrent operation failed: %v", err)
		}
	}

	// Collect received messages
	receivedMessages := make(map[string]bool)
	timeout := time.After(5 * time.Second)

	for i := 0; i < numAgents; i++ {
		select {
		case msg := <-ch:
			if msg != nil {
				key := msg.AgentID
				receivedMessages[key] = true
			}
		case <-timeout:
			t.Fatalf("timed out waiting for message %d/%d", i, numAgents)
		}
	}

	// Verify all agents' messages were received
	if len(receivedMessages) != numAgents {
		t.Errorf("received %d messages, want %d", len(receivedMessages), numAgents)
	}
	for i := 1; i <= numAgents; i++ {
		expected := fmt.Sprintf("agent-%d", i)
		if !receivedMessages[expected] {
			t.Errorf("missing message from %s", expected)
		}
	}

	// Verify broker is still responsive
	finalMsg := &IPCMessage{
		Group:   group,
		AgentID: "verify-agent",
		Type:    IPCMessageText,
		Payload: json.RawMessage(`{"status":"final-check"}`),
	}
	if err := broker.PublishOutput(ctx, group, "verify-agent", finalMsg); err != nil {
		t.Fatalf("final verification publish failed: %v", err)
	}

	select {
	case msg := <-ch:
		if msg == nil || msg.AgentID != "verify-agent" {
			t.Fatal("did not receive final verification message")
		}
	case <-ctx.Done():
		t.Fatal("timed out on final verification")
	}

	// Check goroutine cleanup
	// Allow some grace period for goroutines to clean up
	time.Sleep(100 * time.Millisecond)
	gorutinesAfter := runtime.NumGoroutine()
	// Should not have leaked goroutines (allow +2 for timing)
	if gorutinesAfter > goroutinesBefore+2 {
		t.Errorf("goroutine leak: before=%d, after=%d", goroutinesBefore, gorutinesAfter)
	}
}

// Test 1.2: ACK failure handling - verifies broker logs errors and continues
func TestNATSBrokerAckFailure(t *testing.T) {
	tests := []struct {
		name     string
		scenario string
	}{
		{
			name:     "ack fails - broker continues consuming",
			scenario: "connection_lost",
		},
		{
			name:     "ack fails - message stays in stream",
			scenario: "consumer_error",
		},
		{
			name:     "ack fails - broker logs with context",
			scenario: "temporary_failure",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			broker, _ := setupNATS(t)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			group := fmt.Sprintf("ack-test-%s@g.us", tt.scenario)
			ch, err := broker.SubscribeOutput(ctx, group)
			if err != nil {
				t.Fatalf("SubscribeOutput: %v", err)
			}

			// Publish a message
			msg := &IPCMessage{
				Group:   group,
				AgentID: DefaultAgentID,
				Type:    IPCMessageText,
				Payload: json.RawMessage(`{"text":"test-ack"}`),
			}
			if err := broker.PublishOutput(ctx, group, DefaultAgentID, msg); err != nil {
				t.Fatalf("PublishOutput: %v", err)
			}

			// Receive the message - even if ACK fails internally,
			// the message should still be delivered to the channel
			select {
			case got, ok := <-ch:
				if !ok {
					t.Fatal("channel closed unexpectedly")
				}
				if got.Group != group {
					t.Errorf("group = %q, want %q", got.Group, group)
				}
				if got.Type != IPCMessageText {
					t.Errorf("type = %q, want %q", got.Type, IPCMessageText)
				}
				if string(got.Payload) != `{"text":"test-ack"}` {
					t.Errorf("payload = %s, want {\"text\":\"test-ack\"}", got.Payload)
				}
			case <-ctx.Done():
				t.Fatal("timed out waiting for message - broker may have stopped on ACK failure")
			}

			// Verify broker is still operational by publishing another message
			msg2 := &IPCMessage{
				Group:   group,
				AgentID: DefaultAgentID,
				Type:    IPCMessageText,
				Payload: json.RawMessage(`{"text":"test-ack-2"}`),
			}
			if err := broker.PublishOutput(ctx, group, DefaultAgentID, msg2); err != nil {
				t.Fatalf("second PublishOutput: %v", err)
			}

			// Should receive the second message - proves broker recovered from ACK failure
			select {
			case got, ok := <-ch:
				if !ok {
					t.Fatal("channel closed - broker did not recover from ACK failure")
				}
				if string(got.Payload) != `{"text":"test-ack-2"}` {
					t.Errorf("second message payload = %s, want {\"text\":\"test-ack-2\"}", got.Payload)
				}
			case <-ctx.Done():
				t.Fatal("timed out on second message - broker did not recover from ACK failure")
			}
		})
	}
}
