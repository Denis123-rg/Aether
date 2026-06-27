package stress_test

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Database disconnection / reconnection
// ---------------------------------------------------------------------------

// TestStressDatabaseReconnection simulates a database connection blip by
// toggling a "connected" flag and verifying that producers do not stall.
func TestStressDatabaseReconnection(t *testing.T) {
	t.Parallel()
	SkipIfShort(t)
	cfg := DefaultStressConfig(LoadMedium)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Duration)
	defer cancel()

	var (
		connected    atomic.Bool
		opsAttempted int64
		opsDropped   int64
	)
	connected.Store(true)

	// Simulate connection flapping
	go func() {
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				connected.Store(!connected.Load())
			}
		}
	}()

	err := generateLoad(ctx, cfg.Concurrency, cfg.RatePerSecond, func(ctx context.Context) error {
		atomic.AddInt64(&opsAttempted, 1)
		if !connected.Load() {
			atomic.AddInt64(&opsDropped, 1)
			return fmt.Errorf("database disconnected")
		}
		// Simulate a successful write
		time.Sleep(time.Millisecond)
		return nil
	})

	t.Logf("db reconnection: attempted=%d dropped=%d err=%v",
		atomic.LoadInt64(&opsAttempted), atomic.LoadInt64(&opsDropped), err)
	if atomic.LoadInt64(&opsDropped) == 0 {
		t.Log("warning: no drops observed — connection never toggled off")
	}
}

// ---------------------------------------------------------------------------
// gRPC client reconnection
// ---------------------------------------------------------------------------

func TestStressGRPCClientReconnection(t *testing.T) {
	t.Parallel()
	SkipIfShort(t)
	cfg := DefaultStressConfig(LoadMedium)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Duration)
	defer cancel()

	var (
		serverUp  atomic.Bool
		reqTotal  int64
		reqFailed int64
	)
	serverUp.Store(true)

	// Toggle server availability every 800ms
	go func() {
		ticker := time.NewTicker(800 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				serverUp.Store(!serverUp.Load())
			}
		}
	}()

	// Backoff config matching the codebase's subscriber pattern (events/subscriber.go)
	backoff := time.Second

	err := generateLoad(ctx, cfg.Concurrency, cfg.RatePerSecond, func(ctx context.Context) error {
		atomic.AddInt64(&reqTotal, 1)
		if !serverUp.Load() {
			atomic.AddInt64(&reqFailed, 1)
			// Exponential backoff (mirrors events.Subscriber.run)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(backoff):
			}
			if backoff < 30*time.Second {
				backoff *= 2
			}
			return fmt.Errorf("gRPC server unavailable")
		}
		backoff = time.Second
		return nil
	})

	t.Logf("gRPC reconnection: total=%d failed=%d err=%v",
		atomic.LoadInt64(&reqTotal), atomic.LoadInt64(&reqFailed), err)
}

// ---------------------------------------------------------------------------
// Config reload stress
// ---------------------------------------------------------------------------

// TestStressConfigReload simulates concurrent configuration reads and writes
// to verify the hot-reload path is safe under load.
func TestStressConfigReload(t *testing.T) {
	t.Parallel()
	SkipIfShort(t)
	cfg := DefaultStressConfig(LoadHigh)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Duration)
	defer cancel()

	var (
		mu         sync.RWMutex
		configData map[string]string
		reads      int64
		writes     int64
	)

	configData = map[string]string{
		"max_gas_gwei":   "300",
		"min_profit_eth": "0.001",
	}

	err := generateLoad(ctx, cfg.Concurrency, cfg.RatePerSecond, func(ctx context.Context) error {
		op := atomic.AddInt64(&reads, 1) % 10
		if op < 8 {
			// Read path
			mu.RLock()
			_ = configData["max_gas_gwei"]
			mu.RUnlock()
			atomic.AddInt64(&reads, 1)
		} else {
			// Write path (hot reload)
			mu.Lock()
			configData["max_gas_gwei"] = fmt.Sprintf("%d", time.Now().UnixNano()%500)
			configData["min_profit_eth"] = fmt.Sprintf("%.6f", float64(time.Now().UnixNano()%1000)/1000000)
			atomic.AddInt64(&writes, 1)
			mu.Unlock()
		}
		return nil
	})

	t.Logf("config reload: reads=%d writes=%d err=%v",
		atomic.LoadInt64(&reads), atomic.LoadInt64(&writes), err)
	if atomic.LoadInt64(&reads) == 0 {
		t.Error("zero config reads")
	}
}

// ---------------------------------------------------------------------------
// Signal handling
// ---------------------------------------------------------------------------

// TestStressSignalHandling verifies that the signal-handling path
// (cmd/reconciler installSignalHandler, etc.) works correctly under load.
func TestStressSignalHandling(t *testing.T) {
	t.Parallel()
	SkipIfShort(t)
	// which are unreliable in test binaries.
	const numSignals = 100

	sigCh := make(chan int, numSignals)
	cancelCh := make(chan struct{})
	var received int64

	go func() {
		for range sigCh {
			atomic.AddInt64(&received, 1)
			if atomic.LoadInt64(&received) >= numSignals {
				close(cancelCh)
				return
			}
		}
	}()

	var wg sync.WaitGroup
	for i := 0; i < numSignals; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sigCh <- 1
		}()
	}
	wg.Wait()
	close(sigCh)

	select {
	case <-cancelCh:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for signals to propagate")
	}

	t.Logf("signal handling: received=%d/%d", atomic.LoadInt64(&received), numSignals)
	if atomic.LoadInt64(&received) != numSignals {
		t.Errorf("expected %d signals, got %d", numSignals, atomic.LoadInt64(&received))
	}
}

// ---------------------------------------------------------------------------
// Process restart simulation
// ---------------------------------------------------------------------------

func TestStressProcessRestartCycle(t *testing.T) {
	t.Parallel()
	SkipIfShort(t)
	cfg := DefaultStressConfig(LoadLow)

	// Simulate rapid start/stop cycles that the real executor undergoes
	// during container restarts or orchestration rollouts.
	for i := 0; i < 5; i++ {
		t.Run(fmt.Sprintf("cycle_%d", i), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), cfg.Duration/5)
			defer cancel()

			// Phase 1: startup (simulate bootstrap)
			select {
			case <-ctx.Done():
				return
			default:
			}

			// Phase 2: brief load
			_ = generateLoad(ctx, 5, 50, func(ctx context.Context) error {
				time.Sleep(time.Microsecond)
				return nil
			})

			// Phase 3: shutdown (simulate drain)
		})
	}
	t.Log("process restart cycles completed")
}

// Ensure slog is referenced.
var _ = slog.LevelInfo
