package channel

import (
	"context"
	"errors"
	"testing"

	"github.com/johanssonvincent/kraclaw/internal/store"
)

type testChannel struct {
	name             string
	connectErr       error
	disconnectCalled bool
}

func (c *testChannel) Name() string                                            { return c.name }
func (c *testChannel) Connect(_ context.Context) error                         { return c.connectErr }
func (c *testChannel) SendMessage(_ context.Context, _ string, _ string) error { return nil }
func (c *testChannel) IsConnected() bool                                       { return true }
func (c *testChannel) OwnsJID(_ string) bool                                   { return false }
func (c *testChannel) Disconnect(_ context.Context) error                      { c.disconnectCalled = true; return nil }
func (c *testChannel) SetTyping(_ context.Context, _ string, _ bool) error     { return nil }

func validCfg() ChannelConfig {
	return ChannelConfig{
		OnMessage: func(string, *InboundMessage) {},
		Groups:    func() []store.Group { return nil },
	}
}

func TestRegistry_RegisterAndGet(t *testing.T) {
	reg := NewRegistry()
	reg.Register("discord", func(cfg ChannelConfig) (Channel, error) {
		return &testChannel{name: "discord"}, nil
	})

	f, ok := reg.Get("discord")
	if !ok || f == nil {
		t.Fatal("expected to find factory for 'discord'")
	}

	_, ok = reg.Get("telegram")
	if ok {
		t.Fatal("expected not to find factory for 'telegram'")
	}
}

func TestRegistry_Names(t *testing.T) {
	reg := NewRegistry()
	reg.Register("tui", func(cfg ChannelConfig) (Channel, error) { return nil, nil })
	reg.Register("discord", func(cfg ChannelConfig) (Channel, error) { return nil, nil })
	reg.Register("telegram", func(cfg ChannelConfig) (Channel, error) { return nil, nil })

	names := reg.Names()
	want := []string{"discord", "telegram", "tui"}
	if len(names) != len(want) {
		t.Fatalf("got %d names, want %d", len(names), len(want))
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("names[%d] = %q, want %q", i, names[i], want[i])
		}
	}
}

func TestRegistry_ConnectAll_Success(t *testing.T) {
	reg := NewRegistry()
	reg.Register("alpha", func(cfg ChannelConfig) (Channel, error) {
		return &testChannel{name: "alpha"}, nil
	})
	reg.Register("beta", func(cfg ChannelConfig) (Channel, error) {
		return &testChannel{name: "beta"}, nil
	})

	channels, err := reg.ConnectAll(context.Background(), validCfg())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(channels) != 2 {
		t.Fatalf("got %d channels, want 2", len(channels))
	}
	if channels[0].Name() != "alpha" {
		t.Fatalf("channels[0].Name() = %q, want %q", channels[0].Name(), "alpha")
	}
	if channels[1].Name() != "beta" {
		t.Fatalf("channels[1].Name() = %q, want %q", channels[1].Name(), "beta")
	}
}

func TestRegistry_ConnectAll_FactoryError(t *testing.T) {
	reg := NewRegistry()

	okCh := &testChannel{name: "a-ok"}
	reg.Register("a-ok", func(cfg ChannelConfig) (Channel, error) {
		return okCh, nil
	})
	reg.Register("b-fail", func(cfg ChannelConfig) (Channel, error) {
		return nil, errors.New("factory broke")
	})

	_, err := reg.ConnectAll(context.Background(), validCfg())
	if err == nil {
		t.Fatal("expected error from ConnectAll")
	}
	if got := err.Error(); !contains(got, `channel "b-fail"`) {
		t.Fatalf("error %q does not mention b-fail", got)
	}
	if !okCh.disconnectCalled {
		t.Fatal("expected a-ok to be disconnected on rollback")
	}
}

func TestRegistry_ConnectAll_ConnectError(t *testing.T) {
	reg := NewRegistry()

	aOk := &testChannel{name: "a-ok"}
	bOk := &testChannel{name: "b-ok"}
	reg.Register("a-ok", func(cfg ChannelConfig) (Channel, error) { return aOk, nil })
	reg.Register("b-ok", func(cfg ChannelConfig) (Channel, error) { return bOk, nil })
	reg.Register("c-fail", func(cfg ChannelConfig) (Channel, error) {
		return &testChannel{name: "c-fail", connectErr: errors.New("connect broke")}, nil
	})

	_, err := reg.ConnectAll(context.Background(), validCfg())
	if err == nil {
		t.Fatal("expected error from ConnectAll")
	}
	if got := err.Error(); !contains(got, `channel "c-fail"`) {
		t.Fatalf("error %q does not mention c-fail", got)
	}
	if !aOk.disconnectCalled {
		t.Fatal("expected a-ok to be disconnected on rollback")
	}
	if !bOk.disconnectCalled {
		t.Fatal("expected b-ok to be disconnected on rollback")
	}
}

func TestRegistry_ConnectAll_SkipsNilChannel(t *testing.T) {
	reg := NewRegistry()
	reg.Register("null-ch", func(cfg ChannelConfig) (Channel, error) { return nil, nil })
	reg.Register("real-ch", func(cfg ChannelConfig) (Channel, error) {
		return &testChannel{name: "real-ch"}, nil
	})

	channels, err := reg.ConnectAll(context.Background(), validCfg())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(channels) != 1 {
		t.Fatalf("got %d channels, want 1", len(channels))
	}
	if channels[0].Name() != "real-ch" {
		t.Fatalf("channels[0].Name() = %q, want %q", channels[0].Name(), "real-ch")
	}
}

func TestRegistry_ConnectAll_EmptyRegistry(t *testing.T) {
	reg := NewRegistry()

	channels, err := reg.ConnectAll(context.Background(), validCfg())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(channels) != 0 {
		t.Fatalf("got %d channels, want 0", len(channels))
	}
}

// contains is a small helper to check substring presence.
func contains(s, sub string) bool {
	return len(s) >= len(sub) && searchSubstring(s, sub)
}

func searchSubstring(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
