// Package stress_test provides stress/load/chaos tests for the Aether system.
//
// Run with:
//
//	go test ./tests/stress_go/ -v -tags=stress -timeout=5m
//
// Short mode (go test -short) skips all stress tests so CI can run them
// in a limited fashion while local runs exercise the full profile.
package stress_test

import (
	"context"
	crand "crypto/rand"
	"fmt"
	"log/slog"
	"math/big"
	"os"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"

	"github.com/aether-arb/aether/internal/db"
	"github.com/aether-arb/aether/internal/events"
	"github.com/aether-arb/aether/internal/risk"
)

// ---------------------------------------------------------------------------
// LoadProfile
// ---------------------------------------------------------------------------

// LoadProfile classifies the intensity of a stress run.
type LoadProfile string

const (
	LoadLow       LoadProfile = "low"
	LoadMedium    LoadProfile = "medium"
	LoadHigh      LoadProfile = "high"
	LoadBurst     LoadProfile = "burst"
	LoadSustained LoadProfile = "sustained"
)

// ---------------------------------------------------------------------------
// StressTestConfig
// ---------------------------------------------------------------------------

// StressTestConfig holds the tunable parameters for a single stress test.
type StressTestConfig struct {
	Profile           LoadProfile
	Duration          time.Duration
	Concurrency       int
	WarmupDuration    time.Duration
	CooldownDuration  time.Duration
	RatePerSecond     int
	BatchSize         int
	ChannelBufferSize int
}

// DefaultStressConfig returns a medium-profile config suitable for most tests.
// Tests may override fields inline.
func DefaultStressConfig(profile LoadProfile) StressTestConfig {
	cfg := StressTestConfig{
		Profile:           profile,
		Duration:          2 * time.Second,
		Concurrency:       10,
		WarmupDuration:    100 * time.Millisecond,
		CooldownDuration:  100 * time.Millisecond,
		RatePerSecond:     100,
		BatchSize:         50,
		ChannelBufferSize: 256,
	}
	switch profile {
	case LoadLow:
		cfg.Concurrency = 2
		cfg.RatePerSecond = 10
		cfg.Duration = 1 * time.Second
	case LoadHigh:
		cfg.Concurrency = 50
		cfg.RatePerSecond = 500
		cfg.Duration = 3 * time.Second
	case LoadBurst:
		cfg.Concurrency = 100
		cfg.RatePerSecond = 2000
		cfg.Duration = 1 * time.Second
	case LoadSustained:
		cfg.Concurrency = 25
		cfg.RatePerSecond = 200
		cfg.Duration = 2 * time.Second
	}
	if IsCI() && profile != LoadLow {
		cfg.Duration = cfg.Duration / 3
		cfg.Concurrency = max(1, cfg.Concurrency/2)
		cfg.RatePerSecond = max(1, cfg.RatePerSecond/2)
	}
	return cfg
}

// ---------------------------------------------------------------------------
// StressSuite — shared test state
// ---------------------------------------------------------------------------

// StressSuite holds shared state across stress tests. One instance is created
// per test run via TestMain and reused by every stress test that needs it.
type StressSuite struct {
	DBPool         *pgxpool.Pool
	Ledger         db.Ledger
	MetricsStore   db.MetricsStore
	RiskManager    *risk.RiskManager
	EventPublisher *events.Publisher

	metrics *StressMetrics
}

// StressMetrics records the prometheus-style counters collected during a run.
type StressMetrics struct {
	OpsTotal         prometheus.Counter
	OpsFailed        prometheus.Counter
	LatencyMs        prometheus.Histogram
	DroppedTotal     prometheus.Counter
	ActiveGoroutines prometheus.GaugeFunc
}

// NewStressMetrics registers and returns a fresh set of stress-run metrics
// against the given registerer. Passing nil uses the default registry.
func NewStressMetrics(reg prometheus.Registerer) *StressMetrics {
	if reg == nil {
		reg = prometheus.DefaultRegisterer
	}
	factory := promauto.With(reg)
	return &StressMetrics{
		OpsTotal: factory.NewCounter(prometheus.CounterOpts{
			Name: "stress_ops_total",
			Help: "Total operations attempted during stress test",
		}),
		OpsFailed: factory.NewCounter(prometheus.CounterOpts{
			Name: "stress_ops_failed_total",
			Help: "Operations that failed during stress test",
		}),
		LatencyMs: factory.NewHistogram(prometheus.HistogramOpts{
			Name:    "stress_op_latency_ms",
			Help:    "Per-operation latency during stress test",
			Buckets: []float64{0.1, 0.5, 1, 2, 5, 10, 25, 50, 100, 250, 500, 1000, 5000},
		}),
		DroppedTotal: factory.NewCounter(prometheus.CounterOpts{
			Name: "stress_dropped_total",
			Help: "Operations dropped due to channel saturation during stress test",
		}),
		ActiveGoroutines: factory.NewGaugeFunc(prometheus.GaugeOpts{
			Name: "stress_active_goroutines",
			Help: "Current number of goroutines during stress test",
		}, func() float64 { return float64(runtime.NumGoroutine()) }),
	}
}

// ---------------------------------------------------------------------------
// TestMain
// ---------------------------------------------------------------------------

var suite *StressSuite

func TestMain(m *testing.M) {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})))

	suite = &StressSuite{
		metrics: NewStressMetrics(nil),
	}

	code := m.Run()

	if suite.DBPool != nil {
		suite.DBPool.Close()
	}
	os.Exit(code)
}

// ---------------------------------------------------------------------------
// CI detection
// ---------------------------------------------------------------------------

// IsCI returns true when running in a CI environment.
func IsCI() bool {
	return os.Getenv("CI") != "" ||
		os.Getenv("GITHUB_ACTIONS") != "" ||
		os.Getenv("GITLAB_CI") != ""
}

// SkipIfShort skips t when -short is passed, so CI and quick local runs
// avoid long stress tests.
func SkipIfShort(t *testing.T) {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping stress test in short mode")
	}
}

// ---------------------------------------------------------------------------
// Helpers: generateLoad
// ---------------------------------------------------------------------------

// generateLoad spawns n goroutines that each submit f() in a loop until the
// done channel is closed. It returns a function that blocks until all workers
// finish. The concurrency pattern matches the codebase's bounded-semaphore
// writer goroutine pattern (see PgLedger.dispatch in internal/db/ledger_pg.go).
func generateLoad(
	ctx context.Context,
	concurrency int,
	ratePerSecond int,
	f func(ctx context.Context) error,
) error {
	if concurrency <= 0 {
		return fmt.Errorf("concurrency must be positive, got %d", concurrency)
	}
	if ratePerSecond <= 0 {
		return fmt.Errorf("rate_per_second must be positive, got %d", ratePerSecond)
	}

	var wg sync.WaitGroup
	sem := make(chan struct{}, concurrency)
	interval := time.Second / time.Duration(ratePerSecond)
	limiter := time.NewTicker(interval)
	defer limiter.Stop()

	for {
		select {
		case <-ctx.Done():
			wg.Wait()
			return ctx.Err()
		case <-limiter.C:
			select {
			case sem <- struct{}{}:
			default:
				suite.metrics.DroppedTotal.Inc()
				continue
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer func() { <-sem }()
				if err := f(ctx); err != nil {
					suite.metrics.OpsFailed.Inc()
				}
				suite.metrics.OpsTotal.Inc()
			}()
			runtime.Gosched()
		}
	}
}

// generateLoadUnlimited spawns f() in a tight loop without rate-limiting,
// useful for burst / saturation tests.
func generateLoadUnlimited(
	ctx context.Context,
	concurrency int,
	f func(ctx context.Context) error,
) error {
	if concurrency <= 0 {
		return fmt.Errorf("concurrency must be positive, got %d", concurrency)
	}

	var wg sync.WaitGroup
	sem := make(chan struct{}, concurrency)

	for {
		select {
		case <-ctx.Done():
			wg.Wait()
			return ctx.Err()
		case sem <- struct{}{}:
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer func() { <-sem }()
				if err := f(ctx); err != nil {
					suite.metrics.OpsFailed.Inc()
				}
				suite.metrics.OpsTotal.Inc()
			}()
			runtime.Gosched()
		}
	}
}

// ---------------------------------------------------------------------------
// Helper: measureMemoryUsage
// ---------------------------------------------------------------------------

// MemorySnapshot captures heap metrics at a point in time.
type MemorySnapshot struct {
	Alloc      uint64
	TotalAlloc uint64
	Sys        uint64
	NumGC      uint32
	Goroutines int
}

// measureMemoryUsage returns a snapshot of runtime memory stats.
func measureMemoryUsage() MemorySnapshot {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return MemorySnapshot{
		Alloc:      m.Alloc,
		TotalAlloc: m.TotalAlloc,
		Sys:        m.Sys,
		NumGC:      m.NumGC,
		Goroutines: runtime.NumGoroutine(),
	}
}

// ---------------------------------------------------------------------------
// Generic helpers
// ---------------------------------------------------------------------------

// randomHexAddr returns a 20-byte hex address for test usage.
func randomHexAddr() string {
	b := make([]byte, 20)
	_, _ = crand.Read(b)
	return fmt.Sprintf("0x%x", b)
}

// randomUUID returns a new random UUID.
func randomUUID() uuid.UUID {
	return uuid.Must(uuid.NewRandom())
}

// bigIntWei converts an ETH float64 value to a *big.Int in wei.
func bigIntWei(eth float64) *big.Int {
	f := new(big.Float).SetFloat64(eth)
	f.Mul(f, new(big.Float).SetFloat64(1e18))
	wei, _ := f.Int(nil)
	return wei
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
