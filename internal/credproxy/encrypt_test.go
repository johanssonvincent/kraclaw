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
