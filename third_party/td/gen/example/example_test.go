package td

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/tmap"
)

func BenchmarkMessage_Encode(b *testing.B) {
	b.ReportAllocs()

	buf := new(bin.Buffer)
	msg := Message{
		Err: Error{
			Message:   "Foo",
			Code:      134,
			Temporary: true,
		},
	}
	for i := 0; i < b.N; i++ {
		msg.Encode(buf)
		buf.Reset()
	}
}

func BenchmarkMessage_Decode(b *testing.B) {
	b.ReportAllocs()

	encodeBuf := new(bin.Buffer)
	msg := &Message{
		Err: Error{
			Message:   "Foo",
			Code:      134,
			Temporary: true,
		},
	}
	msg.Encode(encodeBuf)
	raw := encodeBuf.Raw()
	b.SetBytes(int64(len(raw)))

	buf := new(bin.Buffer)
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		var decoded Message
		buf.ResetTo(raw)
		if err := decoded.Decode(buf); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkID_Decode(b *testing.B) {
	b.ReportAllocs()

	encodeBuf := new(bin.Buffer)
	msg := ResponseID{ID: 1}
	_ = msg.Encode(encodeBuf)
	raw := encodeBuf.Raw()
	b.SetBytes(int64(len(raw)))

	buf := new(bin.Buffer)
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		var decoded ResponseID
		buf.ResetTo(raw)
		if err := decoded.Decode(buf); err != nil {
			b.Fatal(err)
		}
	}
}

func TestMessage(t *testing.T) {
	b := new(bin.Buffer)
	msg := Message{
		Err: Error{
			Message:   "Foo",
			Code:      134,
			Temporary: true,
		},
	}
	msg.Encode(b)

	result := Message{}
	if err := result.Decode(b); err != nil {
		t.Fatal(err)
	}
}

func TestTargetsMessage_Encode(t *testing.T) {
	b := new(bin.Buffer)
	msg := TargetsMessage{
		Targets: []int32{1, 2, 3},
	}
	msg.Encode(b)
	decoded := TargetsMessage{}
	if err := decoded.Decode(b); err != nil {
		t.Fatal(err)
	}
	require.Equal(t, msg, decoded)
}

func TestVectorVectorRoundTrip(t *testing.T) {
	b := new(bin.Buffer)
	want := TestVectorVector{
		Value: [][]string{{"a", "b"}, nil, {"c"}},
	}
	require.NoError(t, want.Encode(b))

	var got TestVectorVector
	require.NoError(t, got.Decode(b))
	require.Equal(t, want, got)
}

func TestVectorVectorPreallocationBoundary(t *testing.T) {
	for _, n := range []int{1023, 1024, 1025} {
		want := TestVectorVector{Value: make([][]string, n)}
		want.Value[n/2] = make([]string, n)
		for i := range want.Value[n/2] {
			want.Value[n/2][i] = "element"
		}
		var wire bin.Buffer
		require.NoError(t, want.Encode(&wire))
		var got TestVectorVector
		require.NoError(t, got.Decode(&wire))
		require.Equal(t, want, got)
		require.Zero(t, wire.Len())
		if n == bin.PreallocateLimit {
			require.Equal(t, n, cap(got.Value))
			require.Equal(t, n, cap(got.Value[n/2]))
		}
	}
}

func TestGetUpdatesResp(t *testing.T) {
	b := new(bin.Buffer)
	v := GetUpdatesResp{
		Updates: []AbstractMessageClass{
			&BigMessage{ID: 12, Count: 3, Escape: true, Summary: true, TargetID: 1},
			&NoMessage{},
			&BytesMessage{Data: []byte{0x1, 0xf3, 104, 205}},
			&TargetsMessage{Targets: []int32{1, 2, 3, 4}},
		},
	}
	v.Encode(b)
	decoded := GetUpdatesResp{}
	if err := decoded.Decode(b); err != nil {
		t.Fatal(err)
	}
	require.Equal(t, v, decoded)
}

func TestDecodeToNil(t *testing.T) {
	b := new(bin.Buffer)
	if err := (&TargetsMessage{}).Encode(b); err != nil {
		t.Fatal(err)
	}
	var msg *TargetsMessage
	if err := msg.Decode(b); err == nil {
		t.Fatal("unexpected success")
	}
}

func TestGetUpdatesRespNilElem(t *testing.T) {
	b := new(bin.Buffer)
	var tMessage *TargetsMessage
	v := GetUpdatesResp{
		Updates: []AbstractMessageClass{
			&BigMessage{ID: 12, Count: 3, Escape: true, Summary: true, TargetID: 1},
			&NoMessage{},
			&TargetsMessage{Targets: []int32{1, 2, 3, 4}},
			tMessage,
		},
	}
	if err := v.Encode(b); err == nil {
		t.Fatal("unexpected success")
	}
}

type mockInvoker struct {
	input  bin.Encoder
	output bin.Encoder
}

func (m *mockInvoker) Invoke(ctx context.Context, input bin.Encoder, output bin.Decoder) error {
	m.input = input

	buf := bin.Buffer{}
	err := m.output.Encode(&buf)
	if err != nil {
		return err
	}

	return output.Decode(&buf)
}

func TestVectorResponse(t *testing.T) {
	elems := []int{1, 2, 3}
	m := mockInvoker{
		output: &IntVector{Elems: []int{1, 2, 3}},
	}
	client := NewClient(&m)

	r, err := client.EchoVector(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}

	require.Equal(t, r, elems)
}

func BenchmarkDecodeBool(b *testing.B) {
	b.ReportAllocs()

	encodeBuf := new(bin.Buffer)
	(&True{}).Encode(encodeBuf)
	raw := encodeBuf.Raw()
	b.SetBytes(int64(len(raw)))

	buf := new(bin.Buffer)
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		buf.ResetTo(raw)
		v, err := DecodeBool(buf)
		if err != nil {
			b.Fatal(err)
		}
		switch v.(type) {
		case *True: // ok
		default:
			b.Fatalf("Unexpected %T", v)
		}
	}
}

func BenchmarkDecodeResponse(b *testing.B) {
	b.ReportAllocs()

	encodeBuf := new(bin.Buffer)
	(&ResponseID{ID: 13}).Encode(encodeBuf)
	raw := encodeBuf.Raw()
	b.SetBytes(int64(len(raw)))

	buf := new(bin.Buffer)
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		buf.ResetTo(raw)
		v, err := DecodeResponse(buf)
		if err != nil {
			b.Fatal(err)
		}
		switch v.(type) {
		case *ResponseID: // ok
		default:
			b.Fatalf("Unexpected %T", v)
		}
	}
}

func BenchmarkDecodeAbstractMessage(b *testing.B) {
	b.Run("NoMessage", func(b *testing.B) {
		b.ReportAllocs()

		encodeBuf := new(bin.Buffer)
		(&NoMessage{}).Encode(encodeBuf)
		raw := encodeBuf.Raw()
		b.SetBytes(int64(len(raw)))

		buf := new(bin.Buffer)
		b.ResetTimer()

		for i := 0; i < b.N; i++ {
			buf.ResetTo(raw)
			v, err := DecodeAbstractMessage(buf)
			if err != nil {
				b.Fatal(err)
			}
			switch v.(type) {
			case *NoMessage: // ok
			default:
				b.Fatalf("Unexpected %T", v)
			}
		}
	})
	b.Run("BigMessage", func(b *testing.B) {
		b.ReportAllocs()

		encodeBuf := new(bin.Buffer)
		(&BigMessage{}).Encode(encodeBuf)
		raw := encodeBuf.Raw()
		b.SetBytes(int64(len(raw)))

		buf := new(bin.Buffer)
		b.ResetTimer()

		for i := 0; i < b.N; i++ {
			buf.ResetTo(raw)
			v, err := DecodeAbstractMessage(buf)
			if err != nil {
				b.Fatal(err)
			}
			switch v.(type) {
			case *BigMessage: // ok
			default:
				b.Fatalf("Unexpected %T", v)
			}
		}
	})
}

func TestRegistry(t *testing.T) {
	c := tmap.NewConstructor(
		TypesConstructorMap(),
	)
	require.NotNil(t, c.New(TextEntityTypeStrikethroughTypeID))
	require.Nil(t, c.New(0x1))
}
