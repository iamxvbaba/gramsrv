package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"telesrv/internal/domain"
	"telesrv/internal/store"
	"telesrv/internal/store/postgres/sqlcgen"
)

// plainPrivateSendHotPath is deliberately a semantic classifier, not a
// compatibility fallback. The rich/media/hook paths have additional durable
// writes and therefore cannot share this SQL without hiding their invariants.
func plainPrivateSendHotPath(req domain.SendPrivateTextRequest, hooks privateSendTxHooks) bool {
	return len(hooks.lockUserIDs) == 0 && req.ScheduledDelivery == nil &&
		hooks.before == nil && hooks.projectMedia == nil && hooks.afterAllocate == nil && hooks.after == nil &&
		len(req.Entities) == 0 && req.Media.IsZero() && req.ReplyMarkup.IsZero() && req.RichMessage.IsZero() &&
		req.ReplyTo == nil && req.Forward == nil && !req.Silent && !req.NoForwards &&
		req.ViaBotID == 0 && req.GroupedID == 0 && req.Effect == 0 && !req.ClearDraft &&
		req.PaidStars == 0
}

type plainPrivateSendProjection struct {
	Sender    domain.Message
	Recipient domain.Message
}

// createPlainPrivateMessage folds the default/dialog TTL lookup into the
// logical-message insert. A unique random-id conflict still returns
// pgx.ErrNoRows, preserving the transactional replay fence without allocating
// box ids or PTS for the losing request.
func createPlainPrivateMessage(
	ctx context.Context,
	tx pgx.Tx,
	req domain.SendPrivateTextRequest,
	requestFingerprint []byte,
	deliverRecipient bool,
) (sqlcgen.CreatePrivateMessageRow, error) {
	row := tx.QueryRow(ctx, `
WITH effective_ttl AS MATERIALIZED (
  SELECT CASE
    WHEN $7::int <> 0 THEN $7::int
    ELSE private_dialog_initial_ttl($1::bigint, $2::bigint)
  END AS ttl_period
), inserted AS (
  INSERT INTO private_messages (
    sender_user_id, recipient_user_id, random_id, request_fingerprint,
    recipient_delivered, message_date, ttl_period, expires_at, body
  )
  SELECT
    $1::bigint, $2::bigint, $3::bigint, $4::bytea,
    $5::boolean, $6::int, ttl_period,
    CASE WHEN ttl_period > 0 THEN ($6::int + ttl_period)::int ELSE 0 END,
    $8::text
  FROM effective_ttl
  ON CONFLICT (sender_user_id, random_id) WHERE random_id <> 0 DO NOTHING
  RETURNING
    id, sender_user_id, recipient_user_id, random_id, message_date,
    ttl_period, expires_at, edit_date, body, entities::text AS entities_json
)
SELECT
  id, sender_user_id, recipient_user_id, random_id, message_date,
  ttl_period, expires_at, edit_date, body, entities_json
FROM inserted`,
		req.SenderUserID,
		req.RecipientUserID,
		req.RandomID,
		requestFingerprint,
		deliverRecipient,
		req.Date,
		req.TTLPeriod,
		req.Message,
	)
	var result sqlcgen.CreatePrivateMessageRow
	err := row.Scan(
		&result.ID,
		&result.SenderUserID,
		&result.RecipientUserID,
		&result.RandomID,
		&result.MessageDate,
		&result.TtlPeriod,
		&result.ExpiresAt,
		&result.EditDate,
		&result.Body,
		&result.EntitiesJson,
	)
	return result, err
}

// persistPlainPrivateSendProjection reserves both account PTS values and then
// writes boxes, dialogs, update events, dispatch rows and the immutable
// random-id receipt as one auditable SQL statement. The caller owns the
// surrounding transaction and already holds both ordered advisory locks.
func persistPlainPrivateSendProjection(
	ctx context.Context,
	tx pgx.Tx,
	req domain.SendPrivateTextRequest,
	privateMessageID int64,
	senderBoxID, recipientBoxID int,
	ttlPeriod, expiresAt int,
) (plainPrivateSendProjection, error) {
	selfMessage := req.SenderUserID == req.RecipientUserID
	deliverRecipient := !selfMessage && !req.RecipientBlocked
	savedPeer := domain.Peer{}
	if selfMessage {
		savedPeer = domain.SavedPeerForSelfChat(req.SenderUserID, nil)
	}
	senderTemplate := domain.Message{
		ID:          senderBoxID,
		UID:         privateMessageID,
		OwnerUserID: req.SenderUserID,
		Peer:        domain.Peer{Type: domain.PeerTypeUser, ID: req.RecipientUserID},
		From:        domain.Peer{Type: domain.PeerTypeUser, ID: req.SenderUserID},
		Date:        req.Date,
		Out:         true,
		Body:        req.Message,
		// SQL patches this placeholder with the PTS returned by the same
		// watermark CTE before the immutable receipt becomes visible.
		Pts:       1,
		TTLPeriod: ttlPeriod,
		ExpiresAt: expiresAt,
		SavedPeer: savedPeer,
		RandomID:  req.RandomID,
	}
	originUserID := req.OriginUserID
	if originUserID == 0 {
		originUserID = req.SenderUserID
	}
	senderExcludeAuthKeyID, senderExcludeSessionID := [8]byte{}, int64(0)
	if originUserID == req.SenderUserID {
		senderExcludeAuthKeyID = req.OriginAuthKeyID
		senderExcludeSessionID = req.OriginSessionID
	}
	recipientExcludeAuthKeyID, recipientExcludeSessionID := [8]byte{}, int64(0)
	if deliverRecipient && originUserID == req.RecipientUserID {
		recipientExcludeAuthKeyID = req.OriginAuthKeyID
		recipientExcludeSessionID = req.OriginSessionID
	}
	if (senderExcludeAuthKeyID != ([8]byte{})) != (senderExcludeSessionID != 0) ||
		(recipientExcludeAuthKeyID != ([8]byte{})) != (recipientExcludeSessionID != 0) {
		return plainPrivateSendProjection{}, errInvalidDispatchOutboxExclusionPair
	}

	receiptRecipientBoxID := recipientBoxID
	if selfMessage {
		receiptRecipientBoxID = senderBoxID
	}
	senderSnapshotTemplate, err := store.EncodePrivateSendSnapshot(senderTemplate)
	if err != nil {
		return plainPrivateSendProjection{}, err
	}
	expectedRows := 1
	if deliverRecipient {
		expectedRows = 2
	}

	var boxRows, dialogRows, eventRows, dispatchRows, receiptRows, senderPts, recipientPts int
	err = tx.QueryRow(ctx, `
WITH box_seed (
  owner_user_id, box_id, peer_id, outgoing,
  saved_peer_type, saved_peer_id, exclude_auth_key_id, exclude_session_id
) AS MATERIALIZED (
  VALUES
    ($1::bigint, $2::int, $3::bigint, true,
     $4::text, $5::bigint, $6::bytea, $7::bigint),
    ($3::bigint, $8::int, $1::bigint, false,
     ''::text, 0::bigint, $9::bytea, $10::bigint)
), watermark_rows AS (
  INSERT INTO user_update_watermarks (user_id, contiguous_pts)
  SELECT owner_user_id, 1
  FROM box_seed
  WHERE box_id > 0
  GROUP BY owner_user_id
  ORDER BY owner_user_id
  ON CONFLICT (user_id) DO UPDATE
  SET contiguous_pts = user_update_watermarks.contiguous_pts + 1,
      updated_at = now()
  RETURNING user_id, contiguous_pts
), box_input AS MATERIALIZED (
  SELECT seed.*, watermark_rows.contiguous_pts AS pts
  FROM box_seed seed
  JOIN watermark_rows ON watermark_rows.user_id = seed.owner_user_id
  WHERE seed.box_id > 0
), boxes AS (
  INSERT INTO message_boxes (
    owner_user_id, box_id, private_message_id, message_sender_id,
    peer_type, peer_id, from_user_id, message_date, ttl_period,
    expires_at, outgoing, body, pts, saved_peer_type, saved_peer_id
  )
  SELECT
    i.owner_user_id, i.box_id, $11::bigint, $1::bigint,
    'user', i.peer_id, $1::bigint, $12::int, $13::int,
    $14::int, i.outgoing, $15::text, i.pts,
    i.saved_peer_type, i.saved_peer_id
  FROM box_input i
  WHERE i.box_id > 0 AND i.pts > 0
  RETURNING owner_user_id, box_id, peer_id, outgoing, pts
), dialog_rows AS (
  INSERT INTO dialogs (
    user_id, peer_type, peer_id, top_message_id, top_message_date, unread_count, ttl_period
  )
  SELECT
    b.owner_user_id, 'user', b.peer_id, b.box_id, $12::int,
    CASE WHEN b.outgoing THEN 0 ELSE 1 END,
    private_dialog_initial_ttl($1::bigint, $2::bigint)
  FROM boxes b
  ON CONFLICT (user_id, peer_type, peer_id) DO UPDATE SET
    top_message_id = CASE
      WHEN EXCLUDED.unread_count = 0 THEN EXCLUDED.top_message_id
      ELSE GREATEST(dialogs.top_message_id, EXCLUDED.top_message_id)
    END,
    top_message_date = CASE
      WHEN EXCLUDED.unread_count = 0 THEN EXCLUDED.top_message_date
      WHEN EXCLUDED.top_message_id >= dialogs.top_message_id THEN EXCLUDED.top_message_date
      ELSE dialogs.top_message_date
    END,
    -- boxes contains only rows inserted by this statement. Account writers use
    -- the same ordered transaction lock, so the dialog read boundary cannot
    -- cross this incoming box concurrently. Increment the materialized counter
    -- instead of rescanning the owner's complete message history on every send.
    unread_count = dialogs.unread_count + CASE
      WHEN EXCLUDED.unread_count > 0
       AND EXCLUDED.top_message_id > dialogs.read_inbox_max_id THEN 1
      ELSE 0
    END,
    unread_mark = CASE
      WHEN EXCLUDED.unread_count = 0 THEN false
      ELSE dialogs.unread_mark
    END,
    updated_at = now()
  RETURNING user_id
), event_rows AS (
  INSERT INTO user_update_events (
    user_id, pts, pts_count, date, event_type,
    message_box_id, peer_type, peer_id
  )
  SELECT
    b.owner_user_id, b.pts, 1, $12::int, 'new_message',
    b.box_id, 'user', b.peer_id
  FROM boxes b
  RETURNING user_id, pts
), dispatch_rows AS (
	INSERT INTO dispatch_outbox (
		target_user_id, pts, event_type, exclude_auth_key_id, exclude_session_id,
		read_model_peer_type, read_model_peer_id
	)
	SELECT
		e.user_id, e.pts, 'new_message', i.exclude_auth_key_id, i.exclude_session_id,
		'user', i.peer_id
  FROM event_rows e
  JOIN box_input i ON i.owner_user_id = e.user_id AND i.pts = e.pts
  ON CONFLICT DO NOTHING
  RETURNING target_user_id
), receipt_rows AS (
  UPDATE private_messages
  SET sender_box_id = $2::int,
      sender_pts = (SELECT pts FROM boxes WHERE outgoing),
      recipient_box_id = $16::int,
      recipient_pts = CASE
        WHEN $16::int = 0 THEN 0
        WHEN $3::bigint = $1::bigint THEN (SELECT pts FROM boxes WHERE outgoing)
        ELSE (SELECT pts FROM boxes WHERE NOT outgoing)
      END,
      sender_snapshot = jsonb_set(
        $17::jsonb,
        ARRAY['message', 'Pts'],
        to_jsonb((SELECT pts FROM boxes WHERE outgoing)),
        false
      )
  WHERE sender_user_id = $1::bigint
    AND id = $11::bigint
    AND sender_box_id = 0
    AND sender_pts = 0
    AND sender_snapshot = '{}'::jsonb
    AND (SELECT COUNT(*) FROM boxes) = $18::int
  RETURNING id
)
SELECT
  (SELECT COUNT(*) FROM boxes)::int,
  (SELECT COUNT(*) FROM dialog_rows)::int,
  (SELECT COUNT(*) FROM event_rows)::int,
  (SELECT COUNT(*) FROM dispatch_rows)::int,
  (SELECT COUNT(*) FROM receipt_rows)::int,
  (SELECT pts FROM boxes WHERE outgoing)::int,
  COALESCE((SELECT pts FROM boxes WHERE NOT outgoing), 0)::int`,
		req.SenderUserID,
		senderBoxID,
		req.RecipientUserID,
		string(savedPeer.Type),
		savedPeer.ID,
		senderExcludeAuthKeyID[:],
		senderExcludeSessionID,
		recipientBoxID,
		recipientExcludeAuthKeyID[:],
		recipientExcludeSessionID,
		privateMessageID,
		req.Date,
		ttlPeriod,
		expiresAt,
		req.Message,
		receiptRecipientBoxID,
		senderSnapshotTemplate,
		expectedRows,
	).Scan(&boxRows, &dialogRows, &eventRows, &dispatchRows, &receiptRows, &senderPts, &recipientPts)
	if err != nil {
		return plainPrivateSendProjection{}, fmt.Errorf("persist plain private send projection: %w", err)
	}
	if boxRows != expectedRows || dialogRows != expectedRows || eventRows != expectedRows ||
		dispatchRows != expectedRows || receiptRows != 1 || senderPts <= 0 || (deliverRecipient && recipientPts <= 0) {
		return plainPrivateSendProjection{}, fmt.Errorf(
			"persist plain private send projection: incomplete rows boxes=%d dialogs=%d events=%d dispatch=%d receipt=%d pts=%d/%d want=%d/%d/%d/%d/1",
			boxRows, dialogRows, eventRows, dispatchRows, receiptRows, senderPts, recipientPts,
			expectedRows, expectedRows, expectedRows, expectedRows,
		)
	}
	sender := senderTemplate
	sender.Pts = senderPts
	recipient := domain.Message{}
	if selfMessage {
		recipient = sender
	} else if deliverRecipient {
		recipient = domain.Message{
			ID:          recipientBoxID,
			UID:         privateMessageID,
			OwnerUserID: req.RecipientUserID,
			Peer:        domain.Peer{Type: domain.PeerTypeUser, ID: req.SenderUserID},
			From:        domain.Peer{Type: domain.PeerTypeUser, ID: req.SenderUserID},
			Date:        req.Date,
			Body:        req.Message,
			Pts:         recipientPts,
			TTLPeriod:   ttlPeriod,
			ExpiresAt:   expiresAt,
			RandomID:    req.RandomID,
		}
	}
	return plainPrivateSendProjection{Sender: sender, Recipient: recipient}, nil
}
