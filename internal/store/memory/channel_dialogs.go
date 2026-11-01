package memory

import (
	"context"
	"errors"
	"sort"
	"telesrv/internal/domain"
)

func (s *ChannelStore) ListChannelDialogs(_ context.Context, viewerUserID int64, filter domain.DialogFilter) (domain.ChannelDialogList, error) {
	if viewerUserID == 0 {
		return domain.ChannelDialogList{}, nil
	}
	limit := filter.Limit
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	channelIDs := make([]int64, 0, len(s.dialogs[viewerUserID]))
	seen := make(map[int64]struct{}, len(s.dialogs[viewerUserID]))
	for channelID := range s.dialogs[viewerUserID] {
		channelIDs = append(channelIDs, channelID)
		seen[channelID] = struct{}{}
	}
	for channelID, members := range s.members {
		if _, ok := seen[channelID]; ok {
			continue
		}
		if member, ok := members[viewerUserID]; ok && member.Status == domain.ChannelMemberActive {
			channelIDs = append(channelIDs, channelID)
			seen[channelID] = struct{}{}
		}
	}
	syntheticMembers := make(map[int64]domain.ChannelMember)
	for channelID, channel := range s.channels {
		if _, ok := seen[channelID]; ok {
			continue
		}
		if !channel.Monoforum || channel.LinkedMonoforumID == 0 || channel.Deleted {
			continue
		}
		parentMember, ok := s.members[channel.LinkedMonoforumID][viewerUserID]
		if !ok || !parentMember.CanManageDirectMessages() {
			continue
		}
		channelIDs = append(channelIDs, channelID)
		seen[channelID] = struct{}{}
		syntheticMembers[channelID] = syntheticMonoforumAdminMember(channel, parentMember)
	}

	items := make([]domain.Dialog, 0, len(channelIDs))
	for _, channelID := range channelIDs {
		channel, ok := s.channels[channelID]
		if !ok || channel.Deleted {
			continue
		}
		member, synthetic := syntheticMembers[channelID]
		if !synthetic {
			if _, err := s.channelForMemberLocked(viewerUserID, channelID); err != nil {
				continue
			}
			member = s.members[channelID][viewerUserID]
		}
		if member.Status != domain.ChannelMemberActive {
			continue
		}
		item := channelDialogToDialog(s.dialogForMemberLocked(viewerUserID, channel, member), channel.Pts, member.Status)
		if !channelDialogMatchesFilter(item, channel, filter) {
			continue
		}
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Pinned != items[j].Pinned {
			return items[i].Pinned
		}
		if items[i].PinnedOrder != items[j].PinnedOrder {
			return items[i].PinnedOrder > items[j].PinnedOrder
		}
		if items[i].TopMessageDate != items[j].TopMessageDate {
			return items[i].TopMessageDate > items[j].TopMessageDate
		}
		if items[i].TopMessage != items[j].TopMessage {
			return items[i].TopMessage > items[j].TopMessage
		}
		return items[i].Peer.ID > items[j].Peer.ID
	})
	out := domain.ChannelDialogList{Count: len(items)}
	for _, dialog := range items {
		if len(out.Dialogs) >= limit {
			break
		}
		out.Dialogs = append(out.Dialogs, dialog)
		channel := s.channels[dialog.Peer.ID]
		out.Channels = append(out.Channels, channel)
		if msg, ok := s.channelMessageForMemberLocked(viewerUserID, dialog.Peer.ID, dialog.TopMessage); ok {
			out.Messages = append(out.Messages, msg)
		}
	}
	// Same reason as PG: getDialogs top messages get their unread-mention flags populated per viewer.
	s.populateChannelMessageUnreadFlagsLocked(viewerUserID, out.Messages)
	return out, nil
}

func (s *ChannelStore) GetChannelDialogs(_ context.Context, viewerUserID int64, channelIDs []int64) (domain.ChannelDialogList, error) {
	if viewerUserID == 0 || len(channelIDs) == 0 {
		return domain.ChannelDialogList{}, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
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
		channel, member, err := s.channelAndMemberLocked(viewerUserID, channelID)
		if err != nil {
			if !errors.Is(err, domain.ErrChannelPrivate) && !errors.Is(err, domain.ErrChannelInvalid) {
				continue
			}
			var ok bool
			channel, ok = s.channels[channelID]
			if !ok || channel.Deleted || !channel.Monoforum || channel.LinkedMonoforumID == 0 {
				continue
			}
			parentMember, ok := s.members[channel.LinkedMonoforumID][viewerUserID]
			if !ok || !parentMember.CanManageDirectMessages() {
				continue
			}
			member = syntheticMonoforumAdminMember(channel, parentMember)
		}
		if member.Status != domain.ChannelMemberActive {
			continue
		}
		dialog := channelDialogToDialog(s.dialogForMemberLocked(viewerUserID, channel, member), channel.Pts, member.Status)
		out.Dialogs = append(out.Dialogs, dialog)
		out.Channels = append(out.Channels, channel)
		// Same as postgres GetChannelDialogs: a monoforum DM dialog must be delivered in
		// the same batch as the parent broadcast channel, so the client can resolve
		// linked_monoforum_id and derive MonoforumAdmin to render the Direct-Messages
		// container; otherwise it degrades into an ordinary megagroup. A monoforum
		// reaching this point is always an admin preview (non-admins get ErrChannelPrivate
		// from channelForViewerLocked and are skipped) and is never leaked to unrelated users.
		if channel.Monoforum && channel.LinkedMonoforumID != 0 {
			if parent, ok := s.channels[channel.LinkedMonoforumID]; ok && !parent.Deleted {
				out.Channels = append(out.Channels, cloneChannel(parent))
			}
		}
		if msg, ok := s.channelMessageForMemberLocked(viewerUserID, channelID, dialog.TopMessage); ok {
			out.Messages = append(out.Messages, msg)
		}
	}
	out.Count = len(out.Dialogs)
	s.populateChannelMessageUnreadFlagsLocked(viewerUserID, out.Messages)
	return out, nil
}

func (s *ChannelStore) ListCommonChannels(_ context.Context, req domain.CommonChannelsRequest) (domain.CommonChannelsResult, error) {
	if req.UserID == 0 || req.TargetUserID == 0 || req.UserID == req.TargetUserID || req.MaxID < 0 {
		return domain.CommonChannelsResult{}, domain.ErrChannelInvalid
	}
	limit := req.Limit
	if limit <= 0 || limit > domain.MaxCommonChannelsLimit {
		limit = domain.MaxCommonChannelsLimit
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	ids := make([]int64, 0)
	for channelID, members := range s.members {
		self, selfOK := members[req.UserID]
		target, targetOK := members[req.TargetUserID]
		if !selfOK || !targetOK || self.Status != domain.ChannelMemberActive || target.Status != domain.ChannelMemberActive {
			continue
		}
		channel, ok := s.channels[channelID]
		if !ok || channel.Deleted || !channel.Megagroup || channel.Broadcast {
			continue
		}
		ids = append(ids, channelID)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	out := domain.CommonChannelsResult{Count: len(ids)}
	if req.CountOnly {
		return out, nil
	}
	for _, channelID := range ids {
		if req.MaxID > 0 && channelID <= req.MaxID {
			continue
		}
		out.Channels = append(out.Channels, cloneChannel(s.channels[channelID]))
		if len(out.Channels) >= limit {
			break
		}
	}
	return out, nil
}

func (s *ChannelStore) ListLeftChannels(_ context.Context, userID int64, offset, limit int) (domain.LeftChannelsResult, error) {
	if userID == 0 || offset < 0 || offset > domain.MaxLeftChannelsOffset {
		return domain.LeftChannelsResult{}, domain.ErrChannelInvalid
	}
	if limit <= 0 || limit > domain.MaxLeftChannelsLimit {
		limit = domain.MaxLeftChannelsLimit
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	all := make([]domain.LeftChannel, 0)
	for channelID, members := range s.members {
		member, ok := members[userID]
		if !ok || member.Status != domain.ChannelMemberLeft {
			continue
		}
		channel, ok := s.channels[channelID]
		if !ok || channel.Deleted || (!channel.Broadcast && !channel.Megagroup) {
			continue
		}
		all = append(all, domain.LeftChannel{
			Channel: cloneChannel(channel),
			Self:    member,
		})
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].Self.LeftAt != all[j].Self.LeftAt {
			return all[i].Self.LeftAt > all[j].Self.LeftAt
		}
		return all[i].Channel.ID > all[j].Channel.ID
	})

	out := domain.LeftChannelsResult{Count: len(all)}
	if offset >= len(all) {
		return out, nil
	}
	end := offset + limit
	if end > len(all) {
		end = len(all)
	}
	out.Channels = append(out.Channels, all[offset:end]...)
	return out, nil
}

func (s *ChannelStore) ListInactiveChannels(_ context.Context, userID int64, limit int) (domain.ChannelDialogList, error) {
	if userID == 0 {
		return domain.ChannelDialogList{}, domain.ErrChannelInvalid
	}
	if limit <= 0 || limit > domain.MaxInactiveChannelsLimit {
		limit = domain.MaxInactiveChannelsLimit
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	type item struct {
		channel domain.Channel
		dialog  domain.Dialog
	}
	items := make([]item, 0, limit)
	for channelID, members := range s.members {
		member, ok := members[userID]
		if !ok || member.Status != domain.ChannelMemberActive {
			continue
		}
		channel, ok := s.channels[channelID]
		if !ok || channel.Deleted || (!channel.Broadcast && !channel.Megagroup) {
			continue
		}
		if _, err := s.channelForMemberLocked(userID, channelID); err != nil {
			continue
		}
		dialog := channelDialogToDialog(s.dialogForUserLocked(userID, channel), channel.Pts, member.Status)
		dialog.TopMessageDate = inactiveChannelDate(dialog, channel, member)
		items = append(items, item{channel: cloneChannel(channel), dialog: dialog})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].dialog.TopMessageDate != items[j].dialog.TopMessageDate {
			return items[i].dialog.TopMessageDate < items[j].dialog.TopMessageDate
		}
		if items[i].dialog.TopMessage != items[j].dialog.TopMessage {
			return items[i].dialog.TopMessage < items[j].dialog.TopMessage
		}
		return items[i].channel.ID < items[j].channel.ID
	})
	if len(items) > limit {
		items = items[:limit]
	}
	out := domain.ChannelDialogList{Count: len(items)}
	for _, item := range items {
		out.Dialogs = append(out.Dialogs, item.dialog)
		out.Channels = append(out.Channels, item.channel)
	}
	return out, nil
}

func (s *ChannelStore) SetChannelDialogPinned(_ context.Context, userID, channelID int64, pinned bool) (bool, int, error) {
	if userID == 0 || channelID == 0 {
		return false, 0, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	channel, err := s.channelForMemberLocked(userID, channelID)
	if err != nil {
		return false, 0, nil
	}
	dialog := s.dialogForUserLocked(userID, channel)
	targetFolderID := dialog.FolderID
	// order is only assigned within the folder the target dialog belongs to; the two
	// memory stores are invisible to each other, so unlike postgres's unified cross-table
	// order space this only looks at the channel table (reorder reassigns everything uniformly).
	nextOrder := 1
	for _, d := range s.dialogs[userID] {
		if d.Pinned && d.FolderID == targetFolderID && d.PinnedOrder >= nextOrder {
			nextOrder = d.PinnedOrder + 1
		}
	}
	changed := dialog.Pinned != pinned || (pinned && dialog.PinnedOrder == 0)
	dialog.Pinned = pinned
	if pinned {
		if dialog.PinnedOrder == 0 {
			dialog.PinnedOrder = nextOrder
		}
	} else {
		dialog.PinnedOrder = 0
	}
	if s.dialogs[userID] == nil {
		s.dialogs[userID] = make(map[int64]domain.ChannelDialog)
	}
	s.dialogs[userID][channelID] = dialog
	return changed, targetFolderID, nil
}

func (s *ChannelStore) ReorderChannelPinnedDialogs(_ context.Context, userID int64, folderID int, order []domain.Peer, force bool) (bool, error) {
	if userID == 0 {
		return false, nil
	}
	positions := make(map[int64]int, len(order))
	for i, peer := range order {
		if peer.Type != domain.PeerTypeChannel || peer.ID == 0 {
			continue
		}
		if _, ok := positions[peer.ID]; ok {
			continue
		}
		positions[peer.ID] = len(order) - i
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	changed := false
	for channelID, dialog := range s.dialogs[userID] {
		if dialog.FolderID != folderID {
			continue
		}
		if pos, ok := positions[channelID]; ok {
			if !dialog.Pinned || dialog.PinnedOrder != pos {
				changed = true
			}
			dialog.Pinned = true
			dialog.PinnedOrder = pos
			s.dialogs[userID][channelID] = dialog
			continue
		}
		if force && dialog.Pinned {
			changed = true
			dialog.Pinned = false
			dialog.PinnedOrder = 0
			s.dialogs[userID][channelID] = dialog
		}
	}
	return changed, nil
}

func (s *ChannelStore) EditChannelPeerFolders(_ context.Context, userID int64, peers []domain.FolderPeerUpdate) error {
	if userID == 0 || len(peers) == 0 {
		return nil
	}
	updates := make(map[int64]int, len(peers))
	for _, item := range peers {
		if item.Peer.Type != domain.PeerTypeChannel || item.Peer.ID == 0 {
			continue
		}
		if item.FolderID != domain.DialogMainFolderID && item.FolderID != domain.DialogArchiveFolderID {
			continue
		}
		updates[item.Peer.ID] = item.FolderID
	}
	if len(updates) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for channelID, folderID := range updates {
		channel, err := s.channelForMemberLocked(userID, channelID)
		if err != nil {
			continue
		}
		dialog := s.dialogForUserLocked(userID, channel)
		// Clear pinned when changing folder: same as private-chat EditPeerFolders,
		// TDesktop unpins unconditionally locally on archive/restore, and keeping an old
		// pin server-side would cause state drift.
		if dialog.FolderID != folderID {
			dialog.Pinned = false
			dialog.PinnedOrder = 0
		}
		dialog.FolderID = folderID
		if s.dialogs[userID] == nil {
			s.dialogs[userID] = make(map[int64]domain.ChannelDialog)
		}
		s.dialogs[userID][channelID] = dialog
	}
	return nil
}

func (s *ChannelStore) CountChannelArchiveUnread(_ context.Context, userID int64) (int, int, error) {
	if userID == 0 {
		return 0, 0, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	peers, messages := 0, 0
	for channelID, dialog := range s.dialogs[userID] {
		if dialog.FolderID != domain.DialogArchiveFolderID {
			continue
		}
		// Same as postgres: only count dialogs of active members.
		if _, err := s.channelForMemberLocked(userID, channelID); err != nil {
			continue
		}
		if dialog.UnreadCount > 0 || dialog.UnreadMark {
			peers++
		}
		messages += dialog.UnreadCount
	}
	return peers, messages, nil
}

func (s *ChannelStore) upsertChannelDialogLocked(userID int64, channel domain.Channel, top domain.ChannelMessage, selfAction bool) {
	if s.dialogs[userID] == nil {
		s.dialogs[userID] = make(map[int64]domain.ChannelDialog)
	}
	dialog := s.dialogs[userID][channel.ID]
	dialog.UserID = userID
	dialog.ChannelID = channel.ID
	dialog.TopMessageID = s.visibleTopMessageIDLocked(userID, channel)
	if top.ID != 0 {
		dialog.TopMessageDate = top.Date
	}
	member := s.members[channel.ID][userID]
	if member.ReadInboxMaxID > dialog.ReadInboxMaxID {
		dialog.ReadInboxMaxID = member.ReadInboxMaxID
	}
	if selfAction {
		// Only advance inbox: messages produced by one's own actions do not count as one's
		// own unread. The outbox read-receipt watermark means "the largest ID read by other
		// members" and must never advance with one's own send/action, otherwise another
		// device on the same account would render one's own messages as read by the peer.
		if channel.TopMessageID > dialog.ReadInboxMaxID {
			dialog.ReadInboxMaxID = channel.TopMessageID
		}
		// On the send direction, clear the manual unread mark, matching postgres's two-table clear on the send path.
		dialog.UnreadMark = false
	}
	dialog.UnreadCount = s.channelUnreadCountLocked(userID, channel.ID, dialog.ReadInboxMaxID, dialog.TopMessageID)
	dialog.UnreadMentions = s.countChannelUnreadMentionsLocked(userID, channel.ID, 0)
	dialog.UnreadReactions = s.countChannelUnreadReactionsLocked(userID, channel.ID, 0)
	s.dialogs[userID][channel.ID] = dialog
}

func channelDialogToDialog(dialog domain.ChannelDialog, channelPts int, memberStatus domain.ChannelMemberStatus) domain.Dialog {
	return domain.Dialog{
		Peer: domain.Peer{Type: domain.PeerTypeChannel, ID: dialog.ChannelID},
		// Non-member previews (publicPreviewMember/kicked) must be marked ChannelLeft, so the client renders the channel as a read-only left preview.
		ChannelLeft:            memberStatus == domain.ChannelMemberLeft,
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
		Pts:                    channelPts,
	}
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

func channelDialogMatchesFilter(dialog domain.Dialog, channel domain.Channel, filter domain.DialogFilter) bool {
	if filter.HasFolderID {
		if filter.FolderID < domain.DialogCustomFolderMinID {
			if dialog.FolderID != filter.FolderID {
				return false
			}
		} else if filter.Folder == nil {
			return false
		}
	} else if dialog.FolderID != domain.DialogMainFolderID {
		// No folder_id is treated as the main list (folder 0), consistent with the private-chat side / official semantics.
		return false
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
	if peerInFolderList(dialog.Peer, folder.ExcludePeers) {
		return false
	}
	if folder.ExcludeRead && dialog.UnreadCount == 0 && !dialog.UnreadMark {
		return false
	}
	if folder.ExcludeArchived && dialog.FolderID == domain.DialogArchiveFolderID {
		return false
	}
	if peerInFolderList(dialog.Peer, folder.PinnedPeers) || peerInFolderList(dialog.Peer, folder.IncludePeers) {
		return true
	}
	if channel.Megagroup && folder.Groups {
		return true
	}
	if channel.Broadcast && folder.Broadcasts {
		return true
	}
	// Groups/channels enter a custom folder only through the Groups/Broadcasts toggles or
	// an explicit include/pinned; a folder with only Contacts/NonContacts/Bots ticked
	// contains no groups/channels (aligned with postgres).
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

func peerInFolderList(peer domain.Peer, items []domain.DialogFolderPeer) bool {
	for _, item := range items {
		if item.Peer == peer {
			return true
		}
	}
	return false
}
