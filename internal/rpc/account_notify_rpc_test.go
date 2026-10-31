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

// TestNotifySettingsRoundTripAndDialogProjection regression: get/update/reset NotifySettings
// used to be an echo stub (not persisted), so mute was lost on restart and the dialog list did
// not reflect muting. This test verifies the per-peer persistence round-trip + mute projected
// into the dialog list + updateNotifySettings push + reset.
func TestNotifySettingsRoundTripAndDialogProjection(t *testing.T) {
	r, sessions := notifyRouter(t)
	const viewer = int64(1000000001)
	const peerID = int64(555)
	ctx := WithUserID(context.Background(), viewer)
	peerInput := &tg.InputNotifyPeer{Peer: &tg.InputPeerUser{UserID: peerID}}

	// Default: not configured -> mute_until=0 (not muted).
	def, err := r.onAccountGetNotifySettings(ctx, peerInput)
	if err != nil {
		t.Fatalf("get default: %v", err)
	}
	if mu, _ := def.GetMuteUntil(); mu != 0 {
		t.Fatalf("default mute_until = %d, want 0", mu)
	}

	// Mute this peer.
	in := tg.InputPeerNotifySettings{}
	in.SetMuteUntil(2000000000)
	in.SetSilent(true)
	if ok, err := r.onAccountUpdateNotifySettings(ctx, &tg.AccountUpdateNotifySettingsRequest{Peer: peerInput, Settings: in}); err != nil || !ok {
		t.Fatalf("update notify = ok %v err %v", ok, err)
	}

	// Push updateNotifySettings.
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

	// get reads the mute back.
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

	// The dialog list projects the mute (key to recovery across restarts).
	list := domain.DialogList{Dialogs: []domain.Dialog{
		{Peer: domain.Peer{Type: domain.PeerTypeUser, ID: peerID}, TopMessage: 1},
		{Peer: domain.Peer{Type: domain.PeerTypeUser, ID: 999}, TopMessage: 1}, // unmuted control
	}}
	out, ok := r.tgMessagesDialogs(ctx, viewer, list).(*tg.MessagesDialogs)
	if !ok {
		t.Fatalf("dialogs projection type = %T", r.tgMessagesDialogs(ctx, viewer, list))
	}
	muted := dialogByPeerUser(t, out.Dialogs, peerID)
	if mu, _ := muted.NotifySettings.GetMuteUntil(); mu != 2000000000 {
		t.Fatalf("dialog mute_until = %d, want 2000000000 (list did not reflect the mute)", mu)
	}
	unmuted := dialogByPeerUser(t, out.Dialogs, 999)
	if mu, _ := unmuted.NotifySettings.GetMuteUntil(); mu != 0 {
		t.Fatalf("unmuted dialog mute_until = %d, want 0", mu)
	}

	// reset -> back to default.
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

// TestNotifySettingsCategoryDefaultScope verifies that category default scopes such as inputNotifyUsers persist independently.
func TestNotifySettingsCategoryDefaultScope(t *testing.T) {
	r, _ := notifyRouter(t)
	ctx := WithUserID(context.Background(), 1000000001)

	in := tg.InputPeerNotifySettings{}
	in.SetMuteUntil(123456)
	if ok, err := r.onAccountUpdateNotifySettings(ctx, &tg.AccountUpdateNotifySettingsRequest{Peer: &tg.InputNotifyUsers{}, Settings: in}); err != nil || !ok {
		t.Fatalf("update users-default = ok %v err %v", ok, err)
	}
	// users default has a value, chats default is still the default (scope isolation).
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
		t.Fatalf("chats-default mute_until = %d, want 0 (scopes must be isolated)", mu)
	}
}

// TestGetNotifyExceptions verifies getNotifyExceptions lists per-peer non-default settings:
// muting several peers -> they appear in the exceptions list; unmute -> they leave;
// compare_stories filters story-only.
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

	// Mute one user + one channel.
	mute(&tg.InputPeerUser{UserID: 555}, func(in *tg.InputPeerNotifySettings) { in.SetMuteUntil(2000000000) })
	mute(&tg.InputPeerChannel{ChannelID: 777}, func(in *tg.InputPeerNotifySettings) { in.SetSilent(true) })

	ex := notifyExceptions(t, r, ctx, &tg.AccountGetNotifyExceptionsRequest{})
	if len(ex) != 2 {
		t.Fatalf("exceptions = %d, want 2", len(ex))
	}
	if !exceptionsHaveUser(ex, 555) || !exceptionsHaveChannel(ex, 777) {
		t.Fatalf("exceptions missing expected peers: %#v", ex)
	}

	// Unmute user 555 (send empty settings -> clear) -> leaves the exceptions list.
	mute(&tg.InputPeerUser{UserID: 555}, func(in *tg.InputPeerNotifySettings) {})
	ex = notifyExceptions(t, r, ctx, &tg.AccountGetNotifyExceptionsRequest{})
	if len(ex) != 1 || !exceptionsHaveChannel(ex, 777) {
		t.Fatalf("after unmute exceptions = %#v, want only channel 777", ex)
	}

	// story-only exception: excluded by default, included with compare_stories.
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

// countingNotifyService wraps *appaccount.Service and counts AllPeerNotifySettings calls,
// verifying the per-user notify cache short-circuits hot-path queries.
type countingNotifyService struct {
	*appaccount.Service
	allCalls int
}

func (s *countingNotifyService) AllPeerNotifySettings(ctx context.Context, userID int64) (map[domain.Peer]domain.PeerNotifySettings, error) {
	s.allCalls++
	return s.Service.AllPeerNotifySettings(ctx, userID)
}

// TestNotifySettingsDialogProjectionCached regression P2-1: dialog projection reads from the
// per-user notify cache -- repeated getDialogs loads it only once (a hit costs 0 PG), and it is
// reloaded after update/reset invalidation.
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
		t.Fatalf("AllPeerNotifySettings calls = %d, want 1 (the last two getDialogs should hit the cache)", svc.allCalls)
	}

	// update invalidates the cache -> the next getDialogs reloads.
	in2 := tg.InputPeerNotifySettings{}
	in2.SetMuteUntil(2100000000)
	if _, err := r.onAccountUpdateNotifySettings(ctx, &tg.AccountUpdateNotifySettingsRequest{Peer: &tg.InputNotifyPeer{Peer: &tg.InputPeerUser{UserID: 555}}, Settings: in2}); err != nil {
		t.Fatalf("re-update notify: %v", err)
	}
	r.tgMessagesDialogs(ctx, viewer, list)
	if svc.allCalls != 2 {
		t.Fatalf("after invalidation calls = %d, want 2 (a cache invalidation should reload)", svc.allCalls)
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
