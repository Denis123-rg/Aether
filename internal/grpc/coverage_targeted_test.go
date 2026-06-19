package grpc

import (
	"testing"

	"google.golang.org/grpc/credentials/insecure"
)

func TestNewTestDialWithOptions_InvalidTarget(t *testing.T) {
	tests := []struct {
		name string
		addr string
	}{
		{"empty", ""},
		{"unsupported scheme", "http://localhost:50051"},
		{"unix no path", "unix://"},
		{"no host", ":50051"},
		{"no port", "localhost:"},
		{"garbage", "not-a-host-port"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := DialWithOptions(tc.addr, DialOptions{})
			if err == nil {
				t.Error("expected error for invalid target")
			}
		})
	}
}

func TestNewTestDialWithOptions_ValidTCP(t *testing.T) {
	t.Setenv("ALLOW_INSECURE_TCP", "true")
	c, err := DialWithOptions("localhost:50051", DialOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	c.Close()
}

func TestNewTestDialWithOptions_UnixAddress(t *testing.T) {
	c, err := DialWithOptions("unix:///var/run/test.sock", DialOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	c.Close()
}

func TestNewTestDialWithOptions_InsecureTCPBlocked(t *testing.T) {
	t.Setenv("ALLOW_INSECURE_TCP", "false")
	_, err := DialWithOptions("localhost:50051", DialOptions{})
	if err == nil {
		t.Error("expected error for blocked insecure TCP")
	}
}

func TestNewTestDialWithOptions_TLSFileNotFound(t *testing.T) {
	t.Setenv("ALLOW_INSECURE_TCP", "true")
	_, err := DialWithOptions("localhost:50051", DialOptions{
		TLSCAFile: "/nonexistent/ca.pem",
	})
	if err == nil {
		t.Error("expected error for non-existent CA file")
	}
}

func TestNewTestDialWithOptions_TLSKeyPairNotFound(t *testing.T) {
	t.Setenv("ALLOW_INSECURE_TCP", "true")
	_, err := DialWithOptions("localhost:50051", DialOptions{
		TLSCertFile: "/nonexistent/cert.pem",
		TLSKeyFile:  "/nonexistent/key.pem",
	})
	if err == nil {
		t.Error("expected error for non-existent key pair")
	}
}

func TestNewTestValidateDialTarget_AllPaths(t *testing.T) {
	tests := []struct {
		name    string
		addr    string
		wantErr bool
	}{
		{"empty", "", true},
		{"unix no path", "unix://", true},
		{"unix with path", "unix:///var/run/engine.sock", false},
		{"unsupported http", "http://localhost:50051", true},
		{"unsupported ftp", "ftp://host", true},
		{"valid tcp", "localhost:50051", false},
		{"valid tcp ip", "127.0.0.1:50051", false},
		{"no host", ":50051", true},
		{"no port", "localhost:", true},
		{"garbage", "not-a-host-port", true},
		{"whitespace", "  localhost:50051  ", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateDialTarget(tc.addr)
			if (err != nil) != tc.wantErr {
				t.Errorf("validateDialTarget(%q) err=%v, wantErr=%v", tc.addr, err, tc.wantErr)
			}
		})
	}
}

func TestNewTestNewClientFromConn_Nil(t *testing.T) {
	_, err := NewClientFromConn(nil)
	if err == nil {
		t.Error("expected error for nil conn")
	}
}

func TestNewTestIsUnixAddress(t *testing.T) {
	tests := []struct {
		addr   string
		expect bool
	}{
		{"unix:///path", true},
		{"unix://path", true},
		{"localhost:50051", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := isUnixAddress(tt.addr); got != tt.expect {
			t.Errorf("isUnixAddress(%q) = %v, want %v", tt.addr, got, tt.expect)
		}
	}
}

func TestNewTestIsTCPAddress(t *testing.T) {
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

func TestNewTestBuildTransportCredentials(t *testing.T) {
	t.Run("unix", func(t *testing.T) {
		creds, err := buildTransportCredentials("unix:///var/run/engine.sock", DialOptions{})
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
		if creds == nil {
			t.Error("expected non-nil")
		}
	})

	t.Run("non-tcp-scheme", func(t *testing.T) {
		creds, err := buildTransportCredentials("http://localhost", DialOptions{})
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
		if creds == nil {
			t.Error("expected non-nil")
		}
	})

	t.Run("tcp blocked", func(t *testing.T) {
		t.Setenv("ALLOW_INSECURE_TCP", "false")
		_, err := buildTransportCredentials("localhost:50051", DialOptions{})
		if err == nil {
			t.Error("expected error for blocked insecure TCP")
		}
	})

	t.Run("tcp allowed", func(t *testing.T) {
		t.Setenv("ALLOW_INSECURE_TCP", "true")
		creds, err := buildTransportCredentials("localhost:50051", DialOptions{})
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
		if creds == nil {
			t.Error("expected non-nil")
		}
	})

	t.Run("tls ca not found", func(t *testing.T) {
		_, err := buildTransportCredentials("localhost:50051", DialOptions{
			TLSCAFile: "/nonexistent/ca.pem",
		})
		if err == nil {
			t.Error("expected error for missing CA file")
		}
	})
}

func TestNewTestLoadDialOptionsFromEnv(t *testing.T) {
	t.Setenv("GRPC_TLS_CERT", "/path/cert.pem")
	t.Setenv("GRPC_TLS_KEY", "/path/key.pem")
	t.Setenv("GRPC_TLS_CA", "/path/ca.pem")
	opts := LoadDialOptionsFromEnv()
	if opts.TLSCertFile != "/path/cert.pem" {
		t.Errorf("cert: %q", opts.TLSCertFile)
	}
	if opts.TLSKeyFile != "/path/key.pem" {
		t.Errorf("key: %q", opts.TLSKeyFile)
	}
	if opts.TLSCAFile != "/path/ca.pem" {
		t.Errorf("ca: %q", opts.TLSCAFile)
	}
}

func TestNewTestLoadClientTLSConfig_Empty(t *testing.T) {
	cfg, err := loadClientTLSConfig(DialOptions{})
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if cfg == nil {
		t.Error("expected non-nil config")
	}
}

func TestNewTestAllowInsecureTCP(t *testing.T) {
	t.Setenv("ALLOW_INSECURE_TCP", "1")
	if !allowInsecureTCP() {
		t.Error("expected true for '1'")
	}
	t.Setenv("ALLOW_INSECURE_TCP", "true")
	if !allowInsecureTCP() {
		t.Error("expected true for 'true'")
	}
	t.Setenv("ALLOW_INSECURE_TCP", "TRUE")
	if !allowInsecureTCP() {
		t.Error("expected true for 'TRUE'")
	}
	t.Setenv("ALLOW_INSECURE_TCP", "0")
	if allowInsecureTCP() {
		t.Error("expected false for '0'")
	}
	t.Setenv("ALLOW_INSECURE_TCP", "")
	if allowInsecureTCP() {
		t.Error("expected false for empty")
	}
}

func TestNewTestInsecureCredentials(t *testing.T) {
	creds := insecure.NewCredentials()
	if creds == nil {
		t.Error("expected non-nil insecure credentials")
	}
}

func TestNewTestDial_CallsDialWithOptions(t *testing.T) {
	t.Setenv("ALLOW_INSECURE_TCP", "true")
	c, err := Dial("localhost:50051")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	c.Close()
}
