package rpc

import (
	"context"
	"errors"
	"testing"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"go.uber.org/zap/zaptest"

	"telesrv/internal/domain"
)

type reactionSettingsAccountStub struct {
	AccountService
	settings domain.AccountReactionSettings
	getErr   error
	setCalls int
}

func (s *reactionSettingsAccountStub) GetReactionSettings(context.Context, int64) (domain.AccountReactionSettings, error) {
	return s.settings, s.getErr
}

func (s *reactionSettingsAccountStub) SetDefaultReaction(_ context.Context, _ int64, reaction domain.MessageReaction) (domain.AccountReactionSettings, error) {
	s.setCalls++
	s.settings.DefaultReaction = reaction
	return s.settings, nil
}

type reactionCatalogErrorFiles struct {
	FilesService
}

func (reactionCatalogErrorFiles) ListAvailableReactions(context.Context) ([]domain.AvailableReaction, error) {
	return nil, errors.New("catalog unavailable")
}

func TestHelpGetConfigReturnsDefaultAndAccountReaction(t *testing.T) {
	account := &reactionSettingsAccountStub{settings: domain.DefaultAccountReactionSettings()}
	r := New(Config{DC: 2, PublicBaseURL: "https://telesrv.net"}, Deps{Account: account}, zaptest.NewLogger(t), clock.System)

	preAuth, err := r.onHelpGetConfig(context.Background())
	if err != nil {
		t.Fatalf("pre-auth help.getConfig: %v", err)
	}
	if emoji, ok := preAuth.ReactionsDefault.(*tg.ReactionEmoji); !ok || emoji.Emoticon != "\U0001f44d" {
		t.Fatalf("pre-auth reactions_default = %#v, want thumbs up", preAuth.ReactionsDefault)
	}

	account.settings.DefaultReaction = domain.MessageReaction{Type: domain.MessageReactionCustomEmoji, DocumentID: 7001}
	authorized, err := r.onHelpGetConfig(WithUserID(context.Background(), 42))
	if err != nil {
		t.Fatalf("authorized help.getConfig: %v", err)
	}
	if custom, ok := authorized.ReactionsDefault.(*tg.ReactionCustomEmoji); !ok || custom.DocumentID != 7001 {
		t.Fatalf("authorized reactions_default = %#v, want custom emoji 7001", authorized.ReactionsDefault)
	}
}

func TestSetDefaultReactionRequiresActiveCatalogEmoji(t *testing.T) {
	account := &reactionSettingsAccountStub{settings: domain.DefaultAccountReactionSettings()}
	files := &fakeFiles{reactions: []domain.AvailableReaction{
		{Reaction: "\U0001f525"},
		{Reaction: "\U0001f602", Inactive: true},
	}}
	r := New(Config{}, Deps{Account: account, Files: files}, zaptest.NewLogger(t), clock.System)
	ctx := WithUserID(context.Background(), 42)

	if ok, err := r.onMessagesSetDefaultReaction(ctx, &tg.ReactionEmoji{Emoticon: "\U0001f525"}); err != nil || !ok {
		t.Fatalf("set active reaction = %v, %v", ok, err)
	}
	if account.setCalls != 1 {
		t.Fatalf("set calls = %d, want 1", account.setCalls)
	}
	for _, emoticon := range []string{"\U0001f602", "\U0001f680"} {
		if _, err := r.onMessagesSetDefaultReaction(ctx, &tg.ReactionEmoji{Emoticon: emoticon}); !tgerr.Is(err, "REACTION_INVALID") {
			t.Fatalf("set reaction %q err = %v, want REACTION_INVALID", emoticon, err)
		}
	}
	if account.setCalls != 1 {
		t.Fatalf("invalid reactions reached persistence: set calls = %d", account.setCalls)
	}
}

func TestSetDefaultReactionFailsClosedWhenCatalogReadFails(t *testing.T) {
	account := &reactionSettingsAccountStub{settings: domain.DefaultAccountReactionSettings()}
	r := New(Config{}, Deps{Account: account, Files: reactionCatalogErrorFiles{}}, zaptest.NewLogger(t), clock.System)
	if _, err := r.onMessagesSetDefaultReaction(WithUserID(context.Background(), 42), &tg.ReactionEmoji{Emoticon: "\U0001f44d"}); !tgerr.Is(err, "INTERNAL_SERVER_ERROR") {
		t.Fatalf("set reaction err = %v, want INTERNAL_SERVER_ERROR", err)
	}
	if account.setCalls != 0 {
		t.Fatalf("failed catalog read reached persistence: set calls = %d", account.setCalls)
	}
}

func TestMessagesReceivedMessagesIsAuthorizedNoop(t *testing.T) {
	r := New(Config{}, Deps{}, zaptest.NewLogger(t), clock.System)
	for _, maxID := range []int{-1, 0, 1, int(^uint32(0) >> 1)} {
		got, err := r.onMessagesReceivedMessages(WithUserID(context.Background(), 42), maxID)
		if err != nil {
			t.Fatalf("max_id %d: %v", maxID, err)
		}
		if got == nil || len(got) != 0 {
			t.Fatalf("max_id %d result = %#v, want non-nil empty cancellation set", maxID, got)
		}
	}
	if _, err := r.onMessagesReceivedMessages(context.Background(), 1); !tgerr.Is(err, "AUTH_KEY_UNREGISTERED") {
		t.Fatalf("unauthorized err = %v, want AUTH_KEY_UNREGISTERED", err)
	}
}

func TestMessagesReceivedMessagesIsRegistered(t *testing.T) {
	r := New(Config{}, Deps{}, zaptest.NewLogger(t), clock.System)
	var request bin.Buffer
	if err := (&tg.MessagesReceivedMessagesRequest{MaxID: 99}).Encode(&request); err != nil {
		t.Fatalf("encode messages.receivedMessages: %v", err)
	}
	result, method, err := r.DispatchWithMethod(WithUserID(context.Background(), 42), [8]byte{1}, 7, &request)
	if err != nil {
		t.Fatalf("dispatch messages.receivedMessages: %v", err)
	}
	if method != "messages.receivedMessages" {
		t.Fatalf("method = %q, want messages.receivedMessages", method)
	}
	vector, ok := result.(*tg.ReceivedNotifyMessageVector)
	if !ok || vector == nil || vector.Elems == nil || len(vector.Elems) != 0 {
		t.Fatalf("result = %#v (%T), want non-nil empty ReceivedNotifyMessageVector", result, result)
	}
}
