package config

// This file performs a startup-time comparison of .env against .env.example (the
// config drift report). It is read-only and never writes back:
//
//   - Missing (.env.example has it, neither .env nor the process environment does):
//     these settings are currently running on the code defaults. Most are intentional
//     (optional items are simply left unset), but omissions like "a new version added
//     a setting while .env stayed old" hide here too, so this is only reported at INFO.
//   - Unknown (.env has it, .env.example doesn't): almost always a key typo or a stale
//     template -- readEnvFile accepts any TELESRV_* key name, while Load only queries
//     keys it knows, so a typo'd value is silently ignored and the service starts on
//     defaults as usual. This is reported at WARN.
//
// The comparison is called after config.Load succeeds (cmd/telesrv/main.go), so .env
// itself has already passed readEnvFile's validation. .env is never rewritten
// automatically: the container uses a read-only rootfs, and production config often
// comes from the process environment rather than a file; silently changing the
// operator's config file is worse than under-reporting.

import (
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
)

var (
	// driftActiveKeyRe / driftCommentedKeyRe are equivalent to procctl's field regexes:
	// a field line in the template is either active `K=V` or a commented-out optional
	// field `# K=V`.
	driftActiveKeyRe    = regexp.MustCompile(`^(?:export\s+)?(TELESRV_[A-Z0-9_]+)\s*=`)
	driftCommentedKeyRe = regexp.MustCompile(`^#\s*(?:export\s+)?(TELESRV_[A-Z0-9_]+)\s*=`)
)

// EnvDrift is the report of how .env differs from .env.example; keys keep the
// order in which they appear in the template/file.
type EnvDrift struct {
	// ConfigPath is the compared config file (TELESRV_CONFIG or the default .env).
	ConfigPath string
	// ExamplePath is the template file path.
	ExamplePath string
	// Missing lists keys declared in .env.example but given by neither .env nor the
	// process environment.
	Missing []string
	// Unknown lists keys present in .env but not covered by .env.example (typo or
	// stale template).
	Unknown []string
}

// DetectEnvDrift locates the config file by the same rules as Load and compares it
// with <config file>.example. A missing template returns the zero value without an
// error -- production images carry no .env.example, which is expected.
func DetectEnvDrift() (EnvDrift, error) {
	cfgPath, _ := configFilePath()
	if cfgPath == "" {
		return EnvDrift{}, nil
	}
	return envDriftAt(cfgPath, cfgPath+".example")
}

func envDriftAt(cfgPath, examplePath string) (EnvDrift, error) {
	drift := EnvDrift{ConfigPath: cfgPath, ExamplePath: examplePath}
	exampleKeys, err := readExampleKeys(examplePath)
	if err != nil {
		if os.IsNotExist(err) {
			return EnvDrift{}, nil
		}
		return EnvDrift{}, fmt.Errorf("read %s: %w", examplePath, err)
	}
	// When .env is missing, treat it as "everything uses defaults": config then
	// usually lives entirely in the process environment, which the per-key
	// os.LookupEnv filter below accounts for, so only genuinely missing keys reach
	// Missing. Consistent with loadConfigEnv -- a missing file on the default path is
	// not an error.
	var envValues envSource
	if cfgPath != "" {
		if envValues, err = readEnvFile(cfgPath); err != nil {
			if !os.IsNotExist(err) {
				return EnvDrift{}, fmt.Errorf("read %s: %w", cfgPath, err)
			}
			err = nil
			envValues = nil
		}
	}

	exampleSet := make(map[string]struct{}, len(exampleKeys))
	for _, key := range exampleKeys {
		exampleSet[key] = struct{}{}
		if _, inFile := envValues[key]; inFile {
			continue
		}
		// The process environment takes precedence over the file (envOr checks
		// os.Getenv first), so being set there means not missing.
		if _, inEnv := os.LookupEnv(key); inEnv {
			continue
		}
		drift.Missing = append(drift.Missing, key)
	}
	for key := range envValues {
		if _, ok := exampleSet[key]; !ok {
			drift.Unknown = append(drift.Unknown, key)
		}
	}
	// envValues is a map with random iteration order; sort once so logs and tests
	// are stable.
	sort.Strings(drift.Unknown)
	return drift, nil
}

// readExampleKeys returns every TELESRV_* field key in the template in order of
// appearance (both active lines and commented-out optional field lines count),
// deduplicated; plain comments, group headers and placeholder lines in the template
// are always skipped.
func readExampleKeys(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var keys []string
	seen := make(map[string]struct{})
	for _, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(raw)
		var key string
		if a := driftActiveKeyRe.FindStringSubmatch(line); a != nil {
			key = a[1]
		} else if c := driftCommentedKeyRe.FindStringSubmatch(line); c != nil {
			key = c[1]
		}
		if key == "" {
			continue
		}
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		keys = append(keys, key)
	}
	return keys, nil
}
