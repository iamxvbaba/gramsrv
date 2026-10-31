package updates

import (
	"context"
	"testing"

	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

func seedEvent(t *testing.T, events *memory.UpdateEventStore, userID int64, pts int) {
	t.Helper()
	if err := events.Append(context.Background(), userID, domain.UpdateEvent{
		Type:     domain.UpdateEventNewMessage,
		Pts:      pts,
		PtsCount: 1,
		Date:     1700000000 + pts,
		Message: domain.Message{
			ID:          pts,
			OwnerUserID: userID,
			Peer:        domain.Peer{Type: domain.PeerTypeUser, ID: domain.OfficialSystemUserID},
			From:        domain.Peer{Type: domain.PeerTypeUser, ID: domain.OfficialSystemUserID},
		},
	}); err != nil {
		t.Fatalf("seed event pts=%d: %v", pts, err)
	}
}

// TestGetDifferenceStopsAtHole: getDifference returns only the contiguous events from
// from onward, truncating at an in-flight hole; State.Pts takes the last contiguous
// value; once the later events arrive, the next fetch can continue, never skipping the hole.
func TestGetDifferenceStopsAtHole(t *testing.T) {
	ctx := context.Background()
	var authKeyID [8]byte
	userID := int64(1000000001)
	events := memory.NewUpdateEventStore()
	svc := NewService(memory.NewUpdateStateStore(), events)

	// pts 1,2,3 committed, 4 in-flight (missing), 5,6 committed → contiguous prefix stops at 3.
	for _, p := range []int{1, 2, 3, 5, 6} {
		seedEvent(t, events, userID, p)
	}
	diff, err := svc.GetDifference(ctx, authKeyID, userID, domain.UpdateState{})
	if err != nil {
		t.Fatalf("GetDifference: %v", err)
	}
	if len(diff.Events) != 3 || diff.State.Pts != 3 {
		t.Fatalf("diff = %d events, state.pts %d; want 3 contiguous events, stopping at the hole (pts=3)", len(diff.Events), diff.State.Pts)
	}
	if diff.Partial {
		t.Fatalf("Partial=true, want false (truncated by a hole, not by limit)")
	}

	// After filling in pts=4 (the in-flight transaction commits), continuing from 3 should yield 4,5,6.
	seedEvent(t, events, userID, 4)
	diff, err = svc.GetDifference(ctx, authKeyID, userID, domain.UpdateState{Pts: 3})
	if err != nil {
		t.Fatalf("GetDifference after fill: %v", err)
	}
	if len(diff.Events) != 3 || diff.State.Pts != 6 {
		t.Fatalf("events after gap filled = %d, state.pts %d; want 4,5,6 through pts=6", len(diff.Events), diff.State.Pts)
	}
}

// TestGetDifferenceSliceOnLimit: when contiguous events fill the limit, returns
// Partial (=differenceSlice) with State.Pts as the intermediate state; the client
// uses it to continue fetching until the final page has Partial=false.
func TestGetDifferenceSliceOnLimit(t *testing.T) {
	ctx := context.Background()
	var authKeyID [8]byte
	userID := int64(1000000002)
	events := memory.NewUpdateEventStore()
	svc := NewService(memory.NewUpdateStateStore(), events)

	total := getDifferenceLimit + 25
	for p := 1; p <= total; p++ {
		seedEvent(t, events, userID, p)
	}
	diff, err := svc.GetDifference(ctx, authKeyID, userID, domain.UpdateState{})
	if err != nil {
		t.Fatalf("GetDifference: %v", err)
	}
	if len(diff.Events) != getDifferenceLimit || !diff.Partial || diff.State.Pts != getDifferenceLimit {
		t.Fatalf("first page = %d events partial %v state.pts %d; want %d/true/%d",
			len(diff.Events), diff.Partial, diff.State.Pts, getDifferenceLimit, getDifferenceLimit)
	}

	diff, err = svc.GetDifference(ctx, authKeyID, userID, domain.UpdateState{Pts: getDifferenceLimit})
	if err != nil {
		t.Fatalf("GetDifference page2: %v", err)
	}
	if len(diff.Events) != 25 || diff.Partial || diff.State.Pts != total {
		t.Fatalf("second page = %d events partial %v state.pts %d; want 25/false/%d",
			len(diff.Events), diff.Partial, diff.State.Pts, total)
	}
}

// TestGetStateReportsContiguousNotMax: getState reports the maximum contiguous pts,
// not the maximum committed pts, so a first-login baseline cannot jump over an
// in-flight hole and lose messages.
func TestGetStateReportsContiguousNotMax(t *testing.T) {
	ctx := context.Background()
	var authKeyID [8]byte
	userID := int64(1000000003)
	events := memory.NewUpdateEventStore()
	svc := NewService(memory.NewUpdateStateStore(), events)

	for _, p := range []int{1, 2, 3, 5, 6} { // 4 is an in-flight hole, max committed=6
		seedEvent(t, events, userID, p)
	}
	st, err := svc.GetState(ctx, authKeyID, userID)
	if err != nil {
		t.Fatalf("GetState: %v", err)
	}
	if st.Pts != 3 {
		t.Fatalf("GetState.Pts=%d, want 3 (max contiguous, not max committed 6)", st.Pts)
	}
}

func TestGetStateDoesNotConfirmUnfetchedEvents(t *testing.T) {
	ctx := context.Background()
	var authKeyID [8]byte
	authKeyID[0] = 7
	userID := int64(1000000004)
	events := memory.NewUpdateEventStore()
	states := memory.NewUpdateStateStore()
	svc := NewService(states, events)

	seedEvent(t, events, userID, 1)
	if err := states.Save(ctx, authKeyID, userID, domain.UpdateState{Pts: 1, Date: 1700000001}); err != nil {
		t.Fatalf("Save state: %v", err)
	}
	seedEvent(t, events, userID, 2)

	st, err := svc.GetState(ctx, authKeyID, userID)
	if err != nil {
		t.Fatalf("GetState: %v", err)
	}
	if st.Pts != 1 {
		t.Fatalf("GetState.Pts=%d, want existing confirmed pts=1", st.Pts)
	}

	diff, err := svc.GetDifference(ctx, authKeyID, userID, st)
	if err != nil {
		t.Fatalf("GetDifference: %v", err)
	}
	if len(diff.Events) != 1 || diff.Events[0].Pts != 2 || diff.State.Pts != 2 {
		t.Fatalf("diff = %+v, want one event at pts=2 and confirmed state pts=2", diff)
	}
	st, err = svc.GetState(ctx, authKeyID, userID)
	if err != nil {
		t.Fatalf("GetState after difference: %v", err)
	}
	if st.Pts != 1 {
		t.Fatalf("GetState before delivery commit pts=%d, want confirmed pts=1", st.Pts)
	}
	if err := svc.CommitDeliveredState(ctx, authKeyID, userID, diff.State, domain.UpdateStateCommitDeliveredOnly); err != nil {
		t.Fatalf("CommitDeliveredState: %v", err)
	}
	st, err = svc.GetState(ctx, authKeyID, userID)
	if err != nil || st.Pts != 2 {
		t.Fatalf("GetState after delivery commit = %+v err=%v, want pts=2", st, err)
	}
}
