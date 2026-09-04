-- +goose Up
ALTER TABLE credentials DROP COLUMN oauth_token_encrypted;

-- +goose Down
ALTER TABLE credentials ADD COLUMN oauth_token_encrypted TEXT AFTER api_key_encrypted;
