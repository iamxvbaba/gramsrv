package rpc

import "time"

// Metrics receives RPC business-layer metrics. Defaults to NopMetrics; can later be wired to
// Prometheus.
type Metrics interface {
	MessageSend(d time.Duration, duplicate bool, err error)
	MessageRateLimited(retryAfterSeconds int)
	OutboxClaimed(count int)
	OutboxDelivered(d time.Duration)
	OutboxFailed(err error)
	PresenceLastSeenBatch(count int, d time.Duration, err error)
	PresenceLastSeenSubmitted()
	PresenceLastSeenPending(delta int)
	PresenceLastSeenOverflow()
	PresenceLastSeenDrainDropped(count int)
}

// NopMetrics is the no-op implementation of Metrics.
type NopMetrics struct{}

func (NopMetrics) MessageSend(time.Duration, bool, error) {}

func (NopMetrics) MessageRateLimited(int) {}

func (NopMetrics) OutboxClaimed(int) {}

func (NopMetrics) OutboxDelivered(time.Duration) {}

func (NopMetrics) OutboxFailed(error) {}

func (NopMetrics) PresenceLastSeenBatch(int, time.Duration, error) {}

func (NopMetrics) PresenceLastSeenSubmitted() {}

func (NopMetrics) PresenceLastSeenPending(int) {}

func (NopMetrics) PresenceLastSeenOverflow() {}

func (NopMetrics) PresenceLastSeenDrainDropped(int) {}
