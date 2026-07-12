package rpc

import (
	"context"
	"testing"

	"github.com/gotd/td/clock"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
	"go.uber.org/zap/zaptest"

	appusers "telesrv/internal/app/users"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

type captureTranslationService struct {
	request  domain.TranslationRequest
	disabled map[[3]int64]bool
}

func (s *captureTranslationService) Translate(_ context.Context, req domain.TranslationRequest) (domain.TranslationResult, error) {
	s.request = req
	out := make([]domain.TranslationText, len(req.Texts))
	for i := range req.Texts {
		out[i].Text = "translated:" + req.Texts[i].Text
	}
	return domain.TranslationResult{Texts: out}, nil
}

func translationSettingKey(userID int64, peer domain.Peer) [3]int64 {
	kind := int64(1)
	if peer.Type == domain.PeerTypeChannel {
		kind = 2
	}
	return [3]int64{userID, kind, peer.ID}
}

func (s *captureTranslationService) SetPeerDisabled(_ context.Context, userID int64, peer domain.Peer, disabled bool) (bool, error) {
	if s.disabled == nil {
		s.disabled = map[[3]int64]bool{}
	}
	key := translationSettingKey(userID, peer)
	previous := s.disabled[key]
	s.disabled[key] = disabled
	return previous != disabled, nil
}

func (s *captureTranslationService) PeerDisabled(_ context.Context, userID int64, peer domain.Peer) (bool, error) {
	return s.disabled[translationSettingKey(userID, peer)], nil
}

func TestMessagesTranslateTextDirectMode(t *testing.T) {
	svc := &captureTranslationService{}
	r := New(Config{}, Deps{Translation: svc}, zaptest.NewLogger(t), clock.System)
	ctx := WithUserID(context.Background(), 1001)
	req := &tg.MessagesTranslateTextRequest{ToLang: "zh"}
	req.SetText([]tg.TextWithEntities{{Text: "hello"}, {Text: "world"}})
	got, err := r.onMessagesTranslateText(ctx, req)
	if err != nil {
		t.Fatalf("translateText: %v", err)
	}
	if len(got.Result) != 2 || got.Result[0].Text != "translated:hello" || got.Result[1].Text != "translated:world" {
		t.Fatalf("result = %#v", got.Result)
	}
	if svc.request.UserID != 1001 || svc.request.ToLang != "zh" || len(svc.request.Texts) != 2 {
		t.Fatalf("domain request = %#v", svc.request)
	}
}

func TestMessagesTranslateTextRejectsInvalidFlags(t *testing.T) {
	r := New(Config{}, Deps{Translation: &captureTranslationService{}}, zaptest.NewLogger(t), clock.System)
	ctx := WithUserID(context.Background(), 1001)
	if _, err := r.onMessagesTranslateText(ctx, &tg.MessagesTranslateTextRequest{ToLang: "en"}); !tgerr.Is(err, "INPUT_TEXT_EMPTY") {
		t.Fatalf("empty flags err = %v", err)
	}
	req := &tg.MessagesTranslateTextRequest{ToLang: "en"}
	req.SetPeer(&tg.InputPeerUser{UserID: 2})
	req.SetID([]int{1})
	req.SetText([]tg.TextWithEntities{{Text: "both"}})
	if _, err := r.onMessagesTranslateText(ctx, req); !tgerr.Is(err, "INPUT_TEXT_EMPTY") {
		t.Fatalf("both modes err = %v", err)
	}
}

func TestMessagesTogglePeerTranslationsAndProjection(t *testing.T) {
	svc := &captureTranslationService{}
	r := New(Config{}, Deps{Translation: svc}, zaptest.NewLogger(t), clock.System)
	ctx := WithUserID(context.Background(), 1001)
	peer := &tg.InputPeerUser{UserID: 2002}
	if ok, err := r.onMessagesTogglePeerTranslations(ctx, &tg.MessagesTogglePeerTranslationsRequest{Disabled: true, Peer: peer}); err != nil || !ok {
		t.Fatalf("toggle = %v/%v", ok, err)
	}
	full := tg.UserFull{ID: 2002}
	if err := r.applyTranslationDisabledToUserFull(ctx, 1001, 2002, &full); err != nil {
		t.Fatalf("projection: %v", err)
	}
	if !full.TranslationsDisabled {
		t.Fatal("userFull.translations_disabled = false")
	}
}

func TestMessagesTogglePeerTranslationsValidatesUserAccessHash(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, err := userStore.Create(ctx, domain.User{Phone: "+10000000001", FirstName: "Owner", AccessHash: 11})
	if err != nil {
		t.Fatal(err)
	}
	peer, err := userStore.Create(ctx, domain.User{Phone: "+10000000002", FirstName: "Peer", AccessHash: 22})
	if err != nil {
		t.Fatal(err)
	}
	svc := &captureTranslationService{}
	r := New(Config{}, Deps{Users: appusers.NewService(userStore), Translation: svc}, zaptest.NewLogger(t), clock.System)
	ownerCtx := WithUserID(ctx, owner.ID)
	_, err = r.onMessagesTogglePeerTranslations(ownerCtx, &tg.MessagesTogglePeerTranslationsRequest{
		Disabled: true,
		Peer:     &tg.InputPeerUser{UserID: peer.ID, AccessHash: peer.AccessHash + 1},
	})
	if !tgerr.Is(err, "PEER_ID_INVALID") {
		t.Fatalf("bad access hash err = %v, want PEER_ID_INVALID", err)
	}
	if len(svc.disabled) != 0 {
		t.Fatalf("bad access hash wrote settings: %#v", svc.disabled)
	}
}

func TestMessagesTranslateTextRejectsBotCaller(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	bot, err := userStore.Create(ctx, domain.User{Phone: "+10000000003", FirstName: "Bot", AccessHash: 33, Bot: true})
	if err != nil {
		t.Fatal(err)
	}
	r := New(Config{}, Deps{Users: appusers.NewService(userStore), Translation: &captureTranslationService{}}, zaptest.NewLogger(t), clock.System)
	req := &tg.MessagesTranslateTextRequest{ToLang: "en"}
	req.SetText([]tg.TextWithEntities{{Text: "hello"}})
	if _, err := r.onMessagesTranslateText(WithUserID(ctx, bot.ID), req); !tgerr.Is(err, "BOT_METHOD_INVALID") {
		t.Fatalf("bot translate err = %v, want BOT_METHOD_INVALID", err)
	}
}
