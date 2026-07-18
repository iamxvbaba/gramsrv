package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/tg"
)

func TestHasRenderableStickerAttribute(t *testing.T) {
	tests := []struct {
		name       string
		attributes []tg.DocumentAttributeClass
		want       bool
	}{
		{name: "sticker", attributes: []tg.DocumentAttributeClass{&tg.DocumentAttributeSticker{}}, want: true},
		{name: "custom emoji", attributes: []tg.DocumentAttributeClass{&tg.DocumentAttributeCustomEmoji{}}, want: true},
		{name: "ordinary file", attributes: []tg.DocumentAttributeClass{&tg.DocumentAttributeFilename{}}, want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := hasRenderableStickerAttribute(&tg.Document{Attributes: test.attributes}); got != test.want {
				t.Fatalf("hasRenderableStickerAttribute() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestDocumentExtension(t *testing.T) {
	tests := []struct {
		name string
		mime string
		want string
	}{
		{name: "gift.tgs", mime: "application/octet-stream", want: ".tgs"},
		{name: "", mime: "application/x-tgsticker", want: ".tgs"},
		{name: "unsafe.exe", mime: "video/webm", want: ".webm"},
		{name: "", mime: "application/octet-stream", want: ".bin"},
	}
	for _, test := range tests {
		if got := documentExtension(test.name, test.mime); got != test.want {
			t.Errorf("documentExtension(%q, %q) = %q, want %q", test.name, test.mime, got, test.want)
		}
	}
}

func TestBoundedBuffer(t *testing.T) {
	buffer := &boundedBuffer{max: 4}
	if _, err := buffer.Write([]byte("abc")); err != nil {
		t.Fatal(err)
	}
	if written, err := buffer.Write([]byte("def")); err == nil || written != 1 {
		t.Fatalf("overflow write = (%d, %v), want (1, error)", written, err)
	}
	if !bytes.Equal(buffer.Bytes(), []byte("abcd")) {
		t.Fatalf("buffer = %q, want abcd", buffer.Bytes())
	}
}

func TestDownloadPartSize(t *testing.T) {
	tests := []struct {
		size int64
		want int
	}{
		{size: 1, want: 4 << 10},
		{size: (4 << 10) - 1, want: 4 << 10},
		{size: 4 << 10, want: 8 << 10},
		{size: (512 << 10) - 1, want: 512 << 10},
		{size: 512 << 10, want: 512 << 10},
		{size: 1 << 20, want: 512 << 10},
	}
	for _, test := range tests {
		if got := downloadPartSize(test.size); got != test.want {
			t.Errorf("downloadPartSize(%d) = %d, want %d", test.size, got, test.want)
		}
	}
}

func TestParseAllowedMissingThumbs(t *testing.T) {
	allowed, err := parseAllowedMissingThumbs("5417911440709285239:photo:m,42:video:v")
	if err != nil {
		t.Fatal(err)
	}
	if !missingThumbAllowed(allowed, 5417911440709285239, "photo", "m") || !missingThumbAllowed(allowed, 42, "video", "v") {
		t.Fatalf("allowed = %v", allowed)
	}
	for _, invalid := range []string{"bad", "0:photo:m", "1:audio:m", "1:photo:?"} {
		if _, err := parseAllowedMissingThumbs(invalid); err == nil {
			t.Errorf("parseAllowedMissingThumbs(%q) succeeded", invalid)
		}
	}
}

func TestExistingArtifact(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "resource.bin"), []byte("gift"), 0o644); err != nil {
		t.Fatal(err)
	}
	data, reused, err := existingArtifact(root, "resource.bin", 4, 16)
	if err != nil || !reused || string(data) != "gift" {
		t.Fatalf("existingArtifact(valid) = (%q, %v, %v)", data, reused, err)
	}
	if _, reused, err := existingArtifact(root, "resource.bin", 5, 16); err != nil || reused {
		t.Fatalf("existingArtifact(size mismatch) = (reused=%v, err=%v)", reused, err)
	}
	if _, reused, err := existingArtifact(root, "missing.bin", -1, 16); err != nil || reused {
		t.Fatalf("existingArtifact(missing) = (reused=%v, err=%v)", reused, err)
	}
}

func TestReadTLArtifact(t *testing.T) {
	root := t.TempDir()
	var encoded bin.Buffer
	if err := (&tg.PaymentsStarGiftUpgradeAttributes{}).Encode(&encoded); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "attributes.tl"), encoded.Buf, 0o644); err != nil {
		t.Fatal(err)
	}
	var decoded tg.PaymentsStarGiftUpgradeAttributes
	artifact, err := readTLArtifact(root, "attributes.tl", &decoded)
	if err != nil {
		t.Fatal(err)
	}
	if artifact.Kind != "tl" || artifact.Size != int64(len(encoded.Buf)) || artifact.SHA256 == "" {
		t.Fatalf("artifact = %+v", artifact)
	}

	if err := os.WriteFile(filepath.Join(root, "trailing.tl"), append(append([]byte(nil), encoded.Buf...), 0xff), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readTLArtifact(root, "trailing.tl", &tg.PaymentsStarGiftUpgradeAttributes{}); err == nil {
		t.Fatal("expected trailing-byte error")
	}
}

func TestCollectUpgradeableGiftIDs(t *testing.T) {
	classes := []tg.StarGiftClass{
		&tg.StarGift{ID: 1, UpgradeStars: 10},
		&tg.StarGift{ID: 2, UpgradeVariants: 3},
		&tg.StarGift{ID: 3},
		&tg.StarGiftUnique{ID: 4, GiftID: 1},
	}
	got := collectUpgradeableGiftIDs(classes)
	if len(got) != 2 || got[0] != 1 || got[1] != 2 {
		t.Fatalf("collectUpgradeableGiftIDs() = %v, want [1 2]", got)
	}
}

func TestCollectUpgradeAttributes(t *testing.T) {
	modelDoc := testGiftDocument(101)
	patternDoc := testGiftDocument(102)
	model := &tg.StarGiftAttributeModel{
		Name:     "Crafted model",
		Document: modelDoc,
		Rarity:   &tg.StarGiftAttributeRarityLegendary{},
	}
	model.SetCrafted(true)
	result := &tg.PaymentsStarGiftUpgradeAttributes{Attributes: []tg.StarGiftAttributeClass{
		model,
		&tg.StarGiftAttributePattern{Name: "Pattern", Document: patternDoc, Rarity: &tg.StarGiftAttributeRarity{Permille: 125}},
		&tg.StarGiftAttributeBackdrop{Name: "Backdrop", BackdropID: 7, CenterColor: 1, EdgeColor: 2, PatternColor: 3, TextColor: 4, Rarity: &tg.StarGiftAttributeRarityEpic{}},
	}}
	added := make(map[int64]string)
	set, err := collectUpgradeAttributes(99, result, fileArtifact{Path: "upgrade-attributes/99.tl"}, func(class tg.DocumentClass, purpose string) (*tg.Document, error) {
		doc, ok := class.(*tg.Document)
		if !ok {
			return nil, errors.New("not a document")
		}
		added[doc.ID] = purpose
		return doc, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if set.AttributeCount != 3 || len(set.Models) != 1 || len(set.Patterns) != 1 || len(set.Backdrops) != 1 {
		t.Fatalf("unexpected attribute counts: %+v", set)
	}
	if !set.Models[0].Crafted || set.Models[0].Rarity.Kind != "legendary" {
		t.Fatalf("model = %+v", set.Models[0])
	}
	if set.Patterns[0].Rarity.Permille == nil || *set.Patterns[0].Rarity.Permille != 125 {
		t.Fatalf("pattern rarity = %+v", set.Patterns[0].Rarity)
	}
	if set.Backdrops[0].PatternColor != 3 || set.Backdrops[0].Rarity.Kind != "epic" {
		t.Fatalf("backdrop = %+v", set.Backdrops[0])
	}
	if len(set.DocumentIDs) != 2 || len(added) != 2 {
		t.Fatalf("document ids = %v, added = %v", set.DocumentIDs, added)
	}
}

func TestCollectUpgradeAttributesRejectsInstanceOnlyAttribute(t *testing.T) {
	_, err := collectUpgradeAttributes(99, &tg.PaymentsStarGiftUpgradeAttributes{Attributes: []tg.StarGiftAttributeClass{
		&tg.StarGiftAttributeOriginalDetails{},
	}}, fileArtifact{}, func(tg.DocumentClass, string) (*tg.Document, error) {
		return nil, nil
	})
	if err == nil {
		t.Fatal("expected unsupported-constructor error")
	}
}

func TestCollectRarityKinds(t *testing.T) {
	tests := []struct {
		class tg.StarGiftAttributeRarityClass
		kind  string
	}{
		{class: &tg.StarGiftAttributeRarityUncommon{}, kind: "uncommon"},
		{class: &tg.StarGiftAttributeRarityRare{}, kind: "rare"},
		{class: &tg.StarGiftAttributeRarityEpic{}, kind: "epic"},
		{class: &tg.StarGiftAttributeRarityLegendary{}, kind: "legendary"},
	}
	for _, test := range tests {
		got, err := collectRarity(test.class)
		if err != nil || got.Kind != test.kind || got.ConstructorID == "" {
			t.Fatalf("collectRarity(%T) = (%+v, %v)", test.class, got, err)
		}
	}
	if _, err := collectRarity(nil); err == nil {
		t.Fatal("expected nil-rarity error")
	}
}

func testGiftDocument(id int64) *tg.Document {
	return &tg.Document{
		ID:       id,
		Size:     1,
		MimeType: "application/x-tgsticker",
		Attributes: []tg.DocumentAttributeClass{
			&tg.DocumentAttributeCustomEmoji{Alt: "gift"},
		},
	}
}
