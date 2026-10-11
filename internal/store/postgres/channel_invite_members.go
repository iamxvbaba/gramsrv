package postgres

import (
	"context"
	"fmt"
	"sort"
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
	sort.Slice(requested, func(i, j int) bool { return requested[i] < requested[j] })
	inviteOne := len(requested) == 1
	canRestoreKicked := canBanChannelUsers(inviter)
	invitedIDs := make([]int64, 0, len(requested))
	members := make([]domain.ChannelMember, 0, len(requested))
	restoredKicked := 0
	existingMembers, err := channelMembersForUpdateBatchTx(ctx, tx, channelID, requested)
	if err != nil {
		return domain.CreateChannelResult{}, err
	}
	for _, userID := range requested {
		if existing, ok := existingMembers[userID]; ok {
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
		members = append(members, member)
		invitedIDs = append(invitedIDs, userID)
	}
	if len(members) > 0 {
		if err := enableChannelMembershipBatchTx(ctx, tx); err != nil {
			return domain.CreateChannelResult{}, err
		}
		if err := upsertChannelMembersBatchTx(ctx, tx, channel, members); err != nil {
			return domain.CreateChannelResult{}, err
		}
		if err := insertChannelInviteAdminLogsBatchTx(ctx, tx, channelID, inviterUserID, date, members); err != nil {
			return domain.CreateChannelResult{}, err
		}
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
	if err := upsertChannelDialogsBatchTx(ctx, tx, channel, msg, members); err != nil {
		return domain.CreateChannelResult{}, err
	}
	// 被重新拉入群也是重进:按新 available_min_id 集合重算未读 reaction 计数清幽灵角标。
	if err := refreshChannelUnreadReactionsCountsBatchTx(ctx, tx, channel.ID, invitedIDs); err != nil {
		return domain.CreateChannelResult{}, err
	}
	if err := enqueueWelcomeMessageDeliveriesTx(ctx, tx, channel.ID, members); err != nil {
		return domain.CreateChannelResult{}, err
	}
	if err := bumpChannelMembershipReadModelsBatchTx(ctx, tx, channel.ID, invitedIDs); err != nil {
		return domain.CreateChannelResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.CreateChannelResult{}, fmt.Errorf("commit invite channel: %w", err)
	}
	committed = true
	return domain.CreateChannelResult{Channel: channel, Members: members, Message: msg, Event: event}, nil
}

func canInviteToChannel(channel domain.Channel, member domain.ChannelMember) bool {
	if member.Role == domain.ChannelRoleCreator ||
		(member.Role == domain.ChannelRoleAdmin && (member.AdminRights.InviteUsers || member.AdminRights.ChangeInfo)) {
		return true
	}
	return channel.Megagroup && !channel.DefaultBannedRights.InviteUsers && !member.BannedRights.InviteUsers
}
