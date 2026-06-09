package grpc

import (
	"context"
	"testing"

	pb "github.com/aether-arb/aether/internal/pb"
	"github.com/aether-arb/aether/internal/testutil"
)

func TestSetState_CallsControlService(t *testing.T) {
	srv := testutil.NewMockArbServer()
	dialer, cleanup, err := srv.StartBufconn(1024 * 1024)
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

	resp, err := client.SetState(context.Background(), pb.SystemState_PAUSED, "test")
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Success || resp.PreviousState != pb.SystemState_RUNNING {
		t.Fatalf("resp %+v", resp)
	}
}

func TestReloadConfig_ReturnsPoolsLoaded(t *testing.T) {
	srv := testutil.NewMockArbServer()
	dialer, cleanup, err := srv.StartBufconn(1024 * 1024)
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

	resp, err := client.ReloadConfig(context.Background(), "config/pools.toml")
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Success || resp.PoolsLoaded != 100 {
		t.Fatalf("resp %+v", resp)
	}
}

func TestSetState_InvalidConn(t *testing.T) {
	srv := testutil.NewMockArbServer()
	dialer, cleanup, err := srv.StartBufconn(1024)
	if err != nil {
		t.Fatal(err)
	}
	cleanup() // stop server before RPC

	conn, err := srv.DialBufconn(context.Background(), dialer)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	client, _ := NewClientFromConn(conn)
	_, err = client.SetState(context.Background(), pb.SystemState_HALTED, "")
	if err == nil {
		t.Fatal("expected error on dead connection")
	}
}

func TestReloadConfig_EmptyPathStillSucceeds(t *testing.T) {
	srv := testutil.NewMockArbServer()
	dialer, cleanup, err := srv.StartBufconn(1024 * 1024)
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
	resp, err := client.ReloadConfig(context.Background(), "")
	if err != nil || !resp.Success {
		t.Fatalf("err=%v resp=%+v", err, resp)
	}
}
