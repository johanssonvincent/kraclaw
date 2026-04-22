package credproxy

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

func newTestEncryptor(t *testing.T) *Encryptor {
	t.Helper()
	enc, err := NewEncryptor(strings.Repeat("ab", 32))
	if err != nil {
		t.Fatal(err)
	}
	return enc
}


func TestCredentialStore_UpsertAndGet_APIKey(t *testing.T) {
	t.Parallel()

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

	cred := &Credential{
		GroupJID: "discord:123",
		Provider: "openai",
		AuthMode: AuthModeAPIKey,
		APIKey:   "sk-test-key",
	}

	mock.ExpectExec("REPLACE INTO credentials").
		WithArgs("discord:123", "openai", string(AuthModeAPIKey), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))

	if err := store.UpsertCredential(context.Background(), cred); err != nil {
		t.Errorf("upsert: %v", err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("expectations: %v", err)
	}
}

func TestCredentialStore_Delete(t *testing.T) {
	t.Parallel()

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

	mock.ExpectExec("DELETE FROM credentials WHERE group_jid").
		WithArgs("discord:123").
		WillReturnResult(sqlmock.NewResult(0, 1))

	if err := store.DeleteCredential(context.Background(), "discord:123"); err != nil {
		t.Errorf("delete: %v", err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("expectations: %v", err)
	}
}

func TestCredential_Validate(t *testing.T) {
	t.Parallel()

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
			t.Parallel()
			if err := tt.cred.Validate(); (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestUpsertCredential_RejectsEmptyAPIKey(t *testing.T) {
	t.Parallel()

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
		t.Errorf("expected error for credential with no API key")
	}
}

func TestGetCredential_Found_APIKey(t *testing.T) {
	t.Parallel()

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
		t.Errorf("unexpected error: %v", err)
	}
	if cred == nil {
		t.Errorf("expected non-nil credential")
	}
	if cred != nil && cred.Provider != "openai" {
		t.Errorf("expected provider openai, got %q", cred.Provider)
	}
	if cred != nil && cred.AuthMode != AuthModeAPIKey {
		t.Errorf("expected AuthMode api_key, got %q", cred.AuthMode)
	}
	if cred != nil && cred.APIKey != "sk-test-key" {
		t.Errorf("expected API key sk-test-key, got %q", cred.APIKey)
	}
	if cred != nil && cred.ChatGPT != nil {
		t.Errorf("expected nil ChatGPT tokens for api_key auth mode")
	}
}

func TestGetCredential_LegacyEmptyAuthMode(t *testing.T) {
	t.Parallel()

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
		t.Errorf("unexpected error: %v", err)
	}
	if cred != nil && cred.AuthMode != AuthModeAPIKey {
		t.Errorf("expected legacy empty auth_mode to map to api_key, got %q", cred.AuthMode)
	}
	if cred != nil && cred.APIKey != "sk-legacy" {
		t.Errorf("unexpected api key: %q", cred.APIKey)
	}
}

func TestGetCredential_NotFound(t *testing.T) {
	t.Parallel()

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
		t.Errorf("expected nil error for not found, got: %v", err)
	}
	if cred != nil {
		t.Errorf("expected nil credential for not found")
	}
}

func TestGetCredential_Anthropic(t *testing.T) {
	t.Parallel()

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
		t.Errorf("unexpected error: %v", err)
	}
	if cred == nil {
		t.Errorf("expected non-nil credential")
	}
	if cred != nil && cred.Provider != "anthropic" {
		t.Errorf("expected anthropic provider, got %q", cred.Provider)
	}
	if cred != nil && cred.APIKey != "sk-ant-test-key" {
		t.Errorf("expected API key sk-ant-test-key, got %q", cred.APIKey)
	}
}

func TestUpsertChatGPTCredential_AndGet(t *testing.T) {
	t.Parallel()

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
		t.Errorf("upsert chatgpt: %v", err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("expectations: %v", err)
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
		t.Errorf("get chatgpt: %v", err)
	}
	if got == nil {
		t.Errorf("expected non-nil chatgpt credential")
	}
	if got != nil && got.AuthMode != AuthModeChatGPT {
		t.Errorf("AuthMode = %q, want chatgpt", got.AuthMode)
	}
	if got != nil && got.APIKey != "" {
		t.Errorf("APIKey should be empty in chatgpt mode, got %q", got.APIKey)
	}
	if got != nil && got.ChatGPT == nil {
		t.Errorf("ChatGPT tokens missing")
	}
	if got != nil && got.ChatGPT != nil && got.ChatGPT.AccessToken != "access-token-XYZ" {
		t.Errorf("access token roundtrip mismatch: %q", got.ChatGPT.AccessToken)
	}
	if got != nil && got.ChatGPT != nil && got.ChatGPT.RefreshToken != "refresh-token-ABC" {
		t.Errorf("refresh token roundtrip mismatch: %q", got.ChatGPT.RefreshToken)
	}
	if got != nil && got.ChatGPT != nil && got.ChatGPT.IDToken != "id-token-PQR" {
		t.Errorf("id token roundtrip mismatch: %q", got.ChatGPT.IDToken)
	}
	if got != nil && got.ChatGPT != nil && got.ChatGPT.AccountID != "acct_42" {
		t.Errorf("account id mismatch: %q", got.ChatGPT.AccountID)
	}
	if got != nil && got.ChatGPT != nil && !got.ChatGPT.IsFedRAMP {
		t.Errorf("expected IsFedRAMP true")
	}
	if got != nil && got.ChatGPT != nil && !got.ChatGPT.ExpiresAt.Equal(expiresAt) {
		t.Errorf("expires at mismatch: got %v want %v", got.ChatGPT.ExpiresAt, expiresAt)
	}
}

func TestUpsertChatGPTCredential_OmitsIDToken(t *testing.T) {
	t.Parallel()

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
		t.Errorf("upsert: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("expectations: %v", err)
	}
}

func TestRefreshChatGPTTokens_Success(t *testing.T) {
	t.Parallel()

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
		t.Errorf("refresh: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("expectations: %v", err)
	}
}

func TestRefreshChatGPTTokens_NoRow(t *testing.T) {
	t.Parallel()

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
		t.Errorf("expected error when no chatgpt credential matched")
	}
}

func TestRefreshChatGPTTokens_RejectsInvalid(t *testing.T) {
	t.Parallel()

	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	enc := newTestEncryptor(t)
	store, _ := NewCredentialStore(db, enc)

	if err := store.RefreshChatGPTTokens(context.Background(), "", &ChatGPTTokens{}); err == nil {
		t.Errorf("expected error for empty group jid")
	}
	if err := store.RefreshChatGPTTokens(context.Background(), "g", &ChatGPTTokens{}); err == nil {
		t.Errorf("expected error for empty token bundle")
	}
}

func TestGetCredential_ChatGPT_MissingTokens(t *testing.T) {
	t.Parallel()

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
		t.Errorf("expected error when chatgpt row has missing oauth tokens")
	}
}

func TestGetCredential_APIKey_NullEncryptedKey(t *testing.T) {
	t.Parallel()

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
		t.Errorf("expected error when api_key auth mode but key column is NULL")
	}
}

func TestEncryptor_RoundtripsMultipleSecrets(t *testing.T) {
	t.Parallel()

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
			t.Errorf("roundtrip mismatch: got %q want %q", got, plain)
		}
	}
}

func TestCredential_Validate_RejectsExpiredChatGPTToken(t *testing.T) {
	t.Parallel()

	cred := Credential{
		GroupJID: "g",
		Provider: "openai",
		AuthMode: AuthModeChatGPT,
		ChatGPT: &ChatGPTTokens{
			AccessToken:  "a",
			RefreshToken: "r",
			AccountID:    "acct",
			ExpiresAt:    time.Now().Add(-time.Minute),
		},
	}
	if err := cred.Validate(); err == nil {
		t.Errorf("Validate(expired token) err = nil, want error")
	}
}

func TestUpsertCredential_RejectsEmptyAuthMode(t *testing.T) {
	t.Parallel()

	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	enc := newTestEncryptor(t)
	store, err := NewCredentialStore(db, enc)
	if err != nil {
		t.Fatalf("store: %v", err)
	}

	err = store.UpsertCredential(context.Background(), &Credential{
		GroupJID: "g",
		Provider: "openai",
		APIKey:   "sk",
	})
	if err == nil {
		t.Errorf("UpsertCredential(empty AuthMode) err = nil, want error")
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
		t.Errorf("RefreshChatGPTTokens(no row) err = nil, want sentinel")
	}
	if !errors.Is(err, ErrNoChatGPTCredential) {
		t.Errorf("errors.Is(%v, ErrNoChatGPTCredential) = false, want true", err)
	}
}

func TestNewAPIKeyCredential(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		groupJID string
		provider string
		apiKey   string
		wantErr  bool
	}{
		"valid":          {"g", "openai", "sk", false},
		"empty group":    {"", "openai", "sk", true},
		"empty provider": {"g", "", "sk", true},
		"empty api key":  {"g", "openai", "", true},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			cred, err := NewAPIKeyCredential(tt.groupJID, tt.provider, tt.apiKey)
			if (err != nil) != tt.wantErr {
				t.Errorf("NewAPIKeyCredential(%q, %q, %q) err = %v, wantErr %v",
					tt.groupJID, tt.provider, tt.apiKey, err, tt.wantErr)
			}
			if !tt.wantErr && cred.AuthMode != AuthModeAPIKey {
				t.Errorf("NewAPIKeyCredential(...).AuthMode = %q, want %q", cred.AuthMode, AuthModeAPIKey)
			}
		})
	}
}

func TestNewChatGPTCredential(t *testing.T) {
	t.Parallel()

	good := &ChatGPTTokens{
		AccessToken:  "a",
		RefreshToken: "r",
		AccountID:    "acct",
		ExpiresAt:    time.Now().Add(time.Hour),
	}
	tests := map[string]struct {
		groupJID string
		provider string
		tokens   *ChatGPTTokens
		wantErr  bool
	}{
		"valid":       {"g", "openai", good, false},
		"nil tokens":  {"g", "openai", nil, true},
		"empty group": {"", "openai", good, true},
		"bad tokens":  {"g", "openai", &ChatGPTTokens{AccessToken: "a"}, true},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			cred, err := NewChatGPTCredential(tt.groupJID, tt.provider, tt.tokens)
			if (err != nil) != tt.wantErr {
				t.Errorf("NewChatGPTCredential(%q, %q, %+v) err = %v, wantErr %v",
					tt.groupJID, tt.provider, tt.tokens, err, tt.wantErr)
			}
			if !tt.wantErr && cred.AuthMode != AuthModeChatGPT {
				t.Errorf("NewChatGPTCredential(...).AuthMode = %q, want %q", cred.AuthMode, AuthModeChatGPT)
			}
		})
	}
}

func TestGetCredential_ValidatesDecryptedShape(t *testing.T) {
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

	accessEnc, err := enc.Encrypt("a")
	if err != nil {
		t.Fatalf("encrypt access: %v", err)
	}
	refreshEnc, err := enc.Encrypt("r")
	if err != nil {
		t.Fatalf("encrypt refresh: %v", err)
	}
	pastExpiry := time.Now().Add(-time.Hour).UTC().Truncate(time.Second)
	rows := sqlmock.NewRows([]string{
		"provider", "auth_mode", "api_key_encrypted",
		"oauth_access_token_encrypted", "oauth_refresh_token_encrypted",
		"oauth_id_token_encrypted", "oauth_account_id",
		"oauth_expires_at", "oauth_is_fedramp",
	}).AddRow("openai", string(AuthModeChatGPT), nil, accessEnc, refreshEnc, nil, "acct", pastExpiry, false)
	mock.ExpectQuery("SELECT").WithArgs("g").WillReturnRows(rows)

	if _, err := store.GetCredential(context.Background(), "g"); err == nil {
		t.Errorf("GetCredential(expired chatgpt row) err = nil, want validation error")
	}
}

func TestRefreshChatGPTTokens_NilTokensErrors(t *testing.T) {
	t.Parallel()

	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	enc := newTestEncryptor(t)
	store, err := NewCredentialStore(db, enc)
	if err != nil {
		t.Fatalf("store: %v", err)
	}

	defer func() {
		if r := recover(); r != nil {
			t.Errorf("RefreshChatGPTTokens(nil) panicked: %v, want error", r)
		}
	}()

	if err := store.RefreshChatGPTTokens(context.Background(), "g", nil); err == nil {
		t.Errorf("RefreshChatGPTTokens(nil tokens) err = nil, want error")
	}
}
