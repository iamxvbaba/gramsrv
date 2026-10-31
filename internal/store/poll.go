package store

import (
	"context"

	"telesrv/internal/domain"
)

// PollStore persists the authoritative poll state (definition + closed + secret) and voter-list
// queries. Voting/closing goes through message-side entry points such as
// MessageStore.VoteMessagePoll / ChannelStore.VoteChannelMessagePoll (which need message
// visibility checks); this interface only handles creating the poll on send and the
// getPollVotes list.
type PollStore interface {
	CreatePoll(ctx context.Context, def domain.PollDefinition) error
	GetPollDefinition(ctx context.Context, pollID int64) (domain.PollDefinition, bool, error)
	ListPollVotes(ctx context.Context, req domain.PollVotesListRequest) (domain.PollVotesList, error)
}
