package agent

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func TestIPCClient_SendOutput(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })

	client, err := NewIPCClient(rdb, "testgroup")
	if err != nil {
		t.Fatal(err)
	}

	msg := &OutboundMessage{
		Type: "message",
		Text: "Hello from agent",
	}

	if err := client.SendOutput(context.Background(), msg); err != nil {
		t.Fatalf("send output: %v", err)
	}

	entries, err := rdb.XRange(context.Background(), "kraclaw:ipc:testgroup:output", "-", "+").Result()
	if err != nil {
		t.Fatalf("xrange: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
}

func TestIPCClient_ReadInput(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })

	client, err := NewIPCClient(rdb, "testgroup")
	if err != nil {
		t.Fatal(err)
	}

	payload, _ := json.Marshal(map[string]string{"messages": "Hello"})
	data, _ := json.Marshal(map[string]interface{}{
		"group":   "testgroup",
		"type":    "message",
		"payload": json.RawMessage(payload),
	})
	rdb.XAdd(context.Background(), &redis.XAddArgs{
		Stream: "kraclaw:ipc:testgroup:input",
		Values: map[string]interface{}{"data": string(data)},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	ch, _, err := client.ReadInput(ctx)
	if err != nil {
		t.Fatalf("read input: %v", err)
	}

	select {
	case msg := <-ch:
		if msg.Type != "message" {
			t.Fatalf("expected type message, got %s", msg.Type)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for message")
	}
}

func TestIPCClient_CheckCloseSignal(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })

	client, err := NewIPCClient(rdb, "testgroup")
	if err != nil {
		t.Fatal(err)
	}

	closed, err := client.CheckCloseSignal(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if closed {
		t.Fatal("expected not closed")
	}

	rdb.Set(context.Background(), "kraclaw:ipc:testgroup:close", "1", 60*time.Second)

	closed, err = client.CheckCloseSignal(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !closed {
		t.Fatal("expected closed")
	}
}

func TestIPCClient_SendOutput_MessageContent(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })

	client, err := NewIPCClient(rdb, "testgroup")
	if err != nil {
		t.Fatal(err)
	}

	msg := &OutboundMessage{Type: "message", Text: "Hello from agent"}
	if err := client.SendOutput(context.Background(), msg); err != nil {
		t.Fatalf("send output: %v", err)
	}

	entries, err := rdb.XRange(context.Background(), "kraclaw:ipc:testgroup:output", "-", "+").Result()
	if err != nil {
		t.Fatalf("xrange: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}

	raw, ok := entries[0].Values["data"].(string)
	if !ok {
		t.Fatal("expected data field to be a string")
	}
	var ipcMsg struct {
		Group   string          `json:"group"`
		Type    string          `json:"type"`
		Payload json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal([]byte(raw), &ipcMsg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if ipcMsg.Group != "testgroup" {
		t.Fatalf("expected group testgroup, got %q", ipcMsg.Group)
	}
	if ipcMsg.Type != "message" {
		t.Fatalf("expected type message, got %q", ipcMsg.Type)
	}
	var payload struct{ Text string `json:"text"` }
	if err := json.Unmarshal(ipcMsg.Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload.Text != "Hello from agent" {
		t.Fatalf("expected 'Hello from agent', got %q", payload.Text)
	}
}

func TestReadInput_StopsAfterConsecutiveErrors(t *testing.T) {
	mr := miniredis.RunT(t)
	// Use short dial/read timeouts so each failed attempt resolves quickly.
	rdb := redis.NewClient(&redis.Options{
		Addr:         mr.Addr(),
		DialTimeout:  50 * time.Millisecond,
		ReadTimeout:  50 * time.Millisecond,
		MaxRetries:   0,
	})
	defer func() { _ = rdb.Close() }()

	client, err := NewIPCClient(rdb, "error-limit-test")
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	ch, errCh, err := client.ReadInput(ctx)
	if err != nil {
		t.Fatal(err)
	}

	// Close miniredis to cause persistent errors.
	mr.Close()

	// Channel should close when consecutive error limit is reached.
	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("expected channel to close, got a message")
		}
		// Channel closed — correct behavior.
	case <-ctx.Done():
		t.Fatal("timed out waiting for channel to close after persistent errors")
	}

	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("expected non-nil error from errCh")
		}
	default:
		t.Fatal("expected error on errCh")
	}
}

func TestNewIPCClient_Validation(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })

	_, err := NewIPCClient(nil, "group")
	if err == nil {
		t.Fatal("expected error for nil rdb")
	}
	_, err = NewIPCClient(rdb, "")
	if err == nil {
		t.Fatal("expected error for empty group")
	}
}
