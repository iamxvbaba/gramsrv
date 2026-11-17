package files

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestDiceSeedRecognizedWithoutManualDirectoryRename(t *testing.T) {
	for _, emoji := range []string{"🎲", "🎯", "🏀", "⚽️", "🎳", "🎰"} {
		t.Run(emoji, func(t *testing.T) {
			root := t.TempDir()
			dir := filepath.Join(root, "telegram_default_stickers_export", "Dice_12345")
			if err := os.MkdirAll(dir, 0755); err != nil {
				t.Fatal(err)
			}
			data, _ := json.Marshal(seedStickerSetInfoJSON{InputSetType: "*tg.InputStickerSetDice", Emoticon: emoji, Result: seedStickerSetResultJSON{Set: seedStickerSetJSON{ID: 12345}}})
			if err := os.WriteFile(filepath.Join(dir, "set_info.json"), data, 0644); err != nil {
				t.Fatal(err)
			}
			entries, err := readSeedStickerSetEntries(root, 0)
			if err != nil || len(entries) != 1 {
				t.Fatalf("entries=%v err=%v", entries, err)
			}
			expected := emoji
			if emoji == "⚽️" {
				expected = "⚽"
			}
			if entries[0].systemKey != "dice:"+expected {
				t.Fatalf("system key=%q", entries[0].systemKey)
			}
		})
	}
}
