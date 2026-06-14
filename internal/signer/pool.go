package signer

import (
	"fmt"
	"net"
	"net/rpc"
	"net/rpc/jsonrpc"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
)

// useConnectionPool reports whether the signer client reuses a persistent UDS.
func useConnectionPool() bool {
	raw := strings.TrimSpace(os.Getenv("SIGNER_USE_CONNECTION_POOL"))
	if raw == "" {
		return false
	}
	v, err := strconv.ParseBool(raw)
	return err == nil && v
}

// PooledSignerClient reuses a single Unix socket connection with a mutex.
type PooledSignerClient struct {
	socketPath string
	mu         sync.Mutex
	conn       net.Conn
	rpc        *rpc.Client
	reuseCount atomic.Uint64
}

// NewPooledSignerClient returns a pooled client for the signer socket.
func NewPooledSignerClient(socketPath string) *PooledSignerClient {
	return &PooledSignerClient{socketPath: socketPath}
}

func (c *PooledSignerClient) dialLocked() error {
	if c.rpc != nil {
		return nil
	}
	conn, err := net.Dial("unix", c.socketPath)
	if err != nil {
		return fmt.Errorf("signer: dial %s: %w", c.socketPath, err)
	}
	c.conn = conn
	c.rpc = jsonrpc.NewClient(conn)
	return nil
}

func (c *PooledSignerClient) resetLocked() {
	if c.conn != nil {
		_ = c.conn.Close()
	}
	c.conn = nil
	c.rpc = nil
}

func (c *PooledSignerClient) call(method string, args, reply any) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err := c.dialLocked(); err != nil {
		return err
	}
	c.reuseCount.Add(1)
	err := c.rpc.Call(method, args, reply)
	if err != nil {
		c.resetLocked()
		// Reconnect once and retry.
		if dialErr := c.dialLocked(); dialErr != nil {
			return err
		}
		if retryErr := c.rpc.Call(method, args, reply); retryErr != nil {
			c.resetLocked()
			return retryErr
		}
		return nil
	}
	return nil
}

// ReuseCount returns how many calls reused the persistent connection.
func (c *PooledSignerClient) ReuseCount() uint64 {
	return c.reuseCount.Load()
}

// Close shuts down the pooled connection.
func (c *PooledSignerClient) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn == nil {
		return nil
	}
	err := c.conn.Close()
	c.resetLocked()
	return err
}

func (c *PooledSignerClient) SignDigest(digest []byte) ([]byte, error) {
	var reply SignDigestReply
	if err := c.call(serviceName+".SignDigest", &SignDigestArgs{Digest: digest}, &reply); err != nil {
		return nil, err
	}
	return reply.Signature, nil
}

func (c *PooledSignerClient) Address() (string, error) {
	var reply AddressReply
	if err := c.call(serviceName+".Address", &AddressArgs{}, &reply); err != nil {
		return "", err
	}
	return reply.Address, nil
}

func (c *PooledSignerClient) Ping() error {
	var probe [32]byte
	if _, err := c.SignDigest(probe[:]); err != nil {
		return fmt.Errorf("signer ping: %w", err)
	}
	return nil
}

func (c *PooledSignerClient) SignFlashbotsPayload(payload []byte) (string, error) {
	addrHex, err := c.Address()
	if err != nil {
		return "", fmt.Errorf("signer address: %w", err)
	}
	hashHex := crypto.Keccak256Hash(payload).Hex()
	digest := accounts.TextHash([]byte(hashHex))
	sig, err := c.SignDigest(digest)
	if err != nil {
		return "", fmt.Errorf("signer flashbots digest: %w", err)
	}
	if len(sig) != 65 {
		return "", fmt.Errorf("signer: unexpected signature length %d, want 65", len(sig))
	}
	sig[64] += 27
	return fmt.Sprintf("%s:%s", addrHex, hexutil.Encode(sig)), nil
}

// DialAuto returns a pooled client when SIGNER_USE_CONNECTION_POOL=true,
// otherwise the legacy per-call Client.
func DialAuto(socketPath string) Signer {
	if useConnectionPool() {
		return NewPooledSignerClient(socketPath)
	}
	return Dial(socketPath)
}

// Signer is the minimal signing surface shared by Client and PooledSignerClient.
type Signer interface {
	SignDigest(digest []byte) ([]byte, error)
	Address() (string, error)
	Ping() error
	SignFlashbotsPayload(payload []byte) (string, error)
}
