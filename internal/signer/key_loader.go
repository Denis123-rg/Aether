// Package signer implements Aether's local in-memory bundle signer.
//
// The searcher private key is the single most sensitive secret in the system.
// This package keeps it encrypted at rest (AES-256-GCM with a passphrase-
// derived key) and, once decrypted, holds the raw key bytes in an mlock'd
// buffer that is explicitly zeroed on shutdown. Signing is exposed over a
// local-only unix-domain socket (see signer_server.go) so the key never has to
// live inside the executor process address space.
//
// KDF note: the original design brief called for bcrypt. bcrypt is a password
// *hasher*, not a key-derivation function — it caps input at 72 bytes and emits
// a fixed 60-char digest, not an arbitrary-length symmetric key — so it is the
// wrong primitive for deriving an AES-256 key. We use PBKDF2-HMAC-SHA256
// (std-lib `crypto/pbkdf2`, Go 1.24+) instead, with the iteration count and
// salt stored alongside the ciphertext so the parameters can be tuned over
// time without invalidating existing key files.
package signer

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdsa"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

const (
	// fileMagic tags the encrypted blob so a wrong/garbage file fails fast with
	// a clear error instead of a cryptic GCM auth failure.
	fileMagic = "AETK"
	// fileVersion is bumped on any breaking change to the on-disk layout.
	fileVersion byte = 1
	// kdfPBKDF2SHA256 is the only KDF id currently emitted; the field exists so
	// a future migration (e.g. Argon2id, once a vetted std-lib lands) is a
	// version bump, not a format guess.
	kdfPBKDF2SHA256 byte = 1

	saltLen  = 16
	nonceLen = 12 // AES-GCM standard nonce size
	keyLen   = 32 // AES-256
	privLen  = 32 // secp256k1 scalar

	// DefaultPBKDF2Iters is a 2025-appropriate work factor for an
	// interactive-unlock secret. Stored per-file, so this default only governs
	// freshly-encrypted keys.
	DefaultPBKDF2Iters = 600_000
)

// ErrEmptyPassphrase is returned when an empty passphrase is supplied. An empty
// passphrase defeats the entire at-rest protection, so it is rejected outright
// rather than silently producing a weak key.
var ErrEmptyPassphrase = errors.New("signer: passphrase must not be empty")

// KeyLoader owns a decrypted private key in locked memory. It is the only thing
// in the process that can produce signatures. Construct via LoadKeyFile or
// LoadKey; always pair construction with a deferred Destroy.
type KeyLoader struct {
	priv    *ecdsa.PrivateKey
	address common.Address

	// raw is the decrypted 32-byte scalar in an mlock'd buffer, retained solely
	// so Destroy can zero it. crypto.ToECDSA copies the bytes into the *ecdsa
	// key (whose big.Int we cannot reliably wipe), so this is best-effort
	// hygiene on the one copy we fully control.
	raw    []byte
	locked bool
}

// Address returns the Ethereum address derived from the loaded key.
func (k *KeyLoader) Address() common.Address { return k.address }

// SignDigest signs a 32-byte digest with the loaded key, returning the 65-byte
// [R || S || V] secp256k1 signature (V ∈ {0,1}, go-ethereum convention). The
// caller is responsible for any application-level pre-hashing (EIP-191, EIP-712,
// keccak of an RLP tx, …); this is the raw ECDSA-over-hash primitive.
func (k *KeyLoader) SignDigest(digest []byte) ([]byte, error) {
	if k == nil || k.priv == nil {
		return nil, errors.New("signer: key not loaded")
	}
	if len(digest) != 32 {
		return nil, fmt.Errorf("signer: digest must be 32 bytes, got %d", len(digest))
	}
	return crypto.Sign(digest, k.priv)
}

// Destroy zeroes the locked key buffer and unlocks it. Safe to call multiple
// times and on a nil receiver. After Destroy the loader can no longer sign.
func (k *KeyLoader) Destroy() {
	if k == nil {
		return
	}
	if k.raw != nil {
		zeroize(k.raw)
		if k.locked {
			_ = munlock(k.raw)
			k.locked = false
		}
		k.raw = nil
	}
	k.priv = nil
}

// LoadKeyFile reads an encrypted key file, decrypts it with the passphrase, and
// returns a ready KeyLoader. The decrypted scalar is mlock'd before the *ecdsa
// key is derived.
func LoadKeyFile(path, passphrase string) (*KeyLoader, error) {
	blob, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("signer: read key file: %w", err)
	}
	return LoadKey(blob, passphrase)
}

// LoadKey decrypts an in-memory encrypted blob (the exact bytes Encrypt
// produced) and returns a ready KeyLoader.
func LoadKey(blob []byte, passphrase string) (*KeyLoader, error) {
	if passphrase == "" {
		return nil, ErrEmptyPassphrase
	}
	salt, iters, nonce, ciphertext, err := parseBlob(blob)
	if err != nil {
		return nil, err
	}

	dk, err := pbkdf2.Key(sha256.New, passphrase, salt, iters, keyLen)
	if err != nil {
		return nil, fmt.Errorf("signer: derive key: %w", err)
	}
	defer zeroize(dk)

	gcm, err := newGCM(dk)
	if err != nil {
		return nil, err
	}

	// Lock a destination buffer BEFORE decrypting into it, so the plaintext
	// scalar is never written to a swappable page. gcm.Open(dst[:0], …) appends
	// into our pre-locked, pre-sized buffer instead of allocating a fresh one.
	raw := make([]byte, 0, privLen)
	locked := false
	if full := raw[:privLen]; mlock(full) == nil {
		locked = true
	}
	plain, err := gcm.Open(raw, nonce, ciphertext, nil)
	if err != nil {
		if locked {
			_ = munlock(raw[:privLen])
		}
		// A failed open is overwhelmingly a wrong passphrase; never leak detail.
		return nil, errors.New("signer: decryption failed (wrong passphrase or corrupt key file)")
	}
	if len(plain) != privLen {
		zeroize(plain)
		if locked {
			_ = munlock(raw[:privLen])
		}
		return nil, fmt.Errorf("signer: decrypted key has wrong length %d, want %d", len(plain), privLen)
	}

	priv, err := crypto.ToECDSA(plain)
	if err != nil {
		zeroize(plain)
		if locked {
			_ = munlock(raw[:privLen])
		}
		return nil, errors.New("signer: decrypted bytes are not a valid secp256k1 key")
	}

	return &KeyLoader{
		priv:    priv,
		address: crypto.PubkeyToAddress(priv.PublicKey),
		raw:     plain,
		locked:  locked,
	}, nil
}

// Encrypt produces an encrypted key blob from a raw 32-byte secp256k1 scalar.
// iters <= 0 falls back to DefaultPBKDF2Iters. The returned bytes are the exact
// on-disk format LoadKey expects.
func Encrypt(privKey []byte, passphrase string, iters int) ([]byte, error) {
	if passphrase == "" {
		return nil, ErrEmptyPassphrase
	}
	if len(privKey) != privLen {
		return nil, fmt.Errorf("signer: private key must be %d bytes, got %d", privLen, len(privKey))
	}
	if _, err := crypto.ToECDSA(privKey); err != nil {
		return nil, errors.New("signer: bytes are not a valid secp256k1 key")
	}
	if iters <= 0 {
		iters = DefaultPBKDF2Iters
	}

	salt := make([]byte, saltLen)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return nil, fmt.Errorf("signer: read salt: %w", err)
	}
	dk, err := pbkdf2.Key(sha256.New, passphrase, salt, iters, keyLen)
	if err != nil {
		return nil, fmt.Errorf("signer: derive key: %w", err)
	}
	defer zeroize(dk)

	gcm, err := newGCM(dk)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, nonceLen)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("signer: read nonce: %w", err)
	}
	ciphertext := gcm.Seal(nil, nonce, privKey, nil)

	return encodeBlob(salt, iters, nonce, ciphertext), nil
}

// ParseHexKey decodes a hex-encoded secp256k1 private key (optional 0x prefix)
// into its raw 32-byte form, validating it is a usable key. Used by the CLI
// encrypt path. The caller should zeroize the result when done.
func ParseHexKey(s string) ([]byte, error) {
	cleaned := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(s), "0x"))
	b, err := hex.DecodeString(cleaned)
	if err != nil {
		return nil, errors.New("signer: private key is not valid hex")
	}
	if len(b) != privLen {
		return nil, fmt.Errorf("signer: private key must be %d hex bytes, got %d", privLen, len(b))
	}
	if _, err := crypto.ToECDSA(b); err != nil {
		zeroize(b)
		return nil, errors.New("signer: bytes are not a valid secp256k1 key")
	}
	return b, nil
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("signer: aes init: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("signer: gcm init: %w", err)
	}
	return gcm, nil
}

// encodeBlob lays out the self-describing on-disk format:
//
//	magic[4] | version[1] | kdf[1] | iters[4 BE] | salt[16] | nonce[12] | ciphertext[…]
func encodeBlob(salt []byte, iters int, nonce, ciphertext []byte) []byte {
	out := make([]byte, 0, 4+1+1+4+len(salt)+len(nonce)+len(ciphertext))
	out = append(out, fileMagic...)
	out = append(out, fileVersion, kdfPBKDF2SHA256)
	var itersBE [4]byte
	binary.BigEndian.PutUint32(itersBE[:], uint32(iters))
	out = append(out, itersBE[:]...)
	out = append(out, salt...)
	out = append(out, nonce...)
	out = append(out, ciphertext...)
	return out
}

func parseBlob(blob []byte) (salt []byte, iters int, nonce, ciphertext []byte, err error) {
	const headerLen = 4 + 1 + 1 + 4 + saltLen + nonceLen
	if len(blob) < headerLen+16 { // +16 = minimum GCM tag
		return nil, 0, nil, nil, errors.New("signer: key file too short or truncated")
	}
	if string(blob[:4]) != fileMagic {
		return nil, 0, nil, nil, errors.New("signer: bad key file magic (not an Aether key file)")
	}
	if blob[4] != fileVersion {
		return nil, 0, nil, nil, fmt.Errorf("signer: unsupported key file version %d", blob[4])
	}
	if blob[5] != kdfPBKDF2SHA256 {
		return nil, 0, nil, nil, fmt.Errorf("signer: unsupported kdf id %d", blob[5])
	}
	iters = int(binary.BigEndian.Uint32(blob[6:10]))
	if iters <= 0 {
		return nil, 0, nil, nil, errors.New("signer: key file declares non-positive iteration count")
	}
	off := 10
	salt = blob[off : off+saltLen]
	off += saltLen
	nonce = blob[off : off+nonceLen]
	off += nonceLen
	ciphertext = blob[off:]
	return salt, iters, nonce, ciphertext, nil
}
