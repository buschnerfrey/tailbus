package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	agentpb "github.com/alexanderfrey/tailbus/api/agentpb"
	messagepb "github.com/alexanderfrey/tailbus/api/messagepb"
	"github.com/alexanderfrey/tailbus/internal/session"
	"google.golang.org/grpc/metadata"
)

// --- Mock agent API ---

type mockAgent struct {
	mu       sync.Mutex
	handles  []*agentpb.HandleEntry
	sessions map[string]*session.Session
	respCh   chan *agentpb.IncomingMessage // subscriber receives these
}

func newMockAgent() *mockAgent {
	return &mockAgent{
		sessions: make(map[string]*session.Session),
		respCh:   make(chan *agentpb.IncomingMessage, 10),
	}
}

func (m *mockAgent) Register(_ context.Context, req *agentpb.RegisterRequest) (*agentpb.RegisterResponse, error) {
	return &agentpb.RegisterResponse{Ok: true}, nil
}

func (m *mockAgent) OpenSession(_ context.Context, req *agentpb.OpenSessionRequest) (*agentpb.OpenSessionResponse, error) {
	sess := session.New(req.FromHandle, req.ToHandle)
	m.mu.Lock()
	m.sessions[sess.ID] = sess
	m.mu.Unlock()

	// Simulate agent response after a short delay
	go func() {
		time.Sleep(10 * time.Millisecond)
		m.respCh <- &agentpb.IncomingMessage{
			Envelope: &messagepb.Envelope{
				MessageId:  "resp-1",
				SessionId:  sess.ID,
				FromHandle: req.ToHandle,
				ToHandle:   req.FromHandle,
				Payload:    []byte("42"),
				Type:       messagepb.EnvelopeType_ENVELOPE_TYPE_SESSION_RESOLVE,
			},
		}
	}()

	return &agentpb.OpenSessionResponse{SessionId: sess.ID, MessageId: "msg-1"}, nil
}

func (m *mockAgent) Subscribe(req *agentpb.SubscribeRequest, stream agentpb.AgentAPI_SubscribeServer) error {
	for {
		select {
		case msg := <-m.respCh:
			if err := stream.Send(msg); err != nil {
				return err
			}
		case <-stream.Context().Done():
			return nil
		}
	}
}

func (m *mockAgent) ListHandles(_ context.Context, _ *agentpb.ListHandlesRequest) (*agentpb.ListHandlesResponse, error) {
	return &agentpb.ListHandlesResponse{
		Entries: []*agentpb.HandleEntry{
			{
				Handle: "calculator",
				Manifest: &messagepb.ServiceManifest{
					Description: "A calculator agent",
					Commands: []*messagepb.CommandSpec{
						{Name: "add", Description: "Add two numbers", ParametersSchema: `{"type":"object","properties":{"a":{"type":"number"},"b":{"type":"number"}}}`},
					},
					Tags: []string{"math"},
				},
			},
			{
				Handle: "echo",
				Manifest: &messagepb.ServiceManifest{
					Description: "Echoes messages back",
				},
			},
		},
	}, nil
}

func (m *mockAgent) IntrospectHandle(_ context.Context, req *agentpb.IntrospectHandleRequest) (*agentpb.IntrospectHandleResponse, error) {
	return &agentpb.IntrospectHandleResponse{Handle: req.Handle, Found: true}, nil
}

func (m *mockAgent) ResolveSession(_ context.Context, _ *agentpb.ResolveSessionRequest) (*agentpb.ResolveSessionResponse, error) {
	return &agentpb.ResolveSessionResponse{MessageId: "resolve-1"}, nil
}

// --- Mock session store ---

type mockSessionStore struct{}

func (m *mockSessionStore) Get(id string) (*session.Session, bool) {
	return nil, false
}

// --- Mock subscribe stream ---

type testSubscribeStream struct {
	ctx context.Context
	ch  chan *agentpb.IncomingMessage
}

func (s *testSubscribeStream) Send(msg *agentpb.IncomingMessage) error {
	s.ch <- msg
	return nil
}

func (s *testSubscribeStream) Context() context.Context        { return s.ctx }
func (s *testSubscribeStream) SetHeader(_ metadata.MD) error   { return nil }
func (s *testSubscribeStream) SendHeader(_ metadata.MD) error  { return nil }
func (s *testSubscribeStream) SetTrailer(_ metadata.MD)        {}
func (s *testSubscribeStream) SendMsg(_ interface{}) error     { return nil }
func (s *testSubscribeStream) RecvMsg(_ interface{}) error     { return nil }

// --- Tests ---

func TestGateway_Initialize(t *testing.T) {
	agent := newMockAgent()
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	gw := NewGateway(agent, &mockSessionStore{}, logger)

	reqBody, _ := json.Marshal(jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      1,
		Method:  "initialize",
		Params:  json.RawMessage(`{}`),
	})

	req := httptest.NewRequest("POST", "/mcp", bytes.NewReader(reqBody))
	w := httptest.NewRecorder()
	gw.Handler().ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp jsonRPCResponse
	json.NewDecoder(w.Body).Decode(&resp)
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}

	result := resp.Result.(map[string]any)
	if result["protocolVersion"] != mcpProtocolVersion {
		t.Fatalf("wrong protocol version: %v", result["protocolVersion"])
	}
}

func TestGateway_ToolsList(t *testing.T) {
	agent := newMockAgent()
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	gw := NewGateway(agent, &mockSessionStore{}, logger)

	reqBody, _ := json.Marshal(jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      2,
		Method:  "tools/list",
	})

	req := httptest.NewRequest("POST", "/mcp", bytes.NewReader(reqBody))
	w := httptest.NewRecorder()
	gw.Handler().ServeHTTP(w, req)

	var resp jsonRPCResponse
	json.NewDecoder(w.Body).Decode(&resp)
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}

	result := resp.Result.(map[string]any)
	tools := result["tools"].([]any)

	// calculator.add + echo = 2 tools
	if len(tools) != 2 {
		t.Fatalf("expected 2 tools, got %d: %+v", len(tools), tools)
	}

	// Check calculator.add tool
	tool0 := tools[0].(map[string]any)
	if tool0["name"] != "calculator.add" {
		t.Fatalf("expected calculator.add, got %s", tool0["name"])
	}

	// Check echo tool (generic)
	tool1 := tools[1].(map[string]any)
	if tool1["name"] != "echo" {
		t.Fatalf("expected echo, got %s", tool1["name"])
	}
}

func TestGateway_ToolsCall(t *testing.T) {
	agent := newMockAgent()
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	gw := NewGateway(agent, &mockSessionStore{}, logger)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := gw.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}

	// Give subscriber goroutine time to start
	time.Sleep(50 * time.Millisecond)

	reqBody, _ := json.Marshal(jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      3,
		Method:  "tools/call",
		Params:  json.RawMessage(`{"name":"echo","arguments":{"message":"hello"}}`),
	})

	req := httptest.NewRequest("POST", "/mcp", bytes.NewReader(reqBody))
	w := httptest.NewRecorder()
	gw.Handler().ServeHTTP(w, req)

	var resp jsonRPCResponse
	json.NewDecoder(w.Body).Decode(&resp)
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}

	result := resp.Result.(map[string]any)
	content := result["content"].([]any)
	if len(content) == 0 {
		t.Fatalf("expected content, got empty")
	}
	item := content[0].(map[string]any)
	if item["text"] != "42" {
		t.Fatalf("expected '42', got %q", item["text"])
	}
}

func TestParseToolName(t *testing.T) {
	tests := []struct {
		input   string
		handle  string
		command string
	}{
		{"echo", "echo", ""},
		{"calculator.add", "calculator", "add"},
		{"my-service.do.thing", "my-service.do", "thing"},
	}
	for _, tt := range tests {
		h, c := parseToolName(tt.input)
		if h != tt.handle || c != tt.command {
			t.Errorf("parseToolName(%q) = (%q, %q), want (%q, %q)", tt.input, h, c, tt.handle, tt.command)
		}
	}
}
