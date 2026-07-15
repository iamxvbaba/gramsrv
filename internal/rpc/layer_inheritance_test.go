package rpc

import (
	"context"
	"encoding/binary"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"go.uber.org/zap/zaptest"

	"telesrv/internal/domain"
	"telesrv/internal/store"
	"telesrv/internal/store/memory"
)

type unavailableResolveAuthService struct {
	*captureAuthService
	err error
}

func (s *unavailableResolveAuthService) ResolveAuthKey(context.Context, [8]byte) ([8]byte, bool, error) {
	return [8]byte{}, false, s.err
}

type unavailableAuthKeyInfoService struct {
	*captureAuthService
	err error
}

func (s *unavailableAuthKeyInfoService) AuthKeyClientInfo(context.Context, [8]byte) (domain.AuthKeyClientInfo, bool, error) {
	return domain.AuthKeyClientInfo{}, false, s.err
}

func TestResolveInheritedAuthKeyLayerUsesAuthKeyAuthorityOnly(t *testing.T) {
	tests := []struct {
		name          string
		keyLayer      int
		authorization int
		want          int
		found         bool
	}{
		{name: "auth key primary", keyLayer: 225, authorization: 227, want: 225, found: true},
		{name: "authorization mirror is not protocol evidence", keyLayer: 0, authorization: 225},
		{name: "unsupported primary is authoritative unknown", keyLayer: 229, authorization: 227, found: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			authKeyID := [8]byte{byte(tt.keyLayer), 0x71}
			auth := &captureAuthService{
				authKeyClientInfos: map[[8]byte]domain.AuthKeyClientInfo{
					authKeyID: {Layer: tt.keyLayer},
				},
				authorizations: []domain.Authorization{{AuthKeyID: authKeyID, UserID: 10, Layer: tt.authorization}},
			}
			r := New(Config{DC: 2}, Deps{Auth: auth}, zaptest.NewLogger(t), clock.System)
			got, found, err := r.ResolveInheritedAuthKeyLayer(context.Background(), authKeyID)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want || found != tt.found {
				t.Fatalf("resolved layer = (%d,%v), want (%d,%v)", got, found, tt.want, tt.found)
			}
			got, found, err = r.ResolveInheritedAuthKeyLayer(context.Background(), authKeyID)
			if err != nil || got != tt.want || found != tt.found {
				t.Fatalf("cached resolved layer = (%d,%v,%v), want (%d,%v,nil)", got, found, err, tt.want, tt.found)
			}
			if auth.authKeyInfoLookups != 1 {
				t.Fatalf("auth key info lookups = %d, want 1 after repeated resolve", auth.authKeyInfoLookups)
			}
			if auth.authorizationLookups != 0 {
				t.Fatalf("authorization Layer mirror lookups = %d, want 0", auth.authorizationLookups)
			}
		})
	}
}

func TestResolveInheritedAuthKeyLayerNormalizesBoundTempToPermanent(t *testing.T) {
	rawAuthKeyID := [8]byte{0x31}
	permAuthKeyID := [8]byte{0x32}
	auth := &captureAuthService{
		resolvedAuthKeyID: permAuthKeyID,
		hasResolved:       true,
		authKeyClientInfos: map[[8]byte]domain.AuthKeyClientInfo{
			rawAuthKeyID:  {Layer: 225}, // stale shadow
			permAuthKeyID: {Layer: 227}, // canonical shared default
		},
	}
	r := New(Config{DC: 2}, Deps{Auth: auth}, zaptest.NewLogger(t), clock.System)

	layer, found, err := r.ResolveInheritedAuthKeyLayer(context.Background(), rawAuthKeyID)
	if err != nil {
		t.Fatal(err)
	}
	if !found || layer != 227 {
		t.Fatalf("bound temp layer = (%d,%v), want (227,true)", layer, found)
	}
	if cached, ok := r.cachedAuthKeyLayerDefault(rawAuthKeyID); !ok || cached != 227 {
		t.Fatalf("raw in-memory shadow = (%d,%v), want (227,true)", cached, ok)
	}
}

func TestResolveInheritedAuthKeyLayerMarksOnlyAvailabilityFailures(t *testing.T) {
	boom := errors.New("postgres temporarily unavailable")
	for _, tt := range []struct {
		name       string
		auth       AuthService
		want       error
		wantMarker bool
	}{
		{
			name: "temporary binding lookup outage",
			auth: &unavailableResolveAuthService{
				captureAuthService: &captureAuthService{}, err: boom,
			},
			want: boom, wantMarker: true,
		},
		{
			name: "auth key default lookup outage",
			auth: &unavailableAuthKeyInfoService{
				captureAuthService: &captureAuthService{}, err: boom,
			},
			want: boom, wantMarker: true,
		},
		{
			name: "structural binding failure",
			auth: &unavailableResolveAuthService{
				captureAuthService: &captureAuthService{}, err: store.ErrAuthKeyBindingInvalid,
			},
			want: store.ErrAuthKeyBindingInvalid,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := New(Config{DC: 2}, Deps{Auth: tt.auth}, zaptest.NewLogger(t), clock.System)
			layer, found, err := r.ResolveInheritedAuthKeyLayer(context.Background(), [8]byte{0x33})
			if layer != 0 || found || !errors.Is(err, tt.want) {
				t.Fatalf("resolution = (%d,%v,%v), want (0,false,%v)", layer, found, err, tt.want)
			}
			var marker interface{ LayerEvidenceDurabilityUnavailable() }
			if got := errors.As(err, &marker); got != tt.wantMarker {
				t.Fatalf("availability marker=%v, want %v", got, tt.wantMarker)
			}
		})
	}
}

func TestResolveInheritedDurableAuthKeyLayerIgnoresAuthorizationMirror(t *testing.T) {
	authKeyID := [8]byte{0x39, 0x71}
	auth := &captureAuthService{
		authKeyClientInfos: map[[8]byte]domain.AuthKeyClientInfo{
			authKeyID: {},
		},
		authorizations: []domain.Authorization{{
			AuthKeyID: authKeyID,
			UserID:    10,
			Layer:     227,
		}},
	}
	r := New(Config{DC: 2}, Deps{
		Auth: auth, AuthKeySessionLayers: memory.NewAuthKeyStore(),
	}, zaptest.NewLogger(t), clock.System)

	layer, found, err := r.ResolveInheritedAuthKeyLayer(context.Background(), authKeyID)
	if err != nil || found || layer != 0 {
		t.Fatalf("durable zero primary = (%d,%v,%v), want (0,false,nil)", layer, found, err)
	}
	if auth.authKeyInfoLookups != 1 || auth.authorizationLookups != 0 {
		t.Fatalf("durable lookups = auth_key:%d authorization:%d, want 1/0",
			auth.authKeyInfoLookups, auth.authorizationLookups)
	}
}

func TestExplicitLayerCorrectionUpdatesDefaultWithoutChangingSibling(t *testing.T) {
	authKeyID := [8]byte{0x41}
	auth := &captureAuthService{authKeyClientInfos: make(map[[8]byte]domain.AuthKeyClientInfo)}
	r := New(Config{DC: 2}, Deps{Auth: auth}, zaptest.NewLogger(t), clock.System)

	ctx1 := WithAuthKeyID(WithSessionID(WithRawAuthKeyID(context.Background(), authKeyID), 1), authKeyID)
	ctx2 := WithAuthKeyID(WithSessionID(WithRawAuthKeyID(context.Background(), authKeyID), 2), authKeyID)
	r.rememberClientLayer(ctx1, 225)
	r.rememberClientLayer(ctx2, 225)
	r.rememberClientLayer(ctx1, 227)

	if layer, ok := r.NegotiatedSessionLayer(authKeyID, 1); !ok || layer != 227 {
		t.Fatalf("corrected session = (%d,%v), want (227,true)", layer, ok)
	}
	if layer, ok := r.NegotiatedSessionLayer(authKeyID, 2); !ok || layer != 225 {
		t.Fatalf("sibling session = (%d,%v), want unchanged (225,true)", layer, ok)
	}
	if got := auth.authKeyClientInfos[authKeyID].Layer; got != 227 {
		t.Fatalf("durable shared default = %d, want 227", got)
	}
	layer, found, err := r.ResolveInheritedAuthKeyLayer(context.Background(), authKeyID)
	if err != nil || !found || layer != 227 {
		t.Fatalf("new session default = (%d,%v,%v), want (227,true,nil)", layer, found, err)
	}
}

type inheritedLayerSeedCall struct {
	rawAuthKeyID [8]byte
	layer        int
}

type inheritedLayerCaptureSessions struct {
	captureSessions
	seeds         []inheritedLayerSeedCall
	refreshes     []inheritedLayerSeedCall
	clears        [][8]byte
	explicitLayer int
	explicitMsgID int64
}

func (s *inheritedLayerCaptureSessions) BindAuthKeyForRawAuthKey(rawAuthKeyID [8]byte, authKeyID [8]byte) int {
	s.BindAuthKeyForSession(rawAuthKeyID, 0, authKeyID)
	return 1
}

func (s *inheritedLayerCaptureSessions) SeedInheritedLayerForRawAuthKey(rawAuthKeyID [8]byte, layer int) int {
	s.seeds = append(s.seeds, inheritedLayerSeedCall{rawAuthKeyID: rawAuthKeyID, layer: layer})
	return 1
}

func (s *inheritedLayerCaptureSessions) RefreshInheritedLayerForRawAuthKey(rawAuthKeyID [8]byte, layer int) int {
	s.refreshes = append(s.refreshes, inheritedLayerSeedCall{rawAuthKeyID: rawAuthKeyID, layer: layer})
	return 1
}

func (s *inheritedLayerCaptureSessions) ClearInheritedLayerForRawAuthKey(rawAuthKeyID [8]byte) int {
	s.clears = append(s.clears, rawAuthKeyID)
	return 1
}

func (s *inheritedLayerCaptureSessions) ExplicitLayerEvidenceForAuthKey([8]byte, int64) (int, int64, bool) {
	return s.explicitLayer, s.explicitMsgID, s.explicitLayer != 0
}

func TestBindTempAuthKeyLayerPrecedenceAndRawShadow(t *testing.T) {
	for _, tt := range []struct {
		name          string
		explicitLayer int
		want          int
	}{
		{name: "inherit permanent", want: 225},
		{name: "current explicit wins", explicitLayer: 227, want: 227},
	} {
		t.Run(tt.name, func(t *testing.T) {
			rawAuthKeyID := [8]byte{0x51, byte(tt.want)}
			permAuthKeyID := [8]byte{0x52, byte(tt.want)}
			const sessionID = int64(99)
			auth := &captureAuthService{authKeyClientInfos: map[[8]byte]domain.AuthKeyClientInfo{
				permAuthKeyID: {Layer: 225},
			}}
			// Model the store-owned bind transaction. The router must only reload
			// this merged permanent primary; it must not derive or persist a winner
			// from its process-local exact-session registry after Bind returns.
			auth.bindTempHook = func(domain.TempAuthKeyBinding) error {
				auth.authKeyClientInfos[rawAuthKeyID] = domain.AuthKeyClientInfo{Layer: tt.want, LayerObservationID: 42}
				auth.authKeyClientInfos[permAuthKeyID] = domain.AuthKeyClientInfo{Layer: tt.want, LayerObservationID: 42}
				return nil
			}
			sessions := &inheritedLayerCaptureSessions{}
			r := New(Config{DC: 2}, Deps{Auth: auth, Sessions: sessions}, zaptest.NewLogger(t), clock.System)
			if tt.explicitLayer != 0 {
				freezeAndPublishLayer(t, r, rawAuthKeyID, sessionID, 10, 1, tt.explicitLayer)
				sessions.seeds = nil
			}
			ctx := WithAuthKeyID(WithSessionID(WithRawAuthKeyID(context.Background(), rawAuthKeyID), sessionID), rawAuthKeyID)
			ok, err := r.onAuthBindTempAuthKey(ctx, &tg.AuthBindTempAuthKeyRequest{PermAuthKeyID: businessAuthKeyInt64(permAuthKeyID)})
			if err != nil || !ok {
				t.Fatalf("bind = (%v,%v), want (true,nil)", ok, err)
			}
			if got := auth.authKeyClientInfos[rawAuthKeyID].Layer; got != tt.want {
				t.Fatalf("raw shadow = %d, want %d", got, tt.want)
			}
			if got := auth.authKeyClientInfos[permAuthKeyID].Layer; got != tt.want {
				t.Fatalf("permanent default = %d, want %d", got, tt.want)
			}
			if len(sessions.refreshes) != 1 || sessions.refreshes[0] != (inheritedLayerSeedCall{rawAuthKeyID: rawAuthKeyID, layer: tt.want}) {
				t.Fatalf("refresh calls = %+v", sessions.refreshes)
			}
			if len(sessions.seeds) != 0 {
				t.Fatalf("bind used ordinary seed instead of identity refresh: %+v", sessions.seeds)
			}
			if cached, found := r.cachedAuthKeyLayerDefault(rawAuthKeyID); !found || cached != tt.want {
				t.Fatalf("raw cached default = (%d,%v), want (%d,true)", cached, found, tt.want)
			}
			r.clientInfoMu.RLock()
			rawCached := r.authInfo[rawAuthKeyID]
			permCached := r.authInfo[permAuthKeyID]
			r.clientInfoMu.RUnlock()
			if rawCached.layerObservationID != 42 || rawCached.layerObservationID != permCached.layerObservationID {
				t.Fatalf("bound observation shadow = raw:%d perm:%d, want 42/42",
					rawCached.layerObservationID, permCached.layerObservationID)
			}
		})
	}
}

func TestResolveInheritedAuthKeyLayerCachesTerminalMissingRows(t *testing.T) {
	authKeyID := [8]byte{0x61}
	auth := &captureAuthService{authKeyClientInfos: make(map[[8]byte]domain.AuthKeyClientInfo)}
	r := New(Config{DC: 2}, Deps{Auth: auth}, zaptest.NewLogger(t), clock.System)

	for i := 0; i < 2; i++ {
		layer, found, err := r.ResolveInheritedAuthKeyLayer(context.Background(), authKeyID)
		if err != nil || found || layer != 0 {
			t.Fatalf("resolve %d = (%d,%v,%v), want (0,false,nil)", i, layer, found, err)
		}
	}
	if auth.authKeyInfoLookups != 1 || auth.authorizationLookups != 0 {
		t.Fatalf("terminal missing lookups = auth_key:%d authorization:%d, want 1/0", auth.authKeyInfoLookups, auth.authorizationLookups)
	}
}

func TestResolveInheritedBoundTempUnsupportedPermanentBlocksRawShadow(t *testing.T) {
	rawAuthKeyID := [8]byte{0x62}
	permAuthKeyID := [8]byte{0x63}
	auth := &captureAuthService{
		resolvedAuthKeyID: permAuthKeyID,
		hasResolved:       true,
		authKeyClientInfos: map[[8]byte]domain.AuthKeyClientInfo{
			rawAuthKeyID:  {Layer: 225},
			permAuthKeyID: {Layer: 229},
		},
	}
	r := New(Config{DC: 2}, Deps{Auth: auth}, zaptest.NewLogger(t), clock.System)

	layer, authoritative, err := r.ResolveInheritedAuthKeyLayer(context.Background(), rawAuthKeyID)
	if err != nil || !authoritative || layer != 0 {
		t.Fatalf("bound future default = (%d,%v,%v), want (0,true,nil)", layer, authoritative, err)
	}
	if auth.authKeyInfoLookups != 1 {
		t.Fatalf("canonical auth-key lookups = %d, want 1", auth.authKeyInfoLookups)
	}
}

func TestBindTempAuthKeyFuturePermanentClearsInheritedUntilFreshExplicit(t *testing.T) {
	rawAuthKeyID := [8]byte{0x62, 1}
	permAuthKeyID := [8]byte{0x62, 2}
	const sessionID = int64(621)
	auth := &captureAuthService{
		resolvedAuthKeyID: permAuthKeyID,
		hasResolved:       true,
		authKeyClientInfos: map[[8]byte]domain.AuthKeyClientInfo{
			rawAuthKeyID:  {Layer: 225},
			permAuthKeyID: {Layer: 229, LayerObservationID: 44},
		},
	}
	auth.bindTempHook = func(domain.TempAuthKeyBinding) error {
		auth.authKeyClientInfos[rawAuthKeyID] = domain.AuthKeyClientInfo{Layer: 229, LayerObservationID: 44}
		return nil
	}
	sessions := &inheritedLayerCaptureSessions{}
	r := New(Config{DC: 2}, Deps{Auth: auth, Sessions: sessions}, zaptest.NewLogger(t), clock.System)
	r.setAuthKeyLayerDefaults(225, rawAuthKeyID)
	ctx := WithAuthKeyID(WithSessionID(WithRawAuthKeyID(context.Background(), rawAuthKeyID), sessionID), rawAuthKeyID)
	ok, err := r.onAuthBindTempAuthKey(ctx, &tg.AuthBindTempAuthKeyRequest{PermAuthKeyID: businessAuthKeyInt64(permAuthKeyID)})
	if err != nil || !ok {
		t.Fatalf("bind to future permanent = (%v,%v)", ok, err)
	}
	if len(sessions.clears) != 1 || sessions.clears[0] != rawAuthKeyID {
		t.Fatalf("inherited clear calls = %x", sessions.clears)
	}
	if len(sessions.refreshes) != 0 {
		t.Fatalf("future permanent refreshed stale inherited profile: %+v", sessions.refreshes)
	}
	r.clientInfoMu.RLock()
	blocked := r.authInfo[rawAuthKeyID]
	r.clientInfoMu.RUnlock()
	if blocked.layer != 0 || blocked.layerObservationID != 44 || !blocked.layerBlocked || !blocked.layerBlockedByAuthKey {
		t.Fatalf("raw blocked shadow = %+v", blocked)
	}

	// A later in-window explicit selector is new wire evidence and may correct
	// both the session and the permanent shared default.
	freezeAndPublishLayer(t, r, rawAuthKeyID, sessionID, 20, 1, 225)
	if got := auth.authKeyClientInfos[permAuthKeyID].Layer; got != 225 {
		t.Fatalf("fresh explicit did not self-heal future default: %d", got)
	}
	if got, found := r.cachedAuthKeyLayerDefault(rawAuthKeyID); !found || got != 225 {
		t.Fatalf("raw default after self-heal = (%d,%v)", got, found)
	}
}

func TestSupportedExplicitEvidenceClearsUnsupportedCacheState(t *testing.T) {
	authKeyID := [8]byte{0x63, 1}
	auth := &captureAuthService{authKeyClientInfos: map[[8]byte]domain.AuthKeyClientInfo{
		authKeyID: {Layer: 229},
	}}
	r := New(Config{DC: 2}, Deps{Auth: auth}, zaptest.NewLogger(t), clock.System)
	if layer, found, err := r.ResolveInheritedAuthKeyLayer(context.Background(), authKeyID); err != nil || !found || layer != 0 {
		t.Fatalf("future default = (%d,%v,%v), want (0,true,nil)", layer, found, err)
	}
	freezeAndPublishLayer(t, r, authKeyID, 1, 10, 1, 228)
	r.clientInfoMu.RLock()
	info := r.authInfo[authKeyID]
	r.clientInfoMu.RUnlock()
	if info.layer != 228 || info.layerBlocked || info.layerBlockedByAuthKey {
		t.Fatalf("supported and blocked cache state coexisted: %+v", info)
	}
}

type inheritedLayerResolution struct {
	layer int
	found bool
	err   error
}

type blockingLayerReadAuthService struct {
	*captureAuthService
	started chan struct{}
	release chan struct{}
	once    sync.Once
	stale   domain.AuthKeyClientInfo
}

func (s *blockingLayerReadAuthService) AuthKeyClientInfo(ctx context.Context, _ [8]byte) (domain.AuthKeyClientInfo, bool, error) {
	s.authKeyInfoLookups++
	s.once.Do(func() { close(s.started) })
	select {
	case <-s.release:
		return s.stale, true, nil
	case <-ctx.Done():
		return domain.AuthKeyClientInfo{}, false, ctx.Err()
	}
}

func TestResolveInheritedLayerDoesNotReturnStaleSingleflightRead(t *testing.T) {
	authKeyID := [8]byte{0x64}
	const sessionID = int64(640)
	auth := &blockingLayerReadAuthService{
		captureAuthService: &captureAuthService{authKeyClientInfos: make(map[[8]byte]domain.AuthKeyClientInfo)},
		started:            make(chan struct{}),
		release:            make(chan struct{}),
		stale:              domain.AuthKeyClientInfo{Layer: 225},
	}
	r := New(Config{DC: 2}, Deps{Auth: auth}, zaptest.NewLogger(t), clock.System)
	resolved := make(chan inheritedLayerResolution, 1)
	go func() {
		layer, found, err := r.ResolveInheritedAuthKeyLayer(context.Background(), authKeyID)
		resolved <- inheritedLayerResolution{layer: layer, found: found, err: err}
	}()
	<-auth.started

	freezeAndPublishLayer(t, r, authKeyID, sessionID, 100, 2, 227)
	close(auth.release)
	got := <-resolved
	if got.err != nil || !got.found || got.layer != 227 {
		t.Fatalf("resolver returned stale DB value = (%d,%v,%v), want (227,true,nil)", got.layer, got.found, got.err)
	}
}

func TestBoundDurableResolveCannotProjectStalePermanentLayerOverNewObservation(t *testing.T) {
	ctx := context.Background()
	keys := memory.NewAuthKeyStore()
	bindings := memory.NewTempAuthKeyBindingStore(keys)
	rawAuthKeyID := [8]byte{0x64, 1}
	permAuthKeyID := [8]byte{0x64, 2}
	const (
		sessionID  = int64(641)
		tempExpiry = 2_000_000_000
	)
	if err := keys.Save(ctx, store.AuthKeyData{ID: rawAuthKeyID, ExpiresAt: tempExpiry}); err != nil {
		t.Fatal(err)
	}
	if err := keys.Save(ctx, store.AuthKeyData{
		ID: permAuthKeyID, Layer: 225, LayerObservationID: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if err := bindings.Save(ctx, domain.TempAuthKeyBinding{
		TempAuthKeyID: rawAuthKeyID,
		PermAuthKeyID: int64(binary.LittleEndian.Uint64(permAuthKeyID[:])),
		ExpiresAt:     tempExpiry,
	}); err != nil {
		t.Fatal(err)
	}
	auth := &blockingLayerReadAuthService{
		captureAuthService: &captureAuthService{
			resolvedAuthKeyID: permAuthKeyID,
			hasResolved:       true,
		},
		started: make(chan struct{}),
		release: make(chan struct{}),
		stale:   domain.AuthKeyClientInfo{Layer: 225, LayerObservationID: 1},
	}
	sessions := &inheritedLayerCaptureSessions{}
	r := New(Config{DC: 2}, Deps{
		Auth: auth, Sessions: sessions, AuthKeySessionLayers: keys,
	}, zaptest.NewLogger(t), clock.System)
	resolved := make(chan inheritedLayerResolution, 1)
	go func() {
		layer, found, err := r.ResolveInheritedAuthKeyLayer(ctx, rawAuthKeyID)
		resolved <- inheritedLayerResolution{layer: layer, found: found, err: err}
	}()
	<-auth.started

	msgID := int64((uint64(time.Now().UTC().Unix()) << 32) | 4)
	if layer, gotMsgID, publish, err := r.AdvanceNegotiatedSessionLayerEvidence(
		ctx, rawAuthKeyID, sessionID, 227, msgID,
	); err != nil || layer != 227 || gotMsgID != msgID || !publish {
		t.Fatalf("advance while stale read blocked = (%d,%d,%v,%v)", layer, gotMsgID, publish, err)
	}
	if err := r.PublishAdmittedLayerProfileEvidence(ctx, rawAuthKeyID, sessionID, msgID, 2, 1, 227); err != nil {
		t.Fatal(err)
	}
	close(auth.release)
	got := <-resolved
	if got.err != nil || !got.found || got.layer != 227 {
		t.Fatalf("bound resolver returned stale permanent read = (%d,%v,%v)", got.layer, got.found, got.err)
	}

	permanent, found, err := keys.Get(ctx, permAuthKeyID)
	if err != nil || !found || permanent.Layer != 227 || permanent.LayerObservationID <= 1 {
		t.Fatalf("durable permanent tuple = (%+v,%v,%v)", permanent, found, err)
	}
	r.clientInfoMu.RLock()
	rawInfo := r.authInfo[rawAuthKeyID]
	permInfo := r.authInfo[permAuthKeyID]
	r.clientInfoMu.RUnlock()
	for id, info := range map[[8]byte]clientSessionInfo{
		rawAuthKeyID: rawInfo, permAuthKeyID: permInfo,
	} {
		if info.layer != permanent.Layer || info.layerObservationID != permanent.LayerObservationID {
			t.Fatalf("cache %x = layer/obs %d/%d, durable %d/%d",
				id, info.layer, info.layerObservationID, permanent.Layer, permanent.LayerObservationID)
		}
	}
}

func TestAdmittedLayerSequenceOrdersSharedDefaultAcrossSessions(t *testing.T) {
	rawOld := [8]byte{0x65, 1}
	rawNew := [8]byte{0x65, 2}
	permAuthKeyID := [8]byte{0x65, 3}
	auth := &captureAuthService{
		resolvedAuthKeyID:  permAuthKeyID,
		hasResolved:        true,
		authKeyClientInfos: make(map[[8]byte]domain.AuthKeyClientInfo),
	}
	r := New(Config{DC: 2}, Deps{Auth: auth}, zaptest.NewLogger(t), clock.System)
	if _, err := r.FreezeNegotiatedSessionLayerAt(rawOld, 1, 225, 10); err != nil {
		t.Fatal(err)
	}
	if _, err := r.FreezeNegotiatedSessionLayerAt(rawNew, 2, 227, 20); err != nil {
		t.Fatal(err)
	}

	if err := r.PublishAdmittedLayerProfileEvidence(context.Background(), rawNew, 2, 20, 2, 1, 227); err != nil {
		t.Fatal(err)
	}
	if err := r.PublishAdmittedLayerProfileEvidence(context.Background(), rawOld, 1, 10, 1, 1, 225); err != nil {
		t.Fatal(err)
	}
	if got := auth.authKeyClientInfos[permAuthKeyID].Layer; got != 227 {
		t.Fatalf("durable shared default rolled back = %d, want 227", got)
	}
	if got, ok := r.cachedAuthKeyLayerDefault(permAuthKeyID); !ok || got != 227 {
		t.Fatalf("memory shared default = (%d,%v), want (227,true)", got, ok)
	}
	if got, ok := r.NegotiatedSessionLayer(rawOld, 1); !ok || got != 225 {
		t.Fatalf("old live session changed = (%d,%v), want (225,true)", got, ok)
	}
}

func TestDurableObservationOrdersLocalDefaultWhenAdmissionCommitsInReverse(t *testing.T) {
	ctx := context.Background()
	keys := memory.NewAuthKeyStore()
	bindings := memory.NewTempAuthKeyBindingStore(keys)
	rawEarlierAdmission := [8]byte{0x65, 0x11}
	rawLaterAdmission := [8]byte{0x65, 0x12}
	permAuthKeyID := [8]byte{0x65, 0x13}
	const tempExpiry = 2_000_000_000
	for _, key := range []store.AuthKeyData{
		{ID: rawEarlierAdmission, ExpiresAt: tempExpiry},
		{ID: rawLaterAdmission, ExpiresAt: tempExpiry},
		{ID: permAuthKeyID},
	} {
		if err := keys.Save(ctx, key); err != nil {
			t.Fatal(err)
		}
	}
	for index, raw := range [][8]byte{rawEarlierAdmission, rawLaterAdmission} {
		if err := bindings.Save(ctx, domain.TempAuthKeyBinding{
			TempAuthKeyID: raw,
			PermAuthKeyID: int64(binary.LittleEndian.Uint64(permAuthKeyID[:])),
			Nonce:         int64(index + 1),
			ExpiresAt:     tempExpiry,
		}); err != nil {
			t.Fatal(err)
		}
	}
	auth := &captureAuthService{resolvedAuthKeyID: permAuthKeyID, hasResolved: true}
	sessions := &inheritedLayerCaptureSessions{}
	r := New(Config{DC: 2}, Deps{
		Auth: auth, Sessions: sessions, AuthKeySessionLayers: keys,
	}, zaptest.NewLogger(t), clock.System)
	now := time.Now().UTC()
	msgLaterAdmission := int64((uint64(now.Unix()) << 32) | 4)
	msgEarlierAdmission := int64((uint64(now.Unix()) << 32) | 8)

	// Admission sequence 2 reaches the permanent identity gate and commits
	// first. It is temporarily the durable/local default.
	if layer, msgID, publish, err := r.AdvanceNegotiatedSessionLayerEvidence(
		ctx, rawLaterAdmission, 2, 227, msgLaterAdmission,
	); err != nil || layer != 227 || msgID != msgLaterAdmission || !publish {
		t.Fatalf("later admission first commit = (%d,%d,%v,%v)", layer, msgID, publish, err)
	}
	if err := r.PublishAdmittedLayerProfileEvidence(
		ctx, rawLaterAdmission, 2, msgLaterAdmission, 2, 1, 227,
	); err != nil {
		t.Fatal(err)
	}

	// Admission sequence 1 was allocated earlier but commits second. Database
	// observation order is authoritative, so local authInfo/session seeding must
	// move to 225 despite the lower process admission sequence.
	if layer, msgID, publish, err := r.AdvanceNegotiatedSessionLayerEvidence(
		ctx, rawEarlierAdmission, 1, 225, msgEarlierAdmission,
	); err != nil || layer != 225 || msgID != msgEarlierAdmission || !publish {
		t.Fatalf("earlier admission late commit = (%d,%d,%v,%v)", layer, msgID, publish, err)
	}
	if err := r.PublishAdmittedLayerProfileEvidence(
		ctx, rawEarlierAdmission, 1, msgEarlierAdmission, 1, 1, 225,
	); err != nil {
		t.Fatal(err)
	}

	permanent, found, err := keys.Get(ctx, permAuthKeyID)
	if err != nil || !found || permanent.Layer != 225 || permanent.LayerObservationID <= 0 {
		t.Fatalf("durable permanent default = (%+v,%v,%v)", permanent, found, err)
	}
	r.clientInfoMu.RLock()
	local := r.authInfo[permAuthKeyID]
	r.clientInfoMu.RUnlock()
	if local.layer != permanent.Layer || local.layerObservationID != permanent.LayerObservationID {
		t.Fatalf("local default = layer/obs %d/%d, durable %d/%d",
			local.layer, local.layerObservationID, permanent.Layer, permanent.LayerObservationID)
	}
	lastSeed := 0
	for _, seed := range sessions.seeds {
		if seed.rawAuthKeyID == permAuthKeyID {
			lastSeed = seed.layer
		}
	}
	if lastSeed != 225 {
		t.Fatalf("SessionManager permanent seed = %d, want 225", lastSeed)
	}
}

type blockingLayerWriteAuthService struct {
	*captureAuthService
	mu         sync.Mutex
	started    chan struct{}
	release    chan struct{}
	updates    int
	blockFirst sync.Once
}

func (s *blockingLayerWriteAuthService) UpdateAuthKeyClientInfo(ctx context.Context, authKeyID [8]byte, info domain.AuthKeyClientInfo) error {
	s.mu.Lock()
	s.updates++
	first := s.updates == 1
	s.mu.Unlock()
	if first {
		s.blockFirst.Do(func() { close(s.started) })
		select {
		case <-s.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.captureAuthService.UpdateAuthKeyClientInfo(ctx, authKeyID, info)
}

func (s *blockingLayerWriteAuthService) layer(authKeyID [8]byte) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.authKeyClientInfos[authKeyID].Layer
}

func TestAdmittedLayerPersistentWritesFollowAdmissionSequence(t *testing.T) {
	rawOld := [8]byte{0x66, 1}
	rawNew := [8]byte{0x66, 2}
	permAuthKeyID := [8]byte{0x66, 3}
	auth := &blockingLayerWriteAuthService{
		captureAuthService: &captureAuthService{
			resolvedAuthKeyID:  permAuthKeyID,
			hasResolved:        true,
			authKeyClientInfos: make(map[[8]byte]domain.AuthKeyClientInfo),
		},
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	r := New(Config{DC: 2}, Deps{Auth: auth}, zaptest.NewLogger(t), clock.System)
	if _, err := r.FreezeNegotiatedSessionLayerAt(rawOld, 1, 225, 10); err != nil {
		t.Fatal(err)
	}
	if _, err := r.FreezeNegotiatedSessionLayerAt(rawNew, 2, 227, 20); err != nil {
		t.Fatal(err)
	}

	errs := make(chan error, 2)
	go func() { errs <- r.PublishAdmittedLayerProfileEvidence(context.Background(), rawOld, 1, 10, 1, 1, 225) }()
	<-auth.started
	go func() { errs <- r.PublishAdmittedLayerProfileEvidence(context.Background(), rawNew, 2, 20, 2, 1, 227) }()
	close(auth.release)
	for i := 0; i < 2; i++ {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	if got := auth.layer(permAuthKeyID); got != 227 {
		t.Fatalf("ordered durable default = %d, want 227", got)
	}
}

func TestAuthLayerEvidenceCapacityUsesExactSafeFloor(t *testing.T) {
	fill := func(r *Router) {
		for i := uint64(1); i <= maxAuthLayerEvidenceEntries; i++ {
			var id [8]byte
			binary.LittleEndian.PutUint64(id[:], i)
			r.authLayerEvidence[id] = authLayerDefaultEvidence{layer: 225, sequence: 1}
		}
	}
	newID := [8]byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff}

	t.Run("retired entry is evictable", func(t *testing.T) {
		r := New(Config{DC: 2}, Deps{}, zaptest.NewLogger(t), clock.System)
		fill(r)
		r.authLayerSafeEvictionFloor = 2
		applied, err := r.claimAuthLayerDefaultEvidenceLocked(227, 2, false, newID)
		if err != nil || !applied {
			t.Fatalf("claim with retired capacity = (%v,%v), want (true,nil)", applied, err)
		}
		if len(r.authLayerEvidence) != maxAuthLayerEvidenceEntries {
			t.Fatalf("evidence entries = %d, want %d", len(r.authLayerEvidence), maxAuthLayerEvidenceEntries)
		}
		if got := r.authLayerEvidence[newID]; got != (authLayerDefaultEvidence{layer: 227, sequence: 2}) {
			t.Fatalf("new evidence = %+v", got)
		}
	})

	t.Run("live entry is not evicted", func(t *testing.T) {
		r := New(Config{DC: 2}, Deps{}, zaptest.NewLogger(t), clock.System)
		fill(r)
		r.authLayerSafeEvictionFloor = 1
		applied, err := r.claimAuthLayerDefaultEvidenceLocked(227, 2, false, newID)
		if err != nil || applied {
			t.Fatalf("claim with all-live capacity = (%v,%v), want (false,nil)", applied, err)
		}
		if _, exists := r.authLayerEvidence[newID]; exists {
			t.Fatal("capacity refusal installed new evidence")
		}
	})
}

func TestAdmittedLayerSafeFloorIsMonotonic(t *testing.T) {
	r := New(Config{DC: 2}, Deps{}, zaptest.NewLogger(t), clock.System)
	first := [8]byte{0x67, 1}
	second := [8]byte{0x67, 2}
	if _, err := r.FreezeNegotiatedSessionLayerAt(first, 1, 225, 10); err != nil {
		t.Fatal(err)
	}
	if err := r.PublishAdmittedLayerProfileEvidence(context.Background(), first, 1, 10, 2, 2, 225); err != nil {
		t.Fatal(err)
	}
	if _, err := r.FreezeNegotiatedSessionLayerAt(second, 2, 227, 20); err != nil {
		t.Fatal(err)
	}
	if err := r.PublishAdmittedLayerProfileEvidence(context.Background(), second, 2, 20, 3, 1, 227); err != nil {
		t.Fatal(err)
	}
	r.clientInfoMu.RLock()
	floor := r.authLayerSafeEvictionFloor
	r.clientInfoMu.RUnlock()
	if floor != 2 {
		t.Fatalf("safe eviction floor regressed = %d, want 2", floor)
	}
}

func TestInvalidateAuthUserCachePreservesLayerEvidenceWatermark(t *testing.T) {
	rawOld := [8]byte{0x68, 1}
	permAuthKeyID := [8]byte{0x68, 2}
	auth := &captureAuthService{
		resolvedAuthKeyID:  permAuthKeyID,
		hasResolved:        true,
		authKeyClientInfos: make(map[[8]byte]domain.AuthKeyClientInfo),
	}
	r := New(Config{DC: 2}, Deps{Auth: auth}, zaptest.NewLogger(t), clock.System)
	freezeAndPublishLayer(t, r, permAuthKeyID, 2, 20, 2, 227)
	r.invalidateAuthUserCache(permAuthKeyID)
	freezeAndPublishLayer(t, r, rawOld, 1, 10, 1, 225)
	if got := auth.authKeyClientInfos[permAuthKeyID].Layer; got != 227 {
		t.Fatalf("authorization invalidation erased protocol order = %d, want 227", got)
	}
}

func TestStaleExactCorrectionSkipsSharedPublicationWithoutFailingRPC(t *testing.T) {
	authKeyID := [8]byte{0x69}
	auth := &captureAuthService{authKeyClientInfos: make(map[[8]byte]domain.AuthKeyClientInfo)}
	r := New(Config{DC: 2}, Deps{Auth: auth}, zaptest.NewLogger(t), clock.System)
	if _, err := r.FreezeNegotiatedSessionLayerAt(authKeyID, 1, 225, 10); err != nil {
		t.Fatal(err)
	}
	if _, err := r.FreezeNegotiatedSessionLayerAt(authKeyID, 1, 227, 20); err != nil {
		t.Fatal(err)
	}
	if err := r.PublishAdmittedLayerProfileEvidence(context.Background(), authKeyID, 1, 10, 1, 1, 225); err != nil {
		t.Fatalf("stale publication rejected immutable old RPC: %v", err)
	}
	if _, exists := auth.authKeyClientInfos[authKeyID]; exists {
		t.Fatal("stale exact observation published durable default")
	}
	if err := r.PublishAdmittedLayerProfileEvidence(context.Background(), authKeyID, 1, 20, 2, 2, 227); err != nil {
		t.Fatal(err)
	}
}

func TestBindTempAuthKeyUsesLiveExplicitLayerAfterExactTTL(t *testing.T) {
	clk := &exactProfileTestClock{now: time.Unix(1_700_000_000, 0)}
	rawAuthKeyID := [8]byte{0x6a, 1}
	permAuthKeyID := [8]byte{0x6a, 2}
	const sessionID = int64(106)
	auth := &captureAuthService{authKeyClientInfos: map[[8]byte]domain.AuthKeyClientInfo{
		permAuthKeyID: {Layer: 225},
	}}
	auth.bindTempHook = func(domain.TempAuthKeyBinding) error {
		// The durable raw observation was published before the in-process exact
		// registry expired. Bind merges that observation into the permanent row.
		auth.authKeyClientInfos[rawAuthKeyID] = domain.AuthKeyClientInfo{Layer: 227}
		auth.authKeyClientInfos[permAuthKeyID] = domain.AuthKeyClientInfo{Layer: 227}
		return nil
	}
	sessions := &inheritedLayerCaptureSessions{explicitLayer: 227, explicitMsgID: 100}
	r := New(Config{DC: 2}, Deps{Auth: auth, Sessions: sessions}, zaptest.NewLogger(t), clk)
	freezeAndPublishLayer(t, r, rawAuthKeyID, sessionID, 100, 5, 227)
	sessions.seeds = nil
	clk.advance(exactSessionProfileTTL + time.Second)
	if _, ok := r.NegotiatedSessionLayer(rawAuthKeyID, sessionID); ok {
		t.Fatal("test setup retained expired exact registry entry")
	}

	ctx := WithAuthKeyID(WithSessionID(WithRawAuthKeyID(context.Background(), rawAuthKeyID), sessionID), rawAuthKeyID)
	ok, err := r.onAuthBindTempAuthKey(ctx, &tg.AuthBindTempAuthKeyRequest{PermAuthKeyID: businessAuthKeyInt64(permAuthKeyID)})
	if err != nil || !ok {
		t.Fatalf("bind after exact TTL = (%v,%v)", ok, err)
	}
	if got := auth.authKeyClientInfos[permAuthKeyID].Layer; got != 227 {
		t.Fatalf("live explicit Layer was lost at bind = %d, want 227", got)
	}
	if len(sessions.refreshes) != 1 || sessions.refreshes[0].layer != 227 {
		t.Fatalf("bind refresh calls = %+v", sessions.refreshes)
	}
}

func TestNakedBindDoesNotRedateOlderExplicitEvidence(t *testing.T) {
	rawAuthKeyID := [8]byte{0x6b, 1}
	permAuthKeyID := [8]byte{0x6b, 2}
	const sessionID = int64(107)
	auth := &captureAuthService{authKeyClientInfos: make(map[[8]byte]domain.AuthKeyClientInfo)}
	auth.bindTempHook = func(domain.TempAuthKeyBinding) error {
		// The permanent observation is globally newer than the raw observation.
		// Model the transaction's observation_id merge before the router reloads.
		auth.authKeyClientInfos[rawAuthKeyID] = domain.AuthKeyClientInfo{Layer: 227}
		auth.authKeyClientInfos[permAuthKeyID] = domain.AuthKeyClientInfo{Layer: 227}
		return nil
	}
	sessions := &inheritedLayerCaptureSessions{explicitLayer: 225, explicitMsgID: 10}
	r := New(Config{DC: 2}, Deps{Auth: auth, Sessions: sessions}, zaptest.NewLogger(t), clock.System)
	freezeAndPublishLayer(t, r, rawAuthKeyID, sessionID, 10, 1, 225)
	freezeAndPublishLayer(t, r, permAuthKeyID, 2, 20, 2, 227)

	ctx := WithAuthKeyID(WithSessionID(WithRawAuthKeyID(context.Background(), rawAuthKeyID), sessionID), rawAuthKeyID)
	ctx = withLayerAdmissionSequence(ctx, 99) // naked bind order is not Layer evidence order.
	ok, err := r.onAuthBindTempAuthKey(ctx, &tg.AuthBindTempAuthKeyRequest{PermAuthKeyID: businessAuthKeyInt64(permAuthKeyID)})
	if err != nil || !ok {
		t.Fatalf("naked bind = (%v,%v)", ok, err)
	}
	if got := auth.authKeyClientInfos[permAuthKeyID].Layer; got != 227 {
		t.Fatalf("naked bind re-dated old explicit Layer = %d, want 227", got)
	}
	if got := auth.authKeyClientInfos[rawAuthKeyID].Layer; got != 227 {
		t.Fatalf("raw inherited shadow did not normalize to permanent default = %d", got)
	}
}

func TestRequestBoundBindHasNoStoreCacheOrSessionSideEffects(t *testing.T) {
	rawAuthKeyID := [8]byte{0x6c, 1}
	permAuthKeyID := [8]byte{0x6c, 2}
	oldPermAuthKeyID := [8]byte{0x6c, 3}
	const sessionID = int64(108)
	auth := &captureAuthService{authKeyClientInfos: map[[8]byte]domain.AuthKeyClientInfo{
		rawAuthKeyID:  {Layer: 227, DeviceModel: "raw-before"},
		permAuthKeyID: {Layer: 225},
	}}
	sessions := &inheritedLayerCaptureSessions{explicitLayer: 227, explicitMsgID: 10}
	r := New(Config{DC: 2}, Deps{Auth: auth, Sessions: sessions}, zaptest.NewLogger(t), clock.System)
	sessions.BindAuthKeyForSession(rawAuthKeyID, sessionID, oldPermAuthKeyID)
	beforeSession := sessions.snapshot()
	now := time.Now()
	r.tempKeyResolveCache.Store(rawAuthKeyID, oldPermAuthKeyID, now.Add(time.Hour), now)
	r.setAuthUserCache(rawAuthKeyID, 101, true)
	r.setAuthUserCache(permAuthKeyID, 202, true)
	r.clientInfoMu.Lock()
	if r.authInfo == nil {
		r.authInfo = make(map[[8]byte]clientSessionInfo)
	}
	r.authInfo[rawAuthKeyID] = clientSessionInfo{
		layer: 227, layerAdmissionSeq: 11, authKeyInfoChecked: true,
		authorizationChecked: true, hasClientInfo: true,
		clientInfo: ClientInfo{DeviceModel: "raw-cache"},
	}
	r.authInfo[permAuthKeyID] = clientSessionInfo{
		layer: 225, layerAdmissionSeq: 9, authKeyInfoChecked: true,
		authorizationChecked: true, hasClientInfo: true,
		clientInfo: ClientInfo{DeviceModel: "perm-cache"},
	}
	beforeRawInfo := r.authInfo[rawAuthKeyID]
	beforePermInfo := r.authInfo[permAuthKeyID]
	r.clientInfoMu.Unlock()

	ctx := WithAuthKeyID(WithSessionID(WithRawAuthKeyID(context.Background(), rawAuthKeyID), sessionID), rawAuthKeyID)
	ctx = r.WithLayerRPCProfileEvidenceFresh(ctx, false)
	ok, err := r.onAuthBindTempAuthKey(ctx, &tg.AuthBindTempAuthKeyRequest{PermAuthKeyID: businessAuthKeyInt64(permAuthKeyID)})
	if ok || !tgerr.Is(err, "TEMP_AUTH_KEY_EMPTY") {
		t.Fatalf("stale request-bound bind = (%v,%v), want (false,TEMP_AUTH_KEY_EMPTY)", ok, err)
	}
	if auth.bindTempCalls != 0 || auth.resolveCount != 0 || auth.authKeyInfoLookups != 0 || auth.authorizationLookups != 0 {
		t.Fatalf("stale bind touched auth store: bind=%d resolve=%d key=%d authorization=%d",
			auth.bindTempCalls, auth.resolveCount, auth.authKeyInfoLookups, auth.authorizationLookups)
	}
	if got := auth.authKeyClientInfos[rawAuthKeyID]; got != (domain.AuthKeyClientInfo{Layer: 227, DeviceModel: "raw-before"}) {
		t.Fatalf("stale bind changed raw durable fake: %+v", got)
	}
	if got := auth.authKeyClientInfos[permAuthKeyID]; got != (domain.AuthKeyClientInfo{Layer: 225}) {
		t.Fatalf("stale bind changed permanent durable fake: %+v", got)
	}
	if got := sessions.snapshot(); got != beforeSession {
		t.Fatalf("stale bind changed session binding: got %+v want %+v", got, beforeSession)
	}
	if len(sessions.seeds) != 0 || len(sessions.refreshes) != 0 || len(sessions.clears) != 0 {
		t.Fatalf("stale bind changed inherited session state: seeds=%+v refreshes=%+v clears=%x",
			sessions.seeds, sessions.refreshes, sessions.clears)
	}
	if got, found := r.tempKeyResolveCache.Get(rawAuthKeyID, oldPermAuthKeyID, now); !found || got != oldPermAuthKeyID {
		t.Fatalf("stale bind changed temp-key cache: got=%x found=%v", got, found)
	}
	r.authUserMu.RLock()
	rawUser := r.authUsers[rawAuthKeyID]
	permUser := r.authUsers[permAuthKeyID]
	r.authUserMu.RUnlock()
	if rawUser != (authUserCacheEntry{userID: 101, found: true}) || permUser != (authUserCacheEntry{userID: 202, found: true}) {
		t.Fatalf("stale bind changed auth-user cache: raw=%+v perm=%+v", rawUser, permUser)
	}
	r.clientInfoMu.RLock()
	afterRawInfo := r.authInfo[rawAuthKeyID]
	afterPermInfo := r.authInfo[permAuthKeyID]
	r.clientInfoMu.RUnlock()
	if afterRawInfo != beforeRawInfo || afterPermInfo != beforePermInfo {
		t.Fatalf("stale bind changed layer cache: raw=%+v/%+v perm=%+v/%+v",
			afterRawInfo, beforeRawInfo, afterPermInfo, beforePermInfo)
	}
}

func freezeAndPublishLayer(t *testing.T, r *Router, rawAuthKeyID [8]byte, sessionID, msgID int64, admissionSeq uint64, layer int) {
	t.Helper()
	if _, err := r.FreezeNegotiatedSessionLayerAt(rawAuthKeyID, sessionID, layer, msgID); err != nil {
		t.Fatal(err)
	}
	if err := r.PublishAdmittedLayerProfileEvidence(context.Background(), rawAuthKeyID, sessionID, msgID, admissionSeq, 1, layer); err != nil {
		t.Fatal(err)
	}
}
