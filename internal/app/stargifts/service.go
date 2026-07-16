// Package stargifts implements the durable Star Gift catalog and received-gift state.
package stargifts

import (
	"context"
	"crypto/rand"
	"encoding/binary"
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
	store    store.StarGiftStore
	upgrades store.StarGiftUpgradeStore
	blobs    BlobBackend
	dc       int

	mu    sync.RWMutex
	built bool
	gifts []domain.StarGift
	byID  map[int64]domain.StarGift
	hash  int
}

type Option func(*Service)

func WithUpgradeStore(upgrades store.StarGiftUpgradeStore) Option {
	return func(service *Service) { service.upgrades = upgrades }
}

func NewService(st store.StarGiftStore, blobs BlobBackend, dc int, opts ...Option) *Service {
	service := &Service{store: st, blobs: blobs, dc: dc}
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
	objectKey, err := s.blobs.Put(ctx, write.Animation.TGS)
	if err != nil {
		return domain.StarGiftCatalogEntry{}, fmt.Errorf("store star gift animation: %w", err)
	}
	documentID, err := randomPositiveInt64()
	if err != nil {
		return domain.StarGiftCatalogEntry{}, err
	}
	accessHash, err := randomPositiveInt64()
	if err != nil {
		return domain.StarGiftCatalogEntry{}, err
	}
	fileReference := make([]byte, 16)
	if _, err := rand.Read(fileReference); err != nil {
		return domain.StarGiftCatalogEntry{}, fmt.Errorf("generate star gift file reference: %w", err)
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
	entry, err := s.store.CreateCatalogRevision(ctx, write)
	if err != nil {
		return domain.StarGiftCatalogEntry{}, err
	}
	s.InvalidateStarGiftCatalog()
	return entry, nil
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
	materialize := func(attributes []domain.StarGiftCollectibleAttribute) error {
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
	if err := materialize(write.Models); err != nil {
		return domain.StarGiftCollectibleRevision{}, err
	}
	if err := materialize(write.Patterns); err != nil {
		return domain.StarGiftCollectibleRevision{}, err
	}
	return s.PublishCollectibleRevision(ctx, write)
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

func (s *Service) Convert(ctx context.Context, ref domain.SavedStarGiftRef) (domain.SavedStarGift, error) {
	return s.store.MarkConverted(ctx, ref)
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
