ALTER TABLE credentials
    MODIFY COLUMN api_key_encrypted TEXT NOT NULL,
    DROP COLUMN oauth_is_fedramp,
    DROP COLUMN oauth_expires_at,
    DROP COLUMN oauth_account_id,
    DROP COLUMN oauth_id_token_encrypted,
    DROP COLUMN oauth_refresh_token_encrypted,
    DROP COLUMN oauth_access_token_encrypted,
    DROP COLUMN auth_mode;
