package botavatars

import (
	"context"
	"errors"
	"testing"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

// recordingAvatarSetter is a minimal AvatarSetter that records the bytes of
// every created photo and lets the current photo be replaced, matching the
// real media store (AddProfilePhotoKind appends a newer active row). SeedTx
// rolls back photos, bindings, deliveries and seed states when fn fails.
type recordingAvatarSetter struct {
	current    map[int64]int64
	created    map[int64][]byte
	seeds      map[string]string
	deliveries int
	nextID     int64
}

func newRecordingAvatarSetter() *recordingAvatarSetter {
	return &recordingAvatarSetter{
		current: make(map[int64]int64),
		created: make(map[int64][]byte),
		seeds:   make(map[string]string),
	}
}

func (f *recordingAvatarSetter) CurrentProfilePhotoKind(_ context.Context, _ domain.PeerType, peerID int64, _ domain.ProfilePhotoKind) (domain.Photo, bool, error) {
	id, ok := f.current[peerID]
	return domain.Photo{ID: id}, ok, nil
}

func (f *recordingAvatarSetter) CreateAvatarFromBytes(_ context.Context, data []byte) (domain.Photo, error) {
	f.nextID++
	f.created[f.nextID] = append([]byte(nil), data...)
	return domain.Photo{ID: f.nextID}, nil
}

func (f *recordingAvatarSetter) CreateAvatarVideoFromBytes(_ context.Context, _ []byte, _ float64) (domain.Photo, error) {
	return domain.Photo{}, nil
}

func (f *recordingAvatarSetter) SetCurrentProfilePhotoKind(_ context.Context, ownerType domain.PeerType, peerID int64, kind domain.ProfilePhotoKind, photoID int64, _ int, effects store.DeliveryEffectsBuilder[store.ProfilePhotoMutation]) (store.ProfilePhotoMutation, bool, error) {
	if effects == nil {
		return store.ProfilePhotoMutation{}, false, store.ErrDeliveryOutboxRequired
	}
	snapshot := store.ProfilePhotoMutation{OwnerType: ownerType, OwnerID: peerID, Kind: kind, Current: domain.Photo{ID: photoID}, HasCurrent: true}
	built, err := effects(snapshot)
	if err != nil {
		return store.ProfilePhotoMutation{}, false, err
	}
	f.current[peerID] = photoID
	f.deliveries += len(built)
	return snapshot, true, nil
}

func (f *recordingAvatarSetter) SeedState(_ context.Context, key string) (string, bool, error) {
	hash, ok := f.seeds[key]
	return hash, ok, nil
}

func (f *recordingAvatarSetter) PutSeedState(_ context.Context, key, hash string) error {
	f.seeds[key] = hash
	return nil
}

func (f *recordingAvatarSetter) SeedTx(ctx context.Context, fn func(ctx context.Context, tx AvatarSetter) error) error {
	current := make(map[int64]int64, len(f.current))
	for k, v := range f.current {
		current[k] = v
	}
	seeds := make(map[string]string, len(f.seeds))
	for k, v := range f.seeds {
		seeds[k] = v
	}
	deliveries := f.deliveries
	if err := fn(ctx, f); err != nil {
		f.current, f.seeds, f.deliveries = current, seeds, deliveries
		return err
	}
	return nil
}

func TestSeedOfficialSystemAvatarUsesCustomIcon(t *testing.T) {
	av := newRecordingAvatarSetter()
	custom := []byte("operator-icon")

	usingCustom, err := SeedOfficialSystemAvatar(context.Background(), av, newFakeDelivery(), custom, 1000)
	if err != nil {
		t.Fatalf("SeedOfficialSystemAvatar: %v", err)
	}
	if !usingCustom {
		t.Fatal("expected usingCustom=true with a non-empty icon")
	}
	photoID, ok := av.current[domain.OfficialSystemUserID]
	if !ok || photoID == 0 {
		t.Fatalf("official system account has no avatar: %+v", av.current)
	}
	if got := av.created[photoID]; string(got) != string(custom) {
		t.Fatalf("avatar bytes = %q, want the custom icon %q", got, custom)
	}
}

func TestSeedOfficialSystemAvatarReplacesExistingWithCustomIcon(t *testing.T) {
	av := newRecordingAvatarSetter()
	av.current[domain.OfficialSystemUserID] = 99
	av.created[99] = []byte("previous")
	custom := []byte("new-operator-icon")

	if _, err := SeedOfficialSystemAvatar(context.Background(), av, newFakeDelivery(), custom, 1000); err != nil {
		t.Fatalf("SeedOfficialSystemAvatar: %v", err)
	}
	photoID := av.current[domain.OfficialSystemUserID]
	if photoID == 99 {
		t.Fatal("custom icon did not replace the existing avatar")
	}
	if got := av.created[photoID]; string(got) != string(custom) {
		t.Fatalf("avatar bytes = %q, want the custom icon %q", got, custom)
	}
}

func TestSeedOfficialSystemAvatarSeedsDefaultWithoutCustomIcon(t *testing.T) {
	av := newRecordingAvatarSetter()

	usingCustom, err := SeedOfficialSystemAvatar(context.Background(), av, newFakeDelivery(), nil, 1000)
	if err != nil {
		t.Fatalf("SeedOfficialSystemAvatar: %v", err)
	}
	if usingCustom {
		t.Fatal("expected usingCustom=false without a custom icon")
	}
	first := av.current[domain.OfficialSystemUserID]
	if first == 0 {
		t.Fatal("default avatar was not seeded for the official system account")
	}
	if len(av.created[first]) == 0 {
		t.Fatal("default avatar bytes are empty")
	}
}

func TestSeedOfficialSystemAvatarRevertsToDefaultOnIconRemoval(t *testing.T) {
	av := newRecordingAvatarSetter()
	av.current[domain.OfficialSystemUserID] = 99
	av.created[99] = []byte("previous-custom")

	// Removing the operator icon (nil) must replace the custom avatar with the
	// bundled default rather than leave the stale custom one in place.
	usingCustom, err := SeedOfficialSystemAvatar(context.Background(), av, newFakeDelivery(), nil, 2000)
	if err != nil {
		t.Fatalf("SeedOfficialSystemAvatar: %v", err)
	}
	if usingCustom {
		t.Fatal("expected usingCustom=false without a custom icon")
	}
	photoID := av.current[domain.OfficialSystemUserID]
	if photoID == 99 {
		t.Fatal("removing the custom icon did not replace the existing avatar")
	}
	if len(av.created[photoID]) == 0 {
		t.Fatal("default avatar bytes are empty")
	}
}

func TestSeedOfficialSystemAvatarIsIdempotentForUnchangedIcon(t *testing.T) {
	av := newRecordingAvatarSetter()
	delivery := newFakeDelivery()
	custom := []byte("operator-icon")
	if _, err := SeedOfficialSystemAvatar(context.Background(), av, delivery, custom, 1000); err != nil {
		t.Fatalf("first seed: %v", err)
	}
	first := av.current[domain.OfficialSystemUserID]
	// A restart or a second Core instance applies the same identity again.
	if _, err := SeedOfficialSystemAvatar(context.Background(), av, delivery, custom, 1001); err != nil {
		t.Fatalf("second seed: %v", err)
	}
	if got := av.current[domain.OfficialSystemUserID]; got != first || av.nextID != 1 {
		t.Fatalf("unchanged icon reseeded: current=%d created=%d", got, av.nextID)
	}
	if av.deliveries != 1 || delivery.committed[domain.OfficialSystemUserID] != 1 {
		t.Fatalf("unchanged icon delivered again: deliveries=%d committed=%d", av.deliveries, delivery.committed[domain.OfficialSystemUserID])
	}
}

func TestSeedOfficialSystemAvatarKeepsOperatorAvatarUntilIconChanges(t *testing.T) {
	av := newRecordingAvatarSetter()
	delivery := newFakeDelivery()
	icon := []byte("operator-icon")
	if _, err := SeedOfficialSystemAvatar(context.Background(), av, delivery, icon, 1000); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// An operator later sets 777000's avatar through the admin account command.
	av.current[domain.OfficialSystemUserID] = 500
	if _, err := SeedOfficialSystemAvatar(context.Background(), av, delivery, icon, 1001); err != nil {
		t.Fatalf("restart seed: %v", err)
	}
	if got := av.current[domain.OfficialSystemUserID]; got != 500 {
		t.Fatalf("startup with the same identity icon replaced the operator avatar: %d", got)
	}
	if _, err := SeedOfficialSystemAvatar(context.Background(), av, delivery, []byte("new-icon"), 1002); err != nil {
		t.Fatalf("changed icon seed: %v", err)
	}
	if got := av.current[domain.OfficialSystemUserID]; got == 500 || string(av.created[got]) != "new-icon" {
		t.Fatalf("changed identity icon did not apply: current=%d", got)
	}
}

func TestSeedOfficialSystemAvatarRequiresDeliveryAndRollsBackOnDeliveryError(t *testing.T) {
	av := newRecordingAvatarSetter()
	if _, err := SeedOfficialSystemAvatar(context.Background(), av, nil, nil, 1000); err == nil {
		t.Fatal("seed without delivery succeeded")
	}
	delivery := newFakeDelivery()
	delivery.err = errors.New("projection unavailable")
	if _, err := SeedOfficialSystemAvatar(context.Background(), av, delivery, []byte("icon"), 1000); err == nil {
		t.Fatal("seed with failing delivery succeeded")
	}
	if len(av.current) != 0 || len(av.seeds) != 0 || av.deliveries != 0 {
		t.Fatalf("failed seed left state: current=%v seeds=%v deliveries=%d", av.current, av.seeds, av.deliveries)
	}
}
