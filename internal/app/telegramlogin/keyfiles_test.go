package telegramlogin

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadSigningKeyRingAndSymmetricKeyFiles(t *testing.T) {
	dir := t.TempDir()
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(rsaKey)})
	if err := os.WriteFile(filepath.Join(dir, "rsa.pem"), pemBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	manifest := map[string]any{
		"version": 1,
		"keys": []map[string]any{{
			"algorithm": "RS256", "kid": "rsa-test", "private_key_file": "rsa.pem", "active": true,
		}},
	}
	manifestBytes, _ := json.Marshal(manifest)
	manifestPath := filepath.Join(dir, "signing.json")
	if err := os.WriteFile(manifestPath, manifestBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	ring, err := LoadSigningKeyRing(manifestPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := ring.SupportedAlgorithms(); len(got) != 1 || got[0] != "RS256" {
		t.Fatalf("algorithms=%#v", got)
	}

	codeKey := make([]byte, 32)
	pepper := make([]byte, 32)
	_, _ = rand.Read(codeKey)
	_, _ = rand.Read(pepper)
	codeManifest, _ := json.Marshal(map[string]any{
		"version": 1, "active": "2026-07", "keys": map[string]string{"2026-07": base64.RawURLEncoding.EncodeToString(codeKey)},
	})
	codePath := filepath.Join(dir, "code-keys.json")
	pepperPath := filepath.Join(dir, "pepper")
	if err := os.WriteFile(codePath, codeManifest, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pepperPath, []byte(base64.RawURLEncoding.EncodeToString(pepper)), 0o600); err != nil {
		t.Fatal(err)
	}
	sealer, err := LoadCodeSealer(codePath)
	if err != nil {
		t.Fatal(err)
	}
	sealed, nonce, kid, err := sealer.Seal("code", []byte("aad"))
	if err != nil {
		t.Fatal(err)
	}
	if opened, err := sealer.Open(sealed, nonce, kid, []byte("aad")); err != nil || opened != "code" {
		t.Fatalf("open=%q err=%v", opened, err)
	}
	loadedPepper, err := LoadClientSecretPepper(pepperPath)
	if err != nil || string(loadedPepper) != string(pepper) {
		t.Fatalf("pepper len=%d err=%v", len(loadedPepper), err)
	}
}

func TestLoadSigningKeyRingRejectsUnknownManifestFieldAndUnboundedRetiringKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(path, []byte(`{"version":1,"keys":[],"unknown":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadSigningKeyRing(path, func() time.Time { return time.Now() }); err == nil {
		t.Fatal("unknown manifest field unexpectedly accepted")
	}
}
