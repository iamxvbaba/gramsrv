package postgres

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"telesrv/internal/domain"
	"telesrv/internal/store"
	"telesrv/internal/store/postgres/sqlcgen"
)

// The two-int advisory-lock namespace is disjoint from the one-bigint user
// locks used by lockUsersForUpdate. Only 32 digest bits are needed here:
// collisions merely serialize unrelated deliveries and cannot merge receipts.
const loginCodeDeliveryAdvisoryNamespace int32 = 0x4c434f44 // "LCOD"

const (
	loginCodeDeliveryRecoveryTimeout = 2 * time.Second
	loginCodeDeliveryRecoveryPoll    = 20 * time.Millisecond
)

type loginCodeDeliveryReceiptQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

type loginCodeDeliveryReceipt struct {
	userID           int64
	codeFingerprint  []byte
	privateMessageID int64
	messageBoxID     int
	pts              int
	messageDate      int
}

// DeliverLoginCodeMessage commits the account-visible 777000 message, dialog
// projection, user pts event, dispatch outbox row and compact idempotency
// receipt in one transaction. The raw phone_code_hash is never persisted.
func (s *MessageStore) DeliverLoginCodeMessage(ctx context.Context, req domain.LoginCodeDeliveryRequest) (domain.LoginCodeDeliveryResult, error) {
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
		return domain.LoginCodeDeliveryResult{}, fmt.Errorf("login code receipt expiry: %w: date=%d expires_at=%d", domain.ErrLoginCodeDeliveryInvalid, req.Date, req.ExpiresAt)
	}
	base, err := domain.OfficialLoginCodeMessage(req.UserID, req.Code, req.Date)
	if err != nil {
		return domain.LoginCodeDeliveryResult{}, err
	}
	entitiesJSON, err := encodeMessageEntities(base.Entities)
	if err != nil {
		return domain.LoginCodeDeliveryResult{}, fmt.Errorf("encode login code entities: %w", err)
	}

	beginner, ok := s.db.(txBeginner)
	if !ok {
		return domain.LoginCodeDeliveryResult{}, fmt.Errorf("deliver login code: database does not support transactions")
	}
	tx, err := beginner.Begin(ctx)
	if err != nil {
		return domain.LoginCodeDeliveryResult{}, fmt.Errorf("begin login code delivery: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), loginCodeDeliveryRecoveryTimeout)
			defer cancel()
			_ = tx.Rollback(rollbackCtx)
		}
	}()

	// Serialize the global idempotency key before any per-user row/advisory
	// lock. This makes same-key concurrent calls deterministic even if a caller
	// accidentally supplies a different user ID.
	lockKey := int32(binary.BigEndian.Uint32(deliveryKey[:4]))
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1::integer, $2::integer)`, loginCodeDeliveryAdvisoryNamespace, lockKey); err != nil {
		return domain.LoginCodeDeliveryResult{}, fmt.Errorf("lock login code delivery: %w", err)
	}

	receipt, found, err := getLoginCodeDeliveryReceipt(ctx, tx, deliveryKey)
	if err != nil {
		return domain.LoginCodeDeliveryResult{}, err
	}
	if found {
		if receipt.userID != req.UserID || !store.SameLoginCodeFingerprint(receipt.codeFingerprint, codeFingerprint) {
			return domain.LoginCodeDeliveryResult{}, fmt.Errorf("deliver login code replay: %w", domain.ErrLoginCodeDeliveryConflict)
		}
		msg, err := store.RestoreLoginCodeDeliveryMessage(
			receipt.userID,
			req.Code,
			receipt.messageDate,
			receipt.privateMessageID,
			receipt.messageBoxID,
			receipt.pts,
		)
		if err != nil {
			return domain.LoginCodeDeliveryResult{}, fmt.Errorf("restore login code replay: %w", err)
		}
		return domain.LoginCodeDeliveryResult{Message: msg, Created: false}, nil
	}

	// All user-scoped message/update writers share this lock and acquire it
	// before watermark/dialog rows, keeping box IDs and pts contiguous.
	if err := lockUsersForUpdate(ctx, tx, req.UserID); err != nil {
		return domain.LoginCodeDeliveryResult{}, fmt.Errorf("lock login code recipient: %w", err)
	}
	if err := ensureOfficialSystemUserWithDB(ctx, tx, base); err != nil {
		return domain.LoginCodeDeliveryResult{}, err
	}
	qtx := sqlcgen.New(tx)

	pm, err := qtx.CreatePrivateMessage(ctx, sqlcgen.CreatePrivateMessageParams{
		SenderUserID:       domain.OfficialSystemUserID,
		RecipientUserID:    req.UserID,
		RandomID:           0,
		MessageDate:        int32(base.Date),
		Body:               base.Body,
		RequestFingerprint: []byte{},
		RecipientDelivered: true,
		EntitiesJson:       entitiesJSON,
		QuoteEntitiesJson:  []byte("[]"),
		MediaJson:          []byte("{}"),
		ReplyMarkupJson:    []byte("{}"),
		RichMessageJson:    []byte("{}"),
	})
	if err != nil {
		return domain.LoginCodeDeliveryResult{}, fmt.Errorf("create login code private message: %w", err)
	}

	boxID, err := s.nextLoginCodeBoxID(ctx, qtx, req.UserID)
	if err != nil {
		return domain.LoginCodeDeliveryResult{}, fmt.Errorf("allocate login code box id: %w", err)
	}
	if boxID <= 0 || boxID > domain.MaxMessageBoxID {
		return domain.LoginCodeDeliveryResult{}, fmt.Errorf("allocate login code box id: %w: %d", domain.ErrLoginCodeDeliveryInvalid, boxID)
	}
	pts, err := s.reservePts(ctx, tx, req.UserID)
	if err != nil {
		return domain.LoginCodeDeliveryResult{}, fmt.Errorf("allocate login code pts: %w", err)
	}

	boxRow, err := qtx.CreateMessageBox(ctx, sqlcgen.CreateMessageBoxParams{
		OwnerUserID:       req.UserID,
		BoxID:             int32(boxID),
		PrivateMessageID:  pm.ID,
		MessageSenderID:   domain.OfficialSystemUserID,
		PeerType:          string(domain.PeerTypeUser),
		PeerID:            domain.OfficialSystemUserID,
		FromUserID:        domain.OfficialSystemUserID,
		MessageDate:       int32(base.Date),
		Outgoing:          false,
		Body:              base.Body,
		EntitiesJson:      entitiesJSON,
		QuoteEntitiesJson: []byte("[]"),
		Pts:               int32(pts),
		MediaJson:         []byte("{}"),
		ReplyMarkupJson:   []byte("{}"),
		RichMessageJson:   []byte("{}"),
	})
	if err != nil {
		return domain.LoginCodeDeliveryResult{}, fmt.Errorf("create login code recipient box: %w", err)
	}
	msg := messageFromBoxRow(boxRow)

	if err := qtx.UpsertInboxDialog(ctx, sqlcgen.UpsertInboxDialogParams{
		UserID:         req.UserID,
		PeerType:       string(domain.PeerTypeUser),
		PeerID:         domain.OfficialSystemUserID,
		TopMessageID:   int32(msg.ID),
		TopMessageDate: int32(msg.Date),
	}); err != nil {
		return domain.LoginCodeDeliveryResult{}, fmt.Errorf("upsert login code dialog: %w", err)
	}
	if err := appendNewMessageEvent(ctx, qtx, msg); err != nil {
		return domain.LoginCodeDeliveryResult{}, err
	}
	if err := qtx.EnqueueDispatch(ctx, sqlcgen.EnqueueDispatchParams{
		TargetUserID:     req.UserID,
		Pts:              int32(msg.Pts),
		EventType:        string(domain.UpdateEventNewMessage),
		ExcludeAuthKeyID: 0,
		ExcludeSessionID: 0,
	}); err != nil {
		return domain.LoginCodeDeliveryResult{}, fmt.Errorf("enqueue login code dispatch: %w", err)
	}

	tag, err := tx.Exec(ctx, `
UPDATE private_messages
SET recipient_box_id = $3,
    recipient_pts = $4
WHERE sender_user_id = $1
  AND id = $2
  AND recipient_delivered
  AND recipient_box_id = 0
  AND recipient_pts = 0`, domain.OfficialSystemUserID, pm.ID, msg.ID, msg.Pts)
	if err != nil {
		return domain.LoginCodeDeliveryResult{}, fmt.Errorf("save login code private receipt: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return domain.LoginCodeDeliveryResult{}, fmt.Errorf("save login code private receipt: message %d lost its allocation boundary", pm.ID)
	}

	if _, err := tx.Exec(ctx, `
INSERT INTO login_code_message_deliveries (
  delivery_key,
  code_fingerprint,
  user_id,
  private_message_id,
  message_box_id,
  pts,
  message_date,
  expires_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		deliveryKey[:], codeFingerprint[:], req.UserID, msg.UID, msg.ID, msg.Pts, msg.Date, time.Unix(req.ExpiresAt, 0).UTC(),
	); err != nil {
		return domain.LoginCodeDeliveryResult{}, fmt.Errorf("save login code delivery receipt: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		result, recoverErr := s.recoverLoginCodeDeliveryAfterCommitError(ctx, req, deliveryKey, codeFingerprint)
		if recoverErr != nil {
			return domain.LoginCodeDeliveryResult{}, errors.Join(
				fmt.Errorf("commit login code delivery: %w", err),
				recoverErr,
			)
		}
		committed = true
		return result, nil
	}
	committed = true
	return domain.LoginCodeDeliveryResult{Message: msg, Created: true}, nil
}

func (s *MessageStore) recoverLoginCodeDeliveryAfterCommitError(
	ctx context.Context,
	req domain.LoginCodeDeliveryRequest,
	deliveryKey, codeFingerprint [32]byte,
) (domain.LoginCodeDeliveryResult, error) {
	probeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), loginCodeDeliveryRecoveryTimeout)
	defer cancel()
	ticker := time.NewTicker(loginCodeDeliveryRecoveryPoll)
	defer ticker.Stop()
	for {
		receipt, found, err := getLoginCodeDeliveryReceipt(probeCtx, s.db, deliveryKey)
		if err != nil {
			return domain.LoginCodeDeliveryResult{}, errors.Join(
				domain.ErrLoginCodeDeliveryCommitAmbiguous,
				fmt.Errorf("probe login code delivery receipt after commit error: %w", err),
			)
		}
		if found {
			if receipt.userID != req.UserID || !store.SameLoginCodeFingerprint(receipt.codeFingerprint, codeFingerprint) {
				return domain.LoginCodeDeliveryResult{}, fmt.Errorf("probe login code delivery receipt after commit error: %w", domain.ErrLoginCodeDeliveryConflict)
			}
			msg, err := store.RestoreLoginCodeDeliveryMessage(
				receipt.userID,
				req.Code,
				receipt.messageDate,
				receipt.privateMessageID,
				receipt.messageBoxID,
				receipt.pts,
			)
			if err != nil {
				return domain.LoginCodeDeliveryResult{}, errors.Join(
					domain.ErrLoginCodeDeliveryCommitAmbiguous,
					fmt.Errorf("restore probed login code delivery: %w", err),
				)
			}
			// The receipt proves durable success but cannot prove whether this
			// caller or an equivalent replay won the commit race.
			return domain.LoginCodeDeliveryResult{Message: msg, Created: false}, nil
		}
		select {
		case <-probeCtx.Done():
			return domain.LoginCodeDeliveryResult{}, errors.Join(
				domain.ErrLoginCodeDeliveryCommitAmbiguous,
				fmt.Errorf("probe login code delivery receipt after commit error: %w", probeCtx.Err()),
			)
		case <-ticker.C:
		}
	}
}

func getLoginCodeDeliveryReceipt(ctx context.Context, q loginCodeDeliveryReceiptQuerier, deliveryKey [32]byte) (loginCodeDeliveryReceipt, bool, error) {
	var receipt loginCodeDeliveryReceipt
	var boxID, pts, messageDate int32
	err := q.QueryRow(ctx, `
SELECT user_id,
       code_fingerprint,
       private_message_id,
       message_box_id,
       pts,
       message_date
FROM login_code_message_deliveries
WHERE delivery_key = $1`, deliveryKey[:]).Scan(
		&receipt.userID,
		&receipt.codeFingerprint,
		&receipt.privateMessageID,
		&boxID,
		&pts,
		&messageDate,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return loginCodeDeliveryReceipt{}, false, nil
	}
	if err != nil {
		return loginCodeDeliveryReceipt{}, false, fmt.Errorf("load login code delivery receipt: %w", err)
	}
	receipt.messageBoxID = int(boxID)
	receipt.pts = int(pts)
	receipt.messageDate = int(messageDate)
	return receipt, true, nil
}

func (s *MessageStore) nextLoginCodeBoxID(ctx context.Context, qtx *sqlcgen.Queries, userID int64) (int, error) {
	// The default allocator queries PostgreSQL. Run that query on the active
	// transaction connection: querying s.q while holding the transaction can
	// deadlock a MaxConns=1 pool. External allocators (Redis/counters) retain
	// their normal semantics.
	switch s.boxIDs.(type) {
	case pgBoxIDAllocator, *pgBoxIDAllocator:
		current, err := qtx.MaxMessageBoxID(ctx, userID)
		if err != nil {
			return 0, err
		}
		return int(current) + 1, nil
	default:
		return s.boxIDs.NextBoxID(ctx, userID)
	}
}
