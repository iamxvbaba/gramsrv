package tlprofile

import (
	"bytes"
	"fmt"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/tg"
)

// legacySRPTmpPasswordPrefix marks a canonical tmp_password that carries the
// 2FA SRP proof of a pre-232 payments.getStarGiftWithdrawalUrl request.
//
// Layer 232 replaced `password:InputCheckPasswordSRP` with an opaque
// `tmp_password:bytes` obtained from account.getTmpPassword. Older clients
// still prove the password inline; the proof cannot be turned into a server
// token by the codec, so it is carried verbatim and the server verifies it
// exactly as before. The NUL-framed prefix cannot collide with a token the
// server issues as long as the server never issues tokens with this prefix.
var legacySRPTmpPasswordPrefix = []byte("\x00tdlayer/srp-password/v1\x00")

// EncodeLegacySRPTmpPassword packs an SRP password proof into the canonical
// tmp_password field.
func EncodeLegacySRPTmpPassword(password tg.InputCheckPasswordSRPClass) ([]byte, error) {
	if password == nil {
		return nil, fmt.Errorf("legacy SRP password is nil")
	}
	var buf bin.Buffer
	buf.Put(legacySRPTmpPasswordPrefix)
	if err := password.Encode(&buf); err != nil {
		return nil, fmt.Errorf("encode legacy SRP password: %w", err)
	}
	return buf.Copy(), nil
}

// DecodeLegacySRPTmpPassword reports whether tmpPassword carries a packed
// pre-232 SRP proof and, if so, returns it. ok is false for an ordinary
// tmp_password token; a malformed packed proof returns an error.
func DecodeLegacySRPTmpPassword(tmpPassword []byte) (password tg.InputCheckPasswordSRPClass, ok bool, err error) {
	if !bytes.HasPrefix(tmpPassword, legacySRPTmpPasswordPrefix) {
		return nil, false, nil
	}
	buf := bin.Buffer{Buf: tmpPassword[len(legacySRPTmpPasswordPrefix):]}
	password, err = tg.DecodeInputCheckPasswordSRP(&buf)
	if err != nil {
		return nil, true, fmt.Errorf("decode legacy SRP password: %w", err)
	}
	if buf.Len() != 0 {
		return nil, true, fmt.Errorf("decode legacy SRP password: %d trailing bytes", buf.Len())
	}
	return password, true, nil
}

func layerCaptureStarGiftWithdrawalLegacyPasswordDecode(
	_ LayerProfile,
	value *tg.PaymentsGetStarGiftWithdrawalURLRequest,
	password tg.InputCheckPasswordSRPClass,
) error {
	if value == nil {
		return fmt.Errorf("star gift withdrawal target is nil")
	}
	packed, err := EncodeLegacySRPTmpPassword(password)
	if err != nil {
		return err
	}
	value.TmpPassword = packed
	return nil
}

func layerRequireStarGiftWithdrawalTmpPasswordDecode(
	_ LayerProfile,
	value *tg.PaymentsGetStarGiftWithdrawalURLRequest,
) ([]byte, error) {
	if value == nil {
		return nil, fmt.Errorf("star gift withdrawal target is nil")
	}
	if len(value.TmpPassword) == 0 {
		return nil, fmt.Errorf("historical star gift withdrawal omitted its SRP password")
	}
	return value.TmpPassword, nil
}
