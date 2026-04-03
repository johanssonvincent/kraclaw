package credproxy

import (
	"context"
	"database/sql"
	"fmt"
)

// Credential represents per-group API credentials.
type Credential struct {
	GroupJID string
	Provider string
	APIKey   string
}

// Validate checks that required Credential fields are set.
func (c *Credential) Validate() error {
	if c.GroupJID == "" {
		return fmt.Errorf("credential: group JID is required")
	}
	if c.Provider == "" {
		return fmt.Errorf("credential: provider is required")
	}
	if c.APIKey == "" {
		return fmt.Errorf("credential: API key is required")
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

// GetCredential retrieves and decrypts a credential for a group.
func (s *CredentialStore) GetCredential(ctx context.Context, groupJID string) (*Credential, error) {
	var provider, apiKeyEnc string

	err := s.db.QueryRowContext(ctx,
		"SELECT provider, api_key_encrypted FROM credentials WHERE group_jid = ?",
		groupJID,
	).Scan(&provider, &apiKeyEnc)
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

	return &Credential{
		GroupJID: groupJID,
		Provider: provider,
		APIKey:   apiKey,
	}, nil
}

// UpsertCredential encrypts and stores a credential for a group.
func (s *CredentialStore) UpsertCredential(ctx context.Context, cred *Credential) error {
	if err := cred.Validate(); err != nil {
		return err
	}

	apiKeyEnc, err := s.enc.Encrypt(cred.APIKey)
	if err != nil {
		return fmt.Errorf("encrypt api key: %w", err)
	}

	_, err = s.db.ExecContext(ctx,
		"REPLACE INTO credentials (group_jid, provider, api_key_encrypted) VALUES (?, ?, ?)",
		cred.GroupJID, cred.Provider, apiKeyEnc,
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
