package domain

import (
	"errors"
	"testing"
)

// TestStarGiftCatalogWriteValidateLifecycleAuthoring covers the operator
// authoring boundary shared by the auction panel and the scheduled-release
// ("delayed drop") surfaces.
func TestStarGiftCatalogWriteValidateLifecycleAuthoring(t *testing.T) {
	const now = 1_000_000

	cases := []struct {
		name    string
		write   StarGiftCatalogWrite
		wantErr bool
	}{
		{
			name:  "regular gift with no lifecycle fields",
			write: StarGiftCatalogWrite{},
		},
		{
			name:  "scheduled release in the future",
			write: StarGiftCatalogWrite{LockedUntilDate: now + 120},
		},
		{
			name:    "scheduled release in the past",
			write:   StarGiftCatalogWrite{LockedUntilDate: now - 1},
			wantErr: true,
		},
		{
			name: "valid auction, immediate start, custom round duration",
			write: StarGiftCatalogWrite{
				Auction: true, AuctionSlug: "spring", GiftsPerRound: 2,
				AvailabilityTotal: 10, AuctionRoundDuration: 60,
			},
		},
		{
			name: "valid auction, future start, default round duration",
			write: StarGiftCatalogWrite{
				Auction: true, AuctionSlug: "spring", GiftsPerRound: 5,
				AvailabilityTotal: 5, AuctionStartDate: now + 3600,
			},
		},
		{
			name:    "auction missing slug",
			write:   StarGiftCatalogWrite{Auction: true, GiftsPerRound: 2, AvailabilityTotal: 10},
			wantErr: true,
		},
		{
			name:    "auction with zero gifts per round",
			write:   StarGiftCatalogWrite{Auction: true, AuctionSlug: "s", AvailabilityTotal: 10},
			wantErr: true,
		},
		{
			name:    "auction with zero supply",
			write:   StarGiftCatalogWrite{Auction: true, AuctionSlug: "s", GiftsPerRound: 2},
			wantErr: true,
		},
		{
			name: "auction gifts per round exceeds supply",
			write: StarGiftCatalogWrite{
				Auction: true, AuctionSlug: "s", GiftsPerRound: 11, AvailabilityTotal: 10,
			},
			wantErr: true,
		},
		{
			name: "auction start in the past",
			write: StarGiftCatalogWrite{
				Auction: true, AuctionSlug: "s", GiftsPerRound: 2,
				AvailabilityTotal: 10, AuctionStartDate: now - 1,
			},
			wantErr: true,
		},
		{
			name: "auction must not carry a scheduled-release time",
			write: StarGiftCatalogWrite{
				Auction: true, AuctionSlug: "s", GiftsPerRound: 2,
				AvailabilityTotal: 10, LockedUntilDate: now + 60,
			},
			wantErr: true,
		},
		{
			name:    "auction slug on a non-auction gift",
			write:   StarGiftCatalogWrite{AuctionSlug: "s"},
			wantErr: true,
		},
		{
			name:    "round duration on a non-auction gift",
			write:   StarGiftCatalogWrite{AuctionRoundDuration: 60},
			wantErr: true,
		},
		{
			name: "negative round duration",
			write: StarGiftCatalogWrite{
				Auction: true, AuctionSlug: "s", GiftsPerRound: 2,
				AvailabilityTotal: 10, AuctionRoundDuration: -1,
			},
			wantErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.write.ValidateLifecycleAuthoring(now)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				if !errors.Is(err, ErrStarGiftLifecycleInvalid) {
					t.Fatalf("expected ErrStarGiftLifecycleInvalid, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}
