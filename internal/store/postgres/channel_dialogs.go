package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"sort"
	"strings"
	"telesrv/internal/domain"
	"telesrv/internal/store/postgres/sqlcgen"
)

type channelDialogListItem struct {
	channel            domain.Channel
	dialog             domain.Dialog
	defaultSendAs      *domain.Peer
	topMentioned       bool
	topMediaUnread     bool
	topUnreadProjected bool
	listCacheable      bool
}

func channelDialogVisibleTopIDSQL() string {
	return `CASE
    WHEN c.top_message_id > m.available_min_id THEN c.top_message_id
    WHEN m.history_clear_anchor_id > 0
      AND m.history_clear_anchor_id = m.available_min_id
      THEN m.history_clear_anchor_id
    ELSE 0
END`
}

func channelDialogVisibleTopDateSQL(globalTopDate, emptyFallback string) string {
	return `CASE
    WHEN c.top_message_id > m.available_min_id THEN ` + globalTopDate + `
    WHEN m.history_clear_anchor_id > 0
      AND m.history_clear_anchor_id = m.available_min_id
      THEN m.history_clear_anchor_date
    ELSE ` + emptyFallback + `
END`
}

func (s *ChannelStore) ListChannelDialogs(ctx context.Context, viewerUserID int64, filter domain.DialogFilter) (domain.ChannelDialogList, error) {
	return s.listChannelDialogs(ctx, viewerUserID, filter)
}

func (s *ChannelStore) listChannelDialogs(ctx context.Context, viewerUserID int64, filter domain.DialogFilter) (domain.ChannelDialogList, error) {
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
	visibleTopID := channelDialogVisibleTopIDSQL()
	visibleTopDate := channelDialogVisibleTopDateSQL("COALESCE(top_msg.message_date, d.top_message_date, c.date)", "0")
	visibleReadInbox := "GREATEST(COALESCE(d.read_inbox_max_id, 0), m.read_inbox_max_id)"
	visibleUnreadCount := channelDialogVisibleUnreadCountSQL(visibleReadInbox, visibleTopID)
	args := []any{viewerUserID, channelIDs}
	where := []string{"m.user_id = $1", "m.channel_id = ANY($2::bigint[])", "m.status = 'active'"}
	from := `FROM channel_members m
JOIN channels c ON c.id = m.channel_id AND c.id = ANY($2::bigint[]) AND NOT c.deleted
LEFT JOIN channel_messages top_msg ON top_msg.channel_id = m.channel_id AND top_msg.channel_id = ANY($2::bigint[]) AND top_msg.id = c.top_message_id AND NOT top_msg.deleted
LEFT JOIN channel_dialogs d ON d.user_id = m.user_id AND d.channel_id = m.channel_id`
	// The archive and pinned sets always have explicit channel_dialogs rows. Use that owner index as the entry
	// to avoid archive summary(limit=1) and getPinnedDialogs scanning all of the account's active memberships
	// for a collection that is usually empty/tiny. Keep the $2 typed no-op so placeholder numbering for later
	// dynamic conditions needs no branching; monoforum is still merged after the main query via the original permission path.
	if (filter.HasFolderID && filter.FolderID == domain.DialogArchiveFolderID) || filter.PinnedOnly {
		from = `FROM channel_dialogs d
JOIN channel_members m ON m.user_id = d.user_id AND m.channel_id = d.channel_id AND m.status = 'active'
JOIN channels c ON c.id = d.channel_id AND NOT c.deleted
LEFT JOIN channel_messages top_msg ON top_msg.channel_id = d.channel_id AND top_msg.id = c.top_message_id AND NOT top_msg.deleted`
		where = []string{"d.user_id = $1", "cardinality($2::bigint[]) >= 0"}
	}
	if filter.HasFolderID && filter.FolderID < domain.DialogCustomFolderMinID {
		args = append(args, filter.FolderID)
		where = append(where, fmt.Sprintf("COALESCE(d.folder_id, 0) = $%d", len(args)))
	} else if !filter.HasFolderID {
		// No folder_id is treated as the main list (folder 0), consistent with the private-chat side / official semantics.
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
			// When a custom filter declares no positive channel-dimension condition (only private-chat
			// category switches like Contacts/NonContacts/Bots), include no groups/channels—matching the
			// final return false of channelDialogMatchesFilter, to avoid pouring all groups/channels the user joined into that folder.
			where = append(where, "false")
		}
	}
	// Fetch only one extra row as has-more evidence. The old implementation ignored the RPC limit and always read 500 full
	// channel + viewer dialogs, deriving unread dynamically per row; getDialogs(limit=100)
	// therefore hydrated up to 5x the objects, and archive summary(limit=1) was amplified up to 500x.
	// All folder/offset conditions are already applied before the SQL LIMIT, and monoforum results are also
	// merged and sorted below, so taking limit+1 per source is enough to build the correct merged page and continue-pagination signal.
	queryLimit := limit + 1
	args = append(args, queryLimit)
	limitArg := fmt.Sprintf("$%d", len(args))
	// Warm write-back epoch guard: snapshot before loading; on write-back, reject the stale projection if an
	// invalidation arrived meanwhile (epoch changed), preventing a concurrent invalidation in the scan→put window from being overwritten by a bare Store (@ badge/unread lost-update).
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
       d.default_send_as_peer_id,
       m.history_clear_anchor_id,
       m.history_clear_anchor_date
	`+from+`
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
	// hasBroadcastAdmin is a page-independent superset gate: only a user who is creator/admin in some
	// broadcast channel can have a monoforum direct-messages dialog (linked_monoforum_id<>0 is a subset of that set). Most getDialogs callers do not match,
	// so this skips the extra multi-table JOIN round trip of listMonoforumAdminDialogItems (previously run unconditionally on every getDialogs).
	// It does not use a row-level flag in the main scan loop, because the main query carries an offset cursor: when paging to later pages the parent broadcast channel may already have been consumed by the
	// previous page and no longer scanned, while its direct-messages dialog (independent sort position) still belongs to this page—a row-level flag would miss it. The superset gate reads the full candidate set and is page-independent.
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
			// The list warm-cache value must be equivalent to the single-channel full read path: add the
			// default_send_as that exists only in channel_dialogs, otherwise getFullChannel/getSendAs hitting the warm cache would lose the "send as channel" default.
			cd := channelDialogFromDialog(viewerUserID, item.dialog)
			cd.DefaultSendAs = item.defaultSendAs
			s.dialogCache.putListProjectionIfEpoch(cd, false, false, false, dialogCacheEpoch)
		}
		out.Dialogs = append(out.Dialogs, item.dialog)
		out.Channels = append(out.Channels, item.channel)
	}
	// getDialogs' top message must be augmented per viewer with mentioned/media_unread and
	// reactions: TDesktop caches it first and it is not overwritten by the
	// full version from later difference/getHistory, so missing flags keep the client from ever reporting contents-read, and the @ badge returns after restart.
	if err := s.populateChannelDialogTopMessageReactions(ctx, s.db, viewerUserID, out.Channels, out.Messages, false); err != nil {
		return domain.ChannelDialogList{}, err
	}
	projectChannelDialogHistoryClearMessages(out.Dialogs, out.Messages)
	return out, nil
}

// ListChannelDialogSnapshotHeaders builds the bounded owner-specific ordering
// index used by app pagination. It intentionally excludes channel metadata and
// top-message payloads; those are hydrated per page through versioned peer read
// models, so one owner snapshot remains lightweight and shared channel facts do
// not get duplicated for every online account.
func (s *ChannelStore) ListChannelDialogSnapshotHeaders(ctx context.Context, viewerUserID int64, filter domain.DialogFilter) (domain.ChannelDialogList, error) {
	if viewerUserID == 0 {
		return domain.ChannelDialogList{}, nil
	}
	if filter.Folder != nil || (filter.HasFolderID && filter.FolderID >= domain.DialogCustomFolderMinID) ||
		filter.OffsetDate != 0 || filter.OffsetID != 0 || filter.HasOffsetPeer {
		return domain.ChannelDialogList{}, errors.New("channel dialog snapshot headers require an offset-free built-in folder")
	}
	channelIDs, hasBroadcastAdmin, err := s.listActiveChannelDialogCandidateIDs(ctx, viewerUserID, false)
	if err != nil {
		return domain.ChannelDialogList{}, err
	}
	if len(channelIDs) == 0 {
		return domain.ChannelDialogList{}, nil
	}
	visibleTopID := channelDialogVisibleTopIDSQL()
	visibleTopDate := channelDialogVisibleTopDateSQL("COALESCE(top_msg.message_date, d.top_message_date, c.date)", "0")
	args := []any{viewerUserID}
	where := []string{"i.user_id = $1", "i.status = 'active'", "NOT i.deleted", "m.status = 'active'"}
	from := `FROM user_channel_member_index i
JOIN channel_members m ON m.user_id = i.user_id AND m.channel_id = i.channel_id
JOIN channels c ON c.id = i.channel_id AND NOT c.deleted
LEFT JOIN channel_messages top_msg ON top_msg.channel_id = i.channel_id AND top_msg.id = c.top_message_id AND NOT top_msg.deleted
LEFT JOIN channel_dialogs d ON d.user_id = m.user_id AND d.channel_id = m.channel_id`
	if (filter.HasFolderID && filter.FolderID == domain.DialogArchiveFolderID) || filter.PinnedOnly {
		from = `FROM channel_dialogs d
JOIN user_channel_member_index i ON i.user_id = d.user_id AND i.channel_id = d.channel_id
JOIN channel_members m ON m.user_id = i.user_id AND m.channel_id = i.channel_id
JOIN channels c ON c.id = d.channel_id AND NOT c.deleted
LEFT JOIN channel_messages top_msg ON top_msg.channel_id = d.channel_id AND top_msg.id = c.top_message_id AND NOT top_msg.deleted`
		where = []string{"d.user_id = $1", "i.status = 'active'", "NOT i.deleted", "m.status = 'active'"}
	}
	if filter.HasFolderID {
		args = append(args, filter.FolderID)
		where = append(where, fmt.Sprintf("COALESCE(d.folder_id, 0) = $%d", len(args)))
	} else {
		where = append(where, "COALESCE(d.folder_id, 0) = 0")
	}
	if filter.PinnedOnly {
		where = append(where, "COALESCE(d.pinned, false)")
	}
	if filter.ExcludePinned {
		where = append(where, "NOT COALESCE(d.pinned, false)")
	}
	args = append(args, channelDialogCandidateLimit)
	rows, err := s.db.Query(ctx, `
WITH dependency_peers AS MATERIALIZED (
    SELECT i.channel_id AS id
    FROM user_channel_member_index i
    WHERE i.user_id = $1
      AND i.status = 'active'
      AND NOT i.deleted
    UNION
    SELECT parent.linked_monoforum_id
    FROM user_channel_member_index i
    JOIN channels parent ON parent.id = i.channel_id
    WHERE i.user_id = $1
      AND i.status = 'active'
      AND NOT i.deleted
      AND parent.linked_monoforum_id <> 0
),
dependency AS MATERIALIZED (
    SELECT COALESCE(bit_xor(v.hash), 0)::bigint AS hash
    FROM read_model_versions v
    JOIN dependency_peers peer ON peer.id = v.peer_id
    WHERE v.peer_type = 'channel'
      AND (
        (v.model = 'channel_base' AND v.owner_user_id = 0)
        OR
        (v.model IN ('channel_member', 'dialog_light') AND v.owner_user_id = $1)
      )
)
SELECT c.id,
       `+visibleTopID+`,
       `+visibleTopDate+`,
       COALESCE(d.folder_id, 0),
       COALESCE(d.pinned, false),
       COALESCE(d.pinned_order, 0),
	   dependency.hash
`+from+`
CROSS JOIN dependency
WHERE `+strings.Join(where, " AND ")+`
ORDER BY COALESCE(d.pinned, false) DESC,
         COALESCE(d.pinned_order, 0) DESC,
         `+visibleTopDate+` DESC,
         `+visibleTopID+` DESC,
         c.id DESC
LIMIT $`+fmt.Sprint(len(args)), args...)
	if err != nil {
		return domain.ChannelDialogList{}, fmt.Errorf("list channel dialog snapshot headers: %w", err)
	}
	defer rows.Close()
	dialogs := make([]domain.Dialog, 0, minInt(len(channelIDs), 1024))
	seenChannels := make(map[int64]struct{}, len(channelIDs))
	var dependencyHash int64
	for rows.Next() {
		var dialog domain.Dialog
		var channelID int64
		if err := rows.Scan(
			&channelID,
			&dialog.TopMessage,
			&dialog.TopMessageDate,
			&dialog.FolderID,
			&dialog.Pinned,
			&dialog.PinnedOrder,
			&dependencyHash,
		); err != nil {
			return domain.ChannelDialogList{}, err
		}
		dialog.Peer = domain.Peer{Type: domain.PeerTypeChannel, ID: channelID}
		dialogs = append(dialogs, dialog)
		seenChannels[channelID] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return domain.ChannelDialogList{}, err
	}
	if hasBroadcastAdmin {
		items, err := s.listMonoforumAdminDialogItems(ctx, viewerUserID, channelIDs, filter, seenChannels)
		if err != nil {
			return domain.ChannelDialogList{}, err
		}
		for _, item := range items {
			dialogs = append(dialogs, item.dialog)
		}
	}
	if len(dialogs) > channelDialogCandidateLimit {
		return domain.ChannelDialogList{}, fmt.Errorf("channel dialog snapshot exceeds %d entries", channelDialogCandidateLimit)
	}
	sort.SliceStable(dialogs, func(i, j int) bool {
		if dialogs[i].Pinned != dialogs[j].Pinned {
			return dialogs[i].Pinned
		}
		if dialogs[i].PinnedOrder != dialogs[j].PinnedOrder {
			return dialogs[i].PinnedOrder > dialogs[j].PinnedOrder
		}
		if dialogs[i].TopMessageDate != dialogs[j].TopMessageDate {
			return dialogs[i].TopMessageDate > dialogs[j].TopMessageDate
		}
		if dialogs[i].TopMessage != dialogs[j].TopMessage {
			return dialogs[i].TopMessage > dialogs[j].TopMessage
		}
		return dialogs[i].Peer.ID > dialogs[j].Peer.ID
	})
	return domain.ChannelDialogList{
		Dialogs: dialogs,
		Count:   len(dialogs),
		Hash:    mixDialogListDependencyHash(dialogListHash(dialogs), dependencyHash),
	}, nil
}

// ListAllBuiltinChannelDialogSnapshot loads every owner-varying dialog fact
// needed to derive main/archive/pinned pages in one bounded scan. Shared
// channel rows and top-message payloads remain channel-keyed response overlays.
func (s *ChannelStore) ListAllBuiltinChannelDialogSnapshot(ctx context.Context, viewerUserID int64) (domain.ChannelDialogList, error) {
	if viewerUserID == 0 {
		return domain.ChannelDialogList{}, nil
	}
	visibleTopID := channelDialogVisibleTopIDSQL()
	visibleTopDate := channelDialogVisibleTopDateSQL("COALESCE(top_msg.message_date, d.top_message_date, c.date)", "0")
	visibleReadInbox := "GREATEST(COALESCE(d.read_inbox_max_id, 0), m.read_inbox_max_id)"
	visibleUnreadCount := channelDialogVisibleUnreadCountSQL(visibleReadInbox, visibleTopID)
	rows, err := s.db.Query(ctx, `
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
       d.default_send_as_peer_id,
       m.history_clear_anchor_id,
		m.history_clear_anchor_date,
		top_unread.message_id IS NOT NULL,
		COALESCE(top_unread.unread, false),
		m.user_id,
		m.inviter_user_id,
		m.role,
		m.status,
		m.joined_at,
		m.left_at,
		m.admin_rights::text,
		m.banned_rights::text,
		m.rank,
		m.available_min_id,
		m.available_min_pts,
		m.read_inbox_max_id,
		m.read_outbox_max_id,
		m.unread_mark,
		m.slowmode_last_send_date,
		bool_or(i.broadcast AND i.role IN ('creator', 'admin')) OVER ()
FROM user_channel_member_index AS i
JOIN channel_members AS m
  ON m.user_id = i.user_id
 AND m.channel_id = i.channel_id
 AND m.status = 'active'
JOIN channels AS c
  ON c.id = i.channel_id
 AND NOT c.deleted
LEFT JOIN channel_messages AS top_msg
  ON top_msg.channel_id = i.channel_id
 AND top_msg.id = c.top_message_id
 AND NOT top_msg.deleted
LEFT JOIN channel_dialogs AS d
  ON d.user_id = i.user_id
 AND d.channel_id = i.channel_id
LEFT JOIN channel_unread_mentions AS top_unread
  ON top_unread.user_id = i.user_id
 AND top_unread.channel_id = i.channel_id
 AND top_unread.message_id = `+visibleTopID+`
WHERE i.user_id = $1
  AND i.status = 'active'
  AND NOT i.deleted
  AND COALESCE(d.folder_id, 0) IN (0, 1)
ORDER BY COALESCE(d.pinned, false) DESC,
         COALESCE(d.pinned_order, 0) DESC,
         `+visibleTopDate+` DESC,
         `+visibleTopID+` DESC,
         c.id DESC
LIMIT $2`, viewerUserID, channelDialogCandidateLimit+1)
	if err != nil {
		return domain.ChannelDialogList{}, fmt.Errorf("list all built-in channel dialog snapshot: %w", err)
	}
	defer rows.Close()
	dialogs := make([]domain.Dialog, 0, 128)
	parentChannelIDs := make([]int64, 0, 128)
	seenChannels := make(map[int64]struct{}, 128)
	hasBroadcastAdmin := false
	for rows.Next() {
		var values channelDialogProjectionValues
		var rowHasBroadcastAdmin bool
		destinations := append(values.scanDestinations(), &rowHasBroadcastAdmin)
		if err := rows.Scan(destinations...); err != nil {
			return domain.ChannelDialogList{}, fmt.Errorf("scan all built-in channel dialog snapshot: %w", err)
		}
		channelID, dialog, defaultSendAs, _, _ := values.result()
		dialog.DefaultSendAs = defaultSendAs
		dialogs = append(dialogs, dialog)
		parentChannelIDs = append(parentChannelIDs, channelID)
		seenChannels[channelID] = struct{}{}
		hasBroadcastAdmin = hasBroadcastAdmin || rowHasBroadcastAdmin
	}
	if err := rows.Err(); err != nil {
		return domain.ChannelDialogList{}, fmt.Errorf("list all built-in channel dialog snapshot rows: %w", err)
	}
	if len(dialogs) > channelDialogCandidateLimit {
		return domain.ChannelDialogList{}, fmt.Errorf("channel dialog snapshot exceeds %d entries", channelDialogCandidateLimit)
	}
	if hasBroadcastAdmin {
		items, err := s.listMonoforumAdminDialogItems(
			ctx, viewerUserID, parentChannelIDs, domain.DialogFilter{}, seenChannels,
		)
		if err != nil {
			return domain.ChannelDialogList{}, err
		}
		for _, item := range items {
			if item.dialog.FolderID == domain.DialogMainFolderID || item.dialog.FolderID == domain.DialogArchiveFolderID {
				item.dialog.DefaultSendAs = item.defaultSendAs
				dialogs = append(dialogs, item.dialog)
			}
		}
	}
	if len(dialogs) > channelDialogCandidateLimit {
		return domain.ChannelDialogList{}, fmt.Errorf("channel dialog snapshot exceeds %d entries", channelDialogCandidateLimit)
	}
	sort.SliceStable(dialogs, func(i, j int) bool {
		if dialogs[i].Pinned != dialogs[j].Pinned {
			return dialogs[i].Pinned
		}
		if dialogs[i].PinnedOrder != dialogs[j].PinnedOrder {
			return dialogs[i].PinnedOrder > dialogs[j].PinnedOrder
		}
		if dialogs[i].TopMessageDate != dialogs[j].TopMessageDate {
			return dialogs[i].TopMessageDate > dialogs[j].TopMessageDate
		}
		if dialogs[i].TopMessage != dialogs[j].TopMessage {
			return dialogs[i].TopMessage > dialogs[j].TopMessage
		}
		return dialogs[i].Peer.ID > dialogs[j].Peer.ID
	})
	return domain.ChannelDialogList{Dialogs: dialogs, Count: len(dialogs)}, nil
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
       d.default_send_as_peer_id,
       0,
       0
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
  AND (pm.role = 'creator' OR (pm.role = 'admin' AND COALESCE((pm.admin_rights->>'ManageDirectMessages')::boolean, false)))
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
	keys := make([]channelMessageLookupKey, 0, len(dialogs))
	for _, dialog := range dialogs {
		if dialog.Peer.Type != domain.PeerTypeChannel || dialog.Peer.ID == 0 || dialog.TopMessage <= 0 {
			continue
		}
		key := channelMessageLookupKey{channelID: dialog.Peer.ID, id: dialog.TopMessage}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		keys = append(keys, key)
	}
	if len(keys) == 0 {
		return nil, nil
	}
	load := func(ctx context.Context, missing []channelMessageLookupKey) (map[channelMessageLookupKey]domain.ChannelMessage, error) {
		return loadChannelDialogTopMessages(ctx, db, missing)
	}
	var (
		out map[channelMessageLookupKey]domain.ChannelMessage
		err error
	)
	if s.topMessageCacheActive(db) {
		out, err = s.topMsgCache.getOrLoadBatch(ctx, keys, load)
	} else {
		out, err = load(ctx, keys)
	}
	if err != nil {
		return nil, err
	}
	// History-clear anchors are owner-local projections and must never enter
	// the shared cache. Apply them to the cloned result after cache hydration.
	for _, dialog := range dialogs {
		if dialog.Peer.Type != domain.PeerTypeChannel ||
			dialog.TopMessage <= 0 ||
			dialog.TopMessage != dialog.HistoryClearAnchorID {
			continue
		}
		key := channelMessageLookupKey{channelID: dialog.Peer.ID, id: dialog.TopMessage}
		out[key] = domain.ProjectChannelHistoryClearMessage(
			out[key],
			dialog.Peer.ID,
			dialog.HistoryClearAnchorID,
			dialog.HistoryClearAnchorDate,
		)
	}
	return out, nil
}

func loadChannelDialogTopMessages(ctx context.Context, db sqlcgen.DBTX, keys []channelMessageLookupKey) (map[channelMessageLookupKey]domain.ChannelMessage, error) {
	idsByChannel := make(map[int64][]int, len(keys))
	for _, key := range keys {
		if key.channelID == 0 || key.id <= 0 {
			continue
		}
		idsByChannel[key.channelID] = append(idsByChannel[key.channelID], key.id)
	}
	if len(idsByChannel) == 0 {
		return map[channelMessageLookupKey]domain.ChannelMessage{}, nil
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
	out := make(map[channelMessageLookupKey]domain.ChannelMessage, len(keys))
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
	if viewerUserID == 0 || len(channelIDs) == 0 {
		return domain.ChannelDialogList{}, nil
	}
	orderedIDs := make([]int64, 0, len(channelIDs))
	seen := make(map[int64]struct{}, len(channelIDs))
	for _, channelID := range channelIDs {
		if channelID == 0 {
			continue
		}
		if _, ok := seen[channelID]; ok {
			continue
		}
		seen[channelID] = struct{}{}
		orderedIDs = append(orderedIDs, channelID)
	}
	if len(orderedIDs) == 0 {
		return domain.ChannelDialogList{}, nil
	}
	itemsByID := make(map[int64]channelDialogListItem, len(orderedIDs))
	loadedIDs := make([]int64, 0, len(orderedIDs))
	misses := make([]int64, 0, len(orderedIDs))
	dialogCacheActive := s.dialogCacheActive(s.db)
	memberCacheActive := s.memberCacheActive(s.db)
	var dialogCacheEpoch uint64
	var memberCacheEpoch uint64
	if dialogCacheActive {
		dialogCacheEpoch = s.dialogCache.cacheEpoch()
	}
	if memberCacheActive {
		memberCacheEpoch = s.memberCache.cacheEpoch()
	}
	for _, channelID := range orderedIDs {
		if dialogCacheActive {
			if cached, ok := s.dialogCache.getListProjection(viewerUserID, channelID); ok {
				itemsByID[channelID] = channelDialogListItem{
					dialog:             channelDialogToDialog(cached.dialog, 0),
					defaultSendAs:      cached.dialog.DefaultSendAs,
					topMentioned:       cached.topMentioned,
					topMediaUnread:     cached.topMediaUnread,
					topUnreadProjected: cached.topUnreadProjected,
					listCacheable:      true,
				}
				loadedIDs = append(loadedIDs, channelID)
				continue
			}
		}
		misses = append(misses, channelID)
	}
	if len(misses) > 0 {
		loaded, err := s.loadChannelDialogListItems(ctx, viewerUserID, misses)
		if err != nil {
			return domain.ChannelDialogList{}, err
		}
		for _, channelID := range misses {
			if item, ok := loaded[channelID]; ok {
				itemsByID[channelID] = item
				loadedIDs = append(loadedIDs, channelID)
			}
		}
	}
	channelsByID, err := s.channelsByIDs(ctx, s.db, loadedIDs)
	if err != nil {
		return domain.ChannelDialogList{}, err
	}
	for _, channelID := range loadedIDs {
		item := itemsByID[channelID]
		channel := channelsByID[channelID]
		if channel.ID == 0 {
			// The shared row was deleted after the owner-state snapshot. Treat
			// it as absent instead of returning a half-hydrated dialog.
			delete(itemsByID, channelID)
			continue
		}
		item.channel = channel
		item.dialog.Pts = channel.Pts
		itemsByID[channelID] = item
	}

	out := domain.ChannelDialogList{}
	// A requested monoforum preview can be absent from channel_members. Keep the
	// rare admission path exact, but only pay its per-peer checks for IDs missed
	// by the normal batch query.
	for _, channelID := range orderedIDs {
		if _, ok := itemsByID[channelID]; ok {
			continue
		}
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
				// Send the parent broadcast channel in the same batch as the mono. TDesktop's applyMonoforumLinkedId
				// only calls setMonoforumLink to derive MonoforumAdmin (needed for Direct-Messages container rendering) when the parent object is already loaded; a missing parent
				// degrades the mono into an ordinary megagroup ("You created a group"). tgChannelsForDialogs dedups
				// by id, so sending it twice is harmless; this branch is already gated on isChannelAdmin(parentMember) and does not leak to non-admins.
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
		itemsByID[channelID] = channelDialogListItem{
			channel:       channel,
			dialog:        channelDialogToDialog(dialog, channel.Pts),
			defaultSendAs: dialog.DefaultSendAs,
			listCacheable: !synthetic,
		}
	}

	dialogs := make([]domain.Dialog, 0, len(itemsByID))
	for _, channelID := range orderedIDs {
		if item, ok := itemsByID[channelID]; ok {
			item.dialog.TopMessageMentioned = item.topMentioned
			item.dialog.TopMessageMediaUnread = item.topMediaUnread
			item.dialog.TopMessageUnreadProjected = item.topUnreadProjected
			itemsByID[channelID] = item
			dialogs = append(dialogs, item.dialog)
		}
	}
	topMessages, err := s.channelDialogTopMessages(ctx, s.db, dialogs)
	if err != nil {
		return domain.ChannelDialogList{}, err
	}
	allTopUnreadProjected := true
	for _, channelID := range orderedIDs {
		item, ok := itemsByID[channelID]
		if !ok {
			continue
		}
		msg := topMessages[channelMessageLookupKey{channelID: channelID, id: item.dialog.TopMessage}]
		if msg.ID != 0 {
			if item.topUnreadProjected {
				msg.Mentioned = item.topMentioned
				msg.MediaUnread = item.topMediaUnread
			} else {
				allTopUnreadProjected = false
			}
			item.dialog.TopMessageDate = msg.Date
			out.Messages = append(out.Messages, msg)
		}
		if dialogCacheActive && item.listCacheable {
			cached := channelDialogFromDialog(viewerUserID, item.dialog)
			cached.DefaultSendAs = item.defaultSendAs
			s.dialogCache.putListProjectionIfEpoch(
				cached,
				item.topMentioned,
				item.topMediaUnread,
				item.topUnreadProjected,
				dialogCacheEpoch,
			)
		}
		if memberCacheActive && item.dialog.ChannelMember != nil {
			s.memberCache.putIfEpoch(*item.dialog.ChannelMember, memberCacheEpoch)
		}
		out.Dialogs = append(out.Dialogs, item.dialog)
		out.Channels = append(out.Channels, item.channel)
	}
	out.Count = len(out.Dialogs)
	// Same reason as ListChannelDialogs: augment the top message per viewer with unread flags and reactions.
	if err := s.populateChannelDialogTopMessageReactions(ctx, s.db, viewerUserID, out.Channels, out.Messages, allTopUnreadProjected); err != nil {
		return domain.ChannelDialogList{}, err
	}
	projectChannelDialogHistoryClearMessages(out.Dialogs, out.Messages)
	return out, nil
}

// HydrateChannelDialogSnapshot attaches viewer-independent channel rows/top
// messages and the small viewer reaction overlay to already materialized
// owner dialog facts. It deliberately does not read channel_members or
// channel_dialogs: dialog_owner + channel_base generations protecting the
// caller's snapshot are the authority for those fields.
func (s *ChannelStore) HydrateChannelDialogSnapshot(
	ctx context.Context,
	viewerUserID int64,
	dialogs []domain.Dialog,
) (domain.ChannelDialogList, error) {
	if viewerUserID == 0 || len(dialogs) == 0 {
		return domain.ChannelDialogList{}, nil
	}
	ordered := make([]domain.Dialog, 0, len(dialogs))
	ids := make([]int64, 0, len(dialogs))
	seen := make(map[int64]struct{}, len(dialogs))
	for _, dialog := range dialogs {
		if dialog.Peer.Type != domain.PeerTypeChannel || dialog.Peer.ID == 0 {
			continue
		}
		if _, duplicate := seen[dialog.Peer.ID]; duplicate {
			continue
		}
		seen[dialog.Peer.ID] = struct{}{}
		ordered = append(ordered, dialog)
		ids = append(ids, dialog.Peer.ID)
	}
	if len(ordered) == 0 {
		return domain.ChannelDialogList{}, nil
	}
	dialogCacheActive := s.dialogCacheActive(s.db)
	memberCacheActive := s.memberCacheActive(s.db)
	var dialogCacheEpoch uint64
	var memberCacheEpoch uint64
	if dialogCacheActive {
		// Snapshot the epoch before any shared hydration. A concurrent
		// dialog_light/channel_member/channel_base invalidation must prevent
		// the owner projection from being written back after it became stale.
		dialogCacheEpoch = s.dialogCache.cacheEpoch()
	}
	if memberCacheActive {
		memberCacheEpoch = s.memberCache.cacheEpoch()
	}
	channelsByID, err := s.channelsByIDs(ctx, s.db, ids)
	if err != nil {
		return domain.ChannelDialogList{}, err
	}
	for index := range ordered {
		channel := channelsByID[ordered[index].Peer.ID]
		if channel.ID == 0 {
			return domain.ChannelDialogList{}, fmt.Errorf("hydrate channel dialog snapshot %d: %w", ordered[index].Peer.ID, domain.ErrChannelInvalid)
		}
		ordered[index].Pts = channel.Pts
	}
	topMessages, err := s.channelDialogTopMessages(ctx, s.db, ordered)
	if err != nil {
		return domain.ChannelDialogList{}, err
	}
	out := domain.ChannelDialogList{Dialogs: make([]domain.Dialog, 0, len(ordered))}
	allTopUnreadProjected := true
	for _, dialog := range ordered {
		channel := channelsByID[dialog.Peer.ID]
		message := topMessages[channelMessageLookupKey{channelID: dialog.Peer.ID, id: dialog.TopMessage}]
		if message.ID != 0 {
			if dialog.TopMessageUnreadProjected {
				message.Mentioned = dialog.TopMessageMentioned
				message.MediaUnread = dialog.TopMessageMediaUnread
			} else {
				allTopUnreadProjected = false
			}
			dialog.TopMessageDate = message.Date
			out.Messages = append(out.Messages, message)
		}
		if dialogCacheActive {
			cached := channelDialogFromDialog(viewerUserID, dialog)
			cached.DefaultSendAs = clonePeer(dialog.DefaultSendAs)
			s.dialogCache.putListProjectionIfEpoch(
				cached,
				dialog.TopMessageMentioned,
				dialog.TopMessageMediaUnread,
				dialog.TopMessageUnreadProjected,
				dialogCacheEpoch,
			)
		}
		if memberCacheActive && dialog.ChannelMember != nil {
			s.memberCache.putIfEpoch(*dialog.ChannelMember, memberCacheEpoch)
		}
		out.Dialogs = append(out.Dialogs, dialog)
		out.Channels = append(out.Channels, channel)
	}
	out.Count = len(out.Dialogs)
	if err := s.populateChannelDialogTopMessageReactions(
		ctx, s.db, viewerUserID, out.Channels, out.Messages, allTopUnreadProjected,
	); err != nil {
		return domain.ChannelDialogList{}, err
	}
	projectChannelDialogHistoryClearMessages(out.Dialogs, out.Messages)
	return out, nil
}

func (s *ChannelStore) loadChannelDialogListItems(ctx context.Context, viewerUserID int64, channelIDs []int64) (map[int64]channelDialogListItem, error) {
	visibleTopID := channelDialogVisibleTopIDSQL()
	visibleTopDate := channelDialogVisibleTopDateSQL("COALESCE(top_msg.message_date, d.top_message_date, c.date)", "0")
	visibleReadInbox := "GREATEST(COALESCE(d.read_inbox_max_id, 0), m.read_inbox_max_id)"
	visibleUnreadCount := channelDialogVisibleUnreadCountSQL(visibleReadInbox, visibleTopID)
	rows, err := s.db.Query(ctx, `
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
       d.default_send_as_peer_id,
       m.history_clear_anchor_id,
       m.history_clear_anchor_date,
		top_unread.message_id IS NOT NULL,
		COALESCE(top_unread.unread, false),
		m.user_id,
		m.inviter_user_id,
		m.role,
		m.status,
		m.joined_at,
		m.left_at,
		m.admin_rights::text,
		m.banned_rights::text,
		m.rank,
		m.available_min_id,
		m.available_min_pts,
		m.read_inbox_max_id,
		m.read_outbox_max_id,
		m.unread_mark,
		m.slowmode_last_send_date
FROM channel_members m
JOIN channels c ON c.id = m.channel_id AND NOT c.deleted
LEFT JOIN channel_messages top_msg ON top_msg.channel_id = m.channel_id AND top_msg.id = c.top_message_id AND NOT top_msg.deleted
LEFT JOIN channel_dialogs d ON d.user_id = m.user_id AND d.channel_id = m.channel_id
LEFT JOIN channel_unread_mentions top_unread
       ON top_unread.user_id = m.user_id
      AND top_unread.channel_id = m.channel_id
      AND top_unread.message_id = `+visibleTopID+`
WHERE m.user_id = $1
  AND m.channel_id = ANY($2::bigint[])
  AND m.status = 'active'`, viewerUserID, channelIDs)
	if err != nil {
		return nil, fmt.Errorf("batch get channel dialogs: %w", err)
	}
	defer rows.Close()
	itemsByID := make(map[int64]channelDialogListItem, len(channelIDs))
	for rows.Next() {
		channelID, dialog, defaultSendAs, topMentioned, topMediaUnread, err := scanChannelDialogProjectionRow(rows)
		if err != nil {
			return nil, err
		}
		itemsByID[channelID] = channelDialogListItem{
			dialog:             dialog,
			defaultSendAs:      defaultSendAs,
			topMentioned:       topMentioned,
			topMediaUnread:     topMediaUnread,
			topUnreadProjected: true,
			listCacheable:      true,
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return itemsByID, nil
}

func projectChannelDialogHistoryClearMessages(dialogs []domain.Dialog, messages []domain.ChannelMessage) {
	anchors := make(map[channelMessageLookupKey]domain.Dialog)
	for _, dialog := range dialogs {
		if dialog.Peer.Type != domain.PeerTypeChannel ||
			dialog.TopMessage <= 0 ||
			dialog.TopMessage != dialog.HistoryClearAnchorID {
			continue
		}
		anchors[channelMessageLookupKey{channelID: dialog.Peer.ID, id: dialog.TopMessage}] = dialog
	}
	for i := range messages {
		dialog, ok := anchors[channelMessageLookupKey{channelID: messages[i].ChannelID, id: messages[i].ID}]
		if !ok {
			continue
		}
		messages[i] = domain.ProjectChannelHistoryClearMessage(
			messages[i],
			dialog.Peer.ID,
			dialog.HistoryClearAnchorID,
			dialog.HistoryClearAnchorDate,
		)
	}
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
       history_clear_anchor_id, history_clear_anchor_date,
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
	visibleTopID := channelDialogVisibleTopIDSQL()
	visibleTopDate := channelDialogVisibleTopDateSQL("COALESCE(top_msg.message_date, d.top_message_date, c.date)", "GREATEST(c.date, m.joined_at)")
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
       d.default_send_as_peer_id,
       m.history_clear_anchor_id,
       m.history_clear_anchor_date
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

// listActiveChannelDialogCandidateIDs returns candidate channel ids, along with hasBroadcastAdmin: whether the user is
// creator/admin in any broadcast channel. The latter is the page-independent superset test for the existence of a monoforum direct-messages dialog, letting the getDialogs hot path
// skip the extra monoforum admin dialog query (see ListChannelDialogs). It reads only the existing broadcast/role columns of the
// user_channel_member_index table, with zero extra round trips.
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
    -- A lazily created row must carry the member's real watermark and unread count: once a zero-value cached row exists it would
    -- mask the true value (small groups read the cached column).
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
-- The pin order spans the dialogs/channel_dialogs tables but is allocated only within the dialog's current folder,
-- sharing the same (user, folder) order space as private-chat SetDialogPinned.
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
	// Archiving must ensure-INSERT: a channel never read/pinned has no dialog row yet, so
	// UPDATE alone would silently lose the archive; the new row also carries the member's real watermark,
	// avoiding a zero-watermark cached row masking unread/read state.
	visibleTopID := channelDialogVisibleTopIDSQL()
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
       `+visibleTopID+`,
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
-- Clear pinned when changing folder: consistent with private-chat EditDialogPeerFolders, TDesktop
-- unconditionally unpins locally on archive/restore, so keeping the old pin on the server would cause state drift.
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
	// JOIN active member: leftover channel_dialogs rows from members who left are not counted in the archive badge.
	// unread is derived dynamically at read time (H4a): no longer consuming the channel_dialogs.unread_count cached column; the archive set is
	// bounded (an explicit archive creates the row), and the per-row dynamic COUNT is already clamped by the cap.
	visibleTopID := channelDialogVisibleTopIDSQL()
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
	visibleTopID := channelDialogVisibleTopIDSQL()
	visibleReadInbox := "GREATEST(COALESCE(d.read_inbox_max_id, 0), m.read_inbox_max_id)"
	visibleUnreadCount := channelDialogVisibleUnreadCountSQL(visibleReadInbox, visibleTopID)
	// Single-channel TopMessageDate is taken directly via LEFT JOIN top_msg, replacing the previous extra
	// round trip of a separate getChannelMessage per channel (chf-3, one of the measured pg_stat "channel message by id" sources). Strictly
	// preserving the original semantics: take message_date only when the top is visible (top_message_id > available_min_id) and the message row exists
	// (not filtering deleted, same as the original getChannelMessage), otherwise fall back to
	// COALESCE(d.top_message_date, c.date). Note: this deliberately keeps the existing difference from the batch getChannelDialogs hidden state
	// (ELSE 0); this change only removes the round trip and does not alter single-channel output.
	visibleTopDate := channelDialogVisibleTopDateSQL(
		"CASE WHEN top_msg.id IS NOT NULL THEN top_msg.message_date ELSE COALESCE(d.top_message_date, c.date) END",
		"COALESCE(d.top_message_date, c.date)",
	)
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
       d.default_send_as_peer_id,
       m.history_clear_anchor_id,
       m.history_clear_anchor_date
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
		&dialog.HistoryClearAnchorID,
		&dialog.HistoryClearAnchorDate,
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
		// Snapshot the epoch before the warm write-back: reject the stale projection write-back if an invalidation arrived during the miss-load (same as ListChannelDialogs).
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
	visibleTopID := channelDialogVisibleTopIDSQL()
	visibleTopDate := channelDialogVisibleTopDateSQL("COALESCE(top_msg.message_date, d.top_message_date, c.date)", "0")
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
       d.default_send_as_peer_id,
       m.history_clear_anchor_id,
       m.history_clear_anchor_date
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
			&dialog.HistoryClearAnchorID,
			&dialog.HistoryClearAnchorDate,
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

// cappedChannelUnreadCount generates an unread COUNT subquery that clamps the scanned row count to MaxDialogUnreadCount,
// consistent with the read path's channelDialogDynamicUnreadCountSQL. The write fan-out path previously used a bare COUNT without
// LIMIT, so a member who has not read for a long time (read far below top) degraded to an O(unread backlog) full scan; the inner LIMIT subquery caps
// the per-member scan work, so the written cached column and the read path's badge are both min(actual, MaxDialogUnreadCount).
// whereBody is the full WHERE clause on the channel_messages alias msg.
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

// upsertChannelDialogsForMessageTx handles the recipient-side read-boundary side effects of a message within the send transaction.
//
// Performance audit H4a: no longer upsert channel_dialogs for all members (the old gate was megagroup ≤ MaxSynchronousChannelDialogFanout)—
// the O(members) row writes + row locks in the send transaction were the blocker to opening up large groups (P1-u remnant).
// All channels (broadcast / megagroup regardless of size) uniformly derive at read time: unread is recomputed from visible incoming messages
// (channelDialogDynamicUnreadCountSQL, capped), top is provided by channels.top_message_id,
// the read-boundary truth lives in channel_members, and the dialog row carries only pin/folder/unread_mark/draft and other
// explicit user state, creating rows lazily as needed.
//
// The only per-recipient write kept: advancing the read_inbox boundary of skipDelivery (privacy bot) members—these
// members are excluded from delivery and their boundary must be raised in sync to keep their unread derivation from counting hidden messages as unread; the set is bounded
// (bot-only candidates).
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
	// A skipDelivery member may already have a channel_dialogs row (lazily created by pin/archive etc.), whose read_inbox
	// is read via GREATEST(d, m)—advancing the member row is enough, the dialog row needs no sync.
	return nil
}

func scanChannelDialogRow(row rowScanner, userID int64) (domain.Channel, domain.Dialog, *domain.Peer, error) {
	var ch domain.Channel
	var rights, reactionPolicy string
	var wallpaper *string
	var topID, topDate, folderID, readInbox, readOutbox, unreadCount, pinnedOrder, unreadMentions, unreadReactions int
	var historyClearAnchorID, historyClearAnchorDate int
	var pinned, unreadMark, viewForumAsMessages, hasScheduled bool
	var defaultSendAsType sql.NullString
	var defaultSendAsID sql.NullInt64
	dest := append(channelScanDest(&ch, &rights, &reactionPolicy, &wallpaper),
		&topID, &topDate,
		&folderID, &readInbox, &readOutbox, &unreadCount, &pinned, &pinnedOrder, &unreadMark, &unreadMentions, &unreadReactions, &viewForumAsMessages, &hasScheduled,
		&defaultSendAsType, &defaultSendAsID,
		&historyClearAnchorID, &historyClearAnchorDate,
	)
	if err := row.Scan(dest...); err != nil {
		return domain.Channel{}, domain.Dialog{}, nil, err
	}
	finishChannelScan(&ch, rights, reactionPolicy, wallpaper)
	dialog := domain.Dialog{
		Peer:                   domain.Peer{Type: domain.PeerTypeChannel, ID: ch.ID},
		FolderID:               folderID,
		TopMessage:             topID,
		TopMessageDate:         topDate,
		HistoryClearAnchorID:   historyClearAnchorID,
		HistoryClearAnchorDate: historyClearAnchorDate,
		ReadInboxMaxID:         readInbox,
		ReadOutboxMaxID:        readOutbox,
		UnreadCount:            unreadCount,
		UnreadMentions:         unreadMentions,
		UnreadReactions:        unreadReactions,
		Pinned:                 pinned,
		PinnedOrder:            pinnedOrder,
		UnreadMark:             unreadMark,
		ViewForumAsMessages:    viewForumAsMessages,
		HasScheduled:           hasScheduled,
		Pts:                    ch.Pts,
	}
	var defaultSendAs *domain.Peer
	if defaultSendAsType.Valid && defaultSendAsID.Valid && defaultSendAsID.Int64 != 0 {
		defaultSendAs = &domain.Peer{Type: domain.PeerType(defaultSendAsType.String), ID: defaultSendAsID.Int64}
	}
	_ = userID
	return ch, dialog, defaultSendAs, nil
}

type channelDialogProjectionValues struct {
	channelID                                                 int64
	topID, topDate, folderID, readInbox, readOutbox           int
	unreadCount, pinnedOrder, unreadMentions, unreadReactions int
	historyClearAnchorID, historyClearAnchorDate              int
	pinned, unreadMark, viewForumAsMessages, hasScheduled     bool
	defaultSendAsType                                         sql.NullString
	defaultSendAsID                                           sql.NullInt64
	topMentioned, topMediaUnread                              bool
	memberUserID, memberInviterUserID                         int64
	memberRole, memberStatus                                  string
	memberJoinedAt, memberLeftAt                              int
	memberAdminRights, memberBannedRights, memberRank         string
	memberAvailableMinID, memberAvailableMinPts               int
	memberReadInboxMaxID, memberReadOutboxMaxID               int
	memberUnreadMark                                          bool
	memberSlowmodeLastSendDate                                int
}

func (v *channelDialogProjectionValues) scanDestinations() []any {
	return []any{
		&v.channelID,
		&v.topID, &v.topDate,
		&v.folderID, &v.readInbox, &v.readOutbox, &v.unreadCount, &v.pinned, &v.pinnedOrder, &v.unreadMark, &v.unreadMentions, &v.unreadReactions, &v.viewForumAsMessages, &v.hasScheduled,
		&v.defaultSendAsType, &v.defaultSendAsID,
		&v.historyClearAnchorID, &v.historyClearAnchorDate,
		&v.topMentioned, &v.topMediaUnread,
		&v.memberUserID, &v.memberInviterUserID,
		&v.memberRole, &v.memberStatus,
		&v.memberJoinedAt, &v.memberLeftAt,
		&v.memberAdminRights, &v.memberBannedRights, &v.memberRank,
		&v.memberAvailableMinID, &v.memberAvailableMinPts,
		&v.memberReadInboxMaxID, &v.memberReadOutboxMaxID,
		&v.memberUnreadMark, &v.memberSlowmodeLastSendDate,
	}
}

func (v *channelDialogProjectionValues) result() (int64, domain.Dialog, *domain.Peer, bool, bool) {
	dialog := domain.Dialog{
		Peer:                      domain.Peer{Type: domain.PeerTypeChannel, ID: v.channelID},
		FolderID:                  v.folderID,
		TopMessage:                v.topID,
		TopMessageDate:            v.topDate,
		HistoryClearAnchorID:      v.historyClearAnchorID,
		HistoryClearAnchorDate:    v.historyClearAnchorDate,
		ReadInboxMaxID:            v.readInbox,
		ReadOutboxMaxID:           v.readOutbox,
		UnreadCount:               v.unreadCount,
		UnreadMentions:            v.unreadMentions,
		UnreadReactions:           v.unreadReactions,
		Pinned:                    v.pinned,
		PinnedOrder:               v.pinnedOrder,
		UnreadMark:                v.unreadMark,
		ViewForumAsMessages:       v.viewForumAsMessages,
		HasScheduled:              v.hasScheduled,
		TopMessageMentioned:       v.topMentioned,
		TopMessageMediaUnread:     v.topMediaUnread,
		TopMessageUnreadProjected: true,
	}
	var defaultSendAs *domain.Peer
	if v.defaultSendAsType.Valid && v.defaultSendAsID.Valid && v.defaultSendAsID.Int64 != 0 {
		defaultSendAs = &domain.Peer{Type: domain.PeerType(v.defaultSendAsType.String), ID: v.defaultSendAsID.Int64}
	}
	member := domain.ChannelMember{
		ChannelID:              v.channelID,
		UserID:                 v.memberUserID,
		InviterUserID:          v.memberInviterUserID,
		Role:                   domain.ChannelMemberRole(v.memberRole),
		Status:                 domain.ChannelMemberStatus(v.memberStatus),
		JoinedAt:               v.memberJoinedAt,
		LeftAt:                 v.memberLeftAt,
		Rank:                   v.memberRank,
		AvailableMinID:         v.memberAvailableMinID,
		AvailableMinPts:        v.memberAvailableMinPts,
		HistoryClearAnchorID:   v.historyClearAnchorID,
		HistoryClearAnchorDate: v.historyClearAnchorDate,
		ReadInboxMaxID:         v.memberReadInboxMaxID,
		ReadOutboxMaxID:        v.memberReadOutboxMaxID,
		UnreadMark:             v.memberUnreadMark,
		SlowmodeLastSendDate:   v.memberSlowmodeLastSendDate,
	}
	_ = json.Unmarshal([]byte(v.memberAdminRights), &member.AdminRights)
	_ = json.Unmarshal([]byte(v.memberBannedRights), &member.BannedRights)
	dialog.ChannelMember = &member
	return v.channelID, dialog, defaultSendAs, v.topMentioned, v.topMediaUnread
}

// scanChannelDialogProjectionRow scans only owner-varying channel dialog
// state. Shared channel metadata is hydrated separately through ChannelRowCache
// so a page containing the same channel for many online owners does not decode
// and transfer the wide channels row repeatedly.
func scanChannelDialogProjectionRow(row rowScanner) (int64, domain.Dialog, *domain.Peer, bool, bool, error) {
	var values channelDialogProjectionValues
	if err := row.Scan(values.scanDestinations()...); err != nil {
		return 0, domain.Dialog{}, nil, false, false, err
	}
	channelID, dialog, defaultSendAs, topMentioned, topMediaUnread := values.result()
	return channelID, dialog, defaultSendAs, topMentioned, topMediaUnread, nil
}

func channelDialogToDialog(dialog domain.ChannelDialog, channelPts int) domain.Dialog {
	return domain.Dialog{
		Peer:                   domain.Peer{Type: domain.PeerTypeChannel, ID: dialog.ChannelID},
		FolderID:               dialog.FolderID,
		TopMessage:             dialog.TopMessageID,
		TopMessageDate:         dialog.TopMessageDate,
		HistoryClearAnchorID:   dialog.HistoryClearAnchorID,
		HistoryClearAnchorDate: dialog.HistoryClearAnchorDate,
		ReadInboxMaxID:         dialog.ReadInboxMaxID,
		ReadOutboxMaxID:        dialog.ReadOutboxMaxID,
		UnreadCount:            dialog.UnreadCount,
		UnreadMentions:         dialog.UnreadMentions,
		UnreadReactions:        dialog.UnreadReactions,
		Pinned:                 dialog.Pinned,
		PinnedOrder:            dialog.PinnedOrder,
		UnreadMark:             dialog.UnreadMark,
		ViewForumAsMessages:    dialog.ViewForumAsMessages,
		HasScheduled:           dialog.HasScheduled,
		DefaultSendAs:          clonePeer(dialog.DefaultSendAs),
		Pts:                    channelPts,
	}
}

func channelDialogFromDialog(userID int64, dialog domain.Dialog) domain.ChannelDialog {
	return domain.ChannelDialog{
		UserID:                 userID,
		ChannelID:              dialog.Peer.ID,
		FolderID:               dialog.FolderID,
		TopMessageID:           dialog.TopMessage,
		TopMessageDate:         dialog.TopMessageDate,
		HistoryClearAnchorID:   dialog.HistoryClearAnchorID,
		HistoryClearAnchorDate: dialog.HistoryClearAnchorDate,
		ReadInboxMaxID:         dialog.ReadInboxMaxID,
		ReadOutboxMaxID:        dialog.ReadOutboxMaxID,
		UnreadCount:            dialog.UnreadCount,
		UnreadMentions:         dialog.UnreadMentions,
		UnreadReactions:        dialog.UnreadReactions,
		Pinned:                 dialog.Pinned,
		PinnedOrder:            dialog.PinnedOrder,
		UnreadMark:             dialog.UnreadMark,
		ViewForumAsMessages:    dialog.ViewForumAsMessages,
		HasScheduled:           dialog.HasScheduled,
		DefaultSendAs:          clonePeer(dialog.DefaultSendAs),
	}
}

func clonePeer(peer *domain.Peer) *domain.Peer {
	if peer == nil {
		return nil
	}
	cloned := *peer
	return &cloned
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
	// Groups/channels can only enter a custom folder via the Groups/Broadcasts category switches or an explicit
	// include/pinned; a folder that only checks Contacts/NonContacts/Bots (private-chat categories) contains no groups/channels.
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
		topMessageID = member.HistoryClearAnchorID
		if topMessageID != member.AvailableMinID {
			topMessageID = 0
		}
	}
	topMessageDate := channel.Date
	if topMessageID > 0 && topMessageID == member.HistoryClearAnchorID {
		topMessageDate = member.HistoryClearAnchorDate
	}
	return domain.ChannelDialog{
		UserID:                 userID,
		ChannelID:              channel.ID,
		TopMessageID:           topMessageID,
		TopMessageDate:         topMessageDate,
		HistoryClearAnchorID:   member.HistoryClearAnchorID,
		HistoryClearAnchorDate: member.HistoryClearAnchorDate,
		ReadInboxMaxID:         maxInt(channel.TopMessageID, member.ReadInboxMaxID),
		ReadOutboxMaxID:        maxInt(channel.TopMessageID, member.ReadOutboxMaxID),
	}
}
