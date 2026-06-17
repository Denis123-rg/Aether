package testutil

import (
	"math/big"
	"testing"

	pb "github.com/aether-arb/aether/internal/pb"
)

func TestETHToWeiBytes(t *testing.T) {
	got := ETHToWeiBytes(1.0)
	want := new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil).Bytes()
	if len(got) == 0 {
		t.Fatal("expected non-empty bytes")
	}
	if new(big.Int).SetBytes(got).Cmp(new(big.Int).SetBytes(want)) != 0 {
		t.Fatalf("1 ETH mismatch: got %x, want %x", got, want)
	}
}

func TestProfitableTriangleArb(t *testing.T) {
	arb := ProfitableTriangleArb()
	assertArb(t, arb, "arb-triangle-001", 3)
}

func TestProfitable2HopArb(t *testing.T) {
	arb := Profitable2HopArb()
	assertArb(t, arb, "arb-2hop-001", 2)
}

func TestMarginalProfitArb(t *testing.T) {
	arb := MarginalProfitArb()
	assertArb(t, arb, "arb-marginal-001", 1)
}

func TestLowProfitArb(t *testing.T) {
	arb := LowProfitArb()
	assertArb(t, arb, "arb-lowprofit-001", 1)
}

func TestLargeTradeArb(t *testing.T) {
	arb := LargeTradeArb()
	assertArb(t, arb, "arb-largetrade-001", 1)
}

func TestBatchArbs(t *testing.T) {
	arbs := BatchArbs()
	if len(arbs) != 5 {
		t.Fatalf("expected 5 arbs, got %d", len(arbs))
	}
	for _, arb := range arbs {
		if arb.Id == "" {
			t.Fatal("expected non-empty arb id")
		}
	}
}

func assertArb(t *testing.T, arb *pb.ValidatedArb, wantID string, wantHops int) {
	t.Helper()
	if arb == nil {
		t.Fatal("expected non-nil arb")
	}
	if arb.Id != wantID {
		t.Fatalf("id mismatch: got %q, want %q", arb.Id, wantID)
	}
	if len(arb.Hops) != wantHops {
		t.Fatalf("expected %d hops, got %d", wantHops, len(arb.Hops))
	}
	if arb.TotalGas == 0 {
		t.Fatal("expected non-zero total gas")
	}
	if len(arb.NetProfitWei) == 0 {
		t.Fatal("expected non-empty net profit")
	}
}
