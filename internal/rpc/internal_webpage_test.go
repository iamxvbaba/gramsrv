package rpc

import (
	"testing"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
)

func TestParseInternalLink(t *testing.T) {
	cases := []struct {
		raw, kind, username, slug string
		collection                int
	}{
		{"https://sgq.me/durov", "profile", "durov", "", 0},
		{"sgq.me/ShuzaGram_bot", "profile", "ShuzaGram_bot", "", 0},
		{"https://sgq.me/+AbCdEf123", "invite", "", "AbCdEf123", 0},
		{"https://sgq.me/joinchat/XyZ", "invite", "", "XyZ", 0},
		{"https://sgq.me/nft/PlushPepe-1", "nft", "", "PlushPepe-1", 0},
		{"https://sgq.me/auction/chillflame", "auction", "", "chillflame", 0},
		{"https://sgq.me/durov/c/3", "collection", "durov", "", 3},
		{"https://sgq.me/durov?collection=7", "collection", "durov", "", 7},
	}
	for _, c := range cases {
		link, ok := parseInternalLink(c.raw, "sgq.me")
		if !ok || link.kind != c.kind || link.username != c.username || link.slug != c.slug || link.collection != c.collection {
			t.Errorf("%s -> %+v ok=%v", c.raw, link, ok)
		}
	}
	for _, raw := range []string{
		"https://example.com/durov",   // other site
		"https://sgq.me/",             // no path
		"https://sgq.me/+79991234567", // phone link
		"https://sgq.me/ab",           // too short for a username
		"https://sgq.me/durov/123",    // message link
		"https://sgq.me/durov/c/x",
	} {
		if link, ok := parseInternalLink(raw, "sgq.me"); ok {
			t.Errorf("%s parsed as %+v", raw, link)
		}
	}
}

func TestInternalWebPageAttributes(t *testing.T) {
	page := tgWebPage(domain.MessageWebPage{
		State: domain.MessageWebPageStateDone, ID: 1, URL: "https://sgq.me/durov/c/1", DisplayURL: "sgq.me/durov/c/1",
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
