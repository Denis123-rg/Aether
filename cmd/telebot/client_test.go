package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestAdminClient_SendsBearerWhenTokenConfigured(t *testing.T) {
	const wantToken = "secret-admin-token"
	os.Setenv("AETHER_ADMIN_TOKEN", wantToken)
	defer os.Unsetenv("AETHER_ADMIN_TOKEN")

	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := NewAdminClient(srv.URL + "/metrics/json")
	if err := client.Pause(context.Background()); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	if gotAuth != "Bearer "+wantToken {
		t.Fatalf("Authorization = %q, want Bearer %q", gotAuth, wantToken)
	}
}

func TestAdminClient_NoBearerWhenTokenEmpty(t *testing.T) {
	os.Unsetenv("AETHER_ADMIN_TOKEN")

	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := NewAdminClient(srv.URL + "/metrics/json")
	if err := client.Resume(context.Background()); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if gotAuth != "" {
		t.Fatalf("expected no Authorization header, got %q", gotAuth)
	}
}

func TestAdminClient_Handles401(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, "unauthorized")
	}))
	defer srv.Close()

	client := NewAdminClient(srv.URL + "/metrics/json")
	err := client.Pause(context.Background())
	if err == nil {
		t.Fatal("expected error on 401")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Fatalf("error should mention 401: %v", err)
	}
}

func TestAdminClient_SetMinProfit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "set_min_profit") {
			t.Fatalf("path: %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := NewAdminClient(srv.URL + "/metrics/json")
	if err := client.SetMinProfit(context.Background(), 0.005); err != nil {
		t.Fatal(err)
	}
}

func TestNewMetricsClient_FetchSnapshot(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"pnl_today_wei":"0","pnl_total_wei":"0","win_rate":0,"last_bundle_profit_wei":"0","last_bundle_gas":0,"last_builder":"","breaker_open":false,"signer_healthy":true,"rpc_healthy":true,"top_pools":[]}`))
	}))
	defer srv.Close()

	client := NewMetricsClient(srv.URL)
	snap, err := client.FetchSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !snap.ExecutorReachable {
		t.Fatal("expected reachable")
	}
}

func TestNewAdminClient_StripsMetricsPath(t *testing.T) {
	client := NewAdminClient("http://localhost:8080/metrics/json")
	if !strings.HasSuffix(client.baseHost, "8080") {
		t.Fatalf("baseHost=%q", client.baseHost)
	}
}
