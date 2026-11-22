package push

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// ErrTokenUnregistered means the provider permanently rejected the device token
// (FCM UNREGISTERED / INVALID_ARGUMENT). The caller should delete the row.
var ErrTokenUnregistered = errors.New("push token unregistered")

// ServiceAccount is the subset of a Google service-account JSON (the Firebase
// project's server credential) that the FCM sender needs.
type ServiceAccount struct {
	ProjectID   string `json:"project_id"`
	ClientEmail string `json:"client_email"`
	PrivateKey  string `json:"private_key"`
	TokenURI    string `json:"token_uri"`
}

// ParseServiceAccount decodes and validates a service-account JSON document.
func ParseServiceAccount(data []byte) (ServiceAccount, error) {
	var sa ServiceAccount
	if err := json.Unmarshal(data, &sa); err != nil {
		return ServiceAccount{}, fmt.Errorf("parse service account: %w", err)
	}
	if sa.ClientEmail == "" || sa.PrivateKey == "" {
		return ServiceAccount{}, errors.New("service account is missing client_email or private_key")
	}
	return sa, nil
}

// Sender delivers one data-only push message to a single provider token.
type Sender interface {
	Send(ctx context.Context, deviceToken string, data map[string]string) error
}

// FCMSender sends messages through the Firebase Cloud Messaging HTTP v1 API using
// an OAuth2 service-account access token.
type FCMSender struct {
	projectID string
	baseURL   string
	client    *http.Client
	tokens    *serviceAccountTokenSource
}

// NewFCMSender builds an FCM sender. baseURL defaults to the public FCM endpoint;
// it is overridable for tests. An empty projectID falls back to the service
// account's project_id.
func NewFCMSender(sa ServiceAccount, projectID, baseURL, scope string, timeout time.Duration) (*FCMSender, error) {
	if projectID == "" {
		projectID = sa.ProjectID
	}
	if projectID == "" {
		return nil, errors.New("fcm: project id is required")
	}
	tokenURI := sa.TokenURI
	if tokenURI == "" {
		tokenURI = "https://oauth2.googleapis.com/token"
	}
	if baseURL == "" {
		baseURL = "https://fcm.googleapis.com"
	}
	if scope == "" {
		scope = "https://www.googleapis.com/auth/firebase.messaging"
	}
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	key, err := parseRSAPrivateKey(sa.PrivateKey)
	if err != nil {
		return nil, err
	}
	client := &http.Client{Timeout: timeout}
	return &FCMSender{
		projectID: projectID,
		baseURL:   strings.TrimSuffix(baseURL, "/"),
		client:    client,
		tokens: &serviceAccountTokenSource{
			clientEmail: sa.ClientEmail,
			key:         key,
			tokenURI:    tokenURI,
			scope:       scope,
			client:      client,
		},
	}, nil
}

// Send posts one data-only message. A permanent token rejection is returned as
// ErrTokenUnregistered.
func (s *FCMSender) Send(ctx context.Context, deviceToken string, data map[string]string) error {
	accessToken, err := s.tokens.Token(ctx)
	if err != nil {
		return err
	}
	body, err := json.Marshal(map[string]any{
		"message": map[string]any{
			"token":   deviceToken,
			"data":    data,
			"android": map[string]any{"priority": "high"},
		},
	})
	if err != nil {
		return err
	}
	endpoint := fmt.Sprintf("%s/v1/projects/%s/messages:send", s.baseURL, url.PathEscape(s.projectID))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	payload, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
	if isUnregisteredFCMError(resp.StatusCode, payload) {
		return ErrTokenUnregistered
	}
	return fmt.Errorf("fcm send: status %d: %s", resp.StatusCode, strings.TrimSpace(string(payload)))
}

// isUnregisteredFCMError reports whether an FCM error body means the device token
// is permanently invalid.
func isUnregisteredFCMError(status int, body []byte) bool {
	var parsed struct {
		Error struct {
			Status  string `json:"status"`
			Message string `json:"message"`
			Details []struct {
				ErrorCode string `json:"errorCode"`
			} `json:"details"`
		} `json:"error"`
	}
	_ = json.Unmarshal(body, &parsed)
	switch parsed.Error.Status {
	case "UNREGISTERED", "NOT_FOUND":
		return true
	case "INVALID_ARGUMENT":
		for _, d := range parsed.Error.Details {
			if d.ErrorCode == "UNREGISTERED" {
				return true
			}
		}
		return strings.Contains(strings.ToLower(parsed.Error.Message), "registration token")
	}
	return status == http.StatusNotFound
}

func parseRSAPrivateKey(pemKey string) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(pemKey))
	if block == nil {
		return nil, errors.New("fcm: service account private_key is not PEM")
	}
	if k, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		rsaKey, ok := k.(*rsa.PrivateKey)
		if !ok {
			return nil, errors.New("fcm: service account private key is not RSA")
		}
		return rsaKey, nil
	}
	rsaKey, err := x509.ParsePKCS1PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("fcm: parse service account private key: %w", err)
	}
	return rsaKey, nil
}

// serviceAccountTokenSource exchanges a signed JWT for an OAuth2 access token and
// caches it until shortly before expiry.
type serviceAccountTokenSource struct {
	clientEmail string
	key         *rsa.PrivateKey
	tokenURI    string
	scope       string
	client      *http.Client

	mu     sync.Mutex
	token  string
	expiry time.Time
}

func (t *serviceAccountTokenSource) Token(ctx context.Context) (string, error) {
	t.mu.Lock()
	if t.token != "" && time.Now().Before(t.expiry) {
		token := t.token
		t.mu.Unlock()
		return token, nil
	}
	t.mu.Unlock()

	assertion, err := t.signedAssertion()
	if err != nil {
		return "", err
	}
	form := url.Values{
		"grant_type": {"urn:ietf:params:oauth:grant-type:jwt-bearer"},
		"assertion":  {assertion},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.tokenURI, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := t.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("fcm token exchange: status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var parsed struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", fmt.Errorf("fcm token exchange: %w", err)
	}
	if parsed.AccessToken == "" {
		return "", errors.New("fcm token exchange: empty access_token")
	}
	ttl := time.Duration(parsed.ExpiresIn) * time.Second
	if ttl <= 0 {
		ttl = time.Hour
	}
	t.mu.Lock()
	t.token = parsed.AccessToken
	t.expiry = time.Now().Add(ttl - time.Minute)
	t.mu.Unlock()
	return parsed.AccessToken, nil
}

func (t *serviceAccountTokenSource) signedAssertion() (string, error) {
	now := time.Now()
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	claims, err := json.Marshal(map[string]any{
		"iss":   t.clientEmail,
		"scope": t.scope,
		"aud":   t.tokenURI,
		"iat":   now.Unix(),
		"exp":   now.Add(time.Hour).Unix(),
	})
	if err != nil {
		return "", err
	}
	signingInput := header + "." + base64.RawURLEncoding.EncodeToString(claims)
	digest := sha256.Sum256([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, t.key, crypto.SHA256, digest[:])
	if err != nil {
		return "", err
	}
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}
