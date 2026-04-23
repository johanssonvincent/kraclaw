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

// Valid reports whether m is a known, non-empty auth mode.
// Empty string is NOT valid — legacy row reads coerce "" to AuthModeAPIKey
// inside GetCredential before this check applies.
func (m AuthMode) Valid() bool {
	switch m {
	case AuthModeAPIKey, AuthModeChatGPT:
		return true
	default:
		return false
	}
}

// ErrNoChatGPTCredential is returned when a ChatGPT-mode credential operation
// targets a group that has no such credential (already deleted or never
// existed). Callers can use errors.Is to route between "force re-auth" and
// "retry" paths.
var ErrNoChatGPTCredential = errors.New("no chatgpt credential for group")

// encrypter is the encryption contract CredentialStore relies on. It exists so
// tests can inject fakes that return errors from Encrypt/Decrypt — the real
// *Encryptor satisfies this interface.
type encrypter interface {
	Encrypt(plaintext string) (string, error)
	Decrypt(ciphertext string) (string, error)
}

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
// Construct via NewAPIKeyCredential or NewChatGPTCredential. The payload is
// intentionally unexported so "both APIKey and ChatGPT populated" is
// unrepresentable at the type level — the constructors enforce the xor.
type Credential struct {
	GroupJID string
	Provider string
	AuthMode AuthMode

	apiKey  string
	chatGPT *ChatGPTTokens
}

// APIKey returns the API key for an api_key-mode credential, or "" otherwise.
func (c *Credential) APIKey() string {
	if c == nil {
		return ""
	}
	return c.apiKey
}

// ChatGPT returns the OAuth token bundle for a chatgpt-mode credential, or nil otherwise.
func (c *Credential) ChatGPT() *ChatGPTTokens {
	if c == nil {
		return nil
	}
	return c.chatGPT
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
		if c.apiKey == "" {
			return fmt.Errorf("credential: API key is required for api_key auth mode")
		}
		if c.chatGPT != nil {
			return fmt.Errorf("credential: ChatGPT tokens must be nil for api_key auth mode")
		}
	case AuthModeChatGPT:
		if c.chatGPT == nil {
			return fmt.Errorf("credential: ChatGPT tokens are required for chatgpt auth mode")
		}
		if c.apiKey != "" {
			return fmt.Errorf("credential: APIKey must be empty for chatgpt auth mode")
		}
		if err := c.chatGPT.validate(); err != nil {
			return err
		}
	default:
		return fmt.Errorf("credential: unknown auth mode %q", c.AuthMode)
	}
	return nil
}

// validateForRead validates structural invariants only — it does NOT reject
// expired chatgpt tokens. An expired access token is the normal precondition
// for a refresh, so reads must succeed for the refresh flow to work.
func (c *Credential) validateForRead() error {
	if c.GroupJID == "" {
		return fmt.Errorf("credential: group JID is required")
	}
	if c.Provider == "" {
		return fmt.Errorf("credential: provider is required")
	}
	switch c.AuthMode {
	case "", AuthModeAPIKey:
		if c.apiKey == "" {
			return fmt.Errorf("credential: API key is required for api_key auth mode")
		}
		if c.chatGPT != nil {
			return fmt.Errorf("credential: ChatGPT tokens must be nil for api_key auth mode")
		}
	case AuthModeChatGPT:
		if c.chatGPT == nil {
			return fmt.Errorf("credential: ChatGPT tokens are required for chatgpt auth mode")
		}
		if c.apiKey != "" {
			return fmt.Errorf("credential: APIKey must be empty for chatgpt auth mode")
		}
		if err := c.chatGPT.validateStructure(); err != nil {
			return err
		}
	default:
		return fmt.Errorf("credential: unknown auth mode %q", c.AuthMode)
	}
	return nil
}

// NewAPIKeyCredential constructs and validates an api_key credential.
func NewAPIKeyCredential(groupJID, provider, apiKey string) (*Credential, error) {
	cred := &Credential{
		GroupJID: groupJID,
		Provider: provider,
		AuthMode: AuthModeAPIKey,
		apiKey:   apiKey,
	}
	if err := cred.Validate(); err != nil {
		return nil, err
	}
	return cred, nil
}

// NewChatGPTCredential constructs and validates a chatgpt credential.
func NewChatGPTCredential(groupJID, provider string, tokens *ChatGPTTokens) (*Credential, error) {
	cred := &Credential{
		GroupJID: groupJID,
		Provider: provider,
		AuthMode: AuthModeChatGPT,
		chatGPT:  tokens,
	}
	if err := cred.Validate(); err != nil {
		return nil, err
	}
	return cred, nil
}

func (t *ChatGPTTokens) validateStructure() error {
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
	return nil
}

func (t *ChatGPTTokens) validateFresh() error {
	if !t.ExpiresAt.After(time.Now()) {
		return fmt.Errorf("chatgpt tokens: expires_at %s is not in the future", t.ExpiresAt.UTC().Format(time.RFC3339))
	}
	return nil
}

// validate keeps the pre-split contract for the construction path:
// structure + freshness. Callers that read persisted rows should call
// validateStructure directly — an expired access token is the normal
// trigger for the refresh flow, not an error.
func (t *ChatGPTTokens) validate() error {
	if err := t.validateStructure(); err != nil {
		return err
	}
	return t.validateFresh()
}

// CredentialStore manages per-group credentials in MySQL with at-rest encryption.
type CredentialStore struct {
	db  *sql.DB
	enc encrypter
}

// NewCredentialStore creates a credential store backed by MySQL.
//
// The DSN must include loc=UTC&parseTime=true. oauth_expires_at is stored as
// DATETIME (timezone-naive at the SQL layer); freshness comparisons rely on
// round-tripping through UTC. A Local-configured DSN silently shifts stored
// expiries by the server's offset, which can mis-classify fresh vs expired
// tokens by up to 24 hours. NewCredentialStore issues one probe query to
// catch the most common misconfiguration early.
func NewCredentialStore(db *sql.DB, enc *Encryptor) (*CredentialStore, error) {
	if db == nil {
		return nil, fmt.Errorf("credential store: database connection is required")
	}
	if enc == nil {
		return nil, fmt.Errorf("credential store: encryptor is required")
	}
	s := &CredentialStore{db: db, enc: enc}
	if err := s.probeTimezone(); err != nil {
		return nil, fmt.Errorf("credential store: timezone probe: %w", err)
	}
	return s, nil
}

// probeTimezone verifies the DB driver returns timestamps in UTC. Any
// non-UTC location signals a DSN misconfiguration that would silently
// corrupt oauth_expires_at comparisons.
func (s *CredentialStore) probeTimezone() error {
	var got time.Time
	if err := s.db.QueryRow("SELECT TIMESTAMP('2000-01-01 00:00:00')").Scan(&got); err != nil {
		// If the driver doesn't support this literal, fall back silently —
		// the DSN contract is documented and the probe is best-effort.
		return nil
	}
	if loc := got.Location(); loc != time.UTC {
		return fmt.Errorf("DB driver returned time in %q, want UTC (set loc=UTC&parseTime=true on DSN)", loc.String())
	}
	return nil
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
		if errors.Is(err, sql.ErrNoRows) {
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
		cred.apiKey = apiKey
	case AuthModeChatGPT:
		tokens, err := s.decryptChatGPTTokens(groupJID, accessEnc, refreshEnc, idEnc, accountID, expiresAt, isFedRAMP)
		if err != nil {
			return nil, err
		}
		cred.chatGPT = tokens
	default:
		return nil, fmt.Errorf("get credential: unknown auth mode %q", cred.AuthMode)
	}

	if err := cred.validateForRead(); err != nil {
		return nil, fmt.Errorf("get credential: %w", err)
	}
	return cred, nil
}

func (s *CredentialStore) decryptChatGPTTokens(
	groupJID string,
	accessEnc, refreshEnc, idEnc sql.NullString,
	accountID sql.NullString,
	expiresAt sql.NullTime,
	isFedRAMP sql.NullBool,
) (*ChatGPTTokens, error) {
	if !accessEnc.Valid {
		return nil, fmt.Errorf("get credential for group %q: oauth access token is NULL", groupJID)
	}
	if !refreshEnc.Valid {
		return nil, fmt.Errorf("get credential for group %q: oauth refresh token is NULL", groupJID)
	}
	if !accountID.Valid || accountID.String == "" {
		return nil, fmt.Errorf("get credential for group %q: oauth account id is NULL", groupJID)
	}
	if !expiresAt.Valid {
		return nil, fmt.Errorf("get credential for group %q: oauth expires_at is NULL", groupJID)
	}
	access, err := s.enc.Decrypt(accessEnc.String)
	if err != nil {
		return nil, fmt.Errorf("decrypt access token for group %q: %w", groupJID, err)
	}
	refresh, err := s.enc.Decrypt(refreshEnc.String)
	if err != nil {
		return nil, fmt.Errorf("decrypt refresh token for group %q: %w", groupJID, err)
	}
	var idToken string
	if idEnc.Valid && idEnc.String != "" {
		idToken, err = s.enc.Decrypt(idEnc.String)
		if err != nil {
			return nil, fmt.Errorf("decrypt id token for group %q: %w", groupJID, err)
		}
	}
	// oauth_is_fedramp is declared NOT NULL DEFAULT FALSE in the up migration,
	// so isFedRAMP.Valid is always true. We read through sql.NullBool only
	// because the scan target must accept the DB boolean type; the .Bool value
	// is the source of truth.
	return &ChatGPTTokens{
		AccessToken:  access,
		RefreshToken: refresh,
		IDToken:      idToken,
		AccountID:    accountID.String,
		ExpiresAt:    expiresAt.Time,
		IsFedRAMP:    isFedRAMP.Bool,
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
		apiKeyEnc, err := s.enc.Encrypt(cred.apiKey)
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
		return s.upsertChatGPT(ctx, cred.GroupJID, cred.Provider, cred.chatGPT)
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
		chatGPT:  tokens,
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
	if tokens == nil {
		return fmt.Errorf("refresh chatgpt tokens: tokens bundle is required")
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

