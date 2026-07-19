package rpc

import (
	"context"
	"testing"

	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap/zaptest"

	appstars "telesrv/internal/app/stars"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

func starsRouter(t *testing.T, grant int64) *Router {
	t.Helper()
	svc := appstars.NewService(memory.NewStarsStore(), appstars.WithStartingGrant(grant))
	return New(Config{}, Deps{Stars: svc}, zaptest.NewLogger(t), clock.System)
}

// getStarsStatus 首读惰性授予后返回真实余额；响应必须是合法 starsStatus
// （balance 必填 + chats/users 非 nil vector）。
func TestOnPaymentsGetStarsStatusGranted(t *testing.T) {
	r := starsRouter(t, 1000)
	ctx := WithUserID(context.Background(), 1000000001)

	status, err := r.onPaymentsGetStarsStatus(ctx, &tg.PaymentsGetStarsStatusRequest{Peer: &tg.InputPeerSelf{}})
	if err != nil {
		t.Fatalf("getStarsStatus: %v", err)
	}
	amount, ok := status.Balance.(*tg.StarsAmount)
	if !ok || amount.Amount != 1000 {
		t.Fatalf("balance = %#v, want StarsAmount 1000", status.Balance)
	}
	if status.Chats == nil || status.Users == nil {
		t.Fatalf("chats/users must be non-nil vectors, got chats=%v users=%v", status.Chats, status.Users)
	}
	// 余额是 flag 外必填字段，不能省略。
	if _, hasHistory := status.GetHistory(); hasHistory {
		t.Fatalf("status (not transactions) should carry no history")
	}
}

// TON 余额未建模：返回 starsTonAmount 的合法响应（不崩客户端）。
func TestOnPaymentsGetStarsStatusTon(t *testing.T) {
	r := starsRouter(t, 1000)
	ctx := WithUserID(context.Background(), 1000000001)
	// SetTon 同时置 flag 位+字段（gotd true-flag：GetTon 读 flag 位，手工 struct 字面量不置位）。
	req := &tg.PaymentsGetStarsStatusRequest{Peer: &tg.InputPeerSelf{}}
	req.SetTon(true)
	status, err := r.onPaymentsGetStarsStatus(ctx, req)
	if err != nil {
		t.Fatalf("getStarsStatus ton: %v", err)
	}
	if _, ok := status.Balance.(*tg.StarsTonAmount); !ok {
		t.Fatalf("ton balance = %#v, want StarsTonAmount", status.Balance)
	}
}

// getStarsTransactions 返回授予流水；keyset 分页末页省略 next_offset（防 DrKLO 死循环）。
func TestOnPaymentsGetStarsTransactions(t *testing.T) {
	r := starsRouter(t, 1000)
	ctx := WithUserID(context.Background(), 1000000001)

	status, err := r.onPaymentsGetStarsTransactions(ctx, &tg.PaymentsGetStarsTransactionsRequest{Peer: &tg.InputPeerSelf{}})
	if err != nil {
		t.Fatalf("getStarsTransactions: %v", err)
	}
	history, ok := status.GetHistory()
	if !ok || len(history) != 1 {
		t.Fatalf("history = %d ok=%v, want 1 grant txn", len(history), ok)
	}
	txn := history[0]
	if amount, ok := txn.Amount.(*tg.StarsAmount); !ok || amount.Amount != 1000 {
		t.Fatalf("grant txn amount = %#v, want +1000", txn.Amount)
	}
	// grant 走 Fragment 对手方（Peer 必填，不可 nil）。
	if _, ok := txn.Peer.(*tg.StarsTransactionPeerFragment); !ok {
		t.Fatalf("grant peer = %#v, want StarsTransactionPeerFragment", txn.Peer)
	}
	// 单页装得下 → 无 next_offset。
	if off, ok := status.GetNextOffset(); ok {
		t.Fatalf("single-page next_offset = %q, want absent (no infinite paging)", off)
	}
}

func TestTGStarsTransactionsPaidMessage(t *testing.T) {
	out := tgStarsTransactions([]domain.StarsTransaction{{
		ID: 1, UserID: 42, Peer: domain.Peer{Type: domain.PeerTypeChannel, ID: 50},
		Amount: -10, Date: 1700002002, Reason: domain.StarsReasonPaidMessage, Title: "Paid message",
	}})
	if len(out) != 1 {
		t.Fatalf("paid-message transactions = %d, want 1", len(out))
	}
	if paid, ok := out[0].GetPaidMessages(); !ok || paid != 1 {
		t.Fatalf("paid_messages = %d/%v, want 1/true", paid, ok)
	}
	if amount, ok := out[0].Amount.(*tg.StarsAmount); !ok || amount.Amount != -10 {
		t.Fatalf("paid-message amount = %#v, want -10", out[0].Amount)
	}
}

// deps.Stars==nil 兜底：返回合法的空 starsStatus（余额 0），不崩。
func TestOnPaymentsGetStarsStatusNilDeps(t *testing.T) {
	r := New(Config{}, Deps{}, zaptest.NewLogger(t), clock.System)
	ctx := WithUserID(context.Background(), 1000000001)
	status, err := r.onPaymentsGetStarsStatus(ctx, &tg.PaymentsGetStarsStatusRequest{Peer: &tg.InputPeerSelf{}})
	if err != nil {
		t.Fatalf("nil-deps getStarsStatus: %v", err)
	}
	if amount, ok := status.Balance.(*tg.StarsAmount); !ok || amount.Amount != 0 {
		t.Fatalf("nil-deps balance = %#v, want StarsAmount 0", status.Balance)
	}
	_ = domain.DefaultStarsStartingGrant
}

type channelLedgerGifts struct {
	GiftsService
	starsBalance int64
	tonBalance   int64
	starsPage    domain.StarsTransactionPage
	tonPage      domain.TonTransactionPage
}

func (s *channelLedgerGifts) ChannelStarsBalance(context.Context, int64) (int64, error) {
	return s.starsBalance, nil
}

func (s *channelLedgerGifts) ChannelStarsTransactions(context.Context, int64, string, int) (domain.StarsTransactionPage, error) {
	return s.starsPage, nil
}

func (s *channelLedgerGifts) ChannelTonBalance(context.Context, int64) (int64, error) {
	return s.tonBalance, nil
}

func (s *channelLedgerGifts) ChannelTonTransactions(context.Context, int64, string, int) (domain.TonTransactionPage, error) {
	return s.tonPage, nil
}

type channelLedgerChannels struct {
	ChannelsService
	view domain.ChannelView
}

func (s *channelLedgerChannels) ResolveChannel(context.Context, int64, int64) (domain.ChannelView, error) {
	return s.view, nil
}

func (s *channelLedgerChannels) GetChannels(context.Context, int64, []int64) ([]domain.ChannelView, error) {
	return []domain.ChannelView{s.view}, nil
}

func TestPaymentsStarsLedgerUsesRequestedChannelOwner(t *testing.T) {
	const viewerID, channelID int64 = 1000000001, 2000000001
	view := domain.ChannelView{
		Channel: domain.Channel{ID: channelID, AccessHash: 9876, Title: "Gift Channel", Broadcast: true, CreatorUserID: viewerID},
		Self:    domain.ChannelMember{ChannelID: channelID, UserID: viewerID, Role: domain.ChannelRoleCreator, Status: domain.ChannelMemberActive},
	}
	gifts := &channelLedgerGifts{
		starsBalance: 20,
		tonBalance:   900,
		starsPage: domain.StarsTransactionPage{Balance: 20, Transactions: []domain.StarsTransaction{{
			ID: 1, Peer: domain.Peer{Type: domain.PeerTypeUser, ID: 1000000002}, Amount: 20, Date: 10, Reason: domain.StarsReasonGift,
		}}},
		tonPage: domain.TonTransactionPage{Balance: 900, Transactions: []domain.TonTransaction{{
			ID: 2, Peer: domain.Peer{Type: domain.PeerTypeChannel, ID: 2000000002}, GiftID: 9, Amount: 900, Date: 11, Reason: domain.StarsReasonGiftResale,
		}}},
	}
	r := New(Config{}, Deps{Gifts: gifts, Channels: &channelLedgerChannels{view: view}}, zaptest.NewLogger(t), clock.System)
	ctx := WithUserID(context.Background(), viewerID)
	peer := &tg.InputPeerChannel{ChannelID: channelID, AccessHash: view.Channel.AccessHash}

	status, err := r.onPaymentsGetStarsStatus(ctx, &tg.PaymentsGetStarsStatusRequest{Peer: peer})
	if err != nil {
		t.Fatalf("get channel stars status: %v", err)
	}
	if amount, ok := status.Balance.(*tg.StarsAmount); !ok || amount.Amount != 20 || len(status.Chats) != 1 {
		t.Fatalf("channel stars status = %+v chats=%d", status.Balance, len(status.Chats))
	}
	revenue, err := r.onPaymentsGetStarsRevenueStats(ctx, &tg.PaymentsGetStarsRevenueStatsRequest{Peer: peer})
	if err != nil {
		t.Fatalf("get channel stars revenue: %v", err)
	}
	if current, ok := revenue.Status.CurrentBalance.(*tg.StarsAmount); !ok || current.Amount != 20 {
		t.Fatalf("channel stars revenue current = %+v", revenue.Status.CurrentBalance)
	}
	if overall, ok := revenue.Status.OverallRevenue.(*tg.StarsAmount); !ok || overall.Amount != 20 || revenue.Status.WithdrawalEnabled {
		t.Fatalf("channel stars revenue overall = %+v withdrawal=%v", revenue.Status.OverallRevenue, revenue.Status.WithdrawalEnabled)
	}

	txnReq := &tg.PaymentsGetStarsTransactionsRequest{Peer: peer, Limit: 20}
	txnReq.SetTon(true)
	transactions, err := r.onPaymentsGetStarsTransactions(ctx, txnReq)
	if err != nil {
		t.Fatalf("get channel ton transactions: %v", err)
	}
	history, ok := transactions.GetHistory()
	if amount, amountOK := transactions.Balance.(*tg.StarsTonAmount); !amountOK || amount.Amount != 900 || !ok || len(history) != 1 || !history[0].StargiftResale {
		t.Fatalf("channel ton transactions = balance=%+v history=%+v", transactions.Balance, history)
	}
	revenueReq := &tg.PaymentsGetStarsRevenueStatsRequest{Peer: peer}
	revenueReq.SetTon(true)
	tonRevenue, err := r.onPaymentsGetStarsRevenueStats(ctx, revenueReq)
	if err != nil {
		t.Fatalf("get channel ton revenue: %v", err)
	}
	if current, ok := tonRevenue.Status.CurrentBalance.(*tg.StarsTonAmount); !ok || current.Amount != 900 {
		t.Fatalf("channel ton revenue current = %+v", tonRevenue.Status.CurrentBalance)
	}
}

func TestPaymentsStarsLedgerRejectsNonAdminChannelReader(t *testing.T) {
	const viewerID, channelID int64 = 1000000001, 2000000001
	view := domain.ChannelView{
		Channel: domain.Channel{ID: channelID, AccessHash: 9876, Title: "Gift Channel", Broadcast: true},
		Self:    domain.ChannelMember{ChannelID: channelID, UserID: viewerID, Role: domain.ChannelRoleMember, Status: domain.ChannelMemberActive},
	}
	r := New(Config{}, Deps{Gifts: &channelLedgerGifts{}, Channels: &channelLedgerChannels{view: view}}, zaptest.NewLogger(t), clock.System)
	ctx := WithUserID(context.Background(), viewerID)
	_, err := r.onPaymentsGetStarsStatus(ctx, &tg.PaymentsGetStarsStatusRequest{Peer: &tg.InputPeerChannel{ChannelID: channelID, AccessHash: view.Channel.AccessHash}})
	if err == nil {
		t.Fatal("non-admin channel ledger read unexpectedly succeeded")
	}
}
