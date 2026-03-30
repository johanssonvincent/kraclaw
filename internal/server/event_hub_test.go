package server

import (
	"fmt"
	"log/slog"
	"sync"
	"testing"

	kraclawv1 "github.com/johanssonvincent/kraclaw/pkg/pb/kraclawv1"
)

func TestEventHub_PublishSubscribe(t *testing.T) {
	hub := newEventHub(slog.Default())

	ch, unsub := hub.subscribe()
	defer unsub()

	hub.publish(&kraclawv1.Event{Type: "test-event"})

	select {
	case evt := <-ch:
		if evt.Type != "test-event" {
			t.Fatalf("got Type %q, want %q", evt.Type, "test-event")
		}
	default:
		t.Fatal("expected to receive event, got nothing")
	}
}

func TestEventHub_MultipleSubscribers(t *testing.T) {
	hub := newEventHub(slog.Default())

	ch1, unsub1 := hub.subscribe()
	defer unsub1()
	ch2, unsub2 := hub.subscribe()
	defer unsub2()

	hub.publish(&kraclawv1.Event{Type: "multi"})

	for i, ch := range []<-chan *kraclawv1.Event{ch1, ch2} {
		select {
		case evt := <-ch:
			if evt.Type != "multi" {
				t.Fatalf("subscriber %d: got Type %q, want %q", i, evt.Type, "multi")
			}
		default:
			t.Fatalf("subscriber %d: expected event, got nothing", i)
		}
	}
}

func TestEventHub_Unsubscribe(t *testing.T) {
	hub := newEventHub(slog.Default())

	ch, unsub := hub.subscribe()
	unsub()

	hub.publish(&kraclawv1.Event{Type: "after-unsub"})

	_, ok := <-ch
	if ok {
		t.Fatal("expected channel to be closed after unsubscribe")
	}

	hub.mu.RLock()
	defer hub.mu.RUnlock()
	if len(hub.subs) != 0 {
		t.Fatalf("expected 0 subscribers, got %d", len(hub.subs))
	}
}

func TestEventHub_SlowSubscriberDrop(t *testing.T) {
	hub := newEventHub(slog.Default())

	ch, unsub := hub.subscribe()
	defer unsub()

	// Publish 65 events without reading; buffer is 64, so 65th should be dropped.
	for i := 0; i < 65; i++ {
		hub.publish(&kraclawv1.Event{Type: fmt.Sprintf("evt-%d", i)})
	}

	if got := len(ch); got != 64 {
		t.Fatalf("expected buffer len 64, got %d", got)
	}

	// First event in buffer should be evt-0.
	evt := <-ch
	if evt.Type != "evt-0" {
		t.Fatalf("got Type %q, want %q", evt.Type, "evt-0")
	}
}

func TestEventHub_ConcurrentAccess(t *testing.T) {
	hub := newEventHub(slog.Default())

	var wg sync.WaitGroup
	for g := 0; g < 10; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ch, unsub := hub.subscribe()
			defer unsub()

			for i := 0; i < 10; i++ {
				hub.publish(&kraclawv1.Event{Type: fmt.Sprintf("concurrent-%d", i)})
			}

			// Drain available events.
			for {
				select {
				case <-ch:
				default:
					return
				}
			}
		}()
	}
	wg.Wait()
	// Test passes if it completes without panic or deadlock.
}
