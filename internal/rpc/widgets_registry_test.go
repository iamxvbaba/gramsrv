package rpc

import (
	"context"
	"testing"
	"time"

	"telesrv/internal/domain"
)

func TestWidgetRegistryResolveOnlyForOwner(t *testing.T) {
	reg := newWidgetRegistry()
	queryID, pending, err := reg.registerContext(context.Background(), time.Now(), 7, 42, widgetQueryTimeout)
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if queryID == 0 {
		t.Fatal("register returned a zero query id")
	}
	if resolved, err := reg.resolveContext(context.Background(), 8, queryID, domain.BotWidgetQueryAnswer{Result: "forged"}); err != nil || resolved {
		t.Fatalf("non-owner resolve = %v, %v; want false", resolved, err)
	}
	if resolved, err := reg.resolveContext(context.Background(), 7, queryID, domain.BotWidgetQueryAnswer{Result: "answer"}); err != nil || !resolved {
		t.Fatalf("owner resolve = %v, %v; want true", resolved, err)
	}
	select {
	case ans := <-pending.ch:
		if ans.Result != "answer" {
			t.Fatalf("answer = %q", ans.Result)
		}
	default:
		t.Fatal("the answer was not delivered to the waiter")
	}
	if resolved, _ := reg.resolveContext(context.Background(), 7, queryID, domain.BotWidgetQueryAnswer{Result: "again"}); resolved {
		t.Fatal("an already-resolved query id resolved twice")
	}
}

func TestWidgetRegistryDeregisterDropsWaiter(t *testing.T) {
	reg := newWidgetRegistry()
	queryID, _, err := reg.registerContext(context.Background(), time.Now(), 7, 42, widgetQueryTimeout)
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	reg.deregister(queryID)
	if reg.size() != 0 {
		t.Fatalf("registry size = %d, want 0", reg.size())
	}
	if resolved, _ := reg.resolveContext(context.Background(), 7, queryID, domain.BotWidgetQueryAnswer{Result: "late"}); resolved {
		t.Fatal("a deregistered query id still resolved")
	}
}
