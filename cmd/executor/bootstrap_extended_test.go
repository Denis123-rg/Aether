package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/aether-arb/aether/internal/config"
	"github.com/aether-arb/aether/internal/db"
	aethergrpc "github.com/aether-arb/aether/internal/grpc"
	pb "github.com/aether-arb/aether/internal/pb"
	"github.com/aether-arb/aether/internal/testutil"
)

func TestDefaultEthDial_InvalidEndpoint(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := defaultEthDial(ctx, "://not-a-valid-url")
	if err == nil {
		t.Fatal("expected dial error for malformed URL")
	}
}

func TestBootstrap_NilDialUsesDefaultEthDial(t *testing.T) {
	srv := mockEthRPC(t, 1, []byte{0x60, 0x80, 0x60, 0x40})
	defer srv.Close()

	execCfg := config.ExecutorFileConfig{
		ExecutorAddress: "0x0000000000000000000000000000000000000001",
		ExpectedChainID: 1,
	}
	res, err := bootstrap(context.Background(), execCfg, srv.URL, nil)
	if err != nil {
		t.Fatalf("bootstrap with nil dial: %v", err)
	}
	defer res.Client.Close()
	if res.ChainID != 1 {
		t.Fatalf("chain id = %d", res.ChainID)
	}
}

func repoConfigDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs(filepath.Join("..", "..", "config"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "builders.yaml")); err != nil {
		t.Skip("repo config/builders.yaml not found from test cwd")
	}
	return dir
}

func TestLoadConfig_BuildersYAMLRoutingAndStrategy(t *testing.T) {
	prevRouting := routingMode
	t.Cleanup(func() { routingMode = prevRouting })

	t.Setenv("AETHER_CONFIG_DIR", repoConfigDir(t))
	cfg := loadConfig()
	found := false
	for _, b := range cfg.BuilderConfigs {
		if b.Name == "flashbots" {
			found = true
			if b.URL == "" {
				t.Fatal("flashbots URL should be set")
			}
			break
		}
	}
	if !found {
		t.Fatal("expected flashbots builder from builders.yaml")
	}
	if cfg.RoutingMode != "select" {
		t.Fatalf("routing_mode = %q, want select", cfg.RoutingMode)
	}
	if cfg.Strategy.ExplorationFloor <= 0 {
		t.Fatal("strategy config should be loaded from builders.yaml")
	}
}

func TestConsumeArbStream_RecvErrorReconnects(t *testing.T) {
	srv := testutil.NewMockArbServer()
	srv.SetArbs([]*pb.ValidatedArb{testutil.ProfitableTriangleArb()})
	addr, err := srv.Start()
	if err != nil {
		t.Fatalf("start mock: %v", err)
	}
	defer srv.Stop()

	client, err := aethergrpc.Dial(addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer client.Close()

	rm, bundler, submitter := newTestComponents()
	ctx, cancel := context.WithTimeout(context.Background(), 800*time.Millisecond)
	defer cancel()

	lb := NewLiveBalance()
	lb.Set(0.5)
	// Stream ends after one arb → recv error → reconnect loop until ctx expires.
	consumeArbStream(ctx, client, bundler, submitter, rm, db.NewNoopLedger(),
		"0x0000000000000000000000000000000000000001", lb, 30*time.Millisecond)
}

func TestConsumeArbStream_ZeroReconnectDelayUsesDefault(t *testing.T) {
	client, err := aethergrpc.Dial("127.0.0.1:1")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer client.Close()

	rm, bundler, submitter := newTestComponents()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	lb := NewLiveBalance()
	lb.Set(0.5)
	consumeArbStream(ctx, client, bundler, submitter, rm, db.NewNoopLedger(),
		"0x0000000000000000000000000000000000000001", lb, 0)
}
