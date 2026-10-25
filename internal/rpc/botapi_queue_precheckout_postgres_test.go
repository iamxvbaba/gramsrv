package rpc

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/iamxvbaba/td/clock"
	"go.uber.org/zap/zaptest"

	appbots "telesrv/internal/app/bots"
	"telesrv/internal/domain"
	"telesrv/internal/store/postgres"
)

// The queue is postgres in production and in-memory everywhere else, so the
// enqueue-to-event path has to be proven against the real store. A drop here is
// silent: the row exists, the log says queued, and getUpdates never shows the bot
// anything, which looks exactly like a bot that ignored the question.
func TestBotAPIUpdatesDeliversPreCheckoutPostgres(t *testing.T) {
	dsn := os.Getenv("TELESRV_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set TELESRV_TEST_POSTGRES_DSN to run")
	}
	ctx := context.Background()
	if err := postgres.Migrate(dsn); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	pool, err := postgres.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(pool.Close)

	// Unique per run: the test database is shared and phones are unique per user.
	suffix := int64(time.Now().UnixNano()%100000000) + 1
	users := postgres.NewUserStore(pool)
	botStore := postgres.NewBotStore(pool)
	bots := appbots.NewService(users, botStore, nil)
	updates := postgres.NewBotAPIUpdateStore(pool)

	botUser, err := users.Create(ctx, domain.User{
		AccessHash: 9601 + suffix, Phone: fmt.Sprintf("+1960%08d01", suffix), FirstName: "QueuePreCheckoutBot",
	})
	if err != nil {
		t.Fatalf("create bot user: %v", err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO bots (bot_user_id, owner_user_id, token_secret)
VALUES ($1, $1, 'queue-precheckout-secret')
ON CONFLICT (bot_user_id) DO NOTHING`, botUser.ID); err != nil {
		t.Fatalf("seed bot: %v", err)
	}
	payer, err := users.Create(ctx, domain.User{
		AccessHash: 9602 + suffix, Phone: fmt.Sprintf("+1960%08d02", suffix), FirstName: "QueuePreCheckoutPayer",
	})
	if err != nil {
		t.Fatalf("create payer: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DELETE FROM bot_api_updates WHERE bot_user_id = $1", botUser.ID)
		_, _ = pool.Exec(ctx, "DELETE FROM bots WHERE bot_user_id = $1", botUser.ID)
	})

	r := New(Config{}, Deps{
		Bots:          bots,
		BotAPIUpdates: updates,
	}, zaptest.NewLogger(t), clock.System)

	_, created, err := updates.EnqueueBotAPIUpdate(ctx, domain.EnqueueBotAPIUpdateRequest{
		BotUserID: botUser.ID,
		Kind:      domain.BotAPIUpdatePreCheckoutQuery,
		Date:      int(r.clock.Now().Unix()),
		PreCheckout: &domain.BotPreCheckoutQuery{
			ID: 7001, BotUserID: botUser.ID, UserID: payer.ID,
			Currency: "XTR", TotalAmount: 100, Payload: "queue-test",
		},
	})
	if err != nil || !created {
		t.Fatalf("enqueue = created %v, err %v", created, err)
	}

	events, err := r.BotAPIUpdates(ctx, botUser.ID, 0)
	if err != nil {
		t.Fatalf("BotAPIUpdates: %v", err)
	}
	for _, event := range events {
		if event.Type != domain.UpdateEventBotPreCheckoutQuery {
			continue
		}
		if event.BotPreCheckout == nil {
			t.Fatal("the event survived without its payload")
		}
		query := *event.BotPreCheckout
		if query.ID != 7001 || query.UserID != payer.ID || query.TotalAmount != 100 || query.Payload != "queue-test" {
			t.Fatalf("payload = %+v, want the enqueued question", query)
		}
		return
	}
	t.Fatalf("no pre-checkout event reached getUpdates; %d events came back: %+v", len(events), eventTypes(events))
}

func eventTypes(events []domain.UpdateEvent) []domain.UpdateEventType {
	out := make([]domain.UpdateEventType, 0, len(events))
	for _, event := range events {
		out = append(out, event.Type)
	}
	return out
}
