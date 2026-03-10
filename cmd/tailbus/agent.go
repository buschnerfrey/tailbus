package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"time"

	agentpb "github.com/alexanderfrey/tailbus/api/agentpb"
	messagepb "github.com/alexanderfrey/tailbus/api/messagepb"
	"google.golang.org/grpc"
)

// roomOpTimeout is the per-operation timeout for room commands in the bridge,
// slightly longer than the daemon's internal room command timeout to give it
// a chance to return its own error first.
const roomOpTimeout = 15 * time.Second

// Inbound command types (stdin)

type commandCmd struct {
	Name             string `json:"name"`
	Description      string `json:"description,omitempty"`
	ParametersSchema string `json:"parameters_schema,omitempty"`
}

type manifestCmd struct {
	Description  string       `json:"description,omitempty"`
	Commands     []commandCmd `json:"commands,omitempty"`
	Tags         []string     `json:"tags,omitempty"`
	Version      string       `json:"version,omitempty"`
	Capabilities []string     `json:"capabilities,omitempty"`
	Domains      []string     `json:"domains,omitempty"`
	InputTypes   []string     `json:"input_types,omitempty"`
	OutputTypes  []string     `json:"output_types,omitempty"`
}

type inboundCmd struct {
	Type         string       `json:"type"`
	RequestID    string       `json:"request_id,omitempty"`
	Handle       string       `json:"handle,omitempty"`
	Description  string       `json:"description,omitempty"` // deprecated, use manifest
	Manifest     *manifestCmd `json:"manifest,omitempty"`
	Capabilities []string     `json:"capabilities,omitempty"`
	Domains      []string     `json:"domains,omitempty"`
	Tags         []string     `json:"tags,omitempty"` // for list command
	CommandName  string       `json:"command_name,omitempty"`
	Version      string       `json:"version,omitempty"`
	Limit        uint32       `json:"limit,omitempty"`
	To           string       `json:"to,omitempty"`
	Session      string       `json:"session,omitempty"`
	RoomID       string       `json:"room_id,omitempty"`
	Title        string       `json:"title,omitempty"`
	Members      []string     `json:"members,omitempty"`
	SinceSeq     uint64       `json:"since_seq,omitempty"`
	Payload      string       `json:"payload,omitempty"`
	ContentType  string       `json:"content_type,omitempty"`
	TraceID      string       `json:"trace_id,omitempty"`
}

// Outbound response types (stdout)

type registeredResp struct {
	Type      string `json:"type"`
	RequestID string `json:"request_id,omitempty"`
	Handle    string `json:"handle"`
}

type openedResp struct {
	Type      string `json:"type"`
	RequestID string `json:"request_id,omitempty"`
	Session   string `json:"session"`
	MessageID string `json:"message_id"`
	TraceID   string `json:"trace_id"`
}

type sentResp struct {
	Type      string `json:"type"`
	RequestID string `json:"request_id,omitempty"`
	MessageID string `json:"message_id"`
}

type resolvedResp struct {
	Type      string `json:"type"`
	RequestID string `json:"request_id,omitempty"`
	MessageID string `json:"message_id"`
}

type introspectManifestResp struct {
	Description  string       `json:"description,omitempty"`
	Commands     []commandCmd `json:"commands,omitempty"`
	Tags         []string     `json:"tags,omitempty"`
	Version      string       `json:"version,omitempty"`
	Capabilities []string     `json:"capabilities,omitempty"`
	Domains      []string     `json:"domains,omitempty"`
	InputTypes   []string     `json:"input_types,omitempty"`
	OutputTypes  []string     `json:"output_types,omitempty"`
}

type introspectedResp struct {
	Type      string                  `json:"type"`
	RequestID string                  `json:"request_id,omitempty"`
	Handle    string                  `json:"handle"`
	Found     bool                    `json:"found"`
	Manifest  *introspectManifestResp `json:"manifest,omitempty"`
}

type listEntry struct {
	Handle   string                  `json:"handle"`
	Manifest *introspectManifestResp `json:"manifest,omitempty"`
}

type listResp struct {
	Type      string      `json:"type"`
	RequestID string      `json:"request_id,omitempty"`
	Entries   []listEntry `json:"entries"`
}

type matchEntry struct {
	Handle       string                  `json:"handle"`
	Manifest     *introspectManifestResp `json:"manifest,omitempty"`
	Score        int32                   `json:"score"`
	MatchReasons []string                `json:"match_reasons,omitempty"`
}

type matchesResp struct {
	Type      string       `json:"type"`
	RequestID string       `json:"request_id,omitempty"`
	Matches   []matchEntry `json:"matches"`
}

type messageResp struct {
	Type        string `json:"type"`
	Session     string `json:"session"`
	From        string `json:"from"`
	To          string `json:"to"`
	Payload     string `json:"payload"`
	ContentType string `json:"content_type"`
	MessageType string `json:"message_type"`
	TraceID     string `json:"trace_id"`
	MessageID   string `json:"message_id"`
	SentAt      int64  `json:"sent_at"`
}

type roomEventResp struct {
	Type        string   `json:"type"`
	RoomID      string   `json:"room_id"`
	RoomSeq     uint64   `json:"room_seq"`
	Sender      string   `json:"sender"`
	Subject     string   `json:"subject,omitempty"`
	Payload     string   `json:"payload"`
	ContentType string   `json:"content_type"`
	EventType   string   `json:"event_type"`
	TraceID     string   `json:"trace_id"`
	EventID     string   `json:"event_id"`
	SentAt      int64    `json:"sent_at"`
	Members     []string `json:"members,omitempty"`
}

type roomCreatedResp struct {
	Type      string `json:"type"`
	RequestID string `json:"request_id,omitempty"`
	RoomID    string `json:"room_id"`
}

type roomPostedResp struct {
	Type      string `json:"type"`
	RequestID string `json:"request_id,omitempty"`
	EventID   string `json:"event_id"`
	RoomSeq   uint64 `json:"room_seq"`
}

type roomOpResp struct {
	Type      string `json:"type"`
	RequestID string `json:"request_id,omitempty"`
	Ok        bool   `json:"ok"`
}

type roomInfoResp struct {
	RoomID     string   `json:"room_id"`
	Title      string   `json:"title"`
	CreatedBy  string   `json:"created_by"`
	HomeNodeID string   `json:"home_node_id"`
	Members    []string `json:"members"`
	Status     string   `json:"status"`
	NextSeq    uint64   `json:"next_seq"`
	CreatedAt  int64    `json:"created_at"`
	UpdatedAt  int64    `json:"updated_at"`
}

type roomsResp struct {
	Type      string         `json:"type"`
	RequestID string         `json:"request_id,omitempty"`
	Rooms     []roomInfoResp `json:"rooms"`
}

type roomMembersResp struct {
	Type      string   `json:"type"`
	RequestID string   `json:"request_id,omitempty"`
	Members   []string `json:"members"`
}

type roomReplayResp struct {
	Type      string          `json:"type"`
	RequestID string          `json:"request_id,omitempty"`
	Events    []roomEventResp `json:"events"`
}

type sessionItem struct {
	Session string `json:"session"`
	From    string `json:"from"`
	To      string `json:"to"`
	State   string `json:"state"`
}

type sessionsResp struct {
	Type      string        `json:"type"`
	RequestID string        `json:"request_id,omitempty"`
	Sessions  []sessionItem `json:"sessions"`
}

type errorResp struct {
	Type        string `json:"type"`
	RequestID   string `json:"request_id,omitempty"`
	Error       string `json:"error"`
	RequestType string `json:"request_type"`
}

type bridgeClient interface {
	Register(ctx context.Context, in *agentpb.RegisterRequest, opts ...grpc.CallOption) (*agentpb.RegisterResponse, error)
	IntrospectHandle(ctx context.Context, in *agentpb.IntrospectHandleRequest, opts ...grpc.CallOption) (*agentpb.IntrospectHandleResponse, error)
	ListHandles(ctx context.Context, in *agentpb.ListHandlesRequest, opts ...grpc.CallOption) (*agentpb.ListHandlesResponse, error)
	FindHandles(ctx context.Context, in *agentpb.FindHandlesRequest, opts ...grpc.CallOption) (*agentpb.FindHandlesResponse, error)
	OpenSession(ctx context.Context, in *agentpb.OpenSessionRequest, opts ...grpc.CallOption) (*agentpb.OpenSessionResponse, error)
	SendMessage(ctx context.Context, in *agentpb.SendMessageRequest, opts ...grpc.CallOption) (*agentpb.SendMessageResponse, error)
	Subscribe(ctx context.Context, in *agentpb.SubscribeRequest, opts ...grpc.CallOption) (grpc.ServerStreamingClient[agentpb.IncomingMessage], error)
	ResolveSession(ctx context.Context, in *agentpb.ResolveSessionRequest, opts ...grpc.CallOption) (*agentpb.ResolveSessionResponse, error)
	CreateRoom(ctx context.Context, in *agentpb.CreateRoomRequest, opts ...grpc.CallOption) (*agentpb.CreateRoomResponse, error)
	JoinRoom(ctx context.Context, in *agentpb.JoinRoomRequest, opts ...grpc.CallOption) (*agentpb.JoinRoomResponse, error)
	LeaveRoom(ctx context.Context, in *agentpb.LeaveRoomRequest, opts ...grpc.CallOption) (*agentpb.LeaveRoomResponse, error)
	PostRoomMessage(ctx context.Context, in *agentpb.PostRoomMessageRequest, opts ...grpc.CallOption) (*agentpb.PostRoomMessageResponse, error)
	ListRooms(ctx context.Context, in *agentpb.ListRoomsRequest, opts ...grpc.CallOption) (*agentpb.ListRoomsResponse, error)
	ListRoomMembers(ctx context.Context, in *agentpb.ListRoomMembersRequest, opts ...grpc.CallOption) (*agentpb.ListRoomMembersResponse, error)
	ReplayRoom(ctx context.Context, in *agentpb.ReplayRoomRequest, opts ...grpc.CallOption) (*agentpb.ReplayRoomResponse, error)
	CloseRoom(ctx context.Context, in *agentpb.CloseRoomRequest, opts ...grpc.CallOption) (*agentpb.CloseRoomResponse, error)
	ListSessions(ctx context.Context, in *agentpb.ListSessionsRequest, opts ...grpc.CallOption) (*agentpb.ListSessionsResponse, error)
}

// jsonWriter provides mutex-protected JSON line output to stdout.
type jsonWriter struct {
	mu  sync.Mutex
	enc *json.Encoder
}

func newJSONWriter(out io.Writer) *jsonWriter {
	return &jsonWriter{enc: json.NewEncoder(out)}
}

func (w *jsonWriter) Write(v any) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.enc.Encode(v) //nolint:errcheck
}

func envelopeTypeString(t messagepb.EnvelopeType) string {
	switch t {
	case messagepb.EnvelopeType_ENVELOPE_TYPE_SESSION_OPEN:
		return "session_open"
	case messagepb.EnvelopeType_ENVELOPE_TYPE_MESSAGE:
		return "message"
	case messagepb.EnvelopeType_ENVELOPE_TYPE_SESSION_RESOLVE:
		return "session_resolve"
	case messagepb.EnvelopeType_ENVELOPE_TYPE_ACK:
		return "ack"
	default:
		return "unknown"
	}
}

func protoManifestToResp(m *messagepb.ServiceManifest) *introspectManifestResp {
	if m == nil {
		return nil
	}
	resp := &introspectManifestResp{
		Description:  m.Description,
		Tags:         m.Tags,
		Version:      m.Version,
		Capabilities: m.Capabilities,
		Domains:      m.Domains,
		InputTypes:   m.InputTypes,
		OutputTypes:  m.OutputTypes,
	}
	for _, c := range m.Commands {
		resp.Commands = append(resp.Commands, commandCmd{
			Name:             c.Name,
			Description:      c.Description,
			ParametersSchema: c.ParametersSchema,
		})
	}
	return resp
}

func roomEventTypeString(t messagepb.RoomEventType) string {
	switch t {
	case messagepb.RoomEventType_ROOM_EVENT_TYPE_MEMBER_JOINED:
		return "member_joined"
	case messagepb.RoomEventType_ROOM_EVENT_TYPE_MEMBER_LEFT:
		return "member_left"
	case messagepb.RoomEventType_ROOM_EVENT_TYPE_MESSAGE_POSTED:
		return "message_posted"
	case messagepb.RoomEventType_ROOM_EVENT_TYPE_ROOM_CLOSED:
		return "room_closed"
	default:
		return "unknown"
	}
}

func protoRoomEventToResp(event *messagepb.RoomEvent) roomEventResp {
	return roomEventResp{
		Type:        "room_event",
		RoomID:      event.RoomId,
		RoomSeq:     event.RoomSeq,
		Sender:      event.SenderHandle,
		Subject:     event.SubjectHandle,
		Payload:     string(event.Payload),
		ContentType: event.ContentType,
		EventType:   roomEventTypeString(event.Type),
		TraceID:     event.TraceId,
		EventID:     event.EventId,
		SentAt:      event.SentAtUnix,
		Members:     event.Members,
	}
}

func protoRoomInfoToResp(room *messagepb.RoomInfo) roomInfoResp {
	return roomInfoResp{
		RoomID:     room.RoomId,
		Title:      room.Title,
		CreatedBy:  room.CreatedBy,
		HomeNodeID: room.HomeNodeId,
		Members:    room.Members,
		Status:     room.Status,
		NextSeq:    room.NextSeq,
		CreatedAt:  room.CreatedAtUnix,
		UpdatedAt:  room.UpdatedAtUnix,
	}
}

func buildRegisterRequest(cmd inboundCmd) *agentpb.RegisterRequest {
	req := &agentpb.RegisterRequest{Handle: cmd.Handle}
	if cmd.Manifest != nil {
		m := &messagepb.ServiceManifest{
			Description:  cmd.Manifest.Description,
			Tags:         cmd.Manifest.Tags,
			Version:      cmd.Manifest.Version,
			Capabilities: cmd.Manifest.Capabilities,
			Domains:      cmd.Manifest.Domains,
			InputTypes:   cmd.Manifest.InputTypes,
			OutputTypes:  cmd.Manifest.OutputTypes,
		}
		for _, c := range cmd.Manifest.Commands {
			m.Commands = append(m.Commands, &messagepb.CommandSpec{
				Name:             c.Name,
				Description:      c.Description,
				ParametersSchema: c.ParametersSchema,
			})
		}
		req.Manifest = m
	} else if cmd.Description != "" {
		req.Description = cmd.Description
	}
	return req
}

func startBridgeSubscription(ctx context.Context, client bridgeClient, logger *slog.Logger, w *jsonWriter, handle string) error {
	stream, err := client.Subscribe(ctx, &agentpb.SubscribeRequest{Handle: handle})
	if err != nil {
		return err
	}
	go func() {
		for {
			msg, err := stream.Recv()
			if err != nil {
				if ctx.Err() != nil || err == io.EOF {
					return
				}
				logger.Error("subscribe stream error", "error", err)
				return
			}
			if env := msg.Envelope; env != nil {
				w.Write(messageResp{
					Type:        "message",
					Session:     env.SessionId,
					From:        env.FromHandle,
					To:          env.ToHandle,
					Payload:     string(env.Payload),
					ContentType: env.ContentType,
					MessageType: envelopeTypeString(env.Type),
					TraceID:     env.TraceId,
					MessageID:   env.MessageId,
					SentAt:      env.SentAtUnix,
				})
			} else if event := msg.RoomEvent; event != nil {
				w.Write(protoRoomEventToResp(event))
			}
		}
	}()
	return nil
}

func executeBridgeCommand(ctx context.Context, client bridgeClient, logger *slog.Logger, w *jsonWriter, handle *string, cmd inboundCmd, fatal bool) error {
	fail := func(requestType, message string, err error) error {
		w.Write(errorResp{Type: "error", RequestID: cmd.RequestID, Error: message, RequestType: requestType})
		if fatal {
			if err != nil {
				return err
			}
			return fmt.Errorf("%s", message)
		}
		return nil
	}

	switch cmd.Type {
	case "register":
		if *handle != "" {
			return fail("register", "already registered as "+*handle, nil)
		}
		if cmd.Handle == "" {
			return fail("register", "handle is required", nil)
		}
		resp, err := client.Register(ctx, buildRegisterRequest(cmd))
		if err != nil {
			return fail("register", err.Error(), err)
		}
		if !resp.Ok {
			return fail("register", resp.Error, nil)
		}
		if err := startBridgeSubscription(ctx, client, logger, w, cmd.Handle); err != nil {
			logger.Error("subscribe failed", "error", err)
			return fail("register", "subscribe failed: "+err.Error(), err)
		}
		*handle = cmd.Handle
		w.Write(registeredResp{Type: "registered", RequestID: cmd.RequestID, Handle: *handle})
		return nil

	case "open":
		if *handle == "" {
			return fail("open", "must register first", nil)
		}
		if cmd.To == "" {
			return fail("open", "to is required", nil)
		}
		ct := cmd.ContentType
		if ct == "" {
			ct = "text/plain"
		}
		resp, err := client.OpenSession(ctx, &agentpb.OpenSessionRequest{
			FromHandle:  *handle,
			ToHandle:    cmd.To,
			Payload:     []byte(cmd.Payload),
			ContentType: ct,
			TraceId:     cmd.TraceID,
		})
		if err != nil {
			return fail("open", err.Error(), err)
		}
		w.Write(openedResp{Type: "opened", RequestID: cmd.RequestID, Session: resp.SessionId, MessageID: resp.MessageId, TraceID: resp.TraceId})
		return nil

	case "send":
		if *handle == "" {
			return fail("send", "must register first", nil)
		}
		if cmd.Session == "" {
			return fail("send", "session is required", nil)
		}
		ct := cmd.ContentType
		if ct == "" {
			ct = "text/plain"
		}
		resp, err := client.SendMessage(ctx, &agentpb.SendMessageRequest{
			SessionId:   cmd.Session,
			FromHandle:  *handle,
			Payload:     []byte(cmd.Payload),
			ContentType: ct,
		})
		if err != nil {
			return fail("send", err.Error(), err)
		}
		w.Write(sentResp{Type: "sent", RequestID: cmd.RequestID, MessageID: resp.MessageId})
		return nil

	case "resolve":
		if *handle == "" {
			return fail("resolve", "must register first", nil)
		}
		if cmd.Session == "" {
			return fail("resolve", "session is required", nil)
		}
		ct := cmd.ContentType
		if ct == "" {
			ct = "text/plain"
		}
		resp, err := client.ResolveSession(ctx, &agentpb.ResolveSessionRequest{
			SessionId:   cmd.Session,
			FromHandle:  *handle,
			Payload:     []byte(cmd.Payload),
			ContentType: ct,
		})
		if err != nil {
			return fail("resolve", err.Error(), err)
		}
		w.Write(resolvedResp{Type: "resolved", RequestID: cmd.RequestID, MessageID: resp.MessageId})
		return nil

	case "sessions":
		if *handle == "" {
			return fail("sessions", "must register first", nil)
		}
		resp, err := client.ListSessions(ctx, &agentpb.ListSessionsRequest{Handle: *handle})
		if err != nil {
			return fail("sessions", err.Error(), err)
		}
		items := make([]sessionItem, 0, len(resp.Sessions))
		for _, s := range resp.Sessions {
			items = append(items, sessionItem{
				Session: s.SessionId,
				From:    s.FromHandle,
				To:      s.ToHandle,
				State:   s.State,
			})
		}
		w.Write(sessionsResp{Type: "sessions", RequestID: cmd.RequestID, Sessions: items})
		return nil

	case "create_room":
		if *handle == "" {
			return fail("create_room", "must register first", nil)
		}
		opCtx, opCancel := context.WithTimeout(ctx, roomOpTimeout)
		resp, err := client.CreateRoom(opCtx, &agentpb.CreateRoomRequest{
			CreatorHandle:  *handle,
			Title:          cmd.Title,
			InitialMembers: cmd.Members,
		})
		opCancel()
		if err != nil {
			return fail("create_room", err.Error(), err)
		}
		w.Write(roomCreatedResp{Type: "room_created", RequestID: cmd.RequestID, RoomID: resp.RoomId})
		return nil

	case "join_room":
		if *handle == "" {
			return fail("join_room", "must register first", nil)
		}
		opCtx, opCancel := context.WithTimeout(ctx, roomOpTimeout)
		resp, err := client.JoinRoom(opCtx, &agentpb.JoinRoomRequest{RoomId: cmd.RoomID, Handle: *handle})
		opCancel()
		if err != nil {
			return fail("join_room", err.Error(), err)
		}
		w.Write(roomOpResp{Type: "room_joined", RequestID: cmd.RequestID, Ok: resp.Ok})
		return nil

	case "leave_room":
		if *handle == "" {
			return fail("leave_room", "must register first", nil)
		}
		opCtx, opCancel := context.WithTimeout(ctx, roomOpTimeout)
		resp, err := client.LeaveRoom(opCtx, &agentpb.LeaveRoomRequest{RoomId: cmd.RoomID, Handle: *handle})
		opCancel()
		if err != nil {
			return fail("leave_room", err.Error(), err)
		}
		w.Write(roomOpResp{Type: "room_left", RequestID: cmd.RequestID, Ok: resp.Ok})
		return nil

	case "post_room":
		if *handle == "" {
			return fail("post_room", "must register first", nil)
		}
		ct := cmd.ContentType
		if ct == "" {
			ct = "text/plain"
		}
		opCtx, opCancel := context.WithTimeout(ctx, roomOpTimeout)
		resp, err := client.PostRoomMessage(opCtx, &agentpb.PostRoomMessageRequest{
			RoomId:      cmd.RoomID,
			FromHandle:  *handle,
			Payload:     []byte(cmd.Payload),
			ContentType: ct,
			TraceId:     cmd.TraceID,
		})
		opCancel()
		if err != nil {
			return fail("post_room", err.Error(), err)
		}
		w.Write(roomPostedResp{Type: "room_posted", RequestID: cmd.RequestID, EventID: resp.EventId, RoomSeq: resp.RoomSeq})
		return nil

	case "list_rooms":
		if *handle == "" {
			return fail("list_rooms", "must register first", nil)
		}
		opCtx, opCancel := context.WithTimeout(ctx, roomOpTimeout)
		resp, err := client.ListRooms(opCtx, &agentpb.ListRoomsRequest{Handle: *handle})
		opCancel()
		if err != nil {
			return fail("list_rooms", err.Error(), err)
		}
		rooms := make([]roomInfoResp, 0, len(resp.Rooms))
		for _, room := range resp.Rooms {
			rooms = append(rooms, protoRoomInfoToResp(room))
		}
		w.Write(roomsResp{Type: "rooms", RequestID: cmd.RequestID, Rooms: rooms})
		return nil

	case "room_members":
		if *handle == "" {
			return fail("room_members", "must register first", nil)
		}
		opCtx, opCancel := context.WithTimeout(ctx, roomOpTimeout)
		resp, err := client.ListRoomMembers(opCtx, &agentpb.ListRoomMembersRequest{RoomId: cmd.RoomID, Handle: *handle})
		opCancel()
		if err != nil {
			return fail("room_members", err.Error(), err)
		}
		w.Write(roomMembersResp{Type: "room_members", RequestID: cmd.RequestID, Members: resp.Members})
		return nil

	case "replay_room":
		if *handle == "" {
			return fail("replay_room", "must register first", nil)
		}
		opCtx, opCancel := context.WithTimeout(ctx, roomOpTimeout)
		resp, err := client.ReplayRoom(opCtx, &agentpb.ReplayRoomRequest{RoomId: cmd.RoomID, Handle: *handle, SinceSeq: cmd.SinceSeq})
		opCancel()
		if err != nil {
			return fail("replay_room", err.Error(), err)
		}
		events := make([]roomEventResp, 0, len(resp.Events))
		for _, event := range resp.Events {
			events = append(events, protoRoomEventToResp(event))
		}
		w.Write(roomReplayResp{Type: "room_replay", RequestID: cmd.RequestID, Events: events})
		return nil

	case "close_room":
		if *handle == "" {
			return fail("close_room", "must register first", nil)
		}
		opCtx, opCancel := context.WithTimeout(ctx, roomOpTimeout)
		resp, err := client.CloseRoom(opCtx, &agentpb.CloseRoomRequest{RoomId: cmd.RoomID, Handle: *handle})
		opCancel()
		if err != nil {
			return fail("close_room", err.Error(), err)
		}
		w.Write(roomOpResp{Type: "room_closed", RequestID: cmd.RequestID, Ok: resp.Ok})
		return nil

	case "introspect", "describe":
		if cmd.Handle == "" {
			return fail(cmd.Type, "handle is required", nil)
		}
		resp, err := client.IntrospectHandle(ctx, &agentpb.IntrospectHandleRequest{Handle: cmd.Handle})
		if err != nil {
			return fail(cmd.Type, err.Error(), err)
		}
		w.Write(introspectedResp{
			Type:      "introspected",
			RequestID: cmd.RequestID,
			Handle:    resp.Handle,
			Found:     resp.Found,
			Manifest:  protoManifestToResp(resp.Manifest),
		})
		return nil

	case "list":
		resp, err := client.ListHandles(ctx, &agentpb.ListHandlesRequest{Tags: cmd.Tags})
		if err != nil {
			return fail("list", err.Error(), err)
		}
		entries := make([]listEntry, 0, len(resp.Entries))
		for _, e := range resp.Entries {
			entries = append(entries, listEntry{
				Handle:   e.Handle,
				Manifest: protoManifestToResp(e.Manifest),
			})
		}
		w.Write(listResp{Type: "handles", RequestID: cmd.RequestID, Entries: entries})
		return nil

	case "find":
		resp, err := client.FindHandles(ctx, &agentpb.FindHandlesRequest{
			Capabilities: cmd.Capabilities,
			Domains:      cmd.Domains,
			Tags:         cmd.Tags,
			CommandName:  cmd.CommandName,
			Version:      cmd.Version,
			Limit:        cmd.Limit,
		})
		if err != nil {
			return fail("find", err.Error(), err)
		}
		matches := make([]matchEntry, 0, len(resp.Matches))
		for _, match := range resp.Matches {
			matches = append(matches, matchEntry{
				Handle:       match.Handle,
				Manifest:     protoManifestToResp(match.Manifest),
				Score:        match.Score,
				MatchReasons: match.MatchReasons,
			})
		}
		w.Write(matchesResp{Type: "handle_matches", RequestID: cmd.RequestID, Matches: matches})
		return nil

	default:
		return fail(cmd.Type, "unknown command type: "+cmd.Type, nil)
	}
}

func runBridge(ctx context.Context, client bridgeClient, logger *slog.Logger, input io.Reader, output io.Writer, initial []inboundCmd) error {
	w := newJSONWriter(output)
	var handle string

	for _, cmd := range initial {
		if err := executeBridgeCommand(ctx, client, logger, w, &handle, cmd, true); err != nil {
			return err
		}
	}

	cmdCh := make(chan inboundCmd)
	scanErrCh := make(chan error, 1)
	go func() {
		defer close(cmdCh)
		scanner := bufio.NewScanner(input)
		scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
		for scanner.Scan() {
			line := scanner.Bytes()
			if len(line) == 0 {
				continue
			}
			var cmd inboundCmd
			if err := json.Unmarshal(line, &cmd); err != nil {
				w.Write(errorResp{Type: "error", Error: "invalid JSON: " + err.Error(), RequestType: "unknown"})
				continue
			}
			select {
			case cmdCh <- cmd:
			case <-ctx.Done():
				return
			}
		}
		scanErrCh <- scanner.Err()
	}()

	for {
		select {
		case cmd, ok := <-cmdCh:
			if !ok {
				if err := <-scanErrCh; err != nil {
					return err
				}
				logger.Info("stdin closed, exiting")
				return nil
			}
			if err := executeBridgeCommand(ctx, client, logger, w, &handle, cmd, false); err != nil {
				return err
			}
		case <-ctx.Done():
			logger.Info("shutting down")
			return nil
		}
	}
}

func runAgent(client bridgeClient, logger *slog.Logger) error {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	return runBridge(ctx, client, logger, os.Stdin, os.Stdout, nil)
}
