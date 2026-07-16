package rpc

import (
	"context"
	"encoding/binary"
	"fmt"
	"sort"
	"time"

	"go.uber.org/zap"

	"github.com/iamxvbaba/td/tlprofile"
	"telesrv/internal/domain"
	"telesrv/internal/store"
)

const (
	inheritedAuthKeyLayerSingleflightPrefix        = "inherited-layer:"
	durableInheritedAuthKeyLayerSingleflightPrefix = "durable-inherited-layer:"
)

const authLayerCommitStripes = 4096

const (
	authLayerPublicationTimeout = 5 * time.Second
	maxAuthLayerEvidenceEntries = maxAuthInfoEntries
)

type inheritedAuthKeyLayerResult struct {
	layer int
	found bool
}

// PublishAdmittedLayerProfileEvidence commits protocol evidence before an
// admitted request can be delayed by the business scheduler or replaced by an
// initConnection rewrap alias. admissionSeq is allocated by mtprotoedge when
// the fresh request obtains flight ownership; it orders process-local/no-store
// publication and supplies the active-admission safe floor. In durable mode the
// store's ObservationID is the cross-session/cross-process publication order.
// Replays reuse the owner's admission sequence and do not publish again.
func (r *Router) PublishAdmittedLayerProfileEvidence(
	ctx context.Context,
	rawAuthKeyID [8]byte,
	sessionID int64,
	msgID int64,
	admissionSeq uint64,
	safeFloor uint64,
	layer int,
) error {
	if r == nil || rawAuthKeyID == ([8]byte{}) || sessionID == 0 {
		return fmt.Errorf("invalid admitted layer evidence identity")
	}
	if admissionSeq == 0 {
		return fmt.Errorf("invalid admitted layer evidence sequence")
	}
	if safeFloor == 0 || safeFloor > admissionSeq {
		return fmt.Errorf("invalid admitted layer evidence safe floor %d for sequence %d", safeFloor, admissionSeq)
	}
	if !isSupportedLayer(layer) {
		return fmt.Errorf("unsupported admitted layer evidence %d", layer)
	}
	// The edge tracker owns admission lifecycle and supplies an exact global
	// retirement floor. Advance it even if this particular proof became stale:
	// the floor itself remains valid capacity-reclamation evidence.
	r.clientInfoMu.Lock()
	if safeFloor > r.authLayerSafeEvictionFloor {
		r.authLayerSafeEvictionFloor = safeFloor
	}
	r.clientInfoMu.Unlock()
	if r.deps.AuthKeySessionLayers == nil {
		currentLayer, currentMsgID, ok := r.NegotiatedSessionLayerEvidence(rawAuthKeyID, sessionID)
		if !ok || currentLayer != layer || (msgID > 0 && currentMsgID != msgID) {
			// A newer correction or bounded registry eviction makes this evidence
			// stale, not invalid. The admitted request keeps its immutable codec but
			// no longer owns shared mutable state.
			return nil
		}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	publicationSequence := admissionSeq
	publicationDurable := false
	if r.deps.AuthKeySessionLayers != nil {
		current, found, err := r.deps.AuthKeySessionLayers.GetSessionLayer(ctx, rawAuthKeyID, sessionID)
		if err != nil {
			// The durable transaction has already committed the protocol fact.
			// A failed cache revalidation must not reject/re-execute the RPC; leave
			// this process cache untouched and let the next session read primary.
			if r.log != nil {
				r.log.Warn("revalidate durable auth-key Layer default for local publication failed",
					zap.String("raw_auth_key_id", fmt.Sprintf("%x", rawAuthKeyID[:])),
					zap.Int64("session_id", sessionID),
					zap.Error(err))
			}
			return nil
		}
		if !found || !current.SharedDefault || current.Layer != layer || current.MessageID != msgID {
			// Another session/process has already committed a newer shared
			// default. This request keeps its immutable result codec but cannot
			// overwrite process-local inherited state.
			return nil
		}
		if current.ObservationID <= 0 {
			return store.ErrAuthKeySessionLayerInvalid
		}
		publicationSequence = uint64(current.ObservationID)
		publicationDurable = true
	}

	effectiveAuthKeyID := rawAuthKeyID
	if r.deps.Auth != nil {
		resolveCtx, cancel := context.WithTimeout(ctx, authLayerPublicationTimeout)
		resolved, found, err := r.deps.Auth.ResolveAuthKey(resolveCtx, rawAuthKeyID)
		cancel()
		switch {
		case err != nil:
			// Persistence identity lookup is availability metadata, not proof of
			// whether this request's generated wire profile is valid. Publish the
			// raw-key default now; a later bind/explicit observation can retry the
			// durable permanent-key normalization.
			if r.log != nil {
				r.log.Warn("resolve auth key for admitted layer evidence failed; publishing raw default",
					zap.String("raw_auth_key_id", fmt.Sprintf("%x", rawAuthKeyID[:])),
					zap.Uint64("admission_seq", admissionSeq),
					zap.Error(err))
			}
		case found && resolved == ([8]byte{}):
			return fmt.Errorf("admitted layer evidence resolved an empty permanent auth key")
		case found:
			effectiveAuthKeyID = resolved
		}
	}

	unlockCommit := r.lockAuthLayerCommit(rawAuthKeyID, effectiveAuthKeyID)
	defer unlockCommit()
	// Freeze may have admitted a newer same-session correction while identity
	// resolution was in flight. The old request stays decodable, but can no
	// longer publish shared state.
	if r.deps.AuthKeySessionLayers == nil {
		if currentLayer, currentMsgID, ok := r.NegotiatedSessionLayerEvidence(rawAuthKeyID, sessionID); !ok || currentLayer != layer || (msgID > 0 && currentMsgID != msgID) {
			return nil
		}
	}

	r.clientInfoMu.Lock()
	applied, err := r.claimAuthLayerDefaultEvidenceLocked(
		layer, publicationSequence, publicationDurable, rawAuthKeyID, effectiveAuthKeyID,
	)
	if err == nil && applied {
		if r.clientInfo == nil {
			r.clientInfo = make(map[clientInfoSessionKey]clientSessionInfo)
		}
		key := clientInfoSessionKey{rawAuthKeyID: rawAuthKeyID, sessionID: sessionID}
		info, exists := r.clientInfo[key]
		if !exists {
			evictMapEntryIfFullLocked(r.clientInfo, maxClientInfoEntries)
		}
		info.layer = layer
		if publicationDurable {
			info.layerObservationID = int64(publicationSequence)
		} else {
			info.layerAdmissionSeq = admissionSeq
		}
		r.clientInfo[key] = info
	}
	r.clientInfoMu.Unlock()
	if err != nil || !applied {
		return err
	}
	if binder, ok := r.deps.Sessions.(AuthKeyLayerBinder); ok {
		binder.SeedInheritedLayerForRawAuthKey(rawAuthKeyID, layer)
	}
	if effectiveAuthKeyID != rawAuthKeyID {
		if binder, ok := r.deps.Sessions.(BusinessAuthKeyLayerBinder); ok {
			binder.SeedInheritedLayerForBusinessAuthKey(effectiveAuthKeyID, layer)
		} else if binder, ok := r.deps.Sessions.(AuthKeyLayerBinder); ok {
			// Compatibility fallback for test doubles/older embedders where a
			// permanent key is itself also the raw connection key.
			binder.SeedInheritedLayerForRawAuthKey(effectiveAuthKeyID, layer)
		}
	}

	// Production durable evidence already updated raw/permanent auth_keys and
	// the authorization mirror in one transaction. The legacy path remains only
	// for isolated Router tests that intentionally omit the protocol store.
	if r.deps.AuthKeySessionLayers == nil {
		r.persistAdmittedAuthKeyLayer(ctx, effectiveAuthKeyID, layer, admissionSeq)
		if effectiveAuthKeyID != rawAuthKeyID {
			r.persistAdmittedAuthKeyLayer(ctx, rawAuthKeyID, layer, admissionSeq)
		}
	}
	return nil
}

func (r *Router) claimAuthLayerDefaultEvidenceLocked(
	layer int,
	sequence uint64,
	durable bool,
	authKeyIDs ...[8]byte,
) (bool, error) {
	if r.authLayerEvidence == nil {
		r.authLayerEvidence = make(map[[8]byte]authLayerDefaultEvidence)
	}
	ids := make([][8]byte, 0, len(authKeyIDs))
	for _, authKeyID := range authKeyIDs {
		if authKeyID == ([8]byte{}) {
			continue
		}
		duplicate := false
		for _, existing := range ids {
			if existing == authKeyID {
				duplicate = true
				break
			}
		}
		if !duplicate {
			ids = append(ids, authKeyID)
		}
	}
	for _, authKeyID := range ids {
		info := r.authInfo[authKeyID]
		if durable {
			if info.layerObservationID < 0 {
				return false, fmt.Errorf("invalid cached auth-key layer observation %d", info.layerObservationID)
			}
			if uint64(info.layerObservationID) > sequence {
				return false, nil
			}
			if info.layerObservationID > 0 && uint64(info.layerObservationID) == sequence && info.layer != layer {
				return false, fmt.Errorf("auth-key layer evidence conflict at durable observation %d: current=%d requested=%d", sequence, info.layer, layer)
			}
		} else {
			if info.layerAdmissionSeq > sequence {
				return false, nil
			}
			if info.layerAdmissionSeq == sequence && info.layerAdmissionSeq > 0 && info.layer != layer {
				return false, fmt.Errorf("auth-key layer evidence conflict at admission sequence %d: current=%d requested=%d", sequence, info.layer, layer)
			}
		}
		current, exists := r.authLayerEvidence[authKeyID]
		if !exists {
			continue
		}
		if current.durable && !durable {
			return false, nil
		}
		if durable && !current.durable {
			continue
		}
		if current.sequence > sequence {
			return false, nil
		}
		if current.sequence == sequence && current.layer != layer {
			kind := "admission sequence"
			if durable {
				kind = "durable observation"
			}
			return false, fmt.Errorf("auth-key layer evidence conflict at %s %d: current=%d requested=%d", kind, sequence, current.layer, layer)
		}
	}

	missing := 0
	for _, authKeyID := range ids {
		if _, exists := r.authLayerEvidence[authKeyID]; !exists {
			missing++
		}
	}
	if len(r.authLayerEvidence)+missing > maxAuthLayerEvidenceEntries {
		for len(r.authLayerEvidence)+missing > maxAuthLayerEvidenceEntries {
			var (
				candidate     [8]byte
				candidateSeq  uint64
				haveCandidate bool
			)
			for currentID, current := range r.authLayerEvidence {
				keep := false
				for _, id := range ids {
					if currentID == id {
						keep = true
						break
					}
				}
				if keep {
					continue
				}
				if !current.durable && current.sequence >= r.authLayerSafeEvictionFloor {
					continue
				}
				if !haveCandidate || current.sequence < candidateSeq {
					candidate, candidateSeq, haveCandidate = currentID, current.sequence, true
				}
			}
			if !haveCandidate {
				if r.log != nil {
					r.log.Warn("auth-key layer evidence capacity exhausted; skip shared default",
						zap.Int("entries", len(r.authLayerEvidence)),
						zap.Uint64("evidence_sequence", sequence),
						zap.Bool("durable", durable),
						zap.Int("layer", layer))
				}
				return false, nil
			}
			delete(r.authLayerEvidence, candidate)
		}
	}

	for _, authKeyID := range ids {
		r.authLayerEvidence[authKeyID] = authLayerDefaultEvidence{
			layer: layer, sequence: sequence, durable: durable,
		}
		if durable {
			r.rememberAuthClientLayerObservationLocked(authKeyID, layer, int64(sequence))
		} else {
			r.rememberAuthClientLayerAtLocked(authKeyID, layer, sequence)
		}
	}
	return true, nil
}

func (r *Router) persistAdmittedAuthKeyLayer(ctx context.Context, authKeyID [8]byte, layer int, admissionSeq uint64) {
	if r.deps.Auth == nil || authKeyID == ([8]byte{}) {
		return
	}
	persistCtx, cancel := context.WithTimeout(ctx, authLayerPublicationTimeout)
	err := r.deps.Auth.UpdateAuthKeyClientInfo(persistCtx, authKeyID, domain.AuthKeyClientInfo{Layer: layer})
	cancel()
	if err != nil && r.log != nil {
		r.log.Warn("persist admitted auth-key layer default failed",
			zap.String("auth_key_id", fmt.Sprintf("%x", authKeyID[:])),
			zap.Int("layer", layer),
			zap.Uint64("admission_seq", admissionSeq),
			zap.Error(err))
	}
}

func (r *Router) lockAuthLayerCommit(authKeyIDs ...[8]byte) func() {
	indices := make([]int, 0, len(authKeyIDs))
	for _, authKeyID := range authKeyIDs {
		if authKeyID == ([8]byte{}) {
			continue
		}
		index := authLayerCommitIndex(authKeyID)
		duplicate := false
		for _, existing := range indices {
			if existing == index {
				duplicate = true
				break
			}
		}
		if !duplicate {
			indices = append(indices, index)
		}
	}
	sort.Ints(indices)
	for _, index := range indices {
		r.authLayerCommit[index].Lock()
	}
	return func() {
		for index := len(indices) - 1; index >= 0; index-- {
			r.authLayerCommit[indices[index]].Unlock()
		}
	}
}

func authLayerCommitIndex(authKeyID [8]byte) int {
	return int(binary.LittleEndian.Uint64(authKeyID[:]) % authLayerCommitStripes)
}

// ResolveInheritedAuthKeyLayer resolves the durable last-known default for a
// newly-created connection. The input is always the physical/raw auth key. A
// bound temporary key is normalized to its permanent identity before reading
// Layer, so an old raw shadow can never outrank the current permanent default.
//
// found=false means no canonical default exists and permits the edge to inspect
// a raw-key fallback. found=true with layer=0 means the canonical record carries
// a future Layer not generated into this binary; it is authoritative-but-
// unusable and therefore blocks fallback/clamping until fresh supported explicit
// invokeWithLayer evidence or a newer binary establishes a usable profile.
func (r *Router) ResolveInheritedAuthKeyLayer(ctx context.Context, rawAuthKeyID [8]byte) (int, bool, error) {
	if r == nil || rawAuthKeyID == ([8]byte{}) {
		return 0, false, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	effectiveAuthKeyID := rawAuthKeyID
	if r.deps.Auth != nil {
		resolved, found, err := r.deps.Auth.ResolveAuthKey(ctx, rawAuthKeyID)
		if err != nil {
			return 0, false, wrapLayerEvidenceStoreAvailability(err)
		}
		if found {
			if resolved == ([8]byte{}) {
				return 0, false, fmt.Errorf("resolved empty permanent auth key")
			}
			effectiveAuthKeyID = resolved
		}
	}
	layer, found, err := r.resolveAuthKeyLayerDefault(ctx, effectiveAuthKeyID)
	if err != nil {
		return 0, false, wrapLayerEvidenceStoreAvailability(err)
	}
	if !found {
		return 0, false, nil
	}
	var durableProjection clientSessionInfo
	if r.deps.AuthKeySessionLayers != nil {
		// resolveDurableAuthKeyLayerDefault may have completed a stale DB read
		// after a newer same-process explicit observation was published. The
		// observation-aware cache merge keeps the newer tuple; re-read it before
		// returning or projecting to a bound raw identity.
		r.clientInfoMu.RLock()
		current := r.authInfo[effectiveAuthKeyID]
		r.clientInfoMu.RUnlock()
		if current.layerObservationID > 0 {
			durableProjection = clientSessionInfo{
				layer:                 current.layer,
				layerObservationID:    current.layerObservationID,
				layerBlocked:          current.layerBlocked,
				layerBlockedByAuthKey: current.layerBlockedByAuthKey,
				authKeyInfoChecked:    current.authKeyInfoChecked,
				authorizationChecked:  current.authorizationChecked,
			}
			switch {
			case isSupportedLayer(current.layer):
				layer, found = current.layer, true
			case current.layerBlocked:
				layer, found = 0, true
			}
		}
	}
	if layer == 0 {
		// found=true with no usable profile is an authoritative future/unsupported
		// canonical default. The edge must stay unknown rather than falling back
		// to an older raw temporary-key shadow.
		return 0, true, nil
	}
	if effectiveAuthKeyID != rawAuthKeyID {
		// This updates only the auth-key default/shadow. Existing session entries
		// remain untouched and therefore keep mixed Layer 225/227 profiles.
		r.clientInfoMu.Lock()
		if durableProjection.layerObservationID > 0 {
			r.rememberAuthClientInfoLocked(rawAuthKeyID, durableProjection)
		} else {
			r.rememberAuthClientLayerLocked(rawAuthKeyID, layer)
		}
		r.clientInfoMu.Unlock()
	}
	return layer, true, nil
}

func (r *Router) resolveAuthKeyLayerDefault(ctx context.Context, authKeyID [8]byte) (int, bool, error) {
	if r == nil || authKeyID == ([8]byte{}) {
		return 0, false, nil
	}
	if r.deps.AuthKeySessionLayers != nil {
		return r.resolveDurableAuthKeyLayerDefault(ctx, authKeyID)
	}
	if layer, found, resolved := r.cachedAuthKeyLayerResolution(authKeyID); resolved {
		return layer, found, nil
	}
	if r.deps.Auth == nil {
		return 0, false, nil
	}
	v, err, _ := r.authUserSF.Do(inheritedAuthKeyLayerSingleflightPrefix+string(authKeyID[:]), func() (any, error) {
		if layer, found, resolved := r.cachedAuthKeyLayerResolution(authKeyID); resolved {
			return inheritedAuthKeyLayerResult{layer: layer, found: found}, nil
		}

		keyInfo, keyFound, err := r.deps.Auth.AuthKeyClientInfo(ctx, authKeyID)
		if err != nil {
			return inheritedAuthKeyLayerResult{}, err
		}
		if keyFound {
			r.cacheAuthKeyClientInfo(authKeyID, clientSessionInfoFromAuthKeyClientInfo(keyInfo))
			if current, ok := r.cachedAuthKeyLayerDefault(authKeyID); ok {
				return inheritedAuthKeyLayerResult{layer: current, found: true}, nil
			}
			if keyInfo.Layer != 0 {
				if !isSupportedLayer(keyInfo.Layer) {
					r.cacheAuthKeyClientInfo(authKeyID, clientSessionInfo{layerBlocked: true, layerBlockedByAuthKey: true})
					return inheritedAuthKeyLayerResult{found: true}, nil
				}
				return inheritedAuthKeyLayerResult{layer: keyInfo.Layer, found: true}, nil
			}
		} else {
			r.cacheAuthKeyClientInfo(authKeyID, clientSessionInfo{authKeyInfoChecked: true})
		}
		// auth_keys.layer is the only protocol authority. A zero value means no
		// default exists yet; authorization.layer is a materialized device-list
		// projection and must never be promoted back into wire evidence.
		return inheritedAuthKeyLayerResult{}, nil
	})
	if err != nil {
		return 0, false, err
	}
	if current, ok := r.cachedAuthKeyLayerDefault(authKeyID); ok {
		return current, true, nil
	}
	result := v.(inheritedAuthKeyLayerResult)
	return result.layer, result.found, nil
}

// resolveDurableAuthKeyLayerDefault deliberately re-reads auth_keys for each
// new logical session. A process-local cache has no way to observe a newer
// observation committed by another server process; using it as authority would
// make new sessions inherit an old supported Layer forever (and could hide a
// future authoritative Layer). singleflight only coalesces the concurrent
// startup burst; it does not turn the result into an unbounded authority.
func (r *Router) resolveDurableAuthKeyLayerDefault(ctx context.Context, authKeyID [8]byte) (int, bool, error) {
	if r.deps.Auth == nil {
		return 0, false, nil
	}
	v, err, _ := r.authUserSF.Do(durableInheritedAuthKeyLayerSingleflightPrefix+string(authKeyID[:]), func() (any, error) {
		keyInfo, keyFound, err := r.deps.Auth.AuthKeyClientInfo(ctx, authKeyID)
		if err != nil {
			return inheritedAuthKeyLayerResult{}, err
		}
		if keyFound {
			parsed := clientSessionInfoFromAuthKeyClientInfo(keyInfo)
			r.cacheAuthKeyClientInfo(authKeyID, parsed)
			if keyInfo.Layer != 0 {
				if !isSupportedLayer(keyInfo.Layer) {
					return inheritedAuthKeyLayerResult{found: true}, nil
				}
				return inheritedAuthKeyLayerResult{layer: keyInfo.Layer, found: true}, nil
			}
		}

		// Do not normalize old authorization mirrors on read. Missing primary
		// evidence stays unknown until a selector advances auth_keys explicitly;
		// any legacy repair belongs in an audited migration.
		return inheritedAuthKeyLayerResult{}, nil
	})
	if err != nil {
		return 0, false, err
	}
	result := v.(inheritedAuthKeyLayerResult)
	return result.layer, result.found, nil
}

func (r *Router) cachedAuthKeyLayerDefault(authKeyID [8]byte) (int, bool) {
	r.clientInfoMu.RLock()
	layer := r.authInfo[authKeyID].layer
	r.clientInfoMu.RUnlock()
	return layer, isSupportedLayer(layer)
}

func (r *Router) cachedAuthKeyLayerResolution(authKeyID [8]byte) (layer int, found, resolved bool) {
	r.clientInfoMu.RLock()
	info, ok := r.authInfo[authKeyID]
	r.clientInfoMu.RUnlock()
	if !ok {
		return 0, false, false
	}
	if isSupportedLayer(info.layer) {
		return info.layer, true, true
	}
	if info.layerBlocked {
		return 0, true, true
	}
	if info.authKeyInfoChecked {
		return 0, false, true
	}
	return 0, false, false
}

func (r *Router) cacheAuthKeyClientInfo(authKeyID [8]byte, info clientSessionInfo) {
	r.clientInfoMu.Lock()
	r.rememberAuthClientInfoLocked(authKeyID, info)
	r.clientInfoMu.Unlock()
}

func (r *Router) setAuthKeyLayerDefaults(layer int, authKeyIDs ...[8]byte) {
	if !isSupportedLayer(layer) {
		return
	}
	r.clientInfoMu.Lock()
	for _, authKeyID := range authKeyIDs {
		r.rememberAuthClientLayerLocked(authKeyID, layer)
	}
	r.clientInfoMu.Unlock()
}

func (r *Router) persistAuthKeyLayerDefaults(ctx context.Context, layer int, authKeyIDs ...[8]byte) {
	if r == nil || r.deps.Auth == nil || !isSupportedLayer(layer) {
		return
	}
	seen := make(map[[8]byte]struct{}, len(authKeyIDs))
	for _, authKeyID := range authKeyIDs {
		if authKeyID == ([8]byte{}) {
			continue
		}
		if _, ok := seen[authKeyID]; ok {
			continue
		}
		seen[authKeyID] = struct{}{}
		if err := r.deps.Auth.UpdateAuthKeyClientInfo(ctx, authKeyID, domain.AuthKeyClientInfo{Layer: layer}); err != nil && r.log != nil {
			r.log.Warn("persist inherited auth key layer default failed",
				zap.String("auth_key_id", fmt.Sprintf("%x", authKeyID[:])),
				zap.Int("layer", layer),
				zap.Error(err))
		}
	}
}

func isSupportedLayer(layer int) bool {
	if layer <= 0 {
		return false
	}
	profile, ok := tlprofile.ResolveProfile(layer)
	return ok && int(profile) == layer
}
