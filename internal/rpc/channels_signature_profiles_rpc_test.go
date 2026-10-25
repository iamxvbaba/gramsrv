package rpc

import (
	"testing"
	"time"

	"github.com/iamxvbaba/td/tg"
	"telesrv/internal/domain"
)

func TestChannelNaturalSendAsDefaultsAndViewerIsolation(t *testing.T) {
	f := newRPCChannelFixture(t)
	owner := f.user(901, "15550009901", "Owner")
	member := f.user(902, "15550009902", "Member")
	group := f.createLegacyMegagroup(owner, "Defaults", member)
	for _, viewer := range []domain.User{owner, member, owner} {
		for i := 0; i < 2; i++ { // Cold response and projection-cache hit.
			full, err := f.router.onChannelsGetFullChannel(f.userCtx(viewer), inputChannel(group))
			if err != nil {
				t.Fatal(err)
			}
			peer, ok := full.FullChat.(*tg.ChannelFull).DefaultSendAs.(*tg.PeerUser)
			if !ok || peer.UserID != viewer.ID {
				t.Fatalf("default = %#v, viewer %d", full.FullChat, viewer.ID)
			}
		}
	}
	if _, err := f.users.SetPremiumUntil(f.ctx, owner.ID, int(time.Now().Add(time.Hour).Unix())); err != nil {
		t.Fatal(err)
	}
	foreign := createBroadcastChannelRPC(t, f.router, owner.ID, "Identity")
	if _, err := f.router.onMessagesSaveDefaultSendAs(f.userCtx(owner), &tg.MessagesSaveDefaultSendAsRequest{Peer: inputPeerChannel(group), SendAs: inputPeerChannel(foreign)}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		full, err := f.router.onChannelsGetFullChannel(f.userCtx(owner), inputChannel(group))
		if err != nil {
			t.Fatal(err)
		}
		peer, ok := full.FullChat.(*tg.ChannelFull).DefaultSendAs.(*tg.PeerChannel)
		if !ok || peer.ChannelID != foreign.ID || !chatsContainChannel(full.Chats, foreign.ID) {
			t.Fatal("foreign default lost on cached full info")
		}
	}
	// The target's cache is still warm when eligibility of its foreign identity changes.
	if _, err := f.users.SetPremiumUntil(f.ctx, owner.ID, 0); err != nil {
		t.Fatal(err)
	}
	full, err := f.router.onChannelsGetFullChannel(f.userCtx(owner), inputChannel(group))
	if err != nil {
		t.Fatal(err)
	}
	if peer, ok := full.FullChat.(*tg.ChannelFull).DefaultSendAs.(*tg.PeerUser); !ok || peer.UserID != owner.ID {
		t.Fatal("expired Premium left a stale foreign default")
	}
}

func TestBroadcastSignatureProfilesRPC(t *testing.T) {
	f := newRPCChannelFixture(t)
	owner := f.user(911, "15550009911", "Owner")
	ctx, r := f.userCtx(owner), f.router
	channel := createBroadcastChannelRPC(t, r, owner.ID, "Profiles")
	foreign := createBroadcastChannelRPC(t, r, owner.ID, "Other identity")
	assertDefault := func(want tg.PeerClass) {
		t.Helper()
		for i := 0; i < 2; i++ {
			full, err := r.onChannelsGetFullChannel(ctx, inputChannel(channel))
			if err != nil {
				t.Fatal(err)
			}
			got := full.FullChat.(*tg.ChannelFull).DefaultSendAs
			if !sameSignaturePeer(got, want) {
				t.Fatalf("default = %#v, want %#v", got, want)
			}
		}
	}
	toggle := func(signatures, profiles bool) {
		t.Helper()
		sessions := &captureSessions{onlineUserIDs: []int64{owner.ID}}
		r.deps.Sessions = sessions
		defer func() { r.deps.Sessions = nil }()
		updates, err := r.onChannelsToggleSignatures(ctx, &tg.ChannelsToggleSignaturesRequest{Channel: inputChannel(channel), SignaturesEnabled: signatures, ProfilesEnabled: profiles})
		if err != nil {
			t.Fatal(err)
		}
		pushed, ok := sessions.lastUserPush().(*tg.Updates)
		if !ok || len(pushed.Updates) != 1 {
			t.Fatal("missing channel setting push")
		}
		if _, ok := pushed.Updates[0].(*tg.UpdateChannel); !ok {
			t.Fatal("setting push must not manufacture PTS")
		}
		pushedChannel := pushed.Chats[0].(*tg.Channel)
		if pushedChannel.Signatures != signatures || pushedChannel.SignatureProfiles != profiles {
			t.Fatal("other-session setting flags lost")
		}
		ch := updates.(*tg.Updates).Chats[0].(*tg.Channel)
		if ch.Signatures != signatures || ch.SignatureProfiles != profiles {
			t.Fatalf("toggle flags = %+v", ch)
		}
		full, err := r.onChannelsGetFullChannel(ctx, inputChannel(channel))
		if err != nil {
			t.Fatal(err)
		}
		ch = full.Chats[0].(*tg.Channel)
		if ch.Signatures != signatures || ch.SignatureProfiles != profiles {
			t.Fatal("cached full info retained old flags")
		}
	}
	send := func(id int64, as tg.InputPeerClass) *tg.Message {
		t.Helper()
		updates, err := r.onMessagesSendMessage(ctx, &tg.MessagesSendMessageRequest{Peer: inputPeerChannel(channel), Message: "profile test", RandomID: id, SendAs: as})
		if err != nil {
			t.Fatal(err)
		}
		for _, update := range updates.(*tg.Updates).Updates {
			if u, ok := update.(*tg.UpdateNewChannelMessage); ok {
				return u.Message.(*tg.Message)
			}
		}
		t.Fatal("missing new channel message")
		return nil
	}
	assertDefault(&tg.PeerChannel{ChannelID: channel.ID})
	old := send(9101, nil)
	if old.FromID != nil {
		t.Fatal("anonymous post exposed author")
	}
	if _, err := r.onChannelsToggleSignatures(ctx, &tg.ChannelsToggleSignaturesRequest{Channel: inputChannel(channel), ProfilesEnabled: true}); err == nil {
		t.Fatal("profiles without signatures accepted")
	}
	if _, err := r.onMessagesSaveDefaultSendAs(ctx, &tg.MessagesSaveDefaultSendAsRequest{Peer: inputPeerChannel(channel), SendAs: &tg.InputPeerSelf{}}); err == nil {
		t.Fatal("personal identity accepted with profiles off")
	}
	toggle(true, false)
	signed := send(9102, nil)
	if signed.FromID != nil || signed.PostAuthor != "Owner" {
		t.Fatalf("text signature = %#v", signed)
	}
	toggle(true, true)
	peers, err := r.onChannelsGetSendAs(ctx, &tg.ChannelsGetSendAsRequest{Peer: inputPeerChannel(channel)})
	if err != nil {
		t.Fatal(err)
	}
	if p, ok := findSendAsChannel(peers.Peers, foreign.ID); !ok || p.PremiumRequired {
		t.Fatal("broadcast profile identity incorrectly requires Premium")
	}
	if _, err := r.onMessagesSaveDefaultSendAs(ctx, &tg.MessagesSaveDefaultSendAsRequest{Peer: inputPeerChannel(channel), SendAs: &tg.InputPeerSelf{}}); err != nil {
		t.Fatal(err)
	}
	assertDefault(&tg.PeerUser{UserID: owner.ID})
	personal := send(9103, nil)
	if !sameSignaturePeer(personal.FromID, &tg.PeerUser{UserID: owner.ID}) || personal.PostAuthor != "" {
		t.Fatal("personal author missing")
	}
	asChannel := send(9104, inputPeerChannel(foreign))
	if !sameSignaturePeer(asChannel.FromID, &tg.PeerChannel{ChannelID: foreign.ID}) {
		t.Fatal("foreign author missing")
	}
	toggle(false, false)
	assertDefault(&tg.PeerChannel{ChannelID: channel.ID})
	if msg := send(9105, nil); msg.FromID != nil {
		t.Fatal("disabled profile leaked sender")
	}
	// History and exact retries retain the original author visibility after the setting changes.
	history, err := f.channels.GetChannelMessages(f.ctx, owner.ID, channel.ID, []int{old.ID, signed.ID, personal.ID, asChannel.ID})
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range history.Messages {
		got := tgChannelMessage(owner.ID, m).(*tg.Message)
		want := map[int]tg.PeerClass{old.ID: nil, signed.ID: nil, personal.ID: &tg.PeerUser{UserID: owner.ID}, asChannel.ID: &tg.PeerChannel{ChannelID: foreign.ID}}[m.ID]
		if !sameSignaturePeer(got.FromID, want) {
			t.Fatalf("history author changed for message %d", m.ID)
		}
	}
	if got := send(9103, nil); !sameSignaturePeer(got.FromID, personal.FromID) || got.ID != personal.ID {
		t.Fatal("retry changed profile snapshot")
	}
}

func sameSignaturePeer(a, b tg.PeerClass) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	switch x := a.(type) {
	case *tg.PeerUser:
		y, ok := b.(*tg.PeerUser)
		return ok && x.UserID == y.UserID
	case *tg.PeerChannel:
		y, ok := b.(*tg.PeerChannel)
		return ok && x.ChannelID == y.ChannelID
	}
	return false
}
