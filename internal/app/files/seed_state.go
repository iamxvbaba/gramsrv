package files

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"sort"
)

const (
	seedEffectsStateKey        = "files.effects"
	seedEffectsStateVersion    = "effects-v2"
	seedAppearanceStateKey     = "files.appearance"
	seedAppearanceStateVersion = "appearance-v1"
)

func (s *Service) seedStateMatches(ctx context.Context, key, want string) (bool, error) {
	if want == "" {
		return false, nil
	}
	got, found, err := s.media.GetSeedState(ctx, key)
	if err != nil || !found {
		return false, err
	}
	return got == want, nil
}

func (s *Service) putSeedState(ctx context.Context, key, hash string) error {
	if hash == "" {
		return nil
	}
	return s.media.PutSeedState(ctx, key, hash)
}

func seedStateHash(write func(hash.Hash) error) (string, error) {
	h := sha256.New()
	if err := write(h); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func writeSeedStateHeader(h io.Writer, version string, dc int) {
	_, _ = fmt.Fprintf(h, "version=%s\ndc=%d\n", version, dc)
}

func writeSeedDirFingerprint(h io.Writer, dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	names := make([]string, 0, len(entries))
	byName := make(map[string]os.DirEntry, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		names = append(names, entry.Name())
		byName[entry.Name()] = entry
	}
	sort.Strings(names)
	for _, name := range names {
		info, err := byName[name].Info()
		if err != nil {
			return err
		}
		rel := filepath.ToSlash(name)
		_, _ = fmt.Fprintf(h, "file=%s\x00size=%d\x00mtime=%d\n", rel, info.Size(), info.ModTime().UnixNano())
	}
	return nil
}

func seedDocumentJSONLocationKeys(dj seedDocumentJSON, index seedDirIndex) []string {
	if dj.ID == 0 {
		return nil
	}
	keys := make([]string, 0, 1+len(dj.Thumbs))
	if _, ok := index.main[dj.ID]; ok {
		keys = append(keys, fmt.Sprintf("doc:%d", dj.ID))
	}
	for _, tj := range dj.Thumbs {
		ps, downloadable := seedPhotoSize(tj)
		if !downloadable || ps.Type == "" {
			continue
		}
		if _, ok := index.thumb[dj.ID][ps.Type]; ok {
			keys = append(keys, fmt.Sprintf("doc:%d:%s", dj.ID, ps.Type))
		}
	}
	if seedDocumentJSONNeedsSyntheticTGStickerPreviewThumb(dj) {
		keys = append(keys, fmt.Sprintf("doc:%d:%s", dj.ID, seedSyntheticDocumentThumbType))
	}
	return keys
}

func seedDocumentJSONNeedsSyntheticTGStickerPreviewThumb(dj seedDocumentJSON) bool {
	return dj.MimeType == "application/x-tgsticker" && len(dj.Thumbs) == 0
}

func (s *Service) seedDocumentJSONsReady(ctx context.Context, docs []seedDocumentJSON, index seedDirIndex) (bool, error) {
	expected := make(map[int64]seedDocumentJSON, len(docs))
	ids := make([]int64, 0, len(docs))
	locationKeys := make([]string, 0, len(docs))
	seenLocationKeys := make(map[string]struct{}, len(docs))
	for _, dj := range docs {
		if dj.ID == 0 {
			continue
		}
		if _, ok := expected[dj.ID]; !ok {
			expected[dj.ID] = dj
			ids = append(ids, dj.ID)
		}
		for _, key := range seedDocumentJSONLocationKeys(dj, index) {
			if _, ok := seenLocationKeys[key]; ok {
				continue
			}
			seenLocationKeys[key] = struct{}{}
			locationKeys = append(locationKeys, key)
		}
	}
	if len(ids) == 0 {
		return true, nil
	}
	stored, err := s.media.GetDocuments(ctx, ids)
	if err != nil {
		return false, err
	}
	if len(stored) < len(expected) {
		return false, nil
	}
	for _, doc := range stored {
		dj, ok := expected[doc.ID]
		if !ok {
			continue
		}
		if doc.DCID != s.dc || doc.MimeType != dj.MimeType || doc.Size != dj.Size {
			return false, nil
		}
		delete(expected, doc.ID)
	}
	if len(expected) > 0 {
		return false, nil
	}
	if len(locationKeys) == 0 {
		return true, nil
	}
	blobs, err := s.media.GetFileBlobs(ctx, locationKeys)
	if err != nil {
		return false, err
	}
	for _, key := range locationKeys {
		if _, ok := blobs[key]; !ok {
			return false, nil
		}
	}
	return true, nil
}
