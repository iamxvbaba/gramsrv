package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
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
	if ownerUserID <= 0 || msgID <= 0 || savedGiftID <= 0 || uniqueGiftID < 0 {
		return fmt.Errorf("register user star gift message ref: invalid identity")
	}
	tag, err := tx.Exec(ctx, `
INSERT INTO star_gift_user_message_refs(owner_user_id,msg_id,saved_gift_id)
SELECT $1,$2,p.id
FROM peer_star_gifts p
WHERE p.id=$3 AND p.owner_peer_type='user' AND p.owner_peer_id=$1
  AND (($4::bigint=0 AND p.unique_gift_id IS NULL) OR ($4::bigint>0 AND p.unique_gift_id=$4::bigint))
  AND p.lifecycle_status='active'
ON CONFLICT(owner_user_id,msg_id) DO UPDATE
SET saved_gift_id=EXCLUDED.saved_gift_id
WHERE star_gift_user_message_refs.saved_gift_id=EXCLUDED.saved_gift_id`, ownerUserID, msgID, savedGiftID, uniqueGiftID)
	if err != nil {
		return fmt.Errorf("register user star gift message ref: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("register user star gift message ref: identity collision")
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
