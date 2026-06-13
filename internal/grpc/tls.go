package grpc

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"log/slog"
	"net"
	"os"
	"strings"

	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
)

// DialOptions holds optional TLS material for gRPC dial.
type DialOptions struct {
	TLSCertFile string
	TLSKeyFile  string
	TLSCAFile   string
}

// LoadDialOptionsFromEnv reads GRPC_TLS_CERT, GRPC_TLS_KEY, GRPC_TLS_CA.
func LoadDialOptionsFromEnv() DialOptions {
	return DialOptions{
		TLSCertFile: strings.TrimSpace(os.Getenv("GRPC_TLS_CERT")),
		TLSKeyFile:  strings.TrimSpace(os.Getenv("GRPC_TLS_KEY")),
		TLSCAFile:   strings.TrimSpace(os.Getenv("GRPC_TLS_CA")),
	}
}

func isUnixAddress(addr string) bool {
	return strings.HasPrefix(strings.TrimSpace(addr), "unix://")
}

func isTCPAddress(addr string) bool {
	addr = strings.TrimSpace(addr)
	if isUnixAddress(addr) || strings.Contains(addr, "://") {
		return false
	}
	_, _, err := net.SplitHostPort(addr)
	return err == nil
}

func allowInsecureTCP() bool {
	v := strings.TrimSpace(os.Getenv("ALLOW_INSECURE_TCP"))
	return v == "1" || strings.EqualFold(v, "true")
}

func buildTransportCredentials(addr string, opts DialOptions) (credentials.TransportCredentials, error) {
	if isUnixAddress(addr) {
		return insecure.NewCredentials(), nil
	}
	if !isTCPAddress(addr) {
		return insecure.NewCredentials(), nil
	}
	if opts.TLSCertFile != "" || opts.TLSKeyFile != "" || opts.TLSCAFile != "" {
		tlsCfg, err := loadClientTLSConfig(opts)
		if err != nil {
			return nil, err
		}
		return credentials.NewTLS(tlsCfg), nil
	}
	if !allowInsecureTCP() {
		return nil, fmt.Errorf("insecure TCP gRPC to %s blocked — set ALLOW_INSECURE_TCP=true for dev or configure GRPC_TLS_* for mTLS", addr)
	}
	slog.Warn("using insecure gRPC over TCP — not recommended for production", "addr", addr)
	return insecure.NewCredentials(), nil
}

func loadClientTLSConfig(opts DialOptions) (*tls.Config, error) {
	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12}
	if opts.TLSCAFile != "" {
		caPEM, err := os.ReadFile(opts.TLSCAFile)
		if err != nil {
			return nil, fmt.Errorf("read ca: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(caPEM) {
			return nil, fmt.Errorf("invalid ca pem")
		}
		tlsCfg.RootCAs = pool
	}
	if opts.TLSCertFile != "" && opts.TLSKeyFile != "" {
		cert, err := tls.LoadX509KeyPair(opts.TLSCertFile, opts.TLSKeyFile)
		if err != nil {
			return nil, fmt.Errorf("load client cert: %w", err)
		}
		tlsCfg.Certificates = []tls.Certificate{cert}
	}
	return tlsCfg, nil
}
