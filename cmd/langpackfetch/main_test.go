package main

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iamxvbaba/td/tg"
)

func TestWritePack(t *testing.T) {
	root := t.TempDir()
	diff := &tg.LangPackDifference{
		LangCode: "pt-br",
		Version:  42,
		Strings: []tg.LangPackStringClass{
			&tg.LangPackString{Key: "plain", Value: "value"},
			&tg.LangPackStringPluralized{Key: "items", OneValue: "one", OtherValue: "many"},
			&tg.LangPackStringDeleted{Key: "removed"},
		},
	}
	written, err := writePack(root, "tdesktop", diff)
	if err != nil {
		t.Fatal(err)
	}
	wantPath := filepath.Join(root, "tdesktop", "tdesktop_pt-br_v42.strings")
	if written.Path != wantPath {
		t.Fatalf("path = %q, want %q", written.Path, wantPath)
	}
	data, err := os.ReadFile(wantPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{`"plain" = "value";`, `"items#one" = "one";`, `"items#other" = "many";`} {
		if !strings.Contains(text, want) {
			t.Errorf("output does not contain %q: %s", want, text)
		}
	}
	if strings.Contains(text, "removed") {
		t.Errorf("deleted key was emitted: %s", text)
	}
	sum := sha256.Sum256(data)
	if written.SHA256 != hex.EncodeToString(sum[:]) {
		t.Fatalf("sha256 = %q, want %x", written.SHA256, sum)
	}
}

func TestWritePackRejectsPathComponents(t *testing.T) {
	_, err := writePack(t.TempDir(), "../android", &tg.LangPackDifference{LangCode: "en", Version: 1})
	if err == nil {
		t.Fatal("expected invalid pack error")
	}
	_, err = writePack(t.TempDir(), "android", &tg.LangPackDifference{LangCode: "../en", Version: 1})
	if err == nil {
		t.Fatal("expected invalid language code error")
	}
}

func TestWriteFileAtomicReplacesExisting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "manifest.json")
	if err := writeFileAtomic(path, []byte("first")); err != nil {
		t.Fatal(err)
	}
	if err := writeFileAtomic(path, []byte("second")); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "second" {
		t.Fatalf("content = %q, want second", data)
	}
}
