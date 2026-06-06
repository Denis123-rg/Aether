package main

import (
	"context"
	"errors"
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"

	"github.com/aether-arb/aether/internal/db"
	"github.com/aether-arb/aether/internal/metrics"
	"github.com/aether-arb/aether/internal/risk"
	"github.com/aether-arb/aether/internal/strategy"
)

func TestSignerHealthLoop_ExitsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		signerHealthLoop(ctx, func() error { return nil }, 20*time.Millisecond)
		close(done)
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("signerHealthLoop did not exit")
	}
}

func TestSignerHealthLoop_LogsPingError(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	signerHealthLoop(ctx, func() error { return errors.New("ping failed") }, 15*time.Millisecond)
}

func TestBalanceWatchLoop_ExitsOnCancel(t *testing.T) {
	mock := &mockBalanceProvider{balance: big.NewInt(1e18)}
	lb := NewLiveBalance()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		balanceWatchLoop(ctx, mock, common.HexToAddress("0xabc"), 20*time.Millisecond, lb, "http://mock")
		close(done)
	}()
	time.Sleep(40 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("balanceWatchLoop did not exit")
	}
}

type mockBalanceProvider struct {
	balance *big.Int
	err     error
}

func (m *mockBalanceProvider) BalanceAt(ctx context.Context, addr common.Address, blockNumber *big.Int) (*big.Int, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.balance, nil
}

func TestFetchAndStoreBalance(t *testing.T) {
	mock := &mockBalanceProvider{balance: big.NewInt(2e18)}
	lb := NewLiveBalance()
	if err := fetchAndStoreBalance(context.Background(), mock, common.HexToAddress("0xabc"), lb); err != nil {
		t.Fatalf("fetchAndStoreBalance: %v", err)
	}
	if lb.Get() < 1.9 {
		t.Fatalf("balance = %f, want ~2 ETH", lb.Get())
	}
}

func TestLogSelectorSnapshotLoop_ExitsOnCancel(t *testing.T) {
	oldSel := builderSelector
	oldStore := metricsStore
	defer func() {
		builderSelector = oldSel
		metricsStore = oldStore
	}()
	builderSelector = strategy.New([]string{"b1"}, strategy.Config{})
	metricsStore = db.NewNoopMetricsStore()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		logSelectorSnapshotLoop(ctx, 15*time.Millisecond)
		close(done)
	}()
	time.Sleep(40 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("logSelectorSnapshotLoop did not exit")
	}
}

func TestInclusionPollLoop_ExitsOnCancel(t *testing.T) {
	rm := risk.NewRiskManager(risk.DefaultRiskConfig())
	submitter, _ := NewSubmitter(defaultBuilderConfigs(), "")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		inclusionPollLoop(ctx, submitter, db.NewNoopLedger(), rm, 20*time.Millisecond)
		close(done)
	}()
	time.Sleep(40 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("inclusionPollLoop did not exit")
	}
}

func TestPollTopPoolsLoop_ExitsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	store := metrics.NewStore()
	done := make(chan struct{})
	go func() {
		pollTopPoolsLoop(ctx, "http://127.0.0.1:1", store, 20*time.Millisecond)
		close(done)
	}()
	time.Sleep(40 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("pollTopPoolsLoop did not exit")
	}
}

func TestStartMetricsServer_Binds(t *testing.T) {
	t.Setenv("AETHER_METRICS_PORT", "0")
	startMetricsServer()
}

func TestStateToInt_Default(t *testing.T) {
	if got := stateToInt(risk.SystemState("unknown")); got != -1 {
		t.Fatalf("stateToInt unknown = %d, want -1", got)
	}
}

func TestTokenLabel_EmptyAndKnown(t *testing.T) {
	if tokenLabel(nil) != "?" {
		t.Fatal("nil addr")
	}
	weth := common.Hex2Bytes("c02aaa39b223fe8d0a0e5c4f27ead9083c756cc2")
	if tokenLabel(weth) != "WETH" {
		t.Fatalf("got %q", tokenLabel(weth))
	}
}
