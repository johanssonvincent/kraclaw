package ipc

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	nats "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	natserver "github.com/nats-io/nats-server/v2/server"
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
