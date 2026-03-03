package transport

import (
	"bytes"
	"fmt"

	"github.com/alexanderfrey/tailbus/internal/handle"
)

// PeerVerifier verifies peer identity from TLS certificates.
type PeerVerifier interface {
	VerifyPeerKey(addr string, pubKey []byte) error
}

// ResolverVerifier verifies peer public keys against the resolver's peer map.
type ResolverVerifier struct {
	resolver *handle.Resolver
}

// NewResolverVerifier creates a verifier backed by a handle resolver.
func NewResolverVerifier(resolver *handle.Resolver) *ResolverVerifier {
	return &ResolverVerifier{resolver: resolver}
}

// VerifyPeerKey checks that the public key belongs to a known peer.
// If addr is non-empty, it finds the peer by address and compares keys.
// If addr is empty (server-side inbound), it checks that the key exists
// anywhere in the peer map.
func (v *ResolverVerifier) VerifyPeerKey(addr string, pubKey []byte) error {
	peerMap := v.resolver.GetPeerMap()

	if addr != "" {
		// Find peer by address and compare keys
		for _, info := range peerMap {
			if info.AdvertiseAddr == addr {
				if bytes.Equal(info.PublicKey, pubKey) {
					return nil
				}
				return fmt.Errorf("peer %s key mismatch", addr)
			}
		}
		return fmt.Errorf("peer %s not found in peer map", addr)
	}

	// Server-side: verify pubkey exists anywhere
	for _, info := range peerMap {
		if bytes.Equal(info.PublicKey, pubKey) {
			return nil
		}
	}
	return fmt.Errorf("unknown peer key")
}
