package grpc

import (
	"context"
	"testing"
	"time"

	pb "github.com/aether-arb/aether/internal/pb"
	"github.com/aether-arb/aether/internal/testutil"
)

func TestDial_RejectsEmptyTarget(t *testing.T) {
	t.Parallel()
	_, err := Dial("   ")
	if err == nil {
		t.Fatal("expected error for whitespace-only address")
	}
}

func TestDial_RejectsHostWithoutPort(t *testing.T) {
	t.Parallel()
	_, err := Dial("localhost")
	if err == nil {
		t.Fatal("expected error for host without port")
	}
}

func TestDial_RejectsHTTPScheme(t *testing.T) {
	t.Parallel()
	_, err := Dial("http://127.0.0.1:50051")
	if err == nil {
		t.Fatal("expected error for http scheme")
	}
}

func TestCheckHealth_DegradedStateStillHealthy(t *testing.T) {
	t.Parallel()
	srv := testutil.NewMockArbServer()
	srv.HealthStatus = pb.SystemState_DEGRADED
	addr, err := srv.Start()
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Stop()

	client, err := Dial(addr)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resp, err := client.CheckHealth(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Healthy {
		t.Fatal("DEGRADED is still operational and must report healthy")
	}
	if resp.GetStatus() != pb.SystemState_DEGRADED.String() {
		t.Fatalf("status = %q", resp.GetStatus())
	}
}

func TestCheckHealth_HaltedUnhealthy(t *testing.T) {
	t.Parallel()
	srv := testutil.NewMockArbServer()
	srv.HealthStatus = pb.SystemState_HALTED
	addr, err := srv.Start()
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Stop()

	client, err := Dial(addr)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resp, err := client.CheckHealth(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Healthy {
		t.Fatal("HALTED must not report healthy")
	}
}

func TestStreamArbs_MinProfitFilter(t *testing.T) {
	t.Parallel()
	srv := testutil.NewMockArbServer()
	srv.SetArbs([]*pb.ValidatedArb{
		testutil.ProfitableTriangleArb(),
	})
	addr, err := srv.Start()
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Stop()

	client, err := Dial(addr)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// Min profit above the fixture's net profit should yield no arbs.
	stream, err := client.StreamArbs(ctx, 999.0)
	if err != nil {
		t.Fatal(err)
	}
	ctx2, cancel2 := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel2()
	done := make(chan error, 1)
	go func() {
		_, recvErr := stream.Recv()
		done <- recvErr
	}()
	select {
	case recvErr := <-done:
		if recvErr == nil {
			t.Fatal("expected no arb above min profit threshold")
		}
	case <-ctx2.Done():
		// Timeout waiting for arb is acceptable — mock may not send any.
	}
}

func TestValidateDialTarget_IPv6Literal(t *testing.T) {
	t.Parallel()
	if err := validateDialTarget("[::1]:50051"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateDialTarget_UnixPath(t *testing.T) {
	t.Parallel()
	if err := validateDialTarget("unix:///var/run/aether.sock"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
