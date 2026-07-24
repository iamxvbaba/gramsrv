package rpc

import (
	"github.com/iamxvbaba/td/tg"
	"telesrv/internal/domain"
)

func tgUpdateState(st domain.UpdateState) tg.UpdatesState {
	return tg.UpdatesState{Pts: st.Pts, Qts: st.Qts, Date: st.Date, Seq: st.Seq}
}

func tgUpdatesDifference(viewerUserID int64, diff domain.UpdateDifference) tg.UpdatesDifferenceClass {
	out := &tg.UpdatesDifference{
		NewMessages:  make([]tg.MessageClass, 0, len(diff.Events)),
		OtherUpdates: make([]tg.UpdateClass, 0, len(diff.Events)),
		Users:        make([]tg.UserClass, 0, 1),
		Chats:        make([]tg.ChatClass, 0, 1),
	}
	seenUsers := make(map[int64]struct{})
	seenChats := make(map[int64]struct{})
	for _, event := range diff.Events {
		addUsers(out, seenUsers, viewerUserID, event.Users)
		addChannels(out, seenChats, event.UserID, event.Channels)
		switch event.Type {
		case domain.UpdateEventNewMessage:
			if msg := tgMessage(event.Message); msg != nil {
				out.NewMessages = append(out.NewMessages, msg)
				addMessageUsers(out, seenUsers, event.Message)
			}
		case domain.UpdateEventReadHistoryInbox:
			if update := tgReadHistoryInboxUpdate(event); update != nil {
				out.OtherUpdates = append(out.OtherUpdates, update)
			}
		case domain.UpdateEventReadHistoryOutbox:
			if update := tgReadHistoryOutboxUpdate(event); update != nil {
				out.OtherUpdates = append(out.OtherUpdates, update)
			}
		case domain.UpdateEventMessageReactions, domain.UpdateEventMessagePoll:
			// 同时下发消息快照（含最新聚合）与对应通知 update；事件无 TL pts，
			// pts 推进靠 difference state 本身。
			if msg := tgMessage(event.Message); msg != nil {
				out.NewMessages = append(out.NewMessages, msg)
				addMessageUsers(out, seenUsers, event.Message)
			}
			if update := tgOtherUpdateFromEvent(event); update != nil {
				out.OtherUpdates = append(out.OtherUpdates, update)
			}
		default:
			if update := tgOtherUpdateFromEvent(event); update != nil {
				out.OtherUpdates = append(out.OtherUpdates, update)
			}
		}
	}
	for _, nudge := range diff.ChannelNudges {
		if nudge.ChannelID == 0 {
			continue
		}
		update := &tg.UpdateChannelTooLong{ChannelID: nudge.ChannelID}
		if nudge.Pts > 0 {
			update.SetPts(nudge.Pts)
		}
		out.OtherUpdates = append(out.OtherUpdates, update)
		if nudge.Channel != nil && nudge.Channel.Channel.ID != 0 {
			addChannelNudgeChat(out, seenChats, tgChannelChatForView(viewerUserID, *nudge.Channel))
		}
	}
	// Partial：连续事件被 limit 截断、后面还有 → updates.differenceSlice，客户端据 IntermediateState 续拉。
	if diff.Partial {
		return &tg.UpdatesDifferenceSlice{
			NewMessages:       out.NewMessages,
			OtherUpdates:      out.OtherUpdates,
			Chats:             out.Chats,
			Users:             out.Users,
			IntermediateState: tgUpdateState(diff.State),
		}
	}
	out.State = tgUpdateState(diff.State)
	return out
}

func tgChannelDifference(viewerUserID int64, diff domain.ChannelDifference) tg.UpdatesChannelDifferenceClass {
	if diff.TooLong {
		messages := make([]tg.MessageClass, 0, len(diff.NewMessages))
		for _, msg := range diff.NewMessages {
			if item := tgChannelMessage(viewerUserID, msg); item != nil {
				messages = append(messages, item)
			}
		}
		dialog := tgDialog(domain.Dialog{
			Peer:            domain.Peer{Type: domain.PeerTypeChannel, ID: diff.Channel.ID},
			TopMessage:      diff.Dialog.TopMessageID,
			TopMessageDate:  diff.Dialog.TopMessageDate,
			ReadInboxMaxID:  diff.Dialog.ReadInboxMaxID,
			ReadOutboxMaxID: diff.Dialog.ReadOutboxMaxID,
			UnreadCount:     diff.Dialog.UnreadCount,
			UnreadMentions:  diff.Dialog.UnreadMentions,
			UnreadReactions: diff.Dialog.UnreadReactions,
			UnreadMark:      diff.Dialog.UnreadMark,
			FolderID:        diff.Dialog.FolderID,
		})
		if dialog != nil {
			dialog.SetPts(diff.Pts)
		}
		return &tg.UpdatesChannelDifferenceTooLong{
			Final:    diff.Final,
			Timeout:  diff.Timeout,
			Dialog:   dialog,
			Messages: messages,
			Chats:    tgChannelDifferenceChats(viewerUserID, diff),
			Users:    tgUsersForViewer(viewerUserID, diff.Users),
		}
	}
	if len(diff.Events) == 0 && len(diff.NewMessages) == 0 && len(diff.OtherUpdates) == 0 {
		return &tg.UpdatesChannelDifferenceEmpty{
			Final:   diff.Final,
			Pts:     diff.Pts,
			Timeout: diff.Timeout,
		}
	}
	messages := make([]tg.MessageClass, 0, len(diff.NewMessages))
	for _, msg := range diff.NewMessages {
		if item := tgChannelMessage(viewerUserID, msg); item != nil {
			messages = append(messages, item)
		}
	}
	updates := make([]tg.UpdateClass, 0, len(diff.OtherUpdates))
	// 断线后经差量首次看到自己消息时，updateMessageID 让客户端把本地
	// pending 按 random_id 对账，避免短暂的重复气泡。
	for _, msg := range diff.NewMessages {
		if msg.SenderUserID == viewerUserID && viewerUserID != 0 && msg.RandomID != 0 && msg.ID > 0 {
			updates = append(updates, &tg.UpdateMessageID{ID: msg.ID, RandomID: msg.RandomID})
		}
	}
	for _, event := range diff.OtherUpdates {
		if update := tgChannelUpdate(viewerUserID, event); update != nil {
			updates = append(updates, update)
		}
	}
	chats := tgChannelDifferenceChats(viewerUserID, diff)
	users := tgUsersForViewer(viewerUserID, diff.Users)
	return &tg.UpdatesChannelDifference{
		Final:        diff.Final,
		Pts:          diff.Pts,
		Timeout:      diff.Timeout,
		NewMessages:  messages,
		OtherUpdates: updates,
		Chats:        chats,
		Users:        users,
	}
}

func tgChannelDifferenceChats(viewerUserID int64, diff domain.ChannelDifference) []tg.ChatClass {
	return tgChannelChatsWithPrimarySelf(viewerUserID, diff.Channel, diff.Channels, diff.Self)
}

func tgChannelUpdate(viewerUserID int64, event domain.ChannelUpdateEvent) tg.UpdateClass {
	switch event.Type {
	case domain.ChannelUpdateNewMessage:
		msg := tgChannelMessage(viewerUserID, event.Message)
		if msg == nil {
			return nil
		}
		return &tg.UpdateNewChannelMessage{Message: msg, Pts: event.Pts, PtsCount: event.PtsCount}
	case domain.ChannelUpdateEditMessage:
		msg := tgChannelMessage(viewerUserID, event.Message)
		if msg == nil {
			return nil
		}
		return &tg.UpdateEditChannelMessage{Message: msg, Pts: event.Pts, PtsCount: event.PtsCount}
	case domain.ChannelUpdateWebPage:
		// 频道链接预览就地替换：updateChannelWebPage 仅携已解析 webPage（按 id 关联消息里的
		// pending 占位），客户端就地换卡片、不触碰 edit_date。
		if event.Message.Media == nil || event.Message.Media.WebPage == nil {
			return nil
		}
		return &tg.UpdateChannelWebPage{
			ChannelID: event.ChannelID,
			Webpage:   tgWebPage(*event.Message.Media.WebPage),
			Pts:       event.Pts,
			PtsCount:  event.PtsCount,
		}
	case domain.ChannelUpdateDeleteMessages:
		if len(event.MessageIDs) == 0 {
			return nil
		}
		return &tg.UpdateDeleteChannelMessages{
			ChannelID: event.ChannelID,
			Messages:  append([]int(nil), event.MessageIDs...),
			Pts:       event.Pts,
			PtsCount:  event.PtsCount,
		}
	case domain.ChannelUpdatePinnedMessages:
		if len(event.MessageIDs) == 0 {
			return nil
		}
		return &tg.UpdatePinnedChannelMessages{
			Pinned:    event.Pinned,
			ChannelID: event.ChannelID,
			Messages:  append([]int(nil), event.MessageIDs...),
			Pts:       event.Pts,
			PtsCount:  event.PtsCount,
		}
	case domain.ChannelUpdateParticipant:
		userID := event.Participant.UserID
		if userID == 0 {
			userID = event.Previous.UserID
		}
		if userID == 0 {
			return nil
		}
		actorID := event.SenderUserID
		if actorID == 0 {
			actorID = userID
		}
		update := &tg.UpdateChannelParticipant{
			ChannelID: event.ChannelID,
			Date:      event.Date,
			ActorID:   actorID,
			UserID:    userID,
		}
		if event.Previous.UserID != 0 {
			update.SetPrevParticipant(tgChannelParticipantForUpdate(viewerUserID, event.Previous))
		}
		if event.Participant.UserID != 0 {
			update.SetNewParticipant(tgChannelParticipantForUpdate(viewerUserID, event.Participant))
		}
		return update
	default:
		return nil
	}
}

func tgOtherUpdateFromEvent(event domain.UpdateEvent) tg.UpdateClass {
	switch event.Type {
	case domain.UpdateEventUserPhone:
		if event.UserID == 0 || event.Phone == "" {
			return nil
		}
		return &tg.UpdateUserPhone{UserID: event.UserID, Phone: event.Phone}
	case domain.UpdateEventUserEmojiStatus:
		if event.UserID == 0 || !event.EmojiStatus.Valid() {
			return nil
		}
		return &tg.UpdateUserEmojiStatus{UserID: event.UserID, EmojiStatus: tgUserEmojiStatusValue(event.EmojiStatus)}
	case domain.UpdateEventUserProfile:
		if event.Peer.Type != domain.PeerTypeUser || event.Peer.ID == 0 {
			return nil
		}
		return &tg.UpdateUser{UserID: event.Peer.ID}
	case domain.UpdateEventChannelState:
		if event.Peer.Type != domain.PeerTypeChannel || event.Peer.ID == 0 {
			return nil
		}
		return &tg.UpdateChannel{ChannelID: event.Peer.ID}
	case domain.UpdateEventStory:
		peer := tgPeer(event.Peer)
		if peer == nil {
			peer = tgPeer(event.Story.Owner)
		}
		if peer == nil || event.Story.ID == 0 {
			return nil
		}
		return &tg.UpdateStory{Peer: peer, Story: tgStoryItem(event.Story)}
	case domain.UpdateEventReadStories:
		peer := tgPeer(event.Peer)
		if peer == nil || event.MaxID <= 0 {
			return nil
		}
		return &tg.UpdateReadStories{Peer: peer, MaxID: event.MaxID}
	case domain.UpdateEventSentStoryReaction:
		peer := tgPeer(event.Peer)
		if peer == nil || event.MaxID <= 0 {
			return nil
		}
		reaction := tg.ReactionClass(&tg.ReactionEmpty{})
		if event.Reaction != nil {
			reaction = tgMessageReaction(*event.Reaction)
		}
		return &tg.UpdateSentStoryReaction{Peer: peer, StoryID: event.MaxID, Reaction: reaction}
	case domain.UpdateEventNewStoryReaction:
		peer := tgPeer(event.Peer)
		if peer == nil || event.MaxID <= 0 || event.Reaction == nil {
			return nil
		}
		return &tg.UpdateNewStoryReaction{Peer: peer, StoryID: event.MaxID, Reaction: tgMessageReaction(*event.Reaction)}
	case domain.UpdateEventQuickReplies:
		return &tg.UpdateQuickReplies{QuickReplies: tgQuickReplies(event.QuickReplies)}
	case domain.UpdateEventNewQuickReply:
		return &tg.UpdateNewQuickReply{QuickReply: tgQuickReply(event.QuickReply)}
	case domain.UpdateEventDeleteQuickReply:
		if event.MaxID <= 0 {
			return nil
		}
		return &tg.UpdateDeleteQuickReply{ShortcutID: event.MaxID}
	case domain.UpdateEventQuickReplyMessage:
		msg := tgQuickReplyMessage(event.QuickReplyMessage)
		if msg == nil {
			return nil
		}
		return &tg.UpdateQuickReplyMessage{Message: msg}
	case domain.UpdateEventDeleteQuickReplyMessages:
		if event.MaxID <= 0 || len(event.MessageIDs) == 0 {
			return nil
		}
		return &tg.UpdateDeleteQuickReplyMessages{
			ShortcutID: event.MaxID,
			Messages:   append([]int(nil), event.MessageIDs...),
		}
	case domain.UpdateEventContactsReset:
		return &tg.UpdateContactsReset{}
	case domain.UpdateEventDialogPinned:
		// archive folder 行自身的置顶：peer 是 dialogPeerFolder 且绝不能带
		// folder_id flag（TDesktop Folder::applyPinnedUpdate 视其为
		// "Nested folders" 错误）。
		if event.Peer.Type == domain.PeerTypeFolder {
			if event.Peer.ID == 0 {
				return nil
			}
			return &tg.UpdateDialogPinned{Pinned: event.Bool, Peer: &tg.DialogPeerFolder{FolderID: int(event.Peer.ID)}}
		}
		peer := tgDialogPeer(event.Peer)
		if peer == nil {
			return nil
		}
		// FolderID 零值不编码 flag（EncodeBare 自动 SetFlags），归档内置顶
		// 必须带 folder_id=1，否则离线重放会把它应用到主列表。
		return &tg.UpdateDialogPinned{Pinned: event.Bool, Peer: peer, FolderID: event.FolderID}
	case domain.UpdateEventPinnedDialogs:
		update := &tg.UpdatePinnedDialogs{FolderID: event.FolderID}
		if len(event.Peers) > 0 {
			update.Order = tgDialogPeers(event.Peers)
		}
		return update
	case domain.UpdateEventSavedDialogPinned:
		peer := tgDialogPeer(event.Peer)
		if peer == nil {
			return nil
		}
		return &tg.UpdateSavedDialogPinned{Pinned: event.Bool, Peer: peer}
	case domain.UpdateEventPinnedSavedDialogs:
		update := &tg.UpdatePinnedSavedDialogs{}
		if len(event.Peers) > 0 {
			update.SetOrder(tgDialogPeers(event.Peers))
		}
		return update
	case domain.UpdateEventDialogUnreadMark:
		peer := tgDialogPeer(event.Peer)
		if peer == nil {
			return nil
		}
		return &tg.UpdateDialogUnreadMark{Unread: event.Bool, Peer: peer}
	case domain.UpdateEventDraftMessage:
		peer := tgPeer(event.Peer)
		if peer == nil {
			return nil
		}
		// Draft 由 enrichDraftMessageEvent 按当前权威态填充；nil/空 = 草稿已删。
		var draft tg.DraftMessageClass
		if event.Draft != nil && !event.Draft.Empty() {
			draft = tgDialogDraft(*event.Draft)
		} else {
			empty := &tg.DraftMessageEmpty{}
			if event.Date > 0 {
				empty.SetDate(event.Date)
			}
			draft = empty
		}
		update := &tg.UpdateDraftMessage{Peer: peer, Draft: draft}
		if event.MaxID > 0 {
			update.SetTopMsgID(event.MaxID)
		}
		return update
	case domain.UpdateEventChannelViewForum:
		if event.Peer.Type != domain.PeerTypeChannel || event.Peer.ID == 0 {
			return nil
		}
		return &tg.UpdateChannelViewForumAsMessages{ChannelID: event.Peer.ID, Enabled: event.Bool}
	case domain.UpdateEventReadChannelDiscussionInbox:
		if event.Peer.Type != domain.PeerTypeChannel || event.Peer.ID == 0 {
			return nil
		}
		return &tg.UpdateReadChannelDiscussionInbox{ChannelID: event.Peer.ID, TopMsgID: event.TopMsgID, ReadMaxID: event.MaxID}
	case domain.UpdateEventReadChannelDiscussionOutbox:
		if event.Peer.Type != domain.PeerTypeChannel || event.Peer.ID == 0 {
			return nil
		}
		return &tg.UpdateReadChannelDiscussionOutbox{ChannelID: event.Peer.ID, TopMsgID: event.TopMsgID, ReadMaxID: event.MaxID}
	case domain.UpdateEventPeerSettings:
		peer := tgPeer(event.Peer)
		if peer == nil {
			return nil
		}
		return &tg.UpdatePeerSettings{Peer: peer, Settings: tgPeerSettings(event.Settings)}
	case domain.UpdateEventPeerStoryBlocked:
		peer := tgPeer(event.Peer)
		if peer == nil {
			return nil
		}
		return &tg.UpdatePeerBlocked{PeerID: peer, Blocked: event.Bool, BlockedMyStoriesFrom: true}
	case domain.UpdateEventDeleteMessages:
		if len(event.MessageIDs) == 0 {
			return nil
		}
		return &tg.UpdateDeleteMessages{
			Messages: append([]int(nil), event.MessageIDs...),
			Pts:      event.Pts,
			PtsCount: event.PtsCount,
		}
	case domain.UpdateEventPinnedMessages:
		peer := tgPeer(event.Peer)
		if peer == nil || len(event.MessageIDs) == 0 {
			return nil
		}
		return &tg.UpdatePinnedMessages{
			Pinned:   event.Bool,
			Peer:     peer,
			Messages: append([]int(nil), event.MessageIDs...),
			Pts:      event.Pts,
			PtsCount: event.PtsCount,
		}
	case domain.UpdateEventReadHistoryOutbox:
		return tgReadHistoryOutbox(event)
	case domain.UpdateEventReadMessageContents:
		if len(event.MessageIDs) == 0 {
			return nil
		}
		contents := &tg.UpdateReadMessagesContents{
			Messages: append([]int(nil), event.MessageIDs...),
			Pts:      event.Pts,
			PtsCount: event.PtsCount,
		}
		if event.Date > 0 {
			// date 是内容被读取的时刻：客户端用它调度 TTL 媒体的
			// "自读取起算"删除与 sender 侧已听时间。
			contents.SetDate(event.Date)
		}
		return contents
	case domain.UpdateEventEditMessage:
		msg := tgMessage(event.Message)
		if msg == nil {
			return nil
		}
		return &tg.UpdateEditMessage{
			Message:  msg,
			Pts:      event.Pts,
			PtsCount: event.PtsCount,
		}
	case domain.UpdateEventWebPage:
		// 链接预览就地替换：updateWebPage 仅携带已解析 webPage（按 webPage id 与消息里的
		// pending 占位关联），客户端就地换卡片、不触碰 edit_date。
		if event.Message.Media == nil || event.Message.Media.WebPage == nil {
			return nil
		}
		return &tg.UpdateWebPage{
			Webpage:  tgWebPage(*event.Message.Media.WebPage),
			Pts:      event.Pts,
			PtsCount: event.PtsCount,
		}
	case domain.UpdateEventMessagePoll:
		if event.Message.ID <= 0 || event.Message.ID > domain.MaxMessageBoxID {
			return nil
		}
		pollPeer := event.Message.Peer
		if pollPeer.Type == "" || pollPeer.ID == 0 {
			pollPeer = event.Peer
		}
		media := event.Message.Media
		if media == nil || media.Kind != domain.MessageMediaKindPoll || media.Poll == nil {
			return nil
		}
		return tgUpdateMessagePoll(pollPeer, event.Message.ID, media.Poll)
	case domain.UpdateEventMessageReactions:
		if event.Message.ID <= 0 || event.Message.ID > domain.MaxMessageBoxID {
			return nil
		}
		peer := event.Message.Peer
		if peer.Type == "" || peer.ID == 0 {
			peer = event.Peer
		}
		outPeer := tgPeer(peer)
		if outPeer == nil {
			return nil
		}
		reactions := event.Message.Reactions
		if reactions == nil {
			empty := domain.ChannelMessageReactions{CanSeeList: true, Results: []domain.ChannelMessageReactionCount{}, Recent: []domain.ChannelMessagePeerReaction{}}
			reactions = &empty
		}
		converted := tgMessageReactions(event.UserID, reactions)
		if converted == nil {
			converted = &tg.MessageReactions{Results: []tg.ReactionCount{}}
		}
		return &tg.UpdateMessageReactions{
			Peer:      outPeer,
			MsgID:     event.Message.ID,
			Reactions: *converted,
		}
	case domain.UpdateEventDialogFilter:
		update := &tg.UpdateDialogFilter{ID: event.FilterID}
		if event.DialogFilter != nil {
			update.SetFilter(tgDialogFilter(*event.DialogFilter))
		}
		return update
	case domain.UpdateEventDialogFilterOrder:
		return &tg.UpdateDialogFilterOrder{Order: append([]int(nil), event.FilterOrder...)}
	case domain.UpdateEventDialogFilters:
		return &tg.UpdateDialogFilters{}
	case domain.UpdateEventFolderPeers:
		if len(event.FolderPeers) == 0 {
			return nil
		}
		return &tg.UpdateFolderPeers{
			FolderPeers: tgFolderPeers(event.FolderPeers),
			Pts:         event.Pts,
			PtsCount:    event.PtsCount,
		}
	case domain.UpdateEventChannelAvailable:
		if event.Peer.Type != domain.PeerTypeChannel || event.Peer.ID == 0 || event.MaxID <= 0 {
			return nil
		}
		return &tg.UpdateChannelAvailableMessages{
			ChannelID:      event.Peer.ID,
			AvailableMinID: event.MaxID,
		}
	default:
		return nil
	}
}

func addChannels(out *tg.UpdatesDifference, seen map[int64]struct{}, viewerUserID int64, channels []domain.Channel) {
	for _, ch := range channels {
		if ch.ID == 0 {
			continue
		}
		if _, ok := seen[ch.ID]; ok {
			continue
		}
		seen[ch.ID] = struct{}{}
		out.Chats = append(out.Chats, tgChannelChatMin(viewerUserID, ch))
	}
}

func addChannelNudgeChat(out *tg.UpdatesDifference, seen map[int64]struct{}, chat tg.ChatClass) {
	channelID, ok := channelChatID(chat)
	if !ok || channelID == 0 {
		return
	}
	if _, exists := seen[channelID]; exists {
		upgradeMinChannelChat(out, channelID, chat)
		return
	}
	seen[channelID] = struct{}{}
	out.Chats = append(out.Chats, chat)
}

func upgradeMinChannelChat(out *tg.UpdatesDifference, channelID int64, candidate tg.ChatClass) {
	next, ok := candidate.(*tg.Channel)
	if !ok || next.Min {
		return
	}
	for i, existing := range out.Chats {
		existingID, ok := channelChatID(existing)
		if !ok || existingID != channelID {
			continue
		}
		if current, ok := existing.(*tg.Channel); ok && current.Min {
			out.Chats[i] = candidate
		}
		return
	}
}

func channelChatID(chat tg.ChatClass) (int64, bool) {
	switch v := chat.(type) {
	case *tg.Channel:
		return v.ID, true
	case *tg.ChannelForbidden:
		return v.ID, true
	default:
		return 0, false
	}
}

func tgChannelParticipantForUpdate(selfUserID int64, member domain.ChannelMember) tg.ChannelParticipantClass {
	if member.UserID == 0 {
		return nil
	}
	if member.Status == domain.ChannelMemberKicked || member.Status == domain.ChannelMemberBanned || member.BannedRights.ViewMessages {
		return &tg.ChannelParticipantBanned{
			Left:         member.Status != domain.ChannelMemberActive,
			Peer:         &tg.PeerUser{UserID: member.UserID},
			KickedBy:     member.InviterUserID,
			Date:         member.LeftAt,
			BannedRights: tgChatBannedRights(member.BannedRights),
		}
	}
	if member.Status == domain.ChannelMemberLeft {
		return &tg.ChannelParticipantLeft{Peer: &tg.PeerUser{UserID: member.UserID}}
	}
	return tgChannelParticipant(selfUserID, member)
}

func tgLangPackDifference(pack domain.LangPack) *tg.LangPackDifference {
	return &tg.LangPackDifference{
		LangCode:    pack.LangCode,
		FromVersion: pack.FromVersion,
		Version:     pack.Version,
		Strings:     tgLangPackStrings(pack.Strings),
	}
}
