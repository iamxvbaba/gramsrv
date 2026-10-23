package codec

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/iamxvbaba/td/bin"
)

func fullTestData() (packet, payload []byte) {
	return []byte{
		15, 0, 0, 0, // length
		1, 0, 0, 0, // seqNo
		97, 98, 99, // payload
		78, 214, 109, 148, // crc
	}, []byte("abc")
}

func TestFull(t *testing.T) {
	packet, payload := fullTestData()
	t.Run("write", func(t *testing.T) {
		b := bytes.NewBuffer(nil)

		buf := &bin.Buffer{Buf: payload}
		err := writeFull(b, 1, buf)
		require.NoError(t, err)

		require.Equal(t, packet, b.Bytes())
	})

	t.Run("read", func(t *testing.T) {
		b := &bin.Buffer{}
		err := readFull(bytes.NewBuffer(packet), 1, b)
		require.NoError(t, err)

		require.Equal(t, payload, b.Buf)
	})
}

func TestFullRejectsUndersizedEnvelopeWithoutPanic(t *testing.T) {
	for _, n := range []uint32{1, 4, 8, 11} {
		t.Run(fmt.Sprintf("%d", n), func(t *testing.T) {
			var header [4]byte
			binary.LittleEndian.PutUint32(header[:], n)

			var b bin.Buffer
			require.NotPanics(t, func() {
				err := readFull(bytes.NewReader(header[:]), 0, &b)
				require.ErrorIs(t, err, ErrInvalidMessageLength)
			})
		})
	}
}

func TestFullDeclaredLengthGrowsWithReceivedPayload(t *testing.T) {
	var header [4]byte
	binary.LittleEndian.PutUint32(header[:], MaxMessageSize)

	var b bin.Buffer
	err := readFull(bytes.NewReader(header[:]), 0, &b)
	require.ErrorIs(t, err, io.EOF)
	require.LessOrEqual(t, cap(b.Buf), 2*inboundReadChunkSize)
}
