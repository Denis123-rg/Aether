package db

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
)

func TestPgLedger_DispatchCtxDoneDuringSemaphore(t *testing.T) {
	url := startPostgres(t)
	ctx := context.Background()
	metrics := getTestLedgerMetrics()

	ledger, err := NewPgLedger(ctx, url, metrics)
	if err != nil {
		t.Fatal(err)
	}

	ledger.dispatcherCancel()

	for i := 0; i < ledgerMaxInflight+10; i++ {
		ledger.InsertInclusion(NewInclusion{
			BundleID: uuid.New(),
			Builder:  "test",
		})
	}

	done := make(chan struct{})
	go func() {
		ledger.Close()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Close() hung with cancelled dispatcher context")
	}
}

func TestNewPgLedger_ConnectErr(t *testing.T) {
	ctx := context.Background()
	metrics := getTestLedgerMetrics()

	_, err := NewPgLedger(ctx, "postgres://127.0.0.1:1/none?connect_timeout=1", metrics)
	if err == nil {
		t.Fatal("expected connect error")
	}
}

func TestNewPgLedger_ParseErr(t *testing.T) {
	ctx := context.Background()
	metrics := getTestLedgerMetrics()

	_, err := NewPgLedger(ctx, "://invalid-url", metrics)
	if err == nil {
		t.Fatal("expected parse error")
	}
}

func TestRunMigrations_ConnectErr(t *testing.T) {
	err := RunMigrations("postgres://127.0.0.1:1/none?connect_timeout=1", t.TempDir())
	if err == nil {
		t.Fatal("expected connect error for unreachable DB")
	}
}

func TestRunMigrations_ConnectErrFullPath(t *testing.T) {
	dir := t.TempDir()
	sql := filepath.Join(dir, "0001_test.sql")
	if err := os.WriteFile(sql, []byte("SELECT 1;"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := RunMigrations("postgres://127.0.0.1:1/none?connect_timeout=1", dir)
	if err == nil {
		t.Fatal("expected connect error")
	}
}

func TestApplyMigrationFile_ReadErrNonExistent(t *testing.T) {
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

	if err := applyMigrationFile(ctx, conn, "/nonexistent/0199_gone.sql"); err == nil {
		t.Fatal("expected error for non-existent migration file")
	}
}

func TestApplyMigrationFile_SkipsApplied(t *testing.T) {
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
	path := filepath.Join(dir, "0200_skip_test.sql")
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

func TestPgMempoolReconciliation_NewConnErr(t *testing.T) {
	ctx := context.Background()
	reg := prometheus.NewRegistry()
	metrics := NewMempoolReconciliationMetrics(reg)

	_, err := NewPgMempoolReconciliation(ctx, "postgres://127.0.0.1:1/none?connect_timeout=1", metrics)
	if err == nil {
		t.Fatal("expected connect error")
	}
}

func TestPgMempoolReconciliation_NewParseErr(t *testing.T) {
	ctx := context.Background()
	reg := prometheus.NewRegistry()
	metrics := NewMempoolReconciliationMetrics(reg)

	_, err := NewPgMempoolReconciliation(ctx, "://invalid-url", metrics)
	if err == nil {
		t.Fatal("expected parse error")
	}
}

func TestPgLedger_InsertBundleNonNilGas(t *testing.T) {
	url := startPostgres(t)
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	l := &PgLedger{pool: pool, metrics: getTestLedgerMetrics()}
	gas := uint64(21000)
	b := &NewBundle{
		BundleID:    uuid.New(),
		ArbID:       uuid.New(),
		SubmittedAt: time.Now().UTC(),
		TargetBlock: 1,
		SignedTxHex: "0xdead",
		GasUsed:     &gas,
		Builders:    []string{"flashbots"},
	}
	if err := l.insertBundleInner(ctx, b); err != nil {
		t.Fatalf("insertBundleInner: %v", err)
	}
}

func TestPgLedger_RunOneBadOp(t *testing.T) {
	url := startPostgres(t)
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	l := &PgLedger{pool: pool, metrics: getTestLedgerMetrics()}
	l.runOne(ctx, ledgerOp{kind: "bogus_op_kind_" + fmt.Sprintf("%d", time.Now().UnixNano())})
}

func TestPgLedger_CloseTimeoutPath(t *testing.T) {
	url := startPostgres(t)
	ctx := context.Background()
	metrics := getTestLedgerMetrics()

	ledger, err := NewPgLedger(ctx, url, metrics)
	if err != nil {
		t.Fatal(err)
	}

	origTimeout := ledgerCloseDrainTimeout
	ledgerCloseDrainTimeout = 10 * time.Millisecond
	defer func() { ledgerCloseDrainTimeout = origTimeout }()

	for i := 0; i < ledgerMaxInflight; i++ {
		ledger.InsertBundle(NewBundle{
			BundleID: uuid.New(),
			ArbID:    uuid.New(),
		})
	}

	time.Sleep(50 * time.Millisecond)

	ledger.dispatcherCancel()

	done := make(chan struct{})
	go func() {
		ledger.Close()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Close() hung on timeout path")
	}
}

func TestPgLedger_EnqueueDropMetrics(t *testing.T) {
	metrics := getTestLedgerMetrics()
	l := &PgLedger{
		ch:      make(chan ledgerOp, 1),
		metrics: metrics,
	}
	for i := 0; i < 5; i++ {
		l.enqueue(ledgerOp{kind: "insert_bundle", bundle: &NewBundle{BundleID: uuid.New()}})
	}
	if len(l.ch) != 1 {
		t.Fatalf("channel len = %d, want 1", len(l.ch))
	}
}

func TestPgMempoolReconciliation_InsertDropMetrics(t *testing.T) {
	reg := prometheus.NewRegistry()
	metrics := NewMempoolReconciliationMetrics(reg)
	r := &PgMempoolReconciliation{
		ch:      make(chan NewReconciliation, 1),
		metrics: metrics,
	}
	for i := 0; i < 5; i++ {
		r.InsertReconciliation(NewReconciliation{
			PredictionID: uuid.New(),
			Outcome:      OutcomeConfirmed,
		})
	}
	if len(r.ch) != 1 {
		t.Fatalf("channel len = %d, want 1", len(r.ch))
	}
}

func TestApplyMigrationFile_BadFilename(t *testing.T) {
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

	bad := filepath.Join(t.TempDir(), "bad_name.sql")
	if err := os.WriteFile(bad, []byte("SELECT 1;"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := applyMigrationFile(ctx, conn, bad); err == nil {
		t.Fatal("expected error for invalid migration filename")
	}
}

func TestApplyMigrationFile_InvalidSQLSyntax(t *testing.T) {
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
	path := filepath.Join(dir, "0099_bad_syntax.sql")
	if err := os.WriteFile(path, []byte("NOT VALID SQL SYNTAX;;;"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := applyMigrationFile(ctx, conn, path); err == nil {
		t.Fatal("expected SQL apply error")
	}
}
