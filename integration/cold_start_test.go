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

// TestColdStart_FastStartEnabled_PreCreatesConsumer exercises the
// server-side EnsureStreamForAgent → agent-side fetch contract end-to-end
// over a real NATS broker. With the consumer pre-created on the broker,
// the agent's IPCClient.ReadInput must succeed on the first fetch attempt
// without hitting the bounded-retry path.
func TestColdStart_FastStartEnabled_PreCreatesConsumer(t *testing.T) {
	nc, cleanup := startColdStartNATS(t)
	defer cleanup()

	broker, err := ipc.NewNATSBroker(nc, slog.Default())
	if err != nil {
		t.Fatalf("NewNATSBroker: %v", err)
	}
	defer func() { _ = broker.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	const group = "cold-start-fast@g.us"
	const agentID = "main"

	// Pre-create stream + consumer (the orchestrator-side step).
	if err := broker.EnsureStreamForAgent(ctx, group, agentID); err != nil {
		t.Fatalf("EnsureStreamForAgent: %v", err)
	}

	// Agent attaches using the pre-created consumer; this must NOT trigger a
	// stream/consumer create round-trip (KRACLAW_AGENT_DEFENSIVE_STREAM unset).
	if err := os.Unsetenv("KRACLAW_AGENT_DEFENSIVE_STREAM"); err != nil {
		t.Fatalf("unset env: %v", err)
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

	// Verify the broker can publish input and the agent receives it.
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

// TestColdStart_FastStartDisabled_RestoresLegacyBehaviour proves the
// rollback path: with KRACLAW_AGENT_DEFENSIVE_STREAM=1 the agent
// re-enables its self-create defensive ensureStream call and can attach
// without a prior broker-side EnsureStreamForAgent. Mirrors the helm
// legacy path emitted when sandbox.fastStart.enabled=false.
func TestColdStart_FastStartDisabled_RestoresLegacyBehaviour(t *testing.T) {
	nc, cleanup := startColdStartNATS(t)
	defer cleanup()

	t.Setenv("KRACLAW_AGENT_DEFENSIVE_STREAM", "1")

	const group = "cold-start-legacy@g.us"
	const agentID = "main"

	client, err := agent.NewIPCClient(nc, group, agentID, slog.Default())
	if err != nil {
		t.Fatalf("NewIPCClient: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Sanity: no server-side EnsureStreamForAgent was called. SendOutput must
	// succeed because the gated ensureStream path runs and creates the stream.
	if err := client.SendOutput(ctx, &agent.OutboundMessage{Type: "test"}); err != nil {
		t.Fatalf("SendOutput err = %v, want nil (defensive path should create stream)", err)
	}

	// Verify the stream now exists on the broker.
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

// TestColdStart_AgentFetchExhaustion_NoServerPreCreate verifies that when
// the orchestrator's EnsureStreamForAgent is *not* called and the legacy
// defensive env var is also unset, the agent's bounded fetch correctly
// surfaces the terminal "fetch input consumer ... after retries" error
// rather than silently creating a consumer.
func TestColdStart_AgentFetchExhaustion_NoServerPreCreate(t *testing.T) {
	nc, cleanup := startColdStartNATS(t)
	defer cleanup()

	t.Setenv("KRACLAW_AGENT_DEFENSIVE_STREAM", "")

	const group = "cold-start-exhaust@g.us"
	const agentID = "main"

	client, err := agent.NewIPCClient(nc, group, agentID, slog.Default())
	if err != nil {
		t.Fatalf("NewIPCClient: %v", err)
	}

	// consumerFetchBackoff is unexported and unreachable from this external
	// integration_test package, so the retry loop uses its default backoff.
	// That is fine: the default 100ms × 5 doublings is bounded ≤ 3.1s, well
	// within the 5s context below, so the fetch still exhausts and returns.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, _, err = client.ReadInput(ctx)
	if err == nil {
		t.Fatal("ReadInput err = nil, want non-nil (no pre-create, no defensive create)")
	}
	if !strings.Contains(err.Error(), "fetch input consumer") {
		t.Errorf("err = %v, want substring \"fetch input consumer\"", err)
	}
}

// TestColdStart_EnsureStreamIdempotent_ProductionContract proves that
// repeated EnsureStreamForAgent calls (e.g. across retries or restarts)
// do not error or double-create resources. The orchestrator's bounded
// retry helper relies on this idempotence.
func TestColdStart_EnsureStreamIdempotent_ProductionContract(t *testing.T) {
	nc, cleanup := startColdStartNATS(t)
	defer cleanup()

	broker, err := ipc.NewNATSBroker(nc, slog.Default())
	if err != nil {
		t.Fatalf("NewNATSBroker: %v", err)
	}
	defer func() { _ = broker.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	const group = "cold-start-idempotent@g.us"
	const agentID = "main"

	for attempt := 1; attempt <= 5; attempt++ {
		if err := broker.EnsureStreamForAgent(ctx, group, agentID); err != nil {
			t.Fatalf("EnsureStreamForAgent attempt %d: %v", attempt, err)
		}
	}
}
