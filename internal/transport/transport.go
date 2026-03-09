package transport

import (
	"context"

	transportpb "github.com/alexanderfrey/tailbus/api/transportpb"
)

// Transport defines the interface for P2P communication between daemons.
type Transport interface {
	// Send sends a transport message to a peer at the given address.
	// The context controls the deadline for connection establishment and
	// the send operation; the underlying stream lives beyond the call.
	Send(ctx context.Context, addr string, msg *transportpb.TransportMessage) error

	// OnReceive registers a callback for incoming transport messages.
	OnReceive(fn func(*transportpb.TransportMessage))

	// Close shuts down the transport.
	Close() error
}
