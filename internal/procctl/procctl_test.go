package procctl

import (
	"os"
	"path/filepath"
	"testing"
)

const sampleEnvExample = `# Generated automatically. Keep the resulting .env private.

COMPOSE_PROJECT_NAME=gramsrv-main
TELESRV_LOG_LEVEL=info

# Independent random values generated for each deployment.
TELESRV_ADMIN_API_TOKEN=CHANGEME
TELESRV_ADMIN_UI_PASSWORD=CHANGEME
TELESRV_SECRET_CHAT_DELETE_FILE_AFTER_DOWNLOAD=true

# Optional webhook delivery, disabled by default.
#TELESRV_OTP_WEBHOOK_URL=
#TELESRV_OTP_WEBHOOK_SECRET=
`

func writeSampleTemplate(t *testing.T, dir string) (envPath, tmplPath string) {
	t.Helper()
	tmplPath = filepath.Join(dir, ".env.example")
	if err := os.WriteFile(tmplPath, []byte(sampleEnvExample), 0o644); err != nil {
		t.Fatal(err)
	}
	envPath = filepath.Join(dir, ".env")
	return envPath, tmplPath
}

func TestReadEnvGroupsBlankLineBlocks(t *testing.T) {
	dir := t.TempDir()
	envPath, tmplPath := writeSampleTemplate(t, dir)
	if err := os.WriteFile(envPath, []byte("COMPOSE_PROJECT_NAME=gramsrv-main\nTELESRV_LOG_LEVEL=debug\nTELESRV_ADMIN_API_TOKEN=real-token\nTELESRV_ADMIN_UI_PASSWORD=real-password\nTELESRV_SECRET_CHAT_DELETE_FILE_AFTER_DOWNLOAD=false\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	m := NewManager(envPath, tmplPath, "test-container")
	groups, err := m.ReadEnvGroups()
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 3 {
		t.Fatalf("groups = %d, want 3 (one per blank-line block with fields): %+v", len(groups), groups)
	}
	if groups[0].Title != "" {
		t.Fatalf("first group title = %q, want empty (no comment precedes it)", groups[0].Title)
	}
	if len(groups[0].Fields) != 2 || groups[0].Fields[0].Key != "COMPOSE_PROJECT_NAME" {
		t.Fatalf("first group fields = %+v", groups[0].Fields)
	}

	if groups[1].Title != "Independent random values generated for each deployment." {
		t.Fatalf("second group title = %q", groups[1].Title)
	}
	var apiToken, secretChat EnvField
	for _, f := range groups[1].Fields {
		switch f.Key {
		case "TELESRV_ADMIN_API_TOKEN":
			apiToken = f
		case "TELESRV_SECRET_CHAT_DELETE_FILE_AFTER_DOWNLOAD":
			secretChat = f
		}
	}
	if apiToken.Value != "real-token" {
		t.Fatalf("TELESRV_ADMIN_API_TOKEN value = %q, want the live .env value", apiToken.Value)
	}
	if !apiToken.Sensitive {
		t.Fatal("TELESRV_ADMIN_API_TOKEN should be flagged sensitive")
	}
	if secretChat.Sensitive {
		t.Fatal("TELESRV_SECRET_CHAT_DELETE_FILE_AFTER_DOWNLOAD matches \"SECRET\" but is a boolean toggle, not a credential -- must not be flagged sensitive")
	}

	if groups[2].Title != "Optional webhook delivery, disabled by default." {
		t.Fatalf("third group title = %q", groups[2].Title)
	}
	if groups[2].Fields[0].Enabled {
		t.Fatal("commented-out field with no live .env override should read as disabled")
	}
	if groups[2].Fields[0].Value != "" {
		t.Fatalf("disabled field value = %q, want empty default", groups[2].Fields[0].Value)
	}
}

func TestReadEnvGroupsMissingTemplateIsNilNotError(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(filepath.Join(dir, ".env"), filepath.Join(dir, ".env.example"), "test-container")
	groups, err := m.ReadEnvGroups()
	if err != nil {
		t.Fatalf("missing .env.example should not error: %v", err)
	}
	if groups != nil {
		t.Fatalf("expected nil groups, got %+v", groups)
	}
}

func TestWriteEnvValuesPreservesUntouchedKeys(t *testing.T) {
	dir := t.TempDir()
	envPath, tmplPath := writeSampleTemplate(t, dir)
	if err := os.WriteFile(envPath, []byte("COMPOSE_PROJECT_NAME=gramsrv-main\nTELESRV_LOG_LEVEL=debug\nTELESRV_ADMIN_API_TOKEN=old-token\nTELESRV_ADMIN_UI_PASSWORD=old-password\nTELESRV_SECRET_CHAT_DELETE_FILE_AFTER_DOWNLOAD=false\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	m := NewManager(envPath, tmplPath, "test-container")
	if err := m.WriteEnvValues(map[string]string{"TELESRV_LOG_LEVEL": "warn"}); err != nil {
		t.Fatal(err)
	}

	values, err := m.readEnvFile()
	if err != nil {
		t.Fatal(err)
	}
	if values["TELESRV_LOG_LEVEL"] != "warn" {
		t.Fatalf("TELESRV_LOG_LEVEL = %q, want warn", values["TELESRV_LOG_LEVEL"])
	}
	// Every other key must survive untouched -- a naive rewrite from the
	// template would silently reset these to CHANGEME/false.
	if values["TELESRV_ADMIN_API_TOKEN"] != "old-token" {
		t.Fatalf("TELESRV_ADMIN_API_TOKEN clobbered: %q", values["TELESRV_ADMIN_API_TOKEN"])
	}
	if values["TELESRV_ADMIN_UI_PASSWORD"] != "old-password" {
		t.Fatalf("TELESRV_ADMIN_UI_PASSWORD clobbered: %q", values["TELESRV_ADMIN_UI_PASSWORD"])
	}
}

func TestWriteEnvValuesEnablesCommentedField(t *testing.T) {
	dir := t.TempDir()
	envPath, tmplPath := writeSampleTemplate(t, dir)
	if err := os.WriteFile(envPath, []byte("TELESRV_ADMIN_API_TOKEN=x\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	m := NewManager(envPath, tmplPath, "test-container")
	if err := m.WriteEnvValues(map[string]string{"TELESRV_OTP_WEBHOOK_URL": "https://example.com/hook"}); err != nil {
		t.Fatal(err)
	}
	values, err := m.readEnvFile()
	if err != nil {
		t.Fatal(err)
	}
	if values["TELESRV_OTP_WEBHOOK_URL"] != "https://example.com/hook" {
		t.Fatalf("TELESRV_OTP_WEBHOOK_URL = %q, want it enabled with the new value", values["TELESRV_OTP_WEBHOOK_URL"])
	}
}
