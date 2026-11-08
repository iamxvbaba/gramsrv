package memory

import (
	"context"
	"testing"

	"telesrv/internal/domain"
)

func TestWidgetStoreRoundTrip(t *testing.T) {
	ctx := context.Background()
	store := NewWidgetStore()
	id, err := store.CreateWidget(ctx, domain.BotWidget{
		BotUserID: 7, Payload: []byte("<b>x</b>"),
		Width: 100, Height: 50, AllowQueries: true,
	})
	if err != nil {
		t.Fatalf("CreateWidget: %v", err)
	}
	if id == 0 {
		t.Fatal("CreateWidget returned a zero id")
	}
	got, found, err := store.GetWidget(ctx, id)
	if err != nil || !found {
		t.Fatalf("GetWidget = %v, %v", found, err)
	}
	if got.BotUserID != 7 || got.Width != 100 || !got.AllowQueries {
		t.Fatalf("stored widget = %#v", got)
	}
	if err := store.SetWidgetPublicData(ctx, id, "public", 12345, 60); err != nil {
		t.Fatalf("SetWidgetPublicData: %v", err)
	}
	got, found, err = store.GetWidget(ctx, id)
	if err != nil || !found {
		t.Fatalf("GetWidget after update = %v, %v", found, err)
	}
	if got.PublicData != "public" || got.PublicDataHash != 12345 || got.CacheTime != 60 {
		t.Fatalf("updated widget = %#v", got)
	}
	if _, found, err := store.GetWidget(ctx, id+999); err != nil || found {
		t.Fatalf("missing widget = %v, %v", found, err)
	}
	if err := store.SetWidgetPublicData(ctx, id+999, "x", 1, 0); err != domain.ErrWidgetNotFound {
		t.Fatalf("SetWidgetPublicData missing = %v, want ErrWidgetNotFound", err)
	}
}
