package memory

import (
	"context"
	"sort"
	"sync"
	"time"

	"telesrv/internal/domain"
)

// PollStore is the in-memory implementation of store.PollStore and doubles as the shared
// authority for poll voting and read-path enrichment in MessageStore/ChannelStore (corresponding
// to the polls / poll_votes tables on the postgres side). Validation and gating reuse
// domain.ValidatePollVote / ResolvePollResults to stay consistent with postgres behavior.
type PollStore struct {
	mu    sync.RWMutex
	polls map[int64]domain.PollDefinition
	votes map[int64]map[int64]domain.PollVote
}

// NewPollStore creates the in-memory poll authority store.
func NewPollStore() *PollStore {
	return &PollStore{
		polls: make(map[int64]domain.PollDefinition),
		votes: make(map[int64]map[int64]domain.PollVote),
	}
}

func (s *PollStore) CreatePoll(_ context.Context, def domain.PollDefinition) error {
	if def.ID == 0 || len(def.Options) == 0 {
		return domain.ErrPollInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.polls[def.ID]; exists {
		return domain.ErrPollInvalid
	}
	s.polls[def.ID] = clonePollDefinition(def)
	return nil
}

func (s *PollStore) GetPollDefinition(_ context.Context, pollID int64) (domain.PollDefinition, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	def, ok := s.polls[pollID]
	if !ok {
		return domain.PollDefinition{}, false, nil
	}
	return clonePollDefinition(def), true, nil
}

func (s *PollStore) ListPollVotes(_ context.Context, req domain.PollVotesListRequest) (domain.PollVotesList, error) {
	if req.PollID == 0 || req.Limit <= 0 {
		return domain.PollVotesList{}, domain.ErrPollInvalid
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if _, ok := s.polls[req.PollID]; !ok {
		return domain.PollVotesList{}, domain.ErrPollNotFound
	}
	rows := make([]domain.PollVote, 0, len(s.votes[req.PollID]))
	for _, vote := range s.votes[req.PollID] {
		if len(req.Option) > 0 && !voteHasOption(vote, req.Option) {
			continue
		}
		rows = append(rows, clonePollVote(vote))
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Date != rows[j].Date {
			return rows[i].Date > rows[j].Date
		}
		return rows[i].UserID > rows[j].UserID
	})
	out := domain.PollVotesList{Count: len(rows)}
	start := 0
	if req.OffsetDate > 0 || req.OffsetUserID > 0 {
		for i, row := range rows {
			if row.Date < req.OffsetDate || (row.Date == req.OffsetDate && row.UserID < req.OffsetUserID) {
				start = i
				break
			}
			start = i + 1
		}
	}
	end := start + req.Limit
	if end > len(rows) {
		end = len(rows)
	}
	if start < end {
		out.Votes = rows[start:end]
	}
	out.HasMore = end < len(rows)
	return out, nil
}

// Vote validates and records a vote (an empty options list retracts the vote). All validation logic comes from domain.ValidatePollVote.
func (s *PollStore) Vote(pollID, userID int64, options [][]byte, date int) error {
	if pollID == 0 || userID == 0 {
		return domain.ErrPollNotFound
	}
	if date == 0 {
		date = int(time.Now().Unix())
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	def, ok := s.polls[pollID]
	if !ok {
		return domain.ErrPollNotFound
	}
	var existing [][]byte
	if vote, voted := s.votes[pollID][userID]; voted {
		existing = vote.Options
	}
	if err := domain.ValidatePollVote(def, existing, options, date); err != nil {
		return err
	}
	if len(options) == 0 {
		delete(s.votes[pollID], userID)
		return nil
	}
	if s.votes[pollID] == nil {
		s.votes[pollID] = make(map[int64]domain.PollVote)
	}
	s.votes[pollID][userID] = clonePollVote(domain.PollVote{PollID: pollID, UserID: userID, Options: options, Date: date})
	return nil
}

// Close closes the poll; only the creator can close it, and repeated closes are idempotent.
func (s *PollStore) Close(pollID, byUserID int64) error {
	if pollID == 0 || byUserID == 0 {
		return domain.ErrPollNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	def, ok := s.polls[pollID]
	if !ok {
		return domain.ErrPollNotFound
	}
	if def.CreatorUserID != byUserID {
		return domain.ErrPollNotCreator
	}
	def.Closed = true
	s.polls[pollID] = def
	return nil
}

// EnrichPoll writes the authoritative state plus viewer-perspective aggregates back into the media definition snapshot; the snapshot is left unchanged when the poll does not exist.
func (s *PollStore) EnrichPoll(poll *domain.MessagePoll, viewerUserID int64, now int) {
	if s == nil || poll == nil || poll.ID == 0 {
		return
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	def, ok := s.polls[poll.ID]
	if !ok {
		return
	}
	agg := domain.PollAggregates{Counts: make(map[string]int)}
	recent := make([]domain.PollVote, 0, len(s.votes[poll.ID]))
	for userID, vote := range s.votes[poll.ID] {
		agg.TotalVoters++
		for _, option := range vote.Options {
			agg.Counts[string(option)]++
		}
		if userID == viewerUserID {
			agg.ViewerOptions = append([][]byte(nil), vote.Options...)
		}
		recent = append(recent, vote)
	}
	sort.Slice(recent, func(i, j int) bool {
		if recent[i].Date != recent[j].Date {
			return recent[i].Date > recent[j].Date
		}
		return recent[i].UserID > recent[j].UserID
	})
	for i, vote := range recent {
		if i >= domain.MaxPollRecentVoters {
			break
		}
		agg.RecentVoters = append(agg.RecentVoters, vote.UserID)
	}
	results := domain.ResolvePollResults(def, agg, viewerUserID, now)
	domain.ApplyPollState(poll, def, results, now)
}

// EnrichPollForViewers returns a per-viewer enriched poll for a batch of viewers (templated
// fan-out): viewer-invariant aggregates (counts/total/recent) are iterated once plus per-viewer
// ViewerOptions, and each viewer is composed with exactly the same ResolvePollResults/ApplyPollState
// as the single-viewer EnrichPoll (byte-identical). It returns map[viewer] with each viewer's poll
// clone; when def does not exist it returns an empty map (matching the no-op of EnrichPoll).
func (s *PollStore) EnrichPollForViewers(basePoll *domain.MessagePoll, viewers []int64, now int) map[int64]*domain.MessagePoll {
	out := make(map[int64]*domain.MessagePoll, len(viewers))
	if s == nil || basePoll == nil || basePoll.ID == 0 || len(viewers) == 0 {
		return out
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	def, ok := s.polls[basePoll.ID]
	if !ok {
		return out
	}
	counts := make(map[string]int)
	total := 0
	optionsByUser := make(map[int64][][]byte, len(s.votes[basePoll.ID]))
	recent := make([]domain.PollVote, 0, len(s.votes[basePoll.ID]))
	for userID, vote := range s.votes[basePoll.ID] {
		total++
		for _, option := range vote.Options {
			counts[string(option)]++
		}
		optionsByUser[userID] = append([][]byte(nil), vote.Options...)
		recent = append(recent, vote)
	}
	sort.Slice(recent, func(i, j int) bool {
		if recent[i].Date != recent[j].Date {
			return recent[i].Date > recent[j].Date
		}
		return recent[i].UserID > recent[j].UserID
	})
	recentVoters := make([]int64, 0, domain.MaxPollRecentVoters)
	for i, vote := range recent {
		if i >= domain.MaxPollRecentVoters {
			break
		}
		recentVoters = append(recentVoters, vote.UserID)
	}
	for _, viewer := range viewers {
		agg := domain.PollAggregates{
			Counts:        counts,
			TotalVoters:   total,
			RecentVoters:  recentVoters,
			ViewerOptions: optionsByUser[viewer],
		}
		results := domain.ResolvePollResults(def, agg, viewer, now)
		pollCopy := *basePoll
		domain.ApplyPollState(&pollCopy, def, results, now)
		out[viewer] = &pollCopy
	}
	return out
}

// enrichPollMediaForViewer clones the media (avoiding a shared pointer writing viewer state into the store itself) and enriches it.
func enrichPollMediaForViewer(polls *PollStore, media *domain.MessageMedia, viewerUserID int64, now int) *domain.MessageMedia {
	if polls == nil || media == nil || media.Kind != domain.MessageMediaKindPoll || media.Poll == nil {
		return media
	}
	cloned := *media
	poll := *media.Poll
	poll.Answers = append([]domain.MessagePollAnswer(nil), media.Poll.Answers...)
	cloned.Poll = &poll
	polls.EnrichPoll(cloned.Poll, viewerUserID, now)
	return &cloned
}

func clonePollDefinition(def domain.PollDefinition) domain.PollDefinition {
	def.Options = cloneOptionList(def.Options)
	def.CorrectOptions = cloneOptionList(def.CorrectOptions)
	def.SolutionEntities = append([]domain.MessageEntity(nil), def.SolutionEntities...)
	return def
}

func clonePollVote(vote domain.PollVote) domain.PollVote {
	vote.Options = cloneOptionList(vote.Options)
	return vote
}

func cloneOptionList(options [][]byte) [][]byte {
	if options == nil {
		return nil
	}
	out := make([][]byte, 0, len(options))
	for _, option := range options {
		out = append(out, append([]byte(nil), option...))
	}
	return out
}

func voteHasOption(vote domain.PollVote, option []byte) bool {
	for _, candidate := range vote.Options {
		if string(candidate) == string(option) {
			return true
		}
	}
	return false
}
