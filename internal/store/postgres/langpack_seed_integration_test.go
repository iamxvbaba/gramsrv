package postgres

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	applangpack "telesrv/internal/app/langpack"
	"telesrv/internal/domain"
)

func TestLangPackSeedReconciliationPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	packName := "seedtest-" + randomSuffix(t)
	store := NewLangPackStore(pool)
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DELETE FROM lang_pack_strings WHERE lang_pack = $1", packName)
		_, _ = pool.Exec(ctx, "DELETE FROM lang_packs WHERE lang_pack = $1", packName)
		_, _ = pool.Exec(ctx, "DELETE FROM seed_states WHERE key LIKE $1 OR key = $2 OR key = $3", "langpack:v1:entry:"+packName+":%", "langpack:v1:catalog:"+packName, "langpack:v2:catalog:"+packName)
	})

	seedV1 := domain.LangPackSeed{
		Catalog: packName,
		Scopes:  []string{packName},
		Packs: []domain.LangPackSeedEntry{{
			SourceHash:    "source-v1",
			ContentHash:   "hash-v1",
			StringsCount:  2,
			ContentLoaded: true,
			Pack: domain.LangPack{
				LangPack: packName,
				LangCode: "fr",
				Version:  1,
				Strings: []domain.LangPackString{
					{Key: "LanguageName", Value: "Français"},
					{Key: "old", Value: "old"},
				},
			},
		}},
	}
	written, err := store.ReconcileSeed(ctx, seedV1)
	if err != nil || written != 2 {
		t.Fatalf("reconcile v1 = %d, %v", written, err)
	}
	seedV1Unloaded := seedV1
	seedV1Unloaded.Packs = append([]domain.LangPackSeedEntry(nil), seedV1.Packs...)
	seedV1Unloaded.Packs[0].Pack.Strings = nil
	seedV1Unloaded.Packs[0].ContentLoaded = false
	if written, err := store.ReconcileSeed(ctx, seedV1Unloaded); err != nil || written != 0 {
		t.Fatalf("reconcile unchanged v1 = %d, %v", written, err)
	}
	catalog, err := store.GetSeedCatalog(ctx, packName)
	if err != nil || len(catalog.Packs) != 1 || catalog.Packs[0].SourceHash != "source-v1" {
		t.Fatalf("seed catalog = %+v, %v", catalog, err)
	}

	seedV2 := domain.LangPackSeed{
		Catalog: packName,
		Scopes:  []string{packName},
		Packs: []domain.LangPackSeedEntry{{
			SourceHash:    "source-v2",
			ContentHash:   "hash-v2",
			StringsCount:  2,
			ContentLoaded: true,
			Pack: domain.LangPack{
				LangPack: packName,
				LangCode: "fr",
				Version:  2,
				Strings: []domain.LangPackString{
					{Key: "LanguageName", Value: "Français v2"},
					{Key: "new", Value: "new"},
				},
			},
		}},
	}
	written, err = store.ReconcileSeed(ctx, seedV2)
	if err != nil || written != 2 {
		t.Fatalf("reconcile v2 = %d, %v", written, err)
	}
	pack, err := store.GetPack(ctx, packName, "fr", 0)
	if err != nil || pack.Version != 2 || len(pack.Strings) != 2 || postgresLangPackValue(pack.Strings, "old") != "" || postgresLangPackValue(pack.Strings, "new") != "new" {
		t.Fatalf("replaced postgres pack = %+v, err %v", pack, err)
	}

	mutated := seedV2
	mutated.Packs = append([]domain.LangPackSeedEntry(nil), seedV2.Packs...)
	mutated.Packs[0].ContentHash = "changed-without-version"
	if _, err := store.ReconcileSeed(ctx, mutated); err == nil || !strings.Contains(err.Error(), "without version bump") {
		t.Fatalf("same-version mutation error = %v", err)
	}
	rollback := seedV2
	rollback.Packs = append([]domain.LangPackSeedEntry(nil), seedV2.Packs...)
	rollback.Packs[0].Pack.Version = 1
	rollback.Packs[0].ContentHash = "rollback"
	if _, err := store.ReconcileSeed(ctx, rollback); err == nil || !strings.Contains(err.Error(), "version rollback") {
		t.Fatalf("rollback error = %v", err)
	}

	if written, err := store.ReconcileSeed(ctx, domain.LangPackSeed{Catalog: packName}); err != nil || written != 0 {
		t.Fatalf("remove missing language = %d, %v", written, err)
	}
	languages, err := store.ListLanguages(ctx, packName)
	if err != nil || len(languages) != 0 {
		t.Fatalf("languages after manifest removal = %+v, err %v", languages, err)
	}
}

func postgresLangPackValue(items []domain.LangPackString, key string) string {
	for _, item := range items {
		if item.Key == key {
			return item.Value
		}
	}
	return ""
}

func TestBundledLangPackSeedPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	scopes := []string{"android", "android_x", "ios", "macos", "tdesktop", "weba"}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DELETE FROM lang_pack_strings WHERE lang_pack = ANY($1::text[])", scopes)
		_, _ = pool.Exec(ctx, "DELETE FROM lang_packs WHERE lang_pack = ANY($1::text[])", scopes)
		for _, scope := range scopes {
			_, _ = pool.Exec(ctx, "DELETE FROM seed_states WHERE key LIKE $1", "langpack:v1:entry:"+scope+":%")
		}
		_, _ = pool.Exec(ctx, "DELETE FROM seed_states WHERE key = 'langpack:v1:catalog:langpack'")
		_, _ = pool.Exec(ctx, "DELETE FROM seed_states WHERE key = 'langpack:v2:catalog:langpack'")
	})

	service := applangpack.NewService(NewLangPackStore(pool))
	root := filepath.Join("..", "..", "..", "data", "langpack")
	started := time.Now()
	seeded, err := service.SeedDirectory(ctx, root)
	firstDuration := time.Since(started)
	if err != nil {
		t.Fatalf("seed bundled langpacks to postgres: %v", err)
	}
	if seeded < 50000 {
		t.Fatalf("seeded bundled strings = %d, want full catalog", seeded)
	}

	started = time.Now()
	seededAgain, err := service.SeedDirectory(ctx, root)
	secondDuration := time.Since(started)
	if err != nil || seededAgain != 0 {
		t.Fatalf("reconcile unchanged bundled langpacks = %d, %v", seededAgain, err)
	}
	var metadataCount, activeCount int64
	if err := pool.QueryRow(ctx, `
SELECT
  (SELECT COALESCE(sum(strings_count), 0) FROM lang_packs WHERE lang_pack = ANY($1::text[])),
  (SELECT count(*) FROM lang_pack_strings WHERE lang_pack = ANY($1::text[]) AND NOT deleted)
`, scopes).Scan(&metadataCount, &activeCount); err != nil {
		t.Fatalf("count seeded langpack rows: %v", err)
	}
	if metadataCount != activeCount {
		t.Fatalf("seeded strings_count = %d, active rows = %d", metadataCount, activeCount)
	}
	t.Logf("bundled langpack seed: first=%s unchanged=%s strings=%d", firstDuration, secondDuration, metadataCount)
}
