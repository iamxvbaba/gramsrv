package bots

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"go.uber.org/zap"

	adminapp "telesrv/internal/admin"
	"telesrv/internal/domain"
)

const gramsrvOperatorBotUserID int64 = 1250000023

type gramsrvAccountFreezeManager interface {
	AccountFreeze(context.Context, int64) (domain.AccountFreeze, bool, error)
	SetAccountFrozen(context.Context, adminapp.SetAccountFrozenRequest) (adminapp.CommandResult, error)
}

// WithGramsrvOperatorBot enables the in-process @gramsrv dialog for exact user IDs.
func WithGramsrvOperatorBot(chatIDs []int64, freezes gramsrvAccountFreezeManager) Option {
	return func(s *Service) {
		if freezes == nil {
			return
		}
		s.gramsrvFreezes = freezes
		s.gramsrvOperators = make(map[int64]bool, len(chatIDs))
		for _, id := range chatIDs {
			if id > 0 {
				s.gramsrvOperators[id] = true
			}
		}
	}
}

func (s *Service) respondAsGramsrvOperator(userID int64, body string) {
	if !s.gramsrvOperators[userID] {
		return
	}
	mu := s.serviceBotReplyLock(gramsrvOperatorBotUserID, userID)
	mu.Lock()
	defer mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fields := strings.Fields(strings.TrimSpace(body))
	if len(fields) == 0 {
		s.sendServiceBotReply(ctx, gramsrvOperatorBotUserID, userID, botReply{Text: gramsrvOperatorHelp()})
		return
	}
	command := strings.ToLower(strings.SplitN(strings.TrimPrefix(fields[0], "/"), "@", 2)[0])
	if command == "start" || command == "help" {
		s.sendServiceBotReply(ctx, gramsrvOperatorBotUserID, userID, botReply{Text: gramsrvOperatorHelp()})
		return
	}
	if command != "account" && command != "freeze" && command != "unfreeze" || len(fields) != 2 {
		s.sendServiceBotReply(ctx, gramsrvOperatorBotUserID, userID, botReply{Text: "Используй /account @username, /freeze @username или /unfreeze @username."})
		return
	}
	target, found, err := s.gramsrvOperatorTarget(ctx, fields[1])
	if err != nil {
		s.log.Error("gramsrv operator: lookup target", zap.Error(err))
		return
	}
	if !found {
		s.sendServiceBotReply(ctx, gramsrvOperatorBotUserID, userID, botReply{Text: "Аккаунт не найден или недоступен для этой операции."})
		return
	}
	if target.ID == userID {
		s.sendServiceBotReply(ctx, gramsrvOperatorBotUserID, userID, botReply{Text: "Свой аккаунт через бота заморозить нельзя."})
		return
	}
	reply, err := s.gramsrvOperatorAccountReply(ctx, userID, target)
	if err != nil {
		s.log.Error("gramsrv operator: account status", zap.Int64("target_user_id", target.ID), zap.Error(err))
		s.sendServiceBotReply(ctx, gramsrvOperatorBotUserID, userID, botReply{Text: "Не удалось получить статус аккаунта."})
		return
	}
	s.sendServiceBotReply(ctx, gramsrvOperatorBotUserID, userID, reply)
}

func gramsrvOperatorHelp() string {
	return "Управление заморозкой аккаунтов Gramsrv.\n/account @username — статус и кнопка действия\n/freeze @username — открыть действие\n/unfreeze @username — открыть действие"
}

func (s *Service) gramsrvOperatorTarget(ctx context.Context, input string) (domain.User, bool, error) {
	if s.users == nil {
		return domain.User{}, false, nil
	}
	name := strings.TrimPrefix(strings.TrimSpace(input), "@")
	var target domain.User
	var found bool
	var err error
	if id, parseErr := strconv.ParseInt(name, 10, 64); parseErr == nil && id > 0 {
		target, found, err = s.users.ByID(ctx, id)
	} else if name != "" {
		target, found, err = s.users.ByUsername(ctx, name)
	}
	if !found || err != nil || target.ID <= 0 || target.Bot || domain.IsSystemUserID(target.ID) {
		return domain.User{}, false, err
	}
	return target, true, nil
}

func (s *Service) gramsrvOperatorAccountReply(ctx context.Context, userID int64, target domain.User) (botReply, error) {
	freeze, found, err := s.gramsrvFreezes.AccountFreeze(ctx, target.ID)
	if err != nil {
		return botReply{}, err
	}
	frozen := found && freeze.Frozen
	token := s.verifyOptionToken()
	state := domain.BotChatState{BotUserID: gramsrvOperatorBotUserID, UserID: userID,
		Command: "gramsrv", Step: "confirm", Draft: map[string]string{
			"target": strconv.FormatInt(target.ID, 10),
			"name":   target.Username,
			"frozen": strconv.FormatBool(frozen),
			"token":  token,
		}}
	if err := s.bots.UpsertBotChatState(ctx, state); err != nil {
		return botReply{}, err
	}
	status, action := "активен", "🔒 Заморозить"
	if frozen {
		status, action = "заморожен", "🔓 Разморозить"
	}
	label := fmt.Sprintf("@%s", target.Username)
	if target.Username == "" {
		label = strconv.FormatInt(target.ID, 10)
	}
	return botReply{Text: fmt.Sprintf("%s (ID %d): %s.", label, target.ID, status),
		ReplyMarkup: &domain.MessageReplyMarkup{Type: domain.MessageReplyMarkupInline,
			Inline: [][]domain.MarkupButton{{{Type: domain.MarkupButtonCallback, Text: action, Data: []byte("gs:" + token)}}}}}, nil
}

func (s *Service) onGramsrvOperatorCallback(ctx context.Context, query domain.BotCallbackQuery) (domain.BotCallbackAnswer, bool, error) {
	if !s.gramsrvOperators[query.UserID] {
		return domain.BotCallbackAnswer{Alert: true, Message: "Доступ закрыт."}, true, nil
	}
	mu := s.serviceBotReplyLock(gramsrvOperatorBotUserID, query.UserID)
	mu.Lock()
	defer mu.Unlock()
	state, found, err := s.bots.GetBotChatState(ctx, gramsrvOperatorBotUserID, query.UserID)
	if err != nil {
		return domain.BotCallbackAnswer{}, true, err
	}
	if !found || state.Command != "gramsrv" || state.Step != "confirm" ||
		string(query.Data) != "gs:"+state.Draft["token"] || state.Draft["token"] == "" {
		return domain.BotCallbackAnswer{Alert: true, Message: "Кнопка устарела. Открой /account снова."}, true, nil
	}
	targetID, err := strconv.ParseInt(state.Draft["target"], 10, 64)
	if err != nil || targetID <= 0 || targetID == query.UserID {
		return domain.BotCallbackAnswer{Alert: true, Message: "Некорректная цель."}, true, nil
	}
	target, ok, err := s.gramsrvOperatorTarget(ctx, strconv.FormatInt(targetID, 10))
	if err != nil || !ok {
		return domain.BotCallbackAnswer{Alert: true, Message: "Аккаунт недоступен."}, true, err
	}
	current, exists, err := s.gramsrvFreezes.AccountFreeze(ctx, targetID)
	if err != nil {
		return domain.BotCallbackAnswer{}, true, err
	}
	expected := state.Draft["frozen"] == "true"
	if (exists && current.Frozen) != expected {
		return domain.BotCallbackAnswer{Alert: true, Message: "Статус уже изменился. Открой /account снова."}, true, nil
	}
	next := !expected
	request := adminapp.SetAccountFrozenRequest{CommandMeta: adminapp.CommandMeta{
		CommandID: fmt.Sprintf("gramsrv-bot:%d:%d:%s", query.UserID, query.ID, state.Draft["token"]),
		Actor:     fmt.Sprintf("gramsrv-bot:%d", query.UserID),
		Reason:    "operator bot action",
	}, UserID: targetID, Frozen: next}
	if next {
		request.Until = time.Unix(math.MaxInt32, 0).UTC()
		request.AppealURL = strings.TrimRight(s.publicBaseURL, "/") + "/"
		if request.AppealURL == "/" {
			request.AppealURL = "https://telesrv.net/"
		}
	}
	result, err := s.gramsrvFreezes.SetAccountFrozen(ctx, request)
	if err != nil {
		s.log.Error("gramsrv operator: set account freeze", zap.Int64("target_user_id", targetID), zap.Error(err))
		return domain.BotCallbackAnswer{Alert: true, Message: "Не удалось изменить статус аккаунта."}, true, nil
	}
	state.Step = "done"
	if err := s.bots.UpsertBotChatState(ctx, state); err != nil {
		s.log.Error("gramsrv operator: save callback state", zap.Error(err))
	}
	if !result.AlreadyExecuted {
		s.gramsrvOperatorNotify(ctx, query.UserID, target, next)
	}
	reply, err := s.gramsrvOperatorAccountReply(ctx, query.UserID, target)
	if err == nil {
		s.sendServiceBotReply(ctx, gramsrvOperatorBotUserID, query.UserID, reply)
	}
	return domain.BotCallbackAnswer{Message: "Готово."}, true, nil
}

func (s *Service) gramsrvOperatorNotify(ctx context.Context, actorID int64, target domain.User, frozen bool) {
	actor := strconv.FormatInt(actorID, 10)
	if user, found, err := s.users.ByID(ctx, actorID); err == nil && found && user.Username != "" {
		actor = "@" + user.Username
	}
	name := fmt.Sprintf("@%s", target.Username)
	if target.Username == "" {
		name = strconv.FormatInt(target.ID, 10)
	}
	action := "разморозил"
	if frozen {
		action = "заморозил"
	}
	text := fmt.Sprintf("%s %s аккаунт %s (ID %d).", actor, action, name, target.ID)
	sendCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	for id := range s.gramsrvOperators {
		s.sendServiceBotReply(sendCtx, gramsrvOperatorBotUserID, id, botReply{Text: text})
	}
}
