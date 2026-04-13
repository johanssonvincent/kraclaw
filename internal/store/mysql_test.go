package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/DATA-DOG/go-sqlmock"
	"github.com/ory/dockertest/v3"
	"github.com/ory/dockertest/v3/docker"
)

// ---------------------------------------------------------------------------
// Real-MySQL helpers (dockertest)
// ---------------------------------------------------------------------------

var (
	realStoreOnce sync.Once
	realStoreDSN  string
	realStoreErr  error
	realStorePool *dockertest.Pool
	realStoreRes  *dockertest.Resource

	// errConnLost is a sentinel used to test that store methods wrap driver errors.
	errConnLost = errors.New("connection lost")
)

func requireTestStore(t *testing.T) *MySQLStore {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping integration test in -short mode")
	}

	realStoreOnce.Do(func() {
		pool, err := dockertest.NewPool("")
		if err != nil {
			realStoreErr = fmt.Errorf("create docker pool: %w", err)
			return
		}
		pool.MaxWait = 2 * time.Minute
		realStorePool = pool

		res, err := pool.RunWithOptions(&dockertest.RunOptions{
			Repository: "mysql",
			Tag:        "8.0",
			Env: []string{
				"MYSQL_ROOT_PASSWORD=kraclaw",
				"MYSQL_DATABASE=kraclaw_test",
			},
		}, func(hc *docker.HostConfig) {
			hc.AutoRemove = true
			hc.RestartPolicy = docker.RestartPolicy{Name: "no"}
		})
		if err != nil {
			realStoreErr = fmt.Errorf("start mysql container: %w", err)
			return
		}
		realStoreRes = res

		port := res.GetPort("3306/tcp")
		dsn := fmt.Sprintf("root:kraclaw@tcp(localhost:%s)/kraclaw_test?parseTime=true", port)

		if err := pool.Retry(func() error {
			db, err := sql.Open("mysql", dsn)
			if err != nil {
				return err
			}
			defer func() { _ = db.Close() }()
			return db.Ping()
		}); err != nil {
			realStoreErr = fmt.Errorf("wait for mysql: %w", err)
			_ = pool.Purge(res)
			return
		}

		realStoreDSN = dsn
	})

	if realStoreErr != nil {
		t.Skipf("skipping: docker MySQL unavailable: %v", realStoreErr)
	}

	s, err := NewMySQLStore(realStoreDSN, 5, 5, time.Minute)
	if err != nil {
		t.Fatalf("NewMySQLStore: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// TestRunMigrations_DirtyReturnsError verifies that the runMigrations function
// contains fail-fast behavior for dirty migration state rather than auto-reset.
// Because runMigrations opens its own DB connection from a DSN, it cannot be
// unit-tested with sqlmock. Instead we verify the source code contains the
// expected error path and does NOT contain the dangerous auto-reset pattern.
func TestRunMigrations_DirtyReturnsError(t *testing.T) {
	src, err := os.ReadFile("mysql.go")
	if err != nil {
		t.Fatalf("read mysql.go: %v", err)
	}
	source := string(src)

	// Must contain the fail-fast error message.
	if !strings.Contains(source, "dirty migration state detected at startup") {
		t.Fatal("mysql.go missing fail-fast error message for dirty migration state")
	}

	// Must use errors.Is for ErrNoChange comparison.
	if !strings.Contains(source, "errors.Is(err, migrate.ErrNoChange)") {
		t.Fatal("mysql.go should use errors.Is for ErrNoChange comparison")
	}

	// Must NOT contain the dangerous auto-reset pattern.
	if strings.Contains(source, "m.Force(-1)") {
		t.Fatal("mysql.go still contains m.Force(-1) — dirty migration auto-reset must be removed")
	}
	if strings.Contains(source, "forcing version reset and retrying") {
		t.Fatal("mysql.go still contains old auto-reset log message")
	}
}


func TestRetryWithBackoff(t *testing.T) {
	tests := []struct {
		name            string
		attempts        int
		returnErrs      []error
		wantErr         bool
		wantCalls       int
		errContains     string
		wantUnwrappable bool // if true, asserts dirtyMigrationError is reachable via errors.As through a wrapping layer
	}{
		{
			name:       "succeeds on first attempt",
			attempts:   3,
			returnErrs: []error{nil},
			wantErr:    false,
			wantCalls:  1,
		},
		{
			name:       "succeeds on second attempt",
			attempts:   3,
			returnErrs: []error{errors.New("transient"), nil},
			wantErr:    false,
			wantCalls:  2,
		},
		{
			name:        "attempts=1 succeeds on first and only attempt",
			attempts:    1,
			returnErrs:  []error{nil},
			wantErr:     false,
			wantCalls:   1,
		},
		{
			name:        "exhausts all attempts",
			attempts:    3,
			returnErrs:  []error{errors.New("fail"), errors.New("fail"), errors.New("fail")},
			wantErr:     true,
			wantCalls:   3,
			errContains: "failed after 3 attempts",
		},
		{
			name:        "attempts=1 calls fn once and returns error",
			attempts:    1,
			returnErrs:  []error{errors.New("single-attempt fail")},
			wantErr:     true,
			wantCalls:   1,
			errContains: "failed after 1 attempts",
		},
		{
			name:            "non-retryable error exits immediately and is unwrappable through a wrapping layer",
			attempts:        5,
			returnErrs:      []error{&dirtyMigrationError{msg: "dirty", migErr: errors.New("original")}},
			wantErr:         true,
			wantCalls:       1,
			errContains:     "dirty",
			wantUnwrappable: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			callCount := 0
			idx := 0
			fn := func() error {
				callCount++
				if idx < len(tt.returnErrs) {
					err := tt.returnErrs[idx]
					idx++
					return err
				}
				return nil
			}
			err := retryWithBackoff(tt.attempts, time.Millisecond, "test-op", fn)
			if (err != nil) != tt.wantErr {
				t.Fatalf("wantErr=%v got err=%v", tt.wantErr, err)
			}
			if callCount != tt.wantCalls {
				t.Fatalf("wantCalls=%d got %d", tt.wantCalls, callCount)
			}
			if tt.errContains != "" && !strings.Contains(err.Error(), tt.errContains) {
				t.Fatalf("expected error containing %q, got %q", tt.errContains, err.Error())
			}
			if tt.wantUnwrappable {
				// Simulate the fmt.Errorf("%w") wrapping that runMigrations applies,
				// to verify the chain survives an additional wrapping layer.
				wrapped := fmt.Errorf("migrate up: %w", err)
				var dme *dirtyMigrationError
				if !errors.As(wrapped, &dme) {
					t.Fatalf("expected dirtyMigrationError to be reachable via errors.As after wrapping, got %T: %v", wrapped, wrapped)
				}
			}
		})
	}
}

func TestPingRetryOnTransientError(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.MonitorPingsOption(true))
	if err != nil {
		t.Fatalf("create sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()

	mock.ExpectPing().WillReturnError(errors.New("connection refused"))
	mock.ExpectPing()

	callCount := 0
	if err := retryWithBackoff(5, time.Millisecond, "ping mysql", func() error {
		callCount++
		return db.Ping()
	}); err != nil {
		t.Fatalf("expected success after retry, got: %v", err)
	}
	if callCount != 2 {
		t.Fatalf("expected 2 ping attempts, got %d", callCount)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}


func newTestStore(t *testing.T) (*MySQLStore, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("create sqlmock: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return newMySQLStoreFromDB(db), mock
}

// ---------------------------------------------------------------------------
// GroupStore
// ---------------------------------------------------------------------------

func TestGetGroup(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)

	tests := []struct {
		name    string
		jid     string
		setup   func(sqlmock.Sqlmock)
		wantNil bool
		wantErr bool
	}{
		{
			name: "found",
			jid:  "group1@g.us",
			setup: func(mock sqlmock.Sqlmock) {
				rows := sqlmock.NewRows([]string{"jid", "name", "folder", "trigger_pattern", "is_main", "requires_trigger", "container_config", "added_at"}).
					AddRow("group1@g.us", "Test Group", "test-group", "!bot", false, true, nil, now)
				mock.ExpectQuery("SELECT .+ FROM `groups` WHERE jid = \\?").
					WithArgs("group1@g.us").
					WillReturnRows(rows)
			},
		},
		{
			name: "not found",
			jid:  "missing@g.us",
			setup: func(mock sqlmock.Sqlmock) {
				rows := sqlmock.NewRows([]string{"jid", "name", "folder", "trigger_pattern", "is_main", "requires_trigger", "container_config", "added_at"})
				mock.ExpectQuery("SELECT .+ FROM `groups` WHERE jid = \\?").
					WithArgs("missing@g.us").
					WillReturnRows(rows)
			},
			wantNil: true,
		},
		{
			name: "with container config",
			jid:  "group2@g.us",
			setup: func(mock sqlmock.Sqlmock) {
				rows := sqlmock.NewRows([]string{"jid", "name", "folder", "trigger_pattern", "is_main", "requires_trigger", "container_config", "added_at"}).
					AddRow("group2@g.us", "Config Group", "config-group", "!ai", true, false, []byte(`{"timeout":5000}`), now)
				mock.ExpectQuery("SELECT .+ FROM `groups` WHERE jid = \\?").
					WithArgs("group2@g.us").
					WillReturnRows(rows)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store, mock := newTestStore(t)
			tt.setup(mock)

			g, err := store.GetGroup(context.Background(), tt.jid)
			if (err != nil) != tt.wantErr {
				t.Fatalf("GetGroup() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantNil && g != nil {
				t.Fatalf("expected nil group, got %+v", g)
			}
			if !tt.wantNil && !tt.wantErr && g == nil {
				t.Fatal("expected group, got nil")
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Errorf("unmet expectations: %v", err)
			}
		})
	}
}

func TestGetGroupByFolder(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	store, mock := newTestStore(t)

	rows := sqlmock.NewRows([]string{"jid", "name", "folder", "trigger_pattern", "is_main", "requires_trigger", "container_config", "added_at"}).
		AddRow("group1@g.us", "Test", "my-folder", "!bot", false, true, nil, now)
	mock.ExpectQuery("SELECT .+ FROM `groups` WHERE folder = \\?").
		WithArgs("my-folder").
		WillReturnRows(rows)

	g, err := store.GetGroupByFolder(context.Background(), "my-folder")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if g == nil || g.Folder != "my-folder" {
		t.Fatalf("expected folder my-folder, got %+v", g)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

func TestListGroups(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	store, mock := newTestStore(t)

	rows := sqlmock.NewRows([]string{"jid", "name", "folder", "trigger_pattern", "is_main", "requires_trigger", "container_config", "added_at"}).
		AddRow("g1@g.us", "G1", "g1", "!bot", false, true, nil, now).
		AddRow("g2@g.us", "G2", "g2", "!ai", true, false, nil, now)
	mock.ExpectQuery("SELECT .+ FROM `groups`").WillReturnRows(rows)

	groups, err := store.ListGroups(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(groups) != 2 {
		t.Fatalf("expected 2 groups, got %d", len(groups))
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

func TestUpsertGroup(t *testing.T) {
	store, mock := newTestStore(t)

	mock.ExpectExec("INSERT INTO `groups`.*ON DUPLICATE KEY UPDATE").
		WithArgs("g1@g.us", "Test", "test", "!bot", false, true, sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))

	err := store.UpsertGroup(context.Background(), &Group{
		JID:             "g1@g.us",
		Name:            "Test",
		Folder:          "test",
		TriggerPattern:  "!bot",
		RequiresTrigger: true,
		AddedAt:         time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

// TestUpsertGroupPreservesActiveState verifies that the UpsertGroup SQL does not
// include is_active or last_active_at in the ON DUPLICATE KEY UPDATE clause,
// ensuring those columns are never clobbered on update.
func TestUpsertGroupPreservesActiveState(t *testing.T) {
	src, err := os.ReadFile("mysql.go")
	if err != nil {
		t.Fatalf("read mysql.go: %v", err)
	}
	source := string(src)

	// Must use INSERT ... ON DUPLICATE KEY UPDATE (not REPLACE INTO).
	if strings.Contains(source, "REPLACE INTO `groups`") {
		t.Fatal("UpsertGroup still uses REPLACE INTO — must use INSERT ... ON DUPLICATE KEY UPDATE")
	}

	// Must contain the new upsert pattern.
	if !strings.Contains(source, "ON DUPLICATE KEY UPDATE") {
		t.Fatal("UpsertGroup missing ON DUPLICATE KEY UPDATE")
	}

	// The UPDATE clause must NOT mention is_active or last_active_at.
	// We verify by checking these strings do not appear in an UPDATE context.
	// A simple presence check is sufficient because those columns should only
	// appear in MarkGroupActive / MarkGroupInactive.
	if strings.Contains(source, "is_active = VALUES") {
		t.Fatal("UpsertGroup UPDATE clause must not include is_active — managed by MarkGroupActive/Inactive")
	}
	if strings.Contains(source, "last_active_at = VALUES") {
		t.Fatal("UpsertGroup UPDATE clause must not include last_active_at — managed by MarkGroupActive/Inactive")
	}
}

func TestDeleteGroup(t *testing.T) {
	store, mock := newTestStore(t)

	mock.ExpectExec("DELETE FROM `groups` WHERE jid = \\?").
		WithArgs("g1@g.us").
		WillReturnResult(sqlmock.NewResult(0, 1))

	err := store.DeleteGroup(context.Background(), "g1@g.us")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

// ---------------------------------------------------------------------------
// MessageStore
// ---------------------------------------------------------------------------

func TestStoreMessage(t *testing.T) {
	store, mock := newTestStore(t)
	now := time.Now().UTC()

	mock.ExpectExec("REPLACE INTO messages").
		WithArgs("msg1", "chat1@g.us", "sender1", "Alice", "hello", now, true, false).
		WillReturnResult(sqlmock.NewResult(0, 1))

	err := store.StoreMessage(context.Background(), &Message{
		ID: "msg1", ChatJID: "chat1@g.us", Sender: "sender1", SenderName: "Alice",
		Content: "hello", Timestamp: now, IsFromMe: true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

func TestStoreBatch(t *testing.T) {
	tests := []struct {
		name    string
		msgs    []Message
		setup   func(sqlmock.Sqlmock)
		wantErr bool
	}{
		{
			name: "empty batch",
			msgs: nil,
			setup: func(mock sqlmock.Sqlmock) {
				// no expectations
			},
		},
		{
			name: "two messages",
			msgs: []Message{
				{ID: "m1", ChatJID: "c1", Sender: "s1", SenderName: "A", Content: "hi", Timestamp: time.Now().UTC()},
				{ID: "m2", ChatJID: "c1", Sender: "s2", SenderName: "B", Content: "hey", Timestamp: time.Now().UTC()},
			},
			setup: func(mock sqlmock.Sqlmock) {
				mock.ExpectBegin()
				mock.ExpectPrepare("REPLACE INTO messages")
				mock.ExpectExec("REPLACE INTO messages").WillReturnResult(sqlmock.NewResult(0, 1))
				mock.ExpectExec("REPLACE INTO messages").WillReturnResult(sqlmock.NewResult(0, 1))
				mock.ExpectCommit()
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store, mock := newTestStore(t)
			if tt.setup != nil {
				tt.setup(mock)
			}

			err := store.StoreBatch(context.Background(), tt.msgs)
			if (err != nil) != tt.wantErr {
				t.Fatalf("StoreBatch() error = %v, wantErr %v", err, tt.wantErr)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Errorf("unmet expectations: %v", err)
			}
		})
	}
}

func TestDeleteMessage(t *testing.T) {
	tests := []struct {
		name    string
		id      string
		chatJID string
		setup   func(sqlmock.Sqlmock)
		wantErr bool
	}{
		{
			name:    "happy path",
			id:      "msg1",
			chatJID: "chat1@g.us",
			setup: func(mock sqlmock.Sqlmock) {
				mock.ExpectExec("DELETE FROM messages").
					WithArgs("msg1", "chat1@g.us").
					WillReturnResult(sqlmock.NewResult(0, 1))
			},
		},
		{
			name:    "no match is idempotent",
			id:      "nonexistent",
			chatJID: "chat1@g.us",
			setup: func(mock sqlmock.Sqlmock) {
				mock.ExpectExec("DELETE FROM messages").
					WithArgs("nonexistent", "chat1@g.us").
					WillReturnResult(sqlmock.NewResult(0, 0))
			},
		},
		{
			name:    "sql error is wrapped",
			id:      "msg1",
			chatJID: "chat1@g.us",
			setup: func(mock sqlmock.Sqlmock) {
				mock.ExpectExec("DELETE FROM messages").
					WithArgs("msg1", "chat1@g.us").
					WillReturnError(errConnLost)
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, mock := newTestStore(t)
			if tt.setup != nil {
				tt.setup(mock)
			}

			err := s.DeleteMessage(context.Background(), tt.id, tt.chatJID)
			if (err != nil) != tt.wantErr {
				t.Fatalf("DeleteMessage() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr && err != nil {
				if !errors.Is(err, errConnLost) {
					t.Errorf("DeleteMessage() error = %v, want it to wrap errConnLost", err)
				}
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Errorf("unmet expectations: %v", err)
			}
		})
	}
}

func TestGetNewMessages(t *testing.T) {
	store, mock := newTestStore(t)
	since := time.Now().UTC().Add(-time.Hour)

	rows := sqlmock.NewRows([]string{"id", "chat_jid", "sender", "sender_name", "content", "timestamp", "is_from_me", "is_bot_message"}).
		AddRow("m1", "c1@g.us", "s1", "Alice", "hello", time.Now().UTC(), false, false)
	mock.ExpectQuery("SELECT \\* FROM \\(").
		WithArgs(since, "c1@g.us", "c2@g.us", 100).
		WillReturnRows(rows)

	msgs, err := store.GetNewMessages(context.Background(), []string{"c1@g.us", "c2@g.us"}, since, 100)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

func TestGetNewMessagesEmpty(t *testing.T) {
	store, _ := newTestStore(t)

	msgs, err := store.GetNewMessages(context.Background(), nil, time.Now(), 100)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if msgs != nil {
		t.Fatalf("expected nil, got %v", msgs)
	}
}

func TestGetMessagesSince(t *testing.T) {
	store, mock := newTestStore(t)
	since := time.Now().UTC().Add(-time.Hour)

	rows := sqlmock.NewRows([]string{"id", "chat_jid", "sender", "sender_name", "content", "timestamp", "is_from_me", "is_bot_message"}).
		AddRow("m1", "c1@g.us", "s1", "Alice", "test", time.Now().UTC(), false, false)
	mock.ExpectQuery("SELECT \\* FROM \\(").
		WithArgs("c1@g.us", since, 50).
		WillReturnRows(rows)

	msgs, err := store.GetMessagesSince(context.Background(), "c1@g.us", since, 50)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

// ---------------------------------------------------------------------------
// ChatStore
// ---------------------------------------------------------------------------

func TestUpsertChat(t *testing.T) {
	store, mock := newTestStore(t)

	mock.ExpectExec("INSERT INTO chats").
		WithArgs("c1@g.us", "Chat", "whatsapp", true, sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))

	err := store.UpsertChat(context.Background(), &Chat{
		JID: "c1@g.us", Name: "Chat", Channel: "whatsapp", IsGroup: true, LastMessageTime: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

func TestGetChat(t *testing.T) {
	tests := []struct {
		name    string
		jid     string
		setup   func(sqlmock.Sqlmock)
		wantNil bool
	}{
		{
			name: "found",
			jid:  "c1@g.us",
			setup: func(mock sqlmock.Sqlmock) {
				rows := sqlmock.NewRows([]string{"jid", "name", "channel", "is_group", "last_message_time"}).
					AddRow("c1@g.us", "Chat", "whatsapp", true, time.Now().UTC())
				mock.ExpectQuery("SELECT .+ FROM chats WHERE jid = \\?").
					WithArgs("c1@g.us").WillReturnRows(rows)
			},
		},
		{
			name: "not found",
			jid:  "missing@g.us",
			setup: func(mock sqlmock.Sqlmock) {
				rows := sqlmock.NewRows([]string{"jid", "name", "channel", "is_group", "last_message_time"})
				mock.ExpectQuery("SELECT .+ FROM chats WHERE jid = \\?").
					WithArgs("missing@g.us").WillReturnRows(rows)
			},
			wantNil: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store, mock := newTestStore(t)
			tt.setup(mock)

			c, err := store.GetChat(context.Background(), tt.jid)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.wantNil && c != nil {
				t.Fatalf("expected nil, got %+v", c)
			}
			if !tt.wantNil && c == nil {
				t.Fatal("expected chat, got nil")
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Errorf("unmet expectations: %v", err)
			}
		})
	}
}

func TestListChats(t *testing.T) {
	store, mock := newTestStore(t)

	rows := sqlmock.NewRows([]string{"jid", "name", "channel", "is_group", "last_message_time"}).
		AddRow("c1@g.us", "Chat1", "wa", true, time.Now().UTC()).
		AddRow("c2@s.whatsapp.net", "Chat2", "wa", false, time.Now().UTC())
	mock.ExpectQuery("SELECT .+ FROM chats ORDER BY").WillReturnRows(rows)

	chats, err := store.ListChats(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(chats) != 2 {
		t.Fatalf("expected 2 chats, got %d", len(chats))
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

// ---------------------------------------------------------------------------
// TaskStore
// ---------------------------------------------------------------------------

func TestCreateTask(t *testing.T) {
	store, mock := newTestStore(t)
	now := time.Now().UTC()
	nextRun := now.Add(time.Hour)

	mock.ExpectExec("INSERT INTO scheduled_tasks").
		WithArgs("task1", "folder1", "c1@g.us", "do something", ScheduleCron, "0 * * * *", ContextIsolated, &nextRun, TaskActive, now).
		WillReturnResult(sqlmock.NewResult(0, 1))

	err := store.CreateTask(context.Background(), &ScheduledTask{
		ID: "task1", GroupFolder: "folder1", ChatJID: "c1@g.us", Prompt: "do something",
		ScheduleType: ScheduleCron, ScheduleValue: "0 * * * *", ContextMode: ContextIsolated,
		NextRun: &nextRun, Status: TaskActive, CreatedAt: now,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

func TestGetTask(t *testing.T) {
	tests := []struct {
		name        string
		id          string
		groupFolder string
		setup       func(sqlmock.Sqlmock)
		wantNil     bool
	}{
		{
			name:        "found",
			id:          "task1",
			groupFolder: "f1",
			setup: func(mock sqlmock.Sqlmock) {
				rows := sqlmock.NewRows([]string{
					"id", "group_folder", "chat_jid", "prompt", "schedule_type", "schedule_value",
					"context_mode", "next_run", "last_run", "last_result", "status", "created_at",
				}).AddRow("task1", "f1", "c1", "prompt", "cron", "0 * * * *", "isolated", nil, nil, nil, "active", time.Now().UTC())
				mock.ExpectQuery("SELECT .+ FROM scheduled_tasks WHERE id = \\? AND group_folder = \\?").
					WithArgs("task1", "f1").WillReturnRows(rows)
			},
		},
		{
			name:        "not found",
			id:          "missing",
			groupFolder: "f1",
			setup: func(mock sqlmock.Sqlmock) {
				rows := sqlmock.NewRows([]string{
					"id", "group_folder", "chat_jid", "prompt", "schedule_type", "schedule_value",
					"context_mode", "next_run", "last_run", "last_result", "status", "created_at",
				})
				mock.ExpectQuery("SELECT .+ FROM scheduled_tasks WHERE id = \\? AND group_folder = \\?").
					WithArgs("missing", "f1").WillReturnRows(rows)
			},
			wantNil: true,
		},
		{
			name:        "wrong group returns nil",
			id:          "task1",
			groupFolder: "other-group",
			setup: func(mock sqlmock.Sqlmock) {
				rows := sqlmock.NewRows([]string{
					"id", "group_folder", "chat_jid", "prompt", "schedule_type", "schedule_value",
					"context_mode", "next_run", "last_run", "last_result", "status", "created_at",
				})
				mock.ExpectQuery("SELECT .+ FROM scheduled_tasks WHERE id = \\? AND group_folder = \\?").
					WithArgs("task1", "other-group").WillReturnRows(rows)
			},
			wantNil: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store, mock := newTestStore(t)
			tt.setup(mock)

			task, err := store.GetTask(context.Background(), tt.id, tt.groupFolder)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.wantNil && task != nil {
				t.Fatalf("expected nil, got %+v", task)
			}
			if !tt.wantNil && task == nil {
				t.Fatal("expected task, got nil")
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Errorf("unmet expectations: %v", err)
			}
		})
	}
}

func TestDeleteTask(t *testing.T) {
	tests := []struct {
		name        string
		id          string
		groupFolder string
		setup       func(sqlmock.Sqlmock)
	}{
		{
			name:        "deletes matching task",
			id:          "task1",
			groupFolder: "folder1",
			setup: func(mock sqlmock.Sqlmock) {
				mock.ExpectBegin()
				mock.ExpectExec("DELETE FROM task_run_logs WHERE task_id = \\? AND group_folder = \\?").
					WithArgs("task1", "folder1").WillReturnResult(sqlmock.NewResult(0, 0))
				mock.ExpectExec("DELETE FROM scheduled_tasks WHERE id = \\? AND group_folder = \\?").
					WithArgs("task1", "folder1").WillReturnResult(sqlmock.NewResult(0, 1))
				mock.ExpectCommit()
			},
		},
		{
			name:        "wrong group deletes 0 rows",
			id:          "task1",
			groupFolder: "other-group",
			setup: func(mock sqlmock.Sqlmock) {
				mock.ExpectBegin()
				mock.ExpectExec("DELETE FROM task_run_logs WHERE task_id = \\? AND group_folder = \\?").
					WithArgs("task1", "other-group").WillReturnResult(sqlmock.NewResult(0, 0))
				mock.ExpectExec("DELETE FROM scheduled_tasks WHERE id = \\? AND group_folder = \\?").
					WithArgs("task1", "other-group").WillReturnResult(sqlmock.NewResult(0, 0))
				mock.ExpectCommit()
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store, mock := newTestStore(t)
			tt.setup(mock)

			err := store.DeleteTask(context.Background(), tt.id, tt.groupFolder)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Errorf("unmet expectations: %v", err)
			}
		})
	}
}

func TestUpdateTask(t *testing.T) {
	store, mock := newTestStore(t)
	now := time.Now().UTC()
	nextRun := now.Add(time.Hour)
	lastResult := "done"

	mock.ExpectExec("UPDATE scheduled_tasks SET").
		WithArgs("new prompt", ScheduleCron, "0 * * * *", ContextIsolated,
			&nextRun, &now, &lastResult, TaskActive,
			"task1", "folder1").
		WillReturnResult(sqlmock.NewResult(0, 1))

	err := store.UpdateTask(context.Background(), &ScheduledTask{
		ID: "task1", GroupFolder: "folder1", Prompt: "new prompt",
		ScheduleType: ScheduleCron, ScheduleValue: "0 * * * *", ContextMode: ContextIsolated,
		NextRun: &nextRun, LastRun: &now, LastResult: &lastResult, Status: TaskActive,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

func TestGetDueTasks(t *testing.T) {
	store, mock := newTestStore(t)

	rows := sqlmock.NewRows([]string{
		"id", "group_folder", "chat_jid", "prompt", "schedule_type", "schedule_value",
		"context_mode", "next_run", "last_run", "last_result", "status", "created_at",
	}).AddRow("task1", "f1", "c1", "run it", "cron", "0 * * * *", "isolated",
		time.Now().UTC().Add(-time.Minute), nil, nil, "active", time.Now().UTC())

	mock.ExpectQuery("SELECT .+ FROM scheduled_tasks").
		WithArgs(sqlmock.AnyArg()).
		WillReturnRows(rows)

	tasks, err := store.GetDueTasks(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("expected 1 due task, got %d", len(tasks))
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

func TestLogTaskRun(t *testing.T) {
	store, mock := newTestStore(t)
	now := time.Now().UTC()
	result := "done"

	mock.ExpectExec("INSERT INTO task_run_logs").
		WithArgs("task1", "folder1", now, 150, "success", &result, nil).
		WillReturnResult(sqlmock.NewResult(1, 1))

	err := store.LogTaskRun(context.Background(), &TaskRunLog{
		TaskID: "task1", GroupFolder: "folder1", RunAt: now, DurationMs: 150, Status: RunSuccess, Result: &result,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

func TestGetTaskRunLogs(t *testing.T) {
	tests := []struct {
		name        string
		taskID      string
		groupFolder string
		limit       int
		setup       func(sqlmock.Sqlmock)
		wantCount   int
	}{
		{
			name:        "returns matching logs",
			taskID:      "task1",
			groupFolder: "folder1",
			limit:       10,
			setup: func(mock sqlmock.Sqlmock) {
				now := time.Now().UTC()
				result := "ok"
				rows := sqlmock.NewRows([]string{"id", "task_id", "group_folder", "run_at", "duration_ms", "status", "result", "error"}).
					AddRow(1, "task1", "folder1", now, 100, "success", &result, nil)
				mock.ExpectQuery("SELECT .+ FROM task_run_logs WHERE task_id = \\? AND group_folder = \\?").
					WithArgs("task1", "folder1", 10).WillReturnRows(rows)
			},
			wantCount: 1,
		},
		{
			name:        "wrong group returns empty",
			taskID:      "task1",
			groupFolder: "other-group",
			limit:       10,
			setup: func(mock sqlmock.Sqlmock) {
				rows := sqlmock.NewRows([]string{"id", "task_id", "group_folder", "run_at", "duration_ms", "status", "result", "error"})
				mock.ExpectQuery("SELECT .+ FROM task_run_logs WHERE task_id = \\? AND group_folder = \\?").
					WithArgs("task1", "other-group", 10).WillReturnRows(rows)
			},
			wantCount: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store, mock := newTestStore(t)
			tt.setup(mock)

			logs, err := store.GetTaskRunLogs(context.Background(), tt.taskID, tt.groupFolder, tt.limit)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(logs) != tt.wantCount {
				t.Fatalf("expected %d logs, got %d", tt.wantCount, len(logs))
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Errorf("unmet expectations: %v", err)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// SessionStore
// ---------------------------------------------------------------------------

func TestGetSession(t *testing.T) {
	tests := []struct {
		name    string
		folder  string
		setup   func(sqlmock.Sqlmock)
		wantNil bool
	}{
		{
			name:   "found",
			folder: "group1",
			setup: func(mock sqlmock.Sqlmock) {
				rows := sqlmock.NewRows([]string{"group_folder", "session_id"}).
					AddRow("group1", "sess-abc")
				mock.ExpectQuery("SELECT .+ FROM sessions WHERE group_folder = \\?").
					WithArgs("group1").WillReturnRows(rows)
			},
		},
		{
			name:   "not found",
			folder: "missing",
			setup: func(mock sqlmock.Sqlmock) {
				rows := sqlmock.NewRows([]string{"group_folder", "session_id"})
				mock.ExpectQuery("SELECT .+ FROM sessions WHERE group_folder = \\?").
					WithArgs("missing").WillReturnRows(rows)
			},
			wantNil: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store, mock := newTestStore(t)
			tt.setup(mock)

			sess, err := store.GetSession(context.Background(), tt.folder)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.wantNil && sess != nil {
				t.Fatalf("expected nil, got %+v", sess)
			}
			if !tt.wantNil && sess == nil {
				t.Fatal("expected session, got nil")
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Errorf("unmet expectations: %v", err)
			}
		})
	}
}

func TestUpsertSession(t *testing.T) {
	store, mock := newTestStore(t)

	mock.ExpectExec("REPLACE INTO sessions").
		WithArgs("group1", "sess-new").
		WillReturnResult(sqlmock.NewResult(0, 1))

	err := store.UpsertSession(context.Background(), &Session{GroupFolder: "group1", SessionID: "sess-new"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

func TestDeleteSession(t *testing.T) {
	store, mock := newTestStore(t)

	mock.ExpectExec("DELETE FROM sessions WHERE group_folder = \\?").
		WithArgs("group1").
		WillReturnResult(sqlmock.NewResult(0, 1))

	err := store.DeleteSession(context.Background(), "group1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

// ---------------------------------------------------------------------------
// RouterStateStore
// ---------------------------------------------------------------------------

func TestGetState(t *testing.T) {
	tests := []struct {
		name      string
		key       string
		setup     func(sqlmock.Sqlmock)
		wantValue string
	}{
		{
			name: "found",
			key:  "last_sync",
			setup: func(mock sqlmock.Sqlmock) {
				rows := sqlmock.NewRows([]string{"value"}).AddRow("2024-01-01T00:00:00Z")
				mock.ExpectQuery("SELECT value FROM router_state WHERE").
					WithArgs("last_sync").WillReturnRows(rows)
			},
			wantValue: "2024-01-01T00:00:00Z",
		},
		{
			name: "not found returns empty",
			key:  "missing",
			setup: func(mock sqlmock.Sqlmock) {
				rows := sqlmock.NewRows([]string{"value"})
				mock.ExpectQuery("SELECT value FROM router_state WHERE").
					WithArgs("missing").WillReturnRows(rows)
			},
			wantValue: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store, mock := newTestStore(t)
			tt.setup(mock)

			val, err := store.GetState(context.Background(), tt.key)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if val != tt.wantValue {
				t.Fatalf("expected %q, got %q", tt.wantValue, val)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Errorf("unmet expectations: %v", err)
			}
		})
	}
}

func TestSetState(t *testing.T) {
	store, mock := newTestStore(t)

	mock.ExpectExec("REPLACE INTO router_state").
		WithArgs("mykey", "myval").
		WillReturnResult(sqlmock.NewResult(0, 1))

	err := store.SetState(context.Background(), "mykey", "myval")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

// ---------------------------------------------------------------------------
// AllowlistStore
// ---------------------------------------------------------------------------

func TestGetAllowlist(t *testing.T) {
	store, mock := newTestStore(t)

	rows := sqlmock.NewRows([]string{"id", "chat_jid", "allow_pattern", "mode"}).
		AddRow(1, "c1@g.us", "+1234*", "trigger").
		AddRow(2, "c1@g.us", "+5678*", "drop")
	mock.ExpectQuery("SELECT .+ FROM sender_allowlist WHERE chat_jid = \\?").
		WithArgs("c1@g.us").WillReturnRows(rows)

	entries, err := store.GetAllowlist(context.Background(), "c1@g.us")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}
	if entries[0].Mode != ModeTrigger {
		t.Fatalf("expected trigger mode, got %s", entries[0].Mode)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

func TestUpsertAllowlistEntry(t *testing.T) {
	store, mock := newTestStore(t)

	mock.ExpectExec("INSERT INTO sender_allowlist").
		WithArgs("c1@g.us", "+1234*", "trigger").
		WillReturnResult(sqlmock.NewResult(1, 1))

	err := store.UpsertAllowlistEntry(context.Background(), &SenderAllowlistEntry{
		ChatJID: "c1@g.us", AllowPattern: "+1234*", Mode: ModeTrigger,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

func TestDeleteAllowlistEntry(t *testing.T) {
	store, mock := newTestStore(t)

	mock.ExpectExec("DELETE FROM sender_allowlist WHERE id = \\?").
		WithArgs(int64(1)).
		WillReturnResult(sqlmock.NewResult(0, 1))

	err := store.DeleteAllowlistEntry(context.Background(), 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

// ---------------------------------------------------------------------------
// GroupActiveStore (real MySQL via dockertest)
// ---------------------------------------------------------------------------

func TestGroupActiveStore(t *testing.T) {
	s := requireTestStore(t)

	ctx := context.Background()
	jid := "active-test@g.us"

	// Insert a group so the JID exists.
	err := s.UpsertGroup(ctx, &Group{
		JID:     jid,
		Name:    "Active Test",
		Folder:  "active-test",
		AddedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("upsert group: %v", err)
	}

	// Initially not active.
	active, err := s.IsGroupActive(ctx, jid)
	if err != nil {
		t.Fatalf("IsGroupActive: %v", err)
	}
	if active {
		t.Error("expected not active initially")
	}

	count, err := s.ActiveGroupCount(ctx)
	if err != nil {
		t.Fatalf("ActiveGroupCount: %v", err)
	}
	if count != 0 {
		t.Errorf("ActiveGroupCount = %d, want 0", count)
	}

	// Mark active.
	if err := s.MarkGroupActive(ctx, jid); err != nil {
		t.Fatalf("MarkGroupActive: %v", err)
	}

	active, err = s.IsGroupActive(ctx, jid)
	if err != nil {
		t.Fatalf("IsGroupActive after mark: %v", err)
	}
	if !active {
		t.Error("expected active after MarkGroupActive")
	}

	count, err = s.ActiveGroupCount(ctx)
	if err != nil {
		t.Fatalf("ActiveGroupCount: %v", err)
	}
	if count != 1 {
		t.Errorf("ActiveGroupCount = %d, want 1", count)
	}

	jids, err := s.ActiveGroupJIDs(ctx)
	if err != nil {
		t.Fatalf("ActiveGroupJIDs: %v", err)
	}
	if len(jids) != 1 || jids[0] != jid {
		t.Errorf("ActiveGroupJIDs = %v, want [%q]", jids, jid)
	}

	// Idempotent mark active.
	if err := s.MarkGroupActive(ctx, jid); err != nil {
		t.Fatalf("MarkGroupActive idempotent: %v", err)
	}
	count, _ = s.ActiveGroupCount(ctx)
	if count != 1 {
		t.Errorf("ActiveGroupCount after second mark = %d, want 1", count)
	}

	// Mark inactive.
	if err := s.MarkGroupInactive(ctx, jid); err != nil {
		t.Fatalf("MarkGroupInactive: %v", err)
	}
	active, _ = s.IsGroupActive(ctx, jid)
	if active {
		t.Error("expected not active after MarkGroupInactive")
	}
}

// TestMySQLStore_MarkGroupActive_UnknownJID verifies that MarkGroupActive and
// MarkGroupInactive return errors.Is(err, ErrGroupNotFound) when called with a
// JID that does not exist in the database (gap 10).
// Uses sqlmock so it runs without Docker.
func TestMySQLStore_MarkGroupActive_UnknownJID(t *testing.T) {
	ctx := context.Background()
	const jid = "nonexistent@g.us"

	t.Run("MarkGroupActive", func(t *testing.T) {
		s, mock := newTestStore(t)
		// UPDATE returns 0 rows affected (JID missing).
		mock.ExpectExec("UPDATE `groups` SET is_active = TRUE").
			WithArgs(jid).
			WillReturnResult(sqlmock.NewResult(0, 0))
		// Existence check returns false.
		mock.ExpectQuery("SELECT EXISTS").
			WithArgs(jid).
			WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))

		if err := s.MarkGroupActive(ctx, jid); !errors.Is(err, ErrGroupNotFound) {
			t.Errorf("MarkGroupActive unknown JID: got %v, want errors.Is ErrGroupNotFound", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("unmet expectations: %v", err)
		}
	})

	t.Run("MarkGroupInactive", func(t *testing.T) {
		s, mock := newTestStore(t)
		// UPDATE returns 0 rows affected (JID missing).
		mock.ExpectExec("UPDATE `groups` SET is_active = FALSE").
			WithArgs(jid).
			WillReturnResult(sqlmock.NewResult(0, 0))
		// Existence check returns false.
		mock.ExpectQuery("SELECT EXISTS").
			WithArgs(jid).
			WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))

		if err := s.MarkGroupInactive(ctx, jid); !errors.Is(err, ErrGroupNotFound) {
			t.Errorf("MarkGroupInactive unknown JID: got %v, want errors.Is ErrGroupNotFound", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("unmet expectations: %v", err)
		}
	})
}
