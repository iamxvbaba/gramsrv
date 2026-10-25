package rpc

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap/zaptest"

	apppremium "telesrv/internal/app/premium"
	appusers "telesrv/internal/app/users"
	"telesrv/internal/domain"
	"telesrv/internal/store/postgres"
)

// testPremiumPostgresPool 连接 TELESRV_TEST_POSTGRES_DSN 指向的库（迁移到最新），未设则跳过。
// 与 internal/store/postgres.testPool 同一套门禁，但放在 rpc 包内，以便把真实
// PremiumStore 接进 Router 验证支付表单路径。
func testPremiumPostgresPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TELESRV_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set TELESRV_TEST_POSTGRES_DSN to run postgres integration test")
	}
	parsed, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse TELESRV_TEST_POSTGRES_DSN: %v", err)
	}
	if !strings.Contains(strings.ToLower(parsed.ConnConfig.Database), "test") {
		t.Fatalf("TELESRV_TEST_POSTGRES_DSN must name a dedicated test database, got %q", parsed.ConnConfig.Database)
	}
	if err := postgres.Migrate(dsn); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	pool, err := postgres.Open(context.Background(), dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// 被拒绝的 fiat Premium 结账返回 NOT_IMPLEMENTED，并且不得留下任何 durable 支付意图，
// 哪怕用户反复重试。早期版本先调用 IssuePaymentForm 写入 premium_payment_intents 再做
// 拒绝检查，于是每次对 fiat 发票点开支付表单都会落一条僵尸 intent。这里用真实
// postgres 存储驱动 Router，直接数意图表行数验证。
func TestPremiumDeniedFiatFormWritesNoPaymentIntentInPostgres(t *testing.T) {
	pool := testPremiumPostgresPool(t)
	ctx := context.Background()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano()%1_000_000)
	const months = 97
	var ids []int64
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM premium_audit_events
WHERE target_user_id=ANY($1::bigint[]) OR actor_user_id=ANY($1::bigint[])`, ids)
		_, _ = pool.Exec(ctx, `DELETE FROM premium_payment_intents
WHERE buyer_user_id=ANY($1::bigint[]) OR recipient_user_id=ANY($1::bigint[])`, ids)
		_, _ = pool.Exec(ctx, "DELETE FROM account_settings WHERE user_id=ANY($1::bigint[])", ids)
		_, _ = pool.Exec(ctx, "DELETE FROM peer_usernames WHERE peer_type='user' AND peer_id=ANY($1::bigint[])", ids)
		_, _ = pool.Exec(ctx, `DELETE FROM premium_plans WHERE months=$1`, months)
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id=ANY($1::bigint[])", ids)
	})

	users := postgres.NewUserStore(pool)
	buyer, err := users.Create(ctx, domain.User{
		AccessHash: 97101, Phone: "+1977" + suffix + "01", FirstName: "PremiumBuyer",
	})
	if err != nil {
		t.Fatal(err)
	}
	recipient, err := users.Create(ctx, domain.User{
		AccessHash: 97102, Phone: "+1977" + suffix + "02", FirstName: "PremiumRecipient",
	})
	if err != nil {
		t.Fatal(err)
	}
	ids = []int64{buyer.ID, recipient.ID}

	premiumStore := postgres.NewPremiumStore(pool, postgres.NewMessageStore(pool), domain.PremiumBotConfiguredUserID())
	if err := premiumStore.EnsurePremiumBotIdentity(ctx, "premiumbot"); err != nil {
		t.Fatalf("ensure Premium bot: %v", err)
	}
	// 走 admin upsert 而不是 SyncPlans：SyncPlans 会把共享 catalog 里不在列表内的
	// config 计划全部禁用，污染并行运行的其它 postgres 集成测试。
	if _, err := premiumStore.UpsertPremiumPlan(ctx, domain.PremiumPlanUpsertRequest{
		Months: months, DurationDays: 30, AmountStars: 300,
		FiatCurrency: "USD", FiatAmount: 750, Enabled: true,
		SortOrder: 5, Label: "integration month", ExpectedVersion: 0,
		ActorUserID: buyer.ID, Date: 1_800_000_000, Reason: "denied fiat form integration",
		CommandKey: fmt.Sprintf("premium-plan-denied-fiat-%d", months),
	}); err != nil {
		t.Fatalf("upsert plan: %v", err)
	}
	premiumSvc := apppremium.NewService(premiumStore, apppremium.Config{Username: "premiumbot"})
	router := New(Config{DevStarsPaymentsEnabled: false}, Deps{
		Users:   appusers.NewService(users),
		Premium: premiumSvc,
	}, zaptest.NewLogger(t), fixedClock{now: time.Unix(1_800_000_000, 0)})

	countIntents := func() int {
		var count int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM premium_payment_intents
WHERE buyer_user_id=$1 OR recipient_user_id=$1`, buyer.ID).Scan(&count); err != nil {
			t.Fatalf("count intents: %v", err)
		}
		return count
	}

	giftCodeInvoice := &tg.InputInvoicePremiumGiftCode{
		Purpose: &tg.InputStorePaymentPremiumGiftCode{
			Users:    []tg.InputUserClass{&tg.InputUser{UserID: recipient.ID, AccessHash: recipient.AccessHash}},
			Currency: "USD", Amount: 750,
		},
		Option: tg.PremiumGiftCodeOption{Users: 1, Months: months, Currency: "USD", Amount: 750},
	}
	for i := 0; i < 2; i++ {
		if _, err := router.premiumPaymentForm(ctx, buyer.ID, giftCodeInvoice); !tgerr.Is(err, "NOT_IMPLEMENTED") {
			t.Fatalf("denied fiat form attempt %d err=%v, want NOT_IMPLEMENTED", i+1, err)
		}
	}
	if count := countIntents(); count != 0 {
		t.Fatalf("denied fiat form left %d payment intents, want 0", count)
	}

	// 正向对照：受允许的 Stars gift 表单仍会写一条 intent，证明门禁只拦 fiat、
	// 而驱动整个 Router 的 store 本身是可写的。
	starsInvoice := &tg.InputInvoicePremiumGiftStars{
		UserID: &tg.InputUser{UserID: recipient.ID, AccessHash: recipient.AccessHash},
		Months: months,
	}
	starsForm, err := router.premiumPaymentForm(ctx, buyer.ID, starsInvoice)
	if err != nil {
		t.Fatalf("stars Premium payment form: %v", err)
	}
	if _, ok := starsForm.(*tg.PaymentsPaymentFormStars); !ok {
		t.Fatalf("stars Premium form = %T, want payments.paymentFormStars", starsForm)
	}
	if count := countIntents(); count != 1 {
		t.Fatalf("stars form left %d payment intents, want 1", count)
	}
}
