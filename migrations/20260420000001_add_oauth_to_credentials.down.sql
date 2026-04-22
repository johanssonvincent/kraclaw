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
