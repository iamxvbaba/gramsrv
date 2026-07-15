package rpc

import (
	"context"
	"errors"
	"strings"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
)

func (r *Router) onMessagesForwardMessages(ctx context.Context, req *tg.MessagesForwardMessagesRequest) (tg.UpdatesClass, error) {
	ids, randomIDs, ok := normalizeForwardMessageVectors(req.ID, req.RandomID)
	if !ok {
		return nil, inputRequestInvalidErr()
	}
	req.ID = ids
	req.RandomID = randomIDs
	if len(req.ID) > domain.MaxForwardMessageIDs {
		return nil, limitInvalidErr()
	}
	if req.ScheduleRepeatPeriod != 0 {
		return nil, scheduleDateInvalidErr()
	}
	if err := forwardMessagesUnsupportedOptionErr(req); err != nil {
		return nil, err
	}
	topMsgID, topMsgIDSet := req.GetTopMsgID()
	if !topMsgIDSet && req.TopMsgID != 0 {
		topMsgID, topMsgIDSet = req.TopMsgID, true
	}
	if topMsgIDSet && topMsgID == -1 {
		topMsgID, topMsgIDSet = 0, false
	}
	if topMsgIDSet && (topMsgID < 0 || topMsgID > domain.MaxMessageBoxID) {
		return nil, replyMessageIDInvalidErr()
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	if userID == 0 {
		return nil, peerIDInvalidErr()
	}
	if !forwardMessageIDsValid(req.ID, req.RandomID) {
		return nil, messageIDInvalidErr()
	}
	seenRandomIDs := make(map[int64]struct{}, len(req.RandomID))
	for _, randomID := range req.RandomID {
		if _, duplicate := seenRandomIDs[randomID]; duplicate {
			return nil, randomIDDuplicateErr()
		}
		seenRandomIDs[randomID] = struct{}{}
	}
	toPeer, ok := r.domainPeerFromInputPeer(userID, req.ToPeer)
	if !ok || toPeer.ID == 0 {
		return nil, peerIDInvalidErr()
	}
	idempotencyFingerprints := make([][]byte, len(req.ID))
	for i := range req.ID {
		idempotencyFingerprints[i], err = forwardMessagesItemIdempotencyFingerprint(req, req.ID[i], req.RandomID[i])
		if err != nil {
			return nil, internalErr()
		}
	}
	immediate := req.ScheduleDate == 0 || scheduleDateIsImmediate(req.ScheduleDate, int(r.clock.Now().Unix()))
	replays := make([]outgoingReplayLookup, len(req.ID))
	absentIndexes := make([]int, 0, len(req.ID))
	if immediate {
		for i := range req.ID {
			replay, err := r.lookupOutgoingReplay(ctx, userID, toPeer, req.RandomID[i], idempotencyFingerprints[i])
			if err != nil {
				return nil, err
			}
			replays[i] = replay
			if !replay.found {
				absentIndexes = append(absentIndexes, i)
			}
		}
	} else {
		for i := range req.ID {
			absentIndexes = append(absentIndexes, i)
		}
	}
	if len(absentIndexes) == 0 {
		if toPeer.Type == domain.PeerTypeChannel {
			results := make([]domain.SendChannelMessageResult, len(replays))
			for i := range replays {
				results[i] = replays[i].channel
			}
			return r.channelMessagesUpdatesWithPeerCache(ctx, userID, results, req.RandomID, true, nil, newViewerPeerCache(r)), nil
		}
		res := domain.ForwardPrivateMessagesResult{OwnerUserID: userID}
		for i := range replays {
			sent := replays[i].private
			res.SenderMessages = append(res.SenderMessages, sent.SenderMessage)
			res.RecipientMessages = append(res.RecipientMessages, sent.RecipientMessage)
			res.SenderEvents = append(res.SenderEvents, sent.SenderEvent)
			res.RecipientEvents = append(res.RecipientEvents, sent.RecipientEvent)
			res.Duplicates = append(res.Duplicates, true)
			res.ReplayDeleteEvents = append(res.ReplayDeleteEvents, sent.ReplayDeleteEvent)
		}
		return tgForwardMessagesUpdates(res, req.RandomID, r.usersForMessageUpdates(ctx, userID, res.SenderMessages), r.chatsForMessageUpdates(ctx, userID, res.SenderMessages)), nil
	}
	if err := r.checkSendRateLimit(ctx, userID, len(absentIndexes)); err != nil {
		return nil, err
	}
	toPeer, err = r.checkedDomainPeerFromInputPeer(ctx, userID, req.ToPeer)
	if err != nil {
		return nil, err
	}
	absentIDs := make([]int, len(absentIndexes))
	absentRandomIDs := make([]int64, len(absentIndexes))
	for i, originalIndex := range absentIndexes {
		absentIDs[i] = req.ID[originalIndex]
		absentRandomIDs[i] = req.RandomID[originalIndex]
	}
	fromPeer, preloadedSources, err := r.forwardFromPeerAndSources(ctx, userID, req.FromPeer, absentIDs, absentRandomIDs)
	if err != nil {
		return nil, err
	}
	sendAs, err := r.resolveSendAsPeer(ctx, userID, toPeer, req.SendAs)
	if err != nil {
		return nil, err
	}
	replyTo, err := r.messageReplyFromInput(ctx, userID, toPeer, req.ReplyTo)
	if err != nil {
		return nil, err
	}
	replyTo, err = mergeForwardTopMsgID(toPeer, replyTo, topMsgID, topMsgIDSet)
	if err != nil {
		return nil, err
	}
	if r.deps.Users != nil {
		for _, peer := range []domain.Peer{fromPeer, toPeer} {
			if peer.Type != domain.PeerTypeUser || peer.ID == userID {
				continue
			}
			if _, found, err := r.deps.Users.ByID(ctx, userID, peer.ID); err != nil {
				return nil, internalErr()
			} else if !found {
				return nil, peerIDInvalidErr()
			}
		}
	}
	if req.ScheduleDate != 0 && !scheduleDateIsImmediate(req.ScheduleDate, int(r.clock.Now().Unix())) {
		return r.scheduleForwardMessages(ctx, userID, fromPeer, toPeer, req, replyTo, sendAs, preloadedSources)
	}
	absentSources, err := r.forwardSourcesForRequest(ctx, userID, fromPeer, absentIDs, preloadedSources)
	if err != nil {
		return nil, messageForwardErr(err)
	}
	sources := make([]forwardSource, len(req.ID))
	for i, originalIndex := range absentIndexes {
		sources[originalIndex] = absentSources[i]
	}
	if toPeer.Type == domain.PeerTypeChannel {
		if r.deps.Channels == nil {
			return nil, peerIDInvalidErr()
		}
		recipients := make([]int64, 0)
		results := make([]domain.SendChannelMessageResult, 0, len(sources))
		extraUserIDs := make([]int64, 0, len(sources))
		fanoutResults := make([]domain.SendChannelMessageResult, 0, len(sources))
		fanoutExtraUserIDs := make([]int64, 0, len(sources))
		for i, source := range sources {
			if replays[i].found {
				results = append(results, replays[i].channel)
				continue
			}
			forward := source.forward
			if req.DropAuthor {
				forward = nil
			}
			mentionUserIDs := r.mentionUserIDsFromDomain(ctx, userID, source.body, source.entities)
			res, err := r.deps.Channels.SendMessage(ctx, userID, domain.SendChannelMessageRequest{
				UserID:                 userID,
				ChannelID:              toPeer.ID,
				RandomID:               req.RandomID[i],
				IdempotencyFingerprint: idempotencyFingerprints[i],
				IdempotencyPreflighted: replays[i].checked,
				Message:                source.body,
				Entities:               source.entities,
				Media:                  source.media,
				MentionUserIDs:         mentionUserIDs,
				Silent:                 req.Silent,
				NoForwards:             req.Noforwards,
				ReplyTo:                replyTo,
				Forward:                forward,
				SendAs:                 sendAs,
				Date:                   int(r.clock.Now().Unix()),
			})
			if err != nil {
				return nil, channelInvalidErr(err)
			}
			results = append(results, res)
			sourceUserID := source.userID()
			if sourceUserID != 0 {
				extraUserIDs = append(extraUserIDs, sourceUserID)
			}
			// An exact random_id replay must still appear in the caller's echo, but it must not
			// emit the old channel pts as a fresh realtime payload/Bot API update/discussion push.
			// Mixed batches therefore fan out only newly committed results while preserving the
			// complete result vector for TDesktop's random_id -> message-id reconciliation.
			if res.Duplicate {
				continue
			}
			fanoutResults = append(fanoutResults, res)
			if sourceUserID != 0 {
				fanoutExtraUserIDs = append(fanoutExtraUserIDs, sourceUserID)
			}
			// 收件人是该频道的活跃成员集，对本次转发的每条源都相同；只取一次，
			// 避免一次转发 ≤100 条到 N 成员大群时把 recipients 累积成 ~100×N 条目
			// 的巨大临时切片（N=10^5 时约千万级）。
			if len(recipients) == 0 {
				recipients = res.Recipients
			}
		}
		// echo 与 fan-out 用各自独立 cache（RPC vs worker goroutine 防竞态）。多条转发汇成
		// 一个 fan-out job（channelMessagesUpdatesWithPeerCache 内含多条 UpdateNewChannelMessage），
		// 由同 channel 分片 FIFO 原子投递。
		echoCache := newViewerPeerCache(r)
		updates := r.channelMessagesUpdatesWithPeerCache(ctx, userID, results, req.RandomID, true, extraUserIDs, echoCache)
		if n := len(fanoutResults); n > 0 {
			fanoutPts := fanoutResults[n-1].Event.Pts
			r.enqueueChannelMessagesFanout(ctx, userID, toPeer.ID, fanoutPts, recipients, fanoutResults, fanoutExtraUserIDs)
		}
		for _, res := range fanoutResults {
			r.pushChannelDiscussionUpdate(ctx, userID, res.Discussion)
		}
		return updates, nil
	}
	if toPeer.Type == domain.PeerTypeUser {
		if r.deps.Messages == nil {
			return nil, peerIDInvalidErr()
		}
		recipientBlocked, err := r.peerBlocksUser(ctx, userID, toPeer.ID)
		if err != nil {
			return nil, err
		}
		sessionID, _ := SessionIDFrom(ctx)
		authKeyID := rawAuthKeyIDForOrigin(ctx)
		res := domain.ForwardPrivateMessagesResult{OwnerUserID: userID}
		for i, source := range sources {
			if replays[i].found {
				sent := replays[i].private
				res.SenderMessages = append(res.SenderMessages, sent.SenderMessage)
				res.RecipientMessages = append(res.RecipientMessages, sent.RecipientMessage)
				res.SenderEvents = append(res.SenderEvents, sent.SenderEvent)
				res.RecipientEvents = append(res.RecipientEvents, sent.RecipientEvent)
				res.Duplicates = append(res.Duplicates, true)
				res.ReplayDeleteEvents = append(res.ReplayDeleteEvents, sent.ReplayDeleteEvent)
				continue
			}
			forward := source.forward
			if req.DropAuthor {
				forward = nil
			}
			if forward != nil && toPeer.ID == userID {
				saved := *forward
				saved.SavedFrom = fromPeer
				saved.SavedFromMsgID = req.ID[i]
				forward = &saved
			}
			sent, err := r.deps.Messages.SendPrivateText(ctx, userID, domain.SendPrivateTextRequest{
				SenderUserID:           userID,
				RecipientUserID:        toPeer.ID,
				RandomID:               req.RandomID[i],
				Message:                source.body,
				Entities:               source.entities,
				Media:                  source.media,
				Silent:                 req.Silent,
				NoForwards:             req.Noforwards,
				ReplyTo:                replyTo,
				Forward:                forward,
				Date:                   int(r.clock.Now().Unix()),
				OriginAuthKeyID:        authKeyID,
				OriginSessionID:        sessionID,
				RecipientBlocked:       recipientBlocked,
				IdempotencyFingerprint: idempotencyFingerprints[i],
				IdempotencyPreflighted: replays[i].checked,
			})
			if err != nil {
				return nil, messageForwardErr(err)
			}
			if !sent.Duplicate {
				r.enqueueBotAPIPrivateMessageUpdateAsync(ctx, sent)
			}
			res.SenderMessages = append(res.SenderMessages, sent.SenderMessage)
			res.RecipientMessages = append(res.RecipientMessages, sent.RecipientMessage)
			res.SenderEvents = append(res.SenderEvents, sent.SenderEvent)
			res.RecipientEvents = append(res.RecipientEvents, sent.RecipientEvent)
			res.Duplicates = append(res.Duplicates, sent.Duplicate)
			res.ReplayDeleteEvents = append(res.ReplayDeleteEvents, sent.ReplayDeleteEvent)
		}
		return tgForwardMessagesUpdates(res, req.RandomID, r.usersForMessageUpdates(ctx, userID, res.SenderMessages), r.chatsForMessageUpdates(ctx, userID, res.SenderMessages)), nil
	}
	return nil, peerIDInvalidErr()
}

func normalizeForwardMessageVectors(ids []int, randomIDs []int64) ([]int, []int64, bool) {
	if len(ids) == 0 || len(randomIDs) == 0 {
		return nil, nil, false
	}
	if len(ids) == len(randomIDs) {
		return ids, randomIDs, true
	}
	if len(ids) < len(randomIDs) {
		return nil, nil, false
	}
	compact := make([]int, 0, len(randomIDs))
	runLength := 0
	for i, id := range ids {
		if i == 0 || id != ids[i-1] {
			compact = append(compact, id)
			runLength = 1
			continue
		}
		runLength++
		if runLength > 2 {
			return nil, nil, false
		}
	}
	if len(compact) != len(randomIDs) {
		return nil, nil, false
	}
	return compact, randomIDs, true
}

func (r *Router) forwardFromPeerAndSources(ctx context.Context, userID int64, input tg.InputPeerClass, ids []int, randomIDs []int64) (domain.Peer, []forwardSource, error) {
	if forwardFromPeerIsEmpty(input) {
		if !forwardMessageIDsValid(ids, randomIDs) {
			return domain.Peer{}, nil, messageIDInvalidErr()
		}
		fromPeer, sources, err := r.forwardSourcesFromEmptyPeer(ctx, userID, ids)
		if err != nil {
			return domain.Peer{}, nil, messageForwardErr(err)
		}
		return fromPeer, sources, nil
	}
	fromPeer, err := r.checkedDomainPeerFromInputPeer(ctx, userID, input)
	return fromPeer, nil, err
}

func forwardMessageIDsValid(ids []int, randomIDs []int64) bool {
	if len(ids) == 0 || len(ids) != len(randomIDs) {
		return false
	}
	for i, id := range ids {
		if id <= 0 || id > domain.MaxMessageBoxID || randomIDs[i] == 0 {
			return false
		}
	}
	return true
}

func forwardFromPeerIsEmpty(peer tg.InputPeerClass) bool {
	if inputPeerClassNil(peer) {
		return false
	}
	_, ok := peer.(*tg.InputPeerEmpty)
	return ok
}

func (r *Router) forwardSourcesFromEmptyPeer(ctx context.Context, userID int64, ids []int) (domain.Peer, []forwardSource, error) {
	if r.deps.Messages == nil {
		return domain.Peer{}, nil, domain.ErrMessageIDInvalid
	}
	list, err := r.deps.Messages.GetMessages(ctx, userID, ids)
	if err != nil {
		return domain.Peer{}, nil, domain.ErrMessageIDInvalid
	}
	var fromPeer domain.Peer
	byID := make(map[int]domain.Message, len(list.Messages))
	for _, msg := range list.Messages {
		byID[msg.ID] = msg
	}
	for _, id := range ids {
		msg, ok := byID[id]
		if !ok || msg.Peer.Type != domain.PeerTypeUser || msg.Peer.ID == 0 {
			return domain.Peer{}, nil, domain.ErrMessageIDInvalid
		}
		if fromPeer.ID == 0 {
			fromPeer = msg.Peer
			continue
		}
		if msg.Peer != fromPeer {
			return domain.Peer{}, nil, domain.ErrMessageIDInvalid
		}
	}
	sources, err := r.forwardSourcesFromPrivateMessages(ctx, userID, fromPeer, ids, list.Messages)
	if err != nil {
		return domain.Peer{}, nil, err
	}
	return fromPeer, sources, nil
}

func (r *Router) forwardSourcesForRequest(ctx context.Context, userID int64, fromPeer domain.Peer, ids []int, preloaded []forwardSource) ([]forwardSource, error) {
	if preloaded != nil {
		return preloaded, nil
	}
	return r.forwardSources(ctx, userID, fromPeer, ids)
}

func mergeForwardTopMsgID(toPeer domain.Peer, replyTo *domain.MessageReply, topMsgID int, topMsgIDSet bool) (*domain.MessageReply, error) {
	if !topMsgIDSet || topMsgID == 0 {
		return replyTo, nil
	}
	if topMsgID < 0 || topMsgID > domain.MaxMessageBoxID || toPeer.Type != domain.PeerTypeChannel {
		return nil, replyMessageIDInvalidErr()
	}
	if replyTo == nil {
		return &domain.MessageReply{
			Peer:         toPeer,
			TopMessageID: topMsgID,
			ForumTopic:   true,
		}, nil
	}
	if replyTo.Peer.ID != 0 && replyTo.Peer != toPeer {
		return nil, replyMessageIDInvalidErr()
	}
	if replyTo.TopMessageID != 0 && replyTo.TopMessageID != topMsgID {
		return nil, replyMessageIDInvalidErr()
	}
	merged := *replyTo
	merged.Peer = toPeer
	merged.TopMessageID = topMsgID
	merged.QuoteEntities = append([]domain.MessageEntity(nil), replyTo.QuoteEntities...)
	if merged.MessageID == 0 {
		merged.ForumTopic = true
	}
	return &merged, nil
}

func (r *Router) forwardSources(ctx context.Context, userID int64, fromPeer domain.Peer, ids []int) ([]forwardSource, error) {
	out := make([]forwardSource, 0, len(ids))
	switch fromPeer.Type {
	case domain.PeerTypeUser:
		if r.deps.Messages == nil {
			return nil, domain.ErrMessageIDInvalid
		}
		list, err := r.deps.Messages.GetMessages(ctx, userID, ids)
		if err != nil {
			return nil, domain.ErrMessageIDInvalid
		}
		sources, err := r.forwardSourcesFromPrivateMessages(ctx, userID, fromPeer, ids, list.Messages)
		if err != nil {
			return nil, err
		}
		out = append(out, sources...)
	case domain.PeerTypeChannel:
		if r.deps.Channels == nil {
			return nil, domain.ErrMessageIDInvalid
		}
		history, err := r.deps.Channels.GetMessages(ctx, userID, fromPeer.ID, ids)
		if err != nil {
			return nil, domain.ErrMessageIDInvalid
		}
		byID := make(map[int]domain.ChannelMessage, len(history.Messages))
		for _, msg := range history.Messages {
			byID[msg.ID] = msg
		}
		for _, id := range ids {
			msg, ok := byID[id]
			if !ok {
				return nil, domain.ErrMessageIDInvalid
			}
			if msg.NoForwards || history.Channel.NoForwards {
				return nil, domain.ErrChatForwardsRestricted
			}
			if msg.Action != nil || (msg.Body == "" && msg.Media.IsZero()) {
				return nil, domain.ErrMessageIDInvalid
			}
			forward := cloneDomainMessageForward(msg.Forward)
			from := msg.From
			if from.ID == 0 && msg.SenderUserID != 0 {
				from = domain.Peer{Type: domain.PeerTypeUser, ID: msg.SenderUserID}
			}
			if msg.Post {
				from = domain.Peer{Type: domain.PeerTypeChannel, ID: msg.ChannelID}
			}
			if forward == nil {
				forward = &domain.MessageForward{From: from, Date: msg.Date}
				if from.Type == domain.PeerTypeChannel {
					forward.ChannelPost = msg.ID
				}
				r.applyForwardAuthorPrivacy(ctx, userID, forward)
			}
			out = append(out, forwardSource{
				body: msg.Body,
				entities: append([]domain.MessageEntity(nil),
					msg.Entities...),
				media:   msg.Media,
				forward: forward,
				from:    from,
				date:    msg.Date,
			})
		}
	default:
		return nil, domain.ErrMessageIDInvalid
	}
	return out, nil
}

func (r *Router) forwardSourcesFromPrivateMessages(ctx context.Context, userID int64, fromPeer domain.Peer, ids []int, messages []domain.Message) ([]forwardSource, error) {
	if fromPeer.Type != domain.PeerTypeUser || fromPeer.ID == 0 {
		return nil, domain.ErrMessageIDInvalid
	}
	byID := make(map[int]domain.Message, len(messages))
	for _, msg := range messages {
		byID[msg.ID] = msg
	}
	out := make([]forwardSource, 0, len(ids))
	for _, id := range ids {
		msg, ok := byID[id]
		if !ok {
			return nil, domain.ErrMessageIDInvalid
		}
		if msg.Peer != fromPeer {
			return nil, domain.ErrMessageIDInvalid
		}
		if msg.NoForwards {
			return nil, domain.ErrChatForwardsRestricted
		}
		forward := cloneDomainMessageForward(msg.Forward)
		if forward == nil {
			forward = &domain.MessageForward{From: msg.From, Date: msg.Date}
			r.applyForwardAuthorPrivacy(ctx, userID, forward)
		}
		out = append(out, forwardSource{
			body: msg.Body,
			entities: append([]domain.MessageEntity(nil),
				msg.Entities...),
			media:   msg.Media,
			forward: forward,
			from:    msg.From,
			date:    msg.Date,
		})
	}
	return out, nil
}

func cloneDomainMessageForward(in *domain.MessageForward) *domain.MessageForward {
	if in == nil {
		return nil
	}
	out := *in
	return &out
}

func (r *Router) forwardAuthorDisplayName(ctx context.Context, viewerUserID, authorUserID int64) string {
	name := ""
	if r.deps.Users != nil {
		if author, found, err := r.deps.Users.ByID(ctx, viewerUserID, authorUserID); err == nil && found {
			name = strings.TrimSpace(strings.TrimSpace(author.FirstName) + " " + strings.TrimSpace(author.LastName))
		}
	}
	if name == "" {
		name = "Deleted Account"
	}
	return name
}

// applyForwardAuthorPrivacy 在首次生成 forward header 时按原作者的
// forwards 隐私规则降级：不允许链接回账号时只保留展示名 from_name。
// 已有 header 的再转发沿用原 header，不重新评估。
func (r *Router) applyForwardAuthorPrivacy(ctx context.Context, forwarderUserID int64, forward *domain.MessageForward) {
	if forward == nil || forward.From.Type != domain.PeerTypeUser || forward.From.ID == 0 || forward.FromName != "" {
		return
	}
	if forward.From.ID == forwarderUserID || r.deps.Privacy == nil {
		return
	}
	allowed, err := r.deps.Privacy.CanSee(ctx, forward.From.ID, forwarderUserID, domain.PrivacyKeyForwards)
	if err != nil || allowed {
		return
	}
	name := r.forwardAuthorDisplayName(ctx, forwarderUserID, forward.From.ID)
	forward.From = domain.Peer{}
	forward.FromName = name
}

func messageForwardErr(err error) error {
	switch {
	case errors.Is(err, domain.ErrMessageIDInvalid):
		return messageIDInvalidErr()
	case errors.Is(err, domain.ErrChatForwardsRestricted):
		return chatForwardsRestrictedErr()
	case errors.Is(err, domain.ErrReplyMessageIDInvalid):
		return replyMessageIDInvalidErr()
	case errors.Is(err, domain.ErrMessageRandomIDDuplicate):
		return randomIDDuplicateErr()
	default:
		return internalErr()
	}
}

func tgForwardMessagesUpdates(res domain.ForwardPrivateMessagesResult, randomIDs []int64, users []tg.UserClass, chats []tg.ChatClass) *tg.Updates {
	updates := make([]tg.UpdateClass, 0, len(res.SenderMessages)*2)
	date := 0
	for i, msg := range res.SenderMessages {
		randomID := int64(0)
		if i < len(randomIDs) {
			randomID = randomIDs[i]
		}
		updates = append(updates, &tg.UpdateMessageID{ID: msg.ID, RandomID: randomID})
		event := domain.UpdateEvent{}
		if i < len(res.SenderEvents) {
			event = res.SenderEvents[i]
		}
		item := tgMessage(msg)
		if item == nil {
			item = &tg.MessageEmpty{ID: msg.ID}
		}
		pts := event.Pts
		if pts == 0 {
			pts = msg.Pts
		}
		ptsCount := event.PtsCount
		if ptsCount == 0 {
			ptsCount = 1
		}
		updates = append(updates, &tg.UpdateNewMessage{
			Message:  item,
			Pts:      pts,
			PtsCount: ptsCount,
		})
		if i < len(res.ReplayDeleteEvents) {
			if deleted := res.ReplayDeleteEvents[i]; deleted != nil && deleted.Pts > 0 && len(deleted.MessageIDs) > 0 {
				updates = append(updates, &tg.UpdateDeleteMessages{
					Messages: append([]int(nil), deleted.MessageIDs...),
					Pts:      deleted.Pts,
					PtsCount: deleted.PtsCount,
				})
				if deleted.Date > date {
					date = deleted.Date
				}
			}
		}
		if date == 0 {
			date = event.Date
		}
		if date == 0 {
			date = msg.Date
		}
	}
	return &tg.Updates{
		Updates: updates,
		Users:   users,
		Chats:   chats,
		Date:    date,
		Seq:     0,
	}
}
