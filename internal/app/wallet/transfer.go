package wallet

import (
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"

	"github.com/xssnick/tonutils-go/tlb"
	"github.com/xssnick/tonutils-go/tvm/cell"

	"telesrv/internal/domain"
)

// ErrInvalidTransferBody is returned for a body that is not a transfer this
// server can act on.
var ErrInvalidTransferBody = errors.New("invalid transfer body")

// TransferIntent is what a transfer body says: where the money goes and how
// much of it.
//
// The wallet holds its own ledger, so a transfer is a ledger movement rather
// than a chain transaction. The body is still parsed as the client sends it --
// a TON external message -- because that is the only format the client knows
// how to build, and reading the amount out of it is what tells the server how
// much to move.
type TransferIntent struct {
	// RecipientAddress is the wallet address the body names.
	RecipientAddress string
	// AmountNanoton is the amount the body carries.
	AmountNanoton int64
	// Comment is the note the sender wrote on the transfer. It is what the
	// transfer says it is for, on both sides of the history.
	Comment string
	// Signed reports whether the body carried a signature that matched the
	// sender's public key. A body without a signature is still accepted: the
	// server holds the wallet's custody key, so the authenticated session is
	// what authorizes the movement. A body with a WRONG signature is refused.
	Signed bool
}

// nativeTransferBody is the deployment's own transfer body, for the surfaces
// that are not the Telegram client (the operator's own tooling). It carries the
// same facts the TON body carries, so both paths end in the same ledger
// movement.
type nativeTransferBody struct {
	AmountNanoton int64  `json:"amount_nanoton"`
	Address       string `json:"address,omitempty"`
	Comment       string `json:"comment,omitempty"`
}

// ParseTransferBody reads a transfer body.
//
// Two formats are accepted:
//
//   - a TON external message (the custom-format body the Telegram client
//     builds). The amount and the recipient are read out of it, and a
//     signature is checked against the sender's public key when one is
//     present.
//   - the deployment's own JSON body, for the tooling that is not the client.
//
// The two are told apart by the first byte: a TON body starts with the BOC
// magic, and a JSON body starts with '{'.
func ParseTransferBody(body []byte, senderPublicKey []byte, senderAddress string) (TransferIntent, error) {
	if len(body) == 0 || len(body) > 1<<20 {
		return TransferIntent{}, ErrInvalidTransferBody
	}
	if body[0] == '{' {
		return parseNativeTransferBody(body)
	}
	return parseTONTransferBody(body, senderPublicKey, senderAddress)
}

func parseNativeTransferBody(body []byte) (TransferIntent, error) {
	var parsed nativeTransferBody
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&parsed); err != nil {
		return TransferIntent{}, ErrInvalidTransferBody
	}
	if decoder.Decode(new(any)) != io.EOF {
		return TransferIntent{}, ErrInvalidTransferBody
	}
	if parsed.AmountNanoton <= 0 {
		return TransferIntent{}, ErrInvalidTransferBody
	}
	if parsed.Address != "" {
		if _, err := domain.CanonicalTONAddress(parsed.Address); err != nil {
			return TransferIntent{}, ErrInvalidTransferBody
		}
	}
	return TransferIntent{
		RecipientAddress: parsed.Address,
		AmountNanoton:    parsed.AmountNanoton,
		Comment:          sanitizeTransferComment(parsed.Comment),
	}, nil
}

// parseTONTransferBody reads a TON external message and extracts the transfer
// it carries.
//
// Only a plain wallet transfer is admitted. A body that names another
// contract, that carries extra currencies, or that bounces is not a transfer
// this ledger can record, and admitting it would put an amount in the ledger
// that no account asked for.
func parseTONTransferBody(body []byte, senderPublicKey []byte, senderAddress string) (intent TransferIntent, err error) {
	defer func() {
		// The TON cell parser panics on a malformed cell rather than returning
		// an error. A body comes from the network, so a panic must not take the
		// request down with it.
		if recover() != nil {
			intent, err = TransferIntent{}, ErrInvalidTransferBody
		}
	}()
	root, err := cell.FromBOC(body)
	if err != nil {
		return TransferIntent{}, ErrInvalidTransferBody
	}
	rootSlice, err := root.BeginParse()
	if err != nil {
		return TransferIntent{}, ErrInvalidTransferBody
	}
	var ext tlb.ExternalMessage
	if err := tlb.LoadFromCell(&ext, rootSlice); err != nil || ext.DstAddr == nil || ext.Body == nil {
		return TransferIntent{}, ErrInvalidTransferBody
	}
	if senderAddress != "" && !sameAddress(ext.DstAddr.StringRaw(), senderAddress) {
		return TransferIntent{}, ErrInvalidTransferBody
	}
	if ext.StateInit != nil && !sameAddress(ext.StateInit.CalcAddress(0).StringRaw(), senderAddress) {
		return TransferIntent{}, ErrInvalidTransferBody
	}
	// The body comes in two shapes.
	//
	// The signed one is what a client that holds the wallet's key sends: the
	// signature, then a reference to the unsigned body. The signature is over
	// the hash of that body, and it is checked against the wallet's public key.
	//
	// The unsigned one is what a client that does NOT hold the key sends: the
	// server keeps the custody key, so the client cannot sign, and the fields
	// are the body itself. The authenticated session is what authorizes that
	// debit, so the body is read as it is.
	bodySlice, err := ext.Body.BeginParse()
	if err != nil {
		return TransferIntent{}, ErrInvalidTransferBody
	}
	signed := false
	if signature, sigErr := bodySlice.LoadSlice(512); sigErr == nil {
		if allZero(signature) {
			// An empty signature: the fields follow inline.
		} else {
			// The signed shape: the unsigned body is a reference.
			unsigned, refErr := bodySlice.LoadRefCell()
			if refErr != nil {
				return TransferIntent{}, ErrInvalidTransferBody
			}
			if len(senderPublicKey) == ed25519.PublicKeySize {
				if !ed25519.Verify(senderPublicKey, unsigned.Hash(), signature) {
					return TransferIntent{}, ErrInvalidTransferBody
				}
				signed = true
			}
			bodySlice, err = unsigned.BeginParse()
			if err != nil {
				return TransferIntent{}, ErrInvalidTransferBody
			}
		}
	} else {
		// No signature slot at all: the fields are the body, read from the
		// start.
		bodySlice, err = ext.Body.BeginParse()
		if err != nil {
			return TransferIntent{}, ErrInvalidTransferBody
		}
	}
	opcode, err := bodySlice.LoadUInt(32)
	if err != nil || opcode != 0x63896e75 {
		return TransferIntent{}, ErrInvalidTransferBody
	}
	walletID, err := bodySlice.LoadUInt(32)
	if err != nil || walletID != 0x7fff7f11 {
		return TransferIntent{}, ErrInvalidTransferBody
	}
	expiry, err := bodySlice.LoadUInt(32)
	if err != nil || int64(expiry) <= time.Now().Unix() || int64(expiry) > time.Now().Add(24*time.Hour).Unix() {
		return TransferIntent{}, ErrInvalidTransferBody
	}
	if _, err := bodySlice.LoadUInt(32); err != nil {
		return TransferIntent{}, ErrInvalidTransferBody
	}
	mode, err := bodySlice.LoadUInt(8)
	if err != nil || mode&^uint64(1|2|128) != 0 {
		return TransferIntent{}, ErrInvalidTransferBody
	}
	msgCell, err := bodySlice.LoadRefCell()
	if err != nil || bodySlice.BitsLeft() != 0 || bodySlice.RefsNum() != 0 {
		return TransferIntent{}, ErrInvalidTransferBody
	}
	var msg tlb.InternalMessage
	msgSlice, err := msgCell.BeginParse()
	if err != nil {
		return TransferIntent{}, ErrInvalidTransferBody
	}
	if err := tlb.LoadFromCell(&msg, msgSlice); err != nil || msg.DstAddr == nil || msg.Bounced || msg.StateInit != nil {
		return TransferIntent{}, ErrInvalidTransferBody
	}
	if msg.ExtraCurrencies != nil && !msg.ExtraCurrencies.IsEmpty() {
		return TransferIntent{}, ErrInvalidTransferBody
	}
	if !msg.Amount.Nano().IsInt64() {
		return TransferIntent{}, ErrInvalidTransferBody
	}
	amount := msg.Amount.Nano().Int64()
	if amount <= 0 {
		return TransferIntent{}, ErrInvalidTransferBody
	}
	// Only a plain or encrypted comment is admitted, not an arbitrary contract
	// call: the ledger records an amount and a note, not a contract
	// invocation.
	//
	// A plain comment (op 0) is the note the sender wrote, and it is what the
	// transfer says it is for. An encrypted comment (op 0x2167da4b) is the
	// client's private text: the server holds no key for it, so it is recorded
	// as encrypted and the note stays empty.
	comment := ""
	if msg.Body != nil && msg.Body.BitsSize() > 0 {
		content, err := msg.Body.BeginParse()
		if err != nil {
			return TransferIntent{}, ErrInvalidTransferBody
		}
		op, err := content.LoadUInt(32)
		if err != nil {
			return TransferIntent{}, ErrInvalidTransferBody
		}
		switch op {
		case 0:
			// A plain comment: the text the sender wrote.
			comment = sanitizeTransferComment(msg.Comment())
		case 0x2167da4b:
			// An encrypted comment is the client's own private text. The
			// server holds no key for it, so the note stays empty rather
			// than storing something it cannot read.
		default:
			return TransferIntent{}, ErrInvalidTransferBody
		}
	}
	// The address is canonicalised so the message and the wallet record can be
	// compared: the friendly EQ... form and the raw 0:... form are the same
	// account.
	recipient, err := domain.CanonicalTONAddress(msg.DstAddr.StringRaw())
	if err != nil {
		return TransferIntent{}, ErrInvalidTransferBody
	}
	return TransferIntent{
		RecipientAddress: recipient,
		AmountNanoton:    amount,
		Comment:          comment,
		Signed:           signed,
	}, nil
}

// sameAddress compares two TON addresses by their canonical form, so
// "EQ..." and "0:..." are the same account.
func sameAddress(a, b string) bool {
	x, err := domain.CanonicalTONAddress(a)
	if err != nil {
		return false
	}
	y, err := domain.CanonicalTONAddress(b)
	if err != nil {
		return false
	}
	return x == y
}

// maxTransferCommentRunes bounds the note a transfer can carry. The client
// shows it in the history, so it is short enough to read and short enough to
// store.
const maxTransferCommentRunes = 140

// sanitizeTransferComment trims a note to what the ledger stores: no control
// characters, no leading or trailing space, and at most a readable length.
func sanitizeTransferComment(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	cleaned := strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, raw)
	runes := []rune(cleaned)
	if len(runes) > maxTransferCommentRunes {
		runes = runes[:maxTransferCommentRunes]
	}
	return strings.TrimSpace(string(runes))
}

func allZero(b []byte) bool {
	for _, v := range b {
		if v != 0 {
			return false
		}
	}
	return true
}
