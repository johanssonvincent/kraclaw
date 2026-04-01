package credproxy

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func testEncryptor(t *testing.T) *Encryptor {
	t.Helper()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	enc, err := NewEncryptor(hex.EncodeToString(key))
	if err != nil {
		t.Fatal(err)
	}
	return enc
}

func TestCredentialStore_UpsertAndGet(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	enc := testEncryptor(t)
	store, err := NewCredentialStore(db, enc)
	if err != nil {
		t.Fatal(err)
	}

	cred := &Credential{
		GroupJID: "discord:123",
		Provider: "openai",
		APIKey:   "sk-test-key",
	}

	mock.ExpectExec("REPLACE INTO credentials").
		WithArgs("discord:123", "openai", sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))

	if err := store.UpsertCredential(context.Background(), cred); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestCredentialStore_Delete(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	enc := testEncryptor(t)
	store, err := NewCredentialStore(db, enc)
	if err != nil {
		t.Fatal(err)
	}

	mock.ExpectExec("DELETE FROM credentials WHERE group_jid").
		WithArgs("discord:123").
		WillReturnResult(sqlmock.NewResult(0, 1))

	if err := store.DeleteCredential(context.Background(), "discord:123"); err != nil {
		t.Fatalf("delete: %v", err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func newTestEncryptor(t *testing.T) *Encryptor {
	t.Helper()
	enc, err := NewEncryptor(strings.Repeat("ab", 32))
	if err != nil {
		t.Fatal(err)
	}
	return enc
}

func TestCredential_Validate(t *testing.T) {
	tests := []struct {
		name    string
		cred    Credential
		wantErr bool
	}{
		{"valid with API key", Credential{GroupJID: "discord:123", Provider: "openai", APIKey: "sk-test"}, false},
		{"valid with OAuth token", Credential{GroupJID: "discord:123", Provider: "anthropic", OAuthToken: "oauth-test"}, false},
		{"empty group JID", Credential{GroupJID: "", Provider: "openai", APIKey: "sk-test"}, true},
		{"empty provider", Credential{GroupJID: "discord:123", Provider: "", APIKey: "sk-test"}, true},
		{"no credentials", Credential{GroupJID: "discord:123", Provider: "openai"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cred.Validate()
			if (err != nil) != tt.wantErr {
				t.Fatalf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestUpsertCredential_RejectsEmptyAPIKey(t *testing.T) {
	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	enc := newTestEncryptor(t)
	store, err := NewCredentialStore(db, enc)
	if err != nil {
		t.Fatal(err)
	}

	err = store.UpsertCredential(context.Background(), &Credential{
		GroupJID: "discord:123",
		Provider: "openai",
	})
	if err == nil {
		t.Fatal("expected error for credential with no API key or OAuth token")
	}
}

func TestGetCredential_Found(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	enc := newTestEncryptor(t)
	store, err := NewCredentialStore(db, enc)
	if err != nil {
		t.Fatal(err)
	}

	encKey, _ := enc.Encrypt("sk-test-key")
	encToken, _ := enc.Encrypt("oauth-test")
	rows := sqlmock.NewRows([]string{"provider", "api_key_encrypted", "oauth_token_encrypted"}).
		AddRow("openai", encKey, encToken)
	mock.ExpectQuery("SELECT provider, api_key_encrypted, oauth_token_encrypted FROM credentials WHERE group_jid").
		WithArgs("discord:123").
		WillReturnRows(rows)

	cred, err := store.GetCredential(context.Background(), "discord:123")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cred == nil {
		t.Fatal("expected non-nil credential")
	}
	if cred.Provider != "openai" {
		t.Fatalf("expected provider openai, got %q", cred.Provider)
	}
	if cred.APIKey != "sk-test-key" {
		t.Fatalf("expected API key sk-test-key, got %q", cred.APIKey)
	}
	if cred.OAuthToken != "oauth-test" {
		t.Fatalf("expected OAuth token oauth-test, got %q", cred.OAuthToken)
	}
}

func TestGetCredential_NotFound(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	enc := newTestEncryptor(t)
	store, err := NewCredentialStore(db, enc)
	if err != nil {
		t.Fatal(err)
	}

	mock.ExpectQuery("SELECT provider, api_key_encrypted, oauth_token_encrypted FROM credentials WHERE group_jid").
		WithArgs("unknown:123").
		WillReturnError(sql.ErrNoRows)

	cred, err := store.GetCredential(context.Background(), "unknown:123")
	if err != nil {
		t.Fatalf("expected nil error for not found, got: %v", err)
	}
	if cred != nil {
		t.Fatal("expected nil credential for not found")
	}
}

func TestGetCredential_NullOAuthToken(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	enc := newTestEncryptor(t)
	store, err := NewCredentialStore(db, enc)
	if err != nil {
		t.Fatal(err)
	}

	encKey, _ := enc.Encrypt("sk-test-key")
	rows := sqlmock.NewRows([]string{"provider", "api_key_encrypted", "oauth_token_encrypted"}).
		AddRow("openai", encKey, nil)
	mock.ExpectQuery("SELECT provider, api_key_encrypted, oauth_token_encrypted FROM credentials WHERE group_jid").
		WithArgs("discord:456").
		WillReturnRows(rows)

	cred, err := store.GetCredential(context.Background(), "discord:456")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cred == nil {
		t.Fatal("expected non-nil credential")
	}
	if cred.OAuthToken != "" {
		t.Fatalf("expected empty OAuth token for NULL column, got %q", cred.OAuthToken)
	}
}
