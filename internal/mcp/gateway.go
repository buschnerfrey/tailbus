// Package mcp implements an MCP (Model Context Protocol) gateway that exposes
// tailbus handles as MCP tools. LLM clients can discover agents via tools/list
// and invoke them via tools/call.
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	agentpb "github.com/alexanderfrey/tailbus/api/agentpb"
	messagepb "github.com/alexanderfrey/tailbus/api/messagepb"
	"github.com/alexanderfrey/tailbus/internal/session"
	"google.golang.org/grpc/metadata"
)

const (
	mcpProtocolVersion = "2024-11-05"
	mcpGatewayHandle   = "_mcp_gateway"
	defaultCallTimeout = 30 * time.Second
)

// AgentAPI is the interface to the daemon's agent server that the gateway needs.
type AgentAPI interface {
	Register(ctx context.Context, req *agentpb.RegisterRequest) (*agentpb.RegisterResponse, error)
	OpenSession(ctx context.Context, req *agentpb.OpenSessionRequest) (*agentpb.OpenSessionResponse, error)
	Subscribe(req *agentpb.SubscribeRequest, stream agentpb.AgentAPI_SubscribeServer) error
	ListHandles(ctx context.Context, req *agentpb.ListHandlesRequest) (*agentpb.ListHandlesResponse, error)
	IntrospectHandle(ctx context.Context, req *agentpb.IntrospectHandleRequest) (*agentpb.IntrospectHandleResponse, error)
	ResolveSession(ctx context.Context, req *agentpb.ResolveSessionRequest) (*agentpb.ResolveSessionResponse, error)
}

// SessionStore is the interface to the session store.
type SessionStore interface {
	Get(id string) (*session.Session, bool)
}

// Gateway is the MCP HTTP gateway.
type Gateway struct {
	agent    AgentAPI
	sessions SessionStore
	logger   *slog.Logger

	// subscriber for incoming messages
	mu       sync.Mutex
	waiters  map[string]chan *agentpb.IncomingMessage // sessionID -> waiter
}

// NewGateway creates a new MCP gateway.
func NewGateway(agent AgentAPI, sessions SessionStore, logger *slog.Logger) *Gateway {
	return &Gateway{
		agent:    agent,
		sessions: sessions,
		logger:   logger,
		waiters:  make(map[string]chan *agentpb.IncomingMessage),
	}
}

// Start registers the gateway handle and starts the subscriber goroutine.
func (g *Gateway) Start(ctx context.Context) error {
	// Register a virtual handle for the gateway
	resp, err := g.agent.Register(ctx, &agentpb.RegisterRequest{
		Handle: mcpGatewayHandle,
		Manifest: &messagepb.ServiceManifest{
			Description: "MCP Gateway (internal)",
			Tags:        []string{"internal", "mcp"},
		},
	})
	if err != nil {
		return fmt.Errorf("register mcp gateway handle: %w", err)
	}
	if !resp.Ok {
		return fmt.Errorf("register mcp gateway: %s", resp.Error)
	}

	// Start subscribing for responses
	go g.subscribeLoop(ctx)

	return nil
}

// subscribeLoop receives incoming messages and dispatches to waiters.
func (g *Gateway) subscribeLoop(ctx context.Context) {
	stream := &memSubscribeStream{
		ctx: ctx,
		ch:  make(chan *agentpb.IncomingMessage, 256),
	}

	go func() {
		err := g.agent.Subscribe(&agentpb.SubscribeRequest{Handle: mcpGatewayHandle}, stream)
		if err != nil && ctx.Err() == nil {
			g.logger.Error("mcp gateway subscribe error", "error", err)
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return
		case msg := <-stream.ch:
			if msg == nil {
				return
			}
			sid := msg.Envelope.SessionId
			g.mu.Lock()
			if ch, ok := g.waiters[sid]; ok {
				select {
				case ch <- msg:
				default:
				}
			}
			g.mu.Unlock()
		}
	}
}

// Handler returns the HTTP handler for the MCP gateway.
func (g *Gateway) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /mcp", g.handleMCP)
	mux.HandleFunc("GET /mcp", g.handleMCPSSE)
	return mux
}

// handleMCP handles JSON-RPC requests over HTTP POST (streamable HTTP transport).
func (g *Gateway) handleMCP(w http.ResponseWriter, r *http.Request) {
	var req jsonRPCRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONRPCError(w, nil, -32700, "Parse error")
		return
	}

	result, rpcErr := g.dispatch(r.Context(), &req)
	if rpcErr != nil {
		writeJSONRPCErrorResp(w, req.ID, rpcErr)
		return
	}

	resp := jsonRPCResponse{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result:  result,
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// handleMCPSSE handles the SSE endpoint for server-initiated messages.
func (g *Gateway) handleMCPSSE(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	// Send endpoint event so client knows where to POST
	fmt.Fprintf(w, "event: endpoint\ndata: /mcp\n\n")
	flusher.Flush()

	// Keep alive until client disconnects
	<-r.Context().Done()
}

// dispatch routes a JSON-RPC method to the appropriate handler.
func (g *Gateway) dispatch(ctx context.Context, req *jsonRPCRequest) (any, *jsonRPCError) {
	switch req.Method {
	case "initialize":
		return g.handleInitialize(req)
	case "notifications/initialized":
		return nil, nil // no-op notification
	case "tools/list":
		return g.handleToolsList(ctx)
	case "tools/call":
		return g.handleToolsCall(ctx, req)
	case "ping":
		return map[string]any{}, nil
	default:
		return nil, &jsonRPCError{Code: -32601, Message: fmt.Sprintf("Method not found: %s", req.Method)}
	}
}

func (g *Gateway) handleInitialize(req *jsonRPCRequest) (any, *jsonRPCError) {
	return map[string]any{
		"protocolVersion": mcpProtocolVersion,
		"capabilities": map[string]any{
			"tools": map[string]any{},
		},
		"serverInfo": map[string]any{
			"name":    "tailbus-mcp-gateway",
			"version": "0.1.0",
		},
	}, nil
}

func (g *Gateway) handleToolsList(ctx context.Context) (any, *jsonRPCError) {
	resp, err := g.agent.ListHandles(ctx, &agentpb.ListHandlesRequest{})
	if err != nil {
		return nil, &jsonRPCError{Code: -32603, Message: err.Error()}
	}

	var tools []map[string]any
	for _, entry := range resp.Entries {
		// Skip internal handles
		if entry.Handle == mcpGatewayHandle {
			continue
		}

		if entry.Manifest != nil && len(entry.Manifest.Commands) > 0 {
			// Each command becomes a separate tool
			for _, cmd := range entry.Manifest.Commands {
				tool := map[string]any{
					"name":        fmt.Sprintf("%s.%s", entry.Handle, cmd.Name),
					"description": fmt.Sprintf("[%s] %s", entry.Handle, cmd.Description),
				}
				if cmd.ParametersSchema != "" {
					var schema any
					if json.Unmarshal([]byte(cmd.ParametersSchema), &schema) == nil {
						tool["inputSchema"] = schema
					}
				} else {
					tool["inputSchema"] = defaultInputSchema(entry.Handle, cmd.Name)
				}
				tools = append(tools, tool)
			}
		} else {
			// No commands: expose a generic send tool
			desc := "Send a message to " + entry.Handle
			if entry.Manifest != nil && entry.Manifest.Description != "" {
				desc = fmt.Sprintf("[%s] %s", entry.Handle, entry.Manifest.Description)
			}
			tools = append(tools, map[string]any{
				"name":        entry.Handle,
				"description": desc,
				"inputSchema": defaultInputSchema(entry.Handle, ""),
			})
		}
	}

	return map[string]any{"tools": tools}, nil
}

func defaultInputSchema(handle, command string) map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"message": map[string]any{
				"type":        "string",
				"description": "The message to send",
			},
		},
		"required": []string{"message"},
	}
}

func (g *Gateway) handleToolsCall(ctx context.Context, req *jsonRPCRequest) (any, *jsonRPCError) {
	var params struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return nil, &jsonRPCError{Code: -32602, Message: "Invalid params"}
	}

	// Parse handle and optional command from tool name: "handle.command" or "handle"
	targetHandle, command := parseToolName(params.Name)

	// Extract message from arguments
	var args map[string]any
	if err := json.Unmarshal(params.Arguments, &args); err != nil {
		return nil, &jsonRPCError{Code: -32602, Message: "Invalid arguments"}
	}

	// Build payload
	payload := buildPayload(command, args)

	// Open session, wait for response, resolve
	callCtx, cancel := context.WithTimeout(ctx, defaultCallTimeout)
	defer cancel()

	result, err := g.callAgent(callCtx, targetHandle, payload)
	if err != nil {
		return map[string]any{
			"content": []map[string]any{
				{"type": "text", "text": fmt.Sprintf("Error: %s", err.Error())},
			},
			"isError": true,
		}, nil
	}

	return map[string]any{
		"content": []map[string]any{
			{"type": "text", "text": result},
		},
	}, nil
}

// callAgent opens a session to the target handle, sends the payload, and waits
// for the first response (or session resolve).
func (g *Gateway) callAgent(ctx context.Context, targetHandle string, payload []byte) (string, error) {
	// Open session
	openResp, err := g.agent.OpenSession(ctx, &agentpb.OpenSessionRequest{
		FromHandle:  mcpGatewayHandle,
		ToHandle:    targetHandle,
		Payload:     payload,
		ContentType: "application/json",
	})
	if err != nil {
		return "", fmt.Errorf("open session: %w", err)
	}

	sessionID := openResp.SessionId

	// Set up waiter for response
	ch := make(chan *agentpb.IncomingMessage, 1)
	g.mu.Lock()
	g.waiters[sessionID] = ch
	g.mu.Unlock()
	defer func() {
		g.mu.Lock()
		delete(g.waiters, sessionID)
		g.mu.Unlock()
	}()

	// Wait for response
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case msg := <-ch:
		result := string(msg.Envelope.Payload)

		// If it's a message (not resolve), resolve the session ourselves
		if msg.Envelope.Type == messagepb.EnvelopeType_ENVELOPE_TYPE_MESSAGE {
			g.agent.ResolveSession(ctx, &agentpb.ResolveSessionRequest{
				SessionId:  sessionID,
				FromHandle: mcpGatewayHandle,
			})
		}

		return result, nil
	}
}

func parseToolName(name string) (handle, command string) {
	for i := len(name) - 1; i >= 0; i-- {
		if name[i] == '.' {
			return name[:i], name[i+1:]
		}
	}
	return name, ""
}

func buildPayload(command string, args map[string]any) []byte {
	if command != "" {
		// Structured command invocation
		payload := map[string]any{
			"command":   command,
			"arguments": args,
		}
		b, _ := json.Marshal(payload)
		return b
	}

	// Simple message: just extract the message field
	if msg, ok := args["message"]; ok {
		if s, ok := msg.(string); ok {
			return []byte(s)
		}
	}

	// Fallback: serialize all args
	b, _ := json.Marshal(args)
	return b
}

// --- JSON-RPC types ---

type jsonRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type jsonRPCResponse struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      any         `json:"id"`
	Result  any         `json:"result,omitempty"`
	Error   *jsonRPCError `json:"error,omitempty"`
}

type jsonRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func writeJSONRPCError(w http.ResponseWriter, id any, code int, message string) {
	writeJSONRPCErrorResp(w, id, &jsonRPCError{Code: code, Message: message})
}

func writeJSONRPCErrorResp(w http.ResponseWriter, id any, rpcErr *jsonRPCError) {
	resp := jsonRPCResponse{
		JSONRPC: "2.0",
		ID:      id,
		Error:   rpcErr,
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(resp)
}

// --- In-memory subscribe stream adapter ---

// memSubscribeStream adapts the gRPC Subscribe stream to an in-process channel.
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
func (s *memSubscribeStream) SetHeader(_ metadata.MD) error      { return nil }
func (s *memSubscribeStream) SendHeader(_ metadata.MD) error     { return nil }
func (s *memSubscribeStream) SetTrailer(_ metadata.MD)           {}
func (s *memSubscribeStream) SendMsg(_ interface{}) error        { return nil }
func (s *memSubscribeStream) RecvMsg(_ interface{}) error        { return nil }
