package server

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"log/slog"
	"net"
	"os"
	"strings"
)

func loadGRPCTLSConfig(certFile, keyFile, clientCAFile string) (*tls.Config, error) {
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("load gRPC TLS key pair: %w", err)
	}

	caPEM, err := os.ReadFile(clientCAFile)
	if err != nil {
		return nil, fmt.Errorf("read gRPC client CA: %w", err)
	}

	clientCAs := x509.NewCertPool()
	if !clientCAs.AppendCertsFromPEM(caPEM) {
		return nil, fmt.Errorf("parse gRPC client CA bundle")
	}

	return &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{cert},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    clientCAs,
	}, nil
}

func parseAllowedCIDRs(raw string) ([]*net.IPNet, error) {
	parts := strings.Split(raw, ",")
	nets := make([]*net.IPNet, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}

		_, network, err := net.ParseCIDR(part)
		if err != nil {
			return nil, fmt.Errorf("parse CIDR %q: %w", part, err)
		}
		nets = append(nets, network)
	}

	if len(nets) == 0 {
		return nil, fmt.Errorf("no allowed CIDRs configured")
	}

	return nets, nil
}

func ipAllowed(ip net.IP, nets []*net.IPNet) bool {
	for _, network := range nets {
		if network.Contains(ip) {
			return true
		}
	}
	return false
}

type allowlistListener struct {
	net.Listener
	allowed []*net.IPNet
	log     *slog.Logger
}

func (l *allowlistListener) Accept() (net.Conn, error) {
	for {
		conn, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}

		addr, ok := conn.RemoteAddr().(*net.TCPAddr)
		if !ok {
			l.log.Warn("rejecting non-TCP gRPC connection", "remote_addr", conn.RemoteAddr().String())
			_ = conn.Close()
			continue
		}

		if ipAllowed(addr.IP, l.allowed) {
			return conn, nil
		}

		l.log.Warn("rejecting gRPC connection from disallowed IP", "remote_ip", addr.IP.String())
		_ = conn.Close()
	}
}
