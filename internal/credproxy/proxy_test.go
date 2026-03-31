package credproxy

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/johanssonvincent/kraclaw/internal/config"
)

func TestProxyWriteTimeoutIsSet(t *testing.T) {
	p := newTestProxy(t, "https://api.anthropic.com", "sk-test", "")

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
	if !strings.Contains(err.Error(), "AnthropicAPIKey or AnthropicOAuthToken") {
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

func newTestProxy(t *testing.T, upstream string, apiKey, oauthToken string) *Proxy {
	t.Helper()
	p, err := New(config.ProxyConfig{
		AnthropicUpstreamURL: upstream,
		AnthropicAPIKey:      apiKey,
		AnthropicOAuthToken:  oauthToken,
	})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestHealthz(t *testing.T) {
	p := newTestProxy(t, "https://api.anthropic.com", "sk-test", "")
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
	p := newTestProxy(t, "https://api.anthropic.com", "sk-test", "")
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

	p := newTestProxy(t, upstream.URL, "sk-real-key", "")
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

func TestOAuthMode_ReplacesBearer(t *testing.T) {
	var receivedHeaders http.Header
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedHeaders = r.Header
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()

	p := newTestProxy(t, upstream.URL, "", "real-oauth-token")
	rp := p.newReverseProxy()

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer placeholder")
	w := httptest.NewRecorder()
	rp.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if got := receivedHeaders.Get("Authorization"); got != "Bearer real-oauth-token" {
		t.Fatalf("expected real OAuth token, got %q", got)
	}
}

func TestOAuthMode_PlaceholderApiKey_InjectsToken(t *testing.T) {
	var receivedHeaders http.Header
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedHeaders = r.Header
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	p := newTestProxy(t, upstream.URL, "", "real-oauth-token")
	rp := p.newReverseProxy()

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{}`))
	req.Header.Set("X-Api-Key", "placeholder")
	w := httptest.NewRecorder()
	rp.ServeHTTP(w, req)

	if got := receivedHeaders.Get("Authorization"); got != "Bearer real-oauth-token" {
		t.Fatalf("expected OAuth token injected for placeholder key, got %q", got)
	}
	if got := receivedHeaders.Get("X-Api-Key"); got != "" {
		t.Fatalf("expected placeholder X-Api-Key stripped, got %q", got)
	}
}

func TestOAuthMode_RealApiKey_PassesThrough(t *testing.T) {
	var receivedHeaders http.Header
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedHeaders = r.Header
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	p := newTestProxy(t, upstream.URL, "", "real-oauth-token")
	rp := p.newReverseProxy()

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{}`))
	req.Header.Set("X-Api-Key", "sk-ant-real-temp-key-from-exchange")
	w := httptest.NewRecorder()
	rp.ServeHTTP(w, req)

	// Should NOT inject OAuth token when a real API key is present.
	if got := receivedHeaders.Get("Authorization"); got != "" {
		t.Fatalf("expected no Authorization header for real API key, got %q", got)
	}
	if got := receivedHeaders.Get("X-Api-Key"); got != "sk-ant-real-temp-key-from-exchange" {
		t.Fatalf("expected real API key to pass through, got %q", got)
	}
}

func TestOAuthMode_NoAuthHeader_InjectsToken(t *testing.T) {
	var receivedHeaders http.Header
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedHeaders = r.Header
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	p := newTestProxy(t, upstream.URL, "", "real-oauth-token")
	rp := p.newReverseProxy()

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	w := httptest.NewRecorder()
	rp.ServeHTTP(w, req)

	if got := receivedHeaders.Get("Authorization"); got != "Bearer real-oauth-token" {
		t.Fatalf("expected OAuth token injected, got %q", got)
	}
}

func TestOAuthMode_UsesCachedApiKeyForModels(t *testing.T) {
	var mu sync.Mutex
	headersByPath := make(map[string]http.Header)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		headersByPath[r.URL.Path] = r.Header.Clone()
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	p := newTestProxy(t, upstream.URL, "", "real-oauth-token")
	rp := p.newReverseProxy()

	first := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{}`))
	first.Header.Set("X-Api-Key", "sk-ant-real-temp-key-from-exchange")
	w := httptest.NewRecorder()
	rp.ServeHTTP(w, first)

	second := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	w = httptest.NewRecorder()
	rp.ServeHTTP(w, second)

	mu.Lock()
	modelsHeaders := headersByPath["/v1/models"]
	mu.Unlock()

	if modelsHeaders == nil {
		t.Fatal("expected /v1/models request")
	}
	if got := modelsHeaders.Get("X-Api-Key"); got != "sk-ant-real-temp-key-from-exchange" {
		t.Fatalf("expected cached API key, got %q", got)
	}
	if got := modelsHeaders.Get("Authorization"); got != "" {
		t.Fatalf("expected no Authorization header, got %q", got)
	}
}

func TestMetricsMiddleware_RecordsStatus(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	p := newTestProxy(t, upstream.URL, "sk-test", "")
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
	p := newTestProxy(t, "http://127.0.0.1:1", "sk-test", "")
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

	p := newTestProxy(t, upstream.URL, "sk-test", "")
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
	tests := []struct {
		name     string
		apiKey   string
		oauth    string
		expected string
	}{
		{"api-key mode", "sk-test", "", "api-key"},
		{"oauth mode", "", "token", "oauth"},
		{"api-key takes precedence", "sk-test", "token", "api-key"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := newTestProxy(t, "https://api.anthropic.com", tt.apiKey, tt.oauth)
			if got := p.authMode(); got != tt.expected {
				t.Fatalf("expected %q, got %q", tt.expected, got)
			}
		})
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

	p := newTestProxy(t, upstream.URL, "sk-test", "")
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

	p := newTestProxy(t, upstream.URL, "sk-test", "")
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

func (r *staticCredentialResolver) Resolve(_ context.Context, _ string) (*resolvedCredential, error) {
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

	proxy, err := NewMultiProviderProxy(config.ProxyConfig{Addr: ":0"}, resolver)
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

	proxy, err := NewMultiProviderProxy(config.ProxyConfig{Addr: ":0"}, resolver)
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

func TestProxy_AnthropicOAuthViaResolver(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if auth != "Bearer oauth-token-test" {
			t.Errorf("expected Bearer oauth-token-test, got %q", auth)
		}
		if r.Header.Get("X-Api-Key") != "" {
			t.Error("X-Api-Key should not be set for OAuth mode")
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	resolver := &staticCredentialResolver{
		cred: &resolvedCredential{
			Provider:    "anthropic",
			OAuthToken:  "oauth-token-test",
			UpstreamURL: upstream.URL,
		},
	}

	proxy, err := NewMultiProviderProxy(config.ProxyConfig{Addr: ":0"}, resolver)
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("POST", "/v1/messages", nil)
	req.Header.Set("X-Kraclaw-Group", "discord:789")
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

	cred, err := r.Resolve(context.Background(), "")
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

	cred, err := r.Resolve(context.Background(), "")
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

	_, err := r.Resolve(context.Background(), "discord:123")
	if err == nil {
		t.Fatal("expected error when no credentials configured")
	}
}
