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

func TestParseIDToken_AllClaims(t *testing.T) {
	exp := time.Now().Add(time.Hour).Unix()
	jwt := mintJWT(t, map[string]any{
		"email": "user@example.com",
		"exp":   exp,
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_plan_type":          "plus",
			"chatgpt_user_id":            "user_42",
			"chatgpt_account_id":         "acct_99",
			"chatgpt_account_is_fedramp": true,
		},
	})

	claims, err := ParseIDToken(jwt)
	if err != nil {
		t.Fatalf("ParseIDToken: %v", err)
	}
	if claims.Email != "user@example.com" {
		t.Errorf("Email = %q, want user@example.com", claims.Email)
	}
	if claims.AccountID != "acct_99" {
		t.Errorf("AccountID = %q, want acct_99", claims.AccountID)
	}
	if claims.UserID != "user_42" {
		t.Errorf("UserID = %q, want user_42", claims.UserID)
	}
	if claims.PlanType != "plus" {
		t.Errorf("PlanType = %q, want plus", claims.PlanType)
	}
	if !claims.IsFedRAMP {
		t.Error("IsFedRAMP = false, want true")
	}
	if claims.ExpiresAt.Unix() != exp {
		t.Errorf("ExpiresAt unix = %d, want %d", claims.ExpiresAt.Unix(), exp)
	}
}

func TestParseIDToken_ProfileEmailFallback(t *testing.T) {
	jwt := mintJWT(t, map[string]any{
		"https://api.openai.com/profile": map[string]any{"email": "fallback@example.com"},
		"https://api.openai.com/auth":    map[string]any{"chatgpt_account_id": "acct"},
	})
	claims, err := ParseIDToken(jwt)
	if err != nil {
		t.Fatalf("ParseIDToken: %v", err)
	}
	if claims.Email != "fallback@example.com" {
		t.Errorf("expected profile.email fallback, got %q", claims.Email)
	}
}

func TestParseIDToken_UserIDFallback(t *testing.T) {
	jwt := mintJWT(t, map[string]any{
		"https://api.openai.com/auth": map[string]any{
			"user_id":            "legacy_user_42",
			"chatgpt_account_id": "acct",
		},
	})
	claims, err := ParseIDToken(jwt)
	if err != nil {
		t.Fatalf("ParseIDToken: %v", err)
	}
	if claims.UserID != "legacy_user_42" {
		t.Errorf("expected user_id fallback, got %q", claims.UserID)
	}
}

func TestParseIDToken_ChatGPTUserIDPreferredOverLegacy(t *testing.T) {
	jwt := mintJWT(t, map[string]any{
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_user_id":    "modern",
			"user_id":            "legacy",
			"chatgpt_account_id": "acct",
		},
	})
	claims, err := ParseIDToken(jwt)
	if err != nil {
		t.Fatalf("ParseIDToken: %v", err)
	}
	if claims.UserID != "modern" {
		t.Errorf("expected chatgpt_user_id to win, got %q", claims.UserID)
	}
}

func TestParseIDToken_FedRAMPDefaultsFalse(t *testing.T) {
	jwt := mintJWT(t, map[string]any{
		"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "acct"},
	})
	claims, err := ParseIDToken(jwt)
	if err != nil {
		t.Fatalf("ParseIDToken: %v", err)
	}
	if claims.IsFedRAMP {
		t.Error("expected IsFedRAMP false when claim absent")
	}
}

func TestParseIDToken_NoExp(t *testing.T) {
	jwt := mintJWT(t, map[string]any{
		"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "acct"},
	})
	claims, err := ParseIDToken(jwt)
	if err != nil {
		t.Fatalf("ParseIDToken: %v", err)
	}
	if !claims.ExpiresAt.IsZero() {
		t.Errorf("ExpiresAt should be zero when exp absent, got %v", claims.ExpiresAt)
	}
}

func TestParseIDToken_PaddedBase64(t *testing.T) {
	// Build a JWT where the payload base64 length is divisible by 4 with
	// padding (the parser must accept both URL-safe with and without padding).
	header := base64.URLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	payload := base64.URLEncoding.EncodeToString([]byte(`{"https://api.openai.com/auth":{"chatgpt_account_id":"acct"}}`))
	jwt := header + "." + payload + ".sig"
	if _, err := ParseIDToken(jwt); err != nil {
		t.Fatalf("ParseIDToken with padded base64: %v", err)
	}
}

func TestParseIDToken_Errors(t *testing.T) {
	tests := []struct {
		name  string
		token string
	}{
		{"empty", ""},
		{"two segments", "aGVsbG8.aGVsbG8"},
		{"bad base64", "aGVsbG8.@@@@.sig"},
		{"non-json payload", "aGVsbG8." + base64.RawURLEncoding.EncodeToString([]byte("not json")) + ".sig"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := ParseIDToken(tt.token); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestParseIDToken_RealWorldShape(t *testing.T) {
	// Fixture mirrors the structure of an actual ChatGPT id_token payload to
	// guard against JSON tag drift.
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
	jwt := enc.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`)) + "." + enc.EncodeToString([]byte(payload)) + ".sig"

	claims, err := ParseIDToken(jwt)
	if err != nil {
		t.Fatalf("ParseIDToken: %v", err)
	}
	if claims.PlanType != "pro" || claims.AccountID != "acct_main" || claims.UserID != "user_42" {
		t.Fatalf("claims = %+v", claims)
	}
	if claims.ExpiresAt.Unix() != 1700003600 {
		t.Errorf("ExpiresAt unix = %d, want 1700003600", claims.ExpiresAt.Unix())
	}
}
