package rpc

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/gotd/td/clock"
	"github.com/gotd/td/proto"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
	"go.uber.org/zap/zaptest"

	"telesrv/internal/domain"
)

func TestAuthLoginTokenAcceptedByAndroidBindsTargetSession(t *testing.T) {
	const (
		targetSession  = int64(101)
		scannerSession = int64(202)
		scannerUserID  = int64(1000000001)
	)
	targetRawAuthKeyID := [8]byte{0x10, 0x20, 0x30, 0x40, 0x50, 0x60, 0x70, 0x80}
	targetAuthKeyID := [8]byte{0x81, 0x71, 0x61, 0x51, 0x41, 0x31, 0x21, 0x11}
	scannerRawAuthKeyID := [8]byte{0x22, 0x33, 0x44, 0x55, 0x66, 0x77, 0x88, 0x99}
	scannerAuthKeyID := [8]byte{0x99, 0x88, 0x77, 0x66, 0x55, 0x44, 0x33, 0x22}

	auth := &captureAuthService{}
	sessions := &captureScopedSessions{captureSessions: &captureSessions{}}
	users := mapUsersService{users: map[int64]domain.User{
		scannerUserID: {ID: scannerUserID, FirstName: "Alice", Phone: "15550001001"},
	}}
	r := New(Config{DC: 2, IP: "127.0.0.1", Port: 2398}, Deps{
		Auth:     auth,
		Sessions: sessions,
		Users:    users,
	}, zaptest.NewLogger(t), clock.System)

	targetCtx := WithClientInfo(
		WithLayer(
			WithSessionID(
				WithAuthKeyID(
					WithRawAuthKeyID(context.Background(), targetRawAuthKeyID),
					targetAuthKeyID,
				),
				targetSession,
			),
			currentClientLayer,
		),
		ClientInfo{
			APIID:         2040,
			DeviceModel:   "WebA",
			SystemVersion: "Chrome",
			AppVersion:    "1.0",
			LangPack:      "tdesktop",
		},
	)
	exported, err := r.onAuthExportLoginToken(targetCtx, &tg.AuthExportLoginTokenRequest{APIID: 2040, APIHash: "hash"})
	if err != nil {
		t.Fatalf("export login token: %v", err)
	}
	loginToken, ok := exported.(*tg.AuthLoginToken)
	if !ok {
		t.Fatalf("export type = %T, want *tg.AuthLoginToken", exported)
	}
	if len(loginToken.Token) != loginTokenBytes {
		t.Fatalf("token length = %d, want %d", len(loginToken.Token), loginTokenBytes)
	}

	unauthorizedScannerCtx := WithSessionID(
		WithAuthKeyID(
			WithRawAuthKeyID(context.Background(), scannerRawAuthKeyID),
			scannerAuthKeyID,
		),
		scannerSession,
	)
	if _, err := r.onAuthAcceptLoginToken(unauthorizedScannerCtx, loginToken.Token); !tgerr.Is(err, "AUTH_KEY_UNREGISTERED") {
		t.Fatalf("unauthorized accept err = %v, want AUTH_KEY_UNREGISTERED", err)
	}

	scannerCtx := WithUserID(unauthorizedScannerCtx, scannerUserID)
	authorization, err := r.onAuthAcceptLoginToken(scannerCtx, loginToken.Token)
	if err != nil {
		t.Fatalf("accept login token: %v", err)
	}
	if authorization == nil {
		t.Fatal("accept login token returned nil authorization")
	}
	if auth.acceptedUserID != scannerUserID {
		t.Fatalf("accepted user id = %d, want %d", auth.acceptedUserID, scannerUserID)
	}
	if auth.acceptedAuth.AuthKeyID != targetAuthKeyID {
		t.Fatalf("accepted auth key = %x, want %x", auth.acceptedAuth.AuthKeyID, targetAuthKeyID)
	}
	if auth.acceptedAuth.DeviceModel != "WebA" {
		t.Fatalf("accepted device = %q, want WebA", auth.acceptedAuth.DeviceModel)
	}
	if authorization.Hash == 0 {
		t.Fatal("authorization hash is zero")
	}

	snap := sessions.snapshot()
	if got := sessions.scopedAuthKey(); got != targetRawAuthKeyID {
		t.Fatalf("scoped raw auth key = %x, want %x", got, targetRawAuthKeyID)
	}
	if snap.sessionID != targetSession || snap.userID != scannerUserID || !snap.userResolved {
		t.Fatalf("target session snapshot = %+v, want session/user/resolved %d/%d/true", snap, targetSession, scannerUserID)
	}
	if snap.messageType != proto.MessageFromServer {
		t.Fatalf("push message type = %v, want MessageFromServer", snap.messageType)
	}
	if !sessions.immediatePushSeen() {
		t.Fatal("login token update was not pushed through the immediate pre-auth path")
	}
	short, ok := snap.message.(*tg.UpdateShort)
	if !ok {
		t.Fatalf("push message = %T, want *tg.UpdateShort", snap.message)
	}
	if _, ok := short.Update.(*tg.UpdateLoginToken); !ok {
		t.Fatalf("pushed update = %T, want *tg.UpdateLoginToken", short.Update)
	}

	success, err := r.onAuthExportLoginToken(targetCtx, &tg.AuthExportLoginTokenRequest{APIID: 2040, APIHash: "hash"})
	if err != nil {
		t.Fatalf("export after accept: %v", err)
	}
	loginSuccess, ok := success.(*tg.AuthLoginTokenSuccess)
	if !ok {
		t.Fatalf("export after accept type = %T, want *tg.AuthLoginTokenSuccess", success)
	}
	authz, ok := loginSuccess.Authorization.(*tg.AuthAuthorization)
	if !ok {
		t.Fatalf("success authorization = %T, want *tg.AuthAuthorization", loginSuccess.Authorization)
	}
	user, ok := authz.User.(*tg.User)
	if !ok {
		t.Fatalf("success user = %T, want *tg.User", authz.User)
	}
	if user.ID != scannerUserID {
		t.Fatalf("success user id = %d, want %d", user.ID, scannerUserID)
	}

	if _, err := r.onAuthAcceptLoginToken(scannerCtx, loginToken.Token); !tgerr.Is(err, "AUTH_TOKEN_ALREADY_ACCEPTED") {
		t.Fatalf("duplicate accept err = %v, want AUTH_TOKEN_ALREADY_ACCEPTED", err)
	}
}

func TestAuthLoginTokenExpires(t *testing.T) {
	now := time.Unix(1700000000, 0)
	reg := newLoginTokenRegistry()
	target := loginTokenTarget{
		rawAuthKeyID: [8]byte{1},
		authKeyID:    [8]byte{2},
		sessionID:    3,
	}
	exported, err := reg.export(now, target, domain.Authorization{AuthKeyID: target.authKeyID}, nil)
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if _, err := reg.beginAccept(now.Add(loginTokenTTL+time.Second), exported.token, 1000000001); !tgerr.Is(err, "AUTH_TOKEN_EXPIRED") {
		t.Fatalf("expired accept err = %v, want AUTH_TOKEN_EXPIRED", err)
	}
}

func TestLoginTokenRegistryCapacityBounded(t *testing.T) {
	now := time.Unix(1700000000, 0)
	reg := newLoginTokenRegistry()
	targets := make([]loginTokenTarget, 0, loginTokenMaxRecords)
	var last loginTokenExport
	for i := 0; i < loginTokenMaxRecords; i++ {
		target := loginTokenTarget{rawAuthKeyID: [8]byte{byte(i), 1}, authKeyID: [8]byte{byte(i), 2}, sessionID: int64(i + 1)}
		targets = append(targets, target)
		exported, err := reg.export(now, target, domain.Authorization{AuthKeyID: target.authKeyID}, nil)
		if err != nil {
			t.Fatalf("export %d: %v", i, err)
		}
		last = exported
	}
	again, err := reg.export(now, targets[len(targets)-1], domain.Authorization{AuthKeyID: targets[len(targets)-1].authKeyID}, nil)
	if err != nil {
		t.Fatalf("export existing target: %v", err)
	}
	if !bytes.Equal(again.token, last.token) {
		t.Fatal("existing target token rotated while registry was full")
	}
	if _, err := reg.export(now, loginTokenTarget{rawAuthKeyID: [8]byte{0xfe}, authKeyID: [8]byte{0xfd}, sessionID: 999999}, domain.Authorization{AuthKeyID: [8]byte{0xfd}}, nil); err != nil {
		t.Fatalf("export over capacity: %v", err)
	}
	reg.mu.Lock()
	got := len(reg.byToken)
	reg.mu.Unlock()
	if got > loginTokenMaxRecords {
		t.Fatalf("registry size = %d, want <= %d", got, loginTokenMaxRecords)
	}
}
