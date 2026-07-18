package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"telesrv/internal/domain"
	"telesrv/internal/store"
	"telesrv/internal/store/postgres/sqlcgen"
)

type StarGiftLifecycleStore struct {
	db               sqlcgen.DBTX
	messages         *MessageStore
	tonStartingGrant int64
	market           domain.StarGiftMarketPolicy
	craftDraw        func(int) (int, error)
}

type StarGiftLifecycleOption func(*StarGiftLifecycleStore)

func WithStarGiftMarketPolicy(policy domain.StarGiftMarketPolicy) StarGiftLifecycleOption {
	return func(s *StarGiftLifecycleStore) {
		if policy.Valid() {
			s.market = policy
		}
	}
}

// WithStarGiftCraftDraw replaces the cryptographically random craft draw.
// It exists so integration tests can cover both terminal outcomes without
// probabilistic retries; production constructors use defaultStarGiftCraftDraw.
func WithStarGiftCraftDraw(draw func(int) (int, error)) StarGiftLifecycleOption {
	return func(s *StarGiftLifecycleStore) {
		if draw != nil {
			s.craftDraw = draw
		}
	}
}

func NewStarGiftLifecycleStore(db sqlcgen.DBTX, messages *MessageStore, tonStartingGrant int64, opts ...StarGiftLifecycleOption) *StarGiftLifecycleStore {
	if tonStartingGrant < 0 {
		tonStartingGrant = 0
	}
	s := &StarGiftLifecycleStore{db: db, messages: messages, tonStartingGrant: tonStartingGrant,
		market:    domain.StarGiftMarketPolicy{StarsProceedsPermille: 1000, TONProceedsPermille: 1000},
		craftDraw: defaultStarGiftCraftDraw}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// ConvertStarGift owns the complete conversion aggregate: saved-gift terminal
// state, collection membership, owner-scoped Stars balance and transaction log.
// A channel conversion credits the channel ledger, never ActorUserID's personal
// balance. No external payment or blockchain system participates.
func (s *StarGiftLifecycleStore) ConvertStarGift(ctx context.Context, req domain.StarGiftConvertRequest) (domain.StarGiftConvertResult, error) {
	if s == nil || s.db == nil || req.ActorUserID <= 0 || !req.Ref.Valid() || req.Date <= 0 {
		return domain.StarGiftConvertResult{}, domain.ErrStarGiftNotFound
	}
	if req.Ref.Owner.Type == domain.PeerTypeUser && req.Ref.Owner.ID != req.ActorUserID {
		return domain.StarGiftConvertResult{}, domain.ErrStarGiftOwnerInvalid
	}
	if !validLifecyclePeer(req.Ref.Owner) {
		return domain.StarGiftConvertResult{}, domain.ErrStarGiftOwnerInvalid
	}

	var result domain.StarGiftConvertResult
	err := withTx(ctx, s.db, "convert star gift aggregate", func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, starGiftCollectionLockKey(req.Ref.Owner)); err != nil {
			return fmt.Errorf("lock star gift owner collections: %w", err)
		}
		saved, err := lockSavedStarGiftForUpgrade(ctx, tx, req.Ref)
		if err != nil {
			return err
		}
		if saved.Converted || saved.LifecycleStatus == domain.StarGiftLifecycleConverted {
			return domain.ErrStarGiftAlreadyConverted
		}
		if !saved.LifecycleStatus.Live() || saved.UniqueGiftID != 0 {
			return domain.ErrStarGiftAlreadyUpgraded
		}

		from := domain.Peer{Type: domain.PeerTypeUser, ID: saved.FromUserID}
		amount := saved.ConvertStars
		var balanceAfter int64
		switch saved.Owner.Type {
		case domain.PeerTypeUser:
			if amount > 0 {
				if err := s.creditLifecycleAmount(ctx, tx, saved.Owner.ID,
					domain.StarGiftAmount{Currency: domain.StarGiftCurrencyStars, Amount: amount},
					domain.StarsReasonGift, from, req.Date, "Star gift conversion"); err != nil {
					return err
				}
			}
			if err := tx.QueryRow(ctx, `SELECT COALESCE((SELECT balance FROM stars_balances WHERE user_id=$1),0)`, saved.Owner.ID).Scan(&balanceAfter); err != nil {
				return err
			}
		case domain.PeerTypeChannel:
			if amount > 0 {
				if err := tx.QueryRow(ctx, `INSERT INTO channel_stars_balances(channel_id,balance) VALUES($1,$2)
					ON CONFLICT(channel_id) DO UPDATE SET balance=channel_stars_balances.balance+EXCLUDED.balance,updated_at=now()
					RETURNING balance`, saved.Owner.ID, amount).Scan(&balanceAfter); err != nil {
					return fmt.Errorf("credit channel star gift conversion: %w", err)
				}
				if _, err := tx.Exec(ctx, `INSERT INTO channel_stars_transactions
					(channel_id,actor_user_id,amount,reason,peer_type,peer_id,gift_id,date)
					VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, saved.Owner.ID, req.ActorUserID, amount,
					string(domain.StarsReasonGift), string(from.Type), from.ID, saved.GiftID, req.Date); err != nil {
					return fmt.Errorf("record channel star gift conversion: %w", err)
				}
			} else if err := tx.QueryRow(ctx, `SELECT COALESCE((SELECT balance FROM channel_stars_balances WHERE channel_id=$1),0)`, saved.Owner.ID).Scan(&balanceAfter); err != nil {
				return err
			}
		}

		if _, err := tx.Exec(ctx, `UPDATE peer_star_gifts
			SET converted=true,lifecycle_status='converted',unsaved=true,pinned_order=0
			WHERE id=$1`, saved.ID); err != nil {
			return fmt.Errorf("mark star gift converted: %w", err)
		}
		if err := removeSavedGiftFromCollections(ctx, tx, saved.Owner, saved.ID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO star_gift_conversions
			(saved_gift_id,actor_user_id,owner_peer_type,owner_peer_id,amount,balance_after,converted_at)
			VALUES($1,$2,$3,$4,$5,$6,$7)`, saved.ID, req.ActorUserID, string(saved.Owner.Type),
			saved.Owner.ID, amount, balanceAfter, req.Date); err != nil {
			return fmt.Errorf("record star gift conversion command: %w", err)
		}
		saved.Converted = true
		saved.LifecycleStatus = domain.StarGiftLifecycleConverted
		saved.Unsaved = true
		saved.PinnedOrder = 0
		saved.CollectionIDs = nil
		result = domain.StarGiftConvertResult{Saved: saved, OwnerBalance: balanceAfter}
		return nil
	})
	if err != nil {
		return domain.StarGiftConvertResult{}, err
	}
	return result, nil
}

func (s *StarGiftLifecycleStore) ListResaleStarGifts(ctx context.Context, filter domain.StarGiftResaleFilter) (domain.StarGiftResalePage, error) {
	if s == nil || s.db == nil || filter.GiftID <= 0 || filter.Limit <= 0 || filter.Limit > domain.MaxSavedStarGiftsLimit ||
		filter.SortByPrice && filter.SortByNum || len(filter.Offset) > domain.MaxStarGiftsOffsetBytes {
		return domain.StarGiftResalePage{}, domain.ErrStarGiftResaleUnavailable
	}
	conditions := []string{"u.gift_id=$1", "NOT u.burned", "u.owner_address=''"}
	args := []any{filter.GiftID}
	nextArg := func(value any) string {
		args = append(args, value)
		return "$" + strconv.Itoa(len(args))
	}
	if filter.StarsOnly {
		conditions = append(conditions, "l.currency='XTR'")
	}
	if filter.ForCraft {
		conditions = append(conditions, `u.craft_chance_permille>0 AND EXISTS (
SELECT 1 FROM star_gift_collectible_models model
WHERE model.collectible_revision_id=u.collectible_revision_id AND model.crafted)`)
	}
	if len(filter.ModelIDs) > 0 {
		conditions = append(conditions, "u.model_attribute_id IN (SELECT id FROM star_gift_collectible_models WHERE document_id=ANY("+nextArg(filter.ModelIDs)+"::bigint[]))")
	}
	if len(filter.PatternIDs) > 0 {
		conditions = append(conditions, "u.pattern_attribute_id IN (SELECT id FROM star_gift_collectible_patterns WHERE document_id=ANY("+nextArg(filter.PatternIDs)+"::bigint[]))")
	}
	if len(filter.BackdropIDs) > 0 {
		conditions = append(conditions, "u.backdrop_attribute_id IN (SELECT id FROM star_gift_collectible_backdrops WHERE backdrop_id::bigint=ANY("+nextArg(filter.BackdropIDs)+"::bigint[]))")
	}
	order := "l.updated_at DESC, u.id DESC"
	if filter.SortByPrice {
		order = "l.amount, u.id"
	} else if filter.SortByNum {
		order = "u.num, u.id"
	}
	if filter.Offset != "" {
		parts := strings.Split(filter.Offset, ":")
		if len(parts) != 3 {
			return domain.StarGiftResalePage{}, domain.ErrStarGiftResaleUnavailable
		}
		value, valueErr := strconv.ParseInt(parts[1], 10, 64)
		id, idErr := strconv.ParseInt(parts[2], 10, 64)
		if valueErr != nil || idErr != nil || value < 0 || id <= 0 {
			return domain.StarGiftResalePage{}, domain.ErrStarGiftResaleUnavailable
		}
		switch {
		case filter.SortByPrice && parts[0] == "p":
			p1, p2 := nextArg(value), nextArg(id)
			conditions = append(conditions, "(l.amount,u.id)>("+p1+","+p2+")")
		case filter.SortByNum && parts[0] == "n":
			p1, p2 := nextArg(value), nextArg(id)
			conditions = append(conditions, "(u.num,u.id)>("+p1+","+p2+")")
		case !filter.SortByPrice && !filter.SortByNum && parts[0] == "d":
			p1, p2 := nextArg(value), nextArg(id)
			conditions = append(conditions, "(l.updated_at,u.id)<("+p1+","+p2+")")
		default:
			return domain.StarGiftResalePage{}, domain.ErrStarGiftResaleUnavailable
		}
	}
	where := strings.Join(conditions, " AND ")
	var total int
	if err := s.db.QueryRow(ctx, `SELECT COUNT(*) FROM star_gift_listings l JOIN unique_star_gifts u ON u.id=l.unique_gift_id WHERE `+where, args...).Scan(&total); err != nil {
		return domain.StarGiftResalePage{}, fmt.Errorf("count resale star gifts: %w", err)
	}
	limitArg := nextArg(filter.Limit + 1)
	rows, err := s.db.Query(ctx, `SELECT u.id,l.amount,l.updated_at,u.num
FROM star_gift_listings l JOIN unique_star_gifts u ON u.id=l.unique_gift_id
WHERE `+where+` ORDER BY `+order+` LIMIT `+limitArg, args...)
	if err != nil {
		return domain.StarGiftResalePage{}, fmt.Errorf("list resale star gifts: %w", err)
	}
	defer rows.Close()
	type listedID struct {
		id, amount   int64
		updated, num int
	}
	listed := make([]listedID, 0, filter.Limit+1)
	ids := make([]int64, 0, filter.Limit+1)
	for rows.Next() {
		var item listedID
		if err := rows.Scan(&item.id, &item.amount, &item.updated, &item.num); err != nil {
			return domain.StarGiftResalePage{}, err
		}
		listed = append(listed, item)
		ids = append(ids, item.id)
	}
	if err := rows.Err(); err != nil {
		return domain.StarGiftResalePage{}, err
	}
	hasMore := len(listed) > filter.Limit
	if hasMore {
		listed, ids = listed[:filter.Limit], ids[:filter.Limit]
	}
	uniqueByID, err := NewStarGiftStore(s.db).UniqueByIDs(ctx, ids)
	if err != nil {
		return domain.StarGiftResalePage{}, err
	}
	page := domain.StarGiftResalePage{Count: total, Gifts: make([]domain.UniqueStarGift, 0, len(ids))}
	for _, item := range listed {
		gift, ok := uniqueByID[item.id]
		if !ok {
			return domain.StarGiftResalePage{}, domain.ErrStarGiftResaleUnavailable
		}
		page.Gifts = append(page.Gifts, gift)
	}
	if hasMore && len(listed) > 0 {
		last := listed[len(listed)-1]
		switch {
		case filter.SortByPrice:
			page.NextOffset = fmt.Sprintf("p:%d:%d", last.amount, last.id)
		case filter.SortByNum:
			page.NextOffset = fmt.Sprintf("n:%d:%d", last.num, last.id)
		default:
			page.NextOffset = fmt.Sprintf("d:%d:%d", last.updated, last.id)
		}
	}
	return page, nil
}

func (s *StarGiftLifecycleStore) UniqueStarGiftValueInfo(ctx context.Context, uniqueGiftID int64) (domain.StarGiftValueInfo, error) {
	var out domain.StarGiftValueInfo
	var configuredCurrency string
	var configuredValue int64
	err := s.db.QueryRow(ctx, `
SELECT sg.gift_date, cr.stars, u.value_currency, u.value_amount, u.last_sale_date,
       COALESCE(CASE WHEN u.last_sale_currency='XTR' THEN u.last_sale_amount END,0),
       COALESCE((SELECT MIN(l.amount) FROM star_gift_listings l JOIN unique_star_gifts lu ON lu.id=l.unique_gift_id WHERE lu.gift_id=u.gift_id AND l.currency='XTR'),0),
       COALESCE((SELECT AVG(sa.amount)::bigint FROM star_gift_sales sa JOIN unique_star_gifts su ON su.id=sa.unique_gift_id WHERE su.gift_id=u.gift_id AND sa.currency='XTR'),0),
       (SELECT COUNT(*) FROM star_gift_listings l JOIN unique_star_gifts lu ON lu.id=l.unique_gift_id WHERE lu.gift_id=u.gift_id)
FROM unique_star_gifts u
JOIN peer_star_gifts sg ON sg.id=u.source_saved_gift_id
JOIN star_gift_catalog_revisions cr ON cr.id=sg.catalog_revision_id
WHERE u.id=$1`, uniqueGiftID).Scan(&out.InitialSaleDate, &out.InitialSaleStars, &configuredCurrency,
		&configuredValue, &out.LastSaleDate, &out.LastSalePrice, &out.FloorPrice, &out.AveragePrice, &out.ListedCount)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.StarGiftValueInfo{}, domain.ErrStarGiftNotFound
	}
	if err != nil {
		return domain.StarGiftValueInfo{}, fmt.Errorf("star gift value info: %w", err)
	}
	// The self-hosted ledger has no FX oracle. One Star-cent is the explicit local
	// valuation unit unless an operator/provider has stored a real fiat estimate.
	out.Currency = "USD"
	out.InitialSalePrice = out.InitialSaleStars
	if configuredCurrency != "" && configuredValue > 0 {
		out.Currency, out.Value = configuredCurrency, configuredValue
	} else if out.LastSalePrice > 0 {
		out.Value = out.LastSalePrice
	} else if out.FloorPrice > 0 {
		out.Value = out.FloorPrice
	} else {
		out.Value = out.InitialSalePrice
	}
	out.ValueIsAverage = out.AveragePrice > 0 && out.LastSalePrice == 0
	return out, nil
}

func (s *StarGiftLifecycleStore) SetStarGiftListing(ctx context.Context, req domain.StarGiftListingRequest) (domain.UniqueStarGift, error) {
	if req.ActorUserID <= 0 || !req.Ref.Valid() || req.Date <= 0 || req.Amount != nil && !req.Amount.Valid() {
		return domain.UniqueStarGift{}, domain.ErrStarGiftResaleUnavailable
	}
	var uniqueID int64
	err := withTx(ctx, s.db, "set star gift listing", func(tx pgx.Tx) error {
		saved, err := lockSavedStarGiftForUpgrade(ctx, tx, req.Ref)
		if err != nil {
			return err
		}
		if !saved.LifecycleStatus.Live() || saved.UniqueGiftID == 0 || saved.CanResellAt > req.Date || saved.Owner != req.Ref.Owner {
			return domain.ErrStarGiftResaleUnavailable
		}
		if saved.Owner.Type == domain.PeerTypeUser && saved.Owner.ID != req.ActorUserID {
			return domain.ErrStarGiftOwnerInvalid
		}
		unique, found, err := NewStarGiftStore(tx).UniqueByID(ctx, saved.UniqueGiftID)
		if err != nil {
			return err
		}
		if !found || unique.Burned || unique.Owner != saved.Owner || unique.OwnerAddress != "" {
			return domain.ErrStarGiftResaleUnavailable
		}
		uniqueID = unique.ID
		if req.Amount == nil {
			if _, err := tx.Exec(ctx, `DELETE FROM star_gift_listings WHERE unique_gift_id=$1`, unique.ID); err != nil {
				return err
			}
		} else {
			if unique.ResaleTonOnly && req.Amount.Currency != domain.StarGiftCurrencyTON {
				return domain.ErrStarGiftResaleUnavailable
			}
			var minimum int64
			if req.Amount.Currency == domain.StarGiftCurrencyStars {
				if err := tx.QueryRow(ctx, `SELECT resell_min_stars FROM star_gift_catalog WHERE gift_id=$1`, unique.GiftID).Scan(&minimum); err != nil {
					return err
				}
				if req.Amount.Amount < minimum {
					return domain.ErrStarGiftResaleUnavailable
				}
			}
			_, err = tx.Exec(ctx, `INSERT INTO star_gift_listings(unique_gift_id,seller_peer_type,seller_peer_id,currency,amount,listed_at,updated_at)
VALUES($1,$2,$3,$4,$5,$6,$6)
ON CONFLICT(unique_gift_id) DO UPDATE SET currency=EXCLUDED.currency,amount=EXCLUDED.amount,updated_at=EXCLUDED.updated_at,version=star_gift_listings.version+1`,
				unique.ID, string(saved.Owner.Type), saved.Owner.ID, string(req.Amount.Currency), req.Amount.Amount, req.Date)
			if err != nil {
				return err
			}
		}
		return updateStarGiftResaleProjection(ctx, tx, unique.GiftID)
	})
	if err != nil {
		return domain.UniqueStarGift{}, err
	}
	unique, found, err := NewStarGiftStore(s.db).UniqueByID(ctx, uniqueID)
	if err != nil {
		return domain.UniqueStarGift{}, err
	}
	if !found {
		return domain.UniqueStarGift{}, domain.ErrStarGiftNotFound
	}
	return unique, nil
}

func (s *StarGiftLifecycleStore) TransferStarGift(ctx context.Context, req domain.StarGiftTransferRequest) (domain.StarGiftTransferResult, error) {
	if s == nil || s.messages == nil || req.ActorUserID <= 0 || !req.Ref.Valid() || !validLifecyclePeer(req.To) ||
		req.To == req.Ref.Owner || req.ChargeStars < 0 || req.Date <= 0 || strings.TrimSpace(req.CommandKey) == "" {
		return domain.StarGiftTransferResult{}, domain.ErrStarGiftTransferUnavailable
	}
	if req.To.Type != domain.PeerTypeUser {
		return s.transferStarGiftWithoutPrivateMessage(ctx, req)
	}
	messageReq := domain.SendPrivateTextRequest{
		SenderUserID: req.ActorUserID, RecipientUserID: req.To.ID,
		RandomID: lifecycleCommandRandomID("gift-transfer", req.ActorUserID, req.CommandKey), Date: req.Date,
		OriginAuthKeyID: req.OriginAuthKeyID, OriginSessionID: req.OriginSessionID, OriginUserID: req.ActorUserID,
		Media: &domain.MessageMedia{Kind: domain.MessageMediaKindService, ServiceAction: &domain.MessageServiceAction{
			Kind: domain.MessageServiceActionStarGiftUnique, StarGiftUnique: &domain.MessageStarGiftUniqueAction{Transferred: true, Saved: true},
		}},
	}
	var result domain.StarGiftTransferResult
	hooks := privateSendTxHooks{
		before: func(ctx context.Context, tx pgx.Tx, send *domain.SendPrivateTextRequest) error {
			saved, unique, err := lockTransferableStarGift(ctx, tx, req.ActorUserID, req.Ref, req.Date)
			if err != nil {
				return err
			}
			if saved.TransferStars != req.ChargeStars {
				return domain.ErrStarGiftTransferUnavailable
			}
			if err := ensureNoStarGiftMarketConflict(ctx, tx, unique.ID); err != nil {
				return err
			}
			balance, err := s.debitLifecycleAmount(ctx, tx, req.ActorUserID,
				domain.StarGiftAmount{Currency: domain.StarGiftCurrencyStars, Amount: req.ChargeStars},
				domain.StarsReasonGiftTransfer, req.To, req.Date, "Star gift transfer")
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE unique_star_gifts SET owner_peer_type=$2,owner_peer_id=$3,updated_at=now() WHERE id=$1`,
				unique.ID, string(req.To.Type), req.To.ID); err != nil {
				return err
			}
			if err := removeSavedGiftFromCollections(ctx, tx, saved.Owner, saved.ID); err != nil {
				return err
			}
			unique.Owner = req.To
			saved.Owner = req.To
			result.Saved, result.Unique, result.Balance = saved, unique, balance
			send.Media.ServiceAction.StarGiftUnique = transferUniqueAction(unique, req.ActorUserID, req.To, saved)
			return nil
		},
		after: func(ctx context.Context, tx pgx.Tx, sent domain.SendPrivateTextResult) error {
			msgID := sent.RecipientMessage.ID
			if req.ActorUserID == req.To.ID {
				msgID = sent.SenderMessage.ID
			}
			if msgID <= 0 {
				return domain.ErrStarGiftTransferUnavailable
			}
			if _, err := tx.Exec(ctx, `UPDATE peer_star_gifts SET owner_peer_type='user',owner_peer_id=$2,from_user_id=$3,
			 msg_id=$4,saved_id=0,upgrade_msg_id=$4,gift_date=$5,name_hidden=false,unsaved=false,pinned_order=0,
			 can_transfer_at=0 WHERE id=$1`, result.Saved.ID, req.To.ID, req.ActorUserID, msgID, req.Date); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO star_gift_transfer_commands(actor_user_id,command_key,unique_gift_id,
			 from_peer_type,from_peer_id,to_peer_type,to_peer_id,charge_stars,balance_after,created_at)
			 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, req.ActorUserID, strings.TrimSpace(req.CommandKey), result.Unique.ID,
				string(req.Ref.Owner.Type), req.Ref.Owner.ID, string(req.To.Type), req.To.ID, req.ChargeStars, result.Balance.Balance, req.Date); err != nil {
				return err
			}
			result.Saved.MsgID, result.Saved.SavedID, result.Saved.UpgradeMsgID, result.Saved.Date = msgID, 0, msgID, req.Date
			result.Saved.FromUserID = req.ActorUserID
			return nil
		},
	}
	sent, err := s.messages.sendPrivateTextWithHooks(ctx, messageReq, hooks)
	if err != nil {
		return domain.StarGiftTransferResult{}, err
	}
	result.Send, result.Duplicate = sent, sent.Duplicate
	if sent.Duplicate {
		return s.loadTransferReplay(ctx, req, sent)
	}
	return result, nil
}

func (s *StarGiftLifecycleStore) PurchaseResaleStarGift(ctx context.Context, req domain.StarGiftResalePurchaseRequest) (domain.StarGiftTransferResult, error) {
	if s == nil || s.messages == nil || req.BuyerUserID <= 0 || strings.TrimSpace(req.Slug) == "" ||
		!validLifecyclePeer(req.To) || !req.Amount.Valid() || req.FormID == 0 ||
		strings.TrimSpace(req.CommandKey) == "" || req.Date <= 0 {
		return domain.StarGiftTransferResult{}, domain.ErrStarGiftResaleUnavailable
	}
	unique, found, err := NewStarGiftStore(s.db).UniqueBySlug(ctx, req.Slug)
	if err != nil || !found {
		return domain.StarGiftTransferResult{}, domain.ErrStarGiftResaleUnavailable
	}
	seller := unique.Owner
	var replayUniqueID, replayFromID, replayToID, replayAmount int64
	var replayFromType, replayToType, replayCurrency string
	replayErr := s.db.QueryRow(ctx, `SELECT t.unique_gift_id,t.from_peer_type,t.from_peer_id,t.to_peer_type,t.to_peer_id,
		s.currency,s.amount FROM star_gift_transfer_commands t
		JOIN star_gift_sales s ON s.command_key=t.command_key AND s.unique_gift_id=t.unique_gift_id
		WHERE t.actor_user_id=$1 AND t.command_key=$2`, req.BuyerUserID, strings.TrimSpace(req.CommandKey)).Scan(
		&replayUniqueID, &replayFromType, &replayFromID, &replayToType, &replayToID, &replayCurrency, &replayAmount)
	if replayErr == nil {
		if replayUniqueID != unique.ID || replayToType != string(req.To.Type) || replayToID != req.To.ID ||
			replayCurrency != string(req.Amount.Currency) || replayAmount != req.Amount.Amount {
			return domain.StarGiftTransferResult{}, domain.ErrStarGiftResaleUnavailable
		}
		seller = domain.Peer{Type: domain.PeerType(replayFromType), ID: replayFromID}
	} else if !errors.Is(replayErr, pgx.ErrNoRows) {
		return domain.StarGiftTransferResult{}, replayErr
	} else if !validLifecyclePeer(unique.Owner) || unique.Owner == req.To {
		return domain.StarGiftTransferResult{}, domain.ErrStarGiftResaleUnavailable
	}
	messageSenderID := domain.OfficialSystemUserID
	if seller.Type == domain.PeerTypeUser {
		messageSenderID = seller.ID
	}
	messageRecipientID := req.BuyerUserID
	if req.To.Type == domain.PeerTypeUser {
		messageRecipientID = req.To.ID
	}
	messageReq := domain.SendPrivateTextRequest{
		SenderUserID: messageSenderID, RecipientUserID: messageRecipientID,
		RandomID: lifecycleCommandRandomID("gift-resale", req.BuyerUserID, req.CommandKey), Date: req.Date,
		OriginAuthKeyID: req.OriginAuthKeyID, OriginSessionID: req.OriginSessionID, OriginUserID: req.BuyerUserID,
		Media: &domain.MessageMedia{Kind: domain.MessageMediaKindService, ServiceAction: &domain.MessageServiceAction{
			Kind: domain.MessageServiceActionStarGiftUnique, StarGiftUnique: &domain.MessageStarGiftUniqueAction{Transferred: true, Saved: true},
		}},
	}
	var result domain.StarGiftTransferResult
	var commissionAmount int64
	hooks := privateSendTxHooks{
		before: func(ctx context.Context, tx pgx.Tx, send *domain.SendPrivateTextRequest) error {
			var listingCurrency, sellerType string
			var listingAmount, sellerID, uniqueID int64
			if err := tx.QueryRow(ctx, `SELECT l.currency,l.amount,l.seller_peer_type,l.seller_peer_id,u.id
			 FROM star_gift_listings l JOIN unique_star_gifts u ON u.id=l.unique_gift_id
			 WHERE lower(u.slug)=lower($1) FOR UPDATE OF l,u`, strings.TrimSpace(req.Slug)).Scan(
				&listingCurrency, &listingAmount, &sellerType, &sellerID, &uniqueID); err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					return domain.ErrStarGiftResaleUnavailable
				}
				return err
			}
			if sellerType != string(seller.Type) || sellerID != seller.ID ||
				listingCurrency != string(req.Amount.Currency) || listingAmount != req.Amount.Amount {
				return domain.ErrStarGiftResaleUnavailable
			}
			saved, found, err := lockSavedStarGiftByUniqueID(ctx, tx, uniqueID)
			if err != nil || !found || !saved.LifecycleStatus.Live() || saved.Owner != seller {
				return domain.ErrStarGiftResaleUnavailable
			}
			gift, found, err := NewStarGiftStore(tx).UniqueByID(ctx, uniqueID)
			if err != nil || !found || gift.Burned || gift.Owner != saved.Owner {
				return domain.ErrStarGiftResaleUnavailable
			}
			balance, err := s.debitLifecycleAmount(ctx, tx, req.BuyerUserID, req.Amount, domain.StarsReasonGiftResale,
				saved.Owner, req.Date, "Collectible gift purchase")
			if err != nil {
				return err
			}
			if _, commission, err := s.creditPeerLifecycleAmount(ctx, tx, seller, req.BuyerUserID, req.Amount,
				domain.StarsReasonGiftResale, domain.Peer{Type: domain.PeerTypeUser, ID: req.BuyerUserID},
				gift.ID, req.Date, "Collectible gift sale"); err != nil {
				return err
			} else {
				commissionAmount = commission
			}
			if err := s.refundPendingStarGiftOffers(ctx, tx, uniqueID, req.Date, "listing purchased"); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `DELETE FROM star_gift_listings WHERE unique_gift_id=$1`, uniqueID); err != nil {
				return err
			}
			if err := removeSavedGiftFromCollections(ctx, tx, saved.Owner, saved.ID); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE unique_star_gifts SET owner_peer_type=$2,owner_peer_id=$3,
			 last_sale_date=$4,last_sale_currency=$5,last_sale_amount=$6,updated_at=now() WHERE id=$1`,
				uniqueID, string(req.To.Type), req.To.ID, req.Date, listingCurrency, listingAmount); err != nil {
				return err
			}
			gift.Owner = req.To
			gift.ResellAmount = nil
			gift.LastSaleDate = req.Date
			gift.LastSaleAmount = &domain.StarGiftAmount{Currency: req.Amount.Currency, Amount: req.Amount.Amount}
			saved.Owner = req.To
			if req.To.Type == domain.PeerTypeChannel {
				saved.MsgID, saved.SavedID = 0, saved.ID
			}
			result.Saved, result.Unique, result.Balance = saved, gift, balance
			send.Media.ServiceAction.StarGiftUnique = transferUniqueAction(gift, messageSenderID, req.To, saved)
			return nil
		},
		after: func(ctx context.Context, tx pgx.Tx, sent domain.SendPrivateTextResult) error {
			msgID, savedID := sent.RecipientMessage.ID, int64(0)
			if req.To.Type == domain.PeerTypeChannel {
				msgID, savedID = 0, result.Saved.ID
			}
			if req.To.Type == domain.PeerTypeUser && msgID <= 0 {
				return domain.ErrStarGiftResaleUnavailable
			}
			if _, err := tx.Exec(ctx, `UPDATE peer_star_gifts SET owner_peer_type=$2,owner_peer_id=$3,from_user_id=$4,
			 msg_id=$5,saved_id=$6,upgrade_msg_id=$5,gift_date=$7,name_hidden=false,unsaved=false,pinned_order=0,can_transfer_at=0
			 WHERE id=$1`, result.Saved.ID, string(req.To.Type), req.To.ID, messageSenderID, msgID, savedID, req.Date); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO star_gift_sales(unique_gift_id,seller_peer_type,seller_peer_id,
			 buyer_peer_type,buyer_peer_id,currency,amount,commission_amount,sold_at,command_key)
			 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, result.Unique.ID, string(seller.Type), seller.ID,
				string(req.To.Type), req.To.ID, string(req.Amount.Currency), req.Amount.Amount, commissionAmount,
				req.Date, strings.TrimSpace(req.CommandKey)); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO star_gift_transfer_commands(actor_user_id,command_key,unique_gift_id,
			 from_peer_type,from_peer_id,to_peer_type,to_peer_id,charge_stars,balance_after,created_at)
			 VALUES($1,$2,$3,$4,$5,$6,$7,0,$8,$9)`, req.BuyerUserID, strings.TrimSpace(req.CommandKey),
				result.Unique.ID, string(seller.Type), seller.ID, string(req.To.Type), req.To.ID, result.Balance.Balance, req.Date); err != nil {
				return err
			}
			if req.To.Type == domain.PeerTypeChannel {
				action := domain.ChannelMessageAction{Type: domain.ChannelActionStarGiftUnique,
					StarGiftUnique: transferUniqueAction(result.Unique, messageSenderID, req.To, result.Saved)}
				if err := NewChannelStore(tx).appendStarGiftAdminLogTx(ctx, tx, req.To.ID, req.BuyerUserID,
					result.Saved.ID, req.Date, action); err != nil {
					return err
				}
			}
			result.Saved.MsgID, result.Saved.SavedID, result.Saved.UpgradeMsgID, result.Saved.Date = msgID, savedID, msgID, req.Date
			result.Saved.FromUserID = messageSenderID
			return updateStarGiftResaleProjection(ctx, tx, result.Unique.GiftID)
		},
	}
	sent, err := s.messages.sendPrivateTextWithHooks(ctx, messageReq, hooks)
	if err != nil {
		return domain.StarGiftTransferResult{}, err
	}
	result.Send, result.Duplicate = sent, sent.Duplicate
	if sent.Duplicate {
		return s.loadTransferReplay(ctx, domain.StarGiftTransferRequest{ActorUserID: req.BuyerUserID, CommandKey: req.CommandKey}, sent)
	}
	return result, nil
}

func lockSavedStarGiftByUniqueID(ctx context.Context, tx pgx.Tx, uniqueID int64) (domain.SavedStarGift, bool, error) {
	row := tx.QueryRow(ctx, `SELECT p.id,p.owner_peer_type,p.owner_peer_id,p.from_user_id,p.gift_id,p.catalog_revision_id,
	 p.msg_id,p.saved_id,p.gift_date,p.name_hidden,p.unsaved,p.converted,p.convert_stars,p.prepaid_upgrade_stars,p.prepaid_upgrade_hash,p.gift_num,
	 p.lifecycle_status,p.transfer_stars,p.can_export_at,p.can_transfer_at,p.can_resell_at,p.drop_original_details_stars,p.can_craft_at,
	 p.message,COALESCE(p.unique_gift_id,0),p.upgrade_msg_id,p.pinned_order,
	 COALESCE((SELECT array_agg(i.collection_id ORDER BY c.sort_order,i.collection_id) FROM star_gift_collection_items i
	 JOIN star_gift_collections c ON c.collection_id=i.collection_id WHERE i.saved_gift_id=p.id),ARRAY[]::integer[])
	 FROM peer_star_gifts p WHERE p.unique_gift_id=$1 FOR UPDATE`, uniqueID)
	saved, err := scanSavedStarGift(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.SavedStarGift{}, false, nil
	}
	return saved, err == nil, err
}

func (s *StarGiftLifecycleStore) refundPendingStarGiftOffers(ctx context.Context, tx pgx.Tx, uniqueID int64, date int, reason string) error {
	rows, err := tx.Query(ctx, `SELECT id,buyer_user_id,currency,amount,owner_peer_type,owner_peer_id
	 FROM star_gift_offers WHERE unique_gift_id=$1 AND status='pending' FOR UPDATE`, uniqueID)
	if err != nil {
		return err
	}
	type pending struct {
		id, buyer, amount, ownerID int64
		currency, ownerType        string
	}
	items := make([]pending, 0)
	for rows.Next() {
		var item pending
		if err := rows.Scan(&item.id, &item.buyer, &item.currency, &item.amount, &item.ownerType, &item.ownerID); err != nil {
			rows.Close()
			return err
		}
		items = append(items, item)
	}
	rows.Close()
	for _, item := range items {
		if err := s.creditLifecycleAmount(ctx, tx, item.buyer, domain.StarGiftAmount{Currency: domain.StarGiftCurrency(item.currency), Amount: item.amount},
			domain.StarsReasonGiftOffer, domain.Peer{Type: domain.PeerType(item.ownerType), ID: item.ownerID}, date, "Gift offer refund"); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE star_gift_offers SET status='cancelled',resolved_at=$2 WHERE id=$1`, item.id, date); err != nil {
			return err
		}
	}
	_ = reason
	return nil
}

func (s *StarGiftLifecycleStore) SendStarGiftOffer(ctx context.Context, req domain.StarGiftOfferRequest) (domain.StarGiftOfferResult, error) {
	if s == nil || s.messages == nil || req.BuyerUserID <= 0 || req.Owner.Type != domain.PeerTypeUser ||
		req.Owner.ID <= 0 || req.Owner.ID == req.BuyerUserID || strings.TrimSpace(req.Slug) == "" ||
		!req.Price.Valid() || !validStarGiftOfferDuration(req.Duration) || req.RandomID == 0 || req.Date <= 0 {
		return domain.StarGiftOfferResult{}, domain.ErrStarGiftOfferInvalid
	}
	if err := s.expireStarGiftOffers(ctx, req.Date); err != nil {
		return domain.StarGiftOfferResult{}, err
	}
	unique, found, err := NewStarGiftStore(s.db).UniqueBySlug(ctx, req.Slug)
	if err != nil || !found || unique.Owner != req.Owner || unique.Burned || unique.OwnerAddress != "" || unique.OfferMinStars <= 0 {
		return domain.StarGiftOfferResult{}, domain.ErrStarGiftOfferInvalid
	}
	messageReq := domain.SendPrivateTextRequest{
		SenderUserID: req.BuyerUserID, RecipientUserID: req.Owner.ID, RandomID: req.RandomID, Date: req.Date,
		OriginAuthKeyID: req.OriginAuthKeyID, OriginSessionID: req.OriginSessionID, OriginUserID: req.BuyerUserID,
		Media: &domain.MessageMedia{Kind: domain.MessageMediaKindService, ServiceAction: &domain.MessageServiceAction{
			Kind: domain.MessageServiceActionStarGiftOffer, StarGiftOffer: &domain.MessageStarGiftOfferAction{
				Gift: unique, Price: req.Price, ExpiresAt: req.Date + req.Duration,
			},
		}},
	}
	var result domain.StarGiftOfferResult
	hooks := privateSendTxHooks{
		before: func(ctx context.Context, tx pgx.Tx, send *domain.SendPrivateTextRequest) error {
			// Serialize a new pending offer with Craft/transfer/export, all of
			// which close market claims before changing the gift lifecycle.
			if _, err := tx.Exec(ctx, `SELECT id FROM unique_star_gifts WHERE id=$1 FOR UPDATE`, unique.ID); err != nil {
				return err
			}
			gift, found, err := NewStarGiftStore(tx).UniqueByID(ctx, unique.ID)
			if err != nil || !found || gift.Owner != req.Owner || gift.Burned || gift.OwnerAddress != "" || gift.OfferMinStars <= 0 {
				return domain.ErrStarGiftOfferInvalid
			}
			if req.Price.Currency == domain.StarGiftCurrencyStars && gift.OfferMinStars > 0 && req.Price.Amount < int64(gift.OfferMinStars) {
				return domain.ErrStarGiftOfferInvalid
			}
			balance, err := s.debitLifecycleAmount(ctx, tx, req.BuyerUserID, req.Price, domain.StarsReasonGiftOffer,
				req.Owner, req.Date, "Collectible gift offer")
			if err != nil {
				return err
			}
			var offerID int64
			if err := tx.QueryRow(ctx, `INSERT INTO star_gift_offers(buyer_user_id,owner_peer_type,owner_peer_id,
			 unique_gift_id,currency,amount,random_id,created_at,expires_at,balance_after)
			 VALUES($1,'user',$2,$3,$4,$5,$6,$7,$8,$9) RETURNING id`, req.BuyerUserID, req.Owner.ID,
				gift.ID, string(req.Price.Currency), req.Price.Amount, req.RandomID, req.Date, req.Date+req.Duration, balance.Balance).Scan(&offerID); err != nil {
				return err
			}
			result.Offer = domain.StarGiftOffer{ID: offerID, BuyerUserID: req.BuyerUserID, Owner: req.Owner,
				UniqueGiftID: gift.ID, Price: req.Price, RandomID: req.RandomID, Status: "pending",
				CreatedAt: req.Date, ExpiresAt: req.Date + req.Duration, Gift: gift}
			result.Balance = balance
			send.Media.ServiceAction.StarGiftOffer.Gift = gift
			return nil
		},
		after: func(ctx context.Context, tx pgx.Tx, sent domain.SendPrivateTextResult) error {
			ownerMsgID := sent.RecipientMessage.ID
			buyerMsgID := sent.SenderMessage.ID
			if _, err := tx.Exec(ctx, `UPDATE star_gift_offers SET offer_msg_id=$2,buyer_msg_id=$3 WHERE id=$1`, result.Offer.ID, ownerMsgID, buyerMsgID); err != nil {
				return err
			}
			result.Offer.OfferMsgID, result.Offer.BuyerMsgID = ownerMsgID, buyerMsgID
			return nil
		},
	}
	sent, err := s.messages.sendPrivateTextWithHooks(ctx, messageReq, hooks)
	if err != nil {
		return domain.StarGiftOfferResult{}, err
	}
	result.Send, result.Duplicate = sent, sent.Duplicate
	if sent.Duplicate {
		return s.loadOfferByBuyerRandom(ctx, req.BuyerUserID, req.RandomID, sent)
	}
	return result, nil
}

func validStarGiftOfferDuration(duration int) bool {
	switch duration {
	case 120, 21600, 43200, 86400, 129600, 172800, 259200:
		return true
	default:
		return false
	}
}

func (s *StarGiftLifecycleStore) loadOfferByBuyerRandom(ctx context.Context, buyerUserID, randomID int64, sent domain.SendPrivateTextResult) (domain.StarGiftOfferResult, error) {
	offer, err := scanStarGiftOffer(s.db.QueryRow(ctx, `SELECT id,buyer_user_id,owner_peer_type,owner_peer_id,unique_gift_id,
	 currency,amount,random_id,offer_msg_id,buyer_msg_id,status,created_at,expires_at,resolved_at,balance_after
	 FROM star_gift_offers WHERE buyer_user_id=$1 AND random_id=$2`, buyerUserID, randomID))
	if err != nil {
		return domain.StarGiftOfferResult{}, err
	}
	gift, found, err := NewStarGiftStore(s.db).UniqueByID(ctx, offer.UniqueGiftID)
	if err != nil || !found {
		return domain.StarGiftOfferResult{}, domain.ErrStarGiftOfferInvalid
	}
	offer.Gift = gift
	var balance int64
	if offer.Price.Currency == domain.StarGiftCurrencyTON {
		_ = s.db.QueryRow(ctx, `SELECT balance_nanoton FROM ton_balances WHERE user_id=$1`, buyerUserID).Scan(&balance)
	} else {
		_ = s.db.QueryRow(ctx, `SELECT balance FROM stars_balances WHERE user_id=$1`, buyerUserID).Scan(&balance)
	}
	return domain.StarGiftOfferResult{Offer: offer, Balance: domain.StarsBalance{UserID: buyerUserID, Balance: balance}, Send: sent, Duplicate: true}, nil
}

func scanStarGiftOffer(row rowScanner) (domain.StarGiftOffer, error) {
	var offer domain.StarGiftOffer
	var ownerType, currency string
	if err := row.Scan(&offer.ID, &offer.BuyerUserID, &ownerType, &offer.Owner.ID, &offer.UniqueGiftID,
		&currency, &offer.Price.Amount, &offer.RandomID, &offer.OfferMsgID, &offer.BuyerMsgID,
		&offer.Status, &offer.CreatedAt, &offer.ExpiresAt, &offer.ResolvedAt, new(int64)); err != nil {
		return domain.StarGiftOffer{}, err
	}
	offer.Owner.Type = domain.PeerType(ownerType)
	offer.Price.Currency = domain.StarGiftCurrency(currency)
	return offer, nil
}

func (s *StarGiftLifecycleStore) ResolveStarGiftOffer(ctx context.Context, req domain.StarGiftResolveOfferRequest) (domain.StarGiftOfferResult, error) {
	if s == nil || s.messages == nil || req.OwnerUserID <= 0 || req.OfferMsgID <= 0 || req.Date <= 0 {
		return domain.StarGiftOfferResult{}, domain.ErrStarGiftOfferInvalid
	}
	if err := s.expireStarGiftOffers(ctx, req.Date); err != nil {
		return domain.StarGiftOfferResult{}, err
	}
	offer, err := scanStarGiftOffer(s.db.QueryRow(ctx, `SELECT id,buyer_user_id,owner_peer_type,owner_peer_id,unique_gift_id,
	 currency,amount,random_id,offer_msg_id,buyer_msg_id,status,created_at,expires_at,resolved_at,balance_after
	 FROM star_gift_offers WHERE owner_peer_type='user' AND owner_peer_id=$1 AND offer_msg_id=$2`, req.OwnerUserID, req.OfferMsgID))
	if err != nil || offer.Status != "pending" || offer.ExpiresAt <= req.Date {
		return domain.StarGiftOfferResult{}, domain.ErrStarGiftOfferExpired
	}
	gift, found, err := NewStarGiftStore(s.db).UniqueByID(ctx, offer.UniqueGiftID)
	if err != nil || !found {
		return domain.StarGiftOfferResult{}, domain.ErrStarGiftOfferInvalid
	}
	offer.Gift = gift
	actionKind := domain.MessageServiceActionStarGiftUnique
	action := &domain.MessageServiceAction{Kind: actionKind, StarGiftUnique: &domain.MessageStarGiftUniqueAction{
		Gift: gift, FromUserID: req.OwnerUserID, Peer: domain.Peer{Type: domain.PeerTypeUser, ID: offer.BuyerUserID},
		Transferred: true, FromOffer: true, Saved: true,
	}}
	if req.Decline {
		action = &domain.MessageServiceAction{Kind: domain.MessageServiceActionStarGiftOfferDeclined,
			StarGiftOfferDeclined: &domain.MessageStarGiftOfferDeclinedAction{Gift: gift, Price: offer.Price}}
	}
	messageReq := domain.SendPrivateTextRequest{SenderUserID: req.OwnerUserID, RecipientUserID: offer.BuyerUserID,
		RandomID: lifecycleCommandRandomID("resolve-offer", offer.ID, req.Decline), Date: req.Date,
		OriginAuthKeyID: req.OriginAuthKeyID, OriginSessionID: req.OriginSessionID, OriginUserID: req.OwnerUserID,
		Media: &domain.MessageMedia{Kind: domain.MessageMediaKindService, ServiceAction: action}}
	var result domain.StarGiftOfferResult
	var commissionAmount int64
	hooks := privateSendTxHooks{
		before: func(ctx context.Context, tx pgx.Tx, send *domain.SendPrivateTextRequest) error {
			locked, err := scanStarGiftOffer(tx.QueryRow(ctx, `SELECT id,buyer_user_id,owner_peer_type,owner_peer_id,unique_gift_id,
			 currency,amount,random_id,offer_msg_id,buyer_msg_id,status,created_at,expires_at,resolved_at,balance_after
			 FROM star_gift_offers WHERE id=$1 FOR UPDATE`, offer.ID))
			if err != nil || locked.Status != "pending" || locked.ExpiresAt <= req.Date {
				return domain.ErrStarGiftOfferExpired
			}
			locked.Gift = gift
			if req.Decline {
				if err := s.creditLifecycleAmount(ctx, tx, locked.BuyerUserID, locked.Price, domain.StarsReasonGiftOffer,
					locked.Owner, req.Date, "Gift offer refund"); err != nil {
					return err
				}
				if _, err := tx.Exec(ctx, `UPDATE star_gift_offers SET status='declined',resolved_at=$2,resolution_notified=true WHERE id=$1`, locked.ID, req.Date); err != nil {
					return err
				}
				locked.Status, locked.ResolvedAt = "declined", req.Date
				result.Offer = locked
				return nil
			}
			saved, found, err := lockSavedStarGiftByUniqueID(ctx, tx, locked.UniqueGiftID)
			if err != nil || !found || saved.Owner != locked.Owner || !saved.LifecycleStatus.Live() {
				return domain.ErrStarGiftOfferInvalid
			}
			current, found, err := NewStarGiftStore(tx).UniqueByID(ctx, locked.UniqueGiftID)
			if err != nil || !found || current.Owner != locked.Owner || current.Burned || current.OwnerAddress != "" {
				return domain.ErrStarGiftOfferInvalid
			}
			if _, commission, err := s.creditPeerLifecycleAmount(ctx, tx, locked.Owner, req.OwnerUserID, locked.Price,
				domain.StarsReasonGiftOffer, domain.Peer{Type: domain.PeerTypeUser, ID: locked.BuyerUserID},
				current.ID, req.Date, "Accepted gift offer"); err != nil {
				return err
			} else {
				commissionAmount = commission
			}
			if err := removeSavedGiftFromCollections(ctx, tx, saved.Owner, saved.ID); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `DELETE FROM star_gift_listings WHERE unique_gift_id=$1`, current.ID); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE unique_star_gifts SET owner_peer_type='user',owner_peer_id=$2,
			 last_sale_date=$3,last_sale_currency=$4,last_sale_amount=$5,updated_at=now() WHERE id=$1`, current.ID,
				locked.BuyerUserID, req.Date, string(locked.Price.Currency), locked.Price.Amount); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE star_gift_offers SET status='accepted',resolved_at=$2,resolution_notified=true WHERE id=$1`, locked.ID, req.Date); err != nil {
				return err
			}
			if err := s.refundPendingStarGiftOffersExcept(ctx, tx, current.ID, locked.ID, req.Date); err != nil {
				return err
			}
			current.Owner = domain.Peer{Type: domain.PeerTypeUser, ID: locked.BuyerUserID}
			current.ResellAmount = nil
			current.LastSaleDate = req.Date
			current.LastSaleAmount = &locked.Price
			locked.Status, locked.ResolvedAt, locked.Gift = "accepted", req.Date, current
			result.Offer = locked
			result.Unique = current
			result.Saved = saved
			send.Media.ServiceAction.StarGiftUnique.Gift = current
			return nil
		},
		after: func(ctx context.Context, tx pgx.Tx, sent domain.SendPrivateTextResult) error {
			if req.Decline {
				return nil
			}
			msgID := sent.RecipientMessage.ID
			if _, err := tx.Exec(ctx, `UPDATE peer_star_gifts SET owner_peer_type='user',owner_peer_id=$2,from_user_id=$3,
			 msg_id=$4,saved_id=0,upgrade_msg_id=$4,gift_date=$5,name_hidden=false,unsaved=false,pinned_order=0,can_transfer_at=0
			 WHERE id=$1`, result.Saved.ID, result.Offer.BuyerUserID, req.OwnerUserID, msgID, req.Date); err != nil {
				return err
			}
			result.Saved.Owner = domain.Peer{Type: domain.PeerTypeUser, ID: result.Offer.BuyerUserID}
			result.Saved.FromUserID, result.Saved.MsgID, result.Saved.SavedID, result.Saved.UpgradeMsgID, result.Saved.Date = req.OwnerUserID, msgID, 0, msgID, req.Date
			if _, err := tx.Exec(ctx, `INSERT INTO star_gift_sales(unique_gift_id,seller_peer_type,seller_peer_id,
			 buyer_peer_type,buyer_peer_id,currency,amount,commission_amount,sold_at,command_key)
			 VALUES($1,'user',$2,'user',$3,$4,$5,$6,$7,$8)`, result.Offer.UniqueGiftID, req.OwnerUserID,
				result.Offer.BuyerUserID, string(result.Offer.Price.Currency), result.Offer.Price.Amount, commissionAmount,
				req.Date, fmt.Sprintf("offer:%d", result.Offer.ID)); err != nil {
				return err
			}
			return updateStarGiftResaleProjection(ctx, tx, result.Offer.Gift.GiftID)
		},
	}
	sent, err := s.messages.sendPrivateTextWithHooks(ctx, messageReq, hooks)
	if err != nil {
		return domain.StarGiftOfferResult{}, err
	}
	if sent.Duplicate {
		reloaded, loadErr := s.loadOfferByBuyerRandom(ctx, offer.BuyerUserID, offer.RandomID, sent)
		if loadErr != nil {
			return domain.StarGiftOfferResult{}, loadErr
		}
		reloaded.Duplicate = true
		return reloaded, nil
	}
	result.Send = sent
	return result, nil
}

func (s *StarGiftLifecycleStore) expireStarGiftOffers(ctx context.Context, now int) error {
	if now <= 0 || s.messages == nil {
		return nil
	}
	if _, err := s.expireStarGiftOffersBatch(ctx, now, 100); err != nil {
		return err
	}
	_, err := s.dispatchStarGiftOfferResolutions(ctx, 100)
	return err
}

func (s *StarGiftLifecycleStore) expireStarGiftOffersBatch(ctx context.Context, now, limit int) (int, error) {
	if now <= 0 || limit <= 0 {
		return 0, nil
	}
	processed := 0
	err := withTx(ctx, s.db, "expire star gift offers", func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id,buyer_user_id,owner_peer_type,owner_peer_id,unique_gift_id,currency,amount
FROM star_gift_offers WHERE status='pending' AND expires_at<=$1 ORDER BY expires_at,id LIMIT $2 FOR UPDATE SKIP LOCKED`, now, limit)
		if err != nil {
			return err
		}
		type expired struct {
			id, buyer, ownerID, uniqueID, amount int64
			ownerType, currency                  string
		}
		items := make([]expired, 0)
		for rows.Next() {
			var item expired
			if err := rows.Scan(&item.id, &item.buyer, &item.ownerType, &item.ownerID, &item.uniqueID, &item.currency, &item.amount); err != nil {
				rows.Close()
				return err
			}
			items = append(items, item)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		for _, item := range items {
			owner := domain.Peer{Type: domain.PeerType(item.ownerType), ID: item.ownerID}
			if err := s.creditLifecycleAmount(ctx, tx, item.buyer,
				domain.StarGiftAmount{Currency: domain.StarGiftCurrency(item.currency), Amount: item.amount},
				domain.StarsReasonGiftOffer, owner, now, "Expired gift offer refund"); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE star_gift_offers SET status='expired',resolved_at=$2 WHERE id=$1`, item.id, now); err != nil {
				return err
			}
		}
		processed = len(items)
		return nil
	})
	return processed, err
}

func (s *StarGiftLifecycleStore) dispatchStarGiftOfferResolutions(ctx context.Context, limit int) (int, error) {
	if limit <= 0 || s.messages == nil {
		return 0, nil
	}
	rows, err := s.db.Query(ctx, `SELECT id,buyer_user_id,owner_peer_id,unique_gift_id,currency,amount,resolved_at,status
FROM star_gift_offers WHERE status IN ('expired','cancelled') AND NOT resolution_notified ORDER BY id LIMIT $1`, limit)
	if err != nil {
		return 0, err
	}
	type notice struct {
		id, buyer, owner, uniqueID, amount int64
		currency, status                   string
		date                               int
	}
	items := make([]notice, 0)
	for rows.Next() {
		var item notice
		if err := rows.Scan(&item.id, &item.buyer, &item.owner, &item.uniqueID, &item.currency, &item.amount, &item.date, &item.status); err != nil {
			rows.Close()
			return 0, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	rows.Close()
	for _, item := range items {
		gift, found, err := NewStarGiftStore(s.db).UniqueByID(ctx, item.uniqueID)
		if err != nil || !found {
			return 0, domain.ErrStarGiftOfferInvalid
		}
		_, err = s.messages.SendPrivateText(ctx, domain.SendPrivateTextRequest{SenderUserID: item.owner, RecipientUserID: item.buyer,
			RandomID: lifecycleCommandRandomID("resolve-offer-outbox", item.id, item.status), Date: item.date,
			Media: &domain.MessageMedia{Kind: domain.MessageMediaKindService, ServiceAction: &domain.MessageServiceAction{
				Kind: domain.MessageServiceActionStarGiftOfferDeclined, StarGiftOfferDeclined: &domain.MessageStarGiftOfferDeclinedAction{
					Gift: gift, Price: domain.StarGiftAmount{Currency: domain.StarGiftCurrency(item.currency), Amount: item.amount}, Expired: item.status == "expired"}}}})
		if err != nil {
			return 0, err
		}
		if _, err := s.db.Exec(ctx, `UPDATE star_gift_offers SET resolution_notified=true WHERE id=$1 AND status=$2`, item.id, item.status); err != nil {
			return 0, err
		}
	}
	return len(items), nil
}

func (s *StarGiftLifecycleStore) refundPendingStarGiftOffersExcept(ctx context.Context, tx pgx.Tx, uniqueID, exceptID int64, date int) error {
	rows, err := tx.Query(ctx, `SELECT id,buyer_user_id,currency,amount,owner_peer_type,owner_peer_id
	 FROM star_gift_offers WHERE unique_gift_id=$1 AND status='pending' AND id<>$2 FOR UPDATE`, uniqueID, exceptID)
	if err != nil {
		return err
	}
	type item struct {
		id, buyer, amount, ownerID int64
		currency, ownerType        string
	}
	items := make([]item, 0)
	for rows.Next() {
		var v item
		if err := rows.Scan(&v.id, &v.buyer, &v.currency, &v.amount, &v.ownerType, &v.ownerID); err != nil {
			rows.Close()
			return err
		}
		items = append(items, v)
	}
	rows.Close()
	for _, v := range items {
		if err := s.creditLifecycleAmount(ctx, tx, v.buyer, domain.StarGiftAmount{Currency: domain.StarGiftCurrency(v.currency), Amount: v.amount},
			domain.StarsReasonGiftOffer, domain.Peer{Type: domain.PeerType(v.ownerType), ID: v.ownerID}, date, "Gift offer refund"); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE star_gift_offers SET status='cancelled',resolved_at=$2 WHERE id=$1`, v.id, date); err != nil {
			return err
		}
	}
	return nil
}

func (s *StarGiftLifecycleStore) transferStarGiftWithoutPrivateMessage(ctx context.Context, req domain.StarGiftTransferRequest) (domain.StarGiftTransferResult, error) {
	var result domain.StarGiftTransferResult
	err := withTx(ctx, s.db, "transfer star gift to channel", func(tx pgx.Tx) error {
		saved, unique, err := lockTransferableStarGift(ctx, tx, req.ActorUserID, req.Ref, req.Date)
		if err != nil {
			return err
		}
		if saved.TransferStars != req.ChargeStars {
			return domain.ErrStarGiftTransferUnavailable
		}
		if err := ensureNoStarGiftMarketConflict(ctx, tx, unique.ID); err != nil {
			return err
		}
		balance, err := s.debitLifecycleAmount(ctx, tx, req.ActorUserID,
			domain.StarGiftAmount{Currency: domain.StarGiftCurrencyStars, Amount: req.ChargeStars},
			domain.StarsReasonGiftTransfer, req.To, req.Date, "Star gift transfer")
		if err != nil {
			return err
		}
		if err := removeSavedGiftFromCollections(ctx, tx, saved.Owner, saved.ID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE unique_star_gifts SET owner_peer_type='channel',owner_peer_id=$2,updated_at=now() WHERE id=$1`, unique.ID, req.To.ID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE peer_star_gifts SET owner_peer_type='channel',owner_peer_id=$2,from_user_id=$3,
		 msg_id=0,saved_id=id,upgrade_msg_id=0,gift_date=$4,name_hidden=false,unsaved=false,pinned_order=0,can_transfer_at=0 WHERE id=$1`,
			saved.ID, req.To.ID, req.ActorUserID, req.Date); err != nil {
			return err
		}
		unique.Owner = req.To
		saved.Owner, saved.MsgID, saved.SavedID, saved.UpgradeMsgID, saved.Date = req.To, 0, saved.ID, 0, req.Date
		saved.FromUserID = req.ActorUserID
		action := domain.ChannelMessageAction{Type: domain.ChannelActionStarGiftUnique,
			StarGiftUnique: transferUniqueAction(unique, req.ActorUserID, req.To, saved)}
		if err := NewChannelStore(tx).appendStarGiftAdminLogTx(ctx, tx, req.To.ID, req.ActorUserID, saved.ID, req.Date, action); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO star_gift_transfer_commands(actor_user_id,command_key,unique_gift_id,
		 from_peer_type,from_peer_id,to_peer_type,to_peer_id,charge_stars,balance_after,created_at)
		 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, req.ActorUserID, strings.TrimSpace(req.CommandKey), unique.ID,
			string(req.Ref.Owner.Type), req.Ref.Owner.ID, string(req.To.Type), req.To.ID, req.ChargeStars, balance.Balance, req.Date); err != nil {
			return err
		}
		result.Saved, result.Unique, result.Balance = saved, unique, balance
		return nil
	})
	return result, err
}

func lockTransferableStarGift(ctx context.Context, tx pgx.Tx, actorUserID int64, ref domain.SavedStarGiftRef, now int) (domain.SavedStarGift, domain.UniqueStarGift, error) {
	saved, unique, err := lockOwnedUniqueStarGift(ctx, tx, actorUserID, ref)
	if err != nil || saved.CanTransferAt > now {
		return domain.SavedStarGift{}, domain.UniqueStarGift{}, domain.ErrStarGiftTransferUnavailable
	}
	return saved, unique, nil
}

// lockOwnedUniqueStarGift locks the live ownership aggregate without applying a
// transfer cooldown. Independent capabilities such as dropping original details
// must not be accidentally blocked by can_transfer_at.
func lockOwnedUniqueStarGift(ctx context.Context, tx pgx.Tx, actorUserID int64, ref domain.SavedStarGiftRef) (domain.SavedStarGift, domain.UniqueStarGift, error) {
	saved, err := lockSavedStarGiftForUpgrade(ctx, tx, ref)
	if err != nil {
		return domain.SavedStarGift{}, domain.UniqueStarGift{}, err
	}
	if !saved.LifecycleStatus.Live() || saved.UniqueGiftID == 0 || saved.Owner != ref.Owner ||
		saved.Owner.Type == domain.PeerTypeUser && saved.Owner.ID != actorUserID {
		return domain.SavedStarGift{}, domain.UniqueStarGift{}, domain.ErrStarGiftTransferUnavailable
	}
	unique, found, err := NewStarGiftStore(tx).UniqueByID(ctx, saved.UniqueGiftID)
	if err != nil {
		return domain.SavedStarGift{}, domain.UniqueStarGift{}, err
	}
	if !found || unique.Burned || unique.OwnerAddress != "" || unique.Owner != saved.Owner {
		return domain.SavedStarGift{}, domain.UniqueStarGift{}, domain.ErrStarGiftTransferUnavailable
	}
	return saved, unique, nil
}

func ensureNoStarGiftMarketConflict(ctx context.Context, tx pgx.Tx, uniqueID int64) error {
	var listing, offers bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM star_gift_listings WHERE unique_gift_id=$1),
	 EXISTS(SELECT 1 FROM star_gift_offers WHERE unique_gift_id=$1 AND status='pending')`, uniqueID).Scan(&listing, &offers); err != nil {
		return err
	}
	if listing || offers {
		return domain.ErrStarGiftTransferUnavailable
	}
	return nil
}

func transferUniqueAction(unique domain.UniqueStarGift, fromUserID int64, to domain.Peer, saved domain.SavedStarGift) *domain.MessageStarGiftUniqueAction {
	return &domain.MessageStarGiftUniqueAction{Gift: unique, FromUserID: fromUserID, Peer: to,
		SavedID: saved.SavedID, Transferred: true, Saved: true, CanExportAt: saved.CanExportAt,
		TransferStars: saved.TransferStars, CanTransferAt: saved.CanTransferAt, CanResellAt: saved.CanResellAt,
		DropOriginalDetailsStars: saved.DropOriginalDetailsStars, CanCraftAt: saved.CanCraftAt}
}

func (s *StarGiftLifecycleStore) debitLifecycleAmount(ctx context.Context, tx pgx.Tx, userID int64, amount domain.StarGiftAmount,
	reason domain.StarsTransactionReason, peer domain.Peer, date int, title string) (domain.StarsBalance, error) {
	if amount.Amount == 0 {
		var balance domain.StarsBalance
		balance.UserID = userID
		err := tx.QueryRow(ctx, `SELECT balance,granted FROM stars_balances WHERE user_id=$1`, userID).Scan(&balance.Balance, &balance.Granted)
		if errors.Is(err, pgx.ErrNoRows) {
			return balance, nil
		}
		return balance, err
	}
	if amount.Currency == domain.StarGiftCurrencyTON {
		if _, err := s.ensureTonGrantTx(ctx, tx, userID, date); err != nil {
			return domain.StarsBalance{}, err
		}
		var balance int64
		if err := tx.QueryRow(ctx, `UPDATE ton_balances SET balance_nanoton=balance_nanoton-$2,updated_at=now()
		 WHERE user_id=$1 AND balance_nanoton>=$2 RETURNING balance_nanoton`, userID, amount.Amount).Scan(&balance); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return domain.StarsBalance{}, domain.ErrStarsInsufficient
			}
			return domain.StarsBalance{}, err
		}
		_, err := tx.Exec(ctx, `INSERT INTO ton_transactions(user_id,amount_nanoton,reason,peer_type,peer_id,date)
		 VALUES($1,$2,$3,$4,$5,$6)`, userID, -amount.Amount, string(reason), nullableStarGiftPeerType(peer), nullableStarGiftPeerID(peer), date)
		return domain.StarsBalance{UserID: userID, Balance: balance}, err
	}
	result := domain.StarsBalance{UserID: userID}
	var current int64
	if err := tx.QueryRow(ctx, `SELECT balance,granted FROM stars_balances WHERE user_id=$1 FOR UPDATE`, userID).Scan(&current, &result.Granted); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.StarsBalance{}, domain.ErrStarsInsufficient
		}
		return domain.StarsBalance{}, err
	}
	if current < amount.Amount {
		return domain.StarsBalance{}, domain.ErrStarsInsufficient
	}
	if err := tx.QueryRow(ctx, `UPDATE stars_balances SET balance=balance-$2,updated_at=now() WHERE user_id=$1 RETURNING balance`, userID, amount.Amount).Scan(&result.Balance); err != nil {
		return domain.StarsBalance{}, err
	}
	if err := insertStarsTxn(ctx, tx, userID, -amount.Amount, reason, peer, date, title, ""); err != nil {
		return domain.StarsBalance{}, err
	}
	return result, nil
}

func (s *StarGiftLifecycleStore) creditLifecycleAmount(ctx context.Context, tx pgx.Tx, userID int64, amount domain.StarGiftAmount,
	reason domain.StarsTransactionReason, peer domain.Peer, date int, title string) error {
	if amount.Currency == domain.StarGiftCurrencyTON {
		if _, err := tx.Exec(ctx, `INSERT INTO ton_balances(user_id,balance_nanoton,granted) VALUES($1,$2,false)
		 ON CONFLICT(user_id) DO UPDATE SET balance_nanoton=ton_balances.balance_nanoton+EXCLUDED.balance_nanoton,updated_at=now()`, userID, amount.Amount); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO ton_transactions(user_id,amount_nanoton,reason,peer_type,peer_id,date)
		 VALUES($1,$2,$3,$4,$5,$6)`, userID, amount.Amount, string(reason), nullableStarGiftPeerType(peer), nullableStarGiftPeerID(peer), date)
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO stars_balances(user_id,balance,updated_at) VALUES($1,$2,now())
	 ON CONFLICT(user_id) DO UPDATE SET balance=stars_balances.balance+EXCLUDED.balance,updated_at=now()`, userID, amount.Amount); err != nil {
		return err
	}
	return insertStarsTxn(ctx, tx, userID, amount.Amount, reason, peer, date, title, "")
}

// creditPeerLifecycleAmount credits marketplace proceeds to the actual gift
// owner. Channel Stars and TON are isolated local revenue ledgers; neither is
// redirected to the administrator who happened to execute the RPC.
func (s *StarGiftLifecycleStore) creditPeerLifecycleAmount(ctx context.Context, tx pgx.Tx, owner domain.Peer,
	actorUserID int64, amount domain.StarGiftAmount, reason domain.StarsTransactionReason, counterparty domain.Peer,
	giftID int64, date int, title string) (int64, int64, error) {
	if !validLifecyclePeer(owner) || actorUserID <= 0 || !amount.Valid() || giftID <= 0 || date <= 0 {
		return 0, 0, domain.ErrStarGiftResaleUnavailable
	}
	permille := s.market.StarsProceedsPermille
	if amount.Currency == domain.StarGiftCurrencyTON {
		permille = s.market.TONProceedsPermille
	}
	proceeds := amount.Amount/1000*int64(permille) + amount.Amount%1000*int64(permille)/1000
	commission := amount.Amount - proceeds
	credited := amount
	credited.Amount = proceeds
	if owner.Type == domain.PeerTypeUser {
		if proceeds > 0 {
			if err := s.creditLifecycleAmount(ctx, tx, owner.ID, credited, reason, counterparty, date, title); err != nil {
				return 0, 0, err
			}
		}
		var balance int64
		if amount.Currency == domain.StarGiftCurrencyTON {
			if err := tx.QueryRow(ctx, `SELECT COALESCE((SELECT balance_nanoton FROM ton_balances WHERE user_id=$1),0)`, owner.ID).Scan(&balance); err != nil {
				return 0, 0, err
			}
		} else if err := tx.QueryRow(ctx, `SELECT COALESCE((SELECT balance FROM stars_balances WHERE user_id=$1),0)`, owner.ID).Scan(&balance); err != nil {
			return 0, 0, err
		}
		return balance, commission, nil
	}

	var balance int64
	if amount.Currency == domain.StarGiftCurrencyTON {
		if proceeds == 0 {
			err := tx.QueryRow(ctx, `SELECT COALESCE((SELECT balance_nanoton FROM channel_ton_balances WHERE channel_id=$1),0)`, owner.ID).Scan(&balance)
			return balance, commission, err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO channel_ton_balances(channel_id,balance_nanoton) VALUES($1,$2)
			ON CONFLICT(channel_id) DO UPDATE SET balance_nanoton=channel_ton_balances.balance_nanoton+EXCLUDED.balance_nanoton,updated_at=now()
			RETURNING balance_nanoton`, owner.ID, proceeds).Scan(&balance); err != nil {
			return 0, 0, err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO channel_ton_transactions
			(channel_id,actor_user_id,amount_nanoton,reason,peer_type,peer_id,gift_id,date)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, owner.ID, actorUserID, proceeds, string(reason),
			string(counterparty.Type), counterparty.ID, giftID, date); err != nil {
			return 0, 0, err
		}
		return balance, commission, nil
	}
	if proceeds == 0 {
		err := tx.QueryRow(ctx, `SELECT COALESCE((SELECT balance FROM channel_stars_balances WHERE channel_id=$1),0)`, owner.ID).Scan(&balance)
		return balance, commission, err
	}
	if err := tx.QueryRow(ctx, `INSERT INTO channel_stars_balances(channel_id,balance) VALUES($1,$2)
		ON CONFLICT(channel_id) DO UPDATE SET balance=channel_stars_balances.balance+EXCLUDED.balance,updated_at=now()
		RETURNING balance`, owner.ID, proceeds).Scan(&balance); err != nil {
		return 0, 0, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO channel_stars_transactions
		(channel_id,actor_user_id,amount,reason,peer_type,peer_id,gift_id,date)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, owner.ID, actorUserID, proceeds, string(reason),
		string(counterparty.Type), counterparty.ID, giftID, date); err != nil {
		return 0, 0, err
	}
	return balance, commission, nil
}

func (s *StarGiftLifecycleStore) loadTransferReplay(ctx context.Context, req domain.StarGiftTransferRequest, sent domain.SendPrivateTextResult) (domain.StarGiftTransferResult, error) {
	var uniqueID, balance int64
	if err := s.db.QueryRow(ctx, `SELECT unique_gift_id,balance_after FROM star_gift_transfer_commands WHERE actor_user_id=$1 AND command_key=$2`,
		req.ActorUserID, strings.TrimSpace(req.CommandKey)).Scan(&uniqueID, &balance); err != nil {
		return domain.StarGiftTransferResult{}, err
	}
	unique, found, err := NewStarGiftStore(s.db).UniqueByID(ctx, uniqueID)
	if err != nil || !found {
		return domain.StarGiftTransferResult{}, domain.ErrStarGiftTransferUnavailable
	}
	saved, found, err := savedStarGiftByUniqueID(ctx, s.db, uniqueID)
	if err != nil || !found {
		return domain.StarGiftTransferResult{}, domain.ErrStarGiftTransferUnavailable
	}
	uniqueCopy := unique
	saved.Unique = &uniqueCopy
	return domain.StarGiftTransferResult{Saved: saved, Unique: unique, Balance: domain.StarsBalance{UserID: req.ActorUserID, Balance: balance}, Send: sent, Duplicate: true}, nil
}

func savedStarGiftByUniqueID(ctx context.Context, db sqlcgen.DBTX, uniqueID int64) (domain.SavedStarGift, bool, error) {
	row := db.QueryRow(ctx, `SELECT p.id,p.owner_peer_type,p.owner_peer_id,p.from_user_id,p.gift_id,p.catalog_revision_id,
	 p.msg_id,p.saved_id,p.gift_date,p.name_hidden,p.unsaved,p.converted,p.convert_stars,p.prepaid_upgrade_stars,p.prepaid_upgrade_hash,p.gift_num,
	 p.lifecycle_status,p.transfer_stars,p.can_export_at,p.can_transfer_at,p.can_resell_at,p.drop_original_details_stars,p.can_craft_at,
	 p.message,COALESCE(p.unique_gift_id,0),p.upgrade_msg_id,p.pinned_order,
	 COALESCE((SELECT array_agg(i.collection_id ORDER BY c.sort_order,i.collection_id) FROM star_gift_collection_items i
	 JOIN star_gift_collections c ON c.collection_id=i.collection_id WHERE i.saved_gift_id=p.id),ARRAY[]::integer[])
	 FROM peer_star_gifts p WHERE p.unique_gift_id=$1`, uniqueID)
	saved, err := scanSavedStarGift(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.SavedStarGift{}, false, nil
	}
	return saved, err == nil, err
}

func updateStarGiftResaleProjection(ctx context.Context, tx pgx.Tx, giftID int64) error {
	_, err := tx.Exec(ctx, `UPDATE star_gift_catalog c SET
 availability_resale=(SELECT COUNT(*) FROM star_gift_listings l JOIN unique_star_gifts u ON u.id=l.unique_gift_id WHERE u.gift_id=c.gift_id),
 resell_min_stars=COALESCE((SELECT MIN(l.amount) FROM star_gift_listings l JOIN unique_star_gifts u ON u.id=l.unique_gift_id WHERE u.gift_id=c.gift_id AND l.currency='XTR'),0),
 updated_at=now() WHERE c.gift_id=$1`, giftID)
	return err
}

func (s *StarGiftLifecycleStore) SetStarGiftNotifications(ctx context.Context, userID, channelID int64, enabled bool) error {
	if userID <= 0 || channelID <= 0 {
		return domain.ErrStarGiftOwnerInvalid
	}
	_, err := s.db.Exec(ctx, `INSERT INTO star_gift_notification_settings(user_id,channel_id,enabled) VALUES($1,$2,$3)
ON CONFLICT(user_id,channel_id) DO UPDATE SET enabled=EXCLUDED.enabled,updated_at=now()`, userID, channelID, enabled)
	return err
}

func (s *StarGiftLifecycleStore) RecordStarGiftWithdrawal(ctx context.Context, req domain.StarGiftWithdrawalRequest, provider, providerRequestID, url string, expiresAt int) (domain.StarGiftWithdrawal, error) {
	if req.UserID <= 0 || !req.Ref.Valid() || req.Date <= 0 || expiresAt <= req.Date || strings.TrimSpace(provider) == "" || strings.TrimSpace(providerRequestID) == "" || strings.TrimSpace(url) == "" {
		return domain.StarGiftWithdrawal{}, domain.ErrStarGiftWithdrawalUnavailable
	}
	err := withTx(ctx, s.db, "record star gift withdrawal", func(tx pgx.Tx) error {
		saved, err := lockSavedStarGiftForUpgrade(ctx, tx, req.Ref)
		if err != nil {
			return err
		}
		if saved.Owner != (domain.Peer{Type: domain.PeerTypeUser, ID: req.UserID}) || !saved.LifecycleStatus.Live() || saved.UniqueGiftID == 0 || saved.CanExportAt > req.Date {
			return domain.ErrStarGiftTransferUnavailable
		}
		var existingID int64
		var existingStatus string
		var existingExpires int
		err = tx.QueryRow(ctx, `SELECT id,status,expires_at FROM star_gift_withdrawal_requests WHERE unique_gift_id=$1 FOR UPDATE`, saved.UniqueGiftID).
			Scan(&existingID, &existingStatus, &existingExpires)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if err == nil {
			if existingStatus == "completed" || existingStatus == "pending" && existingExpires > req.Date {
				return nil
			}
			_, err = tx.Exec(ctx, `UPDATE star_gift_withdrawal_requests SET provider=$2,provider_request_id=$3,url=$4,
status='pending',created_at=$5,expires_at=$6,completed_at=0 WHERE id=$1`, existingID, provider, providerRequestID, url, req.Date, expiresAt)
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO star_gift_withdrawal_requests(unique_gift_id,owner_user_id,provider,provider_request_id,url,created_at,expires_at)
VALUES($1,$2,$3,$4,$5,$6,$7)`, saved.UniqueGiftID, req.UserID, provider, providerRequestID, url, req.Date, expiresAt)
		return err
	})
	if err != nil {
		return domain.StarGiftWithdrawal{}, err
	}
	// If an unexpired request already existed, return it instead of exposing a
	// newly generated but unpersisted bearer URL.
	saved, found, err := NewStarGiftStore(s.db).GetByRef(ctx, req.Ref)
	if err != nil || !found {
		return domain.StarGiftWithdrawal{}, domain.ErrStarGiftWithdrawalUnavailable
	}
	return s.resolveStarGiftWithdrawalByUniqueID(ctx, saved.UniqueGiftID)
}

func (s *StarGiftLifecycleStore) ResolveStarGiftWithdrawal(ctx context.Context, providerRequestID string) (domain.StarGiftWithdrawal, bool, error) {
	providerRequestID = strings.TrimSpace(providerRequestID)
	if providerRequestID == "" || len(providerRequestID) > 256 {
		return domain.StarGiftWithdrawal{}, false, nil
	}
	var uniqueID int64
	if err := s.db.QueryRow(ctx, `SELECT unique_gift_id FROM star_gift_withdrawal_requests WHERE provider_request_id=$1`, providerRequestID).Scan(&uniqueID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.StarGiftWithdrawal{}, false, nil
		}
		return domain.StarGiftWithdrawal{}, false, err
	}
	withdrawal, err := s.resolveStarGiftWithdrawalByUniqueID(ctx, uniqueID)
	return withdrawal, err == nil, err
}

func (s *StarGiftLifecycleStore) resolveStarGiftWithdrawalByUniqueID(ctx context.Context, uniqueID int64) (domain.StarGiftWithdrawal, error) {
	var out domain.StarGiftWithdrawal
	if err := s.db.QueryRow(ctx, `SELECT provider_request_id,url,expires_at,status FROM star_gift_withdrawal_requests WHERE unique_gift_id=$1`, uniqueID).
		Scan(&out.ProviderRequestID, &out.URL, &out.ExpiresAt, &out.Status); err != nil {
		return domain.StarGiftWithdrawal{}, err
	}
	gift, found, err := NewStarGiftStore(s.db).UniqueByID(ctx, uniqueID)
	if err != nil || !found {
		return domain.StarGiftWithdrawal{}, domain.ErrStarGiftWithdrawalUnavailable
	}
	out.Gift = gift
	return out, nil
}

func (s *StarGiftLifecycleStore) CompleteStarGiftWithdrawal(ctx context.Context, providerRequestID string, date int) (domain.StarGiftWithdrawal, error) {
	providerRequestID = strings.TrimSpace(providerRequestID)
	if providerRequestID == "" || len(providerRequestID) > 256 || date <= 0 {
		return domain.StarGiftWithdrawal{}, domain.ErrStarGiftWithdrawalUnavailable
	}
	expired := false
	err := withTx(ctx, s.db, "complete star gift withdrawal", func(tx pgx.Tx) error {
		var uniqueID, ownerUserID int64
		var status string
		var expiresAt int
		if err := tx.QueryRow(ctx, `SELECT unique_gift_id,owner_user_id,status,expires_at FROM star_gift_withdrawal_requests
WHERE provider_request_id=$1 FOR UPDATE`, providerRequestID).Scan(&uniqueID, &ownerUserID, &status, &expiresAt); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return domain.ErrStarGiftWithdrawalUnavailable
			}
			return err
		}
		if status == "completed" {
			return nil
		}
		if status != "pending" || expiresAt <= date {
			expired = true
			_, err := tx.Exec(ctx, `UPDATE star_gift_withdrawal_requests SET status='failed',completed_at=$2 WHERE provider_request_id=$1`, providerRequestID, date)
			return err
		}
		saved, found, err := lockSavedStarGiftByUniqueID(ctx, tx, uniqueID)
		if err != nil || !found || saved.Owner != (domain.Peer{Type: domain.PeerTypeUser, ID: ownerUserID}) || !saved.LifecycleStatus.Live() {
			return domain.ErrStarGiftWithdrawalUnavailable
		}
		unique, found, err := NewStarGiftStore(tx).UniqueByID(ctx, uniqueID)
		if err != nil || !found || unique.Owner != saved.Owner || unique.Burned || unique.OwnerAddress != "" {
			return domain.ErrStarGiftWithdrawalUnavailable
		}
		if err := s.refundPendingStarGiftOffers(ctx, tx, uniqueID, date, "gift exported"); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM star_gift_listings WHERE unique_gift_id=$1`, uniqueID); err != nil {
			return err
		}
		if err := removeSavedGiftFromCollections(ctx, tx, saved.Owner, saved.ID); err != nil {
			return err
		}
		ownerAddress := "telesrv-owner:" + providerRequestID
		requestHash := sha256.Sum256([]byte(providerRequestID))
		giftAddress := fmt.Sprintf("telesrv-gift:%s:%x", unique.Slug, requestHash[:8])
		if _, err := tx.Exec(ctx, `UPDATE unique_star_gifts SET owner_peer_type=NULL,owner_peer_id=NULL,
owner_address=$2,gift_address=$3,updated_at=now() WHERE id=$1`, uniqueID, ownerAddress, giftAddress); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE peer_star_gifts SET lifecycle_status='exported',unsaved=true,pinned_order=0 WHERE id=$1`, saved.ID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE star_gift_withdrawal_requests SET status='completed',completed_at=$2 WHERE provider_request_id=$1`, providerRequestID, date); err != nil {
			return err
		}
		return updateStarGiftResaleProjection(ctx, tx, unique.GiftID)
	})
	if err != nil {
		return domain.StarGiftWithdrawal{}, err
	}
	if expired {
		return domain.StarGiftWithdrawal{}, domain.ErrStarGiftWithdrawalUnavailable
	}
	withdrawal, found, err := s.ResolveStarGiftWithdrawal(ctx, providerRequestID)
	if err != nil || !found {
		return domain.StarGiftWithdrawal{}, domain.ErrStarGiftWithdrawalUnavailable
	}
	return withdrawal, nil
}

func (s *StarGiftLifecycleStore) TonBalance(ctx context.Context, userID int64) (int64, error) {
	if userID <= 0 {
		return 0, domain.ErrStarGiftOwnerInvalid
	}
	var balance int64
	err := withTx(ctx, s.db, "ensure internal ton grant", func(tx pgx.Tx) error {
		var err error
		balance, err = s.ensureTonGrantTx(ctx, tx, userID, int(time.Now().Unix()))
		return err
	})
	return balance, err
}

func (s *StarGiftLifecycleStore) ensureTonGrantTx(ctx context.Context, tx pgx.Tx, userID int64, date int) (int64, error) {
	if _, err := tx.Exec(ctx, `INSERT INTO ton_balances(user_id,balance_nanoton,granted) VALUES($1,0,false)
ON CONFLICT(user_id) DO NOTHING`, userID); err != nil {
		return 0, err
	}
	var balance int64
	var granted bool
	if err := tx.QueryRow(ctx, `SELECT balance_nanoton,granted FROM ton_balances WHERE user_id=$1 FOR UPDATE`, userID).
		Scan(&balance, &granted); err != nil {
		return 0, err
	}
	if granted {
		return balance, nil
	}
	if err := tx.QueryRow(ctx, `UPDATE ton_balances SET balance_nanoton=balance_nanoton+$2,granted=true,updated_at=now()
WHERE user_id=$1 RETURNING balance_nanoton`, userID, s.tonStartingGrant).Scan(&balance); err != nil {
		return 0, err
	}
	if s.tonStartingGrant > 0 {
		if _, err := tx.Exec(ctx, `INSERT INTO ton_transactions(user_id,amount_nanoton,reason,date)
VALUES($1,$2,$3,$4)`, userID, s.tonStartingGrant, string(domain.StarsReasonGrant), date); err != nil {
			return 0, err
		}
	}
	return balance, nil
}

func (s *StarGiftLifecycleStore) TonTransactions(ctx context.Context, userID int64, offset string, limit int) (domain.TonTransactionPage, error) {
	if userID <= 0 || limit <= 0 || limit > domain.MaxStarsTransactionsLimit || len(offset) > domain.MaxStarsTransactionsOffsetBytes {
		return domain.TonTransactionPage{}, domain.ErrStarGiftOwnerInvalid
	}
	if _, err := s.TonBalance(ctx, userID); err != nil {
		return domain.TonTransactionPage{}, err
	}
	cursor, hasCursor := domain.DecodeStarsCursor(offset)
	args := []any{userID, limit + 1}
	where := "user_id=$1"
	if hasCursor {
		where += " AND id<$3"
		args = append(args, cursor)
	}
	rows, err := s.db.Query(ctx, `SELECT id,user_id,COALESCE(peer_type,''),COALESCE(peer_id,0),COALESCE(gift_id,0),
amount_nanoton,date,reason FROM ton_transactions WHERE `+where+` ORDER BY id DESC LIMIT $2`, args...)
	if err != nil {
		return domain.TonTransactionPage{}, err
	}
	defer rows.Close()
	items := make([]domain.TonTransaction, 0, limit+1)
	for rows.Next() {
		var item domain.TonTransaction
		var peerType string
		if err := rows.Scan(&item.ID, &item.UserID, &peerType, &item.Peer.ID, &item.GiftID, &item.Amount, &item.Date, &item.Reason); err != nil {
			return domain.TonTransactionPage{}, err
		}
		item.Peer.Type = domain.PeerType(peerType)
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return domain.TonTransactionPage{}, err
	}
	page := domain.TonTransactionPage{}
	if len(items) > limit {
		items = items[:limit]
		page.NextOffset = domain.EncodeStarsCursor(items[len(items)-1].ID)
	}
	page.Transactions = items
	if err := s.db.QueryRow(ctx, `SELECT balance_nanoton FROM ton_balances WHERE user_id=$1`, userID).Scan(&page.Balance); err != nil {
		return domain.TonTransactionPage{}, err
	}
	return page, nil
}

// Channel Stars/TON ledgers are revenue projections owned by the channel. They
// never receive a starting grant and are deliberately separate from the actor
// administrator's personal balances.
func (s *StarGiftLifecycleStore) ChannelStarsBalance(ctx context.Context, channelID int64) (int64, error) {
	if channelID <= 0 {
		return 0, domain.ErrStarGiftOwnerInvalid
	}
	var balance int64
	err := s.db.QueryRow(ctx, `SELECT COALESCE((SELECT balance FROM channel_stars_balances WHERE channel_id=$1),0)`, channelID).Scan(&balance)
	return balance, err
}

func (s *StarGiftLifecycleStore) ChannelStarsTransactions(ctx context.Context, channelID int64, offset string, limit int) (domain.StarsTransactionPage, error) {
	if channelID <= 0 || limit <= 0 || limit > domain.MaxStarsTransactionsLimit || len(offset) > domain.MaxStarsTransactionsOffsetBytes {
		return domain.StarsTransactionPage{}, domain.ErrStarGiftOwnerInvalid
	}
	cursor, hasCursor := domain.DecodeStarsCursor(offset)
	args := []any{channelID, limit + 1}
	where := "channel_id=$1"
	if hasCursor {
		where += " AND id<$3"
		args = append(args, cursor)
	}
	rows, err := s.db.Query(ctx, `SELECT id,COALESCE(peer_type,''),COALESCE(peer_id,0),amount,date,reason
FROM channel_stars_transactions WHERE `+where+` ORDER BY id DESC LIMIT $2`, args...)
	if err != nil {
		return domain.StarsTransactionPage{}, err
	}
	defer rows.Close()
	items := make([]domain.StarsTransaction, 0, limit+1)
	for rows.Next() {
		var item domain.StarsTransaction
		var peerType string
		if err := rows.Scan(&item.ID, &peerType, &item.Peer.ID, &item.Amount, &item.Date, &item.Reason); err != nil {
			return domain.StarsTransactionPage{}, err
		}
		item.Peer.Type = domain.PeerType(peerType)
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return domain.StarsTransactionPage{}, err
	}
	page := domain.StarsTransactionPage{}
	if len(items) > limit {
		items = items[:limit]
		page.NextOffset = domain.EncodeStarsCursor(items[len(items)-1].ID)
	}
	page.Transactions = items
	page.Balance, err = s.ChannelStarsBalance(ctx, channelID)
	return page, err
}

func (s *StarGiftLifecycleStore) ChannelTonBalance(ctx context.Context, channelID int64) (int64, error) {
	if channelID <= 0 {
		return 0, domain.ErrStarGiftOwnerInvalid
	}
	var balance int64
	err := s.db.QueryRow(ctx, `SELECT COALESCE((SELECT balance_nanoton FROM channel_ton_balances WHERE channel_id=$1),0)`, channelID).Scan(&balance)
	return balance, err
}

func (s *StarGiftLifecycleStore) ChannelTonTransactions(ctx context.Context, channelID int64, offset string, limit int) (domain.TonTransactionPage, error) {
	if channelID <= 0 || limit <= 0 || limit > domain.MaxStarsTransactionsLimit || len(offset) > domain.MaxStarsTransactionsOffsetBytes {
		return domain.TonTransactionPage{}, domain.ErrStarGiftOwnerInvalid
	}
	cursor, hasCursor := domain.DecodeStarsCursor(offset)
	args := []any{channelID, limit + 1}
	where := "channel_id=$1"
	if hasCursor {
		where += " AND id<$3"
		args = append(args, cursor)
	}
	rows, err := s.db.Query(ctx, `SELECT id,COALESCE(peer_type,''),COALESCE(peer_id,0),COALESCE(gift_id,0),amount_nanoton,date,reason
FROM channel_ton_transactions WHERE `+where+` ORDER BY id DESC LIMIT $2`, args...)
	if err != nil {
		return domain.TonTransactionPage{}, err
	}
	defer rows.Close()
	items := make([]domain.TonTransaction, 0, limit+1)
	for rows.Next() {
		var item domain.TonTransaction
		var peerType string
		if err := rows.Scan(&item.ID, &peerType, &item.Peer.ID, &item.GiftID, &item.Amount, &item.Date, &item.Reason); err != nil {
			return domain.TonTransactionPage{}, err
		}
		item.Peer.Type = domain.PeerType(peerType)
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return domain.TonTransactionPage{}, err
	}
	page := domain.TonTransactionPage{}
	if len(items) > limit {
		items = items[:limit]
		page.NextOffset = domain.EncodeStarsCursor(items[len(items)-1].ID)
	}
	page.Transactions = items
	page.Balance, err = s.ChannelTonBalance(ctx, channelID)
	return page, err
}

func lifecycleCommandRandomID(parts ...any) int64 {
	sum := sha256.Sum256([]byte(fmt.Sprint(parts...)))
	id := int64(binary.LittleEndian.Uint64(sum[:8]) & 0x7fffffffffffffff)
	if id == 0 {
		return 1
	}
	return id
}

func validLifecyclePeer(peer domain.Peer) bool {
	return peer.ID > 0 && (peer.Type == domain.PeerTypeUser || peer.Type == domain.PeerTypeChannel)
}

func sortedUniqueInt64(values []int64) []int64 {
	out := append([]int64(nil), values...)
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

var _ store.StarGiftLifecycleStore = (*StarGiftLifecycleStore)(nil)
