package files

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"telesrv/internal/domain"
)

func (s *Service) CheckStickerSetShortName(ctx context.Context, shortName string) (bool, error) {
	shortName = normalizeStickerSetShortName(shortName)
	if err := validateStickerSetShortName(shortName); err != nil {
		return false, err
	}
	return s.media.StickerSetShortNameAvailable(ctx, shortName)
}

func (s *Service) SuggestStickerSetShortName(ctx context.Context, title string, userID int64) (string, error) {
	if userID <= 0 {
		return "", domain.ErrStickerSetCreatorInvalid
	}
	title = strings.TrimSpace(title)
	if err := validateStickerSetTitle(title); err != nil {
		return "", err
	}
	base := stickerSetShortNameBase(title)
	candidates := []string{base, base + "_pack"}
	if suffix := userIDSuffix(userID); suffix != "" {
		candidates = append(candidates, base+"_"+suffix)
	}
	for i := 2; i <= 99; i++ {
		candidates = append(candidates, trimStickerSetShortNameBase(base, 3)+"_"+itoaSmall(i))
	}
	for _, candidate := range candidates {
		if err := validateStickerSetShortName(candidate); err != nil {
			continue
		}
		available, err := s.media.StickerSetShortNameAvailable(ctx, candidate)
		if err != nil {
			return "", err
		}
		if available {
			return candidate, nil
		}
	}
	return "", domain.ErrStickerSetShortNameOccupied
}

func (s *Service) CreateStickerSet(ctx context.Context, req domain.CreateStickerSetRequest) (domain.StickerSet, []domain.Document, error) {
	if req.CreatorUserID <= 0 {
		return domain.StickerSet{}, nil, domain.ErrStickerSetCreatorInvalid
	}
	title := strings.TrimSpace(req.Title)
	if err := validateStickerSetTitle(title); err != nil {
		return domain.StickerSet{}, nil, err
	}
	kind := normalizeStickerSetKind(req.Kind)
	if len(req.Items) == 0 {
		return domain.StickerSet{}, nil, domain.ErrStickerSetEmpty
	}
	if len(req.Items) > domain.MaxStickerSetItems {
		return domain.StickerSet{}, nil, domain.ErrStickerSetTooMuch
	}
	shortName := normalizeStickerSetShortName(req.ShortName)
	var err error
	if shortName == "" {
		shortName, err = s.SuggestStickerSetShortName(ctx, title, req.CreatorUserID)
		if err != nil {
			return domain.StickerSet{}, nil, err
		}
	} else {
		if err := validateStickerSetShortName(shortName); err != nil {
			return domain.StickerSet{}, nil, err
		}
		available, err := s.media.StickerSetShortNameAvailable(ctx, shortName)
		if err != nil {
			return domain.StickerSet{}, nil, err
		}
		if !available {
			return domain.StickerSet{}, nil, domain.ErrStickerSetShortNameOccupied
		}
	}

	docIDs, docAccess, thumbID, thumbAccess, err := stickerSetInputDocumentRefs(req)
	if err != nil {
		return domain.StickerSet{}, nil, err
	}
	loaded, err := s.media.GetDocuments(ctx, docIDs)
	if err != nil {
		return domain.StickerSet{}, nil, err
	}
	docByID := documentsByID(loaded)
	items := make([]domain.StickerSetItemInput, 0, len(req.Items))
	seenDocs := map[int64]struct{}{}
	for _, item := range req.Items {
		doc, ok := docByID[item.DocumentID]
		if !ok || doc.AccessHash != docAccess[item.DocumentID] || !doc.IsStickerSetMaterial() {
			return domain.StickerSet{}, nil, domain.ErrStickerSetFileInvalid
		}
		if _, dup := seenDocs[item.DocumentID]; dup {
			return domain.StickerSet{}, nil, domain.ErrStickerSetFileInvalid
		}
		seenDocs[item.DocumentID] = struct{}{}
		emoji := strings.TrimSpace(item.Emoji)
		if err := validateStickerEmoji(emoji); err != nil {
			return domain.StickerSet{}, nil, err
		}
		item.Emoji = emoji
		items = append(items, item)
	}
	if thumbID != 0 {
		thumb, ok := docByID[thumbID]
		if !ok || thumb.AccessHash != thumbAccess {
			return domain.StickerSet{}, nil, domain.ErrStickerSetFileInvalid
		}
	}

	set := domain.StickerSet{
		ID:            randomID(),
		AccessHash:    randomID(),
		ShortName:     shortName,
		Title:         title,
		Kind:          kind,
		Emojis:        kind == domain.StickerSetKindEmoji,
		Masks:         kind == domain.StickerSetKindMasks,
		TextColor:     kind == domain.StickerSetKindEmoji && req.TextColor,
		Creator:       true,
		CreatorUserID: req.CreatorUserID,
		Software:      strings.TrimSpace(req.Software),
	}

	updatedDocs := make([]domain.Document, 0, len(items))
	finalBySourceID := make(map[int64]domain.Document, len(items))
	for _, item := range items {
		doc := docByID[item.DocumentID]
		doc, err = s.materialDocumentForStickerSet(ctx, doc, set.ID)
		if err != nil {
			return domain.StickerSet{}, nil, err
		}
		doc, err = s.prepareStickerSetDocument(ctx, doc, set, item.Emoji)
		if err != nil {
			return domain.StickerSet{}, nil, err
		}
		set.DocumentIDs = append(set.DocumentIDs, doc.ID)
		set.Packs = addDocumentToStickerPacks(set.Packs, item.Emoji, doc.ID)
		set.Keywords = upsertStickerKeywords(set.Keywords, parseStickerKeywords(doc.ID, item.Keywords))
		finalBySourceID[item.DocumentID] = doc
		updatedDocs = append(updatedDocs, doc)
	}
	set.Count = len(set.DocumentIDs)
	if thumbID != 0 {
		thumb, ok := finalBySourceID[thumbID]
		if !ok {
			thumb, ok = docByID[thumbID]
			if !ok || thumb.AccessHash != thumbAccess {
				return domain.StickerSet{}, nil, domain.ErrStickerSetFileInvalid
			}
		}
		set.ThumbDocumentID = thumb.ID
		set.Thumbs = copyPhotoSizes(thumb.Thumbs)
		set.ThumbDCID = thumb.DCID
		if len(set.Thumbs) > 0 {
			set.ThumbVersion = 1
		}
	}
	set.Hash = stickerSetHash(set)
	if err := s.media.CreateStickerSet(ctx, set, updatedDocs); err != nil {
		if errors.Is(err, domain.ErrStickerSetShortNameOccupied) {
			return domain.StickerSet{}, nil, domain.ErrStickerSetShortNameOccupied
		}
		return domain.StickerSet{}, nil, err
	}
	ordered := orderDocuments(updatedDocs, set.DocumentIDs)
	s.cacheStickerSet(set, ordered)
	return set, ordered, nil
}

func (s *Service) ListCreatedStickerSets(ctx context.Context, userID int64, offsetID int64, limit int) ([]domain.StickerSet, int, error) {
	if userID <= 0 {
		return nil, 0, domain.ErrStickerSetCreatorInvalid
	}
	return s.media.ListStickerSetsByCreator(ctx, userID, offsetID, limit)
}

func (s *Service) cacheStickerSet(set domain.StickerSet, docs []domain.Document) {
	if s.stickerSetNegCache != nil {
		refs := []domain.StickerSetRef{{Kind: domain.StickerSetRefByID, ID: set.ID}}
		if set.ShortName != "" {
			refs = append(refs, domain.StickerSetRef{Kind: domain.StickerSetRefByShortName, ShortName: set.ShortName})
		}
		if set.SystemKey != "" {
			refs = append(refs, domain.StickerSetRef{Kind: domain.StickerSetRefBySystem, SystemKey: set.SystemKey})
		}
		s.stickerSetNegCache.delete(refs...)
	}
	if s.stickerSetCache != nil {
		s.stickerSetCache.put(set, docs)
	}
}

func stickerSetInputDocumentRefs(req domain.CreateStickerSetRequest) ([]int64, map[int64]int64, int64, int64, error) {
	ids := make([]int64, 0, len(req.Items)+1)
	access := make(map[int64]int64, len(req.Items))
	seen := map[int64]struct{}{}
	for _, item := range req.Items {
		if item.DocumentID == 0 || item.DocumentAccessHash == 0 {
			return nil, nil, 0, 0, domain.ErrStickerSetFileInvalid
		}
		if _, ok := seen[item.DocumentID]; !ok {
			ids = append(ids, item.DocumentID)
			seen[item.DocumentID] = struct{}{}
		}
		access[item.DocumentID] = item.DocumentAccessHash
	}
	if req.ThumbDocumentID != 0 {
		if req.ThumbAccessHash == 0 {
			return nil, nil, 0, 0, domain.ErrStickerSetFileInvalid
		}
		if _, ok := seen[req.ThumbDocumentID]; !ok {
			ids = append(ids, req.ThumbDocumentID)
		}
	}
	return ids, access, req.ThumbDocumentID, req.ThumbAccessHash, nil
}

func documentsByID(docs []domain.Document) map[int64]domain.Document {
	out := make(map[int64]domain.Document, len(docs))
	for _, doc := range docs {
		out[doc.ID] = doc
	}
	return out
}

func attachStickerSetToDocument(doc domain.Document, set domain.StickerSet, emoji string) domain.Document {
	want := domain.DocAttrSticker
	if set.Kind == domain.StickerSetKindEmoji || set.Emojis {
		want = domain.DocAttrCustomEmoji
	}
	attrs := append([]domain.DocumentAttribute(nil), doc.Attributes...)
	replaced := false
	for i := range attrs {
		if attrs[i].Kind != domain.DocAttrSticker && attrs[i].Kind != domain.DocAttrCustomEmoji {
			continue
		}
		attrs[i].Kind = want
		attrs[i].Alt = emoji
		attrs[i].StickerSetID = set.ID
		attrs[i].StickerSetAccessHash = set.AccessHash
		attrs[i].Mask = set.Kind == domain.StickerSetKindMasks || set.Masks
		attrs[i].TextColor = set.TextColor
		replaced = true
		break
	}
	if !replaced {
		attrs = append(attrs, domain.DocumentAttribute{
			Kind:                 want,
			Alt:                  emoji,
			Mask:                 set.Kind == domain.StickerSetKindMasks || set.Masks,
			StickerSetID:         set.ID,
			StickerSetAccessHash: set.AccessHash,
			TextColor:            set.TextColor,
		})
	}
	doc.Attributes = attrs
	return doc
}

func (s *Service) prepareStickerSetDocument(ctx context.Context, doc domain.Document, set domain.StickerSet, emoji string) (domain.Document, error) {
	doc, err := s.ensureStickerMaterialShape(ctx, doc)
	if err != nil {
		return domain.Document{}, err
	}
	return attachStickerSetToDocument(doc, set, emoji), nil
}

func (s *Service) ensureStickerMaterialShape(ctx context.Context, doc domain.Document) (domain.Document, error) {
	mimeType := canonicalStickerMaterialMime(doc.StickerSetMaterialMime())
	hasImageSize := false
	hasVideo := false
	for _, attr := range doc.Attributes {
		switch attr.Kind {
		case domain.DocAttrImageSize:
			hasImageSize = true
		case domain.DocAttrVideo:
			hasVideo = true
		}
	}
	switch mimeType {
	case stickerMaterialMimeJSON:
		data, ok := s.readStickerMaterialBlob(ctx, doc)
		if !ok {
			return domain.Document{}, domain.ErrStickerSetFileInvalid
		}
		lottieJSON := normalizeLottieStickerJSON(data)
		if _, _, ok := lottieStickerDimensions(lottieJSON); !ok {
			return domain.Document{}, domain.ErrStickerSetFileInvalid
		}
		tgsData, err := gzipLottieStickerData(lottieJSON)
		if err != nil || int64(len(tgsData)) > domain.MaxStickerMaterialDocumentSize {
			return domain.Document{}, domain.ErrStickerSetFileInvalid
		}
		if err := s.rewriteStickerMaterialBlob(ctx, doc.ID, tgsData, stickerMaterialMimeTGS); err != nil {
			return domain.Document{}, err
		}
		doc.MimeType = stickerMaterialMimeTGS
		doc.Size = int64(len(tgsData))
		doc.Attributes = replaceStickerMaterialFilename(doc.Attributes, "sticker.tgs")
		if !hasImageSize {
			doc.Attributes = append(doc.Attributes, domain.DocumentAttribute{
				Kind: domain.DocAttrImageSize,
				W:    512,
				H:    512,
			})
		}
	case stickerMaterialMimeTGS:
		if data, ok := s.readStickerMaterialBlob(ctx, doc); ok && !validTGSStickerData(data) {
			return domain.Document{}, domain.ErrStickerSetFileInvalid
		}
		var err error
		doc, err = s.ensureStickerMaterialMIME(ctx, doc, stickerMaterialMimeTGS)
		if err != nil {
			return domain.Document{}, err
		}
		if !hasImageSize {
			doc.Attributes = append(doc.Attributes, domain.DocumentAttribute{
				Kind: domain.DocAttrImageSize,
				W:    512,
				H:    512,
			})
		}
	case stickerMaterialMimeWebP:
		var err error
		doc, err = s.ensureStickerMaterialMIME(ctx, doc, stickerMaterialMimeWebP)
		if err != nil {
			return domain.Document{}, err
		}
		if !hasImageSize {
			data, ok := s.readStickerMaterialBlob(ctx, doc)
			if !ok {
				return domain.Document{}, domain.ErrStickerSetFileInvalid
			}
			w, h := imageDimensions(data, 0, 0)
			if w <= 0 || h <= 0 {
				return domain.Document{}, domain.ErrStickerSetFileInvalid
			}
			doc.Attributes = append(doc.Attributes, domain.DocumentAttribute{
				Kind: domain.DocAttrImageSize,
				W:    w,
				H:    h,
			})
		}
	case stickerMaterialMimeWebM, stickerMaterialMimeMP4:
		if !hasVideo {
			return domain.Document{}, domain.ErrStickerSetFileInvalid
		}
		var err error
		doc, err = s.ensureStickerMaterialMIME(ctx, doc, mimeType)
		if err != nil {
			return domain.Document{}, err
		}
	default:
		if doc.IsStickerLike() {
			return doc, nil
		}
		return domain.Document{}, domain.ErrStickerSetFileInvalid
	}
	return doc, nil
}

func (s *Service) readStickerMaterialBlob(ctx context.Context, doc domain.Document) ([]byte, bool) {
	if s == nil || s.media == nil || s.blobs == nil || doc.ID == 0 || doc.Size <= 0 || doc.Size > domain.MaxStickerMaterialDocumentSize {
		return nil, false
	}
	blob, found, err := s.media.GetFileBlob(ctx, fmt.Sprintf("doc:%d", doc.ID))
	if err != nil || !found || blob.Size <= 0 || blob.Size > domain.MaxStickerMaterialDocumentSize {
		return nil, false
	}
	data, total, err := s.blobs.GetRange(ctx, blob.ObjectKey, 0, blob.Size)
	if err != nil || int64(len(data)) != total || total != blob.Size {
		return nil, false
	}
	return data, true
}

func (s *Service) rewriteStickerMaterialBlob(ctx context.Context, docID int64, data []byte, mimeType string) error {
	if s == nil || s.media == nil || s.blobs == nil || docID == 0 || len(data) == 0 || int64(len(data)) > domain.MaxStickerMaterialDocumentSize {
		return domain.ErrStickerSetFileInvalid
	}
	objectKey, err := s.blobs.Put(ctx, data)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(data)
	blob := domain.FileBlob{
		LocationKey: fmt.Sprintf("doc:%d", docID),
		Backend:     domain.MediaBackend(s.blobs.Name()),
		ObjectKey:   objectKey,
		Size:        int64(len(data)),
		SHA256:      append([]byte(nil), sum[:]...),
		MimeType:    mimeType,
	}
	if err := s.media.PutFileBlob(ctx, blob); err != nil {
		return err
	}
	if s.blobCache != nil {
		s.blobCache.put(blob.LocationKey, blob)
	}
	if s.byteCache != nil {
		s.byteCache.put(blob.ObjectKey, data)
	}
	return nil
}

func replaceStickerMaterialFilename(attrs []domain.DocumentAttribute, fallback string) []domain.DocumentAttribute {
	out := append([]domain.DocumentAttribute(nil), attrs...)
	for i := range out {
		if out[i].Kind != domain.DocAttrFilename {
			continue
		}
		out[i].FileName = tgsFileName(out[i].FileName, fallback)
		return out
	}
	return append(out, domain.DocumentAttribute{
		Kind:     domain.DocAttrFilename,
		FileName: fallback,
	})
}

func tgsFileName(fileName, fallback string) string {
	fileName = strings.TrimSpace(fileName)
	if fileName == "" {
		return fallback
	}
	ext := filepath.Ext(fileName)
	if ext == "" {
		return fileName + ".tgs"
	}
	return strings.TrimSuffix(fileName, ext) + ".tgs"
}

func validTGSStickerData(data []byte) bool {
	if len(data) == 0 {
		return false
	}
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return false
	}
	defer gz.Close()
	data, err = io.ReadAll(io.LimitReader(gz, domain.MaxStickerMaterialDocumentSize+1))
	if err != nil || int64(len(data)) > domain.MaxStickerMaterialDocumentSize {
		return false
	}
	_, _, ok := lottieStickerDimensions(normalizeLottieStickerJSON(data))
	return ok
}

func normalizeLottieStickerJSON(data []byte) []byte {
	data = bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF})
	return bytes.TrimSpace(data)
}

func lottieStickerDimensions(data []byte) (int, int, bool) {
	if len(data) == 0 || int64(len(data)) > domain.MaxStickerMaterialDocumentSize {
		return 0, 0, false
	}
	var root struct {
		Version string `json:"v"`
		W       int    `json:"w"`
		H       int    `json:"h"`
	}
	if err := json.NewDecoder(bytes.NewReader(data)).Decode(&root); err != nil {
		return 0, 0, false
	}
	return root.W, root.H, root.Version != "" && root.W > 0 && root.H > 0
}

func gzipLottieStickerData(data []byte) ([]byte, error) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	if _, err := gz.Write(data); err != nil {
		_ = gz.Close()
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func normalizeStickerSetKind(kind domain.StickerSetKind) domain.StickerSetKind {
	switch kind {
	case domain.StickerSetKindEmoji, domain.StickerSetKindMasks:
		return kind
	default:
		return domain.StickerSetKindStickers
	}
}

func validateStickerSetTitle(title string) error {
	if title == "" || utf8.RuneCountInString(title) > domain.MaxStickerSetTitleLen {
		return domain.ErrStickerSetTitleInvalid
	}
	return nil
}

func normalizeStickerSetShortName(shortName string) string {
	return strings.ToLower(strings.TrimSpace(shortName))
}

func validateStickerSetShortName(shortName string) error {
	if len(shortName) < domain.MinStickerSetShortNameLen || len(shortName) > domain.MaxStickerSetShortNameLen {
		return domain.ErrStickerSetShortNameInvalid
	}
	prevUnderscore := false
	for i := 0; i < len(shortName); i++ {
		ch := shortName[i]
		switch {
		case ch >= 'a' && ch <= 'z':
		case ch >= '0' && ch <= '9':
			if i == 0 {
				return domain.ErrStickerSetShortNameInvalid
			}
		case ch == '_':
			if i == 0 || i == len(shortName)-1 || prevUnderscore {
				return domain.ErrStickerSetShortNameInvalid
			}
			prevUnderscore = true
			continue
		default:
			return domain.ErrStickerSetShortNameInvalid
		}
		prevUnderscore = false
	}
	return nil
}

func validateStickerEmoji(emoji string) error {
	if emoji == "" || utf8.RuneCountInString(emoji) > 64 {
		return domain.ErrStickerSetEmojiInvalid
	}
	return nil
}

func stickerSetShortNameBase(title string) string {
	var b strings.Builder
	prevUnderscore := false
	for _, r := range strings.ToLower(title) {
		var out rune
		switch {
		case r >= 'a' && r <= 'z':
			out = r
		case r >= '0' && r <= '9':
			out = r
		case unicode.IsSpace(r) || r == '-' || r == '_':
			out = '_'
		default:
			continue
		}
		if out == '_' {
			if b.Len() == 0 || prevUnderscore {
				continue
			}
			prevUnderscore = true
		} else {
			prevUnderscore = false
		}
		b.WriteRune(out)
		if b.Len() >= domain.MaxStickerSetShortNameLen {
			break
		}
	}
	base := strings.Trim(b.String(), "_")
	if base == "" || base[0] < 'a' || base[0] > 'z' {
		base = "stickers_" + base
	}
	base = strings.Trim(base, "_")
	if len(base) < domain.MinStickerSetShortNameLen {
		base += "_pack"
	}
	return trimStickerSetShortNameBase(base, 0)
}

func trimStickerSetShortNameBase(base string, suffixReserve int) string {
	max := domain.MaxStickerSetShortNameLen - suffixReserve
	if max < domain.MinStickerSetShortNameLen {
		max = domain.MinStickerSetShortNameLen
	}
	if len(base) <= max {
		return strings.Trim(base, "_")
	}
	return strings.Trim(base[:max], "_")
}

func userIDSuffix(userID int64) string {
	if userID <= 0 {
		return ""
	}
	return itoaSmall(int(userID % 10000))
}

func itoaSmall(v int) string {
	if v == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[i:])
}

func parseStickerKeywords(documentID int64, raw string) domain.StickerKeyword {
	parts := strings.Split(raw, ",")
	seen := map[string]struct{}{}
	keywords := make([]string, 0, len(parts))
	for _, part := range parts {
		kw := strings.ToLower(strings.TrimSpace(part))
		if kw == "" || utf8.RuneCountInString(kw) > domain.MaxStickerSetKeywordLen {
			continue
		}
		if _, ok := seen[kw]; ok {
			continue
		}
		seen[kw] = struct{}{}
		keywords = append(keywords, kw)
		if len(keywords) >= domain.MaxStickerSetKeywords {
			break
		}
	}
	return domain.StickerKeyword{DocumentID: documentID, Keywords: keywords}
}

func stickerSetHash(set domain.StickerSet) int {
	h := fnv.New32a()
	writeHashString(h, set.ShortName)
	writeHashString(h, set.Title)
	writeHashString(h, string(set.Kind))
	var buf [8]byte
	for _, id := range set.DocumentIDs {
		binary.LittleEndian.PutUint64(buf[:], uint64(id))
		_, _ = h.Write(buf[:])
	}
	for _, pack := range set.Packs {
		writeHashString(h, pack.Emoticon)
		for _, id := range pack.DocumentIDs {
			binary.LittleEndian.PutUint64(buf[:], uint64(id))
			_, _ = h.Write(buf[:])
		}
	}
	sum := int(h.Sum32() & 0x7fffffff)
	if sum == 0 {
		return 1
	}
	return sum
}

func writeHashString(h interface{ Write([]byte) (int, error) }, s string) {
	_, _ = h.Write([]byte(s))
	_, _ = h.Write([]byte{0})
}
