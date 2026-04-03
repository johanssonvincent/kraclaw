package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database/mysql"
	"github.com/golang-migrate/migrate/v4/source/iofs"

	"github.com/johanssonvincent/kraclaw/migrations"
)

// MySQLStore implements the Store interface using MySQL.
type MySQLStore struct {
	db *sql.DB
}

// dirtyMigrationError signals a dirty migration state that must not be retried.
type dirtyMigrationError struct {
	msg string
}

func (e *dirtyMigrationError) Error() string { return e.msg }

// retryWithBackoff retries fn up to attempts times using exponential backoff
// starting at baseDelay (capped at 30 seconds). The operation name is used for
// structured log output on each retry. Returns the last error wrapped with the
// attempt count if all attempts are exhausted.
func retryWithBackoff(attempts int, baseDelay time.Duration, operation string, fn func() error) error {
	var err error
	for i := range attempts {
		err = fn()
		if err == nil {
			return nil
		}
		var nonRetryable *dirtyMigrationError
		if errors.As(err, &nonRetryable) {
			return err
		}
		if i == attempts-1 {
			break
		}
		delay := baseDelay * (1 << i)
		if delay > 30*time.Second {
			delay = 30 * time.Second
		}
		slog.Warn("retrying operation", "operation", operation, "attempt", i+1, "backoff", delay, "error", err)
		time.Sleep(delay)
	}
	return fmt.Errorf("%s failed after %d attempts: %w", operation, attempts, err)
}

// NewMySQLStore creates a new MySQL-backed store and runs migrations.
func NewMySQLStore(dsn string, maxOpen, maxIdle int, connMaxLifetime time.Duration) (*MySQLStore, error) {
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, fmt.Errorf("open mysql: %w", err)
	}

	db.SetMaxOpenConns(maxOpen)
	db.SetMaxIdleConns(maxIdle)
	db.SetConnMaxLifetime(connMaxLifetime)

	if err := retryWithBackoff(5, 1*time.Second, "ping mysql", func() error {
		return db.Ping()
	}); err != nil {
		if cerr := db.Close(); cerr != nil {
			slog.Warn("close db on ping failure", "error", cerr)
		}
		return nil, err
	}

	if err := runMigrations(dsn); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("run migrations: %w", err)
	}

	slog.Info("mysql store initialised")
	return &MySQLStore{db: db}, nil
}

// newMySQLStoreFromDB wraps an existing *sql.DB (used by tests).
func newMySQLStoreFromDB(db *sql.DB) *MySQLStore {
	return &MySQLStore{db: db}
}

// DB returns the underlying *sql.DB connection.
func (s *MySQLStore) DB() *sql.DB {
	return s.db
}

func runMigrations(dsn string) error {
	// golang-migrate requires multiStatements for multi-statement migration files.
	if strings.Contains(dsn, "?") {
		dsn += "&multiStatements=true"
	} else {
		dsn += "?multiStatements=true"
	}

	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return fmt.Errorf("open migration db: %w", err)
	}
	defer func() { _ = db.Close() }()

	src, err := iofs.New(migrations.FS, ".")
	if err != nil {
		return fmt.Errorf("create migration source: %w", err)
	}

	driver, err := mysql.WithInstance(db, &mysql.Config{})
	if err != nil {
		return fmt.Errorf("create migrate driver: %w", err)
	}

	m, err := migrate.NewWithInstance("iofs", src, "mysql", driver)
	if err != nil {
		return fmt.Errorf("create migrate instance: %w", err)
	}

	if err := retryWithBackoff(5, 1*time.Second, "migrate up", func() error {
		err := m.Up()
		if err == nil || errors.Is(err, migrate.ErrNoChange) {
			return nil
		}
		_, dirty, verr := m.Version()
		if verr != nil {
			slog.Error("failed to read migration version after migration error", "error", verr, "migration_error", err)
			return &dirtyMigrationError{msg: "migration failed and version check also failed — manual intervention required"}
		}
		if dirty {
			// Dirty migration state requires manual intervention — do not retry.
			return &dirtyMigrationError{msg: "dirty migration state detected at startup — manual intervention required: inspect schema and run 'migrate force <version>'"}
		}
		return err
	}); err != nil {
		return fmt.Errorf("migrate up: %w", err)
	}

	return nil
}

// Close closes the underlying database connection.
func (s *MySQLStore) Close() error {
	return s.db.Close()
}

// ---------------------------------------------------------------------------
// GroupStore
// ---------------------------------------------------------------------------

func (s *MySQLStore) GetGroup(ctx context.Context, jid string) (*Group, error) {
	row := s.db.QueryRowContext(ctx,
		"SELECT jid, name, folder, trigger_pattern, is_main, requires_trigger, container_config, added_at FROM `groups` WHERE jid = ?",
		jid,
	)
	return scanGroup(row)
}

func (s *MySQLStore) GetGroupByFolder(ctx context.Context, folder string) (*Group, error) {
	row := s.db.QueryRowContext(ctx,
		"SELECT jid, name, folder, trigger_pattern, is_main, requires_trigger, container_config, added_at FROM `groups` WHERE folder = ?",
		folder,
	)
	return scanGroup(row)
}

func (s *MySQLStore) ListGroups(ctx context.Context) ([]Group, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT jid, name, folder, trigger_pattern, is_main, requires_trigger, container_config, added_at FROM `groups`",
	)
	if err != nil {
		return nil, fmt.Errorf("list groups: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var groups []Group
	for rows.Next() {
		g, err := scanGroupRows(rows)
		if err != nil {
			return nil, err
		}
		groups = append(groups, *g)
	}
	return groups, rows.Err()
}

func (s *MySQLStore) UpsertGroup(ctx context.Context, g *Group) error {
	ccJSON, err := ContainerConfigJSON(g.ContainerConfig)
	if err != nil {
		return fmt.Errorf("marshal container config: %w", err)
	}

	_, err = s.db.ExecContext(ctx,
		`REPLACE INTO `+"`groups`"+` (jid, name, folder, trigger_pattern, is_main, requires_trigger, container_config, added_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		g.JID, g.Name, g.Folder, g.TriggerPattern, g.IsMain, g.RequiresTrigger, ccJSON, g.AddedAt,
	)
	if err != nil {
		return fmt.Errorf("upsert group: %w", err)
	}
	return nil
}

func (s *MySQLStore) DeleteGroup(ctx context.Context, jid string) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM `groups` WHERE jid = ?", jid)
	if err != nil {
		return fmt.Errorf("delete group: %w", err)
	}
	return nil
}

type scanner interface {
	Scan(dest ...any) error
}

func scanGroup(row scanner) (*Group, error) {
	var g Group
	var ccRaw []byte
	err := row.Scan(&g.JID, &g.Name, &g.Folder, &g.TriggerPattern, &g.IsMain, &g.RequiresTrigger, &ccRaw, &g.AddedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("scan group: %w", err)
	}
	g.ContainerConfig, err = ParseContainerConfig(ccRaw)
	if err != nil {
		return nil, fmt.Errorf("parse container config: %w", err)
	}
	return &g, nil
}

func scanGroupRows(rows *sql.Rows) (*Group, error) {
	var g Group
	var ccRaw []byte
	err := rows.Scan(&g.JID, &g.Name, &g.Folder, &g.TriggerPattern, &g.IsMain, &g.RequiresTrigger, &ccRaw, &g.AddedAt)
	if err != nil {
		return nil, fmt.Errorf("scan group row: %w", err)
	}
	g.ContainerConfig, err = ParseContainerConfig(ccRaw)
	if err != nil {
		return nil, fmt.Errorf("parse container config: %w", err)
	}
	return &g, nil
}

// ---------------------------------------------------------------------------
// MessageStore
// ---------------------------------------------------------------------------

func (s *MySQLStore) StoreMessage(ctx context.Context, msg *Message) error {
	_, err := s.db.ExecContext(ctx,
		`REPLACE INTO messages (id, chat_jid, sender, sender_name, content, timestamp, is_from_me, is_bot_message)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		msg.ID, msg.ChatJID, msg.Sender, msg.SenderName, msg.Content, msg.Timestamp, msg.IsFromMe, msg.IsBotMessage,
	)
	if err != nil {
		return fmt.Errorf("store message: %w", err)
	}
	return nil
}

func (s *MySQLStore) StoreBatch(ctx context.Context, msgs []Message) error {
	if len(msgs) == 0 {
		return nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	stmt, err := tx.PrepareContext(ctx,
		`REPLACE INTO messages (id, chat_jid, sender, sender_name, content, timestamp, is_from_me, is_bot_message)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
	)
	if err != nil {
		return fmt.Errorf("prepare batch insert: %w", err)
	}
	defer func() { _ = stmt.Close() }()

	for i := range msgs {
		m := &msgs[i]
		if _, err := stmt.ExecContext(ctx, m.ID, m.ChatJID, m.Sender, m.SenderName, m.Content, m.Timestamp, m.IsFromMe, m.IsBotMessage); err != nil {
			return fmt.Errorf("batch insert message %d: %w", i, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit batch: %w", err)
	}
	return nil
}

func (s *MySQLStore) GetNewMessages(ctx context.Context, jids []string, since time.Time, limit int) ([]Message, error) {
	if len(jids) == 0 {
		return nil, nil
	}

	placeholders := make([]string, len(jids))
	args := make([]any, 0, len(jids)+2)
	args = append(args, since)
	for i, jid := range jids {
		placeholders[i] = "?"
		args = append(args, jid)
	}
	args = append(args, limit)

	query := fmt.Sprintf(`
		SELECT * FROM (
			SELECT id, chat_jid, sender, sender_name, content, timestamp, is_from_me, is_bot_message
			FROM messages
			WHERE timestamp > ? AND chat_jid IN (%s)
			  AND is_bot_message = 0
			  AND content != '' AND content IS NOT NULL
			ORDER BY timestamp DESC
			LIMIT ?
		) sub ORDER BY timestamp`,
		strings.Join(placeholders, ","),
	)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("get new messages: %w", err)
	}
	defer func() { _ = rows.Close() }()

	return scanMessages(rows)
}

func (s *MySQLStore) GetMessagesSince(ctx context.Context, chatJID string, since time.Time, limit int) ([]Message, error) {
	query := `
		SELECT * FROM (
			SELECT id, chat_jid, sender, sender_name, content, timestamp, is_from_me, is_bot_message
			FROM messages
			WHERE chat_jid = ? AND timestamp > ?
			  AND is_bot_message = 0
			  AND content != '' AND content IS NOT NULL
			ORDER BY timestamp DESC
			LIMIT ?
		) sub ORDER BY timestamp`

	rows, err := s.db.QueryContext(ctx, query, chatJID, since, limit)
	if err != nil {
		return nil, fmt.Errorf("get messages since: %w", err)
	}
	defer func() { _ = rows.Close() }()

	return scanMessages(rows)
}

func scanMessages(rows *sql.Rows) ([]Message, error) {
	var msgs []Message
	for rows.Next() {
		var m Message
		if err := rows.Scan(&m.ID, &m.ChatJID, &m.Sender, &m.SenderName, &m.Content, &m.Timestamp, &m.IsFromMe, &m.IsBotMessage); err != nil {
			return nil, fmt.Errorf("scan message: %w", err)
		}
		msgs = append(msgs, m)
	}
	return msgs, rows.Err()
}

// ---------------------------------------------------------------------------
// ChatStore
// ---------------------------------------------------------------------------

func (s *MySQLStore) UpsertChat(ctx context.Context, c *Chat) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO chats (jid, name, channel, is_group, last_message_time)
		 VALUES (?, ?, ?, ?, ?)
		 ON DUPLICATE KEY UPDATE
		   name = VALUES(name),
		   last_message_time = GREATEST(last_message_time, VALUES(last_message_time)),
		   channel = COALESCE(VALUES(channel), channel),
		   is_group = COALESCE(VALUES(is_group), is_group)`,
		c.JID, c.Name, c.Channel, c.IsGroup, c.LastMessageTime,
	)
	if err != nil {
		return fmt.Errorf("upsert chat: %w", err)
	}
	return nil
}

func (s *MySQLStore) GetChat(ctx context.Context, jid string) (*Chat, error) {
	var c Chat
	err := s.db.QueryRowContext(ctx,
		"SELECT jid, name, channel, is_group, last_message_time FROM chats WHERE jid = ?",
		jid,
	).Scan(&c.JID, &c.Name, &c.Channel, &c.IsGroup, &c.LastMessageTime)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get chat: %w", err)
	}
	return &c, nil
}

func (s *MySQLStore) ListChats(ctx context.Context) ([]Chat, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT jid, name, channel, is_group, last_message_time FROM chats ORDER BY last_message_time DESC",
	)
	if err != nil {
		return nil, fmt.Errorf("list chats: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var chats []Chat
	for rows.Next() {
		var c Chat
		if err := rows.Scan(&c.JID, &c.Name, &c.Channel, &c.IsGroup, &c.LastMessageTime); err != nil {
			return nil, fmt.Errorf("scan chat: %w", err)
		}
		chats = append(chats, c)
	}
	return chats, rows.Err()
}

// ---------------------------------------------------------------------------
// TaskStore
// ---------------------------------------------------------------------------

func (s *MySQLStore) CreateTask(ctx context.Context, task *ScheduledTask) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO scheduled_tasks (id, group_folder, chat_jid, prompt, schedule_type, schedule_value, context_mode, next_run, status, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		task.ID, task.GroupFolder, task.ChatJID, task.Prompt,
		task.ScheduleType, task.ScheduleValue, task.ContextMode,
		task.NextRun, task.Status, task.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("create task: %w", err)
	}
	return nil
}

func (s *MySQLStore) GetTask(ctx context.Context, id, groupFolder string) (*ScheduledTask, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, group_folder, chat_jid, prompt, schedule_type, schedule_value, context_mode,
		        next_run, last_run, last_result, status, created_at
		 FROM scheduled_tasks WHERE id = ? AND group_folder = ?`, id, groupFolder,
	)
	return scanTask(row)
}

func (s *MySQLStore) ListTasks(ctx context.Context) ([]ScheduledTask, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, group_folder, chat_jid, prompt, schedule_type, schedule_value, context_mode,
		        next_run, last_run, last_result, status, created_at
		 FROM scheduled_tasks ORDER BY created_at DESC`,
	)
	if err != nil {
		return nil, fmt.Errorf("list tasks: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return scanTasks(rows)
}

func (s *MySQLStore) ListTasksByGroup(ctx context.Context, groupFolder string) ([]ScheduledTask, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, group_folder, chat_jid, prompt, schedule_type, schedule_value, context_mode,
		        next_run, last_run, last_result, status, created_at
		 FROM scheduled_tasks WHERE group_folder = ? ORDER BY created_at DESC`, groupFolder,
	)
	if err != nil {
		return nil, fmt.Errorf("list tasks by group: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return scanTasks(rows)
}

func (s *MySQLStore) UpdateTask(ctx context.Context, task *ScheduledTask) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE scheduled_tasks SET
		   prompt = ?, schedule_type = ?, schedule_value = ?, context_mode = ?,
		   next_run = ?, last_run = ?, last_result = ?, status = ?
		 WHERE id = ? AND group_folder = ?`,
		task.Prompt, task.ScheduleType, task.ScheduleValue, task.ContextMode,
		task.NextRun, task.LastRun, task.LastResult, task.Status,
		task.ID, task.GroupFolder,
	)
	if err != nil {
		return fmt.Errorf("update task: %w", err)
	}
	return nil
}

func (s *MySQLStore) DeleteTask(ctx context.Context, id, groupFolder string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, "DELETE FROM task_run_logs WHERE task_id = ? AND group_folder = ?", id, groupFolder); err != nil {
		return fmt.Errorf("delete task run logs: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM scheduled_tasks WHERE id = ? AND group_folder = ?", id, groupFolder); err != nil {
		return fmt.Errorf("delete task: %w", err)
	}

	return tx.Commit()
}

func (s *MySQLStore) GetDueTasks(ctx context.Context) ([]ScheduledTask, error) {
	now := time.Now().UTC()
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, group_folder, chat_jid, prompt, schedule_type, schedule_value, context_mode,
		        next_run, last_run, last_result, status, created_at
		 FROM scheduled_tasks
		 WHERE status = 'active' AND next_run IS NOT NULL AND next_run <= ?
		 ORDER BY next_run`, now,
	)
	if err != nil {
		return nil, fmt.Errorf("get due tasks: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return scanTasks(rows)
}

func (s *MySQLStore) LogTaskRun(ctx context.Context, log *TaskRunLog) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO task_run_logs (task_id, group_folder, run_at, duration_ms, status, result, error)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		log.TaskID, log.GroupFolder, log.RunAt, log.DurationMs, log.Status, log.Result, log.Error,
	)
	if err != nil {
		return fmt.Errorf("log task run: %w", err)
	}
	return nil
}

func (s *MySQLStore) GetTaskRunLogs(ctx context.Context, taskID, groupFolder string, limit int) ([]TaskRunLog, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, task_id, group_folder, run_at, duration_ms, status, result, error
		 FROM task_run_logs WHERE task_id = ? AND group_folder = ? ORDER BY run_at DESC LIMIT ?`,
		taskID, groupFolder, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("get task run logs: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var logs []TaskRunLog
	for rows.Next() {
		var l TaskRunLog
		if err := rows.Scan(&l.ID, &l.TaskID, &l.GroupFolder, &l.RunAt, &l.DurationMs, &l.Status, &l.Result, &l.Error); err != nil {
			return nil, fmt.Errorf("scan task run log: %w", err)
		}
		logs = append(logs, l)
	}
	return logs, rows.Err()
}

func scanTask(row scanner) (*ScheduledTask, error) {
	var t ScheduledTask
	err := row.Scan(
		&t.ID, &t.GroupFolder, &t.ChatJID, &t.Prompt,
		&t.ScheduleType, &t.ScheduleValue, &t.ContextMode,
		&t.NextRun, &t.LastRun, &t.LastResult, &t.Status, &t.CreatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("scan task: %w", err)
	}
	return &t, nil
}

func scanTasks(rows *sql.Rows) ([]ScheduledTask, error) {
	var tasks []ScheduledTask
	for rows.Next() {
		var t ScheduledTask
		if err := rows.Scan(
			&t.ID, &t.GroupFolder, &t.ChatJID, &t.Prompt,
			&t.ScheduleType, &t.ScheduleValue, &t.ContextMode,
			&t.NextRun, &t.LastRun, &t.LastResult, &t.Status, &t.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan task: %w", err)
		}
		tasks = append(tasks, t)
	}
	return tasks, rows.Err()
}

// ---------------------------------------------------------------------------
// SessionStore
// ---------------------------------------------------------------------------

func (s *MySQLStore) GetSession(ctx context.Context, groupFolder string) (*Session, error) {
	var sess Session
	err := s.db.QueryRowContext(ctx,
		"SELECT group_folder, session_id FROM sessions WHERE group_folder = ?",
		groupFolder,
	).Scan(&sess.GroupFolder, &sess.SessionID)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get session: %w", err)
	}
	return &sess, nil
}

func (s *MySQLStore) UpsertSession(ctx context.Context, sess *Session) error {
	_, err := s.db.ExecContext(ctx,
		"REPLACE INTO sessions (group_folder, session_id) VALUES (?, ?)",
		sess.GroupFolder, sess.SessionID,
	)
	if err != nil {
		return fmt.Errorf("upsert session: %w", err)
	}
	return nil
}

func (s *MySQLStore) DeleteSession(ctx context.Context, groupFolder string) error {
	_, err := s.db.ExecContext(ctx,
		"DELETE FROM sessions WHERE group_folder = ?",
		groupFolder,
	)
	if err != nil {
		return fmt.Errorf("delete session: %w", err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// RouterStateStore
// ---------------------------------------------------------------------------

func (s *MySQLStore) GetState(ctx context.Context, key string) (string, error) {
	var value string
	err := s.db.QueryRowContext(ctx,
		"SELECT value FROM router_state WHERE `key` = ?", key,
	).Scan(&value)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("get state: %w", err)
	}
	return value, nil
}

func (s *MySQLStore) SetState(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx,
		"REPLACE INTO router_state (`key`, value) VALUES (?, ?)",
		key, value,
	)
	if err != nil {
		return fmt.Errorf("set state: %w", err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// AllowlistStore
// ---------------------------------------------------------------------------

func (s *MySQLStore) GetAllowlist(ctx context.Context, chatJID string) ([]SenderAllowlistEntry, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT id, chat_jid, allow_pattern, mode FROM sender_allowlist WHERE chat_jid = ?",
		chatJID,
	)
	if err != nil {
		return nil, fmt.Errorf("get allowlist: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var entries []SenderAllowlistEntry
	for rows.Next() {
		var e SenderAllowlistEntry
		if err := rows.Scan(&e.ID, &e.ChatJID, &e.AllowPattern, &e.Mode); err != nil {
			return nil, fmt.Errorf("scan allowlist entry: %w", err)
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

func (s *MySQLStore) UpsertAllowlistEntry(ctx context.Context, entry *SenderAllowlistEntry) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO sender_allowlist (chat_jid, allow_pattern, mode)
		 VALUES (?, ?, ?)
		 ON DUPLICATE KEY UPDATE mode = VALUES(mode)`,
		entry.ChatJID, entry.AllowPattern, entry.Mode,
	)
	if err != nil {
		return fmt.Errorf("upsert allowlist entry: %w", err)
	}
	return nil
}

func (s *MySQLStore) DeleteAllowlistEntry(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM sender_allowlist WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("delete allowlist entry: %w", err)
	}
	return nil
}

// Ensure MySQLStore implements Store at compile time.
var _ Store = (*MySQLStore)(nil)

// ContainerConfigFromJSON is a convenience for JSON deserialization in scan helpers.
func ContainerConfigFromJSON(data json.RawMessage) (*ContainerConfig, error) {
	return ParseContainerConfig(data)
}
