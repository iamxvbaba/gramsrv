package rpc

import (
	"context"
	"encoding/hex"
	"strings"
	"unicode/utf8"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"go.uber.org/zap"

	"telesrv/internal/domain"
)

// pollOptionsHex encodes options into a log-readable form (one hex segment per option).
func pollOptionsHex(options [][]byte) []string {
	out := make([]string, 0, len(options))
	for _, option := range options {
		out = append(out, hex.EncodeToString(option))
	}
	return out
}

// onMessagesSendVote casts a poll vote (empty options = retract the vote). The
// response must inline updateMessagePoll: TDesktop relies on it to clear
// sendingVotes, otherwise the UI sticks at pending.
func (r *Router) onMessagesSendVote(ctx context.Context, req *tg.MessagesSendVoteRequest) (tg.UpdatesClass, error) {
	if req.MsgID <= 0 || req.MsgID > domain.MaxMessageBoxID {
		return nil, messageIDInvalidErr()
	}
	if err := validatePollOptions(req.Options, true); err != nil {
		return nil, err
	}
	userID, peer, err := r.reactionPeer(ctx, req.Peer, nil)
	if err != nil {
		return nil, err
	}
	// Two-sided integration diagnostics: log the raw options received (empty =
	// retract) and the processing outcome, to pin down "vote has no effect"-type
	// problems.
	r.log.Info("sendVote received",
		zap.Int64("user_id", userID),
		zap.String("peer_type", string(peer.Type)),
		zap.Int64("peer_id", peer.ID),
		zap.Int("msg_id", req.MsgID),
		zap.Int("option_count", len(req.Options)),
		zap.Strings("options_hex", pollOptionsHex(req.Options)),
	)
	now := int(r.clock.Now().Unix())
	switch peer.Type {
	case domain.PeerTypeChannel:
		if r.deps.Channels == nil {
			return nil, messageIDInvalidErr()
		}
		res, err := r.deps.Channels.VoteMessagePoll(ctx, userID, domain.VoteChannelMessagePollRequest{
			UserID:    userID,
			ChannelID: peer.ID,
			MessageID: req.MsgID,
			Options:   req.Options,
			Date:      now,
		})
		if err != nil {
			r.log.Warn("sendVote failed", zap.Error(err), zap.Int64("user_id", userID), zap.Int("msg_id", req.MsgID))
			return nil, pollMutationErr(err)
		}
		r.logPollOutcome("sendVote applied", userID, res.PollID, res.Message.Media)
		return r.channelPollUpdates(ctx, userID, peer, req.MsgID, res, true), nil
	case domain.PeerTypeUser:
		if r.deps.Messages == nil {
			return nil, messageIDInvalidErr()
		}
		res, err := r.deps.Messages.VoteMessagePoll(ctx, userID, domain.VotePrivateMessagePollRequest{
			UserID:    userID,
			Peer:      peer,
			MessageID: req.MsgID,
			Options:   req.Options,
			Date:      now,
		})
		if err != nil {
			r.log.Warn("sendVote failed", zap.Error(err), zap.Int64("user_id", userID), zap.Int("msg_id", req.MsgID))
			return nil, pollMutationErr(err)
		}
		for _, msg := range res.Messages {
			if msg.OwnerUserID == userID {
				r.logPollOutcome("sendVote applied", userID, res.PollID, msg.Media)
			}
		}
		return r.privatePollUpdates(ctx, userID, res), nil
	default:
		return nil, peerIDInvalidErr()
	}
}

// logPollOutcome summarizes the viewer-view poll state (chosen/total votes) and
// is read paired with "sendVote received".
func (r *Router) logPollOutcome(event string, viewerUserID, pollID int64, media *domain.MessageMedia) {
	if media == nil || media.Poll == nil || media.Poll.Results == nil {
		r.log.Warn(event+" but poll results missing", zap.Int64("user_id", viewerUserID), zap.Int64("poll_id", pollID))
		return
	}
	results := media.Poll.Results
	chosen := make([]string, 0, 1)
	for _, item := range results.Voters {
		if item.Chosen {
			chosen = append(chosen, hex.EncodeToString(item.Option))
		}
	}
	r.log.Info(event,
		zap.Int64("user_id", viewerUserID),
		zap.Int64("poll_id", pollID),
		zap.Bool("closed", media.Poll.Closed),
		zap.Int("total_voters", results.TotalVoters),
		zap.Bool("viewer_voted", results.ViewerVoted),
		zap.Strings("viewer_chosen_hex", chosen),
	)
}

// onMessagesGetPollResults returns the viewer-view latest poll state
// (updateMessagePoll with an inlined poll).
func (r *Router) onMessagesGetPollResults(ctx context.Context, req *tg.MessagesGetPollResultsRequest) (tg.UpdatesClass, error) {
	if req.MsgID <= 0 || req.MsgID > domain.MaxMessageBoxID {
		return nil, messageIDInvalidErr()
	}
	userID, peer, err := r.reactionPeer(ctx, req.Peer, nil)
	if err != nil {
		return nil, err
	}
	poll, _, found := r.loadMessagePoll(ctx, userID, peer, req.MsgID)
	if !found {
		return nil, messageIDInvalidErr()
	}
	r.logPollOutcome("getPollResults", userID, poll.ID, &domain.MessageMedia{Kind: domain.MessageMediaKindPoll, Poll: poll})
	update := tgUpdateMessagePoll(peer, req.MsgID, poll)
	if update == nil {
		return nil, messageIDInvalidErr()
	}
	users, chats := r.pollUpdateRefs(ctx, userID, peer, poll, domain.Channel{})
	return &tg.Updates{
		Updates: []tg.UpdateClass{update},
		Users:   users,
		Chats:   chats,
		Date:    int(r.clock.Now().Unix()),
	}, nil
}

// onMessagesGetPollVotes lists the voters of a public poll (paginated by vote_date DESC).
func (r *Router) onMessagesGetPollVotes(ctx context.Context, req *tg.MessagesGetPollVotesRequest) (*tg.MessagesVotesList, error) {
	if req.ID <= 0 || req.ID > domain.MaxMessageBoxID {
		return nil, messageIDInvalidErr()
	}
	if req.Limit < 0 || req.Limit > maxSearchResultsLimit {
		return nil, limitInvalidErr()
	}
	option, hasOption := req.GetOption()
	if hasOption {
		if err := validatePollOption(option); err != nil {
			return nil, err
		}
	}
	offset, _ := req.GetOffset()
	if len(offset) > maxPollVotesOffsetLength {
		return nil, limitInvalidErr()
	}
	offsetDate, offsetUserID, ok := decodePollVotesOffset(offset)
	if !ok {
		return nil, limitInvalidErr()
	}
	userID, peer, err := r.reactionPeer(ctx, req.Peer, nil)
	if err != nil {
		return nil, err
	}
	if peer.Type == domain.PeerTypeChannel && r.deps.Channels != nil {
		view, err := r.deps.Channels.GetChannel(ctx, userID, peer.ID)
		if err != nil {
			return nil, channelInvalidErr(err)
		}
		if view.Channel.Broadcast && !view.Channel.Megagroup {
			return nil, tgerr.New(403, "BROADCAST_FORBIDDEN")
		}
	}
	if r.deps.Polls == nil {
		return nil, messageIDInvalidErr()
	}
	poll, _, found := r.loadMessagePoll(ctx, userID, peer, req.ID)
	if !found {
		return nil, messageIDInvalidErr()
	}
	if !poll.PublicVoters {
		return nil, pollVoteRequiredErr()
	}
	limit := req.Limit
	if limit <= 0 || limit > domain.MaxPollVotesPageLimit {
		limit = domain.MaxPollVotesPageLimit
	}
	list, err := r.deps.Polls.ListPollVotes(ctx, domain.PollVotesListRequest{
		PollID:       poll.ID,
		Option:       option,
		OffsetDate:   offsetDate,
		OffsetUserID: offsetUserID,
		Limit:        limit,
	})
	if err != nil {
		return nil, pollMutationErr(err)
	}
	votes := make([]tg.MessagePeerVoteClass, 0, len(list.Votes))
	userIDs := make([]int64, 0, len(list.Votes))
	for _, vote := range list.Votes {
		userIDs = append(userIDs, vote.UserID)
		votes = append(votes, tgMessagePeerVote(vote, option, hasOption))
	}
	out := &tg.MessagesVotesList{
		Count: list.Count,
		Votes: votes,
		Chats: r.chatsForInputPeer(ctx, userID, req.Peer),
		Users: r.tgUsersForIDs(ctx, userID, userIDs),
	}
	if list.HasMore {
		out.SetNextOffset(pollVotesNextOffset(list.Votes))
	}
	return out, nil
}

// tgMessagePeerVote emits a single voter entry; when filtering by option the
// official server uses messagePeerVoteInputOption to save bytes.
func tgMessagePeerVote(vote domain.PollVote, option []byte, filtered bool) tg.MessagePeerVoteClass {
	peer := &tg.PeerUser{UserID: vote.UserID}
	if filtered {
		return &tg.MessagePeerVoteInputOption{Peer: peer, Date: vote.Date}
	}
	if len(vote.Options) == 1 {
		return &tg.MessagePeerVote{Peer: peer, Option: vote.Options[0], Date: vote.Date}
	}
	return &tg.MessagePeerVoteMultiple{Peer: peer, Options: vote.Options, Date: vote.Date}
}

func (r *Router) onMessagesAddPollAnswer(ctx context.Context, req *tg.MessagesAddPollAnswerRequest) (tg.UpdatesClass, error) {
	if req.MsgID <= 0 || req.MsgID > domain.MaxMessageBoxID {
		return nil, messageIDInvalidErr()
	}
	if err := validatePollAnswer(req.Answer); err != nil {
		return nil, err
	}
	if _, _, err := r.reactionPeer(ctx, req.Peer, nil); err != nil {
		return nil, err
	}
	return nil, messageIDInvalidErr()
}

func (r *Router) onMessagesDeletePollAnswer(ctx context.Context, req *tg.MessagesDeletePollAnswerRequest) (tg.UpdatesClass, error) {
	if req.MsgID <= 0 || req.MsgID > domain.MaxMessageBoxID {
		return nil, messageIDInvalidErr()
	}
	if err := validatePollOption(req.Option); err != nil {
		return nil, err
	}
	if _, _, err := r.reactionPeer(ctx, req.Peer, nil); err != nil {
		return nil, err
	}
	return nil, messageIDInvalidErr()
}

func (r *Router) onMessagesGetUnreadPollVotes(ctx context.Context, req *tg.MessagesGetUnreadPollVotesRequest) (tg.MessagesMessagesClass, error) {
	if err := validateHistoryBounds(req.OffsetID, req.AddOffset, req.Limit, req.MaxID, req.MinID); err != nil {
		return nil, err
	}
	userID, _, err := r.reactionPeer(ctx, req.Peer, nil)
	if err != nil {
		return nil, err
	}
	if topMsgID, ok := req.GetTopMsgID(); ok && (topMsgID < 0 || topMsgID > domain.MaxMessageBoxID) {
		return nil, messageIDInvalidErr()
	}
	result := &tg.MessagesMessages{
		Messages: []tg.MessageClass{},
		Topics:   []tg.ForumTopicClass{},
		Chats:    r.chatsForInputPeer(ctx, userID, req.Peer),
		Users:    []tg.UserClass{},
	}
	r.applyPeerReadModelsToMessages(ctx, userID, result)
	return result, nil
}

func (r *Router) onMessagesReadPollVotes(ctx context.Context, req *tg.MessagesReadPollVotesRequest) (*tg.MessagesAffectedHistory, error) {
	authKeyID, _ := AuthKeyIDFrom(ctx)
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	if _, err := r.checkedDomainPeerFromInputPeer(ctx, userID, req.Peer); err != nil {
		return nil, err
	}
	if topMsgID, ok := req.GetTopMsgID(); ok && (topMsgID < 0 || topMsgID > domain.MaxMessageBoxID) {
		return nil, messageIDInvalidErr()
	}
	return r.affectedHistory(ctx, authKeyID, userID, 0)
}

func validatePollOptions(options [][]byte, allowEmpty bool) error {
	if len(options) == 0 {
		if allowEmpty {
			return nil
		}
		return optionInvalidErr()
	}
	if len(options) > maxPollVoteOptions {
		return optionsTooMuchErr()
	}
	for _, option := range options {
		if err := validatePollOption(option); err != nil {
			return err
		}
	}
	return nil
}

func validatePollOption(option []byte) error {
	if len(option) == 0 || len(option) > maxPollOptionBytes {
		return optionInvalidErr()
	}
	return nil
}

func validatePollAnswer(answer tg.PollAnswerClass) error {
	if answer == nil {
		return pollAnswerInvalidErr()
	}
	text := answer.GetText()
	if strings.TrimSpace(text.Text) == "" || utf8.RuneCountInString(text.Text) > maxTodoTitleLength {
		return pollAnswerInvalidErr()
	}
	if len(text.Entities) > maxMessageEntityCount {
		return limitInvalidErr()
	}
	switch typed := answer.(type) {
	case *tg.PollAnswer:
		if err := validatePollOption(typed.Option); err != nil {
			return err
		}
		if typed.Media != nil {
			return mediaInvalidErr()
		}
	case *tg.InputPollAnswer:
		if typed.Media != nil {
			return mediaInvalidErr()
		}
	default:
		return inputConstructorInvalidErr()
	}
	return nil
}
