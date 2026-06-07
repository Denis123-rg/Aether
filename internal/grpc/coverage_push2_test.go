package grpc

import (
	"strings"
	"testing"
)

func TestDial_Table(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		addr    string
		wantSub string
	}{
		{name: "empty unix path", addr: "unix://", wantSub: "invalid dial target"},
		{name: "whitespace only", addr: "   ", wantSub: "invalid dial target"},
		{name: "unsupported scheme", addr: "ftp://host:1", wantSub: "invalid dial target"},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := Dial(tc.addr)
			if err == nil {
				t.Fatal("expected error")
			}
			if tc.wantSub != "" && !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("err = %q, want substring %q", err, tc.wantSub)
			}
		})
	}
}
