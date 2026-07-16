package rpc

import (
	"context"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tlprofile"
	// registerEncrypted 注册私聊端对端加密（Secret Chat）域 RPC。
	//
	// 归属约定：messages.getDhConfig 属通话域（DH 参数下发），由 registerPhone 注册、
	// 密聊复用，**本处绝不重复注册 messages.getDhConfig**（tlprofile.Dispatcher 同一
	// RPC 重复 On* 是静默 last-wins，会覆盖 phone 域真实现）。
	//
	// P0 落地握手三件套；sendEncrypted / sendEncryptedFile / sendEncryptedService /
	// readEncryptedHistory / setEncryptedTyping / receivedQueue / reportEncryptedSpam /
	// uploadEncryptedFile 暂未注册，落 fallback → NOT_IMPLEMENTED + compatibility trace，
	// 属 P1/P2（qts 引擎 + 消息投递）。设计 docs/secret-chat-module.md。
)

func (r *Router) registerEncrypted(d *tlprofile.Dispatcher) {
	registerRPC[
	// P0：握手三件套。
	*tg.MessagesRequestEncryptionRequest](d, tlprofile.SemanticMethodMessagesRequestEncryption, func(ctx context.Context, layerRequest *tg.MessagesRequestEncryptionRequest) (any, error) {
		return r.onMessagesRequestEncryption(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesAcceptEncryptionRequest](d, tlprofile.SemanticMethodMessagesAcceptEncryption, func(ctx context.Context, layerRequest *tg.MessagesAcceptEncryptionRequest) (

		// P1：qts 消息收发 + 已读/typing + 队列确认。
		any, error) {
		return r.onMessagesAcceptEncryption(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesDiscardEncryptionRequest](d, tlprofile.SemanticMethodMessagesDiscardEncryption, func(ctx context.Context, layerRequest *tg.MessagesDiscardEncryptionRequest) (any, error) {
		return r.onMessagesDiscardEncryption(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesSendEncryptedRequest](d, tlprofile.SemanticMethodMessagesSendEncrypted, func(ctx context.Context, layerRequest *tg.MessagesSendEncryptedRequest) (any, error) {
		return r.onMessagesSendEncrypted(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesSendEncryptedServiceRequest](d, tlprofile.SemanticMethodMessagesSendEncryptedService, func(ctx context.Context, layerRequest *tg.MessagesSendEncryptedServiceRequest) (any, error) {
		return r.onMessagesSendEncryptedService(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesReadEncryptedHistoryRequest](d, tlprofile.SemanticMethodMessagesReadEncryptedHistory, func(ctx context.Context, layerRequest *tg.MessagesReadEncryptedHistoryRequest) (any, error) {
		return r.onMessagesReadEncryptedHistory(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesSetEncryptedTypingRequest](d, tlprofile.SemanticMethodMessagesSetEncryptedTyping, func(ctx context.Context, layerRequest *tg.MessagesSetEncryptedTypingRequest) (any, error) {
		return r.onMessagesSetEncryptedTyping(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesReceivedQueueRequest](d, tlprofile.SemanticMethodMessagesReceivedQueue, func(ctx context.Context, layerRequest *tg.MessagesReceivedQueueRequest) (any,

		// P2：密聊文件。
		error) {
		return r.onMessagesReceivedQueue(ctx, layerRequest.
			MaxQts)
	})
	registerRPC[*tg.MessagesReportEncryptedSpamRequest](d, tlprofile.SemanticMethodMessagesReportEncryptedSpam, func(ctx context.Context, layerRequest *tg.MessagesReportEncryptedSpamRequest) (any, error) {
		return r.onMessagesReportEncryptedSpam(ctx, layerRequest.
			Peer)
	})
	registerRPC[*tg.MessagesSendEncryptedFileRequest](d, tlprofile.SemanticMethodMessagesSendEncryptedFile, func(ctx context.Context, layerRequest *tg.MessagesSendEncryptedFileRequest) (any, error) {
		return r.onMessagesSendEncryptedFile(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesUploadEncryptedFileRequest](d, tlprofile.SemanticMethodMessagesUploadEncryptedFile, func(ctx context.Context, layerRequest *tg.MessagesUploadEncryptedFileRequest) (any, error) {
		return r.onMessagesUploadEncryptedFile(ctx, layerRequest)
	})
}
