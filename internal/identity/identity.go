// Package identity stores the server identity an operator can edit in the admin
// panel's Server Settings -> Server identity: name, description, icon, and the
// overridable templates for login notifications (login-code delivery and the
// welcome message sent after every successful login). It deliberately is not part
// of internal/config's Config: config is loaded from .env exactly once at process
// startup, whereas identity is edited by the admin panel at any time and takes
// effect on the main server immediately, with no restart -- so it lives on disk as
// plain text files (the directory is given by Config.IdentityDir) and is read
// fresh on every access rather than cached in memory.
//
// Two processes cooperate: cmd/telesrv-admin (the admin binary) writes, cmd/telesrv
// (the main server binary) reads. Both reads and writes are atomic (temp file +
// rename) and safe for concurrent use.
package identity

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	metaFileName = "identity.json"
	iconBaseName = "icon"
)

// Info is the editable server identity shown to clients. Name affects the 777000
// display name via domain.SetOfficialSystemUserDisplayName (applied once at startup,
// after which internal/app/systemidentity.Watcher keeps following it during runtime
// by polling this file), while template overrides are read live each time a login
// notification is sent.
type Info struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	// IconExt is the icon file's extension (e.g. ".png"), empty when no icon has been
	// uploaded. It is stored alongside Name/Description so the Store can locate the
	// icon file without scanning the directory.
	IconExt string `json:"icon_ext,omitempty"`
	// WelcomeMessagePhoneTemplate/WelcomeMessageEmailTemplate are the admin panel's raw
	// overrides for the welcome message after a phone/email login succeeds (sent by the
	// official system account 777000) -- corresponding to
	// domain.ResolveWelcomeMessageTemplate. Empty means "not configured": the resolver
	// falls through to the TELESRV_WELCOME_MESSAGE_* environment variables and then to
	// the compile-time built-in text. Raw values are stored on purpose (rather than
	// pre-resolved results), so a deployment that never touched the panel always
	// follows the latest value of the fallback chain (including future tweaks to the
	// built-in text).
	WelcomeMessagePhoneTemplate string `json:"welcome_message_phone_template,omitempty"`
	WelcomeMessageEmailTemplate string `json:"welcome_message_email_template,omitempty"`
	// LoginCodeMessageTemplate is the admin panel's raw override for the 777000 login-code
	// delivery message (see domain.ResolveLoginCodeMessageTemplate). Unlike the welcome
	// messages it is singular -- the message does not vary by delivery channel. Empty
	// means "not configured", with the same semantics as above.
	LoginCodeMessageTemplate string `json:"login_code_message_template,omitempty"`
}

// Store reads and writes Info and the icon file under the given directory (usually
// Config.IdentityDir). All methods may be called concurrently from multiple
// goroutines and across processes (the admin binary writes, the main server binary
// reads).
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

// Get reads the current identity. A missing file is not an error -- it just means
// it was never configured -- and returns Info{} (all empty).
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

// SetText updates the name and description, preserving any configured icon and
// template overrides.
func (s *Store) SetText(name, description string) error {
	info, err := s.Get()
	if err != nil {
		return err
	}
	info.Name = strings.TrimSpace(name)
	info.Description = strings.TrimSpace(description)
	return s.save(info)
}

// SetWelcomeMessageTemplates updates the template overrides for the post-login welcome
// messages, preserving the other identity fields. An empty string for either argument
// clears that channel's override (falling back to the environment variable / built-in
// text, following the "empty = unset" convention used throughout Info), and the two
// are independent.
func (s *Store) SetWelcomeMessageTemplates(phone, email string) error {
	info, err := s.Get()
	if err != nil {
		return err
	}
	info.WelcomeMessagePhoneTemplate = strings.TrimSpace(phone)
	info.WelcomeMessageEmailTemplate = strings.TrimSpace(email)
	return s.save(info)
}

// SetLoginCodeMessageTemplate updates the admin panel override for the login-code
// delivery message, preserving the other identity fields. An empty string clears the
// override (see the Info field comment for the fallback chain). The login-code template
// is channel-independent and singular.
//
// The caller must validate the template with domain.ValidateLoginCodeMessageTemplate
// first -- this method does not reject templates missing the {{code}} placeholder by
// itself, because internal/identity does not depend on internal/domain (see the package
// comment).
func (s *Store) SetLoginCodeMessageTemplate(template string) error {
	info, err := s.Get()
	if err != nil {
		return err
	}
	info.LoginCodeMessageTemplate = strings.TrimSpace(template)
	return s.save(info)
}

// SetIcon replaces the icon file (removing any existing icon under the old extension)
// and writes the extension back to identity.json. ext must include the leading dot
// (e.g. ".png").
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

// Icon returns the icon's raw bytes and extension; returns ("", nil, false) when no
// icon is configured.
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

// IconFingerprint returns a short string that changes only with the "icon content", so
// the main server can poll to tell whether the operator added/replaced/removed the
// Server identity icon. Overwriting an upload under the same extension is distinguished
// by the icon's mtime+size; an unconfigured icon returns ""; when the icon file cannot
// be read it degrades to the extension. Name changes are compared directly by the
// caller against Info.Name and need not be mixed in here.
func (s *Store) IconFingerprint() (string, error) {
	info, err := s.Get()
	if err != nil {
		return "", err
	}
	if info.IconExt == "" {
		return "", nil
	}
	if st, statErr := os.Stat(s.iconPath(info.IconExt)); statErr == nil {
		return fmt.Sprintf("%s:%d:%d", info.IconExt, st.ModTime().UnixNano(), st.Size()), nil
	}
	return info.IconExt, nil
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
	return renameWithRetry(tmp, path)
}

// renameWithRetry attempts to atomically replace the target file. On Windows the
// replacement cannot complete while the target file is open for reading by another
// process/goroutine (MoveFileEx reports a sharing violation / access denied); the main
// server continuously polls and reads identity.json, so every save from the admin panel
// can hit this window. The reader holds the handle only briefly during ReadFile, so a
// short retry completes the replacement -- no cross-platform locking is needed.
func renameWithRetry(oldpath, newpath string) error {
	var err error
	for attempt := 0; attempt < 20; attempt++ {
		if err = os.Rename(oldpath, newpath); err == nil {
			return nil
		}
		time.Sleep(5 * time.Millisecond)
	}
	return err
}
