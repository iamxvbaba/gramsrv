package sfu

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/pion/dtls/v3/pkg/crypto/selfsign"
)

// newDTLSCertificate generates a process-wide self-signed certificate (DTLS identity
// relies not on a CA but on the fingerprint delivered by the signaling plane).
func newDTLSCertificate() (tls.Certificate, string, error) {
	cert, err := selfsign.GenerateSelfSigned()
	if err != nil {
		return tls.Certificate{}, "", fmt.Errorf("sfu: self-signed cert: %w", err)
	}
	fp, err := certificateFingerprint(cert.Certificate[0])
	if err != nil {
		return tls.Certificate{}, "", err
	}
	return cert, fp, nil
}

// certificateFingerprint computes an RFC 4572 style sha-256 fingerprint ("AA:BB:...").
func certificateFingerprint(der []byte) (string, error) {
	if _, err := x509.ParseCertificate(der); err != nil {
		return "", fmt.Errorf("sfu: parse cert: %w", err)
	}
	sum := sha256.Sum256(der)
	parts := make([]string, len(sum))
	for i, b := range sum {
		parts[i] = strings.ToUpper(hex.EncodeToString([]byte{b}))
	}
	return strings.Join(parts, ":"), nil
}

// normalizeFingerprint strips case/separator differences for comparison.
func normalizeFingerprint(fp string) string {
	return strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(fp), ":", ""))
}
