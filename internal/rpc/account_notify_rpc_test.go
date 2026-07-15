package rpc

import (
	"context"
	"testing"

	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap/zaptest"

	appaccount "telesrv/internal/app/account"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

func notifyRouter(t *testing.T) (*Router, *captureSessions) {
	t.Helper()
	passwordStore := memory.NewPasswordStore()
	sessions := &captureSessions{}
	r := New(Config{}, Deps{
		Account:  appaccount.NewService(passwordStore, appaccount.WithNotifySettings(passwordStore)),
		Sessions: sessions,
	}, zaptest.NewLogger(t), clock.System)
	return r, sessions
}

// TestNotifySettingsRoundTripAndDialogProjection 回归：get/update/reset NotifySettings
// 此前是回显 stub（不持久化），mute 重启即丢、dialog 列表不反映静音。本测试验证
// per-peer 持久化往返 + dialog 列表投影出 mute + updateNotifySettings 推送 + reset。
func TestNotifySettingsRoundTripAndDialogProjection(t *testing.T) {
	r, sessions := notifyRouter(t)
	const viewer = int64(1000000001)
	const peerID = int64(555)
	ctx := WithUserID(context.Background(), viewer)
	peerInput := &tg.InputNotifyPeer{Peer: &tg.InputPeerUser{UserID: peerID}}

	// 默认：未配置 → mute_until=0（不静音）。
	def, err := r.onAccountGetNotifySettings(ctx, peerInput)
	if err != nil {
		t.Fatalf("get default: %v", err)
	}
	if mu, _ := def.GetMuteUntil(); mu != 0 {
		t.Fatalf("default mute_until = %d, want 0", mu)
	}

	// mute 该 peer。
	in := tg.InputPeerNotifySettings{}
	in.SetMuteUntil(2000000000)
	in.SetSilent(true)
	if ok, err := r.onAccountUpdateNotifySettings(ctx, &tg.AccountUpdateNotifySettingsRequest{Peer: peerInput, Settings: in}); err != nil || !ok {
		t.Fatalf("update notify = ok %v err %v", ok, err)
	}

	// 推送 updateNotifySettings。
	snap := sessions.snapshot()
	updates, ok := snap.message.(*tg.Updates)
	if !ok || len(updates.Updates) == 0 {
		t.Fatalf("pushed message = %#v, want *tg.Updates with updates", snap.message)
	}
	upd, ok := updates.Updates[0].(*tg.UpdateNotifySettings)
	if !ok {
		t.Fatalf("pushed update = %T, want *tg.UpdateNotifySettings", updates.Updates[0])
	}
	if np, ok := upd.Peer.(*tg.NotifyPeer); !ok {
		t.Fatalf("pushed notify peer = %T, want *tg.NotifyPeer", upd.Peer)
	} else if pu, ok := np.Peer.(*tg.PeerUser); !ok || pu.UserID != peerID {
		t.Fatalf("pushed notify peer = %#v, want user %d", np.Peer, peerID)
	}

	// get 读回 mute。
	got, err := r.onAccountGetNotifySettings(ctx, peerInput)
	if err != nil {
		t.Fatalf("get after mute: %v", err)
	}
	if mu, _ := got.GetMuteUntil(); mu != 2000000000 {
		t.Fatalf("mute_until after mute = %d, want 2000000000", mu)
	}
	if silent, ok := got.GetSilent(); !ok || !silent {
		t.Fatalf("silent after mute = %v ok %v, want true", silent, ok)
	}

	// dialog 列表投影出 mute（跨重启恢复的关键）。
	list := domain.DialogList{Dialogs: []domain.Dialog{
		{Peer: domain.Peer{Type: domain.PeerTypeUser, ID: peerID}, TopMessage: 1},
		{Peer: domain.Peer{Type: domain.PeerTypeUser, ID: 999}, TopMessage: 1}, // 未静音对照
	}}
	out, ok := r.tgMessagesDialogs(ctx, viewer, list).(*tg.MessagesDialogs)
	if !ok {
		t.Fatalf("dialogs projection type = %T", r.tgMessagesDialogs(ctx, viewer, list))
	}
	muted := dialogByPeerUser(t, out.Dialogs, peerID)
	if mu, _ := muted.NotifySettings.GetMuteUntil(); mu != 2000000000 {
		t.Fatalf("dialog mute_until = %d, want 2000000000（列表未反映静音）", mu)
	}
	unmuted := dialogByPeerUser(t, out.Dialogs, 999)
	if mu, _ := unmuted.NotifySettings.GetMuteUntil(); mu != 0 {
		t.Fatalf("unmuted dialog mute_until = %d, want 0", mu)
	}

	// reset → 回默认。
	if ok, err := r.onAccountResetNotifySettings(ctx); err != nil || !ok {
		t.Fatalf("reset = ok %v err %v", ok, err)
	}
	after, err := r.onAccountGetNotifySettings(ctx, peerInput)
	if err != nil {
		t.Fatalf("get after reset: %v", err)
	}
	if mu, _ := after.GetMuteUntil(); mu != 0 {
		t.Fatalf("mute_until after reset = %d, want 0", mu)
	}
}

// TestNotifySettingsCategoryDefaultScope 验证 inputNotifyUsers 等类别默认作用域独立持久化。
func TestNotifySettingsCategoryDefaultScope(t *testing.T) {
	r, _ := notifyRouter(t)
	ctx := WithUserID(context.Background(), 1000000001)

	in := tg.InputPeerNotifySettings{}
	in.SetMuteUntil(123456)
	if ok, err := r.onAccountUpdateNotifySettings(ctx, &tg.AccountUpdateNotifySettingsRequest{Peer: &tg.InputNotifyUsers{}, Settings: in}); err != nil || !ok {
		t.Fatalf("update users-default = ok %v err %v", ok, err)
	}
	// users 默认有值，chats 默认仍为默认（作用域隔离）。
	usersGot, err := r.onAccountGetNotifySettings(ctx, &tg.InputNotifyUsers{})
	if err != nil {
		t.Fatalf("get users-default: %v", err)
	}
	if mu, _ := usersGot.GetMuteUntil(); mu != 123456 {
		t.Fatalf("users-default mute_until = %d, want 123456", mu)
	}
	chatsGot, err := r.onAccountGetNotifySettings(ctx, &tg.InputNotifyChats{})
	if err != nil {
		t.Fatalf("get chats-default: %v", err)
	}
	if mu, _ := chatsGot.GetMuteUntil(); mu != 0 {
		t.Fatalf("chats-default mute_until = %d, want 0（作用域应隔离）", mu)
	}
}

// TestGetNotifyExceptions 验证 getNotifyExceptions 列出 per-peer 非默认设置：
// mute 多个 peer → 出现在异常列表；unmute → 退出；compare_stories 过滤 story-only。
func TestGetNotifyExceptions(t *testing.T) {
	r, _ := notifyRouter(t)
	ctx := WithUserID(context.Background(), 1000000001)
	mute := func(peer tg.InputPeerClass, set func(*tg.InputPeerNotifySettings)) {
		in := tg.InputPeerNotifySettings{}
		set(&in)
		if ok, err := r.onAccountUpdateNotifySettings(ctx, &tg.AccountUpdateNotifySettingsRequest{Peer: &tg.InputNotifyPeer{Peer: peer}, Settings: in}); err != nil || !ok {
			t.Fatalf("update notify = ok %v err %v", ok, err)
		}
	}

	// mute 一个 user + 一个 channel。
	mute(&tg.InputPeerUser{UserID: 555}, func(in *tg.InputPeerNotifySettings) { in.SetMuteUntil(2000000000) })
	mute(&tg.InputPeerChannel{ChannelID: 777}, func(in *tg.InputPeerNotifySettings) { in.SetSilent(true) })

	ex := notifyExceptions(t, r, ctx, &tg.AccountGetNotifyExceptionsRequest{})
	if len(ex) != 2 {
		t.Fatalf("exceptions = %d, want 2", len(ex))
	}
	if !exceptionsHaveUser(ex, 555) || !exceptionsHaveChannel(ex, 777) {
		t.Fatalf("exceptions missing expected peers: %#v", ex)
	}

	// unmute user 555（发空设置→清空）→ 退出异常列表。
	mute(&tg.InputPeerUser{UserID: 555}, func(in *tg.InputPeerNotifySettings) {})
	ex = notifyExceptions(t, r, ctx, &tg.AccountGetNotifyExceptionsRequest{})
	if len(ex) != 1 || !exceptionsHaveChannel(ex, 777) {
		t.Fatalf("after unmute exceptions = %#v, want only channel 777", ex)
	}

	// story-only 异常：默认不计入，compare_stories 计入。
	mute(&tg.InputPeerUser{UserID: 888}, func(in *tg.InputPeerNotifySettings) { in.SetStoriesMuted(true) })
	if got := notifyExceptions(t, r, ctx, &tg.AccountGetNotifyExceptionsRequest{}); len(got) != 1 {
		t.Fatalf("default exceptions = %d, want 1 (story-only excluded)", len(got))
	}
	withStories := &tg.AccountGetNotifyExceptionsRequest{}
	withStories.CompareStories = true
	if got := notifyExceptions(t, r, ctx, withStories); len(got) != 2 || !exceptionsHaveUser(got, 888) {
		t.Fatalf("compare_stories exceptions = %#v, want story-only 888 included", got)
	}
}

func notifyExceptions(t *testing.T, r *Router, ctx context.Context, req *tg.AccountGetNotifyExceptionsRequest) []*tg.UpdateNotifySettings {
	t.Helper()
	out, err := r.onAccountGetNotifyExceptions(ctx, req)
	if err != nil {
		t.Fatalf("getNotifyExceptions: %v", err)
	}
	upd, ok := out.(*tg.Updates)
	if !ok {
		t.Fatalf("response = %T, want *tg.Updates", out)
	}
	res := make([]*tg.UpdateNotifySettings, 0, len(upd.Updates))
	for _, u := range upd.Updates {
		if uns, ok := u.(*tg.UpdateNotifySettings); ok {
			res = append(res, uns)
		}
	}
	return res
}

func exceptionsHaveUser(ex []*tg.UpdateNotifySettings, userID int64) bool {
	for _, e := range ex {
		if np, ok := e.Peer.(*tg.NotifyPeer); ok {
			if pu, ok := np.Peer.(*tg.PeerUser); ok && pu.UserID == userID {
				return true
			}
		}
	}
	return false
}

func exceptionsHaveChannel(ex []*tg.UpdateNotifySettings, channelID int64) bool {
	for _, e := range ex {
		if np, ok := e.Peer.(*tg.NotifyPeer); ok {
			if pc, ok := np.Peer.(*tg.PeerChannel); ok && pc.ChannelID == channelID {
				return true
			}
		}
	}
	return false
}

// countingNotifyService 包 *appaccount.Service 计数 AllPeerNotifySettings 调用，
// 验证 per-user notify 缓存短路了热路径查询。
type countingNotifyService struct {
	*appaccount.Service
	allCalls int
}

func (s *countingNotifyService) AllPeerNotifySettings(ctx context.Context, userID int64) (map[domain.Peer]domain.PeerNotifySettings, error) {
	s.allCalls++
	return s.Service.AllPeerNotifySettings(ctx, userID)
}

// TestNotifySettingsDialogProjectionCached 回归 P2-1：dialog 投影从 per-user notify
// 缓存读取——重复 getDialogs 只加载一次（命中 0 PG），update/reset 失效后重载。
func TestNotifySettingsDialogProjectionCached(t *testing.T) {
	passwordStore := memory.NewPasswordStore()
	svc := &countingNotifyService{Service: appaccount.NewService(passwordStore, appaccount.WithNotifySettings(passwordStore))}
	r := New(Config{}, Deps{Account: svc, Sessions: &captureSessions{}}, zaptest.NewLogger(t), clock.System)
	const viewer = int64(1000000001)
	ctx := WithUserID(context.Background(), viewer)

	in := tg.InputPeerNotifySettings{}
	in.SetMuteUntil(2000000000)
	if ok, err := r.onAccountUpdateNotifySettings(ctx, &tg.AccountUpdateNotifySettingsRequest{Peer: &tg.InputNotifyPeer{Peer: &tg.InputPeerUser{UserID: 555}}, Settings: in}); err != nil || !ok {
		t.Fatalf("update notify: ok %v err %v", ok, err)
	}

	list := domain.DialogList{Dialogs: []domain.Dialog{{Peer: domain.Peer{Type: domain.PeerTypeUser, ID: 555}, TopMessage: 1}}}
	for i := 0; i < 3; i++ {
		out := r.tgMessagesDialogs(ctx, viewer, list).(*tg.MessagesDialogs)
		dlg := dialogByPeerUser(t, out.Dialogs, 555)
		if mu, _ := dlg.NotifySettings.GetMuteUntil(); mu != 2000000000 {
			t.Fatalf("iter %d dialog mute = %d, want 2000000000", i, mu)
		}
	}
	if svc.allCalls != 1 {
		t.Fatalf("AllPeerNotifySettings calls = %d, want 1 (后两次 getDialogs 应命中缓存)", svc.allCalls)
	}

	// update 失效缓存 → 下次 getDialogs 重载。
	in2 := tg.InputPeerNotifySettings{}
	in2.SetMuteUntil(2100000000)
	if _, err := r.onAccountUpdateNotifySettings(ctx, &tg.AccountUpdateNotifySettingsRequest{Peer: &tg.InputNotifyPeer{Peer: &tg.InputPeerUser{UserID: 555}}, Settings: in2}); err != nil {
		t.Fatalf("re-update notify: %v", err)
	}
	r.tgMessagesDialogs(ctx, viewer, list)
	if svc.allCalls != 2 {
		t.Fatalf("after invalidation calls = %d, want 2 (缓存失效应重载)", svc.allCalls)
	}
}

func dialogByPeerUser(t *testing.T, dialogs []tg.DialogClass, userID int64) *tg.Dialog {
	t.Helper()
	for _, d := range dialogs {
		dlg, ok := d.(*tg.Dialog)
		if !ok {
			continue
		}
		if pu, ok := dlg.Peer.(*tg.PeerUser); ok && pu.UserID == userID {
			return dlg
		}
	}
	t.Fatalf("dialog for user %d not found", userID)
	return nil
}
