package rpc

import (
	"context"
	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap/zaptest"
	"reflect"
	"strings"
	appchannels "telesrv/internal/app/channels"
	appcontacts "telesrv/internal/app/contacts"
	appprivacy "telesrv/internal/app/privacy"
	appstories "telesrv/internal/app/stories"
	appupdates "telesrv/internal/app/updates"
	appusers "telesrv/internal/app/users"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
	"testing"
	"time"
)

func TestContactsSearchFindsUsers(t *testing.T) {
	ctx := context.Background()
	users := memory.NewUserStore()
	owner, err := users.Create(ctx, domain.User{AccessHash: 1, Phone: "15550000001", FirstName: "Owner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	friend, err := users.Create(ctx, domain.User{AccessHash: 2, Phone: "15550000002", FirstName: "Searchable", LastName: "Friend", Username: "search_friend"})
	if err != nil {
		t.Fatalf("create friend: %v", err)
	}
	r := New(Config{}, Deps{
		Contacts: appcontacts.NewService(memory.NewContactStore(), users),
	}, zaptest.NewLogger(t), clock.System)

	var in bin.Buffer
	if err := (&tg.ContactsSearchRequest{Q: "@search", Limit: 20}).Encode(&in); err != nil {
		t.Fatalf("encode request: %v", err)
	}
	enc, err := r.Dispatch(WithUserID(ctx, owner.ID), [8]byte{}, 0, &in)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	box, ok := enc.(*tg.ContactsFound)
	if !ok {
		t.Fatalf("result type = %T, want *tg.ContactsFound", enc)
	}
	if len(box.Results) != 1 || len(box.Users) != 1 {
		t.Fatalf("search result sizes = results %d users %d, want 1/1", len(box.Results), len(box.Users))
	}
	peer, ok := box.Results[0].(*tg.PeerUser)
	if !ok || peer.UserID != friend.ID {
		t.Fatalf("peer = %T %+v, want friend", box.Results[0], box.Results[0])
	}
}

func TestContactsEditCloseFriendsProjectsUserFlag(t *testing.T) {
	ctx := context.Background()
	users := memory.NewUserStore()
	contactsStore := memory.NewContactStore()
	owner, err := users.Create(ctx, domain.User{AccessHash: 11, Phone: "15550001001", FirstName: "Owner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	friend, err := users.Create(ctx, domain.User{AccessHash: 12, Phone: "15550001002", FirstName: "Friend"})
	if err != nil {
		t.Fatalf("create friend: %v", err)
	}
	if _, err := contactsStore.Upsert(ctx, owner.ID, domain.ContactInput{ContactUserID: friend.ID, FirstName: "Saved"}); err != nil {
		t.Fatalf("upsert contact: %v", err)
	}
	r := New(Config{}, Deps{
		Contacts: appcontacts.NewService(contactsStore, users),
	}, zaptest.NewLogger(t), clock.System)

	var edit bin.Buffer
	if err := (&tg.ContactsEditCloseFriendsRequest{ID: []int64{friend.ID, friend.ID, owner.ID, 0}}).Encode(&edit); err != nil {
		t.Fatalf("encode edit close friends: %v", err)
	}
	enc, err := r.Dispatch(WithUserID(ctx, owner.ID), [8]byte{}, 0, &edit)
	if err != nil {
		t.Fatalf("dispatch edit close friends: %v", err)
	}
	if value, ok := dispatchCanonicalValue(enc).(bool); !ok || !value {
		t.Fatalf("edit close friends result = %#v (%T), want true", dispatchCanonicalValue(enc), enc)
	}

	var get bin.Buffer
	if err := (&tg.ContactsGetContactsRequest{}).Encode(&get); err != nil {
		t.Fatalf("encode get contacts: %v", err)
	}
	got, err := r.Dispatch(WithUserID(ctx, owner.ID), [8]byte{}, 0, &get)
	if err != nil {
		t.Fatalf("dispatch get contacts: %v", err)
	}
	list, ok := got.(*tg.ContactsContacts)
	if !ok {
		t.Fatalf("contacts result = %T %+v, want *tg.ContactsContacts", got, got)
	}
	if len(list.Users) != 1 {
		t.Fatalf("contacts result = %T %+v, want one contact user", got, got)
	}
	user, ok := list.Users[0].(*tg.User)
	if !ok || !user.CloseFriend {
		t.Fatalf("contact user = %T %+v, want close_friend", list.Users[0], list.Users[0])
	}
}

func TestContactsEditCloseFriendsFanoutsCloseFriendStories(t *testing.T) {
	ctx := context.Background()
	users := memory.NewUserStore()
	contactsStore := memory.NewContactStore()
	storyStore := memory.NewStoryStore()
	stateStore := memory.NewUpdateStateStore()
	updateStore := memory.NewUpdateEventStore()
	owner, err := users.Create(ctx, domain.User{AccessHash: 21, Phone: "15550002001", FirstName: "Owner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	friend, err := users.Create(ctx, domain.User{AccessHash: 22, Phone: "15550002002", FirstName: "Friend"})
	if err != nil {
		t.Fatalf("create friend: %v", err)
	}
	if _, err := contactsStore.Upsert(ctx, owner.ID, domain.ContactInput{ContactUserID: friend.ID, FirstName: "Friend"}); err != nil {
		t.Fatalf("upsert contact: %v", err)
	}
	ownerPeer := domain.Peer{Type: domain.PeerTypeUser, ID: owner.ID}
	for _, story := range []domain.Story{
		{
			Owner:        ownerPeer,
			ID:           1,
			Date:         1700000000,
			ExpireDate:   1700003600,
			CloseFriends: true,
			Out:          true,
			Views:        domain.StoryViews{ViewsCount: 1, HasViewers: true, RecentViewers: []int64{friend.ID}},
		},
		{
			Owner:        ownerPeer,
			ID:           2,
			Date:         1700000001,
			ExpireDate:   1700003600,
			CloseFriends: true,
			AllowUserIDs: []int64{friend.ID},
		},
		{
			Owner:        ownerPeer,
			ID:           3,
			Date:         1700000002,
			ExpireDate:   1700000005,
			CloseFriends: true,
		},
	} {
		if _, err := storyStore.UpsertStory(ctx, domain.UpsertStoryRequest{Story: story}); err != nil {
			t.Fatalf("upsert story %d: %v", story.ID, err)
		}
	}
	r := New(Config{}, Deps{
		Contacts: appcontacts.NewService(contactsStore, users),
		Stories:  appstories.NewService(storyStore),
		Updates:  appupdates.NewService(stateStore, updateStore),
	}, zaptest.NewLogger(t), fixedClock{now: time.Unix(1700000100, 0)})

	ownerAuth := [8]byte{1, 2, 3}
	var add bin.Buffer
	if err := (&tg.ContactsEditCloseFriendsRequest{ID: []int64{friend.ID}}).Encode(&add); err != nil {
		t.Fatalf("encode add close friend: %v", err)
	}
	ownerCtx := WithSessionID(WithAuthKeyID(WithUserID(ctx, owner.ID), ownerAuth), 44)
	if _, err := r.Dispatch(ownerCtx, ownerAuth, 44, &add); err != nil {
		t.Fatalf("dispatch add close friend: %v", err)
	}
	if _, found, err := stateStore.Get(ctx, ownerAuth, friend.ID); err != nil || found {
		t.Fatalf("owner auth state for friend found=%v err=%v, want absent", found, err)
	}

	var clear bin.Buffer
	if err := (&tg.ContactsEditCloseFriendsRequest{ID: []int64{}}).Encode(&clear); err != nil {
		t.Fatalf("encode clear close friends: %v", err)
	}
	if _, err := r.Dispatch(ownerCtx, ownerAuth, 44, &clear); err != nil {
		t.Fatalf("dispatch clear close friends: %v", err)
	}

	diff, err := r.onUpdatesGetDifference(WithAuthKeyID(WithUserID(ctx, friend.ID), [8]byte{9}), &tg.UpdatesGetDifferenceRequest{Pts: 0})
	if err != nil {
		t.Fatalf("get friend difference: %v", err)
	}
	got, ok := diff.(*tg.UpdatesDifference)
	if !ok {
		t.Fatalf("difference = %T, want *tg.UpdatesDifference", diff)
	}
	if got.State.Pts != 3 || len(got.OtherUpdates) != 3 {
		t.Fatalf("difference pts/updates = %d/%d, want 3/3", got.State.Pts, len(got.OtherUpdates))
	}
	for i, update := range got.OtherUpdates[:2] {
		storyUpdate, ok := update.(*tg.UpdateStory)
		if !ok {
			t.Fatalf("add update %d = %T, want *tg.UpdateStory", i, update)
		}
		item, ok := storyUpdate.Story.(*tg.StoryItem)
		if !ok {
			t.Fatalf("add story %d = %T, want *tg.StoryItem", i, storyUpdate.Story)
		}
		if _, ok := item.GetViews(); item.Out || ok {
			t.Fatalf("add story %d = %+v, want viewer fanout without out/views", i, item)
		}
	}
	last, ok := got.OtherUpdates[2].(*tg.UpdateStory)
	if !ok {
		t.Fatalf("remove update = %T, want *tg.UpdateStory", got.OtherUpdates[2])
	}
	deleted, ok := last.Story.(*tg.StoryItemDeleted)
	if !ok {
		t.Fatalf("remove story = %T, want *tg.StoryItemDeleted", last.Story)
	}
	if deleted.ID != 1 {
		t.Fatalf("deleted story id = %d, want only story 1 removed", deleted.ID)
	}
}

func TestContactsBlockUnblockFanoutsStoryVisibilityChanges(t *testing.T) {
	ctx := context.Background()
	users := memory.NewUserStore()
	contactsStore := memory.NewContactStore()
	storyStore := memory.NewStoryStore()
	updateStore := memory.NewUpdateEventStore()
	owner, err := users.Create(ctx, domain.User{AccessHash: 31, Phone: "15550003001", FirstName: "Owner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	viewer, err := users.Create(ctx, domain.User{AccessHash: 32, Phone: "15550003002", FirstName: "Viewer"})
	if err != nil {
		t.Fatalf("create viewer: %v", err)
	}
	if _, err := contactsStore.Upsert(ctx, owner.ID, domain.ContactInput{ContactUserID: viewer.ID, FirstName: "Viewer"}); err != nil {
		t.Fatalf("upsert contact: %v", err)
	}
	ownerPeer := domain.Peer{Type: domain.PeerTypeUser, ID: owner.ID}
	for _, story := range []domain.Story{
		{
			Owner:      ownerPeer,
			ID:         1,
			Date:       1700000000,
			ExpireDate: 1700003600,
			Public:     true,
			Out:        true,
			Views:      domain.StoryViews{ViewsCount: 3, HasViewers: true, RecentViewers: []int64{viewer.ID}},
		},
		{
			Owner:            ownerPeer,
			ID:               2,
			Date:             1700000001,
			ExpireDate:       1700003600,
			SelectedContacts: true,
			AllowUserIDs:     []int64{viewer.ID},
		},
		{
			Owner:      ownerPeer,
			ID:         3,
			Date:       1700000002,
			ExpireDate: 1700003600,
			Contacts:   true,
		},
		{
			Owner:        ownerPeer,
			ID:           4,
			Date:         1700000003,
			ExpireDate:   1700003600,
			CloseFriends: true,
		},
		{
			Owner:           ownerPeer,
			ID:              5,
			Date:            1700000004,
			ExpireDate:      1700003600,
			Public:          true,
			DisallowUserIDs: []int64{viewer.ID},
		},
		{
			Owner:      ownerPeer,
			ID:         6,
			Date:       1700000005,
			ExpireDate: 1700000006,
			Public:     true,
		},
	} {
		if _, err := storyStore.UpsertStory(ctx, domain.UpsertStoryRequest{Story: story}); err != nil {
			t.Fatalf("upsert story %d: %v", story.ID, err)
		}
	}
	r := New(Config{}, Deps{
		Contacts: appcontacts.NewService(contactsStore, users),
		Stories:  appstories.NewService(storyStore),
		Updates:  appupdates.NewService(memory.NewUpdateStateStore(), updateStore),
	}, zaptest.NewLogger(t), fixedClock{now: time.Unix(1700000100, 0)})

	ownerAuth := [8]byte{7, 7, 1}
	ownerCtx := WithSessionID(WithAuthKeyID(WithUserID(ctx, owner.ID), ownerAuth), 51)
	if ok, err := r.onContactsBlock(ownerCtx, &tg.ContactsBlockRequest{
		MyStoriesFrom: true,
		ID:            &tg.InputPeerUser{UserID: viewer.ID, AccessHash: viewer.AccessHash},
	}); err != nil || !ok {
		t.Fatalf("contacts.block = %v, %v", ok, err)
	}
	ownerBlockDiff, err := r.onUpdatesGetDifference(WithAuthKeyID(WithUserID(ctx, owner.ID), [8]byte{7, 7, 2}), &tg.UpdatesGetDifferenceRequest{Pts: 0})
	if err != nil {
		t.Fatalf("get owner block difference: %v", err)
	}
	ownerBlockUpdates, ok := ownerBlockDiff.(*tg.UpdatesDifference)
	if !ok {
		t.Fatalf("owner block difference = %T, want *tg.UpdatesDifference", ownerBlockDiff)
	}
	if ownerBlockUpdates.State.Pts != 2 || len(ownerBlockUpdates.OtherUpdates) != 2 {
		t.Fatalf("owner block difference pts/updates = %d/%d, want 2/2", ownerBlockUpdates.State.Pts, len(ownerBlockUpdates.OtherUpdates))
	}
	ownerBlocked, ok := ownerBlockUpdates.OtherUpdates[0].(*tg.UpdatePeerBlocked)
	if !ok || !ownerBlocked.Blocked || !ownerBlocked.BlockedMyStoriesFrom {
		t.Fatalf("owner block update[0] = %+v (%T), want story block", ownerBlockUpdates.OtherUpdates[0], ownerBlockUpdates.OtherUpdates[0])
	}
	if peer, ok := ownerBlocked.PeerID.(*tg.PeerUser); !ok || peer.UserID != viewer.ID {
		t.Fatalf("owner block peer = %#v, want viewer", ownerBlocked.PeerID)
	}
	if _, ok := ownerBlockUpdates.OtherUpdates[1].(*tg.UpdatePeerSettings); !ok {
		t.Fatalf("owner block update[1] = %T, want peer settings", ownerBlockUpdates.OtherUpdates[1])
	}
	blockDiff, err := r.onUpdatesGetDifference(WithAuthKeyID(WithUserID(ctx, viewer.ID), [8]byte{3, 1}), &tg.UpdatesGetDifferenceRequest{Pts: 0})
	if err != nil {
		t.Fatalf("get viewer block difference: %v", err)
	}
	blockUpdates, ok := blockDiff.(*tg.UpdatesDifference)
	if !ok {
		t.Fatalf("block difference = %T, want *tg.UpdatesDifference", blockDiff)
	}
	if blockUpdates.State.Pts != 3 || len(blockUpdates.OtherUpdates) != 3 {
		t.Fatalf("block difference pts/updates = %d/%d, want 3/3", blockUpdates.State.Pts, len(blockUpdates.OtherUpdates))
	}
	for i, update := range blockUpdates.OtherUpdates {
		storyUpdate, ok := update.(*tg.UpdateStory)
		if !ok {
			t.Fatalf("block update %d = %T, want *tg.UpdateStory", i, update)
		}
		if peer, ok := storyUpdate.Peer.(*tg.PeerUser); !ok || peer.UserID != owner.ID {
			t.Fatalf("block update %d peer = %#v, want owner peer", i, storyUpdate.Peer)
		}
		deleted, ok := storyUpdate.Story.(*tg.StoryItemDeleted)
		if !ok {
			t.Fatalf("block story %d = %T, want *tg.StoryItemDeleted", i, storyUpdate.Story)
		}
		if deleted.ID != i+1 {
			t.Fatalf("block deleted story id = %d, want %d", deleted.ID, i+1)
		}
	}

	if ok, err := r.onContactsUnblock(ownerCtx, &tg.ContactsUnblockRequest{
		MyStoriesFrom: true,
		ID:            &tg.InputPeerUser{UserID: viewer.ID, AccessHash: viewer.AccessHash},
	}); err != nil || !ok {
		t.Fatalf("contacts.unblock = %v, %v", ok, err)
	}
	ownerUnblockDiff, err := r.onUpdatesGetDifference(WithAuthKeyID(WithUserID(ctx, owner.ID), [8]byte{7, 7, 3}), &tg.UpdatesGetDifferenceRequest{Pts: ownerBlockUpdates.State.Pts})
	if err != nil {
		t.Fatalf("get owner unblock difference: %v", err)
	}
	ownerUnblockUpdates, ok := ownerUnblockDiff.(*tg.UpdatesDifference)
	if !ok {
		t.Fatalf("owner unblock difference = %T, want *tg.UpdatesDifference", ownerUnblockDiff)
	}
	if ownerUnblockUpdates.State.Pts != 4 || len(ownerUnblockUpdates.OtherUpdates) != 2 {
		t.Fatalf("owner unblock difference pts/updates = %d/%d, want 4/2", ownerUnblockUpdates.State.Pts, len(ownerUnblockUpdates.OtherUpdates))
	}
	ownerUnblocked, ok := ownerUnblockUpdates.OtherUpdates[0].(*tg.UpdatePeerBlocked)
	if !ok || ownerUnblocked.Blocked || !ownerUnblocked.BlockedMyStoriesFrom {
		t.Fatalf("owner unblock update[0] = %+v (%T), want story unblock", ownerUnblockUpdates.OtherUpdates[0], ownerUnblockUpdates.OtherUpdates[0])
	}
	if peer, ok := ownerUnblocked.PeerID.(*tg.PeerUser); !ok || peer.UserID != viewer.ID {
		t.Fatalf("owner unblock peer = %#v, want viewer", ownerUnblocked.PeerID)
	}
	unblockDiff, err := r.onUpdatesGetDifference(WithAuthKeyID(WithUserID(ctx, viewer.ID), [8]byte{3, 2}), &tg.UpdatesGetDifferenceRequest{Pts: blockUpdates.State.Pts})
	if err != nil {
		t.Fatalf("get viewer unblock difference: %v", err)
	}
	unblockUpdates, ok := unblockDiff.(*tg.UpdatesDifference)
	if !ok {
		t.Fatalf("unblock difference = %T, want *tg.UpdatesDifference", unblockDiff)
	}
	if unblockUpdates.State.Pts != 6 || len(unblockUpdates.OtherUpdates) != 3 {
		t.Fatalf("unblock difference pts/updates = %d/%d, want 6/3", unblockUpdates.State.Pts, len(unblockUpdates.OtherUpdates))
	}
	for i, update := range unblockUpdates.OtherUpdates {
		storyUpdate, ok := update.(*tg.UpdateStory)
		if !ok {
			t.Fatalf("unblock update %d = %T, want *tg.UpdateStory", i, update)
		}
		item, ok := storyUpdate.Story.(*tg.StoryItem)
		if !ok {
			t.Fatalf("unblock story %d = %T, want *tg.StoryItem", i, storyUpdate.Story)
		}
		if item.ID != i+1 {
			t.Fatalf("unblock story id = %d, want %d", item.ID, i+1)
		}
		if _, ok := item.GetViews(); item.Out || ok {
			t.Fatalf("unblock story %d = %+v, want viewer fanout without out/views", i, item)
		}
	}
}

func TestContactsSetBlockedReplacesStoryBlocklistFanouts(t *testing.T) {
	ctx := context.Background()
	users := memory.NewUserStore()
	contactsStore := memory.NewContactStore()
	storyStore := memory.NewStoryStore()
	updateStore := memory.NewUpdateEventStore()
	owner, err := users.Create(ctx, domain.User{AccessHash: 41, Phone: "15550004001", FirstName: "Owner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	oldViewer, err := users.Create(ctx, domain.User{AccessHash: 42, Phone: "15550004002", FirstName: "Old"})
	if err != nil {
		t.Fatalf("create old viewer: %v", err)
	}
	newViewer, err := users.Create(ctx, domain.User{AccessHash: 43, Phone: "15550004003", FirstName: "New"})
	if err != nil {
		t.Fatalf("create new viewer: %v", err)
	}
	for _, viewer := range []domain.User{oldViewer, newViewer} {
		if _, err := contactsStore.Upsert(ctx, owner.ID, domain.ContactInput{ContactUserID: viewer.ID, FirstName: viewer.FirstName}); err != nil {
			t.Fatalf("upsert contact %d: %v", viewer.ID, err)
		}
	}
	if _, err := contactsStore.Block(ctx, owner.ID, oldViewer.ID, 1700000000); err != nil {
		t.Fatalf("pre-block old viewer: %v", err)
	}
	ownerPeer := domain.Peer{Type: domain.PeerTypeUser, ID: owner.ID}
	if _, err := storyStore.UpsertStory(ctx, domain.UpsertStoryRequest{Story: domain.Story{
		Owner:      ownerPeer,
		ID:         1,
		Date:       1700000000,
		ExpireDate: 1700003600,
		Public:     true,
		Out:        true,
		Views:      domain.StoryViews{ViewsCount: 2, HasViewers: true, RecentViewers: []int64{oldViewer.ID, newViewer.ID}},
	}}); err != nil {
		t.Fatalf("upsert story: %v", err)
	}
	r := New(Config{}, Deps{
		Contacts: appcontacts.NewService(contactsStore, users),
		Stories:  appstories.NewService(storyStore),
		Updates:  appupdates.NewService(memory.NewUpdateStateStore(), updateStore),
	}, zaptest.NewLogger(t), fixedClock{now: time.Unix(1700000100, 0)})

	ownerAuth := [8]byte{8, 8, 1}
	ownerCtx := WithSessionID(WithAuthKeyID(WithUserID(ctx, owner.ID), ownerAuth), 61)
	ok, err := r.onContactsSetBlocked(ownerCtx, &tg.ContactsSetBlockedRequest{
		MyStoriesFrom: true,
		ID: []tg.InputPeerClass{
			&tg.InputPeerUser{UserID: newViewer.ID, AccessHash: newViewer.AccessHash},
			&tg.InputPeerUser{UserID: newViewer.ID, AccessHash: newViewer.AccessHash},
		},
		Limit: 2,
	})
	if err != nil || !ok {
		t.Fatalf("contacts.setBlocked = %v, %v", ok, err)
	}

	blockedList, err := r.onContactsGetBlocked(WithUserID(ctx, owner.ID), &tg.ContactsGetBlockedRequest{MyStoriesFrom: true, Limit: 10})
	if err != nil {
		t.Fatalf("contacts.getBlocked: %v", err)
	}
	blocked, ok := blockedList.(*tg.ContactsBlocked)
	if !ok || len(blocked.Blocked) != 1 {
		t.Fatalf("blocked list = %T %+v, want one blocked peer", blockedList, blockedList)
	}
	if peer, ok := blocked.Blocked[0].PeerID.(*tg.PeerUser); !ok || peer.UserID != newViewer.ID {
		t.Fatalf("blocked peer = %#v, want new viewer", blocked.Blocked[0].PeerID)
	}

	ownerDiff, err := r.onUpdatesGetDifference(WithAuthKeyID(WithUserID(ctx, owner.ID), [8]byte{8, 8, 2}), &tg.UpdatesGetDifferenceRequest{Pts: 0})
	if err != nil {
		t.Fatalf("owner difference: %v", err)
	}
	ownerUpdates, ok := ownerDiff.(*tg.UpdatesDifference)
	if !ok {
		t.Fatalf("owner difference = %T, want *tg.UpdatesDifference", ownerDiff)
	}
	if ownerUpdates.State.Pts != 4 || len(ownerUpdates.OtherUpdates) != 4 {
		t.Fatalf("owner difference pts/updates = %d/%d, want 4/4", ownerUpdates.State.Pts, len(ownerUpdates.OtherUpdates))
	}
	blockedUpdate, ok := ownerUpdates.OtherUpdates[0].(*tg.UpdatePeerBlocked)
	if !ok || !blockedUpdate.Blocked || !blockedUpdate.BlockedMyStoriesFrom {
		t.Fatalf("owner update[0] = %+v (%T), want new viewer story block", ownerUpdates.OtherUpdates[0], ownerUpdates.OtherUpdates[0])
	}
	if peer, ok := blockedUpdate.PeerID.(*tg.PeerUser); !ok || peer.UserID != newViewer.ID {
		t.Fatalf("owner block peer = %#v, want new viewer", blockedUpdate.PeerID)
	}
	unblockedUpdate, ok := ownerUpdates.OtherUpdates[2].(*tg.UpdatePeerBlocked)
	if !ok || unblockedUpdate.Blocked || !unblockedUpdate.BlockedMyStoriesFrom {
		t.Fatalf("owner update[2] = %+v (%T), want old viewer story unblock", ownerUpdates.OtherUpdates[2], ownerUpdates.OtherUpdates[2])
	}
	if peer, ok := unblockedUpdate.PeerID.(*tg.PeerUser); !ok || peer.UserID != oldViewer.ID {
		t.Fatalf("owner unblock peer = %#v, want old viewer", unblockedUpdate.PeerID)
	}

	oldDiff, err := r.onUpdatesGetDifference(WithAuthKeyID(WithUserID(ctx, oldViewer.ID), [8]byte{8, 8, 3}), &tg.UpdatesGetDifferenceRequest{Pts: 0})
	if err != nil {
		t.Fatalf("old viewer difference: %v", err)
	}
	oldUpdates, ok := oldDiff.(*tg.UpdatesDifference)
	if !ok || oldUpdates.State.Pts != 1 || len(oldUpdates.OtherUpdates) != 1 {
		t.Fatalf("old viewer difference = %T %+v, want one restored story", oldDiff, oldDiff)
	}
	oldStory, ok := oldUpdates.OtherUpdates[0].(*tg.UpdateStory)
	if !ok {
		t.Fatalf("old viewer update = %T, want updateStory", oldUpdates.OtherUpdates[0])
	}
	if item, ok := oldStory.Story.(*tg.StoryItem); !ok || item.ID != 1 || item.Out {
		t.Fatalf("old viewer story = %+v (%T), want visible non-owner story", oldStory.Story, oldStory.Story)
	}

	newDiff, err := r.onUpdatesGetDifference(WithAuthKeyID(WithUserID(ctx, newViewer.ID), [8]byte{8, 8, 4}), &tg.UpdatesGetDifferenceRequest{Pts: 0})
	if err != nil {
		t.Fatalf("new viewer difference: %v", err)
	}
	newUpdates, ok := newDiff.(*tg.UpdatesDifference)
	if !ok || newUpdates.State.Pts != 1 || len(newUpdates.OtherUpdates) != 1 {
		t.Fatalf("new viewer difference = %T %+v, want one deleted story", newDiff, newDiff)
	}
	newStory, ok := newUpdates.OtherUpdates[0].(*tg.UpdateStory)
	if !ok {
		t.Fatalf("new viewer update = %T, want updateStory", newUpdates.OtherUpdates[0])
	}
	if deleted, ok := newStory.Story.(*tg.StoryItemDeleted); !ok || deleted.ID != 1 {
		t.Fatalf("new viewer story = %+v (%T), want deleted story 1", newStory.Story, newStory.Story)
	}
}

func TestContactsSearchFindsPublicChannels(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, err := userStore.Create(ctx, domain.User{AccessHash: 11, Phone: "15550000011", FirstName: "Owner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	viewer, err := userStore.Create(ctx, domain.User{AccessHash: 12, Phone: "15550000012", FirstName: "Viewer"})
	if err != nil {
		t.Fatalf("create viewer: %v", err)
	}
	stranger, err := userStore.Create(ctx, domain.User{AccessHash: 13, Phone: "15550000013", FirstName: "Stranger"})
	if err != nil {
		t.Fatalf("create stranger: %v", err)
	}
	channelStore := memory.NewChannelStore()
	channelSvc := appchannels.NewService(channelStore)
	created, err := channelSvc.CreateChannel(ctx, owner.ID, domain.CreateChannelRequest{
		Title:         "CU Public RPC",
		Megagroup:     true,
		MemberUserIDs: []int64{viewer.ID},
		Date:          1700000012,
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	public, err := channelSvc.UpdateUsername(ctx, owner.ID, domain.UpdateChannelUsernameRequest{
		ChannelID: created.Channel.ID,
		Username:  "cu_public_rpc",
	})
	if err != nil {
		t.Fatalf("set channel username: %v", err)
	}
	r := New(Config{}, Deps{
		Users:    appusers.NewService(userStore),
		Contacts: appcontacts.NewService(memory.NewContactStore(), userStore),
		Channels: channelSvc,
	}, zaptest.NewLogger(t), clock.System)

	var in bin.Buffer
	if err := (&tg.ContactsSearchRequest{Q: "CU Public", Limit: 20}).Encode(&in); err != nil {
		t.Fatalf("encode request: %v", err)
	}
	enc, err := r.Dispatch(WithUserID(ctx, viewer.ID), [8]byte{}, 0, &in)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	box, ok := enc.(*tg.ContactsFound)
	if !ok {
		t.Fatalf("result type = %T, want *tg.ContactsFound", enc)
	}
	if len(box.MyResults) != 0 || len(box.Results) != 0 || len(box.Chats) != 0 {
		t.Fatalf("member public search = my %d results %d chats %d, want no discovery channel for active member", len(box.MyResults), len(box.Results), len(box.Chats))
	}

	var strangerIn bin.Buffer
	if err := (&tg.ContactsSearchRequest{Q: "CU Public", Limit: 20}).Encode(&strangerIn); err != nil {
		t.Fatalf("encode stranger request: %v", err)
	}
	strangerEnc, err := r.Dispatch(WithUserID(ctx, stranger.ID), [8]byte{}, 0, &strangerIn)
	if err != nil {
		t.Fatalf("dispatch stranger: %v", err)
	}
	strangerBox, ok := strangerEnc.(*tg.ContactsFound)
	if !ok {
		t.Fatalf("stranger result type = %T, want *tg.ContactsFound", strangerEnc)
	}
	if len(strangerBox.Results) != 1 || len(strangerBox.Chats) != 1 {
		t.Fatalf("stranger search sizes = results %d chats %d, want 1/1", len(strangerBox.Results), len(strangerBox.Chats))
	}
	strangerPeer, ok := strangerBox.Results[0].(*tg.PeerChannel)
	if !ok || strangerPeer.ChannelID != public.ID {
		t.Fatalf("stranger peer = %T %+v, want public channel", strangerBox.Results[0], strangerBox.Results[0])
	}
	strangerChat, ok := strangerBox.Chats[0].(*tg.Channel)
	if !ok || !strangerChat.Left || strangerChat.ID != public.ID {
		t.Fatalf("stranger chat = %T %+v, want left public channel", strangerBox.Chats[0], strangerBox.Chats[0])
	}

	resolved, err := r.onContactsResolveUsername(WithUserID(ctx, viewer.ID), &tg.ContactsResolveUsernameRequest{Username: "@CU_PUBLIC_RPC"})
	if err != nil {
		t.Fatalf("resolve username: %v", err)
	}
	resolvedPeer, ok := resolved.Peer.(*tg.PeerChannel)
	if !ok || resolvedPeer.ChannelID != public.ID || len(resolved.Chats) != 1 {
		t.Fatalf("resolved channel = %+v, want peer + chat", resolved)
	}
	resolvedChat, ok := resolved.Chats[0].(*tg.Channel)
	if !ok || resolvedChat.ID != public.ID || resolvedChat.Username != "cu_public_rpc" || resolvedChat.AccessHash != public.AccessHash {
		t.Fatalf("resolved chat = %T %+v, want full public channel projection", resolved.Chats[0], resolved.Chats[0])
	}
	if resolvedChat.Left {
		t.Fatalf("member resolved chat left = true, want active member channel")
	}

	strangerResolved, err := r.onContactsResolveUsername(WithUserID(ctx, stranger.ID), &tg.ContactsResolveUsernameRequest{Username: "@CU_PUBLIC_RPC"})
	if err != nil {
		t.Fatalf("resolve username stranger: %v", err)
	}
	strangerResolvedPeer, ok := strangerResolved.Peer.(*tg.PeerChannel)
	if !ok || strangerResolvedPeer.ChannelID != public.ID || len(strangerResolved.Chats) != 1 {
		t.Fatalf("stranger resolved channel = %+v, want peer + chat", strangerResolved)
	}
	strangerResolvedChat, ok := strangerResolved.Chats[0].(*tg.Channel)
	if !ok || strangerResolvedChat.ID != public.ID || strangerResolvedChat.Username != "cu_public_rpc" || strangerResolvedChat.AccessHash != public.AccessHash {
		t.Fatalf("stranger resolved chat = %T %+v, want full public channel projection", strangerResolved.Chats[0], strangerResolved.Chats[0])
	}
	if !strangerResolvedChat.Left {
		t.Fatalf("stranger resolved chat left = false, want public preview channel")
	}
}

func TestUsernameRPCLifecycle(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, err := userStore.Create(ctx, domain.User{AccessHash: 1, Phone: "15550000001", FirstName: "Owner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	other, err := userStore.Create(ctx, domain.User{AccessHash: 2, Phone: "15550000002", FirstName: "Other", Username: "taken_name"})
	if err != nil {
		t.Fatalf("create other: %v", err)
	}
	r := New(Config{}, Deps{Users: appusers.NewService(userStore)}, zaptest.NewLogger(t), clock.System)
	reqCtx := WithUserID(ctx, owner.ID)

	ok, err := r.onAccountCheckUsername(reqCtx, "taken_name")
	if err != nil || ok {
		t.Fatalf("check occupied = ok %v err %v, want false/nil", ok, err)
	}
	user, err := r.onAccountUpdateUsername(reqCtx, "owner_name")
	if err != nil {
		t.Fatalf("update username: %v", err)
	}
	self, ok := user.(*tg.User)
	if !ok || self.Username != "owner_name" || len(self.Usernames) != 1 || !self.Usernames[0].Active {
		t.Fatalf("updated user = %T %+v, want self with active username", user, user)
	}

	resolved, err := r.onContactsResolveUsername(reqCtx, &tg.ContactsResolveUsernameRequest{Username: "@OWNER_NAME"})
	if err != nil {
		t.Fatalf("resolve username: %v", err)
	}
	peer, ok := resolved.Peer.(*tg.PeerUser)
	if !ok || peer.UserID != owner.ID || len(resolved.Users) != 1 {
		t.Fatalf("resolved username = %+v, want owner peer", resolved)
	}

	resolvedPhone, err := r.onContactsResolvePhone(reqCtx, "+15550000002")
	if err != nil {
		t.Fatalf("resolve phone: %v", err)
	}
	phonePeer, ok := resolvedPhone.Peer.(*tg.PeerUser)
	if !ok || phonePeer.UserID != other.ID {
		t.Fatalf("resolved phone peer = %+v, want other", resolvedPhone.Peer)
	}
}

func TestAccountUpdateProfileRPC(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, err := userStore.Create(ctx, domain.User{AccessHash: 1, Phone: "15550000001", FirstName: "Owner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	r := New(Config{}, Deps{Users: appusers.NewService(userStore)}, zaptest.NewLogger(t), clock.System)
	req := &tg.AccountUpdateProfileRequest{}
	req.SetFirstName("Updated")
	req.SetLastName("User")
	req.SetAbout("profile bio")

	user, err := r.onAccountUpdateProfile(WithUserID(ctx, owner.ID), req)
	if err != nil {
		t.Fatalf("update profile: %v", err)
	}
	self, ok := user.(*tg.User)
	if !ok || self.FirstName != "Updated" || self.LastName != "User" {
		t.Fatalf("updated user = %T %+v, want updated self user", user, user)
	}
	full, err := r.onUsersGetFullUser(WithUserID(ctx, owner.ID), &tg.InputUserSelf{})
	if err != nil {
		t.Fatalf("get full user: %v", err)
	}
	if full.FullUser.About != "profile bio" {
		t.Fatalf("full about = %q, want profile bio", full.FullUser.About)
	}
}

func TestUsersSavedMusicStubsValidateInput(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, err := userStore.Create(ctx, domain.User{AccessHash: 11, Phone: "15550000001", FirstName: "Owner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	friend, err := userStore.Create(ctx, domain.User{AccessHash: 22, Phone: "15550000002", FirstName: "Friend"})
	if err != nil {
		t.Fatalf("create friend: %v", err)
	}
	r := New(Config{}, Deps{Users: appusers.NewService(userStore)}, zaptest.NewLogger(t), clock.System)
	reqCtx := WithUserID(ctx, owner.ID)

	got, err := r.onUsersGetSavedMusic(reqCtx, &tg.UsersGetSavedMusicRequest{
		ID:     &tg.InputUser{UserID: friend.ID, AccessHash: friend.AccessHash},
		Offset: 0,
		Limit:  20,
	})
	if err != nil {
		t.Fatalf("users.getSavedMusic: %v", err)
	}
	music, ok := got.(*tg.UsersSavedMusic)
	if !ok || music.Count != 0 || len(music.Documents) != 0 {
		t.Fatalf("saved music = %T %+v, want empty users.savedMusic", got, got)
	}
	if _, err := r.onUsersGetSavedMusic(reqCtx, &tg.UsersGetSavedMusicRequest{
		ID:    &tg.InputUserSelf{},
		Limit: maxSavedMusicLimit + 1,
	}); err == nil || !strings.Contains(err.Error(), "LIMIT_INVALID") {
		t.Fatalf("large saved music limit err = %v, want LIMIT_INVALID", err)
	}
	if _, err := r.onUsersGetSavedMusic(reqCtx, &tg.UsersGetSavedMusicRequest{
		ID:    &tg.InputUser{UserID: friend.ID, AccessHash: friend.AccessHash + 1},
		Limit: 20,
	}); err == nil || !strings.Contains(err.Error(), "USER_ID_INVALID") {
		t.Fatalf("bad saved music user err = %v, want USER_ID_INVALID", err)
	}
}

func TestUsersGetRequirementsToContactReturnsEmptyRequirements(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, err := userStore.Create(ctx, domain.User{AccessHash: 11, Phone: "15550000001", FirstName: "Owner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	friend, err := userStore.Create(ctx, domain.User{AccessHash: 22, Phone: "15550000002", FirstName: "Friend"})
	if err != nil {
		t.Fatalf("create friend: %v", err)
	}
	r := New(Config{}, Deps{Users: appusers.NewService(userStore)}, zaptest.NewLogger(t), clock.System)
	reqCtx := WithUserID(ctx, owner.ID)

	got, err := r.onUsersGetRequirementsToContact(reqCtx, []tg.InputUserClass{
		&tg.InputUser{UserID: friend.ID, AccessHash: friend.AccessHash},
		&tg.InputUserSelf{},
	})
	if err != nil {
		t.Fatalf("users.getRequirementsToContact: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("requirements len = %d, want 2", len(got))
	}
	for i, req := range got {
		if _, ok := req.(*tg.RequirementToContactEmpty); !ok {
			t.Fatalf("requirement %d = %T, want requirementToContactEmpty", i, req)
		}
	}
	got, err = r.onUsersGetRequirementsToContact(reqCtx, []tg.InputUserClass{
		&tg.InputUser{UserID: friend.ID, AccessHash: friend.AccessHash + 1},
	})
	if err != nil {
		t.Fatalf("bad requirements user should degrade to empty requirement: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("bad requirements len = %d, want 1", len(got))
	}
	if _, ok := got[0].(*tg.RequirementToContactEmpty); !ok {
		t.Fatalf("bad requirements result = %T, want requirementToContactEmpty", got[0])
	}
}

func TestTGUserIncludesRecentlyStatusForTypingEligibility(t *testing.T) {
	got := tgUser(domain.User{ID: 1000000002, FirstName: "Bob"})
	if _, ok := got.Status.(*tg.UserStatusRecently); !ok {
		t.Fatalf("status = %T, want *tg.UserStatusRecently", got.Status)
	}

	online := tgUser(domain.User{
		ID:        1000000002,
		FirstName: "Bob",
		Status:    domain.UserStatus{Kind: domain.UserStatusOnline, Expires: 1700000300},
	})
	if status, ok := online.Status.(*tg.UserStatusOnline); !ok || status.Expires != 1700000300 {
		t.Fatalf("online status = %#v, want userStatusOnline expires=1700000300", online.Status)
	}

	offline := tgUser(domain.User{
		ID:        1000000002,
		FirstName: "Bob",
		Status:    domain.UserStatus{Kind: domain.UserStatusOffline, WasOnline: 1700000000},
	})
	if status, ok := offline.Status.(*tg.UserStatusOffline); !ok || status.WasOnline != 1700000000 {
		t.Fatalf("offline status = %#v, want userStatusOffline was_online=1700000000", offline.Status)
	}
}

func TestAccountUpdateStatusPushesPresenceToOnlineContacts(t *testing.T) {
	ctx := context.Background()
	alice := domain.User{ID: 1000000001, AccessHash: 11, FirstName: "Alice"}
	bob := domain.User{ID: 1000000002, AccessHash: 22, FirstName: "Bob", Contact: true}
	contacts := memory.NewContactStore()
	if err := contacts.SaveList(ctx, alice.ID, domain.ContactList{
		Contacts: []domain.Contact{{User: bob}},
	}); err != nil {
		t.Fatalf("save contacts: %v", err)
	}
	sessions := &captureSessions{onlineUserIDs: []int64{bob.ID}}
	r := New(Config{}, Deps{
		Contacts: appcontacts.NewService(contacts),
		Sessions: sessions,
	}, zaptest.NewLogger(t), clock.System)
	callCtx := WithSessionID(WithUserID(ctx, alice.ID), 77)

	ok, err := r.onAccountUpdateStatus(callCtx, false)
	if err != nil || !ok {
		t.Fatalf("account.updateStatus online = %v, %v", ok, err)
	}
	gotPushes := sessions.pushedUserIDs()
	if !reflect.DeepEqual(gotPushes, []int64{alice.ID, bob.ID}) {
		t.Fatalf("pushed users = %+v, want self and online contact", gotPushes)
	}
	update := pushedUserStatus(t, sessions.snapshot().message)
	if update.UserID != alice.ID {
		t.Fatalf("status user = %d, want alice", update.UserID)
	}
	if status, ok := update.Status.(*tg.UserStatusOnline); !ok || status.Expires <= int(time.Now().Unix()) {
		t.Fatalf("status = %#v, want online with future expires", update.Status)
	}

	ok, err = r.onAccountUpdateStatus(callCtx, true)
	if err != nil || !ok {
		t.Fatalf("account.updateStatus offline = %v, %v", ok, err)
	}
	update = pushedUserStatus(t, sessions.snapshot().message)
	if update.UserID != alice.ID {
		t.Fatalf("offline status user = %d, want alice", update.UserID)
	}
	if status, ok := update.Status.(*tg.UserStatusOffline); !ok || status.WasOnline == 0 {
		t.Fatalf("status = %#v, want offline with was_online", update.Status)
	}
}

func TestAccountUpdateStatusPersistsLastSeen(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	alice, err := userStore.Create(ctx, domain.User{AccessHash: 11, Phone: "1001", FirstName: "Alice"})
	if err != nil {
		t.Fatalf("create alice: %v", err)
	}
	const now = 1700000200
	r := New(Config{}, Deps{
		Users: appusers.NewService(userStore),
	}, zaptest.NewLogger(t), fixedClock{now: time.Unix(now, 0)})

	ok, err := r.onAccountUpdateStatus(WithSessionID(WithUserID(ctx, alice.ID), 77), false)
	if err != nil || !ok {
		t.Fatalf("account.updateStatus online = %v, %v", ok, err)
	}
	stored, found, err := userStore.ByID(ctx, alice.ID)
	if err != nil || !found {
		t.Fatalf("load alice found=%v err=%v", found, err)
	}
	if stored.LastSeenAt != now {
		t.Fatalf("last_seen_at after online = %d, want %d", stored.LastSeenAt, now)
	}
}

func TestContactsStatusesUsesPersistedLastSeen(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	alice, err := userStore.Create(ctx, domain.User{AccessHash: 11, Phone: "1001", FirstName: "Alice"})
	if err != nil {
		t.Fatalf("create alice: %v", err)
	}
	bob, err := userStore.Create(ctx, domain.User{AccessHash: 22, Phone: "1002", FirstName: "Bob"})
	if err != nil {
		t.Fatalf("create bob: %v", err)
	}
	const lastSeen = 1700000000
	if err := userStore.UpdateLastSeen(ctx, bob.ID, lastSeen); err != nil {
		t.Fatalf("update last seen: %v", err)
	}
	contacts := memory.NewContactStore()
	if err := contacts.SaveList(ctx, alice.ID, domain.ContactList{
		Contacts: []domain.Contact{{User: domain.User{ID: bob.ID, FirstName: "Old Bob", Contact: true}}},
	}); err != nil {
		t.Fatalf("save contacts: %v", err)
	}
	r := New(Config{}, Deps{
		Contacts: appcontacts.NewService(contacts, userStore),
		Users:    appusers.NewService(userStore),
	}, zaptest.NewLogger(t), clock.System)

	statuses, err := r.onContactsGetStatuses(WithUserID(ctx, alice.ID))
	if err != nil {
		t.Fatalf("contacts.getStatuses: %v", err)
	}
	if len(statuses) != 1 || statuses[0].UserID != bob.ID {
		t.Fatalf("statuses = %+v, want bob only", statuses)
	}
	if status, ok := statuses[0].Status.(*tg.UserStatusOffline); !ok || status.WasOnline != lastSeen {
		t.Fatalf("status = %#v, want userStatusOffline was_online=%d", statuses[0].Status, lastSeen)
	}
}

func TestContactsStatusesUsePresenceAndContactsKeepStableProjection(t *testing.T) {
	ctx := context.Background()
	alice := domain.User{ID: 1000000001, AccessHash: 11, FirstName: "Alice"}
	bob := domain.User{ID: 1000000002, AccessHash: 22, FirstName: "Bob", Contact: true}
	contacts := memory.NewContactStore()
	if err := contacts.SaveList(ctx, alice.ID, domain.ContactList{
		Contacts: []domain.Contact{{User: bob}},
	}); err != nil {
		t.Fatalf("save contacts: %v", err)
	}
	r := New(Config{}, Deps{
		Contacts: appcontacts.NewService(contacts),
	}, zaptest.NewLogger(t), clock.System)

	if ok, err := r.onAccountUpdateStatus(WithSessionID(WithUserID(ctx, bob.ID), 22), false); err != nil || !ok {
		t.Fatalf("bob account.updateStatus = %v, %v", ok, err)
	}
	statuses, err := r.onContactsGetStatuses(WithUserID(ctx, alice.ID))
	if err != nil {
		t.Fatalf("contacts.getStatuses: %v", err)
	}
	if len(statuses) != 1 || statuses[0].UserID != bob.ID {
		t.Fatalf("statuses = %+v, want bob only", statuses)
	}
	if _, ok := statuses[0].Status.(*tg.UserStatusOnline); !ok {
		t.Fatalf("contacts.getStatuses status = %T, want online", statuses[0].Status)
	}

	contactsRes, err := r.onContactsGetContacts(WithUserID(ctx, alice.ID), 0)
	if err != nil {
		t.Fatalf("contacts.getContacts: %v", err)
	}
	full, ok := contactsRes.(*tg.ContactsContacts)
	if !ok || len(full.Users) != 1 {
		t.Fatalf("contacts result = %T %+v, want one full contacts response", contactsRes, contactsRes)
	}
	user, ok := full.Users[0].(*tg.User)
	if !ok || user.ID != bob.ID {
		t.Fatalf("contacts user = %T %+v, want bob", full.Users[0], full.Users[0])
	}
	if _, ok := user.Status.(*tg.UserStatusRecently); !ok {
		t.Fatalf("contacts user status = %T, want stable non-presence projection", user.Status)
	}
}

func TestContactsAcceptContactReturnsSettingsAndReset(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	contactsStore := memory.NewContactStore()
	alice, err := userStore.Create(ctx, domain.User{AccessHash: 11, Phone: "1001", FirstName: "Alice", LastName: "A"})
	if err != nil {
		t.Fatalf("create alice: %v", err)
	}
	bob, err := userStore.Create(ctx, domain.User{AccessHash: 22, Phone: "1002", FirstName: "Bob", LastName: "B"})
	if err != nil {
		t.Fatalf("create bob: %v", err)
	}
	contactsSvc := appcontacts.NewService(contactsStore, userStore)
	if _, err := contactsSvc.AddContact(ctx, alice.ID, domain.ContactInput{
		ContactUserID: bob.ID,
		Phone:         bob.Phone,
		FirstName:     "Bobby",
		LastName:      "Remark",
	}); err != nil {
		t.Fatalf("alice add bob: %v", err)
	}
	updatesSvc := &captureUpdates{state: domain.UpdateState{Pts: 10, Date: 1700000400}}
	r := New(Config{}, Deps{
		Contacts: contactsSvc,
		Users:    appusers.NewService(userStore, appusers.WithContactStore(contactsStore)),
		Updates:  updatesSvc,
	}, zaptest.NewLogger(t), fixedClock{now: time.Unix(1700000400, 0)})

	out, err := r.onContactsAcceptContact(WithUserID(ctx, alice.ID), &tg.InputUser{UserID: bob.ID, AccessHash: bob.AccessHash})
	if err != nil {
		t.Fatalf("contacts.acceptContact: %v", err)
	}
	got, ok := out.(*tg.Updates)
	if !ok {
		t.Fatalf("updates = %T, want *tg.Updates", out)
	}
	if len(got.Updates) != 2 {
		t.Fatalf("updates = %+v, want peer settings + contacts reset", got.Updates)
	}
	settings, ok := got.Updates[0].(*tg.UpdatePeerSettings)
	if !ok {
		t.Fatalf("update[0] = %T, want UpdatePeerSettings", got.Updates[0])
	}
	if settings.Settings.ShareContact || settings.Settings.AddContact {
		t.Fatalf("peer settings = %+v, want share/add false", settings.Settings)
	}
	if _, ok := got.Updates[1].(*tg.UpdateContactsReset); !ok {
		t.Fatalf("update[1] = %T, want UpdateContactsReset", got.Updates[1])
	}
	if len(updatesSvc.events) != 4 {
		t.Fatalf("recorded events = %+v, want current peer/reset and target peer/reset", updatesSvc.events)
	}
	if updatesSvc.events[0].UserID != alice.ID || updatesSvc.events[0].Settings.ShareContact {
		t.Fatalf("current peer settings event = %+v, want alice share=false", updatesSvc.events[0])
	}
	if updatesSvc.events[2].UserID != bob.ID || updatesSvc.events[2].Settings.ShareContact {
		t.Fatalf("target peer settings event = %+v, want bob share=false", updatesSvc.events[2])
	}
	reverse, found, err := contactsStore.Get(ctx, bob.ID, alice.ID)
	if err != nil || !found {
		t.Fatalf("bob contact alice found=%v err=%v", found, err)
	}
	if reverse.Phone != alice.Phone || !reverse.Mutual {
		t.Fatalf("bob contact alice = %+v, want shared phone and mutual", reverse)
	}
}

func TestContactsAddContactPhonePrivacyExceptionUpdatesPeerSettings(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	contactsStore := memory.NewContactStore()
	privacySvc := appprivacy.NewService(memory.NewPrivacyStore(), contactsStore)
	alice, err := userStore.Create(ctx, domain.User{AccessHash: 31, Phone: "2001", FirstName: "Alice", LastName: "A"})
	if err != nil {
		t.Fatalf("create alice: %v", err)
	}
	bob, err := userStore.Create(ctx, domain.User{AccessHash: 32, Phone: "2002", FirstName: "Bob", LastName: "B"})
	if err != nil {
		t.Fatalf("create bob: %v", err)
	}
	carol, err := userStore.Create(ctx, domain.User{AccessHash: 33, Phone: "2003", FirstName: "Carol", LastName: "C"})
	if err != nil {
		t.Fatalf("create carol: %v", err)
	}
	contactsSvc := appcontacts.NewService(contactsStore, userStore).Configure(appcontacts.WithPrivacyEvaluator(privacySvc))
	usersSvc := appusers.NewService(userStore, appusers.WithContactStore(contactsStore), appusers.WithPrivacyEvaluator(privacySvc))
	r := New(Config{}, Deps{
		Contacts: contactsSvc,
		Users:    usersSvc,
	}, zaptest.NewLogger(t), fixedClock{now: time.Unix(1700000410, 0)})

	withoutException, err := r.onContactsAddContact(WithUserID(ctx, alice.ID), &tg.ContactsAddContactRequest{
		ID:        &tg.InputUser{UserID: bob.ID, AccessHash: bob.AccessHash},
		Phone:     bob.Phone,
		FirstName: "Bobby",
	})
	if err != nil {
		t.Fatalf("contacts.addContact without exception: %v", err)
	}
	bobSettings := firstPeerSettingsUpdate(t, withoutException)
	if bobSettings.Settings.AddContact || !bobSettings.Settings.ShareContact || !bobSettings.Settings.NeedContactsException {
		t.Fatalf("bob peer settings = %+v, want share + need exception", bobSettings.Settings)
	}
	if allowed, err := privacySvc.CanSee(ctx, alice.ID, bob.ID, domain.PrivacyKeyPhoneNumber); err != nil {
		t.Fatalf("bob can see alice phone: %v", err)
	} else if allowed {
		t.Fatalf("bob can see alice phone = true, want false before exception")
	}

	withException, err := r.onContactsAddContact(WithUserID(ctx, alice.ID), &tg.ContactsAddContactRequest{
		AddPhonePrivacyException: true,
		ID:                       &tg.InputUser{UserID: carol.ID, AccessHash: carol.AccessHash},
		Phone:                    carol.Phone,
		FirstName:                "Carol",
	})
	if err != nil {
		t.Fatalf("contacts.addContact with exception: %v", err)
	}
	carolSettings := firstPeerSettingsUpdate(t, withException)
	if carolSettings.Settings.AddContact || carolSettings.Settings.ShareContact || carolSettings.Settings.NeedContactsException {
		t.Fatalf("carol peer settings = %+v, want no add/share/need exception", carolSettings.Settings)
	}
	if allowed, err := privacySvc.CanSee(ctx, alice.ID, carol.ID, domain.PrivacyKeyPhoneNumber); err != nil {
		t.Fatalf("carol can see alice phone: %v", err)
	} else if !allowed {
		t.Fatalf("carol can see alice phone = false, want true after exception")
	}
}

func firstPeerSettingsUpdate(t *testing.T, updates tg.UpdatesClass) *tg.UpdatePeerSettings {
	t.Helper()
	got, ok := updates.(*tg.Updates)
	if !ok {
		t.Fatalf("updates = %T, want *tg.Updates", updates)
	}
	if len(got.Updates) == 0 {
		t.Fatalf("updates = %+v, want UpdatePeerSettings", got.Updates)
	}
	settings, ok := got.Updates[0].(*tg.UpdatePeerSettings)
	if !ok {
		t.Fatalf("update[0] = %T, want UpdatePeerSettings", got.Updates[0])
	}
	return settings
}

func TestContactsStatusesUsesOnlineSessionFallback(t *testing.T) {
	ctx := context.Background()
	alice := domain.User{ID: 1000000001, AccessHash: 11, FirstName: "Alice"}
	bob := domain.User{ID: 1000000002, AccessHash: 22, FirstName: "Bob", Contact: true}
	contacts := memory.NewContactStore()
	if err := contacts.SaveList(ctx, alice.ID, domain.ContactList{
		Contacts: []domain.Contact{{User: bob}},
	}); err != nil {
		t.Fatalf("save contacts: %v", err)
	}
	r := New(Config{}, Deps{
		Contacts: appcontacts.NewService(contacts),
		Sessions: &captureSessions{onlineUserIDs: []int64{bob.ID}},
	}, zaptest.NewLogger(t), clock.System)

	statuses, err := r.onContactsGetStatuses(WithUserID(ctx, alice.ID))
	if err != nil {
		t.Fatalf("contacts.getStatuses: %v", err)
	}
	if len(statuses) != 1 || statuses[0].UserID != bob.ID {
		t.Fatalf("statuses = %+v, want bob only", statuses)
	}
	if status, ok := statuses[0].Status.(*tg.UserStatusOnline); !ok || status.Expires <= int(time.Now().Unix()) {
		t.Fatalf("fallback status = %#v, want online from active session", statuses[0].Status)
	}
}

func TestContactsStatusesHonorsExplicitOffline(t *testing.T) {
	ctx := context.Background()
	alice := domain.User{ID: 1000000001, AccessHash: 11, FirstName: "Alice"}
	bob := domain.User{ID: 1000000002, AccessHash: 22, FirstName: "Bob", Contact: true}
	contacts := memory.NewContactStore()
	if err := contacts.SaveList(ctx, alice.ID, domain.ContactList{
		Contacts: []domain.Contact{{User: bob}},
	}); err != nil {
		t.Fatalf("save contacts: %v", err)
	}
	r := New(Config{}, Deps{
		Contacts: appcontacts.NewService(contacts),
		Sessions: &captureSessions{onlineUserIDs: []int64{bob.ID}},
	}, zaptest.NewLogger(t), clock.System)
	if ok, err := r.onAccountUpdateStatus(WithSessionID(WithUserID(ctx, bob.ID), 22), true); err != nil || !ok {
		t.Fatalf("bob account.updateStatus offline = %v, %v", ok, err)
	}

	statuses, err := r.onContactsGetStatuses(WithUserID(ctx, alice.ID))
	if err != nil {
		t.Fatalf("contacts.getStatuses: %v", err)
	}
	if len(statuses) != 1 || statuses[0].UserID != bob.ID {
		t.Fatalf("statuses = %+v, want bob only", statuses)
	}
	if status, ok := statuses[0].Status.(*tg.UserStatusOffline); !ok || status.WasOnline == 0 {
		t.Fatalf("status = %#v, want explicit offline", statuses[0].Status)
	}
}

func TestSessionOfflinePersistsLastSeenAndPushes(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	alice, err := userStore.Create(ctx, domain.User{AccessHash: 11, Phone: "1001", FirstName: "Alice"})
	if err != nil {
		t.Fatalf("create alice: %v", err)
	}
	bob, err := userStore.Create(ctx, domain.User{AccessHash: 22, Phone: "1002", FirstName: "Bob"})
	if err != nil {
		t.Fatalf("create bob: %v", err)
	}
	contacts := memory.NewContactStore()
	if err := contacts.SaveList(ctx, bob.ID, domain.ContactList{Contacts: []domain.Contact{{User: alice}}}); err != nil {
		t.Fatalf("save bob contacts: %v", err)
	}
	const now = 1700000300
	sessions := &captureSessions{onlineUserIDs: []int64{alice.ID}}
	r := New(Config{}, Deps{
		Contacts: appcontacts.NewService(contacts, userStore),
		Users:    appusers.NewService(userStore),
		Sessions: sessions,
	}, zaptest.NewLogger(t), fixedClock{now: time.Unix(now, 0)})

	// offline 广播经去抖宽限后异步执行；缩短宽限并轮询等待。
	grace := offlineAnnounceGrace
	offlineAnnounceGrace = 10 * time.Millisecond
	defer func() { offlineAnnounceGrace = grace }()

	r.SessionOffline([8]byte{1, 2, 3}, 22, bob.ID, true)

	deadline := time.Now().Add(5 * time.Second)
	for len(sessions.pushedUserIDs()) < 2 {
		if time.Now().After(deadline) {
			t.Fatalf("offline announce never fired; pushed users = %+v", sessions.pushedUserIDs())
		}
		time.Sleep(5 * time.Millisecond)
	}

	stored, found, err := userStore.ByID(ctx, bob.ID)
	if err != nil || !found {
		t.Fatalf("load bob found=%v err=%v", found, err)
	}
	if stored.LastSeenAt != now {
		t.Fatalf("last_seen_at = %d, want %d", stored.LastSeenAt, now)
	}
	if got := sessions.pushedUserIDs(); !reflect.DeepEqual(got, []int64{bob.ID, alice.ID}) {
		t.Fatalf("pushed users = %+v, want bob self and online contact alice", got)
	}
	update := pushedUserStatus(t, sessions.snapshot().message)
	if update.UserID != bob.ID {
		t.Fatalf("status user = %d, want bob", update.UserID)
	}
	if status, ok := update.Status.(*tg.UserStatusOffline); !ok || status.WasOnline != now {
		t.Fatalf("status = %#v, want offline was_online=%d", update.Status, now)
	}
}

// TestSessionOfflineSkipsAnnounceWhenUserReconnects 验证去抖宽限内用户重新上线时
// 跳过 offline 广播——移动端重连/换 session 是「先断旧再建新」，没有去抖会对
// 全部好友广播一轮 offline→online 抖动。
func TestSessionOfflineSkipsAnnounceWhenUserReconnects(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	bob, err := userStore.Create(ctx, domain.User{AccessHash: 22, Phone: "1002", FirstName: "Bob"})
	if err != nil {
		t.Fatalf("create bob: %v", err)
	}
	const now = 1700000300
	// bob 在 IsUserOnline 名单里：模拟宽限期内已重新上线。
	sessions := &captureSessions{onlineUserIDs: []int64{bob.ID}}
	r := New(Config{}, Deps{
		Users:    appusers.NewService(userStore),
		Sessions: sessions,
	}, zaptest.NewLogger(t), fixedClock{now: time.Unix(now, 0)})

	grace := offlineAnnounceGrace
	offlineAnnounceGrace = 10 * time.Millisecond
	defer func() { offlineAnnounceGrace = grace }()

	r.SessionOffline([8]byte{1, 2, 3}, 22, bob.ID, true)
	time.Sleep(20 * offlineAnnounceGrace)

	if got := sessions.pushedUserIDs(); len(got) != 0 {
		t.Fatalf("pushed users = %+v, want none (user reconnected within grace)", got)
	}
	stored, found, err := userStore.ByID(ctx, bob.ID)
	if err != nil || !found {
		t.Fatalf("load bob found=%v err=%v", found, err)
	}
	if stored.LastSeenAt != 0 {
		t.Fatalf("last_seen_at = %d, want untouched 0", stored.LastSeenAt)
	}
}

func TestAccountUpdateStatusKeepsUserOnlineUntilAllSessionsOffline(t *testing.T) {
	const userID = int64(1000000001)
	r := New(Config{}, Deps{}, zaptest.NewLogger(t), clock.System)
	session1 := WithSessionID(WithUserID(context.Background(), userID), 1)
	session2 := WithSessionID(WithUserID(context.Background(), userID), 2)

	if ok, err := r.onAccountUpdateStatus(session1, false); err != nil || !ok {
		t.Fatalf("session1 online = %v, %v", ok, err)
	}
	if ok, err := r.onAccountUpdateStatus(session2, false); err != nil || !ok {
		t.Fatalf("session2 online = %v, %v", ok, err)
	}
	if ok, err := r.onAccountUpdateStatus(session1, true); err != nil || !ok {
		t.Fatalf("session1 offline = %v, %v", ok, err)
	}
	if status := r.userPresenceStatus(userID); status.Kind != domain.UserStatusOnline {
		t.Fatalf("aggregate after one offline = %+v, want online", status)
	}
	if ok, err := r.onAccountUpdateStatus(session2, true); err != nil || !ok {
		t.Fatalf("session2 offline = %v, %v", ok, err)
	}
	if status := r.userPresenceStatus(userID); status.Kind != domain.UserStatusOffline || status.WasOnline == 0 {
		t.Fatalf("aggregate after all offline = %+v, want offline", status)
	}
}

func TestContactsBlockGetBlockedAndUnblockRPC(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	alice, err := userStore.Create(ctx, domain.User{AccessHash: 11, Phone: "15550009001", FirstName: "Alice"})
	if err != nil {
		t.Fatalf("create alice: %v", err)
	}
	bob, err := userStore.Create(ctx, domain.User{AccessHash: 22, Phone: "15550009002", FirstName: "Bob"})
	if err != nil {
		t.Fatalf("create bob: %v", err)
	}
	r := New(Config{}, Deps{
		Users:    appusers.NewService(userStore),
		Contacts: appcontacts.NewService(memory.NewContactStore(), userStore),
	}, zaptest.NewLogger(t), clock.System)

	ok, err := r.onContactsBlock(WithUserID(ctx, bob.ID), &tg.ContactsBlockRequest{
		ID: &tg.InputPeerUser{UserID: alice.ID, AccessHash: alice.AccessHash},
	})
	if err != nil || !ok {
		t.Fatalf("contacts.block = %v, %v", ok, err)
	}
	blocked, err := r.onContactsGetBlocked(WithUserID(ctx, bob.ID), &tg.ContactsGetBlockedRequest{Limit: 10})
	if err != nil {
		t.Fatalf("contacts.getBlocked: %v", err)
	}
	full, ok := blocked.(*tg.ContactsBlocked)
	if !ok || len(full.Blocked) != 1 || len(full.Users) != 1 {
		t.Fatalf("blocked = %T %+v, want one blocked user", blocked, blocked)
	}
	if peer, ok := full.Blocked[0].PeerID.(*tg.PeerUser); !ok || peer.UserID != alice.ID {
		t.Fatalf("blocked peer = %#v, want alice", full.Blocked[0].PeerID)
	}
	if user, ok := full.Users[0].(*tg.User); !ok || user.ID != alice.ID {
		t.Fatalf("blocked user = %#v, want alice", full.Users[0])
	}

	ok, err = r.onContactsUnblock(WithUserID(ctx, bob.ID), &tg.ContactsUnblockRequest{
		ID: &tg.InputPeerUser{UserID: alice.ID, AccessHash: alice.AccessHash},
	})
	if err != nil || !ok {
		t.Fatalf("contacts.unblock = %v, %v", ok, err)
	}
	blocked, err = r.onContactsGetBlocked(WithUserID(ctx, bob.ID), &tg.ContactsGetBlockedRequest{Limit: 10})
	if err != nil {
		t.Fatalf("contacts.getBlocked after unblock: %v", err)
	}
	if full, ok := blocked.(*tg.ContactsBlocked); !ok || len(full.Blocked) != 0 {
		t.Fatalf("blocked after unblock = %T %+v, want empty contacts.blocked", blocked, blocked)
	}
}
