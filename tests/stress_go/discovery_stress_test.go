package stress_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestStressDiscoveryLargeScaleNodeTraversal(t *testing.T) {
	t.Parallel()
	SkipIfShort(t)
	cfg := DefaultStressConfig(LoadHigh)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Duration)
	defer cancel()

	type graphNode struct {
		addr  string
		peers []string
	}

	var (
		mu    sync.RWMutex
		nodes = make(map[string]*graphNode)
		ops   int64
	)

	for i := 0; i < 5000; i++ {
		addr := randomHexAddr()
		nodes[addr] = &graphNode{addr: addr, peers: make([]string, 0)}
	}

	err := generateLoad(ctx, cfg.Concurrency, cfg.RatePerSecond, func(ctx context.Context) error {
		atomic.AddInt64(&ops, 1)
		mu.RLock()
		for _, n := range nodes {
			_ = n.addr
			for _, p := range n.peers {
				_ = p
			}
			break
		}
		mu.RUnlock()
		return nil
	})

	t.Logf("large scale node traversal: ops=%d nodes=%d err=%v",
		atomic.LoadInt64(&ops), len(nodes), err)
}

func TestStressDiscoveryStaleEdgeCleanup(t *testing.T) {
	t.Parallel()
	SkipIfShort(t)
	cfg := DefaultStressConfig(LoadMedium)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Duration)
	defer cancel()

	type edge struct {
		src       string
		dst       string
		lastSeen  time.Time
	}
	var (
		mu      sync.Mutex
		edges   []edge
		added   int64
		removed int64
	)

	err := generateLoad(ctx, cfg.Concurrency, cfg.RatePerSecond, func(ctx context.Context) error {
		atomic.AddInt64(&added, 1)
		e := edge{
			src:      randomHexAddr(),
			dst:      randomHexAddr(),
			lastSeen: time.Now(),
		}
		mu.Lock()
		edges = append(edges, e)
		if len(edges) > 10000 {
			cutoff := time.Now().Add(-30 * time.Second)
			keep := edges[:0]
			for _, edge := range edges {
				if edge.lastSeen.After(cutoff) {
					keep = append(keep, edge)
				} else {
					atomic.AddInt64(&removed, 1)
				}
			}
			edges = keep
		}
		mu.Unlock()
		return nil
	})

	t.Logf("stale edge cleanup: added=%d removed=%d err=%v",
		atomic.LoadInt64(&added), atomic.LoadInt64(&removed), err)
}

func TestStressDiscoveryConcurrentFactoryEvents(t *testing.T) {
	t.Parallel()
	SkipIfShort(t)
	cfg := DefaultStressConfig(LoadHigh)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Duration)
	defer cancel()

	type factoryEvent struct {
		poolAddr string
		token0   string
		token1   string
		block    uint64
	}
	var (
		mu      sync.Mutex
		events  []factoryEvent
		ops     int64
	)

	err := generateLoad(ctx, cfg.Concurrency, cfg.RatePerSecond, func(ctx context.Context) error {
		e := factoryEvent{
			poolAddr: randomHexAddr(),
			token0:   randomHexAddr(),
			token1:   randomHexAddr(),
			block:    uint64(atomic.AddInt64(&ops, 1)),
		}
		mu.Lock()
		events = append(events, e)
		if len(events) > 5000 {
			events = events[len(events)/2:]
		}
		mu.Unlock()
		return nil
	})

	t.Logf("concurrent factory events: ops=%d pending=%d err=%v",
		atomic.LoadInt64(&ops), len(events), err)
}

func TestStressDiscoveryPeerRotation(t *testing.T) {
	t.Parallel()
	SkipIfShort(t)
	cfg := DefaultStressConfig(LoadMedium)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Duration)
	defer cancel()

	peers := make([]string, 20)
	for i := range peers {
		peers[i] = randomHexAddr()
	}

	var ops int64
	err := generateLoad(ctx, cfg.Concurrency, cfg.RatePerSecond, func(ctx context.Context) error {
		idx := atomic.AddInt64(&ops, 1) % int64(len(peers))
		peer := peers[idx]
		_ = peer
		return nil
	})

	t.Logf("discovery peer rotation: ops=%d err=%v", atomic.LoadInt64(&ops), err)
}

func TestStressDiscoveryNetworkTopologyChange(t *testing.T) {
	t.Parallel()
	SkipIfShort(t)
	cfg := DefaultStressConfig(LoadMedium)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Duration)
	defer cancel()

	var (
		mu      sync.RWMutex
		topo    = make(map[string][]string)
		ops     int64
		changes int64
	)

	err := generateLoad(ctx, cfg.Concurrency, cfg.RatePerSecond, func(ctx context.Context) error {
		op := atomic.AddInt64(&ops, 1)
		if op%5 == 0 {
			mu.Lock()
			topo[randomHexAddr()] = []string{randomHexAddr(), randomHexAddr()}
			atomic.AddInt64(&changes, 1)
			mu.Unlock()
		} else {
			mu.RLock()
			for k, v := range topo {
				_ = k
				_ = v
				break
			}
			mu.RUnlock()
		}
		return nil
	})

	t.Logf("network topology change: ops=%d changes=%d err=%v",
		atomic.LoadInt64(&ops), atomic.LoadInt64(&changes), err)
}
