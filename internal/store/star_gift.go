package store

import (
	"context"

	"telesrv/internal/domain"
)

// StarGiftStore 持久化礼物目录、不可变版本和 peer 收到的礼物实例。
type StarGiftStore interface {
	// Catalog 返回启用的当前目录快照，按 sort_order/gift_id 排序。
	Catalog(ctx context.Context) ([]domain.StarGift, error)
	// CatalogGift 只返回当前启用版本，供新购买校验。
	CatalogGift(ctx context.Context, giftID int64) (domain.StarGift, bool, error)
	// CatalogRevision 返回不可变历史版本，供已领取礼物投影。
	CatalogRevision(ctx context.Context, revisionID int64) (domain.StarGift, bool, error)
	// CreateCatalogRevision 创建新礼物或为既有礼物创建新版本，并原子切换当前版本。
	CreateCatalogRevision(ctx context.Context, write domain.StarGiftCatalogWrite) (domain.StarGiftCatalogEntry, error)
	SetCatalogEnabled(ctx context.Context, giftID int64, enabled bool) (bool, error)
	SetCatalogSortOrder(ctx context.Context, giftID int64, sortOrder int) (bool, error)
	// AnimationJSON 返回当前版本的规范化 Lottie JSON，供管理后台安全预览。
	AnimationJSON(ctx context.Context, giftID int64) ([]byte, bool, error)
	// PublishCollectibleRevision validates and atomically publishes a new immutable attribute pool.
	PublishCollectibleRevision(ctx context.Context, write domain.StarGiftCollectibleWrite) (domain.StarGiftCollectibleRevision, error)
	ActiveCollectibleRevision(ctx context.Context, giftID int64) (domain.StarGiftCollectibleRevision, bool, error)
	CollectibleAvailability(ctx context.Context, giftIDs []int64) (map[int64]domain.StarGiftCollectibleAvailability, error)
	CollectibleAnimationJSON(ctx context.Context, giftID int64, kind domain.StarGiftCollectibleAttributeKind, attributeID int64) ([]byte, bool, error)
	UniqueBySlug(ctx context.Context, slug string) (domain.UniqueStarGift, bool, error)
	UniqueByID(ctx context.Context, uniqueGiftID int64) (domain.UniqueStarGift, bool, error)
	UniqueByIDs(ctx context.Context, uniqueGiftIDs []int64) (map[int64]domain.UniqueStarGift, error)

	// Create 写一条收到的礼物实例，返回行 id；频道礼物未显式给 saved_id 时以该行 id 作为 saved_id。
	Create(ctx context.Context, gift domain.SavedStarGift) (int64, error)
	// ListByOwner 按 id DESC keyset 分页返回某 owner 未转换的礼物；excludeUnsaved 时只返展示在资料的。
	ListByOwner(ctx context.Context, owner domain.Peer, excludeUnsaved bool, offset string, limit int) (domain.SavedStarGiftPage, error)
	ListByOwnerFiltered(ctx context.Context, filter domain.SavedStarGiftFilter) (domain.SavedStarGiftPage, error)
	// GetByRef 按协议引用取礼物实例：用户用 msg_id，频道用 saved_id。
	GetByRef(ctx context.Context, ref domain.SavedStarGiftRef) (domain.SavedStarGift, bool, error)
	// ResolveSavedIDs resolves an ordered batch of protocol references without
	// per-gift round trips. Every ref must belong to owner and resolve to a live gift.
	ResolveSavedIDs(ctx context.Context, owner domain.Peer, refs []domain.SavedStarGiftRef) ([]int64, error)
	// CountByOwner 返回某 owner 展示在资料的礼物数（非转换、非隐藏），供 full.stargifts_count。
	CountByOwner(ctx context.Context, owner domain.Peer) (int, error)
	// SetUnsaved 切换礼物在资料的展示（saveStarGift）；返回是否命中一行。
	SetUnsaved(ctx context.Context, ref domain.SavedStarGiftRef, unsaved bool) (bool, error)
	// MarkConverted 幂等地把礼物标记为已转换（convertStarGift），返回该行供调用方据 ConvertStars
	// 入账；已转换返回 domain.ErrStarGiftAlreadyConverted，不存在返回 domain.ErrStarGiftNotFound。
	MarkConverted(ctx context.Context, ref domain.SavedStarGiftRef) (domain.SavedStarGift, error)

	ListCollections(ctx context.Context, owner domain.Peer) ([]domain.StarGiftCollection, error)
	CreateCollection(ctx context.Context, owner domain.Peer, title string, savedGiftIDs []int64) (domain.StarGiftCollection, error)
	UpdateCollection(ctx context.Context, owner domain.Peer, collectionID int, patch domain.StarGiftCollectionPatch) (domain.StarGiftCollection, error)
	DeleteCollection(ctx context.Context, owner domain.Peer, collectionID int) (bool, error)
	ReorderCollections(ctx context.Context, owner domain.Peer, collectionIDs []int) error
	SetPinned(ctx context.Context, owner domain.Peer, savedGiftIDs []int64) error
}

// StarGiftUpgradeStore owns the aggregate transaction spanning Stars, the
// collectible issuance state, the saved gift terminal state and durable
// private service-message updates.
type StarGiftUpgradeStore interface {
	UpgradeStarGift(ctx context.Context, req domain.StarGiftUpgradeRequest) (domain.StarGiftUpgradeResult, error)
}
