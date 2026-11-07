package rpc

import (
	"context"
	"testing"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
)

// Inline bot results accept widget messages and project back to
// botInlineMessageWidget.
func TestLayer233InlineWidgetResultRoundTrip(t *testing.T) {
	r := newWidgetTestRouter(t, 7)
	ctx := context.Background()

	raw := &tg.InputBotInlineResult{
		ID: "widget", Type: "article",
		SendMessage: &tg.InputBotInlineMessageWidget{Width: 300, Height: 150, HTML: "<b>w</b>", AllowQueries: true, SharedWidget: true},
	}
	result, err := r.domainInlineResultFromTG(ctx, 7, raw)
	if err != nil {
		t.Fatalf("domainInlineResultFromTG(widget): %v", err)
	}
	if result.Media == nil || result.Media.Kind != domain.MessageMediaKindWidget || result.Media.Widget == nil {
		t.Fatalf("inline widget media = %#v", result.Media)
	}
	if result.Media.Widget.WidgetID == 0 {
		t.Fatal("inline shared widget did not get a widget id")
	}

	projected, ok := tgBotInlineResult(result).(*tg.BotInlineResult)
	if !ok {
		t.Fatalf("inline projection = %T", tgBotInlineResult(result))
	}
	widgetMsg, ok := projected.SendMessage.(*tg.BotInlineMessageWidget)
	if !ok {
		t.Fatalf("send_message = %T, want *tg.BotInlineMessageWidget", projected.SendMessage)
	}
	if widgetMsg.Width != 300 || widgetMsg.Height != 150 || string(widgetMsg.Payload) != "<b>w</b>" {
		t.Fatalf("botInlineMessageWidget = %#v", widgetMsg)
	}
	if id, ok := widgetMsg.GetWidgetID(); !ok || id != result.Media.Widget.WidgetID {
		t.Fatalf("botInlineMessageWidget widget_id = %d, %v", id, ok)
	}

	idRaw := &tg.InputBotInlineResult{
		ID: "widget-copy", Type: "article",
		SendMessage: &tg.InputBotInlineMessageWidgetID{WidgetID: result.Media.Widget.WidgetID},
	}
	copied, err := r.domainInlineResultFromTG(ctx, 7, idRaw)
	if err != nil {
		t.Fatalf("domainInlineResultFromTG(widgetID): %v", err)
	}
	if copied.Media == nil || copied.Media.Widget == nil || !copied.Media.Widget.IsCopy {
		t.Fatalf("inline widget copy = %#v", copied.Media)
	}
}
