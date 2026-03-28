// Package chat implements a web-based chat UI for the tailbus agent mesh.
// It registers each authenticated user as a handle on the mesh and bridges
// HTTP/WebSocket traffic to the daemon's AgentServer RPCs.
package chat

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"

	agentpb "github.com/alexanderfrey/tailbus/api/agentpb"
	messagepb "github.com/alexanderfrey/tailbus/api/messagepb"
	"github.com/alexanderfrey/tailbus/internal/session"
)

// AgentAPI is the interface to the daemon's agent server that the chat gateway needs.
type AgentAPI interface {
	Register(ctx context.Context, req *agentpb.RegisterRequest) (*agentpb.RegisterResponse, error)
	OpenSession(ctx context.Context, req *agentpb.OpenSessionRequest) (*agentpb.OpenSessionResponse, error)
	SendMessage(ctx context.Context, req *agentpb.SendMessageRequest) (*agentpb.SendMessageResponse, error)
	ResolveSession(ctx context.Context, req *agentpb.ResolveSessionRequest) (*agentpb.ResolveSessionResponse, error)
	Subscribe(req *agentpb.SubscribeRequest, stream agentpb.AgentAPI_SubscribeServer) error
	ListHandles(ctx context.Context, req *agentpb.ListHandlesRequest) (*agentpb.ListHandlesResponse, error)
	IntrospectHandle(ctx context.Context, req *agentpb.IntrospectHandleRequest) (*agentpb.IntrospectHandleResponse, error)
	FindHandles(ctx context.Context, req *agentpb.FindHandlesRequest) (*agentpb.FindHandlesResponse, error)
	CreateRoom(ctx context.Context, req *agentpb.CreateRoomRequest) (*agentpb.CreateRoomResponse, error)
	JoinRoom(ctx context.Context, req *agentpb.JoinRoomRequest) (*agentpb.JoinRoomResponse, error)
	LeaveRoom(ctx context.Context, req *agentpb.LeaveRoomRequest) (*agentpb.LeaveRoomResponse, error)
	PostRoomMessage(ctx context.Context, req *agentpb.PostRoomMessageRequest) (*agentpb.PostRoomMessageResponse, error)
	ListRooms(ctx context.Context, req *agentpb.ListRoomsRequest) (*agentpb.ListRoomsResponse, error)
	ListRoomMembers(ctx context.Context, req *agentpb.ListRoomMembersRequest) (*agentpb.ListRoomMembersResponse, error)
	ReplayRoom(ctx context.Context, req *agentpb.ReplayRoomRequest) (*agentpb.ReplayRoomResponse, error)
	CloseRoom(ctx context.Context, req *agentpb.CloseRoomRequest) (*agentpb.CloseRoomResponse, error)
	ListSessions(ctx context.Context, req *agentpb.ListSessionsRequest) (*agentpb.ListSessionsResponse, error)
	GetNodeStatus(ctx context.Context, req *agentpb.GetNodeStatusRequest) (*agentpb.GetNodeStatusResponse, error)
}

// SessionStore provides read access to the session store.
type SessionStore interface {
	Get(id string) (*session.Session, bool)
}

// ActivityBusSubscriber can subscribe to mesh activity events.
type ActivityBusSubscriber interface {
	Subscribe() chan *agentpb.ActivityEvent
	Unsubscribe(ch chan *agentpb.ActivityEvent)
}

const chatUserHandle = "u-you"

// Gateway is the chat UI HTTP gateway.
type Gateway struct {
	agent     AgentAPI
	sessions  SessionStore
	activity  ActivityBusSubscriber
	meshToken string
	users     *UserManager
	logger    *slog.Logger
}

// NewGateway creates a new chat gateway.
func NewGateway(agent AgentAPI, sessions SessionStore, activity ActivityBusSubscriber, meshToken string, logger *slog.Logger) *Gateway {
	return &Gateway{
		agent:     agent,
		sessions:  sessions,
		activity:  activity,
		meshToken: meshToken,
		users:     NewUserManager(agent, logger),
		logger:    logger,
	}
}

// Start registers the default user handle on the mesh.
func (g *Gateway) Start(ctx context.Context) error {
	// For mesh-token mode, register a single user handle
	resp, err := g.agent.Register(ctx, &agentpb.RegisterRequest{
		Handle: chatUserHandle,
		Manifest: &messagepb.ServiceManifest{
			Description: "Chat UI user",
			Tags:        []string{"human", "chat-ui"},
		},
	})
	if err != nil {
		return err
	}
	if !resp.Ok {
		return fmt.Errorf("register chat handle: %s", resp.Error)
	}

	// Start subscription goroutine
	g.users.StartSubscription(ctx, chatUserHandle)

	return nil
}

// Handler returns the HTTP handler for the chat UI.
func (g *Gateway) Handler() http.Handler {
	mux := http.NewServeMux()

	// API routes (mesh token auth)
	mux.HandleFunc("GET /api/me", g.withAuth(g.handleMe))
	mux.HandleFunc("GET /api/agents", g.withAuth(g.handleListAgents))
	mux.HandleFunc("GET /api/agents/{handle}", g.withAuth(g.handleIntrospectAgent))
	mux.HandleFunc("POST /api/agents/{handle}/message", g.withAuth(g.handleSendToAgent))
	mux.HandleFunc("POST /api/rooms", g.withAuth(g.handleCreateRoom))
	mux.HandleFunc("GET /api/rooms", g.withAuth(g.handleListRooms))
	mux.HandleFunc("GET /api/rooms/{id}/messages", g.withAuth(g.handleReplayRoom))
	mux.HandleFunc("POST /api/rooms/{id}/messages", g.withAuth(g.handlePostRoomMessage))
	mux.HandleFunc("POST /api/rooms/{id}/join", g.withAuth(g.handleJoinRoom))
	mux.HandleFunc("GET /api/status", g.withAuth(g.handleNodeStatus))
	mux.HandleFunc("GET /ws", g.handleWebSocket)

	// Static files (no auth — the SPA handles the login flow client-side)
	mux.Handle("/", http.FileServer(http.FS(webUIFS())))

	return mux
}
