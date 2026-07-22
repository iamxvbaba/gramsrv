package rpc

import (
	"context"
	"errors"
	"time"

	"go.uber.org/zap"

	"telesrv/internal/domain"
)

// 链接预览异步回填（P3）：发送时挂的 pending 占位（见 webpage_url_extract.go）由这里带外
// 解析为已解析卡片，并经 messages.EditMessage 的 WebPageResolve 模式就地替换——只换 media、
// 生成 updateWebPage（按 webPage id 与占位关联，不标记「已编辑」、不翻倍扇出）。投递与
// difference 复用既有 dispatch_outbox + 通用消息事件路径。

const (
	webPageResolveConcurrency = 16
	webPageResolveTimeout     = 30 * time.Second
	// webPagePendingLifetime 是客户端在重新拉取 pending 消息前等待的窗口。
	// TDesktop 与 DrKLO 都把 webPagePending.date 解释为绝对截止时间，而不是处理开始时间；
	// 该窗口必须严格大于 resolver 的最大执行时间，避免正常的慢解析被客户端提前标记为失败。
	webPagePendingLifetime = 2 * time.Minute
)

type webPageResolveJob struct {
	senderID    int64
	peer        domain.Peer
	msgID       int
	expectedID  int64
	url         string
	invertMedia bool
	forceLarge  bool
	forceSmall  bool
}

// maybeEnqueueWebPageResolve 若刚发出的消息（私聊或频道）携带 pending 链接预览占位，
// 异步排入解析。msgID 为发送方视角的消息 id，media 为该消息媒体。
func (r *Router) maybeEnqueueWebPageResolve(senderID int64, peer domain.Peer, msgID int, media *domain.MessageMedia) {
	if msgID == 0 || (peer.Type != domain.PeerTypeUser && peer.Type != domain.PeerTypeChannel) {
		return
	}
	if media == nil || media.WebPage == nil || media.WebPage.State != domain.MessageWebPageStatePending {
		return
	}
	r.enqueueWebPageResolve(webPageResolveJob{
		senderID:    senderID,
		peer:        peer,
		msgID:       msgID,
		expectedID:  media.WebPage.ID,
		url:         media.WebPage.URL,
		invertMedia: media.InvertMedia,
		forceLarge:  media.WebPage.ForceLargeMedia,
		forceSmall:  media.WebPage.ForceSmallMedia,
	})
}

// enqueueWebPageResolve 在并发上限内异步处理一个解析任务；满则丢弃（消息留 pending，
// 兜底靠客户端重开会话 getHistory 读到当时仍 pending 的占位——WIP 可接受，P4 加清扫）。
func (r *Router) enqueueWebPageResolve(job webPageResolveJob) {
	if r.webPageResolveSem == nil {
		return
	}
	select {
	case r.webPageResolveSem <- struct{}{}:
	default:
		r.log.Debug("web page resolve dropped: at capacity", zap.Int64("expected_id", job.expectedID))
		return
	}
	go func() {
		defer func() { <-r.webPageResolveSem }()
		ctx, cancel := context.WithTimeout(context.Background(), webPageResolveTimeout)
		defer cancel()
		if err := r.resolvePendingWebPage(ctx, job); err != nil {
			r.log.Debug("web page resolve failed",
				zap.Int64("sender_id", job.senderID),
				zap.Int("msg_id", job.msgID),
				zap.Error(err))
		}
	}()
}

// resolvePendingWebPage 解析链接并就地把消息的 pending 占位替换为已解析卡片（同步，可测）。
// 瞬时抓取失败返回 error（消息留 pending，不毒化热门链接）；幂等冲突（消息已删/已改/已被
// 解析）经 ErrMessageNotModified 静默成功。done 与 empty 两态都替换（empty 让客户端停止转圈）。
func (r *Router) resolvePendingWebPage(ctx context.Context, job webPageResolveJob) error {
	if r.deps.Files == nil {
		return nil
	}
	resolved, err := r.deps.Files.ResolveWebPage(ctx, job.url)
	if err != nil {
		return err
	}
	// 保留发送时占位上的 wrapper 偏好（force large/small），它们不在抓取结果里。
	resolved.ForceLargeMedia = job.forceLarge
	resolved.ForceSmallMedia = job.forceSmall
	media := &domain.MessageMedia{
		Kind:        domain.MessageMediaKindWebPage,
		InvertMedia: job.invertMedia,
		WebPage:     &resolved,
	}
	switch job.peer.Type {
	case domain.PeerTypeChannel:
		if r.deps.Channels == nil {
			return nil
		}
		res, err := r.deps.Channels.EditMessage(ctx, job.senderID, domain.EditChannelMessageRequest{
			UserID:            job.senderID,
			ChannelID:         job.peer.ID,
			ID:                job.msgID,
			Media:             media,
			WebPageResolve:    true,
			ExpectedWebPageID: job.expectedID,
		})
		if errors.Is(err, domain.ErrMessageNotModified) {
			return nil
		}
		if err != nil {
			return err
		}
		// 复用频道编辑扇出（投影 updateChannelWebPage）触达成员；difference 走通用回放。
		r.enqueueChannelEditMessageFanout(ctx, job.senderID, res)
		return nil
	case domain.PeerTypeUser:
		if r.deps.Messages == nil {
			return nil
		}
		_, err = r.deps.Messages.EditMessage(ctx, job.senderID, domain.EditMessageRequest{
			OwnerUserID:       job.senderID,
			Peer:              job.peer,
			ID:                job.msgID,
			Media:             media,
			WebPageResolve:    true,
			ExpectedWebPageID: job.expectedID,
		})
		if errors.Is(err, domain.ErrMessageNotModified) {
			return nil
		}
		return err
	default:
		return nil
	}
}
