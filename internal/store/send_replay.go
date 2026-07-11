package store

import (
	"context"

	"telesrv/internal/domain"
)

// PrivateSendReplayStore exposes the immutable random_id receipt independently from the send
// command.  App/RPC preflight uses it before rate limits, permission gates and media/source
// resolution; SendPrivateText still owns the transactional race fence.
type PrivateSendReplayStore interface {
	LookupPrivateSendReplay(ctx context.Context, req domain.PrivateSendReplayRequest) (domain.SendPrivateTextResult, bool, error)
}

// ChannelSendReplayStore is the channel/monoforum counterpart. SavedPeer in the request is part
// of the monoforum idempotency scope and is zero for ordinary channel sends.
type ChannelSendReplayStore interface {
	LookupChannelSendReplay(ctx context.Context, req domain.ChannelSendReplayRequest) (domain.SendChannelMessageResult, bool, error)
}
