package memory

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	"telesrv/internal/domain"
)

// Default page sizes applied when an admin filter leaves the limit unset. The
// PostgreSQL queries page with LIMIT, so an unset limit has to resolve to a
// finite page in both backends.
const (
	defaultCollectibleUsernameListLimit     = 50
	defaultCollectibleUsernameTransferLimit = 50
)

// CollectibleUsernameStore is the in-memory implementation of both
// store.UsernameRegistryStore and store.CollectibleUsernameStore. RPC unit tests
// run against it, so it reproduces every invariant migration 0149 encodes as an
// index or CHECK constraint and returns the same domain errors PostgreSQL maps
// its violations onto:
//
//   - peer_usernames_peer_editable_idx: exactly one editable row per peer.
//   - peer_usernames_collectible_not_editable_check: a row backed by an asset is
//     never editable, so client-driven username edits cannot move an asset.
//   - peer_usernames.username_lower UNIQUE: global, case-insensitive name
//     uniqueness across users and channels, keyed by lower(username).
//   - collectible_usernames.username_lower UNIQUE: a name is minted at most
//     once. A burn therefore releases the registry row -- the name can be
//     occupied again as a peer username -- while the asset row keeps the name
//     for provenance and blocks a second mint.
//   - the status/owner CHECK pair: owner is populated exactly for status 'owned'.
//   - collectible_username_transfers_command_idx: command keys are globally
//     unique, which is what makes mint/transfer/revoke replay-safe.
type CollectibleUsernameStore struct {
	mu             sync.Mutex
	nextAssetID    int64
	nextTransferID int64
	// assets is collectible_usernames keyed by identity.
	assets map[int64]domain.CollectibleUsername
	// assetsByName resolves a name onto the asset it currently stands for. After
	// migration 0151 uniqueness covers live rows only, so one name can accumulate
	// several burned rows plus at most one live row; this index points at the live
	// row when there is one and at the newest burned row otherwise, mirroring the
	// SQL lookup order.
	assetsByName map[string]int64
	// registry is peer_usernames keyed by username_lower, which is exactly how
	// the table enforces global uniqueness.
	registry map[string]collectibleRegistryRow
	// transfers is the append-only provenance log per asset.
	transfers map[int64][]domain.CollectibleUsernameTransfer
	// commands maps a provenance command key onto the asset it touched.
	commands map[string]int64
}

// collectibleRegistryRow is one peer_usernames row: the owning peer plus the
// projected username shape.
type collectibleRegistryRow struct {
	peer domain.Peer
	row  domain.Username
}

// NewCollectibleUsernameStore creates an empty registry. Asset ids start at 1 so
// a zero CollectibleID keeps meaning "editable slot", matching the nullable
// collectible_id column.
func NewCollectibleUsernameStore() *CollectibleUsernameStore {
	return &CollectibleUsernameStore{
		nextAssetID:    1,
		nextTransferID: 1,
		assets:         make(map[int64]domain.CollectibleUsername),
		assetsByName:   make(map[string]int64),
		registry:       make(map[string]collectibleRegistryRow),
		transfers:      make(map[int64][]domain.CollectibleUsernameTransfer),
		commands:       make(map[string]int64),
	}
}

// SetEditableUsername writes the peer's editable slot, mirroring the
// replace-then-insert the PostgreSQL user and channel stores run inside
// account.updateUsername / channels.updateUsername. The memory backend keeps
// usernames on the user and channel rows, so tests need this hook to give a peer
// the editable registry row the projection expects. An empty username clears the
// slot.
func (s *CollectibleUsernameStore) SetEditableUsername(_ context.Context, peer domain.Peer, username string) (bool, error) {
	if !validCollectibleUsernamePeer(peer) {
		return false, domain.ErrUsernameInvalid
	}
	username = domain.NormalizeUsername(username)
	s.mu.Lock()
	defer s.mu.Unlock()
	if username == "" {
		return s.clearEditableLocked(peer), nil
	}
	// peer_usernames has no length CHECK; the 5..32 editable rule lives in the
	// service layer. Only the character rules are a registry concern.
	if !domain.ValidCollectibleUsername(username) {
		return false, domain.ErrUsernameInvalid
	}
	key := strings.ToLower(username)
	if existing, ok := s.registry[key]; ok {
		if existing.peer == peer && existing.row.Editable {
			if existing.row.Username == username {
				return false, nil
			}
			existing.row.Username = username
			s.registry[key] = existing
			return true, nil
		}
		return false, domain.ErrUsernameOccupied
	}
	// A live asset owns its name even while it sits in the vault: only a burn
	// puts the name back into the free pool.
	if id, ok := s.assetsByName[key]; ok && s.assets[id].Status != domain.CollectibleUsernameStatusBurned {
		return false, domain.ErrUsernameOccupied
	}
	s.clearEditableLocked(peer)
	s.registry[key] = collectibleRegistryRow{
		peer: peer,
		row: domain.Username{
			Username: username,
			Active:   true,
			Editable: true,
		},
	}
	return true, nil
}

// PeerUsernames returns the peer's registry rows in projection order.
func (s *CollectibleUsernameStore) PeerUsernames(_ context.Context, peer domain.Peer) ([]domain.Username, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.peerUsernamesLocked(peer), nil
}

// PeerUsernamesBatch resolves several peers at once; peers holding no username
// are absent from the result.
func (s *CollectibleUsernameStore) PeerUsernamesBatch(_ context.Context, peers []domain.Peer) (map[domain.Peer][]domain.Username, error) {
	out := make(map[domain.Peer][]domain.Username, len(peers))
	if len(peers) == 0 {
		return out, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, peer := range peers {
		if !validCollectibleUsernamePeer(peer) {
			continue
		}
		if _, done := out[peer]; done {
			continue
		}
		rows := s.peerUsernamesLocked(peer)
		if len(rows) == 0 {
			continue
		}
		out[peer] = rows
	}
	return out, nil
}

// SetUsernameActive toggles one collectible row. The domain validator owns the
// rules: the editable slot is off limits and a peer that holds usernames must
// keep at least one active.
func (s *CollectibleUsernameStore) SetUsernameActive(_ context.Context, peer domain.Peer, username string, active bool) (bool, error) {
	if !validCollectibleUsernamePeer(peer) {
		return false, domain.ErrUsernameInvalid
	}
	username = domain.NormalizeUsername(username)
	s.mu.Lock()
	defer s.mu.Unlock()
	current := s.peerUsernamesLocked(peer)
	if err := domain.ValidateUsernameToggle(current, username, active); err != nil {
		return false, err
	}
	key := strings.ToLower(username)
	entry := s.registry[key]
	if entry.row.Active == active {
		return false, nil
	}
	entry.row.Active = active
	s.registry[key] = entry
	return true, nil
}

// ReorderUsernames rewrites collectible sort order. Validation and the resulting
// order both come from the domain helper, so the two backends cannot drift.
func (s *CollectibleUsernameStore) ReorderUsernames(_ context.Context, peer domain.Peer, order []string) (bool, error) {
	if !validCollectibleUsernamePeer(peer) {
		return false, domain.ErrUsernameInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current := s.peerUsernamesLocked(peer)
	reordered, err := domain.ApplyUsernameReorder(current, order)
	if err != nil {
		return false, err
	}
	changed := false
	for _, row := range reordered {
		if !row.Collectible() {
			continue
		}
		key := strings.ToLower(row.Username)
		entry := s.registry[key]
		if entry.row.SortOrder == row.SortOrder {
			continue
		}
		entry.row.SortOrder = row.SortOrder
		s.registry[key] = entry
		changed = true
	}
	return changed, nil
}

// DeactivateAllUsernames clears the active flag on every collectible row and
// leaves the editable slot alone.
func (s *CollectibleUsernameStore) DeactivateAllUsernames(_ context.Context, peer domain.Peer) (bool, error) {
	if !validCollectibleUsernamePeer(peer) {
		return false, domain.ErrUsernameInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	changed := false
	for key, entry := range s.registry {
		if entry.peer != peer || !entry.row.Collectible() || !entry.row.Active {
			continue
		}
		entry.row.Active = false
		s.registry[key] = entry
		changed = true
	}
	return changed, nil
}

// MintCollectibleUsername creates the asset, optionally assigning it in the same
// call. A replayed command key returns the recorded asset with created=false.
func (s *CollectibleUsernameStore) MintCollectibleUsername(_ context.Context, req domain.MintCollectibleUsernameRequest) (domain.CollectibleUsername, bool, error) {
	req.Username = domain.NormalizeUsername(req.Username)
	if err := req.Validate(); err != nil {
		return domain.CollectibleUsername{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if asset, ok := s.replayLocked(req.CommandKey); ok {
		return asset, false, nil
	}
	key := strings.ToLower(req.Username)
	// Only a live asset occupies a name. A name whose history is entirely burned
	// is free to be issued again, and the new asset takes over the index entry
	// while the burned rows stay as provenance.
	if id, ok := s.assetsByName[key]; ok && s.assets[id].Status != domain.CollectibleUsernameStatusBurned {
		return domain.CollectibleUsername{}, false, domain.ErrUsernameOccupied
	}
	if _, ok := s.registry[key]; ok {
		return domain.CollectibleUsername{}, false, domain.ErrUsernameOccupied
	}
	now := time.Now().UTC()
	purchaseDate := req.PurchaseDate
	if purchaseDate.IsZero() {
		purchaseDate = now
	}
	asset := domain.CollectibleUsername{
		ID:             s.nextAssetID,
		Username:       req.Username,
		Status:         domain.CollectibleUsernameStatusVault,
		PurchaseDate:   purchaseDate,
		Currency:       req.Currency,
		Amount:         req.Amount,
		CryptoCurrency: req.CryptoCurrency,
		CryptoAmount:   req.CryptoAmount,
		URL:            req.URL,
		Version:        1,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if req.Owner.Type != "" {
		asset.Status = domain.CollectibleUsernameStatusOwned
		asset.Owner = req.Owner
		// The first holder is the original owner and survives every later move.
		asset.OriginalOwner = req.Owner
	}
	if err := asset.Validate(); err != nil {
		return domain.CollectibleUsername{}, false, err
	}
	if asset.Owned() && s.countCollectiblesLocked(req.Owner) >= domain.MaxPeerCollectibleUsernames {
		return domain.CollectibleUsername{}, false, domain.ErrCollectibleUsernameLimit
	}
	s.nextAssetID++
	s.assets[asset.ID] = asset
	s.assetsByName[key] = asset.ID
	if asset.Owned() {
		s.attachLocked(asset, req.Owner)
	}
	s.recordTransferLocked(domain.CollectibleUsernameTransfer{
		CollectibleID: asset.ID,
		Kind:          domain.CollectibleUsernameKindMint,
		To:            req.Owner,
		Currency:      req.Currency,
		Amount:        req.Amount,
		Actor:         req.Actor,
		Reason:        req.Reason,
		CommandKey:    req.CommandKey,
		CreatedAt:     now,
	})
	return asset, true, nil
}

// TransferCollectibleUsername moves the asset out of the vault or between
// holders. Handing the asset to the peer that already holds it is a no-op.
func (s *CollectibleUsernameStore) TransferCollectibleUsername(_ context.Context, req domain.TransferCollectibleUsernameRequest) (domain.CollectibleUsername, bool, error) {
	req.Username = domain.NormalizeUsername(req.Username)
	if err := req.Validate(); err != nil {
		return domain.CollectibleUsername{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if asset, ok := s.replayLocked(req.CommandKey); ok {
		return asset, false, nil
	}
	asset, err := s.assetByNameLocked(req.Username)
	if err != nil {
		return domain.CollectibleUsername{}, false, err
	}
	if asset.Status == domain.CollectibleUsernameStatusBurned {
		return domain.CollectibleUsername{}, false, domain.ErrCollectibleUsernameBurned
	}
	if asset.Owned() && asset.Owner == req.To {
		return asset, false, nil
	}
	key := strings.ToLower(asset.Username)
	// Defensive: the registry row for this name must be the asset's own. A live
	// asset occupies its name globally, so the only way another row can hold it
	// is an inconsistently seeded store.
	if existing, ok := s.registry[key]; ok && existing.row.CollectibleID != asset.ID {
		return domain.CollectibleUsername{}, false, domain.ErrUsernameOccupied
	}
	if s.countCollectiblesLocked(req.To) >= domain.MaxPeerCollectibleUsernames {
		return domain.CollectibleUsername{}, false, domain.ErrCollectibleUsernameLimit
	}
	from := asset.Owner
	now := time.Now().UTC()
	asset.Status = domain.CollectibleUsernameStatusOwned
	asset.Owner = req.To
	if asset.OriginalOwner.Type == "" {
		asset.OriginalOwner = req.To
	}
	asset.TransferCount++
	asset.Version++
	asset.UpdatedAt = now
	if err := asset.Validate(); err != nil {
		return domain.CollectibleUsername{}, false, err
	}
	s.assets[asset.ID] = asset
	s.detachLocked(asset.ID)
	s.attachLocked(asset, req.To)
	s.recordTransferLocked(domain.CollectibleUsernameTransfer{
		CollectibleID: asset.ID,
		Kind:          domain.CollectibleUsernameKindTransfer,
		From:          from,
		To:            req.To,
		Actor:         req.Actor,
		Reason:        req.Reason,
		CommandKey:    req.CommandKey,
		CreatedAt:     now,
	})
	return asset, true, nil
}

// RevokeCollectibleUsername returns the asset to the vault, or burns it.
//
// A revoke keeps the name owned by the asset -- nobody else can take it -- while
// a burn drops the registry row and releases the name back to the free pool. The
// burned asset row itself survives with its name so provenance stays readable
// and the name never mints twice.
func (s *CollectibleUsernameStore) RevokeCollectibleUsername(_ context.Context, req domain.RevokeCollectibleUsernameRequest) (domain.CollectibleUsername, bool, error) {
	req.Username = domain.NormalizeUsername(req.Username)
	if err := req.Validate(); err != nil {
		return domain.CollectibleUsername{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if asset, ok := s.replayLocked(req.CommandKey); ok {
		return asset, false, nil
	}
	asset, err := s.assetByNameLocked(req.Username)
	if err != nil {
		return domain.CollectibleUsername{}, false, err
	}
	if asset.Status == domain.CollectibleUsernameStatusBurned {
		return domain.CollectibleUsername{}, false, domain.ErrCollectibleUsernameBurned
	}
	if !req.Burn && !asset.Owned() {
		return domain.CollectibleUsername{}, false, domain.ErrCollectibleUsernameNotOwned
	}
	from := asset.Owner
	now := time.Now().UTC()
	kind := domain.CollectibleUsernameKindRevoke
	asset.Status = domain.CollectibleUsernameStatusVault
	if req.Burn {
		kind = domain.CollectibleUsernameKindBurn
		asset.Status = domain.CollectibleUsernameStatusBurned
	}
	asset.Owner = domain.Peer{}
	asset.Version++
	asset.UpdatedAt = now
	if err := asset.Validate(); err != nil {
		return domain.CollectibleUsername{}, false, err
	}
	s.assets[asset.ID] = asset
	s.detachLocked(asset.ID)
	s.recordTransferLocked(domain.CollectibleUsernameTransfer{
		CollectibleID: asset.ID,
		Kind:          kind,
		From:          from,
		Actor:         req.Actor,
		Reason:        req.Reason,
		CommandKey:    req.CommandKey,
		CreatedAt:     now,
	})
	return asset, true, nil
}

// CollectibleUsername looks the asset up by name, case-insensitively.
// DeleteCollectibleUsername removes the live asset for a name completely --
// registry row, asset and provenance -- and frees the name for any use. Revoke
// with Burn retires an asset but keeps its history; this is the escape hatch for
// an asset issued by mistake.
//
// A command key cannot make this idempotent: the record it would resolve to is
// gone. A repeated call therefore reports deleted=false once no live asset is
// left, which is also what a delete of a burned-only name reports.
func (s *CollectibleUsernameStore) DeleteCollectibleUsername(_ context.Context, req domain.DeleteCollectibleUsernameRequest) (bool, error) {
	if s == nil {
		return false, nil
	}
	req.Username = domain.NormalizeUsername(req.Username)
	req.Actor = strings.TrimSpace(req.Actor)
	req.Reason = strings.TrimSpace(req.Reason)
	req.CommandKey = strings.TrimSpace(req.CommandKey)
	if err := req.Validate(); err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := strings.ToLower(req.Username)
	id, ok := s.assetsByName[key]
	if !ok {
		return false, nil
	}
	asset, ok := s.assets[id]
	if !ok || asset.Status == domain.CollectibleUsernameStatusBurned {
		return false, nil
	}
	s.detachLocked(id)
	delete(s.assets, id)
	delete(s.transfers, id)
	for commandKey, target := range s.commands {
		if target == id {
			delete(s.commands, commandKey)
		}
	}
	s.rebindAssetNameLocked(key)
	return true, nil
}

// rebindAssetNameLocked re-points the name index after a row disappears: the
// newest remaining row wins, and the entry is dropped when none is left.
func (s *CollectibleUsernameStore) rebindAssetNameLocked(key string) {
	best := int64(0)
	for id, asset := range s.assets {
		if strings.ToLower(asset.Username) != key {
			continue
		}
		if best == 0 || id > best {
			best = id
		}
	}
	if best == 0 {
		delete(s.assetsByName, key)
		return
	}
	s.assetsByName[key] = best
}

func (s *CollectibleUsernameStore) CollectibleUsername(_ context.Context, username string) (domain.CollectibleUsername, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.assetByNameLocked(domain.NormalizeUsername(username))
}

// CollectibleUsernameByID looks the asset up by identity.
func (s *CollectibleUsernameStore) CollectibleUsernameByID(_ context.Context, id int64) (domain.CollectibleUsername, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	asset, ok := s.assets[id]
	if !ok {
		return domain.CollectibleUsername{}, domain.ErrCollectibleUsernameNotFound
	}
	return asset, nil
}

// ListCollectibleUsernames is the admin listing query: newest first, paged by a
// BeforeID keyset, matching collectible_usernames_status_idx ordering.
func (s *CollectibleUsernameStore) ListCollectibleUsernames(_ context.Context, filter domain.CollectibleUsernameFilter) ([]domain.CollectibleUsername, error) {
	if filter.Status != "" && !filter.Status.Valid() {
		return nil, domain.ErrCollectibleUsernameStateInvalid
	}
	limit := filter.Limit
	if limit <= 0 {
		limit = defaultCollectibleUsernameListLimit
	}
	query := strings.ToLower(domain.NormalizeUsername(filter.Query))
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]domain.CollectibleUsername, 0, len(s.assets))
	for _, asset := range s.assets {
		if filter.Status != "" && asset.Status != filter.Status {
			continue
		}
		if filter.Owner.Type != "" && asset.Owner != filter.Owner {
			continue
		}
		if query != "" && !strings.Contains(strings.ToLower(asset.Username), query) {
			continue
		}
		if filter.BeforeID > 0 && asset.ID >= filter.BeforeID {
			continue
		}
		out = append(out, asset)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// CollectibleUsernameTransfers returns the provenance log newest first.
func (s *CollectibleUsernameStore) CollectibleUsernameTransfers(_ context.Context, collectibleID int64, limit int) ([]domain.CollectibleUsernameTransfer, error) {
	if limit <= 0 {
		limit = defaultCollectibleUsernameTransferLimit
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	stored := s.transfers[collectibleID]
	out := make([]domain.CollectibleUsernameTransfer, 0, len(stored))
	for i := len(stored) - 1; i >= 0 && len(out) < limit; i-- {
		out = append(out, stored[i])
	}
	return out, nil
}

// peerUsernamesLocked collects the peer's rows in projection order. The rows are
// values, so the returned slice cannot be used to mutate stored state.
func (s *CollectibleUsernameStore) peerUsernamesLocked(peer domain.Peer) []domain.Username {
	rows := make([]domain.Username, 0, 4)
	for _, entry := range s.registry {
		if entry.peer != peer {
			continue
		}
		rows = append(rows, entry.row)
	}
	return domain.SortUsernames(rows)
}

// countCollectiblesLocked counts the peer's collectible registry rows, which is
// what MaxPeerCollectibleUsernames bounds.
func (s *CollectibleUsernameStore) countCollectiblesLocked(peer domain.Peer) int {
	count := 0
	for _, entry := range s.registry {
		if entry.peer == peer && entry.row.Collectible() {
			count++
		}
	}
	return count
}

// nextSortOrderLocked appends the new collectible after the peer's existing ones,
// clamped by the registry sort_order CHECK.
func (s *CollectibleUsernameStore) nextSortOrderLocked(peer domain.Peer) int {
	next := 0
	for _, entry := range s.registry {
		if entry.peer != peer || !entry.row.Collectible() {
			continue
		}
		if entry.row.SortOrder >= next {
			next = entry.row.SortOrder + 1
		}
	}
	if next > domain.MaxUsernameSortOrder {
		next = domain.MaxUsernameSortOrder
	}
	return next
}

// attachLocked projects an owned asset into the registry. Callers check
// occupancy and the per-peer bound first, exactly like the PostgreSQL path does
// before it hits the unique index.
func (s *CollectibleUsernameStore) attachLocked(asset domain.CollectibleUsername, peer domain.Peer) {
	s.registry[strings.ToLower(asset.Username)] = collectibleRegistryRow{
		peer: peer,
		row: domain.Username{
			Username:      asset.Username,
			Active:        true,
			Editable:      false,
			SortOrder:     s.nextSortOrderLocked(peer),
			CollectibleID: asset.ID,
		},
	}
}

// detachLocked removes the registry row backed by the asset, leaving the peer's
// editable slot and its other collectibles untouched.
func (s *CollectibleUsernameStore) detachLocked(collectibleID int64) {
	for key, entry := range s.registry {
		if entry.row.CollectibleID == collectibleID {
			delete(s.registry, key)
			return
		}
	}
}

// clearEditableLocked drops the peer's editable row, keeping the one-editable-row
// index true by construction.
func (s *CollectibleUsernameStore) clearEditableLocked(peer domain.Peer) bool {
	for key, entry := range s.registry {
		if entry.peer == peer && entry.row.Editable {
			delete(s.registry, key)
			return true
		}
	}
	return false
}

func (s *CollectibleUsernameStore) assetByNameLocked(username string) (domain.CollectibleUsername, error) {
	if username == "" {
		return domain.CollectibleUsername{}, domain.ErrCollectibleUsernameNotFound
	}
	id, ok := s.assetsByName[strings.ToLower(username)]
	if !ok {
		return domain.CollectibleUsername{}, domain.ErrCollectibleUsernameNotFound
	}
	asset, ok := s.assets[id]
	if !ok {
		return domain.CollectibleUsername{}, domain.ErrCollectibleUsernameNotFound
	}
	return asset, nil
}

// replayLocked resolves a command key onto the asset the recorded command
// touched. The provenance command index is global, so a replayed key is a no-op
// for every kind, just like the INSERT ... ON CONFLICT DO NOTHING path.
func (s *CollectibleUsernameStore) replayLocked(commandKey string) (domain.CollectibleUsername, bool) {
	if commandKey == "" {
		return domain.CollectibleUsername{}, false
	}
	id, ok := s.commands[commandKey]
	if !ok {
		return domain.CollectibleUsername{}, false
	}
	asset, ok := s.assets[id]
	return asset, ok
}

func (s *CollectibleUsernameStore) recordTransferLocked(entry domain.CollectibleUsernameTransfer) {
	entry.ID = s.nextTransferID
	s.nextTransferID++
	s.transfers[entry.CollectibleID] = append(s.transfers[entry.CollectibleID], entry)
	if entry.CommandKey != "" {
		s.commands[entry.CommandKey] = entry.CollectibleID
	}
}

// validCollectibleUsernamePeer mirrors the peer_type CHECK: only real user and
// channel peers can hold a username.
func validCollectibleUsernamePeer(peer domain.Peer) bool {
	switch peer.Type {
	case domain.PeerTypeUser, domain.PeerTypeChannel:
		return peer.ID > 0
	default:
		return false
	}
}
