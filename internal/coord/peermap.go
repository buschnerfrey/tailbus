package coord

import (
	"crypto/sha256"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	pb "github.com/alexanderfrey/tailbus/api/coordpb"
)

// PeerMap builds and distributes peer maps to watching nodes.
type PeerMap struct {
	store   *Store
	logger  *slog.Logger
	version atomic.Int64

	mu       sync.RWMutex
	watchers map[string]chan *pb.PeerMapUpdate // node_id -> update channel
	lastHash string
}

// NewPeerMap creates a new peer map manager.
func NewPeerMap(store *Store, logger *slog.Logger) *PeerMap {
	return &PeerMap{
		store:    store,
		logger:   logger,
		watchers: make(map[string]chan *pb.PeerMapUpdate),
	}
}

// Build reads all nodes from the store and creates a PeerMapUpdate.
func (pm *PeerMap) Build() (*pb.PeerMapUpdate, error) {
	nodes, err := pm.store.GetAllNodes()
	if err != nil {
		return nil, err
	}

	var peers []*pb.PeerInfo
	var relays []*pb.RelayInfo
	for _, n := range nodes {
		if n.IsRelay {
			relays = append(relays, &pb.RelayInfo{
				NodeId:    n.NodeID,
				PublicKey: n.PublicKey,
				Addr:      n.AdvertiseAddr,
			})
			continue
		}
		// Build deprecated HandleDescriptions from manifests for backward compat
		descs := make(map[string]string, len(n.HandleManifests))
		for h, m := range n.HandleManifests {
			if m != nil && m.Description != "" {
				descs[h] = m.Description
			}
		}
		peers = append(peers, &pb.PeerInfo{
			NodeId:             n.NodeID,
			PublicKey:          n.PublicKey,
			AdvertiseAddr:      n.AdvertiseAddr,
			Handles:            n.Handles,
			LastHeartbeatUnix:  n.LastHeartbeat.Unix(),
			HandleDescriptions: descs,
			HandleManifests:    n.HandleManifests,
		})
	}

	ver := pm.version.Add(1)
	return &pb.PeerMapUpdate{
		Peers:   peers,
		Relays:  relays,
		Version: ver,
	}, nil
}

// peerHash computes a hash of the peer topology (node IDs, addresses, handles)
// and relay info. Deliberately excludes heartbeat timestamps so that heartbeats
// alone don't trigger broadcasts.
func peerHash(peers []*pb.PeerInfo, relays []*pb.RelayInfo) string {
	// Sort by node ID for deterministic hashing
	sorted := make([]*pb.PeerInfo, len(peers))
	copy(sorted, peers)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].NodeId < sorted[j].NodeId
	})

	h := sha256.New()
	for _, p := range sorted {
		handles := make([]string, len(p.Handles))
		copy(handles, p.Handles)
		sort.Strings(handles)
		fmt.Fprintf(h, "%s|%s|%s\n", p.NodeId, p.AdvertiseAddr, strings.Join(handles, ","))
	}

	// Include relay info in hash
	sortedRelays := make([]*pb.RelayInfo, len(relays))
	copy(sortedRelays, relays)
	sort.Slice(sortedRelays, func(i, j int) bool {
		return sortedRelays[i].NodeId < sortedRelays[j].NodeId
	})
	for _, r := range sortedRelays {
		fmt.Fprintf(h, "relay|%s|%s\n", r.NodeId, r.Addr)
	}

	return fmt.Sprintf("%x", h.Sum(nil))
}

// AddWatcher registers a node to receive peer map updates.
func (pm *PeerMap) AddWatcher(nodeID string) chan *pb.PeerMapUpdate {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	ch := make(chan *pb.PeerMapUpdate, 8)
	pm.watchers[nodeID] = ch
	pm.logger.Info("watcher added", "node_id", nodeID)
	return ch
}

// RemoveWatcher removes a node from receiving peer map updates.
func (pm *PeerMap) RemoveWatcher(nodeID string) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	if ch, ok := pm.watchers[nodeID]; ok {
		close(ch)
		delete(pm.watchers, nodeID)
		pm.logger.Info("watcher removed", "node_id", nodeID)
	}
}

// Broadcast builds a peer map update and sends it to all watchers only if the
// topology has changed since the last broadcast.
func (pm *PeerMap) Broadcast() error {
	update, err := pm.Build()
	if err != nil {
		return err
	}

	hash := peerHash(update.Peers, update.Relays)

	pm.mu.Lock()
	if hash == pm.lastHash {
		pm.mu.Unlock()
		return nil
	}
	pm.lastHash = hash
	pm.mu.Unlock()

	pm.mu.RLock()
	defer pm.mu.RUnlock()
	for nodeID, ch := range pm.watchers {
		select {
		case ch <- update:
		default:
			pm.logger.Warn("watcher channel full, dropping update", "node_id", nodeID)
		}
	}
	return nil
}

// ForceBroadcast sends a peer map update unconditionally (bypasses hash check).
// Used by the reaper when it knows data has changed.
func (pm *PeerMap) ForceBroadcast() error {
	update, err := pm.Build()
	if err != nil {
		return err
	}

	// Update the hash so subsequent Broadcast() calls see the new state
	hash := peerHash(update.Peers, update.Relays)
	pm.mu.Lock()
	pm.lastHash = hash
	pm.mu.Unlock()

	pm.mu.RLock()
	defer pm.mu.RUnlock()
	for nodeID, ch := range pm.watchers {
		select {
		case ch <- update:
		default:
			pm.logger.Warn("watcher channel full, dropping update", "node_id", nodeID)
		}
	}
	return nil
}
