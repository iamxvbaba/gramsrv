package rpc

import (
	"context"
	"testing"

	"github.com/iamxvbaba/td/tg"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

// TestMainGetDialogsDeliversMonoforumWithResolvableParent locks the full contract
// of the main getDialogs(ListChannelDialogs) delivery to a channel-direct-messages
// (monoforum) admin: the same response must contain ① the monoforum dialog entry,
// ② the monoforum tg.Channel (Monoforum+Megagroup+Creator=false+linked_monoforum_id
// → parent channel), ③ the parent broadcast channel tg.Channel
// (broadcast+Creator=true+linked_monoforum_id→mono). TDesktop derives MonoforumAdmin
// locally from this (mono.isMegagroup && parent.canAccessMonoforum) and renders the
// conversation as a Direct-Messages container; missing any of them makes the mono
// degrade / disappear from the list.
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

	// ① The monoforum must have a dialog entry (otherwise the client list has no such row).
	var monoDialog bool
	for _, d := range list.Dialogs {
		if d.Peer.ID == monoID && d.Peer.Type == domain.PeerTypeChannel {
			monoDialog = true
		}
	}
	if !monoDialog {
		t.Fatalf("getDialogs missing monoforum dialog %d: %+v", monoID, list.Dialogs)
	}

	// ②③ The projected tg.Channel: the parent channel and mono must be in the same
	// batch and linked bidirectionally; the parent Creator=true (=canAccessMonoforum),
	// mono Creator=false (avoids "You created a group").
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
