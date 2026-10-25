package config

import (
	"fmt"
	"strings"
)

type AdminConfigYAML struct {
	Version  int              `yaml:"version"`
	Postgres PostgresYAML     `yaml:"postgres"`
	Admin    AdminYAML        `yaml:"admin"`
	Storage  AdminStorageYAML `yaml:"storage"`
	Identity IdentityYAML     `yaml:"identity"`
}

type AdminYAML struct {
	UI  AdminUIYAML  `yaml:"ui"`
	API AdminAPIYAML `yaml:"api"`
}

type AdminUIYAML struct {
	Addr        string   `yaml:"addr"`
	Password    string   `yaml:"password"`
	Token       string   `yaml:"token"`
	SessionKey  string   `yaml:"session_key"`
	Permissions []string `yaml:"permissions"`
}

type AdminAPIYAML struct {
	Addr  string `yaml:"addr"`
	Token string `yaml:"token"`
}

type AdminStorageYAML struct {
	BlobBackendKind string `yaml:"blob_backend"`
	BlobDir         string `yaml:"blob_dir"`
	BlobStagingDir  string `yaml:"blob_staging_dir"`
}

type AdminConfig struct {
	Addr          string
	PostgresDSN   string
	AdminAPIAddr  string
	AdminAPIToken string
	Password      string
	Token         string
	SessionKey    string
	DiskStatsPath string
	Permissions   []string
	// IdentityDir is the server identity directory shared with Core.
	IdentityDir string
}

func LoadAdmin() (AdminConfig, error) {
	path, err := roleConfigPath(roleAdmin)
	if err != nil {
		return AdminConfig{}, err
	}
	var y AdminConfigYAML
	if err := readStrictYAML(path, &y); err != nil {
		return AdminConfig{}, fmt.Errorf("config file %q: %w", path, err)
	}
	return adminConfigFromYAML(y)
}

func adminConfigFromYAML(y AdminConfigYAML) (AdminConfig, error) {
	if err := requireYAMLVersion(y.Version); err != nil {
		return AdminConfig{}, err
	}
	permissions := trimStringList(y.Admin.UI.Permissions)
	if len(permissions) == 0 {
		permissions = []string{adminPermissionAll}
	}
	for _, permission := range permissions {
		if !validAdminPermission(permission) {
			return AdminConfig{}, fmt.Errorf("admin.ui.permissions contains invalid permission %q", permission)
		}
	}

	backend := strings.ToLower(strings.TrimSpace(y.Storage.BlobBackendKind))
	if backend == "" {
		backend = "localfs"
	}
	blobDir := strings.TrimSpace(y.Storage.BlobDir)
	if blobDir == "" {
		blobDir = "data/blobs"
	}
	blobStagingDir := strings.TrimSpace(y.Storage.BlobStagingDir)
	if blobStagingDir == "" {
		blobStagingDir = "data/blob-staging"
	}
	diskStatsPath := blobDir
	switch backend {
	case "localfs":
	case "s3":
		diskStatsPath = blobStagingDir
	default:
		return AdminConfig{}, fmt.Errorf("storage.blob_backend must be localfs or s3, got %q", backend)
	}

	cfg := AdminConfig{
		Addr:          stringDefault(y.Admin.UI.Addr, "127.0.0.1:2600"),
		PostgresDSN:   strings.TrimSpace(y.Postgres.DSN),
		AdminAPIAddr:  stringDefault(y.Admin.API.Addr, "127.0.0.1:2599"),
		AdminAPIToken: strings.TrimSpace(y.Admin.API.Token),
		Password:      strings.TrimSpace(y.Admin.UI.Password),
		Token:         strings.TrimSpace(y.Admin.UI.Token),
		SessionKey:    strings.TrimSpace(y.Admin.UI.SessionKey),
		DiskStatsPath: diskStatsPath,
		Permissions:   permissions,
		IdentityDir:   stringDefault(y.Identity.Dir, "data/identity"),
	}
	if cfg.PostgresDSN == "" {
		return AdminConfig{}, fmt.Errorf("postgres.dsn is required by cmd/telesrv-admin")
	}
	if cfg.Password == "" && cfg.Token == "" {
		return AdminConfig{}, fmt.Errorf("admin.ui.password or admin.ui.token is required by cmd/telesrv-admin")
	}
	if cfg.AdminAPIToken == "" {
		return AdminConfig{}, fmt.Errorf("admin.api.token is required by cmd/telesrv-admin")
	}
	if cfg.SessionKey == "" {
		return AdminConfig{}, fmt.Errorf("admin.ui.session_key is required by cmd/telesrv-admin")
	}
	return cfg, nil
}

func stringDefault(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	return value
}

func trimStringList(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			out = append(out, value)
		}
	}
	return out
}
