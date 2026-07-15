package rpc

import (
	"testing"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
)

// TestMessageGroupedIDProjection 验证相册 grouped_id 投影到 tg.Message（私聊+频道），
// 0 值不下发（flag-optional），非零下发。
func TestMessageGroupedIDProjection(t *testing.T) {
	const groupedID = int64(880000000123)

	priv := tgMessage(domain.Message{
		ID:        5,
		Peer:      domain.Peer{Type: domain.PeerTypeUser, ID: 1002},
		GroupedID: groupedID,
	}).(*tg.Message)
	if got, ok := priv.GetGroupedID(); !ok || got != groupedID {
		t.Fatalf("private grouped_id = %d ok %v, want %d", got, ok, groupedID)
	}

	plain := tgMessage(domain.Message{ID: 6, Peer: domain.Peer{Type: domain.PeerTypeUser, ID: 1002}}).(*tg.Message)
	if _, ok := plain.GetGroupedID(); ok {
		t.Fatal("non-album private message must not carry grouped_id")
	}

	ch := tgChannelMessage(1001, domain.ChannelMessage{
		ChannelID: 2000,
		ID:        7,
		From:      domain.Peer{Type: domain.PeerTypeUser, ID: 1001},
		GroupedID: groupedID,
	}).(*tg.Message)
	if got, ok := ch.GetGroupedID(); !ok || got != groupedID {
		t.Fatalf("channel grouped_id = %d ok %v, want %d", got, ok, groupedID)
	}
}
