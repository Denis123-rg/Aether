//go:build load

package load

import (
	"context"
	"io"
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
	warmup := duration / 5
	if warmup < time.Second {
		warmup = time.Second
	}

	ctx, cancel := context.WithTimeout(context.Background(), duration)
	defer cancel()

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
					start := time.Now()
					stream, err := arbClient.StreamArbs(reqCtx, &pb.StreamArbsRequest{MinProfitEth: 0})
					if err != nil {
						reqCancel()
						if ctx.Err() == nil {
							errCount.Add(1)
						}
						continue
					}
					for {
						_, recvErr := stream.Recv()
						if recvErr == io.EOF {
							break
						}
						if recvErr != nil {
							if ctx.Err() == nil {
								errCount.Add(1)
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
	runtime.GC()
	var memAfter runtime.MemStats
	runtime.ReadMemStats(&memAfter)

	p99 := time.Duration(hist.ValueAtQuantile(99.0)) * time.Microsecond
	total := reqCount.Load()
	errs := errCount.Load()
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

	if total == 0 {
		t.Fatal("no requests completed")
	}
	if errs > 0 && float64(errs)/float64(total) > 0.01 {
		t.Fatalf("error rate too high: %d/%d", errs, total)
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
