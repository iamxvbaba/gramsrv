package rpc

import (
	"github.com/gotd/td/tg"
	"telesrv/internal/domain"
)

func tgMessagesDialogs(viewerUserID int64, list domain.DialogList) tg.MessagesDialogsClass {
	dialogs := make([]tg.DialogClass, 0, len(list.Dialogs)+1)
	// dialogFolder 条目排在最前：TDesktop 据它发现 archive folder 并渲染
	// 归档行的未读徽章（Folder::applyDialog / MainList::updateCloudUnread）。
	if folder := tgDialogFolder(list.ArchiveSummary); folder != nil {
		dialogs = append(dialogs, folder)
	}
	for _, d := range list.Dialogs {
		if dialog := tgDialog(d); dialog != nil {
			dialogs = append(dialogs, dialog)
		}
	}
	messages := make([]tg.MessageClass, 0, len(list.Messages))
	for _, msg := range list.Messages {
		if item := tgMessage(msg); item != nil {
			messages = append(messages, item)
		}
	}
	for _, msg := range list.ChannelMessages {
		if item := tgChannelMessage(viewerUserID, msg); item != nil {
			messages = append(messages, item)
		}
	}
	users := tgUsersForViewer(viewerUserID, list.Users)
	chats := tgChannelsForDialogs(viewerUserID, list.Channels, list.Dialogs)
	if list.Count > len(dialogs) {
		return &tg.MessagesDialogsSlice{
			Count:    list.Count,
			Dialogs:  dialogs,
			Messages: messages,
			Chats:    chats,
			Users:    users,
		}
	}
	return &tg.MessagesDialogs{
		Dialogs:  dialogs,
		Messages: messages,
		Chats:    chats,
		Users:    users,
	}
}

func tgPeerDialogs(viewerUserID int64, list domain.DialogList, st domain.UpdateState) *tg.MessagesPeerDialogs {
	out := &tg.MessagesPeerDialogs{
		Dialogs:  make([]tg.DialogClass, 0, len(list.Dialogs)+1),
		Messages: make([]tg.MessageClass, 0, len(list.Messages)+len(list.ChannelMessages)),
		Users:    make([]tg.UserClass, 0, len(list.Users)),
		Chats:    make([]tg.ChatClass, 0, len(list.Channels)),
		State:    tgUpdateState(st),
	}
	// getPinnedDialogs(folder_id=0) 必须带 dialogFolder 条目：DrKLO 主列表
	// getDialogs 一律 exclude_pinned，archive 行只能从 pinned 响应发现
	// （fetchFolderInLoadedPinnedDialogs，且要求 top_message/peer 非零）。
	if folder := tgDialogFolder(list.ArchiveSummary); folder != nil {
		out.Dialogs = append(out.Dialogs, folder)
	}
	for _, d := range list.Dialogs {
		if dialog := tgDialog(d); dialog != nil {
			out.Dialogs = append(out.Dialogs, dialog)
		}
	}
	for _, msg := range list.Messages {
		if item := tgMessage(msg); item != nil {
			out.Messages = append(out.Messages, item)
		}
	}
	for _, msg := range list.ChannelMessages {
		if item := tgChannelMessage(viewerUserID, msg); item != nil {
			out.Messages = append(out.Messages, item)
		}
	}
	for _, u := range list.Users {
		if viewerUserID != 0 && u.ID == viewerUserID {
			out.Users = append(out.Users, tgSelfUser(u))
		} else {
			out.Users = append(out.Users, tgUser(u))
		}
	}
	out.Chats = append(out.Chats, tgChannelsForDialogs(viewerUserID, list.Channels, list.Dialogs)...)
	return out
}

// tgDialogFolder 把归档摘要转成 dialogFolder#71bd134c 条目。当前未接
// per-peer mute 状态，未读计数全部归入 unmuted 桶（TDesktop 用 unmuted
// 桶渲染亮色徽章，muted 桶渲染灰色，全归 unmuted 只影响徽章颜色不丢计数）。
func tgDialogFolder(summary *domain.DialogArchiveSummary) tg.DialogClass {
	if summary == nil {
		return nil
	}
	peer := tgPeer(summary.TopPeer)
	if peer == nil {
		return nil
	}
	return &tg.DialogFolder{
		Pinned: summary.Pinned,
		Folder: tg.Folder{
			ID:    domain.DialogArchiveFolderID,
			Title: "Archived Chats",
		},
		Peer:                       peer,
		TopMessage:                 summary.TopMessage,
		UnreadUnmutedPeersCount:    summary.UnreadPeersCount,
		UnreadUnmutedMessagesCount: summary.UnreadMessagesCount,
	}
}

func tgDialog(d domain.Dialog) *tg.Dialog {
	peer := tgPeer(d.Peer)
	if peer == nil {
		return nil
	}
	out := &tg.Dialog{
		Pinned:               d.Pinned,
		UnreadMark:           d.UnreadMark,
		ViewForumAsMessages:  d.ViewForumAsMessages,
		Peer:                 peer,
		TopMessage:           d.TopMessage,
		ReadInboxMaxID:       d.ReadInboxMaxID,
		ReadOutboxMaxID:      d.ReadOutboxMaxID,
		UnreadCount:          d.UnreadCount,
		UnreadMentionsCount:  d.UnreadMentions,
		UnreadReactionsCount: d.UnreadReactions,
		NotifySettings:       *tgPeerNotifySettings(d.NotifySettings),
	}
	if d.FolderID != domain.DialogMainFolderID {
		out.SetFolderID(d.FolderID)
	}
	if d.TTLPeriod > 0 {
		out.SetTTLPeriod(d.TTLPeriod)
	}
	if d.Peer.Type == domain.PeerTypeChannel && d.Pts > 0 {
		// 客户端用 dialog.pts 初始化 channel 本地序列；缺失会让
		// getChannelDifference 起点失效（冷启动 gap 不被发现）。
		out.SetPts(d.Pts)
	}
	if d.Draft != nil {
		out.SetDraft(tgDialogDraft(*d.Draft))
	}
	return out
}

func tgDialogDraft(d domain.DialogDraft) tg.DraftMessageClass {
	if d.Empty() {
		out := &tg.DraftMessageEmpty{}
		out.SetDate(d.Date)
		return out
	}
	out := &tg.DraftMessage{
		NoWebpage:   d.NoWebpage,
		InvertMedia: d.InvertMedia,
		ReplyTo:     tgDraftReplyTo(d),
		Message:     d.Message,
		Entities:    tgMessageEntities(d.Entities),
		Media:       tgDraftWebPage(d.WebPage),
		Date:        d.Date,
		Effect:      d.Effect,
	}
	return out
}

func tgDraftReplyTo(d domain.DialogDraft) tg.InputReplyToClass {
	if d.ReplyTo == nil {
		return nil
	}
	reply := &tg.InputReplyToMessage{ReplyToMsgID: d.ReplyTo.MessageID}
	if d.ReplyTo.TopMessageID > 0 {
		reply.SetTopMsgID(d.ReplyTo.TopMessageID)
	}
	if d.ReplyTo.QuoteText != "" {
		reply.SetQuoteText(d.ReplyTo.QuoteText)
	}
	if len(d.ReplyTo.QuoteEntities) > 0 {
		reply.SetQuoteEntities(tgMessageEntities(d.ReplyTo.QuoteEntities))
	}
	if d.ReplyTo.QuoteOffset > 0 {
		reply.SetQuoteOffset(d.ReplyTo.QuoteOffset)
	}
	return reply
}

func tgDraftWebPage(webpage *domain.DialogDraftWebPage) tg.InputMediaClass {
	if webpage == nil || webpage.URL == "" {
		return nil
	}
	return &tg.InputMediaWebPage{
		ForceLargeMedia: webpage.ForceLargeMedia,
		ForceSmallMedia: webpage.ForceSmallMedia,
		Optional:        webpage.Optional,
		URL:             webpage.URL,
	}
}

func tgDialogPeer(p domain.Peer) tg.DialogPeerClass {
	peer := tgPeer(p)
	if peer == nil {
		return nil
	}
	return &tg.DialogPeer{Peer: peer}
}

func tgDialogPeers(peers []domain.Peer) []tg.DialogPeerClass {
	out := make([]tg.DialogPeerClass, 0, len(peers))
	for _, peer := range peers {
		item := tgDialogPeer(peer)
		if item != nil {
			out = append(out, item)
		}
	}
	return out
}

func tgDialogFilters(list domain.DialogFolderList) *tg.MessagesDialogFilters {
	filters := make([]tg.DialogFilterClass, 0, len(list.Folders)+1)
	filters = append(filters, &tg.DialogFilterDefault{})
	for _, folder := range list.Folders {
		if item := tgDialogFilter(folder); item != nil {
			filters = append(filters, item)
		}
	}
	return &tg.MessagesDialogFilters{TagsEnabled: list.TagsEnabled, Filters: filters}
}

func tgDialogFilter(folder domain.DialogFolder) tg.DialogFilterClass {
	title := tg.TextWithEntities{Text: folder.Title, Entities: tgMessageEntities(folder.TitleEntities)}
	if folder.IsChatlist {
		out := &tg.DialogFilterChatlist{
			TitleNoanimate: folder.TitleNoanimate,
			ID:             folder.ID,
			Title:          title,
			PinnedPeers:    tgDialogFolderInputPeers(folder.PinnedPeers),
			IncludePeers:   tgDialogFolderInputPeers(folder.IncludePeers),
		}
		if folder.HasEmoticon {
			out.SetEmoticon(folder.Emoticon)
		}
		if folder.HasColor {
			out.SetColor(folder.Color)
		}
		return out
	}
	out := &tg.DialogFilter{
		Contacts:        folder.Contacts,
		NonContacts:     folder.NonContacts,
		Groups:          folder.Groups,
		Broadcasts:      folder.Broadcasts,
		Bots:            folder.Bots,
		ExcludeMuted:    folder.ExcludeMuted,
		ExcludeRead:     folder.ExcludeRead,
		ExcludeArchived: folder.ExcludeArchived,
		TitleNoanimate:  folder.TitleNoanimate,
		ID:              folder.ID,
		Title:           title,
		PinnedPeers:     tgDialogFolderInputPeers(folder.PinnedPeers),
		IncludePeers:    tgDialogFolderInputPeers(folder.IncludePeers),
		ExcludePeers:    tgDialogFolderInputPeers(folder.ExcludePeers),
	}
	if folder.HasEmoticon {
		out.SetEmoticon(folder.Emoticon)
	}
	if folder.HasColor {
		out.SetColor(folder.Color)
	}
	return out
}

func tgDialogFolderInputPeers(peers []domain.DialogFolderPeer) []tg.InputPeerClass {
	if len(peers) == 0 {
		return nil
	}
	out := make([]tg.InputPeerClass, 0, len(peers))
	for _, item := range peers {
		switch item.Peer.Type {
		case domain.PeerTypeUser:
			out = append(out, &tg.InputPeerUser{UserID: item.Peer.ID, AccessHash: item.AccessHash})
		case domain.PeerTypeChannel:
			out = append(out, &tg.InputPeerChannel{ChannelID: item.Peer.ID, AccessHash: item.AccessHash})
		}
	}
	return out
}

func tgFolderPeers(peers []domain.FolderPeerUpdate) []tg.FolderPeer {
	out := make([]tg.FolderPeer, 0, len(peers))
	for _, item := range peers {
		peer := tgPeer(item.Peer)
		if peer == nil {
			continue
		}
		out = append(out, tg.FolderPeer{Peer: peer, FolderID: item.FolderID})
	}
	return out
}

func tgChannelsForDialogs(viewerUserID int64, channels []domain.Channel, dialogs []domain.Dialog) []tg.ChatClass {
	left := make(map[int64]struct{})
	for _, dialog := range dialogs {
		if dialog.Peer.Type == domain.PeerTypeChannel && dialog.Peer.ID != 0 && dialog.ChannelLeft {
			left[dialog.Peer.ID] = struct{}{}
		}
	}
	out := make([]tg.ChatClass, 0, len(channels))
	seen := make(map[int64]struct{}, len(channels))
	for _, ch := range channels {
		if ch.ID == 0 {
			continue
		}
		if _, ok := seen[ch.ID]; ok {
			continue
		}
		seen[ch.ID] = struct{}{}
		var self *domain.ChannelMember
		if _, ok := left[ch.ID]; ok {
			self = &domain.ChannelMember{
				ChannelID: ch.ID,
				UserID:    viewerUserID,
				Status:    domain.ChannelMemberLeft,
			}
		} else {
			self = &domain.ChannelMember{
				ChannelID: ch.ID,
				UserID:    viewerUserID,
				Status:    domain.ChannelMemberActive,
			}
		}
		out = append(out, tgChannelChat(viewerUserID, ch, self))
	}
	return out
}
