package proxy

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"time"
)

const (
	certDir  = ".mitmproxy"
	certFile = "mitmproxy-ca-cert.pem"
	keyFile  = "mitmproxy-ca-key.pem"
)

// generateRootCA generates a root CA certificate for MITM
func generateRootCA() (*x509.Certificate, *rsa.PrivateKey, error) {
	// Create private key
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, nil, err
	}

	// Create certificate template
	template := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject: pkix.Name{
			Organization:  []string{"MitmCDN Proxy"},
			Country:       []string{"US"},
			Province:      []string{""},
			Locality:      []string{""},
			StreetAddress: []string{""},
			PostalCode:    []string{""},
		},
		NotBefore:    time.Now(),
		NotAfter:     time.Now().AddDate(10, 0, 0), // Valid for 10 years
		KeyUsage:     x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLen:            0,
		MaxPathLenZero:        true,
	}

	// Create certificate
	certDER, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		return nil, nil, err
	}

	cert, err := x509.ParseCertificate(certDER)
	if err != nil {
		return nil, nil, err
	}

	return cert, key, nil
}

// loadOrCreateRootCA loads existing root CA or creates a new one
func loadOrCreateRootCA() (*x509.Certificate, *rsa.PrivateKey, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, nil, err
	}

	certPath := filepath.Join(homeDir, certDir, certFile)
	keyPath := filepath.Join(homeDir, certDir, keyFile)

	// Try to load existing CA
	if certData, err := os.ReadFile(certPath); err == nil {
		if keyData, err := os.ReadFile(keyPath); err == nil {
			certBlock, _ := pem.Decode(certData)
			keyBlock, _ := pem.Decode(keyData)

			if certBlock != nil && keyBlock != nil {
				cert, err := x509.ParseCertificate(certBlock.Bytes)
				if err == nil {
					key, err := x509.ParsePKCS1PrivateKey(keyBlock.Bytes)
					if err == nil {
						return cert, key, nil
					}
				}
			}
		}
	}

	// Generate new CA
	cert, key, err := generateRootCA()
	if err != nil {
		return nil, nil, err
	}

	// Save CA certificate and key
	if err := os.MkdirAll(filepath.Dir(certPath), 0755); err != nil {
		return nil, nil, err
	}

	// Save certificate
	certPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: cert.Raw,
	})
	if err := os.WriteFile(certPath, certPEM, 0644); err != nil {
		return nil, nil, err
	}

	// Save private key
	keyPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	})
	if err := os.WriteFile(keyPath, keyPEM, 0600); err != nil {
		return nil, nil, err
	}

	fmt.Printf("Root CA certificate generated at: %s\n", certPath)
	fmt.Printf("Please install this certificate in your system/browser to trust MITM connections.\n")

	return cert, key, nil
}

// GenerateCertificate generates a certificate for a specific host signed by the root CA
// This is the public API for certificate generation
func GenerateCertificate(host string) (*tls.Certificate, error) {
	return generateCertificate(host)
}

// generateCertificate generates a certificate for a specific host signed by the root CA
func generateCertificate(host string) (*tls.Certificate, error) {
	// Load or create root CA
	caCert, caKey, err := loadOrCreateRootCA()
	if err != nil {
		return nil, fmt.Errorf("failed to load root CA: %w", err)
	}

	// Create private key for this host
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, err
	}

	// Create certificate template
	template := x509.Certificate{
		SerialNumber: big.NewInt(time.Now().Unix()),
		Subject: pkix.Name{
			CommonName:   host,
			Organization: []string{"MitmCDN Proxy"},
		},
		NotBefore:    time.Now(),
		NotAfter:     time.Now().AddDate(1, 0, 0), // Valid for 1 year
		KeyUsage:     x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:             []string{host},
	}

	// Create certificate signed by CA
	certDER, err := x509.CreateCertificate(rand.Reader, &template, caCert, &key.PublicKey, caKey)
	if err != nil {
		return nil, err
	}

	// Create TLS certificate
	tlsCert := tls.Certificate{
		Certificate: [][]byte{certDER},
		PrivateKey:  key,
	}

	return &tlsCert, nil
}
