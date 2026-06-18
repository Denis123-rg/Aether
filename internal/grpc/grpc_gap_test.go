package grpc

import (
	"os"
	"testing"
)

func TestValidateDialTarget(t *testing.T) {
	// Valid unix address
	if err := validateDialTarget("unix:///tmp/test.sock"); err != nil {
		t.Fatalf("valid unix address: %v", err)
	}

	// Valid TCP address
	if err := validateDialTarget("localhost:50051"); err != nil {
		t.Fatalf("valid tcp address: %v", err)
	}

	// Empty address
	if err := validateDialTarget(""); err == nil {
		t.Fatal("expected error for empty address")
	}

	// Unix address missing path
	if err := validateDialTarget("unix://"); err == nil {
		t.Fatal("expected error for unix address missing path")
	}

	// Unsupported scheme
	if err := validateDialTarget("http://localhost:50051"); err == nil {
		t.Fatal("expected error for unsupported scheme")
	}

	// Invalid host:port
	if err := validateDialTarget("not_valid"); err == nil {
		t.Fatal("expected error for invalid host:port")
	}

	// Missing port
	if err := validateDialTarget("localhost"); err == nil {
		t.Fatal("expected error for missing port")
	}
}

func TestDialWithOptions_InvalidTarget(t *testing.T) {
	_, err := DialWithOptions("", DialOptions{})
	if err == nil {
		t.Fatal("expected error for empty dial target")
	}
}

func TestDialWithOptions_UnsupportedScheme(t *testing.T) {
	_, err := DialWithOptions("http://localhost:50051", DialOptions{})
	if err == nil {
		t.Fatal("expected error for unsupported scheme")
	}
}

func TestNewClientFromConn_NilConn(t *testing.T) {
	_, err := NewClientFromConn(nil)
	if err == nil {
		t.Fatal("expected error for nil connection")
	}
}

func TestBuildTransportCredentials_TLSFileError(t *testing.T) {
	// Create temp files that exist but contain invalid cert data
	tmpDir := t.TempDir()
	certPath := tmpDir + "/cert.pem"
	keyPath := tmpDir + "/key.pem"
	caPath := tmpDir + "/ca.pem"

	// Write empty/invalid files
	for _, path := range []string{certPath, keyPath, caPath} {
		f, err := os.Create(path)
		if err != nil {
			t.Fatalf("create temp file: %v", err)
		}
		f.Close()
	}

	_, err := buildTransportCredentials("localhost:50051", DialOptions{
		TLSCertFile: certPath,
		TLSKeyFile:  keyPath,
		TLSCAFile:   caPath,
	})
	if err == nil {
		t.Fatal("expected error for invalid TLS files")
	}
}

func TestBuildTransportCredentials_TLSReadError(t *testing.T) {
	_, err := buildTransportCredentials("localhost:50051", DialOptions{
		TLSCertFile: "/nonexistent/cert.pem",
		TLSKeyFile:  "/nonexistent/key.pem",
	})
	if err == nil {
		t.Fatal("expected error for missing TLS files")
	}
}

func TestIsUnixAddress(t *testing.T) {
	if !isUnixAddress("unix:///tmp/test.sock") {
		t.Fatal("expected unix address to be detected")
	}
	if isUnixAddress("localhost:50051") {
		t.Fatal("expected tcp address not to be detected as unix")
	}
}

func TestIsTCPAddress(t *testing.T) {
	if !isTCPAddress("localhost:50051") {
		t.Fatal("expected tcp address to be detected")
	}
	if isTCPAddress("unix:///tmp/test.sock") {
		t.Fatal("expected unix address not to be detected as tcp")
	}
	if isTCPAddress("http://localhost:50051") {
		t.Fatal("expected scheme address not to be detected as tcp")
	}
}
