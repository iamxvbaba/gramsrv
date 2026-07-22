package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"telesrv/internal/domain"
)

const suggestedPostSettlementAge = 24 * 60 * 60

type persistedSuggestedPostApproval struct {
	monoforumID, parentID, actorID, payerID                                                       int64
	messageID, scheduleDate, approvalServiceID, publishedMessageID, settlementDue, finalServiceID int
	state                                                                                         domain.SuggestedPostLifecycleState
	price                                                                                         *domain.SuggestedPostPrice
}

func (s *ChannelStore) ToggleSuggestedPostApproval(ctx context.Context, req domain.ToggleSuggestedPostApprovalRequest) (domain.ToggleSuggestedPostApprovalResult, error) {
	if req.UserID == 0 || req.MonoforumID == 0 || req.MessageID <= 0 || (!req.Reject && strings.TrimSpace(req.RejectComment) != "") {
		return domain.ToggleSuggestedPostApprovalResult{}, domain.ErrSuggestedPostInvalid
	}
	if req.Date == 0 {
		req.Date = nowUnix()
	}
	beginner, ok := s.db.(txBeginner)
	if !ok {
		return domain.ToggleSuggestedPostApprovalResult{}, fmt.Errorf("toggle suggested post: db does not support transactions")
	}
	tx, err := beginner.Begin(ctx)
	if err != nil {
		return domain.ToggleSuggestedPostApprovalResult{}, fmt.Errorf("begin toggle suggested post: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()

	mono, err := getChannelByID(ctx, tx, req.MonoforumID)
	if err != nil {
		if errors.Is(err, domain.ErrChannelInvalid) {
			return domain.ToggleSuggestedPostApprovalResult{}, domain.ErrSuggestedPostInvalid
		}
		return domain.ToggleSuggestedPostApprovalResult{}, err
	}
	if mono.Deleted || !mono.Monoforum || mono.LinkedMonoforumID == 0 {
		return domain.ToggleSuggestedPostApprovalResult{}, domain.ErrSuggestedPostInvalid
	}
	parent, err := getChannelByID(ctx, tx, mono.LinkedMonoforumID)
	if err != nil {
		if errors.Is(err, domain.ErrChannelInvalid) {
			return domain.ToggleSuggestedPostApprovalResult{}, domain.ErrSuggestedPostInvalid
		}
		return domain.ToggleSuggestedPostApprovalResult{}, err
	}
	if parent.Deleted || !parent.Broadcast || parent.LinkedMonoforumID != mono.ID {
		return domain.ToggleSuggestedPostApprovalResult{}, domain.ErrSuggestedPostInvalid
	}
	if _, err := tx.Exec(ctx, `SELECT 1 FROM channel_messages WHERE channel_id=$1 AND id=$2 FOR UPDATE`, mono.ID, req.MessageID); err != nil {
		return domain.ToggleSuggestedPostApprovalResult{}, err
	}
	original, err := s.getChannelMessage(ctx, tx, mono.ID, req.MessageID)
	if err != nil {
		if errors.Is(err, domain.ErrMessageIDInvalid) {
			return domain.ToggleSuggestedPostApprovalResult{}, domain.ErrSuggestedPostInvalid
		}
		return domain.ToggleSuggestedPostApprovalResult{}, err
	}
	if original.Deleted || original.SavedPeer.Type != domain.PeerTypeUser || original.SavedPeer.ID == 0 || original.SuggestedPost == nil {
		return domain.ToggleSuggestedPostApprovalResult{}, domain.ErrSuggestedPostInvalid
	}
	fromSubscriber := original.From.Type == domain.PeerTypeUser
	manager := domain.ChannelMember{}
	if fromSubscriber {
		manager, err = s.getChannelMember(ctx, tx, parent.ID, req.UserID)
		if err != nil {
			if errors.Is(err, domain.ErrChannelPrivate) {
				return domain.ToggleSuggestedPostApprovalResult{}, domain.ErrSuggestedPostApprovalForbidden
			}
			return domain.ToggleSuggestedPostApprovalResult{}, err
		}
		if !manager.CanManageDirectMessages() || (!req.Reject && !manager.CanPostChannelMessages()) {
			return domain.ToggleSuggestedPostApprovalResult{}, domain.ErrSuggestedPostApprovalForbidden
		}
	} else if req.UserID != original.SavedPeer.ID {
		return domain.ToggleSuggestedPostApprovalResult{}, domain.ErrSuggestedPostApprovalForbidden
	}

	existing, found, err := loadSuggestedPostApprovalTx(ctx, tx, mono.ID, original.ID, true)
	if err != nil {
		return domain.ToggleSuggestedPostApprovalResult{}, err
	}
	if found && existing.state != domain.SuggestedPostStateBalanceLow {
		result, err := s.loadSuggestedPostResultTx(ctx, tx, existing, true)
		if err != nil {
			return domain.ToggleSuggestedPostApprovalResult{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return domain.ToggleSuggestedPostApprovalResult{}, err
		}
		committed = true
		return result, nil
	}
	if original.SuggestedPost.Accepted || original.SuggestedPost.Rejected {
		return domain.ToggleSuggestedPostApprovalResult{}, domain.ErrSuggestedPostAlreadyHandled
	}
	price := cloneSuggestedPricePG(original.SuggestedPost.Price)
	if price != nil && price.Kind == domain.SuggestedPostPriceStars && price.Nanos != 0 {
		return domain.ToggleSuggestedPostApprovalResult{}, domain.ErrSuggestedPostInvalid
	}
	scheduleDate := original.SuggestedPost.ScheduleDate
	if req.ScheduleDate > 0 {
		scheduleDate = req.ScheduleDate
	}
	if !req.Reject && scheduleDate > 0 && (scheduleDate < req.Date+5*60 || scheduleDate > req.Date+31*24*60*60) {
		return domain.ToggleSuggestedPostApprovalResult{}, domain.ErrSuggestedPostInvalid
	}
	recipients, err := monoforumManagerRecipientsTx(ctx, tx, parent.ID, original.SavedPeer.ID)
	if err != nil {
		return domain.ToggleSuggestedPostApprovalResult{}, err
	}
	result := domain.ToggleSuggestedPostApprovalResult{Monoforum: mono, Parent: parent, SavedPeer: original.SavedPeer, Recipients: recipients}

	if req.Reject {
		original.SuggestedPost.Accepted, original.SuggestedPost.Rejected = false, true
		original, result.OriginalEvent, err = s.persistSuggestedPostEditTx(ctx, tx, original, req.UserID, req.Date)
		if err != nil {
			return domain.ToggleSuggestedPostApprovalResult{}, err
		}
		result.OriginalMessage = original
		result.ServiceMessage, result.ServiceEvent, err = s.insertSuggestedPostServiceTx(ctx, tx, mono, parent, req.UserID, fromSubscriber && manager.CanManageDirectMessages(), original.SavedPeer, original.ID, req.Date, domain.ChannelMessageAction{
			Type: domain.ChannelActionSuggestedPostApproval, SuggestedPostRejected: true,
			SuggestedPostRejectComment: strings.TrimSpace(req.RejectComment), SuggestedPostPrice: price,
		})
		if err != nil {
			return domain.ToggleSuggestedPostApprovalResult{}, err
		}
		result.State = domain.SuggestedPostStateRejected
		if err := upsertSuggestedPostApprovalTx(ctx, tx, persistedSuggestedPostApproval{monoforumID: mono.ID, messageID: original.ID, parentID: parent.ID, actorID: req.UserID, payerID: original.SavedPeer.ID, state: result.State, price: price, scheduleDate: scheduleDate, approvalServiceID: result.ServiceMessage.ID}, req.Date); err != nil {
			return domain.ToggleSuggestedPostApprovalResult{}, err
		}
	} else {
		stars, ton, enough, err := reserveSuggestedPostPaymentTx(ctx, tx, original.SavedPeer.ID, parent.ID, price, req.Date)
		if err != nil {
			return domain.ToggleSuggestedPostApprovalResult{}, err
		}
		result.PayerStarsBalance, result.PayerTONBalance = stars, ton
		if !enough {
			if found {
				result, err = s.loadSuggestedPostResultTx(ctx, tx, existing, true)
				if err != nil {
					return domain.ToggleSuggestedPostApprovalResult{}, err
				}
				result.PayerStarsBalance, result.PayerTONBalance = stars, ton
			} else {
				result.ServiceMessage, result.ServiceEvent, err = s.insertSuggestedPostServiceTx(ctx, tx, mono, parent, req.UserID, fromSubscriber && manager.CanManageDirectMessages(), original.SavedPeer, original.ID, req.Date, domain.ChannelMessageAction{
					Type: domain.ChannelActionSuggestedPostApproval, SuggestedPostBalanceTooLow: true,
					SuggestedPostScheduleDate: scheduleDate, SuggestedPostPrice: price,
				})
				if err != nil {
					return domain.ToggleSuggestedPostApprovalResult{}, err
				}
				result.State = domain.SuggestedPostStateBalanceLow
				if err := upsertSuggestedPostApprovalTx(ctx, tx, persistedSuggestedPostApproval{monoforumID: mono.ID, messageID: original.ID, parentID: parent.ID, actorID: req.UserID, payerID: original.SavedPeer.ID, state: result.State, price: price, scheduleDate: scheduleDate, approvalServiceID: result.ServiceMessage.ID}, req.Date); err != nil {
					return domain.ToggleSuggestedPostApprovalResult{}, err
				}
			}
		} else {
			effectivePublishDate := scheduleDate
			if effectivePublishDate == 0 {
				// TDesktop's "Publish Now" request has no schedule_date flag, but
				// its approval service renderer always expects an absolute date.
				effectivePublishDate = req.Date
			}
			original.SuggestedPost.Accepted, original.SuggestedPost.Rejected, original.SuggestedPost.ScheduleDate = true, false, effectivePublishDate
			original, result.OriginalEvent, err = s.persistSuggestedPostEditTx(ctx, tx, original, req.UserID, req.Date)
			if err != nil {
				return domain.ToggleSuggestedPostApprovalResult{}, err
			}
			result.OriginalMessage = original
			result.ServiceMessage, result.ServiceEvent, err = s.insertSuggestedPostServiceTx(ctx, tx, mono, parent, req.UserID, fromSubscriber && manager.CanManageDirectMessages(), original.SavedPeer, original.ID, req.Date, domain.ChannelMessageAction{Type: domain.ChannelActionSuggestedPostApproval, SuggestedPostScheduleDate: effectivePublishDate, SuggestedPostPrice: price})
			if err != nil {
				return domain.ToggleSuggestedPostApprovalResult{}, err
			}
			record := persistedSuggestedPostApproval{monoforumID: mono.ID, messageID: original.ID, parentID: parent.ID, actorID: req.UserID, payerID: original.SavedPeer.ID, state: domain.SuggestedPostStateScheduled, price: price, scheduleDate: effectivePublishDate, approvalServiceID: result.ServiceMessage.ID}
			if effectivePublishDate <= req.Date {
				published, publishErr := s.publishSuggestedPostTx(ctx, tx, parent, original, req.UserID, req.Date)
				if publishErr != nil {
					return domain.ToggleSuggestedPostApprovalResult{}, publishErr
				}
				result.Published = &published
				record.publishedMessageID = published.Message.ID
				if price == nil {
					record.state = domain.SuggestedPostStateCompleted
				} else {
					record.state = domain.SuggestedPostStatePublished
					record.settlementDue = req.Date + suggestedPostSettlementAge
				}
			}
			result.State = record.state
			if err := upsertSuggestedPostApprovalTx(ctx, tx, record, req.Date); err != nil {
				return domain.ToggleSuggestedPostApprovalResult{}, err
			}
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.ToggleSuggestedPostApprovalResult{}, fmt.Errorf("commit toggle suggested post: %w", err)
	}
	committed = true
	result.Monoforum, err = getChannelByID(ctx, s.db, mono.ID)
	if err != nil {
		return domain.ToggleSuggestedPostApprovalResult{}, fmt.Errorf("reload suggested post monoforum after commit: %w", err)
	}
	result.Parent, err = getChannelByID(ctx, s.db, parent.ID)
	if err != nil {
		return domain.ToggleSuggestedPostApprovalResult{}, fmt.Errorf("reload suggested post parent after commit: %w", err)
	}
	return result, nil
}

func (s *ChannelStore) persistSuggestedPostEditTx(ctx context.Context, tx pgx.Tx, msg domain.ChannelMessage, actor int64, date int) (domain.ChannelMessage, domain.ChannelUpdateEvent, error) {
	pts, err := s.reserveChannelPts(ctx, tx, msg.ChannelID)
	if err != nil {
		return domain.ChannelMessage{}, domain.ChannelUpdateEvent{}, err
	}
	encoded, err := marshalJSON(msg.SuggestedPost, "{}")
	if err != nil {
		return domain.ChannelMessage{}, domain.ChannelUpdateEvent{}, err
	}
	msg.Pts = pts
	if _, err := tx.Exec(ctx, `UPDATE channel_messages SET suggested_post=$3, pts=$4, updated_at=now() WHERE channel_id=$1 AND id=$2 AND NOT deleted`, msg.ChannelID, msg.ID, encoded, pts); err != nil {
		return domain.ChannelMessage{}, domain.ChannelUpdateEvent{}, fmt.Errorf("update suggested post message: %w", err)
	}
	event := domain.ChannelUpdateEvent{ChannelID: msg.ChannelID, Type: domain.ChannelUpdateEditMessage, Pts: pts, PtsCount: 1, Date: date, Message: msg, SenderUserID: actor}
	if err := insertChannelEventTx(ctx, tx, event); err != nil {
		return domain.ChannelMessage{}, domain.ChannelUpdateEvent{}, err
	}
	return msg, event, nil
}

func (s *ChannelStore) insertSuggestedPostServiceTx(ctx context.Context, tx pgx.Tx, mono, parent domain.Channel, actor int64, fromChannel bool, saved domain.Peer, replyID, date int, action domain.ChannelMessageAction) (domain.ChannelMessage, domain.ChannelUpdateEvent, error) {
	msgID, err := s.msgIDs.NextChannelMessageID(ctx, mono.ID)
	if err != nil {
		return domain.ChannelMessage{}, domain.ChannelUpdateEvent{}, err
	}
	pts, err := s.reserveChannelPts(ctx, tx, mono.ID)
	if err != nil {
		return domain.ChannelMessage{}, domain.ChannelUpdateEvent{}, err
	}
	from := domain.Peer{Type: domain.PeerTypeUser, ID: actor}
	if fromChannel {
		from = domain.Peer{Type: domain.PeerTypeChannel, ID: parent.ID}
	}
	msg := domain.ChannelMessage{ChannelID: mono.ID, ID: msgID, SenderUserID: actor, From: from, SavedPeer: saved, Date: date, ReplyTo: &domain.MessageReply{Peer: domain.Peer{Type: domain.PeerTypeChannel, ID: mono.ID}, MessageID: replyID}, Action: &action, Pts: pts}
	event := domain.ChannelUpdateEvent{ChannelID: mono.ID, Type: domain.ChannelUpdateNewMessage, Pts: pts, PtsCount: 1, Date: date, Message: msg, SenderUserID: actor}
	if err := insertChannelMessageTx(ctx, tx, msg); err != nil {
		return domain.ChannelMessage{}, domain.ChannelUpdateEvent{}, err
	}
	if err := insertChannelEventTx(ctx, tx, event); err != nil {
		return domain.ChannelMessage{}, domain.ChannelUpdateEvent{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE channels SET top_message_id=$2, pts=$3, updated_at=now() WHERE id=$1`, mono.ID, msgID, pts); err != nil {
		return domain.ChannelMessage{}, domain.ChannelUpdateEvent{}, err
	}
	return msg, event, nil
}

func (s *ChannelStore) publishSuggestedPostTx(ctx context.Context, tx pgx.Tx, parent domain.Channel, original domain.ChannelMessage, actor int64, date int) (domain.SendChannelMessageResult, error) {
	msgID, err := s.msgIDs.NextChannelMessageID(ctx, parent.ID)
	if err != nil {
		return domain.SendChannelMessageResult{}, err
	}
	pts, err := s.reserveChannelPts(ctx, tx, parent.ID)
	if err != nil {
		return domain.SendChannelMessageResult{}, err
	}
	msg := original
	msg.ChannelID, msg.ID, msg.RandomID, msg.SenderUserID = parent.ID, msgID, 0, actor
	msg.From, msg.SendAs, msg.SavedPeer, msg.Date, msg.EditDate, msg.Post = domain.Peer{Type: domain.PeerTypeChannel, ID: parent.ID}, nil, domain.Peer{}, date, 0, true
	msg.ReplyTo, msg.PaidMessageStars, msg.Pts, msg.Deleted = nil, 0, pts, false
	event := domain.ChannelUpdateEvent{ChannelID: parent.ID, Type: domain.ChannelUpdateNewMessage, Pts: pts, PtsCount: 1, Date: date, Message: msg, SenderUserID: actor}
	if err := insertChannelMessageTx(ctx, tx, msg); err != nil {
		return domain.SendChannelMessageResult{}, err
	}
	if err := insertChannelEventTx(ctx, tx, event); err != nil {
		return domain.SendChannelMessageResult{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE channels SET top_message_id=$2, pts=$3, updated_at=now() WHERE id=$1`, parent.ID, msgID, pts); err != nil {
		return domain.SendChannelMessageResult{}, err
	}
	recipients, err := s.listActiveChannelMemberIDs(ctx, tx, parent.ID, 0)
	if err != nil {
		return domain.SendChannelMessageResult{}, err
	}
	parent.TopMessageID, parent.Pts = msgID, pts
	return domain.SendChannelMessageResult{Channel: parent, Message: msg, Event: event, Recipients: recipients}, nil
}

func reserveSuggestedPostPaymentTx(ctx context.Context, tx pgx.Tx, payerID, parentID int64, price *domain.SuggestedPostPrice, date int) (*domain.StarsBalance, *int64, bool, error) {
	if price == nil {
		return nil, nil, true, nil
	}
	switch price.Kind {
	case domain.SuggestedPostPriceStars:
		if price.Nanos != 0 || price.Amount <= 0 {
			return nil, nil, false, domain.ErrSuggestedPostInvalid
		}
		balance := domain.StarsBalance{UserID: payerID}
		err := tx.QueryRow(ctx, `SELECT balance,granted FROM stars_balances WHERE user_id=$1 FOR UPDATE`, payerID).Scan(&balance.Balance, &balance.Granted)
		if errors.Is(err, pgx.ErrNoRows) {
			return &balance, nil, false, nil
		}
		if err != nil {
			return nil, nil, false, err
		}
		if balance.Balance < price.Amount {
			return &balance, nil, false, nil
		}
		if err := tx.QueryRow(ctx, `UPDATE stars_balances SET balance=balance-$2,updated_at=now() WHERE user_id=$1 RETURNING balance`, payerID, price.Amount).Scan(&balance.Balance); err != nil {
			return nil, nil, false, err
		}
		if err := insertStarsTxn(ctx, tx, payerID, -price.Amount, domain.StarsReasonSuggestedPost, domain.Peer{Type: domain.PeerTypeChannel, ID: parentID}, date, "Suggested post escrow", ""); err != nil {
			return nil, nil, false, err
		}
		return &balance, nil, true, nil
	case domain.SuggestedPostPriceTON:
		var balance int64
		err := tx.QueryRow(ctx, `SELECT balance_nanoton FROM ton_balances WHERE user_id=$1 FOR UPDATE`, payerID).Scan(&balance)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, &balance, false, nil
		}
		if err != nil {
			return nil, nil, false, err
		}
		if balance < price.Amount {
			return nil, &balance, false, nil
		}
		if err := tx.QueryRow(ctx, `UPDATE ton_balances SET balance_nanoton=balance_nanoton-$2,updated_at=now() WHERE user_id=$1 RETURNING balance_nanoton`, payerID, price.Amount).Scan(&balance); err != nil {
			return nil, nil, false, err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO ton_transactions(user_id,amount_nanoton,reason,peer_type,peer_id,date) VALUES($1,$2,$3,'channel',$4,$5)`, payerID, -price.Amount, string(domain.StarsReasonSuggestedPost), parentID, date); err != nil {
			return nil, nil, false, err
		}
		return nil, &balance, true, nil
	default:
		return nil, nil, false, domain.ErrSuggestedPostInvalid
	}
}

func monoforumManagerRecipientsTx(ctx context.Context, tx pgx.Tx, parentID, subscriberID int64) ([]int64, error) {
	rows, err := tx.Query(ctx, `SELECT user_id FROM channel_members WHERE channel_id=$1 AND status='active' AND (role='creator' OR (role='admin' AND COALESCE((admin_rights->>'ManageDirectMessages')::boolean,false))) ORDER BY user_id`, parentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []int64{subscriberID}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return uniqueChannelUserIDs(ids, 0), rows.Err()
}

func upsertSuggestedPostApprovalTx(ctx context.Context, tx pgx.Tx, row persistedSuggestedPostApproval, date int) error {
	kind, amount, nanos := "", int64(0), 0
	if row.price != nil {
		kind, amount, nanos = string(row.price.Kind), row.price.Amount, row.price.Nanos
	}
	_, err := tx.Exec(ctx, `INSERT INTO suggested_post_approvals(monoforum_id,suggestion_message_id,parent_channel_id,actor_user_id,payer_user_id,state,price_kind,price_amount,price_nanos,schedule_date,approval_service_message_id,published_message_id,settlement_due,final_service_message_id,created_at,updated_at)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$15)
ON CONFLICT(monoforum_id,suggestion_message_id) DO UPDATE SET actor_user_id=EXCLUDED.actor_user_id,state=EXCLUDED.state,price_kind=EXCLUDED.price_kind,price_amount=EXCLUDED.price_amount,price_nanos=EXCLUDED.price_nanos,schedule_date=EXCLUDED.schedule_date,approval_service_message_id=EXCLUDED.approval_service_message_id,published_message_id=EXCLUDED.published_message_id,settlement_due=EXCLUDED.settlement_due,final_service_message_id=EXCLUDED.final_service_message_id,updated_at=EXCLUDED.updated_at`,
		row.monoforumID, row.messageID, row.parentID, row.actorID, row.payerID, string(row.state), kind, amount, nanos, row.scheduleDate, row.approvalServiceID, row.publishedMessageID, row.settlementDue, row.finalServiceID, date)
	return err
}

func loadSuggestedPostApprovalTx(ctx context.Context, tx pgx.Tx, monoID int64, messageID int, lock bool) (persistedSuggestedPostApproval, bool, error) {
	q := `SELECT parent_channel_id,actor_user_id,payer_user_id,state,price_kind,price_amount,price_nanos,schedule_date,approval_service_message_id,published_message_id,settlement_due,final_service_message_id FROM suggested_post_approvals WHERE monoforum_id=$1 AND suggestion_message_id=$2`
	if lock {
		q += ` FOR UPDATE`
	}
	var row persistedSuggestedPostApproval
	row.monoforumID, row.messageID = monoID, messageID
	var state, kind string
	var amount int64
	var nanos int
	err := tx.QueryRow(ctx, q, monoID, messageID).Scan(&row.parentID, &row.actorID, &row.payerID, &state, &kind, &amount, &nanos, &row.scheduleDate, &row.approvalServiceID, &row.publishedMessageID, &row.settlementDue, &row.finalServiceID)
	if errors.Is(err, pgx.ErrNoRows) {
		return row, false, nil
	}
	if err != nil {
		return row, false, err
	}
	row.state = domain.SuggestedPostLifecycleState(state)
	if kind != "" {
		row.price = &domain.SuggestedPostPrice{Kind: domain.SuggestedPostPriceKind(kind), Amount: amount, Nanos: nanos}
	}
	return row, true, nil
}

func (s *ChannelStore) loadSuggestedPostResultTx(ctx context.Context, tx pgx.Tx, row persistedSuggestedPostApproval, duplicate bool) (domain.ToggleSuggestedPostApprovalResult, error) {
	mono, err := getChannelByID(ctx, tx, row.monoforumID)
	if err != nil {
		return domain.ToggleSuggestedPostApprovalResult{}, err
	}
	parent, err := getChannelByID(ctx, tx, row.parentID)
	if err != nil {
		return domain.ToggleSuggestedPostApprovalResult{}, err
	}
	original, err := s.getChannelMessage(ctx, tx, row.monoforumID, row.messageID)
	if err != nil {
		return domain.ToggleSuggestedPostApprovalResult{}, err
	}
	recipients, err := monoforumManagerRecipientsTx(ctx, tx, row.parentID, row.payerID)
	if err != nil {
		return domain.ToggleSuggestedPostApprovalResult{}, err
	}
	result := domain.ToggleSuggestedPostApprovalResult{Monoforum: mono, Parent: parent, SavedPeer: domain.Peer{Type: domain.PeerTypeUser, ID: row.payerID}, State: row.state, OriginalMessage: original, Recipients: recipients, Duplicate: duplicate}
	if original.SuggestedPost != nil && (original.SuggestedPost.Accepted || original.SuggestedPost.Rejected) && original.Pts > 0 {
		eventDate, senderUserID := original.Date, row.actorID
		// The event row is the exact durable replay source. Retention may have
		// pruned an old event, in which case the message snapshot still provides
		// a safe replay with its original date and lifecycle actor.
		if err := tx.QueryRow(ctx, `SELECT date,sender_user_id FROM channel_update_events WHERE channel_id=$1 AND pts=$2 AND event_type='edit_channel_message'`, row.monoforumID, original.Pts).Scan(&eventDate, &senderUserID); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return domain.ToggleSuggestedPostApprovalResult{}, fmt.Errorf("load suggested post edit event: %w", err)
		}
		result.OriginalEvent = domain.ChannelUpdateEvent{ChannelID: row.monoforumID, Type: domain.ChannelUpdateEditMessage, Pts: original.Pts, PtsCount: 1, Date: eventDate, Message: original, SenderUserID: senderUserID}
	}
	if row.approvalServiceID > 0 {
		result.ServiceMessage, err = s.getChannelMessage(ctx, tx, row.monoforumID, row.approvalServiceID)
		if err != nil {
			return result, err
		}
		result.ServiceEvent = domain.ChannelUpdateEvent{ChannelID: row.monoforumID, Type: domain.ChannelUpdateNewMessage, Pts: result.ServiceMessage.Pts, PtsCount: 1, Date: result.ServiceMessage.Date, Message: result.ServiceMessage, SenderUserID: row.actorID}
	}
	if row.publishedMessageID > 0 {
		msg, e := s.getChannelMessage(ctx, tx, row.parentID, row.publishedMessageID)
		if e != nil {
			return result, e
		}
		event := domain.ChannelUpdateEvent{ChannelID: row.parentID, Type: domain.ChannelUpdateNewMessage, Pts: msg.Pts, PtsCount: 1, Date: msg.Date, Message: msg, SenderUserID: row.actorID}
		result.Published = &domain.SendChannelMessageResult{Channel: parent, Message: msg, Event: event}
	}
	return result, nil
}

func cloneSuggestedPricePG(in *domain.SuggestedPostPrice) *domain.SuggestedPostPrice {
	if in == nil {
		return nil
	}
	out := *in
	return &out
}

func (s *ChannelStore) ProcessSuggestedPostLifecycle(ctx context.Context, req domain.SuggestedPostLifecycleRequest) ([]domain.ToggleSuggestedPostApprovalResult, error) {
	if req.Now == 0 {
		req.Now = nowUnix()
	}
	if req.Limit <= 0 || req.Limit > 100 {
		req.Limit = 100
	}
	rows, err := s.db.Query(ctx, `
SELECT monoforum_id,suggestion_message_id
FROM suggested_post_approvals a
WHERE (a.state='scheduled' AND a.schedule_date <= $1)
	   OR (a.state='scheduled' AND EXISTS (
	        SELECT 1 FROM channel_messages sm
	        WHERE sm.channel_id=a.monoforum_id AND sm.id=a.suggestion_message_id AND sm.deleted))
	   OR (a.state='published' AND (a.settlement_due <= $1 OR EXISTS (
        SELECT 1 FROM channel_messages m
        WHERE m.channel_id=a.parent_channel_id AND m.id=a.published_message_id AND m.deleted)))
ORDER BY CASE WHEN a.state='scheduled' THEN a.schedule_date ELSE a.settlement_due END,
         a.monoforum_id,a.suggestion_message_id
LIMIT $2`, req.Now, req.Limit)
	if err != nil {
		return nil, fmt.Errorf("list due suggested posts: %w", err)
	}
	type key struct {
		mono    int64
		message int
	}
	keys := make([]key, 0, req.Limit)
	for rows.Next() {
		var k key
		if err := rows.Scan(&k.mono, &k.message); err != nil {
			rows.Close()
			return nil, err
		}
		keys = append(keys, k)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	out := make([]domain.ToggleSuggestedPostApprovalResult, 0, len(keys))
	for _, k := range keys {
		result, changed, err := s.processSuggestedPostLifecycleOne(ctx, k.mono, k.message, req.Now)
		if err != nil {
			return out, err
		}
		if changed {
			out = append(out, result)
		}
	}
	return out, nil
}

func (s *ChannelStore) processSuggestedPostLifecycleOne(ctx context.Context, monoID int64, messageID, now int) (domain.ToggleSuggestedPostApprovalResult, bool, error) {
	beginner, ok := s.db.(txBeginner)
	if !ok {
		return domain.ToggleSuggestedPostApprovalResult{}, false, fmt.Errorf("suggested post lifecycle: db does not support transactions")
	}
	tx, err := beginner.Begin(ctx)
	if err != nil {
		return domain.ToggleSuggestedPostApprovalResult{}, false, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()
	row, found, err := loadSuggestedPostApprovalTx(ctx, tx, monoID, messageID, true)
	if err != nil {
		return domain.ToggleSuggestedPostApprovalResult{}, false, err
	}
	if !found {
		return domain.ToggleSuggestedPostApprovalResult{}, false, fmt.Errorf("suggested post lifecycle invariant: approval row disappeared for monoforum %d message %d", monoID, messageID)
	}
	if row.state != domain.SuggestedPostStateScheduled && row.state != domain.SuggestedPostStatePublished {
		if err := tx.Commit(ctx); err != nil {
			return domain.ToggleSuggestedPostApprovalResult{}, false, err
		}
		committed = true
		return domain.ToggleSuggestedPostApprovalResult{}, false, nil
	}
	mono, err := getChannelByID(ctx, tx, row.monoforumID)
	if err != nil {
		return domain.ToggleSuggestedPostApprovalResult{}, false, err
	}
	parent, err := getChannelByID(ctx, tx, row.parentID)
	if err != nil {
		return domain.ToggleSuggestedPostApprovalResult{}, false, err
	}
	original, err := s.getChannelMessage(ctx, tx, row.monoforumID, row.messageID)
	if err != nil {
		return domain.ToggleSuggestedPostApprovalResult{}, false, err
	}
	recipients, err := monoforumManagerRecipientsTx(ctx, tx, parent.ID, row.payerID)
	if err != nil {
		return domain.ToggleSuggestedPostApprovalResult{}, false, err
	}
	result := domain.ToggleSuggestedPostApprovalResult{Monoforum: mono, Parent: parent, SavedPeer: domain.Peer{Type: domain.PeerTypeUser, ID: row.payerID}, State: row.state, Recipients: recipients}
	changed := false
	if row.state == domain.SuggestedPostStateScheduled && original.Deleted {
		if row.price != nil {
			stars, ton, err := refundSuggestedPostPaymentTx(ctx, tx, row.payerID, row.parentID, row.price, now)
			if err != nil {
				return result, false, err
			}
			result.PayerStarsBalance, result.PayerTONBalance = stars, ton
			service, event, err := s.insertSuggestedPostServiceTx(ctx, tx, mono, parent, row.actorID, true, row.savedPeer(), row.messageID, now, domain.ChannelMessageAction{Type: domain.ChannelActionSuggestedPostRefund})
			if err != nil {
				return result, false, err
			}
			result.ServiceMessage, result.ServiceEvent, row.finalServiceID = service, event, service.ID
		}
		row.state, result.State, changed = domain.SuggestedPostStateRefunded, domain.SuggestedPostStateRefunded, true
	}
	if row.state == domain.SuggestedPostStateScheduled && row.scheduleDate <= now {
		published, err := s.publishSuggestedPostTx(ctx, tx, parent, original, row.actorID, now)
		if err != nil {
			return result, false, err
		}
		result.Published = &published
		row.publishedMessageID = published.Message.ID
		if row.price == nil {
			row.state = domain.SuggestedPostStateCompleted
		} else {
			row.state = domain.SuggestedPostStatePublished
			row.settlementDue = now + suggestedPostSettlementAge
		}
		result.State = row.state
		changed = true
	}
	if row.state == domain.SuggestedPostStatePublished {
		var deleted bool
		var deleteDate int
		if err := tx.QueryRow(ctx, `SELECT deleted,delete_date FROM channel_messages WHERE channel_id=$1 AND id=$2 FOR SHARE`, row.parentID, row.publishedMessageID).Scan(&deleted, &deleteDate); err != nil {
			return result, false, err
		}
		if deleted && (deleteDate == 0 || deleteDate < row.settlementDue) {
			stars, ton, err := refundSuggestedPostPaymentTx(ctx, tx, row.payerID, row.parentID, row.price, now)
			if err != nil {
				return result, false, err
			}
			result.PayerStarsBalance, result.PayerTONBalance = stars, ton
			service, event, err := s.insertSuggestedPostServiceTx(ctx, tx, mono, parent, row.actorID, true, row.savedPeer(), row.messageID, now, domain.ChannelMessageAction{Type: domain.ChannelActionSuggestedPostRefund})
			if err != nil {
				return result, false, err
			}
			result.ServiceMessage, result.ServiceEvent = service, event
			row.state, row.finalServiceID, result.State = domain.SuggestedPostStateRefunded, service.ID, domain.SuggestedPostStateRefunded
			changed = true
		} else if row.settlementDue <= now {
			if err := settleSuggestedPostPaymentTx(ctx, tx, row.actorID, row.payerID, row.parentID, row.price, now); err != nil {
				return result, false, err
			}
			service, event, err := s.insertSuggestedPostServiceTx(ctx, tx, mono, parent, row.actorID, true, row.savedPeer(), row.messageID, now, domain.ChannelMessageAction{Type: domain.ChannelActionSuggestedPostSuccess, SuggestedPostPrice: cloneSuggestedPricePG(row.price)})
			if err != nil {
				return result, false, err
			}
			result.ServiceMessage, result.ServiceEvent = service, event
			row.state, row.finalServiceID, result.State = domain.SuggestedPostStateCompleted, service.ID, domain.SuggestedPostStateCompleted
			changed = true
		}
	}
	if !changed {
		if err := tx.Commit(ctx); err != nil {
			return result, false, err
		}
		committed = true
		return result, false, nil
	}
	if err := upsertSuggestedPostApprovalTx(ctx, tx, row, now); err != nil {
		return result, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return result, false, err
	}
	committed = true
	result.Monoforum, err = getChannelByID(ctx, s.db, row.monoforumID)
	if err != nil {
		return result, true, fmt.Errorf("reload lifecycle monoforum after commit: %w", err)
	}
	result.Parent, err = getChannelByID(ctx, s.db, row.parentID)
	if err != nil {
		return result, true, fmt.Errorf("reload lifecycle parent after commit: %w", err)
	}
	return result, true, nil
}

func (r persistedSuggestedPostApproval) savedPeer() domain.Peer {
	return domain.Peer{Type: domain.PeerTypeUser, ID: r.payerID}
}

func refundSuggestedPostPaymentTx(ctx context.Context, tx pgx.Tx, payerID, parentID int64, price *domain.SuggestedPostPrice, date int) (*domain.StarsBalance, *int64, error) {
	if price == nil {
		return nil, nil, nil
	}
	switch price.Kind {
	case domain.SuggestedPostPriceStars:
		balance := domain.StarsBalance{UserID: payerID, Granted: true}
		if err := tx.QueryRow(ctx, `INSERT INTO stars_balances(user_id,balance,granted) VALUES($1,$2,true) ON CONFLICT(user_id) DO UPDATE SET balance=stars_balances.balance+EXCLUDED.balance,updated_at=now() RETURNING balance,granted`, payerID, price.Amount).Scan(&balance.Balance, &balance.Granted); err != nil {
			return nil, nil, err
		}
		if err := insertStarsTxn(ctx, tx, payerID, price.Amount, domain.StarsReasonSuggestedPost, domain.Peer{Type: domain.PeerTypeChannel, ID: parentID}, date, "Suggested post refund", ""); err != nil {
			return nil, nil, err
		}
		return &balance, nil, nil
	case domain.SuggestedPostPriceTON:
		var balance int64
		if err := tx.QueryRow(ctx, `INSERT INTO ton_balances(user_id,balance_nanoton,granted) VALUES($1,$2,true) ON CONFLICT(user_id) DO UPDATE SET balance_nanoton=ton_balances.balance_nanoton+EXCLUDED.balance_nanoton,updated_at=now() RETURNING balance_nanoton`, payerID, price.Amount).Scan(&balance); err != nil {
			return nil, nil, err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO ton_transactions(user_id,amount_nanoton,reason,peer_type,peer_id,date) VALUES($1,$2,$3,'channel',$4,$5)`, payerID, price.Amount, string(domain.StarsReasonSuggestedPost), parentID, date); err != nil {
			return nil, nil, err
		}
		return nil, &balance, nil
	default:
		return nil, nil, domain.ErrSuggestedPostInvalid
	}
}

func settleSuggestedPostPaymentTx(ctx context.Context, tx pgx.Tx, actorID, payerID, parentID int64, price *domain.SuggestedPostPrice, date int) error {
	if price == nil {
		return nil
	}
	credit := price.Amount * paidMessageChannelCommissionPermille / 1000
	if credit <= 0 {
		return nil
	}
	switch price.Kind {
	case domain.SuggestedPostPriceStars:
		if _, err := tx.Exec(ctx, `INSERT INTO channel_stars_balances(channel_id,balance) VALUES($1,$2) ON CONFLICT(channel_id) DO UPDATE SET balance=channel_stars_balances.balance+EXCLUDED.balance,updated_at=now()`, parentID, credit); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO channel_stars_transactions(channel_id,actor_user_id,amount,reason,peer_type,peer_id,date) VALUES($1,$2,$3,$4,'user',$5,$6)`, parentID, actorID, credit, string(domain.StarsReasonSuggestedPost), payerID, date)
		return err
	case domain.SuggestedPostPriceTON:
		if _, err := tx.Exec(ctx, `INSERT INTO channel_ton_balances(channel_id,balance_nanoton) VALUES($1,$2) ON CONFLICT(channel_id) DO UPDATE SET balance_nanoton=channel_ton_balances.balance_nanoton+EXCLUDED.balance_nanoton,updated_at=now()`, parentID, credit); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO channel_ton_transactions(channel_id,actor_user_id,amount_nanoton,reason,peer_type,peer_id,date) VALUES($1,$2,$3,$4,'user',$5,$6)`, parentID, actorID, credit, string(domain.StarsReasonSuggestedPost), payerID, date)
		return err
	default:
		return domain.ErrSuggestedPostInvalid
	}
}
