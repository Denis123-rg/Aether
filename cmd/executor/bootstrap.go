package main

import (
	"context"
	"fmt"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"

	"github.com/aether-arb/aether/internal/config"
)

// ethDialFunc dials an Ethereum JSON-RPC endpoint. Production uses
// ethclient.DialContext; tests inject a mock dialer.
type ethDialFunc func(ctx context.Context, url string) (*ethclient.Client, error)

func defaultEthDial(ctx context.Context, url string) (*ethclient.Client, error) {
	return ethclient.DialContext(ctx, url)
}

// bootstrapResult holds a validated node connection and chain metadata.
type bootstrapResult struct {
	Client  *ethclient.Client
	ChainID int64
}

// bootstrap connects to rpcURL, verifies chain ID and executor bytecode.
func bootstrap(ctx context.Context, execCfg config.ExecutorFileConfig, rpcURL string, dial ethDialFunc) (*bootstrapResult, error) {
	if rpcURL == "" {
		return nil, fmt.Errorf("ETH_RPC_URL not set")
	}
	if dial == nil {
		dial = defaultEthDial
	}

	ethClient, err := dial(ctx, rpcURL)
	if err != nil {
		return nil, fmt.Errorf("dial eth rpc: %w", err)
	}

	chainCtx, chainCancel := context.WithTimeout(ctx, 5*time.Second)
	defer chainCancel()
	chainID, err := ethClient.ChainID(chainCtx)
	if err != nil {
		ethClient.Close()
		return nil, fmt.Errorf("chain id: %w", err)
	}
	if chainID.Int64() != execCfg.ExpectedChainID {
		ethClient.Close()
		return nil, fmt.Errorf("chain-id mismatch: node=%d config=%d", chainID.Int64(), execCfg.ExpectedChainID)
	}

	codeCtx, codeCancel := context.WithTimeout(ctx, 5*time.Second)
	defer codeCancel()
	code, err := ethClient.CodeAt(codeCtx, common.HexToAddress(execCfg.ExecutorAddress), nil)
	if err != nil {
		ethClient.Close()
		return nil, fmt.Errorf("get code: %w", err)
	}
	if len(code) == 0 {
		ethClient.Close()
		return nil, fmt.Errorf("executor has no bytecode at %s", execCfg.ExecutorAddress)
	}

	return &bootstrapResult{Client: ethClient, ChainID: chainID.Int64()}, nil
}
