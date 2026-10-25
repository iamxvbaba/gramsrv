package redisstore

import (
	"context"
	"os"
	"testing"
	"time"

	"telesrv/internal/domain"
)

func TestRedisBotPreCheckoutRegistryCrossInstanceCASWakeAndTake(t *testing.T) {
	addr := os.Getenv("TELESRV_TEST_REDIS_ADDR")
	if addr == "" {
		t.Skip("set TELESRV_TEST_REDIS_ADDR to run redis integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	clientA, err := Open(ctx, addr, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	defer clientA.Close()
	clientB, err := Open(ctx, addr, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	defer clientB.Close()
	// a is the Core holding the payer's RPC, b the Core the bot answers on.
	a, b := NewBotPreCheckoutRegistryStore(clientA), NewBotPreCheckoutRegistryStore(clientB)
	queryID := time.Now().UnixNano()
	defer a.TakeBotPreCheckout(context.Background(), 1001, queryID)
	created, err := a.PutBotPreCheckoutPending(ctx, queryID, 1001, 2001, time.Second)
	if err != nil || !created {
		t.Fatalf("put created=%v err=%v", created, err)
	}
	if duplicate, err := b.PutBotPreCheckoutPending(ctx, queryID, 1001, 2002, time.Second); err != nil || duplicate {
		t.Fatalf("duplicate=%v err=%v", duplicate, err)
	}
	type waitResult struct {
		answer domain.BotPreCheckoutAnswer
		found  bool
		err    error
	}
	waitCh := make(chan waitResult, 1)
	go func() {
		answer, found, err := a.WaitBotPreCheckoutAnswer(ctx, 1001, queryID)
		waitCh <- waitResult{answer: answer, found: found, err: err}
	}()
	answer := domain.BotPreCheckoutAnswer{OK: false, Error: "out of stock"}
	if resolved, err := b.ResolveBotPreCheckout(ctx, 9999, queryID, answer); err != nil || resolved {
		t.Fatalf("foreign resolve=%v err=%v", resolved, err)
	}
	if resolved, err := b.ResolveBotPreCheckout(ctx, 1001, queryID, answer); err != nil || !resolved {
		t.Fatalf("owner resolve=%v err=%v", resolved, err)
	}
	if second, err := b.ResolveBotPreCheckout(ctx, 1001, queryID, domain.BotPreCheckoutAnswer{OK: true}); err != nil || second {
		t.Fatalf("second resolve=%v err=%v", second, err)
	}
	select {
	case got := <-waitCh:
		if got.err != nil || !got.found || got.answer != answer {
			t.Fatalf("wait answer=%#v found=%v err=%v", got.answer, got.found, got.err)
		}
	case <-ctx.Done():
		t.Fatal("missing cross-instance pre-checkout wake")
	}
	taken, found, err := a.TakeBotPreCheckout(ctx, 1001, queryID)
	if err != nil || !found || taken != answer {
		t.Fatalf("take answer=%#v found=%v err=%v", taken, found, err)
	}
	if resolved, err := b.ResolveBotPreCheckout(ctx, 1001, queryID, domain.BotPreCheckoutAnswer{OK: true}); err != nil || resolved {
		t.Fatalf("resolve after take=%v err=%v", resolved, err)
	}
}

func TestRedisBotPreCheckoutRegistryTakeClosesUnansweredQuestion(t *testing.T) {
	addr := os.Getenv("TELESRV_TEST_REDIS_ADDR")
	if addr == "" {
		t.Skip("set TELESRV_TEST_REDIS_ADDR to run redis integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client, err := Open(ctx, addr, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	s := NewBotPreCheckoutRegistryStore(client)
	queryID := time.Now().UnixNano()
	if created, err := s.PutBotPreCheckoutPending(ctx, queryID, 1001, 2001, time.Second); err != nil || !created {
		t.Fatalf("put created=%v err=%v", created, err)
	}
	waitCtx, waitCancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer waitCancel()
	if _, found, err := s.WaitBotPreCheckoutAnswer(waitCtx, 1001, queryID); err != nil || found {
		t.Fatalf("timed out wait found=%v err=%v", found, err)
	}
	if _, found, err := s.TakeBotPreCheckout(ctx, 9999, queryID); err != nil || found {
		t.Fatalf("foreign take found=%v err=%v", found, err)
	}
	if _, found, err := s.TakeBotPreCheckout(ctx, 1001, queryID); err != nil || found {
		t.Fatalf("take unanswered found=%v err=%v", found, err)
	}
	if resolved, err := s.ResolveBotPreCheckout(ctx, 1001, queryID, domain.BotPreCheckoutAnswer{OK: true}); err != nil || resolved {
		t.Fatalf("late resolve=%v err=%v", resolved, err)
	}
}
