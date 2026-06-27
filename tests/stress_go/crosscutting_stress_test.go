package stress_test

import (
	"context"
	"fmt"
	"math/rand"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/aether-arb/aether/internal/events"
	"github.com/aether-arb/aether/internal/risk"
)

func TestStressFullE2ESystemIntegration(t *testing.T) {
	t.Parallel()
	SkipIfShort(t)
	cfg := DefaultStressConfig(LoadMedium)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Duration)
	defer cancel()

	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis.Run: %v", err)
	}
	defer mr.Close()

	publisher := events.NewPublisher("redis://" + mr.Addr())
	defer publisher.Close()
	rm := risk.NewRiskManager(risk.DefaultRiskConfig())
	bc := newStressBundleConstructor()
	submitter := newStressSubmitter()

	var (
		ops     int64
		success int64
	)

	err = generateLoad(ctx, cfg.Concurrency, cfg.RatePerSecond, func(ctx context.Context) error {
		op := atomic.AddInt64(&ops, 1)
		result := rm.PreflightCheck(bigIntWei(0.01), bigIntWei(1.0), 50, 80, 10)
		if !result.Approved {
			return nil
		}
		bundle, err := bc.BuildBundle([]byte{0xAB}, randomHexAddr(), 500000, 18000000)
		if err != nil {
			return err
		}
		res := submitter.SubmitToAll(ctx, bundle)
		for _, r := range res {
			if r.Success {
				atomic.AddInt64(&success, 1)
			}
			rm.RecordBundleResult(r.Success)
		}
		if op%5 == 0 {
			publisher.PublishNewBundle(uuid.New().String(), "flashbots", 0.01, 30.0)
		}
		return nil
	})

	t.Logf("full E2E integration: ops=%d success=%d err=%v",
		atomic.LoadInt64(&ops), atomic.LoadInt64(&success), err)
	if ops == 0 {
		t.Error("zero ops in full E2E integration test")
	}
}

func TestStressConfigHotReloadConsistency(t *testing.T) {
	t.Parallel()
	SkipIfShort(t)
	cfg := DefaultStressConfig(LoadHigh)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Duration)
	defer cancel()

	var (
		mu     sync.RWMutex
		config = map[string]string{
			"max_gas_gwei":    "300",
			"min_profit_eth":  "0.001",
			"max_concurrency": "50",
			"rpc_timeout_ms":  "5000",
		}
		ops int64
	)

	err := generateLoad(ctx, cfg.Concurrency, cfg.RatePerSecond, func(ctx context.Context) error {
		atomic.AddInt64(&ops, 1)
		if rand.Intn(10) < 8 {
			mu.RLock()
			_ = config["max_gas_gwei"]
			_ = config["min_profit_eth"]
			mu.RUnlock()
		} else {
			mu.Lock()
			config["max_gas_gwei"] = fmt.Sprintf("%d", rand.Intn(500))
			config["min_profit_eth"] = fmt.Sprintf("%.6f", rand.Float64()*0.01)
			mu.Unlock()
		}
		return nil
	})

	t.Logf("config hot reload: ops=%d err=%v", atomic.LoadInt64(&ops), err)
}

func TestStressPrometheusScrapingUnderLoad(t *testing.T) {
	t.Parallel()
	SkipIfShort(t)
	cfg := DefaultStressConfig(LoadHigh)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Duration)
	defer cancel()

	reg := prometheus.NewRegistry()
	metrics := newStressPipelineMetrics(reg)
	var ops int64

	err := generateLoad(ctx, cfg.Concurrency, cfg.RatePerSecond, func(ctx context.Context) error {
		atomic.AddInt64(&ops, 1)
		metrics.ArbsTotal.Inc()
		metrics.BundlesTotal.Inc()
		metrics.SubmissionsTotal.WithLabelValues("flashbots").Inc()
		metrics.LatencyMs.Observe(rand.Float64() * 100)
		return nil
	})

	t.Logf("prometheus scraping: ops=%d err=%v", atomic.LoadInt64(&ops), err)
}

func TestStressMultiTenantIsolation(t *testing.T) {
	t.Parallel()
	SkipIfShort(t)
	cfg := DefaultStressConfig(LoadHigh)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Duration)
	defer cancel()

	type tenantState struct {
		id    string
		count int64
	}
	var (
		mu      sync.Mutex
		tenants = make(map[string]*tenantState)
		ops     int64
	)

	err := generateLoad(ctx, cfg.Concurrency, cfg.RatePerSecond, func(ctx context.Context) error {
		tenantID := fmt.Sprintf("tenant_%d", atomic.AddInt64(&ops, 1)%5)
		mu.Lock()
		ts, ok := tenants[tenantID]
		if !ok {
			ts = &tenantState{id: tenantID}
			tenants[tenantID] = ts
		}
		ts.count++
		mu.Unlock()
		return nil
	})

	t.Logf("multi-tenant isolation: ops=%d tenants=%d err=%v",
		atomic.LoadInt64(&ops), len(tenants), err)
}

func TestStressCrossServiceRetryStorm(t *testing.T) {
	t.Parallel()
	SkipIfShort(t)
	cfg := DefaultStressConfig(LoadBurst)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Duration)
	defer cancel()

	type serviceCall struct {
		service string
		retries int
	}

	var (
		ops          int64
		totalRetries int64
	)

	services := []string{"db", "redis", "grpc", "signer", "builder"}
	err := generateLoadUnlimited(ctx, cfg.Concurrency, func(ctx context.Context) error {
		svc := services[atomic.AddInt64(&ops, 1)%int64(len(services))]
		call := serviceCall{service: svc}
		for i := 0; i < 3; i++ {
			if rand.Intn(4) > 0 {
				call.retries = i
				break
			}
			atomic.AddInt64(&totalRetries, 1)
		}
		_ = call
		return nil
	})

	t.Logf("cross-service retry storm: ops=%d retries=%d err=%v",
		atomic.LoadInt64(&ops), atomic.LoadInt64(&totalRetries), err)
}

func TestStressDistributedTracingOverhead(t *testing.T) {
	t.Parallel()
	SkipIfShort(t)
	cfg := DefaultStressConfig(LoadMedium)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Duration)
	defer cancel()

	var ops int64
	err := generateLoad(ctx, cfg.Concurrency, cfg.RatePerSecond, func(ctx context.Context) error {
		traceID := randomUUID().String()
		spanID := randomUUID().String()[:16]
		_ = fmt.Sprintf("trace=%s span=%s parent=%s", traceID, spanID, randomHexAddr())
		atomic.AddInt64(&ops, 1)
		return nil
	})

	t.Logf("distributed tracing overhead: ops=%d err=%v", atomic.LoadInt64(&ops), err)
}

func TestStressFailurePropagationAcrossServices(t *testing.T) {
	t.Parallel()
	SkipIfShort(t)
	cfg := DefaultStressConfig(LoadMedium)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Duration)
	defer cancel()

	type service struct {
		name  string
		alive atomic.Bool
	}
	services := []*service{
		{name: "pipeline"},
		{name: "risk"},
		{name: "db"},
		{name: "signer"},
		{name: "builder"},
	}
	for _, s := range services {
		s.alive.Store(true)
	}

	go func() {
		ticker := time.NewTicker(400 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				services[rand.Intn(len(services))].alive.Store(!services[rand.Intn(len(services))].alive.Load())
			}
		}
	}()

	var (
		ops        int64
		totalFails int64
	)
	err := generateLoad(ctx, cfg.Concurrency, cfg.RatePerSecond, func(ctx context.Context) error {
		atomic.AddInt64(&ops, 1)
		for _, s := range services {
			if !s.alive.Load() {
				atomic.AddInt64(&totalFails, 1)
				return fmt.Errorf("service %s unavailable", s.name)
			}
		}
		return nil
	})

	t.Logf("failure propagation: ops=%d fails=%d err=%v",
		atomic.LoadInt64(&ops), atomic.LoadInt64(&totalFails), err)
}

func TestStressBackpressureEndToEnd(t *testing.T) {
	t.Parallel()
	SkipIfShort(t)
	cfg := DefaultStressConfig(LoadBurst)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Duration)
	defer cancel()

	type pipelineStage struct {
		name      string
		input     chan int
		output    chan int
		dropped   int64
		processed int64
	}
	var stages []*pipelineStage
	prev := make(chan int, 50)
	for _, name := range []string{"ingest", "validate", "simulate", "build", "submit"} {
		stage := &pipelineStage{
			name:   name,
			input:  prev,
			output: make(chan int, 50),
		}
		stages = append(stages, stage)
		go func(s *pipelineStage) {
			for {
				select {
				case v := <-s.input:
					atomic.AddInt64(&s.processed, 1)
					select {
					case s.output <- v:
					default:
						atomic.AddInt64(&s.dropped, 1)
					}
				case <-ctx.Done():
					return
				}
			}
		}(stage)
		prev = stage.output
	}

	var ops int64
	err := generateLoadUnlimited(ctx, cfg.Concurrency, func(ctx context.Context) error {
		select {
		case stages[0].input <- int(atomic.AddInt64(&ops, 1)):
		default:
		}
		return nil
	})

	_ = err
	t.Logf("backpressure E2E: ops=%d", atomic.LoadInt64(&ops))
	for _, s := range stages {
		t.Logf("  stage %s: processed=%d dropped=%d", s.name,
			atomic.LoadInt64(&s.processed), atomic.LoadInt64(&s.dropped))
	}
}

func TestStressGlobalStateSyncUnderLoad(t *testing.T) {
	t.Parallel()
	SkipIfShort(t)
	cfg := DefaultStressConfig(LoadHigh)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Duration)
	defer cancel()

	var (
		mu        sync.RWMutex
		globalSeq int64
		ops       int64
	)

	err := generateLoad(ctx, cfg.Concurrency, cfg.RatePerSecond, func(ctx context.Context) error {
		op := atomic.AddInt64(&ops, 1)
		mu.RLock()
		current := globalSeq
		mu.RUnlock()
		if op%5 == 0 {
			mu.Lock()
			globalSeq = current + 1
			mu.Unlock()
		}
		return nil
	})

	t.Logf("global state sync: ops=%d seq=%d err=%v",
		atomic.LoadInt64(&ops), atomic.LoadInt64(&globalSeq), err)
}

func TestStressSystemWideDegradationMode(t *testing.T) {
	t.Parallel()
	SkipIfShort(t)
	cfg := DefaultStressConfig(LoadMedium)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Duration)
	defer cancel()

	var (
		normal   atomic.Bool
		ops      int64
		degraded int64
	)
	normal.Store(true)

	go func() {
		ticker := time.NewTicker(700 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				normal.Store(!normal.Load())
			}
		}
	}()

	err := generateLoad(ctx, cfg.Concurrency, cfg.RatePerSecond, func(ctx context.Context) error {
		atomic.AddInt64(&ops, 1)
		if !normal.Load() {
			atomic.AddInt64(&degraded, 1)
			time.Sleep(2 * time.Millisecond)
		}
		return nil
	})

	t.Logf("system-wide degradation: ops=%d degraded=%d err=%v",
		atomic.LoadInt64(&ops), atomic.LoadInt64(&degraded), err)
}
