package transport

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"time"

	transportpb "github.com/alexanderfrey/tailbus/api/transportpb"
	"github.com/alexanderfrey/tailbus/internal/handle"
	"github.com/alexanderfrey/tailbus/internal/identity"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
)

// GRPCTransport implements Transport using gRPC bidirectional streams.
type GRPCTransport struct {
	transportpb.UnimplementedNodeTransportServer

	logger    *slog.Logger
	grpcSrv   *grpc.Server
	receiveFn func(*transportpb.TransportMessage)
	sendFn    func(*transportpb.TransportMessage)

	tlsCert  *tls.Certificate // nil = insecure
	verifier PeerVerifier     // nil = no peer verification

	mu    sync.Mutex
	peers map[string]*peerConn // addr -> peer connection
	ctx   context.Context      // daemon lifetime context

	localPubKey []byte                     // local node's public key (for relay metadata)
	resolver    *handle.Resolver           // for relay addr lookup + pubkey reverse-lookup
	relayMu     sync.Mutex
	relayConns  map[string]*peerConn       // relay addr → connection
	directFails map[string]time.Time       // peer addr → last direct failure time
}

type peerConn struct {
	conn   *grpc.ClientConn
	stream transportpb.NodeTransport_ExchangeClient
	mu     sync.Mutex
}

// NewGRPCTransport creates a new gRPC-based transport.
// If cert and verifier are provided, mTLS is enabled.
func NewGRPCTransport(logger *slog.Logger, cert *tls.Certificate, verifier PeerVerifier) *GRPCTransport {
	t := &GRPCTransport{
		logger:      logger,
		tlsCert:     cert,
		verifier:    verifier,
		peers:       make(map[string]*peerConn),
		ctx:         context.Background(), // default until Start() is called
		relayConns:  make(map[string]*peerConn),
		directFails: make(map[string]time.Time),
	}

	var serverOpts []grpc.ServerOption
	if cert != nil {
		serverTLS := &tls.Config{
			Certificates: []tls.Certificate{*cert},
			ClientAuth:   tls.RequireAnyClientCert,
			VerifyPeerCertificate: func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
				if verifier == nil || len(rawCerts) == 0 {
					return nil
				}
				pubKey, err := identity.PubKeyFromCert(rawCerts[0])
				if err != nil {
					return fmt.Errorf("extract peer key: %w", err)
				}
				return verifier.VerifyPeerKey("", pubKey)
			},
		}
		serverOpts = append(serverOpts, grpc.Creds(credentials.NewTLS(serverTLS)))
	}

	gs := grpc.NewServer(serverOpts...)
	transportpb.RegisterNodeTransportServer(gs, t)
	t.grpcSrv = gs
	return t
}

// Start stores the daemon context. All outbound streams will be bound to this
// context so they are torn down when the daemon shuts down.
func (t *GRPCTransport) Start(ctx context.Context) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.ctx = ctx
}

// Serve starts listening for incoming peer connections.
func (t *GRPCTransport) Serve(lis net.Listener) error {
	t.logger.Info("P2P transport listening", "addr", lis.Addr())
	return t.grpcSrv.Serve(lis)
}

// OnReceive registers the callback for incoming envelopes.
func (t *GRPCTransport) OnReceive(fn func(*transportpb.TransportMessage)) {
	t.receiveFn = fn
}

// OnSend registers a callback invoked after a successful send to a peer.
func (t *GRPCTransport) OnSend(fn func(*transportpb.TransportMessage)) {
	t.sendFn = fn
}

// SetResolver sets the resolver used for relay address and pubkey lookup.
func (t *GRPCTransport) SetResolver(r *handle.Resolver) {
	t.resolver = r
}

// SetLocalPubKey sets the local node's public key, sent as gRPC metadata
// when connecting to relay servers for identification in insecure mode.
func (t *GRPCTransport) SetLocalPubKey(key []byte) {
	t.localPubKey = key
}

const directFailTTL = 60 * time.Second

// Send sends an envelope to a peer. Establishes connection lazily.
// On send failure, falls back to relay if available.
// The context bounds connection establishment and the send; the underlying
// stream lives beyond this call (bound to the daemon lifetime context).
func (t *GRPCTransport) Send(ctx context.Context, addr string, msg *transportpb.TransportMessage) error {
	// Check if direct connection to this addr recently failed
	t.relayMu.Lock()
	failTime, directFailed := t.directFails[addr]
	if directFailed && time.Since(failTime) > directFailTTL {
		delete(t.directFails, addr)
		directFailed = false
	}
	t.relayMu.Unlock()

	if !directFailed {
		err := t.sendDirect(ctx, addr, msg)
		if err == nil {
			return nil
		}
		// Record direct failure and try relay
		t.relayMu.Lock()
		t.directFails[addr] = time.Now()
		t.relayMu.Unlock()
		t.logger.Debug("direct send failed, trying relay", "addr", addr, "error", err)
	}

	// Relay fallback
	if t.resolver == nil {
		return fmt.Errorf("direct send to %s failed and no resolver for relay fallback", addr)
	}
	return t.sendViaRelay(ctx, addr, msg)
}

// sendDirect sends a transport message directly to a peer.
func (t *GRPCTransport) sendDirect(ctx context.Context, addr string, msg *transportpb.TransportMessage) error {
	pc, err := t.getOrConnect(ctx, addr)
	if err != nil {
		return err
	}

	pc.mu.Lock()
	err = pc.stream.Send(msg)
	pc.mu.Unlock()

	if err == nil {
		if t.sendFn != nil {
			t.sendFn(msg)
		}
		return nil
	}

	// Connection broken — clean up the stale entry
	t.mu.Lock()
	if current, ok := t.peers[addr]; ok && current == pc {
		delete(t.peers, addr)
		pc.conn.Close()
	}
	t.mu.Unlock()

	// Reconnect outside the lock
	pc2, cerr := t.connect(ctx, addr)
	if cerr != nil {
		return fmt.Errorf("reconnect to %s: %w", addr, cerr)
	}

	// Store, but check for a race
	t.mu.Lock()
	if existing, ok := t.peers[addr]; ok {
		t.mu.Unlock()
		pc2.conn.Close()
		pc2 = existing
	} else {
		t.peers[addr] = pc2
		t.mu.Unlock()
	}

	pc2.mu.Lock()
	defer pc2.mu.Unlock()
	if err := pc2.stream.Send(msg); err != nil {
		return err
	}
	if t.sendFn != nil {
		t.sendFn(msg)
	}
	return nil
}

// sendViaRelay sends a transport message through a relay server.
func (t *GRPCTransport) sendViaRelay(_ context.Context, peerAddr string, msg *transportpb.TransportMessage) error {
	// Look up the target's pubkey from the resolver by matching addr
	targetKey := t.pubkeyForAddr(peerAddr)
	if targetKey == nil {
		return fmt.Errorf("cannot find pubkey for peer %s for relay routing", peerAddr)
	}

	// Stamp relay target key on the transport wrapper.
	msg.RelayTargetKey = targetKey

	relays := t.resolver.GetRelays()
	if len(relays) == 0 {
		return fmt.Errorf("no relays available for fallback to %s", peerAddr)
	}

	// Try first available relay
	for _, relay := range relays {
		pc, err := t.getOrConnectRelay(relay.Addr)
		if err != nil {
			t.logger.Warn("relay connection failed", "relay", relay.Addr, "error", err)
			continue
		}
		pc.mu.Lock()
		err = pc.stream.Send(msg)
		pc.mu.Unlock()
		if err != nil {
			t.logger.Warn("relay send failed", "relay", relay.Addr, "error", err)
			continue
		}
		if t.sendFn != nil {
			t.sendFn(msg)
		}
		t.logger.Debug("sent via relay", "relay", relay.Addr, "target", peerAddr)
		return nil
	}
	return fmt.Errorf("all relays failed for %s", peerAddr)
}

// pubkeyForAddr finds a peer's public key by matching their advertise address.
func (t *GRPCTransport) pubkeyForAddr(addr string) []byte {
	if t.resolver == nil {
		return nil
	}
	peerMap := t.resolver.GetPeerMap()
	for _, info := range peerMap {
		if info.AdvertiseAddr == addr {
			return info.PublicKey
		}
	}
	return nil
}

// getOrConnectRelay returns an existing or new relay connection.
func (t *GRPCTransport) getOrConnectRelay(addr string) (*peerConn, error) {
	t.relayMu.Lock()
	defer t.relayMu.Unlock()

	if pc, ok := t.relayConns[addr]; ok {
		return pc, nil
	}

	pc, err := t.connectRelay(addr)
	if err != nil {
		return nil, err
	}
	t.relayConns[addr] = pc
	return pc, nil
}

// connectRelay creates a new relay connection. Must be called with relayMu held.
func (t *GRPCTransport) connectRelay(addr string) (*peerConn, error) {
	var dialOpt grpc.DialOption
	if t.tlsCert != nil {
		clientTLS := &tls.Config{
			Certificates:       []tls.Certificate{*t.tlsCert},
			InsecureSkipVerify: true,
			VerifyPeerCertificate: func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
				if t.verifier == nil || len(rawCerts) == 0 {
					return nil
				}
				pubKey, err := identity.PubKeyFromCert(rawCerts[0])
				if err != nil {
					return fmt.Errorf("extract relay key: %w", err)
				}
				return t.verifier.VerifyPeerKey(addr, pubKey)
			},
		}
		dialOpt = grpc.WithTransportCredentials(credentials.NewTLS(clientTLS))
	} else {
		dialOpt = grpc.WithTransportCredentials(insecure.NewCredentials())
	}

	conn, err := grpc.NewClient(addr, dialOpt)
	if err != nil {
		return nil, fmt.Errorf("dial relay %s: %w", addr, err)
	}

	client := transportpb.NewNodeTransportClient(conn)

	// Attach local pubkey as metadata so the relay can identify us in insecure mode
	streamCtx := t.ctx
	if len(t.localPubKey) > 0 {
		md := metadata.Pairs("x-tailbus-pubkey", hex.EncodeToString(t.localPubKey))
		streamCtx = metadata.NewOutgoingContext(t.ctx, md)
	}

	stream, err := client.Exchange(streamCtx)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("open exchange stream to relay %s: %w", addr, err)
	}

	pc := &peerConn{conn: conn, stream: stream}
	go t.relayRecvLoop(addr, pc, stream)

	t.logger.Info("connected to relay", "addr", addr)
	return pc, nil
}

// relayRecvLoop receives envelopes from a relay connection.
func (t *GRPCTransport) relayRecvLoop(addr string, pc *peerConn, stream transportpb.NodeTransport_ExchangeClient) {
	for {
		msg, err := stream.Recv()
		if err != nil {
			t.logger.Debug("relay stream closed", "addr", addr, "error", err)
			t.relayMu.Lock()
			if current, ok := t.relayConns[addr]; ok && current == pc {
				delete(t.relayConns, addr)
				t.logger.Info("removed stale relay conn", "addr", addr)
			}
			t.relayMu.Unlock()
			return
		}
		// Clear relay_target_key before delivering (it was for the relay)
		msg.RelayTargetKey = nil
		if t.receiveFn != nil {
			t.receiveFn(msg)
		}
	}
}

// ConnectToRelays proactively connects to all known relays so this node
// can receive forwarded messages. Should be called after relay info updates.
func (t *GRPCTransport) ConnectToRelays() {
	if t.resolver == nil {
		return
	}
	relays := t.resolver.GetRelays()
	for _, r := range relays {
		if _, err := t.getOrConnectRelay(r.Addr); err != nil {
			t.logger.Warn("failed to connect to relay", "addr", r.Addr, "error", err)
		}
	}
}

func (t *GRPCTransport) getOrConnect(ctx context.Context, addr string) (*peerConn, error) {
	// Fast path: already connected
	t.mu.Lock()
	if pc, ok := t.peers[addr]; ok {
		t.mu.Unlock()
		return pc, nil
	}
	t.mu.Unlock()

	// Slow path: connect outside the global lock
	pc, err := t.connect(ctx, addr)
	if err != nil {
		return nil, err
	}

	// Store, but check if another goroutine raced us
	t.mu.Lock()
	if existing, ok := t.peers[addr]; ok {
		t.mu.Unlock()
		pc.conn.Close() // discard duplicate; recvLoop will exit on stream close
		return existing, nil
	}
	t.peers[addr] = pc
	t.mu.Unlock()
	return pc, nil
}

// connect creates a new peer connection without holding t.mu.
// The ctx bounds connection establishment; the stream itself is bound to t.ctx
// so it lives for the daemon lifetime.
func (t *GRPCTransport) connect(ctx context.Context, addr string) (*peerConn, error) {
	var dialOpt grpc.DialOption
	if t.tlsCert != nil {
		clientTLS := &tls.Config{
			Certificates:       []tls.Certificate{*t.tlsCert},
			InsecureSkipVerify: true, // self-signed; we verify via VerifyPeerCertificate
			VerifyPeerCertificate: func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
				if t.verifier == nil || len(rawCerts) == 0 {
					return nil
				}
				pubKey, err := identity.PubKeyFromCert(rawCerts[0])
				if err != nil {
					return fmt.Errorf("extract peer key: %w", err)
				}
				return t.verifier.VerifyPeerKey(addr, pubKey)
			},
		}
		dialOpt = grpc.WithTransportCredentials(credentials.NewTLS(clientTLS))
	} else {
		dialOpt = grpc.WithTransportCredentials(insecure.NewCredentials())
	}

	conn, err := grpc.NewClient(addr, dialOpt)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", addr, err)
	}

	// Race Exchange(t.ctx) against the per-call ctx so we bail out if the
	// caller's deadline expires, but the stream itself stays bound to the
	// daemon lifetime context (t.ctx).
	type exchangeResult struct {
		stream transportpb.NodeTransport_ExchangeClient
		err    error
	}
	ch := make(chan exchangeResult, 1)
	client := transportpb.NewNodeTransportClient(conn)
	go func() {
		s, e := client.Exchange(t.ctx)
		ch <- exchangeResult{s, e}
	}()

	select {
	case <-ctx.Done():
		conn.Close()
		return nil, fmt.Errorf("connect to %s: %w", addr, ctx.Err())
	case res := <-ch:
		if res.err != nil {
			conn.Close()
			return nil, fmt.Errorf("open exchange stream to %s: %w", addr, res.err)
		}
		pc := &peerConn{conn: conn, stream: res.stream}
		go t.recvLoop(addr, pc, res.stream)
		t.logger.Info("connected to peer", "addr", addr)
		return pc, nil
	}
}

func (t *GRPCTransport) recvLoop(addr string, pc *peerConn, stream transportpb.NodeTransport_ExchangeClient) {
	for {
		msg, err := stream.Recv()
		if err != nil {
			t.logger.Debug("peer stream closed", "addr", addr, "error", err)
			// Clean up the peer entry, but only if it's still the same connection
			// (a reconnect may have already replaced it)
			t.mu.Lock()
			if current, ok := t.peers[addr]; ok && current == pc {
				delete(t.peers, addr)
				t.logger.Info("removed stale peer", "addr", addr)
			}
			t.mu.Unlock()
			return
		}
		if t.receiveFn != nil {
			t.receiveFn(msg)
		}
	}
}

// Exchange handles incoming bidirectional streams from other daemons.
func (t *GRPCTransport) Exchange(stream transportpb.NodeTransport_ExchangeServer) error {
	for {
		msg, err := stream.Recv()
		if err != nil {
			return err
		}
		if t.receiveFn != nil {
			t.receiveFn(msg)
		}
	}
}

// ConnectedAddrs returns the list of currently connected peer addresses.
func (t *GRPCTransport) ConnectedAddrs() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	addrs := make([]string, 0, len(t.peers))
	for addr := range t.peers {
		addrs = append(addrs, addr)
	}
	return addrs
}

// ConnectedRelayAddrs returns the list of currently connected relay addresses.
func (t *GRPCTransport) ConnectedRelayAddrs() []string {
	t.relayMu.Lock()
	defer t.relayMu.Unlock()
	addrs := make([]string, 0, len(t.relayConns))
	for addr := range t.relayConns {
		addrs = append(addrs, addr)
	}
	return addrs
}

// DirectFailedAddrs returns peer addresses where direct connection has recently failed
// (within the last 30s), meaning they may be reachable via relay.
func (t *GRPCTransport) DirectFailedAddrs() []string {
	t.relayMu.Lock()
	defer t.relayMu.Unlock()
	cutoff := time.Now().Add(-30 * time.Second)
	var addrs []string
	for addr, failTime := range t.directFails {
		if failTime.After(cutoff) {
			addrs = append(addrs, addr)
		}
	}
	return addrs
}

// Close shuts down the transport.
func (t *GRPCTransport) Close() error {
	// Close peer connections first so streams end
	t.mu.Lock()
	for addr, pc := range t.peers {
		pc.conn.Close()
		delete(t.peers, addr)
	}
	t.mu.Unlock()
	// Close relay connections
	t.relayMu.Lock()
	for addr, pc := range t.relayConns {
		pc.conn.Close()
		delete(t.relayConns, addr)
	}
	t.relayMu.Unlock()
	// Force stop the server (GracefulStop can hang with open streams)
	t.grpcSrv.Stop()
	return nil
}
