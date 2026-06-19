// Package server wires up the QUIC/HTTP3 transport, TLS, and listeners.
package server

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"math/big"
	"net"
	"time"
)

// CertBundle carries the generated TLS cert plus the SHA-256 hash that
// WebTransport clients pin via `serverCertificateHashes` (no CA install needed).
type CertBundle struct {
	TLS        tls.Certificate
	SHA256     []byte // raw 32-byte digest of the DER certificate
	SHA256Hex  string // hex form, handy for logs
	SHA256B64  string // base64 form, what the browser JS consumes
	NotAfter   time.Time
}

// GenerateCert builds a short-lived ECDSA P-256 self-signed certificate.
//
// WebTransport's serverCertificateHashes only accepts ECDSA certs valid for
// no more than 14 days, so we deliberately keep it small and ephemeral.
func GenerateCert(hosts []string) (*CertBundle, error) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate key: %w", err)
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, fmt.Errorf("serial: %w", err)
	}

	notBefore := time.Now().Add(-time.Hour)
	notAfter := time.Now().Add(13 * 24 * time.Hour) // < 14d for cert-hash pinning

	tmpl := x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{Organization: []string{"Splitwise-QUIC Dev"}},
		NotBefore:             notBefore,
		NotAfter:              notAfter,
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	for _, h := range hosts {
		if ip := net.ParseIP(h); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
		} else {
			tmpl.DNSNames = append(tmpl.DNSNames, h)
		}
	}

	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &priv.PublicKey, priv)
	if err != nil {
		return nil, fmt.Errorf("create cert: %w", err)
	}

	sum := sha256.Sum256(der)
	return &CertBundle{
		TLS: tls.Certificate{
			Certificate: [][]byte{der},
			PrivateKey:  priv,
			Leaf:        mustParse(der),
		},
		SHA256:    sum[:],
		SHA256Hex: hex.EncodeToString(sum[:]),
		SHA256B64: base64.StdEncoding.EncodeToString(sum[:]),
		NotAfter:  notAfter,
	}, nil
}

func mustParse(der []byte) *x509.Certificate {
	c, _ := x509.ParseCertificate(der)
	return c
}

// TLSConfig returns a config that negotiates HTTP/3 ("h3"), HTTP/2 and HTTP/1.1.
// If requireClientCert is set we demand mutual TLS (the "complex" knob).
func (cb *CertBundle) TLSConfig(requireClientCert bool) *tls.Config {
	cfg := &tls.Config{
		Certificates: []tls.Certificate{cb.TLS},
		MinVersion:   tls.VersionTLS13, // QUIC mandates TLS 1.3 anyway
		NextProtos:   []string{"h3", "h2", "http/1.1"},
	}
	if requireClientCert {
		pool := x509.NewCertPool()
		pool.AddCert(cb.TLS.Leaf)
		cfg.ClientAuth = tls.RequireAndVerifyClientCert
		cfg.ClientCAs = pool
	}
	return cfg
}
