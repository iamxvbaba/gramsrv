package postgres

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"telesrv/internal/domain"
	"telesrv/internal/store"
	"telesrv/internal/store/postgres/sqlcgen"
)

// StarsPurchaseStore commits fiat Stars top-ups, friend gifts and giveaway
// launches. No external
// provider is contacted by this local development checkout; form binding and
// settlement idempotency are still production-shaped so retries cannot mint twice.
type StarsPurchaseStore struct {
	db       sqlcgen.DBTX
	messages *MessageStore
	channels *ChannelStore
}

func NewStarsPurchaseStore(db sqlcgen.DBTX, messages *MessageStore, channels ...*ChannelStore) *StarsPurchaseStore {
	var channelStore *ChannelStore
	if len(channels) > 0 {
		channelStore = channels[0]
	}
	return &StarsPurchaseStore{db: db, messages: messages, channels: channelStore}
}

func (s *StarsPurchaseStore) IssueStarsPurchaseForm(ctx context.Context, form domain.StarsPurchaseForm) (domain.StarsPurchaseForm, error) {
	if s == nil || s.db == nil || !validStarsPurchaseForm(form) {
		return domain.StarsPurchaseForm{}, domain.ErrStarsPurchaseFormInvalid
	}
	purposeJSON, err := starsPurchasePurposeJSON(form)
	if err != nil {
		return domain.StarsPurchaseForm{}, domain.ErrStarsPurchaseFormInvalid
	}
	for attempt := 0; attempt < 8; attempt++ {
		formID, err := newStarsPurchaseFormID()
		if err != nil {
			return domain.StarsPurchaseForm{}, fmt.Errorf("generate stars purchase form id: %w", err)
		}
		tag, err := s.db.Exec(ctx, `
INSERT INTO stars_purchase_forms
	(buyer_user_id,form_id,kind,recipient_user_id,spend_peer_type,spend_peer_id,purpose_json,stars,currency,amount,issued_at,expires_at)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
ON CONFLICT DO NOTHING`, form.BuyerUserID, formID, string(form.Kind), starsPurchaseRecipientValue(form.RecipientUserID),
			starsPurchasePeerTypeValue(form.SpendPurposePeer), starsPurchasePeerIDValue(form.SpendPurposePeer),
			purposeJSON, form.Stars, form.Currency, form.Amount, form.IssuedAt, form.ExpiresAt)
		if err != nil {
			return domain.StarsPurchaseForm{}, fmt.Errorf("insert stars purchase form: %w", err)
		}
		if tag.RowsAffected() == 1 {
			form.FormID = formID
			return form, nil
		}
	}
	return domain.StarsPurchaseForm{}, domain.ErrStarsGiftUnavailable
}

var errStarsPurchaseReplay = errors.New("stars purchase replay")

func (s *StarsPurchaseStore) PurchaseStarsWithDelivery(ctx context.Context, req domain.StarsPurchaseRequest, effects store.DeliveryEffectsBuilder[domain.StarsPurchaseResult]) (domain.StarsPurchaseResult, error) {
	if s == nil || s.db == nil || effects == nil || req.FormID == 0 || req.Date <= 0 || !validStarsPurchaseCommand(req.StarsPurchaseForm) {
		return domain.StarsPurchaseResult{}, domain.ErrStarsPurchaseFormInvalid
	}
	fingerprint := starsPurchaseFingerprint(req)
	if replay, found, err := s.loadStarsPurchaseReplay(ctx, req, fingerprint); err != nil || found {
		return replay, err
	}
	// A committed command is replayable after both the checkout and giveaway
	// deadlines: the provider may retry a successful submission after losing the
	// response, and a terminal campaign must not turn that exact retry into a
	// different outcome. The deadline only gates a first settlement.
	if req.Kind == domain.StarsPurchaseGiveaway && req.Giveaway.UntilDate <= req.Date {
		return domain.StarsPurchaseResult{}, domain.ErrStarsPurchaseFormExpired
	}
	switch req.Kind {
	case domain.StarsPurchaseTopup:
		return s.purchaseStarsTopup(ctx, req, fingerprint, effects)
	case domain.StarsPurchaseGiveaway:
		return s.purchaseStarsGiveaway(ctx, req, fingerprint, effects)
	}
	if s.messages == nil {
		return domain.StarsPurchaseResult{}, domain.ErrStarsPurchaseFormInvalid
	}

	transactionID := fmt.Sprintf("stars-gift:%d:%d", req.BuyerUserID, req.FormID)
	randomID := lifecycleCommandRandomID("stars-fiat-gift", req.BuyerUserID, req.FormID)
	media := &domain.MessageMedia{Kind: domain.MessageMediaKindService, ServiceAction: &domain.MessageServiceAction{
		Kind: domain.MessageServiceActionGiftStars,
		GiftStars: &domain.MessageGiftStarsAction{Currency: req.Currency, Amount: req.Amount,
			Stars: req.Stars, TransactionID: transactionID},
	}}
	messageReq := domain.SendPrivateTextRequest{
		SenderUserID: req.BuyerUserID, RecipientUserID: req.RecipientUserID,
		RandomID: randomID, Date: req.Date, Media: media,
		OriginUserID: req.BuyerUserID, OriginAuthKeyID: req.OriginAuthKeyID,
		OriginSessionID: req.OriginSessionID, IdempotencyFingerprint: fingerprint[:],
	}
	result := domain.StarsPurchaseResult{TransactionID: transactionID}
	hooks := privateSendTxHooks{
		before: func(ctx context.Context, tx pgx.Tx, send *domain.SendPrivateTextRequest) error {
			if err := validateStarsPurchaseForm(ctx, tx, req, true); err != nil {
				return err
			}
			if found, err := starsPurchaseCommandExists(ctx, tx, req.BuyerUserID, req.FormID); err != nil {
				return err
			} else if found {
				return errStarsPurchaseReplay
			}
			balance := domain.StarsBalance{UserID: req.RecipientUserID}
			if err := tx.QueryRow(ctx, `
INSERT INTO stars_balances (user_id,balance,updated_at) VALUES($1,$2,now())
ON CONFLICT (user_id) DO UPDATE
SET balance=stars_balances.balance+EXCLUDED.balance, updated_at=now()
RETURNING balance,granted`, req.RecipientUserID, req.Stars).Scan(&balance.Balance, &balance.Granted); err != nil {
				return fmt.Errorf("credit stars gift recipient: %w", err)
			}
			if err := insertStarsTxn(ctx, tx, req.RecipientUserID, req.Stars, domain.StarsReasonGift,
				domain.Peer{Type: domain.PeerTypeUser, ID: req.BuyerUserID}, req.Date,
				"Stars gift", fmt.Sprintf("%d Stars", req.Stars)); err != nil {
				return err
			}
			result.Balance = balance
			if send.Media == nil || send.Media.ServiceAction == nil || send.Media.ServiceAction.GiftStars == nil {
				return domain.ErrStarsPurchaseFormInvalid
			}
			send.Media.ServiceAction.GiftStars.BalanceAfter = balance.Balance
			return nil
		},
		after: func(ctx context.Context, tx pgx.Tx, sent domain.SendPrivateTextResult) error {
			_, err := tx.Exec(ctx, `
INSERT INTO stars_purchase_commands
	(buyer_user_id,form_id,kind,request_fingerprint,recipient_user_id,spend_peer_type,spend_peer_id,purpose_json,stars,currency,amount,
	 balance_after,transaction_id,created_at)
VALUES($1,$2,$3,$4,$5,NULL,NULL,'{}'::jsonb,$6,$7,$8,$9,$10,$11)`, req.BuyerUserID, req.FormID, string(req.Kind), fingerprint[:],
				req.RecipientUserID, req.Stars, req.Currency, req.Amount, result.Balance.Balance, transactionID, req.Date)
			if err != nil {
				return fmt.Errorf("insert stars gift purchase command: %w", err)
			}
			result.Send = sent
			return applyStarsPurchaseDeliveryEffectsTx(ctx, tx, result, effects)
		},
	}
	sent, err := s.messages.sendPrivateTextWithHooks(ctx, messageReq, hooks)
	if err != nil {
		if errors.Is(err, errStarsPurchaseReplay) {
			if replay, found, replayErr := s.loadStarsPurchaseReplay(ctx, req, fingerprint); replayErr != nil || found {
				return replay, replayErr
			}
		}
		return domain.StarsPurchaseResult{}, err
	}
	result.Send = sent
	if sent.Duplicate {
		if replay, found, replayErr := s.loadStarsPurchaseReplay(ctx, req, fingerprint); replayErr != nil || found {
			return replay, replayErr
		}
		return domain.StarsPurchaseResult{}, domain.ErrStarsPurchaseFormInvalid
	}
	return result, nil
}

func (s *StarsPurchaseStore) purchaseStarsTopup(ctx context.Context, req domain.StarsPurchaseRequest, fingerprint [32]byte, effects store.DeliveryEffectsBuilder[domain.StarsPurchaseResult]) (domain.StarsPurchaseResult, error) {
	transactionID := fmt.Sprintf("stars-topup:%d:%d", req.BuyerUserID, req.FormID)
	result := domain.StarsPurchaseResult{TransactionID: transactionID}
	err := withTx(ctx, s.db, "settle stars topup", func(tx pgx.Tx) error {
		if err := validateStarsPurchaseForm(ctx, tx, req, true); err != nil {
			return err
		}
		if found, err := starsPurchaseCommandExists(ctx, tx, req.BuyerUserID, req.FormID); err != nil {
			return err
		} else if found {
			return errStarsPurchaseReplay
		}
		result.Balance = domain.StarsBalance{UserID: req.BuyerUserID}
		if err := tx.QueryRow(ctx, `
INSERT INTO stars_balances (user_id,balance,updated_at) VALUES($1,$2,now())
ON CONFLICT (user_id) DO UPDATE
SET balance=stars_balances.balance+EXCLUDED.balance, updated_at=now()
RETURNING balance,granted`, req.BuyerUserID, req.Stars).Scan(&result.Balance.Balance, &result.Balance.Granted); err != nil {
			return fmt.Errorf("credit stars topup buyer: %w", err)
		}
		if err := insertStarsTxn(ctx, tx, req.BuyerUserID, req.Stars, domain.StarsReasonTopup,
			req.SpendPurposePeer, req.Date, "Stars top-up", "telesrv dev purchase"); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `
INSERT INTO stars_purchase_commands
	(buyer_user_id,form_id,kind,request_fingerprint,recipient_user_id,spend_peer_type,spend_peer_id,purpose_json,stars,currency,amount,
	 balance_after,transaction_id,created_at)
VALUES($1,$2,$3,$4,NULL,$5,$6,'{}'::jsonb,$7,$8,$9,$10,$11,$12)`, req.BuyerUserID, req.FormID, string(req.Kind), fingerprint[:],
			starsPurchasePeerTypeValue(req.SpendPurposePeer), starsPurchasePeerIDValue(req.SpendPurposePeer),
			req.Stars, req.Currency, req.Amount, result.Balance.Balance, transactionID, req.Date)
		if err != nil {
			return fmt.Errorf("insert stars topup command: %w", err)
		}
		return applyStarsPurchaseDeliveryEffectsTx(ctx, tx, result, effects)
	})
	if errors.Is(err, errStarsPurchaseReplay) {
		if replay, found, replayErr := s.loadStarsPurchaseReplay(ctx, req, fingerprint); replayErr != nil || found {
			return replay, replayErr
		}
	}
	if err != nil {
		return domain.StarsPurchaseResult{}, err
	}
	return result, nil
}

func (s *StarsPurchaseStore) purchaseStarsGiveaway(ctx context.Context, req domain.StarsPurchaseRequest, fingerprint [32]byte, effects store.DeliveryEffectsBuilder[domain.StarsPurchaseResult]) (domain.StarsPurchaseResult, error) {
	if s.channels == nil || req.Giveaway == nil {
		return domain.StarsPurchaseResult{}, domain.ErrStarsPurchaseFormInvalid
	}
	purposeJSON, err := starsPurchasePurposeJSON(req.StarsPurchaseForm)
	if err != nil {
		return domain.StarsPurchaseResult{}, domain.ErrStarsPurchaseFormInvalid
	}
	giveaway := *req.Giveaway
	transactionID := fmt.Sprintf("stars-giveaway:%d:%d", req.BuyerUserID, req.FormID)
	result := domain.StarsPurchaseResult{TransactionID: transactionID}
	sendReq := domain.SendChannelMessageRequest{
		UserID: req.BuyerUserID, ChannelID: giveaway.BoostPeer.ID,
		RandomID: giveaway.RandomID, Date: req.Date,
		Media: &domain.MessageMedia{Kind: domain.MessageMediaKindGiveaway, Giveaway: &domain.MessageGiveaway{
			OnlyNewSubscribers: giveaway.OnlyNewSubscribers, WinnersAreVisible: giveaway.WinnersAreVisible,
			Channels: starsGiveawayChannelIDs(giveaway), CountriesISO2: append([]string(nil), giveaway.CountriesISO2...),
			PrizeDescription: giveaway.PrizeDescription, Quantity: giveaway.Users, Stars: req.Stars, UntilDate: giveaway.UntilDate,
		}},
		IdempotencyFingerprint: fingerprint[:],
	}
	hooks := channelSendTxHooks{
		before: func(ctx context.Context, tx pgx.Tx, _ *domain.SendChannelMessageRequest) error {
			if err := validateStarsPurchaseForm(ctx, tx, req, true); err != nil {
				return err
			}
			if found, err := starsPurchaseCommandExists(ctx, tx, req.BuyerUserID, req.FormID); err != nil {
				return err
			} else if found {
				return errStarsPurchaseReplay
			}
			return nil
		},
		after: func(ctx context.Context, tx pgx.Tx, sent domain.SendChannelMessageResult) error {
			if sent.Message.ID <= 0 || sent.Event.Pts <= 0 || sent.Event.PtsCount != 1 {
				return fmt.Errorf("settle stars giveaway: invalid channel send receipt")
			}
			if _, err := tx.Exec(ctx, `
INSERT INTO stars_giveaways
	(buyer_user_id,form_id,channel_id,launch_message_id,random_id,stars,users,per_user_stars,yearly_boosts,
	 until_date,purpose_json,state,created_at)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,'active',$12)`,
				req.BuyerUserID, req.FormID, giveaway.BoostPeer.ID, sent.Message.ID, giveaway.RandomID,
				req.Stars, giveaway.Users, giveaway.PerUserStars, giveaway.YearlyBoosts, giveaway.UntilDate, purposeJSON, req.Date); err != nil {
				return fmt.Errorf("insert stars giveaway campaign: %w", err)
			}
			if _, err := tx.Exec(ctx, `
INSERT INTO stars_purchase_commands
	(buyer_user_id,form_id,kind,request_fingerprint,recipient_user_id,spend_peer_type,spend_peer_id,purpose_json,stars,currency,amount,
	 balance_after,transaction_id,created_at)
VALUES($1,$2,$3,$4,NULL,NULL,NULL,$5,$6,$7,$8,0,$9,$10)`,
				req.BuyerUserID, req.FormID, string(req.Kind), fingerprint[:], purposeJSON,
				req.Stars, req.Currency, req.Amount, transactionID, req.Date); err != nil {
				return fmt.Errorf("insert stars giveaway purchase command: %w", err)
			}
			result.ChannelSend = sent
			return applyStarsPurchaseDeliveryEffectsTx(ctx, tx, result, effects)
		},
	}
	sent, err := s.channels.sendChannelMessageWithHooks(ctx, sendReq, hooks)
	if err != nil {
		if errors.Is(err, errStarsPurchaseReplay) {
			if replay, found, replayErr := s.loadStarsPurchaseReplay(ctx, req, fingerprint); replayErr != nil || found {
				return replay, replayErr
			}
		}
		return domain.StarsPurchaseResult{}, err
	}
	if sent.Duplicate {
		if replay, found, replayErr := s.loadStarsPurchaseReplay(ctx, req, fingerprint); replayErr != nil || found {
			return replay, replayErr
		}
		return domain.StarsPurchaseResult{}, domain.ErrStarsPurchaseFormInvalid
	}
	result.ChannelSend = sent
	return result, nil
}

func applyStarsPurchaseDeliveryEffectsTx(ctx context.Context, tx pgx.Tx, result domain.StarsPurchaseResult, build store.DeliveryEffectsBuilder[domain.StarsPurchaseResult]) error {
	intents, err := build(result)
	if err != nil {
		return fmt.Errorf("build stars purchase delivery: %w", err)
	}
	if err := store.ValidateStarsPurchaseDeliveryEffects(result, intents); err != nil {
		return err
	}
	if _, err := applyDeliveryEffectsTx(ctx, tx, intents); err != nil {
		return fmt.Errorf("apply stars purchase delivery: %w", err)
	}
	return nil
}

func starsGiveawayChannelIDs(giveaway domain.StarsGiveawayPurchase) []int64 {
	ids := make([]int64, 0, 1+len(giveaway.AdditionalPeers))
	ids = append(ids, giveaway.BoostPeer.ID)
	for _, peer := range giveaway.AdditionalPeers {
		ids = append(ids, peer.ID)
	}
	return ids
}

func validateStarsPurchaseForm(ctx context.Context, db sqlcgen.DBTX, req domain.StarsPurchaseRequest, lock bool) error {
	query := `SELECT kind,recipient_user_id,spend_peer_type,spend_peer_id,purpose_json,stars,currency,amount,issued_at,expires_at
FROM stars_purchase_forms WHERE buyer_user_id=$1 AND form_id=$2`
	if lock {
		query += ` FOR UPDATE`
	}
	var kind string
	var recipientID pgtype.Int8
	var spendPeerType pgtype.Text
	var spendPeerID pgtype.Int8
	var purposeJSON []byte
	var stars, amount int64
	var currency string
	var issuedAt, expiresAt int
	err := db.QueryRow(ctx, query, req.BuyerUserID, req.FormID).
		Scan(&kind, &recipientID, &spendPeerType, &spendPeerID, &purposeJSON, &stars, &currency, &amount, &issuedAt, &expiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ErrStarsPurchaseFormInvalid
	}
	if err != nil {
		return fmt.Errorf("load stars gift form: %w", err)
	}
	if req.Date >= expiresAt {
		return domain.ErrStarsPurchaseFormExpired
	}
	if issuedAt <= 0 || kind != string(req.Kind) || nullableStarsRecipient(recipientID) != req.RecipientUserID ||
		nullableStarsPeer(spendPeerType, spendPeerID) != req.SpendPurposePeer || stars != req.Stars ||
		currency != req.Currency || amount != req.Amount || !sameStarsPurchasePurpose(purposeJSON, req.StarsPurchaseForm) {
		return domain.ErrStarsPurchaseFormInvalid
	}
	return nil
}

func (s *StarsPurchaseStore) loadStarsPurchaseReplay(ctx context.Context, req domain.StarsPurchaseRequest, fingerprint [32]byte) (domain.StarsPurchaseResult, bool, error) {
	var recipientID pgtype.Int8
	var spendPeerType pgtype.Text
	var spendPeerID pgtype.Int8
	var stars, amount, balance int64
	var kind, currency, transactionID string
	var storedFingerprint, purposeJSON []byte
	err := s.db.QueryRow(ctx, `
SELECT kind,request_fingerprint,recipient_user_id,spend_peer_type,spend_peer_id,purpose_json,stars,currency,amount,balance_after,transaction_id
FROM stars_purchase_commands WHERE buyer_user_id=$1 AND form_id=$2`, req.BuyerUserID, req.FormID).
		Scan(&kind, &storedFingerprint, &recipientID, &spendPeerType, &spendPeerID, &purposeJSON, &stars, &currency, &amount, &balance, &transactionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.StarsPurchaseResult{}, false, nil
	}
	if err != nil {
		return domain.StarsPurchaseResult{}, false, fmt.Errorf("load stars purchase replay: %w", err)
	}
	if kind != string(req.Kind) || !bytes.Equal(storedFingerprint, fingerprint[:]) || nullableStarsRecipient(recipientID) != req.RecipientUserID ||
		nullableStarsPeer(spendPeerType, spendPeerID) != req.SpendPurposePeer ||
		stars != req.Stars || currency != req.Currency || amount != req.Amount || transactionID == "" ||
		!sameStarsPurchasePurpose(purposeJSON, req.StarsPurchaseForm) {
		return domain.StarsPurchaseResult{}, false, domain.ErrStarsPurchaseFormInvalid
	}
	result := domain.StarsPurchaseResult{
		Balance:       domain.StarsBalance{UserID: req.BuyerUserID, Balance: balance},
		TransactionID: transactionID, Duplicate: true,
	}
	if req.Kind == domain.StarsPurchaseTopup {
		return result, true, nil
	}
	if req.Kind == domain.StarsPurchaseGiveaway {
		if s.channels == nil || req.Giveaway == nil {
			return domain.StarsPurchaseResult{}, false, domain.ErrStarsPurchaseFormInvalid
		}
		sent, found, err := s.channels.LookupChannelSendReplay(ctx, domain.ChannelSendReplayRequest{
			ChannelID: req.Giveaway.BoostPeer.ID, SenderUserID: req.BuyerUserID,
			RandomID: req.Giveaway.RandomID, IdempotencyFingerprint: fingerprint[:],
		})
		if err != nil {
			return domain.StarsPurchaseResult{}, false, err
		}
		if !found {
			return domain.StarsPurchaseResult{}, false, domain.ErrStarsPurchaseFormInvalid
		}
		result.ChannelSend = sent
		return result, true, nil
	}
	if s.messages == nil {
		return domain.StarsPurchaseResult{}, false, domain.ErrStarsPurchaseFormInvalid
	}
	sent, found, err := s.messages.LookupPrivateSendReplay(ctx, domain.PrivateSendReplayRequest{
		SenderUserID: req.BuyerUserID, RecipientUserID: req.RecipientUserID,
		RandomID:               lifecycleCommandRandomID("stars-fiat-gift", req.BuyerUserID, req.FormID),
		IdempotencyFingerprint: fingerprint[:],
	})
	if err != nil {
		return domain.StarsPurchaseResult{}, false, err
	}
	if !found {
		return domain.StarsPurchaseResult{}, false, domain.ErrStarsPurchaseFormInvalid
	}
	result.Balance.UserID = req.RecipientUserID
	result.Send = sent
	return result, true, nil
}

func starsPurchaseCommandExists(ctx context.Context, db sqlcgen.DBTX, buyerUserID, formID int64) (bool, error) {
	var exists bool
	if err := db.QueryRow(ctx, `SELECT EXISTS(
SELECT 1 FROM stars_purchase_commands WHERE buyer_user_id=$1 AND form_id=$2)`, buyerUserID, formID).Scan(&exists); err != nil {
		return false, fmt.Errorf("check stars purchase command: %w", err)
	}
	return exists, nil
}

func starsPurchaseFingerprint(req domain.StarsPurchaseRequest) [32]byte {
	purposeJSON, _ := starsPurchasePurposeJSON(req.StarsPurchaseForm)
	return sha256.Sum256([]byte(fmt.Sprintf("telesrv:stars-fiat-purchase:v2:%s:%d:%d:%s:%d:%d:%s:%d:%d:%s",
		req.Kind, req.BuyerUserID, req.RecipientUserID, req.SpendPurposePeer.Type, req.SpendPurposePeer.ID,
		req.Stars, req.Currency, req.Amount, req.FormID, purposeJSON)))
}

func newStarsPurchaseFormID() (int64, error) {
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return 0, err
	}
	id := int64(binary.LittleEndian.Uint64(raw[:]) & 0x7fffffffffffffff)
	if id == 0 {
		return 1, nil
	}
	return id, nil
}

func validStarsPurchaseForm(form domain.StarsPurchaseForm) bool {
	if !validStarsPurchaseCommand(form) || form.IssuedAt <= 0 || form.ExpiresAt != form.IssuedAt+600 {
		return false
	}
	return form.Kind != domain.StarsPurchaseGiveaway ||
		(form.Giveaway.UntilDate > form.IssuedAt && form.Giveaway.UntilDate <= form.IssuedAt+7*24*60*60)
}

func validStarsPurchaseCommand(form domain.StarsPurchaseForm) bool {
	if !form.Kind.Valid() || form.BuyerUserID <= 0 || form.Stars <= 0 || form.Amount <= 0 || len(form.Currency) != 3 {
		return false
	}
	validSpendPeer := (form.SpendPurposePeer == domain.Peer{}) ||
		((form.SpendPurposePeer.Type == domain.PeerTypeUser || form.SpendPurposePeer.Type == domain.PeerTypeChannel) && form.SpendPurposePeer.ID > 0)
	switch form.Kind {
	case domain.StarsPurchaseTopup:
		return form.RecipientUserID == 0 && validSpendPeer && form.Giveaway == nil
	case domain.StarsPurchaseGift:
		return form.RecipientUserID > 0 && form.RecipientUserID != form.BuyerUserID && form.SpendPurposePeer == (domain.Peer{}) && form.Giveaway == nil
	case domain.StarsPurchaseGiveaway:
		return form.RecipientUserID == 0 && form.SpendPurposePeer == (domain.Peer{}) && validStarsGiveawayPurchase(form.Giveaway, form.Stars)
	default:
		return false
	}
}

func validStarsGiveawayPurchase(g *domain.StarsGiveawayPurchase, stars int64) bool {
	if g == nil || g.BoostPeer.Type != domain.PeerTypeChannel || g.BoostPeer.ID <= 0 || g.RandomID == 0 ||
		g.UntilDate <= 0 || g.Users <= 0 || g.PerUserStars <= 0 || g.YearlyBoosts < 0 ||
		int64(g.Users) > math.MaxInt64/g.PerUserStars || int64(g.Users)*g.PerUserStars != stars ||
		len(g.AdditionalPeers) > 10 || len(g.CountriesISO2) > 10 || utf8.RuneCountInString(g.PrizeDescription) > 128 {
		return false
	}
	seenPeers := map[int64]struct{}{g.BoostPeer.ID: struct{}{}}
	for _, peer := range g.AdditionalPeers {
		if peer.Type != domain.PeerTypeChannel || peer.ID <= 0 {
			return false
		}
		if _, exists := seenPeers[peer.ID]; exists {
			return false
		}
		seenPeers[peer.ID] = struct{}{}
	}
	seenCountries := make(map[string]struct{}, len(g.CountriesISO2))
	for _, country := range g.CountriesISO2 {
		if len(country) != 2 || country != strings.ToUpper(country) || country[0] < 'A' || country[0] > 'Z' || country[1] < 'A' || country[1] > 'Z' {
			return false
		}
		if _, exists := seenCountries[country]; exists {
			return false
		}
		seenCountries[country] = struct{}{}
	}
	return true
}

func starsPurchasePurposeJSON(form domain.StarsPurchaseForm) ([]byte, error) {
	if form.Kind != domain.StarsPurchaseGiveaway {
		return []byte(`{}`), nil
	}
	if form.Giveaway == nil {
		return nil, domain.ErrStarsPurchaseFormInvalid
	}
	return json.Marshal(form.Giveaway)
}

func sameStarsPurchasePurpose(stored []byte, form domain.StarsPurchaseForm) bool {
	want, err := starsPurchasePurposeJSON(form)
	if err != nil {
		return false
	}
	if form.Kind != domain.StarsPurchaseGiveaway {
		var value map[string]any
		return json.Unmarshal(stored, &value) == nil && len(value) == 0
	}
	var decoded domain.StarsGiveawayPurchase
	if err := json.Unmarshal(stored, &decoded); err != nil {
		return false
	}
	got, err := json.Marshal(&decoded)
	return err == nil && bytes.Equal(got, want)
}

func starsPurchaseRecipientValue(recipientUserID int64) any {
	if recipientUserID == 0 {
		return nil
	}
	return recipientUserID
}

func nullableStarsRecipient(value pgtype.Int8) int64 {
	if !value.Valid {
		return 0
	}
	return value.Int64
}

func starsPurchasePeerTypeValue(peer domain.Peer) any {
	if peer == (domain.Peer{}) {
		return nil
	}
	return string(peer.Type)
}

func starsPurchasePeerIDValue(peer domain.Peer) any {
	if peer == (domain.Peer{}) {
		return nil
	}
	return peer.ID
}

func nullableStarsPeer(peerType pgtype.Text, peerID pgtype.Int8) domain.Peer {
	if !peerType.Valid || !peerID.Valid {
		return domain.Peer{}
	}
	return domain.Peer{Type: domain.PeerType(peerType.String), ID: peerID.Int64}
}

func (s *StarsPurchaseStore) GetStarsGiveawayInfo(ctx context.Context, viewerUserID, channelID int64, messageID, date int) (domain.StarsGiveawayInfo, error) {
	if s == nil || s.db == nil || viewerUserID <= 0 || channelID <= 0 || messageID <= 0 || date <= 0 {
		return domain.StarsGiveawayInfo{}, domain.ErrStarsPurchaseFormInvalid
	}
	var purposeJSON []byte
	var state string
	var startDate, untilDate int
	err := s.db.QueryRow(ctx, `
SELECT purpose_json,state,created_at,until_date
FROM stars_giveaways WHERE channel_id=$1 AND launch_message_id=$2`, channelID, messageID).
		Scan(&purposeJSON, &state, &startDate, &untilDate)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.StarsGiveawayInfo{}, domain.ErrMessageIDInvalid
	}
	if err != nil {
		return domain.StarsGiveawayInfo{}, fmt.Errorf("load stars giveaway info: %w", err)
	}
	var purpose domain.StarsGiveawayPurchase
	if err := json.Unmarshal(purposeJSON, &purpose); err != nil || purpose.BoostPeer.ID != channelID {
		return domain.StarsGiveawayInfo{}, domain.ErrStarsPurchaseFormInvalid
	}
	info := domain.StarsGiveawayInfo{StartDate: startDate}
	if state == "cancelled" {
		return info, nil
	}
	if state != "active" || date >= untilDate {
		info.PreparingResults = true
		return info, nil
	}
	channels := starsGiveawayChannelIDs(purpose)
	for _, requiredChannelID := range channels {
		var role, status string
		var joinedAt int
		err := s.db.QueryRow(ctx, `
SELECT role,status,joined_at FROM channel_members WHERE channel_id=$1 AND user_id=$2`, requiredChannelID, viewerUserID).
			Scan(&role, &status, &joinedAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return info, nil
		}
		if err != nil {
			return domain.StarsGiveawayInfo{}, fmt.Errorf("load giveaway participant membership: %w", err)
		}
		if status != string(domain.ChannelMemberActive) {
			return info, nil
		}
		if role == string(domain.ChannelRoleCreator) || role == string(domain.ChannelRoleAdmin) {
			info.AdminDisallowedChatID = requiredChannelID
			return info, nil
		}
		if purpose.OnlyNewSubscribers && joinedAt > 0 && joinedAt <= startDate {
			info.JoinedTooEarlyDate = joinedAt
			return info, nil
		}
	}
	if len(purpose.CountriesISO2) > 0 {
		var country string
		if err := s.db.QueryRow(ctx, `
SELECT COALESCE((SELECT cc.iso2 FROM country_codes cc WHERE cc.country_code=u.country_code ORDER BY cc.id LIMIT 1),'')
FROM users u WHERE u.id=$1`, viewerUserID).Scan(&country); err != nil {
			return domain.StarsGiveawayInfo{}, fmt.Errorf("load giveaway participant country: %w", err)
		}
		allowed := false
		for _, candidate := range purpose.CountriesISO2 {
			if candidate == country {
				allowed = true
				break
			}
		}
		if !allowed {
			info.DisallowedCountry = country
			return info, nil
		}
	}
	info.Participating = true
	return info, nil
}

var _ store.StarsPurchaseStore = (*StarsPurchaseStore)(nil)
