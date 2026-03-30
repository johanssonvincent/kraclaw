package server

import (
	"log/slog"
	"net"
	"testing"
	"time"
)

func TestParseAllowedCIDRs(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		wantLen int
		wantErr bool
	}{
		{name: "single CIDR", raw: "10.0.0.0/8", wantLen: 1},
		{name: "multiple CIDRs", raw: "10.42.0.0/16, 10.43.0.0/16, fd00::/8", wantLen: 3},
		{name: "invalid CIDR", raw: "not-a-cidr", wantErr: true},
		{name: "empty", raw: "", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseAllowedCIDRs(tt.raw)
			if (err != nil) != tt.wantErr {
				t.Fatalf("parseAllowedCIDRs() error = %v, wantErr %v", err, tt.wantErr)
			}
			if len(got) != tt.wantLen {
				t.Fatalf("parseAllowedCIDRs() len = %d, want %d", len(got), tt.wantLen)
			}
		})
	}
}

func TestIPAllowed(t *testing.T) {
	nets, err := parseAllowedCIDRs("10.0.0.0/8,fd00::/8")
	if err != nil {
		t.Fatalf("parseAllowedCIDRs() error = %v", err)
	}

	tests := []struct {
		name string
		ip   string
		want bool
	}{
		{name: "allowed IPv4", ip: "10.42.1.7", want: true},
		{name: "blocked IPv4", ip: "8.8.8.8", want: false},
		{name: "allowed IPv6", ip: "fd12::1", want: true},
		{name: "blocked IPv6", ip: "2001:4860:4860::8888", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ip := net.ParseIP(tt.ip)
			if ip == nil {
				t.Fatalf("ParseIP(%q) returned nil", tt.ip)
			}
			if got := ipAllowed(ip, nets); got != tt.want {
				t.Fatalf("ipAllowed(%q) = %v, want %v", tt.ip, got, tt.want)
			}
		})
	}
}

func TestIPAllowed_IPv6Loopback(t *testing.T) {
	nets, err := parseAllowedCIDRs("10.0.0.0/8,::1/128")
	if err != nil {
		t.Fatalf("parseAllowedCIDRs() error = %v", err)
	}

	ip := net.ParseIP("::1")
	if ip == nil {
		t.Fatal("ParseIP(\"::1\") returned nil")
	}
	if !ipAllowed(ip, nets) {
		t.Fatal("ipAllowed(\"::1\") = false, want true")
	}
}

func TestAllowlistListener_AcceptsAllowed(t *testing.T) {
	inner, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer func() { _ = inner.Close() }()

	nets, err := parseAllowedCIDRs("127.0.0.0/8")
	if err != nil {
		t.Fatalf("parseAllowedCIDRs: %v", err)
	}

	ln := &allowlistListener{
		Listener: inner,
		allowed:  nets,
		log:      slog.Default(),
	}

	accepted := make(chan net.Conn, 1)
	go func() {
		conn, err := ln.Accept()
		if err == nil {
			accepted <- conn
		}
	}()

	conn, err := net.DialTimeout("tcp", inner.Addr().String(), 2*time.Second)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer func() { _ = conn.Close() }()

	select {
	case srv := <-accepted:
		_ = srv.Close()
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for accepted connection")
	}
}

func TestAllowlistListener_RejectsDisallowed(t *testing.T) {
	inner, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer func() { _ = inner.Close() }()

	nets, err := parseAllowedCIDRs("10.0.0.0/8")
	if err != nil {
		t.Fatalf("parseAllowedCIDRs: %v", err)
	}

	ln := &allowlistListener{
		Listener: inner,
		allowed:  nets,
		log:      slog.Default(),
	}

	// Start Accept in background — it will reject connections from 127.0.0.1
	go func() {
		ln.Accept() //nolint:errcheck
	}()

	conn, err := net.DialTimeout("tcp", inner.Addr().String(), 2*time.Second)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}

	// The server should close the connection; reading should fail
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 1)
	_, err = conn.Read(buf)
	if err == nil {
		t.Fatal("expected read error on rejected connection, got nil")
	}
	_ = conn.Close()
}
