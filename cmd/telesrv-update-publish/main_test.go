package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPublishValidatesAndReplacesManifest(t *testing.T) {
	dir := t.TempDir()
	files := filepath.Join(dir, "files")
	if err := os.Mkdir(files, 0o755); err != nil {
		t.Fatal(err)
	}
	candidate := filepath.Join(dir, "manifest.next.json")
	active := filepath.Join(dir, "manifest.json")
	manifest := `{"schema_version":1,"desktop":{"win64":{"stable":{"build":1,"file":"unused","sha256":"0000000000000000000000000000000000000000000000000000000000000000","disabled":true}}}}`
	if err := os.WriteFile(candidate, []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := publish(candidate, active, files); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(active); err != nil {
		t.Fatalf("active manifest missing: %v", err)
	}
	if _, err := os.Stat(candidate); !os.IsNotExist(err) {
		t.Fatalf("candidate still exists, stat error = %v", err)
	}
}