package main

import (
	"strings"
	"testing"
)

func TestFilterPools_KeepsWethPairs(t *testing.T) {
	pools := []PoolEntry{
		{Token0: WETH, Token1: USDC},
		{Token0: USDT, Token1: DAI},
	}
	filtered := FilterPools(pools)
	if len(filtered) != 1 {
		t.Fatalf("len=%d", len(filtered))
	}
}

func TestDiscoverPools_ReturnsWellKnownWithoutRPC(t *testing.T) {
	pd := NewPoolDiscoverer("simulated", 50)
	pools, err := pd.DiscoverPools()
	if err != nil {
		t.Fatal(err)
	}
	if len(pools) == 0 {
		t.Fatal("expected static pools")
	}
}

func TestWellKnownV2Pools_Uniswap(t *testing.T) {
	pools := wellKnownV2Pools("uniswap_v2", 30)
	if len(pools) < 3 {
		t.Fatalf("len=%d", len(pools))
	}
	if pools[0].Protocol != "uniswap_v2" {
		t.Fatalf("protocol=%s", pools[0].Protocol)
	}
}

func TestWellKnownV3Pools_HasFeeTiers(t *testing.T) {
	pools := wellKnownV3Pools()
	for _, p := range pools {
		if p.TickSpacing <= 0 {
			t.Fatalf("tick spacing for %s", p.Address)
		}
	}
}

func TestFormatTOML_ContainsProtocol(t *testing.T) {
	toml := FormatTOML([]PoolEntry{{
		Protocol: "uniswap_v2",
		Address:  "0xabc",
		Token0:   WETH,
		Token1:   USDC,
		FeeBps:   30,
		Tier:     "hot",
	}})
	if !strings.Contains(toml, "uniswap_v2") {
		t.Fatal("missing protocol")
	}
}

func TestContainsTargetToken_Weth(t *testing.T) {
	if !containsTargetToken(WETH) {
		t.Fatal("WETH should match")
	}
}

func TestContainsTargetToken_Other(t *testing.T) {
	if containsTargetToken("0x0000000000000000000000000000000000000001") {
		t.Fatal("random address should not match")
	}
}

func TestDiscoverPools_RespectsLimit(t *testing.T) {
	pd := NewPoolDiscoverer("simulated", 2)
	pools, err := pd.DiscoverPools()
	if err != nil {
		t.Fatal(err)
	}
	if len(pools) > 2 {
		t.Fatalf("limit not applied: %d", len(pools))
	}
}

func TestFilterPools_UsdcPair(t *testing.T) {
	pools := FilterPools([]PoolEntry{{Token0: USDC, Token1: DAI}})
	if len(pools) != 1 {
		t.Fatalf("len=%d", len(pools))
	}
}
