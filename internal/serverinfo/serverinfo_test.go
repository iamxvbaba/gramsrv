package serverinfo

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"telesrv/internal/identity"
)

func TestHandlerServesPublicDescriptorAndIcon(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	store := identity.NewStore(t.TempDir())
	if err := store.SetText("Lab", "Self-hosted test server"); err != nil {
		t.Fatal(err)
	}
	icon := bytes.Repeat([]byte{0x89, 'P', 'N', 'G'}, 4096)
	if err := store.SetIcon(icon, ".png"); err != nil {
		t.Fatal(err)
	}
	h := Handler(2, key, store)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, InfoPath, nil))
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "application/json; charset=utf-8" {
		t.Fatalf("server-info = %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	var info Info
	if err := json.Unmarshal(rec.Body.Bytes(), &info); err != nil {
		t.Fatal(err)
	}
	if info.DCID != 2 || info.Name != "Lab" || info.Description != "Self-hosted test server" || !info.HasIcon {
		t.Fatalf("server-info = %+v", info)
	}
	block, _ := pem.Decode([]byte(info.RSAPublicKeyPEM))
	if block == nil || block.Type != "RSA PUBLIC KEY" {
		t.Fatalf("server-info key block = %+v, want PKCS#1 RSA PUBLIC KEY", block)
	}
	if pub, err := x509.ParsePKCS1PublicKey(block.Bytes); err != nil || !pub.Equal(&key.PublicKey) {
		t.Fatalf("server-info key = %v err=%v, want the server key", pub, err)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, IconPath, nil))
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "image/png" || !bytes.Equal(rec.Body.Bytes(), icon) {
		t.Fatalf("server-icon = %d %q len=%d", rec.Code, rec.Header().Get("Content-Type"), rec.Body.Len())
	}
	if rec.Header().Get("Content-Length") != strconv.Itoa(len(icon)) {
		t.Fatalf("server-icon Content-Length = %q, want %d (no chunked body)", rec.Header().Get("Content-Length"), len(icon))
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodHead, InfoPath, nil))
	if rec.Code != http.StatusOK || rec.Body.Len() != 0 || rec.Header().Get("Content-Length") == "" {
		t.Fatalf("HEAD server-info = %d body=%d", rec.Code, rec.Body.Len())
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, InfoPath, nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST server-info = %d, want 405", rec.Code)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/owpengram/other", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown path = %d, want 404", rec.Code)
	}
}

func TestHandlerWithoutIdentityStillServesKey(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	h := Handler(4, key, identity.NewStore(t.TempDir()))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, InfoPath, nil))
	var info Info
	if err := json.Unmarshal(rec.Body.Bytes(), &info); err != nil || info.DCID != 4 || info.RSAPublicKeyPEM == "" || info.HasIcon {
		t.Fatalf("unconfigured identity server-info = %+v err=%v", info, err)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, IconPath, nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("server-icon without icon = %d, want 404", rec.Code)
	}

	rec = httptest.NewRecorder()
	Handler(2, nil, nil).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, InfoPath, nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("server-info without key = %d, want 503", rec.Code)
	}
}
