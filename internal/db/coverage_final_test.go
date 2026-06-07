package db

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/prometheus/client_golang/prometheus"
	promtest "github.com/prometheus/client_golang/prometheus/testutil"
)

func TestPgLedger_InsertInclusionFKViolation(t *testing.T) {
	url := startPostgres(t)
	ctx := context.Background()
	ledger, err := NewPgLedger(ctx, url, getTestLedgerMetrics())
	if err != nil {
		t.Fatalf("NewPgLedger: %v", err)
	}
	defer ledger.Close()

	// bundle_id does not exist in bundles → FK violation on inclusion_results.
	ledger.InsertInclusion(NewInclusion{
		BundleID: uuid.New(),
		Builder:  "flashbots",
		Included: false,
	})
	waitForLedgerWrite(t, ledger, 2*time.Second)

	errCount := promtest.ToFloat64(getTestLedgerMetrics().WritesTotal.WithLabelValues("insert_inclusion", "err"))
	if errCount < 1 {
		t.Fatalf("expected insert_inclusion err metric after FK violation, got %v", errCount)
	}
}

func TestPgLedger_CloseCleanDrain(t *testing.T) {
	url := startPostgres(t)
	ctx := context.Background()
	ledger, err := NewPgLedger(ctx, url, getTestLedgerMetrics())
	if err != nil {
		t.Fatalf("NewPgLedger: %v", err)
	}

	ledger.InsertBundle(NewBundle{
		BundleID:    uuid.New(),
		ArbID:       uuid.New(),
		SubmittedAt: time.Now().UTC(),
		TargetBlock: 1,
		SignedTxHex: "0x01",
	})
	waitForLedgerWrite(t, ledger, 2*time.Second)

	start := time.Now()
	ledger.Close()
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("clean Close() took %v", elapsed)
	}
}

func TestApplyMigrationFile_ReadFileError(t *testing.T) {
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
	path := filepath.Join(dir, "0200_missing_body.sql")
	if err := os.WriteFile(path, []byte("SELECT 1;"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := applyMigrationFile(ctx, conn, path); err == nil {
		t.Fatal("expected read error for deleted migration file")
	}
}

func TestRunMigrations_EmptyMigrationsDir(t *testing.T) {
	url := startPostgres(t)
	dir := t.TempDir()
	if err := RunMigrations(url, dir); err != nil {
		t.Fatalf("empty dir should succeed: %v", err)
	}
}

func TestPgMempoolReconciliation_CloseCleanDrain(t *testing.T) {
	url := startPostgres(t)
	ctx := context.Background()
	reg := prometheus.NewRegistry()
	recon, err := NewPgMempoolReconciliation(ctx, url, NewMempoolReconciliationMetrics(reg))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}

	start := time.Now()
	recon.Close()
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("clean Close() took %v", elapsed)
	}
}

func TestPgMetricsStore_CloseCleanDrain(t *testing.T) {
	url := startPostgres(t)
	ctx := context.Background()
	store, err := NewPgMetricsStore(ctx, url)
	if err != nil {
		t.Fatalf("NewPgMetricsStore: %v", err)
	}
	store.Record(Metric{Name: "test.metric", Value: 1, Time: time.Now().UTC()})

	start := time.Now()
	store.Close()
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("clean Close() took %v", elapsed)
	}
}
