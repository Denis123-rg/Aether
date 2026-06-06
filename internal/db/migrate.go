package db

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
)

// RunMigrations applies all pending SQL migrations from migrationsPath against
// dbURL. Idempotent — safe to call on every executor boot. Returns nil when
// there are no pending migrations.
func RunMigrations(dbURL, migrationsPath string) error {
	if dbURL == "" {
		return nil
	}
	abs, err := filepath.Abs(migrationsPath)
	if err != nil {
		return fmt.Errorf("resolve migrations path: %w", err)
	}
	if _, err := os.Stat(abs); err != nil {
		return fmt.Errorf("migrations directory %s: %w", abs, err)
	}
	sourceURL := "file://" + filepath.ToSlash(abs)
	m, err := migrate.New(sourceURL, dbURL)
	if err != nil {
		return fmt.Errorf("create migrate instance: %w", err)
	}
	defer m.Close()

	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("apply migrations: %w", err)
	}
	return nil
}
