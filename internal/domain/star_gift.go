package domain

import (
	"encoding/base64"
	"errors"
	"fmt"
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

	// Layer 228 regular-gift shape. Static release facts live in the immutable
	// catalog revision; AvailabilityRemains/AvailabilityResale are the current
	// inventory projection maintained on the catalog aggregate.
	Limited              bool
	SoldOut              bool
	Birthday             bool
	RequirePremium       bool
	LimitedPerUser       bool
	PeerColorAvailable   bool
	Auction              bool
	AvailabilityRemains  int
	AvailabilityTotal    int
	AvailabilityResale   int64
	FirstSaleDate        int
	LastSaleDate         int
	ResellMinStars       int64
	ReleasedBy           Peer
	PerUserTotal         int
	PerUserRemains       int
	LockedUntilDate      int
	AuctionSlug          string
	GiftsPerRound        int
	AuctionStartDate     int
	AuctionRoundDuration int
	UpgradeVariants      int
	Background           *StarGiftBackground
}

// StarGiftBackground is the release-level palette used by auction cards and
// gift previews before a collectible backdrop is selected.
type StarGiftBackground struct {
	CenterColor int
	EdgeColor   int
	TextColor   int
}

// SavedStarGift 是一条已收到的礼物实例（peer_star_gifts 一行）。
type SavedStarGift struct {
	ID                       int64
	Owner                    Peer  // 收礼 peer（user/channel）
	FromUserID               int64 // 送礼人（匿名也保留真实值供账本，下发时按 NameHidden 决定是否暴露）
	GiftID                   int64 // → StarGift.ID
	RevisionID               int64 // → star_gift_catalog_revisions.id，历史查询必须按此版本投影
	MsgID                    int   // 用户礼物的私聊 msg_id；频道礼物不进历史，固定为 0
	SavedID                  int64 // 频道礼物 inputSavedStarGiftChat.saved_id；用户礼物为 0
	Date                     int   // 收到时刻 Unix 秒
	NameHidden               bool  // 送礼人请求隐藏姓名
	Unsaved                  bool  // 未展示在个人资料（saveStarGift 切换）
	Converted                bool  // 已转换回 Stars（终态，从列表排除）
	LifecycleStatus          StarGiftLifecycleStatus
	ConvertStars             int64  // 转换可退回的 Stars
	PrepaidUpgradeStars      int64  // 送礼人随礼物预付的唯一礼物升级额
	PrepaidUpgradeHash       string // 第三方单独代付升级的一次性 entitlement
	GiftNum                  int    // auction-acquired release number for regular gifts
	PaidStars                int64  // 实际成交价（拍卖中标价）；0 表示按目录 revision 价投影
	Message                  string // 附言（可选）
	MessageEntities          []MessageEntity
	UniqueGiftID             int64 // 非 0 表示已升级为唯一礼物；与 Converted 互斥
	TransferStars            int64
	CanExportAt              int
	CanTransferAt            int
	CanResellAt              int
	DropOriginalDetailsStars int64
	CanCraftAt               int
	UpgradeMsgID             int   // 当前 owner 侧承载 messageActionStarGiftUnique 的消息 id；所有权转移时随新消息更新
	PinnedOrder              int   // >0 表示资料页置顶顺序
	CollectionIDs            []int // 当前所属集合；按集合顺序稳定返回
	Unique                   *UniqueStarGift
}

type StarGiftLifecycleStatus string

const (
	StarGiftLifecycleActive    StarGiftLifecycleStatus = "active"
	StarGiftLifecycleConverted StarGiftLifecycleStatus = "converted"
	StarGiftLifecycleBurned    StarGiftLifecycleStatus = "burned"
	StarGiftLifecycleExported  StarGiftLifecycleStatus = "exported"
	// StarGiftLifecycleRevoked marks an instance an admin removed via
	// "delete gift everywhere" -- distinct from every user-initiated
	// terminal state above. Used for a REGULAR (never-upgraded) instance;
	// an already-unique one still goes through the existing Burned path.
	StarGiftLifecycleRevoked StarGiftLifecycleStatus = "revoked"
)

func (s StarGiftLifecycleStatus) Live() bool {
	return s == StarGiftLifecycleActive
}

// StarGiftCollectibleAttributeKind 是唯一礼物三个必选属性槽位。
type StarGiftCollectibleAttributeKind string

const (
	StarGiftCollectibleModel    StarGiftCollectibleAttributeKind = "model"
	StarGiftCollectiblePattern  StarGiftCollectibleAttributeKind = "pattern"
	StarGiftCollectibleBackdrop StarGiftCollectibleAttributeKind = "backdrop"
)

// StarGiftAttributeRarityKind mirrors the Layer 228 rarity union. Permille is the only
// kind eligible for a regular upgrade draw; named rarities are currently used by
// craft-only models and must still be preserved in the published attribute directory.
type StarGiftAttributeRarityKind string

const (
	StarGiftRarityPermille  StarGiftAttributeRarityKind = "permille"
	StarGiftRarityUncommon  StarGiftAttributeRarityKind = "uncommon"
	StarGiftRarityRare      StarGiftAttributeRarityKind = "rare"
	StarGiftRarityEpic      StarGiftAttributeRarityKind = "epic"
	StarGiftRarityLegendary StarGiftAttributeRarityKind = "legendary"
)

func (k StarGiftAttributeRarityKind) Valid() bool {
	switch k {
	case StarGiftRarityPermille, StarGiftRarityUncommon, StarGiftRarityRare,
		StarGiftRarityEpic, StarGiftRarityLegendary:
		return true
	default:
		return false
	}
}

// StarGiftCollectibleAttribute 是已发布属性池的一项。RarityKind/RarityPermille
// 是客户端展示事实；普通升级把非 crafted 的 permille 值当相对权重，不要求合计为 1000。
// 每类仍必须提供至少两个客户端可区分的普通升级属性，否则 TDesktop 的升级滚动无法结束。
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
	RarityKind            StarGiftAttributeRarityKind
	RarityPermille        int
	Crafted               bool
	OfficialDocumentID    int64
	SortOrder             int
	Animation             *StarGiftAnimation
	Blob                  *FileBlob
}

// StarGiftCollectibleRevision 是某普通礼物的一份不可变、可发布属性池。
type StarGiftCollectibleRevision struct {
	ID                   int64
	GiftID               int64
	Revision             int
	UpgradeStars         int64
	SupplyTotal          int
	Issued               int
	SlugPrefix           string
	Published            bool
	Models               []StarGiftCollectibleAttribute
	Patterns             []StarGiftCollectibleAttribute
	Backdrops            []StarGiftCollectibleAttribute
	CreatedBy            string
	CreatedAt            time.Time
	PublishedAt          time.Time
	OfficialGiftID       int64
	SourceManifestSHA256 []byte
}

// StarGiftCollectibleWrite 是后台创建/发布属性池的协议无关输入。
type StarGiftCollectibleWrite struct {
	GiftID               int64
	UpgradeStars         int64
	SupplyTotal          int
	SlugPrefix           string
	Models               []StarGiftCollectibleAttribute
	Patterns             []StarGiftCollectibleAttribute
	Backdrops            []StarGiftCollectibleAttribute
	Actor                string
	CommandID            string
	OfficialGiftID       int64
	SourceManifestSHA256 []byte
}

// UniqueStarGift 是一份已经发行的唯一礼物。属性、编号与 slug 一经创建永久不变。
type UniqueStarGift struct {
	ID                      int64
	GiftID                  int64
	CollectibleRevisionID   int64
	SourceSavedGiftID       int64
	Title                   string
	Slug                    string
	Num                     int
	Owner                   Peer
	RequirePremium          bool
	ResaleTonOnly           bool
	ThemeAvailable          bool
	Burned                  bool
	Crafted                 bool
	OwnerName               string
	OwnerAddress            string
	GiftAddress             string
	ResellAmount            *StarGiftAmount
	ResellVersion           int64
	ReleasedBy              Peer
	ValueAmount             int64
	ValueCurrency           string
	ValueUSD                int64
	ThemePeer               Peer
	Host                    Peer
	OfferMinStars           int
	CraftChancePermille     int
	LastSaleDate            int
	LastSaleAmount          *StarGiftAmount
	Model                   StarGiftCollectibleAttribute
	Pattern                 StarGiftCollectibleAttribute
	Backdrop                StarGiftCollectibleAttribute
	AvailabilityIssued      int
	AvailabilityTotal       int
	KeepOriginalDetails     bool
	OriginalFromUserID      int64
	OriginalOwner           Peer
	OriginalDate            int
	OriginalMessage         string
	OriginalMessageEntities []MessageEntity
	OriginalNameHidden      bool
	CreatedAt               time.Time
}

// CollectibleEmojiStatus projects an immutable unique gift into the complete
// status shape consumed by Telegram clients.  Ownership/lifecycle validation
// is intentionally performed by the caller because it depends on the actor;
// this helper validates only the immutable renderable facts.
func CollectibleEmojiStatus(g UniqueStarGift) (EmojiStatusCollectible, bool) {
	status := EmojiStatusCollectible{
		CollectibleID: g.ID,
		Title:         g.Title,
		Slug:          g.Slug,
		CenterColor:   g.Backdrop.CenterColor,
		EdgeColor:     g.Backdrop.EdgeColor,
		PatternColor:  g.Backdrop.PatternColor,
		TextColor:     g.Backdrop.TextColor,
	}
	if g.Model.Document != nil {
		status.DocumentID = g.Model.Document.ID
	}
	if g.Pattern.Document != nil {
		status.PatternDocumentID = g.Pattern.Document.ID
	}
	return status, status.Valid()
}

type StarGiftCurrency string

const (
	StarGiftCurrencyStars StarGiftCurrency = "XTR"
	StarGiftCurrencyTON   StarGiftCurrency = "TON"
)

// MaxStarGiftAuctionBidStars caps a single auction bid so the bid ladder stays
// inside the range official clients can represent. Telegram Desktop derives its
// bid-slider maximum from the current top bid in 32-bit arithmetic — int(top * 1.2)
// in payments_reaction_box.cpp — and asserts that the result is at least 2. A top
// bid above roughly 1.79e9 overflows that int, the assertion fails, and the client
// dies for everyone who opens the auction, not just the bidder who set the price.
// Since every bid must clear the one before it, an uncapped auction escalates into
// that range on its own.
const MaxStarGiftAuctionBidStars int64 = 10_000_000

type StarGiftAmount struct {
	Currency StarGiftCurrency
	Amount   int64
	Nanos    int
}

func (a StarGiftAmount) Valid() bool {
	if a.Amount <= 0 {
		return false
	}
	switch a.Currency {
	case StarGiftCurrencyStars:
		return a.Nanos >= -999999999 && a.Nanos <= 999999999
	case StarGiftCurrencyTON:
		return a.Nanos == 0
	default:
		return false
	}
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

	// Admin-controlled attribute overrides. When non-zero these pin the specific
	// collectible model/pattern/backdrop instead of the random pool draw. They
	// are only honoured on the admin grant path; the DB FK (attribute must belong
	// to the revision) remains the source of truth. The collectible number is
	// always assigned automatically (sequential).
	ModelAttributeID    int64
	PatternAttributeID  int64
	BackdropAttributeID int64
}

// AdminStarGiftGrant is one admin "give gift" command: deliver GiftID to
// Recipient from the official system account 777000 at no charge.
// When Upgrade is set the gift is minted as a collectible; the optional
// attribute IDs pin specific model/pattern/backdrop (0 => random). The
// collectible number is always assigned automatically.
type AdminStarGiftGrant struct {
	SenderID            int64
	Recipient           Peer
	GiftID              int64
	HideName            bool
	Message             string
	Upgrade             bool
	CommandKey          string
	Date                int
	RecipientBlocked    bool
	RecipientUnsaved    bool
	ModelAttributeID    int64
	PatternAttributeID  int64
	BackdropAttributeID int64
}

// AdminStarGiftGrantResult is the committed direct collectible assignment.
// The saved gift, unique issuance, private message and replay receipt are one
// aggregate transaction.
type AdminStarGiftGrantResult struct {
	Saved     SavedStarGift
	Unique    UniqueStarGift
	Send      SendPrivateTextResult
	Duplicate bool
}

type StarGiftPurchaseRequest struct {
	BuyerUserID      int64
	BuyerPremium     bool
	To               Peer
	GiftID           int64
	RevisionID       int64
	IncludeUpgrade   bool
	HideName         bool
	Message          string
	MessageEntities  []MessageEntity
	ChargeStars      int64
	FormID           int64
	CommandKey       string
	Date             int
	RecipientBlocked bool
	RecipientUnsaved bool
	OriginAuthKeyID  [8]byte
	OriginSessionID  int64
}

// StarGiftPurchaseForm is the server-issued, short-lived payment intent that
// binds payments.getPaymentForm to one later payments.sendStarsForm call. A
// fresh form represents a fresh purchase even when every invoice field is the
// same; retrying one form represents the same purchase command.
type StarGiftPurchaseForm struct {
	FormID          int64
	BuyerUserID     int64
	To              Peer
	GiftID          int64
	RevisionID      int64
	IncludeUpgrade  bool
	HideName        bool
	Message         string
	MessageEntities []MessageEntity
	ChargeStars     int64
	IssuedAt        int
	ExpiresAt       int
}

type StarGiftPurchaseResult struct {
	Gift      StarGift
	Saved     SavedStarGift
	Balance   StarsBalance
	Send      SendPrivateTextResult
	Duplicate bool
}

// StarGiftConvertRequest identifies one owner-scoped regular gift conversion.
// ActorUserID is the authenticated user who owns the user gift or administers
// the channel gift; authorization is checked again at the RPC boundary.
type StarGiftConvertRequest struct {
	ActorUserID int64
	Ref         SavedStarGiftRef
	Date        int
}

// StarGiftConvertResult exposes the committed aggregate state. OwnerBalance is
// the post-credit balance of either the user or the channel internal Stars
// ledger selected by Saved.Owner.
type StarGiftConvertResult struct {
	Saved        SavedStarGift
	OwnerBalance int64
}

type StarGiftUpgradeResult struct {
	Saved       SavedStarGift
	Unique      UniqueStarGift
	Balance     StarsBalance
	Send        SendPrivateTextResult
	SourceEdits []EditedMessageForUser
	Duplicate   bool
}

// StarGiftUpgradeReceipt is the immutable command envelope needed to replay a
// committed upgrade after the saved gift has entered its unique terminal state.
// In particular, a paid replay must not be rebound to a later catalog price.
type StarGiftUpgradeReceipt struct {
	UserID              int64
	SourceSavedGiftID   int64
	FormID              int64
	UniqueGiftID        int64
	ChargeStars         int64
	BalanceAfter        int64
	SourceEditPts       int
	RequirePrepaid      bool
	KeepOriginalDetails bool
}

type StarGiftPrepaidUpgradeRequest struct {
	PayerUserID     int64
	Owner           Peer
	Hash            string
	ChargeStars     int64
	FormID          int64
	CommandKey      string
	Date            int
	OriginAuthKeyID [8]byte
	OriginSessionID int64
}

type StarGiftPrepaidUpgradeResult struct {
	Saved     SavedStarGift
	Balance   StarsBalance
	Send      SendPrivateTextResult
	Duplicate bool
}

type StarGiftDropOriginalDetailsRequest struct {
	UserID      int64
	Ref         SavedStarGiftRef
	ChargeStars int64
	FormID      int64
	CommandKey  string
	Date        int
}

type StarGiftDropOriginalDetailsResult struct {
	Saved     SavedStarGift
	Unique    UniqueStarGift
	Balance   StarsBalance
	Duplicate bool
}

// StarGiftLifecyclePolicy is the server-owned policy snapshotted when a regular
// gift becomes collectible. It deliberately contains no wallet/node/provider
// configuration: TON remains only a currency unit in the local ledger.
type StarGiftLifecyclePolicy struct {
	TransferStars            int64
	DropOriginalDetailsStars int64
	OfferMinStars            int
	ExportDelaySeconds       int
	TransferDelaySeconds     int
	ResellDelaySeconds       int
	CraftDelaySeconds        int
	CraftChancePermille      int
}

func (p StarGiftLifecyclePolicy) Valid() bool {
	return p.TransferStars >= 0 && p.DropOriginalDetailsStars >= 0 && p.OfferMinStars >= 0 &&
		p.ExportDelaySeconds >= 0 && p.TransferDelaySeconds >= 0 && p.ResellDelaySeconds >= 0 &&
		p.CraftDelaySeconds >= 0 && p.CraftChancePermille >= 0 && p.CraftChancePermille <= 1000
}

// StarGiftMarketPolicy is snapshotted in the running aggregate coordinator.
// Proceeds permille is the seller share; the remainder is recorded as platform
// commission. TON is still only a unit in the local ledger.
type StarGiftMarketPolicy struct {
	StarsProceedsPermille int
	TONProceedsPermille   int
}

func (p StarGiftMarketPolicy) Valid() bool {
	return p.StarsProceedsPermille >= 0 && p.StarsProceedsPermille <= 1000 &&
		p.TONProceedsPermille >= 0 && p.TONProceedsPermille <= 1000
}

type StarGiftResaleFilter struct {
	GiftID      int64
	SortByPrice bool
	SortByNum   bool
	ForCraft    bool
	StarsOnly   bool
	ModelIDs    []int64
	PatternIDs  []int64
	BackdropIDs []int64
	Offset      string
	Limit       int
}

type StarGiftResalePage struct {
	Gifts      []UniqueStarGift
	Count      int
	NextOffset string
}

type StarGiftValueInfo struct {
	Currency         string
	Value            int64
	ValueIsAverage   bool
	InitialSaleDate  int
	InitialSaleStars int64
	InitialSalePrice int64
	LastSaleDate     int
	LastSalePrice    int64
	FloorPrice       int64
	AveragePrice     int64
	ListedCount      int
}

type StarGiftTransferRequest struct {
	ActorUserID      int64
	Ref              SavedStarGiftRef
	To               Peer
	ChargeStars      int64
	FormID           int64
	CommandKey       string
	Date             int
	RecipientUnsaved bool
	OriginAuthKeyID  [8]byte
	OriginSessionID  int64
}

type StarGiftTransferResult struct {
	Saved     SavedStarGift
	Unique    UniqueStarGift
	Balance   StarsBalance
	Send      SendPrivateTextResult
	Duplicate bool
}

// StarGiftDeletionCompensation is one user's Stars refund for a deleted
// gift, broken down by the transaction kind it came from -- the admin panel
// shows this breakdown so an operator can see exactly what was paid before
// confirming.
type StarGiftDeletionCompensation struct {
	UserID   int64
	Purchase int64
	Transfer int64
	Upgrade  int64
}

func (c StarGiftDeletionCompensation) Total() int64 { return c.Purchase + c.Transfer + c.Upgrade }

// StarGiftDeletionResult summarizes what "delete gift everywhere" removed.
// RevokedInstances counts every previously-active peer_star_gifts row this
// touched, upgraded (burned) and regular (revoked) combined.
type StarGiftDeletionResult struct {
	GiftID            int64
	RevokedInstances  int
	CancelledListings int
	CancelledOffers   int
	Refunded          bool
	Compensation      []StarGiftDeletionCompensation
	TotalCompensation int64
}

type StarGiftListingRequest struct {
	ActorUserID int64
	Ref         SavedStarGiftRef
	Amount      *StarGiftAmount
	Date        int
}

type StarGiftResalePurchaseRequest struct {
	BuyerUserID      int64
	Slug             string
	To               Peer
	Amount           StarGiftAmount
	FormID           int64
	CommandKey       string
	Date             int
	RecipientUnsaved bool
	OriginAuthKeyID  [8]byte
	OriginSessionID  int64
}

type StarGiftOfferRequest struct {
	BuyerUserID     int64
	Owner           Peer
	Slug            string
	Price           StarGiftAmount
	Duration        int
	RandomID        int64
	Date            int
	OriginAuthKeyID [8]byte
	OriginSessionID int64
}

type StarGiftOffer struct {
	ID           int64
	BuyerUserID  int64
	Owner        Peer
	UniqueGiftID int64
	Price        StarGiftAmount
	RandomID     int64
	OfferMsgID   int
	BuyerMsgID   int
	Status       string
	CreatedAt    int
	ExpiresAt    int
	ResolvedAt   int
	Gift         UniqueStarGift
}

type StarGiftOfferResult struct {
	Offer     StarGiftOffer
	Saved     SavedStarGift
	Unique    UniqueStarGift
	Balance   StarsBalance
	Send      SendPrivateTextResult
	Duplicate bool
}

type StarGiftResolveOfferRequest struct {
	OwnerUserID     int64
	OfferMsgID      int
	Decline         bool
	Date            int
	OriginAuthKeyID [8]byte
	OriginSessionID int64
}

type StarGiftCraftRequest struct {
	UserID          int64
	Refs            []SavedStarGiftRef
	CommandKey      string
	Date            int
	OriginAuthKeyID [8]byte
	OriginSessionID int64
}

type StarGiftCraftResult struct {
	Success     bool
	Chance      int
	Gift        *UniqueStarGift
	Send        SendPrivateTextResult
	SourceEdits []EditedMessageForUser
	Duplicate   bool
}

type StarGiftAuction struct {
	Gift          StarGift
	Version       int
	StartDate     int
	EndDate       int
	MinBidAmount  int64
	NextRoundAt   int
	LastGiftNum   int
	GiftsLeft     int
	CurrentRound  int
	TotalRounds   int
	RoundDuration int
	BidLevels     []StarGiftAuctionBidLevel
	TopBidders    []int64
	UserState     StarGiftAuctionUserState
	Finished      bool
	AveragePrice  int64
	ListedCount   int
}

type StarGiftAuctionBidLevel struct {
	Pos    int
	Amount int64
	Date   int
}

type StarGiftAuctionUserState struct {
	Returned      bool
	BidAmount     int64
	BidDate       int
	MinBidAmount  int64
	BidPeer       Peer
	AcquiredCount int
	// BidVersion is this bidder's own generation counter for this auction. It is
	// bumped by every write to their bid row — placing it, raising it, winning a
	// round, being refunded — and by nothing anyone else does. Payment forms are
	// bound to it so a bid placed after the previous one was settled can never
	// reuse the settled bid's durable payment receipt. It is deliberately kept
	// after the bid goes inactive, because that is exactly the case the binding
	// has to separate. Not projected to clients.
	BidVersion int64
}

type StarGiftAuctionBidRequest struct {
	UserID    int64
	GiftID    int64
	Peer      Peer
	BidAmount int64
	HideName  bool
	Message   string
	UpdateBid bool
	FormID    int64
	Date      int
}

type StarGiftAuctionAcquired struct {
	Peer       Peer
	Date       int
	BidAmount  int64
	Round      int
	Pos        int
	Message    string
	GiftNum    int
	NameHidden bool
}

type StarGiftWithdrawalRequest struct {
	UserID int64
	Ref    SavedStarGiftRef
	Date   int
}

type StarGiftWithdrawal struct {
	ProviderRequestID string
	URL               string
	ExpiresAt         int
	Status            string
	Gift              UniqueStarGift
}

// ChannelRevenueCurrency identifies the two isolated channel revenue ledgers.
// TON here is telesrv's internal nanoton balance, not an on-chain transfer.
type ChannelRevenueCurrency string

const (
	ChannelRevenueStars ChannelRevenueCurrency = "stars"
	ChannelRevenueTON   ChannelRevenueCurrency = "ton"
)

func (c ChannelRevenueCurrency) Valid() bool {
	return c == ChannelRevenueStars || c == ChannelRevenueTON
}

// ChannelRevenueWithdrawalRequest is the immutable, bearer-token-backed claim
// command persisted when payments.getStarsRevenueWithdrawalUrl succeeds.
// TokenDigest is SHA-256(token); plaintext bearer tokens never cross the store
// boundary or enter the database.
type ChannelRevenueWithdrawalRequest struct {
	ChannelID              int64
	CreatorUserID          int64
	Currency               ChannelRevenueCurrency
	Amount                 int64 // zero means bind the full balance at issuance
	PasswordChangedAt      time.Time
	AuthKeyID              [8]byte
	AuthorizationCreatedAt time.Time
	TokenDigest            []byte
	Date                   int
	ExpiresAt              int
}

// ChannelRevenuePasswordStateChangedError means payout admission observed a
// different password state than the one whose SRP proof was checked. It carries
// no password material; callers use it only to return the official freshness
// or missing-password error while the store transaction fails closed.
type ChannelRevenuePasswordStateChangedError struct {
	HasPassword       bool
	PasswordChangedAt time.Time
}

func (e *ChannelRevenuePasswordStateChangedError) Error() string {
	return "channel revenue: password state changed during withdrawal admission"
}

// ChannelRevenueAuthorizationStateChangedError means payout admission no
// longer observes the exact fully-authorized session whose age the RPC checked.
// The public bearer command is not persisted when this error is returned.
type ChannelRevenueAuthorizationStateChangedError struct {
	HasAuthorization bool
	OwnerMatches     bool
	PasswordPending  bool
	CreatedAt        time.Time
}

func (e *ChannelRevenueAuthorizationStateChangedError) Error() string {
	return "channel revenue: authorization state changed during withdrawal admission"
}

// ChannelRevenueWithdrawal is both the confirmation-page projection and the
// durable completion receipt. Completed requests are exact, idempotent replays.
type ChannelRevenueWithdrawal struct {
	ID                   int64
	URL                  string
	ChannelID            int64
	CreatorUserID        int64
	Currency             ChannelRevenueCurrency
	Amount               int64
	Status               string
	ExpiresAt            int
	CreatedAt            int
	CompletedAt          int
	ChannelTransactionID int64
	UserTransactionID    int64
	ChannelBalanceAfter  int64
	UserBalanceAfter     int64
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
	GiftID               int64 // 0 创建新礼物；非 0 为该礼物创建新 revision
	Title                string
	Stars                int64
	ConvertStars         int64
	Enabled              bool
	SortOrder            int
	Document             Document
	Blob                 FileBlob
	Animation            StarGiftAnimation
	Actor                string
	CommandID            string
	OfficialGiftID       int64
	SourceManifestSHA256 []byte
	OfficialSourceJSON   []byte
	Limited              bool
	SoldOut              bool
	Birthday             bool
	RequirePremium       bool
	LimitedPerUser       bool
	PeerColorAvailable   bool
	Auction              bool
	AvailabilityRemains  int
	AvailabilityTotal    int
	AvailabilityResale   int64
	FirstSaleDate        int
	LastSaleDate         int
	ResellMinStars       int64
	ReleasedBy           Peer
	PerUserTotal         int
	LockedUntilDate      int
	AuctionSlug          string
	GiftsPerRound        int
	AuctionStartDate     int
	AuctionRoundDuration int
	UpgradeVariants      int
	Background           *StarGiftBackground
}

// ValidateLifecycleAuthoring validates the optional auction and scheduled-release
// parameters an operator may attach when authoring a catalog revision through the
// admin panel. Zero values describe an ordinary gift and always pass; this is the
// authoring boundary for the auction panel and the scheduled-release surfaces. now
// is the current time in Unix seconds. The official-gift import path builds its
// bundle directly from Telegram's snapshot and does not go through this check, so
// the rules here may be stricter than the official model.
func (w StarGiftCatalogWrite) ValidateLifecycleAuthoring(now int) error {
	if w.GiftsPerRound < 0 || w.AvailabilityTotal < 0 || w.AuctionStartDate < 0 ||
		w.LockedUntilDate < 0 || w.AuctionRoundDuration < 0 {
		return fmt.Errorf("%w: negative field", ErrStarGiftLifecycleInvalid)
	}
	if w.Auction {
		if strings.TrimSpace(w.AuctionSlug) == "" {
			return fmt.Errorf("%w: auction requires a slug", ErrStarGiftLifecycleInvalid)
		}
		if w.GiftsPerRound <= 0 {
			return fmt.Errorf("%w: auction requires gifts_per_round > 0", ErrStarGiftLifecycleInvalid)
		}
		if w.AvailabilityTotal <= 0 {
			return fmt.Errorf("%w: auction requires availability_total > 0", ErrStarGiftLifecycleInvalid)
		}
		if w.GiftsPerRound > w.AvailabilityTotal {
			return fmt.Errorf("%w: gifts_per_round exceeds availability_total", ErrStarGiftLifecycleInvalid)
		}
		if w.AuctionStartDate != 0 && w.AuctionStartDate < now {
			return fmt.Errorf("%w: auction start date is in the past", ErrStarGiftLifecycleInvalid)
		}
		if w.LockedUntilDate != 0 {
			return fmt.Errorf("%w: auctions do not use a scheduled-release time", ErrStarGiftLifecycleInvalid)
		}
		// The gift price is the auction's opening bid (ensureStarGiftAuction seeds
		// min_bid_amount from it), so it is bound by the same ceiling as a bid.
		// Rejecting it here means an auction cannot be authored into a state where
		// no legal bid exists.
		if w.Stars > MaxStarGiftAuctionBidStars {
			return fmt.Errorf("%w: auction opening bid exceeds %d stars", ErrStarGiftLifecycleInvalid, MaxStarGiftAuctionBidStars)
		}
		return nil
	}
	// Non-auction gift: reject stray auction-only parameters, but allow
	// LockedUntilDate, which is exactly the scheduled-release gate.
	if strings.TrimSpace(w.AuctionSlug) != "" || w.GiftsPerRound != 0 ||
		w.AuctionStartDate != 0 || w.AuctionRoundDuration != 0 {
		return fmt.Errorf("%w: auction parameters set on a non-auction gift", ErrStarGiftLifecycleInvalid)
	}
	if w.LockedUntilDate != 0 && w.LockedUntilDate <= now {
		return fmt.Errorf("%w: scheduled-release time must be in the future", ErrStarGiftLifecycleInvalid)
	}
	return nil
}

// NormalizeLifecycleAuthoring derives the revision fields that the catalog's own
// CHECK constraints require but that an operator never states directly. Call it
// after ValidateLifecycleAuthoring, on the same write.
//
// star_gift_catalog_revision_auction_check demands that an auction be limited,
// carry a non-empty slug, and have gifts_per_round > 0 and auction_start_date > 0;
// the supply check separately demands "limited AND availability_total > 0". So an
// auction is by definition a limited gift, and a zero start date — which the
// validator accepts as "start now", and which ensureStarGiftAuction resolves the
// same way — has to become a concrete timestamp or the INSERT is rejected.
// AvailabilityRemains seeds the client's "N left" projection; auction settlement
// keeps it in step with gifts_left afterwards.
func (w *StarGiftCatalogWrite) NormalizeLifecycleAuthoring(now int) {
	if w.Auction {
		w.Limited = true
		if w.AuctionStartDate <= 0 {
			w.AuctionStartDate = now
		}
		if w.AvailabilityRemains <= 0 {
			w.AvailabilityRemains = w.AvailabilityTotal
		}
		return
	}
	// Bug fixed: this early-returned for every non-auction gift, so an
	// operator-supplied AvailabilityTotal on a plain (non-auction) import --
	// a limited-edition regular gift, e.g. "only 10,000 available" with no
	// auction or scheduled drop attached -- silently never became Limited.
	// ValidateLifecycleAuthoring already allows AvailabilityTotal > 0 here
	// (it only rejects auction-only fields on a non-auction write), so
	// nothing upstream was actually gating this on Auction being true; it
	// just never got wired through. The same hard cap should apply
	// everywhere a gift has a real supply -- auctions, scheduled drops, and
	// ordinary gifts alike -- not only auctions.
	if w.AvailabilityTotal > 0 {
		w.Limited = true
		if w.AvailabilityRemains <= 0 {
			w.AvailabilityRemains = w.AvailabilityTotal
		}
	}
}

// StarGiftCatalogBundleWrite atomically publishes one catalog revision and its optional
// complete collectible pool. Collectible.GiftID is filled with the allocated local gift ID.
type StarGiftCatalogBundleWrite struct {
	Catalog     StarGiftCatalogWrite
	Collectible *StarGiftCollectibleWrite
}

type StarGiftCatalogBundleResult struct {
	Catalog     StarGiftCatalogEntry
	Collectible *StarGiftCollectibleRevision
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
// 用户礼物使用 inputSavedStarGiftUser.msg_id；频道礼物使用 inputSavedStarGiftChat.peer + saved_id；
// 已升级的唯一礼物也可使用官方 inputSavedStarGiftSlug.slug。三种身份必须互斥。
type SavedStarGiftRef struct {
	Owner   Peer
	MsgID   int
	SavedID int64
	Slug    string
}

// Valid reports whether the reference has the identity required by its owner kind.
func (r SavedStarGiftRef) Valid() bool {
	slug := strings.TrimSpace(r.Slug)
	validSlug := slug != "" && slug == r.Slug && len(slug) <= MaxStarGiftSlugBytes && r.MsgID == 0 && r.SavedID == 0
	switch r.Owner.Type {
	case PeerTypeUser:
		return r.Owner.ID != 0 && (validSlug || r.MsgID > 0 && r.SavedID == 0 && slug == "")
	case PeerTypeChannel:
		return r.Owner.ID != 0 && (validSlug || r.SavedID > 0 && r.MsgID == 0 && slug == "")
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

// SavedStarGiftListCursor is the composite keyset cursor for the profile gift
// order: pinned gifts first by PinnedOrder, then unpinned gifts by ID DESC.
// PinnedOrder == 0 identifies the unpinned segment.
type SavedStarGiftListCursor struct {
	PinnedOrder int
	ID          int64
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
	MaxStarGiftSlugBytes                    = 255
	MaxStarGiftCollectibleAttributesPerKind = 512
	MaxStarGiftCollectionTitleRunes         = 12
	MaxStarGiftCollectionsPerPeer           = 100
	MaxStarGiftCollectionItems              = 1000
	// MaxPinnedStarGifts matches stargifts_pinned_to_top_limit advertised to
	// official clients. Pin requests are complete replacement vectors.
	MaxPinnedStarGifts = 6
)

// Star gift 哨兵错误（rpc 层 errors.Is 映射为 tgerr）。
var (
	// ErrStarGiftInvalid 表示礼物 id 不在目录里。
	ErrStarGiftInvalid = errors.New("stargift: invalid gift id")
	// ErrStarGiftNotFound 表示找不到该已收到礼物实例。
	ErrStarGiftNotFound = errors.New("stargift: saved gift not found")
	// ErrStarGiftAlreadyConverted 表示礼物已转换回 Stars（不可重复转换）。
	ErrStarGiftAlreadyConverted            = errors.New("stargift: already converted")
	ErrStarGiftFileInvalid                 = errors.New("stargift: invalid animation file")
	ErrStarGiftCatalogFull                 = errors.New("stargift: catalog full")
	ErrStarGiftLifecycleInvalid            = errors.New("stargift: invalid auction or scheduled-release parameters")
	ErrStarGiftCollectibleUnavailable      = errors.New("stargift: collectible upgrade unavailable")
	ErrStarGiftAlreadyUpgraded             = errors.New("stargift: already upgraded")
	ErrStarGiftCollectibleSoldOut          = errors.New("stargift: collectible supply exhausted")
	ErrStarGiftCollectibleInvalid          = errors.New("stargift: invalid collectible definition")
	ErrStarGiftCollectionNotFound          = errors.New("stargift: collection not found")
	ErrStarGiftCollectionsFull             = errors.New("stargift: collections full")
	ErrStarGiftUnavailable                 = errors.New("stargift: unavailable")
	ErrStarGiftOwnerInvalid                = errors.New("stargift: owner invalid")
	ErrStarGiftTransferUnavailable         = errors.New("stargift: transfer unavailable")
	ErrStarGiftResaleUnavailable           = errors.New("stargift: resale unavailable")
	ErrStarGiftOfferInvalid                = errors.New("stargift: offer invalid")
	ErrStarGiftOfferExpired                = errors.New("stargift: offer expired")
	ErrStarGiftCraftUnavailable            = errors.New("stargift: craft unavailable")
	ErrStarGiftAuctionUnavailable          = errors.New("stargift: auction unavailable")
	ErrStarGiftWithdrawalUnavailable       = errors.New("stargift: withdrawal provider unavailable")
	ErrChannelRevenueWithdrawalInvalid     = errors.New("channel revenue: withdrawal invalid")
	ErrChannelRevenueWithdrawalExpired     = errors.New("channel revenue: withdrawal expired")
	ErrChannelRevenueInsufficient          = errors.New("channel revenue: insufficient balance")
	ErrChannelRevenueWithdrawalUnavailable = errors.New("channel revenue: withdrawal unavailable")
	ErrStarGiftFormExpired                 = errors.New("stargift: payment form expired")
	ErrStarGiftFormPurposeInvalid          = errors.New("stargift: payment form purpose invalid")
	ErrStarGiftFormAmountMismatch          = errors.New("stargift: payment form amount mismatch")
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
	if write.OfficialGiftID < 0 ||
		(write.OfficialGiftID == 0 && len(write.SourceManifestSHA256) != 0) ||
		(write.OfficialGiftID > 0 && len(write.SourceManifestSHA256) != 32) {
		return ErrStarGiftCollectibleInvalid
	}
	if err := validateStarGiftAttributes(write.Models, StarGiftCollectibleModel, false); err != nil {
		return err
	}
	if err := validateStarGiftAttributes(write.Patterns, StarGiftCollectiblePattern, false); err != nil {
		return err
	}
	if err := validateStarGiftAttributes(write.Backdrops, StarGiftCollectibleBackdrop, false); err != nil {
		return err
	}
	return validateStarGiftUpgradePreviewPool(write, false)
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
	if err := validateStarGiftAttributes(write.Backdrops, StarGiftCollectibleBackdrop, true); err != nil {
		return err
	}
	return validateStarGiftUpgradePreviewPool(write, true)
}

// validateStarGiftUpgradePreviewPool protects the official-client animation contract. The
// preview response includes the target attribute plus the published selectable pool; TDesktop
// deduplicates models and patterns by document identity and needs a non-target item in every
// category before its spinner can transition to the finished state.
func validateStarGiftUpgradePreviewPool(write StarGiftCollectibleWrite, requireStoredAsset bool) error {
	validateAnimated := func(kind StarGiftCollectibleAttributeKind, attributes []StarGiftCollectibleAttribute) error {
		selectable := 0
		documents := make(map[int64]struct{}, len(attributes))
		for _, attribute := range attributes {
			if attribute.RarityKind != StarGiftRarityPermille || attribute.Crafted {
				continue
			}
			selectable++
			if requireStoredAsset {
				if attribute.Document == nil {
					return fmt.Errorf("%w: %s preview attribute has no document", ErrStarGiftCollectibleInvalid, kind)
				}
				documents[attribute.Document.ID] = struct{}{}
			}
		}
		if selectable < 2 {
			return fmt.Errorf("%w: %s preview requires at least two selectable attributes", ErrStarGiftCollectibleInvalid, kind)
		}
		if requireStoredAsset && len(documents) < 2 {
			return fmt.Errorf("%w: %s preview requires at least two distinct documents", ErrStarGiftCollectibleInvalid, kind)
		}
		return nil
	}
	if err := validateAnimated(StarGiftCollectibleModel, write.Models); err != nil {
		return err
	}
	if err := validateAnimated(StarGiftCollectiblePattern, write.Patterns); err != nil {
		return err
	}
	seenBackdropIDs := make(map[int]struct{}, len(write.Backdrops))
	selectableBackdrops := 0
	for _, attribute := range write.Backdrops {
		if attribute.RarityKind != StarGiftRarityPermille || attribute.Crafted {
			continue
		}
		selectableBackdrops++
		if _, exists := seenBackdropIDs[attribute.BackdropID]; exists {
			return fmt.Errorf("%w: duplicate backdrop_id %d", ErrStarGiftCollectibleInvalid, attribute.BackdropID)
		}
		seenBackdropIDs[attribute.BackdropID] = struct{}{}
	}
	if selectableBackdrops < 2 {
		return fmt.Errorf("%w: backdrop preview requires at least two selectable attributes", ErrStarGiftCollectibleInvalid)
	}
	return nil
}

func validateStarGiftAttributes(attributes []StarGiftCollectibleAttribute, kind StarGiftCollectibleAttributeKind, requireStoredAsset bool) error {
	if len(attributes) == 0 || len(attributes) > MaxStarGiftCollectibleAttributesPerKind {
		return ErrStarGiftCollectibleInvalid
	}
	seen := make(map[string]struct{}, len(attributes))
	selectable := 0
	for _, attribute := range attributes {
		name := strings.TrimSpace(attribute.Name)
		rarityKind := attribute.RarityKind
		if attribute.Kind != kind || name == "" || len([]rune(name)) > MaxStarGiftTitleRunes || !rarityKind.Valid() {
			return ErrStarGiftCollectibleInvalid
		}
		if rarityKind == StarGiftRarityPermille {
			if attribute.RarityPermille <= 0 || attribute.RarityPermille > 1000 || attribute.Crafted {
				return ErrStarGiftCollectibleInvalid
			}
			selectable++
		} else if attribute.RarityPermille != 0 || !attribute.Crafted || kind != StarGiftCollectibleModel {
			return ErrStarGiftCollectibleInvalid
		}
		key := strings.ToLower(name)
		if _, ok := seen[key]; ok {
			return ErrStarGiftCollectibleInvalid
		}
		seen[key] = struct{}{}
		switch kind {
		case StarGiftCollectibleModel, StarGiftCollectiblePattern:
			if attribute.Animation == nil || len(attribute.Animation.JSON) == 0 ||
				len(attribute.Animation.TGS) == 0 || len(attribute.Animation.SHA256) != 32 {
				return ErrStarGiftCollectibleInvalid
			}
			if requireStoredAsset && (attribute.Document == nil ||
				!validStarGiftCollectibleDocument(*attribute.Document, kind) ||
				attribute.Document.MimeType != "application/x-tgsticker" || attribute.Blob == nil) {
				return ErrStarGiftCollectibleInvalid
			}
		case StarGiftCollectibleBackdrop:
			if attribute.BackdropID < 0 || attribute.Document != nil ||
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
	if selectable == 0 {
		return ErrStarGiftCollectibleInvalid
	}
	return nil
}

// validStarGiftCollectibleDocument enforces the client-visible document roles
// materialized by the Star Gift write boundary. Models are ordinary stickers.
// Patterns are text-color custom emoji with an inline PhotoPathSize so Android
// can classify and tint the TGS before its full first frame is downloaded.
func validStarGiftCollectibleDocument(document Document, kind StarGiftCollectibleAttributeKind) bool {
	renderAttributes := 0
	validRenderAttribute := false
	for _, attribute := range document.Attributes {
		switch attribute.Kind {
		case DocAttrSticker:
			renderAttributes++
			validRenderAttribute = validRenderAttribute || kind == StarGiftCollectibleModel
		case DocAttrCustomEmoji:
			renderAttributes++
			validRenderAttribute = validRenderAttribute ||
				(kind == StarGiftCollectiblePattern && attribute.TextColor)
		}
	}
	if renderAttributes != 1 || !validRenderAttribute {
		return false
	}
	if kind == StarGiftCollectibleModel {
		return true
	}
	for _, thumb := range document.Thumbs {
		if thumb.Kind == PhotoSizeKindPath && strings.TrimSpace(thumb.Type) != "" && len(thumb.Bytes) > 0 {
			return true
		}
	}
	return false
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

// EncodeSavedStarGiftListCursor encodes the exact profile-order key of the last
// visible gift. The version prefix keeps this cursor distinct from other star
// gift lists that are ordered only by instance ID.
func EncodeSavedStarGiftListCursor(pinnedOrder int, id int64) string {
	if pinnedOrder < 0 || id <= 0 {
		return ""
	}
	raw := "v1:" + strconv.Itoa(pinnedOrder) + ":" + strconv.FormatInt(id, 10)
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

// DecodeSavedStarGiftListCursor decodes a profile gift list cursor. Invalid or
// obsolete cursor shapes are rejected instead of being normalized on read.
func DecodeSavedStarGiftListCursor(s string) (SavedStarGiftListCursor, bool) {
	if s == "" {
		return SavedStarGiftListCursor{}, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return SavedStarGiftListCursor{}, false
	}
	parts := strings.Split(string(raw), ":")
	if len(parts) != 3 || parts[0] != "v1" {
		return SavedStarGiftListCursor{}, false
	}
	order, err := strconv.ParseInt(parts[1], 10, 32)
	if err != nil || order < 0 {
		return SavedStarGiftListCursor{}, false
	}
	id, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil || id <= 0 {
		return SavedStarGiftListCursor{}, false
	}
	return SavedStarGiftListCursor{PinnedOrder: int(order), ID: id}, true
}

// EncodeStarGiftCursor / DecodeStarGiftCursor are simple instance-ID cursors
// used by star gift lists whose order is strictly ID DESC (for example craft).
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
