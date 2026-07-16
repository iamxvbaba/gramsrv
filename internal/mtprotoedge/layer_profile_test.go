package mtprotoedge

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/iamxvbaba/td/tlprofile"
)

type countingInheritedLayerResolver struct {
	LayerRPCHandler
	calls int
	layer int
	found bool
	err   error
}

type orderedSessionLayerResolver struct {
	LayerRPCHandler
	layer int
	msgID int64
	found bool
}

func (r *orderedSessionLayerResolver) NegotiatedSessionLayerEvidence([8]byte, int64) (int, int64, bool) {
	return r.layer, r.msgID, r.found
}

func (r *countingInheritedLayerResolver) ResolveInheritedAuthKeyLayer(context.Context, [8]byte) (int, bool, error) {
	r.calls++
	return r.layer, r.found, r.err
}

func TestConnLayerProfileUnknownFreezeAndIdempotence(t *testing.T) {
	c := &Conn{}
	if profile, ok := c.LayerProfile(); ok || profile != 0 {
		t.Fatalf("initial LayerProfile = (%d, %v), want (0, false)", profile, ok)
	}

	if err := c.FreezeLayerProfile(tlprofile.Profile225); err != nil {
		t.Fatalf("freeze layer 225: %v", err)
	}
	if err := c.FreezeLayerProfile(tlprofile.Profile225); err != nil {
		t.Fatalf("repeat freeze layer 225: %v", err)
	}
	if profile, ok := c.LayerProfile(); !ok || profile != tlprofile.Profile225 {
		t.Fatalf("LayerProfile = (%d, %v), want (225, true)", profile, ok)
	}
}

func TestConnLayerProfileInheritedCanBeCorrectedExplicitly(t *testing.T) {
	c := &Conn{}
	if err := c.SeedInheritedLayerProfile(tlprofile.Profile225); err != nil {
		t.Fatalf("seed inherited layer 225: %v", err)
	}
	initial := c.LayerProfileState()
	if initial.Profile != tlprofile.Profile225 || initial.Origin != LayerProfileInherited || initial.Epoch != 1 {
		t.Fatalf("initial inherited state = %#v", initial)
	}
	if err := c.SeedInheritedLayerProfile(tlprofile.Profile226); err != nil {
		t.Fatalf("repeat inherited seed: %v", err)
	}
	if got := c.LayerProfileState(); got != initial {
		t.Fatalf("second inherited seed replaced selected default: got %#v want %#v", got, initial)
	}
	if err := c.FreezeLayerProfile(tlprofile.Profile225); err != nil {
		t.Fatalf("promote inherited evidence: %v", err)
	}
	promoted := c.LayerProfileState()
	if promoted.Profile != tlprofile.Profile225 || promoted.Origin != LayerProfileExplicit || promoted.Epoch != initial.Epoch+1 {
		t.Fatalf("promoted explicit state = %#v", promoted)
	}
	if err := c.FreezeLayerProfile(tlprofile.Profile227); err != nil {
		t.Fatalf("correct explicit layer: %v", err)
	}
	corrected := c.LayerProfileState()
	if corrected.Profile != tlprofile.Profile227 || corrected.Origin != LayerProfileExplicit || corrected.Epoch != promoted.Epoch+1 {
		t.Fatalf("corrected explicit state = %#v", corrected)
	}
}

func TestConnSeedLayerProfile(t *testing.T) {
	c := &Conn{}
	if err := c.SeedLayerProfile(tlprofile.Profile226); err != nil {
		t.Fatalf("seed layer 226: %v", err)
	}
	if err := c.SeedLayerProfile(tlprofile.Profile226); err != nil {
		t.Fatalf("repeat seed layer 226: %v", err)
	}
	if err := c.FreezeLayerProfile(tlprofile.Profile226); err != nil {
		t.Fatalf("freeze seeded layer 226: %v", err)
	}
	if profile, ok := c.LayerProfile(); !ok || profile != tlprofile.Profile226 {
		t.Fatalf("LayerProfile = (%d, %v), want (226, true)", profile, ok)
	}
}

func TestConnLayerProfileRejectsUnsupported(t *testing.T) {
	for _, profile := range []tlprofile.Profile{0, 219, 229} {
		t.Run(fmt.Sprintf("layer_%d", profile), func(t *testing.T) {
			c := &Conn{}
			if err := c.FreezeLayerProfile(profile); !errors.Is(err, ErrLayerProfileUnsupported) {
				t.Fatalf("FreezeLayerProfile(%d) error = %v, want ErrLayerProfileUnsupported", profile, err)
			}
			if err := c.SeedLayerProfile(profile); !errors.Is(err, ErrLayerProfileUnsupported) {
				t.Fatalf("SeedLayerProfile(%d) error = %v, want ErrLayerProfileUnsupported", profile, err)
			}
			if got, ok := c.LayerProfile(); ok || got != 0 {
				t.Fatalf("LayerProfile after invalid values = (%d, %v), want (0, false)", got, ok)
			}
		})
	}
}

func TestConnLayerProfileConcurrentCorrectionsRemainAtomic(t *testing.T) {
	const goroutines = 128
	c := &Conn{}
	start := make(chan struct{})
	errs := make([]error, goroutines)
	profiles := make([]tlprofile.Profile, goroutines)
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := range goroutines {
		profile := tlprofile.Profile225
		if i%2 != 0 {
			profile = tlprofile.Profile227
		}
		profiles[i] = profile
		go func(index int, requested tlprofile.Profile) {
			defer wg.Done()
			<-start
			errs[index] = c.FreezeLayerProfile(requested)
		}(i, profile)
	}
	close(start)
	wg.Wait()

	state := c.LayerProfileState()
	if state.Origin != LayerProfileExplicit || (state.Profile != tlprofile.Profile225 && state.Profile != tlprofile.Profile227) {
		t.Fatalf("concurrent final state = %#v, want supported explicit contender", state)
	}
	if state.Epoch == 0 || state.Epoch > goroutines {
		t.Fatalf("concurrent epoch = %d, want 1..%d", state.Epoch, goroutines)
	}
	for i, err := range errs {
		if err != nil {
			t.Fatalf("correction contender %d (%d) returned error: %v", i, profiles[i], err)
		}
	}
}

func TestConnLayerProfileEvidenceUsesClientMessageOrder(t *testing.T) {
	c := &Conn{}
	if err := c.seedOrderedLayerProfile(tlprofile.Profile225, 100); err != nil {
		t.Fatal(err)
	}
	if applied, err := c.FreezeLayerProfileAt(tlprofile.Profile227, 104); err != nil || !applied {
		t.Fatalf("newer correction applied=%v err=%v", applied, err)
	}
	corrected := c.LayerProfileState()
	if applied, err := c.FreezeLayerProfileAt(tlprofile.Profile225, 100); err != nil || applied {
		t.Fatalf("old duplicate applied=%v err=%v", applied, err)
	}
	if got := c.LayerProfileState(); got != corrected {
		t.Fatalf("old duplicate changed profile: got %#v want %#v", got, corrected)
	}
	if applied, err := c.FreezeLayerProfileAt(tlprofile.Profile225, 104); !errors.Is(err, ErrLayerProfileConflict) || applied {
		t.Fatalf("same-msg conflicting evidence applied=%v err=%v", applied, err)
	}
	if applied, err := c.FreezeLayerProfileAt(tlprofile.Profile227, 108); err != nil || !applied {
		t.Fatalf("same-layer newer evidence applied=%v err=%v", applied, err)
	}
	state, msgID := c.layerProfileEvidenceState()
	if state != corrected || msgID != 108 {
		t.Fatalf("same-layer cursor advance = state:%#v msgID:%d, want state:%#v msgID:108", state, msgID, corrected)
	}
}

func TestSessionManagerSeedsOnlyUnknownRawAuthKeyConnections(t *testing.T) {
	m := NewSessionManager(nil)
	authKeyID := [8]byte{2, 2, 7}
	unknown := &Conn{authKeyID: authKeyID, sessionID: 1}
	explicit := &Conn{authKeyID: authKeyID, sessionID: 2}
	inherited := &Conn{authKeyID: authKeyID, sessionID: 3}
	if err := explicit.FreezeLayerProfile(tlprofile.Profile225); err != nil {
		t.Fatal(err)
	}
	if err := inherited.SeedInheritedLayerProfile(tlprofile.Profile226); err != nil {
		t.Fatal(err)
	}
	for _, c := range []*Conn{unknown, explicit, inherited} {
		if err := m.Register(c); err != nil {
			t.Fatal(err)
		}
	}

	if seeded := m.SeedInheritedLayerForRawAuthKey(authKeyID, 227); seeded != 1 {
		t.Fatalf("seeded connections = %d, want 1", seeded)
	}
	if got := unknown.LayerProfileState(); got.Profile != tlprofile.Profile227 || got.Origin != LayerProfileInherited {
		t.Fatalf("unknown connection seed = %#v", got)
	}
	if got := explicit.LayerProfileState(); got.Profile != tlprofile.Profile225 || got.Origin != LayerProfileExplicit {
		t.Fatalf("explicit connection was overwritten = %#v", got)
	}
	if got := inherited.LayerProfileState(); got.Profile != tlprofile.Profile226 || got.Origin != LayerProfileInherited {
		t.Fatalf("existing inherited connection was overwritten = %#v", got)
	}
	if seeded := m.SeedInheritedLayerForRawAuthKey(authKeyID, 229); seeded != 0 {
		t.Fatalf("unsupported layer seeded %d connections", seeded)
	}
}

func TestSessionManagerRefreshesInheritedRawKeyShadowAtBind(t *testing.T) {
	m := NewSessionManager(nil)
	authKeyID := [8]byte{2, 2, 8}
	unknown := &Conn{authKeyID: authKeyID, sessionID: 1}
	inherited := &Conn{authKeyID: authKeyID, sessionID: 2}
	explicit := &Conn{authKeyID: authKeyID, sessionID: 3}
	if err := inherited.SeedInheritedLayerProfile(tlprofile.Profile225); err != nil {
		t.Fatal(err)
	}
	if err := explicit.FreezeLayerProfile(tlprofile.Profile225); err != nil {
		t.Fatal(err)
	}
	for _, c := range []*Conn{unknown, inherited, explicit} {
		if err := m.Register(c); err != nil {
			t.Fatal(err)
		}
	}

	if refreshed := m.RefreshInheritedLayerForRawAuthKey(authKeyID, 227); refreshed != 2 {
		t.Fatalf("refreshed connections = %d, want 2", refreshed)
	}
	for name, c := range map[string]*Conn{"unknown": unknown, "inherited": inherited} {
		if got := c.LayerProfileState(); got.Profile != tlprofile.Profile227 || got.Origin != LayerProfileInherited {
			t.Fatalf("%s refresh = %#v", name, got)
		}
	}
	if got := explicit.LayerProfileState(); got.Profile != tlprofile.Profile225 || got.Origin != LayerProfileExplicit {
		t.Fatalf("explicit evidence overwritten = %#v", got)
	}
}

func TestSessionManagerClearsOnlyInheritedRawKeyShadowAtBind(t *testing.T) {
	m := NewSessionManager(nil)
	authKeyID := [8]byte{2, 2, 81}
	inherited := &Conn{authKeyID: authKeyID, sessionID: 1}
	explicit := &Conn{authKeyID: authKeyID, sessionID: 2}
	unknown := &Conn{authKeyID: authKeyID, sessionID: 3}
	if err := inherited.SeedInheritedLayerProfile(tlprofile.Profile225); err != nil {
		t.Fatal(err)
	}
	if err := explicit.seedOrderedLayerProfile(tlprofile.Profile227, 104); err != nil {
		t.Fatal(err)
	}
	for _, c := range []*Conn{inherited, explicit, unknown} {
		if err := m.Register(c); err != nil {
			t.Fatal(err)
		}
	}

	if cleared := m.ClearInheritedLayerForRawAuthKey(authKeyID); cleared != 1 {
		t.Fatalf("cleared connections = %d, want 1", cleared)
	}
	if got := inherited.LayerProfileState(); got.Origin != LayerProfileUnknown || got.Profile != 0 {
		t.Fatalf("inherited shadow after clear = %#v, want unknown", got)
	}
	if got := explicit.LayerProfileState(); got.Profile != tlprofile.Profile227 || got.Origin != LayerProfileExplicit {
		t.Fatalf("explicit evidence was cleared = %#v", got)
	}
	if state, msgID := explicit.layerProfileEvidenceState(); state.Origin != LayerProfileExplicit || msgID != 104 {
		t.Fatalf("explicit ordered evidence changed = %#v msgID:%d", state, msgID)
	}
	if got := unknown.LayerProfileState(); got.Origin != LayerProfileUnknown || got.Profile != 0 {
		t.Fatalf("unknown state changed = %#v", got)
	}
	if cleared := m.ClearInheritedLayerForRawAuthKey(authKeyID); cleared != 0 {
		t.Fatalf("idempotent clear changed %d connections", cleared)
	}
}

func TestSessionManagerSeedsUnknownSessionsAcrossBusinessAuthKey(t *testing.T) {
	m := NewSessionManager(nil)
	permAuthKeyID := [8]byte{9, 9, 9}
	rawOne := [8]byte{9, 9, 1}
	rawTwo := [8]byte{9, 9, 2}
	first := &Conn{authKeyID: rawOne, sessionID: 1}
	second := &Conn{authKeyID: rawTwo, sessionID: 2}
	explicit := &Conn{authKeyID: rawTwo, sessionID: 3}
	inherited := &Conn{authKeyID: rawOne, sessionID: 4}
	if err := explicit.FreezeLayerProfile(tlprofile.Profile225); err != nil {
		t.Fatal(err)
	}
	if err := inherited.SeedInheritedLayerProfile(tlprofile.Profile225); err != nil {
		t.Fatal(err)
	}
	for _, c := range []*Conn{first, second, explicit, inherited} {
		if err := m.Register(c); err != nil {
			t.Fatal(err)
		}
		m.BindAuthKeyForSession(c.authKeyID, c.sessionID, permAuthKeyID)
	}
	if seeded := m.SeedInheritedLayerForBusinessAuthKey(permAuthKeyID, 227); seeded != 2 {
		t.Fatalf("business auth-key seeded=%d, want 2", seeded)
	}
	for name, c := range map[string]*Conn{"first": first, "second": second} {
		if got := c.LayerProfileState(); got.Profile != tlprofile.Profile227 || got.Origin != LayerProfileInherited {
			t.Fatalf("%s business default = %#v", name, got)
		}
	}
	if got := explicit.LayerProfileState(); got.Profile != tlprofile.Profile225 || got.Origin != LayerProfileExplicit {
		t.Fatalf("business seed overwrote explicit = %#v", got)
	}
	if got := inherited.LayerProfileState(); got.Profile != tlprofile.Profile225 || got.Origin != LayerProfileInherited {
		t.Fatalf("business seed overwrote inherited = %#v", got)
	}
}

func TestSessionManagerExplicitLayerEvidenceUsesLiveExactSession(t *testing.T) {
	m := NewSessionManager(nil)
	authKeyID := [8]byte{2, 2, 9}
	const sessionID = int64(229)
	c := &Conn{authKeyID: authKeyID, sessionID: sessionID}
	if err := c.seedOrderedLayerProfile(tlprofile.Profile226, 1234); err != nil {
		t.Fatal(err)
	}
	if err := m.Register(c); err != nil {
		t.Fatal(err)
	}
	if layer, msgID, ok := m.ExplicitLayerEvidenceForAuthKey(authKeyID, sessionID); !ok || layer != 226 || msgID != 1234 {
		t.Fatalf("live explicit evidence = (%d,%d,%v)", layer, msgID, ok)
	}

	inherited := &Conn{authKeyID: authKeyID, sessionID: sessionID + 1}
	if err := inherited.SeedInheritedLayerProfile(tlprofile.Profile227); err != nil {
		t.Fatal(err)
	}
	if err := m.Register(inherited); err != nil {
		t.Fatal(err)
	}
	if layer, msgID, ok := m.ExplicitLayerEvidenceForAuthKey(authKeyID, sessionID+1); ok || layer != 0 || msgID != 0 {
		t.Fatalf("inherited state exposed as explicit = (%d,%d,%v)", layer, msgID, ok)
	}
}

func TestSessionManagerExplicitLayerEvidenceChoosesNewestActiveOrClaim(t *testing.T) {
	m := NewSessionManager(nil)
	authKeyID := [8]byte{2, 3, 0}
	const sessionID = int64(230)
	active := &Conn{authKeyID: authKeyID, sessionID: sessionID}
	claim := &Conn{authKeyID: authKeyID, sessionID: sessionID}
	if err := active.seedOrderedLayerProfile(tlprofile.Profile225, 100); err != nil {
		t.Fatal(err)
	}
	if err := claim.seedOrderedLayerProfile(tlprofile.Profile227, 104); err != nil {
		t.Fatal(err)
	}
	key := sessionKey{authKeyID: authKeyID, sessionID: sessionID}
	m.mu.Lock()
	m.bySession[key] = active
	m.claims[key] = claim
	m.mu.Unlock()
	if layer, msgID, ok := m.ExplicitLayerEvidenceForAuthKey(authKeyID, sessionID); !ok || layer != 227 || msgID != 104 {
		t.Fatalf("newest active/claim evidence = (%d,%d,%v)", layer, msgID, ok)
	}

	claim.retire()
	if layer, msgID, ok := m.ExplicitLayerEvidenceForAuthKey(authKeyID, sessionID); !ok || layer != 225 || msgID != 100 {
		t.Fatalf("retired claim shadowed active evidence = (%d,%d,%v)", layer, msgID, ok)
	}
}

func TestOrderedSessionLayerBroadcastConvergesAcrossPhysicalGenerations(t *testing.T) {
	m := NewSessionManager(nil)
	authKeyID := [8]byte{3, 0, 0}
	const sessionID = int64(300)
	oldPhysical := &Conn{authKeyID: authKeyID, sessionID: sessionID}
	current := &Conn{authKeyID: authKeyID, sessionID: sessionID}
	m.mu.Lock()
	m.bySession[sessionKey{authKeyID: authKeyID, sessionID: sessionID}] = current
	m.mu.Unlock()

	if applied, err := m.ApplyOrderedLayerProfileForSession(oldPhysical, authKeyID, sessionID, tlprofile.Profile227, 300); err != nil || applied != 2 {
		t.Fatalf("newer broadcast applied=%d err=%v", applied, err)
	}
	if applied, err := m.ApplyOrderedLayerProfileForSession(oldPhysical, authKeyID, sessionID, tlprofile.Profile225, 200); err != nil || applied != 0 {
		t.Fatalf("delayed older broadcast applied=%d err=%v", applied, err)
	}
	for name, c := range map[string]*Conn{"old": oldPhysical, "current": current} {
		state, msgID := c.layerProfileEvidenceState()
		if state.Profile != tlprofile.Profile227 || state.Origin != LayerProfileExplicit || msgID != 300 {
			t.Fatalf("%s physical state = %#v msgID:%d", name, state, msgID)
		}
	}
}

func TestInitialProfileSeedAvoidsPermanentKeyResolverAndPrefersPermForTemp(t *testing.T) {
	t.Run("fetched permanent metadata is authoritative fast path", func(t *testing.T) {
		resolver := &countingInheritedLayerResolver{layer: 225, found: true}
		s := &Server{layerRPC: resolver}
		c := &Conn{authKeyExpiresAt: 0}
		if err := s.seedInitialLayerProfile(context.Background(), c, 227, LayerProfileSnapshot{}); err != nil {
			t.Fatal(err)
		}
		if resolver.calls != 0 {
			t.Fatalf("permanent resolver calls = %d, want 0", resolver.calls)
		}
		if got := c.LayerProfileState(); got.Profile != tlprofile.Profile227 || got.Origin != LayerProfileInherited {
			t.Fatalf("permanent seed = %#v", got)
		}
	})

	t.Run("temporary key resolves permanent before raw shadow", func(t *testing.T) {
		resolver := &countingInheritedLayerResolver{layer: 227, found: true}
		s := &Server{layerRPC: resolver}
		c := &Conn{authKeyExpiresAt: 1_900_000_000}
		if err := s.seedInitialLayerProfile(context.Background(), c, 225, LayerProfileSnapshot{}); err != nil {
			t.Fatal(err)
		}
		if resolver.calls != 1 {
			t.Fatalf("temporary resolver calls = %d, want 1", resolver.calls)
		}
		if got := c.LayerProfileState(); got.Profile != tlprofile.Profile227 || got.Origin != LayerProfileInherited {
			t.Fatalf("temporary canonical seed = %#v", got)
		}
	})

	t.Run("unsupported permanent metadata stays unknown", func(t *testing.T) {
		resolver := &countingInheritedLayerResolver{layer: 227, found: true}
		s := &Server{layerRPC: resolver}
		c := &Conn{authKeyExpiresAt: 0}
		if err := s.seedInitialLayerProfile(context.Background(), c, 229, LayerProfileSnapshot{}); err != nil {
			t.Fatal(err)
		}
		if resolver.calls != 0 {
			t.Fatalf("unsupported permanent metadata fell through to resolver")
		}
		if got := c.LayerProfileState(); got.Origin != LayerProfileUnknown {
			t.Fatalf("unsupported permanent seed = %#v", got)
		}
	})

	t.Run("unsupported bound permanent blocks raw temp shadow", func(t *testing.T) {
		resolver := &countingInheritedLayerResolver{layer: 229, found: true}
		s := &Server{layerRPC: resolver}
		c := &Conn{authKeyExpiresAt: 1_900_000_000}
		if err := s.seedInitialLayerProfile(context.Background(), c, 225, LayerProfileSnapshot{}); err != nil {
			t.Fatal(err)
		}
		if got := c.LayerProfileState(); got.Origin != LayerProfileUnknown {
			t.Fatalf("authoritative unsupported perm fell back to raw shadow = %#v", got)
		}
	})
}

func TestInitialProfileSeedRestoresOrderedExactSessionEvidence(t *testing.T) {
	resolver := &orderedSessionLayerResolver{layer: 226, msgID: 123456, found: true}
	s := &Server{layerRPC: resolver}
	c := &Conn{authKeyExpiresAt: 0}
	if err := s.seedInitialLayerProfile(context.Background(), c, 227, LayerProfileSnapshot{}); err != nil {
		t.Fatal(err)
	}
	state, msgID := c.layerProfileEvidenceState()
	if state.Profile != tlprofile.Profile226 || state.Origin != LayerProfileExplicit || msgID != resolver.msgID {
		t.Fatalf("ordered exact seed = state:%#v msgID:%d", state, msgID)
	}
}

func TestInheritedLayerResolverFailureLeavesExplicitRecoveryAvailable(t *testing.T) {
	resolverErr := errors.New("temporary resolver unavailable")
	resolver := &countingInheritedLayerResolver{err: resolverErr}
	s := &Server{layerRPC: resolver}
	c := &Conn{authKeyExpiresAt: 1_900_000_000}
	if err := s.seedInitialLayerProfile(context.Background(), c, 225, LayerProfileSnapshot{}); err != nil {
		t.Fatalf("optional inherited resolver error escaped: %v", err)
	}
	if got := c.LayerProfileState(); got.Origin != LayerProfileUnknown {
		t.Fatalf("resolver error selected stale fallback = %#v", got)
	}
}

func TestInheritedLayerResolverAvailabilityUsesOnlySupportedRawTempShadow(t *testing.T) {
	for _, tt := range []struct {
		name         string
		fetchedLayer int
		wantProfile  tlprofile.Profile
		wantOrigin   LayerProfileOrigin
	}{
		{name: "supported raw shadow", fetchedLayer: 225, wantProfile: tlprofile.Profile225, wantOrigin: LayerProfileInherited},
		{name: "future raw shadow stays unknown", fetchedLayer: 229, wantOrigin: LayerProfileUnknown},
	} {
		t.Run(tt.name, func(t *testing.T) {
			resolver := &countingInheritedLayerResolver{err: layerDurabilityUnavailableTestError{}}
			s := &Server{layerRPC: resolver}
			c := &Conn{authKeyExpiresAt: 1_900_000_000}
			if err := s.seedInitialLayerProfile(context.Background(), c, tt.fetchedLayer, LayerProfileSnapshot{}); err != nil {
				t.Fatal(err)
			}
			if resolver.calls != 1 {
				t.Fatalf("resolver calls=%d, want 1", resolver.calls)
			}
			if got := c.LayerProfileState(); got.Profile != tt.wantProfile || got.Origin != tt.wantOrigin {
				t.Fatalf("availability seed = %#v, want profile=%d origin=%d", got, tt.wantProfile, tt.wantOrigin)
			}
		})
	}
}

func TestActivationClaimRecheckClosesTempBindLayerRace(t *testing.T) {
	authKeyID := [8]byte{4, 0, 0}

	t.Run("bind wins before claimed recheck", func(t *testing.T) {
		resolver := &countingInheritedLayerResolver{found: false}
		s := &Server{layerRPC: resolver}
		m := NewSessionManager(nil)
		c := &Conn{authKeyID: authKeyID, sessionID: 401, authKeyExpiresAt: 1_900_000_000}
		if err := s.seedInitialLayerProfile(context.Background(), c, 225, LayerProfileSnapshot{}); err != nil {
			t.Fatal(err)
		}
		if err := m.BeginActivation(c); err != nil {
			t.Fatal(err)
		}
		defer m.AbortActivation(c)
		resolver.layer, resolver.found = 227, true
		if err := s.refreshActivatedInheritedLayerProfile(context.Background(), c, 225); err != nil {
			t.Fatal(err)
		}
		if got := c.LayerProfileState(); got.Profile != tlprofile.Profile227 || got.Origin != LayerProfileInherited {
			t.Fatalf("post-claim permanent recheck = %#v", got)
		}
	})

	t.Run("claim wins before bind refresh", func(t *testing.T) {
		m := NewSessionManager(nil)
		c := &Conn{authKeyID: authKeyID, sessionID: 402, authKeyExpiresAt: 1_900_000_000}
		if err := c.SeedInheritedLayerProfile(tlprofile.Profile225); err != nil {
			t.Fatal(err)
		}
		if err := m.BeginActivation(c); err != nil {
			t.Fatal(err)
		}
		defer m.AbortActivation(c)
		if refreshed := m.RefreshInheritedLayerForRawAuthKey(authKeyID, 227); refreshed != 1 {
			t.Fatalf("bind refresh count = %d, want 1", refreshed)
		}
		if got := c.LayerProfileState(); got.Profile != tlprofile.Profile227 || got.Origin != LayerProfileInherited {
			t.Fatalf("claim-visible bind refresh = %#v", got)
		}
	})

	t.Run("unsupported permanent clears preclaim raw shadow", func(t *testing.T) {
		resolver := &countingInheritedLayerResolver{layer: 229, found: true}
		s := &Server{layerRPC: resolver}
		c := &Conn{authKeyID: authKeyID, sessionID: 403, authKeyExpiresAt: 1_900_000_000}
		if err := c.SeedInheritedLayerProfile(tlprofile.Profile225); err != nil {
			t.Fatal(err)
		}
		if err := s.refreshActivatedInheritedLayerProfile(context.Background(), c, 225); err != nil {
			t.Fatal(err)
		}
		if got := c.LayerProfileState(); got.Origin != LayerProfileUnknown {
			t.Fatalf("unsupported permanent kept stale raw shadow = %#v", got)
		}
	})

	t.Run("availability outage retains current raw shadow", func(t *testing.T) {
		resolver := &countingInheritedLayerResolver{err: layerDurabilityUnavailableTestError{}}
		s := &Server{layerRPC: resolver}
		c := &Conn{authKeyID: authKeyID, sessionID: 404, authKeyExpiresAt: 1_900_000_000}
		if err := s.refreshActivatedInheritedLayerProfile(context.Background(), c, 225); err != nil {
			t.Fatal(err)
		}
		if got := c.LayerProfileState(); got.Profile != tlprofile.Profile225 || got.Origin != LayerProfileInherited {
			t.Fatalf("availability recheck lost raw shadow = %#v", got)
		}
	})

	t.Run("structural resolver failure clears preclaim raw shadow", func(t *testing.T) {
		resolver := &countingInheritedLayerResolver{err: errors.New("invalid binding identity")}
		s := &Server{layerRPC: resolver}
		c := &Conn{authKeyID: authKeyID, sessionID: 405, authKeyExpiresAt: 1_900_000_000}
		if err := c.SeedInheritedLayerProfile(tlprofile.Profile225); err != nil {
			t.Fatal(err)
		}
		if err := s.refreshActivatedInheritedLayerProfile(context.Background(), c, 225); err != nil {
			t.Fatal(err)
		}
		if got := c.LayerProfileState(); got.Origin != LayerProfileUnknown {
			t.Fatalf("structural resolver failure retained raw shadow = %#v", got)
		}
	})
}
