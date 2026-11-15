// Package grouptoken derives and verifies the capability tokens that bind a
// credential-proxy request to the group it claims to act for.
//
// It is a leaf package (no internal imports) so both the proxy, the sandbox
// controller and the config validator can use it without an import cycle.
package grouptoken

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
)

const (
	// Version prefixes the MAC input. Bumping it invalidates every previously
	// issued token: rotating the shared secret revokes all of them at once.
	Version = "kraclaw-credproxy-group-token-v1"

	// MinSecretLen is the shortest shared secret accepted. The secret is the
	// only thing between a sandbox and another tenant's credential, so a short
	// or guessable value defeats the check.
	MinSecretLen = 32

	// HeaderGroup names the tenant a request is charged to. Only trusted once
	// the matching token from HeaderGroupToken has been verified.
	HeaderGroup = "X-Kraclaw-Group"
	// HeaderProvider optionally selects which provider credential to use.
	HeaderProvider = "X-Kraclaw-Provider"
	// HeaderGroupToken carries the group's capability token.
	HeaderGroupToken = "X-Kraclaw-Group-Token"
)

// Token derives the capability token for groupJID.
//
// It is a deterministic MAC, so the proxy can verify a caller without shared
// state (no token table, no database migration) and the sandbox controller can
// mint a token for the group it is creating without access to the credential
// store. A sandbox therefore only ever learns its own group's token; naming
// another group in HeaderGroup no longer selects that group's credential.
func Token(secret, groupJID string) string {
	if secret == "" || groupJID == "" {
		return ""
	}

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(Version))
	mac.Write([]byte{0}) // separator: group IDs cannot contain NUL
	mac.Write([]byte(groupJID))

	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// Verify reports whether token is the capability token for groupJID. The
// comparison is constant time, so a caller cannot recover the expected MAC byte
// by byte from response timing.
func Verify(secret, groupJID, token string) bool {
	want := Token(secret, groupJID)
	if want == "" || token == "" {
		return false
	}

	return hmac.Equal([]byte(want), []byte(token))
}
