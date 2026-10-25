package officialgifts

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// 官方礼物快照会随 Telegram 上新礼物而增长。以下下界只用来识别空快照或被截断的
// 快照，不锁定任何一份具体归档，因此刷新快照不会让这些 opt-in 测试变红。
const (
	minOfficialSnapshotGifts    = 100
	minOfficialUpgradeableGifts = 50
)

func TestCatalogVerifiesSelectedDocument(t *testing.T) {
	root := t.TempDir()
	data := []byte("official-tgs")
	sum := sha256.Sum256(data)
	if err := os.MkdirAll(filepath.Join(root, "documents"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "documents", "10.tgs"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	value := manifest{Schema: manifestSchema, GiftCount: 1,
		Gifts:     []giftManifest{{Index: 0, Kind: "regular", ID: 1, Title: "Telegram Pin", Stars: 10, ConvertStars: 5, DocumentIDs: []int64{10}}},
		Documents: []documentManifest{{ID64: 10, FileName: "gift.tgs", File: fileArtifact{Path: "documents/10.tgs", Size: int64(len(data)), SHA256: hex.EncodeToString(sum[:])}}},
	}
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "manifest.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	catalog := New(root)
	items, err := catalog.List(context.Background())
	if err != nil || len(items) != 1 || items[0].ID != 1 || items[0].Title != "Telesrv Pin" {
		t.Fatalf("items=%+v err=%v", items, err)
	}
	bundle, err := catalog.Bundle(context.Background(), 1, false)
	if err != nil || string(bundle.BaseDocument.Data) != string(data) || bundle.Gift.Title != "Telesrv Pin" {
		t.Fatalf("bundle=%+v err=%v", bundle, err)
	}
	if err := os.WriteFile(filepath.Join(root, "documents", "10.tgs"), []byte("tampered---"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.Bundle(context.Background(), 1, false); err == nil {
		t.Fatal("tampered document was accepted")
	}
}

func TestConfiguredOfficialSnapshotIsComplete(t *testing.T) {
	root := os.Getenv("TELESRV_TEST_OFFICIAL_GIFTS_DIR")
	if root == "" {
		t.Skip("TELESRV_TEST_OFFICIAL_GIFTS_DIR is not set")
	}
	catalog := New(root)
	items, err := catalog.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var sets, attributes, crafted, upgradable, craftable int
	verifiedDocuments := map[int64]struct{}{}
	for _, item := range items {
		if item.CanUpgrade() {
			upgradable++
		}
		if item.CanCraft() {
			craftable++
		}
		include := item.ModelCount+item.PatternCount+item.BackdropCount > 0
		bundle, err := catalog.Bundle(context.Background(), item.ID, include)
		if err != nil {
			t.Fatalf("gift %d: %v", item.ID, err)
		}
		verifiedDocuments[bundle.BaseDocument.ID] = struct{}{}
		if bundle.Collectible == nil {
			if include {
				t.Fatalf("gift %d: manifest declares an attribute pool but no collectible was loaded", item.ID)
			}
			continue
		}
		// manifest 声明的属性池必须真的能加载出同样数量、且哈希已校验的文档。
		// 这一条替代了原先写死的属性总数：它检查的是「声明即可用」这个不变量，
		// 而不是某一份具体归档的数字。
		if len(bundle.Collectible.Models) != item.ModelCount ||
			len(bundle.Collectible.Patterns) != item.PatternCount ||
			len(bundle.Collectible.Backdrops) != item.BackdropCount {
			t.Fatalf("gift %d: manifest declares %d/%d/%d models/patterns/backdrops but loaded %d/%d/%d",
				item.ID, item.ModelCount, item.PatternCount, item.BackdropCount,
				len(bundle.Collectible.Models), len(bundle.Collectible.Patterns), len(bundle.Collectible.Backdrops))
		}
		sets++
		attributes += len(bundle.Collectible.Models) + len(bundle.Collectible.Patterns) + len(bundle.Collectible.Backdrops)
		for _, model := range bundle.Collectible.Models {
			verifiedDocuments[model.Document.ID] = struct{}{}
			if model.Crafted {
				crafted++
			}
		}
		for _, pattern := range bundle.Collectible.Patterns {
			verifiedDocuments[pattern.Document.ID] = struct{}{}
		}
	}
	// 官方目录会随 Telegram 上新礼物而变化。把礼物/属性总数写死，会让每次刷新快照
	// 都红灯，而真正的损坏（文档哈希不符、属性池被截断）反而被同样的噪声淹没。
	// 这里只断言与具体归档无关的下界：挡住空快照、截断快照和空属性池。
	if len(items) < minOfficialSnapshotGifts {
		t.Fatalf("catalog has %d gifts, want at least %d", len(items), minOfficialSnapshotGifts)
	}
	if upgradable < minOfficialUpgradeableGifts {
		t.Fatalf("only %d upgradeable gifts, want at least %d", upgradable, minOfficialUpgradeableGifts)
	}
	if len(verifiedDocuments) == 0 {
		t.Fatal("no documents were verified")
	}
	t.Logf("gifts=%d sets=%d attributes=%d crafted=%d upgradable=%d craftable=%d documents=%d",
		len(items), sets, attributes, crafted, upgradable, craftable, len(verifiedDocuments))
}

func TestGiftSummaryCapabilitiesRequireCompleteOfficialFacts(t *testing.T) {
	complete := GiftSummary{UpgradeStars: 25, ModelCount: 1, PatternCount: 1, BackdropCount: 1}
	if !complete.CanUpgrade() || complete.CanCraft() {
		t.Fatalf("complete regular pool capabilities = upgrade:%v craft:%v", complete.CanUpgrade(), complete.CanCraft())
	}
	craftable := complete
	craftable.CraftedModelCount = 1
	if !craftable.CanUpgrade() || !craftable.CanCraft() {
		t.Fatalf("crafted pool capabilities = upgrade:%v craft:%v", craftable.CanUpgrade(), craftable.CanCraft())
	}
	for name, invalid := range map[string]GiftSummary{
		"zero upgrade price": {ModelCount: 1, PatternCount: 1, BackdropCount: 1, CraftedModelCount: 1},
		"missing model":      {UpgradeStars: 25, PatternCount: 1, BackdropCount: 1, CraftedModelCount: 1},
		"missing pattern":    {UpgradeStars: 25, ModelCount: 1, BackdropCount: 1, CraftedModelCount: 1},
		"missing backdrop":   {UpgradeStars: 25, ModelCount: 1, PatternCount: 1, CraftedModelCount: 1},
	} {
		t.Run(name, func(t *testing.T) {
			if invalid.CanUpgrade() || invalid.CanCraft() {
				t.Fatalf("invalid facts advertised capabilities: %+v", invalid)
			}
		})
	}
}
