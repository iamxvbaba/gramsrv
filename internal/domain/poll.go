package domain

import (
	"bytes"
	"errors"
)

// This file defines the poll business objects and their shared semantics:
//
//   - MessagePoll is the render-safe definition snapshot persisted alongside the message media JSONB — it carries no
//     server secret such as the quiz correct answers or solution; Closed is only a send-time snapshot, and the read path must overwrite it with the authoritative polls state.
//   - PollDefinition is a polls authoritative table row (containing the secrets and the mutable closed), which is the only thing vote validation and result gating trust.
//   - Vote validation (ValidatePollVote) and result gating (ResolvePollResults) are pure functions shared by the memory and
//     postgres stores, ruling out behavioral drift between the two implementations.
//
// Forwarding does not copy the poll: the forward snapshot carries the same poll id, and votes/closes aggregate globally (matching the official behavior).

const (
	// MaxPollQuestionLength matches the official poll question limit.
	MaxPollQuestionLength = 255
	// MinPollAnswers / MaxPollAnswers are the answer count bounds (official 2..10).
	MinPollAnswers = 2
	MaxPollAnswers = 10
	// MaxPollAnswerTextLength is the single answer text limit (official 100).
	MaxPollAnswerTextLength = 100
	// MaxPollSolutionLength is the quiz solution text limit (official 200).
	MaxPollSolutionLength = 200
	// MinPollClosePeriod / MaxPollClosePeriod are the auto-close countdown bounds (official 5..600 seconds).
	MinPollClosePeriod = 5
	MaxPollClosePeriod = 600
	// MaxPollRecentVoters is the recent_voters truncation (same as reaction recent, official 3).
	MaxPollRecentVoters = 3
	// MaxPollVotesPageLimit is the per-page limit of messages.getPollVotes.
	MaxPollVotesPageLimit = 50
)

// Poll-path business errors; the rpc layer maps them to the corresponding RPC error text.
var (
	ErrPollInvalid          = errors.New("poll invalid")
	ErrPollNotFound         = errors.New("poll not found")
	ErrPollClosed           = errors.New("poll closed")
	ErrPollOptionInvalid    = errors.New("poll option invalid")
	ErrPollRevoteNotAllowed = errors.New("poll revote not allowed")
	ErrPollNotCreator       = errors.New("poll can only be closed by creator")
	ErrPollNotPublic        = errors.New("poll voters are not public")
)

// MessagePollAnswer is one candidate answer (text + option byte key + optional attached media).
// Media only allows photo/document snapshots (the rpc layer restricts the input types).
type MessagePollAnswer struct {
	Text     string          `json:"text"`
	Entities []MessageEntity `json:"entities,omitempty"`
	Option   []byte          `json:"option"`
	Media    *MessageMedia   `json:"media,omitempty"`
}

// MessagePoll is the poll definition snapshot on a message's media (render-safe, no secrets).
type MessagePoll struct {
	ID               int64               `json:"id"`
	Question         string              `json:"question"`
	QuestionEntities []MessageEntity     `json:"question_entities,omitempty"`
	Answers          []MessagePollAnswer `json:"answers"`
	// Closed is a send-time snapshot; the read path overwrites it with the authoritative polls state (forward copies/history consistency after closing rely on it).
	Closed                bool `json:"closed,omitempty"`
	PublicVoters          bool `json:"public_voters,omitempty"`
	MultipleChoice        bool `json:"multiple_choice,omitempty"`
	Quiz                  bool `json:"quiz,omitempty"`
	RevotingDisabled      bool `json:"revoting_disabled,omitempty"`
	ShuffleAnswers        bool `json:"shuffle_answers,omitempty"`
	HideResultsUntilClose bool `json:"hide_results_until_close,omitempty"`
	ClosePeriod           int  `json:"close_period,omitempty"`
	CloseDate             int  `json:"close_date,omitempty"`

	// AttachedMedia is the poll question's attached media (messageMediaPoll.attached_media), a photo/document snapshot.
	AttachedMedia *MessageMedia `json:"attached_media,omitempty"`

	// Results is filled per viewer by the read path (with chosen/correct/solution gating) and is not persisted.
	Results *MessagePollResults `json:"-"`
}

// MessagePollAnswerVoters is the aggregate result of one answer (already gated per viewer).
type MessagePollAnswerVoters struct {
	Option  []byte
	Voters  int
	Chosen  bool
	Correct bool
}

// MessagePollResults is the aggregate result resolved per viewer.
type MessagePollResults struct {
	TotalVoters      int
	Voters           []MessagePollAnswerVoters // same order as MessagePoll.Answers
	RecentVoters     []int64                   // filled only for public_voters, truncated to MaxPollRecentVoters
	ViewerVoted      bool
	Solution         string // sent only for a quiz and (viewer voted || closed)
	SolutionEntities []MessageEntity
}

// PollDefinition is a polls authoritative table row: mutable state (closed) + server secrets + the option set needed for validation.
type PollDefinition struct {
	ID                    int64
	CreatorUserID         int64
	Options               [][]byte // valid option set (in answer order)
	PublicVoters          bool
	MultipleChoice        bool
	Quiz                  bool
	RevotingDisabled      bool
	HideResultsUntilClose bool
	Closed                bool
	ClosePeriod           int
	CloseDate             int
	CorrectOptions        [][]byte
	Solution              string
	SolutionEntities      []MessageEntity
}

// ClosedAt returns whether the poll should be considered closed at time now (explicitly closed or close_date passed).
func (d PollDefinition) ClosedAt(now int) bool {
	return d.Closed || (d.CloseDate > 0 && now >= d.CloseDate)
}

// HasOption reports whether option is in the valid option set.
func (d PollDefinition) HasOption(option []byte) bool {
	for _, candidate := range d.Options {
		if bytes.Equal(candidate, option) {
			return true
		}
	}
	return false
}

// PollVote is one user's vote row. Options keeps the client-submitted order.
type PollVote struct {
	PollID  int64
	UserID  int64
	Options [][]byte
	Date    int
}

// PollAggregates is the raw aggregate produced by the store layer, handed to ResolvePollResults for viewer gating.
// The Counts key is string(option).
type PollAggregates struct {
	Counts        map[string]int
	TotalVoters   int
	RecentVoters  []int64  // truncated to MaxPollRecentVoters by vote_date DESC; needed only for public_voters
	ViewerOptions [][]byte // the viewer's own vote (nil = not voted)
}

// ValidatePollVote validates one vote submission; existing is the viewer's current vote (nil=not voted) and an empty options means vote retraction.
// The memory and postgres stores must share this function to keep the two implementations consistent.
func ValidatePollVote(def PollDefinition, existing [][]byte, options [][]byte, now int) error {
	if def.ID == 0 {
		return ErrPollNotFound
	}
	if def.ClosedAt(now) {
		return ErrPollClosed
	}
	if def.Quiz || def.RevotingDisabled {
		// quiz / revoting_disabled: no retracting, no changing a vote.
		if len(existing) > 0 || len(options) == 0 {
			return ErrPollRevoteNotAllowed
		}
	}
	if def.Quiz {
		if len(options) != 1 {
			return ErrPollOptionInvalid
		}
	} else if len(options) > 1 && !def.MultipleChoice {
		return ErrPollOptionInvalid
	}
	if len(options) > len(def.Options) {
		return ErrPollOptionInvalid
	}
	seen := make(map[string]struct{}, len(options))
	for _, option := range options {
		if !def.HasOption(option) {
			return ErrPollOptionInvalid
		}
		key := string(option)
		if _, dup := seen[key]; dup {
			return ErrPollOptionInvalid
		}
		seen[key] = struct{}{}
	}
	return nil
}

// ResolvePollResults resolves the raw aggregate into a viewer-perspective result: chosen marks the viewer's own options;
// correct/solution are revealed only for a quiz and (viewer voted || closed);
// hide_results_until_close hides counts from non-creators before closing (to stop a malicious client from bypassing the UI).
func ResolvePollResults(def PollDefinition, agg PollAggregates, viewerUserID int64, now int) MessagePollResults {
	closed := def.ClosedAt(now)
	viewerVoted := len(agg.ViewerOptions) > 0
	reveal := def.Quiz && (viewerVoted || closed)
	hideCounts := def.HideResultsUntilClose && !closed && viewerUserID != def.CreatorUserID
	chosen := make(map[string]struct{}, len(agg.ViewerOptions))
	for _, option := range agg.ViewerOptions {
		chosen[string(option)] = struct{}{}
	}
	correct := make(map[string]struct{}, len(def.CorrectOptions))
	if reveal {
		for _, option := range def.CorrectOptions {
			correct[string(option)] = struct{}{}
		}
	}
	out := MessagePollResults{
		TotalVoters: agg.TotalVoters,
		Voters:      make([]MessagePollAnswerVoters, 0, len(def.Options)),
		ViewerVoted: viewerVoted,
	}
	for _, option := range def.Options {
		key := string(option)
		_, isChosen := chosen[key]
		_, isCorrect := correct[key]
		voters := agg.Counts[key]
		if hideCounts {
			voters = 0
		}
		out.Voters = append(out.Voters, MessagePollAnswerVoters{
			Option:  option,
			Voters:  voters,
			Chosen:  isChosen,
			Correct: isCorrect,
		})
	}
	if def.PublicVoters && !hideCounts && len(agg.RecentVoters) > 0 {
		recent := agg.RecentVoters
		if len(recent) > MaxPollRecentVoters {
			recent = recent[:MaxPollRecentVoters]
		}
		out.RecentVoters = append([]int64(nil), recent...)
	}
	if reveal {
		out.Solution = def.Solution
		out.SolutionEntities = append([]MessageEntity(nil), def.SolutionEntities...)
	}
	return out
}

// ApplyPollState writes the authoritative state and the resolved results back to the definition snapshot on the media (the end of read-path enrichment).
func ApplyPollState(poll *MessagePoll, def PollDefinition, results MessagePollResults, now int) {
	if poll == nil {
		return
	}
	poll.Closed = def.ClosedAt(now)
	poll.Results = &results
}

// VotePrivateMessagePollRequest is a private-message vote request (msg id is the viewer box id).
type VotePrivateMessagePollRequest struct {
	UserID    int64
	Peer      Peer
	MessageID int
	Options   [][]byte // empty = retract the vote
	Date      int
}

// PrivateMessagePollResult returns the message from both owners' perspectives (media already enriched per owner).
type PrivateMessagePollResult struct {
	PollID   int64
	Messages []Message
}

// ClosePrivateMessagePollRequest closes a poll on a private message (poll creator only).
type ClosePrivateMessagePollRequest struct {
	UserID    int64
	Peer      Peer
	MessageID int
	Date      int
}

// VoteChannelMessagePollRequest is a channel/supergroup message vote request.
type VoteChannelMessagePollRequest struct {
	UserID    int64
	ChannelID int64
	MessageID int
	Options   [][]byte
	Date      int
}

// ChannelMessagePollResult returns the voter-perspective message and the suggested push recipients.
type ChannelMessagePollResult struct {
	PollID     int64
	Channel    Channel
	Message    ChannelMessage
	Recipients []int64
}

// ChannelPollFanoutViews is the batched per-viewer enrich result of channel poll fan-out (eliminating the per-viewer GetMessages
// N+1): viewer-invariant aggregates (counts/total/recent) are computed once + batched viewerOptions + batched member visibility,
// and each viewer is composed with the same ResolvePollResults, byte-identical to the per-viewer path.
//   - Found: the poll message exists and is indeed a poll (otherwise treat as nothing to project).
//   - Message: the base message snapshot (not per-viewer enriched), for the app layer to layer bot history-visibility filtering on top.
//   - Polls: a present key = the viewer was evaluated; a nil value = the viewer cannot see it (non-member/pre-history hidden);
//     non-nil = the poll already enriched from that viewer's perspective. Bot history filtering is layered on top by the app layer (setting nil).
type ChannelPollFanoutViews struct {
	Found   bool
	Message ChannelMessage
	Polls   map[int64]*MessagePoll
}

// CloseChannelMessagePollRequest closes a poll on a channel message (poll creator only).
type CloseChannelMessagePollRequest struct {
	UserID    int64
	ChannelID int64
	MessageID int
	Date      int
}

// PollVotesListRequest is the paginated request of messages.getPollVotes.
type PollVotesListRequest struct {
	PollID       int64
	Option       []byte // optional: list only those who chose this option
	OffsetDate   int    // 0 = first page; paging uses the last row (date,user_id) of the previous page
	OffsetUserID int64
	Limit        int
}

// PollVotesList is a paginated voter result.
type PollVotesList struct {
	Count int // total count satisfying the filter
	Votes []PollVote
	// When HasMore is true, the rpc layer encodes next_offset from the last row (Date,UserID).
	HasMore bool
}
