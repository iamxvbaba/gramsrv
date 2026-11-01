package memory

import (
	"context"
	"sync"
	"telesrv/internal/domain"
)

// HelpStore is the in-memory implementation of store.AppConfigStore and store.CountryStore.
type HelpStore struct {
	mu        sync.RWMutex
	appConfig map[string]domain.AppConfig
	countries domain.CountriesList
}

// NewHelpStore creates an in-memory HelpStore.
func NewHelpStore() *HelpStore {
	return &HelpStore{appConfig: make(map[string]domain.AppConfig)}
}

func (s *HelpStore) GetAppConfig(_ context.Context, client string) (domain.AppConfig, bool, error) {
	s.mu.RLock()
	cfg, ok := s.appConfig[client]
	s.mu.RUnlock()
	cfg.JSON = append([]byte(nil), cfg.JSON...)
	return cfg, ok, nil
}

func (s *HelpStore) UpsertAppConfig(_ context.Context, cfg domain.AppConfig) error {
	cfg.JSON = append([]byte(nil), cfg.JSON...)
	s.mu.Lock()
	s.appConfig[cfg.Client] = cfg
	s.mu.Unlock()
	return nil
}

func (s *HelpStore) ListCountries(_ context.Context, _ string) (domain.CountriesList, error) {
	s.mu.RLock()
	list := s.countries
	s.mu.RUnlock()
	list.Countries = append([]domain.Country(nil), list.Countries...)
	for i := range list.Countries {
		list.Countries[i].CountryCodes = append([]domain.CountryCode(nil), list.Countries[i].CountryCodes...)
	}
	return list, nil
}

func (s *HelpStore) UpsertCountries(_ context.Context, countries []domain.Country) error {
	list := domain.CountriesList{Hash: 1, Countries: append([]domain.Country(nil), countries...)}
	for i := range list.Countries {
		list.Countries[i].CountryCodes = append([]domain.CountryCode(nil), list.Countries[i].CountryCodes...)
	}
	s.mu.Lock()
	s.countries = list
	s.mu.Unlock()
	return nil
}
