package integration_test

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/johanssonvincent/kraclaw/internal/ipc"
	"github.com/johanssonvincent/kraclaw/internal/queue"
)

// mockGAS satisfies the unexported queue.groupActiveStore interface via Go
// structural typing, allowing the integration test to create NATSQueue without
// a real MySQL store.
type mockGAS struct {
	mu     sync.Mutex
	active map[string]bool
}

func newMockGAS() *mockGAS { return &mockGAS{active: make(map[string]bool)} }

func (m *mockGAS) MarkGroupActive(_ context.Context, jid string) error {
	m.mu.Lock()
	m.active[jid] = true
	m.mu.Unlock()
	return nil
}

func (m *mockGAS) MarkGroupInactive(_ context.Context, jid string) error {
	m.mu.Lock()
	delete(m.active, jid)
	m.mu.Unlock()
	return nil
}

func (m *mockGAS) IsGroupActive(_ context.Context, jid string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.active[jid], nil
}

func (m *mockGAS) ActiveGroupCount(_ context.Context) (int64, error) { return 0, nil }

func (m *mockGAS) ActiveGroupJIDs(_ context.Context) ([]string, error) { return nil, nil }

// TestNATSIPCAndQueueRoundTrip validates NATSBroker and NATSQueue end-to-end
// against a real embedded NATS JetStream server. This is TST-04.
func TestNATSIPCAndQueueRoundTrip(t *testing.T) {
	env := requireIntegrationEnv(t)
	ctx := context.Background()

	t.Run("queue round-trip", func(t *testing.T) {
		q, err := queue.NewNATSQueue(env.natsConn, newMockGAS(), nil)
		if err != nil {
			t.Fatalf("NewNATSQueue: %v", err)
		}
		t.Cleanup(func() { _ = q.Close() })

		groupJID := "integration-queue-roundtrip@g.us"
		want := &queue.QueueMessage{
			GroupJID:  groupJID,
			Content:   "integration test message",
			Sender:    "test-sender",
			Timestamp: time.Now().UTC().Truncate(time.Millisecond),
		}
		if err := q.Enqueue(ctx, groupJID, want); err != nil {
			t.Fatalf("Enqueue: %v", err)
		}

		got, err := q.Dequeue(ctx, groupJID)
		if err != nil {
			t.Fatalf("Dequeue: %v", err)
		}
		if got == nil {
			t.Fatal("Dequeue returned nil, expected message")
		}
		if got.Content != want.Content {
			t.Errorf("Content = %q, want %q", got.Content, want.Content)
		}
		if got.Sender != want.Sender {
			t.Errorf("Sender = %q, want %q", got.Sender, want.Sender)
		}
		if got.GroupJID != want.GroupJID {
			t.Errorf("GroupJID = %q, want %q", got.GroupJID, want.GroupJID)
		}
	})

	t.Run("ipc broker output round-trip", func(t *testing.T) {
		broker, err := ipc.NewNATSBroker(env.natsConn, nil)
		if err != nil {
			t.Fatalf("NewNATSBroker: %v", err)
		}
		t.Cleanup(func() { _ = broker.Close() })

		group := "integration-ipc-output"
		// SubscribeOutput BEFORE PublishOutput — required by LimitsPolicy with DeliverAllPolicy.
		ch, err := broker.SubscribeOutput(ctx, group)
		if err != nil {
			t.Fatalf("SubscribeOutput: %v", err)
		}

		want := &ipc.IPCMessage{
			Group:   group,
			Type:    ipc.IPCMessageText,
			Payload: json.RawMessage(`{"text":"hello from agent"}`),
		}
		if err := broker.PublishOutput(ctx, group, ipc.DefaultAgentID, want); err != nil {
			t.Fatalf("PublishOutput: %v", err)
		}

		select {
		case got := <-ch:
			if got.Group != want.Group {
				t.Errorf("Group = %q, want %q", got.Group, want.Group)
			}
			if got.Type != want.Type {
				t.Errorf("Type = %q, want %q", got.Type, want.Type)
			}
			if string(got.Payload) != string(want.Payload) {
				t.Errorf("Payload = %s, want %s", got.Payload, want.Payload)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("timeout waiting for IPC output message")
		}
	})

	t.Run("ipc broker input round-trip", func(t *testing.T) {
		broker, err := ipc.NewNATSBroker(env.natsConn, nil)
		if err != nil {
			t.Fatalf("NewNATSBroker: %v", err)
		}
		t.Cleanup(func() { _ = broker.Close() })

		group := "integration-ipc-input"
		// ReadInput BEFORE SendInput — required by LimitsPolicy with DeliverAllPolicy.
		ch, err := broker.ReadInput(ctx, group, ipc.DefaultAgentID)
		if err != nil {
			t.Fatalf("ReadInput: %v", err)
		}

		want := &ipc.IPCMessage{
			Group:   group,
			Type:    ipc.IPCShutdown,
			Payload: json.RawMessage(`{}`),
		}
		if err := broker.SendInput(ctx, group, ipc.DefaultAgentID, want); err != nil {
			t.Fatalf("SendInput: %v", err)
		}

		select {
		case got := <-ch:
			if got.Group != want.Group {
				t.Errorf("Group = %q, want %q", got.Group, want.Group)
			}
			if got.Type != want.Type {
				t.Errorf("Type = %q, want %q", got.Type, want.Type)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("timeout waiting for IPC input message")
		}
	})

	// subscribe-before-publish regression: LimitsPolicy IPC streams with DeliverAllPolicy
	// require a consumer to exist before publish, otherwise messages are lost
	// (InterestPolicy regression). This sub-test guards that SubscribeOutput
	// (which creates the durable consumer) is always called before PublishOutput.
	t.Run("subscribe-before-publish regression", func(t *testing.T) {
		broker, err := ipc.NewNATSBroker(env.natsConn, nil)
		if err != nil {
			t.Fatalf("NewNATSBroker: %v", err)
		}
		t.Cleanup(func() { _ = broker.Close() })

		group := "integration-regression-guard"
		// Subscribe FIRST — creates the durable consumer so messages are not lost.
		ch, err := broker.SubscribeOutput(ctx, group)
		if err != nil {
			t.Fatalf("SubscribeOutput: %v", err)
		}

		want := &ipc.IPCMessage{
			Group:   group,
			Type:    ipc.IPCMessageText,
			Payload: json.RawMessage(`{"text":"regression guard"}`),
		}
		// Publish AFTER subscribe — consumer exists, message will be delivered.
		if err := broker.PublishOutput(ctx, group, ipc.DefaultAgentID, want); err != nil {
			t.Fatalf("PublishOutput: %v", err)
		}

		select {
		case got := <-ch:
			if got.Type != want.Type {
				t.Errorf("Type = %q, want %q", got.Type, want.Type)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("regression: message lost — SubscribeOutput must be called before PublishOutput on LimitsPolicy IPC streams")
		}
	})
}
