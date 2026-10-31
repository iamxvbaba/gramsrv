package memory

import (
	"context"
	"sort"
	"sync"

	"telesrv/internal/domain"
)

// ThemeStore is the in-memory implementation of store.ThemeStore (tests/single instance).
type ThemeStore struct {
	mu       sync.RWMutex
	byID     map[int64]domain.Theme
	bySlug   map[string]int64
	installs map[int64]map[int64]themeInstall // userID -> themeID -> install entry
	seq      int64                             // monotonic sequence, mimicking postgres installed_at ordering
}

type themeInstall struct {
	dark  bool
	order int64
}

// NewThemeStore creates an in-memory theme store.
func NewThemeStore() *ThemeStore {
	return &ThemeStore{
		byID:     make(map[int64]domain.Theme),
		bySlug:   make(map[string]int64),
		installs: make(map[int64]map[int64]themeInstall),
	}
}

func (s *ThemeStore) CreateTheme(_ context.Context, t domain.Theme) error {
	if t.ID == 0 || t.CreatorUserID == 0 {
		return domain.ErrThemeInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.byID[t.ID]; ok {
		return domain.ErrThemeInvalid
	}
	if t.Slug != "" {
		if _, ok := s.bySlug[t.Slug]; ok {
			return domain.ErrThemeSlugTaken
		}
	}
	s.byID[t.ID] = t.Clone()
	if t.Slug != "" {
		s.bySlug[t.Slug] = t.ID
	}
	return nil
}

func (s *ThemeStore) GetThemeByID(_ context.Context, id int64) (domain.Theme, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	t, ok := s.byID[id]
	if !ok {
		return domain.Theme{}, false, nil
	}
	return t.Clone(), true, nil
}

func (s *ThemeStore) GetThemeBySlug(_ context.Context, slug string) (domain.Theme, bool, error) {
	if slug == "" {
		return domain.Theme{}, false, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	id, ok := s.bySlug[slug]
	if !ok {
		return domain.Theme{}, false, nil
	}
	t, ok := s.byID[id]
	if !ok {
		return domain.Theme{}, false, nil
	}
	return t.Clone(), true, nil
}

func (s *ThemeStore) SlugExists(_ context.Context, slug string) (bool, error) {
	if slug == "" {
		return false, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.bySlug[slug]
	return ok, nil
}

func (s *ThemeStore) UpdateTheme(_ context.Context, t domain.Theme) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	prev, ok := s.byID[t.ID]
	if !ok {
		return domain.ErrThemeNotFound
	}
	if t.Slug != "" && t.Slug != prev.Slug {
		if _, taken := s.bySlug[t.Slug]; taken {
			return domain.ErrThemeSlugTaken
		}
	}
	if prev.Slug != "" && prev.Slug != t.Slug {
		delete(s.bySlug, prev.Slug)
	}
	s.byID[t.ID] = t.Clone()
	if t.Slug != "" {
		s.bySlug[t.Slug] = t.ID
	}
	return nil
}

func (s *ThemeStore) IncrementInstalls(_ context.Context, id int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.byID[id]
	if !ok {
		return domain.ErrThemeNotFound
	}
	t.InstallsCount++
	s.byID[id] = t
	return nil
}

func (s *ThemeStore) SetInstalled(_ context.Context, userID, themeID int64, dark bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.byID[themeID]; !ok {
		return domain.ErrThemeNotFound
	}
	byUser := s.installs[userID]
	if byUser == nil {
		byUser = make(map[int64]themeInstall)
		s.installs[userID] = byUser
	}
	if prev, ok := byUser[themeID]; ok {
		byUser[themeID] = themeInstall{dark: dark, order: prev.order}
		return nil
	}
	s.seq++
	byUser[themeID] = themeInstall{dark: dark, order: s.seq}
	return nil
}

func (s *ThemeStore) RemoveInstalled(_ context.Context, userID, themeID int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if byUser := s.installs[userID]; byUser != nil {
		delete(byUser, themeID)
	}
	return nil
}

func (s *ThemeStore) ListInstalledByUser(_ context.Context, userID int64) ([]domain.Theme, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	byUser := s.installs[userID]
	if len(byUser) == 0 {
		return nil, nil
	}
	type row struct {
		t     domain.Theme
		order int64
	}
	rows := make([]row, 0, len(byUser))
	for themeID, inst := range byUser {
		t, ok := s.byID[themeID]
		if !ok {
			continue
		}
		rows = append(rows, row{t: t.Clone(), order: inst.order})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].order < rows[j].order })
	out := make([]domain.Theme, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.t)
	}
	return out, nil
}

func (s *ThemeStore) ListThemesForUser(_ context.Context, userID int64) ([]domain.Theme, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	seen := make(map[int64]bool)
	out := make([]domain.Theme, 0)
	for _, t := range s.byID {
		if t.CreatorUserID == userID {
			out = append(out, t.Clone())
			seen[t.ID] = true
		}
	}
	if byUser := s.installs[userID]; byUser != nil {
		for themeID := range byUser {
			if seen[themeID] {
				continue
			}
			if t, ok := s.byID[themeID]; ok {
				out = append(out, t.Clone())
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt != out[j].CreatedAt {
			return out[i].CreatedAt < out[j].CreatedAt
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}
