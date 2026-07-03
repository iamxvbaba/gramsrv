package rpc

import (
	"context"
	"time"

	"github.com/gotd/td/tg"
	"go.uber.org/zap"

	"telesrv/internal/domain"
)

// 本文件实现 app/bots 的 rpc 回调：token revoke 后的 session 失效闭环，
// 命令变更后的 updateBotCommands 在线推送、@Stickers 发布后的 updateStickerSets
// 在线提示，以及 @ChatBot 流式草稿 transient 推送。Router 创建后经
// botsService.SetRouterHooks / SetTextDraftPusher 装配（见 cmd/telesrv/main.go）。

// maxBotCommandsPushPeers 限制单次命令变更的推送扇出（bot 的最近 dialog peer 数）。
// 超出的离线/长尾用户靠 bot_info_version bump 在下次 getFullUser 时拿到新命令。
const maxBotCommandsPushPeers = 100

// RevokeBotSessions 撤销 bot 的全部已登录 session：删除全部 authorization 行并
// 强制断开在线连接（与 account.resetAuthorization 被踢闭环同款顺序）。
func (r *Router) RevokeBotSessions(ctx context.Context, botUserID int64) error {
	if r.deps.Auth == nil || botUserID == 0 {
		return nil
	}
	deleted, err := r.deps.Auth.ResetAuthorizations(ctx, botUserID, [8]byte{})
	if err != nil {
		return err
	}
	for _, a := range deleted {
		r.revokeAuthKeySessions(a.AuthKeyID)
		if err := r.clearAuthKeyState(ctx, a.AuthKeyID); err != nil {
			r.log.Warn("revoke bot sessions: clear auth key state",
				zap.Int64("bot_user_id", botUserID), zap.Error(err))
		}
	}
	if len(deleted) > 0 {
		r.log.Info("revoked bot sessions", zap.Int64("bot_user_id", botUserID), zap.Int("count", len(deleted)))
	}
	return nil
}

// PushBotCommandsChanged 给「与该 bot 有私聊 dialog 且在线」的用户推
// updateBotCommands（peer = 该 bot 的 user peer，对齐 TDesktop/DrKLO 消费语义）。
// updateBotCommands 无 pts/qts，不进 getDifference——离线用户由随写库一起完成的
// bot_info_version bump 兜底（下次 getFullUser 重拉命令）。
//
// fire-and-forget：在独立 goroutine 内执行（脱离已返回的 setBotCommands RPC ctx），
// 否则 GetDialogs + 最多 maxBotCommandsPushPeers 次 best-effort 推送会把 RPC 响应
// 拖到拥塞超时之和、并跨用户占住 BotFather 条带锁。推送是纯通知，丢失靠 version
// bump 兜底，不需保证投达。扇出有界：只取 bot dialog 列表前 maxBotCommandsPushPeers
// 个 user peer，再按在线快照过滤；超界部分走版本兜底（有意取舍）。
func (r *Router) PushBotCommandsChanged(ctx context.Context, botUserID int64, commands []domain.BotCommand) {
	if r.deps.Dialogs == nil || r.deps.Sessions == nil || botUserID == 0 {
		return
	}
	// 拷贝命令切片：调用方（service）可能复用底层数组。
	cmds := append([]domain.BotCommand(nil), commands...)
	go r.pushBotCommandsChanged(context.WithoutCancel(ctx), botUserID, cmds)
}

// PushStickerSetsChanged 给单个用户在线 session 推 updateStickerSets。该 update 无
// pts，不进 getDifference；权威安装态已写 user_sticker_sets，离线端下次
// messages.getAllStickers/messages.getEmojiStickers 会重建。
func (r *Router) PushStickerSetsChanged(ctx context.Context, userID int64, kind domain.StickerSetKind) {
	if userID == 0 {
		return
	}
	r.invalidateStickerCatalog(kind)
	go func() {
		defer func() {
			if rec := recover(); rec != nil {
				r.log.Error("push sticker sets panicked", zap.Int64("user_id", userID), zap.Any("panic", rec))
			}
		}()
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		r.pushStickerSetsUpdate(ctx, userID, kind)
	}()
}

// PushBotTextDraft 推送内置 service bot 的流式文本草稿。草稿是 TDesktop 专用的
// transient typing action，不写 message/dialog/pts/outbox；最终可恢复事实仍由随后
// 入库的普通 bot message 承担。
func (r *Router) PushBotTextDraft(ctx context.Context, botUserID, userID, randomID int64, text string) {
	if botUserID == 0 || userID == 0 || randomID == 0 || text == "" {
		return
	}
	r.pushUserMessageTransient(context.WithoutCancel(ctx), userID, "push bot text draft", &tg.UpdateShort{
		Update: &tg.UpdateUserTyping{
			UserID: botUserID,
			Action: &tg.SendMessageTextDraftAction{
				RandomID: randomID,
				Text:     tg.TextWithEntities{Text: text},
			},
		},
		Date: int(r.clock.Now().Unix()),
	})
}

func (r *Router) pushBotCommandsChanged(ctx context.Context, botUserID int64, commands []domain.BotCommand) {
	defer func() {
		if rec := recover(); rec != nil {
			r.log.Error("push bot commands panicked", zap.Int64("bot_user_id", botUserID), zap.Any("panic", rec))
		}
	}()
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	list, err := r.deps.Dialogs.GetDialogs(ctx, botUserID, domain.DialogFilter{Limit: maxBotCommandsPushPeers})
	if err != nil {
		r.log.Warn("push bot commands: list bot dialogs", zap.Int64("bot_user_id", botUserID), zap.Error(err))
		return
	}
	candidates := make([]int64, 0, len(list.Dialogs))
	for _, dialog := range list.Dialogs {
		if dialog.Peer.Type == domain.PeerTypeUser && dialog.Peer.ID != 0 && dialog.Peer.ID != botUserID {
			candidates = append(candidates, dialog.Peer.ID)
		}
	}
	if len(candidates) == 0 {
		return
	}
	if provider, ok := r.deps.Sessions.(OnlineUserProvider); ok {
		candidates = provider.OnlineUserIDsForCandidates(candidates, maxBotCommandsPushPeers)
	}
	if len(candidates) == 0 {
		return
	}
	update := &tg.Updates{
		Updates: []tg.UpdateClass{&tg.UpdateBotCommands{
			Peer:     &tg.PeerUser{UserID: botUserID},
			BotID:    botUserID,
			Commands: tgBotCommands(commands),
		}},
		Date: int(r.clock.Now().Unix()),
	}
	for _, userID := range candidates {
		r.pushUserMessage(ctx, userID, "push bot commands", update)
	}
}
