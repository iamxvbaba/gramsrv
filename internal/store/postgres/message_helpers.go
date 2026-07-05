package postgres

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"github.com/jackc/pgx/v5"
	"hash/fnv"
	"sort"
	"telesrv/internal/domain"
)

func cloneMessageReply(reply *domain.MessageReply) *domain.MessageReply {
	if reply == nil {
		return nil
	}
	clone := *reply
	clone.QuoteEntities = append([]domain.MessageEntity(nil), reply.QuoteEntities...)
	return &clone
}

func cloneChannelMessageAction(action *domain.ChannelMessageAction) *domain.ChannelMessageAction {
	if action == nil {
		return nil
	}
	clone := *action
	clone.UserIDs = append([]int64(nil), action.UserIDs...)
	clone.Completed = append([]int(nil), action.Completed...)
	clone.Incompleted = append([]int(nil), action.Incompleted...)
	clone.TodoItems = append([]domain.MessageTodoItem(nil), action.TodoItems...)
	if action.Closed != nil {
		v := *action.Closed
		clone.Closed = &v
	}
	if action.Hidden != nil {
		v := *action.Hidden
		clone.Hidden = &v
	}
	if action.StarGift != nil {
		g := *action.StarGift
		if action.StarGift.Sticker != nil {
			sticker := *action.StarGift.Sticker
			g.Sticker = &sticker
		}
		clone.StarGift = &g
	}
	clone.Wallpaper = domain.CloneWallpaperPtr(action.Wallpaper)
	clone.Photo = domain.ClonePhotoPtr(action.Photo)
	return &clone
}

func normalizeMessageIDs(ids []int) []int {
	if len(ids) == 0 {
		return nil
	}
	out := make([]int, 0, len(ids))
	seen := make(map[int]struct{}, len(ids))
	for _, id := range ids {
		if id <= 0 || id > domain.MaxMessageBoxID {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	sort.Ints(out)
	return out
}

func int32s(ids []int) []int32 {
	if len(ids) == 0 {
		return nil
	}
	out := make([]int32, 0, len(ids))
	for _, id := range ids {
		out = append(out, pgInt32NonNegative(id))
	}
	return out
}

func pgInt32NonNegative(v int) int32 {
	if v <= 0 {
		return 0
	}
	if v > domain.MaxMessageBoxID {
		return int32(domain.MaxMessageBoxID)
	}
	return int32(v)
}

func pgInt32Bounded(v int) int32 {
	if v > domain.MaxMessageBoxID {
		return int32(domain.MaxMessageBoxID)
	}
	if v < -domain.MaxMessageBoxID {
		return int32(-domain.MaxMessageBoxID)
	}
	return int32(v)
}

func maxMessageBoxIDForDialog(ctx context.Context, tx pgx.Tx, ownerUserID int64, peer domain.Peer) (int, error) {
	var maxID int
	if err := tx.QueryRow(ctx, `
SELECT COALESCE(MAX(box_id), 0)::int
FROM message_boxes
WHERE owner_user_id = $1
  AND peer_type = $2
  AND peer_id = $3`, ownerUserID, string(peer.Type), peer.ID).Scan(&maxID); err != nil {
		return 0, err
	}
	return maxID, nil
}

type messageMetadataParams struct {
	Silent               bool
	Noforwards           bool
	ReplyToMsgID         int32
	ReplyToPeerType      string
	ReplyToPeerID        int64
	ReplyToTopID         int32
	ReplyToStoryID       int32
	QuoteText            string
	QuoteEntitiesJSON    []byte
	QuoteOffset          int32
	FwdFromPeerType      string
	FwdFromPeerID        int64
	FwdFromName          string
	FwdDate              int32
	FwdSavedFromPeerType string
	FwdSavedFromPeerID   int64
	FwdSavedFromMsgID    int32
	// SavedPeerType/SavedPeerID 由发送路径按 self-chat 语义填充，
	// 不属于 forward header 本身。
	SavedPeerType string
	SavedPeerID   int64
}

func messageMetadataParamsFrom(silent, noforwards bool, reply *domain.MessageReply, forward *domain.MessageForward) (messageMetadataParams, error) {
	meta := messageMetadataParams{
		Silent:            silent,
		Noforwards:        noforwards,
		QuoteEntitiesJSON: []byte("[]"),
	}
	if reply != nil {
		if err := domain.ValidateMessageReplyBounds(reply); err != nil {
			return messageMetadataParams{}, err
		}
		quoteEntities, err := encodeMessageEntities(reply.QuoteEntities)
		if err != nil {
			return messageMetadataParams{}, err
		}
		meta.ReplyToMsgID = int32(reply.MessageID)
		meta.ReplyToPeerType = string(reply.Peer.Type)
		meta.ReplyToPeerID = reply.Peer.ID
		meta.ReplyToTopID = int32(reply.TopMessageID)
		meta.ReplyToStoryID = int32(reply.StoryID)
		meta.QuoteText = reply.QuoteText
		meta.QuoteEntitiesJSON = quoteEntities
		meta.QuoteOffset = int32(reply.QuoteOffset)
	}
	if forward != nil {
		if forward.Date < 0 {
			return messageMetadataParams{}, fmt.Errorf("forward metadata: invalid date")
		}
		meta.FwdFromPeerType = string(forward.From.Type)
		meta.FwdFromPeerID = forward.From.ID
		meta.FwdFromName = forward.FromName
		meta.FwdDate = int32(forward.Date)
		if forward.SavedFrom.ID != 0 {
			meta.FwdSavedFromPeerType = string(forward.SavedFrom.Type)
			meta.FwdSavedFromPeerID = forward.SavedFrom.ID
			meta.FwdSavedFromMsgID = int32(forward.SavedFromMsgID)
		}
	}
	return meta, nil
}

func messageMetadataFromFields(silent, noforwards bool, replyToMsgID int32, replyToPeerType string, replyToPeerID int64, replyToTopID int32, replyToStoryID int32, quoteText, quoteEntitiesJSON string, quoteOffset int32, fwdFromPeerType string, fwdFromPeerID int64, fwdFromName string, fwdDate int32, fwdSavedFromPeerType string, fwdSavedFromPeerID int64, fwdSavedFromMsgID int32) (bool, bool, *domain.MessageReply, *domain.MessageForward, error) {
	var reply *domain.MessageReply
	if replyToMsgID > 0 || replyToStoryID > 0 {
		quoteEntities, err := decodeMessageEntities(quoteEntitiesJSON)
		if err != nil {
			return false, false, nil, nil, err
		}
		reply = &domain.MessageReply{
			MessageID:     int(replyToMsgID),
			Peer:          domain.Peer{Type: domain.PeerType(replyToPeerType), ID: replyToPeerID},
			TopMessageID:  int(replyToTopID),
			StoryID:       int(replyToStoryID),
			QuoteText:     quoteText,
			QuoteEntities: quoteEntities,
			QuoteOffset:   int(quoteOffset),
		}
	}
	var forward *domain.MessageForward
	if fwdDate != 0 || fwdFromPeerID != 0 || fwdFromName != "" || fwdSavedFromPeerID != 0 {
		forward = &domain.MessageForward{
			From:     domain.Peer{Type: domain.PeerType(fwdFromPeerType), ID: fwdFromPeerID},
			FromName: fwdFromName,
			Date:     int(fwdDate),
		}
		if fwdSavedFromPeerID != 0 {
			forward.SavedFrom = domain.Peer{Type: domain.PeerType(fwdSavedFromPeerType), ID: fwdSavedFromPeerID}
			forward.SavedFromMsgID = int(fwdSavedFromMsgID)
		}
	}
	return silent, noforwards, reply, forward, nil
}

// savedPeerFromFields 还原 self-chat box 行的 saved 子会话分组键。
func savedPeerFromFields(savedPeerType string, savedPeerID int64) domain.Peer {
	if savedPeerType == "" || savedPeerID == 0 {
		return domain.Peer{}
	}
	return domain.Peer{Type: domain.PeerType(savedPeerType), ID: savedPeerID}
}

func (a pgBoxIDAllocator) NextBoxID(ctx context.Context, userID int64) (int, error) {
	cur, err := a.CurrentBoxID(ctx, userID)
	if err != nil {
		return 0, err
	}
	return cur + 1, nil
}

func (a pgBoxIDAllocator) CurrentBoxID(ctx context.Context, userID int64) (int, error) {
	v, err := a.s.q.MaxMessageBoxID(ctx, userID)
	if err != nil {
		return 0, err
	}
	return int(v), nil
}

type messageEntityJSON struct {
	Type       string `json:"type"`
	Offset     int    `json:"offset"`
	Length     int    `json:"length"`
	URL        string `json:"url,omitempty"`
	UserID     int64  `json:"user_id,omitempty"`
	Language   string `json:"language,omitempty"`
	DocumentID int64  `json:"document_id,omitempty"`
	Collapsed  bool   `json:"collapsed,omitempty"`
	Date       int    `json:"date,omitempty"`
	Relative   bool   `json:"relative,omitempty"`
	ShortTime  bool   `json:"short_time,omitempty"`
	LongTime   bool   `json:"long_time,omitempty"`
	ShortDate  bool   `json:"short_date,omitempty"`
	LongDate   bool   `json:"long_date,omitempty"`
	DayOfWeek  bool   `json:"day_of_week,omitempty"`
}

func encodeMessageEntities(entities []domain.MessageEntity) ([]byte, error) {
	if len(entities) == 0 {
		return []byte("[]"), nil
	}
	wire := make([]messageEntityJSON, 0, len(entities))
	for _, entity := range entities {
		wire = append(wire, messageEntityJSON{
			Type:       string(entity.Type),
			Offset:     entity.Offset,
			Length:     entity.Length,
			URL:        entity.URL,
			UserID:     entity.UserID,
			Language:   entity.Language,
			DocumentID: entity.DocumentID,
			Collapsed:  entity.Collapsed,
			Date:       entity.Date,
			Relative:   entity.Relative,
			ShortTime:  entity.ShortTime,
			LongTime:   entity.LongTime,
			ShortDate:  entity.ShortDate,
			LongDate:   entity.LongDate,
			DayOfWeek:  entity.DayOfWeek,
		})
	}
	raw, err := json.Marshal(wire)
	if err != nil {
		return nil, fmt.Errorf("marshal message entities: %w", err)
	}
	return raw, nil
}

func decodeMessageEntities(raw string) ([]domain.MessageEntity, error) {
	if raw == "" {
		return nil, nil
	}
	var wire []messageEntityJSON
	if err := json.Unmarshal([]byte(raw), &wire); err != nil {
		return nil, err
	}
	out := make([]domain.MessageEntity, 0, len(wire))
	for _, entity := range wire {
		out = append(out, domain.MessageEntity{
			Type:       domain.MessageEntityType(entity.Type),
			Offset:     entity.Offset,
			Length:     entity.Length,
			URL:        entity.URL,
			UserID:     entity.UserID,
			Language:   entity.Language,
			DocumentID: entity.DocumentID,
			Collapsed:  entity.Collapsed,
			Date:       entity.Date,
			Relative:   entity.Relative,
			ShortTime:  entity.ShortTime,
			LongTime:   entity.LongTime,
			ShortDate:  entity.ShortDate,
			LongDate:   entity.LongDate,
			DayOfWeek:  entity.DayOfWeek,
		})
	}
	return out, nil
}

func sameMessageEntities(a, b []domain.MessageEntity) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func messageListHash(messages []domain.Message) int64 {
	if len(messages) == 0 {
		return 0
	}
	h := fnv.New64a()
	var buf [24]byte
	for _, msg := range messages {
		binary.LittleEndian.PutUint32(buf[:4], uint32(msg.ID))
		binary.LittleEndian.PutUint32(buf[4:8], uint32(msg.Date))
		binary.LittleEndian.PutUint64(buf[8:16], uint64(msg.From.ID))
		binary.LittleEndian.PutUint64(buf[16:24], uint64(msg.UID))
		_, _ = h.Write(buf[:])
		writeMessageReactionsHash(h, msg.Reactions)
	}
	return int64(h.Sum64())
}
