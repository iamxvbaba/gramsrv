package rpc

import (
	"context"
	"errors"
	"fmt"

	"github.com/iamxvbaba/td/tlprofile"
	"telesrv/internal/store"
)

type layerEvidenceDurabilityUnavailableError struct {
	cause error
}

func (e *layerEvidenceDurabilityUnavailableError) Error() string {
	return fmt.Sprintf("durable Layer evidence unavailable: %v", e.cause)
}

func (e *layerEvidenceDurabilityUnavailableError) Unwrap() error { return e.cause }

// LayerEvidenceDurabilityUnavailable is a structural marker consumed by the
// lower MTProto edge without importing rpc. It permits an explicit selector to
// remain connection-local during a transient store outage; conflicts and
// missing/destroyed auth keys never use this availability fallback.
func (*layerEvidenceDurabilityUnavailableError) LayerEvidenceDurabilityUnavailable() {}

func wrapLayerEvidenceStoreAvailability(err error) error {
	if err == nil ||
		errors.Is(err, store.ErrAuthKeySessionLayerConflict) ||
		errors.Is(err, store.ErrAuthKeySessionLayerInvalid) ||
		errors.Is(err, store.ErrAuthKeyNotFound) ||
		errors.Is(err, store.ErrAuthKeyBindingInvalid) {
		return err
	}
	return &layerEvidenceDurabilityUnavailableError{cause: err}
}

// ResolveNegotiatedSessionLayerEvidence is the restart-safe resolver used by
// the MTProto edge on a physical connection's first exact admission. When a
// durable store is configured it is authoritative on every resolve: another
// Router process may have advanced the same logical session since this
// process cached it. The local registry is only the no-store implementation or
// an availability fallback. A durable future Layer is returned verbatim but
// is not installed into the old binary's typed registry; the edge keeps its raw
// msg_id watermark so a newer supported invokeWithLayer can self-heal.
func (r *Router) ResolveNegotiatedSessionLayerEvidence(
	ctx context.Context,
	rawAuthKeyID [8]byte,
	sessionID int64,
) (layer int, msgID int64, found bool, err error) {
	if r == nil || rawAuthKeyID == ([8]byte{}) || sessionID == 0 {
		return 0, 0, false, nil
	}
	if r.deps.AuthKeySessionLayers == nil {
		layer, msgID, found := r.NegotiatedSessionLayerEvidence(rawAuthKeyID, sessionID)
		return layer, msgID, found, nil
	}
	localLayer, localMsgID, localFound := r.NegotiatedSessionLayerEvidence(rawAuthKeyID, sessionID)
	value, found, err := r.deps.AuthKeySessionLayers.GetSessionLayer(ctx, rawAuthKeyID, sessionID)
	if err != nil {
		if localFound {
			// This exact process observed the selector earlier. It is weaker than
			// primary and is refreshed as soon as the store recovers, but remains a
			// safe same-session availability fallback. Fresh explicit evidence still
			// goes through durable Advance and becomes connection-local on failure.
			return localLayer, localMsgID, true, nil
		}
		return 0, 0, false, wrapLayerEvidenceStoreAvailability(err)
	}
	if !found {
		r.forgetCachedDurableSessionLayer(rawAuthKeyID, sessionID)
		return 0, 0, false, nil
	}
	if value.MessageID <= 0 || value.Layer <= 0 || value.ObservationID <= 0 {
		return 0, 0, false, store.ErrAuthKeySessionLayerInvalid
	}
	if err := r.cacheResolvedDurableSessionLayer(rawAuthKeyID, sessionID, value); err != nil {
		return 0, 0, false, err
	}
	return value.Layer, value.MessageID, true, nil
}

// AdvanceNegotiatedSessionLayerEvidence commits the same-session watermark and
// auth-key-wide default atomically before any connection/profile/readiness
// state is mutated. publishShared is true only when this observation is still
// the durable shared default; an old duplicate cannot overwrite a newer
// session's local cache after restart.
func (r *Router) AdvanceNegotiatedSessionLayerEvidence(
	ctx context.Context,
	rawAuthKeyID [8]byte,
	sessionID int64,
	layer int,
	msgID int64,
) (currentLayer int, currentMsgID int64, publishShared bool, err error) {
	if r == nil || rawAuthKeyID == ([8]byte{}) || sessionID == 0 || msgID <= 0 {
		return 0, 0, false, store.ErrAuthKeySessionLayerInvalid
	}
	if r.deps.AuthKeySessionLayers == nil {
		if _, err := r.FreezeNegotiatedSessionLayerAt(rawAuthKeyID, sessionID, layer, msgID); err != nil {
			return 0, 0, false, err
		}
		currentLayer, currentMsgID, found := r.NegotiatedSessionLayerEvidence(rawAuthKeyID, sessionID)
		if !found {
			return 0, 0, false, fmt.Errorf("exact session Layer disappeared after in-memory advance")
		}
		return currentLayer, currentMsgID, currentLayer == layer && currentMsgID == msgID, nil
	}
	current, _, err := r.deps.AuthKeySessionLayers.AdvanceSessionLayer(
		ctx,
		rawAuthKeyID,
		sessionID,
		layer,
		msgID,
	)
	if err != nil {
		if errors.Is(err, store.ErrAuthKeySessionLayerConflict) {
			return current.Layer, current.MessageID, false, err
		}
		return 0, 0, false, wrapLayerEvidenceStoreAvailability(err)
	}
	if current.ObservationID <= 0 {
		return 0, 0, false, store.ErrAuthKeySessionLayerInvalid
	}
	if err := r.cacheResolvedDurableSessionLayer(rawAuthKeyID, sessionID, current); err != nil {
		return 0, 0, false, err
	}
	return current.Layer, current.MessageID, current.SharedDefault, nil
}

// cacheResolvedDurableSessionLayer updates only the bounded typed accelerator;
// callers always return the store row itself as authority. Ordered replacement
// prevents an older read, which linearized immediately before a concurrent
// Advance, from rolling this process cache back after that Advance completed.
func (r *Router) cacheResolvedDurableSessionLayer(
	rawAuthKeyID [8]byte,
	sessionID int64,
	value store.AuthKeySessionLayer,
) error {
	if r == nil {
		return nil
	}
	key := clientInfoSessionKey{rawAuthKeyID: rawAuthKeyID, sessionID: sessionID}
	profile, supported := tlprofile.ResolveProfile(value.Layer)
	if !supported || int(profile) != value.Layer {
		// A newer binary may have persisted a future profile. Never leave an old
		// typed codec shadow beside that raw authoritative watermark. Observation
		// order still protects a concurrent newer supported Advance from an older
		// in-flight read of this future row.
		r.exactProfileMu.Lock()
		if current, exists := r.exactProfiles[key]; exists && r.clock.Now().Before(current.expiresAt) {
			if current.observationID > value.ObservationID && value.ObservationID > 0 {
				r.exactProfileMu.Unlock()
				return nil
			}
			if current.observationID == value.ObservationID && value.ObservationID > 0 {
				r.exactProfileMu.Unlock()
				return fmt.Errorf("%w: observation %d maps to supported Layer %d and raw Layer %d",
					store.ErrAuthKeySessionLayerConflict, value.ObservationID, current.layer, value.Layer)
			}
		}
		delete(r.exactProfiles, key)
		r.exactProfileMu.Unlock()
		return nil
	}
	now := r.clock.Now()
	expiresAt := value.ExpiresAt
	if expiresAt.IsZero() {
		expiresAt = exactSessionLayerEvidenceExpiry(now, value.MessageID)
	}
	entry := exactSessionProfileEntry{
		layer: value.Layer, msgID: value.MessageID,
		observationID: value.ObservationID, expiresAt: expiresAt,
	}
	r.exactProfileMu.Lock()
	defer r.exactProfileMu.Unlock()
	if r.exactProfiles == nil {
		r.exactProfiles = make(map[clientInfoSessionKey]exactSessionProfileEntry)
	}
	if current, exists := r.exactProfiles[key]; exists && now.Before(current.expiresAt) {
		switch {
		case current.observationID > entry.observationID && entry.observationID > 0:
			// This DB read linearized immediately before a concurrent durable
			// Advance which already refreshed the local cache. Do not roll it back.
			return nil
		case current.observationID == entry.observationID && entry.observationID > 0:
			if current.layer != entry.layer || current.msgID != entry.msgID {
				return fmt.Errorf("%w: observation %d maps to (%d,%d) and (%d,%d)",
					store.ErrAuthKeySessionLayerConflict, entry.observationID,
					current.layer, current.msgID, entry.layer, entry.msgID)
			}
			// The store row may have an authoritative expiry refresh; replace it.
		case entry.observationID <= 0 && current.msgID > entry.msgID:
			// Defensive compatibility for an old custom store without observation
			// ids. Production stores always take the branches above.
			return nil
		}
	}
	if _, exists := r.exactProfiles[key]; !exists && len(r.exactProfiles) >= maxExactSessionProfileEntries {
		r.purgeExpiredExactSessionProfilesLocked(now)
		if len(r.exactProfiles) >= maxExactSessionProfileEntries {
			// Durable primary remains authoritative; skipping this accelerator can
			// never reject the request or lose the raw watermark returned to edge.
			return nil
		}
	}
	r.exactProfiles[key] = entry
	if len(r.exactProfiles) == 1 {
		r.exactProfileEarliestExpiry = entry.expiresAt
	} else {
		r.noteExactSessionProfileExpiryLocked(entry.expiresAt)
	}
	return nil
}

func (r *Router) forgetCachedDurableSessionLayer(rawAuthKeyID [8]byte, sessionID int64) {
	if r == nil {
		return
	}
	r.exactProfileMu.Lock()
	delete(r.exactProfiles, clientInfoSessionKey{rawAuthKeyID: rawAuthKeyID, sessionID: sessionID})
	r.exactProfileMu.Unlock()
}

// DeleteNegotiatedSessionLayerEvidence is the durable destroy_session
// boundary. The edge must complete this call before acknowledging destruction;
// ordinary transport disconnect never invokes it.
func (r *Router) DeleteNegotiatedSessionLayerEvidence(
	ctx context.Context,
	rawAuthKeyID [8]byte,
	sessionID int64,
) (bool, error) {
	if r == nil || rawAuthKeyID == ([8]byte{}) || sessionID == 0 {
		return false, nil
	}
	_, _, inMemory := r.NegotiatedSessionLayerEvidence(rawAuthKeyID, sessionID)
	deleted := false
	if r.deps.AuthKeySessionLayers != nil {
		var err error
		deleted, err = r.deps.AuthKeySessionLayers.DeleteSessionLayer(ctx, rawAuthKeyID, sessionID)
		if err != nil {
			return false, err
		}
	}
	r.ForgetNegotiatedSessionLayer(rawAuthKeyID, sessionID)
	return deleted || inMemory, nil
}
