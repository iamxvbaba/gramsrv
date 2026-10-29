package langpack

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

func TestSeedDirectoryRejectsChangedSources(t *testing.T) {
	for _, tc := range []struct {
		name           string
		content        string
		missingSize    bool
		missingModTime bool
		symlink        bool
	}{
		{name: "same_file_size", content: `"key" = "new";`},
		{name: "legacy_catalog_same_size", content: `"key" = "new";`, missingSize: true, missingModTime: true},
		{name: "legacy_catalog_changed_size", content: `"key" = "a much longer translation";`, missingSize: true, missingModTime: true},
		{name: "size_only_catalog", content: `"key" = "new";`, missingModTime: true},
		{name: "symlink_target", content: `"key" = "new";`, symlink: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			root := t.TempDir()
			path := filepath.Join(root, "tdesktop_en_v1.strings")
			sourcePath := path
			if tc.symlink {
				sourcePath = filepath.Join(t.TempDir(), "source.strings")
				if err := os.Symlink(sourcePath, path); err != nil {
					t.Skipf("symlink unavailable: %v", err)
				}
			}
			writeLangPackFixture(t, sourcePath, `"key" = "old";`)
			mtime := time.Unix(1700000000, 0)
			if err := os.Chtimes(sourcePath, mtime, mtime); err != nil {
				t.Fatal(err)
			}
			store := memory.NewLangPackStore()
			if _, err := NewService(store).SeedDirectory(ctx, root); err != nil {
				t.Fatal(err)
			}
			previous, err := store.GetSeedCatalog(ctx, seedCatalogID(root))
			if err != nil || len(previous.Packs) != 1 {
				t.Fatalf("initial catalog=%+v err=%v", previous, err)
			}
			if tc.missingSize {
				previous.Packs[0].Size = 0
			}
			if tc.missingModTime {
				previous.Packs[0].ModTime = 0
			}
			if err := store.PutSeedCatalog(ctx, previous); err != nil {
				t.Fatal(err)
			}

			writeLangPackFixture(t, sourcePath, tc.content)
			mtime = mtime.Add(time.Minute)
			if err := os.Chtimes(sourcePath, mtime, mtime); err != nil {
				t.Fatal(err)
			}
			if seeded, err := NewService(store).SeedDirectory(ctx, root); err == nil || !strings.Contains(err.Error(), "without version bump") {
				t.Fatalf("changed source accepted without version bump: seeded=%d err=%v", seeded, err)
			}
			pack, err := store.GetPack(ctx, "tdesktop", "en", 0)
			if err != nil || stringValue(pack.Strings, "key") != "old" {
				t.Fatalf("rejected mutation changed pack=%+v err=%v", pack, err)
			}
			current, err := store.GetSeedCatalog(ctx, seedCatalogID(root))
			if err != nil || !domain.SameSeedCatalogSnapshot(previous, current) {
				t.Fatalf("rejected mutation changed catalog=%+v err=%v", current, err)
			}
		})
	}
}

type countingSeedCatalogStore struct {
	*memory.LangPackStore
	reconcileCalls int
	catalogWrites  int
}

func (s *countingSeedCatalogStore) ReconcileSeed(ctx context.Context, seed domain.LangPackSeed) (int, error) {
	s.reconcileCalls++
	return s.LangPackStore.ReconcileSeed(ctx, seed)
}

func (s *countingSeedCatalogStore) PutSeedCatalog(ctx context.Context, catalog domain.LangPackSeedCatalog) error {
	s.catalogWrites++
	return s.LangPackStore.PutSeedCatalog(ctx, catalog)
}

func TestSeedDirectoryRefreshesStatMetadataWithoutReimport(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	path := filepath.Join(root, "tdesktop_en_v1.strings")
	writeLangPackFixture(t, path, `"key" = "old";`)
	store := &countingSeedCatalogStore{LangPackStore: memory.NewLangPackStore()}
	if seeded, err := NewService(store).SeedDirectory(ctx, root); err != nil || seeded != 1 {
		t.Fatalf("initial seed=%d err=%v", seeded, err)
	}
	previous, err := store.GetSeedCatalog(ctx, seedCatalogID(root))
	if err != nil || len(previous.Packs) != 1 {
		t.Fatalf("initial catalog=%+v err=%v", previous, err)
	}
	legacy := previous
	legacy.Packs = append([]domain.LangPackSeedCatalogEntry(nil), previous.Packs...)
	legacy.Packs[0].Size = 0
	legacy.Packs[0].ModTime = 0
	if err := store.LangPackStore.PutSeedCatalog(ctx, legacy); err != nil {
		t.Fatal(err)
	}

	for _, step := range []struct {
		name   string
		writes int
	}{
		{name: "upgrade_legacy_catalog", writes: 1},
		{name: "touch_source", writes: 2},
		{name: "unchanged_restart", writes: 2},
	} {
		t.Run(step.name, func(t *testing.T) {
			if step.name == "touch_source" {
				mtime := time.Unix(0, previous.Packs[0].ModTime).Add(time.Minute)
				if err := os.Chtimes(path, mtime, mtime); err != nil {
					t.Fatal(err)
				}
			}
			if seeded, err := NewService(store).SeedDirectory(ctx, root); err != nil || seeded != 0 {
				t.Fatalf("unchanged seed=%d err=%v", seeded, err)
			}
			if store.reconcileCalls != 1 || store.catalogWrites != step.writes {
				t.Fatalf("reconciles=%d catalog writes=%d, want 1/%d", store.reconcileCalls, store.catalogWrites, step.writes)
			}
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			catalog, err := store.GetSeedCatalog(ctx, seedCatalogID(root))
			if err != nil || len(catalog.Packs) != 1 {
				t.Fatalf("refreshed catalog=%+v err=%v", catalog, err)
			}
			entry := catalog.Packs[0]
			if entry.Size != info.Size() || entry.ModTime != info.ModTime().UnixNano() || entry.SourceHash != previous.Packs[0].SourceHash {
				t.Fatalf("refreshed entry=%+v, want current stat and unchanged source hash", entry)
			}
		})
	}
}
