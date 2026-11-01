package rpc

import (
	"testing"

	"github.com/iamxvbaba/td/tg"
)

// TestGroupCallJoinAsChannel covers the join_as identity loop: an admin joins as the channel ->
// the participant row's peer=PeerChannel (identical from the user's and the peer's perspective); a
// non-admin joining as the channel is rejected; getGroupCallJoinAs returns two candidates
// (self + channel) for an admin and only self for an ordinary member.
func TestGroupCallJoinAsChannel(t *testing.T) {
	f := newGroupCallFixture(t)
	ownerCtx := f.userCtx(f.owner, 11)
	memberCtx := f.userCtx(f.member, 22)
	channelPeer := &tg.InputPeerChannel{ChannelID: f.channel.ID, AccessHash: f.channel.AccessHash}

	// --- getGroupCallJoinAs candidates ---
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

	// --- create + owner joins as the channel ---
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

	// --- peer perspective: a member fetching the participant list also sees the channel identity ---
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

	// --- a non-admin joining as the channel is rejected ---
	if _, err := f.router.onPhoneJoinGroupCall(memberCtx, &tg.PhoneJoinGroupCallRequest{
		Call:   input,
		JoinAs: channelPeer,
		Params: groupCallJoinParams(t, 8002),
	}); err == nil {
		t.Fatalf("non-admin joined as channel")
	} else {
		assertPhoneRPCErr(t, err, "JOIN_AS_PEER_INVALID")
	}

	// --- rejoin back to the user's own identity: the row is replaced, not added ---
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
