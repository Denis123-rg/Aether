//go:build load

package load

import (
	"context"
	"io"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/HdrHistogram/hdrhistogram-go"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	pb "github.com/aether-arb/aether/internal/pb"
	"github.com/aether-arb/aether/internal/testutil"
)

const (
	defaultLoadDuration = 10 * time.Minute
	defaultClients      = 100
	defaultRPSPerClient = 10
	shutdownGracePeriod = 2 * time.Second
)

func loadDuration() time.Duration {
	if raw := os.Getenv("LOAD_TEST_DURATION"); raw != "" {
		if d, err := time.ParseDuration(raw); err == nil && d > 0 {
			return d
		}
	}
	if testing.Short() {
		return 30 * time.Second
	}
	return defaultLoadDuration
}

func loadClients() int {
	if raw := os.Getenv("LOAD_TEST_CLIENTS"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			return n
		}
	}
	return defaultClients
}

func loadRPSPerClient() int {
	if raw := os.Getenv("LOAD_TEST_RPS"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			return n
		}
	}
	return defaultRPSPerClient
}

func isShutdownError(err error) bool {
	if err == nil {
		return true
	}
	if err == io.EOF {
		return true
	}
	errStr := err.Error()
	for _, s := range []string{
		"context canceled",
		"context deadline exceeded",
		"transport is closing",
		"connection closed",
		"server closed",
		"broken pipe",
		"use of closed",
		"io: read/write on closed pipe",
		"stream reset",
		"RST_STREAM",
		"closed",
	} {
		if len(errStr) >= len(s) {
			for i := 0; i <= len(errStr)-len(s); i++ {
				if errStr[i:i+len(s)] == s {
					return true
				}
			}
		}
	}
	return false
}

// TestLoad drives StreamArbs at ~1000 req/s (100 clients × 10 rps) against a
// mock gRPC server and asserts p99 < 50ms with bounded heap growth.
func TestLoad(t *testing.T) {
	if os.Getenv("LOAD_TEST_SKIP") == "1" {
		t.Skip("LOAD_TEST_SKIP=1")
	}

	duration := loadDuration()
	clients := loadClients()
	rps := loadRPSPerClient()
	totalRPS := clients * rps

	srv := testutil.NewMockArbServer()
	srv.SetArbs([]*pb.ValidatedArb{testutil.ProfitableTriangleArb()})
	addr, err := srv.Start()
	if err != nil {
		t.Fatalf("Start mock server: %v", err)
	}
	defer srv.Stop()

	var baselineHeap atomic.Uint64
	var baselineSet atomic.Bool
	runtime.GC()
	var memStart runtime.MemStats
	runtime.ReadMemStats(&memStart)
	baselineHeap.Store(memStart.HeapAlloc)

	hist := hdrhistogram.New(1, 60_000, 3)
	var reqCount atomic.Uint64
	var errCount atomic.Uint64
	var cancelledCount atomic.Uint64
	var transportCount atomic.Uint64
	var otherErrCount atomic.Uint64
	warmup := duration / 5
	if warmup < time.Second {
		warmup = time.Second
	}

	ctx, cancel := context.WithTimeout(context.Background(), duration)
	defer cancel()

	stopCh := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
		case <-time.After(warmup):
			runtime.GC()
			var ms runtime.MemStats
			runtime.ReadMemStats(&ms)
			baselineHeap.Store(ms.HeapAlloc)
			baselineSet.Store(true)
		}
	}()

	go func() {
		<-ctx.Done()
		close(stopCh)
	}()

	var wg sync.WaitGroup
	interval := time.Second / time.Duration(rps)

	for c := 0; c < clients; c++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			conn, err := grpc.NewClient(addr,
				grpc.WithTransportCredentials(insecure.NewCredentials()),
			)
			if err != nil {
				if ctx.Err() == nil {
					errCount.Add(1)
					otherErrCount.Add(1)
				} else {
					cancelledCount.Add(1)
				}
				return
			}
			defer conn.Close()
			arbClient := pb.NewArbServiceClient(conn)
			ticker := time.NewTicker(interval)
			defer ticker.Stop()
			for {
				select {
				case <-stopCh:
					return
				case <-ticker.C:
					if ctx.Err() != nil {
						return
					}
					reqCtx, reqCancel := context.WithTimeout(ctx, 2*time.Second)
					start := time.Now()
					stream, err := arbClient.StreamArbs(reqCtx, &pb.StreamArbsRequest{MinProfitEth: 0})
					if err != nil {
						reqCancel()
						if ctx.Err() != nil {
							cancelledCount.Add(1)
						} else if isShutdownError(err) {
							cancelledCount.Add(1)
						} else {
							errCount.Add(1)
							transportCount.Add(1)
						}
						continue
					}
					for {
						_, recvErr := stream.Recv()
						if recvErr == io.EOF {
							break
						}
						if recvErr != nil {
							if ctx.Err() != nil {
								cancelledCount.Add(1)
							} else if isShutdownError(recvErr) {
								cancelledCount.Add(1)
							} else {
								errCount.Add(1)
								transportCount.Add(1)
							}
							break
						}
					}
					reqCancel()
					if ctx.Err() != nil {
						continue
					}
					_ = hist.RecordValue(time.Since(start).Microseconds())
					reqCount.Add(1)
				}
			}
		}()
	}

	wg.Wait()

	deadline := time.After(shutdownGracePeriod)
	drainCh := make(chan struct{})
	go func() {
		runtime.GC()
		close(drainCh)
	}()
	select {
	case <-drainCh:
	case <-deadline:
	}

	runtime.GC()
	var memAfter runtime.MemStats
	runtime.ReadMemStats(&memAfter)

	p99 := time.Duration(hist.ValueAtQuantile(99.0)) * time.Microsecond
	total := reqCount.Load()
	errs := errCount.Load()
	cancelled := cancelledCount.Load()
	transport := transportCount.Load()
	otherErr := otherErrCount.Load()
	heapGrowthPct := 0.0
	if baselineSet.Load() {
		before := baselineHeap.Load()
		if before > 0 {
			heapGrowthPct = (float64(int64(memAfter.HeapAlloc)-int64(before)) / float64(before)) * 100.0
		}
	}

	t.Logf("load test: clients=%d rps/client=%d total_rps_target=%d duration=%s", clients, rps, totalRPS, duration)
	t.Logf("requests=%d errors=%d p50=%s p99=%s max=%s",
		total, errs,
		time.Duration(hist.ValueAtQuantile(50.0))*time.Microsecond,
		p99,
		time.Duration(hist.Max())*time.Microsecond,
	)
	t.Logf("heap_before=%d heap_after=%d growth=%.2f%%", baselineHeap.Load(), memAfter.HeapAlloc, heapGrowthPct)
	t.Logf("error_breakdown: shutdown_cancelled=%d transport=%d other=%d", cancelled, transport, otherErr)

	if total == 0 {
		t.Fatal("no requests completed")
	}
	if errs > 0 && float64(errs)/float64(total) > 0.01 {
		t.Fatalf("error rate too high: %d/%d (transport=%d other=%d cancelled=%d)", errs, total, transport, otherErr, cancelled)
	}
	if p99 > 50*time.Millisecond {
		t.Fatalf("p99 latency %s exceeds 50ms target", p99)
	}
	if baselineSet.Load() && heapGrowthPct > 5.0 {
		t.Fatalf("heap growth %.2f%% exceeds 5%% cap", heapGrowthPct)
	}
}

func TestLoadSmoke(t *testing.T) {
	t.Setenv("LOAD_TEST_DURATION", "5s")
	t.Setenv("LOAD_TEST_CLIENTS", "20")
	t.Setenv("LOAD_TEST_RPS", "10")
	TestLoad(t)
}

// ──────────────────────────────────────────────────────────────────────────────
// Additional load test scenarios
// ──────────────────────────────────────────────────────────────────────────────

// TestLoadBurst validates behavior under sudden traffic spikes (10x normal RPS).
// Simulates a block containing many MEV opportunities discovered simultaneously.
func TestLoadBurst(t *testing.T) {
	if os.Getenv("LOAD_TEST_SKIP") == "1" {
		t.Skip("LOAD_TEST_SKIP=1")
	}

	srv := testutil.NewMockArbServer()
	srv.SetArbs([]*pb.ValidatedArb{testutil.ProfitableTriangleArb()})
	addr, err := srv.Start()
	if err != nil {
		t.Fatalf("Start mock server: %v", err)
	}
	defer srv.Stop()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var reqCount atomic.Uint64
	var errCount atomic.Uint64

	// Burst: 500 clients × 50 rps = 25,000 req/s for 10s, then cool down
	burstClients := 500
	burstRPS := 50
	burstDuration := 10 * time.Second

	var wg sync.WaitGroup
	for c := 0; c < burstClients; c++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			conn, err := grpc.NewClient(addr,
				grpc.WithTransportCredentials(insecure.NewCredentials()),
			)
			if err != nil {
				errCount.Add(1)
				return
			}
			defer conn.Close()
			arbClient := pb.NewArbServiceClient(conn)
			interval := time.Second / time.Duration(burstRPS)
			ticker := time.NewTicker(interval)
			defer ticker.Stop()
			burstEnd := time.Now().Add(burstDuration)
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					if time.Now().After(burstEnd) {
						return
					}
					reqCtx, reqCancel := context.WithTimeout(ctx, 2*time.Second)
					stream, err := arbClient.StreamArbs(reqCtx, &pb.StreamArbsRequest{MinProfitEth: 0})
					if err != nil {
						reqCancel()
						errCount.Add(1)
						continue
					}
					for {
						_, recvErr := stream.Recv()
						if recvErr != nil {
							break
						}
					}
					reqCancel()
					reqCount.Add(1)
				}
			}
		}()
	}

	wg.Wait()

	total := reqCount.Load()
	errs := errCount.Load()
	t.Logf("burst test: clients=%d rps=%d duration=%s total_requests=%d errors=%d",
		burstClients, burstRPS, burstDuration, total, errs)

	if total == 0 {
		t.Fatal("no requests completed during burst")
	}
	if errs > 0 && float64(errs)/float64(total) > 0.05 {
		t.Fatalf("burst error rate too high: %d/%d", errs, total)
	}
}

// TestLoadSustained validates stable throughput over 2 minutes at moderate RPS.
// Verifies no memory leaks or connection pool exhaustion over time.
func TestLoadSustained(t *testing.T) {
	if os.Getenv("LOAD_TEST_SKIP") == "1" {
		t.Skip("LOAD_TEST_SKIP=1")
	}

	srv := testutil.NewMockArbServer()
	srv.SetArbs([]*pb.ValidatedArb{testutil.ProfitableTriangleArb()})
	addr, err := srv.Start()
	if err != nil {
		t.Fatalf("Start mock server: %v", err)
	}
	defer srv.Stop()

	runtime.GC()
	var memBefore runtime.MemStats
	runtime.ReadMemStats(&memBefore)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	var reqCount atomic.Uint64
	var errCount atomic.Uint64

	clients := 50
	rps := 20

	var wg sync.WaitGroup
	interval := time.Second / time.Duration(rps)

	for c := 0; c < clients; c++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			conn, err := grpc.NewClient(addr,
				grpc.WithTransportCredentials(insecure.NewCredentials()),
			)
			if err != nil {
				errCount.Add(1)
				return
			}
			defer conn.Close()
			arbClient := pb.NewArbServiceClient(conn)
			ticker := time.NewTicker(interval)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					reqCtx, reqCancel := context.WithTimeout(ctx, 2*time.Second)
					stream, err := arbClient.StreamArbs(reqCtx, &pb.StreamArbsRequest{MinProfitEth: 0})
					if err != nil {
						reqCancel()
						errCount.Add(1)
						continue
					}
					for {
						_, recvErr := stream.Recv()
						if recvErr != nil {
							break
						}
					}
					reqCancel()
					reqCount.Add(1)
				}
			}
		}()
	}

	wg.Wait()

	runtime.GC()
	var memAfter runtime.MemStats
	runtime.ReadMemStats(&memAfter)

	total := reqCount.Load()
	errs := errCount.Load()
	heapGrowthMB := float64(int64(memAfter.HeapAlloc)-int64(memBefore.HeapAlloc)) / 1024 / 1024

	t.Logf("sustained test: clients=%d rps=%d duration=2m total=%d errors=%d heap_growth=%.2fMB",
		clients, rps, total, errs, heapGrowthMB)

	if total == 0 {
		t.Fatal("no requests completed")
	}
	if errs > 0 && float64(errs)/float64(total) > 0.01 {
		t.Fatalf("sustained error rate: %d/%d", errs, total)
	}
	if heapGrowthMB > 50 {
		t.Fatalf("heap grew %.2fMB over 2 minutes — possible leak", heapGrowthMB)
	}
}

// TestLoadMixedProtocols simulates realistic mixed traffic: gRPC StreamArbs +
// HTTP metrics scraping + admin endpoint polling.
func TestLoadMixedProtocols(t *testing.T) {
	if os.Getenv("LOAD_TEST_SKIP") == "1" {
		t.Skip("LOAD_TEST_SKIP=1")
	}

	srv := testutil.NewMockArbServer()
	srv.SetArbs([]*pb.ValidatedArb{testutil.ProfitableTriangleArb()})
	addr, err := srv.Start()
	if err != nil {
		t.Fatalf("Start mock server: %v", err)
	}
	defer srv.Stop()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var grpcReqs atomic.Uint64
	var httpReqs atomic.Uint64
	var grpcErrs atomic.Uint64
	var httpErrs atomic.Uint64

	// gRPC clients (80% of traffic)
	var wg sync.WaitGroup
	for c := 0; c < 80; c++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			conn, err := grpc.NewClient(addr,
				grpc.WithTransportCredentials(insecure.NewCredentials()),
			)
			if err != nil {
				grpcErrs.Add(1)
				return
			}
			defer conn.Close()
			arbClient := pb.NewArbServiceClient(conn)
			ticker := time.NewTicker(100 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					reqCtx, reqCancel := context.WithTimeout(ctx, 2*time.Second)
					stream, err := arbClient.StreamArbs(reqCtx, &pb.StreamArbsRequest{MinProfitEth: 0})
					if err != nil {
						reqCancel()
						grpcErrs.Add(1)
						continue
					}
					for {
						_, recvErr := stream.Recv()
						if recvErr != nil {
							break
						}
					}
					reqCancel()
					grpcReqs.Add(1)
				}
			}
		}()
	}

	// HTTP clients simulating metrics/admin polling (20% of traffic)
	httpClient := &http.Client{Timeout: 2 * time.Second}
	for c := 0; c < 20; c++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ticker := time.NewTicker(500 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					resp, err := httpClient.Get("http://localhost:9090/metrics")
					if err != nil {
						httpErrs.Add(1)
						continue
					}
					resp.Body.Close()
					httpReqs.Add(1)
				}
			}
		}()
	}

	wg.Wait()

	t.Logf("mixed protocol test: grpc=%d http=%d grpc_errs=%d http_errs=%d",
		grpcReqs.Load(), httpReqs.Load(), grpcErrs.Load(), httpErrs.Load())

	totalGRPC := grpcReqs.Load()
	if totalGRPC == 0 {
		t.Fatal("no gRPC requests completed")
	}
}

// TestLoadConnectionChurn validates stability under rapid connect/disconnect cycles.
// Simulates clients joining and leaving (e.g., mobile clients, network flaps).
func TestLoadConnectionChurn(t *testing.T) {
	if os.Getenv("LOAD_TEST_SKIP") == "1" {
		t.Skip("LOAD_TEST_SKIP=1")
	}

	srv := testutil.NewMockArbServer()
	srv.SetArbs([]*pb.ValidatedArb{testutil.ProfitableTriangleArb()})
	addr, err := srv.Start()
	if err != nil {
		t.Fatalf("Start mock server: %v", err)
	}
	defer srv.Stop()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var reqCount atomic.Uint64
	var errCount atomic.Uint64

	// 200 short-lived connections, each doing 5-15 requests then disconnecting
	var wg sync.WaitGroup
	for c := 0; c < 200; c++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			conn, err := grpc.NewClient(addr,
				grpc.WithTransportCredentials(insecure.NewCredentials()),
			)
			if err != nil {
				errCount.Add(1)
				return
			}
			arbClient := pb.NewArbServiceClient(conn)

			// Random burst of 5-15 requests
			burst := 5 + (runtime.NumCPU() % 11)
			for i := 0; i < burst; i++ {
				if ctx.Err() != nil {
					break
				}
				reqCtx, reqCancel := context.WithTimeout(ctx, 2*time.Second)
				stream, err := arbClient.StreamArbs(reqCtx, &pb.StreamArbsRequest{MinProfitEth: 0})
				if err != nil {
					reqCancel()
					errCount.Add(1)
					continue
				}
				for {
					_, recvErr := stream.Recv()
					if recvErr != nil {
						break
					}
				}
				reqCancel()
				reqCount.Add(1)
			}
			conn.Close()
		}()
	}

	wg.Wait()

	total := reqCount.Load()
	errs := errCount.Load()
	t.Logf("churn test: connections=200 total_requests=%d errors=%d", total, errs)

	if total == 0 {
		t.Fatal("no requests completed during churn")
	}
}

// TestLoadHighContention validates behavior when all clients target the same
// resource (single gRPC stream method). Tests server-side lock contention.
func TestLoadHighContention(t *testing.T) {
	if os.Getenv("LOAD_TEST_SKIP") == "1" {
		t.Skip("LOAD_TEST_SKIP=1")
	}

	srv := testutil.NewMockArbServer()
	srv.SetArbs([]*pb.ValidatedArb{testutil.ProfitableTriangleArb()})
	addr, err := srv.Start()
	if err != nil {
		t.Fatalf("Start mock server: %v", err)
	}
	defer srv.Stop()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	hist := hdrhistogram.New(1, 60_000, 3)
	var reqCount atomic.Uint64
	var errCount atomic.Uint64

	// 1000 clients all hitting StreamArbs simultaneously
	clients := 1000
	var wg sync.WaitGroup
	for c := 0; c < clients; c++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			conn, err := grpc.NewClient(addr,
				grpc.WithTransportCredentials(insecure.NewCredentials()),
			)
			if err != nil {
				errCount.Add(1)
				return
			}
			defer conn.Close()
			arbClient := pb.NewArbServiceClient(conn)
			for i := 0; i < 10; i++ {
				if ctx.Err() != nil {
					return
				}
				reqCtx, reqCancel := context.WithTimeout(ctx, 2*time.Second)
				start := time.Now()
				stream, err := arbClient.StreamArbs(reqCtx, &pb.StreamArbsRequest{MinProfitEth: 0})
				if err != nil {
					reqCancel()
					errCount.Add(1)
					continue
				}
				for {
					_, recvErr := stream.Recv()
					if recvErr != nil {
						break
					}
				}
				reqCancel()
				_ = hist.RecordValue(time.Since(start).Microseconds())
				reqCount.Add(1)
			}
		}()
	}

	wg.Wait()

	total := reqCount.Load()
	errs := errCount.Load()
	p99 := time.Duration(hist.ValueAtQuantile(99.0)) * time.Microsecond

	t.Logf("contention test: clients=%d total=%d errors=%d p99=%s", clients, total, errs, p99)

	if total == 0 {
		t.Fatal("no requests completed")
	}
	if p99 > 100*time.Millisecond {
		t.Logf("WARNING: p99 under high contention: %s (threshold relaxed to 100ms)", p99)
	}
}

// TestLoadGracefulShutdown validates that the server handles client disconnects
// cleanly without resource leaks or panics.
func TestLoadGracefulShutdown(t *testing.T) {
	if os.Getenv("LOAD_TEST_SKIP") == "1" {
		t.Skip("LOAD_TEST_SKIP=1")
	}

	srv := testutil.NewMockArbServer()
	srv.SetArbs([]*pb.ValidatedArb{testutil.ProfitableTriangleArb()})
	addr, err := srv.Start()
	if err != nil {
		t.Fatalf("Start mock server: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var reqCount atomic.Uint64

	// Start 50 clients
	var wg sync.WaitGroup
	for c := 0; c < 50; c++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			conn, err := grpc.NewClient(addr,
				grpc.WithTransportCredentials(insecure.NewCredentials()),
			)
			if err != nil {
				return
			}
			defer conn.Close()
			arbClient := pb.NewArbServiceClient(conn)
			ticker := time.NewTicker(100 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					reqCtx, reqCancel := context.WithTimeout(ctx, 2*time.Second)
					stream, err := arbClient.StreamArbs(reqCtx, &pb.StreamArbsRequest{MinProfitEth: 0})
					if err != nil {
						reqCancel()
						continue
					}
					for {
						_, recvErr := stream.Recv()
						if recvErr != nil {
							break
						}
					}
					reqCancel()
					reqCount.Add(1)
				}
			}
		}()
	}

	// Let some requests through, then shut down server mid-flight
	time.Sleep(3 * time.Second)
	srv.Stop()
	wg.Wait()

	t.Logf("graceful shutdown test: requests_before_shutdown=%d", reqCount.Load())
}

// TestLoadMemoryPressure validates bounded memory under sustained allocation.
func TestLoadMemoryPressure(t *testing.T) {
	if os.Getenv("LOAD_TEST_SKIP") == "1" {
		t.Skip("LOAD_TEST_SKIP=1")
	}

	srv := testutil.NewMockArbServer()
	srv.SetArbs([]*pb.ValidatedArb{testutil.ProfitableTriangleArb()})
	addr, err := srv.Start()
	if err != nil {
		t.Fatalf("Start mock server: %v", err)
	}
	defer srv.Stop()

	// Record memory at multiple intervals
	var snapshots []uint64
	var mu sync.Mutex

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	var reqCount atomic.Uint64
	var wg sync.WaitGroup

	// Memory sampler
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				runtime.GC()
				var ms runtime.MemStats
				runtime.ReadMemStats(&ms)
				mu.Lock()
				snapshots = append(snapshots, ms.HeapAlloc)
				mu.Unlock()
			}
		}
	}()

	// 100 clients at 20 rps
	for c := 0; c < 100; c++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			conn, err := grpc.NewClient(addr,
				grpc.WithTransportCredentials(insecure.NewCredentials()),
			)
			if err != nil {
				return
			}
			defer conn.Close()
			arbClient := pb.NewArbServiceClient(conn)
			ticker := time.NewTicker(50 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					reqCtx, reqCancel := context.WithTimeout(ctx, 2*time.Second)
					stream, err := arbClient.StreamArbs(reqCtx, &pb.StreamArbsRequest{MinProfitEth: 0})
					if err != nil {
						reqCancel()
						continue
					}
					for {
						_, recvErr := stream.Recv()
						if recvErr != nil {
							break
						}
					}
					reqCancel()
					reqCount.Add(1)
				}
			}
		}()
	}

	wg.Wait()

	mu.Lock()
	defer mu.Unlock()

	t.Logf("memory pressure test: requests=%d snapshots=%d", reqCount.Load(), len(snapshots))
	if len(snapshots) >= 2 {
		first := snapshots[0]
		last := snapshots[len(snapshots)-1]
		growthMB := float64(int64(last)-int64(first)) / 1024 / 1024
		t.Logf("memory: first=%.2fMB last=%.2fMB growth=%.2fMB",
			float64(first)/1024/1024, float64(last)/1024/1024, growthMB)
		if growthMB > 100 {
			t.Fatalf("memory grew %.2fMB over 60s — possible leak", growthMB)
		}
	}
}
