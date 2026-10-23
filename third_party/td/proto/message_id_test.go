package proto

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gotd/neo"

	"github.com/iamxvbaba/td/testutil"
)

func TestMessageID(t *testing.T) {
	now := time.Date(2018, 10, 10, 23, 42, 6, 13600, time.UTC)
	id := MessageID(newMessageID(now.UnixNano(), 0))
	if id.Type() != MessageFromClient {
		t.Fatal("invalid type")
	}
	if id != 0x5bbe8e4e00003520 {
		t.Error("mismatch")
	}
	delta := id.Time().Sub(now)
	if delta < 0 {
		delta *= -1
	}
	if delta > time.Second {
		t.Fatal("unexpected time drift")
	}
	t.Run("NewMessageID", func(t *testing.T) {
		if NewMessageID(now, MessageFromServer).Type() != MessageFromServer {
			t.Error("Mismatch")
		}
		if NewMessageID(now, 100).Type() != MessageFromClient {
			t.Error("Mismatch")
		}
	})
	t.Run("IntegralSecondClientID", func(t *testing.T) {
		exact := time.Unix(now.Unix(), 0)
		id := NewMessageID(exact, MessageFromClient)
		if id.Type() != MessageFromClient {
			t.Fatalf("message type = %s, want %s", id.Type(), MessageFromClient)
		}
		if uint32(id) == 0 {
			t.Fatal("client message id has a zero lower 32-bit fractional part")
		}
		if delta := id.Time().Sub(exact); delta != 4*time.Nanosecond {
			t.Fatalf("message time delta = %s, want 4ns", delta)
		}
	})
	t.Run("String", func(t *testing.T) {
		require.Equal(t, "5bbe8e4e00003520 (FromClient, 2018-10-10T23:42:06Z)", id.String())
	})
}

func BenchmarkNewMessageID(b *testing.B) {
	// Note that most overhead will be from time.Now() calls.
	// Just ensuring that NewMessageID itself is reasonably fast.
	now := testutil.Date()
	for i := 0; i < b.N; i++ {
		if NewMessageID(now, MessageFromServer).Type() != MessageFromServer {
			b.Fatal("Mismatch")
		}
	}
}

func TestMessageIDGen(t *testing.T) {
	date := testutil.Date()
	clock := neo.NewTime(date)

	gen := NewMessageIDGen(clock.Now)
	met := make(map[int64]bool)

	for i := 0; i < 1000; i++ {
		if i%10 == 0 {
			clock.Travel(time.Millisecond * 100)
		}

		id := gen.New(MessageFromClient)
		if met[id] {
			t.Fatal("met")
		}

		met[id] = true
	}
}

func TestMessageIDGenIntegralSecond(t *testing.T) {
	now := time.Unix(1_788_283_940, 0)
	gen := NewMessageIDGen(func() time.Time { return now })
	first := gen.New(MessageFromClient)
	second := gen.New(MessageFromClient)
	if uint32(first) == 0 || uint32(second) == 0 {
		t.Fatalf("client message ids have zero fractional parts: %x, %x", first, second)
	}
	if second <= first {
		t.Fatalf("client message ids are not increasing: %x, %x", first, second)
	}
}

func BenchmarkMsgIDGen_New(b *testing.B) {
	b.ReportAllocs()

	date := testutil.Date()
	var dateCalls int
	now := func() time.Time {
		if dateCalls%100 == 0 {
			date = date.Add(time.Millisecond)
		}
		return date
	}

	gen := NewMessageIDGen(now)

	for i := 0; i < b.N; i++ {
		_ = gen.New(MessageFromServer)
	}
}

func TestNewMessageIDBuf(t *testing.T) {
	t.Run("Zero", func(t *testing.T) {
		buf := NewMessageIDBuf(10)

		assert.False(t, buf.Consume(0))
	})
	t.Run("Ok", func(t *testing.T) {
		buf := NewMessageIDBuf(10)

		assert.True(t, buf.Consume(1))
		assert.False(t, buf.Consume(1))

		t.Run("Sequence", func(t *testing.T) {
			for i := 2; i <= 20; i++ {
				assert.True(t, buf.Consume(int64(i)))
			}
			assert.False(t, buf.Consume(-1))
		})
	})
}

func BenchmarkMessageIDBuf(b *testing.B) {
	buf := NewMessageIDBuf(100)
	for i := 0; i < b.N; i++ {
		buf.Consume(int64(i))
	}
}
