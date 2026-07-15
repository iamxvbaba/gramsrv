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

// clientFrame 是解密后的单帧客户端消息视图。data/plaintext 引用调用方持有的复用明文
// 缓冲，仅在解密下一帧前有效；需要跨帧保留的字节必须拷贝（dispatch 对 RPC body 已
// b.Copy()，container/gzip 均在当前帧内同步消费）。
type clientFrame struct {
	salt      int64
	sessionID int64
	messageID int64
	seqNo     int32
	// data 是去掉 32 字节头与 padding 后的消息体。
	data []byte
	// plaintext 是完整明文（头 + 数据 + padding），quick ack token 直接对它做 SHA256，
	// 免去 gotd 路径上「为算 token 把解密结果整帧重编码一遍」的拷贝。
	plaintext []byte
}

// encryptedFrameHeaderLen 是 MTProto 2.0 明文头长度：salt(8)+session_id(8)+msg_id(8)+seq_no(4)+len(4)。
const encryptedFrameHeaderLen = 32

// decryptClientFrame 把一帧客户端加密消息解密进 plain 复用缓冲（telesrv-owned，
// 与出站 encryptOutboundFrame 对称）。校验集合与 gotd Cipher.Decrypt 逐条一致：
// auth_key_id 匹配、密文 16 字节块对齐、msg_key（SHA256，client side x=0）、明文头
// 长度、message_data_len 非负 / 4 字节对齐 / 不越界、padding ≤ 1024。区别只在
// 明文缓冲复用与免结构体堆分配——gotd DecryptFromBuffer 每帧 make 整帧明文
// （上传帧可达 512KiB+），是入站 Dispatch 链路最大的稳态分配点。
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
