package files

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	"telesrv/internal/domain"
)

type GifSeedStats struct{ Imported, Skipped int }

type gifSeedCandidate struct {
	name, title, digest string
	data                []byte
}

// SeedGifs validates the whole bounded drop directory before importing. Same
// name/new content is a hard conflict; renamed identical content is skipped.
func (s *Service) SeedGifs(ctx context.Context, root string) (GifSeedStats, error) {
	var stats GifSeedStats
	if strings.TrimSpace(root) == "" {
		return stats, nil
	}
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return stats, nil
	}
	if err != nil {
		return stats, fmt.Errorf("read gif seed dir: %w", err)
	}
	if s.gifCatalog == nil {
		return stats, domain.ErrGifCatalogUnavailable
	}
	names := make([]string, 0, domain.MaxGifCatalogEntries+1)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(e.Name()))
		if ext == ".gif" || ext == ".mp4" {
			names = append(names, e.Name())
		}
	}
	if len(names) > domain.MaxGifCatalogEntries {
		return stats, domain.ErrGifCatalogFull
	}
	sort.Strings(names)
	existing, err := s.gifCatalog.ListGifCatalog(ctx, false)
	if err != nil {
		return stats, err
	}
	candidates := make([]gifSeedCandidate, 0, len(names))
	for _, name := range names {
		path := filepath.Join(root, name)
		info, err := os.Stat(path)
		if err != nil {
			return stats, err
		}
		if info.Size() <= 0 || info.Size() > domain.MaxGifCatalogUploadSize {
			return stats, fmt.Errorf("%s: %w", name, domain.ErrGifCatalogFileInvalid)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return stats, err
		}
		if _, ok := s.ValidateGifUpload(name, data); !ok {
			return stats, fmt.Errorf("%s: %w", name, domain.ErrGifCatalogFileInvalid)
		}
		sum := sha256.Sum256(data)
		digest := hex.EncodeToString(sum[:])
		byName, byDigest, err := s.gifCatalog.GifCatalogSeedMatches(ctx, name, digest)
		if err != nil {
			return stats, err
		}
		switch {
		case byName && !byDigest:
			return stats, fmt.Errorf("%s: %w", name, domain.ErrGifCatalogSourceChanged)
		case byName || byDigest:
			stats.Skipped++
		default:
			candidates = append(candidates, gifSeedCandidate{name: name, title: gifTitleFromFilename(name), digest: digest, data: data})
		}
	}
	if len(existing)+len(candidates) > domain.MaxGifCatalogEntries {
		return stats, domain.ErrGifCatalogFull
	}
	for _, candidate := range candidates {
		doc, err := s.AdminUploadGifMaterial(ctx, candidate.name, candidate.data)
		if err != nil {
			return stats, fmt.Errorf("import %s: %w", candidate.name, err)
		}
		if _, err := s.createGifCatalogEntry(ctx, candidate.title, doc.ID, candidate.name, candidate.name, candidate.digest); err != nil {
			return stats, fmt.Errorf("catalog %s: %w", candidate.name, err)
		}
		stats.Imported++
	}
	return stats, nil
}

func gifTitleFromFilename(name string) string {
	base := strings.TrimSuffix(name, filepath.Ext(name))
	base = strings.Map(func(r rune) rune {
		if r == '_' || r == '-' {
			return ' '
		}
		return r
	}, base)
	words := strings.Fields(base)
	for i, word := range words {
		r := []rune(word)
		if len(r) > 0 {
			r[0] = unicode.ToUpper(r[0])
		}
		words[i] = string(r)
	}
	title := strings.Join(words, " ")
	for len(title) > domain.MaxGifCatalogTitleLen {
		runes := []rune(title)
		title = string(runes[:len(runes)-1])
	}
	return title
}
