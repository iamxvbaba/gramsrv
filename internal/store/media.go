package store

import (
	"context"
	"time"

	"telesrv/internal/domain"
)

// MediaStore 持久化媒体元数据：上传分片、blob 索引、文档/照片注册表、
// 贴纸集、可用 reaction、头像历史。blob 字节本身由 blob backend 按 object_key 读写，
// 本接口只管 file_blobs 索引行。
type MediaStore interface {
	// 上传分片（transient：组装成 blob 后即清理）。
	SaveFilePart(ctx context.Context, part domain.UploadPart) error
	UploadPartUsage(ctx context.Context, ownerUserID int64) (domain.UploadPartUsage, error)
	UploadPartSlot(ctx context.Context, ownerUserID, fileID int64, part int) (domain.UploadPartSlot, error)
	LoadFileParts(ctx context.Context, ownerUserID, fileID int64) ([]domain.UploadPart, error)
	DeleteFileParts(ctx context.Context, ownerUserID, fileID int64) ([]string, error)
	DeleteExpiredUploadParts(ctx context.Context, before time.Time, limit int) ([]string, error)

	// blob 索引。
	PutFileBlob(ctx context.Context, blob domain.FileBlob) error
	GetFileBlob(ctx context.Context, locationKey string) (domain.FileBlob, bool, error)
	// GetFileBlobs 批量按 location_key 取 FileBlob 元数据（缺失的 key 不出现在返回 map 中）。
	// 供启动预热等需一次性加载大量 blob 的路径，替代逐个 GetFileBlob 的 N+1 往返。
	GetFileBlobs(ctx context.Context, locationKeys []string) (map[string]domain.FileBlob, error)

	// seed 状态。只记录静态资源 catalog 的内容 hash，用于启动时跳过未变化的重复导入；
	// 真实可服务性仍由 documents/file_blobs 校验保证，不能只相信这里的 hash。
	GetSeedState(ctx context.Context, key string) (hash string, found bool, err error)
	PutSeedState(ctx context.Context, key, hash string) error

	// 文档 / 照片注册表。
	PutDocument(ctx context.Context, doc domain.Document) error
	GetDocument(ctx context.Context, id int64) (domain.Document, bool, error)
	GetDocuments(ctx context.Context, ids []int64) ([]domain.Document, error)
	PutPhoto(ctx context.Context, photo domain.Photo) error
	GetPhoto(ctx context.Context, id int64) (domain.Photo, bool, error)

	// 链接预览解析缓存（web_pages）。按规范化 URL 哈希去重，snapshot 为完整快照。
	PutWebPage(ctx context.Context, urlHash int64, page domain.MessageWebPage, now int) error
	GetWebPageByURLHash(ctx context.Context, urlHash int64) (page domain.MessageWebPage, refreshedAt int, found bool, err error)

	// 贴纸集 / 可用 reaction。
	PutStickerSet(ctx context.Context, set domain.StickerSet) error
	CreateStickerSet(ctx context.Context, set domain.StickerSet, docs []domain.Document) error
	UpdateStickerSet(ctx context.Context, set domain.StickerSet, docs []domain.Document) error
	DeleteStickerSet(ctx context.Context, setID int64, creatorUserID int64) error
	GetStickerSetByID(ctx context.Context, id int64) (domain.StickerSet, bool, error)
	GetStickerSetByShortName(ctx context.Context, shortName string) (domain.StickerSet, bool, error)
	GetStickerSetBySystemKey(ctx context.Context, systemKey string) (domain.StickerSet, bool, error)
	ListStickerSets(ctx context.Context, kind domain.StickerSetKind) ([]domain.StickerSet, error)
	ListStickerSetsByCreator(ctx context.Context, creatorUserID int64, offsetID int64, limit int) ([]domain.StickerSet, int, error)
	StickerSetShortNameAvailable(ctx context.Context, shortName string) (bool, error)
	CountStickerSets(ctx context.Context) (int, error)
	PutAvailableReaction(ctx context.Context, r domain.AvailableReaction) error
	ListAvailableReactions(ctx context.Context) ([]domain.AvailableReaction, error)
	CountAvailableReactions(ctx context.Context) (int, error)

	// 头像历史（owner = user/channel；current = active 中 sort_order 最大者）。
	AddProfilePhotoKind(ctx context.Context, ownerType domain.PeerType, ownerID int64, kind domain.ProfilePhotoKind, photoID int64, date int) error
	CurrentProfilePhotoKind(ctx context.Context, ownerType domain.PeerType, ownerID int64, kind domain.ProfilePhotoKind) (int64, bool, error)
	CurrentProfilePhotos(ctx context.Context, ownerType domain.PeerType, ownerIDs []int64) (map[int64]domain.ProfilePhotoRef, error)
	CurrentProfilePhotosKind(ctx context.Context, ownerType domain.PeerType, ownerIDs []int64, kind domain.ProfilePhotoKind) (map[int64]domain.ProfilePhotoRef, error)
	ListProfilePhotosKind(ctx context.Context, ownerType domain.PeerType, ownerID int64, kind domain.ProfilePhotoKind, offset, limit int, maxID int64) (ids []int64, total int, err error)
	ListProfilePhotoDetailsKind(ctx context.Context, ownerType domain.PeerType, ownerID int64, kind domain.ProfilePhotoKind, offset, limit int, maxID int64) (photos []domain.Photo, total int, err error)
	DeleteProfilePhotos(ctx context.Context, ownerType domain.PeerType, ownerID int64, photoIDs []int64) ([]int64, error)
	DeleteProfilePhotosKind(ctx context.Context, ownerType domain.PeerType, ownerID int64, kind domain.ProfilePhotoKind, photoIDs []int64) ([]int64, error)
}
