package donations

import (
	"bytes"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// v2 (decision D7): the wallet key is a shared file every Core reads. It is
// never generated, so a missing file must fail rather than mint a per-Core key
// that would fork the wallet.
func TestLoadEncryptionKeyFileAcceptsRawAndHex(t *testing.T) {
	dir := t.TempDir()
	raw := bytes.Repeat([]byte{0x2a}, 32)
	rawPath := filepath.Join(dir, "raw.key")
	if err := os.WriteFile(rawPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	hexPath := filepath.Join(dir, "hex.key")
	if err := os.WriteFile(hexPath, []byte(hex.EncodeToString(raw)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fromRaw, err := LoadEncryptionKeyFile(rawPath)
	if err != nil {
		t.Fatalf("raw key: %v", err)
	}
	fromHex, err := LoadEncryptionKeyFile(hexPath)
	if err != nil {
		t.Fatalf("hex key: %v", err)
	}
	if fromRaw != fromHex || !bytes.Equal(fromRaw[:], raw) {
		t.Fatal("raw and hex encodings of the same key loaded differently")
	}
}

func TestLoadEncryptionKeyFileNeverGenerates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.key")
	if _, err := LoadEncryptionKeyFile(path); err == nil {
		t.Fatal("missing key file was accepted")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("loader created %s (stat err %v); keys must never be generated", path, err)
	}
	if _, err := LoadEncryptionKeyFile(""); err == nil {
		t.Fatal("empty path was accepted")
	}
}

func TestLoadEncryptionKeyFileRejectsCorruptAndZeroKeys(t *testing.T) {
	dir := t.TempDir()
	for name, data := range map[string][]byte{
		"short.key": []byte("too short"),
		"hex31.key": []byte(strings.Repeat("ab", 31)),
		"zero.key":  make([]byte, 32),
	} {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadEncryptionKeyFile(path); err == nil {
			t.Fatalf("%s was accepted", name)
		}
	}
}
