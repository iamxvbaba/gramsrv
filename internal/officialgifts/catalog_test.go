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
			continue
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
	if len(items) != 149 || sets != 116 || attributes != 40332 || crafted != 108 || upgradable != 114 || craftable != 2 || len(verifiedDocuments) != 8333 {
		t.Fatalf("gifts=%d sets=%d attributes=%d crafted=%d upgradable=%d craftable=%d documents=%d",
			len(items), sets, attributes, crafted, upgradable, craftable, len(verifiedDocuments))
	}
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
