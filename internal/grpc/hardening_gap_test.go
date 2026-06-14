package grpc

import (
	"context"
	"testing"

	pb "github.com/aether-arb/aether/internal/pb"
	"github.com/aether-arb/aether/internal/testutil"
)

func TestControlService_ReturnsStub(t *testing.T) {
	srv := testutil.NewMockArbServer()
	dialer, cleanup, err := srv.StartBufconn(0)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	conn, err := srv.DialBufconn(context.Background(), dialer)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	client, err := NewClientFromConn(conn)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	ctrl := client.ControlService()
	if ctrl == nil {
		t.Fatal("nil control service")
	}
	resp, err := ctrl.SetState(context.Background(), &pb.SetStateRequest{State: pb.SystemState_PAUSED})
	if err != nil || !resp.Success {
		t.Fatalf("SetState: err=%v resp=%+v", err, resp)
	}
}

func FuzzValidateDialTarget(f *testing.F) {
	f.Add("localhost:50051")
	f.Add("unix:///var/run/aether.sock")
	f.Add("")
	f.Add("://bad")
	f.Fuzz(func(t *testing.T, addr string) {
		_ = validateDialTarget(addr)
	})
}
