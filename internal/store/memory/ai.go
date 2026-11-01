package memory

import (
	"context"
	"sort"
	"sync"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

// AIComposeStore is the in-memory implementation of store.AIComposeStore.
type AIComposeStore struct {
	mu     sync.RWMutex
	byID   map[int64]domain.AIComposeTone
	bySlug map[string]int64
	saves  map[int64]map[int64]int64 // userID -> toneID -> order
	seq    int64
	outbox *DeliveryOutboxStore
}

func NewAIComposeStore() *AIComposeStore {
	return &AIComposeStore{
		byID:   make(map[int64]domain.AIComposeTone),
		bySlug: make(map[string]int64),
		saves:  make(map[int64]map[int64]int64),
		outbox: NewDeliveryOutboxStore(),
	}
}

func (s *AIComposeStore) DeliveryOutbox() *DeliveryOutboxStore { return s.outbox }

func (s *AIComposeStore) CreateAIComposeTone(ctx context.Context, tone domain.AIComposeTone, effects store.DeliveryEffectsBuilder[domain.AIComposeTone]) error {
	if tone.ID == 0 || tone.AccessHash == 0 || tone.OwnerUserID == 0 || tone.Slug == "" {
		return domain.ErrAIComposeToneInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.byID[tone.ID]; ok {
		return domain.ErrAIComposeToneInvalid
	}
	if _, ok := s.bySlug[tone.Slug]; ok {
		return domain.ErrAIComposeToneInvalid
	}
	s.byID[tone.ID] = tone.Clone()
	s.bySlug[tone.Slug] = tone.ID
	if err := s.applyToneEffectsLocked(ctx, tone, effects); err != nil {
		delete(s.byID, tone.ID)
		delete(s.bySlug, tone.Slug)
		return err
	}
	return nil
}

func (s *AIComposeStore) UpdateAIComposeTone(ctx context.Context, tone domain.AIComposeTone, effects store.DeliveryEffectsBuilder[domain.AIComposeTone]) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	prev, ok := s.byID[tone.ID]
	if !ok {
		return domain.ErrAIComposeToneNotFound
	}
	if prev.OwnerUserID != tone.OwnerUserID {
		return domain.ErrAIComposeToneInvalid
	}
	if tone.Slug != prev.Slug {
		if _, taken := s.bySlug[tone.Slug]; taken {
			return domain.ErrAIComposeToneInvalid
		}
		delete(s.bySlug, prev.Slug)
		s.bySlug[tone.Slug] = tone.ID
	}
	s.byID[tone.ID] = tone.Clone()
	if err := s.applyToneEffectsLocked(ctx, tone, effects); err != nil {
		s.byID[tone.ID] = prev
		if tone.Slug != prev.Slug {
			delete(s.bySlug, tone.Slug)
			s.bySlug[prev.Slug] = tone.ID
		}
		return err
	}
	return nil
}

func (s *AIComposeStore) DeleteAIComposeTone(ctx context.Context, ownerUserID, toneID int64, effects store.DeliveryEffectsBuilder[domain.AIComposeTone]) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tone, ok := s.byID[toneID]
	if !ok {
		return domain.ErrAIComposeToneNotFound
	}
	if tone.OwnerUserID != ownerUserID {
		return domain.ErrAIComposeToneInvalid
	}
	delete(s.byID, toneID)
	delete(s.bySlug, tone.Slug)
	removedSaves := make(map[int64]int64)
	for userID := range s.saves {
		if order, found := s.saves[userID][toneID]; found {
			removedSaves[userID] = order
			delete(s.saves[userID], toneID)
		}
	}
	if err := s.applyToneEffectsLocked(ctx, tone, effects); err != nil {
		s.byID[toneID] = tone
		s.bySlug[tone.Slug] = toneID
		for userID, order := range removedSaves {
			if s.saves[userID] == nil {
				s.saves[userID] = make(map[int64]int64)
			}
			s.saves[userID][toneID] = order
		}
		return err
	}
	return nil
}

func (s *AIComposeStore) GetAIComposeToneByID(_ context.Context, id, accessHash int64) (domain.AIComposeTone, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	tone, ok := s.byID[id]
	if !ok || tone.AccessHash != accessHash {
		return domain.AIComposeTone{}, false, nil
	}
	return tone.Clone(), true, nil
}

func (s *AIComposeStore) GetAIComposeToneBySlug(_ context.Context, slug string) (domain.AIComposeTone, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	id, ok := s.bySlug[slug]
	if !ok {
		return domain.AIComposeTone{}, false, nil
	}
	tone, ok := s.byID[id]
	if !ok {
		return domain.AIComposeTone{}, false, nil
	}
	return tone.Clone(), true, nil
}

func (s *AIComposeStore) ListAIComposeTonesForUser(_ context.Context, userID int64) ([]domain.AIComposeTone, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	seen := make(map[int64]bool)
	out := make([]domain.AIComposeTone, 0)
	for _, tone := range s.byID {
		if tone.OwnerUserID != userID {
			continue
		}
		item := tone.Clone()
		item.Creator = true
		item.Saved = true
		out = append(out, item)
		seen[item.ID] = true
	}
	if saved := s.saves[userID]; len(saved) > 0 {
		type row struct {
			tone  domain.AIComposeTone
			order int64
		}
		rows := make([]row, 0, len(saved))
		for id, order := range saved {
			if seen[id] {
				continue
			}
			tone, ok := s.byID[id]
			if !ok {
				continue
			}
			tone = tone.Clone()
			tone.Creator = false
			tone.Saved = true
			rows = append(rows, row{tone: tone, order: order})
		}
		sort.Slice(rows, func(i, j int) bool { return rows[i].order < rows[j].order })
		for _, row := range rows {
			out = append(out, row.tone)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Creator != out[j].Creator {
			return out[i].Creator
		}
		if out[i].UpdatedAt != out[j].UpdatedAt {
			return out[i].UpdatedAt > out[j].UpdatedAt
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

func (s *AIComposeStore) SaveAIComposeTone(ctx context.Context, userID, toneID int64, effects store.DeliveryEffectsBuilder[domain.AIComposeTone]) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tone, ok := s.byID[toneID]
	if !ok {
		return domain.ErrAIComposeToneNotFound
	}
	if tone.OwnerUserID == userID {
		return nil
	}
	byUser := s.saves[userID]
	if byUser == nil {
		byUser = make(map[int64]int64)
		s.saves[userID] = byUser
	}
	if _, ok := byUser[toneID]; ok {
		return nil
	}
	previousSeq := s.seq
	s.seq++
	byUser[toneID] = s.seq
	tone.InstallsCount++
	s.byID[toneID] = tone
	if err := s.applyToneEffectsLocked(ctx, tone, effects); err != nil {
		delete(byUser, toneID)
		if len(byUser) == 0 {
			delete(s.saves, userID)
		}
		tone.InstallsCount--
		s.byID[toneID] = tone
		s.seq = previousSeq
		return err
	}
	return nil
}

func (s *AIComposeStore) UnsaveAIComposeTone(ctx context.Context, userID, toneID int64, effects store.DeliveryEffectsBuilder[domain.AIComposeTone]) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tone, ok := s.byID[toneID]
	if !ok {
		return domain.ErrAIComposeToneNotFound
	}
	byUser := s.saves[userID]
	if byUser == nil {
		return nil
	}
	order, found := byUser[toneID]
	if !found {
		return nil
	}
	delete(byUser, toneID)
	if err := s.applyToneEffectsLocked(ctx, tone, effects); err != nil {
		byUser[toneID] = order
		return err
	}
	return nil
}

func (s *AIComposeStore) applyToneEffectsLocked(ctx context.Context, tone domain.AIComposeTone, build store.DeliveryEffectsBuilder[domain.AIComposeTone]) error {
	if build == nil {
		return store.ErrDeliveryOutboxRequired
	}
	effects, err := build(tone.Clone())
	if err != nil {
		return err
	}
	if len(effects) == 0 {
		return store.ErrDeliveryOutboxRequired
	}
	_, err = applyDeliveryEffects(ctx, effects, s.outbox, nil)
	return err
}

func (s *AIComposeStore) SavedAIComposeToneCount(_ context.Context, userID int64) (int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	seen := make(map[int64]bool)
	for _, tone := range s.byID {
		if tone.OwnerUserID == userID {
			seen[tone.ID] = true
		}
	}
	for toneID := range s.saves[userID] {
		if _, ok := s.byID[toneID]; ok {
			seen[toneID] = true
		}
	}
	return len(seen), nil
}
