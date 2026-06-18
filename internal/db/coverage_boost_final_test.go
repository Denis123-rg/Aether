package db

import (
	"context"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
)

func TestEnsureMigrationsTable_ExecError(t *testing.T) {
	url := startPostgres(t)
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close(ctx)

	cancelCtx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := ensureMigrationsTable(cancelCtx, conn); err == nil {
		t.Fatal("expected error with cancelled context")
	}
}

func TestApplyMigrationFile_CheckQueryError(t *testing.T) {
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
	path := filepath.Join(dir, "0999_check_error.sql")
	if err := os.WriteFile(path, []byte("SELECT 1;"), 0o644); err != nil {
		t.Fatal(err)
	}

	cancelCtx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := applyMigrationFile(cancelCtx, conn, path); err == nil {
		t.Fatal("expected error with cancelled context for check query")
	}
}

func TestApplyMigrationFile_RecordInsertError(t *testing.T) {
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
	path := filepath.Join(dir, "0998_record_error.sql")
	if err := os.WriteFile(path, []byte("CREATE TABLE IF NOT EXISTS cov_record_err_test (id INT PRIMARY KEY);"), 0o644); err != nil {
		t.Fatal(err)
	}

	cancelCtx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := applyMigrationFile(cancelCtx, conn, path); err == nil {
		t.Fatal("expected error with cancelled context for record insert")
	}
}

func TestRunMigrations_ApplyFileError(t *testing.T) {
	url := startPostgres(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "0500_bad_syntax.sql")
	if err := os.WriteFile(path, []byte("NOT VALID SQL SYNTAX;;;"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := RunMigrations(url, dir)
	if err == nil {
		t.Fatal("expected apply migration error")
	}
	if err.Error()[:len("apply migrations:")] != "apply migrations:" {
		t.Fatalf("unexpected error prefix: %v", err)
	}
}

func TestRunMigrations_ListFilesError(t *testing.T) {
	url := startPostgres(t)
	dir := t.TempDir()
	badPath := filepath.Join(dir, "not_a_dir_file")
	if err := os.WriteFile(badPath, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := RunMigrations(url, badPath)
	if err == nil {
		t.Fatal("expected error for unreadable migrations path")
	}
}

func TestLookupPredictionByTxHash_NullPoolAddress(t *testing.T) {
	url := startPostgres(t)
	ctx := context.Background()
	reg := prometheus.NewRegistry()
	metrics := NewMempoolReconciliationMetrics(reg)
	recon, err := NewPgMempoolReconciliation(ctx, url, metrics)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer recon.Close()

	predID := uuid.New()
	var txHash [32]byte
	copy(txHash[:], predID[:])

	_, err = recon.pool.Exec(ctx, `
		INSERT INTO mempool_predictions (
			prediction_id, pending_tx_hash, router_address, protocol,
			token_in, token_out, amount_in, predicted_target_block, predicted_post_state
		) VALUES ($1, $2, $3, 'uni_v2', $3, $3, 100, 100, '{}'::jsonb)
	`, predID, txHash[:], make([]byte, 20))
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	pred, found, err := recon.LookupPredictionByTxHash(ctx, txHash)
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if !found {
		t.Fatal("expected found")
	}
	if pred.PoolAddress != nil {
		t.Fatal("expected nil PoolAddress for NULL pool_address")
	}
	if pred.PredictedTargetBlock != 100 {
		t.Fatalf("PredictedTargetBlock = %d, want 100", pred.PredictedTargetBlock)
	}
}

func TestPgMempoolReconciliation_DispatchCtxErrSkipsInsert(t *testing.T) {
	url := startPostgres(t)
	ctx := context.Background()
	reg := prometheus.NewRegistry()
	metrics := NewMempoolReconciliationMetrics(reg)
	r := &PgMempoolReconciliation{
		ch:      make(chan NewReconciliation, 4),
		metrics: metrics,
	}
	pool, err := pgxpoolConnect(ctx, url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	r.pool = pool

	cancelCtx, cancel := context.WithCancel(context.Background())
	cancel()

	r.wg.Add(1)
	go r.dispatch(cancelCtx)

	r.InsertReconciliation(NewReconciliation{
		PredictionID: uuid.New(),
		ResolutionTs: time.Now().UTC(),
		Outcome:      OutcomeConfirmed,
	})
	time.Sleep(200 * time.Millisecond)
	close(r.ch)
	r.wg.Wait()
	r.pool.Close()
}

func pgxpoolConnect(ctx context.Context, url string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, err
	}
	return pgxpool.NewWithConfig(ctx, cfg)
}

func TestPgMetricsStore_WriterCtxCancel(t *testing.T) {
	url := startPostgres(t)
	ctx := context.Background()
	store, err := NewPgMetricsStore(ctx, url)
	if err != nil {
		t.Fatalf("NewPgMetricsStore: %v", err)
	}

	store.Record(Metric{Name: "ctx_cancel_test", Value: 1, Time: time.Now().UTC()})

	store.cancel()

	done := make(chan struct{})
	go func() {
		store.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("writer did not exit after cancel")
	}
	store.pool.Close()
}

func TestPgMetricsStore_FlushEmptyBatch(t *testing.T) {
	url := startPostgres(t)
	ctx := context.Background()
	store, err := NewPgMetricsStore(ctx, url)
	if err != nil {
		t.Fatalf("NewPgMetricsStore: %v", err)
	}
	defer store.Close()

	store.flush(ctx, nil)
	store.flush(ctx, []Metric{})
}

func TestPgMetricsStore_BatchFullFlush(t *testing.T) {
	s := &PgMetricsStore{
		ch:     make(chan Metric, metricsBatchSize+10),
		cancel: func() {},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.wg.Add(1)
	go s.run(ctx)

	now := time.Now().UTC()
	for i := 0; i < metricsBatchSize; i++ {
		s.Record(Metric{Name: "batch_full", Value: float64(i), Time: now})
	}

	time.Sleep(300 * time.Millisecond)
	close(s.ch)
	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		cancel()
		t.Fatal("writer did not exit")
	}
}

func TestPgMetricsStore_FlushExecError(t *testing.T) {
	store := &PgMetricsStore{
		pool: nil,
	}
	store.flush(context.Background(), []Metric{{Name: "x", Value: 1, Time: time.Now().UTC()}})
}

func TestPgLedger_DispatchCtxCancelDuringSemaphore(t *testing.T) {
	url := startPostgres(t)
	ctx := context.Background()
	pool, err := pgxpoolConnect(ctx, url)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	defer pool.Close()

	dispatcherCtx, dispatcherCancel := context.WithCancel(context.Background())
	l := &PgLedger{
		pool:             pool,
		ch:               make(chan ledgerOp, ledgerChannelCapacity),
		metrics:          getTestLedgerMetrics(),
		dispatcherCancel: dispatcherCancel,
	}

	l.wg.Add(1)
	go l.dispatch(dispatcherCtx)

	dispatcherCancel()
	close(l.ch)

	done := make(chan struct{})
	go func() {
		l.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("dispatcher did not exit after cancel")
	}
}

func TestNewPgLedger_ParseConfigError(t *testing.T) {
	ctx := context.Background()
	_, err := NewPgLedger(ctx, "not-a-valid-url://", getTestLedgerMetrics())
	if err == nil {
		t.Fatal("expected parse config error")
	}
}

func TestNewPgMempoolReconciliation_ParseConfigError(t *testing.T) {
	ctx := context.Background()
	_, err := NewPgMempoolReconciliation(ctx, "not-a-valid-url://",
		NewMempoolReconciliationMetrics(prometheus.NewRegistry()))
	if err == nil {
		t.Fatal("expected parse config error")
	}
}

func TestNewPgMetricsStore_ParseConfigError(t *testing.T) {
	ctx := context.Background()
	_, err := NewPgMetricsStore(ctx, "not-a-valid-url://")
	if err == nil {
		t.Fatal("expected parse config error")
	}
}

func TestRunMigrations_EnsureTableError(t *testing.T) {
	url := startPostgres(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "0001_test.sql")
	if err := os.WriteFile(path, []byte("SELECT 1;"), 0o644); err != nil {
		t.Fatal(err)
	}

	conn, err := pgx.Connect(context.Background(), url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close(context.Background())

	_, err = conn.Exec(context.Background(), `DROP TABLE IF EXISTS _sqlx_migrations`)
	if err != nil {
		t.Fatal(err)
	}

	_, err = conn.Exec(context.Background(), `CREATE TABLE _sqlx_migrations (version INT PRIMARY KEY)`)
	if err != nil {
		t.Fatal(err)
	}

	err = RunMigrations(url, dir)
	if err == nil {
		t.Fatal("expected migration error")
	}
}

func TestPgLedger_InsertInclusionNilOptionalFields(t *testing.T) {
	url := startPostgres(t)
	ctx := context.Background()
	ledger, err := NewPgLedger(ctx, url, getTestLedgerMetrics())
	if err != nil {
		t.Fatalf("NewPgLedger: %v", err)
	}
	defer ledger.Close()

	bundleID := uuid.New()
	poolSeed, _ := pgxpoolConnect(ctx, url)
	_, err = poolSeed.Exec(ctx, `
		INSERT INTO bundles (bundle_id, arb_id, submitted_at, target_block, signed_tx_hex, is_shadow, builders)
		VALUES ($1, $2, now(), 1, '0x01', false, '["eden"]'::jsonb)
	`, bundleID, uuid.New())
	poolSeed.Close()
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	ledger.InsertInclusion(NewInclusion{
		BundleID:   bundleID,
		Builder:    "flashbots",
		Included:   false,
		Error:      nil,
		ResolvedAt: time.Now().UTC(),
	})
	waitForLedgerWrite(t, ledger, 2*time.Second)
}

func TestPgLedger_UpsertPnLDailyNilValues(t *testing.T) {
	url := startPostgres(t)
	ctx := context.Background()
	ledger, err := NewPgLedger(ctx, url, getTestLedgerMetrics())
	if err != nil {
		t.Fatalf("NewPgLedger: %v", err)
	}
	defer ledger.Close()

	ledger.UpsertPnLDaily(PnLDailyDelta{
		Day:               time.Now().UTC(),
		RealizedProfitWei: nil,
		GasSpentWei:       nil,
		BundleCount:       0,
		InclusionCount:    0,
	})
	waitForLedgerWrite(t, ledger, 2*time.Second)
}

func TestPgMempoolReconciliation_InsertWithAllOptionalFieldsNil(t *testing.T) {
	url := startPostgres(t)
	ctx := context.Background()
	reg := prometheus.NewRegistry()
	metrics := NewMempoolReconciliationMetrics(reg)
	recon, err := NewPgMempoolReconciliation(ctx, url, metrics)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer recon.Close()

	predID := uuid.New()
	var txHash [32]byte
	copy(txHash[:], predID[:])

	_, err = recon.pool.Exec(ctx, `
		INSERT INTO mempool_predictions (
			prediction_id, pending_tx_hash, router_address, protocol,
			token_in, token_out, amount_in, predicted_target_block, predicted_post_state
		) VALUES ($1, $2, $3, 'uni_v2', $3, $3, 100, 100, '{}'::jsonb)
	`, predID, txHash[:], make([]byte, 20))
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	recon.InsertReconciliation(NewReconciliation{
		PredictionID:      predID,
		ResolutionTs:      time.Now().UTC(),
		Outcome:           OutcomeDropped,
		ActualTargetBlock: nil,
		ActualTxIndex:     nil,
		BlockDelta:        nil,
		OrderingCorrect:   nil,
		PoolPathCorrect:   nil,
		ReplacedByTxHash:  nil,
		FailureReason:     strPtr("test failure"),
	})
	time.Sleep(300 * time.Millisecond)
}

func TestPgLedger_InsertBundleBuildersEmpty(t *testing.T) {
	url := startPostgres(t)
	ctx := context.Background()
	ledger, err := NewPgLedger(ctx, url, getTestLedgerMetrics())
	if err != nil {
		t.Fatalf("NewPgLedger: %v", err)
	}
	defer ledger.Close()

	ledger.InsertBundle(NewBundle{
		BundleID:    uuid.New(),
		ArbID:       uuid.New(),
		SubmittedAt: time.Now().UTC(),
		TargetBlock: 100,
		SignedTxHex: "0xaa",
		IsShadow:    false,
		Builders:    []string{},
	})
	waitForLedgerWrite(t, ledger, 2*time.Second)
}

func TestBigIntToString_NegativeValue(t *testing.T) {
	v := big.NewInt(-1)
	if got := bigIntToString(v); got != "-1" {
		t.Fatalf("bigIntToString(-1) = %q, want %q", got, "-1")
	}
}

func TestPgMempoolReconciliation_LookupWithPoolAddress(t *testing.T) {
	url := startPostgres(t)
	ctx := context.Background()
	reg := prometheus.NewRegistry()
	metrics := NewMempoolReconciliationMetrics(reg)
	recon, err := NewPgMempoolReconciliation(ctx, url, metrics)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer recon.Close()

	predID := uuid.New()
	var txHash [32]byte
	copy(txHash[:], predID[:])
	poolAddr := [20]byte{0xAA, 0xBB}

	_, err = recon.pool.Exec(ctx, `
		INSERT INTO mempool_predictions (
			prediction_id, pending_tx_hash, router_address, protocol,
			token_in, token_out, amount_in, pool_address,
			predicted_target_block, predicted_post_state
		) VALUES ($1, $2, $3, 'uni_v2', $3, $3, 100, $4, 200, '{}'::jsonb)
	`, predID, txHash[:], make([]byte, 20), poolAddr[:])
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	pred, found, err := recon.LookupPredictionByTxHash(ctx, txHash)
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if !found {
		t.Fatal("expected found")
	}
	if pred.PoolAddress == nil {
		t.Fatal("expected non-nil PoolAddress")
	}
	if *pred.PoolAddress != poolAddr {
		t.Fatalf("PoolAddress mismatch: %v vs %v", *pred.PoolAddress, poolAddr)
	}
	if pred.PredictedTargetBlock != 200 {
		t.Fatalf("PredictedTargetBlock = %d, want 200", pred.PredictedTargetBlock)
	}
}

func TestPgMetricsStore_RecordAndFlushCycle(t *testing.T) {
	url := startPostgres(t)
	ctx := context.Background()
	store, err := NewPgMetricsStore(ctx, url)
	if err != nil {
		t.Fatalf("NewPgMetricsStore: %v", err)
	}
	defer store.Close()

	now := time.Now().UTC()
	for i := 0; i < 10; i++ {
		store.Record(Metric{
			Name:  fmt.Sprintf("cycle_test_%d", i),
			Value: float64(i),
			Time:  now,
			Tags:  map[string]string{"cycle": "test"},
		})
	}
	time.Sleep(1500 * time.Millisecond)
}

func TestPgMetricsStore_LogDropsIfGrownAfterRecord(t *testing.T) {
	s := &PgMetricsStore{ch: make(chan Metric, 2)}
	for i := 0; i < 20; i++ {
		s.Record(Metric{Name: "saturation", Value: float64(i)})
	}
	s.logDropsIfGrown()
	s.logDropsIfGrown()
}

func TestRunMigrations_AppliesMultipleFilesInOrder(t *testing.T) {
	url := startPostgres(t)
	dir := t.TempDir()
	sql1 := `CREATE TABLE IF NOT EXISTS multi_order_a (id INT);`
	sql2 := `CREATE TABLE IF NOT EXISTS multi_order_b (id INT);`
	os.WriteFile(filepath.Join(dir, "0001_a.sql"), []byte(sql1), 0o644)
	os.WriteFile(filepath.Join(dir, "0002_b.sql"), []byte(sql2), 0o644)

	if err := RunMigrations(url, dir); err != nil {
		t.Fatalf("first run: %v", err)
	}
	if err := RunMigrations(url, dir); err != nil {
		t.Fatalf("idempotent second run: %v", err)
	}
}

func TestPgMetricsStore_RecordBatchExceedsCapacity(t *testing.T) {
	s := &PgMetricsStore{ch: make(chan Metric, 1)}
	for i := 0; i < 100; i++ {
		s.Record(Metric{Name: "overflow_test", Value: float64(i)})
	}
	if s.dropped.Load() != 99 {
		t.Fatalf("dropped = %d, want 99", s.dropped.Load())
	}
}
