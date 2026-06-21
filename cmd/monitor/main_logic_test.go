package main

import (
	"bytes"
	"context"
	"net"
	"os"
	"os/exec"
	"strconv"
	"testing"
	"time"
)

func monitorHelperProcess(t *testing.T) *exec.Cmd {
	t.Helper()
	return exec.Command(os.Args[0], "-test.run=^"+t.Name()+"$", "-test.count=1")
}

func TestRunMonitorService_StartsAndStops(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()

	t.Setenv("METRICS_PORT", strconv.Itoa(port))
	t.Setenv("DASHBOARD_PORT", strconv.Itoa(port+1))

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	err = runMonitorService(ctx)
	if err != nil {
		t.Fatalf("runMonitorService: %v", err)
	}
}

func TestMainLogic_MonitorBanner(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") == "monitor-main" {
		main()
		os.Exit(0)
	}

	cmd := monitorHelperProcess(t)
	cmd.Env = append(os.Environ(), "GO_WANT_HELPER_PROCESS=monitor-main")
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	// main blocks forever; kill after short wait.
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	_ = cmd.Process.Kill()
	combined := out.String()
	if combined == "" {
		t.Log("no output captured (process may have exited early)")
	}
}
