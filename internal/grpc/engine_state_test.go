package grpc

import (
	"context"
	"net"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

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

func TestSetEngineState_Paused(t *testing.T) {
	mock := &mockControl{}
	lis := bufconn.Listen(1024 * 1024)
	srv := grpc.NewServer()
	pb.RegisterControlServiceServer(srv, mock)
	go srv.Serve(lis)

	dialer := func(context.Context, string) (net.Conn, error) {
		return lis.Dial()
	}
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(dialer),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	defer srv.Stop()

	client, err := NewClientFromConn(conn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.SetEngineState(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if mock.lastState != pb.SystemState_PAUSED {
		t.Fatalf("state %v", mock.lastState)
	}
}

func TestSetEngineState_Running(t *testing.T) {
	mock := &mockControl{lastState: pb.SystemState_PAUSED}
	lis := bufconn.Listen(1024 * 1024)
	srv := grpc.NewServer()
	pb.RegisterControlServiceServer(srv, mock)
	go srv.Serve(lis)

	dialer := func(context.Context, string) (net.Conn, error) {
		return lis.Dial()
	}
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(dialer),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	defer srv.Stop()

	client, _ := NewClientFromConn(conn)
	if _, err := client.SetEngineState(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if mock.lastState != pb.SystemState_RUNNING {
		t.Fatalf("state %v", mock.lastState)
	}
}
