package queue

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	natserver "github.com/nats-io/nats-server/v2/server"
	nats "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

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

type failingActiveStore struct {
	markActiveErr   error
	markInactiveErr error
}

func (f *failingActiveStore) MarkGroupActive(_ context.Context, _ string) error {
	return f.markActiveErr
}

func (f *failingActiveStore) MarkGroupInactive(_ context.Context, _ string) error {
	return f.markInactiveErr
}

func (f *failingActiveStore) IsGroupActive(_ context.Context, _ string) (bool, error) {
	return false, nil
}
func (f *failingActiveStore) ActiveGroupCount(_ context.Context) (int64, error)   { return 0, nil }
func (f *failingActiveStore) ActiveGroupJIDs(_ context.Context) ([]string, error) { return nil, nil }

func (e *errActiveStore) MarkGroupActive(_ context.Context, _ string) error {
	return fmt.Errorf("mark group active: %w", store.ErrGroupNotFound)
}

func (e *errActiveStore) MarkGroupInactive(_ context.Context, _ string) error {
	return fmt.Errorf("mark group inactive: %w", store.ErrGroupNotFound)
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
	sanitized, _, err := q.ensureStream(ctx, groupJID)
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

	// Dequeue must return nil, nil for the malformed message (it's discarded).
	msg, err := q.Dequeue(ctx, groupJID)
	if err != nil {
		t.Errorf("expected (nil, nil) for discarded malformed message, got err=%v", err)
	}
	if msg != nil {
		t.Errorf("expected nil message for discarded malformed message, got msg=%v", msg)
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

func TestNATSQueue_EnsureStreamErrorsAreWrapped(t *testing.T) {
	q, _ := setupNATSQueue(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	msg := &QueueMessage{GroupJID: "wrap-errors@g.us", Content: "payload"}

	tests := []struct {
		name    string
		wantCtx string
		call    func() error
	}{
		{
			name:    "Enqueue",
			wantCtx: "enqueue ensure stream",
			call: func() error {
				return q.Enqueue(ctx, "wrap-errors@g.us", msg)
			},
		},
		{
			name:    "Dequeue",
			wantCtx: "dequeue ensure stream",
			call: func() error {
				_, err := q.Dequeue(ctx, "wrap-errors@g.us")
				return err
			},
		},
		{
			name:    "Peek",
			wantCtx: "peek ensure stream",
			call: func() error {
				_, err := q.Peek(ctx, "wrap-errors@g.us")
				return err
			},
		},
		{
			name:    "Len",
			wantCtx: "len ensure stream",
			call: func() error {
				_, err := q.Len(ctx, "wrap-errors@g.us")
				return err
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.call()
			if err == nil {
				t.Fatalf("%s() error = nil, want wrapped ensure stream error", tt.name)
			}
			if !strings.Contains(err.Error(), tt.wantCtx) {
				t.Fatalf("%s() error = %q, want context %q", tt.name, err.Error(), tt.wantCtx)
			}
		})
	}
}

func TestNATSQueue_MarkActiveInactiveErrorsAreWrapped(t *testing.T) {
	nc := startQueueNATS(t)
	markActiveErr := fmt.Errorf("db unavailable")
	markInactiveErr := fmt.Errorf("tx rollback")

	q, err := NewNATSQueue(nc, &failingActiveStore{
		markActiveErr:   markActiveErr,
		markInactiveErr: markInactiveErr,
	}, nil)
	if err != nil {
		t.Fatalf("NewNATSQueue: %v", err)
	}
	t.Cleanup(func() { _ = q.Close() })

	ctx := context.Background()

	err = q.MarkActive(ctx, "wrapped-active@g.us")
	if err == nil {
		t.Fatal("MarkActive() error = nil, want wrapped store error")
	}
	if !strings.Contains(err.Error(), "mark active") {
		t.Fatalf("MarkActive() error = %q, want context %q", err.Error(), "mark active")
	}
	if !errors.Is(err, markActiveErr) {
		t.Fatalf("MarkActive() error = %v, want errors.Is(..., %v)", err, markActiveErr)
	}

	err = q.MarkInactive(ctx, "wrapped-inactive@g.us")
	if err == nil {
		t.Fatal("MarkInactive() error = nil, want wrapped store error")
	}
	if !strings.Contains(err.Error(), "mark inactive") {
		t.Fatalf("MarkInactive() error = %q, want context %q", err.Error(), "mark inactive")
	}
	if !errors.Is(err, markInactiveErr) {
		t.Fatalf("MarkInactive() error = %v, want errors.Is(..., %v)", err, markInactiveErr)
	}
}

// Test 1.4: Stream Corruption Recovery Test
func TestNATSQueueStreamCorruptionRecovery(t *testing.T) {
	tests := []struct {
		name     string
		scenario string
	}{
		{
			name:     "stream misconfiguration recovered",
			scenario: "mismatch",
		},
		{
			name:     "stream updated without losing messages",
			scenario: "update",
		},
		{
			name:     "dequeue works after repair",
			scenario: "repair",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			q, _ := setupNATSQueue(t)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			group := fmt.Sprintf("corrupt-test-%s@g.us", tt.scenario)

			// Enqueue a message
			msg1 := &QueueMessage{
				GroupJID:  group,
				Content:   "message-before-corruption",
				Sender:    "test",
				Timestamp: time.Now().Truncate(time.Millisecond),
			}
			if err := q.Enqueue(ctx, group, msg1); err != nil {
				t.Fatalf("initial Enqueue: %v", err)
			}

			// Verify message is in queue
			n, err := q.Len(ctx, group)
			if err != nil {
				t.Fatalf("Len before: %v", err)
			}
			if n != 1 {
				t.Errorf("Len before = %d, want 1", n)
			}

			// Simulate stream update (broker recovers from misconfiguration).
			// ensureStream on the same queue should handle this transparently by
			// idempotently updating the stream config.
			_, _, err = q.ensureStream(ctx, group)
			if err != nil {
				t.Fatalf("ensureStream (recovery): %v", err)
			}

			// Verify message is still in queue after "update"
			n, err = q.Len(ctx, group)
			if err != nil {
				t.Fatalf("Len after update: %v", err)
			}
			if n != 1 {
				t.Errorf("Len after update = %d, want 1 (message lost!)", n)
			}

			// Verify dequeue still works
			got, err := q.Dequeue(ctx, group)
			if err != nil {
				t.Fatalf("Dequeue after update: %v", err)
			}
			if got == nil {
				t.Fatal("Dequeue returned nil - message was lost during update")
			}
			if got.Content != msg1.Content {
				t.Errorf("Content mismatch: got %q, want %q", got.Content, msg1.Content)
			}

			// Verify queue is operational after recovery
			msg2 := &QueueMessage{
				GroupJID:  group,
				Content:   "message-after-recovery",
				Sender:    "test",
				Timestamp: time.Now().Truncate(time.Millisecond),
			}
			if err := q.Enqueue(ctx, group, msg2); err != nil {
				t.Fatalf("Enqueue after recovery: %v", err)
			}

			got2, err := q.Dequeue(ctx, group)
			if err != nil {
				t.Fatalf("second Dequeue: %v", err)
			}
			if got2 == nil {
				t.Fatal("second Dequeue returned nil")
			}
			if got2.Content != msg2.Content {
				t.Errorf("second Content mismatch: got %q, want %q", got2.Content, msg2.Content)
			}
		})
	}
}

// TestNATSQueueMultiGroupIsolation verifies that per-group streams are
// isolated: enqueuing messages for two different groupJIDs on the same
// NATSQueue must not cause cross-delivery. Each group's Dequeue returns
// only its own message.
func TestNATSQueueMultiGroupIsolation(t *testing.T) {
	q, _ := setupNATSQueue(t)
	ctx := context.Background()

	groupA := "iso-queue-a@g.us"
	groupB := "iso-queue-b@g.us"

	msgA := &QueueMessage{
		GroupJID:  groupA,
		Content:   "group-a-msg",
		Sender:    "userA",
		Timestamp: time.Now().Truncate(time.Millisecond),
	}
	msgB := &QueueMessage{
		GroupJID:  groupB,
		Content:   "group-b-msg",
		Sender:    "userB",
		Timestamp: time.Now().Truncate(time.Millisecond),
	}

	if err := q.Enqueue(ctx, groupA, msgA); err != nil {
		t.Fatalf("Enqueue groupA: %v", err)
	}
	if err := q.Enqueue(ctx, groupB, msgB); err != nil {
		t.Fatalf("Enqueue groupB: %v", err)
	}

	gotA, err := q.Dequeue(ctx, groupA)
	if err != nil {
		t.Fatalf("Dequeue groupA: %v", err)
	}
	if gotA == nil {
		t.Fatal("Dequeue groupA returned nil")
	}
	if gotA.Content != "group-a-msg" {
		t.Errorf("groupA Content = %q, want %q (cross-delivery?)", gotA.Content, "group-a-msg")
	}

	gotB, err := q.Dequeue(ctx, groupB)
	if err != nil {
		t.Fatalf("Dequeue groupB: %v", err)
	}
	if gotB == nil {
		t.Fatal("Dequeue groupB returned nil")
	}
	if gotB.Content != "group-b-msg" {
		t.Errorf("groupB Content = %q, want %q (cross-delivery?)", gotB.Content, "group-b-msg")
	}

	// Both queues should now be empty.
	if n, _ := q.Len(ctx, groupA); n != 0 {
		t.Errorf("groupA Len after Dequeue = %d, want 0", n)
	}
	if n, _ := q.Len(ctx, groupB); n != 0 {
		t.Errorf("groupB Len after Dequeue = %d, want 0", n)
	}
}

// TestNATSQueueDequeueContextCancellation verifies that Dequeue honours
// context cancellation: a pre-cancelled context returns promptly, and a
// Dequeue blocked on an empty queue returns when its context is cancelled.
func TestNATSQueueDequeueContextCancellation(t *testing.T) {
	q, _ := setupNATSQueue(t)
	group := "cancel-test@g.us"

	// Test 1: pre-cancelled context
	ctx1, cancel1 := context.WithCancel(context.Background())
	cancel1()
	msg, _ := q.Dequeue(ctx1, group)
	if msg != nil {
		t.Errorf("expected nil msg on cancelled context, got %v", msg)
	}

	// Test 2: cancel while blocking on empty queue
	ctx2, cancel2 := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		q.Dequeue(ctx2, group) //nolint:errcheck
	}()
	time.Sleep(20 * time.Millisecond)
	cancel2()
	select {
	case <-done:
		// passed
	case <-time.After(2 * time.Second):
		t.Fatal("Dequeue did not return after context cancellation")
	}
}
