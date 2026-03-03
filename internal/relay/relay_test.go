package relay

import (
	"context"
	"crypto/tls"
	"encoding/hex"
	"log/slog"
	"net"
	"os"
	"testing"
	"time"

	messagepb "github.com/alexanderfrey/tailbus/api/messagepb"
	transportpb "github.com/alexanderfrey/tailbus/api/transportpb"
	"github.com/alexanderfrey/tailbus/internal/identity"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

// noopVerifier allows all peers (for testing without full peer map).
type noopVerifier struct{}

func (noopVerifier) VerifyPeerKey(string, []byte) error { return nil }

func TestRelayForwarding(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))

	// Generate relay keypair and cert
	relayKP, err := identity.Generate()
	if err != nil {
		t.Fatal(err)
	}
	relayCert, err := identity.SelfSignedCert(relayKP)
	if err != nil {
		t.Fatal(err)
	}

	// Create relay server
	srv := NewServer(logger, &relayCert, noopVerifier{})
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve(lis)
	defer srv.Stop()

	relayAddr := lis.Addr().String()

	// Generate two client keypairs
	kpA, _ := identity.Generate()
	kpB, _ := identity.Generate()

	certA, _ := identity.SelfSignedCert(kpA)
	certB, _ := identity.SelfSignedCert(kpB)

	// Connect daemon A to relay
	streamA := dialRelay(t, relayAddr, certA)
	// Connect daemon B to relay
	streamB := dialRelay(t, relayAddr, certB)

	// Give streams time to register
	time.Sleep(100 * time.Millisecond)

	if srv.ConnectedPeers() != 2 {
		t.Fatalf("expected 2 connected peers, got %d", srv.ConnectedPeers())
	}

	// A sends to B via relay
	env := &messagepb.Envelope{
		MessageId:      "msg-1",
		SessionId:      "sess-1",
		FromHandle:     "alice",
		ToHandle:       "bob",
		Payload:        []byte("hello via relay"),
		Type:           messagepb.EnvelopeType_ENVELOPE_TYPE_MESSAGE,
		RelayTargetKey: kpB.Public,
	}

	if err := streamA.Send(env); err != nil {
		t.Fatalf("A send: %v", err)
	}

	// B receives
	received, err := streamB.Recv()
	if err != nil {
		t.Fatalf("B recv: %v", err)
	}

	if received.MessageId != "msg-1" {
		t.Errorf("message_id = %q, want msg-1", received.MessageId)
	}
	if string(received.Payload) != "hello via relay" {
		t.Errorf("payload = %q", string(received.Payload))
	}
	// The relay_target_key should still be present (relay doesn't strip it)
	if hex.EncodeToString(received.RelayTargetKey) != hex.EncodeToString(kpB.Public) {
		t.Error("relay_target_key changed unexpectedly")
	}

	// B sends back to A
	reply := &messagepb.Envelope{
		MessageId:      "msg-2",
		SessionId:      "sess-1",
		FromHandle:     "bob",
		ToHandle:       "alice",
		Payload:        []byte("reply via relay"),
		Type:           messagepb.EnvelopeType_ENVELOPE_TYPE_MESSAGE,
		RelayTargetKey: kpA.Public,
	}

	if err := streamB.Send(reply); err != nil {
		t.Fatalf("B send: %v", err)
	}

	receivedReply, err := streamA.Recv()
	if err != nil {
		t.Fatalf("A recv: %v", err)
	}

	if receivedReply.MessageId != "msg-2" {
		t.Errorf("reply message_id = %q, want msg-2", receivedReply.MessageId)
	}
	if string(receivedReply.Payload) != "reply via relay" {
		t.Errorf("reply payload = %q", string(receivedReply.Payload))
	}

	t.Log("Relay forwarding test passed")
}

func TestRelayTargetNotConnected(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))

	relayKP, _ := identity.Generate()
	relayCert, _ := identity.SelfSignedCert(relayKP)

	srv := NewServer(logger, &relayCert, noopVerifier{})
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve(lis)
	defer srv.Stop()

	kpA, _ := identity.Generate()
	kpC, _ := identity.Generate() // not connected

	certA, _ := identity.SelfSignedCert(kpA)
	streamA := dialRelay(t, lis.Addr().String(), certA)

	time.Sleep(50 * time.Millisecond)

	// Send to non-existent peer — should not error on sender side (relay just logs warning)
	env := &messagepb.Envelope{
		MessageId:      "msg-orphan",
		RelayTargetKey: kpC.Public,
	}
	if err := streamA.Send(env); err != nil {
		t.Fatalf("send: %v", err)
	}

	// Give relay a moment to process
	time.Sleep(50 * time.Millisecond)
	t.Log("Relay target-not-connected test passed (no crash)")
}

func dialRelay(t *testing.T, addr string, cert tls.Certificate) transportpb.NodeTransport_ExchangeClient {
	t.Helper()

	clientTLS := &tls.Config{
		Certificates:       []tls.Certificate{cert},
		InsecureSkipVerify: true,
	}

	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(credentials.NewTLS(clientTLS)))
	if err != nil {
		t.Fatalf("dial relay: %v", err)
	}
	t.Cleanup(func() { conn.Close() })

	client := transportpb.NewNodeTransportClient(conn)
	stream, err := client.Exchange(context.Background())
	if err != nil {
		t.Fatalf("open exchange: %v", err)
	}
	return stream
}
