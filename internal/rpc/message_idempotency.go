package rpc

import (
	"crypto/sha256"
	"fmt"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/tg"
)

// rpcRequestFingerprint 在任何自动实体补全、链接预览解析或上传媒体落库之前，
// 对客户端原始 TL request 取稳定指纹。这样 lost-response 重放不会因服务端派生
// photo/document id、pending webpage 状态等变化而被误判为另一条消息。
func rpcRequestFingerprint(req bin.Encoder) ([]byte, error) {
	if req == nil {
		return nil, fmt.Errorf("fingerprint rpc request: nil request")
	}
	var b bin.Buffer
	if err := req.Encode(&b); err != nil {
		return nil, fmt.Errorf("fingerprint rpc request: %w", err)
	}
	sum := sha256.Sum256(b.Raw())
	return sum[:], nil
}

// sendMessageIdempotencyFingerprint fingerprints only the durable message intent.
// clear_draft/background/update_stickersets_order are one-shot client-side delivery
// hints: after a lost response DrKLO/TDesktop may legitimately retry the same
// random_id without them. They must not turn an exact send replay into
// RANDOM_ID_DUPLICATE.
func sendMessageIdempotencyFingerprint(req *tg.MessagesSendMessageRequest) ([]byte, error) {
	if req == nil {
		return nil, fmt.Errorf("fingerprint messages.sendMessage: nil request")
	}
	clone := *req
	clone.Flags = 0
	clone.ClearDraft = false
	clone.Background = false
	clone.UpdateStickersetsOrder = false
	return rpcRequestFingerprint(&clone)
}

func sendMediaIdempotencyFingerprint(req *tg.MessagesSendMediaRequest) ([]byte, error) {
	if req == nil {
		return nil, fmt.Errorf("fingerprint messages.sendMedia: nil request")
	}
	clone := *req
	clone.Flags = 0
	clone.ClearDraft = false
	clone.Background = false
	clone.UpdateStickersetsOrder = false
	return rpcRequestFingerprint(&clone)
}

// sendMultiMediaItemIdempotencyFingerprint deliberately reduces a batch to one
// InputSingleMedia. A retry containing only the failed subset therefore produces
// the same fingerprint for every surviving random_id as the original batch.
func sendMultiMediaItemIdempotencyFingerprint(req *tg.MessagesSendMultiMediaRequest, item tg.InputSingleMedia) ([]byte, error) {
	if req == nil {
		return nil, fmt.Errorf("fingerprint messages.sendMultiMedia item: nil request")
	}
	clone := *req
	clone.Flags = 0
	clone.ClearDraft = false
	clone.Background = false
	clone.UpdateStickersetsOrder = false
	clone.MultiMedia = []tg.InputSingleMedia{item}
	return rpcRequestFingerprint(&clone)
}

// forwardMessagesItemIdempotencyFingerprint makes the source message id and its
// paired random_id the unit of idempotency. Hashing the full ID/RandomID vectors
// incorrectly rejects a legal retry that contains only a failed subset.
func forwardMessagesItemIdempotencyFingerprint(req *tg.MessagesForwardMessagesRequest, messageID int, randomID int64) ([]byte, error) {
	if req == nil {
		return nil, fmt.Errorf("fingerprint messages.forwardMessages item: nil request")
	}
	clone := *req
	clone.Flags = 0
	clone.Background = false
	clone.ID = []int{messageID}
	clone.RandomID = []int64{randomID}
	return rpcRequestFingerprint(&clone)
}
