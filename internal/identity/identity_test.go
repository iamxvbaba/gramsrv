package identity

import (
	"os"
	"path/filepath"
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

	if err := s.SetText("  ShuzaGram  ", "  A self-hosted server.  "); err != nil {
		t.Fatal(err)
	}
	info, err = s.Get()
	if err != nil {
		t.Fatal(err)
	}
	if info.Name != "ShuzaGram" || info.Description != "A self-hosted server." {
		t.Fatalf("SetText did not trim/round-trip: %+v", info)
	}
}

func TestStoreIconRoundTrip(t *testing.T) {
	s := NewStore(t.TempDir())

	if _, _, ok := s.Icon(); ok {
		t.Fatal("expected no icon before any write")
	}

	if err := s.SetIcon([]byte("png-bytes"), ".png"); err != nil {
		t.Fatal(err)
	}
	data, ext, ok := s.Icon()
	if !ok || ext != ".png" || string(data) != "png-bytes" {
		t.Fatalf("icon round-trip mismatch: data=%q ext=%q ok=%v", data, ext, ok)
	}

	// Replacing with a different extension must drop the old file, not
	// leave two icons behind.
	if err := s.SetIcon([]byte("jpeg-bytes"), ".jpg"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(s.iconPath(".png")); !os.IsNotExist(err) {
		t.Fatalf("old icon file still exists after replacing with a new extension: %v", err)
	}
	data, ext, ok = s.Icon()
	if !ok || ext != ".jpg" || string(data) != "jpeg-bytes" {
		t.Fatalf("icon after replace mismatch: data=%q ext=%q ok=%v", data, ext, ok)
	}

	if err := s.RemoveIcon(); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := s.Icon(); ok {
		t.Fatal("expected no icon after RemoveIcon")
	}
	if _, err := os.Stat(s.iconPath(".jpg")); !os.IsNotExist(err) {
		t.Fatalf("icon file still exists after RemoveIcon: %v", err)
	}
}

func TestStorePreservesUnrelatedFields(t *testing.T) {
	s := NewStore(t.TempDir())
	if err := s.SetIcon([]byte("bytes"), ".png"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetText("Name", "Description"); err != nil {
		t.Fatal(err)
	}
	info, err := s.Get()
	if err != nil {
		t.Fatal(err)
	}
	if info.IconExt != ".png" {
		t.Fatalf("SetText clobbered the icon extension: %+v", info)
	}
}

func TestGetOnMissingFileIsZeroValueNotError(t *testing.T) {
	s := NewStore(filepath.Join(t.TempDir(), "nonexistent-subdir"))
	info, err := s.Get()
	if err != nil {
		t.Fatalf("Get on a missing file should not error: %v", err)
	}
	if info != (Info{}) {
		t.Fatalf("expected zero value, got %+v", info)
	}
}
