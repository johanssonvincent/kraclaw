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

// DeviceAuthID exposes the opaque server-issued id used to poll for the
// authorization code. Callers that need to persist the device flow across
// process boundaries can stash this and rebuild a DeviceCode later via
// DeviceCodeFromParts.
func (d *DeviceCode) DeviceAuthID() string { return d.deviceAuthID }

// DeviceCodeFromParts reconstructs a DeviceCode from previously persisted
// fields. Useful when the polling loop runs in a different goroutine or
// process than the one that requested the code.
func DeviceCodeFromParts(deviceAuthID, userCode, verificationURL string, interval time.Duration) *DeviceCode {
	return &DeviceCode{
		deviceAuthID:    deviceAuthID,
		UserCode:        userCode,
		VerificationURL: verificationURL,
		Interval:        interval,
	}
}

// AuthorizationCode is the intermediate artefact returned by the device-token
// poll once the user has approved. It is exchanged at /oauth/token via
// ExchangeCode for the final token bundle.
type AuthorizationCode struct {
	Code          string
	CodeChallenge string
	CodeVerifier  string
}

// Tokens is the final OAuth bundle plus parsed id_token claims.
type Tokens struct {
	AccessToken  string
	RefreshToken string
	IDToken      string
	IDClaims     IDTokenClaims

	// ExpiresAt is the absolute token expiry. Valid only when HasExpiry is
	// true — otherwise the server returned neither id_token.exp nor
	// expires_in and the absolute expiry is unknown.
	ExpiresAt time.Time
	HasExpiry bool
}

// userCodeResponse mirrors the Codex UserCodeResp struct. interval is sent
// either as a JSON string or number depending on backend version, so we
// accept both via a custom decoder.
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

// codeSuccessResponse mirrors the Codex CodeSuccessResp.
type codeSuccessResponse struct {
	AuthorizationCode string `json:"authorization_code"`
	CodeChallenge     string `json:"code_challenge"`
	CodeVerifier      string `json:"code_verifier"`
}

// tokenResponse mirrors the Codex /oauth/token success body.
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
		return nil, fmt.Errorf("chatgpt: device-code response missing device_auth_id or user_code")
	}
	if dc.Interval <= 0 {
		dc.Interval = DefaultPollInterval
	}
	return dc, nil
}

// PollOnce performs a single poll of the device-token endpoint. It returns the
// authorization code on success or ErrAuthorizationPending when the user has
// not yet approved (HTTP 403 or 404). Any other failure is returned wrapped.
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

	switch {
	case resp.StatusCode/100 == 2:
		var parsed codeSuccessResponse
		if err := json.Unmarshal(respBody, &parsed); err != nil {
			return nil, fmt.Errorf("chatgpt: decode poll success response: %w", err)
		}
		if parsed.AuthorizationCode == "" || parsed.CodeVerifier == "" {
			return nil, fmt.Errorf("chatgpt: poll success response missing authorization_code or code_verifier")
		}
		return &AuthorizationCode{
			Code:          parsed.AuthorizationCode,
			CodeChallenge: parsed.CodeChallenge,
			CodeVerifier:  parsed.CodeVerifier,
		}, nil
	case isPollPending(resp.StatusCode, respBody):
		return nil, ErrAuthorizationPending
	default:
		c.logger.Warn("chatgpt: poll returned non-pending non-success status",
			slog.Int("status", resp.StatusCode),
			slog.String("url", endpoint),
			slog.String("body_preview", truncate(string(respBody), 200)))
		return nil, &errBadStatus{Status: resp.StatusCode, Body: string(respBody), URL: endpoint}
	}
}

// isPollPending returns true when the device-token response represents an
// RFC 8628 pending state: HTTP 400 with error=authorization_pending|slow_down,
// or — for older Codex backends — 403/404 with the same body code.
func isPollPending(status int, body []byte) bool {
	if status != http.StatusBadRequest && status != http.StatusForbidden && status != http.StatusNotFound {
		return false
	}
	var parsed struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(body, &parsed)
	switch strings.ToLower(strings.TrimSpace(parsed.Error)) {
	case "authorization_pending", "slow_down":
		return true
	}
	return false
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
		if err == nil {
			return code, nil
		}
		if !errors.Is(err, ErrAuthorizationPending) {
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

		if onTick != nil {
			onTick()
		}

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-pollCtx.Done():
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, ErrDeviceAuthTimeout
		case <-time.After(interval):
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
	tokens.HasExpiry = !tokens.ExpiresAt.IsZero()
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
