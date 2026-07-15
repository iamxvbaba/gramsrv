package rpc

import (
	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap/zaptest"
	"telesrv/internal/domain"
	"testing"
)

func TestRouterTGUserUsesPersistedLastSeen(t *testing.T) {
	r := New(Config{}, Deps{}, zaptest.NewLogger(t), clock.System)
	got := r.tgUser(domain.User{ID: 1000000002, FirstName: "Bob", LastSeenAt: 1700000000})
	if status, ok := got.Status.(*tg.UserStatusOffline); !ok || status.WasOnline != 1700000000 {
		t.Fatalf("status = %#v, want userStatusOffline was_online=1700000000", got.Status)
	}
}
