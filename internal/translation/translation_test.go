// Package translation guards the repository against natural-language text
// (Chinese or Russian) creeping back into source files.
//
// Comments, logs and diagnostics in this repo are written in English. The only
// places where CJK or Cyrillic is legitimate are the localized, user-facing
// resources (language packs, the admin/grammystore i18n bundles, the localized
// docs) and a small set of functional literals that must keep their original
// script (brand-name matching, user-facing Russian product strings, Unicode test
// data). Those are listed in allowedPaths below.
//
// The test walks the working tree and fails if a scanned file contains a run of
// Han or Cyrillic characters outside that allowlist. Planned/unfinished files
// (dist bundles, testdata fixtures, generated artifacts under data/) are skipped.
package translation

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"unicode"
)

// scannedExt is the set of extensions whose natural-language text must be English.
var scannedExt = map[string]bool{
	".go":      true,
	".sql":     true,
	".md":      true,
	".tsx":     true,
	".ts":      true,
	".js":      true,
	".json":    true,
	".yaml":    true,
	".yml":     true,
	".ps1":     true,
	".sh":      true,
	".example": true,
	".html":    true,
	".css":     true,
	".bak":     true,
}

// skippedDirs are never walked: VCS metadata, dependencies, built bundles and
// fixtures that intentionally carry non-English sample data.
var skippedDirs = map[string]bool{
	".git":         true,
	"node_modules": true,
	"dist":         true,
	"testdata":     true,
	"vendor":       true,
}

// allowedPaths are repo-relative (slash-separated) prefixes or exact paths where
// CJK/Cyrillic is expected and correct.
var allowedPaths = []string{
	"data/", // language packs, official-gifts/seed data, emoji keywords
	"cmd/telesrv-admin/web/src/i18n.tsx",
	"cmd/bots/grammystore/src/i18n.js",
	"cmd/bots/grammystore/src/catalog.js", // titleRu / descriptionRu fields
	"cmd/bots/grammystore/test/",          // assertions on the ru locale
	"docs/configuration.zh-CN.md",
	"docs/admin-panel-api.ru.md",
	"docs/bot-api.ru.md",
	"README.zh-CN.md",
	"README.md",                                   // holds the "中文 README" locale link
	"deploy/update/manifest.example.json",         // localized "notes" en/ru
	"deploy/migrations/0167_premium_stars.up.sql", // seeded Russian bot text
	"internal/store/postgres/premium.go",          // seeded Russian premium-bot profile text
	"internal/branding/branding.go",               // officialBrandRE matches "телеграм"
	"internal/branding/branding_test.go",          // asserted localized product-name test data
	"internal/seed/catalog/emoji_keywords_en.json",
	"internal/updatecdn/catalog_test.go",                         // asserts on localized "ru" manifest notes
	"internal/app/telegramlogin/url_test.go",                     // IDNA/punycode test data ("例子.测试")
	"internal/app/files/blobfs_test.go",                          // CJK bytes as content-addressing test data ("你好")
	"internal/rpc/langpack_compat_test.go",                       // localized langpack fixture values
	"internal/rpc/help_app_update_test.go",                       // asserts on localized "ru" update notes
	"internal/rpc/precheckout_test.go",                           // asserts on Russian invoice test data
	"internal/rpc/stories_rpc_test.go",                           // Unicode payload test data ("礼物")
	"internal/rpc/bot_invoice_chain_test.go",                     // Russian invoice test data
	"internal/rpc/bot_invoice_postgres_test.go",                  // Russian invoice test data
	"internal/rpc/bot_invoice_projection_test.go",                // Russian invoice test data
	"internal/rpc/botapi_entities_autodetect_test.go",            // Cyrillic entity-offset test inputs
	"internal/rpc/messages_send_split_test.go",                   // asserts on Russian rune-count test data
	"internal/rpc/moderation_profile_warning_projection_test.go", // Russian About-text test data
	"internal/observability/metrics/registry_sort_test.go",       // Unicode test data ("雪")
	"internal/observability/metrics/registry_export_test.go",     // Unicode test data ("雪")
	"internal/app/bots/gramsrvbot.go",                            // user-facing Russian bot replies
	"internal/app/bots/gramsrv_operator.go",                      // user-facing Russian bot replies
	"internal/app/bots/premiumbot.go",                            // user-facing Russian bot strings
	"internal/app/bots/premiumbot_test.go",                       // asserts on Russian bot output
	"internal/domain/star_gift.go",                               // user-facing Russian error string
	"internal/domain/gif_catalog.go",                             // functional GIF-category keyword data
	"internal/domain/gif_catalog_test.go",                        // GIF keyword test data
	"internal/domain/moderation_warning_test.go",                 // Russian moderation fixtures
	"internal/giftclaim/http.go",                                 // user-facing Russian HTML UI
	"internal/translation/",                                      // this guard carries script samples in its own comments
}

func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	// file = <root>/internal/translation/translation_test.go
	return filepath.Dir(filepath.Dir(filepath.Dir(file)))
}

func allowed(rel string) bool {
	rel = filepath.ToSlash(rel)
	for _, p := range allowedPaths {
		if rel == strings.TrimSuffix(p, "/") || strings.HasPrefix(rel, p) {
			return true
		}
	}
	return false
}

// containsRun reports whether s holds a run of at least two Han ideographs or two
// Cyrillic letters. Requiring a run avoids flagging isolated CJK/emoji symbols
// (e.g. "㊗", "🉑") that appear in otherwise-English data.
func containsRun(s string) bool {
	han, cyr := 0, 0
	for _, r := range s {
		switch {
		case unicode.Is(unicode.Han, r):
			han++
			cyr = 0
		case unicode.In(r, unicode.Cyrillic):
			cyr++
			han = 0
		default:
			han, cyr = 0, 0
		}
		if han >= 2 || cyr >= 2 {
			return true
		}
	}
	return false
}

func TestSourceTextIsEnglishOutsideAllowedPaths(t *testing.T) {
	root := repoRoot(t)
	var offenders []string

	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := d.Name()
		if d.IsDir() {
			if skippedDirs[name] {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(name, ".") || strings.Contains(name, ".session") {
			return nil
		}
		if !scannedExt[strings.ToLower(filepath.Ext(name))] {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		if allowed(rel) {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		if containsRun(string(data)) {
			offenders = append(offenders, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}

	if len(offenders) > 0 {
		t.Fatalf("found %d file(s) with Chinese/Russian text outside the allowlist; "+
			"translate them to English or add a justified entry to allowedPaths:\n  %s",
			len(offenders), strings.Join(offenders, "\n  "))
	}
}
