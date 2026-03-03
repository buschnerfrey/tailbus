package handle

import (
	"fmt"
	"sync"
)

// CommandSpec describes a single command an agent supports.
type CommandSpec struct {
	Name             string
	Description      string
	ParametersSchema string
}

// ServiceManifest describes an agent's capabilities.
type ServiceManifest struct {
	Description string
	Commands    []CommandSpec
	Tags        []string
	Version     string
}

// PeerInfo holds the network info for a node that serves a given handle.
type PeerInfo struct {
	NodeID        string
	PublicKey     []byte
	AdvertiseAddr string
	Manifest      ServiceManifest
}

// RelayInfo holds the network info for a relay server.
type RelayInfo struct {
	NodeID    string
	PublicKey []byte
	Addr      string
}

// Resolver resolves handle names to peer info using a cached peer map.
type Resolver struct {
	mu       sync.RWMutex
	handleTo map[string]PeerInfo // handle name -> peer info
	relays   []RelayInfo
}

// NewResolver creates a new resolver.
func NewResolver() *Resolver {
	return &Resolver{
		handleTo: make(map[string]PeerInfo),
	}
}

// UpdatePeerMap replaces the cached peer map with fresh data.
func (r *Resolver) UpdatePeerMap(entries map[string]PeerInfo) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.handleTo = entries
}

// GetPeerMap returns a snapshot of the cached peer map.
func (r *Resolver) GetPeerMap() map[string]PeerInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make(map[string]PeerInfo, len(r.handleTo))
	for k, v := range r.handleTo {
		result[k] = v
	}
	return result
}

// UpdateRelays replaces the cached relay list.
func (r *Resolver) UpdateRelays(relays []RelayInfo) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.relays = relays
}

// GetRelays returns a snapshot of the cached relay list.
func (r *Resolver) GetRelays() []RelayInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]RelayInfo, len(r.relays))
	copy(result, r.relays)
	return result
}

// Resolve looks up a handle in the cached peer map.
func (r *Resolver) Resolve(handle string) (PeerInfo, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	info, ok := r.handleTo[handle]
	if !ok {
		return PeerInfo{}, fmt.Errorf("handle %q not found in peer map", handle)
	}
	return info, nil
}

// GetManifest returns the service manifest for a handle, if known.
func (r *Resolver) GetManifest(handle string) (ServiceManifest, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	info, ok := r.handleTo[handle]
	if !ok {
		return ServiceManifest{}, false
	}
	return info.Manifest, true
}

// ListHandlesByTags returns all handles whose manifest tags match the given filter.
// An empty tags slice returns all handles.
func (r *Resolver) ListHandlesByTags(tags []string) map[string]ServiceManifest {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make(map[string]ServiceManifest)
	for h, info := range r.handleTo {
		if matchesTags(info.Manifest.Tags, tags) {
			result[h] = info.Manifest
		}
	}
	return result
}

// matchesTags returns true if handleTags contains all of the required tags.
// An empty required slice matches everything.
func matchesTags(handleTags, required []string) bool {
	if len(required) == 0 {
		return true
	}
	tagSet := make(map[string]bool, len(handleTags))
	for _, t := range handleTags {
		tagSet[t] = true
	}
	for _, r := range required {
		if !tagSet[r] {
			return false
		}
	}
	return true
}
