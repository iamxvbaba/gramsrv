package files

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"hash"
	"hash/fnv"
	"os"
	"path/filepath"

	"telesrv/internal/domain"
)

// Parse structure for telegram_effects_export/effects.json. effects reference document ids,
// the full document metadata is in documents[] (isomorphic with messages.availableEffects),
// and blobs are in documents/<docid>.<ext>.
type seedEffectJSON struct {
	ID                int64  `json:"id"`
	Emoticon          string `json:"emoticon"`
	StaticIconID      int64  `json:"static_icon_id"`
	EffectStickerID   int64  `json:"effect_sticker_id"`
	EffectAnimationID int64  `json:"effect_animation_id"`
	PremiumRequired   bool   `json:"premium_required"`
}

type seedEffectsFileJSON struct {
	Result struct {
		Effects   []seedEffectJSON   `json:"effects"`
		Documents []seedDocumentJSON `json:"documents"`
	} `json:"result"`
}

// seedEffects imports message effects from telegram_effects_export. Effect metadata is rebuilt
// into memory from JSON on every startup; documents/blobs are re-imported only when the catalog
// hash changes or the persisted resources are incomplete.
func (s *Service) seedEffects(ctx context.Context, root string, stats *SeedStats) error {
	dir := filepath.Join(root, "telegram_effects_export")
	raw, err := os.ReadFile(filepath.Join(dir, "effects.json"))
	if err != nil {
		return nil // no effects resources → skip
	}
	var parsed seedEffectsFileJSON
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return fmt.Errorf("parse effects.json: %w", err)
	}
	docsDir := filepath.Join(dir, "documents")
	index, err := scanSeedDir(docsDir)
	if err != nil {
		return err
	}
	effects, requiredDocs := seedEffectsCatalog(parsed)
	s.effects = effects
	stats.Effects = len(effects)

	stateHash, err := s.seedEffectsStateHash(raw, docsDir)
	if err != nil {
		return err
	}
	storedDocs, ready, err := s.seedDocumentJSONsReady(ctx, requiredDocs, index)
	if err != nil {
		return err
	}
	matched, err := s.seedStateMatches(ctx, seedEffectsStateKey, stateHash)
	if err != nil {
		return err
	}
	ids := make([]int64, 0, len(requiredDocs))
	for _, doc := range requiredDocs {
		ids = append(ids, doc.ID)
	}
	if !matched || !ready {
		for _, dj := range requiredDocs {
			if _, err := s.importDocument(ctx, dj, docsDir, index, domain.StickerSetRef{}, stats); err != nil {
				return err
			}
		}
		if err := s.putSeedState(ctx, seedEffectsStateKey, stateHash); err != nil {
			return err
		}
		// The import may have rewritten documents/thumbnails, so re-read once before computing the hash.
		storedDocs, err = s.media.GetDocuments(ctx, ids)
		if err != nil {
			return err
		}
	}
	if len(storedDocs) != len(ids) {
		return fmt.Errorf("effects catalog documents incomplete")
	}
	s.effectsHash, err = effectsCatalogHash(effects, orderDocuments(storedDocs, ids))
	return err
}

func seedEffectsCatalog(parsed seedEffectsFileJSON) ([]domain.AvailableEffect, []seedDocumentJSON) {
	docByID := make(map[int64]seedDocumentJSON, len(parsed.Result.Documents))
	for _, d := range parsed.Result.Documents {
		docByID[d.ID] = d
	}
	required := make(map[int64]struct{}, len(docByID))
	documentID := func(id int64) int64 {
		if id == 0 {
			return 0
		}
		if _, ok := docByID[id]; !ok {
			return 0
		}
		required[id] = struct{}{}
		return id
	}
	effects := make([]domain.AvailableEffect, 0, len(parsed.Result.Effects))
	for i, ej := range parsed.Result.Effects {
		if ej.ID == 0 || ej.EffectStickerID == 0 {
			continue
		}
		staticID := documentID(ej.StaticIconID)
		stickerID := documentID(ej.EffectStickerID)
		if stickerID == 0 {
			continue
		}
		animID := documentID(ej.EffectAnimationID)
		effects = append(effects, domain.AvailableEffect{
			ID:                ej.ID,
			Emoticon:          ej.Emoticon,
			StaticIconID:      staticID,
			EffectStickerID:   stickerID,
			EffectAnimationID: animID,
			PremiumRequired:   ej.PremiumRequired,
			Order:             i,
		})
	}
	docs := make([]seedDocumentJSON, 0, len(required))
	for _, d := range parsed.Result.Documents {
		if _, ok := required[d.ID]; ok {
			docs = append(docs, d)
		}
	}
	return effects, docs
}

func (s *Service) seedEffectsStateHash(raw []byte, docsDir string) (string, error) {
	return seedStateHash(func(h hash.Hash) error {
		writeSeedStateHeader(h, seedEffectsStateVersion, s.dc)
		_, _ = h.Write(raw)
		_, _ = h.Write([]byte{'\n'})
		return writeSeedDirFingerprint(h, docsDir)
	})
}

// AvailableEffects returns the message effects catalog seeded into memory and its content hash
// (globally static; the hash is computed once at seed time, and the handler compares it directly
// to return NotModified, with no need to recompute per RPC).
func (s *Service) AvailableEffects(ctx context.Context) ([]domain.AvailableEffect, int, error) {
	return s.effects, s.effectsHash, nil
}

// effectsCatalogHash computes a stable positive-integer hash from the effect fields (FNV-1a).
// When content changes the hash changes, so a client sending an old hash misses → refetch.
func effectsCatalogHash(effects []domain.AvailableEffect, docs []domain.Document) (int, error) {
	if len(effects) == 0 {
		return 0, nil
	}
	h := fnv.New64a()
	var buf [8]byte
	put := func(v int64) {
		binary.LittleEndian.PutUint64(buf[:], uint64(v))
		_, _ = h.Write(buf[:])
	}
	for _, e := range effects {
		put(e.ID)
		_, _ = h.Write([]byte(e.Emoticon))
		put(e.StaticIconID)
		put(e.EffectStickerID)
		put(e.EffectAnimationID)
		if e.PremiumRequired {
			put(1)
		} else {
			put(0)
		}
	}
	// Metadata repairs must invalidate cached effect documents too, otherwise
	// TDLib can overwrite a refreshed set credential with a stale effect reference.
	// Thumbnail bytes are fed into the hash with their length directly, avoiding JSON/base64
	// overhead. The effective size of a cached thumb may be determined solely by len(Bytes), and
	// path/stripped Bytes are delivered straight to the client, so they cannot be left out of the hash.
	trimmed := make([]domain.Document, len(docs))
	for i, doc := range docs {
		thumbs := append([]domain.PhotoSize(nil), doc.Thumbs...)
		for j := range thumbs {
			put(int64(len(thumbs[j].Bytes)))
			_, _ = h.Write(thumbs[j].Bytes)
			thumbs[j].Bytes = nil
		}
		doc.Thumbs = thumbs
		trimmed[i] = doc
	}
	if err := json.NewEncoder(h).Encode(trimmed); err != nil {
		return 0, err
	}
	return int(h.Sum64() & 0x7fffffff), nil
}
