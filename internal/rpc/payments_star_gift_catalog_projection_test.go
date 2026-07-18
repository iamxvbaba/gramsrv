package rpc

import (
	"testing"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tlprofile"

	"telesrv/internal/domain"
)

func TestStarGiftCatalogProjectionKeepsSaleDatesBehindSoldOutFlag(t *testing.T) {
	base := domain.StarGift{
		ID:            8001,
		RevisionID:    9001,
		Stars:         100,
		ConvertStars:  85,
		Title:         "Fresh Socks",
		FirstSaleDate: 100,
		LastSaleDate:  200,
		Sticker: domain.Document{
			ID:         700,
			AccessHash: 7,
			DCID:       2,
			MimeType:   "application/x-tgsticker",
			Attributes: []domain.DocumentAttribute{{Kind: domain.DocAttrSticker}},
		},
	}

	tests := []struct {
		name         string
		gift         domain.StarGift
		wantSoldOut  bool
		wantSaleDate bool
	}{
		{name: "unlimited live gift with operational sale history", gift: base},
		{name: "limited live gift", gift: func() domain.StarGift {
			gift := base
			gift.Limited = true
			gift.AvailabilityRemains = 9
			gift.AvailabilityTotal = 10
			return gift
		}()},
		{name: "sold out gift", gift: func() domain.StarGift {
			gift := base
			gift.Limited = true
			gift.SoldOut = true
			gift.AvailabilityTotal = 10
			return gift
		}(), wantSoldOut: true, wantSaleDate: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			for _, profile := range []tlprofile.Profile{
				tlprofile.Profile225,
				tlprofile.Profile226,
				tlprofile.Profile227,
				tlprofile.Profile228,
			} {
				response := &tg.PaymentsStarGifts{
					Hash:  1,
					Gifts: []tg.StarGiftClass{tgStarGift(test.gift)},
					Chats: []tg.ChatClass{},
					Users: []tg.UserClass{},
				}
				wire := &bin.Buffer{}
				if err := tlprofile.EncodeObject(profile, response, wire); err != nil {
					t.Fatalf("encode Layer %d catalog: %v", profile, err)
				}
				decodedObject, err := tlprofile.DecodeObject(profile, &bin.Buffer{Buf: wire.Buf}, tlprofile.Limits{})
				if err != nil {
					t.Fatalf("decode Layer %d catalog: %v", profile, err)
				}
				decoded, ok := decodedObject.(*tg.PaymentsStarGifts)
				if !ok || len(decoded.Gifts) != 1 {
					t.Fatalf("decode Layer %d catalog = %T %#v", profile, decodedObject, decodedObject)
				}
				gift, ok := decoded.Gifts[0].(*tg.StarGift)
				if !ok {
					t.Fatalf("decode Layer %d gift = %T", profile, decoded.Gifts[0])
				}
				if gift.SoldOut != test.wantSoldOut {
					t.Fatalf("Layer %d sold_out = %v, want %v", profile, gift.SoldOut, test.wantSoldOut)
				}
				first, firstSet := gift.GetFirstSaleDate()
				last, lastSet := gift.GetLastSaleDate()
				if firstSet != test.wantSaleDate || lastSet != test.wantSaleDate {
					t.Fatalf("Layer %d sale date flags = (%v,%v), want %v", profile, firstSet, lastSet, test.wantSaleDate)
				}
				if test.wantSaleDate && (first != test.gift.FirstSaleDate || last != test.gift.LastSaleDate) {
					t.Fatalf("Layer %d sale dates = (%d,%d), want (%d,%d)", profile, first, last, test.gift.FirstSaleDate, test.gift.LastSaleDate)
				}
			}
		})
	}
}
