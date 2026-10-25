package systemidentity

import (
	"bytes"
	"context"
	"testing"
	"time"

	"telesrv/internal/domain"
	"telesrv/internal/identity"
)

func resetOfficialDisplayName(t *testing.T) {
	t.Helper()
	t.Cleanup(func() { domain.SetOfficialSystemUserDisplayName("") })
}

func TestApplyProjectsStoredIdentityOntoOfficialSystemUser(t *testing.T) {
	resetOfficialDisplayName(t)

	store := identity.NewStore(t.TempDir())
	if err := store.SetText("Acme Corp", "customer support"); err != nil {
		t.Fatalf("SetText: %v", err)
	}
	iconBytes := []byte("custom-icon-bytes")
	if err := store.SetIcon(iconBytes, ".png"); err != nil {
		t.Fatalf("SetIcon: %v", err)
	}

	var seededIcon []byte
	var seededAt int64
	seeder := func(_ context.Context, icon []byte, now int64) (bool, error) {
		seededIcon = append([]byte(nil), icon...)
		seededAt = now
		return true, nil
	}

	info, err := Apply(context.Background(), store, seeder, 1234)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if info.Name != "Acme Corp" {
		t.Fatalf("Apply info.Name = %q, want Acme Corp", info.Name)
	}
	if got := domain.OfficialSystemUser().FirstName; got != "Acme Corp" {
		t.Fatalf("official system user FirstName = %q, want Acme Corp", got)
	}
	if !bytes.Equal(seededIcon, iconBytes) {
		t.Fatalf("seeder icon = %q, want the stored icon %q", seededIcon, iconBytes)
	}
	if seededAt != 1234 {
		t.Fatalf("seeder now = %d, want 1234", seededAt)
	}
}

func TestApplyFallsBackToBrandNameWithoutIdentity(t *testing.T) {
	resetOfficialDisplayName(t)

	store := identity.NewStore(t.TempDir())
	if _, err := Apply(context.Background(), store, nil, 0); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if got := domain.OfficialSystemUser().FirstName; got != "Telesrv" {
		t.Fatalf("official system user FirstName = %q, want branded default Telesrv", got)
	}
}

func TestWatcherAppliesNameWithoutTouchingAvatar(t *testing.T) {
	resetOfficialDisplayName(t)

	store := identity.NewStore(t.TempDir())
	if err := store.SetText("First", ""); err != nil {
		t.Fatalf("SetText: %v", err)
	}

	avatarCalls := make(chan struct{}, 8)
	seeder := func(_ context.Context, _ []byte, _ int64) (bool, error) {
		avatarCalls <- struct{}{}
		return false, nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go (&Watcher{Store: store, SeedAvatar: seeder, Interval: 10 * time.Millisecond}).Run(ctx)

	// Let Run take its initial snapshot ("First") before changing the store,
	// so the change is guaranteed to be seen as a change.
	time.Sleep(30 * time.Millisecond)
	if err := store.SetText("Second", ""); err != nil {
		t.Fatalf("SetText: %v", err)
	}
	waitForName(t, "Second")

	select {
	case <-avatarCalls:
		t.Fatal("a name-only change must not reseed the avatar")
	case <-time.After(50 * time.Millisecond):
	}
}

func TestWatcherSeedsAvatarOnIconChangeAndRevertsOnRemoval(t *testing.T) {
	resetOfficialDisplayName(t)

	store := identity.NewStore(t.TempDir())
	if err := store.SetText("Srv", ""); err != nil {
		t.Fatalf("SetText: %v", err)
	}

	avatarCalls := make(chan []byte, 8)
	seeder := func(_ context.Context, icon []byte, _ int64) (bool, error) {
		avatarCalls <- append([]byte(nil), icon...)
		return len(icon) > 0, nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go (&Watcher{Store: store, SeedAvatar: seeder, Interval: 10 * time.Millisecond}).Run(ctx)

	// No icon yet: the watcher must not seed anything.
	select {
	case icon := <-avatarCalls:
		t.Fatalf("watcher seeded avatar %q before an icon was set", icon)
	case <-time.After(60 * time.Millisecond):
	}

	if err := store.SetIcon([]byte("icon-a"), ".png"); err != nil {
		t.Fatalf("SetIcon: %v", err)
	}
	expectIcon(t, avatarCalls, []byte("icon-a"))

	if err := store.RemoveIcon(); err != nil {
		t.Fatalf("RemoveIcon: %v", err)
	}
	expectIcon(t, avatarCalls, nil)
}

func waitForName(t *testing.T, want string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if domain.OfficialSystemUser().FirstName == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("official system user FirstName = %q, want %q", domain.OfficialSystemUser().FirstName, want)
}

func expectIcon(t *testing.T, calls <-chan []byte, want []byte) {
	t.Helper()
	select {
	case icon := <-calls:
		if !bytes.Equal(icon, want) {
			t.Fatalf("seeded avatar icon = %q, want %q", icon, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("watcher did not seed avatar for icon %q", want)
	}
}
