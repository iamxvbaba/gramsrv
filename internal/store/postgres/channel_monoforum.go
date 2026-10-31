package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

const paidMessageChannelCommissionPermille int64 = 850

// SendMonoforumMessage sends a message to a monoforum (channel Direct Messages) virtual channel, split into subscriber sub-dialogs by saved_peer.
// Direct messages are stored in channel_messages (reusing channel pts/events/difference); the store enforces again at the write boundary: a subscriber
// needs no member row but can only write their own saved_peer, while a parent-channel admin may reply to any subscriber.
func (s *ChannelStore) SendMonoforumMessage(ctx context.Context, req domain.SendMonoforumMessageRequest) (domain.SendChannelMessageResult, error) {
	if req.MonoforumID == 0 || req.SenderUserID == 0 || req.SavedPeer.ID == 0 ||
		req.SavedPeer.Type != domain.PeerTypeUser || strings.TrimSpace(req.Message) == "" && req.Media == nil {
		return domain.SendChannelMessageResult{}, domain.ErrChannelInvalid
	}
	if req.AllowPaidStars < 0 {
		return domain.SendChannelMessageResult{}, domain.ErrStarsInvalidAmount
	}
	requestFingerprint, err := store.MonoforumSendFingerprint(req)
	if err != nil {
		return domain.SendChannelMessageResult{}, err
	}
	req.IdempotencyFingerprint = requestFingerprint
	if req.Date == 0 {
		req.Date = nowUnix()
	}
	if req.RandomID != 0 && !req.IdempotencyPreflighted {
		if dup, found, err := s.LookupChannelSendReplay(ctx, domain.ChannelSendReplayRequest{
			ChannelID:              req.MonoforumID,
			SenderUserID:           req.SenderUserID,
			SavedPeer:              req.SavedPeer,
			RandomID:               req.RandomID,
			IdempotencyFingerprint: requestFingerprint,
		}); err != nil {
			return domain.SendChannelMessageResult{}, err
		} else if found {
			return dup, nil
		}
	}
	beginner, ok := s.db.(txBeginner)
	if !ok {
		return domain.SendChannelMessageResult{}, fmt.Errorf("send monoforum message: db does not support transactions")
	}
	tx, err := beginner.Begin(ctx)
	if err != nil {
		return domain.SendChannelMessageResult{}, fmt.Errorf("begin send monoforum: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()
	channel, err := getChannelByID(ctx, tx, req.MonoforumID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.SendChannelMessageResult{}, domain.ErrChannelInvalid
		}
		return domain.SendChannelMessageResult{}, err
	}
	if channel.Deleted || !channel.Monoforum {
		return domain.SendChannelMessageResult{}, domain.ErrChannelInvalid
	}
	parent, err := getChannelByID(ctx, tx, channel.LinkedMonoforumID)
	if err != nil {
		return domain.SendChannelMessageResult{}, err
	}
	var monoDeleted, parentDeleted, directEnabled bool
	var linkedMonoforumID, monoPrice, parentPrice int64
	if err := tx.QueryRow(ctx, `
SELECT m.deleted, p.deleted, p.broadcast_messages_allowed, p.linked_monoforum_id,
       m.send_paid_messages_stars, p.send_paid_messages_stars
FROM channels m
JOIN channels p ON p.id = m.linked_monoforum_id
WHERE m.id = $1
FOR SHARE OF m, p`, channel.ID).Scan(
		&monoDeleted, &parentDeleted, &directEnabled, &linkedMonoforumID, &monoPrice, &parentPrice,
	); err != nil {
		return domain.SendChannelMessageResult{}, err
	}
	if monoDeleted || parentDeleted || !directEnabled || linkedMonoforumID != channel.ID {
		return domain.SendChannelMessageResult{}, domain.ErrChannelPrivate
	}
	if monoPrice != parentPrice || monoPrice < 0 {
		return domain.SendChannelMessageResult{}, fmt.Errorf("monoforum %d paid-message price disagrees with parent %d", channel.ID, parent.ID)
	}
	channel.SendPaidMessagesStars = monoPrice
	parent.SendPaidMessagesStars = parentPrice
	parentMember, parentMemberErr := s.getChannelMember(ctx, tx, parent.ID, req.SenderUserID)
	if parentMemberErr != nil && !errors.Is(parentMemberErr, domain.ErrChannelPrivate) {
		return domain.SendChannelMessageResult{}, parentMemberErr
	}
	isAdmin := parentMemberErr == nil && parentMember.CanManageDirectMessages()
	if req.SenderUserID != req.SavedPeer.ID && !isAdmin {
		return domain.SendChannelMessageResult{}, domain.ErrChannelAdminRequired
	}
	var senderBalance *domain.StarsBalance
	paidMessageStars := int64(0)
	if !isAdmin && channel.SendPaidMessagesStars > 0 {
		if req.AllowPaidStars < channel.SendPaidMessagesStars {
			return domain.SendChannelMessageResult{}, &domain.StarsPaymentRequiredError{Stars: channel.SendPaidMessagesStars}
		}
		balance := domain.StarsBalance{UserID: req.SenderUserID}
		if err := tx.QueryRow(ctx, `SELECT balance, granted FROM stars_balances WHERE user_id = $1 FOR UPDATE`, req.SenderUserID).
			Scan(&balance.Balance, &balance.Granted); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return domain.SendChannelMessageResult{}, domain.ErrStarsInsufficient
			}
			return domain.SendChannelMessageResult{}, fmt.Errorf("lock paid-message sender balance: %w", err)
		}
		if balance.Balance < channel.SendPaidMessagesStars {
			return domain.SendChannelMessageResult{}, domain.ErrStarsInsufficient
		}
		paidMessageStars = channel.SendPaidMessagesStars
		if err := tx.QueryRow(ctx, `
UPDATE stars_balances
SET balance = balance - $2, updated_at = now()
WHERE user_id = $1
RETURNING balance`, req.SenderUserID, paidMessageStars).Scan(&balance.Balance); err != nil {
			return domain.SendChannelMessageResult{}, fmt.Errorf("debit paid-message sender balance: %w", err)
		}
		if err := insertStarsTxn(ctx, tx, req.SenderUserID, -paidMessageStars, domain.StarsReasonPaidMessage,
			domain.Peer{Type: domain.PeerTypeChannel, ID: parent.ID}, req.Date, "Paid message", ""); err != nil {
			return domain.SendChannelMessageResult{}, err
		}
		channelCredit := paidMessageStars * paidMessageChannelCommissionPermille / 1000
		if channelCredit > 0 {
			if _, err := tx.Exec(ctx, `
INSERT INTO channel_stars_balances(channel_id, balance)
VALUES($1, $2)
ON CONFLICT(channel_id) DO UPDATE
SET balance = channel_stars_balances.balance + EXCLUDED.balance, updated_at = now()`, parent.ID, channelCredit); err != nil {
				return domain.SendChannelMessageResult{}, fmt.Errorf("credit paid-message channel balance: %w", err)
			}
		}
		senderBalance = &balance
	}
	if req.ReplyTo != nil {
		if req.ReplyTo.MessageID <= 0 || req.ReplyTo.Peer != (domain.Peer{Type: domain.PeerTypeChannel, ID: channel.ID}) {
			return domain.SendChannelMessageResult{}, domain.ErrReplyMessageIDInvalid
		}
		var exists bool
		if err := tx.QueryRow(ctx, `
SELECT EXISTS (
    SELECT 1 FROM channel_messages
    WHERE channel_id = $1 AND id = $2 AND NOT deleted
      AND saved_peer_type = $3 AND saved_peer_id = $4
)`, channel.ID, req.ReplyTo.MessageID, string(req.SavedPeer.Type), req.SavedPeer.ID).Scan(&exists); err != nil {
			return domain.SendChannelMessageResult{}, err
		}
		if !exists {
			return domain.SendChannelMessageResult{}, domain.ErrReplyMessageIDInvalid
		}
	}
	from := domain.Peer{Type: domain.PeerTypeUser, ID: req.SenderUserID}
	if isAdmin {
		from = domain.Peer{Type: domain.PeerTypeChannel, ID: parent.ID}
	}
	msgID, err := s.msgIDs.NextChannelMessageID(ctx, req.MonoforumID)
	if err != nil {
		return domain.SendChannelMessageResult{}, fmt.Errorf("allocate monoforum message id: %w", err)
	}
	pts, err := s.reserveChannelPts(ctx, tx, req.MonoforumID)
	if err != nil {
		return domain.SendChannelMessageResult{}, fmt.Errorf("allocate monoforum pts: %w", err)
	}
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
		Forward:          req.Forward,
		Pts:              pts,
	}
	event := domain.ChannelUpdateEvent{
		ChannelID:    req.MonoforumID,
		Type:         domain.ChannelUpdateNewMessage,
		Pts:          pts,
		PtsCount:     1,
		Date:         req.Date,
		Message:      msg,
		SenderUserID: req.SenderUserID,
	}
	if err := insertChannelMessageWithFingerprintTx(ctx, tx, msg, requestFingerprint); err != nil {
		if isUniqueViolation(err) {
			if req.RandomID == 0 {
				return domain.SendChannelMessageResult{}, err
			}
			// The winner lookup must not ask the pool for a second connection
			// while this aborted transaction still owns the first one.
			if rollbackErr := tx.Rollback(ctx); rollbackErr != nil && !errors.Is(rollbackErr, pgx.ErrTxClosed) {
				return domain.SendChannelMessageResult{}, fmt.Errorf("rollback monoforum random_id conflict: %w", rollbackErr)
			}
			committed = true // transaction is finalized by rollback; suppress deferred rollback
			// The four-column unique scope is only the race fence. Acceptance
			// still requires the exact immutable request fingerprint.
			dup, found, dupErr := s.LookupChannelSendReplay(ctx, domain.ChannelSendReplayRequest{
				ChannelID:              req.MonoforumID,
				SenderUserID:           req.SenderUserID,
				SavedPeer:              req.SavedPeer,
				RandomID:               req.RandomID,
				IdempotencyFingerprint: requestFingerprint,
			})
			if dupErr != nil {
				return domain.SendChannelMessageResult{}, dupErr
			}
			if !found {
				return domain.SendChannelMessageResult{}, fmt.Errorf("monoforum random_id unique conflict without replay receipt")
			}
			return dup, nil
		}
		return domain.SendChannelMessageResult{}, err
	}
	if err := insertChannelEventTx(ctx, tx, event); err != nil {
		return domain.SendChannelMessageResult{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE channels SET top_message_id = $2, pts = $3, updated_at = now() WHERE id = $1`, req.MonoforumID, msgID, pts); err != nil {
		return domain.SendChannelMessageResult{}, fmt.Errorf("update monoforum top: %w", err)
	}
	recipients := []int64{req.SavedPeer.ID}
	rows, err := tx.Query(ctx, `
SELECT user_id
FROM channel_members
WHERE channel_id = $1 AND status = 'active'
  AND (role = 'creator' OR (role = 'admin' AND COALESCE((admin_rights->>'ManageDirectMessages')::boolean, false)))
ORDER BY user_id`, parent.ID)
	if err != nil {
		return domain.SendChannelMessageResult{}, fmt.Errorf("list monoforum recipients: %w", err)
	}
	for rows.Next() {
		var recipient int64
		if err := rows.Scan(&recipient); err != nil {
			rows.Close()
			return domain.SendChannelMessageResult{}, err
		}
		recipients = append(recipients, recipient)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return domain.SendChannelMessageResult{}, err
	}
	rows.Close()
	if err := tx.Commit(ctx); err != nil {
		return domain.SendChannelMessageResult{}, fmt.Errorf("commit send monoforum: %w", err)
	}
	committed = true
	channel.TopMessageID = msgID
	channel.Pts = pts
	return domain.SendChannelMessageResult{Channel: channel, Message: msg, Event: event, Recipients: uniqueChannelUserIDs(recipients, 0), SenderStarsBalance: senderBalance}, nil
}

// ListMonoforumHistory fetches a subscriber's (saved_peer) direct-message history within a monoforum, paginated by id descending.
func (s *ChannelStore) ListMonoforumHistory(ctx context.Context, filter domain.MonoforumHistoryFilter) (domain.ChannelHistory, error) {
	if filter.MonoforumID == 0 || filter.SavedPeer.ID == 0 {
		return domain.ChannelHistory{}, domain.ErrChannelInvalid
	}
	channel, err := getChannelByID(ctx, s.db, filter.MonoforumID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ChannelHistory{}, domain.ErrChannelInvalid
		}
		return domain.ChannelHistory{}, err
	}
	if !channel.Monoforum {
		return domain.ChannelHistory{}, domain.ErrChannelInvalid
	}
	limit := filter.Limit
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	args := []any{filter.MonoforumID, string(filter.SavedPeer.Type), filter.SavedPeer.ID}
	where := `channel_id = $1 AND saved_peer_type = $2 AND saved_peer_id = $3 AND NOT deleted`
	if filter.OffsetID > 0 {
		where += fmt.Sprintf(` AND id < $%d`, len(args)+1)
		args = append(args, filter.OffsetID)
	}
	rows, err := s.db.Query(ctx, `SELECT `+channelMessageColumns+` FROM channel_messages WHERE `+where+fmt.Sprintf(` ORDER BY id DESC LIMIT $%d`, len(args)+1), append(args, limit)...)
	if err != nil {
		return domain.ChannelHistory{}, fmt.Errorf("list monoforum history: %w", err)
	}
	defer rows.Close()
	var msgs []domain.ChannelMessage
	for rows.Next() {
		m, err := scanChannelMessage(rows)
		if err != nil {
			return domain.ChannelHistory{}, err
		}
		msgs = append(msgs, m)
	}
	if err := rows.Err(); err != nil {
		return domain.ChannelHistory{}, err
	}
	var count int
	if err := s.db.QueryRow(ctx, `SELECT count(*)::int FROM channel_messages WHERE channel_id = $1 AND saved_peer_type = $2 AND saved_peer_id = $3 AND NOT deleted`,
		filter.MonoforumID, string(filter.SavedPeer.Type), filter.SavedPeer.ID).Scan(&count); err != nil {
		return domain.ChannelHistory{}, fmt.Errorf("count monoforum history: %w", err)
	}
	return domain.ChannelHistory{Messages: msgs, Count: count, Channel: channel}, nil
}

// ResolveMonoforumSend fetches a monoforum channel by id (without requiring the caller to be a monoforum member), and returns whether the caller is
// the Direct Messages manager of its parent broadcast channel. Not a monoforum / missing → ErrChannelInvalid.
func (s *ChannelStore) ResolveMonoforumSend(ctx context.Context, viewerUserID, monoforumID int64) (domain.Channel, bool, error) {
	if viewerUserID == 0 || monoforumID == 0 {
		return domain.Channel{}, false, domain.ErrChannelInvalid
	}
	mono, err := getChannelByID(ctx, s.db, monoforumID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.Channel{}, false, domain.ErrChannelInvalid
		}
		return domain.Channel{}, false, err
	}
	if mono.Deleted || !mono.Monoforum || mono.LinkedMonoforumID == 0 {
		return domain.Channel{}, false, domain.ErrChannelInvalid
	}
	isAdmin := false
	if _, member, memberErr := s.getChannelForMember(ctx, s.db, viewerUserID, mono.LinkedMonoforumID); memberErr == nil {
		isAdmin = member.CanManageDirectMessages()
	} else if !errors.Is(memberErr, domain.ErrChannelPrivate) {
		return domain.Channel{}, false, memberErr
	}
	return mono, isAdmin, nil
}

// ListMonoforumDialogs lists a monoforum's subscriber sub-dialogs (one per saved_peer, taking its top message),
// paginated by top message id descending. Uses the partial index channel_messages_monoforum_sublist_idx.
func (s *ChannelStore) ListMonoforumDialogs(ctx context.Context, filter domain.MonoforumDialogsFilter) (domain.MonoforumDialogList, error) {
	if filter.MonoforumID == 0 {
		return domain.MonoforumDialogList{}, domain.ErrChannelInvalid
	}
	channel, err := getChannelByID(ctx, s.db, filter.MonoforumID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.MonoforumDialogList{}, domain.ErrChannelInvalid
		}
		return domain.MonoforumDialogList{}, err
	}
	if !channel.Monoforum {
		return domain.MonoforumDialogList{}, domain.ErrChannelInvalid
	}
	limit := filter.Limit
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	args := []any{filter.MonoforumID}
	outerWhere := ""
	if filter.OffsetID > 0 {
		outerWhere = fmt.Sprintf(` WHERE top_id < $%d`, len(args)+1)
		args = append(args, filter.OffsetID)
	}
	// DISTINCT ON saved_peer_id hits the partial index channel_messages_monoforum_sublist_idx directly
	// (channel_id, saved_peer_id, id DESC), avoiding an in-memory full sort of all channel direct messages; saved_peer_type is always
	// 'user' for a monoforum (validated on send), so taking the top row per group matches grouping by (type,id).
	q := `
SELECT saved_peer_type, saved_peer_id, top_id FROM (
    SELECT DISTINCT ON (saved_peer_id) saved_peer_type, saved_peer_id, id AS top_id
    FROM channel_messages
    WHERE channel_id = $1 AND saved_peer_id <> 0 AND NOT deleted
    ORDER BY saved_peer_id, id DESC
) t` + outerWhere + fmt.Sprintf(` ORDER BY top_id DESC LIMIT $%d`, len(args)+1)
	args = append(args, limit)
	rows, err := s.db.Query(ctx, q, args...)
	if err != nil {
		return domain.MonoforumDialogList{}, fmt.Errorf("list monoforum dialogs: %w", err)
	}
	type subRef struct {
		peer  domain.Peer
		topID int
	}
	var refs []subRef
	var topIDs []int
	for rows.Next() {
		var spType string
		var spID int64
		var topID int
		if err := rows.Scan(&spType, &spID, &topID); err != nil {
			rows.Close()
			return domain.MonoforumDialogList{}, err
		}
		refs = append(refs, subRef{peer: domain.Peer{Type: domain.PeerType(spType), ID: spID}, topID: topID})
		topIDs = append(topIDs, topID)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return domain.MonoforumDialogList{}, err
	}
	rows.Close()

	msgByID := make(map[int]domain.ChannelMessage, len(topIDs))
	if len(topIDs) > 0 {
		mrows, err := s.db.Query(ctx, `SELECT `+channelMessageColumns+` FROM channel_messages WHERE channel_id = $1 AND id = ANY($2::int[])`, filter.MonoforumID, topIDs)
		if err != nil {
			return domain.MonoforumDialogList{}, fmt.Errorf("load monoforum dialog top messages: %w", err)
		}
		for mrows.Next() {
			m, err := scanChannelMessage(mrows)
			if err != nil {
				mrows.Close()
				return domain.MonoforumDialogList{}, err
			}
			msgByID[m.ID] = m
		}
		if err := mrows.Err(); err != nil {
			mrows.Close()
			return domain.MonoforumDialogList{}, err
		}
		mrows.Close()
	}

	out := domain.MonoforumDialogList{MonoforumID: filter.MonoforumID, Channel: channel}
	for _, r := range refs {
		m := msgByID[r.topID]
		out.Dialogs = append(out.Dialogs, domain.MonoforumDialog{SavedPeer: r.peer, TopMessageID: r.topID, TopMessageDate: m.Date})
		if m.ID != 0 {
			out.Messages = append(out.Messages, m)
		}
	}
	if err := s.db.QueryRow(ctx, `SELECT count(DISTINCT saved_peer_id)::int FROM channel_messages WHERE channel_id = $1 AND saved_peer_id <> 0 AND NOT deleted`, filter.MonoforumID).Scan(&out.Count); err != nil {
		return domain.MonoforumDialogList{}, fmt.Errorf("count monoforum dialogs: %w", err)
	}
	return out, nil
}
