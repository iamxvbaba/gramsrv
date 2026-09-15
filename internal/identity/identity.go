// Package identity stores the admin-editable server name/description/icon
// shown to clients. It is deliberately not part of internal/config's
// Config: config is loaded once at process start from .env, while identity
// is meant to be edited from the admin web panel and take effect
// immediately, with no server restart -- so it lives as plain files on
// disk, read fresh on every request instead of cached in memory.
//
// Ported (adapted, not copied) from the sibling project owpengram-server
// (github.com/owpengram/owpengram-server, Apache-2.0), which forks the same
// upstream telesrv base as this project -- that fork's internal/identity
// covers welcome-message/login-code template overrides and a first-run
// setup wizard too; this port is deliberately scoped to just name/
// description/icon, the part gramsrv actually asked for. gramsrv has no
// message-template-override system or setup wizard to hang the rest on.
package identity

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	metaFileName = "identity.json"
	iconBaseName = "icon"
)

// Info is the editable identity shown to clients.
type Info struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	// IconExt is the icon file's extension (e.g. ".png"), empty when no
	// icon has been uploaded. Kept alongside Name/Description so Store can
	// find the icon file without a directory listing.
	IconExt string `json:"icon_ext,omitempty"`
}

// Store reads/writes Info and the icon file under a directory (typically
// Config.IdentityDir). All methods are safe to call from multiple goroutines
// and multiple processes (the admin binary writes, the main server binary
// reads) -- writes are atomic via a temp file + rename.
type Store struct {
	dir string
}

func NewStore(dir string) *Store {
	return &Store{dir: dir}
}

func (s *Store) metaPath() string {
	return filepath.Join(s.dir, metaFileName)
}

func (s *Store) iconPath(ext string) string {
	return filepath.Join(s.dir, iconBaseName+ext)
}

// Get reads the current identity. A missing file is not an error -- it just
// means nothing has been configured yet, so Info{} (all empty) is returned.
func (s *Store) Get() (Info, error) {
	data, err := os.ReadFile(s.metaPath())
	if os.IsNotExist(err) {
		return Info{}, nil
	}
	if err != nil {
		return Info{}, fmt.Errorf("identity: read: %w", err)
	}
	var info Info
	if err := json.Unmarshal(data, &info); err != nil {
		return Info{}, fmt.Errorf("identity: decode: %w", err)
	}
	return info, nil
}

// SetText updates name/description, preserving whatever icon is already
// configured.
func (s *Store) SetText(name, description string) error {
	info, err := s.Get()
	if err != nil {
		return err
	}
	info.Name = strings.TrimSpace(name)
	info.Description = strings.TrimSpace(description)
	return s.save(info)
}

// SetIcon replaces the icon file (removing any previous one under a
// different extension) and records its extension in identity.json.
// ext must include the leading dot (e.g. ".png").
func (s *Store) SetIcon(data []byte, ext string) error {
	info, err := s.Get()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return fmt.Errorf("identity: mkdir: %w", err)
	}
	if info.IconExt != "" && info.IconExt != ext {
		_ = os.Remove(s.iconPath(info.IconExt))
	}
	if err := writeFileAtomic(s.iconPath(ext), data, 0o644); err != nil {
		return fmt.Errorf("identity: write icon: %w", err)
	}
	info.IconExt = ext
	return s.save(info)
}

// RemoveIcon deletes the configured icon, if any.
func (s *Store) RemoveIcon() error {
	info, err := s.Get()
	if err != nil {
		return err
	}
	if info.IconExt == "" {
		return nil
	}
	_ = os.Remove(s.iconPath(info.IconExt))
	info.IconExt = ""
	return s.save(info)
}

// Icon returns the icon's raw bytes and its file extension, or ("", nil,
// false) when no icon is configured.
func (s *Store) Icon() (data []byte, ext string, ok bool) {
	info, err := s.Get()
	if err != nil || info.IconExt == "" {
		return nil, "", false
	}
	raw, err := os.ReadFile(s.iconPath(info.IconExt))
	if err != nil {
		return nil, "", false
	}
	return raw, info.IconExt, true
}

func (s *Store) save(info Info) error {
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return fmt.Errorf("identity: mkdir: %w", err)
	}
	data, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		return fmt.Errorf("identity: encode: %w", err)
	}
	if err := writeFileAtomic(s.metaPath(), data, 0o644); err != nil {
		return fmt.Errorf("identity: write: %w", err)
	}
	return nil
}

func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, perm); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
