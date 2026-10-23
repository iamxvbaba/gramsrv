package tlprofile

import (
	"strconv"
	"testing"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/tg"
)

func TestExactProfilesEncodeOnlyConstructorsAndFieldsFromTargetLayer(t *testing.T) {
	user := &tg.User{ID: 10}
	user.SetLinkedCommunityID(100)
	channel := &tg.Channel{
		ID:    20,
		Title: "child",
		Photo: &tg.ChatPhotoEmpty{},
		Date:  1,
	}
	channel.SetLinkedCommunityID(100)
	result := &tg.MessagesDialogs{
		Dialogs: []tg.DialogClass{
			&tg.Dialog{
				Peer:           &tg.PeerChannel{ChannelID: channel.ID},
				NotifySettings: tg.PeerNotifySettings{},
			},
			&tg.DialogCommunity{
				CommunityID:    100,
				NotifySettings: tg.PeerNotifySettings{},
			},
		},
		Messages: []tg.MessageClass{},
		Chats: []tg.ChatClass{
			channel,
			&tg.Community{
				ID:    100,
				Title: "community",
				Photo: &tg.ChatPhotoEmpty{},
				Date:  1,
			},
		},
		Users: []tg.UserClass{user},
	}

	for _, profile := range []Profile{Profile225, Profile226, Profile227, Profile228} {
		t.Run("layer"+strconv.Itoa(int(profile)), func(t *testing.T) {
			request := &tg.MessagesGetDialogsRequest{
				OffsetPeer: &tg.InputPeerEmpty{},
				Limit:      100,
			}
			var canonical bin.Buffer
			if err := request.Encode(&canonical); err != nil {
				t.Fatal(err)
			}
			outbound, err := prepareSparseOutbound(profile, &canonical, Limits{})
			if err != nil {
				t.Fatal(err)
			}
			var requestWire bin.Buffer
			if err := outbound.Encode(&requestWire); err != nil {
				t.Fatal(err)
			}
			admission, err := NewDispatcher().Admit(profile, &requestWire, Limits{})
			if err != nil {
				t.Fatal(err)
			}
			var wire bin.Buffer
			if err := admission.Call().EncodeResult(result, &wire); err != nil {
				t.Fatal(err)
			}
			decoded, err := DecodeObject(profile, &bin.Buffer{Buf: wire.Copy()}, Limits{})
			if err != nil {
				t.Fatal(err)
			}
			dialogs, ok := decoded.(*tg.MessagesDialogs)
			if !ok {
				t.Fatalf("decoded result = %T", decoded)
			}

			wantDialogs, wantChats := 1, 1
			wantLinked := false
			if profile == Profile228 {
				wantDialogs, wantChats = 2, 2
				wantLinked = true
			}
			if len(dialogs.Dialogs) != wantDialogs {
				t.Fatalf("dialogs = %+v, want %d entries", dialogs.Dialogs, wantDialogs)
			}
			if len(dialogs.Chats) != wantChats {
				t.Fatalf("chats = %+v, want %d entries", dialogs.Chats, wantChats)
			}
			gotChannel, ok := dialogs.Chats[0].(*tg.Channel)
			if !ok {
				t.Fatalf("first chat = %T", dialogs.Chats[0])
			}
			_, channelLinked := gotChannel.GetLinkedCommunityID()
			if channelLinked != wantLinked {
				t.Fatalf("channel linked_community_id presence = %v, want %v", channelLinked, wantLinked)
			}
			gotUser, ok := dialogs.Users[0].(*tg.User)
			if !ok {
				t.Fatalf("first user = %T", dialogs.Users[0])
			}
			_, userLinked := gotUser.GetLinkedCommunityID()
			if userLinked != wantLinked {
				t.Fatalf("user linked_community_id presence = %v, want %v", userLinked, wantLinked)
			}
		})
	}
}
