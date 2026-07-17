package rpc

import (
	"context"
	cryptorand "crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap"

	"telesrv/internal/domain"
)

// 本文件实现 messages.uploadMedia / sendMedia / sendMultiMedia 的 photo/document/sticker 主路径，
// 并抽取 sendOutgoing 作为「已校验的一条出站消息（文本或媒体）落地」的共享实现，私聊与频道共用。

const maxContactVcardLength = 8192

// outgoingSend 是 sendOutgoing 的入参：一条已校验的出站消息。
type outgoingSend struct {
	randomID               int64
	idempotencyFingerprint []byte
	idempotencyPreflighted bool
	message                string
	entities               []tg.MessageEntityClass
	media                  *domain.MessageMedia
	silent                 bool
	noforwards             bool
	replyToInput           tg.InputReplyToClass
	sendAsInput            tg.InputPeerClass
	replyTo                *domain.MessageReply
	replyToReady           bool
	sendAs                 *domain.Peer
	sendAsReady            bool
	clearDraft             bool
	// replyMarkup 是 bot inline keyboard（已解析+校验；非 bot 恒 nil）。
	replyMarkup *domain.MessageReplyMarkup
	viaBotID    int64
	// richMessage 是 Layer 227 富文本消息快照（已解析内嵌媒体；普通消息恒 nil）。
	richMessage *domain.MessageRichMessage
	// groupedID 是相册分组 id：sendMultiMedia 同组各条共享一个非零值（客户端据此渲染
	// 成一个相册组）；单条发送恒 0。
	groupedID int64
	// effect 是消息特效 id（私聊专属，0 表无特效）。调用方已对 catalog 校验合法性；
	// 频道侧忽略（官方群/频道不渲染特效）。
	effect int64
}

// sendOutgoing 把一条出站消息落地到私聊或频道，返回 *tg.Updates、是否重复、错误。
// media 为空即纯文本。校验（长度/random_id/限流）由调用方完成。
func (r *Router) sendOutgoing(ctx context.Context, userID int64, peer domain.Peer, p outgoingSend) (tg.UpdatesClass, bool, error) {
	sendAs := p.sendAs
	if !p.sendAsReady {
		resolved, err := r.resolveSendAsPeer(ctx, userID, peer, p.sendAsInput)
		if err != nil {
			return nil, false, err
		}
		sendAs = resolved
	}
	if peer.Type == domain.PeerTypeChannel {
		if r.deps.Channels == nil {
			return nil, false, peerIDInvalidErr()
		}
		replyTo := p.replyTo
		if !p.replyToReady {
			resolved, err := r.messageReplyFromInput(ctx, userID, peer, p.replyToInput)
			if err != nil {
				return nil, false, err
			}
			replyTo = resolved
		}
		mentionUserIDs, err := r.mentionedUserIDsFromMessage(ctx, userID, p.message, p.entities)
		if err != nil {
			return nil, false, err
		}
		res, err := r.deps.Channels.SendMessage(ctx, userID, domain.SendChannelMessageRequest{
			UserID:                 userID,
			ChannelID:              peer.ID,
			RandomID:               p.randomID,
			IdempotencyFingerprint: p.idempotencyFingerprint,
			IdempotencyPreflighted: p.idempotencyPreflighted,
			Message:                p.message,
			Entities:               domainMessageEntitiesForViewer(userID, p.entities),
			Media:                  p.media,
			MentionUserIDs:         mentionUserIDs,
			SkipRecipientLookup:    true,
			PostAuthor:             r.channelPostAuthorName(ctx, userID),
			Silent:                 p.silent,
			NoForwards:             p.noforwards,
			ReplyTo:                replyTo,
			ViaBotID:               p.viaBotID,
			GroupedID:              p.groupedID,
			ReplyMarkup:            p.replyMarkup,
			RichMessage:            p.richMessage,
			SendAs:                 sendAs,
			Date:                   int(r.clock.Now().Unix()),
		})
		if err != nil {
			return nil, false, channelInvalidErr(err)
		}
		// 发送者 echo 走 rpc_result（同步，用独立 cache）；其余成员的 fan-out 异步化，
		// 移出发送者 RPC 同步路径（设计 Phase 0）。echo 与 fan-out 必须用各自独立的
		// viewerPeerCache——前者在 RPC goroutine、后者在 worker goroutine，共享会数据竞态。
		// 发送者 echo 走 rpc_result（同步，用独立 cache）；其余成员的 fan-out 异步化。
		// echo 与 fan-out 必须用各自独立的 viewerPeerCache——前者在 RPC goroutine、后者在
		// worker goroutine，共享会数据竞态。
		echoCache := newViewerPeerCache(r)
		updates := r.channelMessageUpdatesWithPeerCache(ctx, userID, res, p.randomID, echoCache)
		if !res.Duplicate {
			r.enqueueChannelMessageFanout(ctx, userID, res, nil)
			r.pushChannelDiscussionUpdate(ctx, userID, res.Discussion)
			// 频道链接预览 pending 占位：带外解析并就地替换（异步，不阻塞发送 echo）。
			r.maybeEnqueueWebPageResolve(userID, peer, res.Message.ID, res.Message.Media)
		}
		if p.clearDraft && !res.Duplicate {
			r.clearDraftAfterSend(ctx, userID, peer, replyTo)
		}
		return updates, res.Duplicate, nil
	}
	if peer.Type != domain.PeerTypeUser {
		return nil, false, peerIDInvalidErr()
	}
	if r.deps.Messages == nil {
		return nil, false, peerIDInvalidErr()
	}
	if r.deps.Users != nil && peer.ID != userID {
		if _, found, err := r.deps.Users.ByID(ctx, userID, peer.ID); err != nil {
			return nil, false, internalErr()
		} else if !found {
			return nil, false, peerIDInvalidErr()
		}
	}
	replyTo := p.replyTo
	if !p.replyToReady {
		resolved, err := r.messageReplyFromInput(ctx, userID, peer, p.replyToInput)
		if err != nil {
			return nil, false, err
		}
		replyTo = resolved
	}
	recipientBlocked, err := r.peerBlocksUser(ctx, userID, peer.ID)
	if err != nil {
		return nil, false, err
	}
	sessionID, _ := SessionIDFrom(ctx)
	// outbox 的排除键定位的是发起 RPC 的物理连接；PFS temp key 绑定后
	// AuthKeyIDFrom 是业务视角 perm key，不能用它代替连接实际 raw key。
	authKeyID := rawAuthKeyIDForOrigin(ctx)
	res, err := r.deps.Messages.SendPrivateText(ctx, userID, domain.SendPrivateTextRequest{
		SenderUserID:           userID,
		RecipientUserID:        peer.ID,
		RandomID:               p.randomID,
		Message:                p.message,
		Entities:               domainMessageEntitiesForViewer(userID, p.entities),
		Media:                  p.media,
		Silent:                 p.silent,
		NoForwards:             p.noforwards,
		ReplyTo:                replyTo,
		Date:                   int(r.clock.Now().Unix()),
		OriginAuthKeyID:        authKeyID,
		OriginSessionID:        sessionID,
		RecipientBlocked:       recipientBlocked,
		IdempotencyFingerprint: p.idempotencyFingerprint,
		IdempotencyPreflighted: p.idempotencyPreflighted,
		ReplyMarkup:            p.replyMarkup,
		RichMessage:            p.richMessage,
		ViaBotID:               p.viaBotID,
		GroupedID:              p.groupedID,
		Effect:                 p.effect,
	})
	if err != nil {
		fields := append(r.contextLogFields(ctx),
			zap.Error(err),
			zap.Int64("user_id", userID),
			zap.Int64("peer_user_id", peer.ID),
			zap.Int64("random_id", p.randomID),
			zap.Int("message_len", utf8.RuneCountInString(p.message)),
			zap.Bool("has_media", p.media != nil && !p.media.IsZero()),
			zap.Bool("has_rich_message", !p.richMessage.IsZero()),
			zap.Bool("has_reply_to", replyTo != nil),
			zap.Bool("clear_draft", p.clearDraft),
			zap.Int64("via_bot_id", p.viaBotID),
		)
		r.log.Warn("messages.sendMessage private store failed", fields...)
		return nil, false, messageSendErr(err)
	}
	var users []tg.UserClass
	var chats []tg.ChatClass
	if !res.Duplicate {
		users = r.usersForMessageUpdate(ctx, userID, res.SenderMessage)
		chats = r.chatsForMessageUpdate(ctx, userID, res.SenderMessage)
	}
	if p.clearDraft && !res.Duplicate {
		r.clearDraftAfterSend(ctx, userID, peer, replyTo)
	}
	if !res.Duplicate {
		// 链接预览 pending 占位：带外解析并就地替换（异步，不阻塞发送 echo）。
		r.maybeEnqueueWebPageResolve(userID, peer, res.SenderMessage.ID, res.SenderMessage.Media)
		r.enqueueBotAPIPrivateMessageUpdateAsync(ctx, res)
	}
	return tgPrivateSendResultUpdates(res, p.randomID, true, users, chats), res.Duplicate, nil
}

// onMessagesUploadMedia 解析 InputMedia（上传或引用），返回可复用的 tg.MessageMedia。
func (r *Router) onMessagesUploadMedia(ctx context.Context, req *tg.MessagesUploadMediaRequest) (tg.MessageMediaClass, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	if userID == 0 {
		return nil, mediaInvalidErr()
	}
	if len(req.BusinessConnectionID) > maxBusinessConnIDLength {
		return nil, limitInvalidErr()
	}
	if _, ok := req.Peer.(*tg.InputPeerEmpty); !ok {
		if _, err := r.checkedDomainPeerFromInputPeer(ctx, userID, req.Peer); err != nil {
			return nil, err
		}
	}
	if _, ok := req.Media.(*tg.InputMediaEmpty); ok {
		return &tg.MessageMediaEmpty{}, nil
	}
	media, err := r.resolveInputMedia(ctx, userID, req.Media)
	if err != nil {
		return nil, err
	}
	if media == nil {
		return nil, mediaInvalidErr()
	}
	return tgMessageMedia(media), nil
}

// onMessagesSendMedia 发送一条带媒体的消息（photo/document/sticker），私聊与频道均支持。
func (r *Router) onMessagesSendMedia(ctx context.Context, req *tg.MessagesSendMediaRequest) (tg.UpdatesClass, error) {
	if req.RandomID == 0 {
		return nil, randomIDEmptyErr()
	}
	if utf8.RuneCountInString(req.Message) > maxSendMessageTextLength {
		return nil, mediaCaptionTooLongErr()
	}
	if len(req.Entities) > maxMessageEntityCount {
		return nil, limitInvalidErr()
	}
	if req.ScheduleRepeatPeriod != 0 {
		return nil, scheduleDateInvalidErr()
	}
	if req.Media == nil {
		return nil, mediaInvalidErr()
	}
	// InputMediaEmpty / WebPage：退化为纯文本发送（复用 sendMessage 校验与流程，含 url 实体补全）。
	switch req.Media.(type) {
	case *tg.InputMediaEmpty, *tg.InputMediaWebPage:
		return r.onMessagesSendMessage(ctx, sendMessageRequestFromSendMedia(req))
	}
	// 指纹是幂等校验元数据，不能让一个本应由 media resolver 映射成
	// MEDIA_INVALID 的畸形 input 因 Encode 失败提前变成 INTERNAL。合法请求会得到
	// 原始 TL 指纹；编码失败则留空，由 store 使用 domain fallback。
	idempotencyFingerprint, _ := sendMediaIdempotencyFingerprint(req)
	// 媒体 caption 里的链接/@mention/#hashtag 等同样补自动高亮实体（客户端未带时）。
	req.Entities = augmentAutoEntities(req.Message, req.Entities)
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	if userID == 0 {
		return nil, peerIDInvalidErr()
	}
	peer, ok := r.domainPeerFromInputPeer(userID, req.Peer)
	if !ok || peer.ID == 0 {
		return nil, peerIDInvalidErr()
	}
	replay, err := r.lookupOutgoingReplay(ctx, userID, peer, req.RandomID, idempotencyFingerprint)
	if err != nil {
		return nil, err
	}
	if replay.found {
		return r.outgoingReplayUpdates(ctx, userID, peer, req.RandomID, replay), nil
	}
	// 消息特效：仅接受 catalog 内的合法 effect id（非法 id → EFFECT_ID_INVALID，官方行为）。
	if r.messageEffectInvalid(ctx, req.Effect) {
		return nil, effectIDInvalidErr()
	}
	if err := r.checkSendRateLimit(ctx, userID, 1); err != nil {
		return nil, err
	}
	peer, err = r.checkedDomainPeerFromInputPeer(ctx, userID, req.Peer)
	if err != nil {
		return nil, err
	}
	media, err := r.resolveInputMedia(ctx, userID, req.Media)
	if err != nil {
		return nil, err
	}
	if media == nil {
		return nil, mediaInvalidErr()
	}
	// reply_markup（bot inline keyboard on media）：仅 bot 接受+校验，非 bot 静默丢弃。
	var replyMarkup *domain.MessageReplyMarkup
	if req.ReplyMarkup != nil {
		replyMarkup, err = domainReplyMarkupForSender(req.ReplyMarkup, r.userIsBot(ctx, userID))
		if err != nil {
			return nil, replyMarkupErr(err)
		}
	}
	if req.ScheduleDate != 0 && !scheduleDateIsImmediate(req.ScheduleDate, int(r.clock.Now().Unix())) {
		return r.scheduleOutgoing(ctx, userID, peer, outgoingSend{
			randomID:               req.RandomID,
			idempotencyFingerprint: idempotencyFingerprint,
			idempotencyPreflighted: replay.checked,
			message:                req.Message,
			entities:               req.Entities,
			media:                  media,
			silent:                 req.Silent,
			noforwards:             req.Noforwards,
			replyToInput:           req.ReplyTo,
			sendAsInput:            req.SendAs,
			clearDraft:             req.ClearDraft,
		}, req.ScheduleDate, req.ScheduleRepeatPeriod)
	}
	updates, _, err := r.sendOutgoing(ctx, userID, peer, outgoingSend{
		randomID:               req.RandomID,
		idempotencyFingerprint: idempotencyFingerprint,
		idempotencyPreflighted: replay.checked,
		message:                req.Message,
		entities:               req.Entities,
		media:                  media,
		silent:                 req.Silent,
		noforwards:             req.Noforwards,
		replyToInput:           req.ReplyTo,
		sendAsInput:            req.SendAs,
		clearDraft:             req.ClearDraft,
		replyMarkup:            replyMarkup,
		effect:                 req.Effect,
	})
	if err != nil {
		return nil, err
	}
	return updates, nil
}

// onMessagesSendMultiMedia 发送相册（多条媒体），并在解析媒体前持久预留 grouped_id。
func (r *Router) onMessagesSendMultiMedia(ctx context.Context, req *tg.MessagesSendMultiMediaRequest) (tg.UpdatesClass, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	if userID == 0 {
		return nil, peerIDInvalidErr()
	}
	if len(req.MultiMedia) == 0 || len(req.MultiMedia) > maxSendMultiMediaItems {
		return nil, limitInvalidErr()
	}
	peer, ok := r.domainPeerFromInputPeer(userID, req.Peer)
	if !ok || peer.ID == 0 {
		return nil, peerIDInvalidErr()
	}
	randomIDs := make(map[int64]struct{}, len(req.MultiMedia))
	reservationItems := make([]domain.AlbumGroupReservationItem, 0, len(req.MultiMedia))
	for _, item := range req.MultiMedia {
		if item.RandomID == 0 {
			return nil, randomIDEmptyErr()
		}
		if _, duplicate := randomIDs[item.RandomID]; duplicate {
			return nil, randomIDDuplicateErr()
		}
		randomIDs[item.RandomID] = struct{}{}
		if utf8.RuneCountInString(item.Message) > maxSendMessageTextLength {
			return nil, mediaCaptionTooLongErr()
		}
		if len(item.Entities) > maxMessageEntityCount {
			return nil, limitInvalidErr()
		}
		if item.Media == nil {
			return nil, mediaInvalidErr()
		}
		intentHash, fingerprintErr := sendMultiMediaItemIdempotencyFingerprint(req, item)
		if fingerprintErr != nil {
			// 畸形 InputMedia 的 TL 编码失败不能变成 INTERNAL；保持 media 输入错误语义。
			return nil, mediaInvalidErr()
		}
		reservationItems = append(reservationItems, domain.AlbumGroupReservationItem{
			RandomID:   item.RandomID,
			IntentHash: intentHash,
		})
	}
	replays := make([]outgoingReplayLookup, len(req.MultiMedia))
	absentCount := 0
	for i, item := range req.MultiMedia {
		replay, err := r.lookupOutgoingReplay(ctx, userID, peer, item.RandomID, reservationItems[i].IntentHash)
		if err != nil {
			return nil, err
		}
		replays[i] = replay
		if !replay.found {
			absentCount++
		}
	}
	if absentCount == 0 {
		results := make([]tg.UpdatesClass, 0, len(req.MultiMedia))
		for i, item := range req.MultiMedia {
			results = append(results, r.outgoingReplayUpdates(ctx, userID, peer, item.RandomID, replays[i]))
		}
		return combineSendUpdates(results), nil
	}
	if err := r.checkSendRateLimit(ctx, userID, absentCount); err != nil {
		return nil, err
	}
	peer, err = r.checkedDomainPeerFromInputPeer(ctx, userID, req.Peer)
	if err != nil {
		return nil, err
	}

	// 必须在 resolveInputMedia 或发送任何 item 之前原子预留：首次请求若在第 N 条
	// 失败，客户端只重试失败子集时仍从已绑定 random_id 恢复整包 grouped_id。
	groupedID, err := r.reserveAlbumGroup(ctx, userID, peer, reservationItems)
	if err != nil {
		return nil, err
	}
	for _, replay := range replays {
		if replay.found {
			messageGroupedID := replay.private.SenderMessage.GroupedID
			if peer.Type == domain.PeerTypeChannel {
				messageGroupedID = replay.channel.Message.GroupedID
			}
			if messageGroupedID != groupedID {
				return nil, internalErr()
			}
		}
	}

	results := make([]tg.UpdatesClass, 0, len(req.MultiMedia))
	clearDraftPending := req.ClearDraft
	for i, item := range req.MultiMedia {
		idempotencyFingerprint := reservationItems[i].IntentHash
		if replays[i].found {
			results = append(results, r.outgoingReplayUpdates(ctx, userID, peer, item.RandomID, replays[i]))
			continue
		}
		media, err := r.resolveInputMedia(ctx, userID, item.Media)
		if err != nil {
			return nil, err
		}
		if media == nil {
			return nil, mediaInvalidErr()
		}
		p := outgoingSend{
			randomID:               item.RandomID,
			idempotencyFingerprint: idempotencyFingerprint,
			idempotencyPreflighted: replays[i].checked,
			message:                item.Message,
			entities:               augmentAutoEntities(item.Message, item.Entities),
			media:                  media,
			silent:                 req.Silent,
			noforwards:             req.Noforwards,
			replyToInput:           req.ReplyTo,
			sendAsInput:            req.SendAs,
			clearDraft:             clearDraftPending,
			groupedID:              groupedID,
		}
		var result tg.UpdatesClass
		duplicate := false
		if req.ScheduleDate != 0 && !scheduleDateIsImmediate(req.ScheduleDate, int(r.clock.Now().Unix())) {
			result, err = r.scheduleOutgoing(ctx, userID, peer, p, req.ScheduleDate, 0)
		} else {
			result, duplicate, err = r.sendOutgoing(ctx, userID, peer, p)
		}
		if err != nil {
			return nil, err
		}
		if p.clearDraft && !duplicate {
			clearDraftPending = false
		}
		results = append(results, result)
	}
	return combineSendUpdates(results), nil
}

// resolveInputMedia 把 tg.InputMedia 解析为 domain.MessageMedia（上传则落库，引用则加载）。
// 返回 nil 表示 InputMediaEmpty（调用方退化为纯文本）。
func (r *Router) resolveInputMedia(ctx context.Context, userID int64, input tg.InputMediaClass) (*domain.MessageMedia, error) {
	switch in := input.(type) {
	case *tg.InputMediaEmpty:
		return nil, nil
	case *tg.InputMediaContact:
		if !validContactInput(in.PhoneNumber, in.FirstName, in.LastName, "", 0) || utf8.RuneCountInString(in.Vcard) > maxContactVcardLength {
			return nil, mediaInvalidErr()
		}
		if strings.TrimSpace(in.PhoneNumber) == "" && strings.TrimSpace(in.FirstName) == "" && strings.TrimSpace(in.LastName) == "" && strings.TrimSpace(in.Vcard) == "" {
			return nil, mediaEmptyErr()
		}
		return &domain.MessageMedia{
			Kind: domain.MessageMediaKindContact,
			Contact: &domain.MessageContact{
				PhoneNumber: in.PhoneNumber,
				FirstName:   in.FirstName,
				LastName:    in.LastName,
				Vcard:       in.Vcard,
				UserID:      r.messageContactUserID(ctx, userID, in.PhoneNumber),
			},
		}, nil
	case *tg.InputMediaUploadedPhoto:
		if r.deps.Files == nil {
			return nil, mediaInvalidErr()
		}
		if in.File == nil {
			return nil, mediaInvalidErr()
		}
		ref, ok := uploadedFileRef(userID, in.File)
		if !ok {
			return nil, fileReferenceInvalidErr()
		}
		photo, err := r.deps.Files.CreatePhotoFromUpload(ctx, ref)
		if err != nil {
			return nil, mediaUploadErr(err)
		}
		return &domain.MessageMedia{Kind: domain.MessageMediaKindPhoto, Photo: &photo, Spoiler: in.Spoiler, TTLSeconds: in.TTLSeconds}, nil
	case *tg.InputMediaUploadedDocument:
		if r.deps.Files == nil {
			return nil, mediaInvalidErr()
		}
		if in.File == nil {
			return nil, mediaInvalidErr()
		}
		ref, ok := uploadedFileRef(userID, in.File)
		if !ok {
			return nil, fileReferenceInvalidErr()
		}
		spec := domain.DocumentSpec{
			MimeType:     in.MimeType,
			Attributes:   domainDocumentAttributes(in.Attributes),
			ForceFile:    in.ForceFile,
			NosoundVideo: in.NosoundVideo,
		}
		if thumb, ok := in.GetThumb(); ok {
			if tref, ok := uploadedFileRef(userID, thumb); ok {
				spec.Thumb = &tref
			}
		}
		doc, err := r.deps.Files.CreateDocumentFromUpload(ctx, ref, spec)
		if err != nil {
			return nil, mediaUploadErr(err)
		}
		return messageMediaFromDocument(doc, in.Spoiler, in.TTLSeconds), nil
	case *tg.InputMediaPhotoExternal:
		// 外链图片：服务端 SSRF 安全抓取 URL 并铸造 Photo。未启用/拦截/上游失败统一 MEDIA_INVALID。
		if r.deps.Files == nil {
			return nil, mediaInvalidErr()
		}
		photo, err := r.deps.Files.CreatePhotoFromURL(ctx, in.URL)
		if err != nil {
			return nil, mediaInvalidErr()
		}
		return &domain.MessageMedia{Kind: domain.MessageMediaKindPhoto, Photo: &photo, Spoiler: in.Spoiler, TTLSeconds: in.TTLSeconds}, nil
	case *tg.InputMediaDocumentExternal:
		// 外链文档：抓取 URL，mime 取 Content-Type、文件名取 URL basename。video_cover/timestamp 暂忽略。
		if r.deps.Files == nil {
			return nil, mediaInvalidErr()
		}
		doc, err := r.deps.Files.CreateDocumentFromURL(ctx, in.URL)
		if err != nil {
			return nil, mediaInvalidErr()
		}
		return messageMediaFromDocument(doc, in.Spoiler, in.TTLSeconds), nil
	case *tg.InputMediaPhoto:
		if r.deps.Files == nil {
			return nil, mediaInvalidErr()
		}
		photoID, ok := inputPhotoID(in.ID)
		if !ok {
			return nil, photoInvalidErr()
		}
		photo, found, err := r.deps.Files.GetPhoto(ctx, photoID)
		if err != nil {
			return nil, internalErr()
		}
		if !found {
			return nil, photoInvalidErr()
		}
		return &domain.MessageMedia{Kind: domain.MessageMediaKindPhoto, Photo: &photo, Spoiler: in.Spoiler, TTLSeconds: in.TTLSeconds}, nil
	case *tg.InputMediaDocument:
		if r.deps.Files == nil {
			return nil, mediaInvalidErr()
		}
		docIDs, ok := inputDocumentCandidateIDs(in.ID)
		if !ok {
			r.log.Warn("sendMedia InputMediaDocument unresolvable id", zap.String("id_type", fmt.Sprintf("%T", in.ID)))
			return nil, mediaInvalidErr()
		}
		var doc domain.Document
		found := false
		for _, docID := range docIDs {
			var err error
			doc, found, err = r.deps.Files.GetDocument(ctx, docID)
			if err != nil {
				return nil, internalErr()
			}
			if found {
				break
			}
		}
		if !found {
			r.log.Warn("sendMedia references unknown document", zap.Int64s("doc_ids", docIDs), zap.Int64("user_id", userID))
			return nil, mediaInvalidErr()
		}
		return messageMediaFromDocument(doc, in.Spoiler, in.TTLSeconds), nil
	case *tg.InputMediaGeoPoint:
		geo, err := domainGeoPointFromInput(in.GeoPoint)
		if err != nil {
			return nil, err
		}
		return &domain.MessageMedia{Kind: domain.MessageMediaKindGeo, Geo: geo}, nil
	case *tg.InputMediaVenue:
		geo, err := domainGeoPointFromInput(in.GeoPoint)
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(in.Title) == "" {
			return nil, mediaEmptyErr()
		}
		if utf8.RuneCountInString(in.Title) > maxVenueTitleLength ||
			utf8.RuneCountInString(in.Address) > maxVenueAddressLength ||
			utf8.RuneCountInString(in.Provider) > maxVenueProviderLength ||
			utf8.RuneCountInString(in.VenueID) > maxVenueIDLength ||
			utf8.RuneCountInString(in.VenueType) > maxVenueIDLength {
			return nil, mediaInvalidErr()
		}
		return &domain.MessageMedia{Kind: domain.MessageMediaKindVenue, Venue: &domain.MessageVenue{
			Geo:       *geo,
			Title:     in.Title,
			Address:   in.Address,
			Provider:  in.Provider,
			VenueID:   in.VenueID,
			VenueType: in.VenueType,
		}}, nil
	case *tg.InputMediaDice:
		emoticon := normalizeDiceEmoticon(in.Emoticon)
		sides, ok := diceValueSides(emoticon)
		if !ok {
			return nil, emoticonInvalidErr()
		}
		value, err := randomDiceValue(sides)
		if err != nil {
			return nil, internalErr()
		}
		return &domain.MessageMedia{Kind: domain.MessageMediaKindDice, Dice: &domain.MessageDice{
			Emoticon: emoticon,
			Value:    value,
		}}, nil
	case *tg.InputMediaGeoLive:
		if in.Stopped {
			// stopped 只在 editMessage 停止共享时有意义，发送即停没有客户端路径。
			return nil, mediaInvalidErr()
		}
		geo, err := domainGeoPointFromInput(in.GeoPoint)
		if err != nil {
			return nil, err
		}
		live := &domain.MessageGeoLive{Geo: *geo, Period: minLiveLocationPeriod}
		if period, ok := in.GetPeriod(); ok {
			if period != foreverLiveLocationPeriod && (period < minLiveLocationPeriod || period > maxLiveLocationPeriod) {
				return nil, mediaInvalidErr()
			}
			live.Period = period
		}
		if heading, ok := in.GetHeading(); ok {
			if heading < 0 || heading > maxLiveLocationHeading {
				return nil, mediaInvalidErr()
			}
			live.Heading = heading
		}
		if radius, ok := in.GetProximityNotificationRadius(); ok {
			if radius < 0 || radius > maxProximityRadiusMeters {
				return nil, mediaInvalidErr()
			}
			live.ProximityNotificationRadius = radius
		}
		return &domain.MessageMedia{Kind: domain.MessageMediaKindGeoLive, GeoLive: live}, nil
	case *tg.InputMediaPoll:
		if r.deps.Polls == nil {
			return nil, mediaInvalidErr()
		}
		pollID, err := randomPollID()
		if err != nil {
			return nil, internalErr()
		}
		snapshot, def, err := r.domainPollFromInputMedia(ctx, in, userID, pollID, int(r.clock.Now().Unix()))
		if err != nil {
			// compat 诊断：客户端 poll 构造形状多变（flag 默认值/答案构造器随版本漂移），
			// 拒绝时必须留痕，否则只有一个裸 4xx 无从对账。
			answerTypes := make([]string, 0, len(in.Poll.Answers))
			for _, answerClass := range in.Poll.Answers {
				switch answer := answerClass.(type) {
				case *tg.PollAnswer:
					answerTypes = append(answerTypes, fmt.Sprintf("pollAnswer{flags:%#x,text:%d,option:%d,media:%T}",
						uint32(answer.Flags), len(answer.Text.Text), len(answer.Option), answer.Media))
				case *tg.InputPollAnswer:
					media, hasMedia := answer.GetMedia()
					answerTypes = append(answerTypes, fmt.Sprintf("inputPollAnswer{flags:%#x,text:%d,has_media:%v,media:%T}",
						uint32(answer.Flags), len(answer.Text.Text), hasMedia, media))
				default:
					answerTypes = append(answerTypes, fmt.Sprintf("%T", answerClass))
				}
			}
			closePeriod, hasPeriod := in.Poll.GetClosePeriod()
			closeDate, hasDate := in.Poll.GetCloseDate()
			correct, hasCorrect := in.GetCorrectAnswers()
			solution, hasSolution := in.GetSolution()
			r.log.Warn("sendMedia poll rejected",
				zap.Error(err),
				zap.Int64("user_id", userID),
				zap.String("input_flags", fmt.Sprintf("%#x", uint32(in.Flags))),
				zap.String("poll_flags", fmt.Sprintf("%#x", uint32(in.Poll.Flags))),
				zap.Bool("quiz", in.Poll.Quiz),
				zap.Bool("multiple_choice", in.Poll.MultipleChoice),
				zap.Bool("public_voters", in.Poll.PublicVoters),
				zap.Bool("open_answers", in.Poll.OpenAnswers),
				zap.Bool("shuffle_answers", in.Poll.ShuffleAnswers),
				zap.Bool("revoting_disabled", in.Poll.RevotingDisabled),
				zap.Bool("hide_results", in.Poll.HideResultsUntilClose),
				zap.Bool("subscribers_only", in.Poll.SubscribersOnly),
				zap.Int("countries", len(in.Poll.CountriesISO2)),
				zap.Int("question_len", len(in.Poll.Question.Text)),
				zap.Int("question_entities", len(in.Poll.Question.Entities)),
				zap.Strings("answer_types", answerTypes),
				zap.Bool("has_close_period", hasPeriod), zap.Int("close_period", closePeriod),
				zap.Bool("has_close_date", hasDate), zap.Int("close_date", closeDate),
				zap.Bool("has_correct", hasCorrect), zap.Ints("correct", correct),
				zap.Bool("has_solution", hasSolution), zap.Int("solution_len", len(solution)),
			)
			return nil, err
		}
		// 权威行先落库（消息发送失败产生的孤儿 poll 无害且可回收）。
		if err := r.deps.Polls.CreatePoll(ctx, def); err != nil {
			r.log.Warn("create poll failed", zap.Error(err))
			return nil, internalErr()
		}
		return &domain.MessageMedia{Kind: domain.MessageMediaKindPoll, Poll: snapshot}, nil
	case *tg.InputMediaTodo:
		todo, err := domainTodoFromInput(in.Todo)
		if err != nil {
			return nil, err
		}
		return &domain.MessageMedia{Kind: domain.MessageMediaKindTodo, Todo: todo}, nil
	case *tg.InputMediaStory:
		return r.domainMessageStoryFromInput(ctx, userID, in)
	default:
		// poll（独立链路见 messages_polls.go）/ geoLive / todo / game / invoice /
		// paid media / external 等未接入；范围与 stub 决策见 docs/compatibility-matrix.md。
		return nil, mediaInvalidErr()
	}
}

func (r *Router) domainMessageStoryFromInput(ctx context.Context, userID int64, in *tg.InputMediaStory) (*domain.MessageMedia, error) {
	if in == nil || in.ID <= 0 || in.ID > domain.MaxStoryID {
		return nil, storyIDInvalidErr()
	}
	peer, err := r.checkedDomainPeerFromInputPeer(ctx, userID, in.Peer)
	if err != nil {
		return nil, err
	}
	if r.deps.Stories == nil {
		return nil, storyIDInvalidErr()
	}
	now := int(r.clock.Now().Unix())
	list, err := r.deps.Stories.GetStoriesByID(ctx, userID, peer, []int{in.ID}, now)
	if err != nil {
		return nil, storyErr(err)
	}
	if len(list.Stories) != 1 || list.Stories[0].Owner != peer || list.Stories[0].ID != in.ID {
		return nil, storyIDInvalidErr()
	}
	story := list.Stories[0]
	if story.NoForwards {
		return nil, chatForwardsRestrictedErr()
	}
	return &domain.MessageMedia{
		Kind: domain.MessageMediaKindStory,
		Story: &domain.MessageStory{
			Peer:  peer,
			ID:    in.ID,
			Story: &story,
		},
	}, nil
}

// domainGeoPointFromInput 校验并转换 InputGeoPoint；空点返回 MEDIA_EMPTY，越界返回 MEDIA_INVALID。
// access_hash 在此随机生成：客户端会把它原样带回 upload.getWebFile 的地图缩略请求，但
// 服务端地图渲染不依赖鉴权（详见 upload_webfile.go），故无需持久化校验。
func domainGeoPointFromInput(input tg.InputGeoPointClass) (*domain.MessageGeoPoint, error) {
	point, ok := input.(*tg.InputGeoPoint)
	if !ok || point == nil {
		return nil, mediaEmptyErr()
	}
	if point.Lat < -90 || point.Lat > 90 || point.Long < -180 || point.Long > 180 {
		return nil, mediaInvalidErr()
	}
	accuracy, _ := point.GetAccuracyRadius()
	if accuracy < 0 || accuracy > maxGeoAccuracyRadiusMeters {
		accuracy = 0
	}
	hash, err := randomGeoAccessHash()
	if err != nil {
		return nil, internalErr()
	}
	return &domain.MessageGeoPoint{
		Lat:            point.Lat,
		Long:           point.Long,
		AccessHash:     hash,
		AccuracyRadius: accuracy,
	}, nil
}

func randomGeoAccessHash() (int64, error) {
	var b [8]byte
	if _, err := cryptorand.Read(b[:]); err != nil {
		return 0, err
	}
	v := int64(binary.LittleEndian.Uint64(b[:]))
	if v == 0 {
		v = 1
	}
	return v, nil
}

// randomPollID 生成正的 63 位随机 poll id；polls 主键冲突概率可忽略（冲突时 INSERT 报错重试由客户端承担）。
func randomPollID() (int64, error) {
	var b [8]byte
	if _, err := cryptorand.Read(b[:]); err != nil {
		return 0, err
	}
	v := int64(binary.LittleEndian.Uint64(b[:]) >> 1)
	if v == 0 {
		v = 1
	}
	return v, nil
}

// normalizeDiceEmoticon 去掉 emoji 变体选择符（U+FE0F）：TDesktop 的 ⚽️ 与 ⚽ 都允许发送，
// 但 dice 贴纸系统集 key 与官方回包都是裸码点形态。
func normalizeDiceEmoticon(emoticon string) string {
	return strings.ReplaceAll(emoticon, "️", "")
}

// diceValueSides 返回 emoticon 对应的取值上限（值域 [1, sides]，与官方一致）。
// 列表必须与 appConfig 的 emojies_send_dice 保持同步（客户端据其决定单 emoji 是否转 dice）。
func diceValueSides(emoticon string) (int, bool) {
	switch emoticon {
	case "\U0001F3B2", "\U0001F3AF", "\U0001F3B3": // 🎲 🎯 🎳
		return 6, true
	case "\U0001F3C0", "⚽": // 🏀 ⚽
		return 5, true
	case "\U0001F3B0": // 🎰
		return 64, true
	default:
		return 0, false
	}
}

func randomDiceValue(sides int) (int, error) {
	if sides <= 0 {
		return 0, fmt.Errorf("dice sides must be positive")
	}
	// 拒绝采样消除模偏差；sides ≤ 64，单字节足够。
	max := 256 - 256%sides
	var b [1]byte
	for {
		if _, err := cryptorand.Read(b[:]); err != nil {
			return 0, err
		}
		if int(b[0]) < max {
			return 1 + int(b[0])%sides, nil
		}
	}
}

func (r *Router) messageContactUserID(ctx context.Context, userID int64, phone string) int64 {
	if r.deps.Users == nil || strings.TrimSpace(phone) == "" {
		return 0
	}
	identity, ok := r.deps.Users.(UserIdentityService)
	if !ok {
		return 0
	}
	u, found, err := identity.ResolvePhone(ctx, userID, phone)
	if err != nil || !found {
		return 0
	}
	return u.ID
}

// messageMediaFromDocument 由 Document 构造 MessageMedia，并从属性推导 Video/Round/Voice 标志。
func messageMediaFromDocument(doc domain.Document, spoiler bool, ttl int) *domain.MessageMedia {
	media := &domain.MessageMedia{Kind: domain.MessageMediaKindDocument, Document: &doc, Spoiler: spoiler, TTLSeconds: ttl}
	if doc.IsSticker() {
		media.Nopremium = true
	}
	for _, attr := range doc.Attributes {
		switch attr.Kind {
		case domain.DocAttrVideo:
			media.Video = true
			if attr.RoundMessage {
				media.Round = true
			}
		case domain.DocAttrAudio:
			if attr.Voice {
				media.Voice = true
			}
		}
	}
	return media
}

func inputPhotoID(input tg.InputPhotoClass) (int64, bool) {
	if p, ok := input.(*tg.InputPhoto); ok && p != nil && p.ID != 0 {
		return p.ID, true
	}
	return 0, false
}

func inputDocumentID(input tg.InputDocumentClass) (int64, bool) {
	if d, ok := input.(*tg.InputDocument); ok && d != nil && d.ID != 0 {
		return d.ID, true
	}
	return 0, false
}

func inputDocumentCandidateIDs(input tg.InputDocumentClass) ([]int64, bool) {
	if d, ok := input.(*tg.InputDocument); ok && d != nil && d.ID != 0 {
		return []int64{d.ID}, true
	}
	return nil, false
}

// domainDocumentAttributes 把 tg.DocumentAttribute 反向转为 domain（InputMediaUploadedDocument 用）。
func domainDocumentAttributes(attrs []tg.DocumentAttributeClass) []domain.DocumentAttribute {
	out := make([]domain.DocumentAttribute, 0, len(attrs))
	for _, a := range attrs {
		switch v := a.(type) {
		case *tg.DocumentAttributeImageSize:
			out = append(out, domain.DocumentAttribute{Kind: domain.DocAttrImageSize, W: v.W, H: v.H})
		case *tg.DocumentAttributeAnimated:
			out = append(out, domain.DocumentAttribute{Kind: domain.DocAttrAnimated})
		case *tg.DocumentAttributeSticker:
			attr := domain.DocumentAttribute{Kind: domain.DocAttrSticker, Alt: v.Alt, Mask: v.Mask}
			if id, hash, ok := inputStickerSetIDs(v.Stickerset); ok {
				attr.StickerSetID = id
				attr.StickerSetAccessHash = hash
			}
			out = append(out, attr)
		case *tg.DocumentAttributeVideo:
			out = append(out, domain.DocumentAttribute{Kind: domain.DocAttrVideo, W: v.W, H: v.H, Duration: v.Duration, RoundMessage: v.RoundMessage, SupportsStreaming: v.SupportsStreaming, NoSound: v.Nosound, VideoCodec: v.VideoCodec})
		case *tg.DocumentAttributeAudio:
			out = append(out, domain.DocumentAttribute{Kind: domain.DocAttrAudio, AudioDuration: v.Duration, Voice: v.Voice, Title: v.Title, Performer: v.Performer, Waveform: v.Waveform})
		case *tg.DocumentAttributeFilename:
			out = append(out, domain.DocumentAttribute{Kind: domain.DocAttrFilename, FileName: v.FileName})
		case *tg.DocumentAttributeCustomEmoji:
			attr := domain.DocumentAttribute{Kind: domain.DocAttrCustomEmoji, Alt: v.Alt, Free: v.Free, TextColor: v.TextColor}
			if id, hash, ok := inputStickerSetIDs(v.Stickerset); ok {
				attr.StickerSetID = id
				attr.StickerSetAccessHash = hash
			}
			out = append(out, attr)
		}
	}
	return out
}

func inputStickerSetIDs(input tg.InputStickerSetClass) (int64, int64, bool) {
	if s, ok := input.(*tg.InputStickerSetID); ok {
		return s.ID, s.AccessHash, true
	}
	return 0, 0, false
}

// sendMessageRequestFromSendMedia 把 sendMedia（空媒体）的字段映射到 sendMessage 请求。
func sendMessageRequestFromSendMedia(req *tg.MessagesSendMediaRequest) *tg.MessagesSendMessageRequest {
	return &tg.MessagesSendMessageRequest{
		Silent:                 req.Silent,
		Background:             req.Background,
		ClearDraft:             req.ClearDraft,
		Noforwards:             req.Noforwards,
		UpdateStickersetsOrder: req.UpdateStickersetsOrder,
		InvertMedia:            req.InvertMedia,
		AllowPaidFloodskip:     req.AllowPaidFloodskip,
		Peer:                   req.Peer,
		ReplyTo:                req.ReplyTo,
		Message:                req.Message,
		RandomID:               req.RandomID,
		ReplyMarkup:            req.ReplyMarkup,
		Entities:               req.Entities,
		ScheduleDate:           req.ScheduleDate,
		ScheduleRepeatPeriod:   req.ScheduleRepeatPeriod,
		SendAs:                 req.SendAs,
		QuickReplyShortcut:     req.QuickReplyShortcut,
		Effect:                 req.Effect,
		AllowPaidStars:         req.AllowPaidStars,
		SuggestedPost:          req.SuggestedPost,
	}
}

func mediaUploadErr(err error) error {
	switch {
	case errors.Is(err, domain.ErrFilePartsInvalid):
		return filePartsInvalidErr()
	case errors.Is(err, domain.ErrPhotoInvalid):
		return photoInvalidErr()
	case errors.Is(err, domain.ErrDocumentInvalid):
		return mediaInvalidErr()
	default:
		return internalErr()
	}
}

func userClassID(u tg.UserClass) int64 {
	if v, ok := u.(*tg.User); ok {
		return v.ID
	}
	return 0
}

func chatClassID(c tg.ChatClass) int64 {
	switch v := c.(type) {
	case *tg.Channel:
		return v.ID
	case *tg.Chat:
		return v.ID
	}
	return 0
}

func mapValuesUsers(m map[int64]tg.UserClass) []tg.UserClass {
	out := make([]tg.UserClass, 0, len(m))
	for _, v := range m {
		out = append(out, v)
	}
	return out
}

func mapValuesChats(m map[int64]tg.ChatClass) []tg.ChatClass {
	out := make([]tg.ChatClass, 0, len(m))
	for _, v := range m {
		out = append(out, v)
	}
	return out
}

// channelPostAuthorName 取当前用户的展示名作为 broadcast post 签名快照；
// store 层只在 signatures 开启的 post 上落库。
func (r *Router) channelPostAuthorName(ctx context.Context, userID int64) string {
	if r.deps.Users == nil || userID == 0 {
		return ""
	}
	self, err := r.deps.Users.Self(ctx, userID)
	if err != nil {
		return ""
	}
	name := strings.TrimSpace(strings.TrimSpace(self.FirstName) + " " + strings.TrimSpace(self.LastName))
	return name
}
