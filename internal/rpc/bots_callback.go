package rpc

import (
	"context"
	"time"
	"unicode/utf8"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"

	"telesrv/internal/domain"
)

// botCallbackTimeout 是 getBotCallbackAnswer 的挂起上限：bot 未在窗口内
// setBotCallbackAnswer 即回 BOT_RESPONSE_TIMEOUT（不快速失败，给 bot 上线追答的窗口）。
const botCallbackTimeout = 25 * time.Second

func botResponseTimeoutErr() error { return tgerr.New(502, "BOT_RESPONSE_TIMEOUT") }
func dataInvalidErr() error        { return tgerr.New(400, "DATA_INVALID") }

// onMessagesGetBotCallbackAnswer 处理 inline callback 按钮点击：把 updateBotCallbackQuery
// 推给 bot，挂起等待 bot 的 setBotCallbackAnswer，或超时回 BOT_RESPONSE_TIMEOUT。
func (r *Router) onMessagesGetBotCallbackAnswer(ctx context.Context, req *tg.MessagesGetBotCallbackAnswerRequest) (*tg.MessagesBotCallbackAnswer, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	if userID == 0 {
		return nil, peerIDInvalidErr()
	}
	peer, err := r.checkedDomainPeerFromInputPeer(ctx, userID, req.Peer)
	if err != nil {
		return nil, err
	}
	// callback 按钮只存在于 bot 的私聊消息。peer 必须是 bot 用户。
	if peer.Type != domain.PeerTypeUser || !r.userIsBot(ctx, peer.ID) {
		return nil, dataInvalidErr()
	}
	botUserID := peer.ID
	// game 按钮（getBotCallbackAnswer.game）P3 不支持：返回空答案（客户端不弹任何东西），
	// 不挂起、不推送（避免给 bot 投递无法处理的 game query）。
	if req.Game {
		return &tg.MessagesBotCallbackAnswer{}, nil
	}
	data, hasData := req.GetData()
	if !hasData {
		return nil, dataInvalidErr()
	}
	// 校验目标消息存在于请求者自己的盒、且对端正是该 bot。
	if req.MsgID <= 0 || req.MsgID > domain.MaxMessageBoxID {
		return nil, messageIDInvalidErr()
	}
	msg, ok, err := r.lookupOwnerMessage(ctx, userID, req.MsgID)
	if err != nil {
		return nil, internalErr()
	}
	if !ok || msg.Peer != peer {
		return nil, messageIDInvalidErr()
	}

	queryID, pending := r.callbacks.register(botUserID, userID)
	defer r.callbacks.deregister(queryID)

	// updateBotCallbackQuery 是 ephemeral（无 pts/qts，不进 getDifference）：仅在线推给
	// bot；bot 离线则投递 0，但仍走超时窗口（I5，给 bot 上线追答机会）。
	// MsgID 透传请求者侧的 box id（P3 不做 bot 侧 box id 翻译——bot 侧消息编辑后移，记 todo）。
	update := &tg.UpdateBotCallbackQuery{
		QueryID:      queryID,
		UserID:       userID,
		Peer:         &tg.PeerUser{UserID: userID},
		MsgID:        req.MsgID,
		ChatInstance: chatInstanceFor(botUserID, userID),
	}
	update.SetData(data)
	r.pushUserMessage(ctx, botUserID, "push bot callback query", &tg.Updates{
		Updates: []tg.UpdateClass{update},
		Date:    int(r.clock.Now().Unix()),
	})

	waitCtx, cancel := context.WithTimeout(ctx, botCallbackTimeout)
	defer cancel()
	select {
	case ans := <-pending.ch:
		return tgBotCallbackAnswer(ans), nil
	case <-waitCtx.Done():
		return nil, botResponseTimeoutErr()
	}
}

// onMessagesSetBotCallbackAnswer 是 bot 对一次 callback query 的应答：解挂等待中的
// getBotCallbackAnswer。仅属主 bot 可解挂（callerBotID==pending.botUserID，I6）。
func (r *Router) onMessagesSetBotCallbackAnswer(ctx context.Context, req *tg.MessagesSetBotCallbackAnswerRequest) (bool, error) {
	botID, err := r.callerBotID(ctx)
	if err != nil {
		return false, err
	}
	ans := domain.BotCallbackAnswer{Alert: req.Alert, CacheTime: req.CacheTime}
	if msg, ok := req.GetMessage(); ok {
		if utf8.RuneCountInString(msg) > domain.MaxBotCallbackAnswerLen {
			return false, messageTooLongErr()
		}
		ans.Message = msg
	}
	if url, ok := req.GetURL(); ok {
		ans.URL = url
	}
	// resolve 返回是否投递成功；未注册/超时/非属主一律 false。对 bot 而言答案是否
	// 被等待者接收无关紧要（官方恒返回 true），但非属主必须拒绝投递（防钓鱼弹窗）。
	r.callbacks.resolve(botID, req.QueryID, ans)
	return true, nil
}

func tgBotCallbackAnswer(ans domain.BotCallbackAnswer) *tg.MessagesBotCallbackAnswer {
	out := &tg.MessagesBotCallbackAnswer{Alert: ans.Alert, CacheTime: ans.CacheTime}
	if ans.Message != "" {
		out.SetMessage(ans.Message)
	}
	if ans.URL != "" {
		out.SetURL(ans.URL)
		out.HasURL = true
	}
	return out
}
