package rpc

import (
	"context"
	"errors"
	"testing"

	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap/zaptest"

	appusers "telesrv/internal/app/users"
	"telesrv/internal/domain"
	"telesrv/internal/store"
	"telesrv/internal/store/memory"
)

// TestProfilePhotoSeedEffectsBuildsSelfDeliveryForBuiltInAccount pins the
// startup avatar seed boundary: the builder targets the seeded account itself
// with the same self-user updateUser projection as the admin avatar command,
// and rejects a snapshot for any other owner.
func TestProfilePhotoSeedEffectsBuildsSelfDeliveryForBuiltInAccount(t *testing.T) {
	ctx := context.Background()
	users := memory.NewUserStore()
	bot, err := users.Create(ctx, domain.User{AccessHash: 6101, FirstName: "Seed Bot", Bot: true})
	if err != nil {
		t.Fatalf("create bot: %v", err)
	}
	r := New(Config{DC: 2, IP: "127.0.0.1", Port: 2398}, Deps{Users: appusers.NewService(users)}, zaptest.NewLogger(t), clock.System)

	build, err := r.ProfilePhotoSeedEffects(ctx, bot.ID)
	if err != nil {
		t.Fatalf("ProfilePhotoSeedEffects: %v", err)
	}
	photo := domain.Photo{ID: 7101, DCID: 2}
	effects, err := build(store.ProfilePhotoMutation{
		OwnerType: domain.PeerTypeUser, OwnerID: bot.ID, Kind: domain.ProfilePhotoKindProfile,
		Current: photo, HasCurrent: true,
	})
	if err != nil {
		t.Fatalf("build effects: %v", err)
	}
	if len(effects) != 1 || effects[0].TargetUserID != bot.ID || effects[0].Kind != store.DeliveryEffectAbsolute {
		t.Fatalf("effects = %+v, want one absolute self delivery to %d", effects, bot.ID)
	}
	decoded, err := decodeDeliveryUpdate(effects[0].Payload)
	if err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	updates, ok := decoded.(*tg.Updates)
	if !ok || len(updates.Updates) != 1 || len(updates.Users) != 1 {
		t.Fatalf("payload = %#v, want one updateUser with one user", decoded)
	}
	if upd, ok := updates.Updates[0].(*tg.UpdateUser); !ok || upd.UserID != bot.ID {
		t.Fatalf("update = %#v, want updateUser(%d)", updates.Updates[0], bot.ID)
	}
	user, ok := updates.Users[0].(*tg.User)
	if !ok || user.ID != bot.ID {
		t.Fatalf("user = %#v, want projected seed account", updates.Users[0])
	}
	if p, ok := user.Photo.(*tg.UserProfilePhoto); !ok || p.PhotoID != photo.ID {
		t.Fatalf("projected photo = %#v, want photo %d", user.Photo, photo.ID)
	}

	if _, err := build(store.ProfilePhotoMutation{OwnerType: domain.PeerTypeUser, OwnerID: bot.ID + 1, Kind: domain.ProfilePhotoKindProfile}); err == nil {
		t.Fatal("builder accepted a snapshot for a different owner")
	}
}

func TestProfilePhotoSeedEffectsRejectsMissingUsersDependency(t *testing.T) {
	r := New(Config{DC: 2}, Deps{}, zaptest.NewLogger(t), clock.System)
	if _, err := r.ProfilePhotoSeedEffects(context.Background(), domain.BotFatherUserID); err == nil {
		t.Fatal("ProfilePhotoSeedEffects succeeded without a users dependency")
	} else if errors.Is(err, domain.ErrUserNotFound) {
		t.Fatalf("missing dependency must not be reported as a missing user: %v", err)
	}
}
