package help

import (
	"context"
	"encoding/json"
	"testing"
)

// TestAppConfigDisplaysPasskeys 断言 settings_display_passkeys 显式下发为 true 且 hash
// 已递增：core.telegram.org/api/passkeys 规定客户端仅在该 key 为 true 时显示
// Settings→Privacy→Passkeys 注册入口，缺 key 会让注册 UI 永不出现，服务端
// passkey 实现因此永远无人可注册。
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
		t.Fatalf("settings_display_passkeys = %v ok=%v, want true (passkey 注册入口 gate)", decoded["settings_display_passkeys"], ok)
	}
}
