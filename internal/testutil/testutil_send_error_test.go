package testutil

import (
	"context"
	"fmt"
	"testing"
	"time"

	pb "github.com/aether-arb/aether/internal/pb"
)

func TestMockArbServer_StreamArbs_SendError(t *testing.T) {
	srv := NewMockArbServer()

	dialer, cleanup, err := srv.StartBufconn(64)
	if err != nil {
		t.Fatalf("StartBufconn: %v", err)
	}
	defer cleanup()

	var arbs []*pb.ValidatedArb
	for i := 0; i < 50; i++ {
		arb := ProfitableTriangleArb()
		arb.Id = fmt.Sprintf("arb-%d", i)
		arbs = append(arbs, arb)
	}
	srv.SetArbs(arbs)

	ctx := context.Background()
	conn, err := srv.DialBufconn(ctx, dialer)
	if err != nil {
		t.Fatalf("DialBufconn: %v", err)
	}

	client := pb.NewArbServiceClient(conn)
	stream, err := client.StreamArbs(ctx, &pb.StreamArbsRequest{MinProfitEth: 0})
	if err != nil {
		t.Fatalf("StreamArbs: %v", err)
	}

	_, _ = stream.Recv()
	conn.Close()

	for {
		_, err := stream.Recv()
		if err != nil {
			break
		}
	}
}

func TestMockArbServer_StreamArbs_ContextCancellation(t *testing.T) {
	srv := NewMockArbServer()

	dialer, cleanup, err := srv.StartBufconn(0)
	if err != nil {
		t.Fatalf("StartBufconn: %v", err)
	}
	defer cleanup()

	var arbs []*pb.ValidatedArb
	for i := 0; i < 10; i++ {
		arb := ProfitableTriangleArb()
		arb.Id = fmt.Sprintf("arb-cancel-%d", i)
		arbs = append(arbs, arb)
	}
	srv.SetArbs(arbs)

	ctx, cancel := context.WithCancel(context.Background())
	conn, err := srv.DialBufconn(ctx, dialer)
	if err != nil {
		t.Fatalf("DialBufconn: %v", err)
	}
	defer conn.Close()

	client := pb.NewArbServiceClient(conn)
	stream, err := client.StreamArbs(ctx, &pb.StreamArbsRequest{MinProfitEth: 0})
	if err != nil {
		t.Fatalf("StreamArbs: %v", err)
	}

	_, _ = stream.Recv()
	cancel()

	time.Sleep(100 * time.Millisecond)

	for {
		_, err := stream.Recv()
		if err != nil {
			break
		}
	}
}

func TestMockArbServer_StreamArbs_BelowMinProfit(t *testing.T) {
	srv := NewMockArbServer()
	dialer, cleanup, err := srv.StartBufconn(0)
	if err != nil {
		t.Fatalf("StartBufconn: %v", err)
	}
	defer cleanup()

	arbs := []*pb.ValidatedArb{
		LowProfitArb(),
		MarginalProfitArb(),
	}
	srv.SetArbs(arbs)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	conn, err := srv.DialBufconn(ctx, dialer)
	if err != nil {
		t.Fatalf("DialBufconn: %v", err)
	}
	defer conn.Close()

	client := pb.NewArbServiceClient(conn)
	stream, err := client.StreamArbs(ctx, &pb.StreamArbsRequest{MinProfitEth: 999.0})
	if err != nil {
		t.Fatalf("StreamArbs: %v", err)
	}

	_, err = stream.Recv()
	if err == nil {
		t.Fatal("expected EOF when all arbs filtered by min profit")
	}
}

func TestMockArbServer_StreamArbs_ArbsAboveThreshold(t *testing.T) {
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
	stream, err := client.StreamArbs(ctx, &pb.StreamArbsRequest{MinProfitEth: 0})
	if err != nil {
		t.Fatalf("StreamArbs: %v", err)
	}

	var count int
	for {
		_, err := stream.Recv()
		if err != nil {
			break
		}
		count++
	}
	if count != 5 {
		t.Fatalf("expected 5 arbs with minProfit=0, got %d", count)
	}
}

func TestMockArbServer_StreamArbs_EmptyNoArbs(t *testing.T) {
	srv := NewMockArbServer()
	dialer, cleanup, err := srv.StartBufconn(0)
	if err != nil {
		t.Fatalf("StartBufconn: %v", err)
	}
	defer cleanup()

	srv.SetArbs(nil)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	conn, err := srv.DialBufconn(ctx, dialer)
	if err != nil {
		t.Fatalf("DialBufconn: %v", err)
	}
	defer conn.Close()

	client := pb.NewArbServiceClient(conn)
	stream, err := client.StreamArbs(ctx, &pb.StreamArbsRequest{MinProfitEth: 0})
	if err != nil {
		t.Fatalf("StreamArbs: %v", err)
	}

	_, err = stream.Recv()
	if err == nil {
		t.Fatal("expected EOF for empty arbs list")
	}
}

func TestMockArbServer_StreamArbs_ProfitFilter(t *testing.T) {
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
	stream, err := client.StreamArbs(ctx, &pb.StreamArbsRequest{MinProfitEth: 0.1})
	if err != nil {
		t.Fatalf("StreamArbs: %v", err)
	}

	var count int
	for {
		_, err := stream.Recv()
		if err != nil {
			break
		}
		count++
	}
	if count == 0 {
		t.Fatal("expected some arbs above 0.1 ETH threshold")
	}
}

func TestMockArbServer_StartBufconn_SmallBuffer(t *testing.T) {
	srv := NewMockArbServer()

	dialer, cleanup, err := srv.StartBufconn(16)
	if err != nil {
		t.Fatalf("StartBufconn: %v", err)
	}
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	conn, err := srv.DialBufconn(ctx, dialer)
	if err != nil {
		t.Fatalf("DialBufconn: %v", err)
	}
	defer conn.Close()
}

func TestMockArbServer_Stop_CalledTwice(t *testing.T) {
	srv := NewMockArbServer()
	_, cleanup, err := srv.StartBufconn(0)
	if err != nil {
		t.Fatalf("StartBufconn: %v", err)
	}
	defer cleanup()

	srv.Stop()
	srv.Stop()
}
