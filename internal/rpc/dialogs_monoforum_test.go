package rpc

import (
	"context"
	"testing"

	"github.com/iamxvbaba/td/tg"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

// TestMainGetDialogsDeliversMonoforumWithResolvableParent 锁定主 getDialogs(ListChannelDialogs)
// 对频道私信(monoforum)管理员投递的完整契约:同一响应里同时含 ① monoforum 的 dialog 条目、
// ② monoforum tg.Channel(Monoforum+Megagroup+Creator=false+linked_monoforum_id→母频道)、
// ③ 母广播频道 tg.Channel(broadcast+Creator=true+linked_monoforum_id→mono)。TDesktop 据此
// (mono.isMegagroup && parent.canAccessMonoforum)本地派生 MonoforumAdmin 并把会话渲染为
// Direct-Messages 容器;缺任一项都会让 mono 退化/从列表消失。
func TestMainGetDialogsDeliversMonoforumWithResolvableParent(t *testing.T) {
	chans := memory.NewChannelStore()
	ctx := context.Background()
	const owner = int64(1)
	bc, err := chans.CreateChannel(ctx, domain.CreateChannelRequest{CreatorUserID: owner, Title: "testmono2", Broadcast: true, Date: 1700000900})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	parentID := bc.Channel.ID
	res, err := chans.SetPaidMessagesPrice(ctx, owner, parentID, 0, true)
	if err != nil {
		t.Fatalf("enable: %v", err)
	}
	monoID := res.Channel.LinkedMonoforumID
	if monoID == 0 {
		t.Fatalf("no monoforum created")
	}

	list, err := chans.ListChannelDialogs(ctx, owner, domain.DialogFilter{Limit: 100})
	if err != nil {
		t.Fatalf("list: %v", err)
	}

	// ① monoforum 必须有 dialog 条目(否则客户端列表里根本没有这一行)。
	var monoDialog bool
	for _, d := range list.Dialogs {
		if d.Peer.ID == monoID && d.Peer.Type == domain.PeerTypeChannel {
			monoDialog = true
		}
	}
	if !monoDialog {
		t.Fatalf("getDialogs missing monoforum dialog %d: %+v", monoID, list.Dialogs)
	}

	// ②③ 投影出的 tg.Channel:母频道与 mono 必须同批且双向 linked,母频道 Creator=true(=canAccessMonoforum),
	// mono Creator=false(避免 "You created a group")。
	chats := tgChannelsForDialogs(owner, list.Channels, list.Dialogs)
	var parent, mono *tg.Channel
	for _, ch := range chats {
		if c, ok := ch.(*tg.Channel); ok {
			switch c.ID {
			case parentID:
				parent = c
			case monoID:
				mono = c
			}
		}
	}
	if parent == nil || mono == nil {
		t.Fatalf("getDialogs chats missing parent(%d)=%v or mono(%d)=%v", parentID, parent != nil, monoID, mono != nil)
	}
	if !parent.Broadcast || !parent.Creator {
		t.Fatalf("parent broadcast=%v creator=%v, want broadcast + creator (canAccessMonoforum)", parent.Broadcast, parent.Creator)
	}
	if id, ok := parent.GetLinkedMonoforumID(); !ok || id != monoID {
		t.Fatalf("parent linked_monoforum_id = %d ok=%v, want →mono %d", id, ok, monoID)
	}
	if !mono.Monoforum || !mono.Megagroup || mono.Creator {
		t.Fatalf("mono monoforum=%v mega=%v creator=%v, want monoforum+megagroup, creator=false", mono.Monoforum, mono.Megagroup, mono.Creator)
	}
	if id, ok := mono.GetLinkedMonoforumID(); !ok || id != parentID {
		t.Fatalf("mono linked_monoforum_id = %d ok=%v, want →parent %d", id, ok, parentID)
	}
}
