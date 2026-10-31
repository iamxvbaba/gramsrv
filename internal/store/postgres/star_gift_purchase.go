package postgres

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"telesrv/internal/domain"
	"telesrv/internal/store/postgres/sqlcgen"
)

func (s *StarGiftLifecycleStore) IssueStarGiftPurchaseForm(ctx context.Context, form domain.StarGiftPurchaseForm) (domain.StarGiftPurchaseForm, error) {
	if s == nil || s.db == nil || form.FormID != 0 || form.BuyerUserID <= 0 || !validLifecyclePeer(form.To) ||
		form.GiftID <= 0 || form.RevisionID <= 0 || form.ChargeStars <= 0 || form.IssuedAt <= 0 ||
		form.ExpiresAt != form.IssuedAt+600 || !(domain.PremiumGiftMessage{Text: form.Message, Entities: form.MessageEntities}).Valid() {
		return domain.StarGiftPurchaseForm{}, domain.ErrStarGiftFormPurposeInvalid
	}
	entitiesJSON, err := encodeMessageEntities(form.MessageEntities)
	if err != nil {
		return domain.StarGiftPurchaseForm{}, domain.ErrStarGiftFormPurposeInvalid
	}
	for attempt := 0; attempt < 8; attempt++ {
		var raw [8]byte
		if _, err := rand.Read(raw[:]); err != nil {
			return domain.StarGiftPurchaseForm{}, fmt.Errorf("generate star gift form id: %w", err)
		}
		form.FormID = int64(binary.LittleEndian.Uint64(raw[:]) & 0x7fffffffffffffff)
		if form.FormID == 0 {
			form.FormID = 1
		}
		_, err := s.db.Exec(ctx, `INSERT INTO star_gift_purchase_forms(buyer_user_id,form_id,gift_id,revision_id,
recipient_peer_type,recipient_peer_id,include_upgrade,hide_name,message,message_entities,charge_stars,issued_at,expires_at)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`, form.BuyerUserID, form.FormID, form.GiftID, form.RevisionID,
			string(form.To.Type), form.To.ID, form.IncludeUpgrade, form.HideName, form.Message, entitiesJSON, form.ChargeStars, form.IssuedAt, form.ExpiresAt)
		if err == nil {
			return form, nil
		}
		if !isUniqueViolation(err) {
			return domain.StarGiftPurchaseForm{}, err
		}
	}
	return domain.StarGiftPurchaseForm{}, domain.ErrStarGiftUnavailable
}

func (s *StarGiftLifecycleStore) ValidateStarGiftPurchaseForm(ctx context.Context, req domain.StarGiftPurchaseRequest) error {
	if s == nil || s.db == nil {
		return domain.ErrStarGiftUnavailable
	}
	return validateStarGiftPurchaseForm(ctx, s.db, req, false)
}

func validateStarGiftPurchaseForm(ctx context.Context, db sqlcgen.DBTX, req domain.StarGiftPurchaseRequest, lock bool) error {
	if req.BuyerUserID <= 0 || req.FormID == 0 || req.Date <= 0 {
		return domain.ErrStarGiftFormExpired
	}
	query := `SELECT gift_id,revision_id,recipient_peer_type,recipient_peer_id,include_upgrade,hide_name,message,message_entities::text,
charge_stars,issued_at,expires_at FROM star_gift_purchase_forms WHERE buyer_user_id=$1 AND form_id=$2`
	if lock {
		query += ` FOR UPDATE`
	}
	var form domain.StarGiftPurchaseForm
	var peerType string
	var entitiesJSON string
	err := db.QueryRow(ctx, query, req.BuyerUserID, req.FormID).Scan(&form.GiftID, &form.RevisionID, &peerType, &form.To.ID,
		&form.IncludeUpgrade, &form.HideName, &form.Message, &entitiesJSON, &form.ChargeStars, &form.IssuedAt, &form.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ErrStarGiftFormExpired
	}
	if err != nil {
		return err
	}
	form.FormID, form.BuyerUserID, form.To.Type = req.FormID, req.BuyerUserID, domain.PeerType(peerType)
	form.MessageEntities, err = decodeMessageEntities(entitiesJSON)
	if err != nil {
		return domain.ErrStarGiftFormPurposeInvalid
	}
	if form.ExpiresAt < req.Date {
		return domain.ErrStarGiftFormExpired
	}
	if form.To != req.To || form.GiftID != req.GiftID || form.IncludeUpgrade != req.IncludeUpgrade ||
		form.HideName != req.HideName || form.Message != req.Message || !slices.Equal(form.MessageEntities, req.MessageEntities) {
		return domain.ErrStarGiftFormPurposeInvalid
	}
	if form.RevisionID != req.RevisionID || form.ChargeStars != req.ChargeStars {
		return domain.ErrStarGiftFormAmountMismatch
	}
	return nil
}

// validateStarGiftPurchaseRecipient rejects a star gift destined for a frozen
// account inside the send transaction. The projection-level Deleted check alone
// has a check-then-send TOCTOU: the RPC reads the freeze fact, then the freeze
// commits before the gift message/ledger write. FOR SHARE serializes against the
// freeze upsert (account_restrictions.user_id is its PK), so an in-flight freeze
// resolves before we commit. Never-frozen accounts have no row and pass.
func validateStarGiftPurchaseRecipient(ctx context.Context, tx pgx.Tx, to domain.Peer) error {
	if to.Type != domain.PeerTypeUser || to.ID <= 0 {
		return nil
	}
	var frozen bool
	err := tx.QueryRow(ctx, `SELECT frozen FROM account_restrictions WHERE user_id = $1 FOR SHARE`, to.ID).Scan(&frozen)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("check star gift recipient restrictions: %w", err)
	}
	if frozen {
		return domain.ErrStarGiftRecipientUnavailable
	}
	return nil
}

// SettledStarGiftPurchase reports the purchase already committed for req.CommandKey.
// It is the read half of the payment-command table that makes a bot-initiated
// sendGift idempotent: PurchaseStarGift's own replay check demands the same form_id,
// and a Bot API retry always mints a fresh form, so the retry would be reported as a
// failure instead of the success it already achieved.
//
// found=false means the key is unused, so the caller must go through the normal
// issue-and-purchase path. A committed command that disagrees with the request on
// gift, recipient, upgrade or text returns ErrStarGiftIdempotencyConflict rather
// than the other request's result: answering true would tell the caller its own,
// different send succeeded.
func (s *StarGiftLifecycleStore) SettledStarGiftPurchase(ctx context.Context, req domain.StarGiftPurchaseRequest) (domain.StarGiftPurchaseResult, bool, error) {
	if s == nil || s.db == nil || req.BuyerUserID <= 0 || !validLifecyclePeer(req.To) ||
		req.GiftID <= 0 || strings.TrimSpace(req.CommandKey) == "" {
		return domain.StarGiftPurchaseResult{}, false, domain.ErrStarGiftInvalid
	}
	var giftID, savedID, balance, revisionID int64
	var recipientType, message, entitiesJSON string
	var recipientID int64
	var hideName, includeUpgrade bool
	err := s.db.QueryRow(ctx, `SELECT c.gift_id,c.recipient_peer_type,c.recipient_peer_id,c.saved_gift_id,c.balance_after,
f.revision_id,f.hide_name,f.include_upgrade,f.message,f.message_entities::text
FROM star_gift_purchase_commands c JOIN star_gift_purchase_forms f ON f.buyer_user_id=c.buyer_user_id AND f.form_id=c.form_id
WHERE c.buyer_user_id=$1 AND c.command_key=$2`, req.BuyerUserID, req.CommandKey).
		Scan(&giftID, &recipientType, &recipientID, &savedID, &balance, &revisionID, &hideName, &includeUpgrade, &message, &entitiesJSON)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.StarGiftPurchaseResult{}, false, nil
	}
	if err != nil {
		return domain.StarGiftPurchaseResult{}, false, err
	}
	entities, err := decodeMessageEntities(entitiesJSON)
	if err != nil {
		return domain.StarGiftPurchaseResult{}, false, err
	}
	if giftID != req.GiftID || recipientType != string(req.To.Type) || recipientID != req.To.ID ||
		hideName != req.HideName || includeUpgrade != req.IncludeUpgrade || message != req.Message || !slices.Equal(entities, req.MessageEntities) {
		return domain.StarGiftPurchaseResult{}, false, domain.ErrStarGiftIdempotencyConflict
	}
	saved, found, err := savedStarGiftByID(ctx, s.db, savedID)
	if err != nil {
		return domain.StarGiftPurchaseResult{}, false, err
	}
	if !found {
		return domain.StarGiftPurchaseResult{}, false, domain.ErrStarGiftInvalid
	}
	gift, found, err := NewStarGiftStore(s.db).CatalogRevision(ctx, revisionID)
	if err != nil {
		return domain.StarGiftPurchaseResult{}, false, err
	}
	if !found {
		return domain.StarGiftPurchaseResult{}, false, domain.ErrStarGiftInvalid
	}
	return domain.StarGiftPurchaseResult{Gift: gift, Saved: saved,
		Balance: domain.StarsBalance{UserID: req.BuyerUserID, Balance: balance}, Duplicate: true}, true, nil
}

func (s *StarGiftLifecycleStore) PurchaseStarGift(ctx context.Context, req domain.StarGiftPurchaseRequest) (domain.StarGiftPurchaseResult, error) {
	req.CommandKey = strings.TrimSpace(req.CommandKey)
	if s == nil || s.db == nil || req.BuyerUserID <= 0 || !validLifecyclePeer(req.To) || req.GiftID <= 0 ||
		req.FormID == 0 || req.CommandKey == "" || len(req.CommandKey) > 256 || req.Date <= 0 ||
		!(domain.PremiumGiftMessage{Text: req.Message, Entities: req.MessageEntities}).Valid() {
		return domain.StarGiftPurchaseResult{}, domain.ErrStarGiftInvalid
	}
	if replay, found, err := s.loadStarGiftPurchaseReplay(ctx, req, domain.SendPrivateTextResult{}); err != nil || found {
		return replay, err
	}
	if err := s.ValidateStarGiftPurchaseForm(ctx, req); err != nil {
		return domain.StarGiftPurchaseResult{}, err
	}
	if req.To.Type == domain.PeerTypeChannel {
		return s.purchaseStarGiftToChannel(ctx, req)
	}
	if s.messages == nil {
		return domain.StarGiftPurchaseResult{}, domain.ErrStarGiftUnavailable
	}
	fingerprint := starGiftPurchaseFingerprint(req)
	messageReq := domain.SendPrivateTextRequest{SenderUserID: req.BuyerUserID, RecipientUserID: req.To.ID,
		RandomID: lifecycleCommandRandomID("purchase", req.BuyerUserID, req.CommandKey), Date: req.Date,
		OriginAuthKeyID: req.OriginAuthKeyID, OriginSessionID: req.OriginSessionID, OriginUserID: req.BuyerUserID,
		RecipientBlocked: req.RecipientBlocked, IdempotencyFingerprint: fingerprint[:],
		Media: &domain.MessageMedia{Kind: domain.MessageMediaKindService, ServiceAction: &domain.MessageServiceAction{
			Kind: domain.MessageServiceActionStarGift, StarGift: &domain.MessageStarGiftAction{Saved: true}}}}
	var result domain.StarGiftPurchaseResult
	hooks := privateSendTxHooks{
		before: func(ctx context.Context, tx pgx.Tx, send *domain.SendPrivateTextRequest) error {
			if err := validateStarGiftPurchaseRecipient(ctx, tx, req.To); err != nil {
				return err
			}
			if err := validateStarGiftPurchaseForm(ctx, tx, req, true); err != nil {
				return err
			}
			gift, saved, balance, err := s.prepareStarGiftPurchase(ctx, tx, req)
			if err != nil {
				return err
			}
			sticker := gift.Sticker
			send.Media = &domain.MessageMedia{Kind: domain.MessageMediaKindService, ServiceAction: &domain.MessageServiceAction{
				Kind: domain.MessageServiceActionStarGift, StarGift: &domain.MessageStarGiftAction{GiftID: gift.ID,
					Stars: gift.Stars, ConvertStars: saved.ConvertStars, Title: gift.Title, Sticker: &sticker,
					Message: req.Message, MessageEntities: append([]domain.MessageEntity(nil), req.MessageEntities...),
					FromUserID: req.BuyerUserID, PeerUserID: req.To.ID, To: req.To, NameHidden: req.HideName, Saved: true,
					CanUpgrade: gift.UpgradeStars > 0, PrepaidUpgrade: saved.PrepaidUpgradeStars > 0,
					PrepaidUpgradeHash: saved.PrepaidUpgradeHash, UpgradePriceStars: gift.UpgradeStars,
					UpgradeStars: saved.PrepaidUpgradeStars}}}
			result.Gift, result.Saved, result.Balance = gift, saved, balance
			return nil
		},
		projectMedia: projectPrivateStarGiftPurchase,
		after: func(ctx context.Context, tx pgx.Tx, sent domain.SendPrivateTextResult) error {
			msgID := sent.RecipientMessage.ID
			if msgID <= 0 {
				msgID = sent.SenderMessage.ID
			}
			result.Saved.MsgID = msgID
			id, err := NewStarGiftStore(tx).Create(ctx, result.Saved)
			if err != nil {
				return err
			}
			result.Saved.ID = id
			return s.insertStarGiftPurchaseCommand(ctx, tx, req, result.Saved.ID, result.Gift.Stars+result.Saved.PrepaidUpgradeStars, result.Balance.Balance)
		},
	}
	sent, err := s.messages.sendPrivateTextWithHooks(ctx, messageReq, hooks)
	if err != nil {
		if isUniqueViolation(err) {
			if replay, found, replayErr := s.loadStarGiftPurchaseReplay(ctx, req, sent); replayErr != nil || found {
				return replay, replayErr
			}
		}
		return domain.StarGiftPurchaseResult{}, err
	}
	result.Send, result.Duplicate = sent, sent.Duplicate
	if sent.Duplicate {
		replay, _, replayErr := s.loadStarGiftPurchaseReplay(ctx, req, sent)
		return replay, replayErr
	}
	return result, nil
}

func (s *StarGiftLifecycleStore) purchaseStarGiftToChannel(ctx context.Context, req domain.StarGiftPurchaseRequest) (domain.StarGiftPurchaseResult, error) {
	var result domain.StarGiftPurchaseResult
	err := withTx(ctx, s.db, "purchase star gift for channel", func(tx pgx.Tx) error {
		if err := validateStarGiftPurchaseForm(ctx, tx, req, true); err != nil {
			return err
		}
		gift, saved, balance, err := s.prepareStarGiftPurchase(ctx, tx, req)
		if err != nil {
			return err
		}
		id, err := NewStarGiftStore(tx).Create(ctx, saved)
		if err != nil {
			return err
		}
		saved.ID, saved.SavedID = id, id
		sticker := gift.Sticker
		action := domain.ChannelMessageAction{Type: domain.ChannelActionStarGift, StarGift: &domain.MessageStarGiftAction{
			GiftID: gift.ID, Stars: gift.Stars, ConvertStars: saved.ConvertStars, Title: gift.Title,
			Sticker: &sticker, Message: saved.Message, MessageEntities: append([]domain.MessageEntity(nil), saved.MessageEntities...),
			FromUserID: req.BuyerUserID, PeerChannelID: req.To.ID,
			SavedID: id, NameHidden: saved.NameHidden, Saved: true, CanUpgrade: gift.UpgradeStars > 0,
			PrepaidUpgrade: saved.PrepaidUpgradeStars > 0, PrepaidUpgradeHash: saved.PrepaidUpgradeHash,
			UpgradePriceStars: gift.UpgradeStars, UpgradeStars: saved.PrepaidUpgradeStars,
		}}
		if err := NewChannelStore(tx).appendStarGiftAdminLogTx(ctx, tx, req.To.ID, req.BuyerUserID, id, req.Date, action); err != nil {
			return err
		}
		if err := enqueueChannelStarGiftNotifications(ctx, tx, id, req.To.ID, req.Date, action.StarGift); err != nil {
			return err
		}
		if err := s.insertStarGiftPurchaseCommand(ctx, tx, req, id, gift.Stars+saved.PrepaidUpgradeStars, balance.Balance); err != nil {
			return err
		}
		result = domain.StarGiftPurchaseResult{Gift: gift, Saved: saved, Balance: balance}
		return nil
	})
	if err != nil {
		if isUniqueViolation(err) {
			if replay, found, replayErr := s.loadStarGiftPurchaseReplay(ctx, req, domain.SendPrivateTextResult{}); replayErr != nil || found {
				return replay, replayErr
			}
		}
		return domain.StarGiftPurchaseResult{}, err
	}
	// The purchase remains successful once its transaction has committed. Any
	// immediate delivery failure leaves a durable job for the lifecycle sweeper.
	_, _ = s.dispatchChannelStarGiftNotifications(ctx, req.Date, maxChannelStarGiftNotificationRecipients, result.Saved.ID)
	return result, nil
}

func (s *StarGiftLifecycleStore) prepareStarGiftPurchase(ctx context.Context, tx pgx.Tx, req domain.StarGiftPurchaseRequest) (domain.StarGift, domain.SavedStarGift, domain.StarsBalance, error) {
	var revisionID int64
	var enabled bool
	var remains int
	if err := tx.QueryRow(ctx, `SELECT active_revision_id,enabled,availability_remains FROM star_gift_catalog WHERE gift_id=$1 FOR UPDATE`, req.GiftID).
		Scan(&revisionID, &enabled, &remains); err != nil {
		return domain.StarGift{}, domain.SavedStarGift{}, domain.StarsBalance{}, domain.ErrStarGiftInvalid
	}
	gift, found, err := NewStarGiftStore(tx).CatalogRevision(ctx, revisionID)
	if err != nil || !found || !enabled || gift.ID != req.GiftID || gift.SoldOut || gift.Auction || gift.LockedUntilDate > req.Date ||
		gift.Limited && remains <= 0 {
		return domain.StarGift{}, domain.SavedStarGift{}, domain.StarsBalance{}, domain.ErrStarGiftInvalid
	}
	if gift.RevisionID != req.RevisionID {
		return domain.StarGift{}, domain.SavedStarGift{}, domain.StarsBalance{}, domain.ErrStarGiftFormAmountMismatch
	}
	if gift.RequirePremium && !req.BuyerPremium {
		return domain.StarGift{}, domain.SavedStarGift{}, domain.StarsBalance{}, domain.ErrPremiumRequired
	}
	gift.AvailabilityRemains = remains
	upgradePrice := int64(0)
	prepayHash := ""
	if gift.UpgradeStars > 0 || req.IncludeUpgrade {
		revision, err := lockActiveCollectibleRevision(ctx, tx, gift.ID)
		if err != nil || revision.Issued >= revision.SupplyTotal {
			if req.IncludeUpgrade {
				return domain.StarGift{}, domain.SavedStarGift{}, domain.StarsBalance{}, domain.ErrStarGiftCollectibleUnavailable
			}
		} else if req.IncludeUpgrade {
			upgradePrice = revision.UpgradeStars
		} else {
			var token [32]byte
			if _, err := rand.Read(token[:]); err != nil {
				return domain.StarGift{}, domain.SavedStarGift{}, domain.StarsBalance{}, err
			}
			prepayHash = base64.RawURLEncoding.EncodeToString(token[:])
		}
	}
	if req.IncludeUpgrade && upgradePrice <= 0 {
		return domain.StarGift{}, domain.SavedStarGift{}, domain.StarsBalance{}, domain.ErrStarGiftCollectibleUnavailable
	}
	if gift.Stars+upgradePrice != req.ChargeStars {
		return domain.StarGift{}, domain.SavedStarGift{}, domain.StarsBalance{}, domain.ErrStarGiftFormAmountMismatch
	}
	var purchased int
	if err := tx.QueryRow(ctx, `INSERT INTO star_gift_user_purchases(user_id,gift_id,purchased_count) VALUES($1,$2,1)
ON CONFLICT(user_id,gift_id) DO UPDATE SET purchased_count=star_gift_user_purchases.purchased_count+1,updated_at=now()
WHERE NOT $3 OR star_gift_user_purchases.purchased_count<$4 RETURNING purchased_count`, req.BuyerUserID, gift.ID,
		gift.LimitedPerUser, gift.PerUserTotal).Scan(&purchased); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.StarGift{}, domain.SavedStarGift{}, domain.StarsBalance{}, domain.ErrStarGiftUnavailable
		}
		return domain.StarGift{}, domain.SavedStarGift{}, domain.StarsBalance{}, err
	}
	if gift.Limited {
		var remains int
		if err := tx.QueryRow(ctx, `UPDATE star_gift_catalog SET availability_remains=availability_remains-1,
first_sale_date=CASE WHEN first_sale_date=0 THEN $2 ELSE first_sale_date END,last_sale_date=$2,updated_at=now()
WHERE gift_id=$1 AND availability_remains>0 RETURNING availability_remains`, gift.ID, req.Date).Scan(&remains); err != nil {
			return domain.StarGift{}, domain.SavedStarGift{}, domain.StarsBalance{}, domain.ErrStarGiftUnavailable
		}
		if remains == 0 {
			if err := s.markCatalogSoldOutTx(ctx, tx, gift.ID); err != nil {
				return domain.StarGift{}, domain.SavedStarGift{}, domain.StarsBalance{}, err
			}
		}
	} else if _, err := tx.Exec(ctx, `UPDATE star_gift_catalog SET first_sale_date=CASE WHEN first_sale_date=0 THEN $2 ELSE first_sale_date END,
last_sale_date=$2,updated_at=now() WHERE gift_id=$1`, gift.ID, req.Date); err != nil {
		return domain.StarGift{}, domain.SavedStarGift{}, domain.StarsBalance{}, err
	}
	charge := gift.Stars + upgradePrice
	balance, err := s.debitStarGiftPurchase(ctx, tx, req, charge)
	if err != nil {
		return domain.StarGift{}, domain.SavedStarGift{}, domain.StarsBalance{}, err
	}
	// Gifts sent by a bot do not take part in "convert back to Stars": the catalog's ConvertStars is a
	// refund allowance for ordinary user buyers, whereas the bot's funds are the wallet balance it
	// earned from invoicing. Copying it would let the receiving peer redeem the Stars the bot spent
	// straight back to their own balance, effectively opening a withdrawal path into the wallet.
	// Here it is zeroed by payer identity: peer_star_gifts.convert_stars is 0, so the client does not
	// show the convert button and ConvertStarGift only takes the amount==0 pure-archive branch, producing no Credit.
	convertStars := gift.ConvertStars
	if req.BuyerIsBot {
		convertStars = 0
	}
	saved := domain.SavedStarGift{Owner: req.To, FromUserID: req.BuyerUserID, GiftID: gift.ID, RevisionID: gift.RevisionID,
		Date: req.Date, NameHidden: req.HideName, ConvertStars: convertStars, PrepaidUpgradeStars: upgradePrice,
		PrepaidUpgradeHash: prepayHash, Message: req.Message,
		MessageEntities: append([]domain.MessageEntity(nil), req.MessageEntities...), Unsaved: req.RecipientUnsaved}
	return gift, saved, balance, nil
}

// markCatalogSoldOutTx flips sold_out=true on the active revision once a
// limited gift's inventory is exhausted. sold_out, first_sale_date and
// last_sale_date share the TL flag, and the RPC projection only exposes the
// sale timestamps behind the sold-out flag, so the revision must reflect an
// exhausted limited gift for clients to render its first/last sale dates.
func (s *StarGiftLifecycleStore) markCatalogSoldOutTx(ctx context.Context, tx pgx.Tx, giftID int64) error {
	_, err := tx.Exec(ctx, `UPDATE star_gift_catalog_revisions r SET sold_out=true
FROM star_gift_catalog c
WHERE c.gift_id=$1 AND c.active_revision_id=r.id AND r.limited AND NOT r.sold_out`, giftID)
	return err
}

func (s *StarGiftLifecycleStore) insertStarGiftPurchaseCommand(ctx context.Context, tx pgx.Tx, req domain.StarGiftPurchaseRequest, savedID, charge, balance int64) error {
	_, err := tx.Exec(ctx, `INSERT INTO star_gift_purchase_commands(buyer_user_id,command_key,gift_id,recipient_peer_type,
recipient_peer_id,saved_gift_id,form_id,charge_stars,balance_after,created_at)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, req.BuyerUserID, req.CommandKey, req.GiftID, string(req.To.Type), req.To.ID,
		savedID, req.FormID, charge, balance, req.Date)
	return err
}
func (s *StarGiftLifecycleStore) loadStarGiftPurchaseReplay(ctx context.Context, req domain.StarGiftPurchaseRequest, sent domain.SendPrivateTextResult) (domain.StarGiftPurchaseResult, bool, error) {
	var giftID, recipientID, savedID, formID, charge, balance int64
	var recipientType string
	err := s.db.QueryRow(ctx, `SELECT gift_id,recipient_peer_type,recipient_peer_id,saved_gift_id,form_id,charge_stars,balance_after
FROM star_gift_purchase_commands WHERE buyer_user_id=$1 AND command_key=$2`, req.BuyerUserID, req.CommandKey).
		Scan(&giftID, &recipientType, &recipientID, &savedID, &formID, &charge, &balance)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.StarGiftPurchaseResult{}, false, nil
	}
	if err != nil {
		return domain.StarGiftPurchaseResult{}, false, err
	}
	if giftID != req.GiftID || recipientType != string(req.To.Type) || recipientID != req.To.ID || formID != req.FormID || charge <= 0 {
		return domain.StarGiftPurchaseResult{}, false, domain.ErrStarGiftInvalid
	}
	saved, found, err := savedStarGiftByID(ctx, s.db, savedID)
	if err != nil || !found {
		return domain.StarGiftPurchaseResult{}, false, domain.ErrStarGiftInvalid
	}
	if saved.Owner != req.To || saved.GiftID != req.GiftID || saved.NameHidden != req.HideName || saved.Message != req.Message ||
		!slices.Equal(saved.MessageEntities, req.MessageEntities) ||
		(saved.PrepaidUpgradeStars > 0) != req.IncludeUpgrade {
		return domain.StarGiftPurchaseResult{}, false, domain.ErrStarGiftInvalid
	}
	gift, found, err := NewStarGiftStore(s.db).CatalogRevision(ctx, saved.RevisionID)
	if err != nil || !found {
		return domain.StarGiftPurchaseResult{}, false, domain.ErrStarGiftInvalid
	}
	if req.To.Type == domain.PeerTypeUser && sent.SenderMessage.ID == 0 {
		if s.messages == nil {
			return domain.StarGiftPurchaseResult{}, false, domain.ErrStarGiftUnavailable
		}
		fingerprint := starGiftPurchaseFingerprint(req)
		replay, replayFound, replayErr := s.messages.LookupPrivateSendReplay(ctx, domain.PrivateSendReplayRequest{
			SenderUserID: req.BuyerUserID, RecipientUserID: req.To.ID,
			RandomID: lifecycleCommandRandomID("purchase", req.BuyerUserID, req.CommandKey), IdempotencyFingerprint: fingerprint[:],
		})
		if replayErr != nil || !replayFound {
			if replayErr != nil {
				return domain.StarGiftPurchaseResult{}, false, replayErr
			}
			return domain.StarGiftPurchaseResult{}, false, domain.ErrStarGiftInvalid
		}
		sent = replay
	}
	return domain.StarGiftPurchaseResult{Gift: gift, Saved: saved, Balance: domain.StarsBalance{UserID: req.BuyerUserID, Balance: balance},
		Send: sent, Duplicate: true}, true, nil
}

func starGiftPurchaseFingerprint(req domain.StarGiftPurchaseRequest) [32]byte {
	entitiesJSON, _ := encodeMessageEntities(req.MessageEntities)
	return sha256.Sum256([]byte(fmt.Sprintf("telesrv:star-gift-purchase:v2:%d:%s:%d:%d:%t:%t:%s:%s",
		req.BuyerUserID, req.To.Type, req.To.ID, req.GiftID, req.IncludeUpgrade, req.HideName, req.Message, entitiesJSON)))
}

func (s *StarGiftLifecycleStore) debitStarGiftPurchase(ctx context.Context, tx pgx.Tx, req domain.StarGiftPurchaseRequest, charge int64) (domain.StarsBalance, error) {
	if req.BuyerIsBot {
		balance, err := debitBotStarsWallet(ctx, tx, req.BuyerUserID, charge, domain.StarsReasonBotSpend, req.To, req.Date)
		if err != nil {
			return domain.StarsBalance{}, err
		}
		// Granted is a personal-ledger concept (the starting-grant top-up); a bot
		// wallet has no such grant, so it stays false rather than inventing one.
		return domain.StarsBalance{UserID: req.BuyerUserID, Balance: balance}, nil
	}
	return s.debitLifecycleAmount(ctx, tx, req.BuyerUserID,
		domain.StarGiftAmount{Currency: domain.StarGiftCurrencyStars, Amount: charge}, domain.StarsReasonGift,
		req.To, req.Date, "Star gift")
}

func debitBotStarsWallet(ctx context.Context, tx pgx.Tx, botUserID, amount int64,
	reason domain.StarsTransactionReason, peer domain.Peer, date int) (int64, error) {
	var balance int64
	if err := tx.QueryRow(ctx, `SELECT balance FROM bot_stars_balances WHERE bot_user_id=$1 FOR UPDATE`,
		botUserID).Scan(&balance); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// A wallet that was never credited is an empty wallet, not a broken one.
			return 0, domain.ErrStarsInsufficient
		}
		return 0, err
	}
	if balance < amount {
		return 0, domain.ErrStarsInsufficient
	}
	balance -= amount
	if _, err := tx.Exec(ctx, `UPDATE bot_stars_balances SET balance=$2,updated_at=now() WHERE bot_user_id=$1`,
		botUserID, balance); err != nil {
		return 0, err
	}
	// actor_user_id is the bot itself: there is no separate spending principal, and
	// the wallet CHECK requires a positive actor. peer is the gift recipient.
	//
	// Unlike stars_transactions, the wallet columns are NOT NULL, so an absent peer
	// is written as the empty pair rather than NULL.
	peerType, peerID := "", int64(0)
	if validLifecyclePeer(peer) {
		peerType, peerID = string(peer.Type), peer.ID
	}
	if _, err := tx.Exec(ctx, `INSERT INTO bot_stars_transactions
(bot_user_id,actor_user_id,amount,reason,peer_type,peer_id,invoice_key,date)
VALUES($1,$2,$3,$4,$5,$6,NULL,$7)`,
		botUserID, botUserID, -amount, string(reason), peerType, peerID, date); err != nil {
		return 0, err
	}
	return balance, nil
}
