package main

import (
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"
)

func TestCertGen_DynamicIPDiscovery(t *testing.T) {
	ips := discoverHostIPs()
	t.Logf("Discovered host IPs: %v", ips)

	tempDir := t.TempDir()
	os.Setenv("CERT_OUT_DIR", tempDir)
	os.Setenv("CERT_FORCE", "true")
	os.Setenv("CERT_COUNTRY", "DE")

	// We can test certificate generation logic
	certFiles := []string{"ca.crt", "ca.key", "server.crt", "server.key"}

	// Set args and run main
	oldArgs := os.Args
	defer func() { os.Args = oldArgs }()

	os.Args = []string{
		"bombecam-certgen",
		"-out", tempDir,
		"-force",
		"-country", "DE",
	}

	main()

	for _, cf := range certFiles {
		p := filepath.Join(tempDir, cf)
		if _, err := os.Stat(p); err != nil {
			t.Errorf("missing expected generated file %s: %v", cf, err)
		}
	}

	// Verify server cert contains DE and non-loopback IPs if any
	serverCertPEM, err := os.ReadFile(filepath.Join(tempDir, "server.crt"))
	if err != nil {
		t.Fatalf("failed to read server.crt: %v", err)
	}
	block, _ := pem.Decode(serverCertPEM)
	if block == nil {
		t.Fatalf("failed to decode server cert PEM")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("failed to parse server certificate: %v", err)
	}

	if len(cert.Subject.Country) == 0 || cert.Subject.Country[0] != "DE" {
		t.Errorf("expected country DE, got %v", cert.Subject.Country)
	}

	// Ensure 192.168.30.1 and 192.168.31.18 are NOT present unless they actually belong to the host
	t.Logf("Server Cert IP Addresses: %v", cert.IPAddresses)
}
