package rpc

import (
	"testing"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
)

// DrKLO's UserObject.isUserSelf only looks at the user.self flag; any server path
// that emits "the requester's own user" with self=false makes Android putUsers
// overwrite the account cache and degrade Saved Messages into an ordinary self-chat.
// This test pins several channel/global history conversion exits that once missed
// the self branch: when the viewer appears in the users set it must carry the self
// flag.

// selfFlag locates the viewer in users and returns its self flag (fails when not
// found).
func selfFlag(t *testing.T, users []tg.UserClass, viewerID int64) bool {
	t.Helper()
	for _, uc := range users {
		u, ok := uc.(*tg.User)
		if !ok || u.ID != viewerID {
			continue
		}
		return u.Self
	}
	t.Fatalf("viewer %d missing from users slice", viewerID)
	return false
}

func TestChannelHistoryMessagesMarksViewerSelf(t *testing.T) {
	const viewer = int64(1001)
	history := domain.ChannelHistory{
		Channel: domain.Channel{ID: 40, AccessHash: 7, Megagroup: true},
		Users: []domain.User{
			{ID: viewer, AccessHash: 5, FirstName: "Me"},
			{ID: 2002, AccessHash: 6, FirstName: "Other"},
		},
	}
	out, ok := tgChannelHistoryMessages(viewer, history).(*tg.MessagesChannelMessages)
	if !ok {
		t.Fatalf("expected MessagesChannelMessages, got %T", tgChannelHistoryMessages(viewer, history))
	}
	if !selfFlag(t, out.Users, viewer) {
		t.Fatal("viewer's own user must carry self flag in channel history (Saved Messages cache integrity)")
	}
	if selfFlag(t, out.Users, 2002) {
		t.Fatal("other user must not carry self flag")
	}
}

func TestChannelSearchPostsMessagesMarksViewerSelf(t *testing.T) {
	const viewer = int64(1001)
	history := domain.ChannelHistory{
		Channel:  domain.Channel{ID: 40, AccessHash: 7, Megagroup: true},
		Count:    5, // triggers the MessagesMessagesSlice branch
		Channels: []domain.Channel{{ID: 40, AccessHash: 7, Megagroup: true}},
		Users:    []domain.User{{ID: viewer, AccessHash: 5, FirstName: "Me"}},
	}
	slice, ok := tgChannelSearchPostsMessages(viewer, history).(*tg.MessagesMessagesSlice)
	if !ok {
		t.Fatalf("expected MessagesMessagesSlice, got %T", tgChannelSearchPostsMessages(viewer, history))
	}
	if !selfFlag(t, slice.Users, viewer) {
		t.Fatal("viewer self flag lost in channel post search")
	}
}

func TestGlobalSearchMessagesMarksViewerSelf(t *testing.T) {
	const viewer = int64(1001)
	private := domain.MessageList{Users: []domain.User{{ID: viewer, AccessHash: 5, FirstName: "Me"}}}
	channel := domain.ChannelHistory{Users: []domain.User{{ID: viewer, AccessHash: 5, FirstName: "Me"}}}
	switch out := tgGlobalSearchMessages(viewer, 50, private, channel).(type) {
	case *tg.MessagesMessages:
		if !selfFlag(t, out.Users, viewer) {
			t.Fatal("viewer self flag lost in global search (messages)")
		}
	case *tg.MessagesMessagesSlice:
		if !selfFlag(t, out.Users, viewer) {
			t.Fatal("viewer self flag lost in global search (slice)")
		}
	default:
		t.Fatalf("unexpected global search result type %T", out)
	}
}
