package main

import (
	"context"
	"errors"
	"net"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	aethergrpc "github.com/aether-arb/aether/internal/grpc"
	pb "github.com/aether-arb/aether/internal/pb"
)

type mockControl struct {
	pb.UnimplementedControlServiceServer
	lastState pb.SystemState
}

func (m *mockControl) SetState(_ context.Context, req *pb.SetStateRequest) (*pb.SetStateResponse, error) {
	m.lastState = req.State
	return &pb.SetStateResponse{Success: true, PreviousState: pb.SystemState_RUNNING}, nil
}

func dialTestEngineClient(t *testing.T, mock *mockControl) *aethergrpc.Client {
	t.Helper()
	lis := bufconn.Listen(1024 * 1024)
	srv := grpc.NewServer()
	pb.RegisterControlServiceServer(srv, mock)
	go srv.Serve(lis)
	t.Cleanup(func() { srv.Stop() })

	dialer := func(context.Context, string) (net.Conn, error) {
		return lis.Dial()
	}
	conn, err := grpc.DialContext(context.Background(), "bufnet",
		grpc.WithContextDialer(dialer),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })

	client, err := aethergrpc.NewClientFromConn(conn)
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func TestNewGRPCEngineAdapter_NilClient(t *testing.T) {
	t.Parallel()
	if newGRPCEngineAdapter(nil) != nil {
		t.Fatal("expected nil adapter for nil client")
	}
}

func TestGRPCEngineAdapter_NilReceiver(t *testing.T) {
	t.Parallel()
	var a *grpcEngineAdapter
	if err := a.SetEngineState(context.Background(), true); err != nil {
		t.Fatalf("nil adapter should no-op: %v", err)
	}
}

func TestGRPCEngineAdapter_NilClientField(t *testing.T) {
	t.Parallel()
	a := &grpcEngineAdapter{client: nil}
	if err := a.SetEngineState(context.Background(), false); err != nil {
		t.Fatalf("nil client field should no-op: %v", err)
	}
}

func TestGRPCEngineAdapter_SetEngineStatePaused(t *testing.T) {
	t.Parallel()
	mock := &mockControl{}
	client := dialTestEngineClient(t, mock)
	adapter := newGRPCEngineAdapter(client)
	if adapter == nil {
		t.Fatal("expected adapter")
	}
	if err := adapter.SetEngineState(context.Background(), true); err != nil {
		t.Fatalf("SetEngineState paused: %v", err)
	}
	if mock.lastState != pb.SystemState_PAUSED {
		t.Fatalf("state %v", mock.lastState)
	}
}

func TestGRPCEngineAdapter_SetEngineStateRunning(t *testing.T) {
	t.Parallel()
	mock := &mockControl{}
	client := dialTestEngineClient(t, mock)
	adapter := newGRPCEngineAdapter(client)
	if err := adapter.SetEngineState(context.Background(), false); err != nil {
		t.Fatalf("SetEngineState running: %v", err)
	}
	if mock.lastState != pb.SystemState_RUNNING {
		t.Fatalf("state %v", mock.lastState)
	}
}

type stubEngineCtrl struct {
	paused bool
	err    error
}

func (s *stubEngineCtrl) SetEngineState(_ context.Context, paused bool) error {
	if s.err != nil {
		return s.err
	}
	s.paused = paused
	return nil
}

func TestEngineCtrlStub_RecordsState(t *testing.T) {
	t.Parallel()
	stub := &stubEngineCtrl{}
	if err := stub.SetEngineState(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if !stub.paused {
		t.Fatal("expected paused")
	}
	stub.err = errors.New("rpc down")
	if err := stub.SetEngineState(context.Background(), false); err == nil {
		t.Fatal("expected error")
	}
}
