package langpack

import (
	"context"
	"sync"
	"testing"
	"time"

	"telesrv/internal/domain"
	"telesrv/internal/store"
	"telesrv/internal/store/memory"
)

func TestServiceNormalizesWebARawLangCode(t *testing.T) {
	ctx := context.Background()
	packs := memory.NewLangPackStore()
	svc := NewService(packs)
	seed := domain.LangPack{
		LangPack: "android",
		LangCode: "en",
		Version:  7,
		Strings: []domain.LangPackString{
			{Key: "LogOutTitle", Value: "Log Out"},
			{Key: "NewMessageTitle", Value: "New Message"},
		},
	}
	if err := packs.UpsertPack(ctx, seed); err != nil {
		t.Fatalf("seed langpack: %v", err)
	}
	webASeed := domain.LangPack{
		LangPack: "weba",
		LangCode: "en",
		Version:  12,
		Strings: []domain.LangPackString{
			{Key: "AccDescrPollVoteDown", Value: "Go to next unread poll vote"},
			{Key: "NewMessageTitle", Value: "New Message from WebA"},
		},
	}
	if err := packs.UpsertPack(ctx, webASeed); err != nil {
		t.Fatalf("seed weba langpack: %v", err)
	}

	pack, err := svc.GetLangPack(ctx, "android", "EN-raw")
	if err != nil {
		t.Fatalf("get langpack: %v", err)
	}
	if pack.LangCode != "en" || pack.Version != webASeed.Version || len(pack.Strings) != len(seed.Strings)+1 {
		t.Fatalf("pack = %+v, want normalized en pack", pack)
	}
	if got := stringValue(pack.Strings, "AccDescrPollVoteDown"); got != "Go to next unread poll vote" {
		t.Fatalf("AccDescrPollVoteDown = %q, want WebA fallback", got)
	}
	if got := stringValue(pack.Strings, "NewMessageTitle"); got != "New Message" {
		t.Fatalf("NewMessageTitle = %q, want source pack to keep precedence", got)
	}

	selected, err := svc.GetStrings(ctx, "android", "en-raw", []string{"LogOutTitle", "AccDescrPollVoteDown"})
	if err != nil {
		t.Fatalf("get strings: %v", err)
	}
	if got := stringValue(selected.Strings, "LogOutTitle"); got != "Log Out" {
		t.Fatalf("LogOutTitle = %q, want source pack value", got)
	}
	if got := stringValue(selected.Strings, "AccDescrPollVoteDown"); got != "Go to next unread poll vote" {
		t.Fatalf("AccDescrPollVoteDown = %q, want WebA fallback", got)
	}

	notModified, err := svc.GetDifference(ctx, "android", "en-raw", seed.Version)
	if err != nil {
		t.Fatalf("get difference: %v", err)
	}
	if notModified.LangCode != "en" || len(notModified.Strings) != 0 {
		t.Fatalf("difference = %+v, want normalized not-modified source pack", notModified)
	}
}

func TestListLanguagesUsesSeededPacks(t *testing.T) {
	ctx := context.Background()
	packs := memory.NewLangPackStore()
	svc := NewService(packs)
	if err := packs.UpsertPack(ctx, domain.LangPack{
		LangPack: "tdesktop",
		LangCode: "fr",
		Version:  7,
		Strings: []domain.LangPackString{
			{Key: "lng_language_name", Value: "Français"},
			{Key: "lng_test", Value: "Test"},
		},
	}); err != nil {
		t.Fatalf("seed fr langpack: %v", err)
	}
	if err := packs.UpsertPack(ctx, domain.LangPack{
		LangPack: "android",
		LangCode: "fa",
		Version:  8,
		Strings: []domain.LangPackString{
			{Key: "LanguageName", Value: "انگلیسی"},
			{Key: "TranslateLanguageFA", Value: "فارسی"},
			{Key: "lng_test", Value: "Test"},
		},
	}); err != nil {
		t.Fatalf("seed fa langpack: %v", err)
	}
	if err := packs.UpsertPack(ctx, domain.LangPack{
		LangPack: "tdesktop",
		LangCode: "ckb",
		Version:  9,
		Strings:  []domain.LangPackString{{Key: "lng_language_name", Value: "کوردی"}},
	}); err != nil {
		t.Fatalf("seed ckb langpack: %v", err)
	}

	tdesktop, err := svc.ListLanguages(ctx, "tdesktop")
	if err != nil {
		t.Fatalf("list tdesktop languages: %v", err)
	}
	fr := findLanguage(tdesktop, "fr")
	if fr == nil || fr.Name != "Français" || fr.NativeName != "Français" || fr.PluralCode != "fr" || fr.StringsCount != 2 {
		t.Fatalf("fr language = %+v", fr)
	}
	ckb := findLanguage(tdesktop, "ckb")
	if ckb == nil || !ckb.Rtl {
		t.Fatalf("ckb language = %+v, want file-derived rtl", ckb)
	}

	android, err := svc.ListLanguages(ctx, "android")
	if err != nil {
		t.Fatalf("list android languages: %v", err)
	}
	fa := findLanguage(android, "fa")
	if fa == nil || fa.NativeName != "فارسی" || !fa.Rtl || fa.PluralCode != "fa" {
		t.Fatalf("fa language = %+v", fa)
	}
}

func TestServiceCachesLanguageResourcesAfterFirstRequest(t *testing.T) {
	ctx := context.Background()
	base := memory.NewLangPackStore()
	for _, pack := range []domain.LangPack{
		{
			LangPack: "android",
			LangCode: "en",
			Version:  7,
			Strings: []domain.LangPackString{
				{Key: "LogOutTitle", Value: "Log Out"},
				{Key: "NewMessageTitle", Value: "New Message"},
			},
		},
		{
			LangPack: "weba",
			LangCode: "en",
			Version:  12,
			Strings: []domain.LangPackString{
				{Key: "AccDescrPollVoteDown", Value: "Go to next unread poll vote"},
				{Key: "NewMessageTitle", Value: "New Message from WebA"},
			},
		},
	} {
		if err := base.UpsertPack(ctx, pack); err != nil {
			t.Fatalf("seed %s: %v", pack.LangPack, err)
		}
	}
	counting := &countingLangPackStore{LangPackStore: base}
	svc := NewService(counting)

	first, err := svc.GetLangPack(ctx, "android", "en")
	if err != nil {
		t.Fatalf("first get langpack: %v", err)
	}
	first.Strings[0].Value = "caller mutation"
	second, err := svc.GetLangPack(ctx, "android", "en")
	if err != nil {
		t.Fatalf("second get langpack: %v", err)
	}
	if got := stringValue(second.Strings, "LogOutTitle"); got != "Log Out" {
		t.Fatalf("cached pack was mutated through caller alias: %q", got)
	}
	selected, err := svc.GetStrings(ctx, "android", "en", []string{"AccDescrPollVoteDown"})
	if err != nil || stringValue(selected.Strings, "AccDescrPollVoteDown") == "" {
		t.Fatalf("cached get strings = %+v, %v", selected, err)
	}
	if _, err := svc.GetDifference(ctx, "android", "en", 1); err != nil {
		t.Fatalf("cached get difference: %v", err)
	}

	languages, err := svc.ListLanguages(ctx, "android")
	if err != nil || len(languages) != 1 {
		t.Fatalf("first list languages = %+v, %v", languages, err)
	}
	languages[0].Name = "caller mutation"
	languages, err = svc.ListLanguages(ctx, "android")
	if err != nil || len(languages) != 1 || languages[0].Name == "caller mutation" {
		t.Fatalf("cached languages alias = %+v, %v", languages, err)
	}

	getPack, getStrings, listLanguages := counting.counts()
	if getPack != 2 || getStrings != 0 || listLanguages != 1 {
		t.Fatalf("store calls = getPack:%d getStrings:%d list:%d, want 2/0/1", getPack, getStrings, listLanguages)
	}
}

func TestServiceCollapsesConcurrentLanguagePackLoads(t *testing.T) {
	ctx := context.Background()
	base := memory.NewLangPackStore()
	if err := base.UpsertPack(ctx, domain.LangPack{
		LangPack: "weba",
		LangCode: "en",
		Version:  3,
		Strings:  []domain.LangPackString{{Key: "NewMessageTitle", Value: "New Message"}},
	}); err != nil {
		t.Fatalf("seed weba: %v", err)
	}
	counting := &countingLangPackStore{LangPackStore: base, delay: 10 * time.Millisecond}
	svc := NewService(counting)
	start := make(chan struct{})
	errs := make(chan error, 32)
	var wg sync.WaitGroup
	for range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := svc.GetLangPack(ctx, "weba", "en")
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent get langpack: %v", err)
		}
	}
	getPack, _, _ := counting.counts()
	if getPack != 1 {
		t.Fatalf("concurrent store getPack calls = %d, want 1", getPack)
	}
}

func TestServiceLanguagePackCacheIsBounded(t *testing.T) {
	ctx := context.Background()
	base := memory.NewLangPackStore()
	for _, code := range []string{"en", "fr"} {
		if err := base.UpsertPack(ctx, domain.LangPack{
			LangPack: "weba",
			LangCode: code,
			Version:  1,
			Strings:  []domain.LangPackString{{Key: "key", Value: code}},
		}); err != nil {
			t.Fatalf("seed %s: %v", code, err)
		}
	}
	counting := &countingLangPackStore{LangPackStore: base}
	svc := newServiceWithCacheLimits(counting, 1<<20, 1, 1)
	for _, code := range []string{"en", "fr", "en"} {
		if _, err := svc.GetLangPack(ctx, "weba", code); err != nil {
			t.Fatalf("get %s: %v", code, err)
		}
	}
	getPack, _, _ := counting.counts()
	if getPack != 3 {
		t.Fatalf("LRU store getPack calls = %d, want 3", getPack)
	}

	oversized := &countingLangPackStore{LangPackStore: base}
	svc = newServiceWithCacheLimits(oversized, 1, 8, 1)
	if _, err := svc.GetLangPack(ctx, "weba", "en"); err != nil {
		t.Fatalf("first oversized get: %v", err)
	}
	if _, err := svc.GetLangPack(ctx, "weba", "en"); err != nil {
		t.Fatalf("second oversized get: %v", err)
	}
	getPack, _, _ = oversized.counts()
	if getPack != 2 {
		t.Fatalf("oversized store getPack calls = %d, want 2", getPack)
	}
}

type countingLangPackStore struct {
	store.LangPackStore
	mu            sync.Mutex
	delay         time.Duration
	getPack       int
	getStrings    int
	listLanguages int
}

func (s *countingLangPackStore) GetPack(ctx context.Context, langPack, langCode string, fromVersion int) (domain.LangPack, error) {
	s.mu.Lock()
	s.getPack++
	delay := s.delay
	s.mu.Unlock()
	if delay > 0 {
		time.Sleep(delay)
	}
	return s.LangPackStore.GetPack(ctx, langPack, langCode, fromVersion)
}

func (s *countingLangPackStore) GetStrings(ctx context.Context, langPack, langCode string, keys []string) (domain.LangPack, error) {
	s.mu.Lock()
	s.getStrings++
	s.mu.Unlock()
	return s.LangPackStore.GetStrings(ctx, langPack, langCode, keys)
}

func (s *countingLangPackStore) ListLanguages(ctx context.Context, langPack string) ([]domain.LangPackLanguage, error) {
	s.mu.Lock()
	s.listLanguages++
	s.mu.Unlock()
	return s.LangPackStore.ListLanguages(ctx, langPack)
}

func (s *countingLangPackStore) counts() (getPack, getStrings, listLanguages int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.getPack, s.getStrings, s.listLanguages
}

func findLanguage(languages []domain.LangPackLanguage, code string) *domain.LangPackLanguage {
	for i := range languages {
		if languages[i].LangCode == code {
			return &languages[i]
		}
	}
	return nil
}

func stringValue(strings []domain.LangPackString, key string) string {
	for _, item := range strings {
		if item.Key == key {
			return item.Value
		}
	}
	return ""
}
