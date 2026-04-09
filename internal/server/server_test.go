package server

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	_ "github.com/go-sql-driver/mysql"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
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
	if err != wantErr {
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
