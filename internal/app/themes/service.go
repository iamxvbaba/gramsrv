// Package themes implements the business orchestration of custom cloud themes
// (account.createTheme/updateTheme/getTheme/saveTheme/installTheme): allocating
// id/access_hash/slug, validating the creator, and maintaining each user's installed list.
// File upload (uploadTheme) is done directly by the rpc layer via the Files service, not
// through this service.
package themes

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"strings"
	"time"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

// Custom theme id/access_hash use crypto-random 63-bit values, deliberately distinct from
// the static catalog's reserved range (static emoji themes use 92e16/93e16), so install/
// get-by-id cannot stray into the static catalog.
const defaultSlugPrefix = "t-"

// slug alphabet: lowercase + digits, URL-safe.
const slugAlphabet = "abcdefghijklmnopqrstuvwxyz0123456789"

// Service provides custom cloud theme business logic.
type Service struct {
	store      store.ThemeStore
	now        func() time.Time
	slugPrefix string
}

// Option adjusts the theme service's options.
type Option func(*Service)

// WithClock injects the clock (for tests).
func WithClock(now func() time.Time) Option {
	return func(s *Service) {
		if now != nil {
			s.now = now
		}
	}
}

// WithSlugPrefix overrides the prefix of automatically allocated slugs.
func WithSlugPrefix(prefix string) Option {
	return func(s *Service) { s.slugPrefix = prefix }
}

// NewService creates the theme service.
func NewService(st store.ThemeStore, opts ...Option) *Service {
	s := &Service{store: st, now: time.Now, slugPrefix: defaultSlugPrefix}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

func (s *Service) ready() bool { return s != nil && s.store != nil }

// Create creates a new theme. When spec.Slug is empty it is allocated automatically; a slug conflict is retried automatically.
func (s *Service) Create(ctx context.Context, spec domain.ThemeSpec) (domain.Theme, error) {
	if !s.ready() {
		return domain.Theme{}, domain.ErrThemeInvalid
	}
	if spec.CreatorUserID == 0 {
		return domain.Theme{}, domain.ErrThemeInvalid
	}
	userSlug := strings.TrimSpace(spec.Slug)
	// Retry a bounded number of times (automatic slug / extremely unlikely id collision).
	for attempt := 0; attempt < 8; attempt++ {
		t := domain.Theme{
			ID:            randInt63(),
			AccessHash:    randInt63(),
			CreatorUserID: spec.CreatorUserID,
			Title:         spec.Title,
			Emoticon:      spec.Emoticon,
			ForChat:       spec.ForChat,
			DocumentID:    spec.DocumentID,
			Settings:      spec.Settings,
			InstallsCount: 0,
			CreatedAt:     s.now().Unix(),
		}
		if userSlug != "" {
			t.Slug = userSlug
		} else {
			t.Slug = s.slugPrefix + randSlug(12)
		}
		err := s.store.CreateTheme(ctx, t)
		switch {
		case err == nil:
			return t.Clone(), nil
		case err == domain.ErrThemeSlugTaken && userSlug != "":
			// The user-specified slug is already taken: do not retry, error out directly.
			return domain.Theme{}, domain.ErrThemeSlugTaken
		case err == domain.ErrThemeSlugTaken:
			continue // automatic slug/id collision: try another set
		default:
			return domain.Theme{}, err
		}
	}
	return domain.Theme{}, domain.ErrThemeSlugTaken
}

func (s *Service) resolve(ctx context.Context, ref domain.ThemeRef) (domain.Theme, bool, error) {
	if ref.ID != 0 {
		t, ok, err := s.store.GetThemeByID(ctx, ref.ID)
		if err != nil || !ok {
			return domain.Theme{}, ok, err
		}
		if t.AccessHash != ref.AccessHash {
			return domain.Theme{}, false, nil
		}
		return t, true, nil
	}
	if ref.Slug != "" {
		return s.store.GetThemeBySlug(ctx, ref.Slug)
	}
	return domain.Theme{}, false, nil
}

// Get resolves a theme by id or slug.
func (s *Service) Get(ctx context.Context, ref domain.ThemeRef) (domain.Theme, bool, error) {
	if !s.ready() {
		return domain.Theme{}, false, domain.ErrThemeInvalid
	}
	return s.resolve(ctx, ref)
}

// Update updates a theme (only the creator may edit); partial fields.
func (s *Service) Update(ctx context.Context, userID int64, ref domain.ThemeRef, upd domain.ThemeUpdate) (domain.Theme, error) {
	if !s.ready() {
		return domain.Theme{}, domain.ErrThemeInvalid
	}
	t, ok, err := s.resolve(ctx, ref)
	if err != nil {
		return domain.Theme{}, err
	}
	if !ok {
		return domain.Theme{}, domain.ErrThemeNotFound
	}
	if !t.IsCreator(userID) {
		return domain.Theme{}, domain.ErrThemeInvalid
	}
	if upd.Slug != nil {
		t.Slug = strings.TrimSpace(*upd.Slug)
	}
	if upd.Title != nil {
		t.Title = *upd.Title
	}
	if upd.DocumentID != nil {
		t.DocumentID = *upd.DocumentID
	}
	if upd.Settings != nil {
		t.Settings = *upd.Settings
	}
	if err := s.store.UpdateTheme(ctx, t); err != nil {
		return domain.Theme{}, err
	}
	return t.Clone(), nil
}

// Save adds a theme to the user's saved list (saveTheme, unsave=false).
func (s *Service) Save(ctx context.Context, userID int64, ref domain.ThemeRef) error {
	return s.membership(ctx, userID, ref, false, false)
}

// Unsave removes a theme from the user's list (saveTheme, unsave=true).
func (s *Service) Unsave(ctx context.Context, userID int64, ref domain.ThemeRef) error {
	if !s.ready() {
		return domain.ErrThemeInvalid
	}
	t, ok, err := s.resolve(ctx, ref)
	if err != nil {
		return err
	}
	if !ok {
		return domain.ErrThemeInvalid
	}
	return s.store.RemoveInstalled(ctx, userID, t.ID)
}

// Install applies a theme: increments the count by 1 and adds it to the user's installed list.
func (s *Service) Install(ctx context.Context, userID int64, ref domain.ThemeRef, dark bool) error {
	return s.membership(ctx, userID, ref, true, dark)
}

func (s *Service) membership(ctx context.Context, userID int64, ref domain.ThemeRef, increment, dark bool) error {
	if !s.ready() {
		return domain.ErrThemeInvalid
	}
	t, ok, err := s.resolve(ctx, ref)
	if err != nil {
		return err
	}
	if !ok {
		return domain.ErrThemeInvalid
	}
	if increment {
		if err := s.store.IncrementInstalls(ctx, t.ID); err != nil {
			return err
		}
	}
	return s.store.SetInstalled(ctx, userID, t.ID, dark)
}

// ListInstalled returns the user's installed/saved themes.
func (s *Service) ListInstalled(ctx context.Context, userID int64) ([]domain.Theme, error) {
	if !s.ready() {
		return nil, domain.ErrThemeInvalid
	}
	return s.store.ListInstalledByUser(ctx, userID)
}

// ListForUser returns the user's "created ∪ installed" themes, for getThemes cross-device sync.
func (s *Service) ListForUser(ctx context.Context, userID int64) ([]domain.Theme, error) {
	if !s.ready() {
		return nil, domain.ErrThemeInvalid
	}
	return s.store.ListThemesForUser(ctx, userID)
}

// randInt63 returns a crypto-random positive 63-bit integer.
func randInt63() int64 {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return int64(binary.BigEndian.Uint64(b[:]) & 0x7fffffffffffffff)
}

// randSlug returns a random slug fragment of n characters.
func randSlug(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	out := make([]byte, n)
	for i := range out {
		out[i] = slugAlphabet[int(b[i])%len(slugAlphabet)]
	}
	return string(out)
}
