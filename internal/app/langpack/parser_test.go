package langpack

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"telesrv/internal/store/memory"
)

func TestParseTDesktopFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tdesktop_en_v42.strings")
	if err := os.WriteFile(path, []byte(`
"lng_plain" = "Plain value";
"lng_escape" = "Line\nTwo";
"lng_items#one" = "{count} item";
"lng_items#other" = "{count} items";
`), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	pack, err := ParseTDesktopFile(path)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if pack.LangPack != "tdesktop" || pack.LangCode != "en" || pack.Version != 42 {
		t.Fatalf("pack meta = %+v", pack)
	}
	if len(pack.Strings) != 3 {
		t.Fatalf("strings count = %d, want 3", len(pack.Strings))
	}
	if got := pack.Strings[1].Value; got != "Line\nTwo" {
		t.Fatalf("escape value = %q", got)
	}
	plural := pack.Strings[2]
	if !plural.Pluralized || plural.Key != "lng_items" || plural.OneValue == "" || plural.OtherValue == "" {
		t.Fatalf("plural string = %+v", plural)
	}
}

func TestParseClientLangPackFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "weba_en_v12000000.strings")
	if err := os.WriteFile(path, []byte(`
"NewMessageTitle" = "New Message";
`), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	pack, err := ParseTDesktopFile(path)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if pack.LangPack != "weba" || pack.LangCode != "en" || pack.Version != 12000000 {
		t.Fatalf("pack meta = %+v", pack)
	}
	if len(pack.Strings) != 1 || pack.Strings[0].Key != "NewMessageTitle" {
		t.Fatalf("strings = %+v", pack.Strings)
	}
}

func TestSeedDirectoryWalksClientSubdirs(t *testing.T) {
	root := t.TempDir()
	for _, item := range []struct {
		dir  string
		file string
		key  string
	}{
		{dir: "tdesktop", file: "tdesktop_en_v1.strings", key: "lng_language_name"},
		{dir: "weba", file: "weba_en_v2.strings", key: "NewMessageTitle"},
	} {
		dir := filepath.Join(root, item.dir)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("mkdir fixture: %v", err)
		}
		content := []byte(`"` + item.key + `" = "value";`)
		if err := os.WriteFile(filepath.Join(dir, item.file), content, 0o600); err != nil {
			t.Fatalf("write fixture: %v", err)
		}
	}

	store := memory.NewLangPackStore()
	service := NewService(store)
	seeded, err := service.SeedDirectory(context.Background(), root)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	if seeded != 2 {
		t.Fatalf("seeded = %d, want 2", seeded)
	}
	pack, err := service.GetLangPack(context.Background(), "weba", "en")
	if err != nil {
		t.Fatalf("get weba pack: %v", err)
	}
	if pack.Version != 2 || len(pack.Strings) != 1 || pack.Strings[0].Key != "NewMessageTitle" {
		t.Fatalf("weba pack = %+v", pack)
	}
}

func TestBundledAndroidPersianLangPackParses(t *testing.T) {
	path := filepath.Join("..", "..", "..", "data", "langpack", "android", "android_fa_v59634849.strings")
	pack, err := ParseTDesktopFile(path)
	if err != nil {
		t.Fatalf("parse bundled android fa pack: %v", err)
	}
	if pack.LangPack != "android" || pack.LangCode != "fa" || pack.Version != 59634849 {
		t.Fatalf("pack meta = %+v, want android/fa v59634849", pack)
	}
	if len(pack.Strings) < 10000 {
		t.Fatalf("strings count = %d, want full android fa pack", len(pack.Strings))
	}
	wantPersian := "\u0641\u0627\u0631\u0633\u06cc"
	for _, item := range pack.Strings {
		if item.Key == "TranslateLanguageFA" {
			if item.Value != wantPersian {
				t.Fatalf("TranslateLanguageFA = %q, want %q", item.Value, wantPersian)
			}
			return
		}
	}
	t.Fatalf("TranslateLanguageFA not found in bundled android fa pack")
}
