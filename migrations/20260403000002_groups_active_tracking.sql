-- +goose Up
ALTER TABLE `groups`
    ADD COLUMN is_active BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN last_active_at DATETIME NULL;

-- +goose Down
ALTER TABLE `groups`
    DROP COLUMN is_active,
    DROP COLUMN last_active_at;
