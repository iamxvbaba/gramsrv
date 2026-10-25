package postgres

import (
	"context"
	"errors"
	"sync"
	"testing"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

func postgresPremiumPurchaseEffects(authKeyID [8]byte, sessionID int64) store.DeliveryEffectsBuilder[domain.PremiumPurchaseResult] {
	return func(result domain.PremiumPurchaseResult) ([]store.DeliveryEffect, error) {
		return []store.DeliveryEffect{store.AbsoluteDeliveryEffect(store.DeliveryOutboxEnqueue{
			TargetUserID: result.User.ID, ExcludeAuthKeyID: authKeyID,
			ExcludeSessionID: sessionID, Payload: []byte{1},
			RecoveryPolicy: store.OutboxRecoveryAbsoluteReload,
		})}, nil
	}
}

func TestPremiumPlanAdminOverrideSurvivesConfigSync(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	premium := NewPremiumStore(pool, nil, domain.PremiumBotConfiguredUserID())
	const months = 119
	commandKeys := []string{
		"premium-plan-integration-create-119",
		"premium-plan-integration-update-119",
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM premium_audit_events
WHERE action IN ('plan_create','plan_update') AND command_key=ANY($1::text[])`, commandKeys)
		_, _ = pool.Exec(ctx, `DELETE FROM premium_plans WHERE months=$1`, months)
	})

	created, err := premium.UpsertPremiumPlan(ctx, domain.PremiumPlanUpsertRequest{
		Months: months, DurationDays: 365, AmountStars: 111, Enabled: true,
		SortOrder: 1190, Label: "Admin test plan", ExpectedVersion: 0,
		ActorUserID: 991, Date: 1_800_100_000, Reason: "integration create",
		CommandKey: commandKeys[0],
	})
	if err != nil {
		t.Fatalf("create admin plan: %v", err)
	}
	if created.Version != 1 || created.ManagedBy != domain.PremiumPlanManagedByAdmin {
		t.Fatalf("created plan = %+v", created)
	}

	updated, err := premium.UpsertPremiumPlan(ctx, domain.PremiumPlanUpsertRequest{
		Months: months, DurationDays: 365, AmountStars: 222, Enabled: true,
		SortOrder: 1190, Label: "Admin test plan", ExpectedVersion: created.Version,
		ActorUserID: 991, Date: 1_800_100_001, Reason: "integration update",
		CommandKey: commandKeys[1],
	})
	if err != nil {
		t.Fatalf("update admin plan: %v", err)
	}
	if updated.Version != 2 || updated.AmountStars != 222 {
		t.Fatalf("updated plan = %+v", updated)
	}
	if _, err := premium.UpsertPremiumPlan(ctx, domain.PremiumPlanUpsertRequest{
		Months: months, DurationDays: 365, AmountStars: 333, Enabled: true,
		SortOrder: 1190, Label: "stale", ExpectedVersion: created.Version,
		ActorUserID: 991, Date: 1_800_100_002, Reason: "stale update",
		CommandKey: "premium-plan-integration-stale-119",
	}); !errors.Is(err, domain.ErrPremiumPlanConflict) {
		t.Fatalf("stale update err=%v, want ErrPremiumPlanConflict", err)
	}

	if err := premium.SyncPlans(ctx, []domain.PremiumPlan{{
		Months: 3, DurationDays: 90, AmountStars: 750, Enabled: true,
		SortOrder: 10, Label: "3 months", Version: 1,
	}}); err != nil {
		t.Fatalf("config sync: %v", err)
	}
	persisted, found, err := premium.Plan(ctx, months)
	if err != nil || !found || persisted.AmountStars != 222 || !persisted.Enabled ||
		persisted.ManagedBy != domain.PremiumPlanManagedByAdmin {
		t.Fatalf("admin plan after config sync = %+v found=%v err=%v", persisted, found, err)
	}
	var auditCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM premium_audit_events
WHERE action IN ('plan_create','plan_update') AND command_key=ANY($1::text[])`,
		commandKeys).Scan(&auditCount); err != nil || auditCount != 2 {
		t.Fatalf("plan audit count=%d err=%v, want 2", auditCount, err)
	}
}

func TestPremiumPurchaseAtomicConcurrentIdempotentRefundAndExpiry(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	users := NewUserStore(pool)
	suffix := randomSuffix(t)
	buyer, err := users.Create(ctx, domain.User{
		AccessHash: 9101, Phone: "+1775" + suffix + "01", FirstName: "PremiumBuyer",
	})
	if err != nil {
		t.Fatal(err)
	}
	recipient, err := users.Create(ctx, domain.User{
		AccessHash: 9102, Phone: "+1775" + suffix + "02", FirstName: "PremiumRecipient",
	})
	if err != nil {
		t.Fatal(err)
	}
	ids := []int64{buyer.ID, recipient.ID}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DELETE FROM edge_delivery_outbox WHERE target_user_id=ANY($1::bigint[])", ids)
		_, _ = pool.Exec(ctx, `DELETE FROM premium_audit_events
WHERE target_user_id=ANY($1::bigint[]) OR actor_user_id=ANY($1::bigint[])`, ids)
		_, _ = pool.Exec(ctx, "DELETE FROM premium_entitlements WHERE user_id=ANY($1::bigint[]) OR source_user_id=ANY($1::bigint[])", ids)
		_, _ = pool.Exec(ctx, "DELETE FROM dispatch_outbox WHERE target_user_id=ANY($1::bigint[])", ids)
		_, _ = pool.Exec(ctx, "DELETE FROM user_update_events WHERE user_id=ANY($1::bigint[])", ids)
		_, _ = pool.Exec(ctx, "DELETE FROM message_boxes WHERE owner_user_id=ANY($1::bigint[])", ids)
		_, _ = pool.Exec(ctx, "DELETE FROM private_messages WHERE sender_user_id=ANY($1::bigint[]) OR recipient_user_id=ANY($1::bigint[])", ids)
		_, _ = pool.Exec(ctx, "DELETE FROM dialogs WHERE user_id=ANY($1::bigint[])", ids)
		_, _ = pool.Exec(ctx, "DELETE FROM stars_transactions WHERE user_id=ANY($1::bigint[])", ids)
		_, _ = pool.Exec(ctx, "DELETE FROM stars_balances WHERE user_id=ANY($1::bigint[])", ids)
		_, _ = pool.Exec(ctx, "DELETE FROM premium_payment_intents WHERE buyer_user_id=ANY($1::bigint[]) OR recipient_user_id=ANY($1::bigint[])", ids)
		_, _ = pool.Exec(ctx, "DELETE FROM account_settings WHERE user_id=ANY($1::bigint[])", ids)
		_, _ = pool.Exec(ctx, "DELETE FROM peer_usernames WHERE peer_type='user' AND peer_id=ANY($1::bigint[])", ids)
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id=ANY($1::bigint[])", ids)
	})

	stars := NewStarsStore(pool)
	if balance, applied, err := stars.EnsureGrant(ctx, buyer.ID, 5000, 1_800_000_000); err != nil ||
		!applied || balance.Balance != 5000 {
		t.Fatalf("fund buyer = %+v applied=%v err=%v", balance, applied, err)
	}
	messages := newTestMessageStore(pool, WithMessageAllocators(&perUserCounterAllocator{}))
	premium := NewPremiumStore(pool, messages, domain.PremiumBotConfiguredUserID())
	if err := premium.EnsurePremiumBotIdentity(ctx, "premiumbot"); err != nil {
		t.Fatalf("ensure Premium bot: %v", err)
	}
	if err := premium.EnsurePremiumBotIdentity(ctx, "premiumbot"); err != nil {
		t.Fatalf("idempotent Premium bot bootstrap: %v", err)
	}
	var premiumBotUsers, premiumBotUsernames int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM users WHERE id=$1`,
		domain.PremiumBotConfiguredUserID()).Scan(&premiumBotUsers); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM peer_usernames
WHERE username_lower='premiumbot' AND peer_type='user' AND peer_id=$1`,
		domain.PremiumBotConfiguredUserID()).Scan(&premiumBotUsernames); err != nil {
		t.Fatal(err)
	}
	if premiumBotUsers != 1 || premiumBotUsernames != 1 {
		t.Fatalf("Premium bot bootstrap users=%d usernames=%d, want 1/1",
			premiumBotUsers, premiumBotUsernames)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO peer_usernames
(username_lower,username,peer_type,peer_id,active,editable,sort_order)
VALUES('occupiedbot','occupiedbot','user',$1,true,true,0)`, buyer.ID); err != nil {
		t.Fatalf("seed occupied Premium username: %v", err)
	}
	if err := premium.EnsurePremiumBotIdentity(ctx, "occupiedbot"); !errors.Is(err, domain.ErrUsernameOccupied) {
		t.Fatalf("occupied Premium username err=%v, want ErrUsernameOccupied", err)
	}
	plan := domain.PremiumPlan{
		Months: 3, DurationDays: 90, AmountStars: 750, Enabled: true,
		SortOrder: 10, Label: "3 months", Version: 1,
	}
	if err := premium.SyncPlans(ctx, []domain.PremiumPlan{plan}); err != nil {
		t.Fatalf("sync plan: %v", err)
	}
	now := 1_800_000_100
	formRequest := domain.PremiumPaymentForm{
		IdempotencyKey: "premium-integration-form",
		BuyerUserID:    buyer.ID, Kind: domain.PremiumPurchaseGift, RecipientUserID: recipient.ID,
		Months: plan.Months, DurationDays: plan.DurationDays, AmountStars: plan.AmountStars,
		PlanVersion: plan.Version,
		Message: domain.PremiumGiftMessage{
			Text:     "Enjoy 😀",
			Entities: []domain.MessageEntity{{Type: domain.MessageEntityBold, Offset: 6, Length: 2}},
		},
		IssuedAt: now, ExpiresAt: now + domain.PremiumPaymentFormTTLSeconds,
	}
	form, err := premium.IssuePremiumPaymentForm(ctx, formRequest)
	if err != nil {
		t.Fatalf("issue form: %v", err)
	}
	replayedForm, err := premium.IssuePremiumPaymentForm(ctx, formRequest)
	if err != nil || replayedForm.ID != form.ID {
		t.Fatalf("idempotent form = %+v err=%v, want form_id %d", replayedForm, err, form.ID)
	}
	expiredRequest := formRequest
	expiredRequest.IdempotencyKey = "premium-integration-expired-form"
	expiredRequest.IssuedAt = now - domain.PremiumPaymentFormTTLSeconds - 1
	expiredRequest.ExpiresAt = expiredRequest.IssuedAt + domain.PremiumPaymentFormTTLSeconds
	expiredForm, err := premium.IssuePremiumPaymentForm(ctx, expiredRequest)
	if err != nil {
		t.Fatalf("issue expired-window form: %v", err)
	}
	expiredRequest.IssuedAt = now
	expiredRequest.ExpiresAt = now + domain.PremiumPaymentFormTTLSeconds
	renewedForm, err := premium.IssuePremiumPaymentForm(ctx, expiredRequest)
	if err != nil || renewedForm.ID == expiredForm.ID || renewedForm.ExpiresAt != expiredRequest.ExpiresAt {
		t.Fatalf("renewed form=%+v err=%v, old form_id=%d", renewedForm, err, expiredForm.ID)
	}
	request := domain.PremiumPurchaseRequest{
		BuyerUserID: buyer.ID, FormID: form.ID, Kind: form.Kind,
		RecipientUserID: recipient.ID, Months: form.Months, PlanVersion: form.PlanVersion,
		Message: form.Message, Date: now + 1, CommandKey: "premium-integration-concurrent",
	}
	purchaseAuthKeyID := [8]byte{9, 1, 0, 1}
	request.OriginAuthKeyID = purchaseAuthKeyID
	request.OriginSessionID = 91
	wantBuildErr := errors.New("encode premium update failed")
	if _, err := premium.PurchasePremiumWithDelivery(ctx, request, func(domain.PremiumPurchaseResult) ([]store.DeliveryEffect, error) {
		return nil, wantBuildErr
	}); !errors.Is(err, wantBuildErr) {
		t.Fatalf("failed purchase delivery err=%v, want %v", err, wantBuildErr)
	}
	failedBalance, err := stars.GetBalance(ctx, buyer.ID)
	if err != nil || failedBalance.Balance != 5000 {
		t.Fatalf("balance after failed delivery = %+v err=%v, want 5000", failedBalance, err)
	}
	failedRecipient, recipientFound, err := users.ByID(ctx, recipient.ID)
	if err != nil || !recipientFound || failedRecipient.PremiumUntil != 0 {
		t.Fatalf("recipient after failed delivery = %+v found=%v err=%v", failedRecipient, recipientFound, err)
	}
	var failedIntentStatus string
	if err := pool.QueryRow(ctx, `SELECT status FROM premium_payment_intents WHERE form_id=$1`, form.ID).Scan(&failedIntentStatus); err != nil || failedIntentStatus != string(domain.PremiumPaymentPending) {
		t.Fatalf("intent after failed delivery status=%q err=%v, want pending", failedIntentStatus, err)
	}
	var failedEntitlements, failedDebits, failedMessages, failedDeliveries int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM premium_entitlements WHERE user_id=$1`, recipient.ID).Scan(&failedEntitlements); err != nil || failedEntitlements != 0 {
		t.Fatalf("entitlements after failed delivery=%d err=%v, want 0", failedEntitlements, err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM stars_transactions WHERE premium_payment_intent_id=(SELECT id FROM premium_payment_intents WHERE form_id=$1)`, form.ID).Scan(&failedDebits); err != nil || failedDebits != 0 {
		t.Fatalf("debits after failed delivery=%d err=%v, want 0", failedDebits, err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM private_messages WHERE sender_user_id=$1 AND recipient_user_id=$2`, buyer.ID, recipient.ID).Scan(&failedMessages); err != nil || failedMessages != 0 {
		t.Fatalf("messages after failed delivery=%d err=%v, want 0", failedMessages, err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM edge_delivery_outbox WHERE target_user_id=$1`, recipient.ID).Scan(&failedDeliveries); err != nil || failedDeliveries != 0 {
		t.Fatalf("deliveries after failed purchase=%d err=%v, want 0", failedDeliveries, err)
	}

	results := make([]domain.PremiumPurchaseResult, 2)
	errs := make([]error, 2)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range results {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			<-start
			results[index], errs[index] = premium.PurchasePremiumWithDelivery(ctx, request, postgresPremiumPurchaseEffects(purchaseAuthKeyID, 91))
		}(i)
	}
	close(start)
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("concurrent purchase %d: %v", i, err)
		}
	}
	if results[0].Duplicate == results[1].Duplicate {
		t.Fatalf("duplicate flags = %v/%v, want exactly one replay", results[0].Duplicate, results[1].Duplicate)
	}
	replay, err := premium.PurchasePremiumWithDelivery(ctx, request, func(domain.PremiumPurchaseResult) ([]store.DeliveryEffect, error) {
		t.Fatal("idempotent premium replay invoked delivery builder")
		return nil, nil
	})
	if err != nil || !replay.Duplicate {
		t.Fatalf("sequential premium replay = %+v err=%v, want duplicate", replay, err)
	}
	var purchaseDeliveries int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM edge_delivery_outbox WHERE target_user_id=$1`, recipient.ID).Scan(&purchaseDeliveries); err != nil || purchaseDeliveries != 1 {
		t.Fatalf("purchase deliveries after replays=%d err=%v, want 1", purchaseDeliveries, err)
	}
	var storedAuthKey []byte
	var storedSession int64
	if err := pool.QueryRow(ctx, `SELECT exclude_auth_key_id,exclude_session_id FROM edge_delivery_outbox WHERE target_user_id=$1`, recipient.ID).Scan(&storedAuthKey, &storedSession); err != nil {
		t.Fatalf("load premium purchase exclusion: %v", err)
	}
	if string(storedAuthKey) != string(purchaseAuthKeyID[:]) || storedSession != 91 {
		t.Fatalf("premium purchase exclusion=%x/%d, want %x/91", storedAuthKey, storedSession, purchaseAuthKeyID)
	}
	if _, err := premium.IssuePremiumPaymentForm(ctx, formRequest); !errors.Is(err, domain.ErrPremiumInvoiceAlreadyPaid) {
		t.Fatalf("paid invoice reissue err=%v, want ErrPremiumInvoiceAlreadyPaid", err)
	}
	balance, err := stars.GetBalance(ctx, buyer.ID)
	if err != nil || balance.Balance != 4250 {
		t.Fatalf("balance after concurrent replay = %+v err=%v, want 4250", balance, err)
	}
	entitlements, err := premium.ActivePremiumEntitlements(ctx, recipient.ID, now)
	if err != nil || len(entitlements) != 1 {
		t.Fatalf("active entitlements = %+v err=%v", entitlements, err)
	}
	if entitlements[0].ExpiresAt != now+1+90*24*60*60 ||
		entitlements[0].TransactionID == 0 {
		t.Fatalf("entitlement = %+v", entitlements[0])
	}
	allEntitlements, err := premium.PremiumEntitlements(ctx, recipient.ID, 10)
	if err != nil || len(allEntitlements) != 1 || allEntitlements[0].ID != entitlements[0].ID {
		t.Fatalf("operator entitlements = %+v err=%v", allEntitlements, err)
	}
	payment, found, err := premium.PremiumPayment(ctx, entitlements[0].PaymentIntentID)
	if err != nil || !found || payment.Intent.Status != domain.PremiumPaymentPaid ||
		payment.Transaction.ID != entitlements[0].TransactionID ||
		payment.Entitlement.ID != entitlements[0].ID {
		t.Fatalf("operator payment = %+v found=%v err=%v", payment, found, err)
	}
	var debitCount, messageCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM stars_transactions
WHERE premium_payment_intent_id=$1 AND amount=-750`, entitlements[0].PaymentIntentID).Scan(&debitCount); err != nil || debitCount != 1 {
		t.Fatalf("premium debit count = %d err=%v", debitCount, err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM private_messages
WHERE sender_user_id=$1 AND recipient_user_id=$2`, buyer.ID, recipient.ID).Scan(&messageCount); err != nil || messageCount != 1 {
		t.Fatalf("gift service message count = %d err=%v", messageCount, err)
	}
	var actionKind, actionCurrency, actionText string
	var actionAmount int64
	var actionDays int
	if err := pool.QueryRow(ctx, `SELECT
media #>> '{service_action,kind}',
media #>> '{service_action,gift_premium,currency}',
(media #>> '{service_action,gift_premium,amount}')::bigint,
(media #>> '{service_action,gift_premium,days}')::integer,
media #>> '{service_action,gift_premium,message,text}'
FROM private_messages
WHERE sender_user_id=$1 AND recipient_user_id=$2`, buyer.ID, recipient.ID).
		Scan(&actionKind, &actionCurrency, &actionAmount, &actionDays, &actionText); err != nil {
		t.Fatalf("load stored Premium service action: %v", err)
	}
	if actionKind != string(domain.MessageServiceActionGiftPremium) ||
		actionCurrency != domain.PremiumCurrencyStars || actionAmount != plan.AmountStars ||
		actionDays != plan.DurationDays || actionText != form.Message.Text {
		t.Fatalf("stored Premium action kind=%q currency=%q amount=%d days=%d text=%q",
			actionKind, actionCurrency, actionAmount, actionDays, actionText)
	}
	recipientRow, found, err := users.ByID(ctx, recipient.ID)
	if err != nil || !found || !recipientRow.PremiumActiveAt(int64(now+1)) {
		t.Fatalf("recipient Premium = %+v found=%v err=%v", recipientRow, found, err)
	}
	var premiumUpdatedAt bool
	if err := pool.QueryRow(ctx, `SELECT premium_updated_at IS NOT NULL FROM users WHERE id=$1`,
		recipient.ID).Scan(&premiumUpdatedAt); err != nil || !premiumUpdatedAt {
		t.Fatalf("recipient premium_updated_at set=%v err=%v", premiumUpdatedAt, err)
	}

	// A second gift while the recipient is still active must be rejected
	// outright -- Premium rewarded gift windows never stack
	// (PremiumSubscriptionActiveError). Neither the balance, the ledger, the
	// entitlement set nor the message stream may change.
	activeForm, err := premium.IssuePremiumPaymentForm(ctx, domain.PremiumPaymentForm{
		IdempotencyKey:  "premium-integration-extension-form",
		BuyerUserID:     buyer.ID,
		Kind:            domain.PremiumPurchaseGift,
		RecipientUserID: recipient.ID,
		Months:          plan.Months,
		DurationDays:    plan.DurationDays,
		AmountStars:     plan.AmountStars,
		PlanVersion:     plan.Version,
		IssuedAt:        now + 3,
		ExpiresAt:       now + 3 + domain.PremiumPaymentFormTTLSeconds,
	})
	if err != nil {
		t.Fatalf("issue active-extension form: %v", err)
	}
	var activeErr domain.PremiumSubscriptionActiveError
	if _, err := premium.PurchasePremiumWithDelivery(ctx, domain.PremiumPurchaseRequest{
		BuyerUserID: buyer.ID, FormID: activeForm.ID, Kind: domain.PremiumPurchaseGift,
		RecipientUserID: recipient.ID, Months: plan.Months, PlanVersion: plan.Version,
		Date: now + 3, CommandKey: "premium-integration-extension-purchase",
	}, postgresPremiumPurchaseEffects([8]byte{}, 0)); !errors.As(err, &activeErr) {
		t.Fatalf("active gift err=%v, want PremiumSubscriptionActiveError", err)
	}
	if balance, err := stars.GetBalance(ctx, buyer.ID); err != nil || balance.Balance != 4250 {
		t.Fatalf("balance changed by rejected extension=%+v err=%v, want 4250", balance, err)
	}
	activeSet, err := premium.ActivePremiumEntitlements(ctx, recipient.ID, now+3)
	if err != nil || len(activeSet) != 1 {
		t.Fatalf("entitlements after rejected extension=%+v err=%v, want the original one", activeSet, err)
	}
	var rejectedTxn, rejectedMessage int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM stars_transactions WHERE premium_payment_intent_id=$1`,
		activeForm.ID).Scan(&rejectedTxn); err != nil || rejectedTxn != 0 {
		t.Fatalf("rejected extension ledger=%d err=%v, want 0", rejectedTxn, err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM private_messages
WHERE sender_user_id=$1 AND recipient_user_id=$2`, buyer.ID, recipient.ID).
		Scan(&rejectedMessage); err != nil || rejectedMessage != 1 {
		t.Fatalf("rejected extension messages=%d err=%v, want the original gift only", rejectedMessage, err)
	}

	refund := domain.PremiumRefundRequest{
		PaymentIntentID: entitlements[0].PaymentIntentID,
		ActorUserID:     991, Date: now + 4, Reason: "integration refund",
		CommandKey: "premium-integration-refund",
	}
	firstRefund, err := premium.RefundPremiumPayment(ctx, refund)
	if err != nil || firstRefund.Duplicate {
		t.Fatalf("first refund = %+v err=%v", firstRefund, err)
	}
	replayRefund, err := premium.RefundPremiumPayment(ctx, refund)
	if err != nil || !replayRefund.Duplicate {
		t.Fatalf("refund replay = %+v err=%v", replayRefund, err)
	}
	if balance, err := stars.GetBalance(ctx, buyer.ID); err != nil || balance.Balance != 5000 {
		t.Fatalf("balance after refund = %+v err=%v", balance, err)
	}
	var refundCredits int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM stars_transactions
WHERE user_id=$1 AND amount=$2 AND reason=$3 AND premium_recipient_user_id=$4 AND premium_months=$5`,
		buyer.ID, plan.AmountStars, string(domain.StarsReasonPremium), recipient.ID, plan.Months).
		Scan(&refundCredits); err != nil || refundCredits != 1 {
		t.Fatalf("Premium refund ledger credits=%d err=%v, want 1", refundCredits, err)
	}
	if _, err := premium.RefundPremiumPayment(ctx, domain.PremiumRefundRequest{
		PaymentIntentID: refund.PaymentIntentID, ActorUserID: refund.ActorUserID,
		Date: refund.Date, Reason: refund.Reason, CommandKey: "different-refund-command",
	}); !errors.Is(err, domain.ErrPremiumAlreadyRefunded) {
		t.Fatalf("second refund under another command err=%v, want ErrPremiumAlreadyRefunded", err)
	}

	if _, err := pool.Exec(ctx, `INSERT INTO account_settings(user_id,disallow_premium_gifts)
VALUES($1,true) ON CONFLICT(user_id) DO UPDATE SET disallow_premium_gifts=true`, recipient.ID); err != nil {
		t.Fatalf("set recipient Premium gift privacy: %v", err)
	}
	restrictedForm, err := premium.IssuePremiumPaymentForm(ctx, domain.PremiumPaymentForm{
		IdempotencyKey:  "premium-integration-restricted-form",
		BuyerUserID:     buyer.ID,
		Kind:            domain.PremiumPurchaseGift,
		RecipientUserID: recipient.ID,
		Months:          plan.Months,
		DurationDays:    plan.DurationDays,
		AmountStars:     plan.AmountStars,
		PlanVersion:     plan.Version,
		IssuedAt:        now + 5,
		ExpiresAt:       now + 5 + domain.PremiumPaymentFormTTLSeconds,
	})
	if err != nil {
		t.Fatalf("issue privacy-restricted form: %v", err)
	}
	if _, err := premium.PurchasePremiumWithDelivery(ctx, domain.PremiumPurchaseRequest{
		BuyerUserID: buyer.ID, FormID: restrictedForm.ID, Kind: domain.PremiumPurchaseGift,
		RecipientUserID: recipient.ID, Months: plan.Months, PlanVersion: plan.Version,
		Date: now + 6, CommandKey: "premium-integration-restricted-purchase",
	}, postgresPremiumPurchaseEffects([8]byte{}, 0)); !errors.Is(err, domain.ErrPremiumRecipientRestricted) {
		t.Fatalf("privacy-restricted purchase err=%v", err)
	}
	if balance, err := stars.GetBalance(ctx, buyer.ID); err != nil || balance.Balance != 5000 {
		t.Fatalf("balance changed by privacy-restricted purchase=%+v err=%v", balance, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE account_settings SET disallow_premium_gifts=false WHERE user_id=$1`,
		recipient.ID); err != nil {
		t.Fatalf("clear recipient Premium gift privacy: %v", err)
	}

	selfForm, err := premium.IssuePremiumPaymentForm(ctx, domain.PremiumPaymentForm{
		IdempotencyKey:  "premium-integration-self-form",
		BuyerUserID:     buyer.ID,
		Kind:            domain.PremiumPurchaseSelf,
		RecipientUserID: buyer.ID,
		Months:          plan.Months,
		DurationDays:    plan.DurationDays,
		AmountStars:     plan.AmountStars,
		PlanVersion:     plan.Version,
		IssuedAt:        now + 5,
		ExpiresAt:       now + 5 + domain.PremiumPaymentFormTTLSeconds,
	})
	if err != nil {
		t.Fatalf("issue self form: %v", err)
	}
	selfPurchase, err := premium.PurchasePremiumWithDelivery(ctx, domain.PremiumPurchaseRequest{
		BuyerUserID: buyer.ID, FormID: selfForm.ID, Kind: domain.PremiumPurchaseSelf,
		RecipientUserID: buyer.ID, Months: plan.Months, PlanVersion: plan.Version,
		Date: now + 6, CommandKey: "premium-integration-self-purchase",
	}, postgresPremiumPurchaseEffects([8]byte{}, 0))
	if err != nil || selfPurchase.Duplicate || !selfPurchase.User.PremiumActiveAt(int64(now+6)) ||
		selfPurchase.Balance.Balance != 4250 {
		t.Fatalf("self purchase = %+v err=%v", selfPurchase, err)
	}
	var selfConfirmationCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM private_messages
WHERE sender_user_id=$1 AND recipient_user_id=$2`,
		domain.PremiumBotConfiguredUserID(), buyer.ID).Scan(&selfConfirmationCount); err != nil ||
		selfConfirmationCount != 1 {
		t.Fatalf("self purchase confirmation count=%d err=%v", selfConfirmationCount, err)
	}

	grant, grantedUser, err := premium.GrantPremiumEntitlement(ctx, domain.PremiumAdminGrantRequest{
		UserID: recipient.ID, ActorUserID: 992, Months: 1, DurationDays: 1,
		Date: now + 7, Reason: "expiry test", CommandKey: "premium-expiry-grant",
	})
	if err != nil || !grantedUser.PremiumActiveAt(int64(now+7)) {
		t.Fatalf("admin grant = %+v user=%+v err=%v", grant, grantedUser, err)
	}
	wantErr := errors.New("encode expiry failed")
	if _, err := premium.SweepPremiumEntitlementsWithDelivery(ctx, grant.ExpiresAt, 10, func([]domain.User) ([]store.DeliveryEffect, error) {
		return nil, wantErr
	}); !errors.Is(err, wantErr) {
		t.Fatalf("failed expiry sweep error = %v", err)
	}
	if current, found, err := users.ByID(ctx, recipient.ID); err != nil || !found || current.PremiumUntil == 0 {
		t.Fatalf("failed expiry sweep changed user=%+v found=%v err=%v", current, found, err)
	}
	expiredUsers, err := premium.SweepPremiumEntitlementsWithDelivery(ctx, grant.ExpiresAt, 10, func(changed []domain.User) ([]store.DeliveryEffect, error) {
		effects := make([]store.DeliveryEffect, 0, len(changed))
		for _, user := range changed {
			effects = append(effects, store.AbsoluteDeliveryEffect(store.DeliveryOutboxEnqueue{
				TargetUserID: user.ID, Payload: []byte{2}, RecoveryPolicy: store.OutboxRecoveryAbsoluteReload,
			}))
		}
		return effects, nil
	})
	if err != nil {
		t.Fatalf("expiry sweep = %+v err=%v", expiredUsers, err)
	}
	expiredByID := make(map[int64]domain.User, len(expiredUsers))
	for _, expiredUser := range expiredUsers {
		expiredByID[expiredUser.ID] = expiredUser
		if expiredUser.PremiumUntil != 0 {
			t.Fatalf("expiry sweep left Premium active: %+v", expiredUser)
		}
	}
	if _, found := expiredByID[recipient.ID]; !found {
		t.Fatalf("expiry sweep did not report recipient: %+v", expiredUsers)
	}
	var expiryDeliveries int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM edge_delivery_outbox WHERE target_user_id=$1 AND payload=$2", recipient.ID, []byte{2}).Scan(&expiryDeliveries); err != nil || expiryDeliveries != 1 {
		t.Fatalf("premium expiry deliveries=%d err=%v, want 1", expiryDeliveries, err)
	}
}
