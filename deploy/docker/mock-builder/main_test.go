package main

import (
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	os.Exit(m.Run())
}

func TestMockBuilderEndpoints(t *testing.T) {
	if os.Getenv("MOCK_BUILDER_HELPER") == "1" {
		main()
		return
	}

	cmd := exec.Command(os.Args[0], "-test.run=^"+t.Name()+"$")
	cmd.Env = append(os.Environ(), "MOCK_BUILDER_HELPER=1")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper: %v", err)
	}

	// Wait for the server to come up, then exercise both endpoints.
	var lastErr error
	for i := 0; i < 50; i++ {
		time.Sleep(50 * time.Millisecond)
		resp, err := http.Get("http://127.0.0.1:18545/health")
		if err != nil {
			lastErr = err
			continue
		}
		_ = resp.Body.Close()
		break
	}
	if lastErr != nil {
		_ = cmd.Process.Kill()
		t.Fatalf("server did not start: %v", lastErr)
	}

	resp, err := http.Get("http://127.0.0.1:18545/health")
	if err != nil {
		_ = cmd.Process.Kill()
		t.Fatalf("health: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if string(body) != `{"status":"ok"}` {
		t.Fatalf("unexpected health body: %s", body)
	}

	resp, err = http.Post("http://127.0.0.1:18545/", "application/json", nil)
	if err != nil {
		_ = cmd.Process.Kill()
		t.Fatalf("bundle: %v", err)
	}
	body, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if strings.TrimSpace(string(body)) != `{"bundleHash":"0xe2e"}` {
		t.Fatalf("unexpected bundle body: %s", body)
	}

	_ = cmd.Process.Kill()
	_, _ = cmd.Process.Wait()
}
