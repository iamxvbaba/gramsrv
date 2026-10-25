package loadharness

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestEncryptedFileStorageRoundTripAndNonceRotation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.bin")
	var key [32]byte
	for i := range key {
		key[i] = byte(i + 1)
	}
	storage := &EncryptedFileStorage{Path: path, Key: key}
	plain := []byte(`{"auth_key":"plaintext-secret-marker"}`)
	if err := storage.StoreSession(context.Background(), plain); err != nil {
		t.Fatal(err)
	}
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(first, []byte("plaintext-secret-marker")) {
		t.Fatal("encrypted session retained plaintext auth material")
	}
	if got, err := storage.LoadSession(context.Background()); err != nil || !bytes.Equal(got, plain) {
		t.Fatalf("round trip = %q, %v", got, err)
	}
	if err := storage.StoreSession(context.Background(), plain); err != nil {
		t.Fatal(err)
	}
	second, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(first, second) {
		t.Fatal("successive session writes reused ciphertext/nonce")
	}
	wrong := key
	wrong[0] ^= 0xff
	if _, err := (&EncryptedFileStorage{Path: path, Key: wrong}).LoadSession(context.Background()); err == nil {
		t.Fatal("wrong session key unexpectedly authenticated")
	}
	requireOwnerOnly(t, path)
}

func TestSessionKeyGenerationRefusesOverwrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.key")
	if err := GenerateSessionKey(path); err != nil {
		t.Fatal(err)
	}
	first, err := LoadSessionKey(path)
	if err != nil {
		t.Fatal(err)
	}
	if first == ([32]byte{}) {
		t.Fatal("generated all-zero key")
	}
	if err := GenerateSessionKey(path); err == nil {
		t.Fatal("keygen overwrote an existing key")
	}
}

func TestWriteFileAtomicReplacesExisting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "report.json")
	if err := writeFileAtomic(path, []byte("first"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeFileAtomic(path, []byte("second"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != "second" {
		t.Fatalf("replacement = %q, %v", got, err)
	}
}
