package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"telesrv/internal/domain"
	"telesrv/internal/store/postgres/sqlcgen"
)

func (s *ChannelStore) getChannelForMember(ctx context.Context, db sqlcgen.DBTX, viewerUserID, channelID int64) (domain.Channel, domain.ChannelMember, error) {
	member, err := s.getChannelMember(ctx, db, channelID, viewerUserID)
	if err != nil {
		return domain.Channel{}, domain.ChannelMember{}, err
	}
	if err := validateChannelMemberVisible(member); err != nil {
		return domain.Channel{}, domain.ChannelMember{}, err
	}
	ch, err := s.channelByID(ctx, db, channelID)
	if err != nil {
		return domain.Channel{}, domain.ChannelMember{}, err
	}
	return ch, member, nil
}

func (s *ChannelStore) getChannelForMemberOrLinkedGuest(ctx context.Context, db sqlcgen.DBTX, viewerUserID, channelID int64) (domain.Channel, domain.ChannelMember, error) {
	channel, member, err := s.getChannelForMember(ctx, db, viewerUserID, channelID)
	if !errors.Is(err, domain.ErrChannelPrivate) {
		return channel, member, err
	}
	target, targetErr := s.channelByID(ctx, db, channelID)
	if targetErr != nil {
		return domain.Channel{}, domain.ChannelMember{}, targetErr
	}
	guest, allowed, guestErr := s.getLinkedDiscussionGuest(ctx, db, viewerUserID, target)
	if guestErr != nil {
		return domain.Channel{}, domain.ChannelMember{}, guestErr
	}
	if !allowed {
		return domain.Channel{}, domain.ChannelMember{}, err
	}
	return target, guest, nil
}

// getLinkedDiscussionGuest authorizes a private discussion group through an
// active membership in its bidirectionally linked broadcast. The returned
// member is computed only and must never be persisted. An explicit target ban
// or kick takes precedence over the source-channel membership.
func (s *ChannelStore) getLinkedDiscussionGuest(ctx context.Context, db sqlcgen.DBTX, viewerUserID int64, target domain.Channel) (domain.ChannelMember, bool, error) {
	if target.Broadcast || !target.Megagroup || target.LinkedChatID == 0 {
		return domain.ChannelMember{}, false, nil
	}
	existing, err := s.getChannelMember(ctx, db, target.ID, viewerUserID)
	if err == nil {
		if existing.Status == domain.ChannelMemberBanned || existing.Status == domain.ChannelMemberKicked || existing.BannedRights.ViewMessages {
			return domain.ChannelMember{}, false, domain.ErrChannelUserBanned
		}
		if existing.Status == domain.ChannelMemberActive {
			return existing, false, nil
		}
	} else if !errors.Is(err, domain.ErrChannelPrivate) {
		return domain.ChannelMember{}, false, err
	}
	source, err := s.channelByID(ctx, db, target.LinkedChatID)
	if err != nil {
		if errors.Is(err, domain.ErrChannelInvalid) {
			return domain.ChannelMember{}, false, nil
		}
		return domain.ChannelMember{}, false, err
	}
	if !source.Broadcast || source.LinkedChatID != target.ID {
		return domain.ChannelMember{}, false, nil
	}
	sourceMember, err := s.getChannelMember(ctx, db, source.ID, viewerUserID)
	if err != nil {
		if errors.Is(err, domain.ErrChannelPrivate) {
			return domain.ChannelMember{}, false, nil
		}
		return domain.ChannelMember{}, false, err
	}
	if err := validateChannelMemberVisible(sourceMember); err != nil {
		return domain.ChannelMember{}, false, err
	}
	guest := domain.ChannelMember{
		ChannelID: target.ID,
		UserID:    viewerUserID,
		Status:    domain.ChannelMemberLeft,
		Role:      domain.ChannelRoleMember,
		Guest:     true,
	}
	if s.memberCacheActive(db) {
		s.memberCache.put(guest)
	}
	return guest, true, nil
}

func (s *ChannelStore) getPublicPreviewMember(ctx context.Context, db sqlcgen.DBTX, viewerUserID int64, ch domain.Channel) (domain.ChannelMember, error) {
	member, err := s.getChannelMember(ctx, db, ch.ID, viewerUserID)
	if err != nil {
		if errors.Is(err, domain.ErrChannelPrivate) {
			return publicPreviewMember(ch, viewerUserID, domain.ChannelMember{}, false), nil
		}
		return domain.ChannelMember{}, err
	}
	if member.Status == domain.ChannelMemberBanned || member.Status == domain.ChannelMemberKicked || member.BannedRights.ViewMessages {
		return domain.ChannelMember{}, domain.ErrChannelUserBanned
	}
	return publicPreviewMember(ch, viewerUserID, member, true), nil
}

func (s *ChannelStore) getChannelMember(ctx context.Context, db sqlcgen.DBTX, channelID, userID int64) (domain.ChannelMember, error) {
	if s.memberCacheActive(db) {
		return s.memberCache.getOrLoad(ctx, channelID, userID, func() (domain.ChannelMember, error) {
			return getChannelMemberByID(ctx, db, channelID, userID)
		})
	}
	return getChannelMemberByID(ctx, db, channelID, userID)
}

func getChannelMemberByID(ctx context.Context, db sqlcgen.DBTX, channelID, userID int64) (domain.ChannelMember, error) {
	row := db.QueryRow(ctx, `
SELECT channel_id, user_id, inviter_user_id, role, status, joined_at, left_at,
       admin_rights::text, banned_rights::text, rank, available_min_id, available_min_pts,
       read_inbox_max_id, read_outbox_max_id, unread_mark, slowmode_last_send_date
FROM channel_members
WHERE channel_id = $1 AND user_id = $2`, channelID, userID)
	member, err := scanChannelMember(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ChannelMember{}, domain.ErrChannelPrivate
	}
	if err != nil {
		return domain.ChannelMember{}, err
	}
	return member, nil
}

func upsertChannelMemberTx(ctx context.Context, tx pgx.Tx, channel domain.Channel, member domain.ChannelMember) error {
	adminRights, err := marshalJSON(member.AdminRights, "{}")
	if err != nil {
		return err
	}
	bannedRights, err := marshalJSON(member.BannedRights, "{}")
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
INSERT INTO channel_members (
    channel_id, user_id, inviter_user_id, role, status, joined_at, left_at, admin_rights, banned_rights,
    rank, available_min_id, available_min_pts, read_inbox_max_id, read_outbox_max_id, unread_mark, slowmode_last_send_date
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)
ON CONFLICT (channel_id, user_id) DO UPDATE SET
    inviter_user_id = EXCLUDED.inviter_user_id,
    role = EXCLUDED.role,
    status = EXCLUDED.status,
    joined_at = EXCLUDED.joined_at,
    left_at = EXCLUDED.left_at,
    admin_rights = EXCLUDED.admin_rights,
    banned_rights = EXCLUDED.banned_rights,
    rank = EXCLUDED.rank,
    available_min_id = GREATEST(channel_members.available_min_id, EXCLUDED.available_min_id),
    available_min_pts = GREATEST(channel_members.available_min_pts, EXCLUDED.available_min_pts),
    read_inbox_max_id = GREATEST(channel_members.read_inbox_max_id, EXCLUDED.read_inbox_max_id),
    updated_at = now()`,
		member.ChannelID, member.UserID, member.InviterUserID, string(member.Role), string(member.Status),
		member.JoinedAt, member.LeftAt, adminRights, bannedRights, member.Rank, member.AvailableMinID,
		member.AvailableMinPts, member.ReadInboxMaxID, member.ReadOutboxMaxID, member.UnreadMark, member.SlowmodeLastSendDate); err != nil {
		return fmt.Errorf("upsert channel member: %w", err)
	}
	return upsertUserChannelMemberIndexTx(ctx, tx, channel, member)
}

func upsertUserChannelMemberIndexTx(ctx context.Context, tx pgx.Tx, channel domain.Channel, member domain.ChannelMember) error {
	if channel.ID == 0 || member.UserID == 0 {
		return nil
	}
	if _, err := tx.Exec(ctx, `
INSERT INTO user_channel_member_index (
    user_id, channel_id, status, megagroup, broadcast, deleted,
    role, left_at, forum, public_username, can_pin_messages
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
ON CONFLICT (user_id, channel_id) DO UPDATE SET
    status = EXCLUDED.status,
    megagroup = EXCLUDED.megagroup,
    broadcast = EXCLUDED.broadcast,
    deleted = EXCLUDED.deleted,
    role = EXCLUDED.role,
    left_at = EXCLUDED.left_at,
    forum = EXCLUDED.forum,
    public_username = EXCLUDED.public_username,
    can_pin_messages = EXCLUDED.can_pin_messages,
    updated_at = now()`,
		member.UserID, channel.ID, string(member.Status), channel.Megagroup, channel.Broadcast, channel.Deleted,
		string(member.Role), member.LeftAt, channel.Forum, channel.Username != "", channelMemberCanPinMessages(member)); err != nil {
		return fmt.Errorf("upsert user channel member index: %w", err)
	}
	return nil
}

func channelMemberCanPinMessages(member domain.ChannelMember) bool {
	return member.Role == domain.ChannelRoleCreator || (member.Role == domain.ChannelRoleAdmin && member.AdminRights.PinMessages)
}

func markUserChannelMemberIndexDeletedTx(ctx context.Context, tx pgx.Tx, channelID int64, deleted bool) error {
	if channelID == 0 {
		return nil
	}
	if _, err := tx.Exec(ctx, `
UPDATE user_channel_member_index
SET deleted = $2, updated_at = now()
WHERE channel_id = $1`, channelID, deleted); err != nil {
		return fmt.Errorf("mark user channel member index deleted: %w", err)
	}
	return nil
}

func markUserChannelMemberIndexPublicTx(ctx context.Context, tx pgx.Tx, channelID int64, public bool) error {
	if channelID == 0 {
		return nil
	}
	if _, err := tx.Exec(ctx, `
UPDATE user_channel_member_index
SET public_username = $2, updated_at = now()
WHERE channel_id = $1`, channelID, public); err != nil {
		return fmt.Errorf("mark user channel member index public: %w", err)
	}
	return nil
}

func markUserChannelMemberIndexForumTx(ctx context.Context, tx pgx.Tx, channelID int64, forum bool) error {
	if channelID == 0 {
		return nil
	}
	if _, err := tx.Exec(ctx, `
UPDATE user_channel_member_index
SET forum = $2, updated_at = now()
WHERE channel_id = $1`, channelID, forum); err != nil {
		return fmt.Errorf("mark user channel member index forum: %w", err)
	}
	return nil
}

func scanChannelWithMember(row rowScanner) (domain.Channel, domain.ChannelMember, error) {
	var ch domain.Channel
	var member domain.ChannelMember
	var defaultRights, reactionPolicy, adminRights, bannedRights string
	var wallpaper *string
	var role, status string
	dest := append(channelScanDest(&ch, &defaultRights, &reactionPolicy, &wallpaper),
		&member.ChannelID, &member.UserID, &member.InviterUserID, &role, &status,
		&member.JoinedAt, &member.LeftAt, &adminRights, &bannedRights, &member.Rank,
		&member.AvailableMinID, &member.AvailableMinPts, &member.ReadInboxMaxID, &member.ReadOutboxMaxID, &member.UnreadMark, &member.SlowmodeLastSendDate,
	)
	if err := row.Scan(dest...); err != nil {
		return domain.Channel{}, domain.ChannelMember{}, err
	}
	member.Role = domain.ChannelMemberRole(role)
	member.Status = domain.ChannelMemberStatus(status)
	finishChannelScan(&ch, defaultRights, reactionPolicy, wallpaper)
	_ = json.Unmarshal([]byte(adminRights), &member.AdminRights)
	_ = json.Unmarshal([]byte(bannedRights), &member.BannedRights)
	return ch, member, nil
}

func scanChannelWithViewerMember(row rowScanner) (domain.Channel, bool, error) {
	var ch domain.Channel
	var viewerMember bool
	var rights, reactionPolicy string
	var wallpaper *string
	dest := append(channelScanDest(&ch, &rights, &reactionPolicy, &wallpaper),
		&viewerMember,
	)
	if err := row.Scan(dest...); err != nil {
		return domain.Channel{}, false, err
	}
	finishChannelScan(&ch, rights, reactionPolicy, wallpaper)
	return ch, viewerMember, nil
}

func scanChannelMember(row rowScanner) (domain.ChannelMember, error) {
	var member domain.ChannelMember
	var adminRights, bannedRights string
	var role, status string
	if err := row.Scan(
		&member.ChannelID, &member.UserID, &member.InviterUserID, &role, &status,
		&member.JoinedAt, &member.LeftAt, &adminRights, &bannedRights, &member.Rank,
		&member.AvailableMinID, &member.AvailableMinPts, &member.ReadInboxMaxID, &member.ReadOutboxMaxID, &member.UnreadMark, &member.SlowmodeLastSendDate,
	); err != nil {
		return domain.ChannelMember{}, err
	}
	member.Role = domain.ChannelMemberRole(role)
	member.Status = domain.ChannelMemberStatus(status)
	_ = json.Unmarshal([]byte(adminRights), &member.AdminRights)
	_ = json.Unmarshal([]byte(bannedRights), &member.BannedRights)
	return member, nil
}

func scanChannelMemberWithCount(row rowScanner) (domain.ChannelMember, int, error) {
	var member domain.ChannelMember
	var adminRights, bannedRights string
	var role, status string
	var count int
	if err := row.Scan(
		&member.ChannelID, &member.UserID, &member.InviterUserID, &role, &status,
		&member.JoinedAt, &member.LeftAt, &adminRights, &bannedRights, &member.Rank,
		&member.AvailableMinID, &member.AvailableMinPts, &member.ReadInboxMaxID, &member.ReadOutboxMaxID, &member.UnreadMark, &member.SlowmodeLastSendDate,
		&count,
	); err != nil {
		return domain.ChannelMember{}, 0, err
	}
	member.Role = domain.ChannelMemberRole(role)
	member.Status = domain.ChannelMemberStatus(status)
	_ = json.Unmarshal([]byte(adminRights), &member.AdminRights)
	_ = json.Unmarshal([]byte(bannedRights), &member.BannedRights)
	return member, count, nil
}

func validateChannelMemberVisible(member domain.ChannelMember) error {
	switch member.Status {
	case domain.ChannelMemberActive:
		if member.BannedRights.ViewMessages {
			return domain.ErrChannelUserBanned
		}
		return nil
	case domain.ChannelMemberBanned, domain.ChannelMemberKicked:
		return domain.ErrChannelUserBanned
	default:
		return domain.ErrChannelPrivate
	}
}

func canPostChannel(member domain.ChannelMember) bool {
	return member.Role == domain.ChannelRoleCreator || (member.Role == domain.ChannelRoleAdmin && member.AdminRights.PostMessages)
}

func isChannelAdmin(member domain.ChannelMember) bool {
	return member.Role == domain.ChannelRoleCreator || member.Role == domain.ChannelRoleAdmin
}

func canManageDiscussionBroadcast(member domain.ChannelMember) bool {
	return canChangeChannelInfo(member)
}

func canManageDiscussionGroup(member domain.ChannelMember) bool {
	return member.Role == domain.ChannelRoleCreator || (member.Role == domain.ChannelRoleAdmin && member.AdminRights.PinMessages)
}

func canAddChannelAdmins(member domain.ChannelMember) bool {
	return member.Role == domain.ChannelRoleCreator || (member.Role == domain.ChannelRoleAdmin && member.AdminRights.AddAdmins)
}

func canBanChannelUsers(member domain.ChannelMember) bool {
	return member.Role == domain.ChannelRoleCreator || (member.Role == domain.ChannelRoleAdmin && member.AdminRights.BanUsers)
}

func publicPreviewMember(channel domain.Channel, userID int64, existing domain.ChannelMember, found bool) domain.ChannelMember {
	member := domain.ChannelMember{
		ChannelID:       channel.ID,
		UserID:          userID,
		Role:            domain.ChannelRoleMember,
		Status:          domain.ChannelMemberLeft,
		AvailableMinID:  channelInitialAvailableMinID(channel),
		AvailableMinPts: channelInitialAvailableMinPts(channel),
		ReadInboxMaxID:  channel.TopMessageID,
		ReadOutboxMaxID: channel.TopMessageID,
	}
	if found {
		member.InviterUserID = existing.InviterUserID
		member.JoinedAt = existing.JoinedAt
		member.LeftAt = existing.LeftAt
		member.AvailableMinID = maxInt(member.AvailableMinID, existing.AvailableMinID)
		member.AvailableMinPts = maxInt(member.AvailableMinPts, existing.AvailableMinPts)
		member.ReadInboxMaxID = maxInt(member.ReadInboxMaxID, existing.ReadInboxMaxID)
		member.ReadOutboxMaxID = maxInt(member.ReadOutboxMaxID, existing.ReadOutboxMaxID)
	}
	return member
}

func (s *ChannelStore) monoforumAdminPreview(ctx context.Context, db sqlcgen.DBTX, viewerUserID int64, mono domain.Channel) (domain.ChannelMember, domain.Channel, bool, error) {
	if viewerUserID == 0 || !mono.Monoforum || mono.LinkedMonoforumID == 0 {
		return domain.ChannelMember{}, domain.Channel{}, false, nil
	}
	parent, parentMember, err := s.getChannelForMember(ctx, db, viewerUserID, mono.LinkedMonoforumID)
	if err != nil {
		if errors.Is(err, domain.ErrChannelPrivate) {
			return domain.ChannelMember{}, domain.Channel{}, false, nil
		}
		return domain.ChannelMember{}, domain.Channel{}, false, err
	}
	if !isChannelAdmin(parentMember) {
		return domain.ChannelMember{}, domain.Channel{}, false, nil
	}
	return syntheticMonoforumAdminMember(mono, parentMember), parent, true, nil
}

func syntheticMonoforumAdminMember(mono domain.Channel, parentMember domain.ChannelMember) domain.ChannelMember {
	member := parentMember
	member.ChannelID = mono.ID
	member.Status = domain.ChannelMemberActive
	if mono.CreatorUserID == parentMember.UserID {
		member.Role = domain.ChannelRoleCreator
	} else {
		member.Role = domain.ChannelRoleAdmin
	}
	member.AvailableMinID = 0
	member.AvailableMinPts = 0
	member.ReadInboxMaxID = mono.TopMessageID
	member.ReadOutboxMaxID = mono.TopMessageID
	member.UnreadMark = false
	member.SlowmodeLastSendDate = 0
	return member
}

func zeroChannelAdminRights(rights domain.ChannelAdminRights) bool {
	return rights == domain.ChannelAdminRights{}
}

func zeroChannelBannedRights(rights domain.ChannelBannedRights) bool {
	return rights == domain.ChannelBannedRights{}
}

func creatorChannelMember(channelID, userID int64, date int) domain.ChannelMember {
	return domain.ChannelMember{
		ChannelID:   channelID,
		UserID:      userID,
		Role:        domain.ChannelRoleCreator,
		Status:      domain.ChannelMemberActive,
		JoinedAt:    date,
		AdminRights: domain.CreatorChannelAdminRights(),
	}
}

func channelMemberIDs(members []domain.ChannelMember) []int64 {
	out := make([]int64, 0, len(members))
	for _, member := range members {
		if member.UserID != 0 {
			out = append(out, member.UserID)
		}
	}
	return out
}
