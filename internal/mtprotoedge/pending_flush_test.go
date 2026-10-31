package mtprotoedge

import (
	"context"
	"testing"
	"time"

	"github.com/iamxvbaba/td/mt"
	"github.com/iamxvbaba/td/proto"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tlprofile"
)

// TestSetReceivesUpdatesFlushesPendingBeforeActivation verifies that on setting
// the flag the staged pushes are drained before receivesUpdates is activated: the
// client must actually receive the staged messages, and activation must
// eventually complete. On drain failure receivesUpdates stays false and the
// staged data is requeued, retried on the next flag set.
func TestSetReceivesUpdatesFlushesPendingBeforeActivation(t *testing.T) {
	const dc = 2
	addr, pub, srv := startTestServer(t, Options{DC: dc})
	conn, auth, cipher := dialHandshake(t, addr, dc, pub)

	clientMsgID := proto.NewMessageIDGen(time.Now)
	sendEncryptedWithSeq(t, conn, cipher, auth, clientMsgID.New(proto.MessageFromClient), 1, &tg.HelpGetConfigRequest{})
	collectReplies(t, conn, cipher, auth.AuthKey, mt.MsgsAckTypeID)

	raw := auth.AuthKey.ID
	freezeActiveTestSessionProfile(t, srv.Conns(), raw, auth.SessionID, tlprofile.ProfileCanonical)
	ctx := context.Background()

	// Full readiness also requires the membership route to be established (the other half of ReceivesUpdatesForAuthKey).
	srv.Conns().BindUserForAuthKey(raw, auth.SessionID, 100)
	srv.Conns().SetSessionChannelMemberships(raw, auth.SessionID, 100, nil, srv.Conns().ChannelMembershipGeneration(raw, auth.SessionID))

	// Not ready: pushes go into pending rather than being sent directly.
	for i := 0; i < 2; i++ {
		if err := srv.Conns().PushToSessionForAuthKey(ctx, raw, auth.SessionID, proto.MessageFromServer, &tg.UpdatesTooLong{}); err != nil {
			t.Fatalf("queue pending push: %v", err)
		}
	}
	if srv.Conns().ReceivesUpdatesForAuthKey(raw, auth.SessionID) {
		t.Fatal("session ready before activation")
	}

	srv.Conns().SetReceivesUpdatesForAuthKey(raw, auth.SessionID, true)

	// The client should receive the staged updatesTooLong (flushed directly).
	replies := collectReplies(t, conn, cipher, auth.AuthKey, tg.UpdatesTooLongTypeID)
	mustHave(t, replies, tg.UpdatesTooLongTypeID, "flushed pending push")

	// receivesUpdates is set only after the drain completes (asynchronous, poll and wait).
	deadline := time.Now().Add(5 * time.Second)
	for !srv.Conns().ReceivesUpdatesForAuthKey(raw, auth.SessionID) {
		if time.Now().After(deadline) {
			t.Fatal("session never became ready after flush")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
