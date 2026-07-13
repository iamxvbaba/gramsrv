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
