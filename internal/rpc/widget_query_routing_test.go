package rpc

import (
	"context"
	"testing"

	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap/zaptest"

	"telesrv/internal/domain"
)

// A forwarded/shared widget the owner bot did not itself send must be answered as
// updateBotWidgetCopyQuery, never with the querier's peer/msg_id.
func TestBotWidgetQueryForwardedRoutesToCopyQuery(t *testing.T) {
	r := New(Config{}, Deps{}, zaptest.NewLogger(t), clock.System)
	widget := &domain.MessageWidget{WidgetID: 5, CanQuery: true, SharedWidget: true}
	target := widgetMessageTarget{
		senderBotID: 111, // a human forwarded it under the shared owner 999
		media:       &domain.MessageMedia{Kind: domain.MessageMediaKindWidget, Widget: widget},
	}
	req := &tg.MessagesGetBotWidgetQueryAnswerRequest{QueryType: &tg.WidgetQueryTypeData{}}

	update, err := r.botWidgetQueryUpdate(context.Background(), 42,
		domain.Peer{Type: domain.PeerTypeUser, ID: 42}, req, target, widget, 999, 7)
	if err != nil {
		t.Fatalf("botWidgetQueryUpdate: %v", err)
	}
	copyQuery, ok := update.(*tg.UpdateBotWidgetCopyQuery)
	if !ok {
		t.Fatalf("forwarded widget update = %T, want *tg.UpdateBotWidgetCopyQuery", update)
	}
	if copyQuery.WidgetID != 5 || copyQuery.UserID != 42 {
		t.Fatalf("copy query = %+v", copyQuery)
	}
}

// A private query whose UID translation fails must fail closed rather than send
// updateBotWidgetQuery with the querier's peer/box id.
func TestBotWidgetQueryPrivateTranslationFailsClosed(t *testing.T) {
	r := New(Config{}, Deps{}, zaptest.NewLogger(t), clock.System)
	widget := &domain.MessageWidget{CanQuery: true}
	target := widgetMessageTarget{
		senderBotID: 999, // the bot sent it, but the bot's copy cannot be resolved
		media:       &domain.MessageMedia{Kind: domain.MessageMediaKindWidget, Widget: widget},
	}
	req := &tg.MessagesGetBotWidgetQueryAnswerRequest{QueryType: &tg.WidgetQueryTypeData{}}

	if _, err := r.botWidgetQueryUpdate(context.Background(), 42,
		domain.Peer{Type: domain.PeerTypeUser, ID: 42}, req, target, widget, 999, 7); err == nil {
		t.Fatal("private translation failure returned an update instead of failing closed")
	}
}
