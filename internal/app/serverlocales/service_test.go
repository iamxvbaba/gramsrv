package serverlocales

import (
	"strings"
	"testing"
	"time"
)

func TestSupportedLanguagesIncludeBundledClientCodes(t *testing.T) {
	want := []string{"de", "en", "pt-br", "ru", "zh-hans", "zh-hant"}
	supported := map[string]bool{}
	for _, lang := range SupportedLanguages() {
		supported[lang] = true
	}
	for _, lang := range want {
		if !supported[lang] {
			t.Fatalf("SupportedLanguages missing %q in %v", lang, SupportedLanguages())
		}
	}
}

func TestEverySupportedLanguageHasOwnCompleteCatalog(t *testing.T) {
	for _, lang := range SupportedLanguages() {
		values, ok := defaultCatalog.messages[lang]
		if !ok {
			t.Fatalf("supported language %q has no server locale file", lang)
		}
		for _, key := range requiredMessageKeys {
			if values[key] == "" {
				t.Fatalf("supported language %q missing key %q", lang, key)
			}
		}
	}
}

func TestLoginCodeMessageLocalizesRussian(t *testing.T) {
	msg := LoginCodeMessage("ru", "52342")
	for _, want := range []string{"Код для входа в Telesrv: 52342", "Не давайте код никому", "Ваш аккаунт"} {
		if !strings.Contains(msg.Body, want) {
			t.Fatalf("Russian login message %q missing %q", msg.Body, want)
		}
	}
	if len(msg.Entities) != 2 || msg.Entities[1].Length != 5 {
		t.Fatalf("entities = %+v, want title/code bold", msg.Entities)
	}
}

func TestNewLoginMessageLocalizesGerman(t *testing.T) {
	msg := NewLoginMessage("de", NewLoginParams{
		Name:   "Test User",
		When:   time.Date(2026, 7, 23, 18, 50, 39, 0, time.UTC),
		Device: "Telesrv Desktop",
	})
	if !strings.Contains(msg.Body, "Neue Anmeldung.") || strings.Contains(msg.Body, "New login.") {
		t.Fatalf("German message was not localized: %q", msg.Body)
	}
}

func TestNewLoginMessageLocalizesRussian(t *testing.T) {
	msg := NewLoginMessage("ru", NewLoginParams{
		Name:   "Reynard Cloud Admin",
		When:   time.Date(2026, 7, 22, 22, 11, 6, 0, time.UTC),
		Device: "reynard / Android / 1.0",
	})
	for _, want := range []string{"Вход с нового устройства.", "22/07/2026 в 22:11:06 UTC", "Устройство:", "Место входа:"} {
		if !strings.Contains(msg.Body, want) {
			t.Fatalf("Russian new-login message %q missing %q", msg.Body, want)
		}
	}
}
