package web

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"telesrv/internal/domain"
)

func newTestHandler(t *testing.T, resolver StickerSetResolver, publicBaseURL string) http.Handler {
	t.Helper()
	h, err := NewHandler(Config{StickerSets: resolver, PublicBaseURL: publicBaseURL})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	return h
}

func newTestHandlerWithPublicPeers(
	t *testing.T,
	resolver StickerSetResolver,
	users UsernameResolver,
	channels PublicChannelResolver,
	privacy AnonymousPrivacyResolver,
	photos ProfilePhotoResolver,
	publicBaseURL string,
) http.Handler {
	t.Helper()
	h, err := NewHandler(Config{
		StickerSets:   resolver,
		Users:         users,
		Channels:      channels,
		Privacy:       privacy,
		Photos:        photos,
		PublicBaseURL: publicBaseURL,
	})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	return h
}

func TestHandlerServesStickerSetLandingPage(t *testing.T) {
	resolver := fakeResolver{
		"fresh_pack": {
			ID:        10,
			ShortName: "fresh_pack",
			Title:     "Fresh Pack",
			Count:     2,
			Kind:      domain.StickerSetKindStickers,
		},
	}
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/addstickers/fresh_pack", nil)

	newTestHandler(t, resolver, "https://telesrv.net/").ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	for _, want := range []string{
		"Fresh Pack",
		"https://telesrv.net/addstickers/fresh_pack",
		"telesrv://addstickers?set=fresh_pack",
		"tg://addstickers?set=fresh_pack",
		"Files are still fetched by the app through MTProto.",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("body missing %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, `window.location.href = "tg://`) {
		t.Fatalf("landing page must not auto-open tg:// and steal official Telegram:\n%s", body)
	}
	if strings.Contains(body, "/upload/getFile") {
		t.Fatalf("landing page should not expose media download paths:\n%s", body)
	}
}

func TestHandlerServesEmojiLandingPage(t *testing.T) {
	resolver := fakeResolver{
		"emoji_pack": {
			ID:        11,
			ShortName: "emoji_pack",
			Title:     "Emoji Pack",
			Count:     1,
			Kind:      domain.StickerSetKindEmoji,
			Emojis:    true,
		},
	}
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/addemoji/emoji_pack", nil)

	newTestHandler(t, resolver, "https://example.test/base").ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	for _, want := range []string{
		"custom emoji set",
		"https://example.test/base/addemoji/emoji_pack",
		"telesrv://addemoji?set=emoji_pack",
		"tg://addemoji?set=emoji_pack",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("body missing %q:\n%s", want, body)
		}
	}
}

func TestHandlerServesChatlistLandingPage(t *testing.T) {
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/addlist/zNhytIbwRwjaC2GH", nil)

	newTestHandler(t, fakeResolver{}, "http://127.0.0.1:2401").ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	for _, want := range []string{
		"Shared Folder",
		"http://127.0.0.1:2401/addlist/zNhytIbwRwjaC2GH",
		"telesrv://addlist?slug=zNhytIbwRwjaC2GH",
		"tg://addlist?slug=zNhytIbwRwjaC2GH",
		"preview and add this shared folder",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("body missing %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, `window.location.href = "tg://`) {
		t.Fatalf("landing page must not auto-open tg:// and steal official Telegram:\n%s", body)
	}
}

func TestHandlerServesBotUsernameLandingPage(t *testing.T) {
	users := fakeUsers{
		"tetrisbot": {
			ID:        1001,
			Username:  "TetrisBot",
			FirstName: "Tetris Bot",
			Bot:       true,
		},
	}
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/TetrisBot", nil)

	newTestHandlerWithPublicPeers(t, fakeResolver{}, users, nil, nil, nil, "http://127.0.0.1:2401").ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	for _, want := range []string{
		"Tetris Bot",
		"bot",
		"@TetrisBot",
		"http://127.0.0.1:2401/TetrisBot",
		"telesrv://resolve?domain=TetrisBot",
		"tg://resolve?domain=TetrisBot",
		"Start Bot",
		"Open telesrv to start a chat with this bot.",
		`property="og:title" content="Tetris Bot"`,
		`property="al:android:url" content="telesrv://resolve?domain=TetrisBot"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("body missing %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, `window.location.href = "tg://`) {
		t.Fatalf("landing page must not auto-open tg:// and steal official Telegram:\n%s", body)
	}
}

func TestHandlerUsesConfiguredClientLinksAndBrand(t *testing.T) {
	h, err := NewHandler(Config{
		StickerSets: fakeResolver{
			"stickers_pack": {ShortName: "stickers_pack", Title: "Stickers", Kind: domain.StickerSetKindStickers},
			"emoji_pack":    {ShortName: "emoji_pack", Title: "Emoji", Kind: domain.StickerSetKindEmoji, Emojis: true},
		},
		Users:         fakeUsers{"alice": {ID: 2001, Username: "Alice", FirstName: "Alice"}},
		PublicBaseURL: "https://links.example.test",
		AppScheme:     "example-chat",
		WebBaseURL:    "https://web.example.test/client/",
		AppName:       "Example Chat",
	})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/Alice?start=hello", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	for _, want := range []string{
		"example-chat://resolve?domain=Alice&amp;start=hello",
		"https://web.example.test/client/#?tgaddr=",
		"Example Chat",
		"Open Example Chat to send a message to @Alice.",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("body missing %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "telesrv://") || strings.Contains(body, "https://web.telesrv.net") {
		t.Fatalf("body contains stale default client link:\n%s", body)
	}
	for _, tc := range []struct {
		path string
		want string
	}{
		{path: "/addstickers/stickers_pack", want: "example-chat://addstickers?set=stickers_pack"},
		{path: "/addemoji/emoji_pack", want: "example-chat://addemoji?set=emoji_pack"},
		{path: "/addlist/shared-folder", want: "example-chat://addlist?slug=shared-folder"},
	} {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, tc.path, nil))
		if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), tc.want) || !strings.Contains(rr.Body.String(), "Example Chat") {
			t.Fatalf("%s response = %d %q, want configured link %q and brand", tc.path, rr.Code, rr.Body.String(), tc.want)
		}
	}
}

func TestNewHandlerRejectsInvalidClientLinkConfig(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  Config
	}{
		{name: "missing sticker resolver", cfg: Config{}},
		{name: "official scheme", cfg: Config{StickerSets: fakeResolver{}, AppScheme: "tg"}},
		{name: "invalid Web base URL", cfg: Config{StickerSets: fakeResolver{}, WebBaseURL: "file:///tmp/web"}},
		{name: "invalid app name", cfg: Config{StickerSets: fakeResolver{}, AppName: "bad\nname"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := NewHandler(tc.cfg); err == nil {
				t.Fatal("NewHandler succeeded, want error")
			}
		})
	}
}

func TestHandlerServesUserChannelAndSupergroupLandingPages(t *testing.T) {
	users := fakeUsers{
		"alice": {
			ID:         2001,
			AccessHash: 987654321,
			Phone:      "+15551234567",
			Username:   "Alice",
			FirstName:  "Alice",
			LastName:   "Example",
			About:      "Public bio",
			Verified:   true,
			LastSeenAt: 1700000000,
		},
	}
	channels := fakeChannels{
		"newsroom": {
			ID:                3001,
			Username:          "NewsRoom",
			Title:             "News Room",
			About:             "Public channel description",
			Broadcast:         true,
			ParticipantsCount: 12001,
			Verified:          true,
			PhotoID:           301,
		},
		"studygroup": {
			ID:                3002,
			Username:          "StudyGroup",
			Title:             "Study Group",
			About:             "A public supergroup",
			Megagroup:         true,
			ParticipantsCount: 1,
		},
	}
	photos := &fakePhotos{byID: map[int64]domain.Photo{
		301: {ID: 301, Sizes: []domain.PhotoSize{{Kind: domain.PhotoSizeKindDefault, Type: "c", W: 640, H: 640, Size: 12}}},
	}}
	handler := newTestHandlerWithPublicPeers(t, fakeResolver{}, users, channels, nil, photos, "https://telesrv.net")

	for _, tc := range []struct {
		path  string
		wants []string
	}{
		{
			path: "/aLiCe/",
			wants: []string{
				"Alice Example", "Public bio", "@Alice", "Send Message", "Verified",
				"https://telesrv.net/Alice", "telesrv://resolve?domain=Alice",
			},
		},
		{
			path: "/NewsRoom",
			wants: []string{
				"News Room", "Public channel description", "12 001 subscribers", "View Channel",
				"https://telesrv.net/NewsRoom", "telesrv://resolve?domain=NewsRoom",
				"/_public/avatar/NewsRoom/301",
			},
		},
		{
			path: "/StudyGroup",
			wants: []string{
				"Study Group", "A public supergroup", "1 member", "View Group",
			},
		},
	} {
		t.Run(tc.path, func(t *testing.T) {
			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, tc.path, nil))
			if rr.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
			}
			for _, want := range tc.wants {
				if !strings.Contains(rr.Body.String(), want) {
					t.Fatalf("body missing %q:\n%s", want, rr.Body.String())
				}
			}
			if tc.path == "/aLiCe/" && strings.Count(rr.Body.String(), `<p class="username">@Alice</p>`) != 1 {
				t.Fatalf("ordinary user username rendered more than once:\n%s", rr.Body.String())
			}
			if strings.Contains(rr.Body.String(), "+15551234567") || strings.Contains(rr.Body.String(), "987654321") || strings.Contains(rr.Body.String(), "1700000000") {
				t.Fatalf("private protocol fields leaked into page:\n%s", rr.Body.String())
			}
		})
	}
}

func TestHandlerPreservesBoundedResolveQueryAndOverridesDomain(t *testing.T) {
	handler := newTestHandlerWithPublicPeers(t, fakeResolver{}, fakeUsers{
		"tetrisbot": {ID: 2001, Username: "TetrisBot", FirstName: "Tetris", Bot: true},
	}, nil, nil, nil, "https://telesrv.net")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/TetrisBot?start=hello&ref=campaign&domain=EvilBot", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	for _, want := range []string{
		"telesrv://resolve?domain=TetrisBot&amp;ref=campaign&amp;start=hello",
		"tg://resolve?domain=TetrisBot&amp;ref=campaign&amp;start=hello",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("body missing sanitized query %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "EvilBot") {
		t.Fatalf("caller-controlled domain leaked into page:\n%s", body)
	}

	for _, target := range []string{
		"/TetrisBot?bad-key=value",
		"/TetrisBot?start=" + strings.Repeat("a", maxPublicLinkValueLen+1),
		"/TetrisBot?" + strings.Repeat("a", maxPublicLinkRawQuery+1),
	} {
		rr = httptest.NewRecorder()
		handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, target, nil))
		if rr.Code != http.StatusRequestURITooLong {
			t.Fatalf("%s status = %d, want 414", target, rr.Code)
		}
	}
}

func TestHandlerHonorsAnonymousAboutAndPhotoPrivacy(t *testing.T) {
	const userID int64 = 2001
	photos := &fakePhotos{
		photos: map[photoLookupKey]domain.Photo{
			{ownerType: domain.PeerTypeUser, ownerID: userID, kind: domain.ProfilePhotoKindProfile}: {
				ID: 10, Sizes: []domain.PhotoSize{{Kind: domain.PhotoSizeKindDefault, Type: "c", W: 640, H: 640, Size: 12}},
			},
			{ownerType: domain.PeerTypeUser, ownerID: userID, kind: domain.ProfilePhotoKindFallback}: {
				ID: 11, Sizes: []domain.PhotoSize{{Kind: domain.PhotoSizeKindDefault, Type: "c", W: 640, H: 640, Size: 12}},
			},
		},
	}
	privacy := fakeAnonymousPrivacy{
		domain.PrivacyKeyAbout:        false,
		domain.PrivacyKeyProfilePhoto: false,
	}
	handler := newTestHandlerWithPublicPeers(t, fakeResolver{}, fakeUsers{
		"alice": {ID: userID, Username: "Alice", FirstName: "Alice", About: "private biography"},
	}, nil, privacy, photos, "https://telesrv.net")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/Alice", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	if strings.Contains(body, "private biography") || strings.Contains(body, "/10") {
		t.Fatalf("private about or main photo leaked:\n%s", body)
	}
	if !strings.Contains(body, "/_public/avatar/Alice/11") {
		t.Fatalf("fallback public photo missing:\n%s", body)
	}
}

func TestHandlerServesBoundedCurrentAvatarWithETag(t *testing.T) {
	const userID int64 = 2001
	jpeg := []byte{0xff, 0xd8, 0xff, 0xe0, 0x00, 0x10, 'J', 'F', 'I', 'F', 0x00, 0x01}
	photos := &fakePhotos{
		photos: map[photoLookupKey]domain.Photo{
			{ownerType: domain.PeerTypeUser, ownerID: userID, kind: domain.ProfilePhotoKindProfile}: {
				ID: 99, Date: 1700000000,
				Sizes: []domain.PhotoSize{{Kind: domain.PhotoSizeKindDefault, Type: "c", W: 640, H: 640, Size: len(jpeg)}},
			},
		},
		files: map[string]domain.FileChunk{
			"photo:99:c": {Bytes: jpeg, MimeType: "image/jpeg", Total: int64(len(jpeg))},
		},
	}
	handler := newTestHandlerWithPublicPeers(t, fakeResolver{}, fakeUsers{
		"alice": {ID: userID, Username: "Alice", FirstName: "Alice"},
	}, nil, nil, photos, "https://telesrv.net")

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/_public/avatar/Alice/99", nil))
	if rr.Code != http.StatusOK || rr.Header().Get("Content-Type") != "image/jpeg" || rr.Body.String() != string(jpeg) {
		t.Fatalf("avatar response status=%d type=%q body=%x", rr.Code, rr.Header().Get("Content-Type"), rr.Body.Bytes())
	}
	if rr.Header().Get("ETag") == "" || rr.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("avatar headers = %+v", rr.Header())
	}
	etag := rr.Header().Get("ETag")
	req := httptest.NewRequest(http.MethodGet, "/_public/avatar/Alice/99", nil)
	req.Header.Set("If-None-Match", etag)
	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusNotModified || rr.Body.Len() != 0 {
		t.Fatalf("conditional avatar status=%d body=%x", rr.Code, rr.Body.Bytes())
	}

	for _, path := range []string{"/_public/avatar/Alice/100", "/_public/avatar/Missing/99"} {
		rr = httptest.NewRecorder()
		handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, path, nil))
		if rr.Code != http.StatusNotFound {
			t.Fatalf("%s status = %d, want 404", path, rr.Code)
		}
	}
}

func TestHandlerFailsFastForAmbiguousUsernameOwner(t *testing.T) {
	handler := newTestHandlerWithPublicPeers(t, fakeResolver{}, fakeUsers{
		"sharedname": {ID: 2001, Username: "SharedName", FirstName: "User"},
	}, fakeChannels{
		"sharedname": {ID: 3001, Username: "SharedName", Title: "Channel", Broadcast: true},
	}, nil, nil, "https://telesrv.net")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/SharedName", nil))
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("ambiguous owner status = %d, want 500", rr.Code)
	}
}

func TestHandlerReturnsTrustedUsernameNotFoundPage(t *testing.T) {
	handler := newTestHandlerWithPublicPeers(t, fakeResolver{}, fakeUsers{}, fakeChannels{}, nil, nil, "https://telesrv.net")
	for _, path := range []string{"/MissingName", "/bad-name", "/Nope"} {
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, path, nil))
		if rr.Code != http.StatusNotFound || !strings.Contains(rr.Body.String(), "Username not found") || !strings.Contains(rr.Body.String(), "noindex,nofollow") {
			t.Fatalf("%s status=%d body=%s", path, rr.Code, rr.Body.String())
		}
		if strings.Contains(rr.Body.String(), "telesrv://resolve") {
			t.Fatalf("not-found page contains a fabricated app link: %s", rr.Body.String())
		}
		if rr.Header().Get("Content-Security-Policy") == "" || rr.Header().Get("X-Frame-Options") != "DENY" {
			t.Fatalf("not-found security headers = %+v", rr.Header())
		}
	}
}

func TestPublicAvatarRejectsOversizedOrUnsafeBlob(t *testing.T) {
	const userID int64 = 2001
	photo := domain.Photo{
		ID:    99,
		Sizes: []domain.PhotoSize{{Kind: domain.PhotoSizeKindDefault, Type: "c", W: 640, H: 640, Size: 12}},
	}
	for _, tc := range []struct {
		name  string
		chunk domain.FileChunk
	}{
		{name: "oversized", chunk: domain.FileChunk{Bytes: []byte("x"), MimeType: "image/jpeg", Total: maxPublicAvatarBytes + 1}},
		{name: "unsafe mime", chunk: domain.FileChunk{Bytes: []byte("<svg></svg>"), MimeType: "image/svg+xml", Total: int64(len("<svg></svg>"))}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			photos := &fakePhotos{
				photos: map[photoLookupKey]domain.Photo{
					{ownerType: domain.PeerTypeUser, ownerID: userID, kind: domain.ProfilePhotoKindProfile}: photo,
				},
				files: map[string]domain.FileChunk{"photo:99:c": tc.chunk},
			}
			handler := newTestHandlerWithPublicPeers(t, fakeResolver{}, fakeUsers{
				"alice": {ID: userID, Username: "Alice", FirstName: "Alice"},
			}, nil, nil, photos, "https://telesrv.net")
			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/_public/avatar/Alice/99", nil))
			if rr.Code != http.StatusNotFound {
				t.Fatalf("status = %d, want 404; body=%s", rr.Code, rr.Body.String())
			}
		})
	}
}

func TestHandlerRedirectsMismatchedKindToCanonicalURL(t *testing.T) {
	resolver := fakeResolver{
		"emoji_pack": {
			ID:        11,
			ShortName: "emoji_pack",
			Title:     "Emoji Pack",
			Kind:      domain.StickerSetKindEmoji,
			Emojis:    true,
		},
	}
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/addstickers/emoji_pack", nil)

	newTestHandler(t, resolver, "https://telesrv.net").ServeHTTP(rr, req)

	if rr.Code != http.StatusPermanentRedirect {
		t.Fatalf("status = %d, want 308; body=%s", rr.Code, rr.Body.String())
	}
	if got, want := rr.Header().Get("Location"), "https://telesrv.net/addemoji/emoji_pack"; got != want {
		t.Fatalf("Location = %q, want %q", got, want)
	}
}

func TestHandlerNotFoundForMissingOrInvalidShortName(t *testing.T) {
	handler := newTestHandlerWithPublicPeers(t, fakeResolver{}, fakeUsers{
		"alice": {
			ID:        2001,
			Username:  "Alice",
			FirstName: "Alice",
		},
	}, nil, nil, nil, "https://telesrv.net")
	for _, path := range []string{
		"/addstickers/missing_pack",
		"/addstickers/bad-name",
		"/addemoji/%E4%B8%AD%E6%96%87",
		"/addlist/bad!slug",
		"/addlist/%E4%B8%AD%E6%96%87",
		"/MissingBot",
		"/bad-name-bot",
		"/1stBot",
	} {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		handler.ServeHTTP(rr, req)
		if rr.Code != http.StatusNotFound {
			t.Fatalf("%s status = %d, want 404", path, rr.Code)
		}
	}
}

func TestHandlerLookupErrorIsInternalServerError(t *testing.T) {
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/addstickers/fresh_pack", nil)

	newTestHandler(t, errorResolver{}, "https://telesrv.net").ServeHTTP(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rr.Code)
	}
}

type fakeResolver map[string]domain.StickerSet

func (f fakeResolver) ResolveStickerSet(_ context.Context, ref domain.StickerSetRef) (domain.StickerSet, []domain.Document, bool, error) {
	set, ok := f[ref.ShortName]
	if !ok {
		return domain.StickerSet{}, nil, false, nil
	}
	docs := make([]domain.Document, set.Count)
	return set, docs, true, nil
}

type errorResolver struct{}

func (errorResolver) ResolveStickerSet(context.Context, domain.StickerSetRef) (domain.StickerSet, []domain.Document, bool, error) {
	return domain.StickerSet{}, nil, false, errors.New("boom")
}

type fakeUsers map[string]domain.User

func (f fakeUsers) ByUsername(_ context.Context, username string) (domain.User, bool, error) {
	u, ok := f[strings.ToLower(strings.TrimPrefix(username, "@"))]
	return u, ok, nil
}

type fakeChannels map[string]domain.Channel

func (f fakeChannels) ResolvePublicChannelUsername(_ context.Context, _ int64, username string) (domain.Channel, bool, error) {
	ch, ok := f[strings.ToLower(strings.TrimPrefix(username, "@"))]
	return ch, ok, nil
}

type fakeAnonymousPrivacy map[domain.PrivacyKey]bool

func (f fakeAnonymousPrivacy) CanSeeAnonymous(_ context.Context, _ int64, key domain.PrivacyKey) (bool, error) {
	visible, ok := f[key]
	if !ok {
		return true, nil
	}
	return visible, nil
}

type photoLookupKey struct {
	ownerType domain.PeerType
	ownerID   int64
	kind      domain.ProfilePhotoKind
}

type fakePhotos struct {
	photos map[photoLookupKey]domain.Photo
	byID   map[int64]domain.Photo
	files  map[string]domain.FileChunk
}

func (f *fakePhotos) CurrentProfilePhotoKind(_ context.Context, ownerType domain.PeerType, ownerID int64, kind domain.ProfilePhotoKind) (domain.Photo, bool, error) {
	photo, ok := f.photos[photoLookupKey{ownerType: ownerType, ownerID: ownerID, kind: kind}]
	return photo, ok, nil
}

func (f *fakePhotos) GetPhoto(_ context.Context, id int64) (domain.Photo, bool, error) {
	photo, ok := f.byID[id]
	return photo, ok, nil
}

func (f *fakePhotos) GetFile(_ context.Context, req domain.FileDownloadRequest) (domain.FileChunk, bool, error) {
	chunk, ok := f.files[req.LocationKey]
	return chunk, ok, nil
}
