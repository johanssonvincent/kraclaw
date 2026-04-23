package credproxy

import (
	"context"
	"database/sql"
	"database/sql/driver"
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

func expectTimezoneProbe(t *testing.T, mock sqlmock.Sqlmock) {
	t.Helper()
	mock.ExpectQuery("SELECT TIMESTAMP").WillReturnRows(
		sqlmock.NewRows([]string{"t"}).AddRow(time.Now().UTC().Truncate(time.Second)),
	)
}

func TestCredentialStore_UpsertAndGet_APIKey(t *testing.T) {
	t.Parallel()

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	enc := newTestEncryptor(t)
	expectTimezoneProbe(t, mock)
	store, err := NewCredentialStore(db, enc)
	if err != nil {
		t.Fatal(err)
	}

	cred := &Credential{
		GroupJID: "discord:123",
		Provider: "openai",
		AuthMode: AuthModeAPIKey,
		apiKey:   "sk-test-key",
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
	expectTimezoneProbe(t, mock)
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
		{"api_key valid", Credential{GroupJID: "discord:123", Provider: "openai", apiKey: "sk-test"}, false},
		{"api_key explicit valid", Credential{GroupJID: "discord:123", Provider: "openai", AuthMode: AuthModeAPIKey, apiKey: "sk-test"}, false},
		{"empty group JID", Credential{GroupJID: "", Provider: "openai", apiKey: "sk-test"}, true},
		{"empty provider", Credential{GroupJID: "discord:123", Provider: "", apiKey: "sk-test"}, true},
		{"api_key missing key", Credential{GroupJID: "discord:123", Provider: "openai"}, true},
		{"api_key with chatgpt tokens set", Credential{GroupJID: "discord:123", Provider: "openai", apiKey: "sk-test", chatGPT: validTokens()}, true},
		{"chatgpt valid", Credential{GroupJID: "discord:123", Provider: "openai", AuthMode: AuthModeChatGPT, chatGPT: validTokens()}, false},
		{"chatgpt missing tokens", Credential{GroupJID: "discord:123", Provider: "openai", AuthMode: AuthModeChatGPT}, true},
		{"chatgpt with api key", Credential{GroupJID: "discord:123", Provider: "openai", AuthMode: AuthModeChatGPT, apiKey: "sk-test", chatGPT: validTokens()}, true},
		{"chatgpt missing access token", Credential{GroupJID: "discord:123", Provider: "openai", AuthMode: AuthModeChatGPT, chatGPT: &ChatGPTTokens{RefreshToken: "r", AccountID: "a", ExpiresAt: time.Now().Add(time.Hour)}}, true},
		{"chatgpt missing refresh token", Credential{GroupJID: "discord:123", Provider: "openai", AuthMode: AuthModeChatGPT, chatGPT: &ChatGPTTokens{AccessToken: "a", AccountID: "a", ExpiresAt: time.Now().Add(time.Hour)}}, true},
		{"chatgpt missing account id", Credential{GroupJID: "discord:123", Provider: "openai", AuthMode: AuthModeChatGPT, chatGPT: &ChatGPTTokens{AccessToken: "a", RefreshToken: "r", ExpiresAt: time.Now().Add(time.Hour)}}, true},
		{"chatgpt zero expiry", Credential{GroupJID: "discord:123", Provider: "openai", AuthMode: AuthModeChatGPT, chatGPT: &ChatGPTTokens{AccessToken: "a", RefreshToken: "r", AccountID: "a"}}, true},
		{"unknown auth mode", Credential{GroupJID: "discord:123", Provider: "openai", AuthMode: AuthMode("magic"), apiKey: "sk-test"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if err := tt.cred.Validate(); (err != nil) != tt.wantErr {
				t.Errorf("Validate(%+v) error = %v, wantErr %v", tt.cred, err, tt.wantErr)
			}
		})
	}
}

func TestUpsertCredential_RejectsEmptyAPIKey(t *testing.T) {
	t.Parallel()

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	enc := newTestEncryptor(t)
	expectTimezoneProbe(t, mock)
	store, err := NewCredentialStore(db, enc)
	if err != nil {
		t.Fatal(err)
	}

	if err := store.UpsertCredential(context.Background(), &Credential{
		GroupJID: "discord:123",
		Provider: "openai",
	}); err == nil {
		t.Errorf("UpsertCredential(api_key, empty APIKey) err = nil, want error")
	}
}

func TestGetCredential(t *testing.T) {
	t.Parallel()

	enc := newTestEncryptor(t)
	encAPIKey, err := enc.Encrypt("sk-test-key")
	if err != nil {
		t.Fatalf("encrypt setup: %v", err)
	}
	encAntKey, err := enc.Encrypt("sk-ant-test-key")
	if err != nil {
		t.Fatalf("encrypt setup: %v", err)
	}
	encAccess, err := enc.Encrypt("a")
	if err != nil {
		t.Fatalf("encrypt setup: %v", err)
	}
	encRefresh, err := enc.Encrypt("r")
	if err != nil {
		t.Fatalf("encrypt setup: %v", err)
	}
	expiresAt := time.Now().Add(time.Hour).UTC().Truncate(time.Second)

	columns := []string{
		"provider", "auth_mode", "api_key_encrypted",
		"oauth_access_token_encrypted", "oauth_refresh_token_encrypted",
		"oauth_id_token_encrypted", "oauth_account_id",
		"oauth_expires_at", "oauth_is_fedramp",
	}

	tests := map[string]struct {
		groupJID    string
		row         []driver.Value // nil means ExpectQuery returns ErrNoRows
		wantErr     bool
		wantNil     bool
		wantMode    AuthMode
		wantKey     string
		wantChatGPT bool
	}{
		"api_key openai found": {
			groupJID: "discord:123",
			row:      []driver.Value{"openai", string(AuthModeAPIKey), encAPIKey, nil, nil, nil, nil, nil, false},
			wantMode: AuthModeAPIKey,
			wantKey:  "sk-test-key",
		},
		"api_key anthropic found": {
			groupJID: "discord:456",
			row:      []driver.Value{"anthropic", string(AuthModeAPIKey), encAntKey, nil, nil, nil, nil, nil, false},
			wantMode: AuthModeAPIKey,
			wantKey:  "sk-ant-test-key",
		},
		"legacy empty auth_mode maps to api_key": {
			groupJID: "discord:legacy",
			row:      []driver.Value{"openai", "", encAPIKey, nil, nil, nil, nil, nil, false},
			wantMode: AuthModeAPIKey,
			wantKey:  "sk-test-key",
		},
		"not found returns nil": {
			groupJID: "unknown:123",
			row:      nil,
			wantNil:  true,
		},
		"chatgpt with oauth fields": {
			groupJID:    "discord:42",
			row:         []driver.Value{"openai", string(AuthModeChatGPT), nil, encAccess, encRefresh, nil, "acct_42", expiresAt, false},
			wantMode:    AuthModeChatGPT,
			wantChatGPT: true,
		},
		"chatgpt row missing oauth tokens errors": {
			groupJID: "g",
			row:      []driver.Value{"openai", string(AuthModeChatGPT), nil, nil, nil, nil, nil, nil, false},
			wantErr:  true,
		},
		"api_key row with NULL encrypted key errors": {
			groupJID: "g",
			row:      []driver.Value{"openai", string(AuthModeAPIKey), nil, nil, nil, nil, nil, nil, false},
			wantErr:  true,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatalf("sqlmock: %v", err)
			}
			t.Cleanup(func() { _ = db.Close() })

			expectTimezoneProbe(t, mock)
			store, err := NewCredentialStore(db, enc)
			if err != nil {
				t.Fatalf("NewCredentialStore: %v", err)
			}

			if tt.row == nil {
				mock.ExpectQuery("SELECT").WithArgs(tt.groupJID).WillReturnError(sql.ErrNoRows)
			} else {
				mock.ExpectQuery("SELECT").WithArgs(tt.groupJID).WillReturnRows(sqlmock.NewRows(columns).AddRow(tt.row...))
			}

			cred, err := store.GetCredential(context.Background(), tt.groupJID)
			if (err != nil) != tt.wantErr {
				t.Errorf("GetCredential(%q) err = %v, wantErr %v", tt.groupJID, err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if tt.wantNil {
				if cred != nil {
					t.Errorf("GetCredential(%q) cred = %+v, want nil", tt.groupJID, cred)
				}
				return
			}
			if cred == nil {
				t.Fatalf("GetCredential(%q) cred = nil, want non-nil", tt.groupJID)
			}
			if cred.AuthMode != tt.wantMode {
				t.Errorf("GetCredential(%q).AuthMode = %q, want %q", tt.groupJID, cred.AuthMode, tt.wantMode)
			}
			if cred.APIKey() != tt.wantKey {
				t.Errorf("GetCredential(%q).APIKey() = %q, want %q", tt.groupJID, cred.APIKey(), tt.wantKey)
			}
			if (cred.ChatGPT() != nil) != tt.wantChatGPT {
				t.Errorf("GetCredential(%q) has ChatGPT = %v, want %v", tt.groupJID, cred.ChatGPT() != nil, tt.wantChatGPT)
			}
		})
	}
}

func TestUpsertChatGPTCredential(t *testing.T) {
	t.Parallel()

	expiresAt := time.Now().Add(45 * time.Minute).UTC().Truncate(time.Second)

	tests := map[string]struct {
		tokens      *ChatGPTTokens
		wantIDEnc   driver.Value
		wantFedRAMP bool
	}{
		"full bundle with id token and fedramp": {
			tokens: &ChatGPTTokens{
				AccessToken:  "access-token-XYZ",
				RefreshToken: "refresh-token-ABC",
				IDToken:      "id-token-PQR",
				AccountID:    "acct_42",
				ExpiresAt:    expiresAt,
				IsFedRAMP:    true,
			},
			wantIDEnc:   sqlmock.AnyArg(),
			wantFedRAMP: true,
		},
		"omits id token when empty": {
			tokens: &ChatGPTTokens{
				AccessToken:  "a",
				RefreshToken: "r",
				AccountID:    "acct",
				ExpiresAt:    expiresAt,
			},
			wantIDEnc:   sql.NullString{},
			wantFedRAMP: false,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatalf("sqlmock: %v", err)
			}
			t.Cleanup(func() { _ = db.Close() })

			enc := newTestEncryptor(t)
			expectTimezoneProbe(t, mock)
			store, err := NewCredentialStore(db, enc)
			if err != nil {
				t.Fatalf("NewCredentialStore: %v", err)
			}

			mock.ExpectExec("REPLACE INTO credentials").
				WithArgs(
					"g", "openai", string(AuthModeChatGPT),
					sqlmock.AnyArg(), sqlmock.AnyArg(), tt.wantIDEnc,
					tt.tokens.AccountID, expiresAt, tt.wantFedRAMP,
				).
				WillReturnResult(sqlmock.NewResult(0, 1))

			if err := store.UpsertChatGPTCredential(context.Background(), "g", "openai", tt.tokens); err != nil {
				t.Errorf("UpsertChatGPTCredential(%+v) err = %v, want nil", tt.tokens, err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Errorf("sqlmock expectations not met: %v", err)
			}
		})
	}
}

func TestUpsertChatGPTCredential_Roundtrip(t *testing.T) {
	t.Parallel()

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	enc := newTestEncryptor(t)
	expectTimezoneProbe(t, mock)
	store, err := NewCredentialStore(db, enc)
	if err != nil {
		t.Fatalf("NewCredentialStore: %v", err)
	}

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
		WithArgs("discord:42", "openai", string(AuthModeChatGPT),
			sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(),
			"acct_42", expiresAt, true).
		WillReturnResult(sqlmock.NewResult(0, 1))

	if err := store.UpsertChatGPTCredential(context.Background(), "discord:42", "openai", tokens); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	accessEnc, err := enc.Encrypt("access-token-XYZ")
	if err != nil {
		t.Fatalf("encrypt access: %v", err)
	}
	refreshEnc, err := enc.Encrypt("refresh-token-ABC")
	if err != nil {
		t.Fatalf("encrypt refresh: %v", err)
	}
	idEnc, err := enc.Encrypt("id-token-PQR")
	if err != nil {
		t.Fatalf("encrypt id: %v", err)
	}
	rows := sqlmock.NewRows([]string{
		"provider", "auth_mode", "api_key_encrypted",
		"oauth_access_token_encrypted", "oauth_refresh_token_encrypted",
		"oauth_id_token_encrypted", "oauth_account_id",
		"oauth_expires_at", "oauth_is_fedramp",
	}).AddRow("openai", string(AuthModeChatGPT), nil, accessEnc, refreshEnc, idEnc, "acct_42", expiresAt, true)
	mock.ExpectQuery("SELECT").WithArgs("discord:42").WillReturnRows(rows)

	got, err := store.GetCredential(context.Background(), "discord:42")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got == nil || got.ChatGPT() == nil {
		t.Fatalf("GetCredential returned incomplete credential: %+v", got)
	}

	checks := map[string]struct{ got, want string }{
		"access token":  {got.ChatGPT().AccessToken, "access-token-XYZ"},
		"refresh token": {got.ChatGPT().RefreshToken, "refresh-token-ABC"},
		"id token":      {got.ChatGPT().IDToken, "id-token-PQR"},
		"account id":    {got.ChatGPT().AccountID, "acct_42"},
	}
	for name, c := range checks {
		if c.got != c.want {
			t.Errorf("roundtrip %s = %q, want %q", name, c.got, c.want)
		}
	}
	if !got.ChatGPT().IsFedRAMP {
		t.Errorf("roundtrip IsFedRAMP = false, want true")
	}
	if !got.ChatGPT().ExpiresAt.Equal(expiresAt) {
		t.Errorf("roundtrip ExpiresAt = %v, want %v", got.ChatGPT().ExpiresAt, expiresAt)
	}
	if got.APIKey() != "" {
		t.Errorf("roundtrip APIKey() = %q, want empty", got.APIKey())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("sqlmock expectations not met: %v", err)
	}
}

func TestRefreshChatGPTTokens(t *testing.T) {
	t.Parallel()

	expiresAt := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	validTokens := func() *ChatGPTTokens {
		return &ChatGPTTokens{
			AccessToken:  "new-a",
			RefreshToken: "new-r",
			IDToken:      "new-id",
			AccountID:    "acct_99",
			ExpiresAt:    expiresAt,
		}
	}

	tests := map[string]struct {
		groupJID    string
		tokens      *ChatGPTTokens
		setupMock   func(m sqlmock.Sqlmock)
		wantErr     bool
		wantMockMet bool
	}{
		"success updates existing row": {
			groupJID: "g1",
			tokens:   validTokens(),
			setupMock: func(m sqlmock.Sqlmock) {
				m.ExpectExec("UPDATE credentials").
					WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(),
						"acct_99", expiresAt, false, "g1", string(AuthModeChatGPT)).
					WillReturnResult(sqlmock.NewResult(0, 1))
			},
			wantErr:     false,
			wantMockMet: true,
		},
		"rejects empty group jid": {
			groupJID:    "",
			tokens:      validTokens(),
			setupMock:   func(m sqlmock.Sqlmock) {},
			wantErr:     true,
			wantMockMet: false,
		},
		"rejects empty token bundle": {
			groupJID:    "g",
			tokens:      &ChatGPTTokens{},
			setupMock:   func(m sqlmock.Sqlmock) {},
			wantErr:     true,
			wantMockMet: false,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatalf("sqlmock: %v", err)
			}
			t.Cleanup(func() { _ = db.Close() })

			enc := newTestEncryptor(t)
			expectTimezoneProbe(t, mock)
			store, err := NewCredentialStore(db, enc)
			if err != nil {
				t.Fatalf("NewCredentialStore: %v", err)
			}
			tt.setupMock(mock)

			err = store.RefreshChatGPTTokens(context.Background(), tt.groupJID, tt.tokens)
			if (err != nil) != tt.wantErr {
				t.Errorf("RefreshChatGPTTokens(%q, %+v) err = %v, wantErr %v",
					tt.groupJID, tt.tokens, err, tt.wantErr)
			}
			if tt.wantMockMet {
				if err := mock.ExpectationsWereMet(); err != nil {
					t.Errorf("sqlmock expectations not met: %v", err)
				}
			}
		})
	}
}

func TestEncryptor_RoundtripsMultipleSecrets(t *testing.T) {
	t.Parallel()

	enc := newTestEncryptor(t)
	tests := map[string]string{
		"short":     "sk-1",
		"refresh":   "rt-2",
		"id token":  "id-3",
		"4KiB blob": strings.Repeat("z", 4096),
		"empty":     "",
	}
	for name, plain := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ct, err := enc.Encrypt(plain)
			if err != nil {
				t.Fatalf("Encrypt(%q): %v", plain, err)
			}
			got, err := enc.Decrypt(ct)
			if err != nil {
				t.Fatalf("Decrypt(ciphertext of %q): %v", plain, err)
			}
			if got != plain {
				t.Errorf("Decrypt(Encrypt(%q)) = %q, want %q", plain, got, plain)
			}
		})
	}
}

func TestCredential_Validate_RejectsExpiredChatGPTToken(t *testing.T) {
	t.Parallel()

	cred := Credential{
		GroupJID: "g",
		Provider: "openai",
		AuthMode: AuthModeChatGPT,
		chatGPT: &ChatGPTTokens{
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

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	enc := newTestEncryptor(t)
	expectTimezoneProbe(t, mock)
	store, err := NewCredentialStore(db, enc)
	if err != nil {
		t.Fatalf("store: %v", err)
	}

	err = store.UpsertCredential(context.Background(), &Credential{
		GroupJID: "g",
		Provider: "openai",
		apiKey:   "sk",
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
	expectTimezoneProbe(t, mock)
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
	expectTimezoneProbe(t, mock)
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

	got, err := store.GetCredential(context.Background(), "g")
	if err != nil {
		t.Errorf("GetCredential(expired chatgpt row) err = %v, want nil (refresh flow reads expired rows)", err)
	}
	if got == nil || got.AuthMode != AuthModeChatGPT {
		t.Errorf("GetCredential(expired chatgpt row) = %+v, want non-nil chatgpt credential", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

func TestRefreshChatGPTTokens_NilTokensErrors(t *testing.T) {
	t.Parallel()

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	enc := newTestEncryptor(t)
	expectTimezoneProbe(t, mock)
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

func TestGetCredential_DecryptErrors(t *testing.T) {
	t.Parallel()

	columns := []string{
		"provider", "auth_mode", "api_key_encrypted",
		"oauth_access_token_encrypted", "oauth_refresh_token_encrypted",
		"oauth_id_token_encrypted", "oauth_account_id",
		"oauth_expires_at", "oauth_is_fedramp",
	}
	badCipher := "NOT_A_VALID_CIPHERTEXT"
	goodExpiry := time.Now().Add(time.Hour).UTC().Truncate(time.Second)

	enc := newTestEncryptor(t)
	validAccess, err := enc.Encrypt("access")
	if err != nil {
		t.Fatalf("encrypt access: %v", err)
	}
	validRefresh, err := enc.Encrypt("refresh")
	if err != nil {
		t.Fatalf("encrypt refresh: %v", err)
	}

	tests := map[string]struct {
		row     []driver.Value
		wantMsg string
	}{
		"api_key decrypt fails": {
			row:     []driver.Value{"openai", string(AuthModeAPIKey), badCipher, nil, nil, nil, nil, nil, false},
			wantMsg: "decrypt api key",
		},
		"access token decrypt fails": {
			row:     []driver.Value{"openai", string(AuthModeChatGPT), nil, badCipher, "ignored", nil, "acct", goodExpiry, false},
			wantMsg: "decrypt access token",
		},
		"refresh token decrypt fails": {
			row:     []driver.Value{"openai", string(AuthModeChatGPT), nil, validAccess, badCipher, nil, "acct", goodExpiry, false},
			wantMsg: "decrypt refresh token",
		},
		"id token decrypt fails": {
			row:     []driver.Value{"openai", string(AuthModeChatGPT), nil, validAccess, validRefresh, badCipher, "acct", goodExpiry, false},
			wantMsg: "decrypt id token",
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatalf("sqlmock: %v", err)
			}
			t.Cleanup(func() { _ = db.Close() })

			expectTimezoneProbe(t, mock)
			store, err := NewCredentialStore(db, enc)
			if err != nil {
				t.Fatalf("NewCredentialStore: %v", err)
			}
			mock.ExpectQuery("SELECT").WithArgs("g").WillReturnRows(sqlmock.NewRows(columns).AddRow(tt.row...))

			_, err = store.GetCredential(context.Background(), "g")
			if err == nil {
				t.Errorf("GetCredential(bad ciphertext %q) err = nil, want error containing %q", name, tt.wantMsg)
				return
			}
			if !strings.Contains(err.Error(), tt.wantMsg) {
				t.Errorf("GetCredential(%q) err = %v, want message containing %q", name, err, tt.wantMsg)
			}
		})
	}
}

func TestGetCredential_ChatGPTNullFieldGuards(t *testing.T) {
	t.Parallel()

	columns := []string{
		"provider", "auth_mode", "api_key_encrypted",
		"oauth_access_token_encrypted", "oauth_refresh_token_encrypted",
		"oauth_id_token_encrypted", "oauth_account_id",
		"oauth_expires_at", "oauth_is_fedramp",
	}
	enc := newTestEncryptor(t)
	access, err := enc.Encrypt("a")
	if err != nil {
		t.Fatalf("encrypt access: %v", err)
	}
	refresh, err := enc.Encrypt("r")
	if err != nil {
		t.Fatalf("encrypt refresh: %v", err)
	}
	expiry := time.Now().Add(time.Hour).UTC().Truncate(time.Second)

	tests := map[string]struct {
		row     []driver.Value
		wantMsg string
	}{
		"access NULL, refresh set": {
			row:     []driver.Value{"openai", string(AuthModeChatGPT), nil, nil, refresh, nil, "acct", expiry, false},
			wantMsg: "oauth tokens are missing",
		},
		"refresh NULL, access set": {
			row:     []driver.Value{"openai", string(AuthModeChatGPT), nil, access, nil, nil, "acct", expiry, false},
			wantMsg: "oauth tokens are missing",
		},
		"account id NULL with tokens present": {
			row:     []driver.Value{"openai", string(AuthModeChatGPT), nil, access, refresh, nil, nil, expiry, false},
			wantMsg: "oauth_account_id is missing",
		},
		"expires_at NULL with tokens present": {
			row:     []driver.Value{"openai", string(AuthModeChatGPT), nil, access, refresh, nil, "acct", nil, false},
			wantMsg: "oauth_expires_at is NULL",
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatalf("sqlmock: %v", err)
			}
			t.Cleanup(func() { _ = db.Close() })
			expectTimezoneProbe(t, mock)
			store, err := NewCredentialStore(db, enc)
			if err != nil {
				t.Fatalf("NewCredentialStore: %v", err)
			}
			mock.ExpectQuery("SELECT").WithArgs("g").WillReturnRows(sqlmock.NewRows(columns).AddRow(tt.row...))

			_, err = store.GetCredential(context.Background(), "g")
			if err == nil {
				t.Errorf("GetCredential(%s) err = nil, want error containing %q", name, tt.wantMsg)
				return
			}
			if !strings.Contains(err.Error(), tt.wantMsg) {
				t.Errorf("GetCredential(%s) err = %v, want message containing %q", name, err, tt.wantMsg)
			}
		})
	}
}


type fakeCipher struct {
	encryptErr error
	decryptErr error
	encrypted  string
	decrypted  string
}

func (f *fakeCipher) Encrypt(plaintext string) (string, error) {
	if f.encryptErr != nil {
		return "", f.encryptErr
	}
	if f.encrypted != "" {
		return f.encrypted, nil
	}
	return "enc:" + plaintext, nil
}

func (f *fakeCipher) Decrypt(ciphertext string) (string, error) {
	if f.decryptErr != nil {
		return "", f.decryptErr
	}
	if f.decrypted != "" {
		return f.decrypted, nil
	}
	return ciphertext, nil
}

func TestCredentialStore_AcceptsCipherInterface(t *testing.T) {
	t.Parallel()
	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New(): %v", err)
	}
	defer func() { _ = db.Close() }()

	// Compile-time check: *fakeCipher must satisfy the encrypter interface so
	// that the enc field accepts it. The assignment below fails to compile if
	// the interface is not satisfied.
	store := &CredentialStore{db: db, enc: &fakeCipher{}}
	_ = store
}

func TestChatGPTTokens_ValidateStructure(t *testing.T) {
	t.Parallel()
	now := time.Now()
	tests := map[string]struct {
		tokens  *ChatGPTTokens
		wantErr bool
	}{
		"expired but structurally complete": {
			tokens: &ChatGPTTokens{
				AccessToken:  "a",
				RefreshToken: "r",
				AccountID:    "acct",
				ExpiresAt:    now.Add(-1 * time.Hour),
			},
			wantErr: false,
		},
		"future and complete": {
			tokens: &ChatGPTTokens{
				AccessToken:  "a",
				RefreshToken: "r",
				AccountID:    "acct",
				ExpiresAt:    now.Add(1 * time.Hour),
			},
			wantErr: false,
		},
		"missing access token": {
			tokens: &ChatGPTTokens{
				RefreshToken: "r", AccountID: "acct", ExpiresAt: now.Add(1 * time.Hour),
			},
			wantErr: true,
		},
		"missing refresh token": {
			tokens: &ChatGPTTokens{
				AccessToken: "a", AccountID: "acct", ExpiresAt: now.Add(1 * time.Hour),
			},
			wantErr: true,
		},
		"missing account id": {
			tokens: &ChatGPTTokens{
				AccessToken: "a", RefreshToken: "r", ExpiresAt: now.Add(1 * time.Hour),
			},
			wantErr: true,
		},
		"zero expires_at": {
			tokens: &ChatGPTTokens{
				AccessToken: "a", RefreshToken: "r", AccountID: "acct",
			},
			wantErr: true,
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			err := tt.tokens.validateStructure()
			if (err != nil) != tt.wantErr {
				t.Errorf("validateStructure(%+v) err = %v, wantErr = %v", tt.tokens, err, tt.wantErr)
			}
		})
	}
}

func TestChatGPTTokens_ValidateFresh(t *testing.T) {
	t.Parallel()
	now := time.Now()
	tests := map[string]struct {
		tokens  *ChatGPTTokens
		wantErr bool
	}{
		"future expiry accepted": {
			tokens:  &ChatGPTTokens{AccessToken: "a", RefreshToken: "r", AccountID: "acct", ExpiresAt: now.Add(1 * time.Hour)},
			wantErr: false,
		},
		"past expiry rejected": {
			tokens:  &ChatGPTTokens{AccessToken: "a", RefreshToken: "r", AccountID: "acct", ExpiresAt: now.Add(-1 * time.Hour)},
			wantErr: true,
		},
		"exact-now rejected": {
			tokens:  &ChatGPTTokens{AccessToken: "a", RefreshToken: "r", AccountID: "acct", ExpiresAt: now},
			wantErr: true,
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			err := tt.tokens.validateFresh()
			if (err != nil) != tt.wantErr {
				t.Errorf("validateFresh(%+v) err = %v, wantErr = %v", tt.tokens, err, tt.wantErr)
			}
		})
	}
}

func TestGetCredential_ExpiredChatGPTRow_IsReadable(t *testing.T) {
	t.Parallel()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New(): %v", err)
	}
	defer func() { _ = db.Close() }()

	enc := newTestEncryptor(t)
	expectTimezoneProbe(t, mock)
	store, err := NewCredentialStore(db, enc)
	if err != nil {
		t.Fatalf("NewCredentialStore: %v", err)
	}

	accessEnc, _ := enc.Encrypt("access")
	refreshEnc, _ := enc.Encrypt("refresh")
	idEnc, _ := enc.Encrypt("id")
	past := time.Now().Add(-1 * time.Hour).UTC()

	rows := sqlmock.NewRows([]string{
		"provider", "auth_mode", "api_key_encrypted",
		"oauth_access_token_encrypted", "oauth_refresh_token_encrypted",
		"oauth_id_token_encrypted", "oauth_account_id", "oauth_expires_at", "oauth_is_fedramp",
	}).AddRow("openai", "chatgpt", nil, accessEnc, refreshEnc, idEnc, "acct_42", past, false)

	mock.ExpectQuery("SELECT").WithArgs("g1").WillReturnRows(rows)

	got, err := store.GetCredential(context.Background(), "g1")
	if err != nil {
		t.Errorf("GetCredential with expired chatgpt row err = %v, want nil (refresh flow reads expired rows)", err)
	}
	if got == nil || got.AuthMode != AuthModeChatGPT {
		t.Errorf("GetCredential = %+v, want non-nil chatgpt credential", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

func TestNewCredentialStore_ProbesTimezoneInvariant(t *testing.T) {
	t.Parallel()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New(): %v", err)
	}
	defer func() { _ = db.Close() }()

	// Probe should SELECT a UTC datetime and read it back as time.Time with Location == UTC.
	utcNow := time.Now().UTC().Truncate(time.Second)
	mock.ExpectQuery("SELECT TIMESTAMP").WillReturnRows(
		sqlmock.NewRows([]string{"t"}).AddRow(utcNow),
	)

	enc := newTestEncryptor(t)
	_, err = NewCredentialStore(db, enc)
	if err != nil {
		t.Errorf("NewCredentialStore with UTC probe err = %v, want nil", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

func TestCredential_Getters(t *testing.T) {
	t.Parallel()
	tokens := &ChatGPTTokens{AccessToken: "a", RefreshToken: "r", AccountID: "acct", ExpiresAt: time.Now().Add(time.Hour)}
	apiCred, err := NewAPIKeyCredential("g", "openai", "sk-1")
	if err != nil {
		t.Fatalf("NewAPIKeyCredential: %v", err)
	}
	chatCred, err := NewChatGPTCredential("g", "openai", tokens)
	if err != nil {
		t.Fatalf("NewChatGPTCredential: %v", err)
	}

	tests := map[string]struct {
		cred        *Credential
		wantAPIKey  string
		wantChatGPT *ChatGPTTokens
	}{
		"api_key credential": {cred: apiCred, wantAPIKey: "sk-1", wantChatGPT: nil},
		"chatgpt credential": {cred: chatCred, wantAPIKey: "", wantChatGPT: tokens},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := tt.cred.APIKey(); got != tt.wantAPIKey {
				t.Errorf("%s: APIKey() = %q, want %q", name, got, tt.wantAPIKey)
			}
			if got := tt.cred.ChatGPT(); got != tt.wantChatGPT {
				t.Errorf("%s: ChatGPT() = %+v, want %+v", name, got, tt.wantChatGPT)
			}
		})
	}
}

