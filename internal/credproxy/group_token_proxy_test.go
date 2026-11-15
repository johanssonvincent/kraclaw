package credproxy

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/johanssonvincent/kraclaw/internal/config"
	"github.com/johanssonvincent/kraclaw/internal/credproxy/grouptoken"
)

const (
	testGroupTokenSecret = "0123456789abcdef0123456789abcdef"
	victimGroup          = "discord:victim"
	attackerGroup        = "telegram:attacker"
	victimKey            = "sk-ant-victim"
)

// newTokenGuardProxy builds a multi-provider proxy with group-token enforcement
// enabled and an upstream that records what it was sent. It returns the upstream
// request counter so a test can assert that a rejected call never reached the
// provider.
func newTokenGuardProxy(t *testing.T, secret string) (*Proxy, *int32, *http.Header, string) {
	t.Helper()

	var (
		hits  int32
		seen  http.Header
		upURL string
	)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)

		seen = r.Header.Clone()

		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(upstream.Close)

	upURL = upstream.URL

	resolver := &staticCredentialResolver{
		cred: &resolvedCredential{
			Provider:    "anthropic",
			APIKey:      victimKey,
			UpstreamURL: upstream.URL,
		},
	}

	proxy, err := NewMultiProviderProxy(config.ProxyConfig{
		Addr:                 ":0",
		AnthropicUpstreamURL: upstream.URL,
		GroupTokenSecret:     secret,
	}, resolver)
	if err != nil {
		t.Fatalf("NewMultiProviderProxy: %v", err)
	}

	return proxy, &hits, &seen, upURL
}

// TestProxy_GroupTokenGuard is the regression test for unauthenticated tenant
// selection: before the guard existed, naming a group in X-Kraclaw-Group was
// enough to have that group's stored credential injected into the caller's
// request, so any sandbox could spend another tenant's provider quota.
func TestProxy_GroupTokenGuard(t *testing.T) {
	validVictimToken := grouptoken.Token(testGroupTokenSecret, victimGroup)

	tests := []struct {
		name       string
		secret     string
		group      string
		token      string
		wantStatus int
		wantHits   int32
	}{
		{
			name:       "valid token for the named group is served",
			secret:     testGroupTokenSecret,
			group:      victimGroup,
			token:      validVictimToken,
			wantStatus: http.StatusOK,
			wantHits:   1,
		},
		{
			name:       "no token is rejected",
			secret:     testGroupTokenSecret,
			group:      victimGroup,
			token:      "",
			wantStatus: http.StatusForbidden,
			wantHits:   0,
		},
		{
			name:       "token for a different group is rejected",
			secret:     testGroupTokenSecret,
			group:      victimGroup,
			token:      grouptoken.Token(testGroupTokenSecret, attackerGroup),
			wantStatus: http.StatusForbidden,
			wantHits:   0,
		},
		{
			name:       "token minted with a different secret is rejected",
			secret:     testGroupTokenSecret,
			group:      victimGroup,
			token:      grouptoken.Token("ffffffffffffffffffffffffffffffff", victimGroup),
			wantStatus: http.StatusForbidden,
			wantHits:   0,
		},
		{
			name:       "garbage token is rejected",
			secret:     testGroupTokenSecret,
			group:      victimGroup,
			token:      "not-a-token",
			wantStatus: http.StatusForbidden,
			wantHits:   0,
		},
		{
			name:       "enforcement is skipped when no secret is configured",
			secret:     "",
			group:      victimGroup,
			token:      "",
			wantStatus: http.StatusOK,
			wantHits:   1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			proxy, hits, _, _ := newTokenGuardProxy(t, tt.secret)

			req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
			req.Header.Set(grouptoken.HeaderGroup, tt.group)

			if tt.token != "" {
				req.Header.Set(grouptoken.HeaderGroupToken, tt.token)
			}

			w := httptest.NewRecorder()
			proxy.handler().ServeHTTP(w, req)

			if w.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d (body %q)", w.Code, tt.wantStatus, w.Body.String())
			}

			if got := atomic.LoadInt32(hits); got != tt.wantHits {
				t.Errorf("upstream hits = %d, want %d", got, tt.wantHits)
			}
		})
	}
}

// TestProxy_GroupTokenForwardedToUpstreamNeverLeaksInternalHeaders asserts that
// the credential actually injected is the named group's, and that none of the
// internal routing headers reach the provider.
func TestProxy_GroupTokenForwardedToUpstreamNeverLeaksInternalHeaders(t *testing.T) {
	proxy, _, seen, _ := newTokenGuardProxy(t, testGroupTokenSecret)

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	req.Header.Set(grouptoken.HeaderGroup, victimGroup)
	req.Header.Set(grouptoken.HeaderProvider, "anthropic")
	req.Header.Set(grouptoken.HeaderGroupToken, grouptoken.Token(testGroupTokenSecret, victimGroup))

	w := httptest.NewRecorder()
	proxy.handler().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", w.Code, w.Body.String())
	}

	if shared := *seen; shared == nil {
		t.Fatal("upstream was not called")
	}

	if got := seen.Get("X-Api-Key"); got != victimKey {
		t.Errorf("upstream X-Api-Key = %q, want the group's credential", got)
	}

	for _, h := range []string{grouptoken.HeaderGroup, grouptoken.HeaderProvider, grouptoken.HeaderGroupToken} {
		if got := seen.Get(h); got != "" {
			t.Errorf("internal header %s leaked upstream: %q", h, got)
		}
	}
}

// TestProxy_PlatformPathWithoutGroupHeaderStillWorks keeps the legacy path
// usable: requests that do not claim a tenant resolve the deployment's own key
// and must not be caught by the guard.
func TestProxy_PlatformPathWithoutGroupHeaderStillWorks(t *testing.T) {
	proxy, hits, _, _ := newTokenGuardProxy(t, testGroupTokenSecret)

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	w := httptest.NewRecorder()
	proxy.handler().ServeHTTP(w, req)

	if w.Code == http.StatusForbidden {
		t.Fatalf("requests without a group header must not require a token, got 403")
	}

	if atomic.LoadInt32(hits) != 1 {
		t.Fatalf("upstream hits = %d, want 1", atomic.LoadInt32(hits))
	}
}

// TestProxy_LegacyPathStripsGroupHeader covers the single-provider path, which
// previously forwarded the tenant JID to the provider.
func TestProxy_LegacyPathStripsGroupHeader(t *testing.T) {
	var seen http.Header

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Clone()
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	proxy, err := New(config.ProxyConfig{
		Addr:                 ":0",
		AnthropicUpstreamURL: upstream.URL,
		AnthropicAPIKey:      victimKey,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	req.Header.Set(grouptoken.HeaderGroup, victimGroup)

	w := httptest.NewRecorder()
	proxy.handler().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	if got := seen.Get(grouptoken.HeaderGroup); got != "" {
		t.Errorf("legacy path leaked %s upstream: %q", grouptoken.HeaderGroup, got)
	}
}
