package grpc

import (
	"context"
	"io"
	"testing"
	"time"

	pb "github.com/aether-arb/aether/internal/pb"
	"github.com/aether-arb/aether/internal/testutil"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestDial_InvalidAddress(t *testing.T) {
	t.Parallel()
	_, err := Dial("not-a-valid-grpc-address://")
	if err == nil {
		t.Fatal("expected error for invalid address scheme")
	}
}

func TestValidateDialTarget(t *testing.T) {
	t.Parallel()
	cases := []struct {
		addr    string
		wantErr bool
	}{
		{addr: "", wantErr: true},
		{addr: "   ", wantErr: true},
		{addr: "localhost:50051", wantErr: false},
		{addr: "[::1]:50051", wantErr: false},
		{addr: "unix:///var/run/aether.sock", wantErr: false},
		{addr: "unix://", wantErr: true},
		{addr: "http://localhost:1", wantErr: true},
		{addr: "localhost", wantErr: true},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.addr, func(t *testing.T) {
			t.Parallel()
			err := validateDialTarget(tc.addr)
			if tc.wantErr && err == nil {
				t.Fatal("expected error")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestDial_CloseAndServiceStubs(t *testing.T) {
	t.Parallel()
	srv := testutil.NewMockArbServer()
	addr, err := srv.Start()
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Stop()

	client, err := Dial(addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	if client.ArbService() == nil || client.HealthService() == nil || client.ControlService() == nil {
		t.Fatal("service stubs must be non-nil")
	}
	if err := client.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

func TestCheckHealth_Healthy(t *testing.T) {
	t.Parallel()
	srv := testutil.NewMockArbServer()
	srv.HealthStatus = pb.SystemState_RUNNING
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
		t.Fatal("expected healthy")
	}
}

func TestCheckHealth_DeadlineExceeded(t *testing.T) {
	t.Parallel()
	srv := testutil.NewMockArbServer()
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

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = client.CheckHealth(ctx)
	if err == nil {
		t.Fatal("expected error on cancelled context")
	}
}

func TestStreamArbs_ReceivesConfiguredArbs(t *testing.T) {
	t.Parallel()
	srv := testutil.NewMockArbServer()
	srv.SetArbs([]*pb.ValidatedArb{testutil.ProfitableTriangleArb()})
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
	stream, err := client.StreamArbs(ctx, 0.001)
	if err != nil {
		t.Fatal(err)
	}
	arb, err := stream.Recv()
	if err != nil {
		t.Fatal(err)
	}
	if arb.Id != "arb-triangle-001" {
		t.Fatalf("arb id = %s", arb.Id)
	}
}

func TestStreamArbs_ContextCancel(t *testing.T) {
	t.Parallel()
	srv := testutil.NewMockArbServer()
	srv.SetArbs(testutil.BatchArbs())
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

	ctx, cancel := context.WithCancel(context.Background())
	stream, err := client.StreamArbs(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	_, err = stream.Recv()
	if err == nil {
		t.Fatal("expected error after context cancel")
	}
	if status.Code(err) != codes.Canceled && err != io.EOF && !errorsIsContextCanceled(err) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func errorsIsContextCanceled(err error) bool {
	return err == context.Canceled || status.Code(err) == codes.Canceled
}
