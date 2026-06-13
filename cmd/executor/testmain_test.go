package main

import (
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	_ = os.Setenv("ALLOW_INSECURE_TCP", "true")
	os.Exit(m.Run())
}
