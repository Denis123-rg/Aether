package db

import (
	"context"
	"math/big"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

// testLedgerMetrics is registered once per process — NewLedgerMetrics panics on
// duplicate registration against the default Prometheus registry.
var (
	testLedgerMetrics     *LedgerMetrics
	testLedgerMetricsOnce sync.Once
)

func getTestLedgerMetrics() *LedgerMetrics {
	testLedgerMetricsOnce.Do(func() {
		testLedgerMetrics = NewLedgerMetrics()
	})
	return testLedgerMetrics
}

func dockerAvailable() bool {
	if os.Getenv("AETHER_SKIP_TESTCONTAINERS") == "1" {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err := testcontainers.NewDockerClientWithOpts(ctx)
	return err == nil
}

func repoMigrationsDir(t *testing.T) string {
	t.Helper()
	// `go test` sets cwd to the package directory (internal/db).
	dir, err := filepath.Abs(filepath.Join("..", "..", "migrations"))
	if err != nil {
		t.Fatalf("abs migrations: %v", err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("migrations dir %s: %v", dir, err)
	}
	return dir
}

// startPostgres spins up a disposable Postgres 16 container, applies all SQL
// migrations, and returns a pgx-compatible connection URL. Skips when Docker
// is unavailable (local dev without Docker); CI runners provide Docker.
func startPostgres(t *testing.T) string {
	t.Helper()
	if !dockerAvailable() {
		t.Skip("Docker unavailable — set AETHER_SKIP_TESTCONTAINERS=1 to silence")
	}

	ctx := context.Background()
	container, err := postgres.Run(ctx,
		"postgres:16-alpine",
		postgres.WithDatabase("aether_test"),
		postgres.WithUsername("aether"),
		postgres.WithPassword("aether"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(60*time.Second),
		),
	)
	if err != nil {
		t.Fatalf("start postgres container: %v", err)
	}
	t.Cleanup(func() {
		if err := testcontainers.TerminateContainer(container); err != nil {
			t.Logf("terminate postgres: %v", err)
		}
	})

	connStr, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("connection string: %v", err)
	}

	if err := RunMigrations(connStr, repoMigrationsDir(t)); err != nil {
		t.Fatalf("run migrations: %v", err)
	}
	return connStr
}

func waitForLedgerWrite(t *testing.T, _ *PgLedger, timeout time.Duration) {
	t.Helper()
	// Writer goroutine drains asynchronously; bounded sleep is enough for
	// local testcontainers Postgres (sub-ms inserts).
	time.Sleep(min(timeout, 500*time.Millisecond))
}

func TestPostgres_RunMigrations_Idempotent(t *testing.T) {
	url := startPostgres(t)
	migrations := repoMigrationsDir(t)
	if err := RunMigrations(url, migrations); err != nil {
		t.Fatalf("second migration run: %v", err)
	}
}

func TestPostgres_PgLedger_InsertBundleAndQuery(t *testing.T) {
	url := startPostgres(t)
	ctx := context.Background()
	metrics := getTestLedgerMetrics()

	ledger, err := NewPgLedger(ctx, url, metrics)
	if err != nil {
		t.Fatalf("NewPgLedger: %v", err)
	}
	defer ledger.Close()

	bundleID := uuid.New()
	arbID := ArbIDFromOppID("postgres-test-bundle")
	ledger.InsertBundle(NewBundle{
		BundleID:    bundleID,
		ArbID:       arbID,
		SubmittedAt: time.Now().UTC().Truncate(time.Microsecond),
		TargetBlock: 18_000_000,
		SignedTxHex: "0xdeadbeef",
		GasUsed:     ptrU64(21000),
		IsShadow:    true,
		Builders:    []string{"flashbots", "titan"},
	})
	waitForLedgerWrite(t, ledger, 5*time.Second)

	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	defer pool.Close()

	var gotHex string
	var gotShadow bool
	var builders string
	err = pool.QueryRow(ctx, `
		SELECT signed_tx_hex, is_shadow, builders::text
		FROM bundles WHERE bundle_id = $1`, bundleID).Scan(&gotHex, &gotShadow, &builders)
	if err != nil {
		t.Fatalf("query bundle: %v", err)
	}
	if gotHex != "0xdeadbeef" || !gotShadow {
		t.Fatalf("bundle row mismatch: hex=%s shadow=%v", gotHex, gotShadow)
	}
	if builders == "" {
		t.Fatal("builders jsonb empty")
	}
}

func TestPostgres_PgLedger_InsertInclusionUpsert(t *testing.T) {
	url := startPostgres(t)
	ctx := context.Background()
	metrics := getTestLedgerMetrics()

	ledger, err := NewPgLedger(ctx, url, metrics)
	if err != nil {
		t.Fatalf("NewPgLedger: %v", err)
	}
	defer ledger.Close()

	bundleID := uuid.New()
	// Seed bundle synchronously so inclusion FK is satisfied before async writes.
	poolSeed, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatalf("seed pool: %v", err)
	}
	_, err = poolSeed.Exec(ctx, `
		INSERT INTO bundles (bundle_id, arb_id, submitted_at, target_block, signed_tx_hex, is_shadow, builders)
		VALUES ($1, $2, now(), 1, '0x01', false, '["eden"]'::jsonb)
	`, bundleID, uuid.New())
	poolSeed.Close()
	if err != nil {
		t.Fatalf("seed bundle: %v", err)
	}

	var txHash [32]byte
	txHash[0] = 0xab
	errMsg := "not included"
	ledger.InsertInclusion(NewInclusion{
		BundleID:      bundleID,
		Builder:       "flashbots",
		Included:      false,
		IncludedBlock: ptrU64(18_000_001),
		LandedTxHash:  &txHash,
		Error:         &errMsg,
		ResolvedAt:    time.Now().UTC(),
	})

	// Upsert same (bundle, builder) with included=true. Wait for the first
	// write to land before enqueueing the second — concurrent writer goroutines
	// can otherwise race two INSERT..ON CONFLICT statements.
	time.Sleep(300 * time.Millisecond)
	ledger.InsertInclusion(NewInclusion{
		BundleID:   bundleID,
		Builder:    "flashbots",
		Included:   true,
		ResolvedAt: time.Now().UTC(),
	})

	pool, _ := pgxpool.New(ctx, url)
	defer pool.Close()

	var included bool
	var errText *string
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		err = pool.QueryRow(ctx, `
			SELECT included, error FROM inclusion_results
			WHERE bundle_id = $1 AND builder = $2`, bundleID, "flashbots").Scan(&included, &errText)
		if err == nil && included {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("query inclusion: %v", err)
	}
	if !included {
		t.Fatal("upsert should set included=true")
	}
	if errText != nil {
		t.Fatalf("upsert should clear error to NULL, got %q", *errText)
	}
}

func TestPostgres_PgLedger_UpsertPnLDailyAccumulates(t *testing.T) {
	url := startPostgres(t)
	ctx := context.Background()
	metrics := getTestLedgerMetrics()

	ledger, err := NewPgLedger(ctx, url, metrics)
	if err != nil {
		t.Fatalf("NewPgLedger: %v", err)
	}
	defer ledger.Close()

	day := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	ledger.UpsertPnLDaily(PnLDailyDelta{
		Day:               day,
		RealizedProfitWei: big.NewInt(1_000),
		GasSpentWei:       big.NewInt(100),
		BundleCount:       1,
		InclusionCount:    0,
	})
	ledger.UpsertPnLDaily(PnLDailyDelta{
		Day:               day,
		RealizedProfitWei: big.NewInt(2_000),
		GasSpentWei:       big.NewInt(50),
		BundleCount:       2,
		InclusionCount:    1,
	})
	waitForLedgerWrite(t, ledger, 5*time.Second)

	pool, _ := pgxpool.New(ctx, url)
	defer pool.Close()

	var profit, gas string
	var bundles, inclusions int64
	err = pool.QueryRow(ctx, `
		SELECT realized_profit_wei::text, gas_spent_wei::text, bundle_count, inclusion_count
		FROM pnl_daily WHERE day = $1::date`, day.Format("2006-01-02")).Scan(&profit, &gas, &bundles, &inclusions)
	if err != nil {
		t.Fatalf("query pnl_daily: %v", err)
	}
	if profit != "3000" || gas != "150" || bundles != 3 || inclusions != 1 {
		t.Fatalf("pnl_daily = profit %s gas %s bundles %d inclusions %d", profit, gas, bundles, inclusions)
	}
}

func TestPostgres_PgLedger_ConcurrentWrites(t *testing.T) {
	url := startPostgres(t)
	ctx := context.Background()
	metrics := getTestLedgerMetrics()

	ledger, err := NewPgLedger(ctx, url, metrics)
	if err != nil {
		t.Fatalf("NewPgLedger: %v", err)
	}
	defer ledger.Close()

	const n = 50
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ledger.InsertBundle(NewBundle{
				BundleID:    uuid.New(),
				ArbID:       uuid.New(),
				SubmittedAt: time.Now().UTC(),
				TargetBlock: uint64(i),
				SignedTxHex: "0xcc",
				Builders:    []string{"flashbots"},
			})
		}(i)
	}
	wg.Wait()
	waitForLedgerWrite(t, ledger, 10*time.Second)

	pool, _ := pgxpool.New(ctx, url)
	defer pool.Close()
	var count int64
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM bundles`).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != n {
		t.Fatalf("bundle count = %d, want %d", count, n)
	}
	_ = metrics
}

func TestPostgres_PgMetricsStore_RecordFlushAndQuery(t *testing.T) {
	url := startPostgres(t)
	ctx := context.Background()

	store, err := NewPgMetricsStore(ctx, url)
	if err != nil {
		t.Fatalf("NewPgMetricsStore: %v", err)
	}
	defer store.Close()

	ts := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)
	store.Record(Metric{Time: ts, Name: "bundle_latency_ms", Value: 12.5, Tags: map[string]string{"builder": "titan"}})
	store.Record(Metric{Name: "pnl_realized_wei", Value: 0.42})
	// Flush interval is 1s; wait for ticker-driven batch write.
	time.Sleep(1500 * time.Millisecond)

	pool, _ := pgxpool.New(ctx, url)
	defer pool.Close()

	var count int64
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM metrics`).Scan(&count); err != nil {
		t.Fatalf("count metrics: %v", err)
	}
	if count < 2 {
		t.Fatalf("expected >=2 metric rows, got %d", count)
	}
}

func TestPostgres_PgMetricsStore_OverflowDrops(t *testing.T) {
	s := &PgMetricsStore{ch: make(chan Metric, 2)}
	for i := 0; i < 10; i++ {
		s.Record(Metric{Name: "overflow", Value: float64(i)})
	}
	if got := s.dropped.Load(); got != 8 {
		t.Fatalf("dropped = %d, want 8", got)
	}
}

func TestPostgres_PgMetricsStore_Ping(t *testing.T) {
	url := startPostgres(t)
	ctx := context.Background()

	store, err := NewPgMetricsStore(ctx, url)
	if err != nil {
		t.Fatalf("NewPgMetricsStore: %v", err)
	}
	defer store.Close()

	if err := store.Ping(ctx); err != nil {
		t.Fatalf("Ping: %v", err)
	}
}

func TestPostgres_LedgerFromEnv_Connects(t *testing.T) {
	url := startPostgres(t)
	ctx := context.Background()
	metrics := getTestLedgerMetrics()

	ledger := LedgerFromEnv(ctx, url, metrics)
	defer func() {
		if pg, ok := ledger.(*PgLedger); ok {
			pg.Close()
		}
	}()
	if _, ok := ledger.(*PgLedger); !ok {
		t.Fatalf("expected PgLedger, got %T", ledger)
	}
}

func TestPostgres_MetricsStoreFromEnv_Connects(t *testing.T) {
	url := startPostgres(t)
	ctx := context.Background()

	store := MetricsStoreFromEnv(ctx, url)
	defer store.Close()
	if _, ok := store.(*PgMetricsStore); !ok {
		t.Fatalf("expected PgMetricsStore, got %T", store)
	}
}

func TestPostgres_NewPgLedger_InvalidURL(t *testing.T) {
	ctx := context.Background()
	_, err := NewPgLedger(ctx, "postgres://127.0.0.1:1/none?connect_timeout=1", getTestLedgerMetrics())
	if err == nil {
		t.Fatal("expected connect error for invalid URL")
	}
}

func TestPgLedger_EnqueueDropsWhenChannelFull(t *testing.T) {
	metrics := getTestLedgerMetrics()
	l := &PgLedger{
		ch:      make(chan ledgerOp, 2),
		metrics: metrics,
	}
	for i := 0; i < 5; i++ {
		l.enqueue(ledgerOp{kind: "insert_bundle", bundle: &NewBundle{BundleID: uuid.New()}})
	}
	// Channel capacity is 2; 3+ ops must be dropped without blocking.
	if len(l.ch) != 2 {
		t.Fatalf("channel len = %d, want 2 (remaining capacity after drops)", len(l.ch))
	}
}

func TestPgLedger_RunOneUnknownOp(t *testing.T) {
	url := startPostgres(t)
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	defer pool.Close()

	l := &PgLedger{pool: pool, metrics: getTestLedgerMetrics()}
	l.runOne(ctx, ledgerOp{kind: "bogus_op"})
	// err path increments writes_total{op,err} — no panic is success.
}

func TestPgLedger_InsertBundleInnerNilGasUsed(t *testing.T) {
	url := startPostgres(t)
	ctx := context.Background()
	pool, _ := pgxpool.New(ctx, url)
	defer pool.Close()

	l := &PgLedger{pool: pool, metrics: getTestLedgerMetrics()}
	b := &NewBundle{
		BundleID:    uuid.New(),
		ArbID:       uuid.New(),
		SubmittedAt: time.Now().UTC(),
		TargetBlock: 1,
		SignedTxHex: "0x00",
		Builders:    nil,
	}
	if err := l.insertBundleInner(ctx, b); err != nil {
		t.Fatalf("insertBundleInner: %v", err)
	}
}

func TestPostgres_MempoolReconciliation_LookupMiss(t *testing.T) {
	url := startPostgres(t)
	ctx := context.Background()
	reg := prometheus.NewRegistry()
	metrics := NewMempoolReconciliationMetrics(reg)
	recon, err := NewPgMempoolReconciliation(ctx, url, metrics)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer recon.Close()

	var missing [32]byte
	_, ok, err := recon.LookupPredictionByTxHash(ctx, missing)
	if err != nil || ok {
		t.Fatalf("lookup miss: ok=%v err=%v", ok, err)
	}
}

func TestPostgres_MempoolReconciliation_MarkStaleAsDropped(t *testing.T) {
	url := startPostgres(t)
	ctx := context.Background()
	reg := prometheus.NewRegistry()
	metrics := NewMempoolReconciliationMetrics(reg)
	recon, err := NewPgMempoolReconciliation(ctx, url, metrics)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer recon.Close()

	pool, _ := pgxpool.New(ctx, url)
	defer pool.Close()

	staleID := uuid.New()
	_, err = pool.Exec(ctx, `
		INSERT INTO mempool_predictions (
			prediction_id, pending_tx_hash, router_address, protocol,
			token_in, token_out, amount_in, predicted_target_block, predicted_post_state
		) VALUES ($1, $2, $3, 'uni_v2', $3, $3, 1, 100, '{}'::jsonb)
	`, staleID, []byte{0xaa}, make([]byte, 20))
	if err != nil {
		t.Fatalf("seed stale prediction: %v", err)
	}

	rows, err := recon.MarkStaleAsDropped(ctx, StaleConfirmationWindow+200)
	if err != nil {
		t.Fatalf("MarkStaleAsDropped: %v", err)
	}
	if rows < 1 {
		t.Fatalf("expected >=1 dropped rows, got %d", rows)
	}
}

func TestPgMempoolReconciliation_EnqueueDropsWhenFull(t *testing.T) {
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

func TestPostgres_MempoolReconciliation_Insert(t *testing.T) {
	url := startPostgres(t)
	ctx := context.Background()

	reg := prometheus.NewRegistry()
	metrics := NewMempoolReconciliationMetrics(reg)
	recon, err := NewPgMempoolReconciliation(ctx, url, metrics)
	if err != nil {
		t.Fatalf("NewPgMempoolReconciliation: %v", err)
	}
	defer recon.Close()

	pool, _ := pgxpool.New(ctx, url)
	defer pool.Close()

	predID := uuid.New()
	txHash := [32]byte{0x01, 0x02}
	zeroAddr := make([]byte, 20)
	_, err = pool.Exec(ctx, `
		INSERT INTO mempool_predictions (
			prediction_id, pending_tx_hash, router_address, protocol,
			token_in, token_out, amount_in, predicted_target_block, predicted_post_state
		) VALUES ($1, $2, $3, 'uni_v2', $3, $3, 1000, 18_000_001, '{}'::jsonb)
	`, predID, txHash[:], zeroAddr)
	if err != nil {
		t.Fatalf("seed prediction: %v", err)
	}

	got, ok, err := recon.LookupPredictionByTxHash(ctx, txHash)
	if err != nil || !ok {
		t.Fatalf("lookup: ok=%v err=%v", ok, err)
	}
	if got.PredictionID != predID {
		t.Fatalf("prediction_id mismatch")
	}

	recon.InsertReconciliation(NewReconciliation{
		PredictionID: predID,
		ResolutionTs: time.Now().UTC(),
		Outcome:      OutcomeConfirmed,
	})
	time.Sleep(300 * time.Millisecond)

	var count int64
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM mempool_reconciliation WHERE prediction_id = $1`, predID).Scan(&count); err != nil {
		t.Fatalf("count recon: %v", err)
	}
	if count != 1 {
		t.Fatalf("reconciliation count = %d, want 1", count)
	}
}

func ptrU64(v uint64) *uint64 { return &v }
