package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/iamxvbaba/td/tg"
)

func TestSpecToInputSystemSets(t *testing.T) {
	tests := []struct {
		spec  string
		label string
		want  any
	}{
		{"emoji_default_statuses", "EmojiDefaultStatuses", &tg.InputStickerSetEmojiDefaultStatuses{}},
		{"emoji_channel_default_statuses", "EmojiChannelDefaultStatuses", &tg.InputStickerSetEmojiChannelDefaultStatuses{}},
		{"emoji_default_topic_icons", "EmojiDefaultTopicIcons", &tg.InputStickerSetEmojiDefaultTopicIcons{}},
		{"premium_gifts", "PremiumGifts", &tg.InputStickerSetPremiumGifts{}},
		{"ton_gifts", "TonGifts", &tg.InputStickerSetTonGifts{}},
	}
	for _, tt := range tests {
		t.Run(tt.spec, func(t *testing.T) {
			input, label, err := specToInput(tt.spec)
			if err != nil {
				t.Fatalf("specToInput: %v", err)
			}
			if label != tt.label {
				t.Fatalf("label = %q, want %q", label, tt.label)
			}
			if got, want := fmt.Sprintf("%T", input), fmt.Sprintf("%T", tt.want); got != want {
				t.Fatalf("input = %s, want %s", got, want)
			}
		})
	}
}

func TestSpecToInputShortName(t *testing.T) {
	input, label, err := specToInput("short:FestiveFontEmoji")
	if err != nil {
		t.Fatalf("specToInput short: %v", err)
	}
	short, ok := input.(*tg.InputStickerSetShortName)
	if !ok {
		t.Fatalf("input = %T, want short name", input)
	}
	if short.ShortName != "FestiveFontEmoji" || label != "FestiveFontEmoji" {
		t.Fatalf("short input = (%q, %q), want FestiveFontEmoji", short.ShortName, label)
	}
}

func TestCompleteExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "doc.tgs")
	if completeExistingFile(path, 4) {
		t.Fatal("missing file reported complete")
	}
	if err := os.WriteFile(path, []byte("tgs!"), 0o644); err != nil {
		t.Fatalf("write temp doc: %v", err)
	}
	if !completeExistingFile(path, 4) {
		t.Fatal("matching file reported incomplete")
	}
	if completeExistingFile(path, 5) {
		t.Fatal("mismatched file reported complete")
	}
}

func TestSpecToInputExtraSets(t *testing.T) {
	tests := []struct {
		spec string
		want any
	}{
		{"id:42:-7", &tg.InputStickerSetID{ID: 42, AccessHash: -7}},
		{"dice:🎲", &tg.InputStickerSetDice{Emoticon: "🎲"}},
		{"animated_emoji", &tg.InputStickerSetAnimatedEmoji{}},
		{"animated_emoji_animations", &tg.InputStickerSetAnimatedEmojiAnimations{}},
		{"emoji_generic_animations", &tg.InputStickerSetEmojiGenericAnimations{}},
	}
	for _, tt := range tests {
		t.Run(tt.spec, func(t *testing.T) {
			input, _, err := specToInput(tt.spec)
			if err != nil {
				t.Fatalf("specToInput: %v", err)
			}
			if !reflect.DeepEqual(input, tt.want) {
				t.Fatalf("input = %#v, want %#v", input, tt.want)
			}
		})
	}
	for _, bad := range []string{"id:42", "id:x:1", "id:1:2:3"} {
		if _, _, err := specToInput(bad); err == nil {
			t.Fatalf("specToInput(%q) accepted a malformed id spec", bad)
		}
	}
}

func TestAPICredentialsOverride(t *testing.T) {
	t.Setenv("TG_API_ID", "")
	t.Setenv("TG_API_HASH", "")
	if id, hash := apiCredentials(1, "def"); id != 1 || hash != "def" {
		t.Fatalf("defaults = (%d, %q)", id, hash)
	}
	t.Setenv("TG_API_ID", "12345")
	t.Setenv("TG_API_HASH", "custom")
	if id, hash := apiCredentials(1, "def"); id != 12345 || hash != "custom" {
		t.Fatalf("override = (%d, %q)", id, hash)
	}
	t.Setenv("TG_API_ID", "not-a-number")
	if id, _ := apiCredentials(1, "def"); id != 1 {
		t.Fatalf("invalid TG_API_ID used: %d", id)
	}
}

var updateGolden = flag.Bool("update", false, "rewrite testdata/reactions_export")

// fakeFetch writes deterministic content per location instead of downloading.
func fakeFetch(_ context.Context, loc tg.InputFileLocationClass, _, path string) error {
	return os.WriteFile(path, fakeContent(loc.(*tg.InputDocumentFileLocation)), 0o644)
}

func fakeContent(loc *tg.InputDocumentFileLocation) []byte {
	if loc.ThumbSize != "" {
		return []byte(fmt.Sprintf("thumb %d %s", loc.ID, loc.ThumbSize))
	}
	return []byte(fmt.Sprintf("document %d", loc.ID))
}

func fakeReactionDoc(id int64, mime string) *tg.Document {
	thumb := fakeContent(&tg.InputDocumentFileLocation{ID: id, ThumbSize: "m"})
	return &tg.Document{
		ID: id, AccessHash: id * 10, FileReference: []byte{0x01, 0x02}, Date: 1700000000,
		MimeType: mime, Size: int64(len(fakeContent(&tg.InputDocumentFileLocation{ID: id}))), DCID: 2,
		Attributes: []tg.DocumentAttributeClass{&tg.DocumentAttributeImageSize{W: 512, H: 512}},
		Thumbs: []tg.PhotoSizeClass{
			&tg.PhotoPathSize{Type: "j", Bytes: []byte{0x10, 0x20}},
			&tg.PhotoSize{Type: "m", W: 128, H: 128, Size: len(thumb)},
		},
	}
}

// TestWriteReactionsExportMatchesGolden pins the layout that seedReactions in
// internal/app/files imports; TestSeedMediaImportsStickerfetchReactionsExport
// there seeds from the same golden directory.
func TestWriteReactionsExportMatchesGolden(t *testing.T) {
	shared := fakeReactionDoc(3000001, "application/x-tgsticker")
	reactions := []tg.AvailableReaction{
		{
			Reaction: "👍", Title: "Thumbs Up",
			StaticIcon:        fakeReactionDoc(1000001, "image/webp"),
			AppearAnimation:   fakeReactionDoc(1000002, "application/x-tgsticker"),
			SelectAnimation:   fakeReactionDoc(1000003, "application/x-tgsticker"),
			ActivateAnimation: fakeReactionDoc(1000004, "application/x-tgsticker"),
			EffectAnimation:   fakeReactionDoc(1000005, "application/x-tgsticker"),
			AroundAnimation:   shared,
			CenterIcon:        fakeReactionDoc(1000006, "application/x-tgsticker"),
		},
		{
			Reaction: "🔥", Title: "Fire", Premium: true,
			StaticIcon:        fakeReactionDoc(2000001, "image/webp"),
			AppearAnimation:   fakeReactionDoc(2000002, "application/x-tgsticker"),
			SelectAnimation:   fakeReactionDoc(2000003, "application/x-tgsticker"),
			ActivateAnimation: fakeReactionDoc(2000004, "application/x-tgsticker"),
			EffectAnimation:   fakeReactionDoc(2000005, "application/x-tgsticker"),
			AroundAnimation:   shared,
		},
	}
	reactions[0].SetFlags()
	reactions[1].SetFlags()

	out := t.TempDir()
	if err := writeReactionsExport(context.Background(), fakeFetch, out, reactions); err != nil {
		t.Fatalf("writeReactionsExport: %v", err)
	}
	golden := filepath.Join("testdata", "reactions_export")
	if *updateGolden {
		if err := os.RemoveAll(golden); err != nil {
			t.Fatal(err)
		}
		if err := os.CopyFS(golden, os.DirFS(out)); err != nil {
			t.Fatal(err)
		}
	}
	got, want := readTree(t, out), readTree(t, golden)
	for name, data := range want {
		if g, ok := got[name]; !ok {
			t.Errorf("missing %s", name)
		} else if string(g) != string(data) {
			t.Errorf("%s differs from golden (run go test -update):\n%s", name, g)
		}
	}
	for name := range got {
		if _, ok := want[name]; !ok {
			t.Errorf("unexpected %s (run go test -update)", name)
		}
	}
}

func readTree(t *testing.T, root string) map[string][]byte {
	t.Helper()
	files := map[string][]byte{}
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		files[filepath.ToSlash(rel)] = data
		return err
	})
	if err != nil {
		t.Fatalf("read %s: %v", root, err)
	}
	return files
}
