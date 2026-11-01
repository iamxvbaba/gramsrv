package store

import (
	"context"

	"telesrv/internal/domain"
)

// AppConfigStore persists help.getAppConfig data.
type AppConfigStore interface {
	GetAppConfig(ctx context.Context, client string) (domain.AppConfig, bool, error)
	UpsertAppConfig(ctx context.Context, cfg domain.AppConfig) error
}

// CountryStore persists help.getCountriesList data.
type CountryStore interface {
	ListCountries(ctx context.Context, langCode string) (domain.CountriesList, error)
	UpsertCountries(ctx context.Context, countries []domain.Country) error
}
