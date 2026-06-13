package integration

import (
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	// Scenario tests dial mock gRPC over TCP loopback; production blocks insecure TCP.
	_ = os.Setenv("ALLOW_INSECURE_TCP", "true")
	os.Exit(m.Run())
}
