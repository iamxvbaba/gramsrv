package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgerrcode"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"telesrv/internal/domain"
	"telesrv/internal/store/postgres/sqlcgen"
)

const (
	peerUsernameTypeUser    = "user"
	peerUsernameTypeChannel = "channel"
)

// peerUsernameColumns is the registry projection shared by every reader. The
// collectible id is coalesced so domain.Username keeps a plain int64 zero for
// the editable slot.
const peerUsernameColumns = `username, active, editable, sort_order, COALESCE(collectible_id, 0)`

type peerUsernameOwner struct {
	peerType string
	peerID   int64
	// collectible marks a row backed by a collectible asset. Such a row is never
	// editable, so the client-driven username path must not reuse or delete it.
	collectible bool
	// active mirrors the registry flag. An inactive name stays occupied for
	// uniqueness purposes but must not resolve to its holder.
	active bool
}

func (o peerUsernameOwner) matches(peerType string, peerID int64) bool {
	return o.peerType == peerType && o.peerID == peerID
}

// getPeerUsernameOwner resolves the holder of a name across the whole registry:
// username uniqueness is global and covers collectible rows as well as editable
// ones, so occupancy checks and username resolution keep a single source of
// truth.
func getPeerUsernameOwner(ctx context.Context, db sqlcgen.DBTX, usernameLower string, forUpdate bool) (peerUsernameOwner, bool, error) {
	if usernameLower == "" {
		return peerUsernameOwner{}, false, nil
	}
	query := `SELECT peer_type, peer_id, collectible_id IS NOT NULL, active FROM peer_usernames WHERE username_lower = $1`
	if forUpdate {
		query += ` FOR UPDATE`
	}
	var owner peerUsernameOwner
	err := db.QueryRow(ctx, query, usernameLower).Scan(&owner.peerType, &owner.peerID, &owner.collectible, &owner.active)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return peerUsernameOwner{}, false, nil
		}
		return peerUsernameOwner{}, false, fmt.Errorf("get peer username owner: %w", err)
	}
	return owner, true, nil
}

func peerUsernameAvailable(ctx context.Context, db sqlcgen.DBTX, usernameLower, peerType string, peerID int64) (bool, error) {
	owner, found, err := getPeerUsernameOwner(ctx, db, usernameLower, false)
	if err != nil || !found {
		return !found, err
	}
	return owner.matches(peerType, peerID), nil
}

// replacePeerUsernameTx rewrites the peer's editable username slot. username is
// the display form (original case) and usernameLower its registry key; an empty
// pair clears the slot.
//
// Only the editable row is replaced. Collectible rows belong to assets in
// collectible_usernames and must survive every client-driven username edit,
// otherwise account.updateUsername would silently release a minted asset.
func replacePeerUsernameTx(ctx context.Context, tx pgx.Tx, peerType string, peerID int64, username, usernameLower string) error {
	if usernameLower != "" {
		owner, found, err := getPeerUsernameOwner(ctx, tx, usernameLower, true)
		if err != nil {
			return err
		}
		// A collectible row occupies the name even for its own holder: the
		// editable slot cannot duplicate a name the peer already holds as an asset.
		if found && (!owner.matches(peerType, peerID) || owner.collectible) {
			return domain.ErrUsernameOccupied
		}
	}
	if _, err := tx.Exec(ctx, `
DELETE FROM peer_usernames WHERE peer_type = $1 AND peer_id = $2 AND editable`, peerType, peerID); err != nil {
		return fmt.Errorf("delete peer username: %w", err)
	}
	if usernameLower == "" {
		return nil
	}
	if _, err := tx.Exec(ctx, `
INSERT INTO peer_usernames (username_lower, peer_type, peer_id, username, active, editable, sort_order, collectible_id)
VALUES ($1, $2, $3, $4, true, true, 0, NULL)`, usernameLower, peerType, peerID, username); err != nil {
		if isUniqueViolation(err) {
			return domain.ErrUsernameOccupied
		}
		return fmt.Errorf("insert peer username: %w", err)
	}
	return nil
}

// deletePeerUsernameTx clears the peer's editable slot only. Collectible rows are
// released through the asset lifecycle (revoke/burn) or by the peer-deletion
// trigger, never by an editable-slot edit.
func deletePeerUsernameTx(ctx context.Context, tx pgx.Tx, peerType string, peerID int64) error {
	if _, err := tx.Exec(ctx, `
DELETE FROM peer_usernames WHERE peer_type = $1 AND peer_id = $2 AND editable`, peerType, peerID); err != nil {
		return fmt.Errorf("delete peer username: %w", err)
	}
	return nil
}

// listPeerUsernames returns the peer's registry rows in projection order:
// editable slot first, then collectibles by stored sort order.
func listPeerUsernames(ctx context.Context, db sqlcgen.DBTX, peer domain.Peer) ([]domain.Username, error) {
	if peer.Type == "" || peer.ID <= 0 {
		return nil, nil
	}
	rows, err := db.Query(ctx, `
SELECT `+peerUsernameColumns+`
FROM peer_usernames
WHERE peer_type = $1 AND peer_id = $2
ORDER BY editable DESC, sort_order, username_lower`, string(peer.Type), peer.ID)
	if err != nil {
		return nil, fmt.Errorf("list peer usernames: %w", err)
	}
	defer rows.Close()
	out := make([]domain.Username, 0, 4)
	for rows.Next() {
		var item domain.Username
		if err := rows.Scan(&item.Username, &item.Active, &item.Editable, &item.SortOrder, &item.CollectibleID); err != nil {
			return nil, fmt.Errorf("scan peer username: %w", err)
		}
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate peer usernames: %w", err)
	}
	return domain.SortUsernames(out), nil
}

// listPeerUsernamesBatch resolves several peers in one round trip.
func listPeerUsernamesBatch(ctx context.Context, db sqlcgen.DBTX, peers []domain.Peer) (map[domain.Peer][]domain.Username, error) {
	out := make(map[domain.Peer][]domain.Username, len(peers))
	types := make([]string, 0, len(peers))
	ids := make([]int64, 0, len(peers))
	seen := make(map[domain.Peer]struct{}, len(peers))
	for _, peer := range peers {
		if peer.Type == "" || peer.ID <= 0 {
			continue
		}
		if _, dup := seen[peer]; dup {
			continue
		}
		seen[peer] = struct{}{}
		types = append(types, string(peer.Type))
		ids = append(ids, peer.ID)
	}
	if len(types) == 0 {
		return out, nil
	}
	rows, err := db.Query(ctx, `
SELECT peer_type, peer_id, `+peerUsernameColumns+`
FROM peer_usernames
WHERE (peer_type, peer_id) IN (SELECT t, i FROM unnest($1::text[], $2::bigint[]) AS s(t, i))
ORDER BY peer_type, peer_id, editable DESC, sort_order, username_lower`, types, ids)
	if err != nil {
		return nil, fmt.Errorf("list peer usernames batch: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var peer domain.Peer
		var peerType string
		var item domain.Username
		if err := rows.Scan(&peerType, &peer.ID, &item.Username, &item.Active, &item.Editable,
			&item.SortOrder, &item.CollectibleID); err != nil {
			return nil, fmt.Errorf("scan peer username batch: %w", err)
		}
		peer.Type = domain.PeerType(peerType)
		out[peer] = append(out[peer], item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate peer usernames batch: %w", err)
	}
	for peer, list := range out {
		out[peer] = domain.SortUsernames(list)
	}
	return out, nil
}

// lockPeerUsernamesTx reads the peer's registry rows with row locks so a toggle
// or reorder validated against the list cannot race a concurrent asset move.
func lockPeerUsernamesTx(ctx context.Context, tx pgx.Tx, peer domain.Peer) ([]domain.Username, error) {
	rows, err := tx.Query(ctx, `
SELECT `+peerUsernameColumns+`
FROM peer_usernames
WHERE peer_type = $1 AND peer_id = $2
ORDER BY username_lower
FOR UPDATE`, string(peer.Type), peer.ID)
	if err != nil {
		return nil, fmt.Errorf("lock peer usernames: %w", err)
	}
	defer rows.Close()
	out := make([]domain.Username, 0, 4)
	for rows.Next() {
		var item domain.Username
		if err := rows.Scan(&item.Username, &item.Active, &item.Editable, &item.SortOrder, &item.CollectibleID); err != nil {
			return nil, fmt.Errorf("scan locked peer username: %w", err)
		}
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate locked peer usernames: %w", err)
	}
	return domain.SortUsernames(out), nil
}

// countPeerCollectibleUsernamesTx bounds the collectible rows a peer may hold.
func countPeerCollectibleUsernamesTx(ctx context.Context, tx pgx.Tx, peerType string, peerID int64) (int, error) {
	var count int
	if err := tx.QueryRow(ctx, `
SELECT count(*) FROM peer_usernames
WHERE peer_type = $1 AND peer_id = $2 AND collectible_id IS NOT NULL`, peerType, peerID).Scan(&count); err != nil {
		return 0, fmt.Errorf("count peer collectible usernames: %w", err)
	}
	return count, nil
}

// insertCollectiblePeerUsernameTx projects an owned asset into the registry. The
// row sorts after every collectible the peer already holds and always carries
// collectible_id, which the schema forbids on an editable row.
func insertCollectiblePeerUsernameTx(ctx context.Context, tx pgx.Tx, peerType string, peerID int64, username, usernameLower string, collectibleID int64) error {
	if _, err := tx.Exec(ctx, `
INSERT INTO peer_usernames (username_lower, peer_type, peer_id, username, active, editable, sort_order, collectible_id, updated_at)
SELECT $1, $2, $3, $4, true, false, LEAST(
    COALESCE((
      SELECT max(sort_order) + 1 FROM peer_usernames
      WHERE peer_type = $2 AND peer_id = $3 AND collectible_id IS NOT NULL
    ), 0), $6::int
  ), $5, now()`,
		usernameLower, peerType, peerID, username, collectibleID, domain.MaxUsernameSortOrder); err != nil {
		if isUniqueViolation(err) {
			return domain.ErrUsernameOccupied
		}
		return fmt.Errorf("insert collectible peer username: %w", err)
	}
	return nil
}

// deleteCollectiblePeerUsernameTx removes the registry projection of an asset,
// releasing the name for anyone else the moment the asset stops being owned.
func deleteCollectiblePeerUsernameTx(ctx context.Context, tx pgx.Tx, collectibleID int64) error {
	if _, err := tx.Exec(ctx, `
DELETE FROM peer_usernames WHERE collectible_id = $1`, collectibleID); err != nil {
		return fmt.Errorf("delete collectible peer username: %w", err)
	}
	return nil
}

func isUniqueConstraint(err error, constraintName string) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == pgerrcode.UniqueViolation && pgErr.ConstraintName == constraintName
}
