package stargifts_test

import (
	"context"
	"os"
	"testing"

	"telesrv/internal/app/stargifts"
	"telesrv/internal/officialgifts"
)

// 官方礼物快照会随 Telegram 上新礼物而增长，这个下界只用来识别空快照或被截断的
// 快照，不锁定任何一份具体归档。
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
	// 每个礼物都至少贡献了它的 base 文档，所以这里按礼物数取下界；
	// 再加一个绝对下界挡住被截断的快照。原先写死的 8333 只是某一份具体归档的数字，
	// 官方目录每次上新都会变，与真正的动画校验失败混在一起。
	if len(seen) < len(items) {
		t.Fatalf("validated %d documents for %d gifts, want at least one per gift", len(seen), len(items))
	}
	if len(seen) < minOfficialSnapshotDocuments {
		t.Fatalf("validated %d documents, want at least %d", len(seen), minOfficialSnapshotDocuments)
	}
	t.Logf("validated %d documents across %d gifts", len(seen), len(items))
}
