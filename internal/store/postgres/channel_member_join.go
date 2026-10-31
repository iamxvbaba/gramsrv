package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"telesrv/internal/domain"
	"telesrv/internal/store/postgres/sqlcgen"
)

func (s *ChannelStore) JoinChannel(ctx context.Context, channelID, userID int64, date int) (domain.CreateChannelResult, error) {
	if channelID == 0 || userID == 0 {
		return domain.CreateChannelResult{}, domain.ErrChannelInvalid
	}
	beginner, ok := s.db.(txBeginner)
	if !ok {
		return domain.CreateChannelResult{}, fmt.Errorf("join channel: db does not support transactions")
	}
	tx, err := beginner.Begin(ctx)
	if err != nil {
		return domain.CreateChannelResult{}, fmt.Errorf("begin join channel: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()
	channel, err := getChannelByID(ctx, tx, channelID)
	if err != nil {
		return domain.CreateChannelResult{}, err
	}
	if channel.Monoforum {
		return domain.CreateChannelResult{}, domain.ErrChannelMonoforumUnsupported
	}
	existing, existingErr := s.getChannelMember(ctx, tx, channelID, userID)
	if existingErr == nil {
		switch {
		case existing.Status == domain.ChannelMemberActive:
			return domain.CreateChannelResult{}, domain.ErrUserAlreadyParticipant
		case existing.Status == domain.ChannelMemberBanned || existing.Status == domain.ChannelMemberKicked || existing.BannedRights.ViewMessages:
			return domain.CreateChannelResult{}, domain.ErrChannelUserBanned
		}
	}
	if date == 0 {
		date = nowUnix()
	}
	if channel.JoinRequest {
		if err := s.recordPublicJoinRequestTx(ctx, tx, channel, userID, date); err != nil {
			return domain.CreateChannelResult{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return domain.CreateChannelResult{}, fmt.Errorf("commit public channel join request: %w", err)
		}
		committed = true
		return domain.CreateChannelResult{Channel: channel}, domain.ErrInviteRequestSent
	}
	preJoinTopID := channel.TopMessageID
	minID := channelInitialAvailableMinID(channel)
	// Self-join: inviter is the user themself (matching the official channelParticipantSelf.inviter_id == user_id). The client uses this
	// (TDesktop requestSelf→getParticipant→channel->inviter) to generate a local "You joined this channel" service message,
	// which then adds the broadcast channel to the chat list (broadcast joins have no server service message; this local message materializes the dialog).
	member := domain.ChannelMember{ChannelID: channelID, UserID: userID, InviterUserID: userID, Role: domain.ChannelRoleMember, Status: domain.ChannelMemberActive, JoinedAt: date, AvailableMinID: minID, AvailableMinPts: channelInitialAvailableMinPts(channel), ReadInboxMaxID: maxInt(minID, preJoinTopID)}
	if existingErr == nil {
		// Re-entry is a brand-new participant, but some restriction lists are independent of membership and remain in effect after re-entry;
		// the creator identity is kept only when the channel's current owner is still this account.
		member.BannedRights = existing.BannedRights
		if existing.Role == domain.ChannelRoleCreator && channel.CreatorUserID == userID {
			member.Role = domain.ChannelRoleCreator
			member.AdminRights = existing.AdminRights
			member.Rank = existing.Rank
		}
	}
	if err := upsertChannelMemberTx(ctx, tx, channel, member); err != nil {
		return domain.CreateChannelResult{}, err
	}
	// On re-join (including the first time), return the purely prehistory-sourced read boundary: the upsert is GREATEST, so without an explicit
	// lowering, a member who "joined while history was hidden" would never see old messages even after the channel later disables hidden history.
	if err := resetPrehistoryBoundaryTx(ctx, tx, channel, userID); err != nil {
		return domain.CreateChannelResult{}, err
	}
	if err := s.insertChannelAdminLogTx(ctx, tx, domain.ChannelAdminLogEvent{
		ChannelID: channelID,
		UserID:    userID,
		Date:      date,
		Type:      domain.ChannelAdminLogParticipantJoin,
	}); err != nil {
		return domain.CreateChannelResult{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE channels SET participants_count = participants_count + 1, updated_at = now() WHERE id = $1`, channelID); err != nil {
		return domain.CreateChannelResult{}, fmt.Errorf("update channel participants: %w", err)
	}
	channel.ParticipantsCount++
	var msg domain.ChannelMessage
	var event domain.ChannelUpdateEvent
	if channel.Megagroup {
		msg, event, err = s.insertServiceMessage(ctx, tx, channel, userID, date, domain.ChannelMessageAction{
			Type:    domain.ChannelActionChatJoined,
			UserIDs: []int64{userID},
		})
		if err != nil {
			return domain.CreateChannelResult{}, err
		}
		channel.TopMessageID = msg.ID
		channel.Pts = event.Pts
	}
	member.ReadInboxMaxID = maxInt(member.ReadInboxMaxID, channel.TopMessageID)
	if msg.ID != 0 && msg.SenderUserID == userID {
		member.ReadOutboxMaxID = maxInt(member.ReadOutboxMaxID, msg.ID)
	}
	if _, err := tx.Exec(ctx, `
UPDATE channel_members
SET read_inbox_max_id = GREATEST(read_inbox_max_id, $3),
    read_outbox_max_id = GREATEST(read_outbox_max_id, $4),
    unread_mark = false,
    updated_at = now()
WHERE channel_id = $1 AND user_id = $2`, channelID, userID, member.ReadInboxMaxID, member.ReadOutboxMaxID); err != nil {
		return domain.CreateChannelResult{}, fmt.Errorf("update joined channel read watermarks: %w", err)
	}
	if err := upsertChannelDialogTx(ctx, tx, userID, channel, msg, member.ReadInboxMaxID, member.ReadOutboxMaxID); err != nil {
		return domain.CreateChannelResult{}, err
	}
	// Recompute the unread reaction count by the new available_min_id: re-entry is a brand-new participant, and the old
	// channel_dialogs.unread_reactions_count is a stored column never recomputed on the join path, so on
	// PreHistoryHidden/broadcast re-entry the available_min_id moves past the old reactions and the stale count lingers
	// as a ghost badge. refreshChannelUnreadReactionsCountTx gates on cm.id>available_min_id, computing 0
	// for PreHistoryHidden/broadcast (clearing the ghost) and keeping the real count for prehistory-visible (min_id still 0).
	if err := refreshChannelUnreadReactionsCountTx(ctx, tx, userID, channelID); err != nil {
		return domain.CreateChannelResult{}, err
	}
	if err := enqueueWelcomeMessageDeliveriesTx(ctx, tx, channelID, []domain.ChannelMember{member}); err != nil {
		return domain.CreateChannelResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.CreateChannelResult{}, fmt.Errorf("commit join channel: %w", err)
	}
	committed = true
	recipients, _ := s.ListActiveChannelMemberIDs(ctx, userID, channelID, 0)
	return domain.CreateChannelResult{Channel: channel, Members: []domain.ChannelMember{member}, Message: msg, Event: event, Recipients: recipients}, nil
}

func (s *ChannelStore) FutureCreatorAfterLeave(ctx context.Context, channelID, userID int64) (domain.ChannelMember, error) {
	if channelID == 0 || userID == 0 {
		return domain.ChannelMember{}, domain.ErrChannelInvalid
	}
	channel, member, err := s.getChannelForMember(ctx, s.db, userID, channelID)
	if err != nil {
		return domain.ChannelMember{}, err
	}
	if channel.CreatorUserID != userID || member.Role != domain.ChannelRoleCreator {
		return domain.ChannelMember{}, domain.ErrChannelAdminRequired
	}
	return s.futureCreatorAfterLeave(ctx, s.db, channelID, userID)
}

func (s *ChannelStore) LeaveChannel(ctx context.Context, channelID, userID int64, date int) (domain.CreateChannelResult, error) {
	if channelID == 0 || userID == 0 {
		return domain.CreateChannelResult{}, domain.ErrChannelInvalid
	}
	beginner, ok := s.db.(txBeginner)
	if !ok {
		return domain.CreateChannelResult{}, fmt.Errorf("leave channel: db does not support transactions")
	}
	tx, err := beginner.Begin(ctx)
	if err != nil {
		return domain.CreateChannelResult{}, fmt.Errorf("begin leave channel: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()
	channel, member, err := s.getChannelForMember(ctx, tx, userID, channelID)
	if err != nil {
		return domain.CreateChannelResult{}, err
	}
	if date == 0 {
		date = nowUnix()
	}
	members := []domain.ChannelMember{}
	adminsDelta := 0
	if isChannelAdmin(member) {
		adminsDelta--
	}
	if channel.CreatorUserID == userID || member.Role == domain.ChannelRoleCreator {
		if channel.CreatorUserID != userID || member.Role != domain.ChannelRoleCreator {
			return domain.CreateChannelResult{}, domain.ErrChannelUserCreator
		}
		future, err := s.futureCreatorAfterLeave(ctx, tx, channelID, userID)
		if err != nil {
			if errors.Is(err, domain.ErrUserNotParticipant) {
				return domain.CreateChannelResult{}, domain.ErrChannelUserCreator
			}
			return domain.CreateChannelResult{}, err
		}
		if !isChannelAdmin(future) {
			adminsDelta++
		}
		future.Role = domain.ChannelRoleCreator
		future.AdminRights = creatorChannelMember(channelID, future.UserID, date).AdminRights
		future.Rank = ""
		future.Status = domain.ChannelMemberActive
		future.LeftAt = 0
		channel.CreatorUserID = future.UserID
		members = append(members, future)
		member.Role = domain.ChannelRoleMember
		member.AdminRights = domain.ChannelAdminRights{}
		member.Rank = ""
	}
	member.Status = domain.ChannelMemberLeft
	member.LeftAt = date
	members = append([]domain.ChannelMember{member}, members...)
	if _, err := tx.Exec(ctx, `
UPDATE channels
SET creator_user_id = $2,
    participants_count = GREATEST(participants_count - 1, 0),
    admins_count = GREATEST(admins_count + $3, 0),
    updated_at = now()
WHERE id = $1`, channelID, channel.CreatorUserID, adminsDelta); err != nil {
		return domain.CreateChannelResult{}, fmt.Errorf("update channel leave state: %w", err)
	}
	if channel.ParticipantsCount > 0 {
		channel.ParticipantsCount--
	}
	channel.AdminsCount += adminsDelta
	if channel.AdminsCount < 0 {
		channel.AdminsCount = 0
	}
	for _, changed := range members {
		if err := upsertChannelMemberTx(ctx, tx, channel, changed); err != nil {
			return domain.CreateChannelResult{}, err
		}
	}
	recipients, err := s.listActiveChannelMemberIDs(ctx, tx, channelID, 0)
	if err != nil {
		return domain.CreateChannelResult{}, err
	}
	if err := s.insertChannelAdminLogTx(ctx, tx, domain.ChannelAdminLogEvent{
		ChannelID: channelID,
		UserID:    userID,
		Date:      date,
		Type:      domain.ChannelAdminLogParticipantLeave,
	}); err != nil {
		return domain.CreateChannelResult{}, err
	}
	if err := clearChannelMentionsForUserTx(ctx, tx, channelID, userID); err != nil {
		return domain.CreateChannelResult{}, err
	}
	if err := deleteWelcomeMessageDeliveriesTx(ctx, tx, channelID, []int64{userID}); err != nil {
		return domain.CreateChannelResult{}, err
	}
	var msg domain.ChannelMessage
	var event domain.ChannelUpdateEvent
	if channel.Megagroup {
		msg, event, err = s.insertServiceMessage(ctx, tx, channel, userID, date, domain.ChannelMessageAction{
			Type:    domain.ChannelActionChatDelete,
			UserIDs: []int64{userID},
		})
		if err != nil {
			return domain.CreateChannelResult{}, err
		}
		channel.TopMessageID = msg.ID
		channel.Pts = event.Pts
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.CreateChannelResult{}, fmt.Errorf("commit leave channel: %w", err)
	}
	committed = true
	recipients = append(recipients, userID)
	return domain.CreateChannelResult{Channel: channel, Members: members, Message: msg, Event: event, Recipients: recipients}, nil
}

func (s *ChannelStore) futureCreatorAfterLeave(ctx context.Context, db sqlcgen.DBTX, channelID, userID int64) (domain.ChannelMember, error) {
	member, err := scanChannelMember(db.QueryRow(ctx, `
SELECT channel_id, user_id, inviter_user_id, role, status, joined_at, left_at,
       admin_rights::text, banned_rights::text, rank, available_min_id, available_min_pts,
       history_clear_anchor_id, history_clear_anchor_date,
       read_inbox_max_id, read_outbox_max_id, unread_mark, slowmode_last_send_date
FROM channel_members
WHERE channel_id = $1
  AND user_id <> $2
  AND status = 'active'
  AND role <> 'creator'
  AND COALESCE((banned_rights->>'ViewMessages')::boolean, false) = false
ORDER BY CASE role WHEN 'admin' THEN 0 ELSE 1 END, user_id
LIMIT 1`, channelID, userID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ChannelMember{}, domain.ErrUserNotParticipant
		}
		return domain.ChannelMember{}, err
	}
	return member, nil
}

func (s *ChannelStore) listActiveChannelMemberIDs(ctx context.Context, db sqlcgen.DBTX, channelID int64, limit int) ([]int64, error) {
	if limit <= 0 || limit > domain.MaxChannelRealtimeFanout {
		limit = domain.MaxChannelRealtimeFanout
	}
	rows, err := db.Query(ctx, `SELECT user_id FROM channel_members WHERE channel_id = $1 AND status = 'active' ORDER BY user_id LIMIT $2`, channelID, limit)
	if err != nil {
		return nil, fmt.Errorf("list active channel members: %w", err)
	}
	defer rows.Close()
	out := make([]int64, 0, limit)
	for rows.Next() {
		var userID int64
		if err := rows.Scan(&userID); err != nil {
			return nil, err
		}
		out = append(out, userID)
	}
	return out, rows.Err()
}
