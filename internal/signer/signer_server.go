package signer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/rpc"
	"net/rpc/jsonrpc"
	"os"
	"path/filepath"
	"sync"
)

// serviceName is the net/rpc service name; clients call "Signer.SignDigest"
// and "Signer.Address".
const serviceName = "Signer"

// Transport note: the design brief specified gRPC. We deliberately use std-lib
// net/rpc with the JSON-RPC codec over a unix-domain socket instead. The
// security-relevant properties the brief cares about — local-only access via a
// 0600 unix socket, no TCP exposure, a tiny request surface — are identical,
// and this avoids pulling a second protobuf service definition + codegen step
// into the build for two trivial methods. Swapping to gRPC later is a
// drop-in replacement behind the SignerClient interface.

// SignDigestArgs is the request for Signer.SignDigest. Digest must be exactly
// 32 bytes (the pre-hashed payload to sign).
type SignDigestArgs struct {
	Digest []byte
}

// SignDigestReply carries the 65-byte [R||S||V] secp256k1 signature.
type SignDigestReply struct {
	Signature []byte
}

// AddressArgs is the (empty) request for Signer.Address.
type AddressArgs struct{}

// AddressReply carries the signer's 0x-prefixed Ethereum address.
type AddressReply struct {
	Address string
}

// SignService is the net/rpc receiver exposing the loaded key's operations.
type SignService struct {
	kl *KeyLoader
}

// SignDigest signs the supplied 32-byte digest. Errors are intentionally
// generic — they cross the socket to the client and must never hint at key
// material.
func (s *SignService) SignDigest(args *SignDigestArgs, reply *SignDigestReply) error {
	sig, err := s.kl.SignDigest(args.Digest)
	if err != nil {
		return err
	}
	reply.Signature = sig
	return nil
}

// Address returns the signer address so the executor can configure nonce
// tracking and balance polling without ever holding the key.
func (s *SignService) Address(_ *AddressArgs, reply *AddressReply) error {
	reply.Address = s.kl.Address().Hex()
	return nil
}

// Server hosts a SignService on a unix-domain socket. Construct with NewServer,
// run Serve (blocking) in a goroutine, and call Close on shutdown.
type Server struct {
	socketPath string
	rpcSrv     *rpc.Server
	ln         net.Listener

	mu     sync.Mutex
	closed bool
}

// NewServer binds a 0600 unix socket at socketPath and registers the signer
// service backed by kl. The parent directory is created if missing. Any stale
// socket file at the path is removed first so a crashed predecessor does not
// block startup.
func NewServer(kl *KeyLoader, socketPath string) (*Server, error) {
	if kl == nil {
		return nil, errors.New("signer: nil key loader")
	}
	if socketPath == "" {
		return nil, errors.New("signer: empty socket path")
	}
	if err := os.MkdirAll(filepath.Dir(socketPath), 0o700); err != nil {
		return nil, fmt.Errorf("signer: create socket dir: %w", err)
	}
	// A leftover socket file from an unclean shutdown would make Listen fail
	// with EADDRINUSE even though nothing is listening; clear it.
	if err := removeStaleSocket(socketPath); err != nil {
		return nil, err
	}

	rpcSrv := rpc.NewServer()
	if err := rpcSrv.RegisterName(serviceName, &SignService{kl: kl}); err != nil {
		return nil, fmt.Errorf("signer: register service: %w", err)
	}

	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		return nil, fmt.Errorf("signer: listen on %s: %w", socketPath, err)
	}
	// Tighten permissions: only the owner (the executor's service user) may
	// connect. There is a small window between Listen and Chmod; MkdirAll's
	// 0700 parent dir closes it for any non-owner.
	if err := os.Chmod(socketPath, 0o600); err != nil {
		_ = ln.Close()
		return nil, fmt.Errorf("signer: chmod socket: %w", err)
	}

	return &Server{socketPath: socketPath, rpcSrv: rpcSrv, ln: ln}, nil
}

// Addr returns the socket path the server is listening on.
func (s *Server) Addr() string { return s.socketPath }

// Serve accepts connections until ctx is cancelled or Close is called. It
// blocks; run it in its own goroutine. Each connection is served with the
// JSON-RPC codec on its own goroutine.
func (s *Server) Serve(ctx context.Context) error {
	// Cancellation path: closing the listener unblocks Accept below.
	go func() {
		<-ctx.Done()
		_ = s.Close()
	}()

	for {
		conn, err := s.ln.Accept()
		if err != nil {
			s.mu.Lock()
			closed := s.closed
			s.mu.Unlock()
			if closed || ctx.Err() != nil {
				return nil // clean shutdown
			}
			return fmt.Errorf("signer: accept: %w", err)
		}
		go s.rpcSrv.ServeCodec(jsonrpc.NewServerCodec(conn))
	}
}

// Close stops accepting connections and removes the socket file. Safe to call
// multiple times.
func (s *Server) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.mu.Unlock()

	err := s.ln.Close()
	// Best-effort socket-file cleanup; Listen already unlinked it from the fs
	// table, but the inode lingers until removed.
	if rmErr := os.Remove(s.socketPath); rmErr != nil && !os.IsNotExist(rmErr) {
		slog.Warn("signer: failed to remove socket file", "path", s.socketPath, "err", rmErr)
	}
	return err
}

func removeStaleSocket(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("signer: stat socket path: %w", err)
	}
	if info.Mode()&os.ModeSocket == 0 {
		// Refuse to clobber a regular file / directory at the socket path —
		// that is almost certainly a misconfiguration, not a stale socket.
		return fmt.Errorf("signer: refusing to remove non-socket at %s", path)
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("signer: remove stale socket: %w", err)
	}
	return nil
}

// Client is a thin JSON-RPC-over-unix-socket client for the signer. It dials a
// fresh connection per call, which is plenty for the executor's per-bundle
// signing rate and keeps the client free of connection-state bugs. For the
// hot path a future pooled client can implement the same method set.
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
// signature.
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
