package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	telegramlogin "telesrv/internal/app/telegramlogin"
	"telesrv/internal/domain"
)

func TestInitializeAndRotateKeyFiles(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 7, 21, 1, 2, 3, 0, time.UTC)
	if err := run(options{mode: "init", dir: dir, now: func() time.Time { return now }}); err != nil {
		t.Fatal(err)
	}
	ring, err := telegramlogin.LoadSigningKeyRing(filepath.Join(dir, "signing-keys.json"), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	if got := ring.SupportedAlgorithms(); len(got) != 3 || got[0] != "RS256" || got[1] != "ES256" || got[2] != "EdDSA" {
		t.Fatalf("supported algorithms = %#v", got)
	}
	if _, err := telegramlogin.LoadCodeSealer(filepath.Join(dir, "code-keys.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := telegramlogin.LoadClientSecretPepper(filepath.Join(dir, "client-secret-pepper")); err != nil {
		t.Fatal(err)
	}
	if err := run(options{mode: "init", dir: dir, now: func() time.Time { return now }}); err == nil {
		t.Fatal("second initialization unexpectedly overwrote keys")
	}

	later := now.Add(time.Minute)
	if err := run(options{
		mode: "rotate-signing", dir: dir, algorithm: domain.TelegramLoginSigningRS256,
		publishFor: 2 * time.Hour, idTokenTTL: time.Hour, now: func() time.Time { return later },
	}); err != nil {
		t.Fatal(err)
	}
	var manifest signingManifest
	readJSONForTest(t, filepath.Join(dir, "signing-keys.json"), &manifest)
	active, retiring := 0, 0
	for _, key := range manifest.Keys {
		if key.Algorithm != domain.TelegramLoginSigningRS256 {
			continue
		}
		if key.Active {
			active++
		} else if key.PublishUntil == later.Add(2*time.Hour).Format(time.RFC3339) {
			retiring++
		}
	}
	if active != 1 || retiring != 1 {
		t.Fatalf("RS256 active=%d retiring=%d manifest=%#v", active, retiring, manifest)
	}
	ring, err = telegramlogin.LoadSigningKeyRing(filepath.Join(dir, "signing-keys.json"), func() time.Time { return later })
	if err != nil {
		t.Fatal(err)
	}
	jwks, _, err := ring.JWKS()
	if err != nil {
		t.Fatal(err)
	}
	var set struct {
		Keys []json.RawMessage `json:"keys"`
	}
	if err := json.Unmarshal(jwks, &set); err != nil || len(set.Keys) != 4 {
		t.Fatalf("JWKS key count=%d err=%v body=%s", len(set.Keys), err, jwks)
	}

	var before codeManifest
	readJSONForTest(t, filepath.Join(dir, "code-keys.json"), &before)
	if err := run(options{mode: "rotate-code", dir: dir, now: func() time.Time { return later }}); err != nil {
		t.Fatal(err)
	}
	var after codeManifest
	readJSONForTest(t, filepath.Join(dir, "code-keys.json"), &after)
	if after.Active == before.Active || len(after.Keys) != 2 {
		t.Fatalf("code ring before=%#v after=%#v", before, after)
	}
	if _, err := telegramlogin.LoadCodeSealer(filepath.Join(dir, "code-keys.json")); err != nil {
		t.Fatal(err)
	}
}

func TestRotateSigningRejectsTooShortRetirementWindow(t *testing.T) {
	err := run(options{
		mode: "rotate-signing", dir: t.TempDir(), algorithm: domain.TelegramLoginSigningRS256,
		publishFor: 69 * time.Minute, idTokenTTL: time.Hour, now: time.Now,
	})
	if err == nil {
		t.Fatal("short publish-for unexpectedly accepted")
	}
}

func readJSONForTest(t *testing.T, path string, target any) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, target); err != nil {
		t.Fatal(err)
	}
}
