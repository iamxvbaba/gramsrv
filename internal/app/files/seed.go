package files

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"go.uber.org/zap"

	"telesrv/internal/domain"
)

// This file implements importing media seeds from externally exported reaction / sticker resource
// directories: JSON metadata (exported document id/access_hash/file_reference/attributes/thumbs) goes
// into the documents / sticker_sets / available_reactions tables, and binary .tgs/.webp/thumbnails go
// to the blob backend. dc_id is uniformly rewritten to this server's DC so clients download from it.
// The import is idempotent (an existing item is skipped).

// SeedStats reports the result of one seed import.
type SeedStats struct {
	Reactions   int
	StickerSets int
	Effects     int
	Documents   int
	Blobs       int
	Skipped     bool
}

// SeedMedia imports reaction and sticker resources from the export root. maxRegularSets<=0 means unlimited.
func (s *Service) SeedMedia(ctx context.Context, root string, maxRegularSets int) (SeedStats, error) {
	var stats SeedStats
	if root == "" {
		stats.Skipped = true
		return stats, nil
	}
	if _, err := os.Stat(root); err != nil {
		// Directory missing: skip rather than fail (a dev machine may not have the resources). But WARN
		// explicitly — otherwise "configured seed directory missing → stickers/reactions silently not
		// imported" would be buried (especially hidden when the DB already has old data, showing up as
		// clients repeatedly pulling unseeded sets). Configure TELESRV_STICKER_SEED_DIR to point at the real export directory.
		if s.log != nil {
			s.log.Warn("sticker/reaction seed directory does not exist; skipping media seed import (configure TELESRV_STICKER_SEED_DIR)",
				zap.String("dir", root), zap.Error(err))
		}
		stats.Skipped = true
		return stats, nil
	}

	phaseStarted := time.Now()
	phaseBefore := stats
	// Sticker sets (default system sets + regular sets + emoji sets): always scan the export directory
	// to **incrementally** pick up new set directories — importStickerSet skips existing sets whose
	// content (hash) is unchanged, importing only new/changed sets. This way, dropping new sets into a
	// deployed (non-empty store) data/sticker-seed and restarting takes effect without wiping the DB and
	// reseeding. Only when the export directory's own documents/thumbnails are missing or the old preview
	// metadata has not been migrated does force=true trigger a full re-import.
	forceSticker := false
	previewState := fmt.Sprintf("%s:dc=%d", seedStickerPreviewStateVersion, s.dc)
	if n, err := s.media.CountStickerSets(ctx); err != nil {
		return stats, err
	} else if n > 0 {
		migrated, err := s.seedStateMatches(ctx, seedStickerPreviewStateKey, previewState)
		if err != nil {
			return stats, err
		}
		forceSticker = !migrated
	}
	// Steady-state fast path: when the directory fingerprint (only stat'ing set_info.json) is unchanged
	// and the last recorded document thumbnails are still serviceable, skip the JSON parsing of the whole
	// export tree and the per-set DB queries; dropping in a new set or re-exporting changes the fingerprint.
	skipSticker := false
	if !forceSticker {
		if idx, ok, err := s.loadStickerSeedIndex(ctx); err != nil {
			return stats, err
		} else if ok && idx.Version == seedStickerIndexVersion && idx.DC == s.dc &&
			idx.MaxRegularSets == max(0, maxRegularSets) && len(idx.Sets) > 0 && len(idx.Documents) > 0 {
			fingerprint, err := stickerSeedFingerprint(root)
			if err != nil {
				return stats, err
			}
			if fingerprint == idx.Fingerprint {
				stale, err := s.documentsNeedSeedRepair(ctx, idx.Documents)
				if err != nil {
					return stats, err
				}
				present, err := s.media.CountStickerSetsByIDs(ctx, idx.Sets)
				if err != nil {
					return stats, err
				}
				skipSticker = !stale && present == len(idx.Sets)
			}
		}
	}
	if skipSticker && s.log != nil {
		s.log.Info("sticker seed directory unchanged; skipping export directory scan and per-set queries")
	}
	if !skipSticker {
		if err := s.seedStickerSets(ctx, root, maxRegularSets, forceSticker, &stats); err != nil {
			return stats, fmt.Errorf("seed sticker sets: %w", err)
		}
	}
	if err := s.putSeedState(ctx, seedStickerPreviewStateKey, previewState); err != nil {
		return stats, fmt.Errorf("record sticker preview seed state: %w", err)
	}
	s.logSeedPhase("sticker_sets", phaseStarted, phaseBefore, stats)

	phaseStarted = time.Now()
	phaseBefore = stats
	// reactions
	if n, err := s.media.CountAvailableReactions(ctx); err != nil {
		return stats, err
	} else if n == 0 {
		if err := s.seedReactions(ctx, root, &stats); err != nil {
			return stats, fmt.Errorf("seed reactions: %w", err)
		}
	} else if incomplete, err := s.availableReactionSeedNeedsRepair(ctx); err != nil {
		return stats, err
	} else if incomplete {
		if err := s.seedReactions(ctx, root, &stats); err != nil {
			return stats, fmt.Errorf("repair reactions: %w", err)
		}
	}
	s.logSeedPhase("reactions", phaseStarted, phaseBefore, stats)

	phaseStarted = time.Now()
	phaseBefore = stats
	// Message effects: a globally static catalog, rebuilding the in-memory s.effects on every startup (document import is idempotent).
	if err := s.seedEffects(ctx, root, &stats); err != nil {
		return stats, fmt.Errorf("seed effects: %w", err)
	}
	s.logSeedPhase("effects", phaseStarted, phaseBefore, stats)

	if stats.Reactions == 0 && stats.StickerSets == 0 && stats.Effects == 0 {
		stats.Skipped = true
	}
	return stats, nil
}

func (s *Service) logSeedPhase(phase string, started time.Time, before, after SeedStats) {
	if s.log == nil {
		return
	}
	s.log.Info("media seed phase complete",
		zap.String("phase", phase),
		zap.Duration("elapsed", time.Since(started)),
		zap.Int("reactions", after.Reactions-before.Reactions),
		zap.Int("sticker_sets", after.StickerSets-before.StickerSets),
		zap.Int("effects", after.Effects-before.Effects),
		zap.Int("documents", after.Documents-before.Documents),
		zap.Int("blobs", after.Blobs-before.Blobs),
	)
}

// ---- reactions ----

func (s *Service) seedReactions(ctx context.Context, root string, stats *SeedStats) error {
	reactionsDir := filepath.Join(root, "telegram_reactions_export", "reactions")
	rawPath := filepath.Join(root, "telegram_reactions_export", "global_json", "available_reactions_raw.json")
	raw, err := os.ReadFile(rawPath)
	if err != nil {
		return nil // no reaction resources, skip
	}
	var parsed struct {
		Result struct {
			Reactions []seedReactionJSON `json:"reactions"`
		} `json:"result"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return fmt.Errorf("parse available_reactions_raw.json: %w", err)
	}
	index, err := scanSeedDir(reactionsDir)
	if err != nil {
		return err
	}
	for i, rj := range parsed.Result.Reactions {
		ar := domain.AvailableReaction{
			Reaction: rj.Reaction,
			Title:    rj.Title,
			Inactive: rj.Inactive,
			Premium:  rj.Premium,
			Order:    i,
		}
		set := func(dst *int64, d *seedDocumentJSON) error {
			if d == nil || d.ID == 0 {
				return nil
			}
			doc, err := s.importDocument(ctx, *d, reactionsDir, index, domain.StickerSetRef{}, stats)
			if err != nil {
				return err
			}
			*dst = doc.ID
			return nil
		}
		if err := set(&ar.StaticIconID, rj.StaticIcon); err != nil {
			return err
		}
		if err := set(&ar.AppearAnimationID, rj.AppearAnimation); err != nil {
			return err
		}
		if err := set(&ar.SelectAnimationID, rj.SelectAnimation); err != nil {
			return err
		}
		if err := set(&ar.ActivateAnimationID, rj.ActivateAnimation); err != nil {
			return err
		}
		if err := set(&ar.EffectAnimationID, rj.EffectAnimation); err != nil {
			return err
		}
		if err := set(&ar.AroundAnimationID, rj.AroundAnimation); err != nil {
			return err
		}
		if err := set(&ar.CenterIconID, rj.CenterIcon); err != nil {
			return err
		}
		if err := s.media.PutAvailableReaction(ctx, ar); err != nil {
			return err
		}
		stats.Reactions++
	}
	return nil
}

func (s *Service) availableReactionSeedNeedsRepair(ctx context.Context) (bool, error) {
	reactions, err := s.media.ListAvailableReactions(ctx)
	if err != nil {
		return false, err
	}
	var docIDs []int64
	locationKeys := make([]string, 0, len(reactions)*4)
	seenKeys := make(map[string]struct{}, len(reactions)*4)
	for _, r := range reactions {
		for _, id := range r.DocumentIDs() {
			if id == 0 {
				continue
			}
			docIDs = append(docIDs, id)
			key := fmt.Sprintf("doc:%d", id)
			if _, ok := seenKeys[key]; ok {
				continue
			}
			seenKeys[key] = struct{}{}
			locationKeys = append(locationKeys, key)
		}
	}
	// One ANY query replaces the old per-item GetFileBlob: there are hundreds of reaction icons/animations,
	// and querying them one by one costs 6-7 seconds on a remote PG.
	if len(locationKeys) > 0 {
		blobs, err := s.media.GetFileBlobs(ctx, locationKeys)
		if err != nil {
			return false, err
		}
		for _, key := range locationKeys {
			if _, ok := blobs[key]; !ok {
				return true, nil
			}
		}
	}
	if stale, err := s.documentsNeedInlineCachedThumbs(ctx, docIDs); err != nil || stale {
		return stale, err
	}
	return false, nil
}

// ---- sticker sets ----

// seedStickerSetEntry is a sticker set after parsing one export directory. The whole export tree is
// read/parsed once: document integrity checks and the import both reuse it, no longer re-reading
// set_info.json for every path.
type seedStickerSetEntry struct {
	dir       string
	systemKey string
	info      seedStickerSetInfoJSON
}

type seedStickerSetInfoJSON struct {
	InputSetType string                   `json:"input_set_type"`
	Result       seedStickerSetResultJSON `json:"result"`
}

func (s *Service) seedStickerSets(ctx context.Context, root string, maxRegular int, force bool, stats *SeedStats) error {
	entries, err := readSeedStickerSetEntries(root, maxRegular)
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		return nil
	}
	// Validate only the export directory's own documents: thumbnails in user-created sticker sets are not
	// the seed's responsibility, and counting them in the corruption check would force a full re-import of
	// all seed sets on every startup.
	if !force {
		ids := make([]int64, 0, len(entries)*8)
		for _, entry := range entries {
			for _, dj := range entry.info.Result.Documents {
				ids = append(ids, dj.ID)
			}
		}
		stale, err := s.documentsNeedSeedRepair(ctx, ids)
		if err != nil {
			return err
		}
		force = stale
	}
	// Fetch existing sets in one go (4 queries), replacing per-set GetStickerSetByID (65 round-trips).
	existing, err := s.listStickerSetsByID(ctx)
	if err != nil {
		return err
	}
	for order, entry := range entries {
		if err := s.importStickerSet(ctx, entry, order, force, existing, stats); err != nil {
			return fmt.Errorf("import sticker set %s: %w", filepath.Base(entry.dir), err)
		}
	}
	return s.saveStickerSeedIndex(ctx, root, maxRegular, entries)
}

const (
	seedStickerIndexKey     = "files.sticker_seed_index"
	seedStickerIndexVersion = 3
)

// seedStickerSeedIndex records the export tree's stat fingerprint and all its document ids: at steady
// state startup set_info.json need not be parsed, and documentsNeedSeedRepair can still verify whether
// those documents' thumbnails/blobs are present.
type seedStickerSeedIndex struct {
	Version        int     `json:"version"`
	DC             int     `json:"dc"`
	MaxRegularSets int     `json:"max_regular_sets"` // <=0 is stored as 0 uniformly; raising the limit requires a rescan.
	Fingerprint    string  `json:"fingerprint"`
	Sets           []int64 `json:"sets"`
	Documents      []int64 `json:"documents"`
}

func (s *Service) loadStickerSeedIndex(ctx context.Context) (seedStickerSeedIndex, bool, error) {
	raw, found, err := s.media.GetSeedState(ctx, seedStickerIndexKey)
	if err != nil || !found || raw == "" {
		return seedStickerSeedIndex{}, false, err
	}
	var idx seedStickerSeedIndex
	if err := json.Unmarshal([]byte(raw), &idx); err != nil {
		// Old format or corrupted: treat as no index and do a full scan rewrite.
		return seedStickerSeedIndex{}, false, nil
	}
	return idx, true, nil
}

func (s *Service) saveStickerSeedIndex(ctx context.Context, root string, maxRegular int, entries []seedStickerSetEntry) error {
	fingerprint, err := stickerSeedFingerprint(root)
	if err != nil {
		return err
	}
	seen := make(map[int64]struct{}, len(entries)*8)
	ids := make([]int64, 0, len(entries)*8)
	// The same set may appear in two export directories at once (e.g. a Topics emoji set); deduplicate by
	// id before recording, otherwise the "set still exists" count on the next startup never matches and
	// the fast path is skipped for nothing.
	setSeen := make(map[int64]struct{}, len(entries))
	setIDs := make([]int64, 0, len(entries))
	for _, entry := range entries {
		if id := entry.info.Result.Set.ID; id != 0 {
			if _, ok := setSeen[id]; !ok {
				setSeen[id] = struct{}{}
				setIDs = append(setIDs, id)
			}
		}
		for _, dj := range entry.info.Result.Documents {
			if dj.ID == 0 {
				continue
			}
			if _, ok := seen[dj.ID]; ok {
				continue
			}
			seen[dj.ID] = struct{}{}
			ids = append(ids, dj.ID)
		}
	}
	encoded, err := json.Marshal(seedStickerSeedIndex{
		Version:        seedStickerIndexVersion,
		DC:             s.dc,
		MaxRegularSets: max(0, maxRegular),
		Fingerprint:    fingerprint,
		Sets:           setIDs,
		Documents:      ids,
	})
	if err != nil {
		return err
	}
	return s.putSeedState(ctx, seedStickerIndexKey, string(encoded))
}

// stickerSeedFingerprint only stats set_info.json under the three export directories (without decoding
// the content): a new set or a re-export changes the size/mtime, triggering a full scan; a missing
// directory is treated as empty.
func stickerSeedFingerprint(root string) (string, error) {
	h := sha256.New()
	for _, base := range []string{
		"telegram_default_stickers_export",
		"telegram_emoji_export",
		"telegram_stickers_export",
	} {
		dir := filepath.Join(root, base)
		names, err := seedSubdirNames(dir)
		if err != nil {
			return "", err
		}
		// seedSubdirNames is sorted; only stat <set>/set_info.json without descending into documents/
		// (which has tens of thousands of small files, where walking dirents on a cold disk costs hundreds of milliseconds).
		for _, name := range names {
			info, err := os.Stat(filepath.Join(dir, name, "set_info.json"))
			if err != nil {
				if os.IsNotExist(err) {
					continue
				}
				return "", err
			}
			_, _ = fmt.Fprintf(h, "%s/%s\x00%d\x00%d\n", base, name, info.Size(), info.ModTime().UnixNano())
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// readSeedStickerSetEntries collects export directories in the order default → emoji → regular, with
// regular limited by maxRegular. The order is the sort_order.
func readSeedStickerSetEntries(root string, maxRegular int) ([]seedStickerSetEntry, error) {
	var entries []seedStickerSetEntry
	appendDir := func(dir, systemKey string) error {
		infoPath := filepath.Join(dir, "set_info.json")
		raw, err := os.ReadFile(infoPath)
		if err != nil {
			return nil // this directory has no set_info.json → skip
		}
		var info seedStickerSetInfoJSON
		if err := json.Unmarshal(raw, &info); err != nil {
			return fmt.Errorf("parse %s: %w", infoPath, err)
		}
		if info.Result.Set.ID == 0 {
			return nil
		}
		if systemKey == "" {
			systemKey = systemKeyForInputSetType(info.InputSetType)
		}
		entries = append(entries, seedStickerSetEntry{dir: dir, systemKey: systemKey, info: info})
		return nil
	}
	appendKind := func(base string, defaultSets bool, limit int) error {
		names, err := seedSubdirNames(base)
		if err != nil {
			return err
		}
		for i, name := range names {
			if limit > 0 && i >= limit {
				break
			}
			// Default system sets: directory name → system_key; other sets are inferred from input_set_type in appendDir.
			systemKey := ""
			if defaultSets {
				systemKey = systemKeyForDefaultSet(name)
			}
			if err := appendDir(filepath.Join(base, name), systemKey); err != nil {
				return err
			}
		}
		return nil
	}
	if err := appendKind(filepath.Join(root, "telegram_default_stickers_export"), true, 0); err != nil {
		return nil, err
	}
	// custom-emoji sets (telegram_emoji_export/<set>/): not limited by maxRegular, classified as
	// StickerSetKindEmoji by the set_info emojis flag (delivered via getEmojiStickers/getFeaturedEmojiStickers).
	if err := appendKind(filepath.Join(root, "telegram_emoji_export"), false, 0); err != nil {
		return nil, err
	}
	// Regular sticker sets.
	if err := appendKind(filepath.Join(root, "telegram_stickers_export"), false, maxRegular); err != nil {
		return nil, err
	}
	return entries, nil
}

func seedSubdirNames(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	return names, nil
}

// listStickerSetsByID fetches all non-deleted sets in one go.
func (s *Service) listStickerSetsByID(ctx context.Context) (map[int64]domain.StickerSet, error) {
	out := make(map[int64]domain.StickerSet, 64)
	for _, kind := range []domain.StickerSetKind{
		domain.StickerSetKindStickers,
		domain.StickerSetKindEmoji,
		domain.StickerSetKindMasks,
		domain.StickerSetKindSystem,
	} {
		sets, err := s.media.ListStickerSets(ctx, kind)
		if err != nil {
			return nil, err
		}
		for _, set := range sets {
			out[set.ID] = set
		}
	}
	return out, nil
}

func (s *Service) importStickerSet(ctx context.Context, entry seedStickerSetEntry, order int, force bool, existingSets map[int64]domain.StickerSet, stats *SeedStats) error {
	info := entry.info
	sj := info.Result.Set
	systemKey := entry.systemKey
	kind := stickerSetKind(sj, systemKey)
	// Incremental seed: when a set already exists and its content hash is unchanged, skip it (without
	// re-reading documents or re-uploading blobs). force forces a re-import (the inline thumbnail cache
	// repair path). This lets seedStickerSets safely rescan on every startup over a non-empty store,
	// importing only new/changed sets; but system set classification is determined by the export
	// metadata, so records with the same hash but a wrong system_key/kind in an old DB must be rewritten,
	// so that the same resource set cannot be fetched via a constructor.
	existing, found := existingSets[sj.ID]
	if !found {
		var err error
		existing, found, err = s.media.GetStickerSetByID(ctx, sj.ID)
		if err != nil {
			return err
		}
	}
	if found {
		// Source exports may come from different accounts. Once published locally,
		// a set's credential remains stable across imports from those accounts.
		sj.AccessHash = existing.AccessHash
		if !force && existing.Hash == sj.Hash && existing.SystemKey == systemKey && existing.Kind == kind {
			return nil
		}
	}
	stickersDir := filepath.Join(entry.dir, "stickers")
	index, err := scanSeedDir(stickersDir)
	if err != nil {
		return err
	}

	docIDs := make([]int64, 0, len(info.Result.Documents))
	docs := make([]domain.Document, 0, len(info.Result.Documents))
	for _, dj := range info.Result.Documents {
		doc, err := s.importDocument(ctx, dj, stickersDir, index, domain.StickerSetRef{ID: sj.ID, AccessHash: sj.AccessHash}, stats)
		if err != nil {
			return err
		}
		if doc.ID != 0 {
			docIDs = append(docIDs, doc.ID)
			docs = append(docs, doc)
		}
	}

	set := domain.StickerSet{
		ID:              sj.ID,
		AccessHash:      sj.AccessHash,
		ShortName:       sj.ShortName,
		Title:           sj.Title,
		Count:           sj.Count,
		Hash:            sj.Hash,
		Kind:            kind,
		Official:        sj.Official,
		Animated:        true, // exported resources are all .tgs animated stickers
		Emojis:          sj.Emojis,
		Masks:           sj.Masks,
		Archived:        sj.Archived,
		Installed:       seedStickerSetInstalled(kind),
		ThumbDocumentID: derefInt64(sj.ThumbDocumentID),
		Thumbs:          seedStickerSetPhotoSizes(sj.Thumbs),
		ThumbDCID:       s.dc,
		ThumbVersion:    sj.ThumbVersion,
		DocumentIDs:     docIDs,
		Packs:           seedStickerPacks(sj.Packs, info.Result.Packs),
		SortOrder:       order,
		SystemKey:       systemKey,
	}
	if set.Count == 0 {
		set.Count = len(docIDs)
	}
	if err := s.media.PutStickerSet(ctx, set); err != nil {
		return err
	}
	// The same id may appear in a later export directory; the skip decision must see the version just imported this round.
	existingSets[set.ID] = set
	s.stickerSetCache.put(set, docs)
	stats.StickerSets++
	return nil
}

// ---- Single document import ----

func (s *Service) importDocument(ctx context.Context, dj seedDocumentJSON, binDir string, index seedDirIndex, owner domain.StickerSetRef, stats *SeedStats) (domain.Document, error) {
	if dj.ID == 0 {
		return domain.Document{}, nil
	}
	existing, existingFound, err := s.media.GetDocument(ctx, dj.ID)
	if err != nil {
		return domain.Document{}, err
	}
	ref, _ := hex.DecodeString(dj.FileReference)
	doc := domain.Document{
		ID:            dj.ID,
		AccessHash:    dj.AccessHash,
		FileReference: ref,
		Date:          parseSeedDate(dj.Date),
		MimeType:      dj.MimeType,
		Size:          dj.Size,
		DCID:          s.dc,
		Attributes:    seedDocumentAttributes(dj.Attributes),
	}

	if err := s.bindSeedStickerReferences(ctx, doc.Attributes, owner); err != nil {
		return domain.Document{}, fmt.Errorf("document %d sticker reference: %w", dj.ID, err)
	}

	// Main blob: doc:<document-id>
	if mainPath, ok := index.main[dj.ID]; ok {
		data, err := os.ReadFile(mainPath)
		if err != nil {
			return domain.Document{}, err
		}
		objectKey, err := s.blobs.Put(ctx, data)
		if err != nil {
			return domain.Document{}, err
		}
		if err := s.media.PutFileBlob(ctx, domain.FileBlob{
			LocationKey: fmt.Sprintf("doc:%d", doc.ID),
			Backend:     domain.MediaBackend(s.blobs.Name()),
			ObjectKey:   objectKey,
			Size:        int64(len(data)),
			MimeType:    dj.MimeType,
		}); err != nil {
			return domain.Document{}, err
		}
		s.prewarmSmallBlob(objectKey, data)
		stats.Blobs++
	}

	// Thumbnails keep both kinds coexisting (consistent with official stickers):
	//   - PhotoPathSize vector outline: delivered inline with the document metadata, it is the only
	//     placeholder TDesktop can render immediately for an animated sticker before the full .tgs
	//     download completes (history_view_sticker explicitly disables the stripped inline placeholder,
	//     and cached bytes are converted to downloadable at the RPC egress); dropping it makes the
	//     sticker blank on opening a session and triggers an extra thumbnail getFile.
	//   - Small PhotoSize static image: write a blob and stash it as PhotoCachedSize (converted to
	//     downloadable photoSize m at the RPC egress), for scenarios needing a small thumbnail such as
	//     the sticker panel.
	thumbs := make([]domain.PhotoSize, 0, len(dj.Thumbs))
	for _, tj := range dj.Thumbs {
		ps, downloadable := seedPhotoSize(tj)
		if ps.Kind == "" {
			continue
		}
		if downloadable {
			thumbPath, ok := index.thumb[dj.ID][ps.Type]
			if !ok {
				continue // no serviceable thumbnail file, drop this size (keep the PhotoPathSize placeholder)
			}
			data, err := os.ReadFile(thumbPath)
			if err != nil {
				return domain.Document{}, err
			}
			ps.Size = len(data)
			ps = seedInlineCachedDocumentThumb(ps, data)
			// A duplicate document can be present in several catalogs. Do not replace a
			// better already-persisted preview (and its shared location key) with a lower
			// quality rendition from the catalog imported later.
			if prior, ok := seedDocumentThumbByType(existing.Thumbs, ps.Type); existingFound && ok && seedPhotoSizeBetter(prior, ps) {
				ps = prior
			} else {
				objectKey, err := s.blobs.Put(ctx, data)
				if err != nil {
					return domain.Document{}, err
				}
				if err := s.media.PutFileBlob(ctx, domain.FileBlob{
					LocationKey: fmt.Sprintf("doc:%d:%s", doc.ID, ps.Type),
					Backend:     domain.MediaBackend(s.blobs.Name()),
					ObjectKey:   objectKey,
					Size:        int64(len(data)),
					MimeType:    seedThumbMimeType(data),
				}); err != nil {
					return domain.Document{}, err
				}
				s.prewarmSmallBlob(objectKey, data)
				stats.Blobs++
			}
		}
		thumbs = append(thumbs, ps)
	}
	doc.Thumbs = thumbs
	if data, ok := seedBundledDocumentPreview(doc.ID); ok {
		doc.Thumbs = appendSeedBundledDocumentPreview(doc.Thumbs, data)
	}
	if existingFound {
		doc.Thumbs = mergeSeedDocumentThumbs(existing.Thumbs, doc.Thumbs)
	}
	if err := s.ensureSeedCachedThumbBlobs(ctx, doc, stats); err != nil {
		return domain.Document{}, err
	}

	if err := s.media.PutDocument(ctx, doc); err != nil {
		return domain.Document{}, err
	}
	stats.Documents++
	return doc, nil
}

func appendSeedBundledDocumentPreview(thumbs []domain.PhotoSize, data []byte) []domain.PhotoSize {
	for _, thumb := range thumbs {
		if thumb.Type == seedBundledDocumentThumbType && seedPhotoSizePreviewTier(thumb) >= 4 {
			return thumbs
		}
	}
	out := thumbs[:0]
	for _, thumb := range thumbs {
		if thumb.Type != seedBundledDocumentThumbType {
			out = append(out, thumb)
		}
	}
	return append(out, domain.PhotoSize{
		Kind:  domain.PhotoSizeKindCached,
		Type:  seedBundledDocumentThumbType,
		W:     128,
		H:     128,
		Bytes: append([]byte(nil), data...),
	})
}

func (s *Service) prewarmSmallBlob(objectKey string, data []byte) {
	if len(data) > 0 && len(data) <= blobBytesCacheMaxEntryBytes {
		s.byteCache.put(objectKey, data)
	}
}

// ---- Directory scan: docID → main file / downloadable thumbnail ----

type seedDirIndex struct {
	main  map[int64]string            // docID -> main file path
	thumb map[int64]map[string]string // docID -> thumbType -> thumbnail file path
}

var seedTrailingDigits = regexp.MustCompile(`(\d{6,})`)
var seedThumbMarker = regexp.MustCompile(`_thumb\d+_`)

const seedInlineCachedDocumentThumbMaxBytes = 32 * 1024

var seedThumbType = regexp.MustCompile(`PhotoSize_type([a-z])`)

func scanSeedDir(dir string) (seedDirIndex, error) {
	idx := seedDirIndex{main: map[int64]string{}, thumb: map[int64]map[string]string{}}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return idx, nil // directory missing → empty index
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		ext := strings.ToLower(filepath.Ext(name))
		full := filepath.Join(dir, name)
		if marker := seedThumbMarker.FindStringIndex(name); marker != nil {
			// Collect only downloadable PhotoSize thumbnails (jpg); PhotoPathSize(svg) is inline in the JSON.
			if ext != ".jpg" && ext != ".jpeg" {
				continue
			}
			m := seedThumbType.FindStringSubmatch(name)
			if m == nil {
				continue
			}
			docID := docIDFromName(name[:marker[0]])
			if docID == 0 {
				continue
			}
			if idx.thumb[docID] == nil {
				idx.thumb[docID] = map[string]string{}
			}
			idx.thumb[docID][m[1]] = full
			continue
		}
		if ext == ".svg" || ext == ".json" {
			continue
		}
		docID := docIDFromName(strings.TrimSuffix(name, filepath.Ext(name)))
		if docID == 0 {
			continue
		}
		idx.main[docID] = full
	}
	return idx, nil
}

// docIDFromName takes the longest trailing digit run in the base name as the document id.
func docIDFromName(base string) int64 {
	matches := seedTrailingDigits.FindAllString(base, -1)
	if len(matches) == 0 {
		return 0
	}
	last := matches[len(matches)-1]
	id, err := strconv.ParseInt(last, 10, 64)
	if err != nil {
		return 0
	}
	return id
}

func systemKeyForDefaultSet(dirName string) string {
	switch dirName {
	case "DefaultSet_AnimatedEmoji":
		return "animated_emoji"
	case "DefaultSet_AnimatedEmojiAnimations":
		return "animated_emoji_animations"
	case "DefaultSet_EmojiGenericAnimations":
		return "emoji_generic_animations"
	case "DefaultSet_EmojiDefaultStatuses":
		return domain.StickerSetSystemKeyEmojiDefaultStatuses
	case "DefaultSet_EmojiDefaultTopicIcons":
		return domain.StickerSetSystemKeyEmojiDefaultTopicIcons
	case "DefaultSet_PremiumGifts":
		return domain.StickerSetSystemKeyPremiumGifts
	case "DefaultSet_TonGifts":
		return domain.StickerSetSystemKeyTonGifts
	case "DefaultSet_Dice_Normal":
		return "dice:\U0001f3b2"
	case "DefaultSet_Dice_Dart":
		return "dice:\U0001f3af"
	case "DefaultSet_Dice_Basketball":
		return "dice:\U0001f3c0"
	case "DefaultSet_Dice_Football":
		return "dice:⚽"
	case "DefaultSet_Dice_Bowling":
		return "dice:\U0001f3b3"
	case "DefaultSet_Dice_Casino":
		return "dice:\U0001f3b0"
	default:
		return ""
	}
}

func systemKeyForInputSetType(inputSetType string) string {
	normalized := strings.TrimSpace(inputSetType)
	normalized = strings.TrimPrefix(normalized, "*")
	normalized = strings.TrimPrefix(normalized, "tg.")
	switch normalized {
	case "InputStickerSetAnimatedEmoji":
		return "animated_emoji"
	case "InputStickerSetAnimatedEmojiAnimations":
		return "animated_emoji_animations"
	case "InputStickerSetEmojiGenericAnimations":
		return "emoji_generic_animations"
	case "InputStickerSetEmojiDefaultStatuses", "InputStickerSetEmojiChannelDefaultStatuses":
		return domain.StickerSetSystemKeyEmojiDefaultStatuses
	case "InputStickerSetEmojiDefaultTopicIcons":
		return domain.StickerSetSystemKeyEmojiDefaultTopicIcons
	case "InputStickerSetPremiumGifts":
		return domain.StickerSetSystemKeyPremiumGifts
	case "InputStickerSetTonGifts":
		return domain.StickerSetSystemKeyTonGifts
	default:
		return ""
	}
}

func stickerSetKind(sj seedStickerSetJSON, systemKey string) domain.StickerSetKind {
	switch {
	case systemKey != "":
		return domain.StickerSetKindSystem
	case sj.Emojis:
		return domain.StickerSetKindEmoji
	case sj.Masks:
		return domain.StickerSetKindMasks
	default:
		return domain.StickerSetKindStickers
	}
}

func seedStickerSetInstalled(kind domain.StickerSetKind) bool {
	return false
}

// ---- JSON → domain conversion ----

func seedDocumentAttributes(attrs []seedAttrJSON) []domain.DocumentAttribute {
	out := make([]domain.DocumentAttribute, 0, len(attrs))
	for _, a := range attrs {
		switch a.Type {
		case "DocumentAttributeImageSize":
			out = append(out, domain.DocumentAttribute{Kind: domain.DocAttrImageSize, W: a.W, H: a.H})
		case "DocumentAttributeAnimated":
			out = append(out, domain.DocumentAttribute{Kind: domain.DocAttrAnimated})
		case "DocumentAttributeSticker":
			attr := domain.DocumentAttribute{Kind: domain.DocAttrSticker, Alt: a.Alt, Mask: a.Mask}
			if a.Stickerset != nil {
				attr.StickerSetID = a.Stickerset.ID
				attr.StickerSetAccessHash = a.Stickerset.AccessHash
			}
			out = append(out, attr)
		case "DocumentAttributeCustomEmoji":
			attr := domain.DocumentAttribute{Kind: domain.DocAttrCustomEmoji, Alt: a.Alt, Free: a.Free, TextColor: a.TextColor}
			if a.Stickerset != nil {
				attr.StickerSetID = a.Stickerset.ID
				attr.StickerSetAccessHash = a.Stickerset.AccessHash
			}
			out = append(out, attr)
		case "DocumentAttributeVideo":
			out = append(out, domain.DocumentAttribute{Kind: domain.DocAttrVideo, W: a.W, H: a.H, Duration: a.Duration, RoundMessage: a.RoundMessage, SupportsStreaming: a.SupportsStreaming, NoSound: a.NoSound, VideoCodec: a.VideoCodec})
		case "DocumentAttributeAudio":
			out = append(out, domain.DocumentAttribute{Kind: domain.DocAttrAudio, AudioDuration: int(a.Duration), Voice: a.Voice, Title: a.Title, Performer: a.Performer})
		case "DocumentAttributeFilename":
			out = append(out, domain.DocumentAttribute{Kind: domain.DocAttrFilename, FileName: a.FileName})
		}
	}
	return out
}

func seedStickerSetPhotoSizes(thumbs []seedThumbJSON) []domain.PhotoSize {
	out := make([]domain.PhotoSize, 0, len(thumbs))
	for _, t := range thumbs {
		ps, downloadable := seedPhotoSize(t)
		if ps.Kind == "" || downloadable {
			continue
		}
		out = append(out, ps)
	}
	return out
}

func seedPhotoSize(t seedThumbJSON) (domain.PhotoSize, bool) {
	switch t.Type {
	case "PhotoSize":
		return domain.PhotoSize{Kind: domain.PhotoSizeKindDefault, Type: t.SizeType, W: t.W, H: t.H, Size: t.Size}, true
	case "PhotoStrippedSize":
		b, _ := hex.DecodeString(t.Bytes)
		return domain.PhotoSize{Kind: domain.PhotoSizeKindStripped, Type: t.SizeType, Bytes: b}, false
	case "PhotoCachedSize":
		b, _ := hex.DecodeString(t.Bytes)
		return domain.PhotoSize{Kind: domain.PhotoSizeKindCached, Type: t.SizeType, W: t.W, H: t.H, Bytes: b}, false
	case "PhotoPathSize":
		b, _ := hex.DecodeString(t.Bytes)
		return domain.PhotoSize{Kind: domain.PhotoSizeKindPath, Type: t.SizeType, Bytes: b}, false
	case "PhotoSizeProgressive":
		return domain.PhotoSize{Kind: domain.PhotoSizeKindProgressive, Type: t.SizeType, W: t.W, H: t.H, Sizes: t.Sizes}, true
	default:
		return domain.PhotoSize{}, false
	}
}

func seedInlineCachedDocumentThumb(ps domain.PhotoSize, data []byte) domain.PhotoSize {
	if ps.Kind != domain.PhotoSizeKindDefault || len(data) == 0 || len(data) > seedInlineCachedDocumentThumbMaxBytes {
		return ps
	}
	ps.Kind = domain.PhotoSizeKindCached
	ps.Size = 0
	ps.Bytes = append([]byte(nil), data...)
	return ps
}

// mergeSeedDocumentThumbs makes duplicate seed imports monotonic for preview quality.
// PhotoSize.Type is also the blob location suffix, so only one winner per type may be
// advertised. Incoming metadata wins ties; a richer existing preview wins downgrades.
func mergeSeedDocumentThumbs(existing, incoming []domain.PhotoSize) []domain.PhotoSize {
	out := append([]domain.PhotoSize(nil), incoming...)
	byType := make(map[string]int, len(out))
	for i, thumb := range out {
		if thumb.Type != "" {
			byType[thumb.Type] = i
		}
	}
	for _, thumb := range existing {
		if thumb.Type != "" {
			if i, ok := byType[thumb.Type]; ok {
				if seedPhotoSizeBetter(thumb, out[i]) {
					out[i] = thumb
				}
				continue
			}
			byType[thumb.Type] = len(out)
		}
		out = append(out, thumb)
	}

	return out
}

func seedDocumentThumbByType(thumbs []domain.PhotoSize, typ string) (domain.PhotoSize, bool) {
	for _, thumb := range thumbs {
		if thumb.Type == typ {
			return thumb, true
		}
	}
	return domain.PhotoSize{}, false
}

func seedPhotoSizeBetter(a, b domain.PhotoSize) bool {
	aTier, bTier := seedPhotoSizePreviewTier(a), seedPhotoSizePreviewTier(b)
	if aTier != bTier {
		return aTier > bTier
	}
	aArea, bArea := int64(a.W)*int64(a.H), int64(b.W)*int64(b.H)
	if aArea != bArea {
		return aArea > bArea
	}
	aPayload, bPayload := len(a.Bytes)+a.Size, len(b.Bytes)+b.Size
	return aPayload > bPayload
}

func seedPhotoSizePreviewTier(thumb domain.PhotoSize) int {
	switch thumb.Kind {
	case domain.PhotoSizeKindCached:
		if len(thumb.Bytes) > 0 && thumb.W > 0 && thumb.H > 0 {
			return 4
		}
	case domain.PhotoSizeKindDefault, domain.PhotoSizeKindProgressive:
		if thumb.Size > 0 && thumb.W > 0 && thumb.H > 0 {
			return 4
		}
	case domain.PhotoSizeKindPath, domain.PhotoSizeKindStripped:
		if len(thumb.Bytes) > 0 {
			return 3
		}
	}
	return 1
}

// ensureSeedCachedThumbBlobs keeps the RPC conversion invariant: document cached
// previews are exposed as downloadable PhotoSize entries, so every advertised type
// must have a matching blob even when the source JSON carried the bytes inline.
func (s *Service) ensureSeedCachedThumbBlobs(ctx context.Context, doc domain.Document, stats *SeedStats) error {
	for _, thumb := range doc.Thumbs {
		if thumb.Kind != domain.PhotoSizeKindCached || thumb.Type == "" || len(thumb.Bytes) == 0 {
			continue
		}
		locationKey := fmt.Sprintf("doc:%d:%s", doc.ID, thumb.Type)
		mimeType := seedThumbMimeType(thumb.Bytes)
		stored, found, err := s.media.GetFileBlob(ctx, locationKey)
		if err != nil {
			return err
		}
		if found && stored.Size == int64(len(thumb.Bytes)) && stored.MimeType == mimeType {
			continue
		}
		if s.blobs == nil {
			return fmt.Errorf("blob backend not configured for cached document thumb %s", locationKey)
		}
		objectKey, err := s.blobs.Put(ctx, thumb.Bytes)
		if err != nil {
			return err
		}
		if err := s.media.PutFileBlob(ctx, domain.FileBlob{
			LocationKey: locationKey,
			Backend:     domain.MediaBackend(s.blobs.Name()),
			ObjectKey:   objectKey,
			Size:        int64(len(thumb.Bytes)),
			MimeType:    mimeType,
		}); err != nil {
			return err
		}
		s.prewarmSmallBlob(objectKey, thumb.Bytes)
		stats.Blobs++
	}
	return nil
}

func seedDocumentHasAttribute(attrs []domain.DocumentAttribute, kind domain.DocumentAttributeKind) bool {
	for _, attr := range attrs {
		if attr.Kind == kind {
			return true
		}
	}
	return false
}

func seedThumbMimeType(data []byte) string {
	switch {
	case len(data) >= 12 && data[0] == 'R' && data[1] == 'I' && data[2] == 'F' && data[3] == 'F' &&
		data[8] == 'W' && data[9] == 'E' && data[10] == 'B' && data[11] == 'P':
		return "image/webp"
	case len(data) >= 3 && data[0] == 0xFF && data[1] == 0xD8 && data[2] == 0xFF:
		return "image/jpeg"
	case len(data) >= 8 && data[0] == 0x89 && data[1] == 'P' && data[2] == 'N' && data[3] == 'G':
		return "image/png"
	case len(data) >= 6 && data[0] == 'G' && data[1] == 'I' && data[2] == 'F':
		return "image/gif"
	default:
		return "application/octet-stream"
	}
}

func (s *Service) documentsNeedInlineCachedThumbs(ctx context.Context, ids []int64) (bool, error) {
	return s.documentsNeedSeedRepair(ctx, ids)
}

func (s *Service) documentsNeedSeedRepair(ctx context.Context, ids []int64) (bool, error) {
	if len(ids) == 0 {
		return false, nil
	}
	seen := make(map[int64]struct{}, len(ids))
	unique := make([]int64, 0, len(ids))
	for _, id := range ids {
		if id == 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		unique = append(unique, id)
	}
	// Fetch only the thumbnail validation metadata, not the document body: seed documents' thumbs inline
	// thousands of base64 thumbnail bytes, GetDocuments takes 2-3s on a slow machine, while here only
	// kind/size/file header are needed.
	states, err := s.media.SeedThumbStates(ctx, unique)
	if err != nil {
		return false, err
	}
	for _, state := range states {
		switch domain.PhotoSizeKind(state.Kind) {
		case domain.PhotoSizeKindDefault:
			if state.DeclaredSize > 0 && state.DeclaredSize <= seedInlineCachedDocumentThumbMaxBytes {
				return true, nil
			}
		case domain.PhotoSizeKindCached:
			if state.DataSize == 0 {
				continue
			}
			if !state.HasBlob || state.BlobSize != int64(state.DataSize) {
				return true, nil
			}
			if want := seedThumbMimeType(state.Head); want != "application/octet-stream" && state.BlobMime != want {
				return true, nil
			}
		}
	}
	return false, nil
}

func seedStickerPacks(setPacks, resultPacks []seedStickerPackJSON) []domain.StickerPack {
	packs := setPacks
	if len(packs) == 0 {
		packs = resultPacks
	}
	out := make([]domain.StickerPack, 0, len(packs))
	for _, p := range packs {
		documents := make([]int64, 0, len(p.Documents))
		for _, id := range p.Documents {
			if id > 0 {
				documents = append(documents, id)
			}
		}
		out = append(out, domain.StickerPack{Emoticon: p.Emoticon, DocumentIDs: documents})
	}
	return out
}

func parseSeedDate(s string) int {
	if s == "" {
		return 0
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return int(t.Unix())
	}
	return 0
}

func derefInt64(v *int64) int64 {
	if v == nil {
		return 0
	}
	return *v
}

// ---- seed JSON structures ----

type seedInputStickerSetJSON struct {
	ID         int64 `json:"id"`
	AccessHash int64 `json:"access_hash"`
}

type seedAttrJSON struct {
	Type              string                   `json:"_"`
	W                 int                      `json:"w"`
	H                 int                      `json:"h"`
	Alt               string                   `json:"alt"`
	Mask              bool                     `json:"mask"`
	Duration          float64                  `json:"duration"`
	RoundMessage      bool                     `json:"round_message"`
	SupportsStreaming bool                     `json:"supports_streaming"`
	NoSound           bool                     `json:"nosound"`
	VideoCodec        string                   `json:"video_codec"`
	Voice             bool                     `json:"voice"`
	Title             string                   `json:"title"`
	Performer         string                   `json:"performer"`
	FileName          string                   `json:"file_name"`
	Free              bool                     `json:"free"`
	TextColor         bool                     `json:"text_color"`
	Stickerset        *seedInputStickerSetJSON `json:"stickerset"`
}

type seedThumbJSON struct {
	Type     string `json:"_"`
	SizeType string `json:"type"`
	W        int    `json:"w"`
	H        int    `json:"h"`
	Size     int    `json:"size"`
	Bytes    string `json:"bytes"`
	Sizes    []int  `json:"sizes"`
}

type seedDocumentJSON struct {
	ID            int64           `json:"id"`
	AccessHash    int64           `json:"access_hash"`
	FileReference string          `json:"file_reference"`
	Date          string          `json:"date"`
	MimeType      string          `json:"mime_type"`
	Size          int64           `json:"size"`
	DCID          int             `json:"dc_id"`
	Attributes    []seedAttrJSON  `json:"attributes"`
	Thumbs        []seedThumbJSON `json:"thumbs"`
}

type seedStickerPackJSON struct {
	Emoticon  string  `json:"emoticon"`
	Documents []int64 `json:"documents"`
}

type seedStickerSetJSON struct {
	ID              int64                 `json:"id"`
	AccessHash      int64                 `json:"access_hash"`
	Title           string                `json:"title"`
	ShortName       string                `json:"short_name"`
	Count           int                   `json:"count"`
	Hash            int                   `json:"hash"`
	Archived        bool                  `json:"archived"`
	Official        bool                  `json:"official"`
	Masks           bool                  `json:"masks"`
	Emojis          bool                  `json:"emojis"`
	Thumbs          []seedThumbJSON       `json:"thumbs"`
	ThumbDCID       int                   `json:"thumb_dc_id"`
	ThumbVersion    int                   `json:"thumb_version"`
	ThumbDocumentID *int64                `json:"thumb_document_id"`
	Packs           []seedStickerPackJSON `json:"packs"`
}

type seedStickerSetResultJSON struct {
	Set       seedStickerSetJSON    `json:"set"`
	Packs     []seedStickerPackJSON `json:"packs"`
	Documents []seedDocumentJSON    `json:"documents"`
}

type seedReactionJSON struct {
	Reaction          string            `json:"reaction"`
	Title             string            `json:"title"`
	Inactive          bool              `json:"inactive"`
	Premium           bool              `json:"premium"`
	StaticIcon        *seedDocumentJSON `json:"static_icon"`
	AppearAnimation   *seedDocumentJSON `json:"appear_animation"`
	SelectAnimation   *seedDocumentJSON `json:"select_animation"`
	ActivateAnimation *seedDocumentJSON `json:"activate_animation"`
	EffectAnimation   *seedDocumentJSON `json:"effect_animation"`
	AroundAnimation   *seedDocumentJSON `json:"around_animation"`
	CenterIcon        *seedDocumentJSON `json:"center_icon"`
}
