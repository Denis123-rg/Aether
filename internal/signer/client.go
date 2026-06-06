package signer

import (
	"fmt"
	"net"
	"net/rpc/jsonrpc"

	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
)

// Client is a thin JSON-RPC-over-unix-socket client for the signer. It dials a
// fresh connection per call, which is plenty for the executor's per-bundle
// signing rate and keeps the client free of connection-state bugs. For the
// hot path a future pooled client can implement the same method set.
//
// The executor wraps this in cmd/executor.RemoteSigner, which turns the
// digest-signing primitive into transaction signing and Flashbots-auth
// signing so the searcher key never enters the executor's address space.
type Client struct {
	socketPath string
}

// Dial returns a Client for the signer socket. It does not connect eagerly; the
// connection is established per call so a temporarily-down signer surfaces as a
// per-call error the executor can retry, rather than a construction failure.
func Dial(socketPath string) *Client {
	return &Client{socketPath: socketPath}
}

func (c *Client) call(method string, args, reply any) error {
	conn, err := net.Dial("unix", c.socketPath)
	if err != nil {
		return fmt.Errorf("signer: dial %s: %w", c.socketPath, err)
	}
	defer conn.Close()
	rc := jsonrpc.NewClient(conn)
	defer rc.Close()
	return rc.Call(method, args, reply)
}

// SignDigest asks the signer to sign a 32-byte digest, returning the 65-byte
// [R||S||V] signature (V ∈ {0,1}, go-ethereum convention).
func (c *Client) SignDigest(digest []byte) ([]byte, error) {
	var reply SignDigestReply
	if err := c.call(serviceName+".SignDigest", &SignDigestArgs{Digest: digest}, &reply); err != nil {
		return nil, err
	}
	return reply.Signature, nil
}

// Address fetches the signer's Ethereum address.
func (c *Client) Address() (string, error) {
	var reply AddressReply
	if err := c.call(serviceName+".Address", &AddressArgs{}, &reply); err != nil {
		return "", err
	}
	return reply.Address, nil
}

// Ping verifies the signer is reachable by signing a fixed all-zero probe digest.
func (c *Client) Ping() error {
	var probe [32]byte
	if _, err := c.SignDigest(probe[:]); err != nil {
		return fmt.Errorf("signer ping: %w", err)
	}
	return nil
}

// SignFlashbotsPayload produces the X-Flashbots-Signature header value
// ("address:0xsignature") for a builder request body:
//
//	payload -> keccak256 -> hex string -> EIP-191 TextHash -> secp256k1 sign
func (c *Client) SignFlashbotsPayload(payload []byte) (string, error) {
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
