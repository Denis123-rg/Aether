package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/aether-arb/aether/internal/db"
	"github.com/aether-arb/aether/internal/risk"
	"github.com/aether-arb/aether/internal/strategy"
)

func TestPollPendingInclusions_ResolvesIncluded(t *testing.T) {
	pendingMu.Lock()
	pendingQueue = nil
	pendingMu.Unlock()

	oldSel := builderSelector
	oldStore := metricsStore
	defer func() {
		builderSelector = oldSel
		metricsStore = oldStore
	}()
	builderSelector = strategy.New([]string{"mock"}, strategy.Config{})
	metricsStore = db.NewNoopMetricsStore()

	profit := ethToWei(0.01)
	bid := uuid.New()
	enqueuePendingBundle(pendingBundle{
		bundleID:    bid,
		bundleHash:  "0xdead",
		targetBlock: 100,
		builder:     "mock",
		profitWei:   profit,
		source:      SourceBlockDriven,
		submittedAt: time.Now().UTC().Add(-20 * time.Second),
	})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"blockNumber":"0x64","isSentToMiners":true}}`))
	}))
	defer srv.Close()

	submitter, _ := NewSubmitter([]BuilderConfig{{
		Name: "flashbots", URL: srv.URL, AuthType: "flashbots", Enabled: true, TimeoutMs: 2000,
	}}, testSearcherKey)

	rm := risk.NewRiskManager(risk.DefaultRiskConfig())
	ledger := db.NewNoopLedger()
	pollPendingInclusions(context.Background(), submitter, ledger, rm)

	pendingMu.Lock()
	remaining := len(pendingQueue)
	pendingMu.Unlock()
	if remaining != 0 {
		t.Fatalf("expected queue drained, remaining=%d", remaining)
	}
}

func TestPollPendingInclusions_RetriesYoungBundle(t *testing.T) {
	pendingMu.Lock()
	pendingQueue = nil
	pendingMu.Unlock()

	enqueuePendingBundle(pendingBundle{
		bundleID:    uuid.New(),
		bundleHash:  "0xbeef",
		submittedAt: time.Now().UTC(),
	})

	submitter, _ := NewSubmitter([]BuilderConfig{{Name: "mock", Enabled: true}}, "")
	rm := risk.NewRiskManager(risk.DefaultRiskConfig())
	pollPendingInclusions(context.Background(), submitter, db.NewNoopLedger(), rm)

	pendingMu.Lock()
	n := len(pendingQueue)
	pendingMu.Unlock()
	if n != 1 {
		t.Fatalf("young bundle should remain, got %d", n)
	}
}

func TestResolveInclusion_IncludedAndMiss(t *testing.T) {
	oldSel := builderSelector
	defer func() { builderSelector = oldSel }()
	builderSelector = strategy.New([]string{"b1"}, strategy.Config{})

	rm := risk.NewRiskManager(risk.DefaultRiskConfig())
	ledger := db.NewNoopLedger()
	profit := ethToWei(0.02)

	resolveInclusion(pendingBundle{
		bundleID:  uuid.New(),
		builder:   "b1",
		profitWei: profit,
	}, ledger, true, 12345, rm)

	resolveInclusion(pendingBundle{
		bundleID:  uuid.New(),
		builder:   "b1",
		profitWei: profit,
	}, ledger, false, 0, rm)
}

func TestFmtSscanfHex_InvalidChar(t *testing.T) {
	var n uint64
	if _, err := fmtSscanfHex("0xgg", &n); err != nil {
		t.Fatalf("invalid hex should return nil error with partial parse: %v", err)
	}
}
