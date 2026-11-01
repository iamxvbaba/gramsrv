package polls

import (
	"context"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

// Service provides send-time creation of authoritative poll state and voter list queries.
// Voting/closing goes through the messages/channels services (message visibility is required) and not through this service.
type Service struct {
	polls store.PollStore
}

// NewService creates the poll service.
func NewService(polls store.PollStore) *Service {
	return &Service{polls: polls}
}

// CreatePoll persists the authoritative poll row before the message is sent (orphan rows from failed sends are harmless).
func (s *Service) CreatePoll(ctx context.Context, def domain.PollDefinition) error {
	if s == nil || s.polls == nil {
		return domain.ErrPollInvalid
	}
	if def.ID == 0 || def.CreatorUserID == 0 || len(def.Options) < domain.MinPollAnswers || len(def.Options) > domain.MaxPollAnswers {
		return domain.ErrPollInvalid
	}
	return s.polls.CreatePoll(ctx, def)
}

// GetPollDefinition returns the authoritative definition (used by getPollVotes for public_voters/broadcast checks).
func (s *Service) GetPollDefinition(ctx context.Context, pollID int64) (domain.PollDefinition, bool, error) {
	if s == nil || s.polls == nil || pollID == 0 {
		return domain.PollDefinition{}, false, nil
	}
	return s.polls.GetPollDefinition(ctx, pollID)
}

// ListPollVotes lists voters with pagination (only public_voters polls are allowed by the rpc layer).
func (s *Service) ListPollVotes(ctx context.Context, req domain.PollVotesListRequest) (domain.PollVotesList, error) {
	if s == nil || s.polls == nil {
		return domain.PollVotesList{}, domain.ErrPollNotFound
	}
	if req.PollID == 0 || req.Limit <= 0 || req.Limit > domain.MaxPollVotesPageLimit {
		return domain.PollVotesList{}, domain.ErrPollInvalid
	}
	return s.polls.ListPollVotes(ctx, req)
}
