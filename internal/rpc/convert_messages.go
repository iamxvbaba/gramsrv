package rpc

import (
	"time"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
)

func tgMessagesMessages(viewerUserID int64, list domain.MessageList) tg.MessagesMessagesClass {
	messages := make([]tg.MessageClass, 0, len(list.Messages))
	for _, msg := range list.Messages {
		if item := tgMessage(msg); item != nil {
			messages = append(messages, item)
		}
	}
	users := tgUsersForViewer(viewerUserID, list.Users)
	if list.Count > len(messages) {
		return &tg.MessagesMessagesSlice{
			Count:    list.Count,
			Messages: messages,
			Users:    users,
		}
	}
	return &tg.MessagesMessages{
		Messages: messages,
		Users:    users,
	}
}

func tgMessage(m domain.Message) tg.MessageClass {
	peer := tgPeer(m.Peer)
	if peer == nil || m.ID == 0 {
		return nil
	}
	if action := tgMessageServiceAction(m); action != nil {
		msg := &tg.MessageService{
			Out:         m.Out,
			MediaUnread: m.MediaUnread,
			Silent:      m.Silent,
			ID:          m.ID,
			PeerID:      peer,
			Date:        m.Date,
			Action:      action,
		}
		if from := tgPeer(m.From); from != nil {
			msg.FromID = from
		}
		if reply := tgMessageReplyHeader(m); reply != nil {
			msg.SetReplyTo(reply)
		}
		if m.TTLPeriod > 0 {
			msg.SetTTLPeriod(m.TTLPeriod)
		}
		if reactions := tgMessageReactions(m.OwnerUserID, m.Reactions); reactions != nil {
			msg.SetReactions(*reactions)
		}
		return msg
	}
	msg := &tg.Message{
		Out:         m.Out,
		MediaUnread: m.MediaUnread,
		ID:          m.ID,
		PeerID:      peer,
		Date:        m.Date,
		Message:     m.Body,
		Entities:    tgMessageEntities(m.Entities),
	}
	if m.EditDate != 0 {
		msg.SetEditDate(m.EditDate)
	}
	if m.HideEdited {
		msg.SetEditHide(true)
	}
	if m.Silent {
		msg.SetSilent(true)
	}
	if m.NoForwards {
		msg.SetNoforwards(true)
	}
	if m.Pinned {
		msg.SetPinned(true)
	}
	if reply := tgMessageReplyHeader(m); reply != nil {
		msg.SetReplyTo(reply)
	}
	if fwd := tgMessageFwdHeader(m.Forward); fwd != nil {
		msg.SetFwdFrom(*fwd)
	}
	// self-chat 消息恒带 saved_peer_id（收藏夹子会话分组键）：TDesktop 不做
	// 本地 fwd 推导，缺失会把转发消息错归 My Notes。
	if m.SavedPeer.ID != 0 {
		if saved := tgPeer(m.SavedPeer); saved != nil {
			msg.SetSavedPeerID(saved)
		}
	}
	if from := tgPeer(m.From); from != nil {
		msg.FromID = from
	}
	if m.ViaBotID != 0 {
		msg.SetViaBotID(m.ViaBotID)
	}
	if m.GroupedID != 0 {
		msg.SetGroupedID(m.GroupedID)
	}
	// 消息特效（私聊专属）：非零则下发，客户端据此播放一次动画。
	if m.Effect != 0 {
		msg.SetEffect(m.Effect)
	}
	if !m.Media.IsZero() {
		msg.SetMedia(tgMessageMedia(m.Media))
		// invert_media 是 message 级标志，但存于媒体快照（免消息表列）：仅当有媒体时投影。
		if m.Media.InvertMedia {
			msg.SetInvertMedia(true)
		}
	}
	// reply_markup（bot reply/inline keyboard）：仅普通 tg.Message 携带（service 消息不带）。
	if markup := tgReplyMarkup(m.ReplyMarkup); markup != nil {
		msg.SetReplyMarkup(markup)
	}
	if rich := optionalTGRichMessage("private_message", m.ID, m.RichMessage); rich != nil {
		msg.SetRichMessage(*rich)
	}
	if m.TTLPeriod > 0 {
		msg.SetTTLPeriod(m.TTLPeriod)
	}
	if reactions := tgMessageReactions(m.OwnerUserID, m.Reactions); reactions != nil {
		msg.SetReactions(*reactions)
	}
	return msg
}

func tgMessageServiceAction(msg domain.Message) tg.MessageActionClass {
	m := msg.Media
	if m == nil || m.Kind != domain.MessageMediaKindService || m.ServiceAction == nil {
		return nil
	}
	switch m.ServiceAction.Kind {
	case domain.MessageServiceActionHistoryClear:
		return &tg.MessageActionHistoryClear{}
	case domain.MessageServiceActionSuggestProfilePhoto:
		if m.ServiceAction.Photo == nil || m.ServiceAction.Photo.ID == 0 {
			return &tg.MessageActionEmpty{}
		}
		return &tg.MessageActionSuggestProfilePhoto{Photo: tgPhoto(*m.ServiceAction.Photo)}
	case domain.MessageServiceActionPinMessage:
		return &tg.MessageActionPinMessage{}
	case domain.MessageServiceActionPayment:
		payment := m.ServiceAction.Payment
		if payment == nil || payment.ChargeID == "" {
			return &tg.MessageActionEmpty{}
		}
		// messageActionPaymentSentMe is the bot's copy of a settled invoice.
		// Charge.ID is what the Bot API calls telegram_payment_charge_id and
		// refundStarPayment has to be given.
		return &tg.MessageActionPaymentSentMe{
			Currency:    payment.Currency,
			TotalAmount: payment.TotalAmount,
			Payload:     []byte(payment.Payload),
			Charge: tg.PaymentCharge{
				ID:               payment.ChargeID,
				ProviderChargeID: payment.ProviderChargeID,
			},
		}
	case domain.MessageServiceActionSetChatTheme:
		return &tg.MessageActionSetChatTheme{
			Theme: &tg.ChatTheme{Emoticon: m.ServiceAction.ChatThemeEmoticon},
		}
	case domain.MessageServiceActionNoForwardsToggle:
		action := m.ServiceAction.NoForwards
		if action == nil {
			return &tg.MessageActionEmpty{}
		}
		return &tg.MessageActionNoForwardsToggle{
			PrevValue: action.PrevValue,
			NewValue:  action.NewValue,
		}
	case domain.MessageServiceActionNoForwardsRequest:
		action := m.ServiceAction.NoForwards
		if action == nil {
			return &tg.MessageActionEmpty{}
		}
		return &tg.MessageActionNoForwardsRequest{
			Expired:   action.Expired || (action.ExpiresAt > 0 && int(time.Now().Unix()) >= action.ExpiresAt),
			PrevValue: action.PrevValue,
			NewValue:  action.NewValue,
		}
	case domain.MessageServiceActionPhoneCall:
		if m.ServiceAction.Call == nil {
			return &tg.MessageActionEmpty{}
		}
		action := &tg.MessageActionPhoneCall{
			Video:  m.ServiceAction.Call.Video,
			CallID: m.ServiceAction.Call.CallID,
		}
		if reason := tgPhoneCallDiscardReason(domain.PhoneCallDiscardReason(m.ServiceAction.Call.Reason)); reason != nil {
			action.SetReason(reason)
		}
		if m.ServiceAction.Call.Duration > 0 {
			action.SetDuration(m.ServiceAction.Call.Duration)
		}
		return action
	case domain.MessageServiceActionConferenceCall:
		c := m.ServiceAction.ConferenceCall
		if c == nil {
			return &tg.MessageActionEmpty{}
		}
		action := &tg.MessageActionConferenceCall{
			Missed: c.Missed,
			Active: c.Active,
			Video:  c.Video,
			CallID: c.CallID,
		}
		if c.Duration > 0 {
			action.SetDuration(c.Duration)
		}
		if len(c.OtherParticipants) > 0 {
			peers := tgPeerList(c.OtherParticipants)
			if len(peers) > 0 {
				action.SetOtherParticipants(peers)
			}
		}
		return action
	case domain.MessageServiceActionBotAllowed:
		allowed := m.ServiceAction.BotAllowed
		if allowed == nil {
			return &tg.MessageActionBotAllowed{}
		}
		return &tg.MessageActionBotAllowed{
			AttachMenu:  allowed.AttachMenu,
			FromRequest: allowed.FromRequest,
			Domain:      allowed.Domain,
		}
	case domain.MessageServiceActionWebViewDataSent:
		data := m.ServiceAction.WebViewData
		if data == nil {
			return &tg.MessageActionEmpty{}
		}
		if msg.Out {
			return &tg.MessageActionWebViewDataSent{Text: data.ButtonText}
		}
		return &tg.MessageActionWebViewDataSentMe{
			Text: data.ButtonText,
			Data: data.Data,
		}
	case domain.MessageServiceActionRequestedPeer:
		shared := m.ServiceAction.RequestedPeer
		if shared == nil {
			return &tg.MessageActionEmpty{}
		}
		if msg.Out {
			return &tg.MessageActionRequestedPeerSentMe{
				ButtonID: shared.ButtonID,
				Peers:    tgRequestedPeers(shared),
			}
		}
		return &tg.MessageActionRequestedPeer{
			ButtonID: shared.ButtonID,
			Peers:    tgPeerList(shared.Peers),
		}
	case domain.MessageServiceActionStarGift:
		return tgMessageActionStarGiftForViewer(m.ServiceAction.StarGift, msg.OwnerUserID)
	case domain.MessageServiceActionGiftStars:
		action := m.ServiceAction.GiftStars
		if action == nil || action.Currency == "" || action.Amount <= 0 || action.Stars <= 0 {
			return &tg.MessageActionEmpty{}
		}
		out := &tg.MessageActionGiftStars{
			Currency: action.Currency,
			Amount:   action.Amount,
			Stars:    action.Stars,
		}
		// Telegram only exposes the provider transaction id to the receiver.
		if !msg.Out && action.TransactionID != "" {
			out.SetTransactionID(action.TransactionID)
		}
		return out
	case domain.MessageServiceActionStarGiftUnique:
		return tgMessageActionStarGiftUniqueForViewer(m.ServiceAction.StarGiftUnique, msg.OwnerUserID)
	case domain.MessageServiceActionStarGiftOffer:
		action := m.ServiceAction.StarGiftOffer
		if action == nil {
			return &tg.MessageActionEmpty{}
		}
		return &tg.MessageActionStarGiftPurchaseOffer{Accepted: action.Accepted, Declined: action.Declined,
			Gift: tgUniqueStarGift(action.Gift), Price: tgStarGiftAmount(action.Price), ExpiresAt: action.ExpiresAt}
	case domain.MessageServiceActionStarGiftOfferDeclined:
		action := m.ServiceAction.StarGiftOfferDeclined
		if action == nil {
			return &tg.MessageActionEmpty{}
		}
		return &tg.MessageActionStarGiftPurchaseOfferDeclined{Expired: action.Expired,
			Gift: tgUniqueStarGift(action.Gift), Price: tgStarGiftAmount(action.Price)}
	case domain.MessageServiceActionGiftPremium:
		action := m.ServiceAction.GiftPremium
		if action == nil || action.Currency == "" || action.Amount <= 0 || action.Days <= 0 {
			return &tg.MessageActionEmpty{}
		}
		out := &tg.MessageActionGiftPremium{
			Currency: action.Currency,
			Amount:   action.Amount,
			Days:     action.Days,
		}
		if action.Message.Text != "" {
			out.SetMessage(tg.TextWithEntities{
				Text:     action.Message.Text,
				Entities: tgMessageEntities(action.Message.Entities),
			})
		}
		return out
	default:
		return &tg.MessageActionEmpty{}
	}
}

func tgMessageActionStarGiftUnique(action *domain.MessageStarGiftUniqueAction) tg.MessageActionClass {
	if action == nil {
		return &tg.MessageActionEmpty{}
	}
	out := &tg.MessageActionStarGiftUnique{
		Upgrade: action.Upgrade, Saved: action.Saved, PrepaidUpgrade: action.PrepaidUpgrade,
		Transferred: action.Transferred, Refunded: action.Refunded, Assigned: action.Assigned,
		FromOffer: action.FromOffer, Craft: action.Craft,
		Gift: tgUniqueStarGift(action.Gift),
	}
	// Channel-owned gifts cannot execute payments.getStarGiftWithdrawalUrl.
	// Keep that impossible action off service messages and admin-log/history
	// projections just as we already do for channel Craft readiness below.
	if action.Gift.Owner.Type == domain.PeerTypeUser && action.CanExportAt > 0 {
		out.SetCanExportAt(action.CanExportAt)
	}
	if action.TransferStars > 0 {
		out.SetTransferStars(action.TransferStars)
	}
	if action.ResaleAmount != nil {
		out.SetResaleAmount(tgStarGiftAmount(*action.ResaleAmount))
	}
	if action.CanTransferAt > 0 {
		out.SetCanTransferAt(action.CanTransferAt)
	}
	if action.CanResellAt > 0 {
		out.SetCanResellAt(action.CanResellAt)
	}
	if action.DropOriginalDetailsStars > 0 {
		out.SetDropOriginalDetailsStars(action.DropOriginalDetailsStars)
	}
	// Channel Craft is not executable yet. Gate on the authoritative gift owner
	// as a final wire boundary so historical JSON/admin-log actions or a future
	// constructor cannot accidentally expose Android's Craft entry marker.
	if action.Gift.Owner.Type == domain.PeerTypeUser && action.CanCraftAt > 0 {
		out.SetCanCraftAt(action.CanCraftAt)
	}
	if action.FromUserID != 0 {
		out.SetFromID(&tg.PeerUser{UserID: action.FromUserID})
	}
	if peer := tgPeer(action.Peer); peer != nil {
		out.SetPeer(peer)
	}
	if action.SavedID != 0 {
		out.SetSavedID(action.SavedID)
	}
	return out
}

// tgMessageActionStarGiftUniqueForViewer keeps owner-only lifecycle controls off
// the sender's mirror service message. Without this projection the sender sees
// actions such as "remove original details" and submits a slug that correctly
// belongs to the recipient, which then fails with PEER_ID_INVALID.
func tgMessageActionStarGiftUniqueForViewer(action *domain.MessageStarGiftUniqueAction, viewerUserID int64) tg.MessageActionClass {
	if action == nil || viewerUserID <= 0 || action.Gift.Owner.Type != domain.PeerTypeUser ||
		action.Gift.Owner.ID == viewerUserID {
		return tgMessageActionStarGiftUnique(action)
	}
	projected := *action
	projected.CanExportAt = 0
	projected.TransferStars = 0
	projected.ResaleAmount = nil
	projected.CanTransferAt = 0
	projected.CanResellAt = 0
	projected.DropOriginalDetailsStars = 0
	projected.CanCraftAt = 0
	return tgMessageActionStarGiftUnique(&projected)
}

func tgPeerList(peers []domain.Peer) []tg.PeerClass {
	out := make([]tg.PeerClass, 0, len(peers))
	for _, peer := range peers {
		if converted := tgPeer(peer); converted != nil {
			out = append(out, converted)
		}
	}
	return out
}

func tgRequestedPeers(action *domain.MessageRequestedPeerAction) []tg.RequestedPeerClass {
	if action == nil {
		return nil
	}
	details := make(map[domain.Peer]domain.MessageRequestedPeerDetails, len(action.Details))
	for _, detail := range action.Details {
		details[detail.Peer] = detail
	}
	out := make([]tg.RequestedPeerClass, 0, len(action.Peers))
	for _, peer := range action.Peers {
		detail := details[peer]
		switch peer.Type {
		case domain.PeerTypeUser:
			item := &tg.RequestedPeerUser{UserID: peer.ID}
			if action.NameRequested {
				item.SetFirstName(detail.FirstName)
				item.SetLastName(detail.LastName)
			}
			if action.UsernameRequested {
				item.SetUsername(detail.Username)
			}
			if action.PhotoRequested && detail.Photo != nil {
				item.SetPhoto(tgPhoto(*detail.Photo))
			}
			out = append(out, item)
		case domain.PeerTypeChannel:
			item := &tg.RequestedPeerChannel{ChannelID: peer.ID}
			if action.NameRequested {
				item.SetTitle(detail.Title)
			}
			if action.UsernameRequested {
				item.SetUsername(detail.Username)
			}
			if action.PhotoRequested && detail.Photo != nil {
				item.SetPhoto(tgPhoto(*detail.Photo))
			}
			out = append(out, item)
		}
	}
	return out
}

func tgMessagesDiscussionMessage(viewerUserID int64, in domain.ChannelDiscussionMessage) *tg.MessagesDiscussionMessage {
	messages := make([]tg.MessageClass, 0, len(in.Messages))
	for _, msg := range in.Messages {
		if item := tgChannelMessage(viewerUserID, msg); item != nil {
			messages = append(messages, item)
		}
	}
	channels := make([]domain.Channel, 0, 2+len(in.Channels))
	if in.PostChannel.ID != 0 {
		channels = append(channels, in.PostChannel)
	}
	if in.DiscussionChannel.ID != 0 && in.DiscussionChannel.ID != in.PostChannel.ID {
		channels = append(channels, in.DiscussionChannel)
	}
	channels = append(channels, in.Channels...)
	out := &tg.MessagesDiscussionMessage{
		Messages:    messages,
		Chats:       tgChannels(viewerUserID, channels),
		Users:       tgUsers(in.Users),
		UnreadCount: in.UnreadCount,
	}
	if in.MaxID > 0 {
		out.SetMaxID(in.MaxID)
	}
	if in.ReadInboxMaxID > 0 {
		out.SetReadInboxMaxID(in.ReadInboxMaxID)
	}
	if in.ReadOutboxMaxID > 0 {
		out.SetReadOutboxMaxID(in.ReadOutboxMaxID)
	}
	return out
}

func tgMessageReplyHeader(m domain.Message) tg.MessageReplyHeaderClass {
	if m.ReplyTo == nil {
		return nil
	}
	// story 回复（评论）投影为独立的 messageReplyStoryHeader（peer=story 作者 + story_id）。
	if m.ReplyTo.StoryID > 0 {
		peer := tgPeer(m.ReplyTo.Peer)
		if peer == nil {
			return nil
		}
		return &tg.MessageReplyStoryHeader{Peer: peer, StoryID: m.ReplyTo.StoryID}
	}
	if m.ReplyTo.MessageID <= 0 && m.ReplyTo.TopMessageID <= 0 && m.ReplyTo.External == nil {
		return nil
	}
	header := &tg.MessageReplyHeader{}
	if m.ReplyTo.ForumTopic {
		header.SetForumTopic(true)
	}
	if m.ReplyTo.MessageID > 0 {
		header.SetReplyToMsgID(m.ReplyTo.MessageID)
	}
	if m.ReplyTo.TopMessageID > 0 {
		header.SetReplyToTopID(m.ReplyTo.TopMessageID)
	}
	if m.ReplyTo.Peer.ID != 0 && m.ReplyTo.Peer != m.Peer {
		if peer := tgPeer(m.ReplyTo.Peer); peer != nil {
			header.SetReplyToPeerID(peer)
		}
	}
	if external := m.ReplyTo.External; external != nil {
		if from := tgMessageFwdHeader(&external.From); from != nil {
			header.SetReplyFrom(*from)
		}
		if !external.Media.IsZero() {
			header.SetReplyMedia(tgMessageMedia(external.Media))
		}
		if m.ReplyTo.QuoteText == "" && external.Text != "" {
			header.SetQuoteText(external.Text)
			if externalEntities := tgMessageEntities(external.Entities); len(externalEntities) > 0 {
				header.SetQuoteEntities(externalEntities)
			}
		}
	}
	if m.ReplyTo.QuoteText != "" {
		header.SetQuote(true)
		header.SetQuoteText(m.ReplyTo.QuoteText)
		// WHY: the layer-228 adapter rejects a set quote_entities flag with nil; a plain-text quote has none.
		if quoteEntities := tgMessageEntities(m.ReplyTo.QuoteEntities); len(quoteEntities) > 0 {
			header.SetQuoteEntities(quoteEntities)
		}
		header.SetQuoteOffset(m.ReplyTo.QuoteOffset)
	}
	return header
}

func tgMessageFwdHeader(fwd *domain.MessageForward) *tg.MessageFwdHeader {
	if fwd == nil || (fwd.Date == 0 && fwd.From.ID == 0 && fwd.FromName == "" && fwd.ChannelPost == 0 && fwd.SavedFrom.ID == 0 && fwd.SavedFromMsgID == 0) {
		return nil
	}
	header := &tg.MessageFwdHeader{Date: fwd.Date}
	if peer := tgPeer(fwd.From); peer != nil {
		header.SetFromID(peer)
	}
	if fwd.FromName != "" {
		header.SetFromName(fwd.FromName)
	}
	if fwd.ChannelPost > 0 {
		header.SetChannelPost(fwd.ChannelPost)
	}
	if peer := tgPeer(fwd.SavedFrom); peer != nil {
		header.SetSavedFromPeer(peer)
	}
	if fwd.SavedFromMsgID > 0 {
		header.SetSavedFromMsgID(fwd.SavedFromMsgID)
	}
	return header
}

func tgMessageReactions(viewerUserID int64, in *domain.ChannelMessageReactions) *tg.MessageReactions {
	if in == nil {
		return nil
	}
	out := &tg.MessageReactions{
		Results: make([]tg.ReactionCount, 0, len(in.Results)),
	}
	if in.CanSeeList {
		out.SetCanSeeList(true)
	}
	if in.AsTags {
		out.SetReactionsAsTags(true)
	}
	for _, item := range in.Results {
		reaction := tgMessageReaction(item.Reaction)
		if reaction == nil || item.Count <= 0 {
			continue
		}
		count := tg.ReactionCount{
			Reaction: reaction,
			Count:    item.Count,
		}
		if item.ChosenOrder > 0 {
			count.SetChosenOrder(item.ChosenOrder)
		}
		out.Results = append(out.Results, count)
	}
	if len(in.Recent) > 0 {
		recent := make([]tg.MessagePeerReaction, 0, len(in.Recent))
		for _, item := range in.Recent {
			if converted := tgMessagePeerReaction(viewerUserID, item); converted != nil {
				recent = append(recent, *converted)
			}
		}
		if len(recent) > 0 {
			out.SetRecentReactions(recent)
		}
	}
	// 付费 reaction：注入 ReactionPaid 计数 + top reactors（My/chosen 由 in.Paid 的视角数据驱动，
	// 调用方对他人视角已抹除 My/MyStars）。统一在此注入，覆盖所有频道消息读路径。
	if in.Paid != nil {
		injectPaidReaction(out, *in.Paid)
	}
	if out.Results == nil {
		out.Results = []tg.ReactionCount{}
	}
	return out
}

func tgMessagePeerReaction(viewerUserID int64, in domain.ChannelMessagePeerReaction) *tg.MessagePeerReaction {
	reaction := tgMessageReaction(in.Reaction)
	if reaction == nil || in.UserID == 0 {
		return nil
	}
	out := &tg.MessagePeerReaction{
		PeerID:   &tg.PeerUser{UserID: in.UserID},
		Date:     in.Date,
		Reaction: reaction,
	}
	if in.Big {
		out.SetBig(true)
	}
	if in.Unread && in.SenderUserID == viewerUserID {
		out.SetUnread(true)
	}
	if in.My || in.UserID == viewerUserID {
		out.SetMy(true)
	}
	return out
}

func tgMessageReaction(in domain.MessageReaction) tg.ReactionClass {
	switch in.Type {
	case domain.MessageReactionEmoji:
		if in.Emoticon == "" {
			return nil
		}
		return &tg.ReactionEmoji{Emoticon: in.Emoticon}
	case domain.MessageReactionCustomEmoji:
		if in.DocumentID <= 0 {
			return nil
		}
		return &tg.ReactionCustomEmoji{DocumentID: in.DocumentID}
	default:
		return nil
	}
}

func tgMessageEntities(entities []domain.MessageEntity) []tg.MessageEntityClass {
	if len(entities) == 0 {
		return nil
	}
	out := make([]tg.MessageEntityClass, 0, len(entities))
	for _, entity := range entities {
		switch entity.Type {
		case domain.MessageEntityBold:
			out = append(out, &tg.MessageEntityBold{Offset: entity.Offset, Length: entity.Length})
		case domain.MessageEntityItalic:
			out = append(out, &tg.MessageEntityItalic{Offset: entity.Offset, Length: entity.Length})
		case domain.MessageEntityUnderline:
			out = append(out, &tg.MessageEntityUnderline{Offset: entity.Offset, Length: entity.Length})
		case domain.MessageEntityStrike:
			out = append(out, &tg.MessageEntityStrike{Offset: entity.Offset, Length: entity.Length})
		case domain.MessageEntityCode:
			out = append(out, &tg.MessageEntityCode{Offset: entity.Offset, Length: entity.Length})
		case domain.MessageEntityPre:
			out = append(out, &tg.MessageEntityPre{Offset: entity.Offset, Length: entity.Length, Language: entity.Language})
		case domain.MessageEntityTextURL:
			out = append(out, &tg.MessageEntityTextURL{Offset: entity.Offset, Length: entity.Length, URL: entity.URL})
		case domain.MessageEntityMentionName:
			out = append(out, &tg.MessageEntityMentionName{Offset: entity.Offset, Length: entity.Length, UserID: entity.UserID})
		case domain.MessageEntitySpoiler:
			out = append(out, &tg.MessageEntitySpoiler{Offset: entity.Offset, Length: entity.Length})
		case domain.MessageEntityBlockquote:
			out = append(out, &tg.MessageEntityBlockquote{Offset: entity.Offset, Length: entity.Length, Collapsed: entity.Collapsed})
		case domain.MessageEntityCustomEmoji:
			out = append(out, &tg.MessageEntityCustomEmoji{Offset: entity.Offset, Length: entity.Length, DocumentID: entity.DocumentID})
		case domain.MessageEntityMention:
			out = append(out, &tg.MessageEntityMention{Offset: entity.Offset, Length: entity.Length})
		case domain.MessageEntityHashtag:
			out = append(out, &tg.MessageEntityHashtag{Offset: entity.Offset, Length: entity.Length})
		case domain.MessageEntityCashtag:
			out = append(out, &tg.MessageEntityCashtag{Offset: entity.Offset, Length: entity.Length})
		case domain.MessageEntityBotCommand:
			out = append(out, &tg.MessageEntityBotCommand{Offset: entity.Offset, Length: entity.Length})
		case domain.MessageEntityURL:
			out = append(out, &tg.MessageEntityURL{Offset: entity.Offset, Length: entity.Length})
		case domain.MessageEntityEmail:
			out = append(out, &tg.MessageEntityEmail{Offset: entity.Offset, Length: entity.Length})
		case domain.MessageEntityPhone:
			out = append(out, &tg.MessageEntityPhone{Offset: entity.Offset, Length: entity.Length})
		case domain.MessageEntityBankCard:
			out = append(out, &tg.MessageEntityBankCard{Offset: entity.Offset, Length: entity.Length})
		case domain.MessageEntityFormattedDate:
			out = append(out, &tg.MessageEntityFormattedDate{
				Offset:    entity.Offset,
				Length:    entity.Length,
				Date:      entity.Date,
				Relative:  entity.Relative,
				ShortTime: entity.ShortTime,
				LongTime:  entity.LongTime,
				ShortDate: entity.ShortDate,
				LongDate:  entity.LongDate,
				DayOfWeek: entity.DayOfWeek,
			})
		case domain.MessageEntityDiffInsert:
			out = append(out, &tg.MessageEntityDiffInsert{Offset: entity.Offset, Length: entity.Length})
		case domain.MessageEntityDiffReplace:
			out = append(out, &tg.MessageEntityDiffReplace{Offset: entity.Offset, Length: entity.Length, OldText: entity.OldText})
		case domain.MessageEntityDiffDelete:
			out = append(out, &tg.MessageEntityDiffDelete{Offset: entity.Offset, Length: entity.Length})
		}
	}
	return out
}

func domainMessageEntities(entities []tg.MessageEntityClass) []domain.MessageEntity {
	return domainMessageEntitiesForViewer(0, entities)
}

// domainMessageEntitiesForViewer 把客户端实体转成 domain 形态并原样保留语义
// 字段；inputMessageEntityMentionName 携带 InputUser，inputUserSelf 解析为
// viewerUserID（viewer 未知时该实体丢弃而不是落成 user_id=0）。
func domainMessageEntitiesForViewer(viewerUserID int64, entities []tg.MessageEntityClass) []domain.MessageEntity {
	if len(entities) == 0 {
		return nil
	}
	out := make([]domain.MessageEntity, 0, len(entities))
	for _, entity := range entities {
		switch e := entity.(type) {
		case *tg.MessageEntityBold:
			out = append(out, domain.MessageEntity{Type: domain.MessageEntityBold, Offset: e.Offset, Length: e.Length})
		case *tg.MessageEntityItalic:
			out = append(out, domain.MessageEntity{Type: domain.MessageEntityItalic, Offset: e.Offset, Length: e.Length})
		case *tg.MessageEntityUnderline:
			out = append(out, domain.MessageEntity{Type: domain.MessageEntityUnderline, Offset: e.Offset, Length: e.Length})
		case *tg.MessageEntityStrike:
			out = append(out, domain.MessageEntity{Type: domain.MessageEntityStrike, Offset: e.Offset, Length: e.Length})
		case *tg.MessageEntityCode:
			out = append(out, domain.MessageEntity{Type: domain.MessageEntityCode, Offset: e.Offset, Length: e.Length})
		case *tg.MessageEntityPre:
			out = append(out, domain.MessageEntity{Type: domain.MessageEntityPre, Offset: e.Offset, Length: e.Length, Language: e.Language})
		case *tg.MessageEntityTextURL:
			out = append(out, domain.MessageEntity{Type: domain.MessageEntityTextURL, Offset: e.Offset, Length: e.Length, URL: e.URL})
		case *tg.MessageEntityMentionName:
			if e.UserID != 0 {
				out = append(out, domain.MessageEntity{Type: domain.MessageEntityMentionName, Offset: e.Offset, Length: e.Length, UserID: e.UserID})
			}
		case *tg.InputMessageEntityMentionName:
			userID := int64(0)
			switch input := e.UserID.(type) {
			case *tg.InputUser:
				userID = input.UserID
			case *tg.InputUserSelf:
				userID = viewerUserID
			}
			if userID != 0 {
				out = append(out, domain.MessageEntity{Type: domain.MessageEntityMentionName, Offset: e.Offset, Length: e.Length, UserID: userID})
			}
		case *tg.MessageEntitySpoiler:
			out = append(out, domain.MessageEntity{Type: domain.MessageEntitySpoiler, Offset: e.Offset, Length: e.Length})
		case *tg.MessageEntityBlockquote:
			out = append(out, domain.MessageEntity{Type: domain.MessageEntityBlockquote, Offset: e.Offset, Length: e.Length, Collapsed: e.Collapsed})
		case *tg.MessageEntityCustomEmoji:
			out = append(out, domain.MessageEntity{Type: domain.MessageEntityCustomEmoji, Offset: e.Offset, Length: e.Length, DocumentID: e.DocumentID})
		case *tg.MessageEntityMention:
			out = append(out, domain.MessageEntity{Type: domain.MessageEntityMention, Offset: e.Offset, Length: e.Length})
		case *tg.MessageEntityHashtag:
			out = append(out, domain.MessageEntity{Type: domain.MessageEntityHashtag, Offset: e.Offset, Length: e.Length})
		case *tg.MessageEntityCashtag:
			out = append(out, domain.MessageEntity{Type: domain.MessageEntityCashtag, Offset: e.Offset, Length: e.Length})
		case *tg.MessageEntityBotCommand:
			out = append(out, domain.MessageEntity{Type: domain.MessageEntityBotCommand, Offset: e.Offset, Length: e.Length})
		case *tg.MessageEntityURL:
			out = append(out, domain.MessageEntity{Type: domain.MessageEntityURL, Offset: e.Offset, Length: e.Length})
		case *tg.MessageEntityEmail:
			out = append(out, domain.MessageEntity{Type: domain.MessageEntityEmail, Offset: e.Offset, Length: e.Length})
		case *tg.MessageEntityPhone:
			out = append(out, domain.MessageEntity{Type: domain.MessageEntityPhone, Offset: e.Offset, Length: e.Length})
		case *tg.MessageEntityBankCard:
			out = append(out, domain.MessageEntity{Type: domain.MessageEntityBankCard, Offset: e.Offset, Length: e.Length})
		case *tg.MessageEntityFormattedDate:
			out = append(out, domain.MessageEntity{
				Type:      domain.MessageEntityFormattedDate,
				Offset:    e.Offset,
				Length:    e.Length,
				Date:      e.Date,
				Relative:  e.Relative || e.GetRelative(),
				ShortTime: e.ShortTime || e.GetShortTime(),
				LongTime:  e.LongTime || e.GetLongTime(),
				ShortDate: e.ShortDate || e.GetShortDate(),
				LongDate:  e.LongDate || e.GetLongDate(),
				DayOfWeek: e.DayOfWeek || e.GetDayOfWeek(),
			})
		}
	}
	return out
}

func tgPeer(p domain.Peer) tg.PeerClass {
	switch p.Type {
	case domain.PeerTypeUser:
		return &tg.PeerUser{UserID: p.ID}
	case domain.PeerTypeChannel:
		return &tg.PeerChannel{ChannelID: p.ID}
	default:
		return nil
	}
}

func tgMigratedLegacyChat(viewerUserID int64, ch domain.Channel, self *domain.ChannelMember) *tg.Chat {
	if ch.ID == 0 || ch.Deleted {
		return nil
	}
	out := &tg.Chat{
		Creator:           ch.CreatorUserID == viewerUserID && viewerUserID != 0,
		Deactivated:       true,
		Noforwards:        ch.NoForwards,
		ID:                ch.ID,
		Title:             ch.Title,
		Photo:             tgChatPhoto(ch),
		ParticipantsCount: ch.ParticipantsCount,
		Date:              ch.Date,
		Version:           ch.Pts,
	}
	out.SetMigratedTo(&tg.InputChannel{ChannelID: ch.ID, AccessHash: ch.AccessHash})
	if self != nil {
		if self.Status == domain.ChannelMemberLeft {
			out.Left = true
		}
		switch self.Role {
		case domain.ChannelRoleCreator:
			out.Creator = true
			out.SetAdminRights(tgChatAdminRights(self.AdminRights))
		case domain.ChannelRoleAdmin:
			out.SetAdminRights(tgChatAdminRights(self.AdminRights))
		}
	}
	out.SetDefaultBannedRights(tgDefaultChatBannedRights(ch.DefaultBannedRights))
	return out
}
