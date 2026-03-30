package integration_test

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"github.com/johanssonvincent/kraclaw/internal/ipc"
	"github.com/johanssonvincent/kraclaw/internal/queue"
	"github.com/johanssonvincent/kraclaw/internal/store"
	"github.com/redis/go-redis/v9"
)

func TestIntegrationMySQLStoreRoundTrip(t *testing.T) {
	env := requireIntegrationEnv(t)

	s, err := store.NewMySQLStore(env.mysqlDSN, 8, 4, time.Minute)
	if err != nil {
		t.Fatalf("new mysql store: %v", err)
	}
	t.Cleanup(func() {
		_ = s.Close()
	})

	ctx := context.Background()
	group := &store.Group{
		JID:            "integration-group@g.us",
		Name:           "Integration Group",
		Folder:         "integration-group",
		TriggerPattern: "!kr",
		AddedAt:        time.Now().UTC().Truncate(time.Second),
		ContainerConfig: &store.ContainerConfig{
			Timeout: 4000,
			Model:   "claude-sonnet-4",
		},
		RequiresTrigger: true,
	}

	if err := s.UpsertGroup(ctx, group); err != nil {
		t.Fatalf("upsert group: %v", err)
	}

	gotGroup, err := s.GetGroup(ctx, group.JID)
	if err != nil {
		t.Fatalf("get group: %v", err)
	}
	if gotGroup == nil {
		t.Fatal("expected group, got nil")
	}
	if gotGroup.Folder != group.Folder {
		t.Fatalf("group folder = %q, want %q", gotGroup.Folder, group.Folder)
	}
	if gotGroup.ContainerConfig == nil || gotGroup.ContainerConfig.Model != "claude-sonnet-4" {
		t.Fatalf("group container config not persisted: %+v", gotGroup.ContainerConfig)
	}

	since := time.Now().UTC().Add(-1 * time.Minute).Truncate(time.Microsecond)
	messages := []store.Message{
		{
			ID:         "msg-1",
			ChatJID:    "integration-chat@g.us",
			Sender:     "alice",
			SenderName: "Alice",
			Content:    "hello integration",
			Timestamp:  since.Add(10 * time.Second),
		},
		{
			ID:           "msg-2",
			ChatJID:      "integration-chat@g.us",
			Sender:       "bot",
			SenderName:   "Bot",
			Content:      "hidden",
			Timestamp:    since.Add(20 * time.Second),
			IsBotMessage: true,
		},
		{
			ID:         "msg-3",
			ChatJID:    "integration-chat@g.us",
			Sender:     "alice",
			SenderName: "Alice",
			Content:    "",
			Timestamp:  since.Add(30 * time.Second),
		},
	}
	if err := s.StoreBatch(ctx, messages); err != nil {
		t.Fatalf("store batch: %v", err)
	}

	gotMessages, err := s.GetMessagesSince(ctx, "integration-chat@g.us", since, 50)
	if err != nil {
		t.Fatalf("get messages since: %v", err)
	}
	if len(gotMessages) != 1 {
		t.Fatalf("messages len = %d, want 1", len(gotMessages))
	}
	if gotMessages[0].ID != "msg-1" {
		t.Fatalf("message id = %q, want msg-1", gotMessages[0].ID)
	}
}

func TestIntegrationRedisQueueAndIPCBroker(t *testing.T) {
	env := requireIntegrationEnv(t)

	rdb := redis.NewClient(&redis.Options{Addr: env.redisAddr})
	t.Cleanup(func() {
		_ = rdb.Close()
	})

	q := queue.NewRedisQueue(rdb, slog.Default())
	t.Cleanup(func() {
		_ = q.Close()
	})

	b := ipc.NewRedisBroker(rdb, slog.Default())
	t.Cleanup(func() {
		_ = b.Close()
	})

	group := fmt.Sprintf("integration-group-%d", time.Now().UnixNano())
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	qmsg := &queue.QueueMessage{
		GroupJID:  group,
		Content:   "queued payload",
		Sender:    "integration-user",
		Timestamp: time.Now().UTC().Truncate(time.Millisecond),
	}
	if err := q.Enqueue(ctx, group, qmsg); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	deq, err := q.Dequeue(ctx, group)
	if err != nil {
		t.Fatalf("dequeue: %v", err)
	}
	if deq == nil || deq.Content != qmsg.Content {
		t.Fatalf("dequeued message mismatch: %+v", deq)
	}

	out, err := b.SubscribeOutput(ctx, group)
	if err != nil {
		t.Fatalf("subscribe output: %v", err)
	}

	ipcMessage := &ipc.IPCMessage{
		Group:   group,
		Type:    ipc.IPCMessageText,
		Payload: json.RawMessage(`{"text":"hello from integration"}`),
	}
	if err := b.PublishOutput(ctx, group, ipcMessage); err != nil {
		t.Fatalf("publish output: %v", err)
	}

	select {
	case got := <-out:
		if got == nil {
			t.Fatal("expected output message, got nil")
		}
		if got.Type != ipc.IPCMessageText {
			t.Fatalf("output type = %q, want %q", got.Type, ipc.IPCMessageText)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for output message")
	}

	closed, err := b.CheckCloseSignal(ctx, group)
	if err != nil {
		t.Fatalf("check close signal: %v", err)
	}
	if closed {
		t.Fatal("close signal should be false before set")
	}

	if err := b.SetCloseSignal(ctx, group); err != nil {
		t.Fatalf("set close signal: %v", err)
	}

	closed, err = b.CheckCloseSignal(ctx, group)
	if err != nil {
		t.Fatalf("check close signal after set: %v", err)
	}
	if !closed {
		t.Fatal("close signal should be true after set")
	}
}
