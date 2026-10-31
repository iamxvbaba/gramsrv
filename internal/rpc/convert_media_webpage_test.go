package rpc

import (
	"encoding/json"
	"testing"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
)

// jsonRoundTripMedia replicates the postgres media codec (json.Marshal/Unmarshal)
// to verify the new web_page field survives a byte-level JSONB column round trip
// without losing Kind or payload.
func jsonRoundTripMedia(t *testing.T, m *domain.MessageMedia) *domain.MessageMedia {
	t.Helper()
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal media: %v", err)
	}
	var out domain.MessageMedia
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal media: %v", err)
	}
	if out.IsZero() {
		t.Fatalf("web_page media decoded as zero (Kind lost): %s", raw)
	}
	return &out
}

// TestTgMessageMediaWebPageDone verifies the done shape projects to
// messageMediaWebPage{webPage}, with all fields present and binary-encodable
// (catching missing non-optional id/url/display_url/hash).
func TestTgMessageMediaWebPageDone(t *testing.T) {
	src := &domain.MessageMedia{
		Kind: domain.MessageMediaKindWebPage,
		WebPage: &domain.MessageWebPage{
			State:           domain.MessageWebPageStateDone,
			ID:              0x1234abcd,
			URL:             "https://example.com/article",
			DisplayURL:      "example.com",
			Hash:            42,
			Type:            "article",
			SiteName:        "Example",
			Title:           "An Example Article",
			Description:     "A description of the article.",
			Author:          "Jane Doe",
			ForceLargeMedia: true,
			HasLargeMedia:   true,
			Photo: &domain.Photo{
				ID:         0x99,
				AccessHash: 0x55,
				DCID:       2,
				Sizes: []domain.PhotoSize{
					{Kind: domain.PhotoSizeKindDefault, Type: "x", W: 1280, H: 720, Size: 4096},
				},
			},
		},
	}

	got := tgMessageMedia(jsonRoundTripMedia(t, src))
	wrap, ok := got.(*tg.MessageMediaWebPage)
	if !ok {
		t.Fatalf("tgMessageMedia = %T, want *tg.MessageMediaWebPage", got)
	}
	if !wrap.ForceLargeMedia {
		t.Errorf("ForceLargeMedia = false, want true")
	}
	page, ok := wrap.Webpage.(*tg.WebPage)
	if !ok {
		t.Fatalf("Webpage = %T, want *tg.WebPage", wrap.Webpage)
	}
	if page.ID != src.WebPage.ID || page.URL != src.WebPage.URL || page.DisplayURL != src.WebPage.DisplayURL || page.Hash != src.WebPage.Hash {
		t.Errorf("non-optional fields mismatch: %+v", page)
	}
	if !page.HasLargeMedia {
		t.Errorf("HasLargeMedia = false, want true")
	}
	if v, ok := page.GetSiteName(); !ok || v != "Example" {
		t.Errorf("SiteName = %q (ok=%v), want Example", v, ok)
	}
	if v, ok := page.GetTitle(); !ok || v != "An Example Article" {
		t.Errorf("Title = %q (ok=%v)", v, ok)
	}
	if v, ok := page.GetAuthor(); !ok || v != "Jane Doe" {
		t.Errorf("Author = %q (ok=%v)", v, ok)
	}
	if photo, ok := page.GetPhoto(); !ok {
		t.Errorf("GetPhoto ok=false, want embedded photo")
	} else if _, isReal := photo.(*tg.Photo); !isReal {
		t.Errorf("GetPhoto = %T, want *tg.Photo", photo)
	}

	// Binary encoding confirms the non-optional fields are present
	// (messageMediaWebPage.EncodeBare errors when webpage is nil or a non-optional
	// field is missing).
	var buf bin.Buffer
	if err := wrap.Encode(&buf); err != nil {
		t.Fatalf("encode messageMediaWebPage: %v", err)
	}
}

func TestTgMessageMediaWebPageAiComposeToneAttribute(t *testing.T) {
	src := &domain.MessageMedia{
		Kind: domain.MessageMediaKindWebPage,
		WebPage: &domain.MessageWebPage{
			State:              domain.MessageWebPageStateDone,
			ID:                 123,
			URL:                "https://t.me/addstyle/ai-test",
			DisplayURL:         "t.me/addstyle/ai-test",
			Hash:               7,
			Type:               "telegram_aicomposetone",
			Title:              "Sharp",
			ComposeToneEmojiID: 99,
		},
	}
	got := tgMessageMedia(jsonRoundTripMedia(t, src))
	wrap, ok := got.(*tg.MessageMediaWebPage)
	if !ok {
		t.Fatalf("tgMessageMedia = %T, want *tg.MessageMediaWebPage", got)
	}
	page, ok := wrap.Webpage.(*tg.WebPage)
	if !ok {
		t.Fatalf("Webpage = %T, want *tg.WebPage", wrap.Webpage)
	}
	attrs, ok := page.GetAttributes()
	if !ok || len(attrs) != 1 {
		t.Fatalf("attributes = %#v ok=%v, want one", attrs, ok)
	}
	attr, ok := attrs[0].(*tg.WebPageAttributeAiComposeTone)
	if !ok || attr.EmojiID != 99 {
		t.Fatalf("attribute = %#v, want AiComposeTone emoji 99", attrs[0])
	}
}

// TestTgMessageMediaWebPagePending verifies the pending shape projects to webPagePending{id,url,date}.
func TestTgMessageMediaWebPagePending(t *testing.T) {
	src := &domain.MessageMedia{
		Kind: domain.MessageMediaKindWebPage,
		WebPage: &domain.MessageWebPage{
			State: domain.MessageWebPageStatePending,
			ID:    777,
			URL:   "https://pending.example/x",
			Date:  1700000000,
		},
	}
	got := tgMessageMedia(jsonRoundTripMedia(t, src))
	wrap, ok := got.(*tg.MessageMediaWebPage)
	if !ok {
		t.Fatalf("tgMessageMedia = %T, want *tg.MessageMediaWebPage", got)
	}
	pending, ok := wrap.Webpage.(*tg.WebPagePending)
	if !ok {
		t.Fatalf("Webpage = %T, want *tg.WebPagePending", wrap.Webpage)
	}
	if pending.ID != 777 || pending.Date != 1700000000 {
		t.Errorf("pending = %+v, want id=777 date=1700000000", pending)
	}
	if v, ok := pending.GetURL(); !ok || v != src.WebPage.URL {
		t.Errorf("pending URL = %q (ok=%v)", v, ok)
	}
	var buf bin.Buffer
	if err := wrap.Encode(&buf); err != nil {
		t.Fatalf("encode pending: %v", err)
	}
}

// TestTgMessageMediaWebPageEmpty verifies the empty shape projects to webPageEmpty{id}.
func TestTgMessageMediaWebPageEmpty(t *testing.T) {
	src := &domain.MessageMedia{
		Kind: domain.MessageMediaKindWebPage,
		WebPage: &domain.MessageWebPage{
			State: domain.MessageWebPageStateEmpty,
			ID:    555,
			URL:   "https://empty.example/y",
		},
	}
	got := tgMessageMedia(jsonRoundTripMedia(t, src))
	wrap, ok := got.(*tg.MessageMediaWebPage)
	if !ok {
		t.Fatalf("tgMessageMedia = %T, want *tg.MessageMediaWebPage", got)
	}
	empty, ok := wrap.Webpage.(*tg.WebPageEmpty)
	if !ok {
		t.Fatalf("Webpage = %T, want *tg.WebPageEmpty", wrap.Webpage)
	}
	if empty.ID != 555 {
		t.Errorf("empty.ID = %d, want 555", empty.ID)
	}
	var buf bin.Buffer
	if err := wrap.Encode(&buf); err != nil {
		t.Fatalf("encode empty: %v", err)
	}
}

// TestTgMessageMediaWebPageNilGuards verifies a nil WebPage does not panic and falls back to empty media.
func TestTgMessageMediaWebPageNilGuards(t *testing.T) {
	got := tgMessageMedia(&domain.MessageMedia{Kind: domain.MessageMediaKindWebPage})
	if _, ok := got.(*tg.MessageMediaEmpty); !ok {
		t.Fatalf("nil WebPage = %T, want *tg.MessageMediaEmpty", got)
	}
}
