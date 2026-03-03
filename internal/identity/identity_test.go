package identity

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestGenerateAndSaveLoad(t *testing.T) {
	kp, err := Generate()
	if err != nil {
		t.Fatal(err)
	}
	if len(kp.Public) == 0 || len(kp.Private) == 0 {
		t.Fatal("empty keys")
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "key.pem")

	if err := kp.SavePrivateKey(path); err != nil {
		t.Fatal(err)
	}

	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0600 {
		t.Errorf("key file perms = %o, want 0600", info.Mode().Perm())
	}

	loaded, err := LoadPrivateKey(path)
	if err != nil {
		t.Fatal(err)
	}
	if !kp.Public.Equal(loaded.Public) {
		t.Error("public keys don't match")
	}
	if !kp.Private.Equal(loaded.Private) {
		t.Error("private keys don't match")
	}
}

func TestLoadOrGenerate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "key.pem")

	kp1, err := LoadOrGenerate(path)
	if err != nil {
		t.Fatal(err)
	}

	kp2, err := LoadOrGenerate(path)
	if err != nil {
		t.Fatal(err)
	}

	if !kp1.Public.Equal(kp2.Public) {
		t.Error("LoadOrGenerate should return same key on second call")
	}
}

func TestSelfSignedCert(t *testing.T) {
	kp, err := Generate()
	if err != nil {
		t.Fatal(err)
	}
	cert, err := SelfSignedCert(kp)
	if err != nil {
		t.Fatal(err)
	}
	if len(cert.Certificate) == 0 {
		t.Error("empty certificate")
	}
}

func TestPubKeyFromCert(t *testing.T) {
	kp, err := Generate()
	if err != nil {
		t.Fatal(err)
	}
	cert, err := SelfSignedCert(kp)
	if err != nil {
		t.Fatal(err)
	}

	pubKey, err := PubKeyFromCert(cert.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(kp.Public, pubKey) {
		t.Error("extracted public key does not match original")
	}
}

func TestPubKeyFromCertInvalid(t *testing.T) {
	_, err := PubKeyFromCert([]byte("not a cert"))
	if err == nil {
		t.Error("expected error for invalid cert")
	}
}
