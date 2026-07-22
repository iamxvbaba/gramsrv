package postgres

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"telesrv/internal/domain"
)

func (s *StarGiftLifecycleStore) PrepaidUpgradeTarget(ctx context.Context, owner domain.Peer, hash string) (domain.SavedStarGift, int64, error) {
	hash = strings.TrimSpace(hash)
	if s == nil || s.db == nil || !validLifecyclePeer(owner) || len(hash) < 32 || len(hash) > 256 {
		return domain.SavedStarGift{}, 0, domain.ErrStarGiftCollectibleUnavailable
	}
	row := s.db.QueryRow(ctx, `SELECT p.id,p.owner_peer_type,p.owner_peer_id,p.from_user_id,p.gift_id,p.catalog_revision_id,
p.msg_id,p.saved_id,p.gift_date,p.name_hidden,p.unsaved,p.converted,p.convert_stars,p.prepaid_upgrade_stars,p.prepaid_upgrade_hash,p.gift_num,
p.lifecycle_status,p.transfer_stars,p.can_export_at,p.can_transfer_at,p.can_resell_at,p.drop_original_details_stars,p.can_craft_at,
p.message,COALESCE(p.unique_gift_id,0),p.upgrade_msg_id,p.pinned_order,
COALESCE((SELECT array_agg(i.collection_id ORDER BY c.sort_order,i.collection_id) FROM star_gift_collection_items i
JOIN star_gift_collections c ON c.collection_id=i.collection_id WHERE i.saved_gift_id=p.id),ARRAY[]::integer[])
FROM peer_star_gifts p WHERE p.owner_peer_type=$1 AND p.owner_peer_id=$2 AND p.prepaid_upgrade_hash=$3`,
		string(owner.Type), owner.ID, hash)
	saved, err := scanSavedStarGift(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.SavedStarGift{}, 0, domain.ErrStarGiftCollectibleUnavailable
	}
	if err != nil || !saved.LifecycleStatus.Live() || saved.UniqueGiftID != 0 || saved.PrepaidUpgradeStars != 0 {
		if err != nil {
			return domain.SavedStarGift{}, 0, err
		}
		return domain.SavedStarGift{}, 0, domain.ErrStarGiftCollectibleUnavailable
	}
	revision, err := locklessActiveCollectibleRevision(ctx, s.db, saved.GiftID)
	if err != nil || revision.UpgradeStars <= 0 || revision.Issued >= revision.SupplyTotal {
		return domain.SavedStarGift{}, 0, domain.ErrStarGiftCollectibleUnavailable
	}
	return saved, revision.UpgradeStars, nil
}

func locklessActiveCollectibleRevision(ctx context.Context, db interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, giftID int64) (domain.StarGiftCollectibleRevision, error) {
	var revision domain.StarGiftCollectibleRevision
	var status string
	err := db.QueryRow(ctx, `SELECT r.id,r.gift_id,r.upgrade_stars,r.supply_total,r.issued,r.slug_prefix,r.status
FROM star_gift_catalog c JOIN star_gift_collectible_revisions r ON r.id=c.collectible_revision_id
WHERE c.gift_id=$1`, giftID).Scan(&revision.ID, &revision.GiftID, &revision.UpgradeStars,
		&revision.SupplyTotal, &revision.Issued, &revision.SlugPrefix, &status)
	if err != nil || status != "published" {
		return domain.StarGiftCollectibleRevision{}, domain.ErrStarGiftCollectibleUnavailable
	}
	return revision, nil
}

func (s *StarGiftLifecycleStore) PrepayStarGiftUpgrade(ctx context.Context, req domain.StarGiftPrepaidUpgradeRequest) (domain.StarGiftPrepaidUpgradeResult, error) {
	req.Hash, req.CommandKey = strings.TrimSpace(req.Hash), strings.TrimSpace(req.CommandKey)
	if s == nil || s.messages == nil || req.PayerUserID <= 0 || !validLifecyclePeer(req.Owner) ||
		len(req.Hash) < 32 || len(req.Hash) > 256 || req.FormID == 0 || req.Date <= 0 || req.CommandKey == "" || len(req.CommandKey) > 256 || req.ChargeStars < 0 {
		return domain.StarGiftPrepaidUpgradeResult{}, domain.ErrStarGiftCollectibleUnavailable
	}
	if replay, found, err := s.loadPrepaidUpgradeReplay(ctx, req, domain.SendPrivateTextResult{}); err != nil || found {
		return replay, err
	}
	if req.ChargeStars <= 0 {
		return domain.StarGiftPrepaidUpgradeResult{}, domain.ErrStarGiftCollectibleUnavailable
	}
	target, price, err := s.PrepaidUpgradeTarget(ctx, req.Owner, req.Hash)
	if err != nil || price != req.ChargeStars {
		return domain.StarGiftPrepaidUpgradeResult{}, domain.ErrStarGiftCollectibleUnavailable
	}
	fingerprint := sha256.Sum256([]byte(fmt.Sprintf("telesrv:star-gift-prepay:v2:%d:%s:%d:%s:%d:%d", req.PayerUserID,
		req.Owner.Type, req.Owner.ID, req.Hash, req.FormID, req.ChargeStars)))
	placeholder := &domain.MessageMedia{Kind: domain.MessageMediaKindService, ServiceAction: &domain.MessageServiceAction{
		Kind: domain.MessageServiceActionStarGift, StarGift: &domain.MessageStarGiftAction{Saved: true, CanUpgrade: true, UpgradeSeparate: true}}}
	messageSenderID, recipientUserID := req.PayerUserID, req.Owner.ID
	if req.Owner.Type == domain.PeerTypeChannel {
		messageSenderID, recipientUserID = domain.OfficialSystemUserID, req.PayerUserID
	}
	messageReq := domain.SendPrivateTextRequest{SenderUserID: messageSenderID, RecipientUserID: recipientUserID,
		RandomID: lifecycleCommandRandomID("prepay", req.PayerUserID, req.Owner.ID, req.Hash), Media: placeholder, Date: req.Date,
		OriginAuthKeyID: req.OriginAuthKeyID, OriginSessionID: req.OriginSessionID, OriginUserID: req.PayerUserID,
		IdempotencyFingerprint: fingerprint[:]}
	var result domain.StarGiftPrepaidUpgradeResult
	hooks := privateSendTxHooks{before: func(ctx context.Context, tx pgx.Tx, messageReq *domain.SendPrivateTextRequest) error {
		locked, err := lockSavedStarGiftByPrepayHash(ctx, tx, req.Owner, req.Hash)
		if err != nil || locked.ID != target.ID || !locked.LifecycleStatus.Live() || locked.UniqueGiftID != 0 || locked.PrepaidUpgradeStars != 0 {
			return domain.ErrStarGiftCollectibleUnavailable
		}
		revision, err := lockActiveCollectibleRevision(ctx, tx, locked.GiftID)
		if err != nil || revision.UpgradeStars != req.ChargeStars || revision.Issued >= revision.SupplyTotal {
			return domain.ErrStarGiftCollectibleUnavailable
		}
		balance, err := s.debitLifecycleAmount(ctx, tx, req.PayerUserID,
			domain.StarGiftAmount{Currency: domain.StarGiftCurrencyStars, Amount: req.ChargeStars},
			domain.StarsReasonGiftPrepaid, req.Owner, req.Date, "Prepaid star gift upgrade")
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE peer_star_gifts SET prepaid_upgrade_stars=$2,prepaid_upgrade_hash='' WHERE id=$1`, locked.ID, req.ChargeStars); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO star_gift_prepaid_upgrade_commands(payer_user_id,command_key,saved_gift_id,form_id,charge_stars,balance_after,created_at)
VALUES($1,$2,$3,$4,$5,$6,$7)`, req.PayerUserID, req.CommandKey, locked.ID, req.FormID, req.ChargeStars, balance.Balance, req.Date); err != nil {
			return err
		}
		gift, found, err := NewStarGiftStore(tx).CatalogRevision(ctx, locked.RevisionID)
		if err != nil || !found {
			return domain.ErrStarGiftCollectibleUnavailable
		}
		sticker := gift.Sticker
		action := &domain.MessageStarGiftAction{
			GiftID: gift.ID, Stars: gift.Stars, ConvertStars: locked.ConvertStars, Title: gift.Title, Sticker: &sticker,
			FromUserID: req.PayerUserID, To: req.Owner, SavedID: locked.SavedID, Saved: true, CanUpgrade: true,
			PrepaidUpgrade: true, UpgradeSeparate: true, UpgradePriceStars: req.ChargeStars,
			UpgradeStars: req.ChargeStars, GiftMsgID: locked.MsgID,
		}
		if req.Owner.Type == domain.PeerTypeChannel {
			action.PeerChannelID = req.Owner.ID
		} else {
			action.PeerUserID = req.Owner.ID
		}
		messageReq.Media = &domain.MessageMedia{Kind: domain.MessageMediaKindService, ServiceAction: &domain.MessageServiceAction{
			Kind: domain.MessageServiceActionStarGift, StarGift: &domain.MessageStarGiftAction{
				GiftID: action.GiftID, Stars: action.Stars, ConvertStars: action.ConvertStars, Title: action.Title,
				Sticker: action.Sticker, FromUserID: action.FromUserID, PeerUserID: action.PeerUserID,
				PeerChannelID: action.PeerChannelID, To: action.To, SavedID: action.SavedID, Saved: action.Saved,
				CanUpgrade: action.CanUpgrade, PrepaidUpgrade: action.PrepaidUpgrade, UpgradeSeparate: action.UpgradeSeparate,
				UpgradePriceStars: action.UpgradePriceStars, UpgradeStars: action.UpgradeStars, GiftMsgID: action.GiftMsgID}}}
		locked.PrepaidUpgradeStars, locked.PrepaidUpgradeHash = req.ChargeStars, ""
		result.Saved, result.Balance = locked, balance
		return nil
	}, projectMedia: func(ctx context.Context, tx pgx.Tx, messageReq *domain.SendPrivateTextRequest) (privateSendMediaProjection, error) {
		if result.Saved.Owner.Type != domain.PeerTypeUser {
			return privateSendMediaProjection{Shared: messageReq.Media, Sender: messageReq.Media, Recipient: messageReq.Media}, nil
		}
		return projectPrivateStarGiftSourceRef(ctx, tx, messageReq, result.Saved.Owner.ID, result.Saved.MsgID)
	}, after: func(ctx context.Context, tx pgx.Tx, sent domain.SendPrivateTextResult) error {
		if req.Owner.Type == domain.PeerTypeUser {
			ownerMessageID := sent.RecipientMessage.ID
			if sent.SenderMessage.OwnerUserID == req.Owner.ID {
				ownerMessageID = sent.SenderMessage.ID
			}
			return registerUserStarGiftMessageRef(ctx, tx, req.Owner.ID, ownerMessageID, result.Saved.ID, 0)
		}
		action := messageReq.Media.ServiceAction.StarGift
		return NewChannelStore(tx).appendStarGiftAdminLogTx(ctx, tx, req.Owner.ID, req.PayerUserID,
			result.Saved.SavedID, req.Date, domain.ChannelMessageAction{Type: domain.ChannelActionStarGift, StarGift: action})
	}}
	sent, err := s.messages.sendPrivateTextWithHooks(ctx, messageReq, hooks)
	if err != nil {
		if isUniqueViolation(err) {
			if replay, found, replayErr := s.loadPrepaidUpgradeReplay(ctx, req, sent); replayErr != nil || found {
				return replay, replayErr
			}
		}
		return domain.StarGiftPrepaidUpgradeResult{}, err
	}
	result.Send, result.Duplicate = sent, sent.Duplicate
	if sent.Duplicate {
		replay, _, replayErr := s.loadPrepaidUpgradeReplay(ctx, req, sent)
		return replay, replayErr
	}
	return result, nil
}

func lockSavedStarGiftByPrepayHash(ctx context.Context, tx pgx.Tx, owner domain.Peer, hash string) (domain.SavedStarGift, error) {
	row := tx.QueryRow(ctx, `SELECT p.id,p.owner_peer_type,p.owner_peer_id,p.from_user_id,p.gift_id,p.catalog_revision_id,
p.msg_id,p.saved_id,p.gift_date,p.name_hidden,p.unsaved,p.converted,p.convert_stars,p.prepaid_upgrade_stars,p.prepaid_upgrade_hash,p.gift_num,
p.lifecycle_status,p.transfer_stars,p.can_export_at,p.can_transfer_at,p.can_resell_at,p.drop_original_details_stars,p.can_craft_at,
p.message,COALESCE(p.unique_gift_id,0),p.upgrade_msg_id,p.pinned_order,
COALESCE((SELECT array_agg(i.collection_id ORDER BY c.sort_order,i.collection_id) FROM star_gift_collection_items i
JOIN star_gift_collections c ON c.collection_id=i.collection_id WHERE i.saved_gift_id=p.id),ARRAY[]::integer[])
FROM peer_star_gifts p WHERE p.owner_peer_type=$1 AND p.owner_peer_id=$2 AND p.prepaid_upgrade_hash=$3 FOR UPDATE`,
		string(owner.Type), owner.ID, hash)
	saved, err := scanSavedStarGift(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.SavedStarGift{}, domain.ErrStarGiftCollectibleUnavailable
	}
	return saved, err
}

func (s *StarGiftLifecycleStore) loadPrepaidUpgradeReplay(ctx context.Context, req domain.StarGiftPrepaidUpgradeRequest, sent domain.SendPrivateTextResult) (domain.StarGiftPrepaidUpgradeResult, bool, error) {
	var savedID, balance int64
	err := s.db.QueryRow(ctx, `SELECT saved_gift_id,balance_after FROM star_gift_prepaid_upgrade_commands WHERE payer_user_id=$1 AND command_key=$2`,
		req.PayerUserID, req.CommandKey).Scan(&savedID, &balance)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.StarGiftPrepaidUpgradeResult{}, false, nil
	}
	if err != nil {
		return domain.StarGiftPrepaidUpgradeResult{}, false, err
	}
	saved, found, err := savedStarGiftByID(ctx, s.db, savedID)
	if err != nil || !found {
		return domain.StarGiftPrepaidUpgradeResult{}, false, domain.ErrStarGiftCollectibleUnavailable
	}
	return domain.StarGiftPrepaidUpgradeResult{Saved: saved, Balance: domain.StarsBalance{UserID: req.PayerUserID, Balance: balance}, Send: sent, Duplicate: true}, true, nil
}

func (s *StarGiftLifecycleStore) DropStarGiftOriginalDetails(ctx context.Context, req domain.StarGiftDropOriginalDetailsRequest) (domain.StarGiftDropOriginalDetailsResult, error) {
	req.CommandKey = strings.TrimSpace(req.CommandKey)
	if s == nil || s.db == nil || req.UserID <= 0 || !req.Ref.Valid() ||
		(req.Ref.Owner.Type == domain.PeerTypeUser && req.Ref.Owner.ID != req.UserID) || !validLifecyclePeer(req.Ref.Owner) ||
		req.FormID == 0 || req.Date <= 0 || req.CommandKey == "" || len(req.CommandKey) > 256 || req.ChargeStars < 0 {
		return domain.StarGiftDropOriginalDetailsResult{}, domain.ErrStarGiftCollectibleUnavailable
	}
	if replay, found, err := s.loadDropDetailsReplay(ctx, req); err != nil || found {
		return replay, err
	}
	if req.ChargeStars <= 0 {
		return domain.StarGiftDropOriginalDetailsResult{}, domain.ErrStarGiftCollectibleUnavailable
	}
	var result domain.StarGiftDropOriginalDetailsResult
	err := withTx(ctx, s.db, "drop star gift original details", func(tx pgx.Tx) error {
		saved, unique, err := lockOwnedUniqueStarGift(ctx, tx, req.UserID, req.Ref)
		if err != nil || saved.DropOriginalDetailsStars != req.ChargeStars || !unique.KeepOriginalDetails {
			return domain.ErrStarGiftCollectibleUnavailable
		}
		balance, err := s.debitLifecycleAmount(ctx, tx, req.UserID,
			domain.StarGiftAmount{Currency: domain.StarGiftCurrencyStars, Amount: req.ChargeStars},
			domain.StarsReasonGiftDrop, saved.Owner, req.Date, "Drop star gift original details")
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE unique_star_gifts SET keep_original_details=false,updated_at=now() WHERE id=$1`, unique.ID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE peer_star_gifts SET drop_original_details_stars=0 WHERE id=$1`, saved.ID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO star_gift_drop_details_commands(user_id,command_key,saved_gift_id,unique_gift_id,form_id,charge_stars,balance_after,created_at)
VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, req.UserID, req.CommandKey, saved.ID, unique.ID, req.FormID, req.ChargeStars, balance.Balance, req.Date); err != nil {
			return err
		}
		saved.DropOriginalDetailsStars, unique.KeepOriginalDetails = 0, false
		result = domain.StarGiftDropOriginalDetailsResult{Saved: saved, Unique: unique, Balance: balance}
		return nil
	})
	if err != nil {
		if isUniqueViolation(err) {
			if replay, found, replayErr := s.loadDropDetailsReplay(ctx, req); replayErr != nil || found {
				return replay, replayErr
			}
		}
		return domain.StarGiftDropOriginalDetailsResult{}, err
	}
	return result, nil
}

func (s *StarGiftLifecycleStore) loadDropDetailsReplay(ctx context.Context, req domain.StarGiftDropOriginalDetailsRequest) (domain.StarGiftDropOriginalDetailsResult, bool, error) {
	var savedID, uniqueID, balance int64
	err := s.db.QueryRow(ctx, `SELECT saved_gift_id,unique_gift_id,balance_after FROM star_gift_drop_details_commands WHERE user_id=$1 AND command_key=$2`,
		req.UserID, req.CommandKey).Scan(&savedID, &uniqueID, &balance)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.StarGiftDropOriginalDetailsResult{}, false, nil
	}
	if err != nil {
		return domain.StarGiftDropOriginalDetailsResult{}, false, err
	}
	saved, found, err := savedStarGiftByID(ctx, s.db, savedID)
	if err != nil || !found {
		return domain.StarGiftDropOriginalDetailsResult{}, false, domain.ErrStarGiftCollectibleUnavailable
	}
	unique, found, err := NewStarGiftStore(s.db).UniqueByID(ctx, uniqueID)
	if err != nil || !found {
		return domain.StarGiftDropOriginalDetailsResult{}, false, domain.ErrStarGiftCollectibleUnavailable
	}
	return domain.StarGiftDropOriginalDetailsResult{Saved: saved, Unique: unique,
		Balance: domain.StarsBalance{UserID: req.UserID, Balance: balance}, Duplicate: true}, true, nil
}

func savedStarGiftByID(ctx context.Context, db interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, savedID int64) (domain.SavedStarGift, bool, error) {
	row := db.QueryRow(ctx, `SELECT p.id,p.owner_peer_type,p.owner_peer_id,p.from_user_id,p.gift_id,p.catalog_revision_id,
p.msg_id,p.saved_id,p.gift_date,p.name_hidden,p.unsaved,p.converted,p.convert_stars,p.prepaid_upgrade_stars,p.prepaid_upgrade_hash,p.gift_num,
p.lifecycle_status,p.transfer_stars,p.can_export_at,p.can_transfer_at,p.can_resell_at,p.drop_original_details_stars,p.can_craft_at,
p.message,COALESCE(p.unique_gift_id,0),p.upgrade_msg_id,p.pinned_order,
COALESCE((SELECT array_agg(i.collection_id ORDER BY c.sort_order,i.collection_id) FROM star_gift_collection_items i
JOIN star_gift_collections c ON c.collection_id=i.collection_id WHERE i.saved_gift_id=p.id),ARRAY[]::integer[])
FROM peer_star_gifts p WHERE p.id=$1`, savedID)
	saved, err := scanSavedStarGift(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.SavedStarGift{}, false, nil
	}
	return saved, err == nil, err
}
