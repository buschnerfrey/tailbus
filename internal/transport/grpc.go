package transport

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"log/slog"
	"net"
	"sync"

	messagepb "github.com/alexanderfrey/tailbus/api/messagepb"
	transportpb "github.com/alexanderfrey/tailbus/api/transportpb"
	"github.com/alexanderfrey/tailbus/internal/identity"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
)

// GRPCTransport implements Transport using gRPC bidirectional streams.
type GRPCTransport struct {
	transportpb.UnimplementedNodeTransportServer

	logger    *slog.Logger
	grpcSrv   *grpc.Server
	receiveFn func(*messagepb.Envelope)
	sendFn    func(*messagepb.Envelope)

	tlsCert  *tls.Certificate // nil = insecure
	verifier PeerVerifier     // nil = no peer verification

	mu    sync.Mutex
	peers map[string]*peerConn // addr -> peer connection
	ctx   context.Context      // daemon lifetime context
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
		logger:   logger,
		tlsCert:  cert,
		verifier: verifier,
		peers:    make(map[string]*peerConn),
		ctx:      context.Background(), // default until Start() is called
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
func (t *GRPCTransport) OnReceive(fn func(*messagepb.Envelope)) {
	t.receiveFn = fn
}

// OnSend registers a callback invoked after a successful send to a peer.
func (t *GRPCTransport) OnSend(fn func(*messagepb.Envelope)) {
	t.sendFn = fn
}

// Send sends an envelope to a peer. Establishes connection lazily.
// On send failure, reconnects under the same lock to avoid races.
func (t *GRPCTransport) Send(addr string, env *messagepb.Envelope) error {
	pc, err := t.getOrConnect(addr)
	if err != nil {
		return err
	}

	pc.mu.Lock()
	err = pc.stream.Send(env)
	pc.mu.Unlock()

	if err == nil {
		if t.sendFn != nil {
			t.sendFn(env)
		}
		return nil
	}

	// Connection broken — reconnect under lock to avoid races
	t.mu.Lock()
	// Only clean up if the peer entry is still the same broken connection
	if current, ok := t.peers[addr]; ok && current == pc {
		delete(t.peers, addr)
		pc.conn.Close()
	}

	pc2, cerr := t.connectLocked(addr)
	if cerr != nil {
		t.mu.Unlock()
		return fmt.Errorf("reconnect to %s: %w", addr, cerr)
	}
	t.peers[addr] = pc2
	t.mu.Unlock()

	pc2.mu.Lock()
	defer pc2.mu.Unlock()
	if err := pc2.stream.Send(env); err != nil {
		return err
	}
	if t.sendFn != nil {
		t.sendFn(env)
	}
	return nil
}

func (t *GRPCTransport) getOrConnect(addr string) (*peerConn, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if pc, ok := t.peers[addr]; ok {
		return pc, nil
	}

	pc, err := t.connectLocked(addr)
	if err != nil {
		return nil, err
	}
	t.peers[addr] = pc
	return pc, nil
}

// connectLocked creates a new peer connection. Must be called with t.mu held.
func (t *GRPCTransport) connectLocked(addr string) (*peerConn, error) {
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

	client := transportpb.NewNodeTransportClient(conn)
	stream, err := client.Exchange(t.ctx)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("open exchange stream to %s: %w", addr, err)
	}

	pc := &peerConn{conn: conn, stream: stream}

	// Start receiving on this outbound stream
	go t.recvLoop(addr, pc, stream)

	t.logger.Info("connected to peer", "addr", addr)
	return pc, nil
}

func (t *GRPCTransport) recvLoop(addr string, pc *peerConn, stream transportpb.NodeTransport_ExchangeClient) {
	for {
		env, err := stream.Recv()
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
			t.receiveFn(env)
		}
	}
}

// Exchange handles incoming bidirectional streams from other daemons.
func (t *GRPCTransport) Exchange(stream transportpb.NodeTransport_ExchangeServer) error {
	for {
		env, err := stream.Recv()
		if err != nil {
			return err
		}
		if t.receiveFn != nil {
			t.receiveFn(env)
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

// Close shuts down the transport.
func (t *GRPCTransport) Close() error {
	// Close peer connections first so streams end
	t.mu.Lock()
	for addr, pc := range t.peers {
		pc.conn.Close()
		delete(t.peers, addr)
	}
	t.mu.Unlock()
	// Force stop the server (GracefulStop can hang with open streams)
	t.grpcSrv.Stop()
	return nil
}
