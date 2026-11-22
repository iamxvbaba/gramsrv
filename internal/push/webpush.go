package push

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// errNoWebPush means the service was asked to deliver web push without VAPID
// configured (or for a malformed subscription token).
var errNoWebPush = errors.New("web push is not configured")

// webPushSubscription is the JSON the web client registers: the PushSubscription
// serialization plus the vapid flag (token_type 10). See Telegram Web K's
// webPushApiManager.makeTokenData.
type webPushSubscription struct {
	Endpoint string `json:"endpoint"`
	Keys     struct {
		P256DH string `json:"p256dh"`
		Auth   string `json:"auth"`
	} `json:"keys"`
}

func parseWebPushSubscription(token string) (webPushSubscription, error) {
	var sub webPushSubscription
	if err := json.Unmarshal([]byte(token), &sub); err != nil {
		return webPushSubscription{}, fmt.Errorf("parse web push token: %w", err)
	}
	if sub.Endpoint == "" || sub.Keys.P256DH == "" || sub.Keys.Auth == "" {
		return webPushSubscription{}, errors.New("web push token is missing endpoint or keys")
	}
	if _, err := url.Parse(sub.Endpoint); err != nil {
		return webPushSubscription{}, fmt.Errorf("web push endpoint: %w", err)
	}
	return sub, nil
}

// WebPushSender delivers RFC 8291 encrypted web push messages with a VAPID
// (RFC 8292) Authorization header.
type WebPushSender struct {
	vapidPrivate *ecdsa.PrivateKey
	vapidPublic  []byte // uncompressed P-256 point (65 bytes)
	subject      string
	ttl          int
	client       *http.Client
	now          func() time.Time
}

// NewWebPushSender builds a sender from base64url VAPID keys. The public key
// must equal the applicationServerKey the web client subscribed with
// (VITE_PUSH_SERVER_KEY).
func NewWebPushSender(privateKeyB64, publicKeyB64, subject string, ttl int, timeout time.Duration) (*WebPushSender, error) {
	if privateKeyB64 == "" || publicKeyB64 == "" {
		return nil, errNoWebPush
	}
	priv, err := decodeBase64URL(privateKeyB64)
	if err != nil {
		return nil, fmt.Errorf("vapid private key: %w", err)
	}
	pub, err := decodeBase64URL(publicKeyB64)
	if err != nil {
		return nil, fmt.Errorf("vapid public key: %w", err)
	}
	if len(pub) != 65 || pub[0] != 0x04 {
		return nil, errors.New("vapid public key must be an uncompressed P-256 point")
	}
	curve := ecdh.P256()
	ecdhPriv, err := curve.NewPrivateKey(priv)
	if err != nil {
		return nil, fmt.Errorf("vapid private key: %w", err)
	}
	// Rebuild an ecdsa key usable for signing.
	d := new(big.Int).SetBytes(priv)
	ecdsaPriv := &ecdsa.PrivateKey{
		PublicKey: ecdsa.PublicKey{Curve: elliptic.P256(), X: new(big.Int).SetBytes(pub[1:33]), Y: new(big.Int).SetBytes(pub[33:65])},
		D:         d,
	}
	// ecdhPriv is only used to validate the scalar.
	_ = ecdhPriv
	if ttl <= 0 {
		ttl = 86400
	}
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	return &WebPushSender{
		vapidPrivate: ecdsaPriv,
		vapidPublic:  pub,
		subject:      subject,
		ttl:          ttl,
		client:       &http.Client{Timeout: timeout},
		now:          time.Now,
	}, nil
}

// Send encrypts payload for the subscription in tokenJSON and POSTs it to the
// push endpoint. A 404/410 response means the subscription is gone.
func (s *WebPushSender) Send(ctx context.Context, tokenJSON string, payload []byte) error {
	sub, err := parseWebPushSubscription(tokenJSON)
	if err != nil {
		return err
	}
	uaPublic, err := decodeBase64URL(sub.Keys.P256DH)
	if err != nil {
		return fmt.Errorf("web push p256dh: %w", err)
	}
	authSecret, err := decodeBase64URL(sub.Keys.Auth)
	if err != nil {
		return fmt.Errorf("web push auth: %w", err)
	}
	asPrivate, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return err
	}
	body, err := encryptWebPush(uaPublic, authSecret, asPrivate, salt, payload)
	if err != nil {
		return err
	}
	authHeader, err := s.vapidHeader(originOf(sub.Endpoint))
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, sub.Endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Encoding", "aes128gcm")
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("TTL", strconv.Itoa(s.ttl))
	req.Header.Set("Urgency", "high")
	req.Header.Set("Authorization", authHeader)
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	payloadResp, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone {
		return ErrTokenUnregistered
	}
	return fmt.Errorf("web push: status %d: %s", resp.StatusCode, strings.TrimSpace(string(payloadResp)))
}

// vapidHeader builds the RFC 8292 Authorization value for the push service origin.
func (s *WebPushSender) vapidHeader(audience string) (string, error) {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"typ":"JWT","alg":"ES256"}`))
	claims, err := json.Marshal(map[string]any{
		"aud": audience,
		"exp": s.now().Add(12 * time.Hour).Unix(),
		"sub": s.subject,
	})
	if err != nil {
		return "", err
	}
	signingInput := header + "." + base64.RawURLEncoding.EncodeToString(claims)
	digest := sha256.Sum256([]byte(signingInput))
	r, ss, err := ecdsa.Sign(rand.Reader, s.vapidPrivate, digest[:])
	if err != nil {
		return "", err
	}
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	ss.FillBytes(sig[32:])
	jwt := signingInput + "." + base64.RawURLEncoding.EncodeToString(sig)
	return "vapid t=" + jwt + ", k=" + base64.RawURLEncoding.EncodeToString(s.vapidPublic), nil
}

// encryptWebPush implements RFC 8291 (message encryption for web push) with the
// RFC 8188 aes128gcm content coding. It returns the full request body:
// salt(16) || rs(4) || idlen(1) || as_public(65) || AES-128-GCM(plaintext||0x02).
func encryptWebPush(uaPublic, authSecret []byte, asPrivate *ecdh.PrivateKey, salt, plaintext []byte) ([]byte, error) {
	curve := ecdh.P256()
	ua, err := curve.NewPublicKey(uaPublic)
	if err != nil {
		return nil, fmt.Errorf("web push ua public key: %w", err)
	}
	shared, err := asPrivate.ECDH(ua)
	if err != nil {
		return nil, fmt.Errorf("web push ecdh: %w", err)
	}
	asPublic := asPrivate.PublicKey().Bytes()

	// PRK_key = HKDF-Extract(salt=auth_secret, IKM=ecdh_secret)
	prkKey := hkdfExtract(authSecret, shared)
	// IKM = HKDF-Expand(PRK_key, "WebPush: info\0" || ua_public || as_public, 32)
	keyInfo := concat([]byte("WebPush: info\x00"), uaPublic, asPublic)
	ikm := hkdfExpand(prkKey, keyInfo, 32)
	// PRK = HKDF-Extract(salt, IKM)
	prk := hkdfExtract(salt, ikm)
	cek := hkdfExpand(prk, []byte("Content-Encoding: aes128gcm\x00"), 16)
	nonce := hkdfExpand(prk, []byte("Content-Encoding: nonce\x00"), 12)

	block, err := aes.NewCipher(cek)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	record := append(append([]byte{}, plaintext...), 0x02)
	ciphertext := gcm.Seal(nil, nonce, record, nil)

	header := make([]byte, 0, 86)
	header = append(header, salt...)
	rs := make([]byte, 4)
	binary.BigEndian.PutUint32(rs, 4096)
	header = append(header, rs...)
	header = append(header, byte(len(asPublic)))
	header = append(header, asPublic...)
	return append(header, ciphertext...), nil
}

func hkdfExtract(salt, ikm []byte) []byte {
	h := hmac.New(sha256.New, salt)
	h.Write(ikm)
	return h.Sum(nil)
}

func hkdfExpand(prk, info []byte, length int) []byte {
	out := make([]byte, 0, length)
	var block []byte
	for counter := byte(1); len(out) < length; counter++ {
		h := hmac.New(sha256.New, prk)
		h.Write(block)
		h.Write(info)
		h.Write([]byte{counter})
		block = h.Sum(nil)
		out = append(out, block...)
	}
	return out[:length]
}

func originOf(endpoint string) string {
	u, err := url.Parse(endpoint)
	if err != nil {
		return endpoint
	}
	return u.Scheme + "://" + u.Host
}

// decodeBase64URL tolerates both padded and unpadded URL-safe base64.
func decodeBase64URL(s string) ([]byte, error) {
	if b, err := base64.RawURLEncoding.DecodeString(s); err == nil {
		return b, nil
	}
	return base64.URLEncoding.DecodeString(s)
}
