package loadharness

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// requireOwnerOnly asserts that path is not group/world accessible.
//
// Unix file modes carry that permission information, but Windows does not model
// them: os.Stat always reports 0o666 there, so the mode check is meaningless.
// Skip it on Windows, mirroring the production guard in EncryptedFileStorage.
func requireOwnerOnly(t *testing.T, path string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		return
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("%s mode = %o, want 600", filepath.Base(path), got)
	}
}
