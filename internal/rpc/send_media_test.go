package rpc

import (
	"context"
	"errors"
	"sort"
	"strings"
	"testing"

	"github.com/gotd/td/clock"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
	"go.uber.org/zap/zaptest"

	appmessages "telesrv/internal/app/messages"
	apppolls "telesrv/internal/app/polls"
	appstories "telesrv/internal/app/stories"
	appusers "telesrv/internal/app/users"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

// fakeFiles 是 FilesService 的最小测试替身：贴纸文档可解析，上传图片返回固定 Photo。
type fakeFiles struct {
	docs               map[int64]domain.Document
	photos             map[int64]domain.Photo
	profile            map[fakeProfilePhotoKey]int64
	reactions          []domain.AvailableReaction
	effects            []domain.AvailableEffect
	sets               map[domain.StickerSetKind][]domain.StickerSet
	profilePhotos      []domain.Photo
	profilePhotosTotal int
	lastProfileOffset  int
	lastProfileLimit   int
	lastProfileMaxID   int64
	resolveWebPageFn   func(string) (domain.MessageWebPage, error)
	lookupWebPageFn    func(string) (domain.MessageWebPage, bool)
	webPagePreviewOn   bool
}

type fakeProfilePhotoKey struct {
	ownerType domain.PeerType
	ownerID   int64
	kind      domain.ProfilePhotoKind
}

func (f *fakeFiles) putPhoto(photo domain.Photo) domain.Photo {
	if f.photos == nil {
		f.photos = map[int64]domain.Photo{}
	}
	f.photos[photo.ID] = photo
	return photo
}

func (f *fakeFiles) SaveFilePart(context.Context, int64, int64, int, []byte) (bool, error) {
	return true, nil
}
func (f *fakeFiles) SaveBigFilePart(context.Context, int64, int64, int, int, []byte) (bool, error) {
	return true, nil
}
func (f *fakeFiles) GetFile(context.Context, domain.FileDownloadRequest) (domain.FileChunk, bool, error) {
	return domain.FileChunk{}, false, nil
}
func (f *fakeFiles) CreateEncryptedFileFromUpload(context.Context, domain.UploadedFileRef, int) (domain.EncryptedFileRef, error) {
	return domain.EncryptedFileRef{ID: 9001, AccessHash: 9002, Size: 16, DCID: 2, KeyFingerprint: 7}, nil
}
func (f *fakeFiles) GeoMapTile(lat, long float64, w, h, zoom, scale int) ([]byte, string) {
	return []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A, 1, 2, 3, 4}, "image/png"
}
func (f *fakeFiles) ListAvailableReactions(context.Context) ([]domain.AvailableReaction, error) {
	return append([]domain.AvailableReaction(nil), f.reactions...), nil
}
func (f *fakeFiles) AvailableEffects(context.Context) ([]domain.AvailableEffect, int, error) {
	hash := 0
	for _, e := range f.effects {
		hash = hash*31 + int(e.ID&0x7fffffff)
	}
	return append([]domain.AvailableEffect(nil), f.effects...), hash & 0x7fffffff, nil
}
func (f *fakeFiles) GetDocuments(_ context.Context, ids []int64) ([]domain.Document, error) {
	out := make([]domain.Document, 0, len(ids))
	for _, id := range ids {
		if d, ok := f.docs[id]; ok {
			out = append(out, d)
		}
	}
	return out, nil
}
func (f *fakeFiles) ResolveStickerSet(_ context.Context, ref domain.StickerSetRef) (domain.StickerSet, []domain.Document, bool, error) {
	for _, sets := range f.sets {
		for _, set := range sets {
			if set.Deleted {
				continue
			}
			match := false
			switch ref.Kind {
			case domain.StickerSetRefByID:
				match = set.ID == ref.ID
			case domain.StickerSetRefByShortName:
				match = set.ShortName == ref.ShortName
			case domain.StickerSetRefBySystem:
				match = set.SystemKey == ref.SystemKey
			}
			if !match {
				continue
			}
			docs := make([]domain.Document, 0, len(set.DocumentIDs))
			for _, id := range set.DocumentIDs {
				if doc, ok := f.docs[id]; ok {
					docs = append(docs, doc)
				}
			}
			return set, docs, true, nil
		}
	}
	return domain.StickerSet{}, nil, false, nil
}
func (f *fakeFiles) ListStickerSets(_ context.Context, kind domain.StickerSetKind) ([]domain.StickerSet, error) {
	sets := f.sets[kind]
	return append([]domain.StickerSet(nil), sets...), nil
}
func (f *fakeFiles) CheckStickerSetShortName(_ context.Context, shortName string) (bool, error) {
	if !validTestStickerShortName(shortName) {
		return false, domain.ErrStickerSetShortNameInvalid
	}
	for _, sets := range f.sets {
		for _, set := range sets {
			if set.ShortName != "" && strings.EqualFold(set.ShortName, shortName) && !set.Deleted {
				return false, nil
			}
		}
	}
	return true, nil
}
func validTestStickerShortName(shortName string) bool {
	shortName = strings.ToLower(strings.TrimSpace(shortName))
	if len(shortName) < domain.MinStickerSetShortNameLen || len(shortName) > domain.MaxStickerSetShortNameLen {
		return false
	}
	for i := 0; i < len(shortName); i++ {
		ch := shortName[i]
		if (ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9' && i > 0) || (ch == '_' && i > 0 && i < len(shortName)-1) {
			continue
		}
		return false
	}
	return true
}
func (f *fakeFiles) SuggestStickerSetShortName(ctx context.Context, title string, userID int64) (string, error) {
	base := strings.ToLower(strings.TrimSpace(title))
	base = strings.ReplaceAll(base, " ", "_")
	if base == "" {
		base = "stickers"
	}
	if len(base) < domain.MinStickerSetShortNameLen {
		base += "_pack"
	}
	if len(base) > domain.MaxStickerSetShortNameLen {
		base = strings.Trim(base[:domain.MaxStickerSetShortNameLen], "_")
	}
	candidates := []string{base, base + "_pack", base + "_2"}
	for _, c := range candidates {
		if ok, err := f.CheckStickerSetShortName(ctx, c); err != nil {
			continue
		} else if ok {
			return c, nil
		}
	}
	return "", domain.ErrStickerSetShortNameOccupied
}
func (f *fakeFiles) CreateStickerSet(_ context.Context, req domain.CreateStickerSetRequest) (domain.StickerSet, []domain.Document, error) {
	if f.sets == nil {
		f.sets = map[domain.StickerSetKind][]domain.StickerSet{}
	}
	if f.docs == nil {
		f.docs = map[int64]domain.Document{}
	}
	if strings.TrimSpace(req.Title) == "" {
		return domain.StickerSet{}, nil, domain.ErrStickerSetTitleInvalid
	}
	if len(req.Items) == 0 {
		return domain.StickerSet{}, nil, domain.ErrStickerSetEmpty
	}
	shortName := strings.ToLower(strings.TrimSpace(req.ShortName))
	if shortName == "" {
		shortName = "created_pack"
	}
	if ok, err := f.CheckStickerSetShortName(context.Background(), shortName); err != nil {
		return domain.StickerSet{}, nil, err
	} else if !ok {
		return domain.StickerSet{}, nil, domain.ErrStickerSetShortNameOccupied
	}
	kind := req.Kind
	if kind == "" {
		kind = domain.StickerSetKindStickers
	}
	docIDs := make([]int64, 0, len(req.Items))
	packs := []domain.StickerPack{}
	keywords := []domain.StickerKeyword{}
	docs := make([]domain.Document, 0, len(req.Items))
	for _, item := range req.Items {
		doc, ok := f.docs[item.DocumentID]
		if !ok || doc.AccessHash != item.DocumentAccessHash || !doc.IsStickerSetMaterial() {
			return domain.StickerSet{}, nil, domain.ErrStickerSetFileInvalid
		}
		if strings.TrimSpace(item.Emoji) == "" {
			return domain.StickerSet{}, nil, domain.ErrStickerSetEmojiInvalid
		}
		docIDs = append(docIDs, item.DocumentID)
		packs = append(packs, domain.StickerPack{Emoticon: item.Emoji, DocumentIDs: []int64{item.DocumentID}})
		if item.Keywords != "" {
			keywords = append(keywords, domain.StickerKeyword{DocumentID: item.DocumentID, Keywords: []string{strings.TrimSpace(item.Keywords)}})
		}
		doc.Attributes = []domain.DocumentAttribute{{Kind: domain.DocAttrSticker, Alt: item.Emoji, StickerSetID: 9000, StickerSetAccessHash: 9001}}
		if kind == domain.StickerSetKindEmoji {
			doc.Attributes[0].Kind = domain.DocAttrCustomEmoji
			doc.Attributes[0].TextColor = req.TextColor
		}
		f.docs[item.DocumentID] = doc
		docs = append(docs, doc)
	}
	set := domain.StickerSet{
		ID:            9000 + int64(len(f.sets[kind])),
		AccessHash:    9001 + int64(len(f.sets[kind])),
		ShortName:     shortName,
		Title:         req.Title,
		Kind:          kind,
		Emojis:        kind == domain.StickerSetKindEmoji,
		Masks:         kind == domain.StickerSetKindMasks,
		TextColor:     kind == domain.StickerSetKindEmoji && req.TextColor,
		Creator:       true,
		CreatorUserID: req.CreatorUserID,
		Count:         len(docIDs),
		Hash:          77 + len(f.sets[kind]),
		DocumentIDs:   docIDs,
		Packs:         packs,
		Keywords:      keywords,
	}
	f.sets[kind] = append(f.sets[kind], set)
	return set, docs, nil
}
func (f *fakeFiles) ListCreatedStickerSets(_ context.Context, userID int64, offsetID int64, limit int) ([]domain.StickerSet, int, error) {
	var all []domain.StickerSet
	for _, sets := range f.sets {
		for _, set := range sets {
			if set.CreatorUserID == userID && !set.Deleted {
				set.Creator = true
				all = append(all, set)
			}
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].ID > all[j].ID })
	total := len(all)
	if offsetID != 0 {
		filtered := all[:0]
		for _, set := range all {
			if set.ID < offsetID {
				filtered = append(filtered, set)
			}
		}
		all = filtered
	}
	if limit > 0 && len(all) > limit {
		all = all[:limit]
	}
	return all, total, nil
}
func (f *fakeFiles) AddStickerToSet(_ context.Context, actorUserID int64, ref domain.StickerSetRef, item domain.StickerSetItemInput) (domain.StickerSet, []domain.Document, error) {
	kind, idx, ok := f.fakeStickerSetIndex(ref)
	if !ok {
		return domain.StickerSet{}, nil, domain.ErrStickerSetInvalid
	}
	set := f.sets[kind][idx]
	if set.CreatorUserID != actorUserID {
		return domain.StickerSet{}, nil, domain.ErrStickerSetNotOwned
	}
	doc, ok := f.docs[item.DocumentID]
	if !ok || doc.AccessHash != item.DocumentAccessHash || !doc.IsStickerSetMaterial() {
		return domain.StickerSet{}, nil, domain.ErrStickerSetFileInvalid
	}
	if setID, _, ok := doc.StickerSetRef(); ok && setID != 0 && setID != set.ID {
		return domain.StickerSet{}, nil, domain.ErrStickerSetFileInvalid
	}
	if fakeContainsInt64(set.DocumentIDs, doc.ID) {
		return set, f.fakeStickerSetDocs(set), nil
	}
	emoji := strings.TrimSpace(item.Emoji)
	if emoji == "" {
		return domain.StickerSet{}, nil, domain.ErrStickerSetEmojiInvalid
	}
	doc = fakeAttachStickerSet(doc, set, emoji)
	f.docs[doc.ID] = doc
	set.DocumentIDs = append(set.DocumentIDs, doc.ID)
	set.Count = len(set.DocumentIDs)
	set.Packs = fakeAddStickerPackDoc(set.Packs, emoji, doc.ID)
	if kw := strings.TrimSpace(item.Keywords); kw != "" {
		set.Keywords = fakeUpsertStickerKeyword(set.Keywords, domain.StickerKeyword{DocumentID: doc.ID, Keywords: []string{kw}})
	}
	set.Hash++
	f.sets[kind][idx] = set
	return set, f.fakeStickerSetDocs(set), nil
}
func (f *fakeFiles) RemoveStickerFromSet(_ context.Context, actorUserID int64, documentID int64, accessHash int64) (domain.StickerSet, []domain.Document, error) {
	doc, ok := f.docs[documentID]
	if !ok || doc.AccessHash != accessHash || !doc.IsStickerLike() {
		return domain.StickerSet{}, nil, domain.ErrStickerSetFileInvalid
	}
	setID, setAccessHash, ok := doc.StickerSetRef()
	if !ok {
		return domain.StickerSet{}, nil, domain.ErrStickerSetFileInvalid
	}
	kind, idx, ok := f.fakeStickerSetIndex(domain.StickerSetRef{Kind: domain.StickerSetRefByID, ID: setID, AccessHash: setAccessHash})
	if !ok {
		return domain.StickerSet{}, nil, domain.ErrStickerSetInvalid
	}
	set := f.sets[kind][idx]
	if set.CreatorUserID != actorUserID {
		return domain.StickerSet{}, nil, domain.ErrStickerSetNotOwned
	}
	pos := fakeIndexInt64(set.DocumentIDs, documentID)
	if pos < 0 {
		return domain.StickerSet{}, nil, domain.ErrStickerSetFileInvalid
	}
	set.DocumentIDs = append(append([]int64(nil), set.DocumentIDs[:pos]...), set.DocumentIDs[pos+1:]...)
	set.Count = len(set.DocumentIDs)
	set.Packs = fakeRemoveStickerPackDoc(set.Packs, documentID)
	set.Keywords = fakeRemoveStickerKeyword(set.Keywords, documentID)
	set.Hash++
	doc = fakeDetachStickerSet(doc)
	f.docs[doc.ID] = doc
	f.sets[kind][idx] = set
	return set, f.fakeStickerSetDocs(set), nil
}
func (f *fakeFiles) ChangeStickerPosition(_ context.Context, actorUserID int64, documentID int64, accessHash int64, position int) (domain.StickerSet, []domain.Document, error) {
	doc, ok := f.docs[documentID]
	if !ok || doc.AccessHash != accessHash || !doc.IsStickerLike() {
		return domain.StickerSet{}, nil, domain.ErrStickerSetFileInvalid
	}
	setID, setAccessHash, ok := doc.StickerSetRef()
	if !ok {
		return domain.StickerSet{}, nil, domain.ErrStickerSetFileInvalid
	}
	kind, idx, ok := f.fakeStickerSetIndex(domain.StickerSetRef{Kind: domain.StickerSetRefByID, ID: setID, AccessHash: setAccessHash})
	if !ok {
		return domain.StickerSet{}, nil, domain.ErrStickerSetInvalid
	}
	set := f.sets[kind][idx]
	if set.CreatorUserID != actorUserID {
		return domain.StickerSet{}, nil, domain.ErrStickerSetNotOwned
	}
	from := fakeIndexInt64(set.DocumentIDs, documentID)
	if from < 0 {
		return domain.StickerSet{}, nil, domain.ErrStickerSetFileInvalid
	}
	if position < 0 || position >= len(set.DocumentIDs) {
		return domain.StickerSet{}, nil, domain.ErrStickerSetPositionInvalid
	}
	set.DocumentIDs = fakeMoveInt64(set.DocumentIDs, from, position)
	set.Hash++
	f.sets[kind][idx] = set
	return set, f.fakeStickerSetDocs(set), nil
}
func (f *fakeFiles) RenameStickerSet(_ context.Context, actorUserID int64, ref domain.StickerSetRef, title string) (domain.StickerSet, []domain.Document, error) {
	kind, idx, ok := f.fakeStickerSetIndex(ref)
	if !ok {
		return domain.StickerSet{}, nil, domain.ErrStickerSetInvalid
	}
	set := f.sets[kind][idx]
	if set.CreatorUserID != actorUserID {
		return domain.StickerSet{}, nil, domain.ErrStickerSetNotOwned
	}
	title = strings.TrimSpace(title)
	if title == "" {
		return domain.StickerSet{}, nil, domain.ErrStickerSetTitleInvalid
	}
	set.Title = title
	set.Hash++
	f.sets[kind][idx] = set
	return set, f.fakeStickerSetDocs(set), nil
}
func (f *fakeFiles) DeleteStickerSet(_ context.Context, actorUserID int64, ref domain.StickerSetRef) (domain.StickerSetKind, error) {
	kind, idx, ok := f.fakeStickerSetIndex(ref)
	if !ok {
		return "", domain.ErrStickerSetInvalid
	}
	set := f.sets[kind][idx]
	if set.CreatorUserID != actorUserID {
		return "", domain.ErrStickerSetNotOwned
	}
	set.Deleted = true
	f.sets[kind][idx] = set
	return kind, nil
}
func (f *fakeFiles) fakeStickerSetIndex(ref domain.StickerSetRef) (domain.StickerSetKind, int, bool) {
	for kind, sets := range f.sets {
		for idx, set := range sets {
			if set.Deleted {
				continue
			}
			switch ref.Kind {
			case domain.StickerSetRefByID:
				if set.ID == ref.ID && (ref.AccessHash == 0 || set.AccessHash == ref.AccessHash) {
					return kind, idx, true
				}
			case domain.StickerSetRefByShortName:
				if strings.EqualFold(set.ShortName, ref.ShortName) {
					return kind, idx, true
				}
			case domain.StickerSetRefBySystem:
				if set.SystemKey == ref.SystemKey {
					return kind, idx, true
				}
			}
		}
	}
	return "", 0, false
}
func (f *fakeFiles) fakeStickerSetDocs(set domain.StickerSet) []domain.Document {
	out := make([]domain.Document, 0, len(set.DocumentIDs))
	for _, id := range set.DocumentIDs {
		if doc, ok := f.docs[id]; ok {
			out = append(out, doc)
		}
	}
	return out
}
func fakeAttachStickerSet(doc domain.Document, set domain.StickerSet, emoji string) domain.Document {
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
		attrs[i].TextColor = set.TextColor
		replaced = true
		break
	}
	if !replaced {
		attrs = append(attrs, domain.DocumentAttribute{Kind: want, Alt: emoji, StickerSetID: set.ID, StickerSetAccessHash: set.AccessHash, TextColor: set.TextColor})
	}
	doc.Attributes = attrs
	return doc
}
func fakeDetachStickerSet(doc domain.Document) domain.Document {
	attrs := append([]domain.DocumentAttribute(nil), doc.Attributes...)
	for i := range attrs {
		if attrs[i].Kind == domain.DocAttrSticker || attrs[i].Kind == domain.DocAttrCustomEmoji {
			attrs[i].StickerSetID = 0
			attrs[i].StickerSetAccessHash = 0
			attrs[i].TextColor = false
			break
		}
	}
	doc.Attributes = attrs
	return doc
}
func fakeAddStickerPackDoc(packs []domain.StickerPack, emoji string, documentID int64) []domain.StickerPack {
	out := append([]domain.StickerPack(nil), packs...)
	for i := range out {
		out[i].DocumentIDs = append([]int64(nil), out[i].DocumentIDs...)
		if out[i].Emoticon == emoji {
			if !fakeContainsInt64(out[i].DocumentIDs, documentID) {
				out[i].DocumentIDs = append(out[i].DocumentIDs, documentID)
			}
			return out
		}
	}
	return append(out, domain.StickerPack{Emoticon: emoji, DocumentIDs: []int64{documentID}})
}
func fakeRemoveStickerPackDoc(packs []domain.StickerPack, documentID int64) []domain.StickerPack {
	out := make([]domain.StickerPack, 0, len(packs))
	for _, pack := range packs {
		ids := make([]int64, 0, len(pack.DocumentIDs))
		for _, id := range pack.DocumentIDs {
			if id != documentID {
				ids = append(ids, id)
			}
		}
		if len(ids) != 0 {
			out = append(out, domain.StickerPack{Emoticon: pack.Emoticon, DocumentIDs: ids})
		}
	}
	return out
}
func fakeUpsertStickerKeyword(in []domain.StickerKeyword, keyword domain.StickerKeyword) []domain.StickerKeyword {
	out := fakeRemoveStickerKeyword(in, keyword.DocumentID)
	return append(out, keyword)
}
func fakeRemoveStickerKeyword(in []domain.StickerKeyword, documentID int64) []domain.StickerKeyword {
	out := make([]domain.StickerKeyword, 0, len(in))
	for _, kw := range in {
		if kw.DocumentID != documentID {
			out = append(out, kw)
		}
	}
	return out
}
func fakeContainsInt64(in []int64, value int64) bool {
	return fakeIndexInt64(in, value) >= 0
}
func fakeIndexInt64(in []int64, value int64) int {
	for i, v := range in {
		if v == value {
			return i
		}
	}
	return -1
}
func fakeMoveInt64(in []int64, from, to int) []int64 {
	out := append([]int64(nil), in...)
	value := out[from]
	out = append(out[:from], out[from+1:]...)
	if to >= len(out) {
		return append(out, value)
	}
	out = append(out[:to], append([]int64{value}, out[to:]...)...)
	return out
}
func (f *fakeFiles) CreatePhotoFromUpload(_ context.Context, _ domain.UploadedFileRef) (domain.Photo, error) {
	photo := domain.Photo{ID: 777, AccessHash: 7, DCID: 2, Sizes: []domain.PhotoSize{{Kind: domain.PhotoSizeKindDefault, Type: "x", W: 800, H: 600}}}
	return f.putPhoto(photo), nil
}
func (f *fakeFiles) CreatePhotoFromBytes(_ context.Context, data []byte) (domain.Photo, error) {
	photo := domain.Photo{
		ID:         8300 + int64(len(f.photos)),
		AccessHash: 83,
		DCID:       2,
		Sizes:      []domain.PhotoSize{{Kind: domain.PhotoSizeKindDefault, Type: "x", W: 320, H: 200, Size: len(data)}},
	}
	return f.putPhoto(photo), nil
}
func (f *fakeFiles) CreateAvatarFromUpload(_ context.Context, _ domain.UploadedFileRef) (domain.Photo, error) {
	photo := domain.Photo{ID: 778, AccessHash: 7, DCID: 2, Sizes: []domain.PhotoSize{{Kind: domain.PhotoSizeKindDefault, Type: "a", W: 160, H: 160}, {Kind: domain.PhotoSizeKindDefault, Type: "c", W: 640, H: 640}}}
	return f.putPhoto(photo), nil
}
func (f *fakeFiles) CreateAvatarVideoFromUpload(_ context.Context, _ domain.UploadedFileRef, videoStartTs float64) (domain.Photo, error) {
	photo := domain.Photo{ID: 779, AccessHash: 7, DCID: 2, Sizes: append(fakeAvatarStaticSizes(), domain.PhotoSize{Kind: domain.PhotoSizeKindVideo, Type: "u", W: 640, H: 640, Size: 1024, VideoStartTs: videoStartTs})}
	return f.putPhoto(photo), nil
}
func (f *fakeFiles) CreateAvatarVideoMarkupFromUpload(_ context.Context, _ domain.UploadedFileRef, videoStartTs float64, markup domain.PhotoSize) (domain.Photo, error) {
	sizes := append(fakeAvatarStaticSizes(), domain.PhotoSize{Kind: domain.PhotoSizeKindVideo, Type: "u", W: 640, H: 640, Size: 1024, VideoStartTs: videoStartTs})
	sizes = append(sizes, markup)
	photo := domain.Photo{ID: 781, AccessHash: 7, DCID: 2, Sizes: sizes}
	return f.putPhoto(photo), nil
}
func (f *fakeFiles) CreateAvatarMarkup(_ context.Context, size domain.PhotoSize) (domain.Photo, error) {
	photo := domain.Photo{ID: 780, AccessHash: 7, DCID: 2, Sizes: append(fakeAvatarStaticSizes(), size)}
	return f.putPhoto(photo), nil
}

func fakeAvatarStaticSizes() []domain.PhotoSize {
	return []domain.PhotoSize{
		{Kind: domain.PhotoSizeKindDefault, Type: "a", W: 160, H: 160, Size: 1024},
		{Kind: domain.PhotoSizeKindDefault, Type: "c", W: 640, H: 640, Size: 1024},
	}
}
func (f *fakeFiles) CreateDocumentFromUpload(_ context.Context, _ domain.UploadedFileRef, spec domain.DocumentSpec) (domain.Document, error) {
	return domain.Document{ID: 888, AccessHash: 8, DCID: 2, MimeType: spec.MimeType, Attributes: spec.Attributes}, nil
}
func (f *fakeFiles) CreateDocumentFromBytes(_ context.Context, data []byte, spec domain.DocumentSpec) (domain.Document, error) {
	doc := domain.Document{
		ID:         8400 + int64(len(f.docs)),
		AccessHash: 84,
		DCID:       2,
		MimeType:   spec.MimeType,
		Size:       int64(len(data)),
		Attributes: append([]domain.DocumentAttribute(nil), spec.Attributes...),
	}
	if f.docs == nil {
		f.docs = map[int64]domain.Document{}
	}
	f.docs[doc.ID] = doc
	return doc, nil
}
func (f *fakeFiles) CreatePhotoFromURL(_ context.Context, rawURL string) (domain.Photo, error) {
	if rawURL == "" {
		return domain.Photo{}, domain.ErrPhotoInvalid
	}
	return f.putPhoto(domain.Photo{ID: 9100, AccessHash: 91, DCID: 2, Sizes: []domain.PhotoSize{{Kind: domain.PhotoSizeKindDefault, Type: "x", W: 320, H: 200}}}), nil
}

func (f *fakeFiles) ResolveWebPage(_ context.Context, rawURL string) (domain.MessageWebPage, error) {
	if f.resolveWebPageFn != nil {
		return f.resolveWebPageFn(rawURL)
	}
	return domain.MessageWebPage{}, errors.New("web page preview unavailable")
}

func (f *fakeFiles) WebPagePreviewEnabled() bool { return f.webPagePreviewOn }

func (f *fakeFiles) LookupWebPage(_ context.Context, rawURL string) (domain.MessageWebPage, bool) {
	if f.lookupWebPageFn != nil {
		return f.lookupWebPageFn(rawURL)
	}
	return domain.MessageWebPage{}, false
}

func (f *fakeFiles) CreateDocumentFromURL(_ context.Context, rawURL string) (domain.Document, error) {
	if rawURL == "" {
		return domain.Document{}, domain.ErrDocumentInvalid
	}
	doc := domain.Document{ID: 9200, AccessHash: 92, DCID: 2, MimeType: "image/jpeg", Attributes: []domain.DocumentAttribute{{Kind: domain.DocAttrFilename, FileName: "ext.jpg"}}}
	if f.docs == nil {
		f.docs = map[int64]domain.Document{}
	}
	f.docs[doc.ID] = doc
	return doc, nil
}

func (f *fakeFiles) GetPhoto(_ context.Context, id int64) (domain.Photo, bool, error) {
	p, ok := f.photos[id]
	return p, ok, nil
}
func (f *fakeFiles) GetDocument(_ context.Context, id int64) (domain.Document, bool, error) {
	d, ok := f.docs[id]
	return d, ok, nil
}
func (f *fakeFiles) UploadProfilePhoto(ctx context.Context, ownerType domain.PeerType, ownerID int64, file domain.UploadedFileRef, date int) (domain.Photo, error) {
	return f.UploadProfilePhotoKind(ctx, ownerType, ownerID, domain.ProfilePhotoKindProfile, file, date)
}
func (f *fakeFiles) UploadProfilePhotoKind(_ context.Context, ownerType domain.PeerType, ownerID int64, kind domain.ProfilePhotoKind, _ domain.UploadedFileRef, _ int) (domain.Photo, error) {
	photo, _ := f.CreateAvatarFromUpload(context.Background(), domain.UploadedFileRef{})
	if f.profile == nil {
		f.profile = map[fakeProfilePhotoKey]int64{}
	}
	f.profile[fakeProfilePhotoKey{ownerType: ownerType, ownerID: ownerID, kind: kind}] = photo.ID
	return photo, nil
}
func (f *fakeFiles) SetCurrentProfilePhoto(ctx context.Context, ownerType domain.PeerType, ownerID, photoID int64, date int) (domain.Photo, bool, error) {
	return f.SetCurrentProfilePhotoKind(ctx, ownerType, ownerID, domain.ProfilePhotoKindProfile, photoID, date)
}
func (f *fakeFiles) SetCurrentProfilePhotoKind(_ context.Context, ownerType domain.PeerType, ownerID int64, kind domain.ProfilePhotoKind, photoID int64, _ int) (domain.Photo, bool, error) {
	photo, ok := f.photos[photoID]
	if !ok {
		return domain.Photo{}, false, nil
	}
	if f.profile == nil {
		f.profile = map[fakeProfilePhotoKey]int64{}
	}
	f.profile[fakeProfilePhotoKey{ownerType: ownerType, ownerID: ownerID, kind: kind}] = photoID
	return photo, true, nil
}
func (f *fakeFiles) CurrentProfilePhoto(ctx context.Context, ownerType domain.PeerType, ownerID int64) (domain.Photo, bool, error) {
	return f.CurrentProfilePhotoKind(ctx, ownerType, ownerID, domain.ProfilePhotoKindProfile)
}
func (f *fakeFiles) CurrentProfilePhotoKind(_ context.Context, ownerType domain.PeerType, ownerID int64, kind domain.ProfilePhotoKind) (domain.Photo, bool, error) {
	photoID := f.profile[fakeProfilePhotoKey{ownerType: ownerType, ownerID: ownerID, kind: kind}]
	if photoID == 0 {
		return domain.Photo{}, false, nil
	}
	photo, ok := f.photos[photoID]
	return photo, ok, nil
}
func (f *fakeFiles) CurrentProfilePhotos(ctx context.Context, ownerType domain.PeerType, ownerIDs []int64) (map[int64]domain.ProfilePhotoRef, error) {
	return f.CurrentProfilePhotosKind(ctx, ownerType, ownerIDs, domain.ProfilePhotoKindProfile)
}
func (f *fakeFiles) CurrentProfilePhotosKind(_ context.Context, ownerType domain.PeerType, ownerIDs []int64, kind domain.ProfilePhotoKind) (map[int64]domain.ProfilePhotoRef, error) {
	out := make(map[int64]domain.ProfilePhotoRef, len(ownerIDs))
	for _, ownerID := range ownerIDs {
		photoID := f.profile[fakeProfilePhotoKey{ownerType: ownerType, ownerID: ownerID, kind: kind}]
		if photoID == 0 {
			continue
		}
		photo, ok := f.photos[photoID]
		if !ok {
			continue
		}
		out[ownerID] = domain.ProfilePhotoRef{
			PhotoID:  photo.ID,
			DCID:     photo.DCID,
			Stripped: domain.StrippedFromSizes(photo.Sizes),
			HasVideo: domain.PhotoHasVideo(photo.Sizes),
		}
	}
	return out, nil
}
func (f *fakeFiles) GetProfilePhotos(_ context.Context, _ domain.PeerType, _ int64, offset, limit int, maxID int64) ([]domain.Photo, int, error) {
	f.lastProfileOffset = offset
	f.lastProfileLimit = limit
	f.lastProfileMaxID = maxID
	return append([]domain.Photo(nil), f.profilePhotos...), f.profilePhotosTotal, nil
}
func (f *fakeFiles) GetProfilePhotosKind(_ context.Context, _ domain.PeerType, _ int64, _ domain.ProfilePhotoKind, offset, limit int, maxID int64) ([]domain.Photo, int, error) {
	f.lastProfileOffset = offset
	f.lastProfileLimit = limit
	f.lastProfileMaxID = maxID
	return append([]domain.Photo(nil), f.profilePhotos...), f.profilePhotosTotal, nil
}
func (f *fakeFiles) DeleteProfilePhotos(ctx context.Context, ownerType domain.PeerType, ownerID int64, photoIDs []int64) (int, error) {
	return f.DeleteProfilePhotosKind(ctx, ownerType, ownerID, domain.ProfilePhotoKindProfile, photoIDs)
}
func (f *fakeFiles) DeleteProfilePhotosKind(_ context.Context, ownerType domain.PeerType, ownerID int64, kind domain.ProfilePhotoKind, photoIDs []int64) (int, error) {
	deleted := 0
	key := fakeProfilePhotoKey{ownerType: ownerType, ownerID: ownerID, kind: kind}
	for _, id := range photoIDs {
		if _, ok := f.photos[id]; !ok {
			continue
		}
		deleted++
		if f.profile[key] == id {
			delete(f.profile, key)
		}
	}
	return deleted, nil
}

func newMediaTestRouter(t *testing.T) (*Router, domain.User, domain.User) {
	t.Helper()
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, _ := userStore.Create(ctx, domain.User{AccessHash: 11, Phone: "15550009001", FirstName: "Owner"})
	friend, _ := userStore.Create(ctx, domain.User{AccessHash: 12, Phone: "15550009002", FirstName: "Friend"})
	dialogStore := memory.NewDialogStore()
	messageStore := memory.NewMessageStore(dialogStore)
	pollStore := memory.NewPollStore()
	messageStore.AttachPollStore(pollStore)
	files := &fakeFiles{
		docs: map[int64]domain.Document{
			555: {
				ID:         555,
				AccessHash: 5,
				DCID:       2,
				MimeType:   "application/x-tgsticker",
				Attributes: []domain.DocumentAttribute{{Kind: domain.DocAttrSticker, Alt: "\U0001f600", StickerSetID: 99, StickerSetAccessHash: 7}},
			},
		},
		photos: map[int64]domain.Photo{},
	}
	r := New(Config{DC: 2, IP: "127.0.0.1", Port: 2398}, Deps{
		Users:    appusers.NewService(userStore),
		Messages: appmessages.NewService(messageStore, dialogStore),
		Files:    files,
		Polls:    apppolls.NewService(pollStore),
		Sessions: &captureSessions{},
	}, zaptest.NewLogger(t), clock.System)
	return r, owner, friend
}

func newMessageFromUpdates(t *testing.T, updates tg.UpdatesClass) *tg.Message {
	t.Helper()
	upd, ok := updates.(*tg.Updates)
	if !ok {
		t.Fatalf("expected *tg.Updates, got %T", updates)
	}
	for _, u := range upd.Updates {
		if nm, ok := u.(*tg.UpdateNewMessage); ok {
			msg, ok := nm.Message.(*tg.Message)
			if !ok {
				t.Fatalf("expected *tg.Message, got %T", nm.Message)
			}
			return msg
		}
		if nm, ok := u.(*tg.UpdateNewChannelMessage); ok {
			msg, ok := nm.Message.(*tg.Message)
			if !ok {
				t.Fatalf("expected channel *tg.Message, got %T", nm.Message)
			}
			return msg
		}
	}
	t.Fatal("no new message update found")
	return nil
}

func assertMessageMediaStory(t *testing.T, media tg.MessageMediaClass, wantUserID int64, wantStoryID int, wantEmbedded bool) {
	t.Helper()
	storyMedia, ok := media.(*tg.MessageMediaStory)
	if !ok {
		t.Fatalf("message media = %T, want *tg.MessageMediaStory", media)
	}
	peer, ok := storyMedia.Peer.(*tg.PeerUser)
	if !ok || peer.UserID != wantUserID {
		t.Fatalf("story media peer = %T %+v, want user %d", storyMedia.Peer, storyMedia.Peer, wantUserID)
	}
	if storyMedia.ID != wantStoryID {
		t.Fatalf("story media id = %d, want %d", storyMedia.ID, wantStoryID)
	}
	story, hasStory := storyMedia.GetStory()
	if hasStory != wantEmbedded {
		t.Fatalf("story media embedded = %v, want %v", hasStory, wantEmbedded)
	}
	if wantEmbedded {
		item, ok := story.(*tg.StoryItem)
		if !ok || item.ID != wantStoryID {
			t.Fatalf("embedded story = %T %+v, want story id %d", story, story, wantStoryID)
		}
	}
}

func TestSendMediaPrivateSticker(t *testing.T) {
	ctx := context.Background()
	r, owner, friend := newMediaTestRouter(t)

	updates, err := r.onMessagesSendMedia(WithUserID(ctx, owner.ID), &tg.MessagesSendMediaRequest{
		Peer:     &tg.InputPeerUser{UserID: friend.ID, AccessHash: friend.AccessHash},
		Media:    &tg.InputMediaDocument{ID: &tg.InputDocument{ID: 555, AccessHash: 5}},
		RandomID: 1001,
	})
	if err != nil {
		t.Fatalf("sendMedia sticker: %v", err)
	}
	msg := newMessageFromUpdates(t, updates)
	media, ok := msg.Media.(*tg.MessageMediaDocument)
	if !ok {
		t.Fatalf("expected MessageMediaDocument, got %T", msg.Media)
	}
	if !media.Nopremium {
		t.Fatal("sticker message media missing nopremium flag")
	}
	doc, ok := media.Document.(*tg.Document)
	if !ok {
		t.Fatalf("expected tg.Document, got %T", media.Document)
	}
	if want := int64(555); doc.ID != want {
		t.Errorf("document id = %d, want %d", doc.ID, want)
	}
	if doc.DCID != 2 {
		t.Errorf("document dc_id = %d, want 2", doc.DCID)
	}
	hasSticker := false
	for _, a := range doc.Attributes {
		if _, ok := a.(*tg.DocumentAttributeSticker); ok {
			hasSticker = true
		}
	}
	if !hasSticker {
		t.Error("document missing sticker attribute")
	}
}

func TestSendMultiMediaPartialFailureSubsetRetryKeepsReservedGroupedID(t *testing.T) {
	ctx := context.Background()
	r, owner, friend := newMediaTestRouter(t)
	files := r.deps.Files.(*fakeFiles)

	first := tg.InputSingleMedia{
		Media:    &tg.InputMediaDocument{ID: &tg.InputDocument{ID: 555, AccessHash: 5}},
		RandomID: 41001,
		Message:  "first",
	}
	second := tg.InputSingleMedia{
		// 首次请求时 556 尚不存在，使第一条已提交后第二条解析失败。
		Media:    &tg.InputMediaDocument{ID: &tg.InputDocument{ID: 556, AccessHash: 6}},
		RandomID: 41002,
		Message:  "second",
	}
	peer := &tg.InputPeerUser{UserID: friend.ID, AccessHash: friend.AccessHash}
	if _, err := r.onMessagesSendMultiMedia(WithUserID(ctx, owner.ID), &tg.MessagesSendMultiMediaRequest{
		Peer:       peer,
		MultiMedia: []tg.InputSingleMedia{first, second},
	}); err == nil || !tgerr.Is(err, "MEDIA_INVALID") {
		t.Fatalf("partial album err=%v, want MEDIA_INVALID after first item commit", err)
	}

	files.docs[556] = domain.Document{ID: 556, AccessHash: 6, DCID: 2, MimeType: "image/jpeg"}
	retry, err := r.onMessagesSendMultiMedia(WithUserID(ctx, owner.ID), &tg.MessagesSendMultiMediaRequest{
		Peer:       peer,
		MultiMedia: []tg.InputSingleMedia{second},
	})
	if err != nil {
		t.Fatalf("retry failed subset: %v", err)
	}
	retryMessage := newMessageFromUpdates(t, retry)
	retryGroup, ok := retryMessage.GetGroupedID()
	if !ok || retryGroup == 0 {
		t.Fatalf("retry grouped_id = %d present=%v, want non-zero reservation", retryGroup, ok)
	}

	history, err := r.deps.Messages.GetHistory(ctx, owner.ID, domain.MessageFilter{
		HasPeer: true,
		Peer:    domain.Peer{Type: domain.PeerTypeUser, ID: friend.ID},
		Limit:   10,
	})
	if err != nil {
		t.Fatalf("album history: %v", err)
	}
	groups := make(map[int64]int64, 2)
	for _, message := range history.Messages {
		if message.RandomID == first.RandomID || message.RandomID == second.RandomID {
			groups[message.RandomID] = message.GroupedID
		}
	}
	if len(groups) != 2 || groups[first.RandomID] != retryGroup || groups[second.RandomID] != retryGroup {
		t.Fatalf("history album groups=%v, want both %d", groups, retryGroup)
	}

	changed := second
	changed.Message = "changed durable intent"
	if _, err := r.onMessagesSendMultiMedia(WithUserID(ctx, owner.ID), &tg.MessagesSendMultiMediaRequest{
		Peer:       peer,
		MultiMedia: []tg.InputSingleMedia{changed},
	}); err == nil || !tgerr.Is(err, "RANDOM_ID_DUPLICATE") {
		t.Fatalf("changed reserved item err=%v, want RANDOM_ID_DUPLICATE", err)
	}
}

func TestSendMultiMediaChannelPartialFailureSubsetRetryKeepsReservedGroupedID(t *testing.T) {
	f := newRPCChannelFixture(t)
	owner := f.user(51, "15550009401", "AlbumOwner")
	member := f.user(52, "15550009402", "AlbumMember")
	channel := f.createLegacyMegagroup(owner, "Album Group", member)
	messageStore := memory.NewMessageStore()
	f.router.deps.Messages = appmessages.NewService(messageStore, nil)
	files := &fakeFiles{docs: map[int64]domain.Document{
		555: {ID: 555, AccessHash: 5, DCID: 2, MimeType: "image/jpeg"},
	}, photos: map[int64]domain.Photo{}}
	f.router.deps.Files = files

	first := tg.InputSingleMedia{
		Media: &tg.InputMediaDocument{ID: &tg.InputDocument{ID: 555, AccessHash: 5}}, RandomID: 42001, Message: "first",
	}
	second := tg.InputSingleMedia{
		Media: &tg.InputMediaDocument{ID: &tg.InputDocument{ID: 556, AccessHash: 6}}, RandomID: 42002, Message: "second",
	}
	peer := inputPeerChannel(channel)
	if _, err := f.router.onMessagesSendMultiMedia(f.userCtx(owner), &tg.MessagesSendMultiMediaRequest{
		Peer: peer, MultiMedia: []tg.InputSingleMedia{first, second},
	}); err == nil || !tgerr.Is(err, "MEDIA_INVALID") {
		t.Fatalf("partial channel album err=%v, want MEDIA_INVALID", err)
	}
	files.docs[556] = domain.Document{ID: 556, AccessHash: 6, DCID: 2, MimeType: "image/jpeg"}
	retry, err := f.router.onMessagesSendMultiMedia(f.userCtx(owner), &tg.MessagesSendMultiMediaRequest{
		Peer: peer, MultiMedia: []tg.InputSingleMedia{second},
	})
	if err != nil {
		t.Fatalf("retry channel subset: %v", err)
	}
	retryMessage := newMessageFromUpdates(t, retry)
	retryGroup, ok := retryMessage.GetGroupedID()
	if !ok || retryGroup == 0 {
		t.Fatalf("channel retry grouped_id=%d present=%v, want non-zero", retryGroup, ok)
	}
	history, err := f.router.deps.Channels.GetHistory(f.ctx, owner.ID, domain.ChannelHistoryFilter{
		ChannelID: channel.ID,
		Limit:     10,
	})
	if err != nil {
		t.Fatalf("channel album history: %v", err)
	}
	groups := make(map[int64]int64, 2)
	for _, message := range history.Messages {
		if message.RandomID == first.RandomID || message.RandomID == second.RandomID {
			groups[message.RandomID] = message.GroupedID
		}
	}
	if len(groups) != 2 || groups[first.RandomID] != retryGroup || groups[second.RandomID] != retryGroup {
		t.Fatalf("channel album groups=%v, want both %d", groups, retryGroup)
	}
}

func TestTGMessageMediaDocumentMarksHistoricalStickerNopremium(t *testing.T) {
	media := tgMessageMedia(&domain.MessageMedia{
		Kind: domain.MessageMediaKindDocument,
		Document: &domain.Document{
			ID:         555,
			AccessHash: 5,
			MimeType:   "application/x-tgsticker",
			Attributes: []domain.DocumentAttribute{
				{Kind: domain.DocAttrImageSize, W: 512, H: 512},
				{Kind: domain.DocAttrSticker, Alt: "🙂", StickerSetID: 10, StickerSetAccessHash: 20},
			},
		},
	})
	docMedia, ok := media.(*tg.MessageMediaDocument)
	if !ok {
		t.Fatalf("media = %T, want *tg.MessageMediaDocument", media)
	}
	if !docMedia.Nopremium {
		t.Fatal("historical sticker message media missing nopremium flag")
	}
}

func TestSendMediaPrivateUploadedPhoto(t *testing.T) {
	ctx := context.Background()
	r, owner, friend := newMediaTestRouter(t)

	updates, err := r.onMessagesSendMedia(WithUserID(ctx, owner.ID), &tg.MessagesSendMediaRequest{
		Peer:     &tg.InputPeerUser{UserID: friend.ID, AccessHash: friend.AccessHash},
		Media:    &tg.InputMediaUploadedPhoto{File: &tg.InputFile{ID: 42, Parts: 1, Name: "p.jpg"}},
		Message:  "caption",
		RandomID: 1002,
	})
	if err != nil {
		t.Fatalf("sendMedia photo: %v", err)
	}
	msg := newMessageFromUpdates(t, updates)
	if msg.Message != "caption" {
		t.Errorf("caption = %q, want %q", msg.Message, "caption")
	}
	media, ok := msg.Media.(*tg.MessageMediaPhoto)
	if !ok {
		t.Fatalf("expected MessageMediaPhoto, got %T", msg.Media)
	}
	photo, ok := media.Photo.(*tg.Photo)
	if !ok {
		t.Fatalf("expected tg.Photo, got %T", media.Photo)
	}
	if photo.ID != 777 {
		t.Errorf("photo id = %d, want 777", photo.ID)
	}
}

func TestSendMediaInputMediaStoryStoresMessageMediaStory(t *testing.T) {
	ctx := context.Background()
	r, owner, friend := newMediaTestRouter(t)
	storyStore := memory.NewStoryStore()
	r.deps.Stories = appstories.NewService(storyStore)
	ownerPeer := domain.Peer{Type: domain.PeerTypeUser, ID: owner.ID}
	if _, err := storyStore.UpsertStory(ctx, domain.UpsertStoryRequest{Story: domain.Story{
		Owner:      ownerPeer,
		ID:         7,
		Date:       1700000001,
		ExpireDate: 1700003600,
		Public:     true,
		Caption:    "story source",
		Media:      &domain.MessageMedia{Kind: domain.MessageMediaKindPhoto, Photo: &domain.Photo{ID: 771, AccessHash: 77, DCID: 2}},
	}}); err != nil {
		t.Fatalf("upsert story: %v", err)
	}

	updates, err := r.onMessagesSendMedia(WithUserID(ctx, owner.ID), &tg.MessagesSendMediaRequest{
		Peer:     &tg.InputPeerUser{UserID: friend.ID, AccessHash: friend.AccessHash},
		Media:    &tg.InputMediaStory{Peer: &tg.InputPeerSelf{}, ID: 7},
		RandomID: 10021,
	})
	if err != nil {
		t.Fatalf("sendMedia story: %v", err)
	}
	msg := newMessageFromUpdates(t, updates)
	assertMessageMediaStory(t, msg.Media, owner.ID, 7, true)

	got, err := r.onMessagesGetMessages(WithUserID(ctx, owner.ID), []tg.InputMessageClass{&tg.InputMessageID{ID: msg.ID}})
	if err != nil {
		t.Fatalf("get story message: %v", err)
	}
	box, ok := got.(*tg.MessagesMessages)
	if !ok || len(box.Messages) != 1 {
		t.Fatalf("get story message = %T %+v, want one messages.messages", got, got)
	}
	stored, ok := box.Messages[0].(*tg.Message)
	if !ok {
		t.Fatalf("stored story message = %T, want *tg.Message", box.Messages[0])
	}
	assertMessageMediaStory(t, stored.Media, owner.ID, 7, true)
}

func TestSendMediaInputMediaStoryRejectsNoForwardsSource(t *testing.T) {
	ctx := context.Background()
	r, owner, friend := newMediaTestRouter(t)
	storyStore := memory.NewStoryStore()
	r.deps.Stories = appstories.NewService(storyStore)
	ownerPeer := domain.Peer{Type: domain.PeerTypeUser, ID: owner.ID}
	if _, err := storyStore.UpsertStory(ctx, domain.UpsertStoryRequest{Story: domain.Story{
		Owner:      ownerPeer,
		ID:         8,
		Date:       1700000002,
		ExpireDate: 1700003600,
		Public:     true,
		NoForwards: true,
	}}); err != nil {
		t.Fatalf("upsert noforwards story: %v", err)
	}

	_, err := r.onMessagesSendMedia(WithUserID(ctx, owner.ID), &tg.MessagesSendMediaRequest{
		Peer:     &tg.InputPeerUser{UserID: friend.ID, AccessHash: friend.AccessHash},
		Media:    &tg.InputMediaStory{Peer: &tg.InputPeerSelf{}, ID: 8},
		RandomID: 10022,
	})
	if err == nil || !tgerr.Is(err, "CHAT_FORWARDS_RESTRICTED") {
		t.Fatalf("sendMedia noforwards story err = %v, want CHAT_FORWARDS_RESTRICTED", err)
	}
}

func TestSendMediaPrivateContact(t *testing.T) {
	ctx := context.Background()
	r, owner, friend := newMediaTestRouter(t)
	r.deps.Files = nil

	updates, err := r.onMessagesSendMedia(WithUserID(ctx, owner.ID), &tg.MessagesSendMediaRequest{
		Peer: &tg.InputPeerUser{UserID: friend.ID, AccessHash: friend.AccessHash},
		Media: &tg.InputMediaContact{
			PhoneNumber: "+1 (555) 000-9002",
			FirstName:   "Bob",
			LastName:    "Shared",
			Vcard:       "BEGIN:VCARD\nFN:Bob Shared\nEND:VCARD",
		},
		RandomID: 1003,
	})
	if err != nil {
		t.Fatalf("sendMedia contact: %v", err)
	}
	upd := updates.(*tg.Updates)
	msg := newMessageFromUpdates(t, updates)
	media, ok := msg.Media.(*tg.MessageMediaContact)
	if !ok {
		t.Fatalf("expected MessageMediaContact, got %T", msg.Media)
	}
	if media.PhoneNumber != "+1 (555) 000-9002" || media.FirstName != "Bob" || media.LastName != "Shared" || media.Vcard == "" {
		t.Fatalf("contact media = %+v, want preserved contact payload", media)
	}
	if media.UserID != friend.ID {
		t.Fatalf("contact user_id = %d, want %d", media.UserID, friend.ID)
	}
	foundFriend := false
	for _, u := range upd.Users {
		if got, ok := u.(*tg.User); ok && got.ID == friend.ID {
			foundFriend = true
		}
	}
	if !foundFriend {
		t.Fatalf("updates users = %#v, want shared contact user", upd.Users)
	}
}

func TestUploadMediaContactUnregistered(t *testing.T) {
	ctx := context.Background()
	r, owner, _ := newMediaTestRouter(t)

	media, err := r.onMessagesUploadMedia(WithUserID(ctx, owner.ID), &tg.MessagesUploadMediaRequest{
		Peer: &tg.InputPeerEmpty{},
		Media: &tg.InputMediaContact{
			PhoneNumber: "+19990000000",
			FirstName:   "External",
			LastName:    "Contact",
		},
	})
	if err != nil {
		t.Fatalf("uploadMedia contact: %v", err)
	}
	contact, ok := media.(*tg.MessageMediaContact)
	if !ok {
		t.Fatalf("expected MessageMediaContact, got %T", media)
	}
	if contact.UserID != 0 {
		t.Fatalf("unregistered contact user_id = %d, want 0", contact.UserID)
	}
	if contact.FirstName != "External" || contact.LastName != "Contact" {
		t.Fatalf("contact media = %+v, want external contact", contact)
	}
}

func TestUploadMediaReturnsReusableMedia(t *testing.T) {
	ctx := context.Background()
	r, owner, _ := newMediaTestRouter(t)

	media, err := r.onMessagesUploadMedia(WithUserID(ctx, owner.ID), &tg.MessagesUploadMediaRequest{
		Peer:  &tg.InputPeerEmpty{},
		Media: &tg.InputMediaDocument{ID: &tg.InputDocument{ID: 555, AccessHash: 5}},
	})
	if err != nil {
		t.Fatalf("uploadMedia: %v", err)
	}
	if _, ok := media.(*tg.MessageMediaDocument); !ok {
		t.Fatalf("expected MessageMediaDocument, got %T", media)
	}
}

func TestStickerSetDoesNotExposeUnserviceableDownloadThumb(t *testing.T) {
	set := tgStickerSet(domain.StickerSet{
		ID:           99,
		AccessHash:   7,
		Title:        "Set",
		ShortName:    "set",
		ThumbDCID:    2,
		ThumbVersion: 123,
		Thumbs: []domain.PhotoSize{
			{Kind: domain.PhotoSizeKindPath, Type: "j", Bytes: []byte{1, 2, 3}},
			{Kind: domain.PhotoSizeKindDefault, Type: "a", W: 100, H: 100, Size: 4096},
		},
	})
	thumbs, ok := set.GetThumbs()
	if !ok || len(thumbs) != 1 {
		t.Fatalf("thumbs = %#v, want only non-downloadable path thumb", thumbs)
	}
	if _, ok := thumbs[0].(*tg.PhotoPathSize); !ok {
		t.Fatalf("thumb[0] = %T, want PhotoPathSize", thumbs[0])
	}
}
