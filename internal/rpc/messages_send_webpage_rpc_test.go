package rpc

import (
	"context"
	"testing"
	"time"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
)

// "see https://example.com/x" 的 URL 在 UTF-16 偏移 4、长度 21。
const (
	wpTestMessage  = "see https://example.com/x"
	wpTestURL      = "https://example.com/x"
	wpURLEntityOff = 4
	wpURLEntityLen = 21
)

func wpURLEntities() []tg.MessageEntityClass {
	return []tg.MessageEntityClass{&tg.MessageEntityURL{Offset: wpURLEntityOff, Length: wpURLEntityLen}}
}

// TestSendMessageAttachesWebPagePending 验证启用预览时,带 URL 的文本消息挂上 pending 占位
// (id==url_hash 保证与 done 解析关联),并投影 invert_media。
func TestSendMessageAttachesWebPagePending(t *testing.T) {
	ctx := context.Background()
	r, owner, friend := newMediaTestRouter(t)
	r.deps.Files.(*fakeFiles).webPagePreviewOn = true
	now := time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC)
	r.clock = fixedClock{now: now}

	updates, err := r.onMessagesSendMessage(WithUserID(ctx, owner.ID), &tg.MessagesSendMessageRequest{
		Peer:        &tg.InputPeerUser{UserID: friend.ID, AccessHash: friend.AccessHash},
		Message:     wpTestMessage,
		Entities:    wpURLEntities(),
		RandomID:    5101,
		InvertMedia: true,
	})
	if err != nil {
		t.Fatalf("sendMessage: %v", err)
	}
	msg := newMessageFromUpdates(t, updates)

	wrap, ok := msg.Media.(*tg.MessageMediaWebPage)
	if !ok {
		t.Fatalf("media = %T, want *tg.MessageMediaWebPage", msg.Media)
	}
	pending, ok := wrap.Webpage.(*tg.WebPagePending)
	if !ok {
		t.Fatalf("webpage = %T, want *tg.WebPagePending", wrap.Webpage)
	}
	wantID := domain.WebPageURLHash(wpTestURL)
	if pending.ID != wantID {
		t.Errorf("pending id = %d, want url_hash %d", pending.ID, wantID)
	}
	if url, _ := pending.GetURL(); url != wpTestURL {
		t.Errorf("pending url = %q, want %q", url, wpTestURL)
	}
	wantDeadline := int(now.Add(webPagePendingLifetime).Unix())
	if pending.Date != wantDeadline {
		t.Errorf("pending date = %d, want retry deadline %d", pending.Date, wantDeadline)
	}
	if time.Unix(int64(pending.Date), 0).Sub(now) <= webPageResolveTimeout {
		t.Errorf("pending deadline must outlive resolver timeout: deadline=%s timeout=%s", time.Unix(int64(pending.Date), 0), webPageResolveTimeout)
	}
	if !msg.InvertMedia {
		t.Errorf("invert_media not projected onto message")
	}
}

// TestSendChannelMessageUsesSameFutureWebPageDeadline 锁定频道发送也经过同一 pending
// 截止时间构造路径，避免只修私聊 echo 而频道仍被客户端立即判定过期。
func TestSendChannelMessageUsesSameFutureWebPageDeadline(t *testing.T) {
	ctx := context.Background()
	r, owner, channel := newRichChannelTestRouter(t)
	r.deps.Files.(*fakeFiles).webPagePreviewOn = true
	// 本测试只验证发送投影，不启动异步解析 goroutine。
	r.webPageResolveSem = nil
	now := time.Date(2030, time.February, 3, 4, 5, 6, 0, time.UTC)
	r.clock = fixedClock{now: now}

	updates, err := r.onMessagesSendMessage(WithUserID(ctx, owner.ID), &tg.MessagesSendMessageRequest{
		Peer:     &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		Message:  wpTestMessage,
		Entities: wpURLEntities(),
		RandomID: 5106,
	})
	if err != nil {
		t.Fatalf("send channel message: %v", err)
	}
	msg := newMessageFromUpdates(t, updates)
	wrap, ok := msg.Media.(*tg.MessageMediaWebPage)
	if !ok {
		t.Fatalf("channel media = %T, want *tg.MessageMediaWebPage", msg.Media)
	}
	pending, ok := wrap.Webpage.(*tg.WebPagePending)
	if !ok {
		t.Fatalf("channel webpage = %T, want *tg.WebPagePending", wrap.Webpage)
	}
	if want := int(now.Add(webPagePendingLifetime).Unix()); pending.Date != want {
		t.Errorf("channel pending date = %d, want retry deadline %d", pending.Date, want)
	}
}

func TestSendChannelMessageAllowsAtPathSegmentURL(t *testing.T) {
	ctx := context.Background()
	r, owner, channel := newRichChannelTestRouter(t)

	const message = "https://github.com/@11"
	updates, err := r.onMessagesSendMessage(WithUserID(ctx, owner.ID), &tg.MessagesSendMessageRequest{
		Peer:     &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		Message:  message,
		RandomID: 5107,
	})
	if err != nil {
		t.Fatalf("send channel message with @ path segment URL: %v", err)
	}
	msg := newMessageFromUpdates(t, updates)
	if msg.Message != message {
		t.Fatalf("message = %q, want %q", msg.Message, message)
	}
	var urls, mentions int
	for _, entity := range msg.Entities {
		switch entity.(type) {
		case *tg.MessageEntityURL:
			urls++
		case *tg.MessageEntityMention:
			mentions++
		}
	}
	if urls != 1 || mentions != 0 {
		t.Fatalf("entities url=%d mention=%d, want url=1 mention=0: %#v", urls, mentions, msg.Entities)
	}
}

func TestSendChannelMessageAllowsBareDomainAtPathSegmentURL(t *testing.T) {
	ctx := context.Background()
	r, owner, channel := newRichChannelTestRouter(t)

	const message = "github.com/@alice"
	updates, err := r.onMessagesSendMessage(WithUserID(ctx, owner.ID), &tg.MessagesSendMessageRequest{
		Peer:     &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		Message:  message,
		RandomID: 5108,
	})
	if err != nil {
		t.Fatalf("send channel message with bare @ path segment URL: %v", err)
	}
	msg := newMessageFromUpdates(t, updates)
	if msg.Message != message {
		t.Fatalf("message = %q, want %q", msg.Message, message)
	}
	var urls, mentions int
	for _, entity := range msg.Entities {
		switch entity.(type) {
		case *tg.MessageEntityURL:
			urls++
		case *tg.MessageEntityMention:
			mentions++
		}
	}
	if urls != 1 || mentions != 0 {
		t.Fatalf("entities url=%d mention=%d, want url=1 mention=0: %#v", urls, mentions, msg.Entities)
	}
}

// TestSendMessageAttachesCachedDoneCard 验证：URL 已缓存解析时，发送 echo 直接带 done 卡片
// （非 pending）——官方行为，TDesktop 据此立即渲染、不依赖异步换卡。
func TestSendMessageAttachesCachedDoneCard(t *testing.T) {
	ctx := context.Background()
	r, owner, friend := newMediaTestRouter(t)
	f := r.deps.Files.(*fakeFiles)
	f.webPagePreviewOn = true
	f.lookupWebPageFn = func(u string) (domain.MessageWebPage, bool) {
		return domain.MessageWebPage{State: domain.MessageWebPageStateDone, ID: domain.WebPageURLHash(u), URL: u, DisplayURL: "example.com", Title: "Cached"}, true
	}
	updates, err := r.onMessagesSendMessage(WithUserID(ctx, owner.ID), &tg.MessagesSendMessageRequest{
		Peer: &tg.InputPeerUser{UserID: friend.ID, AccessHash: friend.AccessHash}, Message: wpTestMessage, Entities: wpURLEntities(), RandomID: 5201,
	})
	if err != nil {
		t.Fatalf("sendMessage: %v", err)
	}
	msg := newMessageFromUpdates(t, updates)
	wrap, ok := msg.Media.(*tg.MessageMediaWebPage)
	if !ok {
		t.Fatalf("media = %T, want *tg.MessageMediaWebPage", msg.Media)
	}
	page, ok := wrap.Webpage.(*tg.WebPage)
	if !ok {
		t.Fatalf("echo webpage = %T, want *tg.WebPage (done card directly)", wrap.Webpage)
	}
	if v, _ := page.GetTitle(); v != "Cached" {
		t.Errorf("title = %q, want Cached", v)
	}
}

// TestSendMessageNoWebpageSuppressesPreview 验证 no_webpage 抑制占位。
func TestSendMessageNoWebpageSuppressesPreview(t *testing.T) {
	ctx := context.Background()
	r, owner, friend := newMediaTestRouter(t)
	r.deps.Files.(*fakeFiles).webPagePreviewOn = true

	updates, err := r.onMessagesSendMessage(WithUserID(ctx, owner.ID), &tg.MessagesSendMessageRequest{
		Peer:      &tg.InputPeerUser{UserID: friend.ID, AccessHash: friend.AccessHash},
		Message:   wpTestMessage,
		Entities:  wpURLEntities(),
		RandomID:  5102,
		NoWebpage: true,
	})
	if err != nil {
		t.Fatalf("sendMessage: %v", err)
	}
	if msg := newMessageFromUpdates(t, updates); msg.Media != nil {
		t.Fatalf("media = %T, want nil (no_webpage)", msg.Media)
	}
}

// TestSendMessagePreviewDisabledNoPlaceholder 验证未启用预览时不挂占位(否则永久 pending)。
func TestSendMessagePreviewDisabledNoPlaceholder(t *testing.T) {
	ctx := context.Background()
	r, owner, friend := newMediaTestRouter(t)
	// webPagePreviewOn 默认 false。

	updates, err := r.onMessagesSendMessage(WithUserID(ctx, owner.ID), &tg.MessagesSendMessageRequest{
		Peer:     &tg.InputPeerUser{UserID: friend.ID, AccessHash: friend.AccessHash},
		Message:  wpTestMessage,
		Entities: wpURLEntities(),
		RandomID: 5103,
	})
	if err != nil {
		t.Fatalf("sendMessage: %v", err)
	}
	if msg := newMessageFromUpdates(t, updates); msg.Media != nil {
		t.Fatalf("media = %T, want nil (preview disabled)", msg.Media)
	}
}

// TestSendMediaInputWebPageAttachesPending 验证 sendMedia 的 InputMediaWebPage 经降级到
// sendMessage 后,仍从文本 URL 挂上 pending 占位(启用预览时)。
func TestSendMediaInputWebPageAttachesPending(t *testing.T) {
	ctx := context.Background()
	r, owner, friend := newMediaTestRouter(t)
	r.deps.Files.(*fakeFiles).webPagePreviewOn = true

	updates, err := r.onMessagesSendMedia(WithUserID(ctx, owner.ID), &tg.MessagesSendMediaRequest{
		Peer:     &tg.InputPeerUser{UserID: friend.ID, AccessHash: friend.AccessHash},
		Media:    &tg.InputMediaWebPage{URL: wpTestURL},
		Message:  wpTestMessage,
		Entities: wpURLEntities(),
		RandomID: 5105,
	})
	if err != nil {
		t.Fatalf("sendMedia webpage: %v", err)
	}
	msg := newMessageFromUpdates(t, updates)
	wrap, ok := msg.Media.(*tg.MessageMediaWebPage)
	if !ok {
		t.Fatalf("media = %T, want *tg.MessageMediaWebPage", msg.Media)
	}
	if _, ok := wrap.Webpage.(*tg.WebPagePending); !ok {
		t.Fatalf("webpage = %T, want *tg.WebPagePending", wrap.Webpage)
	}
}

// TestSendMessageHighlightsBareURL 验证服务端为不带 url 实体的消息（如 TDesktop 发的）补
// MessageEntityURL，使链接高亮——独立于预览是否启用。
func TestSendMessageHighlightsBareURL(t *testing.T) {
	ctx := context.Background()
	r, owner, friend := newMediaTestRouter(t)
	updates, err := r.onMessagesSendMessage(WithUserID(ctx, owner.ID), &tg.MessagesSendMessageRequest{
		Peer:     &tg.InputPeerUser{UserID: friend.ID, AccessHash: friend.AccessHash},
		Message:  "see https://example.com/x",
		RandomID: 5301, // 无 entities，模拟 TDesktop
	})
	if err != nil {
		t.Fatalf("sendMessage: %v", err)
	}
	msg := newMessageFromUpdates(t, updates)
	var found bool
	for _, e := range msg.Entities {
		if u, ok := e.(*tg.MessageEntityURL); ok && u.Offset == 4 && u.Length == 21 {
			found = true
		}
	}
	if !found {
		t.Fatalf("sent message missing url highlight entity: %+v", msg.Entities)
	}
}

// TestSendMessageHighlightsConfiguredAppLink locks the server-only compatibility
// contract: clients may omit the custom-scheme entity, while the persisted/echoed
// message still carries MessageEntityURL and never starts webpage resolution.
func TestSendMessageHighlightsConfiguredAppLink(t *testing.T) {
	ctx := context.Background()
	r, owner, friend := newMediaTestRouter(t)
	r.deps.Files.(*fakeFiles).webPagePreviewOn = true
	message := "👍 sg://resolve?domain=Alice"

	updates, err := r.onMessagesSendMessage(WithUserID(ctx, owner.ID), &tg.MessagesSendMessageRequest{
		Peer:     &tg.InputPeerUser{UserID: friend.ID, AccessHash: friend.AccessHash},
		Message:  message,
		RandomID: 5302,
	})
	if err != nil {
		t.Fatalf("sendMessage: %v", err)
	}
	msg := newMessageFromUpdates(t, updates)
	if msg.Media != nil {
		t.Fatalf("custom app-link media = %T, want nil", msg.Media)
	}
	for _, entity := range msg.Entities {
		if url, ok := entity.(*tg.MessageEntityURL); ok && url.Offset == 3 && url.Length == utf16CodeUnitLen("sg://resolve?domain=Alice") {
			return
		}
	}
	t.Fatalf("sent message missing configured app-link entity: %+v", msg.Entities)
}

// TestSendMessageFillsCustomLinkBesideClientHTTPEntity covers the partial-client
// case exposed by Android: an existing HTTP entity must not suppress app-link detection.
func TestSendMessageFillsCustomLinkBesideClientHTTPEntity(t *testing.T) {
	ctx := context.Background()
	r, owner, friend := newMediaTestRouter(t)
	message := "https://x sg://resolve?domain=Alice"

	updates, err := r.onMessagesSendMessage(WithUserID(ctx, owner.ID), &tg.MessagesSendMessageRequest{
		Peer:     &tg.InputPeerUser{UserID: friend.ID, AccessHash: friend.AccessHash},
		Message:  message,
		Entities: []tg.MessageEntityClass{&tg.MessageEntityURL{Offset: 0, Length: utf16CodeUnitLen("https://x")}},
		RandomID: 5303,
	})
	if err != nil {
		t.Fatalf("sendMessage: %v", err)
	}
	msg := newMessageFromUpdates(t, updates)
	if len(msg.Entities) != 2 {
		t.Fatalf("message entities = %+v, want client HTTP plus server app-link", msg.Entities)
	}
	custom, ok := msg.Entities[1].(*tg.MessageEntityURL)
	if !ok || custom.Offset != utf16CodeUnitLen("https://x ") || custom.Length != utf16CodeUnitLen("sg://resolve?domain=Alice") {
		t.Fatalf("custom entity = %#v", msg.Entities[1])
	}
}

func TestAutoEntityDerivationDoesNotMutateSendRequests(t *testing.T) {
	t.Run("send-message-replay", func(t *testing.T) {
		ctx := context.Background()
		r, owner, friend := newMediaTestRouter(t)
		req := &tg.MessagesSendMessageRequest{
			Peer:     &tg.InputPeerUser{UserID: friend.ID, AccessHash: friend.AccessHash},
			Message:  "sg://resolve?domain=Alice",
			RandomID: 5304,
		}
		first, err := r.onMessagesSendMessage(WithUserID(ctx, owner.ID), req)
		if err != nil {
			t.Fatalf("first sendMessage: %v", err)
		}
		if len(req.Entities) != 0 {
			t.Fatalf("sendMessage request was mutated: %+v", req.Entities)
		}
		if _, err := r.onMessagesSendMessage(WithUserID(ctx, owner.ID), req); err != nil {
			t.Fatalf("sendMessage replay: %v", err)
		}
		if got := newMessageFromUpdates(t, first); len(got.Entities) != 1 {
			t.Fatalf("first send entities = %+v, want derived app-link", got.Entities)
		}
	})

	t.Run("send-media-replay", func(t *testing.T) {
		ctx := context.Background()
		r, owner, friend := newMediaTestRouter(t)
		req := &tg.MessagesSendMediaRequest{
			Peer:     &tg.InputPeerUser{UserID: friend.ID, AccessHash: friend.AccessHash},
			Media:    &tg.InputMediaContact{PhoneNumber: "+15550005305", FirstName: "Alice"},
			Message:  "sg://resolve?domain=Alice",
			RandomID: 5305,
		}
		first, err := r.onMessagesSendMedia(WithUserID(ctx, owner.ID), req)
		if err != nil {
			t.Fatalf("first sendMedia: %v", err)
		}
		if len(req.Entities) != 0 {
			t.Fatalf("sendMedia request was mutated: %+v", req.Entities)
		}
		if _, err := r.onMessagesSendMedia(WithUserID(ctx, owner.ID), req); err != nil {
			t.Fatalf("sendMedia replay: %v", err)
		}
		if got := newMessageFromUpdates(t, first); len(got.Entities) != 1 {
			t.Fatalf("first media caption entities = %+v, want derived app-link", got.Entities)
		}
	})
}

// TestSendMessageNoURLNoPlaceholder 验证无 URL 实体时不挂占位。
func TestSendMessageNoURLNoPlaceholder(t *testing.T) {
	ctx := context.Background()
	r, owner, friend := newMediaTestRouter(t)
	r.deps.Files.(*fakeFiles).webPagePreviewOn = true

	updates, err := r.onMessagesSendMessage(WithUserID(ctx, owner.ID), &tg.MessagesSendMessageRequest{
		Peer:     &tg.InputPeerUser{UserID: friend.ID, AccessHash: friend.AccessHash},
		Message:  "just plain text, no link",
		RandomID: 5104,
	})
	if err != nil {
		t.Fatalf("sendMessage: %v", err)
	}
	if msg := newMessageFromUpdates(t, updates); msg.Media != nil {
		t.Fatalf("media = %T, want nil (no url)", msg.Media)
	}
}
