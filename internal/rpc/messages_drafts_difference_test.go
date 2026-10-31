package rpc

import (
	"context"
	"testing"

	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap/zaptest"

	appdialogs "telesrv/internal/app/dialogs"
	appupdates "telesrv/internal/app/updates"
	appusers "telesrv/internal/app/users"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

// Regression: cloud draft changes must be recoverable by offline devices via
// updates.getDifference (draft_message durable event, reloading the current
// authoritative state per peer on replay).

func newDraftDifferenceTestRouter(t *testing.T) (*Router, domain.User, domain.User) {
	t.Helper()
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, _ := userStore.Create(ctx, domain.User{AccessHash: 21, Phone: "15550008801", FirstName: "Owner"})
	friend, _ := userStore.Create(ctx, domain.User{AccessHash: 22, Phone: "15550008802", FirstName: "Friend"})
	dialogStore := memory.NewDialogStore()
	r := New(Config{DC: 2, IP: "127.0.0.1", Port: 2398}, Deps{
		Users:    appusers.NewService(userStore),
		Dialogs:  appdialogs.NewService(dialogStore, memory.NewChannelStore()),
		Updates:  appupdates.NewService(memory.NewUpdateStateStore(), memory.NewUpdateEventStore()),
		Sessions: &captureSessions{},
	}, zaptest.NewLogger(t), clock.System)
	return r, owner, friend
}

func draftUpdatesFromDifference(t *testing.T, diff tg.UpdatesDifferenceClass) []*tg.UpdateDraftMessage {
	t.Helper()
	full, ok := diff.(*tg.UpdatesDifference)
	if !ok {
		t.Fatalf("expected *tg.UpdatesDifference, got %T", diff)
	}
	out := make([]*tg.UpdateDraftMessage, 0, 1)
	for _, u := range full.OtherUpdates {
		if dm, ok := u.(*tg.UpdateDraftMessage); ok {
			out = append(out, dm)
		}
	}
	return out
}

func TestDraftMessageDifferenceReplay(t *testing.T) {
	ctx := context.Background()
	r, owner, friend := newDraftDifferenceTestRouter(t)
	ownerCtx := WithUserID(ctx, owner.ID)
	peer := &tg.InputPeerUser{UserID: friend.ID, AccessHash: friend.AccessHash}

	// Device A saves a draft.
	saveReq := &tg.MessagesSaveDraftRequest{Peer: peer, Message: "typing offline sync"}
	if _, err := r.onMessagesSaveDraft(ownerCtx, saveReq); err != nil {
		t.Fatalf("saveDraft: %v", err)
	}

	// An offline device pulls the difference from pts=0: it must get
	// updateDraftMessage (carrying the current authoritative draft).
	diff, err := r.onUpdatesGetDifference(ownerCtx, &tg.UpdatesGetDifferenceRequest{Pts: 0})
	if err != nil {
		t.Fatalf("getDifference: %v", err)
	}
	drafts := draftUpdatesFromDifference(t, diff)
	if len(drafts) != 1 {
		t.Fatalf("difference draft updates = %d, want 1", len(drafts))
	}
	dm, ok := drafts[0].Draft.(*tg.DraftMessage)
	if !ok {
		t.Fatalf("difference draft = %T, want *tg.DraftMessage", drafts[0].Draft)
	}
	if dm.Message != "typing offline sync" {
		t.Fatalf("difference draft message = %q, want saved text", dm.Message)
	}
	if peerUser, ok := drafts[0].Peer.(*tg.PeerUser); !ok || peerUser.UserID != friend.ID {
		t.Fatalf("difference draft peer = %#v, want friend", drafts[0].Peer)
	}
	full := diff.(*tg.UpdatesDifference)
	if full.State.Pts < 1 {
		t.Fatalf("difference state pts = %d, want advanced by draft event", full.State.Pts)
	}
	ptsAfterSave := full.State.Pts

	// Clear the draft (empty saveDraft) -> difference replay becomes draftMessageEmpty.
	if _, err := r.onMessagesSaveDraft(ownerCtx, &tg.MessagesSaveDraftRequest{Peer: peer}); err != nil {
		t.Fatalf("clear draft: %v", err)
	}
	diff, err = r.onUpdatesGetDifference(ownerCtx, &tg.UpdatesGetDifferenceRequest{Pts: ptsAfterSave})
	if err != nil {
		t.Fatalf("getDifference after clear: %v", err)
	}
	drafts = draftUpdatesFromDifference(t, diff)
	if len(drafts) != 1 {
		t.Fatalf("difference draft updates after clear = %d, want 1", len(drafts))
	}
	if _, ok := drafts[0].Draft.(*tg.DraftMessageEmpty); !ok {
		t.Fatalf("difference draft after clear = %T, want DraftMessageEmpty", drafts[0].Draft)
	}

	// Historical event replay also renders from the "current authoritative state":
	// re-pull from 0 and both events should be empty (the draft was deleted).
	diff, err = r.onUpdatesGetDifference(ownerCtx, &tg.UpdatesGetDifferenceRequest{Pts: 0})
	if err != nil {
		t.Fatalf("getDifference replay: %v", err)
	}
	for _, dm := range draftUpdatesFromDifference(t, diff) {
		if _, ok := dm.Draft.(*tg.DraftMessageEmpty); !ok {
			t.Fatalf("replayed draft = %T, want DraftMessageEmpty (authority deleted)", dm.Draft)
		}
	}
}
