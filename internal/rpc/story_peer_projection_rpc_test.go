package rpc

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap/zaptest"

	appchannels "telesrv/internal/app/channels"
	appstories "telesrv/internal/app/stories"
	appusers "telesrv/internal/app/users"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

type countingStoriesService struct {
	StoriesService
	maxIDCalls         int
	hiddenCalls        int
	projectionCalls    int
	pinnedAvailCalls   int
	pinnedStoriesCalls int
}

type blockingProjectionStoriesService struct {
	StoriesService
	mu      sync.Mutex
	calls   int
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (s *blockingProjectionStoriesService) GetPeerStoryProjections(ctx context.Context, viewerUserID int64, peers []domain.Peer, now int) ([]domain.PeerStoryProjection, error) {
	s.mu.Lock()
	s.calls++
	s.mu.Unlock()
	s.once.Do(func() { close(s.started) })
	select {
	case <-s.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return s.StoriesService.GetPeerStoryProjections(ctx, viewerUserID, peers, now)
}

func (s *blockingProjectionStoriesService) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func (s *countingStoriesService) GetPeerMaxIDs(ctx context.Context, viewerUserID int64, peers []domain.Peer, now int) ([]domain.RecentStory, error) {
	s.maxIDCalls++
	return s.StoriesService.GetPeerMaxIDs(ctx, viewerUserID, peers, now)
}

func (s *countingStoriesService) GetPeerHiddenStates(ctx context.Context, viewerUserID int64, peers []domain.Peer) (map[domain.Peer]bool, error) {
	s.hiddenCalls++
	return s.StoriesService.GetPeerHiddenStates(ctx, viewerUserID, peers)
}

func (s *countingStoriesService) GetPeerStoryProjections(ctx context.Context, viewerUserID int64, peers []domain.Peer, now int) ([]domain.PeerStoryProjection, error) {
	s.projectionCalls++
	return s.StoriesService.GetPeerStoryProjections(ctx, viewerUserID, peers, now)
}

func (s *countingStoriesService) HasPinnedStories(ctx context.Context, viewerUserID int64, peer domain.Peer, now int) (bool, error) {
	s.pinnedAvailCalls++
	return s.StoriesService.HasPinnedStories(ctx, viewerUserID, peer, now)
}

func (s *countingStoriesService) GetPinnedStories(ctx context.Context, viewerUserID int64, peer domain.Peer, offsetID, limit, now int) (domain.StoryList, error) {
	s.pinnedStoriesCalls++
	return s.StoriesService.GetPinnedStories(ctx, viewerUserID, peer, offsetID, limit, now)
}

func TestUsersProjectStoriesMaxID(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, err := userStore.Create(ctx, domain.User{AccessHash: 93001, Phone: "15550093001", FirstName: "Owner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	viewer, err := userStore.Create(ctx, domain.User{AccessHash: 93002, Phone: "15550093002", FirstName: "Viewer"})
	if err != nil {
		t.Fatalf("create viewer: %v", err)
	}
	storyStore := memory.NewStoryStore()
	if _, err := storyStore.UpsertStory(ctx, domain.UpsertStoryRequest{Story: domain.Story{
		Owner:      domain.Peer{Type: domain.PeerTypeUser, ID: owner.ID},
		ID:         7,
		Date:       1700000000,
		ExpireDate: 1700003600,
		Pinned:     true,
		Public:     true,
	}}); err != nil {
		t.Fatalf("upsert story: %v", err)
	}
	r := New(Config{}, Deps{
		Users:   appusers.NewService(userStore),
		Stories: appstories.NewService(storyStore),
	}, zaptest.NewLogger(t), fixedClock{now: time.Unix(1700000100, 0)})

	users, err := r.onUsersGetUsers(WithUserID(ctx, viewer.ID), []tg.InputUserClass{
		&tg.InputUser{UserID: owner.ID, AccessHash: owner.AccessHash},
	})
	if err != nil {
		t.Fatalf("get users: %v", err)
	}
	if len(users) != 1 {
		t.Fatalf("users = %d, want one", len(users))
	}
	assertUserStoryMaxID(t, users[0], 7)

	full, err := r.onUsersGetFullUser(WithUserID(ctx, viewer.ID), &tg.InputUser{UserID: owner.ID, AccessHash: owner.AccessHash})
	if err != nil {
		t.Fatalf("get full user: %v", err)
	}
	if len(full.Users) != 1 {
		t.Fatalf("full users = %d, want one", len(full.Users))
	}
	assertUserStoryMaxID(t, full.Users[0], 7)
	if !full.FullUser.GetStoriesPinnedAvailable() {
		t.Fatalf("user full stories_pinned_available = false, want true")
	}
}

func TestUsersProjectStoriesHiddenWithoutActiveStory(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, err := userStore.Create(ctx, domain.User{AccessHash: 93101, Phone: "15550093101", FirstName: "HiddenOwner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	viewer, err := userStore.Create(ctx, domain.User{AccessHash: 93102, Phone: "15550093102", FirstName: "Viewer"})
	if err != nil {
		t.Fatalf("create viewer: %v", err)
	}
	storyStore := memory.NewStoryStore()
	r := New(Config{}, Deps{
		Users:   appusers.NewService(userStore),
		Stories: appstories.NewService(storyStore),
	}, zaptest.NewLogger(t), fixedClock{now: time.Unix(1700000100, 0)})
	reqCtx := WithUserID(ctx, viewer.ID)

	if ok, err := r.onStoriesTogglePeerStoriesHidden(reqCtx, &tg.StoriesTogglePeerStoriesHiddenRequest{
		Peer:   &tg.InputPeerUser{UserID: owner.ID, AccessHash: owner.AccessHash},
		Hidden: true,
	}); err != nil || !ok {
		t.Fatalf("toggle hidden = %v, %v", ok, err)
	}
	users, err := r.onUsersGetUsers(reqCtx, []tg.InputUserClass{
		&tg.InputUser{UserID: owner.ID, AccessHash: owner.AccessHash},
	})
	if err != nil {
		t.Fatalf("get users after hide: %v", err)
	}
	if got := storiesHiddenForUser(t, users, owner.ID); !got {
		t.Fatalf("users.getUsers stories_hidden = false, want true")
	}

	if ok, err := r.onStoriesTogglePeerStoriesHidden(reqCtx, &tg.StoriesTogglePeerStoriesHiddenRequest{
		Peer:   &tg.InputPeerUser{UserID: owner.ID, AccessHash: owner.AccessHash},
		Hidden: false,
	}); err != nil || !ok {
		t.Fatalf("toggle unhidden = %v, %v", ok, err)
	}
	users, err = r.onUsersGetUsers(reqCtx, []tg.InputUserClass{
		&tg.InputUser{UserID: owner.ID, AccessHash: owner.AccessHash},
	})
	if err != nil {
		t.Fatalf("get users after unhide: %v", err)
	}
	if got := storiesHiddenForUser(t, users, owner.ID); got {
		t.Fatalf("users.getUsers stories_hidden = true, want false")
	}
}

func TestChannelsProjectStoriesMaxID(t *testing.T) {
	ctx := context.Background()
	const ownerID int64 = 1000000001
	channelStore := memory.NewChannelStore()
	channelService := appchannels.NewService(channelStore)
	created, err := channelService.CreateChannel(ctx, ownerID, domain.CreateChannelRequest{
		CreatorUserID: ownerID,
		Title:         "story channel",
		Broadcast:     true,
		Date:          1700000000,
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	storyStore := memory.NewStoryStore()
	if _, err := storyStore.UpsertStory(ctx, domain.UpsertStoryRequest{Story: domain.Story{
		Owner:      domain.Peer{Type: domain.PeerTypeChannel, ID: created.Channel.ID},
		ID:         11,
		Date:       1700000000,
		ExpireDate: 1700003600,
		Pinned:     true,
		Public:     true,
	}}); err != nil {
		t.Fatalf("upsert channel story: %v", err)
	}
	r := New(Config{}, Deps{
		Channels: channelService,
		Stories:  appstories.NewService(storyStore),
	}, zaptest.NewLogger(t), fixedClock{now: time.Unix(1700000100, 0)})
	input := &tg.InputChannel{ChannelID: created.Channel.ID, AccessHash: created.Channel.AccessHash}

	chats, err := r.onChannelsGetChannels(WithUserID(ctx, ownerID), []tg.InputChannelClass{input})
	if err != nil {
		t.Fatalf("get channels: %v", err)
	}
	list, ok := chats.(*tg.MessagesChats)
	if !ok || len(list.Chats) != 1 {
		t.Fatalf("channels = %T %+v, want one messages.chats", chats, chats)
	}
	assertChannelStoryMaxID(t, list.Chats[0], 11)

	full, err := r.onChannelsGetFullChannel(WithUserID(ctx, ownerID), input)
	if err != nil {
		t.Fatalf("get full channel: %v", err)
	}
	if len(full.Chats) != 1 {
		t.Fatalf("full chats = %d, want one", len(full.Chats))
	}
	assertChannelStoryMaxID(t, full.Chats[0], 11)
	channelFull, ok := full.FullChat.(*tg.ChannelFull)
	if !ok {
		t.Fatalf("full chat = %T, want *tg.ChannelFull", full.FullChat)
	}
	if !channelFull.GetStoriesPinnedAvailable() {
		t.Fatalf("channel full stories_pinned_available = false, want true")
	}
}

func TestFullPeerPinnedAvailabilityUsesBooleanCache(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, err := userStore.Create(ctx, domain.User{AccessHash: 93401, Phone: "15550093401", FirstName: "Owner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	viewer, err := userStore.Create(ctx, domain.User{AccessHash: 93402, Phone: "15550093402", FirstName: "Viewer"})
	if err != nil {
		t.Fatalf("create viewer: %v", err)
	}
	channelStore := memory.NewChannelStore()
	channelService := appchannels.NewService(channelStore)
	created, err := channelService.CreateChannel(ctx, owner.ID, domain.CreateChannelRequest{
		CreatorUserID: owner.ID,
		Title:         "pinned story profile cache",
		Broadcast:     true,
		Date:          1700000000,
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	storyStore := memory.NewStoryStore()
	if _, err := storyStore.UpsertStory(ctx, domain.UpsertStoryRequest{Story: domain.Story{
		Owner:      domain.Peer{Type: domain.PeerTypeUser, ID: owner.ID},
		ID:         7,
		Date:       1700000000,
		ExpireDate: 1700003600,
		Pinned:     true,
		Public:     true,
	}}); err != nil {
		t.Fatalf("upsert user story: %v", err)
	}
	if _, err := storyStore.UpsertStory(ctx, domain.UpsertStoryRequest{Story: domain.Story{
		Owner:      domain.Peer{Type: domain.PeerTypeChannel, ID: created.Channel.ID},
		ID:         11,
		Date:       1700000000,
		ExpireDate: 1700003600,
		Pinned:     true,
		Public:     true,
	}}); err != nil {
		t.Fatalf("upsert channel story: %v", err)
	}
	stories := &countingStoriesService{StoriesService: appstories.NewService(storyStore)}
	r := New(Config{}, Deps{
		Users:    appusers.NewService(userStore),
		Channels: channelService,
		Stories:  stories,
	}, zaptest.NewLogger(t), fixedClock{now: time.Unix(1700000100, 0)})

	for i := 0; i < 2; i++ {
		full, err := r.onUsersGetFullUser(WithUserID(ctx, viewer.ID), &tg.InputUser{UserID: owner.ID, AccessHash: owner.AccessHash})
		if err != nil {
			t.Fatalf("get full user #%d: %v", i+1, err)
		}
		if !full.FullUser.GetStoriesPinnedAvailable() {
			t.Fatalf("user full #%d stories_pinned_available = false, want true", i+1)
		}
	}
	input := &tg.InputChannel{ChannelID: created.Channel.ID, AccessHash: created.Channel.AccessHash}
	for i := 0; i < 2; i++ {
		full, err := r.onChannelsGetFullChannel(WithUserID(ctx, owner.ID), input)
		if err != nil {
			t.Fatalf("get full channel #%d: %v", i+1, err)
		}
		channelFull, ok := full.FullChat.(*tg.ChannelFull)
		if !ok {
			t.Fatalf("full channel #%d = %T, want *tg.ChannelFull", i+1, full.FullChat)
		}
		if !channelFull.GetStoriesPinnedAvailable() {
			t.Fatalf("channel full #%d stories_pinned_available = false, want true", i+1)
		}
	}
	if stories.pinnedAvailCalls != 2 {
		t.Fatalf("HasPinnedStories calls = %d, want 2", stories.pinnedAvailCalls)
	}
	if stories.pinnedStoriesCalls != 0 {
		t.Fatalf("GetPinnedStories calls = %d, want 0", stories.pinnedStoriesCalls)
	}
}

func TestGetPinnedStoriesCachesUntilStoryMutation(t *testing.T) {
	ctx := context.Background()
	ownerID := int64(93541)
	owner := domain.Peer{Type: domain.PeerTypeUser, ID: ownerID}
	storyStore := memory.NewStoryStore()
	if _, err := storyStore.UpsertStory(ctx, domain.UpsertStoryRequest{Story: domain.Story{
		Owner:      owner,
		ID:         61,
		Date:       1700000000,
		ExpireDate: 1700003600,
		Pinned:     true,
		Public:     true,
	}}); err != nil {
		t.Fatalf("upsert pinned story: %v", err)
	}
	stories := &countingStoriesService{StoriesService: appstories.NewService(storyStore)}
	r := New(Config{}, Deps{Stories: stories}, zaptest.NewLogger(t), fixedClock{now: time.Unix(1700000100, 0)})
	reqCtx := WithUserID(ctx, ownerID)
	req := &tg.StoriesGetPinnedStoriesRequest{Peer: &tg.InputPeerSelf{}, Limit: 10}

	for i := 0; i < 2; i++ {
		got, err := r.onStoriesGetPinnedStories(reqCtx, req)
		if err != nil {
			t.Fatalf("get pinned stories #%d: %v", i+1, err)
		}
		if got.Count != 1 || len(got.Stories) != 1 {
			t.Fatalf("pinned stories #%d = count %d len %d, want one", i+1, got.Count, len(got.Stories))
		}
	}
	if stories.pinnedStoriesCalls != 1 {
		t.Fatalf("GetPinnedStories calls = %d, want 1 after cache hit", stories.pinnedStoriesCalls)
	}

	if _, err := r.onStoriesTogglePinned(reqCtx, &tg.StoriesTogglePinnedRequest{
		Peer:   &tg.InputPeerSelf{},
		ID:     []int{61},
		Pinned: false,
	}); err != nil {
		t.Fatalf("unpin story: %v", err)
	}
	got, err := r.onStoriesGetPinnedStories(reqCtx, req)
	if err != nil {
		t.Fatalf("get pinned stories after unpin: %v", err)
	}
	if got.Count != 0 || len(got.Stories) != 0 {
		t.Fatalf("pinned stories after unpin = count %d len %d, want empty", got.Count, len(got.Stories))
	}
	if stories.pinnedStoriesCalls != 2 {
		t.Fatalf("GetPinnedStories calls after mutation = %d, want 2", stories.pinnedStoriesCalls)
	}
}

func TestChannelParticipantsProjectStoriesMaxID(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, err := userStore.Create(ctx, domain.User{AccessHash: 93501, Phone: "15550093501", FirstName: "Owner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	member, err := userStore.Create(ctx, domain.User{AccessHash: 93502, Phone: "15550093502", FirstName: "Member"})
	if err != nil {
		t.Fatalf("create member: %v", err)
	}
	channelStore := memory.NewChannelStore()
	channelService := appchannels.NewService(channelStore)
	created, err := channelService.CreateChannel(ctx, owner.ID, domain.CreateChannelRequest{
		CreatorUserID: owner.ID,
		Title:         "story participants",
		Megagroup:     true,
		MemberUserIDs: []int64{member.ID},
		Date:          1700000000,
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	storyStore := memory.NewStoryStore()
	if _, err := storyStore.UpsertStory(ctx, domain.UpsertStoryRequest{Story: domain.Story{
		Owner:      domain.Peer{Type: domain.PeerTypeUser, ID: owner.ID},
		ID:         29,
		Date:       1700000000,
		ExpireDate: 1700003600,
		Public:     true,
	}}); err != nil {
		t.Fatalf("upsert inviter story: %v", err)
	}
	if _, err := storyStore.UpsertStory(ctx, domain.UpsertStoryRequest{Story: domain.Story{
		Owner:      domain.Peer{Type: domain.PeerTypeUser, ID: member.ID},
		ID:         31,
		Date:       1700000000,
		ExpireDate: 1700003600,
		Public:     true,
	}}); err != nil {
		t.Fatalf("upsert member story: %v", err)
	}
	r := New(Config{}, Deps{
		Users:    appusers.NewService(userStore),
		Channels: channelService,
		Stories:  appstories.NewService(storyStore),
	}, zaptest.NewLogger(t), fixedClock{now: time.Unix(1700000100, 0)})
	input := &tg.InputChannel{ChannelID: created.Channel.ID, AccessHash: created.Channel.AccessHash}

	participants, err := r.onChannelsGetParticipants(WithUserID(ctx, owner.ID), &tg.ChannelsGetParticipantsRequest{
		Channel: input,
		Filter:  &tg.ChannelParticipantsRecent{},
		Limit:   10,
	})
	if err != nil {
		t.Fatalf("get participants: %v", err)
	}
	page, ok := participants.(*tg.ChannelsChannelParticipants)
	if !ok {
		t.Fatalf("participants = %T, want *tg.ChannelsChannelParticipants", participants)
	}
	assertUserStoryMaxID(t, findUserClass(page.Users, member.ID), 31)
	assertUserStoryMaxID(t, findUserClass(page.Users, owner.ID), 29)

	single, err := r.onChannelsGetParticipant(WithUserID(ctx, member.ID), &tg.ChannelsGetParticipantRequest{
		Channel:     input,
		Participant: &tg.InputPeerSelf{},
	})
	if err != nil {
		t.Fatalf("get participant: %v", err)
	}
	self, ok := single.Participant.(*tg.ChannelParticipantSelf)
	if !ok {
		t.Fatalf("participant = %T, want *tg.ChannelParticipantSelf", single.Participant)
	}
	if self.InviterID != owner.ID {
		t.Fatalf("self inviter = %d, want owner %d", self.InviterID, owner.ID)
	}
	assertUserStoryMaxID(t, findUserClass(single.Users, member.ID), 31)
	assertUserStoryMaxID(t, findUserClass(single.Users, owner.ID), 29)
}

func TestChannelParticipantsReuseStoryProjectionWithinBurst(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, err := userStore.Create(ctx, domain.User{AccessHash: 93511, Phone: "15550093511", FirstName: "Owner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	member, err := userStore.Create(ctx, domain.User{AccessHash: 93512, Phone: "15550093512", FirstName: "Member"})
	if err != nil {
		t.Fatalf("create member: %v", err)
	}
	channelStore := memory.NewChannelStore()
	channelService := appchannels.NewService(channelStore)
	created, err := channelService.CreateChannel(ctx, owner.ID, domain.CreateChannelRequest{
		CreatorUserID: owner.ID,
		Title:         "story participants cache",
		Megagroup:     true,
		MemberUserIDs: []int64{member.ID},
		Date:          1700000000,
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	storyStore := memory.NewStoryStore()
	if _, err := storyStore.UpsertStory(ctx, domain.UpsertStoryRequest{Story: domain.Story{
		Owner:      domain.Peer{Type: domain.PeerTypeUser, ID: owner.ID},
		ID:         41,
		Date:       1700000000,
		ExpireDate: 1700003600,
		Public:     true,
	}}); err != nil {
		t.Fatalf("upsert owner story: %v", err)
	}
	if _, err := storyStore.UpsertStory(ctx, domain.UpsertStoryRequest{Story: domain.Story{
		Owner:      domain.Peer{Type: domain.PeerTypeUser, ID: member.ID},
		ID:         43,
		Date:       1700000000,
		ExpireDate: 1700003600,
		Public:     true,
	}}); err != nil {
		t.Fatalf("upsert member story: %v", err)
	}
	stories := &countingStoriesService{StoriesService: appstories.NewService(storyStore)}
	r := New(Config{}, Deps{
		Users:    appusers.NewService(userStore),
		Channels: channelService,
		Stories:  stories,
	}, zaptest.NewLogger(t), fixedClock{now: time.Unix(1700000100, 0)})
	input := &tg.InputChannel{ChannelID: created.Channel.ID, AccessHash: created.Channel.AccessHash}

	for i := 0; i < 2; i++ {
		participants, err := r.onChannelsGetParticipants(WithUserID(ctx, owner.ID), &tg.ChannelsGetParticipantsRequest{
			Channel: input,
			Filter:  &tg.ChannelParticipantsRecent{},
			Limit:   10,
		})
		if err != nil {
			t.Fatalf("get participants #%d: %v", i+1, err)
		}
		page, ok := participants.(*tg.ChannelsChannelParticipants)
		if !ok {
			t.Fatalf("participants #%d = %T, want *tg.ChannelsChannelParticipants", i+1, participants)
		}
		assertUserStoryMaxID(t, findUserClass(page.Users, member.ID), 43)
		assertUserStoryMaxID(t, findUserClass(page.Users, owner.ID), 41)
	}
	if stories.projectionCalls != 1 || stories.maxIDCalls != 0 || stories.hiddenCalls != 0 {
		t.Fatalf("story projection calls combined=%d max_id=%d hidden=%d, want 1/0/0", stories.projectionCalls, stories.maxIDCalls, stories.hiddenCalls)
	}
}

func TestStoryProjectionSingleflightsConcurrentMiss(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, err := userStore.Create(ctx, domain.User{AccessHash: 93521, Phone: "15550093521", FirstName: "Owner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	member, err := userStore.Create(ctx, domain.User{AccessHash: 93522, Phone: "15550093522", FirstName: "Member"})
	if err != nil {
		t.Fatalf("create member: %v", err)
	}
	storyStore := memory.NewStoryStore()
	for _, item := range []struct {
		owner domain.Peer
		id    int
	}{
		{owner: domain.Peer{Type: domain.PeerTypeUser, ID: owner.ID}, id: 51},
		{owner: domain.Peer{Type: domain.PeerTypeUser, ID: member.ID}, id: 53},
	} {
		if _, err := storyStore.UpsertStory(ctx, domain.UpsertStoryRequest{Story: domain.Story{
			Owner:      item.owner,
			ID:         item.id,
			Date:       1700000000,
			ExpireDate: 1700003600,
			Public:     true,
		}}); err != nil {
			t.Fatalf("upsert story %d: %v", item.id, err)
		}
	}
	stories := &blockingProjectionStoriesService{
		StoriesService: appstories.NewService(storyStore),
		started:        make(chan struct{}),
		release:        make(chan struct{}),
	}
	r := New(Config{}, Deps{Stories: stories}, zaptest.NewLogger(t), fixedClock{now: time.Unix(1700000100, 0)})
	peers := []domain.Peer{
		{Type: domain.PeerTypeUser, ID: owner.ID},
		{Type: domain.PeerTypeUser, ID: member.ID},
	}

	const goroutines = 16
	var wg sync.WaitGroup
	errs := make(chan string, goroutines)
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			recent, hidden := r.storyProjectionMaps(ctx, owner.ID, peers)
			if story, ok := recent[peers[0]]; !ok || story.MaxID != 51 {
				errs <- "owner story max id missing"
				return
			}
			if story, ok := recent[peers[1]]; !ok || story.MaxID != 53 {
				errs <- "member story max id missing"
				return
			}
			if got := hidden[peers[0]] || hidden[peers[1]]; got {
				errs <- "hidden state should be false"
			}
		}()
	}
	<-stories.started
	time.Sleep(20 * time.Millisecond)
	close(stories.release)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != "" {
			t.Fatal(err)
		}
	}
	if got := stories.callCount(); got != 1 {
		t.Fatalf("projection calls = %d, want 1", got)
	}
}

func TestStoryProjectionCacheSurvivesFastDialogBurstWindow(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1700000100, 0)
	viewerID := int64(93531)
	peer := domain.Peer{Type: domain.PeerTypeUser, ID: 93532}
	cache := newStoryProjectionCache(func() time.Time { return now })

	loadCalls := 0
	load := func(_ context.Context, _ []domain.Peer) (map[domain.Peer]tg.RecentStory, map[domain.Peer]bool) {
		loadCalls++
		return map[domain.Peer]tg.RecentStory{peer: {MaxID: 7}}, map[domain.Peer]bool{peer: true}
	}

	// 首次:miss → load → 缓存。
	if recent, hidden := cache.getMany(ctx, viewerID, []domain.Peer{peer}, load); recent[peer].MaxID != 7 || !hidden[peer] {
		t.Fatalf("first getMany recent=%+v hidden=%v", recent[peer], hidden[peer])
	}
	// 推进 11s(仍在 TTL 内):再次应命中缓存、不再 load。
	now = now.Add(11 * time.Second)
	recent, hidden := cache.getMany(ctx, viewerID, []domain.Peer{peer}, load)
	if loadCalls != 1 {
		t.Fatalf("burst window re-loaded: loadCalls=%d, want 1", loadCalls)
	}
	if story, ok := recent[peer]; !ok || story.MaxID != 7 {
		t.Fatalf("recent story after burst window = %+v ok=%v, want max_id 7", story, ok)
	}
	if state, ok := hidden[peer]; !ok || !state {
		t.Fatalf("hidden after burst window = %v ok=%v, want true", state, ok)
	}
}

func TestChannelAdminLogProjectsStoriesMaxID(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, err := userStore.Create(ctx, domain.User{AccessHash: 93601, Phone: "15550093601", FirstName: "Owner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	channelStore := memory.NewChannelStore()
	channelService := appchannels.NewService(channelStore)
	created, err := channelService.CreateChannel(ctx, owner.ID, domain.CreateChannelRequest{
		CreatorUserID: owner.ID,
		Title:         "story admin log",
		Megagroup:     true,
		Date:          1700000000,
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	if _, err := channelService.EditTitle(ctx, owner.ID, domain.EditChannelTitleRequest{
		ChannelID: created.Channel.ID,
		Title:     "story admin log renamed",
		Date:      1700000010,
	}); err != nil {
		t.Fatalf("edit title: %v", err)
	}
	storyStore := memory.NewStoryStore()
	if _, err := storyStore.UpsertStory(ctx, domain.UpsertStoryRequest{Story: domain.Story{
		Owner:      domain.Peer{Type: domain.PeerTypeUser, ID: owner.ID},
		ID:         37,
		Date:       1700000000,
		ExpireDate: 1700003600,
		Public:     true,
	}}); err != nil {
		t.Fatalf("upsert owner story: %v", err)
	}
	if _, err := storyStore.UpsertStory(ctx, domain.UpsertStoryRequest{Story: domain.Story{
		Owner:      domain.Peer{Type: domain.PeerTypeChannel, ID: created.Channel.ID},
		ID:         41,
		Date:       1700000000,
		ExpireDate: 1700003600,
		Public:     true,
	}}); err != nil {
		t.Fatalf("upsert channel story: %v", err)
	}
	r := New(Config{}, Deps{
		Users:    appusers.NewService(userStore),
		Channels: channelService,
		Stories:  appstories.NewService(storyStore),
	}, zaptest.NewLogger(t), fixedClock{now: time.Unix(1700000100, 0)})
	input := &tg.InputChannel{ChannelID: created.Channel.ID, AccessHash: created.Channel.AccessHash}

	adminLog, err := r.onChannelsGetAdminLog(WithUserID(ctx, owner.ID), &tg.ChannelsGetAdminLogRequest{
		Channel: input,
		Limit:   10,
	})
	if err != nil {
		t.Fatalf("get admin log: %v", err)
	}
	if len(adminLog.Events) == 0 {
		t.Fatalf("admin log events = none, want title event")
	}
	assertUserStoryMaxID(t, findUserClass(adminLog.Users, owner.ID), 37)
	assertChannelStoryMaxID(t, findChannelClass(adminLog.Chats, created.Channel.ID), 41)
}

func TestMessagesHistoryAndSearchProjectStoriesMaxID(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, err := userStore.Create(ctx, domain.User{AccessHash: 93701, Phone: "15550093701", FirstName: "Owner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	viewer, err := userStore.Create(ctx, domain.User{AccessHash: 93702, Phone: "15550093702", FirstName: "Viewer"})
	if err != nil {
		t.Fatalf("create viewer: %v", err)
	}
	storyStore := memory.NewStoryStore()
	if _, err := storyStore.UpsertStory(ctx, domain.UpsertStoryRequest{Story: domain.Story{
		Owner:      domain.Peer{Type: domain.PeerTypeUser, ID: owner.ID},
		ID:         43,
		Date:       1700000000,
		ExpireDate: 1700003600,
		Public:     true,
	}}); err != nil {
		t.Fatalf("upsert owner story: %v", err)
	}
	messages := &captureMessages{list: domain.MessageList{
		Messages: []domain.Message{{
			ID:          1,
			OwnerUserID: viewer.ID,
			Peer:        domain.Peer{Type: domain.PeerTypeUser, ID: owner.ID},
			From:        domain.Peer{Type: domain.PeerTypeUser, ID: owner.ID},
			Date:        1700000100,
			Body:        "needle",
		}},
		Users: []domain.User{owner},
		Count: 1,
	}}
	r := New(Config{}, Deps{
		Messages: messages,
		Stories:  appstories.NewService(storyStore),
	}, zaptest.NewLogger(t), fixedClock{now: time.Unix(1700000100, 0)})
	peer := &tg.InputPeerUser{UserID: owner.ID, AccessHash: owner.AccessHash}

	history := dispatchMessagesPayload(t, r, WithUserID(ctx, viewer.ID), &tg.MessagesGetHistoryRequest{
		Peer:  peer,
		Limit: 20,
	})
	_, _, users := searchMessagesPayload(t, history)
	assertUserStoryMaxID(t, findUserClass(users, owner.ID), 43)

	search := dispatchMessagesPayload(t, r, WithUserID(ctx, viewer.ID), &tg.MessagesSearchRequest{
		Peer:   peer,
		Q:      "needle",
		Filter: &tg.InputMessagesFilterEmpty{},
		Limit:  20,
	})
	_, _, users = searchMessagesPayload(t, search)
	assertUserStoryMaxID(t, findUserClass(users, owner.ID), 43)
}

func TestChannelHistoryAndSearchProjectStoriesMaxID(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, err := userStore.Create(ctx, domain.User{AccessHash: 93801, Phone: "15550093801", FirstName: "Owner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	channelStore := memory.NewChannelStore()
	channelService := appchannels.NewService(channelStore)
	created, err := channelService.CreateChannel(ctx, owner.ID, domain.CreateChannelRequest{
		CreatorUserID: owner.ID,
		Title:         "story history channel",
		Megagroup:     true,
		Date:          1700000000,
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	if _, err := channelService.SendMessage(ctx, owner.ID, domain.SendChannelMessageRequest{
		ChannelID: created.Channel.ID,
		RandomID:  1,
		Message:   "channel needle",
		Date:      1700000010,
	}); err != nil {
		t.Fatalf("send channel message: %v", err)
	}
	storyStore := memory.NewStoryStore()
	if _, err := storyStore.UpsertStory(ctx, domain.UpsertStoryRequest{Story: domain.Story{
		Owner:      domain.Peer{Type: domain.PeerTypeUser, ID: owner.ID},
		ID:         47,
		Date:       1700000000,
		ExpireDate: 1700003600,
		Public:     true,
	}}); err != nil {
		t.Fatalf("upsert owner story: %v", err)
	}
	if _, err := storyStore.UpsertStory(ctx, domain.UpsertStoryRequest{Story: domain.Story{
		Owner:      domain.Peer{Type: domain.PeerTypeChannel, ID: created.Channel.ID},
		ID:         53,
		Date:       1700000000,
		ExpireDate: 1700003600,
		Public:     true,
	}}); err != nil {
		t.Fatalf("upsert channel story: %v", err)
	}
	r := New(Config{}, Deps{
		Users:    appusers.NewService(userStore),
		Channels: channelService,
		Stories:  appstories.NewService(storyStore),
	}, zaptest.NewLogger(t), fixedClock{now: time.Unix(1700000100, 0)})
	peer := &tg.InputPeerChannel{ChannelID: created.Channel.ID, AccessHash: created.Channel.AccessHash}

	history := dispatchMessagesPayload(t, r, WithUserID(ctx, owner.ID), &tg.MessagesGetHistoryRequest{
		Peer:  peer,
		Limit: 20,
	})
	_, chats, users := searchMessagesPayload(t, history)
	assertUserStoryMaxID(t, findUserClass(users, owner.ID), 47)
	assertChannelStoryMaxID(t, findChannelClass(chats, created.Channel.ID), 53)

	search := dispatchMessagesPayload(t, r, WithUserID(ctx, owner.ID), &tg.MessagesSearchRequest{
		Peer:   peer,
		Q:      "needle",
		Filter: &tg.InputMessagesFilterEmpty{},
		Limit:  20,
	})
	_, chats, users = searchMessagesPayload(t, search)
	assertUserStoryMaxID(t, findUserClass(users, owner.ID), 47)
	assertChannelStoryMaxID(t, findChannelClass(chats, created.Channel.ID), 53)
}

func TestRepliesDiscussionAndForumTopicsProjectStoriesMaxID(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, err := userStore.Create(ctx, domain.User{AccessHash: 93901, Phone: "15550093901", FirstName: "Owner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	member, err := userStore.Create(ctx, domain.User{AccessHash: 93902, Phone: "15550093902", FirstName: "Member"})
	if err != nil {
		t.Fatalf("create member: %v", err)
	}
	channelStore := memory.NewChannelStore()
	channelService := appchannels.NewService(channelStore)
	broadcast, err := channelService.CreateChannel(ctx, owner.ID, domain.CreateChannelRequest{
		CreatorUserID: owner.ID,
		Title:         "story discussion source",
		Broadcast:     true,
		Date:          1700000000,
	})
	if err != nil {
		t.Fatalf("create broadcast: %v", err)
	}
	group, err := channelService.CreateChannel(ctx, owner.ID, domain.CreateChannelRequest{
		CreatorUserID: owner.ID,
		Title:         "story discussion group",
		Megagroup:     true,
		MemberUserIDs: []int64{member.ID},
		Date:          1700000001,
	})
	if err != nil {
		t.Fatalf("create group: %v", err)
	}
	forum, err := channelService.CreateChannel(ctx, owner.ID, domain.CreateChannelRequest{
		CreatorUserID: owner.ID,
		Title:         "story forum",
		Megagroup:     true,
		Forum:         true,
		MemberUserIDs: []int64{member.ID},
		Date:          1700000002,
	})
	if err != nil {
		t.Fatalf("create forum: %v", err)
	}
	storyStore := memory.NewStoryStore()
	upsertStory := func(peer domain.Peer, id int) {
		t.Helper()
		if _, err := storyStore.UpsertStory(ctx, domain.UpsertStoryRequest{Story: domain.Story{
			Owner:      peer,
			ID:         id,
			Date:       1700000000,
			ExpireDate: 1700003600,
			Public:     true,
		}}); err != nil {
			t.Fatalf("upsert story %v/%d: %v", peer, id, err)
		}
	}
	upsertStory(domain.Peer{Type: domain.PeerTypeUser, ID: owner.ID}, 57)
	upsertStory(domain.Peer{Type: domain.PeerTypeUser, ID: member.ID}, 59)
	upsertStory(domain.Peer{Type: domain.PeerTypeChannel, ID: broadcast.Channel.ID}, 61)
	upsertStory(domain.Peer{Type: domain.PeerTypeChannel, ID: group.Channel.ID}, 63)
	upsertStory(domain.Peer{Type: domain.PeerTypeChannel, ID: forum.Channel.ID}, 67)
	r := New(Config{}, Deps{
		Users:    appusers.NewService(userStore),
		Channels: channelService,
		Stories:  appstories.NewService(storyStore),
	}, zaptest.NewLogger(t), fixedClock{now: time.Unix(1700000100, 0)})
	broadcastPeer := &tg.InputPeerChannel{ChannelID: broadcast.Channel.ID, AccessHash: broadcast.Channel.AccessHash}
	groupPeer := &tg.InputPeerChannel{ChannelID: group.Channel.ID, AccessHash: group.Channel.AccessHash}
	forumPeer := &tg.InputPeerChannel{ChannelID: forum.Channel.ID, AccessHash: forum.Channel.AccessHash}
	if ok, err := r.onChannelsSetDiscussionGroup(WithUserID(ctx, owner.ID), &tg.ChannelsSetDiscussionGroupRequest{
		Broadcast: &tg.InputChannel{ChannelID: broadcast.Channel.ID, AccessHash: broadcast.Channel.AccessHash},
		Group:     &tg.InputChannel{ChannelID: group.Channel.ID, AccessHash: group.Channel.AccessHash},
	}); err != nil || !ok {
		t.Fatalf("set discussion group = ok %v err %v, want true", ok, err)
	}

	postUpdates, err := r.onMessagesSendMessage(WithUserID(ctx, owner.ID), &tg.MessagesSendMessageRequest{
		Peer:     broadcastPeer,
		Message:  "story post",
		RandomID: 939001,
	})
	if err != nil {
		t.Fatalf("send broadcast post: %v", err)
	}
	post := findNewChannelTextMessage(t, postUpdates, "story post")
	discussion, err := r.onMessagesGetDiscussionMessage(WithUserID(ctx, owner.ID), &tg.MessagesGetDiscussionMessageRequest{
		Peer:  broadcastPeer,
		MsgID: post.ID,
	})
	if err != nil {
		t.Fatalf("get discussion message: %v", err)
	}
	if len(discussion.Messages) != 1 || len(discussion.Chats) != 2 {
		t.Fatalf("discussion = %+v, want one root message and source/group chats", discussion)
	}
	assertChannelStoryMaxID(t, findChannelClass(discussion.Chats, broadcast.Channel.ID), 61)
	assertChannelStoryMaxID(t, findChannelClass(discussion.Chats, group.Channel.ID), 63)
	root := discussion.Messages[0].(*tg.Message)

	replyTo := &tg.InputReplyToMessage{ReplyToMsgID: root.ID}
	replyReq := &tg.MessagesSendMessageRequest{
		Peer:     groupPeer,
		Message:  "story discussion reply",
		RandomID: 939002,
	}
	replyReq.SetReplyTo(replyTo)
	replyUpdates, err := r.onMessagesSendMessage(WithUserID(ctx, member.ID), replyReq)
	if err != nil {
		t.Fatalf("send discussion reply: %v", err)
	}
	comment := findNewChannelTextMessage(t, replyUpdates, "story discussion reply")
	replies, err := r.onMessagesGetReplies(WithUserID(ctx, owner.ID), &tg.MessagesGetRepliesRequest{
		Peer:  broadcastPeer,
		MsgID: post.ID,
		Limit: 10,
	})
	if err != nil {
		t.Fatalf("get replies: %v", err)
	}
	replyMessages, replyChats, replyUsers := searchMessagesPayload(t, replies)
	if len(replyMessages) != 1 || replyMessages[0].(*tg.Message).ID != comment.ID {
		t.Fatalf("replies = %T %+v, want linked discussion reply %d", replies, replies, comment.ID)
	}
	assertUserStoryMaxID(t, findUserClass(replyUsers, member.ID), 59)
	assertChannelStoryMaxID(t, findChannelClass(replyChats, broadcast.Channel.ID), 61)
	assertChannelStoryMaxID(t, findChannelClass(replyChats, group.Channel.ID), 63)

	forumTopics, err := r.onMessagesGetForumTopics(WithUserID(ctx, owner.ID), &tg.MessagesGetForumTopicsRequest{
		Peer:  forumPeer,
		Limit: 10,
	})
	if err != nil {
		t.Fatalf("get forum topics: %v", err)
	}
	if forumTopics.Count != 1 || len(forumTopics.Topics) != 1 || forumTopics.Pts == 0 {
		t.Fatalf("forum topics = %+v, want general topic with pts", forumTopics)
	}
	assertUserStoryMaxID(t, findUserClass(forumTopics.Users, owner.ID), 57)
	assertChannelStoryMaxID(t, findChannelClass(forumTopics.Chats, forum.Channel.ID), 67)

	forumTopicsByID, err := r.onMessagesGetForumTopicsByID(WithUserID(ctx, owner.ID), &tg.MessagesGetForumTopicsByIDRequest{
		Peer:   forumPeer,
		Topics: []int{forumGeneralTopicID},
	})
	if err != nil {
		t.Fatalf("get forum topics by id: %v", err)
	}
	if forumTopicsByID.Count != 1 || len(forumTopicsByID.Topics) != 1 || forumTopicsByID.Pts != forumTopics.Pts {
		t.Fatalf("forum topics by id = %+v, want same general topic pts %d", forumTopicsByID, forumTopics.Pts)
	}
	assertUserStoryMaxID(t, findUserClass(forumTopicsByID.Users, owner.ID), 57)
	assertChannelStoryMaxID(t, findChannelClass(forumTopicsByID.Chats, forum.Channel.ID), 67)
}

func TestViewsUnreadMentionsAndReactionsProjectStoriesMaxID(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, err := userStore.Create(ctx, domain.User{AccessHash: 93911, Phone: "15550093911", FirstName: "Owner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	member, err := userStore.Create(ctx, domain.User{AccessHash: 93912, Phone: "15550093912", FirstName: "Member"})
	if err != nil {
		t.Fatalf("create member: %v", err)
	}
	channelStore := memory.NewChannelStore()
	channelService := appchannels.NewService(channelStore)
	created, err := channelService.CreateChannel(ctx, owner.ID, domain.CreateChannelRequest{
		CreatorUserID: owner.ID,
		Title:         "story view surfaces",
		Megagroup:     true,
		MemberUserIDs: []int64{member.ID},
		Date:          1700000000,
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	storyStore := memory.NewStoryStore()
	upsertStory := func(peer domain.Peer, id int) {
		t.Helper()
		if _, err := storyStore.UpsertStory(ctx, domain.UpsertStoryRequest{Story: domain.Story{
			Owner:      peer,
			ID:         id,
			Date:       1700000000,
			ExpireDate: 1700003600,
			Public:     true,
		}}); err != nil {
			t.Fatalf("upsert story %v/%d: %v", peer, id, err)
		}
	}
	upsertStory(domain.Peer{Type: domain.PeerTypeUser, ID: owner.ID}, 71)
	upsertStory(domain.Peer{Type: domain.PeerTypeUser, ID: member.ID}, 73)
	upsertStory(domain.Peer{Type: domain.PeerTypeChannel, ID: created.Channel.ID}, 79)
	r := New(Config{}, Deps{
		Users:    appusers.NewService(userStore),
		Channels: channelService,
		Stories:  appstories.NewService(storyStore),
	}, zaptest.NewLogger(t), fixedClock{now: time.Unix(1700000100, 0)})
	peer := &tg.InputPeerChannel{ChannelID: created.Channel.ID, AccessHash: created.Channel.AccessHash}

	viewed, err := channelService.SendMessage(ctx, owner.ID, domain.SendChannelMessageRequest{
		ChannelID: created.Channel.ID,
		RandomID:  9391101,
		Message:   "story views",
		Date:      1700000010,
	})
	if err != nil {
		t.Fatalf("send viewed message: %v", err)
	}
	views, err := r.onMessagesGetMessagesViews(WithUserID(ctx, member.ID), &tg.MessagesGetMessagesViewsRequest{
		Peer: peer,
		ID:   []int{viewed.Message.ID},
	})
	if err != nil {
		t.Fatalf("get message views: %v", err)
	}
	if len(views.Views) != 1 {
		t.Fatalf("views = %+v, want one view", views)
	}
	assertUserStoryMaxID(t, findUserClass(views.Users, owner.ID), 71)
	assertChannelStoryMaxID(t, findChannelClass(views.Chats, created.Channel.ID), 79)

	mention, err := channelService.SendMessage(ctx, owner.ID, domain.SendChannelMessageRequest{
		ChannelID:      created.Channel.ID,
		RandomID:       9391102,
		Message:        "story mention",
		MentionUserIDs: []int64{member.ID},
		Date:           1700000020,
	})
	if err != nil {
		t.Fatalf("send mention: %v", err)
	}
	mentions, err := r.onMessagesGetUnreadMentions(WithUserID(ctx, member.ID), &tg.MessagesGetUnreadMentionsRequest{
		Peer:  peer,
		Limit: 10,
	})
	if err != nil {
		t.Fatalf("get unread mentions: %v", err)
	}
	mentionMessages, mentionChats, mentionUsers := searchMessagesPayload(t, mentions)
	if len(mentionMessages) != 1 || mentionMessages[0].(*tg.Message).ID != mention.Message.ID {
		t.Fatalf("mentions = %T %+v, want mention message %d", mentions, mentions, mention.Message.ID)
	}
	assertUserStoryMaxID(t, findUserClass(mentionUsers, owner.ID), 71)
	assertChannelStoryMaxID(t, findChannelClass(mentionChats, created.Channel.ID), 79)

	reactionReq := &tg.MessagesSendReactionRequest{
		Peer:     peer,
		MsgID:    viewed.Message.ID,
		Reaction: []tg.ReactionClass{&tg.ReactionEmoji{Emoticon: "\U0001f44d"}},
	}
	reactionReq.SetReaction(reactionReq.Reaction)
	if _, err := r.onMessagesSendReaction(WithUserID(ctx, member.ID), reactionReq); err != nil {
		t.Fatalf("send reaction: %v", err)
	}
	unreadReactions, err := r.onMessagesGetUnreadReactions(WithUserID(ctx, owner.ID), &tg.MessagesGetUnreadReactionsRequest{
		Peer:  peer,
		Limit: 10,
	})
	if err != nil {
		t.Fatalf("get unread reactions: %v", err)
	}
	reactionMessages, reactionChats, reactionUsers := searchMessagesPayload(t, unreadReactions)
	if len(reactionMessages) != 1 || reactionMessages[0].(*tg.Message).ID != viewed.Message.ID {
		t.Fatalf("unread reactions = %T %+v, want viewed message %d", unreadReactions, unreadReactions, viewed.Message.ID)
	}
	assertUserStoryMaxID(t, findUserClass(reactionUsers, member.ID), 73)
	assertChannelStoryMaxID(t, findChannelClass(reactionChats, created.Channel.ID), 79)
}

func TestMessageReactionsListProjectsStoriesMaxID(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, err := userStore.Create(ctx, domain.User{AccessHash: 93921, Phone: "15550093921", FirstName: "Owner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	member, err := userStore.Create(ctx, domain.User{AccessHash: 93922, Phone: "15550093922", FirstName: "Member"})
	if err != nil {
		t.Fatalf("create member: %v", err)
	}
	channelStore := memory.NewChannelStore()
	channelService := appchannels.NewService(channelStore)
	created, err := channelService.CreateChannel(ctx, owner.ID, domain.CreateChannelRequest{
		CreatorUserID: owner.ID,
		Title:         "story reaction list",
		Megagroup:     true,
		MemberUserIDs: []int64{member.ID},
		Date:          1700000000,
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	storyStore := memory.NewStoryStore()
	upsertStory := func(peer domain.Peer, id int) {
		t.Helper()
		if _, err := storyStore.UpsertStory(ctx, domain.UpsertStoryRequest{Story: domain.Story{
			Owner:      peer,
			ID:         id,
			Date:       1700000000,
			ExpireDate: 1700003600,
			Public:     true,
		}}); err != nil {
			t.Fatalf("upsert story %v/%d: %v", peer, id, err)
		}
	}
	upsertStory(domain.Peer{Type: domain.PeerTypeUser, ID: member.ID}, 83)
	upsertStory(domain.Peer{Type: domain.PeerTypeChannel, ID: created.Channel.ID}, 89)
	r := New(Config{}, Deps{
		Users:    appusers.NewService(userStore),
		Channels: channelService,
		Stories:  appstories.NewService(storyStore),
	}, zaptest.NewLogger(t), fixedClock{now: time.Unix(1700000100, 0)})
	peer := &tg.InputPeerChannel{ChannelID: created.Channel.ID, AccessHash: created.Channel.AccessHash}

	sent, err := channelService.SendMessage(ctx, owner.ID, domain.SendChannelMessageRequest{
		ChannelID: created.Channel.ID,
		RandomID:  9392101,
		Message:   "story reaction list",
		Date:      1700000010,
	})
	if err != nil {
		t.Fatalf("send channel message: %v", err)
	}
	reactionReq := &tg.MessagesSendReactionRequest{
		Peer:     peer,
		MsgID:    sent.Message.ID,
		Reaction: []tg.ReactionClass{&tg.ReactionEmoji{Emoticon: "\U0001f44d"}},
	}
	reactionReq.SetReaction(reactionReq.Reaction)
	if _, err := r.onMessagesSendReaction(WithUserID(ctx, member.ID), reactionReq); err != nil {
		t.Fatalf("send reaction: %v", err)
	}

	list, err := r.onMessagesGetMessageReactionsList(WithUserID(ctx, owner.ID), &tg.MessagesGetMessageReactionsListRequest{
		Peer:  peer,
		ID:    sent.Message.ID,
		Limit: 10,
	})
	if err != nil {
		t.Fatalf("get message reactions list: %v", err)
	}
	if list.Count != 1 || len(list.Reactions) != 1 {
		t.Fatalf("reaction list = %+v, want one reaction", list)
	}
	if peerID, ok := list.Reactions[0].PeerID.(*tg.PeerUser); !ok || peerID.UserID != member.ID {
		t.Fatalf("reaction peer = %+v, want member %d", list.Reactions[0].PeerID, member.ID)
	}
	assertUserStoryMaxID(t, findUserClass(list.Users, member.ID), 83)
	assertChannelStoryMaxID(t, findChannelClass(list.Chats, created.Channel.ID), 89)
}

func TestStoriesAllStoriesHydratesCompanionPeersWithStoriesMaxID(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, err := userStore.Create(ctx, domain.User{AccessHash: 94001, Phone: "15550094001", FirstName: "Owner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	channelStore := memory.NewChannelStore()
	channelService := appchannels.NewService(channelStore)
	created, err := channelService.CreateChannel(ctx, owner.ID, domain.CreateChannelRequest{
		CreatorUserID: owner.ID,
		Title:         "story strip channel",
		Broadcast:     true,
		Date:          1700000000,
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	storyStore := memory.NewStoryStore()
	if _, err := storyStore.UpsertStory(ctx, domain.UpsertStoryRequest{Story: domain.Story{
		Owner:      domain.Peer{Type: domain.PeerTypeUser, ID: owner.ID},
		ID:         7,
		Date:       1700000000,
		ExpireDate: 1700003600,
		Public:     true,
	}}); err != nil {
		t.Fatalf("upsert user story: %v", err)
	}
	if _, err := storyStore.UpsertStory(ctx, domain.UpsertStoryRequest{Story: domain.Story{
		Owner:      domain.Peer{Type: domain.PeerTypeChannel, ID: created.Channel.ID},
		ID:         11,
		Date:       1700000010,
		ExpireDate: 1700003600,
		Public:     true,
	}}); err != nil {
		t.Fatalf("upsert channel story: %v", err)
	}
	r := New(Config{}, Deps{
		Users:    appusers.NewService(userStore),
		Channels: channelService,
		Stories:  appstories.NewService(storyStore),
	}, zaptest.NewLogger(t), fixedClock{now: time.Unix(1700000100, 0)})

	got, err := r.onStoriesGetAllStories(WithUserID(ctx, owner.ID), &tg.StoriesGetAllStoriesRequest{})
	if err != nil {
		t.Fatalf("get all stories: %v", err)
	}
	all, ok := got.(*tg.StoriesAllStories)
	if !ok {
		t.Fatalf("all stories = %T, want *tg.StoriesAllStories", got)
	}
	if len(all.PeerStories) != 2 {
		t.Fatalf("peer stories = %d, want user and channel stories", len(all.PeerStories))
	}
	assertUserStoryMaxID(t, findUserClass(all.Users, owner.ID), 7)
	assertChannelStoryMaxID(t, findChannelClass(all.Chats, created.Channel.ID), 11)

	peerStories, err := r.onStoriesGetPeerStories(WithUserID(ctx, owner.ID), &tg.InputPeerUser{UserID: owner.ID, AccessHash: owner.AccessHash})
	if err != nil {
		t.Fatalf("get peer stories: %v", err)
	}
	assertUserStoryMaxID(t, findUserClass(peerStories.Users, owner.ID), 7)
}

func TestBuildOutboxStoryUpdatesHydratesCompanionPeersWithStoriesMaxID(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, err := userStore.Create(ctx, domain.User{AccessHash: 95001, Phone: "15550095001", FirstName: "Owner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	viewer, err := userStore.Create(ctx, domain.User{AccessHash: 95002, Phone: "15550095002", FirstName: "Viewer"})
	if err != nil {
		t.Fatalf("create viewer: %v", err)
	}
	ownerPeer := domain.Peer{Type: domain.PeerTypeUser, ID: owner.ID}
	storyStore := memory.NewStoryStore()
	story := domain.Story{
		Owner:      ownerPeer,
		ID:         13,
		Date:       1700000200,
		ExpireDate: 1700003800,
		Public:     true,
		Caption:    "outbox story",
	}
	if _, err := storyStore.UpsertStory(ctx, domain.UpsertStoryRequest{Story: story}); err != nil {
		t.Fatalf("upsert story: %v", err)
	}
	r := New(Config{}, Deps{
		Users:   appusers.NewService(userStore),
		Stories: appstories.NewService(storyStore),
	}, zaptest.NewLogger(t), fixedClock{now: time.Unix(1700000300, 0)})

	updates := r.BuildOutboxUpdates(ctx, []OutboxUpdateRequest{{
		TargetUserID: viewer.ID,
		Event: domain.UpdateEvent{
			UserID:   viewer.ID,
			Type:     domain.UpdateEventStory,
			Pts:      21,
			PtsCount: 1,
			Date:     story.Date,
			Peer:     ownerPeer,
			Story:    story,
		},
	}})
	if len(updates) != 1 || updates[0] == nil {
		t.Fatalf("updates = %+v, want one story update", updates)
	}
	if len(updates[0].Updates) != 2 {
		t.Fatalf("update count = %d, want updateStory plus pts bookkeeping", len(updates[0].Updates))
	}
	if _, ok := updates[0].Updates[0].(*tg.UpdateStory); !ok {
		t.Fatalf("first update = %T, want updateStory", updates[0].Updates[0])
	}
	assertUserStoryMaxID(t, findUserClass(updates[0].Users, owner.ID), 13)
}

func TestBuildOutboxNewStoryReactionHydratesReactorUser(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, err := userStore.Create(ctx, domain.User{AccessHash: 95201, Phone: "15550095201", FirstName: "Owner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	reactor, err := userStore.Create(ctx, domain.User{AccessHash: 95202, Phone: "15550095202", FirstName: "Reactor"})
	if err != nil {
		t.Fatalf("create reactor: %v", err)
	}
	ownerPeer := domain.Peer{Type: domain.PeerTypeUser, ID: owner.ID}
	storyStore := memory.NewStoryStore()
	story := domain.Story{
		Owner:      ownerPeer,
		ID:         14,
		Date:       1700000210,
		ExpireDate: 1700003810,
		Public:     true,
	}
	if _, err := storyStore.UpsertStory(ctx, domain.UpsertStoryRequest{Story: story}); err != nil {
		t.Fatalf("upsert story: %v", err)
	}
	r := New(Config{}, Deps{
		Users:   appusers.NewService(userStore),
		Stories: appstories.NewService(storyStore),
	}, zaptest.NewLogger(t), fixedClock{now: time.Unix(1700000310, 0)})

	updates := r.BuildOutboxUpdates(ctx, []OutboxUpdateRequest{{
		TargetUserID: owner.ID,
		Event: domain.UpdateEvent{
			UserID:   owner.ID,
			Type:     domain.UpdateEventNewStoryReaction,
			Pts:      22,
			PtsCount: 1,
			Date:     1700000310,
			Peer:     domain.Peer{Type: domain.PeerTypeUser, ID: reactor.ID},
			Story:    story,
			MaxID:    story.ID,
			Reaction: &domain.MessageReaction{Type: domain.MessageReactionEmoji, Emoticon: "🔥"},
		},
	}})
	if len(updates) != 1 || updates[0] == nil {
		t.Fatalf("updates = %+v, want one new story reaction update", updates)
	}
	if len(updates[0].Updates) != 2 {
		t.Fatalf("update count = %d, want updateNewStoryReaction plus pts bookkeeping", len(updates[0].Updates))
	}
	got, ok := updates[0].Updates[0].(*tg.UpdateNewStoryReaction)
	if !ok {
		t.Fatalf("first update = %T, want updateNewStoryReaction", updates[0].Updates[0])
	}
	peer, ok := got.Peer.(*tg.PeerUser)
	if !ok || peer.UserID != reactor.ID {
		t.Fatalf("reaction peer = %T %+v, want reactor user peer", got.Peer, got.Peer)
	}
	if findUserClass(updates[0].Users, reactor.ID) == nil {
		t.Fatalf("users = %+v, want reactor companion user", updates[0].Users)
	}
	assertUserStoryMaxID(t, findUserClass(updates[0].Users, owner.ID), story.ID)
}

func TestDialogContactAndSearchPayloadsProjectStoriesMaxID(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, err := userStore.Create(ctx, domain.User{AccessHash: 96001, Phone: "15550096001", FirstName: "Owner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	viewer, err := userStore.Create(ctx, domain.User{AccessHash: 96002, Phone: "15550096002", FirstName: "Viewer"})
	if err != nil {
		t.Fatalf("create viewer: %v", err)
	}
	channelStore := memory.NewChannelStore()
	channelService := appchannels.NewService(channelStore)
	created, err := channelService.CreateChannel(ctx, owner.ID, domain.CreateChannelRequest{
		CreatorUserID: owner.ID,
		Title:         "story payload channel",
		Broadcast:     true,
		Date:          1700000000,
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	ownerPeer := domain.Peer{Type: domain.PeerTypeUser, ID: owner.ID}
	channelPeer := domain.Peer{Type: domain.PeerTypeChannel, ID: created.Channel.ID}
	storyStore := memory.NewStoryStore()
	if _, err := storyStore.UpsertStory(ctx, domain.UpsertStoryRequest{Story: domain.Story{
		Owner:      ownerPeer,
		ID:         17,
		Date:       1700000000,
		ExpireDate: 1700003600,
		Public:     true,
	}}); err != nil {
		t.Fatalf("upsert user story: %v", err)
	}
	if _, err := storyStore.UpsertStory(ctx, domain.UpsertStoryRequest{Story: domain.Story{
		Owner:      channelPeer,
		ID:         19,
		Date:       1700000010,
		ExpireDate: 1700003600,
		Public:     true,
	}}); err != nil {
		t.Fatalf("upsert channel story: %v", err)
	}
	r := New(Config{}, Deps{
		Users:    appusers.NewService(userStore),
		Channels: channelService,
		Stories:  appstories.NewService(storyStore),
	}, zaptest.NewLogger(t), fixedClock{now: time.Unix(1700000100, 0)})

	dialogs, ok := r.tgMessagesDialogs(ctx, viewer.ID, domain.DialogList{
		Dialogs: []domain.Dialog{{Peer: ownerPeer}, {Peer: channelPeer}},
		Users:   []domain.User{owner},
		Channels: []domain.Channel{
			created.Channel,
		},
	}).(*tg.MessagesDialogs)
	if !ok {
		t.Fatalf("dialogs type = %T, want *tg.MessagesDialogs", dialogs)
	}
	assertUserStoryMaxID(t, findUserClass(dialogs.Users, owner.ID), 17)
	assertChannelStoryMaxID(t, findChannelClass(dialogs.Chats, created.Channel.ID), 19)

	peerDialogs := r.tgPeerDialogs(ctx, viewer.ID, domain.DialogList{
		Dialogs: []domain.Dialog{{Peer: ownerPeer}, {Peer: channelPeer}},
		Users:   []domain.User{owner},
		Channels: []domain.Channel{
			created.Channel,
		},
	}, domain.UpdateState{Pts: 7, Date: 1700000100})
	assertUserStoryMaxID(t, findUserClass(peerDialogs.Users, owner.ID), 17)
	assertChannelStoryMaxID(t, findChannelClass(peerDialogs.Chats, created.Channel.ID), 19)

	contacts, ok := r.tgContacts(ctx, viewer.ID, domain.ContactList{
		Contacts: []domain.Contact{{User: owner}},
	}).(*tg.ContactsContacts)
	if !ok {
		t.Fatalf("contacts type = %T, want *tg.ContactsContacts", contacts)
	}
	assertUserStoryMaxID(t, findUserClass(contacts.Users, owner.ID), 17)

	found := r.tgContactsFound(ctx, viewer.ID, domain.UserSearchResult{
		Results:        []domain.User{owner},
		ChannelResults: []domain.Channel{created.Channel},
	})
	assertUserStoryMaxID(t, findUserClass(found.Users, owner.ID), 17)
	assertChannelStoryMaxID(t, findChannelClass(found.Chats, created.Channel.ID), 19)

	contactUpdate := r.contactPeerSettingsUpdates(ctx, viewer.ID, owner, domain.PeerSettings{}, false)
	assertUserStoryMaxID(t, findUserClass(contactUpdate.Users, owner.ID), 17)

	global, ok := r.tgGlobalSearchMessages(ctx, viewer.ID, 10, domain.MessageList{
		Users: []domain.User{owner},
	}, domain.ChannelHistory{
		Channels: []domain.Channel{created.Channel},
	}).(*tg.MessagesMessages)
	if !ok {
		t.Fatalf("global search type = %T, want *tg.MessagesMessages", global)
	}
	assertUserStoryMaxID(t, findUserClass(global.Users, owner.ID), 17)
	assertChannelStoryMaxID(t, findChannelClass(global.Chats, created.Channel.ID), 19)
}

func TestContactsResolveUsernameProjectsStoriesMaxID(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, err := userStore.Create(ctx, domain.User{AccessHash: 97001, Phone: "15550097001", FirstName: "Owner", Username: "story_owner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	viewer, err := userStore.Create(ctx, domain.User{AccessHash: 97002, Phone: "15550097002", FirstName: "Viewer"})
	if err != nil {
		t.Fatalf("create viewer: %v", err)
	}
	channelStore := memory.NewChannelStore()
	channelService := appchannels.NewService(channelStore)
	created, err := channelService.CreateChannel(ctx, owner.ID, domain.CreateChannelRequest{
		CreatorUserID: owner.ID,
		Title:         "story resolve channel",
		Broadcast:     true,
		Date:          1700000000,
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	public, err := channelService.UpdateUsername(ctx, owner.ID, domain.UpdateChannelUsernameRequest{
		ChannelID: created.Channel.ID,
		Username:  "story_resolve_channel",
	})
	if err != nil {
		t.Fatalf("set channel username: %v", err)
	}
	storyStore := memory.NewStoryStore()
	if _, err := storyStore.UpsertStory(ctx, domain.UpsertStoryRequest{Story: domain.Story{
		Owner:      domain.Peer{Type: domain.PeerTypeUser, ID: owner.ID},
		ID:         23,
		Date:       1700000000,
		ExpireDate: 1700003600,
		Public:     true,
	}}); err != nil {
		t.Fatalf("upsert user story: %v", err)
	}
	if _, err := storyStore.UpsertStory(ctx, domain.UpsertStoryRequest{Story: domain.Story{
		Owner:      domain.Peer{Type: domain.PeerTypeChannel, ID: public.ID},
		ID:         29,
		Date:       1700000010,
		ExpireDate: 1700003600,
		Public:     true,
	}}); err != nil {
		t.Fatalf("upsert channel story: %v", err)
	}
	r := New(Config{}, Deps{
		Users:    appusers.NewService(userStore),
		Channels: channelService,
		Stories:  appstories.NewService(storyStore),
	}, zaptest.NewLogger(t), fixedClock{now: time.Unix(1700000100, 0)})

	userResolved, err := r.onContactsResolveUsername(WithUserID(ctx, viewer.ID), &tg.ContactsResolveUsernameRequest{Username: "@story_owner"})
	if err != nil {
		t.Fatalf("resolve user: %v", err)
	}
	assertUserStoryMaxID(t, findUserClass(userResolved.Users, owner.ID), 23)

	channelResolved, err := r.onContactsResolveUsername(WithUserID(ctx, viewer.ID), &tg.ContactsResolveUsernameRequest{Username: "@story_resolve_channel"})
	if err != nil {
		t.Fatalf("resolve channel: %v", err)
	}
	assertChannelStoryMaxID(t, findChannelClass(channelResolved.Chats, public.ID), 29)
}

func dispatchMessagesPayload(t *testing.T, r *Router, ctx context.Context, req bin.Encoder) bin.Encoder {
	t.Helper()
	var in bin.Buffer
	if err := req.Encode(&in); err != nil {
		t.Fatalf("encode request: %v", err)
	}
	out, err := r.Dispatch(ctx, [8]byte{}, 0, &in)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	return out
}

func findNewChannelTextMessage(t *testing.T, updates tg.UpdatesClass, body string) *tg.Message {
	t.Helper()
	box, ok := updates.(*tg.Updates)
	if !ok {
		t.Fatalf("updates = %T, want *tg.Updates", updates)
	}
	for _, update := range box.Updates {
		newMsg, ok := update.(*tg.UpdateNewChannelMessage)
		if !ok {
			continue
		}
		msg, ok := newMsg.Message.(*tg.Message)
		if ok && msg.Message == body {
			return msg
		}
	}
	t.Fatalf("updates = %+v, want new channel message %q", box.Updates, body)
	return nil
}

func findUserClass(users []tg.UserClass, id int64) tg.UserClass {
	for _, item := range users {
		if user, ok := item.(*tg.User); ok && user.ID == id {
			return item
		}
	}
	return nil
}

func findChannelClass(chats []tg.ChatClass, id int64) tg.ChatClass {
	for _, item := range chats {
		if channel, ok := item.(*tg.Channel); ok && channel.ID == id {
			return item
		}
	}
	return nil
}

func assertUserStoryMaxID(t *testing.T, item tg.UserClass, want int) {
	t.Helper()
	user, ok := item.(*tg.User)
	if !ok {
		t.Fatalf("user = %T, want *tg.User", item)
	}
	recent, ok := user.GetStoriesMaxID()
	if !ok {
		t.Fatalf("user stories_max_id missing")
	}
	assertRecentStoryMaxID(t, recent, want)
}

func assertChannelStoryMaxID(t *testing.T, item tg.ChatClass, want int) {
	t.Helper()
	channel, ok := item.(*tg.Channel)
	if !ok {
		t.Fatalf("channel = %T, want *tg.Channel", item)
	}
	recent, ok := channel.GetStoriesMaxID()
	if !ok {
		t.Fatalf("channel stories_max_id missing")
	}
	assertRecentStoryMaxID(t, recent, want)
}

func assertRecentStoryMaxID(t *testing.T, recent tg.RecentStory, want int) {
	t.Helper()
	got, ok := recent.GetMaxID()
	if !ok || got != want {
		t.Fatalf("recent story max_id = %d/%v, want %d", got, ok, want)
	}
}
