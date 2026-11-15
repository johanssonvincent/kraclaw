package grouptoken

import (
	"strings"
	"testing"
)

const testSecret = "0123456789abcdef0123456789abcdef"

func TestToken_Deterministic(t *testing.T) {
	first := Token(testSecret, "discord:123")
	second := Token(testSecret, "discord:123")

	if first == "" {
		t.Fatal("expected a token")
	}

	if first != second {
		t.Fatalf("Token() is not deterministic: %q != %q", first, second)
	}
}

func TestToken_DistinctPerGroup(t *testing.T) {
	a := Token(testSecret, "discord:123")
	b := Token(testSecret, "telegram:456")

	if a == b {
		t.Fatal("different groups must not share a token")
	}
}

func TestToken_EmptyInputs(t *testing.T) {
	tests := []struct {
		name     string
		secret   string
		groupJID string
	}{
		{name: "no secret", secret: "", groupJID: "discord:123"},
		{name: "no group", secret: testSecret, groupJID: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Token(tt.secret, tt.groupJID); got != "" {
				t.Fatalf("Token() = %q, want empty", got)
			}
		})
	}
}

func TestVerify(t *testing.T) {
	tests := []struct {
		name     string
		secret   string
		groupJID string
		token    string
		want     bool
	}{
		{
			name:     "valid token for own group",
			secret:   testSecret,
			groupJID: "discord:123",
			token:    Token(testSecret, "discord:123"),
			want:     true,
		},
		{
			name:     "token minted for another group",
			secret:   testSecret,
			groupJID: "discord:123",
			token:    Token(testSecret, "telegram:456"),
			want:     false,
		},
		{
			name:     "token minted with another secret",
			secret:   testSecret,
			groupJID: "discord:123",
			token:    Token("ffffffffffffffffffffffffffffffff", "discord:123"),
			want:     false,
		},
		{
			name:     "empty token",
			secret:   testSecret,
			groupJID: "discord:123",
			token:    "",
			want:     false,
		},
		{
			name:     "empty secret",
			secret:   "",
			groupJID: "discord:123",
			token:    Token(testSecret, "discord:123"),
			want:     false,
		},
		{
			name:     "truncated token",
			secret:   testSecret,
			groupJID: "discord:123",
			token:    Token(testSecret, "discord:123")[:len(Token(testSecret, "discord:123"))-1],
			want:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Verify(tt.secret, tt.groupJID, tt.token); got != tt.want {
				t.Fatalf("Verify() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestToken_NoGroupSubstringAmbiguity(t *testing.T) {
	// The NUL separator keeps the secret and the group from being concatenated
	// ambiguously: ("ab","c") must not produce the same MAC as ("a","bc").
	if Token("ab", "c") == Token("a", "bc") {
		t.Fatal("token input is ambiguous across the secret/group boundary")
	}

	if strings.Contains(Token(testSecret, "discord:123"), "discord:123") {
		t.Fatal("token must not embed the group ID")
	}
}
