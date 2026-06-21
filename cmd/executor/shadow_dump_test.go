package main

import (
	"math/big"
	"os"
	"path/filepath"
	"testing"

	pb "github.com/aether-arb/aether/internal/pb"
)

func TestDumpShadowBundle_WritesJSON(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("AETHER_SHADOW_DUMP_DIR", dir)

	arb := newValidArb("arb/shadow!001", 0.01, 5.0)
	arb.Hops[0].TokenIn = []byte{0xC0, 0x2a, 0xaA, 0x39, 0xb2, 0x23, 0xFE, 0x8D, 0x0A, 0x0e, 0x5C, 0x4F, 0x27, 0xeA, 0xD9, 0x08, 0x3C, 0x75, 0x6C, 0xc2}
	arb.Hops[0].TokenOut = []byte{0xA0, 0xb8, 0x69, 0x91, 0xc6, 0x21, 0x8b, 0x36, 0xc1, 0xd1, 0x9D, 0x4a, 0x2e, 0x9E, 0xb0, 0xcE, 0x36, 0x06, 0xeB, 0x48}

	bundle := &Bundle{
		BlockNumber: 18000001,
		RawTxs:      [][]byte{{0xab, 0xcd}},
	}

	if err := dumpShadowBundle(arb, bundle, 0.01, 30.0, 90.0); err != nil {
		t.Fatalf("dumpShadowBundle: %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 file, got %d", len(entries))
	}
	if filepath.Ext(entries[0].Name()) != ".json" {
		t.Fatalf("unexpected file: %s", entries[0].Name())
	}
}

func TestDumpMempoolShadowBundle_WithGates(t *testing.T) {
	dir := t.TempDir()
	old := mempoolShadowSessionDir
	mempoolShadowSessionDir = func() string { return dir }
	defer func() { mempoolShadowSessionDir = old }()

	arb := newValidArb("mempool-arb-001", 0.02, 3.0)
	arb.Source = pb.ArbSource_MEMPOOL_BACKRUN
	bundle := &Bundle{
		BlockNumber:     19000000,
		VictimTxHashHex: "0xdead",
		RawTxs:          [][]byte{{0x01, 0x02}},
	}
	gas := GasFees{
		BaseFee:        big.NewInt(30_000_000_000),
		MaxPriorityFee: big.NewInt(2_000_000_000),
		MaxFeePerGas:   big.NewInt(35_000_000_000),
	}
	decision := MempoolPreflightResult{
		Gates: []MempoolGateTrace{{Gate: "profit", Passed: true, Value: "ok"}},
	}

	if err := dumpMempoolShadowBundle(arb, bundle, gas, 50.0, decision); err != nil {
		t.Fatalf("dumpMempoolShadowBundle: %v", err)
	}
}

func TestIsShadowMode_Table(t *testing.T) {
	tests := []struct {
		env  string
		want bool
	}{
		{"", false},
		{"true", true},
		{"1", true},
		{"false", false},
		{"garbage", false},
	}
	for _, tc := range tests {
		if tc.env == "" {
			os.Unsetenv("AETHER_SHADOW")
		} else {
			t.Setenv("AETHER_SHADOW", tc.env)
		}
		if got := isShadowMode(); got != tc.want {
			t.Fatalf("isShadowMode(%q)=%v want %v", tc.env, got, tc.want)
		}
	}
}
