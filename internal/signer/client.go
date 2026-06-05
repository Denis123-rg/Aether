package signer

import (
	"fmt"
	"net"
	"net/rpc/jsonrpc"
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
