package signer

import (
	"sync"
	"testing"
)

func TestPooledSignerClient_ReuseCount(t *testing.T) {
	c := NewPooledSignerClient("/nonexistent.sock")
	// Dial will fail but reuse counter should stay 0.
	_, _ = c.SignDigest(make([]byte, 32))
	if c.ReuseCount() != 0 {
		t.Fatalf("reuse %d", c.ReuseCount())
	}
}

func TestUseConnectionPool_DefaultFalse(t *testing.T) {
	t.Setenv("SIGNER_USE_CONNECTION_POOL", "")
	if useConnectionPool() {
		t.Fatal("default should be false")
	}
}

func TestUseConnectionPool_Enabled(t *testing.T) {
	t.Setenv("SIGNER_USE_CONNECTION_POOL", "true")
	if !useConnectionPool() {
		t.Fatal("should be enabled")
	}
}

func TestDialAuto_ReturnsClientWithoutPool(t *testing.T) {
	t.Setenv("SIGNER_USE_CONNECTION_POOL", "false")
	s := DialAuto("/tmp/test.sock")
	if _, ok := s.(*Client); !ok {
		t.Fatalf("got %T", s)
	}
}

func TestDialAuto_ReturnsPooledWhenEnabled(t *testing.T) {
	t.Setenv("SIGNER_USE_CONNECTION_POOL", "true")
	s := DialAuto("/tmp/test.sock")
	if _, ok := s.(*PooledSignerClient); !ok {
		t.Fatalf("got %T", s)
	}
}

func TestPooledConcurrentCalls_NoDataRace(t *testing.T) {
	c := NewPooledSignerClient("/nonexistent.sock")
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = c.SignDigest(make([]byte, 32))
		}()
	}
	wg.Wait()
}
