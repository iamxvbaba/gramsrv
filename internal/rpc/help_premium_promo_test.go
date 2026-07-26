package rpc

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"github.com/iamxvbaba/td/tlprofile"
	"go.uber.org/zap/zaptest"

	"telesrv/internal/domain"
)

type staticPremiumPromoService struct {
	catalog domain.PremiumPromoCatalog
	found   bool
	err     error
}

func (s staticPremiumPromoService) PremiumPromo(context.Context) (domain.PremiumPromoCatalog, bool, error) {
	out := domain.PremiumPromoCatalog{
		VideoSections: append([]string(nil), s.catalog.VideoSections...),
		Videos:        append([]domain.Document(nil), s.catalog.Videos...),
	}
	for i := range out.Videos {
		out.Videos[i].FileReference = append([]byte(nil), out.Videos[i].FileReference...)
		out.Videos[i].Attributes = append([]domain.DocumentAttribute(nil), out.Videos[i].Attributes...)
		out.Videos[i].Thumbs = append([]domain.PhotoSize(nil), out.Videos[i].Thumbs...)
	}
	return out, s.found, s.err
}

func TestHelpGetPremiumPromoReturnsSeededCatalogAcrossExactProfiles(t *testing.T) {
	const userID int64 = 1000000001
	now := time.Date(2026, 7, 26, 8, 0, 0, 0, time.UTC)
	user := domain.User{
		ID:           userID,
		AccessHash:   17,
		FirstName:    "Alice",
		PremiumUntil: int(now.Add(48 * time.Hour).Unix()),
	}
	catalog := premiumPromoRPCTestCatalog()
	r := New(Config{}, Deps{
		Users:        staticUsersService{user: user},
		PremiumPromo: staticPremiumPromoService{catalog: catalog, found: true},
	}, zaptest.NewLogger(t), fixedClock{now: now})
	ctx := WithUserID(context.Background(), userID)

	for profile := tlprofile.Profile225; profile <= tlprofile.Profile228; profile++ {
		t.Run(fmt.Sprintf("layer_%d", profile), func(t *testing.T) {
			result, method := dispatchExactLayerRPCTest(t, r, ctx, profile, &tg.HelpGetPremiumPromoRequest{})
			if method != "help.getPremiumPromo" {
				t.Fatalf("method = %q", method)
			}
			promo, ok := dispatchCanonicalValue(result).(*tg.HelpPremiumPromo)
			if !ok {
				t.Fatalf("response = %T, want *tg.HelpPremiumPromo", dispatchCanonicalValue(result))
			}
			if len(promo.VideoSections) != 1 || promo.VideoSections[0] != "no_ads" || len(promo.Videos) != 1 {
				t.Fatalf("promo vectors = sections:%v videos:%d", promo.VideoSections, len(promo.Videos))
			}
			doc, ok := promo.Videos[0].(*tg.Document)
			if !ok {
				t.Fatalf("video = %T, want *tg.Document", promo.Videos[0])
			}
			if doc.ID != catalog.Videos[0].ID || doc.DCID != 2 || len(doc.Thumbs) != 1 {
				t.Fatalf("document = %+v", doc)
			}
			thumb, ok := doc.Thumbs[0].(*tg.PhotoSize)
			if !ok || thumb.Type != "m" || thumb.Size != 1234 {
				t.Fatalf("thumb = %#v", doc.Thumbs[0])
			}
			if len(promo.PeriodOptions) != 0 {
				t.Fatalf("period options = %+v, want no dead purchase entry", promo.PeriodOptions)
			}
			if !strings.Contains(promo.StatusText, "2026-07-28") {
				t.Fatalf("status text = %q, want viewer expiry", promo.StatusText)
			}
		})
	}
}

func TestHelpGetPremiumPromoAuthorizationBotAndFallback(t *testing.T) {
	const userID int64 = 1000000001
	user := domain.User{ID: userID, AccessHash: 17, FirstName: "Alice"}
	r := New(Config{}, Deps{
		Users:        staticUsersService{user: user},
		PremiumPromo: staticPremiumPromoService{},
	}, zaptest.NewLogger(t), fixedClock{now: time.Unix(1_700_000_000, 0)})

	if rpcAllowedWithoutAuthorization(tg.HelpGetPremiumPromoRequestTypeID) {
		t.Fatal("help.getPremiumPromo must require a fully authorized user")
	}
	if _, err := r.onHelpGetPremiumPromo(context.Background()); !tgerr.Is(err, "AUTH_KEY_UNREGISTERED") {
		t.Fatalf("unauthorized error = %v, want AUTH_KEY_UNREGISTERED", err)
	}

	promo, err := r.onHelpGetPremiumPromo(WithUserID(context.Background(), userID))
	if err != nil {
		t.Fatalf("fallback response: %v", err)
	}
	if len(promo.VideoSections) != 0 || len(promo.Videos) != 0 || len(promo.PeriodOptions) != 0 {
		t.Fatalf("fallback vectors = %+v", promo)
	}

	botRouter := New(Config{}, Deps{
		Users: staticUsersService{user: domain.User{
			ID:         userID,
			AccessHash: 19,
			FirstName:  "PromoBot",
			Bot:        true,
		}},
	}, zaptest.NewLogger(t), fixedClock{now: time.Unix(1_700_000_000, 0)})
	if _, err := botRouter.onHelpGetPremiumPromo(WithUserID(context.Background(), userID)); !tgerr.Is(err, "BOT_METHOD_INVALID") {
		t.Fatalf("bot error = %v, want BOT_METHOD_INVALID", err)
	}
}

func TestHelpGetPremiumPromoResponsesDoNotShareMutableDocuments(t *testing.T) {
	const userID int64 = 1000000001
	catalog := premiumPromoRPCTestCatalog()
	r := New(Config{}, Deps{
		Users:        staticUsersService{user: domain.User{ID: userID}},
		PremiumPromo: staticPremiumPromoService{catalog: catalog, found: true},
	}, zaptest.NewLogger(t), fixedClock{now: time.Unix(1_700_000_000, 0)})
	ctx := WithUserID(context.Background(), userID)

	first, err := r.onHelpGetPremiumPromo(ctx)
	if err != nil {
		t.Fatal(err)
	}
	first.VideoSections[0] = "mutated"
	firstDoc := first.Videos[0].(*tg.Document)
	firstDoc.DCID = 99
	firstDoc.FileReference[0] ^= 0xff

	second, err := r.onHelpGetPremiumPromo(ctx)
	if err != nil {
		t.Fatal(err)
	}
	secondDoc := second.Videos[0].(*tg.Document)
	if second.VideoSections[0] != "no_ads" || secondDoc.DCID != 2 || secondDoc.FileReference[0] != 0 {
		t.Fatalf("second response inherited mutation: sections=%v doc=%+v", second.VideoSections, secondDoc)
	}
}

func premiumPromoRPCTestCatalog() domain.PremiumPromoCatalog {
	return domain.PremiumPromoCatalog{
		VideoSections: []string{"no_ads"},
		Videos: []domain.Document{{
			ID:            5814500255441357739,
			AccessHash:    5876417653416908580,
			FileReference: []byte{0, 1, 2, 3},
			Date:          1_654_006_663,
			MimeType:      "video/mp4",
			Size:          2_650_178,
			DCID:          2,
			Attributes: []domain.DocumentAttribute{
				{Kind: domain.DocAttrFilename, FileName: "promo.mp4"},
				{Kind: domain.DocAttrVideo, W: 720, H: 1070, Duration: 5, SupportsStreaming: true},
				{Kind: domain.DocAttrAnimated},
			},
			Thumbs: []domain.PhotoSize{{
				Kind: domain.PhotoSizeKindDefault,
				Type: "m",
				W:    160,
				H:    240,
				Size: 1234,
			}},
		}},
	}
}
