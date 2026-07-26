package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"telesrv/internal/domain"
	"telesrv/internal/store"
	"telesrv/internal/store/postgres/sqlcgen"
)

// CollectibleUsernameStore is the PostgreSQL implementation of the collectible
// username registry and the asset lifecycle behind it.
//
// The asset (collectible_usernames) and its registry projection (peer_usernames)
// are always written in one transaction: an asset can never be owned without
// being resolvable, and a resolvable collectible row always has a live owner.
// The asset row is locked with SELECT ... FOR UPDATE before any mutation, and the
// name itself is locked through the registry row, so two concurrent commands on
// the same name or the same asset serialise instead of interleaving.
type CollectibleUsernameStore struct {
	db sqlcgen.DBTX
}

// NewCollectibleUsernameStore builds the store on a pgx pool or transaction.
func NewCollectibleUsernameStore(db sqlcgen.DBTX) *CollectibleUsernameStore {
	return &CollectibleUsernameStore{db: db}
}

var (
	_ store.UsernameRegistryStore    = (*CollectibleUsernameStore)(nil)
	_ store.CollectibleUsernameStore = (*CollectibleUsernameStore)(nil)
)

const (
	defaultCollectibleUsernameListLimit = 50
	maxCollectibleUsernameListLimit     = 200
)

// collectibleUsernameColumns is the asset projection shared by every reader.
const collectibleUsernameColumns = `id, username, status, owner_peer_type, owner_peer_id,
       purchase_date, currency, amount, crypto_currency, crypto_amount, url,
       original_owner_peer_type, original_owner_peer_id, transfer_count, version,
       created_at, updated_at`

// PeerUsernames returns the peer's registry rows in projection order.
func (s *CollectibleUsernameStore) PeerUsernames(ctx context.Context, peer domain.Peer) ([]domain.Username, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("collectible username store is not configured")
	}
	return listPeerUsernames(ctx, s.db, peer)
}

// PeerUsernamesBatch resolves several peers in one round trip.
func (s *CollectibleUsernameStore) PeerUsernamesBatch(ctx context.Context, peers []domain.Peer) (map[domain.Peer][]domain.Username, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("collectible username store is not configured")
	}
	return listPeerUsernamesBatch(ctx, s.db, peers)
}

// SetUsernameActive toggles one collectible row. The editable slot is rejected:
// the client owns it through account.updateUsername, not through this path.
func (s *CollectibleUsernameStore) SetUsernameActive(ctx context.Context, peer domain.Peer, username string, active bool) (bool, error) {
	if s == nil || s.db == nil {
		return false, fmt.Errorf("collectible username store is not configured")
	}
	username = domain.NormalizeUsername(username)
	if peer.Type == "" || peer.ID <= 0 || username == "" {
		return false, domain.ErrUsernameInvalid
	}
	usernameLower := strings.ToLower(username)
	changed := false
	err := withTx(ctx, s.db, "set collectible username active", func(tx pgx.Tx) error {
		current, err := lockPeerUsernamesTx(ctx, tx, peer)
		if err != nil {
			return err
		}
		if err := domain.ValidateUsernameToggle(current, username, active); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `
UPDATE peer_usernames SET active = $4, updated_at = now()
WHERE username_lower = $1 AND peer_type = $2 AND peer_id = $3
  AND collectible_id IS NOT NULL AND active <> $4`,
			usernameLower, string(peer.Type), peer.ID, active)
		if err != nil {
			return fmt.Errorf("update collectible username active: %w", err)
		}
		changed = tag.RowsAffected() > 0
		return nil
	})
	if err != nil {
		return false, err
	}
	return changed, nil
}

// ReorderUsernames rewrites collectible sort order. order must be a permutation
// of the peer's collectible usernames; the editable slot always projects first
// and is never touched.
func (s *CollectibleUsernameStore) ReorderUsernames(ctx context.Context, peer domain.Peer, order []string) (bool, error) {
	if s == nil || s.db == nil {
		return false, fmt.Errorf("collectible username store is not configured")
	}
	if peer.Type == "" || peer.ID <= 0 {
		return false, domain.ErrUsernameInvalid
	}
	changed := false
	err := withTx(ctx, s.db, "reorder collectible usernames", func(tx pgx.Tx) error {
		current, err := lockPeerUsernamesTx(ctx, tx, peer)
		if err != nil {
			return err
		}
		next, err := domain.ApplyUsernameReorder(current, order)
		if err != nil {
			return err
		}
		previous := make(map[string]int, len(current))
		for _, item := range current {
			previous[strings.ToLower(item.Username)] = item.SortOrder
		}
		for _, item := range next {
			if !item.Collectible() {
				continue
			}
			key := strings.ToLower(item.Username)
			if previous[key] == item.SortOrder {
				continue
			}
			if _, err := tx.Exec(ctx, `
UPDATE peer_usernames SET sort_order = $4, updated_at = now()
WHERE username_lower = $1 AND peer_type = $2 AND peer_id = $3`,
				key, string(peer.Type), peer.ID, item.SortOrder); err != nil {
				return fmt.Errorf("update collectible username sort order: %w", err)
			}
			changed = true
		}
		return nil
	})
	if err != nil {
		return false, err
	}
	return changed, nil
}

// DeactivateAllUsernames clears the active flag on every collectible row, which
// is what losing a public surface does to the peer's collectible names. The
// editable slot keeps its own flag.
func (s *CollectibleUsernameStore) DeactivateAllUsernames(ctx context.Context, peer domain.Peer) (bool, error) {
	if s == nil || s.db == nil {
		return false, fmt.Errorf("collectible username store is not configured")
	}
	if peer.Type == "" || peer.ID <= 0 {
		return false, domain.ErrUsernameInvalid
	}
	tag, err := s.db.Exec(ctx, `
UPDATE peer_usernames SET active = false, updated_at = now()
WHERE peer_type = $1 AND peer_id = $2 AND collectible_id IS NOT NULL AND active`,
		string(peer.Type), peer.ID)
	if err != nil {
		return false, fmt.Errorf("deactivate collectible usernames: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// MintCollectibleUsername creates the asset, optionally assigning it in the same
// transaction. A non-empty CommandKey makes the mint replay-safe: the recorded
// provenance row carries the key, so a retry returns the original asset.
func (s *CollectibleUsernameStore) MintCollectibleUsername(ctx context.Context, req domain.MintCollectibleUsernameRequest) (domain.CollectibleUsername, bool, error) {
	if s == nil || s.db == nil {
		return domain.CollectibleUsername{}, false, fmt.Errorf("collectible username store is not configured")
	}
	req.Username = domain.NormalizeUsername(req.Username)
	req.Actor = strings.TrimSpace(req.Actor)
	req.Reason = strings.TrimSpace(req.Reason)
	req.CommandKey = strings.TrimSpace(req.CommandKey)
	if err := req.Validate(); err != nil {
		return domain.CollectibleUsername{}, false, err
	}
	usernameLower := strings.ToLower(req.Username)
	var asset domain.CollectibleUsername
	created := false
	err := withTx(ctx, s.db, "mint collectible username", func(tx pgx.Tx) error {
		if replayed, found, err := replayCollectibleUsernameCommand(ctx, tx, req.CommandKey); err != nil {
			return err
		} else if found {
			asset = replayed
			return nil
		}
		now := time.Now().UTC()
		purchaseDate := req.PurchaseDate.UTC()
		if req.PurchaseDate.IsZero() {
			purchaseDate = now
		}
		// The registry row locks the name against editable usernames. Occupancy of
		// the asset itself is decided by the live rows only: 0152 narrowed
		// uniqueness to status <> 'burned', so a retired name can be issued again
		// while its burned rows stay as provenance.
		if _, found, err := getPeerUsernameOwner(ctx, tx, usernameLower, true); err != nil {
			return err
		} else if found {
			return domain.ErrUsernameOccupied
		}
		var existing int64
		switch err := tx.QueryRow(ctx, `
SELECT id FROM collectible_usernames
WHERE username_lower = $1 AND status <> 'burned' FOR UPDATE`, usernameLower).Scan(&existing); {
		case err == nil:
			return domain.ErrUsernameOccupied
		case errors.Is(err, pgx.ErrNoRows):
		default:
			return fmt.Errorf("lock collectible username: %w", err)
		}
		owner := req.Owner
		status := domain.CollectibleUsernameStatusVault
		if owner.Type != "" {
			status = domain.CollectibleUsernameStatusOwned
			count, err := countPeerCollectibleUsernamesTx(ctx, tx, string(owner.Type), owner.ID)
			if err != nil {
				return err
			}
			if count >= domain.MaxPeerCollectibleUsernames {
				return domain.ErrCollectibleUsernameLimit
			}
		}
		var collectibleID int64
		if err := tx.QueryRow(ctx, `
INSERT INTO collectible_usernames
    (username, username_lower, status, owner_peer_type, owner_peer_id, purchase_date,
     currency, amount, crypto_currency, crypto_amount, url,
     original_owner_peer_type, original_owner_peer_id, transfer_count, version,
     created_at, updated_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$4,$5,0,1,$12,$12)
RETURNING id`,
			req.Username, usernameLower, string(status), string(owner.Type), owner.ID, purchaseDate,
			req.Currency, req.Amount, req.CryptoCurrency, req.CryptoAmount, req.URL, now,
		).Scan(&collectibleID); err != nil {
			if isUniqueViolation(err) {
				return domain.ErrUsernameOccupied
			}
			return fmt.Errorf("insert collectible username: %w", err)
		}
		if owner.Type != "" {
			if err := insertCollectiblePeerUsernameTx(ctx, tx, string(owner.Type), owner.ID,
				req.Username, usernameLower, collectibleID); err != nil {
				return err
			}
		}
		// A mint that assigns an owner records a single 'mint' row carrying the
		// recipient: the command key is unique, so one command owns one row.
		if err := insertCollectibleUsernameTransferTx(ctx, tx, collectibleUsernameTransfer{
			collectibleID: collectibleID,
			kind:          domain.CollectibleUsernameKindMint,
			to:            owner,
			currency:      req.Currency,
			amount:        req.Amount,
			actor:         req.Actor,
			reason:        req.Reason,
			commandKey:    req.CommandKey,
			createdAt:     now,
		}); err != nil {
			return err
		}
		loaded, err := collectibleUsernameByIDTx(ctx, tx, collectibleID)
		if err != nil {
			return err
		}
		asset = loaded
		created = true
		return nil
	})
	if err != nil {
		return domain.CollectibleUsername{}, false, err
	}
	return asset, created, nil
}

// TransferCollectibleUsername moves the asset to req.To, out of the vault or from
// the current holder. The old registry row is removed and the new one inserted in
// the same transaction, so the name never resolves to the wrong peer.
func (s *CollectibleUsernameStore) TransferCollectibleUsername(ctx context.Context, req domain.TransferCollectibleUsernameRequest) (domain.CollectibleUsername, bool, error) {
	if s == nil || s.db == nil {
		return domain.CollectibleUsername{}, false, fmt.Errorf("collectible username store is not configured")
	}
	req.Username = domain.NormalizeUsername(req.Username)
	req.Actor = strings.TrimSpace(req.Actor)
	req.Reason = strings.TrimSpace(req.Reason)
	req.CommandKey = strings.TrimSpace(req.CommandKey)
	if err := req.Validate(); err != nil {
		return domain.CollectibleUsername{}, false, err
	}
	usernameLower := strings.ToLower(req.Username)
	var asset domain.CollectibleUsername
	changed := false
	err := withTx(ctx, s.db, "transfer collectible username", func(tx pgx.Tx) error {
		if replayed, found, err := replayCollectibleUsernameCommand(ctx, tx, req.CommandKey); err != nil {
			return err
		} else if found {
			asset = replayed
			return nil
		}
		current, err := lockCollectibleUsernameTx(ctx, tx, usernameLower)
		if err != nil {
			return err
		}
		if current.Status == domain.CollectibleUsernameStatusBurned {
			return domain.ErrCollectibleUsernameBurned
		}
		if current.Owned() && current.Owner == req.To {
			asset = current
			return nil
		}
		now := time.Now().UTC()
		if err := deleteCollectiblePeerUsernameTx(ctx, tx, current.ID); err != nil {
			return err
		}
		count, err := countPeerCollectibleUsernamesTx(ctx, tx, string(req.To.Type), req.To.ID)
		if err != nil {
			return err
		}
		if count >= domain.MaxPeerCollectibleUsernames {
			return domain.ErrCollectibleUsernameLimit
		}
		if err := insertCollectiblePeerUsernameTx(ctx, tx, string(req.To.Type), req.To.ID,
			current.Username, usernameLower, current.ID); err != nil {
			return err
		}
		// The original owner is the first holder and is recorded once: a name that
		// left the vault keeps its provenance across every later move and the burn.
		if _, err := tx.Exec(ctx, `
UPDATE collectible_usernames
SET status = 'owned',
    owner_peer_type = $2,
    owner_peer_id = $3,
    original_owner_peer_type = CASE WHEN original_owner_peer_type = '' THEN $2 ELSE original_owner_peer_type END,
    original_owner_peer_id = CASE WHEN original_owner_peer_type = '' THEN $3 ELSE original_owner_peer_id END,
    transfer_count = transfer_count + 1,
    version = version + 1,
    updated_at = $4
WHERE id = $1`, current.ID, string(req.To.Type), req.To.ID, now); err != nil {
			return fmt.Errorf("update transferred collectible username: %w", err)
		}
		if err := insertCollectibleUsernameTransferTx(ctx, tx, collectibleUsernameTransfer{
			collectibleID: current.ID,
			kind:          domain.CollectibleUsernameKindTransfer,
			from:          current.Owner,
			to:            req.To,
			actor:         req.Actor,
			reason:        req.Reason,
			commandKey:    req.CommandKey,
			createdAt:     now,
		}); err != nil {
			return err
		}
		loaded, err := collectibleUsernameByIDTx(ctx, tx, current.ID)
		if err != nil {
			return err
		}
		asset = loaded
		changed = true
		return nil
	})
	if err != nil {
		return domain.CollectibleUsername{}, false, err
	}
	return asset, changed, nil
}

// RevokeCollectibleUsername returns the asset to the vault, or burns it when
// req.Burn is set. Either way the registry row goes away, so the name stops
// resolving to the former holder; a burn additionally retires the asset.
func (s *CollectibleUsernameStore) RevokeCollectibleUsername(ctx context.Context, req domain.RevokeCollectibleUsernameRequest) (domain.CollectibleUsername, bool, error) {
	if s == nil || s.db == nil {
		return domain.CollectibleUsername{}, false, fmt.Errorf("collectible username store is not configured")
	}
	req.Username = domain.NormalizeUsername(req.Username)
	req.Actor = strings.TrimSpace(req.Actor)
	req.Reason = strings.TrimSpace(req.Reason)
	req.CommandKey = strings.TrimSpace(req.CommandKey)
	if err := req.Validate(); err != nil {
		return domain.CollectibleUsername{}, false, err
	}
	usernameLower := strings.ToLower(req.Username)
	var asset domain.CollectibleUsername
	changed := false
	err := withTx(ctx, s.db, "revoke collectible username", func(tx pgx.Tx) error {
		if replayed, found, err := replayCollectibleUsernameCommand(ctx, tx, req.CommandKey); err != nil {
			return err
		} else if found {
			asset = replayed
			return nil
		}
		current, err := lockCollectibleUsernameTx(ctx, tx, usernameLower)
		if err != nil {
			return err
		}
		if current.Status == domain.CollectibleUsernameStatusBurned {
			return domain.ErrCollectibleUsernameBurned
		}
		if !req.Burn && !current.Owned() {
			// Already in the vault: nothing to release, nothing to record.
			asset = current
			return nil
		}
		now := time.Now().UTC()
		if err := deleteCollectiblePeerUsernameTx(ctx, tx, current.ID); err != nil {
			return err
		}
		status := domain.CollectibleUsernameStatusVault
		kind := domain.CollectibleUsernameKindRevoke
		if req.Burn {
			status = domain.CollectibleUsernameStatusBurned
			kind = domain.CollectibleUsernameKindBurn
		}
		if _, err := tx.Exec(ctx, `
UPDATE collectible_usernames
SET status = $2,
    owner_peer_type = '',
    owner_peer_id = 0,
    version = version + 1,
    updated_at = $3
WHERE id = $1`, current.ID, string(status), now); err != nil {
			return fmt.Errorf("update revoked collectible username: %w", err)
		}
		if err := insertCollectibleUsernameTransferTx(ctx, tx, collectibleUsernameTransfer{
			collectibleID: current.ID,
			kind:          kind,
			from:          current.Owner,
			actor:         req.Actor,
			reason:        req.Reason,
			commandKey:    req.CommandKey,
			createdAt:     now,
		}); err != nil {
			return err
		}
		loaded, err := collectibleUsernameByIDTx(ctx, tx, current.ID)
		if err != nil {
			return err
		}
		asset = loaded
		changed = true
		return nil
	})
	if err != nil {
		return domain.CollectibleUsername{}, false, err
	}
	return asset, changed, nil
}

// DeleteCollectibleUsername removes the live asset for a name outright: the
// registry row, the asset and its provenance log all go away, and the name
// becomes free for any use. This is the operator's escape hatch for a mistaken
// issue, as opposed to Revoke+Burn, which retires an asset but keeps its history.
//
// The command key cannot make this idempotent -- a replay has no record left to
// return -- so a second call simply reports deleted=false once no live asset
// remains.
func (s *CollectibleUsernameStore) DeleteCollectibleUsername(ctx context.Context, req domain.DeleteCollectibleUsernameRequest) (bool, error) {
	if s == nil || s.db == nil {
		return false, fmt.Errorf("collectible username store is not configured")
	}
	req.Username = domain.NormalizeUsername(req.Username)
	req.Actor = strings.TrimSpace(req.Actor)
	req.Reason = strings.TrimSpace(req.Reason)
	req.CommandKey = strings.TrimSpace(req.CommandKey)
	if err := req.Validate(); err != nil {
		return false, err
	}
	usernameLower := strings.ToLower(req.Username)
	deleted := false
	err := withTx(ctx, s.db, "delete collectible username", func(tx pgx.Tx) error {
		var id int64
		switch err := tx.QueryRow(ctx, `
SELECT id FROM collectible_usernames
WHERE username_lower = $1 AND status <> 'burned'
ORDER BY id DESC
LIMIT 1
FOR UPDATE`, usernameLower).Scan(&id); {
		case err == nil:
		case errors.Is(err, pgx.ErrNoRows):
			// Either the name was never issued, or only burned history remains.
			// Both are "nothing live to delete" rather than an error, so a repeated
			// command stays safe.
			return nil
		default:
			return fmt.Errorf("lock collectible username for delete: %w", err)
		}
		if err := deleteCollectiblePeerUsernameTx(ctx, tx, id); err != nil {
			return err
		}
		// collectible_username_transfers references the asset with ON DELETE
		// CASCADE, so the provenance rows go with it.
		if _, err := tx.Exec(ctx, `DELETE FROM collectible_usernames WHERE id = $1`, id); err != nil {
			return fmt.Errorf("delete collectible username: %w", err)
		}
		deleted = true
		return nil
	})
	if err != nil {
		return false, err
	}
	return deleted, nil
}

// CollectibleUsername looks the asset up by name. A live asset wins; when the
// name only has burned rows the newest one is returned, because the provenance of
// a retired name still has to be inspectable.
func (s *CollectibleUsernameStore) CollectibleUsername(ctx context.Context, username string) (domain.CollectibleUsername, error) {
	if s == nil || s.db == nil {
		return domain.CollectibleUsername{}, fmt.Errorf("collectible username store is not configured")
	}
	usernameLower := strings.ToLower(domain.NormalizeUsername(username))
	if usernameLower == "" {
		return domain.CollectibleUsername{}, domain.ErrCollectibleUsernameNotFound
	}
	asset, err := scanCollectibleUsername(s.db.QueryRow(ctx, `
SELECT `+collectibleUsernameColumns+`
FROM collectible_usernames
WHERE username_lower = $1
ORDER BY (status <> 'burned') DESC, id DESC
LIMIT 1`, usernameLower))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.CollectibleUsername{}, domain.ErrCollectibleUsernameNotFound
	}
	if err != nil {
		return domain.CollectibleUsername{}, fmt.Errorf("get collectible username: %w", err)
	}
	return asset, nil
}

// CollectibleUsernameByID looks the asset up by identity.
func (s *CollectibleUsernameStore) CollectibleUsernameByID(ctx context.Context, id int64) (domain.CollectibleUsername, error) {
	if s == nil || s.db == nil {
		return domain.CollectibleUsername{}, fmt.Errorf("collectible username store is not configured")
	}
	if id <= 0 {
		return domain.CollectibleUsername{}, domain.ErrCollectibleUsernameNotFound
	}
	asset, err := collectibleUsernameByIDTx(ctx, s.db, id)
	if err != nil {
		return domain.CollectibleUsername{}, err
	}
	return asset, nil
}

// ListCollectibleUsernames is the admin listing query with keyset paging on the
// asset id, which matches the (status, id DESC) and (owner, id DESC) indexes.
func (s *CollectibleUsernameStore) ListCollectibleUsernames(ctx context.Context, filter domain.CollectibleUsernameFilter) ([]domain.CollectibleUsername, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("collectible username store is not configured")
	}
	if filter.Status != "" && !filter.Status.Valid() {
		return nil, domain.ErrCollectibleUsernameStateInvalid
	}
	if filter.Owner.Type != "" && filter.Owner.ID <= 0 {
		return nil, domain.ErrCollectibleUsernameStateInvalid
	}
	limit := filter.Limit
	if limit <= 0 {
		limit = defaultCollectibleUsernameListLimit
	}
	if limit > maxCollectibleUsernameListLimit {
		limit = maxCollectibleUsernameListLimit
	}
	query := strings.ToLower(domain.NormalizeUsername(filter.Query))
	rows, err := s.db.Query(ctx, `
SELECT `+collectibleUsernameColumns+`
FROM collectible_usernames
WHERE ($1 = '' OR status = $1)
  AND ($2 = '' OR (owner_peer_type = $2 AND owner_peer_id = $3))
  AND ($4 = '' OR username_lower LIKE $5 || '%')
  AND ($6 = 0 OR id < $6)
ORDER BY id DESC
LIMIT $7`,
		string(filter.Status), string(filter.Owner.Type), filter.Owner.ID,
		query, escapeLike(query), filter.BeforeID, limit)
	if err != nil {
		return nil, fmt.Errorf("list collectible usernames: %w", err)
	}
	defer rows.Close()
	out := make([]domain.CollectibleUsername, 0, limit)
	for rows.Next() {
		asset, err := scanCollectibleUsername(rows)
		if err != nil {
			return nil, fmt.Errorf("scan collectible username: %w", err)
		}
		out = append(out, asset)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate collectible usernames: %w", err)
	}
	return out, nil
}

// CollectibleUsernameTransfers returns the provenance log, newest first.
func (s *CollectibleUsernameStore) CollectibleUsernameTransfers(ctx context.Context, collectibleID int64, limit int) ([]domain.CollectibleUsernameTransfer, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("collectible username store is not configured")
	}
	if collectibleID <= 0 {
		return nil, domain.ErrCollectibleUsernameNotFound
	}
	if limit <= 0 {
		limit = defaultCollectibleUsernameListLimit
	}
	if limit > maxCollectibleUsernameListLimit {
		limit = maxCollectibleUsernameListLimit
	}
	rows, err := s.db.Query(ctx, `
SELECT id, collectible_id, kind, from_peer_type, from_peer_id, to_peer_type, to_peer_id,
       currency, amount, actor, reason, COALESCE(command_key, ''), created_at
FROM collectible_username_transfers
WHERE collectible_id = $1
ORDER BY id DESC
LIMIT $2`, collectibleID, limit)
	if err != nil {
		return nil, fmt.Errorf("list collectible username transfers: %w", err)
	}
	defer rows.Close()
	out := make([]domain.CollectibleUsernameTransfer, 0, limit)
	for rows.Next() {
		var item domain.CollectibleUsernameTransfer
		var kind, fromType, toType string
		if err := rows.Scan(&item.ID, &item.CollectibleID, &kind, &fromType, &item.From.ID,
			&toType, &item.To.ID, &item.Currency, &item.Amount, &item.Actor, &item.Reason,
			&item.CommandKey, &item.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan collectible username transfer: %w", err)
		}
		item.Kind = domain.CollectibleUsernameTransferKind(kind)
		item.From.Type = domain.PeerType(fromType)
		item.To.Type = domain.PeerType(toType)
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate collectible username transfers: %w", err)
	}
	return out, nil
}

type collectibleUsernameTransfer struct {
	collectibleID int64
	kind          domain.CollectibleUsernameTransferKind
	from          domain.Peer
	to            domain.Peer
	currency      string
	amount        int64
	actor         string
	reason        string
	commandKey    string
	createdAt     time.Time
}

func insertCollectibleUsernameTransferTx(ctx context.Context, tx pgx.Tx, entry collectibleUsernameTransfer) error {
	if _, err := tx.Exec(ctx, `
INSERT INTO collectible_username_transfers
    (collectible_id, kind, from_peer_type, from_peer_id, to_peer_type, to_peer_id,
     currency, amount, actor, reason, command_key, created_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,NULLIF($11,''),$12)`,
		entry.collectibleID, string(entry.kind), string(entry.from.Type), entry.from.ID,
		string(entry.to.Type), entry.to.ID, entry.currency, entry.amount,
		entry.actor, entry.reason, entry.commandKey, entry.createdAt); err != nil {
		return fmt.Errorf("insert collectible username transfer: %w", err)
	}
	return nil
}

// replayCollectibleUsernameCommand resolves an already-recorded command key to
// the asset it touched, which is what makes mint/transfer/revoke retry-safe.
//
// The transaction-scoped advisory lock serialises commands sharing a key, so two
// concurrent retries cannot both pass the lookup and race on the unique
// command_key index -- the second one waits and then observes the recorded row.
func replayCollectibleUsernameCommand(ctx context.Context, tx pgx.Tx, commandKey string) (domain.CollectibleUsername, bool, error) {
	if commandKey == "" {
		return domain.CollectibleUsername{}, false, nil
	}
	if _, err := tx.Exec(ctx, `
SELECT pg_advisory_xact_lock(hashtextextended('collectible-username:' || $1::text, 0))`, commandKey); err != nil {
		return domain.CollectibleUsername{}, false, fmt.Errorf("lock collectible username command: %w", err)
	}
	var collectibleID int64
	err := tx.QueryRow(ctx, `
SELECT collectible_id FROM collectible_username_transfers WHERE command_key = $1`, commandKey).Scan(&collectibleID)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.CollectibleUsername{}, false, nil
	}
	if err != nil {
		return domain.CollectibleUsername{}, false, fmt.Errorf("lookup collectible username command: %w", err)
	}
	asset, err := collectibleUsernameByIDTx(ctx, tx, collectibleID)
	if err != nil {
		return domain.CollectibleUsername{}, false, err
	}
	return asset, true, nil
}

// lockCollectibleUsernameTx locks the row a name currently resolves to. After
// 0151 one name can carry several burned rows plus at most one live row, so the
// live row wins and the newest burned row is the fallback. That keeps a mutation
// of a retired name reporting ErrCollectibleUsernameBurned instead of degrading
// to a not-found.
func lockCollectibleUsernameTx(ctx context.Context, tx pgx.Tx, usernameLower string) (domain.CollectibleUsername, error) {
	asset, err := scanCollectibleUsername(tx.QueryRow(ctx, `
SELECT `+collectibleUsernameColumns+`
FROM collectible_usernames
WHERE username_lower = $1
ORDER BY (status <> 'burned') DESC, id DESC
LIMIT 1
FOR UPDATE`, usernameLower))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.CollectibleUsername{}, domain.ErrCollectibleUsernameNotFound
	}
	if err != nil {
		return domain.CollectibleUsername{}, fmt.Errorf("lock collectible username: %w", err)
	}
	return asset, nil
}

func collectibleUsernameByIDTx(ctx context.Context, db sqlcgen.DBTX, id int64) (domain.CollectibleUsername, error) {
	asset, err := scanCollectibleUsername(db.QueryRow(ctx, `
SELECT `+collectibleUsernameColumns+`
FROM collectible_usernames WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.CollectibleUsername{}, domain.ErrCollectibleUsernameNotFound
	}
	if err != nil {
		return domain.CollectibleUsername{}, fmt.Errorf("get collectible username by id: %w", err)
	}
	return asset, nil
}

func scanCollectibleUsername(row pgx.Row) (domain.CollectibleUsername, error) {
	var asset domain.CollectibleUsername
	var status, ownerType, originalOwnerType string
	if err := row.Scan(&asset.ID, &asset.Username, &status, &ownerType, &asset.Owner.ID,
		&asset.PurchaseDate, &asset.Currency, &asset.Amount, &asset.CryptoCurrency,
		&asset.CryptoAmount, &asset.URL, &originalOwnerType, &asset.OriginalOwner.ID,
		&asset.TransferCount, &asset.Version, &asset.CreatedAt, &asset.UpdatedAt); err != nil {
		return domain.CollectibleUsername{}, err
	}
	asset.Status = domain.CollectibleUsernameStatus(status)
	asset.Owner.Type = domain.PeerType(ownerType)
	asset.OriginalOwner.Type = domain.PeerType(originalOwnerType)
	return asset, nil
}
