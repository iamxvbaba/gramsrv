package core

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"

	"telesrv/internal/config"
)

func TestLocalStarGiftWithdrawalRequiresPublicListener(t *testing.T) {
	options, err := localStarGiftWithdrawalOptions("not-a-url", "", config.StarGiftExportModeLocal, nil)
	if err != nil || options != nil {
		t.Fatalf("disabled public listener options=%d err=%v, want nil without URL construction", len(options), err)
	}
	options, err = localStarGiftWithdrawalOptions("https://links.example.test", "127.0.0.1:2401", config.StarGiftExportModeLocal, nil)
	if err != nil || len(options) != 2 {
		t.Fatalf("local mode options=%d err=%v, want gift and revenue providers", len(options), err)
	}
	options, err = localStarGiftWithdrawalOptions("https://links.example.test", "127.0.0.1:2401", config.StarGiftExportModeDisabled, nil)
	if err != nil || len(options) != 1 {
		t.Fatalf("disabled gift export options=%d err=%v, want revenue provider", len(options), err)
	}
	if options, err = localStarGiftWithdrawalOptions("not-a-url", "127.0.0.1:2401", config.StarGiftExportModeLocal, nil); err == nil || options != nil {
		t.Fatalf("enabled listener accepted dead public URL: options=%d err=%v", len(options), err)
	}
}

// TestLocalStarGiftWithdrawalSkipsPlainHTTPOutsideLoopback pins gramsrv#135: the
// Docker LAN quick start uses http://<LAN IP>:2401, which must not stop Core
// from starting; it only leaves the bearer-URL withdrawal providers out, with
// a warning. Loopback HTTP keeps them.
func TestLocalStarGiftWithdrawalSkipsPlainHTTPOutsideLoopback(t *testing.T) {
	core, logs := observer.New(zap.WarnLevel)
	options, err := localStarGiftWithdrawalOptions("http://192.168.1.20:2401", "0.0.0.0:2401", config.StarGiftExportModeLocal, zap.New(core))
	if err != nil || options != nil {
		t.Fatalf("plain-HTTP LAN options=%d err=%v, want no providers and no startup error", len(options), err)
	}
	if logs.FilterMessageSnippet("withdrawal disabled").Len() != 1 {
		t.Fatalf("plain-HTTP LAN logs = %v, want one warning", logs.All())
	}
	if options, err = localStarGiftWithdrawalOptions("http://127.0.0.1:2401", "127.0.0.1:2401", config.StarGiftExportModeLocal, nil); err != nil || len(options) != 2 {
		t.Fatalf("loopback HTTP options=%d err=%v, want gift and revenue providers", len(options), err)
	}
}

func TestLoadTONCapabilitySecretRequiresBoundedBase64Key(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "capability.key")
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}
	if err := os.WriteFile(path, []byte(base64.StdEncoding.EncodeToString(key)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadTONCapabilitySecret(path)
	if err != nil || string(loaded) != string(key) {
		t.Fatalf("load capability secret = %x err=%v", loaded, err)
	}

	for name, value := range map[string]string{
		"plaintext": "this-is-not-a-base64-capability-secret",
		"short":     base64.StdEncoding.EncodeToString(make([]byte, 31)),
	} {
		t.Run(name, func(t *testing.T) {
			if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := loadTONCapabilitySecret(path); err == nil {
				t.Fatal("invalid capability secret accepted")
			}
		})
	}
}

func TestLoadTONClaimBotTokenRequiresSingleValidToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "claim-bot.token")
	if err := os.WriteFile(path, []byte("900001:claim-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if token, err := loadTONClaimBotToken(path); err != nil || token != "900001:claim-secret" {
		t.Fatalf("load claim bot token = %q err=%v", token, err)
	}
	for name, value := range map[string]string{
		"missing secret": "900001",
		"multiple lines": "900001:claim-secret\nextra",
		"oversized":      "900001:" + strings.Repeat("x", 506),
	} {
		t.Run(name, func(t *testing.T) {
			if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := loadTONClaimBotToken(path); err == nil {
				t.Fatal("invalid claim bot token accepted")
			}
		})
	}
}
