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
	s.mu.Lock()
	defer s.mu.Unlock()
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
	uid := s.nextUID
	s.nextUID++
	sender := domain.Message{
		ID:          s.nextBoxIDLocked(req.SenderUserID),
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
		Media:       req.Media,
		ViaBotID:    req.ViaBotID,
		GroupedID:   req.GroupedID,
		Effect:      req.Effect,
		ReplyMarkup: cloneReplyMarkup(req.ReplyMarkup),
		RichMessage: cloneRichMessage(req.RichMessage),
		ReplyTo:     cloneMessageReply(senderReply),
		Forward:     cloneMessageForward(req.Forward),
		Pts:         s.nextPtsLocked(req.SenderUserID),
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
		recipient.ID = s.nextBoxIDLocked(req.RecipientUserID)
		recipient.OwnerUserID = req.RecipientUserID
		recipient.Peer = domain.Peer{Type: domain.PeerTypeUser, ID: req.SenderUserID}
		recipient.Out = false
		recipient.ReplyTo = cloneMessageReply(recipientReply)
		// recipient = sender 是值拷贝，共享 sender.ReplyMarkup 指针/Data 切片——深拷
		// 让双盒各持独立快照（与 postgres 每盒独立 decode 对齐，I3/I2）。
		recipient.ReplyMarkup = cloneReplyMarkup(sender.ReplyMarkup)
		recipient.RichMessage = cloneRichMessage(sender.RichMessage)
		recipient.Pts = s.nextPtsLocked(req.RecipientUserID)
		recipient.MediaUnread = req.Media.HasUnreadPayload()
	}
	var senderSnapshot []byte
	if req.RandomID != 0 {
		senderSnapshot, err = store.EncodePrivateSendSnapshot(sender)
		if err != nil {
			return domain.SendPrivateTextResult{}, err
		}
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
			s.upsertMemoryDialogsLocked(sender, recipient)
		} else {
			s.upsertMemoryDialogsLocked(sender, sender)
		}
	}
	return domain.SendPrivateTextResult{
		SenderMessage:    cloneMessage(sender),
		RecipientMessage: cloneMessage(recipient),
		SenderEvent:      newMessageEvent(sender),
		RecipientEvent:   newMessageEvent(recipient),
	}, nil
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
	if peer.Type != domain.PeerTypeUser || peer.ID != req.RecipientUserID {
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

func (s *MessageStore) upsertMemoryDialogsLocked(sender, recipient domain.Message) {
	s.dialogs.mu.Lock()
	defer s.dialogs.mu.Unlock()
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
