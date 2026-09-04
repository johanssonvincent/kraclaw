package integration_test

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/johanssonvincent/kraclaw/internal/store"
)

const (
	gooseVersionTable   = "goose_db_version"
	legacyVersionTable  = "schema_migrations"
	latestMigrationName = "20260831000001"
	totalMigrations     = 7
)

func createTestDatabase(t *testing.T, env *integrationEnv, name string) string {
	t.Helper()

	rootDSN := strings.Replace(env.mysqlDSN, "/kraclaw_test", "/", 1)

	admin, err := sql.Open("mysql", rootDSN)
	if err != nil {
		t.Fatalf("open admin connection: %v", err)
	}

	defer func() { _ = admin.Close() }()

	if _, err := admin.ExecContext(context.Background(), "CREATE DATABASE IF NOT EXISTS "+name); err != nil {
		t.Fatalf("create database %s: %v", name, err)
	}

	return strings.Replace(env.mysqlDSN, "/kraclaw_test", "/"+name, 1)
}

func migrateDatabase(t *testing.T, dsn string) {
	t.Helper()

	s, err := store.NewMySQLStore(context.Background(), dsn, 2, 2, time.Minute)
	if err != nil {
		t.Fatalf("new mysql store: %v", err)
	}

	if err := s.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
}

func gooseVersionRows(t *testing.T, dsn string) (count int, maxVersion int64) {
	t.Helper()

	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatalf("open verification db: %v", err)
	}
	defer func() { _ = db.Close() }()

	if err := db.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM "+gooseVersionTable).Scan(&count); err != nil {
		t.Fatalf("count goose versions: %v", err)
	}

	if err := db.QueryRowContext(context.Background(), "SELECT COALESCE(MAX(version_id), 0) FROM "+gooseVersionTable).Scan(&maxVersion); err != nil {
		t.Fatalf("max goose version: %v", err)
	}

	return count, maxVersion
}

func execOnDatabase(t *testing.T, dsn, query string) {
	t.Helper()

	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatalf("open exec db: %v", err)
	}
	defer func() { _ = db.Close() }()

	if _, err := db.ExecContext(context.Background(), query); err != nil {
		t.Fatalf("exec %q: %v", query, err)
	}
}

func TestMigrationFreshDatabase(t *testing.T) {
	env := requireIntegrationEnv(t)
	dsn := createTestDatabase(t, env, "kraclaw_mig_fresh")

	migrateDatabase(t, dsn)

	count, maxVersion := gooseVersionRows(t, dsn)
	if count != totalMigrations {
		t.Errorf("goose_db_version rows = %d, want %d", count, totalMigrations)
	}

	if want := int64(20260831000001); maxVersion != want {
		t.Errorf("max version = %d, want %d", maxVersion, want)
	}

	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatalf("open schema check db: %v", err)
	}
	defer func() { _ = db.Close() }()

	for _, table := range []string{"groups", "messages", "scheduled_tasks", "credentials"} {
		var exists bool
		if err := db.QueryRowContext(context.Background(), "SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_schema = DATABASE() AND table_name = ?)", table).Scan(&exists); err != nil {
			t.Fatalf("check table %s: %v", table, err)
		}

		if !exists {
			t.Errorf("expected table %s to exist after fresh migration", table)
		}
	}
}

func TestMigrationLegacySeed(t *testing.T) {
	env := requireIntegrationEnv(t)
	dsn := createTestDatabase(t, env, "kraclaw_mig_seed")

	migrateDatabase(t, dsn)

	execOnDatabase(t, dsn, "DROP TABLE "+gooseVersionTable)
	execOnDatabase(t, dsn, "CREATE TABLE "+legacyVersionTable+" (version bigint NOT NULL, dirty tinyint(1) NOT NULL)")
	execOnDatabase(t, dsn, "INSERT INTO "+legacyVersionTable+" (version, dirty) VALUES (20260831000001, 0)")

	migrateDatabase(t, dsn)

	count, maxVersion := gooseVersionRows(t, dsn)
	if count != totalMigrations {
		t.Errorf("seeded goose_db_version rows = %d, want %d", count, totalMigrations)
	}

	if want := int64(20260831000001); maxVersion != want {
		t.Errorf("seeded max version = %d, want %d", maxVersion, want)
	}

	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatalf("open legacy table check db: %v", err)
	}
	defer func() { _ = db.Close() }()

	var legacyExists bool
	if err := db.QueryRowContext(context.Background(), "SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_schema = DATABASE() AND table_name = ?)", legacyVersionTable).Scan(&legacyExists); err != nil {
		t.Fatalf("check legacy table: %v", err)
	}

	if !legacyExists {
		t.Errorf("schema_migrations must be left in place after seeding")
	}
}

func TestMigrationLegacyDirtyFailsFast(t *testing.T) {
	env := requireIntegrationEnv(t)
	dsn := createTestDatabase(t, env, "kraclaw_mig_dirty")

	migrateDatabase(t, dsn)

	execOnDatabase(t, dsn, "DROP TABLE "+gooseVersionTable)
	execOnDatabase(t, dsn, "CREATE TABLE "+legacyVersionTable+" (version bigint NOT NULL, dirty tinyint(1) NOT NULL)")
	execOnDatabase(t, dsn, "INSERT INTO "+legacyVersionTable+" (version, dirty) VALUES (20260831000001, 1)")

	s, err := store.NewMySQLStore(context.Background(), dsn, 2, 2, time.Minute)
	if err == nil {
		_ = s.Close()

		t.Fatal("expected dirty legacy state to fail startup, got nil error")
	}

	if !strings.Contains(err.Error(), "dirty migration state detected") {
		t.Fatalf("expected dirty migration error, got: %v", err)
	}
}

func TestMigrationIdempotent(t *testing.T) {
	env := requireIntegrationEnv(t)
	dsn := createTestDatabase(t, env, "kraclaw_mig_idempotent")

	migrateDatabase(t, dsn)
	migrateDatabase(t, dsn)

	count, _ := gooseVersionRows(t, dsn)
	if count != totalMigrations {
		t.Errorf("goose_db_version rows after second run = %d, want %d", count, totalMigrations)
	}
}
