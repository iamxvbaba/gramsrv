package stargifts

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"net/url"
	"strings"
	"time"
)

const localWithdrawalTTL = 15 * time.Minute

// LocalWithdrawalProvider implements the TON/export UX entirely inside
// telesrv. It mints an unguessable, short-lived bearer URL; no external
// blockchain, Fragment endpoint, wallet or network RPC is contacted.
type LocalWithdrawalProvider struct {
	publicBaseURL string
}

func NewLocalWithdrawalProvider(publicBaseURL string) (*LocalWithdrawalProvider, error) {
	publicBaseURL = strings.TrimRight(strings.TrimSpace(publicBaseURL), "/")
	parsed, err := url.Parse(publicBaseURL)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" ||
		(parsed.Scheme != "http" && parsed.Scheme != "https") {
		return nil, fmt.Errorf("invalid local star gift withdrawal base URL")
	}
	return &LocalWithdrawalProvider{publicBaseURL: publicBaseURL}, nil
}

func (p *LocalWithdrawalProvider) Name() string { return "telesrv-local" }

func (p *LocalWithdrawalProvider) CreateWithdrawal(_ context.Context, _ StarGiftWithdrawalProviderRequest) (StarGiftWithdrawalProviderResult, error) {
	if p == nil || p.publicBaseURL == "" {
		return StarGiftWithdrawalProviderResult{}, fmt.Errorf("local star gift withdrawal provider is not configured")
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return StarGiftWithdrawalProviderResult{}, fmt.Errorf("generate local withdrawal token: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	return StarGiftWithdrawalProviderResult{
		RequestID: token,
		URL:       p.publicBaseURL + "/gift-withdrawal/" + url.PathEscape(token),
		ExpiresAt: int(time.Now().Add(localWithdrawalTTL).Unix()),
	}, nil
}

var _ StarGiftWithdrawalProvider = (*LocalWithdrawalProvider)(nil)
