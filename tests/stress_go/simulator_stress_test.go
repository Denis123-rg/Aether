package stress_test

import (
	"context"
	"math/big"
	"math/rand"
	"sync"
	"sync/atomic"
	"testing"
)

func TestStressSimulatorHighThroughputOps(t *testing.T) {
	t.Parallel()
	SkipIfShort(t)
	cfg := DefaultStressConfig(LoadBurst)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Duration)
	defer cancel()

	var ops int64
	err := generateLoadUnlimited(ctx, cfg.Concurrency, func(ctx context.Context) error {
		a := big.NewInt(rand.Int63())
		b := big.NewInt(rand.Int63())
		c := new(big.Int).Mul(a, b)
		d := new(big.Int).Div(c, big.NewInt(1000000))
		_ = d
		atomic.AddInt64(&ops, 1)
		return nil
	})

	t.Logf("simulator high throughput: ops=%d err=%v", atomic.LoadInt64(&ops), err)
}

func TestStressSimulatorArbitrageBurst(t *testing.T) {
	t.Parallel()
	SkipIfShort(t)
	cfg := DefaultStressConfig(LoadBurst)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Duration)
	defer cancel()

	type arbOpp struct {
		profitWei   *big.Int
		gasEstimate uint64
		path        []string
	}

	var (
		mu     sync.Mutex
		opps   []arbOpp
		ops    int64
	)

	err := generateLoadUnlimited(ctx, cfg.Concurrency, func(ctx context.Context) error {
		opp := arbOpp{
			profitWei:   big.NewInt(rand.Int63n(1000000)),
			gasEstimate: uint64(rand.Intn(500000) + 100000),
			path:        []string{randomHexAddr(), randomHexAddr(), randomHexAddr()},
		}
		mu.Lock()
		opps = append(opps, opp)
		atomic.AddInt64(&ops, 1)
		mu.Unlock()
		return nil
	})

	t.Logf("arbitrage burst: ops=%d queue=%d err=%v",
		atomic.LoadInt64(&ops), len(opps), err)
}

func TestStressSimulatorMultiPathEval(t *testing.T) {
	t.Parallel()
	SkipIfShort(t)
	cfg := DefaultStressConfig(LoadHigh)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Duration)
	defer cancel()

	var ops int64
	paths := make([][]string, 100)
	for i := range paths {
		n := rand.Intn(5) + 2
		p := make([]string, n)
		for j := range p {
			p[j] = randomHexAddr()
		}
		paths[i] = p
	}

	err := generateLoad(ctx, cfg.Concurrency, cfg.RatePerSecond, func(ctx context.Context) error {
		idx := atomic.AddInt64(&ops, 1) % int64(len(paths))
		path := paths[idx]
		var profit float64
		for range path {
			profit += rand.Float64() * 0.01
		}
		_ = profit
		return nil
	})

	t.Logf("multi-path evaluation: ops=%d paths=%d err=%v",
		atomic.LoadInt64(&ops), len(paths), err)
}

func TestStressSimulatorPriceImpactCalc(t *testing.T) {
	t.Parallel()
	SkipIfShort(t)
	cfg := DefaultStressConfig(LoadHigh)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Duration)
	defer cancel()

	var ops int64
	err := generateLoad(ctx, cfg.Concurrency, cfg.RatePerSecond, func(ctx context.Context) error {
		reserve0 := big.NewInt(rand.Int63n(1000000000) + 1000000)
		reserve1 := big.NewInt(rand.Int63n(1000000000) + 1000000)
		amount := big.NewInt(rand.Int63n(100000) + 1000)

		num := new(big.Int).Mul(amount, reserve1)
		denom := new(big.Int).Add(reserve0, amount)
		output := new(big.Int).Div(num, denom)

		priceImpact := new(big.Int).Mul(output, big.NewInt(10000))
		priceImpact.Div(priceImpact, reserve1)
		_ = priceImpact
		atomic.AddInt64(&ops, 1)
		return nil
	})

	t.Logf("price impact calculation: ops=%d err=%v", atomic.LoadInt64(&ops), err)
}

func TestStressSimulatorFlashLoanSimulation(t *testing.T) {
	t.Parallel()
	SkipIfShort(t)
	cfg := DefaultStressConfig(LoadMedium)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Duration)
	defer cancel()

	var ops int64
	err := generateLoad(ctx, cfg.Concurrency, cfg.RatePerSecond, func(ctx context.Context) error {
		loanAmount := big.NewInt(rand.Int63n(1000000) + 100000)
		fee := new(big.Int).Div(loanAmount, big.NewInt(10000))
		repayAmount := new(big.Int).Add(loanAmount, fee)
		profit := big.NewInt(rand.Int63n(100000))
		netProfit := new(big.Int).Sub(profit, fee)
		_ = netProfit
		_ = repayAmount
		atomic.AddInt64(&ops, 1)
		return nil
	})

	t.Logf("flash loan simulation: ops=%d err=%v", atomic.LoadInt64(&ops), err)
}
