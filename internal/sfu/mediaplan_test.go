package sfu

import "testing"

// Layer-selection contract: the subscriber creates a decode sink only for SIM[0]
// (the lowest layer) and its FID RTX partner; the SFU forwards only these two video
// ssrcs and terminates the remaining layers at the SFU.
func TestBuildMediaPlanSimulcast(t *testing.T) {
	offer := ClientOffer{
		AudioSSRC: 1000,
		SsrcGroups: []SsrcGroup{
			{Semantics: "SIM", Sources: []uint32{1001, 1003, 1005}},
			{Semantics: "FID", Sources: []uint32{1001, 1002}},
			{Semantics: "FID", Sources: []uint32{1003, 1004}},
			{Semantics: "FID", Sources: []uint32{1005, 1006}},
		},
	}
	plan := buildMediaPlan(offer)
	for _, ssrc := range []uint32{1000, 1001, 1002} {
		if !plan.shouldForward(ssrc) {
			t.Fatalf("ssrc %d must be forwarded (audio / SIM[0] / its RTX)", ssrc)
		}
	}
	for _, ssrc := range []uint32{1003, 1004, 1005, 1006} {
		if plan.shouldForward(ssrc) {
			t.Fatalf("ssrc %d must be dropped (higher simulcast layer)", ssrc)
		}
	}
	// Undeclared ssrcs (probing/future extensions) pass through; the subscriber drops them safely.
	if !plan.shouldForward(9999) {
		t.Fatalf("undeclared ssrc must pass through")
	}
}

// Single-layer publish (conference mode): no SIM group, and the first ssrc of the sole FID group is the primary stream.
func TestBuildMediaPlanSingleLayer(t *testing.T) {
	plan := buildMediaPlan(ClientOffer{
		AudioSSRC:  2000,
		SsrcGroups: []SsrcGroup{{Semantics: "FID", Sources: []uint32{2001, 2002}}},
	})
	if !plan.shouldForward(2001) || !plan.shouldForward(2002) {
		t.Fatalf("single-layer media+rtx must be forwarded")
	}
}

// Audio-only (no video group): everything passes through.
func TestBuildMediaPlanAudioOnly(t *testing.T) {
	plan := buildMediaPlan(ClientOffer{AudioSSRC: 3000})
	if !plan.shouldForward(3000) || !plan.shouldForward(3001) {
		t.Fatalf("audio-only plan must forward everything")
	}
}
