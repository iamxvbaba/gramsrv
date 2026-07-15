package rpc

import (
	"context"
	"crypto/sha256"
	"encoding/binary"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
)

func inlineMessageAccessHash(botID int64, msg domain.Message) int64 {
	var buf [32]byte
	binary.LittleEndian.PutUint64(buf[0:8], uint64(botID))
	binary.LittleEndian.PutUint64(buf[8:16], uint64(msg.OwnerUserID))
	binary.LittleEndian.PutUint64(buf[16:24], uint64(int64(msg.ID)))
	binary.LittleEndian.PutUint64(buf[24:32], uint64(msg.UID))
	sum := sha256.Sum256(buf[:])
	v := int64(binary.LittleEndian.Uint64(sum[:8]))
	if v == 0 {
		return 1
	}
	return v
}

func inlineChannelMessageAccessHash(botID int64, msg domain.ChannelMessage) int64 {
	var buf [40]byte
	binary.LittleEndian.PutUint64(buf[0:8], uint64(botID))
	binary.LittleEndian.PutUint64(buf[8:16], uint64(msg.ChannelID))
	binary.LittleEndian.PutUint64(buf[16:24], uint64(int64(msg.ID)))
	binary.LittleEndian.PutUint64(buf[24:32], uint64(msg.RandomID))
	binary.LittleEndian.PutUint64(buf[32:40], uint64(msg.SenderUserID))
	sum := sha256.Sum256(buf[:])
	v := int64(binary.LittleEndian.Uint64(sum[:8]))
	if v == 0 {
		return 1
	}
	return v
}

func (r *Router) inputInlineMessageIDForPrivateMessage(botID int64, msg domain.Message) tg.InputBotInlineMessageIDClass {
	if botID == 0 || msg.OwnerUserID == 0 || msg.ID <= 0 || msg.UID == 0 || msg.ViaBotID != botID || msg.Peer.Type != domain.PeerTypeUser {
		return nil
	}
	return &tg.InputBotInlineMessageID64{
		DCID:       r.cfg.DC,
		OwnerID:    msg.OwnerUserID,
		ID:         msg.ID,
		AccessHash: inlineMessageAccessHash(botID, msg),
	}
}

func (r *Router) inputInlineMessageIDForChannelMessage(botID int64, msg domain.ChannelMessage) tg.InputBotInlineMessageIDClass {
	if botID == 0 || msg.ChannelID == 0 || msg.ID <= 0 || msg.RandomID == 0 || msg.SenderUserID == 0 || msg.ViaBotID != botID {
		return nil
	}
	return &tg.InputBotInlineMessageID64{
		DCID:       r.cfg.DC,
		OwnerID:    msg.ChannelID,
		ID:         msg.ID,
		AccessHash: inlineChannelMessageAccessHash(botID, msg),
	}
}

func (r *Router) privateMessageFromInlineID(ctx context.Context, botID int64, id tg.InputBotInlineMessageIDClass) (domain.Message, bool, error) {
	if err := validateInputBotInlineMessageID(id); err != nil {
		return domain.Message{}, false, err
	}
	in, ok := id.(*tg.InputBotInlineMessageID64)
	if !ok {
		return domain.Message{}, false, nil
	}
	if in.DCID != 0 && in.DCID != r.cfg.DC {
		return domain.Message{}, false, nil
	}
	msg, found, err := r.lookupOwnerMessage(ctx, in.OwnerID, in.ID)
	if err != nil || !found {
		return domain.Message{}, false, err
	}
	if msg.Peer.Type != domain.PeerTypeUser || msg.ViaBotID != botID {
		return domain.Message{}, false, nil
	}
	if inlineMessageAccessHash(botID, msg) != in.AccessHash {
		return domain.Message{}, false, nil
	}
	return msg, true, nil
}

func (r *Router) channelMessageFromInlineID(ctx context.Context, botID int64, id tg.InputBotInlineMessageIDClass) (domain.Channel, domain.ChannelMessage, bool, error) {
	if err := validateInputBotInlineMessageID(id); err != nil {
		return domain.Channel{}, domain.ChannelMessage{}, false, err
	}
	in, ok := id.(*tg.InputBotInlineMessageID64)
	if !ok {
		return domain.Channel{}, domain.ChannelMessage{}, false, nil
	}
	if in.DCID != 0 && in.DCID != r.cfg.DC {
		return domain.Channel{}, domain.ChannelMessage{}, false, nil
	}
	if in.OwnerID <= 0 || r.deps.Channels == nil {
		return domain.Channel{}, domain.ChannelMessage{}, false, nil
	}
	channel, msg, found, err := r.deps.Channels.GetInlineBotMessage(ctx, botID, in.OwnerID, in.ID)
	if err != nil || !found {
		return domain.Channel{}, domain.ChannelMessage{}, false, err
	}
	if inlineChannelMessageAccessHash(botID, msg) != in.AccessHash {
		return domain.Channel{}, domain.ChannelMessage{}, false, nil
	}
	return channel, msg, true, nil
}

func (r *Router) privateInlineMessageIDFromSendUpdates(ctx context.Context, botID, userID int64, updates tg.UpdatesClass) tg.InputBotInlineMessageIDClass {
	box, ok := updates.(*tg.Updates)
	if !ok {
		return nil
	}
	for _, update := range box.Updates {
		newMessage, ok := update.(*tg.UpdateNewMessage)
		if !ok {
			continue
		}
		msg, ok := newMessage.Message.(*tg.Message)
		if !ok || msg.ID <= 0 {
			continue
		}
		domainMsg, found, err := r.lookupOwnerMessage(ctx, userID, msg.ID)
		if err != nil || !found {
			continue
		}
		if id := r.inputInlineMessageIDForPrivateMessage(botID, domainMsg); id != nil {
			return id
		}
	}
	return nil
}

func (r *Router) channelInlineMessageIDFromSendUpdates(ctx context.Context, botID, userID int64, updates tg.UpdatesClass) tg.InputBotInlineMessageIDClass {
	box, ok := updates.(*tg.Updates)
	if !ok || r.deps.Channels == nil {
		return nil
	}
	for _, update := range box.Updates {
		newMessage, ok := update.(*tg.UpdateNewChannelMessage)
		if !ok {
			continue
		}
		msg, ok := newMessage.Message.(*tg.Message)
		if !ok || msg.ID <= 0 {
			continue
		}
		peer, ok := msg.PeerID.(*tg.PeerChannel)
		if !ok || peer.ChannelID == 0 {
			continue
		}
		history, err := r.deps.Channels.GetMessages(ctx, userID, peer.ChannelID, []int{msg.ID})
		if err != nil || len(history.Messages) != 1 {
			continue
		}
		if id := r.inputInlineMessageIDForChannelMessage(botID, history.Messages[0]); id != nil {
			return id
		}
	}
	return nil
}

func (r *Router) pushInlineBotSendFeedback(ctx context.Context, userID int64, results domain.BotInlineResults, result domain.BotInlineResult, updates tg.UpdatesClass) {
	if results.BotUserID == 0 || result.ID == "" {
		return
	}
	update := &tg.UpdateBotInlineSend{
		UserID: userID,
		Query:  results.Query,
		ID:     result.ID,
	}
	if results.Geo != nil {
		update.SetGeo(tgGeoPoint(*results.Geo))
	}
	if result.ReplyMarkup != nil && !result.ReplyMarkup.IsZero() {
		if msgID := r.privateInlineMessageIDFromSendUpdates(ctx, results.BotUserID, userID, updates); msgID != nil {
			update.SetMsgID(msgID)
		} else if msgID := r.channelInlineMessageIDFromSendUpdates(ctx, results.BotUserID, userID, updates); msgID != nil {
			update.SetMsgID(msgID)
		}
	}
	r.pushUserMessage(ctx, results.BotUserID, "push bot inline send", &tg.Updates{
		Updates: []tg.UpdateClass{update},
		Date:    int(r.clock.Now().Unix()),
	})
}
