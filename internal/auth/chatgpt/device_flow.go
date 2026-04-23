package chatgpt

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// DeviceCode is the result of starting the device flow. The verification URL
// and user code go to the human; device_auth_id stays in the client and is
// fed back to the polling endpoint.
type DeviceCode struct {
	UserCode        string
	VerificationURL string
	Interval        time.Duration

	deviceAuthID string
}

// AuthorizationCode is the intermediate artefact returned by the device-token
// poll once the user has approved. It is exchanged at /oauth/token via
// ExchangeCode for the final token bundle. CodeVerifier is the PKCE secret
// sent during the exchange; the server-side challenge is never consumed by
// this client.
type AuthorizationCode struct {
	Code         string
	CodeVerifier string
}

// Tokens is the final OAuth bundle plus parsed id_token claims.
type Tokens struct {
	AccessToken  string
	RefreshToken string
	IDToken      string
	IDClaims     IDTokenClaims

	// ExpiresAt is the absolute token expiry. Zero when the server returned
	// neither id_token.exp nor expires_in; callers must check HasExpiry
	// rather than comparing ExpiresAt to time.Now directly.
	ExpiresAt time.Time
}

// HasExpiry reports whether the server returned a usable expiry for these
// tokens. False means the absolute expiry is unknown; callers should treat
// the access token as "validity managed server-side" and refresh proactively.
func (t *Tokens) HasExpiry() bool {
	return !t.ExpiresAt.IsZero()
}

// userCodeResponse is the device-auth/usercode JSON envelope. interval is
// decoded via intervalString because observed responses encode it as either
// a JSON number or string.
type userCodeResponse struct {
	DeviceAuthID string         `json:"device_auth_id"`
	UserCode     string         `json:"user_code"`
	UserCodeAlt  string         `json:"usercode"`
	Interval     intervalString `json:"interval"`
}

type intervalString time.Duration

func (i *intervalString) UnmarshalJSON(b []byte) error {
	if len(b) == 0 || string(b) == "null" {
		return nil
	}
	// Accept "5", 5, "5.0".
	var (
		secs float64
		err  error
	)
	if b[0] == '"' {
		var s string
		if err = json.Unmarshal(b, &s); err != nil {
			return err
		}
		if s == "" {
			return nil
		}
		secs, err = strconv.ParseFloat(s, 64)
	} else {
		err = json.Unmarshal(b, &secs)
	}
	if err != nil {
		return fmt.Errorf("chatgpt: parse interval: %w", err)
	}
	*i = intervalString(time.Duration(secs * float64(time.Second)))
	return nil
}

type codeSuccessResponse struct {
	AuthorizationCode string `json:"authorization_code"`
	CodeVerifier      string `json:"code_verifier"`
}

// tokenResponse is the /oauth/token success JSON body.
type tokenResponse struct {
	IDToken      string `json:"id_token"`
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in,omitempty"`
}

// RequestDeviceCode starts the device flow and returns the user-facing code,
// verification URL, and polling interval.
func (c *Client) RequestDeviceCode(ctx context.Context) (*DeviceCode, error) {
	body, err := json.Marshal(struct {
		ClientID string `json:"client_id"`
	}{ClientID: c.clientID})
	if err != nil {
		return nil, fmt.Errorf("chatgpt: marshal device-code request: %w", err)
	}
	endpoint := c.issuer + "/api/accounts/deviceauth/usercode"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("chatgpt: build device-code request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("chatgpt: device-code request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("chatgpt: read device-code response: %w", err)
	}
	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("chatgpt: device-code login is not enabled by the auth server (status 404)")
	}
	if resp.StatusCode/100 != 2 {
		c.logger.Warn("chatgpt: device-code request returned non-2xx",
			slog.Int("status", resp.StatusCode),
			slog.String("url", endpoint),
			slog.String("body_preview", truncate(string(respBody), 200)))
		return nil, &errBadStatus{Status: resp.StatusCode, Body: string(respBody), URL: endpoint}
	}

	var parsed userCodeResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return nil, fmt.Errorf("chatgpt: decode device-code response: %w", err)
	}
	dc := &DeviceCode{
		deviceAuthID:    parsed.DeviceAuthID,
		UserCode:        firstNonEmpty(parsed.UserCode, parsed.UserCodeAlt),
		VerificationURL: c.VerificationURL(),
		Interval:        time.Duration(parsed.Interval),
	}
	if dc.deviceAuthID == "" || dc.UserCode == "" {
		c.logger.Warn("chatgpt: device-code response missing required fields",
			slog.Int("status", resp.StatusCode),
			slog.String("url", endpoint),
			slog.Bool("device_auth_id_empty", dc.deviceAuthID == ""),
			slog.Bool("user_code_empty", dc.UserCode == ""),
			slog.String("body_preview", truncate(string(respBody), 200)))
		return nil, fmt.Errorf("chatgpt: device-code response missing device_auth_id or user_code")
	}
	if dc.Interval <= 0 {
		dc.Interval = DefaultPollInterval
	}
	return dc, nil
}

// PollOnce performs a single poll of the device-token endpoint. It returns
// the authorization code on success, ErrAuthorizationPending when the user
// has not yet approved, or ErrSlowDown when the server asks the client to
// back off. See pollPendingCode for the status codes treated as pending.
// Any other failure is returned wrapped.
func (c *Client) PollOnce(ctx context.Context, dc *DeviceCode) (*AuthorizationCode, error) {
	if dc == nil || dc.deviceAuthID == "" || dc.UserCode == "" {
		return nil, fmt.Errorf("chatgpt: device code is empty")
	}
	body, err := json.Marshal(struct {
		DeviceAuthID string `json:"device_auth_id"`
		UserCode     string `json:"user_code"`
	}{DeviceAuthID: dc.deviceAuthID, UserCode: dc.UserCode})
	if err != nil {
		return nil, fmt.Errorf("chatgpt: marshal poll request: %w", err)
	}
	endpoint := c.issuer + "/api/accounts/deviceauth/token"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("chatgpt: build poll request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("chatgpt: poll request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("chatgpt: read poll response: %w", err)
	}

	if resp.StatusCode/100 == 2 {
		var parsed codeSuccessResponse
		if err := json.Unmarshal(respBody, &parsed); err != nil {
			return nil, fmt.Errorf("chatgpt: decode poll success response: %w", err)
		}
		if parsed.AuthorizationCode == "" || parsed.CodeVerifier == "" {
			return nil, fmt.Errorf("chatgpt: poll success response missing authorization_code or code_verifier")
		}
		return &AuthorizationCode{
			Code:         parsed.AuthorizationCode,
			CodeVerifier: parsed.CodeVerifier,
		}, nil
	}
	switch pollPendingCode(resp.StatusCode, respBody) {
	case "authorization_pending":
		return nil, ErrAuthorizationPending
	case "slow_down":
		return nil, ErrSlowDown
	}
	c.logger.Warn("chatgpt: poll returned non-pending non-success status",
		slog.Int("status", resp.StatusCode),
		slog.String("url", endpoint),
		slog.String("body_preview", truncate(string(respBody), 200)))
	return nil, &errBadStatus{Status: resp.StatusCode, Body: string(respBody), URL: endpoint}
}

// pollPendingCode returns the RFC 8628 pending error code
// ("authorization_pending" or "slow_down") from a device-token response, or
// "" if the response is not a pending error. HTTP 400 is the RFC-conformant
// status; 403/404 are accepted for older Codex backends that used the same
// body code at different status values.
func pollPendingCode(status int, body []byte) string {
	if status != http.StatusBadRequest && status != http.StatusForbidden && status != http.StatusNotFound {
		return ""
	}
	var parsed struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(body, &parsed)
	code := strings.ToLower(strings.TrimSpace(parsed.Error))
	switch code {
	case "authorization_pending", "slow_down":
		return code
	}
	return ""
}

// PollUntilCode loops PollOnce on the device-code interval until the user
// approves, the context is cancelled, or PollTimeout elapses. The optional
// onTick callback fires once per pending poll so callers (e.g. the gRPC
// streaming RPC) can emit heartbeats; nil disables it.
func (c *Client) PollUntilCode(ctx context.Context, dc *DeviceCode, onTick func()) (*AuthorizationCode, error) {
	if dc == nil {
		return nil, fmt.Errorf("chatgpt: device code is nil")
	}
	interval := c.pollOverride
	if interval <= 0 {
		interval = dc.Interval
	}
	if interval <= 0 {
		interval = DefaultPollInterval
	}
	pollCtx, cancel := context.WithTimeout(ctx, c.pollTimeout)
	defer cancel()

	for {
		code, err := c.PollOnce(pollCtx, dc)
		switch {
		case err == nil:
			return code, nil
		case errors.Is(err, ErrAuthorizationPending):
			// fall through to pending-handling below
		default:
			// Parent ctx takes precedence: if the caller's ctx is already
			// done, surface their error rather than attributing it to our
			// internal PollTimeout.
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if errors.Is(pollCtx.Err(), context.DeadlineExceeded) {
				return nil, ErrDeviceAuthTimeout
			}
			return nil, err
		}

		// Pending path only from here down.

		// RFC 8628 §3.5: on slow_down the interval MUST be increased by 5s
		// for this and all subsequent requests.
		if errors.Is(err, ErrSlowDown) {
			interval += slowDownBackoff
		}

		if onTick != nil {
			onTick()
		}

		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-pollCtx.Done():
			timer.Stop()
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, ErrDeviceAuthTimeout
		case <-timer.C:
		}
	}
}

// ExchangeCode trades an AuthorizationCode for the final OAuth token bundle.
// The token endpoint expects an x-www-form-urlencoded body matching the
// authorization-code grant with PKCE.
func (c *Client) ExchangeCode(ctx context.Context, code *AuthorizationCode) (*Tokens, error) {
	if code == nil || code.Code == "" || code.CodeVerifier == "" {
		return nil, fmt.Errorf("chatgpt: authorization code is empty")
	}
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code.Code},
		"redirect_uri":  {c.RedirectURI()},
		"client_id":     {c.clientID},
		"code_verifier": {code.CodeVerifier},
	}
	endpoint := c.issuer + "/oauth/token"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader([]byte(form.Encode())))
	if err != nil {
		return nil, fmt.Errorf("chatgpt: build token-exchange request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("chatgpt: token-exchange request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("chatgpt: read token-exchange response: %w", err)
	}
	if resp.StatusCode/100 != 2 {
		c.logger.Warn("chatgpt: token-exchange returned non-2xx",
			slog.Int("status", resp.StatusCode),
			slog.String("url", endpoint),
			slog.String("body_preview", truncate(string(respBody), 200)))
		return nil, &errBadStatus{Status: resp.StatusCode, Body: string(respBody), URL: endpoint}
	}

	var parsed tokenResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return nil, fmt.Errorf("chatgpt: decode token-exchange response: %w", err)
	}
	return c.tokensFromResponse(&parsed)
}

func (c *Client) tokensFromResponse(parsed *tokenResponse) (*Tokens, error) {
	if parsed.AccessToken == "" {
		return nil, fmt.Errorf("chatgpt: token response missing access_token")
	}
	if parsed.RefreshToken == "" {
		return nil, fmt.Errorf("chatgpt: token response missing refresh_token")
	}
	if parsed.IDToken == "" {
		return nil, fmt.Errorf("chatgpt: token response missing id_token")
	}
	claims, err := ParseIDToken(parsed.IDToken)
	if err != nil {
		return nil, fmt.Errorf("chatgpt: parse id_token from token response: %w", err)
	}
	tokens := &Tokens{
		AccessToken:  parsed.AccessToken,
		RefreshToken: parsed.RefreshToken,
		IDToken:      parsed.IDToken,
		IDClaims:     claims,
		ExpiresAt:    claims.ExpiresAt,
	}
	if tokens.ExpiresAt.IsZero() && parsed.ExpiresIn > 0 {
		tokens.ExpiresAt = c.now().Add(time.Duration(parsed.ExpiresIn) * time.Second)
	}
	return tokens, nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
