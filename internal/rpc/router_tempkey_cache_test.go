package rpc

import (
	"context"
	"testing"
	"time"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/clock"
	"github.com/gotd/td/tg"
	"go.uber.org/zap/zaptest"
)

type revokeCaptureSessions struct {
	captureSessions
	closedBusinessAuthKeyIDs [][8]byte
	closedRawAuthKeyIDs      [][8]byte
}

type expiringCaptureSessions struct {
	*captureSessions
	expiresAt int
}

type metadataBlindSessions struct {
	SessionBinder
}

func (s *expiringCaptureSessions) AuthKeyExpiresAtForSession([8]byte, int64) (int, bool) {
	return s.expiresAt, true
}

func (s *revokeCaptureSessions) CloseSessionsForBusinessAuthKey(authKeyID [8]byte) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closedBusinessAuthKeyIDs = append(s.closedBusinessAuthKeyIDs, authKeyID)
	return 1
}

func (s *revokeCaptureSessions) CloseSessionsForRawAuthKeyExcept(authKeyID [8]byte, _ int64) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closedRawAuthKeyIDs = append(s.closedRawAuthKeyIDs, authKeyID)
	return 1
}

func TestCachedRawTemporarySessionReResolvesDurableBinding(t *testing.T) {
	tempAuthKeyID := [8]byte{0x76, 0x76, 0x76, 0x76, 0x76, 0x76, 0x76, 0x76}
	permAuthKeyID := [8]byte{0x36, 0x36, 0x36, 0x36, 0x36, 0x36, 0x36, 0x36}
	base := &captureSessions{}
	base.BindAuthKeyForSession(tempAuthKeyID, 554, tempAuthKeyID)
	sessions := &expiringCaptureSessions{
		captureSessions: base,
		expiresAt:       int(time.Now().Add(time.Hour).Unix()),
	}
	auth := &captureAuthService{
		resolvedAuthKeyID: permAuthKeyID,
		hasResolved:       true,
		userID:            1000000001,
	}
	r := New(Config{TempKeyResolveCacheTTL: time.Minute}, Deps{
		Auth:     auth,
		Files:    &fakeFiles{},
		Sessions: sessions,
	}, zaptest.NewLogger(t), clock.System)

	var in bin.Buffer
	if err := (&tg.UploadSaveFilePartRequest{FileID: 19, FilePart: 0, Bytes: []byte{1}}).Encode(&in); err != nil {
		t.Fatalf("encode: %v", err)
	}
	if _, err := r.Dispatch(context.Background(), tempAuthKeyID, 554, &in); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if auth.resolveCount != 1 {
		t.Fatalf("ResolveAuthKey calls = %d, want 1 for cached raw temporary session", auth.resolveCount)
	}
	got := sessions.snapshot()
	if got.authKeyID != permAuthKeyID || got.userID != 1000000001 {
		t.Fatalf("session = auth %x user %d, want perm/user", got.authKeyID, got.userID)
	}
}

func TestCachedRawSessionWithoutMetadataFailsClosedToDurableResolver(t *testing.T) {
	tempAuthKeyID := [8]byte{0x75, 0x75, 0x75, 0x75, 0x75, 0x75, 0x75, 0x75}
	permAuthKeyID := [8]byte{0x35, 0x35, 0x35, 0x35, 0x35, 0x35, 0x35, 0x35}
	// captureSessions intentionally has no RawAuthKeyMetadataProvider capability.
	// Missing metadata is not evidence that raw is permanent.
	base := &captureSessions{}
	base.BindAuthKeyForSession(tempAuthKeyID, 553, tempAuthKeyID)
	sessions := &metadataBlindSessions{SessionBinder: base}
	auth := &captureAuthService{
		resolvedAuthKeyID: permAuthKeyID,
		hasResolved:       true,
		userID:            1000000001,
	}
	r := New(Config{TempKeyResolveCacheTTL: time.Minute}, Deps{
		Auth:     auth,
		Files:    &fakeFiles{},
		Sessions: sessions,
	}, zaptest.NewLogger(t), clock.System)

	var in bin.Buffer
	if err := (&tg.UploadSaveFilePartRequest{FileID: 18, FilePart: 0, Bytes: []byte{1}}).Encode(&in); err != nil {
		t.Fatalf("encode: %v", err)
	}
	if _, err := r.Dispatch(context.Background(), tempAuthKeyID, 553, &in); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if auth.resolveCount != 1 {
		t.Fatalf("ResolveAuthKey calls = %d, want 1 without metadata proof", auth.resolveCount)
	}
	if got := base.snapshot(); got.authKeyID != permAuthKeyID || got.userID != 1000000001 {
		t.Fatalf("session = auth %x user %d, want canonical perm/user", got.authKeyID, got.userID)
	}
}

// TestTempKeyResolveCacheHitsWithinTTL 验证：TempKeyResolveCacheTTL>0 时，同一 temp key 的连续
// 请求在 TTL 内只解析一次（首帧走 !hasCached 解析 1 次、次帧 hasCached 解析并填缓存 1 次，之后命中
// 缓存不再打 ResolveAuthKey）。固化「缓存生效」语义，与现有「TTL=0 每帧重校验」的安全测试互补。
func TestTempKeyResolveCacheHitsWithinTTL(t *testing.T) {
	tempAuthKeyID := [8]byte{0x77, 0x77, 0x77, 0x77, 0x77, 0x77, 0x77, 0x77}
	permAuthKeyID := [8]byte{0x33, 0x33, 0x33, 0x33, 0x33, 0x33, 0x33, 0x33}
	sessions := &captureSessions{}
	auth := &captureAuthService{
		resolvedAuthKeyID: permAuthKeyID,
		hasResolved:       true,
		userID:            1000000001,
	}
	r := New(Config{TempKeyResolveCacheTTL: time.Minute}, Deps{
		Auth:     auth,
		Files:    &fakeFiles{},
		Sessions: sessions,
	}, zaptest.NewLogger(t), clock.System)

	for i := 0; i < 8; i++ {
		var in bin.Buffer
		if err := (&tg.UploadSaveFilePartRequest{FileID: 20, FilePart: i, Bytes: []byte{1}}).Encode(&in); err != nil {
			t.Fatalf("encode %d: %v", i, err)
		}
		if _, err := r.Dispatch(context.Background(), tempAuthKeyID, 555, &in); err != nil {
			t.Fatalf("dispatch %d: %v", i, err)
		}
	}
	// 首帧 !hasCached 解析 1 次；次帧 hasCached miss 解析 1 次并填缓存；其余 6 帧命中缓存。
	if auth.resolveCount != 2 {
		t.Fatalf("ResolveAuthKey calls = %d over 8 dispatches, want 2 (cached within TTL)", auth.resolveCount)
	}
	got := sessions.snapshot()
	if got.authKeyID != permAuthKeyID || got.userID != 1000000001 {
		t.Fatalf("session = auth %x user %d, want perm/user", got.authKeyID, got.userID)
	}
}

// TestTempKeyResolveCacheExpires 验证 TTL 过期后会重新解析自然到期的 temp key。
func TestTempKeyResolveCacheExpires(t *testing.T) {
	tempAuthKeyID := [8]byte{0x78, 0x78, 0x78, 0x78, 0x78, 0x78, 0x78, 0x78}
	permAuthKeyID := [8]byte{0x34, 0x34, 0x34, 0x34, 0x34, 0x34, 0x34, 0x34}
	sessions := &captureSessions{}
	auth := &captureAuthService{
		resolvedAuthKeyID: permAuthKeyID,
		hasResolved:       true,
		userID:            1000000001,
	}
	r := New(Config{TempKeyResolveCacheTTL: time.Millisecond}, Deps{
		Auth:     auth,
		Files:    &fakeFiles{},
		Sessions: sessions,
	}, zaptest.NewLogger(t), clock.System)

	dispatch := func() {
		var in bin.Buffer
		if err := (&tg.UploadSaveFilePartRequest{FileID: 21, FilePart: 0, Bytes: []byte{1}}).Encode(&in); err != nil {
			t.Fatalf("encode: %v", err)
		}
		if _, err := r.Dispatch(context.Background(), tempAuthKeyID, 556, &in); err != nil {
			t.Fatalf("dispatch: %v", err)
		}
	}
	dispatch() // 首帧 !hasCached
	dispatch() // 次帧填缓存
	before := auth.resolveCount
	time.Sleep(10 * time.Millisecond) // 等缓存过期
	dispatch()
	if auth.resolveCount <= before {
		t.Fatalf("ResolveAuthKey calls = %d, want > %d after TTL expiry (re-validation)", auth.resolveCount, before)
	}
}

func TestRevokeAuthKeySessionsInvalidatesCachedTempKeysAndClosesRawConnections(t *testing.T) {
	permAuthKeyID := [8]byte{0x44, 0x44, 0x44, 0x44, 0x44, 0x44, 0x44, 0x44}
	tempAuthKeyID := [8]byte{0x79, 0x79, 0x79, 0x79, 0x79, 0x79, 0x79, 0x79}
	otherTempAuthKeyID := [8]byte{0x7a, 0x7a, 0x7a, 0x7a, 0x7a, 0x7a, 0x7a, 0x7a}
	otherPermAuthKeyID := [8]byte{0x45, 0x45, 0x45, 0x45, 0x45, 0x45, 0x45, 0x45}
	sessions := &revokeCaptureSessions{}
	r := New(Config{TempKeyResolveCacheTTL: time.Minute}, Deps{
		Sessions: sessions,
	}, zaptest.NewLogger(t), clock.System)
	expires := time.Now().Add(time.Minute)
	now := time.Now()
	r.tempKeyResolveCache.Store(tempAuthKeyID, permAuthKeyID, expires, now)
	r.tempKeyResolveCache.Store(otherTempAuthKeyID, otherPermAuthKeyID, expires, now)

	r.revokeAuthKeySessions(permAuthKeyID)

	if _, ok := r.tempKeyResolveCache.Get(tempAuthKeyID, permAuthKeyID, time.Now()); ok {
		t.Fatal("revoked temp auth key cache entry still present")
	}
	if _, ok := r.tempKeyResolveCache.Get(otherTempAuthKeyID, otherPermAuthKeyID, time.Now()); !ok {
		t.Fatal("unrelated temp auth key cache entry was deleted")
	}
	if got := len(sessions.closedBusinessAuthKeyIDs); got != 1 || sessions.closedBusinessAuthKeyIDs[0] != permAuthKeyID {
		t.Fatalf("business closes = %x, want only %x", sessions.closedBusinessAuthKeyIDs, permAuthKeyID)
	}
	if got := len(sessions.closedRawAuthKeyIDs); got != 1 || sessions.closedRawAuthKeyIDs[0] != tempAuthKeyID {
		t.Fatalf("raw closes = %x, want only %x", sessions.closedRawAuthKeyIDs, tempAuthKeyID)
	}
}

func TestTempKeyResolveCacheEvictsOldestAtCapacity(t *testing.T) {
	cache := newTempKeyResolveCache(2)
	now := time.Now()
	perm := [8]byte{0x40}
	first := [8]byte{0x80}
	second := [8]byte{0x81}
	third := [8]byte{0x82}

	cache.Store(first, perm, now.Add(time.Minute), now)
	cache.Store(second, perm, now.Add(time.Minute), now)
	cache.Store(third, perm, now.Add(time.Minute), now)

	if _, ok := cache.Get(first, perm, now); ok {
		t.Fatal("oldest cache entry still present after capacity eviction")
	}
	if _, ok := cache.Get(second, perm, now); !ok {
		t.Fatal("second cache entry missing after capacity eviction")
	}
	if _, ok := cache.Get(third, perm, now); !ok {
		t.Fatal("newest cache entry missing after capacity eviction")
	}
}
