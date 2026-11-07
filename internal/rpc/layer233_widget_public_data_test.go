package rpc

import (
	"context"
	"testing"

	"github.com/iamxvbaba/td/clock"
	"go.uber.org/zap/zaptest"

	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

// getWidgetPublicData precedence: a message with no per-message override reflects
// the shared definition's public data (so editWidgetData is visible), while an
// explicit per-message override wins, including an explicit "" clear.
func TestLayer233EffectiveWidgetPublicData(t *testing.T) {
	r := New(Config{}, Deps{}, zaptest.NewLogger(t), clock.System)
	r.deps.Widgets = memory.NewWidgetStore()
	ctx := context.Background()

	id, err := r.deps.Widgets.CreateWidget(ctx, domain.BotWidget{
		BotUserID: 7, HTML: "<b>x</b>", Payload: []byte("<b>x</b>"),
		PublicData: "shared", PublicDataHash: widgetDataHash("shared"), CacheTime: 60,
	})
	if err != nil {
		t.Fatalf("CreateWidget: %v", err)
	}

	// No per-message override -> the shared definition's data.
	if hash, data, period := r.effectiveWidgetPublicData(ctx, &domain.MessageWidget{WidgetID: id}); data != "shared" || hash != widgetDataHash("shared") || period != 60 {
		t.Fatalf("shared fallback = %d, %q, %d", hash, data, period)
	}
	// Per-message override wins.
	if hash, data, _ := r.effectiveWidgetPublicData(ctx, &domain.MessageWidget{WidgetID: id, PublicData: "local", PublicDataHash: widgetDataHash("local"), PublicDataSet: true}); data != "local" || hash != widgetDataHash("local") {
		t.Fatalf("per-message override = %d, %q", hash, data)
	}
	// Explicit clear ("") is not replaced by the shared value.
	if hash, data, _ := r.effectiveWidgetPublicData(ctx, &domain.MessageWidget{WidgetID: id, PublicDataHash: widgetDataHash(""), PublicDataSet: true}); data != "" || hash != widgetDataHash("") {
		t.Fatalf("explicit clear = %d, %q", hash, data)
	}
}
