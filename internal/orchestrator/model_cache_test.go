package orchestrator

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestModelCache_CacheHitSkipsHTTP(t *testing.T) {
	var reqCount atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqCount.Add(1)
		_ = json.NewEncoder(w).Encode(modelsResponse{Data: []modelInfo{
			{ID: "claude-3-5-sonnet-20241022"},
		}})
	}))
	defer srv.Close()

	cache := newModelCache(srv.URL, "2023-06-01", time.Hour, false, slog.Default())
	ctx := context.Background()

	// First call — should hit HTTP.
	models, stale, err := cache.List(ctx)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if stale {
		t.Error("List() stale = true, want false")
	}
	if len(models) != 1 || models[0].ID != "claude-3-5-sonnet-20241022" {
		t.Errorf("List() models = %v, want [{ID: claude-3-5-sonnet-20241022}]", models)
	}

	// Second call — should use cache, no HTTP.
	models2, stale2, err2 := cache.List(ctx)
	if err2 != nil {
		t.Fatalf("List() second call error = %v", err2)
	}
	if stale2 {
		t.Error("List() second call stale = true, want false")
	}
	if len(models2) != 1 {
		t.Errorf("List() second call len = %d, want 1", len(models2))
	}

	if got := reqCount.Load(); got != 1 {
		t.Errorf("HTTP requests = %d, want 1 (cache hit should skip HTTP)", got)
	}
}

func TestModelCache_RefreshAfterTTL(t *testing.T) {
	var reqCount atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqCount.Add(1)
		_ = json.NewEncoder(w).Encode(modelsResponse{Data: []modelInfo{
			{ID: "claude-3-5-sonnet-20241022"},
		}})
	}))
	defer srv.Close()

	cache := newModelCache(srv.URL, "2023-06-01", 1*time.Millisecond, false, slog.Default())
	ctx := context.Background()

	// Populate cache.
	if _, _, err := cache.List(ctx); err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if got := reqCount.Load(); got != 1 {
		t.Fatalf("HTTP requests after first call = %d, want 1", got)
	}

	// Expire TTL.
	time.Sleep(2 * time.Millisecond)

	// Should trigger refresh.
	if _, _, err := cache.List(ctx); err != nil {
		t.Fatalf("List() after TTL error = %v", err)
	}
	if got := reqCount.Load(); got != 2 {
		t.Errorf("HTTP requests after TTL expiry = %d, want 2", got)
	}
}

func TestModelCache_ErrorRetainsStaleData(t *testing.T) {
	var mu sync.Mutex
	returnError := false

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		shouldError := returnError
		mu.Unlock()
		if shouldError {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(w).Encode(modelsResponse{Data: []modelInfo{
			{ID: "claude-3-5-sonnet-20241022"},
		}})
	}))
	defer srv.Close()

	cache := newModelCache(srv.URL, "2023-06-01", 1*time.Millisecond, false, slog.Default())
	ctx := context.Background()

	// Populate cache.
	models, _, err := cache.List(ctx)
	if err != nil {
		t.Fatalf("List() initial error = %v", err)
	}
	if len(models) != 1 {
		t.Fatalf("List() initial len = %d, want 1", len(models))
	}

	// Swap handler to 500.
	mu.Lock()
	returnError = true
	mu.Unlock()
	time.Sleep(2 * time.Millisecond)

	// Should return stale data with error.
	models, stale, err := cache.List(ctx)
	if err == nil {
		t.Error("List() after error expected err != nil")
	}
	if !stale {
		t.Error("List() after error stale = false, want true")
	}
	if len(models) != 1 {
		t.Errorf("List() after error len = %d, want 1 (stale data retained)", len(models))
	}
}

func TestModelCache_SingleflightDedup(t *testing.T) {
	// The model cache uses mutex with double-check locking: the first goroutine
	// to acquire the lock fetches from HTTP and populates the cache. Subsequent
	// goroutines see the freshly populated cache (within TTL) and skip the HTTP
	// call. This effectively deduplicates concurrent refresh requests.
	var reqCount atomic.Int32
	barrier := make(chan struct{})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqCount.Add(1)
		<-barrier // Block until released.
		_ = json.NewEncoder(w).Encode(modelsResponse{Data: []modelInfo{
			{ID: "claude-3-5-sonnet-20241022"},
		}})
	}))
	defer srv.Close()

	// Use a long TTL so the double-check in refresh() catches concurrent callers.
	cache := newModelCache(srv.URL, "2023-06-01", time.Hour, false, slog.Default())
	ctx := context.Background()

	const goroutines = 5
	var ready sync.WaitGroup
	ready.Add(goroutines)
	var done sync.WaitGroup
	done.Add(goroutines)

	results := make([][]modelInfo, goroutines)
	for i := 0; i < goroutines; i++ {
		go func(idx int) {
			defer done.Done()
			ready.Done()
			ready.Wait() // All goroutines start together.
			models, _, _ := cache.List(ctx)
			results[idx] = models
		}(i)
	}

	// Wait for all goroutines to be ready, then give them a moment to enter List().
	ready.Wait()
	time.Sleep(5 * time.Millisecond)

	// Release the HTTP handler.
	close(barrier)
	done.Wait()

	if got := reqCount.Load(); got != 1 {
		t.Errorf("HTTP requests = %d, want 1 (double-check locking should coalesce concurrent refreshes)", got)
	}

	// All goroutines should have received the same data.
	for i, models := range results {
		if len(models) != 1 {
			t.Errorf("goroutine %d: len(models) = %d, want 1", i, len(models))
		}
	}
}

func TestModelCache_EmptyResponseError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(modelsResponse{Data: []modelInfo{}})
	}))
	defer srv.Close()

	cache := newModelCache(srv.URL, "2023-06-01", time.Hour, false, slog.Default())
	ctx := context.Background()

	_, _, err := cache.List(ctx)
	if err == nil {
		t.Error("List() expected error for empty response")
	}
	if err != nil && !contains(err.Error(), "models response empty") {
		t.Errorf("List() error = %q, want containing %q", err.Error(), "models response empty")
	}
}

func TestModelCache_OAuthExchangeFallbackOnUnauthorized(t *testing.T) {
	var modelsCalls atomic.Int32
	var exchangeCalls atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			modelsCalls.Add(1)
			if r.Header.Get("X-Api-Key") != "sk-ant-from-exchange" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_ = json.NewEncoder(w).Encode(modelsResponse{Data: []modelInfo{{ID: "claude-3-5-sonnet-20241022"}}})
		case "/api/oauth/claude_cli/create_api_key":
			exchangeCalls.Add(1)
			if got := r.Header.Get("Authorization"); got != "Bearer placeholder" {
				t.Fatalf("exchange Authorization = %q, want Bearer placeholder", got)
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"api_key":"sk-ant-from-exchange"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	cache := newModelCache(srv.URL, "2023-06-01", time.Hour, false, slog.Default())

	models, stale, err := cache.List(context.Background())
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if stale {
		t.Fatal("List() stale = true, want false")
	}
	if len(models) != 1 || models[0].ID != "claude-3-5-sonnet-20241022" {
		t.Fatalf("List() models = %v, want exchanged models", models)
	}
	if got := exchangeCalls.Load(); got != 1 {
		t.Fatalf("exchange calls = %d, want 1", got)
	}
	if got := modelsCalls.Load(); got != 2 {
		t.Fatalf("models calls = %d, want 2 (unauthorized + retry)", got)
	}
}

func TestModelCache_OAuthModeUsesHardcodedModels(t *testing.T) {
	var reqCount atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqCount.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	cache := newModelCache(srv.URL, "2023-06-01", time.Hour, true, slog.Default())

	models, stale, err := cache.List(context.Background())
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if stale {
		t.Fatal("List() stale = true, want false")
	}
	if len(models) == 0 {
		t.Fatal("List() returned no models")
	}

	if got := reqCount.Load(); got != 0 {
		t.Fatalf("HTTP requests = %d, want 0 in oauth hardcoded mode", got)
	}

	found := false
	for _, m := range models {
		if m.ID == "claude-opus-4-6" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("List() missing expected hardcoded model claude-opus-4-6")
	}
}

func TestExtractAPIKey(t *testing.T) {
	tests := []struct {
		name    string
		payload string
		want    string
		wantErr bool
	}{
		{name: "api_key field", payload: `{"api_key":"sk-ant-1"}`, want: "sk-ant-1"},
		{name: "nested field", payload: `{"data":{"token":"sk-ant-2"}}`, want: "sk-ant-2"},
		{name: "string scan", payload: `{"items":["x","sk-ant-3"]}`, want: "sk-ant-3"},
		{name: "missing", payload: `{"token":"not-an-api-key"}`, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := extractAPIKey([]byte(tt.payload))
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				if !strings.Contains(err.Error(), "did not include API key") {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("extractAPIKey() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("extractAPIKey() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestNormalizeProxyURL(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "port only",
			input: ":3001",
			want:  "http://127.0.0.1:3001",
		},
		{
			name:  "http with host and port",
			input: "http://proxy:3001",
			want:  "http://proxy:3001",
		},
		{
			name:  "http with trailing slash",
			input: "http://proxy:3001/",
			want:  "http://proxy:3001",
		},
		{
			name:  "https full URL",
			input: "https://proxy.example.com",
			want:  "https://proxy.example.com",
		},
		{
			name:  "bare host and port",
			input: "proxy:3001",
			want:  "http://proxy:3001",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := normalizeProxyURL(tt.input)
			if got != tt.want {
				t.Errorf("normalizeProxyURL(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// contains is a helper to check substring presence.
func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchSubstring(s, substr)
}

func searchSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
