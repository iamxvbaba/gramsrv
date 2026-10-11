package admin

import (
	"context"
	"errors"
	"strings"
	"testing"

	"telesrv/internal/domain"
)

// A fixed bounceable mainnet address for a fixed hash: CanonicalTONAddress
// normalizes whatever form the panel sends, so the tests only need one input
// that is known to parse.
const nftWalletTestAddress = "EQAAAQIDBAUGBwgJCgsMDQ4PEBESExQVFhcYGRobHB0eHx2j"

type fakeUniqueGiftWalletStore struct {
	gift       domain.UniqueStarGift
	found      bool
	bindErr    error
	releaseErr error
	binds      []domain.StarGiftWalletBind
	released   []int64
}

func (f *fakeUniqueGiftWalletStore) ResolveGiftByRef(context.Context, string) (domain.UniqueStarGift, bool, error) {
	return f.gift, f.found, nil
}

func (f *fakeUniqueGiftWalletStore) WalletBindGift(
	_ context.Context,
	req domain.StarGiftWalletBind) (domain.UniqueStarGift, error) {
	f.binds = append(f.binds, req)
	if f.bindErr != nil {
		return domain.UniqueStarGift{}, f.bindErr
	}
	bound := f.gift
	bound.OwnerName = req.WalletName
	bound.OwnerAddress = req.WalletAddress
	bound.Host = domain.Peer{Type: domain.PeerTypeUser, ID: req.HostPeerID}
	return bound, nil
}

func (f *fakeUniqueGiftWalletStore) WalletReleaseGift(
	_ context.Context,
	uniqueGiftID, actorUserID int64) (domain.UniqueStarGift, error) {
	f.released = append(f.released, uniqueGiftID)
	if f.releaseErr != nil {
		return domain.UniqueStarGift{}, f.releaseErr
	}
	released := f.gift
	released.OwnerName = ""
	released.OwnerAddress = ""
	return released, nil
}

func nftWalletTestService(store *fakeUniqueGiftWalletStore) *Service {
	return NewService(Dependencies{Commands: newMemoryCommandRepo(), UniqueGifts: store, Now: fixedNow})
}

func nftWalletMeta(commandID string, dryRun bool) CommandMeta {
	return CommandMeta{
		CommandID: commandID,
		Actor:     "operator",
		Reason:    "manual wallet entry",
		DryRun:    dryRun,
	}
}

func nftWalletGift() domain.UniqueStarGift {
	return domain.UniqueStarGift{
		ID:    42,
		Slug:  "gift-42",
		Owner: domain.Peer{Type: domain.PeerTypeUser, ID: 7},
	}
}

func TestSetNftGiftWalletReasons(t *testing.T) {
	store := &fakeUniqueGiftWalletStore{gift: nftWalletGift(), found: true}
	svc := nftWalletTestService(store)
	for _, tc := range []struct {
		name string
		req  SetNftGiftWalletRequest
		want string
	}{
		{
			name: "empty ref",
			req: SetNftGiftWalletRequest{
				CommandMeta:   nftWalletMeta("dry-nft-wallet-empty-ref", true),
				WalletName:    "Alice",
				WalletAddress: nftWalletTestAddress,
			},
			want: "ref is required",
		},
		{
			name: "missing owner name",
			req: SetNftGiftWalletRequest{
				CommandMeta:   nftWalletMeta("dry-nft-wallet-no-name", true),
				Ref:           "gift-42",
				WalletAddress: nftWalletTestAddress,
			},
			want: "wallet_name is required",
		},
		{
			name: "telegram identity is not an address",
			req: SetNftGiftWalletRequest{
				CommandMeta:   nftWalletMeta("dry-nft-wallet-tme", true),
				Ref:           "gift-42",
				WalletName:    "Alice",
				WalletAddress: "rayo.t.me",
			},
			want: "must be a TON mainnet address, a .ton name or a Telegram username",
		},
		{
			name: "clear with wallet fields",
			req: SetNftGiftWalletRequest{
				CommandMeta: nftWalletMeta("dry-nft-wallet-clear-with-fields", true),
				Ref:         "gift-42",
				Clear:       true,
				WalletName:  "Alice",
			},
			want: "clear must be sent",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := svc.SetNftGiftWallet(context.Background(), tc.req)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err=%v, want contains %q", err, tc.want)
			}
		})
	}
	if len(store.binds) != 0 || len(store.released) != 0 {
		t.Fatalf("invalid requests must not touch the store: binds=%d releases=%d", len(store.binds), len(store.released))
	}
}

func TestSetNftGiftWalletDryRunDoesNotWrite(t *testing.T) {
	store := &fakeUniqueGiftWalletStore{gift: nftWalletGift(), found: true}
	svc := nftWalletTestService(store)
	result, err := svc.SetNftGiftWallet(context.Background(), SetNftGiftWalletRequest{
		CommandMeta:   nftWalletMeta("dry-nft-wallet-preview", true),
		Ref:           "gift-42",
		WalletName:    "Alice",
		WalletAddress: nftWalletTestAddress,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.DryRun || result.Status != string(domain.AdminCommandCompleted) {
		t.Fatalf("result=%+v", result)
	}
	if len(store.binds) != 0 {
		t.Fatalf("dry run wrote to the store: %+v", store.binds)
	}
	if result.Details["owner_address"] == "" {
		t.Fatalf("details=%v", result.Details)
	}
}

func TestSetNftGiftWalletExecuteBinds(t *testing.T) {
	store := &fakeUniqueGiftWalletStore{gift: nftWalletGift(), found: true}
	svc := nftWalletTestService(store)
	result, err := svc.SetNftGiftWallet(context.Background(), SetNftGiftWalletRequest{
		CommandMeta:   nftWalletMeta("exec-nft-wallet-bind", false),
		Ref:           "gift-42",
		WalletName:    "Alice",
		WalletAddress: nftWalletTestAddress,
		HostUserID:    1234,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.DryRun {
		t.Fatalf("execute result marked dry run: %+v", result)
	}
	if len(store.binds) != 1 {
		t.Fatalf("binds=%d", len(store.binds))
	}
	bind := store.binds[0]
	if bind.UniqueGiftID != 42 || bind.WalletName != "Alice" || bind.HostPeerID != 1234 {
		t.Fatalf("bind=%+v", bind)
	}
	if bind.ActorUserID != domain.GiftRelayerUserID {
		t.Fatalf("actor=%d", bind.ActorUserID)
	}
	if result.Details["owner_name"] != "Alice" {
		t.Fatalf("details=%v", result.Details)
	}
}

func TestSetNftGiftWalletRelease(t *testing.T) {
	gift := nftWalletGift()
	gift.OwnerName = "Alice"
	gift.OwnerAddress = nftWalletTestAddress
	store := &fakeUniqueGiftWalletStore{gift: gift, found: true}
	svc := nftWalletTestService(store)
	result, err := svc.SetNftGiftWallet(context.Background(), SetNftGiftWalletRequest{
		CommandMeta: nftWalletMeta("exec-nft-wallet-release", false),
		Ref:         "gift-42",
		Clear:       true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(store.released) != 1 || store.released[0] != 42 {
		t.Fatalf("released=%v", store.released)
	}
	if len(store.binds) != 0 {
		t.Fatalf("release must not bind: %+v", store.binds)
	}
	if result.Details["released_wallet"] == "" {
		t.Fatalf("details=%v", result.Details)
	}
}

func TestSetNftGiftWalletRefusesUnusableGifts(t *testing.T) {
	onChain := nftWalletGift()
	onChain.GiftAddress = nftWalletTestAddress
	for _, tc := range []struct {
		name  string
		gift  domain.UniqueStarGift
		found bool
	}{
		{name: "not found", found: false},
		{name: "burned", found: true, gift: func() domain.UniqueStarGift {
			gift := nftWalletGift()
			gift.Burned = true
			return gift
		}()},
		{name: "on chain", found: true, gift: onChain},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &fakeUniqueGiftWalletStore{gift: tc.gift, found: tc.found}
			svc := nftWalletTestService(store)
			_, err := svc.SetNftGiftWallet(context.Background(), SetNftGiftWalletRequest{
				CommandMeta:   nftWalletMeta("dry-nft-wallet-refuse", true),
				Ref:           "gift-42",
				WalletName:    "Alice",
				WalletAddress: nftWalletTestAddress,
			})
			if !errors.Is(err, domain.ErrStarGiftNotFound) && !errors.Is(err, domain.ErrStarGiftUnavailable) {
				t.Fatalf("err=%v", err)
			}
			if len(store.binds) != 0 || len(store.released) != 0 {
				t.Fatalf("store was touched: binds=%d releases=%d", len(store.binds), len(store.released))
			}
		})
	}
}

type fakeTonDNSResolver struct {
	address string
	err     error
	calls   []string
}

func (f *fakeTonDNSResolver) Resolve(_ context.Context, name string) (string, error) {
	f.calls = append(f.calls, name)
	return f.address, f.err
}

func TestSetNftGiftWalletResolvesTonName(t *testing.T) {
	store := &fakeUniqueGiftWalletStore{gift: nftWalletGift(), found: true}
	dns := &fakeTonDNSResolver{address: nftWalletTestAddress}
	svc := NewService(Dependencies{Commands: newMemoryCommandRepo(), UniqueGifts: store, Now: fixedNow, TonDNS: dns})
	result, err := svc.SetNftGiftWallet(context.Background(), SetNftGiftWalletRequest{
		CommandMeta:   nftWalletMeta("dry-nft-wallet-ton-name", true),
		Ref:           "gift-42",
		WalletName:    "rayo",
		WalletAddress: "@Rayo",
		HostUserID:    1234,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(dns.calls) != 1 || dns.calls[0] != "rayo.ton" {
		t.Fatalf("dns calls=%v", dns.calls)
	}
	if len(store.binds) != 0 {
		t.Fatalf("dry run wrote to the store: %+v", store.binds)
	}
	if got := result.Details["owner_address"]; got != "0:000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f" {
		t.Fatalf("owner_address=%v", got)
	}
	if result.Details["wallet_alias"] != "rayo.ton" {
		t.Fatalf("details=%v", result.Details)
	}
}

func TestSetNftGiftWalletTonNameNeedsResolver(t *testing.T) {
	store := &fakeUniqueGiftWalletStore{gift: nftWalletGift(), found: true}
	svc := nftWalletTestService(store)
	_, err := svc.SetNftGiftWallet(context.Background(), SetNftGiftWalletRequest{
		CommandMeta:   nftWalletMeta("dry-nft-wallet-ton-no-resolver", true),
		Ref:           "gift-42",
		WalletName:    "rayo",
		WalletAddress: "rayo",
	})
	if err == nil || !strings.Contains(err.Error(), "ton dns resolver is not configured") {
		t.Fatalf("err=%v", err)
	}
	if len(store.binds) != 0 || len(store.released) != 0 {
		t.Fatalf("store was touched: binds=%d releases=%d", len(store.binds), len(store.released))
	}
}

func TestSetNftGiftWalletTonNameResolveFailureExplainsNextStep(t *testing.T) {
	store := &fakeUniqueGiftWalletStore{gift: nftWalletGift(), found: true}
	dns := &fakeTonDNSResolver{err: errors.New("rayo.ton is not registered on ton dns")}
	svc := NewService(Dependencies{Commands: newMemoryCommandRepo(), UniqueGifts: store, Now: fixedNow, TonDNS: dns})
	_, err := svc.SetNftGiftWallet(context.Background(), SetNftGiftWalletRequest{
		CommandMeta:   nftWalletMeta("dry-nft-wallet-ton-missing", true),
		Ref:           "gift-42",
		WalletName:    "rayo",
		WalletAddress: "rayo.ton",
	})
	if err == nil ||
		!strings.Contains(err.Error(), "rayo.ton is not registered on ton dns") ||
		!strings.Contains(err.Error(), "enter the wallet address") {
		t.Fatalf("err=%v", err)
	}
	if len(store.binds) != 0 || len(store.released) != 0 {
		t.Fatalf("store was touched: binds=%d releases=%d", len(store.binds), len(store.released))
	}
}

type fakeUsernameWallets struct {
	records map[string]struct {
		address string
		source  string
	}
	err error
}

func (f *fakeUsernameWallets) WalletForUsername(
	_ context.Context,
	username string) (string, string, bool, error) {
	if f.err != nil {
		return "", "", false, f.err
	}
	record, ok := f.records[username]
	return record.address, record.source, ok, nil
}

func nftWalletServiceWithUsername(
	store *fakeUniqueGiftWalletStore,
	wallets *fakeUsernameWallets,
	dns *fakeTonDNSResolver) *Service {
	return NewService(Dependencies{
		Commands:        newMemoryCommandRepo(),
		UniqueGifts:     store,
		Now:             fixedNow,
		TonDNS:          dns,
		UsernameWallets: wallets,
	})
}

func TestSetNftGiftWalletTelegramUsernameUsesVerifiedRecord(t *testing.T) {
	store := &fakeUniqueGiftWalletStore{gift: nftWalletGift(), found: true}
	wallets := &fakeUsernameWallets{records: map[string]struct {
		address string
		source  string
	}{
		"rayo": {address: nftWalletTestAddress, source: "ton connect"},
	}}
	svc := nftWalletServiceWithUsername(store, wallets, nil)
	res, err := svc.SetNftGiftWallet(context.Background(), SetNftGiftWalletRequest{
		CommandMeta:   nftWalletMeta("dry-nft-wallet-username", true),
		Ref:           "gift-42",
		WalletName:    "rayo-wallet",
		WalletAddress: "@Rayo",
	})
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if res.Details["wallet_alias"] != "@Rayo" {
		t.Fatalf("details=%v", res.Details)
	}
	if res.Details["wallet_source"] != "ton connect" {
		t.Fatalf("details=%v", res.Details)
	}
	if res.Details["owner_address"] != "0:000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f" {
		t.Fatalf("details=%v", res.Details)
	}
	if res.Details["owner_name"] != "rayo-wallet" {
		t.Fatalf("details=%v", res.Details)
	}
}

func TestSetNftGiftWalletUsernameLinkFormUsesMintRecord(t *testing.T) {
	store := &fakeUniqueGiftWalletStore{gift: nftWalletGift(), found: true}
	wallets := &fakeUsernameWallets{records: map[string]struct {
		address string
		source  string
	}{
		"rayo": {address: nftWalletTestAddress, source: "collectible username mint"},
	}}
	svc := nftWalletServiceWithUsername(store, wallets, nil)
	res, err := svc.SetNftGiftWallet(context.Background(), SetNftGiftWalletRequest{
		CommandMeta:   nftWalletMeta("dry-nft-wallet-username-link", true),
		Ref:           "gift-42",
		WalletAddress: "t.me/rayo",
	})
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if res.Details["wallet_source"] != "collectible username mint" {
		t.Fatalf("details=%v", res.Details)
	}
	if res.Details["wallet_alias"] != "t.me/rayo" {
		t.Fatalf("details=%v", res.Details)
	}
	if res.Details["owner_name"] != "t.me/rayo" {
		t.Fatalf("details=%v", res.Details)
	}
}

func TestSetNftGiftWalletUsernameMissReportsRecordThenDNS(t *testing.T) {
	store := &fakeUniqueGiftWalletStore{gift: nftWalletGift(), found: true}
	wallets := &fakeUsernameWallets{records: map[string]struct {
		address string
		source  string
	}{}}
	dns := &fakeTonDNSResolver{err: errors.New("rayo.ton is not registered on ton dns")}
	svc := nftWalletServiceWithUsername(store, wallets, dns)
	_, err := svc.SetNftGiftWallet(context.Background(), SetNftGiftWalletRequest{
		CommandMeta:   nftWalletMeta("dry-nft-wallet-username-miss", true),
		Ref:           "gift-42",
		WalletAddress: "rayo",
	})
	if err == nil {
		t.Fatal("expected an error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "no verified wallet is on record in flashfragment for Telegram username rayo") {
		t.Fatalf("msg=%v", msg)
	}
	if !strings.Contains(msg, "rayo.ton is not registered on ton dns") {
		t.Fatalf("msg=%v", msg)
	}
	if !strings.Contains(msg, "flashfragment has no verified wallet for username rayo either") {
		t.Fatalf("msg=%v", msg)
	}
	if !strings.Contains(msg, "enter the wallet address (EQ… or 0:…) instead") {
		t.Fatalf("msg=%v", msg)
	}
}
func TestSetNftGiftWalletTonNameFallsBackToFlashfragment(t *testing.T) {
	store := &fakeUniqueGiftWalletStore{gift: nftWalletGift(), found: true}
	wallets := &fakeUsernameWallets{records: map[string]struct {
		address string
		source  string
	}{
		"rayo": {address: nftWalletTestAddress, source: "collectible username owner wallet"},
	}}
	dns := &fakeTonDNSResolver{err: errors.New("rayo.ton is not registered on ton dns")}
	svc := nftWalletServiceWithUsername(store, wallets, dns)
	res, err := svc.SetNftGiftWallet(context.Background(), SetNftGiftWalletRequest{
		CommandMeta:   nftWalletMeta("dry-nft-wallet-ton-flashfragment", true),
		Ref:           "gift-42",
		WalletAddress: "rayo.ton",
	})
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if res.Details["wallet_alias"] != "rayo.ton" {
		t.Fatalf("details=%v", res.Details)
	}
	if res.Details["wallet_source"] != "collectible username owner wallet" {
		t.Fatalf("details=%v", res.Details)
	}
	if res.Details["owner_address"] != "0:000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f" {
		t.Fatalf("details=%v", res.Details)
	}
}
