package wallet

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"strings"

	"telesrv/internal/domain"
)

var ErrInvalidTransferBody = errors.New("invalid internal transfer envelope")
var ErrTransferDisabled = errors.New("internal wallet transfers are disabled")

// TransferIntent is an internal-ledger request, never a TON transaction.
// The one-use account.getTmpPassword proof is consumed by the ledger transaction.
type TransferIntent struct {
	RecipientUserID int64
	RandomID        int64
	AmountNanoton   int64
	Comment         string
	TmpPassword     []byte
}

// ParseTransferBody accepts only the versioned, password-authorized extension.
// Legacy JSON and both signed and unsigned TON BOCs are deliberately unsupported:
// wallet keys/seqnos are not authority to spend this deployment's internal ledger.
func ParseTransferBody(body []byte) (TransferIntent, error) {
	if len(body) == 0 || len(body) > 4096 {
		return TransferIntent{}, ErrInvalidTransferBody
	}
	var wire struct {
		Version         int    `json:"version"`
		RecipientUserID int64  `json:"recipient_user_id"`
		RandomID        int64  `json:"random_id"`
		AmountNanoton   int64  `json:"amount_nanoton"`
		Comment         string `json:"comment,omitempty"`
		TmpPassword     string `json:"tmp_password"`
	}
	d := json.NewDecoder(bytes.NewReader(body))
	d.DisallowUnknownFields()
	if d.Decode(&wire) != nil || d.Decode(new(any)) != io.EOF || wire.Version != 1 || wire.RecipientUserID <= 0 || wire.RandomID == 0 || wire.AmountNanoton <= 0 {
		return TransferIntent{}, ErrInvalidTransferBody
	}
	secret, err := base64.StdEncoding.Strict().DecodeString(wire.TmpPassword)
	if err != nil || len(secret) != 32 {
		return TransferIntent{}, domain.ErrTmpPasswordInvalid
	}
	return TransferIntent{RecipientUserID: wire.RecipientUserID, RandomID: wire.RandomID, AmountNanoton: wire.AmountNanoton, Comment: sanitizeTransferComment(wire.Comment), TmpPassword: secret}, nil
}

const maxTransferCommentRunes = 140

func sanitizeTransferComment(raw string) string {
	cleaned := strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, strings.TrimSpace(raw))
	runes := []rune(cleaned)
	if len(runes) > maxTransferCommentRunes {
		runes = runes[:maxTransferCommentRunes]
	}
	return strings.TrimSpace(string(runes))
}

type TransferStore interface {
	TransferWalletGram(context.Context, domain.WalletGramTransferRequest) (domain.WalletGramTransferResult, error)
}

// WithInternalTransfers is an explicit opt-in. Identity/read capabilities alone
// never enable debits, and the service fails closed without the atomic store.
func WithInternalTransfers(enabled bool, store TransferStore) ServiceOption {
	return func(s *Service) {
		if enabled {
			s.transfers = store
		}
	}
}

func (s *Service) TransferWalletGram(ctx context.Context, req domain.WalletGramTransferRequest) (domain.WalletGramTransferResult, error) {
	if s == nil || s.transfers == nil {
		return domain.WalletGramTransferResult{}, ErrTransferDisabled
	}
	return s.transfers.TransferWalletGram(ctx, req)
}
