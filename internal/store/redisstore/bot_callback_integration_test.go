package redisstore

import (
	"context"
	"os"
	"testing"
	"time"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

func TestRedisBotCallbackRegistryCrossInstanceCASAndPubSub(t *testing.T) {
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
	a, b := NewBotCallbackRegistryStore(clientA), NewBotCallbackRegistryStore(clientB)
	queryID := time.Now().UnixNano()
	defer a.DeleteBotCallbackPending(context.Background(), 1001, queryID)
	pushes := make(chan store.BotCallbackAnswerPush, 1)
	subscribed := make(chan struct{})
	go func() {
		_ = b.SubscribeBotCallbackAnswers(ctx, func(_ context.Context, push store.BotCallbackAnswerPush) {
			select {
			case pushes <- push:
			default:
			}
		})
	}()
	// Subscribe uses Redis' acknowledgement before consuming Channel. Give that
	// acknowledgement one bounded scheduling turn before publishing.
	time.AfterFunc(50*time.Millisecond, func() { close(subscribed) })
	<-subscribed
	created, err := a.PutBotCallbackPending(ctx, store.BotCallbackPending{QueryID: queryID, BotUserID: 1001, UserID: 2001}, time.Second)
	if err != nil || !created {
		t.Fatalf("put created=%v err=%v", created, err)
	}
	if duplicate, err := b.PutBotCallbackPending(ctx, store.BotCallbackPending{QueryID: queryID, BotUserID: 1001, UserID: 2002}, time.Second); err != nil || duplicate {
		t.Fatalf("duplicate=%v err=%v", duplicate, err)
	}
	answer := domain.BotCallbackAnswer{Message: "done", CacheTime: 3}
	if resolved, err := b.ResolveBotCallback(ctx, 9999, queryID, answer); err != nil || resolved {
		t.Fatalf("foreign resolve=%v err=%v", resolved, err)
	}
	if resolved, err := b.ResolveBotCallback(ctx, 1001, queryID, answer); err != nil || !resolved {
		t.Fatalf("owner resolve=%v err=%v", resolved, err)
	}
	if second, err := a.ResolveBotCallback(ctx, 1001, queryID, domain.BotCallbackAnswer{Message: "second"}); err != nil || second {
		t.Fatalf("second resolve=%v err=%v", second, err)
	}
	stored, found, err := a.GetBotCallbackAnswer(ctx, 1001, queryID)
	if err != nil || !found || stored.Message != "done" {
		t.Fatalf("stored=%#v found=%v err=%v", stored, found, err)
	}
	select {
	case push := <-pushes:
		if push.QueryID != queryID || push.BotUserID != 1001 || push.Answer.Message != "done" {
			t.Fatalf("push=%#v", push)
		}
	case <-ctx.Done():
		t.Fatal("missing cross-instance callback pubsub")
	}
}
