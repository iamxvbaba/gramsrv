package identity

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestStoreTextRoundTrip(t *testing.T) {
	s := NewStore(t.TempDir())

	info, err := s.Get()
	if err != nil {
		t.Fatal(err)
	}
	if info != (Info{}) {
		t.Fatalf("expected zero-value Info before any write, got %+v", info)
	}

	if err := s.SetText("  Gamsrv  ", "  A self-hosted server.  "); err != nil {
		t.Fatal(err)
	}
	info, err = s.Get()
	if err != nil {
		t.Fatal(err)
	}
	if info.Name != "Gamsrv" || info.Description != "A self-hosted server." {
		t.Fatalf("got %+v", info)
	}
}

func TestConcurrentTextAndIconEditsPreserveBoth(t *testing.T) {
	s := NewStore(t.TempDir())
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			if err := s.SetText("Name", "Description"); err != nil {
				t.Errorf("SetText: %v", err)
			}
		}()
		go func() {
			defer wg.Done()
			if err := s.SetIcon([]byte{1, 2, 3}, ".png"); err != nil {
				t.Errorf("SetIcon: %v", err)
			}
		}()
	}
	wg.Wait()
	info, err := s.Get()
	if err != nil {
		t.Fatal(err)
	}
	if info.Name != "Name" || info.IconExt != ".png" {
		t.Fatalf("concurrent edits lost a field: %+v", info)
	}
}

func TestStoreIconRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)

	if _, _, ok := s.Icon(); ok {
		t.Fatal("expected no icon before any upload")
	}

	png := []byte{0x89, 'P', 'N', 'G', 1, 2, 3}
	if err := s.SetIcon(png, ".png"); err != nil {
		t.Fatal(err)
	}
	data, ext, ok := s.Icon()
	if !ok || ext != ".png" || string(data) != string(png) {
		t.Fatalf("Icon() = %v, %q, %v", data, ext, ok)
	}
	if _, err := os.Stat(filepath.Join(dir, "icon.png")); err != nil {
		t.Fatal(err)
	}

	// Replacing with a different extension removes the old file.
	jpg := []byte{0xFF, 0xD8, 0xFF}
	if err := s.SetIcon(jpg, ".jpg"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "icon.png")); !os.IsNotExist(err) {
		t.Fatal("old icon.png should have been removed")
	}
	data, ext, ok = s.Icon()
	if !ok || ext != ".jpg" || string(data) != string(jpg) {
		t.Fatalf("Icon() after replace = %v, %q, %v", data, ext, ok)
	}

	if err := s.RemoveIcon(); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := s.Icon(); ok {
		t.Fatal("expected no icon after RemoveIcon")
	}

	info, err := s.Get()
	if err != nil {
		t.Fatal(err)
	}
	if info.IconExt != "" {
		t.Fatalf("IconExt should be empty after removal, got %q", info.IconExt)
	}
}

func TestStorePreservesIconAcrossTextEdits(t *testing.T) {
	s := NewStore(t.TempDir())
	if err := s.SetIcon([]byte{1, 2, 3}, ".webp"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetText("New Name", "New description"); err != nil {
		t.Fatal(err)
	}
	_, ext, ok := s.Icon()
	if !ok || ext != ".webp" {
		t.Fatalf("icon lost after unrelated SetText: ext=%q ok=%v", ext, ok)
	}
}

func TestStoreWelcomeMessageTemplatesRoundTrip(t *testing.T) {
	s := NewStore(t.TempDir())

	info, err := s.Get()
	if err != nil {
		t.Fatal(err)
	}
	if info.WelcomeMessagePhoneTemplate != "" || info.WelcomeMessageEmailTemplate != "" {
		t.Fatalf("expected empty overrides before any write, got %+v", info)
	}

	if err := s.SetWelcomeMessageTemplates("  Custom phone template  ", "Custom email template"); err != nil {
		t.Fatal(err)
	}
	info, err = s.Get()
	if err != nil {
		t.Fatal(err)
	}
	if info.WelcomeMessagePhoneTemplate != "Custom phone template" || info.WelcomeMessageEmailTemplate != "Custom email template" {
		t.Fatalf("got %+v", info)
	}

	// Clearing one override (empty string) must not disturb the other.
	if err := s.SetWelcomeMessageTemplates("", "Custom email template"); err != nil {
		t.Fatal(err)
	}
	info, err = s.Get()
	if err != nil {
		t.Fatal(err)
	}
	if info.WelcomeMessagePhoneTemplate != "" || info.WelcomeMessageEmailTemplate != "Custom email template" {
		t.Fatalf("got %+v after clearing phone override", info)
	}
}

func TestStoreLoginCodeMessageTemplateRoundTrip(t *testing.T) {
	s := NewStore(t.TempDir())

	info, err := s.Get()
	if err != nil {
		t.Fatal(err)
	}
	if info.LoginCodeMessageTemplate != "" {
		t.Fatalf("expected empty override before any write, got %+v", info)
	}

	if err := s.SetLoginCodeMessageTemplate("  Custom code template {{code}}  "); err != nil {
		t.Fatal(err)
	}
	info, err = s.Get()
	if err != nil {
		t.Fatal(err)
	}
	if info.LoginCodeMessageTemplate != "Custom code template {{code}}" {
		t.Fatalf("got %+v", info)
	}

	// Clearing (empty string) resets to "unset".
	if err := s.SetLoginCodeMessageTemplate(""); err != nil {
		t.Fatal(err)
	}
	info, err = s.Get()
	if err != nil {
		t.Fatal(err)
	}
	if info.LoginCodeMessageTemplate != "" {
		t.Fatalf("expected override cleared, got %+v", info)
	}
}

func TestStoreLoginCodeMessageTemplatePreservedAcrossTextEdits(t *testing.T) {
	s := NewStore(t.TempDir())
	if err := s.SetLoginCodeMessageTemplate("code tpl {{code}}"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetText("New Name", "New description"); err != nil {
		t.Fatal(err)
	}
	info, err := s.Get()
	if err != nil {
		t.Fatal(err)
	}
	if info.LoginCodeMessageTemplate != "code tpl {{code}}" {
		t.Fatalf("login code message template lost after unrelated SetText: %+v", info)
	}
}

func TestStoreWelcomeMessageTemplatesPreservedAcrossTextEdits(t *testing.T) {
	s := NewStore(t.TempDir())
	if err := s.SetWelcomeMessageTemplates("phone tpl", "email tpl"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetText("New Name", "New description"); err != nil {
		t.Fatal(err)
	}
	info, err := s.Get()
	if err != nil {
		t.Fatal(err)
	}
	if info.WelcomeMessagePhoneTemplate != "phone tpl" || info.WelcomeMessageEmailTemplate != "email tpl" {
		t.Fatalf("welcome message templates lost after unrelated SetText: %+v", info)
	}
}
