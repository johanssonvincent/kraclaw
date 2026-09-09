//go:build integration

package integration_test

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	natserver "github.com/nats-io/nats-server/v2/server"
	nats "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/johanssonvincent/kraclaw/internal/ipc"
	"github.com/johanssonvincent/kraclaw/pkg/agent"
)

// startColdStartNATS starts an embedded NATS+JetStream server for a single
// test. Separate from the docker-based setupIntegrationEnv (which is keyed by
// MySQL availability) — cold-start tests need only NATS, so they bypass that
// machinery to avoid pulling a MySQL image.
func startColdStartNATS(t *testing.T) (*nats.Conn, func()) {
	t.Helper()

	natsDir, err := os.MkdirTemp("", "kraclaw-coldstart-nats-*")
	if err != nil {
		t.Fatalf("mkdir temp: %v", err)
	}
	opts := &natserver.Options{
		JetStream: true,
		StoreDir:  natsDir,
		Port:      -1,
		NoLog:     true,
		NoSigs:    true,
	}
	srv, err := natserver.NewServer(opts)
	if err != nil {
		t.Fatalf("new nats server: %v", err)
	}
	go srv.Start()
	if !srv.ReadyForConnections(5 * time.Second) {
		srv.Shutdown()
		_ = os.RemoveAll(natsDir)
		t.Fatal("nats server not ready")
	}
	nc, err := nats.Connect(srv.ClientURL())
	if err != nil {
		srv.Shutdown()
		_ = os.RemoveAll(natsDir)
		t.Fatalf("nats connect: %v", err)
	}

	return nc, func() {
		nc.Close()
		srv.Shutdown()
		_ = os.RemoveAll(natsDir)
	}
}

// runColdStartFastStartPreCreated exercises the server-side
// EnsureStreamForAgent → agent-side fetch contract end-to-end over a real
// broker: with the consumer pre-created, ReadInput must succeed on the first
// fetch attempt without hitting the bounded-retry path, and the agent must
// receive input published through the broker.
func runColdStartFastStartPreCreated(t *testing.T, ctx context.Context, nc *nats.Conn, broker *ipc.NATSBroker, group, agentID string) {
	t.Helper()

	if err := broker.EnsureStreamForAgent(ctx, group, agentID); err != nil {
		t.Fatalf("EnsureStreamForAgent: %v", err)
	}

	client, err := agent.NewIPCClient(nc, group, agentID, slog.Default())
	if err != nil {
		t.Fatalf("NewIPCClient: %v", err)
	}

	msgCh, errCh, err := client.ReadInput(ctx)
	if err != nil {
		t.Fatalf("ReadInput err = %v, want nil (consumer was pre-created)", err)
	}
	if msgCh == nil || errCh == nil {
		t.Fatalf("channels = %v, %v; want non-nil", msgCh, errCh)
	}

	if err := broker.SendInput(ctx, group, agentID, &ipc.IPCMessage{
		Group:   group,
		AgentID: agentID,
		Type:    "text",
	}); err != nil {
		t.Fatalf("SendInput: %v", err)
	}

	select {
	case got := <-msgCh:
		if got == nil {
			t.Fatal("received nil message")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("agent did not receive the input message")
	}
}

// runColdStartLegacyDefensive proves the rollback path: with the defensive env
// set the agent re-enables its self-create ensureStream call and can attach
// without a prior broker-side EnsureStreamForAgent (the helm legacy path when
// sandbox.fastStart.enabled=false).
func runColdStartLegacyDefensive(t *testing.T, ctx context.Context, nc *nats.Conn, _ *ipc.NATSBroker, group, agentID string) {
	t.Helper()

	client, err := agent.NewIPCClient(nc, group, agentID, slog.Default())
	if err != nil {
		t.Fatalf("NewIPCClient: %v", err)
	}

	if err := client.SendOutput(ctx, &agent.OutboundMessage{Type: "test"}); err != nil {
		t.Fatalf("SendOutput err = %v, want nil (defensive path should create stream)", err)
	}

	js, err := jetstream.New(nc)
	if err != nil {
		t.Fatalf("jetstream: %v", err)
	}
	sanitized := ipc.SanitizeGroupID(group)
	streamName := "KRACLAW_IPC_" + strings.ToUpper(sanitized)
	if _, err := js.Stream(ctx, streamName); err != nil {
		t.Fatalf("stream %s not found after defensive create: %v", streamName, err)
	}
}

// runColdStartFetchExhaustion verifies that with no server-side pre-create and
// the defensive env unset, the agent's bounded fetch surfaces the terminal
// "fetch input consumer ... after retries" error rather than silently creating
// a consumer.
func runColdStartFetchExhaustion(t *testing.T, ctx context.Context, nc *nats.Conn, _ *ipc.NATSBroker, group, agentID string) {
	t.Helper()

	client, err := agent.NewIPCClient(nc, group, agentID, slog.Default())
	if err != nil {
		t.Fatalf("NewIPCClient: %v", err)
	}

	_, _, err = client.ReadInput(ctx)
	if err == nil {
		t.Fatal("ReadInput err = nil, want non-nil (no pre-create, no defensive create)")
	}
	if !strings.Contains(err.Error(), "fetch input consumer") {
		t.Errorf("err = %v, want substring \"fetch input consumer\"", err)
	}
}

// runColdStartEnsureStreamIdempotent proves repeated EnsureStreamForAgent
// calls (e.g. across retries or restarts) do not error or double-create; the
// orchestrator's bounded retry helper relies on this idempotence.
func runColdStartEnsureStreamIdempotent(t *testing.T, _ context.Context, _ *nats.Conn, broker *ipc.NATSBroker, group, agentID string) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	for attempt := 1; attempt <= 5; attempt++ {
		if err := broker.EnsureStreamForAgent(ctx, group, agentID); err != nil {
			t.Fatalf("EnsureStreamForAgent attempt %d: %v", attempt, err)
		}
	}
}

func TestColdStart(t *testing.T) {
	tests := map[string]struct {
		group        string
		defensiveEnv string
		timeout      time.Duration
		run          func(t *testing.T, ctx context.Context, nc *nats.Conn, broker *ipc.NATSBroker, group, agentID string)
	}{
		"fast start pre-creates consumer for first fetch": {
			group:        "cold-start-fast@g.us",
			defensiveEnv: "",
			timeout:      10 * time.Second,
			run:          runColdStartFastStartPreCreated,
		},
		"legacy defensive stream creates on send": {
			group:        "cold-start-legacy@g.us",
			defensiveEnv: "1",
			timeout:      5 * time.Second,
			run:          runColdStartLegacyDefensive,
		},
		"agent fetch exhaustion without pre-create": {
			group:        "cold-start-exhaust@g.us",
			defensiveEnv: "",
			timeout:      5 * time.Second,
			run:          runColdStartFetchExhaustion,
		},
		"ensure stream idempotent across retries": {
			group:        "cold-start-idempotent@g.us",
			defensiveEnv: "",
			timeout:      5 * time.Second,
			run:          runColdStartEnsureStreamIdempotent,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			nc, cleanup := startColdStartNATS(t)
			defer cleanup()

			broker, err := ipc.NewNATSBroker(nc, slog.Default())
			if err != nil {
				t.Fatalf("NewNATSBroker: %v", err)
			}
			defer func() { _ = broker.Close() }()

			t.Setenv("KRACLAW_AGENT_DEFENSIVE_STREAM", tt.defensiveEnv)

			ctx, cancel := context.WithTimeout(context.Background(), tt.timeout)
			defer cancel()

			tt.run(t, ctx, nc, broker, tt.group, "main")
		})
	}
}
