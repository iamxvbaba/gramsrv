package rpc

import (
	"context"
	"testing"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
)

func inlineWidgetResult(id string) tg.InputBotInlineResultClass {
	return &tg.InputBotInlineResult{
		ID: id, Type: "article",
		SendMessage: &tg.InputBotInlineMessageWidget{
			Width: 200, Height: 100, HTML: "<b>w</b>", AllowQueries: true,
		},
	}
}

// A widget inline result must expose msg_id even without a reply keyboard, and
// messages.editInlineBotMessageWidgetData must patch the sent widget's data for
// the owning bot.
func TestMessagesEditInlineBotMessageWidgetDataPrivate(t *testing.T) {
	ctx := context.Background()
	f := newInlineBotRPCTestFixture(t)
	sessions := &captureSessions{}
	f.router.deps.Sessions = sessions
	ownerCtx := WithUserID(ctx, f.owner.ID)
	botCtx := WithUserID(ctx, f.bot.ID)

	queryID, _ := f.router.inlines.registerWithCacheKey(f.router.clock.Now(), f.bot.ID, f.owner.ID,
		domain.Peer{Type: domain.PeerTypeUser, ID: f.peer.ID}, inlineCacheKey{query: "widget-edit"})
	if ok, err := f.router.onMessagesSetInlineBotResults(botCtx, &tg.MessagesSetInlineBotResultsRequest{
		QueryID: queryID,
		Results: []tg.InputBotInlineResultClass{inlineWidgetResult("widget-1")},
	}); err != nil || !ok {
		t.Fatalf("set inline widget results = %v,%v, want true,nil", ok, err)
	}
	if _, err := f.router.onMessagesSendInlineBotResult(ownerCtx, &tg.MessagesSendInlineBotResultRequest{
		Peer: inputPeerUser(f.peer), RandomID: 991101, QueryID: queryID, ID: "widget-1",
	}); err != nil {
		t.Fatalf("send inline widget result: %v", err)
	}
	// A widget result has no keyboard, yet it must still learn the sent msg_id.
	msgID, ok := inlineSendFeedbackFromSessions(t, sessions, f.bot.ID).GetMsgID()
	if !ok {
		t.Fatal("inline widget send feedback missing msg_id")
	}

	editReq := &tg.MessagesEditInlineBotMessageWidgetDataRequest{ID: msgID}
	editReq.SetPublicData("edited-data")
	if ok, err := f.router.onMessagesEditInlineBotMessageWidgetData(botCtx, editReq); err != nil || !ok {
		t.Fatalf("edit inline widget data = %v,%v, want true,nil", ok, err)
	}

	ownerHistory, err := f.router.deps.Messages.GetHistory(ctx, f.owner.ID, domain.MessageFilter{
		HasPeer: true, Peer: domain.Peer{Type: domain.PeerTypeUser, ID: f.peer.ID}, Limit: 1,
	})
	if err != nil || len(ownerHistory.Messages) != 1 {
		t.Fatalf("owner history len = %d err=%v, want 1,nil", len(ownerHistory.Messages), err)
	}
	msg := ownerHistory.Messages[0]
	if msg.Media == nil || msg.Media.Widget == nil || msg.Media.Widget.PublicData != "edited-data" {
		t.Fatalf("inline widget data not edited: %+v", msg.Media)
	}
}
