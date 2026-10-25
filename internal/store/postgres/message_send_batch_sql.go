package postgres

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

type plainPrivateBatchCreated struct {
	inserted         bool
	privateMessageID int64
	ttlPeriod        int
	expiresAt        int
}

type plainPrivateBatchInsertInput struct {
	Ordinal            int    `json:"ordinal"`
	SenderUserID       int64  `json:"sender_user_id"`
	RecipientUserID    int64  `json:"recipient_user_id"`
	RandomID           int64  `json:"random_id"`
	RequestFingerprint string `json:"request_fingerprint"`
	RecipientDelivered bool   `json:"recipient_delivered"`
	MessageDate        int    `json:"message_date"`
	RequestedTTLPeriod int    `json:"requested_ttl_period"`
	Body               string `json:"body"`
}

// createPlainPrivateMessageBatch resolves TTL and inserts every disjoint
// logical message in one statement. Missing RETURNING rows are immutable
// random_id conflicts; callers load those receipts without allocating PTS.
func createPlainPrivateMessageBatch(
	ctx context.Context,
	tx pgx.Tx,
	tasks []*plainPrivateSendBatchTask,
) ([]plainPrivateBatchCreated, error) {
	created := make([]plainPrivateBatchCreated, len(tasks))
	input := make([]plainPrivateBatchInsertInput, len(tasks))
	for i, task := range tasks {
		input[i] = plainPrivateBatchInsertInput{
			Ordinal: i, SenderUserID: task.req.SenderUserID, RecipientUserID: task.req.RecipientUserID,
			RandomID: task.req.RandomID, RequestFingerprint: hex.EncodeToString(task.fingerprint),
			RecipientDelivered: task.req.SenderUserID != task.req.RecipientUserID && !task.req.RecipientBlocked,
			MessageDate:        task.req.Date, RequestedTTLPeriod: task.req.TTLPeriod, Body: task.req.Message,
		}
	}
	raw, err := json.Marshal(input)
	if err != nil {
		return nil, fmt.Errorf("marshal plain private send batch inserts: %w", err)
	}
	rows, err := tx.Query(ctx, `
WITH input AS MATERIALIZED (
  SELECT *
  FROM jsonb_to_recordset($1::jsonb) AS i(
    ordinal int,
    sender_user_id bigint,
    recipient_user_id bigint,
    random_id bigint,
    request_fingerprint text,
    recipient_delivered boolean,
    message_date int,
    requested_ttl_period int,
    body text
  )
), resolved AS MATERIALIZED (
  SELECT
    i.*,
    CASE
      WHEN i.requested_ttl_period <> 0 THEN i.requested_ttl_period
      ELSE private_dialog_initial_ttl(i.sender_user_id, i.recipient_user_id)
    END AS ttl_period
  FROM input i
  JOIN users u ON u.id = i.sender_user_id
), inserted AS (
  INSERT INTO private_messages (
    sender_user_id, recipient_user_id, random_id, request_fingerprint,
    recipient_delivered, message_date, ttl_period, expires_at, body
  )
  SELECT
    sender_user_id, recipient_user_id, random_id, decode(request_fingerprint, 'hex'),
    recipient_delivered, message_date, ttl_period,
    CASE WHEN ttl_period > 0 THEN message_date + ttl_period ELSE 0 END,
    body
  FROM resolved
  ORDER BY ordinal
  ON CONFLICT (sender_user_id, random_id) WHERE random_id <> 0 DO NOTHING
  RETURNING id, sender_user_id, ttl_period, expires_at
)
SELECT i.ordinal, inserted.id, inserted.ttl_period, inserted.expires_at
FROM inserted
JOIN input i ON i.sender_user_id = inserted.sender_user_id
ORDER BY i.ordinal`, raw)
	if err != nil {
		return nil, fmt.Errorf("create plain private send batch messages: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var ordinal int
		var item plainPrivateBatchCreated
		if err := rows.Scan(&ordinal, &item.privateMessageID, &item.ttlPeriod, &item.expiresAt); err != nil {
			return nil, fmt.Errorf("scan plain private send batch message: %w", err)
		}
		if ordinal < 0 || ordinal >= len(created) || created[ordinal].inserted || item.privateMessageID <= 0 {
			return nil, fmt.Errorf("create plain private send batch messages: invalid ordinal/id %d/%d", ordinal, item.privateMessageID)
		}
		item.inserted = true
		created[ordinal] = item
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate plain private send batch messages: %w", err)
	}
	return created, nil
}

type plainPrivateBatchProjectionInput struct {
	Ordinal                    int             `json:"ordinal"`
	SenderUserID               int64           `json:"sender_user_id"`
	RecipientUserID            int64           `json:"recipient_user_id"`
	PrivateMessageID           int64           `json:"private_message_id"`
	SenderBoxID                int             `json:"sender_box_id"`
	RecipientBoxID             int             `json:"recipient_box_id"`
	MessageDate                int             `json:"message_date"`
	TTLPeriod                  int             `json:"ttl_period"`
	ExpiresAt                  int             `json:"expires_at"`
	Body                       string          `json:"body"`
	SavedPeerType              string          `json:"saved_peer_type"`
	SavedPeerID                int64           `json:"saved_peer_id"`
	SenderExcludeAuthKeyHex    string          `json:"sender_exclude_auth_key_hex"`
	SenderExcludeSessionID     int64           `json:"sender_exclude_session_id"`
	RecipientExcludeAuthKeyHex string          `json:"recipient_exclude_auth_key_hex"`
	RecipientExcludeSessionID  int64           `json:"recipient_exclude_session_id"`
	ReceiptRecipientBoxID      int             `json:"receipt_recipient_box_id"`
	SenderSnapshotTemplate     json.RawMessage `json:"sender_snapshot_template"`
	ExpectedRows               int             `json:"expected_rows"`
}

func persistPlainPrivateSendProjectionBatch(
	ctx context.Context,
	tx pgx.Tx,
	tasks []*plainPrivateSendBatchTask,
	created []plainPrivateBatchCreated,
	boxIDs map[int64]int,
) (map[int]plainPrivateSendProjection, error) {
	input := make([]plainPrivateBatchProjectionInput, 0, len(tasks))
	templates := make(map[int]plainPrivateSendProjection, len(tasks))
	expectedTotal := 0
	for ordinal, task := range tasks {
		if ordinal >= len(created) || !created[ordinal].inserted {
			continue
		}
		req := task.req
		selfMessage := req.SenderUserID == req.RecipientUserID
		deliverRecipient := !selfMessage && !req.RecipientBlocked
		senderBoxID := boxIDs[req.SenderUserID]
		if senderBoxID <= 0 {
			return nil, fmt.Errorf("allocate plain private send batch box ids: missing sender %d", req.SenderUserID)
		}
		recipientBoxID := 0
		if deliverRecipient {
			recipientBoxID = boxIDs[req.RecipientUserID]
			if recipientBoxID <= 0 {
				return nil, fmt.Errorf("allocate plain private send batch box ids: missing recipient %d", req.RecipientUserID)
			}
		}
		savedPeer := domain.Peer{}
		if selfMessage {
			savedPeer = domain.SavedPeerForSelfChat(req.SenderUserID, nil)
		}
		item := created[ordinal]
		sender := domain.Message{
			ID: senderBoxID, UID: item.privateMessageID, OwnerUserID: req.SenderUserID,
			Peer: domain.Peer{Type: domain.PeerTypeUser, ID: req.RecipientUserID},
			From: domain.Peer{Type: domain.PeerTypeUser, ID: req.SenderUserID},
			Date: req.Date, Out: true, Body: req.Message, Pts: 1,
			TTLPeriod: item.ttlPeriod, ExpiresAt: item.expiresAt, SavedPeer: savedPeer, RandomID: req.RandomID,
		}
		originUserID := req.OriginUserID
		if originUserID == 0 {
			originUserID = req.SenderUserID
		}
		senderExcludeAuthKeyID, senderExcludeSessionID := [8]byte{}, int64(0)
		if originUserID == req.SenderUserID {
			senderExcludeAuthKeyID, senderExcludeSessionID = req.OriginAuthKeyID, req.OriginSessionID
		}
		recipientExcludeAuthKeyID, recipientExcludeSessionID := [8]byte{}, int64(0)
		if deliverRecipient && originUserID == req.RecipientUserID {
			recipientExcludeAuthKeyID, recipientExcludeSessionID = req.OriginAuthKeyID, req.OriginSessionID
		}
		if (senderExcludeAuthKeyID != ([8]byte{})) != (senderExcludeSessionID != 0) ||
			(recipientExcludeAuthKeyID != ([8]byte{})) != (recipientExcludeSessionID != 0) {
			return nil, errInvalidDispatchOutboxExclusionPair
		}
		receiptRecipientBoxID := recipientBoxID
		if selfMessage {
			receiptRecipientBoxID = senderBoxID
		}
		snapshot, err := store.EncodePrivateSendSnapshot(sender)
		if err != nil {
			return nil, err
		}
		expectedRows := 1
		if deliverRecipient {
			expectedRows = 2
		}
		expectedTotal += expectedRows
		input = append(input, plainPrivateBatchProjectionInput{
			Ordinal: ordinal, SenderUserID: req.SenderUserID, RecipientUserID: req.RecipientUserID,
			PrivateMessageID: item.privateMessageID, SenderBoxID: senderBoxID, RecipientBoxID: recipientBoxID,
			MessageDate: req.Date, TTLPeriod: item.ttlPeriod, ExpiresAt: item.expiresAt, Body: req.Message,
			SavedPeerType: string(savedPeer.Type), SavedPeerID: savedPeer.ID,
			SenderExcludeAuthKeyHex: hex.EncodeToString(senderExcludeAuthKeyID[:]), SenderExcludeSessionID: senderExcludeSessionID,
			RecipientExcludeAuthKeyHex: hex.EncodeToString(recipientExcludeAuthKeyID[:]), RecipientExcludeSessionID: recipientExcludeSessionID,
			ReceiptRecipientBoxID: receiptRecipientBoxID, SenderSnapshotTemplate: snapshot, ExpectedRows: expectedRows,
		})
		templates[ordinal] = plainPrivateSendProjection{Sender: sender}
	}
	if len(input) == 0 {
		return templates, nil
	}
	raw, err := json.Marshal(input)
	if err != nil {
		return nil, fmt.Errorf("marshal plain private send batch projections: %w", err)
	}
	rows, err := tx.Query(ctx, plainPrivateSendBatchProjectionSQL, raw)
	if err != nil {
		return nil, fmt.Errorf("persist plain private send batch projection: %w", err)
	}
	defer rows.Close()
	seen := 0
	for rows.Next() {
		var ordinal, senderPts, recipientPts int
		var boxRows, dialogRows, eventRows, dispatchRows, receiptRows int
		if err := rows.Scan(
			&ordinal, &senderPts, &recipientPts,
			&boxRows, &dialogRows, &eventRows, &dispatchRows, &receiptRows,
		); err != nil {
			return nil, fmt.Errorf("scan plain private send batch projection: %w", err)
		}
		if boxRows != expectedTotal || dialogRows != expectedTotal || eventRows != expectedTotal ||
			dispatchRows != expectedTotal || receiptRows != len(input) {
			return nil, fmt.Errorf(
				"persist plain private send batch projection: incomplete rows boxes=%d dialogs=%d events=%d dispatch=%d receipts=%d want=%d/%d",
				boxRows, dialogRows, eventRows, dispatchRows, receiptRows, expectedTotal, len(input),
			)
		}
		projection, ok := templates[ordinal]
		if !ok || senderPts <= 0 {
			return nil, fmt.Errorf("persist plain private send batch projection: invalid ordinal/pts %d/%d", ordinal, senderPts)
		}
		projection.Sender.Pts = senderPts
		req := tasks[ordinal].req
		if req.SenderUserID == req.RecipientUserID {
			projection.Recipient = projection.Sender
		} else if !req.RecipientBlocked {
			if recipientPts <= 0 {
				return nil, fmt.Errorf("persist plain private send batch projection: missing recipient pts for ordinal %d", ordinal)
			}
			item := created[ordinal]
			projection.Recipient = domain.Message{
				ID: boxIDs[req.RecipientUserID], UID: item.privateMessageID, OwnerUserID: req.RecipientUserID,
				Peer: domain.Peer{Type: domain.PeerTypeUser, ID: req.SenderUserID},
				From: domain.Peer{Type: domain.PeerTypeUser, ID: req.SenderUserID},
				Date: req.Date, Body: req.Message, Pts: recipientPts,
				TTLPeriod: item.ttlPeriod, ExpiresAt: item.expiresAt, RandomID: req.RandomID,
			}
		}
		templates[ordinal] = projection
		seen++
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate plain private send batch projections: %w", err)
	}
	if seen != len(input) {
		return nil, fmt.Errorf("persist plain private send batch projection: returned %d rows want %d", seen, len(input))
	}
	return templates, nil
}

const plainPrivateSendBatchProjectionSQL = `
WITH input AS MATERIALIZED (
  SELECT *
  FROM jsonb_to_recordset($1::jsonb) AS i(
    ordinal int,
    sender_user_id bigint,
    recipient_user_id bigint,
    private_message_id bigint,
    sender_box_id int,
    recipient_box_id int,
    message_date int,
    ttl_period int,
    expires_at int,
    body text,
    saved_peer_type text,
    saved_peer_id bigint,
    sender_exclude_auth_key_hex text,
    sender_exclude_session_id bigint,
    recipient_exclude_auth_key_hex text,
    recipient_exclude_session_id bigint,
    receipt_recipient_box_id int,
    sender_snapshot_template jsonb,
    expected_rows int
  )
), box_seed AS MATERIALIZED (
  SELECT
    ordinal, sender_user_id AS owner_user_id, sender_box_id AS box_id,
    recipient_user_id AS peer_id, true AS outgoing,
    saved_peer_type, saved_peer_id,
    decode(sender_exclude_auth_key_hex, 'hex') AS exclude_auth_key_id,
    sender_exclude_session_id AS exclude_session_id,
    private_message_id, sender_user_id AS message_sender_id,
    message_date, ttl_period, expires_at, body
  FROM input
  UNION ALL
  SELECT
    ordinal, recipient_user_id, recipient_box_id,
    sender_user_id, false,
    ''::text, 0::bigint,
    decode(recipient_exclude_auth_key_hex, 'hex'),
    recipient_exclude_session_id,
    private_message_id, sender_user_id,
    message_date, ttl_period, expires_at, body
  FROM input
  WHERE recipient_box_id > 0
), watermark_rows AS (
  INSERT INTO user_update_watermarks (user_id, contiguous_pts)
  SELECT owner_user_id, 1
  FROM box_seed
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
), boxes AS (
  INSERT INTO message_boxes (
    owner_user_id, box_id, private_message_id, message_sender_id,
    peer_type, peer_id, from_user_id, message_date, ttl_period,
    expires_at, outgoing, body, pts, saved_peer_type, saved_peer_id
  )
  SELECT
    owner_user_id, box_id, private_message_id, message_sender_id,
    'user', peer_id, message_sender_id, message_date, ttl_period,
    expires_at, outgoing, body, pts, saved_peer_type, saved_peer_id
  FROM box_input
  WHERE box_id > 0 AND pts > 0
  ORDER BY owner_user_id
  RETURNING owner_user_id, box_id, private_message_id, peer_id, outgoing, pts
), dialog_rows AS (
  INSERT INTO dialogs (
    user_id, peer_type, peer_id, top_message_id, top_message_date, unread_count, ttl_period
  )
  SELECT
    b.owner_user_id, 'user', b.peer_id, b.box_id, i.message_date,
    CASE WHEN b.outgoing THEN 0 ELSE 1 END,
    private_dialog_initial_ttl(
      CASE WHEN b.outgoing THEN b.owner_user_id ELSE b.peer_id END,
      CASE WHEN b.outgoing THEN b.peer_id ELSE b.owner_user_id END)
  FROM boxes b
  JOIN input i ON i.private_message_id = b.private_message_id
  ORDER BY b.owner_user_id
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
    b.owner_user_id, b.pts, 1, i.message_date, 'new_message',
    b.box_id, 'user', b.peer_id
  FROM boxes b
  JOIN input i ON i.private_message_id = b.private_message_id
  ORDER BY b.owner_user_id
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
  ORDER BY e.user_id
  ON CONFLICT DO NOTHING
  RETURNING target_user_id
), expected AS MATERIALIZED (
  SELECT SUM(expected_rows)::int AS rows, COUNT(*)::int AS receipts
  FROM input
), receipt_rows AS (
  UPDATE private_messages pm
  SET sender_box_id = i.sender_box_id,
      sender_pts = sender_box.pts,
      recipient_box_id = i.receipt_recipient_box_id,
      recipient_pts = CASE
        WHEN i.receipt_recipient_box_id = 0 THEN 0
        WHEN i.sender_user_id = i.recipient_user_id THEN sender_box.pts
        ELSE recipient_box.pts
      END,
      sender_snapshot = jsonb_set(
        i.sender_snapshot_template,
        ARRAY['message', 'Pts'],
        to_jsonb(sender_box.pts),
        false
      )
  FROM input i
  JOIN boxes sender_box
    ON sender_box.private_message_id = i.private_message_id
   AND sender_box.owner_user_id = i.sender_user_id
  LEFT JOIN boxes recipient_box
    ON recipient_box.private_message_id = i.private_message_id
   AND recipient_box.owner_user_id = i.recipient_user_id
   AND i.recipient_box_id > 0
  WHERE pm.sender_user_id = i.sender_user_id
    AND pm.id = i.private_message_id
    AND pm.sender_box_id = 0
    AND pm.sender_pts = 0
    AND pm.sender_snapshot = '{}'::jsonb
    AND (SELECT COUNT(*) FROM boxes) = (SELECT rows FROM expected)
  RETURNING i.ordinal
), counts AS MATERIALIZED (
  SELECT
    (SELECT COUNT(*) FROM boxes)::int AS boxes,
    (SELECT COUNT(*) FROM dialog_rows)::int AS dialogs,
    (SELECT COUNT(*) FROM event_rows)::int AS events,
    (SELECT COUNT(*) FROM dispatch_rows)::int AS dispatches,
    (SELECT COUNT(*) FROM receipt_rows)::int AS receipts
)
SELECT
  i.ordinal,
  sender_box.pts::int,
  CASE
    WHEN i.sender_user_id = i.recipient_user_id THEN sender_box.pts
    ELSE COALESCE(recipient_box.pts, 0)
  END::int,
  counts.boxes, counts.dialogs, counts.events, counts.dispatches, counts.receipts
FROM input i
JOIN boxes sender_box
  ON sender_box.private_message_id = i.private_message_id
 AND sender_box.owner_user_id = i.sender_user_id
LEFT JOIN boxes recipient_box
  ON recipient_box.private_message_id = i.private_message_id
 AND recipient_box.owner_user_id = i.recipient_user_id
 AND i.recipient_box_id > 0
CROSS JOIN counts
ORDER BY i.ordinal`
