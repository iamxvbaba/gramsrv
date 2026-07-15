package store

import (
	"errors"
	"testing"
	"time"
)

func TestMergeAuthKeyLayerObservations(t *testing.T) {
	tests := []struct {
		name      string
		tempLayer int
		tempID    int64
		permLayer int
		permID    int64
		wantLayer int
		wantID    int64
		wantErr   error
	}{
		{name: "temporary newer", tempLayer: 227, tempID: 20, permLayer: 220, permID: 10, wantLayer: 227, wantID: 20},
		{name: "permanent newer", tempLayer: 220, tempID: 10, permLayer: 227, permID: 20, wantLayer: 227, wantID: 20},
		{name: "equal ordered observation", tempLayer: 225, tempID: 30, permLayer: 225, permID: 30, wantLayer: 225, wantID: 30},
		{name: "equal ordered conflict", tempLayer: 220, tempID: 30, permLayer: 227, permID: 30, wantErr: ErrAuthKeySessionLayerConflict},
		{name: "legacy permanent wins", tempLayer: 220, permLayer: 227, wantLayer: 227},
		{name: "legacy permanent zero wins", tempLayer: 220, wantLayer: 0},
		{name: "negative observation", tempLayer: 220, tempID: -1, permLayer: 227, wantErr: ErrAuthKeySessionLayerInvalid},
		{name: "ordered zero layer", tempLayer: 0, tempID: 1, permLayer: 227, wantErr: ErrAuthKeySessionLayerInvalid},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotLayer, gotID, err := MergeAuthKeyLayerObservations(tt.tempLayer, tt.tempID, tt.permLayer, tt.permID)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("merge error = %v, want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil || gotLayer != tt.wantLayer || gotID != tt.wantID {
				t.Fatalf("merge = (%d,%d,%v), want (%d,%d,nil)", gotLayer, gotID, err, tt.wantLayer, tt.wantID)
			}
		})
	}
}

func TestAuthKeySessionLayerExpiryIsOwnedByClientMessageID(t *testing.T) {
	const (
		seconds    = int64(2_000_000_000)
		fractional = int64(123_456_788) // non-zero and client-owned (mod 4 == 0)
	)
	msgID := (seconds << 32) | fractional
	got, ok := AuthKeySessionLayerExpiry(msgID)
	want := time.Unix(seconds, int64(int32(msgID))).UTC().Add(301 * time.Second)
	if !ok || !got.Equal(want) {
		t.Fatalf("expiry = (%v,%v), want (%v,true)", got, ok, want)
	}
	for _, invalid := range []int64{0, -4, seconds << 32, msgID + 1, msgID + 3} {
		if expiry, ok := AuthKeySessionLayerExpiry(invalid); ok || !expiry.IsZero() {
			t.Fatalf("invalid msg_id %d expiry = (%v,%v)", invalid, expiry, ok)
		}
	}
	now := time.Unix(seconds, 0).UTC()
	if expiry, ok := AuthKeySessionLayerEvidenceFresh(now, msgID); !ok || !expiry.Equal(want) {
		t.Fatalf("fresh evidence = (%v,%v), want (%v,true)", expiry, ok, want)
	}
	stale := (now.Add(-302*time.Second).Unix() << 32) | 4
	tooFuture := (now.Add(31*time.Second).Unix() << 32) | 4
	for _, invalid := range []int64{stale, tooFuture} {
		if expiry, ok := AuthKeySessionLayerEvidenceFresh(now, invalid); ok || !expiry.IsZero() {
			t.Fatalf("out-of-window msg_id %d evidence = (%v,%v)", invalid, expiry, ok)
		}
	}
}
