package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/tg"
	"rsc.io/qr"
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
		{size: 48_632, want: 64 << 10},
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

func TestDownloadPartSizeUsesNonPreciseChunkLadder(t *testing.T) {
	const oneMiB = 1 << 20
	for size := int64(1); size < 512<<10; size += 997 {
		partSize := downloadPartSize(size)
		if partSize < 4<<10 || partSize > 512<<10 || partSize%(4<<10) != 0 {
			t.Fatalf("downloadPartSize(%d) = %d is outside the valid 4 KiB-aligned range", size, partSize)
		}
		if oneMiB%partSize != 0 {
			t.Fatalf("downloadPartSize(%d) = %d does not divide a 1 MiB request window", size, partSize)
		}
		if int64(partSize) <= size {
			t.Fatalf("downloadPartSize(%d) = %d does not cover the known-size single chunk", size, partSize)
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

func TestParseSOCKS5Proxy(t *testing.T) {
	tests := []struct {
		name     string
		raw      string
		address  string
		authUser string
		authPass string
	}{
		{name: "bare host port", raw: "127.0.0.1:1080", address: "127.0.0.1:1080"},
		{name: "url host port", raw: "socks5://proxy.example:1080", address: "proxy.example:1080"},
		{name: "url trailing slash", raw: "socks5://proxy.example:1080/", address: "proxy.example:1080"},
		{name: "url ipv6", raw: "socks5://[2001:db8::1]:1080", address: "[2001:db8::1]:1080"},
		{name: "url auth", raw: "socks5://user:pass@proxy.example:1080", address: "proxy.example:1080", authUser: "user", authPass: "pass"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := parseSOCKS5Proxy(test.raw)
			if err != nil {
				t.Fatal(err)
			}
			if got.Address != test.address {
				t.Fatalf("address = %q, want %q", got.Address, test.address)
			}
			if test.authUser == "" {
				if got.Auth != nil {
					t.Fatalf("auth = %+v, want nil", got.Auth)
				}
				return
			}
			if got.Auth == nil || got.Auth.User != test.authUser || got.Auth.Password != test.authPass {
				t.Fatalf("auth = %+v, want %q/%q", got.Auth, test.authUser, test.authPass)
			}
		})
	}

	for _, raw := range []string{
		"",
		"127.0.0.1",
		"socks5://127.0.0.1",
		"http://127.0.0.1:1080",
		"socks5://127.0.0.1:1080/path",
		"socks5://127.0.0.1:1080?debug=true",
	} {
		t.Run("invalid "+raw, func(t *testing.T) {
			if _, err := parseSOCKS5Proxy(raw); err == nil {
				t.Fatalf("parseSOCKS5Proxy(%q) succeeded", raw)
			}
		})
	}
}

func TestClientOptions(t *testing.T) {
	// Login and fetching share the same egress config: SOCKS5_PROXY only affects the Resolver and must never rewrite the session path.
	t.Run("no proxy keeps default resolver", func(t *testing.T) {
		t.Setenv("SOCKS5_PROXY", "")
		opts, err := clientOptions(filepath.Join(t.TempDir(), "gift.session"))
		if err != nil {
			t.Fatal(err)
		}
		if opts.Resolver != nil {
			t.Fatal("resolver was set without SOCKS5_PROXY")
		}
		if opts.SessionStorage == nil {
			t.Fatal("session storage was not configured")
		}
	})
	t.Run("proxy installs resolver", func(t *testing.T) {
		t.Setenv("SOCKS5_PROXY", "socks5://proxy.example:1080")
		opts, err := clientOptions(filepath.Join(t.TempDir(), "gift.session"))
		if err != nil {
			t.Fatal(err)
		}
		if opts.Resolver == nil {
			t.Fatal("resolver was not set for SOCKS5_PROXY")
		}
		if opts.SessionStorage == nil {
			t.Fatal("session storage was lost when a proxy was configured")
		}
	})
	t.Run("invalid proxy is rejected", func(t *testing.T) {
		t.Setenv("SOCKS5_PROXY", "http://proxy.example:1080")
		if _, err := clientOptions(filepath.Join(t.TempDir(), "gift.session")); err == nil {
			t.Fatal("clientOptions accepted a non-socks5 proxy")
		}
	})
}

func TestQRPNGPath(t *testing.T) {
	if got := qrPNGPath("/tmp/gift.session", ""); got != "/tmp/gift.session.qr.png" {
		t.Fatalf("default = %q", got)
	}
	if got := qrPNGPath("/tmp/gift.session", "  /tmp/custom.png  "); got != "/tmp/custom.png" {
		t.Fatalf("override = %q", got)
	}
}

func TestRenderTerminalQR(t *testing.T) {
	code, err := qr.Encode("tg://login?token=AQKRKtNqRuP9uXfzvZO1aM0B1y11TJTdRix55tK-CQVEGw==", qr.H)
	if err != nil {
		t.Fatal(err)
	}
	out := renderTerminalQR(code)
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")

	// Each character carries two rows of modules, plus a two-module quiet zone on each side.
	wantDim := code.Size + 2*2
	wantLines := (wantDim + 1) / 2
	if len(lines) != wantLines {
		t.Fatalf("lines = %d, want %d", len(lines), wantLines)
	}
	runes := 0
	for _, line := range lines {
		n := len([]rune(line))
		if n != wantDim {
			t.Fatalf("line width = %d, want %d", n, wantDim)
		}
		runes += n
	}
	if runes == 0 {
		t.Fatal("rendered nothing")
	}
	// The quiet zone must be entirely white, otherwise a phone cannot scan it.
	for _, line := range lines {
		if !strings.HasPrefix(line, strings.Repeat(" ", 2)) || !strings.HasSuffix(line, strings.Repeat(" ", 2)) {
			t.Fatalf("quiet zone is not blank: %q", line)
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

func TestGiftTitleFallsBackForRegularGifts(t *testing.T) {
	tests := []struct {
		name string
		gift *tg.StarGift
		want string
	}{
		{"telegram title wins", &tg.StarGift{ID: 5170250947678437525, Title: "Chill Flame"}, "Chill Flame"},
		{"regular gift uses artwork name", &tg.StarGift{ID: 5170250947678437525}, "Gift Box"},
		{"unknown regular gift stays empty", &tg.StarGift{ID: 123}, ""},
	}
	for _, test := range tests {
		if got := giftTitle(test.gift); got != test.want {
			t.Fatalf("%s: giftTitle=%q want %q", test.name, got, test.want)
		}
	}
	// The named IDs must cover every regular gift and must not duplicate each other.
	seen := map[string]struct{}{}
	for id, title := range regularGiftTitles {
		if strings.TrimSpace(title) == "" || id <= 0 {
			t.Fatalf("invalid regular gift title entry %d=%q", id, title)
		}
		if _, duplicate := seen[title]; duplicate {
			t.Fatalf("duplicate regular gift title %q", title)
		}
		seen[title] = struct{}{}
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
