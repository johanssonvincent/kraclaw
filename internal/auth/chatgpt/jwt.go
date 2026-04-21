package chatgpt

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// IDTokenClaims is the kraclaw-relevant subset of the ChatGPT id_token JWT.
type IDTokenClaims struct {
	Email     string
	AccountID string
	UserID    string
	PlanType  string
	IsFedRAMP bool
	ExpiresAt time.Time
}

// rawClaims mirrors the Codex IdClaims/StandardJwtClaims struct shape so that a
// single json.Unmarshal pass extracts every field we care about.
type rawClaims struct {
	Email   string             `json:"email,omitempty"`
	Exp     int64              `json:"exp,omitempty"`
	Profile *profileClaimsJSON `json:"https://api.openai.com/profile,omitempty"`
	Auth    *authClaimsJSON    `json:"https://api.openai.com/auth,omitempty"`
}

type profileClaimsJSON struct {
	Email string `json:"email,omitempty"`
}

type authClaimsJSON struct {
	ChatGPTPlanType         string `json:"chatgpt_plan_type,omitempty"`
	ChatGPTUserID           string `json:"chatgpt_user_id,omitempty"`
	UserID                  string `json:"user_id,omitempty"`
	ChatGPTAccountID        string `json:"chatgpt_account_id,omitempty"`
	ChatGPTAccountIsFedRAMP bool   `json:"chatgpt_account_is_fedramp,omitempty"`
}

// ParseIDToken decodes the payload of a JWT id_token and extracts the claims
// kraclaw needs. It does NOT verify the JWT signature — verification is the
// responsibility of the issuer; this code trusts what auth.openai.com hands
// back over TLS.
func ParseIDToken(token string) (IDTokenClaims, error) {
	if token == "" {
		return IDTokenClaims{}, fmt.Errorf("chatgpt: id_token is empty")
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return IDTokenClaims{}, fmt.Errorf("chatgpt: id_token is not a 3-part JWT (got %d parts)", len(parts))
	}
	payload, err := decodeJWTSegment(parts[1])
	if err != nil {
		return IDTokenClaims{}, fmt.Errorf("chatgpt: decode id_token payload: %w", err)
	}

	var raw rawClaims
	if err := json.Unmarshal(payload, &raw); err != nil {
		return IDTokenClaims{}, fmt.Errorf("chatgpt: parse id_token claims: %w", err)
	}

	claims := IDTokenClaims{
		Email: raw.Email,
	}
	if claims.Email == "" && raw.Profile != nil {
		claims.Email = raw.Profile.Email
	}
	if raw.Auth != nil {
		claims.AccountID = raw.Auth.ChatGPTAccountID
		claims.UserID = raw.Auth.ChatGPTUserID
		if claims.UserID == "" {
			claims.UserID = raw.Auth.UserID
		}
		claims.PlanType = raw.Auth.ChatGPTPlanType
		claims.IsFedRAMP = raw.Auth.ChatGPTAccountIsFedRAMP
	}
	if raw.Exp > 0 {
		claims.ExpiresAt = time.Unix(raw.Exp, 0).UTC()
	}
	return claims, nil
}

// decodeJWTSegment decodes a base64url JWT segment, accepting both padded and
// unpadded variants since real-world JWTs do both.
func decodeJWTSegment(seg string) ([]byte, error) {
	if pad := len(seg) % 4; pad != 0 {
		seg += strings.Repeat("=", 4-pad)
	}
	if b, err := base64.URLEncoding.DecodeString(seg); err == nil {
		return b, nil
	}
	return base64.StdEncoding.DecodeString(seg)
}
