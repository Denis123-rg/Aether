package main

import (
	"bytes"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func riskHelperProcess(t *testing.T) *exec.Cmd {
	t.Helper()
	return exec.Command(os.Args[0], "-test.run=^"+t.Name()+"$", "-test.count=1")
}

// TestMainLogic exercises main() via a subprocess so coverage includes the
// entrypoint without calling os.Exit in the test runner.
func TestMainLogic(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") == "1" {
		main()
		os.Exit(0)
	}

	cmd := riskHelperProcess(t)
	cmd.Env = append(os.Environ(), "GO_WANT_HELPER_PROCESS=1")
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("main subprocess: %v\n%s", err, out.String())
	}
	combined := out.String()
	if !strings.Contains(combined, "aether-risk") {
		t.Fatalf("expected startup banner in output, got: %q", combined)
	}
	if !strings.Contains(combined, "Risk manager initialized") {
		t.Fatalf("expected init log in output, got: %q", combined)
	}
}
