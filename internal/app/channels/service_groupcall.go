package channels

import (
	"context"

	"telesrv/internal/domain"
)

// SetActiveCall sets/clears the active group call association on the channel row (groupcalls module only).
func (s *Service) SetActiveCall(ctx context.Context, channelID, callID, callAccessHash int64, notEmpty bool) (domain.Channel, error) {
	if s == nil || s.channels == nil || channelID == 0 {
		return domain.Channel{}, domain.ErrChannelInvalid
	}
	return s.channels.SetActiveCall(ctx, channelID, callID, callAccessHash, notEmpty)
}

// AppendCallServiceMessage generates a group call service message (started/ended/invite, with the channel pts).
func (s *Service) AppendCallServiceMessage(ctx context.Context, channelID, senderUserID int64, date int, action domain.ChannelMessageAction) (domain.SendChannelMessageResult, error) {
	if s == nil || s.channels == nil {
		return domain.SendChannelMessageResult{}, domain.ErrChannelInvalid
	}
	if err := s.ensureCanSend(ctx, senderUserID); err != nil {
		return domain.SendChannelMessageResult{}, err
	}
	return s.channels.AppendCallServiceMessage(ctx, channelID, senderUserID, date, action)
}

// AppendStarGiftAdminLog records a Recent Actions snapshot of a channel Star gift; it is
// not a channel history message, so it produces no channel pts /
// updateNewChannelMessage / subscriber fanout.
func (s *Service) AppendStarGiftAdminLog(ctx context.Context, channelID, senderUserID int64, savedID int64, date int, action domain.ChannelMessageAction) error {
	if s == nil || s.channels == nil {
		return domain.ErrChannelInvalid
	}
	if err := s.ensureCanSend(ctx, senderUserID); err != nil {
		return err
	}
	return s.channels.AppendStarGiftAdminLog(ctx, channelID, senderUserID, savedID, date, action)
}
