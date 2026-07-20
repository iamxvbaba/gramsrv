package branding

import "testing"

func TestUserVisibleTextRebrandsWordsAndOfficialHosts(t *testing.T) {
	got := UserVisibleText(
		"Telegram telegram TELEGRAM Telegram-like https://translations.telegram.org/en t.me/example desktop.telegram.org",
		"https://chat.example/root/",
	)
	want := "Telesrv Telesrv Telesrv Telesrv-like https://chat.example/root/en chat.example/example chat.example"
	if got != want {
		t.Fatalf("UserVisibleText() = %q, want %q", got, want)
	}
}

func TestUserVisibleTextPreservesTechnicalIdentifiers(t *testing.T) {
	for _, value := range []string{
		"org.telegram.messenger",
		"telegram_antispam_user_id",
		"telegram_aicomposetone",
	} {
		if got := UserVisibleText(value, ""); got != value {
			t.Fatalf("UserVisibleText(%q) = %q, want unchanged", value, got)
		}
	}
}

func TestUserVisibleTextRebrandsBareOfficialHostsWithoutTouchingDottedIdentifiers(t *testing.T) {
	for input, want := range map[string]string{
		"telegram.org":           "telesrv.net",
		"desktop.telegram.org":   "telesrv.net",
		"t.me/example":           "telesrv.net/example",
		"org.telegram.messenger": "org.telegram.messenger",
	} {
		if got := UserVisibleText(input, ""); got != want {
			t.Fatalf("UserVisibleText(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestUserVisibleTextRebrandsLocalizedProductNames(t *testing.T) {
	got := UserVisibleText("Телеграмом تيليجرام تلگرام 텔레그램 טלגרם", "")
	if want := "Telesrv Telesrv Telesrv Telesrv Telesrv"; got != want {
		t.Fatalf("UserVisibleText() = %q, want %q", got, want)
	}
}

func TestClientPresentationNames(t *testing.T) {
	for platform, want := range map[string]string{
		"tdesktop":    DesktopAppName,
		"android":     AndroidAppName,
		"ios":         IOSAppName,
		"macos":       MacOSAppName,
		"telegram-tt": WebAAppName,
		"tweb":        WebKAppName,
	} {
		if got := ClientAppName(platform); got != want {
			t.Fatalf("ClientAppName(%q) = %q, want %q", platform, got, want)
		}
	}
	if got := UserVisibleClientPlatform("telegram-tt"); got != "weba" {
		t.Fatalf("UserVisibleClientPlatform() = %q, want weba", got)
	}
}
