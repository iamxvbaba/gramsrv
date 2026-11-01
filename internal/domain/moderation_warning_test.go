package domain

import (
	"strings"
	"testing"
)

// resetModerationProfileWarning restores the process-level default state so tests do not
// bleed into each other through SetModerationProfileWarning (the atomic.Value is global).
func resetModerationProfileWarning(t *testing.T) {
	t.Helper()
	SetModerationProfileWarning("", "")
	t.Cleanup(func() { SetModerationProfileWarning("", "") })
}

func TestModerationProfileWarningPrependsOverOwnerText(t *testing.T) {
	resetModerationProfileWarning(t)

	// Flag + own description: the warning is the first line and the owner's text follows.
	got := ModerationProfileWarning(true, false, "я андрей вот мое описание")
	if want := defaultScamProfileWarning + "\nя андрей вот мое описание"; got != want {
		t.Fatalf("scam about = %q, want %q", got, want)
	}
	if got := ModerationProfileWarning(false, true, "owner text"); got != defaultFakeProfileWarning+"\nowner text" {
		t.Fatalf("fake about = %q", got)
	}
	// With an empty description only the warning remains, with no dangling newline.
	if got := ModerationProfileWarning(true, false, ""); got != defaultScamProfileWarning {
		t.Fatalf("empty-owner about = %q", got)
	}
}

// The default text must be a full readable sentence (⚠️ Warning: ...), not the official client's two-letter
// badge word — the badge is only drawn next to the name, and the description needs to explain "why it was flagged".
func TestModerationProfileWarningDefaultsAreFullSentences(t *testing.T) {
	for name, text := range map[string]string{
		"scam": defaultScamProfileWarning,
		"fake": defaultFakeProfileWarning,
	} {
		if len(text) < 40 {
			t.Errorf("%s default = %q, want a full warning sentence", name, text)
		}
		if text != strings.TrimSpace(text) {
			t.Errorf("%s default = %q, want it trimmed", name, text)
		}
		if !strings.HasPrefix(text, "⚠️ Warning: ") {
			t.Errorf("%s default = %q, want the ⚠️ Warning prefix", name, text)
		}
	}
}

func TestModerationProfileWarningUntouchedWhenUnflagged(t *testing.T) {
	resetModerationProfileWarning(t)

	if got := ModerationProfileWarning(false, false, "owner text"); got != "owner text" {
		t.Fatalf("unflagged about = %q, want the owner text verbatim", got)
	}
	if got := ModerationProfileWarning(false, false, ""); got != "" {
		t.Fatalf("unflagged empty about = %q, want empty", got)
	}
}

func TestModerationProfileWarningUsesConfiguredText(t *testing.T) {
	resetModerationProfileWarning(t)

	SetModerationProfileWarning("⚠️ Аккаунт помечен как SCAM.", "⚠️ Аккаунт помечен как FAKE.")
	if got := ModerationProfileWarning(true, false, "описание"); got != "⚠️ Аккаунт помечен как SCAM.\nописание" {
		t.Fatalf("configured scam about = %q", got)
	}
	if got := ModerationProfileWarning(false, true, "описание"); got != "⚠️ Аккаунт помечен как FAKE.\nописание" {
		t.Fatalf("configured fake about = %q", got)
	}
}

func TestModerationProfileWarningBlankConfigFallsBackToDefault(t *testing.T) {
	resetModerationProfileWarning(t)

	// Pure whitespace (an operator left the .env value as spaces) must not produce a blank first line.
	SetModerationProfileWarning("   ", "\n")
	if got := ModerationProfileWarning(true, false, "owner"); got != defaultScamProfileWarning+"\nowner" {
		t.Fatalf("blank scam config about = %q", got)
	}
	if got := ModerationProfileWarning(false, true, "owner"); got != defaultFakeProfileWarning+"\nowner" {
		t.Fatalf("blank fake config about = %q", got)
	}
}

// The panel rejects setting both flags at once (ErrPeerModerationFlagsInvalid), but the projection layer must still produce
// a determinate single-line result, not a concatenation of two or an empty string.
func TestModerationProfileWarningBothFlagsPrefersScam(t *testing.T) {
	resetModerationProfileWarning(t)

	got := ModerationProfileWarning(true, true, "owner")
	if want := defaultScamProfileWarning + "\nowner"; got != want {
		t.Fatalf("both flags about = %q, want %q", got, want)
	}
}
