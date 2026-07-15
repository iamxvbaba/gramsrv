package rpc

import (
	"context"

	"github.com/iamxvbaba/td/tg"
)

func (r *Router) onMessagesGetEmojiGameInfo(ctx context.Context) (tg.MessagesEmojiGameInfoClass, error) {
	if _, _, err := r.currentUserID(ctx); err != nil {
		return nil, internalErr()
	}
	return &tg.MessagesEmojiGameUnavailable{}, nil
}

func (r *Router) onMessagesGetGameHighScores(ctx context.Context, req *tg.MessagesGetGameHighScoresRequest) (*tg.MessagesHighScores, error) {
	botID, err := r.callerBotID(ctx)
	if err != nil {
		return nil, err
	}
	if req.ID <= 0 {
		return nil, messageIDInvalidErr()
	}
	if _, err := r.checkedDomainPeerFromInputPeer(ctx, botID, req.Peer); err != nil {
		return nil, err
	}
	if err := r.validateInputUser(ctx, req.UserID); err != nil {
		return nil, err
	}
	return nil, messageIDInvalidErr()
}

func (r *Router) onMessagesGetInlineGameHighScores(ctx context.Context, req *tg.MessagesGetInlineGameHighScoresRequest) (*tg.MessagesHighScores, error) {
	if _, err := r.callerBotID(ctx); err != nil {
		return nil, err
	}
	if err := validateInputBotInlineMessageID(req.ID); err != nil {
		return nil, err
	}
	if err := r.validateInputUser(ctx, req.UserID); err != nil {
		return nil, err
	}
	return nil, messageIDInvalidErr()
}

func (r *Router) onMessagesSetGameScore(ctx context.Context, req *tg.MessagesSetGameScoreRequest) (tg.UpdatesClass, error) {
	botID, err := r.callerBotID(ctx)
	if err != nil {
		return nil, err
	}
	if req.ID <= 0 {
		return nil, messageIDInvalidErr()
	}
	if req.Score < 0 {
		return nil, scoreInvalidErr()
	}
	if _, err := r.checkedDomainPeerFromInputPeer(ctx, botID, req.Peer); err != nil {
		return nil, err
	}
	if err := r.validateInputUser(ctx, req.UserID); err != nil {
		return nil, err
	}
	return nil, messageIDInvalidErr()
}

func (r *Router) onMessagesSetInlineGameScore(ctx context.Context, req *tg.MessagesSetInlineGameScoreRequest) (bool, error) {
	if _, err := r.callerBotID(ctx); err != nil {
		return false, err
	}
	if err := validateInputBotInlineMessageID(req.ID); err != nil {
		return false, err
	}
	if req.Score < 0 {
		return false, scoreInvalidErr()
	}
	if err := r.validateInputUser(ctx, req.UserID); err != nil {
		return false, err
	}
	return false, messageIDInvalidErr()
}

func validateInputBotInlineMessageID(id tg.InputBotInlineMessageIDClass) error {
	switch v := id.(type) {
	case *tg.InputBotInlineMessageID:
		if v.ID == 0 || v.AccessHash == 0 {
			return messageIDInvalidErr()
		}
	case *tg.InputBotInlineMessageID64:
		if v.OwnerID == 0 || v.ID <= 0 || v.AccessHash == 0 {
			return messageIDInvalidErr()
		}
	default:
		return messageIDInvalidErr()
	}
	return nil
}
