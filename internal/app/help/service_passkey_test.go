package help

import (
	"context"
	"encoding/json"
	"testing"
)

// TestAppConfigDisplaysPasskeys asserts that settings_display_passkeys is explicitly sent as
// true with an incremented hash: core.telegram.org/api/passkeys specifies that the client
// shows the Settings→Privacy→Passkeys registration entry only when this key is true; a
// missing key means the registration UI never appears, so nobody can ever register against
// the server passkey implementation.
func TestAppConfigDisplaysPasskeys(t *testing.T) {
	cfg, notModified, err := (*Service)(nil).GetAppConfig(context.Background(), 0, 0)
	if err != nil || notModified {
		t.Fatalf("GetAppConfig = notModified %v err %v", notModified, err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(cfg.JSON, &decoded); err != nil {
		t.Fatalf("app config json invalid: %v", err)
	}
	if enabled, ok := decoded["settings_display_passkeys"].(bool); !ok || !enabled {
		t.Fatalf("settings_display_passkeys = %v ok=%v, want true (passkey registration entry gate)", decoded["settings_display_passkeys"], ok)
	}
}
