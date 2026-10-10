package wallet

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"testing"

	"github.com/xssnick/tonutils-go/tlb"
	"github.com/xssnick/tonutils-go/tvm/cell"
)

func TestProofMatchesClientReferenceVector(t *testing.T) {
	hash, err := ProofHash(0, bytes.Repeat([]byte{0x11}, 32), "example.com", 1700000000, "nonce")
	if err != nil || hex.EncodeToString(hash[:]) != "c65bdd5675baff214a9c3fac25c6e48007bcefce280c94827d4e2562a5638441" {
		t.Fatal("wallet-engine signing vector mismatch", err)
	}
}

func TestProofBindsEveryField(t *testing.T) {
	key := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{1}, 32))
	pub := key.Public().(ed25519.PublicKey)
	addr := bytes.Repeat([]byte{0x11}, 32)
	hash, _ := ProofHash(0, addr, "example.com", 1700000000, "nonce")
	sig := ed25519.Sign(key, hash[:])
	if err := VerifyProof(0, addr, pub, "example.com", 1700000000, "nonce", sig); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		wc        int32
		addr, pub []byte
		domain    string
		timestamp uint64
		payload   string
		sig       []byte
	}{
		{-1, addr, pub, "example.com", 1700000000, "nonce", sig},
		{0, bytes.Repeat([]byte{0x12}, 32), pub, "example.com", 1700000000, "nonce", sig},
		{0, addr, bytes.Repeat([]byte{3}, 32), "example.com", 1700000000, "nonce", sig},
		{0, addr, pub, "other.example.com", 1700000000, "nonce", sig},
		{0, addr, pub, "example.com", 1700000001, "nonce", sig},
		{0, addr, pub, "example.com", 1700000000, "other-nonce", sig},
		{0, addr, pub, "example.com", 1700000000, "nonce", sig[:63]},
	} {
		if err := VerifyProof(test.wc, test.addr, test.pub, test.domain, test.timestamp, test.payload, test.sig); err == nil {
			t.Fatal("modified proof accepted")
		}
	}
}

func TestInitialAddressUsesRevisionZeroAndNetwork(t *testing.T) {
	pub := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{1}, 32)).Public().(ed25519.PublicKey)
	main, err := InitialAddress(pub, false)
	if err != nil {
		t.Fatal(err)
	}
	test, err := InitialAddress(pub, true)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(main.Data(), test.Data()) {
		t.Fatal("network IDs must change account hash")
	}
	if _, err := InitialAddress(pub[:31], false); err == nil {
		t.Fatal("invalid anchor accepted")
	}
	// Explicitly assemble the documented StateInit wire graph, independent of
	// tlb.StateInit's reflection encoder, and compare the resulting account hash.
	code := cell.BeginCell().MustStoreSlice([]byte{0xff, 0x00, 0x20, 0x98, 0x21, 0xd7, 0x49, 0x83, 0x08, 0xb9, 0xf2, 0x40, 0xdf, 0x80, 0x85, 0xf8, 0x33, 0xd0, 0xed, 0x1e, 0x20, 0xed, 0x53, 0xd9}, 192).EndCell()
	data := cell.BeginCell().MustStoreSlice(append([]byte{0, 0, 0, 0, 0, 0x7f, 0xff, 0x7f, 0x11}, pub...), 328).EndCell()
	state := cell.BeginCell().MustStoreUInt(0, 2).MustStoreUInt(1, 1).MustStoreRef(code).MustStoreUInt(1, 1).MustStoreRef(data).MustStoreUInt(0, 1).EndCell()
	if !bytes.Equal(main.Data(), state.Hash()) {
		t.Fatal("StateInit layout mismatch")
	}
	var decoded tlb.StateInit
	parser, err := state.BeginParse()
	if err != nil {
		t.Fatal(err)
	}
	if err := tlb.LoadFromCell(&decoded, parser); err != nil {
		t.Fatal(err)
	}
}
