package domain

import "testing"

// ApplyWidgetDataPatch records explicit public-data presence so an explicit ""
// clear is distinguishable from "no per-message override" and is idempotent.
func TestApplyWidgetDataPatchPresenceAndClear(t *testing.T) {
	widget := &MessageWidget{PublicData: "a", PublicDataHash: WidgetDataHash("a")}
	media := &MessageMedia{Kind: MessageMediaKindWidget, Widget: widget}
	empty := ""
	if !ApplyWidgetDataPatch(media, &MessageWidgetDataPatch{PublicData: &empty}) {
		t.Fatal("clearing public data reported no change")
	}
	if !widget.PublicDataSet || widget.PublicData != "" || widget.PublicDataHash != WidgetDataHash("") {
		t.Fatalf("cleared widget = %+v", widget)
	}
	if ApplyWidgetDataPatch(media, &MessageWidgetDataPatch{PublicData: &empty}) {
		t.Fatal("re-applying the same clear reported a change")
	}
	// A non-widget media is never patched.
	if ApplyWidgetDataPatch(&MessageMedia{Kind: MessageMediaKindPhoto}, &MessageWidgetDataPatch{PublicData: &empty}) {
		t.Fatal("non-widget media was patched")
	}
}
