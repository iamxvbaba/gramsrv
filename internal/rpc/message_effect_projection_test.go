package rpc

import (
	"testing"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
)

// TestMessageEffectProjection verifies a private-chat message effect projects to
// tg.Message: the zero value is omitted (flag-optional) and non-zero is sent.
// Effects are private-chat only and do not involve channel projection.
func TestMessageEffectProjection(t *testing.T) {
	const effect = int64(5104841169784993603)

	withEffect := tgMessage(domain.Message{
		ID:     5,
		Peer:   domain.Peer{Type: domain.PeerTypeUser, ID: 1002},
		Effect: effect,
	}).(*tg.Message)
	if got, ok := withEffect.GetEffect(); !ok || got != effect {
		t.Fatalf("message effect = %d ok %v, want %d", got, ok, effect)
	}

	plain := tgMessage(domain.Message{ID: 6, Peer: domain.Peer{Type: domain.PeerTypeUser, ID: 1002}}).(*tg.Message)
	if _, ok := plain.GetEffect(); ok {
		t.Fatal("non-effect message must not carry effect")
	}
}
