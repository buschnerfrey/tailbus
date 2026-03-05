package daemon

import (
	"sync"
	"sync/atomic"
	"time"

	agentpb "github.com/alexanderfrey/tailbus/api/agentpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// ActivityBus is an in-process pub/sub for dashboard activity events.
// It uses non-blocking sends so subscribers never slow down the daemon.
type ActivityBus struct {
	mu   sync.RWMutex
	subs map[chan *agentpb.ActivityEvent]struct{}

	MessagesRouted        atomic.Int64
	MessagesDeliveredLocal atomic.Int64
	MessagesSentRemote    atomic.Int64
	MessagesReceivedRemote atomic.Int64
	SessionsOpened        atomic.Int64
	SessionsResolved      atomic.Int64
	RoomsCreated          atomic.Int64
	RoomMessagesPosted    atomic.Int64
	RoomMembersJoined     atomic.Int64
	RoomMembersLeft       atomic.Int64
	RoomsClosed           atomic.Int64
}

// NewActivityBus creates a new activity bus.
func NewActivityBus() *ActivityBus {
	return &ActivityBus{
		subs: make(map[chan *agentpb.ActivityEvent]struct{}),
	}
}

// Subscribe returns a channel that receives activity events.
// The caller must call Unsubscribe when done.
func (b *ActivityBus) Subscribe() chan *agentpb.ActivityEvent {
	ch := make(chan *agentpb.ActivityEvent, 64)
	b.mu.Lock()
	b.subs[ch] = struct{}{}
	b.mu.Unlock()
	return ch
}

// Unsubscribe removes a subscriber channel.
func (b *ActivityBus) Unsubscribe(ch chan *agentpb.ActivityEvent) {
	b.mu.Lock()
	delete(b.subs, ch)
	b.mu.Unlock()
	close(ch)
}

// Emit fans out an event to all subscribers without blocking.
func (b *ActivityBus) Emit(event *agentpb.ActivityEvent) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for ch := range b.subs {
		select {
		case ch <- event:
		default:
			// Drop event rather than block the daemon
		}
	}
}

// Counters returns a snapshot of all atomic counters.
func (b *ActivityBus) Counters() *agentpb.Counters {
	return &agentpb.Counters{
		MessagesRouted:        b.MessagesRouted.Load(),
		MessagesDeliveredLocal: b.MessagesDeliveredLocal.Load(),
		MessagesSentRemote:    b.MessagesSentRemote.Load(),
		MessagesReceivedRemote: b.MessagesReceivedRemote.Load(),
		SessionsOpened:        b.SessionsOpened.Load(),
		SessionsResolved:      b.SessionsResolved.Load(),
		RoomsCreated:          b.RoomsCreated.Load(),
		RoomMessagesPosted:    b.RoomMessagesPosted.Load(),
		RoomMembersJoined:     b.RoomMembersJoined.Load(),
		RoomMembersLeft:       b.RoomMembersLeft.Load(),
		RoomsClosed:           b.RoomsClosed.Load(),
	}
}

// Helper methods to emit typed events.

func (b *ActivityBus) EmitMessageRouted(sessionID, from, to string, remote bool, traceID, messageID string) {
	b.MessagesRouted.Add(1)
	if remote {
		b.MessagesSentRemote.Add(1)
	} else {
		b.MessagesDeliveredLocal.Add(1)
	}
	b.Emit(&agentpb.ActivityEvent{
		Timestamp: timestamppb.Now(),
		Event: &agentpb.ActivityEvent_MessageRouted{
			MessageRouted: &agentpb.MessageRoutedEvent{
				SessionId:  sessionID,
				FromHandle: from,
				ToHandle:   to,
				Remote:     remote,
				TraceId:    traceID,
				MessageId:  messageID,
			},
		},
	})
}

func (b *ActivityBus) EmitSessionOpened(sessionID, from, to string) {
	b.SessionsOpened.Add(1)
	b.Emit(&agentpb.ActivityEvent{
		Timestamp: timestamppb.Now(),
		Event: &agentpb.ActivityEvent_SessionOpened{
			SessionOpened: &agentpb.SessionOpenedEvent{
				SessionId:  sessionID,
				FromHandle: from,
				ToHandle:   to,
			},
		},
	})
}

func (b *ActivityBus) EmitSessionResolved(sessionID, from string) {
	b.SessionsResolved.Add(1)
	b.Emit(&agentpb.ActivityEvent{
		Timestamp: timestamppb.Now(),
		Event: &agentpb.ActivityEvent_SessionResolved{
			SessionResolved: &agentpb.SessionResolvedEvent{
				SessionId:  sessionID,
				FromHandle: from,
			},
		},
	})
}

func (b *ActivityBus) EmitHandleRegistered(handle string) {
	b.Emit(&agentpb.ActivityEvent{
		Timestamp: timestamppb.Now(),
		Event: &agentpb.ActivityEvent_HandleRegistered{
			HandleRegistered: &agentpb.HandleRegisteredEvent{
				Handle: handle,
			},
		},
	})
}

func (b *ActivityBus) EmitRoomCreated(roomID, title, createdBy string, members []string) {
	b.RoomsCreated.Add(1)
	b.Emit(&agentpb.ActivityEvent{
		Timestamp: timestamppb.Now(),
		Event: &agentpb.ActivityEvent_RoomCreated{
			RoomCreated: &agentpb.RoomCreatedEvent{
				RoomId:        roomID,
				Title:         title,
				CreatedBy:     createdBy,
				MemberHandles: append([]string(nil), members...),
			},
		},
	})
}

func (b *ActivityBus) EmitRoomMessagePosted(
	roomID string,
	roomSeq uint64,
	from string,
	members []string,
	traceID string,
	contentType string,
	contentKind string,
	targetHandle string,
	turnID string,
	status string,
	round uint32,
) {
	b.RoomMessagesPosted.Add(1)
	b.Emit(&agentpb.ActivityEvent{
		Timestamp: timestamppb.Now(),
		Event: &agentpb.ActivityEvent_RoomMessagePosted{
			RoomMessagePosted: &agentpb.RoomMessagePostedEvent{
				RoomId:        roomID,
				RoomSeq:       roomSeq,
				FromHandle:    from,
				MemberHandles: append([]string(nil), members...),
				TraceId:       traceID,
				ContentType:   contentType,
				ContentKind:   contentKind,
				TargetHandle:  targetHandle,
				TurnId:        turnID,
				Status:        status,
				Round:         round,
			},
		},
	})
}

func (b *ActivityBus) EmitRoomMemberJoined(roomID, handle string, members []string) {
	b.RoomMembersJoined.Add(1)
	b.Emit(&agentpb.ActivityEvent{
		Timestamp: timestamppb.Now(),
		Event: &agentpb.ActivityEvent_RoomMemberJoined{
			RoomMemberJoined: &agentpb.RoomMemberJoinedEvent{
				RoomId:        roomID,
				Handle:        handle,
				MemberHandles: append([]string(nil), members...),
			},
		},
	})
}

func (b *ActivityBus) EmitRoomMemberLeft(roomID, handle string, members []string) {
	b.RoomMembersLeft.Add(1)
	b.Emit(&agentpb.ActivityEvent{
		Timestamp: timestamppb.Now(),
		Event: &agentpb.ActivityEvent_RoomMemberLeft{
			RoomMemberLeft: &agentpb.RoomMemberLeftEvent{
				RoomId:        roomID,
				Handle:        handle,
				MemberHandles: append([]string(nil), members...),
			},
		},
	})
}

func (b *ActivityBus) EmitRoomClosed(roomID, closedBy string, members []string) {
	b.RoomsClosed.Add(1)
	b.Emit(&agentpb.ActivityEvent{
		Timestamp: timestamppb.Now(),
		Event: &agentpb.ActivityEvent_RoomClosed{
			RoomClosed: &agentpb.RoomClosedEvent{
				RoomId:        roomID,
				ClosedBy:      closedBy,
				MemberHandles: append([]string(nil), members...),
			},
		},
	})
}

// now is a helper for testing; kept unexported. In production we use timestamppb.Now() directly.
func now() time.Time {
	return time.Now()
}
