package store

import (
	"context"

	"telesrv/internal/domain"
)

// LangPackStore 持久化客户端语言包。
type LangPackStore interface {
	GetPack(ctx context.Context, langPack, langCode string, fromVersion int) (domain.LangPack, error)
	GetStrings(ctx context.Context, langPack, langCode string, keys []string) (domain.LangPack, error)
	ListLanguages(ctx context.Context, langPack string) ([]domain.LangPackLanguage, error)
	GetSeedCatalog(ctx context.Context, catalog string) (domain.LangPackSeedCatalog, error)
	// PutSeedCatalog 只刷新目录清单快照（不触碰语言包内容），用于补充文件 stat 元数据。
	PutSeedCatalog(ctx context.Context, catalog domain.LangPackSeedCatalog) error
	ReconcileSeed(ctx context.Context, seed domain.LangPackSeed) (int, error)
	UpsertPack(ctx context.Context, pack domain.LangPack) error
}
