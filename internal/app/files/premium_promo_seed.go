package files

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"image/jpeg"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	"telesrv/internal/domain"

	"go.uber.org/zap"
)

const (
	premiumPromoSeedStateKey     = "files.premium_promo"
	premiumPromoSeedStateVersion = "premium-promo-v1"

	premiumPromoManifestName = "premium_promo.json"
	premiumPromoMaxVideos    = 128
	premiumPromoMaxVideoSize = int64(64 << 20)
	premiumPromoMaxThumbSize = int64(4 << 20)
	premiumPromoMaxTotalSize = int64(512 << 20)
)

// PremiumPromoSeedStats reports the startup import outcome. Videos is the
// number of usable catalog entries; Blobs counts main/thumbnail blobs written
// during this run.
type PremiumPromoSeedStats struct {
	Videos  int
	Blobs   int
	Skipped bool
}

type premiumPromoSeedJSON struct {
	APICall       string             `json:"api_call"`
	StatusText    string             `json:"status_text"`
	VideoSections []string           `json:"video_sections"`
	Videos        []seedDocumentJSON `json:"videos"`
	PeriodOptions []json.RawMessage  `json:"period_options"`
}

type premiumPromoSeedVideo struct {
	section   string
	document  domain.Document
	mainPath  string
	thumbPath string
	thumbType string
}

// SeedPremiumPromo imports the exported promo videos into the ordinary
// document/file_blob storage. A missing root is an optional-resource fallback;
// once the directory exists, malformed or incomplete data is a startup error.
func (s *Service) SeedPremiumPromo(ctx context.Context, root string) (PremiumPromoSeedStats, error) {
	var stats PremiumPromoSeedStats
	if root == "" {
		s.clearPremiumPromo()
		stats.Skipped = true
		s.warnPremiumPromoMissing(root, errors.New("seed dir is empty"))
		return stats, nil
	}
	info, err := os.Stat(root)
	if err != nil {
		if os.IsNotExist(err) {
			s.clearPremiumPromo()
			stats.Skipped = true
			s.warnPremiumPromoMissing(root, err)
			return stats, nil
		}
		return stats, fmt.Errorf("stat premium promo seed dir %q: %w", root, err)
	}
	if !info.IsDir() {
		return stats, fmt.Errorf("premium promo seed path %q is not a directory", root)
	}

	manifestPath := filepath.Join(root, premiumPromoManifestName)
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		return stats, fmt.Errorf("read premium promo manifest %q: %w", manifestPath, err)
	}
	videos, err := parsePremiumPromoSeed(root, raw)
	if err != nil {
		return stats, fmt.Errorf("validate premium promo seed: %w", err)
	}
	for i := range videos {
		videos[i].document.DCID = s.dc
	}
	stats.Videos = len(videos)

	stateHash, err := premiumPromoSeedHash(raw, videos, s.dc)
	if err != nil {
		return stats, fmt.Errorf("hash premium promo seed: %w", err)
	}
	stateMatches, err := s.seedStateMatches(ctx, premiumPromoSeedStateKey, stateHash)
	if err != nil {
		return stats, fmt.Errorf("read premium promo seed state: %w", err)
	}
	if stateMatches {
		if catalog, ready, err := s.loadPremiumPromoCatalog(ctx, videos); err != nil {
			return stats, fmt.Errorf("verify premium promo catalog: %w", err)
		} else if ready {
			s.setPremiumPromoCatalog(catalog)
			stats.Skipped = true
			return stats, nil
		}
	}

	for _, video := range videos {
		existing, found, err := s.media.GetDocument(ctx, video.document.ID)
		if err != nil {
			return stats, fmt.Errorf("read premium promo document %d: %w", video.document.ID, err)
		}
		if found && existing.AccessHash != video.document.AccessHash {
			return stats, fmt.Errorf(
				"premium promo document %d collides with access_hash %d (seed has %d)",
				video.document.ID,
				existing.AccessHash,
				video.document.AccessHash,
			)
		}

		forceBlobWrite := !stateMatches
		if wrote, err := s.putPremiumPromoBlob(
			ctx,
			fmt.Sprintf("doc:%d", video.document.ID),
			video.mainPath,
			video.document.MimeType,
			video.document.Size,
			forceBlobWrite,
		); err != nil {
			return stats, fmt.Errorf("import premium promo video %d: %w", video.document.ID, err)
		} else if wrote {
			stats.Blobs++
		}
		thumb := video.document.Thumbs[0]
		if wrote, err := s.putPremiumPromoBlob(
			ctx,
			fmt.Sprintf("doc:%d:%s", video.document.ID, video.thumbType),
			video.thumbPath,
			"image/jpeg",
			int64(thumb.Size),
			forceBlobWrite,
		); err != nil {
			return stats, fmt.Errorf("import premium promo thumbnail %d: %w", video.document.ID, err)
		} else if wrote {
			stats.Blobs++
		}
		if err := s.media.PutDocument(ctx, video.document); err != nil {
			return stats, fmt.Errorf("store premium promo document %d: %w", video.document.ID, err)
		}
	}

	catalog, ready, err := s.loadPremiumPromoCatalog(ctx, videos)
	if err != nil {
		return stats, fmt.Errorf("verify imported premium promo catalog: %w", err)
	}
	if !ready {
		return stats, errors.New("premium promo catalog is incomplete after import")
	}
	if err := s.putSeedState(ctx, premiumPromoSeedStateKey, stateHash); err != nil {
		return stats, fmt.Errorf("record premium promo seed state: %w", err)
	}
	s.setPremiumPromoCatalog(catalog)
	return stats, nil
}

// PremiumPromo returns a deep copy so callers cannot mutate the startup
// catalog or another request's response.
func (s *Service) PremiumPromo(_ context.Context) (domain.PremiumPromoCatalog, bool, error) {
	s.premiumPromoMu.RLock()
	defer s.premiumPromoMu.RUnlock()
	if !s.premiumPromoReady {
		return domain.PremiumPromoCatalog{}, false, nil
	}
	return domain.PremiumPromoCatalog{
		VideoSections: append([]string(nil), s.premiumPromo.VideoSections...),
		Videos:        copyDocuments(s.premiumPromo.Videos),
	}, true, nil
}

func (s *Service) setPremiumPromoCatalog(catalog domain.PremiumPromoCatalog) {
	s.premiumPromoMu.Lock()
	defer s.premiumPromoMu.Unlock()
	s.premiumPromo = domain.PremiumPromoCatalog{
		VideoSections: append([]string(nil), catalog.VideoSections...),
		Videos:        copyDocuments(catalog.Videos),
	}
	s.premiumPromoReady = true
}

func (s *Service) clearPremiumPromo() {
	s.premiumPromoMu.Lock()
	defer s.premiumPromoMu.Unlock()
	s.premiumPromo = domain.PremiumPromoCatalog{}
	s.premiumPromoReady = false
}

func (s *Service) warnPremiumPromoMissing(root string, err error) {
	if s.log == nil {
		return
	}
	s.log.Warn(
		"Premium promo seed 目录不存在，help.getPremiumPromo 将返回无视频兼容响应",
		zap.String("dir", root),
		zap.Error(err),
	)
}

func parsePremiumPromoSeed(root string, raw []byte) ([]premiumPromoSeedVideo, error) {
	var parsed premiumPromoSeedJSON
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("parse %s: %w", premiumPromoManifestName, err)
	}
	if parsed.APICall != "help.getPremiumPromo" {
		return nil, fmt.Errorf("api_call = %q, want help.getPremiumPromo", parsed.APICall)
	}
	if len(parsed.VideoSections) == 0 || len(parsed.VideoSections) > premiumPromoMaxVideos {
		return nil, fmt.Errorf("video_sections count %d is outside 1..%d", len(parsed.VideoSections), premiumPromoMaxVideos)
	}
	if len(parsed.VideoSections) != len(parsed.Videos) {
		return nil, fmt.Errorf("video_sections count %d does not match videos count %d", len(parsed.VideoSections), len(parsed.Videos))
	}

	seenSections := make(map[string]struct{}, len(parsed.VideoSections))
	seenDocuments := make(map[int64]struct{}, len(parsed.Videos))
	out := make([]premiumPromoSeedVideo, 0, len(parsed.Videos))
	var totalSize int64
	for i, dj := range parsed.Videos {
		section := parsed.VideoSections[i]
		if !validPremiumPromoSection(section) {
			return nil, fmt.Errorf("video_sections[%d] %q is invalid", i, section)
		}
		if _, exists := seenSections[section]; exists {
			return nil, fmt.Errorf("duplicate video section %q", section)
		}
		seenSections[section] = struct{}{}

		if dj.ID <= 0 {
			return nil, fmt.Errorf("videos[%d].id must be positive", i)
		}
		if _, exists := seenDocuments[dj.ID]; exists {
			return nil, fmt.Errorf("duplicate video document id %d", dj.ID)
		}
		seenDocuments[dj.ID] = struct{}{}
		if dj.AccessHash == 0 {
			return nil, fmt.Errorf("videos[%d].access_hash must be non-zero", i)
		}
		fileReference, err := hex.DecodeString(dj.FileReference)
		if err != nil || len(fileReference) == 0 {
			return nil, fmt.Errorf("videos[%d].file_reference is not non-empty hex", i)
		}
		date, err := time.Parse(time.RFC3339, dj.Date)
		if err != nil || date.Unix() < 0 || date.Unix() > 1<<31-1 {
			return nil, fmt.Errorf("videos[%d].date %q is outside TL int date range", i, dj.Date)
		}
		if dj.MimeType != "video/mp4" {
			return nil, fmt.Errorf("videos[%d].mime_type = %q, want video/mp4", i, dj.MimeType)
		}
		if dj.Size <= 0 || dj.Size > premiumPromoMaxVideoSize {
			return nil, fmt.Errorf("videos[%d].size %d is outside 1..%d", i, dj.Size, premiumPromoMaxVideoSize)
		}
		if err := validatePremiumPromoAttributes(i, dj.Attributes); err != nil {
			return nil, err
		}

		mainPath := filepath.Join(root, "documents", fmt.Sprintf("%d.mp4", dj.ID))
		mainInfo, err := regularFileInfo(mainPath)
		if err != nil {
			return nil, fmt.Errorf("videos[%d] main file: %w", i, err)
		}
		if mainInfo.Size() != dj.Size {
			return nil, fmt.Errorf("videos[%d] main file size %d does not match manifest %d", i, mainInfo.Size(), dj.Size)
		}
		if err := validateMP4Header(mainPath); err != nil {
			return nil, fmt.Errorf("videos[%d] main file: %w", i, err)
		}

		thumbPath := filepath.Join(root, "thumbs", fmt.Sprintf("%d.jpg", dj.ID))
		thumbInfo, err := regularFileInfo(thumbPath)
		if err != nil {
			return nil, fmt.Errorf("videos[%d] thumbnail: %w", i, err)
		}
		if thumbInfo.Size() <= 0 || thumbInfo.Size() > premiumPromoMaxThumbSize {
			return nil, fmt.Errorf("videos[%d] thumbnail size %d is outside 1..%d", i, thumbInfo.Size(), premiumPromoMaxThumbSize)
		}
		w, h, err := jpegDimensions(thumbPath)
		if err != nil {
			return nil, fmt.Errorf("videos[%d] thumbnail: %w", i, err)
		}
		thumbType := premiumPromoThumbType(w, h)
		attributes := seedDocumentAttributes(dj.Attributes)
		document := domain.Document{
			ID:            dj.ID,
			AccessHash:    dj.AccessHash,
			FileReference: fileReference,
			Date:          int(date.Unix()),
			MimeType:      dj.MimeType,
			Size:          dj.Size,
			DCID:          0, // overwritten with the canonical server DC by the caller
			Attributes:    attributes,
			Thumbs: []domain.PhotoSize{{
				Kind: domain.PhotoSizeKindDefault,
				Type: thumbType,
				W:    w,
				H:    h,
				Size: int(thumbInfo.Size()),
			}},
		}
		out = append(out, premiumPromoSeedVideo{
			section:   section,
			document:  document,
			mainPath:  mainPath,
			thumbPath: thumbPath,
			thumbType: thumbType,
		})
		totalSize += mainInfo.Size() + thumbInfo.Size()
		if totalSize > premiumPromoMaxTotalSize {
			return nil, fmt.Errorf("premium promo source bytes %d exceed limit %d", totalSize, premiumPromoMaxTotalSize)
		}
	}
	return out, nil
}

func validatePremiumPromoAttributes(index int, attrs []seedAttrJSON) error {
	var filename, video, animated int
	for j, attr := range attrs {
		switch attr.Type {
		case "DocumentAttributeFilename":
			filename++
			if strings.TrimSpace(attr.FileName) == "" {
				return fmt.Errorf("videos[%d].attributes[%d] has empty file_name", index, j)
			}
		case "DocumentAttributeVideo":
			video++
			if attr.W <= 0 || attr.W > 16384 || attr.H <= 0 || attr.H > 16384 {
				return fmt.Errorf("videos[%d].attributes[%d] has invalid video dimensions %dx%d", index, j, attr.W, attr.H)
			}
			if attr.Duration <= 0 || attr.Duration > 3600 {
				return fmt.Errorf("videos[%d].attributes[%d] has invalid duration %v", index, j, attr.Duration)
			}
		case "DocumentAttributeAnimated":
			animated++
		default:
			return fmt.Errorf("videos[%d].attributes[%d] has unsupported type %q", index, j, attr.Type)
		}
	}
	if filename != 1 || video != 1 || animated > 1 {
		return fmt.Errorf("videos[%d] must contain exactly one filename/video and at most one animated attribute", index)
	}
	return nil
}

func validPremiumPromoSection(section string) bool {
	if section == "" || len(section) > 64 {
		return false
	}
	for _, r := range section {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '_' {
			return false
		}
	}
	return true
}

func regularFileInfo(path string) (os.FileInfo, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%q is not a regular file", path)
	}
	return info, nil
}

func validateMP4Header(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	header := make([]byte, 12)
	if _, err := io.ReadFull(f, header); err != nil {
		return fmt.Errorf("read MP4 header: %w", err)
	}
	if string(header[4:8]) != "ftyp" {
		return errors.New("missing ISO BMFF ftyp header")
	}
	return nil
}

func jpegDimensions(path string) (int, int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, 0, err
	}
	defer f.Close()
	cfg, err := jpeg.DecodeConfig(f)
	if err != nil {
		return 0, 0, fmt.Errorf("decode JPEG config: %w", err)
	}
	if cfg.Width <= 0 || cfg.Width > 16384 || cfg.Height <= 0 || cfg.Height > 16384 {
		return 0, 0, fmt.Errorf("invalid JPEG dimensions %dx%d", cfg.Width, cfg.Height)
	}
	return cfg.Width, cfg.Height, nil
}

func premiumPromoThumbType(w, h int) string {
	maxDimension := w
	if h > maxDimension {
		maxDimension = h
	}
	switch {
	case maxDimension <= 100:
		return "s"
	case maxDimension <= 320:
		return "m"
	case maxDimension <= 800:
		return "x"
	case maxDimension <= 1280:
		return "y"
	default:
		return "w"
	}
}

func premiumPromoSeedHash(raw []byte, videos []premiumPromoSeedVideo, dc int) (string, error) {
	return seedStateHash(func(h hash.Hash) error {
		writeSeedStateHeader(h, premiumPromoSeedStateVersion, dc)
		if _, err := h.Write(raw); err != nil {
			return err
		}
		paths := make([]string, 0, len(videos)*2)
		for _, video := range videos {
			paths = append(paths, video.mainPath, video.thumbPath)
		}
		sort.Strings(paths)
		for _, path := range paths {
			info, err := regularFileInfo(path)
			if err != nil {
				return err
			}
			rel := filepath.Join(filepath.Base(filepath.Dir(path)), filepath.Base(path))
			_, _ = fmt.Fprintf(h, "\nfile=%s\x00size=%d\x00mtime=%d", filepath.ToSlash(rel), info.Size(), info.ModTime().UnixNano())
		}
		return nil
	})
}

func (s *Service) loadPremiumPromoCatalog(ctx context.Context, videos []premiumPromoSeedVideo) (domain.PremiumPromoCatalog, bool, error) {
	ids := make([]int64, 0, len(videos))
	locationKeys := make([]string, 0, len(videos)*2)
	for i := range videos {
		videos[i].document.DCID = s.dc
		ids = append(ids, videos[i].document.ID)
		locationKeys = append(
			locationKeys,
			fmt.Sprintf("doc:%d", videos[i].document.ID),
			fmt.Sprintf("doc:%d:%s", videos[i].document.ID, videos[i].thumbType),
		)
	}
	stored, err := s.media.GetDocuments(ctx, ids)
	if err != nil {
		return domain.PremiumPromoCatalog{}, false, err
	}
	if len(stored) != len(videos) {
		return domain.PremiumPromoCatalog{}, false, nil
	}
	byID := make(map[int64]domain.Document, len(stored))
	for _, doc := range stored {
		byID[doc.ID] = doc
	}
	blobs, err := s.media.GetFileBlobs(ctx, locationKeys)
	if err != nil {
		return domain.PremiumPromoCatalog{}, false, err
	}

	catalog := domain.PremiumPromoCatalog{
		VideoSections: make([]string, 0, len(videos)),
		Videos:        make([]domain.Document, 0, len(videos)),
	}
	for _, video := range videos {
		storedDoc, ok := byID[video.document.ID]
		if !ok || !premiumPromoDocumentEqual(storedDoc, video.document) {
			return domain.PremiumPromoCatalog{}, false, nil
		}
		mainKey := fmt.Sprintf("doc:%d", video.document.ID)
		thumbKey := fmt.Sprintf("doc:%d:%s", video.document.ID, video.thumbType)
		if !s.premiumPromoBlobReady(ctx, blobs[mainKey], mainKey, video.document.Size, video.document.MimeType) {
			return domain.PremiumPromoCatalog{}, false, nil
		}
		if !s.premiumPromoBlobReady(ctx, blobs[thumbKey], thumbKey, int64(video.document.Thumbs[0].Size), "image/jpeg") {
			return domain.PremiumPromoCatalog{}, false, nil
		}
		catalog.VideoSections = append(catalog.VideoSections, video.section)
		catalog.Videos = append(catalog.Videos, storedDoc)
	}
	return catalog, true, nil
}

func premiumPromoDocumentEqual(got, want domain.Document) bool {
	return got.ID == want.ID &&
		got.AccessHash == want.AccessHash &&
		bytes.Equal(got.FileReference, want.FileReference) &&
		got.Date == want.Date &&
		got.MimeType == want.MimeType &&
		got.Size == want.Size &&
		got.DCID == want.DCID &&
		reflect.DeepEqual(got.Attributes, want.Attributes) &&
		reflect.DeepEqual(got.Thumbs, want.Thumbs)
}

func (s *Service) premiumPromoBlobReady(ctx context.Context, blob domain.FileBlob, locationKey string, size int64, mimeType string) bool {
	if blob.LocationKey != locationKey ||
		blob.Backend != domain.MediaBackend(s.blobs.Name()) ||
		blob.ObjectKey == "" ||
		blob.Size != size ||
		blob.MimeType != mimeType {
		return false
	}
	_, total, err := s.blobs.GetRange(ctx, blob.ObjectKey, 0, 1)
	return err == nil && total == size
}

func (s *Service) putPremiumPromoBlob(
	ctx context.Context,
	locationKey string,
	path string,
	mimeType string,
	wantSize int64,
	force bool,
) (bool, error) {
	if !force {
		if blob, found, err := s.media.GetFileBlob(ctx, locationKey); err != nil {
			return false, err
		} else if found && s.premiumPromoBlobReady(ctx, blob, locationKey, wantSize, mimeType) {
			return false, nil
		}
	}
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()
	objectKey, size, sum, err := s.blobs.PutReader(ctx, f)
	if err != nil {
		return false, err
	}
	if size != wantSize {
		return false, fmt.Errorf("streamed size %d does not match validated size %d", size, wantSize)
	}
	blob := domain.FileBlob{
		LocationKey: locationKey,
		Backend:     domain.MediaBackend(s.blobs.Name()),
		ObjectKey:   objectKey,
		Size:        size,
		SHA256:      append([]byte(nil), sum...),
		MimeType:    mimeType,
	}
	if err := s.media.PutFileBlob(ctx, blob); err != nil {
		return false, err
	}
	s.blobCache.put(locationKey, blob)
	return true, nil
}
