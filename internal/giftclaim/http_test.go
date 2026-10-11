package giftclaim

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestClaimPageUsesHTTPSWalletHandoff(t *testing.T) {
	service := &Service{appName: "InvGram Gifts", publicBaseURL: "https://claim.example"}
	page := httptest.NewRecorder()
	service.ServeHTTP(page, httptest.NewRequest(http.MethodGet, "/claim", nil))
	if page.Code != http.StatusOK {
		t.Fatalf("claim page status = %d", page.Code)
	}
	body := page.Body.String()
	for _, expected := range []string{"tc.connector.connect", "tg.openLink", "parsed.protocol!=='https:'", "{request:{tonProof:challenge.payload}}"} {
		if !strings.Contains(body, expected) {
			t.Fatalf("claim page does not contain %q", expected)
		}
	}
	for _, forbidden := range []string{"tc.openModal", "buttonRootId:'wallet'"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("claim page still contains %q", forbidden)
		}
	}

	manifest := httptest.NewRecorder()
	service.ServeHTTP(manifest, httptest.NewRequest(http.MethodGet, "/claim/tonconnect-manifest.json", nil))
	if manifest.Code != http.StatusOK || !strings.Contains(manifest.Body.String(), `"iconUrl":"https://claim.example/custom-fragment/media/gift/owl-1.png"`) {
		t.Fatalf("manifest status=%d body=%q", manifest.Code, manifest.Body.String())
	}
}

func TestClaimTestPageServesUnderItsBasePath(t *testing.T) {
	service := &Service{
		appName: "InvGram Gifts (test)", publicBaseURL: "https://claim-test.example",
		basePath: "/claimtest", botHandle: "@claimtest",
	}
	page := httptest.NewRecorder()
	service.ServeHTTP(page, httptest.NewRequest(http.MethodGet, "/claimtest", nil))
	if page.Code != http.StatusOK {
		t.Fatalf("claim test page status = %d", page.Code)
	}
	body := page.Body.String()
	for _, expected := range []string{"@claimtest", "fetch('\\/claimtest/api/'", "location.origin+'\\/claimtest/tonconnect-manifest.json'"} {
		if !strings.Contains(body, expected) {
			t.Fatalf("claim test page does not contain %q", expected)
		}
	}
	manifest := httptest.NewRecorder()
	service.ServeHTTP(manifest, httptest.NewRequest(http.MethodGet, "/claimtest/tonconnect-manifest.json", nil))
	if manifest.Code != http.StatusOK || !strings.Contains(manifest.Body.String(), `"url":"https://claim-test.example/claimtest"`) {
		t.Fatalf("claim test manifest status=%d body=%q", manifest.Code, manifest.Body.String())
	}
}
