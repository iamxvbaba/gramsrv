package stargifts

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"
)

const localWithdrawalTTL = 15 * time.Minute

// LocalWithdrawalProvider mints the unguessable, short-lived bearer URL used
// by either the legacy local completion page or CustomFragment.
type LocalWithdrawalProvider struct {
	publicBaseURL string
	name          string
}

func NewLocalWithdrawalProvider(publicBaseURL string) (*LocalWithdrawalProvider, error) {
	publicBaseURL = strings.TrimRight(strings.TrimSpace(publicBaseURL), "/")
	parsed, err := url.Parse(publicBaseURL)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" ||
		(parsed.Scheme != "http" && parsed.Scheme != "https") {
		return nil, fmt.Errorf("invalid local star gift withdrawal base URL")
	}
	if parsed.Scheme == "http" && !localWithdrawalLoopbackHost(parsed.Hostname()) {
		return nil, fmt.Errorf("local star gift withdrawal base URL requires HTTPS outside loopback")
	}
	return &LocalWithdrawalProvider{publicBaseURL: publicBaseURL, name: "telesrv-local"}, nil
}

func localWithdrawalLoopbackHost(host string) bool {
	if strings.EqualFold(strings.TrimSpace(host), "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func NewCustomFragmentWithdrawalProvider(publicBaseURL string) (*LocalWithdrawalProvider, error) {
	provider, err := NewLocalWithdrawalProvider(publicBaseURL)
	if err != nil {
		return nil, err
	}
	provider.name = "customfragment-ton-mainnet"
	return provider, nil
}

func (p *LocalWithdrawalProvider) Name() string {
	if p == nil || p.name == "" {
		return "telesrv-local"
	}
	return p.name
}

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
