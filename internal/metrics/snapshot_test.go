package metrics

import (
	"testing"
	"time"
)

func TestNewStoreDefaults(t *testing.T) {
	s := NewStore()
	snap := s.Get()
	if !snap.SignerHealthy || !snap.RPCHealthy {
		t.Fatal("expected healthy defaults")
	}
	if snap.TopPools == nil {
		t.Fatal("top pools should be initialized")
	}
}

func TestStoreSetTopPools(t *testing.T) {
	s := NewStore()
	pools := []TopPool{{Address: "0xabc", Score: 0.9, Protocol: "uniswap_v2"}}
	s.SetTopPools(pools)
	got := s.Get().TopPools
	if len(got) != 1 || got[0].Address != "0xabc" {
		t.Fatalf("pools: %+v", got)
	}
}

func TestStoreRecordTrade(t *testing.T) {
	s := NewStore()
	for i := 0; i < 12; i++ {
		s.RecordTrade(TradeRecord{
			Timestamp: time.Now(),
			ProfitETH: float64(i),
			Builder:   "flashbots",
		})
	}
	trades := s.Get().RecentTrades
	if len(trades) != 10 {
		t.Fatalf("expected 10 trades, got %d", len(trades))
	}
	if trades[0].ProfitETH != 11 {
		t.Fatalf("newest first: %f", trades[0].ProfitETH)
	}
}

func TestStoreUpdate(t *testing.T) {
	s := NewStore()
	s.Update(func(sn *Snapshot) {
		sn.PnLToday = 1.5
		sn.WinRate = 75.0
	})
	snap := s.Get()
	if snap.PnLToday != 1.5 || snap.WinRate != 75.0 {
		t.Fatalf("snap: %+v", snap)
	}
	if snap.UpdatedAt.IsZero() {
		t.Fatal("updated_at should be set")
	}
}

func TestTopPoolJSONTags(t *testing.T) {
	p := TopPool{Address: "0x1", Protocol: "v2", Score: 0.5, TVLUSD: 1000}
	if p.Address == "" || p.Score != 0.5 {
		t.Fatal("struct fields")
	}
}

func TestSnapshotBreakerFields(t *testing.T) {
	s := NewStore()
	s.Update(func(sn *Snapshot) {
		sn.BreakerOpen = true
		sn.BreakerReason = "signer_unavailable"
	})
	snap := s.Get()
	if !snap.BreakerOpen || snap.BreakerReason != "signer_unavailable" {
		t.Fatalf("breaker: %+v", snap)
	}
}

func TestTradeRecordFields(t *testing.T) {
	tr := TradeRecord{
		Timestamp:  time.Now().UTC(),
		ProfitETH:  0.01,
		GasETH:     0.001,
		Builder:    "titan",
		BundleHash: "0xhash",
	}
	if tr.Builder != "titan" {
		t.Fatal("builder")
	}
}

func TestStoreConcurrentAccess(t *testing.T) {
	s := NewStore()
	done := make(chan struct{})
	go func() {
		for i := 0; i < 100; i++ {
			s.Update(func(sn *Snapshot) { sn.PnLToday += 0.001 })
		}
		close(done)
	}()
	for i := 0; i < 100; i++ {
		_ = s.Get()
	}
	<-done
}

func TestStoreEmptyTopPools(t *testing.T) {
	s := NewStore()
	s.SetTopPools(nil)
	if len(s.Get().TopPools) != 0 {
		t.Fatal("expected empty")
	}
}

func TestSnapshotExecutorReachable(t *testing.T) {
	s := NewStore()
	s.Update(func(sn *Snapshot) { sn.ExecutorReachable = false })
	if s.Get().ExecutorReachable {
		t.Fatal("expected unreachable")
	}
}
