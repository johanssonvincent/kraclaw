-- Reverse in opposite order of up migration
ALTER TABLE task_run_logs
    DROP FOREIGN KEY fk_task_run_logs_task;

ALTER TABLE scheduled_tasks
    DROP FOREIGN KEY fk_scheduled_tasks_group;

ALTER TABLE task_run_logs
    DROP INDEX idx_group_task_run;

ALTER TABLE task_run_logs
    DROP COLUMN group_folder;
