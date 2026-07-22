package memory

import (
	"context"
	"sort"
	"strings"
	"time"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

const paidMessageChannelCommissionPermille int64 = 850

// SendMonoforumMessage 向 monoforum(频道私信)虚拟频道发一条消息,按 saved_peer 分订阅者子会话。
// 与 postgres 行为一致:复用 channel pts/事件;订阅者无需成员记录且只能写自己的 saved_peer，
// 母频道管理员可以回复任意订阅者。
func (s *ChannelStore) SendMonoforumMessage(_ context.Context, req domain.SendMonoforumMessageRequest) (domain.SendChannelMessageResult, error) {
	if req.MonoforumID == 0 || req.SenderUserID == 0 || req.SavedPeer.ID == 0 ||
		req.SavedPeer.Type != domain.PeerTypeUser || strings.TrimSpace(req.Message) == "" && req.Media == nil {
		return domain.SendChannelMessageResult{}, domain.ErrChannelInvalid
	}
	if req.AllowPaidStars < 0 {
		return domain.SendChannelMessageResult{}, domain.ErrStarsInvalidAmount
	}
	var fingerprint []byte
	var err error
	if req.RandomID != 0 {
		fingerprint, err = store.MonoforumSendFingerprint(req)
		if err != nil {
			return domain.SendChannelMessageResult{}, err
		}
		req.IdempotencyFingerprint = fingerprint
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if req.RandomID != 0 {
		if replay, found, replayErr := s.lookupChannelSendReplayLocked(domain.ChannelSendReplayRequest{
			ChannelID:              req.MonoforumID,
			SenderUserID:           req.SenderUserID,
			SavedPeer:              req.SavedPeer,
			RandomID:               req.RandomID,
			IdempotencyFingerprint: fingerprint,
		}); replayErr != nil || found {
			return replay, replayErr
		}
	}
	channel, ok := s.channels[req.MonoforumID]
	if !ok || channel.Deleted || !channel.Monoforum {
		return domain.SendChannelMessageResult{}, domain.ErrChannelInvalid
	}
	parent, ok := s.channels[channel.LinkedMonoforumID]
	if !ok || parent.Deleted || !parent.BroadcastMessagesAllowed || parent.LinkedMonoforumID != channel.ID {
		return domain.SendChannelMessageResult{}, domain.ErrChannelPrivate
	}
	parentMember, parentMemberOK := s.members[parent.ID][req.SenderUserID]
	isAdmin := parentMemberOK && parentMember.CanManageDirectMessages()
	if req.SenderUserID != req.SavedPeer.ID && !isAdmin {
		return domain.SendChannelMessageResult{}, domain.ErrChannelAdminRequired
	}
	if req.ReplyTo != nil {
		if req.ReplyTo.MessageID <= 0 || req.ReplyTo.Peer != (domain.Peer{Type: domain.PeerTypeChannel, ID: channel.ID}) {
			return domain.SendChannelMessageResult{}, domain.ErrReplyMessageIDInvalid
		}
		found := false
		for _, candidate := range s.messages[channel.ID] {
			if candidate.ID == req.ReplyTo.MessageID && !candidate.Deleted && candidate.SavedPeer == req.SavedPeer {
				found = true
				break
			}
		}
		if !found {
			return domain.SendChannelMessageResult{}, domain.ErrReplyMessageIDInvalid
		}
	}
	var senderBalance *domain.StarsBalance
	paidMessageStars := int64(0)
	balanceAfter := int64(0)
	if !isAdmin && channel.SendPaidMessagesStars > 0 {
		if channel.SendPaidMessagesStars != parent.SendPaidMessagesStars {
			return domain.SendChannelMessageResult{}, domain.ErrChannelInvalid
		}
		if req.AllowPaidStars < channel.SendPaidMessagesStars {
			return domain.SendChannelMessageResult{}, &domain.StarsPaymentRequiredError{Stars: channel.SendPaidMessagesStars}
		}
		current, ok := s.starsBalances[req.SenderUserID]
		if !ok {
			current = domain.DefaultStarsStartingGrant
		}
		if current < channel.SendPaidMessagesStars {
			return domain.SendChannelMessageResult{}, domain.ErrStarsInsufficient
		}
		paidMessageStars = channel.SendPaidMessagesStars
		balanceAfter = current - paidMessageStars
		senderBalance = &domain.StarsBalance{UserID: req.SenderUserID, Balance: balanceAfter, Granted: true}
	}
	from := domain.Peer{Type: domain.PeerTypeUser, ID: req.SenderUserID}
	if isAdmin {
		from = domain.Peer{Type: domain.PeerTypeChannel, ID: parent.ID}
	}
	if req.Date == 0 {
		req.Date = int(time.Now().Unix())
	}
	pts := s.nextChannelPtsLocked(req.MonoforumID)
	msgID := s.nextChannelMessageIDLocked(req.MonoforumID)
	msg := domain.ChannelMessage{
		ChannelID:        req.MonoforumID,
		ID:               msgID,
		RandomID:         req.RandomID,
		SenderUserID:     req.SenderUserID,
		From:             from,
		SavedPeer:        req.SavedPeer,
		SuggestedPost:    req.SuggestedPost,
		PaidMessageStars: paidMessageStars,
		Date:             req.Date,
		Silent:           req.Silent,
		NoForwards:       req.NoForwards,
		Body:             req.Message,
		Entities:         append([]domain.MessageEntity(nil), req.Entities...),
		Media:            req.Media,
		ReplyTo:          req.ReplyTo,
		Pts:              pts,
	}
	// Store owns the persisted snapshot; callers must not be able to mutate it through
	// SuggestedPost/Media pointers after SendMonoforumMessage returns.
	msg = cloneChannelMessage(msg)
	var sendSnapshot []byte
	if req.RandomID != 0 {
		var snapshotErr error
		sendSnapshot, snapshotErr = store.EncodeChannelSendSnapshot(msg)
		if snapshotErr != nil {
			return domain.SendChannelMessageResult{}, snapshotErr
		}
	}
	event := domain.ChannelUpdateEvent{
		ChannelID:    req.MonoforumID,
		Type:         domain.ChannelUpdateNewMessage,
		Pts:          pts,
		PtsCount:     1,
		Date:         req.Date,
		Message:      cloneChannelMessage(msg),
		SenderUserID: req.SenderUserID,
	}
	s.messages[req.MonoforumID] = append(s.messages[req.MonoforumID], msg)
	if paidMessageStars > 0 {
		s.starsBalances[req.SenderUserID] = balanceAfter
		s.channelStarsBalances[parent.ID] += paidMessageStars * paidMessageChannelCommissionPermille / 1000
	}
	if req.RandomID != 0 {
		replayKey := channelMessageReplayKey{channelID: req.MonoforumID, messageID: msg.ID}
		s.sendSnapshots[replayKey] = sendSnapshot
		s.sendFingerprints[replayKey] = append([]byte(nil), fingerprint...)
	}
	s.appendChannelEventLocked(event)
	channel.TopMessageID = msgID
	channel.Pts = pts
	s.channels[req.MonoforumID] = channel
	recipients := []int64{req.SavedPeer.ID}
	for userID, member := range s.members[parent.ID] {
		if member.CanManageDirectMessages() {
			recipients = append(recipients, userID)
		}
	}
	return domain.SendChannelMessageResult{Channel: cloneChannel(channel), Message: cloneChannelMessage(msg), Event: cloneChannelEvent(event), Recipients: uniqueNonZero(recipients, 0), SenderStarsBalance: senderBalance}, nil
}

// findMonoforumDuplicateLocked 按 (sender, saved_peer, random_id) 查 monoforum 子会话内的重发消息。
func (s *ChannelStore) findMonoforumDuplicateLocked(monoforumID, senderUserID int64, savedPeer domain.Peer, randomID int64) (domain.ChannelMessage, bool) {
	if randomID == 0 {
		return domain.ChannelMessage{}, false
	}
	msgs := s.messages[monoforumID]
	for i := len(msgs) - 1; i >= 0; i-- {
		m := msgs[i]
		if m.RandomID == randomID && m.SenderUserID == senderUserID && m.SavedPeer == savedPeer {
			return m, true
		}
	}
	return domain.ChannelMessage{}, false
}

// ListMonoforumHistory 拉取某订阅者(saved_peer)在 monoforum 内的私信历史,id 倒序分页。
func (s *ChannelStore) ListMonoforumHistory(_ context.Context, filter domain.MonoforumHistoryFilter) (domain.ChannelHistory, error) {
	if filter.MonoforumID == 0 || filter.SavedPeer.ID == 0 {
		return domain.ChannelHistory{}, domain.ErrChannelInvalid
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	channel, ok := s.channels[filter.MonoforumID]
	if !ok || !channel.Monoforum {
		return domain.ChannelHistory{}, domain.ErrChannelInvalid
	}
	limit := filter.Limit
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	all := s.messages[filter.MonoforumID]
	var msgs []domain.ChannelMessage
	count := 0
	for i := len(all) - 1; i >= 0; i-- {
		m := all[i]
		if m.Deleted || m.SavedPeer != filter.SavedPeer {
			continue
		}
		count++
		if filter.OffsetID > 0 && m.ID >= filter.OffsetID {
			continue
		}
		if len(msgs) < limit {
			msgs = append(msgs, cloneChannelMessage(m))
		}
	}
	return domain.ChannelHistory{Messages: msgs, Count: count, Channel: cloneChannel(channel)}, nil
}

// ResolveMonoforumSend 按 id 取 monoforum 频道(不要求调用者是 monoforum 成员——订阅者私信频道时
// 并非 monoforum 成员),并返回调用者是否可管理其母广播频道的 Direct Messages。非 monoforum/不存在 → ErrChannelInvalid。
func (s *ChannelStore) ResolveMonoforumSend(_ context.Context, viewerUserID, monoforumID int64) (domain.Channel, bool, error) {
	if viewerUserID == 0 || monoforumID == 0 {
		return domain.Channel{}, false, domain.ErrChannelInvalid
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	mono, ok := s.channels[monoforumID]
	if !ok || mono.Deleted || !mono.Monoforum || mono.LinkedMonoforumID == 0 {
		return domain.Channel{}, false, domain.ErrChannelInvalid
	}
	member, ok := s.members[mono.LinkedMonoforumID][viewerUserID]
	isAdmin := ok && member.CanManageDirectMessages()
	return cloneChannel(mono), isAdmin, nil
}

// ListMonoforumDialogs 列出 monoforum 的订阅者子会话(每个 saved_peer 一条,取其 top 消息),
// 按 top 消息 id 倒序分页。
func (s *ChannelStore) ListMonoforumDialogs(_ context.Context, filter domain.MonoforumDialogsFilter) (domain.MonoforumDialogList, error) {
	if filter.MonoforumID == 0 {
		return domain.MonoforumDialogList{}, domain.ErrChannelInvalid
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	channel, ok := s.channels[filter.MonoforumID]
	if !ok || !channel.Monoforum {
		return domain.MonoforumDialogList{}, domain.ErrChannelInvalid
	}
	limit := filter.Limit
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	tops := map[domain.Peer]domain.ChannelMessage{}
	for _, m := range s.messages[filter.MonoforumID] {
		if m.Deleted || m.SavedPeer.ID == 0 {
			continue
		}
		if cur, ok := tops[m.SavedPeer]; !ok || m.ID > cur.ID {
			tops[m.SavedPeer] = m
		}
	}
	ordered := make([]domain.ChannelMessage, 0, len(tops))
	for _, m := range tops {
		ordered = append(ordered, m)
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].ID > ordered[j].ID })
	out := domain.MonoforumDialogList{MonoforumID: filter.MonoforumID, Channel: cloneChannel(channel), Count: len(ordered)}
	for _, m := range ordered {
		if filter.OffsetID > 0 && m.ID >= filter.OffsetID {
			continue
		}
		if len(out.Dialogs) >= limit {
			break
		}
		out.Dialogs = append(out.Dialogs, domain.MonoforumDialog{SavedPeer: m.SavedPeer, TopMessageID: m.ID, TopMessageDate: m.Date})
		out.Messages = append(out.Messages, cloneChannelMessage(m))
	}
	return out, nil
}
