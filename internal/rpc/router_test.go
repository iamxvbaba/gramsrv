package rpc

import (
	"context"
	"encoding/binary"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap/zaptest"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/clock"
	"github.com/gotd/td/proto"
	"github.com/gotd/td/tg"

	appauth "telesrv/internal/app/auth"
	appchannels "telesrv/internal/app/channels"
	appcontacts "telesrv/internal/app/contacts"
	appdialogs "telesrv/internal/app/dialogs"
	appmessages "telesrv/internal/app/messages"
	appupdates "telesrv/internal/app/updates"
	appusers "telesrv/internal/app/users"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

// TestDispatchUnwrapsWrappers 验证 Router 能剥离
// invokeWithLayer(initConnection(help.getConfig)) 并路由到 getConfig handler。
func TestDispatchUnwrapsWrappers(t *testing.T) {
	const (
		dc    = 2
		ip    = "127.0.0.1"
		port  = 2398
		layer = 225
	)
	r := New(Config{DC: dc, IP: ip, Port: port}, Deps{}, zaptest.NewLogger(t), clock.System)

	req := &tg.InvokeWithLayerRequest{
		Layer: layer,
		Query: &tg.InitConnectionRequest{
			APIID:          123,
			DeviceModel:    "TestDevice",
			SystemVersion:  "1.0",
			AppVersion:     "1.0",
			SystemLangCode: "en",
			LangPack:       "tdesktop",
			LangCode:       "en",
			Query:          &tg.HelpGetConfigRequest{},
		},
	}
	var b bin.Buffer
	if err := req.Encode(&b); err != nil {
		t.Fatalf("encode wrapped request: %v", err)
	}

	enc, err := r.Dispatch(context.Background(), [8]byte{}, 0, &b)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	cfg, ok := enc.(*tg.Config)
	if !ok {
		t.Fatalf("result type = %T, want *tg.Config", enc)
	}
	if cfg.ThisDC != dc {
		t.Fatalf("ThisDC = %d, want %d", cfg.ThisDC, dc)
	}
	if len(cfg.DCOptions) != 1 || cfg.DCOptions[0].Port != port {
		t.Fatalf("DCOptions = %+v", cfg.DCOptions)
	}
}

// TestDispatchUnknownReturnsError 验证未注册 RPC 经 fallback 返回 rpc_error。
func TestDispatchUnknownReturnsError(t *testing.T) {
	r := New(Config{DC: 2, IP: "127.0.0.1", Port: 2398}, Deps{}, zaptest.NewLogger(t), clock.System)

	// help.getCdnConfig 第一阶段未注册，应走 fallback。
	var b bin.Buffer
	if err := (&tg.HelpGetCDNConfigRequest{}).Encode(&b); err != nil {
		t.Fatalf("encode: %v", err)
	}

	_, err := r.Dispatch(context.Background(), [8]byte{}, 0, &b)
	if err == nil {
		t.Fatal("expected error for unregistered RPC")
	}
}

func TestDispatchResolvesBoundTempAuthKey(t *testing.T) {
	var tempAuthKeyID = [8]byte{0x55, 0x55, 0x55, 0x55, 0x55, 0x55, 0x55, 0x55}
	var permAuthKeyID = [8]byte{0x11, 0x11, 0x11, 0x11, 0x11, 0x11, 0x11, 0x11}
	tempBindings := memory.NewTempAuthKeyBindingStore()
	if err := tempBindings.Save(context.Background(), domain.TempAuthKeyBinding{
		TempAuthKeyID: tempAuthKeyID,
		PermAuthKeyID: int64(binary.LittleEndian.Uint64(permAuthKeyID[:])),
		ExpiresAt:     int(time.Now().Add(time.Hour).Unix()),
	}); err != nil {
		t.Fatalf("save temp binding: %v", err)
	}
	sessions := &captureSessions{}
	r := New(Config{}, Deps{
		Auth:     appauth.NewService(nil, nil, nil, nil, tempBindings, "12345"),
		Sessions: sessions,
	}, zaptest.NewLogger(t), clock.System)
	req := &tg.HelpGetConfigRequest{}
	var in bin.Buffer
	if err := req.Encode(&in); err != nil {
		t.Fatalf("encode request: %v", err)
	}

	if _, err := r.Dispatch(context.Background(), tempAuthKeyID, 123, &in); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	gotSession := sessions.snapshot()
	if gotSession.authKeyID != permAuthKeyID || !gotSession.authKeyResolved {
		t.Fatalf("session auth key = %x resolved %v, want resolved perm %x", gotSession.authKeyID, gotSession.authKeyResolved, permAuthKeyID)
	}
}

func TestBindTempAuthKeyClearsNegativeUserCache(t *testing.T) {
	var tempAuthKeyID = [8]byte{0x55, 0x55, 0x55, 0x55, 0x55, 0x55, 0x55, 0x55}
	var permAuthKeyID = [8]byte{0x11, 0x11, 0x11, 0x11, 0x11, 0x11, 0x11, 0x11}
	sessions := &captureSessions{}
	auth := &captureAuthService{}
	r := New(Config{}, Deps{
		Auth:     auth,
		Sessions: sessions,
	}, zaptest.NewLogger(t), clock.System)
	req := &tg.AuthBindTempAuthKeyRequest{
		PermAuthKeyID: int64(binary.LittleEndian.Uint64(permAuthKeyID[:])),
		Nonce:         2,
		ExpiresAt:     int(time.Now().Add(time.Hour).Unix()),
	}
	var in bin.Buffer
	if err := req.Encode(&in); err != nil {
		t.Fatalf("encode request: %v", err)
	}

	if _, err := r.Dispatch(context.Background(), tempAuthKeyID, 123, &in); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if auth.userIDCount != 1 {
		t.Fatalf("user lookup count = %d, want one negative lookup before temp binding", auth.userIDCount)
	}
	gotSession := sessions.snapshot()
	if gotSession.authKeyID != permAuthKeyID || !gotSession.authKeyResolved {
		t.Fatalf("session auth key = %x resolved %v, want perm %x", gotSession.authKeyID, gotSession.authKeyResolved, permAuthKeyID)
	}
	if gotSession.userResolved || gotSession.userID != 0 {
		t.Fatalf("negative user cache = user %d resolved %v, want cleared after auth key switch", gotSession.userID, gotSession.userResolved)
	}
}

func TestDispatchCachesUnauthenticatedIdentity(t *testing.T) {
	var rawAuthKeyID = [8]byte{0x77, 0x77, 0x77, 0x77, 0x77, 0x77, 0x77, 0x77}
	auth := &captureAuthService{}
	sessions := &captureSessions{}
	r := New(Config{}, Deps{
		Auth:     auth,
		Sessions: sessions,
	}, zaptest.NewLogger(t), clock.System)
	req := &tg.HelpGetConfigRequest{}

	for i := 0; i < 2; i++ {
		var in bin.Buffer
		if err := req.Encode(&in); err != nil {
			t.Fatalf("encode request: %v", err)
		}
		if _, err := r.Dispatch(context.Background(), rawAuthKeyID, 777, &in); err != nil {
			t.Fatalf("dispatch %d: %v", i, err)
		}
	}

	if auth.resolveCount != 1 || auth.userIDCount != 1 {
		t.Fatalf("identity lookups = resolve %d user %d, want one-time negative cache", auth.resolveCount, auth.userIDCount)
	}
	gotSession := sessions.snapshot()
	if !gotSession.userResolved || gotSession.userID != 0 {
		t.Fatalf("cached unauth identity = user %d resolved %v, want 0/true", gotSession.userID, gotSession.userResolved)
	}
}

func TestDispatchCachesConnectionIdentity(t *testing.T) {
	var rawAuthKeyID = [8]byte{0x55, 0x55, 0x55, 0x55, 0x55, 0x55, 0x55, 0x55}
	var permAuthKeyID = [8]byte{0x11, 0x11, 0x11, 0x11, 0x11, 0x11, 0x11, 0x11}
	auth := &captureAuthService{
		resolvedAuthKeyID: permAuthKeyID,
		hasResolved:       true,
		userID:            1000000001,
	}
	sessions := &captureSessions{}
	r := New(Config{}, Deps{
		Auth:     auth,
		Sessions: sessions,
		Users: staticUsersService{user: domain.User{
			ID:        1000000001,
			FirstName: "Test",
		}},
	}, zaptest.NewLogger(t), clock.System)
	req := &tg.UsersGetFullUserRequest{ID: &tg.InputUserSelf{}}

	for i := 0; i < 2; i++ {
		var in bin.Buffer
		if err := req.Encode(&in); err != nil {
			t.Fatalf("encode request: %v", err)
		}
		if _, err := r.Dispatch(context.Background(), rawAuthKeyID, 777, &in); err != nil {
			t.Fatalf("dispatch %d: %v", i, err)
		}
	}

	if auth.resolveCount != 1 || auth.userIDCount != 1 {
		t.Fatalf("identity lookups = resolve %d user %d, want one-time cache", auth.resolveCount, auth.userIDCount)
	}
	gotSession := sessions.snapshot()
	if gotSession.authKeyID != permAuthKeyID || gotSession.userID != 1000000001 {
		t.Fatalf("cached identity = auth %x user %d, want perm/user", gotSession.authKeyID, gotSession.userID)
	}
}

func TestDispatchSingleflightsAuthUserLookupAcrossStartupRPCs(t *testing.T) {
	var authKeyID = [8]byte{0x42, 0x42, 0x42, 0x42, 0x42, 0x42, 0x42, 0x42}
	auth := newBlockingUserAuthService(1000000001)
	r := New(Config{DC: 2, IP: "127.0.0.1", Port: 2398}, Deps{
		Auth: auth,
	}, zaptest.NewLogger(t), clock.System)

	const calls = 16
	errs := make(chan error, calls)
	var wg sync.WaitGroup
	for i := 0; i < calls; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			var in bin.Buffer
			if err := (&tg.HelpGetConfigRequest{}).Encode(&in); err != nil {
				errs <- err
				return
			}
			_, err := r.Dispatch(context.Background(), authKeyID, int64(100+i), &in)
			errs <- err
		}(i)
	}

	select {
	case <-auth.started:
	case <-time.After(time.Second):
		t.Fatal("auth lookup did not start")
	}
	close(auth.release)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("dispatch: %v", err)
		}
	}
	if got := auth.UserIDCount(); got != 1 {
		t.Fatalf("UserID lookups = %d, want singleflighted one lookup", got)
	}

	for i := 0; i < 3; i++ {
		var in bin.Buffer
		if err := (&tg.HelpGetConfigRequest{}).Encode(&in); err != nil {
			t.Fatalf("encode: %v", err)
		}
		if _, err := r.Dispatch(context.Background(), authKeyID, int64(200+i), &in); err != nil {
			t.Fatalf("cached dispatch %d: %v", i, err)
		}
	}
	if got := auth.UserIDCount(); got != 1 {
		t.Fatalf("UserID lookups after cache hits = %d, want still one lookup", got)
	}
}

func TestDispatchAnnouncesPresenceWhenSessionIdentityRestored(t *testing.T) {
	ctx := context.Background()
	alice := domain.User{ID: 1000000001, AccessHash: 11, FirstName: "Alice"}
	bob := domain.User{ID: 1000000002, AccessHash: 22, FirstName: "Bob"}
	dialogs := memory.NewDialogStore()
	if err := dialogs.SaveList(ctx, alice.ID, domain.DialogList{
		Dialogs: []domain.Dialog{{
			Peer:           domain.Peer{Type: domain.PeerTypeUser, ID: bob.ID},
			TopMessage:     10,
			TopMessageDate: 1700000000,
		}},
		Users: []domain.User{bob},
	}); err != nil {
		t.Fatalf("save dialogs: %v", err)
	}
	auth := &captureAuthService{userID: bob.ID}
	sessions := &captureSessions{onlineUserIDs: []int64{alice.ID}}
	r := New(Config{}, Deps{
		Auth:     auth,
		Dialogs:  appdialogs.NewService(dialogs),
		Sessions: sessions,
	}, zaptest.NewLogger(t), clock.System)
	req := &tg.HelpGetConfigRequest{}

	for i := 0; i < 2; i++ {
		var in bin.Buffer
		if err := req.Encode(&in); err != nil {
			t.Fatalf("encode request: %v", err)
		}
		if _, err := r.Dispatch(ctx, [8]byte{0x22}, 333, &in); err != nil {
			t.Fatalf("dispatch %d: %v", i, err)
		}
	}

	if auth.userIDCount != 1 {
		t.Fatalf("user lookup count = %d, want one restored identity lookup", auth.userIDCount)
	}
	gotPushes := sessions.pushedUserIDs()
	if !reflect.DeepEqual(gotPushes, []int64{bob.ID, alice.ID}) {
		t.Fatalf("pushed users = %+v, want self and online private dialog peer once", gotPushes)
	}
	update := pushedUserStatus(t, sessions.snapshot().message)
	if update.UserID != bob.ID {
		t.Fatalf("status user = %d, want bob", update.UserID)
	}
	if status, ok := update.Status.(*tg.UserStatusOnline); !ok || status.Expires <= int(time.Now().Unix()) {
		t.Fatalf("status = %#v, want online with future expires", update.Status)
	}
}

func TestDispatchPushesOnlinePeerStatusesToRestoredSession(t *testing.T) {
	ctx := context.Background()
	alice := domain.User{ID: 1000000001, AccessHash: 11, FirstName: "Alice"}
	bob := domain.User{ID: 1000000002, AccessHash: 22, FirstName: "Bob"}
	dialogs := memory.NewDialogStore()
	if err := dialogs.SaveList(ctx, alice.ID, domain.DialogList{
		Dialogs: []domain.Dialog{{
			Peer:           domain.Peer{Type: domain.PeerTypeUser, ID: bob.ID},
			TopMessage:     10,
			TopMessageDate: 1700000000,
		}},
		Users: []domain.User{bob},
	}); err != nil {
		t.Fatalf("save dialogs: %v", err)
	}
	auth := &captureAuthService{userID: alice.ID}
	sessions := &captureSessions{onlineUserIDs: []int64{bob.ID}}
	r := New(Config{}, Deps{
		Auth:     auth,
		Dialogs:  appdialogs.NewService(dialogs),
		Sessions: sessions,
	}, zaptest.NewLogger(t), clock.System)
	if ok, err := r.onAccountUpdateStatus(WithSessionID(WithUserID(ctx, bob.ID), 22), false); err != nil || !ok {
		t.Fatalf("bob account.updateStatus online = %v, %v", ok, err)
	}
	req := &tg.HelpGetConfigRequest{}

	for i := 0; i < 2; i++ {
		var in bin.Buffer
		if err := req.Encode(&in); err != nil {
			t.Fatalf("encode request: %v", err)
		}
		if _, err := r.Dispatch(ctx, [8]byte{0x33}, 444, &in); err != nil {
			t.Fatalf("dispatch %d: %v", i, err)
		}
	}

	if auth.userIDCount != 1 {
		t.Fatalf("user lookup count = %d, want one restored identity lookup", auth.userIDCount)
	}
	update := pushedUserStatus(t, sessions.snapshot().message)
	if update.UserID != bob.ID {
		t.Fatalf("status user = %d, want bob", update.UserID)
	}
	if status, ok := update.Status.(*tg.UserStatusOnline); !ok || status.Expires <= int(time.Now().Unix()) {
		t.Fatalf("status = %#v, want online peer status for restored session", update.Status)
	}
}

// TestTDesktopStartupRPCsEncode 验证第一阶段 TDesktop 启动 RPC 均能被路由并编码回包。
func TestTDesktopStartupRPCsEncode(t *testing.T) {
	r := New(Config{DC: 2, IP: "127.0.0.1", Port: 2398}, Deps{
		Users: staticUsersService{user: domain.User{
			ID:         1000000001,
			AccessHash: 42,
			FirstName:  "Test",
			LastName:   "User",
			Phone:      "15550000000",
		}},
	}, zaptest.NewLogger(t), clock.System)

	tests := []struct {
		name string
		req  bin.Encoder
	}{
		{name: "auth.bindTempAuthKey", req: &tg.AuthBindTempAuthKeyRequest{PermAuthKeyID: 1, Nonce: 2, ExpiresAt: 3, EncryptedMessage: []byte("binding")}},
		{name: "auth.exportLoginToken", req: &tg.AuthExportLoginTokenRequest{APIID: 1, APIHash: "hash"}},
		{name: "help.getAppConfig", req: &tg.HelpGetAppConfigRequest{}},
		{name: "help.getCountriesList", req: &tg.HelpGetCountriesListRequest{LangCode: "en"}},
		{name: "help.getTimezonesList", req: &tg.HelpGetTimezonesListRequest{}},
		{name: "help.getPeerColors", req: &tg.HelpGetPeerColorsRequest{}},
		{name: "help.getPeerProfileColors", req: &tg.HelpGetPeerProfileColorsRequest{}},
		{name: "help.getPromoData", req: &tg.HelpGetPromoDataRequest{}},
		{name: "help.getTermsOfServiceUpdate", req: &tg.HelpGetTermsOfServiceUpdateRequest{}},
		{name: "help.getPremiumPromo", req: &tg.HelpGetPremiumPromoRequest{}},
		{name: "auth.initPasskeyLogin", req: &tg.AuthInitPasskeyLoginRequest{APIID: 4, APIHash: "test"}},
		{name: "account.getPassword", req: &tg.AccountGetPasswordRequest{}},
		{name: "account.getNotifySettings", req: &tg.AccountGetNotifySettingsRequest{Peer: &tg.InputNotifyUsers{}}},
		{name: "account.resetNotifySettings", req: &tg.AccountResetNotifySettingsRequest{}},
		{name: "account.getPrivacy", req: &tg.AccountGetPrivacyRequest{Key: &tg.InputPrivacyKeyStatusTimestamp{}}},
		{name: "account.getAuthorizations", req: &tg.AccountGetAuthorizationsRequest{}},
		{name: "account.getWebAuthorizations", req: &tg.AccountGetWebAuthorizationsRequest{}},
		{name: "account.getNotifyExceptions", req: &tg.AccountGetNotifyExceptionsRequest{}},
		{name: "account.getDefaultEmojiStatuses", req: &tg.AccountGetDefaultEmojiStatusesRequest{}},
		{name: "account.getRecentEmojiStatuses", req: &tg.AccountGetRecentEmojiStatusesRequest{}},
		{name: "account.getCollectibleEmojiStatuses", req: &tg.AccountGetCollectibleEmojiStatusesRequest{}},
		{name: "account.getDefaultProfilePhotoEmojis", req: &tg.AccountGetDefaultProfilePhotoEmojisRequest{}},
		{name: "account.getDefaultGroupPhotoEmojis", req: &tg.AccountGetDefaultGroupPhotoEmojisRequest{}},
		{name: "account.getDefaultBackgroundEmojis", req: &tg.AccountGetDefaultBackgroundEmojisRequest{}},
		{name: "account.getChannelDefaultEmojiStatuses", req: &tg.AccountGetChannelDefaultEmojiStatusesRequest{}},
		{name: "account.getChannelRestrictedStatusEmojis", req: &tg.AccountGetChannelRestrictedStatusEmojisRequest{}},
		{name: "account.getConnectedBots", req: &tg.AccountGetConnectedBotsRequest{}},
		{name: "account.getReactionsNotifySettings", req: &tg.AccountGetReactionsNotifySettingsRequest{}},
		{name: "account.getContactSignUpNotification", req: &tg.AccountGetContactSignUpNotificationRequest{}},
		{name: "account.getThemes", req: &tg.AccountGetThemesRequest{Format: "tdesktop"}},
		{name: "account.getContentSettings", req: &tg.AccountGetContentSettingsRequest{}},
		{name: "account.getGlobalPrivacySettings", req: &tg.AccountGetGlobalPrivacySettingsRequest{}},
		{name: "account.getPasskeys", req: &tg.AccountGetPasskeysRequest{}},
		{name: "account.getAutoDownloadSettings", req: &tg.AccountGetAutoDownloadSettingsRequest{}},
		{name: "account.getSavedMusicIds", req: &tg.AccountGetSavedMusicIDsRequest{}},
		{name: "account.resetPassword", req: &tg.AccountResetPasswordRequest{}},
		{name: "account.updateStatus", req: &tg.AccountUpdateStatusRequest{Offline: true}},
		{name: "updates.getDifference", req: &tg.UpdatesGetDifferenceRequest{}},
		{name: "users.getFullUser", req: &tg.UsersGetFullUserRequest{ID: &tg.InputUserSelf{}}},
		{name: "users.getSavedMusic", req: &tg.UsersGetSavedMusicRequest{ID: &tg.InputUserSelf{}, Limit: 20}},
		{name: "users.getSavedMusicByID", req: &tg.UsersGetSavedMusicByIDRequest{ID: &tg.InputUserSelf{}, Documents: []tg.InputDocumentClass{}}},
		{name: "messages.getDialogFilters", req: &tg.MessagesGetDialogFiltersRequest{}},
		{name: "messages.getDialogs", req: &tg.MessagesGetDialogsRequest{OffsetPeer: &tg.InputPeerEmpty{}, Limit: 20}},
		{name: "messages.getPinnedDialogs", req: &tg.MessagesGetPinnedDialogsRequest{}},
		{name: "messages.getPeerDialogs", req: &tg.MessagesGetPeerDialogsRequest{Peers: []tg.InputDialogPeerClass{&tg.InputDialogPeer{Peer: &tg.InputPeerUser{UserID: domain.OfficialSystemUserID, AccessHash: domain.OfficialSystemUser().AccessHash}}}}},
		{name: "messages.getAvailableReactions", req: &tg.MessagesGetAvailableReactionsRequest{}},
		{name: "messages.getAvailableEffects", req: &tg.MessagesGetAvailableEffectsRequest{}},
		{name: "messages.getStickers", req: &tg.MessagesGetStickersRequest{}},
		{name: "messages.getStickerSet", req: &tg.MessagesGetStickerSetRequest{Stickerset: &tg.InputStickerSetEmpty{}}},
		{name: "messages.getEmojiGroups", req: &tg.MessagesGetEmojiGroupsRequest{}},
		{name: "messages.getEmojiStickerGroups", req: &tg.MessagesGetEmojiStickerGroupsRequest{}},
		{name: "messages.getEmojiProfilePhotoGroups", req: &tg.MessagesGetEmojiProfilePhotoGroupsRequest{}},
		{name: "messages.getEmojiKeywordsLanguages", req: &tg.MessagesGetEmojiKeywordsLanguagesRequest{LangCodes: []string{"en"}}},
		{name: "messages.getAttachMenuBots", req: &tg.MessagesGetAttachMenuBotsRequest{}},
		{name: "messages.getQuickReplies", req: &tg.MessagesGetQuickRepliesRequest{}},
		{name: "messages.getSavedHistory", req: &tg.MessagesGetSavedHistoryRequest{Peer: &tg.InputPeerSelf{}, Limit: 20}},
		{name: "messages.readSavedHistory", req: &tg.MessagesReadSavedHistoryRequest{ParentPeer: &tg.InputPeerChannel{ChannelID: 1, AccessHash: 1}, Peer: &tg.InputPeerSelf{}}},
		{name: "messages.deleteSavedHistory", req: &tg.MessagesDeleteSavedHistoryRequest{Peer: &tg.InputPeerSelf{}}},
		{name: "messages.getPeerSettings", req: &tg.MessagesGetPeerSettingsRequest{Peer: &tg.InputPeerUser{UserID: domain.OfficialSystemUserID, AccessHash: domain.OfficialSystemUser().AccessHash}}},
		{name: "messages.getHistory", req: &tg.MessagesGetHistoryRequest{Peer: &tg.InputPeerUser{UserID: domain.OfficialSystemUserID, AccessHash: domain.OfficialSystemUser().AccessHash}, Limit: 20}},
		{name: "messages.readHistory", req: &tg.MessagesReadHistoryRequest{Peer: &tg.InputPeerUser{UserID: domain.OfficialSystemUserID, AccessHash: domain.OfficialSystemUser().AccessHash}}},
		{name: "messages.search", req: &tg.MessagesSearchRequest{Peer: &tg.InputPeerUser{UserID: domain.OfficialSystemUserID, AccessHash: domain.OfficialSystemUser().AccessHash}, Filter: &tg.InputMessagesFilterEmpty{}, Limit: 20}},
		{name: "messages.searchGlobal", req: &tg.MessagesSearchGlobalRequest{Q: "login", Filter: &tg.InputMessagesFilterEmpty{}, OffsetPeer: &tg.InputPeerEmpty{}, Limit: 20}},
		{name: "messages.getWebPage", req: &tg.MessagesGetWebPageRequest{URL: "https://example.invalid"}},
		{name: "messages.getScheduledHistory", req: &tg.MessagesGetScheduledHistoryRequest{Peer: &tg.InputPeerUser{UserID: domain.OfficialSystemUserID, AccessHash: domain.OfficialSystemUser().AccessHash}}},
		{name: "contacts.getContacts", req: &tg.ContactsGetContactsRequest{}},
		{name: "contacts.search", req: &tg.ContactsSearchRequest{Q: "Test", Limit: 20}},
		{name: "contacts.getBlocked", req: &tg.ContactsGetBlockedRequest{Limit: 20}},
		{name: "contacts.getTopPeers", req: &tg.ContactsGetTopPeersRequest{Correspondents: true, Limit: 10}},
		{name: "contacts.getSponsoredPeers", req: &tg.ContactsGetSponsoredPeersRequest{Q: "Test"}},
		{name: "stories.getAllStories", req: &tg.StoriesGetAllStoriesRequest{}},
		{name: "stories.getStoriesArchive", req: &tg.StoriesGetStoriesArchiveRequest{Peer: &tg.InputPeerSelf{}, Limit: 20}},
		{name: "stories.getPinnedStories", req: &tg.StoriesGetPinnedStoriesRequest{Peer: &tg.InputPeerSelf{}, Limit: 20}},
		{name: "stories.getAlbums", req: &tg.StoriesGetAlbumsRequest{Peer: &tg.InputPeerSelf{}}},
		{name: "payments.getStarGiftActiveAuctions", req: &tg.PaymentsGetStarGiftActiveAuctionsRequest{}},
		{name: "payments.getSavedStarGifts", req: &tg.PaymentsGetSavedStarGiftsRequest{Peer: &tg.InputPeerSelf{}, Limit: 20}},
		{name: "payments.getSavedStarGift", req: &tg.PaymentsGetSavedStarGiftRequest{Stargift: []tg.InputSavedStarGiftClass{}}},
		{name: "aicompose.getTones", req: &tg.AicomposeGetTonesRequest{}},
		{name: "langpack.getLangPack", req: &tg.LangpackGetLangPackRequest{LangPack: "tdesktop", LangCode: "en"}},
		{name: "langpack.getDifference", req: &tg.LangpackGetDifferenceRequest{LangPack: "tdesktop", LangCode: "en", FromVersion: 1}},
		{name: "langpack.getStrings", req: &tg.LangpackGetStringsRequest{LangPack: "tdesktop", LangCode: "en", Keys: []string{"lng_intro_about"}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var in bin.Buffer
			if err := tt.req.Encode(&in); err != nil {
				t.Fatalf("encode request: %v", err)
			}
			ctx := WithUserID(context.Background(), 1000000001)
			enc, err := r.Dispatch(ctx, [8]byte{}, 0, &in)
			if err != nil {
				t.Fatalf("dispatch: %v", err)
			}
			var out bin.Buffer
			if err := enc.Encode(&out); err != nil {
				t.Fatalf("encode response: %v", err)
			}
			if out.Len() == 0 {
				t.Fatal("encoded response is empty")
			}
		})
	}
}

func TestContactsSearchFindsUsers(t *testing.T) {
	ctx := context.Background()
	users := memory.NewUserStore()
	owner, err := users.Create(ctx, domain.User{AccessHash: 1, Phone: "15550000001", FirstName: "Owner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	friend, err := users.Create(ctx, domain.User{AccessHash: 2, Phone: "15550000002", FirstName: "Searchable", LastName: "Friend", Username: "search_friend"})
	if err != nil {
		t.Fatalf("create friend: %v", err)
	}
	r := New(Config{}, Deps{
		Contacts: appcontacts.NewService(memory.NewContactStore(), users),
	}, zaptest.NewLogger(t), clock.System)

	var in bin.Buffer
	if err := (&tg.ContactsSearchRequest{Q: "@search", Limit: 20}).Encode(&in); err != nil {
		t.Fatalf("encode request: %v", err)
	}
	enc, err := r.Dispatch(WithUserID(ctx, owner.ID), [8]byte{}, 0, &in)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	box, ok := enc.(*tg.ContactsFound)
	if !ok {
		t.Fatalf("result type = %T, want *tg.ContactsFound", enc)
	}
	if len(box.Results) != 1 || len(box.Users) != 1 {
		t.Fatalf("search result sizes = results %d users %d, want 1/1", len(box.Results), len(box.Users))
	}
	peer, ok := box.Results[0].(*tg.PeerUser)
	if !ok || peer.UserID != friend.ID {
		t.Fatalf("peer = %T %+v, want friend", box.Results[0], box.Results[0])
	}
}

func TestContactsSearchFindsPublicChannels(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, err := userStore.Create(ctx, domain.User{AccessHash: 11, Phone: "15550000011", FirstName: "Owner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	viewer, err := userStore.Create(ctx, domain.User{AccessHash: 12, Phone: "15550000012", FirstName: "Viewer"})
	if err != nil {
		t.Fatalf("create viewer: %v", err)
	}
	stranger, err := userStore.Create(ctx, domain.User{AccessHash: 13, Phone: "15550000013", FirstName: "Stranger"})
	if err != nil {
		t.Fatalf("create stranger: %v", err)
	}
	channelStore := memory.NewChannelStore()
	channelSvc := appchannels.NewService(channelStore)
	created, err := channelSvc.CreateChannel(ctx, owner.ID, domain.CreateChannelRequest{
		Title:         "CU Public RPC",
		Megagroup:     true,
		MemberUserIDs: []int64{viewer.ID},
		Date:          1700000012,
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	public, err := channelSvc.UpdateUsername(ctx, owner.ID, domain.UpdateChannelUsernameRequest{
		ChannelID: created.Channel.ID,
		Username:  "cu_public_rpc",
	})
	if err != nil {
		t.Fatalf("set channel username: %v", err)
	}
	r := New(Config{}, Deps{
		Users:    appusers.NewService(userStore),
		Contacts: appcontacts.NewService(memory.NewContactStore(), userStore),
		Channels: channelSvc,
	}, zaptest.NewLogger(t), clock.System)

	var in bin.Buffer
	if err := (&tg.ContactsSearchRequest{Q: "CU Public", Limit: 20}).Encode(&in); err != nil {
		t.Fatalf("encode request: %v", err)
	}
	enc, err := r.Dispatch(WithUserID(ctx, viewer.ID), [8]byte{}, 0, &in)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	box, ok := enc.(*tg.ContactsFound)
	if !ok {
		t.Fatalf("result type = %T, want *tg.ContactsFound", enc)
	}
	if len(box.MyResults) != 1 || len(box.Chats) != 1 {
		t.Fatalf("search result sizes = my %d chats %d, want 1/1", len(box.MyResults), len(box.Chats))
	}
	peer, ok := box.MyResults[0].(*tg.PeerChannel)
	if !ok || peer.ChannelID != public.ID {
		t.Fatalf("peer = %T %+v, want public channel", box.MyResults[0], box.MyResults[0])
	}
	chat, ok := box.Chats[0].(*tg.Channel)
	if !ok || chat.ID != public.ID || chat.Username != "cu_public_rpc" {
		t.Fatalf("chat = %T %+v, want public channel chat", box.Chats[0], box.Chats[0])
	}
	if chat.Left {
		t.Fatalf("member search chat left = true, want active member channel")
	}

	var strangerIn bin.Buffer
	if err := (&tg.ContactsSearchRequest{Q: "CU Public", Limit: 20}).Encode(&strangerIn); err != nil {
		t.Fatalf("encode stranger request: %v", err)
	}
	strangerEnc, err := r.Dispatch(WithUserID(ctx, stranger.ID), [8]byte{}, 0, &strangerIn)
	if err != nil {
		t.Fatalf("dispatch stranger: %v", err)
	}
	strangerBox, ok := strangerEnc.(*tg.ContactsFound)
	if !ok {
		t.Fatalf("stranger result type = %T, want *tg.ContactsFound", strangerEnc)
	}
	if len(strangerBox.Results) != 1 || len(strangerBox.Chats) != 1 {
		t.Fatalf("stranger search sizes = results %d chats %d, want 1/1", len(strangerBox.Results), len(strangerBox.Chats))
	}
	strangerPeer, ok := strangerBox.Results[0].(*tg.PeerChannel)
	if !ok || strangerPeer.ChannelID != public.ID {
		t.Fatalf("stranger peer = %T %+v, want public channel", strangerBox.Results[0], strangerBox.Results[0])
	}
	strangerChat, ok := strangerBox.Chats[0].(*tg.Channel)
	if !ok || !strangerChat.Left || strangerChat.ID != public.ID {
		t.Fatalf("stranger chat = %T %+v, want left public channel", strangerBox.Chats[0], strangerBox.Chats[0])
	}

	resolved, err := r.onContactsResolveUsername(WithUserID(ctx, viewer.ID), &tg.ContactsResolveUsernameRequest{Username: "@CU_PUBLIC_RPC"})
	if err != nil {
		t.Fatalf("resolve username: %v", err)
	}
	resolvedPeer, ok := resolved.Peer.(*tg.PeerChannel)
	if !ok || resolvedPeer.ChannelID != public.ID || len(resolved.Chats) != 1 {
		t.Fatalf("resolved channel = %+v, want peer + chat", resolved)
	}
}

func TestUsernameRPCLifecycle(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, err := userStore.Create(ctx, domain.User{AccessHash: 1, Phone: "15550000001", FirstName: "Owner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	other, err := userStore.Create(ctx, domain.User{AccessHash: 2, Phone: "15550000002", FirstName: "Other", Username: "taken_name"})
	if err != nil {
		t.Fatalf("create other: %v", err)
	}
	r := New(Config{}, Deps{Users: appusers.NewService(userStore)}, zaptest.NewLogger(t), clock.System)
	reqCtx := WithUserID(ctx, owner.ID)

	ok, err := r.onAccountCheckUsername(reqCtx, "taken_name")
	if err != nil || ok {
		t.Fatalf("check occupied = ok %v err %v, want false/nil", ok, err)
	}
	user, err := r.onAccountUpdateUsername(reqCtx, "owner_name")
	if err != nil {
		t.Fatalf("update username: %v", err)
	}
	self, ok := user.(*tg.User)
	if !ok || self.Username != "owner_name" || len(self.Usernames) != 1 || !self.Usernames[0].Active {
		t.Fatalf("updated user = %T %+v, want self with active username", user, user)
	}

	resolved, err := r.onContactsResolveUsername(reqCtx, &tg.ContactsResolveUsernameRequest{Username: "@OWNER_NAME"})
	if err != nil {
		t.Fatalf("resolve username: %v", err)
	}
	peer, ok := resolved.Peer.(*tg.PeerUser)
	if !ok || peer.UserID != owner.ID || len(resolved.Users) != 1 {
		t.Fatalf("resolved username = %+v, want owner peer", resolved)
	}

	resolvedPhone, err := r.onContactsResolvePhone(reqCtx, "+15550000002")
	if err != nil {
		t.Fatalf("resolve phone: %v", err)
	}
	phonePeer, ok := resolvedPhone.Peer.(*tg.PeerUser)
	if !ok || phonePeer.UserID != other.ID {
		t.Fatalf("resolved phone peer = %+v, want other", resolvedPhone.Peer)
	}
}

func TestAccountUpdateProfileRPC(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, err := userStore.Create(ctx, domain.User{AccessHash: 1, Phone: "15550000001", FirstName: "Owner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	r := New(Config{}, Deps{Users: appusers.NewService(userStore)}, zaptest.NewLogger(t), clock.System)
	req := &tg.AccountUpdateProfileRequest{}
	req.SetFirstName("Updated")
	req.SetLastName("User")
	req.SetAbout("profile bio")

	user, err := r.onAccountUpdateProfile(WithUserID(ctx, owner.ID), req)
	if err != nil {
		t.Fatalf("update profile: %v", err)
	}
	self, ok := user.(*tg.User)
	if !ok || self.FirstName != "Updated" || self.LastName != "User" {
		t.Fatalf("updated user = %T %+v, want updated self user", user, user)
	}
	full, err := r.onUsersGetFullUser(WithUserID(ctx, owner.ID), &tg.InputUserSelf{})
	if err != nil {
		t.Fatalf("get full user: %v", err)
	}
	if full.FullUser.About != "profile bio" {
		t.Fatalf("full about = %q, want profile bio", full.FullUser.About)
	}
}

func TestUsersSavedMusicStubsValidateInput(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, err := userStore.Create(ctx, domain.User{AccessHash: 11, Phone: "15550000001", FirstName: "Owner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	friend, err := userStore.Create(ctx, domain.User{AccessHash: 22, Phone: "15550000002", FirstName: "Friend"})
	if err != nil {
		t.Fatalf("create friend: %v", err)
	}
	r := New(Config{}, Deps{Users: appusers.NewService(userStore)}, zaptest.NewLogger(t), clock.System)
	reqCtx := WithUserID(ctx, owner.ID)

	got, err := r.onUsersGetSavedMusic(reqCtx, &tg.UsersGetSavedMusicRequest{
		ID:     &tg.InputUser{UserID: friend.ID, AccessHash: friend.AccessHash},
		Offset: 0,
		Limit:  20,
	})
	if err != nil {
		t.Fatalf("users.getSavedMusic: %v", err)
	}
	music, ok := got.(*tg.UsersSavedMusic)
	if !ok || music.Count != 0 || len(music.Documents) != 0 {
		t.Fatalf("saved music = %T %+v, want empty users.savedMusic", got, got)
	}
	if _, err := r.onUsersGetSavedMusic(reqCtx, &tg.UsersGetSavedMusicRequest{
		ID:    &tg.InputUserSelf{},
		Limit: maxSavedMusicLimit + 1,
	}); err == nil || !strings.Contains(err.Error(), "LIMIT_INVALID") {
		t.Fatalf("large saved music limit err = %v, want LIMIT_INVALID", err)
	}
	if _, err := r.onUsersGetSavedMusic(reqCtx, &tg.UsersGetSavedMusicRequest{
		ID:    &tg.InputUser{UserID: friend.ID, AccessHash: friend.AccessHash + 1},
		Limit: 20,
	}); err == nil || !strings.Contains(err.Error(), "USER_ID_INVALID") {
		t.Fatalf("bad saved music user err = %v, want USER_ID_INVALID", err)
	}
}

func TestDialogFilterFromGetDialogsRequestUsesAllParameters(t *testing.T) {
	r := New(Config{}, Deps{
		Users: staticUsersService{user: domain.User{ID: 1000000001, AccessHash: 42}},
	}, zaptest.NewLogger(t), clock.System)
	req := &tg.MessagesGetDialogsRequest{
		ExcludePinned: true,
		OffsetDate:    1700000000,
		OffsetID:      22,
		OffsetPeer:    &tg.InputPeerUser{UserID: domain.OfficialSystemUserID, AccessHash: domain.OfficialSystemUser().AccessHash},
		Limit:         999,
		Hash:          123456,
	}
	req.SetFolderID(7)

	filter, err := r.dialogFilterFromRequest(context.Background(), 1000000001, req)
	if err != nil {
		t.Fatalf("dialogFilterFromRequest: %v", err)
	}
	if !filter.ExcludePinned || !filter.HasFolderID || filter.FolderID != 7 {
		t.Fatalf("folder/pinned filter = %+v", filter)
	}
	if filter.OffsetDate != req.OffsetDate || filter.OffsetID != req.OffsetID || !filter.HasOffsetPeer || filter.OffsetPeer.ID != domain.OfficialSystemUserID {
		t.Fatalf("offset filter = %+v", filter)
	}
	if filter.Limit != 500 || filter.Hash != req.Hash {
		t.Fatalf("limit/hash filter = %+v", filter)
	}
}

func TestMessagesGetDialogsReturnsNotModifiedFromFullListHash(t *testing.T) {
	dialogs := &captureDialogs{list: domain.DialogList{Count: 3, Hash: 77}}
	r := New(Config{}, Deps{Dialogs: dialogs}, zaptest.NewLogger(t), clock.System)
	req := &tg.MessagesGetDialogsRequest{
		OffsetPeer: &tg.InputPeerEmpty{},
		Limit:      20,
		Hash:       77,
	}
	var in bin.Buffer
	if err := req.Encode(&in); err != nil {
		t.Fatalf("encode request: %v", err)
	}

	enc, err := r.Dispatch(WithUserID(context.Background(), 1000000001), [8]byte{}, 0, &in)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	box, ok := enc.(*tg.MessagesDialogsBox)
	if !ok {
		t.Fatalf("response = %T, want *tg.MessagesDialogsBox", enc)
	}
	got, ok := box.Dialogs.(*tg.MessagesDialogsNotModified)
	if !ok {
		t.Fatalf("boxed response = %T, want *tg.MessagesDialogsNotModified", box.Dialogs)
	}
	if got.Count != 3 || dialogs.filter.Hash != 77 {
		t.Fatalf("not modified = %+v filter %+v, want count/hash from service", got, dialogs.filter)
	}
}

func TestMessagesCreateChatCreatesMegagroupAndDialogsRPC(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, err := userStore.Create(ctx, domain.User{AccessHash: 11, Phone: "15550001001", FirstName: "Owner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	friend, err := userStore.Create(ctx, domain.User{AccessHash: 22, Phone: "15550001002", FirstName: "Friend"})
	if err != nil {
		t.Fatalf("create friend: %v", err)
	}
	channelStore := memory.NewChannelStore()
	r := New(Config{}, Deps{
		Users:    appusers.NewService(userStore),
		Channels: appchannels.NewService(channelStore),
		Dialogs:  appdialogs.NewService(memory.NewDialogStore(), channelStore),
	}, zaptest.NewLogger(t), clock.System)

	invited, err := r.onMessagesCreateChat(WithUserID(ctx, owner.ID), &tg.MessagesCreateChatRequest{
		Users: []tg.InputUserClass{&tg.InputUser{UserID: friend.ID, AccessHash: friend.AccessHash}},
		Title: "E2E Group",
	})
	if err != nil {
		t.Fatalf("create chat: %v", err)
	}
	updates, ok := invited.Updates.(*tg.Updates)
	if !ok || len(updates.Chats) != 1 {
		t.Fatalf("updates = %T %+v, want one chat", invited.Updates, invited.Updates)
	}
	channel, ok := updates.Chats[0].(*tg.Channel)
	if !ok || !channel.Megagroup || channel.Broadcast {
		t.Fatalf("chat = %#v, want megagroup channel", updates.Chats[0])
	}
	if len(updates.Updates) != 4 {
		t.Fatalf("updates len = %d, want create/invite service messages plus channel refreshes", len(updates.Updates))
	}
	newMsg, ok := updates.Updates[0].(*tg.UpdateNewChannelMessage)
	if !ok || newMsg.Pts != 1 || newMsg.PtsCount != 1 {
		t.Fatalf("create update = %#v, want channel pts=1", updates.Updates[0])
	}
	if refresh, ok := updates.Updates[1].(*tg.UpdateChannel); !ok || refresh.ChannelID != channel.ID {
		t.Fatalf("create refresh = %#v, want channel refresh", updates.Updates[1])
	}
	service, ok := newMsg.Message.(*tg.MessageService)
	if !ok {
		t.Fatalf("create message = %T, want service", newMsg.Message)
	}
	if _, ok := service.Action.(*tg.MessageActionChannelCreate); !ok {
		t.Fatalf("service action = %T, want channel create", service.Action)
	}
	inviteMsg, ok := updates.Updates[2].(*tg.UpdateNewChannelMessage)
	if !ok || inviteMsg.Pts != 2 || inviteMsg.PtsCount != 1 {
		t.Fatalf("invite update = %#v, want channel pts=2", updates.Updates[2])
	}
	if refresh, ok := updates.Updates[3].(*tg.UpdateChannel); !ok || refresh.ChannelID != channel.ID {
		t.Fatalf("invite refresh = %#v, want channel refresh", updates.Updates[3])
	}
	inviteService, ok := inviteMsg.Message.(*tg.MessageService)
	if !ok {
		t.Fatalf("invite message = %T, want service", inviteMsg.Message)
	}
	addUser, ok := inviteService.Action.(*tg.MessageActionChatAddUser)
	if !ok || len(addUser.Users) != 1 || addUser.Users[0] != friend.ID {
		t.Fatalf("invite action = %#v, want add friend %d", inviteService.Action, friend.ID)
	}
	if len(updates.Users) != 2 {
		t.Fatalf("updates users len = %d, want owner + friend", len(updates.Users))
	}

	participants, err := r.onChannelsGetParticipants(WithUserID(ctx, owner.ID), &tg.ChannelsGetParticipantsRequest{
		Channel: &tg.InputChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		Filter:  &tg.ChannelParticipantsRecent{},
		Limit:   10,
	})
	if err != nil {
		t.Fatalf("get participants: %v", err)
	}
	participantList, ok := participants.(*tg.ChannelsChannelParticipants)
	if !ok || participantList.Count != 2 || len(participantList.Participants) != 2 || len(participantList.Users) != 2 {
		t.Fatalf("participants = %T %+v, want owner + friend participants/users", participants, participants)
	}

	req := &tg.MessagesGetDialogsRequest{OffsetPeer: &tg.InputPeerEmpty{}, Limit: 20}
	var b bin.Buffer
	if err := req.Encode(&b); err != nil {
		t.Fatalf("encode get dialogs: %v", err)
	}
	enc, err := r.Dispatch(WithUserID(ctx, owner.ID), [8]byte{}, 0, &b)
	if err != nil {
		t.Fatalf("dispatch get dialogs: %v", err)
	}
	box, ok := enc.(*tg.MessagesDialogsBox)
	if !ok {
		t.Fatalf("dialogs response = %T, want box", enc)
	}
	dialogs, ok := box.Dialogs.(*tg.MessagesDialogs)
	if !ok || len(dialogs.Dialogs) != 1 || len(dialogs.Chats) != 1 || len(dialogs.Messages) != 1 {
		t.Fatalf("dialogs = %T %+v, want channel dialog/chat/message", box.Dialogs, box.Dialogs)
	}
	dialog := dialogs.Dialogs[0].(*tg.Dialog)
	if peer, ok := dialog.Peer.(*tg.PeerChannel); !ok || peer.ChannelID != channel.ID {
		t.Fatalf("dialog peer = %#v, want channel %d", dialog.Peer, channel.ID)
	}
}

func TestMessagesCreateChatRejectsEmptyInviteListRPC(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, err := userStore.Create(ctx, domain.User{AccessHash: 21, Phone: "15550001021", FirstName: "Owner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	r := New(Config{}, Deps{
		Users:    appusers.NewService(userStore),
		Channels: appchannels.NewService(memory.NewChannelStore()),
	}, zaptest.NewLogger(t), clock.System)

	if _, err := r.onMessagesCreateChat(WithUserID(ctx, owner.ID), &tg.MessagesCreateChatRequest{
		Title: "No Invitees",
	}); err == nil || !strings.Contains(err.Error(), "USERS_TOO_FEW") {
		t.Fatalf("create chat without users err = %v, want USERS_TOO_FEW", err)
	}
}

func TestMessagesCreateChatTDesktopReturnsLegacyChatAndAcceptsInputPeerChatRPC(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, err := userStore.Create(ctx, domain.User{AccessHash: 31, Phone: "15550001031", FirstName: "Owner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	friend, err := userStore.Create(ctx, domain.User{AccessHash: 32, Phone: "15550001032", FirstName: "Friend"})
	if err != nil {
		t.Fatalf("create friend: %v", err)
	}
	channelStore := memory.NewChannelStore()
	r := New(Config{}, Deps{
		Users:    appusers.NewService(userStore),
		Channels: appchannels.NewService(channelStore),
		Dialogs:  appdialogs.NewService(memory.NewDialogStore(), channelStore),
	}, zaptest.NewLogger(t), clock.System)
	tdCtx := WithClientInfo(WithUserID(ctx, owner.ID), ClientInfo{
		DeviceModel: "Desktop",
		AppVersion:  "6.8.4 x64",
		LangPack:    "tdesktop",
	})

	invited, err := r.onMessagesCreateChat(tdCtx, &tg.MessagesCreateChatRequest{
		Users: []tg.InputUserClass{&tg.InputUser{UserID: friend.ID, AccessHash: friend.AccessHash}},
		Title: "TDesktop Group",
	})
	if err != nil {
		t.Fatalf("create chat: %v", err)
	}
	updates, ok := invited.Updates.(*tg.Updates)
	if !ok || len(updates.Chats) != 2 {
		t.Fatalf("updates = %T %+v, want legacy chat + channel", invited.Updates, invited.Updates)
	}
	legacy, ok := updates.Chats[0].(*tg.Chat)
	if !ok || !legacy.Deactivated {
		t.Fatalf("legacy chat = %#v, want migrated chat", updates.Chats[0])
	}
	channel, ok := updates.Chats[1].(*tg.Channel)
	if !ok || !channel.Megagroup || channel.Broadcast {
		t.Fatalf("channel = %#v, want megagroup channel", updates.Chats[1])
	}
	if !channel.Creator {
		t.Fatalf("channel creator flag = false, want true for creator")
	}
	if rights, ok := channel.GetAdminRights(); !ok || !rights.ChangeInfo || !rights.InviteUsers {
		t.Fatalf("channel admin rights = %+v ok=%v, want creator manage rights", rights, ok)
	}
	migrated, ok := legacy.GetMigratedTo()
	if !ok {
		t.Fatalf("legacy chat missing migrated_to")
	}
	migratedTo, ok := migrated.(*tg.InputChannel)
	if !ok || migratedTo.ChannelID != channel.ID || migratedTo.AccessHash != channel.AccessHash {
		t.Fatalf("migrated_to = %#v, want channel %d/%d", migrated, channel.ID, channel.AccessHash)
	}

	participants, err := r.onChannelsGetParticipants(tdCtx, &tg.ChannelsGetParticipantsRequest{
		Channel: &tg.InputChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		Filter:  &tg.ChannelParticipantsRecent{},
		Limit:   10,
	})
	if err != nil {
		t.Fatalf("get participants after create chat: %v", err)
	}
	participantList, ok := participants.(*tg.ChannelsChannelParticipants)
	if !ok || len(participantList.Chats) != 0 {
		t.Fatalf("participants = %T %+v, want no chat side vector", participants, participants)
	}
	if participantList.Count != 2 || len(participantList.Participants) != 2 || len(participantList.Users) != 2 {
		t.Fatalf("participants count/rows/users = %d/%d/%d, want owner + friend",
			participantList.Count, len(participantList.Participants), len(participantList.Users))
	}
	legacyHistoryReq := &tg.MessagesGetHistoryRequest{
		Peer:  &tg.InputPeerChat{ChatID: channel.ID},
		Limit: 20,
	}
	var legacyHistoryBuf bin.Buffer
	if err := legacyHistoryReq.Encode(&legacyHistoryBuf); err != nil {
		t.Fatalf("encode legacy history: %v", err)
	}
	legacyHistory, err := r.Dispatch(tdCtx, [8]byte{}, 0, &legacyHistoryBuf)
	if err != nil {
		t.Fatalf("legacy history: %v", err)
	}
	legacyBox, ok := legacyHistory.(*tg.MessagesMessagesBox)
	if !ok {
		t.Fatalf("legacy history = %T %+v, want messages box", legacyHistory, legacyHistory)
	}
	legacyMessages, ok := legacyBox.Messages.(*tg.MessagesMessages)
	if !ok || len(legacyMessages.Messages) != 0 {
		t.Fatalf("legacy history = %T %+v, want empty messages.messages", legacyHistory, legacyHistory)
	}

	sent, err := r.onMessagesSendMessage(tdCtx, &tg.MessagesSendMessageRequest{
		Peer:     &tg.InputPeerChat{ChatID: channel.ID},
		RandomID: 99,
		Message:  "via legacy input peer",
	})
	if err != nil {
		t.Fatalf("send via inputPeerChat: %v", err)
	}
	sentUpdates, ok := sent.(*tg.Updates)
	if !ok || len(sentUpdates.Updates) < 2 {
		t.Fatalf("send updates = %T %+v, want channel message updates", sent, sent)
	}
	newMsg, ok := sentUpdates.Updates[1].(*tg.UpdateNewChannelMessage)
	if !ok {
		t.Fatalf("send update = %#v, want updateNewChannelMessage", sentUpdates.Updates[1])
	}
	msg, ok := newMsg.Message.(*tg.Message)
	if !ok || msg.Message != "via legacy input peer" {
		t.Fatalf("sent message = %#v, want text channel message", newMsg.Message)
	}
	if peer, ok := msg.PeerID.(*tg.PeerChannel); !ok || peer.ChannelID != channel.ID {
		t.Fatalf("sent peer = %#v, want peerChannel %d", msg.PeerID, channel.ID)
	}
}

func TestMessagesCreateChatDispatchRemembersTDesktopClientInfo(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, err := userStore.Create(ctx, domain.User{AccessHash: 41, Phone: "15550001041", FirstName: "Owner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	friend, err := userStore.Create(ctx, domain.User{AccessHash: 42, Phone: "15550001042", FirstName: "Friend"})
	if err != nil {
		t.Fatalf("create friend: %v", err)
	}
	channelStore := memory.NewChannelStore()
	sessions := &captureSessions{}
	r := New(Config{DC: 2, IP: "127.0.0.1", Port: 2398}, Deps{
		Users:    appusers.NewService(userStore),
		Channels: appchannels.NewService(channelStore),
		Dialogs:  appdialogs.NewService(memory.NewDialogStore(), channelStore),
		Sessions: sessions,
	}, zaptest.NewLogger(t), clock.System)

	rawAuthKeyID := [8]byte{0x42, 0x01}
	sessionID := int64(77)
	initReq := &tg.InvokeWithLayerRequest{
		Layer: 225,
		Query: &tg.InitConnectionRequest{
			APIID:          111111,
			DeviceModel:    "Desktop",
			AppVersion:     "6.8.4 x64",
			SystemLangCode: "en",
			LangPack:       "tdesktop",
			LangCode:       "en",
			Query:          &tg.HelpGetConfigRequest{},
		},
	}
	var initBuf bin.Buffer
	if err := initReq.Encode(&initBuf); err != nil {
		t.Fatalf("encode init: %v", err)
	}
	if _, err := r.Dispatch(WithUserID(ctx, owner.ID), rawAuthKeyID, sessionID, &initBuf); err != nil {
		t.Fatalf("dispatch init: %v", err)
	}

	createReq := &tg.MessagesCreateChatRequest{
		Users: []tg.InputUserClass{&tg.InputUser{UserID: friend.ID, AccessHash: friend.AccessHash}},
		Title: "Dispatch TDesktop Group",
	}
	var createBuf bin.Buffer
	if err := createReq.Encode(&createBuf); err != nil {
		t.Fatalf("encode create chat: %v", err)
	}
	enc, err := r.Dispatch(context.Background(), rawAuthKeyID, sessionID, &createBuf)
	if err != nil {
		t.Fatalf("dispatch create chat: %v", err)
	}
	invited, ok := enc.(*tg.MessagesInvitedUsers)
	if !ok {
		t.Fatalf("create response = %T, want messages.invitedUsers", enc)
	}
	updates, ok := invited.Updates.(*tg.Updates)
	if !ok || len(updates.Chats) != 2 {
		t.Fatalf("updates = %T %+v, want legacy chat + channel", invited.Updates, invited.Updates)
	}
	if legacy, ok := updates.Chats[0].(*tg.Chat); !ok || !legacy.Deactivated {
		t.Fatalf("first chat = %#v, want migrated legacy chat", updates.Chats[0])
	}
	channel, ok := updates.Chats[1].(*tg.Channel)
	if !ok || !channel.Megagroup || !channel.Creator {
		t.Fatalf("second chat = %#v, want creator megagroup channel", updates.Chats[1])
	}
	if len(updates.Updates) != 4 {
		t.Fatalf("updates len = %d, want create/invite service messages plus channel refreshes", len(updates.Updates))
	}
	participants, err := r.onChannelsGetParticipants(WithUserID(ctx, owner.ID), &tg.ChannelsGetParticipantsRequest{
		Channel: &tg.InputChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		Filter:  &tg.ChannelParticipantsRecent{},
		Limit:   10,
	})
	if err != nil {
		t.Fatalf("get participants: %v", err)
	}
	list, ok := participants.(*tg.ChannelsChannelParticipants)
	if !ok || list.Count != 2 || len(list.Participants) != 2 || len(list.Users) != 2 {
		t.Fatalf("participants = %T %+v, want owner + friend", participants, participants)
	}
	sessions.mu.Lock()
	pushUserIDs := append([]int64(nil), sessions.pushUserIDs...)
	sessions.mu.Unlock()
	if len(pushUserIDs) != 1 || pushUserIDs[0] != friend.ID {
		t.Fatalf("push user ids = %v, want only invited friend %d", pushUserIDs, friend.ID)
	}
}

func TestMessagesCreateChatSessionWithoutClientInfoReturnsLegacyChat(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, err := userStore.Create(ctx, domain.User{AccessHash: 51, Phone: "15550001051", FirstName: "Owner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	friend, err := userStore.Create(ctx, domain.User{AccessHash: 52, Phone: "15550001052", FirstName: "Friend"})
	if err != nil {
		t.Fatalf("create friend: %v", err)
	}
	channelStore := memory.NewChannelStore()
	r := New(Config{}, Deps{
		Users:    appusers.NewService(userStore),
		Channels: appchannels.NewService(channelStore),
		Dialogs:  appdialogs.NewService(memory.NewDialogStore(), channelStore),
	}, zaptest.NewLogger(t), clock.System)

	invited, err := r.onMessagesCreateChat(WithSessionID(WithUserID(ctx, owner.ID), 99), &tg.MessagesCreateChatRequest{
		Users: []tg.InputUserClass{&tg.InputUser{UserID: friend.ID, AccessHash: friend.AccessHash}},
		Title: "Session Legacy Group",
	})
	if err != nil {
		t.Fatalf("create chat: %v", err)
	}
	updates, ok := invited.Updates.(*tg.Updates)
	if !ok || len(updates.Chats) != 2 {
		t.Fatalf("updates = %T %+v, want legacy chat + channel", invited.Updates, invited.Updates)
	}
	if legacy, ok := updates.Chats[0].(*tg.Chat); !ok || !legacy.Deactivated {
		t.Fatalf("first chat = %#v, want migrated legacy chat", updates.Chats[0])
	}
	if channel, ok := updates.Chats[1].(*tg.Channel); !ok || !channel.Megagroup || !channel.Creator {
		t.Fatalf("second chat = %#v, want creator megagroup channel", updates.Chats[1])
	}
}

func TestChannelsGetInactiveChannelsReturnsLeastActiveRPC(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, err := userStore.Create(ctx, domain.User{AccessHash: 41, Phone: "15550001041", FirstName: "Owner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	channelStore := memory.NewChannelStore()
	channelSvc := appchannels.NewService(channelStore)
	r := New(Config{}, Deps{
		Users:    appusers.NewService(userStore),
		Channels: channelSvc,
	}, zaptest.NewLogger(t), clock.System)

	createAndSend := func(title string, createDate, msgDate int) int64 {
		t.Helper()
		created, err := channelSvc.CreateChannel(ctx, owner.ID, domain.CreateChannelRequest{
			Title:     title,
			Megagroup: true,
			Date:      createDate,
		})
		if err != nil {
			t.Fatalf("create %s: %v", title, err)
		}
		if _, err := channelSvc.SendMessage(ctx, owner.ID, domain.SendChannelMessageRequest{
			ChannelID: created.Channel.ID,
			RandomID:  int64(msgDate),
			Message:   title + " message",
			Date:      msgDate,
		}); err != nil {
			t.Fatalf("send %s: %v", title, err)
		}
		return created.Channel.ID
	}

	oldID := createAndSend("Old inactive", 1000, 1100)
	midID := createAndSend("Middle inactive", 1000, 1200)
	newID := createAndSend("New inactive", 1000, 1300)
	leftID := createAndSend("Left inactive", 1000, 900)
	if _, err := channelSvc.LeaveChannel(ctx, owner.ID, leftID, 1400); err != nil {
		t.Fatalf("leave channel: %v", err)
	}

	got, err := r.onChannelsGetInactiveChannels(WithUserID(ctx, owner.ID))
	if err != nil {
		t.Fatalf("get inactive channels: %v", err)
	}
	if len(got.Dates) != 3 || len(got.Chats) != 3 || len(got.Users) != 0 {
		t.Fatalf("inactive chats = %+v, want three active channel chats and no users", got)
	}
	wantIDs := []int64{oldID, midID, newID}
	wantDates := []int{1100, 1200, 1300}
	for i, chat := range got.Chats {
		channel, ok := chat.(*tg.Channel)
		if !ok {
			t.Fatalf("chat %d = %T, want *tg.Channel", i, chat)
		}
		if channel.ID != wantIDs[i] || got.Dates[i] != wantDates[i] {
			t.Fatalf("inactive item %d = id %d date %d, want id %d date %d", i, channel.ID, got.Dates[i], wantIDs[i], wantDates[i])
		}
	}
}

func TestChannelsGetChannelRecommendationsRPC(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, err := userStore.Create(ctx, domain.User{AccessHash: 51, Phone: "15550001051", FirstName: "Owner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	other, err := userStore.Create(ctx, domain.User{AccessHash: 52, Phone: "15550001052", FirstName: "Other"})
	if err != nil {
		t.Fatalf("create other: %v", err)
	}
	channelStore := memory.NewChannelStore()
	channelSvc := appchannels.NewService(channelStore)
	r := New(Config{}, Deps{
		Users:    appusers.NewService(userStore),
		Channels: channelSvc,
	}, zaptest.NewLogger(t), clock.System)

	createPublicBroadcast := func(creator domain.User, title, username string, date int) domain.Channel {
		t.Helper()
		created, err := channelSvc.CreateChannel(ctx, creator.ID, domain.CreateChannelRequest{
			Title:     title,
			Broadcast: true,
			Date:      date,
		})
		if err != nil {
			t.Fatalf("create %s: %v", title, err)
		}
		channel, err := channelSvc.UpdateUsername(ctx, creator.ID, domain.UpdateChannelUsernameRequest{
			ChannelID: created.Channel.ID,
			Username:  username,
		})
		if err != nil {
			t.Fatalf("set username for %s: %v", title, err)
		}
		return channel
	}

	source := createPublicBroadcast(owner, "Source Recommendations", "source_recs", 1000)
	for i := 0; i < 12; i++ {
		createPublicBroadcast(owner, "Candidate "+strconv.Itoa(i), "rec"+strconv.Itoa(i)+"public", 2000+i)
	}
	groupCreated, err := channelSvc.CreateChannel(ctx, owner.ID, domain.CreateChannelRequest{
		Title:     "Public Group",
		Megagroup: true,
		Date:      3000,
	})
	if err != nil {
		t.Fatalf("create public group: %v", err)
	}
	group, err := channelSvc.UpdateUsername(ctx, owner.ID, domain.UpdateChannelUsernameRequest{
		ChannelID: groupCreated.Channel.ID,
		Username:  "group_recs",
	})
	if err != nil {
		t.Fatalf("set group username: %v", err)
	}

	recommendationsReq := func(channel domain.Channel) *tg.ChannelsGetChannelRecommendationsRequest {
		req := &tg.ChannelsGetChannelRecommendationsRequest{}
		req.SetChannel(&tg.InputChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash})
		return req
	}

	got, err := r.onChannelsGetChannelRecommendations(WithUserID(ctx, owner.ID), recommendationsReq(source))
	if err != nil {
		t.Fatalf("get recommendations by source: %v", err)
	}
	slice, ok := got.(*tg.MessagesChatsSlice)
	if !ok {
		t.Fatalf("recommendations = %T %+v, want messages.chatsSlice", got, got)
	}
	if slice.Count != 12 || len(slice.Chats) != domain.DefaultChannelRecommendationsLimit {
		t.Fatalf("recommendations count=%d len=%d, want count 12 len %d", slice.Count, len(slice.Chats), domain.DefaultChannelRecommendationsLimit)
	}
	for _, chat := range slice.Chats {
		channel, ok := chat.(*tg.Channel)
		if !ok {
			t.Fatalf("recommendation chat = %T, want channel", chat)
		}
		if channel.ID == source.ID || !channel.Broadcast || channel.Megagroup || channel.Username == "" {
			t.Fatalf("recommendation channel = %+v, want public broadcast excluding source", channel)
		}
	}

	if _, err := r.onChannelsGetChannelRecommendations(WithUserID(ctx, owner.ID), recommendationsReq(group)); err == nil || !strings.Contains(err.Error(), "CHANNEL_INVALID") {
		t.Fatalf("megagroup recommendations err = %v, want CHANNEL_INVALID", err)
	}

	globalA := createPublicBroadcast(other, "Global A", "global_recs_a", 5000)
	globalB := createPublicBroadcast(other, "Global B", "global_recs_b", 5100)
	global, err := r.onChannelsGetChannelRecommendations(WithUserID(ctx, owner.ID), &tg.ChannelsGetChannelRecommendationsRequest{})
	if err != nil {
		t.Fatalf("get global recommendations: %v", err)
	}
	box, ok := global.(*tg.MessagesChats)
	if !ok {
		t.Fatalf("global recommendations = %T %+v, want messages.chats", global, global)
	}
	if len(box.Chats) != 2 {
		t.Fatalf("global recommendations len=%d chats=%+v, want two channels", len(box.Chats), box.Chats)
	}
	wantIDs := []int64{globalB.ID, globalA.ID}
	for i, chat := range box.Chats {
		channel, ok := chat.(*tg.Channel)
		if !ok {
			t.Fatalf("global chat %d = %T, want channel", i, chat)
		}
		if channel.ID != wantIDs[i] {
			t.Fatalf("global chat %d id=%d, want %d", i, channel.ID, wantIDs[i])
		}
	}
}

func TestChannelsDeleteChannelReturnsForbiddenChatAndHidesDialogRPC(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, err := userStore.Create(ctx, domain.User{AccessHash: 31, Phone: "15550001031", FirstName: "Owner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	friend, err := userStore.Create(ctx, domain.User{AccessHash: 32, Phone: "15550001032", FirstName: "Friend"})
	if err != nil {
		t.Fatalf("create friend: %v", err)
	}
	channelStore := memory.NewChannelStore()
	sessions := &captureSessions{}
	r := New(Config{}, Deps{
		Users:    appusers.NewService(userStore),
		Channels: appchannels.NewService(channelStore),
		Dialogs:  appdialogs.NewService(memory.NewDialogStore(), channelStore),
		Sessions: sessions,
	}, zaptest.NewLogger(t), clock.System)

	invited, err := r.onMessagesCreateChat(WithUserID(ctx, owner.ID), &tg.MessagesCreateChatRequest{
		Users: []tg.InputUserClass{&tg.InputUser{UserID: friend.ID, AccessHash: friend.AccessHash}},
		Title: "Delete Me",
	})
	if err != nil {
		t.Fatalf("create chat: %v", err)
	}
	channel := invited.Updates.(*tg.Updates).Chats[0].(*tg.Channel)
	getDialogs := func(userID int64) *tg.MessagesDialogs {
		t.Helper()
		req := &tg.MessagesGetDialogsRequest{OffsetPeer: &tg.InputPeerEmpty{}, Limit: 20}
		var b bin.Buffer
		if err := req.Encode(&b); err != nil {
			t.Fatalf("encode get dialogs: %v", err)
		}
		enc, err := r.Dispatch(WithUserID(ctx, userID), [8]byte{}, 0, &b)
		if err != nil {
			t.Fatalf("dispatch get dialogs: %v", err)
		}
		box, ok := enc.(*tg.MessagesDialogsBox)
		if !ok {
			t.Fatalf("dialogs response = %T, want box", enc)
		}
		dialogs, ok := box.Dialogs.(*tg.MessagesDialogs)
		if !ok {
			t.Fatalf("dialogs = %T %+v, want messages.dialogs", box.Dialogs, box.Dialogs)
		}
		return dialogs
	}
	if got := getDialogs(owner.ID); len(got.Dialogs) != 1 || len(got.Chats) != 1 {
		t.Fatalf("dialogs before delete = %+v, want one channel dialog", got)
	}

	deleted, err := r.onChannelsDeleteChannel(WithUserID(ctx, owner.ID), &tg.InputChannel{
		ChannelID:  channel.ID,
		AccessHash: channel.AccessHash,
	})
	if err != nil {
		t.Fatalf("delete channel: %v", err)
	}
	updates, ok := deleted.(*tg.Updates)
	if !ok || len(updates.Updates) != 1 || len(updates.Chats) != 1 {
		t.Fatalf("delete response = %T %+v, want updateChannel + channelForbidden", deleted, deleted)
	}
	if update, ok := updates.Updates[0].(*tg.UpdateChannel); !ok || update.ChannelID != channel.ID {
		t.Fatalf("delete update = %#v, want updateChannel %d", updates.Updates[0], channel.ID)
	}
	forbidden, ok := updates.Chats[0].(*tg.ChannelForbidden)
	if !ok || forbidden.ID != channel.ID || forbidden.AccessHash != channel.AccessHash || forbidden.Title != channel.Title || !forbidden.Megagroup {
		t.Fatalf("delete chat = %#v, want channelForbidden tombstone", updates.Chats[0])
	}

	pushed := sessions.snapshot()
	if pushed.messageType != proto.MessageFromServer || pushed.userID == 0 {
		t.Fatalf("push snapshot = %+v, want server update to a channel member", pushed)
	}
	pushedUpdates, ok := pushed.message.(*tg.Updates)
	if !ok || len(pushedUpdates.Chats) != 1 {
		t.Fatalf("pushed update = %T %+v, want channelForbidden chat", pushed.message, pushed.message)
	}
	if pushedForbidden, ok := pushedUpdates.Chats[0].(*tg.ChannelForbidden); !ok || pushedForbidden.ID != channel.ID {
		t.Fatalf("pushed chat = %#v, want channelForbidden %d", pushedUpdates.Chats[0], channel.ID)
	}
	if got := getDialogs(owner.ID); len(got.Dialogs) != 0 || len(got.Chats) != 0 {
		t.Fatalf("owner dialogs after delete = %+v, want hidden deleted channel", got)
	}
	if got := getDialogs(friend.ID); len(got.Dialogs) != 0 || len(got.Chats) != 0 {
		t.Fatalf("friend dialogs after delete = %+v, want hidden deleted channel", got)
	}
}

func TestChannelRealtimeRecipientsPreferOnlineMembers(t *testing.T) {
	ctx := context.Background()
	channelStore := memory.NewChannelStore()
	channelService := appchannels.NewService(channelStore)
	created, err := channelService.CreateMegagroupFromCreateChat(ctx, 1001, domain.CreateChannelRequest{
		Title:         "Fanout",
		MemberUserIDs: []int64{1002, 1999},
		Date:          10,
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	sessions := &captureSessions{
		onlineUserIDs:  []int64{1999, 3000},
		channelViewers: map[int64][]int64{created.Channel.ID: {1999}},
		channelMembers: map[int64][]int64{created.Channel.ID: {1999, 3000}},
	}
	r := New(Config{}, Deps{
		Channels: channelService,
		Sessions: sessions,
	}, zaptest.NewLogger(t), clock.System)
	contains := func(items []int64, want int64) bool {
		for _, item := range items {
			if item == want {
				return true
			}
		}
		return false
	}

	got := r.channelFanoutRecipients(ctx, channelFanoutMembers, created.Channel.ID, []int64{1002})
	if !contains(got, 1999) {
		t.Fatalf("recipients = %v, want online active member 1999", got)
	}
	if contains(got, 3000) {
		t.Fatalf("recipients = %v, non-member online user leaked", got)
	}
	if !contains(got, 1002) {
		t.Fatalf("recipients = %v, want explicit fallback recipient 1002", got)
	}
	onlines, err := r.onMessagesGetOnlines(WithUserID(ctx, 1001), &tg.InputPeerChannel{
		ChannelID:  created.Channel.ID,
		AccessHash: created.Channel.AccessHash,
	})
	if err != nil {
		t.Fatalf("messages.getOnlines: %v", err)
	}
	if onlines.Onlines != 2 {
		t.Fatalf("messages.getOnlines = %d, want caller plus online active member", onlines.Onlines)
	}
}

func TestChannelInputAccessHashIsValidatedRPC(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, err := userStore.Create(ctx, domain.User{AccessHash: 11, Phone: "15550001101", FirstName: "Owner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	friend, err := userStore.Create(ctx, domain.User{AccessHash: 22, Phone: "15550001102", FirstName: "Friend"})
	if err != nil {
		t.Fatalf("create friend: %v", err)
	}
	channelStore := memory.NewChannelStore()
	r := New(Config{}, Deps{
		Users:    appusers.NewService(userStore),
		Channels: appchannels.NewService(channelStore),
		Dialogs:  appdialogs.NewService(memory.NewDialogStore(), channelStore),
	}, zaptest.NewLogger(t), clock.System)

	invited, err := r.onMessagesCreateChat(WithUserID(ctx, owner.ID), &tg.MessagesCreateChatRequest{
		Users: []tg.InputUserClass{&tg.InputUser{UserID: friend.ID, AccessHash: friend.AccessHash}},
		Title: "Hash Group",
	})
	if err != nil {
		t.Fatalf("create chat: %v", err)
	}
	channel := invited.Updates.(*tg.Updates).Chats[0].(*tg.Channel)
	badHash := channel.AccessHash + 1
	if badHash == 0 {
		badHash = 1
	}

	if _, err := r.onChannelsGetFullChannel(WithUserID(ctx, owner.ID), &tg.InputChannel{ChannelID: channel.ID, AccessHash: badHash}); err == nil || !strings.Contains(err.Error(), "CHANNEL_PRIVATE") {
		t.Fatalf("get full bad access_hash err = %v, want CHANNEL_PRIVATE", err)
	}
	if _, err := r.onMessagesSendMessage(WithUserID(ctx, owner.ID), &tg.MessagesSendMessageRequest{
		Peer:     &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: badHash},
		Message:  "bad hash",
		RandomID: 991,
	}); err == nil || !strings.Contains(err.Error(), "CHANNEL_PRIVATE") {
		t.Fatalf("send bad access_hash err = %v, want CHANNEL_PRIVATE", err)
	}
	if _, err := r.onMessagesGetPeerSettings(WithUserID(ctx, owner.ID), &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: badHash}); err == nil || !strings.Contains(err.Error(), "CHANNEL_PRIVATE") {
		t.Fatalf("peer settings bad access_hash err = %v, want CHANNEL_PRIVATE", err)
	}
	if _, err := r.onMessagesToggleDialogPin(WithUserID(ctx, owner.ID), &tg.MessagesToggleDialogPinRequest{
		Peer: &tg.InputDialogPeer{Peer: &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: badHash}},
	}); err == nil || !strings.Contains(err.Error(), "CHANNEL_PRIVATE") {
		t.Fatalf("toggle pin bad access_hash err = %v, want CHANNEL_PRIVATE", err)
	}
	if _, err := r.onFoldersEditPeerFolders(WithUserID(ctx, owner.ID), []tg.InputFolderPeer{{
		Peer:     &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: badHash},
		FolderID: domain.DialogArchiveFolderID,
	}}); err == nil || !strings.Contains(err.Error(), "CHANNEL_PRIVATE") {
		t.Fatalf("edit peer folder bad access_hash err = %v, want CHANNEL_PRIVATE", err)
	}
	if _, err := r.onUpdatesGetChannelDifference(WithUserID(ctx, friend.ID), &tg.UpdatesGetChannelDifferenceRequest{
		Channel: &tg.InputChannel{ChannelID: channel.ID, AccessHash: badHash},
		Filter:  &tg.ChannelMessagesFilterEmpty{},
		Pts:     0,
		Limit:   10,
	}); err == nil || !strings.Contains(err.Error(), "CHANNEL_PRIVATE") {
		t.Fatalf("difference bad access_hash err = %v, want CHANNEL_PRIVATE", err)
	}
	chats, err := r.onChannelsGetChannels(WithUserID(ctx, owner.ID), []tg.InputChannelClass{&tg.InputChannel{ChannelID: channel.ID, AccessHash: badHash}})
	if err != nil {
		t.Fatalf("get channels bad access_hash: %v", err)
	}
	if got := len(chats.(*tg.MessagesChats).Chats); got != 0 {
		t.Fatalf("get channels bad access_hash chats = %d, want 0", got)
	}
	if _, err := r.dialogFilterFromRequest(WithUserID(ctx, owner.ID), owner.ID, &tg.MessagesGetDialogsRequest{
		OffsetPeer: &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: badHash},
		Limit:      20,
	}); err == nil || !strings.Contains(err.Error(), "CHANNEL_PRIVATE") {
		t.Fatalf("get dialogs offset bad access_hash err = %v, want CHANNEL_PRIVATE", err)
	}
	if _, err := r.onMessagesSaveDraft(WithUserID(ctx, owner.ID), &tg.MessagesSaveDraftRequest{
		Peer:    &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: badHash},
		Message: "draft",
	}); err == nil || !strings.Contains(err.Error(), "CHANNEL_PRIVATE") {
		t.Fatalf("save draft bad access_hash err = %v, want CHANNEL_PRIVATE", err)
	}
	filterReq := &tg.MessagesUpdateDialogFilterRequest{ID: domain.DialogCustomFolderMinID}
	filterReq.SetFilter(&tg.DialogFilter{
		ID:    domain.DialogCustomFolderMinID,
		Title: tg.TextWithEntities{Text: "Channels"},
		IncludePeers: []tg.InputPeerClass{
			&tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: badHash},
		},
	})
	if ok, err := r.onMessagesUpdateDialogFilter(WithUserID(ctx, owner.ID), filterReq); err == nil || !strings.Contains(err.Error(), "CHANNEL_PRIVATE") || ok {
		t.Fatalf("update dialog filter bad access_hash = %v, %v; want CHANNEL_PRIVATE", ok, err)
	}
	reply := &tg.InputReplyToMessage{ReplyToMsgID: 1}
	reply.SetReplyToPeerID(&tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: badHash})
	sendReq := &tg.MessagesSendMessageRequest{
		Peer:     &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		Message:  "bad reply hash",
		RandomID: 992,
	}
	sendReq.SetReplyTo(reply)
	if _, err := r.onMessagesSendMessage(WithUserID(ctx, owner.ID), sendReq); err == nil || !strings.Contains(err.Error(), "REPLY_MESSAGE_ID_INVALID") {
		t.Fatalf("send reply_to_peer bad access_hash err = %v, want REPLY_MESSAGE_ID_INVALID", err)
	}
}

func TestChannelSendHistoryAndDifferenceRPC(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, _ := userStore.Create(ctx, domain.User{AccessHash: 11, Phone: "15550002001", FirstName: "Owner"})
	friend, _ := userStore.Create(ctx, domain.User{AccessHash: 22, Phone: "15550002002", FirstName: "Friend"})
	channelStore := memory.NewChannelStore()
	sessions := &captureScopedSessions{captureSessions: &captureSessions{}}
	r := New(Config{}, Deps{
		Users:    appusers.NewService(userStore),
		Channels: appchannels.NewService(channelStore),
		Sessions: sessions,
	}, zaptest.NewLogger(t), clock.System)
	created, err := r.onMessagesCreateChat(WithUserID(ctx, owner.ID), &tg.MessagesCreateChatRequest{
		Users: []tg.InputUserClass{&tg.InputUser{UserID: friend.ID, AccessHash: friend.AccessHash}},
		Title: "RPC Group",
	})
	if err != nil {
		t.Fatalf("create chat: %v", err)
	}
	channel := created.Updates.(*tg.Updates).Chats[0].(*tg.Channel)

	var authKeyID [8]byte
	authKeyID[0] = 9
	sendCtx := WithSessionID(WithAuthKeyID(WithUserID(ctx, owner.ID), authKeyID), 77)
	sent, err := r.onMessagesSendMessage(sendCtx, &tg.MessagesSendMessageRequest{
		Peer:     &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		Message:  "hello channel",
		RandomID: 99,
	})
	if err != nil {
		t.Fatalf("send channel message: %v", err)
	}
	sendUpdates := sent.(*tg.Updates)
	if id, ok := sendUpdates.Updates[0].(*tg.UpdateMessageID); !ok || id.ID != 3 || id.RandomID != 99 {
		t.Fatalf("message id update = %#v, want id=3 random_id=99", sendUpdates.Updates[0])
	}
	newMsg, ok := sendUpdates.Updates[1].(*tg.UpdateNewChannelMessage)
	if !ok || newMsg.Pts != 3 || newMsg.PtsCount != 1 {
		t.Fatalf("new channel update = %#v, want pts=3", sendUpdates.Updates[1])
	}
	msg := newMsg.Message.(*tg.Message)
	if msg.PeerID.(*tg.PeerChannel).ChannelID != channel.ID || msg.Message != "hello channel" || !msg.Out {
		t.Fatalf("channel message = %#v, want outgoing channel text", msg)
	}
	pushed := sessions.snapshot()
	if pushed.userID != friend.ID || pushed.sessionID != 77 || pushed.messageType != proto.MessageFromServer {
		t.Fatalf("pushed channel update = user %d exclude session %d type %v, want friend/exclude/from_server", pushed.userID, pushed.sessionID, pushed.messageType)
	}
	if gotAuthKeyID := sessions.scopedAuthKeyID; gotAuthKeyID != authKeyID {
		t.Fatalf("exclude auth_key_id = %x, want %x", gotAuthKeyID, authKeyID)
	}
	pushedUpdates, ok := pushed.message.(*tg.Updates)
	if !ok || len(pushedUpdates.Updates) != 1 {
		t.Fatalf("pushed channel update = %T %+v, want one updates container without updateMessageID", pushed.message, pushed.message)
	}
	pushedNew, ok := pushedUpdates.Updates[0].(*tg.UpdateNewChannelMessage)
	if !ok {
		t.Fatalf("pushed update[0] = %T, want updateNewChannelMessage", pushedUpdates.Updates[0])
	}
	pushedMsg := pushedNew.Message.(*tg.Message)
	if pushedMsg.Out || pushedMsg.Message != "hello channel" {
		t.Fatalf("pushed message = %#v, want incoming channel text for friend", pushedMsg)
	}

	history, err := r.onChannelsGetMessages(WithUserID(ctx, friend.ID), &tg.ChannelsGetMessagesRequest{
		Channel: &tg.InputChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		ID:      []tg.InputMessageClass{&tg.InputMessageID{ID: msg.ID}},
	})
	if err != nil {
		t.Fatalf("get channel messages: %v", err)
	}
	messages := history.(*tg.MessagesMessages)
	got := messages.Messages[0].(*tg.Message)
	if got.Message != "hello channel" || got.Out {
		t.Fatalf("history message = %#v, want incoming text for friend", got)
	}

	contentAuthKeyID := [8]byte{0x44}
	contentCtx := WithSessionID(WithAuthKeyID(WithUserID(ctx, friend.ID), contentAuthKeyID), 88)
	if ok, err := r.onChannelsReadMessageContents(contentCtx, &tg.ChannelsReadMessageContentsRequest{
		Channel: &tg.InputChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		ID:      []int{msg.ID},
	}); err != nil || !ok {
		t.Fatalf("channels.readMessageContents = ok %v err %v, want true", ok, err)
	}
	contentPush := sessions.snapshot()
	if contentPush.userID != friend.ID || contentPush.sessionID != 88 || contentPush.messageType != proto.MessageFromServer {
		t.Fatalf("content-read push = user %d exclude session %d type %v, want friend/exclude/from_server", contentPush.userID, contentPush.sessionID, contentPush.messageType)
	}
	if gotAuthKeyID := sessions.scopedAuthKeyID; gotAuthKeyID != contentAuthKeyID {
		t.Fatalf("content-read exclude auth_key_id = %x, want %x", gotAuthKeyID, contentAuthKeyID)
	}
	contentUpdates, ok := contentPush.message.(*tg.Updates)
	if !ok || len(contentUpdates.Updates) != 1 {
		t.Fatalf("content-read pushed message = %T %+v, want one update", contentPush.message, contentPush.message)
	}
	contentRead, ok := contentUpdates.Updates[0].(*tg.UpdateChannelReadMessagesContents)
	if !ok || contentRead.ChannelID != channel.ID || len(contentRead.Messages) != 1 || contentRead.Messages[0] != msg.ID {
		t.Fatalf("content-read update = %#v, want channel %d msg %d", contentUpdates.Updates[0], channel.ID, msg.ID)
	}

	diff, err := r.onUpdatesGetChannelDifference(WithUserID(ctx, friend.ID), &tg.UpdatesGetChannelDifferenceRequest{
		Channel: &tg.InputChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		Filter:  &tg.ChannelMessagesFilterEmpty{},
		Pts:     newMsg.Pts - 1,
		Limit:   10,
	})
	if err != nil {
		t.Fatalf("channel difference: %v", err)
	}
	fullDiff, ok := diff.(*tg.UpdatesChannelDifference)
	if !ok || fullDiff.Pts != newMsg.Pts || len(fullDiff.NewMessages) != 1 {
		t.Fatalf("diff = %T %+v, want one new message at pts=%d", diff, diff, newMsg.Pts)
	}
	if _, err := r.onUpdatesGetChannelDifference(WithUserID(ctx, friend.ID), &tg.UpdatesGetChannelDifferenceRequest{
		Channel: &tg.InputChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		Filter:  &tg.ChannelMessagesFilterEmpty{},
		Pts:     fullDiff.Pts + 1,
		Limit:   10,
	}); err == nil || !strings.Contains(err.Error(), "PERSISTENT_TIMESTAMP_INVALID") {
		t.Fatalf("future channel pts err = %v, want PERSISTENT_TIMESTAMP_INVALID", err)
	}

	readOK, err := r.onChannelsReadHistory(WithUserID(ctx, friend.ID), &tg.ChannelsReadHistoryRequest{
		Channel: &tg.InputChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		MaxID:   msg.ID,
	})
	if err != nil || !readOK {
		t.Fatalf("channels.readHistory = %v err %v, want true", readOK, err)
	}
	readPush := sessions.snapshot()
	if readPush.userID != owner.ID || readPush.messageType != proto.MessageFromServer {
		t.Fatalf("read outbox push = user %d type %v, want owner/from_server", readPush.userID, readPush.messageType)
	}
	readPushUpdates, ok := readPush.message.(*tg.Updates)
	if !ok || len(readPushUpdates.Updates) != 1 {
		t.Fatalf("read outbox pushed message = %T %+v, want one update", readPush.message, readPush.message)
	}
	readOutbox, ok := readPushUpdates.Updates[0].(*tg.UpdateReadChannelOutbox)
	if !ok || readOutbox.ChannelID != channel.ID || readOutbox.MaxID != msg.ID {
		t.Fatalf("read outbox update = %#v, want channel %d max %d", readPushUpdates.Updates[0], channel.ID, msg.ID)
	}
	fullAfterRead, err := r.onChannelsGetFullChannel(WithUserID(ctx, owner.ID), &tg.InputChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash})
	if err != nil {
		t.Fatalf("get full channel after read: %v", err)
	}
	fullChannel := fullAfterRead.FullChat.(*tg.ChannelFull)
	if fullChannel.ReadOutboxMaxID != msg.ID {
		t.Fatalf("full channel read_outbox = %d, want %d", fullChannel.ReadOutboxMaxID, msg.ID)
	}
	readers, err := r.onMessagesGetMessageReadParticipants(WithUserID(ctx, owner.ID), &tg.MessagesGetMessageReadParticipantsRequest{
		Peer:  &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		MsgID: msg.ID,
	})
	if err != nil {
		t.Fatalf("get message read participants: %v", err)
	}
	if len(readers) != 1 || readers[0].UserID != friend.ID || readers[0].Date == 0 {
		t.Fatalf("read participants = %+v, want friend read date", readers)
	}

	editReq := &tg.MessagesEditMessageRequest{
		Peer:    &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		ID:      msg.ID,
		Message: "edited channel",
	}
	editReq.SetMessage("edited channel")
	edited, err := r.onMessagesEditMessage(WithUserID(ctx, owner.ID), editReq)
	if err != nil {
		t.Fatalf("edit channel message: %v", err)
	}
	editUpdates := edited.(*tg.Updates)
	edit, ok := editUpdates.Updates[0].(*tg.UpdateEditChannelMessage)
	if !ok || edit.Pts != newMsg.Pts+1 || edit.PtsCount != 1 {
		t.Fatalf("edit update = %#v, want updateEditChannelMessage pts=%d", editUpdates.Updates[0], newMsg.Pts+1)
	}
	if edit.Message.(*tg.Message).Message != "edited channel" {
		t.Fatalf("edited message = %#v, want edited text", edit.Message)
	}
	editData, err := r.onMessagesGetMessageEditData(WithUserID(ctx, owner.ID), &tg.MessagesGetMessageEditDataRequest{
		Peer: &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		ID:   msg.ID,
	})
	if err != nil {
		t.Fatalf("get channel edit data: %v", err)
	}
	if editData.GetCaption() {
		t.Fatalf("channel edit data caption = true, want false for text-only message")
	}

	forwardReplyTo := &tg.InputReplyToMessage{ReplyToMsgID: msg.ID}
	forwardReplyTo.SetQuoteText("channel")
	forwardReq := &tg.MessagesForwardMessagesRequest{
		FromPeer: &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		ToPeer:   &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		ID:       []int{msg.ID},
		RandomID: []int64{100},
	}
	forwardReq.SetReplyTo(forwardReplyTo)
	forwarded, err := r.onMessagesForwardMessages(WithUserID(ctx, friend.ID), forwardReq)
	if err != nil {
		t.Fatalf("forward channel message: %v", err)
	}
	forwardUpdates := forwarded.(*tg.Updates)
	if id, ok := forwardUpdates.Updates[0].(*tg.UpdateMessageID); !ok || id.ID != msg.ID+1 || id.RandomID != 100 {
		t.Fatalf("forward id update = %#v, want id=%d", forwardUpdates.Updates[0], msg.ID+1)
	}
	forwardNew, ok := forwardUpdates.Updates[1].(*tg.UpdateNewChannelMessage)
	if !ok || forwardNew.Pts != edit.Pts+1 || forwardNew.PtsCount != 1 {
		t.Fatalf("forward new update = %#v, want pts=%d", forwardUpdates.Updates[1], edit.Pts+1)
	}
	forwardMsg := forwardNew.Message.(*tg.Message)
	if forwardMsg.Message != "edited channel" || forwardMsg.FwdFrom.FromID == nil {
		t.Fatalf("forward message = %#v, want fwd header and edited body", forwardMsg)
	}
	if header, ok := forwardMsg.ReplyTo.(*tg.MessageReplyHeader); !ok || header.ReplyToMsgID != msg.ID {
		t.Fatalf("forward reply header = %#v, want reply to channel message %d", forwardMsg.ReplyTo, msg.ID)
	}

	deleted, err := r.onChannelsDeleteMessages(WithUserID(ctx, owner.ID), &tg.ChannelsDeleteMessagesRequest{
		Channel: &tg.InputChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		ID:      []int{msg.ID, forwardMsg.ID},
	})
	if err != nil {
		t.Fatalf("delete channel messages: %v", err)
	}
	if deleted.Pts != forwardNew.Pts+2 || deleted.PtsCount != 2 {
		t.Fatalf("delete affected = %+v, want pts=%d count=2", deleted, forwardNew.Pts+2)
	}

	diff, err = r.onUpdatesGetChannelDifference(WithUserID(ctx, friend.ID), &tg.UpdatesGetChannelDifferenceRequest{
		Channel: &tg.InputChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		Filter:  &tg.ChannelMessagesFilterEmpty{},
		Pts:     newMsg.Pts,
		Limit:   10,
	})
	if err != nil {
		t.Fatalf("channel difference after edit/delete: %v", err)
	}
	fullDiff, ok = diff.(*tg.UpdatesChannelDifference)
	if !ok || fullDiff.Pts != deleted.Pts || len(fullDiff.NewMessages) != 1 || len(fullDiff.OtherUpdates) != 2 {
		t.Fatalf("diff after edit/delete = %T %+v, want forward message plus edit/delete updates", diff, diff)
	}
}

func TestChannelsReadMessageContentsClearsUnreadReactionAndPushesUpdate(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, _ := userStore.Create(ctx, domain.User{AccessHash: 41, Phone: "15550002141", FirstName: "Owner"})
	friend, _ := userStore.Create(ctx, domain.User{AccessHash: 42, Phone: "15550002142", FirstName: "Friend"})
	channelStore := memory.NewChannelStore()
	sessions := &captureScopedSessions{captureSessions: &captureSessions{}}
	r := New(Config{}, Deps{
		Users:    appusers.NewService(userStore),
		Channels: appchannels.NewService(channelStore),
		Sessions: sessions,
	}, zaptest.NewLogger(t), clock.System)
	created, err := r.onMessagesCreateChat(WithUserID(ctx, owner.ID), &tg.MessagesCreateChatRequest{
		Users: []tg.InputUserClass{&tg.InputUser{UserID: friend.ID, AccessHash: friend.AccessHash}},
		Title: "Reaction Read",
	})
	if err != nil {
		t.Fatalf("create chat: %v", err)
	}
	channel := created.Updates.(*tg.Updates).Chats[0].(*tg.Channel)
	sent, err := r.onMessagesSendMessage(WithUserID(ctx, owner.ID), &tg.MessagesSendMessageRequest{
		Peer:     &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		Message:  "owner message",
		RandomID: 21041,
	})
	if err != nil {
		t.Fatalf("send channel message: %v", err)
	}
	msgID := sent.(*tg.Updates).Updates[1].(*tg.UpdateNewChannelMessage).Message.(*tg.Message).ID
	req := &tg.MessagesSendReactionRequest{
		Peer:     &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		MsgID:    msgID,
		Reaction: []tg.ReactionClass{&tg.ReactionEmoji{Emoticon: "\U0001f525"}},
	}
	req.SetReaction(req.Reaction)
	if _, err := r.onMessagesSendReaction(WithUserID(ctx, friend.ID), req); err != nil {
		t.Fatalf("friend send reaction: %v", err)
	}
	unread, err := r.onMessagesGetUnreadReactions(WithUserID(ctx, owner.ID), &tg.MessagesGetUnreadReactionsRequest{
		Peer:  &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		Limit: 10,
	})
	if err != nil {
		t.Fatalf("get unread reactions: %v", err)
	}
	unreadMessages, _, _ := searchMessagesPayload(t, unread)
	if len(unreadMessages) != 1 {
		t.Fatalf("unread reactions = %+v, want one message", unread)
	}

	contentAuthKeyID := [8]byte{0x66}
	contentCtx := WithSessionID(WithAuthKeyID(WithUserID(ctx, owner.ID), contentAuthKeyID), 99)
	if ok, err := r.onChannelsReadMessageContents(contentCtx, &tg.ChannelsReadMessageContentsRequest{
		Channel: &tg.InputChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		ID:      []int{msgID},
	}); err != nil || !ok {
		t.Fatalf("channels.readMessageContents = ok %v err %v, want true", ok, err)
	}
	pushed := sessions.snapshot()
	if pushed.userID != owner.ID || pushed.sessionID != 99 || pushed.messageType != proto.MessageFromServer {
		t.Fatalf("reaction read push = user %d session %d type %v, want owner/exclude/from_server", pushed.userID, pushed.sessionID, pushed.messageType)
	}
	if gotAuthKeyID := sessions.scopedAuthKeyID; gotAuthKeyID != contentAuthKeyID {
		t.Fatalf("reaction read exclude auth_key_id = %x, want %x", gotAuthKeyID, contentAuthKeyID)
	}
	pushedUpdates, ok := pushed.message.(*tg.Updates)
	if !ok || len(pushedUpdates.Updates) != 1 {
		t.Fatalf("reaction read push = %T %+v, want one updateMessageReactions", pushed.message, pushed.message)
	}
	reactionUpdate, ok := pushedUpdates.Updates[0].(*tg.UpdateMessageReactions)
	if !ok || reactionUpdate.MsgID != msgID {
		t.Fatalf("reaction read update = %#v, want updateMessageReactions for %d", pushedUpdates.Updates[0], msgID)
	}
	for _, recent := range reactionUpdate.Reactions.RecentReactions {
		if recent.Unread {
			t.Fatalf("reaction read update recent = %+v, want unread cleared", reactionUpdate.Reactions.RecentReactions)
		}
	}
	unreadAfter, err := r.onMessagesGetUnreadReactions(WithUserID(ctx, owner.ID), &tg.MessagesGetUnreadReactionsRequest{
		Peer:  &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		Limit: 10,
	})
	if err != nil {
		t.Fatalf("get unread reactions after read contents: %v", err)
	}
	unreadAfterMessages, _, _ := searchMessagesPayload(t, unreadAfter)
	if len(unreadAfterMessages) != 0 {
		t.Fatalf("unread reactions after read contents = %+v, want empty", unreadAfter)
	}
}

func TestChannelReadHistoryProducesReadChannelInboxDifference(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, _ := userStore.Create(ctx, domain.User{AccessHash: 31, Phone: "15550002131", FirstName: "Owner"})
	friend, _ := userStore.Create(ctx, domain.User{AccessHash: 32, Phone: "15550002132", FirstName: "Friend"})
	channelStore := memory.NewChannelStore()
	updates := appupdates.NewService(memory.NewUpdateStateStore(), memory.NewUpdateEventStore())
	r := New(Config{}, Deps{
		Users:    appusers.NewService(userStore),
		Channels: appchannels.NewService(channelStore),
		Updates:  updates,
		Sessions: &captureSessions{},
	}, zaptest.NewLogger(t), clock.System)
	created, err := r.onMessagesCreateChat(WithUserID(ctx, owner.ID), &tg.MessagesCreateChatRequest{
		Users: []tg.InputUserClass{&tg.InputUser{UserID: friend.ID, AccessHash: friend.AccessHash}},
		Title: "Read Channel Inbox",
	})
	if err != nil {
		t.Fatalf("create chat: %v", err)
	}
	channel := created.Updates.(*tg.Updates).Chats[0].(*tg.Channel)
	sent, err := r.onMessagesSendMessage(WithUserID(ctx, owner.ID), &tg.MessagesSendMessageRequest{
		Peer:     &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		Message:  "read me",
		RandomID: 301,
	})
	if err != nil {
		t.Fatalf("send channel message: %v", err)
	}
	msg := sent.(*tg.Updates).Updates[1].(*tg.UpdateNewChannelMessage).Message.(*tg.Message)
	readOK, err := r.onChannelsReadHistory(WithUserID(ctx, friend.ID), &tg.ChannelsReadHistoryRequest{
		Channel: &tg.InputChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		MaxID:   msg.ID,
	})
	if err != nil {
		t.Fatalf("channels.readHistory: %v", err)
	}
	if !readOK {
		t.Fatalf("channels.readHistory = false, want true")
	}
	diff, err := r.onUpdatesGetDifference(WithUserID(ctx, friend.ID), &tg.UpdatesGetDifferenceRequest{Pts: 0})
	if err != nil {
		t.Fatalf("updates.getDifference: %v", err)
	}
	full, ok := diff.(*tg.UpdatesDifference)
	if !ok || len(full.OtherUpdates) != 1 {
		t.Fatalf("difference = %T %+v, want one read channel inbox update", diff, diff)
	}
	read, ok := full.OtherUpdates[0].(*tg.UpdateReadChannelInbox)
	if !ok || read.ChannelID != channel.ID || read.MaxID != msg.ID || read.StillUnreadCount != 0 {
		t.Fatalf("difference update = %#v, want updateReadChannelInbox channel %d max %d", full.OtherUpdates[0], channel.ID, msg.ID)
	}
	if len(full.Chats) != 1 {
		t.Fatalf("difference chats = %d, want channel context", len(full.Chats))
	}
}

func TestChannelGetMessagesReturnsSparseIDs(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, _ := userStore.Create(ctx, domain.User{AccessHash: 11, Phone: "15550002101", FirstName: "Owner"})
	friend, _ := userStore.Create(ctx, domain.User{AccessHash: 22, Phone: "15550002102", FirstName: "Friend"})
	channelStore := memory.NewChannelStore()
	r := New(Config{}, Deps{
		Users:    appusers.NewService(userStore),
		Channels: appchannels.NewService(channelStore),
		Sessions: &captureSessions{},
	}, zaptest.NewLogger(t), clock.System)
	created, err := r.onMessagesCreateChat(WithUserID(ctx, owner.ID), &tg.MessagesCreateChatRequest{
		Users: []tg.InputUserClass{&tg.InputUser{UserID: friend.ID, AccessHash: friend.AccessHash}},
		Title: "Sparse RPC Group",
	})
	if err != nil {
		t.Fatalf("create chat: %v", err)
	}
	channel := created.Updates.(*tg.Updates).Chats[0].(*tg.Channel)
	var firstID, lastID int
	for i := 0; i < 5; i++ {
		updates, err := r.onMessagesSendMessage(WithUserID(ctx, owner.ID), &tg.MessagesSendMessageRequest{
			Peer:     &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
			Message:  "sparse-" + strconv.Itoa(i),
			RandomID: int64(100 + i),
		})
		if err != nil {
			t.Fatalf("send channel message %d: %v", i, err)
		}
		msg := updates.(*tg.Updates).Updates[1].(*tg.UpdateNewChannelMessage).Message.(*tg.Message)
		if i == 0 {
			firstID = msg.ID
		}
		lastID = msg.ID
	}

	got, err := r.onChannelsGetMessages(WithUserID(ctx, friend.ID), &tg.ChannelsGetMessagesRequest{
		Channel: &tg.InputChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		ID: []tg.InputMessageClass{
			&tg.InputMessageID{ID: firstID},
			&tg.InputMessageID{ID: lastID},
		},
	})
	if err != nil {
		t.Fatalf("get sparse channel messages: %v", err)
	}
	messages := got.(*tg.MessagesMessages).Messages
	if len(messages) != 2 {
		t.Fatalf("messages len = %d, want 2", len(messages))
	}
	first := messages[0].(*tg.Message)
	last := messages[1].(*tg.Message)
	if first.ID != firstID || first.Message != "sparse-0" || last.ID != lastID || last.Message != "sparse-4" {
		t.Fatalf("sparse messages = %#v %#v, want first and last exact ids", first, last)
	}
}

func TestChannelsDeleteHistoryLocalClearEmitsAvailableMessagesUpdate(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, _ := userStore.Create(ctx, domain.User{AccessHash: 41, Phone: "15550002141", FirstName: "Owner"})
	friend, _ := userStore.Create(ctx, domain.User{AccessHash: 42, Phone: "15550002142", FirstName: "Friend"})
	channelStore := memory.NewChannelStore()
	updateSvc := appupdates.NewService(memory.NewUpdateStateStore(), memory.NewUpdateEventStore())
	sessions := &captureSessions{}
	r := New(Config{}, Deps{
		Users:    appusers.NewService(userStore),
		Channels: appchannels.NewService(channelStore),
		Updates:  updateSvc,
		Sessions: sessions,
	}, zaptest.NewLogger(t), clock.System)
	created, err := r.onMessagesCreateChat(WithUserID(ctx, owner.ID), &tg.MessagesCreateChatRequest{
		Users: []tg.InputUserClass{&tg.InputUser{UserID: friend.ID, AccessHash: friend.AccessHash}},
		Title: "Clear Channel",
	})
	if err != nil {
		t.Fatalf("create chat: %v", err)
	}
	channel := created.Updates.(*tg.Updates).Chats[0].(*tg.Channel)
	sent, err := r.onMessagesSendMessage(WithUserID(ctx, owner.ID), &tg.MessagesSendMessageRequest{
		Peer:     &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		Message:  "clear me",
		RandomID: 401,
	})
	if err != nil {
		t.Fatalf("send channel message: %v", err)
	}
	msg := sent.(*tg.Updates).Updates[1].(*tg.UpdateNewChannelMessage).Message.(*tg.Message)
	cleared, err := r.onChannelsDeleteHistory(WithUserID(ctx, owner.ID), &tg.ChannelsDeleteHistoryRequest{
		Channel: &tg.InputChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		MaxID:   msg.ID,
	})
	if err != nil {
		t.Fatalf("delete channel history local: %v", err)
	}
	updates, ok := cleared.(*tg.Updates)
	if !ok || len(updates.Updates) != 1 {
		t.Fatalf("clear response = %T %+v, want one update", cleared, cleared)
	}
	available, ok := updates.Updates[0].(*tg.UpdateChannelAvailableMessages)
	if !ok || available.ChannelID != channel.ID || available.AvailableMinID != msg.ID {
		t.Fatalf("clear update = %#v, want updateChannelAvailableMessages channel=%d min=%d", updates.Updates[0], channel.ID, msg.ID)
	}
	pushed := sessions.snapshot()
	if pushed.userID != owner.ID {
		t.Fatalf("pushed user = %d, want owner %d", pushed.userID, owner.ID)
	}
	pushedUpdates, ok := pushed.message.(*tg.Updates)
	if !ok || len(pushedUpdates.Updates) != 1 {
		t.Fatalf("pushed clear update = %T %+v, want one update", pushed.message, pushed.message)
	}
	if _, ok := pushedUpdates.Updates[0].(*tg.UpdateChannelAvailableMessages); !ok {
		t.Fatalf("pushed update[0] = %T, want updateChannelAvailableMessages", pushedUpdates.Updates[0])
	}
	diff, err := r.onUpdatesGetDifference(WithUserID(ctx, owner.ID), &tg.UpdatesGetDifferenceRequest{Pts: 0})
	if err != nil {
		t.Fatalf("get difference: %v", err)
	}
	full, ok := diff.(*tg.UpdatesDifference)
	if !ok || len(full.OtherUpdates) != 1 {
		t.Fatalf("difference = %T %+v, want one other update", diff, diff)
	}
	if diffUpdate, ok := full.OtherUpdates[0].(*tg.UpdateChannelAvailableMessages); !ok || diffUpdate.ChannelID != channel.ID || diffUpdate.AvailableMinID != msg.ID {
		t.Fatalf("difference update = %#v, want updateChannelAvailableMessages", full.OtherUpdates[0])
	}

	stale, err := r.onChannelsDeleteHistory(WithUserID(ctx, owner.ID), &tg.ChannelsDeleteHistoryRequest{
		Channel: &tg.InputChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		MaxID:   msg.ID - 1,
	})
	if err != nil {
		t.Fatalf("stale delete channel history local: %v", err)
	}
	staleUpdates, ok := stale.(*tg.Updates)
	if !ok || len(staleUpdates.Updates) != 1 {
		t.Fatalf("stale clear response = %T %+v, want one update", stale, stale)
	}
	staleAvailable, ok := staleUpdates.Updates[0].(*tg.UpdateChannelAvailableMessages)
	if !ok || staleAvailable.ChannelID != channel.ID || staleAvailable.AvailableMinID != msg.ID {
		t.Fatalf("stale clear update = %#v, want monotonic updateChannelAvailableMessages channel=%d min=%d", staleUpdates.Updates[0], channel.ID, msg.ID)
	}
}

func TestChannelDeleteRejectsInvalidMessageIDsRPC(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, _ := userStore.Create(ctx, domain.User{AccessHash: 43, Phone: "15550002143", FirstName: "Owner"})
	friend, _ := userStore.Create(ctx, domain.User{AccessHash: 44, Phone: "15550002144", FirstName: "Friend"})
	channelStore := memory.NewChannelStore()
	r := New(Config{}, Deps{
		Users:    appusers.NewService(userStore),
		Channels: appchannels.NewService(channelStore),
	}, zaptest.NewLogger(t), clock.System)
	created, err := r.onMessagesCreateChat(WithUserID(ctx, owner.ID), &tg.MessagesCreateChatRequest{
		Users: []tg.InputUserClass{&tg.InputUser{UserID: friend.ID, AccessHash: friend.AccessHash}},
		Title: "Invalid Delete IDs",
	})
	if err != nil {
		t.Fatalf("create chat: %v", err)
	}
	channel := created.Updates.(*tg.Updates).Chats[0].(*tg.Channel)
	input := &tg.InputChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash}
	peer := &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash}

	for _, maxID := range []int{-1, domain.MaxMessageBoxID + 1} {
		if _, err := r.onChannelsDeleteHistory(WithUserID(ctx, owner.ID), &tg.ChannelsDeleteHistoryRequest{Channel: input, MaxID: maxID}); err == nil || !strings.Contains(err.Error(), "MESSAGE_ID_INVALID") {
			t.Fatalf("channels.deleteHistory max_id=%d err = %v, want MESSAGE_ID_INVALID", maxID, err)
		}
		if _, err := r.onMessagesDeleteHistory(WithUserID(ctx, owner.ID), &tg.MessagesDeleteHistoryRequest{Peer: peer, MaxID: maxID}); err == nil || !strings.Contains(err.Error(), "MESSAGE_ID_INVALID") {
			t.Fatalf("messages.deleteHistory channel max_id=%d err = %v, want MESSAGE_ID_INVALID", maxID, err)
		}
	}
	if _, err := r.onChannelsDeleteMessages(WithUserID(ctx, owner.ID), &tg.ChannelsDeleteMessagesRequest{Channel: input, ID: []int{domain.MaxMessageBoxID + 1}}); err == nil || !strings.Contains(err.Error(), "MESSAGE_ID_INVALID") {
		t.Fatalf("channels.deleteMessages huge id err = %v, want MESSAGE_ID_INVALID", err)
	}
	tooMany := make([]int, domain.MaxDeleteMessageIDs+1)
	for i := range tooMany {
		tooMany[i] = i + 1
	}
	if _, err := r.onChannelsDeleteMessages(WithUserID(ctx, owner.ID), &tg.ChannelsDeleteMessagesRequest{Channel: input, ID: tooMany}); err == nil || !strings.Contains(err.Error(), "LIMIT_INVALID") {
		t.Fatalf("channels.deleteMessages too many ids err = %v, want LIMIT_INVALID", err)
	}
}

func TestMessagesDeleteHistoryChannelReturnsOffsetForBoundedPage(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, _ := userStore.Create(ctx, domain.User{AccessHash: 45, Phone: "15550002145", FirstName: "Owner"})
	friend, _ := userStore.Create(ctx, domain.User{AccessHash: 46, Phone: "15550002146", FirstName: "Friend"})
	channelStore := memory.NewChannelStore()
	created, err := channelStore.CreateChannel(ctx, domain.CreateChannelRequest{
		CreatorUserID: owner.ID,
		Title:         "Delete History Offset",
		Megagroup:     true,
		MemberUserIDs: []int64{friend.ID},
		Date:          1_700_002_145,
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	total := domain.MaxDeleteHistoryBatch + 2
	for i := 0; i < total; i++ {
		if _, err := channelStore.SendChannelMessage(ctx, domain.SendChannelMessageRequest{
			UserID:    owner.ID,
			ChannelID: created.Channel.ID,
			RandomID:  int64(210_000 + i),
			Message:   "bulk delete",
			Date:      1_700_002_146 + i,
		}); err != nil {
			t.Fatalf("send channel message %d: %v", i, err)
		}
	}
	sessions := &captureSessions{}
	r := New(Config{}, Deps{
		Channels: appchannels.NewService(channelStore),
		Sessions: sessions,
	}, zaptest.NewLogger(t), clock.System)

	req := &tg.MessagesDeleteHistoryRequest{
		Peer:  &tg.InputPeerChannel{ChannelID: created.Channel.ID, AccessHash: created.Channel.AccessHash},
		MaxID: 0,
	}
	req.SetRevoke(true)
	affected, err := r.onMessagesDeleteHistory(WithUserID(ctx, owner.ID), req)
	if err != nil {
		t.Fatalf("messages.deleteHistory channel: %v", err)
	}
	if affected.Offset != 1 || affected.PtsCount != domain.MaxDeleteHistoryBatch {
		t.Fatalf("affected history = %+v, want offset=1 pts_count=%d", affected, domain.MaxDeleteHistoryBatch)
	}
	pushed := sessions.snapshot()
	updates, ok := pushed.message.(*tg.Updates)
	if !ok || len(updates.Updates) != 1 {
		t.Fatalf("pushed update = %T %+v, want one bounded delete update", pushed.message, pushed.message)
	}
	deleted, ok := updates.Updates[0].(*tg.UpdateDeleteChannelMessages)
	if !ok {
		t.Fatalf("pushed update[0] = %T, want updateDeleteChannelMessages", updates.Updates[0])
	}
	if len(deleted.Messages) != domain.MaxDeleteHistoryBatch || deleted.PtsCount != domain.MaxDeleteHistoryBatch {
		t.Fatalf("delete update len=%d pts_count=%d, want bounded %d", len(deleted.Messages), deleted.PtsCount, domain.MaxDeleteHistoryBatch)
	}
}

func TestChannelDifferenceTooLongCarriesDialogPts(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, _ := userStore.Create(ctx, domain.User{AccessHash: 61, Phone: "15550002161", FirstName: "Owner"})
	friend, _ := userStore.Create(ctx, domain.User{AccessHash: 62, Phone: "15550002162", FirstName: "Friend"})
	channelStore := memory.NewChannelStore()
	r := New(Config{}, Deps{
		Users:    appusers.NewService(userStore),
		Channels: appchannels.NewService(channelStore),
	}, zaptest.NewLogger(t), clock.System)
	created, err := r.onMessagesCreateChat(WithUserID(ctx, owner.ID), &tg.MessagesCreateChatRequest{
		Users: []tg.InputUserClass{&tg.InputUser{UserID: friend.ID, AccessHash: friend.AccessHash}},
		Title: "RPC TooLong Group",
	})
	if err != nil {
		t.Fatalf("create chat: %v", err)
	}
	channel := created.Updates.(*tg.Updates).Chats[0].(*tg.Channel)
	sourceCreated, err := r.onChannelsCreateChannel(WithUserID(ctx, owner.ID), &tg.ChannelsCreateChannelRequest{
		Title:     "RPC TooLong Source",
		About:     "forward source",
		Broadcast: true,
	})
	if err != nil {
		t.Fatalf("create source channel: %v", err)
	}
	sourceChannel := sourceCreated.(*tg.Updates).Chats[0].(*tg.Channel)
	if _, err := r.onChannelsInviteToChannel(WithUserID(ctx, owner.ID), &tg.ChannelsInviteToChannelRequest{
		Channel: &tg.InputChannel{ChannelID: sourceChannel.ID, AccessHash: sourceChannel.AccessHash},
		Users:   []tg.InputUserClass{&tg.InputUser{UserID: friend.ID, AccessHash: friend.AccessHash}},
	}); err != nil {
		t.Fatalf("invite friend to source channel: %v", err)
	}
	sourceSent, err := r.onMessagesSendMessage(WithUserID(ctx, owner.ID), &tg.MessagesSendMessageRequest{
		Peer:     &tg.InputPeerChannel{ChannelID: sourceChannel.ID, AccessHash: sourceChannel.AccessHash},
		Message:  "forward source",
		RandomID: 7000,
	})
	if err != nil {
		t.Fatalf("send source channel message: %v", err)
	}
	sourceMsgID := sourceSent.(*tg.Updates).Updates[0].(*tg.UpdateMessageID).ID
	if _, err := r.onMessagesForwardMessages(WithUserID(ctx, owner.ID), &tg.MessagesForwardMessagesRequest{
		FromPeer: &tg.InputPeerChannel{ChannelID: sourceChannel.ID, AccessHash: sourceChannel.AccessHash},
		ToPeer:   &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		ID:       []int{sourceMsgID},
		RandomID: []int64{7001},
	}); err != nil {
		t.Fatalf("forward source channel to target channel: %v", err)
	}
	for i := 0; i < 12; i++ {
		if _, err := r.onMessagesSendMessage(WithUserID(ctx, owner.ID), &tg.MessagesSendMessageRequest{
			Peer:     &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
			Message:  "too long page",
			RandomID: int64(i + 1),
		}); err != nil {
			t.Fatalf("send channel message %d: %v", i, err)
		}
	}
	diff, err := r.onUpdatesGetChannelDifference(WithUserID(ctx, friend.ID), &tg.UpdatesGetChannelDifferenceRequest{
		Channel: &tg.InputChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		Filter:  &tg.ChannelMessagesFilterEmpty{},
		Pts:     0,
		Limit:   3,
	})
	if err != nil {
		t.Fatalf("channel difference: %v", err)
	}
	tooLong, ok := diff.(*tg.UpdatesChannelDifferenceTooLong)
	if !ok {
		t.Fatalf("diff = %T %+v, want channelDifferenceTooLong", diff, diff)
	}
	dialog, ok := tooLong.Dialog.(*tg.Dialog)
	if !ok {
		t.Fatalf("tooLong dialog = %T, want dialog", tooLong.Dialog)
	}
	pts, ok := dialog.GetPts()
	if !ok || pts == 0 {
		t.Fatalf("tooLong dialog pts = %d ok=%v, want current channel pts", pts, ok)
	}
	if len(tooLong.Messages) == 0 || len(tooLong.Messages) > domain.MaxChannelDifferenceTooLongMessages {
		t.Fatalf("tooLong messages = %d, want bounded latest snapshot", len(tooLong.Messages))
	}
	if len(tooLong.Chats) == 0 || tooLong.Chats[0].(*tg.Channel).ID != channel.ID {
		t.Fatalf("tooLong chats = %+v, want source channel context", tooLong.Chats)
	}
	hasSourceChannel := false
	for _, chat := range tooLong.Chats {
		if ch, ok := chat.(*tg.Channel); ok && ch.ID == sourceChannel.ID {
			hasSourceChannel = true
			break
		}
	}
	if !hasSourceChannel {
		t.Fatalf("tooLong chats = %+v, want forwarded source channel context %d", tooLong.Chats, sourceChannel.ID)
	}
	hasOwnerUser := false
	for _, user := range tooLong.Users {
		if u, ok := user.(*tg.User); ok && u.ID == owner.ID {
			hasOwnerUser = true
			break
		}
	}
	if !hasOwnerUser {
		t.Fatalf("tooLong users = %+v, want sender user context", tooLong.Users)
	}
}

func TestBroadcastChannelPostRPC(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, _ := userStore.Create(ctx, domain.User{AccessHash: 71, Phone: "15550002171", FirstName: "Owner"})
	member, _ := userStore.Create(ctx, domain.User{AccessHash: 72, Phone: "15550002172", FirstName: "Member"})
	channelStore := memory.NewChannelStore()
	r := New(Config{}, Deps{
		Users:    appusers.NewService(userStore),
		Channels: appchannels.NewService(channelStore),
	}, zaptest.NewLogger(t), clock.System)
	created, err := r.onChannelsCreateChannel(WithUserID(ctx, owner.ID), &tg.ChannelsCreateChannelRequest{
		Title:     "RPC Broadcast",
		About:     "news",
		Broadcast: true,
	})
	if err != nil {
		t.Fatalf("create broadcast: %v", err)
	}
	channel := created.(*tg.Updates).Chats[0].(*tg.Channel)
	if !channel.Broadcast || channel.Megagroup {
		t.Fatalf("channel flags = broadcast:%v megagroup:%v, want broadcast only", channel.Broadcast, channel.Megagroup)
	}
	if _, err := r.onChannelsInviteToChannel(WithUserID(ctx, owner.ID), &tg.ChannelsInviteToChannelRequest{
		Channel: &tg.InputChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		Users:   []tg.InputUserClass{&tg.InputUser{UserID: member.ID, AccessHash: member.AccessHash}},
	}); err != nil {
		t.Fatalf("invite member to broadcast: %v", err)
	}
	posted, err := r.onMessagesSendMessage(WithUserID(ctx, owner.ID), &tg.MessagesSendMessageRequest{
		Peer:     &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		Message:  "broadcast post",
		RandomID: 7071,
	})
	if err != nil {
		t.Fatalf("owner send broadcast post: %v", err)
	}
	postUpdate := posted.(*tg.Updates).Updates[1].(*tg.UpdateNewChannelMessage)
	postMsg := postUpdate.Message.(*tg.Message)
	if !postMsg.Post || postMsg.FromID != nil || postMsg.Message != "broadcast post" {
		t.Fatalf("broadcast post message = %#v, want post without from_id", postMsg)
	}
	if _, err := r.onMessagesSendMessage(WithUserID(ctx, member.ID), &tg.MessagesSendMessageRequest{
		Peer:     &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		Message:  "member post",
		RandomID: 7072,
	}); err == nil || !strings.Contains(err.Error(), "CHAT_WRITE_FORBIDDEN") {
		t.Fatalf("member broadcast send err = %v, want CHAT_WRITE_FORBIDDEN", err)
	}
	if _, err := r.onChannelsEditAdmin(WithUserID(ctx, owner.ID), &tg.ChannelsEditAdminRequest{
		Channel:     &tg.InputChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		UserID:      &tg.InputUser{UserID: member.ID, AccessHash: member.AccessHash},
		AdminRights: tg.ChatAdminRights{ChangeInfo: true},
	}); err != nil {
		t.Fatalf("promote member without post_messages: %v", err)
	}
	if _, err := r.onMessagesSendMessage(WithUserID(ctx, member.ID), &tg.MessagesSendMessageRequest{
		Peer:     &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		Message:  "admin without post right",
		RandomID: 7073,
	}); err == nil || !strings.Contains(err.Error(), "CHAT_WRITE_FORBIDDEN") {
		t.Fatalf("admin without post right send err = %v, want CHAT_WRITE_FORBIDDEN", err)
	}
	if _, err := r.onChannelsEditAdmin(WithUserID(ctx, owner.ID), &tg.ChannelsEditAdminRequest{
		Channel:     &tg.InputChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		UserID:      &tg.InputUser{UserID: member.ID, AccessHash: member.AccessHash},
		AdminRights: tg.ChatAdminRights{PostMessages: true},
	}); err != nil {
		t.Fatalf("grant member post_messages: %v", err)
	}
	adminPosted, err := r.onMessagesSendMessage(WithUserID(ctx, member.ID), &tg.MessagesSendMessageRequest{
		Peer:     &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		Message:  "admin post",
		RandomID: 7074,
	})
	if err != nil {
		t.Fatalf("admin send broadcast post: %v", err)
	}
	adminPostMsg := adminPosted.(*tg.Updates).Updates[1].(*tg.UpdateNewChannelMessage).Message.(*tg.Message)
	if !adminPostMsg.Post || adminPostMsg.FromID != nil || adminPostMsg.Message != "admin post" {
		t.Fatalf("admin broadcast post message = %#v, want post without from_id", adminPostMsg)
	}
	diff, err := r.onUpdatesGetChannelDifference(WithUserID(ctx, member.ID), &tg.UpdatesGetChannelDifferenceRequest{
		Channel: &tg.InputChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		Filter:  &tg.ChannelMessagesFilterEmpty{},
		Pts:     1,
		Limit:   10,
	})
	if err != nil {
		t.Fatalf("member channel difference: %v", err)
	}
	fullDiff := diff.(*tg.UpdatesChannelDifference)
	foundPost := false
	for _, msg := range fullDiff.NewMessages {
		if item, ok := msg.(*tg.Message); ok && item.Message == "broadcast post" {
			foundPost = item.Post && item.FromID == nil
		}
	}
	if !foundPost {
		t.Fatalf("diff new messages = %+v, want broadcast post without from_id", fullDiff.NewMessages)
	}
}

func TestChannelsCreateChannelUnsupportedOptionsReturnExplicitErrors(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, _ := userStore.Create(ctx, domain.User{AccessHash: 73, Phone: "15550002173", FirstName: "Owner"})
	r := New(Config{}, Deps{
		Users:    appusers.NewService(userStore),
		Channels: appchannels.NewService(memory.NewChannelStore()),
	}, zaptest.NewLogger(t), clock.System)

	tests := []struct {
		name string
		req  *tg.ChannelsCreateChannelRequest
		want string
	}{
		{
			name: "history import",
			req: &tg.ChannelsCreateChannelRequest{
				Title:     "Import Group",
				Megagroup: true,
				ForImport: true,
			},
			want: "CHAT_INVALID",
		},
		{
			name: "geogroup",
			req: &tg.ChannelsCreateChannelRequest{
				Title:     "Geo Group",
				Megagroup: true,
				GeoPoint:  &tg.InputGeoPoint{Lat: 1.2, Long: 3.4},
				Address:   "somewhere",
			},
			want: "ADDRESS_INVALID",
		},
		{
			name: "address without geo",
			req: &tg.ChannelsCreateChannelRequest{
				Title:     "Bad Geo Group",
				Megagroup: true,
				Address:   "somewhere",
			},
			want: "ADDRESS_INVALID",
		},
		{
			name: "negative ttl",
			req: &tg.ChannelsCreateChannelRequest{
				Title:     "Bad TTL Group",
				Megagroup: true,
				TTLPeriod: -1,
			},
			want: "TTL_PERIOD_INVALID",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := r.onChannelsCreateChannel(WithUserID(ctx, owner.ID), tc.req); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("create channel err = %v, want %s", err, tc.want)
			}
		})
	}
}

func TestLegacyChannelSettingsRPC(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, _ := userStore.Create(ctx, domain.User{AccessHash: 81, Phone: "15550002181", FirstName: "Owner"})
	friend, _ := userStore.Create(ctx, domain.User{AccessHash: 82, Phone: "15550002182", FirstName: "Friend"})
	channelStore := memory.NewChannelStore()
	r := New(Config{}, Deps{
		Users:    appusers.NewService(userStore),
		Channels: appchannels.NewService(channelStore),
	}, zaptest.NewLogger(t), clock.System)
	created, err := r.onMessagesCreateChat(WithUserID(ctx, owner.ID), &tg.MessagesCreateChatRequest{
		Users: []tg.InputUserClass{&tg.InputUser{UserID: friend.ID, AccessHash: friend.AccessHash}},
		Title: "Legacy Settings Group",
	})
	if err != nil {
		t.Fatalf("create chat: %v", err)
	}
	channel := created.Updates.(*tg.Updates).Chats[0].(*tg.Channel)
	peer := &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash}

	themeUpdates, err := r.onMessagesSetChatTheme(WithUserID(ctx, owner.ID), &tg.MessagesSetChatThemeRequest{Peer: peer})
	if err != nil {
		t.Fatalf("set chat theme channel peer: %v", err)
	}
	if len(themeUpdates.(*tg.Updates).Chats) != 1 {
		t.Fatalf("set chat theme updates = %+v, want channel context", themeUpdates)
	}
	privateTheme, err := r.onMessagesSetChatTheme(WithUserID(ctx, owner.ID), &tg.MessagesSetChatThemeRequest{
		Peer: &tg.InputPeerUser{UserID: friend.ID, AccessHash: friend.AccessHash},
	})
	if err != nil {
		t.Fatalf("set chat theme private peer: %v", err)
	}
	if len(privateTheme.(*tg.Updates).Updates) != 0 {
		t.Fatalf("private set chat theme updates = %+v, want empty compat ack", privateTheme)
	}

	reactionUpdates, err := r.onMessagesSetChatAvailableReactions(WithUserID(ctx, owner.ID), &tg.MessagesSetChatAvailableReactionsRequest{
		Peer:               peer,
		AvailableReactions: &tg.ChatReactionsSome{Reactions: []tg.ReactionClass{&tg.ReactionEmoji{Emoticon: "\U0001f44d"}}},
		ReactionsLimit:     8,
	})
	if err != nil {
		t.Fatalf("set available reactions: %v", err)
	}
	if len(reactionUpdates.(*tg.Updates).Chats) != 1 {
		t.Fatalf("set reactions updates = %+v, want channel state update", reactionUpdates)
	}
	full, err := r.onChannelsGetFullChannel(WithUserID(ctx, owner.ID), &tg.InputChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash})
	if err != nil {
		t.Fatalf("get full channel after reactions: %v", err)
	}
	fullChannel := full.FullChat.(*tg.ChannelFull)
	reactions, ok := fullChannel.GetAvailableReactions()
	if !ok {
		t.Fatalf("full channel reactions missing after set")
	}
	some, ok := reactions.(*tg.ChatReactionsSome)
	if !ok || len(some.Reactions) != 1 {
		t.Fatalf("full channel reactions = %#v, want one explicit reaction", reactions)
	}
	if fullChannel.ReactionsLimit != 8 {
		t.Fatalf("full channel reactions limit = %d, want 8", fullChannel.ReactionsLimit)
	}
	if _, err := r.onMessagesSetChatAvailableReactions(WithUserID(ctx, owner.ID), &tg.MessagesSetChatAvailableReactionsRequest{
		Peer:               peer,
		AvailableReactions: &tg.ChatReactionsSome{Reactions: make([]tg.ReactionClass, maxChatAvailableReactions+1)},
	}); err == nil {
		t.Fatalf("set too many reactions err = nil, want limit error")
	}

	noForwards, err := r.onMessagesToggleNoForwards(WithUserID(ctx, owner.ID), &tg.MessagesToggleNoForwardsRequest{
		Peer:    peer,
		Enabled: true,
	})
	if err != nil {
		t.Fatalf("toggle noforwards: %v", err)
	}
	if got := noForwards.(*tg.Updates).Chats[0].(*tg.Channel); !got.Noforwards {
		t.Fatalf("noforwards channel = %+v, want enabled", got)
	}
	sent, err := r.onMessagesSendMessage(WithUserID(ctx, owner.ID), &tg.MessagesSendMessageRequest{
		Peer:     peer,
		Message:  "protected content",
		RandomID: 8181,
	})
	if err != nil {
		t.Fatalf("send protected message: %v", err)
	}
	msg := sent.(*tg.Updates).Updates[1].(*tg.UpdateNewChannelMessage).Message.(*tg.Message)
	if !msg.Noforwards {
		t.Fatalf("protected channel message = %+v, want noforwards inherited", msg)
	}
}

func TestChannelParticipantsSearchQueryIsBounded(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, _ := userStore.Create(ctx, domain.User{AccessHash: 11, Phone: "15550002111", FirstName: "Owner"})
	friend, _ := userStore.Create(ctx, domain.User{AccessHash: 22, Phone: "15550002112", FirstName: "Friend"})
	channelStore := memory.NewChannelStore()
	r := New(Config{}, Deps{
		Users:    appusers.NewService(userStore),
		Channels: appchannels.NewService(channelStore),
	}, zaptest.NewLogger(t), clock.System)
	created, err := r.onMessagesCreateChat(WithUserID(ctx, owner.ID), &tg.MessagesCreateChatRequest{
		Users: []tg.InputUserClass{&tg.InputUser{UserID: friend.ID, AccessHash: friend.AccessHash}},
		Title: "Participants RPC Group",
	})
	if err != nil {
		t.Fatalf("create chat: %v", err)
	}
	channel := created.Updates.(*tg.Updates).Chats[0].(*tg.Channel)

	_, err = r.onChannelsGetParticipants(WithUserID(ctx, owner.ID), &tg.ChannelsGetParticipantsRequest{
		Channel: &tg.InputChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		Filter:  &tg.ChannelParticipantsSearch{Q: strings.Repeat("x", domain.MaxChannelParticipantsQueryLength+1)},
		Limit:   20,
	})
	if err == nil || !strings.Contains(err.Error(), "LIMIT_INVALID") {
		t.Fatalf("get participants long query err = %v, want LIMIT_INVALID", err)
	}
}

func TestChannelSendMessageRPCResolvesReplyHeader(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, _ := userStore.Create(ctx, domain.User{AccessHash: 31, Phone: "15550002031", FirstName: "Owner"})
	friend, _ := userStore.Create(ctx, domain.User{AccessHash: 32, Phone: "15550002032", FirstName: "Friend"})
	channelStore := memory.NewChannelStore()
	r := New(Config{}, Deps{
		Users:    appusers.NewService(userStore),
		Channels: appchannels.NewService(channelStore),
	}, zaptest.NewLogger(t), clock.System)
	created, err := r.onMessagesCreateChat(WithUserID(ctx, owner.ID), &tg.MessagesCreateChatRequest{
		Users: []tg.InputUserClass{&tg.InputUser{UserID: friend.ID, AccessHash: friend.AccessHash}},
		Title: "RPC Reply Group",
	})
	if err != nil {
		t.Fatalf("create chat: %v", err)
	}
	channel := created.Updates.(*tg.Updates).Chats[0].(*tg.Channel)
	rootUpdates, err := r.onMessagesSendMessage(WithUserID(ctx, owner.ID), &tg.MessagesSendMessageRequest{
		Peer:     &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		Message:  "root",
		RandomID: 3001,
	})
	if err != nil {
		t.Fatalf("send root: %v", err)
	}
	rootMsg := rootUpdates.(*tg.Updates).Updates[1].(*tg.UpdateNewChannelMessage).Message.(*tg.Message)
	replyTo := &tg.InputReplyToMessage{ReplyToMsgID: rootMsg.ID}
	replyTo.SetQuoteText("root")
	replyTo.SetQuoteOffset(0)
	replyReq := &tg.MessagesSendMessageRequest{
		Peer:     &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		Message:  "reply",
		RandomID: 3002,
	}
	replyReq.SetReplyTo(replyTo)
	replyUpdates, err := r.onMessagesSendMessage(WithUserID(ctx, friend.ID), replyReq)
	if err != nil {
		t.Fatalf("send reply: %v", err)
	}
	replyMsg := replyUpdates.(*tg.Updates).Updates[1].(*tg.UpdateNewChannelMessage).Message.(*tg.Message)
	header, ok := replyMsg.ReplyTo.(*tg.MessageReplyHeader)
	if !ok {
		t.Fatalf("reply header = %#v, want messageReplyHeader", replyMsg.ReplyTo)
	}
	topID, topOK := header.GetReplyToTopID()
	quoteText, quoteOK := header.GetQuoteText()
	if header.ReplyToMsgID != rootMsg.ID || !topOK || topID != rootMsg.ID || !quoteOK || quoteText != "root" {
		t.Fatalf("reply header = %#v, want msg/top %d and quote", header, rootMsg.ID)
	}
	badReq := &tg.MessagesSendMessageRequest{
		Peer:     &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		Message:  "bad",
		RandomID: 3003,
	}
	badReq.SetReplyTo(&tg.InputReplyToMessage{ReplyToMsgID: 999})
	if _, err := r.onMessagesSendMessage(WithUserID(ctx, owner.ID), badReq); err == nil || !strings.Contains(err.Error(), "REPLY_MESSAGE_ID_INVALID") {
		t.Fatalf("bad reply err = %v, want REPLY_MESSAGE_ID_INVALID", err)
	}
}

func TestMessagesSearchChannelPeerReturnsSingleCopyMessages(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, _ := userStore.Create(ctx, domain.User{AccessHash: 35, Phone: "15550002035", FirstName: "Owner"})
	friend, _ := userStore.Create(ctx, domain.User{AccessHash: 36, Phone: "15550002036", FirstName: "Friend"})
	channelStore := memory.NewChannelStore()
	r := New(Config{}, Deps{
		Users:    appusers.NewService(userStore),
		Channels: appchannels.NewService(channelStore),
	}, zaptest.NewLogger(t), clock.System)
	created, err := r.onMessagesCreateChat(WithUserID(ctx, owner.ID), &tg.MessagesCreateChatRequest{
		Users: []tg.InputUserClass{&tg.InputUser{UserID: friend.ID, AccessHash: friend.AccessHash}},
		Title: "RPC Search Group",
	})
	if err != nil {
		t.Fatalf("create chat: %v", err)
	}
	channel := created.Updates.(*tg.Updates).Chats[0].(*tg.Channel)
	for _, item := range []struct {
		userID int64
		text   string
		random int64
	}{
		{owner.ID, "needle from owner", 5001},
		{friend.ID, "not this one", 5002},
		{friend.ID, "needle from friend", 5003},
	} {
		if _, err := r.onMessagesSendMessage(WithUserID(ctx, item.userID), &tg.MessagesSendMessageRequest{
			Peer:     &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
			Message:  item.text,
			RandomID: item.random,
		}); err != nil {
			t.Fatalf("send %q: %v", item.text, err)
		}
	}

	req := &tg.MessagesSearchRequest{
		Peer:   &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		Q:      "needle",
		Filter: &tg.InputMessagesFilterEmpty{},
		Limit:  10,
	}
	var in bin.Buffer
	if err := req.Encode(&in); err != nil {
		t.Fatalf("encode search: %v", err)
	}
	enc, err := r.Dispatch(WithUserID(ctx, friend.ID), [8]byte{}, 0, &in)
	if err != nil {
		t.Fatalf("dispatch search: %v", err)
	}
	messages, chats, users := searchMessagesPayload(t, enc)
	if len(messages) != 2 || len(chats) != 1 || len(users) < 2 {
		t.Fatalf("search payload sizes = messages %d chats %d users %d, want 2/1/2+", len(messages), len(chats), len(users))
	}
	for _, msg := range messages {
		item := msg.(*tg.Message)
		if !strings.Contains(item.Message, "needle") {
			t.Fatalf("search result message = %q, want only needle hits", item.Message)
		}
	}

	fromReq := *req
	fromReq.SetFromID(&tg.InputPeerUser{UserID: friend.ID, AccessHash: friend.AccessHash})
	in.Reset()
	if err := fromReq.Encode(&in); err != nil {
		t.Fatalf("encode from search: %v", err)
	}
	enc, err = r.Dispatch(WithUserID(ctx, owner.ID), [8]byte{}, 0, &in)
	if err != nil {
		t.Fatalf("dispatch from search: %v", err)
	}
	messages, _, _ = searchMessagesPayload(t, enc)
	if len(messages) != 1 || messages[0].(*tg.Message).Message != "needle from friend" {
		t.Fatalf("from search messages = %+v, want friend needle only", messages)
	}

	mediaCountReq := &tg.MessagesSearchRequest{
		Peer:   &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		Filter: &tg.InputMessagesFilterPhotos{},
		Limit:  0,
	}
	in.Reset()
	if err := mediaCountReq.Encode(&in); err != nil {
		t.Fatalf("encode shared media count search: %v", err)
	}
	enc, err = r.Dispatch(WithUserID(ctx, friend.ID), [8]byte{}, 0, &in)
	if err != nil {
		t.Fatalf("dispatch shared media count search: %v", err)
	}
	if box, ok := enc.(*tg.MessagesMessagesBox); ok {
		enc = box.Messages
	}
	channelMessages, ok := enc.(*tg.MessagesChannelMessages)
	if !ok {
		t.Fatalf("shared media count search result = %T, want messages.channelMessages", enc)
	}
	if channelMessages.Count != 0 || len(channelMessages.Messages) != 0 {
		t.Fatalf("shared media count search = count %d messages %d, want empty without media store", channelMessages.Count, len(channelMessages.Messages))
	}
}

func searchMessagesPayload(t *testing.T, enc bin.Encoder) ([]tg.MessageClass, []tg.ChatClass, []tg.UserClass) {
	t.Helper()
	switch result := enc.(type) {
	case *tg.MessagesMessages:
		return result.Messages, result.Chats, result.Users
	case *tg.MessagesMessagesSlice:
		return result.Messages, result.Chats, result.Users
	case *tg.MessagesChannelMessages:
		return result.Messages, result.Chats, result.Users
	case *tg.MessagesMessagesBox:
		return searchMessagesPayload(t, result.Messages)
	default:
		t.Fatalf("search result type = %T, want messages/messagesSlice", enc)
		return nil, nil, nil
	}
}

func TestForwardChannelMessageToUserIncludesSourceChannelChat(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, _ := userStore.Create(ctx, domain.User{AccessHash: 33, Phone: "15550002033", FirstName: "Owner"})
	recipient, _ := userStore.Create(ctx, domain.User{AccessHash: 34, Phone: "15550002034", FirstName: "Recipient"})
	channelStore := memory.NewChannelStore()
	messageStore := memory.NewMessageStore()
	r := New(Config{}, Deps{
		Users:    appusers.NewService(userStore),
		Channels: appchannels.NewService(channelStore),
		Messages: appmessages.NewService(messageStore, nil),
	}, zaptest.NewLogger(t), clock.System)
	created, err := r.onChannelsCreateChannel(WithUserID(ctx, owner.ID), &tg.ChannelsCreateChannelRequest{
		Title:     "Source Channel",
		About:     "forward source",
		Broadcast: true,
	})
	if err != nil {
		t.Fatalf("create source channel: %v", err)
	}
	channel := created.(*tg.Updates).Chats[0].(*tg.Channel)
	sent, err := r.onMessagesSendMessage(WithUserID(ctx, owner.ID), &tg.MessagesSendMessageRequest{
		Peer:     &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		Message:  "source post",
		RandomID: 4001,
	})
	if err != nil {
		t.Fatalf("send source channel message: %v", err)
	}
	msgID := sent.(*tg.Updates).Updates[0].(*tg.UpdateMessageID).ID
	forwarded, err := r.onMessagesForwardMessages(WithUserID(ctx, owner.ID), &tg.MessagesForwardMessagesRequest{
		FromPeer: &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		ToPeer:   &tg.InputPeerUser{UserID: recipient.ID, AccessHash: recipient.AccessHash},
		ID:       []int{msgID},
		RandomID: []int64{4002},
	})
	if err != nil {
		t.Fatalf("forward channel to user: %v", err)
	}
	updates := forwarded.(*tg.Updates)
	if len(updates.Chats) != 1 || updates.Chats[0].(*tg.Channel).ID != channel.ID {
		t.Fatalf("forward updates chats = %+v, want source channel chat", updates.Chats)
	}
	newMsg := updates.Updates[1].(*tg.UpdateNewMessage).Message.(*tg.Message)
	if from, ok := newMsg.FwdFrom.FromID.(*tg.PeerChannel); !ok || from.ChannelID != channel.ID {
		t.Fatalf("forward header = %#v, want source channel peer", newMsg.FwdFrom)
	}
}

func TestChannelSendAsCurrentChannelRPC(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, _ := userStore.Create(ctx, domain.User{AccessHash: 51, Phone: "15550002111", FirstName: "Owner"})
	friend, _ := userStore.Create(ctx, domain.User{AccessHash: 52, Phone: "15550002112", FirstName: "Friend"})
	channelStore := memory.NewChannelStore()
	r := New(Config{}, Deps{
		Users:    appusers.NewService(userStore),
		Channels: appchannels.NewService(channelStore),
	}, zaptest.NewLogger(t), clock.System)
	created, err := r.onMessagesCreateChat(WithUserID(ctx, owner.ID), &tg.MessagesCreateChatRequest{
		Users: []tg.InputUserClass{&tg.InputUser{UserID: friend.ID, AccessHash: friend.AccessHash}},
		Title: "Send As Group",
	})
	if err != nil {
		t.Fatalf("create chat: %v", err)
	}
	channel := created.Updates.(*tg.Updates).Chats[0].(*tg.Channel)
	sendAs := &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash}
	sent, err := r.onMessagesSendMessage(WithUserID(ctx, owner.ID), &tg.MessagesSendMessageRequest{
		Peer:     &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		Message:  "as channel",
		RandomID: 501,
		SendAs:   sendAs,
	})
	if err != nil {
		t.Fatalf("send as current channel: %v", err)
	}
	sentUpdates := sent.(*tg.Updates)
	sentMsg := sentUpdates.Updates[1].(*tg.UpdateNewChannelMessage).Message.(*tg.Message)
	if from, ok := sentMsg.FromID.(*tg.PeerChannel); !ok || from.ChannelID != channel.ID {
		t.Fatalf("send_as message from = %#v, want channel %d", sentMsg.FromID, channel.ID)
	}

	history, err := r.onChannelsGetMessages(WithUserID(ctx, friend.ID), &tg.ChannelsGetMessagesRequest{
		Channel: &tg.InputChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		ID:      []tg.InputMessageClass{&tg.InputMessageID{ID: sentMsg.ID}},
	})
	if err != nil {
		t.Fatalf("get send_as history: %v", err)
	}
	historyMsg := history.(*tg.MessagesMessages).Messages[0].(*tg.Message)
	if from, ok := historyMsg.FromID.(*tg.PeerChannel); !ok || from.ChannelID != channel.ID {
		t.Fatalf("history send_as from = %#v, want channel %d", historyMsg.FromID, channel.ID)
	}

	forwarded, err := r.onMessagesForwardMessages(WithUserID(ctx, owner.ID), &tg.MessagesForwardMessagesRequest{
		FromPeer: &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		ToPeer:   &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		ID:       []int{sentMsg.ID},
		RandomID: []int64{502},
		SendAs:   sendAs,
	})
	if err != nil {
		t.Fatalf("forward as current channel: %v", err)
	}
	forwardMsg := forwarded.(*tg.Updates).Updates[1].(*tg.UpdateNewChannelMessage).Message.(*tg.Message)
	if from, ok := forwardMsg.FromID.(*tg.PeerChannel); !ok || from.ChannelID != channel.ID {
		t.Fatalf("forward send_as from = %#v, want channel %d", forwardMsg.FromID, channel.ID)
	}

	if _, err := r.onMessagesSendMessage(WithUserID(ctx, owner.ID), &tg.MessagesSendMessageRequest{
		Peer:     &tg.InputPeerUser{UserID: friend.ID, AccessHash: friend.AccessHash},
		Message:  "bad private send_as",
		RandomID: 503,
		SendAs:   sendAs,
	}); err == nil || !strings.Contains(err.Error(), "SEND_AS_PEER_INVALID") {
		t.Fatalf("private send_as err = %v, want SEND_AS_PEER_INVALID", err)
	}
	if _, err := r.onMessagesSendMessage(WithUserID(ctx, friend.ID), &tg.MessagesSendMessageRequest{
		Peer:     &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		Message:  "bad member send_as",
		RandomID: 504,
		SendAs:   sendAs,
	}); err == nil || !strings.Contains(err.Error(), "SEND_AS_PEER_INVALID") {
		t.Fatalf("member send_as err = %v, want SEND_AS_PEER_INVALID", err)
	}
	if _, err := r.onMessagesForwardMessages(WithUserID(ctx, owner.ID), &tg.MessagesForwardMessagesRequest{
		FromPeer: &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		ToPeer:   &tg.InputPeerUser{UserID: friend.ID, AccessHash: friend.AccessHash},
		ID:       []int{sentMsg.ID},
		RandomID: []int64{505},
		SendAs:   sendAs,
	}); err == nil || !strings.Contains(err.Error(), "SEND_AS_PEER_INVALID") {
		t.Fatalf("private forward send_as err = %v, want SEND_AS_PEER_INVALID", err)
	}
}

func TestTDesktopPassiveChannelStubs(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, _ := userStore.Create(ctx, domain.User{AccessHash: 41, Phone: "15550002101", FirstName: "Owner"})
	friend, _ := userStore.Create(ctx, domain.User{AccessHash: 42, Phone: "15550002102", FirstName: "Friend"})
	invited, _ := userStore.Create(ctx, domain.User{AccessHash: 43, Phone: "15550002103", FirstName: "Invited"})
	channelStore := memory.NewChannelStore()
	r := New(Config{}, Deps{
		Users:    appusers.NewService(userStore),
		Channels: appchannels.NewService(channelStore),
	}, zaptest.NewLogger(t), clock.System)
	created, err := r.onMessagesCreateChat(WithUserID(ctx, owner.ID), &tg.MessagesCreateChatRequest{
		Users: []tg.InputUserClass{&tg.InputUser{UserID: friend.ID, AccessHash: friend.AccessHash}},
		Title: "RPC Passive Group",
	})
	if err != nil {
		t.Fatalf("create chat: %v", err)
	}
	channel := created.Updates.(*tg.Updates).Chats[0].(*tg.Channel)

	sendAs, err := r.onChannelsGetSendAs(WithUserID(ctx, owner.ID), &tg.ChannelsGetSendAsRequest{
		Peer: &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
	})
	if err != nil {
		t.Fatalf("get send as: %v", err)
	}
	if len(sendAs.Peers) != 2 || len(sendAs.Users) != 1 || len(sendAs.Chats) != 1 {
		t.Fatalf("send as = %+v, want self + current channel peers with current channel context", sendAs)
	}
	if peer, ok := sendAs.Peers[0].Peer.(*tg.PeerUser); !ok || peer.UserID != owner.ID {
		t.Fatalf("send as peer = %#v, want owner user", sendAs.Peers[0].Peer)
	}
	if peer, ok := sendAs.Peers[1].Peer.(*tg.PeerChannel); !ok || peer.ChannelID != channel.ID {
		t.Fatalf("send as peer[1] = %#v, want current channel", sendAs.Peers[1].Peer)
	}
	friendSendAs, err := r.onChannelsGetSendAs(WithUserID(ctx, friend.ID), &tg.ChannelsGetSendAsRequest{
		Peer: &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
	})
	if err != nil {
		t.Fatalf("friend get send as: %v", err)
	}
	if len(friendSendAs.Peers) != 1 {
		t.Fatalf("friend send as peers = %+v, want only self", friendSendAs.Peers)
	}
	if ok, err := r.onMessagesSaveDefaultSendAs(WithUserID(ctx, owner.ID), &tg.MessagesSaveDefaultSendAsRequest{
		Peer:   &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		SendAs: &tg.InputPeerSelf{},
	}); err != nil || !ok {
		t.Fatalf("save default send as self = ok %v err %v, want true", ok, err)
	}
	if ok, err := r.onMessagesSaveDefaultSendAs(WithUserID(ctx, owner.ID), &tg.MessagesSaveDefaultSendAsRequest{
		Peer:   &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		SendAs: &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
	}); err != nil || !ok {
		t.Fatalf("save default send as channel = ok %v err %v, want true", ok, err)
	}
	fullWithDefault, err := r.onChannelsGetFullChannel(WithUserID(ctx, owner.ID), &tg.InputChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash})
	if err != nil {
		t.Fatalf("get full channel with default send as: %v", err)
	}
	channelFull := fullWithDefault.FullChat.(*tg.ChannelFull)
	defaultSendAs, ok := channelFull.GetDefaultSendAs()
	if !ok {
		t.Fatalf("full channel default_send_as missing")
	}
	if peer, ok := defaultSendAs.(*tg.PeerChannel); !ok || peer.ChannelID != channel.ID {
		t.Fatalf("full channel default_send_as = %#v, want current channel %d", defaultSendAs, channel.ID)
	}
	sentWithDefault, err := r.onMessagesSendMessage(WithUserID(ctx, owner.ID), &tg.MessagesSendMessageRequest{
		Peer:     &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		Message:  "default send as fallback",
		RandomID: 80102,
	})
	if err != nil {
		t.Fatalf("send message with default send as: %v", err)
	}
	defaultMsg := sentWithDefault.(*tg.Updates).Updates[1].(*tg.UpdateNewChannelMessage).Message.(*tg.Message)
	if from, ok := defaultMsg.FromID.(*tg.PeerChannel); !ok || from.ChannelID != channel.ID {
		t.Fatalf("default send_as message from = %#v, want channel %d", defaultMsg.FromID, channel.ID)
	}
	forwardedWithDefault, err := r.onMessagesForwardMessages(WithUserID(ctx, owner.ID), &tg.MessagesForwardMessagesRequest{
		FromPeer: &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		ToPeer:   &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		ID:       []int{defaultMsg.ID},
		RandomID: []int64{80103},
	})
	if err != nil {
		t.Fatalf("forward with default send as: %v", err)
	}
	defaultForward := forwardedWithDefault.(*tg.Updates).Updates[1].(*tg.UpdateNewChannelMessage).Message.(*tg.Message)
	if from, ok := defaultForward.FromID.(*tg.PeerChannel); !ok || from.ChannelID != channel.ID {
		t.Fatalf("default forward send_as from = %#v, want channel %d", defaultForward.FromID, channel.ID)
	}
	if ok, err := r.onMessagesSaveDefaultSendAs(WithUserID(ctx, owner.ID), &tg.MessagesSaveDefaultSendAsRequest{
		Peer:   &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		SendAs: &tg.InputPeerSelf{},
	}); err != nil || !ok {
		t.Fatalf("clear default send as self = ok %v err %v, want true", ok, err)
	}
	fullAfterClear, err := r.onChannelsGetFullChannel(WithUserID(ctx, owner.ID), &tg.InputChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash})
	if err != nil {
		t.Fatalf("get full channel after clearing default send as: %v", err)
	}
	if _, ok := fullAfterClear.FullChat.(*tg.ChannelFull).GetDefaultSendAs(); ok {
		t.Fatalf("full channel default_send_as still set after saving self")
	}
	if _, err := r.onMessagesSaveDefaultSendAs(WithUserID(ctx, owner.ID), &tg.MessagesSaveDefaultSendAsRequest{
		Peer:   &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		SendAs: &tg.InputPeerUser{UserID: friend.ID, AccessHash: friend.AccessHash},
	}); err == nil || !strings.Contains(err.Error(), "SEND_AS_PEER_INVALID") {
		t.Fatalf("save default send as friend err = %v, want SEND_AS_PEER_INVALID", err)
	}

	sponsored, err := r.onMessagesGetSponsoredMessages(WithUserID(ctx, owner.ID), &tg.MessagesGetSponsoredMessagesRequest{
		Peer: &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
	})
	if err != nil {
		t.Fatalf("get sponsored messages: %v", err)
	}
	if _, ok := sponsored.(*tg.MessagesSponsoredMessagesEmpty); !ok {
		t.Fatalf("sponsored = %T, want empty", sponsored)
	}
	historyTTL, err := r.onMessagesGetDefaultHistoryTTL(WithUserID(ctx, owner.ID))
	if err != nil {
		t.Fatalf("get default history ttl: %v", err)
	}
	if historyTTL.Period != 0 {
		t.Fatalf("default history ttl = %+v, want disabled", historyTTL)
	}
	accountTTL, err := r.onAccountGetAccountTTL(WithUserID(ctx, owner.ID))
	if err != nil {
		t.Fatalf("get account ttl: %v", err)
	}
	if accountTTL.Days <= 0 {
		t.Fatalf("account ttl = %+v, want positive days", accountTTL)
	}
	preview, err := r.onMessagesGetWebPagePreview(WithUserID(ctx, owner.ID), &tg.MessagesGetWebPagePreviewRequest{
		Message: "https://example.com",
	})
	if err != nil {
		t.Fatalf("messages.getWebPagePreview: %v", err)
	}
	if _, ok := preview.Media.(*tg.MessageMediaEmpty); !ok || len(preview.Chats) != 0 || len(preview.Users) != 0 {
		t.Fatalf("messages.getWebPagePreview = %+v, want empty media preview", preview)
	}
	if _, err := r.onMessagesGetWebPagePreview(WithUserID(ctx, owner.ID), &tg.MessagesGetWebPagePreviewRequest{}); err == nil || !strings.Contains(err.Error(), "MESSAGE_EMPTY") {
		t.Fatalf("messages.getWebPagePreview empty err = %v, want MESSAGE_EMPTY", err)
	}
	if media, err := r.onMessagesUploadMedia(WithUserID(ctx, owner.ID), &tg.MessagesUploadMediaRequest{
		Peer:  &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		Media: &tg.InputMediaEmpty{},
	}); err != nil {
		t.Fatalf("messages.uploadMedia empty: %v", err)
	} else if _, ok := media.(*tg.MessageMediaEmpty); !ok {
		t.Fatalf("messages.uploadMedia empty = %#v, want messageMediaEmpty", media)
	}
	if _, err := r.onMessagesUploadMedia(WithUserID(ctx, owner.ID), &tg.MessagesUploadMediaRequest{
		Peer:  &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		Media: &tg.InputMediaUploadedPhoto{},
	}); err == nil || !strings.Contains(err.Error(), "MEDIA_INVALID") {
		t.Fatalf("messages.uploadMedia unsupported err = %v, want MEDIA_INVALID", err)
	}
	if updates, err := r.onMessagesSendMedia(WithUserID(ctx, owner.ID), &tg.MessagesSendMediaRequest{
		Peer:     &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		Media:    &tg.InputMediaWebPage{URL: "https://example.com"},
		Message:  "https://example.com",
		RandomID: 91001,
	}); err != nil {
		t.Fatalf("messages.sendMedia webpage-as-text: %v", err)
	} else if len(updates.(*tg.Updates).Updates) == 0 {
		t.Fatalf("messages.sendMedia webpage-as-text = %+v, want channel message updates", updates)
	}
	if _, err := r.onMessagesSendMedia(WithUserID(ctx, owner.ID), &tg.MessagesSendMediaRequest{
		Peer:     &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		Media:    &tg.InputMediaUploadedPhoto{},
		Message:  "photo",
		RandomID: 91002,
	}); err == nil || !strings.Contains(err.Error(), "MEDIA_INVALID") {
		t.Fatalf("messages.sendMedia unsupported err = %v, want MEDIA_INVALID", err)
	}
	if _, err := r.onMessagesSendMultiMedia(WithUserID(ctx, owner.ID), &tg.MessagesSendMultiMediaRequest{
		Peer: &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		MultiMedia: []tg.InputSingleMedia{{
			Media:    &tg.InputMediaUploadedPhoto{},
			RandomID: 91003,
			Message:  "photo",
		}},
	}); err == nil || !strings.Contains(err.Error(), "MEDIA_INVALID") {
		t.Fatalf("messages.sendMultiMedia unsupported err = %v, want MEDIA_INVALID", err)
	}
	savedHistoryReq := &tg.MessagesGetSavedHistoryRequest{
		Peer:  &tg.InputPeerSelf{},
		Limit: 20,
	}
	savedHistoryReq.SetParentPeer(&tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash})
	savedHistory, err := r.onMessagesGetSavedHistory(WithUserID(ctx, owner.ID), savedHistoryReq)
	if err != nil {
		t.Fatalf("messages.getSavedHistory: %v", err)
	}
	if len(savedHistory.(*tg.MessagesMessages).Messages) != 0 || len(savedHistory.(*tg.MessagesMessages).Chats) != 1 {
		t.Fatalf("messages.getSavedHistory = %+v, want empty with parent channel context", savedHistory)
	}
	badSavedParent := &tg.MessagesGetSavedHistoryRequest{
		Peer:  &tg.InputPeerSelf{},
		Limit: 20,
	}
	badSavedParent.SetParentPeer(&tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash + 1})
	if _, err := r.onMessagesGetSavedHistory(WithUserID(ctx, owner.ID), badSavedParent); err == nil || !strings.Contains(err.Error(), "PARENT_PEER_INVALID") {
		t.Fatalf("messages.getSavedHistory bad parent err = %v, want PARENT_PEER_INVALID", err)
	}
	if _, err := r.onMessagesGetSavedHistory(WithUserID(ctx, owner.ID), &tg.MessagesGetSavedHistoryRequest{
		Peer:  &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash + 1},
		Limit: 20,
	}); err == nil || !strings.Contains(err.Error(), "CHANNEL_PRIVATE") {
		t.Fatalf("messages.getSavedHistory bad peer err = %v, want CHANNEL_PRIVATE", err)
	}
	if _, err := r.onMessagesGetSavedHistory(WithUserID(ctx, owner.ID), &tg.MessagesGetSavedHistoryRequest{
		Peer:  &tg.InputPeerSelf{},
		Limit: maxSearchResultsLimit + 1,
	}); err == nil || !strings.Contains(err.Error(), "LIMIT_INVALID") {
		t.Fatalf("messages.getSavedHistory limit err = %v, want LIMIT_INVALID", err)
	}
	if ok, err := r.onMessagesReadSavedHistory(WithUserID(ctx, owner.ID), &tg.MessagesReadSavedHistoryRequest{
		ParentPeer: &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		Peer:       &tg.InputPeerSelf{},
		MaxID:      0,
	}); err != nil || !ok {
		t.Fatalf("messages.readSavedHistory = ok %v err %v, want true", ok, err)
	}
	if _, err := r.onMessagesReadSavedHistory(WithUserID(ctx, owner.ID), &tg.MessagesReadSavedHistoryRequest{
		ParentPeer: &tg.InputPeerSelf{},
		Peer:       &tg.InputPeerSelf{},
	}); err == nil || !strings.Contains(err.Error(), "PARENT_PEER_INVALID") {
		t.Fatalf("messages.readSavedHistory bad parent err = %v, want PARENT_PEER_INVALID", err)
	}
	if _, err := r.onMessagesReadSavedHistory(WithUserID(ctx, owner.ID), &tg.MessagesReadSavedHistoryRequest{
		ParentPeer: &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		Peer:       &tg.InputPeerSelf{},
		MaxID:      domain.MaxMessageBoxID + 1,
	}); err == nil || !strings.Contains(err.Error(), "MESSAGE_ID_INVALID") {
		t.Fatalf("messages.readSavedHistory max_id err = %v, want MESSAGE_ID_INVALID", err)
	}
	deleteSavedReq := &tg.MessagesDeleteSavedHistoryRequest{
		Peer: &tg.InputPeerSelf{},
	}
	deleteSavedReq.SetParentPeer(&tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash})
	deletedSaved, err := r.onMessagesDeleteSavedHistory(WithUserID(ctx, owner.ID), deleteSavedReq)
	if err != nil {
		t.Fatalf("messages.deleteSavedHistory: %v", err)
	}
	if deletedSaved.Offset != 0 || deletedSaved.PtsCount != 0 {
		t.Fatalf("messages.deleteSavedHistory = %+v, want empty affected history", deletedSaved)
	}
	badDeleteDate := &tg.MessagesDeleteSavedHistoryRequest{
		Peer: &tg.InputPeerSelf{},
	}
	badDeleteDate.SetMinDate(-1)
	if _, err := r.onMessagesDeleteSavedHistory(WithUserID(ctx, owner.ID), badDeleteDate); err == nil || !strings.Contains(err.Error(), "LIMIT_INVALID") {
		t.Fatalf("messages.deleteSavedHistory min_date err = %v, want LIMIT_INVALID", err)
	}
	scheduledHistory, err := r.onMessagesGetScheduledHistory(WithUserID(ctx, owner.ID), &tg.MessagesGetScheduledHistoryRequest{
		Peer: &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
	})
	if err != nil {
		t.Fatalf("messages.getScheduledHistory: %v", err)
	}
	if len(scheduledHistory.(*tg.MessagesMessages).Messages) != 0 || len(scheduledHistory.(*tg.MessagesMessages).Chats) != 1 {
		t.Fatalf("messages.getScheduledHistory = %+v, want empty with channel context", scheduledHistory)
	}
	if _, err := r.onMessagesGetScheduledHistory(WithUserID(ctx, owner.ID), &tg.MessagesGetScheduledHistoryRequest{
		Peer: &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash + 1},
	}); err == nil || !strings.Contains(err.Error(), "CHANNEL_PRIVATE") {
		t.Fatalf("messages.getScheduledHistory bad hash err = %v, want CHANNEL_PRIVATE", err)
	}
	scheduled, err := r.onMessagesGetScheduledMessages(WithUserID(ctx, owner.ID), &tg.MessagesGetScheduledMessagesRequest{
		Peer: &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		ID:   []int{1},
	})
	if err != nil {
		t.Fatalf("messages.getScheduledMessages: %v", err)
	}
	if len(scheduled.(*tg.MessagesMessages).Messages) != 0 || len(scheduled.(*tg.MessagesMessages).Chats) != 1 {
		t.Fatalf("messages.getScheduledMessages = %+v, want empty with channel context", scheduled)
	}
	if _, err := r.onMessagesGetScheduledMessages(WithUserID(ctx, owner.ID), &tg.MessagesGetScheduledMessagesRequest{
		Peer: &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		ID:   make([]int, maxGetMessagesIDs+1),
	}); err == nil || !strings.Contains(err.Error(), "LIMIT_INVALID") {
		t.Fatalf("messages.getScheduledMessages id cap err = %v, want LIMIT_INVALID", err)
	}
	deletedScheduled, err := r.onMessagesDeleteScheduledMessages(WithUserID(ctx, owner.ID), &tg.MessagesDeleteScheduledMessagesRequest{
		Peer: &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		ID:   []int{7, 8},
	})
	if err != nil {
		t.Fatalf("messages.deleteScheduledMessages: %v", err)
	}
	if got := deletedScheduled.(*tg.Updates).Updates; len(got) != 1 {
		t.Fatalf("messages.deleteScheduledMessages updates = %+v, want one update", got)
	} else if update, ok := got[0].(*tg.UpdateDeleteScheduledMessages); !ok || len(update.Messages) != 2 || update.Messages[0] != 7 {
		t.Fatalf("messages.deleteScheduledMessages update = %#v, want requested ids", got[0])
	}
	if _, err := r.onMessagesSendScheduledMessages(WithUserID(ctx, owner.ID), &tg.MessagesSendScheduledMessagesRequest{
		Peer: &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		ID:   []int{7},
	}); err == nil || !strings.Contains(err.Error(), "MESSAGE_ID_INVALID") {
		t.Fatalf("messages.sendScheduledMessages err = %v, want MESSAGE_ID_INVALID", err)
	}
	if _, err := r.onMessagesCreateForumTopic(WithUserID(ctx, owner.ID), &tg.MessagesCreateForumTopicRequest{
		Peer:     &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		Title:    "Topic",
		RandomID: 91004,
	}); err == nil || !strings.Contains(err.Error(), "CHANNEL_FORUM_MISSING") {
		t.Fatalf("messages.createForumTopic err = %v, want CHANNEL_FORUM_MISSING", err)
	}
	if _, err := r.onMessagesCreateForumTopic(WithUserID(ctx, owner.ID), &tg.MessagesCreateForumTopicRequest{
		Peer:     &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		RandomID: 91005,
	}); err == nil || !strings.Contains(err.Error(), "TOPIC_TITLE_EMPTY") {
		t.Fatalf("messages.createForumTopic empty title err = %v, want TOPIC_TITLE_EMPTY", err)
	}
	if _, err := r.onMessagesEditForumTopic(WithUserID(ctx, owner.ID), &tg.MessagesEditForumTopicRequest{
		Peer:    &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		TopicID: 1,
	}); err == nil || !strings.Contains(err.Error(), "CHANNEL_FORUM_MISSING") {
		t.Fatalf("messages.editForumTopic err = %v, want CHANNEL_FORUM_MISSING", err)
	}
	if _, err := r.onMessagesUpdatePinnedForumTopic(WithUserID(ctx, owner.ID), &tg.MessagesUpdatePinnedForumTopicRequest{
		Peer:    &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		TopicID: 1,
		Pinned:  true,
	}); err == nil || !strings.Contains(err.Error(), "CHANNEL_FORUM_MISSING") {
		t.Fatalf("messages.updatePinnedForumTopic err = %v, want CHANNEL_FORUM_MISSING", err)
	}
	if _, err := r.onMessagesReorderPinnedForumTopics(WithUserID(ctx, owner.ID), &tg.MessagesReorderPinnedForumTopicsRequest{
		Peer:  &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		Order: []int{1, 2},
	}); err == nil || !strings.Contains(err.Error(), "CHANNEL_FORUM_MISSING") {
		t.Fatalf("messages.reorderPinnedForumTopics err = %v, want CHANNEL_FORUM_MISSING", err)
	}
	if _, err := r.onMessagesDeleteTopicHistory(WithUserID(ctx, owner.ID), &tg.MessagesDeleteTopicHistoryRequest{
		Peer:     &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		TopMsgID: 1,
	}); err == nil || !strings.Contains(err.Error(), "CHANNEL_FORUM_MISSING") {
		t.Fatalf("messages.deleteTopicHistory err = %v, want CHANNEL_FORUM_MISSING", err)
	}
	chats, err := r.onMessagesGetChats(WithUserID(ctx, owner.ID), []int64{channel.ID})
	if err != nil {
		t.Fatalf("messages.getChats legacy wrapper: %v", err)
	}
	if len(chats.(*tg.MessagesChats).Chats) != 1 {
		t.Fatalf("messages.getChats = %+v, want current megagroup channel", chats)
	}
	migrated, err := r.onMessagesMigrateChat(WithUserID(ctx, owner.ID), channel.ID)
	if err != nil {
		t.Fatalf("messages.migrateChat legacy mapping: %v", err)
	}
	migratedUpdates := migrated.(*tg.Updates)
	if len(migratedUpdates.Chats) != 1 || len(migratedUpdates.Updates) != 1 {
		t.Fatalf("messages.migrateChat = %+v, want updateChannel with current megagroup", migratedUpdates)
	}
	if _, err := r.onMessagesMigrateChat(WithUserID(ctx, friend.ID), channel.ID); err == nil {
		t.Fatalf("messages.migrateChat by non-admin = nil err, want CHAT_ADMIN_REQUIRED")
	}
	full, err := r.onMessagesGetFullChat(WithUserID(ctx, owner.ID), channel.ID)
	if err != nil {
		t.Fatalf("messages.getFullChat legacy wrapper: %v", err)
	}
	if got := full.FullChat.(*tg.ChannelFull).ID; got != channel.ID {
		t.Fatalf("messages.getFullChat id = %d, want %d", got, channel.ID)
	}
	viewedID := defaultMsg.ID
	views, err := r.onMessagesGetMessagesViews(WithUserID(ctx, owner.ID), &tg.MessagesGetMessagesViewsRequest{
		Peer: &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		ID:   []int{viewedID},
	})
	if err != nil {
		t.Fatalf("messages.getMessagesViews: %v", err)
	}
	if len(views.Views) != 1 || len(views.Chats) != 1 {
		t.Fatalf("messages.getMessagesViews = %+v, want one view with channel context", views)
	}
	if got, ok := views.Views[0].GetViews(); !ok || got != 0 {
		t.Fatalf("messages.getMessagesViews views = %d ok %v, want explicit zero before increment", got, ok)
	}
	incremented, err := r.onMessagesGetMessagesViews(WithUserID(ctx, owner.ID), &tg.MessagesGetMessagesViewsRequest{
		Peer:      &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		ID:        []int{viewedID},
		Increment: true,
	})
	if err != nil {
		t.Fatalf("messages.getMessagesViews increment: %v", err)
	}
	if got, ok := incrementalViewCount(incremented); !ok || got != 1 {
		t.Fatalf("messages.getMessagesViews increment count = %d ok %v, want 1", got, ok)
	}
	repeated, err := r.onMessagesGetMessagesViews(WithUserID(ctx, owner.ID), &tg.MessagesGetMessagesViewsRequest{
		Peer:      &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		ID:        []int{viewedID},
		Increment: true,
	})
	if err != nil {
		t.Fatalf("messages.getMessagesViews repeat increment: %v", err)
	}
	if got, ok := incrementalViewCount(repeated); !ok || got != 1 {
		t.Fatalf("messages.getMessagesViews repeated count = %d ok %v, want still 1", got, ok)
	}
	friendIncremented, err := r.onMessagesGetMessagesViews(WithUserID(ctx, friend.ID), &tg.MessagesGetMessagesViewsRequest{
		Peer:      &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		ID:        []int{viewedID},
		Increment: true,
	})
	if err != nil {
		t.Fatalf("messages.getMessagesViews friend increment: %v", err)
	}
	if got, ok := incrementalViewCount(friendIncremented); !ok || got != 2 {
		t.Fatalf("messages.getMessagesViews friend count = %d ok %v, want 2", got, ok)
	}
	if _, err := r.onMessagesReadMessageContents(WithUserID(ctx, owner.ID), []int{1}); err != nil {
		t.Fatalf("messages.readMessageContents: %v", err)
	}
	mentions, err := r.onMessagesGetUnreadMentions(WithUserID(ctx, owner.ID), &tg.MessagesGetUnreadMentionsRequest{
		Peer:  &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		Limit: 10,
	})
	if err != nil {
		t.Fatalf("messages.getUnreadMentions: %v", err)
	}
	_, mentionChats, _ := searchMessagesPayload(t, mentions)
	if len(mentionChats) != 1 {
		t.Fatalf("messages.getUnreadMentions = %+v, want channel context", mentions)
	}
	if _, err := r.onMessagesReadMentions(WithUserID(ctx, owner.ID), &tg.MessagesReadMentionsRequest{
		Peer: &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
	}); err != nil {
		t.Fatalf("messages.readMentions: %v", err)
	}
	if ok, err := r.onMessagesReportSpam(WithUserID(ctx, owner.ID), &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash}); err != nil || !ok {
		t.Fatalf("messages.reportSpam = ok %v err %v, want true nil", ok, err)
	}
	reportOptions, err := r.onMessagesReport(WithUserID(ctx, owner.ID), &tg.MessagesReportRequest{
		Peer: &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		ID:   []int{1},
	})
	if err != nil {
		t.Fatalf("messages.report options: %v", err)
	}
	if choices, ok := reportOptions.(*tg.ReportResultChooseOption); !ok || len(choices.Options) == 0 {
		t.Fatalf("messages.report options = %#v, want chooseOption", reportOptions)
	}
	reportComment, err := r.onMessagesReport(WithUserID(ctx, owner.ID), &tg.MessagesReportRequest{
		Peer:   &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		ID:     []int{1},
		Option: []byte("other"),
	})
	if err != nil {
		t.Fatalf("messages.report other: %v", err)
	}
	if _, ok := reportComment.(*tg.ReportResultAddComment); !ok {
		t.Fatalf("messages.report other = %#v, want addComment", reportComment)
	}
	reported, err := r.onMessagesReport(WithUserID(ctx, owner.ID), &tg.MessagesReportRequest{
		Peer:   &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		ID:     []int{1},
		Option: []byte("spam"),
	})
	if err != nil {
		t.Fatalf("messages.report spam: %v", err)
	}
	if _, ok := reported.(*tg.ReportResultReported); !ok {
		t.Fatalf("messages.report spam = %#v, want reported", reported)
	}
	if _, err := r.onMessagesReport(WithUserID(ctx, owner.ID), &tg.MessagesReportRequest{
		Peer:   &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		ID:     []int{1},
		Option: []byte("bogus"),
	}); err == nil || !strings.Contains(err.Error(), "OPTION_INVALID") {
		t.Fatalf("messages.report invalid option err = %v, want OPTION_INVALID", err)
	}
	if ok, err := r.onMessagesReportReaction(WithUserID(ctx, owner.ID), &tg.MessagesReportReactionRequest{
		Peer:         &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		ID:           1,
		ReactionPeer: &tg.InputPeerUser{UserID: friend.ID, AccessHash: friend.AccessHash},
	}); err != nil || !ok {
		t.Fatalf("messages.reportReaction = ok %v err %v, want true nil", ok, err)
	}
	if ok, err := r.onMessagesReportMessagesDelivery(WithUserID(ctx, owner.ID), &tg.MessagesReportMessagesDeliveryRequest{
		Peer: &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		ID:   []int{1},
	}); err != nil || !ok {
		t.Fatalf("messages.reportMessagesDelivery = ok %v err %v, want true nil", ok, err)
	}
	if ok, err := r.onMessagesReportReadMetrics(WithUserID(ctx, owner.ID), &tg.MessagesReportReadMetricsRequest{
		Peer: &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		Metrics: []tg.InputMessageReadMetric{{
			MsgID:                         1,
			ViewID:                        99,
			TimeInViewMs:                  10,
			ActiveTimeInViewMs:            10,
			HeightToViewportRatioPermille: 1000,
			SeenRangeRatioPermille:        1000,
		}},
	}); err != nil || !ok {
		t.Fatalf("messages.reportReadMetrics = ok %v err %v, want true nil", ok, err)
	}
	if ok, err := r.onMessagesReportMusicListen(WithUserID(ctx, owner.ID), &tg.MessagesReportMusicListenRequest{
		ID:               &tg.InputDocument{ID: 1, AccessHash: 2},
		ListenedDuration: 1,
	}); err != nil || !ok {
		t.Fatalf("messages.reportMusicListen = ok %v err %v, want true nil", ok, err)
	}
	sponsoredReport, err := r.onMessagesReportSponsoredMessage(WithUserID(ctx, owner.ID), &tg.MessagesReportSponsoredMessageRequest{
		RandomID: []byte("ad"),
		Option:   []byte("spam"),
	})
	if err != nil {
		t.Fatalf("messages.reportSponsoredMessage: %v", err)
	}
	if _, ok := sponsoredReport.(*tg.ChannelsSponsoredMessageReportResultReported); !ok {
		t.Fatalf("messages.reportSponsoredMessage = %#v, want reported", sponsoredReport)
	}
	sendReactionReq := &tg.MessagesSendReactionRequest{
		Peer:     &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		MsgID:    viewedID,
		Reaction: []tg.ReactionClass{&tg.ReactionEmoji{Emoticon: "\U0001f44d"}},
	}
	sendReactionReq.SetReaction(sendReactionReq.Reaction)
	sendReactionReq.SetAddToRecent(true)
	if updates, err := r.onMessagesSendReaction(WithUserID(ctx, owner.ID), sendReactionReq); err != nil {
		t.Fatalf("messages.sendReaction: %v", err)
	} else if got := updates.(*tg.Updates).Updates; len(got) != 1 {
		t.Fatalf("messages.sendReaction updates = %+v, want one reaction update", got)
	} else if update, ok := got[0].(*tg.UpdateMessageReactions); !ok || update.MsgID != viewedID || len(update.Reactions.Results) != 1 || update.Reactions.Results[0].Count != 1 || update.Reactions.Results[0].ChosenOrder != 1 {
		t.Fatalf("messages.sendReaction update = %#v, want one chosen reaction", got[0])
	}
	tooManyReactionsReq := &tg.MessagesSendReactionRequest{
		Peer:     &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		MsgID:    viewedID,
		Reaction: make([]tg.ReactionClass, maxReactionVector+1),
	}
	tooManyReactionsReq.SetReaction(tooManyReactionsReq.Reaction)
	if _, err := r.onMessagesSendReaction(WithUserID(ctx, owner.ID), tooManyReactionsReq); err == nil || !strings.Contains(err.Error(), "LIMIT_INVALID") {
		t.Fatalf("messages.sendReaction huge reaction vector err = %v, want LIMIT_INVALID", err)
	}
	reactionUpdates, err := r.onMessagesGetMessagesReactions(WithUserID(ctx, owner.ID), &tg.MessagesGetMessagesReactionsRequest{
		Peer: &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		ID:   []int{viewedID},
	})
	if err != nil {
		t.Fatalf("messages.getMessagesReactions: %v", err)
	}
	if got := reactionUpdates.(*tg.Updates).Updates; len(got) != 1 {
		t.Fatalf("messages.getMessagesReactions updates = %+v, want one reaction update", got)
	} else if update, ok := got[0].(*tg.UpdateMessageReactions); !ok || update.MsgID != viewedID || len(update.Reactions.Results) != 1 || update.Reactions.Results[0].Count != 1 {
		t.Fatalf("messages.getMessagesReactions update = %#v, want one reaction", got[0])
	}
	reactionList, err := r.onMessagesGetMessageReactionsList(WithUserID(ctx, owner.ID), &tg.MessagesGetMessageReactionsListRequest{
		Peer:  &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		ID:    viewedID,
		Limit: 10,
	})
	if err != nil {
		t.Fatalf("messages.getMessageReactionsList: %v", err)
	}
	if reactionList.Count != 1 || len(reactionList.Reactions) != 1 || len(reactionList.Chats) != 1 || len(reactionList.Users) != 1 {
		t.Fatalf("messages.getMessageReactionsList = %+v, want one reaction with channel context", reactionList)
	}
	if peer, ok := reactionList.Reactions[0].PeerID.(*tg.PeerUser); !ok || peer.UserID != owner.ID || !reactionList.Reactions[0].My {
		t.Fatalf("messages.getMessageReactionsList reaction = %+v, want current user reaction", reactionList.Reactions[0])
	}
	friendReactionReq := &tg.MessagesSendReactionRequest{
		Peer:     &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		MsgID:    viewedID,
		Reaction: []tg.ReactionClass{&tg.ReactionEmoji{Emoticon: "\U0001f44d"}},
	}
	friendReactionReq.SetReaction(friendReactionReq.Reaction)
	if _, err := r.onMessagesSendReaction(WithUserID(ctx, friend.ID), friendReactionReq); err != nil {
		t.Fatalf("messages.sendReaction by friend: %v", err)
	}
	unreadReactions, err := r.onMessagesGetUnreadReactions(WithUserID(ctx, owner.ID), &tg.MessagesGetUnreadReactionsRequest{
		Peer:  &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		Limit: 10,
	})
	if err != nil {
		t.Fatalf("messages.getUnreadReactions: %v", err)
	}
	unreadMessages, unreadChats, unreadUsers := searchMessagesPayload(t, unreadReactions)
	if len(unreadMessages) != 1 || len(unreadChats) != 1 || len(unreadUsers) == 0 {
		t.Fatalf("messages.getUnreadReactions = %+v, want one unread reaction with channel context", unreadReactions)
	}
	unreadMessage, ok := unreadMessages[0].(*tg.Message)
	if !ok || unreadMessage.ID != viewedID {
		t.Fatalf("messages.getUnreadReactions message = %#v, want message %d", unreadMessages[0], viewedID)
	}
	reactions, ok := unreadMessage.GetReactions()
	hasUnreadRecent := false
	for _, recent := range reactions.RecentReactions {
		if recent.Unread {
			hasUnreadRecent = true
			break
		}
	}
	if !ok || !hasUnreadRecent {
		t.Fatalf("messages.getUnreadReactions reactions = %+v ok %v, want unread recent reaction", reactions, ok)
	}
	if _, err := r.onMessagesReadReactions(WithUserID(ctx, owner.ID), &tg.MessagesReadReactionsRequest{
		Peer: &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
	}); err != nil {
		t.Fatalf("messages.readReactions: %v", err)
	}
	unreadAfterRead, err := r.onMessagesGetUnreadReactions(WithUserID(ctx, owner.ID), &tg.MessagesGetUnreadReactionsRequest{
		Peer:  &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		Limit: 10,
	})
	if err != nil {
		t.Fatalf("messages.getUnreadReactions after read: %v", err)
	}
	unreadAfterReadMessages, _, _ := searchMessagesPayload(t, unreadAfterRead)
	if len(unreadAfterReadMessages) != 0 {
		t.Fatalf("messages.getUnreadReactions after read = %+v, want empty messages", unreadAfterRead)
	}
	common, err := r.onMessagesGetCommonChats(WithUserID(ctx, owner.ID), &tg.MessagesGetCommonChatsRequest{
		UserID: &tg.InputUser{UserID: friend.ID, AccessHash: friend.AccessHash},
		Limit:  40,
	})
	if err != nil {
		t.Fatalf("messages.getCommonChats: %v", err)
	}
	commonChats := common.(*tg.MessagesChats).Chats
	if len(commonChats) != 1 || commonChats[0].(*tg.Channel).ID != channel.ID {
		t.Fatalf("messages.getCommonChats = %+v, want current shared megagroup", common)
	}
	commonNext, err := r.onMessagesGetCommonChats(WithUserID(ctx, owner.ID), &tg.MessagesGetCommonChatsRequest{
		UserID: &tg.InputUser{UserID: friend.ID, AccessHash: friend.AccessHash},
		MaxID:  channel.ID,
		Limit:  40,
	})
	if err != nil {
		t.Fatalf("messages.getCommonChats next page: %v", err)
	}
	if len(commonNext.(*tg.MessagesChats).Chats) != 0 {
		t.Fatalf("messages.getCommonChats next page = %+v, want empty after max id", commonNext)
	}
	fullFriend, err := r.onUsersGetFullUser(WithUserID(ctx, owner.ID), &tg.InputUser{UserID: friend.ID, AccessHash: friend.AccessHash})
	if err != nil {
		t.Fatalf("users.getFullUser friend: %v", err)
	}
	if fullFriend.FullUser.CommonChatsCount != 1 {
		t.Fatalf("users.getFullUser commonChatsCount = %d, want 1", fullFriend.FullUser.CommonChatsCount)
	}
	if _, err := r.onMessagesGetCommonChats(WithUserID(ctx, owner.ID), &tg.MessagesGetCommonChatsRequest{
		UserID: &tg.InputUserSelf{},
		Limit:  1,
	}); err == nil || !strings.Contains(err.Error(), "USER_ID_INVALID") {
		t.Fatalf("messages.getCommonChats self err = %v, want USER_ID_INVALID", err)
	}
	if _, err := r.onMessagesGetAttachedStickers(WithUserID(ctx, owner.ID), nil); err == nil || !strings.Contains(err.Error(), "MEDIA_EMPTY") {
		t.Fatalf("messages.getAttachedStickers nil err = %v, want MEDIA_EMPTY", err)
	}
	attached, err := r.onMessagesGetAttachedStickers(WithUserID(ctx, owner.ID), &tg.InputStickeredMediaDocument{
		ID: &tg.InputDocument{ID: 1, AccessHash: 2},
	})
	if err != nil || len(attached) != 0 {
		t.Fatalf("messages.getAttachedStickers = %+v err=%v, want empty", attached, err)
	}
	customEmojiDocs, err := r.onMessagesGetCustomEmojiDocuments(WithUserID(ctx, owner.ID), []int64{1, 2})
	if err != nil || len(customEmojiDocs) != 0 {
		t.Fatalf("messages.getCustomEmojiDocuments = %+v err=%v, want empty", customEmojiDocs, err)
	}
	if _, err := r.onMessagesGetCustomEmojiDocuments(WithUserID(ctx, owner.ID), make([]int64, maxEmojiDocuments+1)); err == nil || !strings.Contains(err.Error(), "LIMIT_INVALID") {
		t.Fatalf("messages.getCustomEmojiDocuments too many err = %v, want LIMIT_INVALID", err)
	}
	stickerSets, err := r.onMessagesSearchStickerSets(WithUserID(ctx, owner.ID), &tg.MessagesSearchStickerSetsRequest{Q: "cat"})
	if err != nil {
		t.Fatalf("messages.searchStickerSets: %v", err)
	}
	if found, ok := stickerSets.(*tg.MessagesFoundStickerSets); !ok || len(found.Sets) != 0 {
		t.Fatalf("messages.searchStickerSets = %#v, want empty found sets", stickerSets)
	}
	stickers, err := r.onMessagesSearchStickers(WithUserID(ctx, owner.ID), &tg.MessagesSearchStickersRequest{
		Q:        "cat",
		LangCode: []string{"en"},
		Limit:    50,
	})
	if err != nil {
		t.Fatalf("messages.searchStickers: %v", err)
	}
	if found, ok := stickers.(*tg.MessagesFoundStickers); !ok || len(found.Stickers) != 0 {
		t.Fatalf("messages.searchStickers = %#v, want empty found stickers", stickers)
	}
	if _, err := r.onMessagesSearchStickers(WithUserID(ctx, owner.ID), &tg.MessagesSearchStickersRequest{
		Limit: maxSearchResultsLimit + 1,
	}); err == nil || !strings.Contains(err.Error(), "LIMIT_INVALID") {
		t.Fatalf("messages.searchStickers huge limit err = %v, want LIMIT_INVALID", err)
	}
	keywords, err := r.onMessagesGetEmojiKeywords(WithUserID(ctx, owner.ID), "en")
	if err != nil || keywords.LangCode != "en" || len(keywords.Keywords) != 0 {
		t.Fatalf("messages.getEmojiKeywords = %+v err=%v, want empty en", keywords, err)
	}
	keywordsDiff, err := r.onMessagesGetEmojiKeywordsDifference(WithUserID(ctx, owner.ID), &tg.MessagesGetEmojiKeywordsDifferenceRequest{
		LangCode:    "en",
		FromVersion: 7,
	})
	if err != nil || keywordsDiff.Version != 7 || keywordsDiff.FromVersion != 7 {
		t.Fatalf("messages.getEmojiKeywordsDifference = %+v err=%v, want version echo", keywordsDiff, err)
	}
	extended, err := r.onMessagesGetExtendedMedia(WithUserID(ctx, owner.ID), &tg.MessagesGetExtendedMediaRequest{
		Peer: &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		ID:   []int{1},
	})
	if err != nil {
		t.Fatalf("messages.getExtendedMedia: %v", err)
	}
	if len(extended.(*tg.Updates).Updates) != 0 {
		t.Fatalf("messages.getExtendedMedia = %+v, want empty updates", extended)
	}
	topReactions, err := r.onMessagesGetTopReactions(WithUserID(ctx, owner.ID), &tg.MessagesGetTopReactionsRequest{Limit: 3})
	if err != nil {
		t.Fatalf("messages.getTopReactions: %v", err)
	}
	topPage, ok := topReactions.(*tg.MessagesReactions)
	if !ok || topPage.Hash == 0 || len(topPage.Reactions) != 3 {
		t.Fatalf("messages.getTopReactions = %#v, want three hashable reactions", topReactions)
	}
	if emoji, ok := topPage.Reactions[0].(*tg.ReactionEmoji); !ok || emoji.Emoticon != "\U0001f44d" {
		t.Fatalf("messages.getTopReactions first reaction = %#v, want account top thumb", topPage.Reactions[0])
	}
	if emoji, ok := topPage.Reactions[1].(*tg.ReactionEmoji); !ok || emoji.Emoticon != "\u2764\ufe0f" {
		t.Fatalf("messages.getTopReactions fallback reaction = %#v, want catalog heart", topPage.Reactions[1])
	}
	topNotModified, err := r.onMessagesGetTopReactions(WithUserID(ctx, owner.ID), &tg.MessagesGetTopReactionsRequest{Limit: 3, Hash: topPage.Hash})
	if err != nil {
		t.Fatalf("messages.getTopReactions hash: %v", err)
	}
	if _, ok := topNotModified.(*tg.MessagesReactionsNotModified); !ok {
		t.Fatalf("messages.getTopReactions hash = %#v, want notModified", topNotModified)
	}
	recentReactions, err := r.onMessagesGetRecentReactions(WithUserID(ctx, owner.ID), &tg.MessagesGetRecentReactionsRequest{Limit: 40})
	if err != nil {
		t.Fatalf("messages.getRecentReactions: %v", err)
	}
	recentPage, ok := recentReactions.(*tg.MessagesReactions)
	if !ok || recentPage.Hash == 0 || len(recentPage.Reactions) != 1 {
		t.Fatalf("messages.getRecentReactions = %#v, want one reaction with non-zero hash", recentReactions)
	}
	if emoji, ok := recentPage.Reactions[0].(*tg.ReactionEmoji); !ok || emoji.Emoticon != "\U0001f44d" {
		t.Fatalf("messages.getRecentReactions reaction = %#v, want thumb emoji", recentPage.Reactions[0])
	}
	recentNotModified, err := r.onMessagesGetRecentReactions(WithUserID(ctx, owner.ID), &tg.MessagesGetRecentReactionsRequest{Limit: 40, Hash: recentPage.Hash})
	if err != nil {
		t.Fatalf("messages.getRecentReactions hash: %v", err)
	}
	if _, ok := recentNotModified.(*tg.MessagesReactionsNotModified); !ok {
		t.Fatalf("messages.getRecentReactions hash = %#v, want notModified", recentNotModified)
	}
	if ok, err := r.onMessagesClearRecentReactions(WithUserID(ctx, owner.ID)); err != nil || !ok {
		t.Fatalf("messages.clearRecentReactions = ok %v err %v, want true nil", ok, err)
	}
	recentAfterClear, err := r.onMessagesGetRecentReactions(WithUserID(ctx, owner.ID), &tg.MessagesGetRecentReactionsRequest{Limit: 40, Hash: recentPage.Hash})
	if err != nil {
		t.Fatalf("messages.getRecentReactions after clear: %v", err)
	}
	clearedPage, ok := recentAfterClear.(*tg.MessagesReactions)
	if !ok || clearedPage.Hash != 0 || len(clearedPage.Reactions) != 0 {
		t.Fatalf("messages.getRecentReactions after clear = %#v, want empty page hash 0", recentAfterClear)
	}
	savedTagsReq := &tg.MessagesGetSavedReactionTagsRequest{
		Peer: &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
	}
	savedTagsReq.SetPeer(savedTagsReq.Peer)
	savedTags, err := r.onMessagesGetSavedReactionTags(WithUserID(ctx, owner.ID), savedTagsReq)
	if err != nil {
		t.Fatalf("messages.getSavedReactionTags: %v", err)
	}
	if got := savedTags.(*tg.MessagesSavedReactionTags).Tags; len(got) != 0 {
		t.Fatalf("messages.getSavedReactionTags = %+v, want empty", got)
	}
	staleEmptyTags, err := r.onMessagesGetSavedReactionTags(WithUserID(ctx, owner.ID), &tg.MessagesGetSavedReactionTagsRequest{Hash: 123})
	if err != nil {
		t.Fatalf("messages.getSavedReactionTags stale empty hash: %v", err)
	}
	staleEmptyPage, ok := staleEmptyTags.(*tg.MessagesSavedReactionTags)
	if !ok || staleEmptyPage.Hash != 0 || len(staleEmptyPage.Tags) != 0 {
		t.Fatalf("messages.getSavedReactionTags stale empty hash = %#v, want empty page hash 0", staleEmptyTags)
	}
	updateTagReq := &tg.MessagesUpdateSavedReactionTagRequest{Reaction: &tg.ReactionEmoji{Emoticon: "ok"}}
	updateTagReq.SetTitle("Work")
	if ok, err := r.onMessagesUpdateSavedReactionTag(WithUserID(ctx, owner.ID), updateTagReq); err != nil || !ok {
		t.Fatalf("messages.updateSavedReactionTag = ok %v err %v, want true nil", ok, err)
	}
	globalTags, err := r.onMessagesGetSavedReactionTags(WithUserID(ctx, owner.ID), &tg.MessagesGetSavedReactionTagsRequest{})
	if err != nil {
		t.Fatalf("messages.getSavedReactionTags global: %v", err)
	}
	globalPage, ok := globalTags.(*tg.MessagesSavedReactionTags)
	if !ok || globalPage.Hash == 0 || len(globalPage.Tags) != 1 {
		t.Fatalf("messages.getSavedReactionTags global = %#v, want one hashable tag", globalTags)
	}
	if emoji, ok := globalPage.Tags[0].Reaction.(*tg.ReactionEmoji); !ok || emoji.Emoticon != "ok" || globalPage.Tags[0].Title != "Work" || globalPage.Tags[0].Count != 0 {
		t.Fatalf("messages.getSavedReactionTags tag = %+v, want ok/Work/count0", globalPage.Tags[0])
	}
	globalNotModified, err := r.onMessagesGetSavedReactionTags(WithUserID(ctx, owner.ID), &tg.MessagesGetSavedReactionTagsRequest{Hash: globalPage.Hash})
	if err != nil {
		t.Fatalf("messages.getSavedReactionTags hash: %v", err)
	}
	if _, ok := globalNotModified.(*tg.MessagesSavedReactionTagsNotModified); !ok {
		t.Fatalf("messages.getSavedReactionTags hash = %#v, want notModified", globalNotModified)
	}
	peerTagsAfterUpdate, err := r.onMessagesGetSavedReactionTags(WithUserID(ctx, owner.ID), savedTagsReq)
	if err != nil {
		t.Fatalf("messages.getSavedReactionTags peer after update: %v", err)
	}
	if got := peerTagsAfterUpdate.(*tg.MessagesSavedReactionTags).Tags; len(got) != 0 {
		t.Fatalf("messages.getSavedReactionTags peer after update = %+v, want empty until saved-message tag store exists", got)
	}
	longTagReq := &tg.MessagesUpdateSavedReactionTagRequest{Reaction: &tg.ReactionEmoji{Emoticon: "ok"}}
	longTagReq.SetTitle("abcdefghijklmnop")
	if _, err := r.onMessagesUpdateSavedReactionTag(WithUserID(ctx, owner.ID), longTagReq); err == nil || !strings.Contains(err.Error(), "LIMIT_INVALID") {
		t.Fatalf("messages.updateSavedReactionTag long title err = %v, want LIMIT_INVALID", err)
	}
	if _, err := r.onMessagesUpdateSavedReactionTag(WithUserID(ctx, owner.ID), &tg.MessagesUpdateSavedReactionTagRequest{
		Reaction: &tg.ReactionCustomEmoji{DocumentID: 1},
	}); err == nil || !strings.Contains(err.Error(), "REACTION_INVALID") {
		t.Fatalf("messages.updateSavedReactionTag custom emoji err = %v, want REACTION_INVALID", err)
	}
	tagReactions, err := r.onMessagesGetDefaultTagReactions(WithUserID(ctx, owner.ID), 0)
	if err != nil {
		t.Fatalf("messages.getDefaultTagReactions: %v", err)
	}
	if got := tagReactions.(*tg.MessagesReactions).Reactions; len(got) != 0 {
		t.Fatalf("messages.getDefaultTagReactions = %+v, want empty", got)
	}
	if _, err := r.onMessagesSendVote(WithUserID(ctx, owner.ID), &tg.MessagesSendVoteRequest{
		Peer:    &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		MsgID:   1,
		Options: [][]byte{[]byte("a")},
	}); err == nil || !strings.Contains(err.Error(), "MESSAGE_ID_INVALID") {
		t.Fatalf("messages.sendVote err = %v, want MESSAGE_ID_INVALID without poll store", err)
	}
	if _, err := r.onMessagesSendVote(WithUserID(ctx, owner.ID), &tg.MessagesSendVoteRequest{
		Peer:    &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		MsgID:   1,
		Options: make([][]byte, maxPollVoteOptions+1),
	}); err == nil || !strings.Contains(err.Error(), "OPTIONS_TOO_MUCH") {
		t.Fatalf("messages.sendVote too many options err = %v, want OPTIONS_TOO_MUCH", err)
	}
	pollResults, err := r.onMessagesGetPollResults(WithUserID(ctx, owner.ID), &tg.MessagesGetPollResultsRequest{
		Peer:  &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		MsgID: 1,
	})
	if err != nil {
		t.Fatalf("messages.getPollResults: %v", err)
	}
	if len(pollResults.(*tg.Updates).Updates) != 0 {
		t.Fatalf("messages.getPollResults = %+v, want empty updates", pollResults)
	}
	pollVotesReq := &tg.MessagesGetPollVotesRequest{
		Peer:   &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		ID:     1,
		Option: []byte("a"),
		Limit:  10,
	}
	pollVotesReq.SetOption(pollVotesReq.Option)
	pollVotes, err := r.onMessagesGetPollVotes(WithUserID(ctx, owner.ID), pollVotesReq)
	if err != nil {
		t.Fatalf("messages.getPollVotes: %v", err)
	}
	if pollVotes.Count != 0 || len(pollVotes.Votes) != 0 || len(pollVotes.Chats) != 1 {
		t.Fatalf("messages.getPollVotes = %+v, want empty votes with channel context", pollVotes)
	}
	if _, err := r.onMessagesGetPollVotes(WithUserID(ctx, owner.ID), &tg.MessagesGetPollVotesRequest{
		Peer:  &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		ID:    1,
		Limit: maxSearchResultsLimit + 1,
	}); err == nil || !strings.Contains(err.Error(), "LIMIT_INVALID") {
		t.Fatalf("messages.getPollVotes huge limit err = %v, want LIMIT_INVALID", err)
	}
	if _, err := r.onMessagesAddPollAnswer(WithUserID(ctx, owner.ID), &tg.MessagesAddPollAnswerRequest{
		Peer:   &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		MsgID:  1,
		Answer: &tg.InputPollAnswer{Text: tg.TextWithEntities{Text: "extra"}},
	}); err == nil || !strings.Contains(err.Error(), "MESSAGE_ID_INVALID") {
		t.Fatalf("messages.addPollAnswer err = %v, want MESSAGE_ID_INVALID without poll store", err)
	}
	if _, err := r.onMessagesDeletePollAnswer(WithUserID(ctx, owner.ID), &tg.MessagesDeletePollAnswerRequest{
		Peer:   &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		MsgID:  1,
		Option: []byte("a"),
	}); err == nil || !strings.Contains(err.Error(), "MESSAGE_ID_INVALID") {
		t.Fatalf("messages.deletePollAnswer err = %v, want MESSAGE_ID_INVALID without poll store", err)
	}
	unreadPollVotes, err := r.onMessagesGetUnreadPollVotes(WithUserID(ctx, owner.ID), &tg.MessagesGetUnreadPollVotesRequest{
		Peer:  &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		Limit: 10,
	})
	if err != nil {
		t.Fatalf("messages.getUnreadPollVotes: %v", err)
	}
	if len(unreadPollVotes.(*tg.MessagesMessages).Messages) != 0 {
		t.Fatalf("messages.getUnreadPollVotes = %+v, want empty messages", unreadPollVotes)
	}
	if _, err := r.onMessagesReadPollVotes(WithUserID(ctx, owner.ID), &tg.MessagesReadPollVotesRequest{
		Peer: &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
	}); err != nil {
		t.Fatalf("messages.readPollVotes: %v", err)
	}
	if _, err := r.onMessagesAppendTodoList(WithUserID(ctx, owner.ID), &tg.MessagesAppendTodoListRequest{
		Peer:  &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		MsgID: 1,
	}); err == nil || !strings.Contains(err.Error(), "TODO_NOT_MODIFIED") {
		t.Fatalf("messages.appendTodoList empty err = %v, want TODO_NOT_MODIFIED", err)
	}
	if _, err := r.onMessagesAppendTodoList(WithUserID(ctx, owner.ID), &tg.MessagesAppendTodoListRequest{
		Peer:  &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		MsgID: 1,
		List:  []tg.TodoItem{{Title: tg.TextWithEntities{Text: "item"}}},
	}); err == nil || !strings.Contains(err.Error(), "MESSAGE_ID_INVALID") {
		t.Fatalf("messages.appendTodoList err = %v, want MESSAGE_ID_INVALID without todo store", err)
	}
	if _, err := r.onMessagesToggleTodoCompleted(WithUserID(ctx, owner.ID), &tg.MessagesToggleTodoCompletedRequest{
		Peer:  &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		MsgID: 1,
	}); err == nil || !strings.Contains(err.Error(), "TODO_NOT_MODIFIED") {
		t.Fatalf("messages.toggleTodoCompleted empty err = %v, want TODO_NOT_MODIFIED", err)
	}
	if _, err := r.onMessagesToggleTodoCompleted(WithUserID(ctx, owner.ID), &tg.MessagesToggleTodoCompletedRequest{
		Peer:      &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		MsgID:     1,
		Completed: []int{1},
	}); err == nil || !strings.Contains(err.Error(), "MESSAGE_ID_INVALID") {
		t.Fatalf("messages.toggleTodoCompleted err = %v, want MESSAGE_ID_INVALID without todo store", err)
	}
	counters, err := r.onMessagesGetSearchCounters(WithUserID(ctx, owner.ID), &tg.MessagesGetSearchCountersRequest{
		Peer:    &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		Filters: []tg.MessagesFilterClass{&tg.InputMessagesFilterEmpty{}},
	})
	if err != nil {
		t.Fatalf("messages.getSearchCounters: %v", err)
	}
	if len(counters) != 1 || counters[0].Count != 0 {
		t.Fatalf("messages.getSearchCounters = %+v, want one zero counter", counters)
	}
	calendar, err := r.onMessagesGetSearchResultsCalendar(WithUserID(ctx, owner.ID), &tg.MessagesGetSearchResultsCalendarRequest{
		Peer:       &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		Filter:     &tg.InputMessagesFilterPhotos{},
		OffsetID:   7,
		OffsetDate: 1700000000,
	})
	if err != nil {
		t.Fatalf("messages.getSearchResultsCalendar: %v", err)
	}
	if calendar.Count != 0 || calendar.MinDate != 1700000000 || calendar.MinMsgID != 7 || len(calendar.Messages) != 0 {
		t.Fatalf("messages.getSearchResultsCalendar = %+v, want empty stable-offset result", calendar)
	}
	positions, err := r.onMessagesGetSearchResultsPositions(WithUserID(ctx, owner.ID), &tg.MessagesGetSearchResultsPositionsRequest{
		Peer:     &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		Filter:   &tg.InputMessagesFilterPhotos{},
		OffsetID: 7,
		Limit:    10,
	})
	if err != nil {
		t.Fatalf("messages.getSearchResultsPositions: %v", err)
	}
	if positions.Count != 0 || len(positions.Positions) != 0 {
		t.Fatalf("messages.getSearchResultsPositions = %+v, want empty positions", positions)
	}
	if _, err := r.onMessagesGetSearchResultsPositions(WithUserID(ctx, owner.ID), &tg.MessagesGetSearchResultsPositionsRequest{
		Peer:     &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		Filter:   &tg.InputMessagesFilterPhotos{},
		OffsetID: 7,
		Limit:    maxSearchResultsLimit + 1,
	}); err == nil || !strings.Contains(err.Error(), "LIMIT_INVALID") {
		t.Fatalf("messages.getSearchResultsPositions huge limit err = %v, want LIMIT_INVALID", err)
	}
	replies, err := r.onMessagesGetReplies(WithUserID(ctx, owner.ID), &tg.MessagesGetRepliesRequest{
		Peer:  &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		MsgID: 1,
		Limit: 10,
	})
	if err != nil {
		t.Fatalf("messages.getReplies: %v", err)
	}
	_, replyChats, _ := searchMessagesPayload(t, replies)
	if len(replyChats) != 1 {
		t.Fatalf("messages.getReplies = %+v, want channel context", replies)
	}
	discussion, err := r.onMessagesGetDiscussionMessage(WithUserID(ctx, owner.ID), &tg.MessagesGetDiscussionMessageRequest{
		Peer:  &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		MsgID: 1,
	})
	if err != nil {
		t.Fatalf("messages.getDiscussionMessage: %v", err)
	}
	if len(discussion.Messages) != 1 || len(discussion.Chats) != 1 || discussion.UnreadCount != 0 {
		t.Fatalf("messages.getDiscussionMessage = %+v, want root message with channel context", discussion)
	}
	if _, ok := discussion.Messages[0].(*tg.MessageService); !ok {
		t.Fatalf("messages.getDiscussionMessage message = %T, want service root", discussion.Messages[0])
	}
	if _, err := r.onMessagesReadDiscussion(WithUserID(ctx, owner.ID), &tg.MessagesReadDiscussionRequest{
		Peer:      &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		MsgID:     1,
		ReadMaxID: 1,
	}); err != nil {
		t.Fatalf("messages.readDiscussion err = %v, want nil", err)
	}
	if _, err := r.onMessagesGetDiscussionMessage(WithUserID(ctx, owner.ID), &tg.MessagesGetDiscussionMessageRequest{
		Peer:  &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash + 1},
		MsgID: 1,
	}); err == nil || !strings.Contains(err.Error(), "CHANNEL_PRIVATE") {
		t.Fatalf("messages.getDiscussionMessage bad hash err = %v, want CHANNEL_PRIVATE", err)
	}
	if _, err := r.onMessagesReadDiscussion(WithUserID(ctx, owner.ID), &tg.MessagesReadDiscussionRequest{
		Peer:      &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		MsgID:     1,
		ReadMaxID: domain.MaxMessageBoxID + 1,
	}); err == nil || !strings.Contains(err.Error(), "MESSAGE_ID_INVALID") {
		t.Fatalf("messages.readDiscussion huge read_max_id err = %v, want MESSAGE_ID_INVALID", err)
	}
	if _, err := r.onMessagesGetForumTopics(WithUserID(ctx, owner.ID), &tg.MessagesGetForumTopicsRequest{
		Peer:  &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		Limit: 10,
	}); err == nil || !strings.Contains(err.Error(), "CHANNEL_FORUM_MISSING") {
		t.Fatalf("messages.getForumTopics non-forum err = %v, want CHANNEL_FORUM_MISSING", err)
	}
	if _, err := r.onMessagesGetForumTopicsByID(WithUserID(ctx, owner.ID), &tg.MessagesGetForumTopicsByIDRequest{
		Peer:   &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		Topics: []int{1},
	}); err == nil || !strings.Contains(err.Error(), "CHANNEL_FORUM_MISSING") {
		t.Fatalf("messages.getForumTopicsByID non-forum err = %v, want CHANNEL_FORUM_MISSING", err)
	}
	onlines, err := r.onMessagesGetOnlines(WithUserID(ctx, owner.ID), &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash})
	if err != nil {
		t.Fatalf("messages.getOnlines: %v", err)
	}
	if onlines.Onlines != 1 {
		t.Fatalf("messages.getOnlines = %+v, want count 1", onlines)
	}
	stats, err := r.onStatsGetMegagroupStats(WithUserID(ctx, owner.ID), &tg.StatsGetMegagroupStatsRequest{
		Channel: &tg.InputChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
	})
	if err != nil {
		t.Fatalf("stats.getMegagroupStats: %v", err)
	}
	if stats.GrowthGraph == nil || stats.MembersGraph == nil {
		t.Fatalf("megagroup stats = %+v, want non-nil graph stubs", stats)
	}
	if _, err := r.onStatsGetBroadcastStats(WithUserID(ctx, owner.ID), &tg.StatsGetBroadcastStatsRequest{
		Channel: &tg.InputChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
	}); err == nil || !strings.Contains(err.Error(), "BROADCAST_REQUIRED") {
		t.Fatalf("stats.getBroadcastStats on megagroup err = %v, want BROADCAST_REQUIRED", err)
	}
	messageStats, err := r.onStatsGetMessageStats(WithUserID(ctx, owner.ID), &tg.StatsGetMessageStatsRequest{
		Channel: &tg.InputChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		MsgID:   1,
	})
	if err != nil {
		t.Fatalf("stats.getMessageStats: %v", err)
	}
	if messageStats.ViewsGraph == nil || messageStats.ReactionsByEmotionGraph == nil {
		t.Fatalf("message stats = %+v, want non-nil graph stubs", messageStats)
	}
	forwards, err := r.onStatsGetMessagePublicForwards(WithUserID(ctx, owner.ID), &tg.StatsGetMessagePublicForwardsRequest{
		Channel: &tg.InputChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		MsgID:   1,
		Limit:   10,
	})
	if err != nil {
		t.Fatalf("stats.getMessagePublicForwards: %v", err)
	}
	if forwards.Count != 0 || len(forwards.Forwards) != 0 {
		t.Fatalf("message public forwards = %+v, want empty", forwards)
	}
	if _, err := r.onStatsGetMessagePublicForwards(WithUserID(ctx, owner.ID), &tg.StatsGetMessagePublicForwardsRequest{
		Channel: &tg.InputChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		MsgID:   1,
		Limit:   maxStatsPublicForwardsLimit + 1,
	}); err == nil || !strings.Contains(err.Error(), "LIMIT_INVALID") {
		t.Fatalf("stats.getMessagePublicForwards huge limit err = %v, want LIMIT_INVALID", err)
	}
	graph, err := r.onStatsLoadAsyncGraph(WithUserID(ctx, owner.ID), &tg.StatsLoadAsyncGraphRequest{Token: "stale"})
	if err != nil {
		t.Fatalf("stats.loadAsyncGraph: %v", err)
	}
	if _, ok := graph.(*tg.StatsGraphError); !ok {
		t.Fatalf("stats.loadAsyncGraph = %T, want statsGraphError", graph)
	}
	boostStatus, err := r.onPremiumGetBoostsStatus(WithUserID(ctx, owner.ID), &tg.InputPeerChannel{
		ChannelID:  channel.ID,
		AccessHash: channel.AccessHash,
	})
	if err != nil {
		t.Fatalf("premium.getBoostsStatus: %v", err)
	}
	if boostStatus.Level != 0 || boostStatus.Boosts != 0 {
		t.Fatalf("premium.getBoostsStatus = %+v, want zero status", boostStatus)
	}
	boosts, err := r.onPremiumGetBoostsList(WithUserID(ctx, owner.ID), &tg.PremiumGetBoostsListRequest{
		Peer:  &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		Limit: 10,
	})
	if err != nil {
		t.Fatalf("premium.getBoostsList: %v", err)
	}
	if boosts.Count != 0 || len(boosts.Boosts) != 0 || len(boosts.Users) != 0 {
		t.Fatalf("premium.getBoostsList = %+v, want empty", boosts)
	}
	if _, err := r.onPremiumGetBoostsList(WithUserID(ctx, owner.ID), &tg.PremiumGetBoostsListRequest{
		Peer:  &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		Limit: maxPremiumBoostsListLimit + 1,
	}); err == nil || !strings.Contains(err.Error(), "LIMIT_INVALID") {
		t.Fatalf("premium.getBoostsList huge limit err = %v, want LIMIT_INVALID", err)
	}
	myBoosts, err := r.onPremiumGetMyBoosts(WithUserID(ctx, owner.ID))
	if err != nil {
		t.Fatalf("premium.getMyBoosts: %v", err)
	}
	if len(myBoosts.MyBoosts) != 0 || len(myBoosts.Chats) != 0 || len(myBoosts.Users) != 0 {
		t.Fatalf("premium.getMyBoosts = %+v, want empty", myBoosts)
	}
	applyReq := &tg.PremiumApplyBoostRequest{
		Peer: &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
	}
	applyReq.SetSlots([]int{1})
	applied, err := r.onPremiumApplyBoost(WithUserID(ctx, owner.ID), applyReq)
	if err != nil {
		t.Fatalf("premium.applyBoost: %v", err)
	}
	if len(applied.MyBoosts) != 0 {
		t.Fatalf("premium.applyBoost = %+v, want empty noop", applied)
	}
	if _, err := r.onPremiumApplyBoost(WithUserID(ctx, owner.ID), &tg.PremiumApplyBoostRequest{
		Peer: &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
	}); err == nil || !strings.Contains(err.Error(), "BOOSTS_EMPTY") {
		t.Fatalf("premium.applyBoost missing slots err = %v, want BOOSTS_EMPTY", err)
	}
	userBoosts, err := r.onPremiumGetUserBoosts(WithUserID(ctx, owner.ID), &tg.PremiumGetUserBoostsRequest{
		Peer:   &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		UserID: &tg.InputUser{UserID: invited.ID, AccessHash: invited.AccessHash},
	})
	if err != nil {
		t.Fatalf("premium.getUserBoosts: %v", err)
	}
	if userBoosts.Count != 0 || len(userBoosts.Boosts) != 0 {
		t.Fatalf("premium.getUserBoosts = %+v, want empty", userBoosts)
	}
	if _, err := r.onMessagesAddChatUser(WithUserID(ctx, owner.ID), &tg.MessagesAddChatUserRequest{
		ChatID: channel.ID,
		UserID: &tg.InputUser{UserID: invited.ID, AccessHash: invited.AccessHash},
	}); err != nil {
		t.Fatalf("messages.addChatUser legacy wrapper: %v", err)
	}
	if _, err := r.onMessagesEditChatPhoto(WithUserID(ctx, owner.ID), &tg.MessagesEditChatPhotoRequest{
		ChatID: channel.ID,
		Photo:  &tg.InputChatPhotoEmpty{},
	}); err != nil {
		t.Fatalf("messages.editChatPhoto legacy wrapper: %v", err)
	}
	if _, err := r.onChannelsEditPhoto(WithUserID(ctx, owner.ID), &tg.ChannelsEditPhotoRequest{
		Channel: &tg.InputChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		Photo:   &tg.InputChatUploadedPhoto{},
	}); err == nil || !strings.Contains(err.Error(), "PHOTO_INVALID") {
		t.Fatalf("channels.editPhoto uploaded photo err = %v, want PHOTO_INVALID", err)
	}
	if _, err := r.onChannelsEditPhoto(WithUserID(ctx, owner.ID), &tg.ChannelsEditPhotoRequest{
		Channel: &tg.InputChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		Photo:   &tg.InputChatPhoto{ID: &tg.InputPhoto{ID: 1, AccessHash: 2, FileReference: []byte{3}}},
	}); err == nil || !strings.Contains(err.Error(), "PHOTO_INVALID") {
		t.Fatalf("channels.editPhoto existing photo err = %v, want PHOTO_INVALID", err)
	}
	if ok, err := r.onMessagesEditChatAdmin(WithUserID(ctx, owner.ID), &tg.MessagesEditChatAdminRequest{
		ChatID:  channel.ID,
		UserID:  &tg.InputUser{UserID: invited.ID, AccessHash: invited.AccessHash},
		IsAdmin: true,
	}); err != nil || !ok {
		t.Fatalf("messages.editChatAdmin legacy wrapper = %v, %v", ok, err)
	}
	if _, err := r.onMessagesEditChatParticipantRank(WithUserID(ctx, owner.ID), &tg.MessagesEditChatParticipantRankRequest{
		Peer:        &tg.InputPeerChat{ChatID: channel.ID},
		Participant: &tg.InputPeerUser{UserID: invited.ID, AccessHash: invited.AccessHash},
		Rank:        "ops",
	}); err != nil {
		t.Fatalf("messages.editChatParticipantRank legacy wrapper: %v", err)
	}
	if ok, err := r.onMessagesEditChatAbout(WithUserID(ctx, owner.ID), &tg.MessagesEditChatAboutRequest{
		Peer:  &tg.InputPeerChat{ChatID: channel.ID},
		About: "legacy about",
	}); err != nil || !ok {
		t.Fatalf("messages.editChatAbout legacy wrapper = %v, %v", ok, err)
	}
	fullAfterAbout, err := r.onMessagesGetFullChat(WithUserID(ctx, owner.ID), channel.ID)
	if err != nil {
		t.Fatalf("messages.getFullChat after editChatAbout: %v", err)
	}
	if got := fullAfterAbout.FullChat.(*tg.ChannelFull).About; got != "legacy about" {
		t.Fatalf("full chat about = %q, want legacy about", got)
	}
	if _, err := r.onMessagesEditChatCreator(WithUserID(ctx, owner.ID), &tg.MessagesEditChatCreatorRequest{
		Peer:   &tg.InputPeerChat{ChatID: channel.ID},
		UserID: &tg.InputUser{UserID: invited.ID, AccessHash: invited.AccessHash},
	}); err == nil {
		t.Fatalf("messages.editChatCreator = nil error, want explicit password/2FA error")
	}
	permUpdates, err := r.onMessagesEditChatDefaultBannedRights(WithUserID(ctx, owner.ID), &tg.MessagesEditChatDefaultBannedRightsRequest{
		Peer: &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		BannedRights: tg.ChatBannedRights{
			SendMessages: true,
		},
	})
	if err != nil {
		t.Fatalf("messages.editChatDefaultBannedRights: %v", err)
	}
	if len(permUpdates.(*tg.Updates).Updates) == 0 {
		t.Fatalf("messages.editChatDefaultBannedRights updates = %+v, want updateChannel", permUpdates)
	}
	if _, err := r.onMessagesDeleteChatUser(WithUserID(ctx, owner.ID), &tg.MessagesDeleteChatUserRequest{
		ChatID: channel.ID,
		UserID: &tg.InputUser{UserID: invited.ID, AccessHash: invited.AccessHash},
	}); err != nil {
		t.Fatalf("messages.deleteChatUser legacy wrapper: %v", err)
	}
}

func incrementalViewCount(result *tg.MessagesMessageViews) (int, bool) {
	if result == nil || len(result.Views) == 0 {
		return 0, false
	}
	return result.Views[0].GetViews()
}

func TestChannelAdminPinInviteRPC(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, _ := userStore.Create(ctx, domain.User{AccessHash: 51, Phone: "15550002201", FirstName: "Owner"})
	friend, _ := userStore.Create(ctx, domain.User{AccessHash: 52, Phone: "15550002202", FirstName: "Friend"})
	joiner, _ := userStore.Create(ctx, domain.User{AccessHash: 53, Phone: "15550002203", FirstName: "Joiner"})
	invited, _ := userStore.Create(ctx, domain.User{AccessHash: 54, Phone: "15550002204", FirstName: "Invited"})
	channelStore := memory.NewChannelStore()
	r := New(Config{}, Deps{
		Users:    appusers.NewService(userStore),
		Channels: appchannels.NewService(channelStore),
	}, zaptest.NewLogger(t), clock.System)
	created, err := r.onMessagesCreateChat(WithUserID(ctx, owner.ID), &tg.MessagesCreateChatRequest{
		Users: []tg.InputUserClass{&tg.InputUser{UserID: friend.ID, AccessHash: friend.AccessHash}},
		Title: "RPC Admin Group",
	})
	if err != nil {
		t.Fatalf("create chat: %v", err)
	}
	channel := created.Updates.(*tg.Updates).Chats[0].(*tg.Channel)
	createdChannel, err := channelStore.GetChannelByID(ctx, channel.ID)
	if err != nil {
		t.Fatalf("get created channel: %v", err)
	}
	initialChannelPts := createdChannel.Pts

	selfParticipant, err := r.onChannelsGetParticipant(WithUserID(ctx, friend.ID), &tg.ChannelsGetParticipantRequest{
		Channel:     &tg.InputChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		Participant: &tg.InputPeerSelf{},
	})
	if err != nil {
		t.Fatalf("get self regular participant: %v", err)
	}
	if _, ok := selfParticipant.Participant.(*tg.ChannelParticipantSelf); !ok {
		t.Fatalf("self regular participant = %T, want channelParticipantSelf", selfParticipant.Participant)
	}
	recentForFriend, err := r.onChannelsGetParticipants(WithUserID(ctx, friend.ID), &tg.ChannelsGetParticipantsRequest{
		Channel: &tg.InputChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		Filter:  &tg.ChannelParticipantsRecent{},
		Limit:   10,
	})
	if err != nil {
		t.Fatalf("get recent participants for regular self: %v", err)
	}
	foundSelf := false
	for _, participant := range recentForFriend.(*tg.ChannelsChannelParticipants).Participants {
		if _, ok := participant.(*tg.ChannelParticipantSelf); ok {
			foundSelf = true
			break
		}
	}
	if !foundSelf {
		t.Fatalf("recent participants = %+v, want current regular member as channelParticipantSelf", recentForFriend.(*tg.ChannelsChannelParticipants).Participants)
	}

	adminUpdates, err := r.onChannelsEditAdmin(WithUserID(ctx, owner.ID), &tg.ChannelsEditAdminRequest{
		Channel: &tg.InputChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		UserID:  &tg.InputUser{UserID: friend.ID, AccessHash: friend.AccessHash},
		AdminRights: tg.ChatAdminRights{
			ChangeInfo:  true,
			InviteUsers: true,
			PinMessages: true,
		},
		Rank: "ops",
	})
	if err != nil {
		t.Fatalf("edit admin: %v", err)
	}
	if updates := adminUpdates.(*tg.Updates); len(updates.Updates) != 2 {
		t.Fatalf("admin updates = %+v, want participant update and channel refresh", updates.Updates)
	} else if _, ok := updates.Updates[0].(*tg.UpdateChannelParticipant); !ok {
		t.Fatalf("admin update[0] = %T, want updateChannelParticipant", updates.Updates[0])
	} else if _, ok := updates.Updates[1].(*tg.UpdateChannel); !ok {
		t.Fatalf("admin update[1] = %T, want updateChannel", updates.Updates[1])
	}
	adminDiff, err := r.onUpdatesGetChannelDifference(WithUserID(ctx, friend.ID), &tg.UpdatesGetChannelDifferenceRequest{
		Channel: &tg.InputChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		Filter:  &tg.ChannelMessagesFilterEmpty{},
		Pts:     initialChannelPts,
		Limit:   10,
	})
	if err != nil {
		t.Fatalf("channel difference after admin: %v", err)
	}
	adminEmptyDiff, ok := adminDiff.(*tg.UpdatesChannelDifferenceEmpty)
	if !ok || !adminEmptyDiff.Final || adminEmptyDiff.Pts != initialChannelPts {
		t.Fatalf("admin diff = %T %+v, want empty difference at unchanged pts %d", adminDiff, adminDiff, initialChannelPts)
	}
	admins, err := r.onChannelsGetParticipants(WithUserID(ctx, owner.ID), &tg.ChannelsGetParticipantsRequest{
		Channel: &tg.InputChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		Filter:  &tg.ChannelParticipantsAdmins{},
		Limit:   10,
	})
	if err != nil {
		t.Fatalf("get admin participants: %v", err)
	}
	if list := admins.(*tg.ChannelsChannelParticipants); len(list.Participants) != 2 {
		t.Fatalf("admin participants = %+v, want creator and promoted admin", list.Participants)
	}

	titleUpdates, err := r.onChannelsEditTitle(WithUserID(ctx, friend.ID), &tg.ChannelsEditTitleRequest{
		Channel: &tg.InputChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		Title:   "RPC Admin Group 2",
	})
	if err != nil {
		t.Fatalf("edit title: %v", err)
	}
	titleContainer := titleUpdates.(*tg.Updates)
	if len(titleContainer.Updates) < 2 {
		t.Fatalf("title updates = %+v, want channel + service message", titleContainer.Updates)
	}
	titleMsg, ok := titleContainer.Updates[1].(*tg.UpdateNewChannelMessage)
	if !ok {
		t.Fatalf("title update[1] = %T, want updateNewChannelMessage", titleContainer.Updates[1])
	}
	if action := titleMsg.Message.(*tg.MessageService).Action; action.(*tg.MessageActionChatEditTitle).Title != "RPC Admin Group 2" {
		t.Fatalf("title action = %#v, want new title", action)
	}

	sent, err := r.onMessagesSendMessage(WithUserID(ctx, owner.ID), &tg.MessagesSendMessageRequest{
		Peer:     &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		Message:  "pin me",
		RandomID: 123,
	})
	if err != nil {
		t.Fatalf("send for pin: %v", err)
	}
	msgID := sent.(*tg.Updates).Updates[0].(*tg.UpdateMessageID).ID
	pinUpdates, err := r.onMessagesUpdatePinnedMessage(WithUserID(ctx, friend.ID), &tg.MessagesUpdatePinnedMessageRequest{
		Peer: &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		ID:   msgID,
	})
	if err != nil {
		t.Fatalf("pin message: %v", err)
	}
	pinned, ok := pinUpdates.(*tg.Updates).Updates[0].(*tg.UpdatePinnedChannelMessages)
	if !ok || !pinned.Pinned || pinned.Messages[0] != msgID {
		t.Fatalf("pin update = %#v, want pinned channel message id=%d", pinUpdates.(*tg.Updates).Updates[0], msgID)
	}

	invitedUsers, err := r.onChannelsInviteToChannel(WithUserID(ctx, friend.ID), &tg.ChannelsInviteToChannelRequest{
		Channel: &tg.InputChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		Users:   []tg.InputUserClass{&tg.InputUser{UserID: invited.ID, AccessHash: invited.AccessHash}},
	})
	if err != nil {
		t.Fatalf("invite to channel: %v", err)
	}
	if invitedUsers.Updates == nil || len(invitedUsers.MissingInvitees) != 0 {
		t.Fatalf("invited users = %+v, want updates and no missing users", invitedUsers)
	}

	invite, err := r.onMessagesExportChatInvite(WithUserID(ctx, friend.ID), &tg.MessagesExportChatInviteRequest{
		Peer:  &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		Title: "join",
	})
	if err != nil {
		t.Fatalf("export invite: %v", err)
	}
	exported := invite.(*tg.ChatInviteExported)
	hash := strings.TrimPrefix(exported.Link, "https://t.me/+")
	checked, err := r.onMessagesCheckChatInvite(WithUserID(ctx, joiner.ID), hash)
	if err != nil {
		t.Fatalf("check invite: %v", err)
	}
	if preview, ok := checked.(*tg.ChatInvite); !ok || !preview.Megagroup || preview.Title != "RPC Admin Group 2" {
		t.Fatalf("invite preview = %#v, want megagroup title", checked)
	}
	imported, err := r.onMessagesImportChatInvite(WithUserID(ctx, joiner.ID), hash)
	if err != nil {
		t.Fatalf("import invite: %v", err)
	}
	if updates := imported.(*tg.Updates); len(updates.Chats) != 1 || len(updates.Updates) != 2 {
		t.Fatalf("import updates = %+v, want chat, join service update, and channel refresh", updates)
	} else if _, ok := updates.Updates[0].(*tg.UpdateNewChannelMessage); !ok {
		t.Fatalf("import first update = %T, want join service update", updates.Updates[0])
	} else if refresh, ok := updates.Updates[1].(*tg.UpdateChannel); !ok || refresh.ChannelID != channel.ID {
		t.Fatalf("import second update = %#v, want channel refresh", updates.Updates[1])
	}
	inviteList, err := r.onMessagesGetExportedChatInvites(WithUserID(ctx, friend.ID), &tg.MessagesGetExportedChatInvitesRequest{
		Peer:    &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		AdminID: &tg.InputUserSelf{},
		Limit:   10,
	})
	if err != nil {
		t.Fatalf("get exported invites: %v", err)
	}
	if inviteList.Count != 1 || len(inviteList.Invites) != 1 || len(inviteList.Users) == 0 {
		t.Fatalf("exported invite list = %+v, want persisted invite plus admin user context", inviteList)
	}
	listedInvite, ok := inviteList.Invites[0].(*tg.ChatInviteExported)
	listedUsage, listedUsageOK := listedInvite.GetUsage()
	listedTitle, listedTitleOK := listedInvite.GetTitle()
	if !ok || listedInvite.Link != exported.Link || !listedUsageOK || listedUsage != 1 || !listedTitleOK || listedTitle != "join" {
		t.Fatalf("listed invite = %#v, want exported link with one import", inviteList.Invites[0])
	}
	if _, err := r.onMessagesGetExportedChatInvites(WithUserID(ctx, friend.ID), &tg.MessagesGetExportedChatInvitesRequest{
		Peer:    &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		AdminID: &tg.InputUserSelf{},
		Limit:   101,
	}); err == nil || !strings.Contains(err.Error(), "LIMIT_INVALID") {
		t.Fatalf("get exported invites high limit err = %v, want LIMIT_INVALID", err)
	}
	inviteDetails, err := r.onMessagesGetExportedChatInvite(WithUserID(ctx, friend.ID), &tg.MessagesGetExportedChatInviteRequest{
		Peer: &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		Link: exported.Link,
	})
	if err != nil {
		t.Fatalf("get exported invite: %v", err)
	}
	if details := inviteDetails.(*tg.MessagesExportedChatInvite); details.Invite == nil || len(details.Users) == 0 {
		t.Fatalf("exported invite details = %+v, want invite plus user context", inviteDetails)
	}
	editInviteReq := &tg.MessagesEditExportedChatInviteRequest{
		Peer: &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		Link: exported.Link,
	}
	editInviteReq.SetTitle("ops link")
	editedInvite, err := r.onMessagesEditExportedChatInvite(WithUserID(ctx, friend.ID), editInviteReq)
	if err != nil {
		t.Fatalf("edit exported invite: %v", err)
	}
	if edited := editedInvite.(*tg.MessagesExportedChatInvite); edited.Invite == nil || len(edited.Users) == 0 {
		t.Fatalf("edited invite = %+v, want invite plus user context", editedInvite)
	} else if got, ok := edited.Invite.(*tg.ChatInviteExported).GetTitle(); !ok || got != "ops link" {
		t.Fatalf("edited invite title = %q, want ops link", got)
	}
	adminsWithInvites, err := r.onMessagesGetAdminsWithInvites(WithUserID(ctx, friend.ID), &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash})
	if err != nil {
		t.Fatalf("get admins with invites: %v", err)
	}
	if len(adminsWithInvites.Admins) != 1 || adminsWithInvites.Admins[0].AdminID != friend.ID || adminsWithInvites.Admins[0].InvitesCount != 1 || len(adminsWithInvites.Users) == 0 {
		t.Fatalf("admins with invites = %+v, want self admin context with one invite", adminsWithInvites)
	}
	importers, err := r.onMessagesGetChatInviteImporters(WithUserID(ctx, friend.ID), &tg.MessagesGetChatInviteImportersRequest{
		Peer:  &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		Limit: 10,
	})
	if err != nil {
		t.Fatalf("get invite importers: %v", err)
	}
	if importers.Count != 1 || len(importers.Importers) != 1 || importers.Importers[0].UserID != joiner.ID || len(importers.Users) == 0 {
		t.Fatalf("invite importers = %+v, want joined importer", importers)
	}
	importersSearchReq := &tg.MessagesGetChatInviteImportersRequest{
		Peer:  &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		Limit: 10,
	}
	importersSearchReq.SetLink(exported.Link)
	importersSearchReq.SetQ("bob")
	if _, err := r.onMessagesGetChatInviteImporters(WithUserID(ctx, friend.ID), importersSearchReq); err == nil || !strings.Contains(err.Error(), "SEARCH_WITH_LINK_NOT_SUPPORTED") {
		t.Fatalf("get invite importers q+link err = %v, want SEARCH_WITH_LINK_NOT_SUPPORTED", err)
	}
	if _, err := r.onMessagesHideChatJoinRequest(WithUserID(ctx, friend.ID), &tg.MessagesHideChatJoinRequestRequest{
		Approved: true,
		Peer:     &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		UserID:   &tg.InputUser{UserID: invited.ID, AccessHash: invited.AccessHash},
	}); err == nil || !strings.Contains(err.Error(), "HIDE_REQUESTER_MISSING") {
		t.Fatalf("hide chat join request without pending err = %v, want HIDE_REQUESTER_MISSING", err)
	}
	if updates, err := r.onMessagesHideAllChatJoinRequests(WithUserID(ctx, friend.ID), &tg.MessagesHideAllChatJoinRequestsRequest{
		Approved: false,
		Peer:     &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
	}); err != nil {
		t.Fatalf("hide all chat join requests: %v", err)
	} else if _, ok := updates.(*tg.Updates); !ok {
		t.Fatalf("hide all chat join requests updates = %T, want *tg.Updates", updates)
	}
	if ok, err := r.onMessagesDeleteExportedChatInvite(WithUserID(ctx, friend.ID), &tg.MessagesDeleteExportedChatInviteRequest{
		Peer: &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		Link: exported.Link,
	}); err != nil || !ok {
		t.Fatalf("delete exported invite ok=%v err=%v, want true nil", ok, err)
	}
	if ok, err := r.onMessagesDeleteRevokedExportedChatInvites(WithUserID(ctx, friend.ID), &tg.MessagesDeleteRevokedExportedChatInvitesRequest{
		Peer:    &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		AdminID: &tg.InputUserSelf{},
	}); err != nil || !ok {
		t.Fatalf("delete revoked invites ok=%v err=%v, want true nil", ok, err)
	}
	adminLog, err := r.onChannelsGetAdminLog(WithUserID(ctx, owner.ID), &tg.ChannelsGetAdminLogRequest{
		Channel: &tg.InputChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		Limit:   20,
	})
	if err != nil {
		t.Fatalf("get admin log: %v", err)
	}
	if len(adminLog.Events) < 5 || len(adminLog.Chats) != 1 || len(adminLog.Users) < 3 {
		t.Fatalf("admin log = %+v, want events plus chat/users", adminLog)
	}
	tooManyAdmins := make([]tg.InputUserClass, domain.MaxChannelAdminLogAdmins+1)
	for i := range tooManyAdmins {
		tooManyAdmins[i] = &tg.InputUser{UserID: owner.ID, AccessHash: owner.AccessHash}
	}
	tooManyAdminsReq := &tg.ChannelsGetAdminLogRequest{
		Channel: &tg.InputChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		Limit:   1,
	}
	tooManyAdminsReq.SetAdmins(tooManyAdmins)
	if _, err := r.onChannelsGetAdminLog(WithUserID(ctx, owner.ID), tooManyAdminsReq); err == nil || !strings.Contains(err.Error(), "LIMIT_INVALID") {
		t.Fatalf("get admin log too many admins err = %v, want LIMIT_INVALID", err)
	}
	pinnedFilter := tg.ChannelAdminLogEventsFilter{}
	pinnedFilter.SetPinned(true)
	pinnedReq := &tg.ChannelsGetAdminLogRequest{
		Channel: &tg.InputChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		Limit:   10,
	}
	pinnedReq.SetEventsFilter(pinnedFilter)
	pinnedLog, err := r.onChannelsGetAdminLog(WithUserID(ctx, owner.ID), pinnedReq)
	if err != nil {
		t.Fatalf("get pinned admin log: %v", err)
	}
	if len(pinnedLog.Events) != 1 {
		t.Fatalf("pinned admin log events = %+v, want one", pinnedLog.Events)
	}
	if _, ok := pinnedLog.Events[0].Action.(*tg.ChannelAdminLogEventActionUpdatePinned); !ok {
		t.Fatalf("pinned admin log action = %T, want updatePinned", pinnedLog.Events[0].Action)
	}
	unpinnedAll, err := r.onMessagesUnpinAllMessages(WithUserID(ctx, friend.ID), &tg.MessagesUnpinAllMessagesRequest{
		Peer: &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
	})
	if err != nil {
		t.Fatalf("unpin all messages: %v", err)
	}
	if unpinnedAll.Pts == 0 || unpinnedAll.PtsCount != 1 || unpinnedAll.Offset != 0 {
		t.Fatalf("unpin all affected history = %+v, want one channel pts event", unpinnedAll)
	}
	afterUnpin, err := r.deps.Channels.GetChannel(ctx, friend.ID, channel.ID)
	if err != nil {
		t.Fatalf("get channel after unpin: %v", err)
	}
	if afterUnpin.Channel.PinnedMessageID != 0 {
		t.Fatalf("pinned message after unpin all = %d, want 0", afterUnpin.Channel.PinnedMessageID)
	}
	unpinnedAgain, err := r.onMessagesUnpinAllMessages(WithUserID(ctx, friend.ID), &tg.MessagesUnpinAllMessagesRequest{
		Peer: &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
	})
	if err != nil {
		t.Fatalf("unpin all messages again: %v", err)
	}
	if unpinnedAgain.Pts != afterUnpin.Channel.Pts || unpinnedAgain.PtsCount != 0 || unpinnedAgain.Offset != 0 {
		t.Fatalf("unpin all no-op affected history = %+v, want current pts with zero pts_count", unpinnedAgain)
	}
	invalidTopicUnpin := &tg.MessagesUnpinAllMessagesRequest{
		Peer: &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
	}
	invalidTopicUnpin.SetTopMsgID(domain.MaxMessageBoxID + 1)
	if _, err := r.onMessagesUnpinAllMessages(WithUserID(ctx, friend.ID), invalidTopicUnpin); err == nil || !strings.Contains(err.Error(), "MESSAGE_ID_INVALID") {
		t.Fatalf("unpin all invalid top msg err = %v, want MESSAGE_ID_INVALID", err)
	}
}

func TestChannelInviteKickedMemberRPC(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, _ := userStore.Create(ctx, domain.User{AccessHash: 55, Phone: "15550002255", FirstName: "Owner"})
	helper, _ := userStore.Create(ctx, domain.User{AccessHash: 56, Phone: "15550002256", FirstName: "Helper"})
	kicked, _ := userStore.Create(ctx, domain.User{AccessHash: 57, Phone: "15550002257", FirstName: "Kicked"})
	channelStore := memory.NewChannelStore()
	r := New(Config{}, Deps{
		Users:    appusers.NewService(userStore),
		Channels: appchannels.NewService(channelStore),
	}, zaptest.NewLogger(t), clock.System)
	created, err := r.onMessagesCreateChat(WithUserID(ctx, owner.ID), &tg.MessagesCreateChatRequest{
		Users: []tg.InputUserClass{
			&tg.InputUser{UserID: helper.ID, AccessHash: helper.AccessHash},
			&tg.InputUser{UserID: kicked.ID, AccessHash: kicked.AccessHash},
		},
		Title: "RPC Invite Kicked",
	})
	if err != nil {
		t.Fatalf("create chat: %v", err)
	}
	channel := created.Updates.(*tg.Updates).Chats[0].(*tg.Channel)
	input := &tg.InputChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash}
	if _, err := r.onChannelsEditBanned(WithUserID(ctx, owner.ID), &tg.ChannelsEditBannedRequest{
		Channel:     input,
		Participant: &tg.InputPeerUser{UserID: kicked.ID, AccessHash: kicked.AccessHash},
		BannedRights: tg.ChatBannedRights{
			ViewMessages: true,
		},
	}); err != nil {
		t.Fatalf("kick member: %v", err)
	}
	if _, err := r.onChannelsInviteToChannel(WithUserID(ctx, helper.ID), &tg.ChannelsInviteToChannelRequest{
		Channel: input,
		Users:   []tg.InputUserClass{&tg.InputUser{UserID: kicked.ID, AccessHash: kicked.AccessHash}},
	}); err == nil || !strings.Contains(err.Error(), "USER_KICKED") {
		t.Fatalf("helper invite kicked err = %v, want USER_KICKED", err)
	}
	if _, err := r.onChannelsInviteToChannel(WithUserID(ctx, owner.ID), &tg.ChannelsInviteToChannelRequest{
		Channel: input,
		Users:   []tg.InputUserClass{&tg.InputUser{UserID: kicked.ID, AccessHash: kicked.AccessHash}},
	}); err != nil {
		t.Fatalf("owner restore kicked invite: %v", err)
	}
	if _, err := r.onChannelsInviteToChannel(WithUserID(ctx, owner.ID), &tg.ChannelsInviteToChannelRequest{
		Channel: input,
		Users:   []tg.InputUserClass{&tg.InputUser{UserID: kicked.ID, AccessHash: kicked.AccessHash}},
	}); err == nil || !strings.Contains(err.Error(), "USER_ALREADY_PARTICIPANT") {
		t.Fatalf("duplicate invite err = %v, want USER_ALREADY_PARTICIPANT", err)
	}
}

func TestImportChatInviteErrorsRPC(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, _ := userStore.Create(ctx, domain.User{AccessHash: 58, Phone: "15550002258", FirstName: "Owner"})
	first, _ := userStore.Create(ctx, domain.User{AccessHash: 59, Phone: "15550002259", FirstName: "First"})
	second, _ := userStore.Create(ctx, domain.User{AccessHash: 60, Phone: "15550002260", FirstName: "Second"})
	channelStore := memory.NewChannelStore()
	sessions := &captureSessions{}
	r := New(Config{}, Deps{
		Users:    appusers.NewService(userStore),
		Channels: appchannels.NewService(channelStore),
		Sessions: sessions,
	}, zaptest.NewLogger(t), clock.System)
	created, err := r.onChannelsCreateChannel(WithUserID(ctx, owner.ID), &tg.ChannelsCreateChannelRequest{
		Title:     "RPC Import Errors",
		Megagroup: true,
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	channel := created.(*tg.Updates).Chats[0].(*tg.Channel)
	input := &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash}
	inputChannel := &tg.InputChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash}
	requestInvite, err := r.onMessagesExportChatInvite(WithUserID(ctx, owner.ID), &tg.MessagesExportChatInviteRequest{
		Peer:          input,
		Title:         "approval",
		RequestNeeded: true,
	})
	if err != nil {
		t.Fatalf("export request-needed invite: %v", err)
	}
	requestHash := strings.TrimPrefix(requestInvite.(*tg.ChatInviteExported).Link, "https://t.me/+")
	if _, err := r.onMessagesImportChatInvite(WithUserID(ctx, first.ID), requestHash); err == nil || !strings.Contains(err.Error(), "INVITE_REQUEST_SENT") {
		t.Fatalf("import request-needed err = %v, want INVITE_REQUEST_SENT", err)
	}
	pushedPending := sessions.snapshot()
	if pushedPending.userID != owner.ID || pushedPending.messageType != proto.MessageFromServer {
		t.Fatalf("pending request push = %+v, want owner server update", pushedPending)
	}
	pushedPendingUpdates, ok := pushedPending.message.(*tg.Updates)
	if !ok || len(pushedPendingUpdates.Updates) != 1 {
		t.Fatalf("pending request push message = %T %+v, want one update", pushedPending.message, pushedPending.message)
	}
	pushedPendingUpdate, ok := pushedPendingUpdates.Updates[0].(*tg.UpdatePendingJoinRequests)
	if !ok || pushedPendingUpdate.RequestsPending != 1 || len(pushedPendingUpdate.RecentRequesters) != 1 || pushedPendingUpdate.RecentRequesters[0] != first.ID {
		t.Fatalf("pending request update = %+v, want first requester", pushedPendingUpdates.Updates[0])
	}
	fullAfterPending, err := r.onChannelsGetFullChannel(WithUserID(ctx, owner.ID), inputChannel)
	if err != nil {
		t.Fatalf("get full channel after pending request: %v", err)
	}
	fullPending := fullAfterPending.FullChat.(*tg.ChannelFull)
	requestsPending, ok := fullPending.GetRequestsPending()
	recentRequesters, recentOK := fullPending.GetRecentRequesters()
	if !ok || requestsPending != 1 || !recentOK || len(recentRequesters) != 1 || recentRequesters[0] != first.ID {
		t.Fatalf("full pending = count %d ok %v recent %+v ok %v, want first requester", requestsPending, ok, recentRequesters, recentOK)
	}
	pendingReq := &tg.MessagesGetChatInviteImportersRequest{
		Requested: true,
		Peer:      input,
		Limit:     10,
	}
	pendingReq.SetLink(requestInvite.(*tg.ChatInviteExported).Link)
	pending, err := r.onMessagesGetChatInviteImporters(WithUserID(ctx, owner.ID), pendingReq)
	if err != nil {
		t.Fatalf("get pending invite importers: %v", err)
	}
	if pending.Count != 1 || len(pending.Importers) != 1 || pending.Importers[0].UserID != first.ID || !pending.Importers[0].Requested {
		t.Fatalf("pending importers = %+v, want first pending request", pending)
	}
	limitedInvite, err := r.onMessagesExportChatInvite(WithUserID(ctx, owner.ID), &tg.MessagesExportChatInviteRequest{
		Peer:       input,
		Title:      "one",
		UsageLimit: 1,
	})
	if err != nil {
		t.Fatalf("export limited invite: %v", err)
	}
	limitedHash := strings.TrimPrefix(limitedInvite.(*tg.ChatInviteExported).Link, "https://t.me/+")
	if _, err := r.onMessagesImportChatInvite(WithUserID(ctx, first.ID), limitedHash); err != nil {
		t.Fatalf("first import limited invite: %v", err)
	}
	pendingAfterJoin, err := r.onMessagesGetChatInviteImporters(WithUserID(ctx, owner.ID), pendingReq)
	if err != nil {
		t.Fatalf("get pending invite importers after join: %v", err)
	}
	if pendingAfterJoin.Count != 0 || len(pendingAfterJoin.Importers) != 0 {
		t.Fatalf("pending importers after alternate invite join = %+v, want cleared", pendingAfterJoin)
	}
	if _, err := r.onMessagesImportChatInvite(WithUserID(ctx, second.ID), limitedHash); err == nil || !strings.Contains(err.Error(), "USERS_TOO_MUCH") {
		t.Fatalf("second import limited err = %v, want USERS_TOO_MUCH", err)
	}
	if _, err := r.onMessagesImportChatInvite(WithUserID(ctx, second.ID), requestHash); err == nil || !strings.Contains(err.Error(), "INVITE_REQUEST_SENT") {
		t.Fatalf("second import request-needed err = %v, want INVITE_REQUEST_SENT", err)
	}
	pendingSecond, err := r.onMessagesGetChatInviteImporters(WithUserID(ctx, owner.ID), pendingReq)
	if err != nil {
		t.Fatalf("get second pending invite importers: %v", err)
	}
	if pendingSecond.Count != 1 || len(pendingSecond.Importers) != 1 || pendingSecond.Importers[0].UserID != second.ID || !pendingSecond.Importers[0].Requested {
		t.Fatalf("second pending importers = %+v, want second pending request", pendingSecond)
	}
	approved, err := r.onMessagesHideChatJoinRequest(WithUserID(ctx, owner.ID), &tg.MessagesHideChatJoinRequestRequest{
		Approved: true,
		Peer:     input,
		UserID:   &tg.InputUser{UserID: second.ID, AccessHash: second.AccessHash},
	})
	if err != nil {
		t.Fatalf("approve chat join request: %v", err)
	}
	if updates := approved.(*tg.Updates); len(updates.Chats) != 1 || len(updates.Updates) == 0 {
		t.Fatalf("approve join request updates = %+v, want channel updates", updates)
	}
	approvedUpdates := approved.(*tg.Updates)
	var pendingCleared *tg.UpdatePendingJoinRequests
	for _, update := range approvedUpdates.Updates {
		if pending, ok := update.(*tg.UpdatePendingJoinRequests); ok {
			pendingCleared = pending
			break
		}
	}
	if pendingCleared == nil || pendingCleared.RequestsPending != 0 || len(pendingCleared.RecentRequesters) != 0 {
		t.Fatalf("approve pending update = %+v, want cleared pending requests", pendingCleared)
	}
	if _, err := r.onMessagesHideChatJoinRequest(WithUserID(ctx, owner.ID), &tg.MessagesHideChatJoinRequestRequest{
		Approved: true,
		Peer:     input,
		UserID:   &tg.InputUser{UserID: second.ID, AccessHash: second.AccessHash},
	}); err == nil || !strings.Contains(err.Error(), "HIDE_REQUESTER_MISSING") {
		t.Fatalf("approve missing join request err = %v, want HIDE_REQUESTER_MISSING", err)
	}
}

func TestChannelUsernameAndManagementRPC(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, _ := userStore.Create(ctx, domain.User{AccessHash: 61, Phone: "15550002301", FirstName: "Owner"})
	requester, _ := userStore.Create(ctx, domain.User{AccessHash: 62, Phone: "15550002302", FirstName: "Requester"})
	channelStore := memory.NewChannelStore()
	r := New(Config{}, Deps{
		Users:    appusers.NewService(userStore),
		Channels: appchannels.NewService(channelStore),
	}, zaptest.NewLogger(t), clock.System)
	created, err := r.onChannelsCreateChannel(WithUserID(ctx, owner.ID), &tg.ChannelsCreateChannelRequest{
		Title:     "Public Team",
		About:     "public",
		Megagroup: true,
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	channel := created.(*tg.Updates).Chats[0].(*tg.Channel)
	input := &tg.InputChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash}
	sent, err := r.onMessagesSendMessage(WithUserID(ctx, owner.ID), &tg.MessagesSendMessageRequest{
		Peer:     &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		Message:  "channel management seed",
		RandomID: 991,
	})
	if err != nil {
		t.Fatalf("send seed message: %v", err)
	}
	msgID := sent.(*tg.Updates).Updates[0].(*tg.UpdateMessageID).ID

	okUsername, err := r.onChannelsCheckUsername(WithUserID(ctx, owner.ID), &tg.ChannelsCheckUsernameRequest{
		Channel:  input,
		Username: "public_team",
	})
	if err != nil || !okUsername {
		t.Fatalf("check username = ok %v err %v, want true", okUsername, err)
	}
	updated, err := r.onChannelsUpdateUsername(WithUserID(ctx, owner.ID), &tg.ChannelsUpdateUsernameRequest{
		Channel:  input,
		Username: "public_team",
	})
	if err != nil || !updated {
		t.Fatalf("update username = %v err %v, want true", updated, err)
	}
	admined, err := r.onChannelsGetAdminedPublicChannels(WithUserID(ctx, owner.ID), &tg.ChannelsGetAdminedPublicChannelsRequest{})
	if err != nil {
		t.Fatalf("get admined public channels: %v", err)
	}
	adminedChats := admined.(*tg.MessagesChats).Chats
	if len(adminedChats) != 1 || adminedChats[0].(*tg.Channel).Username != "public_team" {
		t.Fatalf("admined chats = %+v, want public channel with username", adminedChats)
	}

	signatures, err := r.onChannelsToggleSignatures(WithUserID(ctx, owner.ID), &tg.ChannelsToggleSignaturesRequest{
		Channel:           input,
		SignaturesEnabled: true,
	})
	if err != nil {
		t.Fatalf("toggle signatures: %v", err)
	}
	if signed := signatures.(*tg.Updates).Chats[0].(*tg.Channel); !signed.Signatures {
		t.Fatalf("signed channel = %+v, want signatures enabled", signed)
	}
	if _, err := r.onChannelsToggleSlowMode(WithUserID(ctx, owner.ID), &tg.ChannelsToggleSlowModeRequest{Channel: input, Seconds: 7}); err == nil {
		t.Fatalf("toggle slow mode invalid seconds err = nil, want SECONDS_INVALID")
	}
	slowUpdates, err := r.onChannelsToggleSlowMode(WithUserID(ctx, owner.ID), &tg.ChannelsToggleSlowModeRequest{Channel: input, Seconds: 30})
	if err != nil {
		t.Fatalf("toggle slow mode valid: %v", err)
	}
	if slowChannel := slowUpdates.(*tg.Updates).Chats[0].(*tg.Channel); !slowChannel.SlowmodeEnabled {
		t.Fatalf("slow mode channel = %+v, want slowmode enabled", slowChannel)
	}
	if ok, err := r.onChannelsSetStickers(WithUserID(ctx, owner.ID), &tg.ChannelsSetStickersRequest{Channel: input, Stickerset: &tg.InputStickerSetEmpty{}}); err != nil || !ok {
		t.Fatalf("set stickers = ok %v err %v, want true", ok, err)
	}
	if _, err := r.onChannelsSetStickers(WithUserID(ctx, owner.ID), &tg.ChannelsSetStickersRequest{
		Channel:    input,
		Stickerset: &tg.InputStickerSetID{ID: 1, AccessHash: 2},
	}); err == nil || !strings.Contains(err.Error(), "STICKERSET_INVALID") {
		t.Fatalf("set stickers non-empty err = %v, want STICKERSET_INVALID", err)
	}
	if ok, err := r.onChannelsReorderUsernames(WithUserID(ctx, owner.ID), &tg.ChannelsReorderUsernamesRequest{Channel: input, Order: []string{"public_team"}}); err != nil || !ok {
		t.Fatalf("reorder usernames = ok %v err %v, want true", ok, err)
	}
	if _, err := r.onChannelsTogglePreHistoryHidden(WithUserID(ctx, owner.ID), &tg.ChannelsTogglePreHistoryHiddenRequest{Channel: input, Enabled: true}); err != nil {
		t.Fatalf("toggle prehistory hidden: %v", err)
	}
	fullAfterSettings, err := r.onChannelsGetFullChannel(WithUserID(ctx, owner.ID), input)
	if err != nil {
		t.Fatalf("get full channel after settings: %v", err)
	}
	fullSettings := fullAfterSettings.FullChat.(*tg.ChannelFull)
	if !fullSettings.HiddenPrehistory || fullSettings.SlowmodeSeconds != 30 {
		t.Fatalf("full settings = %+v, want hidden prehistory and slowmode=30", fullSettings)
	}
	joinToSendUpdates, err := r.onChannelsToggleJoinToSend(WithUserID(ctx, owner.ID), &tg.ChannelsToggleJoinToSendRequest{Channel: input, Enabled: true})
	if err != nil {
		t.Fatalf("toggle join-to-send: %v", err)
	}
	if joinToSendChannel := joinToSendUpdates.(*tg.Updates).Chats[0].(*tg.Channel); !joinToSendChannel.GetJoinToSend() {
		t.Fatalf("join-to-send channel = %+v, want join_to_send flag", joinToSendChannel)
	}
	joinRequestUpdates, err := r.onChannelsToggleJoinRequest(WithUserID(ctx, owner.ID), &tg.ChannelsToggleJoinRequestRequest{Channel: input, Enabled: true})
	if err != nil {
		t.Fatalf("toggle join request: %v", err)
	}
	if joinRequestChannel := joinRequestUpdates.(*tg.Updates).Chats[0].(*tg.Channel); !joinRequestChannel.GetJoinRequest() {
		t.Fatalf("join-request channel = %+v, want join_request flag", joinRequestChannel)
	}
	chatsWithJoinFlags, err := r.onChannelsGetChannels(WithUserID(ctx, owner.ID), []tg.InputChannelClass{input})
	if err != nil {
		t.Fatalf("get channels after join settings: %v", err)
	}
	listedJoinChannel := chatsWithJoinFlags.(*tg.MessagesChats).Chats[0].(*tg.Channel)
	if !listedJoinChannel.GetJoinToSend() || !listedJoinChannel.GetJoinRequest() {
		t.Fatalf("listed channel join flags = %+v, want both flags", listedJoinChannel)
	}
	if _, err := r.onChannelsJoinChannel(WithUserID(ctx, requester.ID), input); err == nil || !strings.Contains(err.Error(), "INVITE_REQUEST_SENT") {
		t.Fatalf("public join request err = %v, want INVITE_REQUEST_SENT", err)
	}
	pendingPublic, err := r.onMessagesGetChatInviteImporters(WithUserID(ctx, owner.ID), &tg.MessagesGetChatInviteImportersRequest{
		Requested: true,
		Peer:      &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		Limit:     10,
	})
	if err != nil {
		t.Fatalf("get public pending join request: %v", err)
	}
	if pendingPublic.Count != 1 || len(pendingPublic.Importers) != 1 || pendingPublic.Importers[0].UserID != requester.ID || !pendingPublic.Importers[0].Requested {
		t.Fatalf("public pending importers = %+v, want requester pending", pendingPublic)
	}
	fullWithPublicPending, err := r.onChannelsGetFullChannel(WithUserID(ctx, owner.ID), input)
	if err != nil {
		t.Fatalf("get full channel with public pending request: %v", err)
	}
	fullPublicPending := fullWithPublicPending.FullChat.(*tg.ChannelFull)
	publicPendingCount, ok := fullPublicPending.GetRequestsPending()
	publicRecent, recentOK := fullPublicPending.GetRecentRequesters()
	if !ok || publicPendingCount != 1 || !recentOK || len(publicRecent) != 1 || publicRecent[0] != requester.ID {
		t.Fatalf("public full pending = count %d ok %v recent %+v ok %v, want requester", publicPendingCount, ok, publicRecent, recentOK)
	}
	approvedPublic, err := r.onMessagesHideChatJoinRequest(WithUserID(ctx, owner.ID), &tg.MessagesHideChatJoinRequestRequest{
		Approved: true,
		Peer:     &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		UserID:   &tg.InputUser{UserID: requester.ID, AccessHash: requester.AccessHash},
	})
	if err != nil {
		t.Fatalf("approve public join request: %v", err)
	}
	var publicPendingCleared *tg.UpdatePendingJoinRequests
	for _, update := range approvedPublic.(*tg.Updates).Updates {
		if pending, ok := update.(*tg.UpdatePendingJoinRequests); ok {
			publicPendingCleared = pending
			break
		}
	}
	if publicPendingCleared == nil || publicPendingCleared.RequestsPending != 0 || len(publicPendingCleared.RecentRequesters) != 0 {
		t.Fatalf("public approve pending update = %+v, want cleared pending requests", publicPendingCleared)
	}
	colorReq := &tg.ChannelsUpdateColorRequest{Channel: input}
	colorReq.SetForProfile(true)
	colorReq.SetColor(1)
	colorReq.SetBackgroundEmojiID(9001)
	colorUpdates, err := r.onChannelsUpdateColor(WithUserID(ctx, owner.ID), colorReq)
	if err != nil {
		t.Fatalf("update color: %v", err)
	}
	colorChannel := colorUpdates.(*tg.Updates).Chats[0].(*tg.Channel)
	profileColor, ok := colorChannel.GetProfileColor()
	if !ok {
		t.Fatalf("update color channel = %+v, want profile color", colorChannel)
	}
	if peerColor := profileColor.(*tg.PeerColor); peerColor.Color != 1 || peerColor.BackgroundEmojiID != 9001 {
		t.Fatalf("profile color = %+v, want color/background", peerColor)
	}
	status := &tg.EmojiStatus{DocumentID: 9101}
	status.SetUntil(1700000100)
	statusUpdates, err := r.onChannelsUpdateEmojiStatus(WithUserID(ctx, owner.ID), &tg.ChannelsUpdateEmojiStatusRequest{Channel: input, EmojiStatus: status})
	if err != nil {
		t.Fatalf("update emoji status: %v", err)
	}
	statusChannel := statusUpdates.(*tg.Updates).Chats[0].(*tg.Channel)
	emojiStatus, ok := statusChannel.GetEmojiStatus()
	if !ok {
		t.Fatalf("update emoji status channel = %+v, want emoji status", statusChannel)
	}
	if got := emojiStatus.(*tg.EmojiStatus); got.DocumentID != 9101 || got.Until != 1700000100 {
		t.Fatalf("emoji status = %+v, want document/until", got)
	}
	chatsWithAppearance, err := r.onChannelsGetChannels(WithUserID(ctx, owner.ID), []tg.InputChannelClass{input})
	if err != nil {
		t.Fatalf("get channels after appearance settings: %v", err)
	}
	persistedAppearance := chatsWithAppearance.(*tg.MessagesChats).Chats[0].(*tg.Channel)
	if _, ok := persistedAppearance.GetProfileColor(); !ok {
		t.Fatalf("persisted channel appearance = %+v, want profile color", persistedAppearance)
	}
	if _, ok := persistedAppearance.GetEmojiStatus(); !ok {
		t.Fatalf("persisted channel appearance = %+v, want emoji status", persistedAppearance)
	}
	link, err := r.onChannelsExportMessageLink(WithUserID(ctx, owner.ID), &tg.ChannelsExportMessageLinkRequest{Channel: input, ID: msgID})
	if err != nil {
		t.Fatalf("export message link: %v", err)
	}
	if !strings.Contains(link.Link, "public_team") || !strings.Contains(link.Link, strconv.Itoa(msgID)) {
		t.Fatalf("exported link = %+v, want username/message id", link)
	}
	replyToSeed := &tg.InputReplyToMessage{ReplyToMsgID: msgID}
	replyReq := &tg.MessagesSendMessageRequest{
		Peer:     &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		Message:  "thread reply",
		RandomID: 992,
	}
	replyReq.SetReplyTo(replyToSeed)
	replySent, err := r.onMessagesSendMessage(WithUserID(ctx, owner.ID), replyReq)
	if err != nil {
		t.Fatalf("send reply for export link: %v", err)
	}
	replyMsgID := replySent.(*tg.Updates).Updates[0].(*tg.UpdateMessageID).ID
	threadLink, err := r.onChannelsExportMessageLink(WithUserID(ctx, owner.ID), &tg.ChannelsExportMessageLinkRequest{Thread: true, Channel: input, ID: replyMsgID})
	if err != nil {
		t.Fatalf("export thread message link: %v", err)
	}
	if !strings.Contains(threadLink.Link, strconv.Itoa(replyMsgID)) || !strings.Contains(threadLink.Link, "?thread="+strconv.Itoa(msgID)) {
		t.Fatalf("thread exported link = %+v, want reply id and thread root %d", threadLink, msgID)
	}
	if _, err := r.onChannelsExportMessageLink(WithUserID(ctx, owner.ID), &tg.ChannelsExportMessageLinkRequest{Channel: input, ID: replyMsgID + 10000}); err == nil || !strings.Contains(err.Error(), "MESSAGE_ID_INVALID") {
		t.Fatalf("export missing message link err = %v, want MESSAGE_ID_INVALID", err)
	}
	if ok, err := r.onChannelsReadMessageContents(WithUserID(ctx, owner.ID), &tg.ChannelsReadMessageContentsRequest{Channel: input, ID: []int{msgID}}); err != nil || !ok {
		t.Fatalf("read message contents = ok %v err %v, want true", ok, err)
	}
	author, err := r.onChannelsGetMessageAuthor(WithUserID(ctx, owner.ID), &tg.ChannelsGetMessageAuthorRequest{Channel: input, ID: msgID})
	if err != nil {
		t.Fatalf("get message author: %v", err)
	}
	if author.(*tg.User).ID != owner.ID {
		t.Fatalf("message author = %+v, want owner", author)
	}
	if _, err := r.onChannelsGetMessageAuthor(WithUserID(ctx, owner.ID), &tg.ChannelsGetMessageAuthorRequest{Channel: input, ID: msgID + 10000}); err == nil || !strings.Contains(err.Error(), "MESSAGE_ID_INVALID") {
		t.Fatalf("get missing message author err = %v, want MESSAGE_ID_INVALID", err)
	}
	if ok, err := r.onChannelsReportSpam(WithUserID(ctx, owner.ID), &tg.ChannelsReportSpamRequest{
		Channel:     input,
		Participant: &tg.InputPeerUser{UserID: owner.ID, AccessHash: owner.AccessHash},
		ID:          []int{msgID},
	}); err != nil || !ok {
		t.Fatalf("report spam = ok %v err %v, want true", ok, err)
	}
	if _, err := r.onChannelsGetLeftChannels(WithUserID(ctx, owner.ID), 0); err != nil {
		t.Fatalf("get left channels: %v", err)
	}
	if _, err := r.onChannelsGetInactiveChannels(WithUserID(ctx, owner.ID)); err != nil {
		t.Fatalf("get inactive channels: %v", err)
	}
	if _, err := r.onChannelsGetGroupsForDiscussion(WithUserID(ctx, owner.ID)); err != nil {
		t.Fatalf("get groups for discussion: %v", err)
	}
	if _, err := r.onChannelsSetDiscussionGroup(WithUserID(ctx, owner.ID), &tg.ChannelsSetDiscussionGroupRequest{Broadcast: input, Group: &tg.InputChannelEmpty{}}); err == nil || !strings.Contains(err.Error(), "BROADCAST_ID_INVALID") {
		t.Fatalf("set discussion group on megagroup err = %v, want BROADCAST_ID_INVALID", err)
	}
	if ok, err := r.onChannelsEditLocation(WithUserID(ctx, owner.ID), &tg.ChannelsEditLocationRequest{Channel: input}); err != nil || !ok {
		t.Fatalf("edit location = ok %v err %v, want true", ok, err)
	}
	if _, err := r.onChannelsConvertToGigagroup(WithUserID(ctx, owner.ID), input); err != nil {
		t.Fatalf("convert to gigagroup: %v", err)
	}
	if affected, err := r.onChannelsDeleteParticipantHistory(WithUserID(ctx, owner.ID), &tg.ChannelsDeleteParticipantHistoryRequest{Channel: input, Participant: &tg.InputPeerUser{UserID: owner.ID, AccessHash: owner.AccessHash}}); err != nil || affected.PtsCount != 3 || affected.Pts == 0 || affected.Offset != 0 {
		t.Fatalf("delete participant history = %+v err %v, want one bounded delete update for owner service, text and reply messages", affected, err)
	}
	if updates, err := r.onChannelsToggleParticipantsHidden(WithUserID(ctx, owner.ID), &tg.ChannelsToggleParticipantsHiddenRequest{Channel: input, Enabled: true}); err != nil {
		t.Fatalf("toggle participants hidden: %v", err)
	} else if len(updates.(*tg.Updates).Chats) != 1 {
		t.Fatalf("toggle participants hidden updates = %+v, want channel chat", updates)
	}
	fullHidden, err := r.onChannelsGetFullChannel(WithUserID(ctx, requester.ID), input)
	if err != nil {
		t.Fatalf("requester get full after participants hidden: %v", err)
	}
	fullChannel := fullHidden.FullChat.(*tg.ChannelFull)
	if hidden := fullChannel.GetParticipantsHidden(); !hidden || fullChannel.CanViewParticipants {
		t.Fatalf("requester full channel = %+v, want participants_hidden and can_view_participants=false", fullChannel)
	}
	hiddenMembers, err := r.onChannelsGetParticipants(WithUserID(ctx, requester.ID), &tg.ChannelsGetParticipantsRequest{
		Channel: input,
		Filter:  &tg.ChannelParticipantsRecent{},
		Limit:   10,
	})
	if err != nil {
		t.Fatalf("requester get participants hidden: %v", err)
	}
	if page := hiddenMembers.(*tg.ChannelsChannelParticipants); len(page.Participants) != 0 || page.Count == 0 {
		t.Fatalf("hidden participants page = %+v, want empty page with aggregate count", page)
	}
	viewAsMessagesUpdates, err := r.onChannelsToggleViewForumAsMessages(WithUserID(ctx, owner.ID), &tg.ChannelsToggleViewForumAsMessagesRequest{Channel: input, Enabled: true})
	if err != nil {
		t.Fatalf("toggle forum as messages: %v", err)
	}
	viewAsMessagesUpdate, ok := viewAsMessagesUpdates.(*tg.Updates).Updates[0].(*tg.UpdateChannelViewForumAsMessages)
	if !ok || viewAsMessagesUpdate.ChannelID != channel.ID || !viewAsMessagesUpdate.Enabled {
		t.Fatalf("toggle forum as messages updates = %+v, want updateChannelViewForumAsMessages", viewAsMessagesUpdates)
	}
	fullWithViewAsMessages, err := r.onChannelsGetFullChannel(WithUserID(ctx, owner.ID), input)
	if err != nil {
		t.Fatalf("full channel after view as messages: %v", err)
	}
	if full := fullWithViewAsMessages.FullChat.(*tg.ChannelFull); !full.ViewForumAsMessages {
		t.Fatalf("full channel view_forum_as_messages = false, want true")
	}
	forumUpdates, err := r.onChannelsToggleForum(WithUserID(ctx, owner.ID), &tg.ChannelsToggleForumRequest{Channel: input, Enabled: true, Tabs: true})
	if err != nil {
		t.Fatalf("toggle forum: %v", err)
	}
	if len(forumUpdates.(*tg.Updates).Chats) != 1 {
		t.Fatalf("toggle forum updates = %+v, want channel chat", forumUpdates)
	}
	forumChat, ok := forumUpdates.(*tg.Updates).Chats[0].(*tg.Channel)
	if !ok || !forumChat.Forum || !forumChat.ForumTabs {
		t.Fatalf("toggle forum chat = %+v, want forum+forum_tabs", forumUpdates.(*tg.Updates).Chats[0])
	}
	forumPeer := &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash}
	forumTopics, err := r.onMessagesGetForumTopics(WithUserID(ctx, owner.ID), &tg.MessagesGetForumTopicsRequest{
		Peer:  forumPeer,
		Limit: 10,
	})
	if err != nil {
		t.Fatalf("messages.getForumTopics after toggle: %v", err)
	}
	if forumTopics.Count != 1 || len(forumTopics.Topics) != 1 || len(forumTopics.Chats) != 1 || forumTopics.Pts == 0 {
		t.Fatalf("messages.getForumTopics after toggle = %+v, want general topic with channel context", forumTopics)
	}
	generalTopic, ok := forumTopics.Topics[0].(*tg.ForumTopic)
	if !ok || generalTopic.ID != forumGeneralTopicID || generalTopic.Title != "General" {
		t.Fatalf("forum topic = %+v, want General topic", forumTopics.Topics[0])
	}
	forumTopicsByID, err := r.onMessagesGetForumTopicsByID(WithUserID(ctx, owner.ID), &tg.MessagesGetForumTopicsByIDRequest{
		Peer:   forumPeer,
		Topics: []int{forumGeneralTopicID},
	})
	if err != nil {
		t.Fatalf("messages.getForumTopicsByID after toggle: %v", err)
	}
	if forumTopicsByID.Count != 1 || len(forumTopicsByID.Topics) != 1 {
		t.Fatalf("messages.getForumTopicsByID after toggle = %+v, want General topic", forumTopicsByID)
	}
	createdForumTopic, err := r.onMessagesCreateForumTopic(WithUserID(ctx, owner.ID), &tg.MessagesCreateForumTopicRequest{
		Peer:      forumPeer,
		Title:     "Ops",
		IconColor: domain.DefaultForumTopicIconColor,
		RandomID:  17803001,
	})
	if err != nil {
		t.Fatalf("messages.createForumTopic after toggle: %v", err)
	}
	var topicID int
	for _, update := range createdForumTopic.(*tg.Updates).Updates {
		newMsg, ok := update.(*tg.UpdateNewChannelMessage)
		if !ok {
			continue
		}
		msg, ok := newMsg.Message.(*tg.MessageService)
		if !ok {
			continue
		}
		action, ok := msg.Action.(*tg.MessageActionTopicCreate)
		if ok && action.Title == "Ops" {
			topicID = msg.ID
			break
		}
	}
	if topicID == 0 {
		t.Fatalf("messages.createForumTopic updates = %+v, want topic root service message", createdForumTopic)
	}
	forumTopicsWithCreated, err := r.onMessagesGetForumTopics(WithUserID(ctx, owner.ID), &tg.MessagesGetForumTopicsRequest{
		Peer:  forumPeer,
		Limit: 10,
	})
	if err != nil {
		t.Fatalf("messages.getForumTopics with created topic: %v", err)
	}
	if forumTopicsWithCreated.Count != 2 || len(forumTopicsWithCreated.Topics) != 2 || len(forumTopicsWithCreated.Messages) == 0 {
		t.Fatalf("messages.getForumTopics with created topic = %+v, want General + created topic + root message", forumTopicsWithCreated)
	}
	var foundCreated bool
	for _, item := range forumTopicsWithCreated.Topics {
		topic, ok := item.(*tg.ForumTopic)
		if ok && topic.ID == topicID && topic.Title == "Ops" && topic.TopMessage == topicID {
			foundCreated = true
			break
		}
	}
	if !foundCreated {
		t.Fatalf("messages.getForumTopics topics = %+v, want created topic id %d", forumTopicsWithCreated.Topics, topicID)
	}
	forumTopicsByID, err = r.onMessagesGetForumTopicsByID(WithUserID(ctx, owner.ID), &tg.MessagesGetForumTopicsByIDRequest{
		Peer:   forumPeer,
		Topics: []int{forumGeneralTopicID, topicID},
	})
	if err != nil {
		t.Fatalf("messages.getForumTopicsByID with created topic: %v", err)
	}
	if forumTopicsByID.Count != 2 || len(forumTopicsByID.Topics) != 2 || len(forumTopicsByID.Messages) == 0 {
		t.Fatalf("messages.getForumTopicsByID with created topic = %+v, want General + created topic", forumTopicsByID)
	}
	topicReply := &tg.InputReplyToMessage{ReplyToMsgID: 0}
	topicReply.SetTopMsgID(topicID)
	topicSendReq := &tg.MessagesSendMessageRequest{
		Peer:     forumPeer,
		Message:  "topic body",
		RandomID: 17803002,
	}
	topicSendReq.SetReplyTo(topicReply)
	topicSent, err := r.onMessagesSendMessage(WithUserID(ctx, owner.ID), topicSendReq)
	if err != nil {
		t.Fatalf("messages.sendMessage topic-only reply: %v", err)
	}
	var topicMsg *tg.Message
	for _, update := range topicSent.(*tg.Updates).Updates {
		newMsg, ok := update.(*tg.UpdateNewChannelMessage)
		if !ok {
			continue
		}
		if msg, ok := newMsg.Message.(*tg.Message); ok && msg.Message == "topic body" {
			topicMsg = msg
			break
		}
	}
	if topicMsg == nil {
		t.Fatalf("messages.sendMessage topic updates = %+v, want topic message", topicSent)
	}
	header, ok := topicMsg.ReplyTo.(*tg.MessageReplyHeader)
	if !ok || !header.ForumTopic || header.ReplyToMsgID != 0 {
		t.Fatalf("topic reply header = %#v, want forum topic without reply_to_msg_id", topicMsg.ReplyTo)
	}
	if topID, ok := header.GetReplyToTopID(); !ok || topID != topicID {
		t.Fatalf("topic reply top id = %d ok %v, want %d", topID, ok, topicID)
	}
	topicReplies, err := r.onMessagesGetReplies(WithUserID(ctx, owner.ID), &tg.MessagesGetRepliesRequest{
		Peer:  forumPeer,
		MsgID: topicID,
		Limit: 10,
	})
	if err != nil {
		t.Fatalf("messages.getReplies forum topic: %v", err)
	}
	topicReplyPage, ok := topicReplies.(*tg.MessagesChannelMessages)
	if !ok || topicReplyPage.Pts == 0 || len(topicReplyPage.Messages) != 1 || len(topicReplyPage.Topics) != 1 {
		t.Fatalf("messages.getReplies forum topic = %T %+v, want channelMessages with one topic", topicReplies, topicReplies)
	}
	if got := topicReplyPage.Messages[0].(*tg.Message); got.ID != topicMsg.ID || got.Message != "topic body" {
		t.Fatalf("topic reply message = %#v, want id %d", got, topicMsg.ID)
	}
	if topic, ok := topicReplyPage.Topics[0].(*tg.ForumTopic); !ok || !topic.Short || topic.ID != topicID || topic.TopMessage != topicMsg.ID {
		t.Fatalf("topic reply page topic = %#v, want short topic with updated top message %d", topicReplyPage.Topics[0], topicMsg.ID)
	}
	forumTopicsAfterMessage, err := r.onMessagesGetForumTopics(WithUserID(ctx, owner.ID), &tg.MessagesGetForumTopicsRequest{
		Peer:  forumPeer,
		Limit: 10,
	})
	if err != nil {
		t.Fatalf("messages.getForumTopics after topic message: %v", err)
	}
	var foundTopicTop bool
	for _, item := range forumTopicsAfterMessage.Topics {
		topic, ok := item.(*tg.ForumTopic)
		if ok && topic.ID == topicID && topic.TopMessage == topicMsg.ID {
			foundTopicTop = true
			break
		}
	}
	if !foundTopicTop {
		t.Fatalf("messages.getForumTopics after topic message topics = %+v, want top message %d", forumTopicsAfterMessage.Topics, topicMsg.ID)
	}
	forwardToTopicReq := &tg.MessagesForwardMessagesRequest{
		FromPeer: forumPeer,
		ToPeer:   forumPeer,
		ID:       []int{topicMsg.ID},
		RandomID: []int64{17803003},
	}
	forwardToTopicReq.SetTopMsgID(topicID)
	forwardToTopic, err := r.onMessagesForwardMessages(WithUserID(ctx, owner.ID), forwardToTopicReq)
	if err != nil {
		t.Fatalf("messages.forwardMessages to forum topic: %v", err)
	}
	var forwardedTopicMsg *tg.Message
	for _, update := range forwardToTopic.(*tg.Updates).Updates {
		newMsg, ok := update.(*tg.UpdateNewChannelMessage)
		if !ok {
			continue
		}
		if msg, ok := newMsg.Message.(*tg.Message); ok && msg.Message == "topic body" && msg.ID != topicMsg.ID {
			forwardedTopicMsg = msg
			break
		}
	}
	if forwardedTopicMsg == nil {
		t.Fatalf("messages.forwardMessages to topic updates = %+v, want forwarded topic message", forwardToTopic)
	}
	if _, ok := forwardedTopicMsg.GetFwdFrom(); !ok {
		t.Fatalf("forwarded topic message = %#v, want fwd header", forwardedTopicMsg)
	}
	forwardReply, ok := forwardedTopicMsg.ReplyTo.(*tg.MessageReplyHeader)
	if !ok || !forwardReply.ForumTopic || forwardReply.ReplyToMsgID != 0 {
		t.Fatalf("forward topic reply header = %#v, want forum topic without reply_to_msg_id", forwardedTopicMsg.ReplyTo)
	}
	if topID, ok := forwardReply.GetReplyToTopID(); !ok || topID != topicID {
		t.Fatalf("forward topic reply top id = %d ok %v, want %d", topID, ok, topicID)
	}
	forumTopicsAfterForward, err := r.onMessagesGetForumTopics(WithUserID(ctx, owner.ID), &tg.MessagesGetForumTopicsRequest{
		Peer:  forumPeer,
		Limit: 10,
	})
	if err != nil {
		t.Fatalf("messages.getForumTopics after forward: %v", err)
	}
	var foundForwardTop bool
	for _, item := range forumTopicsAfterForward.Topics {
		topic, ok := item.(*tg.ForumTopic)
		if ok && topic.ID == topicID && topic.TopMessage == forwardedTopicMsg.ID {
			foundForwardTop = true
			break
		}
	}
	if !foundForwardTop {
		t.Fatalf("messages.getForumTopics after forward topics = %+v, want top message %d", forumTopicsAfterForward.Topics, forwardedTopicMsg.ID)
	}
	invalidForwardTop := &tg.MessagesForwardMessagesRequest{
		FromPeer: forumPeer,
		ToPeer:   forumPeer,
		ID:       []int{topicMsg.ID},
		RandomID: []int64{17803004},
	}
	invalidForwardTop.SetTopMsgID(domain.MaxMessageBoxID + 1)
	if _, err := r.onMessagesForwardMessages(WithUserID(ctx, owner.ID), invalidForwardTop); err == nil || !strings.Contains(err.Error(), "REPLY_MESSAGE_ID_INVALID") {
		t.Fatalf("messages.forwardMessages huge top_msg_id err = %v, want REPLY_MESSAGE_ID_INVALID", err)
	}
	editTopicReq := &tg.MessagesEditForumTopicRequest{Peer: forumPeer, TopicID: topicID}
	editTopicReq.SetTitle("Ops 2")
	editTopicReq.SetClosed(true)
	editedForumTopic, err := r.onMessagesEditForumTopic(WithUserID(ctx, owner.ID), editTopicReq)
	if err != nil {
		t.Fatalf("messages.editForumTopic: %v", err)
	}
	var editedRootID int
	for _, update := range editedForumTopic.(*tg.Updates).Updates {
		newMsg, ok := update.(*tg.UpdateNewChannelMessage)
		if !ok {
			continue
		}
		msg, ok := newMsg.Message.(*tg.MessageService)
		if !ok {
			continue
		}
		action, ok := msg.Action.(*tg.MessageActionTopicEdit)
		if ok && action.Title == "Ops 2" {
			if closed, closedOK := action.GetClosed(); closedOK && closed {
				editedRootID = msg.ID
				break
			}
		}
	}
	if editedRootID == 0 {
		t.Fatalf("messages.editForumTopic updates = %+v, want topic edit service message", editedForumTopic)
	}
	pinnedTopic, err := r.onMessagesUpdatePinnedForumTopic(WithUserID(ctx, owner.ID), &tg.MessagesUpdatePinnedForumTopicRequest{
		Peer:    forumPeer,
		TopicID: topicID,
		Pinned:  true,
	})
	if err != nil {
		t.Fatalf("messages.updatePinnedForumTopic: %v", err)
	}
	if got := pinnedTopic.(*tg.Updates).Updates; len(got) != 1 {
		t.Fatalf("messages.updatePinnedForumTopic updates = %+v, want one update", got)
	} else if update, ok := got[0].(*tg.UpdatePinnedForumTopic); !ok || update.TopicID != topicID || !update.GetPinned() {
		t.Fatalf("messages.updatePinnedForumTopic update = %+v, want pinned topic", got[0])
	}
	reorderedTopics, err := r.onMessagesReorderPinnedForumTopics(WithUserID(ctx, owner.ID), &tg.MessagesReorderPinnedForumTopicsRequest{
		Peer:  forumPeer,
		Order: []int{topicID},
		Force: true,
	})
	if err != nil {
		t.Fatalf("messages.reorderPinnedForumTopics: %v", err)
	}
	if got := reorderedTopics.(*tg.Updates).Updates; len(got) != 1 {
		t.Fatalf("messages.reorderPinnedForumTopics updates = %+v, want one update", got)
	} else if update, ok := got[0].(*tg.UpdatePinnedForumTopics); !ok || len(update.Order) != 1 || update.Order[0] != topicID {
		t.Fatalf("messages.reorderPinnedForumTopics update = %+v, want order", got[0])
	}
	deletedTopic, err := r.onMessagesDeleteTopicHistory(WithUserID(ctx, owner.ID), &tg.MessagesDeleteTopicHistoryRequest{
		Peer:     forumPeer,
		TopMsgID: topicID,
	})
	if err != nil {
		t.Fatalf("messages.deleteTopicHistory: %v", err)
	}
	if deletedTopic.Pts == 0 || deletedTopic.PtsCount == 0 || deletedTopic.Offset != 0 {
		t.Fatalf("messages.deleteTopicHistory = %+v, want affected history with final offset", deletedTopic)
	}
	forumTopicsAfterDelete, err := r.onMessagesGetForumTopics(WithUserID(ctx, owner.ID), &tg.MessagesGetForumTopicsRequest{
		Peer:  forumPeer,
		Limit: 10,
	})
	if err != nil {
		t.Fatalf("messages.getForumTopics after delete topic: %v", err)
	}
	if forumTopicsAfterDelete.Count != 1 || len(forumTopicsAfterDelete.Topics) != 1 {
		t.Fatalf("messages.getForumTopics after delete topic = %+v, want General only", forumTopicsAfterDelete)
	}
	antiSpamUpdates, err := r.onChannelsToggleAntiSpam(WithUserID(ctx, owner.ID), &tg.ChannelsToggleAntiSpamRequest{Channel: input, Enabled: true})
	if err != nil {
		t.Fatalf("toggle antispam: %v", err)
	}
	if len(antiSpamUpdates.(*tg.Updates).Chats) != 1 {
		t.Fatalf("toggle antispam updates = %+v, want channel chat", antiSpamUpdates)
	}
	fullWithAntiSpam, err := r.onChannelsGetFullChannel(WithUserID(ctx, owner.ID), input)
	if err != nil {
		t.Fatalf("full channel after antispam: %v", err)
	}
	if full := fullWithAntiSpam.FullChat.(*tg.ChannelFull); !full.GetAntispam() {
		t.Fatalf("full channel antispam = false, want true")
	}
	settingsFilter := tg.ChannelAdminLogEventsFilter{}
	settingsFilter.SetSettings(true)
	antiSpamLog, err := r.onChannelsGetAdminLog(WithUserID(ctx, owner.ID), &tg.ChannelsGetAdminLogRequest{
		Channel:      input,
		EventsFilter: settingsFilter,
		Limit:        10,
	})
	if err != nil {
		t.Fatalf("admin log after antispam: %v", err)
	}
	foundAntiSpamLog := false
	foundForumLog := false
	for _, event := range antiSpamLog.Events {
		if action, ok := event.Action.(*tg.ChannelAdminLogEventActionToggleAntiSpam); ok && action.NewValue {
			foundAntiSpamLog = true
		}
		if action, ok := event.Action.(*tg.ChannelAdminLogEventActionToggleForum); ok && action.NewValue {
			foundForumLog = true
		}
	}
	if !foundAntiSpamLog || !foundForumLog {
		t.Fatalf("admin log events = %+v, want toggle antispam and toggle forum actions", antiSpamLog.Events)
	}
	channelUpdateStubs := []struct {
		name string
		call func() (tg.UpdatesClass, error)
	}{
		{"set boosts", func() (tg.UpdatesClass, error) {
			return r.onChannelsSetBoostsToUnblockRestrictions(WithUserID(ctx, owner.ID), &tg.ChannelsSetBoostsToUnblockRestrictionsRequest{Channel: input, Boosts: 1})
		}},
		{"restrict sponsored", func() (tg.UpdatesClass, error) {
			return r.onChannelsRestrictSponsoredMessages(WithUserID(ctx, owner.ID), &tg.ChannelsRestrictSponsoredMessagesRequest{Channel: input, Restricted: true})
		}},
		{"update paid messages", func() (tg.UpdatesClass, error) {
			return r.onChannelsUpdatePaidMessagesPrice(WithUserID(ctx, owner.ID), &tg.ChannelsUpdatePaidMessagesPriceRequest{Channel: input, SendPaidMessagesStars: 7})
		}},
		{"toggle autotranslation", func() (tg.UpdatesClass, error) {
			return r.onChannelsToggleAutotranslation(WithUserID(ctx, owner.ID), &tg.ChannelsToggleAutotranslationRequest{Channel: input, Enabled: true})
		}},
	}
	for _, item := range channelUpdateStubs {
		updates, err := item.call()
		if err != nil {
			t.Fatalf("%s: %v", item.name, err)
		}
		if len(updates.(*tg.Updates).Chats) != 1 {
			t.Fatalf("%s updates = %+v, want channel chat", item.name, updates)
		}
	}
	fullAfterPaidSettings, err := r.onChannelsGetFullChannel(WithUserID(ctx, owner.ID), input)
	if err != nil {
		t.Fatalf("full channel after paid/settings toggles: %v", err)
	}
	fullPaidSettings := fullAfterPaidSettings.FullChat.(*tg.ChannelFull)
	if !fullPaidSettings.GetRestrictedSponsored() {
		t.Fatalf("full channel restricted_sponsored = false, want true")
	}
	if stars, ok := fullPaidSettings.GetSendPaidMessagesStars(); !fullPaidSettings.GetPaidMessagesAvailable() || !ok || stars != 7 {
		t.Fatalf("full channel paid messages = available %v stars %d ok %v, want available true stars 7", fullPaidSettings.GetPaidMessagesAvailable(), stars, ok)
	}
	chatsAfterAutotranslation, err := r.onChannelsGetChannels(WithUserID(ctx, owner.ID), []tg.InputChannelClass{input})
	if err != nil {
		t.Fatalf("get channels after autotranslation: %v", err)
	}
	channelAfterAutotranslation := chatsAfterAutotranslation.(*tg.MessagesChats).Chats[0].(*tg.Channel)
	if !channelAfterAutotranslation.GetAutotranslation() {
		t.Fatalf("channel autotranslation = false, want true")
	}
	autoLog, err := r.onChannelsGetAdminLog(WithUserID(ctx, owner.ID), &tg.ChannelsGetAdminLogRequest{
		Channel:      input,
		EventsFilter: settingsFilter,
		Limit:        10,
	})
	if err != nil {
		t.Fatalf("admin log after autotranslation: %v", err)
	}
	foundAutoLog := false
	for _, event := range autoLog.Events {
		if action, ok := event.Action.(*tg.ChannelAdminLogEventActionToggleAutotranslation); ok && action.NewValue {
			foundAutoLog = true
			break
		}
	}
	if !foundAutoLog {
		t.Fatalf("admin log events = %+v, want toggle autotranslation action", autoLog.Events)
	}
	if _, err := r.onChannelsSetBoostsToUnblockRestrictions(WithUserID(ctx, owner.ID), &tg.ChannelsSetBoostsToUnblockRestrictionsRequest{Channel: input, Boosts: maxChannelBoostsToUnblockRestrictions + 1}); err == nil || !strings.Contains(err.Error(), "LIMIT_INVALID") {
		t.Fatalf("set boosts over max err = %v, want LIMIT_INVALID", err)
	}
	if _, err := r.onChannelsUpdatePaidMessagesPrice(WithUserID(ctx, owner.ID), &tg.ChannelsUpdatePaidMessagesPriceRequest{Channel: input, SendPaidMessagesStars: -1}); err == nil || !strings.Contains(err.Error(), "STARS_AMOUNT_INVALID") {
		t.Fatalf("update paid messages negative supergroup err = %v, want STARS_AMOUNT_INVALID", err)
	}
	if _, err := r.onChannelsUpdatePaidMessagesPrice(WithUserID(ctx, owner.ID), &tg.ChannelsUpdatePaidMessagesPriceRequest{Channel: input, SendPaidMessagesStars: maxChannelPaidMessageStars + 1}); err == nil || !strings.Contains(err.Error(), "STARS_AMOUNT_INVALID") {
		t.Fatalf("update paid messages over max err = %v, want STARS_AMOUNT_INVALID", err)
	}
	broadcastCreated, err := r.onChannelsCreateChannel(WithUserID(ctx, owner.ID), &tg.ChannelsCreateChannelRequest{
		Title:     "Broadcast Paid",
		About:     "broadcast direct messages",
		Broadcast: true,
	})
	if err != nil {
		t.Fatalf("create broadcast channel: %v", err)
	}
	broadcast := broadcastCreated.(*tg.Updates).Chats[0].(*tg.Channel)
	broadcastInput := &tg.InputChannel{ChannelID: broadcast.ID, AccessHash: broadcast.AccessHash}
	if _, err := r.onChannelsUpdatePaidMessagesPrice(WithUserID(ctx, owner.ID), &tg.ChannelsUpdatePaidMessagesPriceRequest{Channel: broadcastInput, SendPaidMessagesStars: -1}); err != nil {
		t.Fatalf("update paid messages broadcast disable: %v", err)
	}
	if _, err := r.onChannelsUpdatePaidMessagesPrice(WithUserID(ctx, owner.ID), &tg.ChannelsUpdatePaidMessagesPriceRequest{
		BroadcastMessagesAllowed: true,
		Channel:                  broadcastInput,
		SendPaidMessagesStars:    maxChannelPaidMessageStars,
	}); err != nil {
		t.Fatalf("update paid messages broadcast max: %v", err)
	}
	broadcastChats, err := r.onChannelsGetChannels(WithUserID(ctx, owner.ID), []tg.InputChannelClass{broadcastInput})
	if err != nil {
		t.Fatalf("get broadcast after paid messages: %v", err)
	}
	broadcastAfterPaid := broadcastChats.(*tg.MessagesChats).Chats[0].(*tg.Channel)
	if !broadcastAfterPaid.GetBroadcastMessagesAllowed() {
		t.Fatalf("broadcast_messages_allowed = false, want true")
	}
	if stars, ok := broadcastAfterPaid.GetSendPaidMessagesStars(); !ok || stars != maxChannelPaidMessageStars {
		t.Fatalf("broadcast send_paid_messages_stars = %d ok %v, want %d true", stars, ok, maxChannelPaidMessageStars)
	}
	if _, err := r.onChannelsSetStickers(WithUserID(ctx, owner.ID), &tg.ChannelsSetStickersRequest{Channel: broadcastInput, Stickerset: &tg.InputStickerSetEmpty{}}); err == nil || !strings.Contains(err.Error(), "CHANNEL_INVALID") {
		t.Fatalf("set stickers broadcast err = %v, want CHANNEL_INVALID", err)
	}
	if ok, err := r.onChannelsReportAntiSpamFalsePositive(WithUserID(ctx, owner.ID), &tg.ChannelsReportAntiSpamFalsePositiveRequest{Channel: input, MsgID: msgID}); err != nil || !ok {
		t.Fatalf("report antispam false positive = ok %v err %v, want true", ok, err)
	}
	recommendationsReq := &tg.ChannelsGetChannelRecommendationsRequest{}
	recommendationsReq.SetChannel(input)
	if _, err := r.onChannelsGetChannelRecommendations(WithUserID(ctx, owner.ID), recommendationsReq); err == nil || !strings.Contains(err.Error(), "CHANNEL_INVALID") {
		t.Fatalf("megagroup channel recommendations err = %v, want CHANNEL_INVALID", err)
	}
	if ok, err := r.onChannelsSetEmojiStickers(WithUserID(ctx, owner.ID), &tg.ChannelsSetEmojiStickersRequest{Channel: input, Stickerset: &tg.InputStickerSetEmpty{}}); err != nil || !ok {
		t.Fatalf("set emoji stickers = ok %v err %v, want true", ok, err)
	}
	if _, err := r.onChannelsSetEmojiStickers(WithUserID(ctx, owner.ID), &tg.ChannelsSetEmojiStickersRequest{
		Channel:    input,
		Stickerset: &tg.InputStickerSetShortName{ShortName: "custom_emoji"},
	}); err == nil || !strings.Contains(err.Error(), "STICKERSET_INVALID") {
		t.Fatalf("set emoji stickers non-empty err = %v, want STICKERSET_INVALID", err)
	}
	if _, err := r.onChannelsSetEmojiStickers(WithUserID(ctx, owner.ID), &tg.ChannelsSetEmojiStickersRequest{Channel: broadcastInput, Stickerset: &tg.InputStickerSetEmpty{}}); err == nil || !strings.Contains(err.Error(), "CHANNEL_INVALID") {
		t.Fatalf("set emoji stickers broadcast err = %v, want CHANNEL_INVALID", err)
	}
	searchPostsReq := &tg.ChannelsSearchPostsRequest{OffsetPeer: &tg.InputPeerEmpty{}, Limit: 10}
	searchPostsReq.SetHashtag("ops")
	if posts, err := r.onChannelsSearchPosts(WithUserID(ctx, owner.ID), searchPostsReq); err != nil || len(posts.(*tg.MessagesMessages).Messages) != 0 {
		t.Fatalf("search posts = %+v err %v, want empty", posts, err)
	}
	floodReq := &tg.ChannelsCheckSearchPostsFloodRequest{}
	floodReq.SetQuery("ops")
	if flood, err := r.onChannelsCheckSearchPostsFlood(WithUserID(ctx, owner.ID), floodReq); err != nil || !flood.QueryIsFree {
		t.Fatalf("check search posts flood = %+v err %v, want free", flood, err)
	}
	if ok, err := r.onChannelsSetMainProfileTab(WithUserID(ctx, owner.ID), &tg.ChannelsSetMainProfileTabRequest{Channel: input}); err != nil || !ok {
		t.Fatalf("set main profile tab = ok %v err %v, want true", ok, err)
	}
	if ok, err := r.onChannelsToggleUsername(WithUserID(ctx, owner.ID), &tg.ChannelsToggleUsernameRequest{Channel: input, Username: "public_team", Active: false}); err != nil || !ok {
		t.Fatalf("toggle username = ok %v err %v, want true", ok, err)
	}
	if ok, err := r.onChannelsDeactivateAllUsernames(WithUserID(ctx, owner.ID), input); err != nil || !ok {
		t.Fatalf("deactivate usernames = ok %v err %v, want true", ok, err)
	}
	stillPublic, err := r.onChannelsGetChannels(WithUserID(ctx, owner.ID), []tg.InputChannelClass{input})
	if err != nil {
		t.Fatalf("get channel after deactivate: %v", err)
	}
	if got := stillPublic.(*tg.MessagesChats).Chats[0].(*tg.Channel); got.Username != "public_team" {
		t.Fatalf("channel after fragment username stubs = %+v, want primary username preserved", got)
	}
	if ok, err := r.onChannelsUpdateUsername(WithUserID(ctx, owner.ID), &tg.ChannelsUpdateUsernameRequest{Channel: input, Username: ""}); err != nil || !ok {
		t.Fatalf("clear username = ok %v err %v, want true", ok, err)
	}
	after, err := r.onChannelsGetChannels(WithUserID(ctx, owner.ID), []tg.InputChannelClass{input})
	if err != nil {
		t.Fatalf("get channel after clear: %v", err)
	}
	if got := after.(*tg.MessagesChats).Chats[0].(*tg.Channel); got.Username != "" || len(got.Usernames) != 0 {
		t.Fatalf("channel after clear = %+v, want username cleared", got)
	}
}

func TestChannelsGetLeftChannelsRPCReturnsLeftFlagAndSafePaging(t *testing.T) {
	ctx := context.Background()
	const (
		ownerID  int64 = 1000000901
		memberID int64 = 1000000902
	)
	channelStore := memory.NewChannelStore()
	channelService := appchannels.NewService(channelStore)
	r := New(Config{}, Deps{Channels: channelService}, zaptest.NewLogger(t), clock.System)
	older, err := channelService.CreateMegagroupFromCreateChat(ctx, ownerID, domain.CreateChannelRequest{
		Title:         "Older Left",
		MemberUserIDs: []int64{memberID},
		Date:          1700000900,
	})
	if err != nil {
		t.Fatalf("create older megagroup: %v", err)
	}
	newer, err := channelService.CreateChannel(ctx, ownerID, domain.CreateChannelRequest{
		Title:         "Newer Left Broadcast",
		Broadcast:     true,
		MemberUserIDs: []int64{memberID},
		Date:          1700000901,
	})
	if err != nil {
		t.Fatalf("create newer broadcast: %v", err)
	}
	if _, err := channelService.CreateMegagroupFromCreateChat(ctx, ownerID, domain.CreateChannelRequest{
		Title:         "Active Excluded",
		MemberUserIDs: []int64{memberID},
		Date:          1700000902,
	}); err != nil {
		t.Fatalf("create active megagroup: %v", err)
	}
	if _, err := channelService.LeaveChannel(ctx, memberID, older.Channel.ID, 1700000903); err != nil {
		t.Fatalf("leave older channel: %v", err)
	}
	if _, err := channelService.LeaveChannel(ctx, memberID, newer.Channel.ID, 1700000904); err != nil {
		t.Fatalf("leave newer channel: %v", err)
	}

	got, err := r.onChannelsGetLeftChannels(WithUserID(ctx, memberID), 0)
	if err != nil {
		t.Fatalf("get left channels: %v", err)
	}
	chats, ok := got.(*tg.MessagesChats)
	if !ok || len(chats.Chats) != 2 {
		t.Fatalf("left channels = %T %+v, want final messages.chats with two chats", got, got)
	}
	first, ok := chats.Chats[0].(*tg.Channel)
	if !ok || first.ID != newer.Channel.ID || !first.Left {
		t.Fatalf("first left channel = %+v (%T), want newest with left flag", chats.Chats[0], chats.Chats[0])
	}
	second, ok := chats.Chats[1].(*tg.Channel)
	if !ok || second.ID != older.Channel.ID || !second.Left {
		t.Fatalf("second left channel = %+v (%T), want older with left flag", chats.Chats[1], chats.Chats[1])
	}

	empty, err := r.onChannelsGetLeftChannels(WithUserID(ctx, memberID), 2)
	if err != nil {
		t.Fatalf("get empty left channels page: %v", err)
	}
	emptySlice, ok := empty.(*tg.MessagesChatsSlice)
	if !ok || emptySlice.Count != 2 || len(emptySlice.Chats) != 0 {
		t.Fatalf("empty left page = %T %+v, want empty slice with full count", empty, empty)
	}
	if _, err := r.onChannelsGetLeftChannels(WithUserID(ctx, memberID), domain.MaxLeftChannelsOffset+1); err == nil || !strings.Contains(err.Error(), "LIMIT_INVALID") {
		t.Fatalf("huge offset err = %v, want LIMIT_INVALID", err)
	}
}

func TestChannelsDiscussionGroupRPCPersistsFullChannelLink(t *testing.T) {
	ctx := context.Background()
	const ownerID int64 = 1000000911
	channelStore := memory.NewChannelStore()
	channelService := appchannels.NewService(channelStore)
	r := New(Config{}, Deps{Channels: channelService}, zaptest.NewLogger(t), clock.System)
	broadcast, err := channelService.CreateChannel(ctx, ownerID, domain.CreateChannelRequest{
		Title:     "Discussion Broadcast",
		Broadcast: true,
		Date:      1700000910,
	})
	if err != nil {
		t.Fatalf("create broadcast: %v", err)
	}
	group, err := channelService.CreateMegagroupFromCreateChat(ctx, ownerID, domain.CreateChannelRequest{
		Title: "Discussion Group",
		Date:  1700000911,
	})
	if err != nil {
		t.Fatalf("create group: %v", err)
	}
	inputBroadcast := &tg.InputChannel{ChannelID: broadcast.Channel.ID, AccessHash: broadcast.Channel.AccessHash}
	inputGroup := &tg.InputChannel{ChannelID: group.Channel.ID, AccessHash: group.Channel.AccessHash}

	candidates, err := r.onChannelsGetGroupsForDiscussion(WithUserID(ctx, ownerID))
	if err != nil {
		t.Fatalf("get groups for discussion: %v", err)
	}
	candidateChats := candidates.(*tg.MessagesChats).Chats
	if len(candidateChats) != 1 || candidateChats[0].(*tg.Channel).ID != group.Channel.ID {
		t.Fatalf("discussion candidates = %+v, want the creator megagroup", candidateChats)
	}
	if ok, err := r.onChannelsSetDiscussionGroup(WithUserID(ctx, ownerID), &tg.ChannelsSetDiscussionGroupRequest{
		Broadcast: inputBroadcast,
		Group:     inputGroup,
	}); err != nil || !ok {
		t.Fatalf("set discussion group = ok %v err %v, want true", ok, err)
	}
	fullBroadcast, err := r.onChannelsGetFullChannel(WithUserID(ctx, ownerID), inputBroadcast)
	if err != nil {
		t.Fatalf("get full broadcast: %v", err)
	}
	linkedID, ok := fullBroadcast.FullChat.(*tg.ChannelFull).GetLinkedChatID()
	if !ok || linkedID != group.Channel.ID {
		t.Fatalf("broadcast linked_chat_id = %d ok %v, want group %d", linkedID, ok, group.Channel.ID)
	}
	fullGroup, err := r.onChannelsGetFullChannel(WithUserID(ctx, ownerID), inputGroup)
	if err != nil {
		t.Fatalf("get full group: %v", err)
	}
	groupLinkedID, ok := fullGroup.FullChat.(*tg.ChannelFull).GetLinkedChatID()
	if !ok || groupLinkedID != broadcast.Channel.ID {
		t.Fatalf("group linked_chat_id = %d ok %v, want broadcast %d", groupLinkedID, ok, broadcast.Channel.ID)
	}
	gotChannel := fullBroadcast.Chats[0].(*tg.Channel)
	if !gotChannel.GetHasLink() {
		t.Fatalf("broadcast channel = %+v, want has_link", gotChannel)
	}
	if ok, err := r.onChannelsSetDiscussionGroup(WithUserID(ctx, ownerID), &tg.ChannelsSetDiscussionGroupRequest{
		Broadcast: &tg.InputChannelEmpty{},
		Group:     inputGroup,
	}); err != nil || !ok {
		t.Fatalf("unlink discussion group from group side = ok %v err %v, want true", ok, err)
	}
	fullBroadcast, err = r.onChannelsGetFullChannel(WithUserID(ctx, ownerID), inputBroadcast)
	if err != nil {
		t.Fatalf("get full broadcast after unlink: %v", err)
	}
	if linkedID, ok := fullBroadcast.FullChat.(*tg.ChannelFull).GetLinkedChatID(); ok || linkedID != 0 {
		t.Fatalf("broadcast linked_chat_id after unlink = %d ok %v, want unset", linkedID, ok)
	}
	if _, err := r.onChannelsSetDiscussionGroup(WithUserID(ctx, ownerID), &tg.ChannelsSetDiscussionGroupRequest{
		Broadcast: &tg.InputChannelEmpty{},
		Group:     inputGroup,
	}); err == nil || !strings.Contains(err.Error(), "LINK_NOT_MODIFIED") {
		t.Fatalf("repeat unlink err = %v, want LINK_NOT_MODIFIED", err)
	}
	if _, err := channelService.SetPreHistoryHidden(ctx, ownerID, group.Channel.ID, true); err != nil {
		t.Fatalf("hide group prehistory: %v", err)
	}
	if _, err := r.onChannelsSetDiscussionGroup(WithUserID(ctx, ownerID), &tg.ChannelsSetDiscussionGroupRequest{
		Broadcast: inputBroadcast,
		Group:     inputGroup,
	}); err == nil || !strings.Contains(err.Error(), "MEGAGROUP_PREHISTORY_HIDDEN") {
		t.Fatalf("hidden group link err = %v, want MEGAGROUP_PREHISTORY_HIDDEN", err)
	}
}

func TestChannelDiscussionRepliesRPCUsesLinkedMegagroup(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, _ := userStore.Create(ctx, domain.User{AccessHash: 91, Phone: "15550002911", FirstName: "Owner"})
	member, _ := userStore.Create(ctx, domain.User{AccessHash: 92, Phone: "15550002912", FirstName: "Member"})
	channelStore := memory.NewChannelStore()
	channelService := appchannels.NewService(channelStore)
	r := New(Config{}, Deps{
		Users:    appusers.NewService(userStore),
		Channels: channelService,
	}, zaptest.NewLogger(t), clock.System)
	broadcast, err := channelService.CreateChannel(ctx, owner.ID, domain.CreateChannelRequest{
		Title:     "Discussion Source",
		Broadcast: true,
		Date:      1700002911,
	})
	if err != nil {
		t.Fatalf("create broadcast: %v", err)
	}
	group, err := channelService.CreateMegagroupFromCreateChat(ctx, owner.ID, domain.CreateChannelRequest{
		Title:         "Discussion Replies",
		MemberUserIDs: []int64{member.ID},
		Date:          1700002912,
	})
	if err != nil {
		t.Fatalf("create group: %v", err)
	}
	inputBroadcast := &tg.InputChannel{ChannelID: broadcast.Channel.ID, AccessHash: broadcast.Channel.AccessHash}
	inputGroup := &tg.InputChannel{ChannelID: group.Channel.ID, AccessHash: group.Channel.AccessHash}
	if ok, err := r.onChannelsSetDiscussionGroup(WithUserID(ctx, owner.ID), &tg.ChannelsSetDiscussionGroupRequest{
		Broadcast: inputBroadcast,
		Group:     inputGroup,
	}); err != nil || !ok {
		t.Fatalf("set discussion group = ok %v err %v, want true", ok, err)
	}

	postUpdates, err := r.onMessagesSendMessage(WithUserID(ctx, owner.ID), &tg.MessagesSendMessageRequest{
		Peer:     &tg.InputPeerChannel{ChannelID: broadcast.Channel.ID, AccessHash: broadcast.Channel.AccessHash},
		Message:  "channel post",
		RandomID: 2911001,
	})
	if err != nil {
		t.Fatalf("send broadcast post: %v", err)
	}
	post := postUpdates.(*tg.Updates).Updates[1].(*tg.UpdateNewChannelMessage).Message.(*tg.Message)
	if !post.Post {
		t.Fatalf("broadcast post = %#v, want channel post", post)
	}
	discussion, err := r.onMessagesGetDiscussionMessage(WithUserID(ctx, owner.ID), &tg.MessagesGetDiscussionMessageRequest{
		Peer:  &tg.InputPeerChannel{ChannelID: broadcast.Channel.ID, AccessHash: broadcast.Channel.AccessHash},
		MsgID: post.ID,
	})
	if err != nil {
		t.Fatalf("get discussion message: %v", err)
	}
	if len(discussion.Messages) != 1 || len(discussion.Chats) != 2 {
		t.Fatalf("discussion = %+v, want linked root message with source and group chats", discussion)
	}
	root, ok := discussion.Messages[0].(*tg.Message)
	if !ok {
		t.Fatalf("discussion root = %T, want message", discussion.Messages[0])
	}
	if peer, ok := root.PeerID.(*tg.PeerChannel); !ok || peer.ChannelID != group.Channel.ID {
		t.Fatalf("discussion root peer = %#v, want linked group %d", root.PeerID, group.Channel.ID)
	}
	if from, ok := root.FromID.(*tg.PeerChannel); !ok || from.ChannelID != broadcast.Channel.ID {
		t.Fatalf("discussion root from = %#v, want source channel %d", root.FromID, broadcast.Channel.ID)
	}
	fwd, ok := root.GetFwdFrom()
	if !ok {
		t.Fatalf("discussion root fwd_from missing")
	}
	if channelPost, ok := fwd.GetChannelPost(); !ok || channelPost != post.ID {
		t.Fatalf("discussion root channel_post = %d ok %v, want %d", channelPost, ok, post.ID)
	}
	if savedMsgID, ok := fwd.GetSavedFromMsgID(); !ok || savedMsgID != post.ID {
		t.Fatalf("discussion root saved_from_msg_id = %d ok %v, want %d", savedMsgID, ok, post.ID)
	}
	if savedPeer, ok := fwd.GetSavedFromPeer(); !ok {
		t.Fatalf("discussion root saved_from_peer missing")
	} else if savedChannel, ok := savedPeer.(*tg.PeerChannel); !ok || savedChannel.ChannelID != broadcast.Channel.ID {
		t.Fatalf("discussion root saved_from_peer = %#v, want source channel %d", savedPeer, broadcast.Channel.ID)
	}

	replyTo := &tg.InputReplyToMessage{ReplyToMsgID: root.ID}
	replyUpdates, err := r.onMessagesSendMessage(WithUserID(ctx, member.ID), func() *tg.MessagesSendMessageRequest {
		req := &tg.MessagesSendMessageRequest{
			Peer:     &tg.InputPeerChannel{ChannelID: group.Channel.ID, AccessHash: group.Channel.AccessHash},
			Message:  "discussion reply",
			RandomID: 2911002,
		}
		req.SetReplyTo(replyTo)
		return req
	}())
	if err != nil {
		t.Fatalf("send discussion reply: %v", err)
	}
	comment := replyUpdates.(*tg.Updates).Updates[1].(*tg.UpdateNewChannelMessage).Message.(*tg.Message)
	replies, err := r.onMessagesGetReplies(WithUserID(ctx, owner.ID), &tg.MessagesGetRepliesRequest{
		Peer:  &tg.InputPeerChannel{ChannelID: broadcast.Channel.ID, AccessHash: broadcast.Channel.AccessHash},
		MsgID: post.ID,
		Limit: 10,
	})
	if err != nil {
		t.Fatalf("get replies: %v", err)
	}
	replyMessages, replyChats, _ := searchMessagesPayload(t, replies)
	if len(replyMessages) != 1 || len(replyChats) != 2 {
		t.Fatalf("get replies = %T %+v, want one linked group reply with both channel contexts", replies, replies)
	}
	gotReply := replyMessages[0].(*tg.Message)
	if gotReply.ID != comment.ID || gotReply.Message != "discussion reply" {
		t.Fatalf("reply message = %#v, want comment id %d", gotReply, comment.ID)
	}
	if peer, ok := gotReply.PeerID.(*tg.PeerChannel); !ok || peer.ChannelID != group.Channel.ID {
		t.Fatalf("reply peer = %#v, want linked group %d", gotReply.PeerID, group.Channel.ID)
	}
	header, ok := gotReply.ReplyTo.(*tg.MessageReplyHeader)
	if !ok {
		t.Fatalf("reply header = %#v, want messageReplyHeader", gotReply.ReplyTo)
	}
	topID, topOK := header.GetReplyToTopID()
	if header.ReplyToMsgID != root.ID || !topOK || topID != root.ID {
		t.Fatalf("reply header = %#v, want msg/top %d", header, root.ID)
	}
	views, err := r.onMessagesGetMessagesViews(WithUserID(ctx, owner.ID), &tg.MessagesGetMessagesViewsRequest{
		Peer: &tg.InputPeerChannel{ChannelID: broadcast.Channel.ID, AccessHash: broadcast.Channel.AccessHash},
		ID:   []int{post.ID},
	})
	if err != nil {
		t.Fatalf("get message views: %v", err)
	}
	replyInfo, ok := views.Views[0].GetReplies()
	if !ok || !replyInfo.Comments || replyInfo.Replies != 1 {
		t.Fatalf("message views replies = %+v ok %v, want one comment", replyInfo, ok)
	}
	if channelID, ok := replyInfo.GetChannelID(); !ok || channelID != group.Channel.ID {
		t.Fatalf("message views channel_id = %d ok %v, want %d", channelID, ok, group.Channel.ID)
	}
	if maxID, ok := replyInfo.GetMaxID(); !ok || maxID != comment.ID {
		t.Fatalf("message views max_id = %d ok %v, want %d", maxID, ok, comment.ID)
	}
	if ok, err := r.onMessagesReadDiscussion(WithUserID(ctx, owner.ID), &tg.MessagesReadDiscussionRequest{
		Peer:      &tg.InputPeerChannel{ChannelID: broadcast.Channel.ID, AccessHash: broadcast.Channel.AccessHash},
		MsgID:     post.ID,
		ReadMaxID: comment.ID,
	}); err != nil || !ok {
		t.Fatalf("read discussion = ok %v err %v, want true", ok, err)
	}
	afterRead, err := r.onMessagesGetDiscussionMessage(WithUserID(ctx, owner.ID), &tg.MessagesGetDiscussionMessageRequest{
		Peer:  &tg.InputPeerChannel{ChannelID: broadcast.Channel.ID, AccessHash: broadcast.Channel.AccessHash},
		MsgID: post.ID,
	})
	if err != nil {
		t.Fatalf("get discussion after read: %v", err)
	}
	if afterRead.ReadInboxMaxID != comment.ID || afterRead.UnreadCount != 0 {
		t.Fatalf("discussion after read = %+v, want read inbox %d and no unread", afterRead, comment.ID)
	}
}

func TestChannelUnreadMentionsRPCUsesMentionState(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, _ := userStore.Create(ctx, domain.User{AccessHash: 9101, Phone: "15550009101", FirstName: "Owner", Username: "owner_mention"})
	member, _ := userStore.Create(ctx, domain.User{AccessHash: 9102, Phone: "15550009102", FirstName: "Mentioned", Username: "mention_friend"})
	channelStore := memory.NewChannelStore()
	channelService := appchannels.NewService(channelStore)
	r := New(Config{}, Deps{
		Users:    appusers.NewService(userStore),
		Channels: channelService,
	}, zaptest.NewLogger(t), clock.System)
	created, err := channelService.CreateMegagroupFromCreateChat(ctx, owner.ID, domain.CreateChannelRequest{
		Title:         "Mention RPC",
		MemberUserIDs: []int64{member.ID},
		Date:          1700009101,
	})
	if err != nil {
		t.Fatalf("create megagroup: %v", err)
	}
	peer := &tg.InputPeerChannel{ChannelID: created.Channel.ID, AccessHash: created.Channel.AccessHash}
	if _, err := r.onMessagesSendMessage(WithUserID(ctx, owner.ID), &tg.MessagesSendMessageRequest{
		Peer:     peer,
		Message:  "hello @mention_friend",
		RandomID: 9102001,
	}); err != nil {
		t.Fatalf("send mention: %v", err)
	}
	mentions, err := r.onMessagesGetUnreadMentions(WithUserID(ctx, member.ID), &tg.MessagesGetUnreadMentionsRequest{
		Peer:      peer,
		OffsetID:  1,
		AddOffset: -10,
		Limit:     10,
	})
	if err != nil {
		t.Fatalf("messages.getUnreadMentions: %v", err)
	}
	mentionMessages, _, _ := searchMessagesPayload(t, mentions)
	if len(mentionMessages) != 1 {
		t.Fatalf("messages.getUnreadMentions = %T %+v, want one unread mention", mentions, mentions)
	}
	if msg := mentionMessages[0].(*tg.Message); msg.Message != "hello @mention_friend" {
		t.Fatalf("mention message = %#v, want sent mention", msg)
	}
	read, err := r.onMessagesReadMentions(WithUserID(ctx, member.ID), &tg.MessagesReadMentionsRequest{Peer: peer})
	if err != nil {
		t.Fatalf("messages.readMentions: %v", err)
	}
	if read.Pts <= 0 || read.PtsCount != 0 || read.Offset != 0 {
		t.Fatalf("messages.readMentions = %+v, want current channel pts and no offset", read)
	}
	mentions, err = r.onMessagesGetUnreadMentions(WithUserID(ctx, member.ID), &tg.MessagesGetUnreadMentionsRequest{
		Peer:  peer,
		Limit: 10,
	})
	if err != nil {
		t.Fatalf("messages.getUnreadMentions after read: %v", err)
	}
	mentionMessages, _, _ = searchMessagesPayload(t, mentions)
	if got := len(mentionMessages); got != 0 {
		t.Fatalf("unread mentions after read = %d, want 0", got)
	}
}

func TestChannelsGetChannelsRejectsHugeVector(t *testing.T) {
	r := New(Config{DC: 2, IP: "127.0.0.1", Port: 2398}, Deps{}, zaptest.NewLogger(t), clock.System)
	ids := make([]tg.InputChannelClass, maxGetMessagesIDs+1)
	for i := range ids {
		ids[i] = &tg.InputChannel{ChannelID: int64(i + 1)}
	}
	if _, err := r.onChannelsGetChannels(context.Background(), ids); err == nil || !strings.Contains(err.Error(), "LIMIT_INVALID") {
		t.Fatalf("channels.getChannels huge vector err = %v, want LIMIT_INVALID", err)
	}
}

func TestChannelsSearchPostsReturnsPublicPostsWithSeekPaging(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, _ := userStore.Create(ctx, domain.User{AccessHash: 91001, Phone: "15550091001", FirstName: "Owner"})
	viewer, _ := userStore.Create(ctx, domain.User{AccessHash: 91002, Phone: "15550091002", FirstName: "Viewer"})
	channelStore := memory.NewChannelStore()
	channelService := appchannels.NewService(channelStore)
	r := New(Config{}, Deps{
		Users:    appusers.NewService(userStore),
		Channels: channelService,
	}, zaptest.NewLogger(t), clock.System)
	public, err := channelService.CreateChannel(ctx, owner.ID, domain.CreateChannelRequest{
		Title:     "Public Search",
		Broadcast: true,
		Date:      1700010000,
	})
	if err != nil {
		t.Fatalf("create public channel: %v", err)
	}
	if _, err := channelService.UpdateUsername(ctx, owner.ID, domain.UpdateChannelUsernameRequest{
		UserID:    owner.ID,
		ChannelID: public.Channel.ID,
		Username:  "public_search_posts",
	}); err != nil {
		t.Fatalf("publish channel username: %v", err)
	}
	private, err := channelService.CreateChannel(ctx, owner.ID, domain.CreateChannelRequest{
		Title:     "Private Search",
		Broadcast: true,
		Date:      1700010001,
	})
	if err != nil {
		t.Fatalf("create private channel: %v", err)
	}
	if _, err := channelService.SendMessage(ctx, owner.ID, domain.SendChannelMessageRequest{
		ChannelID: public.Channel.ID,
		RandomID:  101,
		Message:   "launch alpha #ops",
		Date:      1700010010,
	}); err != nil {
		t.Fatalf("send public first: %v", err)
	}
	if _, err := channelService.SendMessage(ctx, owner.ID, domain.SendChannelMessageRequest{
		ChannelID: public.Channel.ID,
		RandomID:  102,
		Message:   "launch beta #ops",
		Date:      1700010020,
	}); err != nil {
		t.Fatalf("send public second: %v", err)
	}
	if _, err := channelService.SendMessage(ctx, owner.ID, domain.SendChannelMessageRequest{
		ChannelID: private.Channel.ID,
		RandomID:  103,
		Message:   "launch private #ops",
		Date:      1700010030,
	}); err != nil {
		t.Fatalf("send private: %v", err)
	}

	req := &tg.ChannelsSearchPostsRequest{
		OffsetPeer: &tg.InputPeerEmpty{},
		Limit:      1,
	}
	req.SetQuery("launch")
	got, err := r.onChannelsSearchPosts(WithUserID(ctx, viewer.ID), req)
	if err != nil {
		t.Fatalf("channels.searchPosts first page: %v", err)
	}
	slice, ok := got.(*tg.MessagesMessagesSlice)
	if !ok {
		t.Fatalf("first page = %T %+v, want messagesSlice", got, got)
	}
	if slice.Count <= len(slice.Messages) {
		t.Fatalf("first page count = %d messages=%d, want more page", slice.Count, len(slice.Messages))
	}
	if nextRate, ok := slice.GetNextRate(); !ok || nextRate != 1700010020 {
		t.Fatalf("first page next_rate = %d ok %v, want newest message date", nextRate, ok)
	}
	if flood, ok := slice.GetSearchFlood(); !ok || !flood.QueryIsFree {
		t.Fatalf("first page search_flood = %+v ok %v, want free flood state", flood, ok)
	}
	messages, chats, users := searchMessagesPayload(t, got)
	if len(messages) != 1 || len(chats) != 1 || len(users) != 1 {
		t.Fatalf("first page payload messages=%d chats=%d users=%d, want 1/1/1", len(messages), len(chats), len(users))
	}
	first := messages[0].(*tg.Message)
	if first.Message != "launch beta #ops" {
		t.Fatalf("first result message = %q, want newest public hit", first.Message)
	}
	if peer, ok := first.PeerID.(*tg.PeerChannel); !ok || peer.ChannelID != public.Channel.ID {
		t.Fatalf("first result peer = %#v, want public channel %d", first.PeerID, public.Channel.ID)
	}

	page2 := &tg.ChannelsSearchPostsRequest{
		OffsetRate: slice.NextRate,
		OffsetPeer: &tg.InputPeerChannel{
			ChannelID:  public.Channel.ID,
			AccessHash: public.Channel.AccessHash,
		},
		OffsetID: first.ID,
		Limit:    10,
	}
	page2.SetQuery("launch")
	got, err = r.onChannelsSearchPosts(WithUserID(ctx, viewer.ID), page2)
	if err != nil {
		t.Fatalf("channels.searchPosts second page: %v", err)
	}
	messages, chats, _ = searchMessagesPayload(t, got)
	if len(messages) != 1 || len(chats) != 1 {
		t.Fatalf("second page payload messages=%d chats=%d, want only older public hit", len(messages), len(chats))
	}
	if msg := messages[0].(*tg.Message); msg.Message != "launch alpha #ops" {
		t.Fatalf("second result message = %q, want older public hit", msg.Message)
	}

	hashtagReq := &tg.ChannelsSearchPostsRequest{
		OffsetPeer: &tg.InputPeerEmpty{},
		Limit:      10,
	}
	hashtagReq.SetHashtag("ops")
	got, err = r.onChannelsSearchPosts(WithUserID(ctx, viewer.ID), hashtagReq)
	if err != nil {
		t.Fatalf("channels.searchPosts hashtag: %v", err)
	}
	messages, _, _ = searchMessagesPayload(t, got)
	if len(messages) != 2 {
		t.Fatalf("hashtag results = %d, want two public hits only", len(messages))
	}
	for _, item := range messages {
		if strings.Contains(item.(*tg.Message).Message, "private") {
			t.Fatalf("hashtag leaked private message: %#v", item)
		}
	}
}

func TestPublicChannelPreviewRPCsAllowNonMember(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, _ := userStore.Create(ctx, domain.User{AccessHash: 92001, Phone: "15550092001", FirstName: "Owner"})
	viewer, _ := userStore.Create(ctx, domain.User{AccessHash: 92002, Phone: "15550092002", FirstName: "Viewer"})
	channelStore := memory.NewChannelStore()
	channelService := appchannels.NewService(channelStore)
	dialogService := appdialogs.NewService(memory.NewDialogStore(), channelStore)
	r := New(Config{}, Deps{
		Users:    appusers.NewService(userStore),
		Channels: channelService,
		Dialogs:  dialogService,
	}, zaptest.NewLogger(t), clock.System)
	public, err := channelService.CreateChannel(ctx, owner.ID, domain.CreateChannelRequest{
		Title:     "Public Preview RPC",
		Broadcast: true,
		Date:      1700010100,
	})
	if err != nil {
		t.Fatalf("create public channel: %v", err)
	}
	if _, err := channelService.UpdateUsername(ctx, owner.ID, domain.UpdateChannelUsernameRequest{
		UserID:    owner.ID,
		ChannelID: public.Channel.ID,
		Username:  "public_preview_rpc",
	}); err != nil {
		t.Fatalf("publish channel username: %v", err)
	}
	sent, err := channelService.SendMessage(ctx, owner.ID, domain.SendChannelMessageRequest{
		ChannelID: public.Channel.ID,
		RandomID:  201,
		Message:   "public preview rpc post",
		Date:      1700010110,
	})
	if err != nil {
		t.Fatalf("send public post: %v", err)
	}
	input := &tg.InputChannel{ChannelID: public.Channel.ID, AccessHash: public.Channel.AccessHash}
	peer := &tg.InputPeerChannel{ChannelID: public.Channel.ID, AccessHash: public.Channel.AccessHash}

	full, err := r.onChannelsGetFullChannel(WithUserID(ctx, viewer.ID), input)
	if err != nil {
		t.Fatalf("non-member getFullChannel public preview: %v", err)
	}
	if len(full.Chats) != 1 {
		t.Fatalf("full chats = %d, want one channel", len(full.Chats))
	}
	chat, ok := full.Chats[0].(*tg.Channel)
	if !ok || !chat.Left || chat.ID != public.Channel.ID {
		t.Fatalf("full channel chat = %T %+v, want left public channel", full.Chats[0], full.Chats[0])
	}
	channelFull, ok := full.FullChat.(*tg.ChannelFull)
	if !ok || channelFull.ID != public.Channel.ID || channelFull.UnreadCount != 0 {
		t.Fatalf("full chat = %T %+v, want channel full without unread", full.FullChat, full.FullChat)
	}

	sendAs, err := r.onChannelsGetSendAs(WithUserID(ctx, viewer.ID), &tg.ChannelsGetSendAsRequest{Peer: peer})
	if err != nil {
		t.Fatalf("non-member getSendAs public preview: %v", err)
	}
	if len(sendAs.Peers) != 1 {
		t.Fatalf("sendAs peers = %+v, want only current user peer", sendAs.Peers)
	}
	if len(sendAs.Chats) != 1 {
		t.Fatalf("sendAs chats = %d, want public channel chat", len(sendAs.Chats))
	}

	historyReq := &tg.MessagesGetHistoryRequest{Peer: peer, Limit: 10}
	var in bin.Buffer
	if err := historyReq.Encode(&in); err != nil {
		t.Fatalf("encode getHistory: %v", err)
	}
	enc, err := r.Dispatch(WithUserID(ctx, viewer.ID), [8]byte{}, 0, &in)
	if err != nil {
		t.Fatalf("dispatch getHistory public preview: %v", err)
	}
	box, ok := enc.(*tg.MessagesMessagesBox)
	if !ok {
		t.Fatalf("getHistory response = %T, want boxed messages", enc)
	}
	history, ok := box.Messages.(*tg.MessagesChannelMessages)
	if !ok {
		t.Fatalf("boxed getHistory = %T, want channel messages", box.Messages)
	}
	foundPost := false
	for _, item := range history.Messages {
		if msg, ok := item.(*tg.Message); ok && msg.Message == "public preview rpc post" {
			foundPost = true
		}
	}
	if !foundPost {
		t.Fatalf("history messages = %+v, want public preview post", history.Messages)
	}
	if len(history.Chats) != 1 {
		t.Fatalf("history chats = %d, want public channel chat", len(history.Chats))
	}
	historyChat, ok := history.Chats[0].(*tg.Channel)
	if !ok || !historyChat.Left || historyChat.ID != public.Channel.ID {
		t.Fatalf("history chat = %T %+v, want left public channel", history.Chats[0], history.Chats[0])
	}

	diff, err := r.onUpdatesGetChannelDifference(WithUserID(ctx, viewer.ID), &tg.UpdatesGetChannelDifferenceRequest{
		Channel: input,
		Pts:     public.Event.Pts,
		Limit:   10,
	})
	if err != nil {
		t.Fatalf("non-member getChannelDifference public preview: %v", err)
	}
	fullDiff, ok := diff.(*tg.UpdatesChannelDifference)
	if !ok || !fullDiff.Final || fullDiff.Pts != sent.Event.Pts || len(fullDiff.NewMessages) != 1 {
		t.Fatalf("channel difference = %T %+v, want one public preview message at current pts", diff, diff)
	}
	diffMsg, ok := fullDiff.NewMessages[0].(*tg.Message)
	if !ok || diffMsg.Message != "public preview rpc post" {
		t.Fatalf("channel difference message = %T %+v, want public preview rpc post", fullDiff.NewMessages[0], fullDiff.NewMessages[0])
	}
	if len(fullDiff.Chats) != 1 {
		t.Fatalf("channel difference chats = %d, want public channel chat", len(fullDiff.Chats))
	}
	diffChat, ok := fullDiff.Chats[0].(*tg.Channel)
	if !ok || !diffChat.Left || diffChat.ID != public.Channel.ID {
		t.Fatalf("channel difference chat = %T %+v, want left public channel", fullDiff.Chats[0], fullDiff.Chats[0])
	}

	domainPeers, err := r.dialogPeersFromInput(WithUserID(ctx, viewer.ID), viewer.ID, []tg.InputDialogPeerClass{&tg.InputDialogPeer{Peer: peer}})
	if err != nil {
		t.Fatalf("dialog peer conversion public preview: %v", err)
	}
	if len(domainPeers) != 1 || domainPeers[0].Type != domain.PeerTypeChannel || domainPeers[0].ID != public.Channel.ID {
		t.Fatalf("domain peers = %+v, want public channel peer", domainPeers)
	}
	directPeerDialogs, err := dialogService.GetPeerDialogs(ctx, viewer.ID, domainPeers)
	if err != nil {
		t.Fatalf("dialog service public preview: %v", err)
	}
	if len(directPeerDialogs.Dialogs) != 1 || len(directPeerDialogs.ChannelMessages) != 1 || len(directPeerDialogs.Channels) != 1 {
		t.Fatalf("direct peer dialogs = %+v, want one dialog/message/channel", directPeerDialogs)
	}

	peerDialogsReq := &tg.MessagesGetPeerDialogsRequest{
		Peers: []tg.InputDialogPeerClass{&tg.InputDialogPeer{Peer: peer}},
	}
	var peerDialogsIn bin.Buffer
	if err := peerDialogsReq.Encode(&peerDialogsIn); err != nil {
		t.Fatalf("encode getPeerDialogs: %v", err)
	}
	peerDialogsEnc, err := r.Dispatch(WithUserID(ctx, viewer.ID), [8]byte{}, 0, &peerDialogsIn)
	if err != nil {
		t.Fatalf("dispatch getPeerDialogs public preview: %v", err)
	}
	peerDialogs, ok := peerDialogsEnc.(*tg.MessagesPeerDialogs)
	if !ok {
		t.Fatalf("getPeerDialogs response = %T, want peer dialogs", peerDialogsEnc)
	}
	if len(peerDialogs.Dialogs) != 1 || len(peerDialogs.Messages) != 1 || len(peerDialogs.Chats) != 1 {
		t.Fatalf("peer dialogs = %+v, want one dialog/message/channel", peerDialogs)
	}
	tgDialog, ok := peerDialogs.Dialogs[0].(*tg.Dialog)
	if !ok || tgDialog.TopMessage <= 0 || tgDialog.UnreadCount != 0 {
		t.Fatalf("peer dialog = %T %+v, want read-only public preview dialog", peerDialogs.Dialogs[0], peerDialogs.Dialogs[0])
	}
	tgMessage, ok := peerDialogs.Messages[0].(*tg.Message)
	if !ok || tgMessage.Message != "public preview rpc post" {
		t.Fatalf("peer dialog message = %T %+v, want public preview rpc post", peerDialogs.Messages[0], peerDialogs.Messages[0])
	}
	peerDialogChat, ok := peerDialogs.Chats[0].(*tg.Channel)
	if !ok || !peerDialogChat.Left || peerDialogChat.ID != public.Channel.ID {
		t.Fatalf("peer dialog chat = %T %+v, want left public channel", peerDialogs.Chats[0], peerDialogs.Chats[0])
	}
}

func TestMessagesSearchGlobalReturnsJoinedChannelMessages(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, _ := userStore.Create(ctx, domain.User{AccessHash: 91101, Phone: "15550091101", FirstName: "Owner"})
	viewer, _ := userStore.Create(ctx, domain.User{AccessHash: 91102, Phone: "15550091102", FirstName: "Viewer"})
	channelStore := memory.NewChannelStore()
	channelService := appchannels.NewService(channelStore)
	r := New(Config{}, Deps{
		Users:    appusers.NewService(userStore),
		Channels: channelService,
	}, zaptest.NewLogger(t), clock.System)
	joined, err := channelService.CreateChannel(ctx, owner.ID, domain.CreateChannelRequest{
		Title:     "Joined Broadcast",
		Broadcast: true,
		Date:      1700020000,
	})
	if err != nil {
		t.Fatalf("create joined channel: %v", err)
	}
	if _, err := channelService.InviteToChannel(ctx, owner.ID, joined.Channel.ID, []int64{viewer.ID}, 1700020001); err != nil {
		t.Fatalf("invite viewer: %v", err)
	}
	hidden, err := channelService.CreateChannel(ctx, owner.ID, domain.CreateChannelRequest{
		Title:     "Hidden Broadcast",
		Broadcast: true,
		Date:      1700020002,
	})
	if err != nil {
		t.Fatalf("create hidden channel: %v", err)
	}
	for i, body := range []string{"global needle older", "global needle newer"} {
		if _, err := channelService.SendMessage(ctx, owner.ID, domain.SendChannelMessageRequest{
			ChannelID: joined.Channel.ID,
			RandomID:  int64(201 + i),
			Message:   body,
			Date:      1700020010 + i*10,
		}); err != nil {
			t.Fatalf("send joined %d: %v", i, err)
		}
	}
	if _, err := channelService.SendMessage(ctx, owner.ID, domain.SendChannelMessageRequest{
		ChannelID: hidden.Channel.ID,
		RandomID:  301,
		Message:   "global needle hidden",
		Date:      1700020030,
	}); err != nil {
		t.Fatalf("send hidden: %v", err)
	}

	req := &tg.MessagesSearchGlobalRequest{
		BroadcastsOnly: true,
		Q:              "global needle",
		Filter:         &tg.InputMessagesFilterEmpty{},
		OffsetPeer:     &tg.InputPeerEmpty{},
		Limit:          1,
	}
	got, err := r.onMessagesSearchGlobal(WithUserID(ctx, viewer.ID), req)
	if err != nil {
		t.Fatalf("messages.searchGlobal first page: %v", err)
	}
	slice, ok := got.(*tg.MessagesMessagesSlice)
	if !ok {
		t.Fatalf("first page = %T %+v, want messagesSlice", got, got)
	}
	if nextRate, ok := slice.GetNextRate(); !ok || nextRate != 1700020020 {
		t.Fatalf("first page next_rate = %d ok %v, want newest date", nextRate, ok)
	}
	messages, chats, users := searchMessagesPayload(t, got)
	if len(messages) != 1 || len(chats) != 1 || len(users) != 1 {
		t.Fatalf("first payload messages=%d chats=%d users=%d, want 1/1/1", len(messages), len(chats), len(users))
	}
	first := messages[0].(*tg.Message)
	if first.Message != "global needle newer" {
		t.Fatalf("first message = %q, want newest joined channel hit", first.Message)
	}
	if peer, ok := first.PeerID.(*tg.PeerChannel); !ok || peer.ChannelID != joined.Channel.ID {
		t.Fatalf("first peer = %#v, want joined channel %d", first.PeerID, joined.Channel.ID)
	}

	page2 := &tg.MessagesSearchGlobalRequest{
		BroadcastsOnly: true,
		Q:              "global needle",
		Filter:         &tg.InputMessagesFilterEmpty{},
		OffsetRate:     slice.NextRate,
		OffsetPeer: &tg.InputPeerChannel{
			ChannelID:  joined.Channel.ID,
			AccessHash: joined.Channel.AccessHash,
		},
		OffsetID: first.ID,
		Limit:    10,
	}
	got, err = r.onMessagesSearchGlobal(WithUserID(ctx, viewer.ID), page2)
	if err != nil {
		t.Fatalf("messages.searchGlobal second page: %v", err)
	}
	messages, chats, _ = searchMessagesPayload(t, got)
	if len(messages) != 1 || len(chats) != 1 {
		t.Fatalf("second payload messages=%d chats=%d, want older joined hit only", len(messages), len(chats))
	}
	if msg := messages[0].(*tg.Message); msg.Message != "global needle older" {
		t.Fatalf("second message = %q, want older joined hit", msg.Message)
	}
}

func TestChannelsSearchPostsValidatesStubBounds(t *testing.T) {
	const userID = int64(1000000001)
	r := New(Config{}, Deps{}, zaptest.NewLogger(t), clock.System)

	valid := &tg.ChannelsSearchPostsRequest{
		OffsetPeer: &tg.InputPeerEmpty{},
		Limit:      20,
	}
	valid.SetQuery("launch")
	got, err := r.onChannelsSearchPosts(WithUserID(context.Background(), userID), valid)
	if err != nil {
		t.Fatalf("channels.searchPosts valid stub: %v", err)
	}
	if page, ok := got.(*tg.MessagesMessages); !ok || len(page.Messages) != 0 || len(page.Chats) != 0 || len(page.Users) != 0 {
		t.Fatalf("channels.searchPosts = %T %+v, want empty messages", got, got)
	}

	both := &tg.ChannelsSearchPostsRequest{OffsetPeer: &tg.InputPeerEmpty{}, Limit: 1}
	both.SetQuery("launch")
	both.SetHashtag("launch")
	if _, err := r.onChannelsSearchPosts(WithUserID(context.Background(), userID), both); err == nil || !strings.Contains(err.Error(), "SEARCH_QUERY_EMPTY") {
		t.Fatalf("channels.searchPosts query+hashtag err = %v, want SEARCH_QUERY_EMPTY", err)
	}
	if _, err := r.onChannelsSearchPosts(WithUserID(context.Background(), userID), &tg.ChannelsSearchPostsRequest{OffsetPeer: &tg.InputPeerEmpty{}, Limit: 1}); err == nil || !strings.Contains(err.Error(), "SEARCH_QUERY_EMPTY") {
		t.Fatalf("channels.searchPosts empty query err = %v, want SEARCH_QUERY_EMPTY", err)
	}

	huge := &tg.ChannelsSearchPostsRequest{OffsetPeer: &tg.InputPeerEmpty{}, Limit: 1}
	huge.SetQuery(strings.Repeat("x", maxChannelSearchPostsQuery+1))
	if _, err := r.onChannelsSearchPosts(WithUserID(context.Background(), userID), huge); err == nil || !strings.Contains(err.Error(), "LIMIT_INVALID") {
		t.Fatalf("channels.searchPosts huge query err = %v, want LIMIT_INVALID", err)
	}

	badOffset := &tg.ChannelsSearchPostsRequest{OffsetPeer: &tg.InputPeerEmpty{}, Limit: 1, OffsetID: domain.MaxMessageBoxID + 1}
	badOffset.SetHashtag("launch")
	if _, err := r.onChannelsSearchPosts(WithUserID(context.Background(), userID), badOffset); err == nil || !strings.Contains(err.Error(), "MESSAGE_ID_INVALID") {
		t.Fatalf("channels.searchPosts huge offset_id err = %v, want MESSAGE_ID_INVALID", err)
	}

	badStars := &tg.ChannelsSearchPostsRequest{OffsetPeer: &tg.InputPeerEmpty{}, Limit: 1, AllowPaidStars: -1}
	badStars.SetQuery("launch")
	if _, err := r.onChannelsSearchPosts(WithUserID(context.Background(), userID), badStars); err == nil || !strings.Contains(err.Error(), "LIMIT_INVALID") {
		t.Fatalf("channels.searchPosts negative stars err = %v, want LIMIT_INVALID", err)
	}

	badPeer := &tg.ChannelsSearchPostsRequest{OffsetPeer: &tg.InputPeerUser{UserID: 42}, Limit: 1}
	badPeer.SetQuery("launch")
	if _, err := r.onChannelsSearchPosts(WithUserID(context.Background(), userID), badPeer); err == nil || !strings.Contains(err.Error(), "PEER_ID_INVALID") {
		t.Fatalf("channels.searchPosts user offset peer err = %v, want PEER_ID_INVALID", err)
	}

	floodReq := &tg.ChannelsCheckSearchPostsFloodRequest{}
	floodReq.SetQuery("launch")
	flood, err := r.onChannelsCheckSearchPostsFlood(WithUserID(context.Background(), userID), floodReq)
	if err != nil {
		t.Fatalf("channels.checkSearchPostsFlood: %v", err)
	}
	if !flood.QueryIsFree || flood.Remains <= 0 || flood.TotalDaily <= 0 {
		t.Fatalf("channels.checkSearchPostsFlood = %+v, want free quota", flood)
	}

	if _, err := r.onChannelsCheckSearchPostsFlood(WithUserID(context.Background(), userID), &tg.ChannelsCheckSearchPostsFloodRequest{}); err == nil || !strings.Contains(err.Error(), "SEARCH_QUERY_EMPTY") {
		t.Fatalf("channels.checkSearchPostsFlood empty query err = %v, want SEARCH_QUERY_EMPTY", err)
	}
	floodHuge := &tg.ChannelsCheckSearchPostsFloodRequest{}
	floodHuge.SetQuery(strings.Repeat("x", maxChannelSearchPostsQuery+1))
	if _, err := r.onChannelsCheckSearchPostsFlood(WithUserID(context.Background(), userID), floodHuge); err == nil || !strings.Contains(err.Error(), "LIMIT_INVALID") {
		t.Fatalf("channels.checkSearchPostsFlood huge query err = %v, want LIMIT_INVALID", err)
	}
}

func TestMessagesGetPeerDialogsReturnsRequestedDialogsAndState(t *testing.T) {
	msg := domain.Message{
		ID:          3,
		OwnerUserID: 1000000001,
		Peer:        domain.Peer{Type: domain.PeerTypeUser, ID: domain.OfficialSystemUserID},
		From:        domain.Peer{Type: domain.PeerTypeUser, ID: domain.OfficialSystemUserID},
		Date:        1700000100,
		Body:        "Login code: 12345",
	}
	dialogs := &captureDialogs{peerList: domain.DialogList{
		Dialogs: []domain.Dialog{{
			Peer:           msg.Peer,
			TopMessage:     msg.ID,
			TopMessageDate: msg.Date,
			UnreadCount:    1,
		}},
		Messages: []domain.Message{msg},
		Users:    []domain.User{domain.OfficialSystemUser()},
	}}
	updates := &captureUpdates{state: domain.UpdateState{Pts: 8, Date: 1700000200, Seq: 4}}
	r := New(Config{}, Deps{Dialogs: dialogs, Updates: updates}, zaptest.NewLogger(t), clock.System)
	req := &tg.MessagesGetPeerDialogsRequest{
		Peers: []tg.InputDialogPeerClass{&tg.InputDialogPeer{
			Peer: &tg.InputPeerUser{UserID: domain.OfficialSystemUserID, AccessHash: domain.OfficialSystemUser().AccessHash},
		}},
	}
	var in bin.Buffer
	if err := req.Encode(&in); err != nil {
		t.Fatalf("encode request: %v", err)
	}

	enc, err := r.Dispatch(WithUserID(context.Background(), 1000000001), [8]byte{}, 0, &in)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	got, ok := enc.(*tg.MessagesPeerDialogs)
	if !ok {
		t.Fatalf("response = %T, want *tg.MessagesPeerDialogs", enc)
	}
	if len(dialogs.peers) != 1 || dialogs.peers[0].ID != domain.OfficialSystemUserID {
		t.Fatalf("requested peers = %+v, want official peer", dialogs.peers)
	}
	if got.State.Pts != 8 || got.State.Seq != 4 {
		t.Fatalf("state = %+v, want updates state", got.State)
	}
	if len(got.Dialogs) != 1 || len(got.Messages) != 1 || len(got.Users) != 1 {
		t.Fatalf("peer dialogs = %+v, want one dialog/message/user", got)
	}
}

func TestPushLoginMessageSendsUpdateNewMessage(t *testing.T) {
	sessions := &captureSessions{}
	r := New(Config{}, Deps{Sessions: sessions}, zaptest.NewLogger(t), clock.System)
	msg := domain.Message{
		ID:          99,
		OwnerUserID: 1000000001,
		Peer:        domain.Peer{Type: domain.PeerTypeUser, ID: domain.OfficialSystemUserID},
		From:        domain.Peer{Type: domain.PeerTypeUser, ID: domain.OfficialSystemUserID},
		Date:        1700000100,
		Body:        "Login code: 12345",
		Entities:    []domain.MessageEntity{{Type: domain.MessageEntityBold, Offset: 0, Length: 11}},
	}
	event := domain.UpdateEvent{Type: domain.UpdateEventNewMessage, Pts: 4, PtsCount: 1, Date: msg.Date, Message: msg}
	state := domain.UpdateState{Pts: 4, Date: 1700000100, Seq: 2}

	r.pushLoginMessage(context.Background(), [8]byte{}, 55, event, state)

	gotSession := sessions.snapshot()
	if gotSession.sessionID != 55 || gotSession.messageType != proto.MessageFromServer {
		t.Fatalf("push target = session %d type %v, want session 55 MessageFromServer", gotSession.sessionID, gotSession.messageType)
	}
	got, ok := gotSession.message.(*tg.Updates)
	if !ok {
		t.Fatalf("pushed message = %T, want *tg.Updates", gotSession.message)
	}
	if len(got.Updates) != 1 || len(got.Users) != 1 {
		t.Fatalf("updates payload = %+v, want one update and official user", got)
	}
	update, ok := got.Updates[0].(*tg.UpdateNewMessage)
	if !ok {
		t.Fatalf("update = %T, want *tg.UpdateNewMessage", got.Updates[0])
	}
	if update.Pts != event.Pts || update.PtsCount != event.PtsCount || got.Seq != state.Seq {
		t.Fatalf("update state = pts %d pts_count %d seq %d, want %d/%d/%d", update.Pts, update.PtsCount, got.Seq, event.Pts, event.PtsCount, state.Seq)
	}
	message, ok := update.Message.(*tg.Message)
	if !ok {
		t.Fatalf("update message = %T, want *tg.Message", update.Message)
	}
	if message.ID != msg.ID || message.Message != msg.Body {
		t.Fatalf("message = %+v, want login message %+v", message, msg)
	}
	user, ok := got.Users[0].(*tg.User)
	if !ok || user.ID != domain.OfficialSystemUserID || !user.Verified || !user.Support {
		t.Fatalf("user = %#v, want verified official system user", got.Users[0])
	}
}

func TestSignInServiceNotificationMatchesEnterpriseShape(t *testing.T) {
	var authKeyID [8]byte
	authKeyID[0] = 9
	r := New(Config{}, Deps{}, zaptest.NewLogger(t), clock.System)
	ctx := WithClientInfo(context.Background(), ClientInfo{
		DeviceModel:   "Telegram Desktop",
		SystemVersion: "Windows",
		AppVersion:    "6.8.4",
	})

	got := r.tgSignInServiceNotification(ctx, domain.User{
		ID:        1000000001,
		FirstName: "Test",
		LastName:  "User",
	}, authKeyID)

	if len(got.Updates) != 1 {
		t.Fatalf("updates = %+v, want one service notification", got.Updates)
	}
	update, ok := got.Updates[0].(*tg.UpdateServiceNotification)
	if !ok {
		t.Fatalf("update = %T, want *tg.UpdateServiceNotification", got.Updates[0])
	}
	if update.Popup || update.InboxDate == 0 || update.Media == nil {
		t.Fatalf("notification flags/media = popup %v inbox %d media %T", update.Popup, update.InboxDate, update.Media)
	}
	for _, want := range []string{"New login.", "Test User", "Telegram Desktop", "Settings > Devices"} {
		if !strings.Contains(update.Message, want) {
			t.Fatalf("notification message %q missing %q", update.Message, want)
		}
	}
	if len(update.Entities) < 3 {
		t.Fatalf("entities = %+v, want bold entities for title/settings links", update.Entities)
	}
}

func TestLogOutClearsSessionAndUpdateState(t *testing.T) {
	var authKeyID [8]byte
	authKeyID[0] = 5
	auth := &captureAuthService{}
	updates := &captureUpdates{}
	sessions := &captureSessions{authKeyID: authKeyID, authKeyResolved: true, userID: 1000000001, userResolved: true}
	r := New(Config{}, Deps{
		Auth:     auth,
		Updates:  updates,
		Sessions: sessions,
	}, zaptest.NewLogger(t), clock.System)

	_, err := r.onAuthLogOut(WithAuthKeyID(context.Background(), authKeyID))
	if err != nil {
		t.Fatalf("auth.logOut: %v", err)
	}
	if auth.loggedOutAuthKeyID != authKeyID {
		t.Fatalf("logged out auth key = %x, want %x", auth.loggedOutAuthKeyID, authKeyID)
	}
	if updates.clearedAuthKeyID != authKeyID || !updates.cleared {
		t.Fatalf("cleared auth key = %x cleared=%v, want %x", updates.clearedAuthKeyID, updates.cleared, authKeyID)
	}
	gotSession := sessions.snapshot()
	if gotSession.userID != 0 || !gotSession.userResolved {
		t.Fatalf("session user after logout = %d resolved=%v, want 0/true", gotSession.userID, gotSession.userResolved)
	}
}

func TestSignInDifferentUserClearsAuthKeyUpdateState(t *testing.T) {
	var authKeyID [8]byte
	authKeyID[0] = 6
	auth := &captureAuthService{signInUser: domain.User{ID: 1000000002, FirstName: "Two"}}
	updates := &captureUpdates{}
	sessions := &captureSessions{authKeyID: authKeyID, authKeyResolved: true, userID: 1000000001, userResolved: true}
	r := New(Config{}, Deps{
		Auth:     auth,
		Updates:  updates,
		Sessions: sessions,
	}, zaptest.NewLogger(t), clock.System)
	ctx := WithUserID(WithAuthKeyID(WithSessionID(context.Background(), 77), authKeyID), 1000000001)

	_, err := r.onAuthSignIn(ctx, &tg.AuthSignInRequest{PhoneNumber: "15550000002", PhoneCodeHash: "hash", PhoneCode: "12345"})
	if err != nil {
		t.Fatalf("auth.signIn: %v", err)
	}
	if updates.clearedAuthKeyID != authKeyID || !updates.cleared {
		t.Fatalf("cleared auth key = %x cleared=%v, want %x", updates.clearedAuthKeyID, updates.cleared, authKeyID)
	}
	gotSession := sessions.snapshot()
	if gotSession.userID != 1000000002 {
		t.Fatalf("session user = %d, want new user 1000000002", gotSession.userID)
	}
}

func TestDialogSettingRPCsRecordDurableUpdates(t *testing.T) {
	var authKeyID [8]byte
	authKeyID[0] = 9
	ctx := WithSessionID(WithAuthKeyID(WithUserID(context.Background(), 1000000001), authKeyID), 55)
	peer := domain.Peer{Type: domain.PeerTypeUser, ID: 1000000002}
	dialogPeer := &tg.InputDialogPeer{Peer: &tg.InputPeerUser{UserID: peer.ID}}
	dialogs := &captureDialogs{}
	updates := &captureUpdates{state: domain.UpdateState{Pts: 11, Date: 1700000400, Seq: 0}}
	r := New(Config{}, Deps{Dialogs: dialogs, Updates: updates}, zaptest.NewLogger(t), clock.System)

	toggle := &tg.MessagesToggleDialogPinRequest{Peer: dialogPeer}
	toggle.SetPinned(true)
	if ok, err := r.onMessagesToggleDialogPin(ctx, toggle); err != nil || !ok {
		t.Fatalf("toggle pin = %v, %v", ok, err)
	}
	if len(updates.events) != 1 || updates.events[0].Type != domain.UpdateEventDialogPinned || updates.events[0].Peer != peer || !updates.events[0].Bool || updates.excludeSessionID != 55 {
		t.Fatalf("pin event = %+v, want durable dialog_pinned", updates.events)
	}

	if ok, err := r.onMessagesReorderPinnedDialogs(ctx, &tg.MessagesReorderPinnedDialogsRequest{Order: []tg.InputDialogPeerClass{dialogPeer}}); err != nil || !ok {
		t.Fatalf("reorder pinned = %v, %v", ok, err)
	}
	if len(updates.events) != 2 || updates.events[1].Type != domain.UpdateEventPinnedDialogs || len(updates.events[1].Peers) != 1 || updates.events[1].Peers[0] != peer {
		t.Fatalf("reorder event = %+v, want durable pinned_dialogs", updates.events)
	}

	mark := &tg.MessagesMarkDialogUnreadRequest{Peer: dialogPeer}
	mark.SetUnread(true)
	if ok, err := r.onMessagesMarkDialogUnread(ctx, mark); err != nil || !ok {
		t.Fatalf("mark unread = %v, %v", ok, err)
	}
	if len(updates.events) != 3 || updates.events[2].Type != domain.UpdateEventDialogUnreadMark || updates.events[2].Peer != peer || !updates.events[2].Bool {
		t.Fatalf("unread event = %+v, want durable dialog_unread_mark", updates.events)
	}

	parentMark := &tg.MessagesMarkDialogUnreadRequest{
		ParentPeer: &tg.InputPeerChannel{ChannelID: 9001},
		Peer:       &tg.InputDialogPeer{Peer: &tg.InputPeerSelf{}},
	}
	parentMark.SetUnread(true)
	parentMark.SetParentPeer(parentMark.ParentPeer)
	if ok, err := r.onMessagesMarkDialogUnread(ctx, parentMark); err != nil || !ok {
		t.Fatalf("mark monoforum unread compat = %v, %v", ok, err)
	}
	if len(updates.events) != 3 {
		t.Fatalf("parent_peer unread compat recorded events = %+v, want no durable event", updates.events)
	}

	getParentMarks := &tg.MessagesGetDialogUnreadMarksRequest{ParentPeer: &tg.InputPeerChannel{ChannelID: 9001}}
	getParentMarks.SetParentPeer(getParentMarks.ParentPeer)
	marks, err := r.onMessagesGetDialogUnreadMarks(ctx, getParentMarks)
	if err != nil {
		t.Fatalf("get monoforum unread marks compat: %v", err)
	}
	if len(marks) != 0 {
		t.Fatalf("monoforum unread marks = %+v, want empty compat list", marks)
	}

	invalidParentMarks := &tg.MessagesGetDialogUnreadMarksRequest{ParentPeer: &tg.InputPeerSelf{}}
	invalidParentMarks.SetParentPeer(invalidParentMarks.ParentPeer)
	if _, err := r.onMessagesGetDialogUnreadMarks(ctx, invalidParentMarks); err == nil || !strings.Contains(err.Error(), "PARENT_PEER_INVALID") {
		t.Fatalf("get unread marks invalid parent err = %v, want PARENT_PEER_INVALID", err)
	}

	if ok, err := r.onMessagesHidePeerSettingsBar(ctx, &tg.InputPeerUser{UserID: peer.ID}); err != nil || !ok {
		t.Fatalf("hide peer settings = %v, %v", ok, err)
	}
	if len(updates.events) != 4 || updates.events[3].Type != domain.UpdateEventPeerSettings || updates.events[3].Peer != peer || !updates.events[3].Settings.HiddenPeerSettingsBar {
		t.Fatalf("peer settings event = %+v, want durable peer_settings", updates.events)
	}
}

func TestDialogUnreadMarksIncludeChannelDialogs(t *testing.T) {
	ctx := context.Background()
	channelStore := memory.NewChannelStore()
	channelSvc := appchannels.NewService(channelStore)
	created, err := channelSvc.CreateMegagroupFromCreateChat(ctx, 1000000001, domain.CreateChannelRequest{
		Title: "Unread Mark Channel",
		Date:  1700001000,
	})
	if err != nil {
		t.Fatalf("CreateMegagroupFromCreateChat: %v", err)
	}
	dialogs := appdialogs.NewService(memory.NewDialogStore(), channelStore)
	r := New(Config{}, Deps{
		Channels: channelSvc,
		Dialogs:  dialogs,
	}, zaptest.NewLogger(t), clock.System)
	rpcCtx := WithUserID(ctx, 1000000001)
	input := &tg.InputDialogPeer{Peer: &tg.InputPeerChannel{
		ChannelID:  created.Channel.ID,
		AccessHash: created.Channel.AccessHash,
	}}

	mark := &tg.MessagesMarkDialogUnreadRequest{Peer: input}
	mark.SetUnread(true)
	if ok, err := r.onMessagesMarkDialogUnread(rpcCtx, mark); err != nil || !ok {
		t.Fatalf("mark channel unread = %v, %v", ok, err)
	}
	marks, err := r.onMessagesGetDialogUnreadMarks(rpcCtx, &tg.MessagesGetDialogUnreadMarksRequest{})
	if err != nil {
		t.Fatalf("get channel unread marks: %v", err)
	}
	if len(marks) != 1 {
		t.Fatalf("channel unread marks len = %d, want 1 (%+v)", len(marks), marks)
	}
	got, ok := marks[0].(*tg.DialogPeer)
	if !ok {
		t.Fatalf("channel unread mark = %T, want dialogPeer", marks[0])
	}
	peer, ok := got.Peer.(*tg.PeerChannel)
	if !ok || peer.ChannelID != created.Channel.ID {
		t.Fatalf("channel unread mark peer = %#v, want channel %d", got.Peer, created.Channel.ID)
	}
	dialogsList, err := dialogs.GetPeerDialogs(ctx, 1000000001, []domain.Peer{{Type: domain.PeerTypeChannel, ID: created.Channel.ID}})
	if err != nil {
		t.Fatalf("GetPeerDialogs: %v", err)
	}
	var dialog domain.Dialog
	for _, item := range dialogsList.Dialogs {
		if item.Peer.Type == domain.PeerTypeChannel && item.Peer.ID == created.Channel.ID {
			dialog = item
			break
		}
	}
	if dialog.Peer.ID == 0 {
		t.Fatalf("channel dialog missing from %+v", dialogsList.Dialogs)
	}
	if !dialog.UnreadMark {
		t.Fatalf("channel dialog unread mark = false, want true")
	}

	clear := &tg.MessagesMarkDialogUnreadRequest{Peer: input}
	if ok, err := r.onMessagesMarkDialogUnread(rpcCtx, clear); err != nil || !ok {
		t.Fatalf("clear channel unread = %v, %v", ok, err)
	}
	marks, err = r.onMessagesGetDialogUnreadMarks(rpcCtx, &tg.MessagesGetDialogUnreadMarksRequest{})
	if err != nil {
		t.Fatalf("get channel unread marks after clear: %v", err)
	}
	if len(marks) != 0 {
		t.Fatalf("channel unread marks after clear = %+v, want empty", marks)
	}
}

func TestDialogFolderRPCsPersistAndRecordUpdates(t *testing.T) {
	var authKeyID [8]byte
	authKeyID[0] = 12
	ctx := WithSessionID(WithAuthKeyID(WithUserID(context.Background(), 1000000001), authKeyID), 66)
	dialogs := &captureDialogs{}
	updates := &captureUpdates{state: domain.UpdateState{Pts: 21, Date: 1700000500, Seq: 0}}
	r := New(Config{}, Deps{Dialogs: dialogs, Updates: updates}, zaptest.NewLogger(t), clock.System)

	req := &tg.MessagesUpdateDialogFilterRequest{ID: 2}
	req.SetFilter(&tg.DialogFilter{
		ID:       2,
		Contacts: true,
		Title:    tg.TextWithEntities{Text: "Work"},
		IncludePeers: []tg.InputPeerClass{&tg.InputPeerUser{
			UserID:     1000000002,
			AccessHash: 222,
		}},
	})
	if ok, err := r.onMessagesUpdateDialogFilter(ctx, req); err != nil || !ok {
		t.Fatalf("update filter = %v, %v", ok, err)
	}
	if dialogs.savedFolder.ID != 2 || dialogs.savedFolder.Title != "Work" || !dialogs.savedFolder.Contacts || len(dialogs.savedFolder.IncludePeers) != 1 {
		t.Fatalf("saved folder = %+v, want parsed dialog folder", dialogs.savedFolder)
	}
	if len(updates.events) != 1 || updates.events[0].Type != domain.UpdateEventDialogFilter || updates.events[0].FilterID != 2 || updates.excludeSessionID != 66 {
		t.Fatalf("filter event = %+v, want durable dialog_filter", updates.events)
	}

	if ok, err := r.onMessagesUpdateDialogFiltersOrder(ctx, []int{2, 2, 0, 3}); err != nil || !ok {
		t.Fatalf("update filter order = %v, %v", ok, err)
	}
	if len(dialogs.folderOrder) != 2 || dialogs.folderOrder[0] != 2 || dialogs.folderOrder[1] != 3 {
		t.Fatalf("folder order = %+v, want cleaned custom IDs", dialogs.folderOrder)
	}
	if len(updates.events) != 2 || updates.events[1].Type != domain.UpdateEventDialogFilterOrder {
		t.Fatalf("order event = %+v, want durable dialog_filter_order", updates.events)
	}

	if ok, err := r.onMessagesToggleDialogFilterTags(ctx, true); err != nil || !ok {
		t.Fatalf("toggle filter tags = %v, %v", ok, err)
	}
	if !dialogs.tagsEnabled || len(updates.events) != 3 || updates.events[2].Type != domain.UpdateEventDialogFilters {
		t.Fatalf("tags/update = tags %v events %+v, want reload update", dialogs.tagsEnabled, updates.events)
	}
}

func TestFoldersEditPeerFoldersPersistsArchiveAndReturnsPtsUpdate(t *testing.T) {
	var authKeyID [8]byte
	authKeyID[0] = 13
	ctx := WithSessionID(WithAuthKeyID(WithUserID(context.Background(), 1000000001), authKeyID), 77)
	dialogs := &captureDialogs{}
	updates := &captureUpdates{state: domain.UpdateState{Pts: 31, Date: 1700000600, Seq: 0}}
	r := New(Config{}, Deps{Dialogs: dialogs, Updates: updates}, zaptest.NewLogger(t), clock.System)

	got, err := r.onFoldersEditPeerFolders(ctx, []tg.InputFolderPeer{{
		Peer:     &tg.InputPeerUser{UserID: 1000000002},
		FolderID: domain.DialogArchiveFolderID,
	}})
	if err != nil {
		t.Fatalf("folders.editPeerFolders: %v", err)
	}
	if len(dialogs.folderPeers) != 1 || dialogs.folderPeers[0].FolderID != domain.DialogArchiveFolderID {
		t.Fatalf("folder peers = %+v, want archive update", dialogs.folderPeers)
	}
	out, ok := got.(*tg.Updates)
	if !ok || len(out.Updates) != 1 {
		t.Fatalf("updates = %T %+v, want tg.Updates with updateFolderPeers", got, got)
	}
	update, ok := out.Updates[0].(*tg.UpdateFolderPeers)
	if !ok || update.Pts != 31 || update.PtsCount != 1 || len(update.FolderPeers) != 1 {
		t.Fatalf("update = %T %+v, want updateFolderPeers pts=31", out.Updates[0], out.Updates[0])
	}
}

func TestDialogSettingRPCsSkipManualPushWhenReliableDispatch(t *testing.T) {
	var authKeyID [8]byte
	authKeyID[0] = 11
	ctx := WithSessionID(WithAuthKeyID(WithUserID(context.Background(), 1000000001), authKeyID), 55)
	peer := domain.Peer{Type: domain.PeerTypeUser, ID: 1000000002}
	dialogs := &captureDialogs{}
	updates := &captureUpdates{state: domain.UpdateState{Pts: 11, Date: 1700000400, Seq: 0}, reliableDispatch: true}
	sessions := &captureSessions{}
	r := New(Config{}, Deps{Dialogs: dialogs, Updates: updates, Sessions: sessions}, zaptest.NewLogger(t), clock.System)

	toggle := &tg.MessagesToggleDialogPinRequest{Peer: &tg.InputDialogPeer{Peer: &tg.InputPeerUser{UserID: peer.ID}}}
	toggle.SetPinned(true)
	if ok, err := r.onMessagesToggleDialogPin(ctx, toggle); err != nil || !ok {
		t.Fatalf("toggle pin = %v, %v", ok, err)
	}
	if len(updates.events) != 1 || updates.events[0].Type != domain.UpdateEventDialogPinned {
		t.Fatalf("events = %+v, want durable event recorded", updates.events)
	}
	if got := sessions.snapshot(); got.message != nil {
		t.Fatalf("manual push = %+v, want reliable outbox to be sole online dispatcher", got)
	}
}

func TestUpdatesGetStateMarksSessionReadyForPush(t *testing.T) {
	sessions := &captureSessions{}
	r := New(Config{}, Deps{
		Sessions: sessions,
		Updates:  &captureUpdates{state: domain.UpdateState{Pts: 3, Date: 1700000000, Seq: 2}},
	}, zaptest.NewLogger(t), clock.System)

	got, err := r.onUpdatesGetState(WithSessionID(context.Background(), 77))
	if err != nil {
		t.Fatalf("updates.getState: %v", err)
	}
	if got.Pts != 3 || got.Seq != 2 {
		t.Fatalf("state = %+v, want pts=3 seq=2", got)
	}
	gotSession := sessions.snapshot()
	if gotSession.sessionID != 77 || !gotSession.receives {
		t.Fatalf("session ready = id %d receives %v, want 77/true", gotSession.sessionID, gotSession.receives)
	}
}

func TestUpdatesGetStateNudgesDifferenceWhenCurrentStateAhead(t *testing.T) {
	sessions := &captureSessions{}
	current := domain.UpdateState{Pts: 4, Date: 1700000001}
	r := New(Config{}, Deps{
		Sessions: sessions,
		Updates: &captureUpdates{
			state:        domain.UpdateState{Pts: 3, Date: 1700000000},
			currentState: &current,
		},
	}, zaptest.NewLogger(t), clock.System)

	if _, err := r.onUpdatesGetState(WithSessionID(context.Background(), 77)); err != nil {
		t.Fatalf("updates.getState: %v", err)
	}
	deadline := time.Now().Add(time.Second)
	for {
		got := sessions.snapshot()
		if _, ok := got.message.(*tg.UpdatesTooLong); ok {
			if got.sessionID != 77 || got.messageType != proto.MessageFromServer {
				t.Fatalf("nudge target = session %d type %v, want 77/from-server", got.sessionID, got.messageType)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for updatesTooLong nudge, last message=%T", got.message)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestUpdatesDifferenceIncludesLoginMessageAndOfficialUser(t *testing.T) {
	msg := domain.Message{
		ID:          88,
		OwnerUserID: 1000000001,
		Peer:        domain.Peer{Type: domain.PeerTypeUser, ID: domain.OfficialSystemUserID},
		From:        domain.Peer{Type: domain.PeerTypeUser, ID: domain.OfficialSystemUserID},
		Date:        1700000100,
		Body:        "Login code: 12345",
	}
	got, ok := tgUpdatesDifference(domain.UpdateDifference{
		State: domain.UpdateState{Pts: 5, Date: msg.Date, Seq: 4},
		Events: []domain.UpdateEvent{{
			Type:     domain.UpdateEventNewMessage,
			Pts:      5,
			PtsCount: 1,
			Date:     msg.Date,
			Message:  msg,
		}},
	}).(*tg.UpdatesDifference)
	if !ok {
		t.Fatalf("difference = %T, want *tg.UpdatesDifference", got)
	}
	if got.State.Pts != 5 || len(got.NewMessages) != 1 || len(got.Users) != 1 {
		t.Fatalf("difference = %+v, want one message, one official user, pts=5", got)
	}
	user, ok := got.Users[0].(*tg.User)
	if !ok || user.ID != domain.OfficialSystemUserID {
		t.Fatalf("user = %#v, want official system user", got.Users[0])
	}
}

func TestUpdatesDifferenceIncludesForwardSourceChannelChat(t *testing.T) {
	source := domain.Channel{
		ID:         2000000001,
		AccessHash: 9001,
		Title:      "Source Channel",
		Broadcast:  true,
		Date:       1700000000,
	}
	msg := domain.Message{
		ID:          89,
		OwnerUserID: 1000000001,
		Peer:        domain.Peer{Type: domain.PeerTypeUser, ID: 1000000002},
		From:        domain.Peer{Type: domain.PeerTypeUser, ID: 1000000001},
		Date:        1700000101,
		Body:        "forwarded",
		Forward:     &domain.MessageForward{From: domain.Peer{Type: domain.PeerTypeChannel, ID: source.ID}, Date: 1700000000},
	}
	got, ok := tgUpdatesDifference(domain.UpdateDifference{
		State: domain.UpdateState{Pts: 6, Date: msg.Date},
		Events: []domain.UpdateEvent{{
			UserID:   msg.OwnerUserID,
			Type:     domain.UpdateEventNewMessage,
			Pts:      6,
			PtsCount: 1,
			Date:     msg.Date,
			Message:  msg,
			Channels: []domain.Channel{source},
		}},
	}).(*tg.UpdatesDifference)
	if !ok {
		t.Fatalf("difference = %T, want *tg.UpdatesDifference", got)
	}
	if len(got.Chats) != 1 {
		t.Fatalf("chats = %+v, want source channel", got.Chats)
	}
	ch, ok := got.Chats[0].(*tg.Channel)
	if !ok || ch.ID != source.ID || ch.Title != source.Title {
		t.Fatalf("chat = %#v, want source channel", got.Chats[0])
	}
}

func TestOutboxEventIncludesForwardSourceChannelChat(t *testing.T) {
	source := domain.Channel{
		ID:         2000000002,
		AccessHash: 9002,
		Title:      "Forward Source",
		Broadcast:  true,
		Date:       1700000000,
	}
	update := tgUpdateForOutboxEvent(domain.UpdateEvent{
		UserID:   1000000001,
		Type:     domain.UpdateEventNewMessage,
		Pts:      7,
		PtsCount: 1,
		Date:     1700000102,
		Message: domain.Message{
			ID:          90,
			OwnerUserID: 1000000001,
			Peer:        domain.Peer{Type: domain.PeerTypeUser, ID: 1000000002},
			From:        domain.Peer{Type: domain.PeerTypeUser, ID: 1000000002},
			Date:        1700000102,
			Body:        "forwarded",
			Forward:     &domain.MessageForward{From: domain.Peer{Type: domain.PeerTypeChannel, ID: source.ID}, Date: 1700000000},
		},
		Channels: []domain.Channel{source},
	})
	if update == nil || len(update.Chats) != 1 {
		t.Fatalf("update = %+v, want source channel chat", update)
	}
	ch, ok := update.Chats[0].(*tg.Channel)
	if !ok || ch.ID != source.ID {
		t.Fatalf("chat = %#v, want source channel", update.Chats[0])
	}
}

func TestOutboxEventChannelViewForumAsMessages(t *testing.T) {
	update := tgUpdateForOutboxEvent(domain.UpdateEvent{
		UserID: 1000000001,
		Type:   domain.UpdateEventChannelViewForum,
		Peer:   domain.Peer{Type: domain.PeerTypeChannel, ID: 2000000002},
		Bool:   true,
		Date:   1700000200,
	})
	if update == nil || len(update.Updates) != 1 {
		t.Fatalf("update = %+v, want one updateChannelViewForumAsMessages", update)
	}
	got, ok := update.Updates[0].(*tg.UpdateChannelViewForumAsMessages)
	if !ok || got.ChannelID != 2000000002 || !got.Enabled {
		t.Fatalf("update = %#v, want channel view-forum-as-messages enabled", update.Updates[0])
	}
}

func TestChannelDifferenceIncludesExtraForwardSourceChannel(t *testing.T) {
	channel := domain.Channel{ID: 2000000100, AccessHash: 9010, Title: "Megagroup", Megagroup: true, Date: 1700000000, Pts: 3}
	source := domain.Channel{ID: 2000000101, AccessHash: 9011, Title: "Source", Broadcast: true, Date: 1700000000}
	got, ok := tgChannelDifference(1000000001, domain.ChannelDifference{
		Channel: channel,
		NewMessages: []domain.ChannelMessage{{
			ChannelID:    channel.ID,
			ID:           3,
			SenderUserID: 1000000002,
			From:         domain.Peer{Type: domain.PeerTypeUser, ID: 1000000002},
			Date:         1700000103,
			Body:         "forwarded",
			Forward:      &domain.MessageForward{From: domain.Peer{Type: domain.PeerTypeChannel, ID: source.ID}, Date: 1700000000},
			Pts:          3,
		}},
		Users:    []domain.User{{ID: 1000000002, AccessHash: 42, FirstName: "Bob"}},
		Channels: []domain.Channel{source},
		Pts:      3,
		Final:    true,
		Timeout:  30,
	}).(*tg.UpdatesChannelDifference)
	if !ok {
		t.Fatalf("difference = %T, want *tg.UpdatesChannelDifference", got)
	}
	if len(got.Users) != 1 || len(got.Chats) != 2 {
		t.Fatalf("difference users/chats = %d/%d, want 1/2", len(got.Users), len(got.Chats))
	}
	if ch, ok := got.Chats[1].(*tg.Channel); !ok || ch.ID != source.ID {
		t.Fatalf("extra chat = %#v, want source channel", got.Chats[1])
	}
}

func TestUpdatesDifferenceIncludesReadHistoryInbox(t *testing.T) {
	peer := domain.Peer{Type: domain.PeerTypeUser, ID: domain.OfficialSystemUserID}
	got, ok := tgUpdatesDifference(domain.UpdateDifference{
		State: domain.UpdateState{Pts: 6, Date: 1700000200, Seq: 5},
		Events: []domain.UpdateEvent{{
			Type:             domain.UpdateEventReadHistoryInbox,
			Pts:              6,
			PtsCount:         1,
			Date:             1700000200,
			Peer:             peer,
			MaxID:            12,
			StillUnreadCount: 0,
		}},
	}).(*tg.UpdatesDifference)
	if !ok {
		t.Fatalf("difference = %T, want *tg.UpdatesDifference", got)
	}
	if got.State.Pts != 6 || len(got.OtherUpdates) != 1 {
		t.Fatalf("difference = %+v, want one read history update and pts=6", got)
	}
	update, ok := got.OtherUpdates[0].(*tg.UpdateReadHistoryInbox)
	if !ok {
		t.Fatalf("update = %T, want *tg.UpdateReadHistoryInbox", got.OtherUpdates[0])
	}
	if update.MaxID != 12 || update.Pts != 6 || update.PtsCount != 1 {
		t.Fatalf("read update = %+v, want max_id=12 pts=6 pts_count=1", update)
	}
}

func TestUpdatesDifferenceIncludesReadHistoryOutbox(t *testing.T) {
	peer := domain.Peer{Type: domain.PeerTypeUser, ID: 1000000002}
	got, ok := tgUpdatesDifference(domain.UpdateDifference{
		State: domain.UpdateState{Pts: 7, Date: 1700000210, Seq: 0},
		Events: []domain.UpdateEvent{{
			Type:     domain.UpdateEventReadHistoryOutbox,
			Pts:      7,
			PtsCount: 1,
			Date:     1700000210,
			Peer:     peer,
			MaxID:    9,
		}},
	}).(*tg.UpdatesDifference)
	if !ok {
		t.Fatalf("difference = %T, want *tg.UpdatesDifference", got)
	}
	if len(got.OtherUpdates) != 1 {
		t.Fatalf("other updates = %+v, want one read outbox update", got.OtherUpdates)
	}
	update, ok := got.OtherUpdates[0].(*tg.UpdateReadHistoryOutbox)
	if !ok || update.MaxID != 9 || update.Pts != 7 || update.PtsCount != 1 {
		t.Fatalf("read outbox = %T %+v, want max_id=9 pts=7", got.OtherUpdates[0], got.OtherUpdates[0])
	}
}

func TestUpdatesDifferenceIncludesEditMessage(t *testing.T) {
	msg := domain.Message{
		ID:          4,
		OwnerUserID: 1000000001,
		Peer:        domain.Peer{Type: domain.PeerTypeUser, ID: 1000000002},
		From:        domain.Peer{Type: domain.PeerTypeUser, ID: 1000000001},
		Date:        1700000200,
		EditDate:    1700000300,
		Out:         true,
		Body:        "edited",
		Pts:         8,
	}
	got, ok := tgUpdatesDifference(domain.UpdateDifference{
		State: domain.UpdateState{Pts: 8, Date: 1700000300, Seq: 0},
		Events: []domain.UpdateEvent{{
			Type:     domain.UpdateEventEditMessage,
			Pts:      8,
			PtsCount: 1,
			Date:     1700000300,
			Message:  msg,
		}},
	}).(*tg.UpdatesDifference)
	if !ok {
		t.Fatalf("difference = %T, want *tg.UpdatesDifference", got)
	}
	if len(got.OtherUpdates) != 1 {
		t.Fatalf("other updates = %+v, want one edit update", got.OtherUpdates)
	}
	update, ok := got.OtherUpdates[0].(*tg.UpdateEditMessage)
	if !ok || update.Pts != 8 || update.PtsCount != 1 {
		t.Fatalf("edit update = %T %+v, want pts=8", got.OtherUpdates[0], got.OtherUpdates[0])
	}
	edited, ok := update.Message.(*tg.Message)
	if !ok || edited.Message != "edited" || edited.EditDate != 1700000300 {
		t.Fatalf("edited message = %#v, want text and edit_date", update.Message)
	}
}

func TestUpdatesDifferenceIncludesReactionMessageAndUpdate(t *testing.T) {
	const (
		aliceID = int64(1000000001)
		bobID   = int64(1000000002)
	)
	reaction := domain.MessageReaction{Type: domain.MessageReactionEmoji, Emoticon: "\U0001f44d"}
	reactions := domain.ChannelMessageReactions{
		CanSeeList: true,
		Results: []domain.ChannelMessageReactionCount{{
			Reaction:    reaction,
			Count:       1,
			ChosenOrder: 1,
		}},
		Recent: []domain.ChannelMessagePeerReaction{{
			UserID:      bobID,
			Reaction:    reaction,
			My:          true,
			ChosenOrder: 1,
			Date:        1700000310,
		}},
	}
	msg := domain.Message{
		ID:          68,
		UID:         7001,
		OwnerUserID: aliceID,
		Peer:        domain.Peer{Type: domain.PeerTypeUser, ID: bobID},
		From:        domain.Peer{Type: domain.PeerTypeUser, ID: aliceID},
		Date:        1700000300,
		Body:        "rx",
		Reactions:   &reactions,
	}
	got, ok := tgUpdatesDifference(domain.UpdateDifference{
		State: domain.UpdateState{Pts: 9, Date: 1700000310},
		Events: []domain.UpdateEvent{{
			UserID:   aliceID,
			Type:     domain.UpdateEventMessageReactions,
			Pts:      9,
			PtsCount: 1,
			Date:     1700000310,
			Peer:     domain.Peer{Type: domain.PeerTypeUser, ID: bobID},
			Message:  msg,
		}},
	}).(*tg.UpdatesDifference)
	if !ok {
		t.Fatalf("difference = %T, want *tg.UpdatesDifference", got)
	}
	if len(got.NewMessages) != 1 || len(got.OtherUpdates) != 1 {
		t.Fatalf("difference messages/updates = %d/%d, want 1/1", len(got.NewMessages), len(got.OtherUpdates))
	}
	wireMsg, ok := got.NewMessages[0].(*tg.Message)
	if !ok || wireMsg.ID != msg.ID {
		t.Fatalf("message = %T %+v, want message %d", got.NewMessages[0], got.NewMessages[0], msg.ID)
	}
	msgReactions, ok := wireMsg.GetReactions()
	if !ok || len(msgReactions.Results) != 1 || msgReactions.Results[0].Count != 1 || msgReactions.Results[0].ChosenOrder != 1 {
		t.Fatalf("message reactions = %+v set=%v, want chosen reaction", msgReactions, ok)
	}
	update, ok := got.OtherUpdates[0].(*tg.UpdateMessageReactions)
	if !ok || update.MsgID != msg.ID || len(update.Reactions.Results) != 1 || update.Reactions.Results[0].ChosenOrder != 1 {
		t.Fatalf("reaction update = %T %+v, want update for msg %d", got.OtherUpdates[0], got.OtherUpdates[0], msg.ID)
	}
}

func TestUpdatesDifferenceIncludesDeleteMessages(t *testing.T) {
	got, ok := tgUpdatesDifference(domain.UpdateDifference{
		State: domain.UpdateState{Pts: 8, Date: 1700000250, Seq: 0},
		Events: []domain.UpdateEvent{{
			Type:       domain.UpdateEventDeleteMessages,
			Pts:        8,
			PtsCount:   3,
			Date:       1700000250,
			MessageIDs: []int{3, 4, 5},
		}},
	}).(*tg.UpdatesDifference)
	if !ok {
		t.Fatalf("difference = %T, want *tg.UpdatesDifference", got)
	}
	if got.State.Pts != 8 || len(got.OtherUpdates) != 1 {
		t.Fatalf("difference = %+v, want one delete update and pts=8", got)
	}
	update, ok := got.OtherUpdates[0].(*tg.UpdateDeleteMessages)
	if !ok {
		t.Fatalf("update = %T, want *tg.UpdateDeleteMessages", got.OtherUpdates[0])
	}
	if update.Pts != 8 || update.PtsCount != 3 || len(update.Messages) != 3 || update.Messages[0] != 3 || update.Messages[2] != 5 {
		t.Fatalf("delete update = %+v, want ids [3 4 5] pts=8 pts_count=3", update)
	}
}

func TestUpdatesDifferenceIncludesChannelTooLongNudge(t *testing.T) {
	got, ok := tgUpdatesDifference(domain.UpdateDifference{
		State: domain.UpdateState{Pts: 8, Date: 1700000250, Seq: 0},
		ChannelNudges: []domain.ChannelDifferenceNudge{{
			ChannelID: 2000000001,
			Pts:       12,
		}},
	}).(*tg.UpdatesDifference)
	if !ok {
		t.Fatalf("difference = %T, want *tg.UpdatesDifference", got)
	}
	if got.State.Pts != 8 || len(got.OtherUpdates) != 1 {
		t.Fatalf("difference = %+v, want one channel nudge and account pts unchanged", got)
	}
	update, ok := got.OtherUpdates[0].(*tg.UpdateChannelTooLong)
	if !ok || update.ChannelID != 2000000001 {
		t.Fatalf("update = %T %+v, want UpdateChannelTooLong", got.OtherUpdates[0], got.OtherUpdates[0])
	}
	if pts, ok := update.GetPts(); !ok || pts != 12 {
		t.Fatalf("channel nudge pts = %d set=%v, want 12", pts, ok)
	}
}

func TestUpdatesDifferenceIncludesSettingsUpdates(t *testing.T) {
	peer := domain.Peer{Type: domain.PeerTypeUser, ID: 1000000002}
	got, ok := tgUpdatesDifference(domain.UpdateDifference{
		State: domain.UpdateState{Pts: 5, Date: 1700000300, Seq: 0},
		Events: []domain.UpdateEvent{
			{Type: domain.UpdateEventContactsReset, Pts: 1, PtsCount: 1, Date: 1700000300},
			{Type: domain.UpdateEventDialogPinned, Pts: 2, PtsCount: 1, Date: 1700000300, Peer: peer, Bool: true},
			{Type: domain.UpdateEventPinnedDialogs, Pts: 3, PtsCount: 1, Date: 1700000300, Peers: []domain.Peer{peer}},
			{Type: domain.UpdateEventDialogUnreadMark, Pts: 4, PtsCount: 1, Date: 1700000300, Peer: peer, Bool: false},
			{Type: domain.UpdateEventPeerSettings, Pts: 5, PtsCount: 1, Date: 1700000300, Peer: peer, Settings: domain.PeerSettings{ShareContact: true}},
		},
	}).(*tg.UpdatesDifference)
	if !ok {
		t.Fatalf("difference = %T, want *tg.UpdatesDifference", got)
	}
	if got.State.Pts != 5 || len(got.OtherUpdates) != 5 {
		t.Fatalf("difference = %+v, want five settings updates and pts=5", got)
	}
	if _, ok := got.OtherUpdates[0].(*tg.UpdateContactsReset); !ok {
		t.Fatalf("update[0] = %T, want contacts reset", got.OtherUpdates[0])
	}
	pinned, ok := got.OtherUpdates[1].(*tg.UpdateDialogPinned)
	if !ok || !pinned.Pinned {
		t.Fatalf("update[1] = %+v (%T), want pinned dialog", got.OtherUpdates[1], got.OtherUpdates[1])
	}
	pinnedDialogs, ok := got.OtherUpdates[2].(*tg.UpdatePinnedDialogs)
	if !ok || len(pinnedDialogs.Order) != 1 {
		t.Fatalf("update[2] = %T, want pinned dialogs", got.OtherUpdates[2])
	}
	unread, ok := got.OtherUpdates[3].(*tg.UpdateDialogUnreadMark)
	if !ok || unread.Unread {
		t.Fatalf("update[3] = %+v (%T), want unread=false mark", got.OtherUpdates[3], got.OtherUpdates[3])
	}
	peerSettings, ok := got.OtherUpdates[4].(*tg.UpdatePeerSettings)
	if !ok || !peerSettings.Settings.ShareContact {
		t.Fatalf("update[4] = %T, want peer settings", got.OtherUpdates[4])
	}
}

func TestMessagesGetHistoryReturnsStoredMessages(t *testing.T) {
	msg := domain.Message{
		ID:          1,
		OwnerUserID: 1000000001,
		Peer:        domain.Peer{Type: domain.PeerTypeUser, ID: domain.OfficialSystemUserID},
		From:        domain.Peer{Type: domain.PeerTypeUser, ID: domain.OfficialSystemUserID},
		Date:        1700000100,
		Body:        "Login code: 12345",
	}
	messages := &captureMessages{
		list: domain.MessageList{
			Messages: []domain.Message{msg},
			Users:    []domain.User{domain.OfficialSystemUser()},
			Count:    1,
			Hash:     99,
		},
	}
	r := New(Config{}, Deps{Messages: messages}, zaptest.NewLogger(t), clock.System)
	req := &tg.MessagesGetHistoryRequest{
		Peer:      &tg.InputPeerUser{UserID: domain.OfficialSystemUserID, AccessHash: domain.OfficialSystemUser().AccessHash},
		Limit:     20,
		AddOffset: 1 << 30,
	}
	var in bin.Buffer
	if err := req.Encode(&in); err != nil {
		t.Fatalf("encode request: %v", err)
	}

	enc, err := r.Dispatch(WithUserID(context.Background(), 1000000001), [8]byte{}, 0, &in)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	box, ok := enc.(*tg.MessagesMessagesBox)
	if !ok {
		t.Fatalf("response = %T, want *tg.MessagesMessagesBox", enc)
	}
	got, ok := box.Messages.(*tg.MessagesMessages)
	if !ok {
		t.Fatalf("boxed response = %T, want *tg.MessagesMessages", box.Messages)
	}
	if len(got.Messages) != 1 || len(got.Users) != 1 {
		t.Fatalf("history = %+v, want one message and one user", got)
	}
	if messages.filter.Peer.ID != domain.OfficialSystemUserID || messages.filter.Limit != 20 || messages.filter.AddOffset != domain.MaxMessageHistoryAddOffset {
		t.Fatalf("filter = %+v, want official peer limit 20 and clamped add_offset", messages.filter)
	}
}

func TestMessagesSetTypingPushesUserTypingUpdate(t *testing.T) {
	sessions := &captureScopedSessions{captureSessions: &captureSessions{}}
	r := New(Config{}, Deps{Sessions: sessions}, zaptest.NewLogger(t), clock.System)
	var authKeyID [8]byte
	authKeyID[0] = 7

	ctx := WithSessionID(WithAuthKeyID(WithUserID(context.Background(), 1000000001), authKeyID), 55)
	ok, err := r.onMessagesSetTyping(ctx, &tg.MessagesSetTypingRequest{
		Peer:   &tg.InputPeerUser{UserID: 1000000002, AccessHash: 22},
		Action: &tg.SendMessageTypingAction{},
	})
	if err != nil {
		t.Fatalf("set typing: %v", err)
	}
	if !ok {
		t.Fatalf("set typing = false, want true")
	}

	got := sessions.snapshot()
	if got.userID != 1000000002 || got.sessionID != 55 || got.messageType != proto.MessageFromServer {
		t.Fatalf("push = user %d exclude session %d type %v, want target/exclude/from_server", got.userID, got.sessionID, got.messageType)
	}
	if gotAuthKeyID := sessions.scopedAuthKeyID; gotAuthKeyID != authKeyID {
		t.Fatalf("exclude auth_key_id = %x, want %x", gotAuthKeyID, authKeyID)
	}
	updateShort, ok := got.message.(*tg.UpdateShort)
	if !ok {
		t.Fatalf("pushed message = %T, want *tg.UpdateShort", got.message)
	}
	typing, ok := updateShort.Update.(*tg.UpdateUserTyping)
	if !ok {
		t.Fatalf("short update = %T, want *tg.UpdateUserTyping", updateShort.Update)
	}
	if typing.UserID != 1000000001 {
		t.Fatalf("typing user_id = %d, want sender", typing.UserID)
	}
	if _, ok := typing.Action.(*tg.SendMessageTypingAction); !ok {
		t.Fatalf("typing action = %T, want *tg.SendMessageTypingAction", typing.Action)
	}
}

func TestMessagesSetTypingRejectsInvalidTopMsgID(t *testing.T) {
	r := New(Config{}, Deps{}, zaptest.NewLogger(t), clock.System)
	req := &tg.MessagesSetTypingRequest{
		Peer:   &tg.InputPeerUser{UserID: 1000000002, AccessHash: 22},
		Action: &tg.SendMessageTypingAction{},
	}
	req.SetTopMsgID(domain.MaxMessageBoxID + 1)

	ok, err := r.onMessagesSetTyping(WithUserID(context.Background(), 1000000001), req)
	if ok || err == nil || !strings.Contains(err.Error(), "MSG_ID_INVALID") {
		t.Fatalf("set typing invalid top_msg_id = ok %v err %v, want MSG_ID_INVALID", ok, err)
	}
}

func TestMessagesSetTypingPushesChannelTypingTopMsgID(t *testing.T) {
	const (
		ownerID  = int64(1000000001)
		memberID = int64(1000000002)
		topicID  = 7
	)
	channels := appchannels.NewService(memory.NewChannelStore())
	created, err := channels.CreateChannel(context.Background(), ownerID, domain.CreateChannelRequest{
		Title:         "topic group",
		CreatorUserID: ownerID,
		Megagroup:     true,
		MemberUserIDs: []int64{memberID},
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	sessions := &captureScopedSessions{captureSessions: &captureSessions{
		channelViewers: map[int64][]int64{created.Channel.ID: {memberID}},
	}}
	r := New(Config{}, Deps{Channels: channels, Sessions: sessions}, zaptest.NewLogger(t), clock.System)
	var authKeyID [8]byte
	authKeyID[0] = 9

	req := &tg.MessagesSetTypingRequest{
		Peer: &tg.InputPeerChannel{
			ChannelID:  created.Channel.ID,
			AccessHash: created.Channel.AccessHash,
		},
		Action: &tg.SendMessageTypingAction{},
	}
	req.SetTopMsgID(topicID)
	ctx := WithSessionID(WithAuthKeyID(WithUserID(context.Background(), ownerID), authKeyID), 77)

	ok, err := r.onMessagesSetTyping(ctx, req)
	if err != nil {
		t.Fatalf("set channel typing: %v", err)
	}
	if !ok {
		t.Fatalf("set channel typing = false, want true")
	}
	got := sessions.snapshot()
	if got.userID != memberID || got.sessionID != 77 || got.messageType != proto.MessageFromServer {
		t.Fatalf("channel typing push = user %d exclude session %d type %v, want member/exclude/from_server", got.userID, got.sessionID, got.messageType)
	}
	if gotAuthKeyID := sessions.scopedAuthKeyID; gotAuthKeyID != authKeyID {
		t.Fatalf("exclude auth_key_id = %x, want %x", gotAuthKeyID, authKeyID)
	}
	updates, ok := got.message.(*tg.Updates)
	if !ok {
		t.Fatalf("pushed message = %T, want *tg.Updates", got.message)
	}
	if len(updates.Updates) != 1 {
		t.Fatalf("updates len = %d, want 1", len(updates.Updates))
	}
	typing, ok := updates.Updates[0].(*tg.UpdateChannelUserTyping)
	if !ok {
		t.Fatalf("channel update = %T, want *tg.UpdateChannelUserTyping", updates.Updates[0])
	}
	if typing.ChannelID != created.Channel.ID || typing.TopMsgID != topicID {
		t.Fatalf("channel typing = channel %d top %d, want channel %d top %d", typing.ChannelID, typing.TopMsgID, created.Channel.ID, topicID)
	}
	from, ok := typing.FromID.(*tg.PeerUser)
	if !ok || from.UserID != ownerID {
		t.Fatalf("typing from = %T %+v, want owner peer", typing.FromID, typing.FromID)
	}
	if _, ok := typing.Action.(*tg.SendMessageTypingAction); !ok {
		t.Fatalf("typing action = %T, want *tg.SendMessageTypingAction", typing.Action)
	}
}

func TestMessagesSetTypingSkipsChannelMemberWithoutViewerInterest(t *testing.T) {
	const (
		ownerID  = int64(1000000001)
		memberID = int64(1000000002)
	)
	channels := appchannels.NewService(memory.NewChannelStore())
	created, err := channels.CreateChannel(context.Background(), ownerID, domain.CreateChannelRequest{
		Title:         "quiet group",
		CreatorUserID: ownerID,
		Megagroup:     true,
		MemberUserIDs: []int64{memberID},
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	sessions := &captureScopedSessions{captureSessions: &captureSessions{
		channelMembers: map[int64][]int64{created.Channel.ID: {memberID}},
	}}
	r := New(Config{}, Deps{Channels: channels, Sessions: sessions}, zaptest.NewLogger(t), clock.System)

	ok, err := r.onMessagesSetTyping(WithUserID(context.Background(), ownerID), &tg.MessagesSetTypingRequest{
		Peer: &tg.InputPeerChannel{
			ChannelID:  created.Channel.ID,
			AccessHash: created.Channel.AccessHash,
		},
		Action: &tg.SendMessageTypingAction{},
	})
	if err != nil {
		t.Fatalf("set channel typing: %v", err)
	}
	if !ok {
		t.Fatalf("set channel typing = false, want true")
	}
	if got := sessions.snapshot(); got.message != nil {
		t.Fatalf("typing push without viewer interest = %+v, want none", got)
	}
}

func TestMessagesGetMessagesReturnsOwnerMessages(t *testing.T) {
	const (
		userID = int64(1000000001)
		peerID = int64(1000000002)
	)
	messages := &captureMessages{list: domain.MessageList{
		Messages: []domain.Message{{
			ID:          7,
			OwnerUserID: userID,
			Peer:        domain.Peer{Type: domain.PeerTypeUser, ID: peerID},
			From:        domain.Peer{Type: domain.PeerTypeUser, ID: peerID},
			Date:        1700000000,
			Body:        "reply source",
		}},
		Count: 1,
	}}
	r := New(Config{}, Deps{
		Messages: messages,
		Users: mapUsersService{users: map[int64]domain.User{
			userID: {ID: userID, FirstName: "Alice"},
			peerID: {ID: peerID, FirstName: "Bob"},
		}},
	}, zaptest.NewLogger(t), clock.System)

	got, err := r.onMessagesGetMessages(WithUserID(context.Background(), userID), []tg.InputMessageClass{&tg.InputMessageID{ID: 7}})
	if err != nil {
		t.Fatalf("get messages: %v", err)
	}
	box, ok := got.(*tg.MessagesMessages)
	if !ok || len(box.Messages) != 1 {
		t.Fatalf("response = %T %+v, want one messages.messages", got, got)
	}
	msg, ok := box.Messages[0].(*tg.Message)
	if !ok || msg.ID != 7 || msg.Message != "reply source" {
		t.Fatalf("message = %#v, want source message", box.Messages[0])
	}
	if len(box.Users) != 1 {
		t.Fatalf("users = %+v, want peer user", box.Users)
	}
}

func TestMessagesSaveDraftPushesDraftUpdateToOtherSessions(t *testing.T) {
	const (
		userID = int64(1000000001)
		peerID = int64(1000000002)
	)
	sessions := &captureScopedSessions{captureSessions: &captureSessions{}}
	r := New(Config{}, Deps{
		Sessions: sessions,
		Users: mapUsersService{users: map[int64]domain.User{
			userID: {ID: userID, FirstName: "Alice"},
			peerID: {ID: peerID, FirstName: "Bob"},
		}},
	}, zaptest.NewLogger(t), clock.System)
	var authKeyID [8]byte
	authKeyID[0] = 8

	ctx := WithSessionID(WithAuthKeyID(WithUserID(context.Background(), userID), authKeyID), 66)
	ok, err := r.onMessagesSaveDraft(ctx, &tg.MessagesSaveDraftRequest{
		Peer:    &tg.InputPeerUser{UserID: peerID, AccessHash: 22},
		Message: "1111",
		Entities: []tg.MessageEntityClass{
			&tg.MessageEntityBold{Offset: 0, Length: 4},
		},
	})
	if err != nil {
		t.Fatalf("save draft: %v", err)
	}
	if !ok {
		t.Fatalf("save draft = false, want true")
	}

	got := sessions.snapshot()
	if got.userID != userID || got.sessionID != 66 || got.messageType != proto.MessageFromServer {
		t.Fatalf("push = user %d exclude session %d type %v, want self/exclude/from_server", got.userID, got.sessionID, got.messageType)
	}
	if gotAuthKeyID := sessions.scopedAuthKeyID; gotAuthKeyID != authKeyID {
		t.Fatalf("exclude auth_key_id = %x, want %x", gotAuthKeyID, authKeyID)
	}
	updates, ok := got.message.(*tg.Updates)
	if !ok {
		t.Fatalf("pushed message = %T, want *tg.Updates", got.message)
	}
	if len(updates.Updates) != 1 {
		t.Fatalf("updates = %+v, want one update", updates.Updates)
	}
	update, ok := updates.Updates[0].(*tg.UpdateDraftMessage)
	if !ok {
		t.Fatalf("update = %T, want *tg.UpdateDraftMessage", updates.Updates[0])
	}
	peer, ok := update.Peer.(*tg.PeerUser)
	if !ok || peer.UserID != peerID {
		t.Fatalf("draft peer = %#v, want peer user %d", update.Peer, peerID)
	}
	draft, ok := update.Draft.(*tg.DraftMessage)
	if !ok || draft.Message != "1111" || len(draft.Entities) != 1 {
		t.Fatalf("draft = %#v, want message and entities", update.Draft)
	}
	if len(updates.Users) != 2 {
		t.Fatalf("users = %+v, want self and peer", updates.Users)
	}
}

func TestMessagesUpdateSavedReactionTagPersistsAndPushesRefresh(t *testing.T) {
	const userID = int64(1000000001)
	sessions := &captureSessions{}
	r := New(Config{}, Deps{
		Channels: appchannels.NewService(memory.NewChannelStore()),
		Sessions: sessions,
	}, zaptest.NewLogger(t), clock.System)

	req := &tg.MessagesUpdateSavedReactionTagRequest{
		Reaction: &tg.ReactionEmoji{Emoticon: "\U0001f44d"},
	}
	req.SetTitle("Fav")
	ok, err := r.onMessagesUpdateSavedReactionTag(WithSessionID(WithUserID(context.Background(), userID), 55), req)
	if err != nil || !ok {
		t.Fatalf("update saved reaction tag = %v, %v, want true nil", ok, err)
	}

	got, err := r.onMessagesGetSavedReactionTags(WithUserID(context.Background(), userID), &tg.MessagesGetSavedReactionTagsRequest{})
	if err != nil {
		t.Fatalf("get saved reaction tags: %v", err)
	}
	page, ok := got.(*tg.MessagesSavedReactionTags)
	if !ok || len(page.Tags) != 1 {
		t.Fatalf("saved reaction tags = %T %+v, want one tag", got, got)
	}
	if emoji, ok := page.Tags[0].Reaction.(*tg.ReactionEmoji); !ok || emoji.Emoticon != "\U0001f44d" || page.Tags[0].Title != "Fav" {
		t.Fatalf("saved reaction tag = %+v, want persisted thumb/Fav", page.Tags[0])
	}

	push := sessions.snapshot()
	if push.userID != userID || push.sessionID != 55 || push.messageType != proto.MessageFromServer {
		t.Fatalf("push = user %d exclude session %d type %v, want self/exclude/from_server", push.userID, push.sessionID, push.messageType)
	}
	updates, ok := push.message.(*tg.Updates)
	if !ok {
		t.Fatalf("pushed message = %T, want *tg.Updates", push.message)
	}
	if len(updates.Updates) != 1 {
		t.Fatalf("updates = %+v, want one update", updates.Updates)
	}
	if _, ok := updates.Updates[0].(*tg.UpdateSavedReactionTags); !ok {
		t.Fatalf("update = %T, want *tg.UpdateSavedReactionTags", updates.Updates[0])
	}
}

func TestMessagesSaveDraftPersistsAndGetAllDraftsReturnsForumDraft(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, err := userStore.Create(ctx, domain.User{AccessHash: 11, Phone: "15550001001", FirstName: "Owner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	channelStore := memory.NewChannelStore()
	channelSvc := appchannels.NewService(channelStore)
	created, err := channelSvc.CreateMegagroupFromCreateChat(ctx, owner.ID, domain.CreateChannelRequest{
		CreatorUserID: owner.ID,
		Title:         "Forum",
		Forum:         true,
		Date:          1700000000,
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	dialogStore := memory.NewDialogStore()
	r := New(Config{}, Deps{
		Users:    appusers.NewService(userStore),
		Channels: channelSvc,
		Dialogs:  appdialogs.NewService(dialogStore, channelStore),
	}, zaptest.NewLogger(t), clock.System)

	reply := &tg.InputReplyToMessage{}
	reply.SetTopMsgID(123)
	ok, err := r.onMessagesSaveDraft(WithUserID(ctx, owner.ID), &tg.MessagesSaveDraftRequest{
		Peer:    &tg.InputPeerChannel{ChannelID: created.Channel.ID, AccessHash: created.Channel.AccessHash},
		ReplyTo: reply,
		Message: "topic draft",
		Media: &tg.InputMediaWebPage{
			URL:      "https://example.test/draft",
			Optional: true,
		},
	})
	if err != nil || !ok {
		t.Fatalf("save forum draft = %v, %v", ok, err)
	}
	drafts, err := dialogStore.ListDrafts(ctx, owner.ID, 10)
	if err != nil {
		t.Fatalf("list stored drafts: %v", err)
	}
	if len(drafts) != 1 || drafts[0].TopMessageID != 123 || drafts[0].Message != "topic draft" || drafts[0].WebPage == nil {
		t.Fatalf("stored drafts = %+v, want forum draft with webpage", drafts)
	}

	got, err := r.onMessagesGetAllDrafts(WithUserID(ctx, owner.ID))
	if err != nil {
		t.Fatalf("get all drafts: %v", err)
	}
	updates, ok := got.(*tg.Updates)
	if !ok || len(updates.Updates) != 1 {
		t.Fatalf("get all drafts = %T %+v, want one updates", got, got)
	}
	update, ok := updates.Updates[0].(*tg.UpdateDraftMessage)
	if !ok {
		t.Fatalf("draft update = %T", updates.Updates[0])
	}
	if top, ok := update.GetTopMsgID(); !ok || top != 123 {
		t.Fatalf("top_msg_id = %d/%v, want 123/true", top, ok)
	}
	draft, ok := update.Draft.(*tg.DraftMessage)
	if !ok || draft.Message != "topic draft" {
		t.Fatalf("draft = %#v, want message draft", update.Draft)
	}
	if media, ok := draft.Media.(*tg.InputMediaWebPage); !ok || media.URL != "https://example.test/draft" {
		t.Fatalf("draft media = %#v, want webpage", draft.Media)
	}
	if len(updates.Chats) != 1 {
		t.Fatalf("chats = %+v, want channel context", updates.Chats)
	}
}

func TestMessagesGetDialogsIncludesCloudDraft(t *testing.T) {
	ctx := context.Background()
	const (
		userID = int64(1000000001)
		peerID = int64(1000000002)
	)
	dialogStore := memory.NewDialogStore()
	if err := dialogStore.Upsert(ctx, userID, domain.Dialog{
		Peer:           domain.Peer{Type: domain.PeerTypeUser, ID: peerID},
		TopMessage:     10,
		TopMessageDate: 1700000000,
	}); err != nil {
		t.Fatalf("upsert dialog: %v", err)
	}
	if err := dialogStore.SaveDraft(ctx, userID, domain.DialogDraft{
		Peer:    domain.Peer{Type: domain.PeerTypeUser, ID: peerID},
		Date:    1700000100,
		Message: "saved draft",
	}); err != nil {
		t.Fatalf("save draft: %v", err)
	}
	r := New(Config{}, Deps{
		Users: mapUsersService{users: map[int64]domain.User{
			userID: {ID: userID, FirstName: "Alice"},
			peerID: {ID: peerID, FirstName: "Bob"},
		}},
		Dialogs: appdialogs.NewService(dialogStore),
	}, zaptest.NewLogger(t), clock.System)
	req := &tg.MessagesGetDialogsRequest{
		OffsetPeer: &tg.InputPeerEmpty{},
		Limit:      10,
	}
	var in bin.Buffer
	if err := req.Encode(&in); err != nil {
		t.Fatalf("encode get dialogs: %v", err)
	}
	got, err := r.Dispatch(WithUserID(ctx, userID), [8]byte{}, 0, &in)
	if err != nil {
		t.Fatalf("get dialogs: %v", err)
	}
	box, ok := got.(*tg.MessagesDialogsBox)
	if !ok {
		t.Fatalf("response = %T, want boxed messages.dialogs", got)
	}
	dialogs, ok := box.Dialogs.(*tg.MessagesDialogs)
	if !ok || len(dialogs.Dialogs) != 1 {
		t.Fatalf("dialogs = %T %+v, want one messages.dialogs", box.Dialogs, box.Dialogs)
	}
	dialog, ok := dialogs.Dialogs[0].(*tg.Dialog)
	if !ok {
		t.Fatalf("dialog = %T", dialogs.Dialogs[0])
	}
	draft, ok := dialog.Draft.(*tg.DraftMessage)
	if !ok || draft.Message != "saved draft" {
		t.Fatalf("dialog draft = %#v, want saved draft", dialog.Draft)
	}
}

func TestMessagesClearAllDraftsClearsAndPushesEmptyUpdates(t *testing.T) {
	const (
		userID = int64(1000000001)
		peerID = int64(1000000002)
	)
	dialogs := &captureDialogs{drafts: []domain.DialogDraft{{
		Peer:    domain.Peer{Type: domain.PeerTypeUser, ID: peerID},
		Date:    1700000100,
		Message: "draft",
	}}}
	sessions := &captureScopedSessions{captureSessions: &captureSessions{}}
	r := New(Config{}, Deps{
		Dialogs:  dialogs,
		Sessions: sessions,
		Users: mapUsersService{users: map[int64]domain.User{
			userID: {ID: userID, FirstName: "Alice"},
			peerID: {ID: peerID, FirstName: "Bob"},
		}},
	}, zaptest.NewLogger(t), clock.System)
	ok, err := r.onMessagesClearAllDrafts(WithSessionID(WithUserID(context.Background(), userID), 9))
	if err != nil || !ok {
		t.Fatalf("clear all drafts = %v, %v", ok, err)
	}
	got := sessions.snapshot()
	updates, ok := got.message.(*tg.Updates)
	if !ok || len(updates.Updates) != 1 {
		t.Fatalf("pushed = %T %+v, want one update", got.message, got.message)
	}
	update, ok := updates.Updates[0].(*tg.UpdateDraftMessage)
	if !ok {
		t.Fatalf("update = %T", updates.Updates[0])
	}
	if _, ok := update.Draft.(*tg.DraftMessageEmpty); !ok {
		t.Fatalf("draft = %#v, want draftMessageEmpty", update.Draft)
	}
	if len(dialogs.drafts) != 0 {
		t.Fatalf("capture drafts = %+v, want cleared", dialogs.drafts)
	}
}

func TestTGUserIncludesRecentlyStatusForTypingEligibility(t *testing.T) {
	got := tgUser(domain.User{ID: 1000000002, FirstName: "Bob"})
	if _, ok := got.Status.(*tg.UserStatusRecently); !ok {
		t.Fatalf("status = %T, want *tg.UserStatusRecently", got.Status)
	}

	online := tgUser(domain.User{
		ID:        1000000002,
		FirstName: "Bob",
		Status:    domain.UserStatus{Kind: domain.UserStatusOnline, Expires: 1700000300},
	})
	if status, ok := online.Status.(*tg.UserStatusOnline); !ok || status.Expires != 1700000300 {
		t.Fatalf("online status = %#v, want userStatusOnline expires=1700000300", online.Status)
	}

	offline := tgUser(domain.User{
		ID:        1000000002,
		FirstName: "Bob",
		Status:    domain.UserStatus{Kind: domain.UserStatusOffline, WasOnline: 1700000000},
	})
	if status, ok := offline.Status.(*tg.UserStatusOffline); !ok || status.WasOnline != 1700000000 {
		t.Fatalf("offline status = %#v, want userStatusOffline was_online=1700000000", offline.Status)
	}
}

func TestRouterTGUserUsesPersistedLastSeen(t *testing.T) {
	r := New(Config{}, Deps{}, zaptest.NewLogger(t), clock.System)
	got := r.tgUser(domain.User{ID: 1000000002, FirstName: "Bob", LastSeenAt: 1700000000})
	if status, ok := got.Status.(*tg.UserStatusOffline); !ok || status.WasOnline != 1700000000 {
		t.Fatalf("status = %#v, want userStatusOffline was_online=1700000000", got.Status)
	}
}

func TestAccountUpdateStatusPushesPresenceToOnlineContacts(t *testing.T) {
	ctx := context.Background()
	alice := domain.User{ID: 1000000001, AccessHash: 11, FirstName: "Alice"}
	bob := domain.User{ID: 1000000002, AccessHash: 22, FirstName: "Bob", Contact: true}
	contacts := memory.NewContactStore()
	if err := contacts.SaveList(ctx, alice.ID, domain.ContactList{
		Contacts: []domain.Contact{{User: bob}},
	}); err != nil {
		t.Fatalf("save contacts: %v", err)
	}
	sessions := &captureSessions{onlineUserIDs: []int64{bob.ID}}
	r := New(Config{}, Deps{
		Contacts: appcontacts.NewService(contacts),
		Sessions: sessions,
	}, zaptest.NewLogger(t), clock.System)
	callCtx := WithSessionID(WithUserID(ctx, alice.ID), 77)

	ok, err := r.onAccountUpdateStatus(callCtx, false)
	if err != nil || !ok {
		t.Fatalf("account.updateStatus online = %v, %v", ok, err)
	}
	gotPushes := sessions.pushedUserIDs()
	if !reflect.DeepEqual(gotPushes, []int64{alice.ID, bob.ID}) {
		t.Fatalf("pushed users = %+v, want self and online contact", gotPushes)
	}
	update := pushedUserStatus(t, sessions.snapshot().message)
	if update.UserID != alice.ID {
		t.Fatalf("status user = %d, want alice", update.UserID)
	}
	if status, ok := update.Status.(*tg.UserStatusOnline); !ok || status.Expires <= int(time.Now().Unix()) {
		t.Fatalf("status = %#v, want online with future expires", update.Status)
	}

	ok, err = r.onAccountUpdateStatus(callCtx, true)
	if err != nil || !ok {
		t.Fatalf("account.updateStatus offline = %v, %v", ok, err)
	}
	update = pushedUserStatus(t, sessions.snapshot().message)
	if update.UserID != alice.ID {
		t.Fatalf("offline status user = %d, want alice", update.UserID)
	}
	if status, ok := update.Status.(*tg.UserStatusOffline); !ok || status.WasOnline == 0 {
		t.Fatalf("status = %#v, want offline with was_online", update.Status)
	}
}

func TestAccountUpdateStatusPersistsLastSeen(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	alice, err := userStore.Create(ctx, domain.User{AccessHash: 11, Phone: "1001", FirstName: "Alice"})
	if err != nil {
		t.Fatalf("create alice: %v", err)
	}
	const now = 1700000200
	r := New(Config{}, Deps{
		Users: appusers.NewService(userStore),
	}, zaptest.NewLogger(t), fixedClock{now: time.Unix(now, 0)})

	ok, err := r.onAccountUpdateStatus(WithSessionID(WithUserID(ctx, alice.ID), 77), false)
	if err != nil || !ok {
		t.Fatalf("account.updateStatus online = %v, %v", ok, err)
	}
	stored, found, err := userStore.ByID(ctx, alice.ID)
	if err != nil || !found {
		t.Fatalf("load alice found=%v err=%v", found, err)
	}
	if stored.LastSeenAt != now {
		t.Fatalf("last_seen_at after online = %d, want %d", stored.LastSeenAt, now)
	}
}

func TestAccountUpdateStatusPushesPresenceToOnlinePrivateDialogPeers(t *testing.T) {
	ctx := context.Background()
	alice := domain.User{ID: 1000000001, AccessHash: 11, FirstName: "Alice"}
	bob := domain.User{ID: 1000000002, AccessHash: 22, FirstName: "Bob"}
	dialogs := memory.NewDialogStore()
	if err := dialogs.SaveList(ctx, alice.ID, domain.DialogList{
		Dialogs: []domain.Dialog{{
			Peer:           domain.Peer{Type: domain.PeerTypeUser, ID: bob.ID},
			TopMessage:     10,
			TopMessageDate: 1700000000,
		}},
		Users: []domain.User{bob},
	}); err != nil {
		t.Fatalf("save dialogs: %v", err)
	}
	sessions := &captureSessions{onlineUserIDs: []int64{alice.ID}}
	r := New(Config{}, Deps{
		Dialogs:  appdialogs.NewService(dialogs),
		Sessions: sessions,
	}, zaptest.NewLogger(t), clock.System)

	ok, err := r.onAccountUpdateStatus(WithSessionID(WithUserID(ctx, bob.ID), 22), false)
	if err != nil || !ok {
		t.Fatalf("bob account.updateStatus online = %v, %v", ok, err)
	}
	gotPushes := sessions.pushedUserIDs()
	if !reflect.DeepEqual(gotPushes, []int64{bob.ID, alice.ID}) {
		t.Fatalf("pushed users = %+v, want self and online private dialog peer", gotPushes)
	}
	update := pushedUserStatus(t, sessions.snapshot().message)
	if update.UserID != bob.ID {
		t.Fatalf("status user = %d, want bob", update.UserID)
	}
	if status, ok := update.Status.(*tg.UserStatusOnline); !ok || status.Expires <= int(time.Now().Unix()) {
		t.Fatalf("status = %#v, want online with future expires", update.Status)
	}
}

func TestContactsStatusesUsesPersistedLastSeen(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	alice, err := userStore.Create(ctx, domain.User{AccessHash: 11, Phone: "1001", FirstName: "Alice"})
	if err != nil {
		t.Fatalf("create alice: %v", err)
	}
	bob, err := userStore.Create(ctx, domain.User{AccessHash: 22, Phone: "1002", FirstName: "Bob"})
	if err != nil {
		t.Fatalf("create bob: %v", err)
	}
	const lastSeen = 1700000000
	if err := userStore.UpdateLastSeen(ctx, bob.ID, lastSeen); err != nil {
		t.Fatalf("update last seen: %v", err)
	}
	contacts := memory.NewContactStore()
	if err := contacts.SaveList(ctx, alice.ID, domain.ContactList{
		Contacts: []domain.Contact{{User: domain.User{ID: bob.ID, FirstName: "Old Bob", Contact: true}}},
	}); err != nil {
		t.Fatalf("save contacts: %v", err)
	}
	r := New(Config{}, Deps{
		Contacts: appcontacts.NewService(contacts, userStore),
		Users:    appusers.NewService(userStore),
	}, zaptest.NewLogger(t), clock.System)

	statuses, err := r.onContactsGetStatuses(WithUserID(ctx, alice.ID))
	if err != nil {
		t.Fatalf("contacts.getStatuses: %v", err)
	}
	if len(statuses) != 1 || statuses[0].UserID != bob.ID {
		t.Fatalf("statuses = %+v, want bob only", statuses)
	}
	if status, ok := statuses[0].Status.(*tg.UserStatusOffline); !ok || status.WasOnline != lastSeen {
		t.Fatalf("status = %#v, want userStatusOffline was_online=%d", statuses[0].Status, lastSeen)
	}
}

func TestContactsStatusesAndContactsUsePresence(t *testing.T) {
	ctx := context.Background()
	alice := domain.User{ID: 1000000001, AccessHash: 11, FirstName: "Alice"}
	bob := domain.User{ID: 1000000002, AccessHash: 22, FirstName: "Bob", Contact: true}
	contacts := memory.NewContactStore()
	if err := contacts.SaveList(ctx, alice.ID, domain.ContactList{
		Contacts: []domain.Contact{{User: bob}},
	}); err != nil {
		t.Fatalf("save contacts: %v", err)
	}
	r := New(Config{}, Deps{
		Contacts: appcontacts.NewService(contacts),
	}, zaptest.NewLogger(t), clock.System)

	if ok, err := r.onAccountUpdateStatus(WithSessionID(WithUserID(ctx, bob.ID), 22), false); err != nil || !ok {
		t.Fatalf("bob account.updateStatus = %v, %v", ok, err)
	}
	statuses, err := r.onContactsGetStatuses(WithUserID(ctx, alice.ID))
	if err != nil {
		t.Fatalf("contacts.getStatuses: %v", err)
	}
	if len(statuses) != 1 || statuses[0].UserID != bob.ID {
		t.Fatalf("statuses = %+v, want bob only", statuses)
	}
	if _, ok := statuses[0].Status.(*tg.UserStatusOnline); !ok {
		t.Fatalf("contacts.getStatuses status = %T, want online", statuses[0].Status)
	}

	contactsRes, err := r.onContactsGetContacts(WithUserID(ctx, alice.ID), 0)
	if err != nil {
		t.Fatalf("contacts.getContacts: %v", err)
	}
	full, ok := contactsRes.(*tg.ContactsContacts)
	if !ok || len(full.Users) != 1 {
		t.Fatalf("contacts result = %T %+v, want one full contacts response", contactsRes, contactsRes)
	}
	user, ok := full.Users[0].(*tg.User)
	if !ok || user.ID != bob.ID {
		t.Fatalf("contacts user = %T %+v, want bob", full.Users[0], full.Users[0])
	}
	if _, ok := user.Status.(*tg.UserStatusOnline); !ok {
		t.Fatalf("contacts user status = %T, want online", user.Status)
	}
}

func TestContactsAcceptContactReturnsSettingsAndReset(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	contactsStore := memory.NewContactStore()
	alice, err := userStore.Create(ctx, domain.User{AccessHash: 11, Phone: "1001", FirstName: "Alice", LastName: "A"})
	if err != nil {
		t.Fatalf("create alice: %v", err)
	}
	bob, err := userStore.Create(ctx, domain.User{AccessHash: 22, Phone: "1002", FirstName: "Bob", LastName: "B"})
	if err != nil {
		t.Fatalf("create bob: %v", err)
	}
	contactsSvc := appcontacts.NewService(contactsStore, userStore)
	if _, err := contactsSvc.AddContact(ctx, alice.ID, domain.ContactInput{
		ContactUserID: bob.ID,
		Phone:         bob.Phone,
		FirstName:     "Bobby",
		LastName:      "Remark",
	}); err != nil {
		t.Fatalf("alice add bob: %v", err)
	}
	updatesSvc := &captureUpdates{state: domain.UpdateState{Pts: 10, Date: 1700000400}}
	r := New(Config{}, Deps{
		Contacts: contactsSvc,
		Users:    appusers.NewService(userStore, appusers.WithContactStore(contactsStore)),
		Updates:  updatesSvc,
	}, zaptest.NewLogger(t), fixedClock{now: time.Unix(1700000400, 0)})

	out, err := r.onContactsAcceptContact(WithUserID(ctx, alice.ID), &tg.InputUser{UserID: bob.ID, AccessHash: bob.AccessHash})
	if err != nil {
		t.Fatalf("contacts.acceptContact: %v", err)
	}
	got, ok := out.(*tg.Updates)
	if !ok {
		t.Fatalf("updates = %T, want *tg.Updates", out)
	}
	if len(got.Updates) != 2 {
		t.Fatalf("updates = %+v, want peer settings + contacts reset", got.Updates)
	}
	settings, ok := got.Updates[0].(*tg.UpdatePeerSettings)
	if !ok {
		t.Fatalf("update[0] = %T, want UpdatePeerSettings", got.Updates[0])
	}
	if settings.Settings.ShareContact || settings.Settings.AddContact {
		t.Fatalf("peer settings = %+v, want share/add false", settings.Settings)
	}
	if _, ok := got.Updates[1].(*tg.UpdateContactsReset); !ok {
		t.Fatalf("update[1] = %T, want UpdateContactsReset", got.Updates[1])
	}
	if len(updatesSvc.events) != 4 {
		t.Fatalf("recorded events = %+v, want current peer/reset and target peer/reset", updatesSvc.events)
	}
	if updatesSvc.events[0].UserID != alice.ID || updatesSvc.events[0].Settings.ShareContact {
		t.Fatalf("current peer settings event = %+v, want alice share=false", updatesSvc.events[0])
	}
	if updatesSvc.events[2].UserID != bob.ID || updatesSvc.events[2].Settings.ShareContact {
		t.Fatalf("target peer settings event = %+v, want bob share=false", updatesSvc.events[2])
	}
	reverse, found, err := contactsStore.Get(ctx, bob.ID, alice.ID)
	if err != nil || !found {
		t.Fatalf("bob contact alice found=%v err=%v", found, err)
	}
	if reverse.Phone != alice.Phone || !reverse.Mutual {
		t.Fatalf("bob contact alice = %+v, want shared phone and mutual", reverse)
	}
}

func TestContactsStatusesUsesOnlineSessionFallback(t *testing.T) {
	ctx := context.Background()
	alice := domain.User{ID: 1000000001, AccessHash: 11, FirstName: "Alice"}
	bob := domain.User{ID: 1000000002, AccessHash: 22, FirstName: "Bob", Contact: true}
	contacts := memory.NewContactStore()
	if err := contacts.SaveList(ctx, alice.ID, domain.ContactList{
		Contacts: []domain.Contact{{User: bob}},
	}); err != nil {
		t.Fatalf("save contacts: %v", err)
	}
	r := New(Config{}, Deps{
		Contacts: appcontacts.NewService(contacts),
		Sessions: &captureSessions{onlineUserIDs: []int64{bob.ID}},
	}, zaptest.NewLogger(t), clock.System)

	statuses, err := r.onContactsGetStatuses(WithUserID(ctx, alice.ID))
	if err != nil {
		t.Fatalf("contacts.getStatuses: %v", err)
	}
	if len(statuses) != 1 || statuses[0].UserID != bob.ID {
		t.Fatalf("statuses = %+v, want bob only", statuses)
	}
	if status, ok := statuses[0].Status.(*tg.UserStatusOnline); !ok || status.Expires <= int(time.Now().Unix()) {
		t.Fatalf("fallback status = %#v, want online from active session", statuses[0].Status)
	}
}

func TestContactsStatusesHonorsExplicitOffline(t *testing.T) {
	ctx := context.Background()
	alice := domain.User{ID: 1000000001, AccessHash: 11, FirstName: "Alice"}
	bob := domain.User{ID: 1000000002, AccessHash: 22, FirstName: "Bob", Contact: true}
	contacts := memory.NewContactStore()
	if err := contacts.SaveList(ctx, alice.ID, domain.ContactList{
		Contacts: []domain.Contact{{User: bob}},
	}); err != nil {
		t.Fatalf("save contacts: %v", err)
	}
	r := New(Config{}, Deps{
		Contacts: appcontacts.NewService(contacts),
		Sessions: &captureSessions{onlineUserIDs: []int64{bob.ID}},
	}, zaptest.NewLogger(t), clock.System)
	if ok, err := r.onAccountUpdateStatus(WithSessionID(WithUserID(ctx, bob.ID), 22), true); err != nil || !ok {
		t.Fatalf("bob account.updateStatus offline = %v, %v", ok, err)
	}

	statuses, err := r.onContactsGetStatuses(WithUserID(ctx, alice.ID))
	if err != nil {
		t.Fatalf("contacts.getStatuses: %v", err)
	}
	if len(statuses) != 1 || statuses[0].UserID != bob.ID {
		t.Fatalf("statuses = %+v, want bob only", statuses)
	}
	if status, ok := statuses[0].Status.(*tg.UserStatusOffline); !ok || status.WasOnline == 0 {
		t.Fatalf("status = %#v, want explicit offline", statuses[0].Status)
	}
}

func TestSessionOfflinePersistsLastSeenAndPushes(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	alice, err := userStore.Create(ctx, domain.User{AccessHash: 11, Phone: "1001", FirstName: "Alice"})
	if err != nil {
		t.Fatalf("create alice: %v", err)
	}
	bob, err := userStore.Create(ctx, domain.User{AccessHash: 22, Phone: "1002", FirstName: "Bob"})
	if err != nil {
		t.Fatalf("create bob: %v", err)
	}
	contacts := memory.NewContactStore()
	if err := contacts.SaveList(ctx, bob.ID, domain.ContactList{Contacts: []domain.Contact{{User: alice}}}); err != nil {
		t.Fatalf("save bob contacts: %v", err)
	}
	const now = 1700000300
	sessions := &captureSessions{onlineUserIDs: []int64{alice.ID}}
	r := New(Config{}, Deps{
		Contacts: appcontacts.NewService(contacts, userStore),
		Users:    appusers.NewService(userStore),
		Sessions: sessions,
	}, zaptest.NewLogger(t), fixedClock{now: time.Unix(now, 0)})

	r.SessionOffline([8]byte{1, 2, 3}, 22, bob.ID, true)

	stored, found, err := userStore.ByID(ctx, bob.ID)
	if err != nil || !found {
		t.Fatalf("load bob found=%v err=%v", found, err)
	}
	if stored.LastSeenAt != now {
		t.Fatalf("last_seen_at = %d, want %d", stored.LastSeenAt, now)
	}
	if got := sessions.pushedUserIDs(); !reflect.DeepEqual(got, []int64{bob.ID, alice.ID}) {
		t.Fatalf("pushed users = %+v, want bob self and online contact alice", got)
	}
	update := pushedUserStatus(t, sessions.snapshot().message)
	if update.UserID != bob.ID {
		t.Fatalf("status user = %d, want bob", update.UserID)
	}
	if status, ok := update.Status.(*tg.UserStatusOffline); !ok || status.WasOnline != now {
		t.Fatalf("status = %#v, want offline was_online=%d", update.Status, now)
	}
}

func TestAccountUpdateStatusKeepsUserOnlineUntilAllSessionsOffline(t *testing.T) {
	const userID = int64(1000000001)
	r := New(Config{}, Deps{}, zaptest.NewLogger(t), clock.System)
	session1 := WithSessionID(WithUserID(context.Background(), userID), 1)
	session2 := WithSessionID(WithUserID(context.Background(), userID), 2)

	if ok, err := r.onAccountUpdateStatus(session1, false); err != nil || !ok {
		t.Fatalf("session1 online = %v, %v", ok, err)
	}
	if ok, err := r.onAccountUpdateStatus(session2, false); err != nil || !ok {
		t.Fatalf("session2 online = %v, %v", ok, err)
	}
	if ok, err := r.onAccountUpdateStatus(session1, true); err != nil || !ok {
		t.Fatalf("session1 offline = %v, %v", ok, err)
	}
	if status := r.userPresenceStatus(userID); status.Kind != domain.UserStatusOnline {
		t.Fatalf("aggregate after one offline = %+v, want online", status)
	}
	if ok, err := r.onAccountUpdateStatus(session2, true); err != nil || !ok {
		t.Fatalf("session2 offline = %v, %v", ok, err)
	}
	if status := r.userPresenceStatus(userID); status.Kind != domain.UserStatusOffline || status.WasOnline == 0 {
		t.Fatalf("aggregate after all offline = %+v, want offline", status)
	}
}

func pushedUserStatus(t *testing.T, msg bin.Encoder) *tg.UpdateUserStatus {
	t.Helper()
	updates, ok := msg.(*tg.Updates)
	if !ok || len(updates.Updates) != 1 {
		t.Fatalf("pushed message = %T %+v, want one update", msg, msg)
	}
	update, ok := updates.Updates[0].(*tg.UpdateUserStatus)
	if !ok {
		t.Fatalf("update = %T, want *tg.UpdateUserStatus", updates.Updates[0])
	}
	return update
}

type fixedClock struct {
	now time.Time
}

func (c fixedClock) Now() time.Time {
	return c.now
}

func (c fixedClock) Timer(d time.Duration) clock.Timer {
	return clock.System.Timer(d)
}

func (c fixedClock) Ticker(d time.Duration) clock.Ticker {
	return clock.System.Ticker(d)
}

func TestMessagesSendReactionPrivatePeerReturnsReactionUpdate(t *testing.T) {
	const (
		userID = int64(1000000001)
		peerID = int64(1000000002)
		now    = int64(1700000200)
	)
	messages := &captureMessages{}
	r := New(Config{}, Deps{Messages: messages}, zaptest.NewLogger(t), fixedClock{now: time.Unix(now, 0)})
	req := &tg.MessagesSendReactionRequest{
		Peer:     &tg.InputPeerUser{UserID: peerID, AccessHash: 22},
		MsgID:    7,
		Reaction: []tg.ReactionClass{&tg.ReactionEmoji{Emoticon: "\U0001f44d"}},
		Big:      true,
	}
	req.SetReaction(req.Reaction)
	req.SetAddToRecent(true)

	updates, err := r.onMessagesSendReaction(WithUserID(context.Background(), userID), req)
	if err != nil {
		t.Fatalf("messages.sendReaction private: %v", err)
	}
	if messages.setReactionReq.UserID != userID || messages.setReactionReq.Peer != (domain.Peer{Type: domain.PeerTypeUser, ID: peerID}) || messages.setReactionReq.MessageID != req.MsgID || !messages.setReactionReq.Big || !messages.setReactionReq.AddToRecent {
		t.Fatalf("set reaction req = %+v, want private peer/message context", messages.setReactionReq)
	}
	got := updates.(*tg.Updates).Updates
	if len(got) != 1 {
		t.Fatalf("updates = %+v, want one reaction update", got)
	}
	update, ok := got[0].(*tg.UpdateMessageReactions)
	if !ok {
		t.Fatalf("update = %T, want *tg.UpdateMessageReactions", got[0])
	}
	peer, ok := update.Peer.(*tg.PeerUser)
	if !ok || peer.UserID != peerID || update.MsgID != req.MsgID {
		t.Fatalf("update peer/msg = %+v/%d, want peer %d msg %d", update.Peer, update.MsgID, peerID, req.MsgID)
	}
	if len(update.Reactions.Results) != 1 || update.Reactions.Results[0].Count != 1 || update.Reactions.Results[0].ChosenOrder != 1 {
		t.Fatalf("reaction results = %+v, want one chosen reaction", update.Reactions.Results)
	}
}

func TestMessagesSendReactionPrivatePushesViewerLocalMessageID(t *testing.T) {
	const (
		aliceID = int64(1000000001)
		bobID   = int64(1000000002)
		now     = int64(1700000200)
	)
	reaction := domain.MessageReaction{Type: domain.MessageReactionEmoji, Emoticon: "\U0001f44d"}
	aliceReactions := domain.ChannelMessageReactions{
		CanSeeList: true,
		Results: []domain.ChannelMessageReactionCount{{
			Reaction: reaction,
			Count:    1,
		}},
		Recent: []domain.ChannelMessagePeerReaction{{
			UserID:   bobID,
			Reaction: reaction,
			Date:     int(now),
		}},
	}
	bobReactions := domain.ChannelMessageReactions{
		CanSeeList: true,
		Results: []domain.ChannelMessageReactionCount{{
			Reaction:    reaction,
			Count:       1,
			ChosenOrder: 1,
		}},
		Recent: []domain.ChannelMessagePeerReaction{{
			UserID:      bobID,
			Reaction:    reaction,
			My:          true,
			ChosenOrder: 1,
			Date:        int(now),
		}},
	}
	messages := &captureMessages{
		setReactionRes: domain.PrivateMessageReactionsResult{
			Messages: []domain.Message{
				{
					ID:          68,
					UID:         7001,
					OwnerUserID: aliceID,
					Peer:        domain.Peer{Type: domain.PeerTypeUser, ID: bobID},
					From:        domain.Peer{Type: domain.PeerTypeUser, ID: aliceID},
					Date:        int(now),
					Reactions:   &aliceReactions,
				},
				{
					ID:          64,
					UID:         7001,
					OwnerUserID: bobID,
					Peer:        domain.Peer{Type: domain.PeerTypeUser, ID: aliceID},
					From:        domain.Peer{Type: domain.PeerTypeUser, ID: aliceID},
					Date:        int(now),
					Reactions:   &bobReactions,
				},
			},
			Reactions: bobReactions,
		},
	}
	sessions := &captureSessions{}
	r := New(Config{}, Deps{Messages: messages, Sessions: sessions}, zaptest.NewLogger(t), fixedClock{now: time.Unix(now, 0)})
	req := &tg.MessagesSendReactionRequest{
		Peer:     &tg.InputPeerUser{UserID: aliceID, AccessHash: 11},
		MsgID:    64,
		Reaction: []tg.ReactionClass{&tg.ReactionEmoji{Emoticon: "\U0001f44d"}},
	}
	req.SetReaction(req.Reaction)

	updates, err := r.onMessagesSendReaction(WithSessionID(WithUserID(context.Background(), bobID), 77), req)
	if err != nil {
		t.Fatalf("messages.sendReaction private: %v", err)
	}
	self := updates.(*tg.Updates).Updates[0].(*tg.UpdateMessageReactions)
	if peer, ok := self.Peer.(*tg.PeerUser); !ok || peer.UserID != aliceID || self.MsgID != 64 {
		t.Fatalf("self update peer/msg = %#v/%d, want alice/msg64", self.Peer, self.MsgID)
	}
	if got := sessions.pushedUserIDs(); len(got) != 2 || got[0] != bobID || got[1] != aliceID {
		t.Fatalf("pushed users = %+v, want bob then alice", got)
	}
	pushed := sessions.snapshot()
	if pushed.userID != aliceID || pushed.sessionID != 77 || pushed.messageType != proto.MessageFromServer {
		t.Fatalf("last push = user %d session %d type %v, want alice/exclude bob/from_server", pushed.userID, pushed.sessionID, pushed.messageType)
	}
	pushedUpdates, ok := pushed.message.(*tg.Updates)
	if !ok || len(pushedUpdates.Updates) != 1 {
		t.Fatalf("pushed message = %T %+v, want one updates container", pushed.message, pushed.message)
	}
	other, ok := pushedUpdates.Updates[0].(*tg.UpdateMessageReactions)
	if !ok {
		t.Fatalf("pushed update = %T, want *tg.UpdateMessageReactions", pushedUpdates.Updates[0])
	}
	peer, ok := other.Peer.(*tg.PeerUser)
	if !ok || peer.UserID != bobID || other.MsgID != 68 {
		t.Fatalf("pushed update peer/msg = %#v/%d, want bob/msg68", other.Peer, other.MsgID)
	}
	if len(other.Reactions.Results) != 1 || other.Reactions.Results[0].Count != 1 || other.Reactions.Results[0].ChosenOrder != 0 {
		t.Fatalf("pushed reaction results = %+v, want one non-chosen reaction", other.Reactions.Results)
	}
}

func TestMessagesSendMessageReturnsUpdateAndRecordsOwnerContext(t *testing.T) {
	sender := domain.User{ID: 1000000001, AccessHash: 11, FirstName: "Sender"}
	recipient := domain.User{ID: 1000000002, AccessHash: 22, FirstName: "Recipient"}
	messages := &captureMessages{}
	metrics := &captureRPCMetrics{}
	r := New(Config{}, Deps{
		Messages: messages,
		Users:    mapUsersService{users: map[int64]domain.User{sender.ID: sender, recipient.ID: recipient}},
		Metrics:  metrics,
	}, zaptest.NewLogger(t), clock.System)
	req := &tg.MessagesSendMessageRequest{
		Peer:     &tg.InputPeerUser{UserID: recipient.ID, AccessHash: recipient.AccessHash},
		Message:  "hello",
		RandomID: 123456,
		Entities: []tg.MessageEntityClass{
			&tg.MessageEntityBold{Offset: 0, Length: 5},
		},
	}
	var in bin.Buffer
	if err := req.Encode(&in); err != nil {
		t.Fatalf("encode request: %v", err)
	}

	enc, err := r.Dispatch(WithUserID(context.Background(), sender.ID), [8]byte{}, 77, &in)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	box, ok := enc.(*tg.UpdatesBox)
	if !ok {
		t.Fatalf("response = %T, want *tg.UpdatesBox", enc)
	}
	got, ok := box.Updates.(*tg.Updates)
	if !ok {
		t.Fatalf("boxed response = %T, want *tg.Updates", box.Updates)
	}
	if messages.sendUserID != sender.ID || messages.sendReq.SenderUserID != sender.ID || messages.sendReq.RecipientUserID != recipient.ID || messages.sendReq.OriginSessionID != 77 {
		t.Fatalf("send context = user %d req %+v, want sender/recipient/session", messages.sendUserID, messages.sendReq)
	}
	if len(messages.sendReq.Entities) != 1 || messages.sendReq.Entities[0].Type != domain.MessageEntityBold {
		t.Fatalf("entities = %+v, want bold entity converted to domain", messages.sendReq.Entities)
	}
	if len(got.Updates) != 2 {
		t.Fatalf("updates = %+v, want message id + new message", got.Updates)
	}
	if id, ok := got.Updates[0].(*tg.UpdateMessageID); !ok || id.ID != 1 || id.RandomID != req.RandomID {
		t.Fatalf("update id = %#v, want id=1 random_id=%d", got.Updates[0], req.RandomID)
	}
	newMsg, ok := got.Updates[1].(*tg.UpdateNewMessage)
	if !ok || newMsg.Pts != 1 || newMsg.PtsCount != 1 {
		t.Fatalf("new message update = %#v, want pts=1 pts_count=1", got.Updates[1])
	}
	msg, ok := newMsg.Message.(*tg.Message)
	if !ok || !msg.Out || msg.PeerID.(*tg.PeerUser).UserID != recipient.ID || msg.Message != req.Message {
		t.Fatalf("message = %#v, want outgoing private text to recipient", newMsg.Message)
	}
	if metrics.messageSend != 1 || metrics.messageSendErr != nil {
		t.Fatalf("metrics send=%d err=%v, want one successful send", metrics.messageSend, metrics.messageSendErr)
	}
}

func TestContactsBlockGetBlockedAndUnblockRPC(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	alice, err := userStore.Create(ctx, domain.User{AccessHash: 11, Phone: "15550009001", FirstName: "Alice"})
	if err != nil {
		t.Fatalf("create alice: %v", err)
	}
	bob, err := userStore.Create(ctx, domain.User{AccessHash: 22, Phone: "15550009002", FirstName: "Bob"})
	if err != nil {
		t.Fatalf("create bob: %v", err)
	}
	r := New(Config{}, Deps{
		Users:    appusers.NewService(userStore),
		Contacts: appcontacts.NewService(memory.NewContactStore(), userStore),
	}, zaptest.NewLogger(t), clock.System)

	ok, err := r.onContactsBlock(WithUserID(ctx, bob.ID), &tg.ContactsBlockRequest{
		ID: &tg.InputPeerUser{UserID: alice.ID, AccessHash: alice.AccessHash},
	})
	if err != nil || !ok {
		t.Fatalf("contacts.block = %v, %v", ok, err)
	}
	blocked, err := r.onContactsGetBlocked(WithUserID(ctx, bob.ID), &tg.ContactsGetBlockedRequest{Limit: 10})
	if err != nil {
		t.Fatalf("contacts.getBlocked: %v", err)
	}
	full, ok := blocked.(*tg.ContactsBlocked)
	if !ok || len(full.Blocked) != 1 || len(full.Users) != 1 {
		t.Fatalf("blocked = %T %+v, want one blocked user", blocked, blocked)
	}
	if peer, ok := full.Blocked[0].PeerID.(*tg.PeerUser); !ok || peer.UserID != alice.ID {
		t.Fatalf("blocked peer = %#v, want alice", full.Blocked[0].PeerID)
	}
	if user, ok := full.Users[0].(*tg.User); !ok || user.ID != alice.ID {
		t.Fatalf("blocked user = %#v, want alice", full.Users[0])
	}

	ok, err = r.onContactsUnblock(WithUserID(ctx, bob.ID), &tg.ContactsUnblockRequest{
		ID: &tg.InputPeerUser{UserID: alice.ID, AccessHash: alice.AccessHash},
	})
	if err != nil || !ok {
		t.Fatalf("contacts.unblock = %v, %v", ok, err)
	}
	blocked, err = r.onContactsGetBlocked(WithUserID(ctx, bob.ID), &tg.ContactsGetBlockedRequest{Limit: 10})
	if err != nil {
		t.Fatalf("contacts.getBlocked after unblock: %v", err)
	}
	if full, ok := blocked.(*tg.ContactsBlocked); !ok || len(full.Blocked) != 0 {
		t.Fatalf("blocked after unblock = %T %+v, want empty contacts.blocked", blocked, blocked)
	}
}

func TestMessagesPrivateBlockPreventsRecipientInboxAndRevokeRPC(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	alice, err := userStore.Create(ctx, domain.User{AccessHash: 11, Phone: "15550009101", FirstName: "Alice"})
	if err != nil {
		t.Fatalf("create alice: %v", err)
	}
	bob, err := userStore.Create(ctx, domain.User{AccessHash: 22, Phone: "15550009102", FirstName: "Bob"})
	if err != nil {
		t.Fatalf("create bob: %v", err)
	}
	dialogs := memory.NewDialogStore()
	messageStore := memory.NewMessageStore(dialogs)
	contactStore := memory.NewContactStore()
	r := New(Config{}, Deps{
		Users:    appusers.NewService(userStore),
		Contacts: appcontacts.NewService(contactStore, userStore),
		Messages: appmessages.NewService(messageStore, dialogs),
		Dialogs:  appdialogs.NewService(dialogs),
	}, zaptest.NewLogger(t), clock.System)

	delivered, err := r.onMessagesSendMessage(WithUserID(ctx, alice.ID), &tg.MessagesSendMessageRequest{
		Peer:     &tg.InputPeerUser{UserID: bob.ID, AccessHash: bob.AccessHash},
		Message:  "before block",
		RandomID: 91001,
	})
	if err != nil {
		t.Fatalf("send before block: %v", err)
	}
	deliveredUpdates := delivered.(*tg.Updates)
	deliveredMsg := deliveredUpdates.Updates[1].(*tg.UpdateNewMessage).Message.(*tg.Message)

	if ok, err := r.onContactsBlock(WithUserID(ctx, bob.ID), &tg.ContactsBlockRequest{
		ID: &tg.InputPeerUser{UserID: alice.ID, AccessHash: alice.AccessHash},
	}); err != nil || !ok {
		t.Fatalf("bob block alice = %v, %v", ok, err)
	}
	blockedSend, err := r.onMessagesSendMessage(WithUserID(ctx, alice.ID), &tg.MessagesSendMessageRequest{
		Peer:     &tg.InputPeerUser{UserID: bob.ID, AccessHash: bob.AccessHash},
		Message:  "after block",
		RandomID: 91002,
	})
	if err != nil {
		t.Fatalf("send after block: %v", err)
	}
	blockedUpdates := blockedSend.(*tg.Updates)
	blockedMsg := blockedUpdates.Updates[1].(*tg.UpdateNewMessage).Message.(*tg.Message)
	if blockedMsg.ID == 0 || !blockedMsg.Out {
		t.Fatalf("blocked sender update = %#v, want outgoing sender message", blockedMsg)
	}
	bobHistory, err := messageStore.ListByUser(ctx, bob.ID, domain.MessageFilter{
		HasPeer: true,
		Peer:    domain.Peer{Type: domain.PeerTypeUser, ID: alice.ID},
		Limit:   10,
	})
	if err != nil {
		t.Fatalf("bob history: %v", err)
	}
	if len(bobHistory.Messages) != 1 || bobHistory.Messages[0].Body != "before block" {
		t.Fatalf("bob history = %+v, want only pre-block delivered message", bobHistory.Messages)
	}
	aliceHistory, err := messageStore.ListByUser(ctx, alice.ID, domain.MessageFilter{
		HasPeer: true,
		Peer:    domain.Peer{Type: domain.PeerTypeUser, ID: bob.ID},
		Limit:   10,
	})
	if err != nil {
		t.Fatalf("alice history: %v", err)
	}
	if len(aliceHistory.Messages) != 2 {
		t.Fatalf("alice history len = %d, want delivered + sender-only blocked message", len(aliceHistory.Messages))
	}
	deleteReq := &tg.MessagesDeleteMessagesRequest{ID: []int{deliveredMsg.ID}}
	deleteReq.SetRevoke(true)
	if _, err := r.onMessagesDeleteMessages(WithUserID(ctx, alice.ID), deleteReq); err == nil || !strings.Contains(err.Error(), "DELETE_MESSAGES_FORBIDDEN") {
		t.Fatalf("revoke after block err = %v, want DELETE_MESSAGES_FORBIDDEN", err)
	}
}

func TestMessagesSendMessageSupportsReplyAndFlags(t *testing.T) {
	const (
		senderID    = int64(1000000001)
		recipientID = int64(1000000002)
	)
	messages := &captureMessages{}
	r := New(Config{}, Deps{
		Messages: messages,
		Users:    mapUsersService{users: map[int64]domain.User{senderID: {ID: senderID, FirstName: "Sender"}, recipientID: {ID: recipientID, FirstName: "Recipient"}}},
	}, zaptest.NewLogger(t), clock.System)
	reply := &tg.InputReplyToMessage{ReplyToMsgID: 7}
	reply.SetQuoteText("hello")
	reply.SetQuoteOffset(1)
	req := &tg.MessagesSendMessageRequest{
		Peer:     &tg.InputPeerUser{UserID: recipientID},
		Message:  "reply",
		RandomID: 456,
		Silent:   true,
	}
	req.SetNoforwards(true)
	req.SetReplyTo(reply)
	var in bin.Buffer
	if err := req.Encode(&in); err != nil {
		t.Fatalf("encode request: %v", err)
	}

	enc, err := r.Dispatch(WithUserID(context.Background(), senderID), [8]byte{}, 88, &in)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if messages.sendReq.ReplyTo == nil || messages.sendReq.ReplyTo.MessageID != 7 || messages.sendReq.ReplyTo.Peer.ID != recipientID || messages.sendReq.ReplyTo.QuoteText != "hello" {
		t.Fatalf("reply request = %+v, want reply metadata", messages.sendReq.ReplyTo)
	}
	if !messages.sendReq.Silent || !messages.sendReq.NoForwards {
		t.Fatalf("send flags silent=%v noforwards=%v, want true/true", messages.sendReq.Silent, messages.sendReq.NoForwards)
	}
	box, ok := enc.(*tg.UpdatesBox)
	if !ok {
		t.Fatalf("response = %T, want *tg.UpdatesBox", enc)
	}
	got := box.Updates.(*tg.Updates)
	newMsg := got.Updates[1].(*tg.UpdateNewMessage)
	msg := newMsg.Message.(*tg.Message)
	if !msg.Silent || !msg.Noforwards {
		t.Fatalf("message flags silent=%v noforwards=%v, want true/true", msg.Silent, msg.Noforwards)
	}
	header, ok := msg.ReplyTo.(*tg.MessageReplyHeader)
	if !ok || header.ReplyToMsgID != 7 {
		t.Fatalf("reply header = %#v, want msg id 7", msg.ReplyTo)
	}
}

func TestMessagesSendMessageRejectsHugeReplyQuoteOffset(t *testing.T) {
	const (
		senderID    = int64(1000000001)
		recipientID = int64(1000000002)
	)
	messages := &captureMessages{}
	r := New(Config{}, Deps{
		Messages: messages,
		Users:    mapUsersService{users: map[int64]domain.User{recipientID: {ID: recipientID, FirstName: "Recipient"}}},
	}, zaptest.NewLogger(t), clock.System)
	reply := &tg.InputReplyToMessage{ReplyToMsgID: 7}
	reply.SetQuoteText("hello")
	reply.SetQuoteOffset(domain.MaxMessageReplyQuoteOffset + 1)
	req := &tg.MessagesSendMessageRequest{
		Peer:     &tg.InputPeerUser{UserID: recipientID},
		Message:  "reply",
		RandomID: 457,
	}
	req.SetReplyTo(reply)

	if _, err := r.onMessagesSendMessage(WithUserID(context.Background(), senderID), req); err == nil || !strings.Contains(err.Error(), "REPLY_MESSAGE_ID_INVALID") {
		t.Fatalf("huge quote offset err = %v, want REPLY_MESSAGE_ID_INVALID", err)
	}
	if messages.sendReq.RandomID != 0 {
		t.Fatalf("send request reached service: %+v", messages.sendReq)
	}
}

func TestMessageReplyFromInputUnsupportedShapesReturnExplicitErrors(t *testing.T) {
	const userID = int64(1000000001)
	ctx := WithUserID(context.Background(), userID)
	r := New(Config{}, Deps{}, zaptest.NewLogger(t), clock.System)
	peer := domain.Peer{Type: domain.PeerTypeChannel, ID: 1000000002}
	withReplyMsg := func(update func(*tg.InputReplyToMessage)) *tg.InputReplyToMessage {
		reply := &tg.InputReplyToMessage{ReplyToMsgID: 7}
		update(reply)
		return reply
	}
	cases := []struct {
		name  string
		input tg.InputReplyToClass
		want  string
	}{
		{
			name:  "story",
			input: &tg.InputReplyToStory{Peer: &tg.InputPeerUser{UserID: 1000000003}, StoryID: 1},
			want:  "STORY_ID_INVALID",
		},
		{
			name:  "monoforum constructor",
			input: &tg.InputReplyToMonoForum{MonoforumPeerID: &tg.InputPeerChannel{ChannelID: 1000000004}},
			want:  "REPLY_TO_MONOFORUM_PEER_INVALID",
		},
		{
			name: "monoforum field",
			input: withReplyMsg(func(reply *tg.InputReplyToMessage) {
				reply.SetMonoforumPeerID(&tg.InputPeerChannel{ChannelID: 1000000004})
			}),
			want: "REPLY_TO_MONOFORUM_PEER_INVALID",
		},
		{
			name: "todo item",
			input: withReplyMsg(func(reply *tg.InputReplyToMessage) {
				reply.SetTodoItemID(1)
			}),
			want: "REPLY_MESSAGE_ID_INVALID",
		},
		{
			name: "poll option",
			input: withReplyMsg(func(reply *tg.InputReplyToMessage) {
				reply.SetPollOption([]byte{1})
			}),
			want: "POLL_OPTION_INVALID",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := r.messageReplyFromInput(ctx, userID, peer, tc.input); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("reply err = %v, want %s", err, tc.want)
			}
		})
	}
}

func TestMessagesSendMessageUnsupportedOptionErrors(t *testing.T) {
	const (
		senderID    = int64(1000000001)
		recipientID = int64(1000000002)
	)
	ctx := WithUserID(context.Background(), senderID)
	r := New(Config{}, Deps{}, zaptest.NewLogger(t), clock.System)
	base := func() *tg.MessagesSendMessageRequest {
		return &tg.MessagesSendMessageRequest{
			Peer:     &tg.InputPeerUser{UserID: recipientID},
			Message:  "hello",
			RandomID: 456,
		}
	}
	suggested := func() tg.SuggestedPost {
		post := tg.SuggestedPost{}
		post.SetAccepted(true)
		return post
	}
	cases := []struct {
		name string
		req  *tg.MessagesSendMessageRequest
		want string
	}{
		{
			name: "reply markup",
			req: func() *tg.MessagesSendMessageRequest {
				req := base()
				req.SetReplyMarkup(&tg.ReplyKeyboardHide{})
				return req
			}(),
			want: "REPLY_MARKUP_INVALID",
		},
		{
			name: "quick reply",
			req: func() *tg.MessagesSendMessageRequest {
				req := base()
				req.SetQuickReplyShortcut(&tg.InputQuickReplyShortcut{Shortcut: "hello"})
				return req
			}(),
			want: "SHORTCUT_INVALID",
		},
		{
			name: "effect",
			req: func() *tg.MessagesSendMessageRequest {
				req := base()
				req.SetEffect(1)
				return req
			}(),
			want: "EFFECT_ID_INVALID",
		},
		{
			name: "negative paid stars",
			req: func() *tg.MessagesSendMessageRequest {
				req := base()
				req.SetAllowPaidStars(-1)
				return req
			}(),
			want: "STARS_AMOUNT_INVALID",
		},
		{
			name: "paid stars",
			req: func() *tg.MessagesSendMessageRequest {
				req := base()
				req.SetAllowPaidStars(1)
				return req
			}(),
			want: "PAYMENT_UNSUPPORTED",
		},
		{
			name: "paid floodskip",
			req: func() *tg.MessagesSendMessageRequest {
				req := base()
				req.SetAllowPaidFloodskip(true)
				return req
			}(),
			want: "PAYMENT_UNSUPPORTED",
		},
		{
			name: "suggested post",
			req: func() *tg.MessagesSendMessageRequest {
				req := base()
				req.SetSuggestedPost(suggested())
				return req
			}(),
			want: "SUGGESTED_POST_PEER_INVALID",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := r.onMessagesSendMessage(ctx, tc.req); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("send err = %v, want %s", err, tc.want)
			}
		})
	}
}

func TestMessagesForwardMessagesRecordsRequestAndReturnsUpdates(t *testing.T) {
	const (
		ownerID = int64(1000000001)
		fromID  = int64(1000000002)
		toID    = int64(1000000003)
	)
	messages := &captureMessages{}
	r := New(Config{}, Deps{
		Messages: messages,
		Users: mapUsersService{users: map[int64]domain.User{
			ownerID: {ID: ownerID, FirstName: "Owner"},
			fromID:  {ID: fromID, FirstName: "From"},
			toID:    {ID: toID, FirstName: "To"},
		}},
	}, zaptest.NewLogger(t), clock.System)
	req := &tg.MessagesForwardMessagesRequest{
		FromPeer:   &tg.InputPeerUser{UserID: fromID},
		ToPeer:     &tg.InputPeerUser{UserID: toID},
		ID:         []int{3, 4},
		RandomID:   []int64{1001, 1002},
		Silent:     true,
		Noforwards: true,
	}
	replyTo := &tg.InputReplyToMessage{ReplyToMsgID: 9}
	replyTo.SetQuoteText("target")
	req.SetReplyTo(replyTo)
	var in bin.Buffer
	if err := req.Encode(&in); err != nil {
		t.Fatalf("encode request: %v", err)
	}

	enc, err := r.Dispatch(WithUserID(context.Background(), ownerID), [8]byte{}, 99, &in)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if messages.forwardUserID != ownerID || messages.forwardReq.FromPeer.ID != fromID || messages.forwardReq.ToUserID != toID || len(messages.forwardReq.MessageIDs) != 2 || messages.forwardReq.OriginSessionID != 99 {
		t.Fatalf("forward request = user %d %+v, want owner/from/to/session", messages.forwardUserID, messages.forwardReq)
	}
	if messages.forwardReq.ReplyTo == nil || messages.forwardReq.ReplyTo.MessageID != 9 || messages.forwardReq.ReplyTo.Peer.ID != toID || messages.forwardReq.ReplyTo.QuoteText != "target" {
		t.Fatalf("forward reply = %+v, want target peer reply metadata", messages.forwardReq.ReplyTo)
	}
	box, ok := enc.(*tg.UpdatesBox)
	if !ok {
		t.Fatalf("response = %T, want *tg.UpdatesBox", enc)
	}
	got := box.Updates.(*tg.Updates)
	if len(got.Updates) != 4 {
		t.Fatalf("updates = %+v, want two message ids and two new messages", got.Updates)
	}
	if id, ok := got.Updates[0].(*tg.UpdateMessageID); !ok || id.RandomID != 1001 {
		t.Fatalf("first update = %#v, want updateMessageID random 1001", got.Updates[0])
	}
	newMsg := got.Updates[1].(*tg.UpdateNewMessage)
	msg := newMsg.Message.(*tg.Message)
	if msg.FwdFrom.Date == 0 || !msg.Silent || !msg.Noforwards {
		t.Fatalf("forwarded message = %#v, want fwd header and flags", msg)
	}
	if header, ok := msg.ReplyTo.(*tg.MessageReplyHeader); !ok || header.ReplyToMsgID != 9 {
		t.Fatalf("forwarded reply = %#v, want reply header id=9", msg.ReplyTo)
	}
	hasForwardAuthor := false
	for _, user := range got.Users {
		if u, ok := user.(*tg.User); ok && u.ID == fromID {
			hasForwardAuthor = true
			break
		}
	}
	if !hasForwardAuthor {
		t.Fatalf("forward users = %+v, want original author %d for fwd_from", got.Users, fromID)
	}
}

func TestMessagesForwardMessagesUnsupportedOptionErrors(t *testing.T) {
	const ownerID = int64(1000000001)
	ctx := WithUserID(context.Background(), ownerID)
	r := New(Config{}, Deps{}, zaptest.NewLogger(t), clock.System)
	base := func() *tg.MessagesForwardMessagesRequest {
		return &tg.MessagesForwardMessagesRequest{
			FromPeer: &tg.InputPeerUser{UserID: 1000000002},
			ToPeer:   &tg.InputPeerUser{UserID: 1000000003},
			ID:       []int{3},
			RandomID: []int64{1001},
		}
	}
	suggested := func() tg.SuggestedPost {
		post := tg.SuggestedPost{}
		post.SetAccepted(true)
		return post
	}
	cases := []struct {
		name string
		req  *tg.MessagesForwardMessagesRequest
		want string
	}{
		{
			name: "quick reply",
			req: func() *tg.MessagesForwardMessagesRequest {
				req := base()
				req.SetQuickReplyShortcut(&tg.InputQuickReplyShortcut{Shortcut: "hello"})
				return req
			}(),
			want: "SHORTCUT_INVALID",
		},
		{
			name: "effect",
			req: func() *tg.MessagesForwardMessagesRequest {
				req := base()
				req.SetEffect(1)
				return req
			}(),
			want: "EFFECT_ID_INVALID",
		},
		{
			name: "video timestamp without media model",
			req: func() *tg.MessagesForwardMessagesRequest {
				req := base()
				req.SetVideoTimestamp(10)
				return req
			}(),
			want: "MEDIA_INVALID",
		},
		{
			name: "negative paid stars",
			req: func() *tg.MessagesForwardMessagesRequest {
				req := base()
				req.SetAllowPaidStars(-1)
				return req
			}(),
			want: "STARS_AMOUNT_INVALID",
		},
		{
			name: "paid floodskip",
			req: func() *tg.MessagesForwardMessagesRequest {
				req := base()
				req.SetAllowPaidFloodskip(true)
				return req
			}(),
			want: "PAYMENT_UNSUPPORTED",
		},
		{
			name: "suggested post",
			req: func() *tg.MessagesForwardMessagesRequest {
				req := base()
				req.SetSuggestedPost(suggested())
				return req
			}(),
			want: "SUGGESTED_POST_PEER_INVALID",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := r.onMessagesForwardMessages(ctx, tc.req); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("forward err = %v, want %s", err, tc.want)
			}
		})
	}
}

func TestMessagesEditMessageReturnsUpdateAndRecordsOwnerContext(t *testing.T) {
	const (
		userID = int64(1000000001)
		peerID = int64(1000000002)
	)
	var authKeyID [8]byte
	authKeyID[0] = 9
	messages := &captureMessages{}
	r := New(Config{}, Deps{Messages: messages}, zaptest.NewLogger(t), clock.System)
	req := &tg.MessagesEditMessageRequest{
		Peer: &tg.InputPeerUser{UserID: peerID, AccessHash: 22},
		ID:   3,
	}
	req.SetMessage("edited")
	req.SetEntities([]tg.MessageEntityClass{&tg.MessageEntityBold{Offset: 0, Length: 6}})
	var in bin.Buffer
	if err := req.Encode(&in); err != nil {
		t.Fatalf("encode request: %v", err)
	}

	enc, err := r.Dispatch(WithUserID(context.Background(), userID), authKeyID, 77, &in)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	box, ok := enc.(*tg.UpdatesBox)
	if !ok {
		t.Fatalf("response = %T, want *tg.UpdatesBox", enc)
	}
	got, ok := box.Updates.(*tg.Updates)
	if !ok || len(got.Updates) != 1 {
		t.Fatalf("boxed updates = %T %+v, want one update", box.Updates, box.Updates)
	}
	edit, ok := got.Updates[0].(*tg.UpdateEditMessage)
	if !ok || edit.Pts != 7 || edit.PtsCount != 1 {
		t.Fatalf("edit update = %#v, want pts=7 count=1", got.Updates[0])
	}
	msg, ok := edit.Message.(*tg.Message)
	if !ok || msg.ID != 3 || msg.Message != "edited" {
		t.Fatalf("edited message = %#v, want id=3 text edited", edit.Message)
	}
	if messages.editReq.OwnerUserID != userID || messages.editReq.Peer.ID != peerID || messages.editReq.ID != 3 || messages.editReq.OriginAuthKeyID != authKeyID || messages.editReq.OriginSessionID != 77 {
		t.Fatalf("edit request = %+v, want owner peer message id and origin", messages.editReq)
	}
	if len(messages.editReq.Entities) != 1 || messages.editReq.Entities[0].Type != domain.MessageEntityBold {
		t.Fatalf("edit entities = %+v, want bold", messages.editReq.Entities)
	}
}

func TestMessagesEditMessageOptionBoundaries(t *testing.T) {
	const (
		userID = int64(1000000001)
		peerID = int64(1000000002)
	)
	ctx := WithUserID(context.Background(), userID)
	r := New(Config{}, Deps{Messages: &captureMessages{}}, zaptest.NewLogger(t), clock.System)

	webPreviewReq := &tg.MessagesEditMessageRequest{
		Peer: &tg.InputPeerUser{UserID: peerID, AccessHash: 22},
		ID:   3,
	}
	webPreviewReq.SetMessage("edited with link")
	webPreviewReq.SetMedia(&tg.InputMediaWebPage{URL: "https://example.test/"})
	if _, err := r.onMessagesEditMessage(ctx, webPreviewReq); err != nil {
		t.Fatalf("edit with webpage media err = %v, want text-only downgrade", err)
	}

	cases := []struct {
		name string
		req  *tg.MessagesEditMessageRequest
		want string
	}{
		{
			name: "quick reply shortcut",
			req: func() *tg.MessagesEditMessageRequest {
				req := &tg.MessagesEditMessageRequest{Peer: &tg.InputPeerUser{UserID: peerID, AccessHash: 22}, ID: 3}
				req.SetMessage("edited")
				req.SetQuickReplyShortcutID(11)
				return req
			}(),
			want: "MESSAGE_ID_INVALID",
		},
		{
			name: "unsupported media",
			req: func() *tg.MessagesEditMessageRequest {
				req := &tg.MessagesEditMessageRequest{Peer: &tg.InputPeerUser{UserID: peerID, AccessHash: 22}, ID: 3}
				req.SetMessage("edited")
				req.SetMedia(&tg.InputMediaUploadedPhoto{})
				return req
			}(),
			want: "MEDIA_INVALID",
		},
		{
			name: "reply markup",
			req: func() *tg.MessagesEditMessageRequest {
				req := &tg.MessagesEditMessageRequest{Peer: &tg.InputPeerUser{UserID: peerID, AccessHash: 22}, ID: 3}
				req.SetMessage("edited")
				req.SetReplyMarkup(&tg.ReplyKeyboardHide{})
				return req
			}(),
			want: "REPLY_MARKUP_INVALID",
		},
		{
			name: "message flag missing",
			req:  &tg.MessagesEditMessageRequest{Peer: &tg.InputPeerUser{UserID: peerID, AccessHash: 22}, ID: 3},
			want: "MESSAGE_EMPTY",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := r.onMessagesEditMessage(ctx, tc.req); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("edit err = %v, want %s", err, tc.want)
			}
		})
	}
}

func TestMessagesGetMessageEditDataPrivateValidatesAuthor(t *testing.T) {
	const (
		userID = int64(1000000001)
		peerID = int64(1000000002)
	)
	messages := &captureMessages{list: domain.MessageList{
		Messages: []domain.Message{{
			ID:          3,
			OwnerUserID: userID,
			Peer:        domain.Peer{Type: domain.PeerTypeUser, ID: peerID},
			From:        domain.Peer{Type: domain.PeerTypeUser, ID: userID},
			Out:         true,
			Body:        "editable",
		}},
	}}
	r := New(Config{}, Deps{Messages: messages}, zaptest.NewLogger(t), clock.System)

	got, err := r.onMessagesGetMessageEditData(WithUserID(context.Background(), userID), &tg.MessagesGetMessageEditDataRequest{
		Peer: &tg.InputPeerUser{UserID: peerID, AccessHash: 22},
		ID:   3,
	})
	if err != nil {
		t.Fatalf("get private edit data: %v", err)
	}
	if got.GetCaption() {
		t.Fatalf("private edit data caption = true, want false for text-only message")
	}

	messages.list.Messages[0].Out = false
	if _, err := r.onMessagesGetMessageEditData(WithUserID(context.Background(), userID), &tg.MessagesGetMessageEditDataRequest{
		Peer: &tg.InputPeerUser{UserID: peerID, AccessHash: 22},
		ID:   3,
	}); err == nil || !strings.Contains(err.Error(), "MESSAGE_AUTHOR_REQUIRED") {
		t.Fatalf("non-author edit data err = %v, want MESSAGE_AUTHOR_REQUIRED", err)
	}
}

func TestMessagesGetOutboxReadDateReturnsDate(t *testing.T) {
	const (
		userID = int64(1000000001)
		peerID = int64(1000000002)
	)
	messages := &captureMessages{outboxReadDate: 1700000300}
	r := New(Config{}, Deps{Messages: messages}, zaptest.NewLogger(t), clock.System)
	req := &tg.MessagesGetOutboxReadDateRequest{
		Peer:  &tg.InputPeerUser{UserID: peerID, AccessHash: 22},
		MsgID: 3,
	}
	var in bin.Buffer
	if err := req.Encode(&in); err != nil {
		t.Fatalf("encode request: %v", err)
	}

	enc, err := r.Dispatch(WithUserID(context.Background(), userID), [8]byte{}, 77, &in)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	got, ok := enc.(*tg.OutboxReadDate)
	if !ok || got.Date != 1700000300 {
		t.Fatalf("response = %T %#v, want outboxReadDate date", enc, enc)
	}
	if messages.outboxReadDateReq.OwnerUserID != userID || messages.outboxReadDateReq.Peer.ID != peerID || messages.outboxReadDateReq.ID != 3 {
		t.Fatalf("read date request = %+v, want owner peer message id", messages.outboxReadDateReq)
	}
}

func TestMessagesReadMessageContentsPushesUpdateToOtherSessions(t *testing.T) {
	authKeyID := [8]byte{9, 9, 9}
	messages := &captureMessages{
		readContentsRes: domain.ReadMessageContentsResult{
			OwnerUserID: 1000000001,
			MessageIDs:  []int{7, 8},
		},
	}
	sessions := &captureSessions{}
	r := New(Config{}, Deps{
		Messages: messages,
		Updates:  &captureUpdates{state: domain.UpdateState{Pts: 42, Date: 1700000200}},
		Sessions: sessions,
	}, zaptest.NewLogger(t), clock.System)
	ctx := WithSessionID(WithAuthKeyID(WithUserID(context.Background(), 1000000001), authKeyID), 55)

	affected, err := r.onMessagesReadMessageContents(ctx, []int{7, 8})
	if err != nil {
		t.Fatalf("messages.readMessageContents: %v", err)
	}
	if affected.Pts != 42 || affected.PtsCount != 0 {
		t.Fatalf("affected = %+v, want pts=42 pts_count=0", affected)
	}
	if messages.readContentsReq.OwnerUserID != 1000000001 || !reflect.DeepEqual(messages.readContentsReq.IDs, []int{7, 8}) {
		t.Fatalf("read contents req = %+v", messages.readContentsReq)
	}
	snap := sessions.snapshot()
	if snap.userID != 1000000001 || snap.sessionID != 55 || snap.messageType != proto.MessageFromServer {
		t.Fatalf("push target = %+v", snap)
	}
	updates, ok := snap.message.(*tg.Updates)
	if !ok {
		t.Fatalf("pushed message = %T, want *tg.Updates", snap.message)
	}
	if len(updates.Updates) != 1 {
		t.Fatalf("updates = %+v", updates.Updates)
	}
	read, ok := updates.Updates[0].(*tg.UpdateReadMessagesContents)
	if !ok {
		t.Fatalf("update = %T, want *tg.UpdateReadMessagesContents", updates.Updates[0])
	}
	if !reflect.DeepEqual(read.Messages, []int{7, 8}) || read.Pts != 42 || read.PtsCount != 0 {
		t.Fatalf("read update = %+v", read)
	}
}

func TestMessagesReadHistoryMarksDialogRead(t *testing.T) {
	var authKeyID [8]byte
	authKeyID[0] = 7
	messages := &captureMessages{readResult: domain.ReadHistoryResult{
		OwnerUserID: 1000000001,
		Peer:        domain.Peer{Type: domain.PeerTypeUser, ID: domain.OfficialSystemUserID},
		MaxID:       12,
		Changed:     true,
		InboxEvent: domain.UpdateEvent{
			Type:     domain.UpdateEventReadHistoryInbox,
			Pts:      5,
			PtsCount: 1,
			Date:     1700000100,
			Peer:     domain.Peer{Type: domain.PeerTypeUser, ID: domain.OfficialSystemUserID},
			MaxID:    12,
		},
	}}
	updates := &captureUpdates{state: domain.UpdateState{Pts: 5, Date: 1700000100, Seq: 3}}
	sessions := &captureSessions{}
	r := New(Config{}, Deps{Messages: messages, Updates: updates, Sessions: sessions}, zaptest.NewLogger(t), clock.System)
	req := &tg.MessagesReadHistoryRequest{
		Peer:  &tg.InputPeerUser{UserID: domain.OfficialSystemUserID, AccessHash: domain.OfficialSystemUser().AccessHash},
		MaxID: 12,
	}
	var in bin.Buffer
	if err := req.Encode(&in); err != nil {
		t.Fatalf("encode request: %v", err)
	}

	enc, err := r.Dispatch(WithUserID(context.Background(), 1000000001), authKeyID, 0, &in)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	got, ok := enc.(*tg.MessagesAffectedMessages)
	if !ok {
		t.Fatalf("response = %T, want *tg.MessagesAffectedMessages", enc)
	}
	if messages.readPeer.ID != domain.OfficialSystemUserID || messages.readMaxID != 12 {
		t.Fatalf("read = peer %+v max %d, want official/12", messages.readPeer, messages.readMaxID)
	}
	if got.Pts != 5 || got.PtsCount != 1 {
		t.Fatalf("affected = %+v, want recorded read-history pts", got)
	}
	gotSession := sessions.snapshot()
	if gotSession.userID != 1000000001 || gotSession.messageType != proto.MessageFromServer {
		t.Fatalf("push target = user %d type %v, want read update push to other sessions", gotSession.userID, gotSession.messageType)
	}
}

func TestMessagesDeleteMessagesPassesOwnerContext(t *testing.T) {
	const userID = int64(1000000001)
	var authKeyID [8]byte
	authKeyID[0] = 10
	messages := &captureMessages{deleteMessagesRes: domain.DeleteMessagesResult{
		OwnerUserID: userID,
		Deleted: []domain.DeletedMessagesForUser{{
			UserID:     userID,
			MessageIDs: []int{2, 3},
			Event:      domain.UpdateEvent{Pts: 9, PtsCount: 2},
		}},
	}}
	r := New(Config{}, Deps{Messages: messages}, zaptest.NewLogger(t), clock.System)
	req := &tg.MessagesDeleteMessagesRequest{Revoke: true, ID: []int{3, 2}}
	var in bin.Buffer
	if err := req.Encode(&in); err != nil {
		t.Fatalf("encode request: %v", err)
	}

	enc, err := r.Dispatch(WithUserID(context.Background(), userID), authKeyID, 77, &in)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	got, ok := enc.(*tg.MessagesAffectedMessages)
	if !ok {
		t.Fatalf("response = %T, want *tg.MessagesAffectedMessages", enc)
	}
	if got.Pts != 9 || got.PtsCount != 2 {
		t.Fatalf("affected = %+v, want pts=9 pts_count=2", got)
	}
	if messages.deleteMessagesReq.OwnerUserID != userID || !messages.deleteMessagesReq.Revoke || messages.deleteMessagesReq.OriginSessionID != 77 || messages.deleteMessagesReq.OriginAuthKeyID != authKeyID {
		t.Fatalf("delete request = %+v, want owner/revoke/current session", messages.deleteMessagesReq)
	}
	if len(messages.deleteMessagesReq.IDs) != 2 || messages.deleteMessagesReq.IDs[0] != 3 || messages.deleteMessagesReq.IDs[1] != 2 {
		t.Fatalf("delete ids = %+v, want request order [3 2]", messages.deleteMessagesReq.IDs)
	}
}

func TestMessagesDeleteHistoryPassesJustClearContext(t *testing.T) {
	const (
		userID = int64(1000000001)
		peerID = int64(1000000002)
	)
	var authKeyID [8]byte
	authKeyID[0] = 11
	messages := &captureMessages{deleteHistoryRes: domain.DeleteMessagesResult{
		OwnerUserID: userID,
		Deleted: []domain.DeletedMessagesForUser{{
			UserID:     userID,
			MessageIDs: []int{1, 2, 3},
			Event:      domain.UpdateEvent{Pts: 12, PtsCount: 3},
		}},
	}}
	r := New(Config{}, Deps{Messages: messages}, zaptest.NewLogger(t), clock.System)
	req := &tg.MessagesDeleteHistoryRequest{
		JustClear: true,
		Revoke:    true,
		Peer:      &tg.InputPeerUser{UserID: peerID, AccessHash: 22},
		MaxID:     15,
	}
	var in bin.Buffer
	if err := req.Encode(&in); err != nil {
		t.Fatalf("encode request: %v", err)
	}

	enc, err := r.Dispatch(WithUserID(context.Background(), userID), authKeyID, 88, &in)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	got, ok := enc.(*tg.MessagesAffectedHistory)
	if !ok {
		t.Fatalf("response = %T, want *tg.MessagesAffectedHistory", enc)
	}
	if got.Pts != 12 || got.PtsCount != 3 {
		t.Fatalf("affected = %+v, want pts=12 pts_count=3", got)
	}
	reqGot := messages.deleteHistoryReq
	if reqGot.OwnerUserID != userID || reqGot.Peer.ID != peerID || reqGot.MaxID != 15 || !reqGot.JustClear || !reqGot.Revoke || reqGot.OriginSessionID != 88 || reqGot.OriginAuthKeyID != authKeyID {
		t.Fatalf("delete history request = %+v, want owner peer max_id flags current session", reqGot)
	}
}

type staticUsersService struct {
	user domain.User
}

type mapUsersService struct {
	users map[int64]domain.User
}

type captureAuthService struct {
	resolvedAuthKeyID  [8]byte
	hasResolved        bool
	resolveCount       int
	userID             int64
	userIDCount        int
	signInUser         domain.User
	loggedOutAuthKeyID [8]byte
}

type blockingUserAuthService struct {
	userID  int64
	started chan struct{}
	release chan struct{}
	once    sync.Once
	mu      sync.Mutex
	count   int
}

func newBlockingUserAuthService(userID int64) *blockingUserAuthService {
	return &blockingUserAuthService{
		userID:  userID,
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
}

func (s *blockingUserAuthService) UserIDCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.count
}

func (s *blockingUserAuthService) BindTempAuthKey(context.Context, int64, domain.TempAuthKeyBinding) error {
	return nil
}

func (s *blockingUserAuthService) ResolveAuthKey(context.Context, [8]byte) ([8]byte, bool, error) {
	return [8]byte{}, false, nil
}

func (s *blockingUserAuthService) UserID(ctx context.Context, _ [8]byte) (int64, bool, error) {
	s.mu.Lock()
	s.count++
	s.mu.Unlock()
	s.once.Do(func() { close(s.started) })
	select {
	case <-s.release:
		return s.userID, s.userID != 0, nil
	case <-ctx.Done():
		return 0, false, ctx.Err()
	}
}

func (s *blockingUserAuthService) SendCode(context.Context, string) (string, error) {
	return "", nil
}

func (s *blockingUserAuthService) ResendCode(context.Context, string, string) (string, error) {
	return "", nil
}

func (s *blockingUserAuthService) CancelCode(context.Context, string, string) error {
	return nil
}

func (s *blockingUserAuthService) SignIn(context.Context, domain.Authorization, string, string, string) (domain.User, domain.Message, bool, error) {
	return domain.User{}, domain.Message{}, false, nil
}

func (s *blockingUserAuthService) SignUp(context.Context, domain.Authorization, string, string, string, string) (domain.User, domain.Message, error) {
	return domain.User{}, domain.Message{}, nil
}

func (s *blockingUserAuthService) LogOut(context.Context, [8]byte) error {
	return nil
}

func (s *blockingUserAuthService) ListAuthorizations(context.Context, int64) ([]domain.Authorization, error) {
	return nil, nil
}

func (s *blockingUserAuthService) ResetAuthorization(context.Context, int64, int64) (domain.Authorization, bool, error) {
	return domain.Authorization{}, false, nil
}

func (s *blockingUserAuthService) ResetAuthorizations(context.Context, int64, [8]byte) ([]domain.Authorization, error) {
	return nil, nil
}

func (s *captureAuthService) BindTempAuthKey(context.Context, int64, domain.TempAuthKeyBinding) error {
	return nil
}

func (s *captureAuthService) ResolveAuthKey(context.Context, [8]byte) ([8]byte, bool, error) {
	s.resolveCount++
	return s.resolvedAuthKeyID, s.hasResolved, nil
}

func (s *captureAuthService) UserID(context.Context, [8]byte) (int64, bool, error) {
	s.userIDCount++
	return s.userID, s.userID != 0, nil
}

func (s *captureAuthService) SendCode(context.Context, string) (string, error) {
	return "", nil
}

func (s *captureAuthService) ResendCode(context.Context, string, string) (string, error) {
	return "", nil
}

func (s *captureAuthService) CancelCode(context.Context, string, string) error {
	return nil
}

func (s *captureAuthService) SignIn(context.Context, domain.Authorization, string, string, string) (domain.User, domain.Message, bool, error) {
	if s.signInUser.ID != 0 {
		return s.signInUser, domain.Message{}, false, nil
	}
	return domain.User{}, domain.Message{}, false, nil
}

func (s *captureAuthService) SignUp(context.Context, domain.Authorization, string, string, string, string) (domain.User, domain.Message, error) {
	return domain.User{}, domain.Message{}, nil
}

func (s *captureAuthService) LogOut(_ context.Context, authKeyID [8]byte) error {
	s.loggedOutAuthKeyID = authKeyID
	return nil
}

func (s *captureAuthService) ListAuthorizations(context.Context, int64) ([]domain.Authorization, error) {
	return nil, nil
}

func (s *captureAuthService) ResetAuthorization(context.Context, int64, int64) (domain.Authorization, bool, error) {
	return domain.Authorization{}, false, nil
}

func (s *captureAuthService) ResetAuthorizations(context.Context, int64, [8]byte) ([]domain.Authorization, error) {
	return nil, nil
}

func (s staticUsersService) Self(context.Context, int64) (domain.User, error) {
	return s.user, nil
}

func (s staticUsersService) ByID(_ context.Context, _, userID int64) (domain.User, bool, error) {
	if userID == s.user.ID {
		return s.user, true, nil
	}
	if userID == domain.OfficialSystemUserID {
		return domain.OfficialSystemUser(), true, nil
	}
	return domain.User{}, false, nil
}

func (s staticUsersService) ByIDs(_ context.Context, _ int64, userIDs []int64) ([]domain.User, error) {
	out := make([]domain.User, 0, len(userIDs))
	seen := map[int64]struct{}{}
	for _, userID := range userIDs {
		if _, ok := seen[userID]; ok {
			continue
		}
		seen[userID] = struct{}{}
		if userID == s.user.ID {
			out = append(out, s.user)
		} else if userID == domain.OfficialSystemUserID {
			out = append(out, domain.OfficialSystemUser())
		}
	}
	return out, nil
}

func (s mapUsersService) Self(_ context.Context, userID int64) (domain.User, error) {
	return s.users[userID], nil
}

func (s mapUsersService) ByID(_ context.Context, _, userID int64) (domain.User, bool, error) {
	u, ok := s.users[userID]
	return u, ok, nil
}

func (s mapUsersService) ByIDs(_ context.Context, _ int64, userIDs []int64) ([]domain.User, error) {
	out := make([]domain.User, 0, len(userIDs))
	seen := map[int64]struct{}{}
	for _, userID := range userIDs {
		if _, ok := seen[userID]; ok {
			continue
		}
		seen[userID] = struct{}{}
		if u, ok := s.users[userID]; ok {
			out = append(out, u)
		}
	}
	return out, nil
}

type captureUsersService struct {
	user   domain.User
	userID int64
}

func (s *captureUsersService) Self(_ context.Context, userID int64) (domain.User, error) {
	s.userID = userID
	return s.user, nil
}

func (s *captureUsersService) ByID(_ context.Context, currentUserID, userID int64) (domain.User, bool, error) {
	s.userID = currentUserID
	if userID == s.user.ID {
		return s.user, true, nil
	}
	return domain.User{}, false, nil
}

func (s *captureUsersService) ByIDs(_ context.Context, currentUserID int64, userIDs []int64) ([]domain.User, error) {
	s.userID = currentUserID
	out := make([]domain.User, 0, len(userIDs))
	for _, userID := range userIDs {
		if userID == s.user.ID {
			out = append(out, s.user)
		}
	}
	return out, nil
}

type captureSessions struct {
	mu              sync.Mutex
	sessionID       int64
	userID          int64
	userResolved    bool
	authKeyID       [8]byte
	authKeyResolved bool
	receives        bool
	messageType     proto.MessageType
	message         bin.Encoder
	pushUserIDs     []int64
	onlineUserIDs   []int64
	channelViewers  map[int64][]int64
	channelMembers  map[int64][]int64
}

type captureSessionsSnapshot struct {
	sessionID       int64
	userID          int64
	userResolved    bool
	authKeyID       [8]byte
	authKeyResolved bool
	receives        bool
	messageType     proto.MessageType
	message         bin.Encoder
}

func (s *captureSessions) snapshot() captureSessionsSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return captureSessionsSnapshot{
		sessionID:       s.sessionID,
		userID:          s.userID,
		userResolved:    s.userResolved,
		authKeyID:       s.authKeyID,
		authKeyResolved: s.authKeyResolved,
		receives:        s.receives,
		messageType:     s.messageType,
		message:         s.message,
	}
}

func (s *captureSessions) pushedUserIDs() []int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]int64(nil), s.pushUserIDs...)
}

func (s *captureSessions) BindAuthKey(sessionID int64, authKeyID [8]byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.authKeyResolved && s.authKeyID != authKeyID {
		s.userID = 0
		s.userResolved = false
	}
	s.sessionID = sessionID
	s.authKeyID = authKeyID
	s.authKeyResolved = true
}

func (s *captureSessions) AuthKeyID(int64) ([8]byte, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.authKeyID, s.authKeyResolved
}

func (s *captureSessions) BindUser(sessionID, userID int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessionID = sessionID
	s.userID = userID
	s.userResolved = true
}

func (s *captureSessions) UserID(int64) (int64, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.userID, s.userID != 0
}

func (s *captureSessions) UserIDResolved(int64) (int64, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.userID, s.userResolved
}

func (s *captureSessions) UnbindAuthKey(authKeyID [8]byte) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.authKeyID == authKeyID {
		s.userID = 0
		s.userResolved = true
		return 1
	}
	return 0
}

func (s *captureSessions) SetReceivesUpdates(sessionID int64, receives bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessionID = sessionID
	s.receives = receives
}

func (s *captureSessions) PushToSession(_ context.Context, sessionID int64, t proto.MessageType, msg bin.Encoder) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessionID = sessionID
	s.messageType = t
	s.message = msg
	return nil
}

func (s *captureSessions) PushToUserExceptSession(_ context.Context, userID, excludeSessionID int64, t proto.MessageType, msg bin.Encoder) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.userID = userID
	s.sessionID = excludeSessionID
	s.messageType = t
	s.message = msg
	s.pushUserIDs = append(s.pushUserIDs, userID)
	return 1, nil
}

func (s *captureSessions) OnlineUserIDs(limit int) []int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := append([]int64(nil), s.onlineUserIDs...)
	if limit > 0 && len(ids) > limit {
		ids = ids[:limit]
	}
	return ids
}

func (s *captureSessions) IsUserOnline(userID int64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, id := range s.onlineUserIDs {
		if id == userID {
			return true
		}
	}
	return false
}

func (s *captureSessions) OnlineUserIDsForCandidates(candidateUserIDs []int64, limit int) []int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	online := make(map[int64]struct{}, len(s.onlineUserIDs))
	for _, id := range s.onlineUserIDs {
		online[id] = struct{}{}
	}
	out := make([]int64, 0, len(candidateUserIDs))
	seen := map[int64]struct{}{}
	for _, id := range candidateUserIDs {
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		if _, ok := online[id]; !ok {
			continue
		}
		out = append(out, id)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out
}

func (s *captureSessions) TrackChannelInterest(_ [8]byte, _ int64, userID int64, channelIDs []int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.channelViewers == nil {
		s.channelViewers = make(map[int64][]int64)
	}
	for channelID, viewers := range s.channelViewers {
		out := viewers[:0]
		for _, viewerID := range viewers {
			if viewerID != userID {
				out = append(out, viewerID)
			}
		}
		if len(out) == 0 {
			delete(s.channelViewers, channelID)
			continue
		}
		s.channelViewers[channelID] = out
	}
	for _, channelID := range channelIDs {
		if channelID == 0 {
			continue
		}
		s.channelViewers[channelID] = append(s.channelViewers[channelID], userID)
	}
}

func (s *captureSessions) ClearChannelInterest(_ [8]byte, _ int64, userID int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for channelID, viewers := range s.channelViewers {
		out := viewers[:0]
		for _, viewerID := range viewers {
			if viewerID != userID {
				out = append(out, viewerID)
			}
		}
		if len(out) == 0 {
			delete(s.channelViewers, channelID)
			continue
		}
		s.channelViewers[channelID] = out
	}
}

func (s *captureSessions) OnlineChannelUserIDs(channelID int64, limit int) []int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return limitIDs(s.channelViewers[channelID], limit)
}

func (s *captureSessions) SetSessionChannelMemberships(_ [8]byte, _ int64, userID int64, channelIDs []int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.channelMembers == nil {
		s.channelMembers = make(map[int64][]int64)
	}
	for _, channelID := range channelIDs {
		if channelID == 0 {
			continue
		}
		s.channelMembers[channelID] = append(s.channelMembers[channelID], userID)
	}
}

func (s *captureSessions) AddUserChannelMembership(userID, channelID int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.channelMembers == nil {
		s.channelMembers = make(map[int64][]int64)
	}
	s.channelMembers[channelID] = append(s.channelMembers[channelID], userID)
}

func (s *captureSessions) RemoveUserChannelMembership(userID, channelID int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	members := s.channelMembers[channelID]
	out := members[:0]
	for _, id := range members {
		if id != userID {
			out = append(out, id)
		}
	}
	if len(out) == 0 {
		delete(s.channelMembers, channelID)
		return
	}
	s.channelMembers[channelID] = out
}

func (s *captureSessions) OnlineChannelMemberUserIDs(channelID int64, limit int) []int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return limitIDs(s.channelMembers[channelID], limit)
}

func limitIDs(ids []int64, limit int) []int64 {
	out := make([]int64, 0, len(ids))
	seen := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		if id == 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out
}

type captureUpdates struct {
	state            domain.UpdateState
	currentState     *domain.UpdateState
	authKeyID        [8]byte
	userID           int64
	clearedAuthKeyID [8]byte
	cleared          bool
	date             int
	events           []domain.UpdateEvent
	excludeSessionID int64
	reliableDispatch bool
}

func (s *captureUpdates) UsesReliableDispatch() bool {
	return s.reliableDispatch
}

func (s *captureUpdates) GetState(_ context.Context, authKeyID [8]byte, userID int64) (domain.UpdateState, error) {
	s.authKeyID = authKeyID
	s.userID = userID
	return s.state, nil
}

func (s *captureUpdates) CurrentState(_ context.Context, userID int64) (domain.UpdateState, error) {
	s.userID = userID
	if s.currentState != nil {
		return *s.currentState, nil
	}
	return s.state, nil
}

func (s *captureUpdates) GetDifference(_ context.Context, authKeyID [8]byte, userID int64, _ domain.UpdateState) (domain.UpdateDifference, error) {
	s.authKeyID = authKeyID
	s.userID = userID
	return domain.UpdateDifference{State: s.state}, nil
}

func (s *captureUpdates) ClearAuthKey(_ context.Context, authKeyID [8]byte) error {
	s.clearedAuthKeyID = authKeyID
	s.cleared = true
	return nil
}

func (s *captureUpdates) RecordNewMessage(_ context.Context, authKeyID [8]byte, userID int64, msg domain.Message) (domain.UpdateEvent, domain.UpdateState, error) {
	s.authKeyID = authKeyID
	s.userID = userID
	s.date = msg.Date
	event := domain.UpdateEvent{Type: domain.UpdateEventNewMessage, Pts: s.state.Pts, PtsCount: 1, Date: msg.Date, Message: msg}
	s.events = append(s.events, event)
	return event, s.state, nil
}

func (s *captureUpdates) RecordReadHistory(_ context.Context, authKeyID [8]byte, userID int64, read domain.ReadHistoryResult, excludeSessionID int64) (domain.UpdateEvent, domain.UpdateState, error) {
	s.authKeyID = authKeyID
	s.userID = userID
	s.excludeSessionID = excludeSessionID
	event := domain.UpdateEvent{
		Type:             domain.UpdateEventReadHistoryInbox,
		Pts:              s.state.Pts,
		PtsCount:         1,
		Date:             s.state.Date,
		Peer:             read.Peer,
		MaxID:            read.MaxID,
		StillUnreadCount: read.StillUnreadCount,
	}
	s.events = append(s.events, event)
	return event, s.state, nil
}

func (s *captureUpdates) RecordContactsReset(_ context.Context, authKeyID [8]byte, userID int64, excludeSessionID int64) (domain.UpdateEvent, domain.UpdateState, error) {
	s.excludeSessionID = excludeSessionID
	return s.recordCapturedEvent(authKeyID, userID, domain.UpdateEvent{Type: domain.UpdateEventContactsReset})
}

func (s *captureUpdates) RecordDialogPinned(_ context.Context, authKeyID [8]byte, userID int64, peer domain.Peer, pinned bool, excludeSessionID int64) (domain.UpdateEvent, domain.UpdateState, error) {
	s.excludeSessionID = excludeSessionID
	return s.recordCapturedEvent(authKeyID, userID, domain.UpdateEvent{Type: domain.UpdateEventDialogPinned, Peer: peer, Bool: pinned})
}

func (s *captureUpdates) RecordPinnedDialogs(_ context.Context, authKeyID [8]byte, userID int64, order []domain.Peer, excludeSessionID int64) (domain.UpdateEvent, domain.UpdateState, error) {
	s.excludeSessionID = excludeSessionID
	return s.recordCapturedEvent(authKeyID, userID, domain.UpdateEvent{Type: domain.UpdateEventPinnedDialogs, Peers: append([]domain.Peer(nil), order...)})
}

func (s *captureUpdates) RecordDialogUnreadMark(_ context.Context, authKeyID [8]byte, userID int64, peer domain.Peer, unread bool, excludeSessionID int64) (domain.UpdateEvent, domain.UpdateState, error) {
	s.excludeSessionID = excludeSessionID
	return s.recordCapturedEvent(authKeyID, userID, domain.UpdateEvent{Type: domain.UpdateEventDialogUnreadMark, Peer: peer, Bool: unread})
}

func (s *captureUpdates) RecordPeerSettings(_ context.Context, authKeyID [8]byte, userID int64, peer domain.Peer, settings domain.PeerSettings, excludeSessionID int64) (domain.UpdateEvent, domain.UpdateState, error) {
	s.excludeSessionID = excludeSessionID
	return s.recordCapturedEvent(authKeyID, userID, domain.UpdateEvent{Type: domain.UpdateEventPeerSettings, Peer: peer, Settings: settings})
}

func (s *captureUpdates) RecordDialogFilter(_ context.Context, authKeyID [8]byte, userID int64, folderID int, folder *domain.DialogFolder, excludeSessionID int64) (domain.UpdateEvent, domain.UpdateState, error) {
	s.excludeSessionID = excludeSessionID
	return s.recordCapturedEvent(authKeyID, userID, domain.UpdateEvent{Type: domain.UpdateEventDialogFilter, FilterID: folderID, DialogFilter: folder})
}

func (s *captureUpdates) RecordDialogFilterOrder(_ context.Context, authKeyID [8]byte, userID int64, order []int, excludeSessionID int64) (domain.UpdateEvent, domain.UpdateState, error) {
	s.excludeSessionID = excludeSessionID
	return s.recordCapturedEvent(authKeyID, userID, domain.UpdateEvent{Type: domain.UpdateEventDialogFilterOrder, FilterOrder: append([]int(nil), order...)})
}

func (s *captureUpdates) RecordDialogFiltersReload(_ context.Context, authKeyID [8]byte, userID int64, excludeSessionID int64) (domain.UpdateEvent, domain.UpdateState, error) {
	s.excludeSessionID = excludeSessionID
	return s.recordCapturedEvent(authKeyID, userID, domain.UpdateEvent{Type: domain.UpdateEventDialogFilters})
}

func (s *captureUpdates) RecordFolderPeers(_ context.Context, authKeyID [8]byte, userID int64, peers []domain.FolderPeerUpdate, excludeSessionID int64) (domain.UpdateEvent, domain.UpdateState, error) {
	s.excludeSessionID = excludeSessionID
	return s.recordCapturedEvent(authKeyID, userID, domain.UpdateEvent{Type: domain.UpdateEventFolderPeers, FolderPeers: append([]domain.FolderPeerUpdate(nil), peers...)})
}

func (s *captureUpdates) RecordChannelAvailableMessages(_ context.Context, authKeyID [8]byte, userID, channelID int64, availableMinID int, excludeSessionID int64) (domain.UpdateEvent, domain.UpdateState, error) {
	s.excludeSessionID = excludeSessionID
	return s.recordCapturedEvent(authKeyID, userID, domain.UpdateEvent{
		Type:  domain.UpdateEventChannelAvailable,
		Peer:  domain.Peer{Type: domain.PeerTypeChannel, ID: channelID},
		MaxID: availableMinID,
	})
}

func (s *captureUpdates) RecordChannelViewForumAsMessages(_ context.Context, authKeyID [8]byte, userID, channelID int64, enabled bool, excludeSessionID int64) (domain.UpdateEvent, domain.UpdateState, error) {
	s.excludeSessionID = excludeSessionID
	return s.recordCapturedEvent(authKeyID, userID, domain.UpdateEvent{
		Type: domain.UpdateEventChannelViewForum,
		Peer: domain.Peer{Type: domain.PeerTypeChannel, ID: channelID},
		Bool: enabled,
	})
}

func (s *captureUpdates) recordCapturedEvent(authKeyID [8]byte, userID int64, event domain.UpdateEvent) (domain.UpdateEvent, domain.UpdateState, error) {
	s.authKeyID = authKeyID
	s.userID = userID
	if event.Pts == 0 {
		event.Pts = s.state.Pts
	}
	if event.PtsCount == 0 {
		event.PtsCount = 1
	}
	if event.Date == 0 {
		event.Date = s.state.Date
	}
	event.UserID = userID
	s.events = append(s.events, event)
	return event, s.state, nil
}

type captureMessages struct {
	list              domain.MessageList
	filter            domain.MessageFilter
	sendResult        domain.SendPrivateTextResult
	sendUserID        int64
	sendReq           domain.SendPrivateTextRequest
	forwardUserID     int64
	forwardReq        domain.ForwardPrivateMessagesRequest
	forwardRes        domain.ForwardPrivateMessagesResult
	readResult        domain.ReadHistoryResult
	readReq           domain.ReadHistoryRequest
	readPeer          domain.Peer
	readMaxID         int
	readContentsReq   domain.ReadMessageContentsRequest
	readContentsRes   domain.ReadMessageContentsResult
	setReactionReq    domain.SetPrivateMessageReactionsRequest
	setReactionRes    domain.PrivateMessageReactionsResult
	getReactionReq    domain.PrivateMessageReactionsRequest
	getReactionRes    domain.PrivateMessageReactionsResult
	editReq           domain.EditMessageRequest
	editRes           domain.EditMessageResult
	outboxReadDateReq domain.OutboxReadDateRequest
	outboxReadDate    int
	deleteMessagesReq domain.DeleteMessagesRequest
	deleteMessagesRes domain.DeleteMessagesResult
	deleteHistoryReq  domain.DeleteHistoryRequest
	deleteHistoryRes  domain.DeleteMessagesResult
}

func (s *captureMessages) SendPrivateText(_ context.Context, userID int64, req domain.SendPrivateTextRequest) (domain.SendPrivateTextResult, error) {
	s.sendUserID = userID
	s.sendReq = req
	if s.sendResult.SenderMessage.ID == 0 {
		s.sendResult.SenderMessage = domain.Message{
			ID:          1,
			OwnerUserID: req.SenderUserID,
			RandomID:    req.RandomID,
			Peer:        domain.Peer{Type: domain.PeerTypeUser, ID: req.RecipientUserID},
			From:        domain.Peer{Type: domain.PeerTypeUser, ID: req.SenderUserID},
			Date:        req.Date,
			Out:         true,
			Silent:      req.Silent,
			NoForwards:  req.NoForwards,
			Body:        req.Message,
			Entities:    req.Entities,
			ReplyTo:     req.ReplyTo,
			Forward:     req.Forward,
			Pts:         1,
		}
		s.sendResult.SenderEvent = domain.UpdateEvent{
			UserID:   req.SenderUserID,
			Type:     domain.UpdateEventNewMessage,
			Pts:      1,
			PtsCount: 1,
			Date:     req.Date,
			Message:  s.sendResult.SenderMessage,
		}
	}
	return s.sendResult, nil
}

func (s *captureMessages) ForwardPrivateMessages(_ context.Context, userID int64, req domain.ForwardPrivateMessagesRequest) (domain.ForwardPrivateMessagesResult, error) {
	s.forwardUserID = userID
	s.forwardReq = req
	if len(s.forwardRes.SenderMessages) == 0 {
		s.forwardRes.OwnerUserID = userID
		s.forwardRes.SenderMessages = make([]domain.Message, 0, len(req.MessageIDs))
		s.forwardRes.SenderEvents = make([]domain.UpdateEvent, 0, len(req.MessageIDs))
		for i := range req.MessageIDs {
			msg := domain.Message{
				ID:          i + 1,
				OwnerUserID: req.OwnerUserID,
				RandomID:    req.RandomIDs[i],
				Peer:        domain.Peer{Type: domain.PeerTypeUser, ID: req.ToUserID},
				From:        domain.Peer{Type: domain.PeerTypeUser, ID: req.OwnerUserID},
				Date:        req.Date,
				Out:         true,
				Silent:      req.Silent,
				NoForwards:  req.NoForwards,
				Body:        "forwarded",
				ReplyTo:     req.ReplyTo,
				Forward:     &domain.MessageForward{From: domain.Peer{Type: domain.PeerTypeUser, ID: req.FromPeer.ID}, Date: req.Date - 1},
				Pts:         i + 1,
			}
			event := domain.UpdateEvent{
				UserID:   req.OwnerUserID,
				Type:     domain.UpdateEventNewMessage,
				Pts:      msg.Pts,
				PtsCount: 1,
				Date:     req.Date,
				Message:  msg,
			}
			s.forwardRes.SenderMessages = append(s.forwardRes.SenderMessages, msg)
			s.forwardRes.SenderEvents = append(s.forwardRes.SenderEvents, event)
		}
	}
	return s.forwardRes, nil
}

func (s *captureMessages) GetMessages(_ context.Context, _ int64, ids []int) (domain.MessageList, error) {
	byID := make(map[int]domain.Message, len(s.list.Messages))
	for _, msg := range s.list.Messages {
		byID[msg.ID] = msg
	}
	out := domain.MessageList{Messages: make([]domain.Message, 0, len(ids)), Users: s.list.Users}
	for _, id := range ids {
		if msg, ok := byID[id]; ok {
			out.Messages = append(out.Messages, msg)
		}
	}
	return out, nil
}

func (s *captureMessages) GetHistory(_ context.Context, _ int64, filter domain.MessageFilter) (domain.MessageList, error) {
	s.filter = filter
	return s.list, nil
}

func (s *captureMessages) Search(_ context.Context, _ int64, filter domain.MessageFilter) (domain.MessageList, error) {
	s.filter = filter
	return s.list, nil
}

func (s *captureMessages) ReadHistory(_ context.Context, _ int64, req domain.ReadHistoryRequest) (domain.ReadHistoryResult, error) {
	s.readReq = req
	s.readPeer = req.Peer
	s.readMaxID = req.MaxID
	if s.readResult.OwnerUserID == 0 {
		s.readResult.OwnerUserID = req.OwnerUserID
	}
	if s.readResult.Peer.ID == 0 {
		s.readResult.Peer = req.Peer
	}
	if s.readResult.MaxID == 0 {
		s.readResult.MaxID = req.MaxID
	}
	return s.readResult, nil
}

func (s *captureMessages) ReadMessageContents(_ context.Context, userID int64, req domain.ReadMessageContentsRequest) (domain.ReadMessageContentsResult, error) {
	s.readContentsReq = req
	if s.readContentsRes.OwnerUserID == 0 {
		s.readContentsRes.OwnerUserID = userID
	}
	return s.readContentsRes, nil
}

func (s *captureMessages) GetOutboxReadDate(_ context.Context, _ int64, req domain.OutboxReadDateRequest) (int, error) {
	s.outboxReadDateReq = req
	if s.outboxReadDate == 0 {
		return 0, domain.ErrMessageNotReadYet
	}
	return s.outboxReadDate, nil
}

func (s *captureMessages) SetMessageReactions(_ context.Context, userID int64, req domain.SetPrivateMessageReactionsRequest) (domain.PrivateMessageReactionsResult, error) {
	s.setReactionReq = req
	if len(s.setReactionRes.Messages) == 0 {
		if len(req.Reactions) == 0 {
			reactions := domain.ChannelMessageReactions{CanSeeList: true, Results: []domain.ChannelMessageReactionCount{}, Recent: []domain.ChannelMessagePeerReaction{}}
			s.setReactionRes = domain.PrivateMessageReactionsResult{
				Messages: []domain.Message{{
					ID:          req.MessageID,
					OwnerUserID: userID,
					Peer:        req.Peer,
					From:        domain.Peer{Type: domain.PeerTypeUser, ID: req.Peer.ID},
					Date:        req.Date,
					Reactions:   &reactions,
				}},
				Reactions: reactions,
			}
			return s.setReactionRes, nil
		}
		reactions := domain.ChannelMessageReactions{
			CanSeeList: true,
			Results: []domain.ChannelMessageReactionCount{{
				Reaction:    req.Reactions[0],
				Count:       1,
				ChosenOrder: 1,
			}},
			Recent: []domain.ChannelMessagePeerReaction{{
				UserID:      userID,
				Reaction:    req.Reactions[0],
				My:          true,
				Big:         req.Big,
				ChosenOrder: 1,
				Date:        req.Date,
			}},
		}
		s.setReactionRes = domain.PrivateMessageReactionsResult{
			Messages: []domain.Message{{
				ID:          req.MessageID,
				OwnerUserID: userID,
				Peer:        req.Peer,
				From:        domain.Peer{Type: domain.PeerTypeUser, ID: req.Peer.ID},
				Date:        req.Date,
				Reactions:   &reactions,
			}},
			Reactions: reactions,
		}
	}
	return s.setReactionRes, nil
}

func (s *captureMessages) GetMessageReactions(_ context.Context, userID int64, req domain.PrivateMessageReactionsRequest) (domain.PrivateMessageReactionsResult, error) {
	s.getReactionReq = req
	if len(s.getReactionRes.Messages) == 0 && len(req.IDs) > 0 {
		reactions := domain.ChannelMessageReactions{CanSeeList: true, Results: []domain.ChannelMessageReactionCount{}, Recent: []domain.ChannelMessagePeerReaction{}}
		s.getReactionRes = domain.PrivateMessageReactionsResult{
			Messages: []domain.Message{{
				ID:          req.IDs[0],
				OwnerUserID: userID,
				Peer:        req.Peer,
				From:        domain.Peer{Type: domain.PeerTypeUser, ID: req.Peer.ID},
				Reactions:   &reactions,
			}},
			Reactions: reactions,
		}
	}
	return s.getReactionRes, nil
}

func (s *captureMessages) EditMessage(_ context.Context, userID int64, req domain.EditMessageRequest) (domain.EditMessageResult, error) {
	s.editReq = req
	if s.editRes.OwnerUserID == 0 {
		s.editRes.OwnerUserID = userID
	}
	if len(s.editRes.Edited) == 0 {
		msg := domain.Message{
			ID:          req.ID,
			OwnerUserID: req.OwnerUserID,
			Peer:        req.Peer,
			From:        domain.Peer{Type: domain.PeerTypeUser, ID: req.OwnerUserID},
			Date:        req.EditDate - 10,
			EditDate:    req.EditDate,
			Out:         true,
			Body:        req.Message,
			Entities:    append([]domain.MessageEntity(nil), req.Entities...),
			Pts:         7,
		}
		s.editRes.Edited = []domain.EditedMessageForUser{{
			UserID:  req.OwnerUserID,
			Message: msg,
			Event: domain.UpdateEvent{
				UserID:   req.OwnerUserID,
				Type:     domain.UpdateEventEditMessage,
				Pts:      7,
				PtsCount: 1,
				Date:     req.EditDate,
				Message:  msg,
			},
		}}
	}
	return s.editRes, nil
}

func (s *captureMessages) DeleteMessages(_ context.Context, userID int64, req domain.DeleteMessagesRequest) (domain.DeleteMessagesResult, error) {
	s.deleteMessagesReq = req
	if s.deleteMessagesRes.OwnerUserID == 0 {
		s.deleteMessagesRes.OwnerUserID = userID
	}
	return s.deleteMessagesRes, nil
}

func (s *captureMessages) DeleteHistory(_ context.Context, userID int64, req domain.DeleteHistoryRequest) (domain.DeleteMessagesResult, error) {
	s.deleteHistoryReq = req
	if s.deleteHistoryRes.OwnerUserID == 0 {
		s.deleteHistoryRes.OwnerUserID = userID
	}
	return s.deleteHistoryRes, nil
}

type captureDialogs struct {
	list            domain.DialogList
	peerList        domain.DialogList
	folderList      domain.DialogFolderList
	filter          domain.DialogFilter
	peers           []domain.Peer
	savedFolder     domain.DialogFolder
	deletedFolderID int
	folderOrder     []int
	tagsEnabled     bool
	folderPeers     []domain.FolderPeerUpdate
	drafts          []domain.DialogDraft
	savedDraft      domain.DialogDraft
	deletedDraft    struct {
		peer         domain.Peer
		topMessageID int
	}
}

type captureRPCMetrics struct {
	messageSend    int
	messageDup     bool
	messageSendErr error
	rateLimited    int
}

func (m *captureRPCMetrics) MessageSend(_ time.Duration, duplicate bool, err error) {
	m.messageSend++
	m.messageDup = duplicate
	m.messageSendErr = err
}

func (m *captureRPCMetrics) MessageRateLimited(retryAfterSeconds int) {
	m.rateLimited = retryAfterSeconds
}

func (m *captureRPCMetrics) OutboxClaimed(int) {}

func (m *captureRPCMetrics) OutboxDelivered(time.Duration) {}

func (m *captureRPCMetrics) OutboxFailed(error) {}

func (s *captureDialogs) GetDialogs(_ context.Context, _ int64, filter domain.DialogFilter) (domain.DialogList, error) {
	s.filter = filter
	return s.list, nil
}

func (s *captureDialogs) GetPeerDialogs(_ context.Context, _ int64, peers []domain.Peer) (domain.DialogList, error) {
	s.peers = append([]domain.Peer(nil), peers...)
	return s.peerList, nil
}

func (s *captureDialogs) SaveDraft(_ context.Context, _ int64, draft domain.DialogDraft) error {
	s.savedDraft = draft
	return nil
}

func (s *captureDialogs) DeleteDraft(_ context.Context, _ int64, peer domain.Peer, topMessageID int) (bool, error) {
	s.deletedDraft.peer = peer
	s.deletedDraft.topMessageID = topMessageID
	return true, nil
}

func (s *captureDialogs) ListDrafts(_ context.Context, _ int64, _ int) ([]domain.DialogDraft, error) {
	return append([]domain.DialogDraft(nil), s.drafts...), nil
}

func (s *captureDialogs) ClearDrafts(_ context.Context, _ int64, _ int) ([]domain.DialogDraft, error) {
	drafts := append([]domain.DialogDraft(nil), s.drafts...)
	s.drafts = nil
	return drafts, nil
}

func (s *captureDialogs) TogglePinned(_ context.Context, _ int64, peer domain.Peer, _ bool) (bool, error) {
	s.peers = []domain.Peer{peer}
	return true, nil
}

func (s *captureDialogs) ReorderPinned(_ context.Context, _ int64, order []domain.Peer, _ bool) error {
	s.peers = append([]domain.Peer(nil), order...)
	return nil
}

func (s *captureDialogs) MarkUnread(_ context.Context, _ int64, peer domain.Peer, _ bool) (bool, error) {
	s.peers = []domain.Peer{peer}
	return true, nil
}

func (s *captureDialogs) UnreadMarks(_ context.Context, _ int64) ([]domain.Peer, error) {
	return s.peers, nil
}

func (s *captureDialogs) HidePeerSettingsBar(_ context.Context, _ int64, peer domain.Peer) (bool, error) {
	s.peers = []domain.Peer{peer}
	return true, nil
}

func (s *captureDialogs) PeerSettingsBarHidden(_ context.Context, _ int64, _ domain.Peer) (bool, error) {
	return false, nil
}

func (s *captureDialogs) GetDialogFolders(_ context.Context, _ int64) (domain.DialogFolderList, error) {
	return s.folderList, nil
}

func (s *captureDialogs) SaveDialogFolder(_ context.Context, _ int64, folder domain.DialogFolder) error {
	s.savedFolder = folder
	return nil
}

func (s *captureDialogs) DeleteDialogFolder(_ context.Context, _ int64, folderID int) error {
	s.deletedFolderID = folderID
	return nil
}

func (s *captureDialogs) ReorderDialogFolders(_ context.Context, _ int64, order []int) error {
	s.folderOrder = append([]int(nil), order...)
	return nil
}

func (s *captureDialogs) ToggleDialogFolderTags(_ context.Context, _ int64, enabled bool) error {
	s.tagsEnabled = enabled
	return nil
}

func (s *captureDialogs) EditPeerFolders(_ context.Context, _ int64, peers []domain.FolderPeerUpdate) error {
	s.folderPeers = append([]domain.FolderPeerUpdate(nil), peers...)
	return nil
}
