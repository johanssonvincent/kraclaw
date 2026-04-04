package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	nats "github.com/nats-io/nats.go"
	natserver "github.com/nats-io/nats-server/v2/server"
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
