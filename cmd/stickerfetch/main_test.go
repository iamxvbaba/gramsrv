package main

import (
	"fmt"
	"os"
	"path/filepath"
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
