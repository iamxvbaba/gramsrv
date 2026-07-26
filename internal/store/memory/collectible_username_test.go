package memory

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

var (
	_ store.UsernameRegistryStore    = (*CollectibleUsernameStore)(nil)
	_ store.CollectibleUsernameStore = (*CollectibleUsernameStore)(nil)
)

func collectibleMintRequest(username string, owner domain.Peer, commandKey string) domain.MintCollectibleUsernameRequest {
	return domain.MintCollectibleUsernameRequest{
		Username:     username,
		Owner:        owner,
		PurchaseDate: time.Unix(1700000000, 0).UTC(),
		Currency:     domain.CollectibleCurrencyStars,
		Amount:       2500,
		Actor:        "admin",
		Reason:       "unit test",
		CommandKey:   commandKey,
	}
}

func mustMintCollectible(t *testing.T, s *CollectibleUsernameStore, username string, owner domain.Peer) domain.CollectibleUsername {
	t.Helper()
	asset, created, err := s.MintCollectibleUsername(context.Background(),
		collectibleMintRequest(username, owner, "mint-"+username))
	if err != nil || !created {
		t.Fatalf("mint %s: created=%v err=%v", username, created, err)
	}
	return asset
}

func mustSetEditable(t *testing.T, s *CollectibleUsernameStore, peer domain.Peer, username string) {
	t.Helper()
	changed, err := s.SetEditableUsername(context.Background(), peer, username)
	if err != nil || !changed {
		t.Fatalf("set editable %s: changed=%v err=%v", username, changed, err)
	}
}

// usernameRow finds a row by name the way the registry keys it: case-insensitively.
func usernameRow(t *testing.T, rows []domain.Username, username string) domain.Username {
	t.Helper()
	want := domain.NormalizeUsername(username)
	for _, row := range rows {
		if strings.EqualFold(row.Username, want) {
			return row
		}
	}
	t.Fatalf("username %q missing from %+v", username, rows)
	return domain.Username{}
}

func TestCollectibleUsernameMint(t *testing.T) {
	ctx := context.Background()
	holder := domain.Peer{Type: domain.PeerTypeUser, ID: 1001}
	channel := domain.Peer{Type: domain.PeerTypeChannel, ID: 2002}

	tests := []struct {
		name        string
		seed        func(t *testing.T, s *CollectibleUsernameStore)
		req         domain.MintCollectibleUsernameRequest
		wantErr     error
		wantCreated bool
		check       func(t *testing.T, s *CollectibleUsernameStore, asset domain.CollectibleUsername)
	}{
		{
			name:        "into vault",
			req:         collectibleMintRequest("vaultname", domain.Peer{}, "cmd-vault"),
			wantCreated: true,
			check: func(t *testing.T, s *CollectibleUsernameStore, asset domain.CollectibleUsername) {
				if asset.Status != domain.CollectibleUsernameStatusVault || asset.Owned() {
					t.Fatalf("asset=%+v", asset)
				}
				if asset.Owner != (domain.Peer{}) || asset.OriginalOwner != (domain.Peer{}) {
					t.Fatalf("vault asset carries an owner: %+v", asset)
				}
				if asset.Version != 1 || asset.TransferCount != 0 {
					t.Fatalf("asset=%+v", asset)
				}
				rows, err := s.PeerUsernames(ctx, holder)
				if err != nil || len(rows) != 0 {
					t.Fatalf("rows=%+v err=%v", rows, err)
				}
			},
		},
		{
			name:        "with owner",
			req:         collectibleMintRequest("OwnedName", holder, "cmd-owned"),
			wantCreated: true,
			check: func(t *testing.T, s *CollectibleUsernameStore, asset domain.CollectibleUsername) {
				if asset.Status != domain.CollectibleUsernameStatusOwned || !asset.Owned() {
					t.Fatalf("asset=%+v", asset)
				}
				if asset.Owner != holder || asset.OriginalOwner != holder {
					t.Fatalf("asset=%+v", asset)
				}
				rows, err := s.PeerUsernames(ctx, holder)
				if err != nil || len(rows) != 1 {
					t.Fatalf("rows=%+v err=%v", rows, err)
				}
				row := rows[0]
				if row.Username != "OwnedName" || !row.Active || row.Editable ||
					row.CollectibleID != asset.ID {
					t.Fatalf("row=%+v", row)
				}
			},
		},
		{
			name: "channel owner",
			req:  collectibleMintRequest("chanpost", channel, "cmd-chan"),

			wantCreated: true,
			check: func(t *testing.T, s *CollectibleUsernameStore, asset domain.CollectibleUsername) {
				rows, err := s.PeerUsernames(ctx, channel)
				if err != nil || len(rows) != 1 || rows[0].CollectibleID != asset.ID {
					t.Fatalf("rows=%+v err=%v", rows, err)
				}
			},
		},
		{
			name: "command key replay",
			seed: func(t *testing.T, s *CollectibleUsernameStore) {
				if _, created, err := s.MintCollectibleUsername(ctx,
					collectibleMintRequest("replayed", holder, "cmd-replay")); err != nil || !created {
					t.Fatalf("seed mint created=%v err=%v", created, err)
				}
			},
			req:         collectibleMintRequest("replayed", holder, "cmd-replay"),
			wantCreated: false,
			check: func(t *testing.T, s *CollectibleUsernameStore, asset domain.CollectibleUsername) {
				if asset.Username != "replayed" || asset.ID != 1 {
					t.Fatalf("replay returned %+v", asset)
				}
				log, err := s.CollectibleUsernameTransfers(ctx, asset.ID, 10)
				if err != nil || len(log) != 1 || log[0].Kind != domain.CollectibleUsernameKindMint {
					t.Fatalf("replay appended provenance: %+v err=%v", log, err)
				}
			},
		},
		{
			name: "name held by another asset",
			seed: func(t *testing.T, s *CollectibleUsernameStore) {
				mustMintCollectible(t, s, "TakenName", holder)
			},
			req:     collectibleMintRequest("takenname", channel, "cmd-dup"),
			wantErr: domain.ErrUsernameOccupied,
		},
		{
			name: "name held by an editable slot",
			seed: func(t *testing.T, s *CollectibleUsernameStore) {
				mustSetEditable(t, s, holder, "EditableOne")
			},
			req:     collectibleMintRequest("editableone", channel, "cmd-editable"),
			wantErr: domain.ErrUsernameOccupied,
		},
		{
			name:    "syntactically invalid",
			req:     collectibleMintRequest("ab", holder, "cmd-short"),
			wantErr: domain.ErrUsernameInvalid,
		},
		{
			name: "crypto pair without amount",
			req: func() domain.MintCollectibleUsernameRequest {
				req := collectibleMintRequest("cryptoname", holder, "cmd-crypto")
				req.CryptoCurrency = domain.CollectibleCryptoCurrencyTON
				return req
			}(),
			wantErr: domain.ErrCollectibleCurrencyInvalid,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := NewCollectibleUsernameStore()
			if tc.seed != nil {
				tc.seed(t, s)
			}
			asset, created, err := s.MintCollectibleUsername(ctx, tc.req)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err=%v want %v", err, tc.wantErr)
			}
			if created != tc.wantCreated {
				t.Fatalf("created=%v want %v", created, tc.wantCreated)
			}
			if tc.wantErr != nil {
				return
			}
			if tc.check != nil {
				tc.check(t, s, asset)
			}
		})
	}
}

func TestCollectibleUsernamePeerLimit(t *testing.T) {
	ctx := context.Background()
	s := NewCollectibleUsernameStore()
	holder := domain.Peer{Type: domain.PeerTypeUser, ID: 1001}
	other := domain.Peer{Type: domain.PeerTypeUser, ID: 1002}
	for i := 0; i < domain.MaxPeerCollectibleUsernames; i++ {
		mustMintCollectible(t, s, fmt.Sprintf("holder%04d", i), holder)
	}
	if _, _, err := s.MintCollectibleUsername(ctx,
		collectibleMintRequest("overflow", holder, "cmd-overflow")); !errors.Is(err, domain.ErrCollectibleUsernameLimit) {
		t.Fatalf("mint over the limit err=%v", err)
	}
	// The rejected mint left no asset behind, so the name is still free.
	if _, err := s.CollectibleUsername(ctx, "overflow"); !errors.Is(err, domain.ErrCollectibleUsernameNotFound) {
		t.Fatalf("rejected mint stored an asset: %v", err)
	}
	spare := mustMintCollectible(t, s, "sparename", other)
	if _, _, err := s.TransferCollectibleUsername(ctx, domain.TransferCollectibleUsernameRequest{
		Username: spare.Username, To: holder, Actor: "admin", CommandKey: "cmd-limit-transfer",
	}); !errors.Is(err, domain.ErrCollectibleUsernameLimit) {
		t.Fatalf("transfer over the limit err=%v", err)
	}
	rows, err := s.PeerUsernames(ctx, holder)
	if err != nil || len(rows) != domain.MaxPeerCollectibleUsernames {
		t.Fatalf("rows=%d err=%v", len(rows), err)
	}
	// The failed transfer did not move the asset either.
	stored, err := s.CollectibleUsername(ctx, "sparename")
	if err != nil || stored.Owner != other {
		t.Fatalf("stored=%+v err=%v", stored, err)
	}
}

func TestCollectibleUsernameTransfer(t *testing.T) {
	ctx := context.Background()
	s := NewCollectibleUsernameStore()
	holder := domain.Peer{Type: domain.PeerTypeUser, ID: 1001}
	other := domain.Peer{Type: domain.PeerTypeUser, ID: 1002}
	mustSetEditable(t, s, holder, "holderslot")
	owned := mustMintCollectible(t, s, "alphaone", holder)
	vaulted := mustMintCollectible(t, s, "vaultone", domain.Peer{})

	// Out of the vault: the asset gains a holder and its first original owner.
	moved, changed, err := s.TransferCollectibleUsername(ctx, domain.TransferCollectibleUsernameRequest{
		Username: "VaultOne", To: holder, Actor: "admin", CommandKey: "cmd-vault-out",
	})
	if err != nil || !changed {
		t.Fatalf("transfer out of vault changed=%v err=%v", changed, err)
	}
	if moved.ID != vaulted.ID || moved.Owner != holder || moved.OriginalOwner != holder ||
		moved.TransferCount != 1 || moved.Version != vaulted.Version+1 {
		t.Fatalf("moved=%+v", moved)
	}

	rows, err := s.PeerUsernames(ctx, holder)
	if err != nil || len(rows) != 3 {
		t.Fatalf("rows=%+v err=%v", rows, err)
	}
	// The editable slot stays first, untouched and editable.
	if rows[0].Username != "holderslot" || !rows[0].Editable || !rows[0].Active ||
		rows[0].CollectibleID != 0 {
		t.Fatalf("editable row=%+v", rows[0])
	}
	if rows[1].Username != "alphaone" || rows[2].Username != "vaultone" {
		t.Fatalf("collectible order=%+v", rows)
	}

	// Replaying the command key is a no-op.
	replay, changed, err := s.TransferCollectibleUsername(ctx, domain.TransferCollectibleUsernameRequest{
		Username: "vaultone", To: other, Actor: "admin", CommandKey: "cmd-vault-out",
	})
	if err != nil || changed || replay.Owner != holder {
		t.Fatalf("replay=%+v changed=%v err=%v", replay, changed, err)
	}

	// Handing an asset to its current holder changes nothing.
	same, changed, err := s.TransferCollectibleUsername(ctx, domain.TransferCollectibleUsernameRequest{
		Username: "alphaone", To: holder, Actor: "admin", CommandKey: "cmd-noop",
	})
	if err != nil || changed || same.Version != owned.Version {
		t.Fatalf("no-op transfer=%+v changed=%v err=%v", same, changed, err)
	}

	// Between peers: the previous holder loses the registry row, the editable
	// slot survives, and the original owner is preserved.
	handed, changed, err := s.TransferCollectibleUsername(ctx, domain.TransferCollectibleUsernameRequest{
		Username: "alphaone", To: other, Actor: "admin", CommandKey: "cmd-handover",
	})
	if err != nil || !changed {
		t.Fatalf("handover changed=%v err=%v", changed, err)
	}
	if handed.Owner != other || handed.OriginalOwner != holder || handed.TransferCount != 1 {
		t.Fatalf("handed=%+v", handed)
	}
	rows, err = s.PeerUsernames(ctx, holder)
	if err != nil || len(rows) != 2 {
		t.Fatalf("rows=%+v err=%v", rows, err)
	}
	if usernameRow(t, rows, "holderslot").Editable != true {
		t.Fatalf("editable slot lost: %+v", rows)
	}
	for _, row := range rows {
		if row.Username == "alphaone" {
			t.Fatalf("old owner kept the registry row: %+v", rows)
		}
	}
	batch, err := s.PeerUsernamesBatch(ctx, []domain.Peer{holder, other, {Type: domain.PeerTypeUser, ID: 9}})
	if err != nil || len(batch) != 2 {
		t.Fatalf("batch=%+v err=%v", batch, err)
	}
	if len(batch[other]) != 1 || batch[other][0].Username != "alphaone" ||
		batch[other][0].SortOrder != 0 {
		t.Fatalf("new owner rows=%+v", batch[other])
	}

	if _, _, err := s.TransferCollectibleUsername(ctx, domain.TransferCollectibleUsernameRequest{
		Username: "missingone", To: other, Actor: "admin",
	}); !errors.Is(err, domain.ErrCollectibleUsernameNotFound) {
		t.Fatalf("transfer of unknown asset err=%v", err)
	}

	log, err := s.CollectibleUsernameTransfers(ctx, owned.ID, 10)
	if err != nil || len(log) != 2 {
		t.Fatalf("provenance=%+v err=%v", log, err)
	}
	if log[0].Kind != domain.CollectibleUsernameKindTransfer || log[0].From != holder || log[0].To != other {
		t.Fatalf("newest provenance=%+v", log[0])
	}
	if log[1].Kind != domain.CollectibleUsernameKindMint || log[1].To != holder {
		t.Fatalf("oldest provenance=%+v", log[1])
	}
}

func TestCollectibleUsernameRevokeAndBurn(t *testing.T) {
	ctx := context.Background()
	s := NewCollectibleUsernameStore()
	holder := domain.Peer{Type: domain.PeerTypeUser, ID: 1001}
	other := domain.Peer{Type: domain.PeerTypeUser, ID: 1002}
	revoked := mustMintCollectible(t, s, "revokeme", holder)
	burned := mustMintCollectible(t, s, "burnme", holder)

	asset, changed, err := s.RevokeCollectibleUsername(ctx, domain.RevokeCollectibleUsernameRequest{
		Username: "RevokeMe", Actor: "admin", Reason: "abuse", CommandKey: "cmd-revoke",
	})
	if err != nil || !changed {
		t.Fatalf("revoke changed=%v err=%v", changed, err)
	}
	if asset.Status != domain.CollectibleUsernameStatusVault || asset.Owned() ||
		asset.Owner != (domain.Peer{}) || asset.OriginalOwner != holder ||
		asset.Version != revoked.Version+1 {
		t.Fatalf("revoked asset=%+v", asset)
	}
	rows, err := s.PeerUsernames(ctx, holder)
	if err != nil || len(rows) != 1 || rows[0].Username != "burnme" {
		t.Fatalf("rows=%+v err=%v", rows, err)
	}
	// Back in the vault the asset still owns its name: nobody else can take it.
	if _, err := s.SetEditableUsername(ctx, other, "revokeme"); !errors.Is(err, domain.ErrUsernameOccupied) {
		t.Fatalf("revoked name became claimable: %v", err)
	}
	if _, _, err := s.MintCollectibleUsername(ctx,
		collectibleMintRequest("revokeme", other, "cmd-remint")); !errors.Is(err, domain.ErrUsernameOccupied) {
		t.Fatalf("revoked name was re-minted: %v", err)
	}
	// Replay and a second revoke of a vault asset.
	if replay, changed, err := s.RevokeCollectibleUsername(ctx, domain.RevokeCollectibleUsernameRequest{
		Username: "revokeme", Actor: "admin", CommandKey: "cmd-revoke",
	}); err != nil || changed || replay.Version != asset.Version {
		t.Fatalf("replay=%+v changed=%v err=%v", replay, changed, err)
	}
	if _, _, err := s.RevokeCollectibleUsername(ctx, domain.RevokeCollectibleUsernameRequest{
		Username: "revokeme", Actor: "admin", CommandKey: "cmd-revoke-again",
	}); !errors.Is(err, domain.ErrCollectibleUsernameNotOwned) {
		t.Fatalf("revoke of an unowned asset err=%v", err)
	}

	dead, changed, err := s.RevokeCollectibleUsername(ctx, domain.RevokeCollectibleUsernameRequest{
		Username: "burnme", Burn: true, Actor: "admin", CommandKey: "cmd-burn",
	})
	if err != nil || !changed {
		t.Fatalf("burn changed=%v err=%v", changed, err)
	}
	if dead.Status != domain.CollectibleUsernameStatusBurned || dead.Owner != (domain.Peer{}) ||
		dead.OriginalOwner != holder || dead.Version != burned.Version+1 {
		t.Fatalf("burned asset=%+v", dead)
	}
	rows, err = s.PeerUsernames(ctx, holder)
	if err != nil || len(rows) != 0 {
		t.Fatalf("burn left registry rows: %+v err=%v", rows, err)
	}
	// The burn released the name: another peer may occupy it again.
	if changed, err := s.SetEditableUsername(ctx, other, "BurnMe"); err != nil || !changed {
		t.Fatalf("claim freed name changed=%v err=%v", changed, err)
	}
	claimed, err := s.PeerUsernames(ctx, other)
	if err != nil || len(claimed) != 1 || claimed[0].Username != "BurnMe" || !claimed[0].Editable {
		t.Fatalf("claimed=%+v err=%v", claimed, err)
	}
	// The burned asset row survives for provenance, so the name never mints twice.
	if _, _, err := s.MintCollectibleUsername(ctx,
		collectibleMintRequest("burnme", holder, "cmd-burn-remint")); !errors.Is(err, domain.ErrUsernameOccupied) {
		t.Fatalf("burned name was re-minted: %v", err)
	}
	for _, err := range []error{
		mustErr(s.TransferCollectibleUsername(ctx, domain.TransferCollectibleUsernameRequest{
			Username: "burnme", To: other, Actor: "admin", CommandKey: "cmd-burn-transfer",
		})),
		mustErr(s.RevokeCollectibleUsername(ctx, domain.RevokeCollectibleUsernameRequest{
			Username: "burnme", Actor: "admin", CommandKey: "cmd-burn-revoke",
		})),
	} {
		if !errors.Is(err, domain.ErrCollectibleUsernameBurned) {
			t.Fatalf("mutation of a burned asset err=%v", err)
		}
	}
	log, err := s.CollectibleUsernameTransfers(ctx, dead.ID, 10)
	if err != nil || len(log) != 2 || log[0].Kind != domain.CollectibleUsernameKindBurn ||
		log[0].From != holder {
		t.Fatalf("burn provenance=%+v err=%v", log, err)
	}
	listed, err := s.ListCollectibleUsernames(ctx, domain.CollectibleUsernameFilter{
		Status: domain.CollectibleUsernameStatusBurned,
	})
	if err != nil || len(listed) != 1 || listed[0].ID != dead.ID {
		t.Fatalf("listed=%+v err=%v", listed, err)
	}
	byID, err := s.CollectibleUsernameByID(ctx, dead.ID)
	if err != nil || byID.Username != "burnme" {
		t.Fatalf("byID=%+v err=%v", byID, err)
	}
}

func mustErr[T any](_ T, _ bool, err error) error { return err }

func TestCollectibleUsernameToggle(t *testing.T) {
	ctx := context.Background()
	holder := domain.Peer{Type: domain.PeerTypeUser, ID: 1001}
	lonely := domain.Peer{Type: domain.PeerTypeUser, ID: 1003}

	tests := []struct {
		name        string
		peer        domain.Peer
		username    string
		active      bool
		wantErr     error
		wantChanged bool
		wantActive  bool
	}{
		{name: "deactivate collectible", peer: holder, username: "alphaone", active: false, wantChanged: true},
		{name: "activating an already active row", peer: holder, username: "ALPHAONE", active: true, wantChanged: false, wantActive: true},
		{name: "editable slot is off limits", peer: holder, username: "holderslot", active: false, wantErr: domain.ErrUsernameNotCollectible, wantActive: true},
		{name: "unknown username", peer: holder, username: "nothere", active: false, wantErr: domain.ErrUsernameNotOccupied},
		{name: "last active username", peer: lonely, username: "lonelyone", active: false, wantErr: domain.ErrUsernameOrderInvalid, wantActive: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := NewCollectibleUsernameStore()
			mustSetEditable(t, s, holder, "holderslot")
			mustMintCollectible(t, s, "alphaone", holder)
			mustMintCollectible(t, s, "betatwo", holder)
			mustMintCollectible(t, s, "lonelyone", lonely)

			changed, err := s.SetUsernameActive(ctx, tc.peer, tc.username, tc.active)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err=%v want %v", err, tc.wantErr)
			}
			if changed != tc.wantChanged {
				t.Fatalf("changed=%v want %v", changed, tc.wantChanged)
			}
			if tc.username == "nothere" {
				return
			}
			rows, err := s.PeerUsernames(ctx, tc.peer)
			if err != nil {
				t.Fatal(err)
			}
			row := usernameRow(t, rows, tc.username)
			if row.Active != tc.wantActive {
				t.Fatalf("row=%+v want active=%v", row, tc.wantActive)
			}
		})
	}

	t.Run("deactivate all keeps the editable slot", func(t *testing.T) {
		s := NewCollectibleUsernameStore()
		mustSetEditable(t, s, holder, "holderslot")
		mustMintCollectible(t, s, "alphaone", holder)
		mustMintCollectible(t, s, "betatwo", holder)
		changed, err := s.DeactivateAllUsernames(ctx, holder)
		if err != nil || !changed {
			t.Fatalf("deactivate all changed=%v err=%v", changed, err)
		}
		rows, err := s.PeerUsernames(ctx, holder)
		if err != nil || len(rows) != 3 {
			t.Fatalf("rows=%+v err=%v", rows, err)
		}
		if !rows[0].Editable || !rows[0].Active {
			t.Fatalf("editable row=%+v", rows[0])
		}
		if rows[1].Active || rows[2].Active {
			t.Fatalf("collectibles still active: %+v", rows)
		}
		if changed, err := s.DeactivateAllUsernames(ctx, holder); err != nil || changed {
			t.Fatalf("second deactivate changed=%v err=%v", changed, err)
		}
	})
}

func TestCollectibleUsernameReorder(t *testing.T) {
	ctx := context.Background()
	holder := domain.Peer{Type: domain.PeerTypeUser, ID: 1001}

	tests := []struct {
		name        string
		order       []string
		wantErr     error
		wantChanged bool
		wantOrder   []string
	}{
		{
			name:        "valid permutation",
			order:       []string{"gammathree", "@AlphaOne", "betatwo"},
			wantChanged: true,
			wantOrder:   []string{"holderslot", "alphaone", "betatwo", "gammathree"},
		},
		{
			name:        "identity permutation",
			order:       []string{"alphaone", "betatwo", "gammathree"},
			wantChanged: false,
			wantOrder:   []string{"holderslot", "alphaone", "betatwo", "gammathree"},
		},
		{
			name:      "partial order",
			order:     []string{"alphaone"},
			wantErr:   domain.ErrUsernameOrderInvalid,
			wantOrder: []string{"holderslot", "alphaone", "betatwo", "gammathree"},
		},
		{
			name:      "duplicate entry",
			order:     []string{"alphaone", "alphaone", "betatwo"},
			wantErr:   domain.ErrUsernameOrderInvalid,
			wantOrder: []string{"holderslot", "alphaone", "betatwo", "gammathree"},
		},
		{
			name:      "unknown username",
			order:     []string{"alphaone", "betatwo", "nothere"},
			wantErr:   domain.ErrUsernameOrderInvalid,
			wantOrder: []string{"holderslot", "alphaone", "betatwo", "gammathree"},
		},
		{
			name:      "editable slot is not reorderable",
			order:     []string{"holderslot", "alphaone", "betatwo", "gammathree"},
			wantErr:   domain.ErrUsernameOrderInvalid,
			wantOrder: []string{"holderslot", "alphaone", "betatwo", "gammathree"},
		},
		{
			name:      "garbage input",
			order:     []string{"", "@", "alphaone"},
			wantErr:   domain.ErrUsernameOrderInvalid,
			wantOrder: []string{"holderslot", "alphaone", "betatwo", "gammathree"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := NewCollectibleUsernameStore()
			mustSetEditable(t, s, holder, "holderslot")
			mustMintCollectible(t, s, "alphaone", holder)
			mustMintCollectible(t, s, "betatwo", holder)
			mustMintCollectible(t, s, "gammathree", holder)

			changed, err := s.ReorderUsernames(ctx, holder, tc.order)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err=%v want %v", err, tc.wantErr)
			}
			if changed != tc.wantChanged {
				t.Fatalf("changed=%v want %v", changed, tc.wantChanged)
			}
			rows, err := s.PeerUsernames(ctx, holder)
			if err != nil {
				t.Fatal(err)
			}
			want := tc.wantOrder
			if tc.wantChanged {
				want = []string{"holderslot", "gammathree", "alphaone", "betatwo"}
			}
			if len(rows) != len(want) {
				t.Fatalf("rows=%+v want %v", rows, want)
			}
			for i, name := range want {
				if rows[i].Username != name {
					t.Fatalf("rows=%+v want %v", rows, want)
				}
			}
		})
	}

	t.Run("no collectibles", func(t *testing.T) {
		s := NewCollectibleUsernameStore()
		mustSetEditable(t, s, holder, "holderslot")
		if changed, err := s.ReorderUsernames(ctx, holder, nil); err != nil || changed {
			t.Fatalf("changed=%v err=%v", changed, err)
		}
	})
}

func TestCollectibleUsernameRegistryUniqueness(t *testing.T) {
	ctx := context.Background()
	s := NewCollectibleUsernameStore()
	holder := domain.Peer{Type: domain.PeerTypeUser, ID: 1001}
	other := domain.Peer{Type: domain.PeerTypeUser, ID: 1002}
	mustSetEditable(t, s, holder, "holderslot")

	// One editable row per peer: setting a new one replaces the old.
	mustSetEditable(t, s, holder, "secondslot")
	rows, err := s.PeerUsernames(ctx, holder)
	if err != nil || len(rows) != 1 || rows[0].Username != "secondslot" {
		t.Fatalf("rows=%+v err=%v", rows, err)
	}
	// The released name is free again, case-insensitively.
	if changed, err := s.SetEditableUsername(ctx, other, "HOLDERSLOT"); err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	if _, err := s.SetEditableUsername(ctx, holder, "holderslot"); !errors.Is(err, domain.ErrUsernameOccupied) {
		t.Fatalf("occupied name reused: %v", err)
	}
	if changed, err := s.SetEditableUsername(ctx, other, ""); err != nil || !changed {
		t.Fatalf("clear editable changed=%v err=%v", changed, err)
	}
	if rows, err := s.PeerUsernames(ctx, other); err != nil || len(rows) != 0 {
		t.Fatalf("rows=%+v err=%v", rows, err)
	}

	// A vault asset owns its name even without a registry row, so the editable
	// slot cannot take it and the asset stays handable.
	vaulted := mustMintCollectible(t, s, "vaultname", domain.Peer{})
	if _, err := s.SetEditableUsername(ctx, other, "VaultName"); !errors.Is(err, domain.ErrUsernameOccupied) {
		t.Fatalf("vault name became claimable: %v", err)
	}
	if moved, _, err := s.TransferCollectibleUsername(ctx, domain.TransferCollectibleUsernameRequest{
		Username: "vaultname", To: other, Actor: "admin", CommandKey: "cmd-vault-out",
	}); err != nil || moved.ID != vaulted.ID || moved.Owner != other {
		t.Fatalf("moved=%+v err=%v", moved, err)
	}

	// Returned slices are copies: mutating them cannot change stored state.
	rows, err = s.PeerUsernames(ctx, holder)
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows=%+v err=%v", rows, err)
	}
	rows[0].Username = "mutated"
	rows[0].Active = false
	again, err := s.PeerUsernames(ctx, holder)
	if err != nil || again[0].Username != "secondslot" || !again[0].Active {
		t.Fatalf("stored state mutated through the returned slice: %+v err=%v", again, err)
	}
}

func TestCollectibleUsernameListingAndPaging(t *testing.T) {
	ctx := context.Background()
	s := NewCollectibleUsernameStore()
	holder := domain.Peer{Type: domain.PeerTypeUser, ID: 1001}
	other := domain.Peer{Type: domain.PeerTypeUser, ID: 1002}
	first := mustMintCollectible(t, s, "alphaone", holder)
	second := mustMintCollectible(t, s, "alphatwo", other)
	third := mustMintCollectible(t, s, "betathree", domain.Peer{})

	all, err := s.ListCollectibleUsernames(ctx, domain.CollectibleUsernameFilter{})
	if err != nil || len(all) != 3 || all[0].ID != third.ID || all[2].ID != first.ID {
		t.Fatalf("all=%+v err=%v", all, err)
	}
	page, err := s.ListCollectibleUsernames(ctx, domain.CollectibleUsernameFilter{Limit: 2})
	if err != nil || len(page) != 2 || page[0].ID != third.ID {
		t.Fatalf("page=%+v err=%v", page, err)
	}
	next, err := s.ListCollectibleUsernames(ctx, domain.CollectibleUsernameFilter{
		BeforeID: page[len(page)-1].ID, Limit: 2,
	})
	if err != nil || len(next) != 1 || next[0].ID != first.ID {
		t.Fatalf("next=%+v err=%v", next, err)
	}
	owned, err := s.ListCollectibleUsernames(ctx, domain.CollectibleUsernameFilter{Owner: other})
	if err != nil || len(owned) != 1 || owned[0].ID != second.ID {
		t.Fatalf("owned=%+v err=%v", owned, err)
	}
	matched, err := s.ListCollectibleUsernames(ctx, domain.CollectibleUsernameFilter{Query: "ALPHA"})
	if err != nil || len(matched) != 2 {
		t.Fatalf("matched=%+v err=%v", matched, err)
	}
	vault, err := s.ListCollectibleUsernames(ctx, domain.CollectibleUsernameFilter{
		Status: domain.CollectibleUsernameStatusVault,
	})
	if err != nil || len(vault) != 1 || vault[0].ID != third.ID {
		t.Fatalf("vault=%+v err=%v", vault, err)
	}
	if _, err := s.ListCollectibleUsernames(ctx, domain.CollectibleUsernameFilter{
		Status: domain.CollectibleUsernameStatus("gone"),
	}); !errors.Is(err, domain.ErrCollectibleUsernameStateInvalid) {
		t.Fatalf("invalid status filter err=%v", err)
	}
	if _, err := s.CollectibleUsername(ctx, "@AlphaOne"); err != nil {
		t.Fatalf("lookup by display form: %v", err)
	}
	if _, err := s.CollectibleUsername(ctx, ""); !errors.Is(err, domain.ErrCollectibleUsernameNotFound) {
		t.Fatalf("empty lookup err=%v", err)
	}
	if _, err := s.CollectibleUsernameByID(ctx, 4242); !errors.Is(err, domain.ErrCollectibleUsernameNotFound) {
		t.Fatalf("unknown id err=%v", err)
	}
}
