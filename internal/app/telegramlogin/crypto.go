package telegramlogin

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"

	"telesrv/internal/domain"
)

const opaqueTokenBytes = 32

func GenerateOpaqueToken() (string, error) {
	raw := make([]byte, opaqueTokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate opaque token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func HashOpaqueToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

func HashClientSecret(pepper []byte, secret string) ([]byte, error) {
	if len(pepper) < 32 || secret == "" {
		return nil, domain.ErrTelegramLoginSecretInvalid
	}
	mac := hmac.New(sha256.New, pepper)
	_, _ = mac.Write([]byte(secret))
	return mac.Sum(nil), nil
}

func VerifyClientSecret(pepper []byte, secret string, expected []byte) bool {
	actual, err := HashClientSecret(pepper, secret)
	if err != nil || len(expected) != sha256.Size {
		return false
	}
	return subtle.ConstantTimeCompare(actual, expected) == 1
}

func PKCEChallenge(verifier string) (string, error) {
	if len(verifier) < 43 || len(verifier) > 128 {
		return "", domain.ErrTelegramLoginPKCEInvalid
	}
	for i := 0; i < len(verifier); i++ {
		c := verifier[i]
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '.' || c == '_' || c == '~') {
			return "", domain.ErrTelegramLoginPKCEInvalid
		}
	}
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:]), nil
}

func ValidatePKCEChallenge(challenge, method string) error {
	if method != "S256" || len(challenge) < 43 || len(challenge) > 128 {
		return domain.ErrTelegramLoginPKCEInvalid
	}
	for i := 0; i < len(challenge); i++ {
		c := challenge[i]
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_') {
			return domain.ErrTelegramLoginPKCEInvalid
		}
	}
	return nil
}

type CodeSealer struct {
	activeKeyID string
	keys        map[string]cipher.AEAD
}

func NewCodeSealer(activeKeyID string, rawKeys map[string][]byte) (*CodeSealer, error) {
	if activeKeyID == "" || len(rawKeys) == 0 {
		return nil, errors.New("telegram login code seal key ring is empty")
	}
	keys := make(map[string]cipher.AEAD, len(rawKeys))
	for keyID, raw := range rawKeys {
		if keyID == "" || len(raw) != 32 {
			return nil, fmt.Errorf("invalid telegram login code seal key %q", keyID)
		}
		block, err := aes.NewCipher(raw)
		if err != nil {
			return nil, fmt.Errorf("create telegram login code seal key %q: %w", keyID, err)
		}
		aead, err := cipher.NewGCM(block)
		if err != nil {
			return nil, fmt.Errorf("create telegram login code sealer %q: %w", keyID, err)
		}
		keys[keyID] = aead
	}
	if _, ok := keys[activeKeyID]; !ok {
		return nil, fmt.Errorf("active telegram login code seal key %q not found", activeKeyID)
	}
	return &CodeSealer{activeKeyID: activeKeyID, keys: keys}, nil
}

func (s *CodeSealer) Seal(plaintext string, aad []byte) (sealed, nonce []byte, keyID string, err error) {
	if s == nil || plaintext == "" {
		return nil, nil, "", domain.ErrTelegramLoginCodeInvalid
	}
	aead := s.keys[s.activeKeyID]
	nonce = make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, nil, "", fmt.Errorf("generate telegram login code nonce: %w", err)
	}
	return aead.Seal(nil, nonce, []byte(plaintext), aad), nonce, s.activeKeyID, nil
}

func (s *CodeSealer) Open(sealed, nonce []byte, keyID string, aad []byte) (string, error) {
	if s == nil {
		return "", domain.ErrTelegramLoginCodeInvalid
	}
	aead, ok := s.keys[keyID]
	if !ok || len(nonce) != aead.NonceSize() {
		return "", domain.ErrTelegramLoginCodeInvalid
	}
	plaintext, err := aead.Open(nil, nonce, sealed, aad)
	if err != nil || len(plaintext) == 0 {
		return "", domain.ErrTelegramLoginCodeInvalid
	}
	return string(plaintext), nil
}
