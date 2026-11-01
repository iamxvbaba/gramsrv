package memory

import (
	"context"
	"time"

	"telesrv/internal/domain"
)

// Channel/supergroup message poll voting and closing: membership and message visibility
// reuse the same checks as reactions, while poll-level semantics are delegated to the
// shared PollStore.

func (s *ChannelStore) VoteChannelMessagePoll(_ context.Context, req domain.VoteChannelMessagePollRequest) (domain.ChannelMessagePollResult, error) {
	if req.Date == 0 {
		req.Date = int(time.Now().Unix())
	}
	channel, msg, err := s.pollChannelMessageTarget(req.UserID, req.ChannelID, req.MessageID)
	if err != nil {
		return domain.ChannelMessagePollResult{}, err
	}
	if err := s.polls.Vote(msg.Media.Poll.ID, req.UserID, req.Options, req.Date); err != nil {
		return domain.ChannelMessagePollResult{}, err
	}
	return s.channelPollResult(channel, msg, req.UserID, req.Date), nil
}

func (s *ChannelStore) CloseChannelMessagePoll(_ context.Context, req domain.CloseChannelMessagePollRequest) (domain.ChannelMessagePollResult, error) {
	if req.Date == 0 {
		req.Date = int(time.Now().Unix())
	}
	channel, msg, err := s.pollChannelMessageTarget(req.UserID, req.ChannelID, req.MessageID)
	if err != nil {
		return domain.ChannelMessagePollResult{}, err
	}
	if err := s.polls.Close(msg.Media.Poll.ID, req.UserID); err != nil {
		return domain.ChannelMessagePollResult{}, err
	}
	return s.channelPollResult(channel, msg, req.UserID, req.Date), nil
}

func (s *ChannelStore) pollChannelMessageTarget(userID, channelID int64, messageID int) (domain.Channel, domain.ChannelMessage, error) {
	if s == nil || s.polls == nil {
		return domain.Channel{}, domain.ChannelMessage{}, domain.ErrMessageIDInvalid
	}
	if userID == 0 || channelID == 0 || messageID <= 0 || messageID > domain.MaxMessageBoxID {
		return domain.Channel{}, domain.ChannelMessage{}, domain.ErrChannelInvalid
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	channel, member, err := s.channelAndMemberLocked(userID, channelID)
	if err != nil {
		return domain.Channel{}, domain.ChannelMessage{}, err
	}
	msg, ok := s.findMessageLocked(channelID, messageID)
	if !ok || msg.Deleted || msg.Action != nil || msg.ID <= member.AvailableMinID {
		return domain.Channel{}, domain.ChannelMessage{}, domain.ErrMessageIDInvalid
	}
	if msg.Media == nil || msg.Media.Kind != domain.MessageMediaKindPoll || msg.Media.Poll == nil || msg.Media.Poll.ID == 0 {
		return domain.Channel{}, domain.ChannelMessage{}, domain.ErrMessageIDInvalid
	}
	return cloneChannel(channel), cloneChannelMessage(msg), nil
}

// channelPollResult assembles only the current RPC caller's view; reliable delivery is done by the channel delivery lane.
func (s *ChannelStore) channelPollResult(channel domain.Channel, msg domain.ChannelMessage, viewerUserID int64, now int) domain.ChannelMessagePollResult {
	msg.Media = enrichPollMediaForViewer(s.polls, msg.Media, viewerUserID, now)
	return domain.ChannelMessagePollResult{
		PollID:  msg.Media.Poll.ID,
		Channel: channel,
		Message: msg,
	}
}
