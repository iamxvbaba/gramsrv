package memory_test

import (
	"telesrv/internal/store"
	"telesrv/internal/store/memory"
	"telesrv/internal/store/storetest"
	"testing"
)

func TestChannelSignatureProfiles(t *testing.T) {
	s := memory.NewChannelStore()
	storetest.ChannelSignatureProfiles(t, s, func() store.ChannelStore { return s }, 101, 102)
}
