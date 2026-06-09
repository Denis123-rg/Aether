// Package e2e — full arb pipeline tests using mock gRPC + builder (no live services).
package e2e

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/aether-arb/aether/internal/events"
	aethergrpc "github.com/aether-arb/aether/internal/grpc"
	pb "github.com/aether-arb/aether/internal/pb"
	"github.com/aether-arb/aether/internal/testutil"
)

func TestE2E_MockGRPCHealthAndStream(t *testing.T) {
	srv := testutil.NewMockArbServer()
	srv.SetArbs([]*pb.ValidatedArb{testutil.ProfitableTriangleArb()})
	addr, err := srv.Start()
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Stop()

	client, err := aethergrpc.Dial(addr)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	health, err := client.CheckHealth(ctx)
	if err != nil || !health.Healthy {
		t.Fatalf("health err=%v healthy=%v", err, health.GetHealthy())
	}

	stream, err := client.StreamArbs(ctx, 0.001)
	if err != nil {
		t.Fatal(err)
	}
	arb, err := stream.Recv()
	if err != nil || arb.GetId() == "" {
		t.Fatalf("recv err=%v arb=%v", err, arb)
	}
}

func TestE2E_ControlSetStateViaClient(t *testing.T) {
	srv := testutil.NewMockArbServer()
	addr, err := srv.Start()
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Stop()

	client, err := aethergrpc.Dial(addr)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	resp, err := client.SetState(context.Background(), pb.SystemState_PAUSED, "e2e")
	if err != nil || !resp.Success {
		t.Fatalf("set state err=%v resp=%+v", err, resp)
	}
}

func TestE2E_ReloadConfigViaClient(t *testing.T) {
	srv := testutil.NewMockArbServer()
	addr, err := srv.Start()
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Stop()

	client, err := aethergrpc.Dial(addr)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	resp, err := client.ReloadConfig(context.Background(), "config/pools.toml")
	if err != nil || !resp.Success {
		t.Fatalf("reload err=%v resp=%+v", err, resp)
	}
}

func TestE2E_RedisPublisherWithMiniredis(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	defer mr.Close()

	pub := events.NewPublisher("redis://" + mr.Addr())
	if !pub.Enabled() {
		t.Fatal("publisher disabled")
	}
	defer pub.Close()
	pub.PublishPnLUpdate(0.01, 55.0)
}

func TestE2E_StreamMinProfitFilter(t *testing.T) {
	srv := testutil.NewMockArbServer()
	srv.SetArbs([]*pb.ValidatedArb{
		testutil.ProfitableTriangleArb(),
		testutil.LowProfitArb(),
	})
	addr, err := srv.Start()
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Stop()

	client, err := aethergrpc.Dial(addr)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	stream, err := client.StreamArbs(context.Background(), 0.005)
	if err != nil {
		t.Fatal(err)
	}
	arb, err := stream.Recv()
	if err != nil {
		t.Fatal(err)
	}
	if arb.GetId() != "arb-triangle-001" {
		t.Fatalf("got %s", arb.GetId())
	}
	_, err = stream.Recv()
	if err == nil {
		t.Fatal("expected stream end after filtered arbs")
	}
}
