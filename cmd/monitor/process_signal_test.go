package main

import (
	"context"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"testing"
	"time"
)

// monitorServiceHelperProcess detects helper env and runs runMonitorService
// until context is cancelled or signal received.
func monitorServiceHelperProcess(t *testing.T) bool {
	if os.Getenv("GO_TEST_MONITOR_SVC_HELPER") != "1" {
		return false
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Wait for SIGUSR1 to signal that the service is up and signal.Notify is ready.
	sigReady := make(chan os.Signal, 1)
	signal.Notify(sigReady, syscall.SIGUSR1)
	defer signal.Stop(sigReady)
	<-sigReady

	// Now run the service — it will block until SIGINT.
	_ = runMonitorService(ctx)
	return true
}

// TestRunMonitorService_Subprocess_SignalShutdown verifies the signal case
// in runMonitorService by sending SIGINT to a subprocess.
func TestRunMonitorService_Subprocess_SignalShutdown(t *testing.T) {
	if monitorServiceHelperProcess(t) {
		return
	}

	cmd := exec.Command(os.Args[0], "-test.run=^TestRunMonitorService_Subprocess_SignalShutdown$", "-test.count=1")
	cmd.Env = append(os.Environ(),
		"GO_TEST_MONITOR_SVC_HELPER=1",
		"METRICS_PORT=0",
		"DASHBOARD_PORT=0",
	)
	cmd.Dir = t.TempDir()

	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}

	// Give the subprocess time to start and register signal.Notify.
	time.Sleep(200 * time.Millisecond)

	// Send SIGUSR1 to confirm the helper is ready, then SIGINT to trigger shutdown.
	_ = cmd.Process.Signal(syscall.SIGUSR1)
	time.Sleep(100 * time.Millisecond)

	err := cmd.Process.Signal(syscall.SIGINT)
	if err != nil {
		t.Fatalf("failed to send SIGINT: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("subprocess did not exit after SIGINT")
	}
}
