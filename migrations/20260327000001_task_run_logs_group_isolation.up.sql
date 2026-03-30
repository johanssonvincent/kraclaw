-- Step 1: Add group_folder column as nullable first (safe for existing rows)
ALTER TABLE task_run_logs
    ADD COLUMN group_folder VARCHAR(64) NULL AFTER task_id;

-- Step 2: Backfill group_folder from the related scheduled_tasks row
UPDATE task_run_logs trl
    INNER JOIN scheduled_tasks st ON st.id = trl.task_id
    SET trl.group_folder = st.group_folder;

-- Step 3: Make group_folder NOT NULL now that all rows are populated
ALTER TABLE task_run_logs
    MODIFY COLUMN group_folder VARCHAR(64) NOT NULL;

-- Step 4: Add composite index for efficient group-scoped queries
ALTER TABLE task_run_logs
    ADD INDEX idx_group_task_run (group_folder, task_id);

-- Step 5: Add FK from scheduled_tasks.group_folder to groups.folder
ALTER TABLE scheduled_tasks
    ADD CONSTRAINT fk_scheduled_tasks_group
    FOREIGN KEY (group_folder) REFERENCES `groups` (folder)
    ON DELETE CASCADE ON UPDATE CASCADE;

-- Step 6: Add FK from task_run_logs.task_id to scheduled_tasks.id
ALTER TABLE task_run_logs
    ADD CONSTRAINT fk_task_run_logs_task
    FOREIGN KEY (task_id) REFERENCES scheduled_tasks (id)
    ON DELETE CASCADE ON UPDATE CASCADE;
