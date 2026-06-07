package db

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func TestRunMigrations_EmptyURL(t *testing.T) {
	if err := RunMigrations("", repoMigrationsDir(t)); err != nil {
		t.Fatalf("empty url should be no-op: %v", err)
	}
}

func TestEnsureMigrationsTable_Idempotent(t *testing.T) {
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
	if err := ensureMigrationsTable(ctx, conn); err != nil {
		t.Fatal(err)
	}
}

func TestApplyMigrationFile_RecordInsert(t *testing.T) {
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

	dir := t.TempDir()
	path := filepath.Join(dir, "0300_record_test.sql")
	sql := `CREATE TABLE IF NOT EXISTS cov_record_test (id INT PRIMARY KEY);`
	if err := os.WriteFile(path, []byte(sql), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := applyMigrationFile(ctx, conn, path); err != nil {
		t.Fatalf("apply: %v", err)
	}

	var count int
	if err := conn.QueryRow(ctx,
		`SELECT COUNT(*) FROM _sqlx_migrations WHERE version = 300 AND success`,
	).Scan(&count); err != nil {
		t.Fatalf("query: %v", err)
	}
	if count != 1 {
		t.Fatalf("migration record count = %d", count)
	}
}

func TestPgLedger_InsertBundleWithGasUsed(t *testing.T) {
	url := startPostgres(t)
	ctx := context.Background()
	ledger, err := NewPgLedger(ctx, url, getTestLedgerMetrics())
	if err != nil {
		t.Fatalf("NewPgLedger: %v", err)
	}
	defer ledger.Close()

	gas := uint64(21000)
	ledger.InsertBundle(NewBundle{
		BundleID:    uuid.New(),
		ArbID:       uuid.New(),
		SubmittedAt: time.Now().UTC(),
		TargetBlock: 42,
		SignedTxHex: "0xbeef",
		GasUsed:     &gas,
		Builders:    []string{"flashbots"},
	})
	waitForLedgerWrite(t, ledger, 2*time.Second)
}

func TestPgLedger_DispatchCanceledContext(t *testing.T) {
	url := startPostgres(t)
	ctx := context.Background()
	ledger, err := NewPgLedger(ctx, url, getTestLedgerMetrics())
	if err != nil {
		t.Fatalf("NewPgLedger: %v", err)
	}
	ledger.dispatcherCancel()
	ledger.Close()
}

func TestPgMetricsStore_FlushOnTicker(t *testing.T) {
	url := startPostgres(t)
	ctx := context.Background()
	store, err := NewPgMetricsStore(ctx, url)
	if err != nil {
		t.Fatalf("NewPgMetricsStore: %v", err)
	}
	defer store.Close()

	store.Record(Metric{Name: "aether.test.ticker", Value: 3.14, Time: time.Now().UTC()})
	time.Sleep(150 * time.Millisecond)
}
