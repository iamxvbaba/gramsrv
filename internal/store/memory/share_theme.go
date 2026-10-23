package memory

import (
	"context"
	"sort"
	"sync"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

// ShareThemeStore is the in-memory store for Layer 232 share themes.
// Backdrops are seeded by tests through SetBackdrops.
type ShareThemeStore struct {
	mu        sync.Mutex
	themes    map[domain.Peer]domain.ShareTheme
	backdrops []domain.ShareThemeBackdrop
}

func NewShareThemeStore() *ShareThemeStore {
	return &ShareThemeStore{themes: make(map[domain.Peer]domain.ShareTheme)}
}

var _ store.ShareThemeStore = (*ShareThemeStore)(nil)

// SetBackdrops replaces the backdrop catalog.
func (s *ShareThemeStore) SetBackdrops(backdrops []domain.ShareThemeBackdrop) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.backdrops = append([]domain.ShareThemeBackdrop(nil), backdrops...)
	sort.Slice(s.backdrops, func(i, j int) bool { return s.backdrops[i].BackdropID < s.backdrops[j].BackdropID })
}

func (s *ShareThemeStore) GetShareTheme(_ context.Context, peer domain.Peer) (domain.ShareTheme, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	theme, ok := s.themes[peer]
	if !ok {
		return domain.ShareTheme{}, false, nil
	}
	theme.Emojis = append([]domain.ShareThemeEmoji(nil), theme.Emojis...)
	return theme, true, nil
}

func (s *ShareThemeStore) SetShareTheme(_ context.Context, peer domain.Peer, theme *domain.ShareTheme) error {
	if peer.ID <= 0 || (peer.Type != domain.PeerTypeUser && peer.Type != domain.PeerTypeChannel) {
		return domain.ErrShareThemeInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if theme == nil {
		delete(s.themes, peer)
		return nil
	}
	if err := theme.Validate(); err != nil {
		return err
	}
	stored := *theme
	stored.Emojis = append([]domain.ShareThemeEmoji(nil), theme.Emojis...)
	s.themes[peer] = stored
	return nil
}

func (s *ShareThemeStore) ShareThemeBackdrops(context.Context) ([]domain.ShareThemeBackdrop, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]domain.ShareThemeBackdrop(nil), s.backdrops...), nil
}
