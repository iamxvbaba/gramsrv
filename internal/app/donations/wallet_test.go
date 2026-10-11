package donations

import (
	"os"
	"path/filepath"
	"testing"
)

// TestLoadOrGenerateEncryptionKeyIsStable pins the "works out of the box"
// contract: the first call with no file present generates and persists a
// key, and every call after that -- including from a second process that
// only sees the file, not the first call's in-memory value -- loads that
// exact same key back. Without this, a server restart would silently start
// deriving different addresses and failing to decrypt its own wallet.
func TestLoadOrGenerateEncryptionKeyIsStable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "donation_wallet.key")

	first, err := LoadOrGenerateEncryptionKey(path)
	if err != nil {
		t.Fatalf("first load: %v", err)
	}
	if first == (EncryptionKey{}) {
		t.Fatal("generated key is all zero, want real random bytes")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("stat generated key file: %v", err)
	}
	// File permission bits are POSIX-specific (Windows/NTFS ACLs don't map
	// onto 0600 the way this test's dev/CI runners might), so the mode
	// itself isn't asserted here -- os.WriteFile(path, data, 0o600) is the
	// same call LoadOrGenerateRSAKey already makes for the same reason.

	second, err := LoadOrGenerateEncryptionKey(path)
	if err != nil {
		t.Fatalf("second load: %v", err)
	}
	if second != first {
		t.Fatal("second load returned a different key than the first generated and persisted")
	}
}

func TestLoadOrGenerateEncryptionKeyRejectsCorruptFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "donation_wallet.key")
	if err := os.WriteFile(path, []byte("not 32 bytes"), 0o600); err != nil {
		t.Fatalf("write corrupt key file: %v", err)
	}
	if _, err := LoadOrGenerateEncryptionKey(path); err == nil {
		t.Fatal("want an error for a key file that isn't exactly 32 bytes")
	}
}
