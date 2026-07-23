package store

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"fmt"

	"telesrv/internal/domain"
)

const (
	maxPhoneCodeHashBytes = 512
	maxLoginCodeBytes     = 64
)

// LoginCodeDeliveryStore atomically creates the recipient message box, dialog,
// account update event and online-dispatch task for a 777000 login code.
type LoginCodeDeliveryStore interface {
	DeliverLoginCodeMessage(ctx context.Context, req domain.LoginCodeDeliveryRequest) (domain.LoginCodeDeliveryResult, error)
}

// LoginCodeDeliveryKey is the only phone-code-hash representation permitted at
// rest. The raw phone_code_hash stays at the auth/store call boundary.
func LoginCodeDeliveryKey(phoneCodeHash string) ([sha256.Size]byte, error) {
	if phoneCodeHash == "" || len(phoneCodeHash) > maxPhoneCodeHashBytes {
		return [sha256.Size]byte{}, domain.ErrLoginCodeDeliveryInvalid
	}
	return sha256.Sum256([]byte(phoneCodeHash)), nil
}

// LoginCodeFingerprint binds a delivery receipt to its immutable secret code.
// It is keyed by the high-entropy raw phone_code_hash, which is never stored;
// unlike a bare digest of a short login code this cannot be brute-forced from
// the compact receipt alone after the message itself has been deleted.
func LoginCodeFingerprint(phoneCodeHash, code string) ([sha256.Size]byte, error) {
	if phoneCodeHash == "" || len(phoneCodeHash) > maxPhoneCodeHashBytes || code == "" || len(code) > maxLoginCodeBytes {
		return [sha256.Size]byte{}, domain.ErrLoginCodeDeliveryInvalid
	}
	mac := hmac.New(sha256.New, []byte(phoneCodeHash))
	_, _ = mac.Write([]byte(code))
	var fingerprint [sha256.Size]byte
	copy(fingerprint[:], mac.Sum(nil))
	return fingerprint, nil
}

func SameLoginCodeFingerprint(stored []byte, expected [sha256.Size]byte) bool {
	return len(stored) == sha256.Size && subtle.ConstantTimeCompare(stored, expected[:]) == 1
}

// RestoreLoginCodeDeliveryMessage reconstructs the immutable first result from
// a compact receipt. The secret code is not duplicated in the receipt: exact
// replay has already proven the supplied code fingerprint matches.
func RestoreLoginCodeDeliveryMessage(userID int64, code string, date int, privateMessageID int64, messageBoxID, pts int, body string, entities []domain.MessageEntity) (domain.Message, error) {
	if privateMessageID <= 0 || messageBoxID <= 0 || messageBoxID > domain.MaxMessageBoxID || pts <= 0 {
		return domain.Message{}, fmt.Errorf("restore login code delivery: %w: uid=%d box=%d pts=%d", domain.ErrLoginCodeDeliveryInvalid, privateMessageID, messageBoxID, pts)
	}
	msg, err := loginCodeMessageFromRequestContent(userID, code, date, body, entities)
	if err != nil {
		return domain.Message{}, err
	}
	msg.ID = messageBoxID
	msg.UID = privateMessageID
	msg.Pts = pts
	return msg, nil
}

func LoginCodeMessageFromDeliveryRequest(req domain.LoginCodeDeliveryRequest) (domain.Message, error) {
	return loginCodeMessageFromRequestContent(req.UserID, req.Code, req.Date, req.Body, req.Entities)
}

func loginCodeMessageFromRequestContent(userID int64, code string, date int, body string, entities []domain.MessageEntity) (domain.Message, error) {
	if body == "" {
		return domain.OfficialLoginCodeMessage(userID, code, date)
	}
	return domain.OfficialLoginCodeMessageWithContent(userID, code, date, body, entities)
}
