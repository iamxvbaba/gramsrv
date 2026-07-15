package rpc

import (
	"context"
	"unicode/utf8"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"

	"telesrv/internal/domain"
)

func startParamInvalidErr() error { return tgerr.New(400, "START_PARAM_INVALID") }

// onMessagesStartBot 处理 messages.startBot（公开 bot?start 深链与「启动 bot」
// 入口）。语义：向 bot 发送一条可见的 "/start" 或 "/start <param>" 普通私聊消息，走标准
// SendPrivateText 双盒+outbox，返回真实 Updates（I7）。bot 经此收到 start_param。
// P3 仅私聊启动；peer 为群（加 bot 进群）后移（P4，群内 bot）。
func (r *Router) onMessagesStartBot(ctx context.Context, req *tg.MessagesStartBotRequest) (tg.UpdatesClass, error) {
	if req.RandomID == 0 {
		return nil, randomIDEmptyErr()
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	if userID == 0 {
		return nil, peerIDInvalidErr()
	}
	if utf8.RuneCountInString(req.StartParam) > domain.MaxStartParamLen {
		return nil, startParamInvalidErr()
	}
	bot, found, err := r.userFromInput(ctx, userID, req.Bot)
	if err != nil {
		return nil, botInvalidErr()
	}
	if !found || !bot.Bot {
		return nil, botInvalidErr()
	}
	// P3 仅私聊启动：peer 必须解析为该 bot（深链 start 的 peer=bot）。群启动（加 bot
	// 进群）属 P4 群内 bot，此处拒绝而非静默改写语义。
	peer, err := r.checkedDomainPeerFromInputPeer(ctx, userID, req.Peer)
	if err != nil {
		return nil, err
	}
	if peer.Type != domain.PeerTypeUser || peer.ID != bot.ID {
		return nil, peerIDInvalidErr()
	}
	if err := r.checkSendRateLimit(ctx, userID, 1); err != nil {
		return nil, err
	}
	body := "/start"
	if req.StartParam != "" {
		body += " " + req.StartParam
	}
	updates, _, err := r.sendOutgoing(ctx, userID, peer, outgoingSend{
		randomID: req.RandomID,
		message:  body,
	})
	if err != nil {
		return nil, err
	}
	return updates, nil
}
