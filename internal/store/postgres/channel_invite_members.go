package postgres

import (
	"context"
	"errors"
	"fmt"
	"telesrv/internal/domain"
)

func (s *ChannelStore) InviteToChannel(ctx context.Context, channelID, inviterUserID int64, userIDs []int64, date int) (domain.CreateChannelResult, error) {
	if channelID == 0 || inviterUserID == 0 || len(userIDs) == 0 {
		return domain.CreateChannelResult{}, domain.ErrChannelInvalid
	}
	beginner, ok := s.db.(txBeginner)
	if !ok {
		return domain.CreateChannelResult{}, fmt.Errorf("invite channel: db does not support transactions")
	}
	tx, err := beginner.Begin(ctx)
	if err != nil {
		return domain.CreateChannelResult{}, fmt.Errorf("begin invite channel: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()
	channel, inviter, err := s.getChannelForMember(ctx, tx, inviterUserID, channelID)
	if err != nil {
		return domain.CreateChannelResult{}, err
	}
	if channel.Monoforum {
		return domain.CreateChannelResult{}, domain.ErrChannelMonoforumUnsupported
	}
	if !canInviteToChannel(channel, inviter) {
		return domain.CreateChannelResult{}, domain.ErrChannelAdminRequired
	}
	if date == 0 {
		date = nowUnix()
	}
	requested := uniqueChannelUserIDs(userIDs, 0)
	inviteOne := len(requested) == 1
	canRestoreKicked := canBanChannelUsers(inviter)
	invitedIDs := make([]int64, 0, len(requested))
	members := make([]domain.ChannelMember, 0, len(requested))
	restoredKicked := 0
	for _, userID := range requested {
		if existing, err := s.getChannelMember(ctx, tx, channelID, userID); err == nil {
			if existing.Status == domain.ChannelMemberActive {
				if inviteOne {
					return domain.CreateChannelResult{}, domain.ErrUserAlreadyParticipant
				}
				continue
			}
			if existing.Status == domain.ChannelMemberBanned || existing.Status == domain.ChannelMemberKicked || existing.BannedRights.ViewMessages {
				if !canRestoreKicked {
					if inviteOne {
						return domain.CreateChannelResult{}, domain.ErrUserKicked
					}
					continue
				}
				if existing.Status == domain.ChannelMemberKicked {
					restoredKicked++
				}
			}
		} else if !errors.Is(err, domain.ErrChannelPrivate) {
			return domain.CreateChannelResult{}, err
		}
		member := domain.ChannelMember{
			ChannelID:       channelID,
			UserID:          userID,
			InviterUserID:   inviterUserID,
			Role:            domain.ChannelRoleMember,
			Status:          domain.ChannelMemberActive,
			JoinedAt:        date,
			AvailableMinID:  channelInitialAvailableMinID(channel),
			AvailableMinPts: channelInitialAvailableMinPts(channel),
			ReadInboxMaxID:  channel.TopMessageID,
		}
		if err := upsertChannelMemberTx(ctx, tx, channel, member); err != nil {
			return domain.CreateChannelResult{}, err
		}
		if err := s.insertChannelAdminLogTx(ctx, tx, domain.ChannelAdminLogEvent{
			ChannelID:   channelID,
			UserID:      inviterUserID,
			Date:        date,
			Type:        domain.ChannelAdminLogParticipantInvite,
			Participant: &member,
		}); err != nil {
			return domain.CreateChannelResult{}, err
		}
		members = append(members, member)
		invitedIDs = append(invitedIDs, userID)
	}
	if len(members) > 0 {
		if _, err := tx.Exec(ctx, `UPDATE channels SET participants_count = participants_count + $2, kicked_count = GREATEST(kicked_count - $3, 0), updated_at = now() WHERE id = $1`, channelID, len(members), restoredKicked); err != nil {
			return domain.CreateChannelResult{}, fmt.Errorf("update channel participants: %w", err)
		}
		channel.ParticipantsCount += len(members)
		channel.KickedCount = maxInt(channel.KickedCount-restoredKicked, 0)
	}
	var msg domain.ChannelMessage
	var event domain.ChannelUpdateEvent
	if len(members) > 0 && channel.Megagroup {
		msg, event, err = s.insertServiceMessage(ctx, tx, channel, inviterUserID, date, domain.ChannelMessageAction{
			Type:    domain.ChannelActionChatAddUser,
			UserIDs: invitedIDs,
		})
		if err != nil {
			return domain.CreateChannelResult{}, err
		}
		channel.TopMessageID = msg.ID
		channel.Pts = event.Pts
	}
	for _, member := range members {
		if err := upsertChannelDialogTx(ctx, tx, member.UserID, channel, msg, member.ReadInboxMaxID, member.ReadOutboxMaxID); err != nil {
			return domain.CreateChannelResult{}, err
		}
		// 被重新拉入群也是重进:按新 available_min_id 重算未读 reaction 计数清幽灵角标。
		if err := refreshChannelUnreadReactionsCountTx(ctx, tx, member.UserID, channel.ID); err != nil {
			return domain.CreateChannelResult{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.CreateChannelResult{}, fmt.Errorf("commit invite channel: %w", err)
	}
	committed = true
	recipients, _ := s.ListActiveChannelMemberIDs(ctx, inviterUserID, channelID, 0)
	return domain.CreateChannelResult{Channel: channel, Members: members, Message: msg, Event: event, Recipients: recipients}, nil
}

func canInviteToChannel(channel domain.Channel, member domain.ChannelMember) bool {
	if member.Role == domain.ChannelRoleCreator ||
		(member.Role == domain.ChannelRoleAdmin && (member.AdminRights.InviteUsers || member.AdminRights.ChangeInfo)) {
		return true
	}
	return channel.Megagroup && !channel.DefaultBannedRights.InviteUsers && !member.BannedRights.InviteUsers
}
