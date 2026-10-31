// Package systemidentity applies the server identity configured in the admin panel's
// Server Settings → Server identity to the running main server: the official system
// account's (777000) display name, and, when an icon is configured, its avatar.
//
// The admin binary and the main server binary are two processes that share state only
// through identity.json on disk (see the internal/identity package comment). The main
// server therefore cannot settle for a single read at startup: the name and icon must keep
// following panel changes at runtime, otherwise an operator's save would not take effect
// until the server restarts. The Watcher polls identity.Store at a fixed interval,
// comparing name and icon separately and re-applying only the one that actually changed —
// a name change does not rebuild the avatar, only an icon change does. The login
// notification template already reads live on every send and does not go through here.
package systemidentity

import (
	"context"
	"fmt"
	"time"

	"go.uber.org/zap"

	"telesrv/internal/domain"
	"telesrv/internal/identity"
)

// AvatarSeeder seeds the official system account's avatar with the operator's icon bytes
// (empty means fall back to the built-in default avatar) and sets it as the current
// avatar. Usually bound to botavatars.SeedOfficialSystemAvatar on the files service.
type AvatarSeeder func(ctx context.Context, icon []byte, now int64) (bool, error)

// Apply reads the stored identity and projects it onto the running server in one pass:
// the 777000 display name (via domain), and the official system account avatar (when a
// seeder is provided). Called once during startup; incremental runtime updates are left
// to the Watcher.
//
// The name is written even if the seeder fails — the name projection is a cheap,
// independent step and should not be lost along with an avatar-seeding error.
func Apply(ctx context.Context, store *identity.Store, seedAvatar AvatarSeeder, now int64) (identity.Info, error) {
	info, err := store.Get()
	if err != nil {
		return identity.Info{}, err
	}
	domain.SetOfficialSystemUserDisplayName(info.Name)
	if seedAvatar != nil {
		if err := seedAvatarFromStore(ctx, store, seedAvatar, now); err != nil {
			return info, err
		}
	}
	return info, nil
}

func seedAvatarFromStore(ctx context.Context, store *identity.Store, seedAvatar AvatarSeeder, now int64) error {
	var icon []byte
	if data, _, ok := store.Icon(); ok {
		icon = data
	}
	if _, err := seedAvatar(ctx, icon, now); err != nil {
		return fmt.Errorf("seed official system avatar: %w", err)
	}
	return nil
}

// Watcher polls identity.Store and re-applies when the name or icon changes. A name change
// only updates the display name, while an icon change rebuilds the 777000 avatar (including
// falling back to the built-in default avatar after the icon is removed).
type Watcher struct {
	Store      *identity.Store
	SeedAvatar AvatarSeeder
	// Interval is the polling interval; <= 0 defaults to 5 seconds.
	Interval time.Duration
	Logger   *zap.Logger
}

// Run polls until ctx is cancelled. It records the current name and icon fingerprint
// before entering the poll loop: startup already applied the identity once (see
// cmd/telesrv/main.go), so this only responds to changes from then on.
func (w *Watcher) Run(ctx context.Context) {
	interval := w.Interval
	if interval <= 0 {
		interval = 5 * time.Second
	}
	name, icon, err := w.snapshot()
	if err != nil {
		w.log().Warn("server identity snapshot failed", zap.Error(err))
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			newName, newIcon, err := w.snapshot()
			if err != nil {
				w.log().Warn("server identity snapshot failed", zap.Error(err))
				continue
			}
			if newName != name {
				domain.SetOfficialSystemUserDisplayName(newName)
				name = newName
				w.log().Info("server identity name applied", zap.String("name", newName))
			}
			if newIcon == icon {
				continue
			}
			if err := seedAvatarFromStore(ctx, w.Store, w.SeedAvatar, time.Now().Unix()); err != nil {
				w.log().Warn("apply server identity avatar failed", zap.Error(err))
				continue
			}
			icon = newIcon
			w.log().Info("server identity avatar applied")
		}
	}
}

func (w *Watcher) snapshot() (string, string, error) {
	info, err := w.Store.Get()
	if err != nil {
		return "", "", err
	}
	icon, err := w.Store.IconFingerprint()
	if err != nil {
		return "", "", err
	}
	return info.Name, icon, nil
}

func (w *Watcher) log() *zap.Logger {
	if w.Logger != nil {
		return w.Logger
	}
	return zap.NewNop()
}
