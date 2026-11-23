package push

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func mustB64(t *testing.T, s string) []byte {
	t.Helper()
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		t.Fatalf("decode %q: %v", s, err)
	}
	return b
}

// TestEncryptWebPushRFC8291Vector checks encryptWebPush against the worked
// example in RFC 8291 section 5 / Appendix A (exact salt and application-server
// key), so every intermediate value is pinned.
func TestEncryptWebPushRFC8291Vector(t *testing.T) {
	uaPublic := mustB64(t, "BCVxsr7N_eNgVRqvHtD0zTZsEc6-VV-JvLexhqUzORcxaOzi6-AYWXvTBHm4bjyPjs7Vd8pZGH6SRpkNtoIAiw4")
	asPrivateRaw := mustB64(t, "yfWPiYE-n46HLnH0KqZOF1fJJU3MYrct3AELtAQ-oRw")
	authSecret := mustB64(t, "BTBZMqHH6r4Tts7J_aSIgg")
	salt := mustB64(t, "DGv6ra1nlYgDCS1FRnbzlw")

	asPrivate, err := ecdh.P256().NewPrivateKey(asPrivateRaw)
	if err != nil {
		t.Fatalf("as private key: %v", err)
	}
	if got := base64.RawURLEncoding.EncodeToString(asPrivate.PublicKey().Bytes()); got != "BP4z9KsN6nGRTbVYI_c7VJSPQTBtkgcy27mlmlMoZIIgDll6e3vCYLocInmYWAmS6TlzAC8wEqKK6PBru3jl7A8" {
		t.Fatalf("as public key = %s, want RFC value", got)
	}

	ua, err := ecdh.P256().NewPublicKey(uaPublic)
	if err != nil {
		t.Fatalf("ua public key: %v", err)
	}
	shared, err := asPrivate.ECDH(ua)
	if err != nil {
		t.Fatalf("ecdh: %v", err)
	}
	if got := base64.RawURLEncoding.EncodeToString(shared); got != "kyrL1jIIOHEzg3sM2ZWRHDRB62YACZhhSlknJ672kSs" {
		t.Fatalf("ecdh_secret = %s, want RFC value", got)
	}

	body, err := encryptWebPush(uaPublic, authSecret, asPrivate, salt, []byte("When I grow up, I want to be a watermelon"))
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	want := "DGv6ra1nlYgDCS1FRnbzlwAAEABBBP4z9KsN6nGRTbVYI_c7VJSPQTBtkgcy27mlmlMoZIIgDll6e3vCYLocInmYWAmS6TlzAC8wEqKK6PBru3jl7A_yl95bQpu6cVPTpK4Mqgkf1CXztLVBSt2Ks3oZwbuwXPXLWyouBWLVWGNWQexSgSxsj_Qulcy4a-fN"
	if got := base64.RawURLEncoding.EncodeToString(body); got != want {
		t.Fatalf("encrypted body mismatch:\n got %s\nwant %s", got, want)
	}
}

func TestParseWebPushSubscription(t *testing.T) {
	valid := `{"endpoint":"https://fcm.googleapis.com/fcm/send/abc","keys":{"p256dh":"BCVx","auth":"BTBZ"},"vapid":true}`
	sub, err := parseWebPushSubscription(valid)
	if err != nil {
		t.Fatalf("valid subscription rejected: %v", err)
	}
	if sub.Endpoint == "" || sub.Keys.P256DH != "BCVx" || sub.Keys.Auth != "BTBZ" {
		t.Fatalf("parsed = %+v", sub)
	}
	for name, token := range map[string]string{
		"not json":  `nope`,
		"no keys":   `{"endpoint":"https://x"}`,
		"empty key": `{"endpoint":"https://x","keys":{"p256dh":"","auth":"a"}}`,
	} {
		if _, err := parseWebPushSubscription(token); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
}

func TestOriginOf(t *testing.T) {
	if got := originOf("https://fcm.googleapis.com/fcm/send/abc"); got != "https://fcm.googleapis.com" {
		t.Fatalf("origin = %q", got)
	}
	if got := originOf("https://updates.push.services.mozilla.com/wpush/v2/xyz"); got != "https://updates.push.services.mozilla.com" {
		t.Fatalf("origin = %q", got)
	}
}

// TestWebPushSenderEndToEnd runs the real sender against a stand-in push service
// that verifies the VAPID JWT and decrypts the RFC 8291 body, proving the whole
// envelope (not just the encryption primitive).
func TestWebPushSenderEndToEnd(t *testing.T) {
	ua, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	authSecret := make([]byte, 16)
	if _, err := rand.Read(authSecret); err != nil {
		t.Fatal(err)
	}
	vapid, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	var gotAuth, gotEncoding string
	var gotBody []byte
	var gotTTL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotEncoding = r.Header.Get("Content-Encoding")
		gotTTL = r.Header.Get("TTL")
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	token := fmt.Sprintf(`{"endpoint":%q,"keys":{"p256dh":%q,"auth":%q}}`,
		srv.URL+"/push/sub", b64(ua.PublicKey().Bytes()), b64(authSecret))
	sender, err := NewWebPushSender(b64(vapid.Bytes()), b64(vapid.PublicKey().Bytes()), "mailto:test@example.com", 60, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte(`{"loc_key":"MESSAGE_TEXT","title":"Alice"}`)
	if err := sender.Send(context.Background(), token, payload); err != nil {
		t.Fatalf("send: %v", err)
	}
	if gotEncoding != "aes128gcm" || gotTTL != "60" {
		t.Fatalf("headers: encoding=%q ttl=%q", gotEncoding, gotTTL)
	}
	verifyVAPID(t, gotAuth, originOf(srv.URL+"/push/sub"), vapid.PublicKey().Bytes(), "mailto:test@example.com")
	decrypted := decryptWebPush(t, ua, authSecret, gotBody)
	if string(decrypted) != string(payload) {
		t.Fatalf("decrypted = %s, want %s", decrypted, payload)
	}
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// decryptWebPush reverses the aes128gcm body for the test.
func decryptWebPush(t *testing.T, ua *ecdh.PrivateKey, authSecret, body []byte) []byte {
	t.Helper()
	if len(body) < 21 {
		t.Fatalf("body too short: %d", len(body))
	}
	salt := body[0:16]
	idlen := int(body[20])
	if len(body) < 21+idlen {
		t.Fatalf("keyid overruns body")
	}
	asPublic := body[21 : 21+idlen]
	ct := body[21+idlen:]
	asPub, err := ecdh.P256().NewPublicKey(asPublic)
	if err != nil {
		t.Fatalf("as public: %v", err)
	}
	shared, err := ua.ECDH(asPub)
	if err != nil {
		t.Fatalf("ecdh: %v", err)
	}
	prkKey := hkdfExtract(authSecret, shared)
	keyInfo := concat([]byte("WebPush: info\x00"), ua.PublicKey().Bytes(), asPublic)
	ikm := hkdfExpand(prkKey, keyInfo, 32)
	prk := hkdfExtract(salt, ikm)
	cek := hkdfExpand(prk, []byte("Content-Encoding: aes128gcm\x00"), 16)
	nonce := hkdfExpand(prk, []byte("Content-Encoding: nonce\x00"), 12)
	block, err := aes.NewCipher(cek)
	if err != nil {
		t.Fatal(err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	record, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		t.Fatalf("gcm open: %v", err)
	}
	if len(record) == 0 || record[len(record)-1] != 0x02 {
		t.Fatalf("missing padding delimiter")
	}
	return record[:len(record)-1]
}

func verifyVAPID(t *testing.T, header, wantAud string, pub []byte, wantSub string) {
	t.Helper()
	if !strings.HasPrefix(header, "vapid t=") {
		t.Fatalf("authorization = %q", header)
	}
	rest := strings.TrimPrefix(header, "vapid t=")
	parts := strings.SplitN(rest, ", k=", 2)
	if len(parts) != 2 {
		t.Fatalf("authorization = %q", header)
	}
	jwt, keyB64 := parts[0], parts[1]
	if keyB64 != b64(pub) {
		t.Fatalf("vapid k = %q, want %q", keyB64, b64(pub))
	}
	seg := strings.Split(jwt, ".")
	if len(seg) != 3 {
		t.Fatalf("jwt = %q", jwt)
	}
	sig, err := base64.RawURLEncoding.DecodeString(seg[2])
	if err != nil || len(sig) != 64 {
		t.Fatalf("jwt signature: %v len=%d", err, len(sig))
	}
	digest := sha256.Sum256([]byte(seg[0] + "." + seg[1]))
	pk := &ecdsa.PublicKey{
		Curve: elliptic.P256(),
		X:     new(big.Int).SetBytes(pub[1:33]),
		Y:     new(big.Int).SetBytes(pub[33:65]),
	}
	r := new(big.Int).SetBytes(sig[:32])
	ss := new(big.Int).SetBytes(sig[32:])
	if !ecdsa.Verify(pk, digest[:], r, ss) {
		t.Fatal("vapid signature did not verify")
	}
	claims, err := base64.RawURLEncoding.DecodeString(seg[1])
	if err != nil {
		t.Fatalf("claims: %v", err)
	}
	var c map[string]any
	if err := json.Unmarshal(claims, &c); err != nil {
		t.Fatalf("claims json: %v", err)
	}
	if c["aud"] != wantAud || c["sub"] != wantSub {
		t.Fatalf("claims = %v, want aud=%s sub=%s", c, wantAud, wantSub)
	}
}
