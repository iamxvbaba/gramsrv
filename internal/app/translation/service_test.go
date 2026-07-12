package translation

import (
	"context"
	"errors"
	"testing"
	"time"

	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

type testLimiter struct{ cost int }

func (l *testLimiter) AllowN(_ context.Context, _ string, cost, _ int, _ time.Duration) (bool, int, error) {
	l.cost = cost
	return true, 0, nil
}

type testProvider struct {
	translate func([]domain.TranslationText) ([]domain.TranslationText, error)
}

func (testProvider) Name() string { return "test" }
func (p testProvider) Translate(_ context.Context, texts []domain.TranslationText, _, _ string) ([]domain.TranslationText, error) {
	return p.translate(texts)
}

type testPrivateMessages struct{ messages []domain.Message }

func (s testPrivateMessages) GetMessages(_ context.Context, _ int64, _ []int) (domain.MessageList, error) {
	return domain.MessageList{Messages: append([]domain.Message(nil), s.messages...)}, nil
}

func TestTranslateDirectTextPreservesBatchOrder(t *testing.T) {
	limiter := &testLimiter{}
	svc := NewService(nil, nil, memory.NewDialogStore(), WithRateLimiter(limiter, 60, time.Minute), WithProviders(testProvider{translate: func(in []domain.TranslationText) ([]domain.TranslationText, error) {
		out := make([]domain.TranslationText, len(in))
		for i := range in {
			out[i].Text = "zh:" + in[i].Text
		}
		return out, nil
	}}))
	got, err := svc.Translate(context.Background(), domain.TranslationRequest{
		UserID: 1,
		Texts:  []domain.TranslationText{{Text: "one"}, {Text: "two"}},
		ToLang: "zh",
	})
	if err != nil {
		t.Fatalf("Translate: %v", err)
	}
	if len(got.Texts) != 2 || got.Texts[0].Text != "zh:one" || got.Texts[1].Text != "zh:two" {
		t.Fatalf("Translate result = %#v", got.Texts)
	}
	if limiter.cost != 2 {
		t.Fatalf("rate limit cost = %d, want 2 text items", limiter.cost)
	}
}

func TestTranslateMessageIDsRejectsWrongPeerAndMissingID(t *testing.T) {
	peer := domain.Peer{Type: domain.PeerTypeUser, ID: 2}
	svc := NewService(testPrivateMessages{messages: []domain.Message{
		{ID: 10, Peer: peer, Body: "visible"},
		{ID: 11, Peer: domain.Peer{Type: domain.PeerTypeUser, ID: 3}, Body: "wrong peer"},
	}}, nil, memory.NewDialogStore(), WithProviders(testProvider{translate: func(in []domain.TranslationText) ([]domain.TranslationText, error) {
		return in, nil
	}}))
	for _, ids := range [][]int{{10, 11}, {10, 12}} {
		_, err := svc.Translate(context.Background(), domain.TranslationRequest{UserID: 1, Peer: peer, IDs: ids, ToLang: "en"})
		if !errors.Is(err, domain.ErrTranslationMessageInvalid) {
			t.Fatalf("Translate ids %v err = %v, want message invalid", ids, err)
		}
	}
}

func TestTranslateRejectsOversizeAndProviderShapeMismatch(t *testing.T) {
	svc := NewService(nil, nil, memory.NewDialogStore(), WithProviders(testProvider{translate: func(in []domain.TranslationText) ([]domain.TranslationText, error) {
		return in[:len(in)-1], nil
	}}))
	texts := make([]domain.TranslationText, domain.MaxTranslationTexts+1)
	for i := range texts {
		texts[i].Text = "x"
	}
	if _, err := svc.Translate(context.Background(), domain.TranslationRequest{UserID: 1, Texts: texts, ToLang: "en"}); !errors.Is(err, domain.ErrTranslationInputTooLong) {
		t.Fatalf("oversize err = %v", err)
	}
	if _, err := svc.Translate(context.Background(), domain.TranslationRequest{UserID: 1, Texts: texts[:2], ToLang: "en"}); !errors.Is(err, domain.ErrTranslationProviderUnavailable) {
		t.Fatalf("shape mismatch err = %v", err)
	}
}

func TestPeerDisabledIsAccountAndPeerScoped(t *testing.T) {
	settings := memory.NewDialogStore()
	svc := NewService(nil, nil, settings)
	peer := domain.Peer{Type: domain.PeerTypeUser, ID: 2}
	if changed, err := svc.SetPeerDisabled(context.Background(), 1, peer, true); err != nil || !changed {
		t.Fatalf("disable = %v/%v", changed, err)
	}
	if disabled, _ := svc.PeerDisabled(context.Background(), 1, peer); !disabled {
		t.Fatal("owner preference not persisted")
	}
	if disabled, _ := svc.PeerDisabled(context.Background(), 2, peer); disabled {
		t.Fatal("preference leaked to another account")
	}
	if changed, err := svc.SetPeerDisabled(context.Background(), 1, peer, false); err != nil || !changed {
		t.Fatalf("enable = %v/%v", changed, err)
	}
}
