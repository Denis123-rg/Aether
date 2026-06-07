package db

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/prometheus/client_golang/prometheus"
)

func TestListMigrationFiles_SkipsNonSQL(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "0001_valid.sql"), []byte("SELECT 1;"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "subdir"), 0o755); err != nil {
		t.Fatal(err)
	}
	files, err := listMigrationFiles(dir)
	if err != nil {
		t.Fatalf("listMigrationFiles: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("files = %v", files)
	}
}

func TestApplyMigrationFile_InvalidFilename_Push3(t *testing.T) {
	url := startPostgres(t)
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close(ctx)
	if err := ensureMigrationsTable(ctx, conn); err != nil {
		t.Fatal(err)
	}
	bad := filepath.Join(t.TempDir(), "badname.sql")
	if err := os.WriteFile(bad, []byte("SELECT 1;"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := applyMigrationFile(ctx, conn, bad); err == nil {
		t.Fatal("expected invalid filename error")
	}
}

func TestRunMigrations_ConnectFailure(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "0001_test.sql")
	if err := os.WriteFile(path, []byte("SELECT 1;"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := RunMigrations("postgres://127.0.0.1:1/none?connect_timeout=1", dir)
	if err == nil {
		t.Fatal("expected connect error")
	}
}

func TestNewPgMempoolReconciliation_InvalidURL(t *testing.T) {
	ctx := context.Background()
	_, err := NewPgMempoolReconciliation(ctx, "postgres://127.0.0.1:1/none?connect_timeout=1",
		NewMempoolReconciliationMetrics(prometheus.NewRegistry()))
	if err == nil {
		t.Fatal("expected connect error")
	}
}

func TestPgMempoolReconciliation_LookupMissingRow(t *testing.T) {
	url := startPostgres(t)
	ctx := context.Background()
	reg := prometheus.NewRegistry()
	recon, err := NewPgMempoolReconciliation(ctx, url, NewMempoolReconciliationMetrics(reg))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer recon.Close()

	var txHash [32]byte
	txHash[0] = 0xab
	_, found, err := recon.LookupPredictionByTxHash(ctx, txHash)
	if err != nil {
		t.Fatalf("LookupPredictionByTxHash: %v", err)
	}
	if found {
		t.Fatal("expected not found for random hash")
	}
}
