package db

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestRunMigrations_EmptyURLNoOp(t *testing.T) {
	if err := RunMigrations("", t.TempDir()); err != nil {
		t.Fatalf("empty url: %v", err)
	}
}

func TestRunMigrations_MissingDirectory(t *testing.T) {
	url := startPostgres(t)
	err := RunMigrations(url, filepath.Join(t.TempDir(), "does-not-exist"))
	if err == nil {
		t.Fatal("expected error for missing migrations dir")
	}
}

func TestCoveragePush_NoopLedgerAllMethods(t *testing.T) {
	l := NewNoopLedger()
	l.InsertBundle(NewBundle{BundleID: uuid.New(), ArbID: uuid.New()})
	l.InsertInclusion(NewInclusion{BundleID: uuid.New(), Builder: "b"})
	l.UpsertPnLDaily(PnLDailyDelta{Day: time.Now().UTC(), BundleCount: 1})
}

func TestListMigrationFiles_ReadError(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "not-a-dir")
	if err := os.WriteFile(bad, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := listMigrationFiles(bad)
	if err == nil {
		t.Fatal("expected read error when path is a file")
	}
}

func TestRunMigrations_AppliesPending(t *testing.T) {
	url := startPostgres(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "0100_cov_push.sql")
	if err := os.WriteFile(path, []byte(`
		CREATE TABLE IF NOT EXISTS cov_push_test (id INT PRIMARY KEY);
	`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := RunMigrations(url, dir); err != nil {
		t.Fatalf("RunMigrations: %v", err)
	}
	// Idempotent second run.
	if err := RunMigrations(url, dir); err != nil {
		t.Fatalf("RunMigrations second: %v", err)
	}
}
