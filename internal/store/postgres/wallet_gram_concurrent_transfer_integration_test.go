package postgres

import (
	"context"
	"fmt"
	"github.com/jackc/pgx/v5/pgxpool"
	"sync"
	"telesrv/internal/domain"
	"testing"
	"time"
)

func TestTransferWalletGramConcurrentRetries(t *testing.T) {
	pool := testPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	config := pool.Config()
	config.MaxConns = 1
	config.MinConns = 0
	single, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer single.Close()
	ledger := NewStarGiftLifecycleStore(single, NewMessageStore(single), 0)
	sender := walletGramTestUser(t, ctx, pool, "+18870000101", "ConcurrentSender")
	recipient := walletGramTestUser(t, ctx, pool, "+18870000102", "ConcurrentRecipient")
	fundWalletGram(t, ctx, pool, sender.ID, 1000)
	fundWalletGram(t, ctx, pool, recipient.ID, 0)
	request := domain.WalletGramTransferRequest{FromUserID: sender.ID, ToUserID: recipient.ID, AmountNanoton: 10, Reason: domain.StarsReasonTransfer, Date: int(time.Now().Unix()), Comment: "retry", CommandKey: fmt.Sprintf("concurrent-transfer:%d:%d", sender.ID, recipient.ID)}
	const count = 24
	results := make([]domain.WalletGramTransferResult, count)
	failures := make([]error, count)
	start := make(chan struct{})
	var workers sync.WaitGroup
	for i := 0; i < count; i++ {
		workers.Add(1)
		go func(i int) {
			defer workers.Done()
			<-start
			results[i], failures[i] = ledger.TransferWalletGram(ctx, request)
		}(i)
	}
	close(start)
	workers.Wait()
	for i, err := range failures {
		if err != nil {
			t.Fatalf("retry %d: %v", i, err)
		}
		if results[i].TransactionID <= 0 || results[i].TransactionID != results[0].TransactionID {
			t.Fatalf("different receipt on retry %d", i)
		}
	}
	if walletGramBalance(t, ctx, pool, sender.ID) != 990 || walletGramBalance(t, ctx, pool, recipient.ID) != 10 {
		t.Fatal("concurrent retry moved funds more than once")
	}
	var entries int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM ton_transactions WHERE user_id IN ($1,$2)", sender.ID, recipient.ID).Scan(&entries); err != nil || entries != 2 {
		t.Fatalf("ledger entries=%d err=%v", entries, err)
	}
	conflict := request
	conflict.AmountNanoton++
	if _, err := ledger.TransferWalletGram(ctx, conflict); err == nil {
		t.Fatal("conflicting retry accepted")
	}
}

func TestTransferWalletGramConcurrentChatWrites(t *testing.T) {
	pool := testPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	config := pool.Config()
	config.MaxConns = 2
	config.MinConns = 0
	limited, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer limited.Close()
	messages := NewMessageStore(limited)
	ledger := NewStarGiftLifecycleStore(limited, messages, 0)
	sender := walletGramTestUser(t, ctx, pool, "+18870000103", "MixedSender")
	recipient := walletGramTestUser(t, ctx, pool, "+18870000104", "MixedRecipient")
	fundWalletGram(t, ctx, pool, sender.ID, 1000)
	fundWalletGram(t, ctx, pool, recipient.ID, 0)
	const count = 8
	failures := make(chan error, count*2)
	start := make(chan struct{})
	var workers sync.WaitGroup
	for i := 0; i < count; i++ {
		workers.Add(2)
		go func(i int) {
			defer workers.Done()
			<-start
			_, err := ledger.TransferWalletGram(ctx, domain.WalletGramTransferRequest{FromUserID: sender.ID, ToUserID: recipient.ID, AmountNanoton: 10, Reason: domain.StarsReasonTransfer, Date: int(time.Now().Unix()), CommandKey: fmt.Sprintf("mixed:%d:%d", sender.ID, i)})
			failures <- err
		}(i)
		go func(i int) {
			defer workers.Done()
			<-start
			_, err := messages.SendPrivateText(ctx, domain.SendPrivateTextRequest{SenderUserID: sender.ID, RecipientUserID: recipient.ID, RandomID: int64(100 + i), Message: "concurrent chat", Date: int(time.Now().Unix())})
			failures <- err
		}(i)
	}
	close(start)
	workers.Wait()
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	if walletGramBalance(t, ctx, pool, sender.ID) != 920 || walletGramBalance(t, ctx, pool, recipient.ID) != 80 {
		t.Fatal("chat writes changed transfer results")
	}
}
