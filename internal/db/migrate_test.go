package db

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRunMigrationsEmptyURL(t *testing.T) {
	if err := RunMigrations("", "migrations"); err != nil {
		t.Fatalf("empty url: %v", err)
	}
}

func TestRunMigrationsMissingDir(t *testing.T) {
	err := RunMigrations("postgres://localhost:5432/aether_test", "/nonexistent/migrations")
	if err == nil {
		t.Fatal("expected error for missing migrations dir")
	}
}

func TestRunMigrationsResolvesWorkspaceMigrations(t *testing.T) {
	// Without a live Postgres this only validates path resolution + migrate init.
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	// Walk up to repo root from internal/db.
	root := filepath.Join(wd, "..", "..")
	migrations := filepath.Join(root, "migrations")
	if _, err := os.Stat(migrations); err != nil {
		t.Skip("migrations dir not found:", err)
	}
	// Invalid DB — expect connect error, not path error.
	err = RunMigrations("postgres://127.0.0.1:59999/none?sslmode=disable", migrations)
	if err == nil {
		t.Fatal("expected connect error")
	}
}
