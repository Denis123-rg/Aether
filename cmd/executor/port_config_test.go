package main

import (
	"os"
	"testing"
)

func TestLoadAdminPort_Default8080(t *testing.T) {
	os.Unsetenv("ADMIN_HTTP_PORT")
	port, _ := loadAdminPort()
	if port != 8080 {
		t.Fatalf("port %d", port)
	}
}

func TestLoadAdminPort_EnvOverride(t *testing.T) {
	t.Setenv("ADMIN_HTTP_PORT", "8081")
	port, _ := loadAdminPort()
	if port != 8081 {
		t.Fatalf("port %d", port)
	}
}

func TestExecutorDefaultPort_Not8090(t *testing.T) {
	os.Unsetenv("ADMIN_HTTP_PORT")
	port, _ := loadAdminPort()
	if port == 8090 {
		t.Fatal("executor should not default to monitor port")
	}
}
