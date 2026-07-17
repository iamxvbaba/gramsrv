package langpack

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"telesrv/internal/domain"
)

type seedCandidate struct {
	path string
	meta domain.LangPack
}

// SeedDirectory 将导出的 .strings 文件清单原子对账到 LangPackStore。
// root 可直接指向 data/langpack，也可指向包含 .strings 的具体平台目录。
func (s *Service) SeedDirectory(ctx context.Context, root string) (int, error) {
	if s == nil || s.packs == nil || root == "" {
		return 0, nil
	}
	dir := filepath.Clean(root)
	if _, err := os.Stat(dir); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil
		}
		return 0, fmt.Errorf("stat langpack seed dir: %w", err)
	}

	candidates, scopes, err := scanSeedCandidates(dir)
	if err != nil {
		return 0, fmt.Errorf("scan langpack seed dir: %w", err)
	}
	keys := make([]string, 0, len(candidates))
	for key := range candidates {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	seed := domain.LangPackSeed{
		Catalog: seedCatalogID(dir),
		Scopes:  scopes,
		Packs:   make([]domain.LangPackSeedEntry, 0, len(keys)),
	}
	previous, err := s.packs.GetSeedCatalog(ctx, seed.Catalog)
	if err != nil {
		return 0, fmt.Errorf("get previous langpack seed catalog: %w", err)
	}
	previousByKey := make(map[string]domain.LangPackSeedCatalogEntry, len(previous.Packs))
	for _, entry := range previous.Packs {
		previousByKey[entry.LangPack+"\x00"+entry.LangCode] = entry
	}
	for _, key := range keys {
		candidate := candidates[key]
		sourceHash, err := fileSHA256(candidate.path)
		if err != nil {
			return 0, fmt.Errorf("hash langpack source %q: %w", candidate.path, err)
		}
		if old, ok := previousByKey[key]; ok &&
			old.Version == candidate.meta.Version &&
			old.SourceHash == sourceHash &&
			old.ContentHash != "" && old.StringsCount > 0 {
			seed.Packs = append(seed.Packs, domain.LangPackSeedEntry{
				Pack:          candidate.meta,
				SourceHash:    sourceHash,
				ContentHash:   old.ContentHash,
				StringsCount:  old.StringsCount,
				ContentLoaded: false,
			})
			continue
		}
		pack, err := ParseTDesktopFile(candidate.path)
		if err != nil {
			return 0, err
		}
		pack, err = prepareSeedPack(pack)
		if err != nil {
			return 0, fmt.Errorf("prepare langpack %q: %w", candidate.path, err)
		}
		hash, err := langPackContentHash(pack)
		if err != nil {
			return 0, fmt.Errorf("hash langpack %q: %w", candidate.path, err)
		}
		seed.Packs = append(seed.Packs, domain.LangPackSeedEntry{
			Pack:          pack,
			SourceHash:    sourceHash,
			ContentHash:   hash,
			StringsCount:  len(pack.Strings),
			ContentLoaded: true,
		})
	}

	seeded, err := s.packs.ReconcileSeed(ctx, seed)
	if err != nil {
		return 0, fmt.Errorf("reconcile langpack seed: %w", err)
	}
	s.flushCaches()
	return seeded, nil
}

func fileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func scanSeedCandidates(root string) (map[string]seedCandidate, []string, error) {
	candidates := make(map[string]seedCandidate)
	scopeSet := make(map[string]struct{})
	hasChildDirs := false
	hasFiles := false

	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if rel != "." && filepath.Dir(rel) == "." {
				hasChildDirs = true
				scope := normalizePack(entry.Name())
				if langPackNameRE.MatchString(scope) {
					scopeSet[scope] = struct{}{}
				}
			}
			return nil
		}
		if !strings.EqualFold(filepath.Ext(entry.Name()), ".strings") {
			return nil
		}
		hasFiles = true
		meta, err := packFromFilename(path)
		if err != nil {
			return err
		}
		if relDir := filepath.Dir(rel); relDir != "." {
			firstDir := strings.Split(relDir, string(filepath.Separator))[0]
			scope := normalizePack(firstDir)
			if !langPackNameRE.MatchString(scope) || scope != meta.LangPack {
				return fmt.Errorf("langpack file %q is under pack directory %q", path, firstDir)
			}
		}
		scopeSet[meta.LangPack] = struct{}{}
		key := meta.LangPack + "\x00" + meta.LangCode
		if previous, ok := candidates[key]; ok {
			switch {
			case meta.Version < previous.meta.Version:
				return nil
			case meta.Version == previous.meta.Version:
				return fmt.Errorf("duplicate langpack version %s/%s v%d in %q and %q", meta.LangPack, meta.LangCode, meta.Version, previous.path, path)
			}
		}
		candidates[key] = seedCandidate{path: path, meta: meta}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	if !hasChildDirs && !hasFiles {
		scope := normalizePack(filepath.Base(root))
		if langPackNameRE.MatchString(scope) {
			scopeSet[scope] = struct{}{}
		}
	}

	scopes := make([]string, 0, len(scopeSet))
	for scope := range scopeSet {
		scopes = append(scopes, scope)
	}
	sort.Strings(scopes)
	return candidates, scopes, nil
}

func prepareSeedPack(pack domain.LangPack) (domain.LangPack, error) {
	pack.LangPack = normalizePack(pack.LangPack)
	pack.LangCode = normalizeCode(pack.LangCode)
	pack.FromVersion = 0
	if len(pack.Strings) == 0 {
		return domain.LangPack{}, errors.New("language file contains no strings")
	}
	deduplicated := make([]domain.LangPackString, 0, len(pack.Strings))
	indexes := make(map[string]int, len(pack.Strings))
	for _, item := range pack.Strings {
		if item.Key == "" || utf8.RuneCountInString(item.Key) > 128 {
			return domain.LangPack{}, fmt.Errorf("invalid string key %q", item.Key)
		}
		if index, exists := indexes[item.Key]; exists {
			deduplicated[index] = item
			continue
		}
		indexes[item.Key] = len(deduplicated)
		deduplicated = append(deduplicated, item)
	}
	pack.Strings = deduplicated
	sort.Slice(pack.Strings, func(i, j int) bool {
		return pack.Strings[i].Key < pack.Strings[j].Key
	})
	return pack, nil
}

func langPackContentHash(pack domain.LangPack) (string, error) {
	encoded, err := json.Marshal(pack)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

func seedCatalogID(root string) string {
	base := normalizePack(filepath.Base(root))
	if langPackNameRE.MatchString(base) {
		return base
	}
	sum := sha256.Sum256([]byte(filepath.Clean(root)))
	return "path-" + hex.EncodeToString(sum[:8])
}
