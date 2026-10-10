package giftclaim

import (
	"context"
	"math/big"
	"testing"
	"time"

	tonwallet "github.com/xssnick/tonutils-go/ton/wallet"
	"go.uber.org/zap"

	"telesrv/internal/domain"
)

type ownershipSyncStore struct {
	gifts   []domain.UniqueStarGift
	changes []ownershipChange
}

type ownershipChange struct {
	id             int64
	giftAddress    string
	previousWallet string
	wallet         string
}

func (s *ownershipSyncStore) ResolveOnChainGift(context.Context, string) (domain.UniqueStarGift, bool, error) {
	return domain.UniqueStarGift{}, false, nil
}
func (s *ownershipSyncStore) ResolveGiftByRef(context.Context, string) (domain.UniqueStarGift, bool, error) {
	return domain.UniqueStarGift{}, false, nil
}
func (s *ownershipSyncStore) WithdrawalForGift(context.Context, int64, int64) (domain.StarGiftWithdrawal, bool, error) {
	return domain.StarGiftWithdrawal{}, false, nil
}
func (s *ownershipSyncStore) WithdrawalByRequestID(context.Context, string) (domain.StarGiftWithdrawal, bool, error) {
	return domain.StarGiftWithdrawal{}, false, nil
}
func (s *ownershipSyncStore) ListOnChainGifts(_ context.Context, afterID int64, limit int) ([]domain.UniqueStarGift, error) {
	out := make([]domain.UniqueStarGift, 0, limit)
	for _, gift := range s.gifts {
		if gift.ID > afterID && len(out) < limit {
			out = append(out, gift)
		}
	}
	return out, nil
}
func (s *ownershipSyncStore) ReconcileOnChainOwner(_ context.Context, id int64, giftAddress, previousWallet, wallet string) (bool, error) {
	s.changes = append(s.changes, ownershipChange{id: id, giftAddress: giftAddress, previousWallet: previousWallet, wallet: wallet})
	return true, nil
}
func (*ownershipSyncStore) ProfileUsername(context.Context, int64) (string, error) { return "", nil }
func (*ownershipSyncStore) CreateChallenge(context.Context, int64, int64, time.Time, time.Duration) (domain.StarGiftClaimChallenge, error) {
	return domain.StarGiftClaimChallenge{}, nil
}
func (*ownershipSyncStore) ResolveChallenge(context.Context, string, int64, int) (domain.StarGiftClaimChallenge, bool, error) {
	return domain.StarGiftClaimChallenge{}, false, nil
}
func (*ownershipSyncStore) CommitClaim(context.Context, domain.StarGiftOnChainClaim) (domain.StarGiftOnChainClaimResult, error) {
	return domain.StarGiftOnChainClaimResult{}, nil
}

type ownershipSyncVerifier struct {
	owners map[int64]struct {
		wallet string
		active bool
	}
}

func (*ownershipSyncVerifier) VerifyWalletProof(context.Context, string, string, tonwallet.TonConnectProof, []byte, time.Duration) error {
	return nil
}
func (*ownershipSyncVerifier) VerifyMint(context.Context, string, *big.Int, string) (string, error) {
	return "", nil
}
func (v *ownershipSyncVerifier) CurrentNFTOwner(_ context.Context, _ string, index *big.Int, _ string) (string, bool, error) {
	owner := v.owners[index.Int64()]
	return owner.wallet, owner.active, nil
}
func (*ownershipSyncVerifier) Close() {}

func TestOwnershipSyncReturnsTransferredGiftToRelayer(t *testing.T) {
	store := &ownershipSyncStore{gifts: []domain.UniqueStarGift{
		{ID: 8, Slug: "owl-8", OwnerAddress: "wallet-old-8", GiftAddress: "item-old-8"},
		{ID: 9, Slug: "owl-9", OwnerAddress: "wallet-a", GiftAddress: "item-9"},
		{ID: 10, Slug: "owl-10", OwnerAddress: "wallet-c", GiftAddress: "item-10"},
	}}
	verifier := &ownershipSyncVerifier{owners: map[int64]struct {
		wallet string
		active bool
	}{
		8:  {wallet: "wallet-b", active: false}, // immutable previous collection
		9:  {wallet: "wallet-b", active: true},
		10: {wallet: "wallet-c", active: true},
	}}
	service := &Service{
		store: store, verifier: verifier, collection: "active-collection",
		ownershipSyncBatch: 100, logger: zap.NewNop(),
	}
	next, complete, err := service.syncOwnershipPage(context.Background(), 0)
	if err != nil || !complete || next != 10 {
		t.Fatalf("sync page = next %d complete %v err %v", next, complete, err)
	}
	if len(store.changes) != 1 {
		t.Fatalf("ownership changes = %+v, want one", store.changes)
	}
	change := store.changes[0]
	if change.id != 9 || change.giftAddress != "item-9" || change.previousWallet != "wallet-a" || change.wallet != "wallet-b" {
		t.Fatalf("ownership change = %+v", change)
	}
}
