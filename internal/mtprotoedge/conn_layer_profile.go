package mtprotoedge

import (
	"context"
	"fmt"
	"math"

	"github.com/iamxvbaba/td/tlprofile"
	"go.uber.org/zap"
)

// LayerProfileOrigin records why a Conn currently uses a wire profile. The
// distinction is protocol-significant: inherited auth-key metadata is a useful
// availability default, while explicit invokeWithLayer evidence may correct it.
type LayerProfileOrigin uint8

const (
	LayerProfileUnknown LayerProfileOrigin = iota
	LayerProfileInherited
	LayerProfileExplicit
)

// LayerProfileSnapshot is one atomic observation of a connection's current
// profile. Epoch advances on every effective correction, including promotion
// from inherited to explicit evidence at the same numeric layer.
type LayerProfileSnapshot struct {
	Profile tlprofile.Profile
	Origin  LayerProfileOrigin
	Epoch   uint32
}

const (
	layerProfileValueBits   = 16
	layerProfileOriginBits  = 8
	layerProfileOriginShift = layerProfileValueBits
	layerProfileEpochShift  = 32
	layerProfileValueMask   = uint64(1<<layerProfileValueBits - 1)
	layerProfileOriginMask  = uint64(1<<layerProfileOriginBits - 1)
)

func packLayerProfileState(state LayerProfileSnapshot) uint64 {
	return uint64(state.Epoch)<<layerProfileEpochShift |
		(uint64(state.Origin)&layerProfileOriginMask)<<layerProfileOriginShift |
		(uint64(state.Profile) & layerProfileValueMask)
}

func unpackLayerProfileState(raw uint64) LayerProfileSnapshot {
	if raw == 0 {
		return LayerProfileSnapshot{}
	}
	return LayerProfileSnapshot{
		Profile: tlprofile.Profile(raw & layerProfileValueMask),
		Origin:  LayerProfileOrigin((raw >> layerProfileOriginShift) & layerProfileOriginMask),
		Epoch:   uint32(raw >> layerProfileEpochShift),
	}
}

// LayerProfileState returns profile, provenance and epoch from one atomic load.
func (c *Conn) LayerProfileState() LayerProfileSnapshot {
	if c == nil {
		return LayerProfileSnapshot{}
	}
	return unpackLayerProfileState(c.layerProfileState.Load())
}

func (c *Conn) setLayerProfile(profile tlprofile.Profile, origin LayerProfileOrigin, replace bool) (bool, error) {
	if err := validateLayerProfile(profile); err != nil {
		return false, err
	}
	if origin != LayerProfileInherited && origin != LayerProfileExplicit {
		return false, fmt.Errorf("invalid layer profile origin %d", origin)
	}
	c.layerProfileMu.Lock()
	defer c.layerProfileMu.Unlock()
	for {
		raw := c.layerProfileState.Load()
		current := unpackLayerProfileState(raw)
		if origin == LayerProfileInherited && c.layerProfileEvidenceMsgID > 0 {
			return false, nil
		}
		if current.Origin != LayerProfileUnknown {
			if !replace {
				return false, nil
			}
			if current.Profile == profile && current.Origin == origin {
				// The force-style compatibility APIs carry no ordered msg_id.
				// Clearing the cursor keeps a subsequent production observation
				// eligible instead of comparing it with unrelated test/recovery state.
				c.layerProfileEvidenceMsgID = 0
				c.layerProfileEvidenceLayer = 0
				return false, nil
			}
		}
		if current.Epoch == math.MaxUint32 {
			return false, ErrLayerProfileEpochExhausted
		}
		next := LayerProfileSnapshot{Profile: profile, Origin: origin, Epoch: current.Epoch + 1}
		if c.layerProfileState.CompareAndSwap(raw, packLayerProfileState(next)) {
			c.layerProfileEvidenceMsgID = 0
			c.layerProfileEvidenceLayer = 0
			c.setLegacyClientLayer(int(profile))
			return true, nil
		}
	}
}

func validateLayerProfile(profile tlprofile.Profile) error {
	resolved, ok := tlprofile.ResolveProfile(int(profile))
	if !ok || resolved != profile || uint64(profile) > layerProfileValueMask {
		return fmt.Errorf("%w: %d", ErrLayerProfileUnsupported, profile)
	}
	return nil
}

// layerProfileEvidenceState observes the packed wire state and its ordering
// cursor under one read lock. LayerProfileState remains the allocation-free
// atomic hot-path accessor used by outbound encoding.
func (c *Conn) layerProfileEvidenceState() (LayerProfileSnapshot, int64) {
	state, _, msgID := c.layerProfileRawEvidenceState()
	return state, msgID
}

func (c *Conn) layerProfileRawEvidenceState() (LayerProfileSnapshot, int, int64) {
	if c == nil {
		return LayerProfileSnapshot{}, 0, 0
	}
	c.layerProfileMu.RLock()
	state := unpackLayerProfileState(c.layerProfileState.Load())
	layer := c.layerProfileEvidenceLayer
	msgID := c.layerProfileEvidenceMsgID
	c.layerProfileMu.RUnlock()
	if layer == 0 && state.Origin == LayerProfileExplicit {
		layer = int(state.Profile)
	}
	return state, layer, msgID
}

// freezeLayerProfileAt is the production explicit-evidence transition. The
// positive client msg_id is the protocol ordering authority across TCP
// reconnects and cached request replays.
func (c *Conn) freezeLayerProfileAt(profile tlprofile.Profile, msgID int64) (bool, error) {
	if c == nil {
		return false, fmt.Errorf("nil connection layer profile")
	}
	if msgID <= 0 {
		return false, fmt.Errorf("invalid layer evidence msg_id %d", msgID)
	}
	if err := validateLayerProfile(profile); err != nil {
		return false, err
	}
	return c.freezeRawLayerProfileAt(int(profile), msgID)
}

func (c *Conn) freezeRawLayerProfileAt(layer int, msgID int64) (bool, error) {
	if c == nil {
		return false, fmt.Errorf("nil connection layer profile")
	}
	if layer <= 0 || msgID <= 0 {
		return false, fmt.Errorf("invalid raw layer evidence layer=%d msg_id=%d", layer, msgID)
	}
	profile, supported := tlprofile.ResolveProfile(layer)
	c.layerProfileMu.Lock()
	defer c.layerProfileMu.Unlock()

	current := unpackLayerProfileState(c.layerProfileState.Load())
	if c.layerProfileEvidenceMsgID > 0 {
		switch {
		case msgID < c.layerProfileEvidenceMsgID:
			return false, nil
		case msgID == c.layerProfileEvidenceMsgID:
			if c.layerProfileEvidenceLayer != layer {
				return false, fmt.Errorf("%w: msg_id %d selected both layer %d and %d", ErrLayerProfileConflict, msgID, c.layerProfileEvidenceLayer, layer)
			}
			return false, nil
		}
	}

	desired := LayerProfileSnapshot{Epoch: current.Epoch}
	if supported {
		desired.Profile = profile
		desired.Origin = LayerProfileExplicit
	}
	// A newer proof at the same Layer advances only the ordering cursor. No wire
	// bytes prepared under this profile become stale, so rotating epoch would be
	// unnecessary push churn.
	if current.Profile == desired.Profile && current.Origin == desired.Origin {
		c.layerProfileEvidenceLayer = layer
		c.layerProfileEvidenceMsgID = msgID
		return true, nil
	}
	if current.Epoch == math.MaxUint32 {
		return false, ErrLayerProfileEpochExhausted
	}
	next := desired
	next.Epoch = current.Epoch + 1
	c.layerProfileState.Store(packLayerProfileState(next))
	c.layerProfileEvidenceLayer = layer
	c.layerProfileEvidenceMsgID = msgID
	if supported {
		c.setLegacyClientLayer(layer)
	} else {
		c.setLegacyClientLayer(0)
	}
	return true, nil
}

// seedOrderedLayerProfile restores exact-session evidence atomically before
// any request on a replacement physical connection is admitted.
func (c *Conn) seedOrderedLayerProfile(profile tlprofile.Profile, msgID int64) error {
	if c == nil {
		return nil
	}
	if msgID < 0 {
		return fmt.Errorf("invalid restored layer evidence msg_id %d", msgID)
	}
	if err := validateLayerProfile(profile); err != nil {
		return err
	}
	if msgID > 0 {
		_, err := c.freezeRawLayerProfileAt(int(profile), msgID)
		return err
	}
	c.layerProfileMu.Lock()
	defer c.layerProfileMu.Unlock()
	current := unpackLayerProfileState(c.layerProfileState.Load())
	if current.Profile != profile || current.Origin != LayerProfileExplicit {
		if current.Epoch == math.MaxUint32 {
			return ErrLayerProfileEpochExhausted
		}
		current = LayerProfileSnapshot{Profile: profile, Origin: LayerProfileExplicit, Epoch: current.Epoch + 1}
		c.layerProfileState.Store(packLayerProfileState(current))
		c.setLegacyClientLayer(int(profile))
	}
	c.layerProfileEvidenceMsgID = msgID
	c.layerProfileEvidenceLayer = 0
	return nil
}

func (c *Conn) seedRawLayerEvidence(layer int, msgID int64) error {
	if layer <= 0 || msgID <= 0 {
		return fmt.Errorf("invalid restored raw layer evidence layer=%d msg_id=%d", layer, msgID)
	}
	_, err := c.freezeRawLayerProfileAt(layer, msgID)
	return err
}

// refreshInheritedLayerProfile is reserved for identity normalization at
// auth.bindTempAuthKey: once a raw temporary key is resolved to its permanent
// key, the permanent key's default supersedes an older raw-key shadow. Explicit
// evidence on the concrete session is never overwritten.
func (c *Conn) refreshInheritedLayerProfile(profile tlprofile.Profile) (bool, error) {
	if c == nil {
		return false, nil
	}
	if err := validateLayerProfile(profile); err != nil {
		return false, err
	}
	c.layerProfileMu.Lock()
	defer c.layerProfileMu.Unlock()
	current := unpackLayerProfileState(c.layerProfileState.Load())
	if current.Origin == LayerProfileExplicit || c.layerProfileEvidenceMsgID > 0 {
		return false, nil
	}
	if current.Origin == LayerProfileInherited && current.Profile == profile {
		return false, nil
	}
	if current.Epoch == math.MaxUint32 {
		return false, ErrLayerProfileEpochExhausted
	}
	next := LayerProfileSnapshot{Profile: profile, Origin: LayerProfileInherited, Epoch: current.Epoch + 1}
	c.layerProfileState.Store(packLayerProfileState(next))
	c.layerProfileEvidenceMsgID = 0
	c.layerProfileEvidenceLayer = 0
	c.setLegacyClientLayer(int(profile))
	return true, nil
}

func (c *Conn) clearInheritedLayerProfileState() (bool, error) {
	if c == nil {
		return false, nil
	}
	c.layerProfileMu.Lock()
	defer c.layerProfileMu.Unlock()
	current := unpackLayerProfileState(c.layerProfileState.Load())
	if current.Origin != LayerProfileInherited {
		return false, nil
	}
	if current.Epoch == math.MaxUint32 {
		return false, ErrLayerProfileEpochExhausted
	}
	c.layerProfileState.Store(packLayerProfileState(LayerProfileSnapshot{Epoch: current.Epoch + 1}))
	c.layerProfileEvidenceMsgID = 0
	c.layerProfileEvidenceLayer = 0
	c.setLegacyClientLayer(0)
	return true, nil
}

func (c *Conn) clearInheritedLayerProfile() error {
	_, err := c.clearInheritedLayerProfileState()
	return err
}

// seedInitialLayerProfile applies recovery sources in descending authority.
// Unsupported metadata never clamps to a nearby generated layer: the Conn stays
// unknown so the client can explicitly renegotiate.
func (s *Server) seedInitialLayerProfile(
	ctx context.Context,
	c *Conn,
	fetchedLayer int,
	previous LayerProfileSnapshot,
) error {
	if s == nil || c == nil {
		return nil
	}
	durableResolver, hasDurableResolver := s.layerRPC.(LayerRPCDurableSessionProfileResolver)
	if hasDurableResolver {
		layer, msgID, found, err := durableResolver.ResolveNegotiatedSessionLayerEvidence(ctx, c.authKeyID, c.sessionID)
		if err != nil {
			if isLayerEvidenceDurabilityUnavailable(err) {
				s.log.Warn("Resolve durable exact session Layer during connection seed unavailable; continuing with auth-key default",
					zap.String("auth_key_id", c.authKeyHex), zap.Int64("session_id", c.sessionID), zap.Error(err))
				// Exact-session proof is the strongest recovery source, but its
				// availability failure must not discard a permanent auth_keys.layer
				// already loaded with the key on this same first frame. Continue to the
				// auth-key default below; never reuse previous connection-local evidence
				// in durable mode.
				found = false
			} else {
				return fmt.Errorf("resolve durable exact session Layer during connection seed: %w", err)
			}
		}
		if found {
			if layer <= 0 || msgID < 0 {
				return fmt.Errorf("invalid durable exact session Layer seed layer=%d msg_id=%d", layer, msgID)
			}
			if msgID > 0 {
				return c.seedRawLayerEvidence(layer, msgID)
			}
			// Older in-process exact-session registries did not retain a message
			// watermark. Keep that compatibility-only seed usable without treating
			// it as durable ordered evidence; real durable stores never persist zero.
			profile, supported := tlprofile.ResolveProfile(layer)
			if !supported {
				return nil
			}
			return c.seedOrderedLayerProfile(profile, 0)
		}
	} else if resolver, ok := s.layerRPC.(LayerRPCOrderedSessionProfileResolver); ok {
		if layer, msgID, found := resolver.NegotiatedSessionLayerEvidence(c.authKeyID, c.sessionID); found {
			if layer <= 0 || msgID < 0 {
				return nil
			}
			if msgID > 0 {
				return c.seedRawLayerEvidence(layer, msgID)
			}
			profile, supported := tlprofile.ResolveProfile(layer)
			if !supported {
				return nil
			}
			return c.seedOrderedLayerProfile(profile, 0)
		}
	} else if resolver, ok := s.layerRPC.(LayerRPCSessionProfileResolver); ok {
		if layer, found := resolver.NegotiatedSessionLayer(c.authKeyID, c.sessionID); found {
			profile, supported := tlprofile.ResolveProfile(layer)
			if !supported {
				return nil
			}
			return c.SeedLayerProfile(profile)
		}
	}
	// A freshly fetched permanent-key row is already the canonical auth-key
	// record. Prefer its non-zero Layer without making Router query the same PG
	// row again. Unsupported metadata remains unknown and must not fall through
	// to a weaker mirror.
	if c.authKeyExpiresAt == 0 && fetchedLayer != 0 {
		profile, supported := tlprofile.ResolveProfile(fetchedLayer)
		if !supported {
			return nil
		}
		return c.SeedInheritedLayerProfile(profile)
	}
	// Temporary keys resolve through their bound permanent key before consulting
	// the raw temp-key shadow, which may predate a client upgrade.
	if resolver, ok := s.layerRPC.(LayerRPCInheritedAuthKeyProfileResolver); ok {
		layer, found, err := resolver.ResolveInheritedAuthKeyLayer(ctx, c.authKeyID)
		if err != nil {
			if !isLayerEvidenceDurabilityUnavailable(err) {
				// A binding conflict, missing/destroyed key, or malformed durable
				// value is not an availability hint. Stay unknown and never let a
				// stale raw temp-key shadow outrank that structural failure.
				if s.log != nil {
					s.log.Warn("Resolve inherited auth-key Layer failed; awaiting explicit evidence",
						zap.String("auth_key_id", c.authKeyHex), zap.Error(err))
				}
				return nil
			}
			// The same first frame already authenticated and loaded fetchedLayer
			// from this raw temp key. During a transient permanent-identity lookup
			// outage it is safe only as this physical Conn's inherited shadow; it
			// is never published back to the shared permanent default.
			if s.log != nil {
				s.log.Warn("Resolve inherited auth-key Layer unavailable; continuing with raw auth-key shadow",
					zap.String("auth_key_id", c.authKeyHex), zap.Error(err))
			}
		} else if !found {
			// Fall through to a raw auth-key shadow when the resolver has no
			// canonical permanent-key default (for example an unbound temp key).
		} else {
			profile, supported := tlprofile.ResolveProfile(layer)
			if !supported {
				return nil
			}
			return c.SeedInheritedLayerProfile(profile)
		}
	}
	if fetchedLayer != 0 {
		profile, supported := tlprofile.ResolveProfile(fetchedLayer)
		if !supported {
			return nil
		}
		return c.SeedInheritedLayerProfile(profile)
	}
	if hasDurableResolver {
		// previous belongs to the old logical Conn which occupied this physical
		// transport. In durable mode only an exact session row or auth-key default
		// may cross that boundary. In particular, a connection-local selector used
		// during a store outage must not leak into a newly selected session.
		return nil
	}
	if previous.Origin != LayerProfileUnknown {
		return c.SeedInheritedLayerProfile(previous.Profile)
	}
	return nil
}

// refreshActivatedInheritedLayerProfile closes the auth.bindTempAuthKey race
// after BeginActivation has made this Conn visible in claimsByAuth. If bind won
// first, the second resolver read sees the permanent-key default; if the claim
// won first, bind's SessionManager refresh sees and updates this Conn. Explicit
// session evidence always wins either ordering.
func (s *Server) refreshActivatedInheritedLayerProfile(ctx context.Context, c *Conn, fetchedLayer int) error {
	if s == nil || c == nil || c.LayerProfileState().Origin == LayerProfileExplicit {
		return nil
	}
	if c.authKeyExpiresAt == 0 {
		if fetchedLayer == 0 {
			return nil
		}
		profile, ok := tlprofile.ResolveProfile(fetchedLayer)
		if !ok {
			return c.clearInheritedLayerProfile()
		}
		_, err := c.refreshInheritedLayerProfile(profile)
		return err
	}
	if resolver, ok := s.layerRPC.(LayerRPCInheritedAuthKeyProfileResolver); ok {
		layer, found, err := resolver.ResolveInheritedAuthKeyLayer(ctx, c.authKeyID)
		if err != nil {
			if !isLayerEvidenceDurabilityUnavailable(err) {
				if s.log != nil {
					s.log.Warn("Re-resolve inherited auth-key Layer after activation claim failed",
						zap.String("auth_key_id", c.authKeyHex), zap.Error(err))
				}
				// A pre-claim raw temp shadow may have been installed before a
				// concurrent bind became visible. Structural identity/key failures
				// must revoke that weaker inherited evidence; only an explicit
				// selector (guarded above) is allowed to survive this branch.
				return c.clearInheritedLayerProfile()
			}
			if s.log != nil {
				s.log.Warn("Re-resolve inherited auth-key Layer unavailable; keeping raw auth-key shadow",
					zap.String("auth_key_id", c.authKeyHex), zap.Error(err))
			}
		} else if found {
			profile, supported := tlprofile.ResolveProfile(layer)
			if !supported {
				return c.clearInheritedLayerProfile()
			}
			_, err = c.refreshInheritedLayerProfile(profile)
			return err
		}
	}
	if fetchedLayer == 0 {
		return nil
	}
	profile, ok := tlprofile.ResolveProfile(fetchedLayer)
	if !ok {
		return c.clearInheritedLayerProfile()
	}
	_, err := c.refreshInheritedLayerProfile(profile)
	return err
}

// lockSessionLayerBinding linearizes a proactive update's final validation and
// physical write with profile correction. If correction wins first, validation
// observes the new epoch and drops the update. If the writer wins first, the
// correction becomes visible only after those bytes have landed. Returning a
// bool avoids allocating a release closure on the outbound hot path.
func (c *Conn) lockSessionLayerBinding(binding *outboundLayerBinding) bool {
	if c == nil || binding == nil || binding.wireInvariant ||
		binding.kind == outboundLayerBindingRequest || binding.epoch == 0 {
		return false
	}
	c.layerProfileMu.RLock()
	return true
}
