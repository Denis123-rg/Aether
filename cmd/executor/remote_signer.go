package main

import (
	"errors"
	"fmt"
	"math/big"
	"os"
	"strings"

	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"

	"github.com/aether-arb/aether/internal/signer"
)

// TxSigner abstracts transaction signing so the bundle constructor can use
// either the in-process key signer (*TransactionSigner) or the out-of-process
// remote signer (*RemoteSigner) interchangeably. Both expose the searcher
// address and a tx-signing primitive; only the remote variant keeps the key
// out of this process.
type TxSigner interface {
	Address() common.Address
	SignTx(*types.Transaction) (*types.Transaction, error)
}

// flashbotsAuther signs builder request bodies for the X-Flashbots-Signature
// header. Both *FlashbotsSigner (local key) and remoteFlashbotsAuth (remote
// signer) satisfy it, letting the submitter authenticate without holding a key.
type flashbotsAuther interface {
	Sign(payload []byte) (string, error)
}

// errSignerUnavailable marks errors caused by the remote signer being
// unreachable (socket down / dial failure / RPC error), as distinct from a
// malformed request. processArb checks for it via errors.Is to pause the
// executor when the signer disappears mid-run.
var errSignerUnavailable = errors.New("remote signer unavailable")

// RemoteSigner signs transactions and Flashbots auth payloads via the local
// out-of-process signer (internal/signer) over its 0600 unix socket. The
// searcher private key never enters the executor's address space — only the
// 32-byte signing digest crosses the socket.
type RemoteSigner struct {
	client    *signer.Client
	address   common.Address
	ethSigner types.Signer
}

// NewRemoteSigner dials the signer socket, fetches the signer address, and
// returns a ready RemoteSigner. The address probe doubles as an eager
// connectivity check so a misconfigured / down signer fails fast at startup
// (as errSignerUnavailable).
func NewRemoteSigner(socketPath string, chainID int64) (*RemoteSigner, error) {
	if socketPath == "" {
		return nil, errors.New("remote signer: empty socket path")
	}
	if chainID <= 0 {
		return nil, fmt.Errorf("remote signer: chain id must be positive, got %d", chainID)
	}
	client := signer.Dial(socketPath)
	addrHex, err := client.Address()
	if err != nil {
		return nil, fmt.Errorf("%w: address probe on %s: %v", errSignerUnavailable, socketPath, err)
	}
	if !common.IsHexAddress(addrHex) {
		return nil, fmt.Errorf("remote signer: returned invalid address %q", addrHex)
	}
	return &RemoteSigner{
		client:    client,
		address:   common.HexToAddress(addrHex),
		ethSigner: types.LatestSignerForChainID(big.NewInt(chainID)),
	}, nil
}

// Address returns the signer's Ethereum address.
func (r *RemoteSigner) Address() common.Address { return r.address }

// SignTx signs a transaction by computing its EIP-1559 signing hash locally and
// asking the remote signer to sign that 32-byte digest, then reattaching the
// returned [R||S||V] signature. The digest computation matches
// types.SignTx (both use the chain's LatestSigner), so a bundle signed here is
// byte-identical to one signed in-process.
func (r *RemoteSigner) SignTx(tx *types.Transaction) (*types.Transaction, error) {
	h := r.ethSigner.Hash(tx)
	sig, err := r.client.SignDigest(h[:])
	if err != nil {
		return nil, fmt.Errorf("%w: sign tx digest: %v", errSignerUnavailable, err)
	}
	signed, err := tx.WithSignature(r.ethSigner, sig)
	if err != nil {
		// A malformed signature is a signer/key bug, not a connectivity
		// problem, so it is NOT wrapped as errSignerUnavailable.
		return nil, fmt.Errorf("remote signer: attach signature: %w", err)
	}
	return signed, nil
}

// Ping verifies the signer is reachable and able to sign by signing a fixed
// 32-byte probe digest. Used at startup as an explicit liveness check beyond
// the address probe (the prompt's "sign a known hash"). The signature is
// discarded.
func (r *RemoteSigner) Ping() error {
	var probe [32]byte // deterministic all-zero digest
	if _, err := r.client.SignDigest(probe[:]); err != nil {
		return fmt.Errorf("%w: ping: %v", errSignerUnavailable, err)
	}
	return nil
}

// SignFlashbotsPayload produces the X-Flashbots-Signature header value
// ("address:0xsignature") for a builder request body, mirroring
// FlashbotsSigner.Sign exactly but signing through the remote signer:
//
//	payload -> keccak256 -> hex string -> EIP-191 TextHash -> secp256k1 sign
func (r *RemoteSigner) SignFlashbotsPayload(payload []byte) (string, error) {
	hashHex := crypto.Keccak256Hash(payload).Hex()
	digest := accounts.TextHash([]byte(hashHex))
	sig, err := r.client.SignDigest(digest)
	if err != nil {
		return "", fmt.Errorf("%w: flashbots auth: %v", errSignerUnavailable, err)
	}
	if len(sig) != 65 {
		return "", fmt.Errorf("remote signer: unexpected signature length %d, want 65", len(sig))
	}
	// Adjust V from 0/1 to 27/28 per Ethereum convention (matches FlashbotsSigner).
	sig[64] += 27
	return fmt.Sprintf("%s:%s", r.address.Hex(), hexutil.Encode(sig)), nil
}

// remoteFlashbotsAuth adapts a *RemoteSigner to the flashbotsAuther interface
// the submitter consumes for the auth header.
type remoteFlashbotsAuth struct{ rs *RemoteSigner }

func (a remoteFlashbotsAuth) Sign(payload []byte) (string, error) {
	return a.rs.SignFlashbotsPayload(payload)
}

// resolveSignerSocket returns the configured remote-signer socket path, or ""
// when no remote signer is configured (in which case the executor falls back to
// the in-process SEARCHER_KEY). Selection is intentionally explicit via the
// AETHER_SIGNER_SOCKET env var rather than auto-detecting config/signer.yaml
// (which is the signer *service*'s own config): the committed signer.yaml must
// not silently force every executor — including dev/demo runs without a running
// signer — onto the remote path. A leading "unix://" is stripped for
// convenience so either form works.
func resolveSignerSocket() string {
	s := strings.TrimSpace(os.Getenv("AETHER_SIGNER_SOCKET"))
	return strings.TrimPrefix(s, "unix://")
}
