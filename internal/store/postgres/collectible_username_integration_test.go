package postgres

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"telesrv/internal/domain"
)

// collectibleTestUser inserts a bare user row and returns its user peer. The
// registry rows for collectibles reference no peer table, but the editable-slot
// assertions and the peer-deletion trigger both need a real user.
func collectibleTestUser(t *testing.T, pool *pgxpool.Pool, seed int64, username string) domain.Peer {
	t.Helper()
	ctx := context.Background()
	var id int64
	if err := pool.QueryRow(ctx, `
INSERT INTO users (access_hash, phone, first_name, username)
VALUES ($1, $2, 'collectible test', $3)
RETURNING id`, seed, fmt.Sprintf("%d", seed), username).Scan(&id); err != nil {
		t.Fatalf("insert collectible test user: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, id)
	})
	return domain.Peer{Type: domain.PeerTypeUser, ID: id}
}

// setEditableUsername installs an editable registry row through the same helper
// the client-driven username path uses.
func setEditableUsername(t *testing.T, pool *pgxpool.Pool, peer domain.Peer, username string) {
	t.Helper()
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin editable username: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := replacePeerUsernameTx(ctx, tx, peerUsernameTypeUser, peer.ID, username, lowerASCII(username)); err != nil {
		t.Fatalf("set editable username %q: %v", username, err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit editable username: %v", err)
	}
}

func lowerASCII(s string) string {
	out := []byte(s)
	for i := range out {
		if out[i] >= 'A' && out[i] <= 'Z' {
			out[i] += 'a' - 'A'
		}
	}
	return string(out)
}

func cleanupCollectible(t *testing.T, pool *pgxpool.Pool, usernameLower string) {
	t.Helper()
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM collectible_usernames WHERE username_lower = $1`, usernameLower)
	})
}

func mintRequest(username string, owner domain.Peer, commandKey string) domain.MintCollectibleUsernameRequest {
	return domain.MintCollectibleUsernameRequest{
		Username:     username,
		Owner:        owner,
		PurchaseDate: time.Now().UTC().Truncate(time.Second),
		Currency:     domain.CollectibleCurrencyStars,
		Amount:       5000,
		URL:          "https://fragment.example/" + username,
		Actor:        "ops",
		Reason:       "integration test",
		CommandKey:   commandKey,
	}
}

func registryRows(t *testing.T, pool *pgxpool.Pool, peer domain.Peer) []domain.Username {
	t.Helper()
	list, err := listPeerUsernames(context.Background(), pool, peer)
	if err != nil {
		t.Fatalf("list peer usernames: %v", err)
	}
	return list
}

// TestCollectibleUsernameMintIntoVault covers a vault mint: the asset exists, the
// name is not projected into any peer's registry, and the provenance log records
// the mint.
func TestCollectibleUsernameMintIntoVault(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	store := NewCollectibleUsernameStore(pool)
	name := fmt.Sprintf("vault%d", time.Now().UnixNano()%1_000_000)
	cleanupCollectible(t, pool, lowerASCII(name))

	asset, created, err := store.MintCollectibleUsername(ctx, mintRequest(name, domain.Peer{}, ""))
	if err != nil || !created {
		t.Fatalf("mint into vault created=%v err=%v", created, err)
	}
	if asset.Status != domain.CollectibleUsernameStatusVault || asset.Owned() ||
		asset.Version != 1 || asset.TransferCount != 0 || asset.Username != name {
		t.Fatalf("vault asset = %+v", asset)
	}
	if err := asset.Validate(); err != nil {
		t.Fatalf("vault asset invariants: %v", err)
	}
	var registry int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM peer_usernames WHERE collectible_id = $1`, asset.ID).Scan(&registry); err != nil {
		t.Fatal(err)
	}
	if registry != 0 {
		t.Fatalf("vault asset must not be projected, got %d registry rows", registry)
	}
	transfers, err := store.CollectibleUsernameTransfers(ctx, asset.ID, 10)
	if err != nil || len(transfers) != 1 || transfers[0].Kind != domain.CollectibleUsernameKindMint {
		t.Fatalf("transfers=%+v err=%v", transfers, err)
	}
	// A vault revoke has nothing to release and must stay a no-op.
	same, changed, err := store.RevokeCollectibleUsername(ctx, domain.RevokeCollectibleUsernameRequest{Username: name})
	if err != nil || changed || same.Version != asset.Version {
		t.Fatalf("vault revoke changed=%v asset=%+v err=%v", changed, same, err)
	}
}

// TestCollectibleUsernameMintWithOwnerAndReplay covers a mint that assigns the
// asset immediately, and the command-key replay that must return the recorded
// state instead of minting twice.
func TestCollectibleUsernameMintWithOwnerAndReplay(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	store := NewCollectibleUsernameStore(pool)
	seed := time.Now().UnixNano() % 1_000_000
	owner := collectibleTestUser(t, pool, 2_100_000_000+seed, "")
	name := fmt.Sprintf("owned%d", seed)
	cleanupCollectible(t, pool, lowerASCII(name))
	key := fmt.Sprintf("mint-%d", seed)

	asset, created, err := store.MintCollectibleUsername(ctx, mintRequest(name, owner, key))
	if err != nil || !created {
		t.Fatalf("mint with owner created=%v err=%v", created, err)
	}
	if asset.Status != domain.CollectibleUsernameStatusOwned || asset.Owner != owner ||
		asset.OriginalOwner != owner || asset.TransferCount != 0 {
		t.Fatalf("owned asset = %+v", asset)
	}
	if err := asset.Validate(); err != nil {
		t.Fatalf("owned asset invariants: %v", err)
	}
	list := registryRows(t, pool, owner)
	if len(list) != 1 || list[0].Username != name || list[0].Editable ||
		!list[0].Active || list[0].CollectibleID != asset.ID {
		t.Fatalf("registry = %+v", list)
	}

	replay, created, err := store.MintCollectibleUsername(ctx, mintRequest(name, owner, key))
	if err != nil || created || replay.ID != asset.ID || replay.Version != asset.Version {
		t.Fatalf("replay created=%v asset=%+v err=%v", created, replay, err)
	}
	var assets int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM collectible_usernames WHERE username_lower = $1`, lowerASCII(name)).Scan(&assets); err != nil {
		t.Fatal(err)
	}
	if assets != 1 {
		t.Fatalf("replay must not mint again, got %d assets", assets)
	}

	// The same name cannot be minted twice, even under a different command key.
	if _, _, err := store.MintCollectibleUsername(ctx, mintRequest(name, domain.Peer{}, key+"-again")); !errors.Is(err, domain.ErrUsernameOccupied) {
		t.Fatalf("duplicate mint err = %v, want ErrUsernameOccupied", err)
	}
}

// TestCollectibleUsernameMintRejectsOccupiedEditableName proves the collectible
// registry and the editable slot share one occupancy namespace.
func TestCollectibleUsernameMintRejectsOccupiedEditableName(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	store := NewCollectibleUsernameStore(pool)
	seed := time.Now().UnixNano() % 1_000_000
	holder := collectibleTestUser(t, pool, 2_200_000_000+seed, "")
	name := fmt.Sprintf("taken%d", seed)
	setEditableUsername(t, pool, holder, name)
	cleanupCollectible(t, pool, lowerASCII(name))

	if _, _, err := store.MintCollectibleUsername(ctx, mintRequest(name, domain.Peer{}, "")); !errors.Is(err, domain.ErrUsernameOccupied) {
		t.Fatalf("mint over editable name err = %v, want ErrUsernameOccupied", err)
	}
	if _, err := store.CollectibleUsername(ctx, name); !errors.Is(err, domain.ErrCollectibleUsernameNotFound) {
		t.Fatalf("rejected mint must leave no asset, err = %v", err)
	}
}

// TestCollectibleUsernameTransferPreservesRecipientEditableSlot is the core
// regression: moving an asset must not disturb either peer's editable username.
func TestCollectibleUsernameTransferPreservesRecipientEditableSlot(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	store := NewCollectibleUsernameStore(pool)
	seed := time.Now().UnixNano() % 1_000_000
	from := collectibleTestUser(t, pool, 2_300_000_000+seed, "")
	to := collectibleTestUser(t, pool, 2_400_000_000+seed, "")
	fromEditable := fmt.Sprintf("sender%d", seed)
	toEditable := fmt.Sprintf("recip%d", seed)
	setEditableUsername(t, pool, from, fromEditable)
	setEditableUsername(t, pool, to, toEditable)
	name := fmt.Sprintf("moved%d", seed)
	cleanupCollectible(t, pool, lowerASCII(name))

	asset, _, err := store.MintCollectibleUsername(ctx, mintRequest(name, from, ""))
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	key := fmt.Sprintf("transfer-%d", seed)
	moved, changed, err := store.TransferCollectibleUsername(ctx, domain.TransferCollectibleUsernameRequest{
		Username: name, To: to, Actor: "ops", Reason: "sold", CommandKey: key,
	})
	if err != nil || !changed {
		t.Fatalf("transfer changed=%v err=%v", changed, err)
	}
	if moved.Owner != to || moved.OriginalOwner != from || moved.TransferCount != 1 ||
		moved.Version != asset.Version+1 {
		t.Fatalf("transferred asset = %+v", moved)
	}

	fromList := registryRows(t, pool, from)
	if len(fromList) != 1 || fromList[0].Username != fromEditable || !fromList[0].Editable {
		t.Fatalf("sender registry = %+v, editable slot must survive", fromList)
	}
	toList := registryRows(t, pool, to)
	if len(toList) != 2 || toList[0].Username != toEditable || !toList[0].Editable ||
		toList[1].Username != name || !toList[1].Collectible() {
		t.Fatalf("recipient registry = %+v", toList)
	}

	replay, changed, err := store.TransferCollectibleUsername(ctx, domain.TransferCollectibleUsernameRequest{
		Username: name, To: to, CommandKey: key,
	})
	if err != nil || changed || replay.Version != moved.Version {
		t.Fatalf("transfer replay changed=%v asset=%+v err=%v", changed, replay, err)
	}

	// Revoking back to the vault releases the recipient's registry row only.
	vaulted, changed, err := store.RevokeCollectibleUsername(ctx, domain.RevokeCollectibleUsernameRequest{
		Username: name, Actor: "ops", Reason: "recalled", CommandKey: key + "-revoke",
	})
	if err != nil || !changed {
		t.Fatalf("revoke changed=%v err=%v", changed, err)
	}
	if vaulted.Status != domain.CollectibleUsernameStatusVault || vaulted.Owned() ||
		vaulted.OriginalOwner != from {
		t.Fatalf("revoked asset = %+v", vaulted)
	}
	toList = registryRows(t, pool, to)
	if len(toList) != 1 || toList[0].Username != toEditable {
		t.Fatalf("recipient registry after revoke = %+v", toList)
	}
}

// TestCollectibleUsernameBurnReleasesName covers a burn: the asset is retired and
// the name stops resolving, so an ordinary peer can claim it as its editable slot.
func TestCollectibleUsernameBurnReleasesName(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	store := NewCollectibleUsernameStore(pool)
	seed := time.Now().UnixNano() % 1_000_000
	holder := collectibleTestUser(t, pool, 2_500_000_000+seed, "")
	claimer := collectibleTestUser(t, pool, 2_600_000_000+seed, "")
	name := fmt.Sprintf("burned%d", seed)
	cleanupCollectible(t, pool, lowerASCII(name))

	if _, _, err := store.MintCollectibleUsername(ctx, mintRequest(name, holder, "")); err != nil {
		t.Fatalf("mint: %v", err)
	}
	burned, changed, err := store.RevokeCollectibleUsername(ctx, domain.RevokeCollectibleUsernameRequest{
		Username: name, Burn: true, Actor: "ops", Reason: "abuse",
	})
	if err != nil || !changed {
		t.Fatalf("burn changed=%v err=%v", changed, err)
	}
	if burned.Status != domain.CollectibleUsernameStatusBurned || burned.Owned() {
		t.Fatalf("burned asset = %+v", burned)
	}
	if list := registryRows(t, pool, holder); len(list) != 0 {
		t.Fatalf("holder registry after burn = %+v", list)
	}
	// The freed name is claimable as an ordinary editable username.
	setEditableUsername(t, pool, claimer, name)
	if list := registryRows(t, pool, claimer); len(list) != 1 || !list[0].Editable || list[0].Username != name {
		t.Fatalf("claimer registry = %+v", list)
	}
	// Every further mutation of a burned asset is refused.
	if _, _, err := store.TransferCollectibleUsername(ctx, domain.TransferCollectibleUsernameRequest{
		Username: name, To: holder,
	}); !errors.Is(err, domain.ErrCollectibleUsernameBurned) {
		t.Fatalf("transfer of burned asset err = %v, want ErrCollectibleUsernameBurned", err)
	}
	if _, _, err := store.RevokeCollectibleUsername(ctx, domain.RevokeCollectibleUsernameRequest{
		Username: name, Burn: true,
	}); !errors.Is(err, domain.ErrCollectibleUsernameBurned) {
		t.Fatalf("re-burn err = %v, want ErrCollectibleUsernameBurned", err)
	}
	if _, _, err := store.TransferCollectibleUsername(ctx, domain.TransferCollectibleUsernameRequest{
		Username: fmt.Sprintf("ghost%d", seed), To: holder,
	}); !errors.Is(err, domain.ErrCollectibleUsernameNotFound) {
		t.Fatalf("transfer of unknown asset err = %v, want ErrCollectibleUsernameNotFound", err)
	}
}

// TestCollectibleUsernamePeerLimit covers the per-peer bound on both entry paths:
// minting straight to the holder and transferring into it.
func TestCollectibleUsernamePeerLimit(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	store := NewCollectibleUsernameStore(pool)
	seed := time.Now().UnixNano() % 1_000_000
	holder := collectibleTestUser(t, pool, 2_700_000_000+seed, "")
	prefix := fmt.Sprintf("lim%d", seed)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM collectible_usernames WHERE username_lower LIKE $1 || '%'`, lowerASCII(prefix))
	})
	for i := 0; i < domain.MaxPeerCollectibleUsernames; i++ {
		name := fmt.Sprintf("%sn%02d", prefix, i)
		if _, _, err := store.MintCollectibleUsername(ctx, mintRequest(name, holder, "")); err != nil {
			t.Fatalf("mint %s: %v", name, err)
		}
	}
	if list := registryRows(t, pool, holder); len(list) != domain.MaxPeerCollectibleUsernames {
		t.Fatalf("registry size = %d", len(list))
	}
	overflow := prefix + "over"
	if _, _, err := store.MintCollectibleUsername(ctx, mintRequest(overflow, holder, "")); !errors.Is(err, domain.ErrCollectibleUsernameLimit) {
		t.Fatalf("mint over limit err = %v, want ErrCollectibleUsernameLimit", err)
	}
	if _, err := store.CollectibleUsername(ctx, overflow); !errors.Is(err, domain.ErrCollectibleUsernameNotFound) {
		t.Fatalf("rejected mint must not leave an asset: %v", err)
	}
	vaulted, _, err := store.MintCollectibleUsername(ctx, mintRequest(prefix+"vault", domain.Peer{}, ""))
	if err != nil {
		t.Fatalf("mint vault asset: %v", err)
	}
	if _, _, err := store.TransferCollectibleUsername(ctx, domain.TransferCollectibleUsernameRequest{
		Username: vaulted.Username, To: holder,
	}); !errors.Is(err, domain.ErrCollectibleUsernameLimit) {
		t.Fatalf("transfer over limit err = %v, want ErrCollectibleUsernameLimit", err)
	}
	// The refused transfer must not have released the asset from the vault.
	after, err := store.CollectibleUsername(ctx, vaulted.Username)
	if err != nil || after.Status != domain.CollectibleUsernameStatusVault {
		t.Fatalf("vault asset after refused transfer = %+v err=%v", after, err)
	}
	owned, err := store.ListCollectibleUsernames(ctx, domain.CollectibleUsernameFilter{
		Owner: holder, Status: domain.CollectibleUsernameStatusOwned, Limit: 100,
	})
	if err != nil || len(owned) != domain.MaxPeerCollectibleUsernames {
		t.Fatalf("list owned = %d err=%v", len(owned), err)
	}
	prefixed, err := store.ListCollectibleUsernames(ctx, domain.CollectibleUsernameFilter{
		Query: prefix + "n0", Limit: 100,
	})
	if err != nil || len(prefixed) != 10 {
		t.Fatalf("list by prefix = %d err=%v", len(prefixed), err)
	}
}

// TestCollectibleUsernameRegistryToggleAndReorder covers the registry-only
// surface: activation, ordering and the bulk deactivation, none of which may
// touch the editable slot.
func TestCollectibleUsernameRegistryToggleAndReorder(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	store := NewCollectibleUsernameStore(pool)
	seed := time.Now().UnixNano() % 1_000_000
	holder := collectibleTestUser(t, pool, 2_800_000_000+seed, "")
	editable := fmt.Sprintf("edit%d", seed)
	setEditableUsername(t, pool, holder, editable)
	first := fmt.Sprintf("alpha%d", seed)
	second := fmt.Sprintf("beta%d", seed)
	cleanupCollectible(t, pool, lowerASCII(first))
	cleanupCollectible(t, pool, lowerASCII(second))
	for _, name := range []string{first, second} {
		if _, _, err := store.MintCollectibleUsername(ctx, mintRequest(name, holder, "")); err != nil {
			t.Fatalf("mint %s: %v", name, err)
		}
	}
	changed, err := store.SetUsernameActive(ctx, holder, first, false)
	if err != nil || !changed {
		t.Fatalf("deactivate collectible changed=%v err=%v", changed, err)
	}
	if _, err := store.SetUsernameActive(ctx, holder, editable, false); !errors.Is(err, domain.ErrUsernameNotCollectible) {
		t.Fatalf("toggling the editable slot err = %v, want ErrUsernameNotCollectible", err)
	}
	changed, err = store.ReorderUsernames(ctx, holder, []string{second, first})
	if err != nil || !changed {
		t.Fatalf("reorder changed=%v err=%v", changed, err)
	}
	list := registryRows(t, pool, holder)
	if len(list) != 3 || list[0].Username != editable || list[1].Username != second || list[2].Username != first {
		t.Fatalf("registry order = %+v", list)
	}
	if _, err := store.ReorderUsernames(ctx, holder, []string{second}); !errors.Is(err, domain.ErrUsernameOrderInvalid) {
		t.Fatalf("partial reorder err = %v, want ErrUsernameOrderInvalid", err)
	}
	changed, err = store.DeactivateAllUsernames(ctx, holder)
	if err != nil || !changed {
		t.Fatalf("deactivate all changed=%v err=%v", changed, err)
	}
	list = registryRows(t, pool, holder)
	if len(list) != 3 || !list[0].Active || list[1].Active || list[2].Active {
		t.Fatalf("registry after deactivate all = %+v", list)
	}
	batch, err := store.PeerUsernamesBatch(ctx, []domain.Peer{holder, {Type: domain.PeerTypeUser, ID: holder.ID + 1}})
	if err != nil || len(batch[holder]) != 3 {
		t.Fatalf("batch = %+v err=%v", batch, err)
	}
	if domain.ActiveUsername(batch[holder]) != editable {
		t.Fatalf("active username = %q", domain.ActiveUsername(batch[holder]))
	}
}

// TestCollectibleUsernameEditableEditKeepsCollectibles pins the peer_username.go
// surgery: rewriting or clearing the editable slot must leave collectible rows
// and their assets untouched.
func TestCollectibleUsernameEditableEditKeepsCollectibles(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	store := NewCollectibleUsernameStore(pool)
	seed := time.Now().UnixNano() % 1_000_000
	holder := collectibleTestUser(t, pool, 2_900_000_000+seed, "")
	name := fmt.Sprintf("keep%d", seed)
	cleanupCollectible(t, pool, lowerASCII(name))
	asset, _, err := store.MintCollectibleUsername(ctx, mintRequest(name, holder, ""))
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	setEditableUsername(t, pool, holder, fmt.Sprintf("one%d", seed))
	setEditableUsername(t, pool, holder, fmt.Sprintf("two%d", seed))
	setEditableUsername(t, pool, holder, "")
	list := registryRows(t, pool, holder)
	if len(list) != 1 || list[0].CollectibleID != asset.ID {
		t.Fatalf("registry after editable churn = %+v", list)
	}
	stored, err := store.CollectibleUsernameByID(ctx, asset.ID)
	if err != nil || stored.Owner != holder {
		t.Fatalf("asset after editable churn = %+v err=%v", stored, err)
	}
	// The editable slot may not duplicate a name the peer holds as a collectible.
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := replacePeerUsernameTx(ctx, tx, peerUsernameTypeUser, holder.ID, name, lowerASCII(name)); !errors.Is(err, domain.ErrUsernameOccupied) {
		t.Fatalf("editable slot over own collectible err = %v, want ErrUsernameOccupied", err)
	}
}
