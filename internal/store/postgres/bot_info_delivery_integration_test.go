package postgres

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

func TestBotInfoDeliveryAtomicLocalizationAndReplayPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	users := NewUserStore(pool)
	bots := NewBotStore(pool)
	suffix := time.Now().UnixNano() % 1_000_000_000
	owner, err := users.Create(ctx, domain.User{AccessHash: suffix, Phone: testPhone(suffix, 31), FirstName: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	viewer, err := users.Create(ctx, domain.User{AccessHash: suffix + 1, Phone: testPhone(suffix, 32), FirstName: "viewer"})
	if err != nil {
		t.Fatal(err)
	}
	bot, _, err := bots.CreateBotAccountWithDelivery(ctx, domain.User{AccessHash: suffix + 2, FirstName: "Default", Username: fmt.Sprintf("localized_%d_bot", suffix)}, domain.BotProfile{OwnerUserID: owner.ID}, testBotLifecycleEffects)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM edge_delivery_outbox WHERE target_user_id = ANY($1::bigint[])`, []int64{owner.ID, viewer.ID})
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE id = ANY($1::bigint[])`, []int64{owner.ID, viewer.ID, bot.ID})
	})
	if _, err := pool.Exec(ctx, `DELETE FROM edge_delivery_outbox WHERE target_user_id = $1 AND payload = $2`, owner.ID, testBotLifecyclePayload); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO dialogs (user_id, peer_type, peer_id, top_message_id, top_message_date)
VALUES ($1, 'user', $2, 0, 0)
ON CONFLICT (user_id, peer_type, peer_id) DO NOTHING`, viewer.ID, bot.ID); err != nil {
		t.Fatal(err)
	}

	builds := 0
	payloads := make([][]byte, 0, 3)
	effects := func(snapshot store.UserAudienceDeliverySnapshot) ([]store.DeliveryEffect, error) {
		builds++
		if len(snapshot.Audience) != 2 || snapshot.Audience[0] != owner.ID || snapshot.Audience[1] != viewer.ID {
			return nil, fmt.Errorf("unexpected audience %v", snapshot.Audience)
		}
		payload := []byte(fmt.Sprintf("bot-info-%d-%d", suffix, snapshot.User.BotInfoVersion))
		payloads = append(payloads, payload)
		out := make([]store.DeliveryEffect, len(snapshot.Audience))
		for i, viewerID := range snapshot.Audience {
			out[i] = store.AbsoluteDeliveryEffect(store.DeliveryOutboxEnqueue{
				TargetUserID: viewerID, Payload: payload, RecoveryPolicy: store.OutboxRecoveryAbsoluteReload,
			})
		}
		return out, nil
	}
	before, _, _ := users.ByID(ctx, bot.ID)
	version, changed, err := bots.UpdateBotInfoWithDelivery(ctx, bot.ID, domain.BotInfoUpdate{
		SetName: true, Name: "Renamed", SetAbout: true, About: "default about", SetDescription: true, Description: "default description",
	}, effects)
	if err != nil || !changed || version != before.BotInfoVersion+1 {
		t.Fatalf("default update = version:%d changed:%v err:%v", version, changed, err)
	}
	defaults, found, err := bots.GetBotInfo(ctx, bot.ID, "")
	if err != nil || !found || defaults.Name != "Renamed" || defaults.About != "default about" || defaults.Description != "default description" {
		t.Fatalf("default info = %+v found=%v err=%v", defaults, found, err)
	}
	assertBotInfoDeliveryPayloadRows(t, ctx, pool, payloads[0], []int64{owner.ID, viewer.ID})

	version, changed, err = bots.UpdateBotInfoWithDelivery(ctx, bot.ID, domain.BotInfoUpdate{LangCode: "zh-hans", SetName: true, Name: "Localized Name"}, effects)
	if err != nil || !changed || version != before.BotInfoVersion+2 {
		t.Fatalf("localized update = version:%d changed:%v err:%v", version, changed, err)
	}
	localized, found, err := bots.GetBotInfo(ctx, bot.ID, "zh-hans")
	if err != nil || !found || localized.Name != "Localized Name" || localized.About != defaults.About || localized.Description != defaults.Description {
		t.Fatalf("localized info = %+v found=%v err=%v", localized, found, err)
	}
	global, _, _ := bots.GetBotInfo(ctx, bot.ID, "")
	if global.Name != "Renamed" {
		t.Fatalf("localized write changed global name to %q", global.Name)
	}
	assertBotInfoDeliveryPayloadRows(t, ctx, pool, payloads[1], []int64{owner.ID, viewer.ID})

	version, changed, err = bots.UpdateBotInfoWithDelivery(ctx, bot.ID, domain.BotInfoUpdate{LangCode: "fr"}, effects)
	if err != nil || !changed || version != before.BotInfoVersion+3 {
		t.Fatalf("lang-only update = version:%d changed:%v err:%v", version, changed, err)
	}
	version, changed, err = bots.UpdateBotInfoWithDelivery(ctx, bot.ID, domain.BotInfoUpdate{LangCode: "fr"}, func(store.UserAudienceDeliverySnapshot) ([]store.DeliveryEffect, error) {
		return nil, errors.New("replay must not project")
	})
	if err != nil || changed || version != before.BotInfoVersion+3 {
		t.Fatalf("lang replay = version:%d changed:%v err:%v", version, changed, err)
	}

	projectionErr := errors.New("projection failed")
	if _, _, err := bots.UpdateBotInfoWithDelivery(ctx, bot.ID, domain.BotInfoUpdate{SetDescription: true, Description: "must roll back"}, func(store.UserAudienceDeliverySnapshot) ([]store.DeliveryEffect, error) {
		return nil, projectionErr
	}); !errors.Is(err, projectionErr) {
		t.Fatalf("projection error = %v", err)
	}
	afterFailure, _, _ := bots.GetBotInfo(ctx, bot.ID, "")
	userAfterFailure, _, _ := users.ByID(ctx, bot.ID)
	if afterFailure.Description != "default description" || userAfterFailure.BotInfoVersion != before.BotInfoVersion+3 {
		t.Fatalf("failed mutation leaked: info=%+v version=%d", afterFailure, userAfterFailure.BotInfoVersion)
	}
	if _, _, err := bots.UpdateBotInfoWithDelivery(ctx, bot.ID, domain.BotInfoUpdate{SetDescription: true, Description: "missing builder"}, nil); !errors.Is(err, store.ErrDeliveryOutboxRequired) {
		t.Fatalf("missing builder error = %v", err)
	}
	if builds != 3 {
		t.Fatalf("builder calls = %d, want three committed mutations", builds)
	}
}

func assertBotInfoDeliveryPayloadRows(t *testing.T, ctx context.Context, pool *pgxpool.Pool, payload []byte, wantTargets []int64) {
	t.Helper()
	var gotTargets []int64
	if err := pool.QueryRow(ctx, `
SELECT COALESCE(array_agg(target_user_id ORDER BY target_user_id), '{}'::bigint[])
FROM edge_delivery_outbox
WHERE payload = $1`, payload).Scan(&gotTargets); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(gotTargets) != fmt.Sprint(wantTargets) {
		t.Fatalf("delivery targets = %v, want %v", gotTargets, wantTargets)
	}
}
