package help

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"telesrv/internal/domain"
)

func TestAccountAppConfigFreezeOverlayIsUserScopedAndHashAware(t *testing.T) {
	since := time.Date(2026, 7, 15, 1, 2, 3, 0, time.UTC)
	until := since.Add(7 * 24 * time.Hour)
	provider := &fakeAccountFreezeProvider{items: map[int64]domain.AccountFreeze{
		1001: {UserID: 1001, Frozen: true, Since: since, Until: until, AppealURL: "https://appeals.example.test/1001"},
	}}
	svc := NewService(nil, nil, WithAccountFreezeProvider(provider))

	normal, notModified, err := svc.GetAppConfig(context.Background(), 1002, 0)
	if err != nil || notModified {
		t.Fatalf("normal GetAppConfig = %+v notModified=%v err=%v", normal, notModified, err)
	}
	frozen, notModified, err := svc.GetAppConfig(context.Background(), 1001, normal.Hash)
	if err != nil || notModified || frozen.Hash == normal.Hash {
		t.Fatalf("frozen GetAppConfig = hash:%d normal:%d notModified=%v err=%v", frozen.Hash, normal.Hash, notModified, err)
	}
	assertFreezeConfig(t, frozen.JSON, since.Unix(), until.Unix(), "https://appeals.example.test/1001")

	if _, notModified, err := svc.GetAppConfig(context.Background(), 1001, frozen.Hash); err != nil || !notModified {
		t.Fatalf("frozen hash replay = notModified:%v err:%v", notModified, err)
	}
	provider.items[1001] = domain.AccountFreeze{
		UserID:    1001,
		Frozen:    true,
		Since:     since,
		Until:     until.Add(24 * time.Hour),
		AppealURL: "https://appeals.example.test/1001/review",
	}
	updated, notModified, err := svc.GetAppConfig(context.Background(), 1001, frozen.Hash)
	if err != nil || notModified || updated.Hash == frozen.Hash {
		t.Fatalf("updated freeze config = hash:%d old:%d notModified=%v err=%v", updated.Hash, frozen.Hash, notModified, err)
	}
	assertFreezeConfig(t, updated.JSON, since.Unix(), until.Add(24*time.Hour).Unix(), "https://appeals.example.test/1001/review")
	other, notModified, err := svc.GetAppConfig(context.Background(), 1002, frozen.Hash)
	if err != nil || notModified || other.Hash != normal.Hash {
		t.Fatalf("other user = hash:%d notModified:%v err:%v", other.Hash, notModified, err)
	}
	assertClearedFreezeConfig(t, other.JSON)
	unauthorized, _, err := svc.GetAppConfig(context.Background(), 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	assertNoFreezeConfig(t, unauthorized.JSON)

	provider.items[1001] = domain.AccountFreeze{UserID: 1001}
	unfrozen, notModified, err := svc.GetAppConfig(context.Background(), 1001, updated.Hash)
	if err != nil || notModified || unfrozen.Hash != normal.Hash {
		t.Fatalf("unfreeze refresh = hash:%d notModified:%v err:%v", unfrozen.Hash, notModified, err)
	}
	assertClearedFreezeConfig(t, unfrozen.JSON)
}

func TestAuthenticatedAppConfigClearsPersistedFreezeWithoutProvider(t *testing.T) {
	svc := NewService(nil, nil)
	unauthorized, _, err := svc.GetAppConfig(context.Background(), 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	assertNoFreezeConfig(t, unauthorized.JSON)
	authenticated, notModified, err := svc.GetAppConfig(context.Background(), 1001, unauthorized.Hash)
	if err != nil || notModified || authenticated.Hash == unauthorized.Hash {
		t.Fatalf("authenticated clear config = hash:%d base:%d notModified:%v err:%v", authenticated.Hash, unauthorized.Hash, notModified, err)
	}
	assertClearedFreezeConfig(t, authenticated.JSON)
}

func TestAccountAppConfigStripsGlobalFreezeFields(t *testing.T) {
	svc := NewService(nil, nil)
	base := domain.AppConfig{Client: "tdesktop", Hash: 9, JSON: []byte(`{"quote_length_max":1024,"freeze_since_date":1,"freeze_until_date":2,"freeze_appeal_url":"https://wrong.example"}`)}
	cfg, err := svc.accountAppConfig(context.Background(), 0, base)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Hash == base.Hash {
		t.Fatal("stripped config reused base hash")
	}
	assertNoFreezeConfig(t, cfg.JSON)
}

func assertFreezeConfig(t *testing.T, body []byte, since, until int64, appealURL string) {
	t.Helper()
	var values map[string]any
	if err := json.Unmarshal(body, &values); err != nil {
		t.Fatal(err)
	}
	if values["freeze_since_date"] != float64(since) || values["freeze_until_date"] != float64(until) || values["freeze_appeal_url"] != appealURL {
		t.Fatalf("freeze config = %#v", values)
	}
}

func assertNoFreezeConfig(t *testing.T, body []byte) {
	t.Helper()
	var values map[string]any
	if err := json.Unmarshal(body, &values); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"freeze_since_date", "freeze_until_date", "freeze_appeal_url"} {
		if _, exists := values[key]; exists {
			t.Fatalf("unexpected %s in config", key)
		}
	}
}

func assertClearedFreezeConfig(t *testing.T, body []byte) {
	t.Helper()
	var values map[string]any
	if err := json.Unmarshal(body, &values); err != nil {
		t.Fatal(err)
	}
	if values["freeze_since_date"] != float64(0) || values["freeze_until_date"] != float64(0) || values["freeze_appeal_url"] != "" {
		t.Fatalf("freeze clear config = %#v", values)
	}
}

type fakeAccountFreezeProvider struct {
	items map[int64]domain.AccountFreeze
}

func (f *fakeAccountFreezeProvider) AccountFreeze(_ context.Context, userID int64) (domain.AccountFreeze, bool, error) {
	freeze, found := f.items[userID]
	return freeze, found, nil
}
