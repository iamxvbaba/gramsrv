package stargifts

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"
)

const localWithdrawalTTL = 15 * time.Minute

// ErrLocalWithdrawalInsecureBaseURL reports a plain-HTTP public base URL
// outside loopback. Withdrawal links are bearer URLs, so they must not travel
// over a plaintext network; callers decide whether that disables the feature.
var ErrLocalWithdrawalInsecureBaseURL = errors.New("local star gift withdrawal base URL requires HTTPS outside loopback")

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
	if parsed.Scheme == "http" && !localWithdrawalLoopbackHost(parsed.Hostname()) {
		return nil, ErrLocalWithdrawalInsecureBaseURL
	}
	return &LocalWithdrawalProvider{publicBaseURL: publicBaseURL}, nil
}

func localWithdrawalLoopbackHost(host string) bool {
	if strings.EqualFold(strings.TrimSpace(host), "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (p *LocalWithdrawalProvider) Name() string { return "telesrv-local" }

func (p *LocalWithdrawalProvider) CreateWithdrawal(_ context.Context, _ StarGiftWithdrawalProviderRequest) (StarGiftWithdrawalProviderResult, error) {
	return p.createWithdrawal("gift-withdrawal")
}

func (p *LocalWithdrawalProvider) CreateRevenueWithdrawal(_ context.Context, _ ChannelRevenueWithdrawalProviderRequest) (StarGiftWithdrawalProviderResult, error) {
	return p.createWithdrawal("revenue-withdrawal")
}

func (p *LocalWithdrawalProvider) createWithdrawal(path string) (StarGiftWithdrawalProviderResult, error) {
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
		URL:       p.publicBaseURL + "/" + path + "/" + url.PathEscape(token),
		ExpiresAt: int(time.Now().Add(localWithdrawalTTL).Unix()),
	}, nil
}

var _ StarGiftWithdrawalProvider = (*LocalWithdrawalProvider)(nil)
var _ ChannelRevenueWithdrawalProvider = (*LocalWithdrawalProvider)(nil)
