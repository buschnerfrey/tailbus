package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	agentpb "github.com/alexanderfrey/tailbus/api/agentpb"
	messagepb "github.com/alexanderfrey/tailbus/api/messagepb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

type fakeIncomingStream struct {
	msgs []*agentpb.IncomingMessage
	err  error
	idx  int
}

func (s *fakeIncomingStream) Header() (metadata.MD, error) { return metadata.MD{}, nil }
func (s *fakeIncomingStream) Trailer() metadata.MD         { return metadata.MD{} }
func (s *fakeIncomingStream) CloseSend() error             { return nil }
func (s *fakeIncomingStream) Context() context.Context     { return context.Background() }
func (s *fakeIncomingStream) SendMsg(any) error            { return nil }
func (s *fakeIncomingStream) RecvMsg(any) error            { return nil }
func (s *fakeIncomingStream) Recv() (*agentpb.IncomingMessage, error) {
	if s.idx < len(s.msgs) {
		msg := s.msgs[s.idx]
		s.idx++
		return msg, nil
	}
	if s.err != nil {
		err := s.err
		s.err = nil
		return nil, err
	}
	return nil, io.EOF
}

type fakeBridgeClient struct {
	registerReqs []*agentpb.RegisterRequest
	joinRoomReqs []*agentpb.JoinRoomRequest
	listReqs     []*agentpb.ListHandlesRequest
	stream       grpc.ServerStreamingClient[agentpb.IncomingMessage]
}

func (f *fakeBridgeClient) Register(_ context.Context, in *agentpb.RegisterRequest, _ ...grpc.CallOption) (*agentpb.RegisterResponse, error) {
	f.registerReqs = append(f.registerReqs, in)
	return &agentpb.RegisterResponse{Ok: true}, nil
}

func (f *fakeBridgeClient) IntrospectHandle(context.Context, *agentpb.IntrospectHandleRequest, ...grpc.CallOption) (*agentpb.IntrospectHandleResponse, error) {
	return &agentpb.IntrospectHandleResponse{}, nil
}

func (f *fakeBridgeClient) ListHandles(_ context.Context, in *agentpb.ListHandlesRequest, _ ...grpc.CallOption) (*agentpb.ListHandlesResponse, error) {
	f.listReqs = append(f.listReqs, in)
	return &agentpb.ListHandlesResponse{
		Entries: []*agentpb.HandleEntry{
			{Handle: "solver", Manifest: &messagepb.ServiceManifest{Description: "demo"}},
		},
	}, nil
}

func (f *fakeBridgeClient) FindHandles(context.Context, *agentpb.FindHandlesRequest, ...grpc.CallOption) (*agentpb.FindHandlesResponse, error) {
	return &agentpb.FindHandlesResponse{}, nil
}

func (f *fakeBridgeClient) OpenSession(context.Context, *agentpb.OpenSessionRequest, ...grpc.CallOption) (*agentpb.OpenSessionResponse, error) {
	return &agentpb.OpenSessionResponse{}, nil
}

func (f *fakeBridgeClient) SendMessage(context.Context, *agentpb.SendMessageRequest, ...grpc.CallOption) (*agentpb.SendMessageResponse, error) {
	return &agentpb.SendMessageResponse{}, nil
}

func (f *fakeBridgeClient) Subscribe(context.Context, *agentpb.SubscribeRequest, ...grpc.CallOption) (grpc.ServerStreamingClient[agentpb.IncomingMessage], error) {
	if f.stream == nil {
		f.stream = &fakeIncomingStream{}
	}
	return f.stream, nil
}

func (f *fakeBridgeClient) ResolveSession(context.Context, *agentpb.ResolveSessionRequest, ...grpc.CallOption) (*agentpb.ResolveSessionResponse, error) {
	return &agentpb.ResolveSessionResponse{}, nil
}

func (f *fakeBridgeClient) CreateRoom(context.Context, *agentpb.CreateRoomRequest, ...grpc.CallOption) (*agentpb.CreateRoomResponse, error) {
	return &agentpb.CreateRoomResponse{}, nil
}

func (f *fakeBridgeClient) JoinRoom(_ context.Context, in *agentpb.JoinRoomRequest, _ ...grpc.CallOption) (*agentpb.JoinRoomResponse, error) {
	f.joinRoomReqs = append(f.joinRoomReqs, in)
	return &agentpb.JoinRoomResponse{Ok: true}, nil
}

func (f *fakeBridgeClient) LeaveRoom(context.Context, *agentpb.LeaveRoomRequest, ...grpc.CallOption) (*agentpb.LeaveRoomResponse, error) {
	return &agentpb.LeaveRoomResponse{}, nil
}

func (f *fakeBridgeClient) PostRoomMessage(context.Context, *agentpb.PostRoomMessageRequest, ...grpc.CallOption) (*agentpb.PostRoomMessageResponse, error) {
	return &agentpb.PostRoomMessageResponse{}, nil
}

func (f *fakeBridgeClient) ListRooms(context.Context, *agentpb.ListRoomsRequest, ...grpc.CallOption) (*agentpb.ListRoomsResponse, error) {
	return &agentpb.ListRoomsResponse{}, nil
}

func (f *fakeBridgeClient) ListRoomMembers(context.Context, *agentpb.ListRoomMembersRequest, ...grpc.CallOption) (*agentpb.ListRoomMembersResponse, error) {
	return &agentpb.ListRoomMembersResponse{}, nil
}

func (f *fakeBridgeClient) ReplayRoom(context.Context, *agentpb.ReplayRoomRequest, ...grpc.CallOption) (*agentpb.ReplayRoomResponse, error) {
	return &agentpb.ReplayRoomResponse{}, nil
}

func (f *fakeBridgeClient) CloseRoom(context.Context, *agentpb.CloseRoomRequest, ...grpc.CallOption) (*agentpb.CloseRoomResponse, error) {
	return &agentpb.CloseRoomResponse{}, nil
}

func (f *fakeBridgeClient) ListSessions(context.Context, *agentpb.ListSessionsRequest, ...grpc.CallOption) (*agentpb.ListSessionsResponse, error) {
	return &agentpb.ListSessionsResponse{}, nil
}

func TestRunBridgeProcessesInitialRegisterAndJoin(t *testing.T) {
	client := &fakeBridgeClient{}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	input := strings.NewReader("{\"type\":\"list\",\"request_id\":\"req-1\"}\n")
	var output bytes.Buffer

	initial := buildAttachInitialCommands("critic-2", &manifestCmd{Description: "demo"}, []string{"room-1"})
	if err := runBridge(context.Background(), client, logger, input, &output, initial); err != nil {
		t.Fatalf("runBridge: %v", err)
	}

	if len(client.registerReqs) != 1 {
		t.Fatalf("register call count = %d, want 1", len(client.registerReqs))
	}
	if client.registerReqs[0].Handle != "critic-2" {
		t.Fatalf("registered handle = %q, want critic-2", client.registerReqs[0].Handle)
	}
	if got := client.registerReqs[0].Manifest.GetDescription(); got != "demo" {
		t.Fatalf("manifest description = %q, want demo", got)
	}
	if len(client.joinRoomReqs) != 1 || client.joinRoomReqs[0].RoomId != "room-1" {
		t.Fatalf("join_room calls = %+v, want room-1", client.joinRoomReqs)
	}

	lines := decodeOutputLines(t, output.String())
	if len(lines) != 3 {
		t.Fatalf("output line count = %d, want 3; output=%q", len(lines), output.String())
	}
	if lines[0]["type"] != "registered" {
		t.Fatalf("first output type = %v, want registered", lines[0]["type"])
	}
	if lines[1]["type"] != "room_joined" {
		t.Fatalf("second output type = %v, want room_joined", lines[1]["type"])
	}
	if lines[2]["type"] != "handles" {
		t.Fatalf("third output type = %v, want handles", lines[2]["type"])
	}
}

func TestBuildAttachManifestAndCommands(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.json")
	if err := os.WriteFile(path, []byte(`{"description":"demo","capabilities":["dev.review"],"tags":["llm"]}`), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	manifest, err := loadAttachManifest(path)
	if err != nil {
		t.Fatalf("loadAttachManifest: %v", err)
	}
	if manifest.Description != "demo" {
		t.Fatalf("manifest description = %q", manifest.Description)
	}
	cmds := buildAttachInitialCommands("critic-2", manifest, []string{"room-1", "room-2"})
	if len(cmds) != 3 {
		t.Fatalf("initial command count = %d, want 3", len(cmds))
	}
	if cmds[0].Type != "register" || cmds[1].Type != "join_room" {
		t.Fatalf("unexpected initial commands: %+v", cmds)
	}
}

func decodeOutputLines(t *testing.T, data string) []map[string]any {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(data), "\n")
	var out []map[string]any
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var item map[string]any
		if err := json.Unmarshal([]byte(line), &item); err != nil {
			t.Fatalf("decode line %q: %v", line, err)
		}
		out = append(out, item)
	}
	return out
}
