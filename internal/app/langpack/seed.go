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
	path    string
	meta    domain.LangPack
	size    int64
	modTime int64
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
		// 版本号（编码在文件名里）、文件大小和 mtime 都未变时复用上次的 hash。
		// 同大小的编辑也必须重新校验；旧清单缺少 size/mtime 时先算一次 SHA256，
		// 再补写完整 stat 元数据，不能把未知大小当作内容相同。
		if old, ok := previousByKey[key]; ok &&
			old.Version == candidate.meta.Version &&
			old.Size > 0 && old.Size == candidate.size &&
			old.ModTime != 0 && old.ModTime == candidate.modTime &&
			old.SourceHash != "" && old.ContentHash != "" && old.StringsCount > 0 {
			seed.Packs = append(seed.Packs, domain.LangPackSeedEntry{
				Pack:          candidate.meta,
				SourceHash:    old.SourceHash,
				ContentHash:   old.ContentHash,
				StringsCount:  old.StringsCount,
				Size:          candidate.size,
				ModTime:       candidate.modTime,
				ContentLoaded: false,
			})
			continue
		}
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
				Size:          candidate.size,
				ModTime:       candidate.modTime,
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
			Size:          candidate.size,
			ModTime:       candidate.modTime,
			ContentLoaded: true,
		})
	}

	// 目录内容没变时直接返回：ReconcileSeed 会为每个语言包做 2-3 次查询，远端 PG 上
	// 是数百次往返（启动期数秒），而结果只会全部命中相同 hash。要强制重放，删掉
	// seed_states 里的 langpack:v2:catalog:<catalog> 即可。
	if sameSeedCatalog(previous, seed) {
		// 旧清单或 touch 后的文件已在上面通过内容 hash 校验；只补写 stat 快照，
		// 下次启动即可复用 hash，不必再读整个文件。
		snapshot := seed.CatalogSnapshot()
		if !domain.SameSeedCatalogSnapshot(previous, snapshot) {
			if err := s.packs.PutSeedCatalog(ctx, snapshot); err != nil {
				return 0, fmt.Errorf("refresh langpack seed catalog: %w", err)
			}
		}
		return 0, nil
	}

	seeded, err := s.packs.ReconcileSeed(ctx, seed)
	if err != nil {
		return 0, fmt.Errorf("reconcile langpack seed: %w", err)
	}
	s.flushCaches()
	return seeded, nil
}

// sameSeedCatalog 判断导出目录算出的目录清单与上次落库的清单是否一致。比较按
// pack/code 建映射，不依赖历史清单的写入顺序（早期的记录并非按路径排序写入）。
func sameSeedCatalog(previous domain.LangPackSeedCatalog, seed domain.LangPackSeed) bool {
	if previous.Catalog != seed.Catalog ||
		len(previous.Scopes) != len(seed.Scopes) ||
		len(previous.Packs) != len(seed.Packs) {
		return false
	}
	scopes := make(map[string]struct{}, len(previous.Scopes))
	for _, scope := range previous.Scopes {
		scopes[scope] = struct{}{}
	}
	for _, scope := range seed.Scopes {
		if _, ok := scopes[scope]; !ok {
			return false
		}
	}
	packs := make(map[string]domain.LangPackSeedCatalogEntry, len(previous.Packs))
	for _, entry := range previous.Packs {
		packs[entry.LangPack+"\x00"+entry.LangCode] = entry
	}
	for _, pack := range seed.Packs {
		old, ok := packs[pack.Pack.LangPack+"\x00"+pack.Pack.LangCode]
		if !ok ||
			old.Version != pack.Pack.Version ||
			old.SourceHash != pack.SourceHash ||
			old.ContentHash != pack.ContentHash ||
			old.StringsCount != pack.StringsCount {
			return false
		}
	}
	return true
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
		// 与 ParseTDesktopFile 一样跟随符号链接，校验实际被读取文件的 stat。
		info, err := os.Stat(path)
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
		candidates[key] = seedCandidate{path: path, meta: meta, size: info.Size(), modTime: info.ModTime().UnixNano()}
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
