package telegramlogin

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lestrrat-go/jwx/v3/jwk"

	"telesrv/internal/domain"
)

const (
	maxTelegramLoginManifestBytes = 1 << 20
	maxTelegramLoginKeyBytes      = 256 << 10
)

type signingKeyManifest struct {
	Version int                       `json:"version"`
	Keys    []signingKeyManifestEntry `json:"keys"`
}

type signingKeyManifestEntry struct {
	Algorithm      domain.TelegramLoginSigningAlgorithm `json:"algorithm"`
	KeyID          string                               `json:"kid,omitempty"`
	PrivateKeyFile string                               `json:"private_key_file"`
	Active         bool                                 `json:"active"`
	PublishUntil   string                               `json:"publish_until,omitempty"`
}

// LoadSigningKeyRing reads a versioned manifest and private PEM/JWK files.
// Relative key paths are resolved against the manifest directory. The caller
// should atomically replace files and rebuild/swap the ring when rotating.
func LoadSigningKeyRing(path string, now func() time.Time) (*SigningKeyRing, error) {
	var manifest signingKeyManifest
	if err := readStrictJSONFile(path, maxTelegramLoginManifestBytes, &manifest); err != nil {
		return nil, fmt.Errorf("load telegram login signing manifest: %w", err)
	}
	if manifest.Version != 1 || len(manifest.Keys) == 0 || len(manifest.Keys) > 32 {
		return nil, errors.New("telegram login signing manifest has invalid version or key count")
	}
	baseDir := filepath.Dir(path)
	materials := make([]SigningKeyMaterial, 0, len(manifest.Keys))
	for index, entry := range manifest.Keys {
		keyPath := strings.TrimSpace(entry.PrivateKeyFile)
		if !entry.Algorithm.Valid() || keyPath == "" {
			return nil, fmt.Errorf("telegram login signing manifest key %d is invalid", index)
		}
		if !filepath.IsAbs(keyPath) {
			keyPath = filepath.Join(baseDir, keyPath)
		}
		data, err := readBoundedFile(keyPath, maxTelegramLoginKeyBytes)
		if err != nil {
			return nil, fmt.Errorf("read telegram login signing key %d: %w", index, err)
		}
		var parsed jwk.Key
		if len(bytes.TrimSpace(data)) > 0 && bytes.TrimSpace(data)[0] == '{' {
			parsed, err = jwk.ParseKey(data)
		} else {
			parsed, err = jwk.ParseKey(data, jwk.WithPEM(true))
		}
		if err != nil {
			return nil, fmt.Errorf("parse telegram login signing key %d: %w", index, err)
		}
		var raw any
		if err := jwk.Export(parsed, &raw); err != nil {
			return nil, fmt.Errorf("export telegram login signing key %d: %w", index, err)
		}
		var publishUntil time.Time
		if entry.PublishUntil != "" {
			publishUntil, err = time.Parse(time.RFC3339, entry.PublishUntil)
			if err != nil {
				return nil, fmt.Errorf("parse telegram login signing key %d publish_until: %w", index, err)
			}
		}
		if entry.Active && !publishUntil.IsZero() {
			return nil, fmt.Errorf("active telegram login signing key %d must not set publish_until", index)
		}
		if !entry.Active && publishUntil.IsZero() {
			return nil, fmt.Errorf("retiring telegram login signing key %d requires publish_until", index)
		}
		materials = append(materials, SigningKeyMaterial{
			Algorithm: entry.Algorithm, KeyID: entry.KeyID, PrivateKey: raw,
			Active: entry.Active, PublishUntil: publishUntil,
		})
	}
	return NewSigningKeyRing(materials, now)
}

type codeKeyManifest struct {
	Version int               `json:"version"`
	Active  string            `json:"active"`
	Keys    map[string]string `json:"keys"`
}

func LoadCodeSealer(path string) (*CodeSealer, error) {
	var manifest codeKeyManifest
	if err := readStrictJSONFile(path, maxTelegramLoginManifestBytes, &manifest); err != nil {
		return nil, fmt.Errorf("load telegram login code-key manifest: %w", err)
	}
	if manifest.Version != 1 || manifest.Active == "" || len(manifest.Keys) == 0 || len(manifest.Keys) > 16 {
		return nil, errors.New("telegram login code-key manifest has invalid version or key count")
	}
	keys := make(map[string][]byte, len(manifest.Keys))
	for keyID, encoded := range manifest.Keys {
		if strings.TrimSpace(keyID) == "" || keyID != strings.TrimSpace(keyID) || len(keyID) > 128 {
			return nil, errors.New("telegram login code-key manifest has invalid key id")
		}
		raw, err := decodeBase64Key(encoded)
		if err != nil || len(raw) != 32 {
			return nil, fmt.Errorf("telegram login code-key %q must be 32 base64-encoded bytes", keyID)
		}
		keys[keyID] = raw
	}
	return NewCodeSealer(manifest.Active, keys)
}

func LoadClientSecretPepper(path string) ([]byte, error) {
	data, err := readBoundedFile(path, 4096)
	if err != nil {
		return nil, fmt.Errorf("read telegram login client-secret pepper: %w", err)
	}
	raw, err := decodeBase64Key(strings.TrimSpace(string(data)))
	if err != nil || len(raw) != 32 {
		return nil, errors.New("telegram login client-secret pepper must be 32 base64-encoded bytes")
	}
	return raw, nil
}

func decodeBase64Key(value string) ([]byte, error) {
	value = strings.TrimSpace(value)
	for _, encoding := range []*base64.Encoding{
		base64.RawURLEncoding, base64.URLEncoding, base64.RawStdEncoding, base64.StdEncoding,
	} {
		if raw, err := encoding.DecodeString(value); err == nil {
			return raw, nil
		}
	}
	return nil, errors.New("invalid base64")
}

func readStrictJSONFile(path string, maxBytes int64, target any) error {
	data, err := readBoundedFile(path, maxBytes)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

func readBoundedFile(path string, maxBytes int64) ([]byte, error) {
	if strings.TrimSpace(path) == "" || maxBytes <= 0 {
		return nil, errors.New("invalid file path or size bound")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > maxBytes {
		return nil, errors.New("file is not regular or exceeds size bound")
	}
	return io.ReadAll(io.LimitReader(file, maxBytes+1))
}
