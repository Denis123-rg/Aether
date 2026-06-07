package main

import (
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"testing"
)

func helperProcess(t *testing.T) *exec.Cmd {
	t.Helper()
	return exec.Command(os.Args[0], "-test.run=^"+t.Name()+"$")
}

func TestGenerateBundleID_RandFailureExits(t *testing.T) {
	old := bundleIDRand
	defer func() { bundleIDRand = old }()
	bundleIDRand = func([]byte) (int, error) { return 0, errors.New("rand broke") }

	if os.Getenv("GO_WANT_HELPER_PROCESS") == "1" {
		GenerateBundleID()
		os.Exit(0)
	}

	cmd := helperProcess(t)
	cmd.Env = append(os.Environ(), "GO_WANT_HELPER_PROCESS=1")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	state, err := cmd.Process.Wait()
	if err != nil {
		t.Fatalf("wait: %v", err)
	}
	if state.Success() {
		t.Fatal("expected non-zero exit on rand failure")
	}
}

func TestGenerateBundleID_ProducesHex(t *testing.T) {
	id := GenerateBundleID()
	if len(id) != 32 {
		t.Fatalf("len = %d", len(id))
	}
	if _, err := hex.DecodeString(id); err != nil {
		t.Fatalf("not hex: %v", err)
	}
}
