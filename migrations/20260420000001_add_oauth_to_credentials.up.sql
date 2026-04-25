ALTER TABLE credentials
    ADD COLUMN auth_mode ENUM('api_key','chatgpt') NOT NULL DEFAULT 'api_key' AFTER provider,
    ADD COLUMN oauth_access_token_encrypted TEXT NULL AFTER auth_mode,
    ADD COLUMN oauth_refresh_token_encrypted TEXT NULL AFTER oauth_access_token_encrypted,
    ADD COLUMN oauth_id_token_encrypted TEXT NULL AFTER oauth_refresh_token_encrypted,
    ADD COLUMN oauth_account_id VARCHAR(255) NULL AFTER oauth_id_token_encrypted,
    ADD COLUMN oauth_expires_at DATETIME NULL AFTER oauth_account_id,
    ADD COLUMN oauth_is_fedramp BOOLEAN NOT NULL DEFAULT FALSE AFTER oauth_expires_at,
    MODIFY COLUMN api_key_encrypted TEXT NULL;
