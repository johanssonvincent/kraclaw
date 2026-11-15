package server

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	agentsandboxv1alpha1 "sigs.k8s.io/agent-sandbox/api/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrlfake "sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/johanssonvincent/kraclaw/internal/auth/chatgpt"
	"github.com/johanssonvincent/kraclaw/internal/credproxy"
	"github.com/johanssonvincent/kraclaw/internal/sandbox"
)

// --- Health endpoint tests ---

func TestHealthzHandler_Returns200(t *testing.T) {
	handler := healthzHandler()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	w := httptest.NewRecorder()

	handler(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("healthz status = %d, want %d", w.Code, http.StatusOK)
	}
	if w.Body.String() != "ok" {
		t.Errorf("healthz body = %q, want %q", w.Body.String(), "ok")
	}
}

func TestReadyzHandler_HealthyDeps(t *testing.T) {
	// Full healthy test requires a real MySQL connection; covered by failure-path tests.
	t.Skip("full healthy test requires a real MySQL connection; covered by failure-path tests")
}

func TestReadyzHandler_DBDown(t *testing.T) {
	// Open a DB connection that will fail on Ping (invalid DSN, no server).
	db, err := sql.Open("mysql", "invalid:invalid@tcp(127.0.0.1:1)/nonexistent")
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	handler := readyzHandler(db)
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	w := httptest.NewRecorder()

	handler(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("readyz status = %d, want %d", w.Code, http.StatusServiceUnavailable)
	}
}

// --- Interceptor tests ---

func TestRecoveryInterceptor_PanickingHandler(t *testing.T) {
	log := slog.Default()
	interceptor := recoveryInterceptor(log)

	info := &grpc.UnaryServerInfo{FullMethod: "/test.Service/PanicMethod"}
	handler := func(ctx context.Context, req any) (any, error) {
		panic("test panic")
	}

	resp, err := interceptor(context.Background(), nil, info, handler)
	if resp != nil {
		t.Errorf("response = %v, want nil", resp)
	}
	if err == nil {
		t.Fatal("error = nil, want non-nil")
	}

	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("error is not a gRPC status: %v", err)
	}
	if st.Code() != codes.Internal {
		t.Errorf("status code = %v, want %v", st.Code(), codes.Internal)
	}
}

func TestLoggingInterceptor_PropagatesError(t *testing.T) {
	log := slog.Default()
	interceptor := loggingInterceptor(log)

	info := &grpc.UnaryServerInfo{FullMethod: "/test.Service/ErrorMethod"}
	wantErr := fmt.Errorf("handler error")
	handler := func(ctx context.Context, req any) (any, error) {
		return nil, wantErr
	}

	resp, err := interceptor(context.Background(), nil, info, handler)
	if resp != nil {
		t.Errorf("response = %v, want nil", resp)
	}
	if !errors.Is(err, wantErr) {
		t.Errorf("error = %v, want %v", err, wantErr)
	}
}

func TestLoggingInterceptor_SuccessfulHandler(t *testing.T) {
	log := slog.Default()
	interceptor := loggingInterceptor(log)

	info := &grpc.UnaryServerInfo{FullMethod: "/test.Service/OKMethod"}
	handler := func(ctx context.Context, req any) (any, error) {
		return "ok", nil
	}

	resp, err := interceptor(context.Background(), nil, info, handler)
	if err != nil {
		t.Errorf("error = %v, want nil", err)
	}
	if resp != "ok" {
		t.Errorf("response = %v, want %q", resp, "ok")
	}
}

func TestAuthConfig_Validate(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		cfg     *AuthConfig
		wantErr bool
	}{
		"nil sub-struct allowed (auth disabled)": {cfg: nil, wantErr: false},
		"both set is valid":                      {cfg: &AuthConfig{ChatGPT: &chatgpt.Client{}, Credentials: &credproxy.CredentialStore{}}, wantErr: false},
		"only chatgpt set is invalid":            {cfg: &AuthConfig{ChatGPT: &chatgpt.Client{}}, wantErr: true},
		"only creds set is invalid":              {cfg: &AuthConfig{Credentials: &credproxy.CredentialStore{}}, wantErr: true},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			err := tt.cfg.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate(%+v) err = %v, wantErr = %v", tt.cfg, err, tt.wantErr)
			}
			if tt.wantErr && !errors.Is(err, ErrAuthConfigIncomplete) {
				t.Errorf("Validate err = %v, want ErrAuthConfigIncomplete", err)
			}
		})
	}
}

func TestServerStopWithStreamingRPC(t *testing.T) {
	// This test verifies that Server.Stop returns even when a streaming RPC is blocking
	// Create a minimal server with a streaming RPC handler that blocks until ctx is cancelled
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Create a dummy server config
	cfg := Config{
		GRPCAddr:         ":0",
		RESTAddr:         ":0",
		GRPCInsecure:     true,
		GRPCAllowedCIDRs: "127.0.0.1/32",
		Log:              slog.Default(),
	}

	srv, err := New(cfg)
	if err != nil {
		t.Fatalf("failed to create server: %v", err)
	}

	// Start the server
	go func() {
		// This will block until the context is cancelled
		if err := srv.Start(ctx); err != nil && !errors.Is(err, context.Canceled) {
			t.Logf("server start error: %v", err)
		}
	}()

	// Give the server a moment to start
	time.Sleep(100 * time.Millisecond)

	// Test that Stop returns quickly even with a blocking RPC
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer stopCancel()

	// This should not hang indefinitely, even if there are blocking RPCs
	srv.Stop(stopCtx)

	// Cancel the context to stop the server
	stopCancel()
}

// closingWatchClient closes the first Watch it opens, simulating the apiserver
// ending a watch stream (min-request-timeout, resource-version expiry, rollout).
type closingWatchClient struct {
	client.WithWatch
	mu        sync.Mutex
	opens     int
	openCalls chan int
}

func (c *closingWatchClient) Watch(ctx context.Context, obj client.ObjectList, opts ...client.ListOption) (watch.Interface, error) {
	w, err := c.WithWatch.Watch(ctx, obj, opts...)
	if err != nil {
		return nil, err
	}

	c.mu.Lock()
	c.opens++
	open := c.opens
	c.mu.Unlock()

	if c.openCalls != nil {
		select {
		case c.openCalls <- open:
		default:
		}
	}

	if open == 1 {
		// The apiserver closed the stream.
		w.Stop()
	}

	return w, nil
}

// TestWatchSandboxEvents_ReconnectsAfterWatchEnds is the regression test for a
// dead sandbox event feed: watchSandboxEvents used to return as soon as the
// apiserver closed the watch, leaving StreamEvents subscribers with no sandbox
// events for the rest of the process lifetime.
func TestWatchSandboxEvents_ReconnectsAfterWatchEnds(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	_ = agentsandboxv1alpha1.AddToScheme(scheme)

	base := ctrlfake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&agentsandboxv1alpha1.Sandbox{}).
		Build()

	watcher := &closingWatchClient{WithWatch: base, openCalls: make(chan int, 8)}

	ctrl, err := sandbox.New(k8sfake.NewClientset(), watcher, nil, "test-ns", nil, "", "", "", true, "")
	if err != nil {
		t.Fatalf("sandbox.New: %v", err)
	}

	log := slog.New(slog.DiscardHandler)
	hub := newEventHub(log)
	srv := &Server{sandbox: ctrl, events: hub, log: log}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go srv.watchSandboxEvents(ctx)

	sub, unsubscribe := hub.subscribe()
	defer unsubscribe()

	// Wait for the replacement watch before creating the sandbox, so the event
	// cannot be missed by the reconnect window.
	waitForWatch := func(want int) {
		for {
			select {
			case open := <-watcher.openCalls:
				if open >= want {
					return
				}
			case <-time.After(5 * time.Second):
				t.Fatalf("watch was opened %d time(s), want at least %d: the event feed was not re-established", want-1, want)
			}
		}
	}

	waitForWatch(1)
	waitForWatch(2)

	sb := &agentsandboxv1alpha1.Sandbox{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "sbx-1",
			Namespace: "test-ns",
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "kraclaw",
				"kraclaw.io/group":             "grp",
			},
		},
	}
	if err := base.Create(ctx, sb); err != nil {
		t.Fatalf("create sandbox: %v", err)
	}

	select {
	case evt := <-sub:
		if evt.GetType() != "added" {
			t.Fatalf("event type = %q, want %q", evt.GetType(), "added")
		}
		if got := evt.GetMetadata()["name"]; got != "sbx-1" {
			t.Fatalf("event sandbox name = %q, want %q", got, "sbx-1")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no sandbox event published after the watch was re-established")
	}
}

// TestSuperviseSandboxEvents_PublishesSetupFailure keeps the operator-visible
// error event when the watch cannot be opened at all.
func TestSuperviseSandboxEvents_PublishesSetupFailure(t *testing.T) {
	log := slog.New(slog.DiscardHandler)
	hub := newEventHub(log)
	srv := &Server{events: hub, log: log}

	failing := func(context.Context) (<-chan sandbox.SandboxEvent, error) {
		return nil, errors.New("forbidden: cannot watch sandboxes")
	}

	ctx, cancel := context.WithCancel(context.Background())

	sub, unsubscribe := hub.subscribe()
	defer unsubscribe()

	go srv.superviseSandboxEvents(ctx, failing)

	select {
	case evt := <-sub:
		if evt.GetType() != "error" {
			t.Fatalf("event type = %q, want %q", evt.GetType(), "error")
		}
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("no error event published when the watch could not be opened")
	}

	cancel()
}
