package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tlprofile"
	"go.uber.org/zap/zaptest"

	"telesrv/internal/domain"
)

// walletTransferGifts is the ledger the transfer RPC drives. It embeds the
// gifts interface, so it only carries what the transfer path needs.
type walletTransferGifts struct {
	GiftsService
	fromUserID  int64
	toUserID    int64
	amount      int64
	reason      domain.StarsTransactionReason
	commandKey  string
	comment     string
	peerAddress string
	transferErr error
	calls       int
}

func (g *walletTransferGifts) TransferWalletGram(_ context.Context, req domain.WalletGramTransferRequest) (domain.WalletGramTransferResult, error) {
	g.calls++
	g.fromUserID = req.FromUserID
	g.toUserID = req.ToUserID
	g.amount = req.AmountNanoton
	g.reason = req.Reason
	g.commandKey = req.CommandKey
	g.comment = req.Comment
	g.peerAddress = req.PeerAddress
	if g.transferErr != nil {
		return domain.WalletGramTransferResult{}, g.transferErr
	}
	return domain.WalletGramTransferResult{Balance: req.AmountNanoton, TransactionID: 4242}, nil
}

func (g *walletTransferGifts) TonBalance(context.Context, int64) (int64, error) {
	return 1_000_000_000, nil
}

// walletTransferUsers resolves the recipient of a transfer.
type walletTransferUsers struct {
	UsersService
}

func (walletTransferUsers) Self(context.Context, int64) (domain.User, error) {
	return domain.User{ID: 42}, nil
}

func (walletTransferUsers) ByID(_ context.Context, _ int64, userID int64) (domain.User, bool, error) {
	return domain.User{ID: userID}, true, nil
}

// newWalletTransferRouter builds a router with a wallet and a ledger.
func newWalletTransferRouter(t *testing.T, gifts *walletTransferGifts) *Router {
	t.Helper()
	return New(Config{}, Deps{
		Wallet: &walletRPCFixture{links: []domain.WalletLink{{
			UserID:    42,
			Address:   "EQ-sender",
			PublicKey: make([]byte, 32),
		}}},
		Gifts: gifts,
		Users: walletTransferUsers{},
	}, zaptest.NewLogger(t), clock.System)
}

// A transfer body in the deployment's own format moves the money to the
// account the request names.
func TestWalletSendTransferMovesGram(t *testing.T) {
	gifts := &walletTransferGifts{}
	r := newWalletTransferRouter(t, gifts)

	body, err := json.Marshal(map[string]any{
		"amount_nanoton": 12_500_000_000,
		"comment":        "for the gift",
	})
	if err != nil {
		t.Fatal(err)
	}
	out := dispatchWallet230(t, r, &tg.WalletSendTransferRequest{
		UserID:     &tg.InputUser{UserID: 7},
		RandomID:   99,
		DataNormal: body,
	})
	if out == nil {
		t.Fatal("the transfer returned nothing")
	}
	if gifts.calls != 1 {
		t.Fatalf("transfer calls = %d, want 1", gifts.calls)
	}
	if gifts.fromUserID != 42 || gifts.toUserID != 7 {
		t.Fatalf("transfer %d -> %d, want 42 -> 7", gifts.fromUserID, gifts.toUserID)
	}
	if gifts.amount != 12_500_000_000 {
		t.Fatalf("amount = %d, want 12500000000", gifts.amount)
	}
	if gifts.reason != domain.StarsReasonTransfer {
		t.Fatalf("reason = %q, want %q", gifts.reason, domain.StarsReasonTransfer)
	}
	// The random id is the idempotency key: a retry must not move the money
	// twice, so the key has to carry it.
	if gifts.commandKey == "" {
		t.Fatal("the transfer carried no command key")
	}
	if gifts.comment != "for the gift" {
		t.Fatalf("comment = %q, want %q", gifts.comment, "for the gift")
	}
}

// A body that is not a transfer is refused, and the ledger is not touched.
func TestWalletSendTransferRejectsABadBody(t *testing.T) {
	gifts := &walletTransferGifts{}
	r := newWalletTransferRouter(t, gifts)

	if _, err := dispatchWallet230Err(t, r, &tg.WalletSendTransferRequest{
		UserID:     &tg.InputUser{UserID: 7},
		RandomID:   100,
		DataNormal: []byte("not a transfer"),
	}); err == nil {
		t.Fatal("want an error for a body that is not a transfer, got none")
	}
	if gifts.calls != 0 {
		t.Fatalf("transfer calls = %d, want 0", gifts.calls)
	}
}

// A transfer to oneself is not a transfer.
func TestWalletSendTransferRejectsSelfTransfer(t *testing.T) {
	gifts := &walletTransferGifts{}
	r := newWalletTransferRouter(t, gifts)

	body, err := json.Marshal(map[string]any{"amount_nanoton": 1_000})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dispatchWallet230Err(t, r, &tg.WalletSendTransferRequest{
		UserID:     &tg.InputUserSelf{},
		RandomID:   101,
		DataNormal: body,
	}); err == nil {
		t.Fatal("want an error for a transfer to oneself, got none")
	}
	if gifts.calls != 0 {
		t.Fatalf("transfer calls = %d, want 0", gifts.calls)
	}
}

// A ledger that refuses the movement answers the client with the balance
// error, not an internal one.
func TestWalletSendTransferReportsInsufficientFunds(t *testing.T) {
	gifts := &walletTransferGifts{transferErr: domain.ErrStarsInsufficient}
	r := newWalletTransferRouter(t, gifts)

	body, err := json.Marshal(map[string]any{"amount_nanoton": 1_000})
	if err != nil {
		t.Fatal(err)
	}
	_, err = dispatchWallet230Err(t, r, &tg.WalletSendTransferRequest{
		UserID:     &tg.InputUser{UserID: 7},
		RandomID:   102,
		DataNormal: body,
	})
	if err == nil {
		t.Fatal("want an error, got none")
	}
	if !errors.Is(err, domain.ErrStarsInsufficient) && !containsBalanceError(err) {
		t.Fatalf("err = %v, want the balance error", err)
	}
}

// dispatchWallet230Err dispatches a Layer 230 request and returns the error.
func dispatchWallet230Err(t *testing.T, r *Router, req bin.Object) (any, error) {
	t.Helper()
	ctx := WithAuthKeyID(WithUserID(context.Background(), 42), [8]byte{1})
	var body bin.Buffer
	if err := req.Encode(&body); err != nil {
		t.Fatal(err)
	}
	admitted, err := r.AdmitLayer(tlprofile.Profile230, &body, tlprofile.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	result, method, err := r.DispatchAdmitted(ctx, [8]byte{1}, 1, 0, 1, admitted)
	if err != nil {
		return nil, err
	}
	var out bin.Buffer
	if err = result.Encode(&out); err != nil {
		t.Fatal(err)
	}
	decoded, err := tlprofile.DecodeObject(tlprofile.Profile230, &out, tlprofile.Limits{})
	if err != nil {
		t.Fatal(method, err)
	}
	return decoded, nil
}

// containsBalanceError reports whether the error is the client's balance
// error.
func containsBalanceError(err error) bool {
	return err != nil && strings.Contains(err.Error(), "BALANCE_TOO_LOW")
}
