package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"

	"telesrv/internal/domain"
	"telesrv/internal/store/postgres/sqlcgen"
)

func (s *ChannelStore) SetChannelMessageReactions(ctx context.Context, req domain.SetChannelMessageReactionsRequest) (domain.ChannelMessageReactionsResult, error) {
	if req.UserID == 0 || req.ChannelID == 0 || req.MessageID <= 0 || req.MessageID > domain.MaxMessageBoxID {
		return domain.ChannelMessageReactionsResult{}, domain.ErrChannelInvalid
	}
	if len(req.Reactions) > domain.MaxChannelMessageReactionsPerUser {
		return domain.ChannelMessageReactionsResult{}, domain.ErrChannelInvalid
	}
	for _, reaction := range req.Reactions {
		if !reaction.Valid() {
			return domain.ChannelMessageReactionsResult{}, domain.ErrChannelInvalid
		}
	}
	req.Reactions = domain.TrimMessageReactionsToUserMax(req.Reactions, req.ReactionsPerUserMax)
	if req.Date <= 0 {
		req.Date = nowUnix()
	}
	beginner, ok := s.db.(txBeginner)
	if !ok {
		return domain.ChannelMessageReactionsResult{}, fmt.Errorf("set channel message reactions: db does not support transactions")
	}
	tx, err := beginner.Begin(ctx)
	if err != nil {
		return domain.ChannelMessageReactionsResult{}, fmt.Errorf("begin set channel message reactions: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()
	channel, member, _, err := s.getChannelForViewer(ctx, tx, req.UserID, req.ChannelID)
	if err != nil {
		return domain.ChannelMessageReactionsResult{}, err
	}
	if len(req.Reactions) > 0 {
		selfBoostsApplied := 0
		if channel.Megagroup {
			selfBoostsApplied, err = countActiveUserBoostsForPeer(ctx, tx, req.UserID, domain.Peer{Type: domain.PeerTypeChannel, ID: req.ChannelID}, req.Date)
			if err != nil {
				return domain.ChannelMessageReactionsResult{}, err
			}
		}
		if domain.ChannelBannedRightsBlockReactions(channel, member, selfBoostsApplied) {
			return domain.ChannelMessageReactionsResult{}, domain.ErrChannelWriteForbidden
		}
	}
	msg, err := s.getChannelMessage(ctx, tx, req.ChannelID, req.MessageID)
	if err != nil {
		return domain.ChannelMessageReactionsResult{}, err
	}
	if msg.Deleted || msg.Action != nil || msg.ID <= member.AvailableMinID ||
		!channelMessageVisibleToViewer(channel, member, req.UserID, msg) {
		return domain.ChannelMessageReactionsResult{}, domain.ErrMessageIDInvalid
	}
	// Only additions/replacements are policy-gated; an empty vector is a retraction, and
	// retracting existing reactions must remain allowed after the policy is tightened.
	for _, reaction := range req.Reactions {
		if !channel.ReactionPolicy.AllowsReaction(reaction) {
			return domain.ChannelMessageReactionsResult{}, domain.ErrReactionInvalid
		}
	}
	if len(req.Reactions) > 0 {
		// Under READ COMMITTED, concurrent additions of different new kinds are mutually
		// invisible and both pass the dedup gate; a transaction-level advisory lock
		// serializes gated writes per (channel, message) (independent of
		// lockUsersForUpdate's single-argument key space). Retractions bypass the gate and
		// need no lock.
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashint8($1::bigint), $2::int)`, req.ChannelID, req.MessageID); err != nil {
			return domain.ChannelMessageReactionsResult{}, fmt.Errorf("advisory lock channel message reactions: %w", err)
		}
		// The official REACTIONS_TOO_MANY only blocks "introducing a kind that does not
		// already exist on the message": when the existing set is already over the limit
		// (an admin lowered reactions_limit / pre-deployment data), re-sending one's own
		// reaction or voting on an existing kind must be allowed, otherwise clicking a
		// legitimate chip would also return 400.
		existing := make(map[string]struct{})
		final := make(map[string]struct{})
		rows, err := tx.Query(ctx, `
SELECT reaction_type, reaction_value, BOOL_OR(reacted_user_id <> $3)
FROM channel_message_reactions
WHERE channel_id = $1 AND message_id = $2
GROUP BY reaction_type, reaction_value`, req.ChannelID, req.MessageID, req.UserID)
		if err != nil {
			return domain.ChannelMessageReactionsResult{}, fmt.Errorf("list channel message reaction values: %w", err)
		}
		for rows.Next() {
			var reactionType, value string
			var byOthers bool
			if err := rows.Scan(&reactionType, &value, &byOthers); err != nil {
				rows.Close()
				return domain.ChannelMessageReactionsResult{}, err
			}
			key := string(reactionType) + "\x00" + value
			existing[key] = struct{}{}
			if byOthers {
				final[key] = struct{}{}
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return domain.ChannelMessageReactionsResult{}, err
		}
		rows.Close()
		newKind := false
		for _, reaction := range req.Reactions {
			key := reaction.Key()
			if _, ok := existing[key]; !ok {
				newKind = true
			}
			final[key] = struct{}{}
		}
		if newKind && len(final) > channel.ReactionPolicy.UniqueReactionsLimit() {
			return domain.ChannelMessageReactionsResult{}, domain.ErrReactionsTooMany
		}
	}
	if _, err := tx.Exec(ctx, `
DELETE FROM channel_message_reactions
WHERE channel_id = $1 AND message_id = $2 AND reacted_user_id = $3`, req.ChannelID, req.MessageID, req.UserID); err != nil {
		return domain.ChannelMessageReactionsResult{}, fmt.Errorf("delete channel message reactions: %w", err)
	}
	// Broadcast channel reactions are anonymous (official semantics); the author receives
	// no unread badge and no unread bookkeeping is written.
	unreadEligible := !channel.Broadcast || channel.Megagroup
	for i, reaction := range req.Reactions {
		if _, err := tx.Exec(ctx, `
INSERT INTO channel_message_reactions (
    channel_id, message_id, reacted_user_id, sender_user_id, reaction_type, reaction_value,
    big, unread, chosen_order, reaction_date
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
			req.ChannelID, req.MessageID, req.UserID, msg.SenderUserID, string(reaction.Type), reaction.Value(), req.Big, unreadEligible && msg.SenderUserID != 0 && msg.SenderUserID != req.UserID, i+1, req.Date); err != nil {
			return domain.ChannelMessageReactionsResult{}, fmt.Errorf("insert channel message reaction: %w", err)
		}
		if req.AddToRecent {
			if _, err := tx.Exec(ctx, `
INSERT INTO user_recent_reactions (user_id, reaction_type, reaction_value, reaction_date)
VALUES ($1,$2,$3,$4)
ON CONFLICT (user_id, reaction_type, reaction_value)
DO UPDATE SET reaction_date = EXCLUDED.reaction_date, updated_at = now()`,
				req.UserID, string(reaction.Type), reaction.Value(), req.Date); err != nil {
				return domain.ChannelMessageReactionsResult{}, fmt.Errorf("upsert recent message reaction: %w", err)
			}
		}
		if _, err := tx.Exec(ctx, `
INSERT INTO user_top_reactions (user_id, reaction_type, reaction_value, reaction_count, reaction_date)
VALUES ($1,$2,$3,1,$4)
ON CONFLICT (user_id, reaction_type, reaction_value)
DO UPDATE SET reaction_count = user_top_reactions.reaction_count + 1, reaction_date = EXCLUDED.reaction_date, updated_at = now()`,
			req.UserID, string(reaction.Type), reaction.Value(), req.Date); err != nil {
			return domain.ChannelMessageReactionsResult{}, fmt.Errorf("upsert top message reaction: %w", err)
		}
	}
	if unreadEligible {
		if err := refreshChannelUnreadReactionsCountTx(ctx, tx, msg.SenderUserID, req.ChannelID); err != nil {
			return domain.ChannelMessageReactionsResult{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.ChannelMessageReactionsResult{}, fmt.Errorf("commit set channel message reactions: %w", err)
	}
	committed = true
	messages := []domain.ChannelMessage{msg}
	if err := s.populateChannelMessagesReactions(ctx, s.db, req.UserID, []domain.Channel{channel}, messages); err != nil {
		return domain.ChannelMessageReactionsResult{}, err
	}
	msg = messages[0]
	reactions := emptyChannelMessageReactions(channel)
	if msg.Reactions != nil {
		reactions = *msg.Reactions
	} else {
		msg.Reactions = &reactions
	}
	// sendReaction realtime fanout uses the online viewer scope (capped at the rpc layer),
	// without pre-warming the full member list.
	return domain.ChannelMessageReactionsResult{
		Channel:    channel,
		Message:    msg,
		Messages:   []domain.ChannelMessage{msg},
		Reactions:  reactions,
		Recipients: []int64{req.UserID},
	}, nil
}

// AddChannelMessagePaidReaction atomically binds random_id to an immutable
// command, debits the payer, credits the channel, increments the reaction and
// stores the first settlement receipt. It deliberately allocates no PTS.
func (s *ChannelStore) AddChannelMessagePaidReaction(ctx context.Context, req domain.SendChannelPaidReactionRequest) (domain.ChannelMessagePaidReactionResult, error) {
	if req.UserID == 0 || req.ChannelID == 0 || req.MessageID <= 0 || req.MessageID > domain.MaxMessageBoxID || req.RandomID == 0 {
		return domain.ChannelMessagePaidReactionResult{}, domain.ErrChannelInvalid
	}
	if req.Stars <= 0 || req.Stars > domain.MaxPaidReactionStarsPerRequest {
		return domain.ChannelMessagePaidReactionResult{}, domain.ErrChannelInvalid
	}
	privacyKind := req.Privacy.Kind
	if privacyKind == "" {
		privacyKind = domain.PaidReactionPrivacyDefault
	}
	displayPeer := req.DisplayPeer
	switch privacyKind {
	case domain.PaidReactionPrivacyDefault:
		if req.Anonymous || displayPeer.ID != 0 {
			return domain.ChannelMessagePaidReactionResult{}, domain.ErrChannelInvalid
		}
	case domain.PaidReactionPrivacyAccountDefault:
		if req.Anonymous && displayPeer.ID != 0 {
			return domain.ChannelMessagePaidReactionResult{}, domain.ErrChannelInvalid
		}
	case domain.PaidReactionPrivacyAnonymous:
		if !req.Anonymous || displayPeer.ID != 0 {
			return domain.ChannelMessagePaidReactionResult{}, domain.ErrChannelInvalid
		}
	case domain.PaidReactionPrivacyPeer:
		if req.Anonymous || req.Privacy.Peer == nil || req.Privacy.Peer.Type != domain.PeerTypeChannel || req.Privacy.Peer.ID <= 0 {
			return domain.ChannelMessagePaidReactionResult{}, domain.ErrPaidReactionSendAsPeerInvalid
		}
		if displayPeer.ID == 0 {
			displayPeer = *req.Privacy.Peer
		}
		if displayPeer != *req.Privacy.Peer {
			return domain.ChannelMessagePaidReactionResult{}, domain.ErrPaidReactionSendAsPeerInvalid
		}
	default:
		return domain.ChannelMessagePaidReactionResult{}, domain.ErrChannelInvalid
	}
	if displayPeer.ID != 0 && (displayPeer.Type != domain.PeerTypeChannel || displayPeer.ID <= 0) {
		return domain.ChannelMessagePaidReactionResult{}, domain.ErrPaidReactionSendAsPeerInvalid
	}
	if req.Date <= 0 {
		req.Date = nowUnix()
	}
	fingerprint := req.Fingerprint()
	beginner, ok := s.db.(txBeginner)
	if !ok {
		return domain.ChannelMessagePaidReactionResult{}, fmt.Errorf("add channel paid reaction: db does not support transactions")
	}
	tx, err := beginner.Begin(ctx)
	if err != nil {
		return domain.ChannelMessagePaidReactionResult{}, fmt.Errorf("begin add channel paid reaction: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()
	if _, err := tx.Exec(ctx, `
DELETE FROM channel_paid_reaction_commands
WHERE ctid IN (
    SELECT ctid FROM channel_paid_reaction_commands
    WHERE created_at < now() - ($1::bigint * interval '1 second')
    ORDER BY created_at ASC
    LIMIT 32
)`, domain.PaidReactionReceiptRetentionSeconds); err != nil {
		return domain.ChannelMessagePaidReactionResult{}, fmt.Errorf("prune paid reaction receipts: %w", err)
	}

	// The placeholder is invisible until commit. A concurrent retry blocks on
	// the primary key and then observes either the complete receipt or no row if
	// this transaction rolled back.
	inserted := false
	err = tx.QueryRow(ctx, `
INSERT INTO channel_paid_reaction_commands
    (payer_user_id,random_id,request_fingerprint,channel_id,message_id,stars,anonymous,reaction_date)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
ON CONFLICT (payer_user_id,random_id) DO NOTHING
RETURNING true`, req.UserID, req.RandomID, fingerprint[:], req.ChannelID, req.MessageID, req.Stars, req.Anonymous, req.Date).Scan(&inserted)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return domain.ChannelMessagePaidReactionResult{}, fmt.Errorf("reserve paid reaction command: %w", err)
	}

	result := domain.ChannelMessagePaidReactionResult{}
	if !inserted {
		var (
			storedFingerprint []byte
			completed         bool
			resultSnapshot    []byte
		)
		if err := tx.QueryRow(ctx, `
SELECT request_fingerprint,completed,result_snapshot
FROM channel_paid_reaction_commands
WHERE payer_user_id=$1 AND random_id=$2`, req.UserID, req.RandomID).
			Scan(&storedFingerprint, &completed, &resultSnapshot); err != nil {
			return domain.ChannelMessagePaidReactionResult{}, fmt.Errorf("load paid reaction receipt: %w", err)
		}
		if !bytes.Equal(storedFingerprint, fingerprint[:]) {
			return domain.ChannelMessagePaidReactionResult{}, domain.ErrMessageRandomIDDuplicate
		}
		if !completed {
			return domain.ChannelMessagePaidReactionResult{}, fmt.Errorf("paid reaction receipt is incomplete")
		}
		result, err = decodePaidReactionResultSnapshot(resultSnapshot)
		if err != nil {
			return domain.ChannelMessagePaidReactionResult{}, fmt.Errorf("decode paid reaction receipt: %w", err)
		}
		// Financial effects remain the immutable first receipt, but an absolute
		// balance update must reflect current authority rather than rewinding a
		// client after later transactions.
		if err := tx.QueryRow(ctx, `SELECT balance,granted FROM stars_balances WHERE user_id=$1`, req.UserID).
			Scan(&result.PayerBalance.Balance, &result.PayerBalance.Granted); err != nil {
			return domain.ChannelMessagePaidReactionResult{}, fmt.Errorf("load current paid reaction replay balance: %w", err)
		}
		result.PayerBalance.UserID = req.UserID
		result.Duplicate = true
		result.Recipients = []int64{req.UserID}
		if err := tx.Commit(ctx); err != nil {
			return domain.ChannelMessagePaidReactionResult{}, fmt.Errorf("commit paid reaction replay: %w", err)
		}
		committed = true
		return result, nil
	}
	if domain.PaidReactionRandomIDExpired(req.RandomID, req.Date) {
		return domain.ChannelMessagePaidReactionResult{}, domain.ErrPaidReactionRandomIDExpired
	}
	var rejectRandomIDThrough int
	if err := tx.QueryRow(ctx, `SELECT reject_random_id_through
FROM channel_paid_reaction_cutover
WHERE singleton`).Scan(&rejectRandomIDThrough); err != nil {
		return domain.ChannelMessagePaidReactionResult{}, fmt.Errorf("load paid reaction cutover fence: %w", err)
	}
	if rejectRandomIDThrough > 0 && domain.PaidReactionRandomIDTimestamp(req.RandomID) <= rejectRandomIDThrough {
		return domain.ChannelMessagePaidReactionResult{}, domain.ErrPaidReactionCutoverAmbiguous
	}

	if inserted {
		// Lock target and display channel/member rows in one global channel-id
		// order. This prevents target-A/as-B versus target-B/as-A deadlocks and
		// holds paid policy plus send-as ownership through settlement.
		lockIDs := []int64{req.ChannelID}
		if displayPeer.ID != 0 && displayPeer.ID != req.ChannelID {
			lockIDs = append(lockIDs, displayPeer.ID)
		}
		sort.Slice(lockIDs, func(i, j int) bool { return lockIDs[i] < lockIDs[j] })
		rows, err := tx.Query(ctx, `SELECT id FROM channels WHERE id=ANY($1::bigint[]) ORDER BY id FOR SHARE`, lockIDs)
		if err != nil {
			return domain.ChannelMessagePaidReactionResult{}, fmt.Errorf("lock paid reaction channels: %w", err)
		}
		for rows.Next() {
			var ignored int64
			if err := rows.Scan(&ignored); err != nil {
				rows.Close()
				return domain.ChannelMessagePaidReactionResult{}, fmt.Errorf("scan paid reaction channel lock: %w", err)
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return domain.ChannelMessagePaidReactionResult{}, fmt.Errorf("lock paid reaction channels: %w", err)
		}
		rows.Close()
		rows, err = tx.Query(ctx, `
SELECT channel_id FROM channel_members
WHERE user_id=$1 AND channel_id=ANY($2::bigint[])
ORDER BY channel_id FOR SHARE`, req.UserID, lockIDs)
		if err != nil {
			return domain.ChannelMessagePaidReactionResult{}, fmt.Errorf("lock paid reaction members: %w", err)
		}
		for rows.Next() {
			var ignored int64
			if err := rows.Scan(&ignored); err != nil {
				rows.Close()
				return domain.ChannelMessagePaidReactionResult{}, fmt.Errorf("scan paid reaction member lock: %w", err)
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return domain.ChannelMessagePaidReactionResult{}, fmt.Errorf("lock paid reaction members: %w", err)
		}
		rows.Close()
	}
	channel, member, err := s.getChannelForMember(ctx, tx, req.UserID, req.ChannelID)
	if err != nil {
		return domain.ChannelMessagePaidReactionResult{}, err
	}
	// Paid reactions are available only on broadcast posts whose owner enabled
	// the durable paid reaction policy. A committed exact replay is still valid
	// if the owner disables the feature later.
	if !channel.Broadcast || channel.Megagroup || (inserted && !channel.ReactionPolicy.PaidEnabled) {
		return domain.ChannelMessagePaidReactionResult{}, domain.ErrReactionInvalid
	}
	if inserted {
		var lockedMessageID int
		if err := tx.QueryRow(ctx, `SELECT id FROM channel_messages WHERE channel_id=$1 AND id=$2 FOR UPDATE`, req.ChannelID, req.MessageID).Scan(&lockedMessageID); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return domain.ChannelMessagePaidReactionResult{}, fmt.Errorf("lock paid reaction message: %w", err)
		}
	}
	msg, err := s.getChannelMessage(ctx, tx, req.ChannelID, req.MessageID)
	if err != nil {
		return domain.ChannelMessagePaidReactionResult{}, err
	}
	if msg.Deleted || msg.Action != nil || msg.ID <= member.AvailableMinID {
		return domain.ChannelMessagePaidReactionResult{}, domain.ErrMessageIDInvalid
	}
	if displayPeer.ID != 0 {
		displayChannel, displayMember, err := s.getChannelForMember(ctx, tx, req.UserID, displayPeer.ID)
		if err != nil || displayChannel.Deleted || !displayChannel.Broadcast ||
			displayChannel.CreatorUserID != req.UserID || displayMember.Role != domain.ChannelRoleCreator {
			return domain.ChannelMessagePaidReactionResult{}, domain.ErrPaidReactionSendAsPeerInvalid
		}
	}

	if inserted {
		var (
			balance        int64
			granted        bool
			payerTxnID     int64
			channelTxnID   int64
			reactorStars   int64
			totalStars     int64
			channelBalance int64
		)
		storedDisplayPeer := domain.Peer{Type: domain.PeerTypeUser, ID: req.UserID}
		if displayPeer.ID != 0 {
			storedDisplayPeer = displayPeer
		}
		// Materialize the lazy grant with a unique-row upsert before locking it.
		// Concurrent distinct commands from a fresh payer then serialize on the
		// same row and exactly one transaction records the starting grant.
		insertedGrant := false
		err := tx.QueryRow(ctx, `
INSERT INTO stars_balances(user_id,balance,granted,updated_at)
VALUES($1,$2,true,now())
ON CONFLICT(user_id) DO NOTHING
RETURNING true`, req.UserID, s.paidReactionStartingGrant).Scan(&insertedGrant)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return domain.ChannelMessagePaidReactionResult{}, fmt.Errorf("materialize paid reaction payer balance: %w", err)
		}
		if insertedGrant && s.paidReactionStartingGrant > 0 {
			if err := insertStarsTxn(ctx, tx, req.UserID, s.paidReactionStartingGrant, domain.StarsReasonGrant, domain.Peer{}, req.Date, "", ""); err != nil {
				return domain.ChannelMessagePaidReactionResult{}, fmt.Errorf("insert paid reaction payer grant: %w", err)
			}
		}
		err = tx.QueryRow(ctx, `SELECT balance,granted FROM stars_balances WHERE user_id=$1 FOR UPDATE`, req.UserID).Scan(&balance, &granted)
		switch {
		case err != nil:
			return domain.ChannelMessagePaidReactionResult{}, fmt.Errorf("lock paid reaction payer balance: %w", err)
		case !granted && s.paidReactionStartingGrant > 0:
			if err := tx.QueryRow(ctx, `UPDATE stars_balances SET balance=balance+$2,granted=true,updated_at=now() WHERE user_id=$1 RETURNING balance`, req.UserID, s.paidReactionStartingGrant).Scan(&balance); err != nil {
				return domain.ChannelMessagePaidReactionResult{}, fmt.Errorf("apply paid reaction payer grant: %w", err)
			}
			granted = true
			if err := insertStarsTxn(ctx, tx, req.UserID, s.paidReactionStartingGrant, domain.StarsReasonGrant, domain.Peer{}, req.Date, "", ""); err != nil {
				return domain.ChannelMessagePaidReactionResult{}, fmt.Errorf("insert paid reaction payer grant: %w", err)
			}
		}
		if balance < req.Stars {
			return domain.ChannelMessagePaidReactionResult{}, domain.ErrStarsInsufficient
		}
		if err := tx.QueryRow(ctx, `UPDATE stars_balances SET balance=balance-$2,updated_at=now() WHERE user_id=$1 RETURNING balance`, req.UserID, req.Stars).Scan(&balance); err != nil {
			return domain.ChannelMessagePaidReactionResult{}, fmt.Errorf("debit paid reaction payer: %w", err)
		}
		if err := tx.QueryRow(ctx, `
INSERT INTO stars_transactions(user_id,peer_type,peer_id,amount,reason,title,description,date)
VALUES($1,'channel',$2,$3,$4,'Paid reaction','',$5)
RETURNING id`, req.UserID, req.ChannelID, -req.Stars, string(domain.StarsReasonReaction), req.Date).Scan(&payerTxnID); err != nil {
			return domain.ChannelMessagePaidReactionResult{}, fmt.Errorf("insert paid reaction payer transaction: %w", err)
		}
		if err := tx.QueryRow(ctx, `
INSERT INTO channel_message_paid_reactions
    (channel_id,message_id,reactor_user_id,stars,anonymous,reaction_date,display_peer_type,display_peer_id)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
ON CONFLICT (channel_id, message_id, reactor_user_id)
DO UPDATE SET stars = channel_message_paid_reactions.stars + EXCLUDED.stars,
             anonymous = EXCLUDED.anonymous,
		     reaction_date = EXCLUDED.reaction_date,
		     display_peer_type = EXCLUDED.display_peer_type,
		     display_peer_id = EXCLUDED.display_peer_id
RETURNING stars`, req.ChannelID, req.MessageID, req.UserID, req.Stars, req.Anonymous, req.Date,
			storedDisplayPeer.Type, storedDisplayPeer.ID).Scan(&reactorStars); err != nil {
			return domain.ChannelMessagePaidReactionResult{}, fmt.Errorf("upsert channel paid reaction: %w", err)
		}
		if err := tx.QueryRow(ctx, `SELECT COALESCE(SUM(stars),0) FROM channel_message_paid_reactions WHERE channel_id=$1 AND message_id=$2`, req.ChannelID, req.MessageID).Scan(&totalStars); err != nil {
			return domain.ChannelMessagePaidReactionResult{}, fmt.Errorf("sum channel paid reactions: %w", err)
		}
		// messages.sendPaidReaction transfers the full count to the channel;
		// unlike paid messages there is no commission parameter in this method.
		if err := tx.QueryRow(ctx, `
INSERT INTO channel_stars_balances(channel_id,balance) VALUES($1,$2)
ON CONFLICT(channel_id) DO UPDATE SET balance=channel_stars_balances.balance+EXCLUDED.balance,updated_at=now()
RETURNING balance`, req.ChannelID, req.Stars).Scan(&channelBalance); err != nil {
			return domain.ChannelMessagePaidReactionResult{}, fmt.Errorf("credit paid reaction channel: %w", err)
		}
		if err := tx.QueryRow(ctx, `
INSERT INTO channel_stars_transactions(channel_id,actor_user_id,amount,reason,peer_type,peer_id,date)
VALUES($1,$2,$3,$4,'user',$2,$5)
RETURNING id`, req.ChannelID, req.UserID, req.Stars, string(domain.StarsReasonReaction), req.Date).Scan(&channelTxnID); err != nil {
			return domain.ChannelMessagePaidReactionResult{}, fmt.Errorf("insert paid reaction channel transaction: %w", err)
		}
		result.PayerBalance = domain.StarsBalance{UserID: req.UserID, Balance: balance, Granted: granted}
		result.ChannelBalance = channelBalance
		messages := []domain.ChannelMessage{msg}
		if err := s.populateChannelMessagesReactions(ctx, tx, req.UserID, []domain.Channel{channel}, messages); err != nil {
			return domain.ChannelMessagePaidReactionResult{}, fmt.Errorf("project paid reaction receipt: %w", err)
		}
		msg = messages[0]
		if msg.Reactions == nil || msg.Reactions.Paid == nil {
			return domain.ChannelMessagePaidReactionResult{}, fmt.Errorf("project paid reaction receipt: paid aggregate missing")
		}
		result.Channel = channel
		result.Message = msg
		result.Paid = *msg.Reactions.Paid
		result.DisplayChannels, err = listChannelsByIDsInOrder(ctx, tx, paidReactionDisplayChannelIDs(result.Paid))
		if err != nil {
			return domain.ChannelMessagePaidReactionResult{}, fmt.Errorf("snapshot paid reaction display channels: %w", err)
		}
		resultSnapshot, err := encodePaidReactionResultSnapshot(result)
		if err != nil {
			return domain.ChannelMessagePaidReactionResult{}, fmt.Errorf("encode paid reaction receipt: %w", err)
		}
		commandTag, err := tx.Exec(ctx, `
UPDATE channel_paid_reaction_commands
SET completed=true,payer_transaction_id=$3,channel_transaction_id=$4,
    payer_balance_after=$5,channel_balance_after=$6,reactor_stars_after=$7,total_stars_after=$8,
    result_snapshot=$9
WHERE payer_user_id=$1 AND random_id=$2 AND NOT completed`, req.UserID, req.RandomID,
			payerTxnID, channelTxnID, balance, channelBalance, reactorStars, totalStars, resultSnapshot)
		if err != nil {
			return domain.ChannelMessagePaidReactionResult{}, fmt.Errorf("complete paid reaction receipt: %w", err)
		}
		if commandTag.RowsAffected() != 1 {
			return domain.ChannelMessagePaidReactionResult{}, fmt.Errorf("complete paid reaction receipt: command changed")
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.ChannelMessagePaidReactionResult{}, fmt.Errorf("commit add channel paid reaction: %w", err)
	}
	committed = true
	recipients := []int64{req.UserID}
	recipients, err = s.ListActiveChannelMemberIDs(ctx, req.UserID, req.ChannelID, domain.MaxChannelRealtimeFanout)
	if err != nil || len(recipients) == 0 {
		recipients = []int64{req.UserID}
	}
	result.Recipients = recipients
	return result, nil
}

func paidReactionDisplayChannelIDs(paid domain.ChannelMessagePaidReactions) []int64 {
	seen := make(map[int64]struct{}, len(paid.TopReactors))
	for _, reactor := range paid.TopReactors {
		peer := reactor.DisplayPeer()
		if peer.Type == domain.PeerTypeChannel && peer.ID > 0 {
			seen[peer.ID] = struct{}{}
		}
	}
	ids := make([]int64, 0, len(seen))
	for id := range seen {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

type paidReactionResultSnapshot struct {
	Version         int                                `json:"version"`
	Channel         domain.Channel                     `json:"channel"`
	Message         domain.ChannelMessage              `json:"message"`
	Paid            domain.ChannelMessagePaidReactions `json:"paid"`
	PayerBalance    domain.StarsBalance                `json:"payer_balance"`
	ChannelBalance  int64                              `json:"channel_balance"`
	DisplayChannels []domain.Channel                   `json:"display_channels,omitempty"`
}

func encodePaidReactionResultSnapshot(result domain.ChannelMessagePaidReactionResult) ([]byte, error) {
	return json.Marshal(paidReactionResultSnapshot{
		Version:         1,
		Channel:         result.Channel,
		Message:         result.Message,
		Paid:            result.Paid,
		PayerBalance:    result.PayerBalance,
		ChannelBalance:  result.ChannelBalance,
		DisplayChannels: result.DisplayChannels,
	})
}

func decodePaidReactionResultSnapshot(raw []byte) (domain.ChannelMessagePaidReactionResult, error) {
	var snapshot paidReactionResultSnapshot
	if len(raw) == 0 {
		return domain.ChannelMessagePaidReactionResult{}, fmt.Errorf("empty snapshot")
	}
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		return domain.ChannelMessagePaidReactionResult{}, err
	}
	if snapshot.Version != 1 || snapshot.Channel.ID <= 0 || snapshot.Message.ID <= 0 ||
		snapshot.PayerBalance.UserID <= 0 || snapshot.PayerBalance.Balance < 0 ||
		snapshot.ChannelBalance < 0 || snapshot.Paid.TotalStars <= 0 {
		return domain.ChannelMessagePaidReactionResult{}, fmt.Errorf("invalid snapshot")
	}
	return domain.ChannelMessagePaidReactionResult{
		Channel:         snapshot.Channel,
		Message:         snapshot.Message,
		Paid:            snapshot.Paid,
		PayerBalance:    snapshot.PayerBalance,
		ChannelBalance:  snapshot.ChannelBalance,
		DisplayChannels: snapshot.DisplayChannels,
	}, nil
}

// ReplayChannelMessagePaidReaction returns an immutable completed receipt
// without consulting current channel/message/member/send-as state. RPC uses
// this before access checks so a lost successful response remains replayable.
func (s *ChannelStore) ReplayChannelMessagePaidReaction(ctx context.Context, req domain.SendChannelPaidReactionRequest) (domain.ChannelMessagePaidReactionResult, bool, error) {
	if req.UserID == 0 || req.RandomID == 0 {
		return domain.ChannelMessagePaidReactionResult{}, false, domain.ErrChannelInvalid
	}
	fingerprint := req.Fingerprint()
	var storedFingerprint, resultSnapshot []byte
	err := s.db.QueryRow(ctx, `
SELECT request_fingerprint,result_snapshot
FROM channel_paid_reaction_commands
WHERE payer_user_id=$1 AND random_id=$2 AND completed
  AND created_at >= now() - ($3::bigint * interval '1 second')`,
		req.UserID, req.RandomID, domain.PaidReactionReceiptRetentionSeconds).
		Scan(&storedFingerprint, &resultSnapshot)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ChannelMessagePaidReactionResult{}, false, nil
	}
	if err != nil {
		return domain.ChannelMessagePaidReactionResult{}, false, fmt.Errorf("load paid reaction replay: %w", err)
	}
	if !bytes.Equal(storedFingerprint, fingerprint[:]) {
		return domain.ChannelMessagePaidReactionResult{}, true, domain.ErrMessageRandomIDDuplicate
	}
	result, err := decodePaidReactionResultSnapshot(resultSnapshot)
	if err != nil {
		return domain.ChannelMessagePaidReactionResult{}, true, fmt.Errorf("decode paid reaction replay: %w", err)
	}
	if err := s.db.QueryRow(ctx, `SELECT balance,granted FROM stars_balances WHERE user_id=$1`, req.UserID).
		Scan(&result.PayerBalance.Balance, &result.PayerBalance.Granted); err != nil {
		return domain.ChannelMessagePaidReactionResult{}, true, fmt.Errorf("load current paid reaction replay balance: %w", err)
	}
	result.PayerBalance.UserID = req.UserID
	result.Duplicate = true
	result.Recipients = []int64{req.UserID}
	return result, true, nil
}

// aggregateChannelPaidReactions aggregates the paid reactions of one message: total stars + the viewer's own + top reactors.
func (s *ChannelStore) aggregateChannelPaidReactions(ctx context.Context, channelID int64, messageID int, viewerUserID int64) (domain.ChannelMessagePaidReactions, error) {
	return aggregateChannelPaidReactionsDB(ctx, s.db, channelID, messageID, viewerUserID)
}

func aggregateChannelPaidReactionsDB(ctx context.Context, db sqlcgen.DBTX, channelID int64, messageID int, viewerUserID int64) (domain.ChannelMessagePaidReactions, error) {
	rows, err := db.Query(ctx, `
SELECT reactor_user_id, display_peer_type, display_peer_id, stars, anonymous
FROM channel_message_paid_reactions
WHERE channel_id = $1 AND message_id = $2
ORDER BY stars DESC, reactor_user_id ASC`, channelID, messageID)
	if err != nil {
		return domain.ChannelMessagePaidReactions{}, fmt.Errorf("aggregate channel paid reactions: %w", err)
	}
	defer rows.Close()
	var out domain.ChannelMessagePaidReactions
	var myReactor domain.PaidReactor
	myInTop := false
	for rows.Next() {
		var r domain.PaidReactor
		if err := rows.Scan(&r.UserID, &r.Peer.Type, &r.Peer.ID, &r.Stars, &r.Anonymous); err != nil {
			return domain.ChannelMessagePaidReactions{}, err
		}
		out.TotalStars += r.Stars
		r.My = r.UserID == viewerUserID
		if r.My {
			out.MyStars = r.Stars
			out.MyAnonymous = r.Anonymous
			myReactor = r
		}
		if len(out.TopReactors) < domain.MaxPaidReactionTopReactors {
			out.TopReactors = append(out.TopReactors, r)
			if r.My {
				myInTop = true
			}
		}
	}
	if err := rows.Err(); err != nil {
		return domain.ChannelMessagePaidReactions{}, err
	}
	// The viewer itself always appears in top reactors (official: your entry is always in
	// the list, flagged My).
	if out.MyStars > 0 && !myInTop {
		out.TopReactors = append(out.TopReactors, myReactor)
	}
	return out, nil
}

func (s *ChannelStore) DeleteChannelParticipantReaction(ctx context.Context, req domain.DeleteChannelParticipantReactionRequest) (domain.ChannelMessageReactionsResult, error) {
	if req.UserID == 0 || req.ChannelID == 0 || req.MessageID <= 0 || req.MessageID > domain.MaxMessageBoxID || req.ParticipantUserID == 0 {
		return domain.ChannelMessageReactionsResult{}, domain.ErrChannelInvalid
	}
	beginner, ok := s.db.(txBeginner)
	if !ok {
		return domain.ChannelMessageReactionsResult{}, fmt.Errorf("delete channel participant reaction: db does not support transactions")
	}
	tx, err := beginner.Begin(ctx)
	if err != nil {
		return domain.ChannelMessageReactionsResult{}, fmt.Errorf("begin delete channel participant reaction: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()
	channel, member, err := s.getChannelForMember(ctx, tx, req.UserID, req.ChannelID)
	if err != nil {
		return domain.ChannelMessageReactionsResult{}, err
	}
	if !canDeleteAnyChannelMessage(member) {
		return domain.ChannelMessageReactionsResult{}, domain.ErrChannelAdminRequired
	}
	msg, err := s.getChannelMessage(ctx, tx, req.ChannelID, req.MessageID)
	if err != nil {
		return domain.ChannelMessageReactionsResult{}, err
	}
	if msg.Deleted || msg.ID <= member.AvailableMinID {
		return domain.ChannelMessageReactionsResult{}, domain.ErrMessageIDInvalid
	}
	if _, err := tx.Exec(ctx, `
DELETE FROM channel_message_reactions
WHERE channel_id = $1 AND message_id = $2 AND reacted_user_id = $3`,
		req.ChannelID, req.MessageID, req.ParticipantUserID); err != nil {
		return domain.ChannelMessageReactionsResult{}, fmt.Errorf("delete participant reaction: %w", err)
	}
	if err := refreshChannelUnreadReactionsCountTx(ctx, tx, msg.SenderUserID, req.ChannelID); err != nil {
		return domain.ChannelMessageReactionsResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.ChannelMessageReactionsResult{}, fmt.Errorf("commit delete participant reaction: %w", err)
	}
	committed = true
	messages := []domain.ChannelMessage{msg}
	if err := s.populateChannelMessagesReactions(ctx, s.db, req.UserID, []domain.Channel{channel}, messages); err != nil {
		return domain.ChannelMessageReactionsResult{}, err
	}
	msg = messages[0]
	reactions := emptyChannelMessageReactions(channel)
	if msg.Reactions != nil {
		reactions = *msg.Reactions
	} else {
		msg.Reactions = &reactions
	}
	recipients, err := s.ListActiveChannelMemberIDs(ctx, req.UserID, req.ChannelID, domain.MaxChannelRealtimeFanout)
	if err != nil {
		recipients = []int64{req.UserID}
	}
	return domain.ChannelMessageReactionsResult{
		Channel:    channel,
		Message:    msg,
		Messages:   []domain.ChannelMessage{msg},
		Reactions:  reactions,
		Recipients: recipients,
	}, nil
}

func (s *ChannelStore) DeleteChannelParticipantReactions(ctx context.Context, req domain.DeleteChannelParticipantReactionsRequest) (domain.DeleteChannelParticipantReactionsResult, error) {
	if req.UserID == 0 || req.ChannelID == 0 || req.ParticipantUserID == 0 {
		return domain.DeleteChannelParticipantReactionsResult{}, domain.ErrChannelInvalid
	}
	if req.Limit <= 0 || req.Limit > domain.MaxDeleteParticipantReactionsBatch {
		req.Limit = domain.MaxDeleteParticipantReactionsBatch
	}
	beginner, ok := s.db.(txBeginner)
	if !ok {
		return domain.DeleteChannelParticipantReactionsResult{}, fmt.Errorf("delete channel participant reactions: db does not support transactions")
	}
	tx, err := beginner.Begin(ctx)
	if err != nil {
		return domain.DeleteChannelParticipantReactionsResult{}, fmt.Errorf("begin delete channel participant reactions: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()
	channel, member, err := s.getChannelForMember(ctx, tx, req.UserID, req.ChannelID)
	if err != nil {
		return domain.DeleteChannelParticipantReactionsResult{}, err
	}
	if !canDeleteAnyChannelMessage(member) {
		return domain.DeleteChannelParticipantReactionsResult{}, domain.ErrChannelAdminRequired
	}
	rows, err := tx.Query(ctx, `
SELECT message_id, MAX(sender_user_id)
FROM channel_message_reactions
WHERE channel_id = $1 AND reacted_user_id = $2
GROUP BY message_id
ORDER BY MAX(reaction_date) DESC, message_id DESC
LIMIT $3`, req.ChannelID, req.ParticipantUserID, req.Limit)
	if err != nil {
		return domain.DeleteChannelParticipantReactionsResult{}, fmt.Errorf("list participant reaction messages: %w", err)
	}
	ids := make([]int, 0, req.Limit)
	owners := make(map[int64]struct{})
	for rows.Next() {
		var msgID int
		var senderUserID int64
		if err := rows.Scan(&msgID, &senderUserID); err != nil {
			rows.Close()
			return domain.DeleteChannelParticipantReactionsResult{}, err
		}
		ids = append(ids, msgID)
		if senderUserID != 0 {
			owners[senderUserID] = struct{}{}
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return domain.DeleteChannelParticipantReactionsResult{}, err
	}
	rows.Close()
	if len(ids) > 0 {
		if _, err := tx.Exec(ctx, `
DELETE FROM channel_message_reactions
WHERE channel_id = $1 AND reacted_user_id = $2 AND message_id = ANY($3::int[])`,
			req.ChannelID, req.ParticipantUserID, int32s(ids)); err != nil {
			return domain.DeleteChannelParticipantReactionsResult{}, fmt.Errorf("delete participant reactions: %w", err)
		}
		for ownerID := range owners {
			if err := refreshChannelUnreadReactionsCountTx(ctx, tx, ownerID, req.ChannelID); err != nil {
				return domain.DeleteChannelParticipantReactionsResult{}, err
			}
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.DeleteChannelParticipantReactionsResult{}, fmt.Errorf("commit delete participant reactions: %w", err)
	}
	committed = true
	messages := []domain.ChannelMessage{}
	if len(ids) > 0 {
		res, err := s.GetChannelMessageReactions(ctx, domain.ChannelMessageReactionsRequest{
			UserID:    req.UserID,
			ChannelID: req.ChannelID,
			IDs:       ids,
		})
		if err != nil {
			return domain.DeleteChannelParticipantReactionsResult{}, err
		}
		messages = res.Messages
	}
	recipients, err := s.ListActiveChannelMemberIDs(ctx, req.UserID, req.ChannelID, domain.MaxChannelRealtimeFanout)
	if err != nil {
		recipients = []int64{req.UserID}
	}
	return domain.DeleteChannelParticipantReactionsResult{
		Channel:    channel,
		Messages:   messages,
		Recipients: recipients,
		Deleted:    len(ids),
	}, nil
}

func (s *ChannelStore) GetChannelMessageReactions(ctx context.Context, req domain.ChannelMessageReactionsRequest) (domain.ChannelMessageReactionsResult, error) {
	if req.UserID == 0 || req.ChannelID == 0 {
		return domain.ChannelMessageReactionsResult{}, domain.ErrChannelInvalid
	}
	if len(req.IDs) > domain.MaxGetMessageIDs {
		return domain.ChannelMessageReactionsResult{}, domain.ErrChannelInvalid
	}
	channel, member, _, err := s.getChannelForViewer(ctx, s.db, req.UserID, req.ChannelID)
	if err != nil {
		return domain.ChannelMessageReactionsResult{}, err
	}
	if len(req.IDs) == 0 {
		return domain.ChannelMessageReactionsResult{Channel: channel}, nil
	}
	id32, _, err := validUniqueChannelMessageIDs(req.IDs)
	if err != nil {
		return domain.ChannelMessageReactionsResult{}, err
	}
	args := []any{req.ChannelID, id32}
	where := "channel_id = $1 AND id = ANY($2::int[]) AND NOT deleted"
	if member.AvailableMinID > 0 {
		args = append(args, member.AvailableMinID)
		where += fmt.Sprintf(" AND id > $%d", len(args))
	}
	if channel.Monoforum && !member.CanManageDirectMessages() {
		args = append(args, string(domain.PeerTypeUser), req.UserID)
		where += fmt.Sprintf(" AND saved_peer_type = $%d AND saved_peer_id = $%d", len(args)-1, len(args))
	}
	rows, err := s.db.Query(ctx, `
SELECT `+channelMessageColumns+`
FROM channel_messages
WHERE `+where+`
ORDER BY id DESC`, args...)
	if err != nil {
		return domain.ChannelMessageReactionsResult{}, fmt.Errorf("get channel message reactions messages: %w", err)
	}
	defer rows.Close()
	messages := make([]domain.ChannelMessage, 0, len(req.IDs))
	for rows.Next() {
		msg, err := scanChannelMessage(rows)
		if err != nil {
			return domain.ChannelMessageReactionsResult{}, err
		}
		messages = append(messages, msg)
	}
	if err := rows.Err(); err != nil {
		return domain.ChannelMessageReactionsResult{}, err
	}
	if err := s.populateChannelMessagesReactions(ctx, s.db, req.UserID, []domain.Channel{channel}, messages); err != nil {
		return domain.ChannelMessageReactionsResult{}, err
	}
	res := domain.ChannelMessageReactionsResult{Channel: channel, Messages: messages}
	if len(messages) == 1 {
		res.Message = messages[0]
		res.Reactions = emptyChannelMessageReactions(channel)
		if messages[0].Reactions != nil {
			res.Reactions = *messages[0].Reactions
		}
	}
	return res, nil
}

func (s *ChannelStore) ListChannelMessageReactions(ctx context.Context, req domain.ChannelMessageReactionsListRequest) (domain.ChannelMessageReactionsList, error) {
	if req.UserID == 0 || req.ChannelID == 0 || req.MessageID <= 0 || req.MessageID > domain.MaxMessageBoxID {
		return domain.ChannelMessageReactionsList{}, domain.ErrChannelInvalid
	}
	if req.Limit <= 0 || req.Limit > domain.MaxChannelMessageReactionListLimit {
		req.Limit = domain.MaxChannelMessageReactionListLimit
	}
	channel, member, _, err := s.getChannelForViewer(ctx, s.db, req.UserID, req.ChannelID)
	if err != nil {
		return domain.ChannelMessageReactionsList{}, err
	}
	if channel.Broadcast && !channel.Megagroup {
		return domain.ChannelMessageReactionsList{}, domain.ErrChannelRightForbidden
	}
	msg, err := s.getChannelMessage(ctx, s.db, req.ChannelID, req.MessageID)
	if err != nil {
		return domain.ChannelMessageReactionsList{}, err
	}
	if msg.Deleted || msg.ID <= member.AvailableMinID ||
		!channelMessageVisibleToViewer(channel, member, req.UserID, msg) {
		return domain.ChannelMessageReactionsList{}, domain.ErrMessageIDInvalid
	}
	baseWhere := []string{"channel_id = $1", "message_id = $2"}
	baseArgs := []any{req.ChannelID, req.MessageID}
	if req.Reaction != nil {
		if !req.Reaction.Valid() {
			return domain.ChannelMessageReactionsList{}, domain.ErrChannelInvalid
		}
		baseArgs = append(baseArgs, string(req.Reaction.Type), req.Reaction.Value())
		baseWhere = append(baseWhere, fmt.Sprintf("reaction_type = $%d AND reaction_value = $%d", len(baseArgs)-1, len(baseArgs)))
	}
	var count int
	if err := s.db.QueryRow(ctx, `SELECT COUNT(*)::int FROM channel_message_reactions WHERE `+strings.Join(baseWhere, " AND "), baseArgs...).Scan(&count); err != nil {
		return domain.ChannelMessageReactionsList{}, fmt.Errorf("count channel message reactions: %w", err)
	}
	where := append([]string(nil), baseWhere...)
	args := append([]any(nil), baseArgs...)
	if req.Offset != "" {
		cursor, ok := parseChannelReactionOffset(req.Offset)
		if !ok {
			return domain.ChannelMessageReactionsList{}, domain.ErrChannelInvalid
		}
		if cursor.legacyValue {
			args = append(args, cursor.date, cursor.userID, cursor.value)
			n := len(args)
			where = append(where, fmt.Sprintf("(reaction_date < $%d OR (reaction_date = $%d AND (reacted_user_id < $%d OR (reacted_user_id = $%d AND reaction_value > $%d))))", n-2, n-2, n-1, n-1, n))
		} else {
			args = append(args, cursor.date, cursor.userID, string(cursor.reactionType), cursor.value)
			n := len(args)
			where = append(where, fmt.Sprintf("(reaction_date < $%d OR (reaction_date = $%d AND (reacted_user_id < $%d OR (reacted_user_id = $%d AND (reaction_type > $%d OR (reaction_type = $%d AND reaction_value > $%d))))))", n-3, n-3, n-2, n-2, n-1, n-1, n))
		}
	}
	args = append(args, req.Limit+1)
	rows, err := s.db.Query(ctx, `
SELECT channel_id, message_id, reacted_user_id, sender_user_id, reaction_type, reaction_value,
       big, unread, chosen_order, reaction_date
FROM channel_message_reactions
WHERE `+strings.Join(where, " AND ")+`
ORDER BY reaction_date DESC, reacted_user_id DESC, reaction_type ASC, reaction_value ASC
LIMIT $`+fmt.Sprint(len(args)), args...)
	if err != nil {
		return domain.ChannelMessageReactionsList{}, fmt.Errorf("list channel message reactions: %w", err)
	}
	defer rows.Close()
	reactions := make([]domain.ChannelMessagePeerReaction, 0, req.Limit+1)
	for rows.Next() {
		row, err := scanChannelMessagePeerReaction(rows, req.UserID)
		if err != nil {
			return domain.ChannelMessageReactionsList{}, err
		}
		reactions = append(reactions, row)
	}
	if err := rows.Err(); err != nil {
		return domain.ChannelMessageReactionsList{}, err
	}
	next := ""
	if len(reactions) > req.Limit {
		reactions = reactions[:req.Limit]
		next = channelReactionOffset(reactions[len(reactions)-1])
	}
	return domain.ChannelMessageReactionsList{
		Channel:    channel,
		Message:    msg,
		Count:      count,
		Reactions:  reactions,
		NextOffset: next,
	}, nil
}

func (s *ChannelStore) FindChannelMessageReaction(ctx context.Context, req domain.ChannelMessageReactionLookupRequest) (domain.ChannelMessageReactionLookup, bool, error) {
	if req.ViewerUserID == 0 || req.ChannelID == 0 || req.MessageID <= 0 ||
		req.MessageID > domain.MaxMessageBoxID || req.ReactorUserID == 0 {
		return domain.ChannelMessageReactionLookup{}, false, domain.ErrChannelInvalid
	}
	channel, member, _, err := s.getChannelForViewer(ctx, s.db, req.ViewerUserID, req.ChannelID)
	if err != nil {
		return domain.ChannelMessageReactionLookup{}, false, err
	}
	message, err := s.getChannelMessage(ctx, s.db, req.ChannelID, req.MessageID)
	if err != nil {
		return domain.ChannelMessageReactionLookup{}, false, err
	}
	if message.Deleted || message.ID <= member.AvailableMinID ||
		!channelMessageVisibleToViewer(channel, member, req.ViewerUserID, message) {
		return domain.ChannelMessageReactionLookup{}, false, domain.ErrMessageIDInvalid
	}
	rows, err := s.db.Query(ctx, `
SELECT channel_id, message_id, reacted_user_id, sender_user_id,
       reaction_type, reaction_value, big, unread, chosen_order, reaction_date
FROM channel_message_reactions
WHERE channel_id = $1 AND message_id = $2 AND reacted_user_id = $3
ORDER BY chosen_order, reaction_type, reaction_value
LIMIT $4`,
		req.ChannelID, req.MessageID, req.ReactorUserID,
		domain.MaxChannelMessageReactionsPerUser)
	if err != nil {
		return domain.ChannelMessageReactionLookup{}, false, fmt.Errorf("find channel message reaction: %w", err)
	}
	defer rows.Close()
	reactions := make([]domain.ChannelMessagePeerReaction, 0, domain.MaxChannelMessageReactionsPerUser)
	for rows.Next() {
		reaction, err := scanChannelMessagePeerReaction(rows, req.ViewerUserID)
		if err != nil {
			return domain.ChannelMessageReactionLookup{}, false, err
		}
		reactions = append(reactions, reaction)
	}
	if err := rows.Err(); err != nil {
		return domain.ChannelMessageReactionLookup{}, false, err
	}
	return domain.ChannelMessageReactionLookup{
		Channel: channel, Message: message, Reactions: reactions,
	}, len(reactions) > 0, nil
}
