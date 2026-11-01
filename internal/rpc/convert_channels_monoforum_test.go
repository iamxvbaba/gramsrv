package rpc

import (
	"testing"

	"telesrv/internal/domain"
)

// TestTgChannelMonoforumSuppressesCreatorChrome locks the core invariant of the
// channel-direct-messages (monoforum) rendering fix: the monoforum virtual channel
// must present as a Direct-Messages container in TDesktop, not a normal group. The
// root cause of the real-device bug was that mono itself was projected with
// Creator=true -- TDesktop's NeedAboutGroup checks amCreator() for megagroup to
// decide whether to paint the "You created a group" group empty state, and the
// Leave/subscriber-count chrome also keys off amCreator/asMegagroup. The DM
// container identity (local MonoforumAdmin flag) is derived client-side from the
// parent channel's canAccessMonoforum, independent of mono itself.
func TestTgChannelMonoforumSuppressesCreatorChrome(t *testing.T) {
	const owner = int64(1001)
	const parentID = int64(5001)
	const monoID = int64(5002)

	mono := domain.Channel{
		ID: monoID, CreatorUserID: owner, Title: "Parent",
		Megagroup: true, Monoforum: true, LinkedMonoforumID: parentID,
		// The monoforum mirrors the parent channel's DM enabled state; linked_monoforum_id is only sent when enabled.
		BroadcastMessagesAllowed: true,
	}
	// Even with a synthetic self carrying the creator role (admin preview), the mono object itself must be Creator=false / carry no admin_rights.
	syntheticSelf := &domain.ChannelMember{ChannelID: monoID, UserID: owner, Status: domain.ChannelMemberActive, Role: domain.ChannelRoleCreator}
	monoTg := tgChannel(owner, mono, syntheticSelf)
	if !monoTg.Megagroup || !monoTg.Monoforum {
		t.Fatalf("mono megagroup=%v monoforum=%v, want both true", monoTg.Megagroup, monoTg.Monoforum)
	}
	if monoTg.Creator {
		t.Fatalf("mono Creator=true, want false (Creator=true paints the wrong group chrome)")
	}
	if _, ok := monoTg.GetAdminRights(); ok {
		t.Fatalf("mono must carry no admin_rights (admin status derived client-side from parent)")
	}
	if monoTg.Left {
		t.Fatalf("mono Left=true, want false")
	}
	if id, ok := monoTg.GetLinkedMonoforumID(); !ok || id != parentID {
		t.Fatalf("mono linked_monoforum_id = %d ok=%v, want parent %d", id, ok, parentID)
	}

	// Projection of the parent broadcast channel for the owner: broadcast-only +
	// Creator=true (canAccessMonoforum goes through the amCreator bypass) +
	// broadcast_messages_allowed + reverse linked_monoforum_id. Together the three
	// let the client derive MonoforumAdmin.
	parent := domain.Channel{
		ID: parentID, CreatorUserID: owner, Title: "Parent",
		Broadcast: true, BroadcastMessagesAllowed: true, LinkedMonoforumID: monoID,
	}
	parentTg := tgChannel(owner, parent, nil)
	if !parentTg.Broadcast || parentTg.Megagroup || parentTg.Monoforum {
		t.Fatalf("parent broadcast=%v mega=%v mono=%v, want broadcast only", parentTg.Broadcast, parentTg.Megagroup, parentTg.Monoforum)
	}
	if !parentTg.Creator {
		t.Fatalf("parent Creator=false for owner viewer, want true (canAccessMonoforum via amCreator)")
	}
	if !parentTg.BroadcastMessagesAllowed {
		t.Fatalf("parent broadcast_messages_allowed=false, want true")
	}
	if id, ok := parentTg.GetLinkedMonoforumID(); !ok || id != monoID {
		t.Fatalf("parent linked_monoforum_id = %d ok=%v, want mono %d", id, ok, monoID)
	}

	// Normal channels (Monoforum=false) are unaffected: creator self is still projected Creator=true.
	normal := domain.Channel{ID: 6001, CreatorUserID: owner, Title: "Normal", Megagroup: true}
	normalTg := tgChannel(owner, normal, &domain.ChannelMember{ChannelID: 6001, UserID: owner, Status: domain.ChannelMemberActive, Role: domain.ChannelRoleCreator})
	if !normalTg.Creator {
		t.Fatalf("normal megagroup creator suppressed; monoforum guard leaked to non-monoforum channels")
	}
}

// TestChannelAdminRightsManageDirectMessagesRoundTrip proves the newly mapped
// manage_direct_messages(flags.17) is bidirectionally lossless and stays inert
// (does not pollute the wire format) for plain admins that do not set the right.
func TestChannelAdminRightsManageDirectMessagesRoundTrip(t *testing.T) {
	tgRights := tgChatAdminRights(domain.ChannelAdminRights{ManageDirectMessages: true, PostMessages: true})
	if !tgRights.ManageDirectMessages {
		t.Fatalf("tg ManageDirectMessages=false, want true")
	}
	if back := domainChannelAdminRights(tgRights); !back.ManageDirectMessages {
		t.Fatalf("round-trip dropped ManageDirectMessages")
	}
	if plain := tgChatAdminRights(domain.ChannelAdminRights{PinMessages: true}); plain.ManageDirectMessages {
		t.Fatalf("normal admin rights wrongly set ManageDirectMessages (not inert)")
	}
}

// TestTgChannelMonoforumDisabledHidesLink locks the projection when Direct Messages
// is disabled: both the parent channel and the monoforum **hide**
// linked_monoforum_id (both represent DM-enabled via BroadcastMessagesAllowed,
// which is false when disabled). The monoforum must hide it too -- otherwise, when
// the user opens the monoforum and re-fetches the channel object, the mono still
// carries the link → setMonoforumLink(parent) triggers the link&&monoforumDisabled
// branch that clears MonoforumDisabled, and the "direct messages disabled" footer
// disappears. The monoforum itself never projects broadcast_messages_allowed (it is
// a megagroup).
func TestTgChannelMonoforumDisabledHidesLink(t *testing.T) {
	const owner = int64(1001)
	const parentID = int64(5001)
	const monoID = int64(5002)

	parent := domain.Channel{ID: parentID, CreatorUserID: owner, Broadcast: true, LinkedMonoforumID: monoID, BroadcastMessagesAllowed: false}
	if _, ok := tgChannel(owner, parent, nil).GetLinkedMonoforumID(); ok {
		t.Fatalf("disabled parent projected linked_monoforum_id, want hidden")
	}

	mono := domain.Channel{ID: monoID, CreatorUserID: owner, Megagroup: true, Monoforum: true, LinkedMonoforumID: parentID, BroadcastMessagesAllowed: false}
	monoTg := tgChannel(owner, mono, nil)
	if _, ok := monoTg.GetLinkedMonoforumID(); ok {
		t.Fatalf("disabled monoforum projected linked_monoforum_id, want hidden (else opening it clears MonoforumDisabled → footer disappears)")
	}
	if !monoTg.Monoforum || !monoTg.Megagroup {
		t.Fatalf("disabled mono monoforum=%v mega=%v, want both still true", monoTg.Monoforum, monoTg.Megagroup)
	}
	if monoTg.BroadcastMessagesAllowed {
		t.Fatalf("monoforum leaked broadcast_messages_allowed, want never projected (it is a megagroup)")
	}
}
