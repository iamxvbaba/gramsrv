package memory

import (
	"context"
	"fmt"
	"time"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

type loginCodeDeliveryRecord struct {
	userID           int64
	codeFingerprint  [32]byte
	privateMessageID int64
	messageBoxID     int
	pts              int
	messageDate      int
	body             string
	entities         []domain.MessageEntity
}

// LoginCodeDeliveryStore composes the message projection with the same
// durable update-event store consumed by updates.getDifference. Keeping this
// as an explicit dependency prevents tests (and future in-memory runtimes)
// from accidentally creating a visible message without its pts event.
type LoginCodeDeliveryStore struct {
	messages *MessageStore
	events   *UpdateEventStore
}

func NewLoginCodeDeliveryStore(messages *MessageStore, events *UpdateEventStore) *LoginCodeDeliveryStore {
	return &LoginCodeDeliveryStore{messages: messages, events: events}
}

// DeliverLoginCodeMessage is the in-memory equivalent of the PostgreSQL
// transaction. Message, dialog, pts, durable event and immutable receipt are
// published under the two backing stores' locks; a repeated phone_code_hash
// returns the first snapshot.
func (s *LoginCodeDeliveryStore) DeliverLoginCodeMessage(_ context.Context, req domain.LoginCodeDeliveryRequest) (domain.LoginCodeDeliveryResult, error) {
	if s == nil || s.messages == nil || s.events == nil {
		return domain.LoginCodeDeliveryResult{}, fmt.Errorf("memory login code delivery: %w: message and update stores are required", domain.ErrLoginCodeDeliveryInvalid)
	}
	deliveryKey, err := store.LoginCodeDeliveryKey(req.PhoneCodeHash)
	if err != nil {
		return domain.LoginCodeDeliveryResult{}, err
	}
	codeFingerprint, err := store.LoginCodeFingerprint(req.PhoneCodeHash, req.Code)
	if err != nil {
		return domain.LoginCodeDeliveryResult{}, err
	}
	if req.Date == 0 {
		req.Date = int(time.Now().Unix())
	}
	if req.ExpiresAt <= int64(req.Date) {
		return domain.LoginCodeDeliveryResult{}, fmt.Errorf("memory login code receipt expiry: %w: date=%d expires_at=%d", domain.ErrLoginCodeDeliveryInvalid, req.Date, req.ExpiresAt)
	}
	base, err := store.LoginCodeMessageFromDeliveryRequest(req)
	if err != nil {
		return domain.LoginCodeDeliveryResult{}, err
	}

	// Lock ordering is local and fixed: MessageStore -> UpdateEventStore ->
	// DialogStore. No other memory operation takes the first two together.
	s.messages.mu.Lock()
	defer s.messages.mu.Unlock()
	s.events.mu.Lock()
	defer s.events.mu.Unlock()
	if receipt, ok := s.messages.loginCodeDeliveries[deliveryKey]; ok {
		if receipt.userID != req.UserID || !store.SameLoginCodeFingerprint(receipt.codeFingerprint[:], codeFingerprint) {
			return domain.LoginCodeDeliveryResult{}, fmt.Errorf("memory login code delivery: %w", domain.ErrLoginCodeDeliveryConflict)
		}
		msg, err := store.RestoreLoginCodeDeliveryMessage(
			receipt.userID,
			req.Code,
			receipt.messageDate,
			receipt.privateMessageID,
			receipt.messageBoxID,
			receipt.pts,
			receipt.body,
			receipt.entities,
		)
		if err != nil {
			return domain.LoginCodeDeliveryResult{}, fmt.Errorf("memory login code delivery replay: %w", err)
		}
		return domain.LoginCodeDeliveryResult{Message: msg, Created: false}, nil
	}

	currentEventPts := 0
	for _, event := range s.events.events[req.UserID] {
		if event.Pts > currentEventPts {
			currentEventPts = event.Pts
		}
	}
	if messagePts := s.messages.nextPts[req.UserID]; messagePts > currentEventPts {
		return domain.LoginCodeDeliveryResult{}, fmt.Errorf("memory login code delivery: %w: message pts %d exceeds durable event pts %d", domain.ErrLoginCodeDeliveryInvalid, messagePts, currentEventPts)
	}

	base.ID = s.messages.nextBoxIDLocked(req.UserID)
	base.UID = s.messages.nextUID
	s.messages.nextUID++
	base.Pts = currentEventPts + 1

	s.messages.nextPts[req.UserID] = base.Pts
	s.messages.m[req.UserID] = append(s.messages.m[req.UserID], cloneMessage(base))
	if s.messages.dialogs != nil {
		s.messages.dialogs.mu.Lock()
		list := s.messages.dialogs.m[req.UserID]
		list = upsertMemoryDialog(list, domain.Dialog{
			Peer:           base.Peer,
			TopMessage:     base.ID,
			TopMessageDate: base.Date,
			UnreadCount:    s.messages.privateUnreadCountLocked(req.UserID, base.Peer),
		})
		if !hasUser(list.Users, domain.OfficialSystemUserID) {
			list.Users = append(list.Users, domain.OfficialSystemUser())
		}
		list.Messages = append(list.Messages, cloneMessage(base))
		s.messages.dialogs.m[req.UserID] = list
		s.messages.dialogs.mu.Unlock()
	}
	event := newMessageEvent(base)
	s.events.events[req.UserID] = append(s.events.events[req.UserID], cloneUpdateEvent(event))
	s.messages.loginCodeDeliveries[deliveryKey] = loginCodeDeliveryRecord{
		userID:           req.UserID,
		codeFingerprint:  codeFingerprint,
		privateMessageID: base.UID,
		messageBoxID:     base.ID,
		pts:              base.Pts,
		messageDate:      base.Date,
		body:             base.Body,
		entities:         append([]domain.MessageEntity(nil), base.Entities...),
	}
	return domain.LoginCodeDeliveryResult{Message: cloneMessage(base), Created: true}, nil
}
