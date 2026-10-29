package postgres

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"telesrv/internal/domain"
)

// The wallet gram ledger is the money the in-app wallet shows. A transfer moves
// it between two accounts, and the two sides have to agree: a transfer that
// half applied would leave money that belongs to nobody.

func walletGramTestUser(t *testing.T, ctx context.Context, pool *pgxpool.Pool, phone, name string) domain.User {
	t.Helper()
	users := NewUserStore(pool)
	// The phone is unique per run, so a rerun of the suite does not collide
	// with the rows the previous run left behind.
	suffix := time.Now().UnixNano() % 1_000_000_000
	return createTestUser(t, ctx, users, fmt.Sprintf("%s%09d", phone, suffix), name, "")
}

// fundWalletGram gives an account a balance to move around.
func fundWalletGram(t *testing.T, ctx context.Context, pool *pgxpool.Pool, userID, amount int64) {
	t.Helper()
	if _, err := pool.Exec(ctx, `
INSERT INTO ton_balances(user_id,balance_nanoton,granted,updated_at)
VALUES($1,$2,true,now())
ON CONFLICT (user_id) DO UPDATE SET balance_nanoton=EXCLUDED.balance_nanoton,updated_at=now()`,
		userID, amount); err != nil {
		t.Fatalf("fund wallet gram: %v", err)
	}
}

// walletGramBalance reads the ledger balance of an account.
func walletGramBalance(t *testing.T, ctx context.Context, pool *pgxpool.Pool, userID int64) int64 {
	t.Helper()
	var balance int64
	err := pool.QueryRow(ctx, `SELECT balance_nanoton FROM ton_balances WHERE user_id=$1`, userID).Scan(&balance)
	if errors.Is(err, pgx.ErrNoRows) {
		// An account that never held gram has no balance row at all.
		return 0
	}
	if err != nil {
		t.Fatalf("read wallet gram balance: %v", err)
	}
	return balance
}

// A transfer moves the money and writes both sides of the history, each naming
// the other side as its peer.
func TestTransferWalletGramMovesBothSides(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	lifecycle := newAuthorizedWalletTransferStore(t, pool, NewMessageStore(pool))

	sender := walletGramTestUser(t, ctx, pool, "+18870000001", "GramSender")
	receiver := walletGramTestUser(t, ctx, pool, "+18870000002", "GramReceiver")
	fundWalletGram(t, ctx, pool, sender.ID, 500_000_000_000)
	fundWalletGram(t, ctx, pool, receiver.ID, 1_000_000_000)

	transfer, err := lifecycle.TransferWalletGram(ctx, domain.WalletGramTransferRequest{
		FromUserID: sender.ID, ToUserID: receiver.ID, AmountNanoton: 120_000_000_000,
		Reason: domain.StarsReasonTransfer, Date: 1_700_000_000, Title: "Wallet transfer",
	})
	if err != nil {
		t.Fatalf("transfer wallet gram: %v", err)
	}
	balance := transfer.Balance
	if err != nil {
		t.Fatalf("transfer wallet gram: %v", err)
	}
	if balance != 121_000_000_000 {
		t.Fatalf("receiver balance = %d, want 121000000000", balance)
	}
	if got := walletGramBalance(t, ctx, pool, sender.ID); got != 380_000_000_000 {
		t.Fatalf("sender balance = %d, want 380000000000", got)
	}
	if got := walletGramBalance(t, ctx, pool, receiver.ID); got != 121_000_000_000 {
		t.Fatalf("receiver balance = %d, want 121000000000", got)
	}

	// Both sides of the movement are in the history, each naming the other.
	var amount int64
	var peerType string
	var peerID int64
	var reason string
	if err := pool.QueryRow(ctx, `
SELECT amount_nanoton, reason, peer_type, peer_id FROM ton_transactions
 WHERE user_id=$1 ORDER BY id DESC LIMIT 1`, sender.ID).Scan(&amount, &reason, &peerType, &peerID); err != nil {
		t.Fatalf("read sender transaction: %v", err)
	}
	if amount != -120_000_000_000 || reason != string(domain.StarsReasonTransfer) {
		t.Fatalf("sender transaction = %d %q, want -120000000000 %q", amount, reason, domain.StarsReasonTransfer)
	}
	if peerType != "user" || peerID != receiver.ID {
		t.Fatalf("sender peer = %s/%d, want user/%d", peerType, peerID, receiver.ID)
	}
	if err := pool.QueryRow(ctx, `
SELECT amount_nanoton, reason, peer_type, peer_id FROM ton_transactions
 WHERE user_id=$1 ORDER BY id DESC LIMIT 1`, receiver.ID).Scan(&amount, &reason, &peerType, &peerID); err != nil {
		t.Fatalf("read receiver transaction: %v", err)
	}
	if amount != 120_000_000_000 || peerType != "user" || peerID != sender.ID {
		t.Fatalf("receiver transaction = %d %s/%d, want 120000000000 user/%d", amount, peerType, peerID, sender.ID)
	}
}

// A transfer of more than the sender holds is refused, and nothing moves.
func TestTransferWalletGramRefusesMoreThanHeld(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	lifecycle := newAuthorizedWalletTransferStore(t, pool, NewMessageStore(pool))

	sender := walletGramTestUser(t, ctx, pool, "+18870000003", "GramPoor")
	receiver := walletGramTestUser(t, ctx, pool, "+18870000004", "GramTarget")
	fundWalletGram(t, ctx, pool, sender.ID, 1_000_000_000)

	if _, err := lifecycle.TransferWalletGram(ctx, domain.WalletGramTransferRequest{
		FromUserID: sender.ID, ToUserID: receiver.ID, AmountNanoton: 2_000_000_000,
		Reason: domain.StarsReasonTransfer, Date: 1_700_000_000, Title: "Wallet transfer",
	}); !errors.Is(err, domain.ErrStarsInsufficient) {
		t.Fatalf("err = %v, want %v", err, domain.ErrStarsInsufficient)
	}
	if got := walletGramBalance(t, ctx, pool, sender.ID); got != 1_000_000_000 {
		t.Fatalf("sender balance = %d, want it untouched", got)
	}
	if got := walletGramBalance(t, ctx, pool, receiver.ID); got != 0 {
		t.Fatalf("receiver balance = %d, want 0", got)
	}
}

// A retry after a timeout sends the same command key. The money must move
// once, not twice.
func TestTransferWalletGramIsIdempotent(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	lifecycle := newAuthorizedWalletTransferStore(t, pool, NewMessageStore(pool))

	sender := walletGramTestUser(t, ctx, pool, "+18870000005", "GramRetry")
	receiver := walletGramTestUser(t, ctx, pool, "+18870000006", "GramOnce")
	fundWalletGram(t, ctx, pool, sender.ID, 500_000_000_000)

	// The command key is unique per run: the key is the idempotency token, so
	// a key that was used by an earlier run would replay that run's result.
	key := fmt.Sprintf("wallet-transfer:retry-%d", time.Now().UnixNano())
	first, err := lifecycle.TransferWalletGram(ctx, domain.WalletGramTransferRequest{
		FromUserID: sender.ID, ToUserID: receiver.ID, AmountNanoton: 10_000_000_000,
		Reason: domain.StarsReasonTransfer, Date: 1_700_000_000, Title: "Wallet transfer",
		Comment: "a note", CommandKey: key,
	})
	if err != nil {
		t.Fatalf("first transfer: %v", err)
	}
	second, err := lifecycle.TransferWalletGram(ctx, domain.WalletGramTransferRequest{
		FromUserID: sender.ID, ToUserID: receiver.ID, AmountNanoton: 10_000_000_000,
		Reason: domain.StarsReasonTransfer, Date: 1_700_000_000, Title: "Wallet transfer",
		Comment: "a note", CommandKey: key,
	})
	if err != nil {
		t.Fatalf("replayed transfer: %v", err)
	}
	if first.Balance != second.Balance {
		t.Fatalf("replayed balance = %d, want the first result %d", second.Balance, first.Balance)
	}
	if second.TransactionID != first.TransactionID {
		t.Fatalf("replayed transaction id = %d, want %d", second.TransactionID, first.TransactionID)
	}
	if got := walletGramBalance(t, ctx, pool, receiver.ID); got != 10_000_000_000 {
		t.Fatalf("receiver balance = %d, want 10000000000 (one transfer only)", got)
	}
	if got := walletGramBalance(t, ctx, pool, sender.ID); got != 490_000_000_000 {
		t.Fatalf("sender balance = %d, want 490000000000", got)
	}
}

// A transfer to oneself is not a transfer.
func TestTransferWalletGramRejectsSelfAndBadAmounts(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	lifecycle := newAuthorizedWalletTransferStore(t, pool, NewMessageStore(pool))

	account := walletGramTestUser(t, ctx, pool, "+18870000007", "GramSelf")
	fundWalletGram(t, ctx, pool, account.ID, 5_000_000_000)

	for _, tc := range []struct {
		name           string
		from, to, want int64
	}{
		{"same account", account.ID, account.ID, 1_000_000_000},
		{"zero amount", account.ID, account.ID + 1, 0},
		{"negative amount", account.ID, account.ID + 1, -1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := lifecycle.TransferWalletGram(ctx, domain.WalletGramTransferRequest{
				FromUserID: tc.from, ToUserID: tc.to, AmountNanoton: tc.want,
				Reason: domain.StarsReasonTransfer, Date: 1_700_000_000, Title: "Wallet transfer",
			}); !errors.Is(err, domain.ErrStarGiftOwnerInvalid) {
				t.Fatalf("err = %v, want %v", err, domain.ErrStarGiftOwnerInvalid)
			}
		})
	}
}

// A transfer tells the chat about itself: the service message is what the
// client renders, and it is written in the same transaction as the movement.
func TestTransferWalletGramWritesTheServiceMessage(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	messages := NewMessageStore(pool)
	lifecycle := newAuthorizedWalletTransferStore(t, pool, messages)

	sender := walletGramTestUser(t, ctx, pool, "+18870000010", "GramTalker")
	receiver := walletGramTestUser(t, ctx, pool, "+18870000011", "GramListener")
	fundWalletGram(t, ctx, pool, sender.ID, 100_000_000_000)

	transfer, err := lifecycle.TransferWalletGram(ctx, domain.WalletGramTransferRequest{
		FromUserID:    sender.ID,
		ToUserID:      receiver.ID,
		AmountNanoton: 25_000_000_000,
		Reason:        domain.StarsReasonTransfer,
		PeerAddress:   "EQ-receiver",
		Comment:       "for the gift",
		Date:          1_700_000_000,
		Title:         "Wallet transfer",
	})
	if err != nil {
		t.Fatalf("transfer wallet gram: %v", err)
	}
	if transfer.Send.SenderMessage.ID <= 0 {
		t.Fatalf("the transfer sent no service message: %+v", transfer.Send)
	}
	// The message is in the private chat between the two accounts, and it
	// carries the movement: the amount, the address it went to, the ledger
	// row and the note.
	var kind, peerAddress, comment, transactionID string
	var amount int64
	if err := pool.QueryRow(ctx, `
SELECT media->'service_action'->>'kind',
       media->'service_action'->'gram_transfer'->>'peer_address',
       media->'service_action'->'gram_transfer'->>'comment',
       media->'service_action'->'gram_transfer'->>'transaction_id',
       (media->'service_action'->'gram_transfer'->>'amount')::bigint
FROM private_messages
WHERE sender_user_id=$1 AND recipient_user_id=$2
ORDER BY id DESC LIMIT 1`, sender.ID, receiver.ID).
		Scan(&kind, &peerAddress, &comment, &transactionID, &amount); err != nil {
		t.Fatalf("read the service message: %v", err)
	}
	if kind != string(domain.MessageServiceActionGramTransfer) {
		t.Fatalf("service action = %q, want %q", kind, domain.MessageServiceActionGramTransfer)
	}
	if amount != 25_000_000_000 {
		t.Fatalf("the message amount = %d, want 25000000000", amount)
	}
	if peerAddress != "EQ-receiver" {
		t.Fatalf("the message address = %q, want %q", peerAddress, "EQ-receiver")
	}
	if comment != "for the gift" {
		t.Fatalf("the message comment = %q, want %q", comment, "for the gift")
	}
	if transactionID == "" {
		t.Fatalf("the message has no transaction id: %q", transactionID)
	}
	if want := strconv.FormatInt(transfer.TransactionID, 10); transactionID != want {
		t.Fatalf("the message transaction id = %q, want %q", transactionID, want)
	}
}

// A transfer between two accounts in both directions: each side sends
// the same amount to the other, and the service message tells the chat about
// the movement both times. Balances are checked before and after, so the test
// proves the ledger moves the money correctly without anything getting lost.
//
// The amount is 5049.9 gram = 5049900000000 nanoton.
func TestTransferWalletGramBothDirections(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	messages := NewMessageStore(pool)
	lifecycle := newAuthorizedWalletTransferStore(t, pool, messages)

	const amountNanoton = 5_049_900_000_000 // 5049.9 gram

	sender := walletGramTestUser(t, ctx, pool, "+18870000011", "RoundTripSender")
	recipient := walletGramTestUser(t, ctx, pool, "+18870000012", "RoundTripRecipient")
	fromUserID, toUserID := sender.ID, recipient.ID

	// Fund both sides with enough gram for the round trip.
	fundWalletGram(t, ctx, pool, fromUserID, amountNanoton*2)
	fundWalletGram(t, ctx, pool, toUserID, amountNanoton*2)

	fromBefore := walletGramBalance(t, ctx, pool, fromUserID)
	toBefore := walletGramBalance(t, ctx, pool, toUserID)

	// Direction 1: fromUserID sends to toUserID.
	transfer1, err := lifecycle.TransferWalletGram(ctx, domain.WalletGramTransferRequest{
		FromUserID:    fromUserID,
		ToUserID:      toUserID,
		AmountNanoton: amountNanoton,
		Reason:        domain.StarsReasonTransfer,
		PeerAddress:   "EQ-receiver",
		Comment:       "for the test",
		Date:          1_700_000_000,
		Title:         "Wallet transfer",
	})
	if err != nil {
		t.Fatalf("transfer 1 wallet gram: %v", err)
	}
	if transfer1.TransactionID <= 0 {
		t.Fatalf("transfer 1 produced no transaction id")
	}
	if transfer1.Balance != toBefore+amountNanoton {
		t.Fatalf("transfer 1 receiver balance = %d, want %d", transfer1.Balance, toBefore+amountNanoton)
	}

	// Direction 2: toUserID sends back to fromUserID.
	transfer2, err := lifecycle.TransferWalletGram(ctx, domain.WalletGramTransferRequest{
		FromUserID:    toUserID,
		ToUserID:      fromUserID,
		AmountNanoton: amountNanoton,
		Reason:        domain.StarsReasonTransfer,
		PeerAddress:   "EQ-sender",
		Comment:       "back to you",
		Date:          1_700_000_001,
		Title:         "Wallet transfer",
	})
	if err != nil {
		t.Fatalf("transfer 2 wallet gram: %v", err)
	}
	if transfer2.TransactionID <= 0 {
		t.Fatalf("transfer 2 produced no transaction id")
	}
	if transfer2.Balance != fromBefore {
		t.Fatalf("transfer 2 sender balance = %d, want %d", transfer2.Balance, fromBefore)
	}

	// After both transfers, balances are back where they started.
	if got := walletGramBalance(t, ctx, pool, fromUserID); got != fromBefore {
		t.Fatalf("from balance after round trip = %d, want %d", got, fromBefore)
	}
	if got := walletGramBalance(t, ctx, pool, toUserID); got != toBefore {
		t.Fatalf("to balance after round trip = %d, want %d", got, toBefore)
	}

	// Both service messages exist in the private chat, each carrying the
	// movement: amount, address, transaction id, comment.
	var count int
	if err := pool.QueryRow(ctx, `
SELECT count(*) FROM private_messages
WHERE (sender_user_id=$1 AND recipient_user_id=$2)
   OR (sender_user_id=$2 AND recipient_user_id=$1)
   AND media->'service_action'->>'kind'=$3`, fromUserID, toUserID, string(domain.MessageServiceActionGramTransfer)).Scan(&count); err != nil {
		t.Fatalf("count service messages: %v", err)
	}
	if count != 2 {
		t.Fatalf("service messages = %d, want 2 (one per direction)", count)
	}

	// Direction 1 message: from->to, amount, receiver address.
	var amount1 int64
	var peer1, comment1, txid1 string
	if err := pool.QueryRow(ctx, `
SELECT (media->'service_action'->'gram_transfer'->>'amount')::bigint,
       media->'service_action'->'gram_transfer'->>'peer_address',
       media->'service_action'->'gram_transfer'->>'comment',
       media->'service_action'->'gram_transfer'->>'transaction_id'
FROM private_messages
WHERE sender_user_id=$1 AND recipient_user_id=$2
  AND media->'service_action'->>'kind'=$3
ORDER BY id DESC LIMIT 1`, fromUserID, toUserID, string(domain.MessageServiceActionGramTransfer)).
		Scan(&amount1, &peer1, &comment1, &txid1); err != nil {
		t.Fatalf("read direction 1 service message: %v", err)
	}
	if amount1 != amountNanoton {
		t.Fatalf("direction 1 message amount = %d, want %d", amount1, amountNanoton)
	}
	if peer1 != "EQ-receiver" {
		t.Fatalf("direction 1 message peer address = %q, want %q", peer1, "EQ-receiver")
	}
	if comment1 != "for the test" {
		t.Fatalf("direction 1 message comment = %q, want %q", comment1, "for the test")
	}
	if txid1 != strconv.FormatInt(transfer1.TransactionID, 10) {
		t.Fatalf("direction 1 message transaction id = %q, want %q", txid1, strconv.FormatInt(transfer1.TransactionID, 10))
	}

	// Direction 2 message: to->from, amount, sender address.
	var amount2 int64
	var peer2, comment2, txid2 string
	if err := pool.QueryRow(ctx, `
SELECT (media->'service_action'->'gram_transfer'->>'amount')::bigint,
       media->'service_action'->'gram_transfer'->>'peer_address',
       media->'service_action'->'gram_transfer'->>'comment',
       media->'service_action'->'gram_transfer'->>'transaction_id'
FROM private_messages
WHERE sender_user_id=$1 AND recipient_user_id=$2
  AND media->'service_action'->>'kind'=$3
ORDER BY id DESC LIMIT 1`, toUserID, fromUserID, string(domain.MessageServiceActionGramTransfer)).
		Scan(&amount2, &peer2, &comment2, &txid2); err != nil {
		t.Fatalf("read direction 2 service message: %v", err)
	}
	if amount2 != amountNanoton {
		t.Fatalf("direction 2 message amount = %d, want %d", amount2, amountNanoton)
	}
	if peer2 != "EQ-sender" {
		t.Fatalf("direction 2 message peer address = %q, want %q", peer2, "EQ-sender")
	}
	if comment2 != "back to you" {
		t.Fatalf("direction 2 message comment = %q, want %q", comment2, "back to you")
	}
	if txid2 != strconv.FormatInt(transfer2.TransactionID, 10) {
		t.Fatalf("direction 2 message transaction id = %q, want %q", txid2, strconv.FormatInt(transfer2.TransactionID, 10))
	}
}
