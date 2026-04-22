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
				t.Errorf("Validate(%+v) error = %v, wantErr %v", tt.cred, err, tt.wantErr)
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
			if cred.APIKey != tt.wantKey {
				t.Errorf("GetCredential(%q).APIKey = %q, want %q", tt.groupJID, cred.APIKey, tt.wantKey)
			}
			if (cred.ChatGPT != nil) != tt.wantChatGPT {
				t.Errorf("GetCredential(%q) has ChatGPT = %v, want %v", tt.groupJID, cred.ChatGPT != nil, tt.wantChatGPT)
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
	if got == nil || got.ChatGPT == nil {
		t.Fatalf("GetCredential returned incomplete credential: %+v", got)
	}

	checks := map[string]struct{ got, want string }{
		"access token":  {got.ChatGPT.AccessToken, "access-token-XYZ"},
		"refresh token": {got.ChatGPT.RefreshToken, "refresh-token-ABC"},
		"id token":      {got.ChatGPT.IDToken, "id-token-PQR"},
		"account id":    {got.ChatGPT.AccountID, "acct_42"},
	}
	for name, c := range checks {
		if c.got != c.want {
			t.Errorf("roundtrip %s = %q, want %q", name, c.got, c.want)
		}
	}
	if !got.ChatGPT.IsFedRAMP {
		t.Errorf("roundtrip IsFedRAMP = false, want true")
	}
	if !got.ChatGPT.ExpiresAt.Equal(expiresAt) {
		t.Errorf("roundtrip ExpiresAt = %v, want %v", got.ChatGPT.ExpiresAt, expiresAt)
	}
	if got.APIKey != "" {
		t.Errorf("roundtrip APIKey = %q, want empty", got.APIKey)
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
