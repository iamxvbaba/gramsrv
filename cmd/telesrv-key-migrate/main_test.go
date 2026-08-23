package main

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"
)

func TestMigratePKCS8InPlaceCreatesPKCS1Backup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "server_rsa.pem")
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	data := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: mustMarshalPKCS8(t, key)})
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := migrate(path, path, ""); err != nil {
		t.Fatal(err)
	}
	converted, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(converted)
	if block == nil {
		t.Fatal("converted key is not PEM")
	}
	if block.Type != "RSA PRIVATE KEY" {
		t.Fatalf("converted key type = %q, want RSA PRIVATE KEY", block.Type)
	}
	if _, err := x509.ParsePKCS1PrivateKey(block.Bytes); err != nil {
		t.Fatalf("parse converted key: %v", err)
	}
	if _, err := os.Stat(path + ".before-pkcs1"); err != nil {
		t.Fatalf("backup missing: %v", err)
	}
}

func mustMarshalPKCS8(t *testing.T, key *rsa.PrivateKey) []byte {
	t.Helper()
	data, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return data
}