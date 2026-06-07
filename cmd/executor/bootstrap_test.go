package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ethereum/go-ethereum/ethclient"

	"github.com/aether-arb/aether/internal/config"
)

func mockEthRPC(t *testing.T, chainID int64, code []byte) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Method string `json:"method"`
			ID     any    `json:"id"`
		}
		_ = json.Unmarshal(body, &req)
		resp := map[string]any{"jsonrpc": "2.0", "id": req.ID}
		switch req.Method {
		case "eth_chainId":
			resp["result"] = fmt.Sprintf("0x%x", chainID)
		case "eth_getCode":
			if len(code) == 0 {
				resp["result"] = "0x"
			} else {
				resp["result"] = fmt.Sprintf("0x%x", code)
			}
		default:
			resp["result"] = "0x1"
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
}

func TestBootstrap_Table(t *testing.T) {
	t.Parallel()

	validExec := config.ExecutorFileConfig{
		ExecutorAddress: "0x0000000000000000000000000000000000000001",
		ExpectedChainID: 1,
	}

	tests := []struct {
		name    string
		rpcURL  string
		execCfg config.ExecutorFileConfig
		dial    ethDialFunc
		wantErr string
	}{
		{
			name:    "empty rpc url",
			rpcURL:  "",
			execCfg: validExec,
			wantErr: "ETH_RPC_URL not set",
		},
		{
			name:    "dial failure",
			rpcURL:  "http://127.0.0.1:1",
			execCfg: validExec,
			dial: func(ctx context.Context, url string) (*ethclient.Client, error) {
				return nil, fmt.Errorf("connection refused")
			},
			wantErr: "dial eth rpc",
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := bootstrap(context.Background(), tc.execCfg, tc.rpcURL, tc.dial)
			if err == nil {
				t.Fatal("expected error")
			}
			if tc.wantErr != "" && !containsSubstring(err.Error(), tc.wantErr) {
				t.Fatalf("err = %q, want substring %q", err, tc.wantErr)
			}
		})
	}
}

func TestBootstrap_Success(t *testing.T) {
	srv := mockEthRPC(t, 1, []byte{0x60, 0x80, 0x60, 0x40})
	defer srv.Close()

	execCfg := config.ExecutorFileConfig{
		ExecutorAddress: "0x0000000000000000000000000000000000000001",
		ExpectedChainID: 1,
	}
	dial := func(ctx context.Context, url string) (*ethclient.Client, error) {
		return ethclient.DialContext(ctx, srv.URL)
	}

	res, err := bootstrap(context.Background(), execCfg, srv.URL, dial)
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	defer res.Client.Close()
	if res.ChainID != 1 {
		t.Fatalf("chain id = %d", res.ChainID)
	}
}

func TestBootstrap_ChainIDMismatch(t *testing.T) {
	srv := mockEthRPC(t, 5, []byte{0x60})
	defer srv.Close()

	execCfg := config.ExecutorFileConfig{
		ExecutorAddress: "0x0000000000000000000000000000000000000001",
		ExpectedChainID: 1,
	}
	dial := func(ctx context.Context, url string) (*ethclient.Client, error) {
		return ethclient.DialContext(ctx, srv.URL)
	}

	_, err := bootstrap(context.Background(), execCfg, srv.URL, dial)
	if err == nil || !containsSubstring(err.Error(), "chain-id mismatch") {
		t.Fatalf("expected chain-id mismatch, got %v", err)
	}
}

func TestBootstrap_ChainIDRPCError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"jsonrpc": "2.0",
			"id":      1,
			"error":   map[string]any{"code": -32000, "message": "unavailable"},
		})
	}))
	defer srv.Close()

	execCfg := config.ExecutorFileConfig{
		ExecutorAddress: "0x0000000000000000000000000000000000000001",
		ExpectedChainID: 1,
	}
	dial := func(ctx context.Context, url string) (*ethclient.Client, error) {
		return ethclient.DialContext(ctx, srv.URL)
	}
	_, err := bootstrap(context.Background(), execCfg, srv.URL, dial)
	if err == nil || !containsSubstring(err.Error(), "chain id") {
		t.Fatalf("expected chain id error, got %v", err)
	}
}

func TestBootstrap_GetCodeError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Method string `json:"method"`
			ID     any    `json:"id"`
		}
		_ = json.Unmarshal(body, &req)
		resp := map[string]any{"jsonrpc": "2.0", "id": req.ID}
		switch req.Method {
		case "eth_chainId":
			resp["result"] = "0x1"
		case "eth_getCode":
			resp["error"] = map[string]any{"code": -32000, "message": "backend down"}
		default:
			resp["result"] = "0x1"
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	execCfg := config.ExecutorFileConfig{
		ExecutorAddress: "0x0000000000000000000000000000000000000001",
		ExpectedChainID: 1,
	}
	dial := func(ctx context.Context, url string) (*ethclient.Client, error) {
		return ethclient.DialContext(ctx, srv.URL)
	}
	_, err := bootstrap(context.Background(), execCfg, srv.URL, dial)
	if err == nil || !containsSubstring(err.Error(), "get code") {
		t.Fatalf("expected get code error, got %v", err)
	}
}

func TestBootstrap_NoBytecode(t *testing.T) {
	srv := mockEthRPC(t, 1, nil)
	defer srv.Close()

	execCfg := config.ExecutorFileConfig{
		ExecutorAddress: "0x0000000000000000000000000000000000000001",
		ExpectedChainID: 1,
	}
	dial := func(ctx context.Context, url string) (*ethclient.Client, error) {
		return ethclient.DialContext(ctx, srv.URL)
	}

	_, err := bootstrap(context.Background(), execCfg, srv.URL, dial)
	if err == nil || !containsSubstring(err.Error(), "no bytecode") {
		t.Fatalf("expected no bytecode error, got %v", err)
	}
}

func containsSubstring(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexSubstring(s, sub) >= 0)
}

func indexSubstring(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
