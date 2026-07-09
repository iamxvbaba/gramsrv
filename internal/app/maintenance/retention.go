package maintenance

import (
	"context"
	"time"

	"go.uber.org/zap"
)

// DispatchOutboxRetentionStore 清理彻底失败（已放弃重试）的 outbox 死任务。
type DispatchOutboxRetentionStore interface {
	DeleteFailed(ctx context.Context, olderThan time.Duration, limit int) (int, error)
}

// TempAuthKeyRetentionStore 回收过期的 PFS temp auth key 绑定。
type TempAuthKeyRetentionStore interface {
	DeleteExpired(ctx context.Context, expiredBefore int64, limit int) (int, error)
}

// BotAPIUpdateRetentionStore 回收 Bot API getUpdates 投递队列的死行（性能审计 H1）：
// 已确认且超过宽限期的行 + 按消息 date 超过保留期的行（官方 Bot API updates 最多保留 24h）。
type BotAPIUpdateRetentionStore interface {
	DeleteDeliveredOrExpired(ctx context.Context, confirmedGrace, maxAge time.Duration, limit int) (int, error)
}

// botAPIConfirmedGrace 是已确认 Bot API update 行的删除宽限：确认水位之下的行不会再被
// getUpdates 读取（fromID 恒 > confirmed），宽限仅防御 offset 回拨调试；回收目标是清堆积。
const botAPIConfirmedGrace = 15 * time.Minute

// tempAuthKeyExpiryGrace 是 temp key 过期后的回收宽限：ResolveAuthKey 对
// 「已过期但 perm 已授权」的绑定是容忍的，立即删除会突然断掉这批宽限中的
// 连接；回收目标是清堆积，晚一天无妨。
const tempAuthKeyExpiryGrace = 24 * time.Hour

// RetentionWorker 周期性回收存储中的死数据。
//
// 注意：本 worker 刻意不清理 user_update_events —— pts log 永久保留。原因：TDesktop 不支持
// 账号级 updates.differenceTooLong（api_updates.cpp 收到该响应只打一行日志，且漏掉
// setRequesting(false)，会永久锁死整个 update 引擎），服务端因此无法让"落后超过保留期"的
// 客户端整库重置；一旦裁剪 events，落后客户端的 getDifference 会拿到不完整的事件链而静默
// 丢消息。详见 docs/performance-audit.md 与 docs/compatibility-matrix.md。user_update_events
// 长期膨胀作为已知 todo。
type RetentionWorker struct {
	outbox          DispatchOutboxRetentionStore
	tempKeys        TempAuthKeyRetentionStore  // 可为 nil（不回收 temp key 绑定）
	botAPIUpdates   BotAPIUpdateRetentionStore // 可为 nil（不回收 Bot API 队列）
	logger          *zap.Logger
	retention       time.Duration
	botAPIRetention time.Duration
	interval        time.Duration
	batch           int
}

func NewRetentionWorker(outbox DispatchOutboxRetentionStore, tempKeys TempAuthKeyRetentionStore, logger *zap.Logger, retention, interval time.Duration, batch int) *RetentionWorker {
	if logger == nil {
		logger = zap.NewNop()
	}
	if retention <= 0 {
		retention = 168 * time.Hour
	}
	if interval <= 0 {
		interval = time.Hour
	}
	if batch <= 0 {
		batch = 10000
	}
	return &RetentionWorker{
		outbox:    outbox,
		tempKeys:  tempKeys,
		logger:    logger,
		retention: retention,
		interval:  interval,
		batch:     batch,
	}
}

// WithBotAPIUpdateRetention 启用 bot_api_updates 队列回收；retention <=0 时用官方语义默认 24h。
func (w *RetentionWorker) WithBotAPIUpdateRetention(store BotAPIUpdateRetentionStore, retention time.Duration) *RetentionWorker {
	if retention <= 0 {
		retention = 24 * time.Hour
	}
	w.botAPIUpdates = store
	w.botAPIRetention = retention
	return w
}

func (w *RetentionWorker) Run(ctx context.Context) {
	w.runOnce(ctx)
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.runOnce(ctx)
		}
	}
}

func (w *RetentionWorker) runOnce(ctx context.Context) {
	outboxDeleted, err := w.outbox.DeleteFailed(ctx, w.retention, w.batch)
	if err != nil {
		w.logger.Warn("清理 failed dispatch_outbox 失败", zap.Error(err))
	} else if outboxDeleted > 0 {
		w.logger.Info("清理 failed dispatch_outbox 完成", zap.Int("deleted", outboxDeleted))
	}
	if w.tempKeys != nil {
		expiredBefore := time.Now().Add(-tempAuthKeyExpiryGrace).Unix()
		tempDeleted, err := w.tempKeys.DeleteExpired(ctx, expiredBefore, w.batch)
		if err != nil {
			w.logger.Warn("回收过期 temp auth key 绑定失败", zap.Error(err))
		} else if tempDeleted > 0 {
			w.logger.Info("回收过期 temp auth key 绑定完成", zap.Int("deleted", tempDeleted))
		}
	}
	if w.botAPIUpdates != nil {
		botAPIDeleted, err := w.botAPIUpdates.DeleteDeliveredOrExpired(ctx, botAPIConfirmedGrace, w.botAPIRetention, w.batch)
		if err != nil {
			w.logger.Warn("回收 bot_api_updates 队列失败", zap.Error(err))
		} else if botAPIDeleted > 0 {
			w.logger.Info("回收 bot_api_updates 队列完成", zap.Int("deleted", botAPIDeleted))
		}
	}
}
