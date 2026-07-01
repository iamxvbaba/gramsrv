package rpc

import (
	"context"
	"math"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
	"go.uber.org/zap/zaptest"

	appchannels "telesrv/internal/app/channels"
	appcontacts "telesrv/internal/app/contacts"
	appprivacy "telesrv/internal/app/privacy"
	appstories "telesrv/internal/app/stories"
	appupdates "telesrv/internal/app/updates"
	appusers "telesrv/internal/app/users"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

func TestStoriesGetPeerStoriesReturnsStoredStory(t *testing.T) {
	ctx := context.Background()
	storyStore := memory.NewStoryStore()
	owner := domain.Peer{Type: domain.PeerTypeUser, ID: 1000000001}
	if _, err := storyStore.UpsertStory(ctx, domain.UpsertStoryRequest{Story: domain.Story{
		Owner:      owner,
		ID:         1,
		Date:       1700000000,
		ExpireDate: 1700003600,
		Public:     true,
		Caption:    "hello story",
	}}); err != nil {
		t.Fatalf("upsert story: %v", err)
	}
	r := New(Config{}, Deps{Stories: appstories.NewService(storyStore)}, zaptest.NewLogger(t), fixedClock{now: time.Unix(1700000100, 0)})

	got, err := r.onStoriesGetPeerStories(WithUserID(ctx, 1000000002), &tg.InputPeerUser{UserID: owner.ID})
	if err != nil {
		t.Fatalf("get peer stories: %v", err)
	}
	if got.Stories.Peer.(*tg.PeerUser).UserID != owner.ID || len(got.Stories.Stories) != 1 {
		t.Fatalf("peer stories = %+v, want one story for owner", got.Stories)
	}
	item, ok := got.Stories.Stories[0].(*tg.StoryItem)
	if !ok {
		t.Fatalf("story item = %T, want *tg.StoryItem", got.Stories.Stories[0])
	}
	if item.ID != 1 || item.Caption != "hello story" {
		t.Fatalf("story item = %+v, want id 1 caption", item)
	}
}

func TestStoriesGetAllStoriesStateNotModifiedAndBounds(t *testing.T) {
	ctx := context.Background()
	storyStore := memory.NewStoryStore()
	owner := domain.Peer{Type: domain.PeerTypeUser, ID: 1000000001}
	if _, err := storyStore.UpsertStory(ctx, domain.UpsertStoryRequest{Story: domain.Story{
		Owner:      owner,
		ID:         1,
		Date:       1700000000,
		ExpireDate: 1700003600,
		Public:     true,
		Caption:    "first story",
	}}); err != nil {
		t.Fatalf("upsert first story: %v", err)
	}
	r := New(Config{}, Deps{Stories: appstories.NewService(storyStore)}, zaptest.NewLogger(t), fixedClock{now: time.Unix(1700000100, 0)})
	reqCtx := WithUserID(ctx, 1000000002)

	if _, err := r.onStoriesGetAllStories(ctx, nil); err == nil || !tgerr.Is(err, "INPUT_REQUEST_INVALID") {
		t.Fatalf("nil get all stories without user ctx err = %v, want INPUT_REQUEST_INVALID", err)
	}

	firstClass, err := r.onStoriesGetAllStories(reqCtx, &tg.StoriesGetAllStoriesRequest{})
	if err != nil {
		t.Fatalf("get all stories first: %v", err)
	}
	first, ok := firstClass.(*tg.StoriesAllStories)
	if !ok {
		t.Fatalf("first all stories = %T, want stories.allStories", firstClass)
	}
	if first.State == "" || len(first.PeerStories) != 1 || len(first.PeerStories[0].Stories) != 1 {
		t.Fatalf("first all stories = %+v, want one peer story and non-empty state", first)
	}

	sameReq := &tg.StoriesGetAllStoriesRequest{}
	sameReq.SetState(first.State)
	sameClass, err := r.onStoriesGetAllStories(reqCtx, sameReq)
	if err != nil {
		t.Fatalf("get all stories same state: %v", err)
	}
	notModified, ok := sameClass.(*tg.StoriesAllStoriesNotModified)
	if !ok || notModified.State != first.State {
		t.Fatalf("same state response = %T %+v, want notModified with original state", sameClass, sameClass)
	}

	readIDs, err := r.onStoriesReadStories(reqCtx, &tg.StoriesReadStoriesRequest{
		Peer:  &tg.InputPeerUser{UserID: owner.ID},
		MaxID: 1,
	})
	if err != nil {
		t.Fatalf("read stories: %v", err)
	}
	if len(readIDs) != 1 || readIDs[0] != 1 {
		t.Fatalf("read stories ids = %v, want [1]", readIDs)
	}
	readChangedClass, err := r.onStoriesGetAllStories(reqCtx, sameReq)
	if err != nil {
		t.Fatalf("get all stories after read boundary change: %v", err)
	}
	readChanged, ok := readChangedClass.(*tg.StoriesAllStories)
	if !ok {
		t.Fatalf("read boundary response = %T, want stories.allStories", readChangedClass)
	}
	if readChanged.State == "" || readChanged.State == first.State || len(readChanged.PeerStories) != 1 {
		t.Fatalf("read boundary all stories = %+v, want one peer story and new state", readChanged)
	}
	if readChanged.PeerStories[0].MaxReadID != 1 {
		t.Fatalf("read boundary max_read_id = %d, want 1", readChanged.PeerStories[0].MaxReadID)
	}
	sameReq.SetState(readChanged.State)
	readSameClass, err := r.onStoriesGetAllStories(reqCtx, sameReq)
	if err != nil {
		t.Fatalf("get all stories same read state: %v", err)
	}
	readNotModified, ok := readSameClass.(*tg.StoriesAllStoriesNotModified)
	if !ok || readNotModified.State != readChanged.State {
		t.Fatalf("same read state response = %T %+v, want notModified with read state", readSameClass, readSameClass)
	}

	if _, err := storyStore.UpsertStory(ctx, domain.UpsertStoryRequest{Story: domain.Story{
		Owner:      owner,
		ID:         2,
		Date:       1700000001,
		ExpireDate: 1700003600,
		Public:     true,
		Caption:    "second story",
	}}); err != nil {
		t.Fatalf("upsert second story: %v", err)
	}
	changedClass, err := r.onStoriesGetAllStories(reqCtx, sameReq)
	if err != nil {
		t.Fatalf("get all stories changed state: %v", err)
	}
	changed, ok := changedClass.(*tg.StoriesAllStories)
	if !ok {
		t.Fatalf("changed state response = %T, want stories.allStories", changedClass)
	}
	if changed.State == "" || changed.State == readChanged.State || len(changed.PeerStories) != 1 || len(changed.PeerStories[0].Stories) != 2 {
		t.Fatalf("changed all stories = %+v, want two stories and new state", changed)
	}

	nextWithoutState := &tg.StoriesGetAllStoriesRequest{}
	nextWithoutState.SetNext(true)
	nextEmptyState := &tg.StoriesGetAllStoriesRequest{}
	nextEmptyState.SetNext(true)
	nextEmptyState.SetState("")
	emptyState := &tg.StoriesGetAllStoriesRequest{}
	emptyState.SetState("")
	unknownState := &tg.StoriesGetAllStoriesRequest{}
	unknownState.SetState("bogus")
	cursorWithoutNext := &tg.StoriesGetAllStoriesRequest{}
	cursorWithoutNext.SetState("tsc1:0:1700003000:user:1000000001")
	malformedComplete := &tg.StoriesGetAllStoriesRequest{}
	malformedComplete.SetState("ts1:0:not-a-count:0000000000000000")
	hiddenMismatchComplete := &tg.StoriesGetAllStoriesRequest{}
	hiddenMismatchComplete.SetHidden(true)
	hiddenMismatchComplete.SetState("ts1:0:0:0000000000000000")
	oversizedState := &tg.StoriesGetAllStoriesRequest{}
	oversizedState.SetState(strings.Repeat("x", maxStoryAllStoriesStateLength+1))
	for _, tc := range []struct {
		name string
		req  *tg.StoriesGetAllStoriesRequest
	}{
		{name: "next_without_state", req: nextWithoutState},
		{name: "next_empty_state", req: nextEmptyState},
		{name: "empty_state", req: emptyState},
		{name: "unknown_state", req: unknownState},
		{name: "cursor_without_next", req: cursorWithoutNext},
		{name: "malformed_complete", req: malformedComplete},
		{name: "hidden_mismatch_complete", req: hiddenMismatchComplete},
		{name: "oversized_state", req: oversizedState},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := r.onStoriesGetAllStories(ctx, tc.req); err == nil || !tgerr.Is(err, "OFFSET_INVALID") {
				t.Fatalf("err = %v, want OFFSET_INVALID before auth/session", err)
			}
		})
	}
}

func TestStoriesGetAllStoriesPaginatesByPeerState(t *testing.T) {
	ctx := context.Background()
	storyStore := memory.NewStoryStore()
	for i := 0; i < domain.MaxStoryListLimit+1; i++ {
		owner := domain.Peer{Type: domain.PeerTypeUser, ID: int64(1000001000 + i)}
		if _, err := storyStore.UpsertStory(ctx, domain.UpsertStoryRequest{Story: domain.Story{
			Owner:      owner,
			ID:         1,
			Date:       1700003000 - i,
			ExpireDate: 1700009000,
			Public:     true,
			Caption:    "story " + strconv.Itoa(i),
		}}); err != nil {
			t.Fatalf("upsert story %d: %v", i, err)
		}
	}
	firstOwner := domain.Peer{Type: domain.PeerTypeUser, ID: 1000001000}
	if _, err := storyStore.UpsertStory(ctx, domain.UpsertStoryRequest{Story: domain.Story{
		Owner:      firstOwner,
		ID:         2,
		Date:       1700002999,
		ExpireDate: 1700009000,
		Public:     true,
		Caption:    "same peer older story",
	}}); err != nil {
		t.Fatalf("upsert same-peer story: %v", err)
	}
	r := New(Config{}, Deps{Stories: appstories.NewService(storyStore)}, zaptest.NewLogger(t), fixedClock{now: time.Unix(1700004000, 0)})
	reqCtx := WithUserID(ctx, 2000000001)

	firstClass, err := r.onStoriesGetAllStories(reqCtx, &tg.StoriesGetAllStoriesRequest{})
	if err != nil {
		t.Fatalf("get all stories first page: %v", err)
	}
	first, ok := firstClass.(*tg.StoriesAllStories)
	if !ok {
		t.Fatalf("first page = %T, want stories.allStories", firstClass)
	}
	if !first.HasMore || first.Count != domain.MaxStoryListLimit+1 || len(first.PeerStories) != domain.MaxStoryListLimit {
		t.Fatalf("first page count/more/peers = count %d more %v peers %d", first.Count, first.HasMore, len(first.PeerStories))
	}
	if !strings.HasPrefix(first.State, "tsc1:") {
		t.Fatalf("first page state = %q, want cursor state", first.State)
	}
	if len(first.PeerStories[0].Stories) != 2 {
		t.Fatalf("first peer stories = %d, want same peer kept on one page", len(first.PeerStories[0].Stories))
	}

	nextReq := &tg.StoriesGetAllStoriesRequest{}
	nextReq.SetState(first.State)
	nextReq.SetNext(true)
	nextClass, err := r.onStoriesGetAllStories(reqCtx, nextReq)
	if err != nil {
		t.Fatalf("get all stories next page: %v", err)
	}
	next, ok := nextClass.(*tg.StoriesAllStories)
	if !ok {
		t.Fatalf("next page = %T, want stories.allStories", nextClass)
	}
	if next.HasMore || next.Count != domain.MaxStoryListLimit+1 || len(next.PeerStories) != 1 {
		t.Fatalf("next page count/more/peers = count %d more %v peers %d", next.Count, next.HasMore, len(next.PeerStories))
	}
	if !strings.HasPrefix(next.State, "ts1:") {
		t.Fatalf("next page state = %q, want final snapshot state", next.State)
	}

	finalCheck := &tg.StoriesGetAllStoriesRequest{}
	finalCheck.SetState(next.State)
	finalCheckClass, err := r.onStoriesGetAllStories(reqCtx, finalCheck)
	if err != nil {
		t.Fatalf("get all stories final state check: %v", err)
	}
	if notModified, ok := finalCheckClass.(*tg.StoriesAllStoriesNotModified); !ok || notModified.State != next.State {
		t.Fatalf("final state check = %T %+v, want notModified with final state", finalCheckClass, finalCheckClass)
	}

	newOwner := domain.Peer{Type: domain.PeerTypeUser, ID: 1000009999}
	if _, err := storyStore.UpsertStory(ctx, domain.UpsertStoryRequest{Story: domain.Story{
		Owner:      newOwner,
		ID:         1,
		Date:       1700004000,
		ExpireDate: 1700009000,
		Public:     true,
	}}); err != nil {
		t.Fatalf("upsert changed story: %v", err)
	}
	changedClass, err := r.onStoriesGetAllStories(reqCtx, finalCheck)
	if err != nil {
		t.Fatalf("get all stories changed final state: %v", err)
	}
	changed, ok := changedClass.(*tg.StoriesAllStories)
	if !ok || !changed.HasMore || changed.State == next.State {
		t.Fatalf("changed final state response = %T %+v, want refreshed first page with new cursor state", changedClass, changedClass)
	}

	wrongHidden := &tg.StoriesGetAllStoriesRequest{}
	wrongHidden.SetState(first.State)
	wrongHidden.SetNext(true)
	wrongHidden.SetHidden(true)
	if _, err := r.onStoriesGetAllStories(ctx, wrongHidden); err == nil || !tgerr.Is(err, "OFFSET_INVALID") {
		t.Fatalf("hidden/cursor mismatch err = %v, want OFFSET_INVALID", err)
	}
	finalAsCursor := &tg.StoriesGetAllStoriesRequest{}
	finalAsCursor.SetState(next.State)
	finalAsCursor.SetNext(true)
	if _, err := r.onStoriesGetAllStories(ctx, finalAsCursor); err == nil || !tgerr.Is(err, "OFFSET_INVALID") {
		t.Fatalf("final state as cursor err = %v, want OFFSET_INVALID", err)
	}
}

func TestStoriesTogglePeerStoriesHiddenProjectsAllStoriesPeerFlags(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	ownerUser, err := userStore.Create(ctx, domain.User{
		AccessHash: 12345,
		Phone:      "15550000101",
		FirstName:  "StoryOwner",
	})
	if err != nil {
		t.Fatalf("create owner user: %v", err)
	}
	owner := domain.Peer{Type: domain.PeerTypeUser, ID: ownerUser.ID}
	storyStore := memory.NewStoryStore()
	if _, err := storyStore.UpsertStory(ctx, domain.UpsertStoryRequest{Story: domain.Story{
		Owner:      owner,
		ID:         1,
		Date:       1700001000,
		ExpireDate: 1700009000,
		Public:     true,
	}}); err != nil {
		t.Fatalf("upsert story: %v", err)
	}
	r := New(Config{}, Deps{
		Stories: appstories.NewService(storyStore),
		Users:   appusers.NewService(userStore),
	}, zaptest.NewLogger(t), fixedClock{now: time.Unix(1700002000, 0)})
	reqCtx := WithUserID(ctx, ownerUser.ID+100)

	firstClass, err := r.onStoriesGetAllStories(reqCtx, &tg.StoriesGetAllStoriesRequest{})
	if err != nil {
		t.Fatalf("get normal stories: %v", err)
	}
	first := mustAllStories(t, firstClass)
	if got := storiesHiddenForUser(t, first.Users, ownerUser.ID); got {
		t.Fatalf("normal allStories user stories_hidden = true, want false")
	}

	if _, err := r.onStoriesTogglePeerStoriesHidden(reqCtx, nil); err == nil || !tgerr.Is(err, "INPUT_REQUEST_INVALID") {
		t.Fatalf("nil toggle peer stories hidden err = %v, want INPUT_REQUEST_INVALID", err)
	}

	ok, err := r.onStoriesTogglePeerStoriesHidden(reqCtx, &tg.StoriesTogglePeerStoriesHiddenRequest{
		Peer:   &tg.InputPeerUser{UserID: ownerUser.ID},
		Hidden: true,
	})
	if err != nil || !ok {
		t.Fatalf("toggle hidden = %v, %v", ok, err)
	}
	hiddenReq := &tg.StoriesGetAllStoriesRequest{}
	hiddenReq.SetHidden(true)
	hiddenClass, err := r.onStoriesGetAllStories(reqCtx, hiddenReq)
	if err != nil {
		t.Fatalf("get hidden stories: %v", err)
	}
	hidden := mustAllStories(t, hiddenClass)
	if len(hidden.PeerStories) != 1 {
		t.Fatalf("hidden peer stories = %d, want 1", len(hidden.PeerStories))
	}
	if got := storiesHiddenForUser(t, hidden.Users, ownerUser.ID); !got {
		t.Fatalf("hidden allStories user stories_hidden = false, want true")
	}

	staleReq := &tg.StoriesGetAllStoriesRequest{}
	staleReq.SetState(first.State)
	normalAfterHideClass, err := r.onStoriesGetAllStories(reqCtx, staleReq)
	if err != nil {
		t.Fatalf("get normal stories with stale state: %v", err)
	}
	normalAfterHide := mustAllStories(t, normalAfterHideClass)
	if normalAfterHide.State == first.State || len(normalAfterHide.PeerStories) != 0 {
		t.Fatalf("normal after hide = state %q peers %d, want changed empty list", normalAfterHide.State, len(normalAfterHide.PeerStories))
	}

	ok, err = r.onStoriesTogglePeerStoriesHidden(reqCtx, &tg.StoriesTogglePeerStoriesHiddenRequest{
		Peer:   &tg.InputPeerUser{UserID: ownerUser.ID},
		Hidden: false,
	})
	if err != nil || !ok {
		t.Fatalf("toggle unhidden = %v, %v", ok, err)
	}
	unhiddenClass, err := r.onStoriesGetAllStories(reqCtx, &tg.StoriesGetAllStoriesRequest{})
	if err != nil {
		t.Fatalf("get unhidden stories: %v", err)
	}
	unhidden := mustAllStories(t, unhiddenClass)
	if len(unhidden.PeerStories) != 1 {
		t.Fatalf("unhidden peer stories = %d, want 1", len(unhidden.PeerStories))
	}
	if got := storiesHiddenForUser(t, unhidden.Users, ownerUser.ID); got {
		t.Fatalf("unhidden allStories user stories_hidden = true, want false")
	}
}

func mustAllStories(t *testing.T, got tg.StoriesAllStoriesClass) *tg.StoriesAllStories {
	t.Helper()
	out, ok := got.(*tg.StoriesAllStories)
	if !ok {
		t.Fatalf("allStories response = %T, want *tg.StoriesAllStories", got)
	}
	return out
}

func storiesHiddenForUser(t *testing.T, users []tg.UserClass, userID int64) bool {
	t.Helper()
	item := findUserClass(users, userID)
	if item == nil {
		t.Fatalf("user %d missing from companion users", userID)
	}
	user, ok := item.(*tg.User)
	if !ok {
		t.Fatalf("user %d companion = %T, want *tg.User", userID, item)
	}
	return user.StoriesHidden
}

func TestStoriesReadStoriesRecordsDifferenceUpdate(t *testing.T) {
	ctx := context.Background()
	authKeyID := [8]byte{1, 2, 3}
	storyStore := memory.NewStoryStore()
	updateStore := memory.NewUpdateEventStore()
	owner := domain.Peer{Type: domain.PeerTypeUser, ID: 1000000001}
	if _, err := storyStore.UpsertStory(ctx, domain.UpsertStoryRequest{Story: domain.Story{
		Owner:      owner,
		ID:         3,
		Date:       1700000000,
		ExpireDate: 1700003600,
		Public:     true,
	}}); err != nil {
		t.Fatalf("upsert story: %v", err)
	}
	r := New(Config{}, Deps{
		Stories: appstories.NewService(storyStore),
		Updates: appupdates.NewService(memory.NewUpdateStateStore(), updateStore),
	}, zaptest.NewLogger(t), fixedClock{now: time.Unix(1700000100, 0)})

	readCtx := WithSessionID(WithAuthKeyID(WithUserID(ctx, 1000000002), authKeyID), 77)
	ids, err := r.onStoriesReadStories(readCtx, &tg.StoriesReadStoriesRequest{
		Peer:  &tg.InputPeerUser{UserID: owner.ID},
		MaxID: 3,
	})
	if err != nil {
		t.Fatalf("read stories: %v", err)
	}
	if len(ids) != 1 || ids[0] != 3 {
		t.Fatalf("read ids = %v, want [3]", ids)
	}
	diff, err := r.onUpdatesGetDifference(readCtx, &tg.UpdatesGetDifferenceRequest{Pts: 0})
	if err != nil {
		t.Fatalf("get difference: %v", err)
	}
	got, ok := diff.(*tg.UpdatesDifference)
	if !ok {
		t.Fatalf("difference = %T, want *tg.UpdatesDifference", diff)
	}
	if got.State.Pts != 1 || len(got.OtherUpdates) != 1 {
		t.Fatalf("difference state/updates = pts %d updates %d, want pts 1 and read update", got.State.Pts, len(got.OtherUpdates))
	}
	readUpdate, ok := got.OtherUpdates[0].(*tg.UpdateReadStories)
	if !ok {
		t.Fatalf("first update = %T, want *tg.UpdateReadStories", got.OtherUpdates[0])
	}
	if readUpdate.MaxID != 3 {
		t.Fatalf("read update max_id = %d, want 3", readUpdate.MaxID)
	}
	retryIDs, err := r.onStoriesReadStories(readCtx, &tg.StoriesReadStoriesRequest{
		Peer:  &tg.InputPeerUser{UserID: owner.ID},
		MaxID: 3,
	})
	if err != nil {
		t.Fatalf("retry read stories: %v", err)
	}
	if len(retryIDs) != 1 || retryIDs[0] != 3 {
		t.Fatalf("retry read ids = %v, want current boundary [3]", retryIDs)
	}
	lowerIDs, err := r.onStoriesReadStories(readCtx, &tg.StoriesReadStoriesRequest{
		Peer:  &tg.InputPeerUser{UserID: owner.ID},
		MaxID: 2,
	})
	if err != nil {
		t.Fatalf("lower read stories: %v", err)
	}
	if len(lowerIDs) != 1 || lowerIDs[0] != 3 {
		t.Fatalf("lower read ids = %v, want current boundary [3]", lowerIDs)
	}
	events, err := updateStore.ListAfter(ctx, 1000000002, 0, 10)
	if err != nil {
		t.Fatalf("list read story events: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("read story events after retry/lower reads = %d, want only initial advancement", len(events))
	}
}

func TestStoriesReadAndReactionEventsExcludeCurrentSession(t *testing.T) {
	ctx := context.Background()
	authKeyID := [8]byte{1, 2, 7}
	const sessionID int64 = 2277
	userStore := memory.NewUserStore()
	ownerUser, err := userStore.Create(ctx, domain.User{AccessHash: 90011, Phone: "15550090011", FirstName: "Owner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	viewer, err := userStore.Create(ctx, domain.User{AccessHash: 90012, Phone: "15550090012", FirstName: "Viewer"})
	if err != nil {
		t.Fatalf("create viewer: %v", err)
	}
	creator, err := userStore.Create(ctx, domain.User{AccessHash: 90013, Phone: "15550090013", FirstName: "Creator"})
	if err != nil {
		t.Fatalf("create creator: %v", err)
	}
	channelStore := memory.NewChannelStore()
	channelService := appchannels.NewService(channelStore)
	created, err := channelService.CreateChannel(ctx, creator.ID, domain.CreateChannelRequest{
		CreatorUserID: creator.ID,
		Title:         "story reaction session channel",
		Broadcast:     true,
		MemberUserIDs: []int64{viewer.ID},
		Date:          1700000210,
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	storyStore := memory.NewStoryStore()
	userPeer := domain.Peer{Type: domain.PeerTypeUser, ID: ownerUser.ID}
	if _, err := storyStore.UpsertStory(ctx, domain.UpsertStoryRequest{Story: domain.Story{
		Owner:      userPeer,
		ID:         3,
		Date:       1700000200,
		ExpireDate: 1700003600,
		Public:     true,
	}}); err != nil {
		t.Fatalf("upsert user story: %v", err)
	}
	channelPeer := domain.Peer{Type: domain.PeerTypeChannel, ID: created.Channel.ID}
	if _, err := storyStore.UpsertStory(ctx, domain.UpsertStoryRequest{Story: domain.Story{
		Owner:      channelPeer,
		ID:         1,
		Date:       1700000201,
		ExpireDate: 1700003600,
		Public:     true,
	}}); err != nil {
		t.Fatalf("upsert channel story: %v", err)
	}
	updates := &captureUpdates{}
	r := New(Config{}, Deps{
		Users:    appusers.NewService(userStore),
		Channels: channelService,
		Stories:  appstories.NewService(storyStore),
		Updates:  updates,
	}, zaptest.NewLogger(t), fixedClock{now: time.Unix(1700000220, 0)})
	reqCtx := WithSessionID(WithAuthKeyID(WithUserID(ctx, viewer.ID), authKeyID), sessionID)

	if ids, err := r.onStoriesReadStories(reqCtx, &tg.StoriesReadStoriesRequest{
		Peer:  &tg.InputPeerUser{UserID: ownerUser.ID, AccessHash: ownerUser.AccessHash},
		MaxID: 3,
	}); err != nil {
		t.Fatalf("read stories: %v", err)
	} else if len(ids) != 1 || ids[0] != 3 {
		t.Fatalf("read ids = %v, want [3]", ids)
	}
	if updates.authKeyID != authKeyID || updates.userID != viewer.ID || updates.excludeSessionID != sessionID {
		t.Fatalf("read captured update = auth %v user %d exclude_session %d, want auth %v user %d exclude_session %d", updates.authKeyID, updates.userID, updates.excludeSessionID, authKeyID, viewer.ID, sessionID)
	}
	if len(updates.events) != 1 || updates.events[0].Type != domain.UpdateEventReadStories || updates.events[0].Peer != userPeer || updates.events[0].MaxID != 3 {
		t.Fatalf("read captured events = %+v, want readStories max_id 3", updates.events)
	}

	updates.events = nil
	if _, err := r.onStoriesSendReaction(reqCtx, &tg.StoriesSendReactionRequest{
		Peer:     &tg.InputPeerChannel{ChannelID: created.Channel.ID, AccessHash: created.Channel.AccessHash},
		StoryID:  1,
		Reaction: &tg.ReactionEmoji{Emoticon: "🔥"},
	}); err != nil {
		t.Fatalf("send channel story reaction: %v", err)
	}
	if updates.authKeyID != authKeyID || updates.userID != viewer.ID || updates.excludeSessionID != sessionID {
		t.Fatalf("reaction captured update = auth %v user %d exclude_session %d, want auth %v user %d exclude_session %d", updates.authKeyID, updates.userID, updates.excludeSessionID, authKeyID, viewer.ID, sessionID)
	}
	if len(updates.events) != 1 || updates.events[0].Type != domain.UpdateEventSentStoryReaction || updates.events[0].Peer != channelPeer || updates.events[0].MaxID != 1 {
		t.Fatalf("reaction captured events = %+v, want sentStoryReaction for channel story 1", updates.events)
	}
	if updates.events[0].Reaction == nil || updates.events[0].Reaction.Emoticon != "🔥" {
		t.Fatalf("reaction captured event = %+v, want fire reaction", updates.events[0])
	}
}

func TestStoriesReadStoriesClampsFutureMaxID(t *testing.T) {
	ctx := context.Background()
	authKeyID := [8]byte{4, 5, 6}
	storyStore := memory.NewStoryStore()
	owner := domain.Peer{Type: domain.PeerTypeUser, ID: 1000000001}
	for _, id := range []int{1, 2} {
		if _, err := storyStore.UpsertStory(ctx, domain.UpsertStoryRequest{Story: domain.Story{
			Owner:      owner,
			ID:         id,
			Date:       1700000000 + id,
			ExpireDate: 1700003600,
			Public:     true,
		}}); err != nil {
			t.Fatalf("upsert story %d: %v", id, err)
		}
	}
	r := New(Config{}, Deps{
		Stories: appstories.NewService(storyStore),
		Updates: appupdates.NewService(memory.NewUpdateStateStore(), memory.NewUpdateEventStore()),
	}, zaptest.NewLogger(t), fixedClock{now: time.Unix(1700000100, 0)})

	readCtx := WithSessionID(WithAuthKeyID(WithUserID(ctx, 1000000002), authKeyID), 88)
	ids, err := r.onStoriesReadStories(readCtx, &tg.StoriesReadStoriesRequest{
		Peer:  &tg.InputPeerUser{UserID: owner.ID},
		MaxID: 99,
	})
	if err != nil {
		t.Fatalf("read future max id: %v", err)
	}
	if len(ids) != 1 || ids[0] != 2 {
		t.Fatalf("read ids = %v, want [2]", ids)
	}
	diff, err := r.onUpdatesGetDifference(readCtx, &tg.UpdatesGetDifferenceRequest{Pts: 0})
	if err != nil {
		t.Fatalf("get difference: %v", err)
	}
	got, ok := diff.(*tg.UpdatesDifference)
	if !ok {
		t.Fatalf("difference = %T, want *tg.UpdatesDifference", diff)
	}
	if len(got.OtherUpdates) != 1 {
		t.Fatalf("difference updates = %d, want one read update", len(got.OtherUpdates))
	}
	readUpdate, ok := got.OtherUpdates[0].(*tg.UpdateReadStories)
	if !ok {
		t.Fatalf("first update = %T, want *tg.UpdateReadStories", got.OtherUpdates[0])
	}
	if readUpdate.MaxID != 2 {
		t.Fatalf("read update max_id = %d, want 2", readUpdate.MaxID)
	}

	if _, err := storyStore.UpsertStory(ctx, domain.UpsertStoryRequest{Story: domain.Story{
		Owner:      owner,
		ID:         3,
		Date:       1700000200,
		ExpireDate: 1700003600,
		Public:     true,
	}}); err != nil {
		t.Fatalf("upsert story after read: %v", err)
	}
	peerStories, err := r.onStoriesGetPeerStories(readCtx, &tg.InputPeerUser{UserID: owner.ID})
	if err != nil {
		t.Fatalf("get peer stories: %v", err)
	}
	if peerStories.Stories.MaxReadID != 2 || len(peerStories.Stories.Stories) != 3 {
		t.Fatalf("peer stories = %+v, want max read 2 and three stories", peerStories.Stories)
	}
}

func TestStoriesReadStoriesInvisiblePeerDoesNotWriteDifference(t *testing.T) {
	ctx := context.Background()
	authKeyID := [8]byte{4, 5, 7}
	storyStore := memory.NewStoryStore()
	updateStore := memory.NewUpdateEventStore()
	owner := domain.Peer{Type: domain.PeerTypeUser, ID: 1000000001}
	viewerID := int64(1000000002)
	if _, err := storyStore.UpsertStory(ctx, domain.UpsertStoryRequest{Story: domain.Story{
		Owner:      owner,
		ID:         1,
		Date:       1700000001,
		ExpireDate: 1700003600,
	}}); err != nil {
		t.Fatalf("upsert invisible story: %v", err)
	}
	r := New(Config{}, Deps{
		Stories: appstories.NewService(storyStore),
		Updates: appupdates.NewService(memory.NewUpdateStateStore(), updateStore),
	}, zaptest.NewLogger(t), fixedClock{now: time.Unix(1700000100, 0)})

	readCtx := WithSessionID(WithAuthKeyID(WithUserID(ctx, viewerID), authKeyID), 89)
	ids, err := r.onStoriesReadStories(readCtx, &tg.StoriesReadStoriesRequest{
		Peer:  &tg.InputPeerUser{UserID: owner.ID},
		MaxID: 99,
	})
	if err != nil {
		t.Fatalf("read invisible stories: %v", err)
	}
	if len(ids) != 0 {
		t.Fatalf("read invisible ids = %v, want empty no-op", ids)
	}
	events, err := updateStore.ListAfter(ctx, viewerID, 0, 10)
	if err != nil {
		t.Fatalf("list invisible read events: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("read events after invisible story = %+v, want none", events)
	}

	if _, err := storyStore.UpsertStory(ctx, domain.UpsertStoryRequest{Story: domain.Story{
		Owner:      owner,
		ID:         2,
		Date:       1700000200,
		ExpireDate: 1700003600,
		Public:     true,
	}}); err != nil {
		t.Fatalf("upsert visible story after invisible read: %v", err)
	}
	peerStories, err := r.onStoriesGetPeerStories(readCtx, &tg.InputPeerUser{UserID: owner.ID})
	if err != nil {
		t.Fatalf("get peer stories after invisible read: %v", err)
	}
	if peerStories.Stories.MaxReadID != 0 || len(peerStories.Stories.Stories) != 1 {
		t.Fatalf("peer stories after invisible read = %+v, want unread visible story only", peerStories.Stories)
	}
	if item, ok := peerStories.Stories.Stories[0].(*tg.StoryItem); !ok || item.ID != 2 {
		t.Fatalf("visible story after invisible read = %T %+v, want story 2", peerStories.Stories.Stories[0], peerStories.Stories.Stories[0])
	}
}

func TestStoriesReadStoriesFallbackDoesNotConfirmFutureBoundary(t *testing.T) {
	ctx := context.Background()
	r := New(Config{}, Deps{}, zaptest.NewLogger(t), fixedClock{now: time.Unix(1700000100, 0)})
	reqCtx := WithUserID(ctx, 1000000001)

	if _, err := r.onStoriesReadStories(reqCtx, nil); err == nil || !tgerr.Is(err, "INPUT_REQUEST_INVALID") {
		t.Fatalf("nil read stories err = %v, want INPUT_REQUEST_INVALID", err)
	}
	if _, err := r.onStoriesReadStories(reqCtx, &tg.StoriesReadStoriesRequest{
		Peer:  &tg.InputPeerEmpty{},
		MaxID: 0,
	}); err == nil || !tgerr.Is(err, "STORY_ID_INVALID") {
		t.Fatalf("zero read stories max id before peer err = %v, want STORY_ID_INVALID", err)
	}
	if _, err := r.onStoriesReadStories(reqCtx, &tg.StoriesReadStoriesRequest{
		Peer:  &tg.InputPeerEmpty{},
		MaxID: domain.MaxStoryID + 1,
	}); err == nil || !tgerr.Is(err, "STORY_ID_INVALID") {
		t.Fatalf("oversized read stories max id before peer err = %v, want STORY_ID_INVALID", err)
	}

	ids, err := r.onStoriesReadStories(reqCtx, &tg.StoriesReadStoriesRequest{
		Peer:  &tg.InputPeerUser{UserID: 1000000002},
		MaxID: 99,
	})
	if err != nil {
		t.Fatalf("read stories fallback: %v", err)
	}
	if len(ids) != 0 {
		t.Fatalf("read stories fallback ids = %v, want no-op empty vector", ids)
	}
}

func TestStoriesGetAllReadPeerStoriesReturnsStoredReadUpdates(t *testing.T) {
	ctx := context.Background()
	viewerID := int64(1000000001)
	storyStore := memory.NewStoryStore()
	userPeer := domain.Peer{Type: domain.PeerTypeUser, ID: 1000000002}
	channelPeer := domain.Peer{Type: domain.PeerTypeChannel, ID: 1000000003}
	if _, err := storyStore.MarkRead(ctx, viewerID, userPeer, 2, 1700000001); err != nil {
		t.Fatalf("mark user read: %v", err)
	}
	if _, err := storyStore.MarkRead(ctx, viewerID, channelPeer, 5, 1700000002); err != nil {
		t.Fatalf("mark channel read: %v", err)
	}
	if _, err := storyStore.MarkRead(ctx, 1000000099, userPeer, 9, 1700000003); err != nil {
		t.Fatalf("mark other viewer read: %v", err)
	}
	r := New(Config{}, Deps{
		Stories: appstories.NewService(storyStore),
	}, zaptest.NewLogger(t), fixedClock{now: time.Unix(1700000100, 0)})

	updates, err := r.onStoriesGetAllReadPeerStories(WithUserID(ctx, viewerID))
	if err != nil {
		t.Fatalf("get all read peer stories: %v", err)
	}
	got, ok := updates.(*tg.Updates)
	if !ok {
		t.Fatalf("updates = %T, want *tg.Updates", updates)
	}
	if got.Date != 1700000100 || got.Seq != 0 || len(got.Users) != 0 || len(got.Chats) != 0 {
		t.Fatalf("updates shape = %+v, want date 1700000100 seq 0 empty users/chats", got)
	}
	readByPeer := map[string]int{}
	for _, update := range got.Updates {
		read, ok := update.(*tg.UpdateReadStories)
		if !ok {
			t.Fatalf("update = %T, want updateReadStories", update)
		}
		switch peer := read.Peer.(type) {
		case *tg.PeerUser:
			readByPeer["user"] = read.MaxID
			if peer.UserID != userPeer.ID {
				t.Fatalf("peer user id = %d, want %d", peer.UserID, userPeer.ID)
			}
		case *tg.PeerChannel:
			readByPeer["channel"] = read.MaxID
			if peer.ChannelID != channelPeer.ID {
				t.Fatalf("peer channel id = %d, want %d", peer.ChannelID, channelPeer.ID)
			}
		default:
			t.Fatalf("read peer = %T, want user/channel", read.Peer)
		}
	}
	if len(readByPeer) != 2 || readByPeer["user"] != 2 || readByPeer["channel"] != 5 {
		t.Fatalf("read updates = %+v, want user 2 channel 5", readByPeer)
	}

	emptyRouter := New(Config{}, Deps{}, zaptest.NewLogger(t), fixedClock{now: time.Unix(1700000200, 0)})
	empty, err := emptyRouter.onStoriesGetAllReadPeerStories(WithUserID(ctx, viewerID))
	if err != nil {
		t.Fatalf("empty get all read peer stories: %v", err)
	}
	emptyUpdates, ok := empty.(*tg.Updates)
	if !ok || emptyUpdates.Date != 1700000200 || len(emptyUpdates.Updates) != 0 || len(emptyUpdates.Users) != 0 || len(emptyUpdates.Chats) != 0 {
		t.Fatalf("empty updates = %#v, want empty *tg.Updates with fixed date", empty)
	}
}

type readStateListStoryService struct {
	StoriesService
	states []domain.StoryReadState
}

func (s readStateListStoryService) ListReadStates(context.Context, int64) ([]domain.StoryReadState, error) {
	return append([]domain.StoryReadState(nil), s.states...), nil
}

type storiesByIDStoryService struct {
	StoriesService
	list domain.StoryList
}

func (s storiesByIDStoryService) GetStoriesByID(context.Context, int64, domain.Peer, []int, int) (domain.StoryList, error) {
	return s.list, nil
}

type incrementViewsStoryService struct {
	StoriesService
	viewerUserID int64
	peer         domain.Peer
	ids          []int
	calls        int
}

func (s *incrementViewsStoryService) IncrementViews(_ context.Context, viewerUserID int64, peer domain.Peer, ids []int, _ int) (int, error) {
	s.viewerUserID = viewerUserID
	s.peer = peer
	s.ids = append([]int(nil), ids...)
	s.calls++
	return 0, nil
}

func TestStoriesGetAllReadPeerStoriesNormalizesReadSnapshot(t *testing.T) {
	ctx := context.Background()
	viewerID := int64(1000000001)
	userPeer := domain.Peer{Type: domain.PeerTypeUser, ID: 1000000002}
	channelPeer := domain.Peer{Type: domain.PeerTypeChannel, ID: 1000000003}
	r := New(Config{}, Deps{
		Stories: readStateListStoryService{states: []domain.StoryReadState{
			{ViewerID: viewerID, Peer: userPeer, MaxReadID: 3, Date: 1700000001},
			{ViewerID: viewerID, Peer: userPeer, MaxReadID: 5, Date: 1700000002},
			{ViewerID: viewerID, Peer: userPeer, MaxReadID: 4, Date: 1700000009},
			{ViewerID: viewerID, Peer: channelPeer, MaxReadID: 7, Date: 1700000003},
			{ViewerID: viewerID + 1, Peer: userPeer, MaxReadID: 99, Date: 1700000004},
			{ViewerID: viewerID, Peer: domain.Peer{Type: domain.PeerTypeUser}, MaxReadID: 8, Date: 1700000005},
			{ViewerID: viewerID, Peer: domain.Peer{Type: domain.PeerTypeUser, ID: 1000000004}, MaxReadID: 0, Date: 1700000006},
			{ViewerID: viewerID, Peer: domain.Peer{Type: domain.PeerTypeUser, ID: 1000000005}, MaxReadID: domain.MaxStoryID + 1, Date: 1700000007},
		}},
	}, zaptest.NewLogger(t), fixedClock{now: time.Unix(1700000100, 0)})

	updates, err := r.onStoriesGetAllReadPeerStories(WithUserID(ctx, viewerID))
	if err != nil {
		t.Fatalf("get all read peer stories: %v", err)
	}
	got, ok := updates.(*tg.Updates)
	if !ok {
		t.Fatalf("updates = %T, want *tg.Updates", updates)
	}
	readByPeer := map[string]int{}
	for _, update := range got.Updates {
		read, ok := update.(*tg.UpdateReadStories)
		if !ok {
			t.Fatalf("update = %T, want updateReadStories", update)
		}
		switch peer := read.Peer.(type) {
		case *tg.PeerUser:
			readByPeer["user:"+strconv.FormatInt(peer.UserID, 10)] = read.MaxID
		case *tg.PeerChannel:
			readByPeer["channel:"+strconv.FormatInt(peer.ChannelID, 10)] = read.MaxID
		default:
			t.Fatalf("read peer = %T, want user/channel", read.Peer)
		}
	}
	if len(readByPeer) != 2 || readByPeer["user:1000000002"] != 5 || readByPeer["channel:1000000003"] != 7 {
		t.Fatalf("read updates = %+v, want only current viewer max user/channel boundaries", readByPeer)
	}
}

func TestStoriesIncrementStoryViewsRejectsEmptyIDs(t *testing.T) {
	ctx := context.Background()
	storyStore := memory.NewStoryStore()
	owner := domain.Peer{Type: domain.PeerTypeUser, ID: 1000000001}
	if _, err := storyStore.UpsertStory(ctx, domain.UpsertStoryRequest{Story: domain.Story{
		Owner:      owner,
		ID:         1,
		Date:       1700000000,
		ExpireDate: 1700003600,
		Public:     true,
	}}); err != nil {
		t.Fatalf("upsert story: %v", err)
	}
	r := New(Config{}, Deps{Stories: appstories.NewService(storyStore)}, zaptest.NewLogger(t), fixedClock{now: time.Unix(1700000100, 0)})

	_, err := r.onStoriesIncrementStoryViews(WithUserID(ctx, 1000000002), &tg.StoriesIncrementStoryViewsRequest{
		Peer: &tg.InputPeerUser{UserID: owner.ID},
	})
	if err == nil || !tgerr.Is(err, "STORY_ID_EMPTY") {
		t.Fatalf("empty increment story views err = %v, want STORY_ID_EMPTY", err)
	}

	oversized := make([]int, domain.MaxStoryIDs+1)
	for i := range oversized {
		oversized[i] = 1
	}
	for _, tc := range []struct {
		name string
		ids  []int
		want string
	}{
		{name: "empty before peer", ids: nil, want: "STORY_ID_EMPTY"},
		{name: "invalid before peer", ids: []int{0}, want: "STORY_ID_INVALID"},
		{name: "oversized before peer", ids: oversized, want: "STORY_ID_INVALID"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := r.onStoriesIncrementStoryViews(WithUserID(ctx, 1000000002), &tg.StoriesIncrementStoryViewsRequest{
				Peer: &tg.InputPeerEmpty{},
				ID:   tc.ids,
			})
			if err == nil || !tgerr.Is(err, tc.want) {
				t.Fatalf("increment story views err = %v, want %s", err, tc.want)
			}
		})
	}
}

func TestStoriesIncrementStoryViewsDedupesIDsBeforeCounters(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	ownerUser, err := userStore.Create(ctx, domain.User{AccessHash: 90101, Phone: "15550090101", FirstName: "Owner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	viewer, err := userStore.Create(ctx, domain.User{AccessHash: 90102, Phone: "15550090102", FirstName: "Viewer"})
	if err != nil {
		t.Fatalf("create viewer: %v", err)
	}
	storyStore := memory.NewStoryStore()
	owner := domain.Peer{Type: domain.PeerTypeUser, ID: ownerUser.ID}
	if _, err := storyStore.UpsertStory(ctx, domain.UpsertStoryRequest{Story: domain.Story{
		Owner:      owner,
		ID:         1,
		Date:       1700000000,
		ExpireDate: 1700003600,
		Public:     true,
	}}); err != nil {
		t.Fatalf("upsert story: %v", err)
	}
	r := New(Config{}, Deps{
		Users:   appusers.NewService(userStore),
		Stories: appstories.NewService(storyStore),
	}, zaptest.NewLogger(t), fixedClock{now: time.Unix(1700000100, 0)})
	viewerCtx := WithUserID(ctx, viewer.ID)
	peer := &tg.InputPeerUser{UserID: ownerUser.ID, AccessHash: ownerUser.AccessHash}

	ok, err := r.onStoriesIncrementStoryViews(viewerCtx, &tg.StoriesIncrementStoryViewsRequest{
		Peer: peer,
		ID:   []int{1, 1, 1},
	})
	if err != nil || !ok {
		t.Fatalf("increment duplicate story views = %v, %v; want true nil", ok, err)
	}
	got, err := r.onStoriesGetStoriesViews(viewerCtx, &tg.StoriesGetStoriesViewsRequest{
		Peer: peer,
		ID:   []int{1},
	})
	if err != nil {
		t.Fatalf("get story views after duplicate increment: %v", err)
	}
	if len(got.Views) != 1 || got.Views[0].ViewsCount != 1 {
		t.Fatalf("story view counters after duplicate increment = %+v, want one view", got.Views)
	}
	if got.Views[0].HasViewers || len(got.Views[0].RecentViewers) != 0 || len(got.Users) != 0 {
		t.Fatalf("non-owner story views = %+v users=%+v, want counters only", got.Views[0], got.Users)
	}

	ownerViews, err := r.onStoriesGetStoryViewsList(WithUserID(ctx, ownerUser.ID), &tg.StoriesGetStoryViewsListRequest{
		Peer:  &tg.InputPeerSelf{},
		ID:    1,
		Limit: 10,
	})
	if err != nil {
		t.Fatalf("get owner views list after duplicate increment: %v", err)
	}
	if ownerViews.Count != 1 || ownerViews.ViewsCount != 1 || len(ownerViews.Views) != 1 {
		t.Fatalf("owner views after duplicate increment = %+v, want one durable viewer row", ownerViews)
	}
	view, ok := ownerViews.Views[0].(*tg.StoryView)
	if !ok || view.UserID != viewer.ID {
		t.Fatalf("owner view row = %T %+v, want viewer %d", ownerViews.Views[0], ownerViews.Views[0], viewer.ID)
	}
	if findUserClass(ownerViews.Users, viewer.ID) == nil {
		t.Fatalf("owner views users = %+v, want viewer companion user", ownerViews.Users)
	}

	if ok, err = r.onStoriesIncrementStoryViews(viewerCtx, &tg.StoriesIncrementStoryViewsRequest{
		Peer: peer,
		ID:   []int{1, 1},
	}); err != nil || !ok {
		t.Fatalf("retry duplicate story views = %v, %v; want true nil", ok, err)
	}
	ownerViews, err = r.onStoriesGetStoryViewsList(WithUserID(ctx, ownerUser.ID), &tg.StoriesGetStoryViewsListRequest{
		Peer:  &tg.InputPeerSelf{},
		ID:    1,
		Limit: 10,
	})
	if err != nil {
		t.Fatalf("get owner views list after duplicate retry: %v", err)
	}
	if ownerViews.Count != 1 || ownerViews.ViewsCount != 1 || len(ownerViews.Views) != 1 {
		t.Fatalf("owner views after duplicate retry = %+v, want still one durable viewer row", ownerViews)
	}
}

func TestStoriesIncrementStoryViewsDedupesIDsBeforeService(t *testing.T) {
	ctx := context.Background()
	userID := int64(1000000001)
	stories := &incrementViewsStoryService{}
	r := New(Config{}, Deps{Stories: stories}, zaptest.NewLogger(t), fixedClock{now: time.Unix(1700000100, 0)})

	ok, err := r.onStoriesIncrementStoryViews(WithUserID(ctx, userID), &tg.StoriesIncrementStoryViewsRequest{
		Peer: &tg.InputPeerSelf{},
		ID:   []int{3, 1, 3, 2, 1},
	})
	if err != nil || !ok {
		t.Fatalf("increment duplicate story views = %v, %v; want true nil", ok, err)
	}
	if stories.calls != 1 {
		t.Fatalf("increment service calls = %d, want 1", stories.calls)
	}
	if stories.viewerUserID != userID || stories.peer.Type != domain.PeerTypeUser || stories.peer.ID != userID {
		t.Fatalf("increment service viewer/peer = user %d peer %+v, want self user %d", stories.viewerUserID, stories.peer, userID)
	}
	want := []int{3, 1, 2}
	if len(stories.ids) != len(want) {
		t.Fatalf("increment service ids = %v, want %v", stories.ids, want)
	}
	for i := range want {
		if stories.ids[i] != want[i] {
			t.Fatalf("increment service ids = %v, want first-seen dedupe %v", stories.ids, want)
		}
	}
}

func TestStoriesGetPeerMaxIDsBoundsBeforeFallback(t *testing.T) {
	ctx := context.Background()
	r := New(Config{}, Deps{}, zaptest.NewLogger(t), fixedClock{now: time.Unix(1700000100, 0)})

	empty, err := r.onStoriesGetPeerMaxIDs(WithUserID(ctx, 1000000001), nil)
	if err != nil {
		t.Fatalf("empty peer max ids: %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("empty peer max ids length = %d, want 0", len(empty))
	}

	one, err := r.onStoriesGetPeerMaxIDs(WithUserID(ctx, 1000000001), []tg.InputPeerClass{&tg.InputPeerSelf{}})
	if err != nil {
		t.Fatalf("single peer max id fallback: %v", err)
	}
	if len(one) != 1 {
		t.Fatalf("single peer max id fallback length = %d, want 1", len(one))
	}

	oversized := make([]tg.InputPeerClass, domain.MaxStoryIDs+1)
	for i := range oversized {
		oversized[i] = &tg.InputPeerSelf{}
	}
	if _, err := r.onStoriesGetPeerMaxIDs(WithUserID(ctx, 1000000001), oversized); err == nil || !tgerr.Is(err, "STORY_ID_INVALID") {
		t.Fatalf("oversized peer max ids err = %v, want STORY_ID_INVALID", err)
	}
}

func TestStoriesGetPeerMaxIDsRepeatsDuplicatePeersInOrder(t *testing.T) {
	ctx := context.Background()
	ownerID := int64(1000000001)
	owner := domain.Peer{Type: domain.PeerTypeUser, ID: ownerID}
	storyStore := memory.NewStoryStore()
	for _, storyID := range []int{1, 3} {
		if _, err := storyStore.UpsertStory(ctx, domain.UpsertStoryRequest{Story: domain.Story{
			Owner:      owner,
			ID:         storyID,
			Date:       1700000000 + storyID,
			ExpireDate: 1700003600,
			Public:     true,
		}}); err != nil {
			t.Fatalf("upsert story %d: %v", storyID, err)
		}
	}
	r := New(Config{}, Deps{
		Stories: appstories.NewService(storyStore),
	}, zaptest.NewLogger(t), fixedClock{now: time.Unix(1700000100, 0)})

	got, err := r.onStoriesGetPeerMaxIDs(WithUserID(ctx, ownerID), []tg.InputPeerClass{
		&tg.InputPeerSelf{},
		&tg.InputPeerSelf{},
	})
	if err != nil {
		t.Fatalf("get duplicate peer max ids: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("recent stories length = %d, want one slot per requested peer", len(got))
	}
	for i := range got {
		if maxID, ok := got[i].GetMaxID(); !ok || maxID != 3 {
			t.Fatalf("recent stories[%d].max_id = %d ok=%v, want 3 true", i, maxID, ok)
		}
	}
}

func TestStoriesRecentAlignmentPreservesRequestSlots(t *testing.T) {
	userPeer := domain.Peer{Type: domain.PeerTypeUser, ID: 1000000001}
	channelPeer := domain.Peer{Type: domain.PeerTypeChannel, ID: 2000000001}
	missingPeer := domain.Peer{Type: domain.PeerTypeUser, ID: 1000000002}
	got := alignStoryRecentByPeer([]domain.Peer{userPeer, channelPeer, userPeer, missingPeer}, []domain.RecentStory{
		{Peer: channelPeer, MaxID: 5},
		{Peer: userPeer, MaxID: 3},
		{Peer: userPeer, MaxID: 4, Live: true},
		{Peer: domain.Peer{}, MaxID: 99},
	})
	if len(got) != 4 {
		t.Fatalf("aligned recent length = %d, want one slot per requested peer", len(got))
	}
	if got[0].Peer != userPeer || got[0].MaxID != 4 || !got[0].Live {
		t.Fatalf("aligned recent[0] = %+v, want user max 4 live", got[0])
	}
	if got[1].Peer != channelPeer || got[1].MaxID != 5 || got[1].Live {
		t.Fatalf("aligned recent[1] = %+v, want channel max 5 non-live", got[1])
	}
	if got[2].Peer != userPeer || got[2].MaxID != 4 || !got[2].Live {
		t.Fatalf("aligned recent[2] = %+v, want repeated user max 4 live", got[2])
	}
	if got[3].Peer != missingPeer || got[3].MaxID != 0 || got[3].Live {
		t.Fatalf("aligned recent[3] = %+v, want missing peer zero recent", got[3])
	}
}

func TestStoriesCanSendStoryFallbackRejectsNonOwnerPeers(t *testing.T) {
	ctx := context.Background()
	r := New(Config{}, Deps{}, zaptest.NewLogger(t), fixedClock{now: time.Unix(1700000100, 0)})
	reqCtx := WithUserID(ctx, 1000000001)

	self, err := r.onStoriesCanSendStory(reqCtx, &tg.InputPeerSelf{})
	if err != nil {
		t.Fatalf("self canSendStory fallback: %v", err)
	}
	if self.CountRemains != domain.DefaultStoryCanSendRemaining {
		t.Fatalf("self canSendStory fallback = %+v, want default remaining", self)
	}
	if _, err := r.onStoriesCanSendStory(reqCtx, &tg.InputPeerUser{UserID: 1000000001}); err != nil {
		t.Fatalf("self inputUser canSendStory fallback: %v", err)
	}
	if _, err := r.onStoriesCanSendStory(reqCtx, &tg.InputPeerUser{UserID: 1000000002}); err == nil || !tgerr.Is(err, "PEER_ID_INVALID") {
		t.Fatalf("other user canSendStory fallback err = %v, want PEER_ID_INVALID", err)
	}
	if _, err := r.onStoriesCanSendStory(reqCtx, &tg.InputPeerChannel{ChannelID: 10}); err == nil || !tgerr.Is(err, "PEER_ID_INVALID") {
		t.Fatalf("channel canSendStory fallback err = %v, want PEER_ID_INVALID", err)
	}
}

func TestStoriesCanSendStoryRejectsNilPeersBeforeFallback(t *testing.T) {
	ctx := context.Background()
	r := New(Config{}, Deps{}, zaptest.NewLogger(t), fixedClock{now: time.Unix(1700000100, 0)})
	reqCtx := WithUserID(ctx, 1000000001)
	var typedNilSelf *tg.InputPeerSelf
	var typedNilUser *tg.InputPeerUser
	var typedNilChannel *tg.InputPeerChannel
	for _, peer := range []tg.InputPeerClass{
		nil,
		typedNilSelf,
		typedNilUser,
		typedNilChannel,
	} {
		if _, err := r.onStoriesCanSendStory(reqCtx, peer); err == nil || !tgerr.Is(err, "PEER_ID_INVALID") {
			t.Fatalf("canSendStory peer %T err = %v, want PEER_ID_INVALID", peer, err)
		}
	}
}

func TestStoriesDirectPeerRequestsValidateMalformedPeerBeforeAuth(t *testing.T) {
	ctx := context.Background()
	r := New(Config{}, Deps{}, zaptest.NewLogger(t), fixedClock{now: time.Unix(1700000100, 0)})
	var typedNilSelf *tg.InputPeerSelf
	malformedPeers := []tg.InputPeerClass{
		nil,
		typedNilSelf,
		&tg.InputPeerEmpty{},
		&tg.InputPeerChat{},
		&tg.InputPeerUser{},
		&tg.InputPeerChannel{},
		&tg.InputPeerUserFromMessage{Peer: &tg.InputPeerSelf{}, UserID: 1},
		&tg.InputPeerChannelFromMessage{Peer: &tg.InputPeerSelf{}, ChannelID: 1},
	}
	calls := []struct {
		name string
		call func(tg.InputPeerClass) error
	}{
		{
			name: "getPeerStories",
			call: func(peer tg.InputPeerClass) error {
				_, err := r.onStoriesGetPeerStories(ctx, peer)
				return err
			},
		},
		{
			name: "getPeerMaxIDs",
			call: func(peer tg.InputPeerClass) error {
				_, err := r.onStoriesGetPeerMaxIDs(ctx, []tg.InputPeerClass{peer})
				return err
			},
		},
		{
			name: "canSendStory",
			call: func(peer tg.InputPeerClass) error {
				_, err := r.onStoriesCanSendStory(ctx, peer)
				return err
			},
		},
	}
	for _, peer := range malformedPeers {
		for _, call := range calls {
			if err := call.call(peer); err == nil || !tgerr.Is(err, "PEER_ID_INVALID") {
				t.Fatalf("%s peer %T err = %v, want PEER_ID_INVALID before auth", call.name, peer, err)
			}
		}
	}

	oversized := make([]tg.InputPeerClass, domain.MaxStoryIDs+1)
	for i := range oversized {
		oversized[i] = &tg.InputPeerSelf{}
	}
	if _, err := r.onStoriesGetPeerMaxIDs(ctx, oversized); err == nil || !tgerr.Is(err, "STORY_ID_INVALID") {
		t.Fatalf("oversized getPeerMaxIDs err = %v, want STORY_ID_INVALID before auth", err)
	}
}

func TestStoriesReadViewReactionRequestsValidateMalformedPeerBeforeAuth(t *testing.T) {
	ctx := context.Background()
	r := New(Config{}, Deps{}, zaptest.NewLogger(t), fixedClock{now: time.Unix(1700000100, 0)})
	malformedPeers := []tg.InputPeerClass{
		nil,
		(*tg.InputPeerSelf)(nil),
		&tg.InputPeerEmpty{},
		&tg.InputPeerChat{},
		&tg.InputPeerUser{},
		&tg.InputPeerChannel{},
		&tg.InputPeerUserFromMessage{Peer: &tg.InputPeerSelf{}, UserID: 1},
		&tg.InputPeerChannelFromMessage{Peer: &tg.InputPeerSelf{}, ChannelID: 1},
	}
	calls := []struct {
		name string
		call func(tg.InputPeerClass) error
	}{
		{
			name: "readStories",
			call: func(peer tg.InputPeerClass) error {
				_, err := r.onStoriesReadStories(ctx, &tg.StoriesReadStoriesRequest{Peer: peer, MaxID: 1})
				return err
			},
		},
		{
			name: "incrementStoryViews",
			call: func(peer tg.InputPeerClass) error {
				_, err := r.onStoriesIncrementStoryViews(ctx, &tg.StoriesIncrementStoryViewsRequest{Peer: peer, ID: []int{1}})
				return err
			},
		},
		{
			name: "getStoriesViews",
			call: func(peer tg.InputPeerClass) error {
				_, err := r.onStoriesGetStoriesViews(ctx, &tg.StoriesGetStoriesViewsRequest{Peer: peer, ID: []int{1}})
				return err
			},
		},
		{
			name: "sendReaction",
			call: func(peer tg.InputPeerClass) error {
				_, err := r.onStoriesSendReaction(ctx, &tg.StoriesSendReactionRequest{
					Peer:     peer,
					StoryID:  1,
					Reaction: &tg.ReactionEmoji{Emoticon: "👍"},
				})
				return err
			},
		},
	}
	for _, peer := range malformedPeers {
		for _, call := range calls {
			if err := call.call(peer); err == nil || !tgerr.Is(err, "PEER_ID_INVALID") {
				t.Fatalf("%s peer %T err = %v, want PEER_ID_INVALID before auth", call.name, peer, err)
			}
		}
	}

	if _, err := r.onStoriesReadStories(ctx, &tg.StoriesReadStoriesRequest{Peer: &tg.InputPeerEmpty{}}); err == nil || !tgerr.Is(err, "STORY_ID_INVALID") {
		t.Fatalf("readStories invalid id err = %v, want STORY_ID_INVALID before peer", err)
	}
	if _, err := r.onStoriesIncrementStoryViews(ctx, &tg.StoriesIncrementStoryViewsRequest{Peer: &tg.InputPeerEmpty{}}); err == nil || !tgerr.Is(err, "STORY_ID_EMPTY") {
		t.Fatalf("incrementStoryViews empty ids err = %v, want STORY_ID_EMPTY before peer", err)
	}
	if _, err := r.onStoriesGetStoriesViews(ctx, &tg.StoriesGetStoriesViewsRequest{Peer: &tg.InputPeerEmpty{}}); err == nil || !tgerr.Is(err, "STORY_ID_EMPTY") {
		t.Fatalf("getStoriesViews empty ids err = %v, want STORY_ID_EMPTY before peer", err)
	}
	if _, err := r.onStoriesSendReaction(ctx, &tg.StoriesSendReactionRequest{
		Peer:     &tg.InputPeerEmpty{},
		StoryID:  1,
		Reaction: &tg.ReactionCustomEmoji{},
	}); err == nil || !tgerr.Is(err, "REACTION_INVALID") {
		t.Fatalf("sendReaction malformed reaction err = %v, want REACTION_INVALID before peer", err)
	}
}

func TestStoriesProfileWriteRequestsValidateMalformedPeerBeforeAuth(t *testing.T) {
	ctx := context.Background()
	r := New(Config{}, Deps{}, zaptest.NewLogger(t), fixedClock{now: time.Unix(1700000100, 0)})
	malformedPeers := []tg.InputPeerClass{
		nil,
		(*tg.InputPeerSelf)(nil),
		&tg.InputPeerEmpty{},
		&tg.InputPeerChat{},
		&tg.InputPeerUser{},
		&tg.InputPeerChannel{},
		&tg.InputPeerUserFromMessage{Peer: &tg.InputPeerSelf{}, UserID: 1},
		&tg.InputPeerChannelFromMessage{Peer: &tg.InputPeerSelf{}, ChannelID: 1},
	}
	calls := []struct {
		name string
		call func(tg.InputPeerClass) error
	}{
		{
			name: "deleteStories",
			call: func(peer tg.InputPeerClass) error {
				_, err := r.onStoriesDeleteStories(ctx, &tg.StoriesDeleteStoriesRequest{Peer: peer, ID: []int{1}})
				return err
			},
		},
		{
			name: "togglePinned",
			call: func(peer tg.InputPeerClass) error {
				_, err := r.onStoriesTogglePinned(ctx, &tg.StoriesTogglePinnedRequest{Peer: peer, ID: []int{1}})
				return err
			},
		},
		{
			name: "togglePinned empty IDs",
			call: func(peer tg.InputPeerClass) error {
				_, err := r.onStoriesTogglePinned(ctx, &tg.StoriesTogglePinnedRequest{Peer: peer})
				return err
			},
		},
		{
			name: "togglePinnedToTop",
			call: func(peer tg.InputPeerClass) error {
				_, err := r.onStoriesTogglePinnedToTop(ctx, &tg.StoriesTogglePinnedToTopRequest{Peer: peer, ID: []int{1}})
				return err
			},
		},
		{
			name: "togglePinnedToTop empty IDs",
			call: func(peer tg.InputPeerClass) error {
				_, err := r.onStoriesTogglePinnedToTop(ctx, &tg.StoriesTogglePinnedToTopRequest{Peer: peer})
				return err
			},
		},
	}
	for _, peer := range malformedPeers {
		for _, call := range calls {
			if err := call.call(peer); err == nil || !tgerr.Is(err, "PEER_ID_INVALID") {
				t.Fatalf("%s peer %T err = %v, want PEER_ID_INVALID before auth", call.name, peer, err)
			}
		}
	}

	if _, err := r.onStoriesDeleteStories(ctx, &tg.StoriesDeleteStoriesRequest{Peer: &tg.InputPeerEmpty{}}); err == nil || !tgerr.Is(err, "STORY_ID_EMPTY") {
		t.Fatalf("deleteStories empty ids err = %v, want STORY_ID_EMPTY before peer", err)
	}
	if _, err := r.onStoriesDeleteStories(ctx, &tg.StoriesDeleteStoriesRequest{
		Peer: &tg.InputPeerEmpty{},
		ID:   []int{0},
	}); err == nil || !tgerr.Is(err, "STORY_ID_INVALID") {
		t.Fatalf("deleteStories invalid id err = %v, want STORY_ID_INVALID before peer", err)
	}
	if _, err := r.onStoriesTogglePinned(ctx, &tg.StoriesTogglePinnedRequest{
		Peer: &tg.InputPeerEmpty{},
		ID:   []int{0},
	}); err == nil || !tgerr.Is(err, "STORY_ID_INVALID") {
		t.Fatalf("togglePinned invalid id err = %v, want STORY_ID_INVALID before peer", err)
	}
	if _, err := r.onStoriesTogglePinnedToTop(ctx, &tg.StoriesTogglePinnedToTopRequest{
		Peer: &tg.InputPeerEmpty{},
		ID:   []int{0},
	}); err == nil || !tgerr.Is(err, "STORY_ID_INVALID") {
		t.Fatalf("togglePinnedToTop invalid id err = %v, want STORY_ID_INVALID before peer", err)
	}
	overTop := make([]int, domain.MaxStoryPinnedToTop+1)
	for i := range overTop {
		overTop[i] = i + 1
	}
	if _, err := r.onStoriesTogglePinnedToTop(ctx, &tg.StoriesTogglePinnedToTopRequest{
		Peer: &tg.InputPeerEmpty{},
		ID:   overTop,
	}); err == nil || !tgerr.Is(err, "STORY_ID_INVALID") {
		t.Fatalf("togglePinnedToTop over top limit err = %v, want STORY_ID_INVALID before peer", err)
	}
}

func TestStoriesAlbumRequestsValidateMalformedPeerBeforeAuth(t *testing.T) {
	ctx := context.Background()
	r := New(Config{}, Deps{}, zaptest.NewLogger(t), fixedClock{now: time.Unix(1700000100, 0)})
	malformedPeers := []tg.InputPeerClass{
		nil,
		(*tg.InputPeerSelf)(nil),
		&tg.InputPeerEmpty{},
		&tg.InputPeerChat{},
		&tg.InputPeerUser{},
		&tg.InputPeerChannel{},
		&tg.InputPeerUserFromMessage{Peer: &tg.InputPeerSelf{}, UserID: 1},
		&tg.InputPeerChannelFromMessage{Peer: &tg.InputPeerSelf{}, ChannelID: 1},
	}
	calls := []struct {
		name string
		call func(tg.InputPeerClass) error
	}{
		{
			name: "getAlbums",
			call: func(peer tg.InputPeerClass) error {
				_, err := r.onStoriesGetAlbums(ctx, &tg.StoriesGetAlbumsRequest{Peer: peer})
				return err
			},
		},
		{
			name: "getAlbumStories",
			call: func(peer tg.InputPeerClass) error {
				_, err := r.onStoriesGetAlbumStories(ctx, &tg.StoriesGetAlbumStoriesRequest{Peer: peer, AlbumID: 1, Limit: 20})
				return err
			},
		},
		{
			name: "createAlbum",
			call: func(peer tg.InputPeerClass) error {
				_, err := r.onStoriesCreateAlbum(ctx, &tg.StoriesCreateAlbumRequest{Peer: peer, Title: "Favorites", Stories: []int{1}})
				return err
			},
		},
		{
			name: "updateAlbum title",
			call: func(peer tg.InputPeerClass) error {
				req := &tg.StoriesUpdateAlbumRequest{Peer: peer, AlbumID: 1}
				req.SetTitle("Travel")
				_, err := r.onStoriesUpdateAlbum(ctx, req)
				return err
			},
		},
		{
			name: "updateAlbum add stories",
			call: func(peer tg.InputPeerClass) error {
				req := &tg.StoriesUpdateAlbumRequest{Peer: peer, AlbumID: 1}
				req.SetAddStories([]int{1})
				_, err := r.onStoriesUpdateAlbum(ctx, req)
				return err
			},
		},
		{
			name: "reorderAlbums",
			call: func(peer tg.InputPeerClass) error {
				_, err := r.onStoriesReorderAlbums(ctx, &tg.StoriesReorderAlbumsRequest{Peer: peer, Order: []int{1}})
				return err
			},
		},
		{
			name: "reorderAlbums empty order",
			call: func(peer tg.InputPeerClass) error {
				_, err := r.onStoriesReorderAlbums(ctx, &tg.StoriesReorderAlbumsRequest{Peer: peer})
				return err
			},
		},
		{
			name: "deleteAlbum",
			call: func(peer tg.InputPeerClass) error {
				_, err := r.onStoriesDeleteAlbum(ctx, &tg.StoriesDeleteAlbumRequest{Peer: peer, AlbumID: 1})
				return err
			},
		},
	}
	for _, peer := range malformedPeers {
		for _, call := range calls {
			if err := call.call(peer); err == nil || !tgerr.Is(err, "PEER_ID_INVALID") {
				t.Fatalf("%s peer %T err = %v, want PEER_ID_INVALID before auth", call.name, peer, err)
			}
		}
	}

	if _, err := r.onStoriesGetAlbumStories(ctx, &tg.StoriesGetAlbumStoriesRequest{
		Peer:    &tg.InputPeerEmpty{},
		AlbumID: 0,
		Limit:   20,
	}); err == nil || !tgerr.Is(err, "INPUT_REQUEST_INVALID") {
		t.Fatalf("getAlbumStories invalid album id err = %v, want INPUT_REQUEST_INVALID before peer", err)
	}
	if _, err := r.onStoriesCreateAlbum(ctx, &tg.StoriesCreateAlbumRequest{
		Peer:  &tg.InputPeerEmpty{},
		Title: strings.Repeat("x", maxStoryAlbumTitleLength+1),
	}); err == nil || !tgerr.Is(err, "LIMIT_INVALID") {
		t.Fatalf("createAlbum long title err = %v, want LIMIT_INVALID before peer", err)
	}
	emptyUpdateReq := &tg.StoriesUpdateAlbumRequest{Peer: &tg.InputPeerEmpty{}, AlbumID: 1}
	if _, err := r.onStoriesUpdateAlbum(ctx, emptyUpdateReq); err == nil || !tgerr.Is(err, "INPUT_REQUEST_INVALID") {
		t.Fatalf("updateAlbum empty mutation err = %v, want INPUT_REQUEST_INVALID before peer", err)
	}
	invalidUpdateReq := &tg.StoriesUpdateAlbumRequest{Peer: &tg.InputPeerEmpty{}, AlbumID: 1}
	invalidUpdateReq.SetAddStories([]int{0})
	if _, err := r.onStoriesUpdateAlbum(ctx, invalidUpdateReq); err == nil || !tgerr.Is(err, "STORY_ID_INVALID") {
		t.Fatalf("updateAlbum invalid story id err = %v, want STORY_ID_INVALID before peer", err)
	}
	if _, err := r.onStoriesReorderAlbums(ctx, &tg.StoriesReorderAlbumsRequest{
		Peer:  &tg.InputPeerEmpty{},
		Order: []int{1, 1},
	}); err == nil || !tgerr.Is(err, "INPUT_REQUEST_INVALID") {
		t.Fatalf("reorderAlbums duplicate err = %v, want INPUT_REQUEST_INVALID before peer", err)
	}
	if _, err := r.onStoriesDeleteAlbum(ctx, &tg.StoriesDeleteAlbumRequest{
		Peer:    &tg.InputPeerEmpty{},
		AlbumID: 0,
	}); err == nil || !tgerr.Is(err, "INPUT_REQUEST_INVALID") {
		t.Fatalf("deleteAlbum invalid album id err = %v, want INPUT_REQUEST_INVALID before peer", err)
	}
}

func TestStoriesLongtailPeerRequestsValidateMalformedPeerBeforeAuth(t *testing.T) {
	ctx := context.Background()
	r := New(Config{}, Deps{}, zaptest.NewLogger(t), fixedClock{now: time.Unix(1700000100, 0)})
	malformedPeers := []tg.InputPeerClass{
		nil,
		(*tg.InputPeerSelf)(nil),
		&tg.InputPeerEmpty{},
		&tg.InputPeerChat{},
		&tg.InputPeerUser{},
		&tg.InputPeerChannel{},
		&tg.InputPeerUserFromMessage{Peer: &tg.InputPeerSelf{}, UserID: 1},
		&tg.InputPeerChannelFromMessage{Peer: &tg.InputPeerSelf{}, ChannelID: 1},
	}
	calls := []struct {
		name string
		call func(tg.InputPeerClass) error
	}{
		{
			name: "getStoriesArchive",
			call: func(peer tg.InputPeerClass) error {
				_, err := r.onStoriesGetStoriesArchive(ctx, &tg.StoriesGetStoriesArchiveRequest{Peer: peer, Limit: 10})
				return err
			},
		},
		{
			name: "getPinnedStories",
			call: func(peer tg.InputPeerClass) error {
				_, err := r.onStoriesGetPinnedStories(ctx, &tg.StoriesGetPinnedStoriesRequest{Peer: peer, Limit: 10})
				return err
			},
		},
		{
			name: "exportStoryLink",
			call: func(peer tg.InputPeerClass) error {
				_, err := r.onStoriesExportStoryLink(ctx, &tg.StoriesExportStoryLinkRequest{Peer: peer, ID: 1})
				return err
			},
		},
		{
			name: "report",
			call: func(peer tg.InputPeerClass) error {
				_, err := r.onStoriesReport(ctx, &tg.StoriesReportRequest{Peer: peer, ID: []int{1}})
				return err
			},
		},
		{
			name: "startLive",
			call: func(peer tg.InputPeerClass) error {
				_, err := r.onStoriesStartLive(ctx, &tg.StoriesStartLiveRequest{
					Peer:         peer,
					RandomID:     9001,
					PrivacyRules: []tg.InputPrivacyRuleClass{&tg.InputPrivacyValueAllowAll{}},
				})
				return err
			},
		},
		{
			name: "searchPosts",
			call: func(peer tg.InputPeerClass) error {
				req := &tg.StoriesSearchPostsRequest{Limit: 10}
				req.SetHashtag("travel")
				req.SetPeer(peer)
				_, err := r.onStoriesSearchPosts(ctx, req)
				return err
			},
		},
		{
			name: "getStoryViewsList",
			call: func(peer tg.InputPeerClass) error {
				_, err := r.onStoriesGetStoryViewsList(ctx, &tg.StoriesGetStoryViewsListRequest{Peer: peer, ID: 1, Limit: 10})
				return err
			},
		},
		{
			name: "getStoryReactionsList",
			call: func(peer tg.InputPeerClass) error {
				_, err := r.onStoriesGetStoryReactionsList(ctx, &tg.StoriesGetStoryReactionsListRequest{Peer: peer, ID: 1, Limit: 10})
				return err
			},
		},
		{
			name: "togglePeerStoriesHidden",
			call: func(peer tg.InputPeerClass) error {
				_, err := r.onStoriesTogglePeerStoriesHidden(ctx, &tg.StoriesTogglePeerStoriesHiddenRequest{Peer: peer, Hidden: true})
				return err
			},
		},
	}
	for _, peer := range malformedPeers {
		for _, call := range calls {
			if err := call.call(peer); err == nil || !tgerr.Is(err, "PEER_ID_INVALID") {
				t.Fatalf("%s peer %T err = %v, want PEER_ID_INVALID before auth", call.name, peer, err)
			}
		}
	}

	if _, err := r.onStoriesExportStoryLink(ctx, &tg.StoriesExportStoryLinkRequest{
		Peer: &tg.InputPeerEmpty{},
		ID:   0,
	}); err == nil || !tgerr.Is(err, "STORY_ID_INVALID") {
		t.Fatalf("exportStoryLink invalid id err = %v, want STORY_ID_INVALID before peer", err)
	}
	if _, err := r.onStoriesReport(ctx, &tg.StoriesReportRequest{Peer: &tg.InputPeerEmpty{}}); err == nil || !tgerr.Is(err, "STORY_ID_EMPTY") {
		t.Fatalf("report empty ids err = %v, want STORY_ID_EMPTY before peer", err)
	}
	if _, err := r.onStoriesStartLive(ctx, &tg.StoriesStartLiveRequest{
		Peer:     &tg.InputPeerEmpty{},
		RandomID: 0,
	}); err == nil || !tgerr.Is(err, "RANDOM_ID_EMPTY") {
		t.Fatalf("startLive zero random err = %v, want RANDOM_ID_EMPTY before peer", err)
	}
	if _, err := r.onStoriesStartLive(ctx, &tg.StoriesStartLiveRequest{
		Peer:         &tg.InputPeerEmpty{},
		RandomID:     9001,
		PrivacyRules: []tg.InputPrivacyRuleClass{nil},
	}); err == nil || !tgerr.Is(err, "PRIVACY_VALUE_INVALID") {
		t.Fatalf("startLive malformed privacy err = %v, want PRIVACY_VALUE_INVALID before peer", err)
	}
	if _, err := r.onStoriesSearchPosts(ctx, &tg.StoriesSearchPostsRequest{Limit: 10}); err == nil || !tgerr.Is(err, "SEARCH_QUERY_EMPTY") {
		t.Fatalf("searchPosts empty query err = %v, want SEARCH_QUERY_EMPTY before peer", err)
	}
	if _, err := r.onStoriesGetStoryViewsList(ctx, &tg.StoriesGetStoryViewsListRequest{
		Peer: &tg.InputPeerEmpty{},
		ID:   0,
	}); err == nil || !tgerr.Is(err, "STORY_ID_INVALID") {
		t.Fatalf("getStoryViewsList invalid id err = %v, want STORY_ID_INVALID before peer", err)
	}
	invalidReactionListReq := &tg.StoriesGetStoryReactionsListRequest{
		Peer: &tg.InputPeerEmpty{},
		ID:   1,
	}
	invalidReactionListReq.SetReaction(&tg.ReactionCustomEmoji{})
	if _, err := r.onStoriesGetStoryReactionsList(ctx, invalidReactionListReq); err == nil || !tgerr.Is(err, "REACTION_INVALID") {
		t.Fatalf("getStoryReactionsList invalid reaction err = %v, want REACTION_INVALID before peer", err)
	}
}

func TestStoriesTogglePinnedFallbackDedupeIDs(t *testing.T) {
	ctx := context.Background()
	r := New(Config{}, Deps{}, zaptest.NewLogger(t), fixedClock{now: time.Unix(1700000100, 0)})
	reqCtx := WithUserID(ctx, 1000000001)

	got, err := r.onStoriesTogglePinned(reqCtx, &tg.StoriesTogglePinnedRequest{
		Peer:   &tg.InputPeerSelf{},
		ID:     []int{3, 2, 3, 1, 2},
		Pinned: true,
	})
	if err != nil {
		t.Fatalf("toggle pinned fallback: %v", err)
	}
	want := []int{3, 2, 1}
	if len(got) != len(want) {
		t.Fatalf("toggle pinned fallback ids = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("toggle pinned fallback ids = %v, want first-seen dedupe %v", got, want)
		}
	}
}

func TestStoriesProfileAndLongtailRequestsValidateBeforePeerAndFallback(t *testing.T) {
	ctx := WithUserID(context.Background(), 1000000001)
	r := New(Config{}, Deps{}, zaptest.NewLogger(t), fixedClock{now: time.Unix(1700000100, 0)})

	nilCases := []struct {
		name string
		call func() error
	}{
		{name: "get archive", call: func() error {
			_, err := r.onStoriesGetStoriesArchive(ctx, nil)
			return err
		}},
		{name: "get pinned", call: func() error {
			_, err := r.onStoriesGetPinnedStories(ctx, nil)
			return err
		}},
		{name: "export link", call: func() error {
			_, err := r.onStoriesExportStoryLink(ctx, nil)
			return err
		}},
		{name: "get albums", call: func() error {
			_, err := r.onStoriesGetAlbums(ctx, nil)
			return err
		}},
		{name: "start live", call: func() error {
			_, err := r.onStoriesStartLive(ctx, nil)
			return err
		}},
		{name: "toggle pinned", call: func() error {
			_, err := r.onStoriesTogglePinned(ctx, nil)
			return err
		}},
		{name: "toggle pinned to top", call: func() error {
			_, err := r.onStoriesTogglePinnedToTop(ctx, nil)
			return err
		}},
	}
	for _, tc := range nilCases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.call(); err == nil || !tgerr.Is(err, "INPUT_REQUEST_INVALID") {
				t.Fatalf("%s nil err = %v, want INPUT_REQUEST_INVALID", tc.name, err)
			}
		})
	}

	if _, err := r.onStoriesGetStoriesArchive(ctx, &tg.StoriesGetStoriesArchiveRequest{
		Peer:     &tg.InputPeerEmpty{},
		OffsetID: -2,
		Limit:    1,
	}); err == nil || !tgerr.Is(err, "STORY_ID_INVALID") {
		t.Fatalf("archive bad offset before peer err = %v, want STORY_ID_INVALID", err)
	}
	if _, err := r.onStoriesGetPinnedStories(ctx, &tg.StoriesGetPinnedStoriesRequest{
		Peer:     &tg.InputPeerEmpty{},
		OffsetID: 0,
		Limit:    domain.MaxStoryListLimit + 1,
	}); err == nil || !tgerr.Is(err, "LIMIT_INVALID") {
		t.Fatalf("pinned bad limit before peer err = %v, want LIMIT_INVALID", err)
	}
	if _, err := r.onStoriesExportStoryLink(ctx, &tg.StoriesExportStoryLinkRequest{
		Peer: &tg.InputPeerEmpty{},
		ID:   0,
	}); err == nil || !tgerr.Is(err, "STORY_ID_INVALID") {
		t.Fatalf("export bad id before peer err = %v, want STORY_ID_INVALID", err)
	}
	if _, err := r.onStoriesStartLive(ctx, &tg.StoriesStartLiveRequest{
		Peer:     &tg.InputPeerEmpty{},
		RandomID: 0,
	}); err == nil || !tgerr.Is(err, "RANDOM_ID_EMPTY") {
		t.Fatalf("start live zero random before peer err = %v, want RANDOM_ID_EMPTY", err)
	}
	if _, err := r.onStoriesTogglePinned(ctx, &tg.StoriesTogglePinnedRequest{
		Peer: &tg.InputPeerEmpty{},
		ID:   []int{0},
	}); err == nil || !tgerr.Is(err, "STORY_ID_INVALID") {
		t.Fatalf("toggle pinned bad id before peer err = %v, want STORY_ID_INVALID", err)
	}
	if _, err := r.onStoriesTogglePinnedToTop(ctx, &tg.StoriesTogglePinnedToTopRequest{
		Peer: &tg.InputPeerEmpty{},
		ID:   []int{0},
	}); err == nil || !tgerr.Is(err, "STORY_ID_INVALID") {
		t.Fatalf("toggle pinned to top bad id before peer err = %v, want STORY_ID_INVALID", err)
	}
}

func TestStoriesTogglePinnedToTopBoundsBeforeNoop(t *testing.T) {
	ctx := context.Background()
	r := New(Config{}, Deps{}, zaptest.NewLogger(t), fixedClock{now: time.Unix(1700000100, 0)})
	reqCtx := WithUserID(ctx, 1000000001)

	ok, err := r.onStoriesTogglePinnedToTop(reqCtx, &tg.StoriesTogglePinnedToTopRequest{
		Peer: &tg.InputPeerSelf{},
	})
	if err != nil || !ok {
		t.Fatalf("empty togglePinnedToTop = %v, %v; want true nil", ok, err)
	}
	ok, err = r.onStoriesTogglePinnedToTop(reqCtx, &tg.StoriesTogglePinnedToTopRequest{
		Peer: &tg.InputPeerSelf{},
		ID:   []int{2, 1, 2},
	})
	if err != nil || !ok {
		t.Fatalf("valid togglePinnedToTop = %v, %v; want true nil", ok, err)
	}
	if _, err := r.onStoriesTogglePinnedToTop(reqCtx, &tg.StoriesTogglePinnedToTopRequest{
		Peer: &tg.InputPeerSelf{},
		ID:   []int{1, 2, 3, 4},
	}); err == nil || !tgerr.Is(err, "STORY_ID_INVALID") {
		t.Fatalf("over limit togglePinnedToTop err = %v, want STORY_ID_INVALID", err)
	}
	if _, err := r.onStoriesTogglePinnedToTop(reqCtx, &tg.StoriesTogglePinnedToTopRequest{
		Peer: &tg.InputPeerSelf{},
		ID:   []int{0},
	}); err == nil || !tgerr.Is(err, "STORY_ID_INVALID") {
		t.Fatalf("invalid togglePinnedToTop id err = %v, want STORY_ID_INVALID", err)
	}

	oversized := make([]int, domain.MaxStoryIDs+1)
	for i := range oversized {
		oversized[i] = 1
	}
	if _, err := r.onStoriesTogglePinnedToTop(reqCtx, &tg.StoriesTogglePinnedToTopRequest{
		Peer: &tg.InputPeerSelf{},
		ID:   oversized,
	}); err == nil || !tgerr.Is(err, "STORY_ID_INVALID") {
		t.Fatalf("oversized togglePinnedToTop err = %v, want STORY_ID_INVALID", err)
	}
	if _, err := r.onStoriesTogglePinnedToTop(reqCtx, &tg.StoriesTogglePinnedToTopRequest{
		Peer: &tg.InputPeerEmpty{},
		ID:   []int{1},
	}); err == nil || !tgerr.Is(err, "PEER_ID_INVALID") {
		t.Fatalf("invalid togglePinnedToTop peer err = %v, want PEER_ID_INVALID", err)
	}
}

func TestStoriesTogglePinnedToTopPersistsOrder(t *testing.T) {
	ctx := context.Background()
	ownerID := int64(1000000001)
	owner := domain.Peer{Type: domain.PeerTypeUser, ID: ownerID}
	storyStore := memory.NewStoryStore()
	for _, id := range []int{1, 2, 3} {
		if _, err := storyStore.UpsertStory(ctx, domain.UpsertStoryRequest{Story: domain.Story{
			Owner:      owner,
			ID:         id,
			Date:       1700000000 + id,
			ExpireDate: 1700003600,
			Pinned:     true,
			Public:     true,
		}}); err != nil {
			t.Fatalf("upsert pinned story %d: %v", id, err)
		}
	}
	r := New(Config{}, Deps{Stories: appstories.NewService(storyStore)}, zaptest.NewLogger(t), fixedClock{now: time.Unix(1700000100, 0)})
	reqCtx := WithUserID(ctx, ownerID)

	ok, err := r.onStoriesTogglePinnedToTop(reqCtx, &tg.StoriesTogglePinnedToTopRequest{
		Peer: &tg.InputPeerSelf{},
		ID:   []int{2, 1, 2},
	})
	if err != nil || !ok {
		t.Fatalf("toggle pinned to top = %v, %v; want true nil", ok, err)
	}
	pinned, err := r.onStoriesGetPinnedStories(reqCtx, &tg.StoriesGetPinnedStoriesRequest{
		Peer:  &tg.InputPeerSelf{},
		Limit: 10,
	})
	if err != nil {
		t.Fatalf("get pinned stories: %v", err)
	}
	top, topSet := pinned.GetPinnedToTop()
	if !topSet || !sameIntIDs(top, []int{2, 1}) {
		t.Fatalf("pinned_to_top = %v ok=%v, want 2,1", top, topSet)
	}

	if _, err := r.onStoriesTogglePinned(reqCtx, &tg.StoriesTogglePinnedRequest{
		Peer:   &tg.InputPeerSelf{},
		ID:     []int{2},
		Pinned: false,
	}); err != nil {
		t.Fatalf("unpin top story: %v", err)
	}
	pinned, err = r.onStoriesGetPinnedStories(reqCtx, &tg.StoriesGetPinnedStoriesRequest{
		Peer:  &tg.InputPeerSelf{},
		Limit: 10,
	})
	if err != nil {
		t.Fatalf("get pinned stories after unpin: %v", err)
	}
	top, topSet = pinned.GetPinnedToTop()
	if !topSet || !sameIntIDs(top, []int{1}) {
		t.Fatalf("pinned_to_top after unpin = %v ok=%v, want 1", top, topSet)
	}
	if _, err := r.onStoriesTogglePinnedToTop(reqCtx, &tg.StoriesTogglePinnedToTopRequest{
		Peer: &tg.InputPeerSelf{},
		ID:   []int{2},
	}); err == nil || !tgerr.Is(err, "STORY_ID_INVALID") {
		t.Fatalf("unpinned story top err = %v, want STORY_ID_INVALID", err)
	}

	ok, err = r.onStoriesTogglePinnedToTop(reqCtx, &tg.StoriesTogglePinnedToTopRequest{
		Peer: &tg.InputPeerSelf{},
	})
	if err != nil || !ok {
		t.Fatalf("clear pinned to top = %v, %v; want true nil", ok, err)
	}
	pinned, err = r.onStoriesGetPinnedStories(reqCtx, &tg.StoriesGetPinnedStoriesRequest{
		Peer:  &tg.InputPeerSelf{},
		Limit: 10,
	})
	if err != nil {
		t.Fatalf("get pinned stories after clear: %v", err)
	}
	top, topSet = pinned.GetPinnedToTop()
	if topSet || len(top) != 0 {
		t.Fatalf("pinned_to_top after clear = %v ok=%v, want absent", top, topSet)
	}
}

func TestStoriesPinnedArchiveBoundsBeforeFallback(t *testing.T) {
	ctx := WithUserID(context.Background(), 1000000001)
	r := New(Config{}, Deps{}, zaptest.NewLogger(t), fixedClock{now: time.Unix(1700000100, 0)})

	callPinned := func(offsetID, limit int) error {
		_, err := r.onStoriesGetPinnedStories(ctx, &tg.StoriesGetPinnedStoriesRequest{
			Peer:     &tg.InputPeerSelf{},
			OffsetID: offsetID,
			Limit:    limit,
		})
		return err
	}
	callArchive := func(offsetID, limit int) error {
		_, err := r.onStoriesGetStoriesArchive(ctx, &tg.StoriesGetStoriesArchiveRequest{
			Peer:     &tg.InputPeerSelf{},
			OffsetID: offsetID,
			Limit:    limit,
		})
		return err
	}

	if err := callPinned(-1, 0); err != nil {
		t.Fatalf("pinned Android sentinel bounds err = %v, want nil", err)
	}
	if err := callArchive(-1, 0); err != nil {
		t.Fatalf("archive Android sentinel bounds err = %v, want nil", err)
	}

	tests := []struct {
		name     string
		offsetID int
		limit    int
		want     string
	}{
		{name: "offset below android sentinel", offsetID: -2, limit: 1, want: "STORY_ID_INVALID"},
		{name: "offset above max story id", offsetID: domain.MaxStoryID + 1, limit: 1, want: "STORY_ID_INVALID"},
		{name: "negative limit", offsetID: 0, limit: -1, want: "LIMIT_INVALID"},
		{name: "limit above max", offsetID: 0, limit: domain.MaxStoryListLimit + 1, want: "LIMIT_INVALID"},
	}
	for _, tt := range tests {
		t.Run(tt.name+"/pinned", func(t *testing.T) {
			if err := callPinned(tt.offsetID, tt.limit); err == nil || !tgerr.Is(err, tt.want) {
				t.Fatalf("pinned err = %v, want %s", err, tt.want)
			}
		})
		t.Run(tt.name+"/archive", func(t *testing.T) {
			if err := callArchive(tt.offsetID, tt.limit); err == nil || !tgerr.Is(err, tt.want) {
				t.Fatalf("archive err = %v, want %s", err, tt.want)
			}
		})
	}
}

func TestStoriesDeleteStoriesRejectsEmptyIDsBeforeFallback(t *testing.T) {
	ctx := context.Background()
	reqCtx := WithUserID(ctx, 1000000001)
	req := &tg.StoriesDeleteStoriesRequest{Peer: &tg.InputPeerSelf{}}

	emptyRouter := New(Config{}, Deps{}, zaptest.NewLogger(t), fixedClock{now: time.Unix(1700000100, 0)})
	if _, err := emptyRouter.onStoriesDeleteStories(reqCtx, req); err == nil || !tgerr.Is(err, "STORY_ID_EMPTY") {
		t.Fatalf("empty deleteStories fallback err = %v, want STORY_ID_EMPTY", err)
	}

	serviceRouter := New(Config{}, Deps{
		Stories: appstories.NewService(memory.NewStoryStore()),
	}, zaptest.NewLogger(t), fixedClock{now: time.Unix(1700000100, 0)})
	if _, err := serviceRouter.onStoriesDeleteStories(reqCtx, req); err == nil || !tgerr.Is(err, "STORY_ID_EMPTY") {
		t.Fatalf("empty deleteStories service err = %v, want STORY_ID_EMPTY", err)
	}
}

func TestStoriesDeleteStoriesDedupeIDsBeforeFallback(t *testing.T) {
	ctx := context.Background()
	r := New(Config{}, Deps{}, zaptest.NewLogger(t), fixedClock{now: time.Unix(1700000100, 0)})

	deleted, err := r.onStoriesDeleteStories(WithUserID(ctx, 1000000001), &tg.StoriesDeleteStoriesRequest{
		Peer: &tg.InputPeerSelf{},
		ID:   []int{2, 1, 2, 1},
	})
	if err != nil {
		t.Fatalf("deleteStories fallback duplicate ids: %v", err)
	}
	if len(deleted) != 2 || deleted[0] != 2 || deleted[1] != 1 {
		t.Fatalf("deleteStories fallback ids = %v, want [2 1]", deleted)
	}
}

func TestStoriesWriteRequestsValidateBeforePeerAndFallback(t *testing.T) {
	ctx := WithUserID(context.Background(), 1000000001)
	r := New(Config{}, Deps{}, zaptest.NewLogger(t), fixedClock{now: time.Unix(1700000100, 0)})

	if _, err := r.onStoriesSendStory(ctx, nil); err == nil || !tgerr.Is(err, "INPUT_REQUEST_INVALID") {
		t.Fatalf("nil sendStory err = %v, want INPUT_REQUEST_INVALID", err)
	}
	if _, err := r.onStoriesEditStory(ctx, nil); err == nil || !tgerr.Is(err, "INPUT_REQUEST_INVALID") {
		t.Fatalf("nil editStory err = %v, want INPUT_REQUEST_INVALID", err)
	}
	if _, err := r.onStoriesDeleteStories(ctx, nil); err == nil || !tgerr.Is(err, "INPUT_REQUEST_INVALID") {
		t.Fatalf("nil deleteStories err = %v, want INPUT_REQUEST_INVALID", err)
	}

	if _, err := r.onStoriesSendStory(ctx, &tg.StoriesSendStoryRequest{
		Peer:     &tg.InputPeerEmpty{},
		RandomID: 0,
	}); err == nil || !tgerr.Is(err, "RANDOM_ID_EMPTY") {
		t.Fatalf("sendStory zero random before peer err = %v, want RANDOM_ID_EMPTY", err)
	}
	if _, err := r.onStoriesEditStory(ctx, &tg.StoriesEditStoryRequest{
		Peer: &tg.InputPeerEmpty{},
		ID:   0,
	}); err == nil || !tgerr.Is(err, "STORY_ID_INVALID") {
		t.Fatalf("editStory invalid id before peer err = %v, want STORY_ID_INVALID", err)
	}
	if _, err := r.onStoriesDeleteStories(ctx, &tg.StoriesDeleteStoriesRequest{
		Peer: &tg.InputPeerEmpty{},
	}); err == nil || !tgerr.Is(err, "STORY_ID_EMPTY") {
		t.Fatalf("deleteStories empty ids before peer err = %v, want STORY_ID_EMPTY", err)
	}
	if _, err := r.onStoriesDeleteStories(ctx, &tg.StoriesDeleteStoriesRequest{
		Peer: &tg.InputPeerEmpty{},
		ID:   []int{0},
	}); err == nil || !tgerr.Is(err, "STORY_ID_INVALID") {
		t.Fatalf("deleteStories invalid id before peer err = %v, want STORY_ID_INVALID", err)
	}
}

func TestStoriesRejectTypedNilInputPeers(t *testing.T) {
	ctx := context.Background()
	storyStore := memory.NewStoryStore()
	r := New(Config{}, Deps{
		Stories: appstories.NewService(storyStore),
	}, zaptest.NewLogger(t), fixedClock{now: time.Unix(1700000101, 0)})
	reqCtx := WithUserID(ctx, 1000000001)

	peers := []struct {
		name string
		peer tg.InputPeerClass
	}{
		{name: "nil", peer: nil},
		{name: "typed nil empty", peer: (*tg.InputPeerEmpty)(nil)},
		{name: "typed nil self", peer: (*tg.InputPeerSelf)(nil)},
		{name: "typed nil chat", peer: (*tg.InputPeerChat)(nil)},
		{name: "typed nil user", peer: (*tg.InputPeerUser)(nil)},
		{name: "typed nil channel", peer: (*tg.InputPeerChannel)(nil)},
		{name: "typed nil user from message", peer: (*tg.InputPeerUserFromMessage)(nil)},
		{name: "typed nil channel from message", peer: (*tg.InputPeerChannelFromMessage)(nil)},
	}
	calls := []struct {
		name string
		call func(t *testing.T, peer tg.InputPeerClass) error
	}{
		{
			name: "getPeerStories",
			call: func(t *testing.T, peer tg.InputPeerClass) error {
				_, err := r.onStoriesGetPeerStories(reqCtx, peer)
				return err
			},
		},
		{
			name: "getPeerMaxIDs",
			call: func(t *testing.T, peer tg.InputPeerClass) error {
				_, err := r.onStoriesGetPeerMaxIDs(reqCtx, []tg.InputPeerClass{peer})
				return err
			},
		},
		{
			name: "readStories",
			call: func(t *testing.T, peer tg.InputPeerClass) error {
				_, err := r.onStoriesReadStories(reqCtx, &tg.StoriesReadStoriesRequest{Peer: peer, MaxID: 1})
				return err
			},
		},
		{
			name: "incrementStoryViews",
			call: func(t *testing.T, peer tg.InputPeerClass) error {
				_, err := r.onStoriesIncrementStoryViews(reqCtx, &tg.StoriesIncrementStoryViewsRequest{Peer: peer, ID: []int{1}})
				return err
			},
		},
		{
			name: "getStoriesViews",
			call: func(t *testing.T, peer tg.InputPeerClass) error {
				_, err := r.onStoriesGetStoriesViews(reqCtx, &tg.StoriesGetStoriesViewsRequest{Peer: peer, ID: []int{1}})
				return err
			},
		},
		{
			name: "sendStory",
			call: func(t *testing.T, peer tg.InputPeerClass) error {
				_, err := r.onStoriesSendStory(reqCtx, &tg.StoriesSendStoryRequest{
					Peer:         peer,
					Media:        &tg.InputMediaUploadedPhoto{File: &tg.InputFile{ID: 9001, Parts: 1, Name: "story.jpg"}},
					PrivacyRules: []tg.InputPrivacyRuleClass{&tg.InputPrivacyValueAllowAll{}},
					RandomID:     9001,
					Period:       86400,
				})
				return err
			},
		},
		{
			name: "editStory",
			call: func(t *testing.T, peer tg.InputPeerClass) error {
				_, err := r.onStoriesEditStory(reqCtx, &tg.StoriesEditStoryRequest{Peer: peer, ID: 1})
				return err
			},
		},
		{
			name: "deleteStories",
			call: func(t *testing.T, peer tg.InputPeerClass) error {
				_, err := r.onStoriesDeleteStories(reqCtx, &tg.StoriesDeleteStoriesRequest{Peer: peer, ID: []int{1}})
				return err
			},
		},
		{
			name: "togglePinned",
			call: func(t *testing.T, peer tg.InputPeerClass) error {
				_, err := r.onStoriesTogglePinned(reqCtx, &tg.StoriesTogglePinnedRequest{Peer: peer, ID: []int{1}})
				return err
			},
		},
		{
			name: "togglePinnedToTop",
			call: func(t *testing.T, peer tg.InputPeerClass) error {
				_, err := r.onStoriesTogglePinnedToTop(reqCtx, &tg.StoriesTogglePinnedToTopRequest{Peer: peer, ID: []int{1}})
				return err
			},
		},
	}
	for _, peerCase := range peers {
		for _, callCase := range calls {
			t.Run(peerCase.name+"/"+callCase.name, func(t *testing.T) {
				if err := callCase.call(t, peerCase.peer); err == nil || !tgerr.Is(err, "PEER_ID_INVALID") {
					t.Fatalf("%s with %s err = %v, want PEER_ID_INVALID", callCase.name, peerCase.name, err)
				}
			})
		}
	}

	stories, err := r.onStoriesGetPeerStories(reqCtx, &tg.InputPeerSelf{})
	if err != nil {
		t.Fatalf("get self stories after rejected typed nil peers: %v", err)
	}
	if len(stories.Stories.Stories) != 0 {
		t.Fatalf("stories after rejected typed nil peers = %+v, want none", stories.Stories.Stories)
	}
}

func TestStoriesSelfUserViewAndReactionDoNotPolluteOwnerInteractions(t *testing.T) {
	ctx := context.Background()
	storyStore := memory.NewStoryStore()
	owner := domain.Peer{Type: domain.PeerTypeUser, ID: 1000000001}
	if _, err := storyStore.UpsertStory(ctx, domain.UpsertStoryRequest{Story: domain.Story{
		Owner:      owner,
		ID:         1,
		Date:       1700000000,
		ExpireDate: 1700003600,
		Public:     true,
	}}); err != nil {
		t.Fatalf("upsert story: %v", err)
	}
	r := New(Config{}, Deps{Stories: appstories.NewService(storyStore)}, zaptest.NewLogger(t), fixedClock{now: time.Unix(1700000100, 0)})
	reqCtx := WithUserID(ctx, owner.ID)

	ok, err := r.onStoriesIncrementStoryViews(reqCtx, &tg.StoriesIncrementStoryViewsRequest{
		Peer: &tg.InputPeerSelf{},
		ID:   []int{1},
	})
	if err != nil || !ok {
		t.Fatalf("increment self story views = %v, %v; want true nil", ok, err)
	}
	views, err := r.onStoriesGetStoryViewsList(reqCtx, &tg.StoriesGetStoryViewsListRequest{
		Peer:  &tg.InputPeerSelf{},
		ID:    1,
		Limit: 10,
	})
	if err != nil {
		t.Fatalf("get self story views list: %v", err)
	}
	if views.Count != 0 || views.ViewsCount != 0 || views.ReactionsCount != 0 || len(views.Views) != 0 {
		t.Fatalf("self story views = %+v, want empty counters", views)
	}
	if _, err := r.onStoriesSendReaction(reqCtx, &tg.StoriesSendReactionRequest{
		Peer:     &tg.InputPeerSelf{},
		StoryID:  1,
		Reaction: &tg.ReactionEmoji{Emoticon: "🔥"},
	}); err == nil || !tgerr.Is(err, "PEER_ID_INVALID") {
		t.Fatalf("self story reaction err = %v, want PEER_ID_INVALID", err)
	}
}

func TestStoriesViewsListMarksStoryBlockedViewer(t *testing.T) {
	ctx := context.Background()
	storyStore := memory.NewStoryStore()
	owner := domain.Peer{Type: domain.PeerTypeUser, ID: 1000000001}
	viewerID := int64(1000000002)
	if _, err := storyStore.UpsertStory(ctx, domain.UpsertStoryRequest{Story: domain.Story{
		Owner:      owner,
		ID:         1,
		Date:       1700000000,
		ExpireDate: 1700003600,
		Public:     true,
	}}); err != nil {
		t.Fatalf("upsert story: %v", err)
	}
	if created, err := storyStore.IncrementViews(ctx, viewerID, owner, []int{1}, 1700000100); err != nil || created != 1 {
		t.Fatalf("increment story view = %d, %v, want 1 nil", created, err)
	}
	storyStore.SetStoryBlockedUsers(owner.ID, viewerID)
	r := New(Config{}, Deps{Stories: appstories.NewService(storyStore)}, zaptest.NewLogger(t), fixedClock{now: time.Unix(1700000200, 0)})

	views, err := r.onStoriesGetStoryViewsList(WithUserID(ctx, owner.ID), &tg.StoriesGetStoryViewsListRequest{
		Peer:  &tg.InputPeerSelf{},
		ID:    1,
		Limit: 10,
	})
	if err != nil {
		t.Fatalf("get story views list: %v", err)
	}
	if views.Count != 1 || len(views.Views) != 1 {
		t.Fatalf("story views = %+v, want one historical viewer", views)
	}
	view, ok := views.Views[0].(*tg.StoryView)
	if !ok {
		t.Fatalf("story view = %T, want storyView", views.Views[0])
	}
	if !view.BlockedMyStoriesFrom || view.Blocked {
		t.Fatalf("story view flags = blocked %v blocked_my_stories_from %v, want only story block", view.Blocked, view.BlockedMyStoriesFrom)
	}
}

func TestStoriesSendReactionAllowsEmptyClear(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	ownerUser, err := userStore.Create(ctx, domain.User{AccessHash: 91001, Phone: "15550091001", FirstName: "Owner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	viewer, err := userStore.Create(ctx, domain.User{AccessHash: 91002, Phone: "15550091002", FirstName: "Viewer"})
	if err != nil {
		t.Fatalf("create viewer: %v", err)
	}
	storyStore := memory.NewStoryStore()
	owner := domain.Peer{Type: domain.PeerTypeUser, ID: ownerUser.ID}
	if _, err := storyStore.UpsertStory(ctx, domain.UpsertStoryRequest{Story: domain.Story{
		Owner:      owner,
		ID:         1,
		Date:       1700000000,
		ExpireDate: 1700003600,
		Public:     true,
	}}); err != nil {
		t.Fatalf("upsert story: %v", err)
	}
	updateStore := memory.NewUpdateEventStore()
	r := New(Config{}, Deps{
		Users:   appusers.NewService(userStore),
		Stories: appstories.NewService(storyStore),
		Updates: appupdates.NewService(memory.NewUpdateStateStore(), updateStore),
	}, zaptest.NewLogger(t), fixedClock{now: time.Unix(1700000100, 0)})

	viewerCtx := WithAuthKeyID(WithUserID(ctx, viewer.ID), [8]byte{9, 1, 2})
	updates, err := r.onStoriesSendReaction(viewerCtx, &tg.StoriesSendReactionRequest{
		Peer:     &tg.InputPeerUser{UserID: owner.ID, AccessHash: ownerUser.AccessHash},
		StoryID:  1,
		Reaction: &tg.ReactionEmpty{},
	})
	if err != nil {
		t.Fatalf("send empty reaction: %v", err)
	}
	got, ok := updates.(*tg.Updates)
	if !ok || len(got.Updates) != 1 {
		t.Fatalf("updates = %T %+v, want one *tg.Updates", updates, updates)
	}
	reaction, ok := got.Updates[0].(*tg.UpdateSentStoryReaction)
	if !ok {
		t.Fatalf("update = %T, want updateSentStoryReaction", got.Updates[0])
	}
	if _, ok := reaction.Reaction.(*tg.ReactionEmpty); !ok {
		t.Fatalf("reaction = %T, want reactionEmpty", reaction.Reaction)
	}
	assertUserStoryMaxID(t, findUserClass(got.Users, owner.ID), 1)
	diff, err := r.onUpdatesGetDifference(viewerCtx, &tg.UpdatesGetDifferenceRequest{Pts: 0})
	if err != nil {
		t.Fatalf("viewer get difference after empty clear: %v", err)
	}
	if _, ok := diff.(*tg.UpdatesDifferenceEmpty); !ok {
		t.Fatalf("viewer difference after empty clear = %T %+v, want empty", diff, diff)
	}
	events, err := updateStore.ListAfter(ctx, viewer.ID, 0, 10)
	if err != nil {
		t.Fatalf("list viewer events after empty clear: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("viewer events after empty clear = %+v, want none", events)
	}
	ownerEvents, err := updateStore.ListAfter(ctx, owner.ID, 0, 10)
	if err != nil {
		t.Fatalf("list owner events after empty clear: %v", err)
	}
	if len(ownerEvents) != 0 {
		t.Fatalf("owner events after empty clear = %+v, want none", ownerEvents)
	}
}

func TestStoriesSendReactionRejectsUnsupportedReactionsBeforeFallback(t *testing.T) {
	ctx := WithUserID(context.Background(), 91003)
	r := New(Config{}, Deps{}, zaptest.NewLogger(t), fixedClock{now: time.Unix(1700000100, 0)})

	if _, err := r.onStoriesSendReaction(ctx, nil); err == nil || !tgerr.Is(err, "INPUT_REQUEST_INVALID") {
		t.Fatalf("nil send story reaction err = %v, want INPUT_REQUEST_INVALID", err)
	}
	if _, err := r.onStoriesSendReaction(ctx, &tg.StoriesSendReactionRequest{
		Peer:     &tg.InputPeerEmpty{},
		StoryID:  0,
		Reaction: &tg.ReactionEmoji{Emoticon: "🔥"},
	}); err == nil || !tgerr.Is(err, "STORY_ID_INVALID") {
		t.Fatalf("invalid story reaction id before peer err = %v, want STORY_ID_INVALID", err)
	}

	for _, tt := range []struct {
		name     string
		reaction tg.ReactionClass
	}{
		{name: "custom emoji without document id", reaction: &tg.ReactionCustomEmoji{}},
		{name: "paid", reaction: &tg.ReactionPaid{}},
		{name: "nil", reaction: nil},
		{name: "typed nil emoji", reaction: (*tg.ReactionEmoji)(nil)},
		{name: "typed nil custom emoji", reaction: (*tg.ReactionCustomEmoji)(nil)},
		{name: "typed nil empty", reaction: (*tg.ReactionEmpty)(nil)},
		{name: "typed nil paid", reaction: (*tg.ReactionPaid)(nil)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := r.onStoriesSendReaction(ctx, &tg.StoriesSendReactionRequest{
				Peer:     &tg.InputPeerEmpty{},
				StoryID:  1,
				Reaction: tt.reaction,
			})
			if err == nil || !tgerr.Is(err, "REACTION_INVALID") {
				t.Fatalf("send unsupported story reaction before peer err = %v, want REACTION_INVALID", err)
			}
		})
	}
}

func TestStoriesSendReactionRecordsUsageOnlyAfterStorySuccess(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	ownerUser, err := userStore.Create(ctx, domain.User{AccessHash: 91011, Phone: "15550091011", FirstName: "Owner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	viewer, err := userStore.Create(ctx, domain.User{AccessHash: 91012, Phone: "15550091012", FirstName: "Viewer"})
	if err != nil {
		t.Fatalf("create viewer: %v", err)
	}
	storyStore := memory.NewStoryStore()
	channelService := appchannels.NewService(memory.NewChannelStore())
	r := New(Config{}, Deps{
		Users:    appusers.NewService(userStore),
		Stories:  appstories.NewService(storyStore),
		Channels: channelService,
	}, zaptest.NewLogger(t), fixedClock{now: time.Unix(1700000100, 0)})

	missingStoryReaction := &tg.StoriesSendReactionRequest{
		Peer:     &tg.InputPeerUser{UserID: ownerUser.ID, AccessHash: ownerUser.AccessHash},
		StoryID:  1,
		Reaction: &tg.ReactionEmoji{Emoticon: "\U0001f44d"},
	}
	missingStoryReaction.SetAddToRecent(true)
	_, err = r.onStoriesSendReaction(WithUserID(ctx, viewer.ID), missingStoryReaction)
	if err == nil || !tgerr.Is(err, "STORY_ID_INVALID") {
		t.Fatalf("missing story reaction err = %v, want STORY_ID_INVALID", err)
	}

	top, err := channelService.TopReactions(ctx, viewer.ID, 10)
	if err != nil {
		t.Fatalf("top reactions after failed story reaction: %v", err)
	}
	if len(top) != 0 {
		t.Fatalf("top reactions after failed story reaction = %+v, want empty", top)
	}
	recent, err := r.onMessagesGetRecentReactions(WithUserID(ctx, viewer.ID), &tg.MessagesGetRecentReactionsRequest{Limit: 40})
	if err != nil {
		t.Fatalf("recent reactions after failed story reaction: %v", err)
	}
	page, ok := recent.(*tg.MessagesReactions)
	if !ok || page.Hash != 0 || len(page.Reactions) != 0 {
		t.Fatalf("recent reactions after failed story reaction = %#v, want empty page", recent)
	}

	owner := domain.Peer{Type: domain.PeerTypeUser, ID: ownerUser.ID}
	if _, err := storyStore.UpsertStory(ctx, domain.UpsertStoryRequest{Story: domain.Story{
		Owner:      owner,
		ID:         1,
		Date:       1700000000,
		ExpireDate: 1700003600,
		Public:     true,
	}}); err != nil {
		t.Fatalf("upsert story: %v", err)
	}
	successfulStoryReaction := &tg.StoriesSendReactionRequest{
		Peer:     &tg.InputPeerUser{UserID: ownerUser.ID, AccessHash: ownerUser.AccessHash},
		StoryID:  1,
		Reaction: &tg.ReactionEmoji{Emoticon: "\U0001f44d"},
	}
	successfulStoryReaction.SetAddToRecent(true)
	if _, err := r.onStoriesSendReaction(WithUserID(ctx, viewer.ID), successfulStoryReaction); err != nil {
		t.Fatalf("successful story reaction: %v", err)
	}
	top, err = channelService.TopReactions(ctx, viewer.ID, 10)
	if err != nil {
		t.Fatalf("top reactions after successful story reaction: %v", err)
	}
	if len(top) != 1 || top[0].Emoticon != "\U0001f44d" {
		t.Fatalf("top reactions after successful story reaction = %+v, want thumb", top)
	}
	recent, err = r.onMessagesGetRecentReactions(WithUserID(ctx, viewer.ID), &tg.MessagesGetRecentReactionsRequest{Limit: 40})
	if err != nil {
		t.Fatalf("recent reactions after successful story reaction: %v", err)
	}
	page, ok = recent.(*tg.MessagesReactions)
	if !ok || page.Hash == 0 || len(page.Reactions) != 1 {
		t.Fatalf("recent reactions after successful story reaction = %#v, want one hashable reaction", recent)
	}
	if emoji, ok := page.Reactions[0].(*tg.ReactionEmoji); !ok || emoji.Emoticon != "\U0001f44d" {
		t.Fatalf("recent reaction after successful story reaction = %#v, want thumb", page.Reactions[0])
	}

	if _, err := storyStore.UpsertStory(ctx, domain.UpsertStoryRequest{Story: domain.Story{
		Owner:      owner,
		ID:         2,
		Date:       1700000001,
		ExpireDate: 1700003600,
		Public:     true,
	}}); err != nil {
		t.Fatalf("upsert second story: %v", err)
	}
	laterRouter := New(Config{}, Deps{
		Users:    appusers.NewService(userStore),
		Stories:  appstories.NewService(storyStore),
		Channels: channelService,
	}, zaptest.NewLogger(t), fixedClock{now: time.Unix(1700000200, 0)})
	fireStoryReaction := &tg.StoriesSendReactionRequest{
		Peer:     &tg.InputPeerUser{UserID: ownerUser.ID, AccessHash: ownerUser.AccessHash},
		StoryID:  2,
		Reaction: &tg.ReactionEmoji{Emoticon: "🔥"},
	}
	fireStoryReaction.SetAddToRecent(true)
	if _, err := laterRouter.onStoriesSendReaction(WithUserID(ctx, viewer.ID), fireStoryReaction); err != nil {
		t.Fatalf("fire story reaction: %v", err)
	}
	retryRouter := New(Config{}, Deps{
		Users:    appusers.NewService(userStore),
		Stories:  appstories.NewService(storyStore),
		Channels: channelService,
	}, zaptest.NewLogger(t), fixedClock{now: time.Unix(1700000300, 0)})
	if _, err := retryRouter.onStoriesSendReaction(WithUserID(ctx, viewer.ID), successfulStoryReaction); err != nil {
		t.Fatalf("retry same story reaction: %v", err)
	}
	top, err = channelService.TopReactions(ctx, viewer.ID, 10)
	if err != nil {
		t.Fatalf("top reactions after duplicate story reaction: %v", err)
	}
	if len(top) < 2 || top[0].Emoticon != "🔥" || top[1].Emoticon != "\U0001f44d" {
		t.Fatalf("top reactions after duplicate story reaction = %+v, want fire before unchanged thumb", top)
	}
	recent, err = retryRouter.onMessagesGetRecentReactions(WithUserID(ctx, viewer.ID), &tg.MessagesGetRecentReactionsRequest{Limit: 40})
	if err != nil {
		t.Fatalf("recent reactions after duplicate story reaction: %v", err)
	}
	page, ok = recent.(*tg.MessagesReactions)
	if !ok || len(page.Reactions) < 2 {
		t.Fatalf("recent reactions after duplicate story reaction = %#v, want two reactions", recent)
	}
	if emoji, ok := page.Reactions[0].(*tg.ReactionEmoji); !ok || emoji.Emoticon != "🔥" {
		t.Fatalf("recent first after duplicate story reaction = %#v, want fire", page.Reactions[0])
	}

	if _, err := storyStore.UpsertStory(ctx, domain.UpsertStoryRequest{Story: domain.Story{
		Owner:      owner,
		ID:         3,
		Date:       1700000002,
		ExpireDate: 1700003600,
		Public:     true,
	}}); err != nil {
		t.Fatalf("upsert custom reaction story: %v", err)
	}
	customRouter := New(Config{}, Deps{
		Users:    appusers.NewService(userStore),
		Stories:  appstories.NewService(storyStore),
		Channels: channelService,
	}, zaptest.NewLogger(t), fixedClock{now: time.Unix(1700000400, 0)})
	customStoryReaction := &tg.StoriesSendReactionRequest{
		Peer:     &tg.InputPeerUser{UserID: ownerUser.ID, AccessHash: ownerUser.AccessHash},
		StoryID:  3,
		Reaction: &tg.ReactionCustomEmoji{DocumentID: 12345},
	}
	customStoryReaction.SetAddToRecent(true)
	if _, err := customRouter.onStoriesSendReaction(WithUserID(ctx, viewer.ID), customStoryReaction); err != nil {
		t.Fatalf("custom story reaction: %v", err)
	}
	top, err = channelService.TopReactions(ctx, viewer.ID, 10)
	if err != nil {
		t.Fatalf("top reactions after custom story reaction: %v", err)
	}
	if len(top) < 2 || top[0].Emoticon != "🔥" || top[1].Emoticon != "\U0001f44d" {
		t.Fatalf("top reactions after custom story reaction = %+v, want unchanged emoji-only top reactions", top)
	}
	recent, err = customRouter.onMessagesGetRecentReactions(WithUserID(ctx, viewer.ID), &tg.MessagesGetRecentReactionsRequest{Limit: 40})
	if err != nil {
		t.Fatalf("recent reactions after custom story reaction: %v", err)
	}
	page, ok = recent.(*tg.MessagesReactions)
	if !ok || len(page.Reactions) < 2 {
		t.Fatalf("recent reactions after custom story reaction = %#v, want unchanged emoji reactions", recent)
	}
	if emoji, ok := page.Reactions[0].(*tg.ReactionEmoji); !ok || emoji.Emoticon != "🔥" {
		t.Fatalf("recent first after custom story reaction = %#v, want fire", page.Reactions[0])
	}
}

func TestStoriesSendReactionRecordsOwnerNewReactionDifference(t *testing.T) {
	ctx := context.Background()
	ownerAuthKeyID := [8]byte{7, 7, 1}
	viewerAuthKeyID := [8]byte{7, 7, 2}
	userStore := memory.NewUserStore()
	ownerUser, err := userStore.Create(ctx, domain.User{AccessHash: 91101, Phone: "15550091101", FirstName: "Owner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	viewer, err := userStore.Create(ctx, domain.User{AccessHash: 91102, Phone: "15550091102", FirstName: "Viewer"})
	if err != nil {
		t.Fatalf("create viewer: %v", err)
	}
	storyStore := memory.NewStoryStore()
	owner := domain.Peer{Type: domain.PeerTypeUser, ID: ownerUser.ID}
	if _, err := storyStore.UpsertStory(ctx, domain.UpsertStoryRequest{Story: domain.Story{
		Owner:      owner,
		ID:         1,
		Date:       1700000000,
		ExpireDate: 1700003600,
		Public:     true,
	}}); err != nil {
		t.Fatalf("upsert story: %v", err)
	}
	r := New(Config{}, Deps{
		Users:   appusers.NewService(userStore),
		Stories: appstories.NewService(storyStore),
		Updates: appupdates.NewService(memory.NewUpdateStateStore(), memory.NewUpdateEventStore()),
	}, zaptest.NewLogger(t), fixedClock{now: time.Unix(1700000100, 0)})
	viewerCtx := WithSessionID(WithAuthKeyID(WithUserID(ctx, viewer.ID), viewerAuthKeyID), 7102)

	updates, err := r.onStoriesSendReaction(viewerCtx, &tg.StoriesSendReactionRequest{
		Peer:     &tg.InputPeerUser{UserID: owner.ID, AccessHash: ownerUser.AccessHash},
		StoryID:  1,
		Reaction: &tg.ReactionCustomEmoji{DocumentID: 12345},
	})
	if err != nil {
		t.Fatalf("send reaction: %v", err)
	}
	gotUpdates, ok := updates.(*tg.Updates)
	if !ok || len(gotUpdates.Updates) != 1 {
		t.Fatalf("send updates = %T %+v, want one updateSentStoryReaction", updates, updates)
	}
	sentReaction, ok := gotUpdates.Updates[0].(*tg.UpdateSentStoryReaction)
	if !ok {
		t.Fatalf("send update = %T, want updateSentStoryReaction", gotUpdates.Updates[0])
	}
	if reaction, ok := sentReaction.Reaction.(*tg.ReactionCustomEmoji); !ok || reaction.DocumentID != 12345 {
		t.Fatalf("send update reaction = %T %+v, want custom emoji 12345", sentReaction.Reaction, sentReaction.Reaction)
	}

	ownerCtx := WithAuthKeyID(WithUserID(ctx, owner.ID), ownerAuthKeyID)
	diff, err := r.onUpdatesGetDifference(ownerCtx, &tg.UpdatesGetDifferenceRequest{Pts: 0})
	if err != nil {
		t.Fatalf("owner get difference: %v", err)
	}
	got, ok := diff.(*tg.UpdatesDifference)
	if !ok {
		t.Fatalf("difference = %T, want updates.difference", diff)
	}
	if got.State.Pts != 1 || len(got.OtherUpdates) != 1 {
		t.Fatalf("difference pts/updates = %d/%d, want one owner reaction update", got.State.Pts, len(got.OtherUpdates))
	}
	ownerUpdate, ok := got.OtherUpdates[0].(*tg.UpdateNewStoryReaction)
	if !ok {
		t.Fatalf("owner update = %T, want updateNewStoryReaction", got.OtherUpdates[0])
	}
	reactorPeer, ok := ownerUpdate.Peer.(*tg.PeerUser)
	if !ok || reactorPeer.UserID != viewer.ID {
		t.Fatalf("owner update peer = %T %+v, want reacting viewer peer", ownerUpdate.Peer, ownerUpdate.Peer)
	}
	reaction, ok := ownerUpdate.Reaction.(*tg.ReactionCustomEmoji)
	if want := clientDocumentIDFromServerID(12345); !ok || reaction.DocumentID != want {
		t.Fatalf("owner update reaction = %T %+v, want custom emoji %d", ownerUpdate.Reaction, ownerUpdate.Reaction, want)
	}
	if findUserClass(got.Users, viewer.ID) == nil {
		t.Fatalf("difference users = %+v, want reacting viewer user", got.Users)
	}

	if _, err := r.onStoriesSendReaction(viewerCtx, &tg.StoriesSendReactionRequest{
		Peer:     &tg.InputPeerUser{UserID: owner.ID, AccessHash: ownerUser.AccessHash},
		StoryID:  1,
		Reaction: &tg.ReactionCustomEmoji{DocumentID: 12345},
	}); err != nil {
		t.Fatalf("retry same reaction: %v", err)
	}
	diff, err = r.onUpdatesGetDifference(ownerCtx, &tg.UpdatesGetDifferenceRequest{Pts: got.State.Pts})
	if err != nil {
		t.Fatalf("owner get difference after same reaction retry: %v", err)
	}
	if _, ok := diff.(*tg.UpdatesDifferenceEmpty); !ok {
		t.Fatalf("difference after same reaction retry = %T %+v, want empty owner difference", diff, diff)
	}

	if _, err := r.onStoriesSendReaction(viewerCtx, &tg.StoriesSendReactionRequest{
		Peer:     &tg.InputPeerUser{UserID: owner.ID, AccessHash: ownerUser.AccessHash},
		StoryID:  1,
		Reaction: &tg.ReactionEmpty{},
	}); err != nil {
		t.Fatalf("clear reaction: %v", err)
	}
	diff, err = r.onUpdatesGetDifference(ownerCtx, &tg.UpdatesGetDifferenceRequest{Pts: got.State.Pts})
	if err != nil {
		t.Fatalf("owner get difference after clear: %v", err)
	}
	if _, ok := diff.(*tg.UpdatesDifferenceEmpty); !ok {
		t.Fatalf("difference after clear = %T %+v, want empty owner difference", diff, diff)
	}
}

func TestStoriesViewsAndReactionsListReturnViewerUsers(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	ownerUser, err := userStore.Create(ctx, domain.User{AccessHash: 91201, Phone: "15550091201", FirstName: "Owner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	viewerOne, err := userStore.Create(ctx, domain.User{AccessHash: 91202, Phone: "15550091202", FirstName: "Viewer", LastName: "One"})
	if err != nil {
		t.Fatalf("create viewer one: %v", err)
	}
	viewerTwo, err := userStore.Create(ctx, domain.User{AccessHash: 91203, Phone: "15550091203", FirstName: "Viewer", LastName: "Two"})
	if err != nil {
		t.Fatalf("create viewer two: %v", err)
	}
	storyStore := memory.NewStoryStore()
	owner := domain.Peer{Type: domain.PeerTypeUser, ID: ownerUser.ID}
	storyStore.SetStoryViewerProfiles(viewerOne, viewerTwo)
	storyStore.SetStoryViewerContacts(ownerUser.ID, domain.Contact{
		User:      viewerOne,
		FirstName: "Saved",
		LastName:  "One",
		Phone:     viewerOne.Phone,
	})
	if _, err := storyStore.UpsertStory(ctx, domain.UpsertStoryRequest{Story: domain.Story{
		Owner:      owner,
		ID:         1,
		Date:       1700000000,
		ExpireDate: 1700003600,
		Public:     true,
	}}); err != nil {
		t.Fatalf("upsert story: %v", err)
	}
	if _, err := storyStore.IncrementViews(ctx, viewerOne.ID, owner, []int{1}, 1700000001); err != nil {
		t.Fatalf("increment view: %v", err)
	}
	fire := &domain.MessageReaction{Type: domain.MessageReactionEmoji, Emoticon: "🔥"}
	if _, err := storyStore.SetReaction(ctx, viewerTwo.ID, owner, 1, fire, 1700000002); err != nil {
		t.Fatalf("set reaction: %v", err)
	}
	r := New(Config{}, Deps{
		Users:   appusers.NewService(userStore),
		Stories: appstories.NewService(storyStore),
	}, zaptest.NewLogger(t), fixedClock{now: time.Unix(1700000100, 0)})
	reqCtx := WithUserID(ctx, ownerUser.ID)

	views, err := r.onStoriesGetStoryViewsList(reqCtx, &tg.StoriesGetStoryViewsListRequest{
		Peer:           &tg.InputPeerSelf{},
		ID:             1,
		Limit:          10,
		ReactionsFirst: true,
	})
	if err != nil {
		t.Fatalf("get story views list: %v", err)
	}
	if views.Count != 2 || views.ViewsCount != 2 || views.ReactionsCount != 1 || len(views.Views) != 2 {
		t.Fatalf("views list = %+v, want two views and one reaction", views)
	}
	firstView, ok := views.Views[0].(*tg.StoryView)
	if !ok || firstView.UserID != viewerTwo.ID {
		t.Fatalf("first view = %T %+v, want reacting viewer first", views.Views[0], views.Views[0])
	}
	if reaction, ok := firstView.Reaction.(*tg.ReactionEmoji); !ok || reaction.Emoticon != "🔥" {
		t.Fatalf("first view reaction = %T %+v, want fire emoji", firstView.Reaction, firstView.Reaction)
	}
	if findUserClass(views.Users, viewerOne.ID) == nil || findUserClass(views.Users, viewerTwo.ID) == nil {
		t.Fatalf("views users = %+v, want both viewer users", views.Users)
	}

	contactsOnlyReq := &tg.StoriesGetStoryViewsListRequest{
		Peer:  &tg.InputPeerSelf{},
		ID:    1,
		Limit: 10,
	}
	contactsOnlyReq.SetJustContacts(true)
	contactsOnly, err := r.onStoriesGetStoryViewsList(reqCtx, contactsOnlyReq)
	if err != nil {
		t.Fatalf("get story views contacts-only: %v", err)
	}
	if contactsOnly.Count != 1 || contactsOnly.ViewsCount != 2 || len(contactsOnly.Views) != 1 {
		t.Fatalf("contacts-only views = %+v, want one filtered contact with total views 2", contactsOnly)
	}
	contactView, ok := contactsOnly.Views[0].(*tg.StoryView)
	if !ok || contactView.UserID != viewerOne.ID {
		t.Fatalf("contacts-only view = %T %+v, want viewer one", contactsOnly.Views[0], contactsOnly.Views[0])
	}
	if findUserClass(contactsOnly.Users, viewerOne.ID) == nil {
		t.Fatalf("contacts-only users = %+v, want viewer one user", contactsOnly.Users)
	}

	searchReq := &tg.StoriesGetStoryViewsListRequest{
		Peer:  &tg.InputPeerSelf{},
		ID:    1,
		Limit: 10,
	}
	searchReq.SetQ("two")
	search, err := r.onStoriesGetStoryViewsList(reqCtx, searchReq)
	if err != nil {
		t.Fatalf("get story views search: %v", err)
	}
	if search.Count != 1 || len(search.Views) != 1 {
		t.Fatalf("search views = %+v, want one filtered viewer", search)
	}
	searchView, ok := search.Views[0].(*tg.StoryView)
	if !ok || searchView.UserID != viewerTwo.ID {
		t.Fatalf("search view = %T %+v, want viewer two", search.Views[0], search.Views[0])
	}
	if _, err := r.onStoriesGetStoryViewsList(reqCtx, &tg.StoriesGetStoryViewsListRequest{
		Peer:   &tg.InputPeerSelf{},
		ID:     1,
		Offset: "bad",
		Limit:  10,
	}); err == nil || !tgerr.Is(err, "OFFSET_INVALID") {
		t.Fatalf("bad story views offset err = %v, want OFFSET_INVALID", err)
	}
	if _, err := r.onStoriesGetStoryViewsList(reqCtx, &tg.StoriesGetStoryViewsListRequest{
		Peer:  &tg.InputPeerSelf{},
		ID:    1,
		Limit: -1,
	}); err == nil || !tgerr.Is(err, "LIMIT_INVALID") {
		t.Fatalf("negative story views limit err = %v, want LIMIT_INVALID", err)
	}
	compatOnly := New(Config{}, Deps{}, zaptest.NewLogger(t), fixedClock{now: time.Unix(1700000100, 0)})
	if _, err := compatOnly.onStoriesGetStoryViewsList(reqCtx, &tg.StoriesGetStoryViewsListRequest{
		Peer:   &tg.InputPeerSelf{},
		ID:     1,
		Offset: strings.Repeat("9", domain.MaxStoryInteractionOffsetLength+1),
		Limit:  10,
	}); err == nil || !tgerr.Is(err, "OFFSET_INVALID") {
		t.Fatalf("oversized story views offset fallback err = %v, want OFFSET_INVALID", err)
	}
	if _, err := compatOnly.onStoriesGetStoryViewsList(reqCtx, &tg.StoriesGetStoryViewsListRequest{
		Peer:  &tg.InputPeerSelf{},
		ID:    1,
		Limit: domain.MaxStoryListLimit + 1,
	}); err == nil || !tgerr.Is(err, "LIMIT_INVALID") {
		t.Fatalf("oversized story views limit fallback err = %v, want LIMIT_INVALID", err)
	}
	if _, err := r.onStoriesGetStoryViewsList(reqCtx, nil); err == nil || !tgerr.Is(err, "INPUT_REQUEST_INVALID") {
		t.Fatalf("nil story views list err = %v, want INPUT_REQUEST_INVALID", err)
	}
	if _, err := r.onStoriesGetStoryViewsList(reqCtx, &tg.StoriesGetStoryViewsListRequest{
		Peer:  &tg.InputPeerEmpty{},
		ID:    0,
		Limit: 10,
	}); err == nil || !tgerr.Is(err, "STORY_ID_INVALID") {
		t.Fatalf("bad story views id before peer err = %v, want STORY_ID_INVALID", err)
	}
	if _, err := r.onStoriesGetStoryViewsList(reqCtx, &tg.StoriesGetStoryViewsListRequest{
		Peer:  &tg.InputPeerEmpty{},
		ID:    1,
		Limit: -1,
	}); err == nil || !tgerr.Is(err, "LIMIT_INVALID") {
		t.Fatalf("bad story views limit before peer err = %v, want LIMIT_INVALID", err)
	}
	if _, err := r.onStoriesGetStoryViewsList(reqCtx, &tg.StoriesGetStoryViewsListRequest{
		Peer:   &tg.InputPeerEmpty{},
		ID:     1,
		Offset: "bad",
		Limit:  10,
	}); err == nil || !tgerr.Is(err, "OFFSET_INVALID") {
		t.Fatalf("bad story views offset before peer err = %v, want OFFSET_INVALID", err)
	}
	oversizedQuery := &tg.StoriesGetStoryViewsListRequest{
		Peer:  &tg.InputPeerEmpty{},
		ID:    1,
		Limit: 10,
	}
	oversizedQuery.SetQ(strings.Repeat("x", domain.MaxStoryViewQueryLength+1))
	if _, err := r.onStoriesGetStoryViewsList(reqCtx, oversizedQuery); err == nil || !tgerr.Is(err, "LIMIT_INVALID") {
		t.Fatalf("oversized story views query before peer err = %v, want LIMIT_INVALID", err)
	}
}

func TestStoriesGetStoriesViewsAlignsWithRequestedIDs(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	ownerUser, err := userStore.Create(ctx, domain.User{AccessHash: 91211, Phone: "15550091211", FirstName: "Owner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	viewerOne, err := userStore.Create(ctx, domain.User{AccessHash: 91212, Phone: "15550091212", FirstName: "Viewer", LastName: "One"})
	if err != nil {
		t.Fatalf("create viewer one: %v", err)
	}
	viewerTwo, err := userStore.Create(ctx, domain.User{AccessHash: 91213, Phone: "15550091213", FirstName: "Viewer", LastName: "Two"})
	if err != nil {
		t.Fatalf("create viewer two: %v", err)
	}
	storyStore := memory.NewStoryStore()
	owner := domain.Peer{Type: domain.PeerTypeUser, ID: ownerUser.ID}
	for _, storyID := range []int{1, 2} {
		if _, err := storyStore.UpsertStory(ctx, domain.UpsertStoryRequest{Story: domain.Story{
			Owner:      owner,
			ID:         storyID,
			Date:       1700001000 + storyID,
			ExpireDate: 1700004600,
			Public:     true,
		}}); err != nil {
			t.Fatalf("upsert story %d: %v", storyID, err)
		}
	}
	if _, err := storyStore.IncrementViews(ctx, viewerOne.ID, owner, []int{1, 2}, 1700001101); err != nil {
		t.Fatalf("increment viewer one: %v", err)
	}
	if _, err := storyStore.IncrementViews(ctx, viewerTwo.ID, owner, []int{2}, 1700001102); err != nil {
		t.Fatalf("increment viewer two: %v", err)
	}
	r := New(Config{}, Deps{
		Users:   appusers.NewService(userStore),
		Stories: appstories.NewService(storyStore),
	}, zaptest.NewLogger(t), fixedClock{now: time.Unix(1700001200, 0)})

	got, err := r.onStoriesGetStoriesViews(WithUserID(ctx, ownerUser.ID), &tg.StoriesGetStoriesViewsRequest{
		Peer: &tg.InputPeerSelf{},
		ID:   []int{1, 1, 99, 2},
	})
	if err != nil {
		t.Fatalf("get stories views: %v", err)
	}
	if len(got.Views) != 4 {
		t.Fatalf("views length = %d, want one entry per requested id including duplicates", len(got.Views))
	}
	if got.Views[0].ViewsCount != 1 || got.Views[1].ViewsCount != 1 || got.Views[2].ViewsCount != 0 || got.Views[3].ViewsCount != 2 {
		t.Fatalf("views counts = [%d %d %d %d], want [1 1 0 2]", got.Views[0].ViewsCount, got.Views[1].ViewsCount, got.Views[2].ViewsCount, got.Views[3].ViewsCount)
	}
	if recent, ok := got.Views[0].GetRecentViewers(); !ok || len(recent) != 1 || recent[0] != viewerOne.ID {
		t.Fatalf("owner recent viewers = %v ok %v, want viewer one", recent, ok)
	}
	if findUserClass(got.Users, viewerOne.ID) == nil || findUserClass(got.Users, viewerTwo.ID) == nil {
		t.Fatalf("views users = %+v, want recent viewer companions", got.Users)
	}
	if len(got.Users) != 2 {
		t.Fatalf("views users length = %d, want deduped recent viewer companions", len(got.Users))
	}

	nonOwner, err := r.onStoriesGetStoriesViews(WithUserID(ctx, viewerTwo.ID), &tg.StoriesGetStoriesViewsRequest{
		Peer: &tg.InputPeerUser{UserID: ownerUser.ID, AccessHash: ownerUser.AccessHash},
		ID:   []int{1, 2},
	})
	if err != nil {
		t.Fatalf("non-owner get stories views: %v", err)
	}
	if len(nonOwner.Views) != 2 || nonOwner.Views[0].ViewsCount != 1 || nonOwner.Views[1].ViewsCount != 2 {
		t.Fatalf("non-owner views = %+v, want counts [1 2]", nonOwner.Views)
	}
	for i, view := range nonOwner.Views {
		if view.HasViewers {
			t.Fatalf("non-owner view[%d].has_viewers = true, want false", i)
		}
		if recent, ok := view.GetRecentViewers(); ok || len(recent) != 0 {
			t.Fatalf("non-owner view[%d] recent viewers = %v ok %v, want absent", i, recent, ok)
		}
	}
	if len(nonOwner.Users) != 0 {
		t.Fatalf("non-owner users = %+v, want no recent-viewer companions", nonOwner.Users)
	}

	if _, err := r.onStoriesGetStoriesViews(WithUserID(ctx, ownerUser.ID), &tg.StoriesGetStoriesViewsRequest{
		Peer: &tg.InputPeerSelf{},
	}); err == nil || !tgerr.Is(err, "STORY_ID_EMPTY") {
		t.Fatalf("empty get stories views err = %v, want STORY_ID_EMPTY", err)
	}
	if _, err := r.onStoriesGetStoriesViews(WithUserID(ctx, ownerUser.ID), &tg.StoriesGetStoriesViewsRequest{
		Peer: &tg.InputPeerEmpty{},
	}); err == nil || !tgerr.Is(err, "STORY_ID_EMPTY") {
		t.Fatalf("empty get stories views before peer err = %v, want STORY_ID_EMPTY", err)
	}

	compatOnly := New(Config{}, Deps{}, zaptest.NewLogger(t), fixedClock{now: time.Unix(1700001200, 0)})
	if _, err := compatOnly.onStoriesGetStoriesViews(WithUserID(ctx, ownerUser.ID), &tg.StoriesGetStoriesViewsRequest{
		Peer: &tg.InputPeerSelf{},
		ID:   []int{0},
	}); err == nil || !tgerr.Is(err, "STORY_ID_INVALID") {
		t.Fatalf("invalid get stories views id err = %v, want STORY_ID_INVALID", err)
	}
	if _, err := compatOnly.onStoriesGetStoriesViews(WithUserID(ctx, ownerUser.ID), &tg.StoriesGetStoriesViewsRequest{
		Peer: &tg.InputPeerEmpty{},
		ID:   []int{0},
	}); err == nil || !tgerr.Is(err, "STORY_ID_INVALID") {
		t.Fatalf("invalid get stories views id before peer err = %v, want STORY_ID_INVALID", err)
	}
	oversizedIDs := make([]int, domain.MaxStoryIDs+1)
	for i := range oversizedIDs {
		oversizedIDs[i] = 1
	}
	if _, err := compatOnly.onStoriesGetStoriesViews(WithUserID(ctx, ownerUser.ID), &tg.StoriesGetStoriesViewsRequest{
		Peer: &tg.InputPeerSelf{},
		ID:   oversizedIDs,
	}); err == nil || !tgerr.Is(err, "STORY_ID_INVALID") {
		t.Fatalf("oversized get stories views id err = %v, want STORY_ID_INVALID", err)
	}
	if _, err := compatOnly.onStoriesGetStoriesViews(WithUserID(ctx, ownerUser.ID), &tg.StoriesGetStoriesViewsRequest{
		Peer: &tg.InputPeerEmpty{},
		ID:   oversizedIDs,
	}); err == nil || !tgerr.Is(err, "STORY_ID_INVALID") {
		t.Fatalf("oversized get stories views id before peer err = %v, want STORY_ID_INVALID", err)
	}
}

func TestStoriesGetStoriesViewsIgnoresWrongOwnerRows(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	requester, err := userStore.Create(ctx, domain.User{AccessHash: 91221, Phone: "15550091221", FirstName: "Requester"})
	if err != nil {
		t.Fatalf("create requester: %v", err)
	}
	ownerUser, err := userStore.Create(ctx, domain.User{AccessHash: 91222, Phone: "15550091222", FirstName: "Owner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	wrongRecentViewer, err := userStore.Create(ctx, domain.User{AccessHash: 91223, Phone: "15550091223", FirstName: "Wrong", LastName: "Viewer"})
	if err != nil {
		t.Fatalf("create wrong recent viewer: %v", err)
	}
	requestedPeer := domain.Peer{Type: domain.PeerTypeUser, ID: ownerUser.ID}
	r := New(Config{}, Deps{
		Users: appusers.NewService(userStore),
		Stories: storiesByIDStoryService{list: domain.StoryList{Stories: []domain.Story{
			{
				Owner: requestedPeer,
				ID:    1,
				Views: domain.StoryViews{ViewsCount: 3},
			},
			{
				Owner: domain.Peer{Type: domain.PeerTypeUser, ID: requester.ID},
				ID:    2,
				Views: domain.StoryViews{
					ViewsCount:    99,
					HasViewers:    true,
					RecentViewers: []int64{wrongRecentViewer.ID},
				},
			},
		}}},
	}, zaptest.NewLogger(t), fixedClock{now: time.Unix(1700001200, 0)})

	got, err := r.onStoriesGetStoriesViews(WithUserID(ctx, requester.ID), &tg.StoriesGetStoriesViewsRequest{
		Peer: &tg.InputPeerUser{UserID: ownerUser.ID, AccessHash: ownerUser.AccessHash},
		ID:   []int{1, 2, 2},
	})
	if err != nil {
		t.Fatalf("get stories views with wrong owner rows: %v", err)
	}
	if len(got.Views) != 3 {
		t.Fatalf("views length = %d, want three request-aligned slots", len(got.Views))
	}
	if got.Views[0].ViewsCount != 3 || got.Views[1].ViewsCount != 0 || got.Views[2].ViewsCount != 0 {
		t.Fatalf("views counts = [%d %d %d], want [3 0 0]", got.Views[0].ViewsCount, got.Views[1].ViewsCount, got.Views[2].ViewsCount)
	}
	for i, view := range got.Views {
		if view.HasViewers {
			t.Fatalf("view[%d].has_viewers = true, want false", i)
		}
		if recent, ok := view.GetRecentViewers(); ok || len(recent) != 0 {
			t.Fatalf("view[%d] recent viewers = %v ok %v, want absent", i, recent, ok)
		}
	}
	if len(got.Users) != 0 {
		t.Fatalf("users = %+v, want no companions from wrong-owner recent viewers", got.Users)
	}
}

func TestStoriesGetStoryReactionsListReturnsChannelAdminReactions(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	creator, err := userStore.Create(ctx, domain.User{AccessHash: 91301, Phone: "15550091301", FirstName: "Creator"})
	if err != nil {
		t.Fatalf("create creator: %v", err)
	}
	admin, err := userStore.Create(ctx, domain.User{AccessHash: 91302, Phone: "15550091302", FirstName: "Admin"})
	if err != nil {
		t.Fatalf("create admin: %v", err)
	}
	member, err := userStore.Create(ctx, domain.User{AccessHash: 91303, Phone: "15550091303", FirstName: "Member"})
	if err != nil {
		t.Fatalf("create member: %v", err)
	}
	reactor, err := userStore.Create(ctx, domain.User{AccessHash: 91304, Phone: "15550091304", FirstName: "Reactor"})
	if err != nil {
		t.Fatalf("create reactor: %v", err)
	}
	channelStore := memory.NewChannelStore()
	channelService := appchannels.NewService(channelStore)
	created, err := channelService.CreateChannel(ctx, creator.ID, domain.CreateChannelRequest{
		CreatorUserID: creator.ID,
		Title:         "story reaction channel",
		Broadcast:     true,
		MemberUserIDs: []int64{admin.ID, member.ID, reactor.ID},
		Date:          1700000000,
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	if _, err := channelService.EditAdmin(ctx, creator.ID, domain.EditChannelAdminRequest{
		ChannelID: created.Channel.ID,
		MemberID:  admin.ID,
		AdminRights: domain.ChannelAdminRights{
			ChangeInfo: true,
		},
		Date: 1700000001,
	}); err != nil {
		t.Fatalf("promote admin: %v", err)
	}
	storyStore := memory.NewStoryStore()
	owner := domain.Peer{Type: domain.PeerTypeChannel, ID: created.Channel.ID}
	if _, err := storyStore.UpsertStory(ctx, domain.UpsertStoryRequest{Story: domain.Story{
		Owner:      owner,
		ID:         1,
		Date:       1700000002,
		ExpireDate: 1700003600,
		Public:     true,
	}}); err != nil {
		t.Fatalf("upsert channel story: %v", err)
	}
	fire := &domain.MessageReaction{Type: domain.MessageReactionEmoji, Emoticon: "🔥"}
	if _, err := storyStore.SetReaction(ctx, reactor.ID, owner, 1, fire, 1700000003); err != nil {
		t.Fatalf("set reaction: %v", err)
	}
	custom := &domain.MessageReaction{Type: domain.MessageReactionCustomEmoji, DocumentID: 12345}
	if _, err := storyStore.SetReaction(ctx, member.ID, owner, 1, custom, 1700000004); err != nil {
		t.Fatalf("set custom reaction: %v", err)
	}
	r := New(Config{}, Deps{
		Users:    appusers.NewService(userStore),
		Channels: channelService,
		Stories:  appstories.NewService(storyStore),
	}, zaptest.NewLogger(t), fixedClock{now: time.Unix(1700000100, 0)})
	req := &tg.StoriesGetStoryReactionsListRequest{
		Peer:  &tg.InputPeerChannel{ChannelID: created.Channel.ID, AccessHash: created.Channel.AccessHash},
		ID:    1,
		Limit: 10,
	}
	req.SetReaction(&tg.ReactionEmoji{Emoticon: "🔥"})

	creatorList, err := r.onStoriesGetStoryReactionsList(WithUserID(ctx, creator.ID), req)
	if err != nil {
		t.Fatalf("creator get story reactions list: %v", err)
	}
	assertStoryReactionListViewer(t, creatorList, reactor.ID)

	adminList, err := r.onStoriesGetStoryReactionsList(WithUserID(ctx, admin.ID), req)
	if err != nil {
		t.Fatalf("admin get story reactions list: %v", err)
	}
	assertStoryReactionListViewer(t, adminList, reactor.ID)

	customReq := *req
	customReq.SetReaction(&tg.ReactionCustomEmoji{DocumentID: 12345})
	customList, err := r.onStoriesGetStoryReactionsList(WithUserID(ctx, creator.ID), &customReq)
	if err != nil {
		t.Fatalf("creator get custom story reactions list: %v", err)
	}
	assertStoryReactionListCustomViewer(t, customList, member.ID, clientDocumentIDFromServerID(12345))

	invalidOffsetReq := *req
	invalidOffsetReq.SetOffset("1:1700000003:" + strconv.FormatInt(reactor.ID, 10))
	if _, err := r.onStoriesGetStoryReactionsList(WithUserID(ctx, creator.ID), &invalidOffsetReq); err == nil || !tgerr.Is(err, "OFFSET_INVALID") {
		t.Fatalf("bad story reactions offset err = %v, want OFFSET_INVALID", err)
	}
	compatOnly := New(Config{}, Deps{
		Users:    appusers.NewService(userStore),
		Channels: channelService,
	}, zaptest.NewLogger(t), fixedClock{now: time.Unix(1700000100, 0)})
	if _, err := compatOnly.onStoriesGetStoryReactionsList(WithUserID(ctx, creator.ID), &invalidOffsetReq); err == nil || !tgerr.Is(err, "OFFSET_INVALID") {
		t.Fatalf("bad story reactions offset fallback err = %v, want OFFSET_INVALID", err)
	}
	invalidLimitReq := *req
	invalidLimitReq.Limit = -1
	if _, err := r.onStoriesGetStoryReactionsList(WithUserID(ctx, creator.ID), &invalidLimitReq); err == nil || !tgerr.Is(err, "LIMIT_INVALID") {
		t.Fatalf("negative story reactions limit err = %v, want LIMIT_INVALID", err)
	}
	invalidLimitReq.Limit = domain.MaxStoryListLimit + 1
	if _, err := compatOnly.onStoriesGetStoryReactionsList(WithUserID(ctx, creator.ID), &invalidLimitReq); err == nil || !tgerr.Is(err, "LIMIT_INVALID") {
		t.Fatalf("oversized story reactions limit fallback err = %v, want LIMIT_INVALID", err)
	}
	if _, err := r.onStoriesGetStoryReactionsList(WithUserID(ctx, creator.ID), nil); err == nil || !tgerr.Is(err, "INPUT_REQUEST_INVALID") {
		t.Fatalf("nil story reactions list err = %v, want INPUT_REQUEST_INVALID", err)
	}
	for _, tt := range []struct {
		name     string
		reaction tg.ReactionClass
	}{
		{name: "typed nil emoji", reaction: (*tg.ReactionEmoji)(nil)},
		{name: "typed nil custom emoji", reaction: (*tg.ReactionCustomEmoji)(nil)},
		{name: "typed nil empty", reaction: (*tg.ReactionEmpty)(nil)},
		{name: "typed nil paid", reaction: (*tg.ReactionPaid)(nil)},
	} {
		t.Run("bad filter "+tt.name, func(t *testing.T) {
			badFilter := &tg.StoriesGetStoryReactionsListRequest{
				Peer:  &tg.InputPeerEmpty{},
				ID:    1,
				Limit: 10,
			}
			badFilter.SetReaction(tt.reaction)
			if _, err := r.onStoriesGetStoryReactionsList(WithUserID(ctx, creator.ID), badFilter); err == nil || !tgerr.Is(err, "REACTION_INVALID") {
				t.Fatalf("bad story reactions filter err = %v, want REACTION_INVALID", err)
			}
		})
	}
	if _, err := r.onStoriesGetStoryReactionsList(WithUserID(ctx, creator.ID), &tg.StoriesGetStoryReactionsListRequest{
		Peer:  &tg.InputPeerEmpty{},
		ID:    0,
		Limit: 10,
	}); err == nil || !tgerr.Is(err, "STORY_ID_INVALID") {
		t.Fatalf("bad story reactions id before peer err = %v, want STORY_ID_INVALID", err)
	}
	if _, err := r.onStoriesGetStoryReactionsList(WithUserID(ctx, creator.ID), &tg.StoriesGetStoryReactionsListRequest{
		Peer:  &tg.InputPeerEmpty{},
		ID:    1,
		Limit: -1,
	}); err == nil || !tgerr.Is(err, "LIMIT_INVALID") {
		t.Fatalf("bad story reactions limit before peer err = %v, want LIMIT_INVALID", err)
	}
	badOffsetBeforePeer := &tg.StoriesGetStoryReactionsListRequest{
		Peer:  &tg.InputPeerEmpty{},
		ID:    1,
		Limit: 10,
	}
	badOffsetBeforePeer.SetOffset("1:1700000003:" + strconv.FormatInt(reactor.ID, 10))
	if _, err := r.onStoriesGetStoryReactionsList(WithUserID(ctx, creator.ID), badOffsetBeforePeer); err == nil || !tgerr.Is(err, "OFFSET_INVALID") {
		t.Fatalf("bad story reactions offset before peer err = %v, want OFFSET_INVALID", err)
	}

	if _, err := r.onStoriesGetStoryReactionsList(WithUserID(ctx, member.ID), req); err == nil || !strings.Contains(err.Error(), "CHAT_ADMIN_REQUIRED") {
		t.Fatalf("member get story reactions list err = %v, want CHAT_ADMIN_REQUIRED", err)
	}
	if _, err := r.onStoriesGetStoryReactionsList(WithUserID(ctx, creator.ID), &tg.StoriesGetStoryReactionsListRequest{
		Peer:  &tg.InputPeerSelf{},
		ID:    1,
		Limit: 10,
	}); err == nil || !strings.Contains(err.Error(), "PEER_ID_INVALID") {
		t.Fatalf("user peer story reactions list err = %v, want PEER_ID_INVALID", err)
	}
}

func TestStoriesGetStoryReactionsListReturnsPublicRepost(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	creator, err := userStore.Create(ctx, domain.User{AccessHash: 91401, Phone: "15550091401", FirstName: "Creator"})
	if err != nil {
		t.Fatalf("create creator: %v", err)
	}
	reposter, err := userStore.Create(ctx, domain.User{AccessHash: 91402, Phone: "15550091402", FirstName: "Reposter"})
	if err != nil {
		t.Fatalf("create reposter: %v", err)
	}
	channelStore := memory.NewChannelStore()
	channelService := appchannels.NewService(channelStore)
	created, err := channelService.CreateChannel(ctx, creator.ID, domain.CreateChannelRequest{
		CreatorUserID: creator.ID,
		Title:         "story repost channel",
		Broadcast:     true,
		MemberUserIDs: []int64{reposter.ID},
		Date:          1700000200,
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	storyStore := memory.NewStoryStore()
	sourceOwner := domain.Peer{Type: domain.PeerTypeChannel, ID: created.Channel.ID}
	if _, err := storyStore.UpsertStory(ctx, domain.UpsertStoryRequest{Story: domain.Story{
		Owner:      sourceOwner,
		ID:         1,
		Date:       1700000201,
		ExpireDate: 1700003800,
		Public:     true,
	}}); err != nil {
		t.Fatalf("upsert channel story: %v", err)
	}
	repostOwner := domain.Peer{Type: domain.PeerTypeUser, ID: reposter.ID}
	repost, err := storyStore.CreateStory(ctx, domain.StoryCreateRequest{
		Owner:    repostOwner,
		RandomID: 9140201,
		Date:     1700000202,
		Period:   86400,
		Public:   true,
		Forward: &domain.StoryForward{
			From:    sourceOwner,
			StoryID: 1,
		},
	})
	if err != nil {
		t.Fatalf("create repost story: %v", err)
	}
	r := New(Config{}, Deps{
		Users:    appusers.NewService(userStore),
		Channels: channelService,
		Stories:  appstories.NewService(storyStore),
	}, zaptest.NewLogger(t), fixedClock{now: time.Unix(1700000300, 0)})
	req := &tg.StoriesGetStoryReactionsListRequest{
		ForwardsFirst: true,
		Peer:          &tg.InputPeerChannel{ChannelID: created.Channel.ID, AccessHash: created.Channel.AccessHash},
		ID:            1,
		Limit:         20,
	}
	list, err := r.onStoriesGetStoryReactionsList(WithUserID(ctx, creator.ID), req)
	if err != nil {
		t.Fatalf("creator get story repost reactions list: %v", err)
	}
	if list.Count != 1 || len(list.Reactions) != 1 {
		t.Fatalf("repost reactions list = %+v, want one public repost", list)
	}
	repostReaction, ok := list.Reactions[0].(*tg.StoryReactionPublicRepost)
	if !ok {
		t.Fatalf("story reaction = %T, want *tg.StoryReactionPublicRepost", list.Reactions[0])
	}
	peer, ok := repostReaction.PeerID.(*tg.PeerUser)
	if !ok || peer.UserID != reposter.ID {
		t.Fatalf("repost reaction peer = %T %+v, want user %d", repostReaction.PeerID, repostReaction.PeerID, reposter.ID)
	}
	storyItem, ok := repostReaction.Story.(*tg.StoryItem)
	if !ok || storyItem.ID != repost.Story.ID {
		t.Fatalf("repost reaction story = %T %+v, want story id %d", repostReaction.Story, repostReaction.Story, repost.Story.ID)
	}
	if findUserClass(list.Users, reposter.ID) == nil {
		t.Fatalf("repost reaction users = %+v, want reposter %d", list.Users, reposter.ID)
	}
	if findChannelClass(list.Chats, created.Channel.ID) == nil {
		t.Fatalf("repost reaction chats = %+v, want source channel %d", list.Chats, created.Channel.ID)
	}

	filteredReq := *req
	filteredReq.SetReaction(&tg.ReactionEmoji{Emoticon: "🔥"})
	filtered, err := r.onStoriesGetStoryReactionsList(WithUserID(ctx, creator.ID), &filteredReq)
	if err != nil {
		t.Fatalf("creator get filtered story reactions list: %v", err)
	}
	if filtered.Count != 0 || len(filtered.Reactions) != 0 {
		t.Fatalf("filtered reactions list = %+v, want no repost rows", filtered)
	}
	if _, err := storyStore.DeleteStories(ctx, repostOwner, []int{repost.Story.ID}, 1700000203); err != nil {
		t.Fatalf("delete repost: %v", err)
	}
	empty, err := r.onStoriesGetStoryReactionsList(WithUserID(ctx, creator.ID), req)
	if err != nil {
		t.Fatalf("creator get story reactions list after delete: %v", err)
	}
	if empty.Count != 0 || len(empty.Reactions) != 0 {
		t.Fatalf("repost reactions list after delete = %+v, want empty", empty)
	}
}

func TestStoriesGetStoryViewsListReturnsPublicMessageForward(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	author, err := userStore.Create(ctx, domain.User{AccessHash: 91411, Phone: "15550091411", FirstName: "Author"})
	if err != nil {
		t.Fatalf("create author: %v", err)
	}
	poster, err := userStore.Create(ctx, domain.User{AccessHash: 91412, Phone: "15550091412", FirstName: "Poster"})
	if err != nil {
		t.Fatalf("create poster: %v", err)
	}
	storyStore := memory.NewStoryStore()
	authorPeer := domain.Peer{Type: domain.PeerTypeUser, ID: author.ID}
	if _, err := storyStore.UpsertStory(ctx, domain.UpsertStoryRequest{Story: domain.Story{
		Owner:      authorPeer,
		ID:         11,
		Date:       1700000211,
		ExpireDate: 1700003811,
		Public:     true,
		Media:      &domain.MessageMedia{Kind: domain.MessageMediaKindPhoto, Photo: &domain.Photo{ID: 9141101, AccessHash: 11, DCID: 2}},
	}}); err != nil {
		t.Fatalf("upsert source story: %v", err)
	}
	channelStore := memory.NewChannelStore()
	channelService := appchannels.NewService(channelStore)
	created, err := channelService.CreateChannel(ctx, poster.ID, domain.CreateChannelRequest{
		CreatorUserID: poster.ID,
		Title:         "story forward public",
		Broadcast:     true,
		Date:          1700000212,
	})
	if err != nil {
		t.Fatalf("create public channel: %v", err)
	}
	public, err := channelService.UpdateUsername(ctx, poster.ID, domain.UpdateChannelUsernameRequest{
		ChannelID: created.Channel.ID,
		Username:  "story_forward_public",
	})
	if err != nil {
		t.Fatalf("make channel public: %v", err)
	}
	r := New(Config{}, Deps{
		Users:    appusers.NewService(userStore),
		Channels: channelService,
		Stories:  appstories.NewService(storyStore),
	}, zaptest.NewLogger(t), fixedClock{now: time.Unix(1700000300, 0)})
	updates, err := r.onMessagesSendMedia(WithUserID(ctx, poster.ID), &tg.MessagesSendMediaRequest{
		Peer:     &tg.InputPeerChannel{ChannelID: public.ID, AccessHash: public.AccessHash},
		Media:    &tg.InputMediaStory{Peer: &tg.InputPeerUser{UserID: author.ID, AccessHash: author.AccessHash}, ID: 11},
		RandomID: 9141201,
	})
	if err != nil {
		t.Fatalf("send public story message: %v", err)
	}
	msg := newMessageFromUpdates(t, updates)

	views, err := r.onStoriesGetStoriesViews(WithUserID(ctx, author.ID), &tg.StoriesGetStoriesViewsRequest{
		Peer: &tg.InputPeerSelf{},
		ID:   []int{11},
	})
	if err != nil {
		t.Fatalf("get story views: %v", err)
	}
	if len(views.Views) != 1 || views.Views[0].ForwardsCount != 1 {
		t.Fatalf("story views = %+v, want forwards_count=1", views.Views)
	}
	list, err := r.onStoriesGetStoryViewsList(WithUserID(ctx, author.ID), &tg.StoriesGetStoryViewsListRequest{
		Peer:   &tg.InputPeerSelf{},
		ID:     11,
		Limit:  20,
		Offset: "",
	})
	if err != nil {
		t.Fatalf("get story views list: %v", err)
	}
	if list.Count != 1 || list.ForwardsCount != 1 || len(list.Views) != 1 {
		t.Fatalf("story views list = %+v, want one public message forward", list)
	}
	forward, ok := list.Views[0].(*tg.StoryViewPublicForward)
	if !ok {
		t.Fatalf("story view = %T, want *tg.StoryViewPublicForward", list.Views[0])
	}
	forwardMsg, ok := forward.Message.(*tg.Message)
	if !ok || forwardMsg.ID != msg.ID {
		t.Fatalf("forward message = %T %+v, want message id %d", forward.Message, forward.Message, msg.ID)
	}
	if peer, ok := forwardMsg.PeerID.(*tg.PeerChannel); !ok || peer.ChannelID != public.ID {
		t.Fatalf("forward message peer = %T %+v, want channel %d", forwardMsg.PeerID, forwardMsg.PeerID, public.ID)
	}
	assertMessageMediaStory(t, forwardMsg.Media, author.ID, 11, true)
	if findChannelClass(list.Chats, public.ID) == nil {
		t.Fatalf("story views list chats = %+v, want public forward channel %d", list.Chats, public.ID)
	}

	if _, err := r.onChannelsDeleteMessages(WithUserID(ctx, poster.ID), &tg.ChannelsDeleteMessagesRequest{
		Channel: &tg.InputChannel{ChannelID: public.ID, AccessHash: public.AccessHash},
		ID:      []int{msg.ID},
	}); err != nil {
		t.Fatalf("delete public story message: %v", err)
	}
	emptyList, err := r.onStoriesGetStoryViewsList(WithUserID(ctx, author.ID), &tg.StoriesGetStoryViewsListRequest{
		Peer:  &tg.InputPeerSelf{},
		ID:    11,
		Limit: 20,
	})
	if err != nil {
		t.Fatalf("get story views list after delete: %v", err)
	}
	if emptyList.Count != 0 || emptyList.ForwardsCount != 0 || len(emptyList.Views) != 0 {
		t.Fatalf("story views list after delete = %+v, want empty", emptyList)
	}
}

func TestStoriesGetStoryReactionsListReturnsPublicMessageForward(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	creator, err := userStore.Create(ctx, domain.User{AccessHash: 91421, Phone: "15550091421", FirstName: "Creator"})
	if err != nil {
		t.Fatalf("create creator: %v", err)
	}
	channelStore := memory.NewChannelStore()
	channelService := appchannels.NewService(channelStore)
	source, err := channelService.CreateChannel(ctx, creator.ID, domain.CreateChannelRequest{
		CreatorUserID: creator.ID,
		Title:         "source story channel",
		Broadcast:     true,
		Date:          1700000220,
	})
	if err != nil {
		t.Fatalf("create source channel: %v", err)
	}
	target, err := channelService.CreateChannel(ctx, creator.ID, domain.CreateChannelRequest{
		CreatorUserID: creator.ID,
		Title:         "target public channel",
		Broadcast:     true,
		Date:          1700000221,
	})
	if err != nil {
		t.Fatalf("create target channel: %v", err)
	}
	targetPublic, err := channelService.UpdateUsername(ctx, creator.ID, domain.UpdateChannelUsernameRequest{
		ChannelID: target.Channel.ID,
		Username:  "story_forward_target",
	})
	if err != nil {
		t.Fatalf("make target public: %v", err)
	}
	storyStore := memory.NewStoryStore()
	sourcePeer := domain.Peer{Type: domain.PeerTypeChannel, ID: source.Channel.ID}
	if _, err := storyStore.UpsertStory(ctx, domain.UpsertStoryRequest{Story: domain.Story{
		Owner:      sourcePeer,
		ID:         21,
		Date:       1700000222,
		ExpireDate: 1700003822,
		Public:     true,
		Media:      &domain.MessageMedia{Kind: domain.MessageMediaKindPhoto, Photo: &domain.Photo{ID: 9142101, AccessHash: 21, DCID: 2}},
	}}); err != nil {
		t.Fatalf("upsert channel story: %v", err)
	}
	r := New(Config{}, Deps{
		Users:    appusers.NewService(userStore),
		Channels: channelService,
		Stories:  appstories.NewService(storyStore),
	}, zaptest.NewLogger(t), fixedClock{now: time.Unix(1700000300, 0)})
	updates, err := r.onMessagesSendMedia(WithUserID(ctx, creator.ID), &tg.MessagesSendMediaRequest{
		Peer: &tg.InputPeerChannel{ChannelID: targetPublic.ID, AccessHash: targetPublic.AccessHash},
		Media: &tg.InputMediaStory{
			Peer: &tg.InputPeerChannel{ChannelID: source.Channel.ID, AccessHash: source.Channel.AccessHash},
			ID:   21,
		},
		RandomID: 9142102,
	})
	if err != nil {
		t.Fatalf("send channel story message: %v", err)
	}
	msg := newMessageFromUpdates(t, updates)
	list, err := r.onStoriesGetStoryReactionsList(WithUserID(ctx, creator.ID), &tg.StoriesGetStoryReactionsListRequest{
		ForwardsFirst: true,
		Peer:          &tg.InputPeerChannel{ChannelID: source.Channel.ID, AccessHash: source.Channel.AccessHash},
		ID:            21,
		Limit:         20,
	})
	if err != nil {
		t.Fatalf("get story reactions list: %v", err)
	}
	if list.Count != 1 || len(list.Reactions) != 1 {
		t.Fatalf("story reactions list = %+v, want one public message forward", list)
	}
	forward, ok := list.Reactions[0].(*tg.StoryReactionPublicForward)
	if !ok {
		t.Fatalf("story reaction = %T, want *tg.StoryReactionPublicForward", list.Reactions[0])
	}
	forwardMsg, ok := forward.Message.(*tg.Message)
	if !ok || forwardMsg.ID != msg.ID {
		t.Fatalf("forward reaction message = %T %+v, want message id %d", forward.Message, forward.Message, msg.ID)
	}
	if peer, ok := forwardMsg.PeerID.(*tg.PeerChannel); !ok || peer.ChannelID != targetPublic.ID {
		t.Fatalf("forward reaction message peer = %T %+v, want channel %d", forwardMsg.PeerID, forwardMsg.PeerID, targetPublic.ID)
	}
	storyMedia, ok := forwardMsg.Media.(*tg.MessageMediaStory)
	if !ok {
		t.Fatalf("forward reaction media = %T, want *tg.MessageMediaStory", forwardMsg.Media)
	}
	if peer, ok := storyMedia.Peer.(*tg.PeerChannel); !ok || peer.ChannelID != source.Channel.ID || storyMedia.ID != 21 {
		t.Fatalf("forward reaction story media = %+v, want source channel story", storyMedia)
	}
	if story, ok := storyMedia.GetStory(); !ok || story == nil {
		t.Fatalf("forward reaction story media embedded = %T, want embedded story", story)
	}
	if findChannelClass(list.Chats, source.Channel.ID) == nil || findChannelClass(list.Chats, targetPublic.ID) == nil {
		t.Fatalf("story reactions list chats = %+v, want source and target channels", list.Chats)
	}

	filteredReq := &tg.StoriesGetStoryReactionsListRequest{
		ForwardsFirst: true,
		Peer:          &tg.InputPeerChannel{ChannelID: source.Channel.ID, AccessHash: source.Channel.AccessHash},
		ID:            21,
		Limit:         20,
	}
	filteredReq.SetReaction(&tg.ReactionEmoji{Emoticon: "🔥"})
	filtered, err := r.onStoriesGetStoryReactionsList(WithUserID(ctx, creator.ID), filteredReq)
	if err != nil {
		t.Fatalf("get filtered story reactions list: %v", err)
	}
	if filtered.Count != 0 || len(filtered.Reactions) != 0 {
		t.Fatalf("filtered story reactions list = %+v, want no public forwards", filtered)
	}

	if _, err := r.onChannelsDeleteMessages(WithUserID(ctx, creator.ID), &tg.ChannelsDeleteMessagesRequest{
		Channel: &tg.InputChannel{ChannelID: targetPublic.ID, AccessHash: targetPublic.AccessHash},
		ID:      []int{msg.ID},
	}); err != nil {
		t.Fatalf("delete public story message: %v", err)
	}
	empty, err := r.onStoriesGetStoryReactionsList(WithUserID(ctx, creator.ID), &tg.StoriesGetStoryReactionsListRequest{
		ForwardsFirst: true,
		Peer:          &tg.InputPeerChannel{ChannelID: source.Channel.ID, AccessHash: source.Channel.AccessHash},
		ID:            21,
		Limit:         20,
	})
	if err != nil {
		t.Fatalf("get story reactions after delete: %v", err)
	}
	if empty.Count != 0 || len(empty.Reactions) != 0 {
		t.Fatalf("story reactions after delete = %+v, want empty", empty)
	}
}

func TestStatsGetStoryPublicForwardsReturnsStoryAndMessageForwards(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	creator, err := userStore.Create(ctx, domain.User{AccessHash: 91431, Phone: "15550091431", FirstName: "Creator"})
	if err != nil {
		t.Fatalf("create creator: %v", err)
	}
	channelStore := memory.NewChannelStore()
	channelService := appchannels.NewService(channelStore)
	source, err := channelService.CreateChannel(ctx, creator.ID, domain.CreateChannelRequest{
		CreatorUserID: creator.ID,
		Title:         "stats source story channel",
		Broadcast:     true,
		Date:          1700000300,
	})
	if err != nil {
		t.Fatalf("create source channel: %v", err)
	}
	target, err := channelService.CreateChannel(ctx, creator.ID, domain.CreateChannelRequest{
		CreatorUserID: creator.ID,
		Title:         "stats target public channel",
		Broadcast:     true,
		Date:          1700000301,
	})
	if err != nil {
		t.Fatalf("create target channel: %v", err)
	}
	targetPublic, err := channelService.UpdateUsername(ctx, creator.ID, domain.UpdateChannelUsernameRequest{
		ChannelID: target.Channel.ID,
		Username:  "stats_story_forward_target",
	})
	if err != nil {
		t.Fatalf("make target public: %v", err)
	}
	repostOwner, err := channelService.CreateChannel(ctx, creator.ID, domain.CreateChannelRequest{
		CreatorUserID: creator.ID,
		Title:         "stats repost public channel",
		Broadcast:     true,
		Date:          1700000302,
	})
	if err != nil {
		t.Fatalf("create repost channel: %v", err)
	}
	repostPublic, err := channelService.UpdateUsername(ctx, creator.ID, domain.UpdateChannelUsernameRequest{
		ChannelID: repostOwner.Channel.ID,
		Username:  "stats_story_repost_public",
	})
	if err != nil {
		t.Fatalf("make repost channel public: %v", err)
	}

	storyStore := memory.NewStoryStore()
	sourcePeer := domain.Peer{Type: domain.PeerTypeChannel, ID: source.Channel.ID}
	if _, err := storyStore.UpsertStory(ctx, domain.UpsertStoryRequest{Story: domain.Story{
		Owner:      sourcePeer,
		ID:         31,
		Date:       1700000303,
		ExpireDate: 1700003903,
		Public:     true,
		Media:      &domain.MessageMedia{Kind: domain.MessageMediaKindPhoto, Photo: &domain.Photo{ID: 9143101, AccessHash: 31, DCID: 2}},
	}}); err != nil {
		t.Fatalf("upsert source story: %v", err)
	}
	repostPeer := domain.Peer{Type: domain.PeerTypeChannel, ID: repostPublic.ID}
	repost, err := storyStore.UpsertStory(ctx, domain.UpsertStoryRequest{Story: domain.Story{
		Owner:      repostPeer,
		ID:         32,
		Date:       1700000310,
		ExpireDate: 1700003910,
		Public:     true,
		Media:      &domain.MessageMedia{Kind: domain.MessageMediaKindPhoto, Photo: &domain.Photo{ID: 9143102, AccessHash: 32, DCID: 2}},
		Forward: &domain.StoryForward{
			From:    sourcePeer,
			StoryID: 31,
		},
	}})
	if err != nil {
		t.Fatalf("upsert public repost story: %v", err)
	}

	r := New(Config{}, Deps{
		Users:    appusers.NewService(userStore),
		Channels: channelService,
		Stories:  appstories.NewService(storyStore, appstories.WithChannelStoryAccess(channelService)),
	}, zaptest.NewLogger(t), fixedClock{now: time.Unix(1700000400, 0)})
	updates, err := r.onMessagesSendMedia(WithUserID(ctx, creator.ID), &tg.MessagesSendMediaRequest{
		Peer: &tg.InputPeerChannel{ChannelID: targetPublic.ID, AccessHash: targetPublic.AccessHash},
		Media: &tg.InputMediaStory{
			Peer: &tg.InputPeerChannel{ChannelID: source.Channel.ID, AccessHash: source.Channel.AccessHash},
			ID:   31,
		},
		RandomID: 9143103,
	})
	if err != nil {
		t.Fatalf("send public story message: %v", err)
	}
	msg := newMessageFromUpdates(t, updates)

	req := &tg.StatsGetStoryPublicForwardsRequest{
		Peer:  &tg.InputPeerChannel{ChannelID: source.Channel.ID, AccessHash: source.Channel.AccessHash},
		ID:    31,
		Limit: 1,
	}
	first, err := r.onStatsGetStoryPublicForwards(WithUserID(ctx, creator.ID), req)
	if err != nil {
		t.Fatalf("get story public forwards first page: %v", err)
	}
	if first.Count != 2 || len(first.Forwards) != 1 {
		t.Fatalf("first public forwards page = %+v, want count 2 and one row", first)
	}
	next, ok := first.GetNextOffset()
	if !ok || next == "" {
		t.Fatalf("first public forwards next_offset = %q %v, want set", next, ok)
	}
	forwardMsg, ok := first.Forwards[0].(*tg.PublicForwardMessage)
	if !ok {
		t.Fatalf("first forward = %T, want *tg.PublicForwardMessage", first.Forwards[0])
	}
	msgForward, ok := forwardMsg.Message.(*tg.Message)
	if !ok || msgForward.ID != msg.ID {
		t.Fatalf("first forward message = %T %+v, want message id %d", forwardMsg.Message, forwardMsg.Message, msg.ID)
	}
	if peer, ok := msgForward.PeerID.(*tg.PeerChannel); !ok || peer.ChannelID != targetPublic.ID {
		t.Fatalf("first forward peer = %T %+v, want target channel %d", msgForward.PeerID, msgForward.PeerID, targetPublic.ID)
	}
	storyMedia, ok := msgForward.Media.(*tg.MessageMediaStory)
	if !ok {
		t.Fatalf("first forward media = %T, want *tg.MessageMediaStory", msgForward.Media)
	}
	if peer, ok := storyMedia.Peer.(*tg.PeerChannel); !ok || peer.ChannelID != source.Channel.ID || storyMedia.ID != 31 {
		t.Fatalf("first forward story media = %+v, want source channel story", storyMedia)
	}
	if story, ok := storyMedia.GetStory(); !ok || story == nil {
		t.Fatalf("first forward story media embedded = %T, want embedded story", story)
	}
	if findChannelClass(first.Chats, source.Channel.ID) == nil || findChannelClass(first.Chats, targetPublic.ID) == nil {
		t.Fatalf("first public forwards chats = %+v, want source and target channels", first.Chats)
	}
	assertChannelStoryMaxID(t, findChannelClass(first.Chats, source.Channel.ID), 31)

	secondReq := *req
	secondReq.Offset = next
	second, err := r.onStatsGetStoryPublicForwards(WithUserID(ctx, creator.ID), &secondReq)
	if err != nil {
		t.Fatalf("get story public forwards second page: %v", err)
	}
	if second.Count != 2 || len(second.Forwards) != 1 {
		t.Fatalf("second public forwards page = %+v, want count 2 and one row", second)
	}
	if next, ok := second.GetNextOffset(); ok || next != "" {
		t.Fatalf("second public forwards next_offset = %q %v, want empty", next, ok)
	}
	forwardStory, ok := second.Forwards[0].(*tg.PublicForwardStory)
	if !ok {
		t.Fatalf("second forward = %T, want *tg.PublicForwardStory", second.Forwards[0])
	}
	if peer, ok := forwardStory.Peer.(*tg.PeerChannel); !ok || peer.ChannelID != repostPublic.ID {
		t.Fatalf("second forward peer = %T %+v, want repost channel %d", forwardStory.Peer, forwardStory.Peer, repostPublic.ID)
	}
	storyItem, ok := forwardStory.Story.(*tg.StoryItem)
	if !ok || storyItem.ID != repost.ID {
		t.Fatalf("second forward story = %T %+v, want story id %d", forwardStory.Story, forwardStory.Story, repost.ID)
	}
	if findChannelClass(second.Chats, source.Channel.ID) == nil || findChannelClass(second.Chats, repostPublic.ID) == nil {
		t.Fatalf("second public forwards chats = %+v, want source and repost channels", second.Chats)
	}
	assertChannelStoryMaxID(t, findChannelClass(second.Chats, source.Channel.ID), 31)
	assertChannelStoryMaxID(t, findChannelClass(second.Chats, repostPublic.ID), 32)

	if _, err := r.onChannelsDeleteMessages(WithUserID(ctx, creator.ID), &tg.ChannelsDeleteMessagesRequest{
		Channel: &tg.InputChannel{ChannelID: targetPublic.ID, AccessHash: targetPublic.AccessHash},
		ID:      []int{msg.ID},
	}); err != nil {
		t.Fatalf("delete public story message: %v", err)
	}
	afterMessageDelete, err := r.onStatsGetStoryPublicForwards(WithUserID(ctx, creator.ID), &tg.StatsGetStoryPublicForwardsRequest{
		Peer:  &tg.InputPeerChannel{ChannelID: source.Channel.ID, AccessHash: source.Channel.AccessHash},
		ID:    31,
		Limit: 20,
	})
	if err != nil {
		t.Fatalf("get story public forwards after message delete: %v", err)
	}
	if afterMessageDelete.Count != 1 || len(afterMessageDelete.Forwards) != 1 {
		t.Fatalf("public forwards after message delete = %+v, want one repost", afterMessageDelete)
	}
	if _, ok := afterMessageDelete.Forwards[0].(*tg.PublicForwardStory); !ok {
		t.Fatalf("public forwards after message delete row = %T, want *tg.PublicForwardStory", afterMessageDelete.Forwards[0])
	}

	if _, err := storyStore.DeleteStories(ctx, repostPeer, []int{repost.ID}, 1700000410); err != nil {
		t.Fatalf("delete public repost: %v", err)
	}
	empty, err := r.onStatsGetStoryPublicForwards(WithUserID(ctx, creator.ID), &tg.StatsGetStoryPublicForwardsRequest{
		Peer:  &tg.InputPeerChannel{ChannelID: source.Channel.ID, AccessHash: source.Channel.AccessHash},
		ID:    31,
		Limit: 20,
	})
	if err != nil {
		t.Fatalf("get story public forwards after repost delete: %v", err)
	}
	if empty.Count != 0 || len(empty.Forwards) != 0 {
		t.Fatalf("public forwards after repost delete = %+v, want empty", empty)
	}
}

func TestStatsStoryRPCsValidateStoryIDBeforePeer(t *testing.T) {
	ctx := WithUserID(context.Background(), 1000000001)
	r := New(Config{}, Deps{}, zaptest.NewLogger(t), fixedClock{now: time.Unix(1700000400, 0)})

	for _, id := range []int{0, -1, domain.MaxStoryID + 1} {
		if _, err := r.onStatsGetStoryStats(ctx, &tg.StatsGetStoryStatsRequest{ID: id}); err == nil || !tgerr.Is(err, "STORY_ID_INVALID") {
			t.Fatalf("stats.getStoryStats id %d err = %v, want STORY_ID_INVALID", id, err)
		}
		if _, err := r.onStatsGetStoryPublicForwards(ctx, &tg.StatsGetStoryPublicForwardsRequest{ID: id, Limit: 1}); err == nil || !tgerr.Is(err, "STORY_ID_INVALID") {
			t.Fatalf("stats.getStoryPublicForwards id %d err = %v, want STORY_ID_INVALID", id, err)
		}
	}
}

func TestStatsGetStoryStatsUsesStoryOwnerPermissions(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, err := userStore.Create(ctx, domain.User{AccessHash: 91441, Phone: "15550091441", FirstName: "Owner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	other, err := userStore.Create(ctx, domain.User{AccessHash: 91442, Phone: "15550091442", FirstName: "Other"})
	if err != nil {
		t.Fatalf("create other: %v", err)
	}
	admin, err := userStore.Create(ctx, domain.User{AccessHash: 91443, Phone: "15550091443", FirstName: "Admin"})
	if err != nil {
		t.Fatalf("create admin: %v", err)
	}
	member, err := userStore.Create(ctx, domain.User{AccessHash: 91444, Phone: "15550091444", FirstName: "Member"})
	if err != nil {
		t.Fatalf("create member: %v", err)
	}
	channelStore := memory.NewChannelStore()
	channelService := appchannels.NewService(channelStore)
	created, err := channelService.CreateChannel(ctx, owner.ID, domain.CreateChannelRequest{
		CreatorUserID: owner.ID,
		Title:         "story stats channel",
		Broadcast:     true,
		MemberUserIDs: []int64{admin.ID, member.ID},
		Date:          1700000400,
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	if _, err := channelService.EditAdmin(ctx, owner.ID, domain.EditChannelAdminRequest{
		ChannelID: created.Channel.ID,
		MemberID:  admin.ID,
		AdminRights: domain.ChannelAdminRights{
			EditStories: true,
		},
		Date: 1700000401,
	}); err != nil {
		t.Fatalf("promote story stats admin: %v", err)
	}
	r := New(Config{}, Deps{
		Users:    appusers.NewService(userStore),
		Channels: channelService,
		Stories:  appstories.NewService(memory.NewStoryStore(), appstories.WithChannelStoryAccess(channelService)),
	}, zaptest.NewLogger(t), fixedClock{now: time.Unix(1700000402, 0)})

	if _, err := r.onStatsGetStoryStats(WithUserID(ctx, owner.ID), &tg.StatsGetStoryStatsRequest{
		Peer: &tg.InputPeerSelf{},
		ID:   1,
	}); err != nil {
		t.Fatalf("owner self story stats: %v", err)
	}
	if _, err := r.onStatsGetStoryStats(WithUserID(ctx, owner.ID), &tg.StatsGetStoryStatsRequest{
		Peer: &tg.InputPeerUser{UserID: other.ID, AccessHash: other.AccessHash},
		ID:   1,
	}); err == nil || !tgerr.Is(err, "PEER_ID_INVALID") {
		t.Fatalf("other user story stats err = %v, want PEER_ID_INVALID", err)
	}
	channelPeer := &tg.InputPeerChannel{ChannelID: created.Channel.ID, AccessHash: created.Channel.AccessHash}
	if _, err := r.onStatsGetStoryStats(WithUserID(ctx, member.ID), &tg.StatsGetStoryStatsRequest{
		Peer: channelPeer,
		ID:   1,
	}); err == nil || !tgerr.Is(err, "CHAT_ADMIN_REQUIRED") {
		t.Fatalf("member channel story stats err = %v, want CHAT_ADMIN_REQUIRED", err)
	}
	if _, err := r.onStatsGetStoryStats(WithUserID(ctx, admin.ID), &tg.StatsGetStoryStatsRequest{
		Peer: channelPeer,
		ID:   1,
	}); err != nil {
		t.Fatalf("admin channel story stats: %v", err)
	}
}

func TestStoriesSendReactionDoesNotFakeChannelAdminNewReactionNotification(t *testing.T) {
	ctx := context.Background()
	creatorAuthKeyID := [8]byte{8, 1, 1}
	adminAuthKeyID := [8]byte{8, 1, 2}
	reactorAuthKeyID := [8]byte{8, 1, 3}
	userStore := memory.NewUserStore()
	creator, err := userStore.Create(ctx, domain.User{AccessHash: 91321, Phone: "15550091321", FirstName: "Creator"})
	if err != nil {
		t.Fatalf("create creator: %v", err)
	}
	admin, err := userStore.Create(ctx, domain.User{AccessHash: 91322, Phone: "15550091322", FirstName: "Admin"})
	if err != nil {
		t.Fatalf("create admin: %v", err)
	}
	reactor, err := userStore.Create(ctx, domain.User{AccessHash: 91323, Phone: "15550091323", FirstName: "Reactor"})
	if err != nil {
		t.Fatalf("create reactor: %v", err)
	}
	channelStore := memory.NewChannelStore()
	channelService := appchannels.NewService(channelStore)
	created, err := channelService.CreateChannel(ctx, creator.ID, domain.CreateChannelRequest{
		CreatorUserID: creator.ID,
		Title:         "story reaction notification channel",
		Broadcast:     true,
		MemberUserIDs: []int64{admin.ID, reactor.ID},
		Date:          1700000000,
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	if _, err := channelService.EditAdmin(ctx, creator.ID, domain.EditChannelAdminRequest{
		ChannelID: created.Channel.ID,
		MemberID:  admin.ID,
		AdminRights: domain.ChannelAdminRights{
			EditStories: true,
		},
		Date: 1700000001,
	}); err != nil {
		t.Fatalf("promote story admin: %v", err)
	}
	storyStore := memory.NewStoryStore()
	owner := domain.Peer{Type: domain.PeerTypeChannel, ID: created.Channel.ID}
	if _, err := storyStore.UpsertStory(ctx, domain.UpsertStoryRequest{Story: domain.Story{
		Owner:      owner,
		ID:         1,
		Date:       1700000002,
		ExpireDate: 1700003600,
		Public:     true,
	}}); err != nil {
		t.Fatalf("upsert channel story: %v", err)
	}
	r := New(Config{}, Deps{
		Users:    appusers.NewService(userStore),
		Channels: channelService,
		Stories:  appstories.NewService(storyStore),
		Updates:  appupdates.NewService(memory.NewUpdateStateStore(), memory.NewUpdateEventStore()),
	}, zaptest.NewLogger(t), fixedClock{now: time.Unix(1700000100, 0)})
	updates, err := r.onStoriesSendReaction(
		WithSessionID(WithAuthKeyID(WithUserID(ctx, reactor.ID), reactorAuthKeyID), 8123),
		&tg.StoriesSendReactionRequest{
			Peer:     &tg.InputPeerChannel{ChannelID: created.Channel.ID, AccessHash: created.Channel.AccessHash},
			StoryID:  1,
			Reaction: &tg.ReactionEmoji{Emoticon: "🔥"},
		},
	)
	if err != nil {
		t.Fatalf("send channel story reaction: %v", err)
	}
	gotUpdates, ok := updates.(*tg.Updates)
	if !ok || len(gotUpdates.Updates) != 1 {
		t.Fatalf("send updates = %T %+v, want one updateSentStoryReaction", updates, updates)
	}
	sentReaction, ok := gotUpdates.Updates[0].(*tg.UpdateSentStoryReaction)
	if !ok {
		t.Fatalf("send update = %T, want updateSentStoryReaction", gotUpdates.Updates[0])
	}
	if peer, ok := sentReaction.Peer.(*tg.PeerChannel); !ok || peer.ChannelID != created.Channel.ID {
		t.Fatalf("sent reaction peer = %T %+v, want channel", sentReaction.Peer, sentReaction.Peer)
	}

	for _, tt := range []struct {
		name      string
		userID    int64
		authKeyID [8]byte
	}{
		{name: "creator", userID: creator.ID, authKeyID: creatorAuthKeyID},
		{name: "story admin", userID: admin.ID, authKeyID: adminAuthKeyID},
	} {
		t.Run(tt.name, func(t *testing.T) {
			diff, err := r.onUpdatesGetDifference(WithAuthKeyID(WithUserID(ctx, tt.userID), tt.authKeyID), &tg.UpdatesGetDifferenceRequest{Pts: 0})
			if err != nil {
				t.Fatalf("get difference: %v", err)
			}
			if _, ok := diff.(*tg.UpdatesDifferenceEmpty); !ok {
				t.Fatalf("difference = %T %+v, want empty; updateNewStoryReaction has no owner/channel field for channel admin routing", diff, diff)
			}
		})
	}
}

func TestStoriesGetStoryViewsListReturnsChannelAdminViews(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	creator, err := userStore.Create(ctx, domain.User{AccessHash: 92301, Phone: "15550092301", FirstName: "Creator"})
	if err != nil {
		t.Fatalf("create creator: %v", err)
	}
	admin, err := userStore.Create(ctx, domain.User{AccessHash: 92302, Phone: "15550092302", FirstName: "Admin"})
	if err != nil {
		t.Fatalf("create admin: %v", err)
	}
	member, err := userStore.Create(ctx, domain.User{AccessHash: 92303, Phone: "15550092303", FirstName: "Member"})
	if err != nil {
		t.Fatalf("create member: %v", err)
	}
	viewer, err := userStore.Create(ctx, domain.User{AccessHash: 92304, Phone: "15550092304", FirstName: "Viewer"})
	if err != nil {
		t.Fatalf("create viewer: %v", err)
	}
	channelStore := memory.NewChannelStore()
	channelService := appchannels.NewService(channelStore)
	created, err := channelService.CreateChannel(ctx, creator.ID, domain.CreateChannelRequest{
		CreatorUserID: creator.ID,
		Title:         "story views channel",
		Broadcast:     true,
		MemberUserIDs: []int64{admin.ID, member.ID, viewer.ID},
		Date:          1700005000,
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	if _, err := channelService.EditAdmin(ctx, creator.ID, domain.EditChannelAdminRequest{
		ChannelID: created.Channel.ID,
		MemberID:  admin.ID,
		AdminRights: domain.ChannelAdminRights{
			EditStories: true,
		},
		Date: 1700005001,
	}); err != nil {
		t.Fatalf("promote story admin: %v", err)
	}
	storyStore := memory.NewStoryStore()
	owner := domain.Peer{Type: domain.PeerTypeChannel, ID: created.Channel.ID}
	if _, err := storyStore.UpsertStory(ctx, domain.UpsertStoryRequest{Story: domain.Story{
		Owner:      owner,
		ID:         1,
		Date:       1700005002,
		ExpireDate: 1700008600,
		Public:     true,
	}}); err != nil {
		t.Fatalf("upsert channel story: %v", err)
	}
	if _, err := storyStore.IncrementViews(ctx, viewer.ID, owner, []int{1}, 1700005003); err != nil {
		t.Fatalf("increment channel story view: %v", err)
	}
	r := New(Config{}, Deps{
		Users:    appusers.NewService(userStore),
		Channels: channelService,
		Stories:  appstories.NewService(storyStore, appstories.WithChannelStoryAccess(channelService)),
	}, zaptest.NewLogger(t), fixedClock{now: time.Unix(1700005100, 0)})
	req := &tg.StoriesGetStoryViewsListRequest{
		Peer:  &tg.InputPeerChannel{ChannelID: created.Channel.ID, AccessHash: created.Channel.AccessHash},
		ID:    1,
		Limit: 10,
	}

	creatorList, err := r.onStoriesGetStoryViewsList(WithUserID(ctx, creator.ID), req)
	if err != nil {
		t.Fatalf("creator get story views list: %v", err)
	}
	assertStoryViewsListViewer(t, creatorList, viewer.ID)

	adminList, err := r.onStoriesGetStoryViewsList(WithUserID(ctx, admin.ID), req)
	if err != nil {
		t.Fatalf("admin get story views list: %v", err)
	}
	assertStoryViewsListViewer(t, adminList, viewer.ID)

	if _, err := r.onStoriesGetStoryViewsList(WithUserID(ctx, member.ID), req); err == nil || !tgerr.Is(err, "CHAT_ADMIN_REQUIRED") {
		t.Fatalf("member get story views list err = %v, want CHAT_ADMIN_REQUIRED", err)
	}
}

func TestStoriesChannelAdminCanSendEditDeleteStory(t *testing.T) {
	ctx := context.Background()
	authKeyID := [8]byte{6, 6, 1}
	userStore := memory.NewUserStore()
	creator, err := userStore.Create(ctx, domain.User{AccessHash: 91401, Phone: "15550091401", FirstName: "Creator"})
	if err != nil {
		t.Fatalf("create creator: %v", err)
	}
	admin, err := userStore.Create(ctx, domain.User{AccessHash: 91402, Phone: "15550091402", FirstName: "Admin"})
	if err != nil {
		t.Fatalf("create admin: %v", err)
	}
	member, err := userStore.Create(ctx, domain.User{AccessHash: 91403, Phone: "15550091403", FirstName: "Member"})
	if err != nil {
		t.Fatalf("create member: %v", err)
	}
	channelStore := memory.NewChannelStore()
	channelService := appchannels.NewService(channelStore)
	created, err := channelService.CreateChannel(ctx, creator.ID, domain.CreateChannelRequest{
		CreatorUserID: creator.ID,
		Title:         "story publish channel",
		Broadcast:     true,
		MemberUserIDs: []int64{admin.ID, member.ID},
		Date:          1700002000,
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	if _, err := channelService.EditAdmin(ctx, creator.ID, domain.EditChannelAdminRequest{
		ChannelID: created.Channel.ID,
		MemberID:  admin.ID,
		AdminRights: domain.ChannelAdminRights{
			PostStories:   true,
			EditStories:   true,
			DeleteStories: true,
		},
		Date: 1700002001,
	}); err != nil {
		t.Fatalf("promote story admin: %v", err)
	}
	storyStore := memory.NewStoryStore()
	stateStore := memory.NewUpdateStateStore()
	updateStore := memory.NewUpdateEventStore()
	r := New(Config{}, Deps{
		Users:    appusers.NewService(userStore),
		Channels: channelService,
		Stories:  appstories.NewService(storyStore, appstories.WithChannelStoryAccess(channelService)),
		Updates:  appupdates.NewService(stateStore, updateStore),
		Files:    &fakeFiles{photos: map[int64]domain.Photo{}},
	}, zaptest.NewLogger(t), fixedClock{now: time.Unix(1700002100, 0)})
	channelPeer := &tg.InputPeerChannel{ChannelID: created.Channel.ID, AccessHash: created.Channel.AccessHash}

	if _, err := r.onStoriesCanSendStory(WithUserID(ctx, member.ID), channelPeer); err == nil || !strings.Contains(err.Error(), "CHAT_ADMIN_REQUIRED") {
		t.Fatalf("member canSendStory err = %v, want CHAT_ADMIN_REQUIRED", err)
	}
	canSend, err := r.onStoriesCanSendStory(WithUserID(ctx, admin.ID), channelPeer)
	if err != nil {
		t.Fatalf("admin canSendStory: %v", err)
	}
	if canSend.CountRemains <= 0 {
		t.Fatalf("canSendStory = %+v, want positive count", canSend)
	}

	reqCtx := WithSessionID(WithAuthKeyID(WithUserID(ctx, admin.ID), authKeyID), 66)
	updates, err := r.onStoriesSendStory(reqCtx, &tg.StoriesSendStoryRequest{
		Peer:         channelPeer,
		Media:        &tg.InputMediaUploadedPhoto{File: &tg.InputFile{ID: 62, Parts: 1, Name: "channel-story.jpg"}},
		PrivacyRules: []tg.InputPrivacyRuleClass{&tg.InputPrivacyValueAllowAll{}},
		RandomID:     9001,
		Period:       86400,
		Caption:      "channel story",
	})
	if err != nil {
		t.Fatalf("send channel story: %v", err)
	}
	sendUpdates, ok := updates.(*tg.Updates)
	if !ok || len(sendUpdates.Updates) != 2 {
		t.Fatalf("send updates = %T %+v, want updateStoryID + updateStory", updates, updates)
	}
	idUpdate, ok := sendUpdates.Updates[0].(*tg.UpdateStoryID)
	if !ok || idUpdate.ID != 1 || idUpdate.RandomID != 9001 {
		t.Fatalf("story id update = %T %+v, want id 1 random 9001", sendUpdates.Updates[0], sendUpdates.Updates[0])
	}
	storyUpdate, ok := sendUpdates.Updates[1].(*tg.UpdateStory)
	if !ok {
		t.Fatalf("story update = %T, want updateStory", sendUpdates.Updates[1])
	}
	if peer, ok := storyUpdate.Peer.(*tg.PeerChannel); !ok || peer.ChannelID != created.Channel.ID {
		t.Fatalf("story update peer = %T %+v, want channel peer", storyUpdate.Peer, storyUpdate.Peer)
	}
	item, ok := storyUpdate.Story.(*tg.StoryItem)
	if !ok || item.ID != 1 || item.Caption != "channel story" || !item.Public {
		t.Fatalf("sent channel story = %T %+v, want public story item", storyUpdate.Story, storyUpdate.Story)
	}
	assertChannelStoryMaxID(t, findChannelClass(sendUpdates.Chats, created.Channel.ID), 1)

	retryUpdatesClass, err := r.onStoriesSendStory(reqCtx, &tg.StoriesSendStoryRequest{
		Peer:         channelPeer,
		Media:        &tg.InputMediaUploadedPhoto{File: &tg.InputFile{ID: 63, Parts: 1, Name: "channel-story-retry.jpg"}},
		PrivacyRules: []tg.InputPrivacyRuleClass{&tg.InputPrivacyValueAllowAll{}},
		RandomID:     9001,
		Period:       86400,
		Caption:      "retry must not replace channel story",
	})
	if err != nil {
		t.Fatalf("retry channel story: %v", err)
	}
	retryUpdates, ok := retryUpdatesClass.(*tg.Updates)
	if !ok || len(retryUpdates.Updates) != 2 {
		t.Fatalf("retry updates = %T %+v, want updateStoryID + updateStory", retryUpdatesClass, retryUpdatesClass)
	}
	retryID, ok := retryUpdates.Updates[0].(*tg.UpdateStoryID)
	if !ok || retryID.ID != idUpdate.ID || retryID.RandomID != 9001 {
		t.Fatalf("retry story id update = %T %+v, want original id/random", retryUpdates.Updates[0], retryUpdates.Updates[0])
	}
	retryStoryUpdate, ok := retryUpdates.Updates[1].(*tg.UpdateStory)
	if !ok {
		t.Fatalf("retry story update = %T, want updateStory", retryUpdates.Updates[1])
	}
	retryItem, ok := retryStoryUpdate.Story.(*tg.StoryItem)
	if !ok || retryItem.ID != idUpdate.ID || retryItem.Caption != "channel story" {
		t.Fatalf("retry channel story = %T %+v, want original story snapshot", retryStoryUpdate.Story, retryStoryUpdate.Story)
	}

	memberStories, err := r.onStoriesGetPeerStories(WithUserID(ctx, member.ID), &tg.InputPeerChannel{ChannelID: created.Channel.ID, AccessHash: created.Channel.AccessHash})
	if err != nil {
		t.Fatalf("member get channel stories: %v", err)
	}
	if len(memberStories.Stories.Stories) != 1 {
		t.Fatalf("member channel stories = %+v, want one story", memberStories.Stories.Stories)
	}
	memberItem, ok := memberStories.Stories.Stories[0].(*tg.StoryItem)
	if !ok || memberItem.ID != idUpdate.ID || memberItem.Caption != "channel story" {
		t.Fatalf("member channel story after retry = %T %+v, want original story", memberStories.Stories.Stories[0], memberStories.Stories.Stories[0])
	}

	edit := &tg.StoriesEditStoryRequest{Peer: channelPeer, ID: idUpdate.ID}
	edit.SetCaption("edited channel story")
	updates, err = r.onStoriesEditStory(reqCtx, edit)
	if err != nil {
		t.Fatalf("edit channel story: %v", err)
	}
	editUpdates := updates.(*tg.Updates)
	edited, ok := editUpdates.Updates[0].(*tg.UpdateStory)
	if !ok {
		t.Fatalf("edit update = %T, want updateStory", editUpdates.Updates[0])
	}
	editedItem, ok := edited.Story.(*tg.StoryItem)
	if !ok || !editedItem.Edited || editedItem.Caption != "edited channel story" {
		t.Fatalf("edited story = %T %+v, want edited channel story", edited.Story, edited.Story)
	}

	pinnedIDs, err := r.onStoriesTogglePinned(reqCtx, &tg.StoriesTogglePinnedRequest{
		Peer:   channelPeer,
		ID:     []int{idUpdate.ID},
		Pinned: true,
	})
	if err != nil {
		t.Fatalf("pin channel story: %v", err)
	}
	if len(pinnedIDs) != 1 || pinnedIDs[0] != idUpdate.ID {
		t.Fatalf("pinned ids = %v, want story id", pinnedIDs)
	}
	retryPinnedIDs, err := r.onStoriesTogglePinned(reqCtx, &tg.StoriesTogglePinnedRequest{
		Peer:   channelPeer,
		ID:     []int{idUpdate.ID},
		Pinned: true,
	})
	if err != nil {
		t.Fatalf("retry pin channel story: %v", err)
	}
	if len(retryPinnedIDs) != 1 || retryPinnedIDs[0] != idUpdate.ID {
		t.Fatalf("retry pinned ids = %v, want story id echo", retryPinnedIDs)
	}
	pinnedStories, err := r.onStoriesGetPinnedStories(WithUserID(ctx, member.ID), &tg.StoriesGetPinnedStoriesRequest{
		Peer:  channelPeer,
		Limit: 10,
	})
	if err != nil {
		t.Fatalf("member get pinned channel stories: %v", err)
	}
	if len(pinnedStories.Stories) != 1 {
		t.Fatalf("member pinned channel stories = %+v, want one story", pinnedStories.Stories)
	}

	deletedIDs, err := r.onStoriesDeleteStories(reqCtx, &tg.StoriesDeleteStoriesRequest{
		Peer: channelPeer,
		ID:   []int{idUpdate.ID},
	})
	if err != nil {
		t.Fatalf("delete channel story: %v", err)
	}
	if len(deletedIDs) != 1 || deletedIDs[0] != idUpdate.ID {
		t.Fatalf("deleted ids = %v, want story id", deletedIDs)
	}
	memberStories, err = r.onStoriesGetPeerStories(WithUserID(ctx, member.ID), &tg.InputPeerChannel{ChannelID: created.Channel.ID, AccessHash: created.Channel.AccessHash})
	if err != nil {
		t.Fatalf("member get channel stories after delete: %v", err)
	}
	if len(memberStories.Stories.Stories) != 0 {
		t.Fatalf("member channel stories after delete = %+v, want empty", memberStories.Stories.Stories)
	}
	memberDiff, err := r.onUpdatesGetDifference(WithAuthKeyID(WithUserID(ctx, member.ID), [8]byte{6, 6, 2}), &tg.UpdatesGetDifferenceRequest{Pts: 0})
	if err != nil {
		t.Fatalf("get member difference: %v", err)
	}
	memberUpdates, ok := memberDiff.(*tg.UpdatesDifference)
	if !ok || memberUpdates.State.Pts != 4 || len(memberUpdates.OtherUpdates) != 4 {
		t.Fatalf("member difference = %T %+v, want pts 4 and four story updates", memberDiff, memberDiff)
	}
	memberSend, ok := memberUpdates.OtherUpdates[0].(*tg.UpdateStory)
	if !ok {
		t.Fatalf("member first diff update = %T, want updateStory", memberUpdates.OtherUpdates[0])
	}
	if peer, ok := memberSend.Peer.(*tg.PeerChannel); !ok || peer.ChannelID != created.Channel.ID {
		t.Fatalf("member story peer = %T %+v, want channel peer", memberSend.Peer, memberSend.Peer)
	}
	memberSendItem, ok := memberSend.Story.(*tg.StoryItem)
	if !ok || memberSendItem.ID != idUpdate.ID || memberSendItem.Caption != "channel story" {
		t.Fatalf("member sent story = %T %+v, want original story item", memberSend.Story, memberSend.Story)
	}
	if _, hasViews := memberSendItem.GetViews(); memberSendItem.Out || len(memberSendItem.Privacy) != 0 || hasViews {
		t.Fatalf("member sent story owner-only fields = out:%v privacy:%v views:%+v, want stripped", memberSendItem.Out, memberSendItem.Privacy, memberSendItem.Views)
	}
	memberEdit, ok := memberUpdates.OtherUpdates[1].(*tg.UpdateStory)
	if !ok {
		t.Fatalf("member second diff update = %T, want updateStory", memberUpdates.OtherUpdates[1])
	}
	memberEditItem, ok := memberEdit.Story.(*tg.StoryItem)
	if !ok || !memberEditItem.Edited || memberEditItem.Caption != "edited channel story" {
		t.Fatalf("member edited story = %T %+v, want edited story item", memberEdit.Story, memberEdit.Story)
	}
	memberPinned, ok := memberUpdates.OtherUpdates[2].(*tg.UpdateStory)
	if !ok {
		t.Fatalf("member third diff update = %T, want updateStory", memberUpdates.OtherUpdates[2])
	}
	memberPinnedItem, ok := memberPinned.Story.(*tg.StoryItem)
	if !ok || !memberPinnedItem.Pinned {
		t.Fatalf("member pinned story = %T %+v, want pinned story item", memberPinned.Story, memberPinned.Story)
	}
	memberDelete, ok := memberUpdates.OtherUpdates[3].(*tg.UpdateStory)
	if !ok {
		t.Fatalf("member fourth diff update = %T, want updateStory", memberUpdates.OtherUpdates[3])
	}
	if deleted, ok := memberDelete.Story.(*tg.StoryItemDeleted); !ok || deleted.ID != idUpdate.ID {
		t.Fatalf("member deleted story = %T %+v, want storyItemDeleted", memberDelete.Story, memberDelete.Story)
	}
	if findChannelClass(memberUpdates.Chats, created.Channel.ID) == nil {
		t.Fatalf("member difference chats = %+v, want story owner channel", memberUpdates.Chats)
	}
	diff, err := r.onUpdatesGetDifference(reqCtx, &tg.UpdatesGetDifferenceRequest{Pts: 0})
	if err != nil {
		t.Fatalf("get admin difference: %v", err)
	}
	got, ok := diff.(*tg.UpdatesDifference)
	if !ok || got.State.Pts != 4 || len(got.OtherUpdates) != 4 {
		t.Fatalf("admin difference = %T %+v, want pts 4 and four story updates", diff, diff)
	}
	last, ok := got.OtherUpdates[3].(*tg.UpdateStory)
	if !ok {
		t.Fatalf("last diff update = %T, want updateStory", got.OtherUpdates[2])
	}
	if _, ok := last.Story.(*tg.StoryItemDeleted); !ok {
		t.Fatalf("last diff story = %T, want storyItemDeleted", last.Story)
	}
}

func TestStoriesAlbumCompatHandlersValidatePeerAndPermissions(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	creator, err := userStore.Create(ctx, domain.User{AccessHash: 92201, Phone: "15550092201", FirstName: "Creator"})
	if err != nil {
		t.Fatalf("create creator: %v", err)
	}
	admin, err := userStore.Create(ctx, domain.User{AccessHash: 92202, Phone: "15550092202", FirstName: "Admin"})
	if err != nil {
		t.Fatalf("create admin: %v", err)
	}
	member, err := userStore.Create(ctx, domain.User{AccessHash: 92203, Phone: "15550092203", FirstName: "Member"})
	if err != nil {
		t.Fatalf("create member: %v", err)
	}
	channelStore := memory.NewChannelStore()
	channelService := appchannels.NewService(channelStore)
	created, err := channelService.CreateChannel(ctx, creator.ID, domain.CreateChannelRequest{
		CreatorUserID: creator.ID,
		Title:         "story album channel",
		Broadcast:     true,
		MemberUserIDs: []int64{admin.ID, member.ID},
		Date:          1700004000,
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	if _, err := channelService.EditAdmin(ctx, creator.ID, domain.EditChannelAdminRequest{
		ChannelID: created.Channel.ID,
		MemberID:  admin.ID,
		AdminRights: domain.ChannelAdminRights{
			EditStories: true,
		},
		Date: 1700004001,
	}); err != nil {
		t.Fatalf("promote album admin: %v", err)
	}
	r := New(Config{}, Deps{
		Users:    appusers.NewService(userStore),
		Channels: channelService,
	}, zaptest.NewLogger(t), fixedClock{now: time.Unix(1700004100, 0)})
	creatorCtx := WithUserID(ctx, creator.ID)
	adminCtx := WithUserID(ctx, admin.ID)
	memberCtx := WithUserID(ctx, member.ID)
	channelPeer := &tg.InputPeerChannel{ChannelID: created.Channel.ID, AccessHash: created.Channel.AccessHash}

	albums, err := r.onStoriesGetAlbums(creatorCtx, &tg.StoriesGetAlbumsRequest{Peer: channelPeer, Hash: 123})
	if err != nil {
		t.Fatalf("get albums: %v", err)
	}
	gotAlbums, ok := albums.(*tg.StoriesAlbums)
	if !ok {
		t.Fatalf("albums = %T, want *tg.StoriesAlbums", albums)
	}
	if gotAlbums.Hash != 0 || len(gotAlbums.Albums) != 0 {
		t.Fatalf("albums = %+v, want empty modified list", gotAlbums)
	}
	albumStories, err := r.onStoriesGetAlbumStories(creatorCtx, &tg.StoriesGetAlbumStoriesRequest{
		Peer:    channelPeer,
		AlbumID: 1,
		Limit:   20,
	})
	if err != nil {
		t.Fatalf("get album stories: %v", err)
	}
	if len(albumStories.Stories) != 0 || len(albumStories.Users) != 0 || len(albumStories.Chats) != 0 {
		t.Fatalf("album stories = %+v, want empty stories page", albumStories)
	}
	if _, err := r.onStoriesGetAlbumStories(creatorCtx, &tg.StoriesGetAlbumStoriesRequest{
		Peer:    &tg.InputPeerEmpty{},
		AlbumID: 0,
		Limit:   20,
	}); err == nil || !tgerr.Is(err, "INPUT_REQUEST_INVALID") {
		t.Fatalf("invalid album id before peer err = %v, want INPUT_REQUEST_INVALID", err)
	}
	if _, err := r.onStoriesGetAlbumStories(creatorCtx, &tg.StoriesGetAlbumStoriesRequest{
		Peer:    &tg.InputPeerEmpty{},
		AlbumID: 1,
		Offset:  -1,
		Limit:   20,
	}); err == nil || !tgerr.Is(err, "OFFSET_INVALID") {
		t.Fatalf("invalid album offset before peer err = %v, want OFFSET_INVALID", err)
	}
	if _, err := r.onStoriesGetAlbumStories(creatorCtx, &tg.StoriesGetAlbumStoriesRequest{
		Peer:    &tg.InputPeerEmpty{},
		AlbumID: 1,
		Offset:  domain.MaxStoryAlbumOffset + 1,
		Limit:   20,
	}); err == nil || !tgerr.Is(err, "OFFSET_INVALID") {
		t.Fatalf("oversized album offset before peer err = %v, want OFFSET_INVALID", err)
	}
	if _, err := r.onStoriesGetAlbumStories(creatorCtx, &tg.StoriesGetAlbumStoriesRequest{
		Peer:    &tg.InputPeerEmpty{},
		AlbumID: 1,
		Limit:   domain.MaxStoryListLimit + 1,
	}); err == nil || !tgerr.Is(err, "LIMIT_INVALID") {
		t.Fatalf("invalid album limit before peer err = %v, want LIMIT_INVALID", err)
	}
	if _, err := r.onStoriesReorderAlbums(adminCtx, &tg.StoriesReorderAlbumsRequest{
		Peer:  &tg.InputPeerEmpty{},
		Order: []int{1, 1},
	}); err == nil || !tgerr.Is(err, "INPUT_REQUEST_INVALID") {
		t.Fatalf("duplicate reorder albums before peer err = %v, want INPUT_REQUEST_INVALID", err)
	}
	if _, err := r.onStoriesReorderAlbums(adminCtx, &tg.StoriesReorderAlbumsRequest{
		Peer:  &tg.InputPeerEmpty{},
		Order: make([]int, domain.MaxStoryIDs+1),
	}); err == nil || !tgerr.Is(err, "LIMIT_INVALID") {
		t.Fatalf("oversized reorder albums before peer err = %v, want LIMIT_INVALID", err)
	}
	okReorder, err := r.onStoriesReorderAlbums(adminCtx, &tg.StoriesReorderAlbumsRequest{
		Peer:  channelPeer,
		Order: []int{2, 1},
	})
	if err != nil || !okReorder {
		t.Fatalf("admin reorder albums = %v, %v, want true nil", okReorder, err)
	}
	okDelete, err := r.onStoriesDeleteAlbum(adminCtx, &tg.StoriesDeleteAlbumRequest{
		Peer:    channelPeer,
		AlbumID: 1,
	})
	if err != nil || !okDelete {
		t.Fatalf("admin delete album = %v, %v, want true nil", okDelete, err)
	}
	if _, err := r.onStoriesDeleteAlbum(adminCtx, &tg.StoriesDeleteAlbumRequest{Peer: &tg.InputPeerEmpty{}, AlbumID: 0}); err == nil || !tgerr.Is(err, "INPUT_REQUEST_INVALID") {
		t.Fatalf("invalid delete album id before peer err = %v, want INPUT_REQUEST_INVALID", err)
	}
	if _, err := r.onStoriesReorderAlbums(memberCtx, &tg.StoriesReorderAlbumsRequest{Peer: channelPeer, Order: []int{1}}); err == nil || !tgerr.Is(err, "CHAT_ADMIN_REQUIRED") {
		t.Fatalf("member reorder albums err = %v, want CHAT_ADMIN_REQUIRED", err)
	}
	if _, err := r.onStoriesReorderAlbums(adminCtx, &tg.StoriesReorderAlbumsRequest{Peer: channelPeer, Order: []int{1, 1}}); err == nil || !tgerr.Is(err, "INPUT_REQUEST_INVALID") {
		t.Fatalf("duplicate reorder albums err = %v, want INPUT_REQUEST_INVALID", err)
	}
	if _, err := r.onStoriesReorderAlbums(adminCtx, &tg.StoriesReorderAlbumsRequest{Peer: channelPeer, Order: make([]int, domain.MaxStoryIDs+1)}); err == nil || !tgerr.Is(err, "LIMIT_INVALID") {
		t.Fatalf("oversized reorder albums err = %v, want LIMIT_INVALID", err)
	}

	if _, err := r.onStoriesCreateAlbum(creatorCtx, &tg.StoriesCreateAlbumRequest{
		Peer:    &tg.InputPeerSelf{},
		Title:   "Favorites",
		Stories: []int{1},
	}); err == nil || !tgerr.Is(err, "METHOD_INVALID") {
		t.Fatalf("create album err = %v, want METHOD_INVALID", err)
	}
	if _, err := r.onStoriesCreateAlbum(creatorCtx, &tg.StoriesCreateAlbumRequest{
		Peer:    &tg.InputPeerSelf{},
		Title:   "Favorites",
		Stories: []int{1, 1},
	}); err == nil || !tgerr.Is(err, "INPUT_REQUEST_INVALID") {
		t.Fatalf("duplicate create album stories err = %v, want INPUT_REQUEST_INVALID", err)
	}
	if _, err := r.onStoriesCreateAlbum(creatorCtx, &tg.StoriesCreateAlbumRequest{
		Peer:  &tg.InputPeerEmpty{},
		Title: strings.Repeat("x", maxStoryAlbumTitleLength+1),
	}); err == nil || !tgerr.Is(err, "LIMIT_INVALID") {
		t.Fatalf("long title create before peer err = %v, want LIMIT_INVALID", err)
	}
	if _, err := r.onStoriesCreateAlbum(creatorCtx, &tg.StoriesCreateAlbumRequest{
		Peer:    &tg.InputPeerEmpty{},
		Title:   "Favorites",
		Stories: []int{1, 1},
	}); err == nil || !tgerr.Is(err, "INPUT_REQUEST_INVALID") {
		t.Fatalf("duplicate create album stories before peer err = %v, want INPUT_REQUEST_INVALID", err)
	}
	updateReq := &tg.StoriesUpdateAlbumRequest{
		Peer:    &tg.InputPeerSelf{},
		AlbumID: 1,
	}
	updateReq.SetTitle("Travel")
	if _, err := r.onStoriesUpdateAlbum(creatorCtx, updateReq); err == nil || !tgerr.Is(err, "METHOD_INVALID") {
		t.Fatalf("update album err = %v, want METHOD_INVALID", err)
	}
	invalidUpdateReq := &tg.StoriesUpdateAlbumRequest{
		Peer:    &tg.InputPeerSelf{},
		AlbumID: 1,
	}
	invalidUpdateReq.SetAddStories([]int{0})
	if _, err := r.onStoriesUpdateAlbum(creatorCtx, invalidUpdateReq); err == nil || !tgerr.Is(err, "STORY_ID_INVALID") {
		t.Fatalf("invalid add stories update album err = %v, want STORY_ID_INVALID", err)
	}
	invalidAlbumUpdateReq := &tg.StoriesUpdateAlbumRequest{
		Peer:    &tg.InputPeerEmpty{},
		AlbumID: 0,
	}
	if _, err := r.onStoriesUpdateAlbum(creatorCtx, invalidAlbumUpdateReq); err == nil || !tgerr.Is(err, "INPUT_REQUEST_INVALID") {
		t.Fatalf("invalid update album id before peer err = %v, want INPUT_REQUEST_INVALID", err)
	}
	emptyUpdateReq := &tg.StoriesUpdateAlbumRequest{
		Peer:    &tg.InputPeerEmpty{},
		AlbumID: 1,
	}
	if _, err := r.onStoriesUpdateAlbum(creatorCtx, emptyUpdateReq); err == nil || !tgerr.Is(err, "INPUT_REQUEST_INVALID") {
		t.Fatalf("empty update album mutation before peer err = %v, want INPUT_REQUEST_INVALID", err)
	}
	emptyUpdateVectors := []struct {
		name string
		set  func(*tg.StoriesUpdateAlbumRequest)
	}{
		{name: "add stories", set: func(req *tg.StoriesUpdateAlbumRequest) { req.SetAddStories([]int{}) }},
		{name: "delete stories", set: func(req *tg.StoriesUpdateAlbumRequest) { req.SetDeleteStories([]int{}) }},
		{name: "order stories", set: func(req *tg.StoriesUpdateAlbumRequest) { req.SetOrder([]int{}) }},
	}
	for _, tc := range emptyUpdateVectors {
		req := &tg.StoriesUpdateAlbumRequest{
			Peer:    &tg.InputPeerEmpty{},
			AlbumID: 1,
		}
		tc.set(req)
		if _, err := r.onStoriesUpdateAlbum(creatorCtx, req); err == nil || !tgerr.Is(err, "INPUT_REQUEST_INVALID") {
			t.Fatalf("empty update album %s vector before peer err = %v, want INPUT_REQUEST_INVALID", tc.name, err)
		}
	}
	longTitleUpdateReq := &tg.StoriesUpdateAlbumRequest{
		Peer:    &tg.InputPeerEmpty{},
		AlbumID: 1,
	}
	longTitleUpdateReq.SetTitle(strings.Repeat("x", maxStoryAlbumTitleLength+1))
	if _, err := r.onStoriesUpdateAlbum(creatorCtx, longTitleUpdateReq); err == nil || !tgerr.Is(err, "LIMIT_INVALID") {
		t.Fatalf("long title update before peer err = %v, want LIMIT_INVALID", err)
	}
	duplicateAddReq := &tg.StoriesUpdateAlbumRequest{
		Peer:    &tg.InputPeerSelf{},
		AlbumID: 1,
	}
	duplicateAddReq.SetAddStories([]int{1, 1})
	if _, err := r.onStoriesUpdateAlbum(creatorCtx, duplicateAddReq); err == nil || !tgerr.Is(err, "INPUT_REQUEST_INVALID") {
		t.Fatalf("duplicate add stories update album err = %v, want INPUT_REQUEST_INVALID", err)
	}
	duplicateAddBeforePeerReq := &tg.StoriesUpdateAlbumRequest{
		Peer:    &tg.InputPeerEmpty{},
		AlbumID: 1,
	}
	duplicateAddBeforePeerReq.SetAddStories([]int{1, 1})
	if _, err := r.onStoriesUpdateAlbum(creatorCtx, duplicateAddBeforePeerReq); err == nil || !tgerr.Is(err, "INPUT_REQUEST_INVALID") {
		t.Fatalf("duplicate add stories update before peer err = %v, want INPUT_REQUEST_INVALID", err)
	}
	duplicateDeleteReq := &tg.StoriesUpdateAlbumRequest{
		Peer:    &tg.InputPeerSelf{},
		AlbumID: 1,
	}
	duplicateDeleteReq.SetDeleteStories([]int{1, 1})
	if _, err := r.onStoriesUpdateAlbum(creatorCtx, duplicateDeleteReq); err == nil || !tgerr.Is(err, "INPUT_REQUEST_INVALID") {
		t.Fatalf("duplicate delete stories update album err = %v, want INPUT_REQUEST_INVALID", err)
	}
	duplicateOrderReq := &tg.StoriesUpdateAlbumRequest{
		Peer:    &tg.InputPeerSelf{},
		AlbumID: 1,
	}
	duplicateOrderReq.SetOrder([]int{1, 1})
	if _, err := r.onStoriesUpdateAlbum(creatorCtx, duplicateOrderReq); err == nil || !tgerr.Is(err, "INPUT_REQUEST_INVALID") {
		t.Fatalf("duplicate order stories update album err = %v, want INPUT_REQUEST_INVALID", err)
	}
	if _, err := r.onStoriesCreateAlbum(creatorCtx, &tg.StoriesCreateAlbumRequest{Peer: &tg.InputPeerSelf{}, Title: strings.Repeat("x", maxStoryAlbumTitleLength+1)}); err == nil || !tgerr.Is(err, "LIMIT_INVALID") {
		t.Fatalf("long title create err = %v, want LIMIT_INVALID", err)
	}
	if _, err := r.onStoriesDeleteAlbum(creatorCtx, &tg.StoriesDeleteAlbumRequest{Peer: &tg.InputPeerSelf{}, AlbumID: 0}); err == nil || !tgerr.Is(err, "INPUT_REQUEST_INVALID") {
		t.Fatalf("invalid album id err = %v, want INPUT_REQUEST_INVALID", err)
	}
}

func TestStoriesGetChatsToSendReturnsPostableChannels(t *testing.T) {
	ctx := context.Background()
	userID := int64(91001)
	creatorID := int64(91002)
	channelStore := memory.NewChannelStore()
	channelService := appchannels.NewService(channelStore)
	owned, err := channelService.CreateChannel(ctx, userID, domain.CreateChannelRequest{
		CreatorUserID: userID,
		Title:         "own story send-as",
		Broadcast:     true,
		Date:          1700003000,
	})
	if err != nil {
		t.Fatalf("create owned channel: %v", err)
	}
	postable, err := channelService.CreateChannel(ctx, creatorID, domain.CreateChannelRequest{
		CreatorUserID: creatorID,
		Title:         "admin story send-as",
		Broadcast:     true,
		MemberUserIDs: []int64{userID},
		Date:          1700003001,
	})
	if err != nil {
		t.Fatalf("create postable channel: %v", err)
	}
	if _, err := channelService.EditAdmin(ctx, creatorID, domain.EditChannelAdminRequest{
		ChannelID: postable.Channel.ID,
		MemberID:  userID,
		AdminRights: domain.ChannelAdminRights{
			PostStories: true,
		},
		Date: 1700003002,
	}); err != nil {
		t.Fatalf("grant post stories: %v", err)
	}
	editOnly, err := channelService.CreateChannel(ctx, creatorID, domain.CreateChannelRequest{
		CreatorUserID: creatorID,
		Title:         "edit-only story send-as",
		Broadcast:     true,
		MemberUserIDs: []int64{userID},
		Date:          1700003003,
	})
	if err != nil {
		t.Fatalf("create edit-only channel: %v", err)
	}
	if _, err := channelService.EditAdmin(ctx, creatorID, domain.EditChannelAdminRequest{
		ChannelID: editOnly.Channel.ID,
		MemberID:  userID,
		AdminRights: domain.ChannelAdminRights{
			EditStories: true,
		},
		Date: 1700003004,
	}); err != nil {
		t.Fatalf("grant edit stories only: %v", err)
	}
	memberOnly, err := channelService.CreateChannel(ctx, creatorID, domain.CreateChannelRequest{
		CreatorUserID: creatorID,
		Title:         "member-only story send-as",
		Broadcast:     true,
		MemberUserIDs: []int64{userID},
		Date:          1700003005,
	})
	if err != nil {
		t.Fatalf("create member-only channel: %v", err)
	}
	storyStore := memory.NewStoryStore()
	upsertStory := func(peer domain.Peer, id int) {
		t.Helper()
		if _, err := storyStore.UpsertStory(ctx, domain.UpsertStoryRequest{Story: domain.Story{
			Owner:      peer,
			ID:         id,
			Date:       1700003006,
			ExpireDate: 1700006600,
			Public:     true,
		}}); err != nil {
			t.Fatalf("upsert story %v/%d: %v", peer, id, err)
		}
	}
	upsertStory(domain.Peer{Type: domain.PeerTypeChannel, ID: postable.Channel.ID}, 31)
	upsertStory(domain.Peer{Type: domain.PeerTypeChannel, ID: owned.Channel.ID}, 37)

	r := New(Config{}, Deps{
		Channels: channelService,
		Stories:  appstories.NewService(storyStore),
	}, zaptest.NewLogger(t), fixedClock{now: time.Unix(1700003010, 0)})
	got, err := r.onStoriesGetChatsToSend(WithUserID(ctx, userID))
	if err != nil {
		t.Fatalf("get chats to send: %v", err)
	}
	chats, ok := got.(*tg.MessagesChats)
	if !ok {
		t.Fatalf("get chats to send = %T, want messages.chats", got)
	}
	if len(chats.Chats) != 2 {
		t.Fatalf("send-as chats = %+v, want two channels; excluded edit-only=%d member-only=%d", chats.Chats, editOnly.Channel.ID, memberOnly.Channel.ID)
	}
	first, ok := chats.Chats[0].(*tg.Channel)
	if !ok {
		t.Fatalf("first send-as chat = %T %+v, want channel", chats.Chats[0], chats.Chats[0])
	}
	firstRights, firstHasRights := first.GetAdminRights()
	if first.ID != postable.Channel.ID || first.Min || !firstHasRights || !firstRights.PostStories {
		t.Fatalf("first send-as chat = %T %+v, want full post_stories admin channel", chats.Chats[0], chats.Chats[0])
	}
	assertChannelStoryMaxID(t, first, 31)
	second, ok := chats.Chats[1].(*tg.Channel)
	if !ok {
		t.Fatalf("second send-as chat = %T %+v, want channel", chats.Chats[1], chats.Chats[1])
	}
	if second.ID != owned.Channel.ID || second.Min || !second.Creator {
		t.Fatalf("second send-as chat = %T %+v, want full creator channel", chats.Chats[1], chats.Chats[1])
	}
	assertChannelStoryMaxID(t, second, 37)
}

func assertStoryReactionListViewer(t *testing.T, list *tg.StoriesStoryReactionsList, wantUserID int64) {
	t.Helper()
	if list.Count != 1 || len(list.Reactions) != 1 {
		t.Fatalf("reactions list = %+v, want one reaction", list)
	}
	storyReaction, ok := list.Reactions[0].(*tg.StoryReaction)
	if !ok {
		t.Fatalf("story reaction = %T, want *tg.StoryReaction", list.Reactions[0])
	}
	peer, ok := storyReaction.PeerID.(*tg.PeerUser)
	if !ok || peer.UserID != wantUserID {
		t.Fatalf("reaction peer = %T %+v, want user %d", storyReaction.PeerID, storyReaction.PeerID, wantUserID)
	}
	if reaction, ok := storyReaction.Reaction.(*tg.ReactionEmoji); !ok || reaction.Emoticon != "🔥" {
		t.Fatalf("reaction = %T %+v, want fire emoji", storyReaction.Reaction, storyReaction.Reaction)
	}
	if findUserClass(list.Users, wantUserID) == nil {
		t.Fatalf("reaction users = %+v, want user %d", list.Users, wantUserID)
	}
}

func assertStoryReactionListCustomViewer(t *testing.T, list *tg.StoriesStoryReactionsList, wantUserID, wantDocumentID int64) {
	t.Helper()
	if list.Count != 1 || len(list.Reactions) != 1 {
		t.Fatalf("reactions list = %+v, want one reaction", list)
	}
	storyReaction, ok := list.Reactions[0].(*tg.StoryReaction)
	if !ok {
		t.Fatalf("story reaction = %T, want *tg.StoryReaction", list.Reactions[0])
	}
	peer, ok := storyReaction.PeerID.(*tg.PeerUser)
	if !ok || peer.UserID != wantUserID {
		t.Fatalf("reaction peer = %T %+v, want user %d", storyReaction.PeerID, storyReaction.PeerID, wantUserID)
	}
	reaction, ok := storyReaction.Reaction.(*tg.ReactionCustomEmoji)
	if !ok || reaction.DocumentID != wantDocumentID {
		t.Fatalf("reaction = %T %+v, want custom emoji %d", storyReaction.Reaction, storyReaction.Reaction, wantDocumentID)
	}
	if findUserClass(list.Users, wantUserID) == nil {
		t.Fatalf("reaction users = %+v, want user %d", list.Users, wantUserID)
	}
}

func assertStoryViewsListViewer(t *testing.T, list *tg.StoriesStoryViewsList, wantUserID int64) {
	t.Helper()
	if list.Count != 1 || list.ViewsCount != 1 || len(list.Views) != 1 {
		t.Fatalf("views list = %+v, want one view", list)
	}
	view, ok := list.Views[0].(*tg.StoryView)
	if !ok {
		t.Fatalf("story view = %T, want *tg.StoryView", list.Views[0])
	}
	if view.UserID != wantUserID {
		t.Fatalf("story view user = %d, want %d", view.UserID, wantUserID)
	}
	if findUserClass(list.Users, wantUserID) == nil {
		t.Fatalf("view users = %+v, want user %d", list.Users, wantUserID)
	}
}

func sameIntIDs(got, want []int) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func TestStoryDifferenceProjectsCompanionStoriesMaxID(t *testing.T) {
	ctx := context.Background()
	authKeyID := [8]byte{4, 5, 6}
	userStore := memory.NewUserStore()
	user, err := userStore.Create(ctx, domain.User{AccessHash: 91501, Phone: "15550091501", FirstName: "Author"})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	storyStore := memory.NewStoryStore()
	updateStore := memory.NewUpdateEventStore()
	now := time.Unix(1700000500, 0)
	r := New(Config{}, Deps{
		Users:   appusers.NewService(userStore),
		Stories: appstories.NewService(storyStore),
		Updates: appupdates.NewService(memory.NewUpdateStateStore(), updateStore),
		Files:   &fakeFiles{photos: map[int64]domain.Photo{}},
	}, zaptest.NewLogger(t), fixedClock{now: now})
	reqCtx := WithSessionID(WithAuthKeyID(WithUserID(ctx, user.ID), authKeyID), 91)

	if _, err := r.onStoriesSendStory(reqCtx, &tg.StoriesSendStoryRequest{
		Peer:         &tg.InputPeerSelf{},
		Media:        &tg.InputMediaUploadedPhoto{File: &tg.InputFile{ID: 44, Parts: 1, Name: "story.jpg"}},
		PrivacyRules: []tg.InputPrivacyRuleClass{&tg.InputPrivacyValueAllowAll{}},
		RandomID:     7101,
		Period:       86400,
		Caption:      "diff story",
	}); err != nil {
		t.Fatalf("send story: %v", err)
	}
	diff, err := r.onUpdatesGetDifference(reqCtx, &tg.UpdatesGetDifferenceRequest{Pts: 0})
	if err != nil {
		t.Fatalf("get difference: %v", err)
	}
	got, ok := diff.(*tg.UpdatesDifference)
	if !ok {
		t.Fatalf("difference = %T, want updates.difference", diff)
	}
	if got.State.Pts != 1 || len(got.OtherUpdates) != 1 {
		t.Fatalf("difference pts/updates = %d/%d, want 1/1", got.State.Pts, len(got.OtherUpdates))
	}
	assertUserStoryMaxID(t, findUserClass(got.Users, user.ID), 1)
}

func TestStoriesSendStoryRepostRecordsForwardHeaderAndDifference(t *testing.T) {
	ctx := context.Background()
	bobAuthKeyID := [8]byte{4, 6, 8}
	userStore := memory.NewUserStore()
	alice, err := userStore.Create(ctx, domain.User{AccessHash: 91601, Phone: "15550091601", FirstName: "Alice"})
	if err != nil {
		t.Fatalf("create alice: %v", err)
	}
	bob, err := userStore.Create(ctx, domain.User{AccessHash: 91602, Phone: "15550091602", FirstName: "Bob"})
	if err != nil {
		t.Fatalf("create bob: %v", err)
	}
	storyStore := memory.NewStoryStore()
	updateStore := memory.NewUpdateEventStore()
	now := time.Unix(1700000501, 0)
	r := New(Config{}, Deps{
		Users:   appusers.NewService(userStore),
		Stories: appstories.NewService(storyStore),
		Updates: appupdates.NewService(memory.NewUpdateStateStore(), updateStore),
		Files:   &fakeFiles{photos: map[int64]domain.Photo{}},
	}, zaptest.NewLogger(t), fixedClock{now: now})
	aliceCtx := WithSessionID(WithAuthKeyID(WithUserID(ctx, alice.ID), [8]byte{4, 6, 7}), 61)
	bobCtx := WithSessionID(WithAuthKeyID(WithUserID(ctx, bob.ID), bobAuthKeyID), 62)

	sourceUpdatesClass, err := r.onStoriesSendStory(aliceCtx, &tg.StoriesSendStoryRequest{
		Peer:         &tg.InputPeerSelf{},
		Media:        &tg.InputMediaUploadedPhoto{File: &tg.InputFile{ID: 91601, Parts: 1, Name: "alice-source.jpg"}},
		PrivacyRules: []tg.InputPrivacyRuleClass{&tg.InputPrivacyValueAllowAll{}},
		RandomID:     91601,
		Period:       86400,
		Caption:      "source story",
	})
	if err != nil {
		t.Fatalf("send source story: %v", err)
	}
	sourceUpdates := sourceUpdatesClass.(*tg.Updates)
	sourceID := sourceUpdates.Updates[0].(*tg.UpdateStoryID).ID

	repostReq := &tg.StoriesSendStoryRequest{
		Peer:         &tg.InputPeerSelf{},
		Media:        &tg.InputMediaUploadedPhoto{File: &tg.InputFile{ID: 91602, Parts: 1, Name: "bob-repost.jpg"}},
		PrivacyRules: []tg.InputPrivacyRuleClass{&tg.InputPrivacyValueAllowAll{}},
		RandomID:     91602,
		Period:       86400,
		Caption:      "bob repost",
		FwdModified:  true,
	}
	repostReq.SetFwdFromID(&tg.InputPeerUser{UserID: alice.ID, AccessHash: alice.AccessHash})
	repostReq.SetFwdFromStory(sourceID)
	repostUpdatesClass, err := r.onStoriesSendStory(bobCtx, repostReq)
	if err != nil {
		t.Fatalf("send repost story: %v", err)
	}
	repostUpdates, ok := repostUpdatesClass.(*tg.Updates)
	if !ok || len(repostUpdates.Updates) != 2 {
		t.Fatalf("repost updates = %T %+v, want updateStoryID + updateStory", repostUpdatesClass, repostUpdatesClass)
	}
	repostID := repostUpdates.Updates[0].(*tg.UpdateStoryID).ID
	repostItem, ok := repostUpdates.Updates[1].(*tg.UpdateStory).Story.(*tg.StoryItem)
	if !ok {
		t.Fatalf("repost story update = %T, want storyItem", repostUpdates.Updates[1].(*tg.UpdateStory).Story)
	}
	assertStoryForwardHeader(t, repostItem, alice.ID, sourceID, true)
	if findUserClass(repostUpdates.Users, alice.ID) == nil || findUserClass(repostUpdates.Users, bob.ID) == nil {
		t.Fatalf("repost companion users = %+v, want source and repost owner users", repostUpdates.Users)
	}

	peerStories, err := r.onStoriesGetPeerStories(bobCtx, &tg.InputPeerSelf{})
	if err != nil {
		t.Fatalf("get bob peer stories: %v", err)
	}
	if len(peerStories.Stories.Stories) != 1 {
		t.Fatalf("bob peer stories = %+v, want one repost", peerStories.Stories.Stories)
	}
	assertStoryForwardHeader(t, peerStories.Stories.Stories[0].(*tg.StoryItem), alice.ID, sourceID, true)
	if findUserClass(peerStories.Users, alice.ID) == nil {
		t.Fatalf("peer stories users = %+v, want source user", peerStories.Users)
	}

	byID, err := r.onStoriesGetStoriesByID(bobCtx, &tg.StoriesGetStoriesByIDRequest{
		Peer: &tg.InputPeerSelf{},
		ID:   []int{repostID},
	})
	if err != nil {
		t.Fatalf("get repost by id: %v", err)
	}
	if len(byID.Stories) != 1 {
		t.Fatalf("stories by id = %+v, want repost", byID.Stories)
	}
	assertStoryForwardHeader(t, byID.Stories[0].(*tg.StoryItem), alice.ID, sourceID, true)
	if findUserClass(byID.Users, alice.ID) == nil {
		t.Fatalf("stories by id users = %+v, want source user", byID.Users)
	}

	diff, err := r.onUpdatesGetDifference(bobCtx, &tg.UpdatesGetDifferenceRequest{Pts: 0})
	if err != nil {
		t.Fatalf("get bob difference: %v", err)
	}
	gotDiff, ok := diff.(*tg.UpdatesDifference)
	if !ok || gotDiff.State.Pts != 1 || len(gotDiff.OtherUpdates) != 1 {
		t.Fatalf("bob difference = %T %+v, want one repost update", diff, diff)
	}
	diffItem, ok := gotDiff.OtherUpdates[0].(*tg.UpdateStory).Story.(*tg.StoryItem)
	if !ok {
		t.Fatalf("difference story = %T, want storyItem", gotDiff.OtherUpdates[0].(*tg.UpdateStory).Story)
	}
	assertStoryForwardHeader(t, diffItem, alice.ID, sourceID, true)
	if findUserClass(gotDiff.Users, alice.ID) == nil || findUserClass(gotDiff.Users, bob.ID) == nil {
		t.Fatalf("difference users = %+v, want source and repost owner users", gotDiff.Users)
	}

	sourceViews, err := r.onStoriesGetStoriesViews(aliceCtx, &tg.StoriesGetStoriesViewsRequest{
		Peer: &tg.InputPeerSelf{},
		ID:   []int{sourceID},
	})
	if err != nil {
		t.Fatalf("get source story views: %v", err)
	}
	if len(sourceViews.Views) != 1 || sourceViews.Views[0].ForwardsCount != 1 {
		t.Fatalf("source story views = %+v, want forwards_count=1", sourceViews.Views)
	}
	sourceViewList, err := r.onStoriesGetStoryViewsList(aliceCtx, &tg.StoriesGetStoryViewsListRequest{
		Peer:   &tg.InputPeerSelf{},
		ID:     sourceID,
		Offset: "",
		Limit:  20,
	})
	if err != nil {
		t.Fatalf("get source story views list: %v", err)
	}
	if sourceViewList.Count != 1 || sourceViewList.ForwardsCount != 1 || len(sourceViewList.Views) != 1 {
		t.Fatalf("source story views list = %+v, want one public repost", sourceViewList)
	}
	publicRepost, ok := sourceViewList.Views[0].(*tg.StoryViewPublicRepost)
	if !ok {
		t.Fatalf("source story view = %T, want storyViewPublicRepost", sourceViewList.Views[0])
	}
	repostPeer, ok := publicRepost.PeerID.(*tg.PeerUser)
	if !ok || repostPeer.UserID != bob.ID {
		t.Fatalf("public repost peer = %T %+v, want Bob peer", publicRepost.PeerID, publicRepost.PeerID)
	}
	repostStory, ok := publicRepost.Story.(*tg.StoryItem)
	if !ok || repostStory.ID != repostID {
		t.Fatalf("public repost story = %T %+v, want repost story id %d", publicRepost.Story, publicRepost.Story, repostID)
	}
	assertStoryForwardHeader(t, repostStory, alice.ID, sourceID, true)
	if findUserClass(sourceViewList.Users, alice.ID) == nil || findUserClass(sourceViewList.Users, bob.ID) == nil {
		t.Fatalf("source story views list users = %+v, want source and repost users", sourceViewList.Users)
	}

	protectedSourceClass, err := r.onStoriesSendStory(aliceCtx, &tg.StoriesSendStoryRequest{
		Peer:         &tg.InputPeerSelf{},
		Media:        &tg.InputMediaUploadedPhoto{File: &tg.InputFile{ID: 91603, Parts: 1, Name: "alice-protected.jpg"}},
		PrivacyRules: []tg.InputPrivacyRuleClass{&tg.InputPrivacyValueAllowAll{}},
		RandomID:     91603,
		Period:       86400,
		Caption:      "protected source",
		Noforwards:   true,
	})
	if err != nil {
		t.Fatalf("send protected source story: %v", err)
	}
	protectedID := protectedSourceClass.(*tg.Updates).Updates[0].(*tg.UpdateStoryID).ID
	protectedRepost := &tg.StoriesSendStoryRequest{
		Peer:         &tg.InputPeerSelf{},
		Media:        &tg.InputMediaUploadedPhoto{File: &tg.InputFile{ID: 91604, Parts: 1, Name: "blocked-repost.jpg"}},
		PrivacyRules: []tg.InputPrivacyRuleClass{&tg.InputPrivacyValueAllowAll{}},
		RandomID:     91604,
		Period:       86400,
	}
	protectedRepost.SetFwdFromID(&tg.InputPeerUser{UserID: alice.ID, AccessHash: alice.AccessHash})
	protectedRepost.SetFwdFromStory(protectedID)
	if _, err := r.onStoriesSendStory(bobCtx, protectedRepost); err == nil || !tgerr.Is(err, "CHAT_FORWARDS_RESTRICTED") {
		t.Fatalf("protected repost err = %v, want CHAT_FORWARDS_RESTRICTED", err)
	}
	peerStories, err = r.onStoriesGetPeerStories(bobCtx, &tg.InputPeerSelf{})
	if err != nil {
		t.Fatalf("get bob peer stories after protected repost: %v", err)
	}
	if len(peerStories.Stories.Stories) != 1 {
		t.Fatalf("bob peer stories after protected repost = %+v, want only first repost", peerStories.Stories.Stories)
	}

	if _, err := r.onStoriesDeleteStories(bobCtx, &tg.StoriesDeleteStoriesRequest{
		Peer: &tg.InputPeerSelf{},
		ID:   []int{repostID},
	}); err != nil {
		t.Fatalf("delete repost story: %v", err)
	}
	sourceViews, err = r.onStoriesGetStoriesViews(aliceCtx, &tg.StoriesGetStoriesViewsRequest{
		Peer: &tg.InputPeerSelf{},
		ID:   []int{sourceID},
	})
	if err != nil {
		t.Fatalf("get source story views after delete: %v", err)
	}
	if len(sourceViews.Views) != 1 || sourceViews.Views[0].ForwardsCount != 0 {
		t.Fatalf("source story views after delete = %+v, want forwards_count=0", sourceViews.Views)
	}
}

func TestStoriesSendStoryRepostRespectsForwardPrivacyFromName(t *testing.T) {
	ctx := context.Background()
	bobAuthKeyID := [8]byte{4, 6, 9}
	userStore := memory.NewUserStore()
	alice, err := userStore.Create(ctx, domain.User{AccessHash: 91611, Phone: "15550091611", FirstName: "Alice", LastName: "Hidden"})
	if err != nil {
		t.Fatalf("create alice: %v", err)
	}
	bob, err := userStore.Create(ctx, domain.User{AccessHash: 91612, Phone: "15550091612", FirstName: "Bob"})
	if err != nil {
		t.Fatalf("create bob: %v", err)
	}
	privacyStore := memory.NewPrivacyStore()
	privacyService := appprivacy.NewService(privacyStore, nil)
	if _, err := privacyService.SetRules(ctx, alice.ID, domain.PrivacyKeyForwards, []domain.PrivacyRule{{Kind: domain.PrivacyRuleDisallowAll}}); err != nil {
		t.Fatalf("set alice forwards privacy: %v", err)
	}
	storyStore := memory.NewStoryStore()
	updateStore := memory.NewUpdateEventStore()
	now := time.Unix(1700000521, 0)
	r := New(Config{}, Deps{
		Users:   appusers.NewService(userStore),
		Privacy: privacyService,
		Stories: appstories.NewService(storyStore),
		Updates: appupdates.NewService(memory.NewUpdateStateStore(), updateStore),
		Files:   &fakeFiles{photos: map[int64]domain.Photo{}},
	}, zaptest.NewLogger(t), fixedClock{now: now})
	aliceCtx := WithSessionID(WithAuthKeyID(WithUserID(ctx, alice.ID), [8]byte{4, 6, 10}), 63)
	bobCtx := WithSessionID(WithAuthKeyID(WithUserID(ctx, bob.ID), bobAuthKeyID), 64)

	sourceUpdatesClass, err := r.onStoriesSendStory(aliceCtx, &tg.StoriesSendStoryRequest{
		Peer:         &tg.InputPeerSelf{},
		Media:        &tg.InputMediaUploadedPhoto{File: &tg.InputFile{ID: 91611, Parts: 1, Name: "alice-hidden-source.jpg"}},
		PrivacyRules: []tg.InputPrivacyRuleClass{&tg.InputPrivacyValueAllowAll{}},
		RandomID:     91611,
		Period:       86400,
		Caption:      "source story",
	})
	if err != nil {
		t.Fatalf("send source story: %v", err)
	}
	sourceID := sourceUpdatesClass.(*tg.Updates).Updates[0].(*tg.UpdateStoryID).ID

	repostReq := &tg.StoriesSendStoryRequest{
		Peer:         &tg.InputPeerSelf{},
		Media:        &tg.InputMediaUploadedPhoto{File: &tg.InputFile{ID: 91612, Parts: 1, Name: "bob-hidden-repost.jpg"}},
		PrivacyRules: []tg.InputPrivacyRuleClass{&tg.InputPrivacyValueAllowAll{}},
		RandomID:     91612,
		Period:       86400,
		Caption:      "bob repost hidden source",
		FwdModified:  true,
	}
	repostReq.SetFwdFromID(&tg.InputPeerUser{UserID: alice.ID, AccessHash: alice.AccessHash})
	repostReq.SetFwdFromStory(sourceID)
	repostUpdatesClass, err := r.onStoriesSendStory(bobCtx, repostReq)
	if err != nil {
		t.Fatalf("send repost story: %v", err)
	}
	repostUpdates, ok := repostUpdatesClass.(*tg.Updates)
	if !ok || len(repostUpdates.Updates) != 2 {
		t.Fatalf("repost updates = %T %+v, want updateStoryID + updateStory", repostUpdatesClass, repostUpdatesClass)
	}
	repostID := repostUpdates.Updates[0].(*tg.UpdateStoryID).ID
	repostItem, ok := repostUpdates.Updates[1].(*tg.UpdateStory).Story.(*tg.StoryItem)
	if !ok {
		t.Fatalf("repost story update = %T, want storyItem", repostUpdates.Updates[1].(*tg.UpdateStory).Story)
	}
	assertStoryForwardNameHeader(t, repostItem, "Alice Hidden", sourceID, true)
	if findUserClass(repostUpdates.Users, alice.ID) != nil {
		t.Fatalf("repost companion users = %+v, want no hidden source user", repostUpdates.Users)
	}
	if findUserClass(repostUpdates.Users, bob.ID) == nil {
		t.Fatalf("repost companion users = %+v, want repost owner user", repostUpdates.Users)
	}

	peerStories, err := r.onStoriesGetPeerStories(bobCtx, &tg.InputPeerSelf{})
	if err != nil {
		t.Fatalf("get bob peer stories: %v", err)
	}
	if len(peerStories.Stories.Stories) != 1 {
		t.Fatalf("bob peer stories = %+v, want one repost", peerStories.Stories.Stories)
	}
	assertStoryForwardNameHeader(t, peerStories.Stories.Stories[0].(*tg.StoryItem), "Alice Hidden", sourceID, true)
	if findUserClass(peerStories.Users, alice.ID) != nil {
		t.Fatalf("peer stories users = %+v, want no hidden source user", peerStories.Users)
	}

	byID, err := r.onStoriesGetStoriesByID(bobCtx, &tg.StoriesGetStoriesByIDRequest{
		Peer: &tg.InputPeerSelf{},
		ID:   []int{repostID},
	})
	if err != nil {
		t.Fatalf("get repost by id: %v", err)
	}
	if len(byID.Stories) != 1 {
		t.Fatalf("stories by id = %+v, want repost", byID.Stories)
	}
	assertStoryForwardNameHeader(t, byID.Stories[0].(*tg.StoryItem), "Alice Hidden", sourceID, true)
	if findUserClass(byID.Users, alice.ID) != nil {
		t.Fatalf("stories by id users = %+v, want no hidden source user", byID.Users)
	}

	diff, err := r.onUpdatesGetDifference(bobCtx, &tg.UpdatesGetDifferenceRequest{Pts: 0})
	if err != nil {
		t.Fatalf("get bob difference: %v", err)
	}
	gotDiff, ok := diff.(*tg.UpdatesDifference)
	if !ok || gotDiff.State.Pts != 1 || len(gotDiff.OtherUpdates) != 1 {
		t.Fatalf("bob difference = %T %+v, want one repost update", diff, diff)
	}
	diffItem, ok := gotDiff.OtherUpdates[0].(*tg.UpdateStory).Story.(*tg.StoryItem)
	if !ok {
		t.Fatalf("difference story = %T, want storyItem", gotDiff.OtherUpdates[0].(*tg.UpdateStory).Story)
	}
	assertStoryForwardNameHeader(t, diffItem, "Alice Hidden", sourceID, true)
	if findUserClass(gotDiff.Users, alice.ID) != nil {
		t.Fatalf("difference users = %+v, want no hidden source user", gotDiff.Users)
	}

	sourceViewList, err := r.onStoriesGetStoryViewsList(aliceCtx, &tg.StoriesGetStoryViewsListRequest{
		Peer:  &tg.InputPeerSelf{},
		ID:    sourceID,
		Limit: 20,
	})
	if err != nil {
		t.Fatalf("get source story views list: %v", err)
	}
	if sourceViewList.Count != 1 || sourceViewList.ForwardsCount != 1 || len(sourceViewList.Views) != 1 {
		t.Fatalf("source story views list = %+v, want one public repost", sourceViewList)
	}
	publicRepost, ok := sourceViewList.Views[0].(*tg.StoryViewPublicRepost)
	if !ok {
		t.Fatalf("source story view = %T, want storyViewPublicRepost", sourceViewList.Views[0])
	}
	if peer, ok := publicRepost.PeerID.(*tg.PeerUser); !ok || peer.UserID != bob.ID {
		t.Fatalf("public repost peer = %T %+v, want Bob peer", publicRepost.PeerID, publicRepost.PeerID)
	}
	sourceViewRepost, ok := publicRepost.Story.(*tg.StoryItem)
	if !ok || sourceViewRepost.ID != repostID {
		t.Fatalf("public repost story = %T %+v, want repost story id %d", publicRepost.Story, publicRepost.Story, repostID)
	}
	assertStoryForwardNameHeader(t, sourceViewRepost, "Alice Hidden", sourceID, true)
	if findUserClass(sourceViewList.Users, alice.ID) != nil {
		t.Fatalf("source story views list users = %+v, want no hidden source user", sourceViewList.Users)
	}
	if findUserClass(sourceViewList.Users, bob.ID) == nil {
		t.Fatalf("source story views list users = %+v, want repost owner user", sourceViewList.Users)
	}

	publicForwards, err := r.onStatsGetStoryPublicForwards(aliceCtx, &tg.StatsGetStoryPublicForwardsRequest{
		Peer:  &tg.InputPeerSelf{},
		ID:    sourceID,
		Limit: 20,
	})
	if err != nil {
		t.Fatalf("get source story public forwards: %v", err)
	}
	if publicForwards.Count != 1 || len(publicForwards.Forwards) != 1 {
		t.Fatalf("source story public forwards = %+v, want one public repost", publicForwards)
	}
	forwardStory, ok := publicForwards.Forwards[0].(*tg.PublicForwardStory)
	if !ok {
		t.Fatalf("public forward = %T, want *tg.PublicForwardStory", publicForwards.Forwards[0])
	}
	if peer, ok := forwardStory.Peer.(*tg.PeerUser); !ok || peer.UserID != bob.ID {
		t.Fatalf("public forward peer = %T %+v, want Bob peer", forwardStory.Peer, forwardStory.Peer)
	}
	statsRepost, ok := forwardStory.Story.(*tg.StoryItem)
	if !ok || statsRepost.ID != repostID {
		t.Fatalf("public forward story = %T %+v, want repost story id %d", forwardStory.Story, forwardStory.Story, repostID)
	}
	assertStoryForwardNameHeader(t, statsRepost, "Alice Hidden", sourceID, true)
	if findUserClass(publicForwards.Users, alice.ID) != nil {
		t.Fatalf("public forwards users = %+v, want no hidden source user", publicForwards.Users)
	}
	if findUserClass(publicForwards.Users, bob.ID) == nil {
		t.Fatalf("public forwards users = %+v, want repost owner user", publicForwards.Users)
	}
}

func TestStoriesSendEditDeleteRecordsStoryUpdates(t *testing.T) {
	ctx := context.Background()
	authKeyID := [8]byte{9, 8, 7}
	userStore := memory.NewUserStore()
	user, err := userStore.Create(ctx, domain.User{AccessHash: 92001, Phone: "15550092001", FirstName: "Author"})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	userID := user.ID
	storyStore := memory.NewStoryStore()
	updateStore := memory.NewUpdateEventStore()
	now := time.Unix(1700000500, 0)
	r := New(Config{}, Deps{
		Users:   appusers.NewService(userStore),
		Stories: appstories.NewService(storyStore),
		Updates: appupdates.NewService(memory.NewUpdateStateStore(), updateStore),
		Files:   &fakeFiles{photos: map[int64]domain.Photo{}},
	}, zaptest.NewLogger(t), fixedClock{now: now})
	reqCtx := WithSessionID(WithAuthKeyID(WithUserID(ctx, userID), authKeyID), 99)

	updates, err := r.onStoriesSendStory(reqCtx, &tg.StoriesSendStoryRequest{
		Peer:         &tg.InputPeerSelf{},
		Media:        &tg.InputMediaUploadedPhoto{File: &tg.InputFile{ID: 42, Parts: 1, Name: "story.jpg"}},
		PrivacyRules: []tg.InputPrivacyRuleClass{&tg.InputPrivacyValueAllowAll{}},
		RandomID:     7001,
		Period:       86400,
		Caption:      "first story",
		Pinned:       true,
	})
	if err != nil {
		t.Fatalf("send story: %v", err)
	}
	sendUpdates, ok := updates.(*tg.Updates)
	if !ok || len(sendUpdates.Updates) != 2 {
		t.Fatalf("send updates = %T %+v, want updateStoryID + updateStory", updates, updates)
	}
	idUpdate, ok := sendUpdates.Updates[0].(*tg.UpdateStoryID)
	if !ok || idUpdate.ID != 1 || idUpdate.RandomID != 7001 {
		t.Fatalf("story id update = %T %+v, want id 1 random 7001", sendUpdates.Updates[0], sendUpdates.Updates[0])
	}
	storyUpdate, ok := sendUpdates.Updates[1].(*tg.UpdateStory)
	if !ok {
		t.Fatalf("second update = %T, want updateStory", sendUpdates.Updates[1])
	}
	item, ok := storyUpdate.Story.(*tg.StoryItem)
	if !ok || item.ID != 1 || item.Caption != "first story" || !item.Pinned || !item.Public {
		t.Fatalf("sent story item = %T %+v, want pinned public story", storyUpdate.Story, storyUpdate.Story)
	}
	media, ok := item.Media.(*tg.MessageMediaPhoto)
	if !ok {
		t.Fatalf("story media = %T, want photo", item.Media)
	}
	if photo, ok := media.Photo.(*tg.Photo); !ok || photo.ID != 777 {
		t.Fatalf("story photo = %T %+v, want fake photo id 777", media.Photo, media.Photo)
	}
	assertUserStoryMaxID(t, findUserClass(sendUpdates.Users, userID), 1)

	edit := &tg.StoriesEditStoryRequest{Peer: &tg.InputPeerSelf{}, ID: idUpdate.ID}
	edit.SetCaption("edited story")
	if updates, err = r.onStoriesEditStory(reqCtx, edit); err != nil {
		t.Fatalf("edit story: %v", err)
	}
	editUpdates := updates.(*tg.Updates)
	edited, ok := editUpdates.Updates[0].(*tg.UpdateStory)
	if !ok {
		t.Fatalf("edit update = %T, want updateStory", editUpdates.Updates[0])
	}
	editedItem, ok := edited.Story.(*tg.StoryItem)
	if !ok || !editedItem.Edited || editedItem.Caption != "edited story" {
		t.Fatalf("edited story = %T %+v, want edited caption", edited.Story, edited.Story)
	}

	pinnedIDs, err := r.onStoriesTogglePinned(reqCtx, &tg.StoriesTogglePinnedRequest{
		Peer:   &tg.InputPeerSelf{},
		ID:     []int{idUpdate.ID, idUpdate.ID},
		Pinned: false,
	})
	if err != nil {
		t.Fatalf("toggle pinned with duplicate ids: %v", err)
	}
	if len(pinnedIDs) != 1 || pinnedIDs[0] != idUpdate.ID {
		t.Fatalf("pinned ids = %v, want deduped story id", pinnedIDs)
	}
	retryPinnedIDs, err := r.onStoriesTogglePinned(reqCtx, &tg.StoriesTogglePinnedRequest{
		Peer:   &tg.InputPeerSelf{},
		ID:     []int{idUpdate.ID},
		Pinned: false,
	})
	if err != nil {
		t.Fatalf("retry toggle pinned: %v", err)
	}
	if len(retryPinnedIDs) != 1 || retryPinnedIDs[0] != idUpdate.ID {
		t.Fatalf("retry pinned ids = %v, want story id echo", retryPinnedIDs)
	}

	deletedIDs, err := r.onStoriesDeleteStories(reqCtx, &tg.StoriesDeleteStoriesRequest{
		Peer: &tg.InputPeerSelf{},
		ID:   []int{idUpdate.ID, idUpdate.ID},
	})
	if err != nil {
		t.Fatalf("delete story with duplicate ids: %v", err)
	}
	if len(deletedIDs) != 1 || deletedIDs[0] != idUpdate.ID {
		t.Fatalf("deleted ids = %v, want deduped story id", deletedIDs)
	}
	retryDeletedIDs, err := r.onStoriesDeleteStories(reqCtx, &tg.StoriesDeleteStoriesRequest{
		Peer: &tg.InputPeerSelf{},
		ID:   []int{idUpdate.ID},
	})
	if err != nil {
		t.Fatalf("retry delete story: %v", err)
	}
	if len(retryDeletedIDs) != 1 || retryDeletedIDs[0] != idUpdate.ID {
		t.Fatalf("retry deleted ids = %v, want story id echo", retryDeletedIDs)
	}
	diff, err := r.onUpdatesGetDifference(reqCtx, &tg.UpdatesGetDifferenceRequest{Pts: 0})
	if err != nil {
		t.Fatalf("get difference: %v", err)
	}
	got, ok := diff.(*tg.UpdatesDifference)
	if !ok {
		t.Fatalf("difference = %T, want updates.difference", diff)
	}
	if got.State.Pts != 4 || len(got.OtherUpdates) != 4 {
		t.Fatalf("difference pts/updates = %d/%d, want 4/4", got.State.Pts, len(got.OtherUpdates))
	}
	unpinned, ok := got.OtherUpdates[2].(*tg.UpdateStory)
	if !ok {
		t.Fatalf("third diff update = %T, want updateStory", got.OtherUpdates[2])
	}
	unpinnedItem, ok := unpinned.Story.(*tg.StoryItem)
	if !ok || unpinnedItem.Pinned {
		t.Fatalf("third story = %T %+v, want unpinned storyItem", unpinned.Story, unpinned.Story)
	}
	last, ok := got.OtherUpdates[3].(*tg.UpdateStory)
	if !ok {
		t.Fatalf("last diff update = %T, want updateStory", got.OtherUpdates[3])
	}
	if _, ok := last.Story.(*tg.StoryItemDeleted); !ok {
		t.Fatalf("last story = %T, want storyItemDeleted", last.Story)
	}
}

func TestStoriesWriteEventsExcludeCurrentSession(t *testing.T) {
	ctx := context.Background()
	authKeyID := [8]byte{9, 8, 6}
	const sessionID int64 = 1234
	userStore := memory.NewUserStore()
	user, err := userStore.Create(ctx, domain.User{AccessHash: 92011, Phone: "15550092011", FirstName: "Author"})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	storyStore := memory.NewStoryStore()
	updates := &captureUpdates{}
	r := New(Config{}, Deps{
		Users:   appusers.NewService(userStore),
		Stories: appstories.NewService(storyStore),
		Updates: updates,
		Files:   &fakeFiles{photos: map[int64]domain.Photo{}},
	}, zaptest.NewLogger(t), fixedClock{now: time.Unix(1700000510, 0)})
	reqCtx := WithSessionID(WithAuthKeyID(WithUserID(ctx, user.ID), authKeyID), sessionID)

	response, err := r.onStoriesSendStory(reqCtx, &tg.StoriesSendStoryRequest{
		Peer:         &tg.InputPeerSelf{},
		Media:        &tg.InputMediaUploadedPhoto{File: &tg.InputFile{ID: 92011, Parts: 1, Name: "story.jpg"}},
		PrivacyRules: []tg.InputPrivacyRuleClass{&tg.InputPrivacyValueAllowAll{}},
		RandomID:     92011,
		Period:       86400,
		Caption:      "first story",
		Pinned:       true,
	})
	if err != nil {
		t.Fatalf("send story: %v", err)
	}
	storyID := response.(*tg.Updates).Updates[0].(*tg.UpdateStoryID).ID
	assertCapturedStoryEventExcludesSession(t, updates, authKeyID, user.ID, sessionID, 1, storyID, false)

	edit := &tg.StoriesEditStoryRequest{Peer: &tg.InputPeerSelf{}, ID: storyID}
	edit.SetCaption("edited story")
	if _, err := r.onStoriesEditStory(reqCtx, edit); err != nil {
		t.Fatalf("edit story: %v", err)
	}
	assertCapturedStoryEventExcludesSession(t, updates, authKeyID, user.ID, sessionID, 2, storyID, false)

	if _, err := r.onStoriesTogglePinned(reqCtx, &tg.StoriesTogglePinnedRequest{
		Peer:   &tg.InputPeerSelf{},
		ID:     []int{storyID},
		Pinned: false,
	}); err != nil {
		t.Fatalf("toggle pinned: %v", err)
	}
	assertCapturedStoryEventExcludesSession(t, updates, authKeyID, user.ID, sessionID, 3, storyID, false)

	if _, err := r.onStoriesDeleteStories(reqCtx, &tg.StoriesDeleteStoriesRequest{
		Peer: &tg.InputPeerSelf{},
		ID:   []int{storyID},
	}); err != nil {
		t.Fatalf("delete story: %v", err)
	}
	assertCapturedStoryEventExcludesSession(t, updates, authKeyID, user.ID, sessionID, 4, storyID, true)
}

func assertCapturedStoryEventExcludesSession(t *testing.T, updates *captureUpdates, authKeyID [8]byte, userID, sessionID int64, wantEvents, storyID int, wantDeleted bool) {
	t.Helper()
	if updates.authKeyID != authKeyID || updates.userID != userID || updates.excludeSessionID != sessionID {
		t.Fatalf("captured update = auth %v user %d exclude_session %d, want auth %v user %d exclude_session %d", updates.authKeyID, updates.userID, updates.excludeSessionID, authKeyID, userID, sessionID)
	}
	if len(updates.events) != wantEvents {
		t.Fatalf("captured events = %d, want %d", len(updates.events), wantEvents)
	}
	last := updates.events[len(updates.events)-1]
	if last.Type != domain.UpdateEventStory || last.Peer != (domain.Peer{Type: domain.PeerTypeUser, ID: userID}) || last.Story.ID != storyID {
		t.Fatalf("last captured event = %+v, want story event for user %d story %d", last, userID, storyID)
	}
	if last.Story.Deleted != wantDeleted {
		t.Fatalf("last captured story deleted = %v, want %v", last.Story.Deleted, wantDeleted)
	}
}

func TestStoriesDeleteStoriesFanoutsDeletedUserStoryToVisibleViewer(t *testing.T) {
	ctx := context.Background()
	ownerAuthKey := [8]byte{9, 2, 1}
	viewerAuthKey := [8]byte{9, 2, 2}
	userStore := memory.NewUserStore()
	owner, err := userStore.Create(ctx, domain.User{AccessHash: 92101, Phone: "15550092101", FirstName: "Owner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	viewer, err := userStore.Create(ctx, domain.User{AccessHash: 92102, Phone: "15550092102", FirstName: "Viewer"})
	if err != nil {
		t.Fatalf("create viewer: %v", err)
	}
	storyStore := memory.NewStoryStore()
	stateStore := memory.NewUpdateStateStore()
	updateStore := memory.NewUpdateEventStore()
	r := New(Config{}, Deps{
		Users:   appusers.NewService(userStore),
		Stories: appstories.NewService(storyStore),
		Updates: appupdates.NewService(stateStore, updateStore),
		Files:   &fakeFiles{photos: map[int64]domain.Photo{}},
	}, zaptest.NewLogger(t), fixedClock{now: time.Unix(1700000520, 0)})
	ownerCtx := WithSessionID(WithAuthKeyID(WithUserID(ctx, owner.ID), ownerAuthKey), 92)

	updates, err := r.onStoriesSendStory(ownerCtx, &tg.StoriesSendStoryRequest{
		Peer:         &tg.InputPeerSelf{},
		Media:        &tg.InputMediaUploadedPhoto{File: &tg.InputFile{ID: 92101, Parts: 1, Name: "story.jpg"}},
		PrivacyRules: []tg.InputPrivacyRuleClass{&tg.InputPrivacyValueAllowAll{}},
		RandomID:     92101,
		Period:       86400,
		Caption:      "public story",
	})
	if err != nil {
		t.Fatalf("send story: %v", err)
	}
	storyID := updates.(*tg.Updates).Updates[0].(*tg.UpdateStoryID).ID
	viewerCtx := WithAuthKeyID(WithUserID(ctx, viewer.ID), viewerAuthKey)
	if ok, err := r.onStoriesIncrementStoryViews(viewerCtx, &tg.StoriesIncrementStoryViewsRequest{
		Peer: &tg.InputPeerUser{UserID: owner.ID, AccessHash: owner.AccessHash},
		ID:   []int{storyID},
	}); err != nil || !ok {
		t.Fatalf("increment story view = %v, %v, want true nil", ok, err)
	}

	deletedIDs, err := r.onStoriesDeleteStories(ownerCtx, &tg.StoriesDeleteStoriesRequest{
		Peer: &tg.InputPeerSelf{},
		ID:   []int{storyID},
	})
	if err != nil {
		t.Fatalf("delete story: %v", err)
	}
	if len(deletedIDs) != 1 || deletedIDs[0] != storyID {
		t.Fatalf("deleted ids = %v, want %d", deletedIDs, storyID)
	}
	diff, err := r.onUpdatesGetDifference(viewerCtx, &tg.UpdatesGetDifferenceRequest{Pts: 0})
	if err != nil {
		t.Fatalf("viewer get difference: %v", err)
	}
	got := diff.(*tg.UpdatesDifference)
	if got.State.Pts != 1 || len(got.OtherUpdates) != 1 {
		t.Fatalf("viewer difference pts/updates = %d/%d, want 1/1", got.State.Pts, len(got.OtherUpdates))
	}
	update, ok := got.OtherUpdates[0].(*tg.UpdateStory)
	if !ok {
		t.Fatalf("viewer update = %T, want *tg.UpdateStory", got.OtherUpdates[0])
	}
	if peer, ok := update.Peer.(*tg.PeerUser); !ok || peer.UserID != owner.ID {
		t.Fatalf("viewer update peer = %T %+v, want owner peer", update.Peer, update.Peer)
	}
	deleted, ok := update.Story.(*tg.StoryItemDeleted)
	if !ok || deleted.ID != storyID {
		t.Fatalf("viewer story = %T %+v, want storyItemDeleted %d", update.Story, update.Story, storyID)
	}
}

func TestStoriesDeleteStoriesFanoutsDeletedUserStoryToAllStoriesCachedViewer(t *testing.T) {
	ctx := context.Background()
	ownerAuthKey := [8]byte{9, 3, 1}
	viewerAuthKey := [8]byte{9, 3, 2}
	userStore := memory.NewUserStore()
	owner, err := userStore.Create(ctx, domain.User{AccessHash: 93101, Phone: "15550093101", FirstName: "Owner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	viewer, err := userStore.Create(ctx, domain.User{AccessHash: 93102, Phone: "15550093102", FirstName: "Viewer"})
	if err != nil {
		t.Fatalf("create viewer: %v", err)
	}
	storyStore := memory.NewStoryStore()
	stateStore := memory.NewUpdateStateStore()
	updateStore := memory.NewUpdateEventStore()
	r := New(Config{}, Deps{
		Users:   appusers.NewService(userStore),
		Stories: appstories.NewService(storyStore),
		Updates: appupdates.NewService(stateStore, updateStore),
		Files:   &fakeFiles{photos: map[int64]domain.Photo{}},
	}, zaptest.NewLogger(t), fixedClock{now: time.Unix(1700000520, 0)})
	ownerCtx := WithSessionID(WithAuthKeyID(WithUserID(ctx, owner.ID), ownerAuthKey), 93)

	updates, err := r.onStoriesSendStory(ownerCtx, &tg.StoriesSendStoryRequest{
		Peer:         &tg.InputPeerSelf{},
		Media:        &tg.InputMediaUploadedPhoto{File: &tg.InputFile{ID: 93101, Parts: 1, Name: "story.jpg"}},
		PrivacyRules: []tg.InputPrivacyRuleClass{&tg.InputPrivacyValueAllowAll{}},
		RandomID:     93101,
		Period:       86400,
		Caption:      "public story",
	})
	if err != nil {
		t.Fatalf("send story: %v", err)
	}
	storyID := updates.(*tg.Updates).Updates[0].(*tg.UpdateStoryID).ID
	viewerCtx := WithAuthKeyID(WithUserID(ctx, viewer.ID), viewerAuthKey)
	allClass, err := r.onStoriesGetAllStories(viewerCtx, &tg.StoriesGetAllStoriesRequest{})
	if err != nil {
		t.Fatalf("viewer get all stories: %v", err)
	}
	all := mustAllStories(t, allClass)
	if len(all.PeerStories) != 1 || len(all.PeerStories[0].Stories) != 1 {
		t.Fatalf("all stories = %+v, want cached owner story", all.PeerStories)
	}
	views, err := storyStore.ListStoryViews(ctx, domain.StoryViewListRequest{
		ViewerUserID: owner.ID,
		Owner:        domain.Peer{Type: domain.PeerTypeUser, ID: owner.ID},
		StoryID:      storyID,
		Limit:        10,
	})
	if err != nil {
		t.Fatalf("list story views: %v", err)
	}
	if views.Count != 0 || views.ViewsCount != 0 || len(views.Views) != 0 {
		t.Fatalf("story views = %+v, want cached exposure without view counters", views)
	}

	deletedIDs, err := r.onStoriesDeleteStories(ownerCtx, &tg.StoriesDeleteStoriesRequest{
		Peer: &tg.InputPeerSelf{},
		ID:   []int{storyID},
	})
	if err != nil {
		t.Fatalf("delete story: %v", err)
	}
	if len(deletedIDs) != 1 || deletedIDs[0] != storyID {
		t.Fatalf("deleted ids = %v, want %d", deletedIDs, storyID)
	}
	diff, err := r.onUpdatesGetDifference(viewerCtx, &tg.UpdatesGetDifferenceRequest{Pts: 0})
	if err != nil {
		t.Fatalf("viewer get difference: %v", err)
	}
	got := diff.(*tg.UpdatesDifference)
	if got.State.Pts != 1 || len(got.OtherUpdates) != 1 {
		t.Fatalf("viewer difference pts/updates = %d/%d, want 1/1", got.State.Pts, len(got.OtherUpdates))
	}
	update, ok := got.OtherUpdates[0].(*tg.UpdateStory)
	if !ok {
		t.Fatalf("viewer update = %T, want *tg.UpdateStory", got.OtherUpdates[0])
	}
	if peer, ok := update.Peer.(*tg.PeerUser); !ok || peer.UserID != owner.ID {
		t.Fatalf("viewer update peer = %T %+v, want owner peer", update.Peer, update.Peer)
	}
	deleted, ok := update.Story.(*tg.StoryItemDeleted)
	if !ok || deleted.ID != storyID {
		t.Fatalf("viewer story = %T %+v, want storyItemDeleted %d", update.Story, update.Story, storyID)
	}
}

func TestStoriesDeleteStoriesFanoutsExpiredPinnedUserStory(t *testing.T) {
	ctx := context.Background()
	ownerAuthKey := [8]byte{9, 2, 3}
	viewerAuthKey := [8]byte{9, 2, 4}
	userStore := memory.NewUserStore()
	owner, err := userStore.Create(ctx, domain.User{AccessHash: 92103, Phone: "15550092103", FirstName: "Owner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	viewer, err := userStore.Create(ctx, domain.User{AccessHash: 92104, Phone: "15550092104", FirstName: "Viewer"})
	if err != nil {
		t.Fatalf("create viewer: %v", err)
	}
	now := time.Unix(1700000800, 0)
	ownerPeer := domain.Peer{Type: domain.PeerTypeUser, ID: owner.ID}
	storyStore := memory.NewStoryStore()
	if _, err := storyStore.UpsertStory(ctx, domain.UpsertStoryRequest{Story: domain.Story{
		Owner:      ownerPeer,
		ID:         1,
		Date:       int(now.Unix()) - 7200,
		ExpireDate: int(now.Unix()) - 3600,
		Pinned:     true,
		Public:     true,
	}}); err != nil {
		t.Fatalf("upsert expired pinned story: %v", err)
	}
	if _, err := storyStore.IncrementViews(ctx, viewer.ID, ownerPeer, []int{1}, int(now.Unix())); err != nil {
		t.Fatalf("increment expired pinned story view: %v", err)
	}
	r := New(Config{}, Deps{
		Users:   appusers.NewService(userStore),
		Stories: appstories.NewService(storyStore),
		Updates: appupdates.NewService(memory.NewUpdateStateStore(), memory.NewUpdateEventStore()),
	}, zaptest.NewLogger(t), fixedClock{now: now})
	ownerCtx := WithSessionID(WithAuthKeyID(WithUserID(ctx, owner.ID), ownerAuthKey), 92)

	deletedIDs, err := r.onStoriesDeleteStories(ownerCtx, &tg.StoriesDeleteStoriesRequest{
		Peer: &tg.InputPeerSelf{},
		ID:   []int{1},
	})
	if err != nil {
		t.Fatalf("delete expired pinned story: %v", err)
	}
	if len(deletedIDs) != 1 || deletedIDs[0] != 1 {
		t.Fatalf("deleted ids = %v, want [1]", deletedIDs)
	}
	viewerCtx := WithAuthKeyID(WithUserID(ctx, viewer.ID), viewerAuthKey)
	diff, err := r.onUpdatesGetDifference(viewerCtx, &tg.UpdatesGetDifferenceRequest{Pts: 0})
	if err != nil {
		t.Fatalf("viewer get difference: %v", err)
	}
	got := diff.(*tg.UpdatesDifference)
	if got.State.Pts != 1 || len(got.OtherUpdates) != 1 {
		t.Fatalf("viewer difference pts/updates = %d/%d, want 1/1", got.State.Pts, len(got.OtherUpdates))
	}
	update, ok := got.OtherUpdates[0].(*tg.UpdateStory)
	if !ok {
		t.Fatalf("viewer update = %T, want *tg.UpdateStory", got.OtherUpdates[0])
	}
	deleted, ok := update.Story.(*tg.StoryItemDeleted)
	if !ok || deleted.ID != 1 {
		t.Fatalf("viewer story = %T %+v, want storyItemDeleted 1", update.Story, update.Story)
	}
}

func TestStoriesTogglePinnedFanoutsExpiredPinnedUserStoryRemoval(t *testing.T) {
	ctx := context.Background()
	ownerAuthKey := [8]byte{9, 2, 5}
	viewerAuthKey := [8]byte{9, 2, 6}
	userStore := memory.NewUserStore()
	owner, err := userStore.Create(ctx, domain.User{AccessHash: 92105, Phone: "15550092105", FirstName: "Owner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	viewer, err := userStore.Create(ctx, domain.User{AccessHash: 92106, Phone: "15550092106", FirstName: "Viewer"})
	if err != nil {
		t.Fatalf("create viewer: %v", err)
	}
	now := time.Unix(1700000900, 0)
	ownerPeer := domain.Peer{Type: domain.PeerTypeUser, ID: owner.ID}
	storyStore := memory.NewStoryStore()
	if _, err := storyStore.UpsertStory(ctx, domain.UpsertStoryRequest{Story: domain.Story{
		Owner:      ownerPeer,
		ID:         1,
		Date:       int(now.Unix()) - 7200,
		ExpireDate: int(now.Unix()) - 3600,
		Pinned:     true,
		Public:     true,
	}}); err != nil {
		t.Fatalf("upsert expired pinned story: %v", err)
	}
	if _, err := storyStore.IncrementViews(ctx, viewer.ID, ownerPeer, []int{1}, int(now.Unix())); err != nil {
		t.Fatalf("increment expired pinned story view: %v", err)
	}
	r := New(Config{}, Deps{
		Users:   appusers.NewService(userStore),
		Stories: appstories.NewService(storyStore),
		Updates: appupdates.NewService(memory.NewUpdateStateStore(), memory.NewUpdateEventStore()),
	}, zaptest.NewLogger(t), fixedClock{now: now})
	ownerCtx := WithSessionID(WithAuthKeyID(WithUserID(ctx, owner.ID), ownerAuthKey), 93)

	pinnedIDs, err := r.onStoriesTogglePinned(ownerCtx, &tg.StoriesTogglePinnedRequest{
		Peer:   &tg.InputPeerSelf{},
		ID:     []int{1},
		Pinned: false,
	})
	if err != nil {
		t.Fatalf("toggle expired pinned story: %v", err)
	}
	if len(pinnedIDs) != 1 || pinnedIDs[0] != 1 {
		t.Fatalf("pinned ids = %v, want [1]", pinnedIDs)
	}
	viewerCtx := WithAuthKeyID(WithUserID(ctx, viewer.ID), viewerAuthKey)
	diff, err := r.onUpdatesGetDifference(viewerCtx, &tg.UpdatesGetDifferenceRequest{Pts: 0})
	if err != nil {
		t.Fatalf("viewer get difference: %v", err)
	}
	got := diff.(*tg.UpdatesDifference)
	if got.State.Pts != 1 || len(got.OtherUpdates) != 1 {
		t.Fatalf("viewer difference pts/updates = %d/%d, want 1/1", got.State.Pts, len(got.OtherUpdates))
	}
	update, ok := got.OtherUpdates[0].(*tg.UpdateStory)
	if !ok {
		t.Fatalf("viewer update = %T, want *tg.UpdateStory", got.OtherUpdates[0])
	}
	deleted, ok := update.Story.(*tg.StoryItemDeleted)
	if !ok || deleted.ID != 1 {
		t.Fatalf("viewer story = %T %+v, want storyItemDeleted 1", update.Story, update.Story)
	}
}

func TestStoriesSendStoryRandomIDRetryReturnsOriginalWithoutNewUpdate(t *testing.T) {
	ctx := context.Background()
	authKeyID := [8]byte{7, 4, 1}
	userStore := memory.NewUserStore()
	user, err := userStore.Create(ctx, domain.User{AccessHash: 92011, Phone: "15550092011", FirstName: "Author"})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	storyStore := memory.NewStoryStore()
	updateStore := memory.NewUpdateEventStore()
	now := time.Unix(1700000510, 0)
	r := New(Config{}, Deps{
		Users:   appusers.NewService(userStore),
		Stories: appstories.NewService(storyStore),
		Updates: appupdates.NewService(memory.NewUpdateStateStore(), updateStore),
		Files:   &fakeFiles{photos: map[int64]domain.Photo{}},
	}, zaptest.NewLogger(t), fixedClock{now: now})
	reqCtx := WithSessionID(WithAuthKeyID(WithUserID(ctx, user.ID), authKeyID), 74)

	first, err := r.onStoriesSendStory(reqCtx, &tg.StoriesSendStoryRequest{
		Peer:         &tg.InputPeerSelf{},
		Media:        &tg.InputMediaUploadedPhoto{File: &tg.InputFile{ID: 7401, Parts: 1, Name: "first.jpg"}},
		PrivacyRules: []tg.InputPrivacyRuleClass{&tg.InputPrivacyValueAllowAll{}},
		RandomID:     7401,
		Period:       86400,
		Caption:      "first random story",
	})
	if err != nil {
		t.Fatalf("send first story: %v", err)
	}
	firstUpdates, ok := first.(*tg.Updates)
	if !ok || len(firstUpdates.Updates) != 2 {
		t.Fatalf("first updates = %T %+v, want updateStoryID + updateStory", first, first)
	}
	firstID, ok := firstUpdates.Updates[0].(*tg.UpdateStoryID)
	if !ok || firstID.ID != 1 || firstID.RandomID != 7401 {
		t.Fatalf("first story id update = %T %+v, want id 1 random 7401", firstUpdates.Updates[0], firstUpdates.Updates[0])
	}

	retry, err := r.onStoriesSendStory(reqCtx, &tg.StoriesSendStoryRequest{
		Peer:         &tg.InputPeerSelf{},
		Media:        &tg.InputMediaUploadedPhoto{File: &tg.InputFile{ID: 7402, Parts: 1, Name: "retry.jpg"}},
		PrivacyRules: []tg.InputPrivacyRuleClass{&tg.InputPrivacyValueAllowAll{}},
		RandomID:     7401,
		Period:       86400,
		Caption:      "retry must not replace",
	})
	if err != nil {
		t.Fatalf("retry story: %v", err)
	}
	retryUpdates, ok := retry.(*tg.Updates)
	if !ok || len(retryUpdates.Updates) != 2 {
		t.Fatalf("retry updates = %T %+v, want updateStoryID + updateStory", retry, retry)
	}
	retryID, ok := retryUpdates.Updates[0].(*tg.UpdateStoryID)
	if !ok || retryID.ID != firstID.ID || retryID.RandomID != 7401 {
		t.Fatalf("retry story id update = %T %+v, want same id/random", retryUpdates.Updates[0], retryUpdates.Updates[0])
	}
	retryStoryUpdate, ok := retryUpdates.Updates[1].(*tg.UpdateStory)
	if !ok {
		t.Fatalf("retry second update = %T, want updateStory", retryUpdates.Updates[1])
	}
	retryItem, ok := retryStoryUpdate.Story.(*tg.StoryItem)
	if !ok || retryItem.ID != firstID.ID || retryItem.Caption != "first random story" {
		t.Fatalf("retry story = %T %+v, want original story snapshot", retryStoryUpdate.Story, retryStoryUpdate.Story)
	}

	peerStories, err := r.onStoriesGetPeerStories(reqCtx, &tg.InputPeerSelf{})
	if err != nil {
		t.Fatalf("get peer stories after retry: %v", err)
	}
	if len(peerStories.Stories.Stories) != 1 {
		t.Fatalf("peer stories = %+v, want one story after retry", peerStories.Stories.Stories)
	}
	stored, ok := peerStories.Stories.Stories[0].(*tg.StoryItem)
	if !ok || stored.ID != firstID.ID || stored.Caption != "first random story" {
		t.Fatalf("stored story = %T %+v, want original story", peerStories.Stories.Stories[0], peerStories.Stories.Stories[0])
	}

	diff, err := r.onUpdatesGetDifference(reqCtx, &tg.UpdatesGetDifferenceRequest{Pts: 0})
	if err != nil {
		t.Fatalf("get difference after retry: %v", err)
	}
	got, ok := diff.(*tg.UpdatesDifference)
	if !ok {
		t.Fatalf("difference = %T, want updates.difference", diff)
	}
	if got.State.Pts != 1 || len(got.OtherUpdates) != 1 {
		t.Fatalf("difference pts/updates = %d/%d, want only first publish", got.State.Pts, len(got.OtherUpdates))
	}
	diffStory, ok := got.OtherUpdates[0].(*tg.UpdateStory)
	if !ok {
		t.Fatalf("difference update = %T, want updateStory", got.OtherUpdates[0])
	}
	diffItem, ok := diffStory.Story.(*tg.StoryItem)
	if !ok || diffItem.ID != firstID.ID || diffItem.Caption != "first random story" {
		t.Fatalf("difference story = %T %+v, want original story snapshot", diffStory.Story, diffStory.Story)
	}
}

func TestStoriesGetStoriesArchiveReturnsOwnerExpiredStories(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, err := userStore.Create(ctx, domain.User{AccessHash: 92021, Phone: "15550092021", FirstName: "Owner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	stranger, err := userStore.Create(ctx, domain.User{AccessHash: 92022, Phone: "15550092022", FirstName: "Stranger"})
	if err != nil {
		t.Fatalf("create stranger: %v", err)
	}
	storyStore := memory.NewStoryStore()
	ownerPeer := domain.Peer{Type: domain.PeerTypeUser, ID: owner.ID}
	fixtures := []domain.Story{
		{Owner: ownerPeer, ID: 1, Date: 1700000511, ExpireDate: 1700000520, Public: true, Caption: "old one"},
		{Owner: ownerPeer, ID: 2, Date: 1700000512, ExpireDate: 1700000600, Public: true, Caption: "active"},
		{Owner: ownerPeer, ID: 3, Date: 1700000513, ExpireDate: 1700000520, Public: true, Pinned: true, Caption: "old pinned"},
		{Owner: ownerPeer, ID: 4, Date: 1700000514, ExpireDate: 1700000520, Public: true, Deleted: true, Caption: "deleted"},
		{Owner: ownerPeer, ID: 5, Date: 1700000515, ExpireDate: 1700000520, Public: true, Caption: "old latest"},
	}
	for _, story := range fixtures {
		if _, err := storyStore.UpsertStory(ctx, domain.UpsertStoryRequest{Story: story}); err != nil {
			t.Fatalf("upsert story %d: %v", story.ID, err)
		}
	}
	r := New(Config{}, Deps{
		Users:   appusers.NewService(userStore),
		Stories: appstories.NewService(storyStore),
	}, zaptest.NewLogger(t), fixedClock{now: time.Unix(1700000550, 0)})
	ownerCtx := WithUserID(ctx, owner.ID)

	countOnly, err := r.onStoriesGetStoriesArchive(ownerCtx, &tg.StoriesGetStoriesArchiveRequest{
		Peer:  &tg.InputPeerSelf{},
		Limit: 0,
	})
	if err != nil {
		t.Fatalf("count archive stories: %v", err)
	}
	if countOnly.Count != 3 || len(countOnly.Stories) != 0 {
		t.Fatalf("count-only archive = %+v, want count 3 and no stories", countOnly)
	}

	first, err := r.onStoriesGetStoriesArchive(ownerCtx, &tg.StoriesGetStoriesArchiveRequest{
		Peer:  &tg.InputPeerSelf{},
		Limit: 2,
	})
	if err != nil {
		t.Fatalf("get archive first page: %v", err)
	}
	if first.Count != 3 || len(first.Stories) != 2 {
		t.Fatalf("first archive page = %+v, want count 3 and two stories", first)
	}
	firstA, ok := first.Stories[0].(*tg.StoryItem)
	firstB, okB := first.Stories[1].(*tg.StoryItem)
	if !ok || !okB || firstA.ID != 5 || firstB.ID != 3 || !firstB.Pinned || !firstA.Out || !firstB.Out {
		t.Fatalf("first archive stories = %T/%T %+v, want story ids 5,3 owner out and pinned id 3", first.Stories[0], first.Stories[1], first.Stories)
	}

	second, err := r.onStoriesGetStoriesArchive(ownerCtx, &tg.StoriesGetStoriesArchiveRequest{
		Peer:     &tg.InputPeerSelf{},
		OffsetID: 3,
		Limit:    2,
	})
	if err != nil {
		t.Fatalf("get archive second page: %v", err)
	}
	if second.Count != 3 || len(second.Stories) != 1 {
		t.Fatalf("second archive page = %+v, want count 3 and one story", second)
	}
	secondItem, ok := second.Stories[0].(*tg.StoryItem)
	if !ok || secondItem.ID != 1 || secondItem.Caption != "old one" {
		t.Fatalf("second archive story = %T %+v, want story id 1", second.Stories[0], second.Stories[0])
	}

	_, err = r.onStoriesGetStoriesArchive(WithUserID(ctx, stranger.ID), &tg.StoriesGetStoriesArchiveRequest{
		Peer:  &tg.InputPeerUser{UserID: owner.ID, AccessHash: owner.AccessHash},
		Limit: 10,
	})
	if err == nil || !strings.Contains(err.Error(), "PEER_ID_INVALID") {
		t.Fatalf("stranger archive err = %v, want PEER_ID_INVALID", err)
	}
}

func TestStoriesLongtailCompatHandlers(t *testing.T) {
	ctx := context.Background()
	storyStore := memory.NewStoryStore()
	ownerID := int64(9311)
	owner := domain.Peer{Type: domain.PeerTypeUser, ID: ownerID}
	if _, err := storyStore.UpsertStory(ctx, domain.UpsertStoryRequest{Story: domain.Story{
		Owner:      owner,
		ID:         1,
		Date:       1700000000,
		ExpireDate: 1700003600,
		Public:     true,
	}}); err != nil {
		t.Fatalf("upsert story: %v", err)
	}
	r := New(Config{}, Deps{
		Stories: appstories.NewService(storyStore),
	}, zaptest.NewLogger(t), fixedClock{now: time.Unix(1700000100, 0)})
	reqCtx := WithUserID(ctx, ownerID)

	link, err := r.onStoriesExportStoryLink(reqCtx, &tg.StoriesExportStoryLinkRequest{
		Peer: &tg.InputPeerSelf{},
		ID:   1,
	})
	if err != nil {
		t.Fatalf("export story link: %v", err)
	}
	if link.Link != "https://telesrv.local/story/user/9311/1" {
		t.Fatalf("export story link = %q, want deterministic telesrv link", link.Link)
	}
	if _, err := r.onStoriesExportStoryLink(reqCtx, &tg.StoriesExportStoryLinkRequest{
		Peer: &tg.InputPeerSelf{},
		ID:   99,
	}); err == nil || !strings.Contains(err.Error(), "STORY_ID_INVALID") {
		t.Fatalf("export missing story err = %v, want STORY_ID_INVALID", err)
	}

	if _, err := r.onStoriesActivateStealthMode(reqCtx, nil); err == nil || !tgerr.Is(err, "INPUT_REQUEST_INVALID") {
		t.Fatalf("activate stealth nil request err = %v, want INPUT_REQUEST_INVALID", err)
	}
	if _, err := r.onStoriesActivateStealthMode(ctx, &tg.StoriesActivateStealthModeRequest{}); err == nil || !tgerr.Is(err, "INPUT_REQUEST_INVALID") {
		t.Fatalf("activate stealth no flags err = %v, want INPUT_REQUEST_INVALID before auth", err)
	}
	activateBoth := &tg.StoriesActivateStealthModeRequest{}
	activateBoth.SetPast(true)
	activateBoth.SetFuture(true)
	updates, err := r.onStoriesActivateStealthMode(reqCtx, activateBoth)
	if err != nil {
		t.Fatalf("activate stealth mode: %v", err)
	}
	got, ok := updates.(*tg.Updates)
	if !ok || len(got.Updates) != 1 || got.Date != 1700000100 {
		t.Fatalf("stealth updates = %T %+v, want one update at fixed date", updates, updates)
	}
	stealthUpdate, ok := got.Updates[0].(*tg.UpdateStoriesStealthMode)
	if !ok {
		t.Fatalf("stealth update = %T %+v, want updateStoriesStealthMode", got.Updates[0], got.Updates[0])
	}
	if active, ok := stealthUpdate.StealthMode.GetActiveUntilDate(); !ok || active != 1700000100+storyStealthFuturePeriodSeconds {
		t.Fatalf("stealth active_until = %d %v, want %d", active, ok, 1700000100+storyStealthFuturePeriodSeconds)
	}
	if cooldown, ok := stealthUpdate.StealthMode.GetCooldownUntilDate(); !ok || cooldown != 1700000100+storyStealthCooldownSeconds {
		t.Fatalf("stealth cooldown_until = %d %v, want %d", cooldown, ok, 1700000100+storyStealthCooldownSeconds)
	}
	activatePastOnly := &tg.StoriesActivateStealthModeRequest{}
	activatePastOnly.SetPast(true)
	updates, err = r.onStoriesActivateStealthMode(reqCtx, activatePastOnly)
	if err != nil {
		t.Fatalf("activate stealth past-only: %v", err)
	}
	got = updates.(*tg.Updates)
	stealthUpdate = got.Updates[0].(*tg.UpdateStoriesStealthMode)
	if active, ok := stealthUpdate.StealthMode.GetActiveUntilDate(); ok || active != 0 {
		t.Fatalf("past-only stealth active_until = %d %v, want absent", active, ok)
	}
	if cooldown, ok := stealthUpdate.StealthMode.GetCooldownUntilDate(); !ok || cooldown != 1700000100+storyStealthCooldownSeconds {
		t.Fatalf("past-only stealth cooldown_until = %d %v, want %d", cooldown, ok, 1700000100+storyStealthCooldownSeconds)
	}

	if _, err := r.onStoriesReport(reqCtx, &tg.StoriesReportRequest{
		Peer: &tg.InputPeerSelf{},
	}); err == nil || !tgerr.Is(err, "STORY_ID_EMPTY") {
		t.Fatalf("report empty stories err = %v, want STORY_ID_EMPTY", err)
	}
	if _, err := r.onStoriesReport(reqCtx, &tg.StoriesReportRequest{
		Peer: &tg.InputPeerEmpty{},
	}); err == nil || !tgerr.Is(err, "STORY_ID_EMPTY") {
		t.Fatalf("report empty stories before peer err = %v, want STORY_ID_EMPTY", err)
	}
	oversizedReportIDs := make([]int, domain.MaxStoryIDs+1)
	for i := range oversizedReportIDs {
		oversizedReportIDs[i] = 1
	}
	if _, err := r.onStoriesReport(reqCtx, &tg.StoriesReportRequest{
		Peer: &tg.InputPeerSelf{},
		ID:   oversizedReportIDs,
	}); err == nil || !tgerr.Is(err, "LIMIT_INVALID") {
		t.Fatalf("report oversized stories err = %v, want LIMIT_INVALID", err)
	}
	if _, err := r.onStoriesReport(reqCtx, &tg.StoriesReportRequest{
		Peer: &tg.InputPeerEmpty{},
		ID:   oversizedReportIDs,
	}); err == nil || !tgerr.Is(err, "LIMIT_INVALID") {
		t.Fatalf("report oversized stories before peer err = %v, want LIMIT_INVALID", err)
	}
	if _, err := r.onStoriesReport(reqCtx, &tg.StoriesReportRequest{
		Peer: &tg.InputPeerEmpty{},
		ID:   []int{0},
	}); err == nil || !tgerr.Is(err, "STORY_ID_INVALID") {
		t.Fatalf("report invalid story id before peer err = %v, want STORY_ID_INVALID", err)
	}
	if _, err := r.onStoriesReport(reqCtx, &tg.StoriesReportRequest{
		Peer:   &tg.InputPeerSelf{},
		ID:     []int{1},
		Option: []byte(strings.Repeat("x", maxReportOptionLength+1)),
	}); err == nil || !tgerr.Is(err, "LIMIT_INVALID") {
		t.Fatalf("report oversized option err = %v, want LIMIT_INVALID", err)
	}
	if _, err := r.onStoriesReport(reqCtx, &tg.StoriesReportRequest{
		Peer:   &tg.InputPeerEmpty{},
		ID:     []int{1},
		Option: []byte(strings.Repeat("x", maxReportOptionLength+1)),
	}); err == nil || !tgerr.Is(err, "LIMIT_INVALID") {
		t.Fatalf("report oversized option before peer err = %v, want LIMIT_INVALID", err)
	}
	if _, err := r.onStoriesReport(reqCtx, &tg.StoriesReportRequest{
		Peer:    &tg.InputPeerSelf{},
		ID:      []int{1},
		Message: strings.Repeat("x", maxReportCommentLength+1),
	}); err == nil || !tgerr.Is(err, "LIMIT_INVALID") {
		t.Fatalf("report oversized comment err = %v, want LIMIT_INVALID", err)
	}
	if _, err := r.onStoriesReport(reqCtx, &tg.StoriesReportRequest{
		Peer:   &tg.InputPeerSelf{},
		ID:     []int{1},
		Option: []byte("definitely_unknown"),
	}); err == nil || !tgerr.Is(err, "OPTION_INVALID") {
		t.Fatalf("report unknown option err = %v, want OPTION_INVALID", err)
	}

	reportOptions, err := r.onStoriesReport(reqCtx, &tg.StoriesReportRequest{
		Peer: &tg.InputPeerSelf{},
		ID:   []int{1, 1},
	})
	if err != nil {
		t.Fatalf("report initial options: %v", err)
	}
	if choices, ok := reportOptions.(*tg.ReportResultChooseOption); !ok || len(choices.Options) == 0 {
		t.Fatalf("report options = %T %+v, want choose-option", reportOptions, reportOptions)
	}
	reportComment, err := r.onStoriesReport(reqCtx, &tg.StoriesReportRequest{
		Peer:   &tg.InputPeerSelf{},
		ID:     []int{1},
		Option: []byte("other"),
	})
	if err != nil {
		t.Fatalf("report other: %v", err)
	}
	if _, ok := reportComment.(*tg.ReportResultAddComment); !ok {
		t.Fatalf("report other = %T %+v, want add-comment", reportComment, reportComment)
	}
	reported, err := r.onStoriesReport(reqCtx, &tg.StoriesReportRequest{
		Peer:   &tg.InputPeerSelf{},
		ID:     []int{1},
		Option: []byte("spam"),
	})
	if err != nil {
		t.Fatalf("report spam: %v", err)
	}
	if _, ok := reported.(*tg.ReportResultReported); !ok {
		t.Fatalf("report spam = %T %+v, want reported", reported, reported)
	}
	if _, err := r.onStoriesReport(reqCtx, &tg.StoriesReportRequest{
		Peer:   &tg.InputPeerSelf{},
		ID:     []int{99},
		Option: []byte("spam"),
	}); err == nil || !strings.Contains(err.Error(), "STORY_ID_INVALID") {
		t.Fatalf("report missing story err = %v, want STORY_ID_INVALID", err)
	}

	found, err := r.onStoriesSearchPosts(reqCtx, &tg.StoriesSearchPostsRequest{
		Hashtag: "storytag",
		Limit:   20,
	})
	if err != nil {
		t.Fatalf("search story posts: %v", err)
	}
	if found.Count != 0 || len(found.Stories) != 0 || len(found.Users) != 0 || len(found.Chats) != 0 {
		t.Fatalf("search story posts = %+v, want empty bounded result", found)
	}
	if _, err := r.onStoriesSearchPosts(ctx, nil); err == nil || !tgerr.Is(err, "INPUT_REQUEST_INVALID") {
		t.Fatalf("nil search story posts without user ctx err = %v, want INPUT_REQUEST_INVALID", err)
	}
	areaSearch, err := r.onStoriesSearchPosts(reqCtx, &tg.StoriesSearchPostsRequest{
		Area:  testStoryGeoPointMediaArea(31.2, 121.4, 10),
		Limit: 20,
	})
	if err != nil {
		t.Fatalf("search story posts by area: %v", err)
	}
	if areaSearch.Count != 0 || len(areaSearch.Stories) != 0 || len(areaSearch.Users) != 0 || len(areaSearch.Chats) != 0 {
		t.Fatalf("search story posts by area = %+v, want empty bounded result", areaSearch)
	}
	venueAreaSearch, err := r.onStoriesSearchPosts(reqCtx, &tg.StoriesSearchPostsRequest{
		Area:  testStoryVenueMediaArea("Inline Cafe", 31.2, 121.4, 12),
		Limit: 20,
	})
	if err != nil {
		t.Fatalf("search story posts by venue area: %v", err)
	}
	if venueAreaSearch.Count != 0 || len(venueAreaSearch.Stories) != 0 || len(venueAreaSearch.Users) != 0 || len(venueAreaSearch.Chats) != 0 {
		t.Fatalf("search story posts by venue area = %+v, want empty bounded result", venueAreaSearch)
	}
	if _, err := r.onStoriesSearchPosts(reqCtx, &tg.StoriesSearchPostsRequest{Limit: 20}); err == nil || !tgerr.Is(err, "SEARCH_QUERY_EMPTY") {
		t.Fatalf("search story posts empty query err = %v, want SEARCH_QUERY_EMPTY", err)
	}
	if _, err := r.onStoriesSearchPosts(reqCtx, &tg.StoriesSearchPostsRequest{
		Hashtag: "storytag",
		Area:    testStoryGeoPointMediaArea(31.2, 121.4, 10),
		Limit:   20,
	}); err == nil || !tgerr.Is(err, "SEARCH_QUERY_EMPTY") {
		t.Fatalf("search story posts hashtag+area err = %v, want SEARCH_QUERY_EMPTY", err)
	}
	prefixed, err := r.onStoriesSearchPosts(reqCtx, &tg.StoriesSearchPostsRequest{
		Hashtag: "#storytag",
		Limit:   20,
	})
	if err != nil {
		t.Fatalf("search story posts prefixed hashtag: %v", err)
	}
	if prefixed.Count != 0 || len(prefixed.Stories) != 0 || len(prefixed.Users) != 0 || len(prefixed.Chats) != 0 {
		t.Fatalf("search story posts prefixed hashtag = %+v, want empty bounded result", prefixed)
	}
	cashtag, err := r.onStoriesSearchPosts(reqCtx, &tg.StoriesSearchPostsRequest{
		Hashtag: "$storytag",
		Limit:   20,
	})
	if err != nil {
		t.Fatalf("search story posts cashtag: %v", err)
	}
	if cashtag.Count != 0 || len(cashtag.Stories) != 0 || len(cashtag.Users) != 0 || len(cashtag.Chats) != 0 {
		t.Fatalf("search story posts cashtag = %+v, want empty bounded result", cashtag)
	}
	if _, err := r.onStoriesSearchPosts(reqCtx, &tg.StoriesSearchPostsRequest{
		Hashtag: "story#tag",
		Limit:   20,
	}); err == nil || !tgerr.Is(err, "LIMIT_INVALID") {
		t.Fatalf("search story posts embedded marker err = %v, want LIMIT_INVALID", err)
	}
	if _, err := r.onStoriesSearchPosts(reqCtx, &tg.StoriesSearchPostsRequest{
		Hashtag: "storytag",
		Offset:  strings.Repeat("x", maxStorySearchPostsOffsetLength+1),
		Limit:   20,
	}); err == nil || !tgerr.Is(err, "OFFSET_INVALID") {
		t.Fatalf("search story posts offset err = %v, want OFFSET_INVALID", err)
	}
	if _, err := r.onStoriesSearchPosts(reqCtx, &tg.StoriesSearchPostsRequest{
		Area:  &tg.MediaAreaSuggestedReaction{},
		Limit: 20,
	}); err == nil || !tgerr.Is(err, "MEDIA_INVALID") {
		t.Fatalf("search story posts unsupported area err = %v, want MEDIA_INVALID", err)
	}
	var typedNilGeo *tg.MediaAreaGeoPoint
	for _, tc := range []struct {
		name string
		area tg.MediaAreaClass
	}{
		{name: "typed nil geo", area: typedNilGeo},
		{name: "missing geo", area: &tg.MediaAreaGeoPoint{Coordinates: tg.MediaAreaCoordinates{X: 10, Y: 10, W: 10, H: 10}}},
		{name: "bad venue", area: &tg.MediaAreaVenue{
			Coordinates: tg.MediaAreaCoordinates{X: 10, Y: 10, W: 10, H: 10},
			Geo:         &tg.GeoPoint{Lat: 31.2, Long: 121.4},
		}},
	} {
		t.Run("search posts area "+tc.name, func(t *testing.T) {
			if _, err := r.onStoriesSearchPosts(reqCtx, &tg.StoriesSearchPostsRequest{
				Area:  tc.area,
				Peer:  &tg.InputPeerEmpty{},
				Limit: 20,
			}); err == nil || !tgerr.Is(err, "MEDIA_INVALID") {
				t.Fatalf("search story posts bad area err = %v, want MEDIA_INVALID", err)
			}
		})
	}

	if ok, err := r.onStoriesToggleAllStoriesHidden(reqCtx, true); err != nil || !ok {
		t.Fatalf("toggle all stories hidden = %v, %v, want true nil", ok, err)
	}

	_, err = r.onStoriesStartLive(reqCtx, &tg.StoriesStartLiveRequest{
		Peer:         &tg.InputPeerSelf{},
		PrivacyRules: []tg.InputPrivacyRuleClass{&tg.InputPrivacyValueAllowAll{}},
		RandomID:     42,
	})
	if err == nil || !tgerr.Is(err, "METHOD_INVALID") {
		t.Fatalf("start live err = %v, want METHOD_INVALID", err)
	}
	_, err = r.onStoriesStartLive(reqCtx, &tg.StoriesStartLiveRequest{
		Peer:         &tg.InputPeerEmpty{},
		PrivacyRules: []tg.InputPrivacyRuleClass{&tg.InputPrivacyValueAllowAll{}},
	})
	if err == nil || !tgerr.Is(err, "RANDOM_ID_EMPTY") {
		t.Fatalf("start live zero random before peer err = %v, want RANDOM_ID_EMPTY", err)
	}
	for _, tc := range []struct {
		name  string
		stars int64
	}{
		{name: "negative stars", stars: -1},
		{name: "too many stars", stars: maxChannelPaidMessageStars + 1},
	} {
		t.Run("start live "+tc.name, func(t *testing.T) {
			req := &tg.StoriesStartLiveRequest{
				Peer:         &tg.InputPeerEmpty{},
				PrivacyRules: []tg.InputPrivacyRuleClass{&tg.InputPrivacyValueAllowAll{}},
				RandomID:     42,
			}
			req.SetSendPaidMessagesStars(tc.stars)
			_, err := r.onStoriesStartLive(reqCtx, req)
			if err == nil || !tgerr.Is(err, "STARS_AMOUNT_INVALID") {
				t.Fatalf("start live %s err = %v, want STARS_AMOUNT_INVALID", tc.name, err)
			}
		})
	}
	_, err = r.onStoriesStartLive(reqCtx, &tg.StoriesStartLiveRequest{
		Peer:         &tg.InputPeerEmpty{},
		PrivacyRules: []tg.InputPrivacyRuleClass{&tg.InputPrivacyValueAllowAll{}},
		RandomID:     42,
	})
	if err == nil || !tgerr.Is(err, "PEER_ID_INVALID") {
		t.Fatalf("start live invalid peer err = %v, want PEER_ID_INVALID", err)
	}
	_, err = r.onStoriesStartLive(reqCtx, &tg.StoriesStartLiveRequest{
		Peer:         &tg.InputPeerEmpty{},
		PrivacyRules: []tg.InputPrivacyRuleClass{nil},
		RandomID:     42,
	})
	if err == nil || !tgerr.Is(err, "PRIVACY_VALUE_INVALID") {
		t.Fatalf("start live bad privacy before peer err = %v, want PRIVACY_VALUE_INVALID", err)
	}
	_, err = r.onStoriesStartLive(reqCtx, &tg.StoriesStartLiveRequest{
		Peer: &tg.InputPeerSelf{},
		PrivacyRules: []tg.InputPrivacyRuleClass{&tg.InputPrivacyValueAllowUsers{
			Users: []tg.InputUserClass{&tg.InputUser{UserID: 1000000002}},
		}},
		RandomID: 42,
	})
	if err == nil || !tgerr.Is(err, "USER_ID_INVALID") {
		t.Fatalf("start live allow users without user resolver err = %v, want USER_ID_INVALID", err)
	}
	inBounds := &tg.StoriesStartLiveRequest{
		Peer:         &tg.InputPeerSelf{},
		PrivacyRules: []tg.InputPrivacyRuleClass{&tg.InputPrivacyValueAllowAll{}},
		RandomID:     43,
		RtmpStream:   true,
	}
	inBounds.SetMessagesEnabled(false)
	inBounds.SetSendPaidMessagesStars(0)
	_, err = r.onStoriesStartLive(reqCtx, inBounds)
	if err == nil || !tgerr.Is(err, "METHOD_INVALID") {
		t.Fatalf("start live false messages/zero stars err = %v, want METHOD_INVALID", err)
	}
}

func TestStoriesMediaAreasRoundTripSendEditClearAndDifference(t *testing.T) {
	ctx := context.Background()
	authKeyID := [8]byte{9, 7, 1}
	userStore := memory.NewUserStore()
	user, err := userStore.Create(ctx, domain.User{AccessHash: 92001, Phone: "15550092001", FirstName: "Author"})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	storyStore := memory.NewStoryStore()
	updateStore := memory.NewUpdateEventStore()
	r := New(Config{}, Deps{
		Users:   appusers.NewService(userStore),
		Stories: appstories.NewService(storyStore),
		Updates: appupdates.NewService(memory.NewUpdateStateStore(), updateStore),
		Files:   &fakeFiles{photos: map[int64]domain.Photo{}},
	}, zaptest.NewLogger(t), fixedClock{now: time.Unix(1700000520, 0)})
	reqCtx := WithSessionID(WithAuthKeyID(WithUserID(ctx, user.ID), authKeyID), 71)

	sendReq := &tg.StoriesSendStoryRequest{
		Peer:         &tg.InputPeerSelf{},
		Media:        &tg.InputMediaUploadedPhoto{File: &tg.InputFile{ID: 41, Parts: 1, Name: "story.jpg"}},
		PrivacyRules: []tg.InputPrivacyRuleClass{&tg.InputPrivacyValueAllowAll{}},
		RandomID:     7101,
		Period:       86400,
		Caption:      "area story",
	}
	sendReq.SetMediaAreas([]tg.MediaAreaClass{testStoryMediaAreaWith("🔥", 10, true, true)})
	updates, err := r.onStoriesSendStory(reqCtx, sendReq)
	if err != nil {
		t.Fatalf("send story with media area: %v", err)
	}
	sendUpdates := updates.(*tg.Updates)
	idUpdate := sendUpdates.Updates[0].(*tg.UpdateStoryID)
	sentItem := sendUpdates.Updates[1].(*tg.UpdateStory).Story.(*tg.StoryItem)
	assertStorySuggestedReactionArea(t, sentItem, "🔥", 10, true, true)

	peerStories, err := r.onStoriesGetPeerStories(reqCtx, &tg.InputPeerSelf{})
	if err != nil {
		t.Fatalf("get peer stories: %v", err)
	}
	if len(peerStories.Stories.Stories) != 1 {
		t.Fatalf("peer stories = %+v, want one story", peerStories.Stories.Stories)
	}
	assertStorySuggestedReactionArea(t, peerStories.Stories.Stories[0].(*tg.StoryItem), "🔥", 10, true, true)

	byID, err := r.onStoriesGetStoriesByID(reqCtx, &tg.StoriesGetStoriesByIDRequest{
		Peer: &tg.InputPeerSelf{},
		ID:   []int{idUpdate.ID, idUpdate.ID},
	})
	if err != nil {
		t.Fatalf("get stories by id: %v", err)
	}
	if len(byID.Stories) != 1 {
		t.Fatalf("stories by duplicate ids = %+v, want one story", byID.Stories)
	}
	assertStorySuggestedReactionArea(t, byID.Stories[0].(*tg.StoryItem), "🔥", 10, true, true)
	if _, err := r.onStoriesGetStoriesByID(reqCtx, &tg.StoriesGetStoriesByIDRequest{
		Peer: &tg.InputPeerSelf{},
	}); err == nil || !tgerr.Is(err, "STORY_ID_EMPTY") {
		t.Fatalf("empty get stories by id err = %v, want STORY_ID_EMPTY", err)
	}
	if _, err := r.onStoriesGetStoriesByID(reqCtx, &tg.StoriesGetStoriesByIDRequest{
		Peer: &tg.InputPeerEmpty{},
	}); err == nil || !tgerr.Is(err, "STORY_ID_EMPTY") {
		t.Fatalf("empty get stories by id before peer err = %v, want STORY_ID_EMPTY", err)
	}
	if _, err := r.onStoriesGetStoriesByID(reqCtx, &tg.StoriesGetStoriesByIDRequest{
		Peer: &tg.InputPeerEmpty{},
		ID:   []int{0},
	}); err == nil || !tgerr.Is(err, "STORY_ID_INVALID") {
		t.Fatalf("invalid get stories by id before peer err = %v, want STORY_ID_INVALID", err)
	}
	if _, err := r.onStoriesGetStoriesByID(reqCtx, &tg.StoriesGetStoriesByIDRequest{
		Peer: &tg.InputPeerEmpty{},
		ID:   make([]int, domain.MaxStoryIDs+1),
	}); err == nil || !tgerr.Is(err, "STORY_ID_INVALID") {
		t.Fatalf("oversized get stories by id before peer err = %v, want STORY_ID_INVALID", err)
	}

	edit := &tg.StoriesEditStoryRequest{Peer: &tg.InputPeerSelf{}, ID: idUpdate.ID}
	edit.SetMediaAreas([]tg.MediaAreaClass{testStoryURLMediaArea("https://example.com/story/link", 25)})
	updates, err = r.onStoriesEditStory(reqCtx, edit)
	if err != nil {
		t.Fatalf("edit story media area: %v", err)
	}
	editedItem := updates.(*tg.Updates).Updates[0].(*tg.UpdateStory).Story.(*tg.StoryItem)
	if !editedItem.Edited {
		t.Fatalf("edited item = %+v, want edited flag", editedItem)
	}
	assertStoryURLMediaArea(t, editedItem, "https://example.com/story/link", 25)

	diff, err := r.onUpdatesGetDifference(reqCtx, &tg.UpdatesGetDifferenceRequest{Pts: 0})
	if err != nil {
		t.Fatalf("get difference after media area edit: %v", err)
	}
	gotDiff := diff.(*tg.UpdatesDifference)
	if gotDiff.State.Pts != 2 || len(gotDiff.OtherUpdates) != 2 {
		t.Fatalf("difference = pts %d updates %d, want send+edit", gotDiff.State.Pts, len(gotDiff.OtherUpdates))
	}
	diffEdited := gotDiff.OtherUpdates[1].(*tg.UpdateStory).Story.(*tg.StoryItem)
	assertStoryURLMediaArea(t, diffEdited, "https://example.com/story/link", 25)

	geoEdit := &tg.StoriesEditStoryRequest{Peer: &tg.InputPeerSelf{}, ID: idUpdate.ID}
	geoEdit.SetMediaAreas([]tg.MediaAreaClass{testStoryGeoPointMediaArea(31.2304, 121.4737, 35)})
	updates, err = r.onStoriesEditStory(reqCtx, geoEdit)
	if err != nil {
		t.Fatalf("edit story geo media area: %v", err)
	}
	geoItem := updates.(*tg.Updates).Updates[0].(*tg.UpdateStory).Story.(*tg.StoryItem)
	assertStoryGeoPointMediaArea(t, geoItem, 31.2304, 121.4737, 35)

	diff, err = r.onUpdatesGetDifference(reqCtx, &tg.UpdatesGetDifferenceRequest{Pts: 0})
	if err != nil {
		t.Fatalf("get difference after geo media area edit: %v", err)
	}
	gotDiff = diff.(*tg.UpdatesDifference)
	if gotDiff.State.Pts != 3 || len(gotDiff.OtherUpdates) != 3 {
		t.Fatalf("difference after geo edit = pts %d updates %d, want send+url+geo edits", gotDiff.State.Pts, len(gotDiff.OtherUpdates))
	}
	diffGeo := gotDiff.OtherUpdates[2].(*tg.UpdateStory).Story.(*tg.StoryItem)
	assertStoryGeoPointMediaArea(t, diffGeo, 31.2304, 121.4737, 35)

	weatherEdit := &tg.StoriesEditStoryRequest{Peer: &tg.InputPeerSelf{}, ID: idUpdate.ID}
	weatherEdit.SetMediaAreas([]tg.MediaAreaClass{testStoryWeatherMediaArea("☀️", 22.5, 0x00cc6600, 45)})
	updates, err = r.onStoriesEditStory(reqCtx, weatherEdit)
	if err != nil {
		t.Fatalf("edit story weather media area: %v", err)
	}
	weatherItem := updates.(*tg.Updates).Updates[0].(*tg.UpdateStory).Story.(*tg.StoryItem)
	assertStoryWeatherMediaArea(t, weatherItem, "☀️", 22.5, 0x00cc6600, 45)

	diff, err = r.onUpdatesGetDifference(reqCtx, &tg.UpdatesGetDifferenceRequest{Pts: 0})
	if err != nil {
		t.Fatalf("get difference after weather media area edit: %v", err)
	}
	gotDiff = diff.(*tg.UpdatesDifference)
	if gotDiff.State.Pts != 4 || len(gotDiff.OtherUpdates) != 4 {
		t.Fatalf("difference after weather edit = pts %d updates %d, want send+url+geo+weather edits", gotDiff.State.Pts, len(gotDiff.OtherUpdates))
	}
	diffWeather := gotDiff.OtherUpdates[3].(*tg.UpdateStory).Story.(*tg.StoryItem)
	assertStoryWeatherMediaArea(t, diffWeather, "☀️", 22.5, 0x00cc6600, 45)

	storyPeer := domain.Peer{Type: domain.PeerTypeUser, ID: user.ID}
	queryID, _ := r.inlines.register(r.clock.Now(), 90001, user.ID, storyPeer)
	if !r.inlines.resolve(r.clock.Now(), 90001, queryID, domain.BotInlineResults{
		BotUserID: 90001,
		UserID:    user.ID,
		Peer:      storyPeer,
		Results:   []domain.BotInlineResult{testStoryInlineVenueResult("venue-result", "Inline Cafe", 31.231, 121.474)},
	}) {
		t.Fatalf("resolve inline venue result")
	}
	inputVenueEdit := &tg.StoriesEditStoryRequest{Peer: &tg.InputPeerSelf{}, ID: idUpdate.ID}
	inputVenueEdit.SetMediaAreas([]tg.MediaAreaClass{testStoryInputVenueMediaArea(queryID, "venue-result", 55)})
	updates, err = r.onStoriesEditStory(reqCtx, inputVenueEdit)
	if err != nil {
		t.Fatalf("edit story input venue media area: %v", err)
	}
	inputVenueItem := updates.(*tg.Updates).Updates[0].(*tg.UpdateStory).Story.(*tg.StoryItem)
	assertStoryVenueMediaArea(t, inputVenueItem, "Inline Cafe", 31.231, 121.474, 55)

	concreteVenueEdit := &tg.StoriesEditStoryRequest{Peer: &tg.InputPeerSelf{}, ID: idUpdate.ID}
	concreteVenueEdit.SetMediaAreas([]tg.MediaAreaClass{testStoryVenueMediaArea("Gallery", 31.232, 121.475, 65)})
	updates, err = r.onStoriesEditStory(reqCtx, concreteVenueEdit)
	if err != nil {
		t.Fatalf("edit story concrete venue media area: %v", err)
	}
	concreteVenueItem := updates.(*tg.Updates).Updates[0].(*tg.UpdateStory).Story.(*tg.StoryItem)
	assertStoryVenueMediaArea(t, concreteVenueItem, "Gallery", 31.232, 121.475, 65)

	diff, err = r.onUpdatesGetDifference(reqCtx, &tg.UpdatesGetDifferenceRequest{Pts: 0})
	if err != nil {
		t.Fatalf("get difference after venue media area edits: %v", err)
	}
	gotDiff = diff.(*tg.UpdatesDifference)
	if gotDiff.State.Pts != 6 || len(gotDiff.OtherUpdates) != 6 {
		t.Fatalf("difference after venue edits = pts %d updates %d, want send+url+geo+weather+venue edits", gotDiff.State.Pts, len(gotDiff.OtherUpdates))
	}
	diffInputVenue := gotDiff.OtherUpdates[4].(*tg.UpdateStory).Story.(*tg.StoryItem)
	assertStoryVenueMediaArea(t, diffInputVenue, "Inline Cafe", 31.231, 121.474, 55)
	diffConcreteVenue := gotDiff.OtherUpdates[5].(*tg.UpdateStory).Story.(*tg.StoryItem)
	assertStoryVenueMediaArea(t, diffConcreteVenue, "Gallery", 31.232, 121.475, 65)

	channelPostEdit := &tg.StoriesEditStoryRequest{Peer: &tg.InputPeerSelf{}, ID: idUpdate.ID}
	channelPostEdit.SetMediaAreas([]tg.MediaAreaClass{testStoryChannelPostMediaArea(777001, 42, 75)})
	updates, err = r.onStoriesEditStory(reqCtx, channelPostEdit)
	if err != nil {
		t.Fatalf("edit story channel post media area: %v", err)
	}
	channelPostItem := updates.(*tg.Updates).Updates[0].(*tg.UpdateStory).Story.(*tg.StoryItem)
	assertStoryChannelPostMediaArea(t, channelPostItem, 777001, 42, 75)

	diff, err = r.onUpdatesGetDifference(reqCtx, &tg.UpdatesGetDifferenceRequest{Pts: 0})
	if err != nil {
		t.Fatalf("get difference after channel post media area edit: %v", err)
	}
	gotDiff = diff.(*tg.UpdatesDifference)
	if gotDiff.State.Pts != 7 || len(gotDiff.OtherUpdates) != 7 {
		t.Fatalf("difference after channel post edit = pts %d updates %d, want seven story updates", gotDiff.State.Pts, len(gotDiff.OtherUpdates))
	}
	diffChannelPost := gotDiff.OtherUpdates[6].(*tg.UpdateStory).Story.(*tg.StoryItem)
	assertStoryChannelPostMediaArea(t, diffChannelPost, 777001, 42, 75)

	starGiftEdit := &tg.StoriesEditStoryRequest{Peer: &tg.InputPeerSelf{}, ID: idUpdate.ID}
	starGiftEdit.SetMediaAreas([]tg.MediaAreaClass{testStoryStarGiftMediaArea("Gift.Series_01-42", 85)})
	updates, err = r.onStoriesEditStory(reqCtx, starGiftEdit)
	if err != nil {
		t.Fatalf("edit story star gift media area: %v", err)
	}
	starGiftItem := updates.(*tg.Updates).Updates[0].(*tg.UpdateStory).Story.(*tg.StoryItem)
	assertStoryStarGiftMediaArea(t, starGiftItem, "Gift.Series_01-42", 85)

	diff, err = r.onUpdatesGetDifference(reqCtx, &tg.UpdatesGetDifferenceRequest{Pts: 0})
	if err != nil {
		t.Fatalf("get difference after star gift media area edit: %v", err)
	}
	gotDiff = diff.(*tg.UpdatesDifference)
	if gotDiff.State.Pts != 8 || len(gotDiff.OtherUpdates) != 8 {
		t.Fatalf("difference after star gift edit = pts %d updates %d, want eight story updates", gotDiff.State.Pts, len(gotDiff.OtherUpdates))
	}
	diffStarGift := gotDiff.OtherUpdates[7].(*tg.UpdateStory).Story.(*tg.StoryItem)
	assertStoryStarGiftMediaArea(t, diffStarGift, "Gift.Series_01-42", 85)

	clear := &tg.StoriesEditStoryRequest{Peer: &tg.InputPeerSelf{}, ID: idUpdate.ID}
	clear.SetMediaAreas([]tg.MediaAreaClass{})
	updates, err = r.onStoriesEditStory(reqCtx, clear)
	if err != nil {
		t.Fatalf("clear story media areas: %v", err)
	}
	clearedItem := updates.(*tg.Updates).Updates[0].(*tg.UpdateStory).Story.(*tg.StoryItem)
	assertStoryHasNoMediaAreas(t, clearedItem)

	diff, err = r.onUpdatesGetDifference(reqCtx, &tg.UpdatesGetDifferenceRequest{Pts: 0})
	if err != nil {
		t.Fatalf("get difference after media area clear: %v", err)
	}
	gotDiff = diff.(*tg.UpdatesDifference)
	if gotDiff.State.Pts != 9 || len(gotDiff.OtherUpdates) != 9 {
		t.Fatalf("difference after clear = pts %d updates %d, want nine story updates", gotDiff.State.Pts, len(gotDiff.OtherUpdates))
	}
	diffCleared := gotDiff.OtherUpdates[8].(*tg.UpdateStory).Story.(*tg.StoryItem)
	assertStoryHasNoMediaAreas(t, diffCleared)
}

func TestStoriesInputChannelPostMediaAreaResolvesToConcrete(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	user, err := userStore.Create(ctx, domain.User{AccessHash: 92006, Phone: "15550092006", FirstName: "Author"})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	channelStore := memory.NewChannelStore()
	channelService := appchannels.NewService(channelStore)
	created, err := channelService.CreateChannel(ctx, user.ID, domain.CreateChannelRequest{
		CreatorUserID: user.ID,
		Title:         "story channel post source",
		Broadcast:     true,
		Date:          1700000521,
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	sent, err := channelService.SendMessage(ctx, user.ID, domain.SendChannelMessageRequest{
		ChannelID: created.Channel.ID,
		RandomID:  8101,
		Message:   "source post",
		Date:      1700000522,
	})
	if err != nil {
		t.Fatalf("send channel message: %v", err)
	}
	storyStore := memory.NewStoryStore()
	r := New(Config{}, Deps{
		Users:    appusers.NewService(userStore),
		Channels: channelService,
		Stories:  appstories.NewService(storyStore),
		Files:    &fakeFiles{photos: map[int64]domain.Photo{}},
	}, zaptest.NewLogger(t), fixedClock{now: time.Unix(1700000523, 0)})
	reqCtx := WithUserID(ctx, user.ID)

	sendReq := &tg.StoriesSendStoryRequest{
		Peer:         &tg.InputPeerSelf{},
		Media:        &tg.InputMediaUploadedPhoto{File: &tg.InputFile{ID: 8102, Parts: 1, Name: "story.jpg"}},
		PrivacyRules: []tg.InputPrivacyRuleClass{&tg.InputPrivacyValueAllowAll{}},
		RandomID:     8102,
		Period:       86400,
	}
	sendReq.SetMediaAreas([]tg.MediaAreaClass{testStoryInputChannelPostMediaArea(created.Channel.ID, created.Channel.AccessHash, sent.Message.ID, 35)})
	updates, err := r.onStoriesSendStory(reqCtx, sendReq)
	if err != nil {
		t.Fatalf("send story with input channel post media area: %v", err)
	}
	item := updates.(*tg.Updates).Updates[1].(*tg.UpdateStory).Story.(*tg.StoryItem)
	assertStoryChannelPostMediaArea(t, item, created.Channel.ID, sent.Message.ID, 35)

	peerStories, err := r.onStoriesGetPeerStories(reqCtx, &tg.InputPeerSelf{})
	if err != nil {
		t.Fatalf("get peer stories: %v", err)
	}
	if len(peerStories.Stories.Stories) != 1 {
		t.Fatalf("peer stories = %+v, want one story", peerStories.Stories.Stories)
	}
	assertStoryChannelPostMediaArea(t, peerStories.Stories.Stories[0].(*tg.StoryItem), created.Channel.ID, sent.Message.ID, 35)

	badHash := &tg.StoriesSendStoryRequest{
		Peer:         &tg.InputPeerSelf{},
		Media:        &tg.InputMediaUploadedPhoto{File: &tg.InputFile{ID: 8103, Parts: 1, Name: "story.jpg"}},
		PrivacyRules: []tg.InputPrivacyRuleClass{&tg.InputPrivacyValueAllowAll{}},
		RandomID:     8103,
		Period:       86400,
	}
	badHash.SetMediaAreas([]tg.MediaAreaClass{testStoryInputChannelPostMediaArea(created.Channel.ID, created.Channel.AccessHash+1, sent.Message.ID, 35)})
	if _, err := r.onStoriesSendStory(reqCtx, badHash); err == nil || !tgerr.Is(err, "CHANNEL_PRIVATE") {
		t.Fatalf("send story input channel post bad hash err = %v, want CHANNEL_PRIVATE", err)
	}

	missingMessage := &tg.StoriesSendStoryRequest{
		Peer:         &tg.InputPeerSelf{},
		Media:        &tg.InputMediaUploadedPhoto{File: &tg.InputFile{ID: 8104, Parts: 1, Name: "story.jpg"}},
		PrivacyRules: []tg.InputPrivacyRuleClass{&tg.InputPrivacyValueAllowAll{}},
		RandomID:     8104,
		Period:       86400,
	}
	missingMessage.SetMediaAreas([]tg.MediaAreaClass{testStoryInputChannelPostMediaArea(created.Channel.ID, created.Channel.AccessHash, sent.Message.ID+100, 35)})
	if _, err := r.onStoriesSendStory(reqCtx, missingMessage); err == nil || !tgerr.Is(err, "MEDIA_INVALID") {
		t.Fatalf("send story input channel post missing message err = %v, want MEDIA_INVALID", err)
	}

	emptyInput := &tg.StoriesSendStoryRequest{
		Peer:         &tg.InputPeerSelf{},
		Media:        &tg.InputMediaUploadedPhoto{File: &tg.InputFile{ID: 8105, Parts: 1, Name: "story.jpg"}},
		PrivacyRules: []tg.InputPrivacyRuleClass{&tg.InputPrivacyValueAllowAll{}},
		RandomID:     8105,
		Period:       86400,
	}
	emptyInput.SetMediaAreas([]tg.MediaAreaClass{&tg.InputMediaAreaChannelPost{Coordinates: tg.MediaAreaCoordinates{X: 35, Y: 12, W: 22, H: 11}}})
	if _, err := r.onStoriesSendStory(reqCtx, emptyInput); err == nil || !tgerr.Is(err, "MEDIA_INVALID") {
		t.Fatalf("send story empty input channel post err = %v, want MEDIA_INVALID", err)
	}

	peerStories, err = r.onStoriesGetPeerStories(reqCtx, &tg.InputPeerSelf{})
	if err != nil {
		t.Fatalf("get peer stories after rejected channel posts: %v", err)
	}
	if len(peerStories.Stories.Stories) != 1 {
		t.Fatalf("stories after rejected channel posts = %+v, want only original story", peerStories.Stories.Stories)
	}
}

func TestStoriesMediaAreasRejectInvalidInputWithoutMutation(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	user, err := userStore.Create(ctx, domain.User{AccessHash: 92011, Phone: "15550092011", FirstName: "Author"})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	storyStore := memory.NewStoryStore()
	r := New(Config{}, Deps{
		Users:   appusers.NewService(userStore),
		Stories: appstories.NewService(storyStore),
		Files:   &fakeFiles{photos: map[int64]domain.Photo{}},
	}, zaptest.NewLogger(t), fixedClock{now: time.Unix(1700000530, 0)})
	reqCtx := WithUserID(ctx, user.ID)

	tooMany := make([]tg.MediaAreaClass, domain.MaxStoryMediaAreas+1)
	for i := range tooMany {
		tooMany[i] = testStoryMediaAreaWith("🔥", float64(i), false, false)
	}
	tooManyReq := &tg.StoriesSendStoryRequest{
		Peer:         &tg.InputPeerSelf{},
		Media:        &tg.InputMediaUploadedPhoto{File: &tg.InputFile{ID: 42, Parts: 1, Name: "story.jpg"}},
		PrivacyRules: []tg.InputPrivacyRuleClass{&tg.InputPrivacyValueAllowAll{}},
		RandomID:     7111,
		Period:       86400,
	}
	tooManyReq.SetMediaAreas(tooMany)
	if _, err := r.onStoriesSendStory(reqCtx, tooManyReq); err == nil || !strings.Contains(err.Error(), "LIMIT_INVALID") {
		t.Fatalf("send too many media areas err = %v, want LIMIT_INVALID", err)
	}
	stories, err := r.onStoriesGetPeerStories(reqCtx, &tg.InputPeerSelf{})
	if err != nil {
		t.Fatalf("get peer stories after rejected send: %v", err)
	}
	if len(stories.Stories.Stories) != 0 {
		t.Fatalf("stories after rejected send = %+v, want none", stories.Stories.Stories)
	}
	for i, area := range testNilStoryMediaAreas() {
		req := &tg.StoriesSendStoryRequest{
			Peer:         &tg.InputPeerSelf{},
			Media:        &tg.InputMediaUploadedPhoto{File: &tg.InputFile{ID: int64(7140 + i), Parts: 1, Name: "story.jpg"}},
			PrivacyRules: []tg.InputPrivacyRuleClass{&tg.InputPrivacyValueAllowAll{}},
			RandomID:     int64(7140 + i),
			Period:       86400,
		}
		req.SetMediaAreas([]tg.MediaAreaClass{area})
		if _, err := r.onStoriesSendStory(reqCtx, req); err == nil || !strings.Contains(err.Error(), "MEDIA_INVALID") {
			t.Fatalf("send nil media area[%d] err = %v, want MEDIA_INVALID", i, err)
		}
	}
	stories, err = r.onStoriesGetPeerStories(reqCtx, &tg.InputPeerSelf{})
	if err != nil {
		t.Fatalf("get peer stories after rejected nil media areas: %v", err)
	}
	if len(stories.Stories.Stories) != 0 {
		t.Fatalf("stories after rejected nil media areas = %+v, want none", stories.Stories.Stories)
	}
	badURLReq := func(randomID int64, url string) *tg.StoriesSendStoryRequest {
		req := &tg.StoriesSendStoryRequest{
			Peer:         &tg.InputPeerSelf{},
			Media:        &tg.InputMediaUploadedPhoto{File: &tg.InputFile{ID: randomID, Parts: 1, Name: "story.jpg"}},
			PrivacyRules: []tg.InputPrivacyRuleClass{&tg.InputPrivacyValueAllowAll{}},
			RandomID:     randomID,
			Period:       86400,
		}
		req.SetMediaAreas([]tg.MediaAreaClass{testStoryURLMediaArea(url, 10)})
		return req
	}
	for _, tc := range []struct {
		name string
		req  *tg.StoriesSendStoryRequest
	}{
		{name: "empty url", req: badURLReq(7113, "")},
		{name: "trimmed url", req: badURLReq(7114, " https://example.com ")},
		{name: "long url", req: badURLReq(7115, "https://example.com/"+strings.Repeat("x", domain.MaxStoryMediaAreaURLLength))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := r.onStoriesSendStory(reqCtx, tc.req); err == nil || !strings.Contains(err.Error(), "MEDIA_INVALID") {
				t.Fatalf("send story invalid url err = %v, want MEDIA_INVALID", err)
			}
		})
	}
	stories, err = r.onStoriesGetPeerStories(reqCtx, &tg.InputPeerSelf{})
	if err != nil {
		t.Fatalf("get peer stories after rejected url sends: %v", err)
	}
	if len(stories.Stories.Stories) != 0 {
		t.Fatalf("stories after rejected url sends = %+v, want none", stories.Stories.Stories)
	}
	badGeoReq := func(randomID int64, area tg.MediaAreaClass) *tg.StoriesSendStoryRequest {
		req := &tg.StoriesSendStoryRequest{
			Peer:         &tg.InputPeerSelf{},
			Media:        &tg.InputMediaUploadedPhoto{File: &tg.InputFile{ID: randomID, Parts: 1, Name: "story.jpg"}},
			PrivacyRules: []tg.InputPrivacyRuleClass{&tg.InputPrivacyValueAllowAll{}},
			RandomID:     randomID,
			Period:       86400,
		}
		req.SetMediaAreas([]tg.MediaAreaClass{area})
		return req
	}
	badAddress := testStoryGeoPointMediaArea(31.2, 121.4, 10).(*tg.MediaAreaGeoPoint)
	badAddress.SetAddress(tg.GeoPointAddress{CountryISO2: "C", City: strings.Repeat("x", domain.MaxStoryGeoAddressPartLength+1)})
	for _, tc := range []struct {
		name string
		req  *tg.StoriesSendStoryRequest
	}{
		{name: "geo empty", req: badGeoReq(7116, &tg.MediaAreaGeoPoint{Coordinates: tg.MediaAreaCoordinates{X: 10, Y: 10, W: 10, H: 10}, Geo: &tg.GeoPointEmpty{}})},
		{name: "geo out of range", req: badGeoReq(7117, testStoryGeoPointMediaArea(91, 0, 10))},
		{name: "geo bad address", req: badGeoReq(7118, badAddress)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := r.onStoriesSendStory(reqCtx, tc.req); err == nil || !strings.Contains(err.Error(), "MEDIA_INVALID") {
				t.Fatalf("send story invalid geo err = %v, want MEDIA_INVALID", err)
			}
		})
	}
	stories, err = r.onStoriesGetPeerStories(reqCtx, &tg.InputPeerSelf{})
	if err != nil {
		t.Fatalf("get peer stories after rejected geo sends: %v", err)
	}
	if len(stories.Stories.Stories) != 0 {
		t.Fatalf("stories after rejected geo sends = %+v, want none", stories.Stories.Stories)
	}
	badWeatherReq := func(randomID int64, area tg.MediaAreaClass) *tg.StoriesSendStoryRequest {
		req := &tg.StoriesSendStoryRequest{
			Peer:         &tg.InputPeerSelf{},
			Media:        &tg.InputMediaUploadedPhoto{File: &tg.InputFile{ID: randomID, Parts: 1, Name: "story.jpg"}},
			PrivacyRules: []tg.InputPrivacyRuleClass{&tg.InputPrivacyValueAllowAll{}},
			RandomID:     randomID,
			Period:       86400,
		}
		req.SetMediaAreas([]tg.MediaAreaClass{area})
		return req
	}
	for _, tc := range []struct {
		name string
		req  *tg.StoriesSendStoryRequest
	}{
		{name: "weather empty emoji", req: badWeatherReq(7119, testStoryWeatherMediaArea("", 22, 0x00cc6600, 10))},
		{name: "weather trimmed emoji", req: badWeatherReq(7120, testStoryWeatherMediaArea(" ☀️ ", 22, 0x00cc6600, 10))},
		{name: "weather long emoji", req: badWeatherReq(7121, testStoryWeatherMediaArea(strings.Repeat("☀", domain.MaxStoryWeatherEmojiLength+1), 22, 0x00cc6600, 10))},
		{name: "weather nan temperature", req: badWeatherReq(7122, testStoryWeatherMediaArea("☀️", math.NaN(), 0x00cc6600, 10))},
		{name: "weather too cold", req: badWeatherReq(7123, testStoryWeatherMediaArea("☀️", -275, 0x00cc6600, 10))},
		{name: "weather too hot", req: badWeatherReq(7124, testStoryWeatherMediaArea("☀️", 1000001, 0x00cc6600, 10))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := r.onStoriesSendStory(reqCtx, tc.req); err == nil || !strings.Contains(err.Error(), "MEDIA_INVALID") {
				t.Fatalf("send story invalid weather err = %v, want MEDIA_INVALID", err)
			}
		})
	}
	stories, err = r.onStoriesGetPeerStories(reqCtx, &tg.InputPeerSelf{})
	if err != nil {
		t.Fatalf("get peer stories after rejected weather sends: %v", err)
	}
	if len(stories.Stories.Stories) != 0 {
		t.Fatalf("stories after rejected weather sends = %+v, want none", stories.Stories.Stories)
	}
	badChannelPostReq := func(randomID int64, area tg.MediaAreaClass) *tg.StoriesSendStoryRequest {
		req := &tg.StoriesSendStoryRequest{
			Peer:         &tg.InputPeerSelf{},
			Media:        &tg.InputMediaUploadedPhoto{File: &tg.InputFile{ID: randomID, Parts: 1, Name: "story.jpg"}},
			PrivacyRules: []tg.InputPrivacyRuleClass{&tg.InputPrivacyValueAllowAll{}},
			RandomID:     randomID,
			Period:       86400,
		}
		req.SetMediaAreas([]tg.MediaAreaClass{area})
		return req
	}
	for _, tc := range []struct {
		name string
		req  *tg.StoriesSendStoryRequest
	}{
		{name: "channel post empty channel", req: badChannelPostReq(7130, testStoryChannelPostMediaArea(0, 1, 10))},
		{name: "channel post empty message", req: badChannelPostReq(7131, testStoryChannelPostMediaArea(1001, 0, 10))},
		{name: "channel post message overflow", req: badChannelPostReq(7132, testStoryChannelPostMediaArea(1001, domain.MaxMessageBoxID+1, 10))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := r.onStoriesSendStory(reqCtx, tc.req); err == nil || !strings.Contains(err.Error(), "MEDIA_INVALID") {
				t.Fatalf("send story invalid channel post err = %v, want MEDIA_INVALID", err)
			}
		})
	}
	stories, err = r.onStoriesGetPeerStories(reqCtx, &tg.InputPeerSelf{})
	if err != nil {
		t.Fatalf("get peer stories after rejected channel post sends: %v", err)
	}
	if len(stories.Stories.Stories) != 0 {
		t.Fatalf("stories after rejected channel post sends = %+v, want none", stories.Stories.Stories)
	}
	badStarGiftReq := func(randomID int64, slug string) *tg.StoriesSendStoryRequest {
		req := &tg.StoriesSendStoryRequest{
			Peer:         &tg.InputPeerSelf{},
			Media:        &tg.InputMediaUploadedPhoto{File: &tg.InputFile{ID: randomID, Parts: 1, Name: "story.jpg"}},
			PrivacyRules: []tg.InputPrivacyRuleClass{&tg.InputPrivacyValueAllowAll{}},
			RandomID:     randomID,
			Period:       86400,
		}
		req.SetMediaAreas([]tg.MediaAreaClass{testStoryStarGiftMediaArea(slug, 10)})
		return req
	}
	for _, tc := range []struct {
		name string
		req  *tg.StoriesSendStoryRequest
	}{
		{name: "star gift empty slug", req: badStarGiftReq(7133, "")},
		{name: "star gift trimmed slug", req: badStarGiftReq(7134, " GiftSlug ")},
		{name: "star gift long slug", req: badStarGiftReq(7135, strings.Repeat("x", domain.MaxStoryStarGiftSlugLength+1))},
		{name: "star gift query separator", req: badStarGiftReq(7136, "GiftSlug&bad=1")},
		{name: "star gift fragment separator", req: badStarGiftReq(7137, "GiftSlug#fragment")},
		{name: "star gift unicode", req: badStarGiftReq(7138, "礼物")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := r.onStoriesSendStory(reqCtx, tc.req); err == nil || !strings.Contains(err.Error(), "MEDIA_INVALID") {
				t.Fatalf("send story invalid star gift err = %v, want MEDIA_INVALID", err)
			}
		})
	}
	stories, err = r.onStoriesGetPeerStories(reqCtx, &tg.InputPeerSelf{})
	if err != nil {
		t.Fatalf("get peer stories after rejected star gift sends: %v", err)
	}
	if len(stories.Stories.Stories) != 0 {
		t.Fatalf("stories after rejected star gift sends = %+v, want none", stories.Stories.Stories)
	}
	badVenueReq := func(randomID int64, area tg.MediaAreaClass) *tg.StoriesSendStoryRequest {
		req := &tg.StoriesSendStoryRequest{
			Peer:         &tg.InputPeerSelf{},
			Media:        &tg.InputMediaUploadedPhoto{File: &tg.InputFile{ID: randomID, Parts: 1, Name: "story.jpg"}},
			PrivacyRules: []tg.InputPrivacyRuleClass{&tg.InputPrivacyValueAllowAll{}},
			RandomID:     randomID,
			Period:       86400,
		}
		req.SetMediaAreas([]tg.MediaAreaClass{area})
		return req
	}
	for _, tc := range []struct {
		name string
		req  *tg.StoriesSendStoryRequest
	}{
		{name: "venue empty title", req: badVenueReq(7125, testStoryVenueMediaArea("", 31.2, 121.4, 10))},
		{name: "venue out of range geo", req: badVenueReq(7126, testStoryVenueMediaArea("Cafe", 91, 121.4, 10))},
		{name: "venue long provider", req: badVenueReq(7127, &tg.MediaAreaVenue{
			Coordinates: tg.MediaAreaCoordinates{X: 10, Y: 10, W: 10, H: 10},
			Geo:         &tg.GeoPoint{Lat: 31.2, Long: 121.4, AccessHash: 123456},
			Title:       "Cafe",
			Provider:    strings.Repeat("p", maxVenueProviderLength+1),
		})},
		{name: "input venue unknown query", req: badVenueReq(7128, testStoryInputVenueMediaArea(99999, "venue-result", 10))},
		{name: "input venue empty result", req: badVenueReq(7129, testStoryInputVenueMediaArea(1, "", 10))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := r.onStoriesSendStory(reqCtx, tc.req); err == nil || !strings.Contains(err.Error(), "MEDIA_INVALID") {
				t.Fatalf("send story invalid venue err = %v, want MEDIA_INVALID", err)
			}
		})
	}
	stories, err = r.onStoriesGetPeerStories(reqCtx, &tg.InputPeerSelf{})
	if err != nil {
		t.Fatalf("get peer stories after rejected venue sends: %v", err)
	}
	if len(stories.Stories.Stories) != 0 {
		t.Fatalf("stories after rejected venue sends = %+v, want none", stories.Stories.Stories)
	}

	sendReq := &tg.StoriesSendStoryRequest{
		Peer:         &tg.InputPeerSelf{},
		Media:        &tg.InputMediaUploadedPhoto{File: &tg.InputFile{ID: 43, Parts: 1, Name: "story.jpg"}},
		PrivacyRules: []tg.InputPrivacyRuleClass{&tg.InputPrivacyValueAllowAll{}},
		RandomID:     7112,
		Period:       86400,
	}
	sendReq.SetMediaAreas([]tg.MediaAreaClass{testStoryMediaAreaWith("🔥", 10, false, false)})
	updates, err := r.onStoriesSendStory(reqCtx, sendReq)
	if err != nil {
		t.Fatalf("send valid media area story: %v", err)
	}
	idUpdate := updates.(*tg.Updates).Updates[0].(*tg.UpdateStoryID)

	customReaction := &tg.StoriesEditStoryRequest{Peer: &tg.InputPeerSelf{}, ID: idUpdate.ID}
	customReaction.SetMediaAreas([]tg.MediaAreaClass{&tg.MediaAreaSuggestedReaction{
		Coordinates: tg.MediaAreaCoordinates{X: 10, Y: 10, W: 10, H: 10},
		Reaction:    &tg.ReactionCustomEmoji{DocumentID: 12345},
	}})
	customUpdates, err := r.onStoriesEditStory(reqCtx, customReaction)
	if err != nil {
		t.Fatalf("custom reaction media area edit: %v", err)
	}
	assertStorySuggestedCustomReactionArea(t, customUpdates.(*tg.Updates).Updates[0].(*tg.UpdateStory).Story.(*tg.StoryItem), clientDocumentIDFromServerID(12345), 10, false, false)

	invalidCoordinates := &tg.StoriesEditStoryRequest{Peer: &tg.InputPeerSelf{}, ID: idUpdate.ID}
	invalidCoordinates.SetMediaAreas([]tg.MediaAreaClass{&tg.MediaAreaSuggestedReaction{
		Coordinates: tg.MediaAreaCoordinates{X: 10, Y: 10, W: 0, H: 10},
		Reaction:    &tg.ReactionEmoji{Emoticon: "👍"},
	}})
	if _, err := r.onStoriesEditStory(reqCtx, invalidCoordinates); err == nil || !strings.Contains(err.Error(), "MEDIA_INVALID") {
		t.Fatalf("invalid coordinates media area edit err = %v, want MEDIA_INVALID", err)
	}
	for i, area := range testNilStoryMediaAreas() {
		nilAreaEdit := &tg.StoriesEditStoryRequest{Peer: &tg.InputPeerSelf{}, ID: idUpdate.ID}
		nilAreaEdit.SetMediaAreas([]tg.MediaAreaClass{area})
		if _, err := r.onStoriesEditStory(reqCtx, nilAreaEdit); err == nil || !strings.Contains(err.Error(), "MEDIA_INVALID") {
			t.Fatalf("nil media area edit[%d] err = %v, want MEDIA_INVALID", i, err)
		}
	}

	stories, err = r.onStoriesGetPeerStories(reqCtx, &tg.InputPeerSelf{})
	if err != nil {
		t.Fatalf("get peer stories after rejected edits: %v", err)
	}
	if len(stories.Stories.Stories) != 1 {
		t.Fatalf("stories after rejected edits = %+v, want one original story", stories.Stories.Stories)
	}
	assertStorySuggestedCustomReactionArea(t, stories.Stories.Stories[0].(*tg.StoryItem), clientDocumentIDFromServerID(12345), 10, false, false)
}

func TestStoriesSendStoryAcceptsEmptyOptionalNoops(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	user, err := userStore.Create(ctx, domain.User{AccessHash: 92051, Phone: "15550092051", FirstName: "Author"})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	storyStore := memory.NewStoryStore()
	r := New(Config{}, Deps{
		Users:   appusers.NewService(userStore),
		Stories: appstories.NewService(storyStore),
		Files:   &fakeFiles{photos: map[int64]domain.Photo{}},
	}, zaptest.NewLogger(t), fixedClock{now: time.Unix(1700000525, 0)})
	reqCtx := WithUserID(ctx, user.ID)
	req := &tg.StoriesSendStoryRequest{
		Peer:         &tg.InputPeerSelf{},
		Media:        &tg.InputMediaUploadedPhoto{File: &tg.InputFile{ID: 7210, Parts: 1, Name: "story.jpg"}},
		PrivacyRules: []tg.InputPrivacyRuleClass{&tg.InputPrivacyValueAllowAll{}},
		RandomID:     7210,
		Period:       86400,
		Caption:      "empty optionals",
	}
	req.SetAlbums([]int{})
	req.SetMusic(&tg.InputDocumentEmpty{})

	updates, err := r.onStoriesSendStory(reqCtx, req)
	if err != nil {
		t.Fatalf("send story with empty optional noops: %v", err)
	}
	got, ok := updates.(*tg.Updates)
	if !ok || len(got.Updates) != 2 {
		t.Fatalf("updates = %T %+v, want updateStoryID + updateStory", updates, updates)
	}
	storyUpdate, ok := got.Updates[1].(*tg.UpdateStory)
	if !ok {
		t.Fatalf("second update = %T, want updateStory", got.Updates[1])
	}
	item, ok := storyUpdate.Story.(*tg.StoryItem)
	if !ok {
		t.Fatalf("story update item = %T, want storyItem", storyUpdate.Story)
	}
	if albums, ok := item.GetAlbums(); ok || len(albums) != 0 {
		t.Fatalf("story albums = %v ok=%v, want absent", albums, ok)
	}
	if music, ok := item.GetMusic(); ok || music != nil {
		t.Fatalf("story music = %T ok=%v, want absent", music, ok)
	}

	stories, err := r.onStoriesGetPeerStories(reqCtx, &tg.InputPeerSelf{})
	if err != nil {
		t.Fatalf("get peer stories after empty optional noops: %v", err)
	}
	if len(stories.Stories.Stories) != 1 {
		t.Fatalf("peer stories = %+v, want one story", stories.Stories.Stories)
	}
	stored := stories.Stories.Stories[0].(*tg.StoryItem)
	if albums, ok := stored.GetAlbums(); ok || len(albums) != 0 {
		t.Fatalf("stored story albums = %v ok=%v, want absent", albums, ok)
	}
	if music, ok := stored.GetMusic(); ok || music != nil {
		t.Fatalf("stored story music = %T ok=%v, want absent", music, ok)
	}
}

func TestStoriesSendStoryValidatesPeriodBeforeMutation(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	user, err := userStore.Create(ctx, domain.User{AccessHash: 92061, Phone: "15550092061", FirstName: "Author"})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	storyStore := memory.NewStoryStore()
	r := New(Config{}, Deps{
		Users:   appusers.NewService(userStore),
		Stories: appstories.NewService(storyStore),
		Files:   &fakeFiles{photos: map[int64]domain.Photo{}},
	}, zaptest.NewLogger(t), fixedClock{now: time.Unix(1700000526, 0)})
	reqCtx := WithUserID(ctx, user.ID)
	cases := []int{-1, 1, 5 * 3600, domain.DefaultStoryPeriod + 1, 3 * domain.DefaultStoryPeriod}
	for _, period := range cases {
		req := &tg.StoriesSendStoryRequest{
			Peer:         &tg.InputPeerSelf{},
			Media:        &tg.InputMediaUploadedPhoto{File: &tg.InputFile{ID: int64(7300 + period), Parts: 1, Name: "story.jpg"}},
			PrivacyRules: []tg.InputPrivacyRuleClass{&tg.InputPrivacyValueAllowAll{}},
			RandomID:     int64(7300 + period),
		}
		req.SetPeriod(period)
		if _, err := r.onStoriesSendStory(reqCtx, req); err == nil || !strings.Contains(err.Error(), "STORY_PERIOD_INVALID") {
			t.Fatalf("send story period %d err = %v, want STORY_PERIOD_INVALID", period, err)
		}
	}
	stories, err := r.onStoriesGetPeerStories(reqCtx, &tg.InputPeerSelf{})
	if err != nil {
		t.Fatalf("get peer stories after rejected periods: %v", err)
	}
	if len(stories.Stories.Stories) != 0 {
		t.Fatalf("stories after rejected periods = %+v, want none persisted", stories.Stories.Stories)
	}
}

func TestStoriesSendStoryUsesDefaultPeriodWhenFlagAbsent(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	user, err := userStore.Create(ctx, domain.User{AccessHash: 92071, Phone: "15550092071", FirstName: "Author"})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	storyStore := memory.NewStoryStore()
	now := time.Unix(1700000527, 0)
	r := New(Config{}, Deps{
		Users:   appusers.NewService(userStore),
		Stories: appstories.NewService(storyStore),
		Files:   &fakeFiles{photos: map[int64]domain.Photo{}},
	}, zaptest.NewLogger(t), fixedClock{now: now})
	reqCtx := WithUserID(ctx, user.ID)
	updates, err := r.onStoriesSendStory(reqCtx, &tg.StoriesSendStoryRequest{
		Peer:         &tg.InputPeerSelf{},
		Media:        &tg.InputMediaUploadedPhoto{File: &tg.InputFile{ID: 7310, Parts: 1, Name: "story.jpg"}},
		PrivacyRules: []tg.InputPrivacyRuleClass{&tg.InputPrivacyValueAllowAll{}},
		RandomID:     7310,
		Period:       -1,
		Caption:      "default period",
	})
	if err != nil {
		t.Fatalf("send story without period flag: %v", err)
	}
	got := updates.(*tg.Updates)
	item, ok := got.Updates[1].(*tg.UpdateStory).Story.(*tg.StoryItem)
	if !ok {
		t.Fatalf("story update item = %T, want storyItem", got.Updates[1].(*tg.UpdateStory).Story)
	}
	if want := int(now.Unix()) + domain.DefaultStoryPeriod; item.ExpireDate != want {
		t.Fatalf("story expire_date = %d, want %d", item.ExpireDate, want)
	}
}

func TestStoriesCaptionEntitiesRejectMalformedInputsBeforePeerAndMutation(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	user, err := userStore.Create(ctx, domain.User{AccessHash: 92081, Phone: "15550092081", FirstName: "Author"})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	storyStore := memory.NewStoryStore()
	r := New(Config{}, Deps{
		Users:   appusers.NewService(userStore),
		Stories: appstories.NewService(storyStore),
		Files:   &fakeFiles{photos: map[int64]domain.Photo{}},
	}, zaptest.NewLogger(t), fixedClock{now: time.Unix(1700000528, 0)})
	reqCtx := WithUserID(ctx, user.ID)

	validReq := &tg.StoriesSendStoryRequest{
		Peer:         &tg.InputPeerSelf{},
		Media:        &tg.InputMediaUploadedPhoto{File: &tg.InputFile{ID: 7320, Parts: 1, Name: "story.jpg"}},
		PrivacyRules: []tg.InputPrivacyRuleClass{&tg.InputPrivacyValueAllowAll{}},
		RandomID:     7320,
		Period:       86400,
		Caption:      "🙂 ok",
		Entities:     []tg.MessageEntityClass{&tg.MessageEntityBold{Offset: 0, Length: 2}},
	}
	updates, err := r.onStoriesSendStory(reqCtx, validReq)
	if err != nil {
		t.Fatalf("send story with utf16 entity: %v", err)
	}
	item := updates.(*tg.Updates).Updates[1].(*tg.UpdateStory).Story.(*tg.StoryItem)
	entities, ok := item.GetEntities()
	if !ok || len(entities) != 1 {
		t.Fatalf("story entities = ok %v %+v, want one entity", ok, entities)
	}
	if bold, ok := entities[0].(*tg.MessageEntityBold); !ok || bold.Offset != 0 || bold.Length != 2 {
		t.Fatalf("story entity = %T %+v, want bold offset 0 length 2", entities[0], entities[0])
	}
	storyID := updates.(*tg.Updates).Updates[0].(*tg.UpdateStoryID).ID

	var typedNilBold *tg.MessageEntityBold
	var typedNilMention *tg.InputMessageEntityMentionName
	tooMany := make([]tg.MessageEntityClass, maxMessageEntityCount+1)
	for i := range tooMany {
		tooMany[i] = &tg.MessageEntityBold{Offset: 0, Length: 1}
	}
	cases := []struct {
		name     string
		caption  string
		entities []tg.MessageEntityClass
		want     string
	}{
		{name: "nil entity", caption: "bad", entities: []tg.MessageEntityClass{nil}, want: "ENTITY_BOUNDS_INVALID"},
		{name: "typed nil entity", caption: "bad", entities: []tg.MessageEntityClass{typedNilBold}, want: "ENTITY_BOUNDS_INVALID"},
		{name: "typed nil mention name", caption: "bad", entities: []tg.MessageEntityClass{typedNilMention}, want: "ENTITY_BOUNDS_INVALID"},
		{name: "unknown constructor", caption: "bad", entities: []tg.MessageEntityClass{&tg.MessageEntityUnknown{Offset: 0, Length: 1}}, want: "ENTITY_BOUNDS_INVALID"},
		{name: "offset beyond caption", caption: "bad", entities: []tg.MessageEntityClass{&tg.MessageEntityBold{Offset: 3, Length: 1}}, want: "ENTITY_BOUNDS_INVALID"},
		{name: "negative offset", caption: "bad", entities: []tg.MessageEntityClass{&tg.MessageEntityBold{Offset: -1, Length: 1}}, want: "ENTITY_BOUNDS_INVALID"},
		{name: "zero length", caption: "bad", entities: []tg.MessageEntityClass{&tg.MessageEntityBold{Offset: 0, Length: 0}}, want: "ENTITY_BOUNDS_INVALID"},
		{name: "custom emoji zero document", caption: "bad", entities: []tg.MessageEntityClass{&tg.MessageEntityCustomEmoji{Offset: 0, Length: 1}}, want: "ENTITY_BOUNDS_INVALID"},
		{name: "mention name nil user", caption: "bad", entities: []tg.MessageEntityClass{&tg.InputMessageEntityMentionName{Offset: 0, Length: 1}}, want: "USER_ID_INVALID"},
		{name: "too many entities", caption: "bad", entities: tooMany, want: "ENTITIES_TOO_LONG"},
	}
	for i, tc := range cases {
		t.Run("send "+tc.name, func(t *testing.T) {
			_, err := r.onStoriesSendStory(reqCtx, &tg.StoriesSendStoryRequest{
				Peer:         &tg.InputPeerEmpty{},
				Media:        &tg.InputMediaUploadedPhoto{File: &tg.InputFile{ID: int64(7330 + i), Parts: 1, Name: "story.jpg"}},
				PrivacyRules: []tg.InputPrivacyRuleClass{&tg.InputPrivacyValueAllowAll{}},
				RandomID:     int64(7330 + i),
				Period:       86400,
				Caption:      tc.caption,
				Entities:     tc.entities,
			})
			if err == nil || !tgerr.Is(err, tc.want) {
				t.Fatalf("send bad entities err = %v, want %s", err, tc.want)
			}
		})
		t.Run("edit "+tc.name, func(t *testing.T) {
			edit := &tg.StoriesEditStoryRequest{Peer: &tg.InputPeerEmpty{}, ID: storyID}
			edit.SetCaption(tc.caption)
			edit.SetEntities(tc.entities)
			_, err := r.onStoriesEditStory(reqCtx, edit)
			if err == nil || !tgerr.Is(err, tc.want) {
				t.Fatalf("edit bad entities err = %v, want %s", err, tc.want)
			}
		})
		t.Run("start live "+tc.name, func(t *testing.T) {
			_, err := r.onStoriesStartLive(reqCtx, &tg.StoriesStartLiveRequest{
				Peer:     &tg.InputPeerEmpty{},
				RandomID: int64(7340 + i),
				Caption:  tc.caption,
				Entities: tc.entities,
			})
			if err == nil || !tgerr.Is(err, tc.want) {
				t.Fatalf("startLive bad entities err = %v, want %s", err, tc.want)
			}
		})
	}

	edit := &tg.StoriesEditStoryRequest{Peer: &tg.InputPeerSelf{}, ID: storyID}
	edit.SetCaption("mutate")
	edit.SetEntities([]tg.MessageEntityClass{&tg.MessageEntityBold{Offset: 6, Length: 1}})
	if _, err := r.onStoriesEditStory(reqCtx, edit); err == nil || !tgerr.Is(err, "ENTITY_BOUNDS_INVALID") {
		t.Fatalf("edit bad entities before mutation err = %v, want ENTITY_BOUNDS_INVALID", err)
	}
	stories, err := r.onStoriesGetPeerStories(reqCtx, &tg.InputPeerSelf{})
	if err != nil {
		t.Fatalf("get peer stories after rejected caption entities: %v", err)
	}
	if len(stories.Stories.Stories) != 1 {
		t.Fatalf("stories after rejected caption entities = %+v, want original story only", stories.Stories.Stories)
	}
	stored := stories.Stories.Stories[0].(*tg.StoryItem)
	if stored.Caption != "🙂 ok" || stored.Edited {
		t.Fatalf("story after rejected caption entities = %+v, want original caption and not edited", stored)
	}
}

func TestStoriesSendStoryRejectsUnsupportedOptionalFlags(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	user, err := userStore.Create(ctx, domain.User{AccessHash: 92101, Phone: "15550092101", FirstName: "Author"})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	storyStore := memory.NewStoryStore()
	files := &fakeFiles{photos: map[int64]domain.Photo{}}
	r := New(Config{}, Deps{
		Users:   appusers.NewService(userStore),
		Stories: appstories.NewService(storyStore),
		Files:   files,
	}, zaptest.NewLogger(t), fixedClock{now: time.Unix(1700000550, 0)})
	reqCtx := WithUserID(ctx, user.ID)
	baseReq := func(randomID int64) *tg.StoriesSendStoryRequest {
		return &tg.StoriesSendStoryRequest{
			Peer:         &tg.InputPeerSelf{},
			Media:        &tg.InputMediaUploadedPhoto{File: &tg.InputFile{ID: randomID, Parts: 1, Name: "story.jpg"}},
			PrivacyRules: []tg.InputPrivacyRuleClass{&tg.InputPrivacyValueAllowAll{}},
			RandomID:     randomID,
			Period:       86400,
		}
	}
	cases := []struct {
		name string
		req  *tg.StoriesSendStoryRequest
		want string
	}{
		{
			name: "nil main media before peer",
			req: func() *tg.StoriesSendStoryRequest {
				req := baseReq(7208)
				req.Peer = &tg.InputPeerEmpty{}
				req.Media = nil
				return req
			}(),
			want: "MEDIA_EMPTY",
		},
		{
			name: "typed nil main media before peer",
			req: func() *tg.StoriesSendStoryRequest {
				req := baseReq(7209)
				req.Peer = &tg.InputPeerEmpty{}
				req.Media = (*tg.InputMediaUploadedPhoto)(nil)
				return req
			}(),
			want: "MEDIA_EMPTY",
		},
		{
			name: "unsupported main media before peer",
			req: func() *tg.StoriesSendStoryRequest {
				req := baseReq(7210)
				req.Peer = &tg.InputPeerEmpty{}
				req.Media = &tg.InputMediaContact{PhoneNumber: "15550000000", FirstName: "Contact"}
				return req
			}(),
			want: "MEDIA_TYPE_INVALID",
		},
		{
			name: "uploaded photo missing file before peer",
			req: func() *tg.StoriesSendStoryRequest {
				req := baseReq(7211)
				req.Peer = &tg.InputPeerEmpty{}
				req.Media = &tg.InputMediaUploadedPhoto{}
				return req
			}(),
			want: "MEDIA_INVALID",
		},
		{
			name: "referenced photo missing id before peer",
			req: func() *tg.StoriesSendStoryRequest {
				req := baseReq(7212)
				req.Peer = &tg.InputPeerEmpty{}
				req.Media = &tg.InputMediaPhoto{}
				return req
			}(),
			want: "PHOTO_INVALID",
		},
		{
			name: "referenced photo typed nil id before peer",
			req: func() *tg.StoriesSendStoryRequest {
				req := baseReq(7213)
				req.Peer = &tg.InputPeerEmpty{}
				req.Media = &tg.InputMediaPhoto{ID: (*tg.InputPhoto)(nil)}
				return req
			}(),
			want: "PHOTO_INVALID",
		},
		{
			name: "uploaded document missing file before peer",
			req: func() *tg.StoriesSendStoryRequest {
				req := baseReq(7214)
				req.Peer = &tg.InputPeerEmpty{}
				req.Media = &tg.InputMediaUploadedDocument{}
				return req
			}(),
			want: "MEDIA_INVALID",
		},
		{
			name: "referenced document missing id before peer",
			req: func() *tg.StoriesSendStoryRequest {
				req := baseReq(7215)
				req.Peer = &tg.InputPeerEmpty{}
				req.Media = &tg.InputMediaDocument{}
				return req
			}(),
			want: "MEDIA_INVALID",
		},
		{
			name: "referenced document typed nil id before peer",
			req: func() *tg.StoriesSendStoryRequest {
				req := baseReq(7216)
				req.Peer = &tg.InputPeerEmpty{}
				req.Media = &tg.InputMediaDocument{ID: (*tg.InputDocument)(nil)}
				return req
			}(),
			want: "MEDIA_INVALID",
		},
		{
			name: "music document",
			req: func() *tg.StoriesSendStoryRequest {
				req := baseReq(7201)
				req.SetMusic(&tg.InputDocument{ID: 1001, AccessHash: 2001})
				return req
			}(),
			want: "DOCUMENT_INVALID",
		},
		{
			name: "typed nil empty music before peer",
			req: func() *tg.StoriesSendStoryRequest {
				req := baseReq(7206)
				req.Peer = &tg.InputPeerEmpty{}
				req.SetMusic((*tg.InputDocumentEmpty)(nil))
				return req
			}(),
			want: "DOCUMENT_INVALID",
		},
		{
			name: "typed nil document music",
			req: func() *tg.StoriesSendStoryRequest {
				req := baseReq(7207)
				req.SetMusic((*tg.InputDocument)(nil))
				return req
			}(),
			want: "DOCUMENT_INVALID",
		},
		{
			name: "malformed media area",
			req: func() *tg.StoriesSendStoryRequest {
				req := baseReq(7202)
				req.SetMediaAreas([]tg.MediaAreaClass{testMalformedStoryMediaArea()})
				return req
			}(),
			want: "MEDIA_INVALID",
		},
		{
			name: "albums",
			req: func() *tg.StoriesSendStoryRequest {
				req := baseReq(7203)
				req.SetAlbums([]int{1})
				return req
			}(),
			want: "MEDIA_INVALID",
		},
		{
			name: "repost",
			req: func() *tg.StoriesSendStoryRequest {
				req := baseReq(7204)
				req.SetFwdFromID(&tg.InputPeerSelf{})
				req.SetFwdFromStory(1)
				return req
			}(),
			want: "STORY_ID_INVALID",
		},
		{
			name: "repost story without source peer before peer",
			req: func() *tg.StoriesSendStoryRequest {
				req := baseReq(7217)
				req.Peer = &tg.InputPeerEmpty{}
				req.SetFwdFromStory(1)
				return req
			}(),
			want: "STORY_ID_INVALID",
		},
		{
			name: "repost source peer without story before peer",
			req: func() *tg.StoriesSendStoryRequest {
				req := baseReq(7218)
				req.Peer = &tg.InputPeerEmpty{}
				req.SetFwdFromID(&tg.InputPeerSelf{})
				return req
			}(),
			want: "STORY_ID_INVALID",
		},
		{
			name: "repost invalid story id before peer",
			req: func() *tg.StoriesSendStoryRequest {
				req := baseReq(7219)
				req.Peer = &tg.InputPeerEmpty{}
				req.SetFwdFromID(&tg.InputPeerSelf{})
				req.SetFwdFromStory(domain.MaxStoryID + 1)
				return req
			}(),
			want: "STORY_ID_INVALID",
		},
		{
			name: "repost typed nil source peer before peer",
			req: func() *tg.StoriesSendStoryRequest {
				req := baseReq(7220)
				req.Peer = &tg.InputPeerEmpty{}
				req.SetFwdFromID((*tg.InputPeerUser)(nil))
				req.SetFwdFromStory(1)
				return req
			}(),
			want: "STORY_ID_INVALID",
		},
		{
			name: "repost empty source peer before peer",
			req: func() *tg.StoriesSendStoryRequest {
				req := baseReq(7221)
				req.Peer = &tg.InputPeerEmpty{}
				req.SetFwdFromID(&tg.InputPeerEmpty{})
				req.SetFwdFromStory(1)
				return req
			}(),
			want: "STORY_ID_INVALID",
		},
		{
			name: "repost zero source user id before peer",
			req: func() *tg.StoriesSendStoryRequest {
				req := baseReq(7222)
				req.Peer = &tg.InputPeerEmpty{}
				req.SetFwdFromID(&tg.InputPeerUser{})
				req.SetFwdFromStory(1)
				return req
			}(),
			want: "STORY_ID_INVALID",
		},
		{
			name: "repost source from message without msg id before peer",
			req: func() *tg.StoriesSendStoryRequest {
				req := baseReq(7223)
				req.Peer = &tg.InputPeerEmpty{}
				req.SetFwdFromID(&tg.InputPeerUserFromMessage{Peer: &tg.InputPeerSelf{}, UserID: 1})
				req.SetFwdFromStory(1)
				return req
			}(),
			want: "STORY_ID_INVALID",
		},
		{
			name: "fwd modified",
			req: func() *tg.StoriesSendStoryRequest {
				req := baseReq(7205)
				req.FwdModified = true
				return req
			}(),
			want: "STORY_ID_INVALID",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := r.onStoriesSendStory(reqCtx, tc.req); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("send story err = %v, want %s", err, tc.want)
			}
		})
	}
	stories, err := r.onStoriesGetPeerStories(reqCtx, &tg.InputPeerSelf{})
	if err != nil {
		t.Fatalf("get peer stories after rejected sends: %v", err)
	}
	if len(stories.Stories.Stories) != 0 {
		t.Fatalf("stories after rejected sends = %+v, want none persisted", stories.Stories.Stories)
	}
}

func TestStoriesSendStoryRejectsMissingRepostSourceBeforeMediaResolution(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	user, err := userStore.Create(ctx, domain.User{AccessHash: 92151, Phone: "15550092151", FirstName: "Author"})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	storyStore := memory.NewStoryStore()
	files := &fakeFiles{photos: map[int64]domain.Photo{}}
	r := New(Config{}, Deps{
		Users:   appusers.NewService(userStore),
		Stories: appstories.NewService(storyStore),
		Files:   files,
	}, zaptest.NewLogger(t), fixedClock{now: time.Unix(1700000575, 0)})
	reqCtx := WithUserID(ctx, user.ID)
	req := &tg.StoriesSendStoryRequest{
		Peer:         &tg.InputPeerSelf{},
		Media:        &tg.InputMediaUploadedPhoto{File: &tg.InputFile{ID: 7250, Parts: 1, Name: "orphan.jpg"}},
		PrivacyRules: []tg.InputPrivacyRuleClass{&tg.InputPrivacyValueAllowAll{}},
		RandomID:     7250,
		Period:       86400,
	}
	req.SetFwdFromID(&tg.InputPeerSelf{})
	req.SetFwdFromStory(1)
	if _, err := r.onStoriesSendStory(reqCtx, req); err == nil || !strings.Contains(err.Error(), "STORY_ID_INVALID") {
		t.Fatalf("missing repost source err = %v, want STORY_ID_INVALID", err)
	}
	if len(files.photos) != 0 {
		t.Fatalf("photos after missing repost source = %+v, want no media resolution before source validation", files.photos)
	}
	malformedSource := &tg.StoriesSendStoryRequest{
		Peer:         &tg.InputPeerSelf{},
		Media:        &tg.InputMediaUploadedPhoto{File: &tg.InputFile{ID: 7251, Parts: 1, Name: "malformed-source.jpg"}},
		PrivacyRules: []tg.InputPrivacyRuleClass{&tg.InputPrivacyValueAllowAll{}},
		RandomID:     7251,
		Period:       86400,
	}
	malformedSource.SetFwdFromID(&tg.InputPeerEmpty{})
	malformedSource.SetFwdFromStory(1)
	if _, err := r.onStoriesSendStory(reqCtx, malformedSource); err == nil || !strings.Contains(err.Error(), "STORY_ID_INVALID") {
		t.Fatalf("malformed repost source err = %v, want STORY_ID_INVALID", err)
	}
	if len(files.photos) != 0 {
		t.Fatalf("photos after malformed repost source = %+v, want no media resolution before source validation", files.photos)
	}
	stories, err := r.onStoriesGetPeerStories(reqCtx, &tg.InputPeerSelf{})
	if err != nil {
		t.Fatalf("get peer stories after missing repost source: %v", err)
	}
	if len(stories.Stories.Stories) != 0 {
		t.Fatalf("stories after missing repost source = %+v, want none persisted", stories.Stories.Stories)
	}
}

func TestStoriesEditStoryHandlesAndroidEmptyMusicAndRejectsUnsupportedFlags(t *testing.T) {
	ctx := context.Background()
	authKeyID := [8]byte{9, 8, 8}
	userStore := memory.NewUserStore()
	user, err := userStore.Create(ctx, domain.User{AccessHash: 92201, Phone: "15550092201", FirstName: "Author"})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	storyStore := memory.NewStoryStore()
	updateStore := memory.NewUpdateEventStore()
	r := New(Config{}, Deps{
		Users:   appusers.NewService(userStore),
		Stories: appstories.NewService(storyStore),
		Updates: appupdates.NewService(memory.NewUpdateStateStore(), updateStore),
		Files:   &fakeFiles{photos: map[int64]domain.Photo{}},
	}, zaptest.NewLogger(t), fixedClock{now: time.Unix(1700000600, 0)})
	reqCtx := WithSessionID(WithAuthKeyID(WithUserID(ctx, user.ID), authKeyID), 98)
	updates, err := r.onStoriesSendStory(reqCtx, &tg.StoriesSendStoryRequest{
		Peer:         &tg.InputPeerSelf{},
		Media:        &tg.InputMediaUploadedPhoto{File: &tg.InputFile{ID: 43, Parts: 1, Name: "story.jpg"}},
		PrivacyRules: []tg.InputPrivacyRuleClass{&tg.InputPrivacyValueAllowAll{}},
		RandomID:     7301,
		Period:       86400,
		Caption:      "first story",
	})
	if err != nil {
		t.Fatalf("send story: %v", err)
	}
	idUpdate := updates.(*tg.Updates).Updates[0].(*tg.UpdateStoryID)

	noop := &tg.StoriesEditStoryRequest{Peer: &tg.InputPeerSelf{}, ID: idUpdate.ID}
	noop.SetMusic(&tg.InputDocumentEmpty{})
	updates, err = r.onStoriesEditStory(reqCtx, noop)
	if err != nil {
		t.Fatalf("empty music noop edit: %v", err)
	}
	noopItem := updates.(*tg.Updates).Updates[0].(*tg.UpdateStory).Story.(*tg.StoryItem)
	if noopItem.Caption != "first story" || noopItem.Edited {
		t.Fatalf("noop edit story = %+v, want unchanged caption and edited=false", noopItem)
	}
	diff, err := r.onUpdatesGetDifference(reqCtx, &tg.UpdatesGetDifferenceRequest{Pts: 0})
	if err != nil {
		t.Fatalf("get difference after noop: %v", err)
	}
	gotDiff := diff.(*tg.UpdatesDifference)
	if gotDiff.State.Pts != 1 || len(gotDiff.OtherUpdates) != 1 {
		t.Fatalf("difference after noop = pts %d updates %d, want only original send", gotDiff.State.Pts, len(gotDiff.OtherUpdates))
	}

	edit := &tg.StoriesEditStoryRequest{Peer: &tg.InputPeerSelf{}, ID: idUpdate.ID}
	edit.SetMusic(&tg.InputDocumentEmpty{})
	edit.SetCaption("android caption edit")
	if updates, err = r.onStoriesEditStory(reqCtx, edit); err != nil {
		t.Fatalf("caption edit with empty music: %v", err)
	}
	editedItem := updates.(*tg.Updates).Updates[0].(*tg.UpdateStory).Story.(*tg.StoryItem)
	if !editedItem.Edited || editedItem.Caption != "android caption edit" {
		t.Fatalf("caption edit story = %+v, want edited caption", editedItem)
	}
	if music, ok := editedItem.GetMusic(); ok || music != nil {
		t.Fatalf("caption edit story music = %T ok=%v, want absent", music, ok)
	}
	diff, err = r.onUpdatesGetDifference(reqCtx, &tg.UpdatesGetDifferenceRequest{Pts: 0})
	if err != nil {
		t.Fatalf("get difference after caption edit: %v", err)
	}
	gotDiff = diff.(*tg.UpdatesDifference)
	if gotDiff.State.Pts != 2 || len(gotDiff.OtherUpdates) != 2 {
		t.Fatalf("difference after caption edit = pts %d updates %d, want send+edit", gotDiff.State.Pts, len(gotDiff.OtherUpdates))
	}
	diffEdited := gotDiff.OtherUpdates[1].(*tg.UpdateStory).Story.(*tg.StoryItem)
	if diffEdited.Caption != "android caption edit" || !diffEdited.Edited {
		t.Fatalf("difference edited story = %+v, want edited caption", diffEdited)
	}
	if music, ok := diffEdited.GetMusic(); ok || music != nil {
		t.Fatalf("difference edited story music = %T ok=%v, want absent", music, ok)
	}

	sameCaption := &tg.StoriesEditStoryRequest{Peer: &tg.InputPeerSelf{}, ID: idUpdate.ID}
	sameCaption.SetMusic(&tg.InputDocumentEmpty{})
	sameCaption.SetCaption("android caption edit")
	if _, err := r.onStoriesEditStory(reqCtx, sameCaption); err == nil || !tgerr.Is(err, "STORY_NOT_MODIFIED") {
		t.Fatalf("same caption edit err = %v, want STORY_NOT_MODIFIED", err)
	}
	diff, err = r.onUpdatesGetDifference(reqCtx, &tg.UpdatesGetDifferenceRequest{Pts: 0})
	if err != nil {
		t.Fatalf("get difference after not modified edit: %v", err)
	}
	gotDiff = diff.(*tg.UpdatesDifference)
	if gotDiff.State.Pts != 2 || len(gotDiff.OtherUpdates) != 2 {
		t.Fatalf("difference after not modified edit = pts %d updates %d, want no new events", gotDiff.State.Pts, len(gotDiff.OtherUpdates))
	}

	nonEmptyMusic := &tg.StoriesEditStoryRequest{Peer: &tg.InputPeerSelf{}, ID: idUpdate.ID}
	nonEmptyMusic.SetMusic(&tg.InputDocument{ID: 1002, AccessHash: 2002})
	if _, err := r.onStoriesEditStory(reqCtx, nonEmptyMusic); err == nil || !strings.Contains(err.Error(), "DOCUMENT_INVALID") {
		t.Fatalf("non-empty music edit err = %v, want DOCUMENT_INVALID", err)
	}
	typedNilEmptyMusic := &tg.StoriesEditStoryRequest{Peer: &tg.InputPeerSelf{}, ID: idUpdate.ID}
	typedNilEmptyMusic.SetMusic((*tg.InputDocumentEmpty)(nil))
	if _, err := r.onStoriesEditStory(reqCtx, typedNilEmptyMusic); err == nil || !strings.Contains(err.Error(), "DOCUMENT_INVALID") {
		t.Fatalf("typed nil empty music edit err = %v, want DOCUMENT_INVALID", err)
	}
	typedNilMusicDocument := &tg.StoriesEditStoryRequest{Peer: &tg.InputPeerSelf{}, ID: idUpdate.ID}
	typedNilMusicDocument.SetMusic((*tg.InputDocument)(nil))
	if _, err := r.onStoriesEditStory(reqCtx, typedNilMusicDocument); err == nil || !strings.Contains(err.Error(), "DOCUMENT_INVALID") {
		t.Fatalf("typed nil document music edit err = %v, want DOCUMENT_INVALID", err)
	}
	invalidMediaEdits := []struct {
		name  string
		media tg.InputMediaClass
		want  string
	}{
		{
			name:  "nil media before peer",
			media: nil,
			want:  "MEDIA_EMPTY",
		},
		{
			name:  "typed nil media before peer",
			media: (*tg.InputMediaUploadedPhoto)(nil),
			want:  "MEDIA_EMPTY",
		},
		{
			name:  "unsupported media before peer",
			media: &tg.InputMediaContact{PhoneNumber: "15550000000", FirstName: "Contact"},
			want:  "MEDIA_TYPE_INVALID",
		},
		{
			name:  "uploaded photo missing file before peer",
			media: &tg.InputMediaUploadedPhoto{},
			want:  "MEDIA_INVALID",
		},
		{
			name:  "referenced photo missing id before peer",
			media: &tg.InputMediaPhoto{},
			want:  "PHOTO_INVALID",
		},
		{
			name:  "referenced photo typed nil id before peer",
			media: &tg.InputMediaPhoto{ID: (*tg.InputPhoto)(nil)},
			want:  "PHOTO_INVALID",
		},
		{
			name:  "uploaded document missing file before peer",
			media: &tg.InputMediaUploadedDocument{},
			want:  "MEDIA_INVALID",
		},
		{
			name:  "referenced document missing id before peer",
			media: &tg.InputMediaDocument{},
			want:  "MEDIA_INVALID",
		},
		{
			name:  "referenced document typed nil id before peer",
			media: &tg.InputMediaDocument{ID: (*tg.InputDocument)(nil)},
			want:  "MEDIA_INVALID",
		},
	}
	for _, tc := range invalidMediaEdits {
		t.Run(tc.name, func(t *testing.T) {
			invalidMedia := &tg.StoriesEditStoryRequest{Peer: &tg.InputPeerEmpty{}, ID: idUpdate.ID}
			invalidMedia.SetMedia(tc.media)
			if _, err := r.onStoriesEditStory(reqCtx, invalidMedia); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("invalid media edit err = %v, want %s", err, tc.want)
			}
		})
	}
	mediaAreas := &tg.StoriesEditStoryRequest{Peer: &tg.InputPeerSelf{}, ID: idUpdate.ID}
	mediaAreas.SetMediaAreas([]tg.MediaAreaClass{testMalformedStoryMediaArea()})
	if _, err := r.onStoriesEditStory(reqCtx, mediaAreas); err == nil || !strings.Contains(err.Error(), "MEDIA_INVALID") {
		t.Fatalf("media areas edit err = %v, want MEDIA_INVALID", err)
	}
	stories, err := r.onStoriesGetPeerStories(reqCtx, &tg.InputPeerSelf{})
	if err != nil {
		t.Fatalf("get peer stories after rejected edits: %v", err)
	}
	item := stories.Stories.Stories[0].(*tg.StoryItem)
	if item.Caption != "android caption edit" {
		t.Fatalf("story after rejected edits = %+v, want caption preserved", item)
	}
	if music, ok := item.GetMusic(); ok || music != nil {
		t.Fatalf("story after rejected edits music = %T ok=%v, want absent", music, ok)
	}
	diff, err = r.onUpdatesGetDifference(reqCtx, &tg.UpdatesGetDifferenceRequest{Pts: 0})
	if err != nil {
		t.Fatalf("get difference after rejected edits: %v", err)
	}
	gotDiff = diff.(*tg.UpdatesDifference)
	if gotDiff.State.Pts != 2 || len(gotDiff.OtherUpdates) != 2 {
		t.Fatalf("difference after rejected edits = pts %d updates %d, want no new events", gotDiff.State.Pts, len(gotDiff.OtherUpdates))
	}
}

func TestStoriesSendEditPrivacyRulesDriveVisibility(t *testing.T) {
	ctx := context.Background()
	authKeyID := [8]byte{9, 9, 1}
	userStore := memory.NewUserStore()
	author, err := userStore.Create(ctx, domain.User{AccessHash: 93001, Phone: "15550093001", FirstName: "Author"})
	if err != nil {
		t.Fatalf("create author: %v", err)
	}
	selected, err := userStore.Create(ctx, domain.User{AccessHash: 93002, Phone: "15550093002", FirstName: "Selected"})
	if err != nil {
		t.Fatalf("create selected viewer: %v", err)
	}
	stranger, err := userStore.Create(ctx, domain.User{AccessHash: 93003, Phone: "15550093003", FirstName: "Stranger"})
	if err != nil {
		t.Fatalf("create stranger: %v", err)
	}
	storyStore := memory.NewStoryStore()
	now := time.Unix(1700000900, 0)
	r := New(Config{}, Deps{
		Users:   appusers.NewService(userStore),
		Stories: appstories.NewService(storyStore),
		Updates: appupdates.NewService(memory.NewUpdateStateStore(), memory.NewUpdateEventStore()),
		Files:   &fakeFiles{photos: map[int64]domain.Photo{}},
	}, zaptest.NewLogger(t), fixedClock{now: now})
	reqCtx := WithSessionID(WithAuthKeyID(WithUserID(ctx, author.ID), authKeyID), 991)

	updates, err := r.onStoriesSendStory(reqCtx, &tg.StoriesSendStoryRequest{
		Peer:  &tg.InputPeerSelf{},
		Media: &tg.InputMediaUploadedPhoto{File: &tg.InputFile{ID: 52, Parts: 1, Name: "selected.jpg"}},
		PrivacyRules: []tg.InputPrivacyRuleClass{&tg.InputPrivacyValueAllowUsers{
			Users: []tg.InputUserClass{&tg.InputUser{UserID: selected.ID, AccessHash: selected.AccessHash}},
		}},
		RandomID: 8001,
		Period:   86400,
		Caption:  "selected only",
	})
	if err != nil {
		t.Fatalf("send selected story: %v", err)
	}
	sendUpdates, ok := updates.(*tg.Updates)
	if !ok || len(sendUpdates.Updates) != 2 {
		t.Fatalf("send updates = %T %+v, want updateStoryID + updateStory", updates, updates)
	}
	idUpdate := sendUpdates.Updates[0].(*tg.UpdateStoryID)
	storyUpdate, ok := sendUpdates.Updates[1].(*tg.UpdateStory)
	if !ok {
		t.Fatalf("story update = %T, want updateStory", sendUpdates.Updates[1])
	}
	item, ok := storyUpdate.Story.(*tg.StoryItem)
	if !ok || !item.SelectedContacts || item.Public {
		t.Fatalf("sent story item = %T %+v, want selected_contacts private", storyUpdate.Story, storyUpdate.Story)
	}
	if len(item.Privacy) != 1 {
		t.Fatalf("sent story privacy = %+v, want one allowUsers rule", item.Privacy)
	}
	allowUsers, ok := item.Privacy[0].(*tg.PrivacyValueAllowUsers)
	if !ok || len(allowUsers.Users) != 1 || allowUsers.Users[0] != selected.ID {
		t.Fatalf("sent allowUsers = %T %+v, want selected user", item.Privacy[0], item.Privacy[0])
	}
	if findUserClass(sendUpdates.Users, selected.ID) == nil {
		t.Fatalf("send users = %+v, want selected privacy user", sendUpdates.Users)
	}

	selectedPeerStories, err := r.onStoriesGetPeerStories(WithUserID(ctx, selected.ID), &tg.InputPeerUser{UserID: author.ID, AccessHash: author.AccessHash})
	if err != nil {
		t.Fatalf("selected get peer stories: %v", err)
	}
	if len(selectedPeerStories.Stories.Stories) != 1 {
		t.Fatalf("selected peer stories = %+v, want one visible story", selectedPeerStories.Stories.Stories)
	}
	strangerPeerStories, err := r.onStoriesGetPeerStories(WithUserID(ctx, stranger.ID), &tg.InputPeerUser{UserID: author.ID, AccessHash: author.AccessHash})
	if err != nil {
		t.Fatalf("stranger get peer stories: %v", err)
	}
	if len(strangerPeerStories.Stories.Stories) != 0 {
		t.Fatalf("stranger peer stories = %+v, want hidden selected story", strangerPeerStories.Stories.Stories)
	}

	edit := &tg.StoriesEditStoryRequest{Peer: &tg.InputPeerSelf{}, ID: idUpdate.ID}
	edit.SetPrivacyRules([]tg.InputPrivacyRuleClass{
		&tg.InputPrivacyValueAllowAll{},
		&tg.InputPrivacyValueDisallowUsers{Users: []tg.InputUserClass{
			&tg.InputUser{UserID: selected.ID, AccessHash: selected.AccessHash},
		}},
	})
	updates, err = r.onStoriesEditStory(reqCtx, edit)
	if err != nil {
		t.Fatalf("edit story privacy: %v", err)
	}
	editUpdates := updates.(*tg.Updates)
	edited, ok := editUpdates.Updates[0].(*tg.UpdateStory)
	if !ok {
		t.Fatalf("edit update = %T, want updateStory", editUpdates.Updates[0])
	}
	editedItem, ok := edited.Story.(*tg.StoryItem)
	if !ok || !editedItem.Public || editedItem.SelectedContacts {
		t.Fatalf("edited story = %T %+v, want public with privacy exceptions", edited.Story, edited.Story)
	}
	if len(editedItem.Privacy) != 2 {
		t.Fatalf("edited privacy = %+v, want allowAll + disallowUsers", editedItem.Privacy)
	}
	if findUserClass(editUpdates.Users, selected.ID) == nil {
		t.Fatalf("edit users = %+v, want selected privacy user", editUpdates.Users)
	}

	selectedPeerStories, err = r.onStoriesGetPeerStories(WithUserID(ctx, selected.ID), &tg.InputPeerUser{UserID: author.ID, AccessHash: author.AccessHash})
	if err != nil {
		t.Fatalf("selected get peer stories after edit: %v", err)
	}
	if len(selectedPeerStories.Stories.Stories) != 0 {
		t.Fatalf("selected peer stories after disallow = %+v, want hidden", selectedPeerStories.Stories.Stories)
	}
	strangerPeerStories, err = r.onStoriesGetPeerStories(WithUserID(ctx, stranger.ID), &tg.InputPeerUser{UserID: author.ID, AccessHash: author.AccessHash})
	if err != nil {
		t.Fatalf("stranger get peer stories after edit: %v", err)
	}
	if len(strangerPeerStories.Stories.Stories) != 1 {
		t.Fatalf("stranger peer stories after public = %+v, want visible", strangerPeerStories.Stories.Stories)
	}
}

func TestStoriesPrivacyRulesRejectNilInputsBeforeMutation(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	author, err := userStore.Create(ctx, domain.User{AccessHash: 93201, Phone: "15550093201", FirstName: "Author"})
	if err != nil {
		t.Fatalf("create author: %v", err)
	}
	storyStore := memory.NewStoryStore()
	r := New(Config{}, Deps{
		Users:   appusers.NewService(userStore),
		Stories: appstories.NewService(storyStore),
		Updates: appupdates.NewService(memory.NewUpdateStateStore(), memory.NewUpdateEventStore()),
		Files:   &fakeFiles{photos: map[int64]domain.Photo{}},
	}, zaptest.NewLogger(t), fixedClock{now: time.Unix(1700000920, 0)})
	reqCtx := WithUserID(ctx, author.ID)

	var typedNilAllowAll *tg.InputPrivacyValueAllowAll
	var typedNilAllowUsers *tg.InputPrivacyValueAllowUsers
	var typedNilInputUser *tg.InputUser
	cases := []struct {
		name  string
		rules []tg.InputPrivacyRuleClass
		want  string
	}{
		{name: "nil rule", rules: []tg.InputPrivacyRuleClass{nil}, want: "PRIVACY_VALUE_INVALID"},
		{name: "typed nil allow all", rules: []tg.InputPrivacyRuleClass{typedNilAllowAll}, want: "PRIVACY_VALUE_INVALID"},
		{name: "typed nil allow users", rules: []tg.InputPrivacyRuleClass{typedNilAllowUsers}, want: "PRIVACY_VALUE_INVALID"},
		{name: "nil input user", rules: []tg.InputPrivacyRuleClass{&tg.InputPrivacyValueAllowUsers{Users: []tg.InputUserClass{nil}}}, want: "USER_ID_INVALID"},
		{name: "typed nil input user", rules: []tg.InputPrivacyRuleClass{&tg.InputPrivacyValueAllowUsers{Users: []tg.InputUserClass{typedNilInputUser}}}, want: "USER_ID_INVALID"},
	}
	for i, tc := range cases {
		t.Run("send "+tc.name, func(t *testing.T) {
			_, err := r.onStoriesSendStory(reqCtx, &tg.StoriesSendStoryRequest{
				Peer:         &tg.InputPeerSelf{},
				Media:        &tg.InputMediaUploadedPhoto{File: &tg.InputFile{ID: int64(8200 + i), Parts: 1, Name: "story.jpg"}},
				PrivacyRules: tc.rules,
				RandomID:     int64(8200 + i),
				Period:       86400,
				Caption:      "bad privacy",
			})
			if err == nil || !tgerr.Is(err, tc.want) {
				t.Fatalf("send privacy err = %v, want %s", err, tc.want)
			}
		})
	}
	peerStories, err := r.onStoriesGetPeerStories(reqCtx, &tg.InputPeerSelf{})
	if err != nil {
		t.Fatalf("get peer stories after rejected privacy sends: %v", err)
	}
	if len(peerStories.Stories.Stories) != 0 {
		t.Fatalf("stories after rejected privacy sends = %+v, want none", peerStories.Stories.Stories)
	}

	updates, err := r.onStoriesSendStory(reqCtx, &tg.StoriesSendStoryRequest{
		Peer:         &tg.InputPeerSelf{},
		Media:        &tg.InputMediaUploadedPhoto{File: &tg.InputFile{ID: 8300, Parts: 1, Name: "story.jpg"}},
		PrivacyRules: []tg.InputPrivacyRuleClass{&tg.InputPrivacyValueAllowAll{}},
		RandomID:     8300,
		Period:       86400,
		Caption:      "valid privacy",
	})
	if err != nil {
		t.Fatalf("send valid privacy story: %v", err)
	}
	storyID := updates.(*tg.Updates).Updates[0].(*tg.UpdateStoryID).ID

	for _, tc := range cases {
		t.Run("edit "+tc.name, func(t *testing.T) {
			edit := &tg.StoriesEditStoryRequest{Peer: &tg.InputPeerSelf{}, ID: storyID}
			edit.SetPrivacyRules(tc.rules)
			_, err := r.onStoriesEditStory(reqCtx, edit)
			if err == nil || !tgerr.Is(err, tc.want) {
				t.Fatalf("edit privacy err = %v, want %s", err, tc.want)
			}
		})
	}
	peerStories, err = r.onStoriesGetPeerStories(reqCtx, &tg.InputPeerSelf{})
	if err != nil {
		t.Fatalf("get peer stories after rejected privacy edits: %v", err)
	}
	if len(peerStories.Stories.Stories) != 1 {
		t.Fatalf("stories after rejected privacy edits = %+v, want original story", peerStories.Stories.Stories)
	}
	item := peerStories.Stories.Stories[0].(*tg.StoryItem)
	if !item.Public || item.SelectedContacts || item.CloseFriends || item.Contacts {
		t.Fatalf("story visibility after rejected privacy edits = public %v selected %v close %v contacts %v, want original public only", item.Public, item.SelectedContacts, item.CloseFriends, item.Contacts)
	}
	if item.Caption != "valid privacy" {
		t.Fatalf("story caption after rejected privacy edits = %q, want original", item.Caption)
	}
	if len(item.Privacy) != 1 {
		t.Fatalf("story privacy after rejected edits = %+v, want original allowAll", item.Privacy)
	}
	if _, ok := item.Privacy[0].(*tg.PrivacyValueAllowAll); !ok {
		t.Fatalf("story privacy[0] after rejected edits = %T, want allowAll", item.Privacy[0])
	}
}

func testStoryMediaArea() tg.MediaAreaClass {
	return testStoryMediaAreaWith("🔥", 10, false, false)
}

func testStoryMediaAreaWith(emoticon string, x float64, dark, flipped bool) tg.MediaAreaClass {
	coordinates := tg.MediaAreaCoordinates{X: x, Y: 10, W: 10, H: 10, Rotation: 15}
	coordinates.SetRadius(8)
	return &tg.MediaAreaSuggestedReaction{
		Dark:        dark,
		Flipped:     flipped,
		Coordinates: coordinates,
		Reaction:    &tg.ReactionEmoji{Emoticon: emoticon},
	}
}

func testMalformedStoryMediaArea() tg.MediaAreaClass {
	return nil
}

func testNilStoryMediaAreas() []tg.MediaAreaClass {
	var suggested *tg.MediaAreaSuggestedReaction
	var url *tg.MediaAreaURL
	var geo *tg.MediaAreaGeoPoint
	var venue *tg.MediaAreaVenue
	var inputVenue *tg.InputMediaAreaVenue
	var weather *tg.MediaAreaWeather
	var channelPost *tg.MediaAreaChannelPost
	var starGift *tg.MediaAreaStarGift
	var inputChannelPost *tg.InputMediaAreaChannelPost
	return []tg.MediaAreaClass{
		nil,
		suggested,
		url,
		geo,
		venue,
		inputVenue,
		weather,
		channelPost,
		starGift,
		inputChannelPost,
	}
}

func testStoryURLMediaArea(url string, x float64) tg.MediaAreaClass {
	return &tg.MediaAreaURL{
		Coordinates: tg.MediaAreaCoordinates{X: x, Y: 10, W: 20, H: 10, Rotation: 15},
		URL:         url,
	}
}

func testStoryGeoPointMediaArea(lat, long, x float64) tg.MediaAreaClass {
	area := &tg.MediaAreaGeoPoint{
		Coordinates: tg.MediaAreaCoordinates{X: x, Y: 12, W: 22, H: 11, Rotation: 20},
		Geo:         &tg.GeoPoint{Lat: lat, Long: long, AccessHash: 123456},
	}
	area.SetAddress(tg.GeoPointAddress{
		CountryISO2: "CN",
		State:       "Shanghai",
		City:        "Shanghai",
		Street:      "People Square",
	})
	return area
}

func testStoryVenueMediaArea(title string, lat, long, x float64) tg.MediaAreaClass {
	return &tg.MediaAreaVenue{
		Coordinates: tg.MediaAreaCoordinates{X: x, Y: 13, W: 23, H: 12, Rotation: 22},
		Geo:         &tg.GeoPoint{Lat: lat, Long: long, AccessHash: 123456},
		Title:       title,
		Address:     "Inline Street",
		Provider:    "gplaces",
		VenueID:     "venue-id",
		VenueType:   "cafe",
	}
}

func testStoryInputVenueMediaArea(queryID int64, resultID string, x float64) tg.MediaAreaClass {
	return &tg.InputMediaAreaVenue{
		Coordinates: tg.MediaAreaCoordinates{X: x, Y: 13, W: 23, H: 12, Rotation: 22},
		QueryID:     queryID,
		ResultID:    resultID,
	}
}

func testStoryInlineVenueResult(id, title string, lat, long float64) domain.BotInlineResult {
	return domain.BotInlineResult{
		ID:   id,
		Type: "venue",
		Media: &domain.MessageMedia{Kind: domain.MessageMediaKindVenue, Venue: &domain.MessageVenue{
			Geo:       domain.MessageGeoPoint{Lat: lat, Long: long, AccessHash: 123456},
			Title:     title,
			Address:   "Inline Street",
			Provider:  "gplaces",
			VenueID:   "venue-id",
			VenueType: "cafe",
		}},
	}
}

func testStoryWeatherMediaArea(emoji string, temperatureC float64, color int, x float64) tg.MediaAreaClass {
	return &tg.MediaAreaWeather{
		Coordinates:  tg.MediaAreaCoordinates{X: x, Y: 14, W: 24, H: 12, Rotation: 25},
		Emoji:        emoji,
		TemperatureC: temperatureC,
		Color:        color,
	}
}

func testStoryChannelPostMediaArea(channelID int64, msgID int, x float64) tg.MediaAreaClass {
	return &tg.MediaAreaChannelPost{
		Coordinates: tg.MediaAreaCoordinates{X: x, Y: 15, W: 25, H: 12, Rotation: 27},
		ChannelID:   channelID,
		MsgID:       msgID,
	}
}

func testStoryStarGiftMediaArea(slug string, x float64) tg.MediaAreaClass {
	return &tg.MediaAreaStarGift{
		Coordinates: tg.MediaAreaCoordinates{X: x, Y: 16, W: 26, H: 12, Rotation: 29},
		Slug:        slug,
	}
}

func testStoryInputChannelPostMediaArea(channelID, accessHash int64, msgID int, x float64) tg.MediaAreaClass {
	return &tg.InputMediaAreaChannelPost{
		Coordinates: tg.MediaAreaCoordinates{X: x, Y: 15, W: 25, H: 12, Rotation: 27},
		Channel:     &tg.InputChannel{ChannelID: channelID, AccessHash: accessHash},
		MsgID:       msgID,
	}
}

func assertStorySuggestedReactionArea(t *testing.T, item *tg.StoryItem, wantEmoticon string, wantX float64, wantDark, wantFlipped bool) {
	t.Helper()
	areas, ok := item.GetMediaAreas()
	if !ok || len(areas) != 1 {
		t.Fatalf("story media areas = ok %v %+v, want one area", ok, areas)
	}
	area, ok := areas[0].(*tg.MediaAreaSuggestedReaction)
	if !ok {
		t.Fatalf("story media area = %T, want mediaAreaSuggestedReaction", areas[0])
	}
	if area.Dark != wantDark || area.Flipped != wantFlipped {
		t.Fatalf("story media area flags = dark %v flipped %v, want %v/%v", area.Dark, area.Flipped, wantDark, wantFlipped)
	}
	if area.Coordinates.X != wantX || area.Coordinates.Y != 10 || area.Coordinates.W != 10 || area.Coordinates.H != 10 || area.Coordinates.Rotation != 15 {
		t.Fatalf("story media area coordinates = %+v, want x %v y/w/h 10 rotation 15", area.Coordinates, wantX)
	}
	radius, ok := area.Coordinates.GetRadius()
	if !ok || radius != 8 {
		t.Fatalf("story media area radius = %v ok %v, want 8", radius, ok)
	}
	reaction, ok := area.Reaction.(*tg.ReactionEmoji)
	if !ok || reaction.Emoticon != wantEmoticon {
		t.Fatalf("story media area reaction = %T %+v, want emoji %q", area.Reaction, area.Reaction, wantEmoticon)
	}
}

func assertStorySuggestedCustomReactionArea(t *testing.T, item *tg.StoryItem, wantDocumentID int64, wantX float64, wantDark, wantFlipped bool) {
	t.Helper()
	areas, ok := item.GetMediaAreas()
	if !ok || len(areas) != 1 {
		t.Fatalf("story media areas = ok %v %+v, want one area", ok, areas)
	}
	area, ok := areas[0].(*tg.MediaAreaSuggestedReaction)
	if !ok {
		t.Fatalf("story media area = %T, want mediaAreaSuggestedReaction", areas[0])
	}
	if area.Dark != wantDark || area.Flipped != wantFlipped {
		t.Fatalf("story media area flags = dark %v flipped %v, want %v/%v", area.Dark, area.Flipped, wantDark, wantFlipped)
	}
	if area.Coordinates.X != wantX || area.Coordinates.Y != 10 || area.Coordinates.W != 10 || area.Coordinates.H != 10 {
		t.Fatalf("story media area coordinates = %+v, want x %v y/w/h 10", area.Coordinates, wantX)
	}
	reaction, ok := area.Reaction.(*tg.ReactionCustomEmoji)
	if !ok || reaction.DocumentID != wantDocumentID {
		t.Fatalf("story media area reaction = %T %+v, want custom emoji %d", area.Reaction, area.Reaction, wantDocumentID)
	}
}

func assertStoryURLMediaArea(t *testing.T, item *tg.StoryItem, wantURL string, wantX float64) {
	t.Helper()
	areas, ok := item.GetMediaAreas()
	if !ok || len(areas) != 1 {
		t.Fatalf("story media areas = ok %v %+v, want one url area", ok, areas)
	}
	area, ok := areas[0].(*tg.MediaAreaURL)
	if !ok {
		t.Fatalf("story media area = %T, want mediaAreaUrl", areas[0])
	}
	if area.URL != wantURL {
		t.Fatalf("story media area url = %q, want %q", area.URL, wantURL)
	}
	if area.Coordinates.X != wantX || area.Coordinates.Y != 10 || area.Coordinates.W != 20 || area.Coordinates.H != 10 || area.Coordinates.Rotation != 15 {
		t.Fatalf("story media area coordinates = %+v, want x %v y 10 w 20 h 10 rotation 15", area.Coordinates, wantX)
	}
}

func assertStoryGeoPointMediaArea(t *testing.T, item *tg.StoryItem, wantLat, wantLong, wantX float64) {
	t.Helper()
	areas, ok := item.GetMediaAreas()
	if !ok || len(areas) != 1 {
		t.Fatalf("story media areas = ok %v %+v, want one geo area", ok, areas)
	}
	area, ok := areas[0].(*tg.MediaAreaGeoPoint)
	if !ok {
		t.Fatalf("story media area = %T, want mediaAreaGeoPoint", areas[0])
	}
	geo, ok := area.Geo.(*tg.GeoPoint)
	if !ok || geo.Lat != wantLat || geo.Long != wantLong || geo.AccessHash != 123456 {
		t.Fatalf("story geo point = %T %+v, want lat %v long %v access_hash 123456", area.Geo, area.Geo, wantLat, wantLong)
	}
	address, ok := area.GetAddress()
	if !ok || address.CountryISO2 != "CN" || address.State != "Shanghai" || address.City != "Shanghai" || address.Street != "People Square" {
		t.Fatalf("story geo address = ok %v %+v, want CN/Shanghai/Shanghai/People Square", ok, address)
	}
	if area.Coordinates.X != wantX || area.Coordinates.Y != 12 || area.Coordinates.W != 22 || area.Coordinates.H != 11 || area.Coordinates.Rotation != 20 {
		t.Fatalf("story media area coordinates = %+v, want x %v y 12 w 22 h 11 rotation 20", area.Coordinates, wantX)
	}
}

func assertStoryVenueMediaArea(t *testing.T, item *tg.StoryItem, wantTitle string, wantLat, wantLong, wantX float64) {
	t.Helper()
	areas, ok := item.GetMediaAreas()
	if !ok || len(areas) != 1 {
		t.Fatalf("story media areas = ok %v %+v, want one venue area", ok, areas)
	}
	area, ok := areas[0].(*tg.MediaAreaVenue)
	if !ok {
		t.Fatalf("story media area = %T, want mediaAreaVenue", areas[0])
	}
	geo, ok := area.Geo.(*tg.GeoPoint)
	if !ok || geo.Lat != wantLat || geo.Long != wantLong || geo.AccessHash != 123456 {
		t.Fatalf("story venue geo = %T %+v, want lat %v long %v access_hash 123456", area.Geo, area.Geo, wantLat, wantLong)
	}
	if area.Title != wantTitle || area.Address != "Inline Street" || area.Provider != "gplaces" || area.VenueID != "venue-id" || area.VenueType != "cafe" {
		t.Fatalf("story venue fields = title %q address %q provider %q id %q type %q, want %q/Inline Street/gplaces/venue-id/cafe", area.Title, area.Address, area.Provider, area.VenueID, area.VenueType, wantTitle)
	}
	if area.Coordinates.X != wantX || area.Coordinates.Y != 13 || area.Coordinates.W != 23 || area.Coordinates.H != 12 || area.Coordinates.Rotation != 22 {
		t.Fatalf("story media area coordinates = %+v, want x %v y 13 w 23 h 12 rotation 22", area.Coordinates, wantX)
	}
}

func assertStoryWeatherMediaArea(t *testing.T, item *tg.StoryItem, wantEmoji string, wantTemperatureC float64, wantColor int, wantX float64) {
	t.Helper()
	areas, ok := item.GetMediaAreas()
	if !ok || len(areas) != 1 {
		t.Fatalf("story media areas = ok %v %+v, want one weather area", ok, areas)
	}
	area, ok := areas[0].(*tg.MediaAreaWeather)
	if !ok {
		t.Fatalf("story media area = %T, want mediaAreaWeather", areas[0])
	}
	if area.Emoji != wantEmoji || area.TemperatureC != wantTemperatureC || area.Color != wantColor {
		t.Fatalf("story weather area = emoji %q temp %v color %d, want %q/%v/%d", area.Emoji, area.TemperatureC, area.Color, wantEmoji, wantTemperatureC, wantColor)
	}
	if area.Coordinates.X != wantX || area.Coordinates.Y != 14 || area.Coordinates.W != 24 || area.Coordinates.H != 12 || area.Coordinates.Rotation != 25 {
		t.Fatalf("story media area coordinates = %+v, want x %v y 14 w 24 h 12 rotation 25", area.Coordinates, wantX)
	}
}

func assertStoryChannelPostMediaArea(t *testing.T, item *tg.StoryItem, wantChannelID int64, wantMsgID int, wantX float64) {
	t.Helper()
	areas, ok := item.GetMediaAreas()
	if !ok || len(areas) != 1 {
		t.Fatalf("story media areas = ok %v %+v, want one channel post area", ok, areas)
	}
	area, ok := areas[0].(*tg.MediaAreaChannelPost)
	if !ok {
		t.Fatalf("story media area = %T, want mediaAreaChannelPost", areas[0])
	}
	if area.ChannelID != wantChannelID || area.MsgID != wantMsgID {
		t.Fatalf("story channel post area = channel %d msg %d, want %d/%d", area.ChannelID, area.MsgID, wantChannelID, wantMsgID)
	}
	if area.Coordinates.X != wantX || area.Coordinates.Y != 15 || area.Coordinates.W != 25 || area.Coordinates.H != 12 || area.Coordinates.Rotation != 27 {
		t.Fatalf("story media area coordinates = %+v, want x %v y 15 w 25 h 12 rotation 27", area.Coordinates, wantX)
	}
}

func assertStoryStarGiftMediaArea(t *testing.T, item *tg.StoryItem, wantSlug string, wantX float64) {
	t.Helper()
	areas, ok := item.GetMediaAreas()
	if !ok || len(areas) != 1 {
		t.Fatalf("story media areas = ok %v %+v, want one star gift area", ok, areas)
	}
	area, ok := areas[0].(*tg.MediaAreaStarGift)
	if !ok {
		t.Fatalf("story media area = %T, want mediaAreaStarGift", areas[0])
	}
	if area.Slug != wantSlug {
		t.Fatalf("story star gift slug = %q, want %q", area.Slug, wantSlug)
	}
	if area.Coordinates.X != wantX || area.Coordinates.Y != 16 || area.Coordinates.W != 26 || area.Coordinates.H != 12 || area.Coordinates.Rotation != 29 {
		t.Fatalf("story media area coordinates = %+v, want x %v y 16 w 26 h 12 rotation 29", area.Coordinates, wantX)
	}
}

func assertStoryForwardHeader(t *testing.T, item *tg.StoryItem, wantUserID int64, wantStoryID int, wantModified bool) {
	t.Helper()
	if item == nil {
		t.Fatalf("story item is nil")
	}
	forward, ok := item.GetFwdFrom()
	if !ok {
		t.Fatalf("story fwd_from absent, want source user %d story %d", wantUserID, wantStoryID)
	}
	if forward.Modified != wantModified {
		t.Fatalf("story fwd_from modified = %v, want %v", forward.Modified, wantModified)
	}
	if forward.StoryID != wantStoryID {
		t.Fatalf("story fwd_from story_id = %d, want %d", forward.StoryID, wantStoryID)
	}
	source, ok := forward.From.(*tg.PeerUser)
	if !ok || source.UserID != wantUserID {
		t.Fatalf("story fwd_from source = %T %+v, want peerUser %d", forward.From, forward.From, wantUserID)
	}
}

func assertStoryForwardNameHeader(t *testing.T, item *tg.StoryItem, wantName string, wantStoryID int, wantModified bool) {
	t.Helper()
	if item == nil {
		t.Fatalf("story item is nil")
	}
	forward, ok := item.GetFwdFrom()
	if !ok {
		t.Fatalf("story fwd_from absent, want source name %q story %d", wantName, wantStoryID)
	}
	if forward.Modified != wantModified {
		t.Fatalf("story fwd_from modified = %v, want %v", forward.Modified, wantModified)
	}
	if forward.StoryID != wantStoryID {
		t.Fatalf("story fwd_from story_id = %d, want %d", forward.StoryID, wantStoryID)
	}
	if forward.From != nil {
		t.Fatalf("story fwd_from source = %T %+v, want hidden source", forward.From, forward.From)
	}
	name, ok := forward.GetFromName()
	if !ok || name != wantName {
		t.Fatalf("story fwd_from from_name = %q %v, want %q", name, ok, wantName)
	}
}

func assertStoryHasNoMediaAreas(t *testing.T, item *tg.StoryItem) {
	t.Helper()
	if areas, ok := item.GetMediaAreas(); ok && len(areas) > 0 {
		t.Fatalf("story media areas = %+v, want absent or empty", areas)
	}
}

func TestStoriesEditPrivacyFanoutsKnownViewerDifferences(t *testing.T) {
	ctx := context.Background()
	ownerAuth := [8]byte{7, 7, 1}
	userStore := memory.NewUserStore()
	author, err := userStore.Create(ctx, domain.User{AccessHash: 93101, Phone: "15550093101", FirstName: "Author"})
	if err != nil {
		t.Fatalf("create author: %v", err)
	}
	selected, err := userStore.Create(ctx, domain.User{AccessHash: 93102, Phone: "15550093102", FirstName: "Selected"})
	if err != nil {
		t.Fatalf("create selected: %v", err)
	}
	stranger, err := userStore.Create(ctx, domain.User{AccessHash: 93103, Phone: "15550093103", FirstName: "Stranger"})
	if err != nil {
		t.Fatalf("create stranger: %v", err)
	}
	storyStore := memory.NewStoryStore()
	stateStore := memory.NewUpdateStateStore()
	updateStore := memory.NewUpdateEventStore()
	ownerPeer := domain.Peer{Type: domain.PeerTypeUser, ID: author.ID}
	if _, err := storyStore.UpsertStory(ctx, domain.UpsertStoryRequest{Story: domain.Story{
		Owner:      ownerPeer,
		ID:         1,
		Date:       1700001000,
		ExpireDate: 1700004600,
		Public:     true,
		Out:        true,
		Views:      domain.StoryViews{ViewsCount: 1, HasViewers: true, RecentViewers: []int64{stranger.ID}},
	}}); err != nil {
		t.Fatalf("upsert public story: %v", err)
	}
	if _, err := storyStore.IncrementViews(ctx, stranger.ID, ownerPeer, []int{1}, 1700001100); err != nil {
		t.Fatalf("increment stranger view: %v", err)
	}
	r := New(Config{}, Deps{
		Users:   appusers.NewService(userStore),
		Stories: appstories.NewService(storyStore),
		Updates: appupdates.NewService(stateStore, updateStore),
	}, zaptest.NewLogger(t), fixedClock{now: time.Unix(1700001200, 0)})
	editCtx := WithSessionID(WithAuthKeyID(WithUserID(ctx, author.ID), ownerAuth), 101)
	edit := &tg.StoriesEditStoryRequest{Peer: &tg.InputPeerSelf{}, ID: 1}
	edit.SetPrivacyRules([]tg.InputPrivacyRuleClass{&tg.InputPrivacyValueAllowUsers{
		Users: []tg.InputUserClass{&tg.InputUser{UserID: selected.ID, AccessHash: selected.AccessHash}},
	}})
	if _, err := r.onStoriesEditStory(editCtx, edit); err != nil {
		t.Fatalf("edit story privacy: %v", err)
	}
	if _, found, err := stateStore.Get(ctx, ownerAuth, selected.ID); err != nil || found {
		t.Fatalf("owner auth state for selected found=%v err=%v, want absent", found, err)
	}

	selectedDiff, err := r.onUpdatesGetDifference(WithAuthKeyID(WithUserID(ctx, selected.ID), [8]byte{8, 1}), &tg.UpdatesGetDifferenceRequest{Pts: 0})
	if err != nil {
		t.Fatalf("get selected difference: %v", err)
	}
	selectedUpdates := selectedDiff.(*tg.UpdatesDifference)
	if selectedUpdates.State.Pts != 1 || len(selectedUpdates.OtherUpdates) != 1 {
		t.Fatalf("selected difference pts/updates = %d/%d, want 1/1", selectedUpdates.State.Pts, len(selectedUpdates.OtherUpdates))
	}
	selectedStoryUpdate, ok := selectedUpdates.OtherUpdates[0].(*tg.UpdateStory)
	if !ok {
		t.Fatalf("selected update = %T, want *tg.UpdateStory", selectedUpdates.OtherUpdates[0])
	}
	selectedItem, ok := selectedStoryUpdate.Story.(*tg.StoryItem)
	if !ok || selectedItem.ID != 1 || !selectedItem.SelectedContacts || selectedItem.Public {
		t.Fatalf("selected story = %T %+v, want selected story item", selectedStoryUpdate.Story, selectedStoryUpdate.Story)
	}
	if _, ok := selectedItem.GetViews(); selectedItem.Out || ok || len(selectedItem.Privacy) != 0 {
		t.Fatalf("selected story item = %+v, want viewer fanout without out/views/privacy", selectedItem)
	}

	strangerDiff, err := r.onUpdatesGetDifference(WithAuthKeyID(WithUserID(ctx, stranger.ID), [8]byte{8, 2}), &tg.UpdatesGetDifferenceRequest{Pts: 0})
	if err != nil {
		t.Fatalf("get stranger difference: %v", err)
	}
	strangerUpdates := strangerDiff.(*tg.UpdatesDifference)
	if strangerUpdates.State.Pts != 1 || len(strangerUpdates.OtherUpdates) != 1 {
		t.Fatalf("stranger difference pts/updates = %d/%d, want 1/1", strangerUpdates.State.Pts, len(strangerUpdates.OtherUpdates))
	}
	strangerStoryUpdate, ok := strangerUpdates.OtherUpdates[0].(*tg.UpdateStory)
	if !ok {
		t.Fatalf("stranger update = %T, want *tg.UpdateStory", strangerUpdates.OtherUpdates[0])
	}
	deleted, ok := strangerStoryUpdate.Story.(*tg.StoryItemDeleted)
	if !ok || deleted.ID != 1 {
		t.Fatalf("stranger story = %T %+v, want storyItemDeleted id 1", strangerStoryUpdate.Story, strangerStoryUpdate.Story)
	}
}

func TestStoriesEditPrivacyDoesNotFanoutToStoryBlockedViewer(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	contactStore := memory.NewContactStore()
	author, err := userStore.Create(ctx, domain.User{AccessHash: 93201, Phone: "15550093201", FirstName: "Author"})
	if err != nil {
		t.Fatalf("create author: %v", err)
	}
	blocked, err := userStore.Create(ctx, domain.User{AccessHash: 93202, Phone: "15550093202", FirstName: "Blocked"})
	if err != nil {
		t.Fatalf("create blocked viewer: %v", err)
	}
	if _, err := contactStore.Upsert(ctx, author.ID, domain.ContactInput{ContactUserID: blocked.ID, FirstName: "Blocked"}); err != nil {
		t.Fatalf("upsert contact: %v", err)
	}
	contactsSvc := appcontacts.NewService(contactStore, userStore)
	if _, err := contactsSvc.BlockContact(ctx, author.ID, blocked.ID, 1700001000); err != nil {
		t.Fatalf("block story viewer: %v", err)
	}
	storyStore := memory.NewStoryStore()
	updateStore := memory.NewUpdateEventStore()
	ownerPeer := domain.Peer{Type: domain.PeerTypeUser, ID: author.ID}
	if _, err := storyStore.UpsertStory(ctx, domain.UpsertStoryRequest{Story: domain.Story{
		Owner:            ownerPeer,
		ID:               1,
		Date:             1700001000,
		ExpireDate:       1700004600,
		SelectedContacts: true,
	}}); err != nil {
		t.Fatalf("upsert story: %v", err)
	}
	r := New(Config{}, Deps{
		Contacts: contactsSvc,
		Users:    appusers.NewService(userStore),
		Stories:  appstories.NewService(storyStore),
		Updates:  appupdates.NewService(memory.NewUpdateStateStore(), updateStore),
	}, zaptest.NewLogger(t), fixedClock{now: time.Unix(1700001200, 0)})

	edit := &tg.StoriesEditStoryRequest{Peer: &tg.InputPeerSelf{}, ID: 1}
	edit.SetPrivacyRules([]tg.InputPrivacyRuleClass{&tg.InputPrivacyValueAllowAll{}})
	if _, err := r.onStoriesEditStory(WithUserID(ctx, author.ID), edit); err != nil {
		t.Fatalf("edit story privacy: %v", err)
	}
	events, err := updateStore.ListAfter(ctx, blocked.ID, 0, 10)
	if err != nil {
		t.Fatalf("list blocked viewer events: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("blocked viewer events = %+v, want no story fanout", events)
	}
}
