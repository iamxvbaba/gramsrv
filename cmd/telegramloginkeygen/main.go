// Command telegramloginkeygen initializes and rotates telesrv Telegram Login
// key files without ever writing secret material to stdout.
package main

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	telegramlogin "telesrv/internal/app/telegramlogin"
	"telesrv/internal/domain"
)

const maxManifestBytes = 1 << 20
const signingRetirementMargin = 10 * time.Minute

type signingManifest struct {
	Version int                  `json:"version"`
	Keys    []signingManifestKey `json:"keys"`
}

type signingManifestKey struct {
	Algorithm      domain.TelegramLoginSigningAlgorithm `json:"algorithm"`
	KeyID          string                               `json:"kid"`
	PrivateKeyFile string                               `json:"private_key_file"`
	Active         bool                                 `json:"active"`
	PublishUntil   string                               `json:"publish_until,omitempty"`
}

type codeManifest struct {
	Version int               `json:"version"`
	Active  string            `json:"active"`
	Keys    map[string]string `json:"keys"`
}

type options struct {
	mode       string
	dir        string
	algorithm  domain.TelegramLoginSigningAlgorithm
	publishFor time.Duration
	idTokenTTL time.Duration
	now        func() time.Time
}

func main() {
	mode := flag.String("mode", "init", "init, rotate-signing, or rotate-code")
	dir := flag.String("dir", "data/telegram-login", "key directory")
	algorithm := flag.String("algorithm", "RS256", "signing algorithm to rotate: RS256, ES256, or EdDSA")
	publishFor := flag.Duration("publish-for", 2*time.Hour, "how long the retiring public key remains in JWKS")
	idTokenTTL := flag.Duration("id-token-ttl", time.Hour, "configured TELESRV_TELEGRAM_LOGIN_ID_TOKEN_TTL")
	flag.Parse()
	if flag.NArg() != 0 {
		fatal(errors.New("positional arguments are not accepted"))
	}
	opts := options{
		mode: strings.ToLower(strings.TrimSpace(*mode)), dir: strings.TrimSpace(*dir),
		algorithm:  domain.TelegramLoginSigningAlgorithm(strings.ToUpper(strings.TrimSpace(*algorithm))),
		publishFor: *publishFor, idTokenTTL: *idTokenTTL, now: time.Now,
	}
	if err := run(opts); err != nil {
		fatal(err)
	}
	fmt.Printf("Telegram Login key operation %s completed in %s; restart all instances to load one consistent key ring.\n", opts.mode, opts.dir)
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "telegramloginkeygen:", err)
	os.Exit(1)
}

func run(opts options) error {
	if opts.now == nil {
		opts.now = time.Now
	}
	if opts.dir == "" {
		return errors.New("key directory is required")
	}
	absDir, err := filepath.Abs(opts.dir)
	if err != nil {
		return fmt.Errorf("resolve key directory: %w", err)
	}
	if err := os.MkdirAll(absDir, 0o700); err != nil {
		return fmt.Errorf("create key directory: %w", err)
	}
	if err := os.Chmod(absDir, 0o700); err != nil {
		return fmt.Errorf("restrict key directory: %w", err)
	}
	lockPath := filepath.Join(absDir, ".keygen.lock")
	lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("acquire key operation lock: %w", err)
	}
	_ = lock.Close()
	defer func() { _ = os.Remove(lockPath) }()

	switch opts.mode {
	case "init":
		return initialize(absDir, opts.now().UTC())
	case "rotate-signing":
		if opts.idTokenTTL < time.Minute || opts.idTokenTTL > 24*time.Hour {
			return errors.New("id-token-ttl must match the configured 1m..24h ID-token TTL")
		}
		if opts.publishFor < opts.idTokenTTL+signingRetirementMargin || opts.publishFor > 90*24*time.Hour {
			return fmt.Errorf("publish-for must be at least id-token-ttl plus %s and at most 2160h", signingRetirementMargin)
		}
		return rotateSigning(absDir, opts.algorithm, opts.publishFor, opts.now().UTC())
	case "rotate-code":
		return rotateCode(absDir, opts.now().UTC())
	default:
		return errors.New("mode must be init, rotate-signing, or rotate-code")
	}
}

func initialize(dir string, now time.Time) error {
	for _, name := range []string{"signing-keys.json", "code-keys.json", "client-secret-pepper"} {
		if _, err := os.Lstat(filepath.Join(dir, name)); err == nil {
			return fmt.Errorf("refusing to overwrite existing %s", name)
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("inspect %s: %w", name, err)
		}
	}

	manifest := signingManifest{Version: 1}
	for _, algorithm := range []domain.TelegramLoginSigningAlgorithm{
		domain.TelegramLoginSigningRS256,
		domain.TelegramLoginSigningES256,
		domain.TelegramLoginSigningEdDSA,
	} {
		entry, err := generateSigningKey(dir, algorithm, now)
		if err != nil {
			return err
		}
		manifest.Keys = append(manifest.Keys, entry)
	}
	if err := writeSigningManifest(dir, manifest); err != nil {
		return err
	}
	codeID, err := newKeyID("code", now)
	if err != nil {
		return err
	}
	codeKey, err := randomBytes(32)
	if err != nil {
		return err
	}
	if err := writeCodeManifest(dir, codeManifest{
		Version: 1, Active: codeID,
		Keys: map[string]string{codeID: base64.RawURLEncoding.EncodeToString(codeKey)},
	}); err != nil {
		return err
	}
	pepper, err := randomBytes(32)
	if err != nil {
		return err
	}
	if err := writeExclusive(filepath.Join(dir, "client-secret-pepper"), []byte(base64.RawURLEncoding.EncodeToString(pepper)+"\n")); err != nil {
		return fmt.Errorf("write client-secret pepper: %w", err)
	}
	_, err = telegramlogin.LoadClientSecretPepper(filepath.Join(dir, "client-secret-pepper"))
	return err
}

func rotateSigning(dir string, algorithm domain.TelegramLoginSigningAlgorithm, publishFor time.Duration, now time.Time) error {
	if algorithm != domain.TelegramLoginSigningRS256 && algorithm != domain.TelegramLoginSigningES256 && algorithm != domain.TelegramLoginSigningEdDSA {
		return errors.New("default keygen supports RS256, ES256, and EdDSA; ES256K requires an explicit jwx_es256k build and external JWK lifecycle")
	}
	path := filepath.Join(dir, "signing-keys.json")
	var manifest signingManifest
	if err := readStrictJSON(path, &manifest); err != nil {
		return fmt.Errorf("read signing manifest: %w", err)
	}
	if manifest.Version != 1 || len(manifest.Keys) == 0 || len(manifest.Keys) >= 32 {
		return errors.New("signing manifest version or key count is invalid")
	}
	foundActive := false
	kept := make([]signingManifestKey, 0, len(manifest.Keys)+1)
	for _, key := range manifest.Keys {
		if !key.Active && key.PublishUntil != "" {
			until, err := time.Parse(time.RFC3339, key.PublishUntil)
			if err != nil {
				return fmt.Errorf("parse retiring key %s: %w", key.KeyID, err)
			}
			if !now.Before(until) {
				continue
			}
		}
		if key.Algorithm == algorithm && key.Active {
			if foundActive {
				return fmt.Errorf("multiple active %s keys", algorithm)
			}
			foundActive = true
			key.Active = false
			key.PublishUntil = now.Add(publishFor).UTC().Format(time.RFC3339)
		}
		kept = append(kept, key)
	}
	if !foundActive {
		return fmt.Errorf("no active %s key to rotate", algorithm)
	}
	entry, err := generateSigningKey(dir, algorithm, now)
	if err != nil {
		return err
	}
	manifest.Keys = append(kept, entry)
	return writeSigningManifest(dir, manifest)
}

func rotateCode(dir string, now time.Time) error {
	path := filepath.Join(dir, "code-keys.json")
	var manifest codeManifest
	if err := readStrictJSON(path, &manifest); err != nil {
		return fmt.Errorf("read code-key manifest: %w", err)
	}
	if manifest.Version != 1 || manifest.Active == "" || len(manifest.Keys) == 0 || len(manifest.Keys) >= 16 {
		return errors.New("code-key manifest is invalid or at its 16-key safety limit")
	}
	keyID, err := newKeyID("code", now)
	if err != nil {
		return err
	}
	key, err := randomBytes(32)
	if err != nil {
		return err
	}
	manifest.Active = keyID
	manifest.Keys[keyID] = base64.RawURLEncoding.EncodeToString(key)
	return writeCodeManifest(dir, manifest)
}

func generateSigningKey(dir string, algorithm domain.TelegramLoginSigningAlgorithm, now time.Time) (signingManifestKey, error) {
	keyID, err := newKeyID(strings.ToLower(string(algorithm)), now)
	if err != nil {
		return signingManifestKey{}, err
	}
	var privateKey any
	switch algorithm {
	case domain.TelegramLoginSigningRS256:
		privateKey, err = rsa.GenerateKey(rand.Reader, 3072)
	case domain.TelegramLoginSigningES256:
		privateKey, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	case domain.TelegramLoginSigningEdDSA:
		_, privateKey, err = ed25519.GenerateKey(rand.Reader)
	default:
		return signingManifestKey{}, fmt.Errorf("unsupported keygen algorithm %s", algorithm)
	}
	if err != nil {
		return signingManifestKey{}, fmt.Errorf("generate %s key: %w", algorithm, err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		return signingManifestKey{}, fmt.Errorf("marshal %s key: %w", algorithm, err)
	}
	filename := "signing-" + keyID + ".pem"
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	if err := writeExclusive(filepath.Join(dir, filename), pemBytes); err != nil {
		return signingManifestKey{}, fmt.Errorf("write %s key: %w", algorithm, err)
	}
	return signingManifestKey{
		Algorithm: algorithm, KeyID: keyID, PrivateKeyFile: filename, Active: true,
	}, nil
}

func newKeyID(prefix string, now time.Time) (string, error) {
	raw, err := randomBytes(8)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s-%s-%s", prefix, now.UTC().Format("20060102T150405Z"), base64.RawURLEncoding.EncodeToString(raw)), nil
}

func randomBytes(size int) ([]byte, error) {
	raw := make([]byte, size)
	if _, err := rand.Read(raw); err != nil {
		return nil, fmt.Errorf("read cryptographic randomness: %w", err)
	}
	return raw, nil
}

func writeSigningManifest(dir string, manifest signingManifest) error {
	return writeValidatedManifest(filepath.Join(dir, "signing-keys.json"), manifest, func(path string) error {
		_, err := telegramlogin.LoadSigningKeyRing(path, time.Now)
		return err
	})
}

func writeCodeManifest(dir string, manifest codeManifest) error {
	return writeValidatedManifest(filepath.Join(dir, "code-keys.json"), manifest, func(path string) error {
		_, err := telegramlogin.LoadCodeSealer(path)
		return err
	})
}

func writeValidatedManifest(path string, value any, validate func(string) error) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal manifest: %w", err)
	}
	data = append(data, '\n')
	temp, err := os.CreateTemp(filepath.Dir(path), ".telegram-login-manifest-*")
	if err != nil {
		return fmt.Errorf("create temporary manifest: %w", err)
	}
	tempPath := temp.Name()
	defer func() { _ = os.Remove(tempPath) }()
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return err
	}
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := validate(tempPath); err != nil {
		return fmt.Errorf("validate generated manifest: %w", err)
	}
	if err := os.Rename(tempPath, path); err != nil {
		return fmt.Errorf("atomically replace manifest: %w", err)
	}
	return nil
}

func writeExclusive(path string, data []byte) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		_ = file.Close()
		if !ok {
			_ = os.Remove(path)
		}
	}()
	if _, err := file.Write(data); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	ok = true
	return nil
}

func readStrictJSON(path string, target any) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() > maxManifestBytes {
		return errors.New("manifest must be a bounded regular file")
	}
	decoder := json.NewDecoder(io.LimitReader(file, maxManifestBytes+1))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("manifest contains multiple JSON values")
		}
		return err
	}
	return nil
}
