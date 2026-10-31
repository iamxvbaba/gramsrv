package store

import (
	"context"
	"time"

	"telesrv/internal/domain"
)

// MediaStore persists media metadata: upload parts, blob index, document/photo registry,
// sticker sets, available reactions, and profile photo history. The blob bytes themselves are
// read/written by the blob backend by object_key; this interface only manages the file_blobs
// index rows.
type MediaStore interface {
	// Upload parts (transient: cleaned up once assembled into a blob).
	SaveFilePart(ctx context.Context, part domain.UploadPart) error
	UploadPartUsage(ctx context.Context, ownerUserID int64) (domain.UploadPartUsage, error)
	UploadPartSlot(ctx context.Context, ownerUserID, fileID int64, part int) (domain.UploadPartSlot, error)
	LoadFileParts(ctx context.Context, ownerUserID, fileID int64) ([]domain.UploadPart, error)
	DeleteFileParts(ctx context.Context, ownerUserID, fileID int64) ([]string, error)
	DeleteExpiredUploadParts(ctx context.Context, before time.Time, limit int) ([]string, error)
	// Uploaded media receipts survive transient part cleanup so messages.sendMedia can replay an
	// InputMediaUploadedPhoto/Document after a lost response. Put returns the durable winner; when
	// created=false another concurrent materializer won the (owner,file_id) key.
	GetUploadedMediaReceipt(ctx context.Context, ownerUserID, fileID int64) (domain.UploadedMediaReceipt, bool, error)
	PutUploadedMediaReceipt(ctx context.Context, receipt domain.UploadedMediaReceipt) (stored domain.UploadedMediaReceipt, created bool, err error)

	// blob index.
	PutFileBlob(ctx context.Context, blob domain.FileBlob) error
	GetFileBlob(ctx context.Context, locationKey string) (domain.FileBlob, bool, error)
	// GetFileBlobs batch-fetches FileBlob metadata by location_key (missing keys do not appear
	// in the returned map). For paths such as startup warm-up that load many blobs at once,
	// replacing the N+1 round trips of per-blob GetFileBlob.
	GetFileBlobs(ctx context.Context, locationKeys []string) (map[string]domain.FileBlob, error)
	// SeedThumbStates batch-returns document thumbnail validation metadata, deliberately not
	// the inline bytes: when validating thousands of seed documents at startup, pulling all the
	// base64 from thumbs back into memory to decode is the main cost.
	SeedThumbStates(ctx context.Context, documentIDs []int64) ([]SeedThumbState, error)

	// seed state. It records only the content hash of the static asset catalog, used at
	// startup to skip unchanged duplicate imports; real servability is still guaranteed by
	// documents/file_blobs validation, so this hash alone cannot be trusted.
	GetSeedState(ctx context.Context, key string) (hash string, found bool, err error)
	PutSeedState(ctx context.Context, key, hash string) error

	// document / photo registry.
	PutDocument(ctx context.Context, doc domain.Document) error
	GetDocument(ctx context.Context, id int64) (domain.Document, bool, error)
	GetDocuments(ctx context.Context, ids []int64) ([]domain.Document, error)
	PutPhoto(ctx context.Context, photo domain.Photo) error
	GetPhoto(ctx context.Context, id int64) (domain.Photo, bool, error)

	// Link preview resolution cache (web_pages). Deduplicated by normalized URL hash; snapshot is a full snapshot.
	PutWebPage(ctx context.Context, urlHash int64, page domain.MessageWebPage, now int) error
	GetWebPageByURLHash(ctx context.Context, urlHash int64) (page domain.MessageWebPage, refreshedAt int, found bool, err error)

	// sticker sets / available reactions.
	PutStickerSet(ctx context.Context, set domain.StickerSet) error
	CreateStickerSet(ctx context.Context, set domain.StickerSet, docs []domain.Document) error
	UpdateStickerSet(ctx context.Context, set domain.StickerSet, docs []domain.Document) error
	DeleteStickerSet(ctx context.Context, setID int64, creatorUserID int64) error
	// AdminDeleteStickerSet is DeleteStickerSet without the creator_user_id
	// match, for admin-console management of seed-imported and user-created
	// packs alike.
	AdminDeleteStickerSet(ctx context.Context, setID int64) error
	GetStickerSetByID(ctx context.Context, id int64) (domain.StickerSet, bool, error)
	GetStickerSetByShortName(ctx context.Context, shortName string) (domain.StickerSet, bool, error)
	GetStickerSetBySystemKey(ctx context.Context, systemKey string) (domain.StickerSet, bool, error)
	ListStickerSets(ctx context.Context, kind domain.StickerSetKind) ([]domain.StickerSet, error)
	ListStickerSetsByCreator(ctx context.Context, creatorUserID int64, offsetID int64, limit int) ([]domain.StickerSet, int, error)
	StickerSetShortNameAvailable(ctx context.Context, shortName string) (bool, error)
	CountStickerSets(ctx context.Context) (int, error)
	// CountStickerSetsByIDs counts how many of the given ids still exist; the startup fast path
	// uses it to detect deleted seed sets and trigger a re-import.
	CountStickerSetsByIDs(ctx context.Context, ids []int64) (int, error)
	PutAvailableReaction(ctx context.Context, r domain.AvailableReaction) error
	ListAvailableReactions(ctx context.Context) ([]domain.AvailableReaction, error)
	CountAvailableReactions(ctx context.Context) (int, error)

	// WithTx runs fn inside a database transaction with a transaction-scoped
	// MediaStore. If fn returns nil the transaction is committed; otherwise it
	// is rolled back.
	WithTx(ctx context.Context, fn func(ctx context.Context, txMedia MediaStore) error) error

	// Profile photo history (owner = user/channel; current = the active one with the largest sort_order).
	AddProfilePhotoKind(ctx context.Context, ownerType domain.PeerType, ownerID int64, kind domain.ProfilePhotoKind, photoID int64, date int) error
	CurrentProfilePhotoKind(ctx context.Context, ownerType domain.PeerType, ownerID int64, kind domain.ProfilePhotoKind) (int64, bool, error)
	CurrentProfilePhotos(ctx context.Context, ownerType domain.PeerType, ownerIDs []int64) (map[int64]domain.ProfilePhotoRef, error)
	CurrentProfilePhotosKind(ctx context.Context, ownerType domain.PeerType, ownerIDs []int64, kind domain.ProfilePhotoKind) (map[int64]domain.ProfilePhotoRef, error)
	ListProfilePhotosKind(ctx context.Context, ownerType domain.PeerType, ownerID int64, kind domain.ProfilePhotoKind, offset, limit int, maxID int64) (ids []int64, total int, err error)
	ListProfilePhotoDetailsKind(ctx context.Context, ownerType domain.PeerType, ownerID int64, kind domain.ProfilePhotoKind, offset, limit int, maxID int64) (photos []domain.Photo, total int, err error)
	DeleteProfilePhotos(ctx context.Context, ownerType domain.PeerType, ownerID int64, photoIDs []int64) ([]int64, error)
	DeleteProfilePhotosKind(ctx context.Context, ownerType domain.PeerType, ownerID int64, kind domain.ProfilePhotoKind, photoIDs []int64) ([]int64, error)
}

// SeedThumbState is the servability validation info for one document thumbnail. Head is the
// first 12 bytes of the inline bytes, enough to determine the image type; DataSize is the
// decoded byte count, and HasBlob/BlobSize/BlobMime come from the file_blobs index
// (location_key = doc:<id>:<type>).
type SeedThumbState struct {
	DocumentID   int64
	Type         string
	Kind         string
	DeclaredSize int
	DataSize     int
	HasBlob      bool
	BlobSize     int64
	BlobMime     string
	Head         []byte
}
