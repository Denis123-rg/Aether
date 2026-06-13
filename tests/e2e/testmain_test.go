package e2e

import (
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	// Mock gRPC pipeline tests dial TCP loopback; production blocks insecure TCP.
	_ = os.Setenv("ALLOW_INSECURE_TCP", "true")
	os.Exit(m.Run())
}
