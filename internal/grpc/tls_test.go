package grpc

import (
	"testing"
)

func TestBuildTransportCredentials_UnixInsecure(t *testing.T) {
	creds, err := buildTransportCredentials("unix:///tmp/sock", DialOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if creds == nil {
		t.Fatal("nil creds")
	}
}

func TestBuildTransportCredentials_TCPBlocked(t *testing.T) {
	t.Setenv("ALLOW_INSECURE_TCP", "")
	_, err := buildTransportCredentials("127.0.0.1:50051", DialOptions{})
	if err == nil {
		t.Fatal("expected error for insecure tcp")
	}
}

func TestBuildTransportCredentials_TCPAllowedDev(t *testing.T) {
	t.Setenv("ALLOW_INSECURE_TCP", "true")
	creds, err := buildTransportCredentials("127.0.0.1:50051", DialOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if creds == nil {
		t.Fatal("nil creds")
	}
}

func TestAllowInsecureTCP(t *testing.T) {
	t.Setenv("ALLOW_INSECURE_TCP", "1")
	if !allowInsecureTCP() {
		t.Fatal("expected true")
	}
}

func TestValidateDialTarget_Unix(t *testing.T) {
	if err := validateDialTarget("unix:///var/run/aether.sock"); err != nil {
		t.Fatal(err)
	}
}

func TestValidateDialTarget_Empty(t *testing.T) {
	if err := validateDialTarget(""); err == nil {
		t.Fatal("expected error")
	}
}

func TestLoadDialOptionsFromEnv(t *testing.T) {
	t.Setenv("GRPC_TLS_CERT", "/tmp/cert.pem")
	opts := LoadDialOptionsFromEnv()
	if opts.TLSCertFile != "/tmp/cert.pem" {
		t.Fatal(opts)
	}
}
