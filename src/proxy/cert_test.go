package proxy

import (
	"crypto/x509"
	"os"
	"path/filepath"
	"testing"
)

func TestGenerateRootCA(t *testing.T) {
	cert, key, err := generateRootCA()
	if err != nil {
		t.Fatalf("generateRootCA() error = %v", err)
	}

	if cert == nil {
		t.Fatal("Certificate should not be nil")
	}

	if key == nil {
		t.Fatal("Private key should not be nil")
	}

	// Verify certificate properties
	if !cert.IsCA {
		t.Error("Certificate should be marked as CA")
	}

	if len(cert.Subject.Organization) == 0 {
		t.Error("Certificate should have organization")
	}
}

func TestLoadOrCreateRootCA(t *testing.T) {
	// Use a temporary directory for testing
	tmpDir := t.TempDir()
	originalHome := os.Getenv("HOME")

	// Temporarily set HOME to tmpDir
	os.Setenv("HOME", tmpDir)
	defer func() {
		if originalHome != "" {
			os.Setenv("HOME", originalHome)
		} else {
			os.Unsetenv("HOME")
		}
	}()

	// First call should create
	cert1, key1, err := loadOrCreateRootCA()
	if err != nil {
		t.Fatalf("loadOrCreateRootCA() error = %v", err)
	}

	if cert1 == nil || key1 == nil {
		t.Fatal("First call should return valid cert and key")
	}

	// Check files were created
	certPath := filepath.Join(tmpDir, certDir, certFile)
	keyPath := filepath.Join(tmpDir, certDir, keyFile)

	if _, err := os.Stat(certPath); err != nil {
		t.Errorf("Certificate file was not created: %v", err)
	}

	if _, err := os.Stat(keyPath); err != nil {
		t.Errorf("Key file was not created: %v", err)
	}

	// Second call should load existing
	cert2, _, err := loadOrCreateRootCA()
	if err != nil {
		t.Fatalf("loadOrCreateRootCA() second call error = %v", err)
	}

	// Verify it's the same certificate (same serial number)
	if cert1.SerialNumber.Cmp(cert2.SerialNumber) != 0 {
		t.Error("Second call should return the same certificate")
	}
}

func TestGenerateCertificate(t *testing.T) {
	// Use a temporary directory for testing
	tmpDir := t.TempDir()
	originalHome := os.Getenv("HOME")

	os.Setenv("HOME", tmpDir)
	defer func() {
		if originalHome != "" {
			os.Setenv("HOME", originalHome)
		} else {
			os.Unsetenv("HOME")
		}
	}()

	host := "example.com"
	cert, err := generateCertificate(host)
	if err != nil {
		t.Fatalf("generateCertificate() error = %v", err)
	}

	if cert == nil {
		t.Fatal("Certificate should not be nil")
	}

	// Verify certificate
	if len(cert.Certificate) == 0 {
		t.Fatal("Certificate should have certificate data")
	}

	// Parse certificate
	parsedCert, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		t.Fatalf("Failed to parse certificate: %v", err)
	}

	// Check DNS names
	if len(parsedCert.DNSNames) == 0 || parsedCert.DNSNames[0] != host {
		t.Errorf("DNSNames = %v, want [%q]", parsedCert.DNSNames, host)
	}

	// Verify it's signed by our CA
	if parsedCert.Issuer.Organization == nil {
		t.Error("Certificate should have issuer organization")
	}
}

func TestGenerateCertificateMultipleHosts(t *testing.T) {
	// Use a temporary directory for testing
	tmpDir := t.TempDir()
	originalHome := os.Getenv("HOME")

	os.Setenv("HOME", tmpDir)
	defer func() {
		if originalHome != "" {
			os.Setenv("HOME", originalHome)
		} else {
			os.Unsetenv("HOME")
		}
	}()

	hosts := []string{"example.com", "test.com", "cdn.example.com"}

	for _, host := range hosts {
		cert, err := generateCertificate(host)
		if err != nil {
			t.Fatalf("generateCertificate(%q) error = %v", host, err)
		}

		if cert == nil {
			t.Fatalf("Certificate for %q should not be nil", host)
		}

		// Parse and verify
		parsedCert, err := x509.ParseCertificate(cert.Certificate[0])
		if err != nil {
			t.Fatalf("Failed to parse certificate for %q: %v", host, err)
		}

		if len(parsedCert.DNSNames) == 0 || parsedCert.DNSNames[0] != host {
			t.Errorf("Certificate for %q has wrong DNS name: %v", host, parsedCert.DNSNames)
		}
	}
}
