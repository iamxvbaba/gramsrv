package rpc

import (
	"context"
	"strings"
	"unicode/utf8"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
)

// tgFactCheck 把领域事实核查投影成 TL 构造器。
//
// need_check 置位时按协议只带 hash：country/text 由客户端用
// messages.getFactCheck 懒加载；已经有结论时直接带上国家与文本。
func tgFactCheck(check *domain.FactCheck) *tg.FactCheck {
	if check == nil || check.Hash == 0 {
		return nil
	}
	out := &tg.FactCheck{Hash: check.Hash}
	if check.NeedCheck {
		out.SetNeedCheck(true)
		return out
	}
	if check.Country != "" {
		out.SetCountry(check.Country)
	}
	out.SetText(tgTextWithEntities(check.Text, check.Entities))
	return out
}

// onMessagesEditFactCheck 处理 messages.editFactCheck：给一条消息写入事实核查结论。
func (r *Router) onMessagesEditFactCheck(ctx context.Context, req *tg.MessagesEditFactCheckRequest) (tg.UpdatesClass, error) {
	if req == nil {
		return nil, inputRequestInvalidErr()
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil || userID == 0 {
		return nil, internalErr()
	}
	peer, err := r.checkedDomainPeerFromInputPeer(ctx, userID, req.Peer)
	if err != nil {
		return nil, err
	}
	// 客户端只在广播频道提供事实核查入口；私有对话与群组没有这个概念。
	if peer.Type != domain.PeerTypeChannel {
		return nil, peerIDInvalidErr()
	}
	text := req.Text.Text
	if utf8.RuneCountInString(text) > domain.MaxFactCheckTextLength {
		return nil, messageTooLongErr()
	}
	if strings.TrimSpace(text) == "" {
		return nil, messageEmptyErr()
	}
	entities := domainMessageEntitiesForViewer(userID, req.Text.Entities)
	res, err := r.deps.Channels.EditFactCheck(ctx, userID, domain.EditFactCheckRequest{
		UserID:    userID,
		ChannelID: peer.ID,
		MessageID: req.MsgID,
		Text:      text,
		Entities:  entities,
		Date:      int(r.clock.Now().Unix()),
	})
	if err != nil {
		return nil, factCheckErr(err)
	}
	return r.factCheckUpdates(ctx, userID, res)
}

// onMessagesDeleteFactCheck 处理 messages.deleteFactCheck：撤掉事实核查标签。
func (r *Router) onMessagesDeleteFactCheck(ctx context.Context, req *tg.MessagesDeleteFactCheckRequest) (tg.UpdatesClass, error) {
	if req == nil {
		return nil, inputRequestInvalidErr()
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil || userID == 0 {
		return nil, internalErr()
	}
	peer, err := r.checkedDomainPeerFromInputPeer(ctx, userID, req.Peer)
	if err != nil {
		return nil, err
	}
	if peer.Type != domain.PeerTypeChannel {
		return nil, peerIDInvalidErr()
	}
	res, err := r.deps.Channels.EditFactCheck(ctx, userID, domain.EditFactCheckRequest{
		UserID:    userID,
		ChannelID: peer.ID,
		MessageID: req.MsgID,
		Text:      "",
		Date:      int(r.clock.Now().Unix()),
	})
	if err != nil {
		return nil, factCheckErr(err)
	}
	return r.factCheckUpdates(ctx, userID, res)
}

// onMessagesGetFactCheck 处理 messages.getFactCheck：批量读取事实核查标签。
func (r *Router) onMessagesGetFactCheck(ctx context.Context, req *tg.MessagesGetFactCheckRequest) ([]tg.FactCheck, error) {
	if req == nil || len(req.MsgID) == 0 {
		return nil, inputRequestInvalidErr()
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil || userID == 0 {
		return nil, internalErr()
	}
	peer, err := r.checkedDomainPeerFromInputPeer(ctx, userID, req.Peer)
	if err != nil {
		return nil, err
	}
	if peer.Type != domain.PeerTypeChannel {
		return nil, peerIDInvalidErr()
	}
	list, err := r.deps.Channels.GetFactChecks(ctx, userID, peer.ID, req.MsgID)
	if err != nil {
		return nil, factCheckErr(err)
	}
	out := make([]tg.FactCheck, len(list.FactChecks))
	for i, check := range list.FactChecks {
		if projected := tgFactCheck(&check); projected != nil {
			out[i] = *projected
		}
	}
	return out, nil
}

// factCheckUpdates 把事实核查写入产生的结果投影成 tg.Updates。
//
// v2: the store already committed the edit_message channel event together
// with its durable channel delivery signal, so other sessions and members get
// the update from Egress by channel PTS; only the Bot API edited_channel_post
// is enqueued here, exactly as messages.editMessage does for channels.
func (r *Router) factCheckUpdates(ctx context.Context, viewerUserID int64, res domain.EditFactCheckResult) (*tg.Updates, error) {
	edit := domain.EditChannelMessageResult{
		Channel: res.Channel, Message: res.Message, Event: res.Event, Recipients: res.Recipients,
	}
	updates := r.channelEditMessageUpdates(ctx, viewerUserID, edit)
	if err := r.enqueueBotAPIChannelEditMessageUpdate(ctx, viewerUserID, edit); err != nil {
		return nil, internalErr()
	}
	return updates, nil
}
