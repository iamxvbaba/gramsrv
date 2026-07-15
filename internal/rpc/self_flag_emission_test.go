package rpc

import (
	"testing"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
)

// DrKLO 的 UserObject.isUserSelf 只看 user.self flag；服务端任何把"请求者自己的 user"
// 用 self=false 形态下发的路径，都会让 Android putUsers 覆盖账号缓存，Saved Messages
// 退化为普通自聊。本测试锁定几个曾漏 self 分支的 channel/global 历史转换出口：viewer
// 自己出现在 users 集合时必须带 self 标志。

// selfFlag 在 users 里定位 viewer，返回其 self 标志（找不到时 fail）。
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
		Count:    5, // 触发 MessagesMessagesSlice 分支
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
