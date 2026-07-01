package postgres

import (
	"context"
	"errors"
	"fmt"
	"telesrv/internal/domain"
)

func (s *ChannelStore) EditChannelAdmin(ctx context.Context, req domain.EditChannelAdminRequest) (domain.EditChannelAdminResult, error) {
	if req.UserID == 0 || req.ChannelID == 0 || req.MemberID == 0 {
		return domain.EditChannelAdminResult{}, domain.ErrChannelInvalid
	}
	beginner, ok := s.db.(txBeginner)
	if !ok {
		return domain.EditChannelAdminResult{}, fmt.Errorf("edit channel admin: db does not support transactions")
	}
	if req.Date == 0 {
		req.Date = nowUnix()
	}
	tx, err := beginner.Begin(ctx)
	if err != nil {
		return domain.EditChannelAdminResult{}, fmt.Errorf("begin edit channel admin: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()
	channel, actor, err := s.getChannelForMember(ctx, tx, req.UserID, req.ChannelID)
	if err != nil {
		return domain.EditChannelAdminResult{}, err
	}
	if !canAddChannelAdmins(actor) {
		return domain.EditChannelAdminResult{}, domain.ErrChannelAdminRequired
	}
	if actor.Role != domain.ChannelRoleCreator && !adminRightsSubset(req.AdminRights, actor.AdminRights) {
		return domain.EditChannelAdminResult{}, domain.ErrChannelRightForbidden
	}
	previous, err := s.getChannelMember(ctx, tx, req.ChannelID, req.MemberID)
	if err != nil {
		if !errors.Is(err, domain.ErrChannelPrivate) {
			return domain.EditChannelAdminResult{}, err
		}
		previous = domain.ChannelMember{
			ChannelID:       req.ChannelID,
			UserID:          req.MemberID,
			InviterUserID:   req.UserID,
			Role:            domain.ChannelRoleMember,
			Status:          domain.ChannelMemberActive,
			JoinedAt:        req.Date,
			AvailableMinID:  channelInitialAvailableMinID(channel),
			AvailableMinPts: channelInitialAvailableMinPts(channel),
			ReadInboxMaxID:  channel.TopMessageID,
		}
	}
	if previous.Role == domain.ChannelRoleCreator {
		return domain.EditChannelAdminResult{}, domain.ErrChannelUserCreator
	}
	member := previous
	member.InviterUserID = req.UserID
	member.Status = domain.ChannelMemberActive
	member.LeftAt = 0
	member.Rank = req.Rank
	if previous.Status != domain.ChannelMemberActive {
		if minPts := channelInitialAvailableMinPts(channel); minPts > member.AvailableMinPts {
			member.AvailableMinPts = minPts
		}
	}
	member.AdminRights = domain.NormalizeFullMegagroupAdminRights(channel, req.AdminRights)
	if zeroChannelAdminRights(req.AdminRights) {
		member.Role = domain.ChannelRoleMember
		member.Rank = ""
	} else {
		member.Role = domain.ChannelRoleAdmin
	}
	if err := upsertChannelMemberTx(ctx, tx, channel, member); err != nil {
		return domain.EditChannelAdminResult{}, err
	}
	logType := domain.ChannelAdminLogParticipantPromote
	if member.Role != domain.ChannelRoleAdmin {
		logType = domain.ChannelAdminLogParticipantDemote
	}
	if err := s.insertChannelAdminLogTx(ctx, tx, domain.ChannelAdminLogEvent{
		ChannelID:       req.ChannelID,
		UserID:          req.UserID,
		Date:            req.Date,
		Type:            logType,
		PrevParticipant: &previous,
		NewParticipant:  &member,
	}); err != nil {
		return domain.EditChannelAdminResult{}, err
	}
	channel, err = refreshChannelCountsTx(ctx, tx, channel)
	if err != nil {
		return domain.EditChannelAdminResult{}, err
	}
	event := transientChannelParticipantEvent(channel.ID, req.UserID, previous, member, req.Date)
	msg, _ := s.getChannelMessage(ctx, tx, req.ChannelID, channel.TopMessageID)
	if err := upsertChannelDialogTx(ctx, tx, member.UserID, channel, msg, member.ReadInboxMaxID, member.ReadOutboxMaxID); err != nil {
		return domain.EditChannelAdminResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.EditChannelAdminResult{}, fmt.Errorf("commit edit channel admin: %w", err)
	}
	committed = true
	recipients, _ := s.ListActiveChannelMemberIDs(ctx, req.UserID, req.ChannelID, 0)
	recipients = append(recipients, req.MemberID)
	return domain.EditChannelAdminResult{Channel: channel, Previous: previous, Participant: member, Event: event, Recipients: recipients, Date: req.Date}, nil
}

func (s *ChannelStore) TransferChannelOwnership(ctx context.Context, req domain.TransferChannelOwnershipRequest) (domain.TransferChannelOwnershipResult, error) {
	if req.UserID == 0 || req.ChannelID == 0 || req.NewOwnerID == 0 || req.NewOwnerID == req.UserID {
		return domain.TransferChannelOwnershipResult{}, domain.ErrChannelInvalid
	}
	beginner, ok := s.db.(txBeginner)
	if !ok {
		return domain.TransferChannelOwnershipResult{}, fmt.Errorf("transfer channel ownership: db does not support transactions")
	}
	if req.Date == 0 {
		req.Date = nowUnix()
	}
	tx, err := beginner.Begin(ctx)
	if err != nil {
		return domain.TransferChannelOwnershipResult{}, fmt.Errorf("begin transfer channel ownership: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()
	channel, previousOwner, err := s.getChannelForMember(ctx, tx, req.UserID, req.ChannelID)
	if err != nil {
		return domain.TransferChannelOwnershipResult{}, err
	}
	if channel.CreatorUserID != req.UserID || previousOwner.Role != domain.ChannelRoleCreator {
		return domain.TransferChannelOwnershipResult{}, domain.ErrChannelAdminRequired
	}
	previousNewOwner, err := s.getChannelMember(ctx, tx, req.ChannelID, req.NewOwnerID)
	if err != nil {
		if errors.Is(err, domain.ErrChannelPrivate) {
			return domain.TransferChannelOwnershipResult{}, domain.ErrUserNotParticipant
		}
		return domain.TransferChannelOwnershipResult{}, err
	}
	if previousNewOwner.Status != domain.ChannelMemberActive || previousNewOwner.BannedRights.ViewMessages {
		return domain.TransferChannelOwnershipResult{}, domain.ErrUserNotParticipant
	}
	if previousNewOwner.Role == domain.ChannelRoleCreator {
		return domain.TransferChannelOwnershipResult{}, domain.ErrChannelNotModified
	}
	oldOwner := previousOwner
	oldOwner.Role = domain.ChannelRoleAdmin
	oldOwner.AdminRights = creatorChannelMember(req.ChannelID, req.UserID, req.Date).AdminRights
	oldOwner.Rank = ""
	oldOwner.Status = domain.ChannelMemberActive
	oldOwner.LeftAt = 0
	if oldOwner.InviterUserID == 0 {
		oldOwner.InviterUserID = req.UserID
	}
	newOwner := previousNewOwner
	newOwner.Role = domain.ChannelRoleCreator
	newOwner.AdminRights = creatorChannelMember(req.ChannelID, req.NewOwnerID, req.Date).AdminRights
	newOwner.Rank = ""
	newOwner.Status = domain.ChannelMemberActive
	newOwner.LeftAt = 0
	if newOwner.JoinedAt == 0 {
		newOwner.JoinedAt = req.Date
	}
	channel.CreatorUserID = req.NewOwnerID
	if _, err := tx.Exec(ctx, `
UPDATE channels
SET creator_user_id = $2,
    updated_at = now()
WHERE id = $1 AND NOT deleted`, req.ChannelID, req.NewOwnerID); err != nil {
		return domain.TransferChannelOwnershipResult{}, fmt.Errorf("update channel creator: %w", err)
	}
	if err := upsertChannelMemberTx(ctx, tx, channel, oldOwner); err != nil {
		return domain.TransferChannelOwnershipResult{}, err
	}
	if err := upsertChannelMemberTx(ctx, tx, channel, newOwner); err != nil {
		return domain.TransferChannelOwnershipResult{}, err
	}
	if err := s.insertChannelAdminLogTx(ctx, tx, domain.ChannelAdminLogEvent{
		ChannelID:       req.ChannelID,
		UserID:          req.UserID,
		Date:            req.Date,
		Type:            domain.ChannelAdminLogParticipantPromote,
		PrevParticipant: &previousOwner,
		NewParticipant:  &oldOwner,
	}); err != nil {
		return domain.TransferChannelOwnershipResult{}, err
	}
	if err := s.insertChannelAdminLogTx(ctx, tx, domain.ChannelAdminLogEvent{
		ChannelID:       req.ChannelID,
		UserID:          req.UserID,
		Date:            req.Date,
		Type:            domain.ChannelAdminLogParticipantPromote,
		PrevParticipant: &previousNewOwner,
		NewParticipant:  &newOwner,
	}); err != nil {
		return domain.TransferChannelOwnershipResult{}, err
	}
	channel, err = refreshChannelCountsTx(ctx, tx, channel)
	if err != nil {
		return domain.TransferChannelOwnershipResult{}, err
	}
	msg, _ := s.getChannelMessage(ctx, tx, req.ChannelID, channel.TopMessageID)
	if err := upsertChannelDialogTx(ctx, tx, oldOwner.UserID, channel, msg, oldOwner.ReadInboxMaxID, oldOwner.ReadOutboxMaxID); err != nil {
		return domain.TransferChannelOwnershipResult{}, err
	}
	if err := upsertChannelDialogTx(ctx, tx, newOwner.UserID, channel, msg, newOwner.ReadInboxMaxID, newOwner.ReadOutboxMaxID); err != nil {
		return domain.TransferChannelOwnershipResult{}, err
	}
	recipients, err := s.listActiveChannelMemberIDs(ctx, tx, req.ChannelID, 0)
	if err != nil {
		return domain.TransferChannelOwnershipResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.TransferChannelOwnershipResult{}, fmt.Errorf("commit transfer channel ownership: %w", err)
	}
	committed = true
	if s.rowCache != nil {
		s.rowCache.delete(req.ChannelID)
	}
	if s.memberCache != nil {
		s.memberCache.delete(req.ChannelID, req.UserID)
		s.memberCache.delete(req.ChannelID, req.NewOwnerID)
	}
	if s.dialogCache != nil {
		s.dialogCache.delete(oldOwner.UserID, req.ChannelID)
		s.dialogCache.delete(newOwner.UserID, req.ChannelID)
	}
	events := []domain.ChannelUpdateEvent{
		transientChannelParticipantEvent(channel.ID, req.UserID, previousOwner, oldOwner, req.Date),
		transientChannelParticipantEvent(channel.ID, req.UserID, previousNewOwner, newOwner, req.Date),
	}
	recipients = append(recipients, req.UserID, req.NewOwnerID)
	return domain.TransferChannelOwnershipResult{
		Channel:          channel,
		PreviousOwner:    previousOwner,
		OldOwner:         oldOwner,
		PreviousNewOwner: previousNewOwner,
		NewOwner:         newOwner,
		Events:           events,
		Recipients:       recipients,
		Date:             req.Date,
	}, nil
}

func (s *ChannelStore) EditChannelMemberRank(ctx context.Context, req domain.EditChannelMemberRankRequest) (domain.EditChannelAdminResult, error) {
	if req.UserID == 0 || req.ChannelID == 0 || req.MemberID == 0 {
		return domain.EditChannelAdminResult{}, domain.ErrChannelInvalid
	}
	beginner, ok := s.db.(txBeginner)
	if !ok {
		return domain.EditChannelAdminResult{}, fmt.Errorf("edit channel member rank: db does not support transactions")
	}
	if req.Date == 0 {
		req.Date = nowUnix()
	}
	tx, err := beginner.Begin(ctx)
	if err != nil {
		return domain.EditChannelAdminResult{}, fmt.Errorf("begin edit channel member rank: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()
	channel, actor, err := s.getChannelForMember(ctx, tx, req.UserID, req.ChannelID)
	if err != nil {
		return domain.EditChannelAdminResult{}, err
	}
	previous, err := s.getChannelMember(ctx, tx, req.ChannelID, req.MemberID)
	if err != nil {
		if errors.Is(err, domain.ErrChannelPrivate) {
			return domain.EditChannelAdminResult{}, domain.ErrUserNotParticipant
		}
		return domain.EditChannelAdminResult{}, err
	}
	if previous.Status != domain.ChannelMemberActive {
		return domain.EditChannelAdminResult{}, domain.ErrUserNotParticipant
	}
	if err := checkEditMemberRank(channel, actor, previous); err != nil {
		return domain.EditChannelAdminResult{}, err
	}
	member := previous
	member.Rank = req.Rank
	if err := upsertChannelMemberTx(ctx, tx, channel, member); err != nil {
		return domain.EditChannelAdminResult{}, err
	}
	if err := s.insertChannelAdminLogTx(ctx, tx, domain.ChannelAdminLogEvent{
		ChannelID:   req.ChannelID,
		UserID:      req.UserID,
		Date:        req.Date,
		Type:        domain.ChannelAdminLogParticipantEditRank,
		PrevString:  previous.Rank,
		NewString:   member.Rank,
		Participant: &member,
	}); err != nil {
		return domain.EditChannelAdminResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.EditChannelAdminResult{}, fmt.Errorf("commit edit channel member rank: %w", err)
	}
	committed = true
	event := transientChannelParticipantEvent(channel.ID, req.UserID, previous, member, req.Date)
	recipients, _ := s.ListActiveChannelMemberIDs(ctx, req.UserID, req.ChannelID, 0)
	recipients = append(recipients, req.MemberID)
	return domain.EditChannelAdminResult{Channel: channel, Previous: previous, Participant: member, Event: event, Recipients: recipients, Date: req.Date}, nil
}

func (s *ChannelStore) EditChannelBanned(ctx context.Context, req domain.EditChannelBannedRequest) (domain.EditChannelBannedResult, error) {
	if req.UserID == 0 || req.ChannelID == 0 || req.Participant.Type != domain.PeerTypeUser || req.Participant.ID == 0 {
		return domain.EditChannelBannedResult{}, domain.ErrChannelInvalid
	}
	beginner, ok := s.db.(txBeginner)
	if !ok {
		return domain.EditChannelBannedResult{}, fmt.Errorf("edit channel banned: db does not support transactions")
	}
	if req.Date == 0 {
		req.Date = nowUnix()
	}
	tx, err := beginner.Begin(ctx)
	if err != nil {
		return domain.EditChannelBannedResult{}, fmt.Errorf("begin edit channel banned: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()
	channel, actor, err := s.getChannelForMember(ctx, tx, req.UserID, req.ChannelID)
	if err != nil {
		return domain.EditChannelBannedResult{}, err
	}
	if !canBanChannelUsers(actor) {
		return domain.EditChannelBannedResult{}, domain.ErrChannelAdminRequired
	}
	previous, err := s.getChannelMember(ctx, tx, req.ChannelID, req.Participant.ID)
	if err != nil {
		if !errors.Is(err, domain.ErrChannelPrivate) {
			return domain.EditChannelBannedResult{}, err
		}
		previous = domain.ChannelMember{
			ChannelID:     req.ChannelID,
			UserID:        req.Participant.ID,
			InviterUserID: req.UserID,
			Role:          domain.ChannelRoleMember,
			Status:        domain.ChannelMemberLeft,
		}
	}
	if previous.Role == domain.ChannelRoleCreator {
		return domain.EditChannelBannedResult{}, domain.ErrChannelUserCreator
	}
	member := previous
	member.BannedRights = req.BannedRights
	member.Role = domain.ChannelRoleMember
	switch {
	case req.BannedRights.ViewMessages:
		member.InviterUserID = req.UserID
		member.Status = domain.ChannelMemberKicked
		member.LeftAt = req.Date
	case zeroChannelBannedRights(req.BannedRights):
		if previous.Status == domain.ChannelMemberActive {
			member.Status = domain.ChannelMemberActive
		} else {
			member.Status = domain.ChannelMemberLeft
		}
		member.LeftAt = 0
	default:
		member.InviterUserID = req.UserID
		if previous.Status == domain.ChannelMemberActive {
			member.Status = domain.ChannelMemberActive
		} else {
			member.Status = domain.ChannelMemberBanned
		}
	}
	if member.JoinedAt == 0 && member.Status == domain.ChannelMemberActive {
		member.JoinedAt = req.Date
	}
	if err := upsertChannelMemberTx(ctx, tx, channel, member); err != nil {
		return domain.EditChannelBannedResult{}, err
	}
	if err := s.insertChannelAdminLogTx(ctx, tx, domain.ChannelAdminLogEvent{
		ChannelID:       req.ChannelID,
		UserID:          req.UserID,
		Date:            req.Date,
		Type:            adminLogBanType(previous, member),
		PrevParticipant: &previous,
		NewParticipant:  &member,
	}); err != nil {
		return domain.EditChannelBannedResult{}, err
	}
	channel, err = refreshChannelCountsTx(ctx, tx, channel)
	if err != nil {
		return domain.EditChannelBannedResult{}, err
	}
	event := transientChannelParticipantEvent(channel.ID, req.UserID, previous, member, req.Date)
	if member.Status == domain.ChannelMemberActive {
		msg, _ := s.getChannelMessage(ctx, tx, req.ChannelID, channel.TopMessageID)
		if err := upsertChannelDialogTx(ctx, tx, member.UserID, channel, msg, member.ReadInboxMaxID, member.ReadOutboxMaxID); err != nil {
			return domain.EditChannelBannedResult{}, err
		}
	}
	if member.Status == domain.ChannelMemberKicked && previous.Status == domain.ChannelMemberActive {
		if err := clearChannelMentionsForUserTx(ctx, tx, req.ChannelID, req.Participant.ID); err != nil {
			return domain.EditChannelBannedResult{}, err
		}
	}
	var serviceMsg domain.ChannelMessage
	var serviceEvent domain.ChannelUpdateEvent
	if channel.Megagroup && previous.Status == domain.ChannelMemberActive && member.Status == domain.ChannelMemberKicked {
		// megagroup 踢人产生可见 "X removed Y" 服务消息并占 channel pts，
		// 其它成员的成员面板/人数与离线差量都靠它收敛。
		serviceMsg, serviceEvent, err = s.insertServiceMessage(ctx, tx, channel, req.UserID, req.Date, domain.ChannelMessageAction{
			Type:    domain.ChannelActionChatDelete,
			UserIDs: []int64{req.Participant.ID},
		})
		if err != nil {
			return domain.EditChannelBannedResult{}, err
		}
		channel.TopMessageID = serviceMsg.ID
		channel.Pts = serviceEvent.Pts
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.EditChannelBannedResult{}, fmt.Errorf("commit edit channel banned: %w", err)
	}
	committed = true
	recipients, _ := s.ListActiveChannelMemberIDs(ctx, req.UserID, req.ChannelID, 0)
	recipients = append(recipients, req.Participant.ID)
	return domain.EditChannelBannedResult{Channel: channel, Previous: previous, Participant: member, Event: event, Recipients: recipients, Date: req.Date, Message: serviceMsg, ServiceEvent: serviceEvent}, nil
}

func (s *ChannelStore) EditChannelDefaultBannedRights(ctx context.Context, req domain.EditChannelDefaultBannedRightsRequest) (domain.Channel, error) {
	if req.UserID == 0 || req.ChannelID == 0 {
		return domain.Channel{}, domain.ErrChannelInvalid
	}
	channel, actor, err := s.getChannelForMember(ctx, s.db, req.UserID, req.ChannelID)
	if err != nil {
		return domain.Channel{}, err
	}
	if !canBanChannelUsers(actor) {
		return domain.Channel{}, domain.ErrChannelAdminRequired
	}
	if channel.DefaultBannedRights == req.BannedRights {
		return domain.Channel{}, domain.ErrChannelNotModified
	}
	rights, err := marshalJSON(req.BannedRights, "{}")
	if err != nil {
		return domain.Channel{}, err
	}
	if _, err := s.db.Exec(ctx, `
UPDATE channels
SET default_banned_rights = $2::jsonb, updated_at = now()
WHERE id = $1 AND NOT deleted`, req.ChannelID, rights); err != nil {
		return domain.Channel{}, fmt.Errorf("edit channel default banned rights: %w", err)
	}
	channel.DefaultBannedRights = req.BannedRights
	return channel, nil
}

func (s *ChannelStore) ListAdminedPublicChannels(ctx context.Context, userID int64) ([]domain.Channel, error) {
	if userID == 0 {
		return nil, nil
	}
	rows, err := s.db.Query(ctx, `
SELECT channel_id
FROM user_channel_member_index
WHERE user_id = $1
  AND status = 'active'
  AND role IN ('creator','admin')
  AND public_username
  AND NOT deleted
ORDER BY channel_id DESC
LIMIT $2`, userID, domain.MaxAdminedPublicChannels)
	if err != nil {
		return nil, fmt.Errorf("list admined public channels: %w", err)
	}
	defer rows.Close()
	ids := make([]int64, 0, domain.MaxAdminedPublicChannels)
	for rows.Next() {
		var channelID int64
		if err := rows.Scan(&channelID); err != nil {
			return nil, err
		}
		ids = append(ids, channelID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	channels, err := listChannelsByIDsInOrder(ctx, s.db, ids)
	if err != nil {
		return nil, fmt.Errorf("list admined public channel details: %w", err)
	}
	out := make([]domain.Channel, 0, len(channels))
	for _, channel := range channels {
		if channel.Username == "" || channel.Deleted {
			continue
		}
		out = append(out, channel)
	}
	return out, nil
}

// ListSendAsChannels lists the broadcast channels a user may post messages AS in groups
// (channels.getSendAs candidates): channels where the user is the creator, or an admin holding
// PostMessages rights. Mirrors ListStoryPostableChannels but is restricted to broadcast channels
// and the PostMessages right (sending as an owned megagroup is not a real Telegram capability).
func (s *ChannelStore) ListSendAsChannels(ctx context.Context, userID int64) ([]domain.Channel, error) {
	if userID == 0 {
		return nil, nil
	}
	rows, err := s.db.Query(ctx, `
SELECT i.channel_id
FROM user_channel_member_index i
JOIN channel_members m ON m.channel_id = i.channel_id AND m.user_id = i.user_id
WHERE i.user_id = $1
  AND i.status = 'active'
  AND i.role IN ('creator', 'admin')
  AND i.broadcast
  AND NOT i.deleted
  AND (
      i.role = 'creator'
      OR COALESCE((m.admin_rights->>'PostMessages')::boolean, false)
  )
ORDER BY i.channel_id DESC
LIMIT $2`, userID, domain.MaxSendAsChannels)
	if err != nil {
		return nil, fmt.Errorf("list send-as channels: %w", err)
	}
	defer rows.Close()
	ids := make([]int64, 0, domain.MaxSendAsChannels)
	for rows.Next() {
		var channelID int64
		if err := rows.Scan(&channelID); err != nil {
			return nil, err
		}
		ids = append(ids, channelID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	channels, err := listChannelsByIDsInOrder(ctx, s.db, ids)
	if err != nil {
		return nil, fmt.Errorf("list send-as channel details: %w", err)
	}
	out := make([]domain.Channel, 0, len(channels))
	for _, channel := range channels {
		if channel.Deleted || !channel.Broadcast {
			continue
		}
		out = append(out, channel)
	}
	return out, nil
}

func (s *ChannelStore) ListStoryPostableChannels(ctx context.Context, userID int64) ([]domain.Channel, error) {
	if userID == 0 {
		return nil, nil
	}
	rows, err := s.db.Query(ctx, `
SELECT i.channel_id
FROM user_channel_member_index i
JOIN channel_members m ON m.channel_id = i.channel_id AND m.user_id = i.user_id
WHERE i.user_id = $1
  AND i.status = 'active'
  AND i.role IN ('creator', 'admin')
  AND (i.megagroup OR i.broadcast)
  AND NOT i.deleted
  AND (
      i.role = 'creator'
      OR COALESCE((m.admin_rights->>'PostStories')::boolean, false)
  )
ORDER BY i.channel_id DESC
LIMIT $2`, userID, domain.MaxStorySendAsChannels)
	if err != nil {
		return nil, fmt.Errorf("list story postable channels: %w", err)
	}
	defer rows.Close()
	ids := make([]int64, 0, domain.MaxStorySendAsChannels)
	for rows.Next() {
		var channelID int64
		if err := rows.Scan(&channelID); err != nil {
			return nil, err
		}
		ids = append(ids, channelID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	channels, err := listChannelsByIDsInOrder(ctx, s.db, ids)
	if err != nil {
		return nil, fmt.Errorf("list story postable channel details: %w", err)
	}
	out := make([]domain.Channel, 0, len(channels))
	for _, channel := range channels {
		if channel.Deleted || (!channel.Broadcast && !channel.Megagroup) {
			continue
		}
		out = append(out, channel)
	}
	return out, nil
}
