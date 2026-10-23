package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestValidateAndroidAppLinks(t *testing.T) {
	plain := "aabbccddeeff00112233445566778899aabbccddeeff00112233445566778899"
	pkg, fps, err := ValidateAndroidAppLinks("", []string{"", plain, " AA:BB:CC:DD:EE:FF:00:11:22:33:44:55:66:77:88:99:AA:BB:CC:DD:EE:FF:00:11:22:33:44:55:66:77:88:99 "})
	if err != nil {
		t.Fatal(err)
	}
	if pkg != DefaultAndroidAppPackage {
		t.Fatalf("package = %q, want default", pkg)
	}
	if len(fps) != 1 || fps[0] != "AA:BB:CC:DD:EE:FF:00:11:22:33:44:55:66:77:88:99:AA:BB:CC:DD:EE:FF:00:11:22:33:44:55:66:77:88:99" {
		t.Fatalf("fingerprints = %v, want one normalized entry", fps)
	}
	for _, bad := range []struct{ pkg, fp string }{
		{pkg: "nodots", fp: plain},
		{pkg: "com.example.app", fp: "AA:BB"},
		{pkg: "com.example.app", fp: "zz" + plain[2:]},
	} {
		if _, _, err := ValidateAndroidAppLinks(bad.pkg, []string{bad.fp}); err == nil {
			t.Fatalf("ValidateAndroidAppLinks(%q, %q) succeeded, want error", bad.pkg, bad.fp)
		}
	}
}

func TestHandlerServesAssetLinksOnlyWhenConfigured(t *testing.T) {
	fp := "AA:BB:CC:DD:EE:FF:00:11:22:33:44:55:66:77:88:99:AA:BB:CC:DD:EE:FF:00:11:22:33:44:55:66:77:88:99"
	h, err := NewHandler(Config{StickerSets: fakeResolver{}, AndroidCertSHA256: []string{fp}})
	if err != nil {
		t.Fatal(err)
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/.well-known/assetlinks.json", nil))
	if rr.Code != http.StatusOK || rr.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("status=%d type=%q body=%s", rr.Code, rr.Header().Get("Content-Type"), rr.Body.String())
	}
	var got []struct {
		Relation []string `json:"relation"`
		Target   struct {
			Namespace    string   `json:"namespace"`
			PackageName  string   `json:"package_name"`
			Fingerprints []string `json:"sha256_cert_fingerprints"`
		} `json:"target"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Relation[0] != "delegate_permission/common.handle_all_urls" ||
		got[0].Target.Namespace != "android_app" || got[0].Target.PackageName != DefaultAndroidAppPackage ||
		len(got[0].Target.Fingerprints) != 1 || got[0].Target.Fingerprints[0] != fp {
		t.Fatalf("assetlinks = %+v", got)
	}

	off, err := NewHandler(Config{StickerSets: fakeResolver{}})
	if err != nil {
		t.Fatal(err)
	}
	rr = httptest.NewRecorder()
	off.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/.well-known/assetlinks.json", nil))
	if rr.Code == http.StatusOK {
		t.Fatalf("assetlinks served without fingerprints: %s", rr.Body.String())
	}
}
