package rpc

import (
	"context"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tlprofile"
	// registerEncrypted registers the private end-to-end encrypted (Secret Chat)
	// domain RPCs.
	//
	// Ownership convention: messages.getDhConfig belongs to the call domain (DH
	// parameter distribution), is registered by registerPhone and reused by secret
	// chat; **messages.getDhConfig is never re-registered here** (a duplicate On*
	// for the same RPC in tlprofile.Dispatcher is silently last-wins and would
	// override the real phone-domain implementation).
	//
	// P0 lands the handshake trio; sendEncrypted / sendEncryptedFile /
	// sendEncryptedService / readEncryptedHistory / setEncryptedTyping /
	// receivedQueue / reportEncryptedSpam / uploadEncryptedFile are not registered
	// yet, falling back → NOT_IMPLEMENTED + compatibility trace, belonging to
	// P1/P2 (qts engine + message delivery). Design: docs/secret-chat-module.md.
)

func (r *Router) registerEncrypted(d *tlprofile.Dispatcher) {
	registerRPC[
	// P0: the handshake trio.
	*tg.MessagesRequestEncryptionRequest](d, tlprofile.SemanticMethodMessagesRequestEncryption, func(ctx context.Context, layerRequest *tg.MessagesRequestEncryptionRequest) (any, error) {
		return r.onMessagesRequestEncryption(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesAcceptEncryptionRequest](d, tlprofile.SemanticMethodMessagesAcceptEncryption, func(ctx context.Context, layerRequest *tg.MessagesAcceptEncryptionRequest) (

		// P1: qts message send/receive + read/typing + queue acknowledgement.
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

		// P2: secret chat files.
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
