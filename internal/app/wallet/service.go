package wallet

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
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
	// CommitWalletCreated atomically binds a telesrv-generated wallet and its
	// sealed private key. When the challenge was already consumed it returns
	// the stored link with reused=true so a retry cannot mint a second wallet.
	CommitWalletCreated(context.Context, domain.WalletChallenge, domain.WalletLink, *StoredWalletKey, bool, time.Time) (domain.WalletLink, bool, error)
	WalletLinks(context.Context, []int64) ([]domain.WalletLink, error)
}

type Provider interface {
	Perform(context.Context, string, *string, *string) (string, error)
}

// BalanceSource resolves one account's TON balance in nanoton. telesrv's own
// TON ledger implements it today (the same one that backs star-gift proceeds);
// a real chain client replaces it later without touching proof, storage or RPC
// code.
type BalanceSource interface {
	AccountBalance(ctx context.Context, userID int64) (int64, error)
}

// TransactionSource supplies the account's ledger history. It is injected the
// same way BalanceSource is, because the ledger lives in telesrv's own database
// rather than in the wallet state directory: the file store holds keys and
// bindings, not money movement.
type TransactionSource interface {
	AccountTransactions(ctx context.Context, userID int64, query domain.StarsTransactionQuery) (domain.TonTransactionPage, error)
}

// TONLedgerTransactionSource reads history straight out of telesrv's internal
// nanoton ledger.
func TONLedgerTransactionSource(transactions func(context.Context, int64, domain.StarsTransactionQuery) (domain.TonTransactionPage, error)) TransactionSource {
	return transactionSourceFunc(transactions)
}

type transactionSourceFunc func(context.Context, int64, domain.StarsTransactionQuery) (domain.TonTransactionPage, error)

func (f transactionSourceFunc) AccountTransactions(ctx context.Context, userID int64, query domain.StarsTransactionQuery) (domain.TonTransactionPage, error) {
	if f == nil {
		return domain.TonTransactionPage{}, ErrProviderUnavailable
	}
	return f(ctx, userID, query)
}

// WithTransactionSource makes the ledger's history readable through the wallet.
func WithTransactionSource(source TransactionSource) ServiceOption {
	return func(s *Service) {
		s.transactions = source
	}
}

// balanceSourceFunc adapts a plain balance lookup to BalanceSource.
type balanceSourceFunc func(context.Context, int64) (int64, error)

func (f balanceSourceFunc) AccountBalance(ctx context.Context, userID int64) (int64, error) {
	if f == nil {
		return 0, ErrProviderUnavailable
	}
	return f(ctx, userID)
}

// TONLedgerBalanceSource binds wallet balances to telesrv's internal TON
// ledger (nanoton, the same one that credits star-gift proceeds). Passing a nil
// lookup yields a source that always reports the ledger as unavailable.
func TONLedgerBalanceSource(balance func(context.Context, int64) (int64, error)) BalanceSource {
	return balanceSourceFunc(balance)
}

// ServiceOption configures optional capabilities.
type ServiceOption func(*Service)

// WithBalanceSource makes wallet balances resolve from telesrv's internal TON
// ledger instead of a provider. Without it a wallet has no balance source at
// all, and every binding attempt fails closed rather than storing a fake zero.
func WithBalanceSource(source BalanceSource) ServiceOption {
	return func(s *Service) {
		s.balances = source
	}
}

// WithKeyCipher enables server-held wallets: telesrv generates the key pair,
// seals the private key under the master key and keeps the ciphertext in its
// own state directory. Without it inputWalletNew is refused, because telesrv
// must never hold a key it cannot seal.
func WithKeyCipher(keys *KeyCipher) ServiceOption {
	return func(s *Service) {
		s.keys = keys
	}
}

type Service struct {
	store        Store
	provider     Provider
	balances     BalanceSource
	transactions TransactionSource
	transfers    TransferStore
	keys         *KeyCipher
	domain       string
	now          func() time.Time
}

// NewService wires the wallet identity store. provider may be nil: telesrv does
// not contact any TON node by default, and every provider-backed method then
// answers ErrProviderUnavailable instead of pretending a chain exists.
func NewService(store Store, provider Provider, proofDomain string, opts ...ServiceOption) *Service {
	if store == nil || proofDomain == "" {
		panic("wallet: missing service dependency")
	}
	s := &Service{store: store, provider: provider, domain: proofDomain, now: time.Now}
	for _, opt := range opts {
		if opt != nil {
			opt(s)
		}
	}
	return s
}

// accountBalance resolves the account balance from the local TON ledger when
// one is wired, otherwise from the configured provider. It never invents a
// zero: an absent source is an error so a failed lookup cannot persist a
// misleading balance alongside a verified ownership proof.
func (s *Service) accountBalance(ctx context.Context, userID int64, address string) (int64, error) {
	if s.balances != nil {
		return s.balances.AccountBalance(ctx, userID)
	}
	return s.Balance(ctx, address)
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

// State reports the account's current wallet plus its balance from whichever
// source is configured. An unbound account is reported as not-ok rather than as
// an empty address, so the client renders "no wallet" instead of a broken one.
func (s *Service) State(ctx context.Context, userID int64) (domain.WalletLink, int64, bool, error) {
	if userID <= 0 {
		return domain.WalletLink{}, 0, false, domain.ErrWalletProofInvalid
	}
	links, err := s.store.WalletLinks(ctx, []int64{userID})
	if err != nil {
		return domain.WalletLink{}, 0, false, err
	}
	if len(links) == 0 {
		return domain.WalletLink{}, 0, false, nil
	}
	balance, err := s.accountBalance(ctx, userID, links[0].Address)
	if err != nil {
		return domain.WalletLink{}, 0, false, err
	}
	return links[0], balance, true, nil
}

// Create mints a TON wallet for an account and keeps its private key sealed
// under the server master key. The client never receives key material: only
// the derived mainnet address and public key leave the process.
//
// Creation consumes the account's current ownership challenge, which is what
// makes a client retry return the same wallet instead of minting a second one.
// Replacing an existing wallet still requires the caller to have passed the
// account password check (allowReplacement).
func (s *Service) Create(ctx context.Context, userID int64, key [8]byte, allowReplacement bool) (domain.WalletLink, int64, error) {
	if s == nil || s.keys == nil {
		return domain.WalletLink{}, 0, ErrWalletCreationDisabled
	}
	if userID <= 0 || key == ([8]byte{}) {
		return domain.WalletLink{}, 0, domain.ErrWalletProofInvalid
	}
	c, found, err := s.store.WalletChallenge(ctx, userID, key)
	if err != nil {
		return domain.WalletLink{}, 0, err
	}
	now := s.now().UTC()
	if !found || !now.Before(c.ExpiresAt) {
		return domain.WalletLink{}, 0, domain.ErrWalletProofInvalid
	}
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return domain.WalletLink{}, 0, err
	}
	addr, err := InitialAddress(public, false)
	if err != nil {
		return domain.WalletLink{}, 0, err
	}
	link := domain.WalletLink{UserID: userID, Address: addr.String(), PublicKey: bytes.Clone(public)}
	// The local ledger is the balance source; without one the creation must
	// fail rather than persist a wallet whose reported balance is fictional.
	balance, err := s.accountBalance(ctx, userID, link.Address)
	if err != nil {
		return domain.WalletLink{}, 0, err
	}
	ciphertext, nonce, err := s.keys.Seal(private)
	// Drop the plaintext key as early as possible; it is never stored or logged.
	clear(private)
	if err != nil {
		return domain.WalletLink{}, 0, err
	}
	stored := &StoredWalletKey{
		Ciphertext: base64.RawStdEncoding.EncodeToString(ciphertext),
		Nonce:      base64.RawStdEncoding.EncodeToString(nonce),
		CreatedAt:  now,
		Origin:     "telesrv",
	}
	// A retry returns the wallet that won the first commit; the candidate key
	// generated above is discarded unwritten.
	committed, _, err := s.store.CommitWalletCreated(ctx, c, link, stored, allowReplacement, now)
	if err != nil {
		return domain.WalletLink{}, 0, err
	}
	return committed, balance, nil
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
	// A failed balance lookup must not manufacture a zero balance or mutate
	// the persisted association. The client retains its local secret on error.
	balance, err := s.accountBalance(ctx, userID, addr.String())
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
	if s == nil || s.provider == nil {
		return 0, ErrProviderUnavailable
	}
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
	if s == nil || s.provider == nil {
		return "", ErrProviderUnavailable
	}
	return s.provider.Perform(ctx, endpoint, query, payload)
}

// Transactions returns history from the configured ledger and fails closed without it.
func (s *Service) Transactions(ctx context.Context, userID int64, query domain.StarsTransactionQuery) (domain.TonTransactionPage, error) {
	if s == nil || userID <= 0 {
		return domain.TonTransactionPage{}, domain.ErrWalletProofInvalid
	}
	if s.transactions == nil {
		return domain.TonTransactionPage{}, ErrProviderUnavailable
	}
	page, err := s.transactions.AccountTransactions(ctx, userID, query)
	if err != nil {
		return domain.TonTransactionPage{}, err
	}
	if page.Transactions == nil {
		page.Transactions = []domain.TonTransaction{}
	}
	return page, nil
}
