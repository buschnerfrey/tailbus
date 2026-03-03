package identity

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"math/big"
	"time"
)

// SelfSignedCert generates a self-signed TLS certificate for mTLS.
// We use ECDSA P-256 for TLS (Ed25519 is for identity/signing).
// The node's Ed25519 public key is embedded in the certificate's Organization field
// so peers can verify identity from the peer map.
func SelfSignedCert(kp *Keypair) (tls.Certificate, error) {
	ecKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("generate ECDSA key: %w", err)
	}

	serialNumber, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("generate serial: %w", err)
	}

	template := &x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			CommonName:   "tailbus-node",
			Organization: []string{fmt.Sprintf("%x", kp.Public)},
		},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, template, &ecKey.PublicKey, ecKey)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("create certificate: %w", err)
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	keyDER, err := x509.MarshalECPrivateKey(ecKey)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("marshal ECDSA key: %w", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	return tls.X509KeyPair(certPEM, keyPEM)
}

// PubKeyFromCert extracts the Ed25519 public key from a DER-encoded certificate.
// The key is stored as a hex string in Organization[0].
func PubKeyFromCert(certDER []byte) ([]byte, error) {
	cert, err := x509.ParseCertificate(certDER)
	if err != nil {
		return nil, fmt.Errorf("parse certificate: %w", err)
	}
	if len(cert.Subject.Organization) == 0 {
		return nil, fmt.Errorf("certificate has no Organization field")
	}
	pubKey, err := hex.DecodeString(cert.Subject.Organization[0])
	if err != nil {
		return nil, fmt.Errorf("decode Organization hex: %w", err)
	}
	return pubKey, nil
}
