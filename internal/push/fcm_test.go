package push

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func testServiceAccountPEM(t *testing.T) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
}

func TestFCMSenderSendAndTokenCaching(t *testing.T) {
	var tokenCalls, sendCalls int
	var lastAuth, lastBody string
	sendStatus := http.StatusOK
	sendBody := ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/token":
			tokenCalls++
			_ = r.ParseForm()
			if r.Form.Get("assertion") == "" {
				t.Error("token request is missing the signed assertion")
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"access_token":"tok-123","expires_in":3600}`)
		case strings.HasPrefix(r.URL.Path, "/v1/projects/proj/messages:send"):
			sendCalls++
			lastAuth = r.Header.Get("Authorization")
			body, _ := io.ReadAll(r.Body)
			lastBody = string(body)
			w.WriteHeader(sendStatus)
			_, _ = io.WriteString(w, sendBody)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	sa := ServiceAccount{
		ProjectID:   "proj",
		ClientEmail: "svc@proj.iam.gserviceaccount.com",
		PrivateKey:  testServiceAccountPEM(t),
		TokenURI:    srv.URL + "/token",
	}
	sender, err := NewFCMSender(sa, "", srv.URL, "", time.Second)
	if err != nil {
		t.Fatalf("new sender: %v", err)
	}
	for i := 0; i < 2; i++ {
		if err := sender.Send(context.Background(), "device-token", map[string]string{"p": "x"}); err != nil {
			t.Fatalf("send %d: %v", i, err)
		}
	}
	if tokenCalls != 1 {
		t.Fatalf("token endpoint called %d times, want 1 (token must be cached)", tokenCalls)
	}
	if sendCalls != 2 {
		t.Fatalf("send endpoint called %d times, want 2", sendCalls)
	}
	if lastAuth != "Bearer tok-123" {
		t.Fatalf("authorization = %q, want Bearer tok-123", lastAuth)
	}
	if !strings.Contains(lastBody, `"device-token"`) || !strings.Contains(lastBody, `"p":"x"`) {
		t.Fatalf("send body = %q", lastBody)
	}

	sendStatus = http.StatusNotFound
	sendBody = `{"error":{"status":"UNREGISTERED","message":"Requested entity was not found."}}`
	if err := sender.Send(context.Background(), "device-token", map[string]string{"p": "x"}); !errors.Is(err, ErrTokenUnregistered) {
		t.Fatalf("unregistered send = %v, want ErrTokenUnregistered", err)
	}
}

func TestParseServiceAccount(t *testing.T) {
	if _, err := ParseServiceAccount([]byte(`{"client_email":"a@b","private_key":"-----BEGIN PRIVATE KEY-----\n"}`)); err != nil {
		t.Fatalf("valid service account rejected: %v", err)
	}
	if _, err := ParseServiceAccount([]byte(`{"client_email":""}`)); err == nil {
		t.Fatal("service account without credentials accepted")
	}
	if _, err := ParseServiceAccount([]byte(`not json`)); err == nil {
		t.Fatal("non-JSON service account accepted")
	}
}
