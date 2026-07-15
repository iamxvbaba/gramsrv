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
