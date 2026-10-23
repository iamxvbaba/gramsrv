package rpc

import (
	"reflect"
	"testing"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/links"
)

var testDefaultAppLinks = func() links.AppLinkBuilder {
	appLinks, err := links.NewAppLinkBuilder("sg", "")
	if err != nil {
		panic(err)
	}
	return appLinks
}()

func testAugmentAutoEntities(message string, entities []tg.MessageEntityClass) []tg.MessageEntityClass {
	return augmentAutoEntities(message, entities, testDefaultAppLinks)
}

func testAugmentAutoEntitiesWithAppLinks(t *testing.T, message string, entities []tg.MessageEntityClass, scheme, base string) []tg.MessageEntityClass {
	t.Helper()
	appLinks, err := links.NewAppLinkBuilder(scheme, base)
	if err != nil {
		t.Fatal(err)
	}
	return augmentAutoEntities(message, entities, appLinks)
}

func TestFirstPreviewableURL(t *testing.T) {
	t.Run("text-url-entity", func(t *testing.T) {
		got, ok := firstPreviewableURL("click here", []tg.MessageEntityClass{
			&tg.MessageEntityTextURL{Offset: 0, Length: 5, URL: "https://example.com/a"},
		})
		if !ok || got != "https://example.com/a" {
			t.Fatalf("got (%q,%v)", got, ok)
		}
	})

	t.Run("url-entity-utf16-slice", func(t *testing.T) {
		// 含 4 字节 emoji（UTF-16 占 2 码元）前缀，验证按 UTF-16 偏移切片正确。
		msg := "👍 https://example.com/x done"
		// "👍"=2 units, " "=1 → URL 从 offset 3 起，长度 = len16("https://example.com/x")=21。
		got, ok := firstPreviewableURL(msg, []tg.MessageEntityClass{
			&tg.MessageEntityURL{Offset: 3, Length: 21},
		})
		if !ok || got != "https://example.com/x" {
			t.Fatalf("got (%q,%v), want https://example.com/x", got, ok)
		}
	})

	t.Run("first-of-many", func(t *testing.T) {
		got, ok := firstPreviewableURL("a b", []tg.MessageEntityClass{
			&tg.MessageEntityBold{Offset: 0, Length: 1},
			&tg.MessageEntityTextURL{Offset: 0, Length: 1, URL: "https://first.example/"},
			&tg.MessageEntityTextURL{Offset: 2, Length: 1, URL: "https://second.example/"},
		})
		if !ok || got != "https://first.example/" {
			t.Fatalf("got (%q,%v)", got, ok)
		}
	})

	t.Run("raw-text-fallback-no-entities", func(t *testing.T) {
		// TDesktop 不带 url 实体，依赖服务端扫原始文本。
		got, ok := firstPreviewableURL("check https://example.com/x bare text", nil)
		if !ok || got != "https://example.com/x" {
			t.Fatalf("raw-text fallback got (%q,%v), want https://example.com/x", got, ok)
		}
	})

	t.Run("raw-text-trim-trailing-punct", func(t *testing.T) {
		got, ok := firstPreviewableURL("见 https://example.com/x。", nil)
		if !ok || got != "https://example.com/x" {
			t.Fatalf("trailing punct trim got (%q,%v)", got, ok)
		}
	})

	t.Run("raw-text-bare-url", func(t *testing.T) {
		got, ok := firstPreviewableURL("https://github.com/golang/go", nil)
		if !ok || got != "https://github.com/golang/go" {
			t.Fatalf("bare url got (%q,%v)", got, ok)
		}
	})

	t.Run("raw-text-naked-domain-url", func(t *testing.T) {
		got, ok := firstPreviewableURL("check github.com/@alice", nil)
		if !ok || got != "https://github.com/@alice" {
			t.Fatalf("naked domain fallback got (%q,%v), want https://github.com/@alice", got, ok)
		}
	})

	t.Run("no-url-no-extract", func(t *testing.T) {
		if got, ok := firstPreviewableURL("plain text without any link", nil); ok {
			t.Fatalf("text without url should not extract, got %q", got)
		}
	})

	t.Run("non-http-entity-skipped", func(t *testing.T) {
		if _, ok := firstPreviewableURL("x", []tg.MessageEntityClass{
			&tg.MessageEntityTextURL{Offset: 0, Length: 1, URL: "ftp://example.com/x"},
		}); ok {
			t.Fatalf("ftp URL should be rejected")
		}
	})

	t.Run("custom-scheme-entity-does-not-preview", func(t *testing.T) {
		message := "sg://resolve?domain=Alice"
		entities := testAugmentAutoEntities(message, nil)
		if len(entities) != 1 {
			t.Fatalf("entities = %d, want 1", len(entities))
		}
		if got, ok := firstPreviewableURL(message, entities); ok {
			t.Fatalf("custom app-link must not become a webpage preview, got %q", got)
		}
	})

	t.Run("custom-scheme-before-http-still-previews-http", func(t *testing.T) {
		message := "sg://resolve?domain=Alice then https://example.com/x"
		entities := testAugmentAutoEntities(message, nil)
		got, ok := firstPreviewableURL(message, entities)
		if !ok || got != "https://example.com/x" {
			t.Fatalf("got (%q,%v), want https://example.com/x", got, ok)
		}
	})
}

func urlEntity(t *testing.T, e tg.MessageEntityClass) *tg.MessageEntityURL {
	t.Helper()
	u, ok := e.(*tg.MessageEntityURL)
	if !ok {
		t.Fatalf("entity = %T, want *tg.MessageEntityURL", e)
	}
	return u
}

// TestAugmentAutoEntitiesURL 验证服务端在客户端未带 url 实体时检测文本链接补 MessageEntityURL
// （高亮），UTF-16 偏移正确，多链接全检测，客户端已带 url 实体则不重复。
func TestAugmentAutoEntitiesURL(t *testing.T) {
	t.Run("detect-when-no-client-entities", func(t *testing.T) {
		got := testAugmentAutoEntities("see https://example.com/x now", nil)
		if len(got) != 1 {
			t.Fatalf("entities = %d, want 1", len(got))
		}
		e := urlEntity(t, got[0])
		if e.Offset != 4 || e.Length != 21 {
			t.Errorf("offset/length = %d/%d, want 4/21", e.Offset, e.Length)
		}
	})
	t.Run("utf16-offset-with-emoji", func(t *testing.T) {
		got := testAugmentAutoEntities("\U0001f44d https://x", nil) // 👍=2 units, space=1 → url at 3
		if len(got) != 1 {
			t.Fatalf("entities = %d, want 1", len(got))
		}
		if e := urlEntity(t, got[0]); e.Offset != 3 || e.Length != 9 {
			t.Errorf("offset/length = %d/%d, want 3/9", e.Offset, e.Length)
		}
	})
	t.Run("multiple-urls", func(t *testing.T) {
		if got := testAugmentAutoEntities("https://a.com and https://b.com", nil); len(got) != 2 {
			t.Fatalf("entities = %d, want 2", len(got))
		}
	})
	t.Run("respect-client-url-entity", func(t *testing.T) {
		ents := []tg.MessageEntityClass{&tg.MessageEntityURL{Offset: 0, Length: 9}}
		if got := testAugmentAutoEntities("https://x more https://y", ents); len(got) != 2 {
			t.Fatalf("server should fill the missing URL span, got %d", len(got))
		}
	})
	t.Run("trailing-punct-not-in-entity", func(t *testing.T) {
		got := testAugmentAutoEntities("见 https://example.com。", nil) // 见 ...。
		e := urlEntity(t, got[0])
		if e.Length != utf16CodeUnitLen("https://example.com") {
			t.Errorf("length = %d, want %d (trailing 。 excluded)", e.Length, utf16CodeUnitLen("https://example.com"))
		}
	})
	t.Run("at-path-segment-is-url-only", func(t *testing.T) {
		message := "https://github.com/@11"
		got := testAugmentAutoEntities(message, nil)
		if len(got) != 1 {
			t.Fatalf("entities = %d, want one URL entity: %#v", len(got), got)
		}
		e := urlEntity(t, got[0])
		if e.Offset != 0 || e.Length != utf16CodeUnitLen(message) {
			t.Fatalf("offset/length = %d/%d, want 0/%d", e.Offset, e.Length, utf16CodeUnitLen(message))
		}
	})
	t.Run("bare-domain-at-path-segment-is-url-only", func(t *testing.T) {
		message := "github.com/@alice"
		got := testAugmentAutoEntities(message, nil)
		if len(got) != 1 {
			t.Fatalf("entities = %d, want one URL entity: %#v", len(got), got)
		}
		e := urlEntity(t, got[0])
		if e.Offset != 0 || e.Length != utf16CodeUnitLen(message) {
			t.Fatalf("offset/length = %d/%d, want 0/%d", e.Offset, e.Length, utf16CodeUnitLen(message))
		}
	})
	t.Run("bare-domain-query-fragment-at-is-url-only", func(t *testing.T) {
		message := "github.com/path?u=@alice#@bob"
		got := testAugmentAutoEntities(message, nil)
		if len(got) != 1 {
			t.Fatalf("entities = %d, want one URL entity: %#v", len(got), got)
		}
		e := urlEntity(t, got[0])
		if e.Offset != 0 || e.Length != utf16CodeUnitLen(message) {
			t.Fatalf("offset/length = %d/%d, want 0/%d", e.Offset, e.Length, utf16CodeUnitLen(message))
		}
	})
	t.Run("bare-domain-without-path", func(t *testing.T) {
		got := testAugmentAutoEntities("see github.com now", nil)
		if len(got) != 1 {
			t.Fatalf("entities = %d, want one bare-domain URL entity: %#v", len(got), got)
		}
		e := urlEntity(t, got[0])
		if e.Offset != 4 || e.Length != utf16CodeUnitLen("github.com") {
			t.Fatalf("offset/length = %d/%d, want 4/%d", e.Offset, e.Length, utf16CodeUnitLen("github.com"))
		}
	})
	t.Run("email-domain-not-url", func(t *testing.T) {
		for _, e := range testAugmentAutoEntities("mail bob@example.com please", nil) {
			if _, ok := e.(*tg.MessageEntityURL); ok {
				t.Fatalf("email domain must not yield URL entity: %#v", e)
			}
		}
	})
	t.Run("unknown-tld-not-url", func(t *testing.T) {
		if got := testAugmentAutoEntities("see foo.invalidtldzz now", nil); len(got) != 0 {
			t.Fatalf("unknown TLD should not be URL, got %#v", got)
		}
	})
	t.Run("no-url-no-entities", func(t *testing.T) {
		if got := testAugmentAutoEntities("plain text", nil); len(got) != 0 {
			t.Fatalf("entities = %d, want 0", len(got))
		}
	})
	t.Run("configured-custom-scheme", func(t *testing.T) {
		message := "👍 SG://resolve?domain=Alice。"
		got := testAugmentAutoEntities(message, nil)
		if len(got) != 1 {
			t.Fatalf("entities = %d, want 1", len(got))
		}
		e := urlEntity(t, got[0])
		wantURL := "SG://resolve?domain=Alice"
		if e.Offset != 3 || e.Length != utf16CodeUnitLen(wantURL) {
			t.Fatalf("offset/length = %d/%d, want 3/%d", e.Offset, e.Length, utf16CodeUnitLen(wantURL))
		}
	})
	t.Run("mixed-client-http-and-missing-custom-scheme", func(t *testing.T) {
		message := "https://x sg://resolve?domain=Alice"
		client := []tg.MessageEntityClass{&tg.MessageEntityURL{Offset: 0, Length: utf16CodeUnitLen("https://x")}}
		got := testAugmentAutoEntities(message, client)
		if len(got) != 2 {
			t.Fatalf("entities = %d, want client HTTP plus server app-link", len(got))
		}
		e := urlEntity(t, got[1])
		if e.Offset != utf16CodeUnitLen("https://x ") || e.Length != utf16CodeUnitLen("sg://resolve?domain=Alice") {
			t.Fatalf("custom entity offset/length = %d/%d", e.Offset, e.Length)
		}
	})
	t.Run("unconfigured-scheme-rejected", func(t *testing.T) {
		if got := testAugmentAutoEntities("foo://resolve sg://resolve", nil); len(got) != 1 {
			t.Fatalf("entities = %d, want only configured sg link", len(got))
		}
	})
	t.Run("host-base-is-exact", func(t *testing.T) {
		message := "owpg://links.example.test/Alice owpg://other.example.test/Bob sg://resolve?domain=Carol"
		got := testAugmentAutoEntitiesWithAppLinks(t, message, nil, "sg", "owpg://links.example.test")
		if len(got) != 2 {
			t.Fatalf("entities = %d, want configured host-base and legacy link", len(got))
		}
	})
}

func TestExtractMentionUsernamesSkipsURLSpans(t *testing.T) {
	got := extractMentionUsernames("https://github.com/@11 hi @alice https://example.com/@bob?x=@carol github.com/@dave github.com/path?u=@erin#@frank", 10, nil)
	want := []string{"alice"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("extractMentionUsernames = %#v, want %#v", got, want)
	}
}

func TestExtractMentionUsernamesSkipsClientURLSpans(t *testing.T) {
	message := "github.com/@alice hi @bob"
	blocked := mentionScanBlockedSpansFromTGEntities(message, []tg.MessageEntityClass{
		&tg.MessageEntityURL{Offset: 0, Length: utf16CodeUnitLen("github.com/@alice")},
	})
	got := extractMentionUsernames(message, 10, blocked)
	want := []string{"bob"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("extractMentionUsernames = %#v, want %#v", got, want)
	}
}

// BenchmarkAugmentAutoEntities 量化发送热路径:纯文本(无触发字符)应零分配走快路径短路;
// 含 @mention/#hashtag/url 时才进入检测+分配。
func BenchmarkAugmentAutoEntities(b *testing.B) {
	b.Run("plain-text", func(b *testing.B) {
		msg := "hey everyone, just wanted to share some thoughts about the meeting today and tomorrow"
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			_ = testAugmentAutoEntities(msg, nil)
		}
	})
	b.Run("with-mention-hashtag-url", func(b *testing.B) {
		msg := "hi @alice please check #golang docs at https://example.com/x thanks"
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			_ = testAugmentAutoEntities(msg, nil)
		}
	})
}

// firstEntityOfType 返回切片里首个匹配类型的实体（找不到则 fail）。
func mentionAt(t *testing.T, got []tg.MessageEntityClass, off, ln int) {
	t.Helper()
	for _, e := range got {
		if m, ok := e.(*tg.MessageEntityMention); ok && m.Offset == off && m.Length == ln {
			return
		}
	}
	t.Fatalf("no MessageEntityMention at offset/length %d/%d in %#v", off, ln, got)
}

// TestAugmentAutoEntitiesMention 验证 @mention/#hashtag/$cashtag/bot command 的服务端检测、
// UTF-16 偏移、边界（email 不误判）与区间不重叠（不打进客户端富文本实体内部）。
func TestAugmentAutoEntitiesMention(t *testing.T) {
	t.Run("bare-mentions", func(t *testing.T) {
		// 对齐官方抓包：纯 "@G0ldenMods\n@NGame_Official" → 两个裸 messageEntityMention，含前导 @。
		got := testAugmentAutoEntities("@G0ldenMods\n@NGame_Official", nil)
		mentionAt(t, got, 0, 11)  // @G0ldenMods = 10+1
		mentionAt(t, got, 12, 15) // @NGame_Official = 14+1
	})
	t.Run("mention-mid-text", func(t *testing.T) {
		got := testAugmentAutoEntities("hi @alice see you", nil)
		mentionAt(t, got, 3, 6) // @alice
	})
	t.Run("email-not-a-mention", func(t *testing.T) {
		for _, e := range testAugmentAutoEntities("mail me at bob@example.com please", nil) {
			if _, ok := e.(*tg.MessageEntityMention); ok {
				t.Fatalf("email local@domain must not yield a mention: %#v", e)
			}
		}
	})
	t.Run("utf16-offset-with-emoji", func(t *testing.T) {
		// 👍(2 units) + space(1) → @bob 起于 offset 3。
		got := testAugmentAutoEntities("\U0001f44d @bob", nil)
		mentionAt(t, got, 3, 4)
	})
	t.Run("no-overlap-with-client-entity", func(t *testing.T) {
		// 客户端把 "@bob" 区间标成 textUrl（[offset 0,len 4)），服务端不得再补 mention。
		ents := []tg.MessageEntityClass{&tg.MessageEntityTextURL{Offset: 0, Length: 4, URL: "https://x"}}
		for _, e := range testAugmentAutoEntities("@bob", ents) {
			if _, ok := e.(*tg.MessageEntityMention); ok {
				t.Fatalf("mention must not overlap client entity: %#v", e)
			}
		}
	})
	t.Run("no-mention-inside-raw-url-with-client-url-entity", func(t *testing.T) {
		// 防回归:客户端带了某个 textUrl 实体（hasClientURL=true）但未包裹另一条裸 URL；
		// 裸 URL 路径里的 @scam / #frag 不得被误标成 mention/hashtag（钓鱼风险）。
		msg := "Docs see https://sgq.me/@scam and #promo"
		ents := []tg.MessageEntityClass{&tg.MessageEntityTextURL{Offset: 0, Length: 4, URL: "https://x"}}
		got := testAugmentAutoEntities(msg, ents)
		for _, e := range got {
			if m, ok := e.(*tg.MessageEntityMention); ok {
				t.Fatalf("mention must not be synthesised inside a raw URL: offset=%d len=%d", m.Offset, m.Length)
			}
		}
		// URL 之外的 #promo 仍应高亮。
		var hasPromo bool
		for _, e := range got {
			if _, ok := e.(*tg.MessageEntityHashtag); ok {
				hasPromo = true
			}
		}
		if !hasPromo {
			t.Fatalf("#promo outside the URL should still be a hashtag: %#v", got)
		}
	})
	t.Run("hashtag", func(t *testing.T) {
		var found bool
		for _, e := range testAugmentAutoEntities("love #golang here", nil) {
			if h, ok := e.(*tg.MessageEntityHashtag); ok && h.Offset == 5 && h.Length == 7 {
				found = true
			}
		}
		if !found {
			t.Fatalf("expected #golang hashtag at 5/7")
		}
	})
	t.Run("hashtag-all-digits-skipped", func(t *testing.T) {
		for _, e := range testAugmentAutoEntities("number #123 here", nil) {
			if _, ok := e.(*tg.MessageEntityHashtag); ok {
				t.Fatalf("leading-digit hashtag must be skipped: %#v", e)
			}
		}
	})
	t.Run("bot-command", func(t *testing.T) {
		var found bool
		for _, e := range testAugmentAutoEntities("/start@MyBot now", nil) {
			if c, ok := e.(*tg.MessageEntityBotCommand); ok && c.Offset == 0 && c.Length == 12 {
				found = true
			}
		}
		if !found {
			t.Fatalf("expected /start@MyBot bot command at 0/12")
		}
	})
	t.Run("slash-in-path-not-command", func(t *testing.T) {
		for _, e := range testAugmentAutoEntities("see and/or maybe", nil) {
			if _, ok := e.(*tg.MessageEntityBotCommand); ok {
				t.Fatalf("and/or must not be a bot command: %#v", e)
			}
		}
	})
	t.Run("cashtag", func(t *testing.T) {
		var found bool
		for _, e := range testAugmentAutoEntities("buy $USD now", nil) {
			if c, ok := e.(*tg.MessageEntityCashtag); ok && c.Offset == 4 && c.Length == 4 {
				found = true
			}
		}
		if !found {
			t.Fatalf("expected $USD cashtag at 4/4")
		}
	})
}
