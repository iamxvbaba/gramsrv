package memory

import (
	"context"
	"sort"
	"sync"

	"telesrv/internal/domain"
)

// AIComposeStore 是 store.AIComposeStore 的内存实现。
type AIComposeStore struct {
	mu     sync.RWMutex
	byID   map[int64]domain.AIComposeTone
	bySlug map[string]int64
	saves  map[int64]map[int64]int64 // userID -> toneID -> order
	seq    int64
}

func NewAIComposeStore() *AIComposeStore {
	return &AIComposeStore{
		byID:   make(map[int64]domain.AIComposeTone),
		bySlug: make(map[string]int64),
		saves:  make(map[int64]map[int64]int64),
	}
}

func (s *AIComposeStore) CreateAIComposeTone(_ context.Context, tone domain.AIComposeTone) error {
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
	return nil
}

func (s *AIComposeStore) UpdateAIComposeTone(_ context.Context, tone domain.AIComposeTone) error {
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
	return nil
}

func (s *AIComposeStore) DeleteAIComposeTone(_ context.Context, ownerUserID, toneID int64) error {
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
	for userID := range s.saves {
		delete(s.saves[userID], toneID)
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

func (s *AIComposeStore) SaveAIComposeTone(_ context.Context, userID, toneID int64) error {
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
	s.seq++
	byUser[toneID] = s.seq
	tone.InstallsCount++
	s.byID[toneID] = tone
	return nil
}

func (s *AIComposeStore) UnsaveAIComposeTone(_ context.Context, userID, toneID int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if byUser := s.saves[userID]; byUser != nil {
		delete(byUser, toneID)
	}
	return nil
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
