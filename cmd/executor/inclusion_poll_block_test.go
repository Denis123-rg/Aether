package main

import (
	"encoding/json"
	"testing"
)

func TestParseBundleStats_OnlyBlockNumberCountsAsIncluded(t *testing.T) {
	raw := json.RawMessage(`{"isHighPriority":true,"isSentToMiners":true}`)
	included, block := parseBundleStats(raw)
	if included {
		t.Fatalf("expected not included without block number, got block=%d", block)
	}
}

func TestParseBundleStats_BlockNumberIncluded(t *testing.T) {
	raw := json.RawMessage(`{"blockNumber":"0x64","isSentToMiners":true}`)
	included, block := parseBundleStats(raw)
	if !included || block != 100 {
		t.Fatalf("included=%v block=%d", included, block)
	}
}

func TestParseBundleStats_SentToMinersWithoutBlockNotIncluded(t *testing.T) {
	raw := json.RawMessage(`{"isSentToMiners":true,"isHighPriority":false}`)
	included, _ := parseBundleStats(raw)
	if included {
		t.Fatal("isSentToMiners alone must not count as inclusion")
	}
}

func TestParseBundleStats_HighPriorityWithoutBlockNotIncluded(t *testing.T) {
	raw := json.RawMessage(`{"isHighPriority":true}`)
	included, _ := parseBundleStats(raw)
	if included {
		t.Fatal("isHighPriority alone must not count as inclusion")
	}
}

func TestParseBundleStats_ZeroBlockNotIncluded(t *testing.T) {
	raw := json.RawMessage(`{"blockNumber":"0x0","isSentToMiners":true}`)
	included, _ := parseBundleStats(raw)
	if included {
		t.Fatal("0x0 block must not count as inclusion")
	}
}

func TestParseBundleStats_InvalidJSON(t *testing.T) {
	raw := json.RawMessage(`not-json`)
	included, block := parseBundleStats(raw)
	if included || block != 0 {
		t.Fatalf("invalid json: included=%v block=%d", included, block)
	}
}

func TestParseBundleStats_EmptyObject(t *testing.T) {
	raw := json.RawMessage(`{}`)
	included, block := parseBundleStats(raw)
	if included || block != 0 {
		t.Fatalf("empty: included=%v block=%d", included, block)
	}
}

func TestParseBundleStats_LargeBlockHex(t *testing.T) {
	raw := json.RawMessage(`{"blockNumber":"0x12a05f200"}`)
	included, block := parseBundleStats(raw)
	if !included || block != 5_000_000_000 {
		t.Fatalf("included=%v block=%d", included, block)
	}
}
