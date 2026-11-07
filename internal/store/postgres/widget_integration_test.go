package postgres

import (
	"context"
	"errors"
	"testing"

	"telesrv/internal/domain"
)

// TestWidgetStoreIntegration verifies the shared bot_widgets store against live
// Postgres: create assigns unpredictable ids, reads round-trip the definition and
// public-data replacement works through SetWidgetPublicData. Requires
// TELESRV_TEST_POSTGRES_DSN.
func TestWidgetStoreIntegration(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	widgets := NewWidgetStore(pool)

	first, err := widgets.CreateWidget(ctx, domain.BotWidget{
		BotUserID: 5001, Payload: []byte("<b>x</b>"),
		Width: 320, Height: 200, AllowQueries: true,
		PublicData: "public", PublicDataHash: domain.WidgetDataHash("public"),
	})
	if err != nil {
		t.Fatalf("CreateWidget(first): %v", err)
	}
	second, err := widgets.CreateWidget(ctx, domain.BotWidget{
		BotUserID: 5001, Payload: []byte("<i>y</i>"),
	})
	if err != nil {
		t.Fatalf("CreateWidget(second): %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DELETE FROM bot_widgets WHERE widget_id = ANY($1)", []int64{first, second})
	})
	if first <= 0 || second <= 0 || first == second {
		t.Fatalf("widget ids = %d, %d; want distinct positive ids", first, second)
	}

	got, found, err := widgets.GetWidget(ctx, first)
	if err != nil || !found {
		t.Fatalf("GetWidget = %v, %v", found, err)
	}
	if got.BotUserID != 5001 || string(got.Payload) != "<b>x</b>" ||
		got.Width != 320 || got.Height != 200 || !got.AllowQueries ||
		got.PublicData != "public" || got.PublicDataHash != domain.WidgetDataHash("public") {
		t.Fatalf("GetWidget = %+v", got)
	}

	if err := widgets.SetWidgetPublicData(ctx, first, "next", domain.WidgetDataHash("next"), 60); err != nil {
		t.Fatalf("SetWidgetPublicData: %v", err)
	}
	got, _, err = widgets.GetWidget(ctx, first)
	if err != nil {
		t.Fatalf("GetWidget after set: %v", err)
	}
	if got.PublicData != "next" || got.PublicDataHash != domain.WidgetDataHash("next") || got.CacheTime != 60 {
		t.Fatalf("public data after set = %+v", got)
	}

	if _, found, err := widgets.GetWidget(ctx, 1<<62); err != nil || found {
		t.Fatalf("GetWidget(missing) = %v, %v; want not found", found, err)
	}
	if err := widgets.SetWidgetPublicData(ctx, 1<<62, "x", 1, 1); !errors.Is(err, domain.ErrWidgetNotFound) {
		t.Fatalf("SetWidgetPublicData(missing) = %v, want ErrWidgetNotFound", err)
	}
}

// TestSharedWidgetDefinitionCreatedOnSendIntegration verifies the shared widget
// row is created inside the committed private send transaction (not while input
// is parsed), with the owner bot and markup carried on the media snapshot.
// Requires TELESRV_TEST_POSTGRES_DSN.
func TestSharedWidgetDefinitionCreatedOnSendIntegration(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)

	users := NewUserStore(pool)
	sender, err := users.Create(ctx, domain.User{AccessHash: 53, Phone: "+1670" + suffix + "11", FirstName: "WSender"})
	if err != nil {
		t.Fatalf("create sender: %v", err)
	}
	recipient, err := users.Create(ctx, domain.User{AccessHash: 54, Phone: "+1670" + suffix + "12", FirstName: "WRecipient"})
	if err != nil {
		t.Fatalf("create recipient: %v", err)
	}
	widgetID := domain.RandomWidgetID()
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DELETE FROM bot_widgets WHERE widget_id = $1", widgetID)
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = ANY($1::bigint[])", []int64{sender.ID, recipient.ID})
	})

	messages := NewMessageStore(pool)
	sent, err := messages.SendPrivateText(ctx, domain.SendPrivateTextRequest{
		SenderUserID: sender.ID, RecipientUserID: recipient.ID, RandomID: 778899,
		Message: "widget", Date: 1700000500,
		Media: &domain.MessageMedia{Kind: domain.MessageMediaKindWidget, Widget: &domain.MessageWidget{
			WidgetID: widgetID, OwnerBotID: sender.ID, SharedWidget: true,
			Payload: []byte("<b>w</b>"), Width: 10, Height: 10, AllowQueries: true,
			PublicData: "p", PublicDataHash: domain.WidgetDataHash("p"),
		}},
	})
	if err != nil {
		t.Fatalf("SendPrivateText(widget): %v", err)
	}
	if sent.SenderMessage.Media == nil || sent.SenderMessage.Media.Widget == nil || sent.SenderMessage.Media.Widget.WidgetID != widgetID {
		t.Fatalf("sent media widget = %+v", sent.SenderMessage.Media)
	}

	def, found, err := NewWidgetStore(pool).GetWidget(ctx, widgetID)
	if err != nil || !found {
		t.Fatalf("GetWidget after send = %v, %v; want the persisted definition", found, err)
	}
	if def.BotUserID != sender.ID || string(def.Payload) != "<b>w</b>" || def.Width != 10 || def.Height != 10 ||
		!def.AllowQueries || def.PublicData != "p" || def.PublicDataHash != domain.WidgetDataHash("p") {
		t.Fatalf("persisted widget = %+v", def)
	}
}
