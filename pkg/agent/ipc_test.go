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

	client := NewIPCClient(rdb, "testgroup")

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

	client := NewIPCClient(rdb, "testgroup")

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

	ch, err := client.ReadInput(ctx)
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

	client := NewIPCClient(rdb, "testgroup")

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
