// Package stargifts implements the durable Star Gift catalog and received-gift state.
package stargifts

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

// BlobBackend is the content-addressed media boundary used by the catalog importer.
type BlobBackend interface {
	Name() string
	Put(ctx context.Context, data []byte) (string, error)
	Get(ctx context.Context, objectKey string) ([]byte, error)
}

type Service struct {
	store      store.StarGiftStore
	upgrades   store.StarGiftUpgradeStore
	lifecycle  store.StarGiftLifecycleStore
	withdrawal StarGiftWithdrawalProvider
	blobs      BlobBackend
	dc         int

	mu    sync.RWMutex
	built bool
	gifts []domain.StarGift
	byID  map[int64]domain.StarGift
	hash  int

	formMu sync.Mutex
	forms  map[starGiftPurchaseFormKey]domain.StarGiftPurchaseForm
}

type starGiftPurchaseFormKey struct {
	buyerUserID int64
	formID      int64
}

// AtomicPurchaseConfigured reports whether the production aggregate
// coordinator is installed. It lets the RPC package keep its isolated memory
// test adapter without silently downgrading PostgreSQL deployments.
func (s *Service) AtomicPurchaseConfigured() bool { return s != nil && s.lifecycle != nil }

type Option func(*Service)

func WithUpgradeStore(upgrades store.StarGiftUpgradeStore) Option {
	return func(service *Service) { service.upgrades = upgrades }
}

func WithLifecycleStore(lifecycle store.StarGiftLifecycleStore) Option {
	return func(service *Service) { service.lifecycle = lifecycle }
}

type StarGiftWithdrawalProvider interface {
	Name() string
	CreateWithdrawal(ctx context.Context, req StarGiftWithdrawalProviderRequest) (StarGiftWithdrawalProviderResult, error)
}

type StarGiftWithdrawalProviderRequest struct {
	UserID int64
	Gift   domain.UniqueStarGift
}

type StarGiftWithdrawalProviderResult struct {
	RequestID string
	URL       string
	ExpiresAt int
}

func WithWithdrawalProvider(provider StarGiftWithdrawalProvider) Option {
	return func(service *Service) { service.withdrawal = provider }
}

func NewService(st store.StarGiftStore, blobs BlobBackend, dc int, opts ...Option) *Service {
	service := &Service{store: st, blobs: blobs, dc: dc, forms: make(map[starGiftPurchaseFormKey]domain.StarGiftPurchaseForm)}
	for _, opt := range opts {
		opt(service)
	}
	return service
}

func (s *Service) ensureCatalog(ctx context.Context) error {
	s.mu.RLock()
	built := s.built
	s.mu.RUnlock()
	if built {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.built {
		return nil
	}
	if s.store == nil {
		return fmt.Errorf("star gift store is not configured")
	}
	gifts, err := s.store.Catalog(ctx)
	if err != nil {
		return err
	}
	s.gifts = gifts
	s.byID = make(map[int64]domain.StarGift, len(gifts))
	for _, gift := range gifts {
		s.byID[gift.ID] = gift
	}
	s.hash = domain.StarGiftCatalogHash(gifts)
	s.built = true
	return nil
}

func (s *Service) Catalog(ctx context.Context) ([]domain.StarGift, error) {
	if err := s.ensureCatalog(ctx); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]domain.StarGift(nil), s.gifts...), nil
}

func (s *Service) CatalogHash(ctx context.Context) (int, error) {
	if err := s.ensureCatalog(ctx); err != nil {
		return 0, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.hash, nil
}

func (s *Service) GiftByID(ctx context.Context, id int64) (domain.StarGift, bool, error) {
	if err := s.ensureCatalog(ctx); err != nil {
		return domain.StarGift{}, false, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	gift, ok := s.byID[id]
	return gift, ok, nil
}

func (s *Service) GiftRevisionByID(ctx context.Context, revisionID int64) (domain.StarGift, bool, error) {
	if s == nil || s.store == nil {
		return domain.StarGift{}, false, nil
	}
	return s.store.CatalogRevision(ctx, revisionID)
}

// InvalidateStarGiftCatalog implements the shared PostgreSQL read-model listener boundary.
func (s *Service) InvalidateStarGiftCatalog() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.built = false
	s.gifts = nil
	s.byID = nil
	s.hash = 0
	s.mu.Unlock()
}

func (s *Service) FlushStarGiftCatalog() { s.InvalidateStarGiftCatalog() }

func (s *Service) CreateCatalogRevision(ctx context.Context, write domain.StarGiftCatalogWrite) (domain.StarGiftCatalogEntry, error) {
	if s == nil || s.store == nil || s.blobs == nil {
		return domain.StarGiftCatalogEntry{}, fmt.Errorf("star gift catalog importer is not configured")
	}
	write.Title = strings.TrimSpace(write.Title)
	if write.Stars <= 0 || write.ConvertStars < 0 || write.ConvertStars > write.Stars ||
		write.Animation.Width != 512 || write.Animation.Height != 512 || len(write.Animation.TGS) == 0 ||
		len([]rune(write.Title)) > domain.MaxStarGiftTitleRunes {
		return domain.StarGiftCatalogEntry{}, domain.ErrStarGiftInvalid
	}
	if err := s.materializeCatalogWrite(ctx, &write); err != nil {
		return domain.StarGiftCatalogEntry{}, err
	}
	entry, err := s.store.CreateCatalogRevision(ctx, write)
	if err != nil {
		return domain.StarGiftCatalogEntry{}, err
	}
	s.InvalidateStarGiftCatalog()
	return entry, nil
}

func (s *Service) materializeCatalogWrite(ctx context.Context, write *domain.StarGiftCatalogWrite) error {
	objectKey, err := s.blobs.Put(ctx, write.Animation.TGS)
	if err != nil {
		return fmt.Errorf("store star gift animation: %w", err)
	}
	documentID, err := randomPositiveInt64()
	if err != nil {
		return err
	}
	accessHash, err := randomPositiveInt64()
	if err != nil {
		return err
	}
	fileReference := make([]byte, 16)
	if _, err := rand.Read(fileReference); err != nil {
		return fmt.Errorf("generate star gift file reference: %w", err)
	}
	write.Document = domain.Document{
		ID:            documentID,
		AccessHash:    accessHash,
		FileReference: fileReference,
		Date:          int(time.Now().Unix()),
		MimeType:      "application/x-tgsticker",
		Size:          int64(len(write.Animation.TGS)),
		DCID:          s.dc,
		Attributes: []domain.DocumentAttribute{
			{Kind: domain.DocAttrImageSize, W: 512, H: 512},
			{Kind: domain.DocAttrSticker, Alt: "🎁"},
			{Kind: domain.DocAttrFilename, FileName: "gift.tgs"},
		},
	}
	write.Blob = domain.FileBlob{
		LocationKey: fmt.Sprintf("doc:%d", documentID),
		Backend:     domain.MediaBackend(s.blobs.Name()),
		ObjectKey:   objectKey,
		Size:        int64(len(write.Animation.TGS)),
		SHA256:      append([]byte(nil), write.Animation.SHA256...),
		MimeType:    "application/x-tgsticker",
	}
	return nil
}

// CreateCatalogBundle materializes every verified asset before publishing both active
// revision pointers in one store transaction. Blob writes are content-addressed and may be
// safely orphaned for later GC if the database transaction fails.
func (s *Service) CreateCatalogBundle(ctx context.Context, write domain.StarGiftCatalogBundleWrite) (domain.StarGiftCatalogBundleResult, error) {
	if s == nil || s.store == nil || s.blobs == nil {
		return domain.StarGiftCatalogBundleResult{}, fmt.Errorf("star gift catalog importer is not configured")
	}
	write.Catalog.Title = strings.TrimSpace(write.Catalog.Title)
	if write.Catalog.Stars <= 0 || write.Catalog.ConvertStars < 0 || write.Catalog.ConvertStars > write.Catalog.Stars ||
		write.Catalog.Animation.Width != 512 || write.Catalog.Animation.Height != 512 || len(write.Catalog.Animation.TGS) == 0 ||
		len([]rune(write.Catalog.Title)) > domain.MaxStarGiftTitleRunes {
		return domain.StarGiftCatalogBundleResult{}, domain.ErrStarGiftInvalid
	}
	var officialSource map[string]any
	if write.Catalog.OfficialGiftID < 0 {
		return domain.StarGiftCatalogBundleResult{}, domain.ErrStarGiftInvalid
	}
	if write.Catalog.OfficialGiftID > 0 && (len(write.Catalog.SourceManifestSHA256) != 32 ||
		json.Unmarshal(write.Catalog.OfficialSourceJSON, &officialSource) != nil || officialSource == nil) {
		return domain.StarGiftCatalogBundleResult{}, domain.ErrStarGiftInvalid
	}
	if write.Catalog.OfficialGiftID == 0 && (len(write.Catalog.SourceManifestSHA256) != 0 || len(write.Catalog.OfficialSourceJSON) != 0) {
		return domain.StarGiftCatalogBundleResult{}, domain.ErrStarGiftInvalid
	}
	if write.Collectible != nil {
		write.Collectible.SlugPrefix = strings.ToLower(strings.TrimSpace(write.Collectible.SlugPrefix))
		if write.Collectible.OfficialGiftID != write.Catalog.OfficialGiftID ||
			!bytes.Equal(write.Collectible.SourceManifestSHA256, write.Catalog.SourceManifestSHA256) {
			return domain.StarGiftCatalogBundleResult{}, domain.ErrStarGiftCollectibleInvalid
		}
		validation := *write.Collectible
		if validation.GiftID == 0 {
			validation.GiftID = write.Catalog.GiftID
			if validation.GiftID == 0 {
				validation.GiftID = 1
			}
		}
		if err := domain.ValidateStarGiftCollectibleDraft(validation); err != nil {
			return domain.StarGiftCatalogBundleResult{}, err
		}
	}
	if err := s.materializeCatalogWrite(ctx, &write.Catalog); err != nil {
		return domain.StarGiftCatalogBundleResult{}, err
	}
	if write.Collectible != nil {
		if err := s.materializeCollectibleAttributes(ctx, write.Collectible.Models); err != nil {
			return domain.StarGiftCatalogBundleResult{}, err
		}
		if err := s.materializeCollectibleAttributes(ctx, write.Collectible.Patterns); err != nil {
			return domain.StarGiftCatalogBundleResult{}, err
		}
	}
	result, err := s.store.CreateCatalogBundle(ctx, write)
	if err == nil {
		s.InvalidateStarGiftCatalog()
	}
	return result, err
}

func (s *Service) SetCatalogEnabled(ctx context.Context, giftID int64, enabled bool) (bool, error) {
	changed, err := s.store.SetCatalogEnabled(ctx, giftID, enabled)
	if err == nil {
		s.InvalidateStarGiftCatalog()
	}
	return changed, err
}

func (s *Service) SetCatalogSortOrder(ctx context.Context, giftID int64, sortOrder int) (bool, error) {
	changed, err := s.store.SetCatalogSortOrder(ctx, giftID, sortOrder)
	if err == nil {
		s.InvalidateStarGiftCatalog()
	}
	return changed, err
}

func (s *Service) AnimationJSON(ctx context.Context, giftID int64) ([]byte, bool, error) {
	return s.store.AnimationJSON(ctx, giftID)
}

func (s *Service) PublishCollectibleRevision(ctx context.Context, write domain.StarGiftCollectibleWrite) (domain.StarGiftCollectibleRevision, error) {
	if s == nil || s.store == nil {
		return domain.StarGiftCollectibleRevision{}, fmt.Errorf("star gift collectible store is not configured")
	}
	revision, err := s.store.PublishCollectibleRevision(ctx, write)
	if err == nil {
		s.InvalidateStarGiftCatalog()
	}
	return revision, err
}

// CreateCollectibleRevision materializes the normalized model/pattern animations and then
// atomically publishes the complete immutable attribute pool. Callers must pass animations
// produced by PrepareAnimation; partial revisions are never exposed to clients.
func (s *Service) CreateCollectibleRevision(ctx context.Context, write domain.StarGiftCollectibleWrite) (domain.StarGiftCollectibleRevision, error) {
	if s == nil || s.store == nil || s.blobs == nil {
		return domain.StarGiftCollectibleRevision{}, fmt.Errorf("star gift collectible importer is not configured")
	}
	write.SlugPrefix = strings.ToLower(strings.TrimSpace(write.SlugPrefix))
	if err := domain.ValidateStarGiftCollectibleDraft(write); err != nil {
		return domain.StarGiftCollectibleRevision{}, err
	}
	if err := s.materializeCollectibleAttributes(ctx, write.Models); err != nil {
		return domain.StarGiftCollectibleRevision{}, err
	}
	if err := s.materializeCollectibleAttributes(ctx, write.Patterns); err != nil {
		return domain.StarGiftCollectibleRevision{}, err
	}
	return s.PublishCollectibleRevision(ctx, write)
}

func (s *Service) materializeCollectibleAttributes(ctx context.Context, attributes []domain.StarGiftCollectibleAttribute) error {
	for i := range attributes {
		animation := attributes[i].Animation
		if animation == nil {
			return domain.ErrStarGiftCollectibleInvalid
		}
		objectKey, err := s.blobs.Put(ctx, animation.TGS)
		if err != nil {
			return fmt.Errorf("store collectible %s animation: %w", attributes[i].Kind, err)
		}
		documentID, err := randomPositiveInt64()
		if err != nil {
			return err
		}
		accessHash, err := randomPositiveInt64()
		if err != nil {
			return err
		}
		fileReference := make([]byte, 16)
		if _, err := rand.Read(fileReference); err != nil {
			return fmt.Errorf("generate collectible file reference: %w", err)
		}
		attributes[i].Document = &domain.Document{
			ID: documentID, AccessHash: accessHash, FileReference: fileReference,
			Date: int(time.Now().Unix()), MimeType: "application/x-tgsticker",
			Size: int64(len(animation.TGS)), DCID: s.dc,
			Attributes: []domain.DocumentAttribute{
				{Kind: domain.DocAttrImageSize, W: 512, H: 512},
				{Kind: domain.DocAttrSticker, Alt: "🎁"},
				{Kind: domain.DocAttrFilename, FileName: string(attributes[i].Kind) + ".tgs"},
			},
		}
		attributes[i].Blob = &domain.FileBlob{
			LocationKey: fmt.Sprintf("doc:%d", documentID), Backend: domain.MediaBackend(s.blobs.Name()),
			ObjectKey: objectKey, Size: int64(len(animation.TGS)),
			SHA256: append([]byte(nil), animation.SHA256...), MimeType: "application/x-tgsticker",
		}
	}
	return nil
}

func (s *Service) CollectiblePreview(ctx context.Context, giftID int64) (domain.StarGiftUpgradePreview, bool, error) {
	if s == nil || s.store == nil || giftID <= 0 {
		return domain.StarGiftUpgradePreview{}, false, nil
	}
	revision, ok, err := s.store.ActiveCollectibleRevision(ctx, giftID)
	if err != nil || !ok || !revision.Published {
		return domain.StarGiftUpgradePreview{}, false, err
	}
	return domain.StarGiftUpgradePreview{
		GiftID: giftID, Revision: revision.Revision, UpgradeStars: revision.UpgradeStars, SupplyTotal: revision.SupplyTotal,
		Issued: revision.Issued, Models: revision.Models, Patterns: revision.Patterns, Backdrops: revision.Backdrops,
		SlugPrefix: revision.SlugPrefix,
	}, true, nil
}

func (s *Service) CollectibleAvailability(ctx context.Context, giftIDs []int64) (map[int64]domain.StarGiftCollectibleAvailability, error) {
	if s == nil || s.store == nil || len(giftIDs) == 0 {
		return map[int64]domain.StarGiftCollectibleAvailability{}, nil
	}
	return s.store.CollectibleAvailability(ctx, giftIDs)
}

func (s *Service) CollectibleAnimationJSON(ctx context.Context, giftID int64, kind domain.StarGiftCollectibleAttributeKind, attributeID int64) ([]byte, bool, error) {
	if s == nil || s.store == nil {
		return nil, false, nil
	}
	return s.store.CollectibleAnimationJSON(ctx, giftID, kind, attributeID)
}

func (s *Service) UniqueBySlug(ctx context.Context, slug string) (domain.UniqueStarGift, bool, error) {
	if s == nil || s.store == nil {
		return domain.UniqueStarGift{}, false, nil
	}
	return s.store.UniqueBySlug(ctx, slug)
}

func (s *Service) UniqueByID(ctx context.Context, uniqueGiftID int64) (domain.UniqueStarGift, bool, error) {
	if s == nil || s.store == nil {
		return domain.UniqueStarGift{}, false, nil
	}
	return s.store.UniqueByID(ctx, uniqueGiftID)
}

func (s *Service) UniqueByIDs(ctx context.Context, uniqueGiftIDs []int64) (map[int64]domain.UniqueStarGift, error) {
	if s == nil || s.store == nil || len(uniqueGiftIDs) == 0 {
		return map[int64]domain.UniqueStarGift{}, nil
	}
	return s.store.UniqueByIDs(ctx, uniqueGiftIDs)
}

func (s *Service) Upgrade(ctx context.Context, req domain.StarGiftUpgradeRequest) (domain.StarGiftUpgradeResult, error) {
	if s == nil || s.upgrades == nil {
		return domain.StarGiftUpgradeResult{}, fmt.Errorf("star gift upgrade store is not configured")
	}
	result, err := s.upgrades.UpgradeStarGift(ctx, req)
	if err == nil {
		s.InvalidateStarGiftCatalog()
	}
	return result, err
}

func (s *Service) UpgradeReceipt(ctx context.Context, userID int64, commandKey string) (domain.StarGiftUpgradeReceipt, bool, error) {
	if s == nil || s.upgrades == nil {
		return domain.StarGiftUpgradeReceipt{}, false, nil
	}
	return s.upgrades.StarGiftUpgradeReceipt(ctx, userID, commandKey)
}

func (s *Service) Purchase(ctx context.Context, req domain.StarGiftPurchaseRequest) (domain.StarGiftPurchaseResult, error) {
	if s == nil || s.lifecycle == nil {
		return domain.StarGiftPurchaseResult{}, domain.ErrStarGiftUnavailable
	}
	result, err := s.lifecycle.PurchaseStarGift(ctx, req)
	if err == nil {
		s.InvalidateStarGiftCatalog()
	}
	return result, err
}

// IssuePurchaseForm creates one fresh payment intent. PostgreSQL persists the
// intent so server restarts cannot turn a valid checkout into an unbound
// payment. The bounded in-memory branch exists only for isolated RPC tests.
func (s *Service) IssuePurchaseForm(ctx context.Context, form domain.StarGiftPurchaseForm) (domain.StarGiftPurchaseForm, error) {
	if !validPurchaseForm(form) {
		return domain.StarGiftPurchaseForm{}, domain.ErrStarGiftFormPurposeInvalid
	}
	if s != nil && s.lifecycle != nil {
		return s.lifecycle.IssueStarGiftPurchaseForm(ctx, form)
	}
	if s == nil {
		return domain.StarGiftPurchaseForm{}, domain.ErrStarGiftUnavailable
	}
	s.formMu.Lock()
	defer s.formMu.Unlock()
	for key, existing := range s.forms {
		if existing.ExpiresAt < form.IssuedAt {
			delete(s.forms, key)
		}
	}
	for attempt := 0; attempt < 8; attempt++ {
		formID, err := randomPositiveInt64()
		if err != nil {
			return domain.StarGiftPurchaseForm{}, err
		}
		key := starGiftPurchaseFormKey{buyerUserID: form.BuyerUserID, formID: formID}
		if _, exists := s.forms[key]; exists {
			continue
		}
		form.FormID = formID
		s.forms[key] = form
		return form, nil
	}
	return domain.StarGiftPurchaseForm{}, domain.ErrStarGiftUnavailable
}

// ValidatePurchaseForm is a read-only preflight used for precise RPC errors.
// The PostgreSQL purchase transaction repeats this validation while holding a
// row lock; callers must not treat this preflight as the atomicity boundary.
func (s *Service) ValidatePurchaseForm(ctx context.Context, req domain.StarGiftPurchaseRequest) error {
	if s != nil && s.lifecycle != nil {
		return s.lifecycle.ValidateStarGiftPurchaseForm(ctx, req)
	}
	if s == nil || req.FormID == 0 {
		return domain.ErrStarGiftFormExpired
	}
	s.formMu.Lock()
	defer s.formMu.Unlock()
	form, ok := s.forms[starGiftPurchaseFormKey{buyerUserID: req.BuyerUserID, formID: req.FormID}]
	if !ok || form.ExpiresAt < req.Date {
		return domain.ErrStarGiftFormExpired
	}
	return validatePurchaseFormIntent(form, req)
}

func validPurchaseForm(form domain.StarGiftPurchaseForm) bool {
	return form.FormID == 0 && form.BuyerUserID > 0 && form.To.ID > 0 &&
		(form.To.Type == domain.PeerTypeUser || form.To.Type == domain.PeerTypeChannel) &&
		form.GiftID > 0 && form.RevisionID > 0 && form.ChargeStars > 0 && form.IssuedAt > 0 &&
		form.ExpiresAt == form.IssuedAt+600 && len([]rune(form.Message)) <= 128
}

func validatePurchaseFormIntent(form domain.StarGiftPurchaseForm, req domain.StarGiftPurchaseRequest) error {
	if form.BuyerUserID != req.BuyerUserID || form.To != req.To || form.GiftID != req.GiftID ||
		form.IncludeUpgrade != req.IncludeUpgrade || form.HideName != req.HideName || form.Message != req.Message {
		return domain.ErrStarGiftFormPurposeInvalid
	}
	if form.RevisionID != req.RevisionID || form.ChargeStars != req.ChargeStars {
		return domain.ErrStarGiftFormAmountMismatch
	}
	return nil
}

func (s *Service) ListResale(ctx context.Context, filter domain.StarGiftResaleFilter) (domain.StarGiftResalePage, error) {
	if s == nil || s.lifecycle == nil {
		return domain.StarGiftResalePage{}, domain.ErrStarGiftResaleUnavailable
	}
	return s.lifecycle.ListResaleStarGifts(ctx, filter)
}

func (s *Service) ValueInfo(ctx context.Context, uniqueGiftID int64) (domain.StarGiftValueInfo, error) {
	if s == nil || s.lifecycle == nil {
		return domain.StarGiftValueInfo{}, domain.ErrStarGiftResaleUnavailable
	}
	return s.lifecycle.UniqueStarGiftValueInfo(ctx, uniqueGiftID)
}

func (s *Service) SetListing(ctx context.Context, req domain.StarGiftListingRequest) (domain.UniqueStarGift, error) {
	if s == nil || s.lifecycle == nil {
		return domain.UniqueStarGift{}, domain.ErrStarGiftResaleUnavailable
	}
	result, err := s.lifecycle.SetStarGiftListing(ctx, req)
	if err == nil {
		s.InvalidateStarGiftCatalog()
	}
	return result, err
}

func (s *Service) Transfer(ctx context.Context, req domain.StarGiftTransferRequest) (domain.StarGiftTransferResult, error) {
	if s == nil || s.lifecycle == nil {
		return domain.StarGiftTransferResult{}, domain.ErrStarGiftTransferUnavailable
	}
	return s.lifecycle.TransferStarGift(ctx, req)
}

func (s *Service) PurchaseResale(ctx context.Context, req domain.StarGiftResalePurchaseRequest) (domain.StarGiftTransferResult, error) {
	if s == nil || s.lifecycle == nil {
		return domain.StarGiftTransferResult{}, domain.ErrStarGiftResaleUnavailable
	}
	result, err := s.lifecycle.PurchaseResaleStarGift(ctx, req)
	if err == nil {
		s.InvalidateStarGiftCatalog()
	}
	return result, err
}

func (s *Service) SendOffer(ctx context.Context, req domain.StarGiftOfferRequest) (domain.StarGiftOfferResult, error) {
	if s == nil || s.lifecycle == nil {
		return domain.StarGiftOfferResult{}, domain.ErrStarGiftOfferInvalid
	}
	return s.lifecycle.SendStarGiftOffer(ctx, req)
}

func (s *Service) ResolveOffer(ctx context.Context, req domain.StarGiftResolveOfferRequest) (domain.StarGiftOfferResult, error) {
	if s == nil || s.lifecycle == nil {
		return domain.StarGiftOfferResult{}, domain.ErrStarGiftOfferInvalid
	}
	return s.lifecycle.ResolveStarGiftOffer(ctx, req)
}

func (s *Service) ListCraft(ctx context.Context, userID, giftID int64, offset string, limit int) (domain.SavedStarGiftPage, error) {
	if s == nil || s.lifecycle == nil {
		return domain.SavedStarGiftPage{}, domain.ErrStarGiftCraftUnavailable
	}
	return s.lifecycle.ListCraftStarGifts(ctx, userID, giftID, offset, limit)
}

func (s *Service) Craft(ctx context.Context, req domain.StarGiftCraftRequest) (domain.StarGiftCraftResult, error) {
	if s == nil || s.lifecycle == nil {
		return domain.StarGiftCraftResult{}, domain.ErrStarGiftCraftUnavailable
	}
	return s.lifecycle.CraftStarGift(ctx, req)
}

func (s *Service) AuctionState(ctx context.Context, userID, giftID int64, slug string, now int) (domain.StarGiftAuction, error) {
	if s == nil || s.lifecycle == nil {
		return domain.StarGiftAuction{}, domain.ErrStarGiftAuctionUnavailable
	}
	return s.lifecycle.StarGiftAuctionState(ctx, userID, giftID, slug, now)
}

func (s *Service) ActiveAuctions(ctx context.Context, userID int64, now int) ([]domain.StarGiftAuction, error) {
	if s == nil || s.lifecycle == nil {
		return nil, domain.ErrStarGiftAuctionUnavailable
	}
	return s.lifecycle.ActiveStarGiftAuctions(ctx, userID, now)
}

func (s *Service) AuctionAcquired(ctx context.Context, userID, giftID int64) ([]domain.StarGiftAuctionAcquired, error) {
	if s == nil || s.lifecycle == nil {
		return nil, domain.ErrStarGiftAuctionUnavailable
	}
	return s.lifecycle.StarGiftAuctionAcquired(ctx, userID, giftID)
}

func (s *Service) BidAuction(ctx context.Context, req domain.StarGiftAuctionBidRequest) (domain.StarGiftAuction, domain.StarsBalance, error) {
	if s == nil || s.lifecycle == nil {
		return domain.StarGiftAuction{}, domain.StarsBalance{}, domain.ErrStarGiftAuctionUnavailable
	}
	return s.lifecycle.BidStarGiftAuction(ctx, req)
}

func (s *Service) PrepaidUpgradeTarget(ctx context.Context, owner domain.Peer, hash string) (domain.SavedStarGift, int64, error) {
	if s == nil || s.lifecycle == nil {
		return domain.SavedStarGift{}, 0, domain.ErrStarGiftCollectibleUnavailable
	}
	return s.lifecycle.PrepaidUpgradeTarget(ctx, owner, hash)
}

func (s *Service) PrepayUpgrade(ctx context.Context, req domain.StarGiftPrepaidUpgradeRequest) (domain.StarGiftPrepaidUpgradeResult, error) {
	if s == nil || s.lifecycle == nil {
		return domain.StarGiftPrepaidUpgradeResult{}, domain.ErrStarGiftCollectibleUnavailable
	}
	return s.lifecycle.PrepayStarGiftUpgrade(ctx, req)
}

func (s *Service) DropOriginalDetails(ctx context.Context, req domain.StarGiftDropOriginalDetailsRequest) (domain.StarGiftDropOriginalDetailsResult, error) {
	if s == nil || s.lifecycle == nil {
		return domain.StarGiftDropOriginalDetailsResult{}, domain.ErrStarGiftCollectibleUnavailable
	}
	return s.lifecycle.DropStarGiftOriginalDetails(ctx, req)
}

func (s *Service) SetNotifications(ctx context.Context, userID, channelID int64, enabled bool) error {
	if s == nil || s.lifecycle == nil {
		return domain.ErrStarGiftUnavailable
	}
	return s.lifecycle.SetStarGiftNotifications(ctx, userID, channelID, enabled)
}

func (s *Service) Withdraw(ctx context.Context, req domain.StarGiftWithdrawalRequest) (domain.StarGiftWithdrawal, error) {
	if s == nil || s.lifecycle == nil || s.withdrawal == nil {
		return domain.StarGiftWithdrawal{}, domain.ErrStarGiftWithdrawalUnavailable
	}
	saved, found, err := s.store.GetByRef(ctx, req.Ref)
	if err != nil || !found || saved.Owner != (domain.Peer{Type: domain.PeerTypeUser, ID: req.UserID}) ||
		saved.UniqueGiftID == 0 || !saved.LifecycleStatus.Live() || saved.CanExportAt > req.Date {
		if err != nil {
			return domain.StarGiftWithdrawal{}, err
		}
		return domain.StarGiftWithdrawal{}, domain.ErrStarGiftTransferUnavailable
	}
	unique, found, err := s.store.UniqueByID(ctx, saved.UniqueGiftID)
	if err != nil || !found || unique.Burned || unique.Owner != saved.Owner {
		if err != nil {
			return domain.StarGiftWithdrawal{}, err
		}
		return domain.StarGiftWithdrawal{}, domain.ErrStarGiftTransferUnavailable
	}
	providerResult, err := s.withdrawal.CreateWithdrawal(ctx, StarGiftWithdrawalProviderRequest{UserID: req.UserID, Gift: unique})
	if err != nil {
		return domain.StarGiftWithdrawal{}, err
	}
	if strings.TrimSpace(providerResult.RequestID) == "" || strings.TrimSpace(providerResult.URL) == "" || providerResult.ExpiresAt <= req.Date {
		return domain.StarGiftWithdrawal{}, domain.ErrStarGiftWithdrawalUnavailable
	}
	recorded, err := s.lifecycle.RecordStarGiftWithdrawal(ctx, req, s.withdrawal.Name(), providerResult.RequestID, providerResult.URL, providerResult.ExpiresAt)
	if err != nil {
		return domain.StarGiftWithdrawal{}, err
	}
	return recorded, nil
}

func (s *Service) ResolveWithdrawal(ctx context.Context, providerRequestID string) (domain.StarGiftWithdrawal, bool, error) {
	if s == nil || s.lifecycle == nil {
		return domain.StarGiftWithdrawal{}, false, nil
	}
	return s.lifecycle.ResolveStarGiftWithdrawal(ctx, providerRequestID)
}

func (s *Service) CompleteWithdrawal(ctx context.Context, providerRequestID string, date int) (domain.StarGiftWithdrawal, error) {
	if s == nil || s.lifecycle == nil {
		return domain.StarGiftWithdrawal{}, domain.ErrStarGiftWithdrawalUnavailable
	}
	return s.lifecycle.CompleteStarGiftWithdrawal(ctx, providerRequestID, date)
}

func (s *Service) TonBalance(ctx context.Context, userID int64) (int64, error) {
	if s == nil || s.lifecycle == nil {
		return 0, nil
	}
	return s.lifecycle.TonBalance(ctx, userID)
}

func (s *Service) TonTransactions(ctx context.Context, userID int64, offset string, limit int) (domain.TonTransactionPage, error) {
	if s == nil || s.lifecycle == nil {
		return domain.TonTransactionPage{}, nil
	}
	if len(offset) > domain.MaxStarsTransactionsOffsetBytes {
		offset = ""
	}
	if limit <= 0 || limit > domain.MaxStarsTransactionsLimit {
		limit = domain.MaxStarsTransactionsLimit
	}
	return s.lifecycle.TonTransactions(ctx, userID, offset, limit)
}

func (s *Service) ChannelStarsBalance(ctx context.Context, channelID int64) (int64, error) {
	if s == nil || s.lifecycle == nil {
		return 0, nil
	}
	return s.lifecycle.ChannelStarsBalance(ctx, channelID)
}

func (s *Service) ChannelStarsTransactions(ctx context.Context, channelID int64, offset string, limit int) (domain.StarsTransactionPage, error) {
	if s == nil || s.lifecycle == nil {
		return domain.StarsTransactionPage{}, nil
	}
	if len(offset) > domain.MaxStarsTransactionsOffsetBytes {
		offset = ""
	}
	if limit <= 0 || limit > domain.MaxStarsTransactionsLimit {
		limit = domain.MaxStarsTransactionsLimit
	}
	return s.lifecycle.ChannelStarsTransactions(ctx, channelID, offset, limit)
}

func (s *Service) ChannelTonBalance(ctx context.Context, channelID int64) (int64, error) {
	if s == nil || s.lifecycle == nil {
		return 0, nil
	}
	return s.lifecycle.ChannelTonBalance(ctx, channelID)
}

func (s *Service) ChannelTonTransactions(ctx context.Context, channelID int64, offset string, limit int) (domain.TonTransactionPage, error) {
	if s == nil || s.lifecycle == nil {
		return domain.TonTransactionPage{}, nil
	}
	if len(offset) > domain.MaxStarsTransactionsOffsetBytes {
		offset = ""
	}
	if limit <= 0 || limit > domain.MaxStarsTransactionsLimit {
		limit = domain.MaxStarsTransactionsLimit
	}
	return s.lifecycle.ChannelTonTransactions(ctx, channelID, offset, limit)
}

func (s *Service) SweepLifecycle(ctx context.Context, now, limit int) error {
	if s == nil || s.lifecycle == nil {
		return nil
	}
	return s.lifecycle.SweepStarGiftLifecycle(ctx, now, limit)
}

func (s *Service) ListCollections(ctx context.Context, owner domain.Peer) ([]domain.StarGiftCollection, error) {
	return s.store.ListCollections(ctx, owner)
}

func (s *Service) CreateCollection(ctx context.Context, owner domain.Peer, title string, savedGiftIDs []int64) (domain.StarGiftCollection, error) {
	return s.store.CreateCollection(ctx, owner, title, savedGiftIDs)
}

func (s *Service) UpdateCollection(ctx context.Context, owner domain.Peer, collectionID int, patch domain.StarGiftCollectionPatch) (domain.StarGiftCollection, error) {
	return s.store.UpdateCollection(ctx, owner, collectionID, patch)
}

func (s *Service) DeleteCollection(ctx context.Context, owner domain.Peer, collectionID int) (bool, error) {
	return s.store.DeleteCollection(ctx, owner, collectionID)
}

func (s *Service) ReorderCollections(ctx context.Context, owner domain.Peer, collectionIDs []int) error {
	return s.store.ReorderCollections(ctx, owner, collectionIDs)
}

func (s *Service) SetPinned(ctx context.Context, owner domain.Peer, savedGiftIDs []int64) error {
	return s.store.SetPinned(ctx, owner, savedGiftIDs)
}

func (s *Service) RecordSavedGift(ctx context.Context, gift domain.SavedStarGift) (int64, error) {
	if gift.UniqueGiftID == 0 && gift.PrepaidUpgradeStars == 0 && gift.PrepaidUpgradeHash == "" && s.store != nil {
		if revision, ok, err := s.store.ActiveCollectibleRevision(ctx, gift.GiftID); err != nil {
			return 0, err
		} else if ok && revision.Published && revision.Issued < revision.SupplyTotal {
			var token [32]byte
			if _, err := rand.Read(token[:]); err != nil {
				return 0, fmt.Errorf("generate prepaid star gift upgrade hash: %w", err)
			}
			gift.PrepaidUpgradeHash = base64.RawURLEncoding.EncodeToString(token[:])
		}
	}
	return s.store.Create(ctx, gift)
}

func (s *Service) ListSaved(ctx context.Context, owner domain.Peer, excludeUnsaved bool, offset string, limit int) (domain.SavedStarGiftPage, error) {
	return s.ListSavedFiltered(ctx, domain.SavedStarGiftFilter{
		Owner: owner, ExcludeUnsaved: excludeUnsaved, Offset: offset, Limit: limit,
	})
}

func (s *Service) ListSavedFiltered(ctx context.Context, filter domain.SavedStarGiftFilter) (domain.SavedStarGiftPage, error) {
	offset := filter.Offset
	if len(offset) > domain.MaxStarGiftsOffsetBytes {
		filter.Offset = ""
	}
	if filter.Limit <= 0 || filter.Limit > domain.MaxSavedStarGiftsLimit {
		filter.Limit = domain.MaxSavedStarGiftsLimit
	}
	return s.store.ListByOwnerFiltered(ctx, filter)
}

func (s *Service) GetSaved(ctx context.Context, ref domain.SavedStarGiftRef) (domain.SavedStarGift, bool, error) {
	return s.store.GetByRef(ctx, ref)
}

func (s *Service) ResolveSavedIDs(ctx context.Context, owner domain.Peer, refs []domain.SavedStarGiftRef) ([]int64, error) {
	return s.store.ResolveSavedIDs(ctx, owner, refs)
}

func (s *Service) CountSaved(ctx context.Context, owner domain.Peer) (int, error) {
	return s.store.CountByOwner(ctx, owner)
}

func (s *Service) ToggleSaved(ctx context.Context, ref domain.SavedStarGiftRef, unsaved bool) (bool, error) {
	return s.store.SetUnsaved(ctx, ref, unsaved)
}

// Convert keeps the in-memory/catalog store primitive available to isolated
// tests and non-production adapters. RPC production paths must use
// ConvertAggregate so balance credit and terminal state cannot split.
func (s *Service) Convert(ctx context.Context, ref domain.SavedStarGiftRef) (domain.SavedStarGift, error) {
	return s.store.MarkConverted(ctx, ref)
}

func (s *Service) ConvertAggregate(ctx context.Context, req domain.StarGiftConvertRequest) (domain.StarGiftConvertResult, error) {
	if s == nil || s.lifecycle == nil {
		return domain.StarGiftConvertResult{}, domain.ErrStarGiftUnavailable
	}
	return s.lifecycle.ConvertStarGift(ctx, req)
}

func randomPositiveInt64() (int64, error) {
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return 0, fmt.Errorf("generate star gift id: %w", err)
	}
	id := int64(binary.LittleEndian.Uint64(raw[:]) & 0x7fffffffffffffff)
	if id == 0 {
		id = 1
	}
	return id, nil
}
