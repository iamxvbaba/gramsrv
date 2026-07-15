package rpc

import "github.com/iamxvbaba/td/tg"

// registerEncrypted 注册私聊端对端加密（Secret Chat）域 RPC。
//
// 归属约定：messages.getDhConfig 属通话域（DH 参数下发），由 registerPhone 注册、
// 密聊复用，**本处绝不重复注册 OnMessagesGetDhConfig**（gotd ServerDispatcher 同一
// RPC 重复 On* 是静默 last-wins，会覆盖 phone 域真实现）。
//
// P0 落地握手三件套；sendEncrypted / sendEncryptedFile / sendEncryptedService /
// readEncryptedHistory / setEncryptedTyping / receivedQueue / reportEncryptedSpam /
// uploadEncryptedFile 暂未注册，落 fallback → NOT_IMPLEMENTED + compatibility trace，
// 属 P1/P2（qts 引擎 + 消息投递）。设计 docs/secret-chat-module.md。
func (r *Router) registerEncrypted(d *tg.ServerDispatcher) {
	// P0：握手三件套。
	d.OnMessagesRequestEncryption(r.onMessagesRequestEncryption)
	d.OnMessagesAcceptEncryption(r.onMessagesAcceptEncryption)
	d.OnMessagesDiscardEncryption(r.onMessagesDiscardEncryption)
	// P1：qts 消息收发 + 已读/typing + 队列确认。
	d.OnMessagesSendEncrypted(r.onMessagesSendEncrypted)
	d.OnMessagesSendEncryptedService(r.onMessagesSendEncryptedService)
	d.OnMessagesReadEncryptedHistory(r.onMessagesReadEncryptedHistory)
	d.OnMessagesSetEncryptedTyping(r.onMessagesSetEncryptedTyping)
	d.OnMessagesReceivedQueue(r.onMessagesReceivedQueue)
	d.OnMessagesReportEncryptedSpam(r.onMessagesReportEncryptedSpam)
	// P2：密聊文件。
	d.OnMessagesSendEncryptedFile(r.onMessagesSendEncryptedFile)
	d.OnMessagesUploadEncryptedFile(r.onMessagesUploadEncryptedFile)
}
