package main

import (
	"bufio"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"os/signal"
	"sync"

	agentpb "github.com/alexanderfrey/tailbus/api/agentpb"
	messagepb "github.com/alexanderfrey/tailbus/api/messagepb"
)

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

// jsonWriter provides mutex-protected JSON line output to stdout.
type jsonWriter struct {
	mu  sync.Mutex
	enc *json.Encoder
}

func newJSONWriter() *jsonWriter {
	return &jsonWriter{enc: json.NewEncoder(os.Stdout)}
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

func runAgent(client agentpb.AgentAPIClient, logger *slog.Logger) error {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	w := newJSONWriter()
	var handle string

	// Scanner goroutine: reads stdin line-by-line into cmdCh.
	cmdCh := make(chan inboundCmd)
	go func() {
		defer close(cmdCh)
		scanner := bufio.NewScanner(os.Stdin)
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
	}()

	// Main dispatch loop.
	for {
		select {
		case cmd, ok := <-cmdCh:
			if !ok {
				// stdin closed
				logger.Info("stdin closed, exiting")
				return nil
			}

			switch cmd.Type {
			case "register":
				if handle != "" {
					w.Write(errorResp{Type: "error", RequestID: cmd.RequestID, Error: "already registered as " + handle, RequestType: "register"})
					continue
				}
				if cmd.Handle == "" {
					w.Write(errorResp{Type: "error", RequestID: cmd.RequestID, Error: "handle is required", RequestType: "register"})
					continue
				}
				// Build register request: prefer manifest, fall back to description
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
				resp, err := client.Register(ctx, req)
				if err != nil {
					w.Write(errorResp{Type: "error", RequestID: cmd.RequestID, Error: err.Error(), RequestType: "register"})
					continue
				}
				if !resp.Ok {
					w.Write(errorResp{Type: "error", RequestID: cmd.RequestID, Error: resp.Error, RequestType: "register"})
					continue
				}
				handle = cmd.Handle
				w.Write(registeredResp{Type: "registered", RequestID: cmd.RequestID, Handle: handle})

				// Launch subscribe goroutine.
				stream, err := client.Subscribe(ctx, &agentpb.SubscribeRequest{Handle: handle})
				if err != nil {
					logger.Error("subscribe failed", "error", err)
					w.Write(errorResp{Type: "error", RequestID: cmd.RequestID, Error: "subscribe failed: " + err.Error(), RequestType: "register"})
					continue
				}
				go func() {
					for {
						msg, err := stream.Recv()
						if err != nil {
							if ctx.Err() != nil {
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

			case "open":
				if handle == "" {
					w.Write(errorResp{Type: "error", RequestID: cmd.RequestID, Error: "must register first", RequestType: "open"})
					continue
				}
				if cmd.To == "" {
					w.Write(errorResp{Type: "error", RequestID: cmd.RequestID, Error: "to is required", RequestType: "open"})
					continue
				}
				ct := cmd.ContentType
				if ct == "" {
					ct = "text/plain"
				}
				resp, err := client.OpenSession(ctx, &agentpb.OpenSessionRequest{
					FromHandle:  handle,
					ToHandle:    cmd.To,
					Payload:     []byte(cmd.Payload),
					ContentType: ct,
					TraceId:     cmd.TraceID,
				})
				if err != nil {
					w.Write(errorResp{Type: "error", RequestID: cmd.RequestID, Error: err.Error(), RequestType: "open"})
					continue
				}
				w.Write(openedResp{Type: "opened", RequestID: cmd.RequestID, Session: resp.SessionId, MessageID: resp.MessageId, TraceID: resp.TraceId})

			case "send":
				if handle == "" {
					w.Write(errorResp{Type: "error", RequestID: cmd.RequestID, Error: "must register first", RequestType: "send"})
					continue
				}
				if cmd.Session == "" {
					w.Write(errorResp{Type: "error", RequestID: cmd.RequestID, Error: "session is required", RequestType: "send"})
					continue
				}
				ct := cmd.ContentType
				if ct == "" {
					ct = "text/plain"
				}
				resp, err := client.SendMessage(ctx, &agentpb.SendMessageRequest{
					SessionId:   cmd.Session,
					FromHandle:  handle,
					Payload:     []byte(cmd.Payload),
					ContentType: ct,
				})
				if err != nil {
					w.Write(errorResp{Type: "error", RequestID: cmd.RequestID, Error: err.Error(), RequestType: "send"})
					continue
				}
				w.Write(sentResp{Type: "sent", RequestID: cmd.RequestID, MessageID: resp.MessageId})

			case "resolve":
				if handle == "" {
					w.Write(errorResp{Type: "error", RequestID: cmd.RequestID, Error: "must register first", RequestType: "resolve"})
					continue
				}
				if cmd.Session == "" {
					w.Write(errorResp{Type: "error", RequestID: cmd.RequestID, Error: "session is required", RequestType: "resolve"})
					continue
				}
				ct := cmd.ContentType
				if ct == "" {
					ct = "text/plain"
				}
				resp, err := client.ResolveSession(ctx, &agentpb.ResolveSessionRequest{
					SessionId:   cmd.Session,
					FromHandle:  handle,
					Payload:     []byte(cmd.Payload),
					ContentType: ct,
				})
				if err != nil {
					w.Write(errorResp{Type: "error", RequestID: cmd.RequestID, Error: err.Error(), RequestType: "resolve"})
					continue
				}
				w.Write(resolvedResp{Type: "resolved", RequestID: cmd.RequestID, MessageID: resp.MessageId})

			case "sessions":
				if handle == "" {
					w.Write(errorResp{Type: "error", RequestID: cmd.RequestID, Error: "must register first", RequestType: "sessions"})
					continue
				}
				resp, err := client.ListSessions(ctx, &agentpb.ListSessionsRequest{Handle: handle})
				if err != nil {
					w.Write(errorResp{Type: "error", RequestID: cmd.RequestID, Error: err.Error(), RequestType: "sessions"})
					continue
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

			case "create_room":
				if handle == "" {
					w.Write(errorResp{Type: "error", RequestID: cmd.RequestID, Error: "must register first", RequestType: "create_room"})
					continue
				}
				resp, err := client.CreateRoom(ctx, &agentpb.CreateRoomRequest{
					CreatorHandle:  handle,
					Title:          cmd.Title,
					InitialMembers: cmd.Members,
				})
				if err != nil {
					w.Write(errorResp{Type: "error", RequestID: cmd.RequestID, Error: err.Error(), RequestType: "create_room"})
					continue
				}
				w.Write(roomCreatedResp{Type: "room_created", RequestID: cmd.RequestID, RoomID: resp.RoomId})

			case "join_room":
				if handle == "" {
					w.Write(errorResp{Type: "error", RequestID: cmd.RequestID, Error: "must register first", RequestType: "join_room"})
					continue
				}
				resp, err := client.JoinRoom(ctx, &agentpb.JoinRoomRequest{RoomId: cmd.RoomID, Handle: handle})
				if err != nil {
					w.Write(errorResp{Type: "error", RequestID: cmd.RequestID, Error: err.Error(), RequestType: "join_room"})
					continue
				}
				w.Write(roomOpResp{Type: "room_joined", RequestID: cmd.RequestID, Ok: resp.Ok})

			case "leave_room":
				if handle == "" {
					w.Write(errorResp{Type: "error", RequestID: cmd.RequestID, Error: "must register first", RequestType: "leave_room"})
					continue
				}
				resp, err := client.LeaveRoom(ctx, &agentpb.LeaveRoomRequest{RoomId: cmd.RoomID, Handle: handle})
				if err != nil {
					w.Write(errorResp{Type: "error", RequestID: cmd.RequestID, Error: err.Error(), RequestType: "leave_room"})
					continue
				}
				w.Write(roomOpResp{Type: "room_left", RequestID: cmd.RequestID, Ok: resp.Ok})

			case "post_room":
				if handle == "" {
					w.Write(errorResp{Type: "error", RequestID: cmd.RequestID, Error: "must register first", RequestType: "post_room"})
					continue
				}
				ct := cmd.ContentType
				if ct == "" {
					ct = "text/plain"
				}
				resp, err := client.PostRoomMessage(ctx, &agentpb.PostRoomMessageRequest{
					RoomId:      cmd.RoomID,
					FromHandle:  handle,
					Payload:     []byte(cmd.Payload),
					ContentType: ct,
					TraceId:     cmd.TraceID,
				})
				if err != nil {
					w.Write(errorResp{Type: "error", RequestID: cmd.RequestID, Error: err.Error(), RequestType: "post_room"})
					continue
				}
				w.Write(roomPostedResp{Type: "room_posted", RequestID: cmd.RequestID, EventID: resp.EventId, RoomSeq: resp.RoomSeq})

			case "list_rooms":
				if handle == "" {
					w.Write(errorResp{Type: "error", RequestID: cmd.RequestID, Error: "must register first", RequestType: "list_rooms"})
					continue
				}
				resp, err := client.ListRooms(ctx, &agentpb.ListRoomsRequest{Handle: handle})
				if err != nil {
					w.Write(errorResp{Type: "error", RequestID: cmd.RequestID, Error: err.Error(), RequestType: "list_rooms"})
					continue
				}
				rooms := make([]roomInfoResp, 0, len(resp.Rooms))
				for _, room := range resp.Rooms {
					rooms = append(rooms, protoRoomInfoToResp(room))
				}
				w.Write(roomsResp{Type: "rooms", RequestID: cmd.RequestID, Rooms: rooms})

			case "room_members":
				if handle == "" {
					w.Write(errorResp{Type: "error", RequestID: cmd.RequestID, Error: "must register first", RequestType: "room_members"})
					continue
				}
				resp, err := client.ListRoomMembers(ctx, &agentpb.ListRoomMembersRequest{RoomId: cmd.RoomID, Handle: handle})
				if err != nil {
					w.Write(errorResp{Type: "error", RequestID: cmd.RequestID, Error: err.Error(), RequestType: "room_members"})
					continue
				}
				w.Write(roomMembersResp{Type: "room_members", RequestID: cmd.RequestID, Members: resp.Members})

			case "replay_room":
				if handle == "" {
					w.Write(errorResp{Type: "error", RequestID: cmd.RequestID, Error: "must register first", RequestType: "replay_room"})
					continue
				}
				resp, err := client.ReplayRoom(ctx, &agentpb.ReplayRoomRequest{RoomId: cmd.RoomID, Handle: handle, SinceSeq: cmd.SinceSeq})
				if err != nil {
					w.Write(errorResp{Type: "error", RequestID: cmd.RequestID, Error: err.Error(), RequestType: "replay_room"})
					continue
				}
				events := make([]roomEventResp, 0, len(resp.Events))
				for _, event := range resp.Events {
					events = append(events, protoRoomEventToResp(event))
				}
				w.Write(roomReplayResp{Type: "room_replay", RequestID: cmd.RequestID, Events: events})

			case "close_room":
				if handle == "" {
					w.Write(errorResp{Type: "error", RequestID: cmd.RequestID, Error: "must register first", RequestType: "close_room"})
					continue
				}
				resp, err := client.CloseRoom(ctx, &agentpb.CloseRoomRequest{RoomId: cmd.RoomID, Handle: handle})
				if err != nil {
					w.Write(errorResp{Type: "error", RequestID: cmd.RequestID, Error: err.Error(), RequestType: "close_room"})
					continue
				}
				w.Write(roomOpResp{Type: "room_closed", RequestID: cmd.RequestID, Ok: resp.Ok})

			case "introspect":
				if cmd.Handle == "" {
					w.Write(errorResp{Type: "error", RequestID: cmd.RequestID, Error: "handle is required", RequestType: "introspect"})
					continue
				}
				resp, err := client.IntrospectHandle(ctx, &agentpb.IntrospectHandleRequest{Handle: cmd.Handle})
				if err != nil {
					w.Write(errorResp{Type: "error", RequestID: cmd.RequestID, Error: err.Error(), RequestType: "introspect"})
					continue
				}
				w.Write(introspectedResp{
					Type:      "introspected",
					RequestID: cmd.RequestID,
					Handle:    resp.Handle,
					Found:     resp.Found,
					Manifest:  protoManifestToResp(resp.Manifest),
				})

			case "list":
				resp, err := client.ListHandles(ctx, &agentpb.ListHandlesRequest{Tags: cmd.Tags})
				if err != nil {
					w.Write(errorResp{Type: "error", RequestID: cmd.RequestID, Error: err.Error(), RequestType: "list"})
					continue
				}
				entries := make([]listEntry, 0, len(resp.Entries))
				for _, e := range resp.Entries {
					entries = append(entries, listEntry{
						Handle:   e.Handle,
						Manifest: protoManifestToResp(e.Manifest),
					})
				}
				w.Write(listResp{Type: "handles", RequestID: cmd.RequestID, Entries: entries})

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
					w.Write(errorResp{Type: "error", RequestID: cmd.RequestID, Error: err.Error(), RequestType: "find"})
					continue
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

			// Keep backward compat: "describe" maps to "introspect"
			case "describe":
				if cmd.Handle == "" {
					w.Write(errorResp{Type: "error", RequestID: cmd.RequestID, Error: "handle is required", RequestType: "describe"})
					continue
				}
				resp, err := client.IntrospectHandle(ctx, &agentpb.IntrospectHandleRequest{Handle: cmd.Handle})
				if err != nil {
					w.Write(errorResp{Type: "error", RequestID: cmd.RequestID, Error: err.Error(), RequestType: "describe"})
					continue
				}
				w.Write(introspectedResp{
					Type:      "introspected",
					RequestID: cmd.RequestID,
					Handle:    resp.Handle,
					Found:     resp.Found,
					Manifest:  protoManifestToResp(resp.Manifest),
				})

			default:
				w.Write(errorResp{Type: "error", RequestID: cmd.RequestID, Error: "unknown command type: " + cmd.Type, RequestType: cmd.Type})
			}

		case <-ctx.Done():
			logger.Info("shutting down")
			return nil
		}
	}
}
