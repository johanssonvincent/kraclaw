package queue

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	nats "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	natserver "github.com/nats-io/nats-server/v2/server"

	"github.com/johanssonvincent/kraclaw/internal/store"
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

// errActiveStore is a groupActiveStore that always returns store.ErrGroupNotFound
// for MarkGroupActive and MarkGroupInactive, simulating a missing JID.
type errActiveStore struct {
	mockActiveStore
}

func (e *errActiveStore) MarkGroupActive(_ context.Context, _ string) error {
	return fmt.Errorf("mark group active: %w", store.ErrGroupNotFound)
}

func (e *errActiveStore) MarkGroupInactive(_ context.Context, _ string) error {
	return fmt.Errorf("mark group inactive: %w", store.ErrGroupNotFound)
}

// TestNATSQueueClose_StopsSubscribe verifies that q.Close() closes the channel
// returned by Subscribe.
func TestNATSQueueClose_StopsSubscribe(t *testing.T) {
	nc := startQueueNATS(t)
	gas := newMockActiveStore()
	q, err := NewNATSQueue(nc, gas, nil)
	if err != nil {
		t.Fatalf("NewNATSQueue: %v", err)
	}
	// Do NOT register q.Close with t.Cleanup — we close it explicitly below.

	ctx := context.Background()
	ch, err := q.Subscribe(ctx)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	if err := q.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	select {
	case _, ok := <-ch:
		if ok {
			t.Error("expected channel to be closed after q.Close()")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for Subscribe channel to close after q.Close()")
	}
}

// TestNATSQueueMarkActive_UnknownJID verifies that MarkActive and MarkInactive
// propagate store.ErrGroupNotFound when the backing store returns it.
func TestNATSQueueMarkActive_UnknownJID(t *testing.T) {
	nc := startQueueNATS(t)
	gas := &errActiveStore{}
	gas.active = make(map[string]bool)
	q, err := NewNATSQueue(nc, gas, nil)
	if err != nil {
		t.Fatalf("NewNATSQueue: %v", err)
	}
	t.Cleanup(func() { _ = q.Close() })

	ctx := context.Background()

	if err := q.MarkActive(ctx, "unknown@g.us"); !errors.Is(err, store.ErrGroupNotFound) {
		t.Errorf("MarkActive unknown JID: got %v, want errors.Is(err, store.ErrGroupNotFound)", err)
	}
	if err := q.MarkInactive(ctx, "unknown@g.us"); !errors.Is(err, store.ErrGroupNotFound) {
		t.Errorf("MarkInactive unknown JID: got %v, want errors.Is(err, store.ErrGroupNotFound)", err)
	}
}

// TestNATSQueueSubscribe_ActiveEvents verifies that EventActive and EventInactive
// are published to the Subscribe channel when MarkActive/MarkInactive are called.
func TestNATSQueueSubscribe_ActiveEvents(t *testing.T) {
	q, _ := setupNATSQueue(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	group := "active-event-group@g.us"
	ch, err := q.Subscribe(ctx)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	if err := q.MarkActive(ctx, group); err != nil {
		t.Fatalf("MarkActive: %v", err)
	}
	select {
	case evt := <-ch:
		if evt.Type != EventActive {
			t.Errorf("expected EventActive, got %q", evt.Type)
		}
		if evt.GroupJID != group {
			t.Errorf("expected group %q, got %q", group, evt.GroupJID)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for EventActive")
	}

	if err := q.MarkInactive(ctx, group); err != nil {
		t.Fatalf("MarkInactive: %v", err)
	}
	select {
	case evt := <-ch:
		if evt.Type != EventInactive {
			t.Errorf("expected EventInactive, got %q", evt.Type)
		}
		if evt.GroupJID != group {
			t.Errorf("expected group %q, got %q", group, evt.GroupJID)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for EventInactive")
	}
}

// TestNATSQueuePeekThenDequeue is the regression test for the durable-consumer
// Peek bug: after the fix, Peek must not lock the message from Dequeue.
func TestNATSQueuePeekThenDequeue(t *testing.T) {
	q, _ := setupNATSQueue(t)
	ctx := context.Background()
	group := "peek-dequeue-group@g.us"

	msg := &QueueMessage{
		GroupJID:  group,
		Content:   "regression message",
		Sender:    "u1",
		Timestamp: time.Now().Truncate(time.Millisecond),
	}

	if err := q.Enqueue(ctx, group, msg); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	// Peek must not remove the message.
	peeked, err := q.Peek(ctx, group)
	if err != nil {
		t.Fatalf("Peek: %v", err)
	}
	if peeked == nil {
		t.Fatal("Peek returned nil after Enqueue")
	}
	if peeked.Content != msg.Content {
		t.Errorf("Peek content = %q, want %q", peeked.Content, msg.Content)
	}

	n, err := q.Len(ctx, group)
	if err != nil {
		t.Fatalf("Len after Peek: %v", err)
	}
	if n != 1 {
		t.Errorf("Len after Peek = %d, want 1", n)
	}

	// Dequeue must still be able to consume the message (not locked by Peek).
	got, err := q.Dequeue(ctx, group)
	if err != nil {
		t.Fatalf("Dequeue: %v", err)
	}
	if got == nil {
		t.Fatal("Dequeue returned nil — message was locked by Peek (regression)")
	}
	if got.Content != msg.Content {
		t.Errorf("Dequeue content = %q, want %q", got.Content, msg.Content)
	}

	n, err = q.Len(ctx, group)
	if err != nil {
		t.Fatalf("Len after Dequeue: %v", err)
	}
	if n != 0 {
		t.Errorf("Len after Dequeue = %d, want 0", n)
	}

	// Second Dequeue on empty queue must return nil.
	empty, err := q.Dequeue(ctx, group)
	if err != nil {
		t.Fatalf("second Dequeue: %v", err)
	}
	if empty != nil {
		t.Errorf("second Dequeue = %+v, want nil (empty queue)", empty)
	}
}

// TestNATSQueueDequeue_MalformedMessage verifies that Dequeue returns a non-nil
// error when the queued payload is not valid JSON, and that a subsequent valid
// message can still be dequeued successfully (gap 9).
func TestNATSQueueDequeue_MalformedMessage(t *testing.T) {
	q, _ := setupNATSQueue(t)
	ctx := context.Background()
	groupJID := "malformed-dequeue@g.us"

	// Ensure the JetStream stream exists.
	sanitized, err := q.ensureStream(ctx, groupJID)
	if err != nil {
		t.Fatalf("ensureStream: %v", err)
	}

	// Inject raw non-JSON bytes directly via JetStream, bypassing Enqueue.
	js, err := jetstream.New(q.nc)
	if err != nil {
		t.Fatalf("jetstream.New: %v", err)
	}
	subj := queueSubject(sanitized)
	if _, err := js.Publish(ctx, subj, []byte("not-json{{{bad")); err != nil {
		t.Fatalf("publish malformed: %v", err)
	}

	// Dequeue must return a non-nil error for the malformed message.
	msg, err := q.Dequeue(ctx, groupJID)
	if err == nil {
		t.Errorf("expected error for malformed message, got msg=%v", msg)
	}

	// Publish a valid message; Dequeue must now succeed.
	if err := q.Enqueue(ctx, groupJID, &QueueMessage{GroupJID: groupJID, Content: "hello"}); err != nil {
		t.Fatalf("Enqueue valid: %v", err)
	}
	got, err := q.Dequeue(ctx, groupJID)
	if err != nil {
		t.Fatalf("Dequeue valid after malformed: %v", err)
	}
	if got == nil || got.Content != "hello" {
		t.Errorf("expected valid message with Content=hello, got %v", got)
	}
}

// TestNATSQueueClose_Idempotent verifies that calling Close() twice does not
// return an error on the second call (gap 12).
func TestNATSQueueClose_Idempotent(t *testing.T) {
	nc := startQueueNATS(t)
	gas := newMockActiveStore()
	q, err := NewNATSQueue(nc, gas, nil)
	if err != nil {
		t.Fatalf("NewNATSQueue: %v", err)
	}
	// Do NOT register Close in t.Cleanup — we call it manually below.
	if err := q.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := q.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}
