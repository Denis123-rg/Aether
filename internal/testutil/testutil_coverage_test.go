package testutil

import (
	"context"
	"fmt"
	"testing"
	"time"

	pb "github.com/aether-arb/aether/internal/pb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestMockArbServer_SubmitArb_Success(t *testing.T) {
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
	defer conn.Close()

	client := pb.NewArbServiceClient(conn)
	resp, err := client.SubmitArb(ctx, ProfitableTriangleArb())
	if err != nil {
		t.Fatalf("SubmitArb: %v", err)
	}
	if !resp.Accepted {
		t.Fatal("expected accepted")
	}
	if resp.BundleHash == "" {
		t.Fatal("expected non-empty bundle hash")
	}
}

func TestMockArbServer_SubmitArb_Error(t *testing.T) {
	srv := NewMockArbServer()
	srv.SubmitArbError = fmt.Errorf("connection refused")
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
	defer conn.Close()

	client := pb.NewArbServiceClient(conn)
	_, err = client.SubmitArb(ctx, ProfitableTriangleArb())
	if err == nil {
		t.Fatal("expected error")
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got %v", err)
	}
	if st.Code() != codes.Unknown {
		t.Fatalf("expected code Unknown, got %v", st.Code())
	}
}

func TestMockArbServer_Check_Running(t *testing.T) {
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
	defer conn.Close()

	client := pb.NewHealthServiceClient(conn)
	resp, err := client.Check(ctx, &pb.HealthCheckRequest{})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !resp.Healthy {
		t.Fatal("expected healthy=true for RUNNING")
	}
	if resp.UptimeSeconds == 0 {
		t.Fatal("expected non-zero uptime")
	}
}

func TestMockArbServer_Check_Degraded(t *testing.T) {
	srv := NewMockArbServer()
	srv.HealthStatus = pb.SystemState_DEGRADED
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
	defer conn.Close()

	client := pb.NewHealthServiceClient(conn)
	resp, err := client.Check(ctx, &pb.HealthCheckRequest{})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !resp.Healthy {
		t.Fatal("expected healthy=true for DEGRADED")
	}
}

func TestMockArbServer_Check_Paused(t *testing.T) {
	srv := NewMockArbServer()
	srv.HealthStatus = pb.SystemState_PAUSED
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
	defer conn.Close()

	client := pb.NewHealthServiceClient(conn)
	resp, err := client.Check(ctx, &pb.HealthCheckRequest{})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if resp.Healthy {
		t.Fatal("expected healthy=false for PAUSED")
	}
}

func TestMockArbServer_Check_Halted(t *testing.T) {
	srv := NewMockArbServer()
	srv.HealthStatus = pb.SystemState_HALTED
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
	defer conn.Close()

	client := pb.NewHealthServiceClient(conn)
	resp, err := client.Check(ctx, &pb.HealthCheckRequest{})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if resp.Healthy {
		t.Fatal("expected healthy=false for HALTED")
	}
}

func TestMockArbServer_SetState(t *testing.T) {
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
	defer conn.Close()

	client := pb.NewControlServiceClient(conn)
	resp, err := client.SetState(ctx, &pb.SetStateRequest{State: pb.SystemState_PAUSED})
	if err != nil {
		t.Fatalf("SetState: %v", err)
	}
	if !resp.Success {
		t.Fatal("expected success")
	}
	if resp.PreviousState != pb.SystemState_RUNNING {
		t.Fatalf("expected previous state RUNNING, got %v", resp.PreviousState)
	}

	hclient := pb.NewHealthServiceClient(conn)
	hresp, err := hclient.Check(ctx, &pb.HealthCheckRequest{})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if hresp.Healthy {
		t.Fatal("expected unhealthy after PAUSED")
	}
}

func TestMockArbServer_SetState_Transitions(t *testing.T) {
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
	defer conn.Close()

	client := pb.NewControlServiceClient(conn)

	states := []pb.SystemState{
		pb.SystemState_DEGRADED,
		pb.SystemState_PAUSED,
		pb.SystemState_HALTED,
		pb.SystemState_RUNNING,
	}

	for i, state := range states {
		resp, err := client.SetState(ctx, &pb.SetStateRequest{State: state})
		if err != nil {
			t.Fatalf("SetState[%d]: %v", i, err)
		}
		if !resp.Success {
			t.Fatalf("SetState[%d]: expected success", i)
		}
	}
}

func TestMockArbServer_ReloadConfig(t *testing.T) {
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
	defer conn.Close()

	client := pb.NewControlServiceClient(conn)
	resp, err := client.ReloadConfig(ctx, &pb.ReloadConfigRequest{})
	if err != nil {
		t.Fatalf("ReloadConfig: %v", err)
	}
	if !resp.Success {
		t.Fatal("expected success")
	}
	if resp.PoolsLoaded != 100 {
		t.Fatalf("expected 100 pools, got %d", resp.PoolsLoaded)
	}
}

func TestMockArbServer_Addr_BeforeStart(t *testing.T) {
	srv := NewMockArbServer()
	if got := srv.Addr(); got != "" {
		t.Fatalf("expected empty addr before Start, got %q", got)
	}
}

func TestMockArbServer_Stop_BeforeStart(t *testing.T) {
	srv := NewMockArbServer()
	srv.Stop()
}

func TestMockArbServer_StreamArbs_NoFilter(t *testing.T) {
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
		t.Fatalf("expected 5 arbs with no filter, got %d", count)
	}
}

func TestMockArbServer_StreamArbs_Empty(t *testing.T) {
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
	defer conn.Close()

	client := pb.NewArbServiceClient(conn)
	stream, err := client.StreamArbs(ctx, &pb.StreamArbsRequest{MinProfitEth: 100})
	if err != nil {
		t.Fatalf("StreamArbs: %v", err)
	}
	_, err = stream.Recv()
	if err == nil {
		t.Fatal("expected EOF")
	}
}

func TestProfitWeiToFloat_Empty(t *testing.T) {
	if got := profitWeiToFloat([]byte{}); got != 0 {
		t.Fatalf("expected 0 for empty, got %v", got)
	}
}
