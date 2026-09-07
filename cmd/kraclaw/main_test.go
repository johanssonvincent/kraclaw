package main

import (
	"log/slog"
	"testing"
	"time"

	"github.com/johanssonvincent/kraclaw/internal/config"
	natserver "github.com/nats-io/nats-server/v2/server"
)

func startAuthRequiredNATSServer(t *testing.T) string {
	t.Helper()
	opts := &natserver.Options{
		Host:     "127.0.0.1",
		Port:     -1,
		Username: "natsuser",
		Password: "natspass",
	}
	s, err := natserver.NewServer(opts)
	if err != nil {
		t.Fatalf("new nats server: %v", err)
	}
	go s.Start()
	if !s.ReadyForConnections(5 * time.Second) {
		t.Fatal("nats server not ready")
	}
	t.Cleanup(s.Shutdown)
	return s.ClientURL()
}

func TestConnectNATS(t *testing.T) {
	serverURL := startAuthRequiredNATSServer(t)
	cases := []struct {
		name string
		cfg  config.NATSConfig
		want bool
	}{
		{"no credentials fails", config.NATSConfig{URL: serverURL}, false},
		{"correct credentials connect", config.NATSConfig{URL: serverURL, User: "natsuser", Password: "natspass"}, true},
		{"wrong password fails", config.NATSConfig{URL: serverURL, User: "natsuser", Password: "wrong"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			nc, err := connectNATS(tc.cfg, slog.Default())
			if !tc.want {
				if err == nil {
					if nc != nil {
						nc.Close()
					}
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("connectNATS: %v", err)
			}
			if nc == nil {
				t.Fatal("expected non-nil connection")
			}
			t.Cleanup(func() { nc.Close() })
		})
	}
}
