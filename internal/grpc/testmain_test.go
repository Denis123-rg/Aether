package grpc

import (
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	// Unit tests dial bufconn over TCP loopback; production blocks insecure TCP.
	_ = os.Setenv("ALLOW_INSECURE_TCP", "true")
	os.Exit(m.Run())
}
