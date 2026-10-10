package rpc

import (
	"context"
	"errors"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"

	"telesrv/internal/domain"
)

func (r *Router) onMessagesSendMessage(ctx context.Context, req *tg.MessagesSendMessageRequest) (tg.UpdatesClass, error) {
	start := r.clock.Now()
	var duplicate bool
	var sendErr error
	defer func() {
		r.metrics().MessageSend(r.clock.Now().Sub(start), duplicate, sendErr)
	}()
	if utf8.RuneCountInString(req.Message) > maxSendMessageTextLength {
		sendErr = messageTooLongErr()
		return nil, sendErr
	}
	if len(req.Entities) > maxMessageEntityCount {
		sendErr = entitiesTooLongErr()
		return nil, sendErr
	}
	if req.RandomID == 0 {
		sendErr = randomIDEmptyErr()
		return nil, sendErr
	}
	if req.QuickReplyShortcut != nil {
		updates, err := r.onMessagesSaveQuickReplyText(ctx, req)
		if err != nil {
			sendErr = err
			return nil, err
		}
		return updates, nil
	}
	if req.ScheduleRepeatPeriod != 0 {
		sendErr = scheduleDateInvalidErr()
		return nil, sendErr
	}
	if err := sendMessageUnsupportedOptionErr(req); err != nil {
		sendErr = err
		return nil, sendErr
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		sendErr = internalErr()
		return nil, sendErr
	}
	if userID == 0 {
		sendErr = peerIDInvalidErr()
		return nil, sendErr
	}
	peer, ok := r.domainPeerFromInputPeer(userID, req.Peer)
	if !ok || peer.ID == 0 {
		sendErr = peerIDInvalidErr()
		return nil, sendErr
	}
	idempotencyFingerprint, err := sendMessageIdempotencyFingerprint(req)
	if err != nil {
		sendErr = internalErr()
		return nil, sendErr
	}
	// 自动实体必须在普通/频道/monoforum 分流前完成，保证所有文本写路径持久化同一份
	// 服务端补全结果。指纹仍基于原始请求，派生实体不改变 random_id 幂等语义。
	entities := req.Entities
	if req.RichMessage == nil {
		entities = r.augmentAutoEntities(req.Message, entities)
	}
	suggestedInput, hasSuggestedPost := req.GetSuggestedPost()
	// monoforum 普通用户发送不带 reply_to，saved_peer 必须由服务端推导为自己；管理员回复才必须
	// 显式携带 monoforum_peer_id。仅凭 reply_to 判路由会把用户请求误送进普通 megagroup 路径。
	var mono domain.Channel
	var monoforum, monoforumAdmin bool
	if peer.Type == domain.PeerTypeChannel && r.deps.Channels != nil {
		mono, monoforumAdmin, err = r.deps.Channels.ResolveMonoforumSend(ctx, userID, peer.ID)
		switch {
		case err == nil:
			monoforum = true
		case !errors.Is(err, domain.ErrChannelInvalid):
			sendErr = internalErr()
			return nil, sendErr
		}
	}
	if hasSuggestedPost && !monoforum {
		sendErr = suggestedPostPeerInvalidErr()
		return nil, sendErr
	}
	if monoforum {
		suggestedPost, suggestedErr := domainSuggestedPost(suggestedInput, hasSuggestedPost)
		if suggestedErr != nil {
			sendErr = suggestedErr
			return nil, sendErr
		}
		savedPeer, err := r.monoforumSavedPeerForSender(userID, monoforumAdmin, req.ReplyTo)
		if err != nil {
			sendErr = err
			return nil, sendErr
		}
		replyTo, err := r.monoforumMessageReplyFromInput(ctx, userID, peer, req.ReplyTo)
		if err != nil {
			sendErr = err
			return nil, sendErr
		}
		replay, err := r.lookupChannelSendReplay(ctx, userID, peer.ID, savedPeer, req.RandomID, idempotencyFingerprint)
		if err != nil {
			sendErr = err
			return nil, err
		}
		if replay.found {
			duplicate = true
			if req.ClearDraft {
				r.clearDraftAfterSend(ctx, userID, peer, replyTo)
			}
			updates, projectionErr := r.monoforumSendUpdatesStrict(ctx, userID, replay.channel.Channel, savedPeer, replay.channel)
			if projectionErr != nil {
				sendErr = projectionErr
				return nil, projectionErr
			}
			return updates, nil
		}
		if err := r.checkSendRateLimit(ctx, userID, 1); err != nil {
			sendErr = err
			return nil, sendErr
		}
		checkedPeer, err := r.checkedDomainPeerFromInputPeer(ctx, userID, req.Peer)
		if err != nil {
			sendErr = err
			return nil, sendErr
		}
		updates, err := r.sendMonoforumMessage(ctx, userID, checkedPeer, mono, monoforumAdmin, domain.SendMonoforumMessageRequest{
			SavedPeer:              savedPeer,
			RandomID:               req.RandomID,
			IdempotencyFingerprint: idempotencyFingerprint,
			IdempotencyPreflighted: replay.checked,
			Message:                req.Message,
			Entities:               domainMessageEntities(entities),
			ReplyTo:                replyTo,
			Silent:                 req.Silent,
			NoForwards:             req.Noforwards,
			SuggestedPost:          suggestedPost,
			AllowPaidStars:         req.AllowPaidStars,
			ClearDraft:             req.ClearDraft,
		})
		if err != nil {
			sendErr = err
			return nil, sendErr
		}
		return updates, nil
	}
	replay, err := r.lookupOutgoingReplay(ctx, userID, peer, req.RandomID, idempotencyFingerprint)
	if err != nil {
		sendErr = err
		return nil, err
	}
	if replay.found {
		duplicate = true
		return r.outgoingReplayUpdates(ctx, userID, peer, req.RandomID, replay), nil
	}
	// Mutable catalog state, rate accounting and access checks are intentionally after exact
	// replay lookup: a committed send remains acknowledgeable after those states change.
	if r.messageEffectInvalid(ctx, req.Effect) {
		sendErr = effectIDInvalidErr()
		return nil, sendErr
	}
	if err := r.checkSendRateLimit(ctx, userID, 1); err != nil {
		sendErr = err
		return nil, sendErr
	}
	peer, err = r.checkedDomainPeerFromInputPeer(ctx, userID, req.Peer)
	if err != nil {
		sendErr = err
		return nil, sendErr
	}
	// reply_markup：bot 可发送 inline keyboard 与普通 reply keyboard/hide/force；
	// 非 bot 静默丢弃。仅请求携带 markup 时查询 is_bot。
	// 仅在请求携带 markup 时才查 is_bot，避免普通发送多打一次查询。
	var replyMarkup *domain.MessageReplyMarkup
	if req.ReplyMarkup != nil {
		replyMarkup, err = domainOutgoingReplyMarkupForSender(req.ReplyMarkup, r.userIsBot(ctx, userID))
		if err != nil {
			sendErr = replyMarkupErr(err)
			return nil, sendErr
		}
		if err := r.validateReplyMarkupForPeer(ctx, userID, peer, replyMarkup); err != nil {
			sendErr = err
			return nil, sendErr
		}
	}
	// rich_message（Layer 228 富文本）：blocks、HTML、Markdown 均在边界归一为
	// PageBlock + 内嵌媒体快照；普通消息恒 nil。
	var richMessage *domain.MessageRichMessage
	if req.RichMessage != nil {
		richMessage, err = r.domainRichMessageFromInput(ctx, req.RichMessage)
		if err != nil {
			sendErr = err
			return nil, sendErr
		}
	}
	if req.Message != "" && richMessage != nil {
		sendErr = mediaInvalidErr()
		return nil, sendErr
	}
	if req.Message == "" && richMessage == nil {
		sendErr = messageEmptyErr()
		return nil, sendErr
	}
	// 链接预览：纯文本消息（私聊或频道）含可预览 URL 且未抑制时，挂 pending 占位，异步解析回填。
	// 富文本消息有独立媒体语义，不叠加。
	var previewMedia *domain.MessageMedia
	if richMessage == nil {
		previewMedia = r.webPageMediaFromText(ctx, req.Message, entities, req.NoWebpage, req.InvertMedia)
	}
	if req.ScheduleDate != 0 && !scheduleDateIsImmediate(req.ScheduleDate, int(r.clock.Now().Unix())) {
		updates, err := r.scheduleOutgoing(ctx, userID, peer, outgoingSend{
			randomID:               req.RandomID,
			idempotencyFingerprint: idempotencyFingerprint,
			idempotencyPreflighted: replay.checked,
			message:                req.Message,
			entities:               entities,
			media:                  previewMedia,
			silent:                 req.Silent,
			noforwards:             req.Noforwards,
			replyToInput:           req.ReplyTo,
			sendAsInput:            req.SendAs,
			clearDraft:             req.ClearDraft,
			richMessage:            richMessage,
			allowPaidStars:         req.AllowPaidStars,
		}, req.ScheduleDate, req.ScheduleRepeatPeriod)
		if err != nil {
			sendErr = err
			return nil, err
		}
		return updates, nil
	}
	updates, dup, err := r.sendOutgoing(ctx, userID, peer, outgoingSend{
		randomID:               req.RandomID,
		idempotencyFingerprint: idempotencyFingerprint,
		idempotencyPreflighted: replay.checked,
		message:                req.Message,
		entities:               entities,
		media:                  previewMedia,
		silent:                 req.Silent,
		noforwards:             req.Noforwards,
		replyToInput:           req.ReplyTo,
		sendAsInput:            req.SendAs,
		clearDraft:             req.ClearDraft,
		replyMarkup:            replyMarkup,
		richMessage:            richMessage,
		effect:                 req.Effect,
		allowPaidStars:         req.AllowPaidStars,
	})
	duplicate = dup
	if err != nil {
		sendErr = err
		return nil, sendErr
	}
	return updates, nil
}

func messageSendErr(err error) error {
	var paymentRequired *domain.StarsPaymentRequiredError
	switch {
	case errors.As(err, &paymentRequired) && paymentRequired.Stars > 0:
		return allowPaymentRequiredErr(paymentRequired.Stars)
	case errors.Is(err, domain.ErrStarsInsufficient):
		return balanceTooLowErr()
	case errors.Is(err, domain.ErrUserFrozen):
		return frozenMethodInvalidErr()
	case errors.Is(err, domain.ErrChatForwardsRestricted):
		return chatForwardsRestrictedErr()
	case errors.Is(err, domain.ErrQuoteTextInvalid):
		return quoteTextInvalidErr()
	case errors.Is(err, domain.ErrReplyMessageIDInvalid):
		return replyMessageIDInvalidErr()
	case errors.Is(err, domain.ErrMessageRandomIDDuplicate):
		return randomIDDuplicateErr()
	case errors.Is(err, domain.ErrMessageEmpty):
		return messageEmptyErr()
	case errors.Is(err, domain.ErrPeerDeleted):
		// The official shape for writing to an account that no longer exists:
		// the peer is not a valid target, not a permission problem.
		return peerIDInvalidErr()
	default:
		return internalErr()
	}
}

func (r *Router) peerBlocksUser(ctx context.Context, userID, peerUserID int64) (bool, error) {
	if userID == 0 || peerUserID == 0 || userID == peerUserID || r.deps.Contacts == nil {
		return false, nil
	}
	blocked, err := r.deps.Contacts.IsBlocked(ctx, peerUserID, userID)
	if err != nil {
		return false, internalErr()
	}
	return blocked, nil
}

func (r *Router) messageReplyFromInput(ctx context.Context, userID int64, peer domain.Peer, input tg.InputReplyToClass) (*domain.MessageReply, error) {
	if input == nil {
		return nil, nil
	}
	reply, ok := input.(*tg.InputReplyToMessage)
	if !ok {
		switch st := input.(type) {
		case *tg.InputReplyToStory:
			// story 回复（评论）：客户端发一条带 reply_to=inputReplyToStory 的私聊消息。
			// 只支持回复会话对端（story 作者）的 story，投影为 messageReplyStoryHeader。
			if st.StoryID <= 0 || st.StoryID > domain.MaxStoryID {
				return nil, storyIDInvalidErr()
			}
			storyOwner, err := r.checkedDomainPeerFromInputPeer(ctx, userID, st.Peer)
			if err != nil {
				return nil, err
			}
			if storyOwner != peer {
				return nil, storyIDInvalidErr()
			}
			return &domain.MessageReply{Peer: storyOwner, StoryID: st.StoryID}, nil
		case *tg.InputReplyToMonoForum:
			return nil, replyToMonoforumPeerInvalidErr()
		default:
			return nil, inputConstructorInvalidErr()
		}
	}
	if reply.Zero() {
		return nil, nil
	}
	if _, ok := reply.GetMonoforumPeerID(); ok {
		return nil, replyToMonoforumPeerInvalidErr()
	}
	if _, ok := reply.GetTodoItemID(); ok {
		return nil, replyMessageIDInvalidErr()
	}
	if _, ok := reply.GetPollOption(); ok {
		return nil, pollOptionInvalidErr()
	}
	replyPeer := peer
	if inputPeer, ok := reply.GetReplyToPeerID(); ok {
		parsed, err := r.checkedMessageReadPeer(ctx, userID, inputPeer, false)
		if err != nil {
			if e, ok := tgerr.As(err); ok && e.Code >= 500 {
				return nil, err
			}
			return nil, replyMessageIDInvalidErr()
		}
		replyPeer = parsed
	}
	topMsgID, ok := reply.GetTopMsgID()
	if ok && (topMsgID < 0 || topMsgID > domain.MaxMessageBoxID) {
		return nil, replyMessageIDInvalidErr()
	}
	if reply.ReplyToMsgID < 0 || reply.ReplyToMsgID > domain.MaxMessageBoxID {
		return nil, replyMessageIDInvalidErr()
	}
	if reply.ReplyToMsgID == 0 && topMsgID == 0 {
		return nil, replyMessageIDInvalidErr()
	}
	// inputReplyToMessage.reply_to_peer_id is explicitly allowed to point to a
	// different dialog. Private-source existence is checked transactionally by
	// MessageStore; channel sources are validated here because they live in the
	// channel store rather than message_boxes.
	if replyPeer.Type == domain.PeerTypeChannel && reply.ReplyToMsgID > 0 {
		if r.deps.Channels == nil {
			return nil, replyMessageIDInvalidErr()
		}
		history, err := r.deps.Channels.GetMessages(ctx, userID, replyPeer.ID, []int{reply.ReplyToMsgID})
		if err != nil || len(history.Messages) != 1 || history.Messages[0].ID != reply.ReplyToMsgID {
			return nil, replyMessageIDInvalidErr()
		}
	}
	quoteText, _ := reply.GetQuoteText()
	if utf8.RuneCountInString(quoteText) > maxReplyQuoteLength {
		return nil, limitInvalidErr()
	}
	quoteEntities, _ := reply.GetQuoteEntities()
	if len(quoteEntities) > maxMessageEntityCount {
		return nil, limitInvalidErr()
	}
	quoteOffset, ok := reply.GetQuoteOffset()
	if ok && (quoteOffset < 0 || quoteOffset > domain.MaxMessageReplyQuoteOffset) {
		return nil, replyMessageIDInvalidErr()
	}
	return &domain.MessageReply{
		MessageID:     reply.ReplyToMsgID,
		Peer:          replyPeer,
		TopMessageID:  topMsgID,
		QuoteText:     quoteText,
		QuoteEntities: domainMessageEntities(quoteEntities),
		QuoteOffset:   quoteOffset,
	}, nil
}

func sendMessageUnsupportedOptionErr(req *tg.MessagesSendMessageRequest) error {
	switch {
	// reply_markup 不再一律拒绝：bot inline keyboard 在 sendOutgoing 前单独解析+校验
	// （非 bot 静默丢弃，I1）。
	case req.QuickReplyShortcut != nil:
		return shortcutInvalidErr()
	// req.Effect 不再一律拒绝：消息特效已实现，合法性在 messageEffectInvalid 单独校验。
	case req.AllowPaidStars < 0:
		return starsAmountInvalidErr()
	case req.AllowPaidFloodskip:
		return paymentUnsupportedErr()
	default:
		return nil
	}
}

// messageEffectInvalid 校验消息特效 id：0（无特效）恒合法；非零必须命中 getAvailableEffects
// 目录（客户端只会从该目录选取 id），否则视为非法 → EFFECT_ID_INVALID。effects 目录常驻
// 内存（seed 时构建），校验为内存线性扫描，不查库；effect==0 的常规发送零额外成本。
func (r *Router) messageEffectInvalid(ctx context.Context, effect int64) bool {
	if effect == 0 {
		return false
	}
	if r.deps.Files == nil {
		return true
	}
	effects, _, err := r.deps.Files.AvailableEffects(ctx)
	if err != nil {
		return true
	}
	for _, e := range effects {
		if e.ID == effect {
			return false
		}
	}
	return true
}

func (r *Router) mentionedUserIDsFromMessage(ctx context.Context, currentUserID int64, message string, entities []tg.MessageEntityClass) ([]int64, error) {
	if r.deps.Users == nil {
		return nil, nil
	}
	identity, _ := r.deps.Users.(UserIdentityService)
	seen := make(map[int64]struct{})
	out := make([]int64, 0)
	add := func(id int64) {
		if id == 0 {
			return
		}
		if _, ok := seen[id]; ok {
			return
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	for _, entity := range entities {
		input, ok := entity.(*tg.InputMessageEntityMentionName)
		if !ok || input.UserID == nil {
			continue
		}
		user, found, err := r.userFromInput(ctx, currentUserID, input.UserID)
		if err != nil {
			return nil, internalErr()
		}
		if found {
			add(user.ID)
		}
		if len(out) >= domain.MaxChannelMentionRecipients {
			return out, nil
		}
	}
	if identity != nil {
		blocked := mentionScanBlockedSpansFromTGEntities(message, entities)
		for _, username := range extractMentionUsernames(message, domain.MaxChannelMentionRecipients-len(out), blocked) {
			user, found, err := identity.ResolveUsername(ctx, currentUserID, username)
			if err != nil {
				if isMentionResolveMiss(err) {
					continue
				}
				return nil, internalErr()
			}
			if found {
				add(user.ID)
			}
			if len(out) >= domain.MaxChannelMentionRecipients {
				return out, nil
			}
		}
	}
	return out, nil
}

func isMentionResolveMiss(err error) bool {
	return errors.Is(err, domain.ErrUsernameInvalid) || errors.Is(err, domain.ErrUsernameNotOccupied)
}

func extractMentionUsernames(message string, limit int, blocked []byteSpan) []string {
	if limit <= 0 || message == "" {
		return nil
	}
	blocked = mergeByteSpans(append(blocked, rawURLByteSpans(message)...))
	blockIndex := 0
	seen := make(map[string]struct{})
	out := make([]string, 0)
	for i := 0; i < len(message); i++ {
		if message[i] != '@' {
			continue
		}
		for blockIndex < len(blocked) && blocked[blockIndex].end <= i {
			blockIndex++
		}
		if blockIndex < len(blocked) && blocked[blockIndex].start <= i && i < blocked[blockIndex].end {
			i = blocked[blockIndex].end - 1
			continue
		}
		if i > 0 && isUsernameByte(message[i-1]) {
			continue
		}
		j := i + 1
		for j < len(message) && isUsernameByte(message[j]) {
			j++
		}
		if j == i+1 {
			continue
		}
		username := strings.ToLower(message[i+1 : j])
		if _, ok := seen[username]; ok {
			continue
		}
		seen[username] = struct{}{}
		out = append(out, username)
		if len(out) == limit {
			return out
		}
		i = j - 1
	}
	return out
}

func mergeByteSpans(spans []byteSpan) []byteSpan {
	if len(spans) == 0 {
		return nil
	}
	out := spans[:0]
	for _, span := range spans {
		if span.start < 0 || span.end <= span.start {
			continue
		}
		inserted := false
		for i := range out {
			if span.start < out[i].start {
				out = append(out, byteSpan{})
				copy(out[i+1:], out[i:])
				out[i] = span
				inserted = true
				break
			}
		}
		if !inserted {
			out = append(out, span)
		}
	}
	if len(out) == 0 {
		return nil
	}
	merged := out[:1]
	for _, span := range out[1:] {
		last := &merged[len(merged)-1]
		if span.start <= last.end {
			if span.end > last.end {
				last.end = span.end
			}
			continue
		}
		merged = append(merged, span)
	}
	return merged
}

func mentionScanBlockedSpansFromTGEntities(message string, entities []tg.MessageEntityClass) []byteSpan {
	if len(entities) == 0 {
		return nil
	}
	bounds := utf16ByteBoundaries(message)
	var out []byteSpan
	for _, entity := range entities {
		switch entity.(type) {
		case *tg.MessageEntityURL, *tg.MessageEntityTextURL:
		default:
			continue
		}
		if span, ok := byteSpanFromUTF16Bounds(bounds, entity.GetOffset(), entity.GetLength()); ok {
			out = append(out, span)
		}
	}
	return out
}

func mentionScanBlockedSpansFromDomainEntities(message string, entities []domain.MessageEntity) []byteSpan {
	if len(entities) == 0 {
		return nil
	}
	bounds := utf16ByteBoundaries(message)
	var out []byteSpan
	for _, entity := range entities {
		if entity.Type != domain.MessageEntityURL && entity.Type != domain.MessageEntityTextURL {
			continue
		}
		if span, ok := byteSpanFromUTF16Bounds(bounds, entity.Offset, entity.Length); ok {
			out = append(out, span)
		}
	}
	return out
}

func utf16ByteBoundaries(message string) []int {
	total := utf16CodeUnitLen(message)
	bounds := make([]int, total+1)
	for i := range bounds {
		bounds[i] = -1
	}
	unit := 0
	bounds[0] = 0
	for i, r := range message {
		bounds[unit] = i
		if r <= 0xFFFF {
			unit++
		} else {
			unit += 2
		}
		bounds[unit] = i + utf8.RuneLen(r)
	}
	return bounds
}

func byteSpanFromUTF16Bounds(bounds []int, offset, length int) (byteSpan, bool) {
	end := offset + length
	if offset < 0 || length <= 0 || end > len(bounds)-1 || bounds[offset] < 0 || bounds[end] < 0 {
		return byteSpan{}, false
	}
	return byteSpan{start: bounds[offset], end: bounds[end]}, true
}

func isUsernameByte(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9') || b == '_'
}

func tgPrivateMessageUpdates(event domain.UpdateEvent, msg domain.Message, randomID int64, includeMessageID bool, users []tg.UserClass, chats []tg.ChatClass) *tg.Updates {
	updates := make([]tg.UpdateClass, 0, 2)
	if includeMessageID {
		updates = append(updates, &tg.UpdateMessageID{ID: msg.ID, RandomID: randomID})
	}
	item := tgMessage(msg)
	if item == nil {
		item = &tg.MessageEmpty{ID: msg.ID}
	}
	updates = append(updates, &tg.UpdateNewMessage{
		Message:  item,
		Pts:      event.Pts,
		PtsCount: event.PtsCount,
	})
	if balance := tgGiftStarsBalanceUpdate(msg); balance != nil {
		updates = append(updates, balance)
	}
	date := event.Date
	if date == 0 {
		date = msg.Date
	}
	return &tg.Updates{
		Updates: updates,
		Users:   users,
		Chats:   chats,
		Date:    date,
		Seq:     0, // 私聊不维护账号级 seq，恒 0（客户端仅靠 pts 同步）
	}
}

func tgGiftStarsBalanceUpdate(msg domain.Message) tg.UpdateClass {
	if msg.Out || msg.Media == nil || msg.Media.ServiceAction == nil ||
		msg.Media.ServiceAction.Kind != domain.MessageServiceActionGiftStars {
		return nil
	}
	action := msg.Media.ServiceAction.GiftStars
	if action == nil || action.BalanceAfter < 0 {
		return nil
	}
	return &tg.UpdateStarsBalance{Balance: &tg.StarsAmount{Amount: action.BalanceAfter}}
}

// tgPrivateSendResultUpdates returns a complete send acknowledgement for exact
// random_id replays. DrKLO requires UpdateNewMessage in an Updates response to
// transition its local pending message to SENT. Visible edited messages use the
// current snapshot; deleted messages use the immutable first snapshot followed
// by the already-durable delete event, so the acknowledgement cannot become a
// permanent resurrection. No replay allocates pts or emits fan-out.
func tgPrivateSendResultUpdates(res domain.SendPrivateTextResult, randomID int64, includeMessageIDForNew bool, users []tg.UserClass, chats []tg.ChatClass) *tg.Updates {
	if !res.Duplicate {
		return tgPrivateMessageUpdates(res.SenderEvent, res.SenderMessage, randomID, includeMessageIDForNew, users, chats)
	}
	if randomID == 0 {
		randomID = res.SenderMessage.RandomID
	}
	out := tgPrivateMessageUpdates(res.SenderEvent, res.SenderMessage, randomID, randomID != 0, users, chats)
	if event := res.ReplayDeleteEvent; event != nil && event.Pts > 0 && len(event.MessageIDs) > 0 {
		out.Updates = append(out.Updates, &tg.UpdateDeleteMessages{
			Messages: append([]int(nil), event.MessageIDs...),
			Pts:      event.Pts,
			PtsCount: event.PtsCount,
		})
		if event.Date > out.Date {
			out.Date = event.Date
		}
	}
	return out
}

func (r *Router) usersForMessageUpdate(ctx context.Context, ownerUserID int64, msg domain.Message) []tg.UserClass {
	return r.usersForMessageUpdates(ctx, ownerUserID, []domain.Message{msg})
}

func (r *Router) usersForMessageUpdateWithPreloaded(ctx context.Context, ownerUserID int64, msg domain.Message, preloaded []domain.User) []tg.UserClass {
	return r.usersForMessageUpdatesWithPreloaded(ctx, ownerUserID, []domain.Message{msg}, preloaded)
}

func (r *Router) usersForMessageUpdates(ctx context.Context, ownerUserID int64, messages []domain.Message) []tg.UserClass {
	return r.usersForMessageUpdatesWithPreloaded(ctx, ownerUserID, messages, nil)
}

func (r *Router) usersForMessageUpdatesWithPreloaded(ctx context.Context, ownerUserID int64, messages []domain.Message, preloaded []domain.User) []tg.UserClass {
	seen := make(map[int64]struct{}, len(messages)*2)
	ids := make([]int64, 0, len(messages)*2)
	addID := func(id int64) {
		if id == 0 {
			return
		}
		if _, ok := seen[id]; ok {
			return
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	for _, msg := range messages {
		for _, id := range appendMessageUserIDs(nil, make(map[int64]struct{}), msg) {
			addID(id)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	loaded := make(map[int64]domain.User, len(ids))
	for _, user := range preloaded {
		if user.ID != 0 {
			loaded[user.ID] = user
		}
	}
	missing := make([]int64, 0, len(ids))
	for _, id := range ids {
		if isSystemUserID(id) {
			continue
		}
		if _, ok := loaded[id]; !ok {
			missing = append(missing, id)
		}
	}
	if r.deps.Users != nil && len(missing) > 0 {
		if users, err := r.deps.Users.ByIDs(ctx, ownerUserID, missing); err == nil {
			for _, user := range users {
				loaded[user.ID] = user
			}
		}
	}
	users := make([]tg.UserClass, 0, len(ids))
	for _, id := range ids {
		switch {
		case isSystemUserID(id):
			if u, ok := domain.SystemUserByID(id); ok {
				users = append(users, r.tgUser(u))
			}
		case id == ownerUserID:
			if user, ok := loaded[id]; ok {
				users = append(users, r.tgSelfUser(user))
			}
		default:
			if user, ok := loaded[id]; ok {
				users = append(users, r.tgUser(user))
			}
		}
	}
	r.applyUsernamesToPeerObjects(ctx, users, nil)
	return users
}

func (r *Router) chatsForMessageUpdate(ctx context.Context, ownerUserID int64, msg domain.Message) []tg.ChatClass {
	return r.chatsForMessageUpdates(ctx, ownerUserID, []domain.Message{msg})
}

func appendMessageUserIDs(ids []int64, seen map[int64]struct{}, msg domain.Message) []int64 {
	add := func(id int64) {
		if id == 0 {
			return
		}
		if _, ok := seen[id]; ok {
			return
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	for _, peer := range []domain.Peer{msg.From, msg.Peer} {
		if peer.Type == domain.PeerTypeUser {
			add(peer.ID)
		}
	}
	if msg.Forward != nil && msg.Forward.From.Type == domain.PeerTypeUser {
		add(msg.Forward.From.ID)
	}
	add(msg.ViaBotID)
	if msg.ReplyTo != nil && msg.ReplyTo.Peer.Type == domain.PeerTypeUser {
		add(msg.ReplyTo.Peer.ID)
	}
	if msg.Media != nil && msg.Media.Contact != nil {
		add(msg.Media.Contact.UserID)
	}
	userRefs := make(map[int64]struct{})
	channelRefs := make(map[int64]struct{})
	collectMessagePeerRefs(msg, 0, userRefs, channelRefs)
	extra := make([]int64, 0, len(userRefs))
	for id := range userRefs {
		extra = append(extra, id)
	}
	sort.Slice(extra, func(i, j int) bool { return extra[i] < extra[j] })
	for _, id := range extra {
		add(id)
	}
	return ids
}

func appendMessageChannelIDs(ids []int64, seen map[int64]struct{}, msg domain.Message) []int64 {
	add := func(id int64) {
		if id == 0 {
			return
		}
		if _, ok := seen[id]; ok {
			return
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	for _, peer := range []domain.Peer{msg.From, msg.Peer} {
		if peer.Type == domain.PeerTypeChannel {
			add(peer.ID)
		}
	}
	if msg.Forward != nil && msg.Forward.From.Type == domain.PeerTypeChannel {
		add(msg.Forward.From.ID)
	}
	if msg.ReplyTo != nil && msg.ReplyTo.Peer.Type == domain.PeerTypeChannel {
		add(msg.ReplyTo.Peer.ID)
	}
	userRefs := make(map[int64]struct{})
	channelRefs := make(map[int64]struct{})
	collectMessagePeerRefs(msg, 0, userRefs, channelRefs)
	extra := make([]int64, 0, len(channelRefs))
	for id := range channelRefs {
		extra = append(extra, id)
	}
	sort.Slice(extra, func(i, j int) bool { return extra[i] < extra[j] })
	for _, id := range extra {
		add(id)
	}
	return ids
}

func (r *Router) chatsForMessageUpdates(ctx context.Context, ownerUserID int64, messages []domain.Message) []tg.ChatClass {
	if r.deps.Channels == nil || len(messages) == 0 {
		return nil
	}
	seen := make(map[int64]struct{}, len(messages)*2)
	ids := make([]int64, 0, len(messages))
	for _, msg := range messages {
		ids = appendMessageChannelIDs(ids, seen, msg)
	}
	if len(ids) == 0 {
		return nil
	}
	views, err := r.deps.Channels.GetChannels(ctx, ownerUserID, ids)
	if err != nil {
		return nil
	}
	byID := make(map[int64]domain.ChannelView, len(views))
	for _, view := range views {
		if view.Channel.ID != 0 {
			byID[view.Channel.ID] = view
		}
	}
	chats := make([]tg.ChatClass, 0, len(ids))
	for _, id := range ids {
		if view, ok := byID[id]; ok {
			chats = append(chats, tgChannelChatForView(ownerUserID, view))
		}
	}
	return chats
}

// mentionUserIDsFromDomain 从 domain 实体与文本解析 @ 目标（转发/重放路径，
// mentionName 实体已携带解析好的 user_id）。
func (r *Router) mentionUserIDsFromDomain(ctx context.Context, currentUserID int64, message string, entities []domain.MessageEntity) []int64 {
	seen := make(map[int64]struct{})
	out := make([]int64, 0)
	add := func(id int64) {
		if id == 0 || len(out) >= domain.MaxChannelMentionRecipients {
			return
		}
		if _, ok := seen[id]; ok {
			return
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	for _, entity := range entities {
		if entity.Type == domain.MessageEntityMentionName {
			add(entity.UserID)
		}
	}
	if identity, ok := r.deps.Users.(UserIdentityService); ok && identity != nil {
		blocked := mentionScanBlockedSpansFromDomainEntities(message, entities)
		for _, username := range extractMentionUsernames(message, domain.MaxChannelMentionRecipients-len(out), blocked) {
			user, found, err := identity.ResolveUsername(ctx, currentUserID, username)
			if err != nil {
				if isMentionResolveMiss(err) {
					continue
				}
				break
			}
			if found {
				add(user.ID)
			}
		}
	}
	return out
}
