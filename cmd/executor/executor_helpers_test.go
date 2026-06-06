package main

import (
	"context"
	"math/big"
	"testing"
	"time"

	"github.com/aether-arb/aether/internal/db"
	"github.com/aether-arb/aether/internal/risk"
	"github.com/aether-arb/aether/internal/strategy"
)

func TestDefaultConfig(t *testing.T) {
	cfg := defaultConfig()
	if cfg.GRPCAddress == "" || cfg.MaxGasGwei <= 0 {
		t.Fatalf("cfg: %+v", cfg)
	}
	if len(cfg.BuilderConfigs) == 0 {
		t.Fatal("expected default builders")
	}
}

func TestLoadConfig_LoadsBuildersYAML(t *testing.T) {
	cfg := loadConfig()
	if len(cfg.BuilderConfigs) == 0 {
		t.Fatal("expected builders from config or defaults")
	}
}

func TestResolveRoutingMode_Table(t *testing.T) {
	tests := []struct {
		mode   string
		fanOut bool
		want   string
	}{
		{"select", false, "select"},
		{"single", false, "select"},
		{"fanout", false, "fanout"},
		{"", true, "fanout"},
		{"", false, "select"},
		{"unknown", false, "fanout"},
	}
	for _, tc := range tests {
		if got := resolveRoutingMode(tc.mode, tc.fanOut); got != tc.want {
			t.Fatalf("resolveRoutingMode(%q,%v)=%q want %q", tc.mode, tc.fanOut, got, tc.want)
		}
	}
}

func TestLoadRiskConfig(t *testing.T) {
	rc := loadRiskConfig()
	if rc.MaxGasGwei <= 0 {
		t.Fatalf("risk config: %+v", rc)
	}
}

func TestSignedTxsHex(t *testing.T) {
	if signedTxsHex(nil) != "" {
		t.Fatal("nil bundle")
	}
	hex := signedTxsHex(&Bundle{RawTxs: [][]byte{{0xab, 0xcd}, {0x01}}})
	if hex != "0xabcd\n0x01" {
		t.Fatalf("hex = %q", hex)
	}
}

func TestGasSpentApprox(t *testing.T) {
	if gasSpentApprox(0, GasFees{GasPriceGwei: 30}).Sign() != 0 {
		t.Fatal("zero gas")
	}
	got := gasSpentApprox(100_000, GasFees{GasPriceGwei: 30})
	want := new(big.Int).Mul(big.NewInt(100_000), big.NewInt(30_000_000_000))
	if got.Cmp(want) != 0 {
		t.Fatalf("got %s want %s", got, want)
	}
}

func TestHexEncode(t *testing.T) {
	if hexEncode([]byte{0xde, 0xad}) != "dead" {
		t.Fatalf("got %q", hexEncode([]byte{0xde, 0xad}))
	}
}

func TestRecordSubmissionReverts(t *testing.T) {
	rm := risk.NewRiskManager(risk.DefaultRiskConfig())
	recordSubmissionReverts(rm, []SubmissionResult{
		{Success: false, Error: errTestRevert},
	})
}

var errTestRevert = &testRevertErr{msg: "execution reverted: insufficient profit"}

type testRevertErr struct{ msg string }

func (e *testRevertErr) Error() string { return e.msg }

func TestRecordBundleMetrics(t *testing.T) {
	oldStore := metricsStore
	oldSel := builderSelector
	metricsStore = db.NewNoopMetricsStore()
	builderSelector = strategy.New([]string{"flashbots"}, strategy.Config{ExplorationFloor: 0.1})
	t.Cleanup(func() {
		metricsStore = oldStore
		builderSelector = oldSel
	})

	recordBundleMetrics(SourceBlockDriven, big.NewInt(1e16), time.Now().Add(-time.Millisecond),
		[]SubmissionResult{{Builder: "flashbots", Success: true, Latency: time.Millisecond}}, true)
}

func TestLogSelectorSnapshotLoop(t *testing.T) {
	oldSel := builderSelector
	oldStore := metricsStore
	builderSelector = strategy.New([]string{"flashbots"}, strategy.Config{ExplorationFloor: 0.1})
	metricsStore = db.NewNoopMetricsStore()
	t.Cleanup(func() {
		builderSelector = oldSel
		metricsStore = oldStore
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		logSelectorSnapshotLoop(ctx, time.Millisecond)
		close(done)
	}()
	time.Sleep(5 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("loop did not exit")
	}
}

func TestBoolToFloat(t *testing.T) {
	if boolToFloat(true) != 1 || boolToFloat(false) != 0 {
		t.Fatal("boolToFloat")
	}
}

func TestStateToInt(t *testing.T) {
	if stateToInt(risk.StateRunning) != 0 {
		t.Fatal("running")
	}
	if stateToInt(risk.StateHalted) != 3 {
		t.Fatal("halted")
	}
	if stateToInt(risk.SystemState("bogus")) != -1 {
		t.Fatal("unknown")
	}
}

func TestExecutorMetricsObserver(t *testing.T) {
	oldStore := metricsStore
	metricsStore = db.NewNoopMetricsStore()
	t.Cleanup(func() { metricsStore = oldStore })

	var obs executorMetricsObserver
	obs.OnStateChange(risk.StatePaused)
	obs.OnCircuitBreakerTrip("test")
}

func TestIsShadowMode(t *testing.T) {
	t.Setenv("AETHER_SHADOW", "1")
	if !isShadowMode() {
		t.Fatal("shadow on")
	}
	t.Setenv("AETHER_SHADOW", "0")
	if isShadowMode() {
		t.Fatal("shadow off")
	}
}

func TestShadowBundleDumpDir(t *testing.T) {
	t.Setenv("AETHER_SHADOW_DUMP_DIR", t.TempDir())
	if shadowBundleDumpDir() == "" {
		t.Fatal("dump dir")
	}
}

func TestTokenLabel(t *testing.T) {
	if tokenLabel(nil) != "?" {
		t.Fatal("empty token")
	}
	if tokenLabel([]byte{0x01, 0x02, 0x03, 0x04}) == "?" {
		t.Fatal("short token should still label")
	}
}
