package langpack

import (
	"context"
	"testing"

	"telesrv/internal/domain"
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

func stringValue(strings []domain.LangPackString, key string) string {
	for _, item := range strings {
		if item.Key == key {
			return item.Value
		}
	}
	return ""
}
