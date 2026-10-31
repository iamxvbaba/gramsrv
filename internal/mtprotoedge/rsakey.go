package mtprotoedge

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// rsaKeyBits is the bit length of the server RSA private key. MTProto requires 2048-bit.
const rsaKeyBits = 2048

// LoadOrGenerateRSAKey loads the PEM-encoded server RSA private key from path;
// if it does not exist, it generates a new 2048-bit key and persists it
// (including the parent directory).
//
// The server RSA private key is used for the MTProto key exchange; its public key
// fingerprint must be patched into TDesktop (recorded in
// docs/tdesktop-patch-notes.md).
func LoadOrGenerateRSAKey(path string) (*rsa.PrivateKey, error) {
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		key, perr := parseRSAKeyPEM(data)
		if perr != nil {
			return nil, fmt.Errorf("parse %q: %w", path, perr)
		}
		return key, nil
	case errors.Is(err, os.ErrNotExist):
		// Continue with generation.
	default:
		return nil, fmt.Errorf("read %q: %w", path, err)
	}

	key, err := rsa.GenerateKey(rand.Reader, rsaKeyBits)
	if err != nil {
		return nil, fmt.Errorf("generate rsa key: %w", err)
	}

	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("create key dir %q: %w", dir, err)
		}
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	})
	if err := os.WriteFile(path, pemBytes, 0o600); err != nil {
		return nil, fmt.Errorf("write key %q: %w", path, err)
	}
	return key, nil
}

func parseRSAKeyPEM(data []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, errors.New("no PEM block found")
	}
	return x509.ParsePKCS1PrivateKey(block.Bytes)
}
