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

// This file implements the main photo/document/sticker paths of
// messages.uploadMedia / sendMedia / sendMultiMedia, and extracts sendOutgoing as the
// shared implementation of "persist one validated outbound message (text or media)",
// used by both private chats and channels.

const maxContactVcardLength = 8192

// outgoingSend is the input to sendOutgoing: one validated outbound message.
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
	// replyMarkup is the bot reply/inline keyboard (resolved+validated; always nil for a
	// non-bot).
	replyMarkup *domain.MessageReplyMarkup
	viaBotID    int64
	// richMessage is the Layer 227 rich-message snapshot (with embedded media resolved;
	// always nil for an ordinary message).
	richMessage *domain.MessageRichMessage
	// groupedID is the album group id: items of the same group in sendMultiMedia share a
	// non-zero value (the client renders them as one album group); always 0 for a single
	// send.
	groupedID int64
	// effect is the message effect id (private-chat only, 0 means no effect). The caller
	// has validated it against the catalog; the channel side ignores it (official
	// groups/channels do not render effects).
	effect         int64
	allowPaidStars int64
	paidStars      int64
	// paidStarsReady means a batch caller already gated paidStars for every item,
	// so the per-message price is not read again.
	paidStarsReady bool
}

// sendOutgoing persists one outbound message to a private chat or channel, returning
// *tg.Updates, whether it is a duplicate, and an error. An empty media means plain text.
// Validation (length/random_id/rate limit) is done by the caller.
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
		// The sender echo goes through rpc_result (synchronously, with its own cache); the
		// other members' fan-out is made asynchronous, moving off the sender's synchronous
		// RPC path (design Phase 0). The echo and the fan-out must use their own separate
		// viewerPeerCache - the former runs in the RPC goroutine and the latter in a worker
		// goroutine, so sharing one would be a data race.
		// The sender echo goes through rpc_result (synchronously, with its own cache); the
		// other members' fan-out is made asynchronous. The echo and the fan-out must use
		// their own separate viewerPeerCache - the former runs in the RPC goroutine and the
		// latter in a worker goroutine, so sharing one would be a data race.
		echoCache := newViewerPeerCache(r)
		updates := r.channelMessageUpdatesWithPeerCache(ctx, userID, res, p.randomID, echoCache)
		if !res.Duplicate {
			r.enqueueChannelMessageFanout(ctx, userID, res, nil)
			r.pushChannelDiscussionUpdate(ctx, userID, res.Discussion)
			// Channel link-preview pending placeholder: resolve out-of-band and swap in
			// place (asynchronous, without blocking the send echo).
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
	if !p.paidStarsReady {
		charged, err := r.privatePaidStarsCharge(ctx, userID, peer.ID, p.allowPaidStars, 1)
		if err != nil {
			return nil, false, err
		}
		p.paidStars = charged
	}
	if err := r.ensureVoiceMessagesAllowed(ctx, userID, peer, p.media != nil && p.media.HasUnreadPayload()); err != nil {
		return nil, false, err
	}
	var projectedUsers []domain.User
	if r.deps.Users != nil {
		loaded, err := r.deps.Users.ByIDs(ctx, userID, []int64{userID, peer.ID})
		if err != nil {
			return nil, false, internalErr()
		}
		projectedUsers = loaded
		peerFound := peer.ID == userID
		for _, user := range projectedUsers {
			if user.ID == peer.ID {
				peerFound = true
				break
			}
		}
		if !peerFound {
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
	// The outbox exclusion key locates the physical connection that initiated the RPC;
	// after PFS temp-key binding, AuthKeyIDFrom is the business-view perm key and cannot
	// stand in for the connection's actual raw key.
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
		OriginClientSession:    clientSessionMetadataFromContext(ctx),
		RecipientBlocked:       recipientBlocked,
		IdempotencyFingerprint: p.idempotencyFingerprint,
		IdempotencyPreflighted: p.idempotencyPreflighted,
		ReplyMarkup:            p.replyMarkup,
		RichMessage:            p.richMessage,
		ViaBotID:               p.viaBotID,
		GroupedID:              p.groupedID,
		Effect:                 p.effect,
		PaidStars:              p.paidStars,
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
		users = r.usersForMessageUpdateWithPreloaded(ctx, userID, res.SenderMessage, projectedUsers)
		chats = r.chatsForMessageUpdate(ctx, userID, res.SenderMessage)
	}
	if p.clearDraft && !res.Duplicate {
		r.clearDraftAfterSendWithPeerObjects(ctx, userID, peer, replyTo, users, chats)
	}
	if !res.Duplicate {
		// Link-preview pending placeholder: resolve out-of-band and swap in place
		// (asynchronous, without blocking the send echo).
		r.maybeEnqueueWebPageResolve(userID, peer, res.SenderMessage.ID, res.SenderMessage.Media)
		r.enqueueBotAPIPrivateMessageUpdateAsync(ctx, res)
	}
	out := tgPrivateSendResultUpdates(res, p.randomID, true, users, chats)
	if !res.Duplicate && res.PaidStars != nil {
		out.Updates = append(out.Updates, &tg.UpdateStarsBalance{Balance: &tg.StarsAmount{Amount: res.PaidStars.SenderBalance}})
		r.pushUserUpdates(ctx, peer.ID, &tg.Updates{Updates: []tg.UpdateClass{&tg.UpdateStarsBalance{Balance: &tg.StarsAmount{Amount: res.PaidStars.RecipientBalance}}}})
	}
	return out, res.Duplicate, nil
}

// onMessagesUploadMedia resolves InputMedia (upload or reference) and returns a reusable
// tg.MessageMedia.
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

// onMessagesSendMedia sends one message with media (photo/document/sticker), supported
// for both private chats and channels.
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
	// InputMediaEmpty / WebPage: degrade to a plain-text send (reusing the sendMessage
	// validation and flow, including url-entity completion).
	switch req.Media.(type) {
	case *tg.InputMediaEmpty, *tg.InputMediaWebPage:
		return r.onMessagesSendMessage(ctx, sendMessageRequestFromSendMedia(req))
	}
	// The fingerprint is idempotency-check metadata; a malformed input that the media
	// resolver should map to MEDIA_INVALID must not turn into INTERNAL early because of
	// an Encode failure. A valid request gets the original TL fingerprint; on an encode
	// failure it is left empty and the store uses the domain fallback.
	idempotencyFingerprint, _ := sendMediaIdempotencyFingerprint(req)
	// Links/@mention/#hashtag etc. in a media caption likewise get auto-highlight entities
	// added (when the client did not carry them). The derived result stays in a local
	// variable and must not be written back to the original TL request, or a retry of the
	// same request object would change the fingerprint from "client input" to
	// "server-derived input", wrongly triggering RANDOM_ID_DUPLICATE.
	entities := r.augmentAutoEntities(req.Message, req.Entities)
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
	suggestedInput, hasSuggestedPost := req.GetSuggestedPost()
	var mono domain.Channel
	var monoforum, monoforumAdmin bool
	if peer.Type == domain.PeerTypeChannel && r.deps.Channels != nil {
		mono, monoforumAdmin, err = r.deps.Channels.ResolveMonoforumSend(ctx, userID, peer.ID)
		switch {
		case err == nil:
			monoforum = true
		case !errors.Is(err, domain.ErrChannelInvalid):
			return nil, internalErr()
		}
	}
	if hasSuggestedPost && !monoforum {
		return nil, suggestedPostPeerInvalidErr()
	}
	if monoforum {
		if req.AllowPaidStars < 0 {
			return nil, starsAmountInvalidErr()
		}
		if req.AllowPaidFloodskip {
			return nil, paymentUnsupportedErr()
		}
		if req.ScheduleDate != 0 && !scheduleDateIsImmediate(req.ScheduleDate, int(r.clock.Now().Unix())) {
			return nil, scheduleDateInvalidErr()
		}
		suggestedPost, err := domainSuggestedPost(suggestedInput, hasSuggestedPost)
		if err != nil {
			return nil, err
		}
		savedPeer, err := r.monoforumSavedPeerForSender(userID, monoforumAdmin, req.ReplyTo)
		if err != nil {
			return nil, err
		}
		replyTo, err := r.monoforumMessageReplyFromInput(ctx, userID, peer, req.ReplyTo)
		if err != nil {
			return nil, err
		}
		replay, err := r.lookupChannelSendReplay(ctx, userID, peer.ID, savedPeer, req.RandomID, idempotencyFingerprint)
		if err != nil {
			return nil, err
		}
		if replay.found {
			if req.ClearDraft {
				r.clearDraftAfterSend(ctx, userID, peer, replyTo)
			}
			return r.monoforumSendUpdatesStrict(ctx, userID, replay.channel.Channel, savedPeer, replay.channel)
		}
		if r.messageEffectInvalid(ctx, req.Effect) {
			return nil, effectIDInvalidErr()
		}
		if err := r.checkSendRateLimit(ctx, userID, 1); err != nil {
			return nil, err
		}
		checkedPeer, err := r.checkedDomainPeerFromInputPeer(ctx, userID, req.Peer)
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
		return r.sendMonoforumMessage(ctx, userID, checkedPeer, mono, monoforumAdmin, domain.SendMonoforumMessageRequest{
			SavedPeer:              savedPeer,
			RandomID:               req.RandomID,
			IdempotencyFingerprint: idempotencyFingerprint,
			IdempotencyPreflighted: replay.checked,
			Message:                req.Message,
			Entities:               domainMessageEntities(entities),
			Media:                  media,
			ReplyTo:                replyTo,
			Silent:                 req.Silent,
			NoForwards:             req.Noforwards,
			SuggestedPost:          suggestedPost,
			AllowPaidStars:         req.AllowPaidStars,
			ClearDraft:             req.ClearDraft,
		})
	}
	if req.AllowPaidFloodskip {
		return nil, paymentUnsupportedErr()
	}
	replay, err := r.lookupOutgoingReplay(ctx, userID, peer, req.RandomID, idempotencyFingerprint)
	if err != nil {
		return nil, err
	}
	if replay.found {
		return r.outgoingReplayUpdates(ctx, userID, peer, req.RandomID, replay), nil
	}
	// Message effect: only a valid effect id from the catalog is accepted (an invalid id
	// -> EFFECT_ID_INVALID, official behavior).
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
	voiceOrRound, err := r.preflightVoiceOrRound(ctx, []tg.InputMediaClass{req.Media})
	if err != nil {
		return nil, err
	}
	if err := r.ensureVoiceMessagesAllowed(ctx, userID, peer, voiceOrRound); err != nil {
		return nil, err
	}
	media, err := r.resolveInputMedia(ctx, userID, req.Media)
	if err != nil {
		return nil, err
	}
	if media == nil {
		return nil, mediaInvalidErr()
	}
	// reply_markup: a bot may send an inline keyboard and an ordinary reply
	// keyboard/hide/force; for a non-bot it is silently dropped.
	var replyMarkup *domain.MessageReplyMarkup
	if req.ReplyMarkup != nil {
		replyMarkup, err = domainOutgoingReplyMarkupForSender(req.ReplyMarkup, r.userIsBot(ctx, userID))
		if err != nil {
			return nil, replyMarkupErr(err)
		}
		if err := r.validateReplyMarkupForPeer(ctx, userID, peer, replyMarkup); err != nil {
			return nil, err
		}
	}
	if req.ScheduleDate != 0 && !scheduleDateIsImmediate(req.ScheduleDate, int(r.clock.Now().Unix())) {
		return r.scheduleOutgoing(ctx, userID, peer, outgoingSend{
			randomID:               req.RandomID,
			idempotencyFingerprint: idempotencyFingerprint,
			idempotencyPreflighted: replay.checked,
			message:                req.Message,
			entities:               entities,
			media:                  media,
			silent:                 req.Silent,
			noforwards:             req.Noforwards,
			replyToInput:           req.ReplyTo,
			sendAsInput:            req.SendAs,
			clearDraft:             req.ClearDraft,
			allowPaidStars:         req.AllowPaidStars,
		}, req.ScheduleDate, req.ScheduleRepeatPeriod)
	}
	updates, _, err := r.sendOutgoing(ctx, userID, peer, outgoingSend{
		randomID:               req.RandomID,
		idempotencyFingerprint: idempotencyFingerprint,
		idempotencyPreflighted: replay.checked,
		message:                req.Message,
		entities:               entities,
		media:                  media,
		silent:                 req.Silent,
		noforwards:             req.Noforwards,
		replyToInput:           req.ReplyTo,
		sendAsInput:            req.SendAs,
		clearDraft:             req.ClearDraft,
		replyMarkup:            replyMarkup,
		effect:                 req.Effect,
		allowPaidStars:         req.AllowPaidStars,
	})
	if err != nil {
		return nil, err
	}
	return updates, nil
}

// onMessagesSendMultiMedia sends an album (multiple media) and durably reserves the
// grouped_id before resolving media.
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
	if req.AllowPaidStars < 0 {
		return nil, starsAmountInvalidErr()
	}
	if req.AllowPaidFloodskip {
		return nil, paymentUnsupportedErr()
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
			// A TL encode failure on a malformed InputMedia must not become INTERNAL; keep
			// the media-input error semantics.
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
	scheduled := req.ScheduleDate != 0 && !scheduleDateIsImmediate(req.ScheduleDate, int(r.clock.Now().Unix()))
	var albumPaidStars int64
	if peer.Type == domain.PeerTypeUser {
		if req.AllowPaidFloodskip {
			return nil, paymentUnsupportedErr()
		}
		// Gate the album once and charge every item this price: reading the price
		// again per item could exceed the allowance if the recipient raises it mid-album.
		albumPaidStars, err = r.privatePaidStarsCharge(ctx, userID, peer.ID, req.AllowPaidStars, absentCount)
		if err != nil {
			return nil, err
		}
		if !scheduled {
			if err := r.ensurePaidStarsBalance(ctx, userID, albumPaidStars, absentCount); err != nil {
				return nil, err
			}
		}
	}
	pendingMedia := make([]tg.InputMediaClass, 0, absentCount)
	for i, item := range req.MultiMedia {
		if !replays[i].found {
			pendingMedia = append(pendingMedia, item.Media)
		}
	}
	voiceOrRound, err := r.preflightVoiceOrRound(ctx, pendingMedia)
	if err != nil {
		return nil, err
	}
	if err := r.ensureVoiceMessagesAllowed(ctx, userID, peer, voiceOrRound); err != nil {
		return nil, err
	}

	// The reservation must happen atomically before resolveInputMedia or sending any item:
	// if the first request fails at item N, a client retrying only the failed subset still
	// recovers the whole package's grouped_id from the already-bound random_id.
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
			entities:               r.augmentAutoEntities(item.Message, item.Entities),
			media:                  media,
			silent:                 req.Silent,
			noforwards:             req.Noforwards,
			replyToInput:           req.ReplyTo,
			sendAsInput:            req.SendAs,
			clearDraft:             clearDraftPending,
			groupedID:              groupedID,
			allowPaidStars:         req.AllowPaidStars,
			paidStars:              albumPaidStars,
			paidStarsReady:         peer.Type == domain.PeerTypeUser,
		}
		var result tg.UpdatesClass
		duplicate := false
		if scheduled {
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

// resolveInputMedia resolves tg.InputMedia into domain.MessageMedia (upload persists,
// reference loads). Returning nil means InputMediaEmpty (the caller degrades to plain
// text).
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
		// External image: the server SSRF-safely fetches the URL and mints a Photo.
		// Disabled/blocked/upstream failure all map to MEDIA_INVALID.
		if r.deps.Files == nil {
			return nil, mediaInvalidErr()
		}
		photo, err := r.deps.Files.CreatePhotoFromURL(ctx, in.URL)
		if err != nil {
			return nil, mediaInvalidErr()
		}
		return &domain.MessageMedia{Kind: domain.MessageMediaKindPhoto, Photo: &photo, Spoiler: in.Spoiler, TTLSeconds: in.TTLSeconds}, nil
	case *tg.InputMediaDocumentExternal:
		// External document: fetch the URL, take mime from Content-Type and the filename
		// from the URL basename. video_cover/timestamp are ignored for now.
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
			// stopped is only meaningful when editMessage stops sharing; sending an
			// immediately-stopped location has no client path.
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
			// compat diagnostics: client poll construction shapes are varied (flag defaults /
			// answer constructors drift across versions), so a rejection must leave a trace;
			// otherwise there is only a bare 4xx with nothing to reconcile against.
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
		// Persist the authoritative row first (an orphaned poll from a failed message send
		// is harmless and reclaimable).
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
		// poll (see messages_polls.go for the separate path) / geoLive / todo / game /
		// invoice / paid media / external etc. are not wired up; see
		// docs/compatibility-matrix.md for the scope and stub decisions.
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

// domainGeoPointFromInput validates and converts InputGeoPoint; an empty point returns
// MEDIA_EMPTY and out-of-range returns MEDIA_INVALID. access_hash is generated randomly
// here: the client echoes it back verbatim in the upload.getWebFile map-thumbnail
// request, but server map rendering does not depend on authorization (see
// upload_webfile.go), so there is no need to persist/validate it.
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

// randomPollID generates a positive 63-bit random poll id; the probability of a polls
// primary-key collision is negligible (on a collision the client bears the INSERT error
// and retry).
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

// normalizeDiceEmoticon strips the emoji variation selector (U+FE0F): TDesktop allows
// sending both ⚽️ and ⚽, but the dice sticker system-set key and the official response
// are both in bare code-point form.
func normalizeDiceEmoticon(emoticon string) string {
	return strings.ReplaceAll(emoticon, "️", "")
}

// diceValueSides returns the value bound for an emoticon (range [1, sides], consistent
// with the official server).
// The list must stay in sync with appConfig's emojies_send_dice (the client uses it to
// decide whether a single emoji becomes a dice).
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
	// Rejection sampling removes modulo bias; with sides ≤ 64 a single byte suffices.
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

// messageMediaFromDocument builds a MessageMedia from a Document and derives the
// Video/Round/Voice flags from its attributes.
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

// domainDocumentAttributes converts tg.DocumentAttribute back to domain (for
// InputMediaUploadedDocument).
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

// sendMessageRequestFromSendMedia maps sendMedia's fields (empty media) onto a
// sendMessage request.
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
	case errors.Is(err, domain.ErrStorageFull):
		return storageFullErr()
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

// channelPostAuthorName takes the current user's display name as the broadcast-post
// signature snapshot; the store only persists it on posts with signatures enabled.
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
