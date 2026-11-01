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

// accountSettingsRouter builds a Router wired to in-memory account-settings persistence.
func accountSettingsRouter(t *testing.T) *Router {
	t.Helper()
	passwordStore := memory.NewPasswordStore()
	return New(Config{}, Deps{
		Account: appaccount.NewService(passwordStore, appaccount.WithAccountSettings(passwordStore)),
	}, zaptest.NewLogger(t), clock.System)
}

// TestAccountSettingsRoundTrip regression: globalPrivacy/accountTTL/contentSettings/
// contactSignUpNotification used to be hard-coded echo stubs (set did not persist). This test
// verifies a true set -> get round-trip: what is read back matches what was written.
func TestAccountSettingsRoundTrip(t *testing.T) {
	r := accountSettingsRouter(t)
	ctx := WithUserID(context.Background(), 1000000001)

	// Default (not persisted): globalPrivacy all off, TTL 365, sensitive off but changeable, signup notification not muted.
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

	// Write globalPrivacy (including the optional paid stars field) -> read back identical.
	in := tg.GlobalPrivacySettings{
		ArchiveAndMuteNewNoncontactPeers: true,
		HideReadMarks:                    true,
		NewNoncontactPeersRequirePremium: true,
	}
	in.SetNoncontactPeersPaidStars(50)
	in.SetDisallowedGifts(tg.DisallowedGiftsSettings{
		DisallowLimitedStargifts: true,
		DisallowPremiumGifts:     true,
	})
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

	// Write TTL -> read back.
	if _, err := r.onAccountSetAccountTTL(ctx, tg.AccountDaysTTL{Days: 30}); err != nil {
		t.Fatalf("set ttl: %v", err)
	}
	if ttl, err := r.onAccountGetAccountTTL(ctx); err != nil || ttl.Days != 30 {
		t.Fatalf("ttl after set = %v err %v, want 30", ttl, err)
	}
	// TTL=0 is invalid.
	if ok, err := r.onAccountSetAccountTTL(ctx, tg.AccountDaysTTL{Days: 0}); ok || !tgerr.Is(err, "TTL_DAYS_INVALID") {
		t.Fatalf("ttl=0 = ok %v err %v, want TTL_DAYS_INVALID", ok, err)
	}

	// Write sensitive content -> read back.
	if ok, err := r.onAccountSetContentSettings(ctx, &tg.AccountSetContentSettingsRequest{SensitiveEnabled: true}); err != nil || !ok {
		t.Fatalf("set content settings: ok %v err %v", ok, err)
	}
	if cs, err := r.onAccountGetContentSettings(ctx); err != nil || !cs.SensitiveEnabled || !cs.SensitiveCanChange {
		t.Fatalf("content settings after set = %+v err %v, want sensitive on + can_change on", cs, err)
	}

	// Write contact signup silent -> read back.
	if ok, err := r.onAccountSetContactSignUpNotification(ctx, true); err != nil || !ok {
		t.Fatalf("set contact signup silent: ok %v err %v", ok, err)
	}
	if silent, err := r.onAccountGetContactSignUpNotification(ctx); err != nil || !silent {
		t.Fatalf("contact signup silent after set = %v err %v, want true", silent, err)
	}

	// No cross-talk: after writing contactSignUp, globalPrivacy still holds the previously written value.
	if got, err := r.onAccountGetGlobalPrivacySettings(ctx); err != nil || !got.ArchiveAndMuteNewNoncontactPeers {
		t.Fatalf("global privacy must survive other settings writes, got %+v err %v", got, err)
	}
}

// TestAccountSettingsNotWiredFallsBack verifies that with no persistence service wired, each handler falls back to defaults without erroring.
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
	wantGifts, wantGiftsOK := want.GetDisallowedGifts()
	gotGifts, gotGiftsOK := got.GetDisallowedGifts()
	if gotGiftsOK != wantGiftsOK || gotGifts != wantGifts {
		t.Fatalf("disallowed gifts = %+v ok=%v, want %+v ok=%v",
			gotGifts, gotGiftsOK, wantGifts, wantGiftsOK)
	}
}
