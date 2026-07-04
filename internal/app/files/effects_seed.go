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

// telegram_effects_export/effects.json 的解析结构。effects 引用文档 id,文档全量元数据
// 在 documents[] 里(与 messages.availableEffects 同构),blob 在 documents/<docid>.<ext>。
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

// seedEffects 从 telegram_effects_export 导入消息特效。特效元数据每次启动都从 JSON
// 重建进内存；document/blob 只有 catalog hash 变化或持久化资源不完整时才重导。
func (s *Service) seedEffects(ctx context.Context, root string, stats *SeedStats) error {
	dir := filepath.Join(root, "telegram_effects_export")
	raw, err := os.ReadFile(filepath.Join(dir, "effects.json"))
	if err != nil {
		return nil // 无 effects 资源 → 跳过
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
	s.effectsHash = effectsCatalogHash(effects)
	stats.Effects = len(effects)

	stateHash, err := s.seedEffectsStateHash(raw, docsDir)
	if err != nil {
		return err
	}
	ready, err := s.seedDocumentJSONsReady(ctx, requiredDocs, index)
	if err != nil {
		return err
	}
	matched, err := s.seedStateMatches(ctx, seedEffectsStateKey, stateHash)
	if err != nil {
		return err
	}
	if matched && ready {
		return nil
	}

	// 多个 effect 常共享同一文档(static icon 尤甚):每个唯一源文档只导一次。
	for _, dj := range requiredDocs {
		if _, err := s.importDocument(ctx, dj, docsDir, index, stats); err != nil {
			return err
		}
	}
	return s.putSeedState(ctx, seedEffectsStateKey, stateHash)
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

// AvailableEffects 返回 seed 进内存的消息特效目录与其内容 hash（全局静态;hash 在 seed 时
// 算一次,handler 直接比对返回 NotModified,无需每次 RPC 重算）。
func (s *Service) AvailableEffects(ctx context.Context) ([]domain.AvailableEffect, int, error) {
	return s.effects, s.effectsHash, nil
}

// effectsCatalogHash 由 effect 字段算稳定正整数 hash（FNV-1a）。内容变则 hash 变,
// 客户端发旧 hash 即不命中→重取。
func effectsCatalogHash(effects []domain.AvailableEffect) int {
	if len(effects) == 0 {
		return 0
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
	return int(h.Sum64() & 0x7fffffff)
}
