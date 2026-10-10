package rpc

import (
	"testing"

	"telesrv/internal/domain"
)

func TestStarGiftActiveAuctionsHashStableAndSensitive(t *testing.T) {
	base := []domain.StarGiftAuction{
		{Gift: domain.StarGift{ID: 11}, Version: 3, CurrentRound: 1, GiftsLeft: 5, EndDate: 1800000000, AveragePrice: 100, ListedCount: 2},
		{Gift: domain.StarGift{ID: 22}, Version: 1, CurrentRound: 2, GiftsLeft: 0, EndDate: 1800000100, AveragePrice: 250, ListedCount: 1},
	}
	first := starGiftActiveAuctionsHash(base)
	if first == 0 {
		t.Fatal("hash of non-empty states is zero")
	}
	if again := starGiftActiveAuctionsHash(base); again != first {
		t.Fatalf("hash not stable: %d != %d", again, first)
	}
	if empty := starGiftActiveAuctionsHash(nil); empty == first {
		t.Fatal("empty states hash collides with non-empty")
	}
	mutated := append([]domain.StarGiftAuction(nil), base...)
	mutated[0].CurrentRound++
	if same := starGiftActiveAuctionsHash(mutated); same == first {
		t.Fatal("round advance did not change hash")
	}
	mutated = append([]domain.StarGiftAuction(nil), base...)
	mutated[1].GiftsLeft++
	if same := starGiftActiveAuctionsHash(mutated); same == first {
		t.Fatal("gifts-left change did not change hash")
	}
}
