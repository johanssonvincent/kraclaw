package credproxy

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// AuthMode names how a group authenticates to its LLM provider.
type AuthMode string

const (
	AuthModeAPIKey  AuthMode = "api_key"
	AuthModeChatGPT AuthMode = "chatgpt"
)

// ErrNoChatGPTCredential is returned when a ChatGPT-mode credential operation
// targets a group that has no such credential (already deleted or never
// existed). Callers can use errors.Is to route between "force re-auth" and
// "retry" paths.
var ErrNoChatGPTCredential = errors.New("no chatgpt credential for group")

// ChatGPTTokens is the OAuth credential bundle for ChatGPT-mode groups.
type ChatGPTTokens struct {
	AccessToken  string
	RefreshToken string
	IDToken      string
	AccountID    string
	ExpiresAt    time.Time
	IsFedRAMP    bool
}

// Credential represents per-group provider credentials.
//
// Exactly one of APIKey or ChatGPT is populated, matching AuthMode.
type Credential struct {
	GroupJID string
	Provider string
	AuthMode AuthMode

	APIKey  string
	ChatGPT *ChatGPTTokens
}

// Validate checks that required Credential fields are set for the active AuthMode.
func (c *Credential) Validate() error {
	if c.GroupJID == "" {
		return fmt.Errorf("credential: group JID is required")
	}
	if c.Provider == "" {
		return fmt.Errorf("credential: provider is required")
	}
	switch c.AuthMode {
	case "", AuthModeAPIKey:
		if c.APIKey == "" {
			return fmt.Errorf("credential: API key is required for api_key auth mode")
		}
		if c.ChatGPT != nil {
			return fmt.Errorf("credential: ChatGPT tokens must be nil for api_key auth mode")
		}
	case AuthModeChatGPT:
		if c.ChatGPT == nil {
			return fmt.Errorf("credential: ChatGPT tokens are required for chatgpt auth mode")
		}
		if c.APIKey != "" {
			return fmt.Errorf("credential: APIKey must be empty for chatgpt auth mode")
		}
		if err := c.ChatGPT.validate(); err != nil {
			return err
		}
	default:
		return fmt.Errorf("credential: unknown auth mode %q", c.AuthMode)
	}
	return nil
}

func (t *ChatGPTTokens) validate() error {
	if t.AccessToken == "" {
		return fmt.Errorf("chatgpt tokens: access token is required")
	}
	if t.RefreshToken == "" {
		return fmt.Errorf("chatgpt tokens: refresh token is required")
	}
	if t.AccountID == "" {
		return fmt.Errorf("chatgpt tokens: account id is required")
	}
	if t.ExpiresAt.IsZero() {
		return fmt.Errorf("chatgpt tokens: expires_at is required")
	}
	if !t.ExpiresAt.After(time.Now()) {
		return fmt.Errorf("chatgpt tokens: expires_at %s is not in the future", t.ExpiresAt.UTC().Format(time.RFC3339))
	}
	return nil
}

// CredentialStore manages per-group credentials in MySQL with at-rest encryption.
type CredentialStore struct {
	db  *sql.DB
	enc *Encryptor
}

// NewCredentialStore creates a credential store backed by MySQL.
func NewCredentialStore(db *sql.DB, enc *Encryptor) (*CredentialStore, error) {
	if db == nil {
		return nil, fmt.Errorf("credential store: database connection is required")
	}
	if enc == nil {
		return nil, fmt.Errorf("credential store: encryptor is required")
	}
	return &CredentialStore{db: db, enc: enc}, nil
}

const selectCredentialColumns = `
    provider,
    auth_mode,
    api_key_encrypted,
    oauth_access_token_encrypted,
    oauth_refresh_token_encrypted,
    oauth_id_token_encrypted,
    oauth_account_id,
    oauth_expires_at,
    oauth_is_fedramp
`

// GetCredential retrieves and decrypts a credential for a group.
func (s *CredentialStore) GetCredential(ctx context.Context, groupJID string) (*Credential, error) {
	var (
		provider     string
		authMode     string
		apiKeyEnc    sql.NullString
		accessEnc    sql.NullString
		refreshEnc   sql.NullString
		idEnc        sql.NullString
		accountID    sql.NullString
		expiresAt    sql.NullTime
		isFedRAMP    sql.NullBool
	)

	if err := s.db.QueryRowContext(ctx,
		"SELECT "+selectCredentialColumns+" FROM credentials WHERE group_jid = ?",
		groupJID,
	).Scan(&provider, &authMode, &apiKeyEnc, &accessEnc, &refreshEnc, &idEnc, &accountID, &expiresAt, &isFedRAMP); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("get credential: %w", err)
	}

	cred := &Credential{
		GroupJID: groupJID,
		Provider: provider,
		AuthMode: AuthMode(authMode),
	}

	switch cred.AuthMode {
	case "", AuthModeAPIKey:
		cred.AuthMode = AuthModeAPIKey
		if !apiKeyEnc.Valid {
			return nil, fmt.Errorf("get credential: api_key auth mode but api_key_encrypted is NULL")
		}
		apiKey, err := s.enc.Decrypt(apiKeyEnc.String)
		if err != nil {
			return nil, fmt.Errorf("decrypt api key: %w", err)
		}
		cred.APIKey = apiKey
	case AuthModeChatGPT:
		tokens, err := s.decryptChatGPTTokens(accessEnc, refreshEnc, idEnc, accountID, expiresAt, isFedRAMP)
		if err != nil {
			return nil, err
		}
		cred.ChatGPT = tokens
	default:
		return nil, fmt.Errorf("get credential: unknown auth mode %q", cred.AuthMode)
	}

	return cred, nil
}

func (s *CredentialStore) decryptChatGPTTokens(
	accessEnc, refreshEnc, idEnc sql.NullString,
	accountID sql.NullString,
	expiresAt sql.NullTime,
	isFedRAMP sql.NullBool,
) (*ChatGPTTokens, error) {
	if !accessEnc.Valid || !refreshEnc.Valid {
		return nil, fmt.Errorf("get credential: chatgpt auth mode but oauth tokens are missing")
	}
	if !accountID.Valid || accountID.String == "" {
		return nil, fmt.Errorf("get credential: chatgpt auth mode but oauth_account_id is missing")
	}
	if !expiresAt.Valid {
		return nil, fmt.Errorf("get credential: chatgpt auth mode but oauth_expires_at is NULL")
	}
	access, err := s.enc.Decrypt(accessEnc.String)
	if err != nil {
		return nil, fmt.Errorf("decrypt access token: %w", err)
	}
	refresh, err := s.enc.Decrypt(refreshEnc.String)
	if err != nil {
		return nil, fmt.Errorf("decrypt refresh token: %w", err)
	}
	var idToken string
	if idEnc.Valid && idEnc.String != "" {
		idToken, err = s.enc.Decrypt(idEnc.String)
		if err != nil {
			return nil, fmt.Errorf("decrypt id token: %w", err)
		}
	}
	return &ChatGPTTokens{
		AccessToken:  access,
		RefreshToken: refresh,
		IDToken:      idToken,
		AccountID:    accountID.String,
		ExpiresAt:    expiresAt.Time,
		IsFedRAMP:    isFedRAMP.Valid && isFedRAMP.Bool,
	}, nil
}

// UpsertCredential encrypts and stores a credential for a group, routed by AuthMode.
func (s *CredentialStore) UpsertCredential(ctx context.Context, cred *Credential) error {
	if cred.AuthMode == "" {
		return fmt.Errorf("upsert credential: auth mode is required")
	}
	if err := cred.Validate(); err != nil {
		return err
	}

	switch cred.AuthMode {
	case AuthModeAPIKey:
		apiKeyEnc, err := s.enc.Encrypt(cred.APIKey)
		if err != nil {
			return fmt.Errorf("encrypt api key: %w", err)
		}
		if _, err := s.db.ExecContext(ctx, `
            REPLACE INTO credentials (
                group_jid, provider, auth_mode, api_key_encrypted
            ) VALUES (?, ?, ?, ?)
        `, cred.GroupJID, cred.Provider, string(AuthModeAPIKey), apiKeyEnc); err != nil {
			return fmt.Errorf("upsert credential: %w", err)
		}
		return nil
	case AuthModeChatGPT:
		return s.upsertChatGPT(ctx, cred.GroupJID, cred.Provider, cred.ChatGPT)
	default:
		return fmt.Errorf("upsert credential: unknown auth mode %q", cred.AuthMode)
	}
}

// UpsertChatGPTCredential records ChatGPT OAuth tokens for the group and sets
// auth_mode to chatgpt. Existing rows for the group are replaced.
func (s *CredentialStore) UpsertChatGPTCredential(ctx context.Context, groupJID, provider string, tokens *ChatGPTTokens) error {
	cred := &Credential{
		GroupJID: groupJID,
		Provider: provider,
		AuthMode: AuthModeChatGPT,
		ChatGPT:  tokens,
	}
	if err := cred.Validate(); err != nil {
		return err
	}
	return s.upsertChatGPT(ctx, groupJID, provider, tokens)
}

func (s *CredentialStore) upsertChatGPT(ctx context.Context, groupJID, provider string, tokens *ChatGPTTokens) error {
	accessEnc, err := s.enc.Encrypt(tokens.AccessToken)
	if err != nil {
		return fmt.Errorf("encrypt access token: %w", err)
	}
	refreshEnc, err := s.enc.Encrypt(tokens.RefreshToken)
	if err != nil {
		return fmt.Errorf("encrypt refresh token: %w", err)
	}
	var idEnc sql.NullString
	if tokens.IDToken != "" {
		v, err := s.enc.Encrypt(tokens.IDToken)
		if err != nil {
			return fmt.Errorf("encrypt id token: %w", err)
		}
		idEnc = sql.NullString{String: v, Valid: true}
	}
	if _, err := s.db.ExecContext(ctx, `
        REPLACE INTO credentials (
            group_jid, provider, auth_mode, api_key_encrypted,
            oauth_access_token_encrypted, oauth_refresh_token_encrypted,
            oauth_id_token_encrypted, oauth_account_id, oauth_expires_at, oauth_is_fedramp
        ) VALUES (?, ?, ?, NULL, ?, ?, ?, ?, ?, ?)
    `, groupJID, provider, string(AuthModeChatGPT),
		accessEnc, refreshEnc, idEnc, tokens.AccountID, tokens.ExpiresAt.UTC(), tokens.IsFedRAMP,
	); err != nil {
		return fmt.Errorf("upsert chatgpt credential: %w", err)
	}
	return nil
}

// RefreshChatGPTTokens atomically replaces the OAuth token triple, expiry, account ID,
// and FedRAMP flag for an existing chatgpt-mode credential. A refresh response that
// re-issues the id_token with new claims is honoured.
func (s *CredentialStore) RefreshChatGPTTokens(ctx context.Context, groupJID string, tokens *ChatGPTTokens) error {
	if groupJID == "" {
		return fmt.Errorf("refresh chatgpt tokens: group JID is required")
	}
	if err := tokens.validate(); err != nil {
		return err
	}
	accessEnc, err := s.enc.Encrypt(tokens.AccessToken)
	if err != nil {
		return fmt.Errorf("encrypt access token: %w", err)
	}
	refreshEnc, err := s.enc.Encrypt(tokens.RefreshToken)
	if err != nil {
		return fmt.Errorf("encrypt refresh token: %w", err)
	}
	var idEnc sql.NullString
	if tokens.IDToken != "" {
		v, err := s.enc.Encrypt(tokens.IDToken)
		if err != nil {
			return fmt.Errorf("encrypt id token: %w", err)
		}
		idEnc = sql.NullString{String: v, Valid: true}
	}
	res, err := s.db.ExecContext(ctx, `
        UPDATE credentials SET
            oauth_access_token_encrypted = ?,
            oauth_refresh_token_encrypted = ?,
            oauth_id_token_encrypted = ?,
            oauth_account_id = ?,
            oauth_expires_at = ?,
            oauth_is_fedramp = ?
        WHERE group_jid = ? AND auth_mode = ?
    `, accessEnc, refreshEnc, idEnc, tokens.AccountID, tokens.ExpiresAt.UTC(), tokens.IsFedRAMP,
		groupJID, string(AuthModeChatGPT),
	)
	if err != nil {
		return fmt.Errorf("refresh chatgpt tokens: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("refresh chatgpt tokens: rows affected: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("refresh chatgpt tokens for group %q: %w", groupJID, ErrNoChatGPTCredential)
	}
	return nil
}

// DeleteCredential removes a credential for a group.
func (s *CredentialStore) DeleteCredential(ctx context.Context, groupJID string) error {
	if _, err := s.db.ExecContext(ctx,
		"DELETE FROM credentials WHERE group_jid = ?",
		groupJID,
	); err != nil {
		return fmt.Errorf("delete credential: %w", err)
	}
	return nil
}
