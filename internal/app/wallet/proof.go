package wallet

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"unicode/utf8"

	"github.com/xssnick/tonutils-go/address"
	"github.com/xssnick/tonutils-go/tlb"
	"github.com/xssnick/tonutils-go/tvm/cell"
)

var ErrOwnershipProof = errors.New("invalid wallet ownership proof")

// Trampoline and revision-00 data layout match wallet-engine's public state
// derivation. A rotated signing key MUST NOT be used as an address anchor.
const trampolineBOC = "te6ccgEBAQEAGgAAMP8AIJgh10mDCLnyQN+Ahfgz0O0eIO1T2Q=="
const trampolineHash = "9149ae51c1e4689710cebf7830297b16acfbadb363a920a537893e7ffeeca768"

// InitialAddress derives the Wallet revision-00 account from its ANCHOR key.
// It does not infer a rotated account's address from its current signing key.
func InitialAddress(anchorKey []byte, testnet bool) (*address.Address, error) {
	if len(anchorKey) != ed25519.PublicKeySize {
		return nil, ErrOwnershipProof
	}
	boc, err := base64.StdEncoding.DecodeString(trampolineBOC)
	if err != nil {
		return nil, err
	}
	code, err := cell.FromBOC(boc)
	if err != nil {
		return nil, err
	}
	expected, _ := hex.DecodeString(trampolineHash)
	if !bytes.Equal(code.Hash(), expected) {
		return nil, errors.New("wallet trampoline hash mismatch")
	}
	walletID := uint64(0x7fff7f11)
	if testnet {
		walletID = 0x7fff7ffd
	}
	data := cell.BeginCell().MustStoreUInt(0, 8).MustStoreUInt(0, 32).MustStoreUInt(walletID, 32).MustStoreSlice(anchorKey, 256).EndCell()
	init, err := tlb.ToCell(tlb.StateInit{Code: code, Data: data})
	if err != nil {
		return nil, err
	}
	addr := address.NewAddress(0, 0, init.Hash())
	addr.SetTestnetOnly(testnet)
	return addr, nil
}

// ProofHash is the TON Connect v2 signing digest used by the current iOS wallet
// link flow. Domain and payload must be the server-issued values, never values
// taken solely from an untrusted request. Replay/expiry checks belong to the
// atomic challenge-consumption transaction in the RPC service.
func ProofHash(workchain int32, accountHash []byte, domain string, timestamp uint64, payload string) ([32]byte, error) {
	if len(accountHash) != 32 || len(domain) == 0 || len(domain) > 2048 || len(payload) > 65536 || !utf8.ValidString(domain) || !utf8.ValidString(payload) {
		return [32]byte{}, ErrOwnershipProof
	}
	message := make([]byte, 0, 80+len(domain)+len(payload))
	message = append(message, "ton-proof-item-v2/"...)
	message = binary.BigEndian.AppendUint32(message, uint32(workchain))
	message = append(message, accountHash...)
	message = binary.LittleEndian.AppendUint32(message, uint32(len(domain)))
	message = append(message, domain...)
	message = binary.LittleEndian.AppendUint64(message, timestamp)
	message = append(message, payload...)
	inner := sha256.Sum256(message)
	outer := append([]byte{0xff, 0xff}, "ton-connect"...)
	outer = append(outer, inner[:]...)
	return sha256.Sum256(outer), nil
}

// VerifyProof verifies the signature for an independently authenticated
// account/key mapping. It does NOT by itself establish that a key owns an
// address: obtain that mapping from validated StateInit or current chain state.
func VerifyProof(workchain int32, accountHash, signingPublicKey []byte, domain string, timestamp uint64, payload string, signature []byte) error {
	if len(signingPublicKey) != ed25519.PublicKeySize || len(signature) != ed25519.SignatureSize {
		return ErrOwnershipProof
	}
	hash, err := ProofHash(workchain, accountHash, domain, timestamp, payload)
	if err != nil || !ed25519.Verify(ed25519.PublicKey(signingPublicKey), hash[:], signature) {
		return ErrOwnershipProof
	}
	return nil
}
