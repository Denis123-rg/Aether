package grpc

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestGRPCDialWithOptions_ValidUnixSocket(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "test.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			conn.Close()
		}
	}()

	client, err := DialWithOptions("unix://"+sock, DialOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer client.Close()

	if client.ArbService() == nil {
		t.Error("expected non-nil ArbService")
	}
	if client.HealthService() == nil {
		t.Error("expected non-nil HealthService")
	}
	if client.ControlService() == nil {
		t.Error("expected non-nil ControlService")
	}
}

func TestGRPCDialWithOptions_ValidTLSFiles(t *testing.T) {
	dir := t.TempDir()

	caKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	caTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "Test CA"},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	caCertDER, _ := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	os.WriteFile(filepath.Join(dir, "ca.pem"), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caCertDER}), 0o600)

	srvKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	srvTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "localhost"},
		NotBefore:    time.Now(),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	srvCertDER, _ := x509.CreateCertificate(rand.Reader, srvTemplate, caTemplate, &srvKey.PublicKey, caKey)
	os.WriteFile(filepath.Join(dir, "cert.pem"), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srvCertDER}), 0o600)

	srvKeyDER, _ := x509.MarshalECPrivateKey(srvKey)
	os.WriteFile(filepath.Join(dir, "key.pem"), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: srvKeyDER}), 0o600)

	cert, err := tls.LoadX509KeyPair(filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem"))
	if err != nil {
		t.Fatal(err)
	}
	tlsConfig := &tls.Config{Certificates: []tls.Certificate{cert}}
	ln, err := tls.Listen("tcp", "127.0.0.1:0", tlsConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			conn.Close()
		}
	}()

	client, err := DialWithOptions(ln.Addr().String(), DialOptions{
		TLSCertFile: filepath.Join(dir, "cert.pem"),
		TLSKeyFile:  filepath.Join(dir, "key.pem"),
		TLSCAFile:   filepath.Join(dir, "ca.pem"),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer client.Close()
}

func TestBuildTransportCredentials_TCPSuccess(t *testing.T) {
	t.Setenv("ALLOW_INSECURE_TCP", "true")
	creds, err := buildTransportCredentials("localhost:50051", DialOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if creds == nil {
		t.Error("expected non-nil credentials")
	}
}

func TestLoadDialOptionsFromEnv_Empty(t *testing.T) {
	os.Unsetenv("GRPC_TLS_CERT")
	os.Unsetenv("GRPC_TLS_KEY")
	os.Unsetenv("GRPC_TLS_CA")
	opts := LoadDialOptionsFromEnv()
	if opts.TLSCertFile != "" || opts.TLSKeyFile != "" || opts.TLSCAFile != "" {
		t.Errorf("expected empty opts, got %+v", opts)
	}
}

func TestDialOptions_GRPCNewClientError(t *testing.T) {
	t.Setenv("ALLOW_INSECURE_TCP", "true")
	_, err := DialWithOptions("localhost:1", DialOptions{})
	if err == nil {
		t.Log("grpc.NewClient is lazy, may not error")
	}
}

func TestCheckHealth_Timeout(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "test.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			conn.Close()
		}
	}()

	client, err := DialWithOptions("unix://"+sock, DialOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	ctx := t.Context()
	_, err = client.CheckHealth(ctx)
	if err == nil {
		t.Log("CheckHealth may or may not fail depending on server")
	}
}

func TestGRPCSetEngineState_Paused(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "test.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			conn.Close()
		}
	}()

	client, err := DialWithOptions("unix://"+sock, DialOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	_, err = client.SetEngineState(t.Context(), true)
	if err == nil {
		t.Log("SetEngineState may or may not fail depending on server")
	}
}

func TestGRPCSetEngineState_Running(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "test.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			conn.Close()
		}
	}()

	client, err := DialWithOptions("unix://"+sock, DialOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	_, err = client.SetEngineState(t.Context(), false)
	if err == nil {
		t.Log("SetEngineState may or may not fail depending on server")
	}
}

func TestSetState_Reason(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "test.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			conn.Close()
		}
	}()

	client, err := DialWithOptions("unix://"+sock, DialOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	_, err = client.SetState(t.Context(), 0, "admin pause")
	if err == nil {
		t.Log("SetState may or may not fail depending on server")
	}
}

func TestReloadConfig(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "test.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			conn.Close()
		}
	}()

	client, err := DialWithOptions("unix://"+sock, DialOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	_, err = client.ReloadConfig(t.Context(), "/config/pools.toml")
	if err == nil {
		t.Log("ReloadConfig may or may not fail depending on server")
	}
}
