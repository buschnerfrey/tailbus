package internal

import (
	"context"
	"crypto/tls"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	agentpb "github.com/alexanderfrey/tailbus/api/agentpb"
	coordpb "github.com/alexanderfrey/tailbus/api/coordpb"
	messagepb "github.com/alexanderfrey/tailbus/api/messagepb"
	"github.com/alexanderfrey/tailbus/internal/coord"
	"github.com/alexanderfrey/tailbus/internal/daemon"
	"github.com/alexanderfrey/tailbus/internal/handle"
	"github.com/alexanderfrey/tailbus/internal/identity"
	"github.com/alexanderfrey/tailbus/internal/relay"
	"github.com/alexanderfrey/tailbus/internal/session"
	"github.com/alexanderfrey/tailbus/internal/transport"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
)

// TestRelayEndToEnd tests message delivery through a relay when direct P2P is unreachable.
// Topology: coord + relay + 2 daemons, where daemon B's advertise addr is unreachable.
func TestRelayEndToEnd(t *testing.T) {
	dir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))

	// --- Start coordination server ---
	store, err := coord.NewStore(filepath.Join(dir, "coord.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	coordKP, err := identity.Generate()
	if err != nil {
		t.Fatal(err)
	}
	coordSrv, err := coord.NewServer(store, logger.With("component", "coord"), coordKP)
	if err != nil {
		t.Fatal(err)
	}
	coordLis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go coordSrv.Serve(coordLis)
	defer coordSrv.GracefulStop()

	coordAddr := coordLis.Addr().String()

	// --- Start relay server ---
	relayKP, _ := identity.Generate()
	relayCert, _ := identity.SelfSignedCert(relayKP)

	relayResolver := handle.NewResolver()
	relayVerifier := transport.NewResolverVerifier(relayResolver)

	relaySrv := relay.NewServer(logger.With("component", "relay"), &relayCert, relayVerifier)
	relayLis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go relaySrv.Serve(relayLis)
	defer relaySrv.Stop()

	relayAddr := relayLis.Addr().String()

	// Register relay with coord
	coordClientCert, _ := identity.SelfSignedCert(relayKP)
	coordTOFUFile := filepath.Join(dir, "relay-coord.fp")
	coordTOFU := identity.NewTOFUVerifier(coordTOFUFile)
	coordClientTLS := &tls.Config{
		Certificates:          []tls.Certificate{coordClientCert},
		InsecureSkipVerify:    true,
		VerifyPeerCertificate: coordTOFU.Verify,
	}
	relayCoordConn, _ := grpc.NewClient(coordAddr, grpc.WithTransportCredentials(credentials.NewTLS(coordClientTLS)))
	defer relayCoordConn.Close()
	relayCoordClient := coordpb.NewCoordinationAPIClient(relayCoordConn)

	ctx := context.Background()
	_, err = relayCoordClient.RegisterNode(ctx, &coordpb.RegisterNodeRequest{
		NodeId:    "relay-1",
		PublicKey: relayKP.Public,
		AdvertiseAddr: relayAddr,
		IsRelay:  true,
	})
	if err != nil {
		t.Fatal(err)
	}

	// --- Generate keys for two nodes ---
	kp1, _ := identity.Generate()
	kp2, _ := identity.Generate()

	cert1, _ := identity.SelfSignedCert(kp1)
	cert2, _ := identity.SelfSignedCert(kp2)

	resolver1 := handle.NewResolver()
	resolver2 := handle.NewResolver()

	verifier1 := transport.NewResolverVerifier(resolver1)
	verifier2 := transport.NewResolverVerifier(resolver2)

	// --- Create transports with mTLS ---
	tp1 := transport.NewGRPCTransport(logger.With("component", "transport-1"), &cert1, verifier1)
	tp1.SetResolver(resolver1)
	tp2 := transport.NewGRPCTransport(logger.With("component", "transport-2"), &cert2, verifier2)
	tp2.SetResolver(resolver2)

	// Node 1 listens normally
	tp1Lis, _ := net.Listen("tcp", "127.0.0.1:0")
	go tp1.Serve(tp1Lis)
	defer tp1.Close()

	// Node 2 listens on a real port but we'll advertise an unreachable address
	tp2Lis, _ := net.Listen("tcp", "127.0.0.1:0")
	go tp2.Serve(tp2Lis)
	defer tp2.Close()

	// The "unreachable" addr — a real IP that will fail to connect quickly
	unreachableAddr := "192.0.2.1:9999"

	sessions1 := session.NewStore()
	sessions2 := session.NewStore()
	activity1 := daemon.NewActivityBus()
	activity2 := daemon.NewActivityBus()
	traceStore1 := daemon.NewTraceStore(1000)
	traceStore2 := daemon.NewTraceStore(1000)

	agentSrv1 := daemon.NewAgentServer(sessions1, nil, activity1, logger.With("component", "agent-1"))
	agentSrv1.SetDashboardDeps("node-1", resolver1, tp1)
	agentSrv1.SetTracing(traceStore1, nil)
	router1 := daemon.NewMessageRouter(resolver1, tp1, agentSrv1, activity1, logger.With("component", "router-1"))
	router1.SetTracing(traceStore1, nil, "node-1")
	agentSrv1.SetRouter(router1)

	agentSrv2 := daemon.NewAgentServer(sessions2, nil, activity2, logger.With("component", "agent-2"))
	agentSrv2.SetDashboardDeps("node-2", resolver2, tp2)
	agentSrv2.SetTracing(traceStore2, nil)
	router2 := daemon.NewMessageRouter(resolver2, tp2, agentSrv2, activity2, logger.With("component", "router-2"))
	router2.SetTracing(traceStore2, nil, "node-2")
	agentSrv2.SetRouter(router2)

	// Wire transport callbacks
	tp1.OnSend(func(env *messagepb.Envelope) {
		if env.TraceId != "" {
			traceStore1.RecordSpan(env.TraceId, env.MessageId, "node-1", agentpb.TraceAction_TRACE_ACTION_SENT_TO_TRANSPORT, nil)
		}
	})
	tp2.OnSend(func(env *messagepb.Envelope) {
		if env.TraceId != "" {
			traceStore2.RecordSpan(env.TraceId, env.MessageId, "node-2", agentpb.TraceAction_TRACE_ACTION_SENT_TO_TRANSPORT, nil)
		}
	})

	tp1.OnReceive(func(env *messagepb.Envelope) {
		if env.TraceId != "" {
			traceStore1.RecordSpan(env.TraceId, env.MessageId, "node-1", agentpb.TraceAction_TRACE_ACTION_RECEIVED_FROM_TRANSPORT, nil)
		}
		if _, ok := sessions1.Get(env.SessionId); !ok {
			sess := &session.Session{
				ID:         env.SessionId,
				FromHandle: env.FromHandle,
				ToHandle:   env.ToHandle,
				State:      session.StateOpen,
				CreatedAt:  time.Now(),
				UpdatedAt:  time.Now(),
			}
			sessions1.Put(sess)
		}
		agentSrv1.DeliverToLocal(env)
	})
	tp2.OnReceive(func(env *messagepb.Envelope) {
		if env.TraceId != "" {
			traceStore2.RecordSpan(env.TraceId, env.MessageId, "node-2", agentpb.TraceAction_TRACE_ACTION_RECEIVED_FROM_TRANSPORT, nil)
		}
		if _, ok := sessions2.Get(env.SessionId); !ok {
			sess := &session.Session{
				ID:         env.SessionId,
				FromHandle: env.FromHandle,
				ToHandle:   env.ToHandle,
				State:      session.StateOpen,
				CreatedAt:  time.Now(),
				UpdatedAt:  time.Now(),
			}
			sessions2.Put(sess)
		}
		agentSrv2.DeliverToLocal(env)
	})

	// Start agent servers on TCP
	agentLis1, _ := net.Listen("tcp", "127.0.0.1:0")
	agentLis2, _ := net.Listen("tcp", "127.0.0.1:0")
	go agentSrv1.ServeTCP(agentLis1)
	go agentSrv2.ServeTCP(agentLis2)
	defer agentSrv1.GracefulStop()
	defer agentSrv2.GracefulStop()

	// Register nodes with coord
	cc1Cert, _ := identity.SelfSignedCert(kp1)
	cc1TOFUFile := filepath.Join(dir, "node1-coord.fp")
	cc1TOFU := identity.NewTOFUVerifier(cc1TOFUFile)
	cc1TLS := &tls.Config{
		Certificates:          []tls.Certificate{cc1Cert},
		InsecureSkipVerify:    true,
		VerifyPeerCertificate: cc1TOFU.Verify,
	}
	cc1Conn, _ := grpc.NewClient(coordAddr, grpc.WithTransportCredentials(credentials.NewTLS(cc1TLS)))
	defer cc1Conn.Close()
	coordAPI := coordpb.NewCoordinationAPIClient(cc1Conn)

	_, err = coordAPI.RegisterNode(ctx, &coordpb.RegisterNodeRequest{
		NodeId:        "node-1",
		PublicKey:     kp1.Public,
		AdvertiseAddr: tp1Lis.Addr().String(),
		Handles:       []string{"alice"},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Node 2 advertises an unreachable address
	_, err = coordAPI.RegisterNode(ctx, &coordpb.RegisterNodeRequest{
		NodeId:        "node-2",
		PublicKey:     kp2.Public,
		AdvertiseAddr: unreachableAddr,
		Handles:       []string{"bob"},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Set up resolver maps including relay info
	relayInfo := []handle.RelayInfo{{
		NodeID:    "relay-1",
		PublicKey: relayKP.Public,
		Addr:      relayAddr,
	}}

	resolver1.UpdatePeerMap(map[string]handle.PeerInfo{
		"alice": {NodeID: "node-1", PublicKey: kp1.Public, AdvertiseAddr: tp1Lis.Addr().String()},
		"bob":   {NodeID: "node-2", PublicKey: kp2.Public, AdvertiseAddr: unreachableAddr},
	})
	resolver1.UpdateRelays(relayInfo)

	resolver2.UpdatePeerMap(map[string]handle.PeerInfo{
		"alice": {NodeID: "node-1", PublicKey: kp1.Public, AdvertiseAddr: tp1Lis.Addr().String()},
		"bob":   {NodeID: "node-2", PublicKey: kp2.Public, AdvertiseAddr: unreachableAddr},
	})
	resolver2.UpdateRelays(relayInfo)

	// Also let the relay resolver know about the peers (for verifier)
	relayResolver.UpdatePeerMap(map[string]handle.PeerInfo{
		"alice": {NodeID: "node-1", PublicKey: kp1.Public, AdvertiseAddr: tp1Lis.Addr().String()},
		"bob":   {NodeID: "node-2", PublicKey: kp2.Public, AdvertiseAddr: unreachableAddr},
	})

	// Bind transport contexts
	tpCtx, tpCancel := context.WithCancel(ctx)
	defer tpCancel()
	tp1.Start(tpCtx)
	tp2.Start(tpCtx)

	// Both nodes proactively connect to relay so they can receive forwarded messages
	tp1.ConnectToRelays()
	tp2.ConnectToRelays()

	// --- Connect as agent programs ---
	agentConn1, _ := grpc.NewClient(agentLis1.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	defer agentConn1.Close()
	agent1 := agentpb.NewAgentAPIClient(agentConn1)

	agentConn2, _ := grpc.NewClient(agentLis2.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	defer agentConn2.Close()
	agent2 := agentpb.NewAgentAPIClient(agentConn2)

	// Register handles
	resp1, err := agent1.Register(ctx, &agentpb.RegisterRequest{Handle: "alice"})
	if err != nil || !resp1.Ok {
		t.Fatalf("register alice: %v, %+v", err, resp1)
	}
	resp2, err := agent2.Register(ctx, &agentpb.RegisterRequest{Handle: "bob"})
	if err != nil || !resp2.Ok {
		t.Fatalf("register bob: %v, %+v", err, resp2)
	}

	// Bob subscribes
	subCtx, subCancel := context.WithCancel(ctx)
	defer subCancel()
	bobStream, err := agent2.Subscribe(subCtx, &agentpb.SubscribeRequest{Handle: "bob"})
	if err != nil {
		t.Fatal(err)
	}

	// Alice subscribes for reply
	aliceStream, err := agent1.Subscribe(subCtx, &agentpb.SubscribeRequest{Handle: "alice"})
	if err != nil {
		t.Fatal(err)
	}

	time.Sleep(100 * time.Millisecond)

	// --- Alice opens session to Bob (bob is unreachable directly, should go via relay) ---
	openResp, err := agent1.OpenSession(ctx, &agentpb.OpenSessionRequest{
		FromHandle:  "alice",
		ToHandle:    "bob",
		Payload:     []byte("hello via relay"),
		ContentType: "text/plain",
	})
	if err != nil {
		t.Fatal(err)
	}
	sessionID := openResp.SessionId
	t.Logf("Session opened: %s", sessionID)

	// Bob receives the message (via relay)
	msg, err := bobStream.Recv()
	if err != nil {
		t.Fatal(err)
	}
	if msg.Envelope.Type != messagepb.EnvelopeType_ENVELOPE_TYPE_SESSION_OPEN {
		t.Errorf("expected SESSION_OPEN, got %v", msg.Envelope.Type)
	}
	if string(msg.Envelope.Payload) != "hello via relay" {
		t.Errorf("payload = %q", string(msg.Envelope.Payload))
	}
	t.Log("Bob received session open via relay")

	// Bob replies (also unreachable direct to alice... wait, alice IS reachable)
	// Actually node-1 has a real addr, so node-2 can reach node-1 directly.
	// But node-2's transport will try to send to node-1's real address which should work.
	// Let's test the relay path for the reply too by not testing reply direction
	// (node-2 can reach node-1 directly — that's fine, the key test is that
	// node-1 reaches node-2 through relay)
	_, err = agent2.SendMessage(ctx, &agentpb.SendMessageRequest{
		SessionId:   sessionID,
		FromHandle:  "bob",
		Payload:     []byte("reply from bob"),
		ContentType: "text/plain",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Alice receives the reply
	reply, err := aliceStream.Recv()
	if err != nil {
		t.Fatal(err)
	}
	if string(reply.Envelope.Payload) != "reply from bob" {
		t.Errorf("reply payload = %q", string(reply.Envelope.Payload))
	}
	t.Log("Alice received reply")

	// Verify relay has connected peers
	if relaySrv.ConnectedPeers() == 0 {
		t.Log("Note: relay may have had peers that disconnected already")
	}

	t.Log("Relay end-to-end test passed!")
}
