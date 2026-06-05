package signer

import "runtime"

// zeroize overwrites b with zeros and keeps it alive across the write so the
// compiler cannot elide the loop as a dead store. This is the std-lib
// equivalent of explicit_bzero for the one key copy we fully own (the mlock'd
// scalar buffer). It is best-effort: Go's runtime may have already copied the
// bytes elsewhere (GC moves, the *ecdsa key's big.Int) which we cannot reach.
func zeroize(b []byte) {
	for i := range b {
		b[i] = 0
	}
	runtime.KeepAlive(b)
}
