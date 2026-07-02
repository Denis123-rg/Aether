package testutil

import (
	"context"
	"testing"
	"time"

	pb "github.com/aether-arb/aether/internal/pb"
)

func TestFixtureArbs(t *testing.T) {
	_ = ProfitableTriangleArb()
	_ = Profitable2HopArb()
	_ = MarginalProfitArb()
	_ = LowProfitArb()
	_ = LargeTradeArb()
	arbs := BatchArbs()
	if len(arbs) != 5 {
		t.Fatalf("expected 5 arbs, got %d", len(arbs))
	}
}

func TestMockArbServer_TCP(t *testing.T) {
	srv := NewMockArbServer()
	addr, err := srv.Start()
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if addr == "" {
		t.Fatal("expected address")
	}
	defer srv.Stop()

	srv.SetArbs([]*pb.ValidatedArb{ProfitableTriangleArb()})
	if got := srv.Addr(); got == "" {
		t.Fatal("expected non-empty Addr")
	}
}

func TestMockArbServer_Bufconn(t *testing.T) {
	srv := NewMockArbServer()
	dialer, cleanup, err := srv.StartBufconn(0)
	if err != nil {
		t.Fatalf("StartBufconn: %v", err)
	}
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := srv.DialBufconn(ctx, dialer)
	if err != nil {
		t.Fatalf("DialBufconn: %v", err)
	}
	_ = conn.Close()
}

func TestMockArbServer_StreamFilter(t *testing.T) {
	srv := NewMockArbServer()
	dialer, cleanup, err := srv.StartBufconn(0)
	if err != nil {
		t.Fatalf("StartBufconn: %v", err)
	}
	defer cleanup()

	srv.SetArbs(BatchArbs())

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := srv.DialBufconn(ctx, dialer)
	if err != nil {
		t.Fatalf("DialBufconn: %v", err)
	}
	defer conn.Close()

	client := pb.NewArbServiceClient(conn)
	stream, err := client.StreamArbs(ctx, &pb.StreamArbsRequest{MinProfitEth: 0.005})
	if err != nil {
		t.Fatalf("StreamArbs: %v", err)
	}
	for {
		_, err := stream.Recv()
		if err != nil {
			break
		}
	}
}

func TestProfitWeiToFloat(t *testing.T) {
	if got := profitWeiToFloat(nil); got != 0 {
		t.Fatalf("expected 0, got %v", got)
	}
	if got := profitWeiToFloat([]byte{0x01, 0x00}); got <= 0 {
		t.Fatalf("expected positive, got %v", got)
	}
}
