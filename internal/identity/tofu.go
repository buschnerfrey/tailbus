package identity

import (
	"crypto/sha256"
	"crypto/x509"
	"fmt"
	"os"
	"strings"
)

// Fingerprint returns the SHA-256 hex fingerprint of a DER-encoded certificate.
func Fingerprint(certDER []byte) string {
	h := sha256.Sum256(certDER)
	return fmt.Sprintf("%x", h)
}

// TOFUVerifier implements trust-on-first-use verification for TLS certificates.
// On first connection it saves the certificate fingerprint to a file.
// On subsequent connections it rejects certificates whose fingerprint doesn't match.
type TOFUVerifier struct {
	path string
}

// NewTOFUVerifier creates a TOFU verifier that persists fingerprints to the given file path.
func NewTOFUVerifier(path string) *TOFUVerifier {
	return &TOFUVerifier{path: path}
}

// Verify checks raw TLS certificates against the stored fingerprint.
// This is intended for use as tls.Config.VerifyPeerCertificate.
func (v *TOFUVerifier) Verify(rawCerts [][]byte, _ [][]*x509.Certificate) error {
	if len(rawCerts) == 0 {
		return fmt.Errorf("no certificates presented")
	}

	fp := Fingerprint(rawCerts[0])

	data, err := os.ReadFile(v.path)
	if os.IsNotExist(err) {
		// First use: trust and save
		if err := os.WriteFile(v.path, []byte(fp), 0600); err != nil {
			return fmt.Errorf("save TOFU fingerprint: %w", err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("read TOFU fingerprint: %w", err)
	}

	saved := strings.TrimSpace(string(data))
	if saved != fp {
		return fmt.Errorf("certificate fingerprint mismatch (TOFU violation): got %s, want %s", fp, saved)
	}
	return nil
}
