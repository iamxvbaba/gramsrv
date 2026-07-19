package memory

import (
	"encoding/binary"
	"hash/fnv"
	"reflect"
	"sort"
	"telesrv/internal/domain"
)

func (s *MessageStore) deleteMemoryMessagesLocked(userID int64, limit int, match func(domain.Message) bool) ([]deletedMemoryMessage, map[int64]struct{}, bool) {
	messages := s.m[userID]
	kept := messages[:0]
	deleted := make([]deletedMemoryMessage, 0)
	revokeUIDs := make(map[int64]struct{})
	more := false
	for _, msg := range messages {
		if match(msg) {
			if limit > 0 && len(deleted) >= limit {
				kept = append(kept, msg)
				more = true
				continue
			}
			deleted = append(deleted, deletedMemoryMessage{
				userID:           userID,
				peer:             msg.Peer,
				id:               msg.ID,
				privateMessageID: msg.UID,
				messageSenderID:  msg.From.ID,
				randomID:         msg.RandomID,
			})
			if msg.UID != 0 {
				revokeUIDs[msg.UID] = struct{}{}
			}
			continue
		}
		kept = append(kept, msg)
	}
	s.m[userID] = kept
	return deleted, revokeUIDs, more
}

func (s *MessageStore) deleteMemoryMessagesByUIDLocked(uids map[int64]struct{}, excludeUserID int64) []deletedMemoryMessage {
	if len(uids) == 0 {
		return nil
	}
	deleted := make([]deletedMemoryMessage, 0)
	for userID, messages := range s.m {
		if userID == excludeUserID {
			continue
		}
		kept := messages[:0]
		for _, msg := range messages {
			if _, ok := uids[msg.UID]; ok {
				deleted = append(deleted, deletedMemoryMessage{
					userID:           userID,
					peer:             msg.Peer,
					id:               msg.ID,
					privateMessageID: msg.UID,
					messageSenderID:  msg.From.ID,
					randomID:         msg.RandomID,
				})
				continue
			}
			kept = append(kept, msg)
		}
		s.m[userID] = kept
	}
	return deleted
}

func normalizeMemoryMessageIDs(ids []int) []int {
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

func cloneMessage(msg domain.Message) domain.Message {
	msg.Entities = append([]domain.MessageEntity(nil), msg.Entities...)
	msg.Media = cloneRequestedPeerMedia(msg.Media)
	msg.ReplyTo = cloneMessageReply(msg.ReplyTo)
	msg.Forward = cloneMessageForward(msg.Forward)
	msg.Reactions = cloneChannelMessageReactionsPtr(msg.Reactions)
	msg.ReplyMarkup = cloneReplyMarkup(msg.ReplyMarkup)
	msg.RichMessage = cloneRichMessage(msg.RichMessage)
	return msg
}

// cloneRequestedPeerMedia isolates the immutable disclosure snapshot carried by
// messageActionRequestedPeer. Other media payloads retain their established
// copy behavior; this helper only deep-copies the newly mutable peer/photo slices.
func cloneRequestedPeerMedia(media *domain.MessageMedia) *domain.MessageMedia {
	if media == nil {
		return nil
	}
	clone := *media
	if media.ServiceAction == nil || media.ServiceAction.RequestedPeer == nil {
		return &clone
	}
	action := *media.ServiceAction
	requested := *media.ServiceAction.RequestedPeer
	requested.Peers = append([]domain.Peer(nil), requested.Peers...)
	requested.Details = append([]domain.MessageRequestedPeerDetails(nil), requested.Details...)
	for i := range requested.Details {
		requested.Details[i].Photo = domain.ClonePhotoPtr(requested.Details[i].Photo)
	}
	action.RequestedPeer = &requested
	clone.ServiceAction = &action
	return &clone
}

// cloneReplyMarkup 深拷 reply markup 快照：与 postgres 每盒独立 decode 对齐
// （双 store 行为一致），避免发送方/接收方两行共享底层 rows/Data 切片。
func cloneReplyMarkup(m *domain.MessageReplyMarkup) *domain.MessageReplyMarkup {
	if m == nil {
		return nil
	}
	clone := *m
	if m.Inline != nil {
		clone.Inline = make([][]domain.MarkupButton, len(m.Inline))
		for i, row := range m.Inline {
			cloneRow := make([]domain.MarkupButton, len(row))
			for j, btn := range row {
				cloneRow[j] = btn
				cloneRow[j].Data = append([]byte(nil), btn.Data...)
			}
			clone.Inline[i] = cloneRow
		}
	}
	if m.Keyboard != nil {
		clone.Keyboard = make([][]domain.MarkupButton, len(m.Keyboard))
		for i, row := range m.Keyboard {
			clone.Keyboard[i] = append([]domain.MarkupButton(nil), row...)
		}
	}
	return &clone
}

// cloneRichMessage 深拷 Layer 227 富文本快照：复制不透明 blocks 字节与内嵌媒体切片，
// 避免发送方/接收方两行共享底层切片（与 postgres 每盒独立 decode 对齐）。
func cloneRichMessage(m *domain.MessageRichMessage) *domain.MessageRichMessage {
	if m == nil {
		return nil
	}
	clone := *m
	clone.Blocks = append([]byte(nil), m.Blocks...)
	clone.Photos = append([]domain.Photo(nil), m.Photos...)
	clone.Documents = append([]domain.Document(nil), m.Documents...)
	return &clone
}

func richMessagesEqual(a, b *domain.MessageRichMessage) bool {
	if a.IsZero() && b.IsZero() {
		return true
	}
	return reflect.DeepEqual(a, b)
}

func cloneMessageReply(reply *domain.MessageReply) *domain.MessageReply {
	if reply == nil {
		return nil
	}
	clone := *reply
	clone.QuoteEntities = append([]domain.MessageEntity(nil), reply.QuoteEntities...)
	return &clone
}

func cloneMessageForward(forward *domain.MessageForward) *domain.MessageForward {
	if forward == nil {
		return nil
	}
	clone := *forward
	return &clone
}

func newMessageEvent(msg domain.Message) domain.UpdateEvent {
	if msg.ID == 0 {
		return domain.UpdateEvent{}
	}
	return domain.UpdateEvent{
		UserID:   msg.OwnerUserID,
		Type:     domain.UpdateEventNewMessage,
		Pts:      msg.Pts,
		PtsCount: 1,
		Date:     msg.Date,
		Message:  cloneMessage(msg),
	}
}

func editMessageEvent(msg domain.Message) domain.UpdateEvent {
	if msg.ID == 0 {
		return domain.UpdateEvent{}
	}
	return domain.UpdateEvent{
		UserID:   msg.OwnerUserID,
		Type:     domain.UpdateEventEditMessage,
		Pts:      msg.Pts,
		PtsCount: 1,
		Date:     msg.EditDate,
		Message:  cloneMessage(msg),
	}
}

// webPageEvent 是链接预览就地替换事件（Date 取消息发送时间，不引入 edit_date）。
func webPageEvent(msg domain.Message) domain.UpdateEvent {
	if msg.ID == 0 {
		return domain.UpdateEvent{}
	}
	return domain.UpdateEvent{
		UserID:   msg.OwnerUserID,
		Type:     domain.UpdateEventWebPage,
		Pts:      msg.Pts,
		PtsCount: 1,
		Date:     msg.Date,
		Message:  cloneMessage(msg),
	}
}

func equalMessageEntities(a, b []domain.MessageEntity) bool {
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

func hasUser(users []domain.User, id int64) bool {
	for _, u := range users {
		if u.ID == id {
			return true
		}
	}
	return false
}

func messageListHash(messages []domain.Message) int64 {
	if len(messages) == 0 {
		return 0
	}
	h := fnv.New64a()
	var buf [16]byte
	for _, msg := range messages {
		binary.LittleEndian.PutUint32(buf[:4], uint32(msg.ID))
		binary.LittleEndian.PutUint32(buf[4:8], uint32(msg.Date))
		binary.LittleEndian.PutUint64(buf[8:16], uint64(msg.From.ID))
		_, _ = h.Write(buf[:])
		writeMessageReactionsHash(h, msg.Reactions)
	}
	return int64(h.Sum64())
}

func cloneMessages(messages []domain.Message) []domain.Message {
	out := append([]domain.Message(nil), messages...)
	for i := range out {
		out[i] = cloneMessage(out[i])
	}
	return out
}
