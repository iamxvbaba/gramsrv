package branding

import "testing"

func TestUserVisibleTextRebrandsWordsAndOfficialHosts(t *testing.T) {
	got := UserVisibleText(
		"Telegram telegram TELEGRAM Telegram-like https://translations.telegram.org/en telegram.org/example desktop.telegram.org",
		"https://chat.example/root/",
	)
	want := "ShuzaGram ShuzaGram ShuzaGram ShuzaGram-like https://chat.example/root/en chat.example/example chat.example"
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
		"telegram.org":           "sgq.me",
		"desktop.telegram.org":   "sgq.me",
		"sgq.me/example":         "sgq.me/example",
		"org.telegram.messenger": "org.telegram.messenger",
	} {
		if got := UserVisibleText(input, ""); got != want {
			t.Fatalf("UserVisibleText(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestUserVisibleTextRebrandsLocalizedProductNames(t *testing.T) {
	got := UserVisibleText("Телеграмом تيليجرام تلگرام 텔레그램 טלגרם", "")
	if want := "ShuzaGram ShuzaGram ShuzaGram ShuzaGram ShuzaGram"; got != want {
		t.Fatalf("UserVisibleText() = %q, want %q", got, want)
	}
}

func TestClientPresentationNames(t *testing.T) {
	cfg := Current()
	for platform, want := range map[string]string{
		"tdesktop":    cfg.DesktopAppName,
		"android":     cfg.AndroidAppName,
		"ios":         cfg.IOSAppName,
		"macos":       cfg.MacOSAppName,
		"telegram-tt": cfg.WebAAppName,
		"tweb":        cfg.WebKAppName,
	} {
		if got := ClientAppName(platform); got != want {
			t.Fatalf("ClientAppName(%q) = %q, want %q", platform, got, want)
		}
	}
	if got := UserVisibleClientPlatform("telegram-tt"); got != "weba" {
		t.Fatalf("UserVisibleClientPlatform() = %q, want weba", got)
	}
}

func TestConfigureInstallsCompleteBrandSnapshot(t *testing.T) {
	previous := Current()
	t.Cleanup(func() {
		if err := Configure(previous); err != nil {
			t.Fatalf("restore branding: %v", err)
		}
	})

	cfg := Config{
		ProductName:     "Example Chat",
		ProductUsername: "@Example_Chat",
		DesktopAppName:  "Example Workstation",
		AndroidAppName:  "Example Droid",
		IOSAppName:      "Example Phone",
		MacOSAppName:    "Example Mac",
		WebAAppName:     "Example Web Alpha",
		WebKAppName:     "Example Web Kappa",
		PremiumName:     "Example Plus",
		StarsName:       "Example Credits",
		PublicBaseURL:   "https://links.example.test/root/",
	}
	if err := Configure(cfg); err != nil {
		t.Fatalf("Configure: %v", err)
	}
	if got := Current(); got.ProductUsername != "example_chat" || got.PublicBaseURL != "https://links.example.test/root" {
		t.Fatalf("Current() = %+v", got)
	}
	if got := ClientAppName("android"); got != "Example Droid" {
		t.Fatalf("ClientAppName(android) = %q", got)
	}
	if got := UserVisibleText("Telegram at telegram.org/example", ""); got != "Example Chat at links.example.test/example" {
		t.Fatalf("UserVisibleText() = %q", got)
	}
}

func TestValidateRejectsIncompleteOrUnsafeBranding(t *testing.T) {
	for name, mutate := range map[string]func(*Config){
		"blank product": func(cfg *Config) { cfg.ProductName = "   " },
		"control":       func(cfg *Config) { cfg.StarsName = "bad\nname" },
		"username":      func(cfg *Config) { cfg.ProductUsername = "3bad" },
		"public URL":    func(cfg *Config) { cfg.PublicBaseURL = "file:///tmp/brand" },
	} {
		t.Run(name, func(t *testing.T) {
			cfg := DefaultConfig()
			mutate(&cfg)
			if _, err := Validate(cfg); err == nil {
				t.Fatal("Validate accepted invalid branding")
			}
		})
	}
}
