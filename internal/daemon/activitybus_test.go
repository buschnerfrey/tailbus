package daemon

import (
	"sync"
	"testing"
	"time"

	agentpb "github.com/alexanderfrey/tailbus/api/agentpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type testUsageRecorder struct {
	mu      sync.Mutex
	metrics []agentpb.UsageMetric
}

func (r *testUsageRecorder) RecordUsage(metric agentpb.UsageMetric, at time.Time, count int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := int64(0); i < count; i++ {
		r.metrics = append(r.metrics, metric)
	}
	return nil
}

func TestActivityBus_SubscribeEmitUnsubscribe(t *testing.T) {
	bus := NewActivityBus()

	ch := bus.Subscribe()

	event := &agentpb.ActivityEvent{
		Timestamp: timestamppb.Now(),
		Event: &agentpb.ActivityEvent_HandleRegistered{
			HandleRegistered: &agentpb.HandleRegisteredEvent{Handle: "test"},
		},
	}

	bus.Emit(event)

	select {
	case got := <-ch:
		reg := got.GetHandleRegistered()
		if reg == nil || reg.Handle != "test" {
			t.Fatalf("unexpected event: %v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for event")
	}

	bus.Unsubscribe(ch)

	// Channel should be closed after unsubscribe
	_, ok := <-ch
	if ok {
		t.Fatal("channel should be closed after unsubscribe")
	}
}

func TestActivityBus_MultipleSubscribers(t *testing.T) {
	bus := NewActivityBus()

	ch1 := bus.Subscribe()
	ch2 := bus.Subscribe()

	bus.EmitHandleRegistered("multi")

	for _, ch := range []chan *agentpb.ActivityEvent{ch1, ch2} {
		select {
		case got := <-ch:
			if got.GetHandleRegistered().Handle != "multi" {
				t.Fatalf("unexpected: %v", got)
			}
		case <-time.After(time.Second):
			t.Fatal("timed out")
		}
	}

	bus.Unsubscribe(ch1)
	bus.Unsubscribe(ch2)
}

func TestActivityBus_NonBlockingEmit(t *testing.T) {
	bus := NewActivityBus()

	ch := bus.Subscribe()

	// Fill the channel buffer (64 events)
	for i := 0; i < 100; i++ {
		bus.EmitHandleRegistered("flood")
	}

	// Should not block or panic — drain what we can
	count := 0
	for {
		select {
		case <-ch:
			count++
		default:
			goto done
		}
	}
done:
	if count == 0 {
		t.Fatal("expected at least some events")
	}
	if count > 64 {
		t.Fatalf("got %d events, expected at most 64 (buffer size)", count)
	}

	bus.Unsubscribe(ch)
}

func TestActivityBus_Counters(t *testing.T) {
	bus := NewActivityBus()

	bus.EmitSessionOpened("s1", "a", "b")
	bus.EmitSessionOpened("s2", "c", "d")
	bus.EmitSessionResolved("s1", "a")
	bus.EmitMessageRouted("s1", "a", "b", false, "t1", "m1")
	bus.EmitMessageRouted("s2", "c", "d", true, "t2", "m2")
	bus.EmitMessageRouted("s2", "c", "d", true, "t2", "m3")
	bus.EmitRoomCreated("room-1", "design-review", "a", []string{"a", "b"})
	bus.EmitRoomMessagePosted("room-1", 1, "a", []string{"a", "b"}, "trace-room", "application/json", "turn_request", "b", "turn-1", "", 1)
	bus.EmitRoomMemberJoined("room-1", "c", []string{"a", "b", "c"})
	bus.EmitRoomMemberLeft("room-1", "b", []string{"a", "c"})
	bus.EmitRoomClosed("room-1", "a", []string{"a", "c"})
	bus.MessagesReceivedRemote.Add(3)

	c := bus.Counters()

	if c.SessionsOpened != 2 {
		t.Errorf("sessions opened = %d, want 2", c.SessionsOpened)
	}
	if c.SessionsResolved != 1 {
		t.Errorf("sessions resolved = %d, want 1", c.SessionsResolved)
	}
	if c.MessagesRouted != 3 {
		t.Errorf("messages routed = %d, want 3", c.MessagesRouted)
	}
	if c.MessagesDeliveredLocal != 1 {
		t.Errorf("messages delivered local = %d, want 1", c.MessagesDeliveredLocal)
	}
	if c.MessagesSentRemote != 2 {
		t.Errorf("messages sent remote = %d, want 2", c.MessagesSentRemote)
	}
	if c.MessagesReceivedRemote != 3 {
		t.Errorf("messages received remote = %d, want 3", c.MessagesReceivedRemote)
	}
	if c.RoomsCreated != 1 {
		t.Errorf("rooms created = %d, want 1", c.RoomsCreated)
	}
	if c.RoomMessagesPosted != 1 {
		t.Errorf("room messages posted = %d, want 1", c.RoomMessagesPosted)
	}
	if c.RoomMembersJoined != 1 {
		t.Errorf("room members joined = %d, want 1", c.RoomMembersJoined)
	}
	if c.RoomMembersLeft != 1 {
		t.Errorf("room members left = %d, want 1", c.RoomMembersLeft)
	}
	if c.RoomsClosed != 1 {
		t.Errorf("rooms closed = %d, want 1", c.RoomsClosed)
	}
}

func TestActivityBus_RecordsUsageMetrics(t *testing.T) {
	bus := NewActivityBus()
	recorder := &testUsageRecorder{}
	bus.SetUsageRecorder(recorder)

	bus.EmitMessageRouted("s1", "a", "b", false, "", "")
	bus.EmitSessionOpened("s1", "a", "b")
	bus.EmitRoomCreated("room-1", "design", "a", []string{"a"})
	bus.EmitRoomMessagePosted("room-1", 1, "a", []string{"a"}, "", "application/json", "turn_request", "b", "turn-1", "", 1)

	got := recorder.metrics
	want := []agentpb.UsageMetric{
		agentpb.UsageMetric_USAGE_METRIC_MESSAGES_ROUTED,
		agentpb.UsageMetric_USAGE_METRIC_SESSIONS_OPENED,
		agentpb.UsageMetric_USAGE_METRIC_ROOMS_CREATED,
		agentpb.UsageMetric_USAGE_METRIC_ROOM_MESSAGES_POSTED,
	}
	if len(got) != len(want) {
		t.Fatalf("usage metrics = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("usage metric[%d] = %v, want %v", i, got[i], want[i])
		}
	}
}

func TestActivityBus_ConcurrentEmit(t *testing.T) {
	bus := NewActivityBus()
	ch := bus.Subscribe()

	var wg sync.WaitGroup
	n := 100
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			bus.EmitHandleRegistered("concurrent")
		}()
	}
	wg.Wait()

	// Drain
	count := 0
	for {
		select {
		case <-ch:
			count++
		default:
			goto done
		}
	}
done:
	if count == 0 {
		t.Fatal("expected some events")
	}

	bus.Unsubscribe(ch)
}
