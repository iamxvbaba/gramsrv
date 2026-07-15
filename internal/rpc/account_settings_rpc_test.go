package rpc

import (
	"context"
	"testing"

	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"go.uber.org/zap/zaptest"

	appaccount "telesrv/internal/app/account"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

// accountSettingsRouter 装配一个接通账号设置持久化（内存）的 Router。
func accountSettingsRouter(t *testing.T) *Router {
	t.Helper()
	passwordStore := memory.NewPasswordStore()
	return New(Config{}, Deps{
		Account: appaccount.NewService(passwordStore, appaccount.WithAccountSettings(passwordStore)),
	}, zaptest.NewLogger(t), clock.System)
}

// TestAccountSettingsRoundTrip 回归：globalPrivacy/accountTTL/contentSettings/
// contactSignUpNotification 此前是硬编码回显 stub（set 不持久化）。本测试验证
// set→get 真往返：写入后读回与写入一致。
func TestAccountSettingsRoundTrip(t *testing.T) {
	r := accountSettingsRouter(t)
	ctx := WithUserID(context.Background(), 1000000001)

	// 默认（未持久化）：globalPrivacy 全关、TTL 365、sensitive 关但可切换、注册通知不静音。
	gp, err := r.onAccountGetGlobalPrivacySettings(ctx)
	if err != nil {
		t.Fatalf("get global privacy: %v", err)
	}
	if gp.ArchiveAndMuteNewNoncontactPeers || gp.HideReadMarks {
		t.Fatalf("default global privacy must be all-off, got %+v", gp)
	}
	if ttl, err := r.onAccountGetAccountTTL(ctx); err != nil || ttl.Days != domain.DefaultAccountTTLDays {
		t.Fatalf("default ttl = %v err %v, want %d", ttl, err, domain.DefaultAccountTTLDays)
	}
	cs, err := r.onAccountGetContentSettings(ctx)
	if err != nil || cs.SensitiveEnabled || !cs.SensitiveCanChange {
		t.Fatalf("default content settings = %+v err %v, want sensitive off + can_change on", cs, err)
	}
	if silent, err := r.onAccountGetContactSignUpNotification(ctx); err != nil || silent {
		t.Fatalf("default contact signup silent = %v err %v, want false", silent, err)
	}

	// 写 globalPrivacy（含 paid stars 可选字段）→ 读回一致。
	in := tg.GlobalPrivacySettings{
		ArchiveAndMuteNewNoncontactPeers: true,
		HideReadMarks:                    true,
		NewNoncontactPeersRequirePremium: true,
	}
	in.SetNoncontactPeersPaidStars(50)
	saved, err := r.onAccountSetGlobalPrivacySettings(ctx, in)
	if err != nil {
		t.Fatalf("set global privacy: %v", err)
	}
	assertGlobalPrivacy(t, saved, in)
	got, err := r.onAccountGetGlobalPrivacySettings(ctx)
	if err != nil {
		t.Fatalf("re-get global privacy: %v", err)
	}
	assertGlobalPrivacy(t, got, in)

	// 写 TTL → 读回。
	if _, err := r.onAccountSetAccountTTL(ctx, tg.AccountDaysTTL{Days: 30}); err != nil {
		t.Fatalf("set ttl: %v", err)
	}
	if ttl, err := r.onAccountGetAccountTTL(ctx); err != nil || ttl.Days != 30 {
		t.Fatalf("ttl after set = %v err %v, want 30", ttl, err)
	}
	// TTL=0 非法。
	if ok, err := r.onAccountSetAccountTTL(ctx, tg.AccountDaysTTL{Days: 0}); ok || !tgerr.Is(err, "TTL_DAYS_INVALID") {
		t.Fatalf("ttl=0 = ok %v err %v, want TTL_DAYS_INVALID", ok, err)
	}

	// 写 sensitive content → 读回。
	if ok, err := r.onAccountSetContentSettings(ctx, &tg.AccountSetContentSettingsRequest{SensitiveEnabled: true}); err != nil || !ok {
		t.Fatalf("set content settings: ok %v err %v", ok, err)
	}
	if cs, err := r.onAccountGetContentSettings(ctx); err != nil || !cs.SensitiveEnabled || !cs.SensitiveCanChange {
		t.Fatalf("content settings after set = %+v err %v, want sensitive on + can_change on", cs, err)
	}

	// 写 contact signup silent → 读回。
	if ok, err := r.onAccountSetContactSignUpNotification(ctx, true); err != nil || !ok {
		t.Fatalf("set contact signup silent: ok %v err %v", ok, err)
	}
	if silent, err := r.onAccountGetContactSignUpNotification(ctx); err != nil || !silent {
		t.Fatalf("contact signup silent after set = %v err %v, want true", silent, err)
	}

	// 互不干扰：写完 contactSignUp 后 globalPrivacy 仍是之前写入的值。
	if got, err := r.onAccountGetGlobalPrivacySettings(ctx); err != nil || !got.ArchiveAndMuteNewNoncontactPeers {
		t.Fatalf("global privacy must survive other settings writes, got %+v err %v", got, err)
	}
}

// TestAccountSettingsNotWiredFallsBack 验证未接通持久化服务时各 handler 回落默认、不报错。
func TestAccountSettingsNotWiredFallsBack(t *testing.T) {
	r := New(Config{}, Deps{}, zaptest.NewLogger(t), clock.System)
	ctx := WithUserID(context.Background(), 1000000001)

	if _, err := r.onAccountGetGlobalPrivacySettings(ctx); err != nil {
		t.Fatalf("get global privacy (unwired): %v", err)
	}
	if ttl, err := r.onAccountGetAccountTTL(ctx); err != nil || ttl.Days != domain.DefaultAccountTTLDays {
		t.Fatalf("get ttl (unwired) = %v err %v", ttl, err)
	}
	if ok, err := r.onAccountSetGlobalPrivacySettings(ctx, tg.GlobalPrivacySettings{HideReadMarks: true}); err != nil || ok == nil {
		t.Fatalf("set global privacy (unwired) must echo, ok %v err %v", ok, err)
	}
	if ok, err := r.onAccountSetContentSettings(ctx, &tg.AccountSetContentSettingsRequest{SensitiveEnabled: true}); err != nil || !ok {
		t.Fatalf("set content settings (unwired): ok %v err %v", ok, err)
	}
}

func assertGlobalPrivacy(t *testing.T, got *tg.GlobalPrivacySettings, want tg.GlobalPrivacySettings) {
	t.Helper()
	if got.ArchiveAndMuteNewNoncontactPeers != want.ArchiveAndMuteNewNoncontactPeers ||
		got.KeepArchivedUnmuted != want.KeepArchivedUnmuted ||
		got.KeepArchivedFolders != want.KeepArchivedFolders ||
		got.HideReadMarks != want.HideReadMarks ||
		got.NewNoncontactPeersRequirePremium != want.NewNoncontactPeersRequirePremium ||
		got.DisplayGiftsButton != want.DisplayGiftsButton {
		t.Fatalf("global privacy bools = %+v, want %+v", got, want)
	}
	wantStars, _ := want.GetNoncontactPeersPaidStars()
	gotStars, _ := got.GetNoncontactPeersPaidStars()
	if gotStars != wantStars {
		t.Fatalf("noncontact paid stars = %d, want %d", gotStars, wantStars)
	}
}
