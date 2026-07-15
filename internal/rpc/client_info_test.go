package rpc

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestNormalizeClientInfoBoundsMetadataAfterClassification(t *testing.T) {
	raw := ClientInfo{
		APIID:          2040,
		DeviceModel:    "Mozilla/5.0 (iPhone) AppleWebKit/605.1.15 Safari/604.1 " + strings.Repeat("x", 160),
		SystemVersion:  strings.Repeat("iOS", 30),
		AppVersion:     strings.Repeat("2.2", 30),
		SystemLangCode: strings.Repeat("zh-", 30),
		LangPack:       "webk" + strings.Repeat("x", 80),
		LangCode:       strings.Repeat("zh-", 30),
	}

	got := normalizeClientInfo(raw)
	if got.ClientType() != ClientTypeTWeb {
		t.Fatalf("client type = %s, want %s", got.ClientType(), ClientTypeTWeb)
	}
	assertClientMetadataRunes(t, "device model", got.DeviceModel, maxClientDeviceModelRunes)
	assertClientMetadataRunes(t, "system version", got.SystemVersion, maxClientMetadataRunes)
	assertClientMetadataRunes(t, "app version", got.AppVersion, maxClientMetadataRunes)
	assertClientMetadataRunes(t, "system language", got.SystemLangCode, maxClientMetadataRunes)
	assertClientMetadataRunes(t, "language pack", got.LangPack, maxClientMetadataRunes)
	assertClientMetadataRunes(t, "language code", got.LangCode, maxClientMetadataRunes)
}

func TestNormalizeClientInfoUsesRuneLimitsAndProducesValidUTF8(t *testing.T) {
	got := normalizeClientInfo(ClientInfo{
		DeviceModel:   strings.Repeat("\u754c", maxClientDeviceModelRunes+1),
		SystemVersion: strings.Repeat("\U0001f4f1", maxClientMetadataRunes+1),
		AppVersion:    "2.2" + string([]byte{0xff}),
		LangPack:      "webk",
	})

	assertClientMetadataRunes(t, "device model", got.DeviceModel, maxClientDeviceModelRunes)
	assertClientMetadataRunes(t, "system version", got.SystemVersion, maxClientMetadataRunes)
	if !utf8.ValidString(got.AppVersion) {
		t.Fatalf("app version is not valid UTF-8: %q", got.AppVersion)
	}
}

func TestNormalizedClientInfoIsSharedByAuthPersistencePaths(t *testing.T) {
	authKeyID := [8]byte{1, 2, 3, 4, 5, 6, 7, 8}
	ctx := WithLayer(context.Background(), 227)
	ctx = WithAuthKeyID(ctx, authKeyID)
	ctx = WithClientInfo(ctx, ClientInfo{
		APIID:         2040,
		DeviceModel:   "Mozilla/5.0 AppleWebKit/605.1.15 " + strings.Repeat("x", 160),
		SystemVersion: strings.Repeat("iOS", 30),
		AppVersion:    strings.Repeat("2.2", 30),
		LangPack:      "webk",
	})
	info, ok := ClientInfoFrom(ctx)
	if !ok {
		t.Fatal("normalized client info missing from context")
	}

	authz := (&Router{}).authzFromCtx(ctx)
	authKeyInfo := domainAuthKeyClientInfo(clientSessionInfo{
		layer:         LayerFrom(ctx),
		hasClientInfo: true,
		clientInfo:    info,
	})
	if authz.DeviceModel != authKeyInfo.DeviceModel ||
		authz.SystemVersion != authKeyInfo.SystemVersion ||
		authz.AppVersion != authKeyInfo.AppVersion ||
		authz.Platform != authKeyInfo.Platform ||
		authz.APIID != authKeyInfo.APIID ||
		authz.Layer != authKeyInfo.Layer {
		t.Fatalf("authorization metadata %+v differs from auth-key metadata %+v", authz, authKeyInfo)
	}
}

func TestNormalizeClientInfoPreservesMetadataWithinLimits(t *testing.T) {
	raw := ClientInfo{
		APIID:          4,
		DeviceModel:    "Google Pixel 9",
		SystemVersion:  "SDK 36",
		AppVersion:     "12.7.3",
		SystemLangCode: "en-US",
		LangPack:       "android",
		LangCode:       "en",
	}
	got := normalizeClientInfo(raw)
	if got.DeviceModel != raw.DeviceModel || got.SystemVersion != raw.SystemVersion ||
		got.AppVersion != raw.AppVersion || got.SystemLangCode != raw.SystemLangCode ||
		got.LangPack != raw.LangPack || got.LangCode != raw.LangCode {
		t.Fatalf("metadata within limits changed: got %+v, want %+v", got, raw)
	}
}

func assertClientMetadataRunes(t *testing.T, field, value string, want int) {
	t.Helper()
	if got := utf8.RuneCountInString(value); got != want {
		t.Fatalf("%s rune count = %d, want %d", field, got, want)
	}
	if !utf8.ValidString(value) {
		t.Fatalf("%s is not valid UTF-8", field)
	}
}
