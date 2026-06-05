//go:build unix

package signer

import "syscall"

// mlock pins b into RAM so the decrypted key scalar can never be written to
// swap. A nil/empty slice is a no-op. Errors are returned to the caller, which
// treats locking as best-effort (a hardened host may forbid mlock for an
// unprivileged process); the key is still zeroed on shutdown regardless.
func mlock(b []byte) error {
	if len(b) == 0 {
		return nil
	}
	return syscall.Mlock(b)
}

// munlock reverses mlock. A nil/empty slice is a no-op.
func munlock(b []byte) error {
	if len(b) == 0 {
		return nil
	}
	return syscall.Munlock(b)
}
