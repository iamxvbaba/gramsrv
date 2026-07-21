package rpc

import (
	"context"
	"errors"
	"github.com/iamxvbaba/td/tg"
	"telesrv/internal/domain"
	"unicode/utf8"
)

func (r *Router) onMessagesEditMessage(ctx context.Context, req *tg.MessagesEditMessageRequest) (tg.UpdatesClass, error) {
	scheduleDate, hasScheduleDate := req.GetScheduleDate()
	scheduleRepeatPeriod, hasScheduleRepeatPeriod := req.GetScheduleRepeatPeriod()
	if hasScheduleRepeatPeriod && scheduleRepeatPeriod != 0 {
		return nil, scheduleDateInvalidErr()
	}
	if hasScheduleRepeatPeriod && !hasScheduleDate {
		return nil, scheduleDateInvalidErr()
	}
	if _, ok := req.GetQuickReplyShortcutID(); ok {
		return nil, messageIDInvalidErr()
	}
	message, hasMessage := req.GetMessage()
	if hasMessage && utf8.RuneCountInString(message) > maxSendMessageTextLength {
		return nil, messageTooLongErr()
	}
	entities, _ := req.GetEntities()
	if hasMessage {
		if len(entities) > maxMessageEntityCount {
			return nil, entitiesTooLongErr()
		}
	} else {
		entities = nil
	}
	var richMessage *domain.MessageRichMessage
	hasRichMessage := req.RichMessage != nil
	if hasRichMessage {
		var richErr error
		richMessage, richErr = r.domainRichMessageFromInput(ctx, req.RichMessage)
		if richErr != nil {
			return nil, richErr
		}
	}
	if hasMessage && hasRichMessage {
		return nil, mediaInvalidErr()
	}
	// Explicit text and rich edits are replacement operations. A text edit must
	// clear a previously stored rich payload; a rich edit already replaces it.
	replaceRichMessage := hasRichMessage || hasMessage
	if hasMessage && richMessage == nil {
		// 编辑后的文本同样补服务端自动实体（url/@mention/#hashtag/bot command），与发送一致；
		// 覆盖频道/私聊编辑与各自的定时编辑分支（editScheduledMessage 仅由本处调用）。
		entities = augmentAutoEntities(message, entities)
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	if userID == 0 {
		return nil, peerIDInvalidErr()
	}
	peer, err := r.checkedDomainPeerFromInputPeer(ctx, userID, req.Peer)
	if err != nil {
		return nil, err
	}
	if hasScheduleDate {
		if media, ok := req.GetMedia(); ok && !editMessageMediaCanDegradeToText(media) {
			return nil, mediaInvalidErr()
		}
		return r.editScheduledMessage(ctx, userID, peer, req.ID, message, hasMessage, entities, richMessage, replaceRichMessage, scheduleDate)
	}
	if media, ok := req.GetMedia(); ok {
		// 关闭 poll 走 editMessage + InputMediaPoll(closed)（TDesktop "Stop poll" 路径）。
		if pollMedia, isPoll := media.(*tg.InputMediaPoll); isPoll {
			return r.onEditMessageClosePoll(ctx, req, pollMedia)
		}
		// live location 续报/停止走 editMessage + InputMediaGeoLive（DrKLO Android 路径）。
		if liveMedia, isLive := media.(*tg.InputMediaGeoLive); isLive {
			return r.onEditMessageLiveLocation(ctx, req, liveMedia)
		}
		if !editMessageMediaCanDegradeToText(media) {
			return nil, mediaInvalidErr()
		}
	}
	_, hasMediaForContent := req.GetMedia()
	if !hasMessage && !hasRichMessage && !hasMediaForContent {
		return nil, messageEmptyErr()
	}
	if message == "" && richMessage == nil {
		// 编辑媒体消息时 message="" 是合法的清空 caption；当前文本-only
		// 编辑模型由 store 层校验目标消息（无媒体的纯文本消息清空仍会
		// 落 MESSAGE_EMPTY），RPC 层不再一刀切拒绝。
		if !hasMediaForContent {
			return nil, messageEmptyErr()
		}
	}
	// reply_markup（bot inline keyboard）：仅 bot 编辑自己消息时替换/清空（非 bot 丢弃，
	// 不报错）。P3 仅私聊；markup-only 编辑（无文本）暂不支持——须随文本一起编辑。
	// 携带 inline markup（即便空 rows）即视为「设置」：空 inline 表示清空键盘，
	// 否则替换；非 inline（reply keyboard 等）或非 bot 一律不设置（保留原 markup）。
	var replyMarkup *domain.MessageReplyMarkup
	var setReplyMarkup bool
	if req.ReplyMarkup != nil {
		isBot := r.userIsBot(ctx, userID)
		replyMarkup, err = domainReplyMarkupForSender(req.ReplyMarkup, isBot)
		if err != nil {
			return nil, replyMarkupErr(err)
		}
		if _, isInline := req.ReplyMarkup.(*tg.ReplyInlineMarkup); isInline && isBot {
			setReplyMarkup = true
		}
	}
	if setReplyMarkup {
		if err := r.validateReplyMarkupForPeer(ctx, userID, peer, replyMarkup); err != nil {
			return nil, err
		}
	}
	if peer.Type == domain.PeerTypeChannel {
		if r.deps.Channels == nil {
			return nil, peerIDInvalidErr()
		}
		mentionUserIDs, err := r.mentionedUserIDsFromMessage(ctx, userID, message, entities)
		if err != nil {
			return nil, internalErr()
		}
		res, err := r.deps.Channels.EditMessage(ctx, userID, domain.EditChannelMessageRequest{
			UserID:         userID,
			ChannelID:      peer.ID,
			ID:             req.ID,
			Message:        message,
			Entities:       domainMessageEntitiesForViewer(userID, entities),
			MentionUserIDs: mentionUserIDs,
			SetReplyMarkup: setReplyMarkup,
			ReplyMarkup:    replyMarkup,
			SetRichMessage: replaceRichMessage,
			RichMessage:    richMessage,
			EditDate:       int(r.clock.Now().Unix()),
		})
		if err != nil {
			return nil, channelEditErr(err)
		}
		updates := r.channelEditMessageUpdates(ctx, userID, res)
		// 编辑 fan-out 异步化 + 跨 viewer 投影预热（设计 Phase 0/Phase 1；P1-x bot 高频 editMessage
		// 是稳态放大源）。同步 echo 仍走上面单 viewer 的 channelEditMessageUpdates。
		r.enqueueChannelEditMessageFanout(ctx, userID, res)
		return updates, nil
	}
	if peer.Type != domain.PeerTypeUser || r.deps.Messages == nil {
		return nil, peerIDInvalidErr()
	}
	blocked, err := r.peerBlocksUser(ctx, userID, peer.ID)
	if err != nil {
		return nil, err
	}
	if blocked {
		return nil, messageEditForbiddenErr()
	}
	sessionID, _ := SessionIDFrom(ctx)
	res, err := r.deps.Messages.EditMessage(ctx, userID, domain.EditMessageRequest{
		OwnerUserID:     userID,
		Peer:            peer,
		ID:              req.ID,
		Message:         message,
		Entities:        domainMessageEntitiesForViewer(userID, entities),
		EditDate:        int(r.clock.Now().Unix()),
		OriginAuthKeyID: rawAuthKeyIDForOrigin(ctx),
		OriginSessionID: sessionID,
		SetReplyMarkup:  setReplyMarkup,
		ReplyMarkup:     replyMarkup,
		SetRichMessage:  replaceRichMessage,
		RichMessage:     richMessage,
	})
	if err != nil {
		return nil, messageEditErr(err)
	}
	r.enqueueBotAPIPrivateEditUpdatesAsync(ctx, res)
	self := res.Self()
	if self.Event.Pts == 0 || self.Message.ID == 0 {
		return nil, messageIDInvalidErr()
	}
	users := r.usersForMessageUpdate(ctx, userID, self.Message)
	chats := r.chatsForMessageUpdate(ctx, userID, self.Message)
	return tgEditMessageUpdates(self.Event, self.Message, users, chats), nil
}

func editMessageMediaCanDegradeToText(media tg.InputMediaClass) bool {
	switch media.(type) {
	case *tg.InputMediaEmpty, *tg.InputMediaWebPage:
		return true
	default:
		return false
	}
}

func (r *Router) onMessagesGetMessageEditData(ctx context.Context, req *tg.MessagesGetMessageEditDataRequest) (*tg.MessagesMessageEditData, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	if req.ID <= 0 || req.ID > domain.MaxMessageBoxID {
		return nil, messageIDInvalidErr()
	}
	peer, err := r.checkedDomainPeerFromInputPeer(ctx, userID, req.Peer)
	if err != nil {
		return nil, err
	}
	switch peer.Type {
	case domain.PeerTypeChannel:
		if r.deps.Channels == nil {
			return nil, peerIDInvalidErr()
		}
		history, err := r.deps.Channels.GetHistory(ctx, userID, domain.ChannelHistoryFilter{
			ChannelID: peer.ID,
			Limit:     1,
			MaxID:     req.ID,
			MinID:     req.ID - 1,
		})
		if err != nil {
			return nil, channelInvalidErr(err)
		}
		if len(history.Messages) != 1 || history.Messages[0].ID != req.ID {
			return nil, messageIDInvalidErr()
		}
		msg := history.Messages[0]
		if msg.Deleted || msg.Action != nil {
			return nil, messageIDInvalidErr()
		}
		view, err := r.deps.Channels.GetChannel(ctx, userID, peer.ID)
		if err != nil {
			return nil, channelInvalidErr(err)
		}
		if msg.SenderUserID != userID && !canEditChannelMessageForRPC(view.Self) {
			return nil, messageAuthorRequiredErr()
		}
	case domain.PeerTypeUser:
		if r.deps.Messages == nil {
			return nil, messageIDInvalidErr()
		}
		msg, ok, err := r.lookupOwnerMessage(ctx, userID, req.ID)
		if err != nil {
			return nil, internalErr()
		}
		if !ok || msg.Peer != peer {
			return nil, messageIDInvalidErr()
		}
		if !msg.Out || msg.From.ID != userID {
			return nil, messageAuthorRequiredErr()
		}
	default:
		return nil, peerIDInvalidErr()
	}
	return &tg.MessagesMessageEditData{}, nil
}

func canEditChannelMessageForRPC(member domain.ChannelMember) bool {
	return member.Role == domain.ChannelRoleCreator ||
		(member.Role == domain.ChannelRoleAdmin && member.AdminRights.EditMessages)
}

func messageEditErr(err error) error {
	switch {
	case errors.Is(err, domain.ErrMessageIDInvalid):
		return messageIDInvalidErr()
	case errors.Is(err, domain.ErrMessageEmpty):
		return messageEmptyErr()
	case errors.Is(err, domain.ErrMessageAuthorRequired):
		return messageAuthorRequiredErr()
	case errors.Is(err, domain.ErrMessageNotModified):
		return messageNotModifiedErr()
	default:
		return internalErr()
	}
}

func channelEditErr(err error) error {
	switch {
	case errors.Is(err, domain.ErrMessageIDInvalid):
		return messageIDInvalidErr()
	case errors.Is(err, domain.ErrMessageEmpty):
		return messageEmptyErr()
	case errors.Is(err, domain.ErrMessageAuthorRequired):
		return messageAuthorRequiredErr()
	case errors.Is(err, domain.ErrMessageNotModified):
		return messageNotModifiedErr()
	default:
		return channelInvalidErr(err)
	}
}

func tgEditMessageUpdates(event domain.UpdateEvent, msg domain.Message, users []tg.UserClass, chats []tg.ChatClass) *tg.Updates {
	update := tgOtherUpdateFromEvent(domain.UpdateEvent{
		Type:     domain.UpdateEventEditMessage,
		Pts:      event.Pts,
		PtsCount: event.PtsCount,
		Message:  msg,
	})
	if update == nil {
		return &tg.Updates{Date: event.Date}
	}
	return &tg.Updates{
		Updates: []tg.UpdateClass{update},
		Users:   users,
		Chats:   chats,
		Date:    event.Date,
		Seq:     0,
	}
}
