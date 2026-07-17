package rpc

import (
	"context"
	"errors"
	"github.com/iamxvbaba/td/tg"
	"strings"
	"telesrv/internal/domain"
	"unicode/utf8"
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
	// 频道私信(monoforum):仅当 reply_to 带 monoforum_peer_id 时走专用发送路径(普通发送恒不带,
	// 故此 gate 对普通发送零额外成本)。peer 解析为 monoforum 频道时按订阅者子会话发送。
	if monoforumReplyPresent(req.ReplyTo) {
		savedPeer, valid := r.monoforumReplyTargetPeer(userID, req.ReplyTo)
		if !valid || savedPeer.Type != domain.PeerTypeUser || savedPeer.ID == 0 {
			sendErr = replyToMonoforumPeerInvalidErr()
			return nil, sendErr
		}
		replay, err := r.lookupChannelSendReplay(ctx, userID, peer.ID, savedPeer, req.RandomID, idempotencyFingerprint)
		if err != nil {
			sendErr = err
			return nil, err
		}
		if replay.found {
			duplicate = true
			return r.monoforumSendUpdates(ctx, userID, replay.channel.Channel, savedPeer, replay.channel), nil
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
		updates, err := r.sendMonoforumMessage(ctx, userID, checkedPeer, req, idempotencyFingerprint, replay.checked)
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
	// reply_markup（bot inline keyboard）：仅 bot 账号发送被接受+校验；非 bot 静默丢弃。
	// 仅在请求携带 markup 时才查 is_bot，避免普通发送多打一次查询。
	var replyMarkup *domain.MessageReplyMarkup
	if req.ReplyMarkup != nil {
		replyMarkup, err = domainReplyMarkupForSender(req.ReplyMarkup, r.userIsBot(ctx, userID))
		if err != nil {
			sendErr = replyMarkupErr(err)
			return nil, sendErr
		}
	}
	// rich_message（Layer 227 富文本）：解析 blocks + 内嵌媒体快照；普通消息恒 nil。
	// Phase 1 仅认 inputRichMessage（blocks 形态），HTML/Markdown 变体返回错误。
	var richMessage *domain.MessageRichMessage
	if req.RichMessage != nil {
		richMessage, err = r.domainRichMessageFromInput(ctx, req.RichMessage)
		if err != nil {
			sendErr = err
			return nil, sendErr
		}
	}
	if req.Message == "" && richMessage == nil {
		sendErr = messageEmptyErr()
		return nil, sendErr
	}
	// 自动实体高亮：客户端未带 url/@mention/#hashtag/bot command 等「可自动识别」实体时，服务端
	// 检测原文补充（官方服务端行为），否则 @username/链接等不渲染为可点蓝色。富文本走独立结构，不处理。
	if richMessage == nil {
		req.Entities = augmentAutoEntities(req.Message, req.Entities)
	}
	// 链接预览：纯文本消息（私聊或频道）含可预览 URL 且未抑制时，挂 pending 占位，异步解析回填。
	// 富文本消息有独立媒体语义，不叠加。
	var previewMedia *domain.MessageMedia
	if richMessage == nil {
		previewMedia = r.webPageMediaFromText(ctx, req.Message, req.Entities, req.NoWebpage, req.InvertMedia)
	}
	if req.ScheduleDate != 0 && !scheduleDateIsImmediate(req.ScheduleDate, int(r.clock.Now().Unix())) {
		updates, err := r.scheduleOutgoing(ctx, userID, peer, outgoingSend{
			randomID:               req.RandomID,
			idempotencyFingerprint: idempotencyFingerprint,
			idempotencyPreflighted: replay.checked,
			message:                req.Message,
			entities:               req.Entities,
			media:                  previewMedia,
			silent:                 req.Silent,
			noforwards:             req.Noforwards,
			replyToInput:           req.ReplyTo,
			sendAsInput:            req.SendAs,
			clearDraft:             req.ClearDraft,
			richMessage:            richMessage,
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
		entities:               req.Entities,
		media:                  previewMedia,
		silent:                 req.Silent,
		noforwards:             req.Noforwards,
		replyToInput:           req.ReplyTo,
		sendAsInput:            req.SendAs,
		clearDraft:             req.ClearDraft,
		replyMarkup:            replyMarkup,
		richMessage:            richMessage,
		effect:                 req.Effect,
	})
	duplicate = dup
	if err != nil {
		sendErr = err
		return nil, sendErr
	}
	return updates, nil
}

func messageSendErr(err error) error {
	switch {
	case errors.Is(err, domain.ErrUserFrozen):
		return frozenMethodInvalidErr()
	case errors.Is(err, domain.ErrReplyMessageIDInvalid):
		return replyMessageIDInvalidErr()
	case errors.Is(err, domain.ErrMessageRandomIDDuplicate):
		return randomIDDuplicateErr()
	case errors.Is(err, domain.ErrMessageEmpty):
		return messageEmptyErr()
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
		parsed, err := r.checkedDomainPeerFromInputPeer(ctx, userID, inputPeer)
		if err != nil || parsed != peer {
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
	case req.AllowPaidStars > 0 || req.AllowPaidFloodskip:
		return paymentUnsupportedErr()
	case !req.SuggestedPost.Zero():
		return suggestedPostPeerInvalidErr()
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
		for _, username := range extractMentionUsernames(message, domain.MaxChannelMentionRecipients-len(out)) {
			user, found, err := identity.ResolveUsername(ctx, currentUserID, username)
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
	}
	return out, nil
}

func extractMentionUsernames(message string, limit int) []string {
	if limit <= 0 || message == "" {
		return nil
	}
	seen := make(map[string]struct{})
	out := make([]string, 0)
	for i := 0; i < len(message); i++ {
		if message[i] != '@' {
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
	seen := make(map[int64]struct{}, 2)
	users := make([]tg.UserClass, 0, 2)
	add := func(id int64) {
		if id == 0 {
			return
		}
		if _, ok := seen[id]; ok {
			return
		}
		seen[id] = struct{}{}
		switch {
		case isSystemUserID(id):
			if u, ok := domain.SystemUserByID(id); ok {
				users = append(users, r.tgUser(u))
			}
		case id == ownerUserID:
			if r.deps.Users == nil {
				return
			}
			u, err := r.deps.Users.Self(ctx, ownerUserID)
			if err == nil && u.ID != 0 {
				users = append(users, r.tgSelfUser(u))
			}
		default:
			if r.deps.Users == nil {
				return
			}
			u, found, err := r.deps.Users.ByID(ctx, ownerUserID, id)
			if err == nil && found {
				users = append(users, r.tgUser(u))
			}
		}
	}
	if msg.From.Type == domain.PeerTypeUser {
		add(msg.From.ID)
	}
	if msg.Peer.Type == domain.PeerTypeUser {
		add(msg.Peer.ID)
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
	return users
}

func (r *Router) usersForMessageUpdates(ctx context.Context, ownerUserID int64, messages []domain.Message) []tg.UserClass {
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
		if msg.From.Type == domain.PeerTypeUser {
			addID(msg.From.ID)
		}
		if msg.Peer.Type == domain.PeerTypeUser {
			addID(msg.Peer.ID)
		}
		if msg.Forward != nil && msg.Forward.From.Type == domain.PeerTypeUser {
			addID(msg.Forward.From.ID)
		}
		addID(msg.ViaBotID)
		if msg.ReplyTo != nil && msg.ReplyTo.Peer.Type == domain.PeerTypeUser {
			addID(msg.ReplyTo.Peer.ID)
		}
		if msg.Media != nil && msg.Media.Contact != nil {
			addID(msg.Media.Contact.UserID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	loaded := make(map[int64]domain.User, len(ids))
	if r.deps.Users != nil {
		if users, err := r.deps.Users.ByIDs(ctx, ownerUserID, ids); err == nil {
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
	return users
}

func (r *Router) chatsForMessageUpdate(ctx context.Context, ownerUserID int64, msg domain.Message) []tg.ChatClass {
	return r.chatsForMessageUpdates(ctx, ownerUserID, []domain.Message{msg})
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
	if msg.From.Type == domain.PeerTypeChannel {
		add(msg.From.ID)
	}
	if msg.Peer.Type == domain.PeerTypeChannel {
		add(msg.Peer.ID)
	}
	if msg.Forward != nil && msg.Forward.From.Type == domain.PeerTypeChannel {
		add(msg.Forward.From.ID)
	}
	if msg.ReplyTo != nil && msg.ReplyTo.Peer.Type == domain.PeerTypeChannel {
		add(msg.ReplyTo.Peer.ID)
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
		for _, username := range extractMentionUsernames(message, domain.MaxChannelMentionRecipients-len(out)) {
			user, found, err := identity.ResolveUsername(ctx, currentUserID, username)
			if err != nil {
				break
			}
			if found {
				add(user.ID)
			}
		}
	}
	return out
}
