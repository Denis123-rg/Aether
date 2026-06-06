package db

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/jackc/pgx/v5"
)

func TestListMigrationFiles_FiltersAndSorts(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{
		"0002_second.sql",
		"0001_first.sql",
		"README.md",
		"not_a_migration.sql",
		"subdir",
	} {
		path := filepath.Join(dir, name)
		if name == "subdir" {
			if err := os.Mkdir(path, 0o755); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.WriteFile(path, []byte("-- noop"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	files, err := listMigrationFiles(dir)
	if err != nil {
		t.Fatalf("listMigrationFiles: %v", err)
	}
	if len(files) != 2 {
		t.Fatalf("files = %v, want 2 migrations", files)
	}
	if filepath.Base(files[0]) != "0001_first.sql" {
		t.Fatalf("first = %s", files[0])
	}
	if filepath.Base(files[1]) != "0002_second.sql" {
		t.Fatalf("second = %s", files[1])
	}
}

func TestApplyMigrationFile_InvalidFilename(t *testing.T) {
	url := startPostgres(t)
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close(ctx)

	bad := filepath.Join(t.TempDir(), "bad_name.sql")
	if err := os.WriteFile(bad, []byte("SELECT 1;"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := applyMigrationFile(ctx, conn, bad); err == nil {
		t.Fatal("expected error for invalid migration filename")
	}
}

func TestApplyMigrationFile_InvalidSQL(t *testing.T) {
	url := startPostgres(t)
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close(ctx)
	if err := ensureMigrationsTable(ctx, conn); err != nil {
		t.Fatalf("ensure table: %v", err)
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "0099_bad_sql.sql")
	if err := os.WriteFile(path, []byte("NOT VALID SQL SYNTAX;;;"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := applyMigrationFile(ctx, conn, path); err == nil {
		t.Fatal("expected SQL apply error")
	}
}

func TestApplyMigrationFile_SkipsAlreadyApplied(t *testing.T) {
	url := startPostgres(t)
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close(ctx)
	if err := ensureMigrationsTable(ctx, conn); err != nil {
		t.Fatalf("ensure table: %v", err)
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "0100_idempotent.sql")
	if err := os.WriteFile(path, []byte("SELECT 1;"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := applyMigrationFile(ctx, conn, path); err != nil {
		t.Fatalf("first apply: %v", err)
	}
	if err := applyMigrationFile(ctx, conn, path); err != nil {
		t.Fatalf("second apply should skip: %v", err)
	}
}

func TestRunMigrations_DuplicateRunIsIdempotent(t *testing.T) {
	url := startPostgres(t)
	migrations := repoMigrationsDir(t)
	if err := RunMigrations(url, migrations); err != nil {
		t.Fatalf("first run: %v", err)
	}
	if err := RunMigrations(url, migrations); err != nil {
		t.Fatalf("second run: %v", err)
	}
}
