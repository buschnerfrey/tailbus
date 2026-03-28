package chat

import (
	"context"
	"log/slog"
	"sync"

	agentpb "github.com/alexanderfrey/tailbus/api/agentpb"
	"google.golang.org/grpc/metadata"
)

// UserManager manages the subscription for the chat user handle and fans
// incoming messages out to all connected WebSocket clients.
type UserManager struct {
	agent  AgentAPI
	logger *slog.Logger

	mu      sync.RWMutex
	clients map[*wsClient]struct{} // active WebSocket connections
}

// wsClient is a single WebSocket connection receiving messages.
type wsClient struct {
	ch chan []byte // JSON-encoded messages to send
}

// NewUserManager creates a new user manager.
func NewUserManager(agent AgentAPI, logger *slog.Logger) *UserManager {
	return &UserManager{
		agent:   agent,
		logger:  logger,
		clients: make(map[*wsClient]struct{}),
	}
}

// StartSubscription starts listening for incoming messages on the given handle
// and fans them out to all connected WebSocket clients.
func (um *UserManager) StartSubscription(ctx context.Context, handle string) {
	stream := &memSubscribeStream{
		ctx: ctx,
		ch:  make(chan *agentpb.IncomingMessage, 256),
	}

	go func() {
		err := um.agent.Subscribe(&agentpb.SubscribeRequest{Handle: handle}, stream)
		if err != nil && ctx.Err() == nil {
			um.logger.Error("chat subscribe error", "error", err)
		}
	}()

	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case msg := <-stream.ch:
				if msg == nil {
					return
				}
				um.broadcast(msg)
			}
		}
	}()
}

// AddClient registers a WebSocket client for message fan-out.
func (um *UserManager) AddClient(c *wsClient) {
	um.mu.Lock()
	um.clients[c] = struct{}{}
	um.mu.Unlock()
}

// RemoveClient unregisters a WebSocket client.
func (um *UserManager) RemoveClient(c *wsClient) {
	um.mu.Lock()
	delete(um.clients, c)
	um.mu.Unlock()
}

// broadcast sends a JSON-encoded message to all connected clients.
func (um *UserManager) broadcast(msg *agentpb.IncomingMessage) {
	data := encodeIncomingMessage(msg)
	if data == nil {
		return
	}

	um.mu.RLock()
	defer um.mu.RUnlock()

	for c := range um.clients {
		select {
		case c.ch <- data:
		default:
			// Client is slow, drop message
		}
	}
}

// --- In-memory subscribe stream adapter (same pattern as internal/mcp) ---

type memSubscribeStream struct {
	ctx context.Context
	ch  chan *agentpb.IncomingMessage
}

func (s *memSubscribeStream) Send(msg *agentpb.IncomingMessage) error {
	select {
	case s.ch <- msg:
		return nil
	case <-s.ctx.Done():
		return s.ctx.Err()
	}
}

func (s *memSubscribeStream) Context() context.Context            { return s.ctx }
func (s *memSubscribeStream) SetHeader(_ metadata.MD) error       { return nil }
func (s *memSubscribeStream) SendHeader(_ metadata.MD) error      { return nil }
func (s *memSubscribeStream) SetTrailer(_ metadata.MD)            {}
func (s *memSubscribeStream) SendMsg(_ interface{}) error         { return nil }
func (s *memSubscribeStream) RecvMsg(_ interface{}) error         { return nil }
