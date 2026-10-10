package wallet

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"time"

	"telesrv/internal/domain"
)

type Store interface {
	PutWalletChallenge(context.Context, domain.WalletChallenge) error
	WalletChallenge(context.Context, int64, [8]byte) (domain.WalletChallenge, bool, error)
	// CommitWalletLink atomically consumes the exact challenge, compares the
	// existing wallet, and commits a link. Only an identical request may replay.
	CommitWalletLink(context.Context, domain.WalletChallenge, domain.WalletLink, [32]byte, bool, time.Time) error
	WalletLinks(context.Context, []int64) ([]domain.WalletLink, error)
}

type Provider interface {
	Perform(context.Context, string, *string, *string) (string, error)
}

type Service struct {
	store    Store
	provider Provider
	domain   string
	now      func() time.Time
}

func NewService(store Store, provider Provider, proofDomain string) *Service {
	if store == nil || provider == nil || proofDomain == "" {
		panic("wallet: missing service dependency")
	}
	return &Service{store: store, provider: provider, domain: proofDomain, now: time.Now}
}

func (s *Service) GetProofChallenge(ctx context.Context, userID int64, key [8]byte) (domain.WalletChallenge, error) {
	if userID <= 0 || key == ([8]byte{}) {
		return domain.WalletChallenge{}, domain.ErrWalletProofInvalid
	}
	random := make([]byte, 32)
	if _, err := rand.Read(random); err != nil {
		return domain.WalletChallenge{}, err
	}
	now := s.now().UTC().Truncate(time.Second)
	c := domain.WalletChallenge{UserID: userID, AuthKeyID: key, Payload: base64.RawURLEncoding.EncodeToString(random), Domain: s.domain, IssuedAt: now, ExpiresAt: now.Add(5 * time.Minute)}
	if err := s.store.PutWalletChallenge(ctx, c); err != nil {
		return domain.WalletChallenge{}, err
	}
	return c, nil
}

func (s *Service) Links(ctx context.Context, ids []int64) ([]domain.WalletLink, error) {
	if len(ids) > 100 {
		return nil, ErrProviderRequest
	}
	return s.store.WalletLinks(ctx, ids)
}

func (s *Service) ReplaceImported(ctx context.Context, userID int64, key [8]byte, publicKey []byte, timestamp int, signature []byte, allowReplacement bool) (domain.WalletLink, int64, error) {
	if userID <= 0 || len(publicKey) != 32 || len(signature) != 64 || timestamp <= 0 {
		return domain.WalletLink{}, 0, domain.ErrWalletProofInvalid
	}
	c, found, err := s.store.WalletChallenge(ctx, userID, key)
	if err != nil {
		return domain.WalletLink{}, 0, err
	}
	now := s.now().UTC()
	if !found || !now.Before(c.ExpiresAt) || int64(timestamp) < c.IssuedAt.Unix()-30 || int64(timestamp) > now.Unix()+30 {
		return domain.WalletLink{}, 0, domain.ErrWalletProofInvalid
	}
	addr, err := InitialAddress(publicKey, false)
	if err != nil {
		return domain.WalletLink{}, 0, domain.ErrWalletProofInvalid
	}
	if err = VerifyProof(0, addr.Data(), publicKey, c.Domain, uint64(timestamp), c.Payload, signature); err != nil {
		return domain.WalletLink{}, 0, domain.ErrWalletProofInvalid
	}
	// A failed provider lookup must not manufacture a zero balance or mutate
	// the persisted association. The client retains its local secret on error.
	balance, err := s.Balance(ctx, addr.String())
	if err != nil {
		return domain.WalletLink{}, 0, err
	}
	link := domain.WalletLink{UserID: userID, Address: addr.String(), PublicKey: bytes.Clone(publicKey)}
	request := append(bytes.Clone(publicKey), binary.LittleEndian.AppendUint32(nil, uint32(timestamp))...)
	request = append(request, signature...)
	if err = s.store.CommitWalletLink(ctx, c, link, sha256.Sum256(request), allowReplacement, now); err != nil {
		return domain.WalletLink{}, 0, err
	}
	return link, balance, nil
}

func (s *Service) Balance(ctx context.Context, address string) (int64, error) {
	query := url.Values{"address": {address}}.Encode()
	raw, err := s.provider.Perform(ctx, "/api/v2/getAddressBalance", &query, nil)
	if err != nil {
		return 0, err
	}
	var response struct {
		OK     bool            `json:"ok"`
		Result json.RawMessage `json:"result"`
	}
	if json.Unmarshal([]byte(raw), &response) != nil || !response.OK {
		return 0, ErrProviderResponse
	}
	var value string
	if json.Unmarshal(response.Result, &value) != nil {
		value = string(response.Result)
	}
	n, err := strconv.ParseInt(value, 10, 64)
	if err != nil || n < 0 {
		return 0, ErrProviderResponse
	}
	return n, nil
}

func (s *Service) Perform(ctx context.Context, endpoint string, query, payload *string) (string, error) {
	if s == nil {
		return "", fmt.Errorf("wallet service unavailable")
	}
	return s.provider.Perform(ctx, endpoint, query, payload)
}
