package postgres

import (
	"context"
	"fmt"
	"time"

	"telesrv/internal/domain"
	"telesrv/internal/store/postgres/sqlcgen"
)

// ListSavedDialogs returns a page of saved-message sub-dialogs: on the first page (invalid offset)
// with pinned not excluded, the pinned block comes first ordered by pinned_order and the normal block
// strictly descends by top box id; subsequent pages always exclude pinned entries
// (pins are returned only with the first page to avoid duplicates across pages). Count follows the filter (with/without pins).
func (s *MessageStore) ListSavedDialogs(ctx context.Context, userID int64, filter domain.SavedDialogsFilter) (domain.SavedDialogList, error) {
	out := domain.SavedDialogList{}
	if userID == 0 {
		return out, fmt.Errorf("list saved dialogs: missing user id")
	}
	limit := filter.Limit
	if limit <= 0 || limit > domain.MaxSavedDialogsLimit {
		limit = domain.MaxSavedDialogsLimit
	}
	// TDesktop's first page has offset_id=0, DrKLO Android's first page has offset_id=int32 max;
	// both are treated as "start from the newest".
	offsetID := filter.OffsetID
	// DrKLO Android's first page sends exactly offset_id = MaxMessageBoxID (int32 max), so >= is
	// required to hit the first-page branch; otherwise the value is misread as a continuation page,
	// the pinned sub-dialog block is skipped entirely, and the first page loses its pins.
	firstPage := offsetID <= 0 || offsetID >= domain.MaxMessageBoxID
	if firstPage {
		offsetID = 0
	}
	if firstPage && !filter.ExcludePinned {
		rows, err := s.q.ListPinnedSavedDialogTops(ctx, userID)
		if err != nil {
			return out, fmt.Errorf("list pinned saved dialog tops: %w", err)
		}
		for _, row := range rows {
			if len(out.Dialogs) >= limit {
				break
			}
			msg, err := messageFromSavedDialogRow(savedDialogRowFields(row))
			if err != nil {
				return out, err
			}
			out.Dialogs = append(out.Dialogs, domain.SavedDialog{
				Peer:       domain.Peer{Type: domain.PeerType(row.DialogPeerType), ID: row.DialogPeerID},
				TopMessage: msg.ID,
				Pinned:     true,
			})
			out.Messages = append(out.Messages, msg)
		}
	}
	remaining := limit - len(out.Dialogs)
	hasMore := false
	if remaining > 0 {
		rows, err := s.q.ListSavedDialogTops(ctx, sqlcgen.ListSavedDialogTopsParams{
			OwnerUserID: userID,
			OffsetID:    pgInt32NonNegative(offsetID),
			// The normal block always excludes pinned entries: a pin either already came back with the
			// first page's pinned block or is explicitly excluded by exclude_pinned, and must never reappear on any page.
			ExcludePinned: true,
			LimitCount:    int32(remaining + 1),
		})
		if err != nil {
			return out, fmt.Errorf("list saved dialog tops: %w", err)
		}
		if len(rows) > remaining {
			hasMore = true
			rows = rows[:remaining]
		}
		for _, row := range rows {
			msg, err := messageFromSavedDialogRow(savedDialogRowFields(row))
			if err != nil {
				return out, err
			}
			out.Dialogs = append(out.Dialogs, domain.SavedDialog{
				Peer:       domain.Peer{Type: domain.PeerType(row.DialogPeerType), ID: row.DialogPeerID},
				TopMessage: msg.ID,
				Pinned:     row.DialogPinned,
			})
			out.Messages = append(out.Messages, msg)
		}
	}
	total, err := s.q.CountSavedDialogs(ctx, sqlcgen.CountSavedDialogsParams{
		OwnerUserID:   userID,
		ExcludePinned: filter.ExcludePinned,
	})
	if err != nil {
		return out, fmt.Errorf("count saved dialogs: %w", err)
	}
	out.Count = int(total)
	out.Full = !hasMore
	if err := s.enrichPrivateMessageReactions(ctx, s.db, userID, out.Messages); err != nil {
		return out, err
	}
	return out, nil
}

// ListPinnedSavedDialogs returns all pinned sub-dialogs (messages.getPinnedSavedDialogs).
func (s *MessageStore) ListPinnedSavedDialogs(ctx context.Context, userID int64) (domain.SavedDialogList, error) {
	out := domain.SavedDialogList{Full: true}
	if userID == 0 {
		return out, fmt.Errorf("list pinned saved dialogs: missing user id")
	}
	rows, err := s.q.ListPinnedSavedDialogTops(ctx, userID)
	if err != nil {
		return out, fmt.Errorf("list pinned saved dialog tops: %w", err)
	}
	for _, row := range rows {
		msg, err := messageFromSavedDialogRow(savedDialogRowFields(row))
		if err != nil {
			return out, err
		}
		out.Dialogs = append(out.Dialogs, domain.SavedDialog{
			Peer:       domain.Peer{Type: domain.PeerType(row.DialogPeerType), ID: row.DialogPeerID},
			TopMessage: msg.ID,
			Pinned:     true,
		})
		out.Messages = append(out.Messages, msg)
	}
	out.Count = len(out.Dialogs)
	if err := s.enrichPrivateMessageReactions(ctx, s.db, userID, out.Messages); err != nil {
		return out, err
	}
	return out, nil
}

// ListSavedDialogsByPeers returns the specified sub-dialogs (messages.getSavedDialogsByID);
// non-existent peers are silently absent.
func (s *MessageStore) ListSavedDialogsByPeers(ctx context.Context, userID int64, peers []domain.Peer) (domain.SavedDialogList, error) {
	out := domain.SavedDialogList{Full: true}
	if userID == 0 {
		return out, fmt.Errorf("list saved dialogs by peers: missing user id")
	}
	peerTypes := make([]string, 0, len(peers))
	peerIDs := make([]int64, 0, len(peers))
	for _, peer := range peers {
		if peer.Type == "" || peer.ID == 0 {
			continue
		}
		peerTypes = append(peerTypes, string(peer.Type))
		peerIDs = append(peerIDs, peer.ID)
	}
	if len(peerTypes) == 0 {
		return out, nil
	}
	rows, err := s.q.ListSavedDialogTopsByPeers(ctx, sqlcgen.ListSavedDialogTopsByPeersParams{
		OwnerUserID: userID,
		PeerTypes:   peerTypes,
		PeerIds:     peerIDs,
	})
	if err != nil {
		return out, fmt.Errorf("list saved dialog tops by peers: %w", err)
	}
	for _, row := range rows {
		msg, err := messageFromSavedDialogRow(savedDialogRowFields(row))
		if err != nil {
			return out, err
		}
		out.Dialogs = append(out.Dialogs, domain.SavedDialog{
			Peer:       domain.Peer{Type: domain.PeerType(row.DialogPeerType), ID: row.DialogPeerID},
			TopMessage: msg.ID,
			Pinned:     row.DialogPinned,
		})
		out.Messages = append(out.Messages, msg)
	}
	out.Count = len(out.Dialogs)
	if err := s.enrichPrivateMessageReactions(ctx, s.db, userID, out.Messages); err != nil {
		return out, err
	}
	return out, nil
}

// ToggleSavedDialogPin toggles a sub-dialog's pinned state; a new pin is inserted at the front.
// Returns whether the state actually changed.
func (s *MessageStore) ToggleSavedDialogPin(ctx context.Context, userID int64, peer domain.Peer, pinned bool) (bool, error) {
	if userID == 0 || peer.Type == "" || peer.ID == 0 {
		return false, fmt.Errorf("toggle saved dialog pin: invalid input")
	}
	if !pinned {
		rows, err := s.q.DeleteSavedDialogPin(ctx, sqlcgen.DeleteSavedDialogPinParams{
			UserID:   userID,
			PeerType: string(peer.Type),
			PeerID:   peer.ID,
		})
		if err != nil {
			return false, fmt.Errorf("delete saved dialog pin: %w", err)
		}
		return rows > 0, nil
	}
	count, err := s.q.CountSavedDialogPins(ctx, userID)
	if err != nil {
		return false, fmt.Errorf("count saved dialog pins: %w", err)
	}
	if int(count) >= domain.MaxPinnedSavedDialogs {
		return false, domain.ErrPinnedSavedDialogsTooMuch
	}
	rows, err := s.q.UpsertSavedDialogPinFront(ctx, sqlcgen.UpsertSavedDialogPinFrontParams{
		UserID:   userID,
		PeerType: string(peer.Type),
		PeerID:   peer.ID,
	})
	if err != nil {
		return false, fmt.Errorf("upsert saved dialog pin: %w", err)
	}
	return rows > 0, nil
}

// ReorderPinnedSavedDialogs fully reorders the pins; when force is set it clears existing
// pins not present in order (matching the force semantics of messages.reorderPinnedDialogs).
func (s *MessageStore) ReorderPinnedSavedDialogs(ctx context.Context, userID int64, order []domain.Peer, force bool) error {
	if userID == 0 {
		return fmt.Errorf("reorder pinned saved dialogs: missing user id")
	}
	if len(order) > domain.MaxPinnedSavedDialogs {
		return domain.ErrPinnedSavedDialogsTooMuch
	}
	peerTypes := make([]string, 0, len(order))
	peerIDs := make([]int64, 0, len(order))
	for _, peer := range order {
		if peer.Type == "" || peer.ID == 0 {
			continue
		}
		peerTypes = append(peerTypes, string(peer.Type))
		peerIDs = append(peerIDs, peer.ID)
	}
	if force {
		if err := s.q.ClearSavedDialogPinsNotInOrder(ctx, sqlcgen.ClearSavedDialogPinsNotInOrderParams{
			UserID:    userID,
			PeerTypes: peerTypes,
			PeerIds:   peerIDs,
		}); err != nil {
			return fmt.Errorf("clear saved dialog pins not in order: %w", err)
		}
	}
	if len(peerTypes) == 0 {
		return nil
	}
	if err := s.q.ReorderSavedDialogPins(ctx, sqlcgen.ReorderSavedDialogPinsParams{
		UserID:    userID,
		PeerTypes: peerTypes,
		PeerIds:   peerIDs,
	}); err != nil {
		return fmt.Errorf("reorder saved dialog pins: %w", err)
	}
	return nil
}

// DeleteSavedHistory deletes the messages of one saved sub-dialog in the self-chat (single batch,
// capped at MaxDeleteHistoryBatch), produces a pts-carrying delete_messages durable event and
// repairs the self dialog top; when the sub-dialog is emptied it also clears its pin row.
func (s *MessageStore) DeleteSavedHistory(ctx context.Context, req domain.DeleteSavedHistoryRequest) (domain.DeleteSavedHistoryResult, error) {
	res := domain.DeleteSavedHistoryResult{}
	if req.OwnerUserID == 0 || req.SavedPeer.Type == "" || req.SavedPeer.ID == 0 {
		return res, fmt.Errorf("delete saved history: invalid input")
	}
	if req.Date == 0 {
		req.Date = int(time.Now().Unix())
	}
	beginner, ok := s.db.(txBeginner)
	if !ok {
		return res, fmt.Errorf("delete saved history: db does not support transactions")
	}
	tx, err := beginner.Begin(ctx)
	if err != nil {
		return res, fmt.Errorf("begin delete saved history tx: %w", err)
	}
	qtx := sqlcgen.New(tx)
	committed := false
	defer func() {
		if committed {
			return
		}
		_ = tx.Rollback(ctx)
	}()
	if err := lockUsersForUpdate(ctx, tx, req.OwnerUserID); err != nil {
		return res, fmt.Errorf("lock delete saved history user: %w", err)
	}
	rows, err := qtx.DeleteMessageBoxesBySavedPeerBatch(ctx, sqlcgen.DeleteMessageBoxesBySavedPeerBatchParams{
		OwnerUserID:   req.OwnerUserID,
		SavedPeerType: string(req.SavedPeer.Type),
		SavedPeerID:   req.SavedPeer.ID,
		MaxID:         pgInt32NonNegative(req.MaxID),
		MinDate:       pgInt32NonNegative(req.MinDate),
		MaxDate:       pgInt32NonNegative(req.MaxDate),
		LimitCount:    int32(domain.MaxDeleteHistoryBatch),
	})
	if err != nil {
		return res, fmt.Errorf("delete saved history batch: %w", err)
	}
	deleted := make([]deletedBox, 0, len(rows))
	for _, row := range rows {
		deleted = append(deleted, deletedBox{
			ownerUserID:      row.OwnerUserID,
			boxID:            int(row.BoxID),
			privateMessageID: row.PrivateMessageID,
			messageSenderID:  row.MessageSenderID,
			peer:             domain.Peer{Type: domain.PeerType(row.PeerType), ID: row.PeerID},
		})
	}
	delRes, err := s.finishDeleteMessagesTx(ctx, tx, qtx, req.OwnerUserID, req.OriginAuthKeyID, req.OriginSessionID, req.Date, deleted, nil)
	if err != nil {
		return res, err
	}
	if len(deleted) > 0 {
		more, err := qtx.HasDeletableMessageBoxBySavedPeer(ctx, sqlcgen.HasDeletableMessageBoxBySavedPeerParams{
			OwnerUserID:   req.OwnerUserID,
			SavedPeerType: string(req.SavedPeer.Type),
			SavedPeerID:   req.SavedPeer.ID,
			MaxID:         pgInt32NonNegative(req.MaxID),
			MinDate:       pgInt32NonNegative(req.MinDate),
			MaxDate:       pgInt32NonNegative(req.MaxDate),
		})
		if err != nil {
			return res, fmt.Errorf("probe deletable saved history: %w", err)
		}
		res.More = more
		if !more {
			// There may be leftovers outside the boundary range (e.g. max_id truncation), so the pin
			// must not be cleared while the sub-dialog is still alive; only a full wipe clears it.
			alive, err := qtx.HasDeletableMessageBoxBySavedPeer(ctx, sqlcgen.HasDeletableMessageBoxBySavedPeerParams{
				OwnerUserID:   req.OwnerUserID,
				SavedPeerType: string(req.SavedPeer.Type),
				SavedPeerID:   req.SavedPeer.ID,
			})
			if err != nil {
				return res, fmt.Errorf("probe saved dialog alive: %w", err)
			}
			if !alive {
				if _, err := qtx.DeleteSavedDialogPin(ctx, sqlcgen.DeleteSavedDialogPinParams{
					UserID:   req.OwnerUserID,
					PeerType: string(req.SavedPeer.Type),
					PeerID:   req.SavedPeer.ID,
				}); err != nil {
					return res, fmt.Errorf("clear pin after saved history delete: %w", err)
				}
			}
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return res, fmt.Errorf("commit delete saved history tx: %w", err)
	}
	committed = true
	for _, d := range delRes.Deleted {
		if d.UserID == req.OwnerUserID {
			res.MessageIDs = d.MessageIDs
			res.Event = d.Event
		}
	}
	return res, nil
}

// savedDialogTopRowFields is the row shape shared by the three saved dialog top queries.
type savedDialogTopRowFields struct {
	BoxID                int32
	PrivateMessageID     int64
	OwnerUserID          int64
	PeerType             string
	PeerID               int64
	FromUserID           int64
	MessageDate          int32
	TtlPeriod            int32
	ExpiresAt            int32
	EditDate             int32
	HideEdited           bool
	Outgoing             bool
	Body                 string
	EntitiesJson         string
	Silent               bool
	Noforwards           bool
	ReplyToMsgID         int32
	ReplyToPeerType      string
	ReplyToPeerID        int64
	ReplyToTopID         int32
	ReplyToStoryID       int32
	QuoteText            string
	QuoteEntitiesJson    string
	QuoteOffset          int32
	ReplyExternalJson    string
	FwdFromPeerType      string
	FwdFromPeerID        int64
	FwdFromName          string
	FwdDate              int32
	FwdSavedFromPeerType string
	FwdSavedFromPeerID   int64
	FwdSavedFromMsgID    int32
	SavedPeerType        string
	SavedPeerID          int64
	Pts                  int32
	MediaJson            string
	MediaUnread          bool
	ReactionUnread       bool
	ViaBotID             int64
	GroupedID            int64
	Effect               int64
	ReplyMarkupJson      string
	RichMessageJson      string
	Pinned               bool
}

func savedDialogRowFields[T sqlcgen.ListSavedDialogTopsRow | sqlcgen.ListPinnedSavedDialogTopsRow | sqlcgen.ListSavedDialogTopsByPeersRow](row T) savedDialogTopRowFields {
	switch r := any(row).(type) {
	case sqlcgen.ListSavedDialogTopsRow:
		return savedDialogTopRowFields{r.BoxID, r.PrivateMessageID, r.OwnerUserID, r.PeerType, r.PeerID, r.FromUserID, r.MessageDate, r.TtlPeriod, r.ExpiresAt, r.EditDate, r.HideEdited, r.Outgoing, r.Body, r.EntitiesJson, r.Silent, r.Noforwards, r.ReplyToMsgID, r.ReplyToPeerType, r.ReplyToPeerID, r.ReplyToTopID, r.ReplyToStoryID, r.QuoteText, r.QuoteEntitiesJson, r.QuoteOffset, r.ReplyExternalJson, r.FwdFromPeerType, r.FwdFromPeerID, r.FwdFromName, r.FwdDate, r.FwdSavedFromPeerType, r.FwdSavedFromPeerID, r.FwdSavedFromMsgID, r.SavedPeerType, r.SavedPeerID, r.Pts, r.MediaJson, r.MediaUnread, r.ReactionUnread, r.ViaBotID, r.GroupedID, r.Effect, r.ReplyMarkupJson, r.RichMessageJson, r.Pinned}
	case sqlcgen.ListPinnedSavedDialogTopsRow:
		return savedDialogTopRowFields{r.BoxID, r.PrivateMessageID, r.OwnerUserID, r.PeerType, r.PeerID, r.FromUserID, r.MessageDate, r.TtlPeriod, r.ExpiresAt, r.EditDate, r.HideEdited, r.Outgoing, r.Body, r.EntitiesJson, r.Silent, r.Noforwards, r.ReplyToMsgID, r.ReplyToPeerType, r.ReplyToPeerID, r.ReplyToTopID, r.ReplyToStoryID, r.QuoteText, r.QuoteEntitiesJson, r.QuoteOffset, r.ReplyExternalJson, r.FwdFromPeerType, r.FwdFromPeerID, r.FwdFromName, r.FwdDate, r.FwdSavedFromPeerType, r.FwdSavedFromPeerID, r.FwdSavedFromMsgID, r.SavedPeerType, r.SavedPeerID, r.Pts, r.MediaJson, r.MediaUnread, r.ReactionUnread, r.ViaBotID, r.GroupedID, r.Effect, r.ReplyMarkupJson, r.RichMessageJson, r.Pinned}
	case sqlcgen.ListSavedDialogTopsByPeersRow:
		return savedDialogTopRowFields{r.BoxID, r.PrivateMessageID, r.OwnerUserID, r.PeerType, r.PeerID, r.FromUserID, r.MessageDate, r.TtlPeriod, r.ExpiresAt, r.EditDate, r.HideEdited, r.Outgoing, r.Body, r.EntitiesJson, r.Silent, r.Noforwards, r.ReplyToMsgID, r.ReplyToPeerType, r.ReplyToPeerID, r.ReplyToTopID, r.ReplyToStoryID, r.QuoteText, r.QuoteEntitiesJson, r.QuoteOffset, r.ReplyExternalJson, r.FwdFromPeerType, r.FwdFromPeerID, r.FwdFromName, r.FwdDate, r.FwdSavedFromPeerType, r.FwdSavedFromPeerID, r.FwdSavedFromMsgID, r.SavedPeerType, r.SavedPeerID, r.Pts, r.MediaJson, r.MediaUnread, r.ReactionUnread, r.ViaBotID, r.GroupedID, r.Effect, r.ReplyMarkupJson, r.RichMessageJson, r.Pinned}
	}
	return savedDialogTopRowFields{}
}

func messageFromSavedDialogRow(row savedDialogTopRowFields) (domain.Message, error) {
	entities, err := decodeMessageEntities(row.EntitiesJson)
	if err != nil {
		return domain.Message{}, fmt.Errorf("decode saved dialog message entities: %w", err)
	}
	silent, noforwards, reply, forward, err := messageMetadataFromFields(
		row.Silent,
		row.Noforwards,
		row.ReplyToMsgID,
		row.ReplyToPeerType,
		row.ReplyToPeerID,
		row.ReplyToTopID,
		row.ReplyToStoryID,
		row.QuoteText,
		row.QuoteEntitiesJson,
		row.QuoteOffset,
		row.ReplyExternalJson,
		row.FwdFromPeerType,
		row.FwdFromPeerID,
		row.FwdFromName,
		row.FwdDate,
		row.FwdSavedFromPeerType,
		row.FwdSavedFromPeerID,
		row.FwdSavedFromMsgID,
	)
	if err != nil {
		return domain.Message{}, fmt.Errorf("decode saved dialog message metadata: %w", err)
	}
	media, err := decodeMessageMedia(row.MediaJson)
	if err != nil {
		return domain.Message{}, fmt.Errorf("decode saved dialog message media: %w", err)
	}
	markup, err := decodeReplyMarkup(row.ReplyMarkupJson)
	if err != nil {
		return domain.Message{}, fmt.Errorf("decode saved dialog message reply markup: %w", err)
	}
	rich, err := decodeRichMessage(row.RichMessageJson)
	if err != nil {
		return domain.Message{}, fmt.Errorf("decode saved dialog message rich message: %w", err)
	}
	return domain.Message{
		ID:             int(row.BoxID),
		UID:            row.PrivateMessageID,
		OwnerUserID:    row.OwnerUserID,
		Peer:           domain.Peer{Type: domain.PeerType(row.PeerType), ID: row.PeerID},
		From:           domain.Peer{Type: domain.PeerTypeUser, ID: row.FromUserID},
		Date:           int(row.MessageDate),
		EditDate:       int(row.EditDate),
		HideEdited:     row.HideEdited,
		Out:            row.Outgoing,
		Silent:         silent,
		NoForwards:     noforwards,
		Body:           row.Body,
		Entities:       entities,
		ReplyTo:        reply,
		Forward:        forward,
		Pts:            int(row.Pts),
		TTLPeriod:      int(row.TtlPeriod),
		ExpiresAt:      int(row.ExpiresAt),
		Media:          media,
		MediaUnread:    row.MediaUnread,
		ReactionUnread: row.ReactionUnread,
		ViaBotID:       row.ViaBotID,
		GroupedID:      row.GroupedID,
		Effect:         row.Effect,
		ReplyMarkup:    markup,
		RichMessage:    rich,
		Pinned:         row.Pinned,
		SavedPeer:      savedPeerFromFields(row.SavedPeerType, row.SavedPeerID),
	}, nil
}
