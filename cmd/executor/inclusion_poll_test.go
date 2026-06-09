package main

import (
	"encoding/json"
	"testing"
)

func TestParseBundleStatsBlockNumber(t *testing.T) {
	raw := json.RawMessage(`{"blockNumber":"0x14f5b3c","isSentToMiners":true}`)
	included, block := parseBundleStats(raw)
	if !included {
		t.Fatal("expected included")
	}
	if block != 21977916 {
		t.Fatalf("block=%d want 21977916", block)
	}
}

func TestParseBundleStatsSentToMiners(t *testing.T) {
	raw := json.RawMessage(`{"isSimulated":true,"isSentToMiners":true}`)
	included, block := parseBundleStats(raw)
	if included {
		t.Fatal("isSentToMiners without blockNumber must not count as included")
	}
	if block != 0 {
		t.Fatalf("block=%d want 0", block)
	}
}

func TestParseBundleStatsMiss(t *testing.T) {
	raw := json.RawMessage(`{"isSimulated":true}`)
	included, _ := parseBundleStats(raw)
	if included {
		t.Fatal("expected not included")
	}
}
