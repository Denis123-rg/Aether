package grpc

import (
	"context"
	"testing"

	"github.com/aether-arb/aether/internal/testutil"
)

func TestNewClientFromConn_Table(t *testing.T) {
	t.Parallel()
	t.Run("nil conn", func(t *testing.T) {
		t.Parallel()
		_, err := NewClientFromConn(nil)
		if err == nil {
			t.Fatal("expected error for nil conn")
		}
	})
	t.Run("valid conn", func(t *testing.T) {
		t.Parallel()
		srv := testutil.NewMockArbServer()
		dialer, cleanup, err := srv.StartBufconn(0)
		if err != nil {
			t.Fatal(err)
		}
		defer cleanup()
		ctx := context.Background()
		conn, err := srv.DialBufconn(ctx, dialer)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		client, err := NewClientFromConn(conn)
		if err != nil {
			t.Fatalf("NewClientFromConn: %v", err)
		}
		defer client.Close()
		if client.ArbService() == nil {
			t.Fatal("nil arb service")
		}
	})
}

func TestDial_UnixTarget(t *testing.T) {
	t.Parallel()
	client, err := Dial("unix:///tmp/aether-test-nonexistent.sock")
	if err != nil {
		t.Fatalf("Dial unix: %v", err)
	}
	defer client.Close()
	if client.HealthService() == nil {
		t.Fatal("nil health service")
	}
}

