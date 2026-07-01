package files

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"telesrv/internal/domain"
)

// fakeMediaStore 是 store.MediaStore 的内存替身，用于在无 PG 时验证 seed 导入器。
type fakeMediaStore struct {
	mu        sync.Mutex
	blobs     map[string]domain.FileBlob
	docs      map[int64]domain.Document
	photos    map[int64]domain.Photo
	sets      map[int64]domain.StickerSet
	reactions []domain.AvailableReaction
	parts     map[string][]domain.UploadPart
	webPages  map[int64]domain.MessageWebPage
	seedState map[string]string
}

func newFakeMediaStore() *fakeMediaStore {
	return &fakeMediaStore{
		blobs:     map[string]domain.FileBlob{},
		docs:      map[int64]domain.Document{},
		photos:    map[int64]domain.Photo{},
		sets:      map[int64]domain.StickerSet{},
		parts:     map[string][]domain.UploadPart{},
		seedState: map[string]string{},
	}
}

func (f *fakeMediaStore) SaveFilePart(_ context.Context, part domain.UploadPart) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := fakeUploadPartKey(part.OwnerUserID, part.FileID)
	part.SHA256 = append([]byte(nil), part.SHA256...)
	parts := f.parts[key]
	for i := range parts {
		if parts[i].Part == part.Part {
			parts[i] = part
			f.parts[key] = parts
			return nil
		}
	}
	f.parts[key] = append(parts, part)
	return nil
}
func (f *fakeMediaStore) UploadPartUsage(_ context.Context, ownerUserID int64) (domain.UploadPartUsage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var usage domain.UploadPartUsage
	files := map[int64]struct{}{}
	for _, parts := range f.parts {
		for _, p := range parts {
			if p.OwnerUserID != ownerUserID {
				continue
			}
			usage.Bytes += p.Size
			usage.Parts++
			files[p.FileID] = struct{}{}
		}
	}
	usage.Files = len(files)
	return usage, nil
}
func (f *fakeMediaStore) UploadPartSlot(_ context.Context, ownerUserID, fileID int64, part int) (domain.UploadPartSlot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	parts := f.parts[fakeUploadPartKey(ownerUserID, fileID)]
	slot := domain.UploadPartSlot{FileParts: len(parts)}
	for _, p := range parts {
		if p.Part == part {
			slot.ExistingBytes = p.Size
			slot.ObjectKey = p.ObjectKey
			slot.Found = true
			break
		}
	}
	return slot, nil
}
func (f *fakeMediaStore) LoadFileParts(_ context.Context, ownerUserID, fileID int64) ([]domain.UploadPart, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	parts := append([]domain.UploadPart(nil), f.parts[fakeUploadPartKey(ownerUserID, fileID)]...)
	sort.Slice(parts, func(i, j int) bool { return parts[i].Part < parts[j].Part })
	return parts, nil
}
func (f *fakeMediaStore) DeleteFileParts(_ context.Context, ownerUserID, fileID int64) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := fakeUploadPartKey(ownerUserID, fileID)
	parts := f.parts[key]
	keys := make([]string, 0, len(parts))
	for _, p := range parts {
		keys = append(keys, p.ObjectKey)
	}
	delete(f.parts, key)
	return keys, nil
}
func (f *fakeMediaStore) DeleteExpiredUploadParts(_ context.Context, _ time.Time, _ int) ([]string, error) {
	return nil, nil
}

func fakeUploadPartKey(ownerUserID, fileID int64) string {
	return fmt.Sprintf("%d:%d", ownerUserID, fileID)
}

func (f *fakeMediaStore) PutFileBlob(_ context.Context, blob domain.FileBlob) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.blobs[blob.LocationKey] = blob
	return nil
}
func (f *fakeMediaStore) GetFileBlob(_ context.Context, key string) (domain.FileBlob, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, ok := f.blobs[key]
	return b, ok, nil
}

func (f *fakeMediaStore) GetFileBlobs(_ context.Context, keys []string) (map[string]domain.FileBlob, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make(map[string]domain.FileBlob, len(keys))
	for _, key := range keys {
		if b, ok := f.blobs[key]; ok {
			out[key] = b
		}
	}
	return out, nil
}

func (f *fakeMediaStore) GetSeedState(_ context.Context, key string) (string, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	hash, ok := f.seedState[key]
	return hash, ok, nil
}

func (f *fakeMediaStore) PutSeedState(_ context.Context, key, hash string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seedState[key] = hash
	return nil
}

func (f *fakeMediaStore) PutDocument(_ context.Context, doc domain.Document) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.docs[doc.ID] = doc
	return nil
}
func (f *fakeMediaStore) GetDocument(_ context.Context, id int64) (domain.Document, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	d, ok := f.docs[id]
	return d, ok, nil
}
func (f *fakeMediaStore) GetDocuments(_ context.Context, ids []int64) ([]domain.Document, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]domain.Document, 0, len(ids))
	for _, id := range ids {
		if d, ok := f.docs[id]; ok {
			out = append(out, d)
		}
	}
	return out, nil
}
func (f *fakeMediaStore) PutPhoto(_ context.Context, p domain.Photo) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.photos[p.ID] = p
	return nil
}
func (f *fakeMediaStore) GetPhoto(_ context.Context, id int64) (domain.Photo, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.photos[id]
	return p, ok, nil
}
func (f *fakeMediaStore) PutWebPage(_ context.Context, urlHash int64, page domain.MessageWebPage, _ int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.webPages == nil {
		f.webPages = map[int64]domain.MessageWebPage{}
	}
	f.webPages[urlHash] = page
	return nil
}
func (f *fakeMediaStore) GetWebPageByURLHash(_ context.Context, urlHash int64) (domain.MessageWebPage, int, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.webPages[urlHash]
	return p, 0, ok, nil
}

func (f *fakeMediaStore) PutStickerSet(_ context.Context, set domain.StickerSet) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sets[set.ID] = set
	return nil
}
func (f *fakeMediaStore) CreateStickerSet(_ context.Context, set domain.StickerSet, docs []domain.Document) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, existing := range f.sets {
		if existing.ShortName != "" && strings.EqualFold(existing.ShortName, set.ShortName) {
			return domain.ErrStickerSetShortNameOccupied
		}
	}
	f.sets[set.ID] = set
	if f.docs == nil {
		f.docs = map[int64]domain.Document{}
	}
	for _, doc := range docs {
		f.docs[doc.ID] = doc
	}
	return nil
}
func (f *fakeMediaStore) UpdateStickerSet(_ context.Context, set domain.StickerSet, docs []domain.Document) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.sets[set.ID]; !ok {
		return domain.ErrStickerSetInvalid
	}
	f.sets[set.ID] = set
	if f.docs == nil {
		f.docs = map[int64]domain.Document{}
	}
	for _, doc := range docs {
		f.docs[doc.ID] = doc
	}
	return nil
}
func (f *fakeMediaStore) DeleteStickerSet(_ context.Context, setID int64, creatorUserID int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	set, ok := f.sets[setID]
	if !ok || set.Deleted || set.CreatorUserID != creatorUserID {
		return domain.ErrStickerSetInvalid
	}
	set.Deleted = true
	f.sets[setID] = set
	return nil
}
func (f *fakeMediaStore) GetStickerSetByID(_ context.Context, id int64) (domain.StickerSet, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.sets[id]
	if ok && s.Deleted {
		return domain.StickerSet{}, false, nil
	}
	return s, ok, nil
}
func (f *fakeMediaStore) GetStickerSetByShortName(_ context.Context, name string) (domain.StickerSet, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, s := range f.sets {
		if strings.EqualFold(s.ShortName, name) && !s.Deleted {
			return s, true, nil
		}
	}
	return domain.StickerSet{}, false, nil
}
func (f *fakeMediaStore) GetStickerSetBySystemKey(_ context.Context, key string) (domain.StickerSet, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, s := range f.sets {
		if s.SystemKey == key {
			return s, true, nil
		}
	}
	return domain.StickerSet{}, false, nil
}
func (f *fakeMediaStore) ListStickerSets(_ context.Context, kind domain.StickerSetKind) ([]domain.StickerSet, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []domain.StickerSet
	for _, s := range f.sets {
		if s.Kind == kind && !s.Deleted {
			out = append(out, s)
		}
	}
	return out, nil
}
func (f *fakeMediaStore) ListStickerSetsByCreator(_ context.Context, creatorUserID int64, offsetID int64, limit int) ([]domain.StickerSet, int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var all []domain.StickerSet
	for _, s := range f.sets {
		if s.CreatorUserID == creatorUserID && !s.Deleted {
			s.Creator = true
			all = append(all, s)
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].ID > all[j].ID })
	total := len(all)
	if offsetID != 0 {
		filtered := all[:0]
		for _, s := range all {
			if s.ID < offsetID {
				filtered = append(filtered, s)
			}
		}
		all = filtered
	}
	if limit > 0 && len(all) > limit {
		all = all[:limit]
	}
	return append([]domain.StickerSet(nil), all...), total, nil
}
func (f *fakeMediaStore) StickerSetShortNameAvailable(_ context.Context, shortName string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, s := range f.sets {
		if s.ShortName != "" && strings.EqualFold(s.ShortName, shortName) && !s.Deleted {
			return false, nil
		}
	}
	return true, nil
}
func (f *fakeMediaStore) CountStickerSets(_ context.Context) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sets), nil
}
func (f *fakeMediaStore) PutAvailableReaction(_ context.Context, r domain.AvailableReaction) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, existing := range f.reactions {
		if existing.Reaction == r.Reaction {
			f.reactions[i] = r
			return nil
		}
	}
	f.reactions = append(f.reactions, r)
	return nil
}
func (f *fakeMediaStore) ListAvailableReactions(_ context.Context) ([]domain.AvailableReaction, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]domain.AvailableReaction(nil), f.reactions...), nil
}
func (f *fakeMediaStore) CountAvailableReactions(_ context.Context) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.reactions), nil
}
func (f *fakeMediaStore) AddProfilePhotoKind(_ context.Context, _ domain.PeerType, _ int64, _ domain.ProfilePhotoKind, _ int64, _ int) error {
	return nil
}
func (f *fakeMediaStore) CurrentProfilePhotoKind(_ context.Context, _ domain.PeerType, _ int64, _ domain.ProfilePhotoKind) (int64, bool, error) {
	return 0, false, nil
}
func (f *fakeMediaStore) CurrentProfilePhotos(_ context.Context, _ domain.PeerType, _ []int64) (map[int64]domain.ProfilePhotoRef, error) {
	return map[int64]domain.ProfilePhotoRef{}, nil
}
func (f *fakeMediaStore) CurrentProfilePhotosKind(_ context.Context, _ domain.PeerType, _ []int64, _ domain.ProfilePhotoKind) (map[int64]domain.ProfilePhotoRef, error) {
	return map[int64]domain.ProfilePhotoRef{}, nil
}
func (f *fakeMediaStore) ListProfilePhotosKind(_ context.Context, _ domain.PeerType, _ int64, _ domain.ProfilePhotoKind, _, _ int, _ int64) ([]int64, int, error) {
	return nil, 0, nil
}
func (f *fakeMediaStore) ListProfilePhotoDetailsKind(_ context.Context, _ domain.PeerType, _ int64, _ domain.ProfilePhotoKind, _, _ int, _ int64) ([]domain.Photo, int, error) {
	return nil, 0, nil
}
func (f *fakeMediaStore) DeleteProfilePhotos(_ context.Context, _ domain.PeerType, _ int64, _ []int64) ([]int64, error) {
	return nil, nil
}
func (f *fakeMediaStore) DeleteProfilePhotosKind(_ context.Context, _ domain.PeerType, _ int64, _ domain.ProfilePhotoKind, _ []int64) ([]int64, error) {
	return nil, nil
}

func TestSeedMediaRepairsPartialReactionBlobs(t *testing.T) {
	seedDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(seedDir, "telegram_reactions_export", "global_json"), 0o755); err != nil {
		t.Fatal(err)
	}
	reactionsDir := filepath.Join(seedDir, "telegram_reactions_export", "reactions")
	if err := os.MkdirAll(reactionsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	raw := `{"result":{"reactions":[{"reaction":"👍","title":"Like","static_icon":{"id":1111111,"access_hash":1,"file_reference":"","date":"2026-06-03T00:00:00Z","mime_type":"image/webp","size":4,"attributes":[],"thumbs":[]},"select_animation":{"id":2222222,"access_hash":2,"file_reference":"","date":"2026-06-03T00:00:00Z","mime_type":"application/x-tgsticker","size":4,"attributes":[],"thumbs":[]}}]}}`
	if err := os.WriteFile(filepath.Join(seedDir, "telegram_reactions_export", "global_json", "available_reactions_raw.json"), []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(reactionsDir, "reaction_thumbs_up_sign_static_icon_Like_1111111.webp"), []byte("webp"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(reactionsDir, "reaction_thumbs_up_sign_static_icon_Like_1111111_thumb1_PhotoSize_types_72x72.jpg"), []byte("jpeg"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(reactionsDir, "reaction_select_2222222.tgs"), []byte("tgs!"), 0o644); err != nil {
		t.Fatal(err)
	}

	media := newFakeMediaStore()
	local, err := NewLocalFS(t.TempDir())
	if err != nil {
		t.Fatalf("local fs: %v", err)
	}
	blobs := &countingBlobBackend{BlobBackend: local}
	svc := NewService(media, blobs, 2)
	if stats, err := svc.SeedMedia(context.Background(), seedDir, 0); err != nil {
		t.Fatalf("initial seed: %v", err)
	} else if stats.Reactions != 1 || stats.Blobs != 3 {
		t.Fatalf("initial stats = %+v, want one reaction and three blobs", stats)
	}
	chunk, ok, err := svc.GetFile(context.Background(), domain.FileDownloadRequest{LocationKey: "doc:2222222", Offset: 0, Limit: 4})
	if err != nil || !ok {
		t.Fatalf("prewarmed getfile ok=%v err=%v", ok, err)
	}
	if string(chunk.Bytes) != "tgs!" {
		t.Fatalf("prewarmed chunk = %q, want tgs!", chunk.Bytes)
	}
	if blobs.getRangeCalls != 0 {
		t.Fatalf("seeded small blob should be served from byte cache, GetRange calls = %d", blobs.getRangeCalls)
	}

	media.mu.Lock()
	delete(media.blobs, "doc:2222222")
	media.mu.Unlock()

	stats, err := svc.SeedMedia(context.Background(), seedDir, 0)
	if err != nil {
		t.Fatalf("repair seed: %v", err)
	}
	if stats.Reactions != 1 || stats.Blobs != 3 || stats.Skipped {
		t.Fatalf("repair stats = %+v, want repair import", stats)
	}
	if _, ok, _ := media.GetFileBlob(context.Background(), "doc:2222222"); !ok {
		t.Fatal("missing reaction blob was not repaired")
	}
	if reactions, _ := media.ListAvailableReactions(context.Background()); len(reactions) != 1 {
		t.Fatalf("reaction upsert duplicated rows: got %d", len(reactions))
	}
}

func TestSeedCustomEmojiTGSWithoutThumbGetsSyntheticPreview(t *testing.T) {
	ctx := context.Background()
	seedDir := t.TempDir()
	const sourceID int64 = 4444444
	writeStatusPackWithoutThumbSeed(t, seedDir, sourceID, 17)

	media := newFakeMediaStore()
	blobs, err := NewLocalFS(t.TempDir())
	if err != nil {
		t.Fatalf("local fs: %v", err)
	}
	svc := NewService(media, blobs, 2)
	stats, err := svc.SeedMedia(ctx, seedDir, 0)
	if err != nil {
		t.Fatalf("seed media: %v", err)
	}
	if stats.StickerSets != 1 || stats.Documents != 1 || stats.Blobs != 2 || stats.Skipped {
		t.Fatalf("stats = %+v, want one set, one doc, main blob plus synthetic preview", stats)
	}

	set, ok, err := media.GetStickerSetByShortName(ctx, "StatusPack")
	if err != nil || !ok {
		t.Fatalf("StatusPack ok=%v err=%v", ok, err)
	}
	doc, ok, err := media.GetDocument(ctx, set.DocumentIDs[0])
	if err != nil || !ok {
		t.Fatalf("StatusPack document ok=%v err=%v", ok, err)
	}
	if !seedDocumentHasAttribute(doc.Attributes, domain.DocAttrCustomEmoji) {
		t.Fatalf("document attributes = %+v, want custom emoji", doc.Attributes)
	}
	thumb, ok := findCachedThumb(doc.Thumbs)
	if !ok {
		t.Fatalf("document thumbs = %+v, want synthetic cached preview", doc.Thumbs)
	}
	if thumb.Type != seedSyntheticDocumentThumbType || thumb.W != 1 || thumb.H != 1 || len(thumb.Bytes) == 0 {
		t.Fatalf("synthetic thumb = %+v, want 1x1 cached %q thumb", thumb, seedSyntheticDocumentThumbType)
	}
	blob, ok, err := media.GetFileBlob(ctx, fmt.Sprintf("doc:%d:%s", doc.ID, thumb.Type))
	if err != nil || !ok {
		t.Fatalf("synthetic thumb blob ok=%v err=%v", ok, err)
	}
	if blob.MimeType != "image/png" {
		t.Fatalf("synthetic thumb blob mime = %q, want image/png", blob.MimeType)
	}
}

func TestSeedMediaRepairsCustomEmojiTGSWithoutThumb(t *testing.T) {
	ctx := context.Background()
	seedDir := t.TempDir()
	const sourceID int64 = 5555555
	const setHash = 23
	writeStatusPackWithoutThumbSeed(t, seedDir, sourceID, setHash)

	media := newFakeMediaStore()
	if err := media.PutDocument(ctx, domain.Document{
		ID:       sourceID,
		MimeType: "application/x-tgsticker",
		Attributes: []domain.DocumentAttribute{{
			Kind:      domain.DocAttrCustomEmoji,
			Alt:       "\U0001f44b",
			TextColor: true,
		}},
	}); err != nil {
		t.Fatalf("put stale document: %v", err)
	}
	if err := media.PutStickerSet(ctx, domain.StickerSet{
		ID:         773947703670341676,
		AccessHash: 1,
		ShortName:  "StatusPack",
		Title:      "Status Pack",
		Hash:       setHash,
		Kind:       domain.StickerSetKindEmoji,
		Emojis:     true,
		DocumentIDs: []int64{
			sourceID,
		},
	}); err != nil {
		t.Fatalf("put stale sticker set: %v", err)
	}

	blobs, err := NewLocalFS(t.TempDir())
	if err != nil {
		t.Fatalf("local fs: %v", err)
	}
	svc := NewService(media, blobs, 2)
	stats, err := svc.SeedMedia(ctx, seedDir, 0)
	if err != nil {
		t.Fatalf("repair seed: %v", err)
	}
	if stats.StickerSets != 1 || stats.Documents != 1 || stats.Blobs != 2 || stats.Skipped {
		t.Fatalf("repair stats = %+v, want forced reimport", stats)
	}
	doc, ok, err := media.GetDocument(ctx, sourceID)
	if err != nil || !ok {
		t.Fatalf("repaired document ok=%v err=%v", ok, err)
	}
	if _, ok := findCachedThumb(doc.Thumbs); !ok {
		t.Fatalf("repaired document thumbs = %+v, want synthetic cached preview", doc.Thumbs)
	}
}

func TestSeedMediaSkipsUnchangedEffectsDocuments(t *testing.T) {
	ctx := context.Background()
	seedDir := t.TempDir()
	const sourceID int64 = 6666666
	writeEffectsSeed(t, seedDir, sourceID)

	media := newFakeMediaStore()
	blobs, err := NewLocalFS(t.TempDir())
	if err != nil {
		t.Fatalf("local fs: %v", err)
	}
	svc := NewService(media, blobs, 2)
	first, err := svc.SeedMedia(ctx, seedDir, 0)
	if err != nil {
		t.Fatalf("first seed: %v", err)
	}
	if first.Effects != 1 || first.Documents != 1 || first.Blobs != 2 {
		t.Fatalf("first stats = %+v, want one imported effect document with main plus synthetic preview blobs", first)
	}

	second, err := svc.SeedMedia(ctx, seedDir, 0)
	if err != nil {
		t.Fatalf("second seed: %v", err)
	}
	if second.Effects != 1 || second.Documents != 0 || second.Blobs != 0 {
		t.Fatalf("second stats = %+v, want effects catalog loaded without document/blob import", second)
	}

	delete(media.blobs, fmt.Sprintf("doc:%d", sourceID))
	repaired, err := svc.SeedMedia(ctx, seedDir, 0)
	if err != nil {
		t.Fatalf("repair seed: %v", err)
	}
	if repaired.Effects != 1 || repaired.Documents != 1 || repaired.Blobs != 2 {
		t.Fatalf("repair stats = %+v, want missing blob to force reimport", repaired)
	}
}

func TestSeedMediaFromRealExport(t *testing.T) {
	seedDir := os.Getenv("TELESRV_REAL_STICKER_SEED_DIR")
	if seedDir == "" {
		t.Skip("TELESRV_REAL_STICKER_SEED_DIR not set")
	}
	if _, err := os.Stat(seedDir); err != nil {
		t.Skipf("seed dir %s not present: %v", seedDir, err)
	}
	media := newFakeMediaStore()
	blobs, err := NewLocalFS(t.TempDir())
	if err != nil {
		t.Fatalf("local fs: %v", err)
	}
	svc := NewService(media, blobs, 2)
	maxRegularSets := 2
	if raw := os.Getenv("TELESRV_REAL_STICKER_SEED_MAX_SETS"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil {
			t.Fatalf("parse TELESRV_REAL_STICKER_SEED_MAX_SETS: %v", err)
		}
		maxRegularSets = n
	}
	stats, err := svc.SeedMedia(context.Background(), seedDir, maxRegularSets)
	if err != nil {
		t.Fatalf("seed media: %v", err)
	}
	t.Logf("seed stats: reactions=%d sets=%d docs=%d blobs=%d", stats.Reactions, stats.StickerSets, stats.Documents, stats.Blobs)
	if stats.Reactions == 0 {
		t.Error("expected reactions imported")
	}
	if stats.StickerSets == 0 {
		t.Error("expected sticker sets imported")
	}
	if stats.Documents == 0 {
		t.Error("expected documents imported")
	}
	if stats.Blobs == 0 {
		t.Error("expected blobs imported")
	}

	// reaction 引用的文档应能被解析回真实 document（带 sticker 属性 + 主体 blob）。
	reactions, _ := media.ListAvailableReactions(context.Background())
	if len(reactions) == 0 {
		t.Fatal("no reactions stored")
	}
	first := reactions[0]
	if first.Reaction == "" {
		t.Error("reaction emoticon empty")
	}
	if first.StaticIconID == 0 || first.SelectAnimationID == 0 {
		t.Error("reaction missing document ids")
	}
	if d, ok, _ := media.GetDocument(context.Background(), first.SelectAnimationID); !ok {
		t.Error("reaction select animation document missing")
	} else {
		if d.ID > seedExternalDocumentIDOffset {
			t.Errorf("reaction document kept external source id: %d", d.ID)
		}
		if d.DCID != 2 {
			t.Errorf("document dc_id not rewritten: %d", d.DCID)
		}
		if _, ok, _ := media.GetFileBlob(context.Background(), blobKeyDoc(d.ID)); !ok {
			t.Errorf("reaction document %d main blob missing", d.ID)
		}
	}

	// 一个常规贴纸集应有 documents 且能按 short_name 解析。
	for _, s := range media.sets {
		for _, thumb := range s.Thumbs {
			if thumb.Downloadable() {
				t.Fatalf("sticker set %s exposes downloadable cover thumb %q without a serviceable blob", s.ShortName, thumb.Type)
			}
		}
	}

	var sample domain.StickerSet
	for _, s := range media.sets {
		if s.Kind == domain.StickerSetKindStickers && len(s.DocumentIDs) > 0 {
			sample = s
			break
		}
	}
	if sample.ID == 0 {
		t.Fatal("no regular sticker set with documents imported")
	}
	if got, ok, _ := media.GetStickerSetByShortName(context.Background(), sample.ShortName); !ok || got.ID != sample.ID {
		t.Error("sticker set not resolvable by short name")
	}
	if doc, ok, _ := media.GetDocument(context.Background(), sample.DocumentIDs[0]); !ok {
		t.Fatalf("sample sticker document %d missing", sample.DocumentIDs[0])
	} else {
		if doc.ID > seedExternalDocumentIDOffset {
			t.Fatalf("sample sticker kept external source id: %d", doc.ID)
		}
		thumb, ok := findCachedThumb(doc.Thumbs)
		if !ok {
			t.Fatalf("sample sticker document thumbs are not inline cached: %+v", doc.Thumbs)
		}
		blob, ok, err := media.GetFileBlob(context.Background(), blobKeyDoc(doc.ID)+":"+thumb.Type)
		if err != nil || !ok {
			t.Fatalf("sample sticker thumb blob ok=%v err=%v", ok, err)
		}
		if want := seedThumbMimeType(thumb.Bytes); blob.MimeType != want {
			t.Fatalf("sample sticker thumb mime = %q, want %q", blob.MimeType, want)
		}
		if !hasPathThumb(doc.Thumbs) {
			t.Logf("sample sticker document has no exported PhotoPathSize placeholder; synthetic cached preview is present: %+v", doc.Thumbs)
		}
	}
}

func writeStatusPackWithoutThumbSeed(t *testing.T, seedDir string, sourceID int64, setHash int) {
	t.Helper()
	setDir := filepath.Join(seedDir, "telegram_emoji_export", "StatusPack_773947703670341676")
	stickersDir := filepath.Join(setDir, "stickers")
	if err := os.MkdirAll(stickersDir, 0o755); err != nil {
		t.Fatal(err)
	}
	raw := fmt.Sprintf(`{"result":{"set":{"id":773947703670341676,"access_hash":1,"title":"Status Pack","short_name":"StatusPack","count":1,"hash":%d,"emojis":true,"packs":[{"emoticon":"👋","documents":[%d]}]},"packs":[{"emoticon":"👋","documents":[%d]}],"documents":[{"id":%d,"access_hash":2,"file_reference":"","date":"2026-06-29T00:00:00Z","mime_type":"application/x-tgsticker","size":4,"dc_id":4,"attributes":[{"_":"DocumentAttributeImageSize","w":512,"h":512},{"_":"DocumentAttributeCustomEmoji","alt":"👋","text_color":true,"stickerset":{"id":773947703670341676,"access_hash":1}},{"_":"DocumentAttributeFilename","file_name":"AnimatedSticker.tgs"}],"thumbs":[]}]}}`, setHash, sourceID, sourceID, sourceID)
	if err := os.WriteFile(filepath.Join(setDir, "set_info.json"), []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stickersDir, fmt.Sprintf("status_%d.tgs", sourceID)), []byte("tgs!"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeEffectsSeed(t *testing.T, seedDir string, sourceID int64) {
	t.Helper()
	docsDir := filepath.Join(seedDir, "telegram_effects_export", "documents")
	if err := os.MkdirAll(docsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	raw := fmt.Sprintf(`{"result":{"effects":[{"id":77,"emoticon":"🔥","effect_sticker_id":%d}],"documents":[{"id":%d,"access_hash":2,"file_reference":"","date":"2026-06-29T00:00:00Z","mime_type":"application/x-tgsticker","size":4,"dc_id":4,"attributes":[{"_":"DocumentAttributeImageSize","w":512,"h":512},{"_":"DocumentAttributeFilename","file_name":"effect.tgs"}],"thumbs":[]}]}}`, sourceID, sourceID)
	if err := os.WriteFile(filepath.Join(seedDir, "telegram_effects_export", "effects.json"), []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(docsDir, fmt.Sprintf("effect_%d.tgs", sourceID)), []byte("tgs!"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSeedDocumentStorageIDNormalizesExternalIDs(t *testing.T) {
	const sourceID int64 = 5382305375846410902
	const want int64 = 1382305375846410902
	if got := seedDocumentStorageID(sourceID); got != want {
		t.Fatalf("seedDocumentStorageID(%d) = %d, want %d", sourceID, got, want)
	}
	if got := seedDocumentStorageID(2222222); got != 2222222 {
		t.Fatalf("small server id changed: %d", got)
	}
}

func TestSeedStickerSetInstalledFlagNeverMarksViewerState(t *testing.T) {
	cases := []struct {
		name string
		kind domain.StickerSetKind
		want bool
	}{
		{name: "regular stickers", kind: domain.StickerSetKindStickers, want: false},
		{name: "custom emoji", kind: domain.StickerSetKindEmoji, want: false},
		{name: "masks", kind: domain.StickerSetKindMasks, want: false},
		{name: "system resources", kind: domain.StickerSetKindSystem, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := seedStickerSetInstalled(tc.kind); got != tc.want {
				t.Fatalf("seedStickerSetInstalled(%q) = %v, want %v", tc.kind, got, tc.want)
			}
		})
	}
}

func TestSeedInlineCachedDocumentThumb(t *testing.T) {
	input := domain.PhotoSize{Kind: domain.PhotoSizeKindDefault, Type: "m", W: 128, H: 128, Size: 6400}
	got := seedInlineCachedDocumentThumb(input, []byte("jpeg"))
	if got.Kind != domain.PhotoSizeKindCached {
		t.Fatalf("kind = %q, want cached", got.Kind)
	}
	if got.Size != 0 || string(got.Bytes) != "jpeg" {
		t.Fatalf("cached thumb = %+v, want inline bytes without downloadable size", got)
	}
	large := make([]byte, seedInlineCachedDocumentThumbMaxBytes+1)
	if got := seedInlineCachedDocumentThumb(input, large); got.Kind != domain.PhotoSizeKindDefault || got.Size != input.Size || len(got.Bytes) != 0 {
		t.Fatalf("large thumb = %+v, want unchanged downloadable thumb", got)
	}
}

func TestSeedThumbMimeType(t *testing.T) {
	webp := []byte{'R', 'I', 'F', 'F', 0, 0, 0, 0, 'W', 'E', 'B', 'P'}
	if got := seedThumbMimeType(webp); got != "image/webp" {
		t.Fatalf("webp mime = %q, want image/webp", got)
	}
	jpeg := []byte{0xFF, 0xD8, 0xFF}
	if got := seedThumbMimeType(jpeg); got != "image/jpeg" {
		t.Fatalf("jpeg mime = %q, want image/jpeg", got)
	}
}

func TestDocumentsNeedInlineCachedThumbsDetectsStaleMime(t *testing.T) {
	ctx := context.Background()
	media := newFakeMediaStore()
	webp := []byte{'R', 'I', 'F', 'F', 0, 0, 0, 0, 'W', 'E', 'B', 'P'}
	doc := domain.Document{
		ID: 100,
		Thumbs: []domain.PhotoSize{
			{Kind: domain.PhotoSizeKindCached, Type: "m", Bytes: webp},
		},
	}
	if err := media.PutDocument(ctx, doc); err != nil {
		t.Fatalf("put doc: %v", err)
	}
	if err := media.PutFileBlob(ctx, domain.FileBlob{LocationKey: "doc:100:m", MimeType: "image/jpeg"}); err != nil {
		t.Fatalf("put blob: %v", err)
	}
	svc := NewService(media, nil, 2)
	stale, err := svc.documentsNeedInlineCachedThumbs(ctx, []int64{doc.ID})
	if err != nil {
		t.Fatalf("documentsNeedInlineCachedThumbs: %v", err)
	}
	if !stale {
		t.Fatal("expected stale mime to require repair")
	}

	if err := media.PutFileBlob(ctx, domain.FileBlob{LocationKey: "doc:100:m", MimeType: "image/webp"}); err != nil {
		t.Fatalf("put repaired blob: %v", err)
	}
	stale, err = svc.documentsNeedInlineCachedThumbs(ctx, []int64{doc.ID})
	if err != nil {
		t.Fatalf("documentsNeedInlineCachedThumbs after repair: %v", err)
	}
	if stale {
		t.Fatal("repaired mime should not require repair")
	}
}

func findCachedThumb(sizes []domain.PhotoSize) (domain.PhotoSize, bool) {
	for _, size := range sizes {
		if size.Kind == domain.PhotoSizeKindCached && len(size.Bytes) > 0 {
			return size, true
		}
	}
	return domain.PhotoSize{}, false
}

func hasPathThumb(sizes []domain.PhotoSize) bool {
	for _, size := range sizes {
		if size.Kind == domain.PhotoSizeKindPath && len(size.Bytes) > 0 {
			return true
		}
	}
	return false
}

func blobKeyDoc(id int64) string {
	return "doc:" + itoa(id)
}

func itoa(v int64) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
