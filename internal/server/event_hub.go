package server

import (
	"log/slog"
	"sync"

	kraclawv1 "github.com/johanssonvincent/kraclaw/pkg/pb/kraclawv1"
)

type eventHub struct {
	mu     sync.RWMutex
	nextID int
	subs   map[int]chan *kraclawv1.Event
	log    *slog.Logger
}

func newEventHub(log *slog.Logger) *eventHub {
	return &eventHub{
		subs: make(map[int]chan *kraclawv1.Event),
		log:  log.With("component", "event-hub"),
	}
}

func (h *eventHub) publish(evt *kraclawv1.Event) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	for id, ch := range h.subs {
		select {
		case ch <- evt:
		default:
			h.log.Warn("dropping event for slow subscriber", "subscriber_id", id, "event_type", evt.Type)
		}
	}
}

func (h *eventHub) subscribe() (<-chan *kraclawv1.Event, func()) {
	h.mu.Lock()
	defer h.mu.Unlock()

	id := h.nextID
	h.nextID++

	ch := make(chan *kraclawv1.Event, 64)
	h.subs[id] = ch

	return ch, func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		if ch, ok := h.subs[id]; ok {
			delete(h.subs, id)
			close(ch)
		}
	}
}
