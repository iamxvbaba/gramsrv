package rpc

import (
	"context"
	"crypto/sha256"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/clock"
	"github.com/gotd/td/proto"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
	"go.uber.org/zap/zaptest"

	appmessages "telesrv/internal/app/messages"
	appphone "telesrv/internal/app/phone"
	appusers "telesrv/internal/app/users"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

// phonePushRecord 记录一次定向推送（目标用户、被排除的 session、载荷）。
type phonePushRecord struct {
	userID         int64
	targetSession  int64
	excludeSession int64
	msg            bin.Encoder
}

// phoneCaptureSessions 是带完整推送日志的 SessionBinder fake（captureSessions 只留最后一条）。
type phoneCaptureSessions struct {
	mu  sync.Mutex
	log []phonePushRecord
}

func (s *phoneCaptureSessions) BindAuthKeyForSession([8]byte, int64, [8]byte) {}
func (s *phoneCaptureSessions) AuthKeyIDForSession([8]byte, int64) ([8]byte, bool) {
	return [8]byte{}, false
}
func (s *phoneCaptureSessions) BindUserForAuthKey([8]byte, int64, int64) {}
func (s *phoneCaptureSessions) UserIDResolvedForAuthKey([8]byte, int64) (int64, bool) {
	return 0, false
}
func (s *phoneCaptureSessions) UnbindAuthKey([8]byte) int                         { return 0 }
func (s *phoneCaptureSessions) SetReceivesUpdatesForAuthKey([8]byte, int64, bool) {}

func (s *phoneCaptureSessions) PushToSessionForAuthKey(_ context.Context, _ [8]byte, sessionID int64, _ proto.MessageType, msg bin.Encoder) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.log = append(s.log, phonePushRecord{targetSession: sessionID, msg: msg})
	return nil
}

func (s *phoneCaptureSessions) PushToUserExceptAuthKeySession(_ context.Context, userID int64, _ [8]byte, excludeSessionID int64, _ proto.MessageType, msg bin.Encoder) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.log = append(s.log, phonePushRecord{userID: userID, excludeSession: excludeSessionID, msg: msg})
	return 1, nil
}

func (s *phoneCaptureSessions) records() []phonePushRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]phonePushRecord(nil), s.log...)
}

func (s *phoneCaptureSessions) reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.log = nil
}

// stubPrivacy 只为 CanSee 服务；其余接口方法不在通话链路使用。
type stubPrivacy struct {
	deny map[domain.PrivacyKey]bool
}

func (p stubPrivacy) GetRules(context.Context, int64, domain.PrivacyKey) (domain.PrivacyRules, error) {
	return domain.PrivacyRules{}, nil
}

func (p stubPrivacy) SetRules(context.Context, int64, domain.PrivacyKey, []domain.PrivacyRule) (domain.PrivacyRules, error) {
	return domain.PrivacyRules{}, nil
}

func (p stubPrivacy) AddAllowUser(context.Context, int64, domain.PrivacyKey, int64) (domain.PrivacyRules, bool, error) {
	return domain.PrivacyRules{}, false, nil
}

func (p stubPrivacy) CanSee(_ context.Context, _, _ int64, key domain.PrivacyKey) (bool, error) {
	return !p.deny[key], nil
}

type phoneFixture struct {
	t        *testing.T
	ctx      context.Context
	router   *Router
	sessions *phoneCaptureSessions
	caller   domain.User
	callee   domain.User
}

const (
	phoneCallerSession = int64(101)
	phoneCalleeSession = int64(202)
)

func newPhoneFixture(t *testing.T, privacy PrivacyService) *phoneFixture {
	t.Helper()
	ctx := context.Background()
	userStore := memory.NewUserStore()
	sessions := &phoneCaptureSessions{}
	router := New(Config{CallSignalingMaxBytes: 1024}, Deps{
		Users:    appusers.NewService(userStore),
		Privacy:  privacy,
		Phone:    appphone.NewService(appphone.Config{}),
		Sessions: sessions,
	}, zaptest.NewLogger(t), clock.System)
	f := &phoneFixture{t: t, ctx: ctx, router: router, sessions: sessions}
	mkUser := func(hash int64, phone, name string) domain.User {
		u, err := userStore.Create(ctx, domain.User{AccessHash: hash, Phone: phone, FirstName: name})
		if err != nil {
			t.Fatalf("create user %s: %v", name, err)
		}
		return u
	}
	f.caller = mkUser(1001, "13800000001", "Caller")
	f.callee = mkUser(1002, "13800000002", "Callee")
	return f
}

func (f *phoneFixture) callerCtx() context.Context {
	return WithSessionID(WithUserID(f.ctx, f.caller.ID), phoneCallerSession)
}

func (f *phoneFixture) calleeCtx() context.Context {
	return WithSessionID(WithUserID(f.ctx, f.callee.ID), phoneCalleeSession)
}

func phoneTestProtocol() tg.PhoneCallProtocol {
	p := tg.PhoneCallProtocol{MinLayer: 65, MaxLayer: 92, LibraryVersions: []string{"11.0.0", "9.0.0"}}
	p.SetUDPP2P(true)
	p.SetUDPReflector(true)
	return p
}

func phoneTestKeys() (ga, gaHash, gb []byte) {
	ga = make([]byte, 256)
	for i := range ga {
		ga[i] = byte(i + 3)
	}
	h := sha256.Sum256(ga)
	gb = make([]byte, 256)
	for i := range gb {
		gb[i] = byte(200 - i%150)
	}
	return ga, h[:], gb
}

func assertPhoneRPCErr(t *testing.T, err error, wantType string) {
	t.Helper()
	var rpcErr *tgerr.Error
	if !errors.As(err, &rpcErr) || rpcErr.Type != wantType {
		t.Fatalf("err = %v, want rpc error %s", err, wantType)
	}
}

// phoneCallPayload 从捕获的推送里取出 updatePhoneCall 的载荷。
func phoneCallPayload(t *testing.T, rec phonePushRecord) tg.PhoneCallClass {
	t.Helper()
	updates, ok := rec.msg.(*tg.Updates)
	if !ok || len(updates.Updates) != 1 {
		t.Fatalf("pushed msg = %T %+v, want single-update tg.Updates", rec.msg, rec.msg)
	}
	upd, ok := updates.Updates[0].(*tg.UpdatePhoneCall)
	if !ok {
		t.Fatalf("pushed update = %T, want UpdatePhoneCall", updates.Updates[0])
	}
	return upd.PhoneCall
}

func TestPhoneCallRPCHappyPath(t *testing.T) {
	f := newPhoneFixture(t, stubPrivacy{})
	ga, gaHash, gb := phoneTestKeys()

	// --- requestCall（主叫） ---
	res, err := f.router.onPhoneRequestCall(f.callerCtx(), &tg.PhoneRequestCallRequest{
		UserID:   &tg.InputUser{UserID: f.callee.ID, AccessHash: f.callee.AccessHash},
		RandomID: 42,
		GAHash:   gaHash,
		Protocol: phoneTestProtocol(),
	})
	if err != nil {
		t.Fatalf("requestCall: %v", err)
	}
	waiting, ok := res.PhoneCall.(*tg.PhoneCallWaiting)
	if !ok {
		t.Fatalf("requestCall result = %T, want PhoneCallWaiting", res.PhoneCall)
	}
	if waiting.AdminID != f.caller.ID || waiting.ParticipantID != f.callee.ID {
		t.Fatalf("waiting identities = %+v", waiting)
	}
	if len(res.Users) != 2 {
		t.Fatalf("requestCall users = %d, want 2", len(res.Users))
	}
	callID, accessHash := waiting.ID, waiting.AccessHash

	pushes := f.sessions.records()
	if len(pushes) != 1 || pushes[0].userID != f.callee.ID {
		t.Fatalf("requestCall pushes = %+v, want one to callee", pushes)
	}
	requested, ok := phoneCallPayload(t, pushes[0]).(*tg.PhoneCallRequested)
	if !ok {
		t.Fatalf("callee payload = %T, want PhoneCallRequested", phoneCallPayload(t, pushes[0]))
	}
	if string(requested.GAHash) != string(gaHash) {
		t.Fatalf("g_a_hash must be relayed verbatim")
	}
	f.sessions.reset()

	// --- receivedCall（被叫上报触达）→ 主叫收 receive_date ---
	if ok, err := f.router.onPhoneReceivedCall(f.calleeCtx(), tg.InputPhoneCall{ID: callID, AccessHash: accessHash}); err != nil || !ok {
		t.Fatalf("receivedCall = %v err=%v", ok, err)
	}
	pushes = f.sessions.records()
	if len(pushes) != 1 || pushes[0].userID != f.caller.ID {
		t.Fatalf("receivedCall pushes = %+v, want one to caller", pushes)
	}
	ringing, ok := phoneCallPayload(t, pushes[0]).(*tg.PhoneCallWaiting)
	if !ok || ringing.ReceiveDate == 0 {
		// ⚠ P1-2：缺 receive_date 推送时主叫只有 20s 接听窗口。
		t.Fatalf("caller payload = %+v, want waiting with receive_date", phoneCallPayload(t, pushes[0]))
	}
	f.sessions.reset()

	// --- acceptCall（被叫赢家设备） ---
	acceptRes, err := f.router.onPhoneAcceptCall(f.calleeCtx(), &tg.PhoneAcceptCallRequest{
		Peer:     tg.InputPhoneCall{ID: callID, AccessHash: accessHash},
		GB:       gb,
		Protocol: phoneTestProtocol(),
	})
	if err != nil {
		t.Fatalf("acceptCall: %v", err)
	}
	if _, ok := acceptRes.PhoneCall.(*tg.PhoneCallWaiting); !ok {
		t.Fatalf("acceptCall result = %T, want PhoneCallWaiting (callee view)", acceptRes.PhoneCall)
	}
	pushes = f.sessions.records()
	if len(pushes) != 2 {
		t.Fatalf("acceptCall pushes = %d, want 2 (caller accepted + callee stop-ringing)", len(pushes))
	}
	accepted, ok := phoneCallPayload(t, pushes[0]).(*tg.PhoneCallAccepted)
	if !ok || pushes[0].userID != f.caller.ID {
		t.Fatalf("first accept push = %+v payload %T, want PhoneCallAccepted to caller", pushes[0], phoneCallPayload(t, pushes[0]))
	}
	if string(accepted.GB) != string(gb) {
		t.Fatalf("accepted.g_b must be relayed verbatim")
	}
	// 协商优先 "9.0.0"（不是语义化最高）：DrKLO 视频 gate 是字符串字典序比较，
	// "1x.0.0" < "2.7.7" 会关掉 Android 视频；见 app/phone preferredVersions。
	if got := accepted.Protocol.LibraryVersions; len(got) != 1 || got[0] != "9.0.0" {
		t.Fatalf("negotiated library_versions = %v, want preferred [9.0.0]", got)
	}
	// ⚠ P0-1：被叫其它设备必须收合成 phoneCallDiscarded（无 reason），绝不能是 accepted。
	stop, ok := phoneCallPayload(t, pushes[1]).(*tg.PhoneCallDiscarded)
	if !ok || pushes[1].userID != f.callee.ID {
		t.Fatalf("second accept push = %+v payload %T, want PhoneCallDiscarded to callee devices", pushes[1], phoneCallPayload(t, pushes[1]))
	}
	if pushes[1].excludeSession != phoneCalleeSession {
		t.Fatalf("stop-ringing must exclude the accepting session, got exclude=%d", pushes[1].excludeSession)
	}
	if _, hasReason := stop.GetReason(); hasReason || stop.NeedRating || stop.NeedDebug {
		t.Fatalf("stop-ringing payload = %+v, want reason-less, need_*=false", stop)
	}
	f.sessions.reset()

	// --- confirmCall（主叫揭示 g_a） ---
	confirmRes, err := f.router.onPhoneConfirmCall(f.callerCtx(), &tg.PhoneConfirmCallRequest{
		Peer:           tg.InputPhoneCall{ID: callID, AccessHash: accessHash},
		GA:             ga,
		KeyFingerprint: 0xCAFE,
		Protocol:       phoneTestProtocol(),
	})
	if err != nil {
		t.Fatalf("confirmCall: %v", err)
	}
	callerView, ok := confirmRes.PhoneCall.(*tg.PhoneCall)
	if !ok || string(callerView.GAOrB) != string(gb) {
		t.Fatalf("confirm result = %T g_a_or_b mismatch, 主叫视角必须是 g_b", confirmRes.PhoneCall)
	}
	if callerView.Connections == nil || len(callerView.Connections) != 0 {
		t.Fatalf("P1 connections = %v, want empty non-nil", callerView.Connections)
	}
	if !callerView.P2PAllowed {
		t.Fatalf("p2p_allowed must be true in P1")
	}
	pushes = f.sessions.records()
	if len(pushes) != 1 || pushes[0].userID != f.callee.ID {
		t.Fatalf("confirm pushes = %+v, want one to callee", pushes)
	}
	calleeView, ok := phoneCallPayload(t, pushes[0]).(*tg.PhoneCall)
	if !ok || string(calleeView.GAOrB) != string(ga) || calleeView.KeyFingerprint != 0xCAFE {
		t.Fatalf("callee confirm payload = %+v, 被叫视角必须是 g_a + fingerprint", calleeView)
	}
	f.sessions.reset()

	// --- sendSignalingData 双向透传 ---
	okSig, err := f.router.onPhoneSendSignalingData(f.callerCtx(), &tg.PhoneSendSignalingDataRequest{
		Peer: tg.InputPhoneCall{ID: callID, AccessHash: accessHash},
		Data: []byte("sdp-offer"),
	})
	if err != nil || !okSig {
		t.Fatalf("sendSignalingData = %v err=%v", okSig, err)
	}
	pushes = f.sessions.records()
	if len(pushes) != 1 || pushes[0].targetSession != phoneCalleeSession {
		t.Fatalf("signaling pushes = %+v, want one to callee session", pushes)
	}
	sigUpdates := pushes[0].msg.(*tg.Updates)
	sig, ok := sigUpdates.Updates[0].(*tg.UpdatePhoneCallSignalingData)
	if !ok || sig.PhoneCallID != callID || string(sig.Data) != "sdp-offer" {
		t.Fatalf("signaling payload = %+v", sigUpdates.Updates[0])
	}
	f.sessions.reset()

	// --- discardCall（主叫挂断，duration 在 Confirmed 后被采纳） ---
	discardRes, err := f.router.onPhoneDiscardCall(f.callerCtx(), &tg.PhoneDiscardCallRequest{
		Peer:     tg.InputPhoneCall{ID: callID, AccessHash: accessHash},
		Duration: 42,
		Reason:   &tg.PhoneCallDiscardReasonHangup{},
	})
	if err != nil {
		t.Fatalf("discardCall: %v", err)
	}
	respUpdates, ok := discardRes.(*tg.Updates)
	if !ok || len(respUpdates.Updates) != 1 {
		t.Fatalf("discard result = %T %+v", discardRes, discardRes)
	}
	discarded, ok := respUpdates.Updates[0].(*tg.UpdatePhoneCall).PhoneCall.(*tg.PhoneCallDiscarded)
	if !ok {
		t.Fatalf("discard payload = %T", respUpdates.Updates[0])
	}
	if d, _ := discarded.GetDuration(); d != 42 {
		t.Fatalf("discard duration = %d, want 42", d)
	}
	if discarded.NeedRating || discarded.NeedDebug {
		t.Fatalf("need_rating/need_debug must stay false")
	}
	pushes = f.sessions.records()
	if len(pushes) != 2 || pushes[0].userID != f.caller.ID || pushes[1].userID != f.callee.ID {
		t.Fatalf("discard pushes = %+v, want caller(other devices)+callee", pushes)
	}
	f.sessions.reset()

	// --- 终态尾包：信令静默吞掉、重复 discard 幂等 ---
	if okSig, err := f.router.onPhoneSendSignalingData(f.calleeCtx(), &tg.PhoneSendSignalingDataRequest{
		Peer: tg.InputPhoneCall{ID: callID, AccessHash: accessHash},
		Data: []byte("late"),
	}); err != nil || !okSig {
		t.Fatalf("late signaling = %v err=%v, want silent true", okSig, err)
	}
	if len(f.sessions.records()) != 0 {
		t.Fatalf("late signaling must not forward")
	}
	if _, err := f.router.onPhoneDiscardCall(f.calleeCtx(), &tg.PhoneDiscardCallRequest{
		Peer:   tg.InputPhoneCall{ID: callID, AccessHash: accessHash},
		Reason: &tg.PhoneCallDiscardReasonBusy{},
	}); err != nil {
		t.Fatalf("idempotent discard: %v", err)
	}
	if len(f.sessions.records()) != 0 {
		t.Fatalf("idempotent discard must not re-push")
	}

	// --- 评分/调试上报打发掉 ---
	if _, err := f.router.onPhoneSetCallRating(f.callerCtx(), &tg.PhoneSetCallRatingRequest{
		Peer: tg.InputPhoneCall{ID: callID, AccessHash: accessHash}, Rating: 5,
	}); err != nil {
		t.Fatalf("setCallRating: %v", err)
	}
	if ok, err := f.router.onPhoneSaveCallDebug(f.callerCtx(), &tg.PhoneSaveCallDebugRequest{
		Peer: tg.InputPhoneCall{ID: callID, AccessHash: accessHash}, Debug: tg.DataJSON{Data: "{}"},
	}); err != nil || !ok {
		t.Fatalf("saveCallDebug = %v err=%v", ok, err)
	}
}

func TestPhoneCallRPCValidation(t *testing.T) {
	f := newPhoneFixture(t, stubPrivacy{})
	_, gaHash, gb := phoneTestKeys()

	// 自呼。
	_, err := f.router.onPhoneRequestCall(f.callerCtx(), &tg.PhoneRequestCallRequest{
		UserID: &tg.InputUser{UserID: f.caller.ID, AccessHash: f.caller.AccessHash},
		GAHash: gaHash, Protocol: phoneTestProtocol(),
	})
	assertPhoneRPCErr(t, err, "USER_ID_INVALID")

	// 协议层级非法。
	badProto := phoneTestProtocol()
	badProto.MinLayer = 93
	_, err = f.router.onPhoneRequestCall(f.callerCtx(), &tg.PhoneRequestCallRequest{
		UserID: &tg.InputUser{UserID: f.callee.ID, AccessHash: f.callee.AccessHash},
		GAHash: gaHash, Protocol: badProto,
	})
	assertPhoneRPCErr(t, err, "CALL_PROTOCOL_LAYER_INVALID")

	// g_a_hash 长度非法。
	_, err = f.router.onPhoneRequestCall(f.callerCtx(), &tg.PhoneRequestCallRequest{
		UserID: &tg.InputUser{UserID: f.callee.ID, AccessHash: f.callee.AccessHash},
		GAHash: []byte("short"), Protocol: phoneTestProtocol(),
	})
	assertPhoneRPCErr(t, err, "CALL_PROTOCOL_FLAGS_INVALID")

	// 建一通合法通话供后续状态校验。
	res, err := f.router.onPhoneRequestCall(f.callerCtx(), &tg.PhoneRequestCallRequest{
		UserID: &tg.InputUser{UserID: f.callee.ID, AccessHash: f.callee.AccessHash},
		GAHash: gaHash, Protocol: phoneTestProtocol(), RandomID: 7,
	})
	if err != nil {
		t.Fatalf("requestCall: %v", err)
	}
	waiting := res.PhoneCall.(*tg.PhoneCallWaiting)
	peer := tg.InputPhoneCall{ID: waiting.ID, AccessHash: waiting.AccessHash}
	f.sessions.reset()

	// 主叫不能 accept 自己发起的通话。
	_, err = f.router.onPhoneAcceptCall(f.callerCtx(), &tg.PhoneAcceptCallRequest{Peer: peer, GB: gb, Protocol: phoneTestProtocol()})
	assertPhoneRPCErr(t, err, "CALL_PEER_INVALID")

	// 信令超限。
	_, err = f.router.onPhoneSendSignalingData(f.callerCtx(), &tg.PhoneSendSignalingDataRequest{
		Peer: peer, Data: make([]byte, 2048),
	})
	assertPhoneRPCErr(t, err, "DATA_INVALID")

	// accept 后重复 accept。
	if _, err := f.router.onPhoneAcceptCall(f.calleeCtx(), &tg.PhoneAcceptCallRequest{Peer: peer, GB: gb, Protocol: phoneTestProtocol()}); err != nil {
		t.Fatalf("accept: %v", err)
	}
	_, err = f.router.onPhoneAcceptCall(f.calleeCtx(), &tg.PhoneAcceptCallRequest{Peer: peer, GB: gb, Protocol: phoneTestProtocol()})
	assertPhoneRPCErr(t, err, "CALL_ALREADY_ACCEPTED")
	f.sessions.reset()

	// g_a 与承诺不符：CALL_PEER_INVALID 且双方收到强制 discarded。
	wrongGA := make([]byte, 256)
	wrongGA[0] = 9
	_, err = f.router.onPhoneConfirmCall(f.callerCtx(), &tg.PhoneConfirmCallRequest{Peer: peer, GA: wrongGA, KeyFingerprint: 1, Protocol: phoneTestProtocol()})
	assertPhoneRPCErr(t, err, "CALL_PEER_INVALID")
	pushes := f.sessions.records()
	if len(pushes) != 2 {
		t.Fatalf("forced discard pushes = %d, want both sides", len(pushes))
	}
	for _, rec := range pushes {
		if _, ok := phoneCallPayload(t, rec).(*tg.PhoneCallDiscarded); !ok {
			t.Fatalf("forced discard payload to %d = %T", rec.userID, phoneCallPayload(t, rec))
		}
	}
}

func TestPhoneCallRPCPrivacyGate(t *testing.T) {
	f := newPhoneFixture(t, stubPrivacy{deny: map[domain.PrivacyKey]bool{domain.PrivacyKeyPhoneCall: true}})
	_, gaHash, _ := phoneTestKeys()
	_, err := f.router.onPhoneRequestCall(f.callerCtx(), &tg.PhoneRequestCallRequest{
		UserID: &tg.InputUser{UserID: f.callee.ID, AccessHash: f.callee.AccessHash},
		GAHash: gaHash, Protocol: phoneTestProtocol(),
	})
	assertPhoneRPCErr(t, err, "USER_PRIVACY_RESTRICTED")
	if len(f.sessions.records()) != 0 {
		t.Fatalf("privacy-restricted request must not push")
	}
}

func TestPhoneUserFullCallFlags(t *testing.T) {
	t.Run("allowed", func(t *testing.T) {
		f := newPhoneFixture(t, stubPrivacy{})
		full, err := f.router.onUsersGetFullUser(f.callerCtx(), &tg.InputUser{UserID: f.callee.ID, AccessHash: f.callee.AccessHash})
		if err != nil {
			t.Fatalf("getFullUser: %v", err)
		}
		if !full.FullUser.PhoneCallsAvailable || !full.FullUser.VideoCallsAvailable || full.FullUser.PhoneCallsPrivate {
			t.Fatalf("call flags = %+v, want available && !private", full.FullUser)
		}
	})
	t.Run("denied", func(t *testing.T) {
		f := newPhoneFixture(t, stubPrivacy{deny: map[domain.PrivacyKey]bool{
			domain.PrivacyKeyPhoneCall: true,
			domain.PrivacyKeyPhoneP2P:  true,
		}})
		full, err := f.router.onUsersGetFullUser(f.callerCtx(), &tg.InputUser{UserID: f.callee.ID, AccessHash: f.callee.AccessHash})
		if err != nil {
			t.Fatalf("getFullUser: %v", err)
		}
		if full.FullUser.PhoneCallsAvailable || full.FullUser.VideoCallsAvailable || !full.FullUser.PhoneCallsPrivate {
			t.Fatalf("call flags = %+v, want unavailable && private", full.FullUser)
		}
	})
}

func TestMessagesGetDhConfig(t *testing.T) {
	f := newPhoneFixture(t, stubPrivacy{})

	res, err := f.router.onMessagesGetDhConfig(f.callerCtx(), &tg.MessagesGetDhConfigRequest{Version: 0, RandomLength: 256})
	if err != nil {
		t.Fatalf("getDhConfig: %v", err)
	}
	full, ok := res.(*tg.MessagesDhConfig)
	if !ok {
		t.Fatalf("result = %T, want MessagesDhConfig", res)
	}
	if full.G != appphone.DHG || full.Version != appphone.DHConfigVersion {
		t.Fatalf("dh config g=%d version=%d", full.G, full.Version)
	}
	if len(full.P) != 256 {
		t.Fatalf("prime length = %d, want 256 (2048-bit)", len(full.P))
	}
	if string(full.P) != string(appphone.DHPrime()) {
		t.Fatalf("prime must equal the official srp prime")
	}
	if len(full.Random) != 256 {
		t.Fatalf("random length = %d, want exactly requested 256", len(full.Random))
	}

	res, err = f.router.onMessagesGetDhConfig(f.callerCtx(), &tg.MessagesGetDhConfigRequest{Version: appphone.DHConfigVersion, RandomLength: 32})
	if err != nil {
		t.Fatalf("getDhConfig cached: %v", err)
	}
	notModified, ok := res.(*tg.MessagesDhConfigNotModified)
	if !ok {
		t.Fatalf("cached result = %T, want MessagesDhConfigNotModified", res)
	}
	if len(notModified.Random) != 32 {
		t.Fatalf("cached random length = %d, want 32", len(notModified.Random))
	}

	// 未登录 → 401。
	_, err = f.router.onMessagesGetDhConfig(f.ctx, &tg.MessagesGetDhConfigRequest{Version: 0, RandomLength: 256})
	assertPhoneRPCErr(t, err, "AUTH_KEY_UNREGISTERED")
}

// phoneTestClock 是可推进的测试时钟（fixedClock 不可变，dispatcher 测试需要推进）。
type phoneTestClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *phoneTestClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *phoneTestClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func (c *phoneTestClock) Timer(d time.Duration) clock.Timer   { return clock.System.Timer(d) }
func (c *phoneTestClock) Ticker(d time.Duration) clock.Ticker { return clock.System.Ticker(d) }

// phoneCaptureMessages 只捕获 SendPrivateText；其余 MessagesService 方法不在通话链路使用。
type phoneCaptureMessages struct {
	MessagesService
	mu   sync.Mutex
	sent []domain.SendPrivateTextRequest
}

func (m *phoneCaptureMessages) SendPrivateText(_ context.Context, _ int64, req domain.SendPrivateTextRequest) (domain.SendPrivateTextResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sent = append(m.sent, req)
	return domain.SendPrivateTextResult{}, nil
}

func (m *phoneCaptureMessages) records() []domain.SendPrivateTextRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]domain.SendPrivateTextRequest(nil), m.sent...)
}

func newPhoneFixtureFull(t *testing.T, clk clock.Clock, messages *phoneCaptureMessages) *phoneFixture {
	t.Helper()
	ctx := context.Background()
	userStore := memory.NewUserStore()
	sessions := &phoneCaptureSessions{}
	deps := Deps{
		Users:    appusers.NewService(userStore),
		Privacy:  stubPrivacy{},
		Phone:    appphone.NewService(appphone.Config{}, appphone.WithClock(clk)),
		Sessions: sessions,
	}
	if messages != nil {
		deps.Messages = messages
	}
	router := New(Config{CallSignalingMaxBytes: 1024}, deps, zaptest.NewLogger(t), clk)
	f := &phoneFixture{t: t, ctx: ctx, router: router, sessions: sessions}
	mkUser := func(hash int64, phone, name string) domain.User {
		u, err := userStore.Create(ctx, domain.User{AccessHash: hash, Phone: phone, FirstName: name})
		if err != nil {
			t.Fatalf("create user %s: %v", name, err)
		}
		return u
	}
	f.caller = mkUser(1001, "13800000001", "Caller")
	f.callee = mkUser(1002, "13800000002", "Callee")
	return f
}

// establishPhoneCall 把一通通话推进到 Confirmed，返回 peer 引用。
func establishPhoneCall(t *testing.T, f *phoneFixture) tg.InputPhoneCall {
	t.Helper()
	ga, gaHash, gb := phoneTestKeys()
	res, err := f.router.onPhoneRequestCall(f.callerCtx(), &tg.PhoneRequestCallRequest{
		UserID: &tg.InputUser{UserID: f.callee.ID, AccessHash: f.callee.AccessHash},
		GAHash: gaHash, Protocol: phoneTestProtocol(), RandomID: 9,
	})
	if err != nil {
		t.Fatalf("requestCall: %v", err)
	}
	waiting := res.PhoneCall.(*tg.PhoneCallWaiting)
	peer := tg.InputPhoneCall{ID: waiting.ID, AccessHash: waiting.AccessHash}
	if _, err := f.router.onPhoneAcceptCall(f.calleeCtx(), &tg.PhoneAcceptCallRequest{Peer: peer, GB: gb, Protocol: phoneTestProtocol()}); err != nil {
		t.Fatalf("acceptCall: %v", err)
	}
	if _, err := f.router.onPhoneConfirmCall(f.callerCtx(), &tg.PhoneConfirmCallRequest{Peer: peer, GA: ga, KeyFingerprint: 7, Protocol: phoneTestProtocol()}); err != nil {
		t.Fatalf("confirmCall: %v", err)
	}
	f.sessions.reset()
	return peer
}

func TestPhoneCallDiscardWritesHistory(t *testing.T) {
	clk := &phoneTestClock{now: time.Unix(1_700_000_000, 0)}
	messages := &phoneCaptureMessages{}
	f := newPhoneFixtureFull(t, clk, messages)
	peer := establishPhoneCall(t, f)

	// 被叫挂断：sender 仍须是主叫（AdminID）。
	if _, err := f.router.onPhoneDiscardCall(f.calleeCtx(), &tg.PhoneDiscardCallRequest{
		Peer: peer, Duration: 17, Reason: &tg.PhoneCallDiscardReasonHangup{},
	}); err != nil {
		t.Fatalf("discardCall: %v", err)
	}
	sent := messages.records()
	if len(sent) != 1 {
		t.Fatalf("history messages = %d, want 1", len(sent))
	}
	req := sent[0]
	if req.SenderUserID != f.caller.ID || req.RecipientUserID != f.callee.ID {
		t.Fatalf("history sender/recipient = %d/%d, want caller→callee", req.SenderUserID, req.RecipientUserID)
	}
	if req.OriginAuthKeyID != [8]byte{} || req.OriginSessionID != 0 {
		t.Fatalf("history origin must be zero (all devices via outbox), got %+v/%d", req.OriginAuthKeyID, req.OriginSessionID)
	}
	if req.Media == nil || req.Media.ServiceAction == nil || req.Media.ServiceAction.Kind != domain.MessageServiceActionPhoneCall {
		t.Fatalf("history media = %+v, want phone_call service action", req.Media)
	}
	action := req.Media.ServiceAction.Call
	if action == nil || action.CallID != peer.ID || action.Duration != 17 || action.Reason != string(domain.PhoneCallDiscardReasonHangup) {
		t.Fatalf("history action = %+v", action)
	}
	// 幂等：重复 discard 不再落历史。
	if _, err := f.router.onPhoneDiscardCall(f.callerCtx(), &tg.PhoneDiscardCallRequest{Peer: peer}); err != nil {
		t.Fatalf("idempotent discard: %v", err)
	}
	if len(messages.records()) != 1 {
		t.Fatalf("idempotent discard must not duplicate history")
	}
}

func TestPhoneExpiryDispatcherMissedCall(t *testing.T) {
	clk := &phoneTestClock{now: time.Unix(1_700_000_000, 0)}
	messages := &phoneCaptureMessages{}
	f := newPhoneFixtureFull(t, clk, messages)
	_, gaHash, _ := phoneTestKeys()

	res, err := f.router.onPhoneRequestCall(f.callerCtx(), &tg.PhoneRequestCallRequest{
		UserID: &tg.InputUser{UserID: f.callee.ID, AccessHash: f.callee.AccessHash},
		GAHash: gaHash, Protocol: phoneTestProtocol(),
	})
	if err != nil {
		t.Fatalf("requestCall: %v", err)
	}
	waiting := res.PhoneCall.(*tg.PhoneCallWaiting)
	f.sessions.reset()

	dispatcher := NewPhoneExpiryDispatcher(f.router, zaptest.NewLogger(t), time.Second)
	dispatcher.DispatchOnce(f.ctx)
	if len(f.sessions.records()) != 0 || len(messages.records()) != 0 {
		t.Fatalf("nothing should expire before ring timeout")
	}

	clk.Advance(91 * time.Second)
	dispatcher.DispatchOnce(f.ctx)
	pushes := f.sessions.records()
	if len(pushes) != 2 || pushes[0].userID != f.caller.ID || pushes[1].userID != f.callee.ID {
		t.Fatalf("expiry pushes = %+v, want both sides", pushes)
	}
	for _, rec := range pushes {
		payload, ok := phoneCallPayload(t, rec).(*tg.PhoneCallDiscarded)
		if !ok {
			t.Fatalf("expiry payload to %d = %T", rec.userID, phoneCallPayload(t, rec))
		}
		if reason, _ := payload.GetReason(); reason == nil {
			t.Fatalf("expiry discarded must carry missed reason")
		} else if _, isMissed := reason.(*tg.PhoneCallDiscardReasonMissed); !isMissed {
			t.Fatalf("expiry reason = %T, want missed", reason)
		}
		// dispatcher 推送不排除任何设备。
		if rec.excludeSession != 0 {
			t.Fatalf("expiry push exclude = %d, want 0", rec.excludeSession)
		}
	}
	sent := messages.records()
	if len(sent) != 1 || sent[0].Media.ServiceAction.Call.Reason != string(domain.PhoneCallDiscardReasonMissed) {
		t.Fatalf("missed history = %+v, want one missed entry", sent)
	}
	if sent[0].Media.ServiceAction.Call.CallID != waiting.ID {
		t.Fatalf("missed history call id = %d, want %d", sent[0].Media.ServiceAction.Call.CallID, waiting.ID)
	}
}

// TestPhoneCallHistoryThroughRealPipeline 走真实 messages 管线（memory store）验证
// 通话历史端到端：新 action kind 经媒体 JSONB 序列化、读路径 TL 转换后完整存活。
func TestPhoneCallHistoryThroughRealPipeline(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	dialogs := memorydialogs(t)
	messageStore := memory.NewMessageStore(dialogs)
	sessions := &phoneCaptureSessions{}
	clk := &phoneTestClock{now: time.Unix(1_700_000_000, 0)}
	router := New(Config{CallSignalingMaxBytes: 1024}, Deps{
		Users:    appusers.NewService(userStore),
		Privacy:  stubPrivacy{},
		Phone:    appphone.NewService(appphone.Config{}, appphone.WithClock(clk)),
		Messages: appmessages.NewService(messageStore, dialogs),
		Sessions: sessions,
	}, zaptest.NewLogger(t), clk)
	f := &phoneFixture{t: t, ctx: ctx, router: router, sessions: sessions}
	mk := func(hash int64, phone, name string) domain.User {
		u, err := userStore.Create(ctx, domain.User{AccessHash: hash, Phone: phone, FirstName: name})
		if err != nil {
			t.Fatalf("create user: %v", err)
		}
		return u
	}
	f.caller = mk(1001, "13800000001", "Caller")
	f.callee = mk(1002, "13800000002", "Callee")

	peer := establishPhoneCall(t, f)
	if _, err := router.onPhoneDiscardCall(f.callerCtx(), &tg.PhoneDiscardCallRequest{
		Peer: peer, Duration: 33, Reason: &tg.PhoneCallDiscardReasonHangup{},
	}); err != nil {
		t.Fatalf("discardCall: %v", err)
	}

	// 被叫视角读历史：服务消息存在且转换为 messageActionPhoneCall。
	list, err := router.deps.Messages.GetHistory(ctx, f.callee.ID, domain.MessageFilter{
		HasPeer: true,
		Peer:    domain.Peer{Type: domain.PeerTypeUser, ID: f.caller.ID},
		Limit:   10,
	})
	if err != nil || len(list.Messages) == 0 {
		t.Fatalf("history = %+v err=%v, want phone call entry", list, err)
	}
	msg := list.Messages[0]
	if msg.From.ID != f.caller.ID {
		t.Fatalf("history sender = %d, want caller %d", msg.From.ID, f.caller.ID)
	}
	converted := tgMessageServiceAction(msg)
	action, ok := converted.(*tg.MessageActionPhoneCall)
	if !ok {
		t.Fatalf("converted action = %T, want MessageActionPhoneCall", converted)
	}
	if action.CallID != peer.ID {
		t.Fatalf("action call id = %d, want %d", action.CallID, peer.ID)
	}
	if d, _ := action.GetDuration(); d != 33 {
		t.Fatalf("action duration = %d, want 33", d)
	}
	if reason, _ := action.GetReason(); reason == nil {
		t.Fatalf("action reason missing")
	}
}

func memorydialogs(t *testing.T) *memory.DialogStore {
	t.Helper()
	return memory.NewDialogStore()
}
