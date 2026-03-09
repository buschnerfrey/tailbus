package daemon

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	agentpb "github.com/alexanderfrey/tailbus/api/agentpb"
	messagepb "github.com/alexanderfrey/tailbus/api/messagepb"
	transportpb "github.com/alexanderfrey/tailbus/api/transportpb"
	"github.com/alexanderfrey/tailbus/internal/handle"
	"github.com/alexanderfrey/tailbus/internal/transport"
	"github.com/prometheus/client_golang/prometheus"
)

// MessageRouter routes envelopes to local agents or remote daemons.
type MessageRouter struct {
	resolver   *handle.Resolver
	transport  transport.Transport
	local      LocalDeliverer
	logger     *slog.Logger
	activity   *ActivityBus
	traceStore *TraceStore
	metrics    *Metrics
	nodeID     string
	ackTracker *AckTracker
}

// LocalDeliverer delivers messages to local agents.
type LocalDeliverer interface {
	DeliverToLocal(env *messagepb.Envelope) bool
	HasHandle(h string) bool
}

// NewMessageRouter creates a new message router.
func NewMessageRouter(resolver *handle.Resolver, transport transport.Transport, local LocalDeliverer, activity *ActivityBus, logger *slog.Logger) *MessageRouter {
	return &MessageRouter{
		resolver:  resolver,
		transport: transport,
		local:     local,
		logger:    logger,
		activity:  activity,
	}
}

// SetTracing sets the trace store, metrics, and node ID for tracing support.
func (r *MessageRouter) SetTracing(ts *TraceStore, m *Metrics, nodeID string) {
	r.traceStore = ts
	r.metrics = m
	r.nodeID = nodeID
}

// SetAckTracker sets the ACK tracker for the router.
func (r *MessageRouter) SetAckTracker(at *AckTracker) {
	r.ackTracker = at
}

// Route routes an envelope to the appropriate destination.
func (r *MessageRouter) Route(ctx context.Context, env *messagepb.Envelope) error {
	start := time.Now()

	// Check if the destination is local
	if r.local.HasHandle(env.ToHandle) {
		if r.local.DeliverToLocal(env) {
			r.recordRouteDone(env, false, start)
			return nil
		}
		return fmt.Errorf("handle %q is local but has no subscribers", env.ToHandle)
	}

	// Resolve to a remote peer
	peer, err := r.resolver.Resolve(env.ToHandle)
	if err != nil {
		return fmt.Errorf("resolve handle %q: %w", env.ToHandle, err)
	}

	r.logger.Debug("routing to remote peer", "handle", env.ToHandle, "peer", peer.NodeID, "addr", peer.AdvertiseAddr)
	msg := &transportpb.TransportMessage{
		Body: &transportpb.TransportMessage_Envelope{Envelope: env},
	}
	if err := r.transport.Send(ctx, peer.AdvertiseAddr, msg); err != nil {
		return err
	}

	// Track for ACK (don't track ACK messages themselves)
	if r.ackTracker != nil && env.Type != messagepb.EnvelopeType_ENVELOPE_TYPE_ACK {
		r.ackTracker.Track(env, peer.AdvertiseAddr)
	}

	r.recordRouteDone(env, true, start)
	return nil
}

func (r *MessageRouter) recordRouteDone(env *messagepb.Envelope, remote bool, start time.Time) {
	duration := time.Since(start)

	// Don't emit activity events for ACKs — they're internal bookkeeping
	// and would pollute the dashboard with spurious flashes.
	if r.activity != nil && env.Type != messagepb.EnvelopeType_ENVELOPE_TYPE_ACK {
		r.activity.EmitMessageRouted(env.SessionId, env.FromHandle, env.ToHandle, remote, env.TraceId, env.MessageId)
	}

	if r.metrics != nil {
		(*r.metrics.MessageRoutingDuration).(prometheus.Histogram).Observe(duration.Seconds())
	}

	if r.traceStore != nil && env.TraceId != "" {
		action := agentpb.TraceAction_TRACE_ACTION_ROUTED_LOCAL
		if remote {
			action = agentpb.TraceAction_TRACE_ACTION_ROUTED_REMOTE
		}
		r.traceStore.RecordSpan(env.TraceId, env.MessageId, r.nodeID, action, map[string]string{
			"from": env.FromHandle,
			"to":   env.ToHandle,
		})
	}
}
