package stress_test

import (
	"context"
	"fmt"
	"math/rand"
	"sync/atomic"
	"testing"
	"time"
)

func TestStressWebsocketFlakyConnections(t *testing.T) {
	t.Parallel()
	SkipIfShort(t)
	cfg := DefaultStressConfig(LoadMedium)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Duration)
	defer cancel()

	var (
		connected atomic.Bool
		ops       int64
		fails     int64
	)
	connected.Store(true)

	go func() {
		ticker := time.NewTicker(300 * time.Millisecond)
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
		atomic.AddInt64(&ops, 1)
		if !connected.Load() {
			atomic.AddInt64(&fails, 1)
			return fmt.Errorf("websocket disconnected")
		}
		time.Sleep(time.Millisecond)
		return nil
	})

	t.Logf("websocket flaky: ops=%d fails=%d err=%v",
		atomic.LoadInt64(&ops), atomic.LoadInt64(&fails), err)
}

func TestStressGRPCStreamBackpressure(t *testing.T) {
	t.Parallel()
	SkipIfShort(t)
	cfg := DefaultStressConfig(LoadHigh)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Duration)
	defer cancel()

	var (
		processed int64
		dropped   int64
	)

	stream := make(chan int, 100)
	go func() {
		defer close(stream)
		for i := 0; ; i++ {
			select {
			case stream <- i:
			default:
				atomic.AddInt64(&dropped, 1)
			}
			select {
			case <-ctx.Done():
				return
			default:
			}
		}
	}()

	err := generateLoad(ctx, cfg.Concurrency, cfg.RatePerSecond, func(ctx context.Context) error {
		select {
		case _, ok := <-stream:
			if !ok {
				return ctx.Err()
			}
			atomic.AddInt64(&processed, 1)
		case <-ctx.Done():
			return ctx.Err()
		}
		return nil
	})

	t.Logf("gRPC stream backpressure: processed=%d dropped=%d err=%v",
		atomic.LoadInt64(&processed), atomic.LoadInt64(&dropped), err)
}

func TestStressMempoolNetworkPartition(t *testing.T) {
	t.Parallel()
	SkipIfShort(t)
	cfg := DefaultStressConfig(LoadMedium)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Duration)
	defer cancel()

	var (
		partitioned atomic.Bool
		ops         int64
		queued      int64
	)
	partitioned.Store(true)

	go func() {
		ticker := time.NewTicker(600 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				partitioned.Store(!partitioned.Load())
			}
		}
	}()

	pending := make(chan struct{}, 1000)
	err := generateLoad(ctx, cfg.Concurrency, cfg.RatePerSecond, func(ctx context.Context) error {
		atomic.AddInt64(&ops, 1)
		if partitioned.Load() {
			select {
			case pending <- struct{}{}:
				atomic.AddInt64(&queued, 1)
			default:
			}
			return fmt.Errorf("network partitioned")
		}
		select {
		case <-pending:
		default:
		}
		return nil
	})

	t.Logf("mempool network partition: ops=%d queued=%d err=%v",
		atomic.LoadInt64(&ops), atomic.LoadInt64(&queued), err)
}

func TestStressDiscoveryServiceFlakes(t *testing.T) {
	t.Parallel()
	SkipIfShort(t)
	cfg := DefaultStressConfig(LoadMedium)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Duration)
	defer cancel()

	var (
		available atomic.Bool
		ops       int64
		fails     int64
		retries   int64
	)
	available.Store(true)

	go func() {
		ticker := time.NewTicker(400 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				available.Store(!available.Load())
			}
		}
	}()

	err := generateLoad(ctx, cfg.Concurrency, cfg.RatePerSecond, func(ctx context.Context) error {
		atomic.AddInt64(&ops, 1)
		if !available.Load() {
			atomic.AddInt64(&fails, 1)
			time.Sleep(time.Duration(50+rand.Intn(100)) * time.Millisecond)
			atomic.AddInt64(&retries, 1)
			return fmt.Errorf("discovery unavailable")
		}
		return nil
	})

	t.Logf("discovery flakes: ops=%d fails=%d retries=%d err=%v",
		atomic.LoadInt64(&ops), atomic.LoadInt64(&fails), atomic.LoadInt64(&retries), err)
}

func TestStressLatencyInjectionPipeline(t *testing.T) {
	t.Parallel()
	SkipIfShort(t)
	cfg := DefaultStressConfig(LoadMedium)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Duration)
	defer cancel()

	var (
		ops        int64
		totalDelay time.Duration
	)

	err := generateLoad(ctx, cfg.Concurrency, cfg.RatePerSecond, func(ctx context.Context) error {
		latency := time.Duration(rand.Intn(50)) * time.Millisecond
		select {
		case <-time.After(latency):
		case <-ctx.Done():
			return ctx.Err()
		}
		atomic.AddInt64(&ops, 1)
		atomic.AddInt64((*int64)(&totalDelay), int64(latency))
		return nil
	})

	t.Logf("latency injection: ops=%d total_delay=%v err=%v",
		atomic.LoadInt64(&ops), time.Duration(totalDelay), err)
}

func TestStressTimeoutCascadePropagation(t *testing.T) {
	t.Parallel()
	SkipIfShort(t)
	cfg := DefaultStressConfig(LoadMedium)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Duration)
	defer cancel()

	var (
		ops    int64
		timeOs int64
	)

	err := generateLoad(ctx, cfg.Concurrency, cfg.RatePerSecond, func(ctx context.Context) error {
		timeout := time.Duration(rand.Intn(20)) * time.Millisecond
		subCtx, subCancel := context.WithTimeout(ctx, timeout)
		defer subCancel()
		select {
		case <-subCtx.Done():
			atomic.AddInt64(&timeOs, 1)
			return subCtx.Err()
		case <-time.After(5 * time.Millisecond):
			atomic.AddInt64(&ops, 1)
			return nil
		}
	})

	t.Logf("timeout cascade: ops=%d timeouts=%d err=%v",
		atomic.LoadInt64(&ops), atomic.LoadInt64(&timeOs), err)
}

func TestStressPacketLossRecovery(t *testing.T) {
	t.Parallel()
	SkipIfShort(t)
	cfg := DefaultStressConfig(LoadMedium)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Duration)
	defer cancel()

	var (
		ops    int64
		losses int64
	)

	err := generateLoad(ctx, cfg.Concurrency, cfg.RatePerSecond, func(ctx context.Context) error {
		atomic.AddInt64(&ops, 1)
		f := func() error {
			if rand.Intn(5) == 0 {
				atomic.AddInt64(&losses, 1)
				return fmt.Errorf("packet lost")
			}
			return nil
		}
		for i := 0; i < 3; i++ {
			if e := f(); e == nil {
				return nil
			}
			time.Sleep(time.Millisecond)
		}
		return nil
	})

	t.Logf("packet loss recovery: ops=%d losses=%d err=%v",
		atomic.LoadInt64(&ops), atomic.LoadInt64(&losses), err)
}

func TestStressReconnectStorm(t *testing.T) {
	t.Parallel()
	SkipIfShort(t)
	cfg := DefaultStressConfig(LoadBurst)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Duration)
	defer cancel()

	var (
		connections int64
		fails       int64
		serverUp    atomic.Bool
	)
	serverUp.Store(true)

	go func() {
		ticker := time.NewTicker(200 * time.Millisecond)
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

	err := generateLoadUnlimited(ctx, cfg.Concurrency, func(ctx context.Context) error {
		atomic.AddInt64(&connections, 1)
		if !serverUp.Load() {
			atomic.AddInt64(&fails, 1)
			return fmt.Errorf("server down")
		}
		return nil
	})

	t.Logf("reconnect storm: connections=%d fails=%d err=%v",
		atomic.LoadInt64(&connections), atomic.LoadInt64(&fails), err)
}

func TestStressPartialNetworkFailureRouting(t *testing.T) {
	t.Parallel()
	SkipIfShort(t)
	cfg := DefaultStressConfig(LoadMedium)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Duration)
	defer cancel()

	type endpoint struct {
		name   string
		active atomic.Bool
	}
	endpoints := []*endpoint{
		{name: "flashbots"},
		{name: "titan"},
		{name: "eden"},
		{name: "rsync"},
		{name: "beaver"},
	}
	for _, ep := range endpoints {
		ep.active.Store(true)
	}

	go func() {
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				for _, ep := range endpoints {
					if rand.Intn(3) == 0 {
						ep.active.Store(!ep.active.Load())
					}
				}
			}
		}
	}()

	var (
		ops     int64
		routed  int64
		blocked int64
	)

	err := generateLoad(ctx, cfg.Concurrency, cfg.RatePerSecond, func(ctx context.Context) error {
		atomic.AddInt64(&ops, 1)
		for _, ep := range endpoints {
			if ep.active.Load() {
				atomic.AddInt64(&routed, 1)
			} else {
				atomic.AddInt64(&blocked, 1)
			}
		}
		return nil
	})

	t.Logf("partial network failure: ops=%d routed=%d blocked=%d err=%v",
		atomic.LoadInt64(&ops), atomic.LoadInt64(&routed), atomic.LoadInt64(&blocked), err)
}

func TestStressIOBurstSaturation(t *testing.T) {
	t.Parallel()
	SkipIfShort(t)
	cfg := DefaultStressConfig(LoadBurst)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Duration)
	defer cancel()

	var (
		writes int64
	)

	err := generateLoadUnlimited(ctx, cfg.Concurrency, func(ctx context.Context) error {
		buf := make([]byte, 4096)
		for i := range buf {
			buf[i] = byte(i & 0xff)
		}
		_ = buf
		atomic.AddInt64(&writes, 1)
		return nil
	})

	t.Logf("IO burst saturation: writes=%d err=%v", atomic.LoadInt64(&writes), err)
}

func TestStressDNSResolutionFlapping(t *testing.T) {
	t.Parallel()
	SkipIfShort(t)
	cfg := DefaultStressConfig(LoadMedium)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Duration)
	defer cancel()

	var (
		resolves int64
		fails    int64
	)

	err := generateLoad(ctx, cfg.Concurrency, cfg.RatePerSecond, func(ctx context.Context) error {
		atomic.AddInt64(&resolves, 1)
		if rand.Intn(4) == 0 {
			atomic.AddInt64(&fails, 1)
			return fmt.Errorf("dns resolution failed")
		}
		return nil
	})

	t.Logf("DNS flapping: resolves=%d fails=%d err=%v",
		atomic.LoadInt64(&resolves), atomic.LoadInt64(&fails), err)
}

func TestStressTLSHandshakeStorm(t *testing.T) {
	t.Parallel()
	SkipIfShort(t)
	cfg := DefaultStressConfig(LoadBurst)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Duration)
	defer cancel()

	var ops int64
	err := generateLoadUnlimited(ctx, cfg.Concurrency, func(ctx context.Context) error {
		atomic.AddInt64(&ops, 1)
		cert := make([]byte, 2048)
		key := make([]byte, 2048)
		_, _ = rand.Read(cert)
		_, _ = rand.Read(key)
		_ = cert
		_ = key
		return nil
	})

	t.Logf("TLS handshake storm: ops=%d err=%v", atomic.LoadInt64(&ops), err)
}

func TestStressProxyRotationUnderLoad(t *testing.T) {
	t.Parallel()
	SkipIfShort(t)
	cfg := DefaultStressConfig(LoadMedium)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Duration)
	defer cancel()

	proxies := []string{"proxy1", "proxy2", "proxy3", "proxy4", "proxy5"}
	var (
		ops     int64
		rotates int64
	)

	err := generateLoad(ctx, cfg.Concurrency, cfg.RatePerSecond, func(ctx context.Context) error {
		idx := atomic.AddInt64(&ops, 1) % int64(len(proxies))
		proxy := proxies[idx]
		if atomic.AddInt64(&rotates, 1)%10 == 0 {
			_ = proxy
		}
		return nil
	})

	t.Logf("proxy rotation: ops=%d rotates=%d err=%v",
		atomic.LoadInt64(&ops), atomic.LoadInt64(&rotates), err)
}
