package chatgpt

import (
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"
)

// mintJWT builds an unsigned JWT carrying the given payload. The header is a
// fixed RS256 stub and the signature segment is a single character — the
// parser only needs the segment count and the payload to be base64-decodable
// JSON.
func mintJWT(t *testing.T, payload map[string]any) string {
	t.Helper()
	header := map[string]string{"alg": "RS256", "typ": "JWT"}
	headerJSON, err := json.Marshal(header)
	if err != nil {
		t.Fatal(err)
	}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	enc := base64.RawURLEncoding
	return enc.EncodeToString(headerJSON) + "." + enc.EncodeToString(payloadJSON) + ".sig"
}

func TestParseIDToken(t *testing.T) {
	expAllClaims := time.Now().Add(time.Hour).Unix()

	tests := map[string]struct {
		// build returns the JWT string. Exactly one of build or payload is set.
		build func(t *testing.T) string
		// payload is the claims map passed to mintJWT when build is nil.
		payload map[string]any
		// check asserts on the parsed claims. err must be nil.
		check func(t *testing.T, c *IDTokenClaims)
	}{
		"all claims populated": {
			payload: map[string]any{
				"email": "user@example.com",
				"exp":   expAllClaims,
				"https://api.openai.com/auth": map[string]any{
					"chatgpt_plan_type":          "plus",
					"chatgpt_user_id":            "user_42",
					"chatgpt_account_id":         "acct_99",
					"chatgpt_account_is_fedramp": true,
				},
			},
			check: func(t *testing.T, c *IDTokenClaims) {
				if c.Email != "user@example.com" {
					t.Errorf("Email = %q, want user@example.com", c.Email)
				}
				if c.AccountID != "acct_99" {
					t.Errorf("AccountID = %q, want acct_99", c.AccountID)
				}
				if c.UserID != "user_42" {
					t.Errorf("UserID = %q, want user_42", c.UserID)
				}
				if c.PlanType != "plus" {
					t.Errorf("PlanType = %q, want plus", c.PlanType)
				}
				if !c.IsFedRAMP {
					t.Error("IsFedRAMP = false, want true")
				}
				if c.ExpiresAt.Unix() != expAllClaims {
					t.Errorf("ExpiresAt unix = %d, want %d", c.ExpiresAt.Unix(), expAllClaims)
				}
			},
		},
		"profile.email fallback when top-level email missing": {
			payload: map[string]any{
				"https://api.openai.com/profile": map[string]any{"email": "fallback@example.com"},
				"https://api.openai.com/auth":    map[string]any{"chatgpt_account_id": "acct"},
			},
			check: func(t *testing.T, c *IDTokenClaims) {
				if c.Email != "fallback@example.com" {
					t.Errorf("Email = %q, want fallback@example.com", c.Email)
				}
			},
		},
		"legacy user_id used when chatgpt_user_id absent": {
			payload: map[string]any{
				"https://api.openai.com/auth": map[string]any{
					"user_id":            "legacy_user_42",
					"chatgpt_account_id": "acct",
				},
			},
			check: func(t *testing.T, c *IDTokenClaims) {
				if c.UserID != "legacy_user_42" {
					t.Errorf("UserID = %q, want legacy_user_42", c.UserID)
				}
			},
		},
		"chatgpt_user_id wins over legacy user_id": {
			payload: map[string]any{
				"https://api.openai.com/auth": map[string]any{
					"chatgpt_user_id":    "modern",
					"user_id":            "legacy",
					"chatgpt_account_id": "acct",
				},
			},
			check: func(t *testing.T, c *IDTokenClaims) {
				if c.UserID != "modern" {
					t.Errorf("UserID = %q, want modern", c.UserID)
				}
			},
		},
		"IsFedRAMP defaults to false when claim absent": {
			payload: map[string]any{
				"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "acct"},
			},
			check: func(t *testing.T, c *IDTokenClaims) {
				if c.IsFedRAMP {
					t.Error("IsFedRAMP = true, want false when claim absent")
				}
			},
		},
		"ExpiresAt is zero when exp absent": {
			payload: map[string]any{
				"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "acct"},
			},
			check: func(t *testing.T, c *IDTokenClaims) {
				if !c.ExpiresAt.IsZero() {
					t.Errorf("ExpiresAt = %v, want zero", c.ExpiresAt)
				}
			},
		},
		"padded base64 segments parse": {
			build: func(t *testing.T) string {
				header := base64.URLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
				payload := base64.URLEncoding.EncodeToString([]byte(`{"https://api.openai.com/auth":{"chatgpt_account_id":"acct"}}`))
				return header + "." + payload + ".sig"
			},
			check: func(t *testing.T, c *IDTokenClaims) {
				if c.AccountID != "acct" {
					t.Errorf("AccountID = %q, want acct", c.AccountID)
				}
			},
		},
		"real-world id_token shape": {
			build: func(t *testing.T) string {
				payload := `{
					"iss": "https://auth.openai.com",
					"sub": "user_42",
					"aud": "app_EMoamEEZ73f0CkXaXp7hrann",
					"iat": 1700000000,
					"exp": 1700003600,
					"email": "vince@example.com",
					"https://api.openai.com/profile": {"email": "vince@example.com"},
					"https://api.openai.com/auth": {
						"chatgpt_plan_type": "pro",
						"chatgpt_user_id": "user_42",
						"chatgpt_account_id": "acct_main",
						"chatgpt_account_is_fedramp": false
					}
				}`
				enc := base64.RawURLEncoding
				return enc.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`)) + "." + enc.EncodeToString([]byte(payload)) + ".sig"
			},
			check: func(t *testing.T, c *IDTokenClaims) {
				if c.PlanType != "pro" || c.AccountID != "acct_main" || c.UserID != "user_42" {
					t.Errorf("claims = %+v", c)
				}
				if c.ExpiresAt.Unix() != 1700003600 {
					t.Errorf("ExpiresAt unix = %d, want 1700003600", c.ExpiresAt.Unix())
				}
			},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			var jwt string
			switch {
			case tc.build != nil:
				jwt = tc.build(t)
			default:
				jwt = mintJWT(t, tc.payload)
			}
			claims, err := ParseIDToken(jwt)
			if err != nil {
				t.Fatalf("ParseIDToken: %v", err)
			}
			tc.check(t, &claims)
		})
	}
}

func TestParseIDToken_Errors(t *testing.T) {
	tests := map[string]struct {
		token string
	}{
		"empty":            {token: ""},
		"two segments":     {token: "aGVsbG8.aGVsbG8"},
		"bad base64":       {token: "aGVsbG8.@@@@.sig"},
		"non-json payload": {token: "aGVsbG8." + base64.RawURLEncoding.EncodeToString([]byte("not json")) + ".sig"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseIDToken(tc.token); err == nil {
				t.Fatalf("ParseIDToken(%q) = nil error, want error", tc.token)
			}
		})
	}
}
