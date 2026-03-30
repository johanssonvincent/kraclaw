package queue

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func setup(t *testing.T) (*miniredis.Miniredis, *RedisQueue) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	q := NewRedisQueue(rdb, slog.Default())
	t.Cleanup(func() { _ = q.Close() })
	return mr, q
}

func TestEnqueueDequeue(t *testing.T) {
	_, q := setup(t)
	ctx := context.Background()
	group := "group1@s.whatsapp.net"

	msg := &QueueMessage{
		GroupJID:  group,
		Content:   "hello",
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
	if got.Content != "hello" {
		t.Errorf("Content = %q, want %q", got.Content, "hello")
	}
	if got.Sender != "user1" {
		t.Errorf("Sender = %q, want %q", got.Sender, "user1")
	}
}

func TestDequeueEmpty(t *testing.T) {
	_, q := setup(t)
	ctx := context.Background()

	got, err := q.Dequeue(ctx, "empty-group")
	if err != nil {
		t.Fatalf("Dequeue: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil from empty queue, got %+v", got)
	}
}

func TestPeek(t *testing.T) {
	_, q := setup(t)
	ctx := context.Background()
	group := "peek-group"

	msg := &QueueMessage{
		GroupJID:  group,
		Content:   "peek me",
		Sender:    "user1",
		Timestamp: time.Now().Truncate(time.Millisecond),
	}
	if err := q.Enqueue(ctx, group, msg); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	got, err := q.Peek(ctx, group)
	if err != nil {
		t.Fatalf("Peek: %v", err)
	}
	if got == nil {
		t.Fatal("Peek returned nil")
	}
	if got.Content != "peek me" {
		t.Errorf("Content = %q, want %q", got.Content, "peek me")
	}

	// Verify item is still in queue.
	n, _ := q.Len(ctx, group)
	if n != 1 {
		t.Errorf("Len after Peek = %d, want 1", n)
	}
}

func TestPeekEmpty(t *testing.T) {
	_, q := setup(t)
	ctx := context.Background()

	got, err := q.Peek(ctx, "empty")
	if err != nil {
		t.Fatalf("Peek: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil, got %+v", got)
	}
}

func TestFIFOOrder(t *testing.T) {
	_, q := setup(t)
	ctx := context.Background()
	group := "fifo-group"

	for i, content := range []string{"first", "second", "third"} {
		msg := &QueueMessage{
			GroupJID:  group,
			Content:   content,
			Sender:    "user1",
			Timestamp: time.Now().Add(time.Duration(i) * time.Second).Truncate(time.Millisecond),
		}
		if err := q.Enqueue(ctx, group, msg); err != nil {
			t.Fatalf("Enqueue[%d]: %v", i, err)
		}
	}

	for _, want := range []string{"first", "second", "third"} {
		got, err := q.Dequeue(ctx, group)
		if err != nil {
			t.Fatalf("Dequeue: %v", err)
		}
		if got.Content != want {
			t.Errorf("got %q, want %q", got.Content, want)
		}
	}
}

func TestActiveTracking(t *testing.T) {
	_, q := setup(t)
	ctx := context.Background()
	group := "active-group"

	active, err := q.IsActive(ctx, group)
	if err != nil {
		t.Fatalf("IsActive: %v", err)
	}
	if active {
		t.Error("expected inactive initially")
	}

	count, _ := q.ActiveCount(ctx)
	if count != 0 {
		t.Errorf("ActiveCount = %d, want 0", count)
	}

	if err := q.MarkActive(ctx, group); err != nil {
		t.Fatalf("MarkActive: %v", err)
	}

	active, _ = q.IsActive(ctx, group)
	if !active {
		t.Error("expected active after MarkActive")
	}

	count, _ = q.ActiveCount(ctx)
	if count != 1 {
		t.Errorf("ActiveCount = %d, want 1", count)
	}

	if err := q.MarkInactive(ctx, group); err != nil {
		t.Fatalf("MarkInactive: %v", err)
	}

	active, _ = q.IsActive(ctx, group)
	if active {
		t.Error("expected inactive after MarkInactive")
	}
}

func TestMultipleActiveGroups(t *testing.T) {
	_, q := setup(t)
	ctx := context.Background()

	groups := []string{"g1", "g2", "g3"}
	for _, g := range groups {
		if err := q.MarkActive(ctx, g); err != nil {
			t.Fatalf("MarkActive(%s): %v", g, err)
		}
	}

	count, _ := q.ActiveCount(ctx)
	if count != 3 {
		t.Errorf("ActiveCount = %d, want 3", count)
	}

	_ = q.MarkInactive(ctx, "g2")
	count, _ = q.ActiveCount(ctx)
	if count != 2 {
		t.Errorf("ActiveCount = %d, want 2", count)
	}
}

func TestMarkActiveIdempotent(t *testing.T) {
	_, q := setup(t)
	ctx := context.Background()
	group := "idem-group"

	_ = q.MarkActive(ctx, group)
	_ = q.MarkActive(ctx, group)

	count, _ := q.ActiveCount(ctx)
	if count != 1 {
		t.Errorf("ActiveCount = %d, want 1 (idempotent)", count)
	}
}

func TestTaskMessage(t *testing.T) {
	_, q := setup(t)
	ctx := context.Background()
	group := "task-group"

	msg := &QueueMessage{
		GroupJID:  group,
		Content:   "run scheduled task",
		Sender:    "scheduler",
		Timestamp: time.Now().Truncate(time.Millisecond),
		IsTask:    true,
		TaskID:    "task-123",
	}
	if err := q.Enqueue(ctx, group, msg); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	got, _ := q.Dequeue(ctx, group)
	if !got.IsTask {
		t.Error("expected IsTask to be true")
	}
	if got.TaskID != "task-123" {
		t.Errorf("TaskID = %q, want %q", got.TaskID, "task-123")
	}
}

func TestKeySchemas(t *testing.T) {
	tests := []struct {
		fn   func(string) string
		arg  string
		want string
	}{
		{queueKey, "g1", "kraclaw:queue:g1"},
	}
	for _, tt := range tests {
		got := tt.fn(tt.arg)
		if got != tt.want {
			t.Errorf("got %q, want %q", got, tt.want)
		}
	}
	if activeSetKey() != "kraclaw:active" {
		t.Errorf("activeSetKey = %q, want %q", activeSetKey(), "kraclaw:active")
	}
	if notifyChannel() != "kraclaw:queue:notify" {
		t.Errorf("notifyChannel = %q, want %q", notifyChannel(), "kraclaw:queue:notify")
	}
}
