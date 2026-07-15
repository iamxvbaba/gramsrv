package rpc

import (
	"testing"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
)

// TestMessageEffectProjection 验证私聊消息特效 effect 投影到 tg.Message：
// 0 值不下发（flag-optional），非零下发。特效是私聊专属，不涉及频道投影。
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
