package channel

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"sync"
)

// DefaultRegistry is the package-level registry used by channel init() functions.
var DefaultRegistry = NewRegistry()

// Registry holds channel factories and creates channel instances.
type Registry struct {
	mu        sync.RWMutex
	factories map[string]Factory
}

// NewRegistry creates a new empty Registry.
func NewRegistry() *Registry {
	return &Registry{
		factories: make(map[string]Factory),
	}
}

// Register adds a channel factory under the given name.
func (r *Registry) Register(name string, factory Factory) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.factories[name] = factory
}

// Get returns the factory for the given name.
func (r *Registry) Get(name string) (Factory, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	f, ok := r.factories[name]
	return f, ok
}

// Names returns all registered channel names in sorted order.
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.factories))
	for name := range r.factories {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// ConnectAll creates and connects all registered channels.
func (r *Registry) ConnectAll(ctx context.Context, cfg ChannelConfig) ([]Channel, error) {
	r.mu.RLock()
	names := make([]string, 0, len(r.factories))
	for name := range r.factories {
		names = append(names, name)
	}
	factories := make(map[string]Factory, len(r.factories))
	for k, v := range r.factories {
		factories[k] = v
	}
	r.mu.RUnlock()

	sort.Strings(names)

	var channels []Channel
	for _, name := range names {
		ch, err := factories[name](cfg)
		if err != nil {
			// Disconnect any already-connected channels.
			for _, c := range channels {
				_ = c.Disconnect(ctx)
			}
			return nil, fmt.Errorf("channel %q: factory failed: %w", name, err)
		}
		if ch == nil {
			slog.Info("channel not configured, skipping", "channel", name)
			continue
		}
		if err := ch.Connect(ctx); err != nil {
			for _, c := range channels {
				_ = c.Disconnect(ctx)
			}
			return nil, fmt.Errorf("channel %q: connect failed: %w", name, err)
		}
		channels = append(channels, ch)
	}
	return channels, nil
}
