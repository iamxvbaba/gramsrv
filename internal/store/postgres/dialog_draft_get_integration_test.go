package postgres

import (
	"context"
	"testing"
	"time"

	"telesrv/internal/domain"
)

// TestDialogDraftGetRoundTrip regresses: GetDraft (draft_message event replay reloading the
// authoritative state per peer) has the same key semantics as SaveDraft/DeleteDraft
// (user, peer, top_message_id).
func TestDialogDraftGetRoundTrip(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)
	owner, err := NewUserStore(pool).Create(ctx, domain.User{AccessHash: 31, Phone: "+1777" + suffix + "01", FirstName: "DraftOwner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	userID := owner.ID
	peer := domain.Peer{Type: domain.PeerTypeUser, ID: userID + 1}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DELETE FROM dialog_drafts WHERE user_id = $1", userID)
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = $1", userID)
	})

	dialogs := NewDialogStore(pool)
	if _, found, err := dialogs.GetDraft(ctx, userID, peer, 0); err != nil || found {
		t.Fatalf("get missing draft = found %v err %v, want absent", found, err)
	}
	saved := domain.DialogDraft{Peer: peer, Message: "pg roundtrip", Date: int(time.Now().Unix())}
	if err := dialogs.SaveDraft(ctx, userID, saved); err != nil {
		t.Fatalf("save draft: %v", err)
	}
	got, found, err := dialogs.GetDraft(ctx, userID, peer, 0)
	if err != nil || !found {
		t.Fatalf("get draft = found %v err %v, want present", found, err)
	}
	if got.Message != "pg roundtrip" || got.Peer != peer {
		t.Fatalf("draft = %+v, want saved payload", got)
	}
	if _, err := dialogs.DeleteDraft(ctx, userID, peer, 0); err != nil {
		t.Fatalf("delete draft: %v", err)
	}
	if _, found, err := dialogs.GetDraft(ctx, userID, peer, 0); err != nil || found {
		t.Fatalf("get deleted draft = found %v err %v, want absent", found, err)
	}
}

func TestListDialogDraftsByPeersMatchesCompositePeerKeys(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)
	owner, err := NewUserStore(pool).Create(ctx, domain.User{AccessHash: 41, Phone: "+1888" + suffix + "01", FirstName: "DraftPageOwner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	userID := owner.ID
	requestedUser := domain.Peer{Type: domain.PeerTypeUser, ID: userID + 1}
	requestedChannel := domain.Peer{Type: domain.PeerTypeChannel, ID: userID + 2}
	crossProductPeer := domain.Peer{Type: domain.PeerTypeChannel, ID: requestedUser.ID}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DELETE FROM dialog_drafts WHERE user_id = $1", userID)
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = $1", userID)
	})

	dialogs := NewDialogStore(pool)
	for _, draft := range []domain.DialogDraft{
		{Peer: requestedUser, Message: "requested user", Date: 101},
		{Peer: requestedChannel, Message: "requested channel", Date: 102},
		{Peer: crossProductPeer, Message: "must not leak", Date: 103},
		{Peer: requestedUser, TopMessageID: 77, Message: "topic draft", Date: 104},
	} {
		if err := dialogs.SaveDraft(ctx, userID, draft); err != nil {
			t.Fatalf("save draft %+v: %v", draft.Peer, err)
		}
	}

	got, err := dialogs.ListDraftsByPeers(ctx, userID, []domain.Peer{requestedUser, requestedChannel, requestedUser})
	if err != nil {
		t.Fatalf("ListDraftsByPeers: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("drafts = %+v, want exactly two requested top-level drafts", got)
	}
	want := map[domain.Peer]string{requestedUser: "requested user", requestedChannel: "requested channel"}
	for _, draft := range got {
		if want[draft.Peer] != draft.Message {
			t.Fatalf("unexpected draft = %+v", draft)
		}
		delete(want, draft.Peer)
	}
	if len(want) != 0 {
		t.Fatalf("missing drafts = %+v", want)
	}
}
