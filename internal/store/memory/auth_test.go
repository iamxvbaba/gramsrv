package memory

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"testing"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

func TestAuthKeyStorePreservesProtocolExpiry(t *testing.T) {
	ctx := context.Background()
	keys := NewAuthKeyStore()
	want := store.AuthKeyData{
		ID:         [8]byte{1, 2, 3, 4, 5, 6, 7, 8},
		ServerSalt: 42,
		ExpiresAt:  1_799_999_999,
	}
	want.Value[0] = 0xaa
	want.Value[len(want.Value)-1] = 0x55

	if err := keys.Save(ctx, want); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, found, err := keys.Get(ctx, want.ID)
	if err != nil || !found {
		t.Fatalf("get: found=%v err=%v", found, err)
	}
	if got != want {
		t.Fatalf("round trip mismatch: got %+v, want %+v", got, want)
	}

	conflicting := want
	conflicting.ExpiresAt++
	if err := keys.Save(ctx, conflicting); !errors.Is(err, store.ErrAuthKeyProtocolMetadataConflict) {
		t.Fatalf("reclassify auth key error = %v, want %v", err, store.ErrAuthKeyProtocolMetadataConflict)
	}
	got, found, err = keys.Get(ctx, want.ID)
	if err != nil || !found || got != want {
		t.Fatalf("auth key changed after rejected reclassification: got=%+v found=%v err=%v", got, found, err)
	}
}

func TestAuthKeyStoreProtocolRetryPreservesClientLayerMetadata(t *testing.T) {
	ctx := context.Background()
	keys := NewAuthKeyStore()
	id := memoryAuthKeyID(17)
	key := store.AuthKeyData{ID: id, ServerSalt: 10, CreatedAt: 11}
	key.Value[0] = 1
	if err := keys.Save(ctx, key); err != nil {
		t.Fatalf("save auth key: %v", err)
	}
	if err := keys.UpdateClientInfo(ctx, id, store.AuthKeyClientInfo{
		Layer: 227, DeviceModel: "Desktop", Platform: "tdesktop",
		SystemVersion: "Windows", APIID: 2040, AppVersion: "6.2",
	}); err != nil {
		t.Fatalf("update client info: %v", err)
	}

	retry := key
	retry.ServerSalt = 20
	retry.CreatedAt = 0
	if err := keys.Save(ctx, retry); err != nil {
		t.Fatalf("retry protocol save: %v", err)
	}
	got, found, err := keys.Get(ctx, id)
	if err != nil || !found {
		t.Fatalf("get auth key: found=%v err=%v", found, err)
	}
	if got.ServerSalt != 20 || got.CreatedAt != 11 {
		t.Fatalf("protocol fields = salt:%d created:%d, want 20/11", got.ServerSalt, got.CreatedAt)
	}
	if got.Layer != 227 || got.DeviceModel != "Desktop" || got.Platform != "tdesktop" ||
		got.SystemVersion != "Windows" || got.APIID != 2040 || got.AppVersion != "6.2" {
		t.Fatalf("client metadata was erased by protocol retry: %+v", got)
	}
}

func TestAuthKeyStoreUpdateClientInfoRejectsMissingPrimary(t *testing.T) {
	keys := NewAuthKeyStore()
	err := keys.UpdateClientInfo(context.Background(), memoryAuthKeyID(18), store.AuthKeyClientInfo{Layer: 227})
	if !errors.Is(err, store.ErrAuthKeyNotFound) {
		t.Fatalf("missing primary update error = %v, want %v", err, store.ErrAuthKeyNotFound)
	}
}

func TestAuthKeyStoreUpdateClientInfoProtectsObservedLayer(t *testing.T) {
	ctx := context.Background()
	keys := NewAuthKeyStore()
	id := memoryAuthKeyID(19)
	want := store.AuthKeyData{
		ID: id, Layer: 227, LayerObservationID: 91,
		DeviceModel: "before", Platform: "tdesktop",
	}
	if err := keys.Save(ctx, want); err != nil {
		t.Fatalf("save observed auth key: %v", err)
	}

	err := keys.UpdateClientInfo(ctx, id, store.AuthKeyClientInfo{
		Layer: 220, DeviceModel: "must-not-merge", AppVersion: "must-not-merge",
	})
	if !errors.Is(err, store.ErrAuthKeySessionLayerConflict) {
		t.Fatalf("conflicting layer update error = %v, want %v", err, store.ErrAuthKeySessionLayerConflict)
	}
	got, found, err := keys.Get(ctx, id)
	if err != nil || !found || got != want {
		t.Fatalf("auth key changed after layer conflict: got=%+v found=%v err=%v, want=%+v", got, found, err, want)
	}

	if err := keys.UpdateClientInfo(ctx, id, store.AuthKeyClientInfo{
		Layer: 227, DeviceModel: "same-layer", AppVersion: "1.0",
	}); err != nil {
		t.Fatalf("same observed layer metadata merge: %v", err)
	}
	if err := keys.UpdateClientInfo(ctx, id, store.AuthKeyClientInfo{
		Layer: 0, Platform: "windows", SystemVersion: "11",
	}); err != nil {
		t.Fatalf("layerless metadata merge: %v", err)
	}
	got, found, err = keys.Get(ctx, id)
	if err != nil || !found {
		t.Fatalf("get merged auth key: found=%v err=%v", found, err)
	}
	if got.Layer != 227 || got.LayerObservationID != 91 || got.DeviceModel != "same-layer" ||
		got.Platform != "windows" || got.SystemVersion != "11" || got.AppVersion != "1.0" {
		t.Fatalf("guarded metadata merge = %+v", got)
	}
}

func TestTempAuthKeyBindingStoreMergesLayerObservations(t *testing.T) {
	const handshakeExpiry = 1_800_000_000
	tests := []struct {
		name      string
		tempLayer int
		tempObs   int64
		permLayer int
		permObs   int64
		wantLayer int
		wantObs   int64
		wantErr   error
	}{
		{name: "temporary newer", tempLayer: 227, tempObs: 20, permLayer: 220, permObs: 10, wantLayer: 227, wantObs: 20},
		{name: "permanent newer", tempLayer: 220, tempObs: 10, permLayer: 227, permObs: 20, wantLayer: 227, wantObs: 20},
		{name: "equal ordered same layer", tempLayer: 225, tempObs: 30, permLayer: 225, permObs: 30, wantLayer: 225, wantObs: 30},
		{name: "equal ordered conflict", tempLayer: 220, tempObs: 30, permLayer: 227, permObs: 30, wantErr: store.ErrAuthKeySessionLayerConflict},
		{name: "legacy permanent wins", tempLayer: 220, permLayer: 227, wantLayer: 227},
	}
	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			keys := NewAuthKeyStore()
			bindings := NewTempAuthKeyBindingStore(keys)
			tempID := memoryAuthKeyID(int64(1_000 + i*2))
			permID := memoryAuthKeyID(int64(1_001 + i*2))
			tempBefore := store.AuthKeyData{
				ID: tempID, ExpiresAt: handshakeExpiry,
				Layer: tt.tempLayer, LayerObservationID: tt.tempObs, DeviceModel: "temp",
			}
			permBefore := store.AuthKeyData{
				ID: permID, Layer: tt.permLayer, LayerObservationID: tt.permObs, DeviceModel: "perm",
			}
			if err := keys.Save(ctx, tempBefore); err != nil {
				t.Fatalf("save temporary auth key: %v", err)
			}
			if err := keys.Save(ctx, permBefore); err != nil {
				t.Fatalf("save permanent auth key: %v", err)
			}
			binding := domain.TempAuthKeyBinding{
				TempAuthKeyID: tempID,
				PermAuthKeyID: int64(binary.LittleEndian.Uint64(permID[:])),
				ExpiresAt:     handshakeExpiry,
			}
			err := bindings.Save(ctx, binding)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("bind error = %v, want %v", err, tt.wantErr)
				}
				tempAfter, _, _ := keys.Get(ctx, tempID)
				permAfter, _, _ := keys.Get(ctx, permID)
				if tempAfter != tempBefore || permAfter != permBefore {
					t.Fatalf("conflicting bind changed keys: temp=%+v perm=%+v", tempAfter, permAfter)
				}
				if _, found, getErr := bindings.GetByTemp(ctx, tempID); getErr != nil || found {
					t.Fatalf("conflicting binding found=%v err=%v, want absent", found, getErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("bind: %v", err)
			}
			tempAfter, tempFound, getErr := keys.Get(ctx, tempID)
			if getErr != nil || !tempFound {
				t.Fatalf("get temporary after bind: found=%v err=%v", tempFound, getErr)
			}
			permAfter, permFound, getErr := keys.Get(ctx, permID)
			if getErr != nil || !permFound {
				t.Fatalf("get permanent after bind: found=%v err=%v", permFound, getErr)
			}
			if tempAfter.Layer != tt.wantLayer || tempAfter.LayerObservationID != tt.wantObs ||
				permAfter.Layer != tt.wantLayer || permAfter.LayerObservationID != tt.wantObs {
				t.Fatalf("merged defaults: temp=(%d,%d) perm=(%d,%d), want=(%d,%d)",
					tempAfter.Layer, tempAfter.LayerObservationID,
					permAfter.Layer, permAfter.LayerObservationID,
					tt.wantLayer, tt.wantObs)
			}
			if tempAfter.DeviceModel != "temp" || permAfter.DeviceModel != "perm" {
				t.Fatalf("bind erased client metadata: temp=%+v perm=%+v", tempAfter, permAfter)
			}
			if _, found, getErr := bindings.GetByTemp(ctx, tempID); getErr != nil || !found {
				t.Fatalf("merged binding found=%v err=%v, want present", found, getErr)
			}
		})
	}
}

func TestTempAuthKeyBindingStoreIsIdempotentAndRejectsCrossPermanentRebind(t *testing.T) {
	ctx := context.Background()
	keys := NewAuthKeyStore()
	bindings := NewTempAuthKeyBindingStore(keys)
	handshakeExpiry := 400
	permID := memoryAuthKeyID(101)
	otherPermID := memoryAuthKeyID(102)
	first := domain.TempAuthKeyBinding{
		TempAuthKeyID:    [8]byte{8, 7, 6, 5, 4, 3, 2, 1},
		PermAuthKeyID:    int64(binary.LittleEndian.Uint64(permID[:])),
		Nonce:            201,
		TempSessionID:    301,
		ExpiresAt:        handshakeExpiry,
		EncryptedMessage: []byte("first"),
	}
	if err := keys.Save(ctx, store.AuthKeyData{ID: permID}); err != nil {
		t.Fatalf("save permanent auth key: %v", err)
	}
	if err := keys.Save(ctx, store.AuthKeyData{ID: otherPermID}); err != nil {
		t.Fatalf("save second permanent auth key: %v", err)
	}
	if err := keys.Save(ctx, store.AuthKeyData{ID: first.TempAuthKeyID, ExpiresAt: handshakeExpiry}); err != nil {
		t.Fatalf("save temporary auth key: %v", err)
	}
	if err := bindings.Save(ctx, first); err != nil {
		t.Fatalf("save first: %v", err)
	}
	assertMemoryAuthKeyExpiry(t, ctx, keys, first.TempAuthKeyID, handshakeExpiry)

	replayed := first
	replayed.Nonce = 202
	replayed.TempSessionID = 302
	replayed.ExpiresAt = 402
	replayed.EncryptedMessage = []byte("replayed")
	if err := bindings.Save(ctx, replayed); !errors.Is(err, store.ErrAuthKeyBindingInvalid) {
		t.Fatalf("replay with changed expiry error = %v, want %v", err, store.ErrAuthKeyBindingInvalid)
	}
	assertMemoryAuthKeyExpiry(t, ctx, keys, first.TempAuthKeyID, handshakeExpiry)
	got, found, err := bindings.GetByTemp(ctx, first.TempAuthKeyID)
	if err != nil || !found || got.ExpiresAt != first.ExpiresAt || got.Nonce != first.Nonce {
		t.Fatalf("binding after invalid expiry replay = %+v found=%v err=%v, want first binding", got, found, err)
	}

	replayed.ExpiresAt = handshakeExpiry
	if err := bindings.Save(ctx, replayed); err != nil {
		t.Fatalf("replay same normalized binding: %v", err)
	}

	forbidden := replayed
	forbidden.PermAuthKeyID = int64(binary.LittleEndian.Uint64(otherPermID[:]))
	forbidden.ExpiresAt = 999
	forbidden.EncryptedMessage = []byte("must not persist")
	if err := bindings.Save(ctx, forbidden); !errors.Is(err, store.ErrTempAuthKeyAlreadyBound) {
		t.Fatalf("cross-permanent rebind error = %v, want %v", err, store.ErrTempAuthKeyAlreadyBound)
	}
	assertMemoryAuthKeyExpiry(t, ctx, keys, first.TempAuthKeyID, handshakeExpiry)

	got, found, err = bindings.GetByTemp(ctx, first.TempAuthKeyID)
	if err != nil || !found {
		t.Fatalf("get: found=%v err=%v", found, err)
	}
	if got.TempAuthKeyID != replayed.TempAuthKeyID || got.PermAuthKeyID != replayed.PermAuthKeyID ||
		got.Nonce != replayed.Nonce || got.TempSessionID != replayed.TempSessionID || got.ExpiresAt != replayed.ExpiresAt ||
		!bytes.Equal(got.EncryptedMessage, replayed.EncryptedMessage) {
		t.Fatalf("binding changed after forbidden rebind: got %+v, want %+v", got, replayed)
	}
}

func TestTempAuthKeyBindingStoreRejectsMissingTypeAndExpiryViolations(t *testing.T) {
	ctx := context.Background()
	const handshakeExpiry = 500
	tempID := memoryAuthKeyID(201)
	permID := memoryAuthKeyID(202)

	tests := []struct {
		name          string
		temp          *store.AuthKeyData
		perm          *store.AuthKeyData
		bindingExpiry int
	}{
		{
			name:          "missing temporary key",
			perm:          &store.AuthKeyData{ID: permID},
			bindingExpiry: handshakeExpiry,
		},
		{
			name:          "missing permanent key",
			temp:          &store.AuthKeyData{ID: tempID, ExpiresAt: handshakeExpiry},
			bindingExpiry: handshakeExpiry,
		},
		{
			name:          "temporary role uses permanent key",
			temp:          &store.AuthKeyData{ID: tempID},
			perm:          &store.AuthKeyData{ID: permID},
			bindingExpiry: handshakeExpiry,
		},
		{
			name:          "permanent role uses temporary key",
			temp:          &store.AuthKeyData{ID: tempID, ExpiresAt: handshakeExpiry},
			perm:          &store.AuthKeyData{ID: permID, ExpiresAt: handshakeExpiry + 1},
			bindingExpiry: handshakeExpiry,
		},
		{
			name:          "binding expiry differs from handshake",
			temp:          &store.AuthKeyData{ID: tempID, ExpiresAt: handshakeExpiry},
			perm:          &store.AuthKeyData{ID: permID},
			bindingExpiry: handshakeExpiry + 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			keys := NewAuthKeyStore()
			bindings := NewTempAuthKeyBindingStore(keys)
			if tt.temp != nil {
				if err := keys.Save(ctx, *tt.temp); err != nil {
					t.Fatalf("save temporary role key: %v", err)
				}
			}
			if tt.perm != nil {
				if err := keys.Save(ctx, *tt.perm); err != nil {
					t.Fatalf("save permanent role key: %v", err)
				}
			}
			err := bindings.Save(ctx, domain.TempAuthKeyBinding{
				TempAuthKeyID: tempID,
				PermAuthKeyID: int64(binary.LittleEndian.Uint64(permID[:])),
				ExpiresAt:     tt.bindingExpiry,
			})
			if !errors.Is(err, store.ErrAuthKeyBindingInvalid) {
				t.Fatalf("Save error = %v, want %v", err, store.ErrAuthKeyBindingInvalid)
			}
			if _, found, getErr := bindings.GetByTemp(ctx, tempID); getErr != nil || found {
				t.Fatalf("invalid binding found=%v err=%v, want absent", found, getErr)
			}
		})
	}
}

func TestAuthKeyStoreDeletePermanentRemovesBoundTemporaryIdentity(t *testing.T) {
	ctx := context.Background()
	keys := NewAuthKeyStore()
	bindings := NewTempAuthKeyBindingStore(keys)
	tempID := memoryAuthKeyID(301)
	permID := memoryAuthKeyID(302)
	const expiresAt = 600
	if err := keys.Save(ctx, store.AuthKeyData{ID: tempID, ExpiresAt: expiresAt}); err != nil {
		t.Fatalf("save temp: %v", err)
	}
	if err := keys.Save(ctx, store.AuthKeyData{ID: permID}); err != nil {
		t.Fatalf("save perm: %v", err)
	}
	if err := bindings.Save(ctx, domain.TempAuthKeyBinding{
		TempAuthKeyID: tempID,
		PermAuthKeyID: int64(binary.LittleEndian.Uint64(permID[:])),
		ExpiresAt:     expiresAt,
	}); err != nil {
		t.Fatalf("save binding: %v", err)
	}

	if err := keys.Delete(ctx, permID); err != nil {
		t.Fatalf("delete permanent key: %v", err)
	}
	if _, found, err := keys.Get(ctx, permID); err != nil || found {
		t.Fatalf("permanent key found=%v err=%v, want absent", found, err)
	}
	if _, found, err := keys.Get(ctx, tempID); err != nil || found {
		t.Fatalf("bound temporary key found=%v err=%v, want absent", found, err)
	}
	if _, found, err := bindings.GetByTemp(ctx, tempID); err != nil || found {
		t.Fatalf("binding found=%v err=%v, want absent", found, err)
	}
}

func TestTempAuthKeyBindingStoreDeleteExpiredUsesAuthKeyExpiry(t *testing.T) {
	ctx := context.Background()
	keys := NewAuthKeyStore()
	bindings := NewTempAuthKeyBindingStore(keys)
	permID := memoryAuthKeyID(401)
	boundExpiredID := memoryAuthKeyID(402)
	unboundExpiredID := memoryAuthKeyID(403)
	liveID := memoryAuthKeyID(404)
	if err := keys.Save(ctx, store.AuthKeyData{ID: permID}); err != nil {
		t.Fatalf("save perm: %v", err)
	}
	for id, expiry := range map[[8]byte]int{
		boundExpiredID:   700,
		unboundExpiredID: 701,
		liveID:           900,
	} {
		if err := keys.Save(ctx, store.AuthKeyData{ID: id, ExpiresAt: expiry}); err != nil {
			t.Fatalf("save temp %x: %v", id, err)
		}
	}
	if err := bindings.Save(ctx, domain.TempAuthKeyBinding{
		TempAuthKeyID: boundExpiredID,
		PermAuthKeyID: int64(binary.LittleEndian.Uint64(permID[:])),
		ExpiresAt:     700,
	}); err != nil {
		t.Fatalf("save binding: %v", err)
	}

	deleted, err := bindings.DeleteExpired(ctx, 800, 10)
	if err != nil || deleted != 2 {
		t.Fatalf("DeleteExpired = %d, %v; want 2, nil", deleted, err)
	}
	for _, id := range [][8]byte{boundExpiredID, unboundExpiredID} {
		if _, found, getErr := keys.Get(ctx, id); getErr != nil || found {
			t.Fatalf("expired key %x found=%v err=%v, want absent", id, found, getErr)
		}
	}
	if _, found, err := bindings.GetByTemp(ctx, boundExpiredID); err != nil || found {
		t.Fatalf("expired binding found=%v err=%v, want absent", found, err)
	}
	for _, id := range [][8]byte{permID, liveID} {
		if _, found, getErr := keys.Get(ctx, id); getErr != nil || !found {
			t.Fatalf("retained key %x found=%v err=%v, want present", id, found, getErr)
		}
	}
}

func memoryAuthKeyID(id int64) [8]byte {
	var out [8]byte
	binary.LittleEndian.PutUint64(out[:], uint64(id))
	return out
}

func assertMemoryAuthKeyExpiry(
	t *testing.T,
	ctx context.Context,
	keys store.AuthKeyStore,
	id [8]byte,
	want int,
) {
	t.Helper()
	got, found, err := keys.Get(ctx, id)
	if err != nil || !found {
		t.Fatalf("get auth key: found=%v err=%v", found, err)
	}
	if got.ExpiresAt != want {
		t.Fatalf("auth key expires_at = %d, want handshake expiry %d", got.ExpiresAt, want)
	}
}
