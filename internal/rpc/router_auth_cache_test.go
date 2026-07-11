package rpc

import (
	"context"
	"encoding/binary"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/clock"
	"github.com/gotd/td/proto"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
	"go.uber.org/zap/zaptest"
	"testing"
	"time"
)

// authBindingCaptureSessions keeps session authorization state separate from the target of an
// asynchronous presence push. The broad captureSessions fake intentionally records the latest
// PushToUser target in userID, which is useful to most RPC tests but can race a stale-auth-key
// assertion and make an old presence echo look like the session was rebound.
type authBindingCaptureSessions struct {
	*captureSessions
}

func newAuthBindingCaptureSessions() *authBindingCaptureSessions {
	return &authBindingCaptureSessions{captureSessions: &captureSessions{}}
}

func (s *authBindingCaptureSessions) PushToUserExceptSession(_ context.Context, userID, _ int64, t proto.MessageType, msg bin.Encoder) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.messageType = t
	s.message = msg
	s.userMessage = msg
	s.pushUserIDs = append(s.pushUserIDs, userID)
	return 1, nil
}

func TestDispatchPromotesNegativeSessionCacheFromPositiveAuthCache(t *testing.T) {
	authKeyID := [8]byte{0x91, 0x91, 0x91, 0x91, 0x91, 0x91, 0x91, 0x91}
	const (
		sessionID = int64(300)
		userID    = int64(1000000001)
	)
	sessions := newAuthBindingCaptureSessions()
	sessions.BindAuthKey(sessionID, authKeyID)
	sessions.BindUser(sessionID, 0)
	auth := &captureAuthService{}
	r := New(Config{}, Deps{
		Auth:     auth,
		Files:    &fakeFiles{},
		Sessions: sessions,
	}, zaptest.NewLogger(t), clock.System)
	r.setAuthUserCache(authKeyID, userID, true)

	var in bin.Buffer
	if err := (&tg.UploadSaveFilePartRequest{FileID: 10, FilePart: 0, Bytes: []byte{1}}).Encode(&in); err != nil {
		t.Fatalf("encode upload part: %v", err)
	}
	enc, err := r.Dispatch(context.Background(), authKeyID, sessionID, &in)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if box, ok := enc.(*tg.BoolBox); !ok {
		t.Fatalf("dispatch result = %T, want *tg.BoolBox", enc)
	} else if _, ok := box.Bool.(*tg.BoolTrue); !ok {
		t.Fatalf("dispatch bool = %T, want BoolTrue", box.Bool)
	}
	gotSession := sessions.snapshot()
	if gotSession.userID != userID || !gotSession.userResolved {
		t.Fatalf("session user = %d resolved %v, want %d/true", gotSession.userID, gotSession.userResolved, userID)
	}
	if auth.userIDCount != 0 {
		t.Fatalf("auth UserID lookups = %d, want 0", auth.userIDCount)
	}
}

func TestBindTempAuthKeyClearsNegativeUserCache(t *testing.T) {
	var tempAuthKeyID = [8]byte{0x55, 0x55, 0x55, 0x55, 0x55, 0x55, 0x55, 0x55}
	var permAuthKeyID = [8]byte{0x11, 0x11, 0x11, 0x11, 0x11, 0x11, 0x11, 0x11}
	sessions := newAuthBindingCaptureSessions()
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

func TestDispatchRevalidatesCachedTempAuthKeyBinding(t *testing.T) {
	var tempAuthKeyID = [8]byte{0x65, 0x65, 0x65, 0x65, 0x65, 0x65, 0x65, 0x65}
	var permAuthKeyID = [8]byte{0x21, 0x21, 0x21, 0x21, 0x21, 0x21, 0x21, 0x21}
	sessions := newAuthBindingCaptureSessions()
	auth := &captureAuthService{
		resolvedAuthKeyID: permAuthKeyID,
		hasResolved:       true,
		userID:            1000000001,
	}
	r := New(Config{}, Deps{
		Auth:     auth,
		Files:    &fakeFiles{},
		Sessions: sessions,
	}, zaptest.NewLogger(t), clock.System)

	var first bin.Buffer
	if err := (&tg.UploadSaveFilePartRequest{FileID: 10, FilePart: 0, Bytes: []byte{1}}).Encode(&first); err != nil {
		t.Fatalf("encode first upload part: %v", err)
	}
	if enc, err := r.Dispatch(context.Background(), tempAuthKeyID, 123, &first); err != nil {
		t.Fatalf("first dispatch: %v", err)
	} else if box, ok := enc.(*tg.BoolBox); !ok {
		t.Fatalf("first dispatch result = %T, want *tg.BoolBox", enc)
	} else if _, ok := box.Bool.(*tg.BoolTrue); !ok {
		t.Fatalf("first dispatch bool = %T, want BoolTrue", box.Bool)
	}
	gotSession := sessions.snapshot()
	if gotSession.authKeyID != permAuthKeyID || gotSession.userID != 1000000001 {
		t.Fatalf("session after valid temp binding = auth %x user %d, want perm/user", gotSession.authKeyID, gotSession.userID)
	}

	auth.hasResolved = false
	auth.resolvedAuthKeyID = [8]byte{}
	auth.userID = 0
	var second bin.Buffer
	if err := (&tg.UploadSaveFilePartRequest{FileID: 10, FilePart: 1, Bytes: []byte{2}}).Encode(&second); err != nil {
		t.Fatalf("encode second upload part: %v", err)
	}
	if _, err := r.Dispatch(context.Background(), tempAuthKeyID, 123, &second); err == nil || !tgerr.Is(err, "AUTH_KEY_UNREGISTERED") {
		t.Fatalf("second dispatch err = %v, want AUTH_KEY_UNREGISTERED after temp binding no longer resolves", err)
	}
	gotSession = sessions.snapshot()
	if gotSession.authKeyID != tempAuthKeyID || !gotSession.authKeyResolved {
		t.Fatalf("session auth after stale temp binding = %x resolved %v, want raw temp", gotSession.authKeyID, gotSession.authKeyResolved)
	}
	if gotSession.userID != 0 || !gotSession.userResolved {
		t.Fatalf("session user after stale temp binding = %d resolved %v, want 0/true", gotSession.userID, gotSession.userResolved)
	}
	if auth.resolveCount != 2 {
		t.Fatalf("ResolveAuthKey calls = %d, want revalidation on cached temp mapping", auth.resolveCount)
	}
}

func TestDispatchUsesCachedTempAuthKeyUserUntilWriteSideInvalidation(t *testing.T) {
	var tempAuthKeyID = [8]byte{0x66, 0x66, 0x66, 0x66, 0x66, 0x66, 0x66, 0x66}
	var permAuthKeyID = [8]byte{0x22, 0x22, 0x22, 0x22, 0x22, 0x22, 0x22, 0x22}
	sessions := newAuthBindingCaptureSessions()
	auth := &captureAuthService{
		resolvedAuthKeyID: permAuthKeyID,
		hasResolved:       true,
		userID:            1000000001,
	}
	r := New(Config{}, Deps{
		Auth:     auth,
		Files:    &fakeFiles{},
		Sessions: sessions,
	}, zaptest.NewLogger(t), clock.System)

	var first bin.Buffer
	if err := (&tg.UploadSaveFilePartRequest{FileID: 11, FilePart: 0, Bytes: []byte{1}}).Encode(&first); err != nil {
		t.Fatalf("encode first upload part: %v", err)
	}
	if _, err := r.Dispatch(context.Background(), tempAuthKeyID, 124, &first); err != nil {
		t.Fatalf("first dispatch: %v", err)
	}

	auth.userID = 0
	var second bin.Buffer
	if err := (&tg.UploadSaveFilePartRequest{FileID: 11, FilePart: 1, Bytes: []byte{2}}).Encode(&second); err != nil {
		t.Fatalf("encode second upload part: %v", err)
	}
	if _, err := r.Dispatch(context.Background(), tempAuthKeyID, 124, &second); err != nil {
		t.Fatalf("second dispatch should use cached user until write-side invalidation: %v", err)
	}
	gotSession := sessions.snapshot()
	if gotSession.authKeyID != permAuthKeyID || !gotSession.authKeyResolved {
		t.Fatalf("session auth = %x resolved %v, want still mapped perm", gotSession.authKeyID, gotSession.authKeyResolved)
	}
	if gotSession.userID != 1000000001 || !gotSession.userResolved {
		t.Fatalf("session user = %d resolved %v, want cached user", gotSession.userID, gotSession.userResolved)
	}
	if auth.resolveCount != 2 || auth.userIDCount != 1 {
		t.Fatalf("lookups = resolve %d user %d, want temp mapping checks and cached user identity", auth.resolveCount, auth.userIDCount)
	}

	r.revokeAuthKeySessions(permAuthKeyID)
	var third bin.Buffer
	if err := (&tg.UploadSaveFilePartRequest{FileID: 11, FilePart: 2, Bytes: []byte{3}}).Encode(&third); err != nil {
		t.Fatalf("encode third upload part: %v", err)
	}
	if _, err := r.Dispatch(context.Background(), tempAuthKeyID, 124, &third); err == nil || !tgerr.Is(err, "AUTH_KEY_UNREGISTERED") {
		t.Fatalf("third dispatch err = %v, want AUTH_KEY_UNREGISTERED after write-side invalidation", err)
	}
	gotSession = sessions.snapshot()
	if gotSession.userID != 0 || !gotSession.userResolved {
		t.Fatalf("session user after write-side invalidation = %d resolved %v, want 0/true", gotSession.userID, gotSession.userResolved)
	}
}
