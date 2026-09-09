-- +goose Up
ALTER TABLE credentials
    ADD COLUMN auth_mode ENUM('api_key','chatgpt') NOT NULL DEFAULT 'api_key' AFTER provider,
    ADD COLUMN oauth_access_token_encrypted TEXT NULL AFTER auth_mode,
    ADD COLUMN oauth_refresh_token_encrypted TEXT NULL AFTER oauth_access_token_encrypted,
    ADD COLUMN oauth_id_token_encrypted TEXT NULL AFTER oauth_refresh_token_encrypted,
    ADD COLUMN oauth_account_id VARCHAR(255) NULL AFTER oauth_id_token_encrypted,
    ADD COLUMN oauth_expires_at DATETIME NULL AFTER oauth_account_id,
    ADD COLUMN oauth_is_fedramp BOOLEAN NOT NULL DEFAULT FALSE AFTER oauth_expires_at,
    MODIFY COLUMN api_key_encrypted TEXT NULL;

-- +goose Down
-- Rollback keeps api_key_encrypted nullable because chatgpt-mode rows
-- written under the up migration have api_key_encrypted = NULL. Tightening
-- the column back to NOT NULL would fail with MySQL error 1138.
-- Operators who want the column NOT NULL again must first delete or
-- migrate chatgpt rows themselves, then run a separate ALTER.
ALTER TABLE credentials
    DROP COLUMN oauth_is_fedramp,
    DROP COLUMN oauth_expires_at,
    DROP COLUMN oauth_account_id,
    DROP COLUMN oauth_id_token_encrypted,
    DROP COLUMN oauth_refresh_token_encrypted,
    DROP COLUMN oauth_access_token_encrypted,
    DROP COLUMN auth_mode;
