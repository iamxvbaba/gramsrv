package rpc

import (
	"context"
	"encoding/binary"
	"github.com/gotd/td/bin"
	"github.com/gotd/td/clock"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest"
	"go.uber.org/zap/zaptest/observer"
	"reflect"
	"sync"
	appauth "telesrv/internal/app/auth"
	appdialogs "telesrv/internal/app/dialogs"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
	"testing"
	"time"
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
	if len(cfg.DCOptions) != 1 {
		t.Fatalf("DCOptions = %+v, want one advertised DC", cfg.DCOptions)
	}
	dcOption := cfg.DCOptions[0]
	if dcOption.ID != dc || dcOption.IPAddress != "127.0.0.1" || dcOption.Port != 2398 || !dcOption.TCPObfuscatedOnly || !dcOption.Static || !dcOption.ThisPortOnly {
		t.Fatalf("DCOptions[0] = %+v, want static tcpo 127.0.0.1:2398 for dc%d", dcOption, dc)
	}
}

func TestDispatchRejectsAuthorizedRPCBeforeLogin(t *testing.T) {
	r := New(Config{DC: 2, IP: "127.0.0.1", Port: 2398}, Deps{
		Auth: &captureAuthService{},
	}, zaptest.NewLogger(t), clock.System)
	authKeyID := [8]byte{0x68, 0xa7, 0x30, 0x65, 0xc1, 0x11, 0x6e, 0x35}

	var dialogs bin.Buffer
	if err := (&tg.MessagesGetDialogsRequest{
		OffsetPeer: &tg.InputPeerEmpty{},
		Limit:      20,
	}).Encode(&dialogs); err != nil {
		t.Fatalf("encode messages.getDialogs: %v", err)
	}
	if _, err := r.Dispatch(context.Background(), authKeyID, 1, &dialogs); !tgerr.Is(err, "AUTH_KEY_UNREGISTERED") {
		t.Fatalf("messages.getDialogs err = %v, want AUTH_KEY_UNREGISTERED", err)
	}

	var help bin.Buffer
	if err := (&tg.HelpGetConfigRequest{}).Encode(&help); err != nil {
		t.Fatalf("encode help.getConfig: %v", err)
	}
	if _, err := r.Dispatch(context.Background(), authKeyID, 1, &help); err != nil {
		t.Fatalf("help.getConfig should be allowed before login: %v", err)
	}

	var sendCode bin.Buffer
	if err := (&tg.AuthSendCodeRequest{
		PhoneNumber: "+15550001111",
		APIID:       2040,
		APIHash:     "test",
		Settings:    tg.CodeSettings{},
	}).Encode(&sendCode); err != nil {
		t.Fatalf("encode auth.sendCode: %v", err)
	}
	if _, err := r.Dispatch(context.Background(), authKeyID, 1, &sendCode); err != nil {
		t.Fatalf("auth.sendCode should be allowed before login: %v", err)
	}
}

func TestDispatchRemembersLayerAndClientTypeForSession(t *testing.T) {
	const layer = 225
	core, logs := observer.New(zap.DebugLevel)
	r := New(Config{DC: 2, IP: "127.0.0.1", Port: 2398}, Deps{}, zap.New(core), clock.System)
	rawAuthKeyID := [8]byte{0x22, 0x5}
	sessionID := int64(88)

	initReq := &tg.InvokeWithLayerRequest{
		Layer: layer,
		Query: &tg.InitConnectionRequest{
			APIID:          123,
			DeviceModel:    "Desktop",
			SystemVersion:  "Windows",
			AppVersion:     "6.8.4 x64",
			SystemLangCode: "en",
			LangPack:       "tdesktop",
			LangCode:       "en",
			Query:          &tg.HelpGetConfigRequest{},
		},
	}
	var initBuf bin.Buffer
	if err := initReq.Encode(&initBuf); err != nil {
		t.Fatalf("encode init request: %v", err)
	}
	if _, err := r.Dispatch(context.Background(), rawAuthKeyID, sessionID, &initBuf); err != nil {
		t.Fatalf("dispatch init request: %v", err)
	}

	sessionCtx := WithSessionID(WithRawAuthKeyID(WithAuthKeyID(context.Background(), rawAuthKeyID), rawAuthKeyID), sessionID)
	info, ok := r.clientSessionInfo(sessionCtx)
	if !ok {
		t.Fatalf("session metadata missing")
	}
	if info.layer != layer {
		t.Fatalf("remembered layer = %d, want %d", info.layer, layer)
	}
	if !info.hasClientInfo || info.clientInfo.ClientType() != ClientTypeTDesktop {
		t.Fatalf("remembered client info = %+v, want tdesktop", info.clientInfo)
	}

	var plainBuf bin.Buffer
	if err := (&tg.HelpGetConfigRequest{}).Encode(&plainBuf); err != nil {
		t.Fatalf("encode plain request: %v", err)
	}
	if _, err := r.Dispatch(context.Background(), rawAuthKeyID, sessionID, &plainBuf); err != nil {
		t.Fatalf("dispatch plain request: %v", err)
	}

	entries := logs.FilterMessage("RPC inner handled").All()
	if len(entries) == 0 {
		t.Fatalf("RPC inner handled log missing")
	}
	fields := entries[len(entries)-1].ContextMap()
	if got := intLogField(fields["layer"]); got != layer {
		t.Fatalf("logged layer = %d fields=%v, want %d", got, fields, layer)
	}
	if got := fields["client_type"]; got != string(ClientTypeTDesktop) {
		t.Fatalf("logged client_type = %v, want %s", got, ClientTypeTDesktop)
	}
	if got := fields["app_version"]; got != "6.8.4 x64" {
		t.Fatalf("logged app_version = %v, want 6.8.4 x64", got)
	}

	newSessionID := int64(99)
	var newSessionBuf bin.Buffer
	if err := (&tg.HelpGetConfigRequest{}).Encode(&newSessionBuf); err != nil {
		t.Fatalf("encode new session request: %v", err)
	}
	if _, err := r.Dispatch(context.Background(), rawAuthKeyID, newSessionID, &newSessionBuf); err != nil {
		t.Fatalf("dispatch new session request: %v", err)
	}
	entries = logs.FilterMessage("RPC inner handled").All()
	fields = entries[len(entries)-1].ContextMap()
	if got := intLogField(fields["layer"]); got != layer {
		t.Fatalf("new session logged layer = %d fields=%v, want %d", got, fields, layer)
	}
	if got := fields["client_type"]; got != string(ClientTypeTDesktop) {
		t.Fatalf("new session logged client_type = %v, want %s", got, ClientTypeTDesktop)
	}
	if got := fields["app_version"]; got != "6.8.4 x64" {
		t.Fatalf("new session logged app_version = %v, want 6.8.4 x64", got)
	}
}

func TestAndroidLegacyCompatLogsClientMetadataWithoutInit(t *testing.T) {
	core, logs := observer.New(zap.DebugLevel)
	r := New(Config{DC: 2, IP: "127.0.0.1", Port: 2398}, Deps{}, zap.New(core), clock.System)

	var flags bin.Fields
	flags.Set(0)
	var in bin.Buffer
	in.PutID(0x25939651)
	if err := flags.Encode(&in); err != nil {
		t.Fatalf("encode flags: %v", err)
	}
	in.PutInt(10)
	in.PutInt(100)
	in.PutInt(123456)
	in.PutInt(0)

	rawAuthKeyID := [8]byte{0x68, 0x25}
	sessionID := int64(-3479521421865518854)
	if _, err := r.Dispatch(WithUserID(context.Background(), 1780269504), rawAuthKeyID, sessionID, &in); err != nil {
		t.Fatalf("dispatch legacy updates.getDifference: %v", err)
	}

	// The legacy android constructor is upgraded by layerwire and dispatched
	// normally; client metadata is still applied (withAndroidCompatMetadata for
	// client drift), now surfaced on the standard "RPC inner handled" log.
	entries := logs.FilterMessage("RPC inner handled").All()
	if len(entries) == 0 {
		t.Fatalf("RPC inner handled log missing")
	}
	fields := entries[len(entries)-1].ContextMap()
	if got := intLogField(fields["layer"]); got != currentClientLayer {
		t.Fatalf("logged layer = %d fields=%v, want %d", got, fields, currentClientLayer)
	}
	if got := fields["client_type"]; got != string(ClientTypeAndroid) {
		t.Fatalf("logged client_type = %v, want %s", got, ClientTypeAndroid)
	}
}

// TestNegotiatedLayerStickyContract pins the (layer, ok) contract that keeps the
// edge from clobbering a live connection's layer: unknown ⇒ ok=false (edge keeps
// last-known), a recorded layer ⇒ ok=true, and a reconnect with a NEW session_id
// still inherits the layer via the stable auth_key fallback.
func TestNegotiatedLayerStickyContract(t *testing.T) {
	r := New(Config{DC: 2, IP: "127.0.0.1", Port: 2398}, Deps{}, zaptest.NewLogger(t), clock.System)
	authKey := [8]byte{1, 2, 3, 4, 5, 6, 7, 8}
	const session = int64(42)

	if l, ok := r.NegotiatedLayer(authKey, session); ok || l != currentClientLayer {
		t.Fatalf("unknown = (%d,%v), want (%d,false)", l, ok, currentClientLayer)
	}

	ctx := WithSessionID(WithRawAuthKeyID(context.Background(), authKey), session)
	r.rememberClientLayer(ctx, 220)

	if l, ok := r.NegotiatedLayer(authKey, session); !ok || l != 220 {
		t.Fatalf("recorded = (%d,%v), want (220,true)", l, ok)
	}
	// Reconnect: new session_id, same auth_key → inherits 220 (no re-invokeWithLayer).
	if l, ok := r.NegotiatedLayer(authKey, 999); !ok || l != 220 {
		t.Fatalf("reconnect new session = (%d,%v), want (220,true)", l, ok)
	}
	// Unrelated auth_key stays unknown.
	if _, ok := r.NegotiatedLayer([8]byte{9, 9}, session); ok {
		t.Fatalf("unrelated auth_key reported known")
	}
}

func TestClientTypeDetectsAndroidSDKVersion(t *testing.T) {
	info := normalizeClientInfo(ClientInfo{
		DeviceModel:   "GooglePixel 9a",
		SystemVersion: "SDK 36",
		AppVersion:    "12.7.3 (67509) pbeta",
	})
	if got := info.ClientType(); got != ClientTypeAndroid {
		t.Fatalf("client type = %s, want %s", got, ClientTypeAndroid)
	}

	info = normalizeClientInfo(ClientInfo{
		DeviceModel:   "go1.26.2",
		SystemVersion: "windows",
		AppVersion:    "v0.144.0",
	})
	if got := info.ClientType(); got != ClientTypeUnknown {
		t.Fatalf("gotd test client type = %s, want %s", got, ClientTypeUnknown)
	}
}

func TestDispatchRestoresClientMetadataFromAuthorization(t *testing.T) {
	core, logs := observer.New(zap.DebugLevel)
	authKeyID := [8]byte{0x68, 0x25, 0xc2, 0xee, 0xf8, 0x82, 0xef, 0x71}
	userID := int64(1780269504)
	auth := &captureAuthService{
		userID: userID,
		authorizations: []domain.Authorization{{
			AuthKeyID:     authKeyID,
			UserID:        userID,
			Layer:         currentClientLayer,
			DeviceModel:   "Android",
			Platform:      string(ClientTypeAndroid),
			SystemVersion: "Android 15",
			APIID:         6,
			AppVersion:    "12.7.3",
		}},
	}
	r := New(Config{DC: 2, IP: "127.0.0.1", Port: 2398}, Deps{
		Auth: auth,
	}, zap.New(core), clock.System)

	var in bin.Buffer
	if err := (&tg.HelpGetConfigRequest{}).Encode(&in); err != nil {
		t.Fatalf("encode request: %v", err)
	}
	if _, err := r.Dispatch(context.Background(), authKeyID, -3479521421865518854, &in); err != nil {
		t.Fatalf("dispatch plain request: %v", err)
	}

	entries := logs.FilterMessage("RPC inner handled").All()
	if len(entries) == 0 {
		t.Fatalf("RPC inner handled log missing")
	}
	fields := entries[len(entries)-1].ContextMap()
	if got := intLogField(fields["layer"]); got != currentClientLayer {
		t.Fatalf("logged layer = %d fields=%v, want %d", got, fields, currentClientLayer)
	}
	if got := fields["client_type"]; got != string(ClientTypeAndroid) {
		t.Fatalf("logged client_type = %v, want %s", got, ClientTypeAndroid)
	}
	if got := fields["app_version"]; got != "12.7.3" {
		t.Fatalf("logged app_version = %v, want 12.7.3", got)
	}

	var second bin.Buffer
	if err := (&tg.HelpGetConfigRequest{}).Encode(&second); err != nil {
		t.Fatalf("encode second request: %v", err)
	}
	if _, err := r.Dispatch(context.Background(), authKeyID, -3479521421865518853, &second); err != nil {
		t.Fatalf("dispatch second plain request: %v", err)
	}
	if auth.authorizationLookups != 1 || auth.authorizationLists != 0 {
		t.Fatalf("authorization lookups/list calls = %d/%d, want exact-key 1/list 0", auth.authorizationLookups, auth.authorizationLists)
	}
}

func TestDispatchRestoresAndroidMetadataFromAuthorizationSDKVersion(t *testing.T) {
	core, logs := observer.New(zap.DebugLevel)
	authKeyID := [8]byte{0x16, 0x65, 0x54, 0x12, 0xaa, 0xbb, 0xcc, 0xdd}
	userID := int64(1780269504)
	auth := &captureAuthService{
		userID: userID,
		authorizations: []domain.Authorization{{
			AuthKeyID:     authKeyID,
			UserID:        userID,
			DeviceModel:   "GooglePixel 9a",
			SystemVersion: "SDK 36",
			APIID:         4,
			AppVersion:    "12.7.3 (67509) pbeta",
		}},
	}
	r := New(Config{DC: 2, IP: "127.0.0.1", Port: 2398}, Deps{
		Auth: auth,
	}, zap.New(core), clock.System)

	var in bin.Buffer
	if err := (&tg.HelpGetConfigRequest{}).Encode(&in); err != nil {
		t.Fatalf("encode request: %v", err)
	}
	if _, err := r.Dispatch(context.Background(), authKeyID, -3479521421865518854, &in); err != nil {
		t.Fatalf("dispatch plain request: %v", err)
	}

	entries := logs.FilterMessage("RPC inner handled").All()
	if len(entries) == 0 {
		t.Fatalf("RPC inner handled log missing")
	}
	fields := entries[len(entries)-1].ContextMap()
	if got := intLogField(fields["layer"]); got != currentClientLayer {
		t.Fatalf("logged layer = %d fields=%v, want %d", got, fields, currentClientLayer)
	}
	if got := fields["client_type"]; got != string(ClientTypeAndroid) {
		t.Fatalf("logged client_type = %v, want %s", got, ClientTypeAndroid)
	}
	if got := fields["app_version"]; got != "12.7.3 (67509) pbeta" {
		t.Fatalf("logged app_version = %v, want 12.7.3 (67509) pbeta", got)
	}
	if auth.authorizationLookups != 1 || auth.authorizationLists != 0 {
		t.Fatalf("authorization lookups/list calls = %d/%d, want exact-key 1/list 0", auth.authorizationLookups, auth.authorizationLists)
	}
}

func TestDispatchCachesMissingClientMetadataAuthorizationLookup(t *testing.T) {
	core, logs := observer.New(zap.DebugLevel)
	authKeyID := [8]byte{0x68, 0x25, 0xc2, 0xee, 0xf8, 0x82, 0xef, 0x71}
	auth := &captureAuthService{userID: 1780269504}
	r := New(Config{DC: 2, IP: "127.0.0.1", Port: 2398}, Deps{
		Auth: auth,
	}, zap.New(core), clock.System)

	for _, sessionID := range []int64{-3479521421865518854, -3479521421865518853} {
		var in bin.Buffer
		if err := (&tg.HelpGetConfigRequest{}).Encode(&in); err != nil {
			t.Fatalf("encode request: %v", err)
		}
		if _, err := r.Dispatch(context.Background(), authKeyID, sessionID, &in); err != nil {
			t.Fatalf("dispatch plain request: %v", err)
		}
	}

	if auth.authorizationLookups != 1 || auth.authorizationLists != 0 {
		t.Fatalf("authorization lookups/list calls = %d/%d, want one cached exact-key empty lookup/list 0", auth.authorizationLookups, auth.authorizationLists)
	}
	entries := logs.FilterMessage("RPC inner handled").All()
	if len(entries) == 0 {
		t.Fatalf("RPC inner handled log missing")
	}
	fields := entries[len(entries)-1].ContextMap()
	if got := intLogField(fields["layer"]); got != 0 {
		t.Fatalf("logged layer = %d fields=%v, want unknown 0", got, fields)
	}
	if got := fields["client_type"]; got != string(ClientTypeUnknown) {
		t.Fatalf("logged client_type = %v, want %s", got, ClientTypeUnknown)
	}
}

func TestCurrentUserIDUsesAuthUserCache(t *testing.T) {
	authKeyID := [8]byte{0x42, 0x42, 0x42, 0x42, 0x42, 0x42, 0x42, 0x42}
	auth := &captureAuthService{userID: 1000000001}
	r := New(Config{}, Deps{Auth: auth}, zaptest.NewLogger(t), clock.System)
	ctx := WithAuthKeyID(context.Background(), authKeyID)

	for i := 0; i < 2; i++ {
		userID, ok, err := r.currentUserID(ctx)
		if err != nil || !ok || userID != auth.userID {
			t.Fatalf("currentUserID %d = user %d ok %v err %v, want %d/true", i, userID, ok, err, auth.userID)
		}
	}
	if auth.userIDCount != 1 {
		t.Fatalf("auth user lookups = %d, want 1", auth.userIDCount)
	}
}

func intLogField(v any) int {
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case int32:
		return int(n)
	case float64:
		return int(n)
	default:
		return 0
	}
}

func TestDispatchUnwrapsInvokeAfterWrappers(t *testing.T) {
	r := New(Config{DC: 2, IP: "127.0.0.1", Port: 2398}, Deps{}, zaptest.NewLogger(t), clock.System)

	tests := []struct {
		name string
		req  bin.Encoder
	}{
		{
			name: "invokeAfterMsg",
			req: &tg.InvokeAfterMsgRequest{
				MsgID: 123,
				Query: &tg.HelpGetConfigRequest{},
			},
		},
		{
			name: "invokeAfterMsgs",
			req: &tg.InvokeAfterMsgsRequest{
				MsgIDs: []int64{123, 456},
				Query:  &tg.HelpGetConfigRequest{},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var b bin.Buffer
			if err := tt.req.Encode(&b); err != nil {
				t.Fatalf("encode: %v", err)
			}
			enc, err := r.Dispatch(context.Background(), [8]byte{}, 0, &b)
			if err != nil {
				t.Fatalf("dispatch: %v", err)
			}
			if _, ok := enc.(*tg.Config); !ok {
				t.Fatalf("result type = %T, want *tg.Config", enc)
			}
		})
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

func TestDispatchCachesTempConnectionIdentity(t *testing.T) {
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

	if auth.resolveCount != 2 || auth.userIDCount != 1 {
		t.Fatalf("identity lookups = resolve %d user %d, want temp resolve checks and cached user identity", auth.resolveCount, auth.userIDCount)
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
	// 私聊 dialog 双向建行：两侧都存。presence 接收者由「主体自己的 dialog 对端 ∩ 在线」
	// 算出，bob 改状态时需 bob 侧 dialog 含 alice。
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
	if err := dialogs.SaveList(ctx, bob.ID, domain.DialogList{
		Dialogs: []domain.Dialog{{
			Peer:           domain.Peer{Type: domain.PeerTypeUser, ID: alice.ID},
			TopMessage:     10,
			TopMessageDate: 1700000000,
		}},
		Users: []domain.User{alice},
	}); err != nil {
		t.Fatalf("save bob dialogs: %v", err)
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
	gotPushes := waitForPushedUserIDs(t, sessions, 2)
	if !reflect.DeepEqual(gotPushes, []int64{bob.ID, alice.ID}) {
		t.Fatalf("pushed users = %+v, want self and online private dialog peer once", gotPushes)
	}
	// 用 lastUserPush（PushToUser* 的内容）而非 snapshot().message：后者还会被
	// pushOnlinePeerStatusesToCurrentSession 把 alice 的在线状态推给 bob 当前 session 覆盖。
	update := pushedUserStatus(t, waitForLastUserPush(t, sessions))
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
	sessions.clearMessages()
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
	update := waitForSessionUserStatus(t, sessions, bob.ID)
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
		{name: "help.getInviteText", req: &tg.HelpGetInviteTextRequest{}},
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
		{name: "account.getBusinessChatLinks", req: &tg.AccountGetBusinessChatLinksRequest{}},
		{name: "account.getReactionsNotifySettings", req: &tg.AccountGetReactionsNotifySettingsRequest{}},
		{name: "account.getContactSignUpNotification", req: &tg.AccountGetContactSignUpNotificationRequest{}},
		{name: "account.getThemes", req: &tg.AccountGetThemesRequest{Format: "tdesktop"}},
		{name: "account.getChatThemes", req: &tg.AccountGetChatThemesRequest{}},
		{name: "account.getWallPapers", req: &tg.AccountGetWallPapersRequest{}},
		{name: "account.getUniqueGiftChatThemes", req: &tg.AccountGetUniqueGiftChatThemesRequest{Limit: 20}},
		{name: "account.getContentSettings", req: &tg.AccountGetContentSettingsRequest{}},
		{name: "account.getGlobalPrivacySettings", req: &tg.AccountGetGlobalPrivacySettingsRequest{}},
		{name: "account.getPasskeys", req: &tg.AccountGetPasskeysRequest{}},
		{name: "account.getAutoDownloadSettings", req: &tg.AccountGetAutoDownloadSettingsRequest{}},
		{name: "account.getSavedMusicIds", req: &tg.AccountGetSavedMusicIDsRequest{}},
		{name: "account.getSavedRingtones", req: &tg.AccountGetSavedRingtonesRequest{}},
		{name: "account.resetPassword", req: &tg.AccountResetPasswordRequest{}},
		{name: "account.updateStatus", req: &tg.AccountUpdateStatusRequest{Offline: true}},
		{name: "payments.getStarsTopupOptions", req: &tg.PaymentsGetStarsTopupOptionsRequest{}},
		{name: "payments.getStarsStatus", req: &tg.PaymentsGetStarsStatusRequest{Peer: &tg.InputPeerSelf{}}},
		{name: "updates.getDifference", req: &tg.UpdatesGetDifferenceRequest{}},
		{name: "users.getFullUser", req: &tg.UsersGetFullUserRequest{ID: &tg.InputUserSelf{}}},
		{name: "users.getRequirementsToContact", req: &tg.UsersGetRequirementsToContactRequest{ID: []tg.InputUserClass{&tg.InputUserSelf{}}}},
		{name: "users.getSavedMusic", req: &tg.UsersGetSavedMusicRequest{ID: &tg.InputUserSelf{}, Limit: 20}},
		{name: "users.getSavedMusicByID", req: &tg.UsersGetSavedMusicByIDRequest{ID: &tg.InputUserSelf{}, Documents: []tg.InputDocumentClass{}}},
		{name: "messages.getDialogFilters", req: &tg.MessagesGetDialogFiltersRequest{}},
		{name: "messages.getDialogs", req: &tg.MessagesGetDialogsRequest{OffsetPeer: &tg.InputPeerEmpty{}, Limit: 20}},
		{name: "messages.getPinnedDialogs", req: &tg.MessagesGetPinnedDialogsRequest{}},
		{name: "messages.getPeerDialogs", req: &tg.MessagesGetPeerDialogsRequest{Peers: []tg.InputDialogPeerClass{&tg.InputDialogPeer{Peer: &tg.InputPeerUser{UserID: domain.OfficialSystemUserID, AccessHash: domain.OfficialSystemUser().AccessHash}}}}},
		{name: "messages.getAvailableReactions", req: &tg.MessagesGetAvailableReactionsRequest{}},
		{name: "messages.getAvailableEffects", req: &tg.MessagesGetAvailableEffectsRequest{}},
		{name: "messages.getStickers", req: &tg.MessagesGetStickersRequest{}},
		{name: "messages.getArchivedStickers", req: &tg.MessagesGetArchivedStickersRequest{Limit: 20}},
		{name: "messages.getMaskStickers", req: &tg.MessagesGetMaskStickersRequest{}},
		{name: "messages.getStickerSet", req: &tg.MessagesGetStickerSetRequest{Stickerset: &tg.InputStickerSetEmpty{}}},
		{name: "messages.getEmojiGroups", req: &tg.MessagesGetEmojiGroupsRequest{}},
		{name: "messages.getEmojiStatusGroups", req: &tg.MessagesGetEmojiStatusGroupsRequest{}},
		{name: "messages.getEmojiStickerGroups", req: &tg.MessagesGetEmojiStickerGroupsRequest{}},
		{name: "messages.getEmojiProfilePhotoGroups", req: &tg.MessagesGetEmojiProfilePhotoGroupsRequest{}},
		{name: "messages.getEmojiKeywordsLanguages", req: &tg.MessagesGetEmojiKeywordsLanguagesRequest{LangCodes: []string{"en"}}},
		{name: "messages.getAttachMenuBots", req: &tg.MessagesGetAttachMenuBotsRequest{}},
		{name: "messages.getQuickReplies", req: &tg.MessagesGetQuickRepliesRequest{}},
		{name: "messages.getQuickReplyMessages", req: &tg.MessagesGetQuickReplyMessagesRequest{ShortcutID: 1}},
		{name: "messages.getSavedHistory", req: &tg.MessagesGetSavedHistoryRequest{Peer: &tg.InputPeerSelf{}, Limit: 20}},
		{name: "messages.readSavedHistory", req: &tg.MessagesReadSavedHistoryRequest{ParentPeer: &tg.InputPeerChannel{ChannelID: 1, AccessHash: 1}, Peer: &tg.InputPeerSelf{}}},
		{name: "messages.deleteSavedHistory", req: &tg.MessagesDeleteSavedHistoryRequest{Peer: &tg.InputPeerSelf{}}},
		{name: "messages.getPeerSettings", req: &tg.MessagesGetPeerSettingsRequest{Peer: &tg.InputPeerUser{UserID: domain.OfficialSystemUserID, AccessHash: domain.OfficialSystemUser().AccessHash}}},
		{name: "messages.setChatWallPaper", req: &tg.MessagesSetChatWallPaperRequest{Peer: &tg.InputPeerUser{UserID: domain.OfficialSystemUserID, AccessHash: domain.OfficialSystemUser().AccessHash}, Wallpaper: &tg.InputWallPaperNoFile{ID: 930000000000000000}}},
		{name: "messages.getHistory", req: &tg.MessagesGetHistoryRequest{Peer: &tg.InputPeerUser{UserID: domain.OfficialSystemUserID, AccessHash: domain.OfficialSystemUser().AccessHash}, Limit: 20}},
		{name: "messages.readHistory", req: &tg.MessagesReadHistoryRequest{Peer: &tg.InputPeerUser{UserID: domain.OfficialSystemUserID, AccessHash: domain.OfficialSystemUser().AccessHash}}},
		{name: "messages.search", req: &tg.MessagesSearchRequest{Peer: &tg.InputPeerUser{UserID: domain.OfficialSystemUserID, AccessHash: domain.OfficialSystemUser().AccessHash}, Filter: &tg.InputMessagesFilterEmpty{}, Limit: 20}},
		{name: "messages.searchGlobal", req: &tg.MessagesSearchGlobalRequest{Q: "login", Filter: &tg.InputMessagesFilterEmpty{}, OffsetPeer: &tg.InputPeerEmpty{}, Limit: 20}},
		{name: "messages.getWebPage", req: &tg.MessagesGetWebPageRequest{URL: "https://example.invalid"}},
		{name: "messages.getScheduledHistory", req: &tg.MessagesGetScheduledHistoryRequest{Peer: &tg.InputPeerUser{UserID: domain.OfficialSystemUserID, AccessHash: domain.OfficialSystemUser().AccessHash}}},
		{name: "contacts.getContacts", req: &tg.ContactsGetContactsRequest{}},
		{name: "contacts.search", req: &tg.ContactsSearchRequest{Q: "Test", Limit: 20}},
		{name: "contacts.getBlocked", req: &tg.ContactsGetBlockedRequest{Limit: 20}},
		{name: "contacts.getBirthdays", req: &tg.ContactsGetBirthdaysRequest{}},
		{name: "contacts.getTopPeers", req: &tg.ContactsGetTopPeersRequest{Correspondents: true, Limit: 10}},
		{name: "contacts.getSponsoredPeers", req: &tg.ContactsGetSponsoredPeersRequest{Q: "Test"}},
		{name: "stories.getAllStories", req: &tg.StoriesGetAllStoriesRequest{}},
		{name: "stories.getStoriesArchive", req: &tg.StoriesGetStoriesArchiveRequest{Peer: &tg.InputPeerSelf{}, Limit: 20}},
		{name: "stories.getPinnedStories", req: &tg.StoriesGetPinnedStoriesRequest{Peer: &tg.InputPeerSelf{}, Limit: 20}},
		{name: "stories.exportStoryLink", req: &tg.StoriesExportStoryLinkRequest{Peer: &tg.InputPeerSelf{}, ID: 1}},
		{name: "stories.report", req: &tg.StoriesReportRequest{Peer: &tg.InputPeerSelf{}, ID: []int{1}}},
		{name: "stories.activateStealthMode", req: &tg.StoriesActivateStealthModeRequest{Past: true, Future: true}},
		{name: "stories.searchPosts", req: &tg.StoriesSearchPostsRequest{Hashtag: "storytag", Limit: 20}},
		{name: "stories.getAlbums", req: &tg.StoriesGetAlbumsRequest{Peer: &tg.InputPeerSelf{}}},
		{name: "stories.getAlbumStories", req: &tg.StoriesGetAlbumStoriesRequest{Peer: &tg.InputPeerSelf{}, AlbumID: 1, Limit: 20}},
		{name: "stories.toggleAllStoriesHidden", req: &tg.StoriesToggleAllStoriesHiddenRequest{Hidden: true}},
		{name: "stories.reorderAlbums", req: &tg.StoriesReorderAlbumsRequest{Peer: &tg.InputPeerSelf{}, Order: []int{1}}},
		{name: "stories.deleteAlbum", req: &tg.StoriesDeleteAlbumRequest{Peer: &tg.InputPeerSelf{}, AlbumID: 1}},
		{name: "stories.getAllReadPeerStories", req: &tg.StoriesGetAllReadPeerStoriesRequest{}},
		{name: "stories.getPeerMaxIDs", req: &tg.StoriesGetPeerMaxIDsRequest{ID: []tg.InputPeerClass{&tg.InputPeerSelf{}}}},
		{name: "stories.getStoriesViews", req: &tg.StoriesGetStoriesViewsRequest{Peer: &tg.InputPeerSelf{}, ID: []int{1}}},
		{name: "stories.getChatsToSend", req: &tg.StoriesGetChatsToSendRequest{}},
		{name: "payments.getStarGiftActiveAuctions", req: &tg.PaymentsGetStarGiftActiveAuctionsRequest{}},
		{name: "payments.getStarGifts", req: &tg.PaymentsGetStarGiftsRequest{}},
		{name: "payments.getStarGiftCollections", req: &tg.PaymentsGetStarGiftCollectionsRequest{Peer: &tg.InputPeerSelf{}}},
		{name: "payments.getSavedStarGifts", req: &tg.PaymentsGetSavedStarGiftsRequest{Peer: &tg.InputPeerSelf{}, Limit: 20}},
		{name: "payments.getSavedStarGift", req: &tg.PaymentsGetSavedStarGiftRequest{Stargift: []tg.InputSavedStarGiftClass{}}},
		{name: "payments.getStarsRevenueAdsAccountUrl", req: &tg.PaymentsGetStarsRevenueAdsAccountURLRequest{Peer: &tg.InputPeerSelf{}}},
		{name: "payments.getStarsRevenueStats", req: &tg.PaymentsGetStarsRevenueStatsRequest{Ton: true, Peer: &tg.InputPeerSelf{}}},
		{name: "bots.getBotRecommendations", req: &tg.BotsGetBotRecommendationsRequest{Bot: &tg.InputUser{UserID: domain.OfficialSystemUserID, AccessHash: domain.OfficialSystemUser().AccessHash}}},
		{name: "aicompose.getTones", req: &tg.AicomposeGetTonesRequest{}},
		{name: "langpack.getLanguage", req: &tg.LangpackGetLanguageRequest{LangPack: "tdesktop", LangCode: "en"}},
		{name: "langpack.getLangPack", req: &tg.LangpackGetLangPackRequest{LangPack: "tdesktop", LangCode: "en"}},
		{name: "langpack.getDifference", req: &tg.LangpackGetDifferenceRequest{LangPack: "tdesktop", LangCode: "en", FromVersion: 1}},
		{name: "langpack.getStrings", req: &tg.LangpackGetStringsRequest{LangPack: "tdesktop", LangCode: "en", Keys: []string{"lng_intro_about"}}},
		{name: "help.getDeepLinkInfo", req: &tg.HelpGetDeepLinkInfoRequest{Path: "join?invite=abc"}},
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

// TestHelpGetDeepLinkInfoReturnsEmpty 回归：help.getDeepLinkInfo 此前未注册 handler，
// 落 fallback 返回 500 NOT_IMPLEMENTED。客户端遇到无法识别的 tg:// 深链就会发该请求
// （DrKLO LaunchActivity unsupportedUrl 分支），应返回规范的 deepLinkInfoEmpty 而非报错。
func TestHelpGetDeepLinkInfoReturnsEmpty(t *testing.T) {
	r := New(Config{}, Deps{}, zaptest.NewLogger(t), clock.System)
	req := &tg.HelpGetDeepLinkInfoRequest{Path: "resolve?domain=unknown"}
	var in bin.Buffer
	if err := req.Encode(&in); err != nil {
		t.Fatalf("encode request: %v", err)
	}
	enc, err := r.Dispatch(WithUserID(context.Background(), 1000000001), [8]byte{}, 0, &in)
	if err != nil {
		t.Fatalf("dispatch help.getDeepLinkInfo err = %v, want nil (未注册时为 500 NOT_IMPLEMENTED)", err)
	}
	var out bin.Buffer
	if err := enc.Encode(&out); err != nil {
		t.Fatalf("encode response: %v", err)
	}
	res, err := tg.DecodeHelpDeepLinkInfo(&out)
	if err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if _, ok := res.(*tg.HelpDeepLinkInfoEmpty); !ok {
		t.Fatalf("response type = %T, want *tg.HelpDeepLinkInfoEmpty", res)
	}
}

func TestStoriesStartLiveDispatchReturnsMethodInvalid(t *testing.T) {
	r := New(Config{}, Deps{}, zaptest.NewLogger(t), clock.System)
	req := &tg.StoriesStartLiveRequest{
		Peer:         &tg.InputPeerSelf{},
		PrivacyRules: []tg.InputPrivacyRuleClass{&tg.InputPrivacyValueAllowAll{}},
		RandomID:     42,
	}
	var in bin.Buffer
	if err := req.Encode(&in); err != nil {
		t.Fatalf("encode request: %v", err)
	}
	_, err := r.Dispatch(WithUserID(context.Background(), 1000000001), [8]byte{}, 0, &in)
	if err == nil || !tgerr.Is(err, "METHOD_INVALID") {
		t.Fatalf("dispatch stories.startLive err = %v, want METHOD_INVALID", err)
	}
}

func TestStoriesAlbumMutationDispatchReturnsMethodInvalid(t *testing.T) {
	r := New(Config{}, Deps{}, zaptest.NewLogger(t), clock.System)
	updateReq := &tg.StoriesUpdateAlbumRequest{
		Peer:    &tg.InputPeerSelf{},
		AlbumID: 1,
	}
	updateReq.SetTitle("Travel")

	tests := []struct {
		name string
		req  bin.Encoder
	}{
		{name: "stories.createAlbum", req: &tg.StoriesCreateAlbumRequest{
			Peer:    &tg.InputPeerSelf{},
			Title:   "Favorites",
			Stories: []int{1},
		}},
		{name: "stories.updateAlbum", req: updateReq},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var in bin.Buffer
			if err := tt.req.Encode(&in); err != nil {
				t.Fatalf("encode request: %v", err)
			}
			_, err := r.Dispatch(WithUserID(context.Background(), 1000000001), [8]byte{}, 0, &in)
			if err == nil || !tgerr.Is(err, "METHOD_INVALID") {
				t.Fatalf("dispatch %s err = %v, want METHOD_INVALID", tt.name, err)
			}
		})
	}
}
