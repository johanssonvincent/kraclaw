package chatgpt

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// RefreshFailureReason classifies permanent /oauth/token refresh failures so
// callers can decide whether to surface "please re-sign-in" UX vs. retry.
type RefreshFailureReason string

const (
	// RefreshFailureExpired = the refresh token itself has expired.
	RefreshFailureExpired RefreshFailureReason = "refresh_token_expired"
	// RefreshFailureReused = a previous refresh response was used twice;
	// the token rotation contract has been broken.
	RefreshFailureReused RefreshFailureReason = "refresh_token_reused"
	// RefreshFailureRevoked = the user or an admin invalidated the token.
	RefreshFailureRevoked RefreshFailureReason = "refresh_token_invalidated"
	// RefreshFailureUnknown = a 401 with a code we don't recognise.
	RefreshFailureUnknown RefreshFailureReason = "unknown"
)

// RefreshErrorKind discriminates permanent vs transient refresh failures.
type RefreshErrorKind int

const (
	// RefreshErrorTransient — caller should retry after backoff.
	RefreshErrorTransient RefreshErrorKind = iota + 1
	// RefreshErrorPermanent — caller must trigger a fresh device-flow sign-in.
	RefreshErrorPermanent
)

// RefreshError describes a token-refresh failure.
type RefreshError struct {
	Kind   RefreshErrorKind
	Reason RefreshFailureReason // meaningful when Kind == RefreshErrorPermanent; otherwise RefreshFailureUnknown
	Status int
	Body   string
	cause  error
}

// Permanent reports whether the caller must trigger a new device-flow sign-in.
func (e *RefreshError) Permanent() bool { return e.Kind == RefreshErrorPermanent }

func (e *RefreshError) Error() string {
	kind := "transient"
	if e.Kind == RefreshErrorPermanent {
		kind = "permanent"
	}
	if e.cause != nil {
		return fmt.Sprintf("chatgpt: refresh failed (kind=%s, status=%d, reason=%s): %v", kind, e.Status, e.Reason, e.cause)
	}
	return fmt.Sprintf("chatgpt: refresh failed (kind=%s, status=%d, reason=%s): %s", kind, e.Status, e.Reason, truncate(e.Body, 200))
}

func (e *RefreshError) Unwrap() error { return e.cause }

func newTransientRefresh(status int, body string, cause error) *RefreshError {
	return &RefreshError{Kind: RefreshErrorTransient, Reason: RefreshFailureUnknown, Status: status, Body: body, cause: cause}
}

func newPermanentRefresh(status int, body string, reason RefreshFailureReason) *RefreshError {
	return &RefreshError{Kind: RefreshErrorPermanent, Reason: reason, Status: status, Body: body}
}

// Refresh exchanges a refresh token for a new ChatGPT OAuth bundle. The
// returned Tokens may carry the same refresh_token as the input if the server
// chose not to rotate it; callers should always persist the value Refresh
// returns rather than retaining the original.
func (c *Client) Refresh(ctx context.Context, refreshToken string) (*Tokens, error) {
	if strings.TrimSpace(refreshToken) == "" {
		return nil, fmt.Errorf("chatgpt: refresh token is empty")
	}
	body, err := json.Marshal(struct {
		ClientID     string `json:"client_id"`
		GrantType    string `json:"grant_type"`
		RefreshToken string `json:"refresh_token"`
	}{ClientID: c.clientID, GrantType: "refresh_token", RefreshToken: refreshToken})
	if err != nil {
		return nil, fmt.Errorf("chatgpt: marshal refresh request: %w", err)
	}
	endpoint := c.issuer + "/oauth/token"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("chatgpt: build refresh request: %w", err)
	}
	// ChatGPT's /oauth/token accepts JSON for the refresh_token grant (in contrast
	// to the authorization_code grant at ExchangeCode, which is form-encoded).
	// Matches the Codex CLI contract.
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, newTransientRefresh(0, "", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, newTransientRefresh(resp.StatusCode, "", err)
	}

	if resp.StatusCode/100 != 2 {
		reason, parsed := classifyRefreshFailure(respBody)
		if resp.StatusCode == http.StatusUnauthorized && !parsed {
			c.logger.Warn("chatgpt: refresh response body unparseable",
				slog.Int("status", resp.StatusCode),
				slog.String("body_preview", truncate(string(respBody), 200)))
		}
		permanent := (resp.StatusCode == http.StatusUnauthorized && parsed) ||
			isPermanentBadRequest(resp.StatusCode, respBody, reason, parsed)
		if permanent {
			return nil, newPermanentRefresh(resp.StatusCode, string(respBody), reason)
		}
		return nil, newTransientRefresh(resp.StatusCode, string(respBody), nil)
	}

	var parsed tokenResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		c.logger.Warn("chatgpt: refresh 2xx body failed to decode",
			slog.Int("status", resp.StatusCode),
			slog.String("body_preview", truncate(string(respBody), 200)),
			slog.String("error", err.Error()))
		return nil, newTransientRefresh(resp.StatusCode, "", fmt.Errorf("decode refresh response: %w", err))
	}

	// Server may rotate refresh_token (and re-issue id_token) but is allowed to
	// omit either. Keep the previous values when fields are absent.
	if parsed.RefreshToken == "" {
		parsed.RefreshToken = refreshToken
	}
	if parsed.AccessToken == "" {
		return nil, newTransientRefresh(resp.StatusCode, "", fmt.Errorf("refresh response missing access_token"))
	}
	tokens := &Tokens{
		AccessToken:  parsed.AccessToken,
		RefreshToken: parsed.RefreshToken,
		IDToken:      parsed.IDToken,
	}
	if parsed.IDToken != "" {
		claims, err := ParseIDToken(parsed.IDToken)
		if err != nil {
			c.logger.Warn("chatgpt: refresh returned malformed id_token",
				slog.Int("status", resp.StatusCode),
				slog.String("error", err.Error()))
			return nil, newTransientRefresh(resp.StatusCode, "", fmt.Errorf("parse refreshed id_token: %w", err))
		}
		tokens.IDClaims = claims
		tokens.ExpiresAt = claims.ExpiresAt
	}
	if tokens.ExpiresAt.IsZero() && parsed.ExpiresIn > 0 {
		tokens.ExpiresAt = c.now().Add(time.Second * time.Duration(parsed.ExpiresIn))
	}
	tokens.HasExpiry = !tokens.ExpiresAt.IsZero()
	return tokens, nil
}

// classifyRefreshFailure inspects the JSON error body returned by /oauth/token
// and maps recognized OpenAI codes to a RefreshFailureReason.
// The bool return is true iff the body was valid JSON with at least one of
// error / error_code / error_description populated.
func classifyRefreshFailure(body []byte) (RefreshFailureReason, bool) {
	var parsed struct {
		Error            string `json:"error"`
		ErrorCode        string `json:"error_code"`
		ErrorDescription string `json:"error_description"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return RefreshFailureUnknown, false
	}
	parsedAny := parsed.Error != "" || parsed.ErrorCode != "" || parsed.ErrorDescription != ""
	for _, candidate := range []string{parsed.ErrorCode, parsed.Error, parsed.ErrorDescription} {
		switch strings.ToLower(strings.TrimSpace(candidate)) {
		case "refresh_token_expired":
			return RefreshFailureExpired, parsedAny
		case "refresh_token_reused":
			return RefreshFailureReused, parsedAny
		case "refresh_token_invalidated":
			return RefreshFailureRevoked, parsedAny
		}
	}
	return RefreshFailureUnknown, parsedAny
}

// isPermanentBadRequest returns true for 400 responses whose body carries an
// OAuth error code that indicates the refresh token itself is dead. RFC 6749
// §5.2 uses 400 for these; any other 4xx/5xx we treat as transient.
func isPermanentBadRequest(status int, body []byte, reason RefreshFailureReason, parsed bool) bool {
	if status != http.StatusBadRequest || !parsed {
		return false
	}
	if reason != RefreshFailureUnknown {
		return true
	}
	var body2 struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(body, &body2)
	return strings.EqualFold(strings.TrimSpace(body2.Error), "invalid_grant")
}
