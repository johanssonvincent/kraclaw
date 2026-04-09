package integration_test

import (
	"context"
	"testing"
	"time"

	"github.com/johanssonvincent/kraclaw/internal/store"
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
	t.Skip("replaced by unit tests with embedded NATS server in internal/ipc and internal/queue packages")
}
