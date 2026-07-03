package rpc

import (
	"context"
	"testing"

	"github.com/gotd/td/clock"
	"github.com/gotd/td/tg"
	"go.uber.org/zap/zaptest"

	"telesrv/internal/domain"
)

func TestPushBotTextDraftUsesUserTypingDraftAction(t *testing.T) {
	sessions := &captureSessions{}
	r := New(Config{}, Deps{Sessions: sessions}, zaptest.NewLogger(t), clock.System)
	ctx := WithSessionID(WithAuthKeyID(context.Background(), [8]byte{1}), 77)

	r.PushBotTextDraft(ctx, domain.ChatBotUserID, 1001, 4242, "Hello from AI")

	short, ok := sessions.lastUserPush().(*tg.UpdateShort)
	if !ok {
		t.Fatalf("pushed update = %T, want *tg.UpdateShort", sessions.lastUserPush())
	}
	update, ok := short.Update.(*tg.UpdateUserTyping)
	if !ok {
		t.Fatalf("short update = %T, want *tg.UpdateUserTyping", short.Update)
	}
	if update.UserID != domain.ChatBotUserID {
		t.Fatalf("typing user_id = %d, want ChatBot", update.UserID)
	}
	action, ok := update.Action.(*tg.SendMessageTextDraftAction)
	if !ok {
		t.Fatalf("typing action = %T, want *tg.SendMessageTextDraftAction", update.Action)
	}
	if action.RandomID != 4242 || action.Text.Text != "Hello from AI" {
		t.Fatalf("draft action = random_id %d text %q", action.RandomID, action.Text.Text)
	}
	if snap := sessions.snapshot(); snap.userID != 1001 || snap.sessionID != 77 {
		t.Fatalf("push target/excluded session = user %d session %d, want user 1001 session 77", snap.userID, snap.sessionID)
	}
}
