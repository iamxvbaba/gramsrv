package wallet

import (
	"crypto/ed25519"
	"testing"
	"time"

	"github.com/xssnick/tonutils-go/address"
	"github.com/xssnick/tonutils-go/tlb"
	"github.com/xssnick/tonutils-go/tvm/cell"
)

// testAddress builds an address the way the wallet does, so the parser is
// tested against the addresses this deployment actually hands out.
func testAddress(t *testing.T, seed byte) string {
	t.Helper()
	hash := make([]byte, 32)
	for i := range hash {
		hash[i] = seed + byte(i)
	}
	addr := address.NewAddress(0, 0, hash)
	return addr.String()
}

// buildTransferBody builds the body a client sends for a transfer: a TON
// external message carrying a wallet transfer and, optionally, the note the
// sender wrote.
//
// The signature is over the unsigned body, so the body is built first, signed,
// and the signature is prepended. A client that does not hold the wallet's key
// cannot sign, and the server keeps the custody key, so the unsigned form is
// the one the server has to accept as well.
func buildTransferBody(t *testing.T, recipient string, amount int64, comment string, sign bool, key ed25519.PrivateKey) []byte {
	t.Helper()
	dst, err := address.ParseAddr(recipient)
	if err != nil {
		t.Fatalf("parse the recipient address: %v", err)
	}
	coins := tlb.FromNanoTONU(uint64(amount))

	// The note is the text the sender wrote: the comment op and the text.
	var commentCell *cell.Cell
	if comment != "" {
		builder := cell.BeginCell()
		if err := builder.StoreUInt(0, 32); err != nil {
			t.Fatalf("store the comment op: %v", err)
		}
		if err := builder.StoreStringSnake(comment); err != nil {
			t.Fatalf("store the comment: %v", err)
		}
		commentCell = builder.EndCell()
	}
	msg := tlb.InternalMessage{DstAddr: dst, Amount: coins, Body: commentCell}
	msgCell, err := tlb.ToCell(msg)
	if err != nil {
		t.Fatalf("build the internal message: %v", err)
	}

	// The body: the opcode, the wallet id, the expiry, the sequence number and
	// the mode, then the message as a reference.
	body := cell.BeginCell()
	if err := body.StoreUInt(0x63896e75, 32); err != nil {
		t.Fatalf("store the opcode: %v", err)
	}
	if err := body.StoreUInt(0x7fff7f11, 32); err != nil {
		t.Fatalf("store the wallet id: %v", err)
	}
	if err := body.StoreUInt(uint64(time.Now().Add(time.Hour).Unix()), 32); err != nil {
		t.Fatalf("store the expiry: %v", err)
	}
	if err := body.StoreUInt(0, 32); err != nil {
		t.Fatalf("store the sequence number: %v", err)
	}
	if err := body.StoreUInt(3, 8); err != nil {
		t.Fatalf("store the mode: %v", err)
	}
	if err := body.StoreRef(msgCell); err != nil {
		t.Fatalf("store the message ref: %v", err)
	}
	bodyCell := body.EndCell()

	if !sign {
		// The unsigned body is the body itself.
		ext := tlb.ExternalMessage{DstAddr: dst, Body: bodyCell}
		root, err := tlb.ToCell(ext)
		if err != nil {
			t.Fatalf("build the external message: %v", err)
		}
		return root.ToBOC()
	}

	// The signed body is the signature followed by the unsigned body.
	signed := cell.BeginCell()
	if err := signed.StoreSlice(ed25519.Sign(key, bodyCell.Hash()), 512); err != nil {
		t.Fatalf("store the signature: %v", err)
	}
	if err := signed.StoreRef(bodyCell); err != nil {
		t.Fatalf("store the unsigned ref: %v", err)
	}
	ext := tlb.ExternalMessage{DstAddr: dst, Body: signed.EndCell()}
	root, err := tlb.ToCell(ext)
	if err != nil {
		t.Fatalf("build the external message: %v", err)
	}
	return root.ToBOC()
}

// The parser reads the amount, the recipient and the note out of the body a
// client builds. A client that does not hold the wallet's key cannot sign it,
// and the server keeps the custody key, so the unsigned body is accepted: the
// authenticated session is what authorizes the debit.
func TestParseTransferBodyReadsTheMovement(t *testing.T) {
	recipient := testAddress(t, 7)
	body := buildTransferBody(t, recipient, 12_500_000_000, "for the gift", false, nil)
	intent, err := ParseTransferBody(body, nil, "")
	if err != nil {
		t.Fatalf("parse the transfer body: %v", err)
	}
	if intent.AmountNanoton != 12_500_000_000 {
		t.Fatalf("amount = %d, want 12500000000", intent.AmountNanoton)
	}
	if intent.RecipientAddress == "" {
		t.Fatal("the parser did not read the recipient")
	}
	if intent.Comment != "for the gift" {
		t.Fatalf("comment = %q, want %q", intent.Comment, "for the gift")
	}
}

// A body the client signed is checked against the wallet's public key, and one
// signed by somebody else is refused.
func TestParseTransferBodyChecksTheSignature(t *testing.T) {
	recipient := testAddress(t, 9)
	publicKey, privateKey, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate a key: %v", err)
	}
	body := buildTransferBody(t, recipient, 1_000_000_000, "", true, privateKey)
	intent, err := ParseTransferBody(body, publicKey, "")
	if err != nil {
		t.Fatalf("parse the signed body: %v", err)
	}
	if !intent.Signed {
		t.Fatal("the parser did not recognize the signature")
	}
	if intent.AmountNanoton != 1_000_000_000 {
		t.Fatalf("amount = %d, want 1000000000", intent.AmountNanoton)
	}

	// A body signed by a different key is refused.
	_, otherKey, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate a key: %v", err)
	}
	forged := buildTransferBody(t, recipient, 1_000_000_000, "", true, otherKey)
	if _, err := ParseTransferBody(forged, publicKey, ""); err == nil {
		t.Fatal("want an error for a body signed by somebody else, got none")
	}
}

// The deployment's own body format carries the same facts.
func TestParseTransferBodyReadsTheNativeBody(t *testing.T) {
	body := []byte(`{"amount_nanoton": 4200000000, "comment": "za kofe"}`)
	intent, err := ParseTransferBody(body, nil, "")
	if err != nil {
		t.Fatalf("parse the native body: %v", err)
	}
	if intent.AmountNanoton != 4_200_000_000 {
		t.Fatalf("amount = %d, want 4200000000", intent.AmountNanoton)
	}
	if intent.Comment != "za kofe" {
		t.Fatalf("comment = %q, want %q", intent.Comment, "za kofe")
	}
}

// A body that is not a transfer is refused, and so is one that names no
// amount.
func TestParseTransferBodyRefusesGarbage(t *testing.T) {
	for name, body := range map[string][]byte{
		"empty":           {},
		"random bytes":    []byte("not a transfer at all"),
		"no amount":       []byte(`{"comment": "x"}`),
		"zero amount":     []byte(`{"amount_nanoton": 0}`),
		"bad address":     []byte(`{"amount_nanoton": 1, "address": "not-an-address"}`),
		"trailing object": []byte(`{"amount_nanoton": 1} {"amount_nanoton": 2}`),
		"overflow":        []byte(`{"amount_nanoton": 9223372036854775808}`),
		"fraction":        []byte(`{"amount_nanoton": 0.5}`),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseTransferBody(body, nil, ""); err == nil {
				t.Fatal("want an error, got none")
			}
		})
	}
}

// The note is bounded and stripped of control characters, so the history can
// carry it without carrying anything a client could use to forge a message.
func TestTransferCommentIsSanitized(t *testing.T) {
	long := ""
	for i := 0; i < 400; i++ {
		long += "x"
	}
	body := []byte(`{"amount_nanoton": 1, "comment": "` + long + `"}`)
	intent, err := ParseTransferBody(body, nil, "")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len([]rune(intent.Comment)) != maxTransferCommentRunes {
		t.Fatalf("comment length = %d, want %d", len([]rune(intent.Comment)), maxTransferCommentRunes)
	}
	control := []byte(`{"amount_nanoton": 1, "comment": "a\u0007b"}`)
	intent, err = ParseTransferBody(control, nil, "")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if intent.Comment != "ab" {
		t.Fatalf("comment = %q, want %q", intent.Comment, "ab")
	}
}
