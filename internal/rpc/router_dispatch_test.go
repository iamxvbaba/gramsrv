package rpc

import (
	"context"
	"encoding/binary"
	"fmt"
	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"github.com/iamxvbaba/td/tlprofile"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest"
	"go.uber.org/zap/zaptest/observer"
	"reflect"
	"sync"
	appauth "telesrv/internal/app/auth"
	appdialogs "telesrv/internal/app/dialogs"
	"telesrv/internal/domain"
	"telesrv/internal/store"
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

	enc, method, err := r.DispatchWithMethod(context.Background(), [8]byte{}, 0, &b)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if method != "help.getConfig" {
		t.Fatalf("effective method = %q, want help.getConfig", method)
	}
	cfg, ok := enc.(*tg.Config)
	if !ok {
		t.Fatalf("result type = %T, want *tg.Config", enc)
	}
	if cfg.ThisDC != dc {
		t.Fatalf("ThisDC = %d, want %d", cfg.ThisDC, dc)
	}
	if len(cfg.DCOptions) != 0 {
		t.Fatalf("DCOptions = %+v, want empty (client uses pinned static address)", cfg.DCOptions)
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
	info, ok, _ := r.clientSessionInfo(sessionCtx)
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
		t.Fatalf("new session logged layer = %d fields=%v, want inherited %d", got, fields, layer)
	}
	if got := fields["client_type"]; got != string(ClientTypeTDesktop) {
		t.Fatalf("new session logged client_type = %v, want %s", got, ClientTypeTDesktop)
	}
	if got := fields["app_version"]; got != "6.8.4 x64" {
		t.Fatalf("new session logged app_version = %v, want 6.8.4 x64", got)
	}
}

func TestDispatchPersistsPreLoginClientMetadataOnInitConnection(t *testing.T) {
	auth := &captureAuthService{}
	r := New(Config{DC: 2, IP: "127.0.0.1", Port: 2398}, Deps{
		Auth: auth,
	}, zaptest.NewLogger(t), clock.System)
	rawAuthKeyID := [8]byte{0x22, 0xdb, 0xcf, 0xc8, 0x0d, 0x4c, 0x77, 0x97}
	sessionID := int64(8103956954238395544)

	req := &tg.InvokeWithLayerRequest{
		Layer: currentClientLayer,
		Query: &tg.InitConnectionRequest{
			APIID:          4,
			DeviceModel:    "GooglePixel 9a",
			SystemVersion:  "SDK 36",
			AppVersion:     "12.8.1 (69169) pbeta",
			SystemLangCode: "en",
			LangPack:       "android",
			LangCode:       "en",
			Query:          &tg.HelpGetConfigRequest{},
		},
	}
	var in bin.Buffer
	if err := req.Encode(&in); err != nil {
		t.Fatalf("encode init request: %v", err)
	}
	if _, err := r.Dispatch(context.Background(), rawAuthKeyID, sessionID, &in); err != nil {
		t.Fatalf("dispatch init request: %v", err)
	}

	got, ok := auth.authKeyClientInfos[rawAuthKeyID]
	if !ok {
		t.Fatalf("auth key client metadata was not persisted")
	}
	if got.Layer != currentClientLayer {
		t.Fatalf("persisted layer = %d, want %d", got.Layer, currentClientLayer)
	}
	if got.Platform != string(ClientTypeAndroid) {
		t.Fatalf("persisted platform = %q, want android", got.Platform)
	}
	if got.DeviceModel != "GooglePixel 9a" || got.SystemVersion != "SDK 36" || got.APIID != 4 || got.AppVersion != "12.8.1 (69169) pbeta" {
		t.Fatalf("persisted client metadata = %+v", got)
	}
}

func TestDispatchPersistsPreLoginClientMetadataFromSendCodeAPIID(t *testing.T) {
	auth := &captureAuthService{}
	rawAuthKeyID := [8]byte{0x33, 0xdb, 0xcf, 0xc8, 0x0d, 0x4c, 0x77, 0x97}
	const sessionID = int64(8103956954238395544)
	r := New(Config{DC: 2, IP: "127.0.0.1", Port: 2398}, Deps{
		Auth: auth,
	}, zaptest.NewLogger(t), clock.System)

	var sendCode bin.Buffer
	if err := (&tg.AuthSendCodeRequest{
		PhoneNumber: "+8618800000020",
		APIID:       4,
		APIHash:     "android",
		Settings:    tg.CodeSettings{},
	}).Encode(&sendCode); err != nil {
		t.Fatalf("encode auth.sendCode: %v", err)
	}
	if _, err := r.Dispatch(context.Background(), rawAuthKeyID, sessionID, &sendCode); err != nil {
		t.Fatalf("dispatch auth.sendCode: %v", err)
	}
	persisted, ok := auth.authKeyClientInfos[rawAuthKeyID]
	if !ok {
		t.Fatalf("auth.sendCode did not persist auth key client metadata")
	}
	if persisted.APIID != 4 || persisted.Platform != string(ClientTypeAndroid) {
		t.Fatalf("persisted client metadata = %+v, want android api_id=4", persisted)
	}

	core, logs := observer.New(zap.DebugLevel)
	afterRestart := New(Config{DC: 2, IP: "127.0.0.1", Port: 2398}, Deps{
		Auth: auth,
	}, zap.New(core), clock.System)
	var help bin.Buffer
	if err := (&tg.HelpGetConfigRequest{}).Encode(&help); err != nil {
		t.Fatalf("encode help.getConfig: %v", err)
	}
	if _, err := afterRestart.Dispatch(context.Background(), rawAuthKeyID, sessionID+1, &help); err != nil {
		t.Fatalf("dispatch help.getConfig after restart: %v", err)
	}
	entries := logs.FilterMessage("RPC inner handled").All()
	if len(entries) == 0 {
		t.Fatalf("RPC inner handled log missing")
	}
	fields := entries[len(entries)-1].ContextMap()
	if got := fields["client_type"]; got != string(ClientTypeAndroid) {
		t.Fatalf("logged client_type = %v, want %s", got, ClientTypeAndroid)
	}
}

func TestSendCodeAPIIDDoesNotOverwriteStrongTWebIdentity(t *testing.T) {
	auth := &captureAuthService{}
	rawAuthKeyID := [8]byte{0x34, 0xdb, 0xcf, 0xc8, 0x0d, 0x4c, 0x77, 0x97}
	const sessionID = int64(8103956954238395545)
	r := New(Config{DC: 2, IP: "127.0.0.1", Port: 2398}, Deps{Auth: auth}, zaptest.NewLogger(t), clock.System)

	initReq := &tg.InvokeWithLayerRequest{
		Layer: currentClientLayer,
		Query: &tg.InitConnectionRequest{
			APIID:          2040,
			DeviceModel:    "Mozilla/5.0 (Windows NT 10.0) AppleWebKit/537.36",
			SystemVersion:  "Win32",
			AppVersion:     "2.2",
			SystemLangCode: "en-US",
			LangPack:       "webk",
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

	var sendCode bin.Buffer
	if err := (&tg.AuthSendCodeRequest{
		PhoneNumber: "+8618800000021",
		APIID:       2040,
		APIHash:     "tweb-local",
		Settings:    tg.CodeSettings{},
	}).Encode(&sendCode); err != nil {
		t.Fatalf("encode auth.sendCode: %v", err)
	}
	if _, err := r.Dispatch(context.Background(), rawAuthKeyID, sessionID, &sendCode); err != nil {
		t.Fatalf("dispatch auth.sendCode: %v", err)
	}

	persisted := auth.authKeyClientInfos[rawAuthKeyID]
	if persisted.Platform != string(ClientTypeTWeb) || persisted.DeviceModel != initReq.Query.(*tg.InitConnectionRequest).DeviceModel {
		t.Fatalf("API id fallback overwrote strong TWeb identity: %+v", persisted)
	}
}

func TestDispatchRestoresPreLoginAndroidDescriptionWithoutLayer(t *testing.T) {
	core, logs := observer.New(zap.DebugLevel)
	authKeyID := [8]byte{0x22, 0xdb, 0xcf, 0xc8, 0x0d, 0x4c, 0x77, 0x97}
	auth := &captureAuthService{
		authKeyClientInfos: map[[8]byte]domain.AuthKeyClientInfo{
			authKeyID: {
				Layer:         currentClientLayer,
				DeviceModel:   "GooglePixel 9a",
				Platform:      string(ClientTypeAndroid),
				SystemVersion: "SDK 36",
				APIID:         4,
				AppVersion:    "12.8.1 (69169) pbeta",
			},
		},
	}
	r := New(Config{DC: 2, IP: "127.0.0.1", Port: 2398}, Deps{
		Auth: auth,
	}, zap.New(core), clock.System)

	var in bin.Buffer
	if err := (&tg.HelpGetConfigRequest{}).Encode(&in); err != nil {
		t.Fatalf("encode request: %v", err)
	}
	const sessionID = int64(8103956954238395544)
	if _, err := r.Dispatch(context.Background(), authKeyID, sessionID, &in); err != nil {
		t.Fatalf("dispatch plain request: %v", err)
	}

	entries := logs.FilterMessage("RPC inner handled").All()
	if len(entries) == 0 {
		t.Fatalf("RPC inner handled log missing")
	}
	fields := entries[len(entries)-1].ContextMap()
	if got := intLogField(fields["layer"]); got != currentClientLayer {
		t.Fatalf("logged layer = %d fields=%v, want restored %d", got, fields, currentClientLayer)
	}
	if got := fields["client_type"]; got != string(ClientTypeAndroid) {
		t.Fatalf("logged client_type = %v, want %s", got, ClientTypeAndroid)
	}
	if got := fields["app_version"]; got != "12.8.1 (69169) pbeta" {
		t.Fatalf("logged app_version = %v, want 12.8.1 (69169) pbeta", got)
	}
	if got, ok := r.NegotiatedLayer(authKeyID, sessionID); ok || got != currentClientLayer {
		t.Fatalf("metadata-only negotiated layer = (%d,%v), want (%d,false)", got, ok, currentClientLayer)
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

	// The legacy Android constructor is upgraded by the gotdgen client overlay and dispatched
	// normally; client metadata is still applied only because IsClientDrift
	// positively identified a DrKLO constructor, now surfaced on the standard
	// "RPC inner handled" log.
	entries := logs.FilterMessage("RPC inner handled").All()
	if len(entries) == 0 {
		t.Fatalf("RPC inner handled log missing")
	}
	fields := entries[len(entries)-1].ContextMap()
	if got := intLogField(fields["layer"]); got != 0 {
		t.Fatalf("logged layer = %d fields=%v, want 0", got, fields)
	}
	if got := fields["client_type"]; got != string(ClientTypeAndroid) {
		t.Fatalf("logged client_type = %v, want %s", got, ClientTypeAndroid)
	}
	if got, ok := r.NegotiatedLayer(rawAuthKeyID, sessionID); ok || got != currentClientLayer {
		t.Fatalf("negotiated layer = (%d,%v), want (%d,false)", got, ok, currentClientLayer)
	}
}

// TestNegotiatedLayerExactSessionContract pins the protocol-only boundary:
// unknown ⇒ ok=false, invokeWithLayer evidence is sticky for the same logical
// session, and another session on the same auth key remains unknown.
func TestNegotiatedLayerExactSessionContract(t *testing.T) {
	r := New(Config{DC: 2, IP: "127.0.0.1", Port: 2398}, Deps{}, zaptest.NewLogger(t), clock.System)
	authKey := [8]byte{1, 2, 3, 4, 5, 6, 7, 8}
	const session = int64(42)

	if l, ok := r.NegotiatedLayer(authKey, session); ok || l != currentClientLayer {
		t.Fatalf("unknown = (%d,%v), want (%d,false)", l, ok, currentClientLayer)
	}

	ctx := WithSessionID(WithRawAuthKeyID(context.Background(), authKey), session)
	r.rememberClientLayer(ctx, 225)

	if l, ok := r.NegotiatedLayer(authKey, session); !ok || l != 225 {
		t.Fatalf("recorded = (%d,%v), want (225,true)", l, ok)
	}
	if l, ok := r.NegotiatedSessionLayer(authKey, session); !ok || l != 225 {
		t.Fatalf("exact session seed = (%d,%v), want (225,true)", l, ok)
	}
	// A different logical session must provide its own protocol evidence.
	if l, ok := r.NegotiatedLayer(authKey, 999); ok || l != currentClientLayer {
		t.Fatalf("new session = (%d,%v), want (%d,false)", l, ok, currentClientLayer)
	}
	if l, ok := r.NegotiatedSessionLayer(authKey, 999); ok || l != 0 {
		t.Fatalf("new-session exact seed = (%d,%v), want (0,false)", l, ok)
	}
	// Unrelated auth_key stays unknown.
	if _, ok := r.NegotiatedLayer([8]byte{9, 9}, session); ok {
		t.Fatalf("unrelated auth_key reported known")
	}
}

func TestObservedClientLayerNeverLeaksAcrossAuthKeySessions(t *testing.T) {
	r := New(Config{DC: 2, IP: "127.0.0.1", Port: 2398}, Deps{}, zaptest.NewLogger(t), clock.System)
	rawAuthKey := [8]byte{0x68, 0x25, 0x7a, 0x01}
	effectiveAuthKey := [8]byte{0x8b, 0x6f, 0x26, 0x17}
	ctx := WithAuthKeyID(
		WithSessionID(WithRawAuthKeyID(context.Background(), rawAuthKey), 100),
		effectiveAuthKey,
	)

	r.rememberClientSessionInfo(ctx, clientSessionInfo{layer: currentClientLayer})
	r.rememberClientLayer(ctx, 225)

	if got, ok := r.NegotiatedLayer(rawAuthKey, 100); !ok || got != 225 {
		t.Fatalf("exact session layer = (%d,%v), want (225,true)", got, ok)
	}
	if got, ok := r.NegotiatedLayer(rawAuthKey, 101); ok || got != currentClientLayer {
		t.Fatalf("raw auth new-session layer = (%d,%v), want (%d,false)", got, ok, currentClientLayer)
	}
	if got, ok := r.NegotiatedLayer(effectiveAuthKey, 101); ok || got != currentClientLayer {
		t.Fatalf("effective auth metadata layer = (%d,%v), want (%d,false)", got, ok, currentClientLayer)
	}
}

func TestInvokeWithLayerPersistsClientLayerUpgrade(t *testing.T) {
	authKeyID := [8]byte{0x68, 0x25, 0x7a, 0x02}
	userID := int64(1780269504)
	auth := &captureAuthService{
		userID: userID,
		authorizations: []domain.Authorization{{
			AuthKeyID: authKeyID,
			UserID:    userID,
			Layer:     225,
		}},
	}
	r := New(Config{DC: 2, IP: "127.0.0.1", Port: 2398}, Deps{
		Auth: auth,
	}, zaptest.NewLogger(t), clock.System)
	req := &tg.InvokeWithLayerRequest{
		Layer: currentClientLayer,
		Query: &tg.HelpGetConfigRequest{},
	}
	var in bin.Buffer
	if err := req.Encode(&in); err != nil {
		t.Fatalf("encode invokeWithLayer: %v", err)
	}

	if _, err := r.Dispatch(context.Background(), authKeyID, 100, &in); err != nil {
		t.Fatalf("dispatch invokeWithLayer: %v", err)
	}

	if got := auth.authKeyClientInfos[authKeyID].Layer; got != currentClientLayer {
		t.Fatalf("persisted auth-key layer = %d, want %d", got, currentClientLayer)
	}
	if got := auth.authorizations[0].Layer; got != 225 {
		t.Fatalf("unordered authorization mirror changed to %d, want 225", got)
	}
	if got, ok := r.NegotiatedLayer(authKeyID, 100); !ok || got != currentClientLayer {
		t.Fatalf("same session negotiated layer = (%d,%v), want (%d,true)", got, ok, currentClientLayer)
	}
	if got, ok := r.NegotiatedLayer(authKeyID, 101); ok || got != currentClientLayer {
		t.Fatalf("new session negotiated layer = (%d,%v), want (%d,false)", got, ok, currentClientLayer)
	}
}

func TestClientTypeDetectionUsesStrongEvidenceBeforeAPIID(t *testing.T) {
	tests := []struct {
		name string
		info ClientInfo
		want ClientType
	}{
		{
			name: "iOS 12.8 simulator",
			info: ClientInfo{APIID: 1, DeviceModel: "iPhone Simulator", SystemVersion: "26.5", AppVersion: "12.8 (10000)", LangPack: "ios"},
			want: ClientTypeIOS,
		},
		{
			name: "restored iOS without lang pack",
			info: ClientInfo{DeviceModel: "iPhone 16 Pro", SystemVersion: "18.5", Type: ClientTypeUnknown},
			want: ClientTypeIOS,
		},
		{
			name: "TWeb WebK",
			info: ClientInfo{APIID: 1025907, DeviceModel: "Mozilla/5.0 Chrome/138.0", SystemVersion: "Win32", LangPack: "webk"},
			want: ClientTypeTWeb,
		},
		{
			name: "telegram-tt WebA",
			info: ClientInfo{APIID: 2040, DeviceModel: "Mozilla/5.0 Chrome/150.0", SystemVersion: "Windows", AppVersion: "12.0.32 A", LangPack: "weba"},
			want: ClientTypeTelegramTT,
		},
		{
			name: "TWeb borrowed TDesktop API id",
			info: ClientInfo{APIID: 2040, DeviceModel: "Mozilla/5.0 AppleWebKit/537.36", SystemVersion: "Win32", Type: ClientTypeTDesktop},
			want: ClientTypeTWeb,
		},
		{
			name: "mobile TWeb is not native Android",
			info: ClientInfo{APIID: 2040, DeviceModel: "Mozilla/5.0 (Linux; Android 15) AppleWebKit/537.36", LangPack: "webk"},
			want: ClientTypeTWeb,
		},
		{
			name: "Android SDK version",
			info: ClientInfo{DeviceModel: "GooglePixel 9a", SystemVersion: "SDK 36", AppVersion: "12.7.3 (67509) pbeta"},
			want: ClientTypeAndroid,
		},
		{name: "DrKLO API fallback", info: ClientInfo{APIID: 4}, want: ClientTypeAndroid},
		{name: "TDesktop API fallback", info: ClientInfo{APIID: 2040}, want: ClientTypeTDesktop},
		{name: "official iOS API fallback", info: ClientInfo{APIID: 8}, want: ClientTypeIOS},
		{name: "official TWeb API fallback", info: ClientInfo{APIID: 2496}, want: ClientTypeTWeb},
		{name: "macOS lang pack", info: ClientInfo{LangPack: "macos"}, want: ClientTypeMacOS},
		{name: "stored known type", info: ClientInfo{Type: ClientTypeIOS}, want: ClientTypeIOS},
		{
			name: "gotd remains unknown",
			info: ClientInfo{DeviceModel: "go1.26.2", SystemVersion: "windows", AppVersion: "v0.144.0"},
			want: ClientTypeUnknown,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := normalizeClientInfo(tt.info).ClientType(); got != tt.want {
				t.Fatalf("client type = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestRestoredUnknownClientInfoIsNotReclassifiedFromHistoricalFields(t *testing.T) {
	info := restoreClientInfo(ClientInfo{
		APIID:         2040,
		DeviceModel:   "Mozilla/5.0 AppleWebKit/537.36",
		SystemVersion: "Windows",
		AppVersion:    "12.0.32 A",
		Type:          ClientTypeUnknown,
	})
	if got := info.ClientType(); got != ClientTypeUnknown {
		t.Fatalf("restored historical client type = %s, want unknown until fresh initConnection", got)
	}

	info = restoreClientInfo(ClientInfo{
		DeviceModel:   "GooglePixel 9a",
		SystemVersion: "SDK 36",
		Type:          ClientTypeUnknown,
	})
	if got := info.ClientType(); got != ClientTypeUnknown {
		t.Fatalf("restored historical Android client type = %s, want unknown until fresh initConnection", got)
	}

	info = restoreClientInfo(ClientInfo{
		DeviceModel: "Mozilla/5.0 AppleWebKit/537.36",
		AppVersion:  "12.0.32 A",
		Type:        ClientTypeTelegramTT,
	})
	if got := info.ClientType(); got != ClientTypeTelegramTT {
		t.Fatalf("restored persisted telegram-tt client type = %s, want %s", got, ClientTypeTelegramTT)
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
	if got := intLogField(fields["layer"]); got != 0 {
		t.Fatalf("logged layer = %d fields=%v, want unknown without auth_keys evidence", got, fields)
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

func TestDispatchCopiesPermanentAuthorizationMetadataWithoutLayerEvidence(t *testing.T) {
	rawAuthKeyID := [8]byte{0x1a, 0x2d, 0x2d, 0x3d, 0x4b, 0x38, 0x62, 0xc0}
	permAuthKeyID := [8]byte{0x5b, 0x1c, 0x12, 0x24, 0x98, 0x85, 0x60, 0xc1}
	userID := int64(1780243218)
	auth := &captureAuthService{
		resolvedAuthKeyID: permAuthKeyID,
		hasResolved:       true,
		userID:            userID,
		authorizations: []domain.Authorization{{
			AuthKeyID:     permAuthKeyID,
			UserID:        userID,
			Layer:         225,
			DeviceModel:   "nubiaNX629J",
			Platform:      string(ClientTypeAndroid),
			SystemVersion: "SDK 30",
			AppVersion:    "12.7.3 (67509) pbeta",
		}},
	}
	r := New(Config{DC: 2, IP: "127.0.0.1", Port: 2398}, Deps{
		Auth: auth,
	}, zaptest.NewLogger(t), clock.System)

	var warm bin.Buffer
	if err := (&tg.HelpGetConfigRequest{}).Encode(&warm); err != nil {
		t.Fatalf("encode warm request: %v", err)
	}
	if _, err := r.Dispatch(context.Background(), permAuthKeyID, 11, &warm); err != nil {
		t.Fatalf("dispatch warm perm request: %v", err)
	}
	if got, ok := r.NegotiatedLayer(permAuthKeyID, 11); ok || got != currentClientLayer {
		t.Fatalf("perm metadata layer = (%d,%v), want (%d,false)", got, ok, currentClientLayer)
	}

	var firstTempRequest bin.Buffer
	if err := (&tg.HelpGetConfigRequest{}).Encode(&firstTempRequest); err != nil {
		t.Fatalf("encode temp request: %v", err)
	}
	const tempSessionID = int64(5579025282411299519)
	if _, err := r.Dispatch(context.Background(), rawAuthKeyID, tempSessionID, &firstTempRequest); err != nil {
		t.Fatalf("dispatch first temp request: %v", err)
	}
	if got, ok := r.NegotiatedLayer(rawAuthKeyID, tempSessionID); ok || got != currentClientLayer {
		t.Fatalf("raw temp metadata layer = (%d,%v), want (%d,false)", got, ok, currentClientLayer)
	}
	if got, ok := r.NegotiatedLayer(rawAuthKeyID, tempSessionID+1); ok || got != currentClientLayer {
		t.Fatalf("raw temp new-session layer = (%d,%v), want (%d,false)", got, ok, currentClientLayer)
	}
	hotCtx := WithAuthKeyID(
		WithSessionID(WithRawAuthKeyID(context.Background(), rawAuthKeyID), tempSessionID),
		permAuthKeyID,
	)
	info, ok, stored := r.clientSessionInfo(hotCtx)
	if !ok || info.layer != 0 || !info.hasClientInfo || info.clientInfo.ClientType() != ClientTypeAndroid {
		t.Fatalf("cached temp session info = (%+v,%v), want Android metadata with unknown Layer", info, ok)
	}
	if !stored {
		t.Fatalf("cached temp session metadata is not fully materialized for hot path")
	}
	if wrote := r.rememberClientSessionInfoIfMissing(hotCtx, info); wrote {
		t.Fatalf("hot path rewrote already materialized client session metadata")
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
	if got := intLogField(fields["layer"]); got != 0 {
		t.Fatalf("logged layer = %d fields=%v, want 0", got, fields)
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

func TestDispatchCachesMissingAuthKeyClientMetadataLookup(t *testing.T) {
	authKeyID := [8]byte{0x68, 0x25, 0xc2, 0xee, 0xf8, 0x82, 0xef, 0x72}
	auth := &captureAuthService{}
	r := New(Config{DC: 2, IP: "127.0.0.1", Port: 2398}, Deps{
		Auth: auth,
	}, zaptest.NewLogger(t), clock.System)

	for _, sessionID := range []int64{101, 102, 103} {
		var in bin.Buffer
		if err := (&tg.HelpGetConfigRequest{}).Encode(&in); err != nil {
			t.Fatalf("encode request: %v", err)
		}
		if _, err := r.Dispatch(context.Background(), authKeyID, sessionID, &in); err != nil {
			t.Fatalf("dispatch session %d: %v", sessionID, err)
		}
	}

	if auth.authKeyInfoLookups != 1 {
		t.Fatalf("auth key client info lookups = %d, want 1 cached miss", auth.authKeyInfoLookups)
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
	expiresAt := int(time.Now().Add(time.Hour).Unix())
	authKeys := memory.NewAuthKeyStore()
	if err := authKeys.Save(context.Background(), store.AuthKeyData{ID: tempAuthKeyID, ExpiresAt: expiresAt}); err != nil {
		t.Fatalf("save temporary auth key: %v", err)
	}
	if err := authKeys.Save(context.Background(), store.AuthKeyData{ID: permAuthKeyID}); err != nil {
		t.Fatalf("save permanent auth key: %v", err)
	}
	tempBindings := memory.NewTempAuthKeyBindingStore(authKeys)
	if err := tempBindings.Save(context.Background(), domain.TempAuthKeyBinding{
		TempAuthKeyID: tempAuthKeyID,
		PermAuthKeyID: int64(binary.LittleEndian.Uint64(permAuthKeyID[:])),
		ExpiresAt:     expiresAt,
	}); err != nil {
		t.Fatalf("save temp binding: %v", err)
	}
	sessions := &captureSessions{}
	r := New(Config{}, Deps{
		Auth:     appauth.NewService(nil, nil, nil, authKeys, tempBindings, "12345"),
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
		LangPack: seededLangPackService(t),
	}, zaptest.NewLogger(t), clock.System)

	tests := []struct {
		name string
		req  bin.Object
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
		{name: "help.getAppUpdate", req: &tg.HelpGetAppUpdateRequest{}},
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
		{name: "account.updateDeviceLocked", req: &tg.AccountUpdateDeviceLockedRequest{Period: 60}},
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
		{name: "messages.getRecentLocations", req: &tg.MessagesGetRecentLocationsRequest{Peer: &tg.InputPeerUser{UserID: domain.OfficialSystemUserID, AccessHash: domain.OfficialSystemUser().AccessHash}, Limit: 20}},
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
			ctx := WithUserID(context.Background(), 1000000001)
			result, method := dispatchExactLayerRPCTest(t, r, ctx, tlprofile.ProfileCanonical, tt.req)
			if method != tt.name {
				t.Fatalf("dispatched method = %q, want %q", method, tt.name)
			}
			var out bin.Buffer
			if err := result.Encode(&out); err != nil {
				t.Fatalf("encode response: %v", err)
			}
			if out.Len() == 0 {
				t.Fatal("encoded response is empty")
			}
		})
	}
}

func dispatchExactLayerRPCTest(
	t *testing.T,
	r *Router,
	ctx context.Context,
	profile tlprofile.Profile,
	request bin.Object,
) (tlprofile.Result, string) {
	t.Helper()
	body := encodeExactLayerRPC(t, profile, request)
	admitted, err := r.AdmitLayer(profile, &body, tlprofile.Limits{})
	if err != nil {
		t.Fatalf("admit exact Layer %d request: %v", profile, err)
	}
	if body.Len() != 0 {
		t.Fatalf("exact Layer %d admission left %d bytes", profile, body.Len())
	}
	result, method, err := r.DispatchAdmitted(ctx, [8]byte{}, 0, 0, 0, admitted)
	if err != nil {
		t.Fatalf("dispatch exact Layer %d request: %v", profile, err)
	}
	if result == nil {
		t.Fatalf("dispatch exact Layer %d request returned nil result", profile)
	}
	return result, method
}

func TestMessagesSearchGlobalExactLayerProfiles(t *testing.T) {
	r := New(Config{}, Deps{}, zaptest.NewLogger(t), clock.System)
	ctx := WithUserID(context.Background(), 1000000001)
	for _, tc := range []struct {
		profile tlprofile.Profile
		wireID  uint32
	}{
		{profile: tlprofile.Profile227, wireID: 0x4bc6589a},
		{profile: tlprofile.Profile228, wireID: 0x6126a43c},
	} {
		t.Run(fmt.Sprintf("layer_%d", tc.profile), func(t *testing.T) {
			request := &tg.MessagesSearchGlobalRequest{
				Q:          "login",
				Filter:     &tg.InputMessagesFilterEmpty{},
				OffsetPeer: &tg.InputPeerEmpty{},
				Limit:      20,
			}
			body := encodeExactLayerRPC(t, tc.profile, request)
			if got := binary.LittleEndian.Uint32(body.Raw()); got != tc.wireID {
				t.Fatalf("Layer %d wire id = %#x, want %#x", tc.profile, got, tc.wireID)
			}
			admitted, err := r.AdmitLayer(tc.profile, &body, tlprofile.Limits{})
			if err != nil {
				t.Fatalf("admit Layer %d searchGlobal: %v", tc.profile, err)
			}
			if body.Len() != 0 {
				t.Fatalf("Layer %d admission left %d bytes", tc.profile, body.Len())
			}
			call := admitted.Call()
			if call.Profile() != tc.profile || call.WireID() != tc.wireID || call.Method() != tlprofile.SemanticMethodMessagesSearchGlobal {
				t.Fatalf("Layer %d call = profile:%d wire:%#x semantic:%#x", tc.profile, call.Profile(), call.WireID(), call.Method())
			}
			result, method, err := r.DispatchAdmitted(ctx, [8]byte{}, 0, 0, 0, admitted)
			if err != nil {
				t.Fatalf("dispatch Layer %d searchGlobal: %v", tc.profile, err)
			}
			if method != "messages.searchGlobal" || result == nil {
				t.Fatalf("Layer %d result = method:%q value:%T", tc.profile, method, result)
			}
			bound := result.Prepared().Call()
			if bound.Identity() != call.Identity() || bound.Profile() != tc.profile || bound.WireID() != tc.wireID {
				t.Fatalf("Layer %d result binding = profile:%d wire:%#x identity:%#v, want request-bound %#v", tc.profile, bound.Profile(), bound.WireID(), bound.Identity(), call.Identity())
			}
			var encoded bin.Buffer
			if err := result.Encode(&encoded); err != nil {
				t.Fatalf("encode Layer %d result: %v", tc.profile, err)
			}
			if encoded.Len() == 0 {
				t.Fatalf("Layer %d result encoded empty", tc.profile)
			}
		})
	}
}

func TestMessagesSearchGlobalCommunityProjectionFailsClosedForLayer227(t *testing.T) {
	request := &tg.MessagesSearchGlobalRequest{
		Q:          "scoped",
		Filter:     &tg.InputMessagesFilterEmpty{},
		OffsetPeer: &tg.InputPeerEmpty{},
		Limit:      20,
	}
	request.SetCommunity(&tg.InputChannel{ChannelID: 42, AccessHash: 84})

	// The Layer 228 shape is valid and carries the new field.
	body228 := encodeExactLayerRPC(t, tlprofile.Profile228, request)
	if got := binary.LittleEndian.Uint32(body228.Raw()); got != 0x6126a43c {
		t.Fatalf("Layer 228 wire id = %#x, want %#x", got, uint32(0x6126a43c))
	}
	if _, err := New(Config{}, Deps{}, zaptest.NewLogger(t), clock.System).AdmitLayer(tlprofile.Profile228, &body228, tlprofile.Limits{}); err != nil {
		t.Fatalf("admit Layer 228 community search: %v", err)
	}
	if body228.Len() != 0 {
		t.Fatalf("Layer 228 community admission left %d bytes", body228.Len())
	}

	var body227 bin.Buffer
	if err := tlprofile.EncodeObject(tlprofile.Profile227, request, &body227); err == nil {
		t.Fatal("Layer 227 projection accepted a Layer 228-only community scope")
	}
	if body227.Len() != 0 {
		t.Fatalf("failed Layer 227 projection emitted %d partial bytes", body227.Len())
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
