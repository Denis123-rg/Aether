package grpc

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeTestCertFiles(t *testing.T) (certFile, keyFile, caFile string) {
	t.Helper()
	dir := t.TempDir()
	certFile = filepath.Join(dir, "cert.pem")
	keyFile = filepath.Join(dir, "key.pem")
	caFile = filepath.Join(dir, "ca.pem")

	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "aether-test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &priv.PublicKey, priv)
	if err != nil {
		t.Fatal(err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if err := os.WriteFile(certFile, certPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(caFile, certPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	if err := os.WriteFile(keyFile, keyPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	return certFile, keyFile, caFile
}

func TestLoadClientTLSConfig_WithCAAndCert(t *testing.T) {
	certFile, keyFile, caFile := writeTestCertFiles(t)
	cfg, err := loadClientTLSConfig(DialOptions{
		TLSCertFile: certFile,
		TLSKeyFile:  keyFile,
		TLSCAFile:   caFile,
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg == nil || len(cfg.Certificates) != 1 || cfg.RootCAs == nil {
		t.Fatal("incomplete tls config")
	}
}

func TestLoadClientTLSConfig_CAOnly(t *testing.T) {
	_, _, caFile := writeTestCertFiles(t)
	cfg, err := loadClientTLSConfig(DialOptions{TLSCAFile: caFile})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.RootCAs == nil {
		t.Fatal("expected root CA pool")
	}
}

func TestLoadClientTLSConfig_InvalidCA(t *testing.T) {
	dir := t.TempDir()
	badCA := filepath.Join(dir, "bad.pem")
	if err := os.WriteFile(badCA, []byte("not-a-cert"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := loadClientTLSConfig(DialOptions{TLSCAFile: badCA})
	if err == nil {
		t.Fatal("expected invalid ca error")
	}
}

func TestLoadClientTLSConfig_MissingCAFile(t *testing.T) {
	_, err := loadClientTLSConfig(DialOptions{TLSCAFile: "/nonexistent/ca.pem"})
	if err == nil {
		t.Fatal("expected read error")
	}
}

func TestLoadClientTLSConfig_BadKeyPair(t *testing.T) {
	dir := t.TempDir()
	cert := filepath.Join(dir, "c.pem")
	key := filepath.Join(dir, "k.pem")
	if err := os.WriteFile(cert, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(key, []byte("y"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := loadClientTLSConfig(DialOptions{TLSCertFile: cert, TLSKeyFile: key})
	if err == nil {
		t.Fatal("expected load cert error")
	}
}

func TestBuildTransportCredentials_WithTLSFiles(t *testing.T) {
	certFile, keyFile, caFile := writeTestCertFiles(t)
	creds, err := buildTransportCredentials("127.0.0.1:50051", DialOptions{
		TLSCertFile: certFile,
		TLSKeyFile:  keyFile,
		TLSCAFile:   caFile,
	})
	if err != nil {
		t.Fatal(err)
	}
	if creds == nil {
		t.Fatal("nil creds")
	}
}

func TestBuildTransportCredentials_NonTCPUnknownScheme(t *testing.T) {
	creds, err := buildTransportCredentials("grpc://weird", DialOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if creds == nil {
		t.Fatal("nil creds")
	}
}

func TestIsTCPAddress_Invalid(t *testing.T) {
	if isTCPAddress("not-a-hostport") {
		t.Fatal("invalid hostport should not be tcp")
	}
}

func TestAllowInsecureTCP_FalseVariants(t *testing.T) {
	t.Setenv("ALLOW_INSECURE_TCP", "false")
	if allowInsecureTCP() {
		t.Fatal("expected false")
	}
}

func TestLoadDialOptionsFromEnv_AllFields(t *testing.T) {
	t.Setenv("GRPC_TLS_CERT", "/c.pem")
	t.Setenv("GRPC_TLS_KEY", "/k.pem")
	t.Setenv("GRPC_TLS_CA", "/ca.pem")
	opts := LoadDialOptionsFromEnv()
	if opts.TLSCertFile != "/c.pem" || opts.TLSKeyFile != "/k.pem" || opts.TLSCAFile != "/ca.pem" {
		t.Fatalf("opts %v", opts)
	}
}
