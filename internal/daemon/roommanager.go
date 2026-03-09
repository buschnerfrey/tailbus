package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"sort"
	"sync"
	"time"

	messagepb "github.com/alexanderfrey/tailbus/api/messagepb"
	transportpb "github.com/alexanderfrey/tailbus/api/transportpb"
	"github.com/alexanderfrey/tailbus/internal/handle"
	"github.com/alexanderfrey/tailbus/internal/transport"
	"github.com/google/uuid"
)

const defaultRoomEventRetention = 10000

// RoomDeliverer delivers room events to local agent subscribers.
type RoomDeliverer interface {
	DeliverRoomEventLocal(event *messagepb.RoomEvent) bool
}

type RoomManager struct {
	nodeID      string
	resolver    *handle.Resolver
	transport   transport.Transport
	deliverer   RoomDeliverer
	store       *MessageStore
	logger      *slog.Logger
	retention   int
	activity    *ActivityBus

	mu          sync.Mutex
	pendingResp map[string]chan *messagepb.RoomCommandResult
	coord       *CoordClient
}

func NewRoomManager(nodeID string, resolver *handle.Resolver, tp transport.Transport, deliverer RoomDeliverer, store *MessageStore, activity *ActivityBus, logger *slog.Logger) *RoomManager {
	return &RoomManager{
		nodeID:      nodeID,
		resolver:    resolver,
		transport:   tp,
		deliverer:   deliverer,
		store:       store,
		logger:      logger,
		retention:   defaultRoomEventRetention,
		activity:    activity,
		pendingResp: make(map[string]chan *messagepb.RoomCommandResult),
	}
}

func (rm *RoomManager) SetCoordClient(cc *CoordClient) {
	rm.coord = cc
}

func (rm *RoomManager) CreateRoom(ctx context.Context, creator, title string, initialMembers []string) (*messagepb.RoomInfo, error) {
	members := uniqueHandles(append([]string{creator}, initialMembers...))
	now := time.Now().Unix()
	room := &messagepb.RoomInfo{
		RoomId:        uuid.New().String(),
		Title:         title,
		CreatedBy:     creator,
		HomeNodeId:    rm.nodeID,
		Members:       members,
		Status:        "open",
		NextSeq:       1,
		CreatedAtUnix: now,
		UpdatedAtUnix: now,
	}
	if err := rm.store.StoreRoom(room); err != nil {
		return nil, err
	}
	if rm.coord != nil {
		if err := rm.coord.UpsertRoom(ctx, room); err != nil {
			return nil, err
		}
	}
	if rm.activity != nil {
		rm.activity.EmitRoomCreated(room.RoomId, room.Title, room.CreatedBy, room.Members)
	}
	return room, nil
}

func (rm *RoomManager) JoinRoom(ctx context.Context, roomID, handle string) (*messagepb.RoomInfo, error) {
	result, err := rm.withRoomControl(ctx, roomID, &messagepb.RoomCommand{
		RequestId:       uuid.New().String(),
		Type:            messagepb.RoomCommandType_ROOM_COMMAND_TYPE_JOIN,
		RoomId:          roomID,
		RequesterHandle: handle,
		SubjectHandle:   handle,
		RequesterNodeId: rm.nodeID,
	})
	if err != nil {
		return nil, err
	}
	return result.Room, nil
}

func (rm *RoomManager) LeaveRoom(ctx context.Context, roomID, handle string) (*messagepb.RoomInfo, error) {
	result, err := rm.withRoomControl(ctx, roomID, &messagepb.RoomCommand{
		RequestId:       uuid.New().String(),
		Type:            messagepb.RoomCommandType_ROOM_COMMAND_TYPE_LEAVE,
		RoomId:          roomID,
		RequesterHandle: handle,
		SubjectHandle:   handle,
		RequesterNodeId: rm.nodeID,
	})
	if err != nil {
		return nil, err
	}
	return result.Room, nil
}

func (rm *RoomManager) PostMessage(ctx context.Context, roomID, from string, payload []byte, contentType, traceID string) (*messagepb.RoomEvent, error) {
	result, err := rm.withRoomControl(ctx, roomID, &messagepb.RoomCommand{
		RequestId:       uuid.New().String(),
		Type:            messagepb.RoomCommandType_ROOM_COMMAND_TYPE_POST,
		RoomId:          roomID,
		RequesterHandle: from,
		Payload:         payload,
		ContentType:     contentType,
		TraceId:         traceID,
		RequesterNodeId: rm.nodeID,
	})
	if err != nil {
		return nil, err
	}
	return result.Event, nil
}

func (rm *RoomManager) ListRooms(ctx context.Context, handle string) ([]*messagepb.RoomInfo, error) {
	if rm.coord != nil {
		return rm.coord.ListRoomsForHandle(ctx, handle)
	}
	return nil, nil
}

func (rm *RoomManager) ListMembers(ctx context.Context, roomID, handle string) ([]string, error) {
	result, err := rm.withRoomControl(ctx, roomID, &messagepb.RoomCommand{
		RequestId:       uuid.New().String(),
		Type:            messagepb.RoomCommandType_ROOM_COMMAND_TYPE_LIST_MEMBERS,
		RoomId:          roomID,
		RequesterHandle: handle,
		RequesterNodeId: rm.nodeID,
	})
	if err != nil {
		return nil, err
	}
	return result.Members, nil
}

func (rm *RoomManager) Replay(ctx context.Context, roomID, handle string, sinceSeq uint64) ([]*messagepb.RoomEvent, error) {
	result, err := rm.withRoomControl(ctx, roomID, &messagepb.RoomCommand{
		RequestId:       uuid.New().String(),
		Type:            messagepb.RoomCommandType_ROOM_COMMAND_TYPE_REPLAY,
		RoomId:          roomID,
		RequesterHandle: handle,
		SinceSeq:        sinceSeq,
		RequesterNodeId: rm.nodeID,
	})
	if err != nil {
		return nil, err
	}
	return result.Events, nil
}

func (rm *RoomManager) CloseRoom(ctx context.Context, roomID, handle string) (*messagepb.RoomInfo, error) {
	result, err := rm.withRoomControl(ctx, roomID, &messagepb.RoomCommand{
		RequestId:       uuid.New().String(),
		Type:            messagepb.RoomCommandType_ROOM_COMMAND_TYPE_CLOSE,
		RoomId:          roomID,
		RequesterHandle: handle,
		RequesterNodeId: rm.nodeID,
	})
	if err != nil {
		return nil, err
	}
	return result.Room, nil
}

func (rm *RoomManager) DashboardRooms(handles []string) ([]*messagepb.RoomInfo, error) {
	roomByID := make(map[string]*messagepb.RoomInfo)
	if rm.store != nil {
		rooms, err := rm.store.LoadRooms()
		if err != nil {
			return nil, err
		}
		for _, room := range rooms {
			if room != nil {
				roomByID[room.RoomId] = room
			}
		}
	}
	if rm.resolver != nil {
		for _, handle := range handles {
			for _, info := range rm.resolver.ListRoomsForMember(handle) {
				if _, ok := roomByID[info.RoomID]; ok {
					continue
				}
				roomByID[info.RoomID] = &messagepb.RoomInfo{
					RoomId:        info.RoomID,
					Title:         info.Title,
					CreatedBy:     info.CreatedBy,
					HomeNodeId:    info.HomeNodeID,
					Members:       append([]string(nil), info.Members...),
					Status:        info.Status,
					NextSeq:       info.NextSeq,
					CreatedAtUnix: info.CreatedAt,
					UpdatedAtUnix: info.UpdatedAt,
				}
			}
		}
	}
	rooms := make([]*messagepb.RoomInfo, 0, len(roomByID))
	for _, room := range roomByID {
		rooms = append(rooms, room)
	}
	sort.Slice(rooms, func(i, j int) bool {
		if rooms[i].UpdatedAtUnix == rooms[j].UpdatedAtUnix {
			return rooms[i].RoomId < rooms[j].RoomId
		}
		return rooms[i].UpdatedAtUnix > rooms[j].UpdatedAtUnix
	})
	return rooms, nil
}

func (rm *RoomManager) HandleTransportMessage(ctx context.Context, msg *transportpb.TransportMessage) {
	switch body := msg.Body.(type) {
	case *transportpb.TransportMessage_RoomEvent:
		if body.RoomEvent != nil {
			rm.cacheRoomFromEvent(body.RoomEvent)
			rm.deliverer.DeliverRoomEventLocal(body.RoomEvent)
		}
	case *transportpb.TransportMessage_RoomCommand:
		if body.RoomCommand != nil {
			rm.handleRemoteCommand(ctx, body.RoomCommand)
		}
	case *transportpb.TransportMessage_RoomCommandResult:
		if body.RoomCommandResult != nil {
			rm.handleRemoteCommandResult(body.RoomCommandResult)
		}
	}
}

// roomCommandTimeout is the unified deadline for remote room commands,
// covering connect + send + result wait.
const roomCommandTimeout = 10 * time.Second

func (rm *RoomManager) withRoomControl(ctx context.Context, roomID string, cmd *messagepb.RoomCommand) (*messagepb.RoomCommandResult, error) {
	room, err := rm.resolveRoomInfo(roomID)
	if err != nil {
		return nil, err
	}
	if room.HomeNodeId == rm.nodeID {
		result, err := rm.executeCommand(ctx, room, cmd)
		if err != nil {
			return nil, err
		}
		result.RequestId = cmd.RequestId
		return result, nil
	}

	addr, err := rm.nodeAddr(room.HomeNodeId)
	if err != nil {
		return nil, err
	}

	// Unified deadline covering connect + send + result wait.
	sendCtx, cancel := context.WithTimeout(ctx, roomCommandTimeout)
	defer cancel()

	respCh := make(chan *messagepb.RoomCommandResult, 1)
	rm.mu.Lock()
	rm.pendingResp[cmd.RequestId] = respCh
	rm.mu.Unlock()

	defer func() {
		rm.mu.Lock()
		delete(rm.pendingResp, cmd.RequestId)
		rm.mu.Unlock()
	}()

	if err := rm.transport.Send(sendCtx, addr, &transportpb.TransportMessage{
		Body: &transportpb.TransportMessage_RoomCommand{RoomCommand: cmd},
	}); err != nil {
		return nil, err
	}

	select {
	case <-sendCtx.Done():
		return nil, fmt.Errorf("room command %s timed out: %w", cmd.Type, sendCtx.Err())
	case result := <-respCh:
		if result == nil {
			return nil, fmt.Errorf("room command %s returned no result", cmd.Type)
		}
		if !result.Ok {
			return nil, fmt.Errorf("%s", result.Error)
		}
		return result, nil
	}
}

func (rm *RoomManager) handleRemoteCommand(ctx context.Context, cmd *messagepb.RoomCommand) {
	room, err := rm.resolveRoomInfo(cmd.RoomId)
	if err != nil {
		rm.replyWithError(cmd, err)
		return
	}
	result, err := rm.executeCommand(ctx, room, cmd)
	if err != nil {
		rm.replyWithError(cmd, err)
		return
	}
	result.RequestId = cmd.RequestId
	if cmd.RequesterNodeId == "" || cmd.RequesterNodeId == rm.nodeID {
		rm.handleRemoteCommandResult(result)
		return
	}
	addr, err := rm.nodeAddr(cmd.RequesterNodeId)
	if err != nil {
		rm.logger.Warn("failed to find requester node for room command result", "node_id", cmd.RequesterNodeId, "error", err)
		return
	}
	replyCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := rm.transport.Send(replyCtx, addr, &transportpb.TransportMessage{
		Body: &transportpb.TransportMessage_RoomCommandResult{RoomCommandResult: result},
	}); err != nil {
		rm.logger.Warn("failed to send room command result", "request_id", cmd.RequestId, "error", err)
	}
}

func (rm *RoomManager) handleRemoteCommandResult(result *messagepb.RoomCommandResult) {
	rm.cacheRoom(result.Room)
	rm.mu.Lock()
	ch := rm.pendingResp[result.RequestId]
	rm.mu.Unlock()
	if ch == nil {
		return
	}
	select {
	case ch <- result:
	default:
	}
}

func (rm *RoomManager) executeCommand(ctx context.Context, room *messagepb.RoomInfo, cmd *messagepb.RoomCommand) (*messagepb.RoomCommandResult, error) {
	switch cmd.Type {
	case messagepb.RoomCommandType_ROOM_COMMAND_TYPE_JOIN:
		return rm.joinLocal(ctx, room, cmd.SubjectHandle)
	case messagepb.RoomCommandType_ROOM_COMMAND_TYPE_LEAVE:
		return rm.leaveLocal(ctx, room, cmd.SubjectHandle)
	case messagepb.RoomCommandType_ROOM_COMMAND_TYPE_POST:
		return rm.postLocal(ctx, room, cmd.RequesterHandle, cmd.Payload, cmd.ContentType, cmd.TraceId)
	case messagepb.RoomCommandType_ROOM_COMMAND_TYPE_REPLAY:
		return rm.replayLocal(room, cmd.RequesterHandle, cmd.SinceSeq)
	case messagepb.RoomCommandType_ROOM_COMMAND_TYPE_LIST_MEMBERS:
		if !isRoomMember(room, cmd.RequesterHandle) {
			return nil, fmt.Errorf("handle %q is not a member of room %q", cmd.RequesterHandle, room.RoomId)
		}
		return &messagepb.RoomCommandResult{
			RequestId: cmd.RequestId,
			Ok:        true,
			Members:   append([]string(nil), room.Members...),
			Room:      room,
		}, nil
	case messagepb.RoomCommandType_ROOM_COMMAND_TYPE_CLOSE:
		return rm.closeLocal(ctx, room, cmd.RequesterHandle)
	default:
		return nil, fmt.Errorf("unsupported room command %s", cmd.Type)
	}
}

func (rm *RoomManager) joinLocal(ctx context.Context, room *messagepb.RoomInfo, handle string) (*messagepb.RoomCommandResult, error) {
	if room.Status != "open" {
		return nil, fmt.Errorf("room %q is %s", room.RoomId, room.Status)
	}
	if slices.Contains(room.Members, handle) {
		return &messagepb.RoomCommandResult{Ok: true, Room: room, Members: room.Members}, nil
	}
	room.Members = append(room.Members, handle)
	return rm.recordMembershipEvent(ctx, room, messagepb.RoomEventType_ROOM_EVENT_TYPE_MEMBER_JOINED, handle)
}

func (rm *RoomManager) leaveLocal(ctx context.Context, room *messagepb.RoomInfo, handle string) (*messagepb.RoomCommandResult, error) {
	if !slices.Contains(room.Members, handle) {
		return nil, fmt.Errorf("handle %q is not a member of room %q", handle, room.RoomId)
	}
	room.Members = removeHandle(room.Members, handle)
	return rm.recordMembershipEvent(ctx, room, messagepb.RoomEventType_ROOM_EVENT_TYPE_MEMBER_LEFT, handle)
}

func (rm *RoomManager) postLocal(ctx context.Context, room *messagepb.RoomInfo, from string, payload []byte, contentType, traceID string) (*messagepb.RoomCommandResult, error) {
	if room.Status != "open" {
		return nil, fmt.Errorf("room %q is %s", room.RoomId, room.Status)
	}
	if !isRoomMember(room, from) {
		return nil, fmt.Errorf("handle %q is not a member of room %q", from, room.RoomId)
	}
	event := rm.nextEvent(room, messagepb.RoomEventType_ROOM_EVENT_TYPE_MESSAGE_POSTED, from, "", payload, contentType, traceID)
	if err := rm.persistAndPublish(ctx, room, event); err != nil {
		return nil, err
	}
	return &messagepb.RoomCommandResult{
		Ok:        true,
		Room:      room,
		Event:     event,
	}, nil
}

func (rm *RoomManager) replayLocal(room *messagepb.RoomInfo, handle string, sinceSeq uint64) (*messagepb.RoomCommandResult, error) {
	if !isRoomMember(room, handle) {
		return nil, fmt.Errorf("handle %q is not a member of room %q", handle, room.RoomId)
	}
	events, err := rm.store.ReplayRoomEvents(room.RoomId, sinceSeq)
	if err != nil {
		return nil, err
	}
	return &messagepb.RoomCommandResult{
		Ok:        true,
		Room:      room,
		Events:    events,
	}, nil
}

func (rm *RoomManager) closeLocal(ctx context.Context, room *messagepb.RoomInfo, handle string) (*messagepb.RoomCommandResult, error) {
	if !isRoomMember(room, handle) {
		return nil, fmt.Errorf("handle %q is not a member of room %q", handle, room.RoomId)
	}
	room.Status = "closed"
	event := rm.nextEvent(room, messagepb.RoomEventType_ROOM_EVENT_TYPE_ROOM_CLOSED, handle, "", nil, "text/plain", "")
	if err := rm.persistAndPublish(ctx, room, event); err != nil {
		return nil, err
	}
	return &messagepb.RoomCommandResult{
		Ok:        true,
		Room:      room,
		Event:     event,
	}, nil
}

func (rm *RoomManager) recordMembershipEvent(ctx context.Context, room *messagepb.RoomInfo, typ messagepb.RoomEventType, subject string) (*messagepb.RoomCommandResult, error) {
	event := rm.nextEvent(room, typ, subject, subject, nil, "text/plain", "")
	if err := rm.persistAndPublish(ctx, room, event); err != nil {
		return nil, err
	}
	return &messagepb.RoomCommandResult{
		Ok:        true,
		Room:      room,
		Event:     event,
		Members:   append([]string(nil), room.Members...),
	}, nil
}

func (rm *RoomManager) nextEvent(room *messagepb.RoomInfo, typ messagepb.RoomEventType, sender, subject string, payload []byte, contentType, traceID string) *messagepb.RoomEvent {
	event := &messagepb.RoomEvent{
		EventId:      uuid.New().String(),
		RoomId:       room.RoomId,
		RoomSeq:      room.NextSeq,
		SenderHandle: sender,
		SubjectHandle: subject,
		Payload:      payload,
		ContentType:  contentType,
		SentAtUnix:   time.Now().Unix(),
		Type:         typ,
		TraceId:      traceID,
		Members:      append([]string(nil), room.Members...),
	}
	room.NextSeq++
	room.UpdatedAtUnix = event.SentAtUnix
	return event
}

func (rm *RoomManager) persistAndPublish(ctx context.Context, room *messagepb.RoomInfo, event *messagepb.RoomEvent) error {
	if err := rm.store.StoreRoom(room); err != nil {
		return err
	}
	if err := rm.store.StoreRoomEvent(event, rm.retention); err != nil {
		return err
	}
	if rm.coord != nil {
		if err := rm.coord.UpsertRoom(ctx, room); err != nil {
			return err
		}
	}
	rm.emitRoomActivity(event)
	rm.publishEvent(event)
	return nil
}

func (rm *RoomManager) emitRoomActivity(event *messagepb.RoomEvent) {
	if rm.activity == nil || event == nil {
		return
	}
	switch event.Type {
	case messagepb.RoomEventType_ROOM_EVENT_TYPE_MESSAGE_POSTED:
		contentKind, targetHandle, turnID, status, round := roomMessageActivityMeta(event)
		rm.activity.EmitRoomMessagePosted(
			event.RoomId,
			event.RoomSeq,
			event.SenderHandle,
			event.Members,
			event.TraceId,
			event.ContentType,
			contentKind,
			targetHandle,
			turnID,
			status,
			round,
		)
	case messagepb.RoomEventType_ROOM_EVENT_TYPE_MEMBER_JOINED:
		rm.activity.EmitRoomMemberJoined(event.RoomId, event.SubjectHandle, event.Members)
	case messagepb.RoomEventType_ROOM_EVENT_TYPE_MEMBER_LEFT:
		rm.activity.EmitRoomMemberLeft(event.RoomId, event.SubjectHandle, event.Members)
	case messagepb.RoomEventType_ROOM_EVENT_TYPE_ROOM_CLOSED:
		rm.activity.EmitRoomClosed(event.RoomId, event.SenderHandle, event.Members)
	}
}

func roomMessageActivityMeta(event *messagepb.RoomEvent) (contentKind, targetHandle, turnID, status string, round uint32) {
	if event == nil || event.ContentType != "application/json" || len(event.Payload) == 0 {
		return "", "", "", "", 0
	}

	var payload map[string]any
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return "", "", "", "", 0
	}

	contentKind = stringField(payload, "kind")
	targetHandle = stringField(payload, "target_handle")
	if targetHandle == "" {
		targetHandle = stringField(payload, "target")
	}
	turnID = stringField(payload, "turn_id")
	status = stringField(payload, "status")
	if roundValue, ok := payload["round"]; ok {
		switch v := roundValue.(type) {
		case float64:
			if v >= 0 {
				round = uint32(v)
			}
		case int:
			if v >= 0 {
				round = uint32(v)
			}
		}
	}
	return contentKind, targetHandle, turnID, status, round
}

func stringField(payload map[string]any, key string) string {
	value, ok := payload[key]
	if !ok {
		return ""
	}
	text, ok := value.(string)
	if !ok {
		return ""
	}
	return text
}

func (rm *RoomManager) publishEvent(event *messagepb.RoomEvent) {
	localDelivered := false
	sent := make(map[string]bool)
	for _, member := range event.Members {
		if member == event.SenderHandle {
			continue
		}
		peer, err := rm.resolver.Resolve(member)
		if err != nil {
			rm.logger.Warn("failed to resolve room member", "room_id", event.RoomId, "member", member, "error", err)
			continue
		}
		if peer.NodeID == rm.nodeID {
			localDelivered = rm.deliverer.DeliverRoomEventLocal(event) || localDelivered
			continue
		}
		if sent[peer.AdvertiseAddr] {
			continue
		}
		sent[peer.AdvertiseAddr] = true
		pubCtx, pubCancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := rm.transport.Send(pubCtx, peer.AdvertiseAddr, &transportpb.TransportMessage{
			Body: &transportpb.TransportMessage_RoomEvent{RoomEvent: event},
		}); err != nil {
			rm.logger.Warn("failed to send room event", "room_id", event.RoomId, "peer", peer.NodeID, "error", err)
		}
		pubCancel()
	}
	if !localDelivered {
		rm.deliverer.DeliverRoomEventLocal(event)
	}
}

func (rm *RoomManager) replyWithError(cmd *messagepb.RoomCommand, err error) {
	result := &messagepb.RoomCommandResult{
		RequestId: cmd.RequestId,
		Ok:        false,
		Error:     err.Error(),
	}
	if cmd.RequesterNodeId == "" || cmd.RequesterNodeId == rm.nodeID {
		rm.handleRemoteCommandResult(result)
		return
	}
	addr, lookupErr := rm.nodeAddr(cmd.RequesterNodeId)
	if lookupErr != nil {
		rm.logger.Warn("failed to resolve requester node for error reply", "node_id", cmd.RequesterNodeId, "error", lookupErr)
		return
	}
	errCtx, errCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer errCancel()
	if sendErr := rm.transport.Send(errCtx, addr, &transportpb.TransportMessage{
		Body: &transportpb.TransportMessage_RoomCommandResult{RoomCommandResult: result},
	}); sendErr != nil {
		rm.logger.Warn("failed to send room error result", "request_id", cmd.RequestId, "error", sendErr)
	}
}

func (rm *RoomManager) resolveRoomInfo(roomID string) (*messagepb.RoomInfo, error) {
	if room, err := rm.store.LoadRoom(roomID); err != nil {
		return nil, err
	} else if room != nil {
		return room, nil
	}
	info, err := rm.resolver.ResolveRoom(roomID)
	if err != nil {
		return nil, err
	}
	return &messagepb.RoomInfo{
		RoomId:        info.RoomID,
		Title:         info.Title,
		CreatedBy:     info.CreatedBy,
		HomeNodeId:    info.HomeNodeID,
		Members:       append([]string(nil), info.Members...),
		Status:        info.Status,
		NextSeq:       info.NextSeq,
		CreatedAtUnix: info.CreatedAt,
		UpdatedAtUnix: info.UpdatedAt,
	}, nil
}

func (rm *RoomManager) cacheRoomFromEvent(event *messagepb.RoomEvent) {
	if event == nil || event.RoomId == "" {
		return
	}
	homeNodeID := ""
	if event.SenderHandle != "" && rm.resolver != nil {
		if peer, err := rm.resolver.Resolve(event.SenderHandle); err == nil {
			homeNodeID = peer.NodeID
		}
	}

	room, err := rm.store.LoadRoom(event.RoomId)
	if err != nil {
		rm.logger.Warn("failed to load cached room", "room_id", event.RoomId, "error", err)
		return
	}
	if room == nil {
		room = &messagepb.RoomInfo{
			RoomId:        event.RoomId,
			HomeNodeId:    homeNodeID,
			Members:       append([]string(nil), event.Members...),
			Status:        "open",
			NextSeq:       event.RoomSeq + 1,
			CreatedAtUnix: event.SentAtUnix,
			UpdatedAtUnix: event.SentAtUnix,
		}
	} else {
		if room.HomeNodeId == "" {
			room.HomeNodeId = homeNodeID
		}
		room.Members = append([]string(nil), event.Members...)
		if room.NextSeq <= event.RoomSeq {
			room.NextSeq = event.RoomSeq + 1
		}
		room.UpdatedAtUnix = event.SentAtUnix
	}
	if event.Type == messagepb.RoomEventType_ROOM_EVENT_TYPE_ROOM_CLOSED {
		room.Status = "closed"
	}
	rm.cacheRoom(room)
}

func (rm *RoomManager) cacheRoom(room *messagepb.RoomInfo) {
	if room == nil || room.RoomId == "" {
		return
	}
	if err := rm.store.StoreRoom(room); err != nil {
		rm.logger.Warn("failed to cache room metadata", "room_id", room.RoomId, "error", err)
	}
}

func (rm *RoomManager) nodeAddr(nodeID string) (string, error) {
	for _, node := range rm.resolver.GetNodes() {
		if node.NodeID == nodeID {
			return node.AdvertiseAddr, nil
		}
	}
	return "", fmt.Errorf("node %q not found", nodeID)
}

func isRoomMember(room *messagepb.RoomInfo, handle string) bool {
	return slices.Contains(room.Members, handle)
}

func uniqueHandles(handles []string) []string {
	var result []string
	seen := make(map[string]bool, len(handles))
	for _, handle := range handles {
		if handle == "" || seen[handle] {
			continue
		}
		seen[handle] = true
		result = append(result, handle)
	}
	return result
}

func removeHandle(handles []string, target string) []string {
	result := make([]string, 0, len(handles))
	for _, handle := range handles {
		if handle != target {
			result = append(result, handle)
		}
	}
	return result
}
