package store

import (
	"context"

	"telesrv/internal/domain"
)

// StarGiftStore persists the gift catalog, immutable revisions, and gift instances received by a peer.
type StarGiftStore interface {
	// Catalog returns the enabled current catalog snapshot, sorted by sort_order/gift_id.
	Catalog(ctx context.Context) ([]domain.StarGift, error)
	// CatalogAll includes disabled gifts for admin import duplicate detection.
	CatalogAll(ctx context.Context) ([]domain.StarGift, error)
	// CatalogGift returns only the currently enabled revision, for validating new purchases.
	CatalogGift(ctx context.Context, giftID int64) (domain.StarGift, bool, error)
	// CatalogRevision returns an immutable historical revision, for projecting already-received gifts.
	CatalogRevision(ctx context.Context, revisionID int64) (domain.StarGift, bool, error)
	// CreateCatalogRevision creates a new gift or a new revision of an existing gift and atomically switches the current revision.
	CreateCatalogRevision(ctx context.Context, write domain.StarGiftCatalogWrite) (domain.StarGiftCatalogEntry, error)
	// CreateCatalogBundle atomically switches the catalog revision and optional complete
	// collectible revision. It is the only write path used by official full imports.
	CreateCatalogBundle(ctx context.Context, write domain.StarGiftCatalogBundleWrite) (domain.StarGiftCatalogBundleResult, error)
	SetCatalogEnabled(ctx context.Context, giftID int64, enabled bool) (bool, error)
	SetCatalogSortOrder(ctx context.Context, giftID int64, sortOrder int) (bool, error)
	// AnimationJSON returns the current revision's normalized Lottie JSON, for safe preview in the admin panel.
	AnimationJSON(ctx context.Context, giftID int64) ([]byte, bool, error)
	// PublishCollectibleRevision validates and atomically publishes a new immutable attribute pool.
	PublishCollectibleRevision(ctx context.Context, write domain.StarGiftCollectibleWrite) (domain.StarGiftCollectibleRevision, error)
	ActiveCollectibleRevision(ctx context.Context, giftID int64) (domain.StarGiftCollectibleRevision, bool, error)
	// ActiveCollectibleProjection omits heavyweight animation bodies from read-only client/admin
	// projections. samplePerKind=0 returns the complete attribute metadata; a positive value
	// returns at most that many randomly selected ordinary-upgrade attributes per kind.
	ActiveCollectibleProjection(ctx context.Context, giftID int64, samplePerKind int) (domain.StarGiftCollectibleRevision, bool, error)
	CollectibleAvailability(ctx context.Context, giftIDs []int64) (map[int64]domain.StarGiftCollectibleAvailability, error)
	CollectibleAnimationJSON(ctx context.Context, giftID int64, kind domain.StarGiftCollectibleAttributeKind, attributeID int64) ([]byte, bool, error)
	UniqueBySlug(ctx context.Context, slug string) (domain.UniqueStarGift, bool, error)
	UniqueByID(ctx context.Context, uniqueGiftID int64) (domain.UniqueStarGift, bool, error)
	UniqueByIDs(ctx context.Context, uniqueGiftIDs []int64) (map[int64]domain.UniqueStarGift, error)
	// ListUniqueByOwner returns active, locally owned collectibles in a bounded
	// stable order. Exported/burned/transferred gifts are deliberately excluded.
	ListUniqueByOwner(ctx context.Context, owner domain.Peer, limit int) ([]domain.UniqueStarGift, error)

	// Create writes one received gift instance and returns the row id; when a channel gift is not given an explicit saved_id, the row id is used as saved_id.
	Create(ctx context.Context, gift domain.SavedStarGift) (int64, error)
	// ListByOwner returns an owner's unconverted gifts paginated by id DESC keyset; with excludeUnsaved it returns only those shown on the profile.
	ListByOwner(ctx context.Context, owner domain.Peer, excludeUnsaved bool, offset string, limit int) (domain.SavedStarGiftPage, error)
	ListByOwnerFiltered(ctx context.Context, filter domain.SavedStarGiftFilter) (domain.SavedStarGiftPage, error)
	// GetByRef fetches a gift instance by protocol reference: users use msg_id, channels use saved_id.
	GetByRef(ctx context.Context, ref domain.SavedStarGiftRef) (domain.SavedStarGift, bool, error)
	// ResolveUserMessageRef resolves only an explicit viewer-local service-message
	// alias. The returned ref retains the saved gift's authoritative user/channel
	// owner; callers must still authorize that owner.
	ResolveUserMessageRef(ctx context.Context, viewerUserID int64, msgID int) (domain.SavedStarGiftRef, bool, error)
	// ResolveSavedIDs resolves an ordered batch of protocol references without
	// per-gift round trips. Every ref must belong to owner and resolve to a live gift.
	ResolveSavedIDs(ctx context.Context, owner domain.Peer, refs []domain.SavedStarGiftRef) ([]int64, error)
	// CountByOwner returns the number of an owner's gifts shown on the profile (not converted, not hidden), for full.stargifts_count.
	CountByOwner(ctx context.Context, owner domain.Peer) (int, error)
	// SetUnsaved toggles a gift's display on the profile (saveStarGift); returns whether a row matched.
	SetUnsaved(ctx context.Context, ref domain.SavedStarGiftRef, unsaved bool) (bool, error)
	// MarkConverted idempotently marks a gift as converted (convertStarGift) and returns the row
	// so the caller can credit Stars per ConvertStars; already-converted returns
	// domain.ErrStarGiftAlreadyConverted, and absent returns domain.ErrStarGiftNotFound.
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
	StarGiftUpgradeReceipt(ctx context.Context, userID int64, commandKey string) (domain.StarGiftUpgradeReceipt, bool, error)
	GrantUniqueStarGift(ctx context.Context, req domain.AdminStarGiftGrant) (domain.AdminStarGiftGrantResult, error)
}

// StarGiftLifecycleStore owns transactions that span collectible ownership, listings,
// balances and service-message updates. Implementations must serialize on the saved/unique
// aggregate and return exact replays for command-key/random-id retries.
type StarGiftLifecycleStore interface {
	SettledStarGiftPurchase(ctx context.Context, req domain.StarGiftPurchaseRequest) (domain.StarGiftPurchaseResult, bool, error)
	IssueStarGiftPurchaseForm(ctx context.Context, form domain.StarGiftPurchaseForm) (domain.StarGiftPurchaseForm, error)
	ValidateStarGiftPurchaseForm(ctx context.Context, req domain.StarGiftPurchaseRequest) error
	PurchaseStarGift(ctx context.Context, req domain.StarGiftPurchaseRequest) (domain.StarGiftPurchaseResult, error)
	ConvertStarGift(ctx context.Context, req domain.StarGiftConvertRequest) (domain.StarGiftConvertResult, error)
	ListResaleStarGifts(ctx context.Context, filter domain.StarGiftResaleFilter) (domain.StarGiftResalePage, error)
	UniqueStarGiftValueInfo(ctx context.Context, uniqueGiftID int64) (domain.StarGiftValueInfo, error)
	SetStarGiftListing(ctx context.Context, req domain.StarGiftListingRequest) (domain.UniqueStarGift, error)
	TransferStarGift(ctx context.Context, req domain.StarGiftTransferRequest) (domain.StarGiftTransferResult, error)
	PurchaseResaleStarGift(ctx context.Context, req domain.StarGiftResalePurchaseRequest) (domain.StarGiftTransferResult, error)
	SendStarGiftOffer(ctx context.Context, req domain.StarGiftOfferRequest) (domain.StarGiftOfferResult, error)
	ResolveStarGiftOffer(ctx context.Context, req domain.StarGiftResolveOfferRequest) (domain.StarGiftOfferResult, error)
	ListCraftStarGifts(ctx context.Context, userID, giftID int64, offset string, limit int) (domain.SavedStarGiftPage, error)
	CraftStarGift(ctx context.Context, req domain.StarGiftCraftRequest) (domain.StarGiftCraftResult, error)
	StarGiftAuctionState(ctx context.Context, userID int64, giftID int64, slug string, now int) (domain.StarGiftAuction, error)
	ActiveStarGiftAuctions(ctx context.Context, userID int64, now int) ([]domain.StarGiftAuction, error)
	StarGiftAuctionAcquired(ctx context.Context, userID, giftID int64) ([]domain.StarGiftAuctionAcquired, error)
	BidStarGiftAuction(ctx context.Context, req domain.StarGiftAuctionBidRequest) (domain.StarGiftAuction, domain.StarsBalance, error)
	PrepaidUpgradeTarget(ctx context.Context, owner domain.Peer, hash string) (domain.SavedStarGift, int64, error)
	PrepayStarGiftUpgrade(ctx context.Context, req domain.StarGiftPrepaidUpgradeRequest) (domain.StarGiftPrepaidUpgradeResult, error)
	DropStarGiftOriginalDetails(ctx context.Context, req domain.StarGiftDropOriginalDetailsRequest) (domain.StarGiftDropOriginalDetailsResult, error)
	SetStarGiftNotifications(ctx context.Context, userID, channelID int64, enabled bool) error
	StarGiftNotificationsEnabled(ctx context.Context, userID, channelID int64) (bool, error)
	RecordStarGiftWithdrawal(ctx context.Context, req domain.StarGiftWithdrawalRequest, provider, providerRequestID, url string, expiresAt int) (domain.StarGiftWithdrawal, error)
	// DeleteStarGift implements payments.deleteStarGift: it moves the saved
	// gift into the deleted lifecycle state and drops it from every collection.
	// Refused: an upgraded collectible, a limited-edition or auction gift, and
	// a gift that still carries Stars (conversion value or prepaid upgrade).
	DeleteStarGift(ctx context.Context, req domain.StarGiftDeleteRequest) (domain.StarGiftDeleteResult, error)
	ResolveStarGiftWithdrawal(ctx context.Context, providerRequestID string) (domain.StarGiftWithdrawal, bool, error)
	CompleteStarGiftWithdrawal(ctx context.Context, providerRequestID string, date int) (domain.StarGiftWithdrawal, error)
	IssueChannelRevenueWithdrawal(ctx context.Context, req domain.ChannelRevenueWithdrawalRequest) (domain.ChannelRevenueWithdrawal, error)
	ResolveChannelRevenueWithdrawal(ctx context.Context, tokenDigest []byte) (domain.ChannelRevenueWithdrawal, bool, error)
	CompleteChannelRevenueWithdrawal(ctx context.Context, tokenDigest []byte, date int) (domain.ChannelRevenueWithdrawal, error)
	TonBalance(ctx context.Context, userID int64) (int64, error)
	TonTransactions(ctx context.Context, userID int64, query domain.StarsTransactionQuery) (domain.TonTransactionPage, error)
	ChannelStarsBalance(ctx context.Context, channelID int64) (int64, error)
	ChannelStarsOverallRevenue(ctx context.Context, channelID int64) (int64, error)
	ChannelStarsTransactions(ctx context.Context, channelID int64, query domain.StarsTransactionQuery) (domain.StarsTransactionPage, error)
	ChannelTonBalance(ctx context.Context, channelID int64) (int64, error)
	ChannelTonOverallRevenue(ctx context.Context, channelID int64) (int64, error)
	ChannelTonTransactions(ctx context.Context, channelID int64, query domain.StarsTransactionQuery) (domain.TonTransactionPage, error)
	// BotStarsBalance reads a bot's own revenue wallet, which is deliberately
	// separate from the bot user identity's personal Stars balance.
	BotStarsBalance(ctx context.Context, botUserID int64) (int64, error)
	// BotStarsOverallRevenue sums only positive entries: it is lifetime revenue
	// and stays monotonic across refunds and spends.
	BotStarsOverallRevenue(ctx context.Context, botUserID int64) (int64, error)
	BotStarsTransactions(ctx context.Context, botUserID int64, query domain.StarsTransactionQuery) (domain.StarsTransactionPage, error)
	// CreditBotStarsWallet applies one settled invoice atomically and reports
	// whether the credit was new. A replay of the same InvoiceKey returns the
	// stored balance with false instead of minting a second credit.
	CreditBotStarsWallet(ctx context.Context, credit domain.BotStarsCredit) (balance int64, credited bool, err error)
	// BotStarsRevenueSeries aggregates the bot wallet journal into one row per
	// UTC day over [fromUnix, toUnix] so the revenue chart can be rendered
	// without pulling the whole history into memory.
	BotStarsRevenueSeries(ctx context.Context, botUserID int64, fromUnix, toUnix int) ([]domain.BotStarsRevenuePoint, error)
	// SweepStarGiftLifecycle advances time-driven offer/auction aggregates and
	// drains their durable notification/delivery outboxes in bounded batches.
	SweepStarGiftLifecycle(ctx context.Context, now, limit int) error
}
