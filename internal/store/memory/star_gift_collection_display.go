package memory

import (
	"context"
	"time"

	"telesrv/internal/domain"
)

func (s *StarGiftStore) ListCollectionDisplaySettings(_ context.Context, owner domain.Peer) ([]domain.StarGiftCollectionDisplaySettings, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	collections := s.collections[owner]
	out := make([]domain.StarGiftCollectionDisplaySettings, 0, len(collections))
	for _, collection := range collections {
		out = append(out, s.collectionDisplaySettingsLocked(owner, collection))
	}
	return out, nil
}

func (s *StarGiftStore) GetCollectionDisplaySettings(_ context.Context, owner domain.Peer, collectionID int) (domain.StarGiftCollectionDisplaySettings, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, collection := range s.collections[owner] {
		if collection.CollectionID == collectionID {
			return s.collectionDisplaySettingsLocked(owner, collection), nil
		}
	}
	return domain.StarGiftCollectionDisplaySettings{}, domain.ErrStarGiftCollectionNotFound
}

func (s *StarGiftStore) UpdateCollectionDisplaySettings(_ context.Context, owner domain.Peer, collectionID int, patch domain.StarGiftCollectionDisplayPatch) (domain.StarGiftCollectionDisplaySettings, error) {
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
		return domain.StarGiftCollectionDisplaySettings{}, domain.ErrStarGiftCollectionNotFound
	}
	if patch.FilterMask != nil && (*patch.FilterMask < 0 || uint32(*patch.FilterMask)&^domain.StarGiftCollectionDisplayFilterMaskAll != 0) {
		return domain.StarGiftCollectionDisplaySettings{}, domain.ErrStarGiftCollectionDisplayInvalid
	}
	collection := collections[index]
	current := s.collectionDisplaySettingsLocked(owner, collection)
	if patch.Hidden != nil {
		current.Hidden = *patch.Hidden
	}
	if patch.FilterMask != nil {
		current.FilterMask = *patch.FilterMask
	}
	if patch.MainTab != nil {
		current.MainTab = *patch.MainTab
	}
	if patch.ItemsSet {
		items, err := domain.NormalizeStarGiftCollectionDisplayItems(collection.GiftIDs, patch.Items)
		if err != nil {
			return domain.StarGiftCollectionDisplaySettings{}, err
		}
		current.Items = items
		collection.GiftIDs = make([]int64, len(items))
		for i, item := range items {
			collection.GiftIDs[i] = item.GiftID
		}
		collection.Hash = domain.StarGiftCollectionHash(collection.Title, collection.GiftIDs)
		collections[index] = collection
		s.collections[owner] = collections
		s.refreshCollectionMembershipsLocked(owner)
	}
	if patch.MainTab != nil && *patch.MainTab {
		for id, other := range s.displaySettings {
			if id == collectionID || other.Owner != owner {
				continue
			}
			other.MainTab = false
			s.displaySettings[id] = other
		}
	}
	current.Owner = owner
	current.CollectionID = collectionID
	current.UpdatedAt = time.Now().UTC()
	s.displaySettings[collectionID] = cloneCollectionDisplaySettings(current)
	return cloneCollectionDisplaySettings(current), nil
}

func (s *StarGiftStore) collectionDisplaySettingsLocked(owner domain.Peer, collection domain.StarGiftCollection) domain.StarGiftCollectionDisplaySettings {
	stored, hasStored := s.displaySettings[collection.CollectionID]
	settings := domain.StarGiftCollectionDisplaySettings{
		Owner: owner, CollectionID: collection.CollectionID, Hidden: stored.Hidden,
		MainTab: stored.MainTab, FilterMask: stored.FilterMask, UpdatedAt: stored.UpdatedAt,
		Items: make([]domain.StarGiftCollectionDisplayItem, 0, len(collection.GiftIDs)),
	}
	sizes := make(map[int64]int, len(stored.Items))
	if hasStored {
		for _, item := range stored.Items {
			sizes[item.GiftID] = item.Size
		}
	}
	for order, giftID := range collection.GiftIDs {
		size := domain.StarGiftCollectionDisplaySizeCompact
		if value, ok := sizes[giftID]; ok {
			size = value
		}
		settings.Items = append(settings.Items, domain.StarGiftCollectionDisplayItem{GiftID: giftID, Order: order, Size: size})
	}
	return settings
}

func cloneCollectionDisplaySettings(in domain.StarGiftCollectionDisplaySettings) domain.StarGiftCollectionDisplaySettings {
	in.Items = append([]domain.StarGiftCollectionDisplayItem(nil), in.Items...)
	return in
}
