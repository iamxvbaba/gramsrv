package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"telesrv/internal/domain"
	"telesrv/internal/store/postgres/sqlcgen"
)

// LangPackStore 用 PostgreSQL 实现 store.LangPackStore。
type LangPackStore struct {
	db sqlcgen.DBTX
	q  *sqlcgen.Queries
}

// NewLangPackStore 基于 pgx 连接池（或事务）创建 LangPackStore。
func NewLangPackStore(db sqlcgen.DBTX) *LangPackStore {
	return &LangPackStore{db: db, q: sqlcgen.New(db)}
}

func (s *LangPackStore) GetPack(ctx context.Context, langPack, langCode string, fromVersion int) (domain.LangPack, error) {
	meta, found, err := s.meta(ctx, langPack, langCode)
	if err != nil || !found {
		return meta, err
	}
	meta.FromVersion = fromVersion
	if meta.Version <= fromVersion {
		return meta, nil
	}
	rows, err := s.q.ListLangPackStrings(ctx, sqlcgen.ListLangPackStringsParams{
		LangPack: langPack,
		LangCode: langCode,
	})
	if err != nil {
		return domain.LangPack{}, fmt.Errorf("list lang pack strings: %w", err)
	}
	meta.Strings = make([]domain.LangPackString, 0, len(rows))
	for _, row := range rows {
		meta.Strings = append(meta.Strings, langPackStringFromListRow(row))
	}
	return meta, nil
}

func (s *LangPackStore) GetStrings(ctx context.Context, langPack, langCode string, keys []string) (domain.LangPack, error) {
	meta, found, err := s.meta(ctx, langPack, langCode)
	if err != nil || !found {
		return meta, err
	}
	if len(keys) == 0 {
		return s.GetPack(ctx, langPack, langCode, 0)
	}
	rows, err := s.q.GetLangPackStringsByKeys(ctx, sqlcgen.GetLangPackStringsByKeysParams{
		LangPack: langPack,
		LangCode: langCode,
		Keys:     keys,
	})
	if err != nil {
		return domain.LangPack{}, fmt.Errorf("get lang pack strings: %w", err)
	}
	meta.Strings = make([]domain.LangPackString, 0, len(rows))
	for _, row := range rows {
		meta.Strings = append(meta.Strings, langPackStringFromKeysRow(row))
	}
	return meta, nil
}

func (s *LangPackStore) ListLanguages(ctx context.Context, langPack string) ([]domain.LangPackLanguage, error) {
	rows, err := s.q.ListLangPackLanguages(ctx, langPack)
	if err != nil {
		return nil, fmt.Errorf("list lang pack languages: %w", err)
	}
	out := make([]domain.LangPackLanguage, 0, len(rows))
	for _, row := range rows {
		out = append(out, domain.LangPackLanguage{
			LangPack:        row.LangPack,
			LangCode:        row.LangCode,
			Name:            row.Name,
			NativeName:      row.NativeName,
			StringsCount:    int(row.StringsCount),
			TranslatedCount: int(row.StringsCount),
		})
	}
	return out, nil
}

func (s *LangPackStore) GetSeedCatalog(ctx context.Context, catalog string) (domain.LangPackSeedCatalog, error) {
	if catalog == "" {
		catalog = "default"
	}
	encoded, err := s.q.GetLangPackSeedHash(ctx, langPackSeedManifestStateKey(catalog))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.LangPackSeedCatalog{Catalog: catalog}, nil
	}
	if err != nil {
		return domain.LangPackSeedCatalog{}, fmt.Errorf("get langpack seed catalog %q: %w", catalog, err)
	}
	var state domain.LangPackSeedCatalog
	if err := json.Unmarshal([]byte(encoded), &state); err != nil {
		return domain.LangPackSeedCatalog{}, fmt.Errorf("decode langpack seed catalog %q: %w", catalog, err)
	}
	if state.Catalog != catalog {
		return domain.LangPackSeedCatalog{}, fmt.Errorf("langpack seed catalog key %q contains catalog %q", catalog, state.Catalog)
	}
	return state, nil
}

func (s *LangPackStore) ReconcileSeed(ctx context.Context, seed domain.LangPackSeed) (int, error) {
	txer, ok := s.db.(interface {
		Begin(context.Context) (pgx.Tx, error)
	})
	if !ok {
		return 0, errors.New("langpack seed reconciliation requires transaction support")
	}
	tx, err := txer.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin langpack seed reconciliation: %w", err)
	}
	written, err := reconcileSeedWith(ctx, tx, s.q.WithTx(tx), seed)
	if err != nil {
		_ = tx.Rollback(ctx)
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit langpack seed reconciliation: %w", err)
	}
	return written, nil
}

func (s *LangPackStore) UpsertPack(ctx context.Context, pack domain.LangPack) error {
	if txer, ok := s.db.(interface {
		Begin(context.Context) (pgx.Tx, error)
	}); ok {
		tx, err := txer.Begin(ctx)
		if err != nil {
			return fmt.Errorf("begin lang pack upsert: %w", err)
		}
		q := s.q.WithTx(tx)
		if err := replacePackWithCopy(ctx, tx, q, pack); err != nil {
			_ = tx.Rollback(ctx)
			return err
		}
		if err := q.DeleteLangPackSeedHash(ctx, langPackSeedStateKey(pack.LangPack, pack.LangCode)); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("delete lang pack seed hash: %w", err)
		}
		if err := invalidateLangPackSeedCatalogs(ctx, tx); err != nil {
			_ = tx.Rollback(ctx)
			return err
		}
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("commit lang pack upsert: %w", err)
		}
		return nil
	}
	if err := replacePackWithQueries(ctx, s.q, pack); err != nil {
		return err
	}
	if err := s.q.DeleteLangPackSeedHash(ctx, langPackSeedStateKey(pack.LangPack, pack.LangCode)); err != nil {
		return fmt.Errorf("delete lang pack seed hash: %w", err)
	}
	if err := invalidateLangPackSeedCatalogs(ctx, s.db); err != nil {
		return err
	}
	return nil
}

func reconcileSeedWith(ctx context.Context, tx pgx.Tx, q *sqlcgen.Queries, seed domain.LangPackSeed) (int, error) {
	catalog := seed.Catalog
	if catalog == "" {
		catalog = "default"
	}
	reconciledScopes := append([]string(nil), seed.Scopes...)
	scopes := make(map[string]struct{}, len(seed.Scopes))
	wanted := make(map[string]map[string]struct{}, len(seed.Scopes))
	for _, scope := range seed.Scopes {
		scopes[scope] = struct{}{}
		wanted[scope] = make(map[string]struct{})
	}
	previousScopesJSON, err := q.GetLangPackSeedHash(ctx, langPackSeedCatalogStateKey(catalog))
	if err == nil {
		var previousScopes []string
		if err := json.Unmarshal([]byte(previousScopesJSON), &previousScopes); err != nil {
			return 0, fmt.Errorf("decode previous langpack seed scopes for %q: %w", catalog, err)
		}
		for _, scope := range previousScopes {
			if _, exists := scopes[scope]; exists {
				continue
			}
			scopes[scope] = struct{}{}
			wanted[scope] = make(map[string]struct{})
			reconciledScopes = append(reconciledScopes, scope)
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return 0, fmt.Errorf("get previous langpack seed scopes for %q: %w", catalog, err)
	}
	for _, entry := range seed.Packs {
		if err := validateSeedEntry(entry); err != nil {
			return 0, err
		}
		if _, ok := scopes[entry.Pack.LangPack]; !ok {
			return 0, fmt.Errorf("seeded langpack %s/%s is outside reconciliation scopes", entry.Pack.LangPack, entry.Pack.LangCode)
		}
		if _, exists := wanted[entry.Pack.LangPack][entry.Pack.LangCode]; exists {
			return 0, fmt.Errorf("duplicate seeded langpack %s/%s", entry.Pack.LangPack, entry.Pack.LangCode)
		}
		wanted[entry.Pack.LangPack][entry.Pack.LangCode] = struct{}{}
	}

	for _, scope := range reconciledScopes {
		codes, err := q.ListLangPackCodes(ctx, scope)
		if err != nil {
			return 0, fmt.Errorf("list existing langpack codes for %q: %w", scope, err)
		}
		for _, code := range codes {
			if _, keep := wanted[scope][code]; keep {
				continue
			}
			params := sqlcgen.DeleteLangPackStringsParams{LangPack: scope, LangCode: code}
			if err := q.DeleteLangPackStrings(ctx, params); err != nil {
				return 0, fmt.Errorf("delete removed langpack strings %s/%s: %w", scope, code, err)
			}
			if err := q.DeleteLangPackMeta(ctx, sqlcgen.DeleteLangPackMetaParams(params)); err != nil {
				return 0, fmt.Errorf("delete removed langpack metadata %s/%s: %w", scope, code, err)
			}
			if err := q.DeleteLangPackSeedHash(ctx, langPackSeedStateKey(scope, code)); err != nil {
				return 0, fmt.Errorf("delete removed langpack seed hash %s/%s: %w", scope, code, err)
			}
		}
	}

	written := 0
	for _, entry := range seed.Packs {
		pack := entry.Pack
		meta, metaErr := q.GetLangPackMeta(ctx, sqlcgen.GetLangPackMetaParams{
			LangPack: pack.LangPack,
			LangCode: pack.LangCode,
		})
		metaFound := metaErr == nil
		if metaErr != nil && !errors.Is(metaErr, pgx.ErrNoRows) {
			return 0, fmt.Errorf("get existing langpack metadata %s/%s: %w", pack.LangPack, pack.LangCode, metaErr)
		}
		if metaFound && pack.Version < int(meta.Version) {
			return 0, fmt.Errorf("langpack version rollback %s/%s: %d < %d", pack.LangPack, pack.LangCode, pack.Version, meta.Version)
		}

		stateKey := langPackSeedStateKey(pack.LangPack, pack.LangCode)
		oldHash, hashErr := q.GetLangPackSeedHash(ctx, stateKey)
		hashFound := hashErr == nil
		if hashErr != nil && !errors.Is(hashErr, pgx.ErrNoRows) {
			return 0, fmt.Errorf("get langpack seed hash %s/%s: %w", pack.LangPack, pack.LangCode, hashErr)
		}
		if metaFound && hashFound && pack.Version == int(meta.Version) && oldHash != entry.ContentHash {
			return 0, fmt.Errorf("langpack %s/%s v%d content changed without version bump", pack.LangPack, pack.LangCode, pack.Version)
		}
		if metaFound && hashFound && pack.Version == int(meta.Version) && int(meta.StringsCount) == entry.StringsCount && oldHash == entry.ContentHash {
			continue
		}
		if !entry.ContentLoaded {
			return 0, fmt.Errorf("langpack %s/%s content is required but was not loaded", pack.LangPack, pack.LangCode)
		}

		if err := replacePackWithCopy(ctx, tx, q, pack); err != nil {
			return 0, err
		}
		if err := q.PutLangPackSeedHash(ctx, sqlcgen.PutLangPackSeedHashParams{Key: stateKey, ContentHash: entry.ContentHash}); err != nil {
			return 0, fmt.Errorf("put langpack seed hash %s/%s: %w", pack.LangPack, pack.LangCode, err)
		}
		written += len(pack.Strings)
	}
	encodedScopes, err := json.Marshal(seed.Scopes)
	if err != nil {
		return 0, fmt.Errorf("encode langpack seed scopes for %q: %w", catalog, err)
	}
	if err := q.PutLangPackSeedHash(ctx, sqlcgen.PutLangPackSeedHashParams{
		Key:         langPackSeedCatalogStateKey(catalog),
		ContentHash: string(encodedScopes),
	}); err != nil {
		return 0, fmt.Errorf("put langpack seed scopes for %q: %w", catalog, err)
	}
	manifest := seedCatalogSnapshot(seed)
	encodedManifest, err := json.Marshal(manifest)
	if err != nil {
		return 0, fmt.Errorf("encode langpack seed catalog %q: %w", catalog, err)
	}
	if err := q.PutLangPackSeedHash(ctx, sqlcgen.PutLangPackSeedHashParams{
		Key:         langPackSeedManifestStateKey(catalog),
		ContentHash: string(encodedManifest),
	}); err != nil {
		return 0, fmt.Errorf("put langpack seed catalog %q: %w", catalog, err)
	}
	return written, nil
}

func replacePackWithCopy(ctx context.Context, tx pgx.Tx, q *sqlcgen.Queries, pack domain.LangPack) error {
	params := sqlcgen.DeleteLangPackStringsParams{LangPack: pack.LangPack, LangCode: pack.LangCode}
	if err := q.DeleteLangPackStrings(ctx, params); err != nil {
		return fmt.Errorf("delete previous lang pack strings: %w", err)
	}
	if err := q.UpsertLangPackMeta(ctx, sqlcgen.UpsertLangPackMetaParams{
		LangPack:     pack.LangPack,
		LangCode:     pack.LangCode,
		Version:      int32(pack.Version),
		StringsCount: int32(len(pack.Strings)),
	}); err != nil {
		return fmt.Errorf("upsert lang pack meta: %w", err)
	}
	if len(pack.Strings) == 0 {
		return nil
	}
	count, err := tx.CopyFrom(
		ctx,
		pgx.Identifier{"lang_pack_strings"},
		[]string{
			"lang_pack", "lang_code", "key", "version", "pluralized", "value",
			"zero_value", "one_value", "two_value", "few_value", "many_value", "other_value", "deleted",
		},
		pgx.CopyFromSlice(len(pack.Strings), func(i int) ([]any, error) {
			item := pack.Strings[i]
			return []any{
				pack.LangPack, pack.LangCode, item.Key, int32(pack.Version), item.Pluralized, item.Value,
				item.ZeroValue, item.OneValue, item.TwoValue, item.FewValue, item.ManyValue, item.OtherValue, item.Deleted,
			}, nil
		}),
	)
	if err != nil {
		return fmt.Errorf("copy lang pack strings: %w", err)
	}
	if count != int64(len(pack.Strings)) {
		return fmt.Errorf("copy lang pack strings wrote %d rows, want %d", count, len(pack.Strings))
	}
	return nil
}

func replacePackWithQueries(ctx context.Context, q *sqlcgen.Queries, pack domain.LangPack) error {
	if err := q.DeleteLangPackStrings(ctx, sqlcgen.DeleteLangPackStringsParams{LangPack: pack.LangPack, LangCode: pack.LangCode}); err != nil {
		return fmt.Errorf("delete previous lang pack strings: %w", err)
	}
	if err := q.UpsertLangPackMeta(ctx, sqlcgen.UpsertLangPackMetaParams{
		LangPack:     pack.LangPack,
		LangCode:     pack.LangCode,
		Version:      int32(pack.Version),
		StringsCount: int32(len(pack.Strings)),
	}); err != nil {
		return fmt.Errorf("upsert lang pack meta: %w", err)
	}
	for _, item := range pack.Strings {
		if err := q.UpsertLangPackString(ctx, sqlcgen.UpsertLangPackStringParams{
			LangPack:   pack.LangPack,
			LangCode:   pack.LangCode,
			Key:        item.Key,
			Version:    int32(pack.Version),
			Pluralized: item.Pluralized,
			Value:      item.Value,
			ZeroValue:  item.ZeroValue,
			OneValue:   item.OneValue,
			TwoValue:   item.TwoValue,
			FewValue:   item.FewValue,
			ManyValue:  item.ManyValue,
			OtherValue: item.OtherValue,
			Deleted:    item.Deleted,
		}); err != nil {
			return fmt.Errorf("upsert lang pack string %q: %w", item.Key, err)
		}
	}
	return nil
}

func langPackSeedStateKey(langPack, langCode string) string {
	return "langpack:v1:entry:" + langPack + ":" + langCode
}

func langPackSeedCatalogStateKey(catalog string) string {
	return "langpack:v1:catalog:" + catalog
}

func langPackSeedManifestStateKey(catalog string) string {
	return "langpack:v2:catalog:" + catalog
}

func validateSeedEntry(entry domain.LangPackSeedEntry) error {
	if entry.SourceHash == "" || entry.ContentHash == "" || entry.StringsCount <= 0 {
		return fmt.Errorf("langpack %s/%s has incomplete seed metadata", entry.Pack.LangPack, entry.Pack.LangCode)
	}
	if entry.ContentLoaded && len(entry.Pack.Strings) != entry.StringsCount {
		return fmt.Errorf("langpack %s/%s loaded %d strings, want %d", entry.Pack.LangPack, entry.Pack.LangCode, len(entry.Pack.Strings), entry.StringsCount)
	}
	return nil
}

func seedCatalogSnapshot(seed domain.LangPackSeed) domain.LangPackSeedCatalog {
	state := domain.LangPackSeedCatalog{
		Catalog: seed.Catalog,
		Scopes:  append([]string(nil), seed.Scopes...),
		Packs:   make([]domain.LangPackSeedCatalogEntry, 0, len(seed.Packs)),
	}
	for _, entry := range seed.Packs {
		state.Packs = append(state.Packs, domain.LangPackSeedCatalogEntry{
			LangPack:     entry.Pack.LangPack,
			LangCode:     entry.Pack.LangCode,
			Version:      entry.Pack.Version,
			SourceHash:   entry.SourceHash,
			ContentHash:  entry.ContentHash,
			StringsCount: entry.StringsCount,
		})
	}
	return state
}

func invalidateLangPackSeedCatalogs(ctx context.Context, db sqlcgen.DBTX) error {
	if _, err := db.Exec(ctx, `DELETE FROM seed_states WHERE key LIKE 'langpack:v2:catalog:%'`); err != nil {
		return fmt.Errorf("invalidate langpack seed catalogs: %w", err)
	}
	return nil
}

func (s *LangPackStore) meta(ctx context.Context, langPack, langCode string) (domain.LangPack, bool, error) {
	row, err := s.q.GetLangPackMeta(ctx, sqlcgen.GetLangPackMetaParams{
		LangPack: langPack,
		LangCode: langCode,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.LangPack{LangPack: langPack, LangCode: langCode}, false, nil
		}
		return domain.LangPack{}, false, fmt.Errorf("get lang pack meta: %w", err)
	}
	return domain.LangPack{
		LangPack: row.LangPack,
		LangCode: row.LangCode,
		Version:  int(row.Version),
	}, true, nil
}

func langPackStringFromListRow(row sqlcgen.ListLangPackStringsRow) domain.LangPackString {
	return domain.LangPackString{
		Key:        row.Key,
		Value:      row.Value,
		Pluralized: row.Pluralized,
		ZeroValue:  row.ZeroValue,
		OneValue:   row.OneValue,
		TwoValue:   row.TwoValue,
		FewValue:   row.FewValue,
		ManyValue:  row.ManyValue,
		OtherValue: row.OtherValue,
		Deleted:    row.Deleted,
	}
}

func langPackStringFromKeysRow(row sqlcgen.GetLangPackStringsByKeysRow) domain.LangPackString {
	return domain.LangPackString{
		Key:        row.Key,
		Value:      row.Value,
		Pluralized: row.Pluralized,
		ZeroValue:  row.ZeroValue,
		OneValue:   row.OneValue,
		TwoValue:   row.TwoValue,
		FewValue:   row.FewValue,
		ManyValue:  row.ManyValue,
		OtherValue: row.OtherValue,
		Deleted:    row.Deleted,
	}
}
