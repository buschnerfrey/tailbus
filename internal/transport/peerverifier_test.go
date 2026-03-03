package transport

import (
	"testing"

	"github.com/alexanderfrey/tailbus/internal/handle"
)

func TestResolverVerifierByAddr(t *testing.T) {
	resolver := handle.NewResolver()
	resolver.UpdatePeerMap(map[string]handle.PeerInfo{
		"marketing": {NodeID: "node-1", PublicKey: []byte("key1"), AdvertiseAddr: "10.0.0.1:9443"},
	})

	v := NewResolverVerifier(resolver)

	// Matching addr + key should pass
	if err := v.VerifyPeerKey("10.0.0.1:9443", []byte("key1")); err != nil {
		t.Fatalf("expected success, got %v", err)
	}

	// Wrong key should fail
	if err := v.VerifyPeerKey("10.0.0.1:9443", []byte("wrongkey")); err == nil {
		t.Fatal("expected key mismatch error")
	}

	// Unknown addr should fail
	if err := v.VerifyPeerKey("10.0.0.99:9443", []byte("key1")); err == nil {
		t.Fatal("expected addr not found error")
	}
}

func TestResolverVerifierServerSide(t *testing.T) {
	resolver := handle.NewResolver()
	resolver.UpdatePeerMap(map[string]handle.PeerInfo{
		"marketing": {NodeID: "node-1", PublicKey: []byte("key1"), AdvertiseAddr: "10.0.0.1:9443"},
	})

	v := NewResolverVerifier(resolver)

	// Empty addr (server-side) — key exists in map
	if err := v.VerifyPeerKey("", []byte("key1")); err != nil {
		t.Fatalf("expected success, got %v", err)
	}

	// Unknown key
	if err := v.VerifyPeerKey("", []byte("unknown")); err == nil {
		t.Fatal("expected unknown key error")
	}
}
