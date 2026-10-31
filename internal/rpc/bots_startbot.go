package rpc

import (
	"context"
	"unicode/utf8"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"

	"telesrv/internal/domain"
)

func startParamInvalidErr() error { return tgerr.New(400, "START_PARAM_INVALID") }

// onMessagesStartBot handles messages.startBot (the public bot?start deep link and the "start
// bot" entry point). Semantics: send a visible ordinary private-chat message of "/start" or
// "/start <param>" to the bot, going through the standard SendPrivateText double-box + outbox and
// returning real Updates (I7). The bot receives start_param through this. P3 only supports
// private-chat start; a group peer (adding the bot to a group) is deferred (P4, in-group bots).
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
	// P3 supports only private-chat start: the peer must resolve to this bot (the deep-link start
	// peer=bot). A group start (adding the bot to a group) belongs to P4 in-group bots and is
	// rejected here rather than silently rewriting the semantics.
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
