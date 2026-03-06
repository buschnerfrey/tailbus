package handle

import (
	"fmt"
	"sort"
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
	Description  string
	Commands     []CommandSpec
	Tags         []string
	Version      string
	Capabilities []string
	Domains      []string
	InputTypes   []string
	OutputTypes  []string
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

// NodeInfo holds basic info about a peer node (may have zero handles).
type NodeInfo struct {
	NodeID        string
	AdvertiseAddr string
	PublicKey     []byte
}

// RoomInfo holds discovery metadata for a shared room.
type RoomInfo struct {
	RoomID     string
	Title      string
	CreatedBy  string
	HomeNodeID string
	Members    []string
	Status     string
	NextSeq    uint64
	CreatedAt  int64
	UpdatedAt  int64
}

// FindQuery describes structured discovery constraints.
type FindQuery struct {
	Capabilities []string
	Domains      []string
	Tags         []string
	CommandName  string
	Version      string
	Limit        int
}

// HandleMatch is a ranked discovery result.
type HandleMatch struct {
	Handle       string
	Manifest     ServiceManifest
	Score        int
	MatchReasons []string
}

// Resolver resolves handle names to peer info using a cached peer map.
type Resolver struct {
	mu       sync.RWMutex
	handleTo map[string]PeerInfo // handle name -> peer info
	nodes    []NodeInfo          // all peer nodes (including those with no handles)
	relays   []RelayInfo
	rooms    map[string]RoomInfo
}

// NewResolver creates a new resolver.
func NewResolver() *Resolver {
	return &Resolver{
		handleTo: make(map[string]PeerInfo),
		rooms:    make(map[string]RoomInfo),
	}
}

// UpdatePeerMap replaces the cached peer map with fresh data.
func (r *Resolver) UpdatePeerMap(entries map[string]PeerInfo) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.handleTo = entries
}

// UpdateNodes replaces the cached node list.
func (r *Resolver) UpdateNodes(nodes []NodeInfo) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.nodes = nodes
}

// GetNodes returns a snapshot of all known peer nodes.
func (r *Resolver) GetNodes() []NodeInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]NodeInfo, len(r.nodes))
	copy(result, r.nodes)
	return result
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

// UpdateRooms replaces the cached room map.
func (r *Resolver) UpdateRooms(rooms map[string]RoomInfo) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rooms = rooms
}

// ResolveRoom looks up a room by ID in the cached room map.
func (r *Resolver) ResolveRoom(roomID string) (RoomInfo, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	info, ok := r.rooms[roomID]
	if !ok {
		return RoomInfo{}, fmt.Errorf("room %q not found in peer map", roomID)
	}
	return info, nil
}

// ListRoomsForMember returns all cached rooms that include the given handle.
func (r *Resolver) ListRoomsForMember(handle string) []RoomInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var result []RoomInfo
	for _, room := range r.rooms {
		for _, member := range room.Members {
			if member == handle {
				result = append(result, room)
				break
			}
		}
	}
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

// FindHandles returns ranked handles matching the structured discovery query.
func (r *Resolver) FindHandles(query FindQuery) []HandleMatch {
	r.mu.RLock()
	defer r.mu.RUnlock()

	entries := make(map[string]ServiceManifest, len(r.handleTo))
	for handle, info := range r.handleTo {
		entries[handle] = info.Manifest
	}
	return FindMatches(entries, query)
}

// FindMatches ranks a manifest set against a structured discovery query.
func FindMatches(entries map[string]ServiceManifest, query FindQuery) []HandleMatch {
	var matches []HandleMatch
	for handle, manifest := range entries {
		match, ok := evaluateMatch(handle, manifest, query)
		if !ok {
			continue
		}
		matches = append(matches, match)
	}
	sort.Slice(matches, func(i, j int) bool {
		if matches[i].Score != matches[j].Score {
			return matches[i].Score > matches[j].Score
		}
		return matches[i].Handle < matches[j].Handle
	})
	if query.Limit > 0 && len(matches) > query.Limit {
		return matches[:query.Limit]
	}
	return matches
}

func evaluateMatch(handle string, manifest ServiceManifest, query FindQuery) (HandleMatch, bool) {
	match := HandleMatch{Handle: handle, Manifest: manifest}

	if !containsAll(manifest.Capabilities, query.Capabilities) {
		return HandleMatch{}, false
	}
	for _, capability := range query.Capabilities {
		match.Score += 100
		match.MatchReasons = append(match.MatchReasons, "capability:"+capability)
	}

	if !containsAll(manifest.Domains, query.Domains) {
		return HandleMatch{}, false
	}
	for _, domain := range query.Domains {
		match.Score += 30
		match.MatchReasons = append(match.MatchReasons, "domain:"+domain)
	}

	if !containsAll(manifest.Tags, query.Tags) {
		return HandleMatch{}, false
	}
	for _, tag := range query.Tags {
		match.Score += 10
		match.MatchReasons = append(match.MatchReasons, "tag:"+tag)
	}

	if query.CommandName != "" {
		if !hasCommand(manifest.Commands, query.CommandName) {
			return HandleMatch{}, false
		}
		match.Score += 20
		match.MatchReasons = append(match.MatchReasons, "command:"+query.CommandName)
	}

	if query.Version != "" {
		if manifest.Version != query.Version {
			return HandleMatch{}, false
		}
		match.Score += 10
		match.MatchReasons = append(match.MatchReasons, "version:"+query.Version)
	}

	return match, true
}

// matchesTags returns true if handleTags contains all of the required tags.
// An empty required slice matches everything.
func matchesTags(handleTags, required []string) bool {
	return containsAll(handleTags, required)
}

func containsAll(values, required []string) bool {
	if len(required) == 0 {
		return true
	}
	valueSet := make(map[string]bool, len(values))
	for _, value := range values {
		valueSet[value] = true
	}
	for _, needed := range required {
		if !valueSet[needed] {
			return false
		}
	}
	return true
}

func hasCommand(commands []CommandSpec, name string) bool {
	for _, command := range commands {
		if command.Name == name {
			return true
		}
	}
	return false
}
