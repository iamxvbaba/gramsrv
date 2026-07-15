package memory

import (
	"context"
	"encoding/binary"
	"errors"
	"testing"
	"time"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

func TestAuthKeySessionLayerOrdersRestartEvidenceAndBindingDefaults(t *testing.T) {
	ctx := context.Background()
	keys := NewAuthKeyStore()
	bindings := NewTempAuthKeyBindingStore(keys)
	temp := [8]byte{1}
	perm := [8]byte{2}
	const tempExpiry = 2_000_000_000
	if err := keys.Save(ctx, store.AuthKeyData{ID: temp, ExpiresAt: tempExpiry}); err != nil {
		t.Fatal(err)
	}
	if err := keys.Save(ctx, store.AuthKeyData{ID: perm, ExpiresAt: 0}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	firstMsgID := authKeySessionLayerTestMsgID(now, 1)
	newerMsgID := authKeySessionLayerTestMsgID(now, 2)
	otherMsgID := authKeySessionLayerTestMsgID(now, 3)

	first, applied, err := keys.AdvanceSessionLayer(ctx, temp, 10, 220, firstMsgID)
	if err != nil || !applied || !first.SharedDefault || first.ObservationID <= 0 {
		t.Fatalf("first advance = (%+v,%v,%v)", first, applied, err)
	}
	var permInt [8]byte
	copy(permInt[:], perm[:])
	if err := bindings.Save(ctx, domain.TempAuthKeyBinding{
		TempAuthKeyID: temp,
		PermAuthKeyID: int64(binary.LittleEndian.Uint64(permInt[:])),
		ExpiresAt:     tempExpiry,
	}); err != nil {
		t.Fatal(err)
	}
	for _, id := range [][8]byte{temp, perm} {
		got, found, err := keys.Get(ctx, id)
		if err != nil || !found || got.Layer != 220 || got.LayerObservationID != first.ObservationID {
			t.Fatalf("bound default %x = (%+v,%v,%v)", id, got, found, err)
		}
	}

	newer, applied, err := keys.AdvanceSessionLayer(ctx, temp, 10, 227, newerMsgID)
	if err != nil || !applied || !newer.SharedDefault || newer.ObservationID <= first.ObservationID {
		t.Fatalf("newer advance = (%+v,%v,%v)", newer, applied, err)
	}
	older, applied, err := keys.AdvanceSessionLayer(ctx, temp, 10, 220, firstMsgID)
	if err != nil || applied || older.Layer != 227 || older.MessageID != newerMsgID || !older.SharedDefault {
		t.Fatalf("older replay = (%+v,%v,%v)", older, applied, err)
	}
	if _, _, err := keys.AdvanceSessionLayer(ctx, temp, 10, 220, newerMsgID); !errors.Is(err, store.ErrAuthKeySessionLayerConflict) {
		t.Fatalf("same-msg conflict = %v", err)
	}

	other, applied, err := keys.AdvanceSessionLayer(ctx, temp, 11, 225, otherMsgID)
	if err != nil || !applied || !other.SharedDefault || other.ObservationID <= newer.ObservationID {
		t.Fatalf("other-session advance = (%+v,%v,%v)", other, applied, err)
	}
	oldSession, found, err := keys.GetSessionLayer(ctx, temp, 10)
	if err != nil || !found || oldSession.Layer != 227 || oldSession.SharedDefault {
		t.Fatalf("old exact session after other default = (%+v,%v,%v)", oldSession, found, err)
	}
	for _, id := range [][8]byte{temp, perm} {
		got, _, _ := keys.Get(ctx, id)
		if got.Layer != 225 || got.LayerObservationID != other.ObservationID {
			t.Fatalf("shared default %x = layer %d observation %d", id, got.Layer, got.LayerObservationID)
		}
	}
}

func TestAuthKeySessionLayerExpiryDeleteAndAuthKeyCascade(t *testing.T) {
	ctx := context.Background()
	keys := NewAuthKeyStore()
	id := [8]byte{3}
	if err := keys.Save(ctx, store.AuthKeyData{ID: id}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	seedMsgID := authKeySessionLayerTestMsgID(now, 1)
	replacementMsgID := authKeySessionLayerTestMsgID(now.Add(time.Second), 1)
	otherMsgID := authKeySessionLayerTestMsgID(now.Add(time.Second), 2)
	if _, _, err := keys.AdvanceSessionLayer(
		ctx, id, 20, 227, authKeySessionLayerTestMsgID(now.Add(-302*time.Second), 1),
	); !errors.Is(err, store.ErrAuthKeySessionLayerInvalid) {
		t.Fatalf("stale evidence err = %v", err)
	}
	if _, applied, err := keys.AdvanceSessionLayer(ctx, id, 20, 227, seedMsgID); err != nil || !applied {
		t.Fatalf("seed = applied %v err %v", applied, err)
	}
	// Model the same once-valid row after wall-clock retention elapsed. Tests do
	// not pass an arbitrary expiry through the production write API.
	key := authKeySessionLayerKey{rawAuthKeyID: id, sessionID: 20}
	keys.state.mu.Lock()
	expired := keys.state.sessionLayers[key]
	expired.ExpiresAt = time.Now().Add(-time.Second)
	keys.state.sessionLayers[key] = expired
	keys.state.mu.Unlock()
	if _, found, err := keys.GetSessionLayer(ctx, id, 20); err != nil || found {
		t.Fatalf("expired lookup = found %v err %v", found, err)
	}
	current, applied, err := keys.AdvanceSessionLayer(ctx, id, 20, 220, replacementMsgID)
	if err != nil || !applied || current.Layer != 220 {
		t.Fatalf("expired replacement = (%+v,%v,%v)", current, applied, err)
	}
	if deleted, err := keys.DeleteSessionLayer(ctx, id, 20); err != nil || !deleted {
		t.Fatalf("delete session layer = (%v,%v)", deleted, err)
	}
	if _, applied, err := keys.AdvanceSessionLayer(ctx, id, 21, 227, otherMsgID); err != nil || !applied {
		t.Fatalf("cascade seed = applied %v err %v", applied, err)
	}
	if err := keys.Delete(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, found, err := keys.GetSessionLayer(ctx, id, 21); err != nil || found {
		t.Fatalf("auth key cascade = found %v err %v", found, err)
	}
}

func TestAuthKeySessionLayerObservationContinuesAfterRestoredAuthKeyWatermark(t *testing.T) {
	ctx := context.Background()
	keys := NewAuthKeyStore()
	id := [8]byte{4}
	if err := keys.Save(ctx, store.AuthKeyData{
		ID: id, Layer: 225, LayerObservationID: 91,
	}); err != nil {
		t.Fatal(err)
	}

	current, applied, err := keys.AdvanceSessionLayer(
		ctx, id, 30, 227, authKeySessionLayerTestMsgID(time.Now().UTC(), 1),
	)
	if err != nil || !applied {
		t.Fatalf("advance restored watermark = (%+v,%v,%v)", current, applied, err)
	}
	if current.ObservationID <= 91 {
		t.Fatalf("new observation = %d, want > 91", current.ObservationID)
	}
}

func TestAuthKeyLayerAuthorityKeepsAuthorizationProjectionInParity(t *testing.T) {
	ctx := context.Background()
	t.Run("advance before stale bind", func(t *testing.T) {
		keys := NewAuthKeyStore()
		auths := NewAuthorizationStore()
		auths.LinkAuthKeyAuthority(keys)
		perm := memoryAuthKeyID(8_701)
		if err := keys.Save(ctx, store.AuthKeyData{ID: perm}); err != nil {
			t.Fatal(err)
		}
		advanced, applied, err := keys.AdvanceSessionLayer(
			ctx, perm, 1, 227, authKeySessionLayerTestMsgID(time.Now().UTC(), 1),
		)
		if err != nil || !applied {
			t.Fatalf("advance = (%+v,%v,%v)", advanced, applied, err)
		}
		if err := auths.Bind(ctx, domain.Authorization{
			AuthKeyID: perm, UserID: 1, Layer: 220,
		}); err != nil {
			t.Fatal(err)
		}
		got, found, err := auths.ByAuthKey(ctx, perm)
		if err != nil || !found || got.Layer != 227 {
			t.Fatalf("authorization after stale bind = (%+v,%v,%v)", got, found, err)
		}
	})

	t.Run("bind before advance", func(t *testing.T) {
		keys := NewAuthKeyStore()
		auths := NewAuthorizationStore()
		auths.LinkAuthKeyAuthority(keys)
		perm := memoryAuthKeyID(8_702)
		if err := keys.Save(ctx, store.AuthKeyData{ID: perm, Layer: 220}); err != nil {
			t.Fatal(err)
		}
		if err := auths.Bind(ctx, domain.Authorization{AuthKeyID: perm, UserID: 2, Layer: 225}); err != nil {
			t.Fatal(err)
		}
		if _, _, err := keys.AdvanceSessionLayer(
			ctx, perm, 2, 227, authKeySessionLayerTestMsgID(time.Now().UTC(), 2),
		); err != nil {
			t.Fatal(err)
		}
		got, found, err := auths.ByAuthKey(ctx, perm)
		if err != nil || !found || got.Layer != 227 {
			t.Fatalf("authorization after advance = (%+v,%v,%v)", got, found, err)
		}
	})

	t.Run("temp merge", func(t *testing.T) {
		keys := NewAuthKeyStore()
		auths := NewAuthorizationStore()
		auths.LinkAuthKeyAuthority(keys)
		bindings := NewTempAuthKeyBindingStore(keys)
		temp := memoryAuthKeyID(8_703)
		perm := memoryAuthKeyID(8_704)
		const expiresAt = 2_000_000_000
		if err := keys.Save(ctx, store.AuthKeyData{
			ID: temp, ExpiresAt: expiresAt, Layer: 225, LayerObservationID: 20,
		}); err != nil {
			t.Fatal(err)
		}
		if err := keys.Save(ctx, store.AuthKeyData{
			ID: perm, Layer: 220, LayerObservationID: 10,
		}); err != nil {
			t.Fatal(err)
		}
		if err := auths.Bind(ctx, domain.Authorization{AuthKeyID: perm, UserID: 3, Layer: 227}); err != nil {
			t.Fatal(err)
		}
		if err := bindings.Save(ctx, domain.TempAuthKeyBinding{
			TempAuthKeyID: temp,
			PermAuthKeyID: int64(binary.LittleEndian.Uint64(perm[:])),
			ExpiresAt:     expiresAt,
		}); err != nil {
			t.Fatal(err)
		}
		got, found, err := auths.ByAuthKey(ctx, perm)
		if err != nil || !found || got.Layer != 225 {
			t.Fatalf("authorization after temp merge = (%+v,%v,%v)", got, found, err)
		}
	})
}

func authKeySessionLayerTestMsgID(at time.Time, order uint32) int64 {
	return int64((uint64(at.Unix()) << 32) | uint64(order)<<2)
}
