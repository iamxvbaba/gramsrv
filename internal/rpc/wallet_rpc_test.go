package rpc

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"path/filepath"
	walletapp "telesrv/internal/app/wallet"
	"testing"
	"time"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"github.com/iamxvbaba/td/tlprofile"
	"go.uber.org/zap"
	"telesrv/internal/domain"
)

type walletRPCFixture struct{ calls int }

func (f *walletRPCFixture) GetProofChallenge(_ context.Context, uid int64, key [8]byte) (domain.WalletChallenge, error) {
	f.calls++
	return domain.WalletChallenge{UserID: uid, AuthKeyID: key, Payload: "challenge", Domain: "wallet.test", ExpiresAt: time.Unix(1800000300, 0)}, nil
}
func (f *walletRPCFixture) Links(context.Context, []int64) ([]domain.WalletLink, error) {
	return nil, nil
}
func (f *walletRPCFixture) ReplaceImported(context.Context, int64, [8]byte, []byte, int, []byte, bool) (domain.WalletLink, int64, error) {
	f.calls++
	return domain.WalletLink{}, 0, domain.ErrWalletProofInvalid
}
func (f *walletRPCFixture) Perform(_ context.Context, endpoint string, query, payload *string) (string, error) {
	f.calls++
	return `{"ok":true,"result":"0"}`, nil
}

func TestWalletLayer228DispatchAndResultWire(t *testing.T) {
	f := &walletRPCFixture{}
	r := New(Config{}, Deps{Wallet: f}, zap.NewNop(), clock.System)
	key := [8]byte{1}
	ctx := WithAuthKeyID(WithUserID(context.Background(), 42), key)
	for _, tc := range []struct {
		req  bin.Object
		want string
		id   uint32
	}{
		{&tg.WalletGetProofChallengeRequest{}, "wallet.getProofChallenge", 0x99e41707},
		{&tg.ToncenterPerformAPIRequestRequest{Endpoint: "/api/v2/getAddressBalance"}, "toncenter.performApiRequest", 0xac8dfe19},
	} {
		var body bin.Buffer
		if err := tc.req.Encode(&body); err != nil {
			t.Fatal(err)
		}
		admitted, err := r.AdmitLayer(tlprofile.Profile228, &body, tlprofile.Limits{})
		if err != nil {
			t.Fatal(err)
		}
		result, method, err := r.DispatchAdmitted(ctx, key, 1, 0, 1, admitted)
		if err != nil || method != tc.want {
			t.Fatal("layer228 dispatch", method, err)
		}
		var out bin.Buffer
		if err = result.Encode(&out); err != nil {
			t.Fatal(err)
		}
		id, err := out.PeekID()
		if err != nil || id != tc.id {
			t.Fatalf("response ID=%x, error=%v", id, err)
		}
		if _, err = tlprofile.DecodeObject(tlprofile.Profile228, &out, tlprofile.Limits{}); err != nil {
			t.Fatal("response decode", err)
		}
		if out.Len() != 0 {
			t.Fatal("response has trailing bytes")
		}
	}
	if f.calls != 2 {
		t.Fatal("handlers not executed")
	}
}

func TestWalletRejectsUnauthenticated(t *testing.T) {
	f := &walletRPCFixture{}
	r := New(Config{}, Deps{Wallet: f}, zap.NewNop(), clock.System)
	if _, _, err := r.walletCaller(context.Background(), "provider", 120); !tgerr.Is(err, "AUTH_KEY_UNREGISTERED") {
		t.Fatal(err)
	}
	ctx := WithUserID(context.Background(), 42)
	if _, _, err := r.walletCaller(ctx, "provider", 120); !tgerr.Is(err, "AUTH_KEY_UNREGISTERED") {
		t.Fatal(err)
	}
	if f.calls != 0 {
		t.Fatal("unauthenticated wallet request reached backend")
	}
}

func TestWalletRejectsInvalidPeerHash(t *testing.T) {
	f := &walletRPCFixture{}
	users := &mapUsersService{users: map[int64]domain.User{
		42: {ID: 42, AccessHash: 101, FirstName: "Alice"},
		43: {ID: 43, AccessHash: 202, FirstName: "Bob"},
	}}
	r := New(Config{}, Deps{Wallet: f, Users: users}, zap.NewNop(), clock.System)
	ctx := WithAuthKeyID(WithUserID(context.Background(), 42), [8]byte{1})
	for _, hash := range []int64{0, 999} {
		_, err := r.onWalletAddresses(ctx, &tg.WalletGetUserAddressesRequest{ID: []tg.InputUserClass{&tg.InputUser{UserID: 43, AccessHash: hash}}})
		if !tgerr.Is(err, "USER_ID_INVALID") {
			t.Fatalf("hash %d: %v", hash, err)
		}
	}
	if _, err := r.onWalletAddresses(ctx, &tg.WalletGetUserAddressesRequest{ID: []tg.InputUserClass{&tg.InputUser{UserID: 43, AccessHash: 202}}}); err != nil {
		t.Fatal(err)
	}
}

func TestWalletLayer228OwnershipAndAddressFlow(t *testing.T) {
	store, err := walletapp.NewFileStore(filepath.Join(t.TempDir(), "wallet"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service := walletapp.NewService(store, &walletRPCFixture{}, "wallet.test")
	users := &mapUsersService{users: map[int64]domain.User{42: {ID: 42, AccessHash: 101, FirstName: "Alice"}}}
	r := New(Config{}, Deps{Wallet: service, Users: users}, zap.NewNop(), clock.System)
	key := [8]byte{1}
	ctx := WithAuthKeyID(WithUserID(context.Background(), 42), key)
	invoke := func(req bin.Object, out bin.Object) {
		t.Helper()
		var body bin.Buffer
		if err := req.Encode(&body); err != nil {
			t.Fatal(err)
		}
		admitted, err := r.AdmitLayer(tlprofile.Profile228, &body, tlprofile.Limits{})
		if err != nil {
			t.Fatal(err)
		}
		result, _, err := r.DispatchAdmitted(ctx, key, 1, 0, 1, admitted)
		if err != nil {
			t.Fatal(err)
		}
		var encoded bin.Buffer
		if err := result.Encode(&encoded); err != nil {
			t.Fatal(err)
		}
		if err := out.Decode(&encoded); err != nil {
			t.Fatal(err)
		}
		if encoded.Len() != 0 {
			t.Fatal("trailing result bytes")
		}
	}
	var challenge tg.WalletProofChallenge
	invoke(&tg.WalletGetProofChallengeRequest{}, &challenge)
	private := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{7}, 32))
	public := []byte(private.Public().(ed25519.PublicKey))
	address, err := walletapp.InitialAddress(public, false)
	if err != nil {
		t.Fatal(err)
	}
	timestamp := time.Now().Unix()
	hash, err := walletapp.ProofHash(0, address.Data(), challenge.Domain, uint64(timestamp), challenge.Payload)
	if err != nil {
		t.Fatal(err)
	}
	var state tg.WalletState
	invoke(&tg.WalletReplaceWalletRequest{Wallet: &tg.InputWalletImported{PublicKey: public, Proof: tg.WalletOwnershipProof{Timestamp: int(timestamp), Signature: ed25519.Sign(private, hash[:])}}}, &state)
	if state.Address != address.String() || !bytes.Equal(state.PublicKey, public) {
		t.Fatal("linked another wallet")
	}
	var addresses tg.WalletUserAddresses
	invoke(&tg.WalletGetUserAddressesRequest{ID: []tg.InputUserClass{&tg.InputUserSelf{}}}, &addresses)
	if len(addresses.Addresses) != 1 || addresses.Addresses[0].UserID != 42 || addresses.Addresses[0].Address != state.Address || len(addresses.Users) != 1 {
		t.Fatalf("wallet lookup mismatch: %+v", addresses)
	}
}
