package domain

import "errors"

// ErrStickerInvalid means the input document is not a valid sticker/GIF (faveSticker/saveGif etc.).
var ErrStickerInvalid = errors.New("sticker document invalid")

var (
	ErrStickerSetTitleInvalid      = errors.New("sticker set title invalid")
	ErrStickerSetShortNameInvalid  = errors.New("sticker set short name invalid")
	ErrStickerSetShortNameOccupied = errors.New("sticker set short name occupied")
	ErrStickerSetTypeInvalid       = errors.New("sticker set type invalid")
	ErrStickerSetEmpty             = errors.New("sticker set empty")
	ErrStickerSetTooMuch           = errors.New("sticker set too much")
	ErrStickerSetEmojiInvalid      = errors.New("sticker set emoji invalid")
	ErrStickerSetFileInvalid       = errors.New("sticker set file invalid")
	ErrStickerSetCreatorInvalid    = errors.New("sticker set creator invalid")
	ErrStickerSetInvalid           = errors.New("sticker set invalid")
	ErrStickerSetNotOwned          = errors.New("sticker set not owned")
	ErrStickerSetPositionInvalid   = errors.New("sticker set position invalid")
)

// StickerCollectionKind distinguishes the several kinds of personal sticker collection a user has.
type StickerCollectionKind string

const (
	// StickerCollectionFaved is favorited stickers (messages.faveSticker / getFavedStickers).
	StickerCollectionFaved StickerCollectionKind = "faved"
	// StickerCollectionRecent is recently used stickers (saveRecentSticker / getRecentStickers).
	StickerCollectionRecent StickerCollectionKind = "recent"
	// StickerCollectionRecentAttached is stickers recently used with media from the attach menu (attached=true).
	StickerCollectionRecentAttached StickerCollectionKind = "recent_attached"
	// StickerCollectionGif is saved GIFs (messages.saveGif / getSavedGifs).
	StickerCollectionGif StickerCollectionKind = "gif"
)

// Capacity upper bounds for each collection (newest first; the oldest are truncated on overflow). These only limit storage growth, not strict Telegram quotas;
// faved has no Premium tiering (outside the user's decision range) and uses a single bound.
const (
	MaxFavedStickers        = 100
	MaxRecentStickers       = 30
	MaxSavedGifs            = 200
	MaxInstalledStickerSets = 200
	MaxCreatedStickerSets   = 200
)

// MaxStickerCollectionItems returns the capacity upper bound for a collection kind.
func MaxStickerCollectionItems(kind StickerCollectionKind) int {
	switch kind {
	case StickerCollectionFaved:
		return MaxFavedStickers
	case StickerCollectionGif:
		return MaxSavedGifs
	default:
		return MaxRecentStickers
	}
}

// StickerCollectionItem is one item in a collection: document id + time added (recent's used_at / faved's favorite time).
type StickerCollectionItem struct {
	DocumentID int64
	Date       int
}

// UserStickerSet is one sticker set's install/archive state for an account.
// The set metadata, documents and packs are still maintained by the media/files module; only the user's view is stored here.
type UserStickerSet struct {
	OwnerUserID   int64
	StickerSetID  int64
	Kind          StickerSetKind
	Archived      bool
	InstalledDate int
	OrderValue    int64
}
