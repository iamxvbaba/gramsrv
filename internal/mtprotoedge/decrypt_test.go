package mtprotoedge

import (
	"bytes"
	"crypto/aes"
	"crypto/rand"
	"encoding/binary"
	"testing"

	"github.com/gotd/ige"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/crypto"
)

func newTestAuthKey(t *testing.T) crypto.AuthKey {
	t.Helper()
	var key crypto.Key
	if _, err := rand.Read(key[:]); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return key.WithID()
}

// encryptRawClientPlaintext 用 client side（x=0）把一段已含 32 字节头与 padding 的
// 原始明文加密成完整入站帧，用于构造 gotd Cipher.Encrypt 不允许生成的畸形明文。
func encryptRawClientPlaintext(t *testing.T, key crypto.AuthKey, plaintext []byte) *bin.Buffer {
	t.Helper()
	if len(plaintext)%16 != 0 {
		t.Fatalf("plaintext must be 16-aligned, got %d", len(plaintext))
	}
	msgKey := crypto.MessageKey(key.Value, plaintext, crypto.Client)
	aesKey, iv := crypto.Keys(key.Value, msgKey, crypto.Client)
	blk, err := aes.NewCipher(aesKey[:])
	if err != nil {
		t.Fatalf("aes: %v", err)
	}
	encrypted := make([]byte, len(plaintext))
	ige.EncryptBlocks(blk, iv[:], encrypted, plaintext)

	var b bin.Buffer
	b.Put(key.ID[:])
	b.Put(msgKey[:])
	b.Put(encrypted)
	return &b
}

// buildRawPlaintext 构造 salt/session/msg_id/seq_no + dataLen 头与指定 data/padding 的明文。
// dataLen 允许与真实 data 长度不一致，用于打边界。
func buildRawPlaintext(salt, sessionID, msgID int64, seqNo, dataLen int32, data, padding []byte) []byte {
	out := make([]byte, 0, 32+len(data)+len(padding))
	var hdr [32]byte
	binary.LittleEndian.PutUint64(hdr[0:8], uint64(salt))
	binary.LittleEndian.PutUint64(hdr[8:16], uint64(sessionID))
	binary.LittleEndian.PutUint64(hdr[16:24], uint64(msgID))
	binary.LittleEndian.PutUint32(hdr[24:28], uint32(seqNo))
	binary.LittleEndian.PutUint32(hdr[28:32], uint32(dataLen))
	out = append(out, hdr[:]...)
	out = append(out, data...)
	return append(out, padding...)
}

// TestDecryptClientFrameParityWithGotd 逐字节对照 telesrv 自建解密与 gotd server cipher：
// 同一帧要么双方都接受且字段/数据一致，要么双方都拒绝。覆盖正常帧、畸形长度、
// 超限 padding、篡改 msg_key/auth_key_id、截断帧。
func TestDecryptClientFrameParityWithGotd(t *testing.T) {
	key := newTestAuthKey(t)
	serverCipher := crypto.NewServerCipher(rand.Reader)

	pad := func(n int) []byte {
		p := make([]byte, n)
		if _, err := rand.Read(p); err != nil {
			t.Fatalf("rand: %v", err)
		}
		return p
	}
	data16 := pad(16)

	cases := []struct {
		name  string
		frame *bin.Buffer
	}{
		{"valid_small", encryptRawClientPlaintext(t, key, buildRawPlaintext(1, 2, 7000, 1, 16, data16, pad(16)))},
		{"valid_large", encryptRawClientPlaintext(t, key, buildRawPlaintext(9, 8, 7002, 3, 4096, pad(4096), pad(16)))},
		{"zero_len_data", encryptRawClientPlaintext(t, key, buildRawPlaintext(1, 2, 7004, 5, 0, nil, pad(16)))},
		{"data_len_negative", encryptRawClientPlaintext(t, key, buildRawPlaintext(1, 2, 7006, 7, -4, data16, pad(16)))},
		{"data_len_unaligned", encryptRawClientPlaintext(t, key, buildRawPlaintext(1, 2, 7008, 9, 6, data16, pad(16)))},
		{"data_len_overflow", encryptRawClientPlaintext(t, key, buildRawPlaintext(1, 2, 7010, 11, 64, data16, pad(16)))},
		{"padding_too_big", encryptRawClientPlaintext(t, key, buildRawPlaintext(1, 2, 7012, 13, 16, data16, pad(1040)))},
		{"plaintext_only_header_block", encryptRawClientPlaintext(t, key, pad(16))},
	}

	// 篡改 msg_key。
	tampered := encryptRawClientPlaintext(t, key, buildRawPlaintext(1, 2, 7014, 15, 16, data16, pad(16)))
	tampered.Buf[8] ^= 0xff
	cases = append(cases, struct {
		name  string
		frame *bin.Buffer
	}{"tampered_msg_key", tampered})

	// 错误 auth_key_id。
	wrongKey := encryptRawClientPlaintext(t, key, buildRawPlaintext(1, 2, 7016, 17, 16, data16, pad(16)))
	wrongKey.Buf[0] ^= 0xff
	cases = append(cases, struct {
		name  string
		frame *bin.Buffer
	}{"wrong_auth_key_id", wrongKey})

	// 截断帧。
	cases = append(cases,
		struct {
			name  string
			frame *bin.Buffer
		}{"truncated_header", &bin.Buffer{Buf: pad(16)}},
		struct {
			name  string
			frame *bin.Buffer
		}{"unaligned_ciphertext", &bin.Buffer{Buf: pad(24 + 15)}},
	)

	var plain bin.Buffer
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotdData, gotdErr := serverCipher.DecryptFromBuffer(key, &bin.Buffer{Buf: append([]byte(nil), tc.frame.Buf...)})
			frame, ourErr := decryptClientFrame(key, &bin.Buffer{Buf: append([]byte(nil), tc.frame.Buf...)}, &plain)

			if (gotdErr == nil) != (ourErr == nil) {
				t.Fatalf("accept/reject mismatch: gotd err=%v, ours err=%v", gotdErr, ourErr)
			}
			if gotdErr != nil {
				return
			}
			if frame.salt != gotdData.Salt || frame.sessionID != gotdData.SessionID ||
				frame.messageID != gotdData.MessageID || frame.seqNo != gotdData.SeqNo {
				t.Fatalf("header mismatch: ours=%+v gotd salt=%d session=%d msg=%d seq=%d",
					frame, gotdData.Salt, gotdData.SessionID, gotdData.MessageID, gotdData.SeqNo)
			}
			if !bytes.Equal(frame.data, gotdData.Data()) {
				t.Fatalf("data mismatch: ours %d bytes, gotd %d bytes", len(frame.data), len(gotdData.Data()))
			}
		})
	}
}

// TestDecryptClientFrameReusesPlainBuffer 验证同一 plain 缓冲跨帧复用：先大帧后小帧，
// 解密结果仍正确且不受前一帧残留字节影响。
func TestDecryptClientFrameReusesPlainBuffer(t *testing.T) {
	key := newTestAuthKey(t)

	big := make([]byte, 2048)
	for i := range big {
		big[i] = byte(i)
	}
	small := []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}

	pad := make([]byte, 16)
	frame1 := encryptRawClientPlaintext(t, key, buildRawPlaintext(1, 2, 7100, 1, int32(len(big)), big, pad))
	frame2 := encryptRawClientPlaintext(t, key, buildRawPlaintext(1, 2, 7102, 3, int32(len(small)), small, pad))

	var plain bin.Buffer
	f1, err := decryptClientFrame(key, frame1, &plain)
	if err != nil {
		t.Fatalf("decrypt big frame: %v", err)
	}
	if !bytes.Equal(f1.data, big) {
		t.Fatal("big frame data mismatch")
	}
	f2, err := decryptClientFrame(key, frame2, &plain)
	if err != nil {
		t.Fatalf("decrypt small frame: %v", err)
	}
	if !bytes.Equal(f2.data, small) {
		t.Fatal("small frame data mismatch after buffer reuse")
	}
	if f2.messageID != 7102 || f2.seqNo != 3 || f2.salt != 1 || f2.sessionID != 2 {
		t.Fatalf("small frame header mismatch: %+v", f2)
	}
	if len(f2.plaintext) != 32+len(small)+len(pad) {
		t.Fatalf("plaintext length not shrunk on reuse: %d", len(f2.plaintext))
	}
}
