package signer

import (
	"sync"
	"testing"

	"github.com/ethereum/go-ethereum/crypto"
)

func TestConcurrentSignDigest(t *testing.T) {
	kl, _ := loadedTestKey(t)
	digest := crypto.Keccak256([]byte("concurrent-sign-load"))

	const workers = 32
	const rounds = 50

	var wg sync.WaitGroup
	errCh := make(chan error, workers*rounds)

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < rounds; i++ {
				sig, err := kl.SignDigest(digest)
				if err != nil {
					errCh <- err
					return
				}
				if len(sig) != 65 {
					errCh <- errDigestLen
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatalf("concurrent sign: %v", err)
		}
	}
}

var errDigestLen = &digestLenError{}

type digestLenError struct{}

func (e *digestLenError) Error() string { return "bad signature length" }
