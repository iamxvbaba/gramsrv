package rpc

import (
	"github.com/iamxvbaba/td/tg"
	"telesrv/internal/compat/tdesktop"
	"telesrv/internal/domain"
)

const defaultChatBannedRightsUntilDate = 2147483647

func tgChannelChatsWithPrimarySelf(viewerUserID int64, primary domain.Channel, extras []domain.Channel, primarySelf domain.ChannelMember) []tg.ChatClass {
	channels := make([]domain.Channel, 0, len(extras)+1)
	channels = append(channels, primary)
	channels = append(channels, extras...)
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
		if ch.ID != primary.ID {
			// Companion channels have no matching viewer membership in
			// ChannelHistory. Keep them minimal so they cannot overwrite the
			// client's cached left/creator/admin/banned state.
			out = append(out, tgChannelChatMin(viewerUserID, ch))
			continue
		}
		var self *domain.ChannelMember
		if primarySelf.ChannelID == ch.ID && primarySelf.UserID == viewerUserID {
			self = &primarySelf
		}
		out = append(out, tgChannelChat(viewerUserID, ch, self))
	}
	return out
}

func tgChannelHistoryMessages(viewerUserID int64, list domain.ChannelHistory) tg.MessagesMessagesClass {
	messages := make([]tg.MessageClass, 0, len(list.Messages))
	for _, msg := range list.Messages {
		if item := tgChannelMessage(viewerUserID, msg); item != nil {
			messages = append(messages, item)
		}
	}
	topics := make([]tg.ForumTopicClass, 0, len(list.Topics))
	for _, topic := range list.Topics {
		item := tgForumTopicFromDomain(viewerUserID, topic)
		item.SetShort(true)
		topics = append(topics, item)
	}
	// The viewer appears in history.Users when they have posted; it must go through
	// the viewer branch to get the self flag, otherwise DrKLO putUsers would overwrite
	// the account cache with self=false and degrade Saved Messages into an ordinary
	// self-chat.
	users := tgUsersForViewer(viewerUserID, list.Users)
	chats := tgChannelChatsWithPrimarySelf(viewerUserID, list.Channel, list.Channels, list.Self)
	if list.Channel.ID != 0 {
		return &tg.MessagesChannelMessages{
			Pts:      list.Channel.Pts,
			Count:    list.Count,
			Messages: messages,
			Topics:   topics,
			Chats:    chats,
			Users:    users,
		}
	}
	if list.Count > len(messages) {
		return &tg.MessagesMessagesSlice{
			Count:    list.Count,
			Messages: messages,
			Topics:   topics,
			Chats:    chats,
			Users:    users,
		}
	}
	return &tg.MessagesMessages{Messages: messages, Topics: topics, Chats: chats, Users: users}
}

func tgChannelMessage(viewerUserID int64, m domain.ChannelMessage) tg.MessageClass {
	if m.ID == 0 || m.ChannelID == 0 {
		return nil
	}
	peer := &tg.PeerChannel{ChannelID: m.ChannelID}
	outgoing := m.SenderUserID == viewerUserID && viewerUserID != 0 && (m.SavedPeer.ID != 0 || m.From.Type != domain.PeerTypeChannel)
	from := tg.PeerClass(nil)
	if (!m.Post || m.ShowMessageSender) && m.SendAs != nil && m.SendAs.ID != 0 {
		from = tgPeer(*m.SendAs)
	}
	if !m.Post && from == nil && m.From.ID != 0 {
		from = tgPeer(m.From)
	}
	if from == nil && m.SenderUserID != 0 && !m.Post {
		from = &tg.PeerUser{UserID: m.SenderUserID}
	}
	if m.Action != nil && m.Action.Type == domain.ChannelActionCreate {
		// messageActionChannelCreate is the channel-level "create" event and carries no
		// sender (matching official). Otherwise, in the monoforum admin view, this
		// creation message with from=admin would be grouped by the client into a fake
		// "admin" sub-session by sender, and opening the monoforum would land in that
		// person's profile instead of the direct-messages admin view. out is still kept
		// (triggered by the admin, = viewer).
		from = nil
	}
	if m.Action != nil {
		msg := &tg.MessageService{
			Out:    outgoing,
			Silent: m.Silent,
			Post:   m.Post,
			ID:     m.ID,
			FromID: from,
			PeerID: peer,
			Date:   m.Date,
			Action: tgChannelMessageAction(*m.Action),
		}
		if msg.Action == nil {
			msg.Action = &tg.MessageActionEmpty{}
		}
		if m.SavedPeer.ID != 0 {
			msg.SetSavedPeerID(tgPeer(m.SavedPeer))
		}
		if reply := tgMessageReplyHeader(domain.Message{
			Peer:    domain.Peer{Type: domain.PeerTypeChannel, ID: m.ChannelID},
			ReplyTo: m.ReplyTo,
		}); reply != nil {
			msg.SetReplyTo(reply)
		}
		return msg
	}
	msg := &tg.Message{
		Out:         outgoing,
		Silent:      m.Silent,
		Post:        m.Post,
		Noforwards:  m.NoForwards,
		Mentioned:   m.Mentioned,
		MediaUnread: m.MediaUnread,
		ID:          m.ID,
		FromID:      from,
		PeerID:      peer,
		Date:        m.Date,
		Message:     m.Body,
		Entities:    tgMessageEntities(m.Entities),
	}
	if m.SavedPeer.ID != 0 {
		// Channel direct messages (monoforum): saved_peer_id lets the client file the message under the corresponding subscriber sub-session.
		msg.SetSavedPeerID(tgPeer(m.SavedPeer))
	}
	if suggested, ok := tgSuggestedPost(m.SuggestedPost); ok {
		if m.Post {
			if m.SuggestedPost != nil && m.SuggestedPost.Accepted && m.SuggestedPost.Price != nil {
				switch m.SuggestedPost.Price.Kind {
				case domain.SuggestedPostPriceStars:
					msg.SetPaidSuggestedPostStars(true)
				case domain.SuggestedPostPriceTON:
					msg.SetPaidSuggestedPostTon(true)
				}
			}
		} else {
			msg.SetSuggestedPost(suggested)
		}
	}
	if m.PaidMessageStars > 0 {
		msg.SetPaidMessageStars(m.PaidMessageStars)
	}
	// The Layer 231 fact-check label is sent with the message; when need_check is set
	// it carries only the hash, and the client lazily loads the conclusion text via
	// messages.getFactCheck when scrolling to the message.
	if factCheck := tgFactCheck(m.FactCheck); factCheck != nil {
		msg.SetFactcheck(*factCheck)
	}
	if m.Pinned {
		msg.SetPinned(true)
	}
	if m.EditDate != 0 {
		msg.SetEditDate(m.EditDate)
	}
	if reply := tgMessageReplyHeader(domain.Message{
		Peer:    domain.Peer{Type: domain.PeerTypeChannel, ID: m.ChannelID},
		ReplyTo: m.ReplyTo,
	}); reply != nil {
		msg.SetReplyTo(reply)
	}
	if fwd := tgMessageFwdHeader(m.Forward); fwd != nil {
		msg.SetFwdFrom(*fwd)
	}
	if m.ViaBotID != 0 {
		msg.SetViaBotID(m.ViaBotID)
	}
	if m.GroupedID != 0 {
		msg.SetGroupedID(m.GroupedID)
	}
	if markup := tgReplyMarkup(m.ReplyMarkup); markup != nil {
		msg.SetReplyMarkup(markup)
	}
	if rich := optionalTGRichMessage("channel_message", m.ID, m.RichMessage); rich != nil {
		msg.SetRichMessage(*rich)
	}
	if replies := tgChannelMessageReplies(m.Replies); replies != nil {
		msg.SetReplies(*replies)
	}
	if reactions := tgMessageReactions(viewerUserID, m.Reactions); reactions != nil {
		msg.SetReactions(*reactions)
	}
	if !m.Media.IsZero() {
		msg.SetMedia(tgMessageMedia(m.Media))
		if m.Media.InvertMedia {
			msg.SetInvertMedia(true)
		}
	}
	if m.TTLPeriod > 0 {
		msg.SetTTLPeriod(m.TTLPeriod)
	}
	if m.FromBoostsApplied > 0 {
		msg.SetFromBoostsApplied(m.FromBoostsApplied)
	}
	if m.Post {
		// An official channel post carries a views counter (initial 1); when signatures are enabled, the author signature is attached.
		views := m.ViewsCount
		if views < 1 {
			views = 1
		}
		msg.SetViews(views)
		if m.PostAuthor != "" {
			msg.SetPostAuthor(m.PostAuthor)
		}
	}
	return msg
}

func tgChannelMessageAction(action domain.ChannelMessageAction) tg.MessageActionClass {
	switch action.Type {
	case domain.ChannelActionCreate:
		return &tg.MessageActionChannelCreate{Title: action.Title}
	case domain.ChannelActionHistoryClear:
		return &tg.MessageActionHistoryClear{}
	case domain.ChannelActionChatAddUser, domain.ChannelActionChatJoined:
		return &tg.MessageActionChatAddUser{Users: append([]int64(nil), action.UserIDs...)}
	case domain.ChannelActionChatJoinedByLink:
		return &tg.MessageActionChatJoinedByLink{InviterID: action.InviterUserID}
	case domain.ChannelActionChatDelete:
		var userID int64
		if len(action.UserIDs) > 0 {
			userID = action.UserIDs[0]
		}
		return &tg.MessageActionChatDeleteUser{UserID: userID}
	case domain.ChannelActionChatEditPhoto:
		if action.Photo == nil {
			return nil
		}
		photo := tgPhoto(*action.Photo)
		if _, empty := photo.(*tg.PhotoEmpty); empty {
			return nil
		}
		return &tg.MessageActionChatEditPhoto{Photo: photo}
	case domain.ChannelActionChatDeletePhoto:
		return &tg.MessageActionChatDeletePhoto{}
	case domain.ChannelActionEditTitle:
		return &tg.MessageActionChatEditTitle{Title: action.Title}
	case domain.ChannelActionTopicCreate:
		return &tg.MessageActionTopicCreate{
			TitleMissing: action.TitleMissing,
			Title:        action.Title,
			IconColor:    action.IconColor,
			IconEmojiID:  action.IconEmojiID,
		}
	case domain.ChannelActionTopicEdit:
		out := &tg.MessageActionTopicEdit{}
		if action.Title != "" {
			out.SetTitle(action.Title)
		}
		if action.IconEmojiIDSet {
			out.SetIconEmojiID(action.IconEmojiID)
		}
		if action.Closed != nil {
			out.SetClosed(*action.Closed)
		}
		if action.Hidden != nil {
			out.SetHidden(*action.Hidden)
		}
		return out
	case domain.ChannelActionTodoCompletions:
		return &tg.MessageActionTodoCompletions{
			Completed:   append([]int(nil), action.Completed...),
			Incompleted: append([]int(nil), action.Incompleted...),
		}
	case domain.ChannelActionTodoAppendTasks:
		return &tg.MessageActionTodoAppendTasks{
			List: tgTodoItems(action.TodoItems),
		}
	case domain.ChannelActionGroupCall:
		// started (no duration) and ended (with duration) share messageActionGroupCall.
		out := &tg.MessageActionGroupCall{
			Call: &tg.InputGroupCall{ID: action.CallID, AccessHash: action.CallAccessHash},
		}
		if action.CallDuration > 0 {
			out.SetDuration(action.CallDuration)
		}
		return out
	case domain.ChannelActionGroupCallScheduled:
		return &tg.MessageActionGroupCallScheduled{
			Call:         &tg.InputGroupCall{ID: action.CallID, AccessHash: action.CallAccessHash},
			ScheduleDate: action.CallScheduleDate,
		}
	case domain.ChannelActionInviteToGroupCall:
		return &tg.MessageActionInviteToGroupCall{
			Call:  &tg.InputGroupCall{ID: action.CallID, AccessHash: action.CallAccessHash},
			Users: append([]int64(nil), action.UserIDs...),
		}
	case domain.ChannelActionBoostApply:
		return &tg.MessageActionBoostApply{Boosts: action.Boosts}
	case domain.ChannelActionPaidMessagesPrice:
		return &tg.MessageActionPaidMessagesPrice{
			BroadcastMessagesAllowed: action.BroadcastMessagesAllowed,
			Stars:                    action.Stars,
		}
	case domain.ChannelActionStarGift:
		return tgMessageActionStarGift(action.StarGift)
	case domain.ChannelActionStarGiftUnique:
		return tgMessageActionStarGiftUnique(action.StarGiftUnique)
	case domain.ChannelActionSetChatWallpaper:
		if wallpaper := tgWallpaper(action.Wallpaper); wallpaper != nil {
			return &tg.MessageActionSetChatWallPaper{Wallpaper: wallpaper}
		}
		return nil
	case domain.ChannelActionChangeCommunity:
		out := &tg.MessageActionChangeCommunity{}
		if action.CommunityID != 0 {
			out.SetCommunityID(action.CommunityID)
		}
		return out
	case domain.ChannelActionSuggestedPostApproval:
		out := &tg.MessageActionSuggestedPostApproval{
			Rejected:      action.SuggestedPostRejected,
			BalanceTooLow: action.SuggestedPostBalanceTooLow,
		}
		if action.SuggestedPostRejectComment != "" {
			out.SetRejectComment(action.SuggestedPostRejectComment)
		}
		if action.SuggestedPostScheduleDate > 0 {
			out.SetScheduleDate(action.SuggestedPostScheduleDate)
		}
		if price := tgSuggestedPostPrice(action.SuggestedPostPrice); price != nil {
			out.SetPrice(price)
		}
		return out
	case domain.ChannelActionSuggestedPostSuccess:
		if price := tgSuggestedPostPrice(action.SuggestedPostPrice); price != nil {
			return &tg.MessageActionSuggestedPostSuccess{Price: price}
		}
		return nil
	case domain.ChannelActionSuggestedPostRefund:
		return &tg.MessageActionSuggestedPostRefund{PayerInitiated: action.SuggestedPostPayerInitiated}
	default:
		return nil
	}
}

func tgSuggestedPostPrice(price *domain.SuggestedPostPrice) tg.StarsAmountClass {
	if price == nil {
		return nil
	}
	switch price.Kind {
	case domain.SuggestedPostPriceStars:
		return &tg.StarsAmount{Amount: price.Amount, Nanos: price.Nanos}
	case domain.SuggestedPostPriceTON:
		return &tg.StarsTonAmount{Amount: price.Amount}
	default:
		return nil
	}
}

func tgChannelMessageReplies(in *domain.ChannelMessageReplies) *tg.MessageReplies {
	if in == nil {
		return nil
	}
	out := &tg.MessageReplies{
		Comments:   in.Comments,
		Replies:    in.Replies,
		RepliesPts: in.RepliesPts,
	}
	if len(in.RecentRepliers) > 0 {
		repliers := make([]tg.PeerClass, 0, len(in.RecentRepliers))
		for _, peer := range in.RecentRepliers {
			if converted := tgPeer(peer); converted != nil {
				repliers = append(repliers, converted)
			}
		}
		if len(repliers) > 0 {
			out.SetRecentRepliers(repliers)
		}
	}
	if in.ChannelID != 0 {
		out.SetChannelID(in.ChannelID)
	}
	if in.MaxID > 0 {
		out.SetMaxID(in.MaxID)
	}
	if in.ReadMaxID > 0 {
		out.SetReadMaxID(in.ReadMaxID)
	}
	return out
}

func tgChannels(viewerUserID int64, channels []domain.Channel) []tg.ChatClass {
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
		out = append(out, tgChannelChatMin(viewerUserID, ch))
	}
	return out
}

func tgChannelChat(viewerUserID int64, ch domain.Channel, self *domain.ChannelMember) tg.ChatClass {
	if ch.Deleted {
		return tgChannelForbidden(ch)
	}
	return tgChannel(viewerUserID, ch, self)
}

// tgChannelChatMin is the min channel shape for message-class update/accompanying
// chats: the client does not apply admin/banned rights or left/creator to a min
// object, avoiding a push with no recipient member projection wiping local
// permission state.
func tgChannelChatMin(viewerUserID int64, ch domain.Channel) tg.ChatClass {
	if ch.Deleted {
		return tgChannelForbidden(ch)
	}
	out := tgChannel(viewerUserID, ch, nil)
	out.Min = true
	out.Creator = false
	return out
}

// tgChannelChatForView converts a viewer-projected ChannelView into a query response
// chat: a kicked/view-banned viewer receives channelForbidden, learning they have
// left the conversation.
func tgChannelChatForView(viewerUserID int64, view domain.ChannelView) tg.ChatClass {
	if view.Forbidden {
		return tgChannelForbidden(view.Channel)
	}
	self := view.Self
	return tgChannelChat(viewerUserID, view.Channel, &self)
}

func tgChannelForbidden(ch domain.Channel) *tg.ChannelForbidden {
	return &tg.ChannelForbidden{
		Broadcast:  ch.Broadcast,
		Megagroup:  ch.Megagroup,
		ID:         ch.ID,
		AccessHash: ch.AccessHash,
		Title:      ch.Title,
	}
}

func tgChannel(viewerUserID int64, ch domain.Channel, self *domain.ChannelMember) *tg.Channel {
	out := &tg.Channel{
		Creator:           ch.CreatorUserID == viewerUserID && viewerUserID != 0,
		Verified:          ch.Verified,
		Scam:              ch.Scam,
		Fake:              ch.Fake,
		Gigagroup:         ch.Gigagroup,
		Broadcast:         ch.Broadcast,
		Megagroup:         ch.Megagroup,
		Forum:             ch.Forum,
		ForumTabs:         ch.ForumTabs,
		Noforwards:        ch.NoForwards,
		Signatures:        ch.Signatures,
		SignatureProfiles: ch.SignatureProfiles,
		ID:                ch.ID,
		Title:             ch.Title,
		Photo:             tgChatPhoto(ch),
		Date:              ch.Date,
	}
	out.SetAccessHash(ch.AccessHash)
	out.SetJoinToSend(ch.JoinToSend)
	out.SetJoinRequest(ch.JoinRequest)
	out.SetAutotranslation(ch.Autotranslation)
	// broadcast_messages_allowed belongs only to the parent broadcast channel. A
	// monoforum mirrors the DM enabled state internally via a same-named field (see
	// below), but it is a megagroup, so that flag must never be projected (the
	// official monoforum object does not carry it either).
	if ch.Broadcast {
		out.SetBroadcastMessagesAllowed(ch.BroadcastMessagesAllowed)
	}
	if ch.SendPaidMessagesStars > 0 || ch.BroadcastMessagesAllowed || ch.Monoforum {
		out.SetSendPaidMessagesStars(ch.SendPaidMessagesStars)
	}
	if ch.HasLink || ch.LinkedChatID != 0 {
		out.SetHasLink(true)
	}
	if ch.Monoforum {
		out.SetMonoforum(true)
	}
	// linked_monoforum_id is sent only when DM is enabled (both the parent channel and
	// the monoforum represent DM-enabled state via BroadcastMessagesAllowed, and the
	// monoforum mirrors the parent channel via SetPaidMessagesPrice). When Direct
	// Messages is disabled, **both sides** hide the id: hiding it on the parent
	// triggers the monoforum's MonoforumDisabled; the monoforum must hide it too,
	// otherwise opening the monoforum re-fetches the mono still carrying the link →
	// setMonoforumLink(parent) clears MonoforumDisabled and the disabled footer
	// disappears. The internal association row is still kept so it can be reused when
	// re-enabled.
	if ch.LinkedMonoforumID != 0 && ch.BroadcastMessagesAllowed {
		out.SetLinkedMonoforumID(ch.LinkedMonoforumID)
	}
	if ch.LinkedCommunityID != 0 {
		out.SetLinkedCommunityID(ch.LinkedCommunityID)
	}
	if ch.Username != "" {
		out.SetUsername(ch.Username)
	}
	if color := tgPeerColor(ch.Color); color != nil {
		out.SetColor(color)
	}
	if profileColor := tgPeerColor(ch.ProfileColor); profileColor != nil {
		out.SetProfileColor(profileColor)
	}
	if status := tgChannelEmojiStatus(ch.EmojiStatus); status != nil {
		out.SetEmojiStatus(status)
	}
	if ch.SlowmodeSeconds > 0 {
		out.SetSlowmodeEnabled(true)
	}
	if ch.ParticipantsCount > 0 {
		out.SetParticipantsCount(ch.ParticipantsCount)
	}
	out.SetDefaultBannedRights(tgDefaultChatBannedRights(ch.DefaultBannedRights))
	// Group call banner data source: the call_active/call_not_empty flags (Android
	// depends more heavily on the flags, and when call_not_empty flips it also
	// re-pushes via pushChannelStateToMembers).
	out.CallActive = ch.ActiveCallID != 0
	out.CallNotEmpty = ch.ActiveCallNotEmpty
	if ch.Monoforum {
		// A Monoforum (channel direct-messages container) must never carry
		// Creator/admin_rights on its own object. TDesktop's NeedAboutGroup checks
		// amCreator() for a megagroup to decide whether to paint the "You created a
		// group" group empty state, and subscriber-count/Leave/Channel chrome also keys
		// off amCreator/asMegagroup -- once the mono itself has Creator=true it would be
		// rendered as a normal group. The Direct-Messages container identity (the local
		// MonoforumAdmin flag) is derived client-side from the parent channel's
		// canAccessMonoforum (amCreator or manage_direct_messages), independent of the
		// mono's own Creator/admin. Server-side DM send authorization goes through the
		// parent channel membership and does not depend on this flag.
		out.Creator = false
	} else if self != nil {
		if self.Status == domain.ChannelMemberLeft || self.Guest {
			out.Left = true
		}
		switch self.Role {
		case domain.ChannelRoleCreator:
			out.Creator = true
			out.SetAdminRights(tgChatAdminRights(creatorProjectionAdminRights(self.AdminRights)))
		case domain.ChannelRoleAdmin:
			out.SetAdminRights(tgChatAdminRights(self.AdminRights))
		}
		if !zeroChannelBannedRights(self.BannedRights) {
			out.SetBannedRights(tgChatBannedRights(self.BannedRights))
		}
	}
	return out
}

func tgChannelFull(view domain.ChannelView, publicBaseURL ...string) *tg.ChannelFull {
	ch := view.Channel
	about := ch.About
	// Editors receive the stored text used by the client description editor.
	// Other viewers receive the warning without changing any write boundary.
	if !channelMemberCanChangeInfo(view.Self) {
		about = domain.ModerationProfileWarning(ch.Scam, ch.Fake, about)
	}
	full := &tg.ChannelFull{
		// A broadcast channel's subscriber list is visible only to admins (official
		// semantics): a non-admin subscriber gets can_view_participants=false, and the
		// Profile rows Subscribers/Administrators/Channel Settings are hidden
		// accordingly. Visible to megagroup members (only admins when members are
		// hidden).
		CanViewParticipants: channelMemberIsAdmin(view.Self) || !ch.MembersListAdminOnly(),
		CanSetUsername:      view.Self.Role == domain.ChannelRoleCreator,
		CanDeleteChannel:    view.Self.Role == domain.ChannelRoleCreator,
		// TDesktop only exposes the Statistics entry after channelFull.can_view_stats.
		// The stats RPCs enforce the same creator/admin boundary, so project the
		// capability from the membership instead of leaving a reachable service
		// hidden behind a permanently false wire flag. Monoforum is an internal
		// direct-message container and has no independent statistics surface.
		CanViewStats: !ch.Monoforum && channelMemberIsAdmin(view.Self),
		ID:           ch.ID,
		// Official clients render localized warnings from scam/fake flags.
		// On top of that badge the server prepends its own warning line to
		// About (operator-configured via TELESRV_SCAM_WARNING /
		// TELESRV_FAKE_WARNING), because the badge alone is easy to miss. The
		// stored description is never rewritten: the warning exists only in
		// this projection (domain.ModerationProfileWarning).
		About:           about,
		ReadInboxMaxID:  view.Dialog.ReadInboxMaxID,
		ReadOutboxMaxID: view.Dialog.ReadOutboxMaxID,
		UnreadCount:     view.Dialog.UnreadCount,
		ChatPhoto:       tgChannelChatPhotoFull(ch),
		NotifySettings:  *tdesktop.NotifySettings(),
		Pts:             ch.Pts,
	}
	if ch.ParticipantsCount > 0 {
		full.SetParticipantsCount(ch.ParticipantsCount)
	}
	if ch.ActiveCallID != 0 {
		// The entry point for the join panel/banner click: the client uses it to call phone.getGroupCall.
		full.SetCall(&tg.InputGroupCall{ID: ch.ActiveCallID, AccessHash: ch.ActiveCallAccessHash})
	}
	if view.Self.AvailableMinID > 0 {
		// The client uses it to trim locally cached pre-join/cleared history (clearUpTill).
		full.SetAvailableMinID(view.Self.AvailableMinID)
	}
	if view.ExportedInvite != nil && !view.ExportedInvite.Revoked {
		baseURL := ""
		if len(publicBaseURL) > 0 {
			baseURL = publicBaseURL[0]
		}
		full.SetExportedInvite(tgExportedChannelInvite(*view.ExportedInvite, baseURL))
	}
	if ch.AdminsCount > 0 {
		full.SetAdminsCount(ch.AdminsCount)
	}
	if ch.KickedCount > 0 {
		full.SetKickedCount(ch.KickedCount)
	}
	if ch.BannedCount > 0 {
		full.SetBannedCount(ch.BannedCount)
	}
	if view.Dialog.FolderID != domain.DialogMainFolderID {
		full.SetFolderID(view.Dialog.FolderID)
	}
	if ch.TTLPeriod > 0 {
		full.SetTTLPeriod(ch.TTLPeriod)
	}
	if view.Dialog.HasScheduled {
		full.SetHasScheduled(true)
	}
	if ch.PreHistoryHidden {
		full.SetHiddenPrehistory(true)
	}
	if ch.ParticipantsHidden {
		full.SetParticipantsHidden(true)
	}
	if ch.AntiSpam {
		full.SetAntispam(true)
	}
	if ch.RestrictedSponsored {
		full.SetRestrictedSponsored(true)
	}
	if ch.SendPaidMessagesStars > 0 || ch.BroadcastMessagesAllowed {
		full.SetPaidMessagesAvailable(true)
		full.SetSendPaidMessagesStars(ch.SendPaidMessagesStars)
	}
	if view.Dialog.ViewForumAsMessages {
		full.SetViewForumAsMessages(true)
	}
	// linked_chat_id is sent only when the discussion channel is actually usable
	// (exists, not deleted, is a megagroup). A dangling id would make the client
	// render a "Discussion" entry that opens an empty channel; the validation result
	// is given by the store on ChannelView.
	if ch.LinkedChatID != 0 && view.LinkedChatUsable {
		full.SetLinkedChatID(ch.LinkedChatID)
	}
	if defaultSendAs := validDefaultSendAsPeer(view); defaultSendAs != nil {
		full.SetDefaultSendAs(tgPeer(*defaultSendAs))
	}
	if ch.SlowmodeSeconds > 0 {
		full.SetSlowmodeSeconds(ch.SlowmodeSeconds)
		if view.Self.SlowmodeLastSendDate > 0 {
			full.SetSlowmodeNextSendDate(view.Self.SlowmodeLastSendDate + ch.SlowmodeSeconds)
		}
	}
	if view.SelfBoostsApplied > 0 {
		full.SetBoostsApplied(view.SelfBoostsApplied)
	}
	if ch.BoostsUnrestrict > 0 {
		full.SetBoostsUnrestrict(ch.BoostsUnrestrict)
	}
	if ch.PinnedMessageID > 0 {
		full.SetPinnedMsgID(ch.PinnedMessageID)
	}
	if wallpaper := tgWallpaper(ch.Wallpaper); wallpaper != nil {
		full.SetWallpaper(wallpaper)
	}
	if reactions := tgChannelReactionPolicy(ch.ReactionPolicy); reactions != nil {
		full.SetAvailableReactions(reactions)
	}
	if ch.ReactionPolicy.Limit > 0 {
		full.SetReactionsLimit(ch.ReactionPolicy.Limit)
	}
	if ch.Broadcast && !ch.Megagroup {
		// Android uses paid_media_allowed as the capability gate for showing the
		// paid-reaction setting; paid_reactions_available below remains the saved
		// on/off state.
		full.SetPaidMediaAllowed(true)
		full.SetStargiftsAvailable(true)
	}
	// paid_reactions_available reflects the saved chat policy, not mere broadcast
	// capability. Android counts this flag as an extra available reaction in the
	// settings row, so advertising it without paid_enabled corrupts the UI count.
	if ch.Broadcast && !ch.Megagroup && ch.ReactionPolicy.PaidEnabled {
		full.SetPaidReactionsAvailable(true)
	}
	return full
}

// applyChannelStatsCapability completes the config-dependent half of the
// channelFull statistics capability. TDLib deliberately clears
// can_view_stats when stats_dc is absent or invalid, so these fields must be
// projected as one invariant rather than as independent optional hints.
// A manually constructed zero-value Router config is treated as unavailable;
// production config validation requires a positive canonical DC.
func (r *Router) applyChannelStatsCapability(full *tg.ChannelFull) {
	if full == nil || !full.CanViewStats {
		return
	}
	if r.cfg.DC <= 0 {
		full.CanViewStats = false
		return
	}
	full.SetStatsDC(r.cfg.DC)
}

func channelMemberIsAdmin(member domain.ChannelMember) bool {
	return member.Role == domain.ChannelRoleCreator || member.Role == domain.ChannelRoleAdmin
}

func tgChannelReactionPolicy(policy domain.ChannelReactionPolicy) tg.ChatReactionsClass {
	switch policy.Type {
	case domain.ChannelReactionPolicyNone:
		return &tg.ChatReactionsNone{}
	case domain.ChannelReactionPolicyAll:
		out := &tg.ChatReactionsAll{}
		if policy.AllowCustom {
			out.SetAllowCustom(true)
		}
		return out
	case domain.ChannelReactionPolicySome:
		reactions := make([]tg.ReactionClass, 0, len(policy.Emoticons)+len(policy.CustomEmojiIDs))
		for _, emoticon := range policy.Emoticons {
			if emoticon == "" {
				continue
			}
			reactions = append(reactions, &tg.ReactionEmoji{Emoticon: emoticon})
		}
		for _, id := range policy.CustomEmojiIDs {
			if id <= 0 {
				continue
			}
			reactions = append(reactions, &tg.ReactionCustomEmoji{DocumentID: id})
		}
		return &tg.ChatReactionsSome{Reactions: reactions}
	default:
		return nil
	}
}

func tgPeerColor(color domain.ChannelPeerColor) tg.PeerColorClass {
	if color.Empty() {
		return nil
	}
	out := &tg.PeerColor{}
	if color.HasColor {
		out.SetColor(color.Color)
	}
	if color.BackgroundEmojiID != 0 {
		out.SetBackgroundEmojiID(color.BackgroundEmojiID)
	}
	return out
}

func tgChannelParticipant(selfUserID int64, member domain.ChannelMember) tg.ChannelParticipantClass {
	switch member.Role {
	case domain.ChannelRoleCreator:
		out := &tg.ChannelParticipantCreator{
			UserID:      member.UserID,
			AdminRights: tgChatAdminRights(creatorProjectionAdminRights(member.AdminRights)),
		}
		if member.Rank != "" {
			out.SetRank(member.Rank)
		}
		return out
	case domain.ChannelRoleAdmin:
		out := &tg.ChannelParticipantAdmin{
			Self:        member.UserID == selfUserID,
			UserID:      member.UserID,
			PromotedBy:  member.InviterUserID,
			Date:        member.JoinedAt,
			AdminRights: tgChatAdminRights(member.AdminRights),
		}
		if member.InviterUserID != 0 {
			out.SetInviterID(member.InviterUserID)
		}
		if member.Rank != "" {
			out.SetRank(member.Rank)
		}
		return out
	default:
		if member.Status != domain.ChannelMemberActive {
			return &tg.ChannelParticipantLeft{Peer: &tg.PeerUser{UserID: member.UserID}}
		}
		if member.UserID == selfUserID {
			out := &tg.ChannelParticipantSelf{
				UserID:    member.UserID,
				InviterID: member.InviterUserID,
				Date:      member.JoinedAt,
			}
			if member.Rank != "" {
				out.SetRank(member.Rank)
			}
			return out
		}
		out := &tg.ChannelParticipant{UserID: member.UserID, Date: member.JoinedAt}
		if member.Rank != "" {
			out.SetRank(member.Rank)
		}
		return out
	}
}

func tgChannelAdminLogEvents(viewerUserID int64, events []domain.ChannelAdminLogEvent) []tg.ChannelAdminLogEvent {
	out := make([]tg.ChannelAdminLogEvent, 0, len(events))
	for _, event := range events {
		if item, ok := tgChannelAdminLogEvent(viewerUserID, event); ok {
			out = append(out, item)
		}
	}
	return out
}

func tgChannelAdminLogEvent(viewerUserID int64, event domain.ChannelAdminLogEvent) (tg.ChannelAdminLogEvent, bool) {
	action := tg.ChannelAdminLogEventActionClass(nil)
	switch event.Type {
	case domain.ChannelAdminLogChangeTitle:
		action = &tg.ChannelAdminLogEventActionChangeTitle{PrevValue: event.PrevString, NewValue: event.NewString}
	case domain.ChannelAdminLogChangeUsername:
		action = &tg.ChannelAdminLogEventActionChangeUsername{PrevValue: event.PrevString, NewValue: event.NewString}
	case domain.ChannelAdminLogChangeLinkedChat:
		action = &tg.ChannelAdminLogEventActionChangeLinkedChat{PrevValue: int64(event.PrevInt), NewValue: int64(event.NewInt)}
	case domain.ChannelAdminLogToggleSignatureProfiles:
		action = &tg.ChannelAdminLogEventActionToggleSignatureProfiles{NewValue: event.NewBool}
	case domain.ChannelAdminLogToggleSignatures:
		action = &tg.ChannelAdminLogEventActionToggleSignatures{NewValue: event.NewBool}
	case domain.ChannelAdminLogTogglePreHistoryHidden:
		action = &tg.ChannelAdminLogEventActionTogglePreHistoryHidden{NewValue: event.NewBool}
	case domain.ChannelAdminLogToggleForum:
		action = &tg.ChannelAdminLogEventActionToggleForum{NewValue: event.NewBool}
	case domain.ChannelAdminLogToggleAutotranslation:
		action = &tg.ChannelAdminLogEventActionToggleAutotranslation{NewValue: event.NewBool}
	case domain.ChannelAdminLogToggleAntiSpam:
		action = &tg.ChannelAdminLogEventActionToggleAntiSpam{NewValue: event.NewBool}
	case domain.ChannelAdminLogToggleSlowMode:
		action = &tg.ChannelAdminLogEventActionToggleSlowMode{PrevValue: event.PrevInt, NewValue: event.NewInt}
	case domain.ChannelAdminLogParticipantJoin:
		action = &tg.ChannelAdminLogEventActionParticipantJoin{}
	case domain.ChannelAdminLogParticipantLeave:
		action = &tg.ChannelAdminLogEventActionParticipantLeave{}
	case domain.ChannelAdminLogParticipantInvite:
		if event.Participant == nil {
			return tg.ChannelAdminLogEvent{}, false
		}
		action = &tg.ChannelAdminLogEventActionParticipantInvite{
			Participant: tgChannelParticipantForUpdate(viewerUserID, *event.Participant),
		}
	case domain.ChannelAdminLogParticipantPromote, domain.ChannelAdminLogParticipantDemote:
		if event.PrevParticipant == nil || event.NewParticipant == nil {
			return tg.ChannelAdminLogEvent{}, false
		}
		action = &tg.ChannelAdminLogEventActionParticipantToggleAdmin{
			PrevParticipant: tgChannelParticipantForUpdate(viewerUserID, *event.PrevParticipant),
			NewParticipant:  tgChannelParticipantForUpdate(viewerUserID, *event.NewParticipant),
		}
	case domain.ChannelAdminLogParticipantEditRank:
		if event.Participant == nil {
			return tg.ChannelAdminLogEvent{}, false
		}
		action = &tg.ChannelAdminLogEventActionParticipantEditRank{
			UserID:   event.Participant.UserID,
			PrevRank: event.PrevString,
			NewRank:  event.NewString,
		}
	case domain.ChannelAdminLogParticipantBan, domain.ChannelAdminLogParticipantUnban, domain.ChannelAdminLogParticipantKick, domain.ChannelAdminLogParticipantUnkick:
		if event.PrevParticipant == nil || event.NewParticipant == nil {
			return tg.ChannelAdminLogEvent{}, false
		}
		action = &tg.ChannelAdminLogEventActionParticipantToggleBan{
			PrevParticipant: tgChannelParticipantForUpdate(viewerUserID, *event.PrevParticipant),
			NewParticipant:  tgChannelParticipantForUpdate(viewerUserID, *event.NewParticipant),
		}
	case domain.ChannelAdminLogUpdatePinned:
		action = &tg.ChannelAdminLogEventActionUpdatePinned{Message: tgAdminLogMessage(viewerUserID, event.ChannelID, event.Message)}
	case domain.ChannelAdminLogSendMessage:
		action = &tg.ChannelAdminLogEventActionSendMessage{Message: tgAdminLogMessage(viewerUserID, event.ChannelID, event.Message)}
	case domain.ChannelAdminLogEditMessage:
		action = &tg.ChannelAdminLogEventActionEditMessage{
			PrevMessage: tgAdminLogMessage(viewerUserID, event.ChannelID, event.PrevMessage),
			NewMessage:  tgAdminLogMessage(viewerUserID, event.ChannelID, event.NewMessage),
		}
	case domain.ChannelAdminLogDeleteMessage:
		action = &tg.ChannelAdminLogEventActionDeleteMessage{Message: tgAdminLogMessage(viewerUserID, event.ChannelID, event.Message)}
	default:
		return tg.ChannelAdminLogEvent{}, false
	}
	if action == nil {
		return tg.ChannelAdminLogEvent{}, false
	}
	return tg.ChannelAdminLogEvent{
		ID:     event.ID,
		Date:   event.Date,
		UserID: event.UserID,
		Action: action,
	}, true
}

func tgAdminLogMessage(viewerUserID, channelID int64, msg *domain.ChannelMessage) tg.MessageClass {
	if msg == nil || msg.ID == 0 {
		out := &tg.MessageEmpty{ID: 0}
		out.SetPeerID(&tg.PeerChannel{ChannelID: channelID})
		return out
	}
	return tgChannelMessage(viewerUserID, *msg)
}

func tgChatAdminRights(rights domain.ChannelAdminRights) tg.ChatAdminRights {
	return tg.ChatAdminRights{
		ChangeInfo:            rights.ChangeInfo,
		PostMessages:          rights.PostMessages,
		EditMessages:          rights.EditMessages,
		DeleteMessages:        rights.DeleteMessages,
		PostStories:           rights.PostStories,
		EditStories:           rights.EditStories,
		DeleteStories:         rights.DeleteStories,
		BanUsers:              rights.BanUsers,
		InviteUsers:           rights.InviteUsers,
		PinMessages:           rights.PinMessages,
		AddAdmins:             rights.AddAdmins,
		Anonymous:             rights.Anonymous,
		ManageCall:            rights.ManageCall,
		Other:                 true,
		ManageTopics:          rights.ManageTopics,
		ManageRanks:           rights.ManageRanks,
		ManageLinkedPeers:     rights.ManageLinkedPeers,
		ManageWelcomeMessages: rights.ManageWelcomeMessages,
		// manage_direct_messages(flags.17): the client uses this on the parent channel
		// to decide canAccessMonoforum, thereby deriving MonoforumAdmin for the linked
		// monoforum (needed for Direct-Messages container rendering).
		ManageDirectMessages: rights.ManageDirectMessages,
	}
}

func creatorProjectionAdminRights(rights domain.ChannelAdminRights) domain.ChannelAdminRights {
	return domain.CreatorProjectionAdminRights(rights)
}

func domainChannelAdminRights(rights tg.ChatAdminRights) domain.ChannelAdminRights {
	return domain.ChannelAdminRights{
		ChangeInfo:            rights.ChangeInfo,
		PostMessages:          rights.PostMessages,
		EditMessages:          rights.EditMessages,
		DeleteMessages:        rights.DeleteMessages,
		PostStories:           rights.PostStories,
		EditStories:           rights.EditStories,
		DeleteStories:         rights.DeleteStories,
		BanUsers:              rights.BanUsers,
		InviteUsers:           rights.InviteUsers,
		PinMessages:           rights.PinMessages,
		AddAdmins:             rights.AddAdmins,
		Anonymous:             rights.Anonymous,
		ManageCall:            rights.ManageCall,
		ManageChat:            rights.Other,
		ManageTopics:          rights.ManageTopics,
		ManageRanks:           rights.ManageRanks,
		ManageLinkedPeers:     rights.ManageLinkedPeers,
		ManageWelcomeMessages: rights.ManageWelcomeMessages,
		ManageDirectMessages:  rights.ManageDirectMessages,
	}
}

func tgChatBannedRights(rights domain.ChannelBannedRights) tg.ChatBannedRights {
	return tg.ChatBannedRights{
		ViewMessages:      rights.ViewMessages,
		SendMessages:      rights.SendMessages,
		SendMedia:         rights.SendMedia,
		SendStickers:      rights.SendStickers,
		SendGifs:          rights.SendGifs,
		SendGames:         rights.SendGames,
		SendInline:        rights.SendInline,
		EmbedLinks:        rights.EmbedLinks,
		SendPolls:         rights.SendPolls,
		ChangeInfo:        rights.ChangeInfo,
		InviteUsers:       rights.InviteUsers,
		PinMessages:       rights.PinMessages,
		ManageTopics:      rights.ManageTopics,
		SendPhotos:        rights.SendPhotos,
		SendVideos:        rights.SendVideos,
		SendRoundvideos:   rights.SendRoundvideos,
		SendAudios:        rights.SendAudios,
		SendVoices:        rights.SendVoices,
		SendDocs:          rights.SendDocs,
		SendPlain:         rights.SendPlain,
		EditRank:          rights.EditRank,
		SendReactions:     rights.SendReactions,
		ManageLinkedPeers: rights.ManageLinkedPeers,
		UntilDate:         rights.UntilDate,
	}
}

func tgDefaultChatBannedRights(rights domain.ChannelBannedRights) tg.ChatBannedRights {
	out := tgChatBannedRights(rights)
	if out.UntilDate == 0 {
		out.UntilDate = defaultChatBannedRightsUntilDate
	}
	return out
}

func domainChannelBannedRights(rights tg.ChatBannedRights) domain.ChannelBannedRights {
	return domain.ChannelBannedRights{
		ViewMessages:      rights.ViewMessages,
		SendMessages:      rights.SendMessages,
		SendMedia:         rights.SendMedia,
		SendStickers:      rights.SendStickers,
		SendGifs:          rights.SendGifs,
		SendGames:         rights.SendGames,
		SendInline:        rights.SendInline,
		EmbedLinks:        rights.EmbedLinks,
		SendPolls:         rights.SendPolls,
		ChangeInfo:        rights.ChangeInfo,
		InviteUsers:       rights.InviteUsers,
		PinMessages:       rights.PinMessages,
		ManageTopics:      rights.ManageTopics,
		SendPhotos:        rights.SendPhotos,
		SendVideos:        rights.SendVideos,
		SendRoundvideos:   rights.SendRoundvideos,
		SendAudios:        rights.SendAudios,
		SendVoices:        rights.SendVoices,
		SendDocs:          rights.SendDocs,
		SendPlain:         rights.SendPlain,
		EditRank:          rights.EditRank,
		SendReactions:     rights.SendReactions,
		ManageLinkedPeers: rights.ManageLinkedPeers,
		UntilDate:         rights.UntilDate,
	}
}

func zeroChannelBannedRights(rights domain.ChannelBannedRights) bool {
	return rights == domain.ChannelBannedRights{}
}
