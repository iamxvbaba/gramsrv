package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"telesrv/internal/admin"
)

func TestGifCatalogCategoryActionForwardsOverrideAndAuto(t *testing.T) {
	var forwarded []admin.SetGifCatalogCategoryRequest
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/gif-catalog/set-category" || r.Header.Get("Authorization") != "Bearer secret" {
			t.Errorf("unexpected upstream request: %s %s", r.URL.Path, r.Header.Get("Authorization"))
		}
		var req admin.SetGifCatalogCategoryRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode upstream request: %v", err)
		}
		forwarded = append(forwarded, req)
		_ = json.NewEncoder(w).Encode(admin.CommandResult{Status: "completed"})
	}))
	defer upstream.Close()
	srv := &server{cfg: uiConfig{AdminAPIURL: upstream.URL, AdminAPIToken: "secret"}}
	for _, category := range []string{"animals", ""} {
		body, err := json.Marshal(map[string]any{"id": "9007199254740993", "category": category, "reason": "catalog", "confirm": true})
		if err != nil {
			t.Fatal(err)
		}
		rec := httptest.NewRecorder()
		srv.handleSetGifCatalogCategoryAPI(rec, httptest.NewRequest(http.MethodPost, "/api/actions/set-gif-catalog-category", strings.NewReader(string(body))))
		if rec.Code != http.StatusOK {
			t.Fatalf("category=%q status=%d body=%s", category, rec.Code, rec.Body.String())
		}
	}
	if len(forwarded) != 2 || forwarded[0].ID != 9_007_199_254_740_993 || forwarded[0].Category != "animals" || forwarded[1].Category != "" {
		t.Fatalf("forwarded requests = %+v", forwarded)
	}
}
