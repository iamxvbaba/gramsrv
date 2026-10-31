package stargifts_test

import (
	"context"
	"os"
	"testing"

	"telesrv/internal/app/stargifts"
	"telesrv/internal/officialgifts"
)

// The official gift snapshot grows as Telegram adds new gifts; this lower bound only
// identifies an empty or truncated snapshot, and does not pin any specific archive.
const minOfficialSnapshotDocuments = 1000

// This opt-in test is run by the official import audit. It validates every distinct base,
// model and pattern document with the trusted official animation policy, including the
// small set of Telegram-authored expression animations.
func TestConfiguredOfficialSnapshotAnimations(t *testing.T) {
	root := os.Getenv("TELESRV_TEST_OFFICIAL_GIFTS_DIR")
	if root == "" {
		t.Skip("TELESRV_TEST_OFFICIAL_GIFTS_DIR is not set")
	}
	catalog := officialgifts.New(root)
	items, err := catalog.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	service := &stargifts.Service{}
	seen := map[int64]struct{}{}
	validate := func(document officialgifts.Document) {
		t.Helper()
		if _, ok := seen[document.ID]; ok {
			return
		}
		seen[document.ID] = struct{}{}
		if _, err := service.PrepareOfficialAnimation(document.FileName, document.Data); err != nil {
			t.Fatalf("document %d (%s): %v", document.ID, document.Path, err)
		}
	}
	for _, item := range items {
		bundle, err := catalog.Bundle(context.Background(), item.ID, item.ModelCount+item.PatternCount+item.BackdropCount > 0)
		if err != nil {
			t.Fatalf("gift %d: %v", item.ID, err)
		}
		validate(bundle.BaseDocument)
		if bundle.Collectible == nil {
			continue
		}
		for _, model := range bundle.Collectible.Models {
			validate(model.Document)
		}
		for _, pattern := range bundle.Collectible.Patterns {
			validate(pattern.Document)
		}
	}
	// Every gift contributes at least its base document, so the lower bound is taken by
	// gift count; an absolute lower bound then guards against a truncated snapshot. The old
	// hard-coded 8333 was just the number for one specific archive; the official catalog
	// changes with every addition, so it got tangled up with real animation validation failures.
	if len(seen) < len(items) {
		t.Fatalf("validated %d documents for %d gifts, want at least one per gift", len(seen), len(items))
	}
	if len(seen) < minOfficialSnapshotDocuments {
		t.Fatalf("validated %d documents, want at least %d", len(seen), minOfficialSnapshotDocuments)
	}
	t.Logf("validated %d documents across %d gifts", len(seen), len(items))
}
