package langpack

import (
	"context"
	"os"
	"path/filepath"
	"strings"
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

func TestParseClientLangPackFileWithUnderscorePackName(t *testing.T) {
	packDir := filepath.Join(t.TempDir(), "android_x")
	if err := os.MkdirAll(packDir, 0o700); err != nil {
		t.Fatalf("mkdir fixture: %v", err)
	}
	path := filepath.Join(packDir, "android_x_en_v42.strings")
	writeLangPackFixture(t, path, `"TranslationMoreText" = "Translation Platform";`)

	pack, err := ParseTDesktopFile(path)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if pack.LangPack != "android_x" || pack.LangCode != "en" || pack.Version != 42 {
		t.Fatalf("pack meta = %+v", pack)
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
	root := filepath.Join("..", "..", "..", "data", "langpack", "android")
	candidates, _, err := scanSeedCandidates(root)
	if err != nil {
		t.Fatalf("scan bundled android packs: %v", err)
	}
	candidate, ok := candidates["android\x00fa"]
	if !ok {
		t.Fatal("bundled android fa pack not found")
	}
	pack, err := ParseTDesktopFile(candidate.path)
	if err != nil {
		t.Fatalf("parse bundled android fa pack: %v", err)
	}
	if pack.LangPack != "android" || pack.LangCode != "fa" || pack.Version <= 0 {
		t.Fatalf("pack meta = %+v, want versioned android/fa", pack)
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

func TestSeedDirectoryReconcilesManifest(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	packDir := filepath.Join(root, "tdesktop")
	if err := os.MkdirAll(packDir, 0o700); err != nil {
		t.Fatalf("mkdir pack dir: %v", err)
	}
	v1 := filepath.Join(packDir, "tdesktop_pt-BR_v1.strings")
	writeLangPackFixture(t, v1, `
"lng_language_name" = "Português (Brasil)";
"lng_old" = "old";
`)

	store := memory.NewLangPackStore()
	service := NewService(store)
	seeded, err := service.SeedDirectory(ctx, root)
	if err != nil || seeded != 2 {
		t.Fatalf("seed v1 = %d, %v", seeded, err)
	}
	pack, err := service.GetLangPack(ctx, "TDESKTOP", "pt_BR")
	if err != nil || pack.LangCode != "pt-br" || pack.Version != 1 || len(pack.Strings) != 2 {
		t.Fatalf("normalized pack = %+v, err %v", pack, err)
	}
	languages, err := service.ListLanguages(ctx, "tdesktop")
	if err != nil || findLanguage(languages, "pt-br") == nil {
		t.Fatalf("languages = %+v, err %v", languages, err)
	}
	if seeded, err := service.SeedDirectory(ctx, root); err != nil || seeded != 0 {
		t.Fatalf("unchanged seed = %d, %v", seeded, err)
	}

	v2 := filepath.Join(packDir, "tdesktop_pt-br_v2.strings")
	writeLangPackFixture(t, v2, `
"lng_language_name" = "Português do Brasil";
"lng_new" = "new";
`)
	seeded, err = service.SeedDirectory(ctx, root)
	if err != nil || seeded != 2 {
		t.Fatalf("seed v2 = %d, %v", seeded, err)
	}
	pack, err = service.GetLangPack(ctx, "tdesktop", "pt-br")
	if err != nil || pack.Version != 2 || len(pack.Strings) != 2 || stringValue(pack.Strings, "lng_old") != "" || stringValue(pack.Strings, "lng_new") != "new" {
		t.Fatalf("replaced pack = %+v, err %v", pack, err)
	}

	if err := os.Remove(v1); err != nil {
		t.Fatalf("remove v1: %v", err)
	}
	if err := os.Remove(v2); err != nil {
		t.Fatalf("remove v2: %v", err)
	}
	if err := os.Remove(packDir); err != nil {
		t.Fatalf("remove pack dir: %v", err)
	}
	if seeded, err := service.SeedDirectory(ctx, root); err != nil || seeded != 0 {
		t.Fatalf("reconcile removed file = %d, %v", seeded, err)
	}
	languages, err = service.ListLanguages(ctx, "tdesktop")
	if err != nil || len(languages) != 0 {
		t.Fatalf("languages after removal = %+v, err %v", languages, err)
	}
}

func TestSeedDirectoryRejectsVersionInvariantViolations(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	packDir := filepath.Join(root, "tdesktop")
	if err := os.MkdirAll(packDir, 0o700); err != nil {
		t.Fatalf("mkdir pack dir: %v", err)
	}
	v2 := filepath.Join(packDir, "tdesktop_fr_v2.strings")
	writeLangPackFixture(t, v2, `"lng_language_name" = "Français";`)
	service := NewService(memory.NewLangPackStore())
	if _, err := service.SeedDirectory(ctx, root); err != nil {
		t.Fatalf("seed v2: %v", err)
	}

	writeLangPackFixture(t, v2, `"lng_language_name" = "Français modifié";`)
	if _, err := service.SeedDirectory(ctx, root); err == nil || !strings.Contains(err.Error(), "without version bump") {
		t.Fatalf("same-version mutation error = %v", err)
	}
	pack, err := service.GetLangPack(ctx, "tdesktop", "fr")
	if err != nil || stringValue(pack.Strings, "lng_language_name") != "Français" {
		t.Fatalf("pack changed after rejected mutation = %+v, err %v", pack, err)
	}

	if err := os.Remove(v2); err != nil {
		t.Fatalf("remove v2: %v", err)
	}
	writeLangPackFixture(t, filepath.Join(packDir, "tdesktop_fr_v1.strings"), `"lng_language_name" = "Français";`)
	if _, err := service.SeedDirectory(ctx, root); err == nil || !strings.Contains(err.Error(), "version rollback") {
		t.Fatalf("version rollback error = %v", err)
	}
}

func TestBundledLangPackDirectoryReconciles(t *testing.T) {
	root := filepath.Join("..", "..", "..", "data", "langpack")
	service := NewService(memory.NewLangPackStore())
	seeded, err := service.SeedDirectory(context.Background(), root)
	if err != nil {
		t.Fatalf("seed bundled langpacks: %v", err)
	}
	if seeded < 50000 {
		t.Fatalf("seeded bundled strings = %d, want full catalog", seeded)
	}
	if seeded, err := service.SeedDirectory(context.Background(), root); err != nil || seeded != 0 {
		t.Fatalf("reconcile unchanged bundled langpacks = %d, %v", seeded, err)
	}
}

func writeLangPackFixture(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write langpack fixture %q: %v", path, err)
	}
}
