package mtprotoedge

import (
	"testing"

	"github.com/iamxvbaba/td/tlprofile"
	"go.uber.org/zap/zaptest"
)

// TestReceivesUpdatesForAuthKeyRequiresMembershipSync verifies that the
// "fully ready" query requires both receivesUpdates and a successfully established
// channel membership route. When membership sync fails the query must return false,
// letting the RPC-driven flag set short-circuit through and retry—otherwise the
// session would silently miss supergroup pushes in the state of "flagged ready but
// byMemberChannel missing", and there is no channel-dimension pts gap signal for
// the client to self-heal from.
func TestReceivesUpdatesForAuthKeyRequiresMembershipSync(t *testing.T) {
	sm := NewSessionManager(zaptest.NewLogger(t))
	raw := [8]byte{1, 2, 3}
	c := &Conn{sessionID: 42, authKeyID: raw}
	if err := c.FreezeLayerProfile(tlprofile.Profile227); err != nil {
		t.Fatal(err)
	}
	sm.Register(c)
	sm.BindUserForAuthKey(raw, 42, 100)

	sm.SetReceivesUpdatesForAuthKey(raw, 42, true)
	if sm.ReceivesUpdatesForAuthKey(raw, 42) {
		t.Fatal("ready before membership sync — a failed sync would never be retried")
	}

	sm.SetSessionChannelMemberships(raw, 42, 100, []int64{7}, sm.ChannelMembershipGeneration(raw, 42))
	if !sm.ReceivesUpdatesForAuthKey(raw, 42) {
		t.Fatal("not ready after successful membership sync")
	}

	// An account with no channels: a successful empty-list sync also counts as ready.
	sm.SetSessionChannelMemberships(raw, 42, 100, nil, sm.ChannelMembershipGeneration(raw, 42))
	if !sm.ReceivesUpdatesForAuthKey(raw, 42) {
		t.Fatal("not ready after successful empty membership sync")
	}

	// When userID does not match the connection's current binding (account-switch
	// race) it does not count as ready; retry with the correct identity.
	sm.SetSessionChannelMemberships(raw, 42, 999, []int64{7}, sm.ChannelMembershipGeneration(raw, 42))
	if sm.ReceivesUpdatesForAuthKey(raw, 42) {
		t.Fatal("ready after membership sync for mismatched user")
	}
	sm.SetSessionChannelMemberships(raw, 42, 100, []int64{7}, sm.ChannelMembershipGeneration(raw, 42))

	// Logout clears the ready flag.
	sm.BindUserForAuthKey(raw, 42, 0)
	if sm.ReceivesUpdatesForAuthKey(raw, 42) {
		t.Fatal("still ready after user unbind")
	}
}
