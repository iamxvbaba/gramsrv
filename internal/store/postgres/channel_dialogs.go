package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"sort"
	"strings"
	"telesrv/internal/domain"
	"telesrv/internal/store/postgres/sqlcgen"
)

type channelDialogListItem struct {
	channel       domain.Channel
	dialog        domain.Dialog
	defaultSendAs *domain.Peer
}

func (s *ChannelStore) ListChannelDialogs(ctx context.Context, viewerUserID int64, filter domain.DialogFilter) (domain.ChannelDialogList, error) {
	if viewerUserID == 0 {
		return domain.ChannelDialogList{}, nil
	}
	limit := filter.Limit
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	channelIDs, hasBroadcastAdmin, err := s.listActiveChannelDialogCandidateIDs(ctx, viewerUserID, false)
	if err != nil {
		return domain.ChannelDialogList{}, err
	}
	if len(channelIDs) == 0 {
		return domain.ChannelDialogList{}, nil
	}
	visibleTopID := "CASE WHEN c.top_message_id > m.available_min_id THEN c.top_message_id ELSE 0 END"
	visibleTopDate := "CASE WHEN c.top_message_id > m.available_min_id THEN COALESCE(top_msg.message_date, d.top_message_date, c.date) ELSE 0 END"
	visibleReadInbox := "GREATEST(COALESCE(d.read_inbox_max_id, 0), m.read_inbox_max_id)"
	visibleUnreadCount := channelDialogVisibleUnreadCountSQL(visibleReadInbox, visibleTopID)
	args := []any{viewerUserID, channelIDs}
	where := []string{"m.user_id = $1", "m.channel_id = ANY($2::bigint[])", "m.status = 'active'"}
	if filter.HasFolderID && filter.FolderID < domain.DialogCustomFolderMinID {
		args = append(args, filter.FolderID)
		where = append(where, fmt.Sprintf("COALESCE(d.folder_id, 0) = $%d", len(args)))
	} else if !filter.HasFolderID {
		// 不带 folder_id 视为主列表（folder 0），与私聊侧/官方语义一致。
		where = append(where, "COALESCE(d.folder_id, 0) = 0")
	}
	if filter.PinnedOnly {
		where = append(where, "COALESCE(d.pinned, false)")
	}
	if filter.ExcludePinned {
		where = append(where, "NOT COALESCE(d.pinned, false)")
	}
	switch {
	case filter.OffsetDate > 0:
		args = append(args, filter.OffsetDate, filter.OffsetID)
		dateArg := fmt.Sprintf("$%d", len(args)-1)
		idArg := fmt.Sprintf("$%d", len(args))
		if filter.HasOffsetPeer && filter.OffsetPeer.Type == domain.PeerTypeChannel && filter.OffsetPeer.ID > 0 {
			args = append(args, filter.OffsetPeer.ID)
			peerArg := fmt.Sprintf("$%d", len(args))
			where = append(where, fmt.Sprintf("(%s < %s OR (%s = %s AND %s < %s) OR (%s = %s AND %s = %s AND c.id < %s))",
				visibleTopDate, dateArg,
				visibleTopDate, dateArg, visibleTopID, idArg,
				visibleTopDate, dateArg, visibleTopID, idArg, peerArg))
		} else {
			where = append(where, fmt.Sprintf("(%s < %s OR (%s = %s AND %s < %s))",
				visibleTopDate, dateArg,
				visibleTopDate, dateArg, visibleTopID, idArg))
		}
	case filter.OffsetID > 0:
		args = append(args, filter.OffsetID)
		idArg := fmt.Sprintf("$%d", len(args))
		if filter.HasOffsetPeer && filter.OffsetPeer.Type == domain.PeerTypeChannel && filter.OffsetPeer.ID > 0 {
			args = append(args, filter.OffsetPeer.ID)
			peerArg := fmt.Sprintf("$%d", len(args))
			where = append(where, fmt.Sprintf("(%s < %s OR (%s = %s AND c.id < %s))",
				visibleTopID, idArg, visibleTopID, idArg, peerArg))
		} else {
			where = append(where, fmt.Sprintf("%s < %s", visibleTopID, idArg))
		}
	case filter.HasOffsetPeer && filter.OffsetPeer.Type == domain.PeerTypeChannel && filter.OffsetPeer.ID > 0:
		args = append(args, filter.OffsetPeer.ID)
		where = append(where, fmt.Sprintf("c.id <> $%d", len(args)))
	}
	if filter.Folder != nil {
		folder := filter.Folder
		if folder.ExcludeArchived {
			where = append(where, fmt.Sprintf("COALESCE(d.folder_id, 0) <> %d", domain.DialogArchiveFolderID))
		}
		if folder.ExcludeRead {
			where = append(where, fmt.Sprintf(`(COALESCE(d.unread_mark, m.unread_mark) OR %s)`,
				channelDialogHasUnreadSQL(visibleReadInbox, visibleTopID)))
		}
		if excludeIDs := channelFolderPeerIDs(folder.ExcludePeers); len(excludeIDs) > 0 {
			args = append(args, excludeIDs)
			where = append(where, fmt.Sprintf("NOT (c.id = ANY($%d::bigint[]))", len(args)))
		}
		includeIDs := channelFolderPeerIDs(folder.IncludePeers, folder.PinnedPeers)
		include := make([]string, 0, 3)
		if len(includeIDs) > 0 {
			args = append(args, includeIDs)
			include = append(include, fmt.Sprintf("c.id = ANY($%d::bigint[])", len(args)))
		}
		if folder.Groups {
			include = append(include, "c.megagroup")
		}
		if folder.Broadcasts {
			include = append(include, "c.broadcast")
		}
		if len(include) > 0 {
			where = append(where, "("+strings.Join(include, " OR ")+")")
		} else {
			// 自定义 filter 未声明任何频道维度正向条件（仅 Contacts/NonContacts/Bots
			// 这类私聊类别开关）时，不纳入任何群/频道——与 channelDialogMatchesFilter
			// 末尾的 return false 对齐，避免把用户加入的全部群/频道灌进该文件夹。
			where = append(where, "false")
		}
	}
	args = append(args, channelDialogQueryLimit)
	limitArg := fmt.Sprintf("$%d", len(args))
	// 暖写回的 epoch 守卫:在加载前快照,写回时若期间收到失效(epoch 变更)则拒绝陈旧投影,
	// 避免 scan→put 窗口内的并发失效被裸 Store 覆盖回(@角标/未读 lost-update)。
	dialogCacheActive := s.dialogCacheActive(s.db)
	var dialogCacheEpoch uint64
	if dialogCacheActive {
		dialogCacheEpoch = s.dialogCache.cacheEpoch()
	}
	rows, err := s.db.Query(ctx, `
SELECT `+channelColumns+`,
       `+visibleTopID+`,
       `+visibleTopDate+`,
       COALESCE(d.folder_id, 0), `+visibleReadInbox+`,
       LEAST(GREATEST(c.top_message_id, 0), GREATEST(COALESCE(d.read_outbox_max_id, 0), m.read_outbox_max_id, CASE WHEN c.read_inbox_top1_user_id = m.user_id THEN c.read_inbox_top2 ELSE c.read_inbox_top1 END)), `+visibleUnreadCount+`,
       COALESCE(d.pinned, false),
       COALESCE(d.pinned_order, 0), COALESCE(d.unread_mark, m.unread_mark),
       COALESCE(d.unread_mentions_count, 0), COALESCE(d.unread_reactions_count, 0),
       COALESCE(d.view_forum_as_messages, false),
       COALESCE(d.has_scheduled, false),
       d.default_send_as_peer_type,
       d.default_send_as_peer_id
FROM channel_members m
JOIN channels c ON c.id = m.channel_id AND c.id = ANY($2::bigint[]) AND NOT c.deleted
LEFT JOIN channel_messages top_msg ON top_msg.channel_id = m.channel_id AND top_msg.channel_id = ANY($2::bigint[]) AND top_msg.id = c.top_message_id AND NOT top_msg.deleted
LEFT JOIN channel_dialogs d ON d.user_id = m.user_id AND d.channel_id = m.channel_id
WHERE `+strings.Join(where, " AND ")+`
ORDER BY COALESCE(d.pinned, false) DESC,
         COALESCE(d.pinned_order, 0) DESC,
         `+visibleTopDate+` DESC,
         `+visibleTopID+` DESC,
         c.id DESC
LIMIT `+limitArg, args...)
	if err != nil {
		return domain.ChannelDialogList{}, fmt.Errorf("list channel dialogs: %w", err)
	}
	defer rows.Close()
	items := make([]channelDialogListItem, 0, limit)
	seenChannels := make(map[int64]struct{}, limit)
	for rows.Next() {
		ch, dialog, defaultSendAs, err := scanChannelDialogRow(rows, viewerUserID)
		if err != nil {
			return domain.ChannelDialogList{}, err
		}
		if !channelDialogMatchesFilter(dialog, ch, filter) {
			continue
		}
		items = append(items, channelDialogListItem{channel: ch, dialog: dialog, defaultSendAs: defaultSendAs})
		seenChannels[ch.ID] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return domain.ChannelDialogList{}, err
	}
	// hasBroadcastAdmin 是页无关的超集门:只有「在某广播频道任 creator/admin」的用户才可能拥有
	// monoforum 私信会话(linked_monoforum_id<>0 是该集合的子集)。绝大多数 getDialogs 调用者命不中,
	// 据此跳过 listMonoforumAdminDialogItems 这条多表 JOIN 的额外往返(此前对每次 getDialogs 无条件执行)。
	// 不用主扫描循环里的行级标志,因为主查询带 offset 游标:翻到后续页时母广播频道可能已被上一页消费、
	// 不再被扫到,而其私信会话(独立排序位)仍属于本页——行级标志会漏发。超集门只读全量候选集,页无关。
	if hasBroadcastAdmin {
		monoItems, err := s.listMonoforumAdminDialogItems(ctx, viewerUserID, channelIDs, filter, seenChannels)
		if err != nil {
			return domain.ChannelDialogList{}, err
		}
		items = append(items, monoItems...)
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].dialog.Pinned != items[j].dialog.Pinned {
			return items[i].dialog.Pinned
		}
		if items[i].dialog.PinnedOrder != items[j].dialog.PinnedOrder {
			return items[i].dialog.PinnedOrder > items[j].dialog.PinnedOrder
		}
		if items[i].dialog.TopMessageDate != items[j].dialog.TopMessageDate {
			return items[i].dialog.TopMessageDate > items[j].dialog.TopMessageDate
		}
		if items[i].dialog.TopMessage != items[j].dialog.TopMessage {
			return items[i].dialog.TopMessage > items[j].dialog.TopMessage
		}
		return items[i].dialog.Peer.ID > items[j].dialog.Peer.ID
	})
	out := domain.ChannelDialogList{Count: len(items)}
	if len(items) > limit {
		items = items[:limit]
	}
	dialogs := make([]domain.Dialog, 0, len(items))
	for _, item := range items {
		dialogs = append(dialogs, item.dialog)
	}
	topMessages, err := s.channelDialogTopMessages(ctx, s.db, dialogs)
	if err != nil {
		return domain.ChannelDialogList{}, err
	}
	for _, item := range items {
		msg := topMessages[channelMessageLookupKey{channelID: item.channel.ID, id: item.dialog.TopMessage}]
		if msg.ID != 0 {
			item.dialog.TopMessageDate = msg.Date
			out.Messages = append(out.Messages, msg)
		}
		if dialogCacheActive {
			// 列表暖缓存的值必须与单频道全量读路径等价：补上仅存于 channel_dialogs 的
			// default_send_as，否则 getFullChannel/getSendAs 命中暖缓存会丢失「以频道发言」默认值。
			cd := channelDialogFromDialog(viewerUserID, item.dialog)
			cd.DefaultSendAs = item.defaultSendAs
			s.dialogCache.putIfEpoch(cd, dialogCacheEpoch)
		}
		out.Dialogs = append(out.Dialogs, item.dialog)
		out.Channels = append(out.Channels, item.channel)
	}
	// getDialogs 的 top message 必须按 viewer 补 mentioned/media_unread 与
	// reactions：TDesktop 把它先入缓存且不被后续 difference/getHistory 的
	// 完整版覆盖，缺标志会让客户端永不上报 contents-read，@ 角标重启回潮。
	if err := s.populateChannelMessagesReactions(ctx, s.db, viewerUserID, out.Channels, out.Messages); err != nil {
		return domain.ChannelDialogList{}, err
	}
	return out, nil
}

func (s *ChannelStore) listMonoforumAdminDialogItems(ctx context.Context, viewerUserID int64, parentChannelIDs []int64, filter domain.DialogFilter, seen map[int64]struct{}) ([]channelDialogListItem, error) {
	if viewerUserID == 0 || len(parentChannelIDs) == 0 {
		return nil, nil
	}
	rows, err := s.db.Query(ctx, `
SELECT `+channelColumns+`,
       c.top_message_id,
       COALESCE(top_msg.message_date, d.top_message_date, c.date),
       COALESCE(d.folder_id, 0),
       GREATEST(COALESCE(d.read_inbox_max_id, 0), c.top_message_id),
       GREATEST(COALESCE(d.read_outbox_max_id, 0), c.top_message_id),
       0,
       COALESCE(d.pinned, false),
       COALESCE(d.pinned_order, 0),
       COALESCE(d.unread_mark, false),
       COALESCE(d.unread_mentions_count, 0),
       COALESCE(d.unread_reactions_count, 0),
       COALESCE(d.view_forum_as_messages, false),
       COALESCE(d.has_scheduled, false),
       d.default_send_as_peer_type,
       d.default_send_as_peer_id
FROM user_channel_member_index i
JOIN channel_members pm ON pm.user_id = i.user_id AND pm.channel_id = i.channel_id
JOIN channels parent ON parent.id = i.channel_id AND parent.id = ANY($2::bigint[]) AND parent.broadcast AND parent.linked_monoforum_id <> 0 AND NOT parent.deleted
JOIN channels c ON c.id = parent.linked_monoforum_id AND c.monoforum AND NOT c.deleted
LEFT JOIN channel_messages top_msg ON top_msg.channel_id = c.id AND top_msg.id = c.top_message_id AND NOT top_msg.deleted
LEFT JOIN channel_dialogs d ON d.user_id = i.user_id AND d.channel_id = c.id
WHERE i.user_id = $1
  AND i.status = 'active'
  AND NOT i.deleted
  AND i.role IN ('creator','admin')
  AND pm.status = 'active'
  AND pm.role IN ('creator','admin')
ORDER BY COALESCE(d.pinned, false) DESC,
         COALESCE(d.pinned_order, 0) DESC,
         COALESCE(top_msg.message_date, d.top_message_date, c.date) DESC,
         c.top_message_id DESC,
         c.id DESC
LIMIT $3`, viewerUserID, parentChannelIDs, channelDialogQueryLimit)
	if err != nil {
		return nil, fmt.Errorf("list monoforum admin dialogs: %w", err)
	}
	defer rows.Close()
	items := make([]channelDialogListItem, 0)
	for rows.Next() {
		ch, dialog, defaultSendAs, err := scanChannelDialogRow(rows, viewerUserID)
		if err != nil {
			return nil, err
		}
		if _, ok := seen[ch.ID]; ok {
			continue
		}
		if !channelDialogMatchesFilter(dialog, ch, filter) {
			continue
		}
		seen[ch.ID] = struct{}{}
		items = append(items, channelDialogListItem{channel: ch, dialog: dialog, defaultSendAs: defaultSendAs})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("scan monoforum admin dialogs: %w", err)
	}
	return items, nil
}

type channelMessageLookupKey struct {
	channelID int64
	id        int
}

func (s *ChannelStore) channelDialogTopMessages(ctx context.Context, db sqlcgen.DBTX, dialogs []domain.Dialog) (map[channelMessageLookupKey]domain.ChannelMessage, error) {
	seen := make(map[channelMessageLookupKey]struct{}, len(dialogs))
	idsByChannel := make(map[int64][]int, len(dialogs))
	for _, dialog := range dialogs {
		if dialog.Peer.Type != domain.PeerTypeChannel || dialog.Peer.ID == 0 || dialog.TopMessage <= 0 {
			continue
		}
		key := channelMessageLookupKey{channelID: dialog.Peer.ID, id: dialog.TopMessage}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		idsByChannel[dialog.Peer.ID] = append(idsByChannel[dialog.Peer.ID], dialog.TopMessage)
	}
	if len(idsByChannel) == 0 {
		return nil, nil
	}
	channelIDs := make([]int64, 0, len(idsByChannel))
	for channelID := range idsByChannel {
		channelIDs = append(channelIDs, channelID)
	}
	sort.Slice(channelIDs, func(i, j int) bool { return channelIDs[i] < channelIDs[j] })
	args := make([]any, 0, len(channelIDs)*2)
	var where strings.Builder
	for i, channelID := range channelIDs {
		if i > 0 {
			where.WriteString(" OR ")
		}
		args = append(args, channelID)
		channelArg := len(args)
		args = append(args, int32s(idsByChannel[channelID]))
		idsArg := len(args)
		fmt.Fprintf(&where, "(channel_id = $%d AND id = ANY($%d::int[]))", channelArg, idsArg)
	}
	rows, err := db.Query(ctx, `
SELECT `+channelMessageColumns+`
FROM channel_messages
WHERE `+where.String(), args...)
	if err != nil {
		return nil, fmt.Errorf("list channel dialog top messages: %w", err)
	}
	defer rows.Close()
	out := make(map[channelMessageLookupKey]domain.ChannelMessage, len(seen))
	for rows.Next() {
		msg, err := scanChannelMessage(rows)
		if err != nil {
			continue
		}
		out[channelMessageLookupKey{channelID: msg.ChannelID, id: msg.ID}] = msg
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("scan channel dialog top messages: %w", err)
	}
	return out, nil
}

func (s *ChannelStore) GetChannelDialogs(ctx context.Context, viewerUserID int64, channelIDs []int64) (domain.ChannelDialogList, error) {
	out := domain.ChannelDialogList{}
	seen := make(map[int64]struct{}, len(channelIDs))
	for _, channelID := range channelIDs {
		if channelID == 0 {
			continue
		}
		if _, ok := seen[channelID]; ok {
			continue
		}
		seen[channelID] = struct{}{}
		channel, member, err := s.getChannelForMember(ctx, s.db, viewerUserID, channelID)
		synthetic := false
		if err != nil {
			if errors.Is(err, domain.ErrChannelInvalid) || errors.Is(err, domain.ErrChannelPrivate) {
				ch, chErr := s.channelByID(ctx, s.db, channelID)
				if chErr != nil {
					if errors.Is(chErr, domain.ErrChannelInvalid) {
						continue
					}
					return domain.ChannelDialogList{}, chErr
				}
				syntheticMember, parentChannel, ok, previewErr := s.monoforumAdminPreview(ctx, s.db, viewerUserID, ch)
				if previewErr != nil {
					return domain.ChannelDialogList{}, previewErr
				}
				if !ok {
					continue
				}
				channel = ch
				member = syntheticMember
				synthetic = true
				// 与 mono 同批下发母广播频道。TDesktop 的 applyMonoforumLinkedId 仅在父对象已加载时
				// 才调 setMonoforumLink 派生 MonoforumAdmin(Direct-Messages 容器渲染所需);父对象缺失
				// 会让 mono 退化成普通 megagroup("You created a group")。tgChannelsForDialogs 按 id
				// 去重,重复下发无害;此分支已 gate 于 isChannelAdmin(parentMember),不泄漏给非管理员。
				if parentChannel.ID != 0 {
					out.Channels = append(out.Channels, parentChannel)
				}
			} else {
				return domain.ChannelDialogList{}, err
			}
		}
		var dialog domain.ChannelDialog
		if synthetic {
			dialog = previewChannelDialog(viewerUserID, channel, member)
		} else {
			dialog, err = s.getChannelDialog(ctx, s.db, viewerUserID, channel)
			if err != nil {
				return domain.ChannelDialogList{}, err
			}
		}
		msg, _ := s.getChannelMessage(ctx, s.db, channelID, dialog.TopMessageID)
		if msg.ID != 0 {
			dialog.TopMessageDate = msg.Date
			out.Messages = append(out.Messages, msg)
		}
		out.Dialogs = append(out.Dialogs, channelDialogToDialog(dialog, channel.Pts))
		out.Channels = append(out.Channels, channel)
	}
	out.Count = len(out.Dialogs)
	// 与 ListChannelDialogs 同因：top message 按 viewer 补未读标志与 reactions。
	if err := s.populateChannelMessagesReactions(ctx, s.db, viewerUserID, out.Channels, out.Messages); err != nil {
		return domain.ChannelDialogList{}, err
	}
	return out, nil
}

func (s *ChannelStore) ListCommonChannels(ctx context.Context, req domain.CommonChannelsRequest) (domain.CommonChannelsResult, error) {
	if req.UserID == 0 || req.TargetUserID == 0 || req.UserID == req.TargetUserID || req.MaxID < 0 {
		return domain.CommonChannelsResult{}, domain.ErrChannelInvalid
	}
	limit := req.Limit
	if limit <= 0 || limit > domain.MaxCommonChannelsLimit {
		limit = domain.MaxCommonChannelsLimit
	}
	var count int
	if err := s.db.QueryRow(ctx, `
SELECT COUNT(*)::int
FROM user_channel_member_index selfm
JOIN user_channel_member_index targetm ON targetm.channel_id = selfm.channel_id
WHERE selfm.user_id = $1
  AND targetm.user_id = $2
  AND selfm.status = 'active'
  AND targetm.status = 'active'
  AND selfm.megagroup
  AND NOT selfm.broadcast
  AND NOT selfm.deleted
  AND targetm.megagroup
  AND NOT targetm.broadcast
  AND NOT targetm.deleted`, req.UserID, req.TargetUserID).Scan(&count); err != nil {
		return domain.CommonChannelsResult{}, fmt.Errorf("count common channels: %w", err)
	}
	out := domain.CommonChannelsResult{Count: count}
	if req.CountOnly {
		return out, nil
	}
	rows, err := s.db.Query(ctx, `
SELECT selfm.channel_id
FROM user_channel_member_index selfm
JOIN user_channel_member_index targetm ON targetm.channel_id = selfm.channel_id
WHERE selfm.user_id = $1
  AND targetm.user_id = $2
  AND selfm.status = 'active'
  AND targetm.status = 'active'
  AND selfm.megagroup
  AND NOT selfm.broadcast
  AND NOT selfm.deleted
  AND targetm.megagroup
  AND NOT targetm.broadcast
  AND NOT targetm.deleted
  AND ($3::bigint = 0 OR selfm.channel_id > $3)
ORDER BY selfm.channel_id ASC
LIMIT $4`, req.UserID, req.TargetUserID, req.MaxID, limit)
	if err != nil {
		return domain.CommonChannelsResult{}, fmt.Errorf("list common channels: %w", err)
	}
	defer rows.Close()
	ids := make([]int64, 0, limit)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return domain.CommonChannelsResult{}, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return domain.CommonChannelsResult{}, err
	}
	channels, err := listChannelsByIDs(ctx, s.db, ids)
	if err != nil {
		return domain.CommonChannelsResult{}, err
	}
	out.Channels = channels
	return out, nil
}

func (s *ChannelStore) ListLeftChannels(ctx context.Context, userID int64, offset, limit int) (domain.LeftChannelsResult, error) {
	if userID == 0 || offset < 0 || offset > domain.MaxLeftChannelsOffset {
		return domain.LeftChannelsResult{}, domain.ErrChannelInvalid
	}
	if limit <= 0 || limit > domain.MaxLeftChannelsLimit {
		limit = domain.MaxLeftChannelsLimit
	}
	var count int
	if err := s.db.QueryRow(ctx, `
SELECT COUNT(*)::int
FROM user_channel_member_index
WHERE user_id = $1
  AND status = 'left'
  AND (broadcast OR megagroup)
  AND NOT deleted`, userID).Scan(&count); err != nil {
		return domain.LeftChannelsResult{}, fmt.Errorf("count left channels: %w", err)
	}
	rows, err := s.db.Query(ctx, `
SELECT channel_id
FROM user_channel_member_index
WHERE user_id = $1
  AND status = 'left'
  AND (broadcast OR megagroup)
  AND NOT deleted
ORDER BY left_at DESC, channel_id DESC
OFFSET $2
LIMIT $3`, userID, offset, limit)
	if err != nil {
		return domain.LeftChannelsResult{}, fmt.Errorf("list left channels: %w", err)
	}
	defer rows.Close()
	ids := make([]int64, 0, limit)
	for rows.Next() {
		var channelID int64
		if err := rows.Scan(&channelID); err != nil {
			return domain.LeftChannelsResult{}, err
		}
		ids = append(ids, channelID)
	}
	if err := rows.Err(); err != nil {
		return domain.LeftChannelsResult{}, err
	}
	items, err := s.leftChannelsByIDs(ctx, userID, ids)
	if err != nil {
		return domain.LeftChannelsResult{}, err
	}
	return domain.LeftChannelsResult{Count: count, Channels: items}, nil
}

func (s *ChannelStore) leftChannelsByIDs(ctx context.Context, userID int64, ids []int64) ([]domain.LeftChannel, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	rows, err := s.db.Query(ctx, `
SELECT channel_id, user_id, inviter_user_id, role, status, joined_at, left_at,
       admin_rights::text, banned_rights::text, rank, available_min_id, available_min_pts,
       read_inbox_max_id, read_outbox_max_id, unread_mark, slowmode_last_send_date
FROM channel_members
WHERE user_id = $1
  AND channel_id = ANY($2::bigint[])
  AND status = 'left'`, userID, ids)
	if err != nil {
		return nil, fmt.Errorf("list left channel details: %w", err)
	}
	defer rows.Close()
	membersByID := make(map[int64]domain.ChannelMember, len(ids))
	for rows.Next() {
		member, err := scanChannelMember(rows)
		if err != nil {
			return nil, err
		}
		membersByID[member.ChannelID] = member
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	channels, err := listChannelsByIDsInOrder(ctx, s.db, ids)
	if err != nil {
		return nil, err
	}
	out := make([]domain.LeftChannel, 0, len(ids))
	for _, channel := range channels {
		if !channel.Broadcast && !channel.Megagroup {
			continue
		}
		if member, ok := membersByID[channel.ID]; ok {
			out = append(out, domain.LeftChannel{Channel: channel, Self: member})
		}
	}
	return out, nil
}

func (s *ChannelStore) ListInactiveChannels(ctx context.Context, userID int64, limit int) (domain.ChannelDialogList, error) {
	if userID == 0 {
		return domain.ChannelDialogList{}, domain.ErrChannelInvalid
	}
	if limit <= 0 || limit > domain.MaxInactiveChannelsLimit {
		limit = domain.MaxInactiveChannelsLimit
	}
	channelIDs, _, err := s.listActiveChannelDialogCandidateIDs(ctx, userID, true)
	if err != nil {
		return domain.ChannelDialogList{}, err
	}
	if len(channelIDs) == 0 {
		return domain.ChannelDialogList{}, nil
	}
	visibleTopID := "CASE WHEN c.top_message_id > m.available_min_id THEN c.top_message_id ELSE 0 END"
	visibleTopDate := "CASE WHEN c.top_message_id > m.available_min_id THEN COALESCE(top_msg.message_date, d.top_message_date, c.date) ELSE GREATEST(c.date, m.joined_at) END"
	visibleReadInbox := "GREATEST(COALESCE(d.read_inbox_max_id, 0), m.read_inbox_max_id)"
	visibleUnreadCount := channelDialogVisibleUnreadCountSQL(visibleReadInbox, visibleTopID)
	rows, err := s.db.Query(ctx, `
SELECT `+channelColumns+`,
       `+visibleTopID+`,
       `+visibleTopDate+`,
       COALESCE(d.folder_id, 0), `+visibleReadInbox+`,
       LEAST(GREATEST(c.top_message_id, 0), GREATEST(COALESCE(d.read_outbox_max_id, 0), m.read_outbox_max_id, CASE WHEN c.read_inbox_top1_user_id = m.user_id THEN c.read_inbox_top2 ELSE c.read_inbox_top1 END)), `+visibleUnreadCount+`,
       COALESCE(d.pinned, false),
       COALESCE(d.pinned_order, 0), COALESCE(d.unread_mark, m.unread_mark),
       COALESCE(d.unread_mentions_count, 0), COALESCE(d.unread_reactions_count, 0),
       COALESCE(d.view_forum_as_messages, false),
       COALESCE(d.has_scheduled, false),
       d.default_send_as_peer_type,
       d.default_send_as_peer_id
FROM channel_members m
JOIN channels c ON c.id = m.channel_id AND c.id = ANY($2::bigint[]) AND NOT c.deleted
LEFT JOIN channel_messages top_msg ON top_msg.channel_id = m.channel_id AND top_msg.channel_id = ANY($2::bigint[]) AND top_msg.id = c.top_message_id AND NOT top_msg.deleted
LEFT JOIN channel_dialogs d ON d.user_id = m.user_id AND d.channel_id = m.channel_id
WHERE m.user_id = $1
  AND m.channel_id = ANY($2::bigint[])
  AND m.status = 'active'
  AND (c.broadcast OR c.megagroup)
ORDER BY `+visibleTopDate+` ASC,
         `+visibleTopID+` ASC,
         c.id ASC
LIMIT $3`, userID, channelIDs, limit)
	if err != nil {
		return domain.ChannelDialogList{}, fmt.Errorf("list inactive channels: %w", err)
	}
	defer rows.Close()
	out := domain.ChannelDialogList{Dialogs: make([]domain.Dialog, 0, limit), Channels: make([]domain.Channel, 0, limit)}
	for rows.Next() {
		ch, dialog, _, err := scanChannelDialogRow(rows, userID)
		if err != nil {
			return domain.ChannelDialogList{}, err
		}
		out.Dialogs = append(out.Dialogs, dialog)
		out.Channels = append(out.Channels, ch)
	}
	if err := rows.Err(); err != nil {
		return domain.ChannelDialogList{}, err
	}
	out.Count = len(out.Dialogs)
	return out, nil
}

// listActiveChannelDialogCandidateIDs 返回候选频道 id,并附带 hasBroadcastAdmin:该用户是否在任一
// 广播频道任 creator/admin。后者是 monoforum 私信会话存在性的页无关超集判定,供 getDialogs 热路径据此
// 跳过额外的 monoforum 管理员会话查询(见 ListChannelDialogs)。只读 user_channel_member_index 表
// 既有的 broadcast/role 列,零额外往返。
func (s *ChannelStore) listActiveChannelDialogCandidateIDs(ctx context.Context, userID int64, channelLikeOnly bool) ([]int64, bool, error) {
	if userID == 0 {
		return nil, false, nil
	}
	where := "user_id = $1 AND status = 'active' AND NOT deleted"
	if channelLikeOnly {
		where += " AND (broadcast OR megagroup)"
	}
	rows, err := s.db.Query(ctx, `
SELECT channel_id, broadcast, role
FROM user_channel_member_index
WHERE `+where+`
ORDER BY channel_id ASC
LIMIT $2`, userID, channelDialogCandidateLimit)
	if err != nil {
		return nil, false, fmt.Errorf("list active channel dialog candidate ids: %w", err)
	}
	defer rows.Close()
	ids := make([]int64, 0, minInt(channelDialogCandidateLimit, 1024))
	hasBroadcastAdmin := false
	for rows.Next() {
		var channelID int64
		var broadcast bool
		var role string
		if err := rows.Scan(&channelID, &broadcast, &role); err != nil {
			return nil, false, err
		}
		ids = append(ids, channelID)
		if broadcast && (role == "creator" || role == "admin") {
			hasBroadcastAdmin = true
		}
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	return ids, hasBroadcastAdmin, nil
}

func (s *ChannelStore) SetChannelDialogPinned(ctx context.Context, userID, channelID int64, pinned bool) (bool, int, error) {
	if userID == 0 || channelID == 0 {
		return false, 0, nil
	}
	var changed bool
	var folderID int32
	if err := s.db.QueryRow(ctx, `
WITH target AS (
    SELECT c.id AS channel_id, c.top_message_id, c.date AS top_message_date,
           m.read_inbox_max_id, m.read_outbox_max_id, m.available_min_id
    FROM channels c
    JOIN channel_members m ON m.channel_id = c.id
    WHERE c.id = $2 AND m.user_id = $1 AND m.status = 'active' AND NOT c.deleted
),
ensured AS (
    -- 惰性建行必须带 member 真实水位与未读数：0 值缓存行一旦存在就会
    -- 遮蔽真值（小群读取信缓存列）。
    INSERT INTO channel_dialogs (user_id, channel_id, top_message_id, top_message_date,
                                 read_inbox_max_id, read_outbox_max_id, unread_count)
    SELECT $1, channel_id, top_message_id, top_message_date,
           read_inbox_max_id, read_outbox_max_id, (
        SELECT COUNT(*)::int
        FROM channel_messages cm
        WHERE cm.channel_id = target.channel_id
          AND cm.id > GREATEST(target.read_inbox_max_id, target.available_min_id)
          AND NOT cm.deleted
          AND cm.sender_user_id <> $1
    )
    FROM target
    ON CONFLICT (user_id, channel_id) DO NOTHING
),
-- 置顶顺序跨 dialogs/channel_dialogs 两表、但仅在该会话当前 folder 内分配，
-- 与私聊 SetDialogPinned 共用同一个 (user, folder) order 空间。
self_folder AS (
    SELECT COALESCE(d.folder_id, 0)::int AS folder_id
    FROM channel_dialogs d
    WHERE d.user_id = $1 AND d.channel_id = $2
),
next_order AS (
    SELECT GREATEST(
        COALESCE((
            SELECT MAX(cd.pinned_order)
            FROM channel_dialogs cd, self_folder f
            WHERE cd.user_id = $1 AND cd.pinned AND COALESCE(cd.folder_id, 0) = f.folder_id
        ), 0),
        COALESCE((
            SELECT MAX(ud.pinned_order)
            FROM dialogs ud, self_folder f
            WHERE ud.user_id = $1 AND ud.pinned AND ud.folder_id = f.folder_id
        ), 0)
    )::int + 1 AS value
),
updated AS (
    UPDATE channel_dialogs d
    SET pinned = $3,
        pinned_order = CASE
            WHEN $3::boolean THEN CASE WHEN d.pinned_order > 0 THEN d.pinned_order ELSE next_order.value END
            ELSE 0
        END,
        updated_at = now()
    FROM next_order
    WHERE d.user_id = $1 AND d.channel_id = $2
      AND EXISTS (SELECT 1 FROM target)
      AND (d.pinned IS DISTINCT FROM $3::boolean OR ($3::boolean AND d.pinned_order = 0))
    RETURNING COALESCE(d.folder_id, 0)::int AS folder_id
)
SELECT EXISTS (SELECT 1 FROM updated)::boolean,
       COALESCE((SELECT folder_id FROM updated), (SELECT folder_id FROM self_folder), 0)::int`,
		userID, channelID, pinned).Scan(&changed, &folderID); err != nil {
		return false, 0, fmt.Errorf("set channel dialog pinned: %w", err)
	}
	return changed, int(folderID), nil
}

func (s *ChannelStore) ReorderChannelPinnedDialogs(ctx context.Context, userID int64, folderID int, order []domain.Peer, force bool) (bool, error) {
	if userID == 0 {
		return false, nil
	}
	peerTypes, peerIDs := peerArrays(order)
	changed := false
	if force {
		tag, err := s.db.Exec(ctx, `
WITH requested AS (
    SELECT ($2::text[])[i] AS peer_type, ($3::bigint[])[i] AS peer_id
    FROM generate_subscripts($3::bigint[], 1) AS g(i)
    WHERE i <= cardinality($2::text[])
)
UPDATE channel_dialogs d
SET pinned = false, pinned_order = 0, updated_at = now()
WHERE d.user_id = $1
  AND d.pinned
  AND COALESCE(d.folder_id, 0) = $4::int
  AND NOT EXISTS (
      SELECT 1 FROM requested r
      WHERE r.peer_type = 'channel' AND r.peer_id = d.channel_id
  )`, userID, peerTypes, peerIDs, folderID)
		if err != nil {
			return false, fmt.Errorf("clear channel pinned dialogs not in order: %w", err)
		}
		if tag.RowsAffected() > 0 {
			changed = true
		}
	}
	if len(peerIDs) == 0 {
		return changed, nil
	}
	tag, err := s.db.Exec(ctx, `
WITH requested AS (
    SELECT ($2::text[])[i] AS peer_type, ($3::bigint[])[i] AS peer_id, i::int AS pos
    FROM generate_subscripts($3::bigint[], 1) AS g(i)
    WHERE i <= cardinality($2::text[])
),
deduped AS (
    SELECT DISTINCT ON (peer_id) peer_id, (cardinality($3::bigint[]) - pos + 1)::int AS ord
    FROM requested
    WHERE peer_type = 'channel'
    ORDER BY peer_id, pos
)
UPDATE channel_dialogs d
SET pinned = true, pinned_order = deduped.ord, updated_at = now()
FROM deduped
WHERE d.user_id = $1 AND d.channel_id = deduped.peer_id
  AND COALESCE(d.folder_id, 0) = $4::int
  AND (NOT d.pinned OR d.pinned_order IS DISTINCT FROM deduped.ord)`, userID, peerTypes, peerIDs, folderID)
	if err != nil {
		return false, fmt.Errorf("reorder channel pinned dialogs: %w", err)
	}
	if tag.RowsAffected() > 0 {
		changed = true
	}
	return changed, nil
}

func (s *ChannelStore) EditChannelPeerFolders(ctx context.Context, userID int64, peers []domain.FolderPeerUpdate) error {
	if userID == 0 || len(peers) == 0 {
		return nil
	}
	peerTypes := make([]string, 0, len(peers))
	peerIDs := make([]int64, 0, len(peers))
	folderIDs := make([]int32, 0, len(peers))
	seen := make(map[int64]struct{}, len(peers))
	for _, item := range peers {
		if item.Peer.Type != domain.PeerTypeChannel || item.Peer.ID == 0 {
			continue
		}
		if item.FolderID != domain.DialogMainFolderID && item.FolderID != domain.DialogArchiveFolderID {
			continue
		}
		if _, ok := seen[item.Peer.ID]; ok {
			continue
		}
		seen[item.Peer.ID] = struct{}{}
		peerTypes = append(peerTypes, string(item.Peer.Type))
		peerIDs = append(peerIDs, item.Peer.ID)
		folderIDs = append(folderIDs, int32(item.FolderID))
	}
	if len(peerIDs) == 0 {
		return nil
	}
	// 归档必须 ensure-INSERT：从未读过/置顶过的频道还没有 dialog 行，
	// 只 UPDATE 会让归档静默丢失；新行同时带上 member 真实水位，
	// 避免 0 水位缓存行遮蔽未读/已读状态。
	if _, err := s.db.Exec(ctx, `
WITH requested AS (
    SELECT ($2::text[])[i] AS peer_type, ($3::bigint[])[i] AS channel_id, ($4::int[])[i] AS folder_id
    FROM generate_subscripts($3::bigint[], 1) AS g(i)
    WHERE i <= cardinality($2::text[]) AND i <= cardinality($4::int[])
),
deduped AS (
    SELECT DISTINCT ON (channel_id) channel_id, folder_id
    FROM requested
    WHERE peer_type = 'channel' AND folder_id IN (0, 1)
    ORDER BY channel_id
)
INSERT INTO channel_dialogs (user_id, channel_id, folder_id, top_message_id, read_inbox_max_id, read_outbox_max_id, unread_count, updated_at)
SELECT $1, m.channel_id, deduped.folder_id,
       CASE WHEN c.top_message_id > m.available_min_id THEN c.top_message_id ELSE 0 END,
       m.read_inbox_max_id, m.read_outbox_max_id,
       (
           SELECT COUNT(*)::int
           FROM channel_messages cm
           WHERE cm.channel_id = m.channel_id
             AND cm.id > GREATEST(m.read_inbox_max_id, m.available_min_id)
             AND NOT cm.deleted
             AND cm.sender_user_id <> $1
       ),
       now()
FROM deduped
JOIN channel_members m ON m.user_id = $1 AND m.channel_id = deduped.channel_id AND m.status = 'active'
JOIN channels c ON c.id = m.channel_id AND NOT c.deleted
ON CONFLICT (user_id, channel_id) DO UPDATE
-- 换 folder 时清 pinned：与私聊 EditDialogPeerFolders 一致，TDesktop
-- 在归档/还原时本地无条件 unpin，服务端保留旧 pin 会造成状态漂移。
SET folder_id = EXCLUDED.folder_id,
    pinned = CASE WHEN COALESCE(channel_dialogs.folder_id, 0) <> EXCLUDED.folder_id THEN false ELSE channel_dialogs.pinned END,
    pinned_order = CASE WHEN COALESCE(channel_dialogs.folder_id, 0) <> EXCLUDED.folder_id THEN 0 ELSE channel_dialogs.pinned_order END,
    updated_at = now()`, userID, peerTypes, peerIDs, folderIDs); err != nil {
		return fmt.Errorf("edit channel peer folders: %w", err)
	}
	return nil
}

func (s *ChannelStore) CountChannelArchiveUnread(ctx context.Context, userID int64) (int, int, error) {
	if userID == 0 {
		return 0, 0, nil
	}
	var peers, messages int32
	// JOIN active member：退群残留的 channel_dialogs 行不计入归档徽章。
	// unread 读时动态派生（H4a）：不再消费 channel_dialogs.unread_count 缓存列；归档集合
	// 有界（需显式归档建行），每行动态 COUNT 已被 cap 钳制。
	visibleTopID := "CASE WHEN c.top_message_id > m.available_min_id THEN c.top_message_id ELSE 0 END"
	visibleReadInbox := "GREATEST(COALESCE(d.read_inbox_max_id, 0), m.read_inbox_max_id)"
	if err := s.db.QueryRow(ctx, `
SELECT
  COUNT(*) FILTER (WHERE t.unread_count > 0 OR t.unread_mark)::int,
  COALESCE(SUM(t.unread_count), 0)::int
FROM (
  SELECT `+channelDialogVisibleUnreadCountSQL(visibleReadInbox, visibleTopID)+` AS unread_count,
         COALESCE(d.unread_mark, m.unread_mark) AS unread_mark
  FROM channel_dialogs d
  JOIN channel_members m ON m.channel_id = d.channel_id AND m.user_id = d.user_id AND m.status = 'active'
  JOIN channels c ON c.id = d.channel_id AND NOT c.deleted
  WHERE d.user_id = $1
    AND COALESCE(d.folder_id, 0) = 1
) t`, userID).Scan(&peers, &messages); err != nil {
		return 0, 0, fmt.Errorf("count channel archive unread: %w", err)
	}
	return int(peers), int(messages), nil
}

func (s *ChannelStore) getChannelDialog(ctx context.Context, db sqlcgen.DBTX, userID int64, channel domain.Channel) (domain.ChannelDialog, error) {
	if s.dialogCacheActive(db) {
		return s.dialogCache.getOrLoad(ctx, userID, channel.ID, func() (domain.ChannelDialog, error) {
			return s.getChannelDialogUncached(ctx, db, userID, channel)
		})
	}
	return s.getChannelDialogUncached(ctx, db, userID, channel)
}

func (s *ChannelStore) getChannelDialogUncached(ctx context.Context, db sqlcgen.DBTX, userID int64, channel domain.Channel) (domain.ChannelDialog, error) {
	dialog := domain.ChannelDialog{UserID: userID, ChannelID: channel.ID, TopMessageID: channel.TopMessageID}
	var defaultSendAsType sql.NullString
	var defaultSendAsID sql.NullInt64
	visibleTopID := "CASE WHEN c.top_message_id > m.available_min_id THEN c.top_message_id ELSE 0 END"
	visibleReadInbox := "GREATEST(COALESCE(d.read_inbox_max_id, 0), m.read_inbox_max_id)"
	visibleUnreadCount := channelDialogVisibleUnreadCountSQL(visibleReadInbox, visibleTopID)
	// 单频道 TopMessageDate 用 LEFT JOIN top_msg 直接取,替代此前对每个频道再单查一次
	// getChannelMessage 的额外往返(chf-3,实测 pg_stat "频道消息按id" 来源之一)。严格
	// 保持原语义:仅当 top 可见(top_message_id > available_min_id)且消息行存在时取其
	// message_date(不过滤 deleted,与原 getChannelMessage 一致),否则回退
	// COALESCE(d.top_message_date, c.date)。注意:与批量版 getChannelDialogs 的隐藏态
	// (ELSE 0)刻意保持各自既有差异,本改动只去往返、不改单频道输出。
	visibleTopDate := "CASE WHEN c.top_message_id > m.available_min_id AND top_msg.id IS NOT NULL THEN top_msg.message_date ELSE COALESCE(d.top_message_date, c.date) END"
	err := db.QueryRow(ctx, `
SELECT `+visibleTopID+`,
       `+visibleTopDate+`,
       COALESCE(d.folder_id, 0),
       `+visibleReadInbox+`,
       LEAST(GREATEST(c.top_message_id, 0), GREATEST(COALESCE(d.read_outbox_max_id, 0), m.read_outbox_max_id, CASE WHEN c.read_inbox_top1_user_id = m.user_id THEN c.read_inbox_top2 ELSE c.read_inbox_top1 END)),
       `+visibleUnreadCount+`,
       COALESCE(d.pinned, false),
       COALESCE(d.pinned_order, 0),
       COALESCE(d.unread_mark, m.unread_mark),
       COALESCE(d.unread_mentions_count, 0),
       COALESCE(d.unread_reactions_count, 0),
       COALESCE(d.view_forum_as_messages, false),
       COALESCE(d.has_scheduled, false),
       d.default_send_as_peer_type,
       d.default_send_as_peer_id
FROM channels c
JOIN channel_members m ON m.channel_id = c.id AND m.user_id = $1
LEFT JOIN channel_messages top_msg ON top_msg.channel_id = c.id AND top_msg.id = c.top_message_id
LEFT JOIN channel_dialogs d ON d.user_id = m.user_id AND d.channel_id = m.channel_id
WHERE c.id = $2`, userID, channel.ID).Scan(
		&dialog.TopMessageID,
		&dialog.TopMessageDate,
		&dialog.FolderID,
		&dialog.ReadInboxMaxID,
		&dialog.ReadOutboxMaxID,
		&dialog.UnreadCount,
		&dialog.Pinned,
		&dialog.PinnedOrder,
		&dialog.UnreadMark,
		&dialog.UnreadMentions,
		&dialog.UnreadReactions,
		&dialog.ViewForumAsMessages,
		&dialog.HasScheduled,
		&defaultSendAsType,
		&defaultSendAsID,
	)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return domain.ChannelDialog{}, fmt.Errorf("get channel dialog: %w", err)
	}
	if defaultSendAsType.Valid && defaultSendAsID.Valid && defaultSendAsID.Int64 != 0 {
		dialog.DefaultSendAs = &domain.Peer{Type: domain.PeerType(defaultSendAsType.String), ID: defaultSendAsID.Int64}
	}
	return dialog, nil
}

func (s *ChannelStore) getChannelDialogs(ctx context.Context, db sqlcgen.DBTX, userID int64, channelIDs []int64) (map[int64]domain.ChannelDialog, error) {
	if userID == 0 || len(channelIDs) == 0 {
		return nil, nil
	}
	if s.dialogCacheActive(db) {
		out := make(map[int64]domain.ChannelDialog, len(channelIDs))
		misses := make([]int64, 0, len(channelIDs))
		seen := make(map[int64]struct{}, len(channelIDs))
		for _, channelID := range channelIDs {
			if channelID == 0 {
				continue
			}
			if _, ok := seen[channelID]; ok {
				continue
			}
			seen[channelID] = struct{}{}
			if dialog, ok := s.dialogCache.get(userID, channelID); ok {
				out[channelID] = dialog
				continue
			}
			misses = append(misses, channelID)
		}
		if len(misses) == 0 {
			return out, nil
		}
		// 暖写回前快照 epoch:miss-load 期间若收到失效则拒绝陈旧投影写回(同 ListChannelDialogs)。
		loadEpoch := s.dialogCache.cacheEpoch()
		loaded, err := s.getChannelDialogsUncached(ctx, db, userID, misses)
		if err != nil {
			return nil, err
		}
		for channelID, dialog := range loaded {
			s.dialogCache.putIfEpoch(dialog, loadEpoch)
			out[channelID] = dialog
		}
		return out, nil
	}
	return s.getChannelDialogsUncached(ctx, db, userID, channelIDs)
}

func (s *ChannelStore) getChannelDialogsUncached(ctx context.Context, db sqlcgen.DBTX, userID int64, channelIDs []int64) (map[int64]domain.ChannelDialog, error) {
	if userID == 0 || len(channelIDs) == 0 {
		return nil, nil
	}
	visibleTopID := "CASE WHEN c.top_message_id > m.available_min_id THEN c.top_message_id ELSE 0 END"
	visibleTopDate := "CASE WHEN c.top_message_id > m.available_min_id THEN COALESCE(top_msg.message_date, d.top_message_date, c.date) ELSE 0 END"
	visibleReadInbox := "GREATEST(COALESCE(d.read_inbox_max_id, 0), m.read_inbox_max_id)"
	visibleUnreadCount := channelDialogVisibleUnreadCountSQL(visibleReadInbox, visibleTopID)
	rows, err := db.Query(ctx, `
SELECT c.id,
       `+visibleTopID+`,
       `+visibleTopDate+`,
       COALESCE(d.folder_id, 0),
       `+visibleReadInbox+`,
       LEAST(GREATEST(c.top_message_id, 0), GREATEST(COALESCE(d.read_outbox_max_id, 0), m.read_outbox_max_id, CASE WHEN c.read_inbox_top1_user_id = m.user_id THEN c.read_inbox_top2 ELSE c.read_inbox_top1 END)),
       `+visibleUnreadCount+`,
       COALESCE(d.pinned, false),
       COALESCE(d.pinned_order, 0),
       COALESCE(d.unread_mark, m.unread_mark),
       COALESCE(d.unread_mentions_count, 0),
       COALESCE(d.unread_reactions_count, 0),
       COALESCE(d.view_forum_as_messages, false),
       COALESCE(d.has_scheduled, false),
       d.default_send_as_peer_type,
       d.default_send_as_peer_id
FROM channels c
JOIN channel_members m ON m.channel_id = c.id AND m.user_id = $1
LEFT JOIN channel_messages top_msg ON top_msg.channel_id = c.id AND top_msg.id = c.top_message_id AND NOT top_msg.deleted
LEFT JOIN channel_dialogs d ON d.user_id = m.user_id AND d.channel_id = m.channel_id
WHERE c.id = ANY($2::bigint[])`, userID, channelIDs)
	if err != nil {
		return nil, fmt.Errorf("get channel dialogs: %w", err)
	}
	defer rows.Close()
	out := make(map[int64]domain.ChannelDialog, len(channelIDs))
	for rows.Next() {
		var dialog domain.ChannelDialog
		var defaultSendAsType sql.NullString
		var defaultSendAsID sql.NullInt64
		if err := rows.Scan(
			&dialog.ChannelID,
			&dialog.TopMessageID,
			&dialog.TopMessageDate,
			&dialog.FolderID,
			&dialog.ReadInboxMaxID,
			&dialog.ReadOutboxMaxID,
			&dialog.UnreadCount,
			&dialog.Pinned,
			&dialog.PinnedOrder,
			&dialog.UnreadMark,
			&dialog.UnreadMentions,
			&dialog.UnreadReactions,
			&dialog.ViewForumAsMessages,
			&dialog.HasScheduled,
			&defaultSendAsType,
			&defaultSendAsID,
		); err != nil {
			return nil, err
		}
		dialog.UserID = userID
		if defaultSendAsType.Valid && defaultSendAsID.Valid && defaultSendAsID.Int64 != 0 {
			dialog.DefaultSendAs = &domain.Peer{Type: domain.PeerType(defaultSendAsType.String), ID: defaultSendAsID.Int64}
		}
		out[dialog.ChannelID] = dialog
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("get channel dialogs: %w", err)
	}
	return out, nil
}

// cappedChannelUnreadCount 生成把扫描行数钳到 MaxDialogUnreadCount 的未读 COUNT 子查询，
// 与读路径 channelDialogDynamicUnreadCountSQL 口径一致。写 fan-out 路径此前是裸 COUNT 无
// LIMIT，久未读成员（read 远低于 top）会退化为 O(未读积压) 全扫；用 LIMIT 内层子查询封顶
// 单成员扫描工作量，写出的缓存列与读路径下发角标同为 min(实际, MaxDialogUnreadCount)。
// whereBody 是 channel_messages 别名 msg 上的完整 WHERE 子句。
func cappedChannelUnreadCount(whereBody string) string {
	return fmt.Sprintf(`(
        SELECT COUNT(*)::int
        FROM (
            SELECT 1
            FROM channel_messages msg
            %s
            LIMIT %d
        ) msg_capped
    )`, whereBody, domain.MaxDialogUnreadCount)
}

func upsertChannelDialogTx(ctx context.Context, tx pgx.Tx, userID int64, channel domain.Channel, top domain.ChannelMessage, readInboxMaxID, readOutboxMaxID int) error {
	topDate := top.Date
	if topDate == 0 {
		topDate = channel.Date
	}
	unread, err := countChannelUnreadMessages(ctx, tx, userID, channel.ID, readInboxMaxID, channel.TopMessageID)
	if err != nil {
		return err
	}
	conflictUnread := cappedChannelUnreadCount(`WHERE msg.channel_id = channel_dialogs.channel_id
          AND msg.id > GREATEST(channel_dialogs.read_inbox_max_id, EXCLUDED.read_inbox_max_id)
          AND msg.id <= GREATEST(channel_dialogs.top_message_id, EXCLUDED.top_message_id)
          AND NOT msg.deleted
          AND msg.sender_user_id <> channel_dialogs.user_id`)
	if _, err := tx.Exec(ctx, fmt.Sprintf(`
INSERT INTO channel_dialogs (
    user_id, channel_id, top_message_id, top_message_date, read_inbox_max_id, read_outbox_max_id, unread_count, unread_mark
) VALUES ($1,$2,$3,$4,$5,$6,$7,false)
ON CONFLICT (user_id, channel_id) DO UPDATE SET
    top_message_id = GREATEST(channel_dialogs.top_message_id, EXCLUDED.top_message_id),
    top_message_date = GREATEST(channel_dialogs.top_message_date, EXCLUDED.top_message_date),
    read_inbox_max_id = GREATEST(channel_dialogs.read_inbox_max_id, EXCLUDED.read_inbox_max_id),
    read_outbox_max_id = GREATEST(channel_dialogs.read_outbox_max_id, EXCLUDED.read_outbox_max_id),
    unread_count = %s,
    unread_mark = false,
    updated_at = now()`, conflictUnread),
		userID, channel.ID, channel.TopMessageID, topDate, readInboxMaxID, readOutboxMaxID, unread); err != nil {
		return fmt.Errorf("upsert channel dialog: %w", err)
	}
	return nil
}

// upsertChannelDialogsForMessageTx 在发送事务内处理消息的收件人侧读边界副作用。
//
// 性能审计 H4a：不再对全员（旧闸门为 megagroup ≤ MaxSynchronousChannelDialogFanout）
// upsert channel_dialogs——发送事务 O(成员) 行写 + 行锁是放开大群的阻断项（P1-u 残余）。
// 所有频道（broadcast / megagroup 不分大小）统一读时派生：unread 由可见 incoming 消息
// 重算（channelDialogDynamicUnreadCountSQL，capped），top 由 channels.top_message_id 提供，
// read boundary 真值在 channel_members，dialog 行仅承载 pin/folder/unread_mark/draft 等
// 用户显式状态并按需惰性建行。
//
// 保留的唯一逐收件人写：skipDelivery（privacy bot）成员的 read_inbox 边界推进——这些
// 成员被排除投递，必须同步抬边界避免其未读派生把被隐藏消息计成 unread；集合有界
// （bot-only 候选）。
func upsertChannelDialogsForMessageTx(ctx context.Context, tx pgx.Tx, channel domain.Channel, top domain.ChannelMessage, selfReadUserID int64, skipDeliveryUserIDs ...[]int64) error {
	if channel.ID == 0 || top.ID == 0 {
		return nil
	}
	skipIDs := []int64{}
	if len(skipDeliveryUserIDs) > 0 {
		skipIDs = uniqueChannelUserIDs(skipDeliveryUserIDs[0], selfReadUserID)
	}
	if len(skipIDs) > 0 {
		if _, err := tx.Exec(ctx, `
UPDATE channel_members
SET read_inbox_max_id = GREATEST(read_inbox_max_id, $3),
    unread_mark = false,
    updated_at = now()
WHERE channel_id = $1
  AND user_id = ANY($2::bigint[])
  AND status = 'active'
  AND NOT COALESCE((banned_rights->>'ViewMessages')::boolean, false)
  AND $3 > available_min_id`, channel.ID, skipIDs, top.ID); err != nil {
			return fmt.Errorf("advance skipped channel delivery boundary: %w", err)
		}
	}
	// skipDelivery 成员可能已有 channel_dialogs 行（pin/归档等惰性建行），其 read_inbox
	// 经 GREATEST(d, m) 读取——member 行已推进即可，dialog 行无需同步。
	return nil
}

func scanChannelDialogRow(row rowScanner, userID int64) (domain.Channel, domain.Dialog, *domain.Peer, error) {
	var ch domain.Channel
	var rights, reactionPolicy string
	var wallpaper *string
	var topID, topDate, folderID, readInbox, readOutbox, unreadCount, pinnedOrder, unreadMentions, unreadReactions int
	var pinned, unreadMark, viewForumAsMessages, hasScheduled bool
	var defaultSendAsType sql.NullString
	var defaultSendAsID sql.NullInt64
	dest := append(channelScanDest(&ch, &rights, &reactionPolicy, &wallpaper),
		&topID, &topDate,
		&folderID, &readInbox, &readOutbox, &unreadCount, &pinned, &pinnedOrder, &unreadMark, &unreadMentions, &unreadReactions, &viewForumAsMessages, &hasScheduled,
		&defaultSendAsType, &defaultSendAsID,
	)
	if err := row.Scan(dest...); err != nil {
		return domain.Channel{}, domain.Dialog{}, nil, err
	}
	finishChannelScan(&ch, rights, reactionPolicy, wallpaper)
	dialog := domain.Dialog{
		Peer:                domain.Peer{Type: domain.PeerTypeChannel, ID: ch.ID},
		FolderID:            folderID,
		TopMessage:          topID,
		TopMessageDate:      topDate,
		ReadInboxMaxID:      readInbox,
		ReadOutboxMaxID:     readOutbox,
		UnreadCount:         unreadCount,
		UnreadMentions:      unreadMentions,
		UnreadReactions:     unreadReactions,
		Pinned:              pinned,
		PinnedOrder:         pinnedOrder,
		UnreadMark:          unreadMark,
		ViewForumAsMessages: viewForumAsMessages,
		HasScheduled:        hasScheduled,
		Pts:                 ch.Pts,
	}
	var defaultSendAs *domain.Peer
	if defaultSendAsType.Valid && defaultSendAsID.Valid && defaultSendAsID.Int64 != 0 {
		defaultSendAs = &domain.Peer{Type: domain.PeerType(defaultSendAsType.String), ID: defaultSendAsID.Int64}
	}
	_ = userID
	return ch, dialog, defaultSendAs, nil
}

func channelDialogToDialog(dialog domain.ChannelDialog, channelPts int) domain.Dialog {
	return domain.Dialog{
		Peer:                domain.Peer{Type: domain.PeerTypeChannel, ID: dialog.ChannelID},
		FolderID:            dialog.FolderID,
		TopMessage:          dialog.TopMessageID,
		TopMessageDate:      dialog.TopMessageDate,
		ReadInboxMaxID:      dialog.ReadInboxMaxID,
		ReadOutboxMaxID:     dialog.ReadOutboxMaxID,
		UnreadCount:         dialog.UnreadCount,
		UnreadMentions:      dialog.UnreadMentions,
		UnreadReactions:     dialog.UnreadReactions,
		Pinned:              dialog.Pinned,
		PinnedOrder:         dialog.PinnedOrder,
		UnreadMark:          dialog.UnreadMark,
		ViewForumAsMessages: dialog.ViewForumAsMessages,
		HasScheduled:        dialog.HasScheduled,
		Pts:                 channelPts,
	}
}

func channelDialogFromDialog(userID int64, dialog domain.Dialog) domain.ChannelDialog {
	return domain.ChannelDialog{
		UserID:              userID,
		ChannelID:           dialog.Peer.ID,
		FolderID:            dialog.FolderID,
		TopMessageID:        dialog.TopMessage,
		TopMessageDate:      dialog.TopMessageDate,
		ReadInboxMaxID:      dialog.ReadInboxMaxID,
		ReadOutboxMaxID:     dialog.ReadOutboxMaxID,
		UnreadCount:         dialog.UnreadCount,
		UnreadMentions:      dialog.UnreadMentions,
		UnreadReactions:     dialog.UnreadReactions,
		Pinned:              dialog.Pinned,
		PinnedOrder:         dialog.PinnedOrder,
		UnreadMark:          dialog.UnreadMark,
		ViewForumAsMessages: dialog.ViewForumAsMessages,
		HasScheduled:        dialog.HasScheduled,
	}
}

func channelDialogMatchesFilter(dialog domain.Dialog, channel domain.Channel, filter domain.DialogFilter) bool {
	if filter.HasFolderID {
		if filter.FolderID < domain.DialogCustomFolderMinID {
			if dialog.FolderID != filter.FolderID {
				return false
			}
		} else if filter.Folder == nil {
			return false
		}
	}
	if filter.PinnedOnly && !dialog.Pinned {
		return false
	}
	if filter.ExcludePinned && dialog.Pinned {
		return false
	}
	if !channelDialogAfterOffset(dialog, filter) {
		return false
	}
	if filter.Folder == nil {
		return true
	}
	folder := filter.Folder
	if peerInDialogFolder(dialog.Peer, folder.ExcludePeers) {
		return false
	}
	if folder.ExcludeRead && dialog.UnreadCount == 0 && !dialog.UnreadMark {
		return false
	}
	if folder.ExcludeArchived && dialog.FolderID == domain.DialogArchiveFolderID {
		return false
	}
	if peerInDialogFolder(dialog.Peer, folder.IncludePeers) || peerInDialogFolder(dialog.Peer, folder.PinnedPeers) {
		return true
	}
	if channel.Megagroup && folder.Groups {
		return true
	}
	if channel.Broadcast && folder.Broadcasts {
		return true
	}
	// 群/频道只能经 Groups/Broadcasts 类别开关或显式 include/pinned 进入自定义
	// 文件夹；仅勾 Contacts/NonContacts/Bots（私聊类别）的文件夹不含任何群/频道。
	return false
}

func channelDialogAfterOffset(dialog domain.Dialog, filter domain.DialogFilter) bool {
	if filter.OffsetDate <= 0 && filter.OffsetID <= 0 {
		if filter.HasOffsetPeer && filter.OffsetPeer == dialog.Peer {
			return false
		}
		return true
	}
	if filter.OffsetDate > 0 {
		if dialog.TopMessageDate != filter.OffsetDate {
			return dialog.TopMessageDate < filter.OffsetDate
		}
		if filter.OffsetID <= 0 {
			return false
		}
		if dialog.TopMessage != filter.OffsetID {
			return dialog.TopMessage < filter.OffsetID
		}
		if filter.HasOffsetPeer && filter.OffsetPeer.Type == dialog.Peer.Type {
			return dialog.Peer.ID < filter.OffsetPeer.ID
		}
		return false
	}
	if dialog.TopMessage != filter.OffsetID {
		return dialog.TopMessage < filter.OffsetID
	}
	if filter.HasOffsetPeer && filter.OffsetPeer.Type == dialog.Peer.Type {
		return dialog.Peer.ID < filter.OffsetPeer.ID
	}
	return false
}

func peerInDialogFolder(peer domain.Peer, items []domain.DialogFolderPeer) bool {
	for _, item := range items {
		if item.Peer == peer {
			return true
		}
	}
	return false
}

func channelFolderPeerIDs(primary []domain.DialogFolderPeer, rest ...[]domain.DialogFolderPeer) []int64 {
	total := len(primary)
	for _, items := range rest {
		total += len(items)
	}
	seen := make(map[int64]struct{}, minInt(total, domain.MaxDialogFolderPeers))
	out := make([]int64, 0, minInt(total, domain.MaxDialogFolderPeers))
	appendOne := func(items []domain.DialogFolderPeer) {
		for _, item := range items {
			if len(out) >= domain.MaxDialogFolderPeers {
				return
			}
			if item.Peer.Type != domain.PeerTypeChannel || item.Peer.ID == 0 {
				continue
			}
			if _, ok := seen[item.Peer.ID]; ok {
				continue
			}
			seen[item.Peer.ID] = struct{}{}
			out = append(out, item.Peer.ID)
		}
	}
	appendOne(primary)
	for _, items := range rest {
		appendOne(items)
	}
	return out
}

func previewChannelDialog(userID int64, channel domain.Channel, member domain.ChannelMember) domain.ChannelDialog {
	topMessageID := channel.TopMessageID
	if topMessageID <= member.AvailableMinID {
		topMessageID = 0
	}
	return domain.ChannelDialog{
		UserID:          userID,
		ChannelID:       channel.ID,
		TopMessageID:    topMessageID,
		TopMessageDate:  channel.Date,
		ReadInboxMaxID:  maxInt(channel.TopMessageID, member.ReadInboxMaxID),
		ReadOutboxMaxID: maxInt(channel.TopMessageID, member.ReadOutboxMaxID),
	}
}
