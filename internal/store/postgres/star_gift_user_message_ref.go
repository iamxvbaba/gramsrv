package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"telesrv/internal/domain"
)

// registerUserStarGiftMessageRef records an owner-scoped service-message alias
// for a user-owned gift. Official clients may continue from a freshly emitted
// messageActionStarGiftUnique or a separate prepaid-upgrade notification and
// pass that message id to a lifecycle RPC, while payments.getSavedStarGifts may
// still expose the original received gift message as the aggregate's primary
// msg_id. expectedUniqueGiftID is zero for an ordinary gift and positive for a
// unique gift; the write boundary never aliases across lifecycle states.
func registerUserStarGiftMessageRef(
	ctx context.Context,
	tx pgx.Tx,
	ownerUserID int64,
	msgID int,
	savedGiftID int64,
	uniqueGiftID int64,
) error {
	return registerViewerStarGiftMessageRef(ctx, tx, ownerUserID, msgID, savedGiftID,
		domain.Peer{Type: domain.PeerTypeUser, ID: ownerUserID}, uniqueGiftID)
}

// registerViewerStarGiftMessageRef binds one viewer-local private message to
// the aggregate owner explicitly named by the action. The alias does not grant
// ownership: RPC callers resolve the real owner and authorize it again.
func registerViewerStarGiftMessageRef(
	ctx context.Context,
	tx pgx.Tx,
	viewerUserID int64,
	msgID int,
	savedGiftID int64,
	expectedOwner domain.Peer,
	uniqueGiftID int64,
) error {
	if viewerUserID <= 0 || msgID <= 0 || savedGiftID <= 0 || !validLifecyclePeer(expectedOwner) || uniqueGiftID < 0 {
		return fmt.Errorf("register star gift message ref: invalid identity")
	}
	tag, err := tx.Exec(ctx, `
INSERT INTO star_gift_user_message_refs(owner_user_id,msg_id,saved_gift_id)
SELECT $1,$2,p.id
FROM peer_star_gifts p
WHERE p.id=$3 AND p.owner_peer_type=$4 AND p.owner_peer_id=$5
  AND (($6::bigint=0 AND p.unique_gift_id IS NULL) OR ($6::bigint>0 AND p.unique_gift_id=$6::bigint))
  AND p.lifecycle_status='active'
ON CONFLICT(owner_user_id,msg_id) DO UPDATE
SET saved_gift_id=EXCLUDED.saved_gift_id
WHERE star_gift_user_message_refs.saved_gift_id=EXCLUDED.saved_gift_id`,
		viewerUserID, msgID, savedGiftID, string(expectedOwner.Type), expectedOwner.ID, uniqueGiftID)
	if err != nil {
		return fmt.Errorf("register star gift message ref: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("register star gift message ref: identity collision")
	}
	return nil
}

func registerChannelNotificationMessageRef(
	ctx context.Context,
	tx pgx.Tx,
	viewerUserID int64,
	msgID int,
	savedGiftID int64,
) error {
	if viewerUserID <= 0 || msgID <= 0 || savedGiftID <= 0 {
		return fmt.Errorf("register channel notification star gift message ref: invalid identity")
	}
	tag, err := tx.Exec(ctx, `
INSERT INTO star_gift_user_message_refs(owner_user_id,msg_id,saved_gift_id)
SELECT $1,$2,gift.id
FROM star_gift_channel_notification_jobs job
JOIN peer_star_gifts gift ON gift.id=job.saved_gift_id
WHERE job.saved_gift_id=$3 AND job.target_user_id=$1
  AND gift.owner_peer_type='channel' AND gift.lifecycle_status='active'
ON CONFLICT(owner_user_id,msg_id) DO UPDATE
SET saved_gift_id=EXCLUDED.saved_gift_id
WHERE star_gift_user_message_refs.saved_gift_id=EXCLUDED.saved_gift_id`,
		viewerUserID, msgID, savedGiftID)
	if err != nil {
		return fmt.Errorf("register channel notification star gift message ref: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("register channel notification star gift message ref: identity collision")
	}
	return nil
}

func userStarGiftMessageRefMatches(
	ctx context.Context,
	db interface {
		QueryRow(context.Context, string, ...any) pgx.Row
	},
	ownerUserID int64,
	msgID int,
	savedGiftID int64,
) (bool, error) {
	var matches bool
	err := db.QueryRow(ctx, `SELECT EXISTS (
SELECT 1 FROM star_gift_user_message_refs
WHERE owner_user_id=$1 AND msg_id=$2 AND saved_gift_id=$3
)`, ownerUserID, msgID, savedGiftID).Scan(&matches)
	return matches, err
}
