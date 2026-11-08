package rpc

import (
	"context"
	"testing"

	"github.com/iamxvbaba/td/tg"
)

// Resolving inputMediaWidget must not persist the shared definition: that happens
// only in the committed send transaction, so uploadMedia, setInlineBotResults and
// sends that fail later cannot leak bot_widgets rows.
func TestLayer233SharedWidgetNotCreatedOnResolve(t *testing.T) {
	r := newWidgetTestRouter(t, 42)
	ctx := context.Background()
	in := &tg.InputMediaWidget{Width: 10, Height: 10, HTML: "<b>x</b>", AllowQueries: true, SharedWidget: true}
	media, err := r.resolveInputMedia(ctx, 42, in)
	if err != nil {
		t.Fatalf("resolveInputMedia: %v", err)
	}
	if media.Widget == nil || media.Widget.WidgetID == 0 {
		t.Fatalf("shared widget did not get an id: %+v", media.Widget)
	}
	if media.Widget.OwnerBotID != 42 {
		t.Fatalf("owner bot id = %d, want 42", media.Widget.OwnerBotID)
	}
	if _, found, err := r.deps.Widgets.GetWidget(ctx, media.Widget.WidgetID); err != nil || found {
		t.Fatalf("resolve created a bot_widgets row: found=%v err=%v", found, err)
	}
}
