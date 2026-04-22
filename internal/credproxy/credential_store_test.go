package credproxy

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"

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

func newTestEncryptor(t *testing.T) *Encryptor {
	t.Helper()
	enc, err := NewEncryptor(strings.Repeat("ab", 32))
	if err != nil {
		t.Fatal(err)
	}
	return enc
}


func TestCredentialStore_UpsertAndGet_APIKey(t *testing.T) {
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
		WithArgs("discord:123", "openai", string(AuthModeAPIKey), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))

	if err := store.UpsertCredential(context.Background(), cred); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if cred.AuthMode != AuthModeAPIKey {
		t.Fatalf("expected AuthMode auto-populated to api_key, got %q", cred.AuthMode)
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

func TestCredential_Validate(t *testing.T) {
	validTokens := func() *ChatGPTTokens {
		return &ChatGPTTokens{
			AccessToken:  "access-1",
			RefreshToken: "refresh-1",
			AccountID:    "acct-1",
			ExpiresAt:    time.Now().Add(time.Hour),
		}
	}
	tests := []struct {
		name    string
		cred    Credential
		wantErr bool
	}{
		{"api_key valid", Credential{GroupJID: "discord:123", Provider: "openai", APIKey: "sk-test"}, false},
		{"api_key explicit valid", Credential{GroupJID: "discord:123", Provider: "openai", AuthMode: AuthModeAPIKey, APIKey: "sk-test"}, false},
		{"empty group JID", Credential{GroupJID: "", Provider: "openai", APIKey: "sk-test"}, true},
		{"empty provider", Credential{GroupJID: "discord:123", Provider: "", APIKey: "sk-test"}, true},
		{"api_key missing key", Credential{GroupJID: "discord:123", Provider: "openai"}, true},
		{"api_key with chatgpt tokens set", Credential{GroupJID: "discord:123", Provider: "openai", APIKey: "sk-test", ChatGPT: validTokens()}, true},
		{"chatgpt valid", Credential{GroupJID: "discord:123", Provider: "openai", AuthMode: AuthModeChatGPT, ChatGPT: validTokens()}, false},
		{"chatgpt missing tokens", Credential{GroupJID: "discord:123", Provider: "openai", AuthMode: AuthModeChatGPT}, true},
		{"chatgpt with api key", Credential{GroupJID: "discord:123", Provider: "openai", AuthMode: AuthModeChatGPT, APIKey: "sk-test", ChatGPT: validTokens()}, true},
		{"chatgpt missing access token", Credential{GroupJID: "discord:123", Provider: "openai", AuthMode: AuthModeChatGPT, ChatGPT: &ChatGPTTokens{RefreshToken: "r", AccountID: "a", ExpiresAt: time.Now().Add(time.Hour)}}, true},
		{"chatgpt missing refresh token", Credential{GroupJID: "discord:123", Provider: "openai", AuthMode: AuthModeChatGPT, ChatGPT: &ChatGPTTokens{AccessToken: "a", AccountID: "a", ExpiresAt: time.Now().Add(time.Hour)}}, true},
		{"chatgpt missing account id", Credential{GroupJID: "discord:123", Provider: "openai", AuthMode: AuthModeChatGPT, ChatGPT: &ChatGPTTokens{AccessToken: "a", RefreshToken: "r", ExpiresAt: time.Now().Add(time.Hour)}}, true},
		{"chatgpt zero expiry", Credential{GroupJID: "discord:123", Provider: "openai", AuthMode: AuthModeChatGPT, ChatGPT: &ChatGPTTokens{AccessToken: "a", RefreshToken: "r", AccountID: "a"}}, true},
		{"unknown auth mode", Credential{GroupJID: "discord:123", Provider: "openai", AuthMode: AuthMode("magic"), APIKey: "sk-test"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.cred.Validate(); (err != nil) != tt.wantErr {
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

	if err := store.UpsertCredential(context.Background(), &Credential{
		GroupJID: "discord:123",
		Provider: "openai",
	}); err == nil {
		t.Fatal("expected error for credential with no API key")
	}
}

func TestGetCredential_Found_APIKey(t *testing.T) {
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
	rows := sqlmock.NewRows([]string{"provider", "auth_mode", "api_key_encrypted", "oauth_access_token_encrypted", "oauth_refresh_token_encrypted", "oauth_id_token_encrypted", "oauth_account_id", "oauth_expires_at", "oauth_is_fedramp"}).
		AddRow("openai", string(AuthModeAPIKey), encKey, nil, nil, nil, nil, nil, false)
	mock.ExpectQuery("SELECT").
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
	if cred.AuthMode != AuthModeAPIKey {
		t.Fatalf("expected AuthMode api_key, got %q", cred.AuthMode)
	}
	if cred.APIKey != "sk-test-key" {
		t.Fatalf("expected API key sk-test-key, got %q", cred.APIKey)
	}
	if cred.ChatGPT != nil {
		t.Fatal("expected nil ChatGPT tokens for api_key auth mode")
	}
}

func TestGetCredential_LegacyEmptyAuthMode(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	enc := newTestEncryptor(t)
	store, _ := NewCredentialStore(db, enc)
	encKey, _ := enc.Encrypt("sk-legacy")
	rows := sqlmock.NewRows([]string{"provider", "auth_mode", "api_key_encrypted", "oauth_access_token_encrypted", "oauth_refresh_token_encrypted", "oauth_id_token_encrypted", "oauth_account_id", "oauth_expires_at", "oauth_is_fedramp"}).
		AddRow("openai", "", encKey, nil, nil, nil, nil, nil, false)
	mock.ExpectQuery("SELECT").WithArgs("discord:legacy").WillReturnRows(rows)

	cred, err := store.GetCredential(context.Background(), "discord:legacy")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cred.AuthMode != AuthModeAPIKey {
		t.Fatalf("expected legacy empty auth_mode to map to api_key, got %q", cred.AuthMode)
	}
	if cred.APIKey != "sk-legacy" {
		t.Fatalf("unexpected api key: %q", cred.APIKey)
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

	mock.ExpectQuery("SELECT").
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

func TestGetCredential_Anthropic(t *testing.T) {
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

	encKey, _ := enc.Encrypt("sk-ant-test-key")
	rows := sqlmock.NewRows([]string{"provider", "auth_mode", "api_key_encrypted", "oauth_access_token_encrypted", "oauth_refresh_token_encrypted", "oauth_id_token_encrypted", "oauth_account_id", "oauth_expires_at", "oauth_is_fedramp"}).
		AddRow("anthropic", string(AuthModeAPIKey), encKey, nil, nil, nil, nil, nil, false)
	mock.ExpectQuery("SELECT").
		WithArgs("discord:456").
		WillReturnRows(rows)

	cred, err := store.GetCredential(context.Background(), "discord:456")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cred == nil {
		t.Fatal("expected non-nil credential")
	}
	if cred.Provider != "anthropic" {
		t.Fatalf("expected anthropic provider, got %q", cred.Provider)
	}
	if cred.APIKey != "sk-ant-test-key" {
		t.Fatalf("expected API key sk-ant-test-key, got %q", cred.APIKey)
	}
}

func TestUpsertChatGPTCredential_AndGet(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	enc := newTestEncryptor(t)
	store, _ := NewCredentialStore(db, enc)

	expiresAt := time.Now().Add(45 * time.Minute).UTC().Truncate(time.Second)
	tokens := &ChatGPTTokens{
		AccessToken:  "access-token-XYZ",
		RefreshToken: "refresh-token-ABC",
		IDToken:      "id-token-PQR",
		AccountID:    "acct_42",
		ExpiresAt:    expiresAt,
		IsFedRAMP:    true,
	}

	mock.ExpectExec("REPLACE INTO credentials").
		WithArgs(
			"discord:42",
			"openai",
			string(AuthModeChatGPT),
			sqlmock.AnyArg(), // access enc
			sqlmock.AnyArg(), // refresh enc
			sqlmock.AnyArg(), // id enc (NullString)
			"acct_42",
			expiresAt,
			true,
		).
		WillReturnResult(sqlmock.NewResult(0, 1))

	if err := store.UpsertChatGPTCredential(context.Background(), "discord:42", "openai", tokens); err != nil {
		t.Fatalf("upsert chatgpt: %v", err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}

	accessEnc, _ := enc.Encrypt("access-token-XYZ")
	refreshEnc, _ := enc.Encrypt("refresh-token-ABC")
	idEnc, _ := enc.Encrypt("id-token-PQR")
	rows := sqlmock.NewRows([]string{"provider", "auth_mode", "api_key_encrypted", "oauth_access_token_encrypted", "oauth_refresh_token_encrypted", "oauth_id_token_encrypted", "oauth_account_id", "oauth_expires_at", "oauth_is_fedramp"}).
		AddRow("openai", string(AuthModeChatGPT), nil, accessEnc, refreshEnc, idEnc, "acct_42", expiresAt, true)
	mock.ExpectQuery("SELECT").
		WithArgs("discord:42").
		WillReturnRows(rows)

	got, err := store.GetCredential(context.Background(), "discord:42")
	if err != nil {
		t.Fatalf("get chatgpt: %v", err)
	}
	if got == nil {
		t.Fatal("expected non-nil chatgpt credential")
	}
	if got.AuthMode != AuthModeChatGPT {
		t.Fatalf("AuthMode = %q, want chatgpt", got.AuthMode)
	}
	if got.APIKey != "" {
		t.Fatalf("APIKey should be empty in chatgpt mode, got %q", got.APIKey)
	}
	if got.ChatGPT == nil {
		t.Fatal("ChatGPT tokens missing")
	}
	if got.ChatGPT.AccessToken != "access-token-XYZ" {
		t.Fatalf("access token roundtrip mismatch: %q", got.ChatGPT.AccessToken)
	}
	if got.ChatGPT.RefreshToken != "refresh-token-ABC" {
		t.Fatalf("refresh token roundtrip mismatch: %q", got.ChatGPT.RefreshToken)
	}
	if got.ChatGPT.IDToken != "id-token-PQR" {
		t.Fatalf("id token roundtrip mismatch: %q", got.ChatGPT.IDToken)
	}
	if got.ChatGPT.AccountID != "acct_42" {
		t.Fatalf("account id mismatch: %q", got.ChatGPT.AccountID)
	}
	if !got.ChatGPT.IsFedRAMP {
		t.Fatal("expected IsFedRAMP true")
	}
	if !got.ChatGPT.ExpiresAt.Equal(expiresAt) {
		t.Fatalf("expires at mismatch: got %v want %v", got.ChatGPT.ExpiresAt, expiresAt)
	}
}

func TestUpsertChatGPTCredential_OmitsIDToken(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	enc := newTestEncryptor(t)
	store, _ := NewCredentialStore(db, enc)

	expiresAt := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	tokens := &ChatGPTTokens{
		AccessToken:  "a",
		RefreshToken: "r",
		AccountID:    "acct",
		ExpiresAt:    expiresAt,
	}
	mock.ExpectExec("REPLACE INTO credentials").
		WithArgs("g", "openai", string(AuthModeChatGPT),
			sqlmock.AnyArg(), sqlmock.AnyArg(), sql.NullString{}, "acct", expiresAt, false).
		WillReturnResult(sqlmock.NewResult(0, 1))

	if err := store.UpsertChatGPTCredential(context.Background(), "g", "openai", tokens); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestRefreshChatGPTTokens_Success(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	enc := newTestEncryptor(t)
	store, _ := NewCredentialStore(db, enc)

	expiresAt := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	mock.ExpectExec("UPDATE credentials").
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), "acct_99", expiresAt, false, "g1", string(AuthModeChatGPT)).
		WillReturnResult(sqlmock.NewResult(0, 1))

	if err := store.RefreshChatGPTTokens(context.Background(), "g1", &ChatGPTTokens{
		AccessToken: "new-a", RefreshToken: "new-r", IDToken: "new-id",
		AccountID: "acct_99", ExpiresAt: expiresAt,
	}); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestRefreshChatGPTTokens_NoRow(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	enc := newTestEncryptor(t)
	store, _ := NewCredentialStore(db, enc)

	mock.ExpectExec("UPDATE credentials").
		WillReturnResult(sqlmock.NewResult(0, 0))

	err = store.RefreshChatGPTTokens(context.Background(), "missing", &ChatGPTTokens{
		AccessToken: "a", RefreshToken: "r", AccountID: "acct", ExpiresAt: time.Now().Add(time.Hour),
	})
	if err == nil {
		t.Fatal("expected error when no chatgpt credential matched")
	}
}

func TestRefreshChatGPTTokens_RejectsInvalid(t *testing.T) {
	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	enc := newTestEncryptor(t)
	store, _ := NewCredentialStore(db, enc)

	if err := store.RefreshChatGPTTokens(context.Background(), "", &ChatGPTTokens{}); err == nil {
		t.Fatal("expected error for empty group jid")
	}
	if err := store.RefreshChatGPTTokens(context.Background(), "g", &ChatGPTTokens{}); err == nil {
		t.Fatal("expected error for empty token bundle")
	}
}

func TestGetCredential_ChatGPT_MissingTokens(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	enc := newTestEncryptor(t)
	store, _ := NewCredentialStore(db, enc)

	rows := sqlmock.NewRows([]string{"provider", "auth_mode", "api_key_encrypted", "oauth_access_token_encrypted", "oauth_refresh_token_encrypted", "oauth_id_token_encrypted", "oauth_account_id", "oauth_expires_at", "oauth_is_fedramp"}).
		AddRow("openai", string(AuthModeChatGPT), nil, nil, nil, nil, nil, nil, false)
	mock.ExpectQuery("SELECT").WithArgs("g").WillReturnRows(rows)

	if _, err := store.GetCredential(context.Background(), "g"); err == nil {
		t.Fatal("expected error when chatgpt row has missing oauth tokens")
	}
}

func TestGetCredential_APIKey_NullEncryptedKey(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	enc := newTestEncryptor(t)
	store, _ := NewCredentialStore(db, enc)

	rows := sqlmock.NewRows([]string{"provider", "auth_mode", "api_key_encrypted", "oauth_access_token_encrypted", "oauth_refresh_token_encrypted", "oauth_id_token_encrypted", "oauth_account_id", "oauth_expires_at", "oauth_is_fedramp"}).
		AddRow("openai", string(AuthModeAPIKey), nil, nil, nil, nil, nil, nil, false)
	mock.ExpectQuery("SELECT").WithArgs("g").WillReturnRows(rows)

	if _, err := store.GetCredential(context.Background(), "g"); err == nil {
		t.Fatal("expected error when api_key auth mode but key column is NULL")
	}
}

func TestEncryptor_RoundtripsMultipleSecrets(t *testing.T) {
	enc := newTestEncryptor(t)
	for _, plain := range []string{"sk-1", "rt-2", "id-3", strings.Repeat("z", 4096), ""} {
		ct, err := enc.Encrypt(plain)
		if err != nil {
			t.Fatalf("encrypt %q: %v", plain, err)
		}
		got, err := enc.Decrypt(ct)
		if err != nil {
			t.Fatalf("decrypt: %v", err)
		}
		if got != plain {
			t.Fatalf("roundtrip mismatch: got %q want %q", got, plain)
		}
	}
}

func TestRefreshChatGPTTokens_NoRow_ReturnsSentinel(t *testing.T) {
	t.Parallel()

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	enc := newTestEncryptor(t)
	store, err := NewCredentialStore(db, enc)
	if err != nil {
		t.Fatalf("store: %v", err)
	}

	mock.ExpectExec("UPDATE credentials").
		WillReturnResult(sqlmock.NewResult(0, 0))

	err = store.RefreshChatGPTTokens(context.Background(), "missing", &ChatGPTTokens{
		AccessToken:  "a",
		RefreshToken: "r",
		AccountID:    "acct",
		ExpiresAt:    time.Now().Add(time.Hour),
	})
	if err == nil {
		t.Fatal("RefreshChatGPTTokens(no row) err = nil, want sentinel")
	}
	if !errors.Is(err, ErrNoChatGPTCredential) {
		t.Errorf("errors.Is(%v, ErrNoChatGPTCredential) = false, want true", err)
	}
}
