package help

import (
	"context"
	"encoding/json"
	"testing"
)

// TestAppConfigEnablesFactCheckEditing asserts that can_edit_factcheck is explicitly sent
// as true. Both TDesktop and WebK use this key to decide whether to show the "Add Fact-Check"
// entry: when the key is missing canEdit() is always false, and the server-implemented
// fact-check method never has a UI that can call it.
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
		t.Fatalf("can_edit_factcheck = %v ok=%v, want true (fact-check editing entry gate)", decoded["can_edit_factcheck"], ok)
	}
	limit, ok := decoded["factcheck_length_limit"].(float64)
	if !ok || limit <= 0 {
		t.Fatalf("factcheck_length_limit = %v ok=%v, want a positive number", decoded["factcheck_length_limit"], ok)
	}
}
