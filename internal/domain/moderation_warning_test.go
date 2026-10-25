package domain

import (
	"strings"
	"testing"
)

// resetModerationProfileWarning 恢复进程级默认状态，避免测试之间通过
// SetModerationProfileWarning 串味（atomic.Value 是全局的）。
func resetModerationProfileWarning(t *testing.T) {
	t.Helper()
	SetModerationProfileWarning("", "")
	t.Cleanup(func() { SetModerationProfileWarning("", "") })
}

func TestModerationProfileWarningPrependsOverOwnerText(t *testing.T) {
	resetModerationProfileWarning(t)

	// 标记 + 自己的描述：警告是第一行，owner 的文本紧随其后。
	got := ModerationProfileWarning(true, false, "я андрей вот мое описание")
	if want := defaultScamProfileWarning + "\nя андрей вот мое описание"; got != want {
		t.Fatalf("scam about = %q, want %q", got, want)
	}
	if got := ModerationProfileWarning(false, true, "owner text"); got != defaultFakeProfileWarning+"\nowner text" {
		t.Fatalf("fake about = %q", got)
	}
	// 描述为空时只有警告，不留悬空的换行。
	if got := ModerationProfileWarning(true, false, ""); got != defaultScamProfileWarning {
		t.Fatalf("empty-owner about = %q", got)
	}
}

// 默认文案必须是完整可读的一句话（⚠️ Warning: ...），而不是官方客户端那两字母
// 徽标词——角标只画在名字旁，描述里需要解释“为什么被标记”。
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

	// 纯空白（operator 把 .env 里的值留成了空格）不该产出空白首行。
	SetModerationProfileWarning("   ", "\n")
	if got := ModerationProfileWarning(true, false, "owner"); got != defaultScamProfileWarning+"\nowner" {
		t.Fatalf("blank scam config about = %q", got)
	}
	if got := ModerationProfileWarning(false, true, "owner"); got != defaultFakeProfileWarning+"\nowner" {
		t.Fatalf("blank fake config about = %q", got)
	}
}

// 面板拒绝同时置两个标记（ErrPeerModerationFlagsInvalid），但投影层仍要给出
// 确定的单行结果，而不是两段拼接或空串。
func TestModerationProfileWarningBothFlagsPrefersScam(t *testing.T) {
	resetModerationProfileWarning(t)

	got := ModerationProfileWarning(true, true, "owner")
	if want := defaultScamProfileWarning + "\nowner"; got != want {
		t.Fatalf("both flags about = %q, want %q", got, want)
	}
}
