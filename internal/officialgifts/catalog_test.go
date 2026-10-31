package officialgifts

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// The official gift snapshot grows as Telegram adds new gifts. The lower bounds
// below only serve to detect an empty or truncated snapshot; they do not pin any
// specific archive, so refreshing the snapshot will not turn these opt-in tests
// red.
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

// An attribute with permille 0 is valid (present in the pool but never drawn);
// only an out-of-range weight should be rejected.
func TestValidRarityPermilleRange(t *testing.T) {
	weight := func(value int) *int { return &value }
	cases := []struct {
		name     string
		rarity   Rarity
		crafted  bool
		expected bool
	}{
		{"zero weight", Rarity{Kind: "permille", Permille: weight(0)}, false, true},
		{"single permille", Rarity{Kind: "permille", Permille: weight(1)}, false, true},
		{"full weight", Rarity{Kind: "permille", Permille: weight(1000)}, false, true},
		{"above range", Rarity{Kind: "permille", Permille: weight(1001)}, false, false},
		{"negative", Rarity{Kind: "permille", Permille: weight(-1)}, false, false},
		{"missing weight", Rarity{Kind: "permille"}, false, false},
		{"crafted permille", Rarity{Kind: "permille", Permille: weight(10)}, true, false},
		{"named rarity", Rarity{Kind: "legendary"}, true, true},
	}
	for _, tc := range cases {
		if got := validRarity(tc.rarity, tc.crafted); got != tc.expected {
			t.Errorf("%s: validRarity=%v want %v", tc.name, got, tc.expected)
		}
	}
}

// The pool of a sold-out limited gift contains a batch of old variants with
// permille 0; the loader must accept the pool as a whole, otherwise the entire
// official snapshot would become unusable because of a single gift.
func TestCatalogAcceptsZeroPermilleAttributes(t *testing.T) {
	root := t.TempDir()
	write := func(id int64, data string) fileArtifact {
		path := filepath.Join("documents", fmt.Sprintf("%d.tgs", id))
		if err := os.MkdirAll(filepath.Join(root, "documents"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, path), []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256([]byte(data))
		return fileArtifact{Path: path, Size: int64(len(data)), SHA256: hex.EncodeToString(sum[:])}
	}
	weight := func(value int) *int { return &value }

	value := manifest{
		Schema:    manifestSchema,
		GiftCount: 1,
		Gifts: []giftManifest{{Index: 0, Kind: "regular", ID: 1, Title: "Sold Out Gift",
			Stars: 500, UpgradeStars: 452018, SoldOut: true, DocumentIDs: []int64{10}}},
		UpgradeAttributeSets: []collectibleManifest{{
			GiftID64:       1,
			AttributeCount: 4,
			Models: []modelManifest{
				{Name: "Retired Variant", DocumentID: 11, Rarity: Rarity{Kind: "permille", Permille: weight(0)}},
				{Name: "Drawn Variant", DocumentID: 12, Rarity: Rarity{Kind: "permille", Permille: weight(1000)}},
			},
			Patterns:  []patternManifest{{Name: "Coin", DocumentID: 13, Rarity: Rarity{Kind: "permille", Permille: weight(1000)}}},
			Backdrops: []backdropManifest{{Name: "Neon Blue", BackdropID: 1, Rarity: Rarity{Kind: "permille", Permille: weight(1000)}}},
		}},
		Documents: []documentManifest{
			{ID64: 10, FileName: "gift.tgs", File: write(10, "official-tgs")},
			{ID64: 11, FileName: "AnimatedSticker.tgs", File: write(11, "retired-model")},
			{ID64: 12, FileName: "AnimatedSticker.tgs", File: write(12, "drawn-model")},
			{ID64: 13, FileName: "AnimatedSticker.tgs", File: write(13, "pattern")},
		},
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
	if err != nil || len(items) != 1 {
		t.Fatalf("items=%+v err=%v", items, err)
	}
	bundle, err := catalog.Bundle(context.Background(), 1, true)
	if err != nil {
		t.Fatalf("bundle: %v", err)
	}
	if bundle.Collectible == nil || len(bundle.Collectible.Models) != 2 {
		t.Fatalf("collectible=%+v", bundle.Collectible)
	}
	if got := bundle.Collectible.Models[0].Rarity.Permille; got == nil || *got != 0 {
		t.Fatalf("zero-weight model rarity=%v", got)
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
		// The attribute pool declared by the manifest must actually load the same
		// number of documents, with their hashes verified. This replaces the
		// previously hard-coded attribute totals: it checks the "declared means
		// usable" invariant rather than the numbers of a specific archive.
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
	// The official catalog changes as Telegram adds new gifts. Hard-coding the
	// gift/attribute totals would turn red on every snapshot refresh, while real
	// corruption (mismatched document hashes, a truncated attribute pool) would be
	// drowned out by the same noise. Here we only assert archive-independent lower
	// bounds: they catch an empty snapshot, a truncated snapshot and an empty
	// attribute pool.
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
