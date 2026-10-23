package mtproto

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPingBeforeRunReturnsTransportNotReady(t *testing.T) {
	conn := New(nil, Options{})
	err := conn.Ping(context.Background())
	require.ErrorIs(t, err, ErrTransportNotReady)
}
