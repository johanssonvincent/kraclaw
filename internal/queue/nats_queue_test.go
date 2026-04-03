package queue

import (
	"context"
	"sync"
	"testing"
	"time"

	nats "github.com/nats-io/nats.go"
	natserver "github.com/nats-io/nats-server/v2/server"
)

// startQueueNATS starts an in-process NATS server with JetStream for queue tests.
func startQueueNATS(t *testing.T) *nats.Conn {
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

// mockActiveStore is a simple in-memory implementation of groupActiveStore for tests.
type mockActiveStore struct {
	mu     sync.Mutex
	active map[string]bool
}

func newMockActiveStore() *mockActiveStore {
	return &mockActiveStore{active: make(map[string]bool)}
}

func (m *mockActiveStore) MarkGroupActive(_ context.Context, jid string) error {
	m.mu.Lock()
	m.active[jid] = true
	m.mu.Unlock()
	return nil
}

func (m *mockActiveStore) MarkGroupInactive(_ context.Context, jid string) error {
	m.mu.Lock()
	m.active[jid] = false
	m.mu.Unlock()
	return nil
}

func (m *mockActiveStore) IsGroupActive(_ context.Context, jid string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.active[jid], nil
}

func (m *mockActiveStore) ActiveGroupCount(_ context.Context) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var count int64
	for _, v := range m.active {
		if v {
			count++
		}
	}
	return count, nil
}

func (m *mockActiveStore) ActiveGroupJIDs(_ context.Context) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var jids []string
	for jid, v := range m.active {
		if v {
			jids = append(jids, jid)
		}
	}
	return jids, nil
}

func setupNATSQueue(t *testing.T) (*NATSQueue, *mockActiveStore) {
	t.Helper()
	nc := startQueueNATS(t)
	store := newMockActiveStore()
	q, err := NewNATSQueue(nc, store, nil)
	if err != nil {
		t.Fatalf("NewNATSQueue: %v", err)
	}
	t.Cleanup(func() { _ = q.Close() })
	return q, store
}

func TestNATSQueueEnqueueDequeue(t *testing.T) {
	q, _ := setupNATSQueue(t)
	ctx := context.Background()
	group := "queue-test@g.us"

	msg := &QueueMessage{
		GroupJID:  group,
		Content:   "hello nats",
		Sender:    "user1",
		Timestamp: time.Now().Truncate(time.Millisecond),
	}

	if err := q.Enqueue(ctx, group, msg); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	n, err := q.Len(ctx, group)
	if err != nil {
		t.Fatalf("Len: %v", err)
	}
	if n != 1 {
		t.Errorf("Len = %d, want 1", n)
	}

	got, err := q.Dequeue(ctx, group)
	if err != nil {
		t.Fatalf("Dequeue: %v", err)
	}
	if got == nil {
		t.Fatal("Dequeue returned nil")
	}
	if got.Content != msg.Content {
		t.Errorf("Content = %q, want %q", got.Content, msg.Content)
	}
	if got.Sender != msg.Sender {
		t.Errorf("Sender = %q, want %q", got.Sender, msg.Sender)
	}
}

func TestNATSQueueDequeue_Empty(t *testing.T) {
	q, _ := setupNATSQueue(t)
	ctx := context.Background()

	got, err := q.Dequeue(ctx, "empty-group@g.us")
	if err != nil {
		t.Fatalf("Dequeue on empty: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil on empty dequeue, got %+v", got)
	}
}

func TestNATSQueuePeek(t *testing.T) {
	q, _ := setupNATSQueue(t)
	ctx := context.Background()
	group := "peek-group@g.us"

	msg := &QueueMessage{GroupJID: group, Content: "peek me", Sender: "u", Timestamp: time.Now()}

	// Peek on empty should return nil.
	got, err := q.Peek(ctx, group)
	if err != nil {
		t.Fatalf("Peek empty: %v", err)
	}
	if got != nil {
		t.Errorf("Peek empty = %+v, want nil", got)
	}

	if err := q.Enqueue(ctx, group, msg); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	// Peek should return message without removing it.
	got, err = q.Peek(ctx, group)
	if err != nil {
		t.Fatalf("Peek: %v", err)
	}
	if got == nil {
		t.Fatal("Peek returned nil after enqueue")
	}
	if got.Content != msg.Content {
		t.Errorf("Peek Content = %q, want %q", got.Content, msg.Content)
	}

	// Message should still be in the queue.
	n, _ := q.Len(ctx, group)
	if n != 1 {
		t.Errorf("Len after Peek = %d, want 1", n)
	}
}

func TestNATSQueueSubscribe_EnqueuedEvent(t *testing.T) {
	q, _ := setupNATSQueue(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	group := "event-group@g.us"
	events, err := q.Subscribe(ctx)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	msg := &QueueMessage{GroupJID: group, Content: "trigger event", Sender: "u", Timestamp: time.Now()}
	if err := q.Enqueue(ctx, group, msg); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	select {
	case evt := <-events:
		if evt.Type != EventEnqueued {
			t.Errorf("event type = %q, want %q", evt.Type, EventEnqueued)
		}
		if evt.GroupJID != group {
			t.Errorf("event group = %q, want %q", evt.GroupJID, group)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for enqueued event")
	}
}

func TestNATSQueueMarkActive(t *testing.T) {
	q, store := setupNATSQueue(t)
	ctx := context.Background()
	group := "active-test@g.us"

	active, _ := q.IsActive(ctx, group)
	if active {
		t.Error("expected inactive initially")
	}

	if err := q.MarkActive(ctx, group); err != nil {
		t.Fatalf("MarkActive: %v", err)
	}

	active, _ = q.IsActive(ctx, group)
	if !active {
		t.Error("expected active after MarkActive")
	}

	count, _ := q.ActiveCount(ctx)
	if count != 1 {
		t.Errorf("ActiveCount = %d, want 1", count)
	}

	if err := q.MarkInactive(ctx, group); err != nil {
		t.Fatalf("MarkInactive: %v", err)
	}

	active, _ = store.IsGroupActive(ctx, group)
	if active {
		t.Error("expected inactive in store after MarkInactive")
	}
}

func TestNATSQueueActiveJIDs(t *testing.T) {
	q, _ := setupNATSQueue(t)
	ctx := context.Background()

	groups := []string{"a@g.us", "b@g.us", "c@g.us"}
	for _, g := range groups {
		if err := q.MarkActive(ctx, g); err != nil {
			t.Fatalf("MarkActive(%s): %v", g, err)
		}
	}

	jids, err := q.ActiveJIDs(ctx)
	if err != nil {
		t.Fatalf("ActiveJIDs: %v", err)
	}
	if len(jids) != 3 {
		t.Errorf("len(ActiveJIDs) = %d, want 3", len(jids))
	}
}
