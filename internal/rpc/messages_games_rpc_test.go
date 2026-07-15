package rpc

import (
	"context"
	"testing"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
)

func TestMessagesGetEmojiGameInfoUnavailable(t *testing.T) {
	ctx := context.Background()
	f := newInlineBotRPCTestFixture(t)

	got, err := f.router.onMessagesGetEmojiGameInfo(WithUserID(ctx, f.owner.ID))
	if err != nil {
		t.Fatalf("get emoji game info: %v", err)
	}
	if _, ok := got.(*tg.MessagesEmojiGameUnavailable); !ok {
		t.Fatalf("emoji game info = %T, want *tg.MessagesEmojiGameUnavailable", got)
	}
}

func TestMessagesGameScoreRPCsRequireBot(t *testing.T) {
	ctx := context.Background()
	f := newInlineBotRPCTestFixture(t)
	userCtx := WithUserID(ctx, f.owner.ID)
	inlineID := validInputBotInlineMessageID(f.owner.ID)

	if _, err := f.router.onMessagesGetGameHighScores(userCtx, &tg.MessagesGetGameHighScoresRequest{
		Peer:   inputPeerUser(f.peer),
		ID:     1,
		UserID: inputUser(f.owner),
	}); !tgerr.Is(err, "USER_BOT_REQUIRED") {
		t.Fatalf("get game high scores err = %v, want USER_BOT_REQUIRED", err)
	}
	if _, err := f.router.onMessagesGetInlineGameHighScores(userCtx, &tg.MessagesGetInlineGameHighScoresRequest{
		ID:     inlineID,
		UserID: inputUser(f.owner),
	}); !tgerr.Is(err, "USER_BOT_REQUIRED") {
		t.Fatalf("get inline game high scores err = %v, want USER_BOT_REQUIRED", err)
	}
	if _, err := f.router.onMessagesSetGameScore(userCtx, &tg.MessagesSetGameScoreRequest{
		Peer:   inputPeerUser(f.peer),
		ID:     1,
		UserID: inputUser(f.owner),
		Score:  10,
	}); !tgerr.Is(err, "USER_BOT_REQUIRED") {
		t.Fatalf("set game score err = %v, want USER_BOT_REQUIRED", err)
	}
	if ok, err := f.router.onMessagesSetInlineGameScore(userCtx, &tg.MessagesSetInlineGameScoreRequest{
		ID:     inlineID,
		UserID: inputUser(f.owner),
		Score:  10,
	}); ok || !tgerr.Is(err, "USER_BOT_REQUIRED") {
		t.Fatalf("set inline game score = %v,%v, want false,USER_BOT_REQUIRED", ok, err)
	}
}

func TestMessagesGameScoreRPCsRejectWithoutGameModel(t *testing.T) {
	ctx := context.Background()
	f := newInlineBotRPCTestFixture(t)
	botCtx := WithUserID(ctx, f.bot.ID)

	if _, err := f.router.onMessagesGetGameHighScores(botCtx, &tg.MessagesGetGameHighScoresRequest{
		Peer:   inputPeerUser(f.peer),
		ID:     1,
		UserID: inputUser(f.owner),
	}); !tgerr.Is(err, "MESSAGE_ID_INVALID") {
		t.Fatalf("get game high scores err = %v, want MESSAGE_ID_INVALID", err)
	}
	if _, err := f.router.onMessagesSetGameScore(botCtx, &tg.MessagesSetGameScoreRequest{
		Peer:   inputPeerUser(f.peer),
		ID:     1,
		UserID: inputUser(f.owner),
		Score:  10,
	}); !tgerr.Is(err, "MESSAGE_ID_INVALID") {
		t.Fatalf("set game score err = %v, want MESSAGE_ID_INVALID", err)
	}
	if _, err := f.router.onMessagesSetGameScore(botCtx, &tg.MessagesSetGameScoreRequest{
		Peer:   inputPeerUser(f.peer),
		ID:     1,
		UserID: inputUser(f.owner),
		Score:  -1,
	}); !tgerr.Is(err, "SCORE_INVALID") {
		t.Fatalf("set negative game score err = %v, want SCORE_INVALID", err)
	}
}

func TestMessagesInlineGameScoreRPCsRejectWithoutGameModel(t *testing.T) {
	ctx := context.Background()
	f := newInlineBotRPCTestFixture(t)
	botCtx := WithUserID(ctx, f.bot.ID)
	inlineID := validInputBotInlineMessageID(f.owner.ID)

	if _, err := f.router.onMessagesGetInlineGameHighScores(botCtx, &tg.MessagesGetInlineGameHighScoresRequest{
		ID:     inlineID,
		UserID: inputUser(f.owner),
	}); !tgerr.Is(err, "MESSAGE_ID_INVALID") {
		t.Fatalf("get inline game high scores err = %v, want MESSAGE_ID_INVALID", err)
	}
	if ok, err := f.router.onMessagesSetInlineGameScore(botCtx, &tg.MessagesSetInlineGameScoreRequest{
		ID:     inlineID,
		UserID: inputUser(f.owner),
		Score:  10,
	}); ok || !tgerr.Is(err, "MESSAGE_ID_INVALID") {
		t.Fatalf("set inline game score = %v,%v, want false,MESSAGE_ID_INVALID", ok, err)
	}
	if ok, err := f.router.onMessagesSetInlineGameScore(botCtx, &tg.MessagesSetInlineGameScoreRequest{
		ID:     inlineID,
		UserID: inputUser(f.owner),
		Score:  -1,
	}); ok || !tgerr.Is(err, "SCORE_INVALID") {
		t.Fatalf("set negative inline game score = %v,%v, want false,SCORE_INVALID", ok, err)
	}
	if _, err := f.router.onMessagesGetInlineGameHighScores(botCtx, &tg.MessagesGetInlineGameHighScoresRequest{
		UserID: inputUser(f.owner),
	}); !tgerr.Is(err, "MESSAGE_ID_INVALID") {
		t.Fatalf("get inline game high scores nil id err = %v, want MESSAGE_ID_INVALID", err)
	}
}

func validInputBotInlineMessageID(ownerID int64) tg.InputBotInlineMessageIDClass {
	return &tg.InputBotInlineMessageID64{
		DCID:       2,
		OwnerID:    ownerID,
		ID:         1,
		AccessHash: 12345,
	}
}
