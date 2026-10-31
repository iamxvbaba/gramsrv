// Package procctl provides the .env parsing and rewriting needed by the admin web
// panel's "Environment (.env)" editor. It implements only the subset this repo needs:
// reading editable field groups from .env.example (ReadEnvGroups) and writing values
// saved by the panel back into .env (WriteEnvValues) -- it does not include the heavier
// features of process management (start/stop/restart/update), PID state files, git
// operations or docker probing.
//
// The conventions match the scripts/panel: .env.example splits groups with `## Title --
// description` lines, breaks sections with `# ====...` lines, and the `#` comment block
// immediately preceding a field acts as its description; a `TELESRV_*` field line is
// either active (`K=V`) or commented out (`# K=V`). The foundation of writing back is
// "use .env.example as the base and replace the values of known keys in place", so
// comments, layout and untouched keys all survive -- never re-dump key/value pairs.
package procctl

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var (
	activeFieldRe    = regexp.MustCompile(`^(TELESRV_[A-Z0-9_]+)=(.*)$`)
	commentedFieldRe = regexp.MustCompile(`^#\s*(TELESRV_[A-Z0-9_]+)=(.*)$`)
	sensitiveKeyRe   = regexp.MustCompile(`(PASSWORD|SECRET|_TOKEN|API_KEY)`)
	// sensitiveKeyExceptRe excludes keys where the sensitive-word match "SECRET" actually
	// refers to Telegram's secret-chat feature rather than a credential (e.g.
	// TELESRV_SECRET_CHAT_DELETE_FILE_AFTER_DOWNLOAD): there is nothing to mask there,
	// it is just an ordinary boolean switch.
	sensitiveKeyExceptRe = regexp.MustCompile(`SECRET_CHAT`)
	groupHeaderRe        = regexp.MustCompile(`^##\s*(.+?)\s*--\s*(.+)$`)
	sectionBreakRe       = regexp.MustCompile(`^#\s*={10,}\s*$`)
)

// EnvField is the presentation of a single editable environment variable in the panel:
// default value, description, whether it is enabled by default, whether it is sensitive
// (its value should be masked in the UI), and the currently effective value.
type EnvField struct {
	Key              string `json:"key"`
	DefaultValue     string `json:"default_value"`
	Description      string `json:"description"`
	EnabledByDefault bool   `json:"enabled_by_default"`
	Sensitive        bool   `json:"sensitive"`
	// Value is the field's currently effective value: .env if it is set there, otherwise
	// the field's default value (only when EnabledByDefault), and empty otherwise.
	Value string `json:"value"`
}

// EnvGroup is a field group, corresponding to a `## Title -- description` block in
// .env.example.
type EnvGroup struct {
	Title       string     `json:"title"`
	Description string     `json:"description"`
	Fields      []EnvField `json:"fields"`
}

// Manager works against the .env and .env.example under the repo root (Root).
type Manager struct {
	Root string
}

func NewManager(root string) *Manager {
	return &Manager{Root: root}
}

// ReadEnvGroups parses .env.example into the panel-visible groups (the header/format
// rules match server-panel.py's parse_env_template()), then back-fills each field's
// effective value from the current values in .env. A missing template file returns no
// groups (without an error).
func (m *Manager) ReadEnvGroups() ([]EnvGroup, error) {
	tmplPath := filepath.Join(m.Root, ".env.example")
	tmplData, err := os.ReadFile(tmplPath)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read .env.example: %w", err)
	}
	envValues, err := m.readEnvFile()
	if err != nil {
		return nil, err
	}

	var groups []EnvGroup
	var current *EnvGroup
	var pending []string
	inCommentRun := false
	seen := map[string]bool{}

	appendField := func(key, defaultValue, description string, enabledByDefault bool) {
		if current == nil || seen[key] {
			return
		}
		seen[key] = true
		value, has := envValues[key]
		if !has {
			if enabledByDefault {
				value = defaultValue
			} else {
				value = ""
			}
		}
		current.Fields = append(current.Fields, EnvField{
			Key:              key,
			DefaultValue:     defaultValue,
			Description:      description,
			EnabledByDefault: enabledByDefault,
			Sensitive:        sensitiveKeyRe.MatchString(key) && !sensitiveKeyExceptRe.MatchString(key),
			Value:            value,
		})
	}

	for _, raw := range strings.Split(string(tmplData), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			pending = nil
			inCommentRun = false
			continue
		}
		if h := groupHeaderRe.FindStringSubmatch(line); h != nil {
			groups = append(groups, EnvGroup{Title: strings.TrimSpace(h[1]), Description: strings.TrimSpace(h[2])})
			current = &groups[len(groups)-1]
			pending = nil
			inCommentRun = false
			continue
		}
		if sectionBreakRe.MatchString(line) {
			current = nil
			pending = nil
			inCommentRun = false
			continue
		}
		if a := activeFieldRe.FindStringSubmatch(line); a != nil {
			appendField(a[1], a[2], strings.Join(pending, " "), true)
			inCommentRun = false
			continue
		}
		if strings.HasPrefix(line, "#") {
			if c := commentedFieldRe.FindStringSubmatch(line); c != nil {
				appendField(c[1], c[2], strings.Join(pending, " "), false)
				inCommentRun = false
				continue
			}
			text := strings.TrimSpace(strings.TrimLeft(line, "#"))
			if inCommentRun {
				pending = append(pending, text)
			} else {
				pending = []string{text}
			}
			inCommentRun = true
			continue
		}
		inCommentRun = false
	}

	out := groups[:0]
	for _, g := range groups {
		if len(g.Fields) > 0 {
			out = append(out, g)
		}
	}
	return out, nil
}

func (m *Manager) readEnvFile() (map[string]string, error) {
	values := map[string]string{}
	data, err := os.ReadFile(filepath.Join(m.Root, ".env"))
	if os.IsNotExist(err) {
		return values, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read .env: %w", err)
	}
	for _, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		idx := strings.IndexByte(line, '=')
		if idx <= 0 {
			continue
		}
		values[line[:idx]] = strings.TrimSpace(line[idx+1:])
	}
	return values, nil
}

// WriteEnvValues writes back to .env using .env.example's text as the base and
// replacing the values of each known key in place -- so comments and layout survive.
// Only keys present in values are rewritten; every other key keeps its current .env
// value (only keys .env never set fall back to the template's own default) -- an
// earlier version fell back to the template default for any key absent from the save
// payload, so a save touching only one group silently wiped other customized settings
// (e.g. TELESRV_ADMIN_UI_PASSWORD). For an optional field commented out in the
// template: a non-empty value uncomments it and writes it, while an empty value leaves
// the commented line as-is.
func (m *Manager) WriteEnvValues(values map[string]string) error {
	tmplPath := filepath.Join(m.Root, ".env.example")
	tmplData, err := os.ReadFile(tmplPath)
	if err != nil {
		return fmt.Errorf("read .env.example: %w", err)
	}
	existing, err := m.readEnvFile()
	if err != nil {
		return err
	}
	lines := strings.Split(string(tmplData), "\n")
	// Split() leaves an empty trailing element for a final "\n"; drop it so the join
	// below doesn't add an extra blank line at the end (this function already appends a
	// newline at the end).
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	out := make([]string, 0, len(lines))
	seen := map[string]bool{}
	for _, raw := range lines {
		line := strings.TrimSpace(raw)
		if a := activeFieldRe.FindStringSubmatch(line); a != nil && !seen[a[1]] {
			if v, ok := values[a[1]]; ok {
				seen[a[1]] = true
				out = append(out, a[1]+"="+v)
				continue
			}
			if v, ok := existing[a[1]]; ok {
				seen[a[1]] = true
				out = append(out, a[1]+"="+v)
				continue
			}
		}
		if c := commentedFieldRe.FindStringSubmatch(line); c != nil && !seen[c[1]] {
			if v, ok := values[c[1]]; ok {
				seen[c[1]] = true
				if v != "" {
					out = append(out, c[1]+"="+v)
				} else {
					out = append(out, raw)
				}
				continue
			}
			// An optional field enabled before: the template still has it commented, but
			// the current .env already has an active line -- keep it enabled and reuse its
			// current value.
			if v, ok := existing[c[1]]; ok {
				seen[c[1]] = true
				out = append(out, c[1]+"="+v)
				continue
			}
		}
		out = append(out, raw)
	}
	return os.WriteFile(filepath.Join(m.Root, ".env"), []byte(strings.Join(out, "\n")+"\n"), 0o644)
}
