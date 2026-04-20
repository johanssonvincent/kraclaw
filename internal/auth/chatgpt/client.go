// Package chatgpt implements the OAuth 2.0 device-code flow and token refresh
// for OpenAI's ChatGPT subscription, mirroring the behaviour of the official
// Codex CLI at github.com/openai/codex (Apache-2.0).
//
// The package only knows how to talk to auth.openai.com. Wiring it into the
// credential store, credential proxy, or gRPC API lives in adjacent packages.
package chatgpt

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

const (
	// DefaultIssuer is the OAuth issuer hosting the ChatGPT auth endpoints.
	DefaultIssuer = "https://auth.openai.com"

	// ClientID is the public OAuth client id used by the Codex CLI; we reuse
	// it because there is no other public client id for ChatGPT subscriptions.
	ClientID = "app_EMoamEEZ73f0CkXaXp7hrann"

	// DefaultPollTimeout is the maximum total time PollUntilCode will spend
	// polling the device-code endpoint, matching the Codex CLI default.
	DefaultPollTimeout = 15 * time.Minute

	// DefaultPollInterval is the fallback polling interval when the server
	// response omits one.
	DefaultPollInterval = 5 * time.Second
)

// Config configures a Client. All fields are optional; sensible defaults are
// applied by NewClient.
type Config struct {
	// Issuer is the OAuth issuer base URL (no trailing slash). Defaults to
	// DefaultIssuer; tests may override it to point at an httptest server.
	Issuer string

	// ClientID overrides the OAuth client id. Defaults to the package ClientID.
	ClientID string

	// HTTPClient is the underlying transport. Defaults to a fresh http.Client
	// with a 30-second timeout.
	HTTPClient *http.Client

	// Now returns the current time. Tests inject this to control expiry math.
	Now func() time.Time

	// PollTimeout caps the time PollUntilCode will wait. Defaults to
	// DefaultPollTimeout.
	PollTimeout time.Duration

	// PollInterval overrides the server-provided polling interval. Useful in
	// tests to make the loop tight; in production leave at zero.
	PollInterval time.Duration
}

// Client speaks the ChatGPT OAuth device flow.
type Client struct {
	issuer       string
	clientID     string
	http         *http.Client
	now          func() time.Time
	pollTimeout  time.Duration
	pollOverride time.Duration
}

// NewClient builds a Client and validates the configuration.
func NewClient(cfg Config) (*Client, error) {
	c := &Client{
		issuer:       strings.TrimRight(cfg.Issuer, "/"),
		clientID:     cfg.ClientID,
		http:         cfg.HTTPClient,
		now:          cfg.Now,
		pollTimeout:  cfg.PollTimeout,
		pollOverride: cfg.PollInterval,
	}
	if c.issuer == "" {
		c.issuer = DefaultIssuer
	}
	if !strings.HasPrefix(c.issuer, "http://") && !strings.HasPrefix(c.issuer, "https://") {
		return nil, fmt.Errorf("chatgpt: issuer must be an http(s) URL, got %q", c.issuer)
	}
	if c.clientID == "" {
		c.clientID = ClientID
	}
	if c.http == nil {
		c.http = &http.Client{Timeout: 30 * time.Second}
	}
	if c.now == nil {
		c.now = time.Now
	}
	if c.pollTimeout <= 0 {
		c.pollTimeout = DefaultPollTimeout
	}
	return c, nil
}

// Issuer returns the configured issuer URL.
func (c *Client) Issuer() string { return c.issuer }

// VerificationURL is the human-facing page where the user enters the user_code.
func (c *Client) VerificationURL() string {
	return c.issuer + "/codex/device"
}

// RedirectURI is the redirect URL the device-flow PKCE grant is bound to.
// It mirrors the constant Codex uses; the browser is never actually sent here
// during the device flow, but the OAuth token endpoint requires it to match.
func (c *Client) RedirectURI() string {
	return c.issuer + "/deviceauth/callback"
}

// errBadStatus wraps a non-success HTTP response so callers can introspect.
type errBadStatus struct {
	Status int
	Body   string
	URL    string
}

func (e *errBadStatus) Error() string {
	return fmt.Sprintf("chatgpt: %s returned status %d: %s", e.URL, e.Status, truncate(e.Body, 256))
}

// ErrAuthorizationPending is returned by PollOnce while the user has not yet
// approved the device code. Callers should sleep for the device-code interval
// and retry.
var ErrAuthorizationPending = errors.New("chatgpt: authorization pending")

// ErrDeviceAuthTimeout is returned by PollUntilCode after PollTimeout elapses
// without a successful response.
var ErrDeviceAuthTimeout = errors.New("chatgpt: device authorization timed out")

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
