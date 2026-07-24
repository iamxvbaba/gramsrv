package memory

import (
	"context"
	"errors"
	"testing"

	"telesrv/internal/domain"
)

func TestStoryStoreReadMaxAndPeerMaxIDs(t *testing.T) {
	ctx := context.Background()
	store := NewStoryStore()
	owner := domain.Peer{Type: domain.PeerTypeUser, ID: 1001}
	for _, id := range []int{1, 2} {
		if _, err := store.UpsertStory(ctx, domain.UpsertStoryRequest{Story: domain.Story{
			Owner:      owner,
			ID:         id,
			Date:       100 + id,
			ExpireDate: 1000,
			Public:     true,
		}}); err != nil {
			t.Fatalf("upsert story %d: %v", id, err)
		}
	}

	read, err := store.MarkRead(ctx, 2001, owner, 2, 200)
	if err != nil {
		t.Fatalf("mark read: %v", err)
	}
	if !read.Advanced || read.MaxReadID != 2 {
		t.Fatalf("read = %+v, want advanced max 2", read)
	}
	read, err = store.MarkRead(ctx, 2001, owner, 1, 201)
	if err != nil {
		t.Fatalf("mark read lower: %v", err)
	}
	if read.Advanced || read.MaxReadID != 2 {
		t.Fatalf("lower read = %+v, want unchanged max 2", read)
	}

	peerStories, err := store.GetPeerStories(ctx, 2001, owner, 300)
	if err != nil {
		t.Fatalf("get peer stories: %v", err)
	}
	if peerStories.MaxReadID != 2 || len(peerStories.Stories) != 2 {
		t.Fatalf("peer stories = %+v, want max read 2 and two stories", peerStories)
	}
	recent, err := store.GetPeerMaxIDs(ctx, 2001, []domain.Peer{owner}, 300)
	if err != nil {
		t.Fatalf("peer max ids: %v", err)
	}
	if len(recent) != 1 || recent[0].MaxID != 2 {
		t.Fatalf("recent = %+v, want max id 2", recent)
	}
	projections, err := store.GetPeerStoryProjections(ctx, 2001, []domain.Peer{owner}, 300)
	if err != nil {
		t.Fatalf("peer projections: %v", err)
	}
	if len(projections) != 1 || projections[0].Peer != owner || projections[0].Recent.MaxID != 2 || projections[0].Hidden {
		t.Fatalf("projections = %+v, want max id 2 and visible owner", projections)
	}
}

func TestStoryStorePeerHiddenStates(t *testing.T) {
	ctx := context.Background()
	store := NewStoryStore()
	viewerID := int64(2001)
	owner := domain.Peer{Type: domain.PeerTypeUser, ID: 1001}
	channel := domain.Peer{Type: domain.PeerTypeChannel, ID: 3001}

	states, err := store.GetPeerHiddenStates(ctx, viewerID, []domain.Peer{owner, channel})
	if err != nil {
		t.Fatalf("initial hidden states: %v", err)
	}
	if states[owner] || states[channel] {
		t.Fatalf("initial hidden states = %+v, want both false", states)
	}
	if err := store.SetPeerHidden(ctx, viewerID, owner, true); err != nil {
		t.Fatalf("set owner hidden: %v", err)
	}
	states, err = store.GetPeerHiddenStates(ctx, viewerID, []domain.Peer{owner, channel})
	if err != nil {
		t.Fatalf("hidden states after set: %v", err)
	}
	if !states[owner] || states[channel] {
		t.Fatalf("hidden states after set = %+v, want owner true channel false", states)
	}
	projections, err := store.GetPeerStoryProjections(ctx, viewerID, []domain.Peer{owner, channel}, 300)
	if err != nil {
		t.Fatalf("story projections after set: %v", err)
	}
	if len(projections) != 2 || !projections[0].Hidden || projections[1].Hidden {
		t.Fatalf("story projections after set = %+v, want owner hidden channel visible", projections)
	}
	if err := store.SetPeerHidden(ctx, viewerID, owner, false); err != nil {
		t.Fatalf("clear owner hidden: %v", err)
	}
	states, err = store.GetPeerHiddenStates(ctx, viewerID, []domain.Peer{owner})
	if err != nil {
		t.Fatalf("hidden states after clear: %v", err)
	}
	if states[owner] {
		t.Fatalf("hidden states after clear = %+v, want owner false", states)
	}
}

func TestStoryStoreListActiveStoriesPaginatesByPeer(t *testing.T) {
	ctx := context.Background()
	store := NewStoryStore()
	owners := []domain.Peer{
		{Type: domain.PeerTypeUser, ID: 1001},
		{Type: domain.PeerTypeUser, ID: 1002},
		{Type: domain.PeerTypeUser, ID: 1003},
	}
	for i, owner := range owners {
		if _, err := store.UpsertStory(ctx, domain.UpsertStoryRequest{Story: domain.Story{
			Owner:      owner,
			ID:         1,
			Date:       300 - i*100,
			ExpireDate: 1000,
			Public:     true,
		}}); err != nil {
			t.Fatalf("upsert owner %d story: %v", owner.ID, err)
		}
	}
	if _, err := store.UpsertStory(ctx, domain.UpsertStoryRequest{Story: domain.Story{
		Owner:      owners[0],
		ID:         2,
		Date:       250,
		ExpireDate: 1000,
		Public:     true,
	}}); err != nil {
		t.Fatalf("upsert second story for first owner: %v", err)
	}

	first, err := store.ListActiveStoriesPage(ctx, 2001, false, 400, domain.StoryListCursor{}, 2)
	if err != nil {
		t.Fatalf("first page: %v", err)
	}
	if first.Count != 3 || !first.HasMore || len(first.Peers) != 2 {
		t.Fatalf("first page = count %d more %v peers %d, want count 3 more true peers 2", first.Count, first.HasMore, len(first.Peers))
	}
	if first.Peers[0].Peer != owners[0] || len(first.Peers[0].Stories) != 2 {
		t.Fatalf("first peer = %+v stories %d, want owner 1001 with two stories", first.Peers[0].Peer, len(first.Peers[0].Stories))
	}

	next, err := store.ListActiveStoriesPage(ctx, 2001, false, 400, domain.StoryListCursor{
		Set:  true,
		Date: 200,
		Peer: owners[1],
	}, 2)
	if err != nil {
		t.Fatalf("next page: %v", err)
	}
	if next.Count != 3 || next.HasMore || len(next.Peers) != 1 || next.Peers[0].Peer != owners[2] {
		t.Fatalf("next page = %+v, want final page with owner 1003", next)
	}
	digest, err := store.ActiveStoriesDigest(ctx, 2001, false, 400)
	if err != nil {
		t.Fatalf("digest active stories: %v", err)
	}
	if digest.Count != 3 {
		t.Fatalf("digest count = %d, want 3", digest.Count)
	}
	if _, err := store.MarkRead(ctx, 2001, owners[0], 2, 401); err != nil {
		t.Fatalf("mark read for digest: %v", err)
	}
	changed, err := store.ActiveStoriesDigest(ctx, 2001, false, 400)
	if err != nil {
		t.Fatalf("changed digest active stories: %v", err)
	}
	if changed.Hash == digest.Hash {
		t.Fatalf("digest hash unchanged after read boundary: %#x", changed.Hash)
	}
}

func TestStoryStoreSelfUserViewAndReactionDoNotPolluteOwnerInteractions(t *testing.T) {
	ctx := context.Background()
	store := NewStoryStore()
	owner := domain.Peer{Type: domain.PeerTypeUser, ID: 1001}
	if _, err := store.UpsertStory(ctx, domain.UpsertStoryRequest{Story: domain.Story{
		Owner:      owner,
		ID:         1,
		Date:       100,
		ExpireDate: 1000,
		Public:     true,
	}}); err != nil {
		t.Fatalf("upsert story: %v", err)
	}

	created, err := store.IncrementViews(ctx, owner.ID, owner, []int{1}, 200)
	if err != nil {
		t.Fatalf("increment self view: %v", err)
	}
	if created != 0 {
		t.Fatalf("created self views = %d, want 0", created)
	}
	if _, err := store.SetReaction(ctx, owner.ID, owner, 1, &domain.MessageReaction{
		Type:     domain.MessageReactionEmoji,
		Emoticon: "🔥",
	}, 201); err != domain.ErrStoryPeerInvalid {
		t.Fatalf("self reaction err = %v, want ErrStoryPeerInvalid", err)
	}
	list, err := store.ListStoryViews(ctx, domain.StoryViewListRequest{
		ViewerUserID: owner.ID,
		Owner:        owner,
		StoryID:      1,
		Limit:        10,
	})
	if err != nil {
		t.Fatalf("list story views: %v", err)
	}
	if list.Count != 0 || list.ViewsCount != 0 || list.ReactionsCount != 0 || len(list.Views) != 0 {
		t.Fatalf("self view list = %+v, want empty counters", list)
	}
}

func TestStoryStoreBlocklistHidesOwnerStoriesFromViewer(t *testing.T) {
	ctx := context.Background()
	store := NewStoryStore()
	owner := domain.Peer{Type: domain.PeerTypeUser, ID: 1001}
	blockedViewerID := int64(2001)
	otherViewerID := int64(2002)
	if _, err := store.UpsertStory(ctx, domain.UpsertStoryRequest{Story: domain.Story{
		Owner:      owner,
		ID:         1,
		Date:       100,
		ExpireDate: 1000,
		Public:     true,
	}}); err != nil {
		t.Fatalf("upsert story: %v", err)
	}
	if created, err := store.IncrementViews(ctx, blockedViewerID, owner, []int{1}, 105); err != nil || created != 1 {
		t.Fatalf("pre-block increment = %d, %v, want 1 nil", created, err)
	}
	store.SetStoryBlockedUsers(owner.ID, blockedViewerID)

	peerStories, err := store.GetPeerStories(ctx, blockedViewerID, owner, 200)
	if err != nil {
		t.Fatalf("get peer stories: %v", err)
	}
	if len(peerStories.Stories) != 0 {
		t.Fatalf("blocked peer stories = %+v, want empty", peerStories.Stories)
	}
	exact, err := store.GetStoriesByID(ctx, blockedViewerID, owner, []int{1}, 200)
	if err != nil {
		t.Fatalf("get stories by id: %v", err)
	}
	if len(exact.Stories) != 0 {
		t.Fatalf("blocked exact stories = %+v, want empty", exact.Stories)
	}
	recent, err := store.GetPeerMaxIDs(ctx, blockedViewerID, []domain.Peer{owner}, 200)
	if err != nil {
		t.Fatalf("get peer max ids: %v", err)
	}
	if len(recent) != 1 || recent[0].MaxID != 0 {
		t.Fatalf("blocked recent = %+v, want max id 0", recent)
	}
	active, err := store.ListActiveStories(ctx, blockedViewerID, false, 200, 100)
	if err != nil {
		t.Fatalf("list active stories: %v", err)
	}
	if active.Count != 0 || len(active.Stories) != 0 {
		t.Fatalf("blocked active stories = %+v, want empty", active)
	}
	if created, err := store.IncrementViews(ctx, blockedViewerID, owner, []int{1}, 210); err != nil || created != 0 {
		t.Fatalf("blocked increment = %d, %v, want 0 nil", created, err)
	}
	if _, err := store.SetReaction(ctx, blockedViewerID, owner, 1, &domain.MessageReaction{
		Type:     domain.MessageReactionEmoji,
		Emoticon: "🔥",
	}, 211); err != domain.ErrStoryNotFound {
		t.Fatalf("blocked reaction err = %v, want ErrStoryNotFound", err)
	}

	ownerStories, err := store.GetPeerStories(ctx, owner.ID, owner, 200)
	if err != nil {
		t.Fatalf("owner get peer stories: %v", err)
	}
	if len(ownerStories.Stories) != 1 {
		t.Fatalf("owner peer stories = %+v, want one story", ownerStories.Stories)
	}
	otherStories, err := store.GetPeerStories(ctx, otherViewerID, owner, 200)
	if err != nil {
		t.Fatalf("other get peer stories: %v", err)
	}
	if len(otherStories.Stories) != 1 {
		t.Fatalf("other peer stories = %+v, want one story", otherStories.Stories)
	}
	if created, err := store.IncrementViews(ctx, otherViewerID, owner, []int{1}, 220); err != nil || created != 1 {
		t.Fatalf("other increment = %d, %v, want 1 nil", created, err)
	}
	list, err := store.ListStoryViews(ctx, domain.StoryViewListRequest{
		ViewerUserID: owner.ID,
		Owner:        owner,
		StoryID:      1,
		Limit:        10,
	})
	if err != nil {
		t.Fatalf("list story views: %v", err)
	}
	got := map[int64]domain.StoryView{}
	for _, view := range list.Views {
		got[view.ViewerID] = view
	}
	if list.Count != 2 || len(list.Views) != 2 {
		t.Fatalf("view list = %+v, want historical blocked viewer and other viewer", list)
	}
	if !got[blockedViewerID].BlockedMyStoriesFrom {
		t.Fatalf("blocked viewer row = %+v, want blocked_my_stories_from", got[blockedViewerID])
	}
	if got[otherViewerID].BlockedMyStoriesFrom {
		t.Fatalf("other viewer row = %+v, want not blocked", got[otherViewerID])
	}
}

func TestStoryStoreChannelStoriesRequireActiveMember(t *testing.T) {
	ctx := context.Background()
	store := NewStoryStore()
	channel := domain.Peer{Type: domain.PeerTypeChannel, ID: 3001}
	memberID := int64(2001)
	outsiderID := int64(2002)
	store.SetStoryChannelMembers(channel.ID, memberID)

	if _, err := store.CreateStory(ctx, domain.StoryCreateRequest{
		Owner:    channel,
		RandomID: 300101,
		Date:     100,
		Period:   86400,
		Public:   true,
	}); err != nil {
		t.Fatalf("create channel story: %v", err)
	}

	memberStories, err := store.GetPeerStories(ctx, memberID, channel, 200)
	if err != nil {
		t.Fatalf("member get peer stories: %v", err)
	}
	if len(memberStories.Stories) != 1 || memberStories.Stories[0].Owner != channel {
		t.Fatalf("member stories = %+v, want channel story", memberStories.Stories)
	}
	outsiderStories, err := store.GetPeerStories(ctx, outsiderID, channel, 200)
	if err != nil {
		t.Fatalf("outsider get peer stories: %v", err)
	}
	if len(outsiderStories.Stories) != 0 {
		t.Fatalf("outsider stories = %+v, want empty", outsiderStories.Stories)
	}
	exact, err := store.GetStoriesByID(ctx, outsiderID, channel, []int{1}, 200)
	if err != nil {
		t.Fatalf("outsider exact story: %v", err)
	}
	if len(exact.Stories) != 0 {
		t.Fatalf("outsider exact = %+v, want empty", exact.Stories)
	}
	recent, err := store.GetPeerMaxIDs(ctx, outsiderID, []domain.Peer{channel}, 200)
	if err != nil {
		t.Fatalf("outsider peer max ids: %v", err)
	}
	if len(recent) != 1 || recent[0].MaxID != 0 {
		t.Fatalf("outsider recent = %+v, want max id 0", recent)
	}
	active, err := store.ListActiveStories(ctx, outsiderID, false, 200, 100)
	if err != nil {
		t.Fatalf("outsider active stories: %v", err)
	}
	if active.Count != 0 || len(active.Stories) != 0 {
		t.Fatalf("outsider active stories = %+v, want empty", active)
	}
	if created, err := store.IncrementViews(ctx, outsiderID, channel, []int{1}, 201); err != nil || created != 0 {
		t.Fatalf("outsider increment = %d, %v, want 0 nil", created, err)
	}
	if _, err := store.SetReaction(ctx, outsiderID, channel, 1, &domain.MessageReaction{
		Type:     domain.MessageReactionEmoji,
		Emoticon: "like",
	}, 202); !errors.Is(err, domain.ErrStoryNotFound) {
		t.Fatalf("outsider reaction err = %v, want ErrStoryNotFound", err)
	}
}

func TestStoryStoreMediaAreasRoundTripEditClearAndClone(t *testing.T) {
	ctx := context.Background()
	store := NewStoryStore()
	owner := domain.Peer{Type: domain.PeerTypeUser, ID: 1001}
	created, err := store.CreateStory(ctx, domain.StoryCreateRequest{
		Owner:      owner,
		RandomID:   101,
		Date:       100,
		Period:     900,
		Public:     true,
		MediaAreas: []domain.StoryMediaArea{testDomainStoryMediaArea("🔥", 10)},
	})
	if err != nil {
		t.Fatalf("create story: %v", err)
	}
	assertDomainStoryMediaArea(t, created.Story, "🔥", 10)
	created.Story.MediaAreas[0].Reaction.Emoticon = "mutated"

	list, err := store.GetStoriesByID(ctx, owner.ID, owner, []int{created.Story.ID, created.Story.ID}, 200)
	if err != nil {
		t.Fatalf("get stories by id: %v", err)
	}
	if len(list.Stories) != 1 {
		t.Fatalf("stories by duplicate ids = %+v, want one story", list.Stories)
	}
	assertDomainStoryMediaArea(t, list.Stories[0], "🔥", 10)
	if _, err := store.GetStoriesByID(ctx, owner.ID, owner, nil, 200); !errors.Is(err, domain.ErrStoryIDInvalid) {
		t.Fatalf("empty get stories by id err = %v, want ErrStoryIDInvalid", err)
	}

	edited, err := store.EditStory(ctx, domain.StoryEditRequest{
		Owner:            owner,
		ID:               created.Story.ID,
		UpdateMediaAreas: true,
		MediaAreas:       []domain.StoryMediaArea{testDomainStoryURLMediaArea("https://example.com/story/link", 25)},
	})
	if err != nil {
		t.Fatalf("edit story media areas: %v", err)
	}
	assertDomainStoryURLMediaArea(t, edited.Story, "https://example.com/story/link", 25)
	edited.Story.MediaAreas[0].URL = "https://mutated.invalid"

	list, err = store.GetStoriesByID(ctx, owner.ID, owner, []int{created.Story.ID}, 201)
	if err != nil {
		t.Fatalf("get edited story by id: %v", err)
	}
	assertDomainStoryURLMediaArea(t, list.Stories[0], "https://example.com/story/link", 25)

	geoEdited, err := store.EditStory(ctx, domain.StoryEditRequest{
		Owner:            owner,
		ID:               created.Story.ID,
		UpdateMediaAreas: true,
		MediaAreas:       []domain.StoryMediaArea{testDomainStoryGeoPointMediaArea(31.2304, 121.4737, 35)},
	})
	if err != nil {
		t.Fatalf("edit story geo media area: %v", err)
	}
	assertDomainStoryGeoPointMediaArea(t, geoEdited.Story, 31.2304, 121.4737, 35)
	geoEdited.Story.MediaAreas[0].Geo.Lat = 0
	geoEdited.Story.MediaAreas[0].GeoAddress.City = "Mutated"

	list, err = store.GetStoriesByID(ctx, owner.ID, owner, []int{created.Story.ID}, 202)
	if err != nil {
		t.Fatalf("get geo edited story by id: %v", err)
	}
	assertDomainStoryGeoPointMediaArea(t, list.Stories[0], 31.2304, 121.4737, 35)

	venueEdited, err := store.EditStory(ctx, domain.StoryEditRequest{
		Owner:            owner,
		ID:               created.Story.ID,
		UpdateMediaAreas: true,
		MediaAreas:       []domain.StoryMediaArea{testDomainStoryVenueMediaArea("Inline Cafe", 31.231, 121.474, 40)},
	})
	if err != nil {
		t.Fatalf("edit story venue media area: %v", err)
	}
	assertDomainStoryVenueMediaArea(t, venueEdited.Story, "Inline Cafe", 31.231, 121.474, 40)
	venueEdited.Story.MediaAreas[0].Venue.Title = "Mutated"

	list, err = store.GetStoriesByID(ctx, owner.ID, owner, []int{created.Story.ID}, 203)
	if err != nil {
		t.Fatalf("get venue edited story by id: %v", err)
	}
	assertDomainStoryVenueMediaArea(t, list.Stories[0], "Inline Cafe", 31.231, 121.474, 40)

	weatherEdited, err := store.EditStory(ctx, domain.StoryEditRequest{
		Owner:            owner,
		ID:               created.Story.ID,
		UpdateMediaAreas: true,
		MediaAreas:       []domain.StoryMediaArea{testDomainStoryWeatherMediaArea("☀️", 22.5, 0x00cc6600, 45)},
	})
	if err != nil {
		t.Fatalf("edit story weather media area: %v", err)
	}
	assertDomainStoryWeatherMediaArea(t, weatherEdited.Story, "☀️", 22.5, 0x00cc6600, 45)
	weatherEdited.Story.MediaAreas[0].WeatherEmoji = "mutated"
	weatherEdited.Story.MediaAreas[0].TemperatureC = -100
	weatherEdited.Story.MediaAreas[0].Color = 0

	list, err = store.GetStoriesByID(ctx, owner.ID, owner, []int{created.Story.ID}, 204)
	if err != nil {
		t.Fatalf("get weather edited story by id: %v", err)
	}
	assertDomainStoryWeatherMediaArea(t, list.Stories[0], "☀️", 22.5, 0x00cc6600, 45)

	channelPostEdited, err := store.EditStory(ctx, domain.StoryEditRequest{
		Owner:            owner,
		ID:               created.Story.ID,
		UpdateMediaAreas: true,
		MediaAreas:       []domain.StoryMediaArea{testDomainStoryChannelPostMediaArea(777001, 42, 50)},
	})
	if err != nil {
		t.Fatalf("edit story channel post media area: %v", err)
	}
	assertDomainStoryChannelPostMediaArea(t, channelPostEdited.Story, 777001, 42, 50)
	channelPostEdited.Story.MediaAreas[0].ChannelID = 0
	channelPostEdited.Story.MediaAreas[0].MsgID = 0

	list, err = store.GetStoriesByID(ctx, owner.ID, owner, []int{created.Story.ID}, 205)
	if err != nil {
		t.Fatalf("get channel post edited story by id: %v", err)
	}
	assertDomainStoryChannelPostMediaArea(t, list.Stories[0], 777001, 42, 50)

	starGiftEdited, err := store.EditStory(ctx, domain.StoryEditRequest{
		Owner:            owner,
		ID:               created.Story.ID,
		UpdateMediaAreas: true,
		MediaAreas:       []domain.StoryMediaArea{testDomainStoryStarGiftMediaArea("Gift.Series_01-42", 55)},
	})
	if err != nil {
		t.Fatalf("edit story star gift media area: %v", err)
	}
	assertDomainStoryStarGiftMediaArea(t, starGiftEdited.Story, "Gift.Series_01-42", 55)
	starGiftEdited.Story.MediaAreas[0].StarGiftSlug = "mutated"

	list, err = store.GetStoriesByID(ctx, owner.ID, owner, []int{created.Story.ID}, 206)
	if err != nil {
		t.Fatalf("get star gift edited story by id: %v", err)
	}
	assertDomainStoryStarGiftMediaArea(t, list.Stories[0], "Gift.Series_01-42", 55)

	cleared, err := store.EditStory(ctx, domain.StoryEditRequest{
		Owner:            owner,
		ID:               created.Story.ID,
		UpdateMediaAreas: true,
	})
	if err != nil {
		t.Fatalf("clear story media areas: %v", err)
	}
	if len(cleared.Story.MediaAreas) != 0 {
		t.Fatalf("cleared story media areas = %+v, want none", cleared.Story.MediaAreas)
	}
}

func TestStoryStoreForwardRoundTripEditAndClone(t *testing.T) {
	ctx := context.Background()
	store := NewStoryStore()
	owner := domain.Peer{Type: domain.PeerTypeUser, ID: 1002}
	source := domain.Peer{Type: domain.PeerTypeUser, ID: 2002}
	created, err := store.CreateStory(ctx, domain.StoryCreateRequest{
		Owner:    owner,
		RandomID: 202,
		Date:     200,
		Period:   900,
		Public:   true,
		Forward: &domain.StoryForward{
			From:     source,
			StoryID:  7,
			Modified: true,
		},
	})
	if err != nil {
		t.Fatalf("create story: %v", err)
	}
	assertDomainStoryForward(t, created.Story, source, 7, true)
	created.Story.Forward.From.ID = 9999
	created.Story.Forward.StoryID = 99

	list, err := store.GetStoriesByID(ctx, owner.ID, owner, []int{created.Story.ID}, 201)
	if err != nil {
		t.Fatalf("get story by id: %v", err)
	}
	if len(list.Stories) != 1 {
		t.Fatalf("stories by id = %+v, want one story", list.Stories)
	}
	assertDomainStoryForward(t, list.Stories[0], source, 7, true)

	edited, err := store.EditStory(ctx, domain.StoryEditRequest{
		Owner:         owner,
		ID:            created.Story.ID,
		UpdateCaption: true,
		Caption:       "edited repost caption",
	})
	if err != nil {
		t.Fatalf("edit story caption: %v", err)
	}
	assertDomainStoryForward(t, edited.Story, source, 7, true)
	edited.Story.Forward.From.ID = 9998

	list, err = store.GetStoriesByID(ctx, owner.ID, owner, []int{created.Story.ID}, 202)
	if err != nil {
		t.Fatalf("get edited story by id: %v", err)
	}
	assertDomainStoryForward(t, list.Stories[0], source, 7, true)

	hidden, err := store.CreateStory(ctx, domain.StoryCreateRequest{
		Owner:    owner,
		RandomID: 203,
		Date:     203,
		Period:   900,
		Public:   true,
		Forward: &domain.StoryForward{
			FromName: "Alice Hidden",
			StoryID:  8,
		},
	})
	if err != nil {
		t.Fatalf("create hidden-author story: %v", err)
	}
	assertDomainStoryForwardName(t, hidden.Story, "Alice Hidden", 8, false)
	hidden.Story.Forward.From = source
	hidden.Story.Forward.FromName = "mutated"
	hidden.Story.Forward.StoryID = 88

	list, err = store.GetStoriesByID(ctx, owner.ID, owner, []int{hidden.Story.ID}, 204)
	if err != nil {
		t.Fatalf("get hidden-author story by id: %v", err)
	}
	if len(list.Stories) != 1 {
		t.Fatalf("hidden-author stories by id = %+v, want one story", list.Stories)
	}
	assertDomainStoryForwardName(t, list.Stories[0], "Alice Hidden", 8, false)

	editedHidden, err := store.EditStory(ctx, domain.StoryEditRequest{
		Owner:         owner,
		ID:            hidden.Story.ID,
		UpdateCaption: true,
		Caption:       "edited hidden repost caption",
	})
	if err != nil {
		t.Fatalf("edit hidden-author story caption: %v", err)
	}
	assertDomainStoryForwardName(t, editedHidden.Story, "Alice Hidden", 8, false)
}

func TestStoryStorePublicRepostForwardCountAndViewsList(t *testing.T) {
	ctx := context.Background()
	store := NewStoryStore()
	sourceOwner := domain.Peer{Type: domain.PeerTypeUser, ID: 1101}
	repostOwner := domain.Peer{Type: domain.PeerTypeUser, ID: 2202}
	source, err := store.CreateStory(ctx, domain.StoryCreateRequest{
		Owner:    sourceOwner,
		RandomID: 1101,
		Date:     100,
		Period:   86400,
		Public:   true,
	})
	if err != nil {
		t.Fatalf("create source story: %v", err)
	}
	repost, err := store.CreateStory(ctx, domain.StoryCreateRequest{
		Owner:    repostOwner,
		RandomID: 2202,
		Date:     200,
		Period:   86400,
		Public:   true,
		Forward: &domain.StoryForward{
			From:    sourceOwner,
			StoryID: source.Story.ID,
		},
	})
	if err != nil {
		t.Fatalf("create repost story: %v", err)
	}
	sourceList, err := store.GetStoriesByID(ctx, sourceOwner.ID, sourceOwner, []int{source.Story.ID}, 201)
	if err != nil {
		t.Fatalf("get source story: %v", err)
	}
	if len(sourceList.Stories) != 1 || sourceList.Stories[0].Views.ForwardsCount != 1 {
		t.Fatalf("source story = %+v, want forwards_count=1", sourceList.Stories)
	}
	views, err := store.ListStoryViews(ctx, domain.StoryViewListRequest{
		ViewerUserID:  sourceOwner.ID,
		Owner:         sourceOwner,
		StoryID:       source.Story.ID,
		Limit:         20,
		ForwardsFirst: true,
	})
	if err != nil {
		t.Fatalf("list story views: %v", err)
	}
	if views.Count != 1 || views.ForwardsCount != 1 || len(views.Views) != 1 || views.Views[0].Repost == nil {
		t.Fatalf("views list = %+v, want one repost interaction", views)
	}
	if views.Views[0].Repost.Owner != repostOwner || views.Views[0].Repost.ID != repost.Story.ID {
		t.Fatalf("repost interaction = %+v, want owner %+v story %d", views.Views[0].Repost, repostOwner, repost.Story.ID)
	}
	if _, err := store.DeleteStories(ctx, repostOwner, []int{repost.Story.ID}, 202); err != nil {
		t.Fatalf("delete repost: %v", err)
	}
	sourceList, err = store.GetStoriesByID(ctx, sourceOwner.ID, sourceOwner, []int{source.Story.ID}, 202)
	if err != nil {
		t.Fatalf("get source story after delete: %v", err)
	}
	if len(sourceList.Stories) != 1 || sourceList.Stories[0].Views.ForwardsCount != 0 {
		t.Fatalf("source story after delete = %+v, want forwards_count=0", sourceList.Stories)
	}
}

func TestStoryStorePublicForwardListReturnsRepostsOnly(t *testing.T) {
	ctx := context.Background()
	store := NewStoryStore()
	sourceOwner := domain.Peer{Type: domain.PeerTypeChannel, ID: 3311}
	repostOwner := domain.Peer{Type: domain.PeerTypeChannel, ID: 4412}
	privateRepostOwner := domain.Peer{Type: domain.PeerTypeChannel, ID: 4413}
	source, err := store.CreateStory(ctx, domain.StoryCreateRequest{
		Owner:    sourceOwner,
		RandomID: 331101,
		Date:     100,
		Period:   86400,
		Public:   true,
	})
	if err != nil {
		t.Fatalf("create source story: %v", err)
	}
	repost, err := store.CreateStory(ctx, domain.StoryCreateRequest{
		Owner:    repostOwner,
		RandomID: 441201,
		Date:     200,
		Period:   86400,
		Public:   true,
		Forward: &domain.StoryForward{
			From:    sourceOwner,
			StoryID: source.Story.ID,
		},
	})
	if err != nil {
		t.Fatalf("create public repost: %v", err)
	}
	hiddenRepost, err := store.CreateStory(ctx, domain.StoryCreateRequest{
		Owner:    repostOwner,
		RandomID: 441202,
		Date:     202,
		Period:   86400,
		Public:   true,
		Forward: &domain.StoryForward{
			Source:   sourceOwner,
			FromName: "Hidden Source",
			StoryID:  source.Story.ID,
		},
	})
	if err != nil {
		t.Fatalf("create hidden-source public repost: %v", err)
	}
	if _, err := store.CreateStory(ctx, domain.StoryCreateRequest{
		Owner:    privateRepostOwner,
		RandomID: 441301,
		Date:     201,
		Period:   86400,
		Public:   false,
		Forward: &domain.StoryForward{
			From:    sourceOwner,
			StoryID: source.Story.ID,
		},
	}); err != nil {
		t.Fatalf("create private repost: %v", err)
	}
	list, err := store.ListStoryPublicForwards(ctx, domain.StoryPublicForwardListRequest{
		ViewerUserID: sourceOwner.ID,
		Owner:        sourceOwner,
		StoryID:      source.Story.ID,
		Limit:        20,
	})
	if err != nil {
		t.Fatalf("list story public forwards: %v", err)
	}
	if list.Count != 2 || len(list.Forwards) != 2 || list.Forwards[0].Repost == nil || list.Forwards[1].Repost == nil {
		t.Fatalf("public forwards = %+v, want two public reposts", list)
	}
	if list.Forwards[0].Repost.ID != hiddenRepost.Story.ID ||
		list.Forwards[0].Repost.Forward == nil ||
		list.Forwards[0].Repost.Forward.From != (domain.Peer{}) ||
		list.Forwards[0].Repost.Forward.Source != sourceOwner ||
		list.Forwards[0].Repost.Forward.FromName != "Hidden Source" {
		t.Fatalf("hidden public forward repost = %+v, want from_name-only header with source %+v", list.Forwards[0].Repost, sourceOwner)
	}
	if list.Forwards[1].Repost.Owner != repostOwner || list.Forwards[1].Repost.ID != repost.Story.ID {
		t.Fatalf("public forward repost = %+v, want owner %+v story %d", list.Forwards[1].Repost, repostOwner, repost.Story.ID)
	}
	if _, err := store.DeleteStories(ctx, repostOwner, []int{repost.Story.ID}, 202); err != nil {
		t.Fatalf("delete repost: %v", err)
	}
	afterVisibleDelete, err := store.ListStoryPublicForwards(ctx, domain.StoryPublicForwardListRequest{
		ViewerUserID: sourceOwner.ID,
		Owner:        sourceOwner,
		StoryID:      source.Story.ID,
		Limit:        20,
	})
	if err != nil {
		t.Fatalf("list story public forwards after delete: %v", err)
	}
	if afterVisibleDelete.Count != 1 || len(afterVisibleDelete.Forwards) != 1 || afterVisibleDelete.Forwards[0].Repost == nil ||
		afterVisibleDelete.Forwards[0].Repost.ID != hiddenRepost.Story.ID {
		t.Fatalf("public forwards after visible delete = %+v, want hidden repost only", afterVisibleDelete)
	}
	if _, err := store.DeleteStories(ctx, repostOwner, []int{hiddenRepost.Story.ID}, 203); err != nil {
		t.Fatalf("delete hidden repost: %v", err)
	}
	empty, err := store.ListStoryPublicForwards(ctx, domain.StoryPublicForwardListRequest{
		ViewerUserID: sourceOwner.ID,
		Owner:        sourceOwner,
		StoryID:      source.Story.ID,
		Limit:        20,
	})
	if err != nil {
		t.Fatalf("list story public forwards after hidden delete: %v", err)
	}
	if empty.Count != 0 || len(empty.Forwards) != 0 {
		t.Fatalf("public forwards after delete = %+v, want empty", empty)
	}
}

func TestStoryStorePublicRepostStoryReactionsList(t *testing.T) {
	ctx := context.Background()
	store := NewStoryStore()
	sourceOwner := domain.Peer{Type: domain.PeerTypeChannel, ID: 3301}
	repostOwner := domain.Peer{Type: domain.PeerTypeUser, ID: 4402}
	source, err := store.CreateStory(ctx, domain.StoryCreateRequest{
		Owner:    sourceOwner,
		RandomID: 3301,
		Date:     100,
		Period:   86400,
		Public:   true,
	})
	if err != nil {
		t.Fatalf("create source story: %v", err)
	}
	if _, err := store.SetReaction(ctx, 5503, sourceOwner, source.Story.ID, &domain.MessageReaction{
		Type:     domain.MessageReactionEmoji,
		Emoticon: "🔥",
	}, 150); err != nil {
		t.Fatalf("set reaction: %v", err)
	}
	repost, err := store.CreateStory(ctx, domain.StoryCreateRequest{
		Owner:    repostOwner,
		RandomID: 4402,
		Date:     200,
		Period:   86400,
		Public:   true,
		Forward: &domain.StoryForward{
			From:    sourceOwner,
			StoryID: source.Story.ID,
		},
	})
	if err != nil {
		t.Fatalf("create repost story: %v", err)
	}
	list, err := store.ListStoryReactions(ctx, domain.StoryReactionListRequest{
		ViewerUserID:  sourceOwner.ID,
		Owner:         sourceOwner,
		StoryID:       source.Story.ID,
		Limit:         20,
		ForwardsFirst: true,
	})
	if err != nil {
		t.Fatalf("list story reactions: %v", err)
	}
	if list.Count != 2 || len(list.Reactions) != 2 || list.Reactions[0].Repost == nil {
		t.Fatalf("reaction list = %+v, want repost first plus reaction", list)
	}
	if list.Reactions[0].Repost.Owner != repostOwner || list.Reactions[0].Repost.ID != repost.Story.ID {
		t.Fatalf("repost reaction = %+v, want owner %+v story %d", list.Reactions[0].Repost, repostOwner, repost.Story.ID)
	}
	filtered, err := store.ListStoryReactions(ctx, domain.StoryReactionListRequest{
		ViewerUserID: sourceOwner.ID,
		Owner:        sourceOwner,
		StoryID:      source.Story.ID,
		Limit:        20,
		Reaction: &domain.MessageReaction{
			Type:     domain.MessageReactionEmoji,
			Emoticon: "🔥",
		},
	})
	if err != nil {
		t.Fatalf("list filtered story reactions: %v", err)
	}
	if filtered.Count != 1 || len(filtered.Reactions) != 1 || filtered.Reactions[0].Repost != nil || filtered.Reactions[0].ViewerID != 5503 {
		t.Fatalf("filtered reaction list = %+v, want only emoji reactor", filtered)
	}
	if _, err := store.DeleteStories(ctx, repostOwner, []int{repost.Story.ID}, 202); err != nil {
		t.Fatalf("delete repost: %v", err)
	}
	list, err = store.ListStoryReactions(ctx, domain.StoryReactionListRequest{
		ViewerUserID:  sourceOwner.ID,
		Owner:         sourceOwner,
		StoryID:       source.Story.ID,
		Limit:         20,
		ForwardsFirst: true,
	})
	if err != nil {
		t.Fatalf("list story reactions after delete: %v", err)
	}
	if list.Count != 1 || len(list.Reactions) != 1 || list.Reactions[0].Repost != nil {
		t.Fatalf("reaction list after delete = %+v, want only durable reaction", list)
	}
}

func TestStoryStoreListStoriesArchiveCountAndSeek(t *testing.T) {
	ctx := context.Background()
	store := NewStoryStore()
	owner := domain.Peer{Type: domain.PeerTypeUser, ID: 1001}
	fixtures := []domain.Story{
		{Owner: owner, ID: 1, Date: 101, ExpireDate: 900, Public: true},
		{Owner: owner, ID: 2, Date: 102, ExpireDate: 1200, Public: true},
		{Owner: owner, ID: 3, Date: 103, ExpireDate: 900, Public: true, Pinned: true},
		{Owner: owner, ID: 4, Date: 104, ExpireDate: 900, Public: true, Deleted: true},
		{Owner: owner, ID: 5, Date: 105, ExpireDate: 900, Public: true},
		{Owner: domain.Peer{Type: domain.PeerTypeUser, ID: 1002}, ID: 1, Date: 106, ExpireDate: 900, Public: true},
	}
	for _, story := range fixtures {
		if _, err := store.UpsertStory(ctx, domain.UpsertStoryRequest{Story: story}); err != nil {
			t.Fatalf("upsert story %d/%d: %v", story.Owner.ID, story.ID, err)
		}
	}

	countOnly, err := store.ListStoriesArchive(ctx, owner.ID, owner, 0, 0, 1000)
	if err != nil {
		t.Fatalf("count archive stories: %v", err)
	}
	if countOnly.Count != 3 || len(countOnly.Stories) != 0 {
		t.Fatalf("count-only archive = %+v, want count 3 and no page", countOnly)
	}
	first, err := store.ListStoriesArchive(ctx, owner.ID, owner, 0, 2, 1000)
	if err != nil {
		t.Fatalf("list archive first page: %v", err)
	}
	if first.Count != 3 || !sameStoryIDs(storyIDs(first.Stories), []int{5, 3}) {
		t.Fatalf("first archive page = count %d ids %v, want count 3 ids 5,3", first.Count, storyIDs(first.Stories))
	}
	if !first.Stories[1].Pinned || !first.Stories[0].Out || !first.Stories[1].Out {
		t.Fatalf("first archive stories = %+v, want pinned expired retained and owner out=true", first.Stories)
	}
	second, err := store.ListStoriesArchive(ctx, owner.ID, owner, 3, 2, 1000)
	if err != nil {
		t.Fatalf("list archive second page: %v", err)
	}
	if second.Count != 3 || !sameStoryIDs(storyIDs(second.Stories), []int{1}) {
		t.Fatalf("second archive page = count %d ids %v, want count 3 ids 1", second.Count, storyIDs(second.Stories))
	}
}

func TestStoryStoreListOwnerActiveStoriesReturnsFanoutSnapshots(t *testing.T) {
	ctx := context.Background()
	store := NewStoryStore()
	owner := domain.Peer{Type: domain.PeerTypeUser, ID: 1001}
	other := domain.Peer{Type: domain.PeerTypeUser, ID: 1002}
	reaction := &domain.MessageReaction{Type: domain.MessageReactionEmoji, Emoticon: "👍"}
	for _, story := range []domain.Story{
		{
			Owner:        owner,
			ID:           1,
			Date:         100,
			ExpireDate:   1000,
			CloseFriends: true,
			Out:          true,
			Views:        domain.StoryViews{ViewsCount: 1, HasViewers: true, RecentViewers: []int64{2001}},
			SentReaction: reaction,
		},
		{Owner: owner, ID: 2, Date: 101, ExpireDate: 90, CloseFriends: true},
		{Owner: owner, ID: 3, Date: 102, ExpireDate: 1000, CloseFriends: true, Deleted: true},
		{Owner: other, ID: 1, Date: 103, ExpireDate: 1000, CloseFriends: true},
	} {
		if _, err := store.UpsertStory(ctx, domain.UpsertStoryRequest{Story: story}); err != nil {
			t.Fatalf("upsert story %d/%d: %v", story.Owner.ID, story.ID, err)
		}
	}

	list, err := store.ListOwnerActiveStories(ctx, owner, 200, 100)
	if err != nil {
		t.Fatalf("list owner active stories: %v", err)
	}
	if len(list.Stories) != 1 || list.Stories[0].ID != 1 {
		t.Fatalf("owner active stories = %+v, want only story 1", list.Stories)
	}
	story := list.Stories[0]
	if story.Out || story.Views.HasViewers || story.Views.ViewsCount != 0 || story.SentReaction != nil {
		t.Fatalf("fanout snapshot = %+v, want no out/views/reaction", story)
	}
}

func testDomainStoryMediaArea(emoticon string, x float64) domain.StoryMediaArea {
	return domain.StoryMediaArea{
		Kind: domain.StoryMediaAreaSuggestedReaction,
		Coordinates: domain.StoryMediaAreaCoordinates{
			X:         x,
			Y:         10,
			W:         10,
			H:         10,
			Rotation:  15,
			Radius:    8,
			HasRadius: true,
		},
		Dark:     true,
		Flipped:  true,
		Reaction: &domain.MessageReaction{Type: domain.MessageReactionEmoji, Emoticon: emoticon},
	}
}

func assertDomainStoryMediaArea(t *testing.T, story domain.Story, wantEmoticon string, wantX float64) {
	t.Helper()
	if len(story.MediaAreas) != 1 {
		t.Fatalf("story media areas = %+v, want one area", story.MediaAreas)
	}
	area := story.MediaAreas[0]
	if area.Kind != domain.StoryMediaAreaSuggestedReaction || area.Reaction == nil {
		t.Fatalf("story media area = %+v, want suggested reaction", area)
	}
	if area.Reaction.Type != domain.MessageReactionEmoji || area.Reaction.Emoticon != wantEmoticon {
		t.Fatalf("story media area reaction = %+v, want emoji %q", area.Reaction, wantEmoticon)
	}
	if area.Coordinates.X != wantX || area.Coordinates.Y != 10 || area.Coordinates.W != 10 || area.Coordinates.H != 10 ||
		area.Coordinates.Rotation != 15 || !area.Coordinates.HasRadius || area.Coordinates.Radius != 8 {
		t.Fatalf("story media area coordinates = %+v, want x %v y/w/h 10 rotation 15 radius 8", area.Coordinates, wantX)
	}
	if !area.Dark || !area.Flipped {
		t.Fatalf("story media area flags = dark %v flipped %v, want true/true", area.Dark, area.Flipped)
	}
}

func testDomainStoryURLMediaArea(url string, x float64) domain.StoryMediaArea {
	return domain.StoryMediaArea{
		Kind: domain.StoryMediaAreaURL,
		Coordinates: domain.StoryMediaAreaCoordinates{
			X:        x,
			Y:        10,
			W:        20,
			H:        10,
			Rotation: 15,
		},
		URL: url,
	}
}

func assertDomainStoryURLMediaArea(t *testing.T, story domain.Story, wantURL string, wantX float64) {
	t.Helper()
	if len(story.MediaAreas) != 1 {
		t.Fatalf("story media areas = %+v, want one url area", story.MediaAreas)
	}
	area := story.MediaAreas[0]
	if area.Kind != domain.StoryMediaAreaURL || area.URL != wantURL {
		t.Fatalf("story media area = %+v, want url %q", area, wantURL)
	}
	if area.Coordinates.X != wantX || area.Coordinates.Y != 10 || area.Coordinates.W != 20 || area.Coordinates.H != 10 ||
		area.Coordinates.Rotation != 15 {
		t.Fatalf("story media area coordinates = %+v, want x %v y 10 w 20 h 10 rotation 15", area.Coordinates, wantX)
	}
}

func testDomainStoryGeoPointMediaArea(lat, long, x float64) domain.StoryMediaArea {
	return domain.StoryMediaArea{
		Kind: domain.StoryMediaAreaGeoPoint,
		Coordinates: domain.StoryMediaAreaCoordinates{
			X:        x,
			Y:        12,
			W:        22,
			H:        11,
			Rotation: 20,
		},
		Geo: &domain.MessageGeoPoint{
			Lat:        lat,
			Long:       long,
			AccessHash: 123456,
		},
		GeoAddress: &domain.StoryGeoPointAddress{
			CountryISO2: "CN",
			State:       "Shanghai",
			City:        "Shanghai",
			Street:      "People Square",
		},
	}
}

func assertDomainStoryGeoPointMediaArea(t *testing.T, story domain.Story, wantLat, wantLong, wantX float64) {
	t.Helper()
	if len(story.MediaAreas) != 1 {
		t.Fatalf("story media areas = %+v, want one geo area", story.MediaAreas)
	}
	area := story.MediaAreas[0]
	if area.Kind != domain.StoryMediaAreaGeoPoint || area.Geo == nil {
		t.Fatalf("story media area = %+v, want geo point", area)
	}
	if area.Geo.Lat != wantLat || area.Geo.Long != wantLong || area.Geo.AccessHash != 123456 {
		t.Fatalf("story media area geo = %+v, want lat %v long %v access_hash 123456", area.Geo, wantLat, wantLong)
	}
	if area.GeoAddress == nil || area.GeoAddress.CountryISO2 != "CN" || area.GeoAddress.State != "Shanghai" || area.GeoAddress.City != "Shanghai" || area.GeoAddress.Street != "People Square" {
		t.Fatalf("story media area address = %+v, want CN/Shanghai/Shanghai/People Square", area.GeoAddress)
	}
	if area.Coordinates.X != wantX || area.Coordinates.Y != 12 || area.Coordinates.W != 22 || area.Coordinates.H != 11 ||
		area.Coordinates.Rotation != 20 {
		t.Fatalf("story media area coordinates = %+v, want x %v y 12 w 22 h 11 rotation 20", area.Coordinates, wantX)
	}
}

func testDomainStoryVenueMediaArea(title string, lat, long, x float64) domain.StoryMediaArea {
	venue := &domain.MessageVenue{
		Geo:       domain.MessageGeoPoint{Lat: lat, Long: long, AccessHash: 123456},
		Title:     title,
		Address:   "Inline Street",
		Provider:  "gplaces",
		VenueID:   "venue-id",
		VenueType: "cafe",
	}
	return domain.StoryMediaArea{
		Kind: domain.StoryMediaAreaVenue,
		Coordinates: domain.StoryMediaAreaCoordinates{
			X:        x,
			Y:        13,
			W:        23,
			H:        12,
			Rotation: 22,
		},
		Geo:   &venue.Geo,
		Venue: venue,
	}
}

func assertDomainStoryVenueMediaArea(t *testing.T, story domain.Story, wantTitle string, wantLat, wantLong, wantX float64) {
	t.Helper()
	if len(story.MediaAreas) != 1 {
		t.Fatalf("story media areas = %+v, want one venue area", story.MediaAreas)
	}
	area := story.MediaAreas[0]
	if area.Kind != domain.StoryMediaAreaVenue || area.Venue == nil || area.Geo == nil {
		t.Fatalf("story media area = %+v, want venue", area)
	}
	if area.Venue.Title != wantTitle || area.Venue.Address != "Inline Street" || area.Venue.Provider != "gplaces" || area.Venue.VenueID != "venue-id" || area.Venue.VenueType != "cafe" {
		t.Fatalf("story venue = %+v, want %q/Inline Street/gplaces/venue-id/cafe", area.Venue, wantTitle)
	}
	if area.Venue.Geo.Lat != wantLat || area.Venue.Geo.Long != wantLong || area.Venue.Geo.AccessHash != 123456 ||
		area.Geo.Lat != wantLat || area.Geo.Long != wantLong || area.Geo.AccessHash != 123456 {
		t.Fatalf("story venue geo = venue %+v area %+v, want lat %v long %v access_hash 123456", area.Venue.Geo, area.Geo, wantLat, wantLong)
	}
	if area.Coordinates.X != wantX || area.Coordinates.Y != 13 || area.Coordinates.W != 23 || area.Coordinates.H != 12 ||
		area.Coordinates.Rotation != 22 {
		t.Fatalf("story media area coordinates = %+v, want x %v y 13 w 23 h 12 rotation 22", area.Coordinates, wantX)
	}
}

func testDomainStoryWeatherMediaArea(emoji string, temperatureC float64, color int, x float64) domain.StoryMediaArea {
	return domain.StoryMediaArea{
		Kind: domain.StoryMediaAreaWeather,
		Coordinates: domain.StoryMediaAreaCoordinates{
			X:        x,
			Y:        14,
			W:        24,
			H:        12,
			Rotation: 25,
		},
		WeatherEmoji: emoji,
		TemperatureC: temperatureC,
		Color:        color,
	}
}

func assertDomainStoryWeatherMediaArea(t *testing.T, story domain.Story, wantEmoji string, wantTemperatureC float64, wantColor int, wantX float64) {
	t.Helper()
	if len(story.MediaAreas) != 1 {
		t.Fatalf("story media areas = %+v, want one weather area", story.MediaAreas)
	}
	area := story.MediaAreas[0]
	if area.Kind != domain.StoryMediaAreaWeather {
		t.Fatalf("story media area = %+v, want weather", area)
	}
	if area.WeatherEmoji != wantEmoji || area.TemperatureC != wantTemperatureC || area.Color != wantColor {
		t.Fatalf("story weather area = emoji %q temp %v color %d, want %q/%v/%d", area.WeatherEmoji, area.TemperatureC, area.Color, wantEmoji, wantTemperatureC, wantColor)
	}
	if area.Coordinates.X != wantX || area.Coordinates.Y != 14 || area.Coordinates.W != 24 || area.Coordinates.H != 12 ||
		area.Coordinates.Rotation != 25 {
		t.Fatalf("story media area coordinates = %+v, want x %v y 14 w 24 h 12 rotation 25", area.Coordinates, wantX)
	}
}

func testDomainStoryChannelPostMediaArea(channelID int64, msgID int, x float64) domain.StoryMediaArea {
	return domain.StoryMediaArea{
		Kind: domain.StoryMediaAreaChannelPost,
		Coordinates: domain.StoryMediaAreaCoordinates{
			X:        x,
			Y:        15,
			W:        25,
			H:        12,
			Rotation: 27,
		},
		ChannelID: channelID,
		MsgID:     msgID,
	}
}

func assertDomainStoryChannelPostMediaArea(t *testing.T, story domain.Story, wantChannelID int64, wantMsgID int, wantX float64) {
	t.Helper()
	if len(story.MediaAreas) != 1 {
		t.Fatalf("story media areas = %+v, want one channel post area", story.MediaAreas)
	}
	area := story.MediaAreas[0]
	if area.Kind != domain.StoryMediaAreaChannelPost || area.ChannelID != wantChannelID || area.MsgID != wantMsgID {
		t.Fatalf("story channel post area = %+v, want channel %d msg %d", area, wantChannelID, wantMsgID)
	}
	if area.Coordinates.X != wantX || area.Coordinates.Y != 15 || area.Coordinates.W != 25 || area.Coordinates.H != 12 ||
		area.Coordinates.Rotation != 27 {
		t.Fatalf("story media area coordinates = %+v, want x %v y 15 w 25 h 12 rotation 27", area.Coordinates, wantX)
	}
}

func testDomainStoryStarGiftMediaArea(slug string, x float64) domain.StoryMediaArea {
	return domain.StoryMediaArea{
		Kind: domain.StoryMediaAreaStarGift,
		Coordinates: domain.StoryMediaAreaCoordinates{
			X:        x,
			Y:        16,
			W:        26,
			H:        12,
			Rotation: 29,
		},
		StarGiftSlug: slug,
	}
}

func assertDomainStoryStarGiftMediaArea(t *testing.T, story domain.Story, wantSlug string, wantX float64) {
	t.Helper()
	if len(story.MediaAreas) != 1 {
		t.Fatalf("story media areas = %+v, want one star gift area", story.MediaAreas)
	}
	area := story.MediaAreas[0]
	if area.Kind != domain.StoryMediaAreaStarGift || area.StarGiftSlug != wantSlug {
		t.Fatalf("story star gift area = %+v, want slug %q", area, wantSlug)
	}
	if area.Coordinates.X != wantX || area.Coordinates.Y != 16 || area.Coordinates.W != 26 || area.Coordinates.H != 12 ||
		area.Coordinates.Rotation != 29 {
		t.Fatalf("story media area coordinates = %+v, want x %v y 16 w 26 h 12 rotation 29", area.Coordinates, wantX)
	}
}

func assertDomainStoryForward(t *testing.T, story domain.Story, wantSource domain.Peer, wantStoryID int, wantModified bool) {
	t.Helper()
	if story.Forward == nil {
		t.Fatalf("story forward is nil, want source %+v story %d", wantSource, wantStoryID)
	}
	if story.Forward.From != wantSource || story.Forward.StoryID != wantStoryID || story.Forward.Modified != wantModified {
		t.Fatalf("story forward = %+v, want source %+v story %d modified %v", story.Forward, wantSource, wantStoryID, wantModified)
	}
}

func assertDomainStoryForwardName(t *testing.T, story domain.Story, wantName string, wantStoryID int, wantModified bool) {
	t.Helper()
	if story.Forward == nil {
		t.Fatalf("story forward is nil, want from_name %q story %d", wantName, wantStoryID)
	}
	if story.Forward.From != (domain.Peer{}) || story.Forward.FromName != wantName ||
		story.Forward.StoryID != wantStoryID || story.Forward.Modified != wantModified {
		t.Fatalf("story forward = %+v, want from_name %q story %d modified %v", story.Forward, wantName, wantStoryID, wantModified)
	}
}

func TestStoryStoreViewsIdempotentAndReactionReplace(t *testing.T) {
	ctx := context.Background()
	store := NewStoryStore()
	owner := domain.Peer{Type: domain.PeerTypeUser, ID: 1001}
	if _, err := store.UpsertStory(ctx, domain.UpsertStoryRequest{Story: domain.Story{
		Owner:      owner,
		ID:         1,
		Date:       100,
		ExpireDate: 1000,
		Public:     true,
	}}); err != nil {
		t.Fatalf("upsert story: %v", err)
	}

	if created, err := store.IncrementViews(ctx, 2001, owner, []int{1, 1}, 200); err != nil || created != 1 {
		t.Fatalf("increment views first = %d, %v; want 1 nil", created, err)
	}
	if created, err := store.IncrementViews(ctx, 2001, owner, []int{1}, 201); err != nil || created != 0 {
		t.Fatalf("increment views duplicate = %d, %v; want 0 nil", created, err)
	}
	if _, err := store.IncrementViews(ctx, 2001, owner, nil, 201); !errors.Is(err, domain.ErrStoryIDInvalid) {
		t.Fatalf("empty increment views err = %v, want ErrStoryIDInvalid", err)
	}
	like := &domain.MessageReaction{Type: domain.MessageReactionEmoji, Emoticon: "👍"}
	fire := &domain.MessageReaction{Type: domain.MessageReactionEmoji, Emoticon: "🔥"}
	res, err := store.SetReaction(ctx, 2001, owner, 1, like, 202)
	if err != nil {
		t.Fatalf("set reaction: %v", err)
	}
	if !res.Changed || res.Story.Views.ViewsCount != 1 || res.Story.Views.ReactionsCount != 1 {
		t.Fatalf("reaction result = %+v, want one view and one reaction", res)
	}
	res, err = store.SetReaction(ctx, 2001, owner, 1, fire, 203)
	if err != nil {
		t.Fatalf("replace reaction: %v", err)
	}
	if !res.Changed || res.Story.Views.ReactionsCount != 1 || len(res.Story.Views.Reactions) != 1 || res.Story.Views.Reactions[0].Reaction.Emoticon != "🔥" {
		t.Fatalf("replace result = %+v, want only fire reaction", res)
	}
	res, err = store.SetReaction(ctx, 2001, owner, 1, fire, 204)
	if err != nil {
		t.Fatalf("retry same reaction: %v", err)
	}
	if res.Changed || res.Date != 203 || res.Story.Views.ReactionsCount != 1 || len(res.Story.Views.Reactions) != 1 || res.Story.Views.Reactions[0].Reaction.Emoticon != "🔥" {
		t.Fatalf("retry same reaction result = %+v, want unchanged fire reaction at original date", res)
	}
	reactions, err := store.ListStoryReactions(ctx, domain.StoryReactionListRequest{
		ViewerUserID: owner.ID,
		Owner:        owner,
		StoryID:      1,
		Limit:        10,
	})
	if err != nil {
		t.Fatalf("list reactions after retry: %v", err)
	}
	if reactions.Count != 1 || len(reactions.Reactions) != 1 || reactions.Reactions[0].ViewerID != 2001 || reactions.Reactions[0].Date != 203 {
		t.Fatalf("reactions after retry = %+v, want original reaction date", reactions)
	}
	custom := &domain.MessageReaction{Type: domain.MessageReactionCustomEmoji, DocumentID: 12345}
	res, err = store.SetReaction(ctx, 2001, owner, 1, custom, 204)
	if err != nil {
		t.Fatalf("replace with custom reaction: %v", err)
	}
	if !res.Changed || res.Story.Views.ReactionsCount != 1 || len(res.Story.Views.Reactions) != 1 || res.Story.Views.Reactions[0].Reaction.DocumentID != 12345 {
		t.Fatalf("custom replace result = %+v, want one custom reaction", res)
	}
	res, err = store.SetReaction(ctx, 2001, owner, 1, custom, 205)
	if err != nil {
		t.Fatalf("retry same custom reaction: %v", err)
	}
	if res.Changed || res.Date != 204 || res.Story.Views.ReactionsCount != 1 || len(res.Story.Views.Reactions) != 1 || res.Story.Views.Reactions[0].Reaction.DocumentID != 12345 {
		t.Fatalf("retry same custom reaction result = %+v, want unchanged custom reaction at original date", res)
	}
	reactions, err = store.ListStoryReactions(ctx, domain.StoryReactionListRequest{
		ViewerUserID: owner.ID,
		Owner:        owner,
		StoryID:      1,
		Reaction:     custom,
		Limit:        10,
	})
	if err != nil {
		t.Fatalf("list custom reactions: %v", err)
	}
	if reactions.Count != 1 || len(reactions.Reactions) != 1 || reactions.Reactions[0].ViewerID != 2001 || reactions.Reactions[0].Date != 204 || reactions.Reactions[0].Reaction == nil || reactions.Reactions[0].Reaction.DocumentID != 12345 {
		t.Fatalf("custom reactions = %+v, want original custom reaction date", reactions)
	}
	if _, err := store.ListStoryViews(ctx, domain.StoryViewListRequest{
		ViewerUserID: owner.ID,
		Owner:        owner,
		StoryID:      1,
		Offset:       "bad",
		Limit:        10,
	}); !errors.Is(err, domain.ErrStoryOffsetInvalid) {
		t.Fatalf("bad story views offset err = %v, want ErrStoryOffsetInvalid", err)
	}
	if _, err := store.ListStoryReactions(ctx, domain.StoryReactionListRequest{
		ViewerUserID: owner.ID,
		Owner:        owner,
		StoryID:      1,
		Offset:       "1:203:2001",
		Limit:        10,
	}); !errors.Is(err, domain.ErrStoryOffsetInvalid) {
		t.Fatalf("bad story reactions offset err = %v, want ErrStoryOffsetInvalid", err)
	}
	res, err = store.SetReaction(ctx, 2001, owner, 1, nil, 206)
	if err != nil {
		t.Fatalf("clear reaction: %v", err)
	}
	if !res.Changed || res.Story.Views.ReactionsCount != 0 || len(res.Story.Views.Reactions) != 0 {
		t.Fatalf("clear result = %+v, want no reactions", res)
	}
}

func TestStoryStoreExpiredUnpinnedStoriesDoNotAcceptNewInteractions(t *testing.T) {
	ctx := context.Background()
	store := NewStoryStore()
	owner := domain.Peer{Type: domain.PeerTypeUser, ID: 1001}
	for _, story := range []domain.Story{
		{Owner: owner, ID: 1, Date: 100, ExpireDate: 150, Public: true},
		{Owner: owner, ID: 2, Date: 101, ExpireDate: 150, Public: true, Pinned: true},
	} {
		if _, err := store.UpsertStory(ctx, domain.UpsertStoryRequest{Story: story}); err != nil {
			t.Fatalf("upsert story %d: %v", story.ID, err)
		}
	}

	if created, err := store.IncrementViews(ctx, 2001, owner, []int{1}, 200); err != nil || created != 0 {
		t.Fatalf("expired unpinned increment = %d, %v; want 0 nil", created, err)
	}
	if _, err := store.SetReaction(ctx, 2001, owner, 1, &domain.MessageReaction{Type: domain.MessageReactionEmoji, Emoticon: "like"}, 200); !errors.Is(err, domain.ErrStoryNotFound) {
		t.Fatalf("expired unpinned reaction err = %v, want ErrStoryNotFound", err)
	}
	if created, err := store.IncrementViews(ctx, 2001, owner, []int{2}, 200); err != nil || created != 1 {
		t.Fatalf("expired pinned increment = %d, %v; want 1 nil", created, err)
	}
	res, err := store.SetReaction(ctx, 2002, owner, 2, &domain.MessageReaction{Type: domain.MessageReactionEmoji, Emoticon: "fire"}, 201)
	if err != nil {
		t.Fatalf("expired pinned reaction: %v", err)
	}
	if !res.Changed || res.Story.Views.ViewsCount != 2 || res.Story.Views.ReactionsCount != 1 {
		t.Fatalf("expired pinned reaction result = %+v, want accepted profile interaction", res)
	}
}

func TestStoryStoreListsViewsAndReactions(t *testing.T) {
	ctx := context.Background()
	store := NewStoryStore()
	owner := domain.Peer{Type: domain.PeerTypeUser, ID: 1001}
	if _, err := store.UpsertStory(ctx, domain.UpsertStoryRequest{Story: domain.Story{
		Owner:      owner,
		ID:         1,
		Date:       100,
		ExpireDate: 1000,
		Public:     true,
	}}); err != nil {
		t.Fatalf("upsert story: %v", err)
	}

	if _, err := store.IncrementViews(ctx, 2001, owner, []int{1}, 201); err != nil {
		t.Fatalf("increment viewer 2001: %v", err)
	}
	if _, err := store.IncrementViews(ctx, 2004, owner, []int{1}, 204); err != nil {
		t.Fatalf("increment viewer 2004: %v", err)
	}
	like := &domain.MessageReaction{Type: domain.MessageReactionEmoji, Emoticon: "like"}
	fire := &domain.MessageReaction{Type: domain.MessageReactionEmoji, Emoticon: "fire"}
	if _, err := store.SetReaction(ctx, 2002, owner, 1, like, 202); err != nil {
		t.Fatalf("set like reaction: %v", err)
	}
	if _, err := store.SetReaction(ctx, 2003, owner, 1, fire, 203); err != nil {
		t.Fatalf("set fire reaction: %v", err)
	}

	viewerIDs, err := store.ListStoryViewerIDs(ctx, owner, 1, 3)
	if err != nil {
		t.Fatalf("list story viewer ids: %v", err)
	}
	if len(viewerIDs) != 3 || viewerIDs[0] != 2001 || viewerIDs[1] != 2002 || viewerIDs[2] != 2003 {
		t.Fatalf("viewer ids = %v, want first three ascending ids", viewerIDs)
	}

	first, err := store.ListStoryViews(ctx, domain.StoryViewListRequest{
		ViewerUserID:   owner.ID,
		Owner:          owner,
		StoryID:        1,
		Limit:          2,
		ReactionsFirst: true,
	})
	if err != nil {
		t.Fatalf("list views first page: %v", err)
	}
	if first.Count != 4 || first.ViewsCount != 4 || first.ReactionsCount != 2 || len(first.Views) != 2 || first.NextOffset == "" {
		t.Fatalf("first page = %+v, want 4 total, 2 reactions, 2 rows and next offset", first)
	}
	if first.Views[0].ViewerID != 2003 || first.Views[1].ViewerID != 2002 {
		t.Fatalf("first page viewers = %+v, want reactions newest first", first.Views)
	}
	second, err := store.ListStoryViews(ctx, domain.StoryViewListRequest{
		ViewerUserID:   owner.ID,
		Owner:          owner,
		StoryID:        1,
		Limit:          2,
		ReactionsFirst: true,
		Offset:         first.NextOffset,
	})
	if err != nil {
		t.Fatalf("list views second page: %v", err)
	}
	if len(second.Views) != 2 || second.NextOffset != "" || second.Views[0].ViewerID != 2004 || second.Views[1].ViewerID != 2001 {
		t.Fatalf("second page = %+v, want remaining non-reaction viewers", second)
	}

	reactions, err := store.ListStoryReactions(ctx, domain.StoryReactionListRequest{
		ViewerUserID: owner.ID,
		Owner:        owner,
		StoryID:      1,
		Limit:        10,
	})
	if err != nil {
		t.Fatalf("list reactions: %v", err)
	}
	if reactions.Count != 2 || len(reactions.Reactions) != 2 || reactions.Reactions[0].ViewerID != 2003 {
		t.Fatalf("reactions = %+v, want two newest reactions", reactions)
	}
	filtered, err := store.ListStoryReactions(ctx, domain.StoryReactionListRequest{
		ViewerUserID: owner.ID,
		Owner:        owner,
		StoryID:      1,
		Reaction:     fire,
		Limit:        10,
	})
	if err != nil {
		t.Fatalf("list filtered reactions: %v", err)
	}
	if filtered.Count != 1 || len(filtered.Reactions) != 1 || filtered.Reactions[0].ViewerID != 2003 {
		t.Fatalf("filtered reactions = %+v, want viewer 2003 only", filtered)
	}
	if _, err := store.SetReaction(ctx, 2003, owner, 1, nil, 205); err != nil {
		t.Fatalf("clear reaction: %v", err)
	}
	reactions, err = store.ListStoryReactions(ctx, domain.StoryReactionListRequest{
		ViewerUserID: owner.ID,
		Owner:        owner,
		StoryID:      1,
		Limit:        10,
	})
	if err != nil {
		t.Fatalf("list reactions after clear: %v", err)
	}
	if reactions.Count != 1 || len(reactions.Reactions) != 1 || reactions.Reactions[0].ViewerID != 2002 {
		t.Fatalf("reactions after clear = %+v, want only viewer 2002", reactions)
	}
	if _, err := store.DeleteStories(ctx, owner, []int{1}, 206); err != nil {
		t.Fatalf("delete story: %v", err)
	}
	deletedViewerIDs, err := store.ListStoryViewerIDs(ctx, owner, 1, 10)
	if err != nil {
		t.Fatalf("list deleted story viewer ids: %v", err)
	}
	if len(deletedViewerIDs) != 4 || deletedViewerIDs[0] != 2001 || deletedViewerIDs[1] != 2002 || deletedViewerIDs[2] != 2003 || deletedViewerIDs[3] != 2004 {
		t.Fatalf("deleted story viewer ids = %v, want all durable viewers ascending", deletedViewerIDs)
	}
}

func TestStoryStoreViewerIDsIncludeCachedStoryExposure(t *testing.T) {
	ctx := context.Background()
	store := NewStoryStore()
	owner := domain.Peer{Type: domain.PeerTypeUser, ID: 1001}
	if _, err := store.UpsertStory(ctx, domain.UpsertStoryRequest{Story: domain.Story{
		Owner:      owner,
		ID:         1,
		Date:       100,
		ExpireDate: 1000,
		Public:     true,
	}}); err != nil {
		t.Fatalf("upsert story: %v", err)
	}
	if list, err := store.ListActiveStories(ctx, 2005, false, 200, 10); err != nil || len(list.Stories) != 1 {
		t.Fatalf("list active exposure = %+v err=%v, want one story", list, err)
	}
	if peerStories, err := store.GetPeerStories(ctx, 2006, owner, 200); err != nil || len(peerStories.Stories) != 1 {
		t.Fatalf("peer stories exposure = %+v err=%v, want one story", peerStories, err)
	}
	if exact, err := store.GetStoriesByID(ctx, 2007, owner, []int{1}, 200); err != nil || len(exact.Stories) != 1 {
		t.Fatalf("exact exposure = %+v err=%v, want one story", exact, err)
	}

	viewerIDs, err := store.ListStoryViewerIDs(ctx, owner, 1, 10)
	if err != nil {
		t.Fatalf("list story viewer ids: %v", err)
	}
	if len(viewerIDs) != 3 || viewerIDs[0] != 2005 || viewerIDs[1] != 2006 || viewerIDs[2] != 2007 {
		t.Fatalf("viewer ids = %v, want exposure viewers ascending", viewerIDs)
	}
	views, err := store.ListStoryViews(ctx, domain.StoryViewListRequest{
		ViewerUserID: owner.ID,
		Owner:        owner,
		StoryID:      1,
		Limit:        10,
	})
	if err != nil {
		t.Fatalf("list story views: %v", err)
	}
	if views.Count != 0 || views.ViewsCount != 0 || len(views.Views) != 0 {
		t.Fatalf("views = %+v, exposure must not count as a real view", views)
	}
}

func TestStoryStoreListStoryViewsFiltersByContactsAndQuery(t *testing.T) {
	ctx := context.Background()
	store := NewStoryStore()
	owner := domain.Peer{Type: domain.PeerTypeUser, ID: 1001}
	if _, err := store.UpsertStory(ctx, domain.UpsertStoryRequest{Story: domain.Story{
		Owner:      owner,
		ID:         1,
		Date:       100,
		ExpireDate: 1000,
		Public:     true,
	}}); err != nil {
		t.Fatalf("upsert story: %v", err)
	}
	store.SetStoryViewerProfiles(
		domain.User{ID: 2001, FirstName: "Alice", LastName: "Viewer", Username: "alicev", Phone: "155501"},
		domain.User{ID: 2002, FirstName: "Bob", LastName: "Stranger", Username: "bobstory", Phone: "155502"},
		domain.User{ID: 2003, FirstName: "Carol", LastName: "Contact", Username: "carolstory", Phone: "155503"},
	)
	store.SetStoryViewerContacts(owner.ID,
		domain.Contact{User: domain.User{ID: 2001, FirstName: "Alice", Username: "alicev"}, FirstName: "Close", LastName: "Friend", Phone: "7001"},
		domain.Contact{User: domain.User{ID: 2003, FirstName: "Carol", Username: "carolstory"}, FirstName: "Work", LastName: "Carol", Phone: "7003"},
	)
	for _, item := range []struct {
		viewerID int64
		date     int
	}{
		{viewerID: 2001, date: 201},
		{viewerID: 2002, date: 202},
		{viewerID: 2003, date: 203},
	} {
		if _, err := store.IncrementViews(ctx, item.viewerID, owner, []int{1}, item.date); err != nil {
			t.Fatalf("increment viewer %d: %v", item.viewerID, err)
		}
	}

	contactsOnly, err := store.ListStoryViews(ctx, domain.StoryViewListRequest{
		ViewerUserID: owner.ID,
		Owner:        owner,
		StoryID:      1,
		Limit:        10,
		JustContacts: true,
	})
	if err != nil {
		t.Fatalf("list contacts-only: %v", err)
	}
	if contactsOnly.Count != 2 || contactsOnly.ViewsCount != 3 || len(contactsOnly.Views) != 2 || contactsOnly.Views[0].ViewerID != 2003 || contactsOnly.Views[1].ViewerID != 2001 {
		t.Fatalf("contacts-only = %+v, want two contacts and total views 3", contactsOnly)
	}

	remark, err := store.ListStoryViews(ctx, domain.StoryViewListRequest{
		ViewerUserID: owner.ID,
		Owner:        owner,
		StoryID:      1,
		Limit:        10,
		Query:        "close",
	})
	if err != nil {
		t.Fatalf("list query remark: %v", err)
	}
	if remark.Count != 1 || len(remark.Views) != 1 || remark.Views[0].ViewerID != 2001 {
		t.Fatalf("remark query = %+v, want viewer 2001", remark)
	}

	stranger, err := store.ListStoryViews(ctx, domain.StoryViewListRequest{
		ViewerUserID: owner.ID,
		Owner:        owner,
		StoryID:      1,
		Limit:        10,
		Query:        "@bobstory",
	})
	if err != nil {
		t.Fatalf("list query username: %v", err)
	}
	if stranger.Count != 1 || len(stranger.Views) != 1 || stranger.Views[0].ViewerID != 2002 {
		t.Fatalf("username query = %+v, want viewer 2002", stranger)
	}

	hiddenAccountPhone, err := store.ListStoryViews(ctx, domain.StoryViewListRequest{
		ViewerUserID: owner.ID,
		Owner:        owner,
		StoryID:      1,
		Limit:        10,
		Query:        "155502",
	})
	if err != nil {
		t.Fatalf("list query hidden account phone: %v", err)
	}
	if hiddenAccountPhone.Count != 0 || len(hiddenAccountPhone.Views) != 0 {
		t.Fatalf("hidden account phone query = %+v, want no match", hiddenAccountPhone)
	}
	knownContactPhone, err := store.ListStoryViews(ctx, domain.StoryViewListRequest{
		ViewerUserID: owner.ID,
		Owner:        owner,
		StoryID:      1,
		Limit:        10,
		Query:        "7001",
	})
	if err != nil {
		t.Fatalf("list query known contact phone: %v", err)
	}
	if knownContactPhone.Count != 1 || len(knownContactPhone.Views) != 1 || knownContactPhone.Views[0].ViewerID != 2001 {
		t.Fatalf("known contact phone query = %+v, want viewer 2001", knownContactPhone)
	}

	intersection, err := store.ListStoryViews(ctx, domain.StoryViewListRequest{
		ViewerUserID: owner.ID,
		Owner:        owner,
		StoryID:      1,
		Limit:        10,
		Query:        "bob",
		JustContacts: true,
	})
	if err != nil {
		t.Fatalf("list query contacts intersection: %v", err)
	}
	if intersection.Count != 0 || len(intersection.Views) != 0 {
		t.Fatalf("intersection = %+v, want no contact match for bob", intersection)
	}
}

func TestStoryStorePrivacyVisibility(t *testing.T) {
	ctx := context.Background()
	store := NewStoryStore()
	owner := domain.Peer{Type: domain.PeerTypeUser, ID: 1001}
	selectedViewer := int64(2001)
	stranger := int64(2002)
	contactViewer := int64(2003)
	excludedContact := int64(2004)
	closeFriendViewer := int64(2005)
	store.SetStoryViewerContacts(owner.ID,
		domain.Contact{User: domain.User{ID: contactViewer, FirstName: "Contact"}},
		domain.Contact{User: domain.User{ID: excludedContact, FirstName: "Excluded"}},
		domain.Contact{User: domain.User{ID: closeFriendViewer, FirstName: "Close"}, CloseFriend: true},
	)
	stories := []domain.Story{
		{
			Owner:           owner,
			ID:              1,
			Date:            100,
			ExpireDate:      1000,
			Public:          true,
			DisallowUserIDs: []int64{stranger},
			PrivacyRules: []domain.PrivacyRule{
				{Kind: domain.PrivacyRuleAllowAll},
				{Kind: domain.PrivacyRuleDisallowUsers, UserIDs: []int64{stranger}},
			},
		},
		{
			Owner:            owner,
			ID:               2,
			Date:             101,
			ExpireDate:       1000,
			SelectedContacts: true,
			AllowUserIDs:     []int64{selectedViewer},
			PrivacyRules: []domain.PrivacyRule{
				{Kind: domain.PrivacyRuleAllowUsers, UserIDs: []int64{selectedViewer}},
			},
		},
		{
			Owner:           owner,
			ID:              3,
			Date:            102,
			ExpireDate:      1000,
			Contacts:        true,
			DisallowUserIDs: []int64{excludedContact},
			PrivacyRules: []domain.PrivacyRule{
				{Kind: domain.PrivacyRuleAllowContacts},
				{Kind: domain.PrivacyRuleDisallowUsers, UserIDs: []int64{excludedContact}},
			},
		},
		{
			Owner:        owner,
			ID:           4,
			Date:         103,
			ExpireDate:   1000,
			CloseFriends: true,
			PrivacyRules: []domain.PrivacyRule{
				{Kind: domain.PrivacyRuleAllowCloseFriends},
			},
		},
	}
	for _, story := range stories {
		if _, err := store.UpsertStory(ctx, domain.UpsertStoryRequest{Story: story}); err != nil {
			t.Fatalf("upsert story %d: %v", story.ID, err)
		}
	}

	selected, err := store.GetStoriesByID(ctx, selectedViewer, owner, []int{1, 2, 3, 4}, 200)
	if err != nil {
		t.Fatalf("selected viewer get stories: %v", err)
	}
	if gotIDs := storyIDs(selected.Stories); !sameStoryIDs(gotIDs, []int{1, 2}) {
		t.Fatalf("selected viewer story ids = %v, want [1 2]", gotIDs)
	}
	hidden, err := store.GetStoriesByID(ctx, stranger, owner, []int{1, 2, 3, 4}, 200)
	if err != nil {
		t.Fatalf("stranger get stories: %v", err)
	}
	if len(hidden.Stories) != 0 {
		t.Fatalf("stranger stories = %+v, want none", hidden.Stories)
	}
	contacts, err := store.GetStoriesByID(ctx, contactViewer, owner, []int{1, 2, 3, 4}, 200)
	if err != nil {
		t.Fatalf("contact get stories: %v", err)
	}
	if gotIDs := storyIDs(contacts.Stories); !sameStoryIDs(gotIDs, []int{1, 3}) {
		t.Fatalf("contact story ids = %v, want [1 3]", gotIDs)
	}
	excluded, err := store.GetStoriesByID(ctx, excludedContact, owner, []int{3}, 200)
	if err != nil {
		t.Fatalf("excluded contact get stories: %v", err)
	}
	if len(excluded.Stories) != 0 {
		t.Fatalf("excluded contact stories = %+v, want none", excluded.Stories)
	}
	closeFriend, err := store.GetStoriesByID(ctx, closeFriendViewer, owner, []int{1, 2, 3, 4}, 200)
	if err != nil {
		t.Fatalf("close friend get stories: %v", err)
	}
	if gotIDs := storyIDs(closeFriend.Stories); !sameStoryIDs(gotIDs, []int{1, 3, 4}) {
		t.Fatalf("close friend story ids = %v, want [1 3 4]", gotIDs)
	}
	if created, err := store.IncrementViews(ctx, stranger, owner, []int{2}, 210); err != nil || created != 0 {
		t.Fatalf("hidden increment views = %d, %v; want 0 nil", created, err)
	}
	if _, err := store.SetReaction(ctx, stranger, owner, 2, &domain.MessageReaction{Type: domain.MessageReactionEmoji, Emoticon: "like"}, 211); err != domain.ErrStoryNotFound {
		t.Fatalf("hidden set reaction err = %v, want ErrStoryNotFound", err)
	}
}

func TestStoryStoreCreateEditDeleteAndPinned(t *testing.T) {
	ctx := context.Background()
	store := NewStoryStore()
	owner := domain.Peer{Type: domain.PeerTypeUser, ID: 1001}
	media := &domain.MessageMedia{Kind: domain.MessageMediaKindPhoto, Photo: &domain.Photo{ID: 77, AccessHash: 7, DCID: 2}}

	created, err := store.CreateStory(ctx, domain.StoryCreateRequest{
		Owner:      owner,
		RandomID:   9001,
		Date:       100,
		Period:     86400,
		Public:     true,
		Pinned:     true,
		Caption:    "first",
		Media:      media,
		NoForwards: true,
	})
	if err != nil {
		t.Fatalf("create story: %v", err)
	}
	if created.Duplicate || created.Story.ID != 1 || created.Story.ExpireDate != 86500 || !created.Story.Pinned {
		t.Fatalf("created = %+v, want new pinned story id 1", created)
	}
	dup, err := store.CreateStory(ctx, domain.StoryCreateRequest{
		Owner:    owner,
		RandomID: 9001,
		Date:     101,
		Period:   86400,
		Public:   true,
		Caption:  "retry must not win",
		Media:    media,
	})
	if err != nil {
		t.Fatalf("create duplicate: %v", err)
	}
	if !dup.Duplicate || dup.Story.ID != created.Story.ID || dup.Story.Caption != "first" {
		t.Fatalf("duplicate = %+v, want original story", dup)
	}
	read, err := store.GetPeerStories(ctx, owner.ID, owner, 102)
	if err != nil {
		t.Fatalf("get self stories: %v", err)
	}
	if read.MaxReadID != 1 {
		t.Fatalf("self max read = %d, want created story id", read.MaxReadID)
	}

	edited, err := store.EditStory(ctx, domain.StoryEditRequest{
		Owner:         owner,
		ID:            created.Story.ID,
		Caption:       "edited",
		UpdateCaption: true,
	})
	if err != nil {
		t.Fatalf("edit story: %v", err)
	}
	if edited.Previous.Caption != "first" || edited.Previous.Edited {
		t.Fatalf("previous = %+v, want pre-edit story", edited.Previous)
	}
	if !edited.Story.Edited || edited.Story.Caption != "edited" || !edited.Story.Pinned {
		t.Fatalf("edited = %+v, want edited caption preserving pin", edited)
	}
	pinned, err := store.ListPinnedStories(ctx, owner.ID, owner, 0, 10, 103)
	if err != nil {
		t.Fatalf("list pinned: %v", err)
	}
	if len(pinned.Stories) != 1 || pinned.Stories[0].ID != created.Story.ID {
		t.Fatalf("pinned = %+v, want created story", pinned)
	}

	deleted, err := store.DeleteStories(ctx, owner, []int{created.Story.ID}, 104)
	if err != nil {
		t.Fatalf("delete story: %v", err)
	}
	if len(deleted.Stories) != 1 || !deleted.Stories[0].Deleted || deleted.Stories[0].Pinned {
		t.Fatalf("deleted = %+v, want deleted unpinned snapshot", deleted)
	}
	if len(deleted.Previous) != 1 || deleted.Previous[0].Deleted || !deleted.Previous[0].Pinned {
		t.Fatalf("deleted previous = %+v, want pre-delete pinned snapshot", deleted.Previous)
	}
	active, err := store.GetPeerStories(ctx, owner.ID, owner, 105)
	if err != nil {
		t.Fatalf("get active after delete: %v", err)
	}
	if len(active.Stories) != 0 {
		t.Fatalf("active after delete = %+v, want empty", active)
	}
}

func TestStoryMutationIDsAreDeduped(t *testing.T) {
	ctx := context.Background()
	store := NewStoryStore()
	owner := domain.Peer{Type: domain.PeerTypeUser, ID: 1001}
	media := &domain.MessageMedia{Kind: domain.MessageMediaKindPhoto, Photo: &domain.Photo{ID: 1}}
	created, err := store.CreateStory(ctx, domain.StoryCreateRequest{
		Owner:    owner,
		RandomID: 9101,
		Date:     100,
		Period:   86400,
		Public:   true,
		Media:    media,
	})
	if err != nil {
		t.Fatalf("create story: %v", err)
	}

	toggled, err := store.TogglePinned(ctx, owner, []int{created.Story.ID, created.Story.ID}, true, 101)
	if err != nil {
		t.Fatalf("toggle pinned: %v", err)
	}
	if len(toggled.IDs) != 1 || toggled.IDs[0] != created.Story.ID || len(toggled.Stories) != 1 || !toggled.Stories[0].Pinned {
		t.Fatalf("toggled = %+v, want one pinned story", toggled)
	}
	if len(toggled.Previous) != 1 || toggled.Previous[0].Pinned {
		t.Fatalf("toggled previous = %+v, want pre-toggle unpinned snapshot", toggled.Previous)
	}
	retryToggled, err := store.TogglePinned(ctx, owner, []int{created.Story.ID}, true, 102)
	if err != nil {
		t.Fatalf("retry toggle pinned: %v", err)
	}
	if len(retryToggled.IDs) != 1 || retryToggled.IDs[0] != created.Story.ID || len(retryToggled.Stories) != 0 || len(retryToggled.Previous) != 0 {
		t.Fatalf("retry toggled = %+v, want id echo without mutation snapshot", retryToggled)
	}
	unpinned, err := store.TogglePinned(ctx, owner, []int{created.Story.ID}, false, 103)
	if err != nil {
		t.Fatalf("toggle unpinned: %v", err)
	}
	if len(unpinned.Stories) != 1 || unpinned.Stories[0].Pinned || len(unpinned.Previous) != 1 || !unpinned.Previous[0].Pinned {
		t.Fatalf("unpinned = %+v, want unpinned story with pre-toggle pinned snapshot", unpinned)
	}
	retryUnpinned, err := store.TogglePinned(ctx, owner, []int{created.Story.ID}, false, 104)
	if err != nil {
		t.Fatalf("retry toggle unpinned: %v", err)
	}
	if len(retryUnpinned.Stories) != 0 || len(retryUnpinned.Previous) != 0 {
		t.Fatalf("retry unpinned = %+v, want no mutation snapshots", retryUnpinned)
	}

	emptyToggle, err := store.TogglePinned(ctx, owner, nil, false, 101)
	if err != nil {
		t.Fatalf("empty toggle pinned: %v", err)
	}
	if len(emptyToggle.IDs) != 0 || len(emptyToggle.Stories) != 0 {
		t.Fatalf("empty toggle = %+v, want no-op", emptyToggle)
	}

	deleted, err := store.DeleteStories(ctx, owner, []int{created.Story.ID, created.Story.ID}, 102)
	if err != nil {
		t.Fatalf("delete story: %v", err)
	}
	if len(deleted.IDs) != 1 || deleted.IDs[0] != created.Story.ID || len(deleted.Stories) != 1 || !deleted.Stories[0].Deleted {
		t.Fatalf("deleted = %+v, want one deleted story", deleted)
	}
	retryDeleted, err := store.DeleteStories(ctx, owner, []int{created.Story.ID}, 103)
	if err != nil {
		t.Fatalf("retry delete story: %v", err)
	}
	if len(retryDeleted.IDs) != 1 || retryDeleted.IDs[0] != created.Story.ID || len(retryDeleted.Stories) != 0 {
		t.Fatalf("retry deleted = %+v, want id echo without mutation snapshot", retryDeleted)
	}
}

func TestStoryStorePinnedToTopOrder(t *testing.T) {
	ctx := context.Background()
	store := NewStoryStore()
	owner := domain.Peer{Type: domain.PeerTypeUser, ID: 1001}
	for _, id := range []int{1, 2, 3, 4} {
		if _, err := store.UpsertStory(ctx, domain.UpsertStoryRequest{Story: domain.Story{
			Owner:      owner,
			ID:         id,
			Date:       100 + id,
			ExpireDate: 1000,
			Pinned:     true,
			Public:     true,
		}}); err != nil {
			t.Fatalf("upsert story %d: %v", id, err)
		}
	}
	if err := store.TogglePinnedToTop(ctx, owner, []int{3, 1, 3}); err != nil {
		t.Fatalf("toggle pinned to top: %v", err)
	}
	pinned, err := store.ListPinnedStories(ctx, owner.ID, owner, 0, 2, 200)
	if err != nil {
		t.Fatalf("list pinned stories: %v", err)
	}
	if pinned.Count != 4 || !sameStoryIDs(storyIDs(pinned.Stories), []int{4, 3}) || !sameStoryIDs(pinned.PinnedToTop, []int{3, 1}) {
		t.Fatalf("pinned = count %d stories %v top %v, want count 4 stories 4,3 top 3,1", pinned.Count, storyIDs(pinned.Stories), pinned.PinnedToTop)
	}
	androidInitial, err := store.ListPinnedStories(ctx, owner.ID, owner, -1, 2, 200)
	if err != nil {
		t.Fatalf("list pinned stories with android sentinel: %v", err)
	}
	if androidInitial.Count != pinned.Count || !sameStoryIDs(storyIDs(androidInitial.Stories), []int{4, 3}) || !sameStoryIDs(androidInitial.PinnedToTop, []int{3, 1}) {
		t.Fatalf("android sentinel pinned = count %d stories %v top %v, want same first page 4,3 top 3,1", androidInitial.Count, storyIDs(androidInitial.Stories), androidInitial.PinnedToTop)
	}
	if err := store.TogglePinnedToTop(ctx, owner, []int{2, 1, 4}); err != nil {
		t.Fatalf("replace pinned to top: %v", err)
	}
	pinned, err = store.ListPinnedStories(ctx, owner.ID, owner, 0, 10, 201)
	if err != nil {
		t.Fatalf("list replaced pinned stories: %v", err)
	}
	if !sameStoryIDs(pinned.PinnedToTop, []int{2, 1, 4}) {
		t.Fatalf("replaced top = %v, want 2,1,4", pinned.PinnedToTop)
	}
	pageAfterOffset, err := store.ListPinnedStories(ctx, owner.ID, owner, 3, 10, 201)
	if err != nil {
		t.Fatalf("list pinned page after offset: %v", err)
	}
	if !sameStoryIDs(storyIDs(pageAfterOffset.Stories), []int{2, 1}) || !sameStoryIDs(pageAfterOffset.PinnedToTop, []int{2, 1, 4}) {
		t.Fatalf("offset page stories %v top %v, want stories 2,1 and full top 2,1,4", storyIDs(pageAfterOffset.Stories), pageAfterOffset.PinnedToTop)
	}
	if _, err := store.TogglePinned(ctx, owner, []int{2}, false, 202); err != nil {
		t.Fatalf("unpin story: %v", err)
	}
	pinned, err = store.ListPinnedStories(ctx, owner.ID, owner, 0, 10, 203)
	if err != nil {
		t.Fatalf("list after unpin: %v", err)
	}
	if !sameStoryIDs(pinned.PinnedToTop, []int{1, 4}) {
		t.Fatalf("top after unpin = %v, want 1,4", pinned.PinnedToTop)
	}
	if err := store.TogglePinnedToTop(ctx, owner, []int{2}); !errors.Is(err, domain.ErrStoryIDInvalid) {
		t.Fatalf("unpinned top err = %v, want ErrStoryIDInvalid", err)
	}
	if _, err := store.DeleteStories(ctx, owner, []int{1}, 204); err != nil {
		t.Fatalf("delete story: %v", err)
	}
	pinned, err = store.ListPinnedStories(ctx, owner.ID, owner, 0, 10, 205)
	if err != nil {
		t.Fatalf("list after delete: %v", err)
	}
	if !sameStoryIDs(pinned.PinnedToTop, []int{4}) {
		t.Fatalf("top after delete = %v, want 4", pinned.PinnedToTop)
	}
	if err := store.TogglePinnedToTop(ctx, owner, nil); err != nil {
		t.Fatalf("clear pinned to top: %v", err)
	}
	pinned, err = store.ListPinnedStories(ctx, owner.ID, owner, 0, 10, 206)
	if err != nil {
		t.Fatalf("list after clear: %v", err)
	}
	if len(pinned.PinnedToTop) != 0 {
		t.Fatalf("top after clear = %v, want empty", pinned.PinnedToTop)
	}
}

func storyIDs(stories []domain.Story) []int {
	out := make([]int, 0, len(stories))
	for _, story := range stories {
		out = append(out, story.ID)
	}
	return out
}

func sameStoryIDs(got, want []int) bool {
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
