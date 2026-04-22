package credproxy

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/johanssonvincent/kraclaw/internal/config"
	"github.com/johanssonvincent/kraclaw/internal/provider"
)

func TestProxyWriteTimeoutIsSet(t *testing.T) {
	p := newTestProxy(t, "https://api.anthropic.com", "sk-test")

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		_ = p.Start(ctx)
	}()
	// Give the server a moment to initialize.
	time.Sleep(50 * time.Millisecond)
	cancel()

	if p.server == nil {
		t.Fatal("server not initialized after Start")
	}
	if p.server.WriteTimeout != 600*time.Second {
		t.Fatalf("expected WriteTimeout 600s, got %v", p.server.WriteTimeout)
	}
}

func TestNew_NoAuth_ReturnsError(t *testing.T) {
	_, err := New(config.ProxyConfig{
		AnthropicUpstreamURL: "https://api.anthropic.com",
	})
	if err == nil {
		t.Fatal("expected error when no Anthropic credentials configured in legacy mode")
	}
	if !strings.Contains(err.Error(), "AnthropicAPIKey") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestNew_InvalidURL(t *testing.T) {
	_, err := New(config.ProxyConfig{
		AnthropicUpstreamURL: "://bad",
		AnthropicAPIKey:      "sk-test",
	})
	if err == nil {
		t.Fatal("expected error for invalid upstream URL")
	}
}

func newTestProxy(t *testing.T, upstream string, apiKey string) *Proxy {
	t.Helper()
	p, err := New(config.ProxyConfig{
		AnthropicUpstreamURL: upstream,
		AnthropicAPIKey:      apiKey,
	})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestHealthz(t *testing.T) {
	p := newTestProxy(t, "https://api.anthropic.com", "sk-test")
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", p.handleHealthz)

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if body := w.Body.String(); body != "ok" {
		t.Fatalf("expected 'ok', got %q", body)
	}
}

func TestReadyz(t *testing.T) {
	p := newTestProxy(t, "https://api.anthropic.com", "sk-test")
	mux := http.NewServeMux()
	mux.HandleFunc("/readyz", p.handleReadyz)

	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if body := w.Body.String(); body != "ok" {
		t.Fatalf("expected 'ok', got %q", body)
	}
}

func TestAPIKeyMode_InjectsKey(t *testing.T) {
	var receivedHeaders http.Header
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedHeaders = r.Header
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()

	p := newTestProxy(t, upstream.URL, "sk-real-key")
	rp := p.newReverseProxy()

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{}`))
	req.Header.Set("X-Api-Key", "placeholder")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	rp.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if got := receivedHeaders.Get("X-Api-Key"); got != "sk-real-key" {
		t.Fatalf("expected real API key, got %q", got)
	}
}


func TestMetricsMiddleware_RecordsStatus(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	p := newTestProxy(t, upstream.URL, "sk-test")
	handler := p.metricsMiddleware(p.newReverseProxy())

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

func TestUpstreamError_Returns502(t *testing.T) {
	// Point to a non-existent upstream
	p := newTestProxy(t, "http://127.0.0.1:1", "sk-test")
	rp := p.newReverseProxy()

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	w := httptest.NewRecorder()
	rp.ServeHTTP(w, req)

	if w.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d", w.Code)
	}
	body, _ := io.ReadAll(w.Body)
	if string(body) != "Bad Gateway" {
		t.Fatalf("expected 'Bad Gateway', got %q", string(body))
	}
}

func TestHopByHopHeaders_Stripped(t *testing.T) {
	var receivedHeaders http.Header
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedHeaders = r.Header
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	p := newTestProxy(t, upstream.URL, "sk-test")
	rp := p.newReverseProxy()

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{}`))
	req.Header.Set("Connection", "keep-alive")
	req.Header.Set("Keep-Alive", "timeout=5")
	w := httptest.NewRecorder()
	rp.ServeHTTP(w, req)

	if got := receivedHeaders.Get("Keep-Alive"); got != "" {
		t.Fatalf("expected Keep-Alive stripped, got %q", got)
	}
}

func TestAuthMode(t *testing.T) {
	p := newTestProxy(t, "https://api.anthropic.com", "sk-test")
	if got := p.authMode(); got != "api-key" {
		t.Fatalf("expected %q, got %q", "api-key", got)
	}
}

func TestSSEStreaming_FlushesImmediately(t *testing.T) {
	var mu sync.Mutex
	var receivedEvents []string

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("expected flusher")
		}
		for i := 0; i < 3; i++ {
			_, _ = fmt.Fprintf(w, "event: message\ndata: {\"index\":%d}\n\n", i)
			flusher.Flush()
			time.Sleep(10 * time.Millisecond)
		}
	}))
	defer upstream.Close()

	p := newTestProxy(t, upstream.URL, "sk-test")
	handler := p.metricsMiddleware(p.newReverseProxy())

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")

	// Use a pipe to read the response as it streams.
	pr, pw := io.Pipe()
	w := &streamRecorder{
		header:     http.Header{},
		body:       pw,
		statusCode: 0,
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		handler.ServeHTTP(w, req)
		_ = pw.Close()
	}()

	buf := make([]byte, 4096)
	for {
		n, err := pr.Read(buf)
		if n > 0 {
			mu.Lock()
			receivedEvents = append(receivedEvents, string(buf[:n]))
			mu.Unlock()
		}
		if err != nil {
			break
		}
	}
	<-done

	mu.Lock()
	defer mu.Unlock()
	if len(receivedEvents) == 0 {
		t.Fatal("expected streamed events, got none")
	}
	// Verify all events arrived
	combined := strings.Join(receivedEvents, "")
	for i := 0; i < 3; i++ {
		expected := fmt.Sprintf("\"index\":%d", i)
		if !strings.Contains(combined, expected) {
			t.Fatalf("missing event %d in response", i)
		}
	}
}

func TestContextCanceled_NoErrorResponse(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Simulate slow upstream that outlasts the client context.
		select {
		case <-r.Context().Done():
		case <-time.After(500 * time.Millisecond):
		}
	}))
	defer upstream.Close()

	p := newTestProxy(t, upstream.URL, "sk-test")
	rp := p.newReverseProxy()

	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{}`)).WithContext(ctx)
	w := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		defer close(done)
		rp.ServeHTTP(w, req)
	}()

	// Cancel client context after a short delay.
	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done

	// context.Canceled should NOT produce a 502 Bad Gateway.
	if w.Code == http.StatusBadGateway {
		t.Fatal("context.Canceled should not return 502")
	}
}

func TestResponseWriter_Unwrap(t *testing.T) {
	inner := httptest.NewRecorder()
	rw := &responseWriter{ResponseWriter: inner, statusCode: http.StatusOK}
	if got := rw.Unwrap(); got != inner {
		t.Fatal("Unwrap should return the inner ResponseWriter")
	}
}

// streamRecorder implements http.ResponseWriter for streaming tests.
type streamRecorder struct {
	header     http.Header
	body       io.Writer
	statusCode int
}

func (sr *streamRecorder) Header() http.Header         { return sr.header }
func (sr *streamRecorder) WriteHeader(code int)        { sr.statusCode = code }
func (sr *streamRecorder) Write(b []byte) (int, error) { return sr.body.Write(b) }
func (sr *streamRecorder) Flush() {
	// no-op for test; data is written directly to pipe
}

// staticCredentialResolver returns a fixed credential for testing.
type staticCredentialResolver struct {
	cred *resolvedCredential
	err  error
}

func (r *staticCredentialResolver) Resolve(_ context.Context, _ string, _ string) (*resolvedCredential, error) {
	return r.cred, r.err
}

func TestProxy_InjectsOpenAIBearerToken(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if auth != "Bearer sk-openai-test" {
			t.Errorf("expected Bearer sk-openai-test, got %q", auth)
		}
		if r.Header.Get("X-Api-Key") != "" {
			t.Error("X-Api-Key should not be set for OpenAI requests")
		}
		if r.Header.Get("X-Kraclaw-Group") != "" {
			t.Error("X-Kraclaw-Group should be stripped")
		}
		if r.Header.Get("X-Kraclaw-Provider") != "" {
			t.Error("X-Kraclaw-Provider should be stripped")
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	resolver := &staticCredentialResolver{
		cred: &resolvedCredential{
			Provider:    "openai",
			APIKey:      "sk-openai-test",
			UpstreamURL: upstream.URL,
		},
	}

	proxy, err := NewMultiProviderProxy(config.ProxyConfig{
		Addr:              ":0",
		OpenAIUpstreamURL: upstream.URL,
	}, resolver)
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	req.Header.Set("X-Kraclaw-Group", "discord:123")
	w := httptest.NewRecorder()
	proxy.handler().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

func TestProxy_InjectsAnthropicKeyViaResolver(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		apiKey := r.Header.Get("X-Api-Key")
		if apiKey != "sk-ant-test" {
			t.Errorf("expected X-Api-Key sk-ant-test, got %q", apiKey)
		}
		if r.Header.Get("X-Kraclaw-Group") != "" {
			t.Error("X-Kraclaw-Group should be stripped")
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	resolver := &staticCredentialResolver{
		cred: &resolvedCredential{
			Provider:    "anthropic",
			APIKey:      "sk-ant-test",
			UpstreamURL: upstream.URL,
		},
	}

	proxy, err := NewMultiProviderProxy(config.ProxyConfig{
		Addr:                 ":0",
		AnthropicUpstreamURL: upstream.URL,
	}, resolver)
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("POST", "/v1/messages", nil)
	req.Header.Set("X-Kraclaw-Group", "telegram:456")
	w := httptest.NewRecorder()
	proxy.handler().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}


func TestProxy_NoGroupHeader_FallsBackToLegacy(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		apiKey := r.Header.Get("X-Api-Key")
		if apiKey != "sk-legacy-key" {
			t.Errorf("expected legacy API key, got %q", apiKey)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	resolver := &staticCredentialResolver{
		cred: &resolvedCredential{
			Provider:    "openai",
			APIKey:      "sk-should-not-be-used",
			UpstreamURL: "http://should-not-be-used",
		},
	}

	proxy, err := NewMultiProviderProxy(config.ProxyConfig{
		Addr:                 ":0",
		AnthropicUpstreamURL: upstream.URL,
		AnthropicAPIKey:      "sk-legacy-key",
	}, resolver)
	if err != nil {
		t.Fatal(err)
	}

	// No X-Kraclaw-Group header -- should use legacy Anthropic path.
	req := httptest.NewRequest("POST", "/v1/messages", nil)
	w := httptest.NewRecorder()
	proxy.handler().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

func TestProxy_ResolverError_ClearsHeaders(t *testing.T) {
	resolver := &staticCredentialResolver{
		err: fmt.Errorf("database connection failed"),
	}

	proxy, err := NewMultiProviderProxy(config.ProxyConfig{
		Addr:                 ":0",
		AnthropicUpstreamURL: "https://api.anthropic.com",
	}, resolver)
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("POST", "/v1/messages", nil)
	req.Header.Set("X-Kraclaw-Group", "discord:123")
	req.Header.Set("X-Api-Key", "should-be-cleared")
	req.Header.Set("Authorization", "Bearer should-be-cleared")
	w := httptest.NewRecorder()
	proxy.handler().ServeHTTP(w, req)

	// The credential middleware returns 502 before the request reaches the Director.
	if w.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d", w.Code)
	}
}

func TestProxy_ResolverError_Returns502WithMessage(t *testing.T) {
	resolver := &staticCredentialResolver{
		err: fmt.Errorf("database connection failed"),
	}

	proxy, err := NewMultiProviderProxy(config.ProxyConfig{
		Addr: ":0",
	}, resolver)
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("POST", "/v1/messages", nil)
	req.Header.Set("X-Kraclaw-Group", "discord:123")
	w := httptest.NewRecorder()
	proxy.handler().ServeHTTP(w, req)

	if w.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "credential resolution failed") {
		t.Fatalf("expected error message in body, got %q", body)
	}
}

func TestNewMultiProviderProxy_DefaultUpstream(t *testing.T) {
	resolver := &staticCredentialResolver{}
	proxy, err := NewMultiProviderProxy(config.ProxyConfig{Addr: ":0"}, resolver)
	if err != nil {
		t.Fatal(err)
	}
	if proxy.upstream.Host != "api.anthropic.com" {
		t.Fatalf("expected default upstream host api.anthropic.com, got %q", proxy.upstream.Host)
	}
	if proxy.resolver == nil {
		t.Fatal("expected resolver to be set")
	}
}

func TestDefaultCredentialResolver_PlatformFallbackAnthropic(t *testing.T) {
	r := NewDefaultResolver(nil, config.ProxyConfig{
		AnthropicAPIKey:      "sk-platform",
		AnthropicUpstreamURL: "https://api.anthropic.com",
	})

	cred, err := r.Resolve(context.Background(), "", "")
	if err != nil {
		t.Fatal(err)
	}
	if cred.Provider != "anthropic" {
		t.Fatalf("expected anthropic, got %q", cred.Provider)
	}
	if cred.APIKey != "sk-platform" {
		t.Fatalf("expected sk-platform, got %q", cred.APIKey)
	}
}

func TestDefaultCredentialResolver_PlatformFallbackOpenAI(t *testing.T) {
	r := NewDefaultResolver(nil, config.ProxyConfig{
		OpenAIAPIKey:      "sk-openai-platform",
		OpenAIUpstreamURL: "https://api.openai.com",
	})

	cred, err := r.Resolve(context.Background(), "", "")
	if err != nil {
		t.Fatal(err)
	}
	if cred.Provider != "openai" {
		t.Fatalf("expected openai, got %q", cred.Provider)
	}
	if cred.APIKey != "sk-openai-platform" {
		t.Fatalf("expected sk-openai-platform, got %q", cred.APIKey)
	}
}

func TestDefaultCredentialResolver_NoCredentials_ReturnsError(t *testing.T) {
	r := NewDefaultResolver(nil, config.ProxyConfig{})

	_, err := r.Resolve(context.Background(), "discord:123", "")
	if err == nil {
		t.Fatal("expected error when no credentials configured")
	}
}

func TestDefaultCredentialResolver_PerGroupOpenAI(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	enc := newTestEncryptor(t)
	credStore, err := NewCredentialStore(db, enc)
	if err != nil {
		t.Fatal(err)
	}

	encKey, _ := enc.Encrypt("sk-group-openai-key")
	rows := sqlmock.NewRows([]string{"provider", "auth_mode", "api_key_encrypted", "oauth_access_token_encrypted", "oauth_refresh_token_encrypted", "oauth_id_token_encrypted", "oauth_account_id", "oauth_expires_at", "oauth_is_fedramp"}).
		AddRow("openai", string(AuthModeAPIKey), encKey, nil, nil, nil, nil, nil, false)
	mock.ExpectQuery("SELECT").WithArgs("discord:123").WillReturnRows(rows)

	r := NewDefaultResolver(credStore, config.ProxyConfig{
		AnthropicUpstreamURL: "https://api.anthropic.com",
		OpenAIUpstreamURL:    "https://api.openai.com",
		AnthropicAPIKey:      "sk-platform-anthropic",
	})

	cred, err := r.Resolve(context.Background(), "discord:123", "")
	if err != nil {
		t.Fatal(err)
	}
	if cred.Provider != "openai" {
		t.Fatalf("expected openai provider, got %q", cred.Provider)
	}
	if cred.APIKey != "sk-group-openai-key" {
		t.Fatalf("expected group API key, got %q", cred.APIKey)
	}
	if cred.UpstreamURL != "https://api.openai.com" {
		t.Fatalf("expected OpenAI upstream, got %q", cred.UpstreamURL)
	}
}

func TestDefaultCredentialResolver_PerGroupNotFound_FallsThroughToPlatform(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	enc := newTestEncryptor(t)
	credStore, err := NewCredentialStore(db, enc)
	if err != nil {
		t.Fatal(err)
	}

	mock.ExpectQuery("SELECT").WithArgs("discord:456").WillReturnError(sql.ErrNoRows)

	r := NewDefaultResolver(credStore, config.ProxyConfig{
		AnthropicAPIKey:      "sk-platform-anthropic",
		AnthropicUpstreamURL: "https://api.anthropic.com",
	})

	cred, err := r.Resolve(context.Background(), "discord:456", "")
	if err != nil {
		t.Fatal(err)
	}
	if cred.Provider != "anthropic" {
		t.Fatalf("expected anthropic fallback, got %q", cred.Provider)
	}
	if cred.APIKey != "sk-platform-anthropic" {
		t.Fatalf("expected platform API key, got %q", cred.APIKey)
	}
}

func TestDefaultCredentialResolver_PerGroupStoreError_PropagatesError(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	enc := newTestEncryptor(t)
	credStore, err := NewCredentialStore(db, enc)
	if err != nil {
		t.Fatal(err)
	}

	mock.ExpectQuery("SELECT").WithArgs("discord:789").WillReturnError(fmt.Errorf("connection refused"))

	r := NewDefaultResolver(credStore, config.ProxyConfig{
		AnthropicAPIKey: "sk-platform",
	})

	_, err = r.Resolve(context.Background(), "discord:789", "")
	if err == nil {
		t.Fatal("expected error from store failure")
	}
	if !strings.Contains(err.Error(), "connection refused") {
		t.Fatalf("expected wrapped error, got: %v", err)
	}
}

func TestDefaultResolver_PlatformFallback_RespectsRequestedProvider(t *testing.T) {
	cfg := config.ProxyConfig{
		AnthropicAPIKey:      "sk-anthropic",
		AnthropicUpstreamURL: "https://api.anthropic.com",
		OpenAIAPIKey:         "sk-openai",
		OpenAIUpstreamURL:    "https://api.openai.com",
	}
	resolver := NewDefaultResolver(nil, cfg)

	tests := []struct {
		name              string
		requestedProvider string
		wantProvider      string
		wantAPIKey        string
		wantUpstream      string
	}{
		{
			name:              "explicit openai request gets openai creds",
			requestedProvider: provider.ProviderOpenAI,
			wantProvider:      provider.ProviderOpenAI,
			wantAPIKey:        "sk-openai",
			wantUpstream:      "https://api.openai.com",
		},
		{
			name:              "explicit anthropic request gets anthropic creds",
			requestedProvider: provider.ProviderAnthropic,
			wantProvider:      provider.ProviderAnthropic,
			wantAPIKey:        "sk-anthropic",
			wantUpstream:      "https://api.anthropic.com",
		},
		{
			name:              "empty provider defaults to anthropic",
			requestedProvider: "",
			wantProvider:      provider.ProviderAnthropic,
			wantAPIKey:        "sk-anthropic",
			wantUpstream:      "https://api.anthropic.com",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cred, err := resolver.Resolve(context.Background(), "group-1", tt.requestedProvider)
			if err != nil {
				t.Fatalf("Resolve() error = %v", err)
			}
			if cred.Provider != tt.wantProvider {
				t.Errorf("Provider = %q, want %q", cred.Provider, tt.wantProvider)
			}
			if cred.APIKey != tt.wantAPIKey {
				t.Errorf("APIKey = %q, want %q", cred.APIKey, tt.wantAPIKey)
			}
			if cred.UpstreamURL != tt.wantUpstream {
				t.Errorf("UpstreamURL = %q, want %q", cred.UpstreamURL, tt.wantUpstream)
			}
		})
	}
}

func TestDefaultCredentialResolver_PerGroupProviderMismatch_FallsThroughToPlatform(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	enc := newTestEncryptor(t)
	credStore, err := NewCredentialStore(db, enc)
	if err != nil {
		t.Fatal(err)
	}

	// Group has Anthropic credentials stored.
	encKey, _ := enc.Encrypt("sk-group-anthropic")
	rows := sqlmock.NewRows([]string{"provider", "auth_mode", "api_key_encrypted", "oauth_access_token_encrypted", "oauth_refresh_token_encrypted", "oauth_id_token_encrypted", "oauth_account_id", "oauth_expires_at", "oauth_is_fedramp"}).
		AddRow("anthropic", string(AuthModeAPIKey), encKey, nil, nil, nil, nil, nil, false)
	mock.ExpectQuery("SELECT").WithArgs("discord:mismatch").WillReturnRows(rows)

	r := NewDefaultResolver(credStore, config.ProxyConfig{
		AnthropicUpstreamURL: "https://api.anthropic.com",
		OpenAIUpstreamURL:    "https://api.openai.com",
		OpenAIAPIKey:         "sk-openai-platform",
	})

	// Agent requests OpenAI, but group has Anthropic creds — should fall through to platform OpenAI.
	cred, err := r.Resolve(context.Background(), "discord:mismatch", provider.ProviderOpenAI)
	if err != nil {
		t.Fatal(err)
	}
	if cred.Provider != provider.ProviderOpenAI {
		t.Fatalf("expected openai provider (platform fallback), got %q", cred.Provider)
	}
	if cred.APIKey != "sk-openai-platform" {
		t.Fatalf("expected platform OpenAI key, got %q", cred.APIKey)
	}
}

func TestProxy_MultiProvider_RejectsUnknownUpstreamHost(t *testing.T) {
	resolver := &staticCredentialResolver{
		cred: &resolvedCredential{
			Provider:    "anthropic",
			APIKey:      "sk-test",
			UpstreamURL: "https://evil.example.com",
		},
	}

	proxy, err := NewMultiProviderProxy(config.ProxyConfig{
		Addr:                 ":0",
		AnthropicUpstreamURL: "https://api.anthropic.com",
		OpenAIUpstreamURL:    "https://api.openai.com",
	}, resolver)
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("POST", "/v1/messages", nil)
	req.Header.Set("X-Kraclaw-Group", "discord:123")
	w := httptest.NewRecorder()
	proxy.handler().ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for unknown upstream host, got %d", w.Code)
	}
}

func TestDefaultResolver_RequestedProviderNotConfigured_ReturnsError(t *testing.T) {
	tests := []struct {
		name              string
		cfg               config.ProxyConfig
		requestedProvider string
	}{
		{
			name: "request openai but only anthropic configured",
			cfg: config.ProxyConfig{
				AnthropicAPIKey:      "sk-anthropic",
				AnthropicUpstreamURL: "https://api.anthropic.com",
			},
			requestedProvider: provider.ProviderOpenAI,
		},
		{
			name: "request anthropic but only openai configured",
			cfg: config.ProxyConfig{
				OpenAIAPIKey:      "sk-openai",
				OpenAIUpstreamURL: "https://api.openai.com",
			},
			requestedProvider: provider.ProviderAnthropic,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resolver := NewDefaultResolver(nil, tt.cfg)
			_, err := resolver.Resolve(context.Background(), "group-1", tt.requestedProvider)
			if err == nil {
				t.Fatalf("expected error when requesting %q with no matching credentials configured", tt.requestedProvider)
			}
		})
	}
}

func TestDefaultResolver_ChatGPTAuthModeNotSupported(t *testing.T) {
	t.Parallel()

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	enc := newTestEncryptor(t)
	store, err := NewCredentialStore(db, enc)
	if err != nil {
		t.Fatalf("store: %v", err)
	}

	accessEnc, _ := enc.Encrypt("access")
	refreshEnc, _ := enc.Encrypt("refresh")
	expiresAt := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	rows := sqlmock.NewRows([]string{
		"provider", "auth_mode", "api_key_encrypted",
		"oauth_access_token_encrypted", "oauth_refresh_token_encrypted",
		"oauth_id_token_encrypted", "oauth_account_id",
		"oauth_expires_at", "oauth_is_fedramp",
	}).AddRow("openai", string(AuthModeChatGPT), nil, accessEnc, refreshEnc, nil, "acct", expiresAt, false)
	mock.ExpectQuery("SELECT").WithArgs("discord:chatgpt").WillReturnRows(rows)

	resolver := NewDefaultResolver(store, config.ProxyConfig{
		OpenAIAPIKey:      "platform-openai-key",
		OpenAIUpstreamURL: "https://api.openai.com",
	})

	rc, err := resolver.Resolve(context.Background(), "discord:chatgpt", provider.ProviderOpenAI)
	if err == nil {
		t.Errorf("Resolve(chatgpt cred) err = nil, want error; got %+v", rc)
	}
	if rc != nil {
		t.Errorf("Resolve(chatgpt cred) rc = %+v, want nil", rc)
	}
	if err != nil && !strings.Contains(err.Error(), "chatgpt auth mode") {
		t.Errorf("Resolve(chatgpt cred) err = %v, want error mentioning chatgpt auth mode", err)
	}
}
