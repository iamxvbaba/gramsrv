package memory

import (
	"context"
	"sort"
	"strings"
	"sync"

	"telesrv/internal/domain"
)

// StarGiftStore 是 store.StarGiftStore 的内存实现。
type StarGiftStore struct {
	mu               sync.Mutex
	nextID           int64
	nextGiftID       int64
	nextRevID        int64
	gifts            []domain.SavedStarGift // 追加序
	catalog          map[int64]domain.StarGift
	revisions        map[int64]domain.StarGift
	enabled          map[int64]bool
	sortOrder        map[int64]int
	animations       map[int64][]byte
	collectibles     map[int64]domain.StarGiftCollectibleRevision
	uniqueByID       map[int64]domain.UniqueStarGift
	uniqueBySlug     map[string]int64
	collections      map[domain.Peer][]domain.StarGiftCollection
	nextAttributeID  int64
	nextCollectionID int
}

// NewStarGiftStore 创建内存 StarGiftStore。
func NewStarGiftStore() *StarGiftStore {
	return &StarGiftStore{
		catalog: make(map[int64]domain.StarGift), revisions: make(map[int64]domain.StarGift),
		enabled: make(map[int64]bool), sortOrder: make(map[int64]int), animations: make(map[int64][]byte),
		collectibles: make(map[int64]domain.StarGiftCollectibleRevision),
		uniqueByID:   make(map[int64]domain.UniqueStarGift), uniqueBySlug: make(map[string]int64),
		collections: make(map[domain.Peer][]domain.StarGiftCollection),
	}
}

// SeedCatalog installs valid immutable catalog snapshots for tests.
func (s *StarGiftStore) SeedCatalog(gifts []domain.StarGift) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, gift := range gifts {
		if gift.RevisionID == 0 {
			s.nextRevID++
			gift.RevisionID = s.nextRevID
		}
		if gift.ID > s.nextGiftID {
			s.nextGiftID = gift.ID
		}
		if gift.RevisionID > s.nextRevID {
			s.nextRevID = gift.RevisionID
		}
		s.catalog[gift.ID] = gift
		s.revisions[gift.RevisionID] = gift
		s.enabled[gift.ID] = true
	}
}

func (s *StarGiftStore) Catalog(_ context.Context) ([]domain.StarGift, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]domain.StarGift, 0, len(s.catalog))
	for id, gift := range s.catalog {
		if s.enabled[id] {
			out = append(out, gift)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if s.sortOrder[out[i].ID] == s.sortOrder[out[j].ID] {
			return out[i].ID < out[j].ID
		}
		return s.sortOrder[out[i].ID] < s.sortOrder[out[j].ID]
	})
	return out, nil
}

func (s *StarGiftStore) CatalogGift(_ context.Context, giftID int64) (domain.StarGift, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	gift, ok := s.catalog[giftID]
	return gift, ok && s.enabled[giftID], nil
}

func (s *StarGiftStore) CatalogRevision(_ context.Context, revisionID int64) (domain.StarGift, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	gift, ok := s.revisions[revisionID]
	return gift, ok, nil
}

func (s *StarGiftStore) CreateCatalogRevision(_ context.Context, write domain.StarGiftCatalogWrite) (domain.StarGiftCatalogEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	giftID := write.GiftID
	if giftID == 0 {
		s.nextGiftID++
		giftID = s.nextGiftID
	} else if _, ok := s.catalog[giftID]; !ok {
		return domain.StarGiftCatalogEntry{}, domain.ErrStarGiftNotFound
	}
	s.nextRevID++
	gift := domain.StarGift{ID: giftID, RevisionID: s.nextRevID, Stars: write.Stars, ConvertStars: write.ConvertStars, Title: write.Title, Sticker: write.Document}
	s.catalog[giftID] = gift
	s.revisions[gift.RevisionID] = gift
	s.enabled[giftID] = write.Enabled
	s.sortOrder[giftID] = write.SortOrder
	s.animations[giftID] = append([]byte(nil), write.Animation.JSON...)
	return domain.StarGiftCatalogEntry{Gift: gift, Enabled: write.Enabled, SortOrder: write.SortOrder}, nil
}

func (s *StarGiftStore) SetCatalogEnabled(_ context.Context, giftID int64, enabled bool) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.catalog[giftID]; !ok {
		return false, domain.ErrStarGiftNotFound
	}
	changed := s.enabled[giftID] != enabled
	s.enabled[giftID] = enabled
	return changed, nil
}

func (s *StarGiftStore) SetCatalogSortOrder(_ context.Context, giftID int64, sortOrder int) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.catalog[giftID]; !ok {
		return false, domain.ErrStarGiftNotFound
	}
	changed := s.sortOrder[giftID] != sortOrder
	s.sortOrder[giftID] = sortOrder
	return changed, nil
}

func (s *StarGiftStore) AnimationJSON(_ context.Context, giftID int64) ([]byte, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	raw, ok := s.animations[giftID]
	return append([]byte(nil), raw...), ok, nil
}

func (s *StarGiftStore) PublishCollectibleRevision(_ context.Context, write domain.StarGiftCollectibleWrite) (domain.StarGiftCollectibleRevision, error) {
	if err := domain.ValidateStarGiftCollectibleWrite(write); err != nil {
		return domain.StarGiftCollectibleRevision{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.catalog[write.GiftID]; !ok {
		return domain.StarGiftCollectibleRevision{}, domain.ErrStarGiftNotFound
	}
	previous := s.collectibles[write.GiftID]
	revision := domain.StarGiftCollectibleRevision{
		ID: previous.ID + 1, GiftID: write.GiftID, Revision: previous.Revision + 1,
		UpgradeStars: write.UpgradeStars, SupplyTotal: write.SupplyTotal,
		SlugPrefix: strings.ToLower(strings.TrimSpace(write.SlugPrefix)), Published: true,
		CreatedBy: write.Actor,
	}
	if revision.ID == 1 {
		revision.ID = write.GiftID*1000 + 1
	}
	revision.Models = s.allocateCollectibleAttributes(write.Models, revision.ID)
	revision.Patterns = s.allocateCollectibleAttributes(write.Patterns, revision.ID)
	revision.Backdrops = s.allocateCollectibleAttributes(write.Backdrops, revision.ID)
	s.collectibles[write.GiftID] = revision
	gift := s.catalog[write.GiftID]
	gift.UpgradeStars = revision.UpgradeStars
	gift.UpgradeTotal = revision.SupplyTotal
	gift.UpgradeIssued = revision.Issued
	s.catalog[write.GiftID] = gift
	return cloneCollectibleRevision(revision), nil
}

func (s *StarGiftStore) allocateCollectibleAttributes(in []domain.StarGiftCollectibleAttribute, revisionID int64) []domain.StarGiftCollectibleAttribute {
	out := make([]domain.StarGiftCollectibleAttribute, len(in))
	for i, attribute := range in {
		s.nextAttributeID++
		attribute.ID = s.nextAttributeID
		attribute.CollectibleRevisionID = revisionID
		out[i] = cloneCollectibleAttribute(attribute)
	}
	return out
}

func (s *StarGiftStore) ActiveCollectibleRevision(_ context.Context, giftID int64) (domain.StarGiftCollectibleRevision, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	revision, ok := s.collectibles[giftID]
	return cloneCollectibleRevision(revision), ok, nil
}

func (s *StarGiftStore) CollectibleAvailability(_ context.Context, giftIDs []int64) (map[int64]domain.StarGiftCollectibleAvailability, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[int64]domain.StarGiftCollectibleAvailability, len(giftIDs))
	for _, giftID := range giftIDs {
		revision, ok := s.collectibles[giftID]
		if !ok || !revision.Published {
			continue
		}
		out[giftID] = domain.StarGiftCollectibleAvailability{
			UpgradeStars: revision.UpgradeStars,
			SupplyTotal:  revision.SupplyTotal,
			Issued:       revision.Issued,
		}
	}
	return out, nil
}

func (s *StarGiftStore) CollectibleAnimationJSON(_ context.Context, giftID int64, kind domain.StarGiftCollectibleAttributeKind, attributeID int64) ([]byte, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	revision, ok := s.collectibles[giftID]
	if !ok {
		return nil, false, nil
	}
	var attributes []domain.StarGiftCollectibleAttribute
	switch kind {
	case domain.StarGiftCollectibleModel:
		attributes = revision.Models
	case domain.StarGiftCollectiblePattern:
		attributes = revision.Patterns
	default:
		return nil, false, nil
	}
	for _, attribute := range attributes {
		if attribute.ID == attributeID && attribute.Animation != nil {
			return append([]byte(nil), attribute.Animation.JSON...), true, nil
		}
	}
	return nil, false, nil
}

func (s *StarGiftStore) UniqueBySlug(_ context.Context, slug string) (domain.UniqueStarGift, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id, ok := s.uniqueBySlug[strings.ToLower(strings.TrimSpace(slug))]
	if !ok {
		return domain.UniqueStarGift{}, false, nil
	}
	unique, ok := s.uniqueByID[id]
	return unique, ok, nil
}

func (s *StarGiftStore) UniqueByID(_ context.Context, uniqueGiftID int64) (domain.UniqueStarGift, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	unique, ok := s.uniqueByID[uniqueGiftID]
	return unique, ok, nil
}

func (s *StarGiftStore) UniqueByIDs(_ context.Context, uniqueGiftIDs []int64) (map[int64]domain.UniqueStarGift, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[int64]domain.UniqueStarGift, len(uniqueGiftIDs))
	for _, id := range uniqueGiftIDs {
		if gift, ok := s.uniqueByID[id]; ok {
			out[id] = gift
		}
	}
	return out, nil
}

func (s *StarGiftStore) Create(_ context.Context, gift domain.SavedStarGift) (int64, error) {
	if !validSavedStarGift(gift) {
		return 0, domain.ErrStarGiftInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextID++
	gift.ID = s.nextID
	if gift.Owner.Type == domain.PeerTypeChannel && gift.SavedID == 0 {
		gift.SavedID = gift.ID
	}
	gift.Converted = false
	s.gifts = append(s.gifts, gift)
	return gift.ID, nil
}

func (s *StarGiftStore) ListByOwner(_ context.Context, owner domain.Peer, excludeUnsaved bool, offset string, limit int) (domain.SavedStarGiftPage, error) {
	return s.ListByOwnerFiltered(context.Background(), domain.SavedStarGiftFilter{
		Owner: owner, ExcludeUnsaved: excludeUnsaved, Offset: offset, Limit: limit,
	})
}

func (s *StarGiftStore) ListByOwnerFiltered(_ context.Context, filter domain.SavedStarGiftFilter) (domain.SavedStarGiftPage, error) {
	owner, offset, limit := filter.Owner, filter.Offset, filter.Limit
	if !validStarGiftOwner(owner) {
		return domain.SavedStarGiftPage{}, nil
	}
	if limit <= 0 || limit > domain.MaxSavedStarGiftsLimit {
		limit = domain.MaxSavedStarGiftsLimit
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	matched := make([]domain.SavedStarGift, 0)
	for _, g := range s.gifts {
		if g.Owner != owner || g.Converted {
			continue
		}
		if filter.ExcludeUnsaved && g.Unsaved {
			continue
		}
		if filter.ExcludeSaved && !g.Unsaved {
			continue
		}
		if filter.ExcludeUnique && g.UniqueGiftID != 0 {
			continue
		}
		if filter.ExcludeUnlimited && g.UniqueGiftID == 0 {
			continue
		}
		upgradable := false
		if g.UniqueGiftID == 0 {
			if gift, ok := s.catalog[g.GiftID]; ok {
				upgradable = gift.UpgradeStars > 0 && gift.UpgradeIssued < gift.UpgradeTotal
			}
		}
		if filter.ExcludeUpgradable && upgradable {
			continue
		}
		if filter.ExcludeUnupgradable && !upgradable {
			continue
		}
		if filter.CollectionID > 0 && !containsInt(g.CollectionIDs, filter.CollectionID) {
			continue
		}
		matched = append(matched, g)
	}
	sort.Slice(matched, func(i, j int) bool { return matched[i].ID > matched[j].ID })
	page := domain.SavedStarGiftPage{Count: len(matched)}
	cursor, hasCursor := domain.DecodeStarGiftCursor(offset)
	out := make([]domain.SavedStarGift, 0, limit)
	for _, g := range matched {
		if hasCursor && g.ID >= cursor {
			continue
		}
		out = append(out, g)
		if len(out) == limit {
			break
		}
	}
	if len(out) == limit {
		// 还有更早的则给下一页游标。
		last := out[len(out)-1].ID
		for _, g := range matched {
			if g.ID < last {
				page.NextOffset = domain.EncodeStarGiftCursor(last)
				break
			}
		}
	}
	page.Gifts = out
	return page, nil
}

func (s *StarGiftStore) ResolveSavedIDs(_ context.Context, owner domain.Peer, refs []domain.SavedStarGiftRef) ([]int64, error) {
	if !validStarGiftOwner(owner) || len(refs) > domain.MaxStarGiftCollectionItems {
		return nil, domain.ErrStarGiftCollectibleInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]int64, 0, len(refs))
	seen := make(map[int64]struct{}, len(refs))
	for _, ref := range refs {
		if ref.Owner != owner || !ref.Valid() {
			return nil, domain.ErrStarGiftNotFound
		}
		var id int64
		for _, gift := range s.gifts {
			if savedStarGiftMatchesRef(gift, ref) && !gift.Converted {
				id = gift.ID
				break
			}
		}
		if id == 0 {
			return nil, domain.ErrStarGiftNotFound
		}
		if _, exists := seen[id]; exists {
			return nil, domain.ErrStarGiftCollectibleInvalid
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out, nil
}

func (s *StarGiftStore) GetByRef(_ context.Context, ref domain.SavedStarGiftRef) (domain.SavedStarGift, bool, error) {
	if !ref.Valid() {
		return domain.SavedStarGift{}, false, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, g := range s.gifts {
		if savedStarGiftMatchesRef(g, ref) {
			return g, true, nil
		}
	}
	return domain.SavedStarGift{}, false, nil
}

func (s *StarGiftStore) CountByOwner(_ context.Context, owner domain.Peer) (int, error) {
	if !validStarGiftOwner(owner) {
		return 0, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, g := range s.gifts {
		if g.Owner == owner && !g.Converted && !g.Unsaved {
			n++
		}
	}
	return n, nil
}

func (s *StarGiftStore) SetUnsaved(_ context.Context, ref domain.SavedStarGiftRef, unsaved bool) (bool, error) {
	if !ref.Valid() {
		return false, domain.ErrStarGiftNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.gifts {
		if savedStarGiftMatchesRef(s.gifts[i], ref) && !s.gifts[i].Converted {
			s.gifts[i].Unsaved = unsaved
			return true, nil
		}
	}
	return false, nil
}

func (s *StarGiftStore) MarkConverted(_ context.Context, ref domain.SavedStarGiftRef) (domain.SavedStarGift, error) {
	if !ref.Valid() {
		return domain.SavedStarGift{}, domain.ErrStarGiftNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.gifts {
		if savedStarGiftMatchesRef(s.gifts[i], ref) {
			if s.gifts[i].UniqueGiftID != 0 {
				return domain.SavedStarGift{}, domain.ErrStarGiftAlreadyUpgraded
			}
			if s.gifts[i].Converted {
				return domain.SavedStarGift{}, domain.ErrStarGiftAlreadyConverted
			}
			s.gifts[i].Converted = true
			s.gifts[i].Unsaved = true
			s.gifts[i].PinnedOrder = 0
			for collectionIndex := range s.collections[ref.Owner] {
				collection := &s.collections[ref.Owner][collectionIndex]
				next := collection.GiftIDs[:0]
				for _, giftID := range collection.GiftIDs {
					if giftID != s.gifts[i].ID {
						next = append(next, giftID)
					}
				}
				collection.GiftIDs = next
				collection.Hash = domain.StarGiftCollectionHash(collection.Title, collection.GiftIDs)
			}
			s.refreshCollectionMembershipsLocked(ref.Owner)
			return s.gifts[i], nil
		}
	}
	return domain.SavedStarGift{}, domain.ErrStarGiftNotFound
}

func (s *StarGiftStore) ListCollections(_ context.Context, owner domain.Peer) ([]domain.StarGiftCollection, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneStarGiftCollections(s.collections[owner]), nil
}

func (s *StarGiftStore) CreateCollection(_ context.Context, owner domain.Peer, title string, savedGiftIDs []int64) (domain.StarGiftCollection, error) {
	title = strings.TrimSpace(title)
	if !validStarGiftOwner(owner) || title == "" || len([]rune(title)) > domain.MaxStarGiftCollectionTitleRunes {
		return domain.StarGiftCollection{}, domain.ErrStarGiftCollectibleInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.collections[owner]) >= domain.MaxStarGiftCollectionsPerPeer {
		return domain.StarGiftCollection{}, domain.ErrStarGiftCollectionsFull
	}
	ids, err := s.validCollectionGiftIDsLocked(owner, savedGiftIDs)
	if err != nil {
		return domain.StarGiftCollection{}, err
	}
	s.nextCollectionID++
	collection := domain.StarGiftCollection{Owner: owner, CollectionID: s.nextCollectionID, Title: title, GiftIDs: ids, SortOrder: len(s.collections[owner])}
	collection.Hash = domain.StarGiftCollectionHash(collection.Title, collection.GiftIDs)
	s.collections[owner] = append(s.collections[owner], collection)
	s.refreshCollectionMembershipsLocked(owner)
	return collection, nil
}

func (s *StarGiftStore) UpdateCollection(_ context.Context, owner domain.Peer, collectionID int, patch domain.StarGiftCollectionPatch) (domain.StarGiftCollection, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	collections := s.collections[owner]
	index := -1
	for i := range collections {
		if collections[i].CollectionID == collectionID {
			index = i
			break
		}
	}
	if index < 0 {
		return domain.StarGiftCollection{}, domain.ErrStarGiftCollectionNotFound
	}
	collection := collections[index]
	if patch.Title != nil {
		title := strings.TrimSpace(*patch.Title)
		if title == "" || len([]rune(title)) > domain.MaxStarGiftCollectionTitleRunes {
			return domain.StarGiftCollection{}, domain.ErrStarGiftCollectibleInvalid
		}
		collection.Title = title
	}
	deleteSet := make(map[int64]struct{}, len(patch.DeleteIDs))
	for _, id := range patch.DeleteIDs {
		deleteSet[id] = struct{}{}
	}
	next := make([]int64, 0, len(collection.GiftIDs)+len(patch.AddIDs))
	for _, id := range collection.GiftIDs {
		if _, deleted := deleteSet[id]; !deleted {
			next = append(next, id)
		}
	}
	add, err := s.validCollectionGiftIDsLocked(owner, patch.AddIDs)
	if err != nil {
		return domain.StarGiftCollection{}, err
	}
	next = appendUniqueInt64(next, add...)
	if patch.Order != nil {
		order, err := s.validCollectionGiftIDsLocked(owner, patch.Order)
		if err != nil || !sameInt64Set(order, next) {
			return domain.StarGiftCollection{}, domain.ErrStarGiftCollectibleInvalid
		}
		next = order
	}
	if len(next) > domain.MaxStarGiftCollectionItems {
		return domain.StarGiftCollection{}, domain.ErrStarGiftCollectibleInvalid
	}
	collection.GiftIDs = next
	collection.Hash = domain.StarGiftCollectionHash(collection.Title, collection.GiftIDs)
	collections[index] = collection
	s.collections[owner] = collections
	s.refreshCollectionMembershipsLocked(owner)
	return collection, nil
}

func (s *StarGiftStore) DeleteCollection(_ context.Context, owner domain.Peer, collectionID int) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	collections := s.collections[owner]
	for i := range collections {
		if collections[i].CollectionID == collectionID {
			collections = append(collections[:i], collections[i+1:]...)
			for j := range collections {
				collections[j].SortOrder = j
			}
			s.collections[owner] = collections
			s.refreshCollectionMembershipsLocked(owner)
			return true, nil
		}
	}
	return false, nil
}

func (s *StarGiftStore) ReorderCollections(_ context.Context, owner domain.Peer, collectionIDs []int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	collections := s.collections[owner]
	if len(collectionIDs) != len(collections) {
		return domain.ErrStarGiftCollectibleInvalid
	}
	byID := make(map[int]domain.StarGiftCollection, len(collections))
	for _, collection := range collections {
		byID[collection.CollectionID] = collection
	}
	next := make([]domain.StarGiftCollection, 0, len(collections))
	for order, id := range collectionIDs {
		collection, ok := byID[id]
		if !ok {
			return domain.ErrStarGiftCollectibleInvalid
		}
		delete(byID, id)
		collection.SortOrder = order
		next = append(next, collection)
	}
	s.collections[owner] = next
	return nil
}

func (s *StarGiftStore) SetPinned(_ context.Context, owner domain.Peer, savedGiftIDs []int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ids, err := s.validCollectionGiftIDsLocked(owner, savedGiftIDs)
	if err != nil {
		return err
	}
	order := make(map[int64]int, len(ids))
	for i, id := range ids {
		order[id] = i + 1
	}
	for i := range s.gifts {
		if s.gifts[i].Owner == owner {
			s.gifts[i].PinnedOrder = order[s.gifts[i].ID]
		}
	}
	return nil
}

// refreshCollectionMembershipsLocked keeps the in-memory saved-gift projection
// equivalent to the PostgreSQL join projection. Callers must hold s.mu.
func (s *StarGiftStore) refreshCollectionMembershipsLocked(owner domain.Peer) {
	memberships := make(map[int64][]int)
	for _, collection := range s.collections[owner] {
		for _, giftID := range collection.GiftIDs {
			memberships[giftID] = append(memberships[giftID], collection.CollectionID)
		}
	}
	for i := range s.gifts {
		if s.gifts[i].Owner != owner {
			continue
		}
		s.gifts[i].CollectionIDs = append([]int(nil), memberships[s.gifts[i].ID]...)
	}
}

func (s *StarGiftStore) validCollectionGiftIDsLocked(owner domain.Peer, ids []int64) ([]int64, error) {
	if len(ids) > domain.MaxStarGiftCollectionItems {
		return nil, domain.ErrStarGiftCollectibleInvalid
	}
	out := make([]int64, 0, len(ids))
	seen := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		if _, ok := seen[id]; ok {
			continue
		}
		valid := false
		for _, gift := range s.gifts {
			if gift.ID == id && gift.Owner == owner && !gift.Converted {
				valid = true
				break
			}
		}
		if !valid {
			return nil, domain.ErrStarGiftNotFound
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out, nil
}

func appendUniqueInt64(dst []int64, values ...int64) []int64 {
	seen := make(map[int64]struct{}, len(dst)+len(values))
	for _, id := range dst {
		seen[id] = struct{}{}
	}
	for _, id := range values {
		if _, ok := seen[id]; !ok {
			seen[id] = struct{}{}
			dst = append(dst, id)
		}
	}
	return dst
}

func sameInt64Set(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	seen := make(map[int64]int, len(a))
	for _, id := range a {
		seen[id]++
	}
	for _, id := range b {
		seen[id]--
		if seen[id] < 0 {
			return false
		}
	}
	return true
}

func cloneCollectibleAttribute(in domain.StarGiftCollectibleAttribute) domain.StarGiftCollectibleAttribute {
	out := in
	if in.Document != nil {
		document := *in.Document
		out.Document = &document
	}
	if in.Animation != nil {
		animation := *in.Animation
		animation.JSON = append([]byte(nil), in.Animation.JSON...)
		animation.TGS = append([]byte(nil), in.Animation.TGS...)
		animation.SHA256 = append([]byte(nil), in.Animation.SHA256...)
		out.Animation = &animation
	}
	if in.Blob != nil {
		blob := *in.Blob
		out.Blob = &blob
	}
	return out
}

func cloneCollectibleRevision(in domain.StarGiftCollectibleRevision) domain.StarGiftCollectibleRevision {
	out := in
	clone := func(attributes []domain.StarGiftCollectibleAttribute) []domain.StarGiftCollectibleAttribute {
		copy := make([]domain.StarGiftCollectibleAttribute, len(attributes))
		for i, attribute := range attributes {
			copy[i] = cloneCollectibleAttribute(attribute)
		}
		return copy
	}
	out.Models = clone(in.Models)
	out.Patterns = clone(in.Patterns)
	out.Backdrops = clone(in.Backdrops)
	return out
}

func cloneStarGiftCollections(in []domain.StarGiftCollection) []domain.StarGiftCollection {
	out := make([]domain.StarGiftCollection, len(in))
	for i, collection := range in {
		out[i] = collection
		out[i].GiftIDs = append([]int64(nil), collection.GiftIDs...)
	}
	return out
}

func validSavedStarGift(g domain.SavedStarGift) bool {
	if g.GiftID == 0 || g.RevisionID == 0 || !validStarGiftOwner(g.Owner) {
		return false
	}
	switch g.Owner.Type {
	case domain.PeerTypeUser:
		return g.MsgID > 0 && g.SavedID == 0
	case domain.PeerTypeChannel:
		return g.MsgID == 0 && g.SavedID >= 0
	default:
		return false
	}
}

func validStarGiftOwner(owner domain.Peer) bool {
	return owner.ID != 0 && (owner.Type == domain.PeerTypeUser || owner.Type == domain.PeerTypeChannel)
}

func savedStarGiftMatchesRef(g domain.SavedStarGift, ref domain.SavedStarGiftRef) bool {
	if g.Owner != ref.Owner {
		return false
	}
	switch ref.Owner.Type {
	case domain.PeerTypeUser:
		return g.MsgID == ref.MsgID
	case domain.PeerTypeChannel:
		return g.SavedID == ref.SavedID
	default:
		return false
	}
}
