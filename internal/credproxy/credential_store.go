package credproxy

import (
	"context"
	"database/sql"
	"fmt"
)

// Credential represents per-group API credentials.
type Credential struct {
	GroupJID   string
	Provider   string
	APIKey     string
	OAuthToken string
}

// CredentialStore manages per-group credentials in MySQL with at-rest encryption.
type CredentialStore struct {
	db  *sql.DB
	enc *Encryptor
}

// NewCredentialStore creates a credential store backed by MySQL.
func NewCredentialStore(db *sql.DB, enc *Encryptor) *CredentialStore {
	return &CredentialStore{db: db, enc: enc}
}

// GetCredential retrieves and decrypts a credential for a group.
func (s *CredentialStore) GetCredential(ctx context.Context, groupJID string) (*Credential, error) {
	var provider, apiKeyEnc string
	var oauthTokenEnc sql.NullString

	err := s.db.QueryRowContext(ctx,
		"SELECT provider, api_key_encrypted, oauth_token_encrypted FROM credentials WHERE group_jid = ?",
		groupJID,
	).Scan(&provider, &apiKeyEnc, &oauthTokenEnc)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get credential: %w", err)
	}

	apiKey, err := s.enc.Decrypt(apiKeyEnc)
	if err != nil {
		return nil, fmt.Errorf("decrypt api key: %w", err)
	}

	cred := &Credential{
		GroupJID: groupJID,
		Provider: provider,
		APIKey:   apiKey,
	}

	if oauthTokenEnc.Valid && oauthTokenEnc.String != "" {
		token, err := s.enc.Decrypt(oauthTokenEnc.String)
		if err != nil {
			return nil, fmt.Errorf("decrypt oauth token: %w", err)
		}
		cred.OAuthToken = token
	}

	return cred, nil
}

// UpsertCredential encrypts and stores a credential for a group.
func (s *CredentialStore) UpsertCredential(ctx context.Context, cred *Credential) error {
	apiKeyEnc, err := s.enc.Encrypt(cred.APIKey)
	if err != nil {
		return fmt.Errorf("encrypt api key: %w", err)
	}

	var oauthTokenEnc sql.NullString
	if cred.OAuthToken != "" {
		enc, err := s.enc.Encrypt(cred.OAuthToken)
		if err != nil {
			return fmt.Errorf("encrypt oauth token: %w", err)
		}
		oauthTokenEnc = sql.NullString{String: enc, Valid: true}
	}

	_, err = s.db.ExecContext(ctx,
		"REPLACE INTO credentials (group_jid, provider, api_key_encrypted, oauth_token_encrypted) VALUES (?, ?, ?, ?)",
		cred.GroupJID, cred.Provider, apiKeyEnc, oauthTokenEnc,
	)
	if err != nil {
		return fmt.Errorf("upsert credential: %w", err)
	}
	return nil
}

// DeleteCredential removes a credential for a group.
func (s *CredentialStore) DeleteCredential(ctx context.Context, groupJID string) error {
	_, err := s.db.ExecContext(ctx,
		"DELETE FROM credentials WHERE group_jid = ?",
		groupJID,
	)
	if err != nil {
		return fmt.Errorf("delete credential: %w", err)
	}
	return nil
}
