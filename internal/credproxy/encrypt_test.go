package credproxy

import (
	"strings"
	"testing"
)

func TestEncryptDecrypt_RoundTrip(t *testing.T) {
	cases := []struct {
		name               string
		plaintext          string
		wantDistinctCipher bool
	}{
		{
			name:               "round_trip",
			plaintext:          "«redacted:sk-…»",
			wantDistinctCipher: true,
		},
		{
			name:      "empty_string",
			plaintext: "",
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			enc := newTestEncryptor(t)

			ciphertext, err := enc.Encrypt(tt.plaintext)
			if err != nil {
				t.Fatalf("encrypt: %v", err)
			}

			if ciphertext == tt.plaintext {
				t.Fatal("ciphertext should differ from plaintext")
			}

			if tt.wantDistinctCipher {
				c2, err := enc.Encrypt(tt.plaintext)
				if err != nil {
					t.Fatalf("re-encrypt: %v", err)
				}

				if c2 == ciphertext {
					t.Error("encrypting same input should produce different ciphertext (random nonce)")
				}
			}

			decrypted, err := enc.Decrypt(ciphertext)
			if err != nil {
				t.Fatalf("decrypt: %v", err)
			}

			if decrypted != tt.plaintext {
				t.Errorf("expected %q, got %q", tt.plaintext, decrypted)
			}
		})
	}
}

func TestNewEncryptor_InvalidKeyLength(t *testing.T) {
	_, err := NewEncryptor("tooshort")
	if err == nil {
		t.Fatal("expected error for short key")
	}
}

func TestDecrypt_Errors(t *testing.T) {
	cases := []struct {
		name string
		prep func(t *testing.T) (*Encryptor, string)
	}{
		{
			name: "tampered_ciphertext",
			prep: func(t *testing.T) (*Encryptor, string) {
				enc := newTestEncryptor(t)

				ciphertext, err := enc.Encrypt("secret")
				if err != nil {
					t.Fatalf("encrypt: %v", err)
				}

				return enc, ciphertext[:len(ciphertext)-2] + "xx"
			},
		},
		{
			name: "wrong_key",
			prep: func(t *testing.T) (*Encryptor, string) {
				enc := newTestEncryptor(t)

				ciphertext, err := enc.Encrypt("secret-api-key")
				if err != nil {
					t.Fatalf("encrypt: %v", err)
				}

				other, err := NewEncryptor(strings.Repeat("cd", 32))
				if err != nil {
					t.Fatalf("new encryptor: %v", err)
				}
				return other, ciphertext
			},
		},
		{
			name: "truncated_ciphertext",
			prep: func(t *testing.T) (*Encryptor, string) {
				// Base64 of just a few bytes — shorter than nonce size.
				return newTestEncryptor(t), "dG9vc2hvcnQ="
			},
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			enc, ciphertext := tt.prep(t)

			if _, err := enc.Decrypt(ciphertext); err == nil {
				t.Errorf("Decrypt(%q) err = nil, want error", ciphertext)
			}
		})
	}
}
