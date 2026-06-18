package grpc

import (
	"testing"
)

func TestValidateDialTarget_Empty_Coverage(t *testing.T) {
	err := validateDialTarget("")
	if err == nil {
		t.Error("expected error for empty address")
	}
}

func TestValidateDialTarget_UnixNoPath_Coverage(t *testing.T) {
	err := validateDialTarget("unix://")
	if err == nil {
		t.Error("expected error for unix with no path")
	}
}

func TestValidateDialTarget_UnixWithPath_Coverage(t *testing.T) {
	err := validateDialTarget("unix:///var/run/engine.sock")
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestValidateDialTarget_UnsupportedScheme_Coverage(t *testing.T) {
	err := validateDialTarget("http://localhost:50051")
	if err == nil {
		t.Error("expected error for unsupported scheme")
	}
}

func TestValidateDialTarget_ValidTCP_Coverage(t *testing.T) {
	err := validateDialTarget("localhost:50051")
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestValidateDialTarget_InvalidHostPort_Coverage(t *testing.T) {
	err := validateDialTarget("not-a-host-port")
	if err == nil {
		t.Error("expected error for invalid host:port")
	}
}

func TestValidateDialTarget_EmptyHost_Coverage(t *testing.T) {
	err := validateDialTarget(":50051")
	if err == nil {
		t.Error("expected error for empty host")
	}
}

func TestValidateDialTarget_EmptyPort_Coverage(t *testing.T) {
	err := validateDialTarget("localhost:")
	if err == nil {
		t.Error("expected error for empty port")
	}
}

func TestValidateDialTarget_Whitespace_Coverage(t *testing.T) {
	err := validateDialTarget("  localhost:50051  ")
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestNewClientFromConn_Nil_Coverage(t *testing.T) {
	_, err := NewClientFromConn(nil)
	if err == nil {
		t.Error("expected error for nil conn")
	}
}

func TestIsUnixAddress_Coverage(t *testing.T) {
	tests := []struct {
		addr   string
		expect bool
	}{
		{"unix:///var/run/engine.sock", true},
		{"unix://engine.sock", true},
		{"localhost:50051", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := isUnixAddress(tt.addr); got != tt.expect {
			t.Errorf("isUnixAddress(%q) = %v, want %v", tt.addr, got, tt.expect)
		}
	}
}

func TestIsTCPAddress_Coverage(t *testing.T) {
	tests := []struct {
		addr   string
		expect bool
	}{
		{"localhost:50051", true},
		{"127.0.0.1:50051", true},
		{"unix:///path", false},
		{"http://localhost", false},
	}
	for _, tt := range tests {
		if got := isTCPAddress(tt.addr); got != tt.expect {
			t.Errorf("isTCPAddress(%q) = %v, want %v", tt.addr, got, tt.expect)
		}
	}
}

func TestAllowInsecureTCP_Coverage(t *testing.T) {
	t.Setenv("ALLOW_INSECURE_TCP", "1")
	if !allowInsecureTCP() {
		t.Error("expected true")
	}

	t.Setenv("ALLOW_INSECURE_TCP", "true")
	if !allowInsecureTCP() {
		t.Error("expected true")
	}

	t.Setenv("ALLOW_INSECURE_TCP", "TRUE")
	if !allowInsecureTCP() {
		t.Error("expected true")
	}

	t.Setenv("ALLOW_INSECURE_TCP", "0")
	if allowInsecureTCP() {
		t.Error("expected false")
	}

	t.Setenv("ALLOW_INSECURE_TCP", "")
	if allowInsecureTCP() {
		t.Error("expected false")
	}
}

func TestBuildTransportCredentials_UnixAddress_Coverage(t *testing.T) {
	creds, err := buildTransportCredentials("unix:///var/run/engine.sock", DialOptions{})
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if creds == nil {
		t.Error("expected non-nil credentials")
	}
}

func TestBuildTransportCredentials_InvalidAddress_Coverage(t *testing.T) {
	creds, err := buildTransportCredentials("http://localhost", DialOptions{})
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if creds == nil {
		t.Error("expected non-nil credentials for invalid address")
	}
}

func TestBuildTransportCredentials_TCPInsecureBlocked_Coverage(t *testing.T) {
	t.Setenv("ALLOW_INSECURE_TCP", "false")
	_, err := buildTransportCredentials("localhost:50051", DialOptions{})
	if err == nil {
		t.Error("expected error for insecure TCP blocked")
	}
}

func TestBuildTransportCredentials_TCPInsecureAllowed_Coverage(t *testing.T) {
	t.Setenv("ALLOW_INSECURE_TCP", "true")
	creds, err := buildTransportCredentials("localhost:50051", DialOptions{})
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if creds == nil {
		t.Error("expected non-nil credentials")
	}
}

func TestLoadDialOptionsFromEnv_Coverage(t *testing.T) {
	t.Setenv("GRPC_TLS_CERT", "/path/cert.pem")
	t.Setenv("GRPC_TLS_KEY", "/path/key.pem")
	t.Setenv("GRPC_TLS_CA", "/path/ca.pem")

	opts := LoadDialOptionsFromEnv()
	if opts.TLSCertFile != "/path/cert.pem" {
		t.Errorf("expected cert, got %s", opts.TLSCertFile)
	}
	if opts.TLSKeyFile != "/path/key.pem" {
		t.Errorf("expected key, got %s", opts.TLSKeyFile)
	}
	if opts.TLSCAFile != "/path/ca.pem" {
		t.Errorf("expected CA, got %s", opts.TLSCAFile)
	}
}

func TestLoadClientTLSConfig_NoFiles_Coverage(t *testing.T) {
	opts := DialOptions{}
	cfg, err := loadClientTLSConfig(opts)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if cfg == nil {
		t.Error("expected non-nil config")
	}
}
