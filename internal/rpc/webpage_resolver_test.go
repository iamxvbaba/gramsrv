package rpc

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
)

// TestTgWebPageUpdateFromEvent verifies a web_page event projects to updateWebPage
// (carrying the resolved webPage + pts).
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

// TestTgChannelWebPageUpdateFromEvent verifies a channel web_page event projects to
// updateChannelWebPage.
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

// TestResolvePendingWebPageSwapsCard verifies the resolver glue: send -> resolve ->
// message media becomes a done card in place.
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
	// The echo reflects the pending placeholder synchronously.
	if wrap, ok := msg.Media.(*tg.MessageMediaWebPage); !ok {
		t.Fatalf("echo media = %T, want pending webpage", msg.Media)
	} else if _, ok := wrap.Webpage.(*tg.WebPagePending); !ok {
		t.Fatalf("echo webpage = %T, want pending", wrap.Webpage)
	}

	// Synchronous resolve (the async path also runs, but converges idempotently to the
	// same done terminal state).
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

	// The sender's message media has been replaced in place with done.
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

func TestResolveExpiredPendingWebPageFailureSwapsEmpty(t *testing.T) {
	ctx := context.Background()
	r, owner, friend := newMediaTestRouter(t)
	f := r.deps.Files.(*fakeFiles)
	f.webPagePreviewOn = true
	f.resolveWebPageFn = func(string) (domain.MessageWebPage, error) {
		return domain.MessageWebPage{}, errors.New("dial timeout")
	}

	const url = "https://example.com/slow"
	urlHash := domain.WebPageURLHash(url)
	updates, err := r.onMessagesSendMessage(WithUserID(ctx, owner.ID), &tg.MessagesSendMessageRequest{
		Peer:     &tg.InputPeerUser{UserID: friend.ID, AccessHash: friend.AccessHash},
		Message:  "see " + url,
		Entities: []tg.MessageEntityClass{&tg.MessageEntityURL{Offset: 4, Length: len(url)}},
		RandomID: 6002,
	})
	if err != nil {
		t.Fatalf("sendMessage: %v", err)
	}
	msg := newMessageFromUpdates(t, updates)

	err = r.resolvePendingWebPage(ctx, webPageResolveJob{
		senderID:       owner.ID,
		peer:           domain.Peer{Type: domain.PeerTypeUser, ID: friend.ID},
		msgID:          msg.ID,
		expectedID:     urlHash,
		url:            url,
		emptyOnFailure: true,
	})
	if err != nil {
		t.Fatalf("resolvePendingWebPage(emptyOnFailure): %v", err)
	}

	got, found, err := r.lookupOwnerMessage(ctx, owner.ID, msg.ID)
	if err != nil || !found {
		t.Fatalf("lookupOwnerMessage: found=%v err=%v", found, err)
	}
	if got.Media == nil || got.Media.WebPage == nil || got.Media.WebPage.State != domain.MessageWebPageStateEmpty {
		t.Fatalf("owner media = %+v, want empty webpage", got.Media)
	}
	if got.Media.WebPage.ID != urlHash {
		t.Fatalf("empty webpage id = %d, want %d", got.Media.WebPage.ID, urlHash)
	}
}

func TestGetMessagesExpiredPendingWebPageConvergesToEmpty(t *testing.T) {
	ctx := context.Background()
	r, owner, friend := newMediaTestRouter(t)
	f := r.deps.Files.(*fakeFiles)
	f.webPagePreviewOn = true
	f.resolveWebPageFn = func(string) (domain.MessageWebPage, error) {
		return domain.MessageWebPage{}, errors.New("temporary fetch failure")
	}

	const url = "https://example.com/retry"
	urlHash := domain.WebPageURLHash(url)
	body := "see " + url
	entities := []tg.MessageEntityClass{&tg.MessageEntityURL{Offset: 4, Length: len(url)}}
	updates, err := r.onMessagesSendMessage(WithUserID(ctx, owner.ID), &tg.MessagesSendMessageRequest{
		Peer:     &tg.InputPeerUser{UserID: friend.ID, AccessHash: friend.AccessHash},
		Message:  body,
		Entities: entities,
		RandomID: 6003,
	})
	if err != nil {
		t.Fatalf("sendMessage: %v", err)
	}
	msg := newMessageFromUpdates(t, updates)

	pastPending := &domain.MessageMedia{
		Kind: domain.MessageMediaKindWebPage,
		WebPage: &domain.MessageWebPage{
			State: domain.MessageWebPageStatePending,
			ID:    urlHash,
			URL:   url,
			Date:  int(r.clock.Now().Add(-time.Minute).Unix()),
		},
	}
	if _, err := r.deps.Messages.EditMessage(ctx, owner.ID, domain.EditMessageRequest{
		OwnerUserID: owner.ID,
		Peer:        domain.Peer{Type: domain.PeerTypeUser, ID: friend.ID},
		ID:          msg.ID,
		Message:     body,
		Entities:    domainMessageEntities(entities),
		Media:       pastPending,
	}); err != nil {
		t.Fatalf("force expired pending: %v", err)
	}

	if _, err := r.onMessagesGetMessages(WithUserID(ctx, owner.ID), []tg.InputMessageClass{&tg.InputMessageID{ID: msg.ID}}); err != nil {
		t.Fatalf("getMessages: %v", err)
	}
	waitForOwnerMessageWebPageState(t, r, owner.ID, msg.ID, domain.MessageWebPageStateEmpty)
}

func waitForOwnerMessageWebPageState(t *testing.T, r *Router, ownerID int64, msgID int, want domain.MessageWebPageState) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		got, found, err := r.lookupOwnerMessage(context.Background(), ownerID, msgID)
		if err == nil && found && got.Media != nil && got.Media.WebPage != nil && got.Media.WebPage.State == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	got, found, err := r.lookupOwnerMessage(context.Background(), ownerID, msgID)
	if err != nil || !found || got.Media == nil || got.Media.WebPage == nil {
		t.Fatalf("message webpage state not found: found=%v err=%v media=%+v", found, err, got.Media)
	}
	t.Fatalf("message webpage state = %q, want %q", got.Media.WebPage.State, want)
}
