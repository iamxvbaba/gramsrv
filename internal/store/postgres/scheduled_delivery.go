package postgres

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

// The row lock is held until the normal message, its PTS/outbox, and the
// schedule transition all commit. Cancellation and expired claims cannot send.
func lockScheduledDelivery(ctx context.Context, tx pgx.Tx, claim *domain.ScheduledDelivery, owner int64, peer domain.Peer, randomID int64) error {
	if claim == nil {
		return nil
	}
	if claim.OwnerUserID != owner || claim.ID <= 0 || claim.Generation <= 0 {
		return domain.ErrScheduledClaimLost
	}
	if claim.Payload == nil {
		return store.ErrDeliveryOutboxRequired
	}
	var valid bool
	err := tx.QueryRow(ctx, `SELECT state = 'dispatching' AND generation = $3 AND peer_type = $4 AND peer_id = $5 AND random_id = $6
 FROM scheduled_messages WHERE owner_user_id=$1 AND scheduled_id=$2 FOR UPDATE`, owner, claim.ID, claim.Generation, string(peer.Type), peer.ID, randomID).Scan(&valid)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !valid) {
		return domain.ErrScheduledClaimLost
	}
	return err
}

func completeScheduledDelivery(ctx context.Context, tx pgx.Tx, claim *domain.ScheduledDelivery, sentID, date int) (*domain.ScheduledMessage, error) {
	if claim == nil {
		return nil, nil
	}
	if sentID <= 0 {
		return nil, fmt.Errorf("scheduled delivery missing sent id")
	}
	row := tx.QueryRow(ctx, `UPDATE scheduled_messages SET state='sent', sent_message_id=$4, lease_until=0, updated_at=$5
 WHERE owner_user_id=$1 AND scheduled_id=$2 AND generation=$3 AND state='dispatching'
 RETURNING `+scheduledMessageSelectColumns(), claim.OwnerUserID, claim.ID, claim.Generation, sentID, date)
	sent, err := scanScheduledMessage(row)
	if err != nil {
		return nil, err
	}
	var next *domain.ScheduledMessage
	if sent.ScheduleRepeatPeriod > 0 {
		if !domain.ValidScheduleRepeat(sent.ScheduleRepeatPeriod) || int64(date)+int64(sent.ScheduleRepeatPeriod) >= domain.ScheduleWhenOnline {
			return nil, domain.ErrScheduleInvalid
		}
		var premium bool
		if err := tx.QueryRow(ctx, `SELECT COALESCE(premium_expires_at > to_timestamp($2), false) FROM users WHERE id=$1 AND deleted_at IS NULL`, claim.OwnerUserID, date).Scan(&premium); err != nil {
			return nil, err
		}
		// Losing Premium stops future occurrences; the already scheduled message is delivered once.
		if premium {
			id, err := nextScheduledMessageID(ctx, tx, claim.OwnerUserID)
			if err != nil {
				return nil, err
			}
			var bytes [8]byte
			if _, err := rand.Read(bytes[:]); err != nil {
				return nil, err
			}
			randomID := int64(binary.LittleEndian.Uint64(bytes[:]) & 0x7fffffffffffffff)
			if randomID == 0 {
				return nil, fmt.Errorf("empty scheduled occurrence identity")
			}
			_, err = tx.Exec(ctx, `INSERT INTO scheduled_messages (
 owner_user_id, scheduled_id, peer_type, peer_id, random_id, message_date,
 body, entities, media, rich_message, silent, noforwards,
 reply_to_msg_id, reply_to_peer_type, reply_to_peer_id, reply_to_top_id,
 quote_text, quote_entities, quote_offset, fwd_from_peer_type, fwd_from_peer_id, fwd_from_name, fwd_date,
 send_as_peer_type, send_as_peer_id, schedule_date, schedule_repeat_period, state, created_at, updated_at,
 reply_to_story_id,reply_forum_topic,reply_external,fwd_channel_post,fwd_saved_from_peer_type,fwd_saved_from_peer_id,fwd_saved_from_msg_id)
 SELECT owner_user_id,$3,peer_type,peer_id,$4,$5,
 body,entities,media,rich_message,silent,noforwards,
 reply_to_msg_id,reply_to_peer_type,reply_to_peer_id,reply_to_top_id,
 quote_text,quote_entities,quote_offset,fwd_from_peer_type,fwd_from_peer_id,fwd_from_name,fwd_date,
 send_as_peer_type,send_as_peer_id,$5+schedule_repeat_period,schedule_repeat_period,'pending',$5,$5,
 reply_to_story_id,reply_forum_topic,reply_external,fwd_channel_post,fwd_saved_from_peer_type,fwd_saved_from_peer_id,fwd_saved_from_msg_id
 FROM scheduled_messages WHERE owner_user_id=$1 AND scheduled_id=$2`, claim.OwnerUserID, claim.ID, id, randomID, date)
			if err != nil {
				return nil, err
			}
			value, found, err := getScheduledByID(ctx, tx, claim.OwnerUserID, id)
			if err != nil {
				return nil, err
			}
			if !found {
				return nil, fmt.Errorf("next scheduled occurrence missing")
			}
			next = &value
		}
	}
	if err := refreshHasScheduledTx(ctx, tx, claim.OwnerUserID, sent.Peer); err != nil {
		return nil, err
	}
	// v2: the owner's scheduled-box updates are a durable absolute delivery in
	// this same transaction, never a post-commit push.
	payload, err := claim.Payload(domain.ScheduledCompletion{Sent: sent, Next: next})
	if err != nil {
		return nil, fmt.Errorf("build scheduled completion delivery: %w", err)
	}
	if _, err := applyDeliveryEffectsTx(ctx, tx, []store.DeliveryEffect{store.AbsoluteDeliveryEffect(store.DeliveryOutboxEnqueue{
		TargetUserID: claim.OwnerUserID, ExcludeAuthKeyID: claim.ExcludeAuthKeyID, ExcludeSessionID: claim.ExcludeSessionID,
		Payload: payload, RecoveryPolicy: store.OutboxRecoveryAbsoluteReload,
	})}); err != nil {
		return nil, fmt.Errorf("apply scheduled completion delivery: %w", err)
	}
	return next, nil
}
