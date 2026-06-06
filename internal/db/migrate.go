package db

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// migrationFile matches sqlx-cli naming: 0001_description.sql
var migrationFile = regexp.MustCompile(`^(\d+)_.+\.sql$`)

// RunMigrations applies pending SQL migrations from migrationsPath against
// dbURL using the same file naming convention as sqlx-cli (`NNNN_name.sql`).
// Idempotent — tracks applied versions in `_sqlx_migrations` (sqlx-compatible).
// Returns nil when dbURL is empty or there are no pending migrations.
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

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	conn, err := pgx.Connect(ctx, dbURL)
	if err != nil {
		return fmt.Errorf("connect postgres: %w", err)
	}
	defer conn.Close(ctx)

	if err := ensureMigrationsTable(ctx, conn); err != nil {
		return err
	}

	files, err := listMigrationFiles(abs)
	if err != nil {
		return err
	}

	for _, path := range files {
		if err := applyMigrationFile(ctx, conn, path); err != nil {
			return fmt.Errorf("apply migrations: %w", err)
		}
	}
	return nil
}

func ensureMigrationsTable(ctx context.Context, conn *pgx.Conn) error {
	_, err := conn.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS _sqlx_migrations (
			version        BIGINT PRIMARY KEY,
			description    TEXT NOT NULL,
			installed_on   TIMESTAMPTZ NOT NULL DEFAULT now(),
			success        BOOLEAN NOT NULL,
			checksum       BYTEA NOT NULL,
			execution_time BIGINT NOT NULL
		)
	`)
	if err != nil {
		return fmt.Errorf("ensure _sqlx_migrations: %w", err)
	}
	return nil
}

func listMigrationFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read migrations dir: %w", err)
	}
	var files []string
	for _, e := range entries {
		if e.IsDir() || !migrationFile.MatchString(e.Name()) {
			continue
		}
		files = append(files, filepath.Join(dir, e.Name()))
	}
	sort.Strings(files)
	return files, nil
}

func applyMigrationFile(ctx context.Context, conn *pgx.Conn, path string) error {
	base := filepath.Base(path)
	m := migrationFile.FindStringSubmatch(base)
	if m == nil {
		return fmt.Errorf("invalid migration filename %q", base)
	}
	version, err := strconv.ParseInt(m[1], 10, 64)
	if err != nil {
		return fmt.Errorf("parse version from %q: %w", base, err)
	}

	var applied bool
	if err := conn.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM _sqlx_migrations WHERE version = $1 AND success)`,
		version,
	).Scan(&applied); err != nil {
		return fmt.Errorf("check migration %d: %w", version, err)
	}
	if applied {
		return nil
	}

	body, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	sum := sha256.Sum256(body)
	desc := strings.TrimSuffix(base, ".sql")

	start := time.Now()
	if _, err := conn.Exec(ctx, string(body)); err != nil {
		return fmt.Errorf("%s: %w", base, err)
	}
	elapsed := time.Since(start).Milliseconds()

	_, err = conn.Exec(ctx, `
		INSERT INTO _sqlx_migrations (version, description, success, checksum, execution_time)
		VALUES ($1, $2, true, $3, $4)
		ON CONFLICT (version) DO UPDATE SET
			success = EXCLUDED.success,
			checksum = EXCLUDED.checksum,
			execution_time = EXCLUDED.execution_time,
			installed_on = now()
	`, version, desc, sum[:], elapsed)
	if err != nil {
		return fmt.Errorf("record migration %d: %w", version, err)
	}
	return nil
}
