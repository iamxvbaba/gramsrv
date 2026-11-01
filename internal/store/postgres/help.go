package postgres

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"

	"github.com/jackc/pgx/v5"

	"telesrv/internal/domain"
	"telesrv/internal/store/postgres/sqlcgen"
)

// HelpStore implements store.AppConfigStore and store.CountryStore with PostgreSQL.
type HelpStore struct {
	db sqlcgen.DBTX
	q  *sqlcgen.Queries
}

// NewHelpStore creates a HelpStore backed by a pgx pool (or transaction).
func NewHelpStore(db sqlcgen.DBTX) *HelpStore {
	return &HelpStore{db: db, q: sqlcgen.New(db)}
}

func (s *HelpStore) GetAppConfig(ctx context.Context, client string) (domain.AppConfig, bool, error) {
	row, err := s.q.GetAppConfig(ctx, client)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.AppConfig{Client: client, JSON: []byte("{}")}, false, nil
		}
		return domain.AppConfig{}, false, fmt.Errorf("get app config: %w", err)
	}
	return domain.AppConfig{
		Client: row.Client,
		Hash:   int(row.Hash),
		JSON:   []byte(row.ConfigJson),
	}, true, nil
}

func (s *HelpStore) UpsertAppConfig(ctx context.Context, cfg domain.AppConfig) error {
	if len(cfg.JSON) == 0 {
		cfg.JSON = []byte("{}")
	}
	if err := s.q.UpsertAppConfig(ctx, sqlcgen.UpsertAppConfigParams{
		Client:     cfg.Client,
		Hash:       int32(cfg.Hash),
		ConfigJson: cfg.JSON,
	}); err != nil {
		return fmt.Errorf("upsert app config: %w", err)
	}
	return nil
}

func (s *HelpStore) ListCountries(ctx context.Context, _ string) (domain.CountriesList, error) {
	rows, err := s.q.ListCountries(ctx)
	if err != nil {
		return domain.CountriesList{}, fmt.Errorf("list countries: %w", err)
	}
	byISO := make(map[string]int)
	out := domain.CountriesList{}
	for _, row := range rows {
		idx, ok := byISO[row.Iso2]
		if !ok {
			idx = len(out.Countries)
			byISO[row.Iso2] = idx
			out.Countries = append(out.Countries, domain.Country{
				ISO2:        row.Iso2,
				DefaultName: row.DefaultName,
				Name:        row.Name,
				Hidden:      row.Hidden,
			})
		}
		out.Countries[idx].CountryCodes = append(out.Countries[idx].CountryCodes, domain.CountryCode{
			CountryCode: row.CountryCode,
			Prefixes:    append([]string(nil), row.Prefixes...),
			Patterns:    append([]string(nil), row.Patterns...),
		})
	}
	out.Hash = countriesHash(out.Countries)
	return out, nil
}

// Write the whole catalog in one go. Previously each country/number had its own auto-committed
// statement (~600 round trips + fsync), costing 15 seconds at startup; here jsonb_to_recordset
// merges all rows into two statements.
const (
	upsertCountriesSQL = `
INSERT INTO countries (iso2, default_name, name, hidden, order_index)
SELECT iso2, default_name, name, hidden, order_index
FROM jsonb_to_recordset($1::jsonb) AS x(
    iso2 text, default_name text, name text, hidden boolean, order_index integer)
ON CONFLICT (iso2) DO UPDATE SET
  default_name = EXCLUDED.default_name,
  name = EXCLUDED.name,
  hidden = EXCLUDED.hidden,
  order_index = EXCLUDED.order_index,
  updated_at = now()`

	upsertCountryCodesSQL = `
INSERT INTO country_codes (iso2, country_code, prefixes, patterns, order_index)
SELECT iso2, country_code,
       ARRAY(SELECT jsonb_array_elements_text(prefixes)),
       ARRAY(SELECT jsonb_array_elements_text(patterns)),
       order_index
FROM jsonb_to_recordset($1::jsonb) AS x(
    iso2 text, country_code text, prefixes jsonb, patterns jsonb, order_index integer)
ON CONFLICT (iso2, country_code) DO UPDATE SET
  prefixes = EXCLUDED.prefixes,
  patterns = EXCLUDED.patterns,
  order_index = EXCLUDED.order_index`
)

type countrySeedRow struct {
	ISO2        string `json:"iso2"`
	DefaultName string `json:"default_name"`
	Name        string `json:"name"`
	Hidden      bool   `json:"hidden"`
	OrderIndex  int32  `json:"order_index"`
}

type countryCodeSeedRow struct {
	ISO2        string   `json:"iso2"`
	CountryCode string   `json:"country_code"`
	Prefixes    []string `json:"prefixes"`
	Patterns    []string `json:"patterns"`
	OrderIndex  int32    `json:"order_index"`
}

func (s *HelpStore) UpsertCountries(ctx context.Context, countries []domain.Country) error {
	if len(countries) == 0 {
		return nil
	}
	countryRows := make([]countrySeedRow, 0, len(countries))
	codeRows := make([]countryCodeSeedRow, 0, len(countries)*3)
	for i, country := range countries {
		countryRows = append(countryRows, countrySeedRow{
			ISO2:        country.ISO2,
			DefaultName: country.DefaultName,
			Name:        country.Name,
			Hidden:      country.Hidden,
			OrderIndex:  int32(i + 1),
		})
		for j, code := range country.CountryCodes {
			// The prefixes/patterns columns are NOT NULL: for a country with no prefix/pattern
			// (e.g. AD) the catalog value is nil, so it must be normalized to an empty array;
			// otherwise jsonb null comes back as NULL.
			prefixes := code.Prefixes
			if prefixes == nil {
				prefixes = []string{}
			}
			patterns := code.Patterns
			if patterns == nil {
				patterns = []string{}
			}
			codeRows = append(codeRows, countryCodeSeedRow{
				ISO2:        country.ISO2,
				CountryCode: code.CountryCode,
				Prefixes:    prefixes,
				Patterns:    patterns,
				OrderIndex:  int32(j + 1),
			})
		}
	}
	countriesJSON, err := json.Marshal(countryRows)
	if err != nil {
		return fmt.Errorf("marshal country seed rows: %w", err)
	}
	codesJSON, err := json.Marshal(codeRows)
	if err != nil {
		return fmt.Errorf("marshal country code seed rows: %w", err)
	}

	// Put the two statements in one transaction to avoid leaving half a catalog on failure.
	// An implementation without Begin (e.g. the passed handle is already a tx) executes directly.
	if beginner, ok := s.db.(interface {
		Begin(context.Context) (pgx.Tx, error)
	}); ok {
		tx, err := beginner.Begin(ctx)
		if err != nil {
			return fmt.Errorf("begin countries upsert: %w", err)
		}
		if err := upsertCountries(ctx, tx, countriesJSON, codesJSON); err != nil {
			_ = tx.Rollback(ctx)
			return err
		}
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("commit countries upsert: %w", err)
		}
		return nil
	}
	return upsertCountries(ctx, s.db, countriesJSON, codesJSON)
}

func upsertCountries(ctx context.Context, db sqlcgen.DBTX, countriesJSON, codesJSON []byte) error {
	if _, err := db.Exec(ctx, upsertCountriesSQL, countriesJSON); err != nil {
		return fmt.Errorf("upsert countries: %w", err)
	}
	if _, err := db.Exec(ctx, upsertCountryCodesSQL, codesJSON); err != nil {
		return fmt.Errorf("upsert country codes: %w", err)
	}
	return nil
}

func countriesHash(countries []domain.Country) int {
	if len(countries) == 0 {
		return 0
	}
	h := fnv.New32a()
	var buf [4]byte
	for _, country := range countries {
		_, _ = h.Write([]byte(country.ISO2))
		_, _ = h.Write([]byte(country.DefaultName))
		_, _ = h.Write([]byte(country.Name))
		if country.Hidden {
			buf[0] = 1
		} else {
			buf[0] = 0
		}
		_, _ = h.Write(buf[:1])
		for _, code := range country.CountryCodes {
			_, _ = h.Write([]byte(code.CountryCode))
			binary.LittleEndian.PutUint32(buf[:], uint32(len(code.Prefixes)))
			_, _ = h.Write(buf[:])
			for _, prefix := range code.Prefixes {
				_, _ = h.Write([]byte(prefix))
			}
			binary.LittleEndian.PutUint32(buf[:], uint32(len(code.Patterns)))
			_, _ = h.Write(buf[:])
			for _, pattern := range code.Patterns {
				_, _ = h.Write([]byte(pattern))
			}
		}
	}
	return int(h.Sum32())
}
