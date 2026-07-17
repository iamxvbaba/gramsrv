package langpack

import (
	"testing"

	"telesrv/internal/domain"
)

func TestLangPackCachesRejectLoadsAcrossFlush(t *testing.T) {
	packCache := newLangPackCache(1<<20, 8)
	key := langPackCacheKey{pack: "tdesktop", code: "en", kind: langPackCacheRaw}
	packEpoch := packCache.loadEpoch()
	packCache.flush()
	if packCache.putIfEpoch(key, domain.LangPack{
		LangPack: "tdesktop",
		LangCode: "en",
		Version:  1,
		Strings:  []domain.LangPackString{{Key: "key", Value: "stale"}},
	}, packEpoch) {
		t.Fatal("pre-flush pack load was accepted")
	}
	if _, ok := packCache.get(key); ok {
		t.Fatal("pre-flush pack load became visible")
	}

	languageCache := newLanguageListCache(8)
	languageEpoch := languageCache.loadEpoch()
	languageCache.flush()
	if languageCache.putIfEpoch("tdesktop", []domain.LangPackLanguage{{LangCode: "en"}}, languageEpoch) {
		t.Fatal("pre-flush language-list load was accepted")
	}
	if _, ok := languageCache.get("tdesktop"); ok {
		t.Fatal("pre-flush language-list load became visible")
	}
}
