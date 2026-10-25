package rpc

import (
	"context"
	"testing"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tlprofile"

	"telesrv/internal/domain"
)

// resaleAttributesRPCService overrides just the two gift-service calls that
// payments.getResaleStarGifts needs. The embedded interface covers the rest.
type resaleAttributesRPCService struct {
	GiftsService
	preview domain.StarGiftUpgradePreview
	found   bool
}

func (s *resaleAttributesRPCService) ListResale(context.Context, domain.StarGiftResaleFilter) (domain.StarGiftResalePage, error) {
	return domain.StarGiftResalePage{}, nil
}

func (s *resaleAttributesRPCService) CollectiblePreview(context.Context, int64) (domain.StarGiftUpgradePreview, bool, error) {
	return s.preview, s.found, nil
}

// TestPaymentsGetResaleStarGiftsAttributesHashFlagEncoding locks in the fix for
// the shared flag bit 1 on attributes/attributes_hash. Setting only
// attributes_hash (when the caller's hash already matched) left Attributes nil
// while the flag bit was set, so the profile encoder failed with
// "explicit flag has nil interface field attributes".
func TestPaymentsGetResaleStarGiftsAttributesHashFlagEncoding(t *testing.T) {
	cases := []struct {
		name           string
		requestedHash  int64
		wantAttributes bool
	}{
		{name: "matching hash omits shared flag", requestedHash: 7, wantAttributes: false},
		{name: "stale hash publishes attributes and hash", requestedHash: 6, wantAttributes: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, _, _, gift := starGiftTestRouter(t)
			model := collectibleRPCAttribute(domain.StarGiftCollectibleModel, 9101, "Aurora")
			r.deps.Gifts = &resaleAttributesRPCService{
				preview: domain.StarGiftUpgradePreview{
					GiftID: gift.ID, Revision: 7,
					Models: []domain.StarGiftCollectibleAttribute{model},
				},
				found: true,
			}

			req := &tg.PaymentsGetResaleStarGiftsRequest{GiftID: gift.ID}
			req.SetAttributesHash(tc.requestedHash)
			response, err := r.onPaymentsGetResaleStarGifts(context.Background(), req)
			if err != nil {
				t.Fatalf("get resale star gifts: %v", err)
			}

			if _, ok := response.GetAttributes(); ok != tc.wantAttributes {
				t.Fatalf("attributes present = %v, want %v", ok, tc.wantAttributes)
			}
			if _, ok := response.GetAttributesHash(); ok != tc.wantAttributes {
				t.Fatalf("attributes_hash present = %v, want %v", ok, tc.wantAttributes)
			}

			for _, profile := range []tlprofile.Profile{tlprofile.Profile227, tlprofile.Profile228} {
				wire := &bin.Buffer{}
				if err := tlprofile.EncodeObject(profile, response, wire); err != nil {
					t.Fatalf("encode Layer %d resale star gifts: %v", profile, err)
				}
			}
		})
	}
}
