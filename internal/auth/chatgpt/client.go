// Package chatgpt implements the OAuth 2.0 device-code flow...
package chatgpt

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

const (
	// DefaultIssuer is the OAuth issuer hosting the ChatGPT auth endpoints.
	DefaultIssuer = "https://auth.openai.com"

// ClientID is the OAuth client id shared with the Codex CLI...
	ClientID = "app_EMoamEEZ73f0CkXaXp7hrann"

	// DefaultPollTimeout caps total polling at 15 minutes (upper bound on how
	// long a user has to approve the device code).
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

	// Logger is the structured logger used for warnings (provider drift, parse
	// failures). Defaults to slog.Default() when nil.
	Logger *slog.Logger
}

// Client speaks the ChatGPT OAuth device flow.
type Client struct {
	issuer       string
	clientID     string
	http         *http.Client
	now          func() time.Time
	pollTimeout  time.Duration
	pollOverride time.Duration
	logger       *slog.Logger
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

	if cfg.Logger == nil {
		c.logger = slog.Default()
	} else {
		c.logger = cfg.Logger
	}

	return c, nil
}

// Issuer returns the configured issuer URL.
func (c *Client) Issuer() string { return c.issuer }

// VerificationURL is the human-facing consent page where th...
func (c *Client) VerificationURL() string {
	return c.issuer + "/codex/device"
}

// RedirectURI is the redirect URL the device-flow PKCE gran...
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

// ErrAuthorizationPending is returned by PollOnce while the...
var ErrAuthorizationPending = errors.New("chatgpt: authorization pending")

// ErrSlowDown is returned by PollOnce when the server respo...
var ErrSlowDown = fmt.Errorf("%w: slow_down", ErrAuthorizationPending)

// slowDownBackoff is the RFC 8628 §3.5 mandatory interval bump applied on
// each slow_down response.
const slowDownBackoff = 5 * time.Second

// ErrAccessDenied is returned by PollOnce / ExchangeCode wh...
var ErrAccessDenied = errors.New("chatgpt: access denied")

// ErrDeviceAuthTimeout is returned by PollUntilCode after PollTimeout elapses
// without a successful response.
var ErrDeviceAuthTimeout = errors.New("chatgpt: device authorization timed out")

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}

	return s[:n] + "…"
}

// maxResponseBodySize caps HTTP response bodies read from t...
const maxResponseBodySize = 1 << 20

// ErrResponseTooLarge is returned when an OAuth endpoint response exceeds
// maxResponseBodySize.
var ErrResponseTooLarge = errors.New("chatgpt: response body exceeded 1 MiB cap")

// readCappedBody reads r up to maxResponseBodySize, returni...
func readCappedBody(r io.Reader) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(r, maxResponseBodySize+1))
	if err != nil {
		return nil, err
	}

	if len(body) > maxResponseBodySize {
		return nil, ErrResponseTooLarge
	}

	return body, nil
}
