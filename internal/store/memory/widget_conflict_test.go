package memory

import (
	"context"
	"errors"
	"testing"

	"telesrv/internal/domain"
)

// A preassigned widget id is reusable only by the same bot; a different owner
// must not silently link to (or overwrite) the existing definition.
func TestWidgetStorePreassignedIDOwnerConflict(t *testing.T) {
	ctx := context.Background()
	st := NewWidgetStore()
	if _, err := st.CreateWidget(ctx, domain.BotWidget{WidgetID: 99, BotUserID: 1, HTML: "a"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := st.CreateWidget(ctx, domain.BotWidget{WidgetID: 99, BotUserID: 2, HTML: "b"}); !errors.Is(err, domain.ErrWidgetNotFound) {
		t.Fatalf("different-owner collision err = %v, want ErrWidgetNotFound", err)
	}
	if _, err := st.CreateWidget(ctx, domain.BotWidget{WidgetID: 99, BotUserID: 1, HTML: "c"}); err != nil {
		t.Fatalf("same-owner reuse err = %v, want nil", err)
	}
	def, found, err := st.GetWidget(ctx, 99)
	if err != nil || !found || def.BotUserID != 1 || def.HTML != "a" {
		t.Fatalf("definition after conflict = %+v, %v, %v", def, found, err)
	}
}
