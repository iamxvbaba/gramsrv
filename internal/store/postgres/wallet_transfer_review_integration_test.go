package postgres

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"telesrv/internal/domain"
)

func TestTransferWalletGramRejectsStartingGrantPostgres(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	sender := walletGramTestUser(t, ctx, pool, "+17931", "GrantSender")
	receiver := walletGramTestUser(t, ctx, pool, "+17932", "GrantRecipient")
	const grant = int64(10_000_000_000)
	messages := NewMessageStore(pool)
	ledger := NewStarGiftLifecycleStore(pool, messages, grant)
	if balance, err := ledger.TonBalance(ctx, sender.ID); err != nil || balance != grant {
		t.Fatalf("initial wallet read: balance=%d err=%v", balance, err)
	}
	f := newAuthorizedWalletTransferStore(t, pool, messages)
	req, err := f.prepare(ctx, domain.WalletGramTransferRequest{FromUserID: sender.ID, ToUserID: receiver.ID, AmountNanoton: grant, Reason: domain.StarsReasonTransfer, Date: int(time.Now().Unix())})
	if err != nil {
		t.Fatal(err)
	}
	store := NewWalletTransferStore(pool, messages, grant)
	if _, err := store.TransferWalletGram(ctx, req); !errors.Is(err, domain.ErrWalletTransferStartingGrant) {
		t.Fatalf("starting grant transferred: %v", err)
	}
	if walletGramBalance(t, ctx, pool, sender.ID) != grant || walletGramBalance(t, ctx, pool, receiver.ID) != 0 {
		t.Fatal("starting grant refusal changed balances")
	}
	var receipts, proofs int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM wallet_gram_commands WHERE command_key=$1`, req.CommandKey).Scan(&receipts); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM account_tmp_passwords WHERE secret_hash=$1`, req.AuthorizationHash[:]).Scan(&proofs); err != nil {
		t.Fatal(err)
	}
	if receipts != 0 || proofs != 1 {
		t.Fatalf("refusal left receipts=%d proofs=%d", receipts, proofs)
	}
}

func TestTransferWalletGramBlockReceiptAndReplayPostgres(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	sender := walletGramTestUser(t, ctx, pool, "+17933", "BlockedSender")
	receiver := walletGramTestUser(t, ctx, pool, "+17934", "BlockRecipient")
	fundWalletGram(t, ctx, pool, sender.ID, 100)
	f := newAuthorizedWalletTransferStore(t, pool, NewMessageStore(pool))
	req, err := f.prepare(ctx, domain.WalletGramTransferRequest{FromUserID: sender.ID, ToUserID: receiver.ID, AmountNanoton: 1, Reason: domain.StarsReasonTransfer, Date: int(time.Now().Unix()), PeerAddress: "original-recipient-address"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO contact_blocks(owner_user_id,blocked_user_id) VALUES($1,$2)`, receiver.ID, sender.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.TransferWalletGram(ctx, req); !errors.Is(err, domain.ErrWalletTransferRecipientBlocked) {
		t.Fatalf("blocked transfer: %v", err)
	}
	if walletGramBalance(t, ctx, pool, sender.ID) != 100 || walletGramBalance(t, ctx, pool, receiver.ID) != 0 {
		t.Fatal("blocked transfer changed balance")
	}
	var receipts int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM wallet_gram_commands WHERE command_key=$1`, req.CommandKey).Scan(&receipts); err != nil {
		t.Fatal(err)
	}
	if receipts != 0 {
		t.Fatal("blocked transfer wrote a receipt")
	}
	if _, err := pool.Exec(ctx, `DELETE FROM contact_blocks WHERE owner_user_id=$1 AND blocked_user_id=$2`, receiver.ID, sender.ID); err != nil {
		t.Fatal(err)
	}
	// The same proof must still work: blocked attempts never consume it.
	first, err := f.store.TransferWalletGram(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	var owner, amount int64
	if err := pool.QueryRow(ctx, `SELECT user_id,amount_nanoton FROM ton_transactions WHERE id=$1`, first.TransactionID).Scan(&owner, &amount); err != nil {
		t.Fatal(err)
	}
	if owner != sender.ID || amount != -1 {
		t.Fatalf("sender receipt identifies ledger owner=%d amount=%d", owner, amount)
	}
	action := first.Send.SenderMessage.Media.ServiceAction.GramTransfer
	if action.TransactionID != strconv.FormatInt(first.TransactionID, 10) {
		t.Fatal("message and sender receipt disagree")
	}
	retry := req
	retry.PeerAddress = "new-recipient-address"
	second, err := f.store.TransferWalletGram(ctx, retry)
	if err != nil || !second.Duplicate || second.TransactionID != first.TransactionID {
		t.Fatalf("address change broke replay: %+v %v", second, err)
	}
	if second.Send.SenderMessage.Media.ServiceAction.GramTransfer.PeerAddress != req.PeerAddress {
		t.Fatal("retry changed stored message address")
	}
	for _, mutate := range []func(*domain.WalletGramTransferRequest){
		func(r *domain.WalletGramTransferRequest) { r.AuthorizationHash[0] ^= 1 },
		func(r *domain.WalletGramTransferRequest) { r.Comment = "changed intent" },
	} {
		changed := req
		mutate(&changed)
		if _, err := f.store.TransferWalletGram(ctx, changed); !errors.Is(err, domain.ErrStarGiftIdempotencyConflict) {
			t.Fatalf("changed envelope error: %v", err)
		}
	}
	if walletGramBalance(t, ctx, pool, sender.ID) != 99 || walletGramBalance(t, ctx, pool, receiver.ID) != 1 {
		t.Fatal("retry changed balances")
	}
}
