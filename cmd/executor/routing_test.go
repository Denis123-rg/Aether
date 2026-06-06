package main

import (
	"context"
	"testing"
)

func TestResolveRoutingMode(t *testing.T) {
	tests := []struct {
		mode, want string
		fanOut     bool
	}{
		{"select", "select", true},
		{"fanout", "fanout", false},
		{"", "fanout", true},
		{"", "select", false},
		{"SINGLE", "select", true},
	}
	for _, tc := range tests {
		if got := resolveRoutingMode(tc.mode, tc.fanOut); got != tc.want {
			t.Fatalf("resolveRoutingMode(%q, %v) = %q, want %q", tc.mode, tc.fanOut, got, tc.want)
		}
	}
}

func TestSubmitToBuilderSelectMode(t *testing.T) {
	builders := []BuilderConfig{
		{Name: "flashbots", URL: "http://127.0.0.1:1", Enabled: true, TimeoutMs: 100},
		{Name: "titan", URL: "http://127.0.0.1:2", Enabled: true, TimeoutMs: 100},
	}
	sub, err := NewSubmitter(builders, "")
	if err != nil {
		t.Fatalf("NewSubmitter: %v", err)
	}
	sub.submitFn = func(_ context.Context, b BuilderConfig, _ *Bundle) SubmissionResult {
		return SubmissionResult{Builder: b.Name, Success: true, BundleHash: "0xabc"}
	}

	results := sub.SubmitToBuilder(context.Background(), &Bundle{RawTxs: [][]byte{{1}}}, "titan")
	if len(results) != 1 {
		t.Fatalf("results len = %d, want 1", len(results))
	}
	if results[0].Builder != "titan" || !results[0].Success {
		t.Fatalf("result: %+v", results[0])
	}

	disabled := sub.SubmitToBuilder(context.Background(), &Bundle{RawTxs: [][]byte{{1}}}, "missing")
	if len(disabled) != 1 || disabled[0].Success {
		t.Fatalf("missing builder: %+v", disabled)
	}
}
