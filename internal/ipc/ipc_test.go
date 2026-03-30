package ipc

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func setup(t *testing.T) (*miniredis.Miniredis, *RedisBroker) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	broker := NewRedisBroker(rdb, slog.Default())
	t.Cleanup(func() { _ = broker.Close() })
	return mr, broker
}

func TestPublishAndSubscribeOutput(t *testing.T) {
	_, broker := setup(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	group := "test-group"
	ch, err := broker.SubscribeOutput(ctx, group)
	if err != nil {
		t.Fatalf("SubscribeOutput: %v", err)
	}

	msg := &IPCMessage{
		Group:   group,
		Type:    IPCMessageText,
		Payload: json.RawMessage(`{"text":"hello"}`),
	}
	if err := broker.PublishOutput(ctx, group, msg); err != nil {
		t.Fatalf("PublishOutput: %v", err)
	}

	select {
	case got := <-ch:
		if got.Type != IPCMessageText {
			t.Errorf("got type %q, want %q", got.Type, IPCMessageText)
		}
		if got.Group != group {
			t.Errorf("got group %q, want %q", got.Group, group)
		}
		if string(got.Payload) != `{"text":"hello"}` {
			t.Errorf("got payload %s, want %s", got.Payload, `{"text":"hello"}`)
		}
		if got.ID == "" {
			t.Error("expected non-empty stream ID")
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for message")
	}
}

func TestSendAndReadInput(t *testing.T) {
	_, broker := setup(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	group := "input-group"
	ch, err := broker.ReadInput(ctx, group)
	if err != nil {
		t.Fatalf("ReadInput: %v", err)
	}

	msg := &IPCMessage{
		Group:   group,
		Type:    IPCTaskCreate,
		Payload: json.RawMessage(`{"taskId":"abc"}`),
	}
	if err := broker.SendInput(ctx, group, msg); err != nil {
		t.Fatalf("SendInput: %v", err)
	}

	select {
	case got := <-ch:
		if got.Type != IPCTaskCreate {
			t.Errorf("got type %q, want %q", got.Type, IPCTaskCreate)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for input message")
	}
}

func TestCloseSignal(t *testing.T) {
	_, broker := setup(t)
	ctx := context.Background()
	group := "close-group"

	// Initially not set.
	closed, err := broker.CheckCloseSignal(ctx, group)
	if err != nil {
		t.Fatalf("CheckCloseSignal: %v", err)
	}
	if closed {
		t.Error("expected close signal to be false initially")
	}

	// Set it.
	if err := broker.SetCloseSignal(ctx, group); err != nil {
		t.Fatalf("SetCloseSignal: %v", err)
	}

	closed, err = broker.CheckCloseSignal(ctx, group)
	if err != nil {
		t.Fatalf("CheckCloseSignal: %v", err)
	}
	if !closed {
		t.Error("expected close signal to be true after setting")
	}
}

func TestMultipleMessages(t *testing.T) {
	_, broker := setup(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	group := "multi-group"
	ch, err := broker.SubscribeOutput(ctx, group)
	if err != nil {
		t.Fatalf("SubscribeOutput: %v", err)
	}

	for i := 0; i < 3; i++ {
		msg := &IPCMessage{
			Group:   group,
			Type:    IPCMessageText,
			Payload: json.RawMessage(`{"i":` + string(rune('0'+i)) + `}`),
		}
		if err := broker.PublishOutput(ctx, group, msg); err != nil {
			t.Fatalf("PublishOutput[%d]: %v", i, err)
		}
	}

	for i := 0; i < 3; i++ {
		select {
		case got := <-ch:
			if got.Type != "message" {
				t.Errorf("message %d: got type %q, want %q", i, got.Type, "message")
			}
		case <-ctx.Done():
			t.Fatalf("timed out waiting for message %d", i)
		}
	}
}

func TestCloseStopsSubscription(t *testing.T) {
	_, broker := setup(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	group := "close-test"
	ch, err := broker.SubscribeOutput(ctx, group)
	if err != nil {
		t.Fatalf("SubscribeOutput: %v", err)
	}

	_ = broker.Close()

	// Channel should be closed eventually.
	select {
	case _, ok := <-ch:
		if ok {
			t.Error("expected channel to be closed")
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for channel close")
	}
}

func TestDeleteStreams(t *testing.T) {
	mr, broker := setup(t)
	ctx := context.Background()
	group := "delete-group"

	// Publish a message so the output stream exists.
	msg := &IPCMessage{
		Group:   group,
		Type:    IPCMessageText,
		Payload: json.RawMessage(`{"text":"hello"}`),
	}
	if err := broker.PublishOutput(ctx, group, msg); err != nil {
		t.Fatalf("PublishOutput: %v", err)
	}
	// Send an input message so the input stream exists.
	if err := broker.SendInput(ctx, group, msg); err != nil {
		t.Fatalf("SendInput: %v", err)
	}

	// Verify streams exist.
	if !mr.Exists(outputKey(group)) {
		t.Fatal("expected output stream to exist before delete")
	}
	if !mr.Exists(inputKey(group)) {
		t.Fatal("expected input stream to exist before delete")
	}

	// Delete streams.
	if err := broker.DeleteStreams(ctx, group); err != nil {
		t.Fatalf("DeleteStreams: %v", err)
	}

	// Verify streams are gone.
	if mr.Exists(outputKey(group)) {
		t.Error("expected output stream to be deleted")
	}
	if mr.Exists(inputKey(group)) {
		t.Error("expected input stream to be deleted")
	}
}

func TestDeleteStreams_Idempotent(t *testing.T) {
	_, broker := setup(t)
	ctx := context.Background()

	// Deleting streams for a group that never had any should not error.
	if err := broker.DeleteStreams(ctx, "nonexistent-group"); err != nil {
		t.Fatalf("DeleteStreams on nonexistent group: %v", err)
	}
}

func TestCancelDeregister(t *testing.T) {
	_, broker := setup(t)
	ctx, cancel := context.WithCancel(context.Background())

	group := "deregister-group"
	_, err := broker.SubscribeOutput(ctx, group)
	if err != nil {
		t.Fatalf("SubscribeOutput: %v", err)
	}

	// Wait briefly for the goroutine to register.
	time.Sleep(50 * time.Millisecond)

	broker.mu.Lock()
	if len(broker.cancels) != 1 {
		t.Fatalf("expected 1 cancel entry, got %d", len(broker.cancels))
	}
	broker.mu.Unlock()

	// Cancel the parent context, which causes the goroutine to exit and deregister.
	cancel()

	// Wait for the goroutine to clean up. The goroutine may be blocked on
	// XReadGroup with readTimeout (500ms), so allow enough time for it to
	// notice cancellation and exit.
	time.Sleep(1 * time.Second)

	broker.mu.Lock()
	if len(broker.cancels) != 0 {
		t.Errorf("expected 0 cancel entries after goroutine exit, got %d", len(broker.cancels))
	}
	broker.mu.Unlock()
}

func TestClose_CancelsAllGoroutines(t *testing.T) {
	_, broker := setup(t)
	ctx := context.Background()

	// Start multiple subscriptions.
	for i := 0; i < 3; i++ {
		group := fmt.Sprintf("close-all-%d", i)
		if _, err := broker.SubscribeOutput(ctx, group); err != nil {
			t.Fatalf("SubscribeOutput[%d]: %v", i, err)
		}
	}

	// Wait for goroutines to register.
	time.Sleep(50 * time.Millisecond)

	broker.mu.Lock()
	if len(broker.cancels) != 3 {
		t.Fatalf("expected 3 cancel entries, got %d", len(broker.cancels))
	}
	broker.mu.Unlock()

	// Close should cancel all.
	_ = broker.Close()

	// Wait for goroutines to clean up.
	time.Sleep(100 * time.Millisecond)

	broker.mu.Lock()
	count := len(broker.cancels)
	broker.mu.Unlock()
	// After Close(), cancels should be nil or empty.
	if count != 0 {
		t.Errorf("expected 0 cancel entries after Close, got %d", count)
	}
}

func TestKeySchemas(t *testing.T) {
	tests := []struct {
		fn   func(string) string
		arg  string
		want string
	}{
		{outputKey, "grp1", "kraclaw:ipc:grp1:output"},
		{inputKey, "grp1", "kraclaw:ipc:grp1:input"},
		{closeKey, "grp1", "kraclaw:ipc:grp1:close"},
	}
	for _, tt := range tests {
		got := tt.fn(tt.arg)
		if got != tt.want {
			t.Errorf("got %q, want %q", got, tt.want)
		}
	}
}
