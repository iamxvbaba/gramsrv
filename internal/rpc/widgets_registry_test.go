package rpc

import (
	"testing"

	"telesrv/internal/domain"
)

func TestWidgetRegistryResolveOnlyForOwner(t *testing.T) {
	reg := newWidgetRegistry()
	queryID, pending, err := reg.register(7, 42)
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if queryID == 0 {
		t.Fatal("register returned a zero query id")
	}
	if reg.resolve(8, queryID, domain.BotWidgetQueryAnswer{Result: "forged"}) {
		t.Fatal("a non-owner bot resolved the widget query")
	}
	if !reg.resolve(7, queryID, domain.BotWidgetQueryAnswer{Result: "answer"}) {
		t.Fatal("the owning bot could not resolve the widget query")
	}
	select {
	case ans := <-pending.ch:
		if ans.Result != "answer" {
			t.Fatalf("answer = %q", ans.Result)
		}
	default:
		t.Fatal("the answer was not delivered to the waiter")
	}
	if reg.resolve(7, queryID, domain.BotWidgetQueryAnswer{Result: "again"}) {
		t.Fatal("an already-resolved query id resolved twice")
	}
}

func TestWidgetRegistryDeregisterDropsWaiter(t *testing.T) {
	reg := newWidgetRegistry()
	queryID, _, err := reg.register(7, 42)
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	reg.deregister(queryID)
	if reg.resolve(7, queryID, domain.BotWidgetQueryAnswer{Result: "late"}) {
		t.Fatal("a deregistered query id still resolved")
	}
}
