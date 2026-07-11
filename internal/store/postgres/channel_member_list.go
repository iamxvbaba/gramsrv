package postgres

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"

	"telesrv/internal/domain"
)

func (s *ChannelStore) GetParticipants(ctx context.Context, viewerUserID, channelID int64, filter domain.ChannelParticipantsFilter, offset, limit int) (domain.ChannelParticipantList, error) {
	channel, viewer, err := s.getChannelForMemberOrLinkedGuest(ctx, s.db, viewerUserID, channelID)
	if err != nil {
		return domain.ChannelParticipantList{}, err
	}
	if offset < 0 {
		offset = 0
	}
	if offset > domain.MaxChannelParticipantsOffset {
		offset = domain.MaxChannelParticipantsOffset
	}
	if limit <= 0 || limit > domain.MaxChannelParticipantsLimit {
		limit = domain.MaxChannelParticipantsLimit
	}
	count := channel.ParticipantsCount
	// 广播频道订阅者列表仅管理员可枚举（与隐藏成员同一门控）：非管理员只拿到计数，
	// admins filter 仍放行（消息管理员徽章数据源），bots 返回空。
	if channel.MembersListAdminOnly() && !isChannelAdmin(viewer) {
		switch filter.Kind {
		case domain.ChannelParticipantsAdmins:
		case domain.ChannelParticipantsBots:
			return domain.ChannelParticipantList{Channel: channel, Count: 0}, nil
		default:
			return domain.ChannelParticipantList{Channel: channel, Count: channel.ParticipantsCount}, nil
		}
	}
	where := []string{"m.channel_id = $1"}
	args := []any{channelID}
	joinUsers := false
	query := strings.TrimSpace(filter.Query)
	switch filter.Kind {
	case "", domain.ChannelParticipantsRecent, domain.ChannelParticipantsContacts, domain.ChannelParticipantsMentions:
		where = append(where, "m.status = 'active'")
	case domain.ChannelParticipantsAdmins:
		// Layer 225 起 admins filter 同时是消息徽章数据源：客户端把整个返回
		// （含 rank）灌进 badge 缓存，因此带成员 Tag 的普通成员也必须返回。
		where = append(where, "m.status = 'active'", "(m.role IN ('creator','admin') OR m.rank <> '')")
		count = 0
	case domain.ChannelParticipantsKicked:
		count = channel.KickedCount
		if !isChannelAdmin(viewer) {
			return domain.ChannelParticipantList{Channel: channel, Count: channel.KickedCount}, nil
		}
		where = append(where, "(m.status = 'kicked' OR (m.banned_rights->>'ViewMessages')::boolean IS TRUE)")
	case domain.ChannelParticipantsBanned:
		count = channel.BannedCount
		if !isChannelAdmin(viewer) {
			return domain.ChannelParticipantList{Channel: channel, Count: channel.BannedCount}, nil
		}
		where = append(where, "m.status <> 'kicked'", `(
    m.status = 'banned' OR
    (m.banned_rights->>'SendMessages')::boolean IS TRUE OR
    (m.banned_rights->>'SendMedia')::boolean IS TRUE OR
    (m.banned_rights->>'SendStickers')::boolean IS TRUE OR
    (m.banned_rights->>'SendGifs')::boolean IS TRUE OR
    (m.banned_rights->>'SendGames')::boolean IS TRUE OR
    (m.banned_rights->>'SendInline')::boolean IS TRUE OR
    (m.banned_rights->>'EmbedLinks')::boolean IS TRUE OR
    (m.banned_rights->>'SendPolls')::boolean IS TRUE OR
    (m.banned_rights->>'ChangeInfo')::boolean IS TRUE OR
    (m.banned_rights->>'InviteUsers')::boolean IS TRUE OR
    (m.banned_rights->>'PinMessages')::boolean IS TRUE OR
    (m.banned_rights->>'ManageTopics')::boolean IS TRUE OR
    (m.banned_rights->>'SendPhotos')::boolean IS TRUE OR
    (m.banned_rights->>'SendVideos')::boolean IS TRUE OR
    (m.banned_rights->>'SendRoundvideos')::boolean IS TRUE OR
    (m.banned_rights->>'SendAudios')::boolean IS TRUE OR
    (m.banned_rights->>'SendVoices')::boolean IS TRUE OR
    (m.banned_rights->>'SendDocs')::boolean IS TRUE OR
    (m.banned_rights->>'SendPlain')::boolean IS TRUE OR
    (m.banned_rights->>'EditRank')::boolean IS TRUE OR
    (m.banned_rights->>'SendReactions')::boolean IS TRUE
)`)
	case domain.ChannelParticipantsSearch:
		where = append(where, "m.status = 'active'")
		count = 0
	case domain.ChannelParticipantsBots:
		return domain.ChannelParticipantList{Channel: channel}, nil
	default:
		where = append(where, "m.status = 'active'")
	}
	if !isChannelAdmin(viewer) {
		where = append(where, "NOT (m.role IN ('creator','admin') AND COALESCE((m.admin_rights->>'Anonymous')::boolean, false))")
		if count == channel.ParticipantsCount {
			hiddenCount, err := countHiddenAnonymousChannelAdmins(ctx, s.db, channelID)
			if err != nil {
				return domain.ChannelParticipantList{}, err
			}
			count -= hiddenCount
			if count < 0 {
				count = 0
			}
		}
	}
	if query != "" {
		joinUsers = true
		count = 0
		args = append(args, "%"+strings.ToLower(query)+"%")
		placeholder := fmt.Sprintf("$%d", len(args))
		where = append(where, fmt.Sprintf(`(
    lower(COALESCE(u.first_name, '')) LIKE %s OR
    lower(COALESCE(u.last_name, '')) LIKE %s OR
    lower(COALESCE(u.username, '')) LIKE %s OR
    COALESCE(u.phone, '') LIKE %s OR
    m.user_id::text LIKE %s
)`, placeholder, placeholder, placeholder, placeholder, placeholder))
	}
	args = append(args, offset, limit)
	offsetArg := fmt.Sprintf("$%d", len(args)-1)
	limitArg := fmt.Sprintf("$%d", len(args))
	from := "FROM channel_members m"
	if joinUsers {
		from += " JOIN users u ON u.id = m.user_id"
	}
	rows, err := s.db.Query(ctx, `
SELECT channel_id, user_id, inviter_user_id, role, status, joined_at, left_at, admin_rights::text, banned_rights::text,
       rank, available_min_id, available_min_pts, read_inbox_max_id, read_outbox_max_id, unread_mark, slowmode_last_send_date
`+from+`
WHERE `+strings.Join(where, " AND ")+`
ORDER BY CASE role WHEN 'creator' THEN 0 WHEN 'admin' THEN 1 ELSE 2 END, user_id
OFFSET `+offsetArg+` LIMIT `+limitArg, args...)
	if err != nil {
		return domain.ChannelParticipantList{}, fmt.Errorf("list channel participants: %w", err)
	}
	defer rows.Close()
	out := domain.ChannelParticipantList{Channel: channel, Count: count}
	for rows.Next() {
		member, err := scanChannelMember(rows)
		if err != nil {
			return domain.ChannelParticipantList{}, err
		}
		out.Participants = append(out.Participants, member)
	}
	if err := rows.Err(); err != nil {
		return domain.ChannelParticipantList{}, err
	}
	if out.Count == 0 {
		out.Count = len(out.Participants)
	}
	return out, nil
}

func countHiddenAnonymousChannelAdmins(ctx context.Context, db interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, channelID int64) (int, error) {
	var count int
	if err := db.QueryRow(ctx, `
SELECT COUNT(*)::int
FROM channel_members
WHERE channel_id = $1
  AND status = 'active'
  AND role IN ('creator','admin')
  AND COALESCE((admin_rights->>'Anonymous')::boolean, false)`, channelID).Scan(&count); err != nil {
		return 0, fmt.Errorf("count hidden anonymous channel admins: %w", err)
	}
	return count, nil
}

func (s *ChannelStore) GetParticipant(ctx context.Context, viewerUserID, channelID, participantUserID int64) (domain.ChannelMember, error) {
	_, viewer, err := s.getChannelForMemberOrLinkedGuest(ctx, s.db, viewerUserID, channelID)
	if err != nil {
		return domain.ChannelMember{}, err
	}
	if viewerUserID == participantUserID && viewer.Guest {
		return viewer, nil
	}
	return s.getChannelMember(ctx, s.db, channelID, participantUserID)
}

func (s *ChannelStore) ListActiveChannelMemberIDs(ctx context.Context, viewerUserID, channelID int64, limit int) ([]int64, error) {
	if _, _, err := s.getChannelForMember(ctx, s.db, viewerUserID, channelID); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > domain.MaxChannelRealtimeFanout {
		limit = domain.MaxChannelRealtimeFanout
	}
	rows, err := s.db.Query(ctx, `SELECT user_id FROM channel_members WHERE channel_id = $1 AND status = 'active' ORDER BY user_id LIMIT $2`, channelID, limit)
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

func (s *ChannelStore) ListActiveChannelMembers(ctx context.Context, viewerUserID, channelID int64, limit int) (domain.Channel, domain.ChannelMember, []domain.ChannelMember, error) {
	channel, viewer, err := s.getChannelForMemberOrLinkedGuest(ctx, s.db, viewerUserID, channelID)
	if err != nil {
		return domain.Channel{}, domain.ChannelMember{}, nil, err
	}
	if limit <= 0 || limit > domain.MaxSynchronousChannelDialogFanout {
		limit = domain.MaxSynchronousChannelDialogFanout
	}
	rows, err := s.db.Query(ctx, `
SELECT channel_id, user_id, inviter_user_id, role, status, joined_at, left_at, admin_rights::text, banned_rights::text,
       rank, available_min_id, available_min_pts, read_inbox_max_id, read_outbox_max_id, unread_mark, slowmode_last_send_date
FROM channel_members
WHERE channel_id = $1 AND status = 'active'
ORDER BY user_id
LIMIT $2`, channelID, limit)
	if err != nil {
		return domain.Channel{}, domain.ChannelMember{}, nil, fmt.Errorf("list active channel members: %w", err)
	}
	defer rows.Close()
	out := make([]domain.ChannelMember, 0, minInt(limit, channel.ParticipantsCount))
	for rows.Next() {
		member, err := scanChannelMember(rows)
		if err != nil {
			return domain.Channel{}, domain.ChannelMember{}, nil, err
		}
		out = append(out, member)
	}
	if err := rows.Err(); err != nil {
		return domain.Channel{}, domain.ChannelMember{}, nil, err
	}
	return channel, viewer, out, nil
}

func (s *ChannelStore) ListActiveChannelBotMembers(ctx context.Context, viewerUserID, channelID int64, offset, limit int) (domain.ChannelParticipantList, error) {
	channel, viewer, err := s.getChannelForMemberOrLinkedGuest(ctx, s.db, viewerUserID, channelID)
	if err != nil {
		return domain.ChannelParticipantList{}, err
	}
	if channel.ParticipantsHidden && !isChannelAdmin(viewer) {
		return domain.ChannelParticipantList{Channel: channel, Count: 0}, nil
	}
	if offset < 0 {
		offset = 0
	}
	if offset > domain.MaxChannelParticipantsOffset {
		offset = domain.MaxChannelParticipantsOffset
	}
	if limit <= 0 || limit > domain.MaxChannelParticipantsLimit {
		limit = domain.MaxChannelParticipantsLimit
	}
	rows, err := s.db.Query(ctx, `
SELECT m.channel_id, m.user_id, m.inviter_user_id, m.role, m.status, m.joined_at, m.left_at,
       m.admin_rights::text, m.banned_rights::text, m.rank, m.available_min_id, m.available_min_pts,
       m.read_inbox_max_id, m.read_outbox_max_id, m.unread_mark, m.slowmode_last_send_date,
       COUNT(*) OVER()::int
FROM bots b
JOIN channel_members m ON m.user_id = b.bot_user_id
WHERE m.channel_id = $1 AND m.status = 'active'
ORDER BY m.user_id
OFFSET $2 LIMIT $3`, channelID, offset, limit)
	if err != nil {
		return domain.ChannelParticipantList{}, fmt.Errorf("list active channel bot members: %w", err)
	}
	defer rows.Close()
	out := domain.ChannelParticipantList{Channel: channel}
	for rows.Next() {
		member, count, err := scanChannelMemberWithCount(rows)
		if err != nil {
			return domain.ChannelParticipantList{}, err
		}
		out.Participants = append(out.Participants, member)
		out.Count = count
	}
	if err := rows.Err(); err != nil {
		return domain.ChannelParticipantList{}, err
	}
	return out, nil
}

func (s *ChannelStore) ListActiveChannelBotMemberIDs(ctx context.Context, viewerUserID, channelID int64, limit int) ([]int64, error) {
	if _, _, err := s.getChannelForMemberOrLinkedGuest(ctx, s.db, viewerUserID, channelID); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > domain.MaxSynchronousChannelDialogFanout {
		limit = domain.MaxSynchronousChannelDialogFanout
	}
	rows, err := s.db.Query(ctx, `
SELECT m.user_id
FROM bots b
JOIN channel_members m ON m.user_id = b.bot_user_id
WHERE m.channel_id = $1 AND m.status = 'active'
ORDER BY m.user_id
LIMIT $2`, channelID, limit)
	if err != nil {
		return nil, fmt.Errorf("list active channel bot member ids: %w", err)
	}
	defer rows.Close()
	out := make([]int64, 0, minInt(limit, domain.MaxChannelParticipantsLimit))
	for rows.Next() {
		var userID int64
		if err := rows.Scan(&userID); err != nil {
			return nil, err
		}
		out = append(out, userID)
	}
	return out, rows.Err()
}

func (s *ChannelStore) FilterActiveChannelMemberIDs(ctx context.Context, channelID int64, userIDs []int64) ([]int64, error) {
	if channelID == 0 || len(userIDs) == 0 {
		return nil, nil
	}
	candidates := uniqueChannelUserIDs(userIDs, 0)
	if len(candidates) == 0 {
		return nil, nil
	}
	out := make([]int64, 0, len(candidates))
	for start := 0; start < len(candidates); start += channelMemberFilterBatch {
		end := start + channelMemberFilterBatch
		if end > len(candidates) {
			end = len(candidates)
		}
		rows, err := s.db.Query(ctx, `
SELECT user_id
FROM channel_members
WHERE channel_id = $1
  AND user_id = ANY($2::bigint[])
  AND status = 'active'
ORDER BY user_id`, channelID, candidates[start:end])
		if err != nil {
			return nil, fmt.Errorf("filter active channel members: %w", err)
		}
		for rows.Next() {
			var userID int64
			if err := rows.Scan(&userID); err != nil {
				rows.Close()
				return nil, err
			}
			out = append(out, userID)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out, nil
}
