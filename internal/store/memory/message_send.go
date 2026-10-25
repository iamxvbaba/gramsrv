package memory

import (
	"context"
	"fmt"
	"telesrv/internal/domain"
	"telesrv/internal/store"
	"time"
)

type privateSendDedupKey struct {
	senderUserID int64
	randomID     int64
}

type privateSendDedupRecord struct {
	recipientUserID   int64
	senderSnapshot    []byte
	recipientMessage  domain.Message
	fingerprint       []byte
	senderDeleteEvent *domain.UpdateEvent
}

func (s *MessageStore) Create(_ context.Context, msg domain.Message) (domain.Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	msg.ID = s.nextBoxIDLocked(msg.OwnerUserID)
	msg.UID = s.nextUID
	s.nextUID++
	msg.Entities = append([]domain.MessageEntity(nil), msg.Entities...)
	s.m[msg.OwnerUserID] = append(s.m[msg.OwnerUserID], msg)
	if s.dialogs != nil {
		s.dialogs.mu.Lock()
		list := s.dialogs.m[msg.OwnerUserID]
		list.Messages = append(list.Messages, msg)
		if msg.Peer.Type == domain.PeerTypeUser && !hasUser(list.Users, msg.Peer.ID) {
			if u, ok := domain.SystemUserByID(msg.Peer.ID); ok {
				list.Users = append(list.Users, u)
			}
		}
		s.dialogs.m[msg.OwnerUserID] = list
		s.dialogs.mu.Unlock()
	}
	return msg, nil
}

func (s *MessageStore) SendPrivateText(_ context.Context, req domain.SendPrivateTextRequest) (domain.SendPrivateTextResult, error) {
	if !req.HasContent() {
		return domain.SendPrivateTextResult{}, domain.ErrMessageEmpty
	}
	fingerprint, err := store.PrivateSendFingerprint(req)
	if err != nil {
		return domain.SendPrivateTextResult{}, err
	}
	// Match TogglePrivateNoForwards lock order; only external private-source
	// replies read pair protection while holding the message mutation lock.
	if r := req.ReplyTo; r != nil && r.Peer.Type == domain.PeerTypeUser && r.Peer.ID > 0 && r.Peer.ID != req.RecipientUserID {
		s.noForwardsMu.Lock()
		defer s.noForwardsMu.Unlock()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sendPrivateTextLocked(req, fingerprint, nil)
}

// A prefix stages related state events before the message. apply is infallible
// and only runs after all validation and receipt encoding have succeeded.
type memoryPrivateSendPrefix struct {
	events []domain.UpdateEvent
	apply  func([]domain.UpdateEvent)
}

// Caller holds s.mu; this helper owns the dialog/event locks through commit.
func (s *MessageStore) sendPrivateTextLocked(req domain.SendPrivateTextRequest, fingerprint []byte, prefix *memoryPrivateSendPrefix) (domain.SendPrivateTextResult, error) {
	if err := s.checkScheduledDeliveryLocked(req.ScheduledDelivery, req.SenderUserID, domain.Peer{Type: domain.PeerTypeUser, ID: req.RecipientUserID}, req.RandomID); err != nil {
		return domain.SendPrivateTextResult{}, err
	}
	if replay, found, err := s.lookupPrivateSendReplayLocked(domain.PrivateSendReplayRequest{
		SenderUserID:           req.SenderUserID,
		RecipientUserID:        req.RecipientUserID,
		RandomID:               req.RandomID,
		IdempotencyFingerprint: fingerprint,
	}); err != nil || found {
		return replay, err
	}
	if req.Date == 0 {
		req.Date = int(time.Now().Unix())
	}
	senderReply, recipientReply, err := s.resolveMemoryReplyLocked(req)
	if err != nil {
		return domain.SendPrivateTextResult{}, err
	}
	dialogs := s.dialogs
	events := s.updateEvents
	if events == nil || (req.ClearDraft && dialogs == nil) {
		return domain.SendPrivateTextResult{}, store.ErrDeliveryOutboxRequired
	}
	if dialogs != nil {
		dialogs.mu.Lock()
		defer dialogs.mu.Unlock()
	}
	events.mu.Lock()
	defer events.mu.Unlock()

	var draftSnapshot store.DialogAccountMutationSnapshot
	var draftEffects []store.DeliveryEffect
	if req.ClearDraft {
		mutation := store.DialogAccountMutation{
			Kind: store.DialogAccountDeleteDraft, UserID: req.SenderUserID, Date: req.Date,
			Peer: domain.Peer{Type: domain.PeerTypeUser, ID: req.RecipientUserID},
		}
		_, changed := dialogs.drafts[mutation.UserID][draftKey(mutation.Peer, 0)]
		draftSnapshot = store.DialogAccountMutationSnapshot{Mutation: mutation, Changed: changed}
		draftEffects, err = sendDraftClearEffects(draftSnapshot)
		if err != nil {
			return domain.SendPrivateTextResult{}, err
		}
		if err := store.ValidateDialogAccountEffects(draftSnapshot, draftEffects); err != nil {
			return domain.SendPrivateTextResult{}, fmt.Errorf("validate memory private send draft effects: %w", err)
		}
	}
	// Stage counters as well as the message: failed encoding cannot consume PTS.
	allocatedPTS := make(map[int64]int)
	nextPTS := func(userID int64) int {
		current, ok := allocatedPTS[userID]
		if !ok {
			current = s.nextPts[userID]
			for _, event := range events.events[userID] {
				if event.Pts > current {
					current = event.Pts
				}
			}
		}
		allocatedPTS[userID] = current + 1
		return current + 1
	}
	var prefixEvents []domain.UpdateEvent
	if prefix != nil {
		for _, event := range prefix.events {
			event.Pts = nextPTS(event.UserID)
			prefixEvents = append(prefixEvents, event)
		}
	}
	uid := s.nextUID
	sender := domain.Message{
		ID:          s.nextBox[req.SenderUserID] + 1,
		UID:         uid,
		RandomID:    req.RandomID,
		OwnerUserID: req.SenderUserID,
		Peer:        domain.Peer{Type: domain.PeerTypeUser, ID: req.RecipientUserID},
		From:        domain.Peer{Type: domain.PeerTypeUser, ID: req.SenderUserID},
		Date:        req.Date,
		Out:         true,
		Silent:      req.Silent,
		NoForwards:  req.NoForwards,
		Body:        req.Message,
		Entities:    append([]domain.MessageEntity(nil), req.Entities...),
		Media:       cloneRequestedPeerMedia(req.Media),
		ViaBotID:    req.ViaBotID,
		GroupedID:   req.GroupedID,
		Effect:      req.Effect,
		ReplyMarkup: cloneReplyMarkup(req.ReplyMarkup),
		RichMessage: cloneRichMessage(req.RichMessage),
		ReplyTo:     cloneMessageReply(senderReply),
		Forward:     cloneMessageForward(req.Forward),
		Pts:         nextPTS(req.SenderUserID),
		// voice/round 在发送者副本上同样保持"未听"，由对端内容已读清除。
		MediaUnread: req.Media.HasUnreadPayload() && req.SenderUserID != req.RecipientUserID,
	}
	if req.SenderUserID == req.RecipientUserID {
		sender.SavedPeer = domain.SavedPeerForSelfChat(req.SenderUserID, req.Forward)
	}
	recipient := domain.Message{}
	if req.SenderUserID == req.RecipientUserID {
		recipient = sender
	}
	if req.SenderUserID != req.RecipientUserID && !req.RecipientBlocked {
		recipient = sender
		recipient.ID = s.nextBox[req.RecipientUserID] + 1
		recipient.OwnerUserID = req.RecipientUserID
		recipient.Peer = domain.Peer{Type: domain.PeerTypeUser, ID: req.SenderUserID}
		recipient.Out = false
		recipient.ReplyTo = cloneMessageReply(recipientReply)
		recipient.Media = cloneRequestedPeerMedia(sender.Media)
		// recipient = sender 是值拷贝，共享 sender.ReplyMarkup 指针/Data 切片——深拷
		// 让双盒各持独立快照（与 postgres 每盒独立 decode 对齐，I3/I2）。
		recipient.ReplyMarkup = cloneReplyMarkup(sender.ReplyMarkup)
		recipient.RichMessage = cloneRichMessage(sender.RichMessage)
		recipient.Pts = nextPTS(req.RecipientUserID)
		recipient.MediaUnread = req.Media.HasUnreadPayload()
	}
	var senderSnapshot []byte
	if req.RandomID != 0 {
		senderSnapshot, err = store.EncodePrivateSendSnapshot(sender)
		if err != nil {
			return domain.SendPrivateTextResult{}, err
		}
	}
	scheduledCompletion, err := s.stageScheduledCompletionLocked(req.ScheduledDelivery, sender.ID, req.Date)
	if err != nil {
		return domain.SendPrivateTextResult{}, err
	}
	if scheduledCompletion != nil {
		if _, err := s.deliveryOutbox.Enqueue(context.Background(), store.DeliveryOutboxEnqueue{
			TargetUserID: scheduledCompletion.effect.TargetUserID, ExcludeAuthKeyID: scheduledCompletion.effect.ExcludeAuthKeyID,
			ExcludeSessionID: scheduledCompletion.effect.ExcludeSessionID, Payload: scheduledCompletion.effect.Payload,
			RecoveryPolicy: scheduledCompletion.effect.RecoveryPolicy,
		}); err != nil {
			return domain.SendPrivateTextResult{}, fmt.Errorf("apply memory scheduled completion delivery: %w", err)
		}
	}
	// No fallible work follows: publish the prefix and message under all locks.
	if prefix != nil {
		prefix.apply(prefixEvents)
	}
	for _, event := range prefixEvents {
		auth, session := [8]byte{}, int64(0)
		if event.UserID == req.SenderUserID {
			auth, session = req.OriginAuthKeyID, req.OriginSessionID
		}
		appendMemorySendEventLocked(events, event.UserID, event, auth, session)
	}
	s.nextUID++
	s.nextBox[req.SenderUserID] = sender.ID
	if recipient.ID != 0 {
		s.nextBox[recipient.OwnerUserID] = recipient.ID
	}
	for userID, pts := range allocatedPTS {
		s.nextPts[userID] = pts
	}
	s.m[req.SenderUserID] = append(s.m[req.SenderUserID], sender)
	if req.SenderUserID != req.RecipientUserID && !req.RecipientBlocked {
		s.m[req.RecipientUserID] = append(s.m[req.RecipientUserID], recipient)
	}
	if req.RandomID != 0 {
		s.privateSendDedup[privateSendDedupKey{senderUserID: req.SenderUserID, randomID: req.RandomID}] = privateSendDedupRecord{
			recipientUserID:  req.RecipientUserID,
			senderSnapshot:   senderSnapshot,
			recipientMessage: immutablePrivateSendReceipt(recipient),
			fingerprint:      append([]byte(nil), fingerprint...),
		}
	}
	if s.dialogs != nil {
		if recipient.ID != 0 {
			s.upsertMemoryDialogsStateLocked(sender, recipient)
		} else {
			s.upsertMemoryDialogsStateLocked(sender, sender)
		}
	}
	originUserID := req.OriginUserID
	if originUserID == 0 {
		originUserID = req.SenderUserID
	}
	senderExcludeAuthKeyID, senderExcludeSessionID := [8]byte{}, int64(0)
	if originUserID == req.SenderUserID {
		senderExcludeAuthKeyID, senderExcludeSessionID = req.OriginAuthKeyID, req.OriginSessionID
	}
	appendMemorySendEventLocked(events, req.SenderUserID, newMessageEvent(sender), senderExcludeAuthKeyID, senderExcludeSessionID)
	if recipient.ID != 0 && recipient.OwnerUserID != sender.OwnerUserID {
		recipientExcludeAuthKeyID, recipientExcludeSessionID := [8]byte{}, int64(0)
		if originUserID == req.RecipientUserID {
			recipientExcludeAuthKeyID, recipientExcludeSessionID = req.OriginAuthKeyID, req.OriginSessionID
		}
		appendMemorySendEventLocked(events, req.RecipientUserID, newMessageEvent(recipient), recipientExcludeAuthKeyID, recipientExcludeSessionID)
	}
	var draftEvent domain.UpdateEvent
	if len(draftEffects) == 1 {
		delete(dialogs.drafts[req.SenderUserID], draftKey(draftSnapshot.Mutation.Peer, 0))
		draftEvent = events.appendLocked(req.SenderUserID, draftEffects[0].Event, true)
		events.dispatches[req.SenderUserID] = append(events.dispatches[req.SenderUserID], memoryUpdateDispatch{Pts: draftEvent.Pts})
		if draftEvent.Pts > s.nextPts[req.SenderUserID] {
			s.nextPts[req.SenderUserID] = draftEvent.Pts
		}
	}
	return domain.SendPrivateTextResult{
		SenderMessage:    cloneMessage(sender),
		RecipientMessage: cloneMessage(recipient),
		SenderEvent:      newMessageEvent(sender),
		RecipientEvent:   newMessageEvent(recipient),
		DraftEvent:       cloneUpdateEvent(draftEvent),
		NextScheduled:    s.publishScheduledCompletionLocked(scheduledCompletion),
	}, nil
}

func (s *MessageStore) nextPtsWithEventsLocked(events *UpdateEventStore, userID int64) int {
	return s.nextPtsNWithEventsLocked(events, userID, 1)
}

func (s *MessageStore) nextPtsNWithEventsLocked(events *UpdateEventStore, userID int64, count int) int {
	if count <= 0 {
		count = 1
	}
	current := s.nextPts[userID]
	for _, event := range events.events[userID] {
		if event.Pts > current {
			current = event.Pts
		}
	}
	current += count
	s.nextPts[userID] = current
	return current
}

func appendMemorySendEventLocked(events *UpdateEventStore, userID int64, event domain.UpdateEvent, excludeAuthKeyID [8]byte, excludeSessionID int64) {
	event = events.appendLocked(userID, event, false)
	events.dispatches[userID] = append(events.dispatches[userID], memoryUpdateDispatch{
		Pts: event.Pts, ExcludeAuthKeyID: excludeAuthKeyID, ExcludeSessionID: excludeSessionID,
	})
}

func appendMemoryAllocatedMessageEventLocked(s *MessageStore, events *UpdateEventStore, userID int64, event domain.UpdateEvent, excludeAuthKeyID [8]byte, excludeSessionID int64) domain.UpdateEvent {
	event.Pts = s.nextPtsNWithEventsLocked(events, userID, event.PtsCount)
	event = events.appendLocked(userID, event, false)
	events.dispatches[userID] = append(events.dispatches[userID], memoryUpdateDispatch{
		Pts: event.Pts, ExcludeAuthKeyID: excludeAuthKeyID, ExcludeSessionID: excludeSessionID,
	})
	return event
}

func sendDraftClearEffects(snapshot store.DialogAccountMutationSnapshot) ([]store.DeliveryEffect, error) {
	if snapshot.Mutation.Kind != store.DialogAccountDeleteDraft {
		return nil, fmt.Errorf("memory private send draft projection: unexpected mutation %q", snapshot.Mutation.Kind)
	}
	if !snapshot.Changed {
		return nil, nil
	}
	event := domain.UpdateEvent{
		Type: domain.UpdateEventDraftMessage, Date: snapshot.Mutation.Date, PtsCount: 1,
		Peer: snapshot.Mutation.Peer, MaxID: snapshot.Mutation.TopMessageID,
	}
	return []store.DeliveryEffect{store.AccountPTSDeliveryEffect(snapshot.Mutation.UserID, event, [8]byte{}, 0)}, nil
}

// LookupPrivateSendReplay returns an existing immutable/current replay receipt without running
// any send permission, reply resolution or allocation path.
func (s *MessageStore) LookupPrivateSendReplay(_ context.Context, req domain.PrivateSendReplayRequest) (domain.SendPrivateTextResult, bool, error) {
	if req.SenderUserID == 0 || req.RecipientUserID == 0 || req.RandomID == 0 {
		return domain.SendPrivateTextResult{}, false, fmt.Errorf("memory private send replay: invalid scope")
	}
	if err := store.ValidateSendFingerprint(req.IdempotencyFingerprint, "private send replay"); err != nil {
		return domain.SendPrivateTextResult{}, false, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.lookupPrivateSendReplayLocked(req)
}

func (s *MessageStore) lookupPrivateSendReplayLocked(req domain.PrivateSendReplayRequest) (domain.SendPrivateTextResult, bool, error) {
	record, ok := s.privateSendDedup[privateSendDedupKey{senderUserID: req.SenderUserID, randomID: req.RandomID}]
	if !ok {
		return domain.SendPrivateTextResult{}, false, nil
	}
	if record.recipientUserID != req.RecipientUserID || !store.SameSendFingerprint(record.fingerprint, req.IdempotencyFingerprint) {
		return domain.SendPrivateTextResult{}, false, domain.ErrMessageRandomIDDuplicate
	}
	firstSender, err := store.DecodePrivateSendSnapshot(record.senderSnapshot)
	if err != nil {
		return domain.SendPrivateTextResult{}, false, fmt.Errorf("memory duplicate private message snapshot: %w", err)
	}
	sender := firstSender
	visible := false
	for _, current := range s.m[req.SenderUserID] {
		if current.UID == firstSender.UID && current.ID == firstSender.ID {
			sender = cloneMessage(current)
			sender.RandomID = firstSender.RandomID
			visible = true
			break
		}
	}
	if !visible && record.senderDeleteEvent == nil {
		return domain.SendPrivateTextResult{}, false, fmt.Errorf("memory duplicate private message %d is absent without a durable sender delete receipt", firstSender.UID)
	}
	recipient := cloneMessage(record.recipientMessage)
	var replayDelete *domain.UpdateEvent
	if record.senderDeleteEvent != nil {
		cloned := cloneUpdateEvent(*record.senderDeleteEvent)
		replayDelete = &cloned
	}
	return domain.SendPrivateTextResult{
		SenderMessage:     sender,
		RecipientMessage:  recipient,
		SenderEvent:       newMessageEvent(firstSender),
		RecipientEvent:    newMessageEvent(recipient),
		Duplicate:         true,
		ReplayDeleteEvent: replayDelete,
	}, true, nil
}

// immutablePrivateSendReceipt keeps the recipient allocation facts used by
// store-level idempotency tests. The sender response snapshot is stored as a
// versioned JSON value above so all nested media/reply graphs are immutable.
func immutablePrivateSendReceipt(msg domain.Message) domain.Message {
	if msg.ID == 0 {
		return domain.Message{}
	}
	return domain.Message{
		ID:          msg.ID,
		UID:         msg.UID,
		RandomID:    msg.RandomID,
		OwnerUserID: msg.OwnerUserID,
		Peer:        msg.Peer,
		From:        msg.From,
		Date:        msg.Date,
		Out:         msg.Out,
		Pts:         msg.Pts,
	}
}

func (s *MessageStore) resolveMemoryReplyLocked(req domain.SendPrivateTextRequest) (*domain.MessageReply, *domain.MessageReply, error) {
	if req.ReplyTo == nil {
		return nil, nil, nil
	}
	if err := domain.ValidateMessageReplyBounds(req.ReplyTo); err != nil {
		return nil, nil, err
	}
	if req.ReplyTo.StoryID > 0 {
		// story 回复（评论）：无源消息可查；story 作者就是会话对端，双盒同持。
		reply := &domain.MessageReply{
			StoryID: req.ReplyTo.StoryID,
			Peer:    domain.Peer{Type: domain.PeerTypeUser, ID: req.RecipientUserID},
		}
		return cloneMessageReply(reply), cloneMessageReply(reply), nil
	}
	peer := req.ReplyTo.Peer
	if peer.ID == 0 {
		peer = domain.Peer{Type: domain.PeerTypeUser, ID: req.RecipientUserID}
	}
	if peer.Type != domain.PeerTypeUser || req.ReplyTo.External != nil {
		return nil, nil, domain.ErrReplyMessageIDInvalid
	}
	var target domain.Message
	for _, msg := range s.m[req.SenderUserID] {
		if msg.Peer == peer && msg.ID == req.ReplyTo.MessageID {
			target = msg
			break
		}
	}
	if target.ID == 0 {
		return nil, nil, domain.ErrReplyMessageIDInvalid
	}
	senderReply := cloneMessageReply(req.ReplyTo)
	senderReply.MessageID = target.ID
	senderReply.Peer = peer
	if peer.ID != req.RecipientUserID {
		protected := target.NoForwards || (target.Media != nil && target.Media.TTLSeconds > 0)
		if pair, ok := noForwardsPair(req.SenderUserID, peer.ID); ok {
			protected = protected || s.privateNoForwards[pair].Enabled()
		}
		if protected {
			return nil, nil, domain.ErrChatForwardsRestricted
		}
		if err := domain.ValidateExternalReplyQuote(req.ReplyTo, target.Body); err != nil {
			return nil, nil, err
		}
		var err error
		senderReply.External, err = domain.NewMessageReplyExternal(target)
		if err != nil {
			return nil, nil, err
		}
		recipientReply := cloneMessageReply(senderReply)
		if req.SenderUserID != req.RecipientUserID {
			recipientReply.MessageID = 0
			recipientReply.TopMessageID = 0
		}
		return senderReply, recipientReply, nil
	}
	if req.SenderUserID == req.RecipientUserID {
		return senderReply, cloneMessageReply(senderReply), nil
	}
	for _, msg := range s.m[req.RecipientUserID] {
		if msg.UID == target.UID {
			recipientReply := cloneMessageReply(senderReply)
			recipientReply.MessageID = msg.ID
			recipientReply.Peer = domain.Peer{Type: domain.PeerTypeUser, ID: req.SenderUserID}
			return senderReply, recipientReply, nil
		}
	}
	return senderReply, nil, nil
}

func (s *MessageStore) upsertMemoryDialogsStateLocked(sender, recipient domain.Message) {
	list := s.dialogs.m[sender.OwnerUserID]
	list = upsertMemoryDialog(list, domain.Dialog{Peer: sender.Peer, TopMessage: sender.ID, TopMessageDate: sender.Date})
	// 发送方向清手动未读标记（对齐 postgres UpsertOutboxDialog 与
	// channel 发送路径：向会话发出消息即视为已知晓内容）。
	for i := range list.Dialogs {
		if list.Dialogs[i].Peer == sender.Peer {
			list.Dialogs[i].UnreadMark = false
			break
		}
	}
	list.Messages = append(list.Messages, sender)
	s.dialogs.m[sender.OwnerUserID] = list
	if recipient.OwnerUserID != sender.OwnerUserID {
		peerList := s.dialogs.m[recipient.OwnerUserID]
		peerList = upsertMemoryDialog(peerList, domain.Dialog{
			Peer:           recipient.Peer,
			TopMessage:     recipient.ID,
			TopMessageDate: recipient.Date,
			UnreadCount:    s.privateUnreadCountLocked(recipient.OwnerUserID, recipient.Peer),
		})
		peerList.Messages = append(peerList.Messages, recipient)
		s.dialogs.m[recipient.OwnerUserID] = peerList
	}
}

func (s *MessageStore) privateUnreadCountLocked(ownerUserID int64, peer domain.Peer) int {
	readMax := 0
	if s.dialogs != nil {
		if list, ok := s.dialogs.m[ownerUserID]; ok {
			for _, dialog := range list.Dialogs {
				if dialog.Peer == peer {
					readMax = dialog.ReadInboxMaxID
					break
				}
			}
		}
	}
	unread := 0
	for _, msg := range s.m[ownerUserID] {
		if msg.Peer == peer && !msg.Out && msg.ID > readMax {
			unread++
		}
	}
	return unread
}
