package rpc

import (
	"context"
	"strings"
	"testing"

	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap/zaptest"

	appusers "telesrv/internal/app/users"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

// getFullUser 里 SCAM/FAKE 账号的描述首行必须是服务端警告，其后才是 owner
// 自己的文本；未标记的账号则原样返回（domain 层已有细粒度用例，这里守的是
// 投影接线本身——提醒真的落进了 UserFull.About 这个字段）。
func TestBuildUserFullProjectionPrependsModerationWarning(t *testing.T) {
	visibleAbout := func() *userFullBatchPrivacy {
		return &userFullBatchPrivacy{visible: map[domain.PrivacyKey]bool{domain.PrivacyKeyAbout: true}}
	}
	for _, test := range []struct {
		name string
		user domain.User
		want string
	}{
		{
			name: "scam over owner text",
			user: domain.User{ID: 20, Scam: true, About: "я андрей вот мое описание"},
			want: domain.ModerationProfileWarning(true, false, "я андрей вот мое описание"),
		},
		{
			name: "fake with empty owner text",
			user: domain.User{ID: 20, Fake: true},
			want: domain.ModerationProfileWarning(false, true, ""),
		},
		{
			name: "unflagged stays verbatim",
			user: domain.User{ID: 20, About: "обычное описание"},
			want: "обычное описание",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := New(Config{}, Deps{Privacy: visibleAbout()}, zaptest.NewLogger(t), clock.System)
			full, err := r.buildUserFullProjection(context.Background(), 10, test.user)
			if err != nil {
				t.Fatal(err)
			}
			if full.About != test.want {
				t.Fatalf("about = %q, want %q", full.About, test.want)
			}
			if test.user.Scam && !strings.HasPrefix(full.About, "⚠️ Warning: ") {
				t.Fatalf("about = %q, want the warning as the first line", full.About)
			}
		})
	}
}

// Клиенты заполняют редактор bio тем же About, который пришёл из profile, поэтому
// владельцу предупреждение отдавать нельзя — иначе серверная строка окажется в
// поле, которое он сейчас правит, и уедет обратно через updateProfile.
func TestBuildUserFullProjectionSkipsModerationWarningForOwner(t *testing.T) {
	r := New(Config{}, Deps{}, zaptest.NewLogger(t), clock.System)
	self := domain.User{
		ID: 20, Scam: true, About: "я андрей вот мое описание",
	}
	full, err := r.buildUserFullProjection(context.Background(), self.ID, self)
	if err != nil {
		t.Fatal(err)
	}
	if full.About != "я андрей вот мое описание" {
		t.Fatalf("own profile about = %q, want the raw stored description", full.About)
	}
	if strings.Contains(full.About, "⚠️") {
		t.Fatalf("own profile about = %q, want no injected warning in the editor source", full.About)
	}
}
func TestBuildUserFullProjectionModerationWarningFollowsAboutPrivacy(t *testing.T) {
	privacy := &userFullBatchPrivacy{visible: map[domain.PrivacyKey]bool{domain.PrivacyKeyAbout: false}}
	r := New(Config{}, Deps{Privacy: privacy}, zaptest.NewLogger(t), clock.System)
	full, err := r.buildUserFullProjection(context.Background(), 10, domain.User{
		ID: 20, Scam: true, About: "я андрей вот мое описание",
	})
	if err != nil {
		t.Fatal(err)
	}
	if full.About != "" {
		t.Fatalf("about = %q, want About privacy to hide the entire projection", full.About)
	}
	if strings.Contains(full.About, "мое описание") {
		t.Fatalf("about = %q, must not leak the hidden owner text", full.About)
	}
}

func TestProfileWarningDoesNotRewriteAuthorTextOnSave(t *testing.T) {
	domain.SetModerationProfileWarning("NOTICE", "IMPERSONATION")
	t.Cleanup(func() { domain.SetModerationProfileWarning("", "") })
	for _, flagged := range []bool{false, true} {
		ctx := context.Background()
		store := memory.NewUserStore()
		owner, err := store.Create(ctx, domain.User{AccessHash: 1, Phone: "15550001260", FirstName: "Owner", Scam: flagged})
		if err != nil {
			t.Fatal(err)
		}
		// v2: profile edits commit a durable delivery, so the router needs the outbox.
		delivery := attachDeliveryOutbox(store)
		r := New(Config{}, Deps{Users: appusers.NewService(store), DeliveryOutbox: delivery}, zaptest.NewLogger(t), clock.System)
		ctx = WithUserID(ctx, owner.ID)
		about := "NOTICE\nmy own quotation"
		for range 2 {
			req := &tg.AccountUpdateProfileRequest{}
			req.SetAbout(about)
			if _, err := r.onAccountUpdateProfile(ctx, req); err != nil {
				t.Fatal(err)
			}
			full, err := r.onUsersGetFullUser(ctx, &tg.InputUserSelf{})
			if err != nil {
				t.Fatal(err)
			}
			if full.FullUser.About != about {
				t.Fatalf("scam=%v saved about=%q, want %q", flagged, full.FullUser.About, about)
			}
		}
	}
}

func TestChannelProfileWarningKeepsEditorSourceAndViewerWarning(t *testing.T) {
	about := strings.Repeat("x", maxChannelAboutLength)
	channel := domain.Channel{ID: 1, Scam: true, About: about}
	for _, test := range []struct {
		name   string
		self   domain.ChannelMember
		editor bool
	}{
		{"creator", domain.ChannelMember{Role: domain.ChannelRoleCreator}, true},
		{"metadata admin", domain.ChannelMember{Role: domain.ChannelRoleAdmin, AdminRights: domain.ChannelAdminRights{ChangeInfo: true}}, true},
		{"other admin", domain.ChannelMember{Role: domain.ChannelRoleAdmin}, false},
		{"reader", domain.ChannelMember{Role: domain.ChannelRoleMember}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			full := tgChannelFull(domain.ChannelView{Channel: channel, Self: test.self})
			want := domain.ModerationProfileWarning(true, false, about)
			if test.editor {
				want = about
			}
			if full.About != want {
				t.Fatalf("about=%q, want %q", full.About, want)
			}
			if channel.About != about {
				t.Fatal("projection changed stored description")
			}
		})
	}
}
