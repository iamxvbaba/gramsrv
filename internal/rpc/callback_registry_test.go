package rpc

import (
	"testing"

	"telesrv/internal/domain"
)

func TestCallbackRegistryResolveDelivers(t *testing.T) {
	reg := newCallbackRegistry()
	queryID, p := reg.register(100, 200)
	if queryID == 0 {
		t.Fatal("query id must be non-zero")
	}
	want := domain.BotCallbackAnswer{Alert: true, Message: "hi"}
	if !reg.resolve(100, queryID, want) {
		t.Fatal("resolve by owner bot must succeed")
	}
	select {
	case got := <-p.ch:
		if got != want {
			t.Fatalf("delivered %+v, want %+v", got, want)
		}
	default:
		t.Fatal("answer not delivered to channel")
	}
	// After unblock the entry is removed: resolving again fails and the registry count drops to zero.
	if reg.resolve(100, queryID, want) {
		t.Fatal("second resolve must fail (already deregistered)")
	}
	if n := reg.size(); n != 0 {
		t.Fatalf("registry size = %d, want 0 after resolve", n)
	}
}

func TestCallbackRegistryNonOwnerRejected(t *testing.T) {
	reg := newCallbackRegistry()
	queryID, p := reg.register(100, 200)
	// A non-owner bot (999) must not unblock (I6).
	if reg.resolve(999, queryID, domain.BotCallbackAnswer{Message: "evil"}) {
		t.Fatal("non-owner resolve must be rejected")
	}
	select {
	case <-p.ch:
		t.Fatal("non-owner answer must not be delivered")
	default:
	}
	if n := reg.size(); n != 1 {
		t.Fatalf("registry size = %d, want 1 (entry retained after rejected resolve)", n)
	}
	reg.deregister(queryID)
	if n := reg.size(); n != 0 {
		t.Fatalf("registry size = %d, want 0 after deregister", n)
	}
}

func TestCallbackRegistryUnknownQuery(t *testing.T) {
	reg := newCallbackRegistry()
	if reg.resolve(100, 12345, domain.BotCallbackAnswer{}) {
		t.Fatal("resolve of unregistered query must fail")
	}
}

func TestCallbackRegistryUniqueQueryIDs(t *testing.T) {
	reg := newCallbackRegistry()
	seen := make(map[int64]struct{})
	for i := 0; i < 1000; i++ {
		q, _ := reg.register(1, 2)
		if _, dup := seen[q]; dup {
			t.Fatalf("duplicate query id %d", q)
		}
		seen[q] = struct{}{}
	}
	if n := reg.size(); n != 1000 {
		t.Fatalf("registry size = %d, want 1000", n)
	}
}
