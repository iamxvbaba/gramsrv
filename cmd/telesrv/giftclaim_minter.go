package main

import (
	"context"
	"errors"
	"time"

	"telesrv/internal/customfragment"
	"telesrv/internal/giftclaim"
)

// giftClaimMinter adapts the CustomFragment mint bridge to the giftclaim Mini
// App's Minter seam. Both backing and target structs are stable mirror shapes;
// the only behavior worth mapping here is the "NFT not finalized yet" signal.
type giftClaimMinter struct {
	backend *customfragment.Service
}

func newGiftClaimMinter(backend *customfragment.Service) giftclaim.Minter {
	if backend == nil {
		return nil
	}
	return &giftClaimMinter{backend: backend}
}

func (m *giftClaimMinter) MintIntent(ctx context.Context, requestID, walletAddress string, now time.Time) (giftclaim.MintIntent, error) {
	intent, err := m.backend.Intent(ctx, requestID, walletAddress, now)
	if err != nil {
		return giftclaim.MintIntent{}, giftClaimMinterError(err)
	}
	return giftclaim.MintIntent{
		Network: intent.Network, CollectionAddress: intent.CollectionAddress,
		Amount: intent.Amount, Payload: intent.Payload, ValidUntil: intent.ValidUntil,
		ItemIndex: intent.ItemIndex, WalletAddress: intent.WalletAddress,
	}, nil
}

func (m *giftClaimMinter) ConfirmMint(ctx context.Context, requestID, walletAddress string, now time.Time) (giftclaim.MintConfirmation, error) {
	confirmation, err := m.backend.Confirm(ctx, requestID, walletAddress, now)
	if err != nil {
		return giftclaim.MintConfirmation{}, giftClaimMinterError(err)
	}
	return giftclaim.MintConfirmation{
		Status: confirmation.Status, OwnerAddress: confirmation.OwnerAddress,
		GiftAddress: confirmation.GiftAddress, Collection: confirmation.Collection,
	}, nil
}

func giftClaimMinterError(err error) error {
	if errors.Is(err, customfragment.ErrNotFinalized) {
		return giftclaim.ErrMintNotFinalized
	}
	return err
}
