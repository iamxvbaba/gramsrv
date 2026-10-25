package botavatars

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

//go:embed *.jpg *.mp4
var assets embed.FS

// peersForSeed returns the peer→avatar mapping, using the configured
// Premium bot ID instead of the compile-time constant so that deployments
// with TELESRV_PREMIUM_BOT_USER_ID get the avatar on the right identity.
//
// The official system account (777000) is deliberately absent: its avatar is
// operator-overridable (Server Settings → Server identity), so it is handled by
// SeedOfficialSystemAvatar instead of the fixed, skip-if-present Seed loop here.
func peersForSeed() map[int64]string {
	return map[int64]string{
		domain.BotFatherUserID:              "botfather.jpg",
		domain.StickersBotUserID:            "stickers.jpg",
		domain.VerifyBotUserID:              "verifybot.jpg",
		domain.GifBotUserID:                 "gif.jpg",
		domain.PremiumBotConfiguredUserID(): "premiumbot.mp4",
	}
}

// AvatarSetter is the subset of the files service used to assign avatars.
type AvatarSetter interface {
	CurrentProfilePhotoKind(ctx context.Context, ownerType domain.PeerType, ownerID int64, kind domain.ProfilePhotoKind) (domain.Photo, bool, error)
	CreateAvatarFromBytes(ctx context.Context, data []byte) (domain.Photo, error)
	CreateAvatarVideoFromBytes(ctx context.Context, data []byte, videoStartTs float64) (domain.Photo, error)
	SetCurrentProfilePhotoKind(ctx context.Context, ownerType domain.PeerType, ownerID int64, kind domain.ProfilePhotoKind, photoID int64, date int, effects store.DeliveryEffectsBuilder[store.ProfilePhotoMutation]) (store.ProfilePhotoMutation, bool, error)
	// SeedTx runs fn inside a single transaction that holds a serialising
	// advisory lock. If fn returns an error the whole transaction (including
	// any media created inside) is rolled back; otherwise it is committed.
	// This makes the check+create+bind sequence atomic and orphan-free across
	// concurrent or multi-instance startup. The tx AvatarSetter passed to fn
	// must route its calls through the same transaction and lock.
	SeedTx(ctx context.Context, fn func(ctx context.Context, tx AvatarSetter) error) error
	// SeedState/PutSeedState read and record a seed fingerprint in
	// seed_states through the same transaction as the avatar writes.
	SeedState(ctx context.Context, key string) (string, bool, error)
	PutSeedState(ctx context.Context, key, hash string) error
}

// Delivery supplies the durable delivery fact that every v2 current-avatar
// write must commit in the same transaction, and the post-commit projection
// invalidation. The RPC router implements it with the same self-user
// projection used by the admin avatar command.
type Delivery interface {
	ProfilePhotoSeedEffects(ctx context.Context, userID int64) (store.DeliveryEffectsBuilder[store.ProfilePhotoMutation], error)
	ProfilePhotoCommitted(userID int64)
}

// Seed assigns embedded avatars to the built-in system account and bots.
// It is idempotent: peers that already have a profile photo are skipped, and
// the whole run is atomic inside the advisory-locked SeedTx transaction. Any
// error fails the seed and aborts the transaction, so a partially written
// avatar photo or delivery fact is rolled back rather than orphaned.
func Seed(ctx context.Context, av AvatarSetter, delivery Delivery, now int64) error {
	if delivery == nil {
		return errors.New("bot avatar: profile photo delivery is not configured")
	}
	var seeded []int64
	err := av.SeedTx(ctx, func(ctx context.Context, txAv AvatarSetter) error {
		seeded = seeded[:0]
		for peerID, fname := range peersForSeed() {
			if _, ok, err := txAv.CurrentProfilePhotoKind(ctx, domain.PeerTypeUser, peerID, domain.ProfilePhotoKindProfile); err != nil {
				return fmt.Errorf("bot avatar: read current photo for peer %d: %w", peerID, err)
			} else if ok {
				continue
			}
			data, err := fs.ReadFile(assets, fname)
			if err != nil {
				return fmt.Errorf("bot avatar: read embedded asset %q: %w", fname, err)
			}
			var photo domain.Photo
			if isVideo(fname) {
				photo, err = txAv.CreateAvatarVideoFromBytes(ctx, data, 0)
			} else {
				photo, err = txAv.CreateAvatarFromBytes(ctx, data)
			}
			if err != nil {
				return fmt.Errorf("bot avatar: create avatar for peer %d: %w", peerID, err)
			}
			effects, err := delivery.ProfilePhotoSeedEffects(ctx, peerID)
			if err != nil {
				return fmt.Errorf("bot avatar: build delivery for peer %d: %w", peerID, err)
			}
			if _, found, err := txAv.SetCurrentProfilePhotoKind(ctx, domain.PeerTypeUser, peerID, domain.ProfilePhotoKindProfile, photo.ID, int(now), effects); err != nil {
				return fmt.Errorf("bot avatar: set current photo for peer %d: %w", peerID, err)
			} else if !found {
				return fmt.Errorf("bot avatar: bind current photo for peer %d: not found", peerID)
			}
			seeded = append(seeded, peerID)
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, peerID := range seeded {
		delivery.ProfilePhotoCommitted(peerID)
	}
	return nil
}

func isVideo(name string) bool {
	return len(name) > 4 && name[len(name)-4:] == ".mp4"
}

// officialAvatarSeedStateKey records which icon content the official system
// account's (777000) current avatar was seeded from, in the seed_states table.
const officialAvatarSeedStateKey = "official_system_avatar"

// SeedOfficialSystemAvatar assigns the built-in official system account's
// (777000) profile photo from the operator's Server Settings → Server identity
// icon, mirroring the identity store's "configured, else bundled default"
// contract.
//
// customIcon, when non-empty, is the operator's uploaded icon (any format the
// files pipeline accepts; it is re-encoded like any avatar). When empty, the
// bundled default telegram.jpg is used. The SHA-256 of the chosen source bytes
// is recorded in seed_states inside the same advisory-locked SeedTx as the
// binding and its durable delivery fact: when it already matches and the
// account still has a current avatar, the call is a no-op. Every Core
// instance can therefore apply identity at startup and from its watcher
// without churning photos or deliveries, and an avatar set later through the
// admin account command survives until the identity icon actually changes.
//
// It returns true when a custom icon is in effect (for the startup log line).
func SeedOfficialSystemAvatar(ctx context.Context, av AvatarSetter, delivery Delivery, customIcon []byte, now int64) (bool, error) {
	if delivery == nil {
		return false, errors.New("official system avatar: profile photo delivery is not configured")
	}
	usingCustom := len(customIcon) > 0
	data := customIcon
	if !usingCustom {
		embedded, err := fs.ReadFile(assets, "telegram.jpg")
		if err != nil {
			return false, fmt.Errorf("official system avatar: read embedded asset: %w", err)
		}
		data = embedded
	}
	digest := sha256.Sum256(data)
	want := hex.EncodeToString(digest[:])
	changed := false
	err := av.SeedTx(ctx, func(ctx context.Context, txAv AvatarSetter) error {
		changed = false
		applied, found, err := txAv.SeedState(ctx, officialAvatarSeedStateKey)
		if err != nil {
			return fmt.Errorf("official system avatar: read seed state: %w", err)
		}
		if found && applied == want {
			if _, ok, err := txAv.CurrentProfilePhotoKind(ctx, domain.PeerTypeUser, domain.OfficialSystemUserID, domain.ProfilePhotoKindProfile); err != nil {
				return fmt.Errorf("official system avatar: read current photo: %w", err)
			} else if ok {
				return nil
			}
		}
		photo, err := txAv.CreateAvatarFromBytes(ctx, data)
		if err != nil {
			return fmt.Errorf("official system avatar: create avatar: %w", err)
		}
		effects, err := delivery.ProfilePhotoSeedEffects(ctx, domain.OfficialSystemUserID)
		if err != nil {
			return fmt.Errorf("official system avatar: build delivery: %w", err)
		}
		if _, found, err := txAv.SetCurrentProfilePhotoKind(ctx, domain.PeerTypeUser, domain.OfficialSystemUserID, domain.ProfilePhotoKindProfile, photo.ID, int(now), effects); err != nil {
			return fmt.Errorf("official system avatar: set current photo: %w", err)
		} else if !found {
			return fmt.Errorf("official system avatar: bind current photo: not found")
		}
		if err := txAv.PutSeedState(ctx, officialAvatarSeedStateKey, want); err != nil {
			return fmt.Errorf("official system avatar: record seed state: %w", err)
		}
		changed = true
		return nil
	})
	if err != nil {
		return false, err
	}
	if changed {
		delivery.ProfilePhotoCommitted(domain.OfficialSystemUserID)
	}
	return usingCustom, nil
}
