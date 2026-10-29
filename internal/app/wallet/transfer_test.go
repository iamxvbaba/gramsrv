package wallet

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"strings"
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
// cannot sign. Both legacy forms must now be rejected by the internal ledger.
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

func TestParseTransferBodyRequiresOneUsePasswordEnvelope(t *testing.T) {
	secret := bytes.Repeat([]byte{7}, 32)
	wire := map[string]any{"version": 1, "recipient_user_id": 42, "random_id": 99, "amount_nanoton": int64(12500000000), "comment": "  hello\nworld  ", "tmp_password": base64.StdEncoding.EncodeToString(secret)}
	body, _ := json.Marshal(wire)
	got, err := ParseTransferBody(body)
	if err != nil || got.AmountNanoton != 12500000000 || got.RecipientUserID != 42 || got.RandomID != 99 || !bytes.Equal(got.TmpPassword, secret) || got.Comment != "helloworld" {
		t.Fatalf("parse: %+v %v", got, err)
	}
	for _, field := range []string{"version", "recipient_user_id", "random_id", "amount_nanoton", "tmp_password"} {
		t.Run(field, func(t *testing.T) {
			copy := map[string]any{}
			for k, v := range wire {
				copy[k] = v
			}
			delete(copy, field)
			b, _ := json.Marshal(copy)
			if _, err := ParseTransferBody(b); err == nil {
				t.Fatal("incomplete authorization accepted")
			}
		})
	}
	wire["unexpected"] = true
	body, _ = json.Marshal(wire)
	if _, err := ParseTransferBody(body); err == nil {
		t.Fatal("unknown field accepted")
	}
}
func TestParseTransferBodyRejectsLegacyAndTON(t *testing.T) {
	_, key, _ := ed25519.GenerateKey(nil)
	bodies := [][]byte{nil, []byte(`{"amount_nanoton":12500000000}`), []byte(`{}`), []byte(strings.Repeat("x", 4097)), buildTransferBody(t, testAddress(t, 7), 100, "", false, nil), buildTransferBody(t, testAddress(t, 7), 100, "", true, key)}
	for i, body := range bodies {
		if _, err := ParseTransferBody(body); err == nil {
			t.Fatalf("legacy body %d accepted", i)
		}
	}
}
func TestTransferCommentIsSanitized(t *testing.T) {
	if got := sanitizeTransferComment("  " + strings.Repeat("я", 200) + "\n"); len([]rune(got)) != 140 {
		t.Fatal("comment bound lost")
	}
}
