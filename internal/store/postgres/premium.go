package postgres

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"telesrv/internal/domain"
	"telesrv/internal/store/postgres/sqlcgen"
)

var errPremiumAlreadyPaid = errors.New("premium: already paid")

// premiumPlanCatalogLockID serializes config sync and operator writes. Row
// locks alone cannot protect the "at least one enabled plan" invariant when
// two transactions disable different rows at the same time.
const premiumPlanCatalogLockID int64 = 0x7072656d706c616e

type PremiumStore struct {
	db       sqlcgen.DBTX
	messages *MessageStore
	botID    int64
}

func NewPremiumStore(db sqlcgen.DBTX, messages *MessageStore, botID int64) *PremiumStore {
	if botID <= 0 {
		botID = domain.PremiumBotConfiguredUserID()
	}
	return &PremiumStore{db: db, messages: messages, botID: botID}
}

// EnsurePremiumBotIdentity applies the configured username without ever
// stealing a registry entry owned by another peer.
func (s *PremiumStore) EnsurePremiumBotIdentity(ctx context.Context, username string) error {
	username = strings.TrimPrefix(strings.TrimSpace(username), "@")
	if s == nil || s.db == nil || !domain.ValidBotUsername(username) {
		return domain.ErrBotUsernameInvalid
	}
	return withTx(ctx, s.db, "configure premium bot identity", func(tx pgx.Tx) error {
		// Migration 0163 seeds the safe default identity. A deployment that
		// deliberately selects another reserved ID migrates that bootstrap
		// identity here; it is never left as a second public @premiumbot.
		if s.botID != domain.PremiumBotUserID {
			var defaultHash int64
			var defaultBot bool
			err := tx.QueryRow(ctx, `SELECT access_hash,is_bot FROM users WHERE id=$1 FOR UPDATE`,
				domain.PremiumBotUserID).Scan(&defaultHash, &defaultBot)
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return fmt.Errorf("lock default premium bot: %w", err)
			}
			if err == nil && defaultBot && defaultHash == domain.PremiumBotAccessHash {
				if _, err := tx.Exec(ctx, `DELETE FROM peer_usernames
WHERE peer_type='user' AND peer_id=$1 AND collectible_id IS NULL`,
					domain.PremiumBotUserID); err != nil {
					return fmt.Errorf("retire default premium bot username: %w", err)
				}
				if _, err := tx.Exec(ctx, `UPDATE users SET username='',updated_at=now() WHERE id=$1`,
					domain.PremiumBotUserID); err != nil {
					return fmt.Errorf("retire default premium bot user: %w", err)
				}
			}
		}

		var peerType string
		var peerID int64
		err := tx.QueryRow(ctx, `SELECT peer_type,peer_id
FROM peer_usernames WHERE username_lower=lower($1) FOR UPDATE`, username).
			Scan(&peerType, &peerID)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("check premium bot username: %w", err)
		}
		if err == nil && (peerType != string(domain.PeerTypeUser) || peerID != s.botID) {
			return domain.ErrUsernameOccupied
		}

		var existingHash int64
		var existingBot bool
		err = tx.QueryRow(ctx, `SELECT access_hash,is_bot FROM users WHERE id=$1 FOR UPDATE`, s.botID).
			Scan(&existingHash, &existingBot)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			_, err = tx.Exec(ctx, `INSERT INTO users (
id,access_hash,phone,first_name,last_name,username,country_code,created_at,updated_at,
verified,support,about,last_seen_at,default_history_ttl_period,is_bot,bot_info_version,
premium_expires_at,emoji_status_document_id,emoji_status_until,color_set,color,
color_background_emoji_id,profile_color_set,profile_color,profile_color_background_emoji_id
) VALUES (
$1,$2,'','Premium Bot','',$3,'',now(),now(),true,false,
'Покупка Premium для себя или в подарок за Telegram Stars.',
0,0,true,1,NULL,0,0,false,0,0,false,0,0
)`, s.botID, domain.PremiumBotAccessHash, username)
			if err != nil {
				return fmt.Errorf("create premium bot user: %w", err)
			}
		case err != nil:
			return fmt.Errorf("lock premium bot user: %w", err)
		case !existingBot || existingHash != domain.PremiumBotAccessHash:
			return fmt.Errorf("premium bot user id %d is already occupied", s.botID)
		}

		tag, err := tx.Exec(ctx, `UPDATE users
SET access_hash=$2,first_name='Premium Bot',username=$3,verified=true,is_bot=true,
about='Покупка Premium для себя или в подарок за Telegram Stars.',
bot_info_version=GREATEST(bot_info_version,1),updated_at=now()
WHERE id=$1 AND is_bot`, s.botID, domain.PremiumBotAccessHash, username)
		if err != nil {
			return fmt.Errorf("update premium bot username: %w", err)
		}
		if tag.RowsAffected() != 1 {
			return fmt.Errorf("premium bot user %d is missing", s.botID)
		}
		if _, err := tx.Exec(ctx, `DELETE FROM peer_usernames
WHERE peer_type='user' AND peer_id=$1 AND collectible_id IS NULL`, s.botID); err != nil {
			return fmt.Errorf("clear premium bot usernames: %w", err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO peer_usernames
(username_lower,username,peer_type,peer_id,active,editable,sort_order,updated_at)
VALUES(lower($1),$1,'user',$2,true,false,0,now())`, username, s.botID); err != nil {
			if isUniqueViolation(err) {
				return domain.ErrUsernameOccupied
			}
			return fmt.Errorf("reserve premium bot username: %w", err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO bots (
bot_user_id,owner_user_id,token_secret,description,commands,bot_chat_history,bot_nochats,
inline_placeholder,created_at,updated_at,menu_button_type,menu_button_text,menu_button_url,bot_inline_geo
) VALUES (
$1,$1,'','Покупка Premium для себя или в подарок. Все цены указаны в Telegram Stars.',
$2::jsonb,false,true,'',now(),now(),0,'','',false
) ON CONFLICT(bot_user_id) DO UPDATE SET owner_user_id=EXCLUDED.owner_user_id,
description=EXCLUDED.description,commands=EXCLUDED.commands,updated_at=now()`,
			s.botID, premiumBotCommandsJSON); err != nil {
			return fmt.Errorf("upsert premium bot profile: %w", err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO read_model_versions
(model,owner_user_id,peer_type,peer_id,version,updated_at,hash) VALUES
('contact_account',$1,'user',$1,1,now(),$2),
('channel_active_memberships',$1,'user',$1,1,now(),$3)
ON CONFLICT(model,owner_user_id,peer_type,peer_id) DO UPDATE SET
version=GREATEST(read_model_versions.version,EXCLUDED.version),updated_at=now(),hash=EXCLUDED.hash`,
			s.botID, domain.PremiumBotAccessHash, domain.PremiumBotAccessHash^1); err != nil {
			return fmt.Errorf("upsert premium bot read models: %w", err)
		}
		return nil
	})
}

const premiumBotCommandsJSON = `[
{"command":"start","description":"открыть магазин Premium"},
{"command":"premium","description":"купить Premium для себя"},
{"command":"gift","description":"подарить Premium пользователю"},
{"command":"status","description":"проверить статус Premium"},
{"command":"history","description":"показать историю покупок"},
{"command":"terms","description":"показать условия покупки"},
{"command":"help","description":"показать справку"}
]`

func normalizedPremiumPlan(plan domain.PremiumPlan) domain.PremiumPlan {
	plan.FiatCurrency = plan.EffectiveFiatCurrency()
	plan.FiatAmount = plan.EffectiveFiatAmount()
	plan.StoreProduct = strings.TrimSpace(plan.StoreProduct)
	if plan.StoreProduct == "" {
		plan.StoreQuantity = 0
	}
	return plan
}

func (s *PremiumStore) Plans(ctx context.Context) ([]domain.PremiumPlan, error) {
	if s == nil || s.db == nil {
		return nil, nil
	}
	rows, err := s.db.Query(ctx, `SELECT months,duration_days,amount_stars,fiat_currency,fiat_amount,store_product,store_quantity,
enabled,sort_order,label,managed_by,version,
EXTRACT(EPOCH FROM updated_at)::bigint
FROM premium_plans ORDER BY sort_order,months`)
	if err != nil {
		return nil, fmt.Errorf("list premium plans: %w", err)
	}
	defer rows.Close()
	out := make([]domain.PremiumPlan, 0)
	for rows.Next() {
		var plan domain.PremiumPlan
		var updated int64
		if err := rows.Scan(&plan.Months, &plan.DurationDays, &plan.AmountStars, &plan.FiatCurrency,
			&plan.FiatAmount, &plan.StoreProduct, &plan.StoreQuantity, &plan.Enabled,
			&plan.SortOrder, &plan.Label, &plan.ManagedBy, &plan.Version, &updated); err != nil {
			return nil, fmt.Errorf("scan premium plan: %w", err)
		}
		plan.UpdatedAt = int(updated)
		out = append(out, plan)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate premium plans: %w", err)
	}
	return out, nil
}

func (s *PremiumStore) Plan(ctx context.Context, months int) (domain.PremiumPlan, bool, error) {
	if s == nil || s.db == nil || months <= 0 {
		return domain.PremiumPlan{}, false, nil
	}
	var plan domain.PremiumPlan
	var updated int64
	err := s.db.QueryRow(ctx, `SELECT months,duration_days,amount_stars,fiat_currency,fiat_amount,store_product,store_quantity,
enabled,sort_order,label,managed_by,version,
EXTRACT(EPOCH FROM updated_at)::bigint FROM premium_plans WHERE months=$1`, months).
		Scan(&plan.Months, &plan.DurationDays, &plan.AmountStars, &plan.FiatCurrency,
			&plan.FiatAmount, &plan.StoreProduct, &plan.StoreQuantity, &plan.Enabled,
			&plan.SortOrder, &plan.Label, &plan.ManagedBy, &plan.Version, &updated)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.PremiumPlan{}, false, nil
	}
	if err != nil {
		return domain.PremiumPlan{}, false, fmt.Errorf("get premium plan: %w", err)
	}
	plan.UpdatedAt = int(updated)
	return plan, true, nil
}

func (s *PremiumStore) SyncPlans(ctx context.Context, plans []domain.PremiumPlan) error {
	if s == nil || s.db == nil {
		return domain.ErrPremiumPlanUnavailable
	}
	if len(plans) == 0 {
		return nil
	}
	return withTx(ctx, s.db, "sync premium plans", func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, premiumPlanCatalogLockID); err != nil {
			return fmt.Errorf("lock premium plan catalog: %w", err)
		}
		months := make([]int, 0, len(plans))
		for _, plan := range plans {
			plan = normalizedPremiumPlan(plan)
			if !plan.Valid() {
				return domain.ErrPremiumPlanInvalid
			}
			months = append(months, plan.Months)
			if _, err := tx.Exec(ctx, `INSERT INTO premium_plans
(months,duration_days,amount_stars,fiat_currency,fiat_amount,store_product,store_quantity,
 enabled,sort_order,label,managed_by,version,updated_at)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,'config',$11,now())
ON CONFLICT(months) DO UPDATE SET
duration_days=EXCLUDED.duration_days,amount_stars=EXCLUDED.amount_stars,enabled=EXCLUDED.enabled,
fiat_currency=EXCLUDED.fiat_currency,fiat_amount=EXCLUDED.fiat_amount,
store_product=EXCLUDED.store_product,store_quantity=EXCLUDED.store_quantity,
sort_order=EXCLUDED.sort_order,label=EXCLUDED.label,
version=CASE WHEN (premium_plans.duration_days,premium_plans.amount_stars,premium_plans.fiat_currency,
premium_plans.fiat_amount,premium_plans.store_product,premium_plans.store_quantity,premium_plans.enabled,
premium_plans.sort_order,premium_plans.label) IS DISTINCT FROM
(EXCLUDED.duration_days,EXCLUDED.amount_stars,EXCLUDED.fiat_currency,EXCLUDED.fiat_amount,
 EXCLUDED.store_product,EXCLUDED.store_quantity,EXCLUDED.enabled,EXCLUDED.sort_order,EXCLUDED.label)
THEN premium_plans.version+1 ELSE premium_plans.version END,
updated_at=CASE WHEN (premium_plans.duration_days,premium_plans.amount_stars,premium_plans.fiat_currency,
premium_plans.fiat_amount,premium_plans.store_product,premium_plans.store_quantity,premium_plans.enabled,
premium_plans.sort_order,premium_plans.label) IS DISTINCT FROM
(EXCLUDED.duration_days,EXCLUDED.amount_stars,EXCLUDED.fiat_currency,EXCLUDED.fiat_amount,
 EXCLUDED.store_product,EXCLUDED.store_quantity,EXCLUDED.enabled,EXCLUDED.sort_order,EXCLUDED.label)
THEN now() ELSE premium_plans.updated_at END
WHERE premium_plans.managed_by='config'`,
				plan.Months, plan.DurationDays, plan.AmountStars, plan.FiatCurrency, plan.FiatAmount,
				plan.StoreProduct, plan.StoreQuantity, plan.Enabled, plan.SortOrder,
				plan.Label, plan.Version); err != nil {
				return fmt.Errorf("upsert premium plan %d: %w", plan.Months, err)
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE premium_plans SET enabled=false,version=version+1,updated_at=now()
WHERE managed_by='config' AND enabled AND NOT (months=ANY($1::int[]))`, months); err != nil {
			return fmt.Errorf("disable removed premium plans: %w", err)
		}
		return nil
	})
}

func (s *PremiumStore) UpsertPremiumPlan(
	ctx context.Context,
	req domain.PremiumPlanUpsertRequest,
) (domain.PremiumPlan, error) {
	req.Label = strings.TrimSpace(req.Label)
	req.FiatCurrency = strings.ToUpper(strings.TrimSpace(req.FiatCurrency))
	if req.FiatCurrency == "" {
		req.FiatCurrency = domain.PremiumDefaultFiatCurrency
	}
	if req.FiatAmount <= 0 {
		req.FiatAmount = req.AmountStars
	}
	req.StoreProduct = strings.TrimSpace(req.StoreProduct)
	req.Reason = strings.TrimSpace(req.Reason)
	req.CommandKey = strings.TrimSpace(req.CommandKey)
	if s == nil || s.db == nil || !req.Valid() {
		return domain.PremiumPlan{}, domain.ErrPremiumPlanInvalid
	}
	var out domain.PremiumPlan
	err := withTx(ctx, s.db, "upsert premium plan", func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, premiumPlanCatalogLockID); err != nil {
			return fmt.Errorf("lock premium plan catalog: %w", err)
		}
		var before domain.PremiumPlan
		var updated int64
		err := tx.QueryRow(ctx, `SELECT months,duration_days,amount_stars,fiat_currency,fiat_amount,store_product,store_quantity,
enabled,sort_order,label,
managed_by,version,EXTRACT(EPOCH FROM updated_at)::bigint
FROM premium_plans WHERE months=$1 FOR UPDATE`, req.Months).
			Scan(&before.Months, &before.DurationDays, &before.AmountStars, &before.FiatCurrency,
				&before.FiatAmount, &before.StoreProduct, &before.StoreQuantity, &before.Enabled,
				&before.SortOrder, &before.Label, &before.ManagedBy, &before.Version, &updated)
		found := err == nil
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("lock premium plan: %w", err)
		}
		if found {
			before.UpdatedAt = int(updated)
			if req.ExpectedVersion == 0 || before.Version != req.ExpectedVersion {
				return domain.ErrPremiumPlanConflict
			}
			if !req.Enabled && before.Enabled {
				var enabled int
				if err := tx.QueryRow(ctx, `SELECT count(*) FROM premium_plans
WHERE enabled AND months<>$1`, req.Months).Scan(&enabled); err != nil {
					return err
				}
				if enabled == 0 {
					return domain.ErrPremiumLastPlan
				}
			}
			err = tx.QueryRow(ctx, `UPDATE premium_plans SET
duration_days=$2,amount_stars=$3,fiat_currency=$4,fiat_amount=$5,store_product=$6,store_quantity=$7,
enabled=$8,sort_order=$9,label=$10,
managed_by='admin',version=version+1,updated_at=now()
WHERE months=$1 AND version=$11
RETURNING months,duration_days,amount_stars,fiat_currency,fiat_amount,store_product,store_quantity,
enabled,sort_order,label,managed_by,version,
EXTRACT(EPOCH FROM updated_at)::bigint`,
				req.Months, req.DurationDays, req.AmountStars, req.FiatCurrency, req.FiatAmount,
				req.StoreProduct, req.StoreQuantity, req.Enabled, req.SortOrder, req.Label, req.ExpectedVersion).
				Scan(&out.Months, &out.DurationDays, &out.AmountStars, &out.FiatCurrency,
					&out.FiatAmount, &out.StoreProduct, &out.StoreQuantity, &out.Enabled,
					&out.SortOrder, &out.Label, &out.ManagedBy, &out.Version, &updated)
			if errors.Is(err, pgx.ErrNoRows) {
				return domain.ErrPremiumPlanConflict
			}
		} else {
			if req.ExpectedVersion != 0 {
				return domain.ErrPremiumPlanUnavailable
			}
			err = tx.QueryRow(ctx, `INSERT INTO premium_plans
(months,duration_days,amount_stars,fiat_currency,fiat_amount,store_product,store_quantity,
 enabled,sort_order,label,managed_by,version,updated_at)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,'admin',1,now())
RETURNING months,duration_days,amount_stars,fiat_currency,fiat_amount,store_product,store_quantity,
enabled,sort_order,label,managed_by,version,
EXTRACT(EPOCH FROM updated_at)::bigint`,
				req.Months, req.DurationDays, req.AmountStars, req.FiatCurrency, req.FiatAmount,
				req.StoreProduct, req.StoreQuantity, req.Enabled, req.SortOrder, req.Label).
				Scan(&out.Months, &out.DurationDays, &out.AmountStars, &out.FiatCurrency,
					&out.FiatAmount, &out.StoreProduct, &out.StoreQuantity, &out.Enabled,
					&out.SortOrder, &out.Label, &out.ManagedBy, &out.Version, &updated)
		}
		if err != nil {
			return fmt.Errorf("write premium plan: %w", err)
		}
		out.UpdatedAt = int(updated)
		var enabledPlans int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM premium_plans WHERE enabled`).
			Scan(&enabledPlans); err != nil {
			return err
		}
		if enabledPlans == 0 {
			return domain.ErrPremiumLastPlan
		}
		beforeJSON, err := json.Marshal(before)
		if err != nil {
			return err
		}
		afterJSON, err := json.Marshal(out)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO premium_audit_events
(actor_user_id,target_user_id,action,command_key,reason,metadata)
VALUES($1,0,$2,$3,$4,jsonb_build_object(
	'months',$5::integer,'before',$6::jsonb,'after',$7::jsonb))`,
			req.ActorUserID, map[bool]string{true: "plan_update", false: "plan_create"}[found],
			req.CommandKey, req.Reason, req.Months, beforeJSON, afterJSON); err != nil {
			return fmt.Errorf("audit premium plan: %w", err)
		}
		return nil
	})
	if err != nil {
		if isUniqueViolation(err) {
			return domain.PremiumPlan{}, domain.ErrPremiumPlanConflict
		}
		return domain.PremiumPlan{}, err
	}
	return out, nil
}

func (s *PremiumStore) IssuePremiumPaymentForm(ctx context.Context, form domain.PremiumPaymentForm) (domain.PremiumPaymentForm, error) {
	form.IdempotencyKey = strings.TrimSpace(form.IdempotencyKey)
	form.PaymentCurrency = form.EffectivePaymentCurrency()
	form.PaymentAmount = form.EffectivePaymentAmount()
	form.DebitStars = form.EffectiveDebitStars()
	if form.Message.Entities == nil {
		form.Message.Entities = []domain.MessageEntity{}
	}
	if s == nil || s.db == nil || !form.Valid() {
		return domain.PremiumPaymentForm{}, domain.ErrPremiumFormInvalid
	}
	giftEntities := form.Message.Entities
	if giftEntities == nil {
		giftEntities = []domain.MessageEntity{}
	}
	entities, err := json.Marshal(giftEntities)
	if err != nil {
		return domain.PremiumPaymentForm{}, domain.ErrPremiumGiftMessageInvalid
	}
	if form.IdempotencyKey != "" {
		if existing, status, found, err := s.premiumPaymentFormByIdempotency(ctx, form.BuyerUserID, form.IdempotencyKey); err != nil {
			return domain.PremiumPaymentForm{}, err
		} else if found {
			return s.issueExistingPremiumPaymentForm(ctx, form, existing, status)
		}
	}
	for attempt := 0; attempt < 8; attempt++ {
		form.ID, err = newPremiumPaymentFormID()
		if err != nil {
			return domain.PremiumPaymentForm{}, err
		}
		idempotencyKey := form.IdempotencyKey
		if idempotencyKey == "" {
			idempotencyKey = fmt.Sprintf("premium-form:%d:%d", form.BuyerUserID, form.ID)
		}
		_, err = s.db.Exec(ctx, `INSERT INTO premium_payment_intents
(form_id,idempotency_key,buyer_user_id,purchase_kind,recipient_user_id,months,duration_days,amount_stars,currency,
payment_amount,debit_stars,plan_version,gift_message,gift_entities,status,issued_at,expires_at)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,'pending',to_timestamp($15),to_timestamp($16))`,
			form.ID, idempotencyKey,
			form.BuyerUserID, string(form.Kind), form.RecipientUserID, form.Months,
			form.DurationDays, form.AmountStars, form.PaymentCurrency, form.PaymentAmount,
			form.DebitStars, form.PlanVersion, form.Message.Text, entities, form.IssuedAt, form.ExpiresAt)
		if err == nil {
			form.IdempotencyKey = idempotencyKey
			return form, nil
		}
		if !isUniqueViolation(err) {
			return domain.PremiumPaymentForm{}, fmt.Errorf("insert premium form: %w", err)
		}
		if form.IdempotencyKey != "" {
			existing, status, found, loadErr := s.premiumPaymentFormByIdempotency(ctx, form.BuyerUserID, form.IdempotencyKey)
			if loadErr != nil {
				return domain.PremiumPaymentForm{}, loadErr
			}
			if found {
				form.ID = 0
				return s.issueExistingPremiumPaymentForm(ctx, form, existing, status)
			}
		}
		form.ID = 0
	}
	return domain.PremiumPaymentForm{}, domain.ErrPremiumPlanUnavailable
}

func newPremiumPaymentFormID() (int64, error) {
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return 0, fmt.Errorf("generate premium form id: %w", err)
	}
	id := int64(binary.LittleEndian.Uint64(raw[:]) & 0x7fffffffffffffff)
	if id == 0 {
		id = 1
	}
	return id, nil
}

func (s *PremiumStore) issueExistingPremiumPaymentForm(
	ctx context.Context,
	requested, existing domain.PremiumPaymentForm,
	status domain.PremiumPaymentStatus,
) (domain.PremiumPaymentForm, error) {
	if !premiumFormIssueMatches(requested, existing) {
		return domain.PremiumPaymentForm{}, domain.ErrPremiumFormInvalid
	}
	switch status {
	case domain.PremiumPaymentPaid, domain.PremiumPaymentRefunded:
		return domain.PremiumPaymentForm{}, domain.ErrPremiumInvoiceAlreadyPaid
	case domain.PremiumPaymentPending:
		if existing.ExpiresAt > requested.IssuedAt {
			return existing, nil
		}
	case domain.PremiumPaymentExpired, domain.PremiumPaymentFailed:
		// A durable invoice may be opened long after its short-lived payment form
		// expired. Reuse the intent identity while rotating only the form id and
		// validity window; a paid/refunded intent is never reopened.
	default:
		return domain.PremiumPaymentForm{}, domain.ErrPremiumFormInvalid
	}

	for attempt := 0; attempt < 8; attempt++ {
		formID, err := newPremiumPaymentFormID()
		if err != nil {
			return domain.PremiumPaymentForm{}, err
		}
		tag, err := s.db.Exec(ctx, `UPDATE premium_payment_intents
SET form_id=$3,status='pending',issued_at=to_timestamp($4),expires_at=to_timestamp($5),
paid_at=NULL,refunded_at=NULL,stars_transaction_id=NULL,sender_message_id=NULL,
recipient_message_id=NULL,updated_at=now()
WHERE buyer_user_id=$1 AND idempotency_key=$2 AND (
    (status='pending' AND expires_at<=to_timestamp($4)) OR status IN ('expired','failed')
)`, requested.BuyerUserID, requested.IdempotencyKey, formID, requested.IssuedAt, requested.ExpiresAt)
		if err != nil {
			if isUniqueViolation(err) {
				continue
			}
			return domain.PremiumPaymentForm{}, fmt.Errorf("renew premium form: %w", err)
		}
		if tag.RowsAffected() == 1 {
			requested.ID = formID
			return requested, nil
		}

		latest, latestStatus, found, err := s.premiumPaymentFormByIdempotency(
			ctx, requested.BuyerUserID, requested.IdempotencyKey,
		)
		if err != nil {
			return domain.PremiumPaymentForm{}, err
		}
		if !found || !premiumFormIssueMatches(requested, latest) {
			return domain.PremiumPaymentForm{}, domain.ErrPremiumFormInvalid
		}
		switch latestStatus {
		case domain.PremiumPaymentPaid, domain.PremiumPaymentRefunded:
			return domain.PremiumPaymentForm{}, domain.ErrPremiumInvoiceAlreadyPaid
		case domain.PremiumPaymentPending:
			if latest.ExpiresAt > requested.IssuedAt {
				return latest, nil
			}
		case domain.PremiumPaymentExpired, domain.PremiumPaymentFailed:
			continue
		default:
			return domain.PremiumPaymentForm{}, domain.ErrPremiumFormInvalid
		}
	}
	return domain.PremiumPaymentForm{}, domain.ErrPremiumPlanUnavailable
}

func (s *PremiumStore) premiumPaymentFormByIdempotency(
	ctx context.Context,
	buyerUserID int64,
	idempotencyKey string,
) (domain.PremiumPaymentForm, domain.PremiumPaymentStatus, bool, error) {
	var formID int64
	err := s.db.QueryRow(ctx, `SELECT form_id FROM premium_payment_intents
WHERE buyer_user_id=$1 AND idempotency_key=$2`, buyerUserID, idempotencyKey).Scan(&formID)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.PremiumPaymentForm{}, "", false, nil
	}
	if err != nil {
		return domain.PremiumPaymentForm{}, "", false, err
	}
	form, status, _, err := loadPremiumPaymentForm(ctx, s.db, buyerUserID, formID, false)
	if err != nil {
		return domain.PremiumPaymentForm{}, "", false, err
	}
	form.IdempotencyKey = idempotencyKey
	return form, status, true, nil
}

func premiumFormIssueMatches(requested, stored domain.PremiumPaymentForm) bool {
	return requested.BuyerUserID == stored.BuyerUserID &&
		requested.Kind == stored.Kind &&
		requested.RecipientUserID == stored.RecipientUserID &&
		requested.Months == stored.Months &&
		requested.DurationDays == stored.DurationDays &&
		requested.AmountStars == stored.AmountStars &&
		requested.EffectivePaymentCurrency() == stored.EffectivePaymentCurrency() &&
		requested.EffectivePaymentAmount() == stored.EffectivePaymentAmount() &&
		requested.EffectiveDebitStars() == stored.EffectiveDebitStars() &&
		requested.PlanVersion == stored.PlanVersion &&
		requested.Message.Text == stored.Message.Text &&
		premiumEntitiesEqual(requested.Message.Entities, stored.Message.Entities)
}

func (s *PremiumStore) PurchasePremium(ctx context.Context, req domain.PremiumPurchaseRequest) (domain.PremiumPurchaseResult, error) {
	req.CommandKey = strings.TrimSpace(req.CommandKey)
	if s == nil || s.db == nil || s.messages == nil || req.BuyerUserID <= 0 ||
		req.FormID <= 0 || req.RecipientUserID <= 0 || req.Months <= 0 ||
		req.Date <= 0 || req.CommandKey == "" ||
		len(req.CommandKey) > 256 || !req.Message.Valid() {
		return domain.PremiumPurchaseResult{}, domain.ErrPremiumFormInvalid
	}
	if replay, found, err := s.loadPremiumPurchaseReplay(ctx, req); err != nil || found {
		return replay, err
	}
	fingerprint := premiumPurchaseFingerprint(req)
	sendReq := domain.SendPrivateTextRequest{
		RandomID:               lifecycleCommandRandomID("premium", req.BuyerUserID, req.CommandKey),
		Date:                   req.Date,
		OriginAuthKeyID:        req.OriginAuthKeyID,
		OriginSessionID:        req.OriginSessionID,
		OriginUserID:           req.BuyerUserID,
		RecipientBlocked:       req.RecipientBlocked,
		IdempotencyFingerprint: fingerprint[:],
	}
	switch req.Kind {
	case domain.PremiumPurchaseSelf:
		sendReq.SenderUserID = s.botID
		sendReq.RecipientUserID = req.BuyerUserID
		sendReq.Media = &domain.MessageMedia{Kind: domain.MessageMediaKindService,
			ServiceAction: &domain.MessageServiceAction{Kind: domain.MessageServiceActionGiftPremium,
				GiftPremium: &domain.MessageGiftPremiumAction{Currency: domain.PremiumCurrencyStars,
					Amount: 1, Days: 1}}}
	case domain.PremiumPurchaseGift:
		sendReq.SenderUserID = req.BuyerUserID
		sendReq.RecipientUserID = req.RecipientUserID
		sendReq.Media = &domain.MessageMedia{Kind: domain.MessageMediaKindService,
			ServiceAction: &domain.MessageServiceAction{Kind: domain.MessageServiceActionGiftPremium,
				GiftPremium: &domain.MessageGiftPremiumAction{Currency: domain.PremiumCurrencyStars,
					Amount: 1, Days: 1}}}
	default:
		return domain.PremiumPurchaseResult{}, domain.ErrPremiumFormInvalid
	}

	var result domain.PremiumPurchaseResult
	hooks := privateSendTxHooks{
		before: func(ctx context.Context, tx pgx.Tx, send *domain.SendPrivateTextRequest) error {
			form, entitlement, user, balance, err := s.settlePremiumPayment(ctx, tx, req)
			if err != nil {
				return err
			}
			result.Form, result.Entitlement, result.User, result.Balance = form, entitlement, user, balance
			send.Media = &domain.MessageMedia{Kind: domain.MessageMediaKindService,
				ServiceAction: &domain.MessageServiceAction{Kind: domain.MessageServiceActionGiftPremium,
					GiftPremium: &domain.MessageGiftPremiumAction{
						Currency: form.EffectivePaymentCurrency(),
						Amount:   form.EffectivePaymentAmount(),
						Days:     form.DurationDays,
						Message:  form.Message,
					}}}
			return nil
		},
		after: func(ctx context.Context, tx pgx.Tx, sent domain.SendPrivateTextResult) error {
			_, err := tx.Exec(ctx, `UPDATE premium_payment_intents
SET sender_message_id=$2,recipient_message_id=$3,updated_at=now()
WHERE form_id=$1 AND status='paid'`, req.FormID, nullableMessageID(sent.SenderMessage.ID),
				nullableMessageID(sent.RecipientMessage.ID))
			return err
		},
	}
	sent, err := s.messages.sendPrivateTextWithHooks(ctx, sendReq, hooks)
	if err != nil {
		if errors.Is(err, errPremiumAlreadyPaid) || isUniqueViolation(err) {
			if replay, found, replayErr := s.loadPremiumPurchaseReplay(ctx, req); replayErr != nil || found {
				return replay, replayErr
			}
		}
		if errors.Is(err, domain.ErrPremiumFormExpired) {
			_, _ = s.db.Exec(ctx, `UPDATE premium_payment_intents
SET status='expired',updated_at=now()
WHERE buyer_user_id=$1 AND form_id=$2 AND status='pending' AND expires_at<=to_timestamp($3)`,
				req.BuyerUserID, req.FormID, req.Date)
		}
		return domain.PremiumPurchaseResult{}, err
	}
	result.Send, result.Duplicate = sent, sent.Duplicate
	if sent.Duplicate {
		if replay, found, replayErr := s.loadPremiumPurchaseReplay(ctx, req); replayErr != nil || found {
			return replay, replayErr
		}
		return domain.PremiumPurchaseResult{}, domain.ErrPremiumFormInvalid
	}
	return result, nil
}

func nullableMessageID(id int) any {
	if id <= 0 {
		return nil
	}
	return id
}

func (s *PremiumStore) settlePremiumPayment(ctx context.Context, tx pgx.Tx, req domain.PremiumPurchaseRequest) (
	domain.PremiumPaymentForm, domain.PremiumEntitlement, domain.User, domain.StarsBalance, error,
) {
	form, status, intentID, err := loadPremiumPaymentForm(ctx, tx, req.BuyerUserID, req.FormID, true)
	if err != nil {
		return domain.PremiumPaymentForm{}, domain.PremiumEntitlement{}, domain.User{}, domain.StarsBalance{}, err
	}
	if status == domain.PremiumPaymentPaid {
		return domain.PremiumPaymentForm{}, domain.PremiumEntitlement{}, domain.User{}, domain.StarsBalance{}, errPremiumAlreadyPaid
	}
	if status != domain.PremiumPaymentPending || form.ExpiresAt <= req.Date {
		return domain.PremiumPaymentForm{}, domain.PremiumEntitlement{}, domain.User{}, domain.StarsBalance{}, domain.ErrPremiumFormExpired
	}
	if !premiumRequestMatchesForm(req, form) {
		return domain.PremiumPaymentForm{}, domain.PremiumEntitlement{}, domain.User{}, domain.StarsBalance{}, domain.ErrPremiumFormInvalid
	}
	var currentPlan domain.PremiumPlan
	if err := tx.QueryRow(ctx, `SELECT months,duration_days,amount_stars,fiat_currency,fiat_amount,store_product,store_quantity,
enabled,sort_order,label,version
FROM premium_plans WHERE months=$1 FOR SHARE`, form.Months).
		Scan(&currentPlan.Months, &currentPlan.DurationDays, &currentPlan.AmountStars, &currentPlan.FiatCurrency,
			&currentPlan.FiatAmount, &currentPlan.StoreProduct, &currentPlan.StoreQuantity, &currentPlan.Enabled,
			&currentPlan.SortOrder, &currentPlan.Label, &currentPlan.Version); err != nil {
		return domain.PremiumPaymentForm{}, domain.PremiumEntitlement{}, domain.User{}, domain.StarsBalance{}, domain.ErrPremiumPlanUnavailable
	}
	if !currentPlan.Enabled {
		return domain.PremiumPaymentForm{}, domain.PremiumEntitlement{}, domain.User{}, domain.StarsBalance{}, domain.ErrPremiumPlanUnavailable
	}
	if currentPlan.Version != form.PlanVersion || currentPlan.DurationDays != form.DurationDays ||
		(form.EffectiveDebitStars() && currentPlan.AmountStars != form.AmountStars) ||
		(!form.EffectiveDebitStars() && (currentPlan.EffectiveFiatCurrency() != form.EffectivePaymentCurrency() ||
			currentPlan.EffectiveFiatAmount() != form.EffectivePaymentAmount())) {
		return domain.PremiumPaymentForm{}, domain.PremiumEntitlement{}, domain.User{}, domain.StarsBalance{}, domain.ErrPremiumFormAmountChanged
	}
	if err := lockUsersForUpdate(ctx, tx, form.BuyerUserID, form.RecipientUserID); err != nil {
		return domain.PremiumPaymentForm{}, domain.PremiumEntitlement{}, domain.User{}, domain.StarsBalance{}, err
	}

	var isBot bool
	var deletedAt, premiumUntil *time.Time
	if err := tx.QueryRow(ctx, `SELECT is_bot,deleted_at,premium_expires_at FROM users
WHERE id=$1 FOR UPDATE`, form.RecipientUserID).Scan(&isBot, &deletedAt, &premiumUntil); err != nil {
		return domain.PremiumPaymentForm{}, domain.PremiumEntitlement{}, domain.User{}, domain.StarsBalance{}, domain.ErrPremiumRecipientInvalid
	}
	if isBot || deletedAt != nil || domain.IsSystemUserID(form.RecipientUserID) ||
		form.RecipientUserID == s.botID {
		return domain.PremiumPaymentForm{}, domain.PremiumEntitlement{}, domain.User{}, domain.StarsBalance{}, domain.ErrPremiumRecipientInvalid
	}
	var disallowPremiumGifts bool
	if err := tx.QueryRow(ctx, `SELECT COALESCE((
SELECT disallow_premium_gifts FROM account_settings WHERE user_id=$1
),false)`, form.RecipientUserID).Scan(&disallowPremiumGifts); err != nil {
		return domain.PremiumPaymentForm{}, domain.PremiumEntitlement{}, domain.User{}, domain.StarsBalance{}, err
	}
	if form.Kind == domain.PremiumPurchaseGift && disallowPremiumGifts {
		return domain.PremiumPaymentForm{}, domain.PremiumEntitlement{}, domain.User{}, domain.StarsBalance{}, domain.ErrPremiumRecipientRestricted
	}
	if form.Kind == domain.PremiumPurchaseGift && form.RecipientUserID == form.BuyerUserID {
		return domain.PremiumPaymentForm{}, domain.PremiumEntitlement{}, domain.User{}, domain.StarsBalance{}, domain.ErrPremiumGiftSelf
	}
	if form.Kind == domain.PremiumPurchaseGift && premiumUntil != nil && int(premiumUntil.Unix()) > req.Date {
		return domain.PremiumPaymentForm{}, domain.PremiumEntitlement{}, domain.User{}, domain.StarsBalance{},
			domain.PremiumSubscriptionActiveError{Until: int(premiumUntil.Unix())}
	}

	var balance int64
	var granted bool
	lockSuffix := ""
	if form.EffectiveDebitStars() {
		lockSuffix = " FOR UPDATE"
	}
	err = tx.QueryRow(ctx, `SELECT balance,granted FROM stars_balances WHERE user_id=$1`+lockSuffix,
		form.BuyerUserID).Scan(&balance, &granted)
	if errors.Is(err, pgx.ErrNoRows) || form.EffectiveDebitStars() && err == nil && balance < form.AmountStars {
		return domain.PremiumPaymentForm{}, domain.PremiumEntitlement{}, domain.User{}, domain.StarsBalance{}, domain.ErrStarsInsufficient
	}
	if err != nil {
		return domain.PremiumPaymentForm{}, domain.PremiumEntitlement{}, domain.User{}, domain.StarsBalance{}, fmt.Errorf("load premium buyer balance: %w", err)
	}
	var starsTransactionID int64
	if form.EffectiveDebitStars() {
		if err := tx.QueryRow(ctx, `UPDATE stars_balances SET balance=balance-$2,updated_at=now()
WHERE user_id=$1 RETURNING balance`, form.BuyerUserID, form.AmountStars).Scan(&balance); err != nil {
			return domain.PremiumPaymentForm{}, domain.PremiumEntitlement{}, domain.User{}, domain.StarsBalance{}, fmt.Errorf("debit premium stars: %w", err)
		}
		if err := tx.QueryRow(ctx, `INSERT INTO stars_transactions
(user_id,peer_type,peer_id,amount,reason,title,description,date,premium_payment_intent_id,
premium_recipient_user_id,premium_months)
VALUES($1,'user',$2,$3,$4,$5,$6,$7,$8,$9,$10) RETURNING id`,
			form.BuyerUserID, s.botID, -form.AmountStars, string(domain.StarsReasonPremium),
			"Telegram Premium", domain.PremiumPurchaseDescription(form.Kind, form.Months, form.RecipientUserID),
			req.Date, intentID, form.RecipientUserID, form.Months).Scan(&starsTransactionID); err != nil {
			return domain.PremiumPaymentForm{}, domain.PremiumEntitlement{}, domain.User{}, domain.StarsBalance{}, fmt.Errorf("insert premium stars transaction: %w", err)
		}
	}

	startsAt := req.Date
	if premiumUntil != nil && int(premiumUntil.Unix()) > startsAt {
		startsAt = int(premiumUntil.Unix())
	}
	expiresAt := int(time.Unix(int64(startsAt), 0).Add(time.Duration(form.DurationDays) * 24 * time.Hour).Unix())
	source := domain.PremiumEntitlementPurchase
	if form.Kind == domain.PremiumPurchaseGift {
		source = domain.PremiumEntitlementGift
	}
	entitlement := domain.PremiumEntitlement{
		UserID: form.RecipientUserID, Source: source, SourceUserID: form.BuyerUserID,
		PaymentIntentID: intentID, TransactionID: starsTransactionID,
		Months: form.Months, DurationDays: form.DurationDays,
		StartsAt: startsAt, ExpiresAt: expiresAt, Status: domain.PremiumEntitlementActive, CreatedAt: req.Date,
	}
	if err := tx.QueryRow(ctx, `INSERT INTO premium_entitlements
(user_id,source,source_user_id,payment_intent_id,transaction_id,months,duration_days,starts_at,expires_at,status)
VALUES($1,$2,$3,$4,$5,$6,$7,to_timestamp($8),to_timestamp($9),'active') RETURNING id`,
		entitlement.UserID, string(entitlement.Source), entitlement.SourceUserID, intentID,
		nullableInt64(starsTransactionID), entitlement.Months, entitlement.DurationDays, startsAt, expiresAt).
		Scan(&entitlement.ID); err != nil {
		return domain.PremiumPaymentForm{}, domain.PremiumEntitlement{}, domain.User{}, domain.StarsBalance{}, fmt.Errorf("insert premium entitlement: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE users SET premium_expires_at=to_timestamp($2),
premium_updated_at=to_timestamp($3),updated_at=now()
WHERE id=$1`, form.RecipientUserID, expiresAt, req.Date); err != nil {
		return domain.PremiumPaymentForm{}, domain.PremiumEntitlement{}, domain.User{}, domain.StarsBalance{}, fmt.Errorf("update premium aggregate: %w", err)
	}
	tag, err := tx.Exec(ctx, `UPDATE premium_payment_intents SET status='paid',paid_at=to_timestamp($2),
stars_transaction_id=$3,updated_at=now() WHERE id=$1 AND status='pending'`,
		intentID, req.Date, nullableInt64(starsTransactionID))
	if err != nil || tag.RowsAffected() != 1 {
		if err != nil {
			return domain.PremiumPaymentForm{}, domain.PremiumEntitlement{}, domain.User{}, domain.StarsBalance{}, err
		}
		return domain.PremiumPaymentForm{}, domain.PremiumEntitlement{}, domain.User{}, domain.StarsBalance{}, errPremiumAlreadyPaid
	}
	if _, err := tx.Exec(ctx, `INSERT INTO premium_audit_events
(actor_user_id,target_user_id,payment_intent_id,entitlement_id,action,metadata)
VALUES($1,$2,$3,$4,'purchase',jsonb_build_object(
	'kind',$5::text,'months',$6::integer,'amount_stars',$7::bigint,
	'currency',$8::text,'payment_amount',$9::bigint,'debit_stars',$10::boolean))`,
		form.BuyerUserID, form.RecipientUserID, intentID, entitlement.ID, string(form.Kind),
		form.Months, form.AmountStars, form.EffectivePaymentCurrency(), form.EffectivePaymentAmount(),
		form.EffectiveDebitStars()); err != nil {
		return domain.PremiumPaymentForm{}, domain.PremiumEntitlement{}, domain.User{}, domain.StarsBalance{}, err
	}
	user, found, err := NewUserStore(tx).ByID(ctx, form.RecipientUserID)
	if err != nil || !found {
		if err != nil {
			return domain.PremiumPaymentForm{}, domain.PremiumEntitlement{}, domain.User{}, domain.StarsBalance{}, err
		}
		return domain.PremiumPaymentForm{}, domain.PremiumEntitlement{}, domain.User{}, domain.StarsBalance{}, domain.ErrPremiumRecipientInvalid
	}
	return form, entitlement, user, domain.StarsBalance{
		UserID: form.BuyerUserID, Balance: balance, Granted: granted,
	}, nil
}

func loadPremiumPaymentForm(ctx context.Context, db sqlcgen.DBTX, buyerID, formID int64, lock bool) (
	domain.PremiumPaymentForm, domain.PremiumPaymentStatus, int64, error,
) {
	query := `SELECT id,purchase_kind,recipient_user_id,months,duration_days,amount_stars,currency,payment_amount,debit_stars,plan_version,
gift_message,gift_entities,EXTRACT(EPOCH FROM issued_at)::bigint,EXTRACT(EPOCH FROM expires_at)::bigint,status
FROM premium_payment_intents WHERE buyer_user_id=$1 AND form_id=$2`
	if lock {
		query += ` FOR UPDATE`
	}
	var form domain.PremiumPaymentForm
	var intentID, issued, expires int64
	var kind, status string
	var entities []byte
	err := db.QueryRow(ctx, query, buyerID, formID).Scan(&intentID, &kind, &form.RecipientUserID,
		&form.Months, &form.DurationDays, &form.AmountStars, &form.PaymentCurrency,
		&form.PaymentAmount, &form.DebitStars, &form.PlanVersion, &form.Message.Text,
		&entities, &issued, &expires, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.PremiumPaymentForm{}, "", 0, domain.ErrPremiumFormExpired
	}
	if err != nil {
		return domain.PremiumPaymentForm{}, "", 0, err
	}
	if err := json.Unmarshal(entities, &form.Message.Entities); err != nil {
		return domain.PremiumPaymentForm{}, "", 0, domain.ErrPremiumFormInvalid
	}
	form.ID, form.BuyerUserID, form.Kind = formID, buyerID, domain.PremiumPurchaseKind(kind)
	form.IssuedAt, form.ExpiresAt = int(issued), int(expires)
	return form, domain.PremiumPaymentStatus(status), intentID, nil
}

func premiumRequestMatchesForm(req domain.PremiumPurchaseRequest, form domain.PremiumPaymentForm) bool {
	return req.BuyerUserID == form.BuyerUserID && req.FormID == form.ID &&
		req.Kind == form.Kind && req.RecipientUserID == form.RecipientUserID &&
		req.Months == form.Months && (req.PlanVersion == 0 || req.PlanVersion == form.PlanVersion) &&
		req.Message.Text == form.Message.Text && premiumEntitiesEqual(req.Message.Entities, form.Message.Entities)
}

func premiumEntitiesEqual(left, right []domain.MessageEntity) bool {
	if len(left) == 0 && len(right) == 0 {
		return true
	}
	return reflect.DeepEqual(left, right)
}

func premiumPurchaseFingerprint(req domain.PremiumPurchaseRequest) [32]byte {
	entities, _ := json.Marshal(req.Message.Entities)
	return sha256.Sum256([]byte(fmt.Sprintf("telesrv:premium-purchase:v1:%d:%d:%s:%d:%d:%d:%s:%s",
		req.BuyerUserID, req.FormID, req.Kind, req.RecipientUserID, req.Months, req.PlanVersion,
		req.Message.Text, entities)))
}

func (s *PremiumStore) loadPremiumPurchaseReplay(ctx context.Context, req domain.PremiumPurchaseRequest) (
	domain.PremiumPurchaseResult, bool, error,
) {
	form, status, intentID, err := loadPremiumPaymentForm(ctx, s.db, req.BuyerUserID, req.FormID, false)
	if errors.Is(err, domain.ErrPremiumFormExpired) {
		return domain.PremiumPurchaseResult{}, false, nil
	}
	if err != nil {
		return domain.PremiumPurchaseResult{}, false, err
	}
	if status != domain.PremiumPaymentPaid && status != domain.PremiumPaymentRefunded {
		return domain.PremiumPurchaseResult{}, false, nil
	}
	if !premiumRequestMatchesForm(req, form) {
		return domain.PremiumPurchaseResult{}, false, domain.ErrPremiumFormInvalid
	}
	entitlement, found, err := premiumEntitlementByPayment(ctx, s.db, intentID)
	if err != nil || !found {
		if err != nil {
			return domain.PremiumPurchaseResult{}, false, err
		}
		return domain.PremiumPurchaseResult{}, false, domain.ErrPremiumFormInvalid
	}
	balance, err := NewStarsStore(s.db).GetBalance(ctx, req.BuyerUserID)
	if err != nil {
		return domain.PremiumPurchaseResult{}, false, err
	}
	user, found, err := NewUserStore(s.db).ByID(ctx, form.RecipientUserID)
	if err != nil || !found {
		return domain.PremiumPurchaseResult{}, false, err
	}
	fingerprint := premiumPurchaseFingerprint(req)
	senderID, recipientID := req.BuyerUserID, req.RecipientUserID
	if req.Kind == domain.PremiumPurchaseSelf {
		senderID, recipientID = s.botID, req.BuyerUserID
	}
	sent, replayFound, err := s.messages.LookupPrivateSendReplay(ctx, domain.PrivateSendReplayRequest{
		SenderUserID: senderID, RecipientUserID: recipientID,
		RandomID:               lifecycleCommandRandomID("premium", req.BuyerUserID, req.CommandKey),
		IdempotencyFingerprint: fingerprint[:],
	})
	if err != nil {
		return domain.PremiumPurchaseResult{}, false, err
	}
	if !replayFound {
		return domain.PremiumPurchaseResult{}, false, domain.ErrPremiumFormInvalid
	}
	return domain.PremiumPurchaseResult{
		Form: form, Entitlement: entitlement, User: user, Balance: balance,
		Send: sent, Duplicate: true,
	}, true, nil
}

func premiumEntitlementByPayment(ctx context.Context, db sqlcgen.DBTX, paymentID int64) (
	domain.PremiumEntitlement, bool, error,
) {
	var out domain.PremiumEntitlement
	var source, status string
	var starts, expires, created int64
	err := db.QueryRow(ctx, `SELECT id,user_id,source,source_user_id,payment_intent_id,COALESCE(transaction_id,0),months,duration_days,
EXTRACT(EPOCH FROM starts_at)::bigint,EXTRACT(EPOCH FROM expires_at)::bigint,status,command_key,
EXTRACT(EPOCH FROM created_at)::bigint FROM premium_entitlements WHERE payment_intent_id=$1`, paymentID).
		Scan(&out.ID, &out.UserID, &source, &out.SourceUserID, &out.PaymentIntentID,
			&out.TransactionID, &out.Months, &out.DurationDays, &starts, &expires, &status, &out.CommandKey, &created)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.PremiumEntitlement{}, false, nil
	}
	if err != nil {
		return domain.PremiumEntitlement{}, false, err
	}
	out.Source, out.Status = domain.PremiumEntitlementSource(source), domain.PremiumEntitlementStatus(status)
	out.StartsAt, out.ExpiresAt, out.CreatedAt = int(starts), int(expires), int(created)
	return out, true, nil
}

func (s *PremiumStore) ActivePremiumEntitlements(ctx context.Context, userID int64, now int) ([]domain.PremiumEntitlement, error) {
	if s == nil || s.db == nil || userID <= 0 {
		return nil, nil
	}
	rows, err := s.db.Query(ctx, `SELECT id,user_id,source,source_user_id,COALESCE(payment_intent_id,0),
COALESCE(transaction_id,0),
months,duration_days,EXTRACT(EPOCH FROM starts_at)::bigint,EXTRACT(EPOCH FROM expires_at)::bigint,
status,command_key,EXTRACT(EPOCH FROM created_at)::bigint
FROM premium_entitlements WHERE user_id=$1 AND status='active' AND expires_at>to_timestamp($2)
ORDER BY expires_at,id`, userID, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]domain.PremiumEntitlement, 0)
	for rows.Next() {
		var item domain.PremiumEntitlement
		var source, status string
		var starts, expires, created int64
		if err := rows.Scan(&item.ID, &item.UserID, &source, &item.SourceUserID, &item.PaymentIntentID,
			&item.TransactionID, &item.Months, &item.DurationDays, &starts, &expires, &status, &item.CommandKey, &created); err != nil {
			return nil, err
		}
		item.Source, item.Status = domain.PremiumEntitlementSource(source), domain.PremiumEntitlementStatus(status)
		item.StartsAt, item.ExpiresAt, item.CreatedAt = int(starts), int(expires), int(created)
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *PremiumStore) PremiumEntitlements(
	ctx context.Context,
	userID int64,
	limit int,
) ([]domain.PremiumEntitlement, error) {
	if s == nil || s.db == nil || userID <= 0 {
		return nil, nil
	}
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	rows, err := s.db.Query(ctx, `SELECT id,user_id,source,source_user_id,COALESCE(payment_intent_id,0),
COALESCE(transaction_id,0),
months,duration_days,EXTRACT(EPOCH FROM starts_at)::bigint,EXTRACT(EPOCH FROM expires_at)::bigint,
status,command_key,EXTRACT(EPOCH FROM created_at)::bigint
FROM premium_entitlements
WHERE user_id=$1 OR source_user_id=$1
ORDER BY created_at DESC,id DESC LIMIT $2`, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]domain.PremiumEntitlement, 0)
	for rows.Next() {
		var item domain.PremiumEntitlement
		var source, status string
		var starts, expires, created int64
		if err := rows.Scan(&item.ID, &item.UserID, &source, &item.SourceUserID, &item.PaymentIntentID,
			&item.TransactionID, &item.Months, &item.DurationDays, &starts, &expires, &status,
			&item.CommandKey, &created); err != nil {
			return nil, err
		}
		item.Source, item.Status = domain.PremiumEntitlementSource(source), domain.PremiumEntitlementStatus(status)
		item.StartsAt, item.ExpiresAt, item.CreatedAt = int(starts), int(expires), int(created)
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *PremiumStore) PremiumPurchaseHistory(
	ctx context.Context,
	userID int64,
	limit int,
) ([]domain.PremiumEntitlement, error) {
	if s == nil || s.db == nil || userID <= 0 {
		return nil, nil
	}
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	rows, err := s.db.Query(ctx, `SELECT id,user_id,source,source_user_id,COALESCE(payment_intent_id,0),
COALESCE(transaction_id,0),
months,duration_days,EXTRACT(EPOCH FROM starts_at)::bigint,EXTRACT(EPOCH FROM expires_at)::bigint,
status,command_key,EXTRACT(EPOCH FROM created_at)::bigint
FROM premium_entitlements
WHERE (user_id=$1 OR source_user_id=$1) AND source IN ('purchase','gift')
ORDER BY created_at DESC,id DESC LIMIT $2`, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]domain.PremiumEntitlement, 0)
	for rows.Next() {
		var item domain.PremiumEntitlement
		var source, status string
		var starts, expires, created int64
		if err := rows.Scan(&item.ID, &item.UserID, &source, &item.SourceUserID, &item.PaymentIntentID,
			&item.TransactionID, &item.Months, &item.DurationDays, &starts, &expires, &status, &item.CommandKey, &created); err != nil {
			return nil, err
		}
		item.Source, item.Status = domain.PremiumEntitlementSource(source), domain.PremiumEntitlementStatus(status)
		item.StartsAt, item.ExpiresAt, item.CreatedAt = int(starts), int(expires), int(created)
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *PremiumStore) PremiumPayment(
	ctx context.Context,
	paymentIntentID int64,
) (domain.PremiumPaymentDetails, bool, error) {
	if s == nil || s.db == nil || paymentIntentID <= 0 {
		return domain.PremiumPaymentDetails{}, false, nil
	}
	var (
		out                             domain.PremiumPaymentDetails
		kind, status                    string
		entities                        []byte
		issued, expires, paid, refunded int64
		created, updated                int64
	)
	err := s.db.QueryRow(ctx, `SELECT id,form_id,idempotency_key,buyer_user_id,purchase_kind,recipient_user_id,
months,duration_days,amount_stars,currency,plan_version,gift_message,gift_entities,status,
payment_amount,debit_stars,
EXTRACT(EPOCH FROM issued_at)::bigint,EXTRACT(EPOCH FROM expires_at)::bigint,
COALESCE(EXTRACT(EPOCH FROM paid_at),0)::bigint,COALESCE(EXTRACT(EPOCH FROM refunded_at),0)::bigint,
COALESCE(stars_transaction_id,0),COALESCE(sender_message_id,0),COALESCE(recipient_message_id,0),
EXTRACT(EPOCH FROM created_at)::bigint,EXTRACT(EPOCH FROM updated_at)::bigint
FROM premium_payment_intents WHERE id=$1`, paymentIntentID).Scan(
		&out.Intent.ID, &out.Intent.FormID, &out.Intent.IdempotencyKey, &out.Intent.BuyerUserID,
		&kind, &out.Intent.RecipientUserID, &out.Intent.Months, &out.Intent.DurationDays,
		&out.Intent.AmountStars, &out.Intent.Currency, &out.Intent.PlanVersion,
		&out.Intent.Message.Text, &entities, &status, &out.Intent.PaymentAmount, &out.Intent.DebitStars,
		&issued, &expires, &paid, &refunded,
		&out.Intent.StarsTransactionID, &out.Intent.SenderMessageID, &out.Intent.RecipientMessageID,
		&created, &updated,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.PremiumPaymentDetails{}, false, nil
	}
	if err != nil {
		return domain.PremiumPaymentDetails{}, false, err
	}
	if err := json.Unmarshal(entities, &out.Intent.Message.Entities); err != nil {
		return domain.PremiumPaymentDetails{}, false, domain.ErrPremiumFormInvalid
	}
	out.Intent.Kind = domain.PremiumPurchaseKind(kind)
	out.Intent.Status = domain.PremiumPaymentStatus(status)
	out.Intent.IssuedAt, out.Intent.ExpiresAt = int(issued), int(expires)
	out.Intent.PaidAt, out.Intent.RefundedAt = int(paid), int(refunded)
	out.Intent.CreatedAt, out.Intent.UpdatedAt = int(created), int(updated)

	entitlement, found, err := premiumEntitlementByPayment(ctx, s.db, paymentIntentID)
	if err != nil {
		return domain.PremiumPaymentDetails{}, false, err
	}
	if found {
		out.Entitlement = entitlement
	}
	if out.Intent.StarsTransactionID != 0 {
		var peerType, reason string
		err := s.db.QueryRow(ctx, `SELECT user_id,peer_type,peer_id,amount,reason,title,description,date,
COALESCE(premium_payment_intent_id,0),COALESCE(premium_recipient_user_id,0),COALESCE(premium_months,0)
FROM stars_transactions WHERE id=$1`, out.Intent.StarsTransactionID).Scan(
			&out.Transaction.UserID, &peerType, &out.Transaction.Peer.ID, &out.Transaction.Amount,
			&reason, &out.Transaction.Title, &out.Transaction.Description, &out.Transaction.Date,
			&out.Transaction.PaymentID, &out.Transaction.RecipientUserID, &out.Transaction.PremiumMonths,
		)
		if err != nil {
			return domain.PremiumPaymentDetails{}, false, err
		}
		out.Transaction.ID = out.Intent.StarsTransactionID
		out.Transaction.Peer.Type = domain.PeerType(peerType)
		out.Transaction.Reason = domain.StarsTransactionReason(reason)
	}
	return out, true, nil
}

func (s *PremiumStore) SweepPremiumEntitlements(ctx context.Context, now, limit int) ([]domain.User, error) {
	if s == nil || s.db == nil {
		return nil, nil
	}
	if limit <= 0 {
		limit = 500
	}
	var users []domain.User
	err := withTx(ctx, s.db, "sweep premium entitlements", func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id,user_id FROM premium_entitlements
WHERE status='active' AND expires_at<=to_timestamp($1)
ORDER BY expires_at,id LIMIT $2 FOR UPDATE SKIP LOCKED`, now, limit)
		if err != nil {
			return err
		}
		type expiredEntitlement struct {
			id, userID int64
		}
		expired := make([]expiredEntitlement, 0)
		var ids []int64
		userIDs := make(map[int64]struct{})
		for rows.Next() {
			var id, userID int64
			if err := rows.Scan(&id, &userID); err != nil {
				rows.Close()
				return err
			}
			ids = append(ids, id)
			expired = append(expired, expiredEntitlement{id: id, userID: userID})
			userIDs[userID] = struct{}{}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if len(ids) == 0 {
			return nil
		}
		if _, err := tx.Exec(ctx, `UPDATE premium_entitlements SET status='expired',updated_at=now()
WHERE id=ANY($1::bigint[])`, ids); err != nil {
			return err
		}
		for _, item := range expired {
			if _, err := tx.Exec(ctx, `INSERT INTO premium_audit_events
(actor_user_id,target_user_id,entitlement_id,action,metadata)
VALUES(0,$1,$2,'expire',jsonb_build_object('expired_at',$3::bigint))`,
				item.userID, item.id, now); err != nil {
				return err
			}
		}
		for userID := range userIDs {
			var maxExpiry *time.Time
			if err := tx.QueryRow(ctx, `SELECT MAX(expires_at) FROM premium_entitlements
WHERE user_id=$1 AND status='active' AND expires_at>to_timestamp($2)`, userID, now).Scan(&maxExpiry); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE users SET premium_expires_at=$2,
premium_updated_at=to_timestamp($3),updated_at=now() WHERE id=$1`,
				userID, maxExpiry, now); err != nil {
				return err
			}
			user, found, err := NewUserStore(tx).ByID(ctx, userID)
			if err != nil {
				return err
			}
			if found {
				users = append(users, user)
			}
		}
		return nil
	})
	return users, err
}

func (s *PremiumStore) GrantPremiumEntitlement(ctx context.Context, req domain.PremiumAdminGrantRequest) (
	domain.PremiumEntitlement, domain.User, error,
) {
	req.CommandKey, req.Reason = strings.TrimSpace(req.CommandKey), strings.TrimSpace(req.Reason)
	if s == nil || s.db == nil || req.UserID <= 0 || req.ActorUserID <= 0 ||
		req.Months <= 0 || req.Months > domain.MaxPremiumPlanMonths ||
		req.DurationDays <= 0 || req.DurationDays > domain.MaxPremiumPlanDurationDays || req.Date <= 0 ||
		req.CommandKey == "" || len(req.CommandKey) > 256 || len(req.Reason) > 1024 {
		return domain.PremiumEntitlement{}, domain.User{}, domain.ErrPremiumFormInvalid
	}
	if entitlement, user, found, err := s.loadPremiumAdminGrant(ctx, req); err != nil || found {
		return entitlement, user, err
	}
	var entitlement domain.PremiumEntitlement
	var user domain.User
	err := withTx(ctx, s.db, "grant premium entitlement", func(tx pgx.Tx) error {
		if err := lockUsersForUpdate(ctx, tx, req.UserID, req.UserID); err != nil {
			return err
		}
		var isBot bool
		var deletedAt, premiumUntil *time.Time
		if err := tx.QueryRow(ctx, `SELECT is_bot,deleted_at,premium_expires_at FROM users WHERE id=$1 FOR UPDATE`,
			req.UserID).Scan(&isBot, &deletedAt, &premiumUntil); err != nil ||
			isBot || deletedAt != nil || domain.IsSystemUserID(req.UserID) {
			return domain.ErrPremiumRecipientInvalid
		}
		startsAt := req.Date
		if premiumUntil != nil && int(premiumUntil.Unix()) > startsAt {
			startsAt = int(premiumUntil.Unix())
		}
		expiresAt := int(time.Unix(int64(startsAt), 0).Add(time.Duration(req.DurationDays) * 24 * time.Hour).Unix())
		entitlement = domain.PremiumEntitlement{
			UserID: req.UserID, Source: domain.PremiumEntitlementAdmin, SourceUserID: req.ActorUserID,
			Months: req.Months, DurationDays: req.DurationDays, StartsAt: startsAt, ExpiresAt: expiresAt,
			Status: domain.PremiumEntitlementActive, CommandKey: req.CommandKey, CreatedAt: req.Date,
		}
		if err := tx.QueryRow(ctx, `INSERT INTO premium_entitlements
(user_id,source,source_user_id,months,duration_days,starts_at,expires_at,status,command_key,reason)
VALUES($1,'admin',$2,$3,$4,to_timestamp($5),to_timestamp($6),'active',$7,$8) RETURNING id`,
			req.UserID, req.ActorUserID, req.Months, req.DurationDays, startsAt, expiresAt,
			req.CommandKey, req.Reason).Scan(&entitlement.ID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE users SET premium_expires_at=to_timestamp($2),
premium_updated_at=to_timestamp($3),updated_at=now() WHERE id=$1`,
			req.UserID, expiresAt, req.Date); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO premium_audit_events
(actor_user_id,target_user_id,entitlement_id,action,command_key,reason,metadata)
VALUES($1,$2,$3,'admin_grant',$4,$5,jsonb_build_object(
	'months',$6::integer,'duration_days',$7::integer))`,
			req.ActorUserID, req.UserID, entitlement.ID, req.CommandKey, req.Reason,
			req.Months, req.DurationDays); err != nil {
			return err
		}
		var found bool
		var err error
		user, found, err = NewUserStore(tx).ByID(ctx, req.UserID)
		if err != nil || !found {
			return domain.ErrPremiumRecipientInvalid
		}
		return nil
	})
	if err != nil {
		if isUniqueViolation(err) {
			if replay, replayUser, found, replayErr := s.loadPremiumAdminGrant(ctx, req); replayErr != nil || found {
				return replay, replayUser, replayErr
			}
		}
		return domain.PremiumEntitlement{}, domain.User{}, err
	}
	return entitlement, user, nil
}

func (s *PremiumStore) loadPremiumAdminGrant(ctx context.Context, req domain.PremiumAdminGrantRequest) (
	domain.PremiumEntitlement, domain.User, bool, error,
) {
	var out domain.PremiumEntitlement
	var starts, expires, created int64
	err := s.db.QueryRow(ctx, `SELECT id,user_id,months,duration_days,
EXTRACT(EPOCH FROM starts_at)::bigint,EXTRACT(EPOCH FROM expires_at)::bigint,
EXTRACT(EPOCH FROM created_at)::bigint FROM premium_entitlements
WHERE source='admin' AND source_user_id=$1 AND command_key=$2`, req.ActorUserID, req.CommandKey).
		Scan(&out.ID, &out.UserID, &out.Months, &out.DurationDays, &starts, &expires, &created)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.PremiumEntitlement{}, domain.User{}, false, nil
	}
	if err != nil {
		return domain.PremiumEntitlement{}, domain.User{}, false, err
	}
	if out.UserID != req.UserID || out.Months != req.Months || out.DurationDays != req.DurationDays {
		return domain.PremiumEntitlement{}, domain.User{}, false, domain.ErrPremiumFormInvalid
	}
	out.Source, out.SourceUserID, out.Status = domain.PremiumEntitlementAdmin, req.ActorUserID, domain.PremiumEntitlementActive
	out.StartsAt, out.ExpiresAt, out.CreatedAt, out.CommandKey = int(starts), int(expires), int(created), req.CommandKey
	user, found, err := NewUserStore(s.db).ByID(ctx, out.UserID)
	return out, user, found, err
}

func (s *PremiumStore) RevokePremiumEntitlements(
	ctx context.Context,
	req domain.PremiumAdminRevokeRequest,
) (domain.User, error) {
	req.CommandKey, req.Reason = strings.TrimSpace(req.CommandKey), strings.TrimSpace(req.Reason)
	if s == nil || s.db == nil || req.UserID <= 0 || req.ActorUserID <= 0 ||
		req.EntitlementID < 0 || req.Date <= 0 || req.CommandKey == "" || len(req.CommandKey) > 256 || len(req.Reason) > 1024 {
		return domain.User{}, domain.ErrPremiumFormInvalid
	}
	if user, found, err := s.loadPremiumAdminRevoke(ctx, req); err != nil || found {
		return user, err
	}
	var user domain.User
	err := withTx(ctx, s.db, "revoke premium entitlements", func(tx pgx.Tx) error {
		if err := lockUsersForUpdate(ctx, tx, req.UserID, req.UserID); err != nil {
			return err
		}
		var isBot bool
		var deletedAt *time.Time
		if err := tx.QueryRow(ctx, `SELECT is_bot,deleted_at FROM users WHERE id=$1 FOR UPDATE`,
			req.UserID).Scan(&isBot, &deletedAt); err != nil ||
			isBot || deletedAt != nil || domain.IsSystemUserID(req.UserID) {
			return domain.ErrPremiumRecipientInvalid
		}
		var tag pgconn.CommandTag
		var err error
		if req.EntitlementID > 0 {
			tag, err = tx.Exec(ctx, `UPDATE premium_entitlements
SET status='revoked',revoked_at=to_timestamp($3),reason=$4,updated_at=now()
WHERE id=$1 AND user_id=$2 AND source='admin' AND status='active'`, req.EntitlementID, req.UserID, req.Date, req.Reason)
		} else {
			tag, err = tx.Exec(ctx, `UPDATE premium_entitlements
SET status='revoked',revoked_at=to_timestamp($2),reason=$3,updated_at=now()
WHERE user_id=$1 AND status='active'`, req.UserID, req.Date, req.Reason)
		}
		if err != nil {
			return err
		}
		if req.EntitlementID > 0 && tag.RowsAffected() != 1 {
			return domain.ErrPremiumFormInvalid
		}
		if err := compactPremiumEntitlements(ctx, tx, req.UserID, req.Date); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO premium_audit_events
(actor_user_id,target_user_id,entitlement_id,action,command_key,reason,metadata)
VALUES($1,$2,NULLIF($3,0),'admin_revoke',$4,$5,jsonb_build_object('revoked_count',$6::bigint,'entitlement_id',$3::bigint))`,
			req.ActorUserID, req.UserID, req.EntitlementID, req.CommandKey, req.Reason, tag.RowsAffected()); err != nil {
			return err
		}
		var found bool
		user, found, err = NewUserStore(tx).ByID(ctx, req.UserID)
		if err != nil || !found {
			return domain.ErrPremiumRecipientInvalid
		}
		return nil
	})
	if err != nil && isUniqueViolation(err) {
		if replay, found, replayErr := s.loadPremiumAdminRevoke(ctx, req); replayErr != nil || found {
			return replay, replayErr
		}
	}
	return user, err
}

func (s *PremiumStore) loadPremiumAdminRevoke(
	ctx context.Context,
	req domain.PremiumAdminRevokeRequest,
) (domain.User, bool, error) {
	var targetID, entitlementID int64
	err := s.db.QueryRow(ctx, `SELECT target_user_id,COALESCE(entitlement_id,0) FROM premium_audit_events
WHERE actor_user_id=$1 AND action='admin_revoke' AND command_key=$2`,
		req.ActorUserID, req.CommandKey).Scan(&targetID, &entitlementID)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.User{}, false, nil
	}
	if err != nil {
		return domain.User{}, false, err
	}
	if targetID != req.UserID || entitlementID != req.EntitlementID {
		return domain.User{}, false, domain.ErrPremiumFormInvalid
	}
	user, found, err := NewUserStore(s.db).ByID(ctx, targetID)
	return user, found, err
}

func (s *PremiumStore) RefundPremiumPayment(ctx context.Context, req domain.PremiumRefundRequest) (
	domain.PremiumPurchaseResult, error,
) {
	req.CommandKey, req.Reason = strings.TrimSpace(req.CommandKey), strings.TrimSpace(req.Reason)
	if s == nil || s.db == nil || req.PaymentIntentID <= 0 || req.ActorUserID <= 0 ||
		req.Date <= 0 || req.CommandKey == "" || len(req.CommandKey) > 256 || len(req.Reason) > 1024 {
		return domain.PremiumPurchaseResult{}, domain.ErrPremiumPaymentNotFound
	}
	if replay, found, err := s.loadPremiumRefundReplay(ctx, req); err != nil || found {
		return replay, err
	}
	var buyerID, recipientID int64
	var debitStars bool
	if err := s.db.QueryRow(ctx, `SELECT buyer_user_id,recipient_user_id,debit_stars
FROM premium_payment_intents WHERE id=$1`,
		req.PaymentIntentID).Scan(&buyerID, &recipientID, &debitStars); err != nil {
		return domain.PremiumPurchaseResult{}, domain.ErrPremiumPaymentNotFound
	}
	if !debitStars {
		return domain.PremiumPurchaseResult{}, domain.ErrPremiumExternalRefund
	}
	var result domain.PremiumPurchaseResult
	err := withTx(ctx, s.db, "refund premium payment", func(tx pgx.Tx) error {
		if err := lockUsersForUpdate(ctx, tx, buyerID, recipientID); err != nil {
			return err
		}
		var formID, amount, planVersion int64
		var kind, status string
		var months, durationDays int
		if err := tx.QueryRow(ctx, `SELECT form_id,purchase_kind,months,duration_days,amount_stars,plan_version,status
FROM premium_payment_intents WHERE id=$1 FOR UPDATE`, req.PaymentIntentID).
			Scan(&formID, &kind, &months, &durationDays, &amount, &planVersion, &status); err != nil {
			return domain.ErrPremiumPaymentNotFound
		}
		if domain.PremiumPaymentStatus(status) == domain.PremiumPaymentRefunded {
			return domain.ErrPremiumAlreadyRefunded
		}
		if domain.PremiumPaymentStatus(status) != domain.PremiumPaymentPaid {
			return domain.ErrPremiumPaymentNotFound
		}
		entitlement, found, err := premiumEntitlementByPayment(ctx, tx, req.PaymentIntentID)
		if err != nil || !found {
			return domain.ErrPremiumPaymentNotFound
		}
		var balance int64
		var granted bool
		if err := tx.QueryRow(ctx, `INSERT INTO stars_balances(user_id,balance,updated_at)
VALUES($1,$2,now()) ON CONFLICT(user_id) DO UPDATE
SET balance=stars_balances.balance+EXCLUDED.balance,updated_at=now()
RETURNING balance,granted`, buyerID, amount).Scan(&balance, &granted); err != nil {
			return err
		}
		var refundTransactionID int64
		if err := tx.QueryRow(ctx, `INSERT INTO stars_transactions
(user_id,peer_type,peer_id,amount,reason,title,description,date,premium_recipient_user_id,premium_months)
VALUES($1,'user',$2,$3,$4,'Telegram Premium refund',$5,$6,$7,$8) RETURNING id`,
			buyerID, s.botID, amount, string(domain.StarsReasonPremium), req.Reason,
			req.Date, recipientID, months).Scan(&refundTransactionID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE premium_entitlements
SET status='refunded',refunded_at=to_timestamp($2),reason=$3,updated_at=now()
WHERE id=$1`, entitlement.ID, req.Date, req.Reason); err != nil {
			return err
		}
		if err := compactPremiumEntitlements(ctx, tx, recipientID, req.Date); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE premium_payment_intents
SET status='refunded',refunded_at=to_timestamp($2),updated_at=now() WHERE id=$1`,
			req.PaymentIntentID, req.Date); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO premium_audit_events
(actor_user_id,target_user_id,payment_intent_id,entitlement_id,action,command_key,reason,
metadata) VALUES($1,$2,$3,$4,'refund',$5,$6,jsonb_build_object(
	'amount_stars',$7::bigint,'refund_transaction_id',$8::bigint))`,
			req.ActorUserID, recipientID, req.PaymentIntentID, entitlement.ID,
			req.CommandKey, req.Reason, amount, refundTransactionID); err != nil {
			return err
		}
		user, found, err := NewUserStore(tx).ByID(ctx, recipientID)
		if err != nil || !found {
			return domain.ErrPremiumRecipientInvalid
		}
		entitlement.Status = domain.PremiumEntitlementRefunded
		result = domain.PremiumPurchaseResult{
			Form: domain.PremiumPaymentForm{ID: formID, BuyerUserID: buyerID,
				Kind: domain.PremiumPurchaseKind(kind), RecipientUserID: recipientID,
				Months: months, DurationDays: durationDays, AmountStars: amount, PlanVersion: planVersion},
			Entitlement: entitlement, User: user,
			Balance: domain.StarsBalance{UserID: buyerID, Balance: balance, Granted: granted},
		}
		return nil
	})
	if errors.Is(err, domain.ErrPremiumAlreadyRefunded) {
		if replay, found, replayErr := s.loadPremiumRefundReplay(ctx, req); replayErr != nil || found {
			return replay, replayErr
		}
	}
	return result, err
}

func compactPremiumEntitlements(ctx context.Context, tx pgx.Tx, userID int64, now int) error {
	rows, err := tx.Query(ctx, `SELECT id,
EXTRACT(EPOCH FROM starts_at)::bigint,EXTRACT(EPOCH FROM expires_at)::bigint,duration_days
FROM premium_entitlements
WHERE user_id=$1 AND status='active' AND expires_at>to_timestamp($2)
ORDER BY starts_at,id FOR UPDATE`, userID, now)
	if err != nil {
		return err
	}
	type window struct {
		id              int64
		starts, expires int
		durationDays    int
	}
	windows := make([]window, 0)
	for rows.Next() {
		var item window
		var starts, expires int64
		if err := rows.Scan(&item.id, &starts, &expires, &item.durationDays); err != nil {
			rows.Close()
			return err
		}
		item.starts, item.expires = int(starts), int(expires)
		windows = append(windows, item)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	cursor := now
	for _, item := range windows {
		seconds := item.durationDays * 24 * 60 * 60
		if item.starts <= now {
			seconds = item.expires - now
		}
		if seconds <= 0 {
			continue
		}
		next := cursor + seconds
		if _, err := tx.Exec(ctx, `UPDATE premium_entitlements
SET starts_at=to_timestamp($2),expires_at=to_timestamp($3),updated_at=now() WHERE id=$1`,
			item.id, cursor, next); err != nil {
			return err
		}
		cursor = next
	}
	if cursor <= now {
		_, err = tx.Exec(ctx, `UPDATE users SET premium_expires_at=NULL,
premium_updated_at=to_timestamp($2),updated_at=now() WHERE id=$1`, userID, now)
	} else {
		_, err = tx.Exec(ctx, `UPDATE users SET premium_expires_at=to_timestamp($2),
premium_updated_at=to_timestamp($3),updated_at=now()
WHERE id=$1`, userID, cursor, now)
	}
	return err
}

func (s *PremiumStore) loadPremiumRefundReplay(
	ctx context.Context,
	req domain.PremiumRefundRequest,
) (domain.PremiumPurchaseResult, bool, error) {
	var paymentID int64
	err := s.db.QueryRow(ctx, `SELECT payment_intent_id FROM premium_audit_events
WHERE actor_user_id=$1 AND action='refund' AND command_key=$2`,
		req.ActorUserID, req.CommandKey).Scan(&paymentID)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.PremiumPurchaseResult{}, false, nil
	}
	if err != nil {
		return domain.PremiumPurchaseResult{}, false, err
	}
	if paymentID != req.PaymentIntentID {
		return domain.PremiumPurchaseResult{}, false, domain.ErrPremiumFormInvalid
	}
	var result domain.PremiumPurchaseResult
	var kind string
	err = s.db.QueryRow(ctx, `SELECT form_id,buyer_user_id,purchase_kind,recipient_user_id,
months,duration_days,amount_stars,plan_version
FROM premium_payment_intents WHERE id=$1 AND status='refunded'`, paymentID).
		Scan(&result.Form.ID, &result.Form.BuyerUserID, &kind, &result.Form.RecipientUserID,
			&result.Form.Months, &result.Form.DurationDays, &result.Form.AmountStars,
			&result.Form.PlanVersion)
	if err != nil {
		return domain.PremiumPurchaseResult{}, false, err
	}
	result.Form.Kind = domain.PremiumPurchaseKind(kind)
	entitlement, found, err := premiumEntitlementByPayment(ctx, s.db, paymentID)
	if err != nil {
		return domain.PremiumPurchaseResult{}, false, err
	}
	if !found {
		return domain.PremiumPurchaseResult{}, false, domain.ErrPremiumPaymentNotFound
	}
	result.Entitlement = entitlement
	result.User, found, err = NewUserStore(s.db).ByID(ctx, result.Form.RecipientUserID)
	if err != nil {
		return domain.PremiumPurchaseResult{}, false, err
	}
	if !found {
		return domain.PremiumPurchaseResult{}, false, domain.ErrPremiumRecipientInvalid
	}
	result.Balance, err = NewStarsStore(s.db).GetBalance(ctx, result.Form.BuyerUserID)
	if err != nil {
		return domain.PremiumPurchaseResult{}, false, err
	}
	result.Duplicate = true
	return result, true, nil
}

var _ interface {
	Plans(context.Context) ([]domain.PremiumPlan, error)
} = (*PremiumStore)(nil)
