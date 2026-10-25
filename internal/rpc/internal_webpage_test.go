package rpc

import (
	"context"
	"testing"

	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap/zaptest"

	appprivacy "telesrv/internal/app/privacy"
	appusers "telesrv/internal/app/users"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

func TestParseInternalLink(t *testing.T) {
	cases := []struct {
		raw, kind, username, slug string
		collection                int
	}{
		{"https://links.example.org/durov", "profile", "durov", "", 0},
		{"links.example.org/Example_bot", "profile", "Example_bot", "", 0},
		{"https://links.example.org/+AbCdEf123", "invite", "", "AbCdEf123", 0},
		{"https://links.example.org/joinchat/XyZ", "invite", "", "XyZ", 0},
		{"https://links.example.org/nft/PlushPepe-1", "nft", "", "PlushPepe-1", 0},
		{"https://links.example.org/auction/chillflame", "auction", "", "chillflame", 0},
		{"https://links.example.org/durov/c/3", "collection", "durov", "", 3},
		{"https://links.example.org/durov?collection=7", "collection", "durov", "", 7},
	}
	for _, c := range cases {
		link, ok := parseInternalLink(c.raw, "links.example.org")
		if !ok || link.kind != c.kind || link.username != c.username || link.slug != c.slug || link.collection != c.collection {
			t.Errorf("%s -> %+v ok=%v", c.raw, link, ok)
		}
	}
	for _, raw := range []string{
		"https://example.com/durov",              // other site
		"https://t.me/durov",                     // a real Telegram account, not ours
		"https://links.example.org/",             // no path
		"https://links.example.org/+79991234567", // phone link
		"https://links.example.org/ab",           // too short for a username
		"https://links.example.org/durov/123",    // message link
		"https://links.example.org/durov/c/x",
	} {
		if link, ok := parseInternalLink(raw, "links.example.org"); ok {
			t.Errorf("%s parsed as %+v", raw, link)
		}
	}
}

func TestInternalWebPageAttributes(t *testing.T) {
	page := tgWebPage(domain.MessageWebPage{
		State: domain.MessageWebPageStateDone, ID: 1, URL: "https://links.example.org/durov/c/1", DisplayURL: "links.example.org/durov/c/1",
		Type: webPageTypeCollection, Title: "Best",
		CollectionIcons: []domain.Document{{ID: 5, AccessHash: 6, MimeType: "application/x-tgsticker"}},
	}).(*tg.WebPage)
	attrs, ok := page.GetAttributes()
	if !ok || len(attrs) != 1 {
		t.Fatalf("attributes = %+v", attrs)
	}
	if c, ok := attrs[0].(*tg.WebPageAttributeStarGiftCollection); !ok || len(c.Icons) != 1 {
		t.Fatalf("collection attribute = %#v", attrs[0])
	}
	if ty, _ := page.GetType(); ty != "telegram_collection" {
		t.Fatalf("type = %q", ty)
	}
}

// The preview is stored in the message and shown to every recipient, so it may
// only carry what an anonymous visitor of the public link page sees -- not what
// the sender happens to be allowed to see.
func TestInternalProfilePreviewShowsOnlyPublicFields(t *testing.T) {
	ctx := context.Background()
	users := memory.NewUserStore()
	alice, err := users.Create(ctx, domain.User{AccessHash: 81, Phone: "15550008101", FirstName: "Alice", Username: "public_alice", About: "bio for contacts"})
	if err != nil {
		t.Fatal(err)
	}
	bob, err := users.Create(ctx, domain.User{AccessHash: 82, Phone: "15550008102", FirstName: "Bob"})
	if err != nil {
		t.Fatal(err)
	}
	contacts := memory.NewContactStore()
	if _, err := contacts.Upsert(ctx, alice.ID, domain.ContactInput{ContactUserID: bob.ID, FirstName: "Bob"}); err != nil {
		t.Fatal(err)
	}
	privacy := appprivacy.NewService(memory.NewPrivacyStore(), contacts)
	if _, err := privacy.SetRules(ctx, alice.ID, domain.PrivacyKeyAbout, []domain.PrivacyRule{{Kind: domain.PrivacyRuleAllowContacts}}); err != nil {
		t.Fatal(err)
	}
	if visible, err := privacy.CanSee(ctx, alice.ID, bob.ID, domain.PrivacyKeyAbout); err != nil || !visible {
		t.Fatalf("bob can see alice's about = %v, %v; the test needs a viewer who may", visible, err)
	}
	r := New(Config{PublicBaseURL: "https://links.example.org"}, Deps{Users: appusers.NewService(users), Privacy: privacy}, zaptest.NewLogger(t), clock.System)
	bobCtx := WithUserID(ctx, bob.ID)

	page, ok := r.resolveInternalWebPage(bobCtx, "https://links.example.org/public_alice")
	if !ok || page.Type != webPageTypeUser || page.Title != "Alice" {
		t.Fatalf("preview = %+v ok=%v", page, ok)
	}
	if page.Description != "" {
		t.Fatalf("contacts-only about leaked into the stored preview: %q", page.Description)
	}

	if _, err := privacy.SetRules(ctx, alice.ID, domain.PrivacyKeyAbout, []domain.PrivacyRule{{Kind: domain.PrivacyRuleAllowAll}}); err != nil {
		t.Fatal(err)
	}
	if page, ok = r.resolveInternalWebPage(bobCtx, "https://links.example.org/public_alice"); !ok || page.Description != "bio for contacts" {
		t.Fatalf("public about preview = %+v ok=%v", page, ok)
	}
	if _, ok := r.resolveInternalWebPage(bobCtx, "https://t.me/public_alice"); ok {
		t.Fatal("a t.me link was answered from local data")
	}
}

type collectionPreviewGifts struct {
	GiftsService
	filter domain.SavedStarGiftFilter
}

func (g *collectionPreviewGifts) ListCollections(_ context.Context, owner domain.Peer) ([]domain.StarGiftCollection, error) {
	return []domain.StarGiftCollection{{Owner: owner, CollectionID: 3, Title: "Best"}}, nil
}

func (g *collectionPreviewGifts) ListSavedFiltered(_ context.Context, filter domain.SavedStarGiftFilter) (domain.SavedStarGiftPage, error) {
	g.filter = filter
	return domain.SavedStarGiftPage{}, nil
}

func TestInternalCollectionPreviewExcludesHiddenGifts(t *testing.T) {
	ctx := context.Background()
	users := memory.NewUserStore()
	owner, err := users.Create(ctx, domain.User{AccessHash: 83, Phone: "15550008103", FirstName: "Owner", Username: "gift_owner"})
	if err != nil {
		t.Fatal(err)
	}
	gifts := &collectionPreviewGifts{}
	r := New(Config{PublicBaseURL: "https://links.example.org"}, Deps{Users: appusers.NewService(users), Gifts: gifts}, zaptest.NewLogger(t), clock.System)

	// The owner is the sender here: even the owner's own preview is public.
	page, ok := r.resolveInternalWebPage(WithUserID(ctx, owner.ID), "https://links.example.org/gift_owner/c/3")
	if !ok || page.Type != webPageTypeCollection || page.Title != "Best" {
		t.Fatalf("collection preview = %+v ok=%v", page, ok)
	}
	if !gifts.filter.ExcludeUnsaved || gifts.filter.CollectionID != 3 || gifts.filter.Owner.ID != owner.ID {
		t.Fatalf("collection icons filter = %+v, want hidden gifts excluded", gifts.filter)
	}
}
