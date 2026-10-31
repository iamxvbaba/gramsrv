package mtprotoedge

import (
	"crypto/aes"
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/gotd/ige"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/crypto"
)

// clientFrame is the decrypted view of a single client message frame.
// data/plaintext reference the caller-owned reused plaintext buffer and are only
// valid until the next frame is decrypted; bytes that must survive across frames
// have to be copied (dispatch already does b.Copy() for the RPC body, and
// container/gzip are consumed synchronously within the current frame).
type clientFrame struct {
	salt      int64
	sessionID int64
	messageID int64
	seqNo     int32
	// data is the message body after stripping the 32-byte header and padding.
	data []byte
	// plaintext is the full plaintext (header + data + padding); the quick ack
	// token is SHA256'd directly over it, avoiding the copy on the gotd path
	// where the whole decrypted frame is re-encoded just to compute the token.
	plaintext []byte
}

// encryptedFrameHeaderLen is the MTProto 2.0 plaintext header length: salt(8)+session_id(8)+msg_id(8)+seq_no(4)+len(4).
const encryptedFrameHeaderLen = 32

// decryptClientFrame decrypts one client encrypted message into the plain reuse
// buffer (telesrv-owned, symmetric with the outbound encryptOutboundFrame). Its
// validation set matches gotd Cipher.Decrypt line by line: auth_key_id match,
// 16-byte ciphertext block alignment, msg_key (SHA256, client side x=0),
// plaintext header length, message_data_len non-negative / 4-byte aligned / not
// out of bounds, padding ≤ 1024. The only difference is plaintext buffer reuse
// and avoiding struct heap allocation—gotd DecryptFromBuffer makes the whole
// plaintext frame every time (upload frames can reach 512KiB+), which is the
// largest steady-state allocation point on the inbound Dispatch path.
func decryptClientFrame(key crypto.AuthKey, b *bin.Buffer, plain *bin.Buffer) (clientFrame, error) {
	buf := b.Buf
	if len(buf) < 24 {
		return clientFrame{}, errors.New("encrypted message is too short")
	}
	var authKeyID [8]byte
	copy(authKeyID[:], buf[:8])
	if authKeyID != key.ID {
		return clientFrame{}, errors.New("unknown auth key id")
	}
	var msgKey bin.Int128
	copy(msgKey[:], buf[8:24])
	encrypted := buf[24:]
	if len(encrypted) == 0 || len(encrypted)%16 != 0 {
		return clientFrame{}, errors.New("invalid encrypted data padding")
	}

	aesKey, iv := crypto.Keys(key.Value, msgKey, crypto.Client)
	aesBlock, err := aes.NewCipher(aesKey[:])
	if err != nil {
		return clientFrame{}, err
	}
	ensureBinBufferLen(plain, len(encrypted))
	ige.DecryptBlocks(aesBlock, iv[:], plain.Buf, encrypted)
	plaintext := plain.Buf

	if crypto.MessageKey(key.Value, plaintext, crypto.Client) != msgKey {
		return clientFrame{}, errors.New("msg_key is invalid")
	}
	if len(plaintext) < encryptedFrameHeaderLen {
		return clientFrame{}, errors.New("message data is too short")
	}

	dataLen := int(int32(binary.LittleEndian.Uint32(plaintext[28:32])))
	withPadding := plaintext[encryptedFrameHeaderLen:]
	switch {
	case dataLen < 0:
		return clientFrame{}, fmt.Errorf("message length is invalid: %d less than zero", dataLen)
	case dataLen%4 != 0:
		return clientFrame{}, fmt.Errorf("message length is invalid: %d is not divisible by 4", dataLen)
	case dataLen > len(withPadding):
		return clientFrame{}, fmt.Errorf("message length %d is bigger than data length %d", dataLen, len(withPadding))
	case len(withPadding)-dataLen > 1024:
		return clientFrame{}, fmt.Errorf("padding %d of message is too big", len(withPadding)-dataLen)
	}

	return clientFrame{
		salt:      int64(binary.LittleEndian.Uint64(plaintext[0:8])),
		sessionID: int64(binary.LittleEndian.Uint64(plaintext[8:16])),
		messageID: int64(binary.LittleEndian.Uint64(plaintext[16:24])),
		seqNo:     int32(binary.LittleEndian.Uint32(plaintext[24:28])),
		data:      withPadding[:dataLen],
		plaintext: plaintext,
	}, nil
}
