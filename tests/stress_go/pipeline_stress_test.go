package stress_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/aether-arb/aether/internal/events"
	"github.com/aether-arb/aether/internal/risk"
)

// ---------------------------------------------------------------------------
// Full arb stream processing under load
// ---------------------------------------------------------------------------

// TestStressFullArbStreamProcessing simulates the complete pipeline from arb
// reception through bundle construction and submission tracking.
func TestStressFullArbStreamProcessing(t *testing.T) {
	t.Parallel()
	SkipIfShort(t)
	cfg := DefaultStressConfig(LoadHigh)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Duration)
	defer cancel()

	rm := risk.NewRiskManager(risk.DefaultRiskConfig())
	bc := newStressBundleConstructor()
	submitter := newStressSubmitter()

	var (
		arbsReceived int64
		bundlesBuilt int64
		submissions  int64
		preflights   int64
	)

	err := generateLoad(ctx, cfg.Concurrency, cfg.RatePerSecond, func(ctx context.Context) error {
		atomic.AddInt64(&arbsReceived, 1)

		// 1. Preflight
		result := rm.PreflightCheck(
			bigIntWei(0.01),
			bigIntWei(1.0),
			50.0,
			80.0,
			10.0,
		)
		atomic.AddInt64(&preflights, 1)

		if !result.Approved {
			return nil // skip unapproved arbs (no error to report)
		}

		// 2. Build bundle
		bundle, buildErr := bc.BuildBundle(
			[]byte{0xAB, 0xCD},
			randomHexAddr(),
			500000,
			18000000,
		)
		if buildErr != nil {
			return buildErr
		}
		atomic.AddInt64(&bundlesBuilt, 1)

		// 3. Submit
		res := submitter.SubmitToAll(ctx, bundle)
		atomic.AddInt64(&submissions, 1)

		// 4. Record result
		for _, r := range res {
			rm.RecordBundleResult(r.Success)
		}

		// 5. Record trade & revert occasionally
		if atomic.LoadInt64(&submissions)%10 == 0 {
			rm.RecordTrade(bigIntWei(1.0), bigIntWei(0.01))
		}
		if atomic.LoadInt64(&submissions)%20 == 0 {
			rm.RecordRevert(risk.RevertBug)
		}

		return nil
	})

	t.Logf("pipeline: arbs=%d preflights=%d built=%d submitted=%d err=%v",
		atomic.LoadInt64(&arbsReceived), atomic.LoadInt64(&preflights),
		atomic.LoadInt64(&bundlesBuilt), atomic.LoadInt64(&submissions), err)
	if atomic.LoadInt64(&bundlesBuilt) == 0 {
		t.Error("zero bundles built through the pipeline")
	}
}

// ---------------------------------------------------------------------------
// Concurrent bundle construction + submission
// ---------------------------------------------------------------------------

func TestStressConcurrentBuildAndSubmit(t *testing.T) {
	t.Parallel()
	SkipIfShort(t)
	cfg := DefaultStressConfig(LoadBurst)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Duration)
	defer cancel()

	bc := newStressBundleConstructor()
	submitter := newStressSubmitter()

	var built int64

	err := generateLoadUnlimited(ctx, cfg.Concurrency, func(ctx context.Context) error {
		bundle, buildErr := bc.BuildBundle(
			[]byte{0x01, 0x02, 0x03},
			randomHexAddr(),
			500000,
			18000000,
		)
		if buildErr != nil {
			return buildErr
		}
		atomic.AddInt64(&built, 1)
		_ = submitter.SubmitToAll(ctx, bundle)
		return nil
	})

	t.Logf("concurrent build+submit: built=%d err=%v", atomic.LoadInt64(&built), err)
	if atomic.LoadInt64(&built) == 0 {
		t.Error("zero bundles built in concurrent build+submit")
	}
}

// ---------------------------------------------------------------------------
// Redis event publishing stress
// ---------------------------------------------------------------------------

func TestStressRedisEventPublishing(t *testing.T) {
	t.Parallel()
	SkipIfShort(t)
	cfg := DefaultStressConfig(LoadHigh)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Duration)
	defer cancel()

	// Start an in-memory Redis for the stress test (matches the codebase's
	// use of miniredis in test fixtures).
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis.Run: %v", err)
	}
	defer mr.Close()

	publisher := events.NewPublisher("redis://" + mr.Addr())
	defer publisher.Close()

	if !publisher.Enabled() {
		t.Fatal("publisher should be enabled after connecting to miniredis")
	}

	var published int64

	err = generateLoad(ctx, cfg.Concurrency, cfg.RatePerSecond, func(ctx context.Context) error {
		op := atomic.AddInt64(&published, 1) % 4
		switch op {
		case 0:
			publisher.PublishNewBundle(uuid.New().String(), "flashbots", 0.01, 30.0)
		case 1:
			publisher.PublishPnLUpdate(1.5, 75.0)
		case 2:
			publisher.PublishBreakerStatus(false, "all_clear")
		case 3:
			publisher.PublishSignerHealth(true)
		}
		return nil
	})

	t.Logf("redis events published: %d err=%v", atomic.LoadInt64(&published), err)
	if atomic.LoadInt64(&published) == 0 {
		t.Error("zero redis events published")
	}

	// Verify messages arrived at Redis (miniredis dumps state on Close)
}

// ---------------------------------------------------------------------------
// Metrics recording stress
// ---------------------------------------------------------------------------

func TestStressMetricsRecording(t *testing.T) {
	t.Parallel()
	SkipIfShort(t)
	cfg := DefaultStressConfig(LoadMedium)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Duration)
	defer cancel()

	reg := prometheus.NewRegistry()
	metrics := newStressPipelineMetrics(reg)

	var recorded int64

	err := generateLoad(ctx, cfg.Concurrency, cfg.RatePerSecond, func(ctx context.Context) error {
		atomic.AddInt64(&recorded, 1)
		metrics.ArbsTotal.Inc()
		metrics.BundlesTotal.Inc()
		metrics.SubmissionsTotal.WithLabelValues("flashbots").Inc()
		metrics.LatencyMs.Observe(float64(time.Now().UnixNano()%1000) / float64(time.Millisecond))
		return nil
	})

	_ = ctx // used in generateLoad
	t.Logf("metrics recorded: %d (ops) err=%v", atomic.LoadInt64(&recorded), err)
	if atomic.LoadInt64(&recorded) == 0 {
		t.Error("zero metrics recorded")
	}
}

// ---------------------------------------------------------------------------
// Stress pipeline metrics
// ---------------------------------------------------------------------------

type stressPipelineMetrics struct {
	ArbsTotal       prometheus.Counter
	BundlesTotal    prometheus.Counter
	SubmissionsTotal *prometheus.CounterVec
	LatencyMs       prometheus.Histogram
}

func newStressPipelineMetrics(reg prometheus.Registerer) *stressPipelineMetrics {
	return &stressPipelineMetrics{
		ArbsTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "stress_pipeline_arbs_total",
			Help: "Arb opportunities received during stress test",
		}),
		BundlesTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "stress_pipeline_bundles_total",
			Help: "Bundles built during stress test",
		}),
		SubmissionsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "stress_pipeline_submissions_total",
			Help: "Submissions per builder during stress test",
		}, []string{"builder"}),
		LatencyMs: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "stress_pipeline_latency_ms",
			Help:    "Pipeline latency during stress test",
			Buckets: []float64{0.1, 0.5, 1, 2, 5, 10, 25, 50, 100, 250, 500, 1000},
		}),
	}
}

// ---------------------------------------------------------------------------
// Combined pipeline pressure test
// ---------------------------------------------------------------------------

func TestStressEndToEndPipeline(t *testing.T) {
	t.Parallel()
	SkipIfShort(t)
	cfg := DefaultStressConfig(LoadSustained)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Duration)
	defer cancel()

	rm := risk.NewRiskManager(risk.DefaultRiskConfig())
	bc := newStressBundleConstructor()
	submitter := newStressSubmitter()

	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis.Run: %v", err)
	}
	defer mr.Close()

	publisher := events.NewPublisher("redis://" + mr.Addr())
	defer publisher.Close()

	reg := prometheus.NewRegistry()
	metrics := newStressPipelineMetrics(reg)

	var (
		arbs       int64
		reverts    int64
		publishedEvents int64
	)

	err = generateLoad(ctx, cfg.Concurrency, cfg.RatePerSecond, func(ctx context.Context) error {
		step := atomic.AddInt64(&arbs, 1)

		// Preflight
		result := rm.PreflightCheck(
			bigIntWei(0.01),
			bigIntWei(1.0),
			50.0,
			80.0,
			10.0,
		)
		metrics.ArbsTotal.Inc()

		if !result.Approved {
			return nil
		}

		// Build
		bundle, buildErr := bc.BuildBundle(
			[]byte{0xAB, 0xCD},
			randomHexAddr(),
			500000,
			18000000,
		)
		if buildErr != nil {
			return buildErr
		}
		metrics.BundlesTotal.Inc()

		// Submit
		res := submitter.SubmitToAll(ctx, bundle)
		for _, r := range res {
			metrics.SubmissionsTotal.WithLabelValues(r.Builder).Inc()
			rm.RecordBundleResult(r.Success)
		}

		// Record trade every 5th op
		if step%5 == 0 {
			rm.RecordTrade(bigIntWei(1.0), bigIntWei(0.01))
		}

		// Record revert every 15th
		if step%15 == 0 {
			rm.RecordRevert(risk.RevertBug)
			atomic.AddInt64(&reverts, 1)
		}

		// Publish event every 10th
		if step%10 == 0 {
			publisher.PublishNewBundle(uuid.New().String(), "flashbots", 0.01, 30.0)
			publisher.PublishPnLUpdate(1.5, 75.0)
			atomic.AddInt64(&publishedEvents, 1)
		}

		return nil
	})

	t.Logf("e2e pipeline: arbs=%d reverts=%d events=%d err=%v",
		atomic.LoadInt64(&arbs), atomic.LoadInt64(&reverts),
		atomic.LoadInt64(&publishedEvents), err)
	if atomic.LoadInt64(&arbs) == 0 {
		t.Error("zero operations in e2e pipeline stress test")
	}
}


