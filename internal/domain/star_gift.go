package domain

import (
	"encoding/base64"
	"errors"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Star gift（payments.sendStarsForm + inputInvoiceStarGift）领域模型。目录和不可变版本
// 持久化在 star_gift_catalog(_revisions)；peer 收到的礼物实例落 peer_star_gifts。
// 与 Stars 账本配合：发礼 Debit、转换回 Stars 时 Credit。

// StarGift 是一个可购买礼物目录项。RevisionID 标识不可变的标题/价格/动画快照。
type StarGift struct {
	ID            int64
	RevisionID    int64
	Stars         int64    // 购买价（Stars）
	ConvertStars  int64    // 收礼人可转换回的 Stars
	UpgradeStars  int64    // 升级为唯一礼物所需 Stars；0 表示当前不可升级
	UpgradeTotal  int      // 当前已发布属性池允许发行的唯一礼物总量
	UpgradeIssued int      // 当前已发行数量
	Title         string   // 可选标题
	Sticker       Document // 礼物贴纸快照（tg 投影必须是带 sticker 属性的有效 Document，否则客户端丢弃）
}

// SavedStarGift 是一条已收到的礼物实例（peer_star_gifts 一行）。
type SavedStarGift struct {
	ID                  int64
	Owner               Peer   // 收礼 peer（user/channel）
	FromUserID          int64  // 送礼人（匿名也保留真实值供账本，下发时按 NameHidden 决定是否暴露）
	GiftID              int64  // → StarGift.ID
	RevisionID          int64  // → star_gift_catalog_revisions.id，历史查询必须按此版本投影
	MsgID               int    // 用户礼物的私聊 msg_id；频道礼物不进历史，固定为 0
	SavedID             int64  // 频道礼物 inputSavedStarGiftChat.saved_id；用户礼物为 0
	Date                int    // 收到时刻 Unix 秒
	NameHidden          bool   // 送礼人请求隐藏姓名
	Unsaved             bool   // 未展示在个人资料（saveStarGift 切换）
	Converted           bool   // 已转换回 Stars（终态，从列表排除）
	ConvertStars        int64  // 转换可退回的 Stars
	PrepaidUpgradeStars int64  // 送礼人随礼物预付的唯一礼物升级额
	Message             string // 附言（可选）
	UniqueGiftID        int64  // 非 0 表示已升级为唯一礼物；与 Converted 互斥
	UpgradeMsgID        int    // messageActionStarGiftUnique 的 owner 侧消息 id
	PinnedOrder         int    // >0 表示资料页置顶顺序
	CollectionIDs       []int  // 当前所属集合；按集合顺序稳定返回
	Unique              *UniqueStarGift
}

// StarGiftCollectibleAttributeKind 是唯一礼物三个必选属性槽位。
type StarGiftCollectibleAttributeKind string

const (
	StarGiftCollectibleModel    StarGiftCollectibleAttributeKind = "model"
	StarGiftCollectiblePattern  StarGiftCollectibleAttributeKind = "pattern"
	StarGiftCollectibleBackdrop StarGiftCollectibleAttributeKind = "backdrop"
)

// StarGiftCollectibleAttribute 是已发布属性池的一项。RarityPermille 同时是客户端展示的
// 精确稀有度和升级抽取概率；同一 revision、同一 kind 的总和必须恰好为 1000。
type StarGiftCollectibleAttribute struct {
	ID                    int64
	CollectibleRevisionID int64
	Kind                  StarGiftCollectibleAttributeKind
	Name                  string
	Document              *Document
	BackdropID            int
	CenterColor           int
	EdgeColor             int
	PatternColor          int
	TextColor             int
	RarityPermille        int
	SortOrder             int
	Animation             *StarGiftAnimation
	Blob                  *FileBlob
}

// StarGiftCollectibleRevision 是某普通礼物的一份不可变、可发布属性池。
type StarGiftCollectibleRevision struct {
	ID           int64
	GiftID       int64
	Revision     int
	UpgradeStars int64
	SupplyTotal  int
	Issued       int
	SlugPrefix   string
	Published    bool
	Models       []StarGiftCollectibleAttribute
	Patterns     []StarGiftCollectibleAttribute
	Backdrops    []StarGiftCollectibleAttribute
	CreatedBy    string
	CreatedAt    time.Time
	PublishedAt  time.Time
}

// StarGiftCollectibleWrite 是后台创建/发布属性池的协议无关输入。
type StarGiftCollectibleWrite struct {
	GiftID       int64
	UpgradeStars int64
	SupplyTotal  int
	SlugPrefix   string
	Models       []StarGiftCollectibleAttribute
	Patterns     []StarGiftCollectibleAttribute
	Backdrops    []StarGiftCollectibleAttribute
	Actor        string
	CommandID    string
}

// UniqueStarGift 是一份已经发行的唯一礼物。属性、编号与 slug 一经创建永久不变。
type UniqueStarGift struct {
	ID                    int64
	GiftID                int64
	CollectibleRevisionID int64
	SourceSavedGiftID     int64
	Title                 string
	Slug                  string
	Num                   int
	Owner                 Peer
	Model                 StarGiftCollectibleAttribute
	Pattern               StarGiftCollectibleAttribute
	Backdrop              StarGiftCollectibleAttribute
	AvailabilityIssued    int
	AvailabilityTotal     int
	KeepOriginalDetails   bool
	OriginalFromUserID    int64
	OriginalOwner         Peer
	OriginalDate          int
	OriginalMessage       string
	OriginalNameHidden    bool
	CreatedAt             time.Time
}

// StarGiftUpgradePreview 是客户端升级弹窗所需的当前价格和属性样例。
type StarGiftUpgradePreview struct {
	GiftID       int64
	Revision     int
	UpgradeStars int64
	SupplyTotal  int
	Issued       int
	SlugPrefix   string
	Models       []StarGiftCollectibleAttribute
	Patterns     []StarGiftCollectibleAttribute
	Backdrops    []StarGiftCollectibleAttribute
}

// StarGiftCollectibleAvailability is the lightweight current-pool projection used
// when rendering historical saved gifts. The saved gift keeps its immutable catalog
// revision for appearance and prices, while upgrade availability follows the pool
// currently published for the logical gift ID.
type StarGiftCollectibleAvailability struct {
	UpgradeStars int64
	SupplyTotal  int
	Issued       int
}

// StarGiftUpgradeRequest is one idempotent user-owned upgrade command. Paid
// invoice upgrades set ChargeStars; the direct payments.upgradeStarGift path
// sets RequirePrepaid and charges zero at upgrade time.
type StarGiftUpgradeRequest struct {
	UserID              int64
	Ref                 SavedStarGiftRef
	KeepOriginalDetails bool
	ChargeStars         int64
	RequirePrepaid      bool
	FormID              int64
	CommandKey          string
	Date                int
	OriginAuthKeyID     [8]byte
	OriginSessionID     int64
}

type StarGiftUpgradeResult struct {
	Saved     SavedStarGift
	Unique    UniqueStarGift
	Balance   StarsBalance
	Send      SendPrivateTextResult
	Duplicate bool
}

// StarGiftCollection 是 peer 资料页中的礼物集合；一份礼物可属于多个集合。
type StarGiftCollection struct {
	Owner        Peer
	CollectionID int
	Title        string
	GiftIDs      []int64 // peer_star_gifts.id，按集合内顺序
	Hash         int64
	SortOrder    int
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// StarGiftCollectionPatch 描述 updateStarGiftCollection 的局部更新。
type StarGiftCollectionPatch struct {
	Title     *string
	DeleteIDs []int64
	AddIDs    []int64
	Order     []int64
}

// StarGiftAnimationFormat 是后台导入源格式。服务端最终总是存储规范化 TGS。
type StarGiftAnimationFormat string

const (
	StarGiftAnimationTGS    StarGiftAnimationFormat = "tgs"
	StarGiftAnimationLottie StarGiftAnimationFormat = "lottie"
)

// StarGiftAnimation 是已规范化并验证的动画。JSON 用于后台播放，TGS 用于客户端。
type StarGiftAnimation struct {
	SourceName   string
	SourceFormat StarGiftAnimationFormat
	JSON         []byte
	TGS          []byte
	SHA256       []byte
	Width        int
	Height       int
	FrameRate    float64
	InPoint      float64
	OutPoint     float64
}

// StarGiftCatalogWrite 是 store 原子创建目录版本所需的协议无关数据。
type StarGiftCatalogWrite struct {
	GiftID       int64 // 0 创建新礼物；非 0 为该礼物创建新 revision
	Title        string
	Stars        int64
	ConvertStars int64
	Enabled      bool
	SortOrder    int
	Document     Document
	Blob         FileBlob
	Animation    StarGiftAnimation
	Actor        string
	CommandID    string
}

// StarGiftCatalogEntry 是管理后台目录视图。
type StarGiftCatalogEntry struct {
	Gift          StarGift
	Enabled       bool
	SortOrder     int
	Revision      int
	SourceName    string
	SourceFormat  StarGiftAnimationFormat
	AnimationSHA  []byte
	AnimationSize int64
	Width         int
	Height        int
	FrameRate     float64
	ReceivedCount int64
	CreatedBy     string
	UpdatedAt     time.Time
}

// SavedStarGiftRef 是 payments.getSavedStarGift/saveStarGift/convertStarGift 的协议中立引用。
// 用户礼物使用 inputSavedStarGiftUser.msg_id；频道礼物使用 inputSavedStarGiftChat.peer + saved_id。
type SavedStarGiftRef struct {
	Owner   Peer
	MsgID   int
	SavedID int64
}

// Valid reports whether the reference has the identity required by its owner kind.
func (r SavedStarGiftRef) Valid() bool {
	switch r.Owner.Type {
	case PeerTypeUser:
		return r.Owner.ID != 0 && r.MsgID > 0
	case PeerTypeChannel:
		return r.Owner.ID != 0 && r.SavedID > 0
	default:
		return false
	}
}

// SavedStarGiftPage 是一页已收到礼物 + keyset 分页游标。
type SavedStarGiftPage struct {
	Gifts      []SavedStarGift
	NextOffset string // 空 = 无更多页（末页必须省略，客户端据此停止翻页）
	Count      int    // 总数（未转换、按 excludeUnsaved 过滤后）
}

// SavedStarGiftFilter describes the client-visible filters supported by
// payments.getSavedStarGifts. CollectionID is the collection membership filter;
// zero means all collections. The current catalog is used only to decide whether
// a regular gift remains upgradable, while its rendered gift snapshot still comes
// from RevisionID.
type SavedStarGiftFilter struct {
	Owner               Peer
	ExcludeUnsaved      bool
	ExcludeSaved        bool
	ExcludeUnlimited    bool
	ExcludeUnique       bool
	ExcludeUpgradable   bool
	ExcludeUnupgradable bool
	CollectionID        int
	Offset              string
	Limit               int
}

// Star gift 边界常量。
const (
	// MaxSavedStarGiftsLimit 是 getSavedStarGifts 单页上限。
	MaxSavedStarGiftsLimit = 100
	// MaxStarGiftMessageRunes 限制附言长度（对齐 stargifts_message_length_max 量级）。
	MaxStarGiftMessageRunes = 255
	// MaxStarGiftsOffsetBytes 是 keyset 游标字符串长度上限。
	MaxStarGiftsOffsetBytes = 64
	// MaxStarGiftTGSBytes 限制后台导入的压缩动画，避免管理面上传成为容量旁路。
	MaxStarGiftTGSBytes int64 = 512 << 10
	// MaxStarGiftLottieBytes 限制解压后的 Lottie JSON。
	MaxStarGiftLottieBytes int64 = 4 << 20
	// MaxStarGiftAnimationFrameRate / Seconds 限制管理后台播放器和客户端动画时间轴。
	MaxStarGiftAnimationFrameRate = 120
	MaxStarGiftAnimationSeconds   = 30
	// MaxStarGiftCatalogSize 是当前普通礼物目录的有界上限。
	MaxStarGiftCatalogSize                  = 500
	MaxStarGiftTitleRunes                   = 128
	MaxStarGiftCollectibleAttributesPerKind = 256
	MaxStarGiftCollectionTitleRunes         = 12
	MaxStarGiftCollectionsPerPeer           = 100
	MaxStarGiftCollectionItems              = 1000
)

// Star gift 哨兵错误（rpc 层 errors.Is 映射为 tgerr）。
var (
	// ErrStarGiftInvalid 表示礼物 id 不在目录里。
	ErrStarGiftInvalid = errors.New("stargift: invalid gift id")
	// ErrStarGiftNotFound 表示找不到该已收到礼物实例。
	ErrStarGiftNotFound = errors.New("stargift: saved gift not found")
	// ErrStarGiftAlreadyConverted 表示礼物已转换回 Stars（不可重复转换）。
	ErrStarGiftAlreadyConverted       = errors.New("stargift: already converted")
	ErrStarGiftFileInvalid            = errors.New("stargift: invalid animation file")
	ErrStarGiftCatalogFull            = errors.New("stargift: catalog full")
	ErrStarGiftCollectibleUnavailable = errors.New("stargift: collectible upgrade unavailable")
	ErrStarGiftAlreadyUpgraded        = errors.New("stargift: already upgraded")
	ErrStarGiftCollectibleSoldOut     = errors.New("stargift: collectible supply exhausted")
	ErrStarGiftCollectibleInvalid     = errors.New("stargift: invalid collectible definition")
	ErrStarGiftCollectionNotFound     = errors.New("stargift: collection not found")
	ErrStarGiftCollectionsFull        = errors.New("stargift: collections full")
)

var starGiftCollectibleSlugPrefix = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,47}$`)

// ValidateStarGiftCollectibleDraft validates the operator-authored definition before animation
// blobs/documents are allocated. This is the validation boundary used by admin dry-runs.
func ValidateStarGiftCollectibleDraft(write StarGiftCollectibleWrite) error {
	write.SlugPrefix = strings.TrimSpace(strings.ToLower(write.SlugPrefix))
	if write.GiftID <= 0 || write.UpgradeStars <= 0 || write.SupplyTotal <= 0 ||
		!starGiftCollectibleSlugPrefix.MatchString(write.SlugPrefix) || strings.TrimSpace(write.CommandID) == "" {
		return ErrStarGiftCollectibleInvalid
	}
	if err := validateStarGiftAttributes(write.Models, StarGiftCollectibleModel, false); err != nil {
		return err
	}
	if err := validateStarGiftAttributes(write.Patterns, StarGiftCollectiblePattern, false); err != nil {
		return err
	}
	return validateStarGiftAttributes(write.Backdrops, StarGiftCollectibleBackdrop, false)
}

// ValidateStarGiftCollectibleWrite validates a complete publish command. Published pools are
// immutable, so partial definitions are rejected before any document/blob rows are written.
func ValidateStarGiftCollectibleWrite(write StarGiftCollectibleWrite) error {
	if err := ValidateStarGiftCollectibleDraft(write); err != nil {
		return err
	}
	if err := validateStarGiftAttributes(write.Models, StarGiftCollectibleModel, true); err != nil {
		return err
	}
	if err := validateStarGiftAttributes(write.Patterns, StarGiftCollectiblePattern, true); err != nil {
		return err
	}
	return validateStarGiftAttributes(write.Backdrops, StarGiftCollectibleBackdrop, true)
}

func validateStarGiftAttributes(attributes []StarGiftCollectibleAttribute, kind StarGiftCollectibleAttributeKind, requireStoredAsset bool) error {
	if len(attributes) == 0 || len(attributes) > MaxStarGiftCollectibleAttributesPerKind {
		return ErrStarGiftCollectibleInvalid
	}
	seen := make(map[string]struct{}, len(attributes))
	total := 0
	for _, attribute := range attributes {
		name := strings.TrimSpace(attribute.Name)
		if attribute.Kind != kind || name == "" || len([]rune(name)) > MaxStarGiftTitleRunes ||
			attribute.RarityPermille <= 0 || attribute.RarityPermille > 1000 {
			return ErrStarGiftCollectibleInvalid
		}
		key := strings.ToLower(name)
		if _, ok := seen[key]; ok {
			return ErrStarGiftCollectibleInvalid
		}
		seen[key] = struct{}{}
		total += attribute.RarityPermille
		switch kind {
		case StarGiftCollectibleModel, StarGiftCollectiblePattern:
			if attribute.Animation == nil || len(attribute.Animation.JSON) == 0 ||
				len(attribute.Animation.TGS) == 0 || len(attribute.Animation.SHA256) != 32 {
				return ErrStarGiftCollectibleInvalid
			}
			if requireStoredAsset && (attribute.Document == nil || !attribute.Document.IsSticker() ||
				attribute.Document.MimeType != "application/x-tgsticker" || attribute.Blob == nil) {
				return ErrStarGiftCollectibleInvalid
			}
		case StarGiftCollectibleBackdrop:
			if attribute.BackdropID <= 0 || attribute.Document != nil ||
				attribute.CenterColor < 0 || attribute.CenterColor > 0xffffff ||
				attribute.EdgeColor < 0 || attribute.EdgeColor > 0xffffff ||
				attribute.PatternColor < 0 || attribute.PatternColor > 0xffffff ||
				attribute.TextColor < 0 || attribute.TextColor > 0xffffff {
				return ErrStarGiftCollectibleInvalid
			}
		default:
			return ErrStarGiftCollectibleInvalid
		}
	}
	if total != 1000 {
		return ErrStarGiftCollectibleInvalid
	}
	return nil
}

// StarGiftCatalogHash 由客户端可见目录字段折叠出稳定 hash，供 getStarGifts NotModified。
func StarGiftCatalogHash(catalog []StarGift) int {
	var h uint64
	for _, g := range catalog {
		h ^= uint64(g.ID)
		h = h*0x4f25 + uint64(g.ID)
		h = h*0x4f25 + uint64(g.RevisionID)
		h = h*0x4f25 + uint64(g.Stars)
		h = h*0x4f25 + uint64(g.ConvertStars)
		h = h*0x4f25 + uint64(g.UpgradeStars)
		h = h*0x4f25 + uint64(g.UpgradeTotal)
		h = h*0x4f25 + uint64(g.UpgradeIssued)
		h = h*0x4f25 + uint64(g.Sticker.ID)
		for _, r := range g.Title {
			h = h*131 + uint64(r)
		}
	}
	return int(h & 0x7fffffff)
}

// StarGiftCollectionsHash 按服务端返回顺序折叠每个集合自己的稳定 hash。
func StarGiftCollectionsHash(collections []StarGiftCollection) int64 {
	var h uint64
	for _, collection := range collections {
		h = h*0x4f25 + uint64(collection.Hash)
	}
	return int64(h & 0x7fffffffffffffff)
}

// StarGiftCollectionHash returns the per-collection hash exposed by starGiftCollection.hash.
func StarGiftCollectionHash(title string, giftIDs []int64) int64 {
	h := uint64(0x534743)
	for _, r := range title {
		h = h*131 + uint64(r)
	}
	for _, id := range giftIDs {
		h = h*0x4f25 + uint64(id)
	}
	return int64(h & 0x7fffffffffffffff)
}

// EncodeStarGiftCursor / DecodeStarGiftCursor 是 saved gifts keyset 游标（最后一条实例 id）。
func EncodeStarGiftCursor(id int64) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatInt(id, 10)))
}

// DecodeStarGiftCursor 反解游标；无法解析（含空串）返回 ok=false（调用方从首页开始）。
func DecodeStarGiftCursor(s string) (int64, bool) {
	if s == "" {
		return 0, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return 0, false
	}
	id, err := strconv.ParseInt(string(raw), 10, 64)
	if err != nil || id <= 0 {
		return 0, false
	}
	return id, true
}
