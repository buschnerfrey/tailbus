package auth

import (
	"path/filepath"
	"testing"
	"time"
)

func TestCredentialsSaveLoad(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "creds.json")

	creds := &Credentials{
		AccessToken:  "at_test",
		RefreshToken: "rt_test",
		Email:        "alice@example.com",
		CoordAddr:    "coord.tailbus.co:8443",
		ExpiresAt:    time.Now().Add(1 * time.Hour).Unix(),
	}

	if err := SaveCredentials(path, creds); err != nil {
		t.Fatal(err)
	}

	loaded, err := LoadCredentials(path)
	if err != nil {
		t.Fatal(err)
	}

	if loaded.Email != "alice@example.com" {
		t.Fatalf("got email %q, want alice@example.com", loaded.Email)
	}
	if loaded.AccessToken != "at_test" {
		t.Fatalf("got access_token %q, want at_test", loaded.AccessToken)
	}
	if loaded.CoordAddr != "coord.tailbus.co:8443" {
		t.Fatalf("got coord_addr %q, want coord.tailbus.co:8443", loaded.CoordAddr)
	}
}

func TestCredentialsNeedsRefresh(t *testing.T) {
	// Token expiring in 10 minutes — within 5 min buffer, so needs refresh
	creds := &Credentials{ExpiresAt: time.Now().Add(4 * time.Minute).Unix()}
	if !creds.NeedsRefresh() {
		t.Fatal("expected NeedsRefresh=true for token expiring in 4 minutes")
	}

	// Token expiring in 1 hour — doesn't need refresh
	creds = &Credentials{ExpiresAt: time.Now().Add(1 * time.Hour).Unix()}
	if creds.NeedsRefresh() {
		t.Fatal("expected NeedsRefresh=false for token expiring in 1 hour")
	}
}

func TestCredentialsRemove(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "creds.json")

	if err := SaveCredentials(path, &Credentials{Email: "test@example.com"}); err != nil {
		t.Fatal(err)
	}

	if err := RemoveCredentials(path); err != nil {
		t.Fatal(err)
	}

	_, err := LoadCredentials(path)
	if err == nil {
		t.Fatal("expected error loading removed credentials")
	}
}

func TestCredentialsRemoveNonExistent(t *testing.T) {
	// Removing non-existent file should not error
	if err := RemoveCredentials("/nonexistent/path/creds.json"); err != nil {
		t.Fatal(err)
	}
}
