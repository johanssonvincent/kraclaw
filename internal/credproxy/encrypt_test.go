package credproxy

import (
	"crypto/rand"
	"encoding/hex"
	"testing"
)

func TestEncryptDecrypt_RoundTrip(t *testing.T) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	keyHex := hex.EncodeToString(key)

	enc, err := NewEncryptor(keyHex)
	if err != nil {
		t.Fatalf("new encryptor: %v", err)
	}

	plaintext := "sk-proj-abc123secretkey"
	ciphertext, err := enc.Encrypt(plaintext)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if ciphertext == plaintext {
		t.Fatal("ciphertext should differ from plaintext")
	}

	decrypted, err := enc.Decrypt(ciphertext)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if decrypted != plaintext {
		t.Fatalf("expected %q, got %q", plaintext, decrypted)
	}
}

func TestEncryptDecrypt_DifferentCiphertextEachTime(t *testing.T) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	enc, err := NewEncryptor(hex.EncodeToString(key))
	if err != nil {
		t.Fatal(err)
	}

	c1, _ := enc.Encrypt("same-input")
	c2, _ := enc.Encrypt("same-input")
	if c1 == c2 {
		t.Fatal("encrypting same input should produce different ciphertext (random nonce)")
	}
}

func TestNewEncryptor_InvalidKeyLength(t *testing.T) {
	_, err := NewEncryptor("tooshort")
	if err == nil {
		t.Fatal("expected error for short key")
	}
}

func TestDecrypt_TamperedCiphertext(t *testing.T) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	enc, err := NewEncryptor(hex.EncodeToString(key))
	if err != nil {
		t.Fatal(err)
	}

	ciphertext, _ := enc.Encrypt("secret")
	tampered := ciphertext[:len(ciphertext)-2] + "xx"
	_, err = enc.Decrypt(tampered)
	if err == nil {
		t.Fatal("expected error for tampered ciphertext")
	}
}

func TestDecrypt_WrongKey_ReturnsError(t *testing.T) {
	key1 := make([]byte, 32)
	key2 := make([]byte, 32)
	if _, err := rand.Read(key1); err != nil {
		t.Fatal(err)
	}
	if _, err := rand.Read(key2); err != nil {
		t.Fatal(err)
	}

	enc1, err := NewEncryptor(hex.EncodeToString(key1))
	if err != nil {
		t.Fatal(err)
	}
	enc2, err := NewEncryptor(hex.EncodeToString(key2))
	if err != nil {
		t.Fatal(err)
	}

	ciphertext, err := enc1.Encrypt("secret-api-key")
	if err != nil {
		t.Fatal(err)
	}

	_, err = enc2.Decrypt(ciphertext)
	if err == nil {
		t.Fatal("expected error when decrypting with wrong key")
	}
}

func TestEncryptDecrypt_EmptyString(t *testing.T) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	enc, err := NewEncryptor(hex.EncodeToString(key))
	if err != nil {
		t.Fatal(err)
	}

	ciphertext, err := enc.Encrypt("")
	if err != nil {
		t.Fatalf("encrypt empty string: %v", err)
	}
	decrypted, err := enc.Decrypt(ciphertext)
	if err != nil {
		t.Fatalf("decrypt empty string: %v", err)
	}
	if decrypted != "" {
		t.Fatalf("expected empty string, got %q", decrypted)
	}
}

func TestDecrypt_TruncatedCiphertext(t *testing.T) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	enc, err := NewEncryptor(hex.EncodeToString(key))
	if err != nil {
		t.Fatal(err)
	}

	// Base64 of just a few bytes — shorter than nonce size.
	_, err = enc.Decrypt("dG9vc2hvcnQ=")
	if err == nil {
		t.Fatal("expected error for truncated ciphertext")
	}
}
