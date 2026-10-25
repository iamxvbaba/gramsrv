package help

import (
	"context"
	"encoding/json"
	"testing"
)

// TestAppConfigEnablesFactCheckEditing 断言 can_edit_factcheck 显式下发为 true。
// TDesktop 与 WebK 都用这个 key 决定是否显示「Add Fact-Check」入口：缺 key 时
// canEdit() 恒为 false，服务端实现的事实核查方法永远没有 UI 可以调用。
func TestAppConfigEnablesFactCheckEditing(t *testing.T) {
	cfg, notModified, err := (*Service)(nil).GetAppConfig(context.Background(), 0, 0)
	if err != nil || notModified {
		t.Fatalf("GetAppConfig = notModified %v err %v", notModified, err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(cfg.JSON, &decoded); err != nil {
		t.Fatalf("app config json invalid: %v", err)
	}
	if enabled, ok := decoded["can_edit_factcheck"].(bool); !ok || !enabled {
		t.Fatalf("can_edit_factcheck = %v ok=%v, want true (事实核查编辑入口 gate)", decoded["can_edit_factcheck"], ok)
	}
	limit, ok := decoded["factcheck_length_limit"].(float64)
	if !ok || limit <= 0 {
		t.Fatalf("factcheck_length_limit = %v ok=%v, want a positive number", decoded["factcheck_length_limit"], ok)
	}
}
