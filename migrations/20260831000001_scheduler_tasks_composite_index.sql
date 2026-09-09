-- +goose Up
CREATE INDEX idx_status_next_run ON scheduled_tasks (status, next_run);

-- +goose Down
ALTER TABLE scheduled_tasks DROP INDEX idx_status_next_run;
