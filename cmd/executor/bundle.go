package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"math/big"
	"os"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

// Bundle represents a Flashbots-style bundle with signed transactions.
//
// For mempool-backrun source, `VictimTxHashHex` is set to the pending
// victim's tx hash; the submitter prepends it to the `txs` array so the
// envelope becomes `[victim_hash, our_arb_signed, our_tip_signed?]`.
// `RevertingTxHashes` is populated with the hashes that may revert
// without dropping the whole bundle — for mempool path this is the
// our-arb tx hash only; never the victim hash (we want the bundle to
// drop if the victim itself reverts).
type Bundle struct {
	Transactions []*types.Transaction // Signed go-ethereum transactions
	RawTxs       [][]byte             // RLP-encoded signed bytes (for eth_sendBundle)
	BlockNumber  uint64
	Timestamp    time.Time

	// Source identifies which pipeline produced this bundle; matches the
	// `source` label on the executor's bundles_* counters. Empty defaults
	// to block-driven for backward compatibility with callers that
	// pre-date #139.
	Source string

	// VictimTxHashHex is "0x"-prefixed when the bundle backruns a
	// pending mempool tx; empty otherwise. Submitter consumes this to
	// prepend the victim reference in the `txs` envelope.
	VictimTxHashHex string

	// RevertingTxHashes is the list of `[]string` hashes the bundle
	// tolerates reverting. Submitter passes through to the
	// `revertingTxHashes` param on `eth_sendBundle`. Always excludes the
	// victim hash for mempool source (see field doc on Bundle above).
	RevertingTxHashes []string
}

// BundleConstructor builds bundles from validated arbs.
type BundleConstructor struct {
	nonceManager *NonceManager
	gasOracle    *GasOracle
	signer       *TransactionSigner
	chainID      int64
}

// NewBundleConstructor creates a new bundle constructor.
// The signer is used to sign transactions; if nil, transactions are left unsigned.
func NewBundleConstructor(nm *NonceManager, go_ *GasOracle, signer *TransactionSigner, chainID int64) *BundleConstructor {
	return &BundleConstructor{
		nonceManager: nm,
		gasOracle:    go_,
		signer:       signer,
		chainID:      chainID,
	}
}

// BuildBundle constructs a single-transaction bundle containing only the arb tx.
// The coinbase tip is now handled inline by the Solidity contract, so no
// separate tip transaction is needed.
func (bc *BundleConstructor) BuildBundle(
	arbCalldata []byte,
	executorAddr string,
	gasEstimate uint64,
	targetBlock uint64,
) (*Bundle, error) {
	gasFees := bc.gasOracle.CurrentFees()
	nonce := bc.nonceManager.Next()
	chainID := big.NewInt(bc.chainID)
	executor := common.HexToAddress(executorAddr)

	arbTx := types.NewTx(&types.DynamicFeeTx{
		ChainID:   chainID,
		Nonce:     nonce,
		GasTipCap: gasFees.MaxPriorityFee,
		GasFeeCap: gasFees.MaxFeePerGas,
		Gas:       gasEstimate,
		To:        &executor,
		Value:     big.NewInt(0),
		Data:      arbCalldata,
	})

	// Sign transaction if signer is available.
	if bc.signer != nil {
		signed, err := bc.signer.SignTx(arbTx)
		if err != nil {
			return nil, fmt.Errorf("sign arb tx: %w", err)
		}

		raw, err := signed.MarshalBinary()
		if err != nil {
			return nil, fmt.Errorf("RLP-encode arb tx: %w", err)
		}

		return &Bundle{
			Transactions: []*types.Transaction{signed},
			RawTxs:       [][]byte{raw},
			BlockNumber:  targetBlock,
			Timestamp:    time.Now(),
		}, nil
	}

	// No signer — return unsigned (for testing).
	return &Bundle{
		Transactions: []*types.Transaction{arbTx},
		BlockNumber:  targetBlock,
		Timestamp:    time.Now(),
	}, nil
}

// BuildMempoolBackrunBundle constructs a bundle that backruns a pending
// victim transaction.
//
// Envelope: `[victim_tx_hash, our_arb_signed]`. The submitter prepends
// the victim hash in `submitToBuilder`; this function returns a Bundle
// whose `RawTxs` contains only our signed arb tx and whose
// `VictimTxHashHex` carries the hash the builder needs to fetch from
// its mempool view.
//
// `revertingTxHashes` includes only the arb tx hash — we tolerate the
// arb reverting (the bundle still mines without polluting the block)
// but we MUST NOT tolerate the victim reverting (an adverse-fill
// scenario where our position is filled at the wrong price). Builder
// drops the whole bundle if the victim hash isn't in `revertingTxHashes`
// and the victim tx reverts on-chain, which is exactly what we want.
func (bc *BundleConstructor) BuildMempoolBackrunBundle(
	arbCalldata []byte,
	executorAddr string,
	gasEstimate uint64,
	targetBlock uint64,
	victimTxHashHex string,
) (*Bundle, error) {
	if !strings.HasPrefix(victimTxHashHex, "0x") {
		return nil, fmt.Errorf("victim_tx_hash must be 0x-prefixed, got %q", victimTxHashHex)
	}
	if len(victimTxHashHex) != 66 {
		return nil, fmt.Errorf("victim_tx_hash must be 32 bytes (66 hex chars incl. 0x), got %d chars", len(victimTxHashHex))
	}

	gasFees := bc.gasOracle.MempoolFees()
	nonce := bc.nonceManager.Next()
	chainID := big.NewInt(bc.chainID)
	executor := common.HexToAddress(executorAddr)

	arbTx := types.NewTx(&types.DynamicFeeTx{
		ChainID:   chainID,
		Nonce:     nonce,
		GasTipCap: gasFees.MaxPriorityFee,
		GasFeeCap: gasFees.MaxFeePerGas,
		Gas:       gasEstimate,
		To:        &executor,
		Value:     big.NewInt(0),
		Data:      arbCalldata,
	})

	if bc.signer == nil {
		// Unsigned path is test-only; tip flow still requires a signer.
		return &Bundle{
			Transactions:      []*types.Transaction{arbTx},
			BlockNumber:       targetBlock,
			Timestamp:         time.Now(),
			Source:            SourceMempoolBackrun,
			VictimTxHashHex:   victimTxHashHex,
			RevertingTxHashes: []string{arbTx.Hash().Hex()},
		}, nil
	}

	signed, err := bc.signer.SignTx(arbTx)
	if err != nil {
		return nil, fmt.Errorf("sign mempool-backrun arb tx: %w", err)
	}
	raw, err := signed.MarshalBinary()
	if err != nil {
		return nil, fmt.Errorf("RLP-encode mempool-backrun arb tx: %w", err)
	}

	return &Bundle{
		Transactions:      []*types.Transaction{signed},
		RawTxs:            [][]byte{raw},
		BlockNumber:       targetBlock,
		Timestamp:         time.Now(),
		Source:            SourceMempoolBackrun,
		VictimTxHashHex:   victimTxHashHex,
		RevertingTxHashes: []string{signed.Hash().Hex()},
	}, nil
}

// GenerateBundleID creates a unique bundle identifier
func GenerateBundleID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		slog.Error("crypto/rand failure", "err", err)
		os.Exit(1)
	}
	return hex.EncodeToString(b)
}
