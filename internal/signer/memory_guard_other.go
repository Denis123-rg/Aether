//go:build !unix

package signer

// On non-unix platforms (Windows dev boxes) mlock is unavailable. These no-ops
// keep `go build ./...` green cross-platform; production runs on Linux where
// the unix build tag selects the real syscall-backed implementation.
func mlock([]byte) error   { return nil }
func munlock([]byte) error { return nil }
