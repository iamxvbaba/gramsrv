package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestWriteFileAtomicReplacesExistingTarget(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "tl_layer_gen.go")
	if err := os.WriteFile(target, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeFileAtomic(target, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, []byte("new")) {
		t.Fatalf("target content = %q", got)
	}
	assertNoAtomicTemporaryFiles(t, target)
}

func TestWriteFileAtomicCleansTemporaryAfterRenameFailure(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "occupied")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(target, "marker")
	if err := os.WriteFile(marker, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeFileAtomic(target, []byte("new"), 0o600); err == nil {
		t.Fatal("rename over directory unexpectedly succeeded")
	}
	if got, err := os.ReadFile(marker); err != nil || string(got) != "keep" {
		t.Fatalf("original target changed: content=%q error=%v", got, err)
	}
	assertNoAtomicTemporaryFiles(t, target)
}

func TestFormattedSourceCommitsOnlyAfterCompleteBatch(t *testing.T) {
	dir := t.TempDir()
	fs := &formattedSource{Root: dir, Format: true, files: make(map[string][]byte)}
	if err := fs.WriteFile("tl_one_gen.go", []byte("package generated\nconst One=1\n")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "tl_one_gen.go")); !os.IsNotExist(err) {
		t.Fatalf("buffered file became visible before commit: %v", err)
	}
	if err := fs.WriteFile("tl_broken_gen.go", []byte("package generated\nfunc")); err == nil {
		t.Fatal("invalid Go source unexpectedly formatted")
	}
	if _, err := os.Stat(filepath.Join(dir, "tl_one_gen.go")); !os.IsNotExist(err) {
		t.Fatalf("format failure changed target directory: %v", err)
	}
	if err := fs.Commit(); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(filepath.Join(dir, "tl_one_gen.go")); err != nil || !bytes.Contains(got, []byte("const One = 1")) {
		t.Fatalf("committed source = %q, error=%v", got, err)
	}
}

func assertNoAtomicTemporaryFiles(t *testing.T, target string) {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(filepath.Dir(target), "."+filepath.Base(target)+".tmp-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("atomic write left temporary files: %v", matches)
	}
}
