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
		BotUserID: 5001, HTML: "<b>x</b>", Payload: []byte("<b>x</b>"),
		Width: 320, Height: 200, AllowQueries: true,
		PublicData: "public", PublicDataHash: domain.WidgetDataHash("public"),
	})
	if err != nil {
		t.Fatalf("CreateWidget(first): %v", err)
	}
	second, err := widgets.CreateWidget(ctx, domain.BotWidget{
		BotUserID: 5001, HTML: "<i>y</i>", Payload: []byte("<i>y</i>"),
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
	if got.BotUserID != 5001 || got.HTML != "<b>x</b>" || string(got.Payload) != "<b>x</b>" ||
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
