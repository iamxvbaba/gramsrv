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

// Star gift (payments.sendStarsForm + inputInvoiceStarGift) domain model. The catalog and
// immutable revisions persist in star_gift_catalog(_revisions); gift instances received by
// a peer land in peer_star_gifts. Works with the Stars ledger: sending a gift debits,
// converting back to Stars credits.

// StarGift is a purchasable gift catalog entry. RevisionID identifies the immutable
// title/price/animation snapshot.
type StarGift struct {
	ID            int64
	RevisionID    int64
	Stars         int64    // purchase price (Stars)
	ConvertStars  int64    // Stars the recipient can convert back
	UpgradeStars  int64    // Stars required to upgrade to a unique gift; 0 means not currently upgradable
	UpgradeTotal  int      // total unique gifts the currently published attribute pool may issue
	UpgradeIssued int      // number currently issued
	Title         string   // optional title
	Sticker       Document // gift sticker snapshot (the tg projection must be a valid Document with a sticker attribute, or clients drop it)

	// Layer 228 regular-gift shape. Static release facts live in the immutable
	// catalog revision; AvailabilityRemains/AvailabilityResale are the current
	// inventory projection maintained on the catalog aggregate.
	Limited              bool
	SoldOut              bool
	Birthday             bool
	RequirePremium       bool
	SupportOnly          bool
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

// SavedStarGift is one received gift instance (a peer_star_gifts row).
type SavedStarGift struct {
	ID                       int64
	Owner                    Peer  // recipient peer (user/channel)
	FromUserID               int64 // sender (the real value is kept for the ledger even when anonymous; dispatch decides visibility by NameHidden)
	GiftID                   int64 // → StarGift.ID
	RevisionID               int64 // → star_gift_catalog_revisions.id; historical queries must project by this revision
	MsgID                    int   // private-chat msg_id for user gifts; channel gifts do not enter history and are fixed at 0
	SavedID                  int64 // inputSavedStarGiftChat.saved_id for channel gifts; 0 for user gifts
	Date                     int   // receive time, Unix seconds
	NameHidden               bool  // sender requested the name be hidden
	Unsaved                  bool  // not shown on the profile (toggled by saveStarGift)
	Converted                bool  // converted back to Stars (terminal; excluded from lists)
	LifecycleStatus          StarGiftLifecycleStatus
	ConvertStars             int64  // Stars refundable by conversion
	PrepaidUpgradeStars      int64  // unique-gift upgrade amount the sender prepaid with the gift
	PrepaidUpgradeHash       string // one-time entitlement for a third party paying for an upgrade separately
	GiftNum                  int    // auction-acquired release number for regular gifts
	PaidStars                int64  // actual transaction price (auction winning bid); 0 means project at the catalog revision price
	Message                  string // attached message (optional)
	MessageEntities          []MessageEntity
	UniqueGiftID             int64 // non-zero means upgraded to a unique gift; mutually exclusive with Converted
	TransferStars            int64
	CanExportAt              int
	CanTransferAt            int
	CanResellAt              int
	DropOriginalDetailsStars int64
	CanCraftAt               int
	UpgradeMsgID             int   // id of the current owner-side message carrying messageActionStarGiftUnique; updated with the new message on ownership transfer
	PinnedOrder              int   // >0 means profile pin order
	CollectionIDs            []int // collections it currently belongs to; returned stably in collection order
	Unique                   *UniqueStarGift
}

type StarGiftLifecycleStatus string

const (
	StarGiftLifecycleActive    StarGiftLifecycleStatus = "active"
	StarGiftLifecycleConverted StarGiftLifecycleStatus = "converted"
	StarGiftLifecycleBurned    StarGiftLifecycleStatus = "burned"
	StarGiftLifecycleExported  StarGiftLifecycleStatus = "exported"
	// StarGiftLifecycleDeleted is the terminal state of payments.deleteStarGift: the gift
	// leaves the profile page, but the history row is retained (upgrade commands, offers
	// and transfer records all reference it, so it cannot be hard-deleted).
	StarGiftLifecycleDeleted StarGiftLifecycleStatus = "deleted"
)

func (s StarGiftLifecycleStatus) Live() bool {
	return s == StarGiftLifecycleActive
}

// StarGiftCollectibleAttributeKind is the three required attribute slots of a unique gift.
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

// StarGiftCollectibleAttribute is one entry of the published attribute pool. RarityKind/
// RarityPermille are client-visible facts; a regular upgrade treats non-crafted permille
// values as relative weights and does not require them to sum to 1000. Each kind must still
// provide at least two client-distinguishable regular upgrade attributes, or TDesktop's
// upgrade spinning cannot finish.
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

// StarGiftCollectibleRevision is an immutable, publishable attribute pool for one regular gift.
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

// StarGiftCollectibleWrite is the protocol-agnostic input for creating/publishing an attribute pool from the backend.
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

// UniqueStarGift is one already-issued unique gift. Its attributes, number and slug are permanent once created.
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
	Unsaved                 bool // true when the source saved gift is hidden from the owner's profile
	CreatedAt               time.Time
}

// StarGiftClaimChallenge binds a short-lived, single-use TON Proof payload to
// one Gramsrv user and one already exported collectible.
type StarGiftClaimChallenge struct {
	Payload   string
	UserID    int64
	Unique    UniqueStarGift
	ExpiresAt int
}

type StarGiftOnChainClaim struct {
	Payload      string
	UserID       int64
	UniqueGiftID int64
	// ExpectedPreviousWallet is the last wallet observed by the database before
	// the TON proof was verified.  CommitClaim uses it as a compare-and-swap
	// guard so a transfer/reconciliation racing the claim cannot overwrite a
	// newer owner projection.
	ExpectedPreviousWallet string
	WalletAddress          string
	GiftAddress            string
	ClaimedAt              int
}

type StarGiftOnChainClaimResult struct {
	Gift            UniqueStarGift
	PreviousWallet  string
	ProfileUsername string
}

// StarGiftAdminExport marks a collectible as owned by a wallet purely in the
// database. It is a test-agent facility: no TON transaction is sent, so the
// projection is display-only and the ownership sync leaves it untouched.
type StarGiftAdminExport struct {
	UniqueGiftID  int64
	WalletName    string
	WalletAddress string
	ExportedBy    int64
	Date          int
}

// WHY: the panel addresses a gift by slug, NFT address or id and may host it on
// a chosen profile, so UniqueGiftID may be zero while Ref carries the lookup and
// HostPeer* overrides the relayer default the Mini App path relies on.
type StarGiftWalletBind struct {
	UniqueGiftID  int64
	Ref           string
	WalletName    string
	WalletAddress string
	ActorUserID   int64
	HostPeerType  string
	HostPeerID    int64
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

// StarGiftResaleFloorMultiple is the maximum magnitude a listing may exceed the
// gift's base release price and still count toward the dynamic resale floor
// (resell_min_stars / floor_price). The floor is computed as the cheapest active
// listing for the gift type, but a single overpriced "troll" listing (set far
// above the ~base price on purpose) would otherwise hijack that MIN and become
// the enforced floor for every future listing of the same gift. Listings above
// multiple×base are ignored by the floor projection while still remaining
// visible and purchasable — the floor stays market-driven and per-gift, never an
// absolute hardcoded minimum.
const StarGiftResaleFloorMultiple int64 = 10

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

// StarGiftUpgradePreview is the current price and sample attributes needed by the client's upgrade dialog.
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
	BuyerIsBot       bool
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

// StarGiftCraftSource is loaded by the server from owned collectible gifts.
type StarGiftCraftSource struct {
	UniqueID      int64
	GiftID        int64
	ModelID       int64
	ModelName     string
	AnimationJSON []byte
	Backdrop      StarGiftCollectibleAttribute
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

// StarGiftDeleteRequest is the input of payments.deleteStarGift: the caller wants to delete
// the regular gift they received. Upgraded collectibles, limited/auction gifts and gifts
// that still carry Stars cannot be deleted.
type StarGiftDeleteRequest struct {
	UserID int64
	Ref    SavedStarGiftRef
	Date   int
}

// StarGiftDeleteResult reports the deletion result. A gift upgraded to a collectible (NFT)
// cannot be deleted, so UniqueGiftID is always 0.
type StarGiftDeleteResult struct {
	SavedGiftID  int64
	UniqueGiftID int64
	Deleted      bool
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

// StarGiftCollection is a gift collection on a peer's profile; a gift may belong to several collections.
type StarGiftCollection struct {
	Owner        Peer
	CollectionID int
	Title        string
	GiftIDs      []int64 // peer_star_gifts.id, in collection order
	Hash         int64
	SortOrder    int
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// StarGiftCollectionPatch describes a partial update of updateStarGiftCollection.
type StarGiftCollectionPatch struct {
	Title     *string
	DeleteIDs []int64
	AddIDs    []int64
	Order     []int64
}

// StarGiftAnimationFormat is the backend import source format. The server always stores normalized TGS.
type StarGiftAnimationFormat string

const (
	StarGiftAnimationTGS    StarGiftAnimationFormat = "tgs"
	StarGiftAnimationLottie StarGiftAnimationFormat = "lottie"
)

// StarGiftAnimation is a normalized and validated animation. JSON is used for backend playback, TGS for clients.
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

// StarGiftCatalogWrite is the protocol-agnostic data the store needs to atomically create a catalog revision.
type StarGiftCatalogWrite struct {
	GiftID               int64 // 0 creates a new gift; non-zero creates a new revision for that gift
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
	SupportOnly          bool
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
	if w.AvailabilityTotal > 0 {
		w.Limited = true
	}
	if w.Auction {
		w.Limited = true
		if w.AuctionStartDate <= 0 {
			w.AuctionStartDate = now
		}
	}
	// Zero on an existing identity can mean exhausted, never uninitialized.
	// The store preserves its inventory while holding the same lock as purchases.
	// Negative values must not be silently repaired either.
	if w.GiftID == 0 && w.Limited && !w.SoldOut && w.AvailabilityRemains == 0 {
		w.AvailabilityRemains = w.AvailabilityTotal
	}
}

// PreserveCatalogInventory applies an existing identity's inventory to a new
// revision. Call at the write boundary while holding the catalog lock.
// Replacing a revision edits presentation/pricing, not its original issuance
// cap. A different cap or sales mode requires a new gift identity.
func (w *StarGiftCatalogWrite) PreserveCatalogInventory(current StarGift) error {
	if w.GiftID == 0 || w.GiftID != current.ID {
		return fmt.Errorf("%w: inventory identity mismatch", ErrStarGiftLifecycleInvalid)
	}
	if current.AvailabilityRemains < 0 || current.AvailabilityTotal < 0 ||
		(current.Limited && (current.AvailabilityTotal == 0 || current.AvailabilityRemains > current.AvailabilityTotal)) ||
		(!current.Limited && (current.AvailabilityTotal != 0 || current.AvailabilityRemains != 0)) {
		return fmt.Errorf("%w: invalid persisted inventory", ErrStarGiftLifecycleInvalid)
	}
	if w.AvailabilityTotal < 0 || (w.AvailabilityTotal != 0 && w.AvailabilityTotal != current.AvailabilityTotal) ||
		(w.Limited && !current.Limited) || w.Auction != current.Auction {
		return fmt.Errorf("%w: existing gift supply and sales mode are immutable; create a new gift", ErrStarGiftLifecycleInvalid)
	}
	// Omitted authoring fields mean preserve, not convert to unlimited.
	w.Limited, w.SoldOut = current.Limited, current.SoldOut
	w.AvailabilityTotal, w.AvailabilityRemains = current.AvailabilityTotal, current.AvailabilityRemains
	w.AvailabilityResale, w.ResellMinStars = current.AvailabilityResale, current.ResellMinStars
	w.FirstSaleDate, w.LastSaleDate = current.FirstSaleDate, current.LastSaleDate
	return nil
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

// StarGiftCatalogEntry is the admin backend catalog view.
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

// SavedStarGiftRef is the protocol-neutral reference for
// payments.getSavedStarGift/saveStarGift/convertStarGift. User gifts use
// inputSavedStarGiftUser.msg_id; channel gifts use inputSavedStarGiftChat.peer + saved_id;
// an upgraded unique gift may also use the official inputSavedStarGiftSlug.slug. The three
// identities must be mutually exclusive.
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

// SavedStarGiftPage is one page of received gifts plus a keyset pagination cursor.
type SavedStarGiftPage struct {
	Gifts      []SavedStarGift
	NextOffset string // empty = no more pages (must be omitted on the last page; clients stop paging on it)
	Count      int    // total (unconverted, after excludeUnsaved filtering)
}

// SavedStarGiftListCursor is the composite keyset cursor for the profile gift
// order: pinned gifts first by PinnedOrder, then unpinned gifts by ID DESC.
// PinnedOrder == 0 identifies the unpinned segment.
type SavedStarGiftListCursor struct {
	Date        int
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
	// ExcludeHosted corresponds to exclude_hosted: keep only gifts a peer owns/received
	// itself, excluding collectibles hosted on-chain on the profile by a host.
	ExcludeHosted bool
	// SortByValue corresponds to sort_by_value: sort by price rather than receive time.
	SortByValue bool
	// PeerColorAvailable corresponds to peer_color_available: return only collectibles
	// whose palette can be used as the collectible-message palette.
	PeerColorAvailable bool
	CollectionID       int
	Offset             string
	Limit              int
}

// Star gift boundary constants.
const (
	// MaxSavedStarGiftsLimit is the per-page cap of getSavedStarGifts.
	MaxSavedStarGiftsLimit = 100
	// MaxStarGiftMessageRunes bounds the attached-message length (aligned with the
	// stargifts_message_length_max magnitude).
	MaxStarGiftMessageRunes = 255
	// MaxStarGiftsOffsetBytes is the maximum length of a keyset cursor string.
	MaxStarGiftsOffsetBytes = 64
	// MaxStarGiftTGSBytes bounds compressed animations imported from the backend, so an
	// admin-panel upload cannot become a capacity bypass.
	MaxStarGiftTGSBytes int64 = 512 << 10
	// MaxStarGiftLottieBytes bounds the decompressed Lottie JSON.
	MaxStarGiftLottieBytes int64 = 4 << 20
	// MaxStarGiftAnimationFrameRate / Seconds bound the admin backend player and the
	// client animation timeline.
	MaxStarGiftAnimationFrameRate = 120
	MaxStarGiftAnimationSeconds   = 30
	// MaxStarGiftCatalogSize is the bounded cap of the current regular gift catalog.
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
	// MinStarGiftResaleStars is the floor of a Stars resale price. It equals the
	// clients' built-in stars_stargift_resale_amount_min default and is
	// advertised under that key in help.getAppConfig. It is deliberately flat: a
	// floor derived from the cheapest listing only ever ratchets up.
	MinStarGiftResaleStars int64 = 125
)

// Star gift sentinel errors (the rpc layer maps them to tgerr via errors.Is).
var (
	// ErrStarGiftInvalid means the gift id is not in the catalog.
	ErrStarGiftInvalid = errors.New("stargift: invalid gift id")
	// ErrStarGiftNotFound means the received gift instance could not be found.
	ErrStarGiftNotFound = errors.New("stargift: saved gift not found")
	// ErrStarGiftAlreadyConverted means the gift was already converted back to Stars
	// (it cannot be converted again).
	ErrStarGiftAlreadyConverted       = errors.New("stargift: already converted")
	ErrStarGiftFileInvalid            = errors.New("stargift: invalid animation file")
	ErrStarGiftCatalogFull            = errors.New("stargift: catalog full")
	ErrStarGiftLifecycleInvalid       = errors.New("stargift: invalid auction or scheduled-release parameters")
	ErrStarGiftCollectibleUnavailable = errors.New("stargift: collectible upgrade unavailable")
	ErrStarGiftAlreadyUpgraded        = errors.New("stargift: already upgraded")
	ErrStarGiftCollectibleSoldOut     = errors.New("stargift: collectible supply exhausted")
	ErrStarGiftCollectibleInvalid     = errors.New("stargift: invalid collectible definition")
	ErrStarGiftCollectionNotFound     = errors.New("stargift: collection not found")
	ErrStarGiftCollectionsFull        = errors.New("stargift: collections full")
	ErrStarGiftUnavailable            = errors.New("stargift: unavailable")
	ErrStarGiftOwnerInvalid           = errors.New("stargift: owner invalid")
	ErrStarGiftTransferUnavailable    = errors.New("stargift: transfer unavailable")
	ErrStarGiftResaleUnavailable      = errors.New("stargift: resale unavailable")
	// ErrStarGiftDeleteUnavailable means this gift cannot currently be deleted: converted,
	// exported, upgraded to a collectible, limited or auction gift, still carrying a
	// conversion price or prepaid upgrade Stars, or already deleted.
	ErrStarGiftDeleteUnavailable = errors.New("stargift: delete unavailable")
	ErrStarGiftOfferInvalid      = errors.New("stargift: offer invalid")
	ErrStarGiftOfferExpired      = errors.New("stargift: offer expired")
	// ErrStarGiftRecipientUnavailable means the recipient appears as a deleted tombstone
	// to other users (a frozen account), so the gift can no longer be sent.
	ErrStarGiftRecipientUnavailable  = errors.New("stargift: recipient account unavailable")
	ErrStarGiftCraftUnavailable      = errors.New("stargift: craft unavailable")
	ErrStarGiftAuctionUnavailable    = errors.New("stargift: auction unavailable")
	ErrStarGiftWithdrawalUnavailable = errors.New("stargift: withdrawal provider unavailable")
	// ErrStarGiftExportCooldown means can_export_at has not yet elapsed: the server clock
	// has not reached the exportable moment, and no client input can trigger the
	// withdrawal early.
	ErrStarGiftExportCooldown              = errors.New("stargift: export cooldown active")
	ErrChannelRevenueWithdrawalInvalid     = errors.New("channel revenue: withdrawal invalid")
	ErrChannelRevenueWithdrawalExpired     = errors.New("channel revenue: withdrawal expired")
	ErrChannelRevenueInsufficient          = errors.New("channel revenue: insufficient balance")
	ErrChannelRevenueWithdrawalUnavailable = errors.New("channel revenue: withdrawal unavailable")
	ErrStarGiftFormExpired                 = errors.New("stargift: payment form expired")
	ErrStarGiftFormPurposeInvalid          = errors.New("stargift: payment form purpose invalid")
	ErrStarGiftFormAmountMismatch          = errors.New("stargift: payment form amount mismatch")
	ErrStarGiftNotConvertible              = errors.New("stargift: not convertible")
	ErrStarGiftIdempotencyConflict         = errors.New("stargift: idempotency conflict")
)

var ErrStarGiftListed = errors.New("сначала снимите подарок с продажи")

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
// preview response includes the target attribute plus the published selectable pool. A pool may
// intentionally contain one deterministic model or pattern; when it contains several entries,
// every entry must still have its own document identity so clients do not deduplicate the pool.
// Only attributes with a positive weight are selectable: the preview sample and the upgrade draw
// skip zero-weight attributes, so they cannot make up the two backdrops a preview needs.
func validateStarGiftUpgradePreviewPool(write StarGiftCollectibleWrite, requireStoredAsset bool) error {
	validateAnimated := func(kind StarGiftCollectibleAttributeKind, attributes []StarGiftCollectibleAttribute) error {
		selectable := 0
		documents := make(map[int64]struct{}, len(attributes))
		for _, attribute := range attributes {
			if attribute.RarityKind != StarGiftRarityPermille || attribute.Crafted || attribute.RarityPermille <= 0 {
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
		if selectable < 1 {
			return fmt.Errorf("%w: %s preview requires a selectable attribute", ErrStarGiftCollectibleInvalid, kind)
		}
		if requireStoredAsset && len(documents) != selectable {
			return fmt.Errorf("%w: %s preview attributes require distinct documents", ErrStarGiftCollectibleInvalid, kind)
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
		if _, exists := seenBackdropIDs[attribute.BackdropID]; exists {
			return fmt.Errorf("%w: duplicate backdrop_id %d", ErrStarGiftCollectibleInvalid, attribute.BackdropID)
		}
		seenBackdropIDs[attribute.BackdropID] = struct{}{}
		if attribute.RarityPermille > 0 {
			selectableBackdrops++
		}
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
			if attribute.RarityPermille < 0 || attribute.RarityPermille > 1000 || attribute.Crafted {
				return ErrStarGiftCollectibleInvalid
			}
			// permille 0 is a legal weight: the attribute stays in the pool (it must be
			// retained to render existing collectibles) but is never drawn at random, so
			// it does not count toward the selectable total.
			if attribute.RarityPermille > 0 {
				selectable++
			}
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

// StarGiftCatalogHash folds the client-visible catalog fields into a stable hash for getStarGifts NotModified.
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

// StarGiftCollectionsHash folds each collection's own stable hash in the server's return order.
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
func EncodeSavedStarGiftListCursor(pinnedOrder, date int, id int64) string {
	if pinnedOrder < 0 || date < 0 || id <= 0 {
		return ""
	}
	raw := "v2:" + strconv.Itoa(pinnedOrder) + ":" + strconv.Itoa(date) + ":" + strconv.FormatInt(id, 10)
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
	if len(parts) != 4 || parts[0] != "v2" {
		return SavedStarGiftListCursor{}, false
	}
	order, err := strconv.ParseInt(parts[1], 10, 32)
	if err != nil || order < 0 {
		return SavedStarGiftListCursor{}, false
	}
	date, err := strconv.ParseInt(parts[2], 10, 32)
	if err != nil || date < 0 {
		return SavedStarGiftListCursor{}, false
	}
	id, err := strconv.ParseInt(parts[3], 10, 64)
	if err != nil || id <= 0 {
		return SavedStarGiftListCursor{}, false
	}
	return SavedStarGiftListCursor{PinnedOrder: int(order), Date: int(date), ID: id}, true
}

// EncodeSavedStarGiftPriceCursor encodes the keyset cursor for sort_by_value ordering
// (price descending, ties by instance id descending). Price may be 0, so only id is validated.
func EncodeSavedStarGiftPriceCursor(price, id int64) string {
	if id <= 0 {
		return ""
	}
	raw := "v2:" + strconv.FormatInt(price, 10) + ":" + strconv.FormatInt(id, 10)
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

// DecodeSavedStarGiftPriceCursor decodes a price cursor; returns ok=false when it cannot be parsed (including stale v1 cursors).
func DecodeSavedStarGiftPriceCursor(s string) (price int64, id int64, ok bool) {
	if s == "" {
		return 0, 0, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return 0, 0, false
	}
	parts := strings.Split(string(raw), ":")
	if len(parts) != 3 || parts[0] != "v2" {
		return 0, 0, false
	}
	price, err = strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return 0, 0, false
	}
	id, err = strconv.ParseInt(parts[2], 10, 64)
	if err != nil || id <= 0 {
		return 0, 0, false
	}
	return price, id, true
}

// EncodeStarGiftCursor / DecodeStarGiftCursor are simple instance-ID cursors
// used by star gift lists whose order is strictly ID DESC (for example craft).
func EncodeStarGiftCursor(id int64) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatInt(id, 10)))
}

// DecodeStarGiftCursor decodes a cursor; returns ok=false when it cannot be parsed (including the empty string), and the caller starts from the first page.
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
