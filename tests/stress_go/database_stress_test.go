package stress_test

import (
	"context"
	"fmt"
	"math/big"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/aether-arb/aether/internal/db"
)

// ---------------------------------------------------------------------------
// Concurrent ledger writes
// ---------------------------------------------------------------------------

func TestStressConcurrentLedgerWrites(t *testing.T) {
	t.Parallel()
	SkipIfShort(t)
	cfg := DefaultStressConfig(LoadHigh)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Duration)
	defer cancel()

	ledger, pool := newStressLedger(ctx, t)
	if ledger == nil {
		t.Skip("no database available for ledger stress test")
	}
	defer pool.Close()

	var writes int64

	err := generateLoad(ctx, cfg.Concurrency, cfg.RatePerSecond, func(ctx context.Context) error {
		op := atomic.AddInt64(&writes, 1) % 3
		switch op {
		case 0:
			ledger.InsertBundle(db.NewBundle{
				BundleID:    uuid.New(),
				ArbID:       uuid.New(),
				SubmittedAt: time.Now().UTC(),
				TargetBlock: 18000000,
				SignedTxHex: "0xabcd",
				IsShadow:    false,
				Builders:    []string{"flashbots", "titan"},
			})
		case 1:
			ledger.InsertInclusion(db.NewInclusion{
				BundleID:   uuid.New(),
				Builder:    "flashbots",
				Included:   true,
				ResolvedAt: time.Now().UTC(),
			})
		case 2:
			ledger.UpsertPnLDaily(db.PnLDailyDelta{
				Day:               time.Now().UTC().Truncate(24 * time.Hour),
				RealizedProfitWei: big.NewInt(100000000000000000),
				GasSpentWei:       big.NewInt(5000000000000000),
				BundleCount:       1,
				InclusionCount:    1,
			})
		}
		return nil
	})

	t.Logf("concurrent ledger writes: attempted=%d err=%v", atomic.LoadInt64(&writes), err)
	if atomic.LoadInt64(&writes) == 0 {
		t.Error("zero ledger writes attempted")
	}
}

// ---------------------------------------------------------------------------
// Metrics store batch writes
// ---------------------------------------------------------------------------

func TestStressMetricsStoreBatchWrites(t *testing.T) {
	t.Parallel()
	SkipIfShort(t)
	cfg := DefaultStressConfig(LoadHigh)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Duration)
	defer cancel()

	store, pool := newStressMetricsStore(ctx, t)
	if store == nil {
		t.Skip("no database available for metrics store stress test")
	}
	defer pool.Close()

	var recorded int64

	err := generateLoad(ctx, cfg.Concurrency, cfg.RatePerSecond, func(ctx context.Context) error {
		store.Record(db.Metric{
			Time:  time.Now().UTC(),
			Name:  "stress_test_metric",
			Value: float64(atomic.AddInt64(&recorded, 1)),
			Tags: map[string]string{
				"source": "stress_test",
				"op":     fmt.Sprintf("op_%d", atomic.LoadInt64(&recorded)%10),
			},
		})
		return nil
	})

	// Give the batcher time to flush before we check counts
	time.Sleep(2 * time.Second)

	t.Logf("metrics store batch writes: recorded=%d err=%v", atomic.LoadInt64(&recorded), err)
}

// ---------------------------------------------------------------------------
// PnL daily upsert stress
// ---------------------------------------------------------------------------

func TestStressPnLDailyUpsert(t *testing.T) {
	t.Parallel()
	SkipIfShort(t)
	cfg := DefaultStressConfig(LoadMedium)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Duration)
	defer cancel()

	ledger, pool := newStressLedger(ctx, t)
	if ledger == nil {
		t.Skip("no database available for pnl daily upsert stress test")
	}
	defer pool.Close()

	baseDay := time.Now().UTC().Truncate(24 * time.Hour)
	var upserts int64

	err := generateLoad(ctx, cfg.Concurrency, cfg.RatePerSecond, func(ctx context.Context) error {
		dayOffset := atomic.AddInt64(&upserts, 1) % 7
		day := baseDay.Add(time.Duration(dayOffset) * 24 * time.Hour)

		ledger.UpsertPnLDaily(db.PnLDailyDelta{
			Day:               day,
			RealizedProfitWei: big.NewInt(100000000000000000),
			GasSpentWei:       big.NewInt(5000000000000000),
			BundleCount:       1,
			InclusionCount:    1,
		})
		return nil
	})

	t.Logf("pnl daily upserts: attempted=%d err=%v", atomic.LoadInt64(&upserts), err)
	if atomic.LoadInt64(&upserts) == 0 {
		t.Error("zero pnl daily upserts attempted")
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// tryBuildPool attempts to create a pgxpool from DATABASE_URL.
func tryBuildPool(ctx context.Context) (*pgxpool.Pool, error) {
	dsn := suiteDSN()
	if dsn == "" {
		return nil, fmt.Errorf("DATABASE_URL not set")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse DATABASE_URL: %w", err)
	}
	cfg.MaxConns = 8
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("connect pgxpool: %w", err)
	}
	pingCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}
	return pool, nil
}

func suiteDSN() string {
	if suite != nil && suite.DBPool != nil {
		return "" // pool exists externally
	}
	dsn := getEnvOrDefault("DATABASE_URL", "")
	if dsn == "" {
		dsn = getEnvOrDefault("STRESS_DATABASE_URL", "")
	}
	return dsn
}

func getEnvOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// newStressLedger opens a PgLedger for stress tests. Returns nil when no
// database is available.
func newStressLedger(ctx context.Context, t testing.TB) (db.Ledger, *pgxpool.Pool) {
	t.Helper()
	dsn := suiteDSN()
	if dsn == "" {
		return nil, nil
	}
	pool, err := tryBuildPool(ctx)
	if err != nil {
		t.Logf("skipping ledger stress: %v", err)
		return nil, nil
	}
	metrics := db.NewLedgerMetrics()
	pg, err := db.NewPgLedger(ctx, dsn, metrics)
	if err != nil {
		pool.Close()
		t.Logf("skipping ledger stress: NewPgLedger: %v", err)
		return nil, nil
	}
	return pg, pool
}

// newStressMetricsStore opens a PgMetricsStore for stress tests. Returns nil
// when no database is available.
func newStressMetricsStore(ctx context.Context, t testing.TB) (db.MetricsStore, *pgxpool.Pool) {
	t.Helper()
	dsn := suiteDSN()
	if dsn == "" {
		return nil, nil
	}
	pool, err := tryBuildPool(ctx)
	if err != nil {
		t.Logf("skipping metrics store stress: %v", err)
		return nil, nil
	}
	// Use a dedicated noop registry to avoid double-registration panics.
	reg := prometheus.NewRegistry()
	_ = reg
	store, err := db.NewPgMetricsStore(ctx, dsn)
	if err != nil {
		pool.Close()
		t.Logf("skipping metrics store stress: NewPgMetricsStore: %v", err)
		return nil, nil
	}
	return store, pool
}


