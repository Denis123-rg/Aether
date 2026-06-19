package grpc

import (
	"testing"
)

func TestDialWithOptions_ValidTCPWithInsecure(t *testing.T) {
	t.Setenv("ALLOW_INSECURE_TCP", "true")
	c, err := DialWithOptions("localhost:50051", DialOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	c.Close()
}

func TestDialWithOptions_ValidUnix(t *testing.T) {
	c, err := DialWithOptions("unix:///var/run/test.sock", DialOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	c.Close()
}

func TestDialWithOptions_TLSCAFileNotFound(t *testing.T) {
	t.Setenv("ALLOW_INSECURE_TCP", "true")
	_, err := DialWithOptions("localhost:50051", DialOptions{
		TLSCAFile: "/nonexistent/ca.pem",
	})
	if err == nil {
		t.Error("expected error for non-existent CA file")
	}
}

func TestDialWithOptions_TLSCertKeyNotFound(t *testing.T) {
	t.Setenv("ALLOW_INSECURE_TCP", "true")
	_, err := DialWithOptions("localhost:50051", DialOptions{
		TLSCertFile: "/nonexistent/cert.pem",
		TLSKeyFile:  "/nonexistent/key.pem",
	})
	if err == nil {
		t.Error("expected error for non-existent cert/key pair")
	}
}

func TestDialWithOptions_CAAndCertNotFound(t *testing.T) {
	t.Setenv("ALLOW_INSECURE_TCP", "true")
	_, err := DialWithOptions("localhost:50051", DialOptions{
		TLSCertFile: "/nonexistent/cert.pem",
		TLSKeyFile:  "/nonexistent/key.pem",
		TLSCAFile:   "/nonexistent/ca.pem",
	})
	if err == nil {
		t.Error("expected error for non-existent TLS files")
	}
}

func TestDialWithOptions_CAOnlyNotFound(t *testing.T) {
	t.Setenv("ALLOW_INSECURE_TCP", "true")
	_, err := DialWithOptions("localhost:50051", DialOptions{
		TLSCAFile: "/nonexistent/ca.pem",
	})
	if err == nil {
		t.Error("expected error for non-existent CA file")
	}
}
