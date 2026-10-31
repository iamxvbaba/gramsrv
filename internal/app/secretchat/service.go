package secretchat

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

// Service implements the secret-chat handshake state machine + qts message delivery. Every
// domain.SecretChat returned is a snapshot at that moment. Access checks (self/bot/blocking/
// privacy) happen first at the rpc layer; this layer does DH validation, chat_id wire
// invariants, access_hash allocation, state-machine transitions, and qts queue writes. The
// binding dimension is the device-level perm auth_key (int64).
type Service struct {
	store store.SecretChatStore
	queue store.EncryptedQueueStore
}

// NewService creates the secret-chat service.
func NewService(st store.SecretChatStore, queue store.EncryptedQueueStore) *Service {
	return &Service{store: st, queue: queue}
}

// RequestEncryption accepts requestEncryption: validate g_a → validate global uniqueness of
// random_id/chat_id → allocate both access_hash values → blindly store g_a → enter the
// requested state. The returned secret chat is projected by the rpc layer as the admin view
// encryptedChatWaiting (synchronous response) and the participant view encryptedChatRequested
// (push).
func (s *Service) RequestEncryption(ctx context.Context, req domain.SecretChatRequest) (domain.SecretChat, error) {
	if req.AdminUserID == 0 || req.ParticipantUserID == 0 || req.AdminAuthKeyID == 0 {
		return domain.SecretChat{}, ErrGAInvalid
	}
	if req.RandomID == 0 {
		return domain.SecretChat{}, domain.ErrSecretChatRandomIDDuplicate
	}
	ga, err := validateDHParam(req.GA)
	if err != nil {
		return domain.SecretChat{}, err
	}
	// Telegram wire contract: requestEncryption.random_id is also the chat_id. TDLib first
	// creates a local SecretChatActor with random_id and strictly verifies response.id equality
	// when consuming the response; using a server-side sequence to replace it is forbidden. A
	// global primary-key collision is allowed only for a network replay of the same intent;
	// everything else is an explicit duplicate.
	chatID := int(req.RandomID)
	if existing, ok, err := s.store.GetSecretChat(ctx, chatID); err != nil {
		return domain.SecretChat{}, err
	} else if ok {
		if sameSecretChatRequest(existing, req, ga) && !existing.Terminal() {
			return existing, nil
		}
		return domain.SecretChat{}, domain.ErrSecretChatRandomIDDuplicate
	}
	adminAH, err := randomAccessHash()
	if err != nil {
		return domain.SecretChat{}, err
	}
	participantAH, err := randomAccessHash()
	if err != nil {
		return domain.SecretChat{}, err
	}
	chat := domain.SecretChat{
		ID:                    chatID,
		AdminAccessHash:       adminAH,
		ParticipantAccessHash: participantAH,
		AdminUserID:           req.AdminUserID,
		ParticipantUserID:     req.ParticipantUserID,
		AdminAuthKeyID:        req.AdminAuthKeyID,
		State:                 domain.SecretChatStateRequested,
		GA:                    ga,
		RandomID:              req.RandomID,
		Date:                  req.Date,
	}
	if err := s.store.CreateSecretChat(ctx, chat); err == nil {
		return chat, nil
	} else if !errors.Is(err, domain.ErrSecretChatRandomIDDuplicate) {
		return domain.SecretChat{}, err
	}
	// A concurrent identical request may have been inserted by another goroutine after the
	// pre-check; converge to idempotent success only when a re-read still proves it is exactly
	// the same intent.
	existing, ok, getErr := s.store.GetSecretChat(ctx, chatID)
	if getErr != nil {
		return domain.SecretChat{}, getErr
	}
	if ok && sameSecretChatRequest(existing, req, ga) && !existing.Terminal() {
		return existing, nil
	}
	return domain.SecretChat{}, domain.ErrSecretChatRandomIDDuplicate
}

func sameSecretChatRequest(chat domain.SecretChat, req domain.SecretChatRequest, normalizedGA []byte) bool {
	return chat.ID == int(req.RandomID) && chat.RandomID == req.RandomID &&
		chat.AdminUserID == req.AdminUserID && chat.AdminAuthKeyID == req.AdminAuthKeyID &&
		chat.ParticipantUserID == req.ParticipantUserID && bytes.Equal(chat.GA, normalizedGA)
}

// AcceptEncryption accepts acceptEncryption: locate + verify the participant-view
// access_hash → validate g_b → atomically CAS-bind the accepting device and persist
// g_b/key_fingerprint → normal. The returned secret chat is projected by the rpc layer as the
// participant view encryptedChat (GAOrB=g_a, synchronous response) and the admin view
// encryptedChat (GAOrB=g_b, push).
func (s *Service) AcceptEncryption(ctx context.Context, chatID int, viewerUserID, participantAuthKeyID, accessHash int64, gb []byte, keyFingerprint int64) (domain.SecretChat, error) {
	gbPadded, err := validateDHParam(gb)
	if err != nil {
		return domain.SecretChat{}, err
	}
	chat, ok, err := s.store.GetSecretChat(ctx, chatID)
	if err != nil {
		return domain.SecretChat{}, err
	}
	// View check: the caller must be the recipient themselves and access_hash must match the participant view.
	if !ok || chat.ParticipantUserID != viewerUserID || chat.ParticipantAccessHash != accessHash {
		return domain.SecretChat{}, domain.ErrSecretChatNotFound
	}
	switch chat.State {
	case domain.SecretChatStateNormal:
		return domain.SecretChat{}, domain.ErrSecretChatAlreadyAccepted
	case domain.SecretChatStateDiscarded:
		return domain.SecretChat{}, domain.ErrSecretChatAlreadyDeclined
	}
	return s.store.AcceptSecretChat(ctx, chatID, participantAuthKeyID, gbPadded, keyFingerprint)
}

// DiscardEncryption accepts discardEncryption: locate + verify participant/bound device →
// transition to discarded. already=true means it was already terminal (idempotent success).
// The returned secret chat is projected by the rpc layer as a peer encryptedChatDiscarded
// push.
func (s *Service) DiscardEncryption(ctx context.Context, chatID int, viewerUserID, viewerAuthKeyID int64, deleteHistory bool) (domain.SecretChat, bool, error) {
	chat, ok, err := s.store.GetSecretChat(ctx, chatID)
	if err != nil {
		return domain.SecretChat{}, false, err
	}
	if !ok || !chat.HasParticipant(viewerUserID) || viewerAuthKeyID == 0 {
		return domain.SecretChat{}, false, domain.ErrSecretChatNotFound
	}
	// Admin is bound from request onward; participant has no binding before accept, so any
	// participant device that received the account-level invitation may decline. Once accept
	// completes, all operations from both sides must come from their respective bound devices.
	boundAuthKeyID := chat.AuthKeyOf(viewerUserID)
	if boundAuthKeyID != 0 && boundAuthKeyID != viewerAuthKeyID {
		return domain.SecretChat{}, false, domain.ErrSecretChatNotFound
	}
	if boundAuthKeyID == 0 && viewerUserID != chat.ParticipantUserID {
		return domain.SecretChat{}, false, domain.ErrSecretChatNotFound
	}
	return s.store.DiscardSecretChat(ctx, chatID, deleteHistory)
}

// GetSecretChat fetches a secret-chat snapshot (for the rpc layer's access checks).
func (s *Service) GetSecretChat(ctx context.Context, chatID int) (domain.SecretChat, bool, error) {
	return s.store.GetSecretChat(ctx, chatID)
}

// DiscardForAuthKey cascades discard on all active secret chats bound to this device's perm
// auth_key (as admin or participant), used for device logout / authorization revocation. It
// returns the secret-chat snapshots actually transitioned from non-terminal to discarded this
// time (already-terminal ones are not returned), so the rpc layer can push
// encryptedChatDiscarded to the peer. Blind relay: history is not deleted
// (history_deleted=false; the peer decides its own local handling). On error it returns the
// successfully discarded part plus err, so the caller can still notify those peers (logout/
// revocation is best-effort and does not roll back on this account).
func (s *Service) DiscardForAuthKey(ctx context.Context, authKeyID int64) ([]domain.SecretChat, error) {
	if authKeyID == 0 {
		return nil, nil
	}
	chats, err := s.store.ListActiveSecretChatsByAuthKey(ctx, authKeyID)
	if err != nil {
		return nil, err
	}
	var discarded []domain.SecretChat
	for _, c := range chats {
		updated, already, derr := s.store.DiscardSecretChat(ctx, c.ID, false)
		if derr != nil {
			if errors.Is(derr, domain.ErrSecretChatNotFound) {
				continue
			}
			return discarded, derr
		}
		if !already {
			discarded = append(discarded, updated)
		}
	}
	return discarded, nil
}

// SendEncrypted accepts sendEncrypted*: locate + verify the sender's bound device/access_hash
// + state must be normal → allocate a qts for [the peer's bound device] and write the opaque
// bytes into the delivery queue (idempotent: the same chat+random_id returns the existing
// qts/date). Returns the secret-chat snapshot + the persisted message (carrying qts/date, from
// which the rpc layer pushes updateNewEncryptedMessage and replies
// SentEncryptedMessage{date}). Blind relay: bytes are not decrypted.
func (s *Service) SendEncrypted(ctx context.Context, chatID int, viewerUserID, viewerAuthKeyID, accessHash int64, delivery domain.SecretMessageDelivery) (domain.SecretChat, domain.SecretChatMessage, error) {
	chat, ok, err := s.store.GetSecretChat(ctx, chatID)
	if err != nil {
		return domain.SecretChat{}, domain.SecretChatMessage{}, err
	}
	if !ok || !chat.HasParticipant(viewerUserID) || viewerAuthKeyID == 0 ||
		chat.AuthKeyOf(viewerUserID) != viewerAuthKeyID || chat.AccessHashFor(viewerUserID) != accessHash {
		return domain.SecretChat{}, domain.SecretChatMessage{}, domain.ErrSecretChatNotFound
	}
	if chat.State != domain.SecretChatStateNormal {
		// An unformed/destroyed secret chat cannot send or receive (CHAT_ID_INVALID).
		return domain.SecretChat{}, domain.SecretChatMessage{}, domain.ErrSecretChatNotFound
	}
	receiverUserID := chat.PeerOf(viewerUserID)
	receiverAuthKeyID := chat.AdminAuthKeyID
	if chat.IsAdmin(viewerUserID) {
		receiverAuthKeyID = chat.ParticipantAuthKeyID
	}
	if receiverUserID == 0 || receiverAuthKeyID == 0 {
		return domain.SecretChat{}, domain.SecretChatMessage{}, domain.ErrSecretChatNotFound
	}
	stored, _, err := s.queue.AppendEncryptedMessage(ctx, domain.SecretChatMessage{
		ReceiverAuthKeyID: receiverAuthKeyID,
		ReceiverUserID:    receiverUserID,
		ChatID:            chatID,
		RandomID:          delivery.RandomID,
		Date:              delivery.Date,
		IsService:         delivery.IsService,
		Bytes:             delivery.Bytes,
		File:              delivery.File,
	})
	if err != nil {
		return domain.SecretChat{}, domain.SecretChatMessage{}, err
	}
	return chat, stored, nil
}

// ListNewMessages returns a device's consecutive encrypted messages with qts > sinceQts (for getDifference catch-up).
func (s *Service) ListNewMessages(ctx context.Context, deviceAuthKeyID int64, sinceQts, limit int) ([]domain.SecretChatMessage, error) {
	if deviceAuthKeyID == 0 {
		return nil, nil
	}
	return s.queue.ListEncryptedMessagesSince(ctx, deviceAuthKeyID, sinceQts, limit)
}

// DeviceReservedQts returns the highest qts currently allocated for a device (for getState).
func (s *Service) DeviceReservedQts(ctx context.Context, deviceAuthKeyID int64) (int, error) {
	if deviceAuthKeyID == 0 {
		return 0, nil
	}
	return s.queue.ReservedQts(ctx, deviceAuthKeyID)
}

// AckQueue advances a device's confirmed qts and marks it acked (receivedQueue).
func (s *Service) AckQueue(ctx context.Context, deviceAuthKeyID int64, maxQts int) error {
	if deviceAuthKeyID == 0 || maxQts <= 0 {
		return nil
	}
	return s.queue.AckEncryptedMessages(ctx, deviceAuthKeyID, maxQts)
}

// RecordEncryptionEvent writes a durable updateEncryption state event (offline catch-up).
// targetAuthKeyID=0 means account-level (an invitation/withdrawal before link setup is
// visible to all of the target's devices); non-zero means directed at a bound device. At
// delivery time it is rebuilt from the authoritative secret_chats state (no frozen snapshot).
func (s *Service) RecordEncryptionEvent(ctx context.Context, chatID int, targetUserID, targetAuthKeyID int64, date int) error {
	if targetUserID == 0 {
		return nil
	}
	_, err := s.queue.AppendStateEvent(ctx, domain.EncryptedStateEvent{
		TargetUserID:    targetUserID,
		TargetAuthKeyID: targetAuthKeyID,
		ChatID:          chatID,
		Type:            domain.EncryptedStateEventEncryption,
		Date:            date,
	})
	return err
}

// RecordReadEvent writes a durable updateEncryptedMessagesRead state event (offline catch-up, device-directed).
func (s *Service) RecordReadEvent(ctx context.Context, chatID int, targetUserID, targetAuthKeyID int64, maxDate, date int) error {
	if targetUserID == 0 {
		return nil
	}
	_, err := s.queue.AppendStateEvent(ctx, domain.EncryptedStateEvent{
		TargetUserID:    targetUserID,
		TargetAuthKeyID: targetAuthKeyID,
		ChatID:          chatID,
		Type:            domain.EncryptedStateEventRead,
		MaxDate:         maxDate,
		Date:            date,
	})
	return err
}

// ListStateEvents returns a device's undelivered secret-chat state events (for getDifference catch-up).
func (s *Service) ListStateEvents(ctx context.Context, userID, deviceAuthKeyID int64, limit int) ([]domain.EncryptedStateEvent, error) {
	if userID == 0 || deviceAuthKeyID == 0 {
		return nil, nil
	}
	return s.queue.ListUndeliveredStateEvents(ctx, userID, deviceAuthKeyID, limit)
}

// MarkStateEventsDelivered records that these state events were delivered to a device.
func (s *Service) MarkStateEventsDelivered(ctx context.Context, deviceAuthKeyID int64, eventIDs []int64) error {
	if deviceAuthKeyID == 0 || len(eventIDs) == 0 {
		return nil
	}
	return s.queue.MarkStateEventsDelivered(ctx, deviceAuthKeyID, eventIDs)
}

// PutEncryptedFile persists a secret-chat file metadata snapshot (written once after creation).
func (s *Service) PutEncryptedFile(ctx context.Context, ownerUserID int64, ref domain.EncryptedFileRef) error {
	if ref.ID == 0 {
		return nil
	}
	return s.queue.PutEncryptedFile(ctx, ownerUserID, ref)
}

// GetEncryptedFile looks up a file snapshot by id + access_hash (the inputEncryptedFile reuse path).
func (s *Service) GetEncryptedFile(ctx context.Context, id, accessHash int64) (domain.EncryptedFileRef, bool, error) {
	return s.queue.GetEncryptedFile(ctx, id, accessHash)
}

// randomAccessHash generates a positive int64 access_hash (8 random bytes → clear the high bit to keep it positive, set 0 to 1).
func randomAccessHash() (int64, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return 0, fmt.Errorf("secretchat: access hash rand: %w", err)
	}
	v := int64(binary.BigEndian.Uint64(b[:]) >> 1)
	if v == 0 {
		v = 1
	}
	return v, nil
}
