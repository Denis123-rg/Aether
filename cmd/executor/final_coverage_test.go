package main

import (
	"context"
	"math/big"
	"os"
	"testing"
	"time"

	aethergrpc "github.com/aether-arb/aether/internal/grpc"
	"github.com/aether-arb/aether/internal/risk"
	"github.com/aether-arb/aether/internal/strategy"
)

func TestLoadConfig_GRPCAddressOverride(t *testing.T) {
	t.Setenv("GRPC_ADDRESS", "127.0.0.1:55555")
	cfg := loadConfig()
	if cfg.GRPCAddress != "127.0.0.1:55555" {
		t.Fatalf("GRPCAddress = %q", cfg.GRPCAddress)
	}
}

func TestLoadConfig_ReturnsDefaultsWhenFilesMissing(t *testing.T) {
	// loadConfig falls back gracefully when config files are absent.
	cfg := loadConfig()
	if cfg.GRPCAddress == "" {
		t.Fatal("empty grpc address")
	}
	if len(cfg.BuilderConfigs) == 0 {
		t.Fatal("expected default builders")
	}
}

func TestLoadRiskConfig_Fallback(t *testing.T) {
	cfg := loadRiskConfig()
	if cfg.MaxGasGwei <= 0 {
		t.Fatalf("invalid risk config: %+v", cfg)
	}
}

func TestDefaultGRPCDial_InvalidAddress(t *testing.T) {
	_, err := defaultGRPCDial("not-a-valid-grpc-target")
	if err == nil {
		t.Fatal("expected dial error")
	}
}

func TestMetricsAddr_Table(t *testing.T) {
	tests := []struct {
		env, want string
	}{
		{"", ":9090"},
		{"9091", ":9091"},
		{":9099", ":9099"},
		{"127.0.0.1:9090", "127.0.0.1:9090"},
	}
	for _, tc := range tests {
		if tc.env == "" {
			os.Unsetenv("METRICS_PORT")
		} else {
			t.Setenv("METRICS_PORT", tc.env)
		}
		if got := metricsAddr(); got != tc.want {
			t.Fatalf("METRICS_PORT=%q: got %q want %q", tc.env, got, tc.want)
		}
	}
}

func TestRecordMempoolMetrics(t *testing.T) {
	recordMempoolBundleBuildLatency(1500 * time.Microsecond)
	recordMempoolMissingVictimRawTx()
}

func TestGenerateBundleID_Unique(t *testing.T) {
	a := GenerateBundleID()
	b := GenerateBundleID()
	if a == b || len(a) != 32 {
		t.Fatalf("ids = %q, %q", a, b)
	}
}

func TestAddBigIntCounter_NilSafe(t *testing.T) {
	addBigIntCounter(profitTotalWei, nil)
	addBigIntCounter(profitTotalWei, big.NewInt(1_000_000_000_000_000_000))
}

func TestAddGasSpent(t *testing.T) {
	addGasSpent(30.0, 21000)
	addGasSpent(0, 0)
}

func TestRecordBundleIncluded(t *testing.T) {
	recordBundleIncluded(SourceBlockDriven, big.NewInt(1e18), 30.0, 200000)
}

func TestLoadAdminPort_Defaults(t *testing.T) {
	port, url := loadAdminPort()
	if port <= 0 {
		t.Fatalf("port = %d", port)
	}
	_ = url
}

func TestStateToInt_AllStates(t *testing.T) {
	for _, st := range []risk.SystemState{
		risk.StateRunning, risk.StateDegraded, risk.StatePaused, risk.StateHalted,
	} {
		if got := stateToInt(st); got < 0 {
			t.Fatalf("stateToInt(%s) = %d", st, got)
		}
	}
}

func TestSetAuthSigner(t *testing.T) {
	submitter, err := NewSubmitter(defaultBuilderConfigs(), "")
	if err != nil {
		t.Fatal(err)
	}
	submitter.SetAuthSigner(nil)
}

func TestBuilderSelectorEmptyBuilders(t *testing.T) {
	old := builderSelector
	defer func() { builderSelector = old }()
	builderSelector = strategy.New([]string{}, strategy.Config{})
	if builderSelector == nil {
		t.Fatal("nil selector")
	}
}

func TestRun_DefaultReconnectDelay(t *testing.T) {
	deps := testRunDeps(t, "bufconn", func(_ string) (*aethergrpc.Client, error) {
		return nil, context.Canceled
	})
	deps.ReconnectDelay = 0 // exercise default branch
	cfg := defaultConfig()

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	deps.WaitForShutdown = func(ctx context.Context, c context.CancelFunc) error {
		<-time.After(50 * time.Millisecond)
		c()
		return nil
	}
	_ = run(ctx, &cfg, deps)
}

func TestRun_GRPCDialFailureStillStarts(t *testing.T) {
	deps := testRunDeps(t, "bufconn", func(_ string) (*aethergrpc.Client, error) {
		return nil, errTestDial
	})
	cfg := defaultConfig()

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	deps.WaitForShutdown = func(ctx context.Context, c context.CancelFunc) error {
		<-time.After(80 * time.Millisecond)
		c()
		return nil
	}
	if err := run(ctx, &cfg, deps); err != nil {
		t.Fatalf("run: %v", err)
	}
}

var errTestDial = context.DeadlineExceeded

func TestPreRegisterBuilderLabels(t *testing.T) {
	PreRegisterBuilderLabels([]string{"b1", "b2"})
}

func TestSetSystemState(t *testing.T) {
	setSystemState(1)
	setSystemState(0)
}

func TestRecordShadowBundle(t *testing.T) {
	recordShadowBundle()
}

func TestRecordBuilderResult(t *testing.T) {
	recordBuilderResult("flashbots", true, 10*time.Millisecond)
	recordBuilderResult("titan", false, 5*time.Millisecond)
}

func TestRecordBundleBuiltAndSubmitted(t *testing.T) {
	recordBundleBuilt(SourceMempoolBackrun)
	recordBundleSubmitted(SourceBlockDriven)
}
