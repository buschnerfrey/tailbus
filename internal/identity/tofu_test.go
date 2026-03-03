package identity

import (
	"path/filepath"
	"testing"
)

func TestTOFUFirstUse(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "coord.fp")

	kp, _ := Generate()
	cert, _ := SelfSignedCert(kp)

	v := NewTOFUVerifier(fp)
	if err := v.Verify(cert.Certificate, nil); err != nil {
		t.Fatalf("first use should succeed: %v", err)
	}
}

func TestTOFUSubsequentMatch(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "coord.fp")

	kp, _ := Generate()
	cert, _ := SelfSignedCert(kp)

	v := NewTOFUVerifier(fp)

	// First use
	if err := v.Verify(cert.Certificate, nil); err != nil {
		t.Fatal(err)
	}

	// Same cert again
	if err := v.Verify(cert.Certificate, nil); err != nil {
		t.Fatalf("subsequent matching cert should succeed: %v", err)
	}
}

func TestTOFUMismatch(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "coord.fp")

	kp1, _ := Generate()
	cert1, _ := SelfSignedCert(kp1)

	kp2, _ := Generate()
	cert2, _ := SelfSignedCert(kp2)

	v := NewTOFUVerifier(fp)

	// First use with cert1
	if err := v.Verify(cert1.Certificate, nil); err != nil {
		t.Fatal(err)
	}

	// Different cert should fail
	if err := v.Verify(cert2.Certificate, nil); err == nil {
		t.Fatal("different cert should fail TOFU check")
	}
}
