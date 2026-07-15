package rpc

import (
	"testing"

	"github.com/iamxvbaba/td/tg"
)

// TestGroupCallJoinAsChannel 覆盖 join_as 身份闭环：admin 以频道身份入会 →
// 参与者行 peer=PeerChannel（本人与对端视角一致）；非 admin 以频道身份被拒；
// getGroupCallJoinAs 对 admin 返回 self+频道两个候选、普通成员只返回 self。
func TestGroupCallJoinAsChannel(t *testing.T) {
	f := newGroupCallFixture(t)
	ownerCtx := f.userCtx(f.owner, 11)
	memberCtx := f.userCtx(f.member, 22)
	channelPeer := &tg.InputPeerChannel{ChannelID: f.channel.ID, AccessHash: f.channel.AccessHash}

	// --- getGroupCallJoinAs 候选 ---
	ownerJoinAs, err := f.router.onPhoneGetGroupCallJoinAs(ownerCtx, channelPeer)
	if err != nil {
		t.Fatalf("owner getGroupCallJoinAs: %v", err)
	}
	if len(ownerJoinAs.Peers) != 2 {
		t.Fatalf("owner join-as peers = %d, want 2 (self + channel): %+v", len(ownerJoinAs.Peers), ownerJoinAs.Peers)
	}
	if _, ok := ownerJoinAs.Peers[1].(*tg.PeerChannel); !ok {
		t.Fatalf("owner join-as second peer = %T, want PeerChannel", ownerJoinAs.Peers[1])
	}
	memberJoinAs, err := f.router.onPhoneGetGroupCallJoinAs(memberCtx, channelPeer)
	if err != nil {
		t.Fatalf("member getGroupCallJoinAs: %v", err)
	}
	if len(memberJoinAs.Peers) != 1 {
		t.Fatalf("member join-as peers = %d, want 1 (self only)", len(memberJoinAs.Peers))
	}

	// --- create + owner 以频道身份 join ---
	createRes, err := f.router.onPhoneCreateGroupCall(ownerCtx, &tg.PhoneCreateGroupCallRequest{
		Peer: channelPeer, RandomID: 1,
	})
	if err != nil {
		t.Fatalf("createGroupCall: %v", err)
	}
	call := findUpdate[*tg.UpdateGroupCall](t, createRes).Call.(*tg.GroupCall)
	input := &tg.InputGroupCall{ID: call.ID, AccessHash: call.AccessHash}

	joinRes, err := f.router.onPhoneJoinGroupCall(ownerCtx, &tg.PhoneJoinGroupCallRequest{
		Call:   input,
		JoinAs: channelPeer,
		Params: groupCallJoinParams(t, 8001),
	})
	if err != nil {
		t.Fatalf("owner joinGroupCall(join_as=channel): %v", err)
	}
	participants := findUpdate[*tg.UpdateGroupCallParticipants](t, joinRes)
	if len(participants.Participants) != 1 {
		t.Fatalf("participants = %d, want 1", len(participants.Participants))
	}
	self := participants.Participants[0]
	peerCh, ok := self.Peer.(*tg.PeerChannel)
	if !ok || peerCh.ChannelID != f.channel.ID {
		t.Fatalf("self participant peer = %#v, want PeerChannel(%d)", self.Peer, f.channel.ID)
	}
	if !self.Self {
		t.Fatalf("join_as channel row missing self flag for the joining user")
	}

	// --- 对端视角：member 拉参与者列表也看到频道身份 ---
	page, err := f.router.onPhoneGetGroupParticipants(memberCtx, &tg.PhoneGetGroupParticipantsRequest{
		Call: input, Limit: 10,
	})
	if err != nil {
		t.Fatalf("member getGroupParticipants: %v", err)
	}
	if len(page.Participants) != 1 {
		t.Fatalf("member sees %d participants, want 1", len(page.Participants))
	}
	if pc, ok := page.Participants[0].Peer.(*tg.PeerChannel); !ok || pc.ChannelID != f.channel.ID {
		t.Fatalf("member view participant peer = %#v, want PeerChannel(%d)", page.Participants[0].Peer, f.channel.ID)
	}
	if page.Participants[0].Self {
		t.Fatalf("member view incorrectly flags channel row as self")
	}

	// --- 非 admin 以频道身份 join 被拒 ---
	if _, err := f.router.onPhoneJoinGroupCall(memberCtx, &tg.PhoneJoinGroupCallRequest{
		Call:   input,
		JoinAs: channelPeer,
		Params: groupCallJoinParams(t, 8002),
	}); err == nil {
		t.Fatalf("non-admin joined as channel")
	} else {
		assertPhoneRPCErr(t, err, "JOIN_AS_PEER_INVALID")
	}

	// --- rejoin 换回本人身份：行替换而非新增 ---
	rejoinRes, err := f.router.onPhoneJoinGroupCall(ownerCtx, &tg.PhoneJoinGroupCallRequest{
		Call:   input,
		JoinAs: &tg.InputPeerSelf{},
		Params: groupCallJoinParams(t, 8003),
	})
	if err != nil {
		t.Fatalf("owner rejoin as self: %v", err)
	}
	rejoined := findUpdate[*tg.UpdateGroupCallParticipants](t, rejoinRes).Participants[0]
	if _, ok := rejoined.Peer.(*tg.PeerUser); !ok {
		t.Fatalf("rejoin-as-self participant peer = %#v, want PeerUser", rejoined.Peer)
	}
	page2, _ := f.router.onPhoneGetGroupParticipants(memberCtx, &tg.PhoneGetGroupParticipantsRequest{Call: input, Limit: 10})
	if len(page2.Participants) != 1 {
		t.Fatalf("after identity switch participants = %d, want 1 (replace not add)", len(page2.Participants))
	}
}
