package postgres

import (
	"context"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"strconv"
	"strings"
	"telesrv/internal/domain"
	"telesrv/internal/store/postgres/sqlcgen"
)

func (s *ChannelStore) ListChannelHistory(ctx context.Context, viewerUserID int64, filter domain.ChannelHistoryFilter) (domain.ChannelHistory, error) {
	channel, member, _, err := s.getChannelForViewer(ctx, s.db, viewerUserID, filter.ChannelID)
	if err != nil {
		return domain.ChannelHistory{}, err
	}
	limit := filter.Limit
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	// 公共过滤条件（不含 offset 锚点的方向条件，供 add_offset 各模式复用）
	baseArgs := []any{filter.ChannelID}
	base := "channel_id = $1 AND NOT deleted"
	extraChannels := []domain.Channel(nil)
	if channel.Monoforum {
		base += " AND saved_peer_id = 0"
		if channel.LinkedMonoforumID != 0 {
			if parent, parentErr := s.channelByID(ctx, s.db, channel.LinkedMonoforumID); parentErr == nil {
				extraChannels = append(extraChannels, parent)
			} else {
				return domain.ChannelHistory{}, parentErr
			}
		}
	}
	if member.AvailableMinID > 0 {
		baseArgs = append(baseArgs, member.AvailableMinID)
		base += fmt.Sprintf(" AND id > $%d", len(baseArgs))
	}
	if filter.PinnedOnly {
		base += " AND pinned"
	}
	if filter.MusicOnly {
		base += ` AND media->>'kind' = 'document'
AND EXISTS (
  SELECT 1
  FROM jsonb_array_elements(COALESCE(media #> '{document,attributes}', '[]'::jsonb)) AS attr
  WHERE attr->>'kind' = 'audio'
    AND COALESCE((attr->>'voice')::boolean, false) = false
)`
	}
	if filter.Query != "" {
		baseArgs = append(baseArgs, filter.Query)
		base += fmt.Sprintf(" AND body ILIKE '%%' || $%d || '%%'", len(baseArgs))
	}
	if filter.SenderUserID != 0 {
		baseArgs = append(baseArgs, filter.SenderUserID)
		base += fmt.Sprintf(" AND sender_user_id = $%d", len(baseArgs))
	}
	if filter.MinDate > 0 {
		baseArgs = append(baseArgs, filter.MinDate)
		base += fmt.Sprintf(" AND message_date > $%d", len(baseArgs))
	}
	if filter.MaxDate > 0 {
		baseArgs = append(baseArgs, filter.MaxDate)
		base += fmt.Sprintf(" AND message_date < $%d", len(baseArgs))
	}
	if filter.MaxID > 0 {
		baseArgs = append(baseArgs, filter.MaxID)
		base += fmt.Sprintf(" AND id <= $%d", len(baseArgs))
	}
	if filter.MinID > 0 {
		baseArgs = append(baseArgs, filter.MinID)
		base += fmt.Sprintf(" AND id > $%d", len(baseArgs))
	}
	scanList := func(sql string, queryArgs []any) ([]domain.ChannelMessage, error) {
		rows, err := s.db.Query(ctx, sql, queryArgs...)
		if err != nil {
			return nil, fmt.Errorf("list channel history: %w", err)
		}
		defer rows.Close()
		var list []domain.ChannelMessage
		for rows.Next() {
			msg, scanErr := scanChannelMessage(rows)
			if scanErr != nil {
				return nil, scanErr
			}
			list = append(list, msg)
		}
		return list, rows.Err()
	}
	// add_offset 决定加载方向（对齐私聊 ListMessagesByUser）：
	//   >= 0           backward：锚点更旧方向，先跳过 add_offset 条
	//   < 0 且 +limit>0 around：以锚点为中心，向更新取 -add_offset 条 + 向更旧取 limit+add_offset 条
	//   否则           forward：仅锚点更新方向（拉未读消息）
	// store 层二次钳制 add_offset 到 [-100,100]（与私聊 ListMessagesByUser 对齐）：
	// 即便某个 caller 漏在 RPC 层钳制，也不会把客户端巨大值变成大 SQL OFFSET 跳扫。
	addOffset := domain.ClampMessageHistoryAddOffset(filter.AddOffset)
	out := domain.ChannelHistory{Channel: channel, Self: member, Channels: extraChannels}
	hasMoreOlder := false
	// 锚点条件：offset_date 优先按日期、否则按消息 id（对齐私聊）；
	// 二者皆空时向更新方向退化为空、向更旧方向退化为全部（取最新）。
	forwardCond := func(args *[]any) string {
		if filter.OffsetDate > 0 {
			*args = append(*args, filter.OffsetDate)
			return fmt.Sprintf("message_date >= $%d", len(*args))
		}
		if filter.OffsetID > 0 {
			*args = append(*args, filter.OffsetID)
			return fmt.Sprintf("id > $%d", len(*args))
		}
		return "false"
	}
	aroundOlderCond := func(args *[]any) string {
		if filter.OffsetDate > 0 {
			*args = append(*args, filter.OffsetDate)
			return fmt.Sprintf("message_date < $%d", len(*args))
		}
		if filter.OffsetID > 0 {
			*args = append(*args, filter.OffsetID)
			return fmt.Sprintf("id <= $%d", len(*args))
		}
		return "true"
	}
	switch {
	case addOffset < 0 && addOffset+limit > 0:
		// around：以锚点为中心，向更新取 -add_offset 条 + 向更旧（含锚点）取 limit+add_offset 条
		fwdLimit := minInt(-addOffset, limit)
		bwdLimit := maxInt(limit+addOffset, 0)
		fwdArgs := append([]any{}, baseArgs...)
		fwdWhere := forwardCond(&fwdArgs)
		fwdArgs = append(fwdArgs, fwdLimit)
		newer, err := scanList(fmt.Sprintf("SELECT "+channelMessageColumns+" FROM channel_messages WHERE %s AND %s ORDER BY id ASC LIMIT $%d", base, fwdWhere, len(fwdArgs)), fwdArgs)
		if err != nil {
			return domain.ChannelHistory{}, err
		}
		bwdArgs := append([]any{}, baseArgs...)
		bwdWhere := aroundOlderCond(&bwdArgs)
		bwdArgs = append(bwdArgs, bwdLimit+1)
		older, err := scanList(fmt.Sprintf("SELECT "+channelMessageColumns+" FROM channel_messages WHERE %s AND %s ORDER BY id DESC LIMIT $%d", base, bwdWhere, len(bwdArgs)), bwdArgs)
		if err != nil {
			return domain.ChannelHistory{}, err
		}
		if len(older) > bwdLimit {
			older = older[:bwdLimit]
			hasMoreOlder = true
		}
		for i := len(newer) - 1; i >= 0; i-- {
			out.Messages = append(out.Messages, newer[i])
		}
		out.Messages = append(out.Messages, older...)
	case addOffset < 0:
		// forward：仅锚点更新方向（拉未读/更新消息）
		fwdArgs := append([]any{}, baseArgs...)
		fwdWhere := forwardCond(&fwdArgs)
		fwdArgs = append(fwdArgs, limit+1)
		newer, err := scanList(fmt.Sprintf("SELECT "+channelMessageColumns+" FROM channel_messages WHERE %s AND %s ORDER BY id ASC LIMIT $%d", base, fwdWhere, len(fwdArgs)), fwdArgs)
		if err != nil {
			return domain.ChannelHistory{}, err
		}
		if len(newer) > limit {
			newer = newer[:limit]
		}
		for i := len(newer) - 1; i >= 0; i-- {
			out.Messages = append(out.Messages, newer[i])
		}
	default:
		// backward：锚点更旧方向（不含锚点），先跳过 add_offset 条
		where := base
		args := append([]any{}, baseArgs...)
		if filter.OffsetDate > 0 {
			args = append(args, filter.OffsetDate)
			where += fmt.Sprintf(" AND message_date < $%d", len(args))
		} else if filter.OffsetID > 0 {
			args = append(args, filter.OffsetID)
			where += fmt.Sprintf(" AND id < $%d", len(args))
		}
		args = append(args, limit+1)
		limIdx := len(args)
		sql := "SELECT " + channelMessageColumns + " FROM channel_messages WHERE " + where + " ORDER BY id DESC"
		if addOffset > 0 {
			args = append(args, addOffset)
			sql += fmt.Sprintf(" OFFSET $%d", len(args))
		}
		sql += fmt.Sprintf(" LIMIT $%d", limIdx)
		older, err := scanList(sql, args)
		if err != nil {
			return domain.ChannelHistory{}, err
		}
		if len(older) > limit {
			older = older[:limit]
			hasMoreOlder = true
		}
		out.Messages = older
	}
	out.Count = len(out.Messages)
	if hasMoreOlder {
		out.Count = len(out.Messages) + 1
	}
	if err := s.populateChannelMessageReplies(ctx, s.db, viewerUserID, channel, out.Messages); err != nil {
		return domain.ChannelHistory{}, err
	}
	if err := s.populateChannelMessagesReactions(ctx, s.db, viewerUserID, []domain.Channel{channel}, out.Messages); err != nil {
		return domain.ChannelHistory{}, err
	}
	return out, nil
}

func (s *ChannelStore) SearchJoinedMessages(ctx context.Context, viewerUserID int64, req domain.ChannelGlobalSearchRequest) (domain.ChannelHistory, error) {
	query := strings.TrimSpace(req.Query)
	if viewerUserID == 0 || (query == "" && !req.MusicOnly) {
		return domain.ChannelHistory{}, domain.ErrChannelInvalid
	}
	limit := req.Limit
	if limit <= 0 || limit > domain.MaxChannelGlobalSearchLimit {
		limit = domain.MaxChannelGlobalSearchLimit
	}
	args := []any{viewerUserID}
	where := `NOT deleted`
	if query != "" {
		args = append(args, "%"+escapeLike(query)+"%")
		where += fmt.Sprintf(`
AND body <> ''
AND body ILIKE $%d ESCAPE '\'`, len(args))
	}
	if req.MusicOnly {
		where += `
AND channel_messages.media->>'kind' = 'document'
AND EXISTS (
  SELECT 1
  FROM jsonb_array_elements(COALESCE(channel_messages.media #> '{document,attributes}', '[]'::jsonb)) AS attr
  WHERE attr->>'kind' = 'audio'
    AND COALESCE((attr->>'voice')::boolean, false) = false
)`
	}
	where += `
AND EXISTS (
  SELECT 1
  FROM channels c
  JOIN channel_members cm ON cm.channel_id = c.id
    AND cm.user_id = $1
    AND cm.status = 'active'
    AND NOT COALESCE((cm.banned_rights->>'ViewMessages')::boolean, false)
  LEFT JOIN channel_dialogs d ON d.channel_id = c.id AND d.user_id = $1
  WHERE c.id = channel_messages.channel_id
    AND NOT c.deleted
    AND (cm.available_min_id <= 0 OR channel_messages.id > cm.available_min_id)`
	if req.BroadcastsOnly {
		where += `
    AND c.broadcast AND NOT c.megagroup`
	}
	if req.GroupsOnly {
		where += `
    AND c.megagroup`
	}
	if req.HasFolderID {
		args = append(args, req.FolderID)
		where += fmt.Sprintf(`
    AND d.folder_id = $%d`, len(args))
	}
	where += `
)`
	if req.MinDate > 0 {
		args = append(args, req.MinDate)
		where += fmt.Sprintf(" AND message_date > $%d", len(args))
	}
	if req.MaxDate > 0 {
		args = append(args, req.MaxDate)
		where += fmt.Sprintf(" AND message_date < $%d", len(args))
	}
	switch {
	case req.OffsetRate > 0 && req.OffsetChannelID > 0 && req.OffsetID > 0:
		args = append(args, req.OffsetRate, req.OffsetChannelID, req.OffsetID)
		n := len(args)
		where += fmt.Sprintf(" AND (message_date < $%d OR (message_date = $%d AND (channel_id < $%d OR (channel_id = $%d AND id < $%d))))", n-2, n-2, n-1, n-1, n)
	case req.OffsetRate > 0:
		args = append(args, req.OffsetRate)
		where += fmt.Sprintf(" AND message_date < $%d", len(args))
	case req.OffsetChannelID > 0 && req.OffsetID > 0:
		args = append(args, req.OffsetChannelID, req.OffsetID)
		n := len(args)
		where += fmt.Sprintf(" AND (channel_id < $%d OR (channel_id = $%d AND id < $%d))", n-1, n-1, n)
	case req.OffsetID > 0:
		args = append(args, req.OffsetID)
		where += fmt.Sprintf(" AND id < $%d", len(args))
	}
	queryLimit := limit + 1
	args = append(args, queryLimit)
	rows, err := s.db.Query(ctx, `
SELECT `+channelMessageColumns+`
FROM channel_messages
WHERE `+where+`
ORDER BY message_date DESC, channel_id DESC, id DESC
LIMIT $`+fmt.Sprint(len(args)), args...)
	if err != nil {
		return domain.ChannelHistory{}, fmt.Errorf("search joined channel messages: %w", err)
	}
	defer rows.Close()
	out := domain.ChannelHistory{}
	channelRefs := make(map[int64]struct{})
	for rows.Next() {
		msg, err := scanChannelMessage(rows)
		if err != nil {
			return domain.ChannelHistory{}, err
		}
		out.Messages = append(out.Messages, msg)
		channelRefs[msg.ChannelID] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return domain.ChannelHistory{}, err
	}
	if len(out.Messages) > limit {
		out.Messages = out.Messages[:limit]
		out.Count = limit + 1
		channelRefs = make(map[int64]struct{}, len(out.Messages))
		for _, msg := range out.Messages {
			channelRefs[msg.ChannelID] = struct{}{}
		}
	} else {
		out.Count = len(out.Messages)
	}
	channels, err := listChannelsByIDs(ctx, s.db, mapKeysInt64(channelRefs))
	if err != nil {
		return domain.ChannelHistory{}, err
	}
	out.Channels = channels
	if err := s.populateChannelMessagesReactions(ctx, s.db, viewerUserID, out.Channels, out.Messages); err != nil {
		return domain.ChannelHistory{}, err
	}
	return out, nil
}

func (s *ChannelStore) GetChannelMessages(ctx context.Context, viewerUserID, channelID int64, ids []int) (domain.ChannelHistory, error) {
	// viewer 口径(非严格 member)：公开频道的非成员可预览读取消息(与 ListChannelHistory 一致)。
	// 否则查看他人资料里设置的公开「个人频道」时，DrKLO 经 channels.getMessages 拉最新一帖会被拒，
	// 资料页个人频道整块不显示。私有频道非成员仍返回 ErrChannelPrivate。
	channel, member, _, err := s.getChannelForViewer(ctx, s.db, viewerUserID, channelID)
	if err != nil {
		return domain.ChannelHistory{}, err
	}
	return s.getChannelMessagesForMember(ctx, viewerUserID, channel, member, ids)
}

func (s *ChannelStore) getChannelMessagesForMember(ctx context.Context, viewerUserID int64, channel domain.Channel, member domain.ChannelMember, ids []int) (domain.ChannelHistory, error) {
	if len(ids) == 0 {
		return domain.ChannelHistory{Channel: channel, Self: member}, nil
	}
	if len(ids) > domain.MaxGetMessageIDs {
		return domain.ChannelHistory{}, domain.ErrChannelInvalid
	}
	id32, _, err := validUniqueChannelMessageIDs(ids)
	if err != nil {
		return domain.ChannelHistory{}, err
	}
	// AvailableMinID 用固定哨兵($3<=0 表示无下限)而非条件追加,使 by-id 补拉恒为
	// 单一 query-shape(2→1),计划可复用。这里安全无副作用:索引由 id=ANY($2) 驱动
	// (Index Cond),哨兵仅作残余 Filter——EXPLAIN 实测仍走 channel_messages_history_idx、
	// 执行不变。注意:这种"OR 哨兵"只对【非排序锚点】的残余过滤安全;ListChannelHistory
	// 的方向/anchor 条件若同样哨兵化会让规划器无法用索引顺序做 LIMIT、退化为全表扫+排序
	// (实测 0.06ms→23ms),故那里【刻意保留】动态 SQL。
	args := []any{channel.ID, id32, member.AvailableMinID}
	where := "channel_id = $1 AND id = ANY($2::int[]) AND NOT deleted AND ($3 <= 0 OR id > $3)"
	rows, err := s.db.Query(ctx, `
SELECT `+channelMessageColumns+`
FROM channel_messages
WHERE `+where+`
ORDER BY id DESC`, args...)
	if err != nil {
		return domain.ChannelHistory{}, fmt.Errorf("get channel messages by ids: %w", err)
	}
	defer rows.Close()
	out := domain.ChannelHistory{Channel: channel, Self: member}
	for rows.Next() {
		msg, err := scanChannelMessage(rows)
		if err != nil {
			return domain.ChannelHistory{}, err
		}
		out.Messages = append(out.Messages, msg)
	}
	if err := rows.Err(); err != nil {
		return domain.ChannelHistory{}, err
	}
	out.Count = len(out.Messages)
	if err := s.populateChannelMessageReplies(ctx, s.db, viewerUserID, channel, out.Messages); err != nil {
		return domain.ChannelHistory{}, err
	}
	if err := s.populateChannelMessagesReactions(ctx, s.db, viewerUserID, []domain.Channel{channel}, out.Messages); err != nil {
		return domain.ChannelHistory{}, err
	}
	return out, nil
}

func (s *ChannelStore) ListStoryMessageForwards(ctx context.Context, req domain.StoryMessageForwardListRequest) (domain.StoryMessageForwardList, error) {
	if req.ViewerUserID == 0 || req.Owner.ID == 0 || req.StoryID <= 0 || req.StoryID > domain.MaxStoryID {
		return domain.StoryMessageForwardList{}, domain.ErrStoryIDInvalid
	}
	if req.Owner.Type != domain.PeerTypeUser && req.Owner.Type != domain.PeerTypeChannel {
		return domain.StoryMessageForwardList{}, domain.ErrStoryPeerInvalid
	}
	if err := domain.ValidateStoryInteractionOffset(req.Offset, false); err != nil {
		return domain.StoryMessageForwardList{}, err
	}
	ownerType := string(req.Owner.Type)
	ownerID := strconv.FormatInt(req.Owner.ID, 10)
	storyID := strconv.Itoa(req.StoryID)
	where := `
NOT deleted
AND media->>'kind' = 'story'
-- domain.Peer 无 json tag → 序列化为大写 Type/ID（与 repost 查询 ->>'Type'/'ID' 同口径）；
-- 用小写 type/id 会永远匹配不到 → story message forward 计数恒 0（postgres-only，memory 读 struct 不受影响）。
AND media #>> '{story,peer,Type}' = $1
AND media #>> '{story,peer,ID}' = $2
AND media #>> '{story,id}' = $3
AND EXISTS (
  SELECT 1
  FROM channels c
  WHERE c.id = channel_messages.channel_id
    AND NOT c.deleted
    AND (c.broadcast OR c.megagroup)
    AND btrim(COALESCE(c.username, '')) <> ''
)`
	var count int
	if err := s.db.QueryRow(ctx, `SELECT count(*)::int FROM channel_messages WHERE `+where, ownerType, ownerID, storyID).Scan(&count); err != nil {
		return domain.StoryMessageForwardList{}, fmt.Errorf("count story message forwards: %w", err)
	}
	limit := clampPGStoryInteractionLimit(req.Limit)
	cursor := parsePGStoryInteractionCursor(req.Offset)
	args := []any{ownerType, ownerID, storyID}
	cursorClause := ""
	group := 0
	if cursor.set {
		cursorChannelID := int64(0)
		if cursor.viewerID < 0 {
			cursorChannelID = -cursor.viewerID
			if cursorChannelID < 0 {
				return domain.StoryMessageForwardList{}, domain.ErrStoryOffsetInvalid
			}
		}
		args = append(args, int32(group), int32(cursor.group), int32(cursor.date), cursorChannelID, int32(cursor.messageID))
		cursorClause = fmt.Sprintf(`
AND (
  $%d::int > $%d::int
  OR (
    $%d::int = $%d::int
    AND (
      message_date < $%d::int
      OR (
        message_date = $%d::int
        AND (
          channel_id > $%d::bigint
          OR (channel_id = $%d::bigint AND id < $%d::int)
        )
      )
    )
  )
)`, len(args)-4, len(args)-3, len(args)-4, len(args)-3, len(args)-2, len(args)-2, len(args)-1, len(args)-1, len(args))
	}
	args = append(args, int32(limit+1))
	rows, err := s.db.Query(ctx, `
SELECT `+channelMessageColumns+`
FROM channel_messages
WHERE `+where+cursorClause+`
ORDER BY message_date DESC, channel_id ASC, id DESC
LIMIT $`+strconv.Itoa(len(args)), args...)
	if err != nil {
		return domain.StoryMessageForwardList{}, fmt.Errorf("list story message forwards: %w", err)
	}
	defer rows.Close()
	views := make([]domain.StoryView, 0)
	for rows.Next() {
		msg, err := scanChannelMessage(rows)
		if err != nil {
			return domain.StoryMessageForwardList{}, err
		}
		views = append(views, domain.StoryView{
			Owner:   req.Owner,
			StoryID: req.StoryID,
			Date:    msg.Date,
			PublicForward: &domain.StoryPublicForward{
				Message: msg,
			},
		})
	}
	if err := rows.Err(); err != nil {
		return domain.StoryMessageForwardList{}, err
	}
	sortPGStoryViewsForList(views, req.ReactionsFirst, req.ForwardsFirst)
	nextOffset := ""
	if len(views) > limit {
		views = views[:limit]
		nextOffset = formatPGStoryInteractionCursor(views[len(views)-1], req.ReactionsFirst, req.ForwardsFirst)
	}
	return domain.StoryMessageForwardList{Count: count, Forwards: views, NextOffset: nextOffset}, nil
}

func (s *ChannelStore) GetChannelMessageForInlineBot(ctx context.Context, botID, channelID int64, id int) (domain.Channel, domain.ChannelMessage, bool, error) {
	if botID == 0 || channelID == 0 || id <= 0 || id > domain.MaxMessageBoxID {
		return domain.Channel{}, domain.ChannelMessage{}, false, nil
	}
	channel, err := getChannelByID(ctx, s.db, channelID)
	if err != nil {
		if errors.Is(err, domain.ErrChannelInvalid) {
			return domain.Channel{}, domain.ChannelMessage{}, false, nil
		}
		return domain.Channel{}, domain.ChannelMessage{}, false, err
	}
	msg, err := s.getChannelMessage(ctx, s.db, channelID, id)
	if err != nil {
		if errors.Is(err, domain.ErrMessageIDInvalid) {
			return domain.Channel{}, domain.ChannelMessage{}, false, nil
		}
		return domain.Channel{}, domain.ChannelMessage{}, false, err
	}
	if msg.Deleted || msg.Action != nil || msg.ViaBotID != botID {
		return domain.Channel{}, domain.ChannelMessage{}, false, nil
	}
	return channel, msg, true, nil
}

func (s *ChannelStore) GetDiscussionMessage(ctx context.Context, viewerUserID, channelID int64, msgID int) (domain.ChannelDiscussionMessage, error) {
	source, member, err := s.getChannelForMember(ctx, s.db, viewerUserID, channelID)
	if err != nil {
		return domain.ChannelDiscussionMessage{}, err
	}
	msg, err := s.getChannelMessage(ctx, s.db, channelID, msgID)
	if err != nil || msg.Deleted || msg.ID <= member.AvailableMinID {
		return domain.ChannelDiscussionMessage{}, domain.ErrMessageIDInvalid
	}
	result := domain.ChannelDiscussionMessage{PostChannel: source, DiscussionChannel: source, Channels: []domain.Channel{source}}
	target := source
	targetMsg := msg
	if source.Broadcast {
		if msg.Discussion == nil || msg.Discussion.ChannelID == 0 || msg.Discussion.MessageID == 0 {
			return result, nil
		}
		linked, err := getChannelByID(ctx, s.db, msg.Discussion.ChannelID)
		if err != nil {
			return result, nil
		}
		linkedMsg, err := s.getChannelMessage(ctx, s.db, linked.ID, msg.Discussion.MessageID)
		if err != nil || linkedMsg.Deleted {
			return result, nil
		}
		target = linked
		targetMsg = linkedMsg
		result.DiscussionChannel = linked
		result.Channels = []domain.Channel{source, linked}
	}
	messages := []domain.ChannelMessage{targetMsg}
	if err := s.populateChannelMessageReplies(ctx, s.db, viewerUserID, target, messages); err != nil {
		return domain.ChannelDiscussionMessage{}, err
	}
	if err := s.populateChannelMessagesReactions(ctx, s.db, viewerUserID, []domain.Channel{target}, messages); err != nil {
		return domain.ChannelDiscussionMessage{}, err
	}
	readInbox, readOutbox := s.channelReadWatermarks(ctx, target.ID, viewerUserID)
	result.Messages = messages
	result.ReadInboxMaxID = readInbox
	result.ReadOutboxMaxID = readOutbox
	if messages[0].Replies != nil {
		result.MaxID = messages[0].Replies.MaxID
	}
	result.UnreadCount = s.channelThreadUnreadCount(ctx, target.ID, targetMsg.ID, viewerUserID, readInbox)
	return result, nil
}

// ResolveDiscussionReadTarget resolves source post -> linked discussion root
// and checks the durable read boundary in one indexed query. It is the hot path
// for messages.readDiscussion and must not load reply stats, reactions or peer
// payloads used by messages.getDiscussionMessage.
func (s *ChannelStore) ResolveDiscussionReadTarget(ctx context.Context, userID, sourceChannelID int64, sourceMessageID, readMaxID int) (domain.ChannelDiscussionReadTarget, error) {
	var out domain.ChannelDiscussionReadTarget
	var readInboxMaxID int
	var unreadMark bool
	var targetVisible bool
	var targetGuest bool
	var targetTopMessageID int
	err := s.db.QueryRow(ctx, `
WITH source AS (
    SELECT c.broadcast,
           m.discussion_channel_id,
           m.discussion_message_id,
           (
               c.megagroup
               AND NOT c.broadcast
               AND c.linked_chat_id <> 0
               AND (viewer.user_id IS NULL OR viewer.status = 'left')
               AND NOT COALESCE((viewer.banned_rights->>'ViewMessages')::boolean, false)
               AND EXISTS (
                   SELECT 1
                   FROM channels parent
                   JOIN channel_members parent_member
                     ON parent_member.channel_id = parent.id
                    AND parent_member.user_id = $1
                    AND parent_member.status = 'active'
                    AND NOT COALESCE((parent_member.banned_rights->>'ViewMessages')::boolean, false)
                   WHERE parent.id = c.linked_chat_id
                     AND parent.broadcast
                     AND NOT parent.deleted
                     AND parent.linked_chat_id = c.id
               )
           ) AS linked_guest
    FROM channels c
    LEFT JOIN channel_members viewer
      ON viewer.channel_id = c.id
     AND viewer.user_id = $1
    JOIN channel_messages m
      ON m.channel_id = c.id
     AND m.id = $3
     AND NOT m.deleted
     AND m.id > COALESCE(viewer.available_min_id, 0)
    WHERE c.id = $2 AND NOT c.deleted
      AND (
          (
              viewer.status = 'active'
              AND NOT COALESCE((viewer.banned_rights->>'ViewMessages')::boolean, false)
          )
          OR (
              c.megagroup
              AND NOT c.broadcast
              AND c.linked_chat_id <> 0
              AND (viewer.user_id IS NULL OR viewer.status = 'left')
              AND NOT COALESCE((viewer.banned_rights->>'ViewMessages')::boolean, false)
              AND EXISTS (
                  SELECT 1
                  FROM channels parent
                  JOIN channel_members parent_member
                    ON parent_member.channel_id = parent.id
                   AND parent_member.user_id = $1
                   AND parent_member.status = 'active'
                   AND NOT COALESCE((parent_member.banned_rights->>'ViewMessages')::boolean, false)
                  WHERE parent.id = c.linked_chat_id
                    AND parent.broadcast
                    AND NOT parent.deleted
                    AND parent.linked_chat_id = c.id
              )
          )
      )
), target AS (
    SELECT CASE
             WHEN broadcast AND discussion_channel_id <> 0 AND discussion_message_id <> 0
               THEN discussion_channel_id
             ELSE $2
           END AS channel_id,
           CASE
             WHEN broadcast AND discussion_channel_id <> 0 AND discussion_message_id <> 0
               THEN discussion_message_id
             ELSE $3
           END AS root_id,
           broadcast,
           linked_guest,
           discussion_channel_id,
           discussion_message_id
    FROM source
)
SELECT target.channel_id, target.root_id,
       target_channel.top_message_id,
       COALESCE(target_member.read_inbox_max_id, 0),
       COALESCE(target_member.unread_mark, false),
       COALESCE(
           target_member.status = 'active'
           AND NOT COALESCE((target_member.banned_rights->>'ViewMessages')::boolean, false),
           false
       ) OR target.linked_guest OR (
           target.channel_id <> $2
           AND target.broadcast
           AND target_channel.linked_chat_id = $2
           AND (
               target_member.user_id IS NULL
               OR target_member.status = 'left'
           )
       ) AS target_visible,
       (
           target.linked_guest OR (
               target.channel_id <> $2
               AND target.broadcast
               AND target_channel.linked_chat_id = $2
               AND (
                   target_member.user_id IS NULL
                   OR target_member.status = 'left'
               )
           )
       ) AS target_guest
FROM target
JOIN channels target_channel
  ON target_channel.id = target.channel_id
 AND NOT target_channel.deleted
LEFT JOIN channel_members target_member
  ON target_member.channel_id = target.channel_id
 AND target_member.user_id = $1
WHERE (
    NOT target.broadcast
    OR (target.discussion_channel_id = 0 AND target.discussion_message_id = 0)
    OR (
        target.discussion_channel_id <> 0
        AND target.discussion_message_id <> 0
        AND target_channel.megagroup
        AND NOT target_channel.broadcast
        AND EXISTS (
            SELECT 1
            FROM channel_messages root
            WHERE root.channel_id = target.channel_id
              AND root.id = target.root_id
              AND NOT root.deleted
        )
    )
)`, userID, sourceChannelID, sourceMessageID).Scan(&out.ChannelID, &out.RootID, &targetTopMessageID, &readInboxMaxID, &unreadMark, &targetVisible, &targetGuest)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ChannelDiscussionReadTarget{}, domain.ErrMessageIDInvalid
	}
	if err != nil {
		return domain.ChannelDiscussionReadTarget{}, fmt.Errorf("resolve discussion read target: %w", err)
	}
	if !targetVisible {
		return domain.ChannelDiscussionReadTarget{}, domain.ErrChannelPrivate
	}
	effectiveMaxID := readMaxID
	if effectiveMaxID <= 0 || effectiveMaxID > targetTopMessageID {
		effectiveMaxID = targetTopMessageID
	}
	out.AlreadyRead = effectiveMaxID <= readInboxMaxID && !unreadMark
	out.Guest = targetGuest
	return out, nil
}

func (s *ChannelStore) readChannelHistoryOnce(ctx context.Context, req domain.ReadChannelHistoryRequest) (domain.ReadChannelHistoryResult, error) {
	channel, _, err := s.getChannelForMember(ctx, s.db, req.UserID, req.ChannelID)
	if err != nil {
		return domain.ReadChannelHistoryResult{}, err
	}
	maxID := req.MaxID
	if maxID <= 0 || maxID > channel.TopMessageID {
		maxID = channel.TopMessageID
	}
	previous, unreadMark, err := s.channelReadHistoryState(ctx, req.ChannelID, req.UserID)
	if err != nil {
		return domain.ReadChannelHistoryResult{}, fmt.Errorf("read channel member state: %w", err)
	}
	if maxID <= previous && !unreadMark {
		return domain.ReadChannelHistoryResult{
			ChannelID: req.ChannelID,
			MaxID:     maxID,
			Changed:   false,
			Pts:       channel.Pts,
			Forum:     channel.Forum,
		}, nil
	}
	beginner, ok := s.db.(txBeginner)
	if !ok {
		return domain.ReadChannelHistoryResult{}, fmt.Errorf("read channel history: db does not support transactions")
	}
	tx, err := beginner.Begin(ctx)
	if err != nil {
		return domain.ReadChannelHistoryResult{}, fmt.Errorf("begin read channel history: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()
	if err := tx.QueryRow(ctx, `SELECT read_inbox_max_id FROM channel_members WHERE channel_id = $1 AND user_id = $2`, req.ChannelID, req.UserID).Scan(&previous); err != nil {
		return domain.ReadChannelHistoryResult{}, fmt.Errorf("read channel member state: %w", err)
	}
	changed := maxID > previous
	var outboxUpdates []domain.ChannelReadOutboxUpdate
	if changed {
		// 先碰 channels 行再碰 channel_members：send 路径的顺序是
		// channels→members，read 路径必须同序，否则并发 send+read 形成
		// AB-BA 死锁（PG 1s 检测击杀后整个事务回滚）。
		// channel 级公共已读水位：top1/top2 是任一成员推进过的最高两个
		// read_inbox。sender 的 read_outbox 由它派生（top1 持有者本人取
		// top2），即使下面的实时 fanout 被截断，回执真值也不会停滞。
		if _, err := tx.Exec(ctx, `
UPDATE channels
SET read_inbox_top2 = CASE
        WHEN read_inbox_top1_user_id = $2 THEN read_inbox_top2
        WHEN $3 >= read_inbox_top1 THEN read_inbox_top1
        ELSE GREATEST(read_inbox_top2, $3)
    END,
    read_inbox_top1_user_id = CASE
        WHEN read_inbox_top1_user_id = $2 THEN read_inbox_top1_user_id
        WHEN $3 >= read_inbox_top1 THEN $2
        ELSE read_inbox_top1_user_id
    END,
    read_inbox_top1 = CASE
        WHEN read_inbox_top1_user_id = $2 THEN GREATEST(read_inbox_top1, $3)
        WHEN $3 >= read_inbox_top1 THEN $3
        ELSE read_inbox_top1
    END,
    updated_at = now()
WHERE id = $1`, req.ChannelID, req.UserID, maxID); err != nil {
			return domain.ReadChannelHistoryResult{}, fmt.Errorf("advance channel read watermarks: %w", err)
		}
	}
	if _, err := tx.Exec(ctx, `
UPDATE channel_members
SET read_inbox_date = CASE WHEN read_inbox_max_id < $3 THEN $4 ELSE read_inbox_date END,
    read_inbox_max_id = GREATEST(read_inbox_max_id, $3),
    unread_mark = false,
    updated_at = now()
WHERE channel_id = $1 AND user_id = $2`, req.ChannelID, req.UserID, maxID, req.Date); err != nil {
		return domain.ReadChannelHistoryResult{}, fmt.Errorf("update channel member read: %w", err)
	}
	msg, _ := s.getChannelMessage(ctx, tx, req.ChannelID, channel.TopMessageID)
	if changed {
		outboxUpdates, err = advanceChannelReadOutboxTx(ctx, tx, channel, msg, req.UserID, previous, maxID)
		if err != nil {
			return domain.ReadChannelHistoryResult{}, err
		}
	}
	if err := upsertChannelDialogTx(ctx, tx, req.UserID, channel, msg, maxID, 0); err != nil {
		return domain.ReadChannelHistoryResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.ReadChannelHistoryResult{}, fmt.Errorf("commit read channel history: %w", err)
	}
	committed = true
	dialog, err := s.getChannelDialog(ctx, s.db, req.UserID, channel)
	if err != nil {
		return domain.ReadChannelHistoryResult{}, err
	}
	return domain.ReadChannelHistoryResult{
		ChannelID:        req.ChannelID,
		MaxID:            maxID,
		StillUnreadCount: dialog.UnreadCount,
		Changed:          changed,
		Pts:              channel.Pts,
		Forum:            channel.Forum,
		Dialog:           dialog,
		OutboxUpdates:    outboxUpdates,
	}, nil
}

func (s *ChannelStore) channelReadHistoryState(ctx context.Context, channelID, userID int64) (readInboxMaxID int, unreadMark bool, err error) {
	err = s.db.QueryRow(ctx, `
SELECT read_inbox_max_id, unread_mark
FROM channel_members
WHERE channel_id = $1 AND user_id = $2`, channelID, userID).Scan(&readInboxMaxID, &unreadMark)
	return readInboxMaxID, unreadMark, err
}

func (s *ChannelStore) getChannelMessage(ctx context.Context, db sqlcgen.DBTX, channelID int64, id int) (domain.ChannelMessage, error) {
	if channelID == 0 || id == 0 {
		return domain.ChannelMessage{}, pgx.ErrNoRows
	}
	msg, err := scanChannelMessage(db.QueryRow(ctx, `SELECT `+channelMessageColumns+` FROM channel_messages WHERE channel_id = $1 AND id = $2`, channelID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ChannelMessage{}, domain.ErrMessageIDInvalid
	}
	return msg, err
}

type messageHistoryLoad int

func messageHistoryLoadType(addOffset, limit int) messageHistoryLoad {
	if addOffset >= 0 {
		return messageHistoryLoadBackward
	}
	if addOffset+limit > 0 {
		return messageHistoryLoadAround
	}
	return messageHistoryLoadForward
}

func channelMessageLess(a, b domain.ChannelMessage) bool {
	if a.Date != b.Date {
		return a.Date > b.Date
	}
	return a.ID > b.ID
}
