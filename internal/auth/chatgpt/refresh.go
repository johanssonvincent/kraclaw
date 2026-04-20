package chatgpt

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
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

// RefreshError describes a token-refresh failure. Permanent errors mean the
// caller must trigger a fresh device-flow sign-in; transient errors should be
// retried later.
type RefreshError struct {
	Permanent bool
	Reason    RefreshFailureReason
	Status    int
	Body      string
	cause     error
}

func (e *RefreshError) Error() string {
	if e.cause != nil {
		return fmt.Sprintf("chatgpt: refresh failed (status=%d, reason=%s): %v", e.Status, e.Reason, e.cause)
	}
	return fmt.Sprintf("chatgpt: refresh failed (status=%d, reason=%s): %s", e.Status, e.Reason, truncate(e.Body, 200))
}

func (e *RefreshError) Unwrap() error { return e.cause }

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
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, &RefreshError{Permanent: false, Reason: RefreshFailureUnknown, cause: err}
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, &RefreshError{Permanent: false, Status: resp.StatusCode, Reason: RefreshFailureUnknown, cause: err}
	}

	if resp.StatusCode == http.StatusUnauthorized {
		return nil, &RefreshError{
			Permanent: true,
			Status:    resp.StatusCode,
			Reason:    classifyRefreshFailure(respBody),
			Body:      string(respBody),
		}
	}
	if resp.StatusCode/100 != 2 {
		return nil, &RefreshError{
			Permanent: false,
			Status:    resp.StatusCode,
			Reason:    RefreshFailureUnknown,
			Body:      string(respBody),
		}
	}

	var parsed tokenResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return nil, &RefreshError{
			Permanent: false,
			Status:    resp.StatusCode,
			Reason:    RefreshFailureUnknown,
			cause:     fmt.Errorf("decode refresh response: %w", err),
		}
	}

	// Server may rotate refresh_token (and re-issue id_token) but is allowed to
	// omit either. Keep the previous values when fields are absent.
	if parsed.RefreshToken == "" {
		parsed.RefreshToken = refreshToken
	}
	if parsed.AccessToken == "" {
		return nil, &RefreshError{
			Permanent: false,
			Status:    resp.StatusCode,
			Reason:    RefreshFailureUnknown,
			cause:     fmt.Errorf("refresh response missing access_token"),
		}
	}
	tokens := &Tokens{
		AccessToken:  parsed.AccessToken,
		RefreshToken: parsed.RefreshToken,
		IDToken:      parsed.IDToken,
	}
	if parsed.IDToken != "" {
		claims, err := ParseIDToken(parsed.IDToken)
		if err != nil {
			return nil, &RefreshError{
				Permanent: false,
				Status:    resp.StatusCode,
				Reason:    RefreshFailureUnknown,
				cause:     fmt.Errorf("parse refreshed id_token: %w", err),
			}
		}
		tokens.IDClaims = claims
		tokens.ExpiresAt = claims.ExpiresAt
	}
	if tokens.ExpiresAt.IsZero() && parsed.ExpiresIn > 0 {
		tokens.ExpiresAt = c.now().Add(time.Second * time.Duration(parsed.ExpiresIn))
	}
	return tokens, nil
}

// classifyRefreshFailure inspects the JSON error body returned with a 401 and
// maps the OpenAI-specific error codes to a RefreshFailureReason.
func classifyRefreshFailure(body []byte) RefreshFailureReason {
	var parsed struct {
		Error            string `json:"error"`
		ErrorCode        string `json:"error_code"`
		ErrorDescription string `json:"error_description"`
	}
	_ = json.Unmarshal(body, &parsed)
	for _, candidate := range []string{parsed.ErrorCode, parsed.Error, parsed.ErrorDescription} {
		switch strings.ToLower(strings.TrimSpace(candidate)) {
		case "refresh_token_expired":
			return RefreshFailureExpired
		case "refresh_token_reused":
			return RefreshFailureReused
		case "refresh_token_invalidated":
			return RefreshFailureRevoked
		}
	}
	return RefreshFailureUnknown
}
