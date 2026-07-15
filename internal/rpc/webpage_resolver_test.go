package rpc

import (
	"context"
	"testing"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
)

// TestTgWebPageUpdateFromEvent 验证 web_page 事件投影为 updateWebPage（携带已解析 webPage + pts）。
func TestTgWebPageUpdateFromEvent(t *testing.T) {
	urlHash := domain.WebPageURLHash("https://example.com/x")
	event := domain.UpdateEvent{
		UserID: 1, Type: domain.UpdateEventWebPage, Pts: 5, PtsCount: 1, Date: 1700000100,
		Message: domain.Message{
			ID: 10, OwnerUserID: 1, Peer: domain.Peer{Type: domain.PeerTypeUser, ID: 2},
			Media: &domain.MessageMedia{
				Kind:    domain.MessageMediaKindWebPage,
				WebPage: &domain.MessageWebPage{State: domain.MessageWebPageStateDone, ID: urlHash, URL: "https://example.com/x", DisplayURL: "example.com", Title: "Example"},
			},
		},
	}
	update := tgOtherUpdateFromEvent(event)
	wp, ok := update.(*tg.UpdateWebPage)
	if !ok {
		t.Fatalf("update = %T, want *tg.UpdateWebPage", update)
	}
	if wp.Pts != 5 || wp.PtsCount != 1 {
		t.Errorf("pts = %d/%d, want 5/1", wp.Pts, wp.PtsCount)
	}
	page, ok := wp.Webpage.(*tg.WebPage)
	if !ok {
		t.Fatalf("webpage = %T, want *tg.WebPage", wp.Webpage)
	}
	if page.ID != urlHash {
		t.Errorf("webpage id = %d, want url_hash %d", page.ID, urlHash)
	}
	if v, _ := page.GetTitle(); v != "Example" {
		t.Errorf("title = %q, want Example", v)
	}
}

// TestTgChannelWebPageUpdateFromEvent 验证频道 web_page 事件投影为 updateChannelWebPage。
func TestTgChannelWebPageUpdateFromEvent(t *testing.T) {
	urlHash := domain.WebPageURLHash("https://example.com/c")
	event := domain.ChannelUpdateEvent{
		ChannelID: 555, Type: domain.ChannelUpdateWebPage, Pts: 9, PtsCount: 1, Date: 1700000100,
		Message: domain.ChannelMessage{
			ChannelID: 555, ID: 12,
			Media: &domain.MessageMedia{
				Kind:    domain.MessageMediaKindWebPage,
				WebPage: &domain.MessageWebPage{State: domain.MessageWebPageStateDone, ID: urlHash, URL: "https://example.com/c", DisplayURL: "example.com", Title: "Example"},
			},
		},
	}
	update := tgChannelUpdate(1, event)
	wp, ok := update.(*tg.UpdateChannelWebPage)
	if !ok {
		t.Fatalf("update = %T, want *tg.UpdateChannelWebPage", update)
	}
	if wp.ChannelID != 555 || wp.Pts != 9 || wp.PtsCount != 1 {
		t.Errorf("channel/pts = %d/%d/%d", wp.ChannelID, wp.Pts, wp.PtsCount)
	}
	page, ok := wp.Webpage.(*tg.WebPage)
	if !ok {
		t.Fatalf("webpage = %T, want *tg.WebPage", wp.Webpage)
	}
	if page.ID != urlHash {
		t.Errorf("webpage id = %d, want url_hash %d", page.ID, urlHash)
	}
}

// TestResolvePendingWebPageSwapsCard 验证 resolver glue：发送→解析→消息 media 就地变为 done 卡片。
func TestResolvePendingWebPageSwapsCard(t *testing.T) {
	ctx := context.Background()
	r, owner, friend := newMediaTestRouter(t)
	f := r.deps.Files.(*fakeFiles)
	f.webPagePreviewOn = true
	f.resolveWebPageFn = func(u string) (domain.MessageWebPage, error) {
		return domain.MessageWebPage{State: domain.MessageWebPageStateDone, ID: domain.WebPageURLHash(u), URL: u, DisplayURL: "example.com", Title: "Resolved"}, nil
	}

	const url = "https://example.com/x"
	urlHash := domain.WebPageURLHash(url)
	updates, err := r.onMessagesSendMessage(WithUserID(ctx, owner.ID), &tg.MessagesSendMessageRequest{
		Peer:     &tg.InputPeerUser{UserID: friend.ID, AccessHash: friend.AccessHash},
		Message:  "see " + url,
		Entities: []tg.MessageEntityClass{&tg.MessageEntityURL{Offset: 4, Length: 21}},
		RandomID: 6001,
	})
	if err != nil {
		t.Fatalf("sendMessage: %v", err)
	}
	msg := newMessageFromUpdates(t, updates)
	// echo 同步反映 pending 占位。
	if wrap, ok := msg.Media.(*tg.MessageMediaWebPage); !ok {
		t.Fatalf("echo media = %T, want pending webpage", msg.Media)
	} else if _, ok := wrap.Webpage.(*tg.WebPagePending); !ok {
		t.Fatalf("echo webpage = %T, want pending", wrap.Webpage)
	}

	// 同步解析（异步路径也会跑，但幂等收敛到同一 done 终态）。
	job := webPageResolveJob{
		senderID:   owner.ID,
		peer:       domain.Peer{Type: domain.PeerTypeUser, ID: friend.ID},
		msgID:      msg.ID,
		expectedID: urlHash,
		url:        url,
	}
	if err := r.resolvePendingWebPage(ctx, job); err != nil {
		t.Fatalf("resolvePendingWebPage: %v", err)
	}

	// 发送方消息 media 已就地替换为 done。
	got, found, err := r.lookupOwnerMessage(ctx, owner.ID, msg.ID)
	if err != nil || !found {
		t.Fatalf("lookupOwnerMessage: found=%v err=%v", found, err)
	}
	if got.Media == nil || got.Media.WebPage == nil || got.Media.WebPage.State != domain.MessageWebPageStateDone {
		t.Fatalf("owner media not resolved: %+v", got.Media)
	}
	if got.Media.WebPage.Title != "Resolved" {
		t.Errorf("resolved title = %q, want Resolved", got.Media.WebPage.Title)
	}
	if got.EditDate != 0 {
		t.Errorf("edit_date = %d, want 0 (no 'edited' marker)", got.EditDate)
	}
}
