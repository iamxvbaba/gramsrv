// Package procctl backs the admin panel's Restart action (docker restart on
// the running server container, over the host's docker.sock) and safe .env
// viewing/editing with secret-value masking.
//
// Adapted (not copied verbatim, attribution here and in the commit this
// shipped in) from the sibling project owpengram-server
// (github.com/owpengram/owpengram-server, Apache-2.0), which forks the same
// upstream telesrv base as this project. That version manages a bare-process
// + git-pull deployment (PID files, git pull + go build for an Update
// action, a same-process self-restart-avoidance handoff for its own admin
// binary). None of that applies to gramsrv's actual production deploy
// (Docker Compose): Restart here is `docker restart` on a different,
// already-running container, so there is no self-restart problem to hand
// off, no PID state to track, and no git/build step -- Restart only was
// ported, a deliberate scope choice, not an oversight.
//
// The .env-groups reader is also rewritten: the source project's
// .env.example uses a strict "## Title -- description" section-header
// convention this project's .env.example doesn't follow, so grouping here
// is by blank-line-separated blocks instead, and the TELESRV_-only key
// pattern is relaxed to match any UPPER_SNAKE_CASE key (this project's
// .env.example also carries POSTGRES_*/COMPOSE_PROJECT_NAME etc.).
package procctl

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
)

var (
	activeFieldRe    = regexp.MustCompile(`^([A-Z][A-Z0-9_]*)=(.*)$`)
	commentedFieldRe = regexp.MustCompile(`^#\s*([A-Z][A-Z0-9_]*)=(.*)$`)
	sensitiveKeyRe   = regexp.MustCompile(`(PASSWORD|SECRET|_TOKEN|API_KEY)`)
	// sensitiveKeyExceptRe excludes names that trip sensitiveKeyRe on the
	// word "SECRET" while meaning Telegram's secret-chat feature, not a
	// credential (e.g. TELESRV_SECRET_CHAT_DELETE_FILE_AFTER_DOWNLOAD) --
	// there's nothing to mask there, it's a plain boolean toggle.
	sensitiveKeyExceptRe = regexp.MustCompile(`SECRET_CHAT`)
)

// EnvField is one key's current state, ready for the panel to render (and,
// for Sensitive keys, mask by default).
type EnvField struct {
	Key          string `json:"key"`
	DefaultValue string `json:"default_value"`
	Sensitive    bool   `json:"sensitive"`
	// Value is the field's current effective value: from .env when set,
	// otherwise DefaultValue.
	Value string `json:"value"`
	// Enabled reports whether Key is an active (non-commented) line in
	// .env.example, or is set in the live .env even if the template has it
	// commented out.
	Enabled bool `json:"enabled"`
}

// EnvGroup is one blank-line-separated block from .env.example, titled by
// whatever comment lines immediately preceded it (joined with a space; may
// be empty).
type EnvGroup struct {
	Title  string     `json:"title"`
	Fields []EnvField `json:"fields"`
}

// Manager operates on one deployment's env files and one Docker container.
// All methods are safe for concurrent use.
type Manager struct {
	envPath        string
	envExamplePath string
	containerName  string
	dockerBin      string // "docker" in production; overridable in tests
}

// NewManager builds a Manager. envPath/envExamplePath are the deployment's
// .env and .env.example files (typically bind-mounted into the admin
// container -- see deploy/docker/compose.yaml); containerName is the
// server container `docker restart` targets (e.g. "gramsrv-main-server-1").
func NewManager(envPath, envExamplePath, containerName string) *Manager {
	return &Manager{envPath: envPath, envExamplePath: envExamplePath, containerName: containerName, dockerBin: "docker"}
}

// ContainerStatus is docker inspect's state/health, projected down to what
// the panel needs.
type ContainerStatus struct {
	Name    string `json:"name"`
	Running bool   `json:"running"`
	// Health is "healthy"/"unhealthy"/"starting", or empty when the
	// container has no HEALTHCHECK.
	Health string `json:"health,omitempty"`
	// Status is docker's raw state string (running/exited/restarting/...).
	Status string `json:"status"`
}

// Status reports the server container's current state.
func (m *Manager) Status(ctx context.Context) (ContainerStatus, error) {
	out, err := m.docker(ctx, "inspect", "--format",
		"{{.State.Status}}|{{if .State.Health}}{{.State.Health.Status}}{{end}}", m.containerName)
	if err != nil {
		return ContainerStatus{Name: m.containerName}, err
	}
	parts := strings.SplitN(strings.TrimSpace(out), "|", 2)
	st := ContainerStatus{Name: m.containerName, Status: parts[0], Running: parts[0] == "running"}
	if len(parts) > 1 {
		st.Health = parts[1]
	}
	return st, nil
}

// Restart runs `docker restart` on the server container. The caller should
// bound ctx with a generous timeout -- a graceful stop can take as long as
// the container's configured stop_grace_period before Docker escalates to
// SIGKILL.
func (m *Manager) Restart(ctx context.Context) (string, error) {
	return m.docker(ctx, "restart", m.containerName)
}

func (m *Manager) docker(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, m.dockerBin, args...)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	if err := cmd.Run(); err != nil {
		return buf.String(), fmt.Errorf("procctl: docker %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(buf.String()))
	}
	return buf.String(), nil
}

// --- .env / .env.example editing --------------------------------------

// ReadEnvGroups parses .env.example into panel-visible groups (blank-line
// blocks, titled by their preceding comment), then fills each field's
// current effective value from .env. A missing .env.example is not an
// error -- it just means nothing to show (nil, nil).
func (m *Manager) ReadEnvGroups() ([]EnvGroup, error) {
	tmplData, err := os.ReadFile(m.envExamplePath)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("procctl: read env example: %w", err)
	}
	envValues, err := m.readEnvFile()
	if err != nil {
		return nil, err
	}

	var groups []EnvGroup
	var current *EnvGroup
	var pending []string
	seen := map[string]bool{}

	appendField := func(key, defaultValue string, templateEnabled bool) {
		if seen[key] {
			return
		}
		if current == nil {
			groups = append(groups, EnvGroup{Title: strings.Join(pending, " ")})
			current = &groups[len(groups)-1]
		}
		seen[key] = true
		value, has := envValues[key]
		if !has {
			value = defaultValue
		}
		current.Fields = append(current.Fields, EnvField{
			Key:          key,
			DefaultValue: defaultValue,
			Sensitive:    sensitiveKeyRe.MatchString(key) && !sensitiveKeyExceptRe.MatchString(key),
			Value:        value,
			Enabled:      has || templateEnabled,
		})
	}

	for _, raw := range strings.Split(string(tmplData), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			// A field under the current group closes it once a blank line
			// is hit; a fresh comment run afterward starts the next group's
			// title. Groups with zero fields (a comment block nothing
			// followed) are simply never created -- appendField is what
			// allocates `current`, and it's never called for them.
			current = nil
			pending = nil
			continue
		}
		if a := activeFieldRe.FindStringSubmatch(line); a != nil {
			appendField(a[1], a[2], true)
			continue
		}
		if strings.HasPrefix(line, "#") {
			if c := commentedFieldRe.FindStringSubmatch(line); c != nil {
				appendField(c[1], c[2], false)
				continue
			}
			pending = append(pending, strings.TrimSpace(strings.TrimLeft(line, "#")))
			continue
		}
	}
	return groups, nil
}

func (m *Manager) readEnvFile() (map[string]string, error) {
	values := map[string]string{}
	data, err := os.ReadFile(m.envPath)
	if os.IsNotExist(err) {
		return values, nil
	}
	if err != nil {
		return nil, fmt.Errorf("procctl: read .env: %w", err)
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

// WriteEnvValues rewrites .env from .env.example's exact text, substituting
// each given key's value in place -- preserves comments/layout and, for
// every key *not* in values, whatever is already in the current .env
// (falling back to the template's own default only for a key .env never
// set at all). A naive fresh key=value dump would silently wipe out every
// customized setting (e.g. TELESRV_ADMIN_UI_PASSWORD) not present in this
// particular save's payload -- this does not do that.
func (m *Manager) WriteEnvValues(values map[string]string) error {
	tmplData, err := os.ReadFile(m.envExamplePath)
	if err != nil {
		return fmt.Errorf("procctl: read env example: %w", err)
	}
	existing, err := m.readEnvFile()
	if err != nil {
		return err
	}
	lines := strings.Split(string(tmplData), "\n")
	// Split() on a trailing "\n" leaves one empty trailing element; drop it
	// so the join below doesn't add a spurious blank line before the final
	// newline this function appends anyway.
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
			// A previously-enabled optional field shows up in the current
			// .env as an active line even though the template still has it
			// commented out -- keep it enabled with its existing value.
			if v, ok := existing[c[1]]; ok {
				seen[c[1]] = true
				out = append(out, c[1]+"="+v)
				continue
			}
		}
		out = append(out, raw)
	}
	return os.WriteFile(m.envPath, []byte(strings.Join(out, "\n")+"\n"), 0o600)
}
