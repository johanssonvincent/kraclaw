package credproxy

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/johanssonvincent/kraclaw/internal/config"
	"github.com/johanssonvincent/kraclaw/internal/metrics"
	"github.com/johanssonvincent/kraclaw/internal/provider"
)

// resolvedCredential contains provider-specific routing info for a single request.
type resolvedCredential struct {
	Provider    string
	APIKey      string
	OAuthToken  string
	UpstreamURL string
}

// CredentialResolver looks up credentials for a group.
type CredentialResolver interface {
	Resolve(ctx context.Context, groupJID string) (*resolvedCredential, error)
}

// defaultCredentialResolver wraps CredentialStore + platform fallback config.
type defaultCredentialResolver struct {
	credStore *CredentialStore
	cfg       config.ProxyConfig
}

// NewDefaultResolver creates a CredentialResolver that checks per-group credentials
// first, then falls back to platform-level credentials from config.
func NewDefaultResolver(credStore *CredentialStore, cfg config.ProxyConfig) *defaultCredentialResolver {
	return &defaultCredentialResolver{credStore: credStore, cfg: cfg}
}

// Resolve looks up credentials for a group, falling back to platform-level config.
func (r *defaultCredentialResolver) Resolve(ctx context.Context, groupJID string) (*resolvedCredential, error) {
	// Try per-group credential first.
	if r.credStore != nil && groupJID != "" {
		cred, err := r.credStore.GetCredential(ctx, groupJID)
		if err != nil {
			return nil, fmt.Errorf("resolve credential: %w", err)
		}
		if cred != nil {
			rc := &resolvedCredential{
				Provider:   cred.Provider,
				APIKey:     cred.APIKey,
				OAuthToken: cred.OAuthToken,
			}
			switch cred.Provider {
			case provider.ProviderOpenAI:
				rc.UpstreamURL = r.cfg.OpenAIUpstreamURL
			default:
				rc.UpstreamURL = r.cfg.AnthropicUpstreamURL
			}
			return rc, nil
		}
	}

	// Platform-level fallback: Anthropic.
	if r.cfg.AnthropicAPIKey != "" || r.cfg.AnthropicOAuthToken != "" {
		return &resolvedCredential{
			Provider:    provider.ProviderAnthropic,
			APIKey:      r.cfg.AnthropicAPIKey,
			OAuthToken:  r.cfg.AnthropicOAuthToken,
			UpstreamURL: r.cfg.AnthropicUpstreamURL,
		}, nil
	}

	// Platform-level fallback: OpenAI.
	if r.cfg.OpenAIAPIKey != "" {
		return &resolvedCredential{
			Provider:    provider.ProviderOpenAI,
			APIKey:      r.cfg.OpenAIAPIKey,
			UpstreamURL: r.cfg.OpenAIUpstreamURL,
		}, nil
	}

	return nil, fmt.Errorf("no credentials found for group %q and no platform fallback configured", groupJID)
}

// Proxy is a credential-injecting reverse proxy for AI provider APIs.
// Agent containers connect here instead of directly to the upstream API,
// so they never see real credentials.
type Proxy struct {
	upstream    *url.URL
	allowedHost string // upstream host for credential injection validation
	apiKey      string
	oauthToken  string
	server      *http.Server
	addr        string
	log         *slog.Logger
	resolver    CredentialResolver // nil = legacy single-provider mode

	mu              sync.RWMutex
	cachedAPIKey    string
	cachedAPIKeyAt  time.Time
	cachedAPIKeyTTL time.Duration
}

// New creates a new credential proxy from the given config.
func New(cfg config.ProxyConfig) (*Proxy, error) {
	if cfg.AnthropicUpstreamURL == "" {
		cfg.AnthropicUpstreamURL = "https://api.anthropic.com"
	}
	upstream, err := url.Parse(cfg.AnthropicUpstreamURL)
	if err != nil {
		return nil, fmt.Errorf("credproxy: invalid upstream URL: %w", err)
	}
	if cfg.AnthropicAPIKey == "" && cfg.AnthropicOAuthToken == "" {
		slog.Info("credproxy: no Anthropic credentials configured, Anthropic requests will use per-group credentials")
	}
	if cfg.AnthropicAPIKey != "" && cfg.AnthropicOAuthToken != "" {
		slog.Warn("both ANTHROPIC_API_KEY and ANTHROPIC_OAUTH_TOKEN set, API key takes precedence")
	}
	return &Proxy{
		upstream:        upstream,
		allowedHost:     upstream.Host,
		apiKey:          cfg.AnthropicAPIKey,
		oauthToken:      cfg.AnthropicOAuthToken,
		addr:            cfg.Addr,
		log:             slog.Default().With("component", "credproxy"),
		cachedAPIKeyTTL: 15 * time.Minute,
	}, nil
}

// NewMultiProviderProxy creates a credential proxy that supports multiple
// upstream providers via per-group credential resolution. When the resolver
// is set and a request includes the X-Kraclaw-Group header, credentials are
// resolved dynamically per request.
func NewMultiProviderProxy(cfg config.ProxyConfig, resolver CredentialResolver) (*Proxy, error) {
	if cfg.AnthropicUpstreamURL == "" {
		cfg.AnthropicUpstreamURL = "https://api.anthropic.com"
	}
	upstream, err := url.Parse(cfg.AnthropicUpstreamURL)
	if err != nil {
		return nil, fmt.Errorf("credproxy: invalid upstream URL: %w", err)
	}
	return &Proxy{
		upstream:        upstream,
		allowedHost:     upstream.Host,
		apiKey:          cfg.AnthropicAPIKey,
		oauthToken:      cfg.AnthropicOAuthToken,
		addr:            cfg.Addr,
		log:             slog.Default().With("component", "credproxy"),
		cachedAPIKeyTTL: 15 * time.Minute,
		resolver:        resolver,
	}, nil
}

// handler returns the HTTP handler for the proxy, useful for testing.
func (p *Proxy) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", p.handleHealthz)
	mux.HandleFunc("/readyz", p.handleReadyz)
	mux.Handle("/", p.metricsMiddleware(p.hostGuard(p.newReverseProxy())))
	return mux
}

// Start begins serving the proxy. It blocks until the context is cancelled
// or an error occurs.
func (p *Proxy) Start(ctx context.Context) error {
	p.server = &http.Server{
		Addr:              p.addr,
		Handler:           p.handler(),
		ReadHeaderTimeout: 10 * time.Second,
		WriteTimeout:      600 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	p.log.Info("credential proxy starting", "addr", p.addr, "upstream", p.upstream.String(), "auth_mode", p.authMode())

	errCh := make(chan error, 1)
	go func() {
		if err := p.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
		close(errCh)
	}()

	select {
	case err := <-errCh:
		return fmt.Errorf("credproxy: listen error: %w", err)
	case <-ctx.Done():
		return p.Stop(context.Background())
	}
}

// Stop gracefully shuts down the proxy server.
func (p *Proxy) Stop(ctx context.Context) error {
	if p.server == nil {
		return nil
	}
	p.log.Info("credential proxy stopping")
	return p.server.Shutdown(ctx)
}

func (p *Proxy) authMode() string {
	if p.apiKey != "" {
		return "api-key"
	}
	return "oauth"
}

func (p *Proxy) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

func (p *Proxy) handleReadyz(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

func (p *Proxy) newReverseProxy() *httputil.ReverseProxy {
	transport := &http.Transport{
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		TLSClientConfig:     &tls.Config{MinVersion: tls.VersionTLS12},
		TLSHandshakeTimeout: 10 * time.Second,
		MaxIdleConns:        20,
		MaxIdleConnsPerHost: 20,
		MaxConnsPerHost:     50,
		IdleConnTimeout:     90 * time.Second,
		DisableCompression:  true,
		ForceAttemptHTTP2:   true,
	}

	rp := &httputil.ReverseProxy{
		Transport:     transport,
		FlushInterval: -1, // flush SSE events immediately
		Director: func(req *http.Request) {
			// Check for multi-provider routing via X-Kraclaw-Group header.
			groupJID := req.Header.Get("X-Kraclaw-Group")

			if p.resolver != nil && groupJID != "" {
				// Multi-provider path: resolve credentials for this group.
				cred, err := p.resolver.Resolve(req.Context(), groupJID)
				if err != nil {
					p.log.Error("credential resolution failed",
						"group", groupJID,
						"error", err,
					)
					req.Header.Del("X-Api-Key")
					req.Header.Del("Authorization")
					req.Header.Del("X-Kraclaw-Group")
					req.Header.Del("X-Kraclaw-Provider")
					return
				}

				// Parse and set upstream URL from resolved credential.
				upstreamURL, err := url.Parse(cred.UpstreamURL)
				if err != nil {
					p.log.Error("invalid resolved upstream URL",
						"upstream_url", cred.UpstreamURL,
						"error", err,
					)
					req.Header.Del("X-Api-Key")
					req.Header.Del("Authorization")
					req.Header.Del("X-Kraclaw-Group")
					req.Header.Del("X-Kraclaw-Provider")
					return
				}

				req.URL.Scheme = upstreamURL.Scheme
				req.URL.Host = upstreamURL.Host
				req.Host = upstreamURL.Host

				// Strip hop-by-hop and kraclaw-internal headers.
				req.Header.Del("Connection")
				req.Header.Del("Keep-Alive")
				req.Header.Del("Transfer-Encoding")
				req.Header.Del("X-Kraclaw-Group")
				req.Header.Del("X-Kraclaw-Provider")

				// Inject provider-specific auth headers.
				switch cred.Provider {
				case provider.ProviderOpenAI:
					req.Header.Del("X-Api-Key")
					req.Header.Set("Authorization", "Bearer "+cred.APIKey)
				default: // anthropic
					if cred.APIKey != "" {
						req.Header.Del("Authorization")
						req.Header.Set("X-Api-Key", cred.APIKey)
					} else if cred.OAuthToken != "" {
						req.Header.Del("X-Api-Key")
						req.Header.Set("Authorization", "Bearer "+cred.OAuthToken)
					}
				}
				return
			}

			// Legacy single-provider path (backwards compatible).
			// Rewrite target to configured upstream.
			req.URL.Scheme = p.upstream.Scheme
			req.URL.Host = p.upstream.Host
			req.Host = p.upstream.Host

			// Safety check: verify the target host matches the allowlist.
			// This guards against programming errors or request manipulation
			// that could route credentials to an unintended host.
			if req.URL.Host != p.allowedHost {
				p.log.Error("blocked request to non-allowed host",
					"target_host", req.URL.Host,
					"allowed_host", p.allowedHost,
					"path", req.URL.Path,
				)
				// Clear any credentials that may have been set and return.
				// The request will fail at the transport level but no credentials leak.
				req.Header.Del("X-Api-Key")
				req.Header.Del("Authorization")
				return
			}

			// Strip hop-by-hop headers
			req.Header.Del("Connection")
			req.Header.Del("Keep-Alive")
			req.Header.Del("Transfer-Encoding")

			existingKey := req.Header.Get("X-Api-Key")
			if existingKey != "" && existingKey != "placeholder" {
				p.cacheAPIKey(existingKey)
			}

			if p.apiKey != "" {
				// API key mode: strip incoming auth, inject real key
				req.Header.Del("X-Api-Key")
				req.Header.Del("Authorization")
				req.Header.Set("X-Api-Key", p.apiKey)
			} else {
				// OAuth mode: only inject when no real X-Api-Key present (pre-exchange).
				// After token exchange, Claude Code sends a real X-Api-Key that should
				// pass through unmodified.
				if existingKey == "" || existingKey == "placeholder" {
					if p.shouldUseCachedAPIKey(req) {
						if cachedKey, ok := p.getCachedAPIKey(); ok {
							req.Header.Del("Authorization")
							req.Header.Set("X-Api-Key", cachedKey)
							return
						}
					}
					req.Header.Del("X-Api-Key")
					req.Header.Set("Authorization", "Bearer "+p.oauthToken)
				}
			}
		},
		ModifyResponse: func(resp *http.Response) error {
			p.log.Debug("upstream response",
				"status", resp.StatusCode,
				"content_type", resp.Header.Get("Content-Type"),
				"path", resp.Request.URL.Path,
			)
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			if errors.Is(err, context.Canceled) {
				p.log.Debug("client disconnected", "path", r.URL.Path)
				return
			}
			if errors.Is(err, context.DeadlineExceeded) {
				p.log.Warn("upstream timeout", "path", r.URL.Path)
				w.WriteHeader(http.StatusGatewayTimeout)
				return
			}
			p.log.Error("upstream error", "url", r.URL.String(), "error", err)
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte("Bad Gateway"))
		},
	}
	return rp
}

func (p *Proxy) metricsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rw := &responseWriter{ResponseWriter: w, statusCode: http.StatusOK}
		next.ServeHTTP(rw, r)
		duration := time.Since(start).Seconds()

		metrics.ProxyRequests.WithLabelValues(strconv.Itoa(rw.statusCode)).Inc()
		metrics.ProxyDuration.Observe(duration)
	})
}

// hostGuard rejects requests with a Host header that does not match the
// proxy's own listen address or is explicitly targeting an external host.
// This is a defense-in-depth measure against SSRF via Host header manipulation.
// When a resolver is configured, the guard is bypassed since the upstream
// changes dynamically per request based on the resolved provider.
func (p *Proxy) hostGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// When a resolver is set, the upstream is determined dynamically by the
		// Director, so static host checking is not applicable.
		if p.resolver != nil {
			next.ServeHTTP(w, r)
			return
		}
		// If the request has an explicit upstream target in the URL (absolute URI),
		// verify it matches the allowed upstream host.
		if r.URL.Host != "" && r.URL.Host != p.allowedHost {
			p.log.Warn("rejected request with non-allowed target host",
				"request_host", r.URL.Host,
				"allowed_host", p.allowedHost,
			)
			http.Error(w, "Forbidden: target host not allowed", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (p *Proxy) cacheAPIKey(key string) {
	if key == "" || key == "placeholder" {
		return
	}
	p.mu.Lock()
	p.cachedAPIKey = key
	p.cachedAPIKeyAt = time.Now()
	p.mu.Unlock()
}

func (p *Proxy) getCachedAPIKey() (string, bool) {
	p.mu.RLock()
	key := p.cachedAPIKey
	storedAt := p.cachedAPIKeyAt
	ttl := p.cachedAPIKeyTTL
	p.mu.RUnlock()

	if key == "" {
		return "", false
	}
	if ttl > 0 && time.Since(storedAt) > ttl {
		return "", false
	}
	return key, true
}

func (p *Proxy) shouldUseCachedAPIKey(req *http.Request) bool {
	return strings.HasPrefix(req.URL.Path, "/v1/models")
}

type responseWriter struct {
	http.ResponseWriter
	statusCode int
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.statusCode = code
	rw.ResponseWriter.WriteHeader(code)
}

func (rw *responseWriter) Flush() {
	if f, ok := rw.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (rw *responseWriter) Unwrap() http.ResponseWriter {
	return rw.ResponseWriter
}
