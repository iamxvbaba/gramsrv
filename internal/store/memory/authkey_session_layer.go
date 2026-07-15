package memory

import (
	"context"
	"encoding/binary"
	"math"
	"time"

	"telesrv/internal/store"
)

type authKeySessionLayerKey struct {
	rawAuthKeyID [8]byte
	sessionID    int64
}

func (s *AuthKeyStore) GetSessionLayer(
	_ context.Context,
	rawAuthKeyID [8]byte,
	sessionID int64,
) (store.AuthKeySessionLayer, bool, error) {
	now := time.Now()
	key := authKeySessionLayerKey{rawAuthKeyID: rawAuthKeyID, sessionID: sessionID}
	s.state.mu.Lock()
	value, found := s.state.sessionLayers[key]
	if found && !now.Before(value.ExpiresAt) {
		delete(s.state.sessionLayers, key)
		found = false
		value = store.AuthKeySessionLayer{}
	} else if found {
		value.SharedDefault = s.state.sessionLayerIsSharedDefaultLocked(rawAuthKeyID, value)
	}
	s.state.mu.Unlock()
	return value, found, nil
}

func (s *AuthKeyStore) AdvanceSessionLayer(
	_ context.Context,
	rawAuthKeyID [8]byte,
	sessionID int64,
	layer int,
	msgID int64,
) (store.AuthKeySessionLayer, bool, error) {
	now := time.Now()
	expiresAt, freshEvidence := store.AuthKeySessionLayerEvidenceFresh(now, msgID)
	if layer <= 0 || !freshEvidence {
		return store.AuthKeySessionLayer{}, false, store.ErrAuthKeySessionLayerInvalid
	}
	key := authKeySessionLayerKey{rawAuthKeyID: rawAuthKeyID, sessionID: sessionID}
	s.state.mu.Lock()
	defer s.state.mu.Unlock()
	if _, found := s.state.keys[rawAuthKeyID]; !found {
		return store.AuthKeySessionLayer{}, false, store.ErrAuthKeyNotFound
	}
	current, found := s.state.sessionLayers[key]
	if found && now.Before(current.ExpiresAt) {
		switch {
		case msgID < current.MessageID:
			current.SharedDefault = s.state.sessionLayerIsSharedDefaultLocked(rawAuthKeyID, current)
			return current, false, nil
		case msgID == current.MessageID:
			if layer != current.Layer {
				return current, false, store.ErrAuthKeySessionLayerConflict
			}
			current.SharedDefault = s.state.sessionLayerIsSharedDefaultLocked(rawAuthKeyID, current)
			return current, false, nil
		}
	}
	var (
		permID [8]byte
		perm   store.AuthKeyData
		bound  bool
	)
	if binding, ok := s.state.bindings[rawAuthKeyID]; ok {
		bound = true
		binary.LittleEndian.PutUint64(permID[:], uint64(binding.PermAuthKeyID))
		var permFound bool
		perm, permFound = s.state.keys[permID]
		if !permFound {
			return store.AuthKeySessionLayer{}, false, store.ErrAuthKeyBindingInvalid
		}
	}
	if s.state.nextLayerObservation == math.MaxInt64 {
		return store.AuthKeySessionLayer{}, false, store.ErrAuthKeySessionLayerInvalid
	}
	s.state.nextLayerObservation++
	current = store.AuthKeySessionLayer{
		Layer:         layer,
		MessageID:     msgID,
		ObservationID: s.state.nextLayerObservation,
		ExpiresAt:     expiresAt,
	}
	stored := current
	stored.SharedDefault = false
	s.state.sessionLayers[key] = stored
	raw := s.state.keys[rawAuthKeyID]
	raw.Layer = layer
	raw.LayerObservationID = current.ObservationID
	s.state.keys[rawAuthKeyID] = raw
	if bound {
		perm.Layer = layer
		perm.LayerObservationID = current.ObservationID
		s.state.keys[permID] = perm
		s.state.mirrorAuthorizationLayersLocked([][8]byte{rawAuthKeyID, permID}, layer)
	} else {
		s.state.mirrorAuthorizationLayersLocked([][8]byte{rawAuthKeyID}, layer)
	}
	current.SharedDefault = true
	return current, true, nil
}

func (s *AuthKeyStore) DeleteSessionLayer(
	_ context.Context,
	rawAuthKeyID [8]byte,
	sessionID int64,
) (bool, error) {
	key := authKeySessionLayerKey{rawAuthKeyID: rawAuthKeyID, sessionID: sessionID}
	s.state.mu.Lock()
	_, deleted := s.state.sessionLayers[key]
	delete(s.state.sessionLayers, key)
	s.state.mu.Unlock()
	return deleted, nil
}

func (s *AuthKeyStore) DeleteExpiredSessionLayers(_ context.Context, limit int) (int, error) {
	if limit <= 0 {
		return 0, nil
	}
	now := time.Now()
	s.state.mu.Lock()
	deleted := 0
	for key, value := range s.state.sessionLayers {
		if deleted >= limit {
			break
		}
		if now.Before(value.ExpiresAt) {
			continue
		}
		delete(s.state.sessionLayers, key)
		deleted++
	}
	s.state.mu.Unlock()
	return deleted, nil
}

func (s *authKeyState) deleteSessionLayersLocked(rawAuthKeyID [8]byte) {
	for key := range s.sessionLayers {
		if key.rawAuthKeyID == rawAuthKeyID {
			delete(s.sessionLayers, key)
		}
	}
}

func (s *authKeyState) sessionLayerIsSharedDefaultLocked(rawAuthKeyID [8]byte, value store.AuthKeySessionLayer) bool {
	defaultKeyID := rawAuthKeyID
	if binding, ok := s.bindings[rawAuthKeyID]; ok {
		binary.LittleEndian.PutUint64(defaultKeyID[:], uint64(binding.PermAuthKeyID))
	}
	key, found := s.keys[defaultKeyID]
	return found &&
		key.Layer == value.Layer &&
		key.LayerObservationID == value.ObservationID
}
