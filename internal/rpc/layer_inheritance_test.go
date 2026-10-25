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

// layerEvidenceTestStore is the Router-level fake for tests which exercise
// publication ordering rather than the store's MTProto freshness validation.
// Store packages cover msg_id validation separately; Router tests explicitly
// inject this production-shaped authority and never rely on a local fallback.
type layerEvidenceTestStore struct {
	mu       sync.Mutex
	next     int64
	sessions map[clientInfoSessionKey]store.AuthKeySessionLayer
	defaults map[[8]byte]store.AuthKeyLayerDefault
	aliases  map[[8]byte][8]byte
	reads    int
}

func newLayerEvidenceTestStore() *layerEvidenceTestStore {
	return &layerEvidenceTestStore{
		sessions: make(map[clientInfoSessionKey]store.AuthKeySessionLayer),
		defaults: make(map[[8]byte]store.AuthKeyLayerDefault),
		aliases:  make(map[[8]byte][8]byte),
	}
}

func (s *layerEvidenceTestStore) canonicalLocked(authKeyID [8]byte) [8]byte {
	if canonical, ok := s.aliases[authKeyID]; ok {
		return canonical
	}
	return authKeyID
}

func (s *layerEvidenceTestStore) bind(rawAuthKeyID, permAuthKeyID [8]byte) {
	s.mu.Lock()
	rawDefault := s.defaults[s.canonicalLocked(rawAuthKeyID)]
	permDefault := s.defaults[s.canonicalLocked(permAuthKeyID)]
	if rawDefault.ObservationID > permDefault.ObservationID {
		permDefault = rawDefault
	}
	s.defaults[permAuthKeyID] = permDefault
	delete(s.defaults, rawAuthKeyID)
	s.aliases[rawAuthKeyID] = permAuthKeyID
	s.mu.Unlock()
}

func (s *layerEvidenceTestStore) seedDefault(authKeyID [8]byte, layer int, observationID int64) {
	s.mu.Lock()
	if observationID <= 0 {
		s.next++
		observationID = s.next
	} else if observationID > s.next {
		s.next = observationID
	}
	s.defaults[s.canonicalLocked(authKeyID)] = store.AuthKeyLayerDefault{Layer: layer, ObservationID: observationID}
	s.mu.Unlock()
}

func (s *layerEvidenceTestStore) GetSessionLayer(_ context.Context, rawAuthKeyID [8]byte, sessionID int64) (store.AuthKeySessionLayer, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, found := s.sessions[clientInfoSessionKey{rawAuthKeyID: rawAuthKeyID, sessionID: sessionID}]
	if found {
		def := s.defaults[s.canonicalLocked(rawAuthKeyID)]
		value.SharedDefault = def.Layer == value.Layer && def.ObservationID == value.ObservationID
	}
	return value, found, nil
}

func (s *layerEvidenceTestStore) AdvanceSessionLayer(_ context.Context, rawAuthKeyID [8]byte, sessionID int64, layer int, msgID int64) (store.AuthKeySessionLayer, bool, error) {
	if layer <= 0 || msgID <= 0 {
		return store.AuthKeySessionLayer{}, false, store.ErrAuthKeySessionLayerInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := clientInfoSessionKey{rawAuthKeyID: rawAuthKeyID, sessionID: sessionID}
	if current, found := s.sessions[key]; found {
		if msgID < current.MessageID {
			def := s.defaults[s.canonicalLocked(rawAuthKeyID)]
			current.SharedDefault = def.Layer == current.Layer && def.ObservationID == current.ObservationID
			return current, false, nil
		}
		if msgID == current.MessageID {
			if layer != current.Layer {
				return current, false, store.ErrAuthKeySessionLayerConflict
			}
			def := s.defaults[s.canonicalLocked(rawAuthKeyID)]
			current.SharedDefault = def.Layer == current.Layer && def.ObservationID == current.ObservationID
			return current, false, nil
		}
	}
	s.next++
	current := store.AuthKeySessionLayer{
		Layer: layer, MessageID: msgID, ObservationID: s.next,
		ExpiresAt: time.Now().Add(time.Hour), SharedDefault: true,
	}
	s.sessions[key] = current
	s.defaults[s.canonicalLocked(rawAuthKeyID)] = store.AuthKeyLayerDefault{Layer: layer, ObservationID: current.ObservationID}
	return current, true, nil
}

func (s *layerEvidenceTestStore) GetAuthKeyLayerDefault(_ context.Context, authKeyID [8]byte) (store.AuthKeyLayerDefault, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reads++
	value, found := s.defaults[s.canonicalLocked(authKeyID)]
	return value, found, nil
}

func (s *layerEvidenceTestStore) DeleteSessionLayer(_ context.Context, rawAuthKeyID [8]byte, sessionID int64) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := clientInfoSessionKey{rawAuthKeyID: rawAuthKeyID, sessionID: sessionID}
	_, found := s.sessions[key]
	delete(s.sessions, key)
	return found, nil
}

func (s *layerEvidenceTestStore) DeleteExpiredSessionLayers(context.Context, int) (int, error) {
	return 0, nil
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
		{name: "unsupported primary is authoritative unknown", keyLayer: testFutureLayer, authorization: 227, found: true},
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
			layers := newLayerEvidenceTestStore()
			if tt.keyLayer > 0 {
				layers.seedDefault(authKeyID, tt.keyLayer, 1)
			}
			r := New(Config{DC: 2}, Deps{Auth: auth, AuthKeySessionLayers: layers}, zaptest.NewLogger(t), clock.System)
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
			if layers.reads != 2 {
				t.Fatalf("durable default reads = %d, want one per new-session resolve", layers.reads)
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
	keys := memory.NewAuthKeyStore()
	if err := keys.Save(context.Background(), store.AuthKeyData{ID: rawAuthKeyID, ExpiresAt: 2_000_000_000, Layer: 225, LayerObservationID: 1}); err != nil {
		t.Fatal(err)
	}
	if err := keys.Save(context.Background(), store.AuthKeyData{ID: permAuthKeyID, Layer: 227, LayerObservationID: 2}); err != nil {
		t.Fatal(err)
	}
	bindings := memory.NewTempAuthKeyBindingStore(keys)
	if err := bindings.Save(context.Background(), domain.TempAuthKeyBinding{
		TempAuthKeyID: rawAuthKeyID,
		PermAuthKeyID: businessAuthKeyInt64(permAuthKeyID),
		ExpiresAt:     2_000_000_000,
	}); err != nil {
		t.Fatal(err)
	}
	r := New(Config{DC: 2}, Deps{AuthKeySessionLayers: keys}, zaptest.NewLogger(t), clock.System)

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

func TestResolveInheritedAuthKeyLayerFailsClosedWithoutAvailabilityMarker(t *testing.T) {
	boom := errors.New("Redis temporarily unavailable")
	for _, tt := range []struct {
		name   string
		layers store.AuthKeySessionLayerStore
		want   error
	}{
		{
			name: "missing durable authority",
			want: store.ErrAuthKeySessionLayerStoreRequired,
		},
		{
			name:   "Redis default lookup outage",
			layers: unavailableSessionLayerStore{err: boom},
			want:   boom,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := New(Config{DC: 2}, Deps{AuthKeySessionLayers: tt.layers}, zaptest.NewLogger(t), clock.System)
			layer, found, err := r.ResolveInheritedAuthKeyLayer(context.Background(), [8]byte{0x33})
			if layer != 0 || found || !errors.Is(err, tt.want) {
				t.Fatalf("resolution = (%d,%v,%v), want (0,false,%v)", layer, found, err, tt.want)
			}
			var marker interface{ LayerEvidenceDurabilityUnavailable() }
			if errors.As(err, &marker) {
				t.Fatal("obsolete local-fallback marker must not escape")
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
	keys := memory.NewAuthKeyStore()
	if err := keys.Save(context.Background(), store.AuthKeyData{ID: authKeyID}); err != nil {
		t.Fatal(err)
	}
	r := New(Config{DC: 2}, Deps{Auth: auth, AuthKeySessionLayers: keys}, zaptest.NewLogger(t), clock.System)

	layer, found, err := r.ResolveInheritedAuthKeyLayer(context.Background(), authKeyID)
	if err != nil || found || layer != 0 {
		t.Fatalf("durable zero primary = (%d,%v,%v), want (0,false,nil)", layer, found, err)
	}
	if auth.authKeyInfoLookups != 0 || auth.authorizationLookups != 0 {
		t.Fatalf("legacy auth metadata was consulted: auth_key:%d authorization:%d",
			auth.authKeyInfoLookups, auth.authorizationLookups)
	}
}

func TestExplicitLayerCorrectionUpdatesDefaultWithoutChangingSibling(t *testing.T) {
	authKeyID := [8]byte{0x41}
	layers := newLayerEvidenceTestStore()
	r := New(Config{DC: 2}, Deps{AuthKeySessionLayers: layers}, zaptest.NewLogger(t), clock.System)
	freezeAndPublishLayer(t, r, authKeyID, 1, 10, 1, 225)
	freezeAndPublishLayer(t, r, authKeyID, 2, 11, 2, 225)
	freezeAndPublishLayer(t, r, authKeyID, 1, 20, 3, 227)

	if layer, ok := r.NegotiatedSessionLayer(authKeyID, 1); !ok || layer != 227 {
		t.Fatalf("corrected session = (%d,%v), want (227,true)", layer, ok)
	}
	if layer, ok := r.NegotiatedSessionLayer(authKeyID, 2); !ok || layer != 225 {
		t.Fatalf("sibling session = (%d,%v), want unchanged (225,true)", layer, ok)
	}
	if got, found, err := layers.GetAuthKeyLayerDefault(context.Background(), authKeyID); err != nil || !found || got.Layer != 227 {
		t.Fatalf("durable shared default = (%+v,%v,%v), want 227", got, found, err)
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
			layers := newLayerEvidenceTestStore()
			layers.seedDefault(permAuthKeyID, 225, 1)
			auth := &captureAuthService{authKeyClientInfos: map[[8]byte]domain.AuthKeyClientInfo{
				permAuthKeyID: {Layer: 225},
			}}
			// Model the store-owned bind transaction. The router must only reload
			// this merged permanent primary; it must not derive or persist a winner
			// from its process-local exact-session registry after Bind returns.
			auth.bindTempHook = func(domain.TempAuthKeyBinding) error {
				layers.bind(rawAuthKeyID, permAuthKeyID)
				merged, _, _ := layers.GetAuthKeyLayerDefault(context.Background(), permAuthKeyID)
				auth.authKeyClientInfos[rawAuthKeyID] = domain.AuthKeyClientInfo{Layer: merged.Layer, LayerObservationID: merged.ObservationID}
				auth.authKeyClientInfos[permAuthKeyID] = domain.AuthKeyClientInfo{Layer: merged.Layer, LayerObservationID: merged.ObservationID}
				return nil
			}
			sessions := &inheritedLayerCaptureSessions{}
			r := New(Config{DC: 2}, Deps{Auth: auth, Sessions: sessions, AuthKeySessionLayers: layers}, zaptest.NewLogger(t), clock.System)
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
			if rawCached.layerObservationID <= 0 || rawCached.layerObservationID != permCached.layerObservationID {
				t.Fatalf("bound observation shadow = raw:%d perm:%d, want one positive shared observation",
					rawCached.layerObservationID, permCached.layerObservationID)
			}
		})
	}
}

func TestResolveInheritedAuthKeyLayerRevalidatesTerminalMissingRows(t *testing.T) {
	authKeyID := [8]byte{0x61}
	layers := newLayerEvidenceTestStore()
	r := New(Config{DC: 2}, Deps{AuthKeySessionLayers: layers}, zaptest.NewLogger(t), clock.System)

	for i := 0; i < 2; i++ {
		layer, found, err := r.ResolveInheritedAuthKeyLayer(context.Background(), authKeyID)
		if err != nil || found || layer != 0 {
			t.Fatalf("resolve %d = (%d,%v,%v), want (0,false,nil)", i, layer, found, err)
		}
	}
	if layers.reads != 2 {
		t.Fatalf("terminal missing durable reads = %d, want one per new-session resolve", layers.reads)
	}
}

func TestResolveInheritedBoundTempUnsupportedPermanentBlocksRawShadow(t *testing.T) {
	rawAuthKeyID := [8]byte{0x62}
	permAuthKeyID := [8]byte{0x63}
	layers := newLayerEvidenceTestStore()
	layers.seedDefault(rawAuthKeyID, 225, 1)
	layers.seedDefault(permAuthKeyID, testFutureLayer, 2)
	layers.bind(rawAuthKeyID, permAuthKeyID)
	r := New(Config{DC: 2}, Deps{AuthKeySessionLayers: layers}, zaptest.NewLogger(t), clock.System)

	layer, authoritative, err := r.ResolveInheritedAuthKeyLayer(context.Background(), rawAuthKeyID)
	if err != nil || !authoritative || layer != 0 {
		t.Fatalf("bound future default = (%d,%v,%v), want (0,true,nil)", layer, authoritative, err)
	}
}

func TestBindTempAuthKeyFuturePermanentClearsInheritedUntilFreshExplicit(t *testing.T) {
	rawAuthKeyID := [8]byte{0x62, 1}
	permAuthKeyID := [8]byte{0x62, 2}
	const sessionID = int64(621)
	layers := newLayerEvidenceTestStore()
	layers.seedDefault(rawAuthKeyID, 225, 1)
	layers.seedDefault(permAuthKeyID, testFutureLayer, 44)
	auth := &captureAuthService{
		resolvedAuthKeyID: permAuthKeyID,
		hasResolved:       true,
		authKeyClientInfos: map[[8]byte]domain.AuthKeyClientInfo{
			rawAuthKeyID:  {Layer: 225},
			permAuthKeyID: {Layer: testFutureLayer, LayerObservationID: 44},
		},
	}
	auth.bindTempHook = func(domain.TempAuthKeyBinding) error {
		layers.bind(rawAuthKeyID, permAuthKeyID)
		auth.authKeyClientInfos[rawAuthKeyID] = domain.AuthKeyClientInfo{Layer: testFutureLayer, LayerObservationID: 44}
		return nil
	}
	sessions := &inheritedLayerCaptureSessions{}
	r := New(Config{DC: 2}, Deps{Auth: auth, Sessions: sessions, AuthKeySessionLayers: layers}, zaptest.NewLogger(t), clock.System)
	r.clientInfoMu.Lock()
	r.rememberAuthClientLayerLocked(rawAuthKeyID, 225)
	r.clientInfoMu.Unlock()
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
	if blocked.layer != testFutureLayer || blocked.layerObservationID != 44 || !blocked.layerBlocked || !blocked.layerBlockedByAuthKey {
		t.Fatalf("raw blocked shadow = %+v", blocked)
	}

	// A later in-window explicit selector is new wire evidence and may correct
	// both the session and the permanent shared default.
	freezeAndPublishLayer(t, r, rawAuthKeyID, sessionID, 20, 1, 225)
	if got, found, err := layers.GetAuthKeyLayerDefault(context.Background(), permAuthKeyID); err != nil || !found || got.Layer != 225 {
		t.Fatalf("fresh explicit did not self-heal future default: (%+v,%v,%v)", got, found, err)
	}
	if got, found := r.cachedAuthKeyLayerDefault(rawAuthKeyID); !found || got != 225 {
		t.Fatalf("raw default after self-heal = (%d,%v)", got, found)
	}
}

func TestSupportedExplicitEvidenceClearsUnsupportedCacheState(t *testing.T) {
	authKeyID := [8]byte{0x63, 1}
	layers := newLayerEvidenceTestStore()
	layers.seedDefault(authKeyID, testFutureLayer, 1)
	r := New(Config{DC: 2}, Deps{AuthKeySessionLayers: layers}, zaptest.NewLogger(t), clock.System)
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

type blockingLayerReadStore struct {
	store.AuthKeySessionLayerStore
	started chan struct{}
	release chan struct{}
	once    sync.Once
	stale   store.AuthKeyLayerDefault
}

func (s *blockingLayerReadStore) GetAuthKeyLayerDefault(ctx context.Context, _ [8]byte) (store.AuthKeyLayerDefault, bool, error) {
	s.once.Do(func() { close(s.started) })
	select {
	case <-s.release:
		return s.stale, true, nil
	case <-ctx.Done():
		return store.AuthKeyLayerDefault{}, false, ctx.Err()
	}
}

func TestResolveInheritedLayerDoesNotReturnStaleSingleflightRead(t *testing.T) {
	authKeyID := [8]byte{0x64}
	const sessionID = int64(640)
	primary := newLayerEvidenceTestStore()
	primary.seedDefault(authKeyID, 225, 1)
	layers := &blockingLayerReadStore{
		AuthKeySessionLayerStore: primary,
		started:                  make(chan struct{}),
		release:                  make(chan struct{}),
		stale:                    store.AuthKeyLayerDefault{Layer: 225, ObservationID: 1},
	}
	r := New(Config{DC: 2}, Deps{AuthKeySessionLayers: layers}, zaptest.NewLogger(t), clock.System)
	resolved := make(chan inheritedLayerResolution, 1)
	go func() {
		layer, found, err := r.ResolveInheritedAuthKeyLayer(context.Background(), authKeyID)
		resolved <- inheritedLayerResolution{layer: layer, found: found, err: err}
	}()
	<-layers.started

	freezeAndPublishLayer(t, r, authKeyID, sessionID, 100, 2, 227)
	close(layers.release)
	got := <-resolved
	if got.err != nil || !got.found || got.layer != 227 {
		t.Fatalf("resolver returned stale DB value = (%d,%v,%v), want (227,true,nil)", got.layer, got.found, got.err)
	}
}

func TestResolveInheritedLayerDoesNotUnblockNewerFutureObservation(t *testing.T) {
	authKeyID := [8]byte{0x64, 3}
	primary := newLayerEvidenceTestStore()
	primary.seedDefault(authKeyID, 225, 1)
	layers := &blockingLayerReadStore{
		AuthKeySessionLayerStore: primary,
		started:                  make(chan struct{}),
		release:                  make(chan struct{}),
		stale:                    store.AuthKeyLayerDefault{Layer: 225, ObservationID: 1},
	}
	r := New(Config{DC: 2}, Deps{AuthKeySessionLayers: layers}, zaptest.NewLogger(t), clock.System)
	resolved := make(chan inheritedLayerResolution, 1)
	go func() {
		layer, found, err := r.ResolveInheritedAuthKeyLayer(context.Background(), authKeyID)
		resolved <- inheritedLayerResolution{layer: layer, found: found, err: err}
	}()
	<-layers.started

	r.cacheAuthKeyClientInfo(authKeyID, clientSessionInfo{
		layer:                 testFutureLayer,
		layerObservationID:    2,
		layerBlocked:          true,
		layerBlockedByAuthKey: true,
		authKeyInfoChecked:    true,
	})
	close(layers.release)
	got := <-resolved
	if got.err != nil || !got.found || got.layer != 0 {
		t.Fatalf("resolver unblocked stale Layer over future observation = (%d,%v,%v)", got.layer, got.found, got.err)
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
	auth := &captureAuthService{
		resolvedAuthKeyID: permAuthKeyID,
		hasResolved:       true,
	}
	layers := &blockingLayerReadStore{
		AuthKeySessionLayerStore: keys,
		started:                  make(chan struct{}),
		release:                  make(chan struct{}),
		stale:                    store.AuthKeyLayerDefault{Layer: 225, ObservationID: 1},
	}
	sessions := &inheritedLayerCaptureSessions{}
	r := New(Config{DC: 2}, Deps{
		Auth: auth, Sessions: sessions, AuthKeySessionLayers: layers,
	}, zaptest.NewLogger(t), clock.System)
	resolved := make(chan inheritedLayerResolution, 1)
	go func() {
		layer, found, err := r.ResolveInheritedAuthKeyLayer(ctx, rawAuthKeyID)
		resolved <- inheritedLayerResolution{layer: layer, found: found, err: err}
	}()
	<-layers.started

	msgID := int64((uint64(time.Now().UTC().Unix()) << 32) | 4)
	if layer, gotMsgID, publish, err := r.AdvanceNegotiatedSessionLayerEvidence(
		ctx, rawAuthKeyID, sessionID, 227, msgID,
	); err != nil || layer != 227 || gotMsgID != msgID || !publish {
		t.Fatalf("advance while stale read blocked = (%d,%d,%v,%v)", layer, gotMsgID, publish, err)
	}
	if err := r.PublishAdmittedLayerProfileEvidence(ctx, rawAuthKeyID, sessionID, msgID, 2, 1, 227); err != nil {
		t.Fatal(err)
	}
	close(layers.release)
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
	r.clientInfoMu.RUnlock()
	if rawInfo.layer != permanent.Layer || rawInfo.layerObservationID != permanent.LayerObservationID {
		t.Fatalf("raw cache = layer/obs %d/%d, canonical durable %d/%d",
			rawInfo.layer, rawInfo.layerObservationID, permanent.Layer, permanent.LayerObservationID)
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
	local := r.authInfo[rawEarlierAdmission]
	r.clientInfoMu.RUnlock()
	if local.layer != permanent.Layer || local.layerObservationID != permanent.LayerObservationID {
		t.Fatalf("raw local default = layer/obs %d/%d, canonical durable %d/%d",
			local.layer, local.layerObservationID, permanent.Layer, permanent.LayerObservationID)
	}
	lastSeed := 0
	for _, seed := range sessions.seeds {
		if seed.rawAuthKeyID == rawEarlierAdmission {
			lastSeed = seed.layer
		}
	}
	if lastSeed != 225 {
		t.Fatalf("SessionManager raw seed = %d, want 225", lastSeed)
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
	layers := newLayerEvidenceTestStore()
	r := New(Config{DC: 2}, Deps{AuthKeySessionLayers: layers}, zaptest.NewLogger(t), clock.System)
	first := [8]byte{0x67, 1}
	second := [8]byte{0x67, 2}
	if _, _, _, err := r.AdvanceNegotiatedSessionLayerEvidence(context.Background(), first, 1, 225, 10); err != nil {
		t.Fatal(err)
	}
	if _, err := r.FreezeNegotiatedSessionLayerAt(first, 1, 225, 10); err != nil {
		t.Fatal(err)
	}
	if err := r.PublishAdmittedLayerProfileEvidence(context.Background(), first, 1, 10, 2, 2, 225); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := r.AdvanceNegotiatedSessionLayerEvidence(context.Background(), second, 2, 227, 20); err != nil {
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
	permAuthKeyID := [8]byte{0x68, 2}
	layers := newLayerEvidenceTestStore()
	layers.seedDefault(permAuthKeyID, 227, 2)
	r := New(Config{DC: 2}, Deps{AuthKeySessionLayers: layers}, zaptest.NewLogger(t), clock.System)
	if layer, found, err := r.ResolveInheritedAuthKeyLayer(context.Background(), permAuthKeyID); err != nil || !found || layer != 227 {
		t.Fatalf("seed resolve = (%d,%v,%v)", layer, found, err)
	}
	r.invalidateAuthUserCache(permAuthKeyID)
	r.clientInfoMu.Lock()
	applied, err := r.claimAuthLayerDefaultEvidenceLocked(225, 1, true, permAuthKeyID)
	r.clientInfoMu.Unlock()
	if err != nil || applied {
		t.Fatalf("older observation claim after invalidation = (%v,%v), want (false,nil)", applied, err)
	}
	if got, found := r.cachedAuthKeyLayerDefault(permAuthKeyID); !found || got != 227 {
		t.Fatalf("authorization invalidation erased protocol order = (%d,%v)", got, found)
	}
}

func TestStaleExactCorrectionSkipsSharedPublicationWithoutFailingRPC(t *testing.T) {
	authKeyID := [8]byte{0x69}
	layers := newLayerEvidenceTestStore()
	r := New(Config{DC: 2}, Deps{AuthKeySessionLayers: layers}, zaptest.NewLogger(t), clock.System)
	if _, _, _, err := r.AdvanceNegotiatedSessionLayerEvidence(context.Background(), authKeyID, 1, 225, 10); err != nil {
		t.Fatal(err)
	}
	if _, err := r.FreezeNegotiatedSessionLayerAt(authKeyID, 1, 225, 10); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := r.AdvanceNegotiatedSessionLayerEvidence(context.Background(), authKeyID, 1, 227, 20); err != nil {
		t.Fatal(err)
	}
	if _, err := r.FreezeNegotiatedSessionLayerAt(authKeyID, 1, 227, 20); err != nil {
		t.Fatal(err)
	}
	if err := r.PublishAdmittedLayerProfileEvidence(context.Background(), authKeyID, 1, 10, 1, 1, 225); err != nil {
		t.Fatalf("stale publication rejected immutable old RPC: %v", err)
	}
	if _, found := r.cachedAuthKeyLayerDefault(authKeyID); found {
		t.Fatal("stale exact observation published process-local default")
	}
	if err := r.PublishAdmittedLayerProfileEvidence(context.Background(), authKeyID, 1, 20, 2, 2, 227); err != nil {
		t.Fatal(err)
	}
	if got, found := r.cachedAuthKeyLayerDefault(authKeyID); !found || got != 227 {
		t.Fatalf("fresh exact publication = (%d,%v)", got, found)
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
	if r.deps.AuthKeySessionLayers == nil {
		r.deps.AuthKeySessionLayers = newLayerEvidenceTestStore()
	}
	if gotLayer, gotMsgID, _, err := r.AdvanceNegotiatedSessionLayerEvidence(
		context.Background(), rawAuthKeyID, sessionID, layer, msgID,
	); err != nil || gotLayer != layer || gotMsgID != msgID {
		t.Fatalf("advance durable layer evidence = (%d,%d,%v), want (%d,%d,nil)", gotLayer, gotMsgID, err, layer, msgID)
	}
	if _, err := r.FreezeNegotiatedSessionLayerAt(rawAuthKeyID, sessionID, layer, msgID); err != nil {
		t.Fatal(err)
	}
	if err := r.PublishAdmittedLayerProfileEvidence(context.Background(), rawAuthKeyID, sessionID, msgID, admissionSeq, 1, layer); err != nil {
		t.Fatal(err)
	}
}
