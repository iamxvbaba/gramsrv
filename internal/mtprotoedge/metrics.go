package mtprotoedge

import "time"

// Metrics receives connection-layer runtime metrics. The production entry point
// wires in a bounded Prometheus exporter; other embedders can keep using
// NopMetrics (zero overhead).
type Metrics interface {
	// ConnOpened is called when a connection is accepted.
	ConnOpened()
	// ConnClosed is called when a connection ends.
	ConnClosed()
	// HandshakeDone is called when a key exchange completes successfully; d is the handshake duration.
	HandshakeDone(d time.Duration)
	// RPCHandled is called when an RPC finishes handling: method is the TL method
	// name, d is the duration, and a non-nil err means failure.
	RPCHandled(method string, d time.Duration, err error)
	// InboundRPCQueued is called when an RPC successfully enters the per-connection bounded queue.
	InboundRPCQueued(method string, len, cap int)
	// InboundRPCStarted is called when an RPC is taken from the bounded queue and starts executing.
	InboundRPCStarted(method string, queueWait time.Duration)
	// InboundRPCDropped is called when an RPC is dropped due to a full queue, a closed connection or a scheduling error.
	InboundRPCDropped(method, reason string)
	// OutboundSend is called when a server outbound message finishes writing or fails.
	OutboundSend(typeID uint32, queueWait time.Duration, bytes int, err error)
	// OutboundResend is called after a resend triggered by msg_resend_req/a duplicate RPC.
	OutboundResend(count int, err error)
	// OutboundDropped is called when the outbound queue or state tracking is dropped due to backpressure.
	OutboundDropped(reason string)
	// OutboundQueueWait is called when outbound enqueue waiting exceeds the threshold.
	OutboundQueueWait(len, cap int)
}

// RPCDatabaseMetrics is an optional extension for request-scoped database
// work. queries/errors are counts attributed to one RPC and duration is the
// cumulative database time observed by the query wrapper under that request.
type RPCDatabaseMetrics interface {
	RPCDatabase(method string, queries int64, duration time.Duration, errors int64)
}

// RPCResultMetrics is an optional extension for the detached response pipeline.
// Keeping it separate preserves lightweight embedders while production exporters
// can observe preparation/compression and end-to-end delivery independently from
// business handler latency.
type RPCResultMetrics interface {
	RPCResultPrepared(method, priority string, innerBytes, wireBytes int, compressed bool)
	RPCResultDelivered(method string, egressLatency time.Duration, wireBytes int, err error)
}

// LogicalOutboxMetrics observes the sole owner of unacknowledged server frames.
// It is intentionally optional: embedders can keep the small Metrics surface,
// while production capacity tests can distinguish physical delivery from the
// later client ACK that actually releases retained bytes.
type LogicalOutboxMetrics interface {
	LogicalOutboxAcknowledged(bytes int, retainedFor time.Duration, rpcResult bool)
}

// ConnectionIntakeMetrics is an optional extension for the pre-session
// connection pipeline. stage is one of raw_accept, mux_sniff, mux_delivery,
// transport_dispatch, transport_promote, or first_frame; outcome is a bounded
// value suitable for metrics labels. Remote addresses are deliberately kept in
// structured logs only so exporters cannot create unbounded cardinality.
type ConnectionIntakeMetrics interface {
	ConnectionIntake(stage, outcome string, duration time.Duration)
}

// NopMetrics is the empty implementation of Metrics.
type NopMetrics struct{}

// ConnOpened implements Metrics.
func (NopMetrics) ConnOpened() {}

// ConnClosed implements Metrics.
func (NopMetrics) ConnClosed() {}

// HandshakeDone implements Metrics.
func (NopMetrics) HandshakeDone(time.Duration) {}

// RPCHandled implements Metrics.
func (NopMetrics) RPCHandled(string, time.Duration, error) {}

// InboundRPCQueued implements Metrics.
func (NopMetrics) InboundRPCQueued(string, int, int) {}

// InboundRPCStarted implements Metrics.
func (NopMetrics) InboundRPCStarted(string, time.Duration) {}

// InboundRPCDropped implements Metrics.
func (NopMetrics) InboundRPCDropped(string, string) {}

// OutboundSend implements Metrics.
func (NopMetrics) OutboundSend(uint32, time.Duration, int, error) {}

// OutboundResend implements Metrics.
func (NopMetrics) OutboundResend(int, error) {}

// OutboundDropped implements Metrics.
func (NopMetrics) OutboundDropped(string) {}

// OutboundQueueWait implements Metrics.
func (NopMetrics) OutboundQueueWait(int, int) {}
