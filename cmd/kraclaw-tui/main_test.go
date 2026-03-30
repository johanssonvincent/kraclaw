package main

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
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	kraclawv1 "github.com/johanssonvincent/kraclaw/pkg/pb/kraclawv1"
)

func generateTestCerts(t *testing.T) (caCertPath, serverCertPath, serverKeyPath, clientCertPath, clientKeyPath string) {
	t.Helper()
	dir := t.TempDir()

	// CA key + cert
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate CA key: %v", err)
	}

	caTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "Test CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}

	caCertDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("create CA cert: %v", err)
	}
	caCert, err := x509.ParseCertificate(caCertDER)
	if err != nil {
		t.Fatalf("parse CA cert: %v", err)
	}

	caCertPath = filepath.Join(dir, "ca.crt")
	writePEM(t, caCertPath, "CERTIFICATE", caCertDER)

	// Server cert
	serverKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate server key: %v", err)
	}

	serverTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "localhost"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
	}

	serverCertDER, err := x509.CreateCertificate(rand.Reader, serverTemplate, caCert, &serverKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("create server cert: %v", err)
	}

	serverCertPath = filepath.Join(dir, "server.crt")
	serverKeyPath = filepath.Join(dir, "server.key")
	writePEM(t, serverCertPath, "CERTIFICATE", serverCertDER)
	writeKeyPEM(t, serverKeyPath, serverKey)

	// Client cert
	clientKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate client key: %v", err)
	}

	clientTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(3),
		Subject:      pkix.Name{CommonName: "test-client"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}

	clientCertDER, err := x509.CreateCertificate(rand.Reader, clientTemplate, caCert, &clientKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("create client cert: %v", err)
	}

	clientCertPath = filepath.Join(dir, "client.crt")
	clientKeyPath = filepath.Join(dir, "client.key")
	writePEM(t, clientCertPath, "CERTIFICATE", clientCertDER)
	writeKeyPEM(t, clientKeyPath, clientKey)

	return
}

func generateWrongCA(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate wrong CA key: %v", err)
	}

	template := &x509.Certificate{
		SerialNumber:          big.NewInt(99),
		Subject:               pkix.Name{CommonName: "Wrong CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}

	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create wrong CA cert: %v", err)
	}

	path := filepath.Join(dir, "wrong-ca.crt")
	writePEM(t, path, "CERTIFICATE", der)
	return path
}

func writePEM(t *testing.T, path, typ string, der []byte) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create %s: %v", path, err)
	}
	defer func() { _ = f.Close() }()
	if err := pem.Encode(f, &pem.Block{Type: typ, Bytes: der}); err != nil {
		t.Fatalf("encode PEM %s: %v", path, err)
	}
}

func writeKeyPEM(t *testing.T, path string, key *ecdsa.PrivateKey) {
	t.Helper()
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	writePEM(t, path, "EC PRIVATE KEY", der)
}

func TestLoadClientTLSConfig(t *testing.T) {
	caCert, _, _, clientCert, clientKey := generateTestCerts(t)

	tests := []struct {
		name       string
		serverAddr string
		caCert     string
		clientCert string
		clientKey  string
		serverName string
		wantErr    string
	}{
		{
			name:       "valid certs",
			serverAddr: "localhost:50051",
			caCert:     caCert,
			clientCert: clientCert,
			clientKey:  clientKey,
		},
		{
			name:       "missing CA file",
			serverAddr: "localhost:50051",
			caCert:     "/nonexistent/ca.crt",
			clientCert: clientCert,
			clientKey:  clientKey,
			wantErr:    "read CA certificate",
		},
		{
			name:       "missing client cert file",
			serverAddr: "localhost:50051",
			caCert:     caCert,
			clientCert: "/nonexistent/client.crt",
			clientKey:  clientKey,
			wantErr:    "load client certificate",
		},
		{
			name:       "empty file paths",
			serverAddr: "localhost:50051",
			caCert:     "",
			clientCert: "",
			clientKey:  "",
			wantErr:    "must all be set",
		},
		{
			name:       "server name override",
			serverAddr: "localhost:50051",
			caCert:     caCert,
			clientCert: clientCert,
			clientKey:  clientKey,
			serverName: "custom.example.com",
		},
		{
			name:       "server name derived from address",
			serverAddr: "myhost.example.com:50051",
			caCert:     caCert,
			clientCert: clientCert,
			clientKey:  clientKey,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := loadClientTLSConfig(tt.serverAddr, tt.caCert, tt.clientCert, tt.clientKey, tt.serverName)

			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error %q does not contain %q", err.Error(), tt.wantErr)
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if cfg.MinVersion != tls.VersionTLS13 {
				t.Fatalf("MinVersion = 0x%04x, want TLS 1.3 (0x0304)", cfg.MinVersion)
			}

			if tt.serverName != "" {
				if cfg.ServerName != tt.serverName {
					t.Fatalf("ServerName = %q, want %q", cfg.ServerName, tt.serverName)
				}
			} else {
				host, _, _ := net.SplitHostPort(tt.serverAddr)
				if cfg.ServerName != host {
					t.Fatalf("ServerName = %q, want %q", cfg.ServerName, host)
				}
			}
		})
	}
}

func TestNewAPIClient_Insecure(t *testing.T) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}

	srv := grpc.NewServer()
	kraclawv1.RegisterAdminServiceServer(srv, &kraclawv1.UnimplementedAdminServiceServer{})
	kraclawv1.RegisterGroupServiceServer(srv, &kraclawv1.UnimplementedGroupServiceServer{})
	kraclawv1.RegisterTaskServiceServer(srv, &kraclawv1.UnimplementedTaskServiceServer{})
	kraclawv1.RegisterSandboxServiceServer(srv, &kraclawv1.UnimplementedSandboxServiceServer{})
	go func() { _ = srv.Serve(lis) }()
	defer srv.Stop()

	client, err := newAPIClient(lis.Addr().String(), "", "", "", "", true)
	if err != nil {
		t.Fatalf("newAPIClient insecure: %v", err)
	}
	defer func() { _ = client.Close() }()

	if client.conn == nil {
		t.Fatal("conn is nil")
	}
	if client.admin == nil {
		t.Fatal("admin client is nil")
	}
	if client.groups == nil {
		t.Fatal("groups client is nil")
	}
	if client.tasks == nil {
		t.Fatal("tasks client is nil")
	}
	if client.sandboxes == nil {
		t.Fatal("sandboxes client is nil")
	}
}

func TestNewAPIClient_TLS(t *testing.T) {
	caCert, serverCert, serverKey, clientCert, clientKey := generateTestCerts(t)

	// Load server TLS with mTLS
	serverTLSCert, err := tls.LoadX509KeyPair(serverCert, serverKey)
	if err != nil {
		t.Fatalf("load server key pair: %v", err)
	}

	caPEM, err := os.ReadFile(caCert)
	if err != nil {
		t.Fatalf("read CA cert: %v", err)
	}
	clientCAs := x509.NewCertPool()
	if !clientCAs.AppendCertsFromPEM(caPEM) {
		t.Fatal("failed to add CA cert to pool")
	}

	serverTLSConfig := &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{serverTLSCert},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    clientCAs,
	}

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}

	srv := grpc.NewServer(grpc.Creds(credentials.NewTLS(serverTLSConfig)))
	kraclawv1.RegisterAdminServiceServer(srv, &kraclawv1.UnimplementedAdminServiceServer{})
	kraclawv1.RegisterGroupServiceServer(srv, &kraclawv1.UnimplementedGroupServiceServer{})
	kraclawv1.RegisterTaskServiceServer(srv, &kraclawv1.UnimplementedTaskServiceServer{})
	kraclawv1.RegisterSandboxServiceServer(srv, &kraclawv1.UnimplementedSandboxServiceServer{})
	go func() { _ = srv.Serve(lis) }()
	defer srv.Stop()

	t.Run("correct certs", func(t *testing.T) {
		client, err := newAPIClient(lis.Addr().String(), caCert, clientCert, clientKey, "localhost", false)
		if err != nil {
			t.Fatalf("newAPIClient with correct certs: %v", err)
		}
		_ = client.Close()
	})

	t.Run("wrong CA", func(t *testing.T) {
		wrongCA := generateWrongCA(t)
		_, err := newAPIClient(lis.Addr().String(), wrongCA, clientCert, clientKey, "localhost", false)
		if err == nil {
			t.Fatal("expected error with wrong CA, got nil")
		}
	})
}
