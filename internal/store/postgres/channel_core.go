package postgres

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"golang.org/x/sync/errgroup"
	"sort"
	"strings"
	"telesrv/internal/domain"
	"telesrv/internal/store/postgres/sqlcgen"
	"time"
)

// channelIDAtLeastAllocator 是 Redis 分配器的撞键自愈扩展：一次把计数器
// 顶到 floor 之上，避免按 1 步进追赶大空洞。
type channelIDAtLeastAllocator interface {
	NextChannelIDAtLeast(ctx context.Context, floor int64) (int64, error)
}

// allocateFreshChannelID 分配未被占用的 channel id。计数器可能落后于
// channels 表真实最大 id（Redis 快照回退、或测试 fallback 分配器绕过
// Redis 直写同一库），盲用会撞主键且可能污染后续 channel message id
// 分配；这里先点查预检，撞到就把分配器对账到表内最大 id 再取。
func (s *ChannelStore) allocateFreshChannelID(ctx context.Context) (int64, error) {
	const maxAttempts = 4
	channelID, err := s.ids.NextChannelID(ctx)
	if err != nil {
		return 0, fmt.Errorf("allocate channel id: %w", err)
	}
	for attempt := 0; attempt < maxAttempts; attempt++ {
		var exists bool
		if err := s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM channels WHERE id = $1)`, channelID).Scan(&exists); err != nil {
			return 0, fmt.Errorf("probe channel id %d: %w", channelID, err)
		}
		if !exists {
			return channelID, nil
		}
		var maxID int64
		if err := s.db.QueryRow(ctx, `SELECT COALESCE(MAX(id), 0) FROM channels`).Scan(&maxID); err != nil {
			return 0, fmt.Errorf("load max channel id: %w", err)
		}
		if atLeast, ok := s.ids.(channelIDAtLeastAllocator); ok {
			channelID, err = atLeast.NextChannelIDAtLeast(ctx, maxID)
		} else {
			channelID, err = s.ids.NextChannelID(ctx)
		}
		if err != nil {
			return 0, fmt.Errorf("re-allocate channel id past %d: %w", maxID, err)
		}
	}
	return 0, fmt.Errorf("allocate channel id: counter still colliding after %d attempts (id %d)", maxAttempts, channelID)
}

func (s *ChannelStore) CreateChannel(ctx context.Context, req domain.CreateChannelRequest) (domain.CreateChannelResult, error) {
	if req.CreatorUserID == 0 || strings.TrimSpace(req.Title) == "" {
		return domain.CreateChannelResult{}, domain.ErrChannelInvalid
	}
	if !req.Broadcast && !req.Megagroup {
		req.Broadcast = true
	}
	beginner, ok := s.db.(txBeginner)
	if !ok {
		return domain.CreateChannelResult{}, fmt.Errorf("create channel: db does not support transactions")
	}
	channelID, err := s.allocateFreshChannelID(ctx)
	if err != nil {
		return domain.CreateChannelResult{}, err
	}
	accessHash, err := randomChannelAccessHash()
	if err != nil {
		return domain.CreateChannelResult{}, err
	}
	inviteID, err := randomPositiveInt64()
	if err != nil {
		return domain.CreateChannelResult{}, err
	}
	inviteHash, err := randomInviteHash()
	if err != nil {
		return domain.CreateChannelResult{}, err
	}
	date := req.Date
	if date == 0 {
		date = nowUnix()
	}
	members := []domain.ChannelMember{creatorChannelMember(channelID, req.CreatorUserID, date)}
	for _, userID := range uniqueChannelUserIDs(req.MemberUserIDs, req.CreatorUserID) {
		members = append(members, domain.ChannelMember{
			ChannelID:     channelID,
			UserID:        userID,
			InviterUserID: req.CreatorUserID,
			Role:          domain.ChannelRoleMember,
			Status:        domain.ChannelMemberActive,
			JoinedAt:      date,
		})
	}

	tx, err := beginner.Begin(ctx)
	if err != nil {
		return domain.CreateChannelResult{}, fmt.Errorf("begin create channel: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()

	msgID, err := s.msgIDs.NextChannelMessageID(ctx, channelID)
	if err != nil {
		return domain.CreateChannelResult{}, fmt.Errorf("allocate channel message id: %w", err)
	}
	pts := 1
	channel := domain.Channel{
		ID:                channelID,
		AccessHash:        accessHash,
		CreatorUserID:     req.CreatorUserID,
		Title:             strings.TrimSpace(req.Title),
		About:             req.About,
		Broadcast:         req.Broadcast,
		Megagroup:         req.Megagroup,
		Forum:             req.Forum,
		ForumTabs:         req.ForumTabs,
		ParticipantsCount: len(members),
		AdminsCount:       1,
		HasLink:           true,
		TopMessageID:      msgID,
		Pts:               pts,
		TTLPeriod:         req.TTLPeriod,
		Date:              date,
	}
	msg := domain.ChannelMessage{
		ChannelID:    channelID,
		ID:           msgID,
		SenderUserID: req.CreatorUserID,
		From:         domain.Peer{Type: domain.PeerTypeUser, ID: req.CreatorUserID},
		Date:         date,
		Post:         channel.Broadcast,
		Action:       &domain.ChannelMessageAction{Type: domain.ChannelActionCreate, Title: channel.Title},
		Pts:          pts,
	}
	event := domain.ChannelUpdateEvent{
		ChannelID:    channelID,
		Type:         domain.ChannelUpdateNewMessage,
		Pts:          pts,
		PtsCount:     1,
		Date:         date,
		Message:      msg,
		SenderUserID: req.CreatorUserID,
	}
	if err := insertChannelTx(ctx, tx, channel); err != nil {
		return domain.CreateChannelResult{}, err
	}
	for _, member := range members {
		if err := upsertChannelMemberTx(ctx, tx, channel, member); err != nil {
			return domain.CreateChannelResult{}, err
		}
	}
	if err := insertChannelMessageTx(ctx, tx, msg); err != nil {
		return domain.CreateChannelResult{}, err
	}
	if err := insertChannelEventTx(ctx, tx, event); err != nil {
		return domain.CreateChannelResult{}, err
	}
	for _, member := range members {
		readMax := 0
		if member.UserID == req.CreatorUserID {
			readMax = msgID
		}
		if err := upsertChannelDialogTx(ctx, tx, member.UserID, channel, msg, readMax, 0); err != nil {
			return domain.CreateChannelResult{}, err
		}
	}
	if err := insertChannelInviteTx(ctx, tx, domain.ChannelInvite{
		ChannelID:   channelID,
		InviteID:    inviteID,
		Hash:        inviteHash,
		AdminUserID: req.CreatorUserID,
		Permanent:   true,
		Date:        date,
	}); err != nil {
		return domain.CreateChannelResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.CreateChannelResult{}, fmt.Errorf("commit create channel: %w", err)
	}
	committed = true
	return domain.CreateChannelResult{
		Channel:    channel,
		Members:    append([]domain.ChannelMember(nil), members...),
		Message:    msg,
		Event:      event,
		Recipients: channelMemberIDs(members),
	}, nil
}

func (s *ChannelStore) GetChannel(ctx context.Context, viewerUserID, channelID int64) (domain.ChannelView, error) {
	channel, member, preview, err := s.getChannelForViewer(ctx, s.db, viewerUserID, channelID)
	if err != nil {
		return domain.ChannelView{}, err
	}
	if preview {
		selfBoosts, err := s.countActiveUserBoostsForPeer(ctx, s.db, viewerUserID, domain.Peer{Type: domain.PeerTypeChannel, ID: channel.ID}, nowUnix())
		if err != nil {
			return domain.ChannelView{}, err
		}
		return domain.ChannelView{
			Channel:           channel,
			Self:              member,
			Dialog:            previewChannelDialog(viewerUserID, channel, member),
			SelfBoostsApplied: selfBoosts,
		}, nil
	}
	// getChannelDialog 与 countActiveUserBoostsForPeer 互不依赖，并发执行省一次串行往返。
	// 两者都走 s.db（pgxpool，并发安全，各取独立连接），各写自己的返回变量。
	var (
		dialog         domain.ChannelDialog
		selfBoosts     int
		exportedInvite *domain.ChannelInvite
	)
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error {
		var derr error
		dialog, derr = s.getChannelDialog(gctx, s.db, viewerUserID, channel)
		return derr
	})
	g.Go(func() error {
		var berr error
		selfBoosts, berr = s.countActiveUserBoostsForPeer(gctx, s.db, viewerUserID, domain.Peer{Type: domain.PeerTypeChannel, ID: channel.ID}, nowUnix())
		return berr
	})
	if canExportChannelInvite(member) {
		g.Go(func() error {
			invite, found, ierr := s.getPermanentInviteForAdmin(gctx, s.db, channel.ID, viewerUserID)
			if ierr != nil {
				return ierr
			}
			if found {
				exportedInvite = &invite
			}
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return domain.ChannelView{}, err
	}
	return domain.ChannelView{Channel: channel, Self: member, Dialog: dialog, SelfBoostsApplied: selfBoosts, ExportedInvite: exportedInvite}, nil
}

// GetLinkedDiscussionChannel projects a private discussion peer through the
// viewer's active membership in the source broadcast channel. This is a
// peer-discovery boundary only; it never creates discussion-group membership.
func (s *ChannelStore) GetLinkedDiscussionChannel(ctx context.Context, viewerUserID, sourceChannelID int64) (domain.ChannelView, error) {
	source, _, err := s.getChannelForMember(ctx, s.db, viewerUserID, sourceChannelID)
	if err != nil {
		return domain.ChannelView{}, err
	}
	if !source.Broadcast || source.LinkedChatID == 0 {
		return domain.ChannelView{}, domain.ErrChannelInvalid
	}
	linked, err := s.channelByID(ctx, s.db, source.LinkedChatID)
	if err != nil {
		return domain.ChannelView{}, err
	}
	if !linked.Megagroup || linked.Broadcast {
		return domain.ChannelView{}, domain.ErrChannelInvalid
	}
	self, guest, guestErr := s.getLinkedDiscussionGuest(ctx, s.db, viewerUserID, linked)
	if guestErr != nil {
		return domain.ChannelView{}, guestErr
	}
	if !guest {
		if member, memberErr := s.getChannelMember(ctx, s.db, linked.ID, viewerUserID); memberErr == nil {
			self = member
		} else {
			return domain.ChannelView{}, memberErr
		}
	}
	return domain.ChannelView{Channel: linked, Self: self}, nil
}

// ResolveChannel 是 GetChannel 的轻量版：只做访问校验并返回 Channel(含 access_hash)+Self，
// 跳过 dialog top message / 读态 / boost 求和这 3 条额外 PG 查询。供 inputPeerFor 等只需
// access_hash / 频道标志的纯解析路径用——它们此前为拿一个 access_hash 付了完整 4 查询投影。
// 访问语义与 GetChannel 完全一致（私有非成员 ErrChannelPrivate、公开预览成员）。
func (s *ChannelStore) ResolveChannel(ctx context.Context, viewerUserID, channelID int64) (domain.ChannelView, error) {
	channel, member, preview, err := s.getChannelForViewer(ctx, s.db, viewerUserID, channelID)
	if err != nil {
		return domain.ChannelView{}, err
	}
	view := domain.ChannelView{Channel: channel, Self: member}
	if preview {
		// previewChannelDialog 是纯内存构造（无额外查询），保持与 GetChannel 预览态的 Dialog 一致。
		view.Dialog = previewChannelDialog(viewerUserID, channel, member)
	}
	return view, nil
}

func (s *ChannelStore) GetChannels(ctx context.Context, viewerUserID int64, channelIDs []int64) ([]domain.ChannelView, error) {
	if viewerUserID == 0 || len(channelIDs) == 0 {
		return nil, nil
	}
	ids := uniqueNonZeroInt64s(channelIDs...)
	if len(ids) == 0 {
		return nil, nil
	}
	views := make(map[int64]domain.ChannelView, len(ids))
	previewMembers := make(map[int64]domain.ChannelMember)
	activeIDs := make([]int64, 0, len(ids))

	rows, err := s.db.Query(ctx, `
SELECT `+channelColumns+`,
       m.channel_id, m.user_id, m.inviter_user_id, m.role, m.status, m.joined_at, m.left_at,
       m.admin_rights::text, m.banned_rights::text, m.rank, m.available_min_id, m.available_min_pts,
       m.read_inbox_max_id, m.read_outbox_max_id, m.unread_mark, m.slowmode_last_send_date
FROM channels c
JOIN channel_members m ON m.channel_id = c.id AND m.user_id = $1
WHERE c.id = ANY($2::bigint[]) AND NOT c.deleted`, viewerUserID, ids)
	if err != nil {
		return nil, fmt.Errorf("list channels for viewer: %w", err)
	}
	for rows.Next() {
		channel, member, err := scanChannelWithMember(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		if err := validateChannelMemberVisible(member); err != nil {
			if errors.Is(err, domain.ErrChannelUserBanned) {
				views[channel.ID] = domain.ChannelView{Channel: channel, Self: member, Forbidden: true}
				continue
			}
			if errors.Is(err, domain.ErrChannelPrivate) {
				previewMembers[channel.ID] = member
				continue
			}
			rows.Close()
			return nil, err
		}
		views[channel.ID] = domain.ChannelView{Channel: channel, Self: member}
		activeIDs = append(activeIDs, channel.ID)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("list channels for viewer: %w", err)
	}
	rows.Close()

	dialogs, err := s.getChannelDialogs(ctx, s.db, viewerUserID, activeIDs)
	if err != nil {
		return nil, err
	}
	selfBoostsByChannelID, err := s.countActiveUserBoostsForChannels(ctx, s.db, viewerUserID, activeIDs, nowUnix())
	if err != nil {
		return nil, err
	}
	for id, dialog := range dialogs {
		view := views[id]
		view.Dialog = dialog
		view.SelfBoostsApplied = selfBoostsByChannelID[id]
		views[id] = view
	}

	remaining := make([]int64, 0, len(ids)-len(views))
	for _, id := range ids {
		if _, ok := views[id]; ok {
			continue
		}
		remaining = append(remaining, id)
	}
	channels, err := listChannelsByIDs(ctx, s.db, remaining)
	if err != nil {
		return nil, err
	}
	for _, channel := range channels {
		if member, _, ok, err := s.monoforumAdminPreview(ctx, s.db, viewerUserID, channel); err != nil {
			return nil, err
		} else if ok {
			views[channel.ID] = domain.ChannelView{
				Channel:           channel,
				Self:              member,
				Dialog:            previewChannelDialog(viewerUserID, channel, member),
				SelfBoostsApplied: 0,
			}
			continue
		}
		if !publicPreviewableChannel(channel) {
			continue
		}
		existing, found := previewMembers[channel.ID]
		member := publicPreviewMember(channel, viewerUserID, existing, found)
		views[channel.ID] = domain.ChannelView{
			Channel:           channel,
			Self:              member,
			Dialog:            previewChannelDialog(viewerUserID, channel, member),
			SelfBoostsApplied: 0,
		}
	}

	out := make([]domain.ChannelView, 0, len(views))
	for _, id := range ids {
		if view, ok := views[id]; ok {
			out = append(out, view)
		}
	}
	return out, nil
}

func (s *ChannelStore) GetChannelByID(ctx context.Context, channelID int64) (domain.Channel, error) {
	if channelID == 0 {
		return domain.Channel{}, domain.ErrChannelInvalid
	}
	return s.channelByID(ctx, s.db, channelID)
}

func insertChannelTx(ctx context.Context, tx pgx.Tx, ch domain.Channel) error {
	rights, err := marshalJSON(ch.DefaultBannedRights, "{}")
	if err != nil {
		return err
	}
	reactions, err := marshalJSON(ch.ReactionPolicy, "{}")
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
INSERT INTO channels (
    id, access_hash, creator_user_id, title, about, username, verified, broadcast, megagroup, forum, forum_tabs,
    autotranslation, restricted_sponsored, broadcast_messages_allowed, send_paid_messages_stars,
    noforwards, join_to_send, join_request, signatures, pre_history_hidden, participants_hidden, antispam, linked_chat_id, slowmode_seconds, boosts_unrestrict, default_banned_rights, available_reactions,
    color_set, color, color_background_emoji_id, profile_color_set, profile_color, profile_color_background_emoji_id, emoji_status_document_id, emoji_status_until,
    participants_count, admins_count, kicked_count, banned_count, top_message_id, pinned_message_id, pts, ttl_period, date, deleted, monoforum, linked_monoforum_id
) VALUES ($1,$2,$3,$4,$5,NULLIF($6,''),$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25,$26,$27,$28,$29,$30,$31,$32,$33,$34,$35,$36,$37,$38,$39,$40,$41,$42,$43,$44,$45,$46,$47)`,
		ch.ID, ch.AccessHash, ch.CreatorUserID, ch.Title, ch.About, ch.Username, ch.Verified, ch.Broadcast, ch.Megagroup, ch.Forum,
		ch.ForumTabs, ch.Autotranslation, ch.RestrictedSponsored, ch.BroadcastMessagesAllowed, ch.SendPaidMessagesStars, ch.NoForwards, ch.JoinToSend, ch.JoinRequest, ch.Signatures, ch.PreHistoryHidden, ch.ParticipantsHidden, ch.AntiSpam, ch.LinkedChatID, ch.SlowmodeSeconds, ch.BoostsUnrestrict, rights, reactions,
		ch.Color.HasColor, ch.Color.Color, ch.Color.BackgroundEmojiID, ch.ProfileColor.HasColor, ch.ProfileColor.Color, ch.ProfileColor.BackgroundEmojiID, ch.EmojiStatus.DocumentID, ch.EmojiStatus.Until,
		ch.ParticipantsCount, ch.AdminsCount,
		ch.KickedCount, ch.BannedCount, ch.TopMessageID, ch.PinnedMessageID, ch.Pts, ch.TTLPeriod, ch.Date, ch.Deleted, ch.Monoforum, ch.LinkedMonoforumID); err != nil {
		return fmt.Errorf("insert channel: %w", err)
	}
	return nil
}

func scanChannel(row rowScanner) (domain.Channel, error) {
	var ch domain.Channel
	var rights, reactionPolicy string
	var wallpaper *string
	if err := row.Scan(channelScanDest(&ch, &rights, &reactionPolicy, &wallpaper)...); err != nil {
		return domain.Channel{}, err
	}
	finishChannelScan(&ch, rights, reactionPolicy, wallpaper)
	return ch, nil
}

func channelScanDest(ch *domain.Channel, rights, reactionPolicy *string, wallpaper **string) []any {
	return []any{
		&ch.ID, &ch.AccessHash, &ch.CreatorUserID, &ch.Title, &ch.About, &ch.Username, &ch.Verified,
		&ch.Broadcast, &ch.Megagroup, &ch.Forum, &ch.ForumTabs, &ch.Autotranslation, &ch.RestrictedSponsored, &ch.BroadcastMessagesAllowed, &ch.SendPaidMessagesStars, &ch.NoForwards, &ch.JoinToSend, &ch.JoinRequest, &ch.Signatures, &ch.PreHistoryHidden, &ch.ParticipantsHidden, &ch.AntiSpam, &ch.HasLink, &ch.LinkedChatID, &ch.Monoforum, &ch.LinkedMonoforumID, &ch.SlowmodeSeconds, &ch.BoostsUnrestrict, rights,
		reactionPolicy, &ch.Color.HasColor, &ch.Color.Color, &ch.Color.BackgroundEmojiID, &ch.ProfileColor.HasColor, &ch.ProfileColor.Color, &ch.ProfileColor.BackgroundEmojiID, &ch.EmojiStatus.DocumentID, &ch.EmojiStatus.Until,
		wallpaper, &ch.ParticipantsCount, &ch.AdminsCount, &ch.KickedCount, &ch.BannedCount, &ch.TopMessageID,
		&ch.PinnedMessageID, &ch.Pts, &ch.TTLPeriod, &ch.Date, &ch.Deleted,
		&ch.PhotoID, &ch.PhotoDCID, &ch.PhotoStripped,
		&ch.ActiveCallID, &ch.ActiveCallAccessHash, &ch.ActiveCallNotEmpty,
	}
}

func finishChannelScan(ch *domain.Channel, rights, reactionPolicy string, wallpaper *string) {
	_ = json.Unmarshal([]byte(rights), &ch.DefaultBannedRights)
	_ = json.Unmarshal([]byte(reactionPolicy), &ch.ReactionPolicy)
	if wallpaper != nil {
		ch.Wallpaper = decodeJSONPtr[domain.Wallpaper](*wallpaper)
	}
}

func publicPreviewableChannel(channel domain.Channel) bool {
	return !channel.Deleted &&
		(channel.Broadcast || channel.Megagroup) &&
		strings.TrimSpace(channel.Username) != ""
}

func refreshChannelCountsTx(ctx context.Context, tx pgx.Tx, channel domain.Channel) (domain.Channel, error) {
	var participants, admins, kicked, banned int
	rows, err := tx.Query(ctx, `
SELECT channel_id, user_id, inviter_user_id, role, status, joined_at, left_at, admin_rights::text, banned_rights::text,
       rank, available_min_id, available_min_pts, read_inbox_max_id, read_outbox_max_id, unread_mark, slowmode_last_send_date
FROM channel_members
WHERE channel_id = $1`, channel.ID)
	if err != nil {
		return domain.Channel{}, fmt.Errorf("list channel members for counts: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		member, err := scanChannelMember(rows)
		if err != nil {
			return domain.Channel{}, err
		}
		if member.Status == domain.ChannelMemberKicked {
			kicked++
		}
		if member.Status != domain.ChannelMemberActive {
			continue
		}
		participants++
		if member.Role == domain.ChannelRoleCreator || member.Role == domain.ChannelRoleAdmin {
			admins++
		}
		if !zeroChannelBannedRights(member.BannedRights) {
			banned++
		}
	}
	if err := rows.Err(); err != nil {
		return domain.Channel{}, err
	}
	if _, err := tx.Exec(ctx, `
UPDATE channels
SET participants_count = $2, admins_count = $3, kicked_count = $4, banned_count = $5, updated_at = now()
WHERE id = $1`, channel.ID, participants, admins, kicked, banned); err != nil {
		return domain.Channel{}, fmt.Errorf("refresh channel counts: %w", err)
	}
	channel.ParticipantsCount = participants
	channel.AdminsCount = admins
	channel.KickedCount = kicked
	channel.BannedCount = banned
	return channel, nil
}

func addPeerRef(peer domain.Peer, currentChannelID int64, userRefs, channelRefs map[int64]struct{}) {
	switch peer.Type {
	case domain.PeerTypeUser:
		if peer.ID != 0 {
			userRefs[peer.ID] = struct{}{}
		}
	case domain.PeerTypeChannel:
		if peer.ID != 0 && peer.ID != currentChannelID {
			channelRefs[peer.ID] = struct{}{}
		}
	}
}

func mapKeysInt64(items map[int64]struct{}) []int64 {
	if len(items) == 0 {
		return nil
	}
	out := make([]int64, 0, len(items))
	for id := range items {
		if id != 0 {
			out = append(out, id)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func uniqueChannelUserIDs(ids []int64, exclude int64) []int64 {
	seen := make(map[int64]struct{}, len(ids))
	out := make([]int64, 0, len(ids))
	for _, id := range ids {
		if id == 0 || id == exclude {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

func uniqueNonZeroInt64s(items ...int64) []int64 {
	seen := make(map[int64]struct{}, len(items))
	out := make([]int64, 0, len(items))
	for _, item := range items {
		if item == 0 {
			continue
		}
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		out = append(out, item)
	}
	return out
}

func marshalJSON(v any, empty string) ([]byte, error) {
	if v == nil {
		return []byte(empty), nil
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	if string(raw) == "null" {
		return []byte(empty), nil
	}
	return raw, nil
}

func int64s(ids []int64) []int64 {
	return append([]int64(nil), ids...)
}

// nonNullInt64s 返回非 nil 的 []int64 副本，供写入 NOT NULL bigint[] 列时用——nil 切片会被
// pgx 编码成 SQL NULL 违反 NOT NULL 约束（列的 DEFAULT '{}' 只在列被省略时生效，显式传参不触发）。
// 空列表写成 '{}' 而非 NULL；读路径仍把空数组归一回 nil，双 store 行为一致。
func nonNullInt64s(ids []int64) []int64 {
	if len(ids) == 0 {
		return []int64{}
	}
	return append([]int64(nil), ids...)
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func decodeJSONPtr[T any](raw string) *T {
	if raw == "" || raw == "{}" || raw == "null" {
		return nil
	}
	var out T
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil
	}
	return &out
}

func randomChannelAccessHash() (int64, error) {
	return randomPositiveInt64()
}

func randomPositiveInt64() (int64, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return 0, fmt.Errorf("rand int64: %w", err)
	}
	return int64(binary.LittleEndian.Uint64(b[:]) & ((1 << 63) - 1)), nil
}

func nowUnix() int {
	return int(time.Now().Unix())
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

func isRetryablePostgresTxError(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	return pgErr.Code == "40P01" || pgErr.Code == "40001"
}

type pgChannelIDAllocator struct {
	db sqlcgen.DBTX
}
