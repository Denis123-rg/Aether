package grpc

import (
	"strings"
	"testing"
)

func TestValidateDialTarget_Table(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		addr    string
		wantErr bool
		substr  string
	}{
		{name: "valid tcp", addr: "127.0.0.1:50051", wantErr: false},
		{name: "valid unix", addr: "unix:///var/run/aether.sock", wantErr: false},
		{name: "empty", addr: "", wantErr: true, substr: "empty"},
		{name: "whitespace", addr: "  ", wantErr: true},
		{name: "unix missing path", addr: "unix://", wantErr: true},
		{name: "bad scheme", addr: "ftp://host:1", wantErr: true, substr: "unsupported"},
		{name: "missing port", addr: "localhost", wantErr: true},
		{name: "empty host", addr: ":50051", wantErr: true},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := validateDialTarget(tc.addr)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				if tc.substr != "" && !strings.Contains(err.Error(), tc.substr) {
					t.Fatalf("err = %q", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestNewClientFromConn_Nil(t *testing.T) {
	_, err := NewClientFromConn(nil)
	if err == nil {
		t.Fatal("expected error for nil conn")
	}
}
