package channels

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"telesrv/internal/app/readmodel"
	"telesrv/internal/domain"
	"telesrv/internal/store"
	"telesrv/internal/store/memory"
)

type testBotProfiles map[int64]domain.BotProfile

func TestServiceSendMessageHonorsSendPermissionGate(t *testing.T) {
	ctx := context.Background()
	svc := NewService(memory.NewChannelStore(), WithSendPermissionChecker(channelDenySendChecker{}))
	if _, err := svc.SendMessage(ctx, 1001, domain.SendChannelMessageRequest{
		UserID:    1001,
		ChannelID: 2001,
		RandomID:  1,
		Message:   "blocked",
	}); !errors.Is(err, domain.ErrUserSendRestricted) {
		t.Fatalf("SendMessage err=%v, want ErrUserSendRestricted", err)
	}
}

func TestServiceChannelReplayPrecedesCurrentSendPermissionGate(t *testing.T) {
	ctx := context.Background()
	channels := memory.NewChannelStore()
	created, err := channels.CreateChannel(ctx, domain.CreateChannelRequest{
		CreatorUserID: 1001,
		Title:         "replay gate",
		Megagroup:     true,
		Date:          1_700_000_000,
	})
	if err != nil {
		t.Fatalf("CreateChannel: %v", err)
	}
	req := domain.SendChannelMessageRequest{
		ChannelID: created.Channel.ID,
		RandomID:  92,
		Message:   "committed before restriction",
		Date:      1_700_000_001,
	}
	allowed := NewService(channels)
	first, err := allowed.SendMessage(ctx, 1001, req)
	if err != nil {
		t.Fatalf("first SendMessage: %v", err)
	}

	denied := NewService(channels, WithSendPermissionChecker(channelDenySendChecker{}))
	req.Date++
	replay, err := denied.SendMessage(ctx, 1001, req)
	if err != nil {
		t.Fatalf("replay through denied gate: %v", err)
	}
	if !replay.Duplicate || replay.Message.ID != first.Message.ID {
		t.Fatalf("replay = %+v, want committed duplicate %d", replay, first.Message.ID)
	}

	req.Message = "different intent"
	if _, err := denied.SendMessage(ctx, 1001, req); !errors.Is(err, domain.ErrMessageRandomIDDuplicate) {
		t.Fatalf("conflicting replay err=%v, want ErrMessageRandomIDDuplicate before send gate", err)
	}
}

func TestServiceSendMonoforumMessageHonorsSendPermissionGate(t *testing.T) {
	ctx := context.Background()
	svc := NewService(memory.NewChannelStore(), WithSendPermissionChecker(channelDenySendChecker{}))
	if _, err := svc.SendMonoforumMessage(ctx, domain.SendMonoforumMessageRequest{
		MonoforumID:  2001,
		SenderUserID: 1001,
		SavedPeer:    domain.Peer{Type: domain.PeerTypeUser, ID: 1002},
		RandomID:     1,
		Message:      "blocked",
	}); !errors.Is(err, domain.ErrUserSendRestricted) {
		t.Fatalf("SendMonoforumMessage err=%v, want ErrUserSendRestricted", err)
	}
}

func TestServiceMonoforumReplayPrecedesCurrentSendPermissionGate(t *testing.T) {
	ctx := context.Background()
	channels := memory.NewChannelStore()
	parent, err := channels.CreateChannel(ctx, domain.CreateChannelRequest{
		CreatorUserID: 1001,
		Title:         "direct messages",
		Broadcast:     true,
		Date:          1_700_000_010,
	})
	if err != nil {
		t.Fatalf("CreateChannel: %v", err)
	}
	enabled, err := channels.SetPaidMessagesPrice(ctx, 1001, parent.Channel.ID, 0, true)
	if err != nil {
		t.Fatalf("SetPaidMessagesPrice: %v", err)
	}
	req := domain.SendMonoforumMessageRequest{
		MonoforumID:  enabled.Channel.LinkedMonoforumID,
		SenderUserID: 1002,
		SavedPeer:    domain.Peer{Type: domain.PeerTypeUser, ID: 1002},
		RandomID:     93,
		Message:      "committed direct message",
		Date:         1_700_000_011,
	}
	allowed := NewService(channels)
	first, err := allowed.SendMonoforumMessage(ctx, req)
	if err != nil {
		t.Fatalf("first SendMonoforumMessage: %v", err)
	}

	denied := NewService(channels, WithSendPermissionChecker(channelDenySendChecker{}))
	req.Date++
	replay, err := denied.SendMonoforumMessage(ctx, req)
	if err != nil {
		t.Fatalf("monoforum replay through denied gate: %v", err)
	}
	if !replay.Duplicate || replay.Message.ID != first.Message.ID {
		t.Fatalf("monoforum replay = %+v, want committed duplicate %d", replay, first.Message.ID)
	}

	req.Message = "different intent"
	if _, err := denied.SendMonoforumMessage(ctx, req); !errors.Is(err, domain.ErrMessageRandomIDDuplicate) {
		t.Fatalf("conflicting monoforum replay err=%v, want ErrMessageRandomIDDuplicate before send gate", err)
	}
}

type channelDenySendChecker struct{}

func (channelDenySendChecker) CanSendMessages(context.Context, int64) error {
	return domain.ErrUserSendRestricted
}

func (p testBotProfiles) BotInfo(_ context.Context, botUserID int64) (domain.BotProfile, bool, error) {
	profile, ok := p[botUserID]
	return profile, ok, nil
}

type countingChannelStore struct {
	*memory.ChannelStore
	mu                       sync.Mutex
	getChannelCalls          int
	resolveChannelCalls      int
	countMediaCalls          int
	getParticipantCalls      int
	listActiveIDsCalls       int
	listActiveMemberIDsCalls int
	resolveStarted           chan struct{}
	resolveRelease           <-chan struct{}
	resolveStartOnce         sync.Once
}

func (s *countingChannelStore) GetChannel(ctx context.Context, viewerUserID, channelID int64) (domain.ChannelView, error) {
	s.getChannelCalls++
	return s.ChannelStore.GetChannel(ctx, viewerUserID, channelID)
}

func (s *countingChannelStore) ResolveChannel(ctx context.Context, viewerUserID, channelID int64) (domain.ChannelView, error) {
	s.mu.Lock()
	s.resolveChannelCalls++
	if s.resolveStarted != nil {
		s.resolveStartOnce.Do(func() { close(s.resolveStarted) })
	}
	release := s.resolveRelease
	s.mu.Unlock()
	if release != nil {
		<-release
	}
	return s.ChannelStore.ResolveChannel(ctx, viewerUserID, channelID)
}

func (s *countingChannelStore) CountChannelMediaCategories(ctx context.Context, viewerUserID, channelID int64) (domain.MediaCategoryCounts, error) {
	s.countMediaCalls++
	return s.ChannelStore.CountChannelMediaCategories(ctx, viewerUserID, channelID)
}

func (s *countingChannelStore) GetParticipants(ctx context.Context, viewerUserID, channelID int64, filter domain.ChannelParticipantsFilter, offset, limit int) (domain.ChannelParticipantList, error) {
	s.getParticipantCalls++
	return s.ChannelStore.GetParticipants(ctx, viewerUserID, channelID, filter, offset, limit)
}

func (s *countingChannelStore) ListActiveChannelIDsForUser(ctx context.Context, userID, afterChannelID int64, limit int) ([]int64, error) {
	s.listActiveIDsCalls++
	return s.ChannelStore.ListActiveChannelIDsForUser(ctx, userID, afterChannelID, limit)
}

func (s *countingChannelStore) ListActiveChannelMemberIDs(ctx context.Context, viewerUserID, channelID int64, limit int) ([]int64, error) {
	s.listActiveMemberIDsCalls++
	return s.ChannelStore.ListActiveChannelMemberIDs(ctx, viewerUserID, channelID, limit)
}

type fakeReadModelVersions struct {
	hashes map[store.ReadModelKey]int64
}

func (f *fakeReadModelVersions) ReadModelHash(_ context.Context, model string, ownerUserID int64, peerType domain.PeerType, peerID int64) (int64, bool, error) {
	hash := f.hashes[store.ReadModelKey{Model: model, OwnerUserID: ownerUserID, PeerType: peerType, PeerID: peerID}]
	return hash, hash != 0, nil
}

func (f *fakeReadModelVersions) ReadModelHashes(_ context.Context, keys []store.ReadModelKey) (map[store.ReadModelKey]int64, error) {
	out := make(map[store.ReadModelKey]int64, len(keys))
	for _, key := range keys {
		if hash := f.hashes[key]; hash != 0 {
			out[key] = hash
		}
	}
	return out, nil
}

func TestChannelFullViewReadModelCacheDefaultTTLIsLongLived(t *testing.T) {
	if defaultChannelViewReadModelTTL != 24*time.Hour {
		t.Fatalf("default full channel read model TTL = %v, want 24h", defaultChannelViewReadModelTTL)
	}
	if cache := newChannelViewReadModelCache(0); cache == nil {
		t.Fatal("newChannelViewReadModelCache(0) 应返回非 nil 缓存")
	}
}

func TestGetChannelCachesFullViewByCompositeReadModelHash(t *testing.T) {
	ctx := context.Background()
	const ownerID int64 = 1001
	base := &countingChannelStore{ChannelStore: memory.NewChannelStore()}
	service := NewService(base)
	created, err := service.CreateChannel(ctx, ownerID, domain.CreateChannelRequest{
		Title:     "Cached Full",
		Megagroup: true,
		Date:      1700004100,
	})
	if err != nil {
		t.Fatalf("CreateChannel: %v", err)
	}
	peer := domain.Peer{Type: domain.PeerTypeChannel, ID: created.Channel.ID}
	versions := &fakeReadModelVersions{hashes: map[store.ReadModelKey]int64{
		{Model: readmodel.ModelChannelBase, OwnerUserID: 0, PeerType: peer.Type, PeerID: peer.ID}:         11,
		{Model: readmodel.ModelChannelMember, OwnerUserID: ownerID, PeerType: peer.Type, PeerID: peer.ID}: 22,
		{Model: readmodel.ModelDialogLight, OwnerUserID: ownerID, PeerType: peer.Type, PeerID: peer.ID}:   33,
	}}
	service = NewService(base, WithReadModelVersions(versions))

	first, err := service.GetChannelReadModel(ctx, ownerID, created.Channel.ID)
	if err != nil {
		t.Fatalf("first GetChannel: %v", err)
	}
	if first.Channel.ID != created.Channel.ID || first.Self.UserID != ownerID {
		t.Fatalf("first channel view = %+v, want owner view", first)
	}
	first.Channel.PhotoStripped = []byte{1, 2, 3}
	first.Dialog.DefaultSendAs = &peer
	second, err := service.GetChannelReadModel(ctx, ownerID, created.Channel.ID)
	if err != nil {
		t.Fatalf("second GetChannel: %v", err)
	}
	if base.getChannelCalls != 1 {
		t.Fatalf("GetChannel calls = %d, want 1 after cache hit", base.getChannelCalls)
	}
	if len(second.Channel.PhotoStripped) != 0 || second.Dialog.DefaultSendAs != nil {
		t.Fatalf("cached channel view was mutated by caller: %+v", second)
	}

	versions.hashes[store.ReadModelKey{Model: readmodel.ModelChannelMember, OwnerUserID: ownerID, PeerType: peer.Type, PeerID: peer.ID}] = 44
	if _, err := service.GetChannelReadModel(ctx, ownerID, created.Channel.ID); err != nil {
		t.Fatalf("GetChannel after hash bump: %v", err)
	}
	if base.getChannelCalls != 2 {
		t.Fatalf("GetChannel calls after hash bump = %d, want 2", base.getChannelCalls)
	}
}

func TestResolveChannelCachesAccessViewByCompositeReadModelHash(t *testing.T) {
	ctx := context.Background()
	const ownerID int64 = 1001
	base := &countingChannelStore{ChannelStore: memory.NewChannelStore()}
	service := NewService(base)
	created, err := service.CreateChannel(ctx, ownerID, domain.CreateChannelRequest{
		Title:     "Cached Resolve",
		Megagroup: true,
		Date:      1700004103,
	})
	if err != nil {
		t.Fatalf("CreateChannel: %v", err)
	}
	peer := domain.Peer{Type: domain.PeerTypeChannel, ID: created.Channel.ID}
	memberKey := store.ReadModelKey{Model: readmodel.ModelChannelMember, OwnerUserID: ownerID, PeerType: peer.Type, PeerID: peer.ID}
	baseKey := store.ReadModelKey{Model: readmodel.ModelChannelBase, OwnerUserID: 0, PeerType: peer.Type, PeerID: peer.ID}
	versions := &fakeReadModelVersions{hashes: map[store.ReadModelKey]int64{
		baseKey:   51,
		memberKey: 52,
	}}
	service = NewService(base, WithReadModelVersions(versions))

	first, err := service.ResolveChannel(ctx, ownerID, created.Channel.ID)
	if err != nil {
		t.Fatalf("first ResolveChannel: %v", err)
	}
	if first.Channel.ID != created.Channel.ID || first.Self.UserID != ownerID {
		t.Fatalf("first resolve view = %+v, want owner view", first)
	}
	first.Channel.PhotoStripped = []byte{1, 2, 3}
	first.Dialog.DefaultSendAs = &peer
	second, err := service.ResolveChannel(ctx, ownerID, created.Channel.ID)
	if err != nil {
		t.Fatalf("second ResolveChannel: %v", err)
	}
	if base.resolveChannelCalls != 1 {
		t.Fatalf("ResolveChannel calls = %d, want 1 after cache hit", base.resolveChannelCalls)
	}
	if len(second.Channel.PhotoStripped) != 0 || second.Dialog.DefaultSendAs != nil {
		t.Fatalf("cached resolve view was mutated by caller: %+v", second)
	}

	versions.hashes[memberKey] = 53
	if _, err := service.ResolveChannel(ctx, ownerID, created.Channel.ID); err != nil {
		t.Fatalf("ResolveChannel after member hash bump: %v", err)
	}
	if base.resolveChannelCalls != 2 {
		t.Fatalf("ResolveChannel calls after member hash bump = %d, want 2", base.resolveChannelCalls)
	}

	versions.hashes[baseKey] = 54
	if _, err := service.ResolveChannel(ctx, ownerID, created.Channel.ID); err != nil {
		t.Fatalf("ResolveChannel after base hash bump: %v", err)
	}
	if base.resolveChannelCalls != 3 {
		t.Fatalf("ResolveChannel calls after base hash bump = %d, want 3", base.resolveChannelCalls)
	}
}

func TestActiveChannelIDsForUserCachesPageByReadModelHash(t *testing.T) {
	ctx := context.Background()
	const ownerID int64 = 1001
	base := &countingChannelStore{ChannelStore: memory.NewChannelStore()}
	service := NewService(base)
	firstChannel, err := service.CreateChannel(ctx, ownerID, domain.CreateChannelRequest{
		Title:     "Active One",
		Megagroup: true,
		Date:      1700004110,
	})
	if err != nil {
		t.Fatalf("CreateChannel first: %v", err)
	}
	secondChannel, err := service.CreateChannel(ctx, ownerID, domain.CreateChannelRequest{
		Title:     "Active Two",
		Megagroup: true,
		Date:      1700004111,
	})
	if err != nil {
		t.Fatalf("CreateChannel second: %v", err)
	}
	key := store.ReadModelKey{Model: readmodel.ModelChannelActiveIDs, OwnerUserID: ownerID, PeerType: domain.PeerTypeUser, PeerID: ownerID}
	versions := &fakeReadModelVersions{hashes: map[store.ReadModelKey]int64{key: 201}}
	service = NewService(base, WithReadModelVersions(versions))

	want := []int64{firstChannel.Channel.ID, secondChannel.Channel.ID}
	first, err := service.ActiveChannelIDsForUser(ctx, ownerID, 0, domain.MaxSynchronousChannelDialogFanout)
	if err != nil {
		t.Fatalf("first ActiveChannelIDsForUser: %v", err)
	}
	if !slices.Equal(first, want) {
		t.Fatalf("first active ids = %v, want %v", first, want)
	}
	first[0] = 999
	second, err := service.ActiveChannelIDsForUser(ctx, ownerID, 0, domain.MaxSynchronousChannelDialogFanout)
	if err != nil {
		t.Fatalf("second ActiveChannelIDsForUser: %v", err)
	}
	if base.listActiveIDsCalls != 1 {
		t.Fatalf("ListActiveChannelIDsForUser calls = %d, want 1 after cache hit", base.listActiveIDsCalls)
	}
	if !slices.Equal(second, want) {
		t.Fatalf("cached active ids were mutated: got %v, want %v", second, want)
	}

	versions.hashes[key] = 202
	if _, err := service.ActiveChannelIDsForUser(ctx, ownerID, 0, domain.MaxSynchronousChannelDialogFanout); err != nil {
		t.Fatalf("active ids after hash bump: %v", err)
	}
	if base.listActiveIDsCalls != 2 {
		t.Fatalf("ListActiveChannelIDsForUser calls after hash bump = %d, want 2", base.listActiveIDsCalls)
	}
}

func TestActiveChannelIDsForUserCachesEmptyMissingReadModelHash(t *testing.T) {
	ctx := context.Background()
	const ownerID int64 = 1001
	base := &countingChannelStore{ChannelStore: memory.NewChannelStore()}
	service := NewService(base, WithReadModelVersions(&fakeReadModelVersions{}))

	first, err := service.ActiveChannelIDsForUser(ctx, ownerID, 0, domain.MaxSynchronousChannelDialogFanout)
	if err != nil {
		t.Fatalf("first ActiveChannelIDsForUser: %v", err)
	}
	if len(first) != 0 {
		t.Fatalf("first active ids = %v, want empty", first)
	}
	second, err := service.ActiveChannelIDsForUser(ctx, ownerID, 0, domain.MaxSynchronousChannelDialogFanout)
	if err != nil {
		t.Fatalf("second ActiveChannelIDsForUser: %v", err)
	}
	if len(second) != 0 {
		t.Fatalf("second active ids = %v, want empty", second)
	}
	if base.listActiveIDsCalls != 1 {
		t.Fatalf("ListActiveChannelIDsForUser calls = %d, want 1 after empty cache hit", base.listActiveIDsCalls)
	}

	created, err := service.CreateChannel(ctx, ownerID, domain.CreateChannelRequest{
		Title:     "Now Active",
		Megagroup: true,
		Date:      1700004114,
	})
	if err != nil {
		t.Fatalf("CreateChannel: %v", err)
	}
	afterWrite, err := service.ActiveChannelIDsForUser(ctx, ownerID, 0, domain.MaxSynchronousChannelDialogFanout)
	if err != nil {
		t.Fatalf("active ids after write: %v", err)
	}
	if want := []int64{created.Channel.ID}; !slices.Equal(afterWrite, want) {
		t.Fatalf("active ids after write = %v, want %v", afterWrite, want)
	}
	if base.listActiveIDsCalls != 2 {
		t.Fatalf("ListActiveChannelIDsForUser calls after write = %d, want 2", base.listActiveIDsCalls)
	}
	if _, err := service.ActiveChannelIDsForUser(ctx, ownerID, 0, domain.MaxSynchronousChannelDialogFanout); err != nil {
		t.Fatalf("active ids cached after write: %v", err)
	}
	if base.listActiveIDsCalls != 2 {
		t.Fatalf("ListActiveChannelIDsForUser calls after second post-write read = %d, want 2", base.listActiveIDsCalls)
	}
}

func TestActiveBotMemberIDsCachesAndInvalidatesOnMembershipWrite(t *testing.T) {
	ctx := context.Background()
	base := &countingChannelStore{ChannelStore: memory.NewChannelStore()}
	bots := testBotProfiles{
		1003: {BotUserID: 1003},
		1004: {BotUserID: 1004},
	}
	service := NewService(base, WithBotProfileResolver(bots))
	created, err := service.CreateMegagroupFromCreateChat(ctx, 1001, domain.CreateChannelRequest{
		Title:         "Bot Cache",
		MemberUserIDs: []int64{1002, 1003},
		Date:          1700004115,
	})
	if err != nil {
		t.Fatalf("CreateMegagroupFromCreateChat: %v", err)
	}
	channelID := created.Channel.ID

	first, err := service.ActiveBotMemberIDs(ctx, 1001, channelID, domain.MaxSynchronousChannelDialogFanout)
	if err != nil {
		t.Fatalf("ActiveBotMemberIDs first: %v", err)
	}
	if want := []int64{1003}; !slices.Equal(first, want) {
		t.Fatalf("ActiveBotMemberIDs first = %v, want %v", first, want)
	}
	first[0] = 9999
	second, err := service.ActiveBotMemberIDs(ctx, 1001, channelID, domain.MaxSynchronousChannelDialogFanout)
	if err != nil {
		t.Fatalf("ActiveBotMemberIDs second: %v", err)
	}
	if want := []int64{1003}; !slices.Equal(second, want) {
		t.Fatalf("ActiveBotMemberIDs cached = %v, want %v", second, want)
	}
	if base.listActiveMemberIDsCalls != 1 {
		t.Fatalf("ListActiveChannelMemberIDs calls = %d, want 1 after cache hit", base.listActiveMemberIDsCalls)
	}

	if _, err := service.InviteToChannel(ctx, 1001, channelID, []int64{1004}, 1700004116); err != nil {
		t.Fatalf("InviteToChannel bot: %v", err)
	}
	third, err := service.ActiveBotMemberIDs(ctx, 1001, channelID, domain.MaxSynchronousChannelDialogFanout)
	if err != nil {
		t.Fatalf("ActiveBotMemberIDs after invite: %v", err)
	}
	if want := []int64{1003, 1004}; !slices.Equal(third, want) {
		t.Fatalf("ActiveBotMemberIDs after invite = %v, want %v", third, want)
	}
	if base.listActiveMemberIDsCalls != 2 {
		t.Fatalf("ListActiveChannelMemberIDs calls = %d, want reload after invalidation", base.listActiveMemberIDsCalls)
	}
}

func TestActiveBotMemberIDsReloadsOnReadModelHashChange(t *testing.T) {
	ctx := context.Background()
	base := &countingChannelStore{ChannelStore: memory.NewChannelStore()}
	bots := testBotProfiles{1003: {BotUserID: 1003}}
	creator := NewService(base, WithBotProfileResolver(bots))
	created, err := creator.CreateMegagroupFromCreateChat(ctx, 1001, domain.CreateChannelRequest{
		Title:         "Bot Hash",
		MemberUserIDs: []int64{1002, 1003},
		Date:          1700004117,
	})
	if err != nil {
		t.Fatalf("CreateMegagroupFromCreateChat: %v", err)
	}
	channelID := created.Channel.ID
	peer := domain.Peer{Type: domain.PeerTypeChannel, ID: channelID}
	baseKey := store.ReadModelKey{Model: readmodel.ModelChannelBase, OwnerUserID: 0, PeerType: peer.Type, PeerID: peer.ID}
	participantsKey := store.ReadModelKey{Model: readmodel.ModelChannelParticipants, OwnerUserID: 0, PeerType: peer.Type, PeerID: peer.ID}
	memberKey := store.ReadModelKey{Model: readmodel.ModelChannelMember, OwnerUserID: 1001, PeerType: peer.Type, PeerID: peer.ID}
	versions := &fakeReadModelVersions{hashes: map[store.ReadModelKey]int64{
		baseKey:         401,
		participantsKey: 402,
		memberKey:       403,
	}}
	service := NewService(base, WithBotProfileResolver(bots), WithReadModelVersions(versions))

	if _, err := service.ActiveBotMemberIDs(ctx, 1001, channelID, domain.MaxSynchronousChannelDialogFanout); err != nil {
		t.Fatalf("ActiveBotMemberIDs first: %v", err)
	}
	if _, err := service.ActiveBotMemberIDs(ctx, 1001, channelID, domain.MaxSynchronousChannelDialogFanout); err != nil {
		t.Fatalf("ActiveBotMemberIDs cached: %v", err)
	}
	if base.listActiveMemberIDsCalls != 1 {
		t.Fatalf("ListActiveChannelMemberIDs calls = %d, want 1 before hash change", base.listActiveMemberIDsCalls)
	}

	versions.hashes[participantsKey] = 404
	if _, err := service.ActiveBotMemberIDs(ctx, 1001, channelID, domain.MaxSynchronousChannelDialogFanout); err != nil {
		t.Fatalf("ActiveBotMemberIDs after hash change: %v", err)
	}
	if base.listActiveMemberIDsCalls != 2 {
		t.Fatalf("ListActiveChannelMemberIDs calls = %d, want reload after hash change", base.listActiveMemberIDsCalls)
	}
}

func TestActiveChannelIDsCacheInvalidatesOnMembershipWrite(t *testing.T) {
	ctx := context.Background()
	const ownerID int64 = 1001
	base := &countingChannelStore{ChannelStore: memory.NewChannelStore()}
	key := store.ReadModelKey{Model: readmodel.ModelChannelActiveIDs, OwnerUserID: ownerID, PeerType: domain.PeerTypeUser, PeerID: ownerID}
	versions := &fakeReadModelVersions{hashes: map[store.ReadModelKey]int64{key: 301}}
	service := NewService(base, WithReadModelVersions(versions))
	firstChannel, err := service.CreateChannel(ctx, ownerID, domain.CreateChannelRequest{
		Title:     "Write One",
		Megagroup: true,
		Date:      1700004112,
	})
	if err != nil {
		t.Fatalf("CreateChannel first: %v", err)
	}
	if _, err := service.ActiveChannelIDsForUser(ctx, ownerID, 0, domain.MaxSynchronousChannelDialogFanout); err != nil {
		t.Fatalf("warm active ids: %v", err)
	}
	if base.listActiveIDsCalls != 1 {
		t.Fatalf("ListActiveChannelIDsForUser calls after warm = %d, want 1", base.listActiveIDsCalls)
	}

	secondChannel, err := service.CreateChannel(ctx, ownerID, domain.CreateChannelRequest{
		Title:     "Write Two",
		Megagroup: true,
		Date:      1700004113,
	})
	if err != nil {
		t.Fatalf("CreateChannel second: %v", err)
	}
	got, err := service.ActiveChannelIDsForUser(ctx, ownerID, 0, domain.MaxSynchronousChannelDialogFanout)
	if err != nil {
		t.Fatalf("active ids after create: %v", err)
	}
	want := []int64{firstChannel.Channel.ID, secondChannel.Channel.ID}
	if !slices.Equal(got, want) {
		t.Fatalf("active ids after create = %v, want %v", got, want)
	}
	if base.listActiveIDsCalls != 2 {
		t.Fatalf("ListActiveChannelIDsForUser calls after create invalidation = %d, want 2", base.listActiveIDsCalls)
	}
}

func TestResolveChannelSingleflightsConcurrentMiss(t *testing.T) {
	ctx := context.Background()
	const ownerID int64 = 1001
	started := make(chan struct{})
	release := make(chan struct{})
	base := &countingChannelStore{
		ChannelStore:   memory.NewChannelStore(),
		resolveStarted: started,
		resolveRelease: release,
	}
	service := NewService(base)
	created, err := service.CreateChannel(ctx, ownerID, domain.CreateChannelRequest{
		Title:     "Concurrent Resolve",
		Megagroup: true,
		Date:      1700004104,
	})
	if err != nil {
		t.Fatalf("CreateChannel: %v", err)
	}
	peer := domain.Peer{Type: domain.PeerTypeChannel, ID: created.Channel.ID}
	versions := &fakeReadModelVersions{hashes: map[store.ReadModelKey]int64{
		{Model: readmodel.ModelChannelBase, OwnerUserID: 0, PeerType: peer.Type, PeerID: peer.ID}:         61,
		{Model: readmodel.ModelChannelMember, OwnerUserID: ownerID, PeerType: peer.Type, PeerID: peer.ID}: 62,
	}}
	service = NewService(base, WithReadModelVersions(versions))

	const goroutines = 16
	var wg sync.WaitGroup
	errs := make(chan error, goroutines)
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			view, err := service.ResolveChannel(ctx, ownerID, created.Channel.ID)
			if err != nil {
				errs <- err
				return
			}
			if view.Channel.ID != created.Channel.ID || view.Self.UserID != ownerID {
				errs <- errors.New("unexpected resolve view")
			}
		}()
	}
	<-started
	time.Sleep(20 * time.Millisecond)
	close(release)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if base.resolveChannelCalls != 1 {
		t.Fatalf("ResolveChannel calls = %d, want 1", base.resolveChannelCalls)
	}
}

func TestCountChannelMediaCategoriesCachesByCompositeReadModelHash(t *testing.T) {
	ctx := context.Background()
	const ownerID int64 = 1001
	base := &countingChannelStore{ChannelStore: memory.NewChannelStore()}
	service := NewService(base)
	created, err := service.CreateChannel(ctx, ownerID, domain.CreateChannelRequest{
		Title:     "Media Counts",
		Megagroup: true,
		Date:      1700004101,
	})
	if err != nil {
		t.Fatalf("CreateChannel: %v", err)
	}
	peer := domain.Peer{Type: domain.PeerTypeChannel, ID: created.Channel.ID}
	versions := &fakeReadModelVersions{hashes: map[store.ReadModelKey]int64{
		{Model: readmodel.ModelChannelMediaCounts, OwnerUserID: 0, PeerType: peer.Type, PeerID: peer.ID}:  91,
		{Model: readmodel.ModelChannelMember, OwnerUserID: ownerID, PeerType: peer.Type, PeerID: peer.ID}: 92,
	}}
	service = NewService(base, WithReadModelVersions(versions))

	first, err := service.CountChannelMediaCategories(ctx, ownerID, created.Channel.ID)
	if err != nil {
		t.Fatalf("first count media: %v", err)
	}
	first[domain.MediaCategoryPhoto] = 99
	second, err := service.CountChannelMediaCategories(ctx, ownerID, created.Channel.ID)
	if err != nil {
		t.Fatalf("second count media: %v", err)
	}
	if base.countMediaCalls != 1 {
		t.Fatalf("CountChannelMediaCategories calls = %d, want 1 after cache hit", base.countMediaCalls)
	}
	if second[domain.MediaCategoryPhoto] != 0 {
		t.Fatalf("cached media counts were mutated by caller: %+v", second)
	}

	service.InvalidateChannelMediaCountReadModel(created.Channel.ID)
	if _, err := service.CountChannelMediaCategories(ctx, ownerID, created.Channel.ID); err != nil {
		t.Fatalf("count media after explicit invalidation: %v", err)
	}
	if base.countMediaCalls != 2 {
		t.Fatalf("CountChannelMediaCategories calls after explicit invalidation = %d, want 2", base.countMediaCalls)
	}

	versions.hashes[store.ReadModelKey{Model: readmodel.ModelChannelMember, OwnerUserID: ownerID, PeerType: peer.Type, PeerID: peer.ID}] = 93
	if _, err := service.CountChannelMediaCategories(ctx, ownerID, created.Channel.ID); err != nil {
		t.Fatalf("count media after hash bump: %v", err)
	}
	if base.countMediaCalls != 3 {
		t.Fatalf("CountChannelMediaCategories calls after hash bump = %d, want 3", base.countMediaCalls)
	}
}

func TestGetParticipantsCachesPageByCompositeReadModelHash(t *testing.T) {
	ctx := context.Background()
	const ownerID int64 = 1001
	base := &countingChannelStore{ChannelStore: memory.NewChannelStore()}
	service := NewService(base)
	created, err := service.CreateChannel(ctx, ownerID, domain.CreateChannelRequest{
		Title:         "Participants",
		Megagroup:     true,
		MemberUserIDs: []int64{1002, 1003},
		Date:          1700004102,
	})
	if err != nil {
		t.Fatalf("CreateChannel: %v", err)
	}
	peer := domain.Peer{Type: domain.PeerTypeChannel, ID: created.Channel.ID}
	versions := &fakeReadModelVersions{hashes: map[store.ReadModelKey]int64{
		{Model: readmodel.ModelChannelBase, OwnerUserID: 0, PeerType: peer.Type, PeerID: peer.ID}:                    101,
		{Model: readmodel.ModelChannelParticipants, OwnerUserID: 0, PeerType: peer.Type, PeerID: peer.ID}:            102,
		{Model: readmodel.ModelChannelMember, OwnerUserID: ownerID, PeerType: peer.Type, PeerID: peer.ID}:            103,
		{Model: readmodel.ModelContactAccount, OwnerUserID: ownerID, PeerType: domain.PeerTypeUser, PeerID: ownerID}: 104,
	}}
	service = NewService(base, WithReadModelVersions(versions))

	first, err := service.GetParticipants(ctx, ownerID, created.Channel.ID, domain.ChannelParticipantsFilter{Kind: domain.ChannelParticipantsRecent}, 0, 20)
	if err != nil {
		t.Fatalf("first participants: %v", err)
	}
	if first.Hash == 0 || len(first.Participants) != 3 {
		t.Fatalf("first participants = %+v, want hash and three members", first)
	}
	first.Channel.PhotoStripped = []byte{1, 2, 3}
	first.Participants[0].Rank = "mutated"
	second, err := service.GetParticipants(ctx, ownerID, created.Channel.ID, domain.ChannelParticipantsFilter{}, 0, 20)
	if err != nil {
		t.Fatalf("second participants: %v", err)
	}
	if base.getParticipantCalls != 1 {
		t.Fatalf("GetParticipants calls = %d, want 1 after cache hit", base.getParticipantCalls)
	}
	if second.Hash != first.Hash {
		t.Fatalf("second hash = %d, want %d", second.Hash, first.Hash)
	}
	if len(second.Channel.PhotoStripped) != 0 || second.Participants[0].Rank == "mutated" {
		t.Fatalf("cached participants were mutated by caller: %+v", second)
	}

	versions.hashes[store.ReadModelKey{Model: readmodel.ModelContactAccount, OwnerUserID: ownerID, PeerType: domain.PeerTypeUser, PeerID: ownerID}] = 105
	third, err := service.GetParticipants(ctx, ownerID, created.Channel.ID, domain.ChannelParticipantsFilter{Kind: domain.ChannelParticipantsRecent}, 0, 20)
	if err != nil {
		t.Fatalf("participants after contact hash bump: %v", err)
	}
	if base.getParticipantCalls != 2 {
		t.Fatalf("GetParticipants calls after contact hash bump = %d, want 2", base.getParticipantCalls)
	}
	if third.Hash == first.Hash {
		t.Fatalf("third hash = %d, want changed from %d", third.Hash, first.Hash)
	}

	versions.hashes[store.ReadModelKey{Model: readmodel.ModelChannelParticipants, OwnerUserID: 0, PeerType: peer.Type, PeerID: peer.ID}] = 106
	fourth, err := service.GetParticipants(ctx, ownerID, created.Channel.ID, domain.ChannelParticipantsFilter{Kind: domain.ChannelParticipantsRecent}, 0, 20)
	if err != nil {
		t.Fatalf("participants after participant hash bump: %v", err)
	}
	if base.getParticipantCalls != 3 {
		t.Fatalf("GetParticipants calls after participant hash bump = %d, want 3", base.getParticipantCalls)
	}
	if fourth.Hash == third.Hash {
		t.Fatalf("fourth hash = %d, want changed from %d", fourth.Hash, third.Hash)
	}
}

func TestGetParticipantsCacheInvalidatesAfterAdminMutation(t *testing.T) {
	ctx := context.Background()
	const ownerID int64 = 1001
	base := &countingChannelStore{ChannelStore: memory.NewChannelStore()}
	service := NewService(base)
	created, err := service.CreateChannel(ctx, ownerID, domain.CreateChannelRequest{
		Title:         "Admin Cache",
		Megagroup:     true,
		MemberUserIDs: []int64{1002},
		Date:          1700004103,
	})
	if err != nil {
		t.Fatalf("CreateChannel: %v", err)
	}
	peer := domain.Peer{Type: domain.PeerTypeChannel, ID: created.Channel.ID}
	versions := &fakeReadModelVersions{hashes: map[store.ReadModelKey]int64{
		{Model: readmodel.ModelChannelBase, OwnerUserID: 0, PeerType: peer.Type, PeerID: peer.ID}:                    201,
		{Model: readmodel.ModelChannelParticipants, OwnerUserID: 0, PeerType: peer.Type, PeerID: peer.ID}:            202,
		{Model: readmodel.ModelChannelMember, OwnerUserID: ownerID, PeerType: peer.Type, PeerID: peer.ID}:            203,
		{Model: readmodel.ModelContactAccount, OwnerUserID: ownerID, PeerType: domain.PeerTypeUser, PeerID: ownerID}: 204,
	}}
	service = NewService(base, WithReadModelVersions(versions))

	filter := domain.ChannelParticipantsFilter{Kind: domain.ChannelParticipantsAdmins}
	before, err := service.GetParticipants(ctx, ownerID, created.Channel.ID, filter, 0, 20)
	if err != nil {
		t.Fatalf("first admins: %v", err)
	}
	if len(before.Participants) != 1 || before.Participants[0].UserID != ownerID {
		t.Fatalf("first admins = %+v, want only creator", before.Participants)
	}
	if _, err := service.GetParticipants(ctx, ownerID, created.Channel.ID, filter, 0, 20); err != nil {
		t.Fatalf("cached admins: %v", err)
	}
	if base.getParticipantCalls != 1 {
		t.Fatalf("GetParticipants calls before mutation = %d, want 1", base.getParticipantCalls)
	}

	if _, err := service.EditAdmin(ctx, ownerID, domain.EditChannelAdminRequest{
		ChannelID:   created.Channel.ID,
		MemberID:    1002,
		AdminRights: domain.ChannelAdminRights{InviteUsers: true},
		Date:        1700004104,
	}); err != nil {
		t.Fatalf("EditAdmin: %v", err)
	}
	after, err := service.GetParticipants(ctx, ownerID, created.Channel.ID, filter, 0, 20)
	if err != nil {
		t.Fatalf("admins after mutation: %v", err)
	}
	if base.getParticipantCalls != 2 {
		t.Fatalf("GetParticipants calls after mutation = %d, want 2", base.getParticipantCalls)
	}
	if len(after.Participants) != 2 || after.Participants[1].UserID != 1002 || after.Participants[1].Role != domain.ChannelRoleAdmin {
		t.Fatalf("admins after mutation = %+v, want fresh promoted admin", after.Participants)
	}
}

func TestFullMegagroupAdminGrantFillsManageRanks(t *testing.T) {
	ctx := context.Background()
	service := NewService(memory.NewChannelStore())
	created, err := service.CreateChannel(ctx, 1001, domain.CreateChannelRequest{
		Title:         "Full Admin",
		Megagroup:     true,
		MemberUserIDs: []int64{1002},
		Date:          1700004200,
	})
	if err != nil {
		t.Fatalf("CreateChannel: %v", err)
	}
	rights := domain.ChannelAdminRights{
		ChangeInfo:     true,
		DeleteMessages: true,
		BanUsers:       true,
		InviteUsers:    true,
		PinMessages:    true,
		AddAdmins:      true,
		ManageCall:     true,
	}
	edited, err := service.EditAdmin(ctx, 1001, domain.EditChannelAdminRequest{
		ChannelID:   created.Channel.ID,
		MemberID:    1002,
		AdminRights: rights,
		Date:        1700004201,
	})
	if err != nil {
		t.Fatalf("EditAdmin full rights: %v", err)
	}
	if !edited.Participant.AdminRights.ManageRanks {
		t.Fatalf("edited admin rights = %+v, want ManageRanks for full megagroup admin", edited.Participant.AdminRights)
	}
	member, err := service.GetParticipant(ctx, 1001, created.Channel.ID, 1002)
	if err != nil {
		t.Fatalf("GetParticipant: %v", err)
	}
	if !member.AdminRights.ManageRanks {
		t.Fatalf("stored admin rights = %+v, want ManageRanks", member.AdminRights)
	}
}

func TestCreateChatCreatesMegagroupWithChannelPts(t *testing.T) {
	ctx := context.Background()
	store := memory.NewChannelStore()
	service := NewService(store)

	created, err := service.CreateMegagroupFromCreateChat(ctx, 1001, domain.CreateChannelRequest{
		Title:         "Team",
		MemberUserIDs: []int64{1002},
		Date:          10,
	})
	if err != nil {
		t.Fatalf("CreateMegagroupFromCreateChat: %v", err)
	}
	if !created.Channel.Megagroup || created.Channel.Broadcast {
		t.Fatalf("channel flags = megagroup:%v broadcast:%v, want megagroup only", created.Channel.Megagroup, created.Channel.Broadcast)
	}
	if created.Channel.Pts != 1 || created.Message.ID != 1 || created.Event.PtsCount != 1 {
		t.Fatalf("created pts/message/event = %+v/%+v/%+v, want initial pts=1 message id=1", created.Channel, created.Message, created.Event)
	}
	if created.Message.Action == nil || created.Message.Action.Type != domain.ChannelActionCreate {
		t.Fatalf("create service action = %+v, want channel create", created.Message.Action)
	}

	sent, err := service.SendMessage(ctx, 1001, domain.SendChannelMessageRequest{
		ChannelID: created.Channel.ID,
		RandomID:  99,
		Message:   "hello",
		ViaBotID:  1003,
		Date:      11,
	})
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if sent.Message.ID != 2 || sent.Message.Pts != 2 || sent.Event.Pts != 2 || sent.Event.PtsCount != 1 {
		t.Fatalf("sent = %+v event=%+v, want message id/pts=2", sent.Message, sent.Event)
	}
	if sent.Message.ViaBotID != 1003 || sent.Event.Message.ViaBotID != 1003 {
		t.Fatalf("sent via_bot_id = msg %d event %d, want 1003", sent.Message.ViaBotID, sent.Event.Message.ViaBotID)
	}

	duplicate, err := service.SendMessage(ctx, 1001, domain.SendChannelMessageRequest{
		ChannelID: created.Channel.ID,
		RandomID:  99,
		Message:   "hello",
		ViaBotID:  1003,
		Date:      12,
	})
	if err != nil {
		t.Fatalf("duplicate SendMessage: %v", err)
	}
	if !duplicate.Duplicate || duplicate.Message.ID != sent.Message.ID || duplicate.Message.Body != "hello" {
		t.Fatalf("duplicate = %+v, want original single-copy message", duplicate)
	}
	if duplicate.Message.ViaBotID != 1003 || duplicate.Event.Message.ViaBotID != 1003 {
		t.Fatalf("duplicate via_bot_id = msg %d event %d, want 1003", duplicate.Message.ViaBotID, duplicate.Event.Message.ViaBotID)
	}

	history, err := service.GetHistory(ctx, 1002, domain.ChannelHistoryFilter{ChannelID: created.Channel.ID, Limit: 10})
	if err != nil {
		t.Fatalf("GetHistory: %v", err)
	}
	if len(history.Messages) != 2 || history.Messages[0].ID != 2 || history.Messages[1].ID != 1 {
		t.Fatalf("history = %+v, want channel messages newest first", history.Messages)
	}
	if history.Messages[0].ViaBotID != 1003 {
		t.Fatalf("history via_bot_id = %d, want 1003", history.Messages[0].ViaBotID)
	}

	diff, err := service.GetDifference(ctx, 1002, domain.ChannelDifferenceRequest{ChannelID: created.Channel.ID, Pts: 1, Limit: 10})
	if err != nil {
		t.Fatalf("GetDifference: %v", err)
	}
	if !diff.Final || diff.Pts != 2 || len(diff.NewMessages) != 1 || diff.NewMessages[0].Body != "hello" {
		t.Fatalf("diff = %+v, want single new channel message at pts=2", diff)
	}
	if diff.NewMessages[0].ViaBotID != 1003 {
		t.Fatalf("diff via_bot_id = %d, want 1003", diff.NewMessages[0].ViaBotID)
	}
	if _, err := service.GetDifference(ctx, 1002, domain.ChannelDifferenceRequest{ChannelID: created.Channel.ID, Pts: sent.Event.Pts + 1, Limit: 10}); !errors.Is(err, domain.ErrPersistentTimestamp) {
		t.Fatalf("future pts diff err = %v, want persistent timestamp invalid", err)
	}

	read, err := service.ReadHistory(ctx, 1002, domain.ReadChannelHistoryRequest{ChannelID: created.Channel.ID, MaxID: 2})
	if err != nil {
		t.Fatalf("ReadHistory: %v", err)
	}
	if !read.Changed || read.StillUnreadCount != 0 || read.Dialog.ReadInboxMaxID != 2 {
		t.Fatalf("read = %+v, want read watermark at message 2", read)
	}
	if len(read.OutboxUpdates) != 1 || read.OutboxUpdates[0].UserID != 1001 || read.OutboxUpdates[0].MaxID != sent.Message.ID {
		t.Fatalf("read outbox updates = %+v, want owner read_outbox through sent message", read.OutboxUpdates)
	}
	ownerView, err := service.GetChannel(ctx, 1001, created.Channel.ID)
	if err != nil {
		t.Fatalf("GetChannel owner: %v", err)
	}
	if ownerView.Dialog.ReadOutboxMaxID != sent.Message.ID {
		t.Fatalf("owner dialog read_outbox = %d, want %d", ownerView.Dialog.ReadOutboxMaxID, sent.Message.ID)
	}
}

func TestSetChannelPhotoCreatesServiceMessagesAndDifference(t *testing.T) {
	ctx := context.Background()
	service := NewService(memory.NewChannelStore())

	created, err := service.CreateMegagroupFromCreateChat(ctx, 1001, domain.CreateChannelRequest{
		Title:         "Avatar Team",
		MemberUserIDs: []int64{1002},
		Date:          10,
	})
	if err != nil {
		t.Fatalf("CreateMegagroupFromCreateChat: %v", err)
	}
	photo := domain.Photo{
		ID:         9001,
		AccessHash: 9002,
		DCID:       2,
		Sizes: []domain.PhotoSize{
			{Kind: domain.PhotoSizeKindStripped, Type: "i", Bytes: []byte{1, 2, 3}},
			{Kind: domain.PhotoSizeKindDefault, Type: "m", W: 160, H: 160, Size: 4096},
		},
	}

	set, err := service.SetPhoto(ctx, 1001, created.Channel.ID, &photo, 11)
	if err != nil {
		t.Fatalf("SetPhoto: %v", err)
	}
	if set.Channel.PhotoID != photo.ID || set.Channel.PhotoDCID != photo.DCID || !slices.Equal(set.Channel.PhotoStripped, []byte{1, 2, 3}) {
		t.Fatalf("set channel photo fields = %+v, want photo id/dc/stripped", set.Channel)
	}
	if set.Channel.TopMessageID != set.Message.ID || set.Channel.Pts != set.Event.Pts {
		t.Fatalf("set top/pts = channel %+v message %+v event %+v, want service message as top", set.Channel, set.Message, set.Event)
	}
	if set.Event.Type != domain.ChannelUpdateNewMessage || set.Event.Pts != created.Event.Pts+1 || set.Event.PtsCount != 1 {
		t.Fatalf("set event = %+v, want durable new-message pts", set.Event)
	}
	if set.Message.Action == nil || set.Message.Action.Type != domain.ChannelActionChatEditPhoto || set.Message.Action.Photo == nil || set.Message.Action.Photo.ID != photo.ID {
		t.Fatalf("set action = %+v, want chat_edit_photo with photo snapshot", set.Message.Action)
	}
	if _, err := service.SetPhoto(ctx, 1001, created.Channel.ID, &photo, 12); !errors.Is(err, domain.ErrChannelNotModified) {
		t.Fatalf("duplicate SetPhoto err = %v, want ErrChannelNotModified", err)
	}
	diff, err := service.GetDifference(ctx, 1002, domain.ChannelDifferenceRequest{ChannelID: created.Channel.ID, Pts: created.Event.Pts, Limit: 10})
	if err != nil {
		t.Fatalf("GetDifference set photo: %v", err)
	}
	if !diff.Final || diff.Pts != set.Event.Pts || len(diff.NewMessages) != 1 {
		t.Fatalf("diff after set = %+v, want one service message through pts %d", diff, set.Event.Pts)
	}
	if action := diff.NewMessages[0].Action; action == nil || action.Type != domain.ChannelActionChatEditPhoto || action.Photo == nil || action.Photo.ID != photo.ID {
		t.Fatalf("diff set action = %+v, want chat_edit_photo", action)
	}

	cleared, err := service.SetPhoto(ctx, 1001, created.Channel.ID, nil, 13)
	if err != nil {
		t.Fatalf("ClearPhoto: %v", err)
	}
	if cleared.Channel.PhotoID != 0 || cleared.Channel.PhotoDCID != 0 || len(cleared.Channel.PhotoStripped) != 0 {
		t.Fatalf("cleared channel photo fields = %+v, want empty photo", cleared.Channel)
	}
	if cleared.Event.Pts != set.Event.Pts+1 || cleared.Message.Action == nil || cleared.Message.Action.Type != domain.ChannelActionChatDeletePhoto {
		t.Fatalf("cleared = event %+v action %+v, want chat_delete_photo next pts", cleared.Event, cleared.Message.Action)
	}
	clearDiff, err := service.GetDifference(ctx, 1002, domain.ChannelDifferenceRequest{ChannelID: created.Channel.ID, Pts: set.Event.Pts, Limit: 10})
	if err != nil {
		t.Fatalf("GetDifference clear photo: %v", err)
	}
	if !clearDiff.Final || clearDiff.Pts != cleared.Event.Pts || len(clearDiff.NewMessages) != 1 {
		t.Fatalf("diff after clear = %+v, want one delete-photo service message", clearDiff)
	}
	if action := clearDiff.NewMessages[0].Action; action == nil || action.Type != domain.ChannelActionChatDeletePhoto {
		t.Fatalf("diff clear action = %+v, want chat_delete_photo", action)
	}
	if _, err := service.SetPhoto(ctx, 1001, created.Channel.ID, nil, 14); !errors.Is(err, domain.ErrChannelNotModified) {
		t.Fatalf("duplicate ClearPhoto err = %v, want ErrChannelNotModified", err)
	}
}

func TestGroupBotPolicies(t *testing.T) {
	ctx := context.Background()
	store := memory.NewChannelStore()
	bots := testBotProfiles{
		1003: {BotUserID: 1003, ChatHistory: false, Nochats: false},
		1004: {BotUserID: 1004, ChatHistory: false, Nochats: true},
	}
	service := NewService(store, WithBotProfileResolver(bots))

	if _, err := service.CreateMegagroupFromCreateChat(ctx, 1001, domain.CreateChannelRequest{
		Title:         "Blocked",
		MemberUserIDs: []int64{1004},
		Date:          10,
	}); !errors.Is(err, domain.ErrBotGroupsBlocked) {
		t.Fatalf("create chat with nochats bot err = %v, want ErrBotGroupsBlocked", err)
	}

	created, err := service.CreateMegagroupFromCreateChat(ctx, 1001, domain.CreateChannelRequest{
		Title:         "Bots",
		MemberUserIDs: []int64{1002, 1003},
		Date:          20,
	})
	if err != nil {
		t.Fatalf("CreateMegagroupFromCreateChat: %v", err)
	}
	if _, err := service.InviteToChannel(ctx, 1001, created.Channel.ID, []int64{1004}, 21); !errors.Is(err, domain.ErrBotGroupsBlocked) {
		t.Fatalf("invite nochats bot err = %v, want ErrBotGroupsBlocked", err)
	}
	botParticipants, err := service.GetParticipants(ctx, 1001, created.Channel.ID, domain.ChannelParticipantsFilter{Kind: domain.ChannelParticipantsBots}, 0, 20)
	if err != nil {
		t.Fatalf("GetParticipants bots: %v", err)
	}
	if botParticipants.Count != 1 || len(botParticipants.Participants) != 1 || botParticipants.Participants[0].UserID != 1003 {
		t.Fatalf("bot participants = %+v, want bot 1003 only", botParticipants)
	}

	plain, err := service.SendMessage(ctx, 1001, domain.SendChannelMessageRequest{
		ChannelID: created.Channel.ID,
		RandomID:  2001,
		Message:   "plain group text",
		Date:      22,
	})
	if err != nil {
		t.Fatalf("SendMessage plain: %v", err)
	}
	if testContainsInt64(plain.Recipients, 1003) {
		t.Fatalf("plain recipients = %+v, privacy bot must be skipped", plain.Recipients)
	}
	hiddenDiff, err := service.GetDifference(ctx, 1003, domain.ChannelDifferenceRequest{ChannelID: created.Channel.ID, Pts: created.Event.Pts, Limit: 20})
	if err != nil {
		t.Fatalf("GetDifference hidden: %v", err)
	}
	if hiddenDiff.Pts != plain.Event.Pts || len(hiddenDiff.NewMessages) != 0 || len(hiddenDiff.Events) != 0 {
		t.Fatalf("hidden diff = %+v, want pts advanced without messages", hiddenDiff)
	}
	hiddenHistory, err := service.GetHistory(ctx, 1003, domain.ChannelHistoryFilter{ChannelID: created.Channel.ID, Limit: 20})
	if err != nil {
		t.Fatalf("GetHistory hidden: %v", err)
	}
	for _, msg := range hiddenHistory.Messages {
		if msg.Body == "plain group text" {
			t.Fatalf("bot history leaked hidden message: %+v", hiddenHistory.Messages)
		}
	}

	command, err := service.SendMessage(ctx, 1001, domain.SendChannelMessageRequest{
		ChannelID: created.Channel.ID,
		RandomID:  2002,
		Message:   "/status",
		Date:      23,
	})
	if err != nil {
		t.Fatalf("SendMessage command: %v", err)
	}
	if !testContainsInt64(command.Recipients, 1003) {
		t.Fatalf("command recipients = %+v, want privacy bot", command.Recipients)
	}
	visibleDiff, err := service.GetDifference(ctx, 1003, domain.ChannelDifferenceRequest{ChannelID: created.Channel.ID, Pts: plain.Event.Pts, Limit: 20})
	if err != nil {
		t.Fatalf("GetDifference command: %v", err)
	}
	if len(visibleDiff.NewMessages) != 1 || visibleDiff.NewMessages[0].Body != "/status" {
		t.Fatalf("visible diff = %+v, want command only", visibleDiff.NewMessages)
	}
	view, err := service.GetChannel(ctx, 1003, created.Channel.ID)
	if err != nil {
		t.Fatalf("GetChannel bot: %v", err)
	}
	if view.Dialog.UnreadCount != 1 || view.Dialog.ReadInboxMaxID != plain.Message.ID {
		t.Fatalf("bot dialog unread/read = %d/%d, want only command unread after hidden boundary %d", view.Dialog.UnreadCount, view.Dialog.ReadInboxMaxID, plain.Message.ID)
	}

	botMessage, err := service.SendMessage(ctx, 1003, domain.SendChannelMessageRequest{
		ChannelID: created.Channel.ID,
		RandomID:  3001,
		Message:   "bot said",
		Date:      24,
	})
	if err != nil {
		t.Fatalf("SendMessage bot: %v", err)
	}
	reply, err := service.SendMessage(ctx, 1001, domain.SendChannelMessageRequest{
		ChannelID: created.Channel.ID,
		RandomID:  2003,
		Message:   "reply to bot",
		ReplyTo:   &domain.MessageReply{MessageID: botMessage.Message.ID},
		Date:      25,
	})
	if err != nil {
		t.Fatalf("SendMessage reply: %v", err)
	}
	if !testContainsInt64(reply.Recipients, 1003) {
		t.Fatalf("reply recipients = %+v, want privacy bot", reply.Recipients)
	}
}

// TestPrivacyBotReceivesDeleteEventsInDifference 回归: privacy-mode bot 经
// getChannelDifference 补差必须收到删除事件。旧逻辑用 GetChannelMessages 重取已删消息
// 判可见性,而该查询带 AND NOT deleted 恒返空→整条 delete 事件被丢弃,导致 bot 对所有删除
// 失明、客户端缓存残留"未删"态。修复后删除事件直接放行(与在线推送一致,删除 id 不泄漏内容)。
func TestPrivacyBotReceivesDeleteEventsInDifference(t *testing.T) {
	ctx := context.Background()
	store := memory.NewChannelStore()
	bots := testBotProfiles{
		1003: {BotUserID: 1003, ChatHistory: false, Nochats: false},
	}
	service := NewService(store, WithBotProfileResolver(bots))

	created, err := service.CreateMegagroupFromCreateChat(ctx, 1001, domain.CreateChannelRequest{
		Title:         "Bots",
		MemberUserIDs: []int64{1002, 1003},
		Date:          20,
	})
	if err != nil {
		t.Fatalf("CreateMegagroupFromCreateChat: %v", err)
	}

	// 命令消息 privacy bot 可见(messageIsCommand)。
	command, err := service.SendMessage(ctx, 1001, domain.SendChannelMessageRequest{
		ChannelID: created.Channel.ID,
		RandomID:  2001,
		Message:   "/status",
		Date:      22,
	})
	if err != nil {
		t.Fatalf("SendMessage command: %v", err)
	}

	deleted, err := service.DeleteMessages(ctx, 1001, domain.DeleteChannelMessagesRequest{
		ChannelID: created.Channel.ID,
		IDs:       []int{command.Message.ID},
		Date:      23,
	})
	if err != nil {
		t.Fatalf("DeleteMessages: %v", err)
	}
	if deleted.Event.Type != domain.ChannelUpdateDeleteMessages {
		t.Fatalf("delete event type = %v, want delete", deleted.Event.Type)
	}

	diff, err := service.GetDifference(ctx, 1003, domain.ChannelDifferenceRequest{ChannelID: created.Channel.ID, Pts: command.Event.Pts, Limit: 20})
	if err != nil {
		t.Fatalf("GetDifference: %v", err)
	}
	foundDelete := false
	for _, ev := range diff.Events {
		if ev.Type != domain.ChannelUpdateDeleteMessages {
			continue
		}
		for _, id := range ev.MessageIDs {
			if id == command.Message.ID {
				foundDelete = true
			}
		}
	}
	if !foundDelete {
		t.Fatalf("privacy bot diff = %+v, want delete event for msg %d", diff, command.Message.ID)
	}
	if diff.Pts != deleted.Event.Pts {
		t.Fatalf("diff pts = %d, want advanced to %d", diff.Pts, deleted.Event.Pts)
	}
}

func testContainsInt64(ids []int64, target int64) bool {
	for _, id := range ids {
		if id == target {
			return true
		}
	}
	return false
}

func TestChannelUnreadMentionsArePagedAndCleared(t *testing.T) {
	ctx := context.Background()
	store := memory.NewChannelStore()
	service := NewService(store)
	created, err := service.CreateMegagroupFromCreateChat(ctx, 1001, domain.CreateChannelRequest{
		Title:         "Mentions",
		MemberUserIDs: []int64{1002, 1003},
		Date:          1700000100,
	})
	if err != nil {
		t.Fatalf("CreateMegagroupFromCreateChat: %v", err)
	}
	sent, err := service.SendMessage(ctx, 1001, domain.SendChannelMessageRequest{
		ChannelID:      created.Channel.ID,
		RandomID:       9101,
		Message:        "hello @friend",
		Media:          &domain.MessageMedia{Kind: domain.MessageMediaKindDocument},
		MentionUserIDs: []int64{1002, 1002, 1001},
		Date:           1700000101,
	})
	if err != nil {
		t.Fatalf("SendMessage mention: %v", err)
	}
	view, err := service.GetChannel(ctx, 1002, created.Channel.ID)
	if err != nil {
		t.Fatalf("GetChannel mentioned: %v", err)
	}
	if view.Dialog.UnreadMentions != 1 {
		t.Fatalf("mentioned dialog unread mentions = %d, want 1", view.Dialog.UnreadMentions)
	}
	other, err := service.GetChannel(ctx, 1003, created.Channel.ID)
	if err != nil {
		t.Fatalf("GetChannel other: %v", err)
	}
	if other.Dialog.UnreadMentions != 0 {
		t.Fatalf("unmentioned dialog unread mentions = %d, want 0", other.Dialog.UnreadMentions)
	}
	history, err := service.GetHistory(ctx, 1002, domain.ChannelHistoryFilter{ChannelID: created.Channel.ID, Limit: 10})
	if err != nil {
		t.Fatalf("GetHistory mentioned: %v", err)
	}
	if len(history.Messages) == 0 || !history.Messages[0].Mentioned || !history.Messages[0].MediaUnread {
		t.Fatalf("mentioned history = %+v, want mentioned/media_unread flags", history.Messages)
	}
	otherHistory, err := service.GetHistory(ctx, 1003, domain.ChannelHistoryFilter{ChannelID: created.Channel.ID, Limit: 10})
	if err != nil {
		t.Fatalf("GetHistory other: %v", err)
	}
	if len(otherHistory.Messages) == 0 || otherHistory.Messages[0].Mentioned || otherHistory.Messages[0].MediaUnread {
		t.Fatalf("other history = %+v, want no viewer-specific mention flags", otherHistory.Messages)
	}
	diff, err := service.GetDifference(ctx, 1002, domain.ChannelDifferenceRequest{
		ChannelID: created.Channel.ID,
		Pts:       sent.Event.Pts - 1,
		Limit:     10,
	})
	if err != nil {
		t.Fatalf("GetDifference mentioned: %v", err)
	}
	if len(diff.NewMessages) != 1 || !diff.NewMessages[0].Mentioned || !diff.NewMessages[0].MediaUnread {
		t.Fatalf("mentioned diff = %+v, want mentioned/media_unread flags", diff.NewMessages)
	}
	mentions, err := service.GetUnreadMentions(ctx, 1002, domain.ChannelUnreadMentionsFilter{
		ChannelID: created.Channel.ID,
		OffsetID:  1,
		AddOffset: -10,
		Limit:     10,
	})
	if err != nil {
		t.Fatalf("GetUnreadMentions: %v", err)
	}
	if mentions.Count != 1 || len(mentions.Messages) != 1 || mentions.Messages[0].ID != sent.Message.ID {
		t.Fatalf("mentions = count %d messages %+v, want sent message", mentions.Count, mentions.Messages)
	}
	read, err := service.ReadMentions(ctx, 1002, domain.ReadChannelMentionsRequest{ChannelID: created.Channel.ID})
	if err != nil {
		t.Fatalf("ReadMentions: %v", err)
	}
	if read.ChannelPts != sent.Event.Pts || read.Offset != 0 || read.Cleared != 1 {
		t.Fatalf("read mentions = %+v, want pts %d cleared 1 no offset", read, sent.Event.Pts)
	}
	mentions, err = service.GetUnreadMentions(ctx, 1002, domain.ChannelUnreadMentionsFilter{ChannelID: created.Channel.ID, Limit: 10})
	if err != nil {
		t.Fatalf("GetUnreadMentions after read: %v", err)
	}
	if mentions.Count != 0 || len(mentions.Messages) != 0 {
		t.Fatalf("mentions after read = count %d messages %d, want empty", mentions.Count, len(mentions.Messages))
	}
	history, err = service.GetHistory(ctx, 1002, domain.ChannelHistoryFilter{ChannelID: created.Channel.ID, Limit: 10})
	if err != nil {
		t.Fatalf("GetHistory after read mentions: %v", err)
	}
	// 官方语义：mention 已读后 mentioned 高亮永久保留，仅 media_unread 清除。
	if len(history.Messages) == 0 || !history.Messages[0].Mentioned || history.Messages[0].MediaUnread {
		t.Fatalf("mentioned history after read = %+v, want mentioned kept with media_unread cleared", history.Messages)
	}
}

func TestServiceRejectsMismatchedUserContextForStateReads(t *testing.T) {
	ctx := context.Background()
	service := NewService(memory.NewChannelStore())
	created, err := service.CreateMegagroupFromCreateChat(ctx, 1001, domain.CreateChannelRequest{
		Title:         "Context Guard",
		MemberUserIDs: []int64{1002},
		Date:          10,
	})
	if err != nil {
		t.Fatalf("CreateMegagroupFromCreateChat: %v", err)
	}
	sent, err := service.SendMessage(ctx, 1001, domain.SendChannelMessageRequest{
		ChannelID: created.Channel.ID,
		RandomID:  91,
		Message:   "guard",
		Date:      11,
	})
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if _, err := service.ReadHistory(ctx, 1001, domain.ReadChannelHistoryRequest{
		UserID:    1002,
		ChannelID: created.Channel.ID,
		MaxID:     sent.Message.ID,
	}); !errors.Is(err, domain.ErrChannelInvalid) {
		t.Fatalf("ReadHistory mismatched user err = %v, want ErrChannelInvalid", err)
	}
	if _, err := service.GetMessageReadParticipants(ctx, 1001, domain.ChannelReadParticipantsRequest{
		UserID:    1002,
		ChannelID: created.Channel.ID,
		MessageID: sent.Message.ID,
	}); !errors.Is(err, domain.ErrChannelInvalid) {
		t.Fatalf("GetMessageReadParticipants mismatched user err = %v, want ErrChannelInvalid", err)
	}
	if _, err := service.GetDifference(ctx, 1001, domain.ChannelDifferenceRequest{
		UserID:    1002,
		ChannelID: created.Channel.ID,
		Pts:       0,
	}); !errors.Is(err, domain.ErrChannelInvalid) {
		t.Fatalf("GetDifference mismatched user err = %v, want ErrChannelInvalid", err)
	}
}

func TestServiceRejectsHugeChannelDialogVector(t *testing.T) {
	service := NewService(memory.NewChannelStore())
	ids := make([]int64, domain.MaxDialogFolderPeers+1)
	for i := range ids {
		ids[i] = int64(i + 1)
	}
	if _, err := service.GetDialogs(context.Background(), 1001, ids); !errors.Is(err, domain.ErrChannelInvalid) {
		t.Fatalf("GetDialogs huge channel vector err = %v, want ErrChannelInvalid", err)
	}
}

func TestChannelHistorySearchQueryIsBounded(t *testing.T) {
	ctx := context.Background()
	store := memory.NewChannelStore()
	service := NewService(store)
	created, err := service.CreateMegagroupFromCreateChat(ctx, 1001, domain.CreateChannelRequest{
		CreatorUserID: 1001,
		Title:         "Bounded History",
		Date:          10,
	})
	if err != nil {
		t.Fatalf("CreateMegagroupFromCreateChat: %v", err)
	}
	_, err = service.GetHistory(ctx, 1001, domain.ChannelHistoryFilter{
		ChannelID: created.Channel.ID,
		Query:     strings.Repeat("x", domain.MaxChannelHistoryQueryLength+1),
		Limit:     10,
	})
	if !errors.Is(err, domain.ErrChannelInvalid) {
		t.Fatalf("GetHistory long query err = %v, want channel invalid", err)
	}
}

func TestChannelHistorySupportsOffsetDateOnly(t *testing.T) {
	ctx := context.Background()
	store := memory.NewChannelStore()
	service := NewService(store)
	created, err := service.CreateMegagroupFromCreateChat(ctx, 1001, domain.CreateChannelRequest{
		CreatorUserID: 1001,
		Title:         "Date Cursor",
		Date:          10,
	})
	if err != nil {
		t.Fatalf("CreateMegagroupFromCreateChat: %v", err)
	}
	if _, err := service.SendMessage(ctx, 1001, domain.SendChannelMessageRequest{
		ChannelID: created.Channel.ID,
		RandomID:  1,
		Message:   "old",
		Date:      20,
	}); err != nil {
		t.Fatalf("send old: %v", err)
	}
	if _, err := service.SendMessage(ctx, 1001, domain.SendChannelMessageRequest{
		ChannelID: created.Channel.ID,
		RandomID:  2,
		Message:   "new",
		Date:      30,
	}); err != nil {
		t.Fatalf("send new: %v", err)
	}

	history, err := service.GetHistory(ctx, 1001, domain.ChannelHistoryFilter{
		ChannelID:  created.Channel.ID,
		OffsetDate: 30,
		Limit:      10,
	})
	if err != nil {
		t.Fatalf("GetHistory: %v", err)
	}
	if len(history.Messages) != 2 || history.Messages[0].Body != "old" || history.Messages[1].Action == nil {
		t.Fatalf("history = %+v, want messages older than offset date including service message", history.Messages)
	}
}

func TestChannelDifferenceTooLongReturnsLatestSnapshot(t *testing.T) {
	ctx := context.Background()
	store := memory.NewChannelStore()
	service := NewService(store)
	created, err := service.CreateMegagroupFromCreateChat(ctx, 1001, domain.CreateChannelRequest{
		CreatorUserID: 1001,
		Title:         "Long Difference",
		MemberUserIDs: []int64{1002},
		Date:          10,
	})
	if err != nil {
		t.Fatalf("CreateMegagroupFromCreateChat: %v", err)
	}
	var lastPts int
	for i := 0; i < 12; i++ {
		sent, err := service.SendMessage(ctx, 1001, domain.SendChannelMessageRequest{
			ChannelID: created.Channel.ID,
			RandomID:  int64(i + 1),
			Message:   "msg",
			Date:      11 + i,
		})
		if err != nil {
			t.Fatalf("SendMessage %d: %v", i, err)
		}
		lastPts = sent.Event.Pts
	}
	diff, err := service.GetDifference(ctx, 1002, domain.ChannelDifferenceRequest{
		ChannelID: created.Channel.ID,
		Pts:       0,
		Limit:     3,
	})
	if err != nil {
		t.Fatalf("GetDifference: %v", err)
	}
	if !diff.TooLong || !diff.Final || diff.Pts != lastPts {
		t.Fatalf("diff = %+v, want tooLong final snapshot at pts %d", diff, lastPts)
	}
	if len(diff.NewMessages) == 0 || len(diff.NewMessages) > domain.MaxChannelDifferenceTooLongMessages {
		t.Fatalf("tooLong messages = %d, want bounded latest snapshot", len(diff.NewMessages))
	}
}

func TestGetParticipantsCapsDeepOffset(t *testing.T) {
	ctx := context.Background()
	service := NewService(memory.NewChannelStore())
	created, err := service.CreateMegagroupFromCreateChat(ctx, 1001, domain.CreateChannelRequest{
		Title:         "Team",
		MemberUserIDs: []int64{1002, 1003},
		Date:          10,
	})
	if err != nil {
		t.Fatalf("CreateMegagroupFromCreateChat: %v", err)
	}
	page, err := service.GetParticipants(ctx, 1001, created.Channel.ID, domain.ChannelParticipantsFilter{}, domain.MaxChannelParticipantsOffset+1_000_000, 10)
	if err != nil {
		t.Fatalf("GetParticipants deep offset: %v", err)
	}
	if len(page.Participants) != 0 || page.Count != 3 {
		t.Fatalf("deep offset page = %+v, want bounded empty page with real count", page)
	}
}

func TestDefaultBannedRightsRestrictMemberSendAndInvite(t *testing.T) {
	ctx := context.Background()
	service := NewService(memory.NewChannelStore())
	created, err := service.CreateMegagroupFromCreateChat(ctx, 1001, domain.CreateChannelRequest{
		Title:         "Permissions",
		MemberUserIDs: []int64{1002},
		Date:          10,
	})
	if err != nil {
		t.Fatalf("CreateMegagroupFromCreateChat: %v", err)
	}
	updated, err := service.EditDefaultBannedRights(ctx, 1001, domain.EditChannelDefaultBannedRightsRequest{
		ChannelID: created.Channel.ID,
		BannedRights: domain.ChannelBannedRights{
			SendMessages: true,
			InviteUsers:  true,
		},
		Date: 11,
	})
	if err != nil {
		t.Fatalf("EditDefaultBannedRights: %v", err)
	}
	if !updated.DefaultBannedRights.SendMessages || !updated.DefaultBannedRights.InviteUsers {
		t.Fatalf("default banned rights = %+v, want send+invite restricted", updated.DefaultBannedRights)
	}
	if _, err := service.SendMessage(ctx, 1002, domain.SendChannelMessageRequest{
		ChannelID: created.Channel.ID,
		RandomID:  1,
		Message:   "blocked",
		Date:      12,
	}); !errors.Is(err, domain.ErrChannelWriteForbidden) {
		t.Fatalf("member SendMessage err = %v, want ErrChannelWriteForbidden", err)
	}
	if _, err := service.InviteToChannel(ctx, 1002, created.Channel.ID, []int64{1003}, 12); !errors.Is(err, domain.ErrChannelAdminRequired) {
		t.Fatalf("member InviteToChannel err = %v, want ErrChannelAdminRequired", err)
	}
	updated, err = service.SetBoostsToUnblockRestrictions(ctx, 1001, created.Channel.ID, 1)
	if err != nil {
		t.Fatalf("SetBoostsToUnblockRestrictions: %v", err)
	}
	if updated.BoostsUnrestrict != 1 {
		t.Fatalf("boosts unrestrict = %d, want 1", updated.BoostsUnrestrict)
	}
	if _, err := service.ApplyPremiumBoost(ctx, 1002, created.Channel.ID, []int{domain.DefaultPremiumBoostSlotID}, 13, 1000); err != nil {
		t.Fatalf("ApplyPremiumBoost member: %v", err)
	}
	boosted, err := service.SendMessage(ctx, 1002, domain.SendChannelMessageRequest{
		ChannelID: created.Channel.ID,
		RandomID:  4,
		Message:   "member boosted ok",
		Date:      14,
	})
	if err != nil {
		t.Fatalf("member SendMessage after boost: %v", err)
	}
	if boosted.Message.FromBoostsApplied != 1 {
		t.Fatalf("from_boosts_applied = %d, want 1", boosted.Message.FromBoostsApplied)
	}
	if _, err := service.SendMessage(ctx, 1001, domain.SendChannelMessageRequest{
		ChannelID: created.Channel.ID,
		RandomID:  2,
		Message:   "owner ok",
		Date:      15,
	}); err != nil {
		t.Fatalf("creator SendMessage under default rights: %v", err)
	}
	if _, err := service.EditDefaultBannedRights(ctx, 1001, domain.EditChannelDefaultBannedRightsRequest{
		ChannelID:    created.Channel.ID,
		BannedRights: domain.ChannelBannedRights{},
		Date:         16,
	}); err != nil {
		t.Fatalf("clear default banned rights: %v", err)
	}
	if _, err := service.SendMessage(ctx, 1002, domain.SendChannelMessageRequest{
		ChannelID: created.Channel.ID,
		RandomID:  3,
		Message:   "member ok",
		Date:      17,
	}); err != nil {
		t.Fatalf("member SendMessage after clear: %v", err)
	}
}

func TestSendPlainRightsRestrictTextMessages(t *testing.T) {
	ctx := context.Background()
	service := NewService(memory.NewChannelStore())
	created, err := service.CreateMegagroupFromCreateChat(ctx, 1001, domain.CreateChannelRequest{
		Title:         "Plain Text Gate",
		MemberUserIDs: []int64{1002},
		Date:          10,
	})
	if err != nil {
		t.Fatalf("CreateMegagroupFromCreateChat: %v", err)
	}

	if _, err := service.EditBanned(ctx, 1001, domain.EditChannelBannedRequest{
		ChannelID:   created.Channel.ID,
		Participant: domain.Peer{Type: domain.PeerTypeUser, ID: 1002},
		BannedRights: domain.ChannelBannedRights{
			SendPlain: true,
		},
		Date: 11,
	}); err != nil {
		t.Fatalf("EditBanned send_plain: %v", err)
	}
	if _, err := service.SendMessage(ctx, 1002, domain.SendChannelMessageRequest{
		ChannelID: created.Channel.ID,
		RandomID:  1,
		Message:   "blocked by member send_plain",
		Date:      12,
	}); !errors.Is(err, domain.ErrChannelWriteForbidden) {
		t.Fatalf("member SendMessage err = %v, want ErrChannelWriteForbidden", err)
	}
	if _, err := service.EditBanned(ctx, 1001, domain.EditChannelBannedRequest{
		ChannelID:    created.Channel.ID,
		Participant:  domain.Peer{Type: domain.PeerTypeUser, ID: 1002},
		BannedRights: domain.ChannelBannedRights{},
		Date:         13,
	}); err != nil {
		t.Fatalf("clear member send_plain: %v", err)
	}

	if _, err := service.EditDefaultBannedRights(ctx, 1001, domain.EditChannelDefaultBannedRightsRequest{
		ChannelID: created.Channel.ID,
		BannedRights: domain.ChannelBannedRights{
			SendPlain: true,
		},
		Date: 14,
	}); err != nil {
		t.Fatalf("EditDefaultBannedRights send_plain: %v", err)
	}
	if _, err := service.SendMessage(ctx, 1002, domain.SendChannelMessageRequest{
		ChannelID: created.Channel.ID,
		RandomID:  2,
		Message:   "blocked by default send_plain",
		Date:      15,
	}); !errors.Is(err, domain.ErrChannelWriteForbidden) {
		t.Fatalf("member SendMessage default err = %v, want ErrChannelWriteForbidden", err)
	}
	if _, err := service.SendMessage(ctx, 1001, domain.SendChannelMessageRequest{
		ChannelID: created.Channel.ID,
		RandomID:  3,
		Message:   "creator bypass",
		Date:      16,
	}); err != nil {
		t.Fatalf("creator SendMessage under send_plain default: %v", err)
	}
}

func TestFineGrainedBannedRightsRestrictMediaReactionsAndTopics(t *testing.T) {
	ctx := context.Background()
	service := NewService(memory.NewChannelStore())
	created, err := service.CreateMegagroupFromCreateChat(ctx, 1001, domain.CreateChannelRequest{
		Title:         "Fine Rights",
		MemberUserIDs: []int64{1002},
		Date:          10,
	})
	if err != nil {
		t.Fatalf("CreateMegagroupFromCreateChat: %v", err)
	}

	if _, err := service.EditBanned(ctx, 1001, domain.EditChannelBannedRequest{
		ChannelID:   created.Channel.ID,
		Participant: domain.Peer{Type: domain.PeerTypeUser, ID: 1002},
		BannedRights: domain.ChannelBannedRights{
			SendPhotos: true,
		},
		Date: 11,
	}); err != nil {
		t.Fatalf("EditBanned send_photos: %v", err)
	}
	if _, err := service.SendMessage(ctx, 1002, domain.SendChannelMessageRequest{
		ChannelID: created.Channel.ID,
		RandomID:  1,
		Media:     &domain.MessageMedia{Kind: domain.MessageMediaKindPhoto, Photo: &domain.Photo{ID: 10}},
		Date:      12,
	}); !errors.Is(err, domain.ErrChannelWriteForbidden) {
		t.Fatalf("member photo SendMessage err = %v, want ErrChannelWriteForbidden", err)
	}
	if _, err := service.SendMessage(ctx, 1002, domain.SendChannelMessageRequest{
		ChannelID: created.Channel.ID,
		RandomID:  2,
		Message:   "text still allowed",
		Date:      13,
	}); err != nil {
		t.Fatalf("member text under send_photos ban: %v", err)
	}

	if _, err := service.EditBanned(ctx, 1001, domain.EditChannelBannedRequest{
		ChannelID:    created.Channel.ID,
		Participant:  domain.Peer{Type: domain.PeerTypeUser, ID: 1002},
		BannedRights: domain.ChannelBannedRights{},
		Date:         14,
	}); err != nil {
		t.Fatalf("clear member rights: %v", err)
	}
	sent, err := service.SendMessage(ctx, 1001, domain.SendChannelMessageRequest{
		ChannelID: created.Channel.ID,
		RandomID:  3,
		Message:   "react here",
		Date:      15,
	})
	if err != nil {
		t.Fatalf("owner SendMessage: %v", err)
	}
	if _, err := service.EditDefaultBannedRights(ctx, 1001, domain.EditChannelDefaultBannedRightsRequest{
		ChannelID: created.Channel.ID,
		BannedRights: domain.ChannelBannedRights{
			SendReactions: true,
		},
		Date: 16,
	}); err != nil {
		t.Fatalf("EditDefaultBannedRights send_reactions: %v", err)
	}
	if _, err := service.SetMessageReactions(ctx, 1002, domain.SetChannelMessageReactionsRequest{
		ChannelID: created.Channel.ID,
		MessageID: sent.Message.ID,
		Reactions: []domain.MessageReaction{{
			Type:     domain.MessageReactionEmoji,
			Emoticon: "\U0001f44d",
		}},
		Date: 17,
	}); !errors.Is(err, domain.ErrChannelWriteForbidden) {
		t.Fatalf("member SetMessageReactions err = %v, want ErrChannelWriteForbidden", err)
	}
	if _, err := service.SetMessageReactions(ctx, 1002, domain.SetChannelMessageReactionsRequest{
		ChannelID: created.Channel.ID,
		MessageID: sent.Message.ID,
		Date:      18,
	}); err != nil {
		t.Fatalf("member clear reactions under send_reactions ban: %v", err)
	}

	if _, err := service.SetForum(ctx, 1001, created.Channel.ID, true, true); err != nil {
		t.Fatalf("SetForum: %v", err)
	}
	if _, err := service.EditDefaultBannedRights(ctx, 1001, domain.EditChannelDefaultBannedRightsRequest{
		ChannelID: created.Channel.ID,
		BannedRights: domain.ChannelBannedRights{
			ManageTopics: true,
		},
		Date: 19,
	}); err != nil {
		t.Fatalf("EditDefaultBannedRights manage_topics: %v", err)
	}
	if _, err := service.CreateForumTopic(ctx, 1002, domain.CreateChannelForumTopicRequest{
		ChannelID: created.Channel.ID,
		Title:     "blocked topic",
		RandomID:  4,
		Date:      20,
	}); !errors.Is(err, domain.ErrChannelWriteForbidden) {
		t.Fatalf("member CreateForumTopic err = %v, want ErrChannelWriteForbidden", err)
	}
	if _, err := service.EditDefaultBannedRights(ctx, 1001, domain.EditChannelDefaultBannedRightsRequest{
		ChannelID:    created.Channel.ID,
		BannedRights: domain.ChannelBannedRights{},
		Date:         21,
	}); err != nil {
		t.Fatalf("clear default rights: %v", err)
	}
	topic, err := service.CreateForumTopic(ctx, 1002, domain.CreateChannelForumTopicRequest{
		ChannelID: created.Channel.ID,
		Title:     "member topic",
		RandomID:  5,
		Date:      22,
	})
	if err != nil {
		t.Fatalf("member CreateForumTopic after clear: %v", err)
	}
	if _, err := service.EditBanned(ctx, 1001, domain.EditChannelBannedRequest{
		ChannelID:   created.Channel.ID,
		Participant: domain.Peer{Type: domain.PeerTypeUser, ID: 1002},
		BannedRights: domain.ChannelBannedRights{
			ManageTopics: true,
		},
		Date: 23,
	}); err != nil {
		t.Fatalf("EditBanned manage_topics: %v", err)
	}
	renamed := "renamed"
	if _, err := service.EditForumTopic(ctx, 1002, domain.EditChannelForumTopicRequest{
		ChannelID: created.Channel.ID,
		TopicID:   topic.Topic.TopicID,
		Title:     &renamed,
		Date:      24,
	}); !errors.Is(err, domain.ErrChannelAdminRequired) {
		t.Fatalf("member EditForumTopic err = %v, want ErrChannelAdminRequired", err)
	}
}

func TestSendMessageResolvesChannelReplyTopID(t *testing.T) {
	ctx := context.Background()
	service := NewService(memory.NewChannelStore())

	created, err := service.CreateMegagroupFromCreateChat(ctx, 1001, domain.CreateChannelRequest{
		Title:         "Replies",
		MemberUserIDs: []int64{1002},
		Date:          10,
	})
	if err != nil {
		t.Fatalf("CreateMegagroupFromCreateChat: %v", err)
	}
	root, err := service.SendMessage(ctx, 1001, domain.SendChannelMessageRequest{
		ChannelID: created.Channel.ID,
		RandomID:  1,
		Message:   "root",
		Date:      11,
	})
	if err != nil {
		t.Fatalf("send root: %v", err)
	}
	reply, err := service.SendMessage(ctx, 1002, domain.SendChannelMessageRequest{
		ChannelID: created.Channel.ID,
		RandomID:  2,
		Message:   "reply",
		ReplyTo: &domain.MessageReply{
			MessageID:   root.Message.ID,
			QuoteText:   "ro",
			QuoteOffset: 0,
			QuoteEntities: []domain.MessageEntity{{
				Type:   domain.MessageEntityBold,
				Offset: 0,
				Length: 2,
			}},
		},
		Date: 12,
	})
	if err != nil {
		t.Fatalf("send reply: %v", err)
	}
	if reply.Message.ReplyTo == nil {
		t.Fatal("reply metadata is nil")
	}
	channelPeer := domain.Peer{Type: domain.PeerTypeChannel, ID: created.Channel.ID}
	if reply.Message.ReplyTo.MessageID != root.Message.ID || reply.Message.ReplyTo.Peer != channelPeer || reply.Message.ReplyTo.TopMessageID != root.Message.ID {
		t.Fatalf("reply metadata = %+v, want channel peer and root top id %d", reply.Message.ReplyTo, root.Message.ID)
	}
	if reply.Message.ReplyTo.QuoteText != "ro" || len(reply.Message.ReplyTo.QuoteEntities) != 1 {
		t.Fatalf("reply quote = %+v, want preserved quote metadata", reply.Message.ReplyTo)
	}

	nested, err := service.SendMessage(ctx, 1001, domain.SendChannelMessageRequest{
		ChannelID: created.Channel.ID,
		RandomID:  3,
		Message:   "nested",
		ReplyTo:   &domain.MessageReply{MessageID: reply.Message.ID},
		Date:      13,
	})
	if err != nil {
		t.Fatalf("send nested reply: %v", err)
	}
	if nested.Message.ReplyTo == nil || nested.Message.ReplyTo.TopMessageID != root.Message.ID {
		t.Fatalf("nested reply = %+v, want inherited top id %d", nested.Message.ReplyTo, root.Message.ID)
	}
	_, err = service.SendMessage(ctx, 1001, domain.SendChannelMessageRequest{
		ChannelID: created.Channel.ID,
		RandomID:  4,
		Message:   "bad reply",
		ReplyTo:   &domain.MessageReply{MessageID: 999},
		Date:      14,
	})
	if !errors.Is(err, domain.ErrReplyMessageIDInvalid) {
		t.Fatalf("bad reply err = %v, want ErrReplyMessageIDInvalid", err)
	}
	_, err = service.SendMessage(ctx, 1001, domain.SendChannelMessageRequest{
		ChannelID: created.Channel.ID,
		RandomID:  5,
		Message:   "bad quote offset",
		ReplyTo: &domain.MessageReply{
			MessageID:   root.Message.ID,
			QuoteText:   "ro",
			QuoteOffset: domain.MaxMessageReplyQuoteOffset + 1,
		},
		Date: 15,
	})
	if !errors.Is(err, domain.ErrReplyMessageIDInvalid) {
		t.Fatalf("bad quote offset err = %v, want ErrReplyMessageIDInvalid", err)
	}
}

func TestGetMessageReadParticipantsUsesChannelReadWatermark(t *testing.T) {
	ctx := context.Background()
	service := NewService(memory.NewChannelStore())

	created, err := service.CreateMegagroupFromCreateChat(ctx, 1001, domain.CreateChannelRequest{
		Title:         "Readers",
		MemberUserIDs: []int64{1002},
		Date:          10,
	})
	if err != nil {
		t.Fatalf("CreateMegagroupFromCreateChat: %v", err)
	}
	sent, err := service.SendMessage(ctx, 1001, domain.SendChannelMessageRequest{
		ChannelID: created.Channel.ID,
		RandomID:  100,
		Message:   "read me",
		Date:      11,
	})
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if _, err := service.ReadHistory(ctx, 1002, domain.ReadChannelHistoryRequest{
		ChannelID: created.Channel.ID,
		MaxID:     sent.Message.ID,
		Date:      20,
	}); err != nil {
		t.Fatalf("ReadHistory: %v", err)
	}

	readers, err := service.GetMessageReadParticipants(ctx, 1001, domain.ChannelReadParticipantsRequest{
		ChannelID: created.Channel.ID,
		MessageID: sent.Message.ID,
		Date:      21,
	})
	if err != nil {
		t.Fatalf("GetMessageReadParticipants: %v", err)
	}
	if len(readers.Participants) != 1 || readers.Participants[0].UserID != 1002 || readers.Participants[0].Date != 20 {
		t.Fatalf("readers = %+v, want friend read at date 20", readers.Participants)
	}
}

func TestParticipantsHiddenHidesMemberListAndReadParticipants(t *testing.T) {
	ctx := context.Background()
	service := NewService(memory.NewChannelStore())

	created, err := service.CreateMegagroupFromCreateChat(ctx, 1001, domain.CreateChannelRequest{
		Title:         "Hidden Members",
		MemberUserIDs: []int64{1002, 1003},
		Date:          10,
	})
	if err != nil {
		t.Fatalf("CreateMegagroupFromCreateChat: %v", err)
	}
	sent, err := service.SendMessage(ctx, 1001, domain.SendChannelMessageRequest{
		ChannelID: created.Channel.ID,
		RandomID:  100,
		Message:   "read me",
		Date:      11,
	})
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if _, err := service.ReadHistory(ctx, 1002, domain.ReadChannelHistoryRequest{
		ChannelID: created.Channel.ID,
		MaxID:     sent.Message.ID,
		Date:      20,
	}); err != nil {
		t.Fatalf("ReadHistory: %v", err)
	}
	hidden, err := service.SetParticipantsHidden(ctx, 1001, created.Channel.ID, true)
	if err != nil {
		t.Fatalf("SetParticipantsHidden: %v", err)
	}
	if !hidden.ParticipantsHidden {
		t.Fatalf("channel = %+v, want participants hidden", hidden)
	}
	if _, err := service.SetParticipantsHidden(ctx, 1002, created.Channel.ID, false); !errors.Is(err, domain.ErrChannelAdminRequired) {
		t.Fatalf("member SetParticipantsHidden err = %v, want ErrChannelAdminRequired", err)
	}
	members, err := service.GetParticipants(ctx, 1002, created.Channel.ID, domain.ChannelParticipantsFilter{}, 0, 10)
	if err != nil {
		t.Fatalf("GetParticipants hidden member view: %v", err)
	}
	if len(members.Participants) != 0 || members.Count != hidden.ParticipantsCount {
		t.Fatalf("hidden members page = %+v, want empty page with aggregate count", members)
	}
	admins, err := service.GetParticipants(ctx, 1002, created.Channel.ID, domain.ChannelParticipantsFilter{Kind: domain.ChannelParticipantsAdmins}, 0, 10)
	if err != nil {
		t.Fatalf("GetParticipants hidden admins: %v", err)
	}
	if len(admins.Participants) != 1 || admins.Participants[0].UserID != 1001 {
		t.Fatalf("hidden admins page = %+v, want creator visible", admins.Participants)
	}
	readers, err := service.GetMessageReadParticipants(ctx, 1001, domain.ChannelReadParticipantsRequest{
		ChannelID: created.Channel.ID,
		MessageID: sent.Message.ID,
		Date:      21,
	})
	if err != nil {
		t.Fatalf("GetMessageReadParticipants hidden: %v", err)
	}
	if len(readers.Participants) != 0 {
		t.Fatalf("hidden readers = %+v, want none", readers.Participants)
	}
}

func TestAnonymousAdminHiddenFromRegularParticipantLists(t *testing.T) {
	ctx := context.Background()
	service := NewService(memory.NewChannelStore())
	created, err := service.CreateMegagroupFromCreateChat(ctx, 1001, domain.CreateChannelRequest{
		Title:         "Anonymous Admins",
		MemberUserIDs: []int64{1002, 1003},
		Date:          10,
	})
	if err != nil {
		t.Fatalf("CreateMegagroupFromCreateChat: %v", err)
	}
	if _, err := service.EditAdmin(ctx, 1001, domain.EditChannelAdminRequest{
		ChannelID: created.Channel.ID,
		MemberID:  1002,
		AdminRights: domain.ChannelAdminRights{
			Anonymous:  true,
			ChangeInfo: true,
		},
		Date: 11,
	}); err != nil {
		t.Fatalf("EditAdmin anonymous: %v", err)
	}

	recent, err := service.GetParticipants(ctx, 1003, created.Channel.ID, domain.ChannelParticipantsFilter{Kind: domain.ChannelParticipantsRecent}, 0, 10)
	if err != nil {
		t.Fatalf("regular GetParticipants recent: %v", err)
	}
	if containsChannelParticipant(recent.Participants, 1002) || recent.Count != 2 {
		t.Fatalf("regular recent participants = %+v count=%d, want anonymous admin hidden and count adjusted", recent.Participants, recent.Count)
	}
	admins, err := service.GetParticipants(ctx, 1003, created.Channel.ID, domain.ChannelParticipantsFilter{Kind: domain.ChannelParticipantsAdmins}, 0, 10)
	if err != nil {
		t.Fatalf("regular GetParticipants admins: %v", err)
	}
	if containsChannelParticipant(admins.Participants, 1002) || len(admins.Participants) != 1 || admins.Participants[0].UserID != 1001 {
		t.Fatalf("regular admin participants = %+v, want only creator visible", admins.Participants)
	}
	adminView, err := service.GetParticipants(ctx, 1001, created.Channel.ID, domain.ChannelParticipantsFilter{Kind: domain.ChannelParticipantsAdmins}, 0, 10)
	if err != nil {
		t.Fatalf("owner GetParticipants admins: %v", err)
	}
	if !containsChannelParticipant(adminView.Participants, 1002) {
		t.Fatalf("owner admin participants = %+v, want anonymous admin visible to admins", adminView.Participants)
	}
}

func containsChannelParticipant(participants []domain.ChannelMember, userID int64) bool {
	for _, participant := range participants {
		if participant.UserID == userID {
			return true
		}
	}
	return false
}

func TestBroadcastRejectsMemberPost(t *testing.T) {
	ctx := context.Background()
	service := NewService(memory.NewChannelStore())

	created, err := service.CreateChannel(ctx, 1001, domain.CreateChannelRequest{
		Title:         "News",
		Broadcast:     true,
		MemberUserIDs: []int64{1002},
		Date:          10,
	})
	if err != nil {
		t.Fatalf("CreateChannel: %v", err)
	}
	if !created.Channel.Broadcast || created.Channel.Megagroup {
		t.Fatalf("channel flags = broadcast:%v megagroup:%v, want broadcast only", created.Channel.Broadcast, created.Channel.Megagroup)
	}

	_, err = service.SendMessage(ctx, 1002, domain.SendChannelMessageRequest{
		ChannelID: created.Channel.ID,
		RandomID:  1,
		Message:   "member post",
		Date:      11,
	})
	if !errors.Is(err, domain.ErrChannelWriteForbidden) {
		t.Fatalf("member SendMessage error = %v, want ErrChannelWriteForbidden", err)
	}

	sent, err := service.SendMessage(ctx, 1001, domain.SendChannelMessageRequest{
		ChannelID: created.Channel.ID,
		RandomID:  2,
		Message:   "owner post",
		Date:      12,
	})
	if err != nil {
		t.Fatalf("creator SendMessage: %v", err)
	}
	if !sent.Message.Post {
		t.Fatalf("broadcast message Post=false, want true")
	}
}

func TestChannelEditDeleteAndLocalClearUseChannelPts(t *testing.T) {
	ctx := context.Background()
	service := NewService(memory.NewChannelStore())
	created, err := service.CreateMegagroupFromCreateChat(ctx, 1001, domain.CreateChannelRequest{
		Title:         "Team",
		MemberUserIDs: []int64{1002},
		Date:          10,
	})
	if err != nil {
		t.Fatalf("CreateMegagroupFromCreateChat: %v", err)
	}
	first, err := service.SendMessage(ctx, 1001, domain.SendChannelMessageRequest{ChannelID: created.Channel.ID, RandomID: 1, Message: "one", Date: 11})
	if err != nil {
		t.Fatalf("SendMessage first: %v", err)
	}
	second, err := service.SendMessage(ctx, 1002, domain.SendChannelMessageRequest{ChannelID: created.Channel.ID, RandomID: 2, Message: "two", Date: 12})
	if err != nil {
		t.Fatalf("SendMessage second: %v", err)
	}

	edited, err := service.EditMessage(ctx, 1002, domain.EditChannelMessageRequest{
		ChannelID: created.Channel.ID,
		ID:        second.Message.ID,
		Message:   "two edited",
		EditDate:  13,
	})
	if err != nil {
		t.Fatalf("EditMessage: %v", err)
	}
	if edited.Event.Type != domain.ChannelUpdateEditMessage || edited.Event.Pts != 4 || edited.Event.PtsCount != 1 {
		t.Fatalf("edit event = %+v, want channel edit pts=4 count=1", edited.Event)
	}
	duplicate, err := service.SendMessage(ctx, 1002, domain.SendChannelMessageRequest{ChannelID: created.Channel.ID, RandomID: 2, Message: "two", Date: 13})
	if err != nil {
		t.Fatalf("duplicate SendMessage after edit: %v", err)
	}
	if !duplicate.Duplicate || duplicate.Event.Type != domain.ChannelUpdateNewMessage || duplicate.Message.Body != "two edited" || duplicate.Event.Message.Body != "two edited" {
		t.Fatalf("duplicate after edit = %+v, want current message in new-message replay", duplicate)
	}

	deleted, err := service.DeleteMessages(ctx, 1001, domain.DeleteChannelMessagesRequest{
		ChannelID: created.Channel.ID,
		IDs:       []int{first.Message.ID, second.Message.ID},
		Date:      14,
	})
	if err != nil {
		t.Fatalf("DeleteMessages: %v", err)
	}
	if deleted.Event.Type != domain.ChannelUpdateDeleteMessages || deleted.Event.Pts != 6 || deleted.Event.PtsCount != 2 {
		t.Fatalf("delete event = %+v, want pts advanced by deleted id count", deleted.Event)
	}
	diff, err := service.GetDifference(ctx, 1002, domain.ChannelDifferenceRequest{ChannelID: created.Channel.ID, Pts: 3, Limit: 10})
	if err != nil {
		t.Fatalf("GetDifference: %v", err)
	}
	if len(diff.OtherUpdates) != 2 || diff.OtherUpdates[1].Type != domain.ChannelUpdateDeleteMessages || diff.Pts != 6 {
		t.Fatalf("diff after edit/delete = %+v, want edit then delete through channel pts", diff)
	}

	clear, err := service.DeleteHistory(ctx, 1002, domain.DeleteChannelHistoryRequest{ChannelID: created.Channel.ID, MaxID: 6})
	if err != nil {
		t.Fatalf("DeleteHistory local: %v", err)
	}
	if clear.Event.Pts != 0 {
		t.Fatalf("local clear event = %+v, want no channel pts event", clear.Event)
	}
	history, err := service.GetHistory(ctx, 1002, domain.ChannelHistoryFilter{ChannelID: created.Channel.ID, Limit: 10})
	if err != nil {
		t.Fatalf("GetHistory after local clear: %v", err)
	}
	if len(history.Messages) != 0 {
		t.Fatalf("history after local clear = %+v, want hidden for current user", history.Messages)
	}
}

func TestDeleteParticipantHistoryDeletesOneBoundedSenderPage(t *testing.T) {
	ctx := context.Background()
	service := NewService(memory.NewChannelStore())
	created, err := service.CreateMegagroupFromCreateChat(ctx, 1001, domain.CreateChannelRequest{
		Title:         "Team",
		MemberUserIDs: []int64{1002},
		Date:          10,
	})
	if err != nil {
		t.Fatalf("CreateMegagroupFromCreateChat: %v", err)
	}
	ownerMsg, err := service.SendMessage(ctx, 1001, domain.SendChannelMessageRequest{ChannelID: created.Channel.ID, RandomID: 1, Message: "owner", Date: 11})
	if err != nil {
		t.Fatalf("owner SendMessage: %v", err)
	}
	first, err := service.SendMessage(ctx, 1002, domain.SendChannelMessageRequest{ChannelID: created.Channel.ID, RandomID: 2, Message: "member one", Date: 12})
	if err != nil {
		t.Fatalf("member first SendMessage: %v", err)
	}
	second, err := service.SendMessage(ctx, 1002, domain.SendChannelMessageRequest{ChannelID: created.Channel.ID, RandomID: 3, Message: "member two", Date: 13})
	if err != nil {
		t.Fatalf("member second SendMessage: %v", err)
	}
	if _, err := service.DeleteParticipantHistory(ctx, 1002, domain.DeleteChannelParticipantHistoryRequest{
		ChannelID:         created.Channel.ID,
		ParticipantUserID: 1001,
		Date:              14,
	}); !errors.Is(err, domain.ErrChannelAdminRequired) {
		t.Fatalf("member DeleteParticipantHistory err = %v, want ErrChannelAdminRequired", err)
	}

	deleted, err := service.DeleteParticipantHistory(ctx, 1001, domain.DeleteChannelParticipantHistoryRequest{
		ChannelID:         created.Channel.ID,
		ParticipantUserID: 1002,
		Date:              15,
	})
	if err != nil {
		t.Fatalf("DeleteParticipantHistory: %v", err)
	}
	if deleted.Event.Type != domain.ChannelUpdateDeleteMessages || deleted.Event.PtsCount != 2 || deleted.Offset != 0 {
		t.Fatalf("deleted = %+v, want one delete update with pts_count=2", deleted)
	}
	wantDeleted := map[int]bool{first.Message.ID: true, second.Message.ID: true}
	for _, id := range deleted.DeletedIDs {
		delete(wantDeleted, id)
	}
	if len(wantDeleted) != 0 {
		t.Fatalf("deleted IDs = %+v, missing member messages %+v", deleted.DeletedIDs, wantDeleted)
	}
	history, err := service.GetHistory(ctx, 1001, domain.ChannelHistoryFilter{ChannelID: created.Channel.ID, Limit: 10})
	if err != nil {
		t.Fatalf("GetHistory: %v", err)
	}
	if len(history.Messages) != 2 || history.Messages[0].ID != ownerMsg.Message.ID {
		t.Fatalf("history after participant delete = %+v, want owner message and create service only", history.Messages)
	}
}

func TestTransferOwnershipDoesNotAdvanceChannelPts(t *testing.T) {
	ctx := context.Background()
	service := NewService(memory.NewChannelStore())
	created, err := service.CreateMegagroupFromCreateChat(ctx, 1001, domain.CreateChannelRequest{
		Title:         "Transfer",
		MemberUserIDs: []int64{1002},
		Date:          10,
	})
	if err != nil {
		t.Fatalf("CreateMegagroupFromCreateChat: %v", err)
	}
	ptsBeforeTransfer := created.Channel.Pts
	transfer, err := service.TransferOwnership(ctx, 1001, domain.TransferChannelOwnershipRequest{
		ChannelID:  created.Channel.ID,
		NewOwnerID: 1002,
		Date:       11,
	})
	if err != nil {
		t.Fatalf("TransferOwnership: %v", err)
	}
	if transfer.Channel.CreatorUserID != 1002 || transfer.NewOwner.Role != domain.ChannelRoleCreator || transfer.OldOwner.Role != domain.ChannelRoleAdmin {
		t.Fatalf("transfer result = %+v, want owner moved to 1002 and old owner admin", transfer)
	}
	if transfer.Channel.Pts != ptsBeforeTransfer {
		t.Fatalf("transfer channel pts = %d, want unchanged %d", transfer.Channel.Pts, ptsBeforeTransfer)
	}
	if len(transfer.Events) != 2 {
		t.Fatalf("transfer events = %+v, want two participant transitions", transfer.Events)
	}
	for _, event := range transfer.Events {
		if event.Type != domain.ChannelUpdateParticipant || event.Pts != 0 || event.PtsCount != 0 {
			t.Fatalf("transfer event = %+v, want transient participant event", event)
		}
	}
	diffAfterTransfer, err := service.GetDifference(ctx, 1002, domain.ChannelDifferenceRequest{ChannelID: created.Channel.ID, Pts: ptsBeforeTransfer, Limit: 10})
	if err != nil {
		t.Fatalf("GetDifference after transfer: %v", err)
	}
	if len(diffAfterTransfer.OtherUpdates) != 0 || diffAfterTransfer.Pts != ptsBeforeTransfer {
		t.Fatalf("diff after transfer = %+v, want no durable participant update", diffAfterTransfer)
	}
	oldOwner, err := service.GetParticipant(ctx, 1002, created.Channel.ID, 1001)
	if err != nil {
		t.Fatalf("GetParticipant old owner: %v", err)
	}
	newOwner, err := service.GetParticipant(ctx, 1002, created.Channel.ID, 1002)
	if err != nil {
		t.Fatalf("GetParticipant new owner: %v", err)
	}
	if oldOwner.Role != domain.ChannelRoleAdmin || newOwner.Role != domain.ChannelRoleCreator {
		t.Fatalf("participants after transfer old=%+v new=%+v, want admin/creator", oldOwner, newOwner)
	}
}

func TestChannelAdminTitlePinAndInvite(t *testing.T) {
	ctx := context.Background()
	service := NewService(memory.NewChannelStore())
	created, err := service.CreateMegagroupFromCreateChat(ctx, 1001, domain.CreateChannelRequest{
		Title:         "Team",
		MemberUserIDs: []int64{1002},
		Date:          10,
	})
	if err != nil {
		t.Fatalf("CreateMegagroupFromCreateChat: %v", err)
	}
	ptsBeforeAdmin := created.Channel.Pts

	admin, err := service.EditAdmin(ctx, 1001, domain.EditChannelAdminRequest{
		ChannelID: created.Channel.ID,
		MemberID:  1002,
		AdminRights: domain.ChannelAdminRights{
			ChangeInfo:  true,
			InviteUsers: true,
			PinMessages: true,
		},
		Rank: "ops",
		Date: 11,
	})
	if err != nil {
		t.Fatalf("EditAdmin: %v", err)
	}
	if admin.Participant.Role != domain.ChannelRoleAdmin || !admin.Participant.AdminRights.PinMessages || admin.Channel.AdminsCount != 2 {
		t.Fatalf("admin result = %+v, want promoted admin with counts", admin)
	}
	if admin.Channel.Pts != ptsBeforeAdmin {
		t.Fatalf("admin channel pts = %d, want unchanged %d", admin.Channel.Pts, ptsBeforeAdmin)
	}
	if admin.Event.Type != domain.ChannelUpdateParticipant || admin.Event.Pts != 0 || admin.Event.PtsCount != 0 || admin.Event.Participant.UserID != 1002 || admin.Event.Previous.UserID != 1002 {
		t.Fatalf("admin participant event = %+v, want transient participant transition", admin.Event)
	}
	diffAfterAdmin, err := service.GetDifference(ctx, 1002, domain.ChannelDifferenceRequest{ChannelID: created.Channel.ID, Pts: ptsBeforeAdmin, Limit: 10})
	if err != nil {
		t.Fatalf("GetDifference after admin: %v", err)
	}
	if len(diffAfterAdmin.OtherUpdates) != 0 || diffAfterAdmin.Pts != ptsBeforeAdmin {
		t.Fatalf("diff after admin = %+v, want no durable participant update", diffAfterAdmin)
	}
	admins, err := service.GetParticipants(ctx, 1001, created.Channel.ID, domain.ChannelParticipantsFilter{Kind: domain.ChannelParticipantsAdmins}, 0, 10)
	if err != nil {
		t.Fatalf("GetParticipants admins: %v", err)
	}
	if len(admins.Participants) != 2 || admins.Participants[1].UserID != 1002 {
		t.Fatalf("admins participants = %+v, want creator and promoted admin", admins.Participants)
	}

	renamed, err := service.EditTitle(ctx, 1002, domain.EditChannelTitleRequest{ChannelID: created.Channel.ID, Title: "Team 2", Date: 12})
	if err != nil {
		t.Fatalf("EditTitle by promoted admin: %v", err)
	}
	if renamed.Channel.Title != "Team 2" || renamed.Event.Type != domain.ChannelUpdateNewMessage || renamed.Message.Action.Type != domain.ChannelActionEditTitle {
		t.Fatalf("renamed = %+v message=%+v, want edit-title service message", renamed.Channel, renamed.Message)
	}

	sent, err := service.SendMessage(ctx, 1001, domain.SendChannelMessageRequest{ChannelID: created.Channel.ID, RandomID: 42, Message: "pin me", Date: 13})
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	pinned, err := service.UpdatePinnedMessage(ctx, 1002, domain.UpdateChannelPinnedMessageRequest{
		ChannelID: created.Channel.ID,
		MessageID: sent.Message.ID,
		Pinned:    true,
		Date:      14,
	})
	if err != nil {
		t.Fatalf("UpdatePinnedMessage: %v", err)
	}
	if pinned.Channel.PinnedMessageID != sent.Message.ID || pinned.Event.Type != domain.ChannelUpdatePinnedMessages || !pinned.Event.Pinned {
		t.Fatalf("pinned = %+v, want pinned channel message event", pinned)
	}

	invited, err := service.InviteToChannel(ctx, 1002, created.Channel.ID, []int64{1004}, 15)
	if err != nil {
		t.Fatalf("InviteToChannel: %v", err)
	}
	if len(invited.Members) != 1 || invited.Members[0].UserID != 1004 {
		t.Fatalf("invited = %+v, want invited user", invited.Members)
	}

	invite, err := service.ExportInvite(ctx, 1002, domain.ExportChannelInviteRequest{ChannelID: created.Channel.ID, Title: "join", Date: 15})
	if err != nil {
		t.Fatalf("ExportInvite: %v", err)
	}
	checked, err := service.CheckInvite(ctx, 1003, invite.Invite.Hash, 16)
	if err != nil {
		t.Fatalf("CheckInvite: %v", err)
	}
	if checked.Already || checked.Channel.ID != created.Channel.ID {
		t.Fatalf("checked invite = %+v, want preview for non-member", checked)
	}
	joined, err := service.ImportInvite(ctx, 1003, domain.ImportChannelInviteRequest{Hash: invite.Invite.Hash, Date: 17})
	if err != nil {
		t.Fatalf("ImportInvite: %v", err)
	}
	if len(joined.Members) != 1 || joined.Members[0].UserID != 1003 || joined.Event.Pts == 0 {
		t.Fatalf("joined = %+v, want imported member with megagroup join event", joined)
	}
	forum, err := service.SetForum(ctx, 1001, created.Channel.ID, true, true)
	if err != nil {
		t.Fatalf("SetForum: %v", err)
	}
	if !forum.Forum || !forum.ForumTabs {
		t.Fatalf("forum = %+v, want enabled with tabs", forum)
	}
	antiSpam, err := service.SetAntiSpam(ctx, 1001, created.Channel.ID, true)
	if err != nil {
		t.Fatalf("SetAntiSpam: %v", err)
	}
	if !antiSpam.AntiSpam {
		t.Fatalf("antiSpam = %+v, want enabled", antiSpam)
	}
	logs, err := service.ListAdminLog(ctx, 1001, domain.ChannelAdminLogRequest{ChannelID: created.Channel.ID, Limit: 20})
	if err != nil {
		t.Fatalf("ListAdminLog: %v", err)
	}
	iosInitialLogs, err := service.ListAdminLog(ctx, 1001, domain.ChannelAdminLogRequest{
		ChannelID: created.Channel.ID,
		MaxID:     1<<63 - 1,
		MinID:     -1 << 63,
		Limit:     20,
	})
	if err != nil {
		t.Fatalf("ListAdminLog iOS initial bounds: %v", err)
	}
	if len(iosInitialLogs.Events) == 0 {
		t.Fatalf("ListAdminLog iOS initial bounds returned no events, want admin history")
	}
	seen := map[domain.ChannelAdminLogEventType]bool{}
	for _, event := range logs.Events {
		seen[event.Type] = true
	}
	for _, typ := range []domain.ChannelAdminLogEventType{
		domain.ChannelAdminLogParticipantPromote,
		domain.ChannelAdminLogChangeTitle,
		domain.ChannelAdminLogUpdatePinned,
		domain.ChannelAdminLogParticipantInvite,
		domain.ChannelAdminLogParticipantJoin,
		domain.ChannelAdminLogToggleForum,
		domain.ChannelAdminLogToggleAntiSpam,
	} {
		if !seen[typ] {
			t.Fatalf("admin logs missing %s in %+v", typ, logs.Events)
		}
	}
	pinnedOnly, err := service.ListAdminLog(ctx, 1001, domain.ChannelAdminLogRequest{
		ChannelID: created.Channel.ID,
		Limit:     10,
		Filter:    domain.ChannelAdminLogFilter{Pinned: true},
	})
	if err != nil {
		t.Fatalf("ListAdminLog pinned: %v", err)
	}
	if len(pinnedOnly.Events) != 1 || pinnedOnly.Events[0].Type != domain.ChannelAdminLogUpdatePinned || pinnedOnly.Events[0].Message == nil {
		t.Fatalf("pinned admin logs = %+v, want one update_pinned with message", pinnedOnly.Events)
	}
	if _, err := service.ListAdminLog(ctx, 1003, domain.ChannelAdminLogRequest{ChannelID: created.Channel.ID, Limit: 10}); !errors.Is(err, domain.ErrChannelAdminRequired) {
		t.Fatalf("non-admin ListAdminLog err = %v, want ErrChannelAdminRequired", err)
	}
}

func TestChannelAboutRequiresChangeInfo(t *testing.T) {
	ctx := context.Background()
	service := NewService(memory.NewChannelStore())
	created, err := service.CreateMegagroupFromCreateChat(ctx, 1001, domain.CreateChannelRequest{
		Title:         "Team",
		MemberUserIDs: []int64{1002},
		Date:          10,
	})
	if err != nil {
		t.Fatalf("CreateMegagroupFromCreateChat: %v", err)
	}

	if _, err := service.EditAbout(ctx, 1002, domain.EditChannelAboutRequest{
		ChannelID: created.Channel.ID,
		About:     "member cannot edit",
		Date:      11,
	}); !errors.Is(err, domain.ErrChannelAdminRequired) {
		t.Fatalf("EditAbout by member err = %v, want ErrChannelAdminRequired", err)
	}

	updated, err := service.EditAbout(ctx, 1001, domain.EditChannelAboutRequest{
		ChannelID: created.Channel.ID,
		About:     "owner about",
		Date:      12,
	})
	if err != nil {
		t.Fatalf("EditAbout by owner: %v", err)
	}
	if updated.About != "owner about" {
		t.Fatalf("updated about = %q, want owner about", updated.About)
	}
	view, err := service.GetChannel(ctx, 1002, created.Channel.ID)
	if err != nil {
		t.Fatalf("GetChannel by member: %v", err)
	}
	if view.Channel.About != "owner about" {
		t.Fatalf("member view about = %q, want owner about", view.Channel.About)
	}

	if _, err := service.EditAdmin(ctx, 1001, domain.EditChannelAdminRequest{
		ChannelID: created.Channel.ID,
		MemberID:  1002,
		AdminRights: domain.ChannelAdminRights{
			ChangeInfo: true,
		},
		Date: 13,
	}); err != nil {
		t.Fatalf("EditAdmin: %v", err)
	}
	updated, err = service.EditAbout(ctx, 1002, domain.EditChannelAboutRequest{
		ChannelID: created.Channel.ID,
		About:     "admin about",
		Date:      14,
	})
	if err != nil {
		t.Fatalf("EditAbout by change_info admin: %v", err)
	}
	if updated.About != "admin about" {
		t.Fatalf("updated about = %q, want admin about", updated.About)
	}
}

func TestChannelBanAndDeletePermissions(t *testing.T) {
	ctx := context.Background()
	service := NewService(memory.NewChannelStore())
	created, err := service.CreateMegagroupFromCreateChat(ctx, 1001, domain.CreateChannelRequest{
		Title:         "Team",
		MemberUserIDs: []int64{1002},
		Date:          10,
	})
	if err != nil {
		t.Fatalf("CreateMegagroupFromCreateChat: %v", err)
	}
	ptsBeforeBan := created.Channel.Pts
	if _, err := service.DeleteChannel(ctx, 1002, domain.DeleteChannelRequest{ChannelID: created.Channel.ID, Date: 11}); !errors.Is(err, domain.ErrChannelAdminRequired) {
		t.Fatalf("member DeleteChannel err = %v, want ErrChannelAdminRequired", err)
	}
	banned, err := service.EditBanned(ctx, 1001, domain.EditChannelBannedRequest{
		ChannelID:   created.Channel.ID,
		Participant: domain.Peer{Type: domain.PeerTypeUser, ID: 1002},
		BannedRights: domain.ChannelBannedRights{
			ViewMessages: true,
			UntilDate:    100,
		},
		Date: 12,
	})
	if err != nil {
		t.Fatalf("EditBanned: %v", err)
	}
	if banned.Participant.Status != domain.ChannelMemberKicked || banned.Channel.ParticipantsCount != 1 || banned.Channel.KickedCount != 1 {
		t.Fatalf("banned = %+v, want kicked participant and counts", banned)
	}
	// megagroup 踢人产生 "X removed Y" 服务消息并占一个 channel pts；
	// participant update 自身仍是 transient。
	if banned.Channel.Pts != ptsBeforeBan+1 || banned.ServiceEvent.Pts != ptsBeforeBan+1 {
		t.Fatalf("banned channel pts = %d service %d, want kick service message at %d", banned.Channel.Pts, banned.ServiceEvent.Pts, ptsBeforeBan+1)
	}
	if banned.Message.Action == nil || banned.Message.Action.Type != domain.ChannelActionChatDelete {
		t.Fatalf("kick service message = %+v, want ChatDelete action", banned.Message)
	}
	if banned.Event.Type != domain.ChannelUpdateParticipant || banned.Event.Participant.Status != domain.ChannelMemberKicked || banned.Event.Pts != 0 || banned.Event.PtsCount != 0 {
		t.Fatalf("ban participant event = %+v, want transient kicked transition", banned.Event)
	}
	kicked, err := service.GetParticipants(ctx, 1001, created.Channel.ID, domain.ChannelParticipantsFilter{Kind: domain.ChannelParticipantsKicked}, 0, 10)
	if err != nil {
		t.Fatalf("GetParticipants kicked: %v", err)
	}
	if len(kicked.Participants) != 1 || kicked.Participants[0].UserID != 1002 || kicked.Participants[0].InviterUserID != 1001 {
		t.Fatalf("kicked participants = %+v, want kicked user with actor as inviter/kicked_by", kicked.Participants)
	}
	hidden, err := service.GetParticipants(ctx, 1002, created.Channel.ID, domain.ChannelParticipantsFilter{Kind: domain.ChannelParticipantsKicked}, 0, 10)
	if !errors.Is(err, domain.ErrChannelUserBanned) && (err != nil || len(hidden.Participants) != 0) {
		t.Fatalf("banned viewer kicked participants = %+v err=%v, want no access", hidden.Participants, err)
	}
	if _, err := service.GetHistory(ctx, 1002, domain.ChannelHistoryFilter{ChannelID: created.Channel.ID, Limit: 10}); !errors.Is(err, domain.ErrChannelUserBanned) {
		t.Fatalf("banned GetHistory err = %v, want ErrChannelUserBanned", err)
	}
	if _, err := service.JoinChannel(ctx, 1002, created.Channel.ID, 13); !errors.Is(err, domain.ErrChannelUserBanned) {
		t.Fatalf("kicked JoinChannel err = %v, want ErrChannelUserBanned", err)
	}
	deleted, err := service.DeleteChannel(ctx, 1001, domain.DeleteChannelRequest{ChannelID: created.Channel.ID, Date: 13})
	if err != nil {
		t.Fatalf("creator DeleteChannel: %v", err)
	}
	if !deleted.Channel.Deleted {
		t.Fatalf("deleted = %+v, want deleted channel", deleted)
	}
}

func TestChannelInviteCannotBypassKickedMemberWithoutBanRight(t *testing.T) {
	ctx := context.Background()
	service := NewService(memory.NewChannelStore())
	created, err := service.CreateMegagroupFromCreateChat(ctx, 1001, domain.CreateChannelRequest{
		Title:         "Invite Kicked",
		MemberUserIDs: []int64{1002, 1003},
		Date:          10,
	})
	if err != nil {
		t.Fatalf("CreateMegagroupFromCreateChat: %v", err)
	}
	if _, err := service.EditBanned(ctx, 1001, domain.EditChannelBannedRequest{
		ChannelID:   created.Channel.ID,
		Participant: domain.Peer{Type: domain.PeerTypeUser, ID: 1002},
		BannedRights: domain.ChannelBannedRights{
			ViewMessages: true,
			UntilDate:    100,
		},
		Date: 11,
	}); err != nil {
		t.Fatalf("EditBanned: %v", err)
	}
	if _, err := service.InviteToChannel(ctx, 1003, created.Channel.ID, []int64{1002}, 12); !errors.Is(err, domain.ErrUserKicked) {
		t.Fatalf("member InviteToChannel kicked err = %v, want ErrUserKicked", err)
	}
	restored, err := service.InviteToChannel(ctx, 1001, created.Channel.ID, []int64{1002}, 13)
	if err != nil {
		t.Fatalf("creator InviteToChannel kicked: %v", err)
	}
	if len(restored.Members) != 1 || restored.Members[0].Status != domain.ChannelMemberActive || restored.Members[0].BannedRights != (domain.ChannelBannedRights{}) {
		t.Fatalf("restored members = %+v, want active unbanned member", restored.Members)
	}
	if restored.Channel.ParticipantsCount != 3 || restored.Channel.KickedCount != 0 {
		t.Fatalf("restored counts = participants:%d kicked:%d, want 3/0", restored.Channel.ParticipantsCount, restored.Channel.KickedCount)
	}
	if _, err := service.InviteToChannel(ctx, 1001, created.Channel.ID, []int64{1002}, 14); !errors.Is(err, domain.ErrUserAlreadyParticipant) {
		t.Fatalf("duplicate InviteToChannel err = %v, want ErrUserAlreadyParticipant", err)
	}
}

func TestChannelLeaveAndRejoinRestoresParticipantCountAndNotifiesLeaver(t *testing.T) {
	ctx := context.Background()
	service := NewService(memory.NewChannelStore())
	created, err := service.CreateMegagroupFromCreateChat(ctx, 1001, domain.CreateChannelRequest{
		Title:         "Leave Rejoin",
		MemberUserIDs: []int64{1002},
		Date:          10,
	})
	if err != nil {
		t.Fatalf("CreateMegagroupFromCreateChat: %v", err)
	}
	left, err := service.LeaveChannel(ctx, 1002, created.Channel.ID, 11)
	if err != nil {
		t.Fatalf("LeaveChannel: %v", err)
	}
	if left.Members[0].Status != domain.ChannelMemberLeft || left.Channel.ParticipantsCount != 1 {
		t.Fatalf("left result = %+v, want left member and participants=1", left)
	}
	hasLeaverRecipient := false
	for _, id := range left.Recipients {
		if id == 1002 {
			hasLeaverRecipient = true
			break
		}
	}
	if !hasLeaverRecipient {
		t.Fatalf("leave recipients = %+v, want leaver included for other sessions", left.Recipients)
	}
	rejoined, err := service.JoinChannel(ctx, 1002, created.Channel.ID, 12)
	if err != nil {
		t.Fatalf("JoinChannel after leave: %v", err)
	}
	if rejoined.Members[0].Status != domain.ChannelMemberActive || rejoined.Channel.ParticipantsCount != 2 {
		t.Fatalf("rejoined result = %+v, want active member and participants=2", rejoined)
	}
	if _, err := service.JoinChannel(ctx, 1002, created.Channel.ID, 13); !errors.Is(err, domain.ErrUserAlreadyParticipant) {
		t.Fatalf("duplicate JoinChannel err = %v, want ErrUserAlreadyParticipant", err)
	}
}

func TestChannelUsernameAndSignatures(t *testing.T) {
	ctx := context.Background()
	service := NewService(memory.NewChannelStore())
	created, err := service.CreateMegagroupFromCreateChat(ctx, 1001, domain.CreateChannelRequest{
		Title:         "Team",
		MemberUserIDs: []int64{1002},
		Date:          10,
	})
	if err != nil {
		t.Fatalf("CreateMegagroupFromCreateChat: %v", err)
	}
	if ok, err := service.CheckUsername(ctx, 1001, created.Channel.ID, "team_public"); err != nil || !ok {
		t.Fatalf("CheckUsername free = ok %v err %v, want true", ok, err)
	}
	public, err := service.UpdateUsername(ctx, 1001, domain.UpdateChannelUsernameRequest{
		ChannelID: created.Channel.ID,
		Username:  "@team_public",
	})
	if err != nil {
		t.Fatalf("UpdateUsername: %v", err)
	}
	if public.Username != "team_public" {
		t.Fatalf("public username = %q, want team_public", public.Username)
	}
	if _, err := service.UpdateUsername(ctx, 1001, domain.UpdateChannelUsernameRequest{ChannelID: created.Channel.ID, Username: "TEAM_PUBLIC"}); !errors.Is(err, domain.ErrChannelNotModified) {
		t.Fatalf("UpdateUsername same username err = %v, want ErrChannelNotModified", err)
	}
	if _, err := service.UpdateUsername(ctx, 1002, domain.UpdateChannelUsernameRequest{ChannelID: created.Channel.ID, Username: "friend_try"}); !errors.Is(err, domain.ErrChannelAdminRequired) {
		t.Fatalf("non-owner UpdateUsername err = %v, want ErrChannelAdminRequired", err)
	}

	other, err := service.CreateMegagroupFromCreateChat(ctx, 1001, domain.CreateChannelRequest{Title: "Other", Date: 11})
	if err != nil {
		t.Fatalf("CreateMegagroupFromCreateChat other: %v", err)
	}
	if ok, err := service.CheckUsername(ctx, 1001, other.Channel.ID, "TEAM_PUBLIC"); err != nil || ok {
		t.Fatalf("CheckUsername occupied = ok %v err %v, want false/nil", ok, err)
	}
	if _, err := service.UpdateUsername(ctx, 1001, domain.UpdateChannelUsernameRequest{ChannelID: other.Channel.ID, Username: "team_public"}); !errors.Is(err, domain.ErrUsernameOccupied) {
		t.Fatalf("UpdateUsername occupied err = %v, want ErrUsernameOccupied", err)
	}
	admined, err := service.ListAdminedPublicChannels(ctx, 1001)
	if err != nil {
		t.Fatalf("ListAdminedPublicChannels: %v", err)
	}
	if len(admined) != 1 || admined[0].ID != created.Channel.ID {
		t.Fatalf("admined public = %+v, want first channel only", admined)
	}

	if _, err := service.EditAdmin(ctx, 1001, domain.EditChannelAdminRequest{
		ChannelID: created.Channel.ID,
		MemberID:  1002,
		AdminRights: domain.ChannelAdminRights{
			ChangeInfo: true,
		},
		Date: 12,
	}); err != nil {
		t.Fatalf("EditAdmin: %v", err)
	}
	signed, err := service.SetSignatures(ctx, 1002, created.Channel.ID, true)
	if err != nil {
		t.Fatalf("SetSignatures by change-info admin: %v", err)
	}
	if !signed.Signatures {
		t.Fatalf("signed channel = %+v, want signatures enabled", signed)
	}
}

func TestListStoryPostableChannelsFiltersPostStoryRights(t *testing.T) {
	ctx := context.Background()
	service := NewService(memory.NewChannelStore())
	userID := int64(1001)
	creatorID := int64(2001)
	created, err := service.CreateChannel(ctx, userID, domain.CreateChannelRequest{
		CreatorUserID: userID,
		Title:         "own private story channel",
		Broadcast:     true,
		Date:          1,
	})
	if err != nil {
		t.Fatalf("CreateChannel own: %v", err)
	}
	postable, err := service.CreateChannel(ctx, creatorID, domain.CreateChannelRequest{
		CreatorUserID: creatorID,
		Title:         "post stories admin",
		Broadcast:     true,
		MemberUserIDs: []int64{userID},
		Date:          2,
	})
	if err != nil {
		t.Fatalf("CreateChannel postable: %v", err)
	}
	if _, err := service.EditAdmin(ctx, creatorID, domain.EditChannelAdminRequest{
		ChannelID: postable.Channel.ID,
		MemberID:  userID,
		AdminRights: domain.ChannelAdminRights{
			PostStories: true,
		},
		Date: 3,
	}); err != nil {
		t.Fatalf("EditAdmin post stories: %v", err)
	}
	editOnly, err := service.CreateChannel(ctx, creatorID, domain.CreateChannelRequest{
		CreatorUserID: creatorID,
		Title:         "edit stories only",
		Broadcast:     true,
		MemberUserIDs: []int64{userID},
		Date:          4,
	})
	if err != nil {
		t.Fatalf("CreateChannel edit-only: %v", err)
	}
	if _, err := service.EditAdmin(ctx, creatorID, domain.EditChannelAdminRequest{
		ChannelID: editOnly.Channel.ID,
		MemberID:  userID,
		AdminRights: domain.ChannelAdminRights{
			EditStories: true,
		},
		Date: 5,
	}); err != nil {
		t.Fatalf("EditAdmin edit stories: %v", err)
	}
	memberOnly, err := service.CreateChannel(ctx, creatorID, domain.CreateChannelRequest{
		CreatorUserID: creatorID,
		Title:         "member story channel",
		Broadcast:     true,
		MemberUserIDs: []int64{userID},
		Date:          6,
	})
	if err != nil {
		t.Fatalf("CreateChannel member-only: %v", err)
	}

	list, err := service.ListStoryPostableChannels(ctx, userID)
	if err != nil {
		t.Fatalf("ListStoryPostableChannels: %v", err)
	}
	got := make([]int64, 0, len(list))
	for _, channel := range list {
		got = append(got, channel.ID)
	}
	want := []int64{postable.Channel.ID, created.Channel.ID}
	if len(got) != len(want) {
		t.Fatalf("story postable channel ids = %v, want %v; excluded edit-only=%d member-only=%d", got, want, editOnly.Channel.ID, memberOnly.Channel.ID)
	}
	if got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("story postable channel ids = %v, want %v; excluded edit-only=%d member-only=%d", got, want, editOnly.Channel.ID, memberOnly.Channel.ID)
	}
}

func TestListSendAsChannelsFiltersPostMessageRights(t *testing.T) {
	ctx := context.Background()
	service := NewService(memory.NewChannelStore())
	userID := int64(1001)
	creatorID := int64(2001)

	// Broadcast channel the user created → eligible.
	owned, err := service.CreateChannel(ctx, userID, domain.CreateChannelRequest{
		CreatorUserID: userID,
		Title:         "own broadcast",
		Broadcast:     true,
		Date:          1,
	})
	if err != nil {
		t.Fatalf("CreateChannel owned: %v", err)
	}
	// Broadcast channel where the user is an admin holding PostMessages → eligible.
	postAdmin, err := service.CreateChannel(ctx, creatorID, domain.CreateChannelRequest{
		CreatorUserID: creatorID,
		Title:         "post admin broadcast",
		Broadcast:     true,
		MemberUserIDs: []int64{userID},
		Date:          2,
	})
	if err != nil {
		t.Fatalf("CreateChannel post-admin: %v", err)
	}
	if _, err := service.EditAdmin(ctx, creatorID, domain.EditChannelAdminRequest{
		ChannelID:   postAdmin.Channel.ID,
		MemberID:    userID,
		AdminRights: domain.ChannelAdminRights{PostMessages: true},
		Date:        3,
	}); err != nil {
		t.Fatalf("EditAdmin post messages: %v", err)
	}
	// Broadcast channel where the user is an admin WITHOUT PostMessages → excluded.
	editAdmin, err := service.CreateChannel(ctx, creatorID, domain.CreateChannelRequest{
		CreatorUserID: creatorID,
		Title:         "edit admin broadcast",
		Broadcast:     true,
		MemberUserIDs: []int64{userID},
		Date:          4,
	})
	if err != nil {
		t.Fatalf("CreateChannel edit-admin: %v", err)
	}
	if _, err := service.EditAdmin(ctx, creatorID, domain.EditChannelAdminRequest{
		ChannelID:   editAdmin.Channel.ID,
		MemberID:    userID,
		AdminRights: domain.ChannelAdminRights{EditMessages: true, DeleteMessages: true},
		Date:        5,
	}); err != nil {
		t.Fatalf("EditAdmin edit messages: %v", err)
	}
	// Megagroup the user created → excluded (sending as an owned megagroup is not a real capability).
	megagroup, err := service.CreateChannel(ctx, userID, domain.CreateChannelRequest{
		CreatorUserID: userID,
		Title:         "own megagroup",
		Megagroup:     true,
		Date:          6,
	})
	if err != nil {
		t.Fatalf("CreateChannel megagroup: %v", err)
	}
	// Broadcast channel the user is only a member of → excluded.
	memberOnly, err := service.CreateChannel(ctx, creatorID, domain.CreateChannelRequest{
		CreatorUserID: creatorID,
		Title:         "member broadcast",
		Broadcast:     true,
		MemberUserIDs: []int64{userID},
		Date:          7,
	})
	if err != nil {
		t.Fatalf("CreateChannel member-only: %v", err)
	}

	list, err := service.ListSendAsChannels(ctx, userID)
	if err != nil {
		t.Fatalf("ListSendAsChannels: %v", err)
	}
	got := make(map[int64]bool, len(list))
	for _, channel := range list {
		got[channel.ID] = true
	}
	if !got[owned.Channel.ID] {
		t.Fatalf("send-as channels %v missing creator-owned broadcast %d", got, owned.Channel.ID)
	}
	if !got[postAdmin.Channel.ID] {
		t.Fatalf("send-as channels %v missing post-admin broadcast %d", got, postAdmin.Channel.ID)
	}
	if got[editAdmin.Channel.ID] {
		t.Fatalf("send-as channels %v should exclude admin-without-post %d", got, editAdmin.Channel.ID)
	}
	if got[megagroup.Channel.ID] {
		t.Fatalf("send-as channels %v should exclude owned megagroup %d", got, megagroup.Channel.ID)
	}
	if got[memberOnly.Channel.ID] {
		t.Fatalf("send-as channels %v should exclude member-only broadcast %d", got, memberOnly.Channel.ID)
	}
	if len(list) != 2 {
		t.Fatalf("send-as channels = %d entries, want 2 (owned + post-admin)", len(list))
	}
}

func TestPublicChannelSearchAndResolveUsername(t *testing.T) {
	ctx := context.Background()
	service := NewService(memory.NewChannelStore())
	created, err := service.CreateMegagroupFromCreateChat(ctx, 1001, domain.CreateChannelRequest{
		Title:         "CU Public Lab",
		MemberUserIDs: []int64{1002},
		Date:          20,
	})
	if err != nil {
		t.Fatalf("CreateMegagroupFromCreateChat: %v", err)
	}
	public, err := service.UpdateUsername(ctx, 1001, domain.UpdateChannelUsernameRequest{
		ChannelID: created.Channel.ID,
		Username:  "cu_public_lab",
	})
	if err != nil {
		t.Fatalf("UpdateUsername: %v", err)
	}
	if _, err := service.CreateMegagroupFromCreateChat(ctx, 1001, domain.CreateChannelRequest{
		Title: "CU Private Lab",
		Date:  21,
	}); err != nil {
		t.Fatalf("CreateMegagroupFromCreateChat private: %v", err)
	}

	joined, err := service.SearchPublicChannels(ctx, 1002, "CU Public", 10)
	if err != nil {
		t.Fatalf("SearchPublicChannels joined: %v", err)
	}
	if len(joined.MyResults) != 0 || len(joined.Results) != 0 {
		t.Fatalf("joined public search = %+v, want no discovery result for active member", joined)
	}
	global, err := service.SearchPublicChannels(ctx, 1003, "public", 10)
	if err != nil {
		t.Fatalf("SearchPublicChannels global: %v", err)
	}
	if len(global.Results) != 1 || global.Results[0].ID != public.ID || len(global.MyResults) != 0 {
		t.Fatalf("global public search = %+v, want public channel result", global)
	}
	resolved, found, err := service.ResolvePublicUsername(ctx, 1003, "@CU_PUBLIC_LAB")
	if err != nil || !found || resolved.ID != public.ID {
		t.Fatalf("ResolvePublicUsername = %+v found %v err %v, want public channel", resolved, found, err)
	}
}

func TestPublicChannelPreviewAllowsNonMemberHistory(t *testing.T) {
	ctx := context.Background()
	service := NewService(memory.NewChannelStore())
	const (
		ownerID  = 1001
		viewerID = 1002
	)
	created, err := service.CreateChannel(ctx, ownerID, domain.CreateChannelRequest{
		Title:     "Public Preview",
		Broadcast: true,
		Date:      10,
	})
	if err != nil {
		t.Fatalf("CreateChannel: %v", err)
	}
	public, err := service.UpdateUsername(ctx, ownerID, domain.UpdateChannelUsernameRequest{
		UserID:    ownerID,
		ChannelID: created.Channel.ID,
		Username:  "public_preview",
	})
	if err != nil {
		t.Fatalf("UpdateUsername: %v", err)
	}
	sent, err := service.SendMessage(ctx, ownerID, domain.SendChannelMessageRequest{
		ChannelID: public.ID,
		RandomID:  101,
		Message:   "public preview post",
		Date:      20,
	})
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	view, err := service.GetChannel(ctx, viewerID, public.ID)
	if err != nil {
		t.Fatalf("non-member GetChannel public preview: %v", err)
	}
	if view.Self.Status != domain.ChannelMemberLeft || view.Self.UserID != viewerID {
		t.Fatalf("preview self = %+v, want synthetic left member for viewer", view.Self)
	}
	if view.Dialog.UnreadCount != 0 || view.Dialog.ReadInboxMaxID < public.TopMessageID {
		t.Fatalf("preview dialog = %+v, want no unread count", view.Dialog)
	}
	history, err := service.GetHistory(ctx, viewerID, domain.ChannelHistoryFilter{ChannelID: public.ID, Limit: 10})
	if err != nil {
		t.Fatalf("non-member GetHistory public preview: %v", err)
	}
	if history.Self.Status != domain.ChannelMemberLeft || history.Self.UserID != viewerID {
		t.Fatalf("history self = %+v, want synthetic left member for viewer", history.Self)
	}
	foundPost := false
	for _, msg := range history.Messages {
		if msg.Body == "public preview post" {
			foundPost = true
		}
	}
	if !foundPost {
		t.Fatalf("history messages = %+v, want public preview post", history.Messages)
	}
	diff, err := service.GetDifference(ctx, viewerID, domain.ChannelDifferenceRequest{
		ChannelID: public.ID,
		Pts:       created.Event.Pts,
		Limit:     10,
	})
	if err != nil {
		t.Fatalf("non-member GetDifference public preview: %v", err)
	}
	if !diff.Final || diff.Pts != sent.Event.Pts || len(diff.Events) != 0 || len(diff.NewMessages) != 0 || len(diff.OtherUpdates) != 0 {
		t.Fatalf("preview diff = %+v, want empty public preview difference at current pts", diff)
	}
	if diff.Dialog.UnreadCount != 0 || diff.Dialog.ReadInboxMaxID < sent.Message.ID {
		t.Fatalf("preview diff dialog = %+v, want read-only public preview dialog", diff.Dialog)
	}

	private, err := service.CreateChannel(ctx, ownerID, domain.CreateChannelRequest{
		Title:     "Private Preview",
		Broadcast: true,
		Date:      30,
	})
	if err != nil {
		t.Fatalf("CreateChannel private: %v", err)
	}
	if _, err := service.GetChannel(ctx, viewerID, private.Channel.ID); !errors.Is(err, domain.ErrChannelPrivate) {
		t.Fatalf("non-member private GetChannel err = %v, want ErrChannelPrivate", err)
	}
	if _, err := service.EditBanned(ctx, ownerID, domain.EditChannelBannedRequest{
		UserID:       ownerID,
		ChannelID:    public.ID,
		Participant:  domain.Peer{Type: domain.PeerTypeUser, ID: viewerID},
		BannedRights: domain.ChannelBannedRights{ViewMessages: true},
		Date:         40,
	}); err != nil {
		t.Fatalf("EditBanned public viewer: %v", err)
	}
	if _, err := service.GetHistory(ctx, viewerID, domain.ChannelHistoryFilter{ChannelID: public.ID, Limit: 10}); !errors.Is(err, domain.ErrChannelUserBanned) {
		t.Fatalf("banned public preview GetHistory err = %v, want ErrChannelUserBanned", err)
	}
	if _, err := service.GetDifference(ctx, viewerID, domain.ChannelDifferenceRequest{ChannelID: public.ID, Pts: created.Event.Pts, Limit: 10}); !errors.Is(err, domain.ErrChannelUserBanned) {
		t.Fatalf("banned public preview GetDifference err = %v, want ErrChannelUserBanned", err)
	}
}

func TestChannelDifferenceStartsAtMemberAvailableMinPts(t *testing.T) {
	ctx := context.Background()
	service := NewService(memory.NewChannelStore())
	created, err := service.CreateMegagroupFromCreateChat(ctx, 1001, domain.CreateChannelRequest{
		Title:         "Visible PTS",
		MemberUserIDs: []int64{1002},
		Date:          10,
	})
	if err != nil {
		t.Fatalf("CreateMegagroupFromCreateChat: %v", err)
	}
	ptsFloor := created.Channel.Pts
	promoted, err := service.EditAdmin(ctx, 1001, domain.EditChannelAdminRequest{
		ChannelID: created.Channel.ID,
		MemberID:  1002,
		AdminRights: domain.ChannelAdminRights{
			InviteUsers: true,
		},
		Date: 11,
	})
	if err != nil {
		t.Fatalf("EditAdmin: %v", err)
	}
	if promoted.Event.Pts != 0 || promoted.Channel.Pts != ptsFloor {
		t.Fatalf("promoted = %+v, want transient admin event and unchanged pts %d", promoted, ptsFloor)
	}
	joined, err := service.JoinChannel(ctx, 1003, created.Channel.ID, 12)
	if err != nil {
		t.Fatalf("JoinChannel: %v", err)
	}
	if joined.Members[0].AvailableMinPts != ptsFloor {
		t.Fatalf("joined available_min_pts = %d, want pre-join channel pts %d", joined.Members[0].AvailableMinPts, ptsFloor)
	}
	diff, err := service.GetDifference(ctx, 1003, domain.ChannelDifferenceRequest{ChannelID: created.Channel.ID, Pts: 0, Limit: 100})
	if err != nil {
		t.Fatalf("GetDifference: %v", err)
	}
	if diff.Pts != joined.Channel.Pts {
		t.Fatalf("diff pts = %d, want current channel pts %d", diff.Pts, joined.Channel.Pts)
	}
	for _, msg := range diff.NewMessages {
		if msg.Pts <= ptsFloor {
			t.Fatalf("diff leaks pre-join message %+v at or before available_min_pts %d", msg, ptsFloor)
		}
	}
	for _, event := range diff.OtherUpdates {
		if event.Pts <= ptsFloor {
			t.Fatalf("diff leaks pre-join event %+v at or before available_min_pts %d", event, ptsFloor)
		}
	}
}

func TestChannelPreHistoryAndSlowMode(t *testing.T) {
	ctx := context.Background()
	service := NewService(memory.NewChannelStore())
	created, err := service.CreateMegagroupFromCreateChat(ctx, 1001, domain.CreateChannelRequest{
		Title:         "Settings Team",
		MemberUserIDs: []int64{1002},
		Date:          10,
	})
	if err != nil {
		t.Fatalf("CreateMegagroupFromCreateChat: %v", err)
	}
	if _, err := service.SetPreHistoryHidden(ctx, 1002, created.Channel.ID, true); !errors.Is(err, domain.ErrChannelAdminRequired) {
		t.Fatalf("member SetPreHistoryHidden err = %v, want ErrChannelAdminRequired", err)
	}
	hidden, err := service.SetPreHistoryHidden(ctx, 1001, created.Channel.ID, true)
	if err != nil {
		t.Fatalf("SetPreHistoryHidden: %v", err)
	}
	if !hidden.PreHistoryHidden {
		t.Fatalf("hidden channel = %+v, want prehistory hidden", hidden)
	}
	hiddenMsg, err := service.SendMessage(ctx, 1001, domain.SendChannelMessageRequest{ChannelID: created.Channel.ID, RandomID: 1, Message: "before new member", Date: 90})
	if err != nil {
		t.Fatalf("owner send before new member: %v", err)
	}
	if _, err := service.JoinChannel(ctx, 1003, created.Channel.ID, 95); err != nil {
		t.Fatalf("new member JoinChannel: %v", err)
	}
	visibleMsg, err := service.SendMessage(ctx, 1001, domain.SendChannelMessageRequest{ChannelID: created.Channel.ID, RandomID: 100, Message: "after new member", Date: 96})
	if err != nil {
		t.Fatalf("owner send after new member: %v", err)
	}
	mixedDelete, err := service.DeleteMessages(ctx, 1001, domain.DeleteChannelMessagesRequest{
		ChannelID: created.Channel.ID,
		IDs:       []int{hiddenMsg.Message.ID, visibleMsg.Message.ID},
		Date:      97,
	})
	if err != nil {
		t.Fatalf("mixed DeleteMessages: %v", err)
	}
	if mixedDelete.Event.PtsCount != 2 {
		t.Fatalf("mixed delete pts_count = %d, want original deleted id count 2", mixedDelete.Event.PtsCount)
	}
	history, err := service.GetHistory(ctx, 1003, domain.ChannelHistoryFilter{ChannelID: created.Channel.ID, Limit: 20})
	if err != nil {
		t.Fatalf("new member GetHistory: %v", err)
	}
	for _, msg := range history.Messages {
		if msg.Body == "before new member" {
			t.Fatalf("new member history includes hidden prehistory message: %+v", history.Messages)
		}
	}
	view, err := service.GetChannel(ctx, 1003, created.Channel.ID)
	if err != nil {
		t.Fatalf("new member GetChannel: %v", err)
	}
	diff, err := service.GetDifference(ctx, 1003, domain.ChannelDifferenceRequest{ChannelID: created.Channel.ID, Pts: 0, Limit: 100})
	if err != nil {
		t.Fatalf("new member GetDifference: %v", err)
	}
	if diff.TooLong {
		t.Fatalf("new member diff unexpectedly too long: %+v", diff)
	}
	if diff.Pts != view.Channel.Pts {
		t.Fatalf("new member diff pts = %d, want current channel pts %d", diff.Pts, view.Channel.Pts)
	}
	for _, msg := range diff.NewMessages {
		if msg.ID <= view.Self.AvailableMinID {
			t.Fatalf("new member diff includes hidden prehistory message id %d <= available_min_id %d", msg.ID, view.Self.AvailableMinID)
		}
	}
	for _, event := range diff.OtherUpdates {
		for _, id := range event.MessageIDs {
			if id <= view.Self.AvailableMinID {
				t.Fatalf("new member diff includes hidden prehistory message id %d in event %+v", id, event)
			}
		}
	}
	foundPartialDelete := false
	for _, event := range diff.OtherUpdates {
		if event.Type != domain.ChannelUpdateDeleteMessages || event.Pts != mixedDelete.Event.Pts {
			continue
		}
		foundPartialDelete = true
		if event.PtsCount != mixedDelete.Event.PtsCount || len(event.MessageIDs) != 1 || event.MessageIDs[0] != visibleMsg.Message.ID {
			t.Fatalf("visible mixed delete event = %+v, want pts_count=%d and only visible id %d", event, mixedDelete.Event.PtsCount, visibleMsg.Message.ID)
		}
	}
	if !foundPartialDelete {
		t.Fatalf("new member diff missing partial mixed delete event at pts %d: %+v", mixedDelete.Event.Pts, diff.OtherUpdates)
	}
	if _, err := service.SetSlowMode(ctx, 1002, created.Channel.ID, 30); !errors.Is(err, domain.ErrChannelAdminRequired) {
		t.Fatalf("member SetSlowMode err = %v, want ErrChannelAdminRequired", err)
	}
	slow, err := service.SetSlowMode(ctx, 1001, created.Channel.ID, 30)
	if err != nil {
		t.Fatalf("SetSlowMode: %v", err)
	}
	if slow.SlowmodeSeconds != 30 {
		t.Fatalf("slow mode = %+v, want 30 seconds", slow)
	}
	if _, err := service.SendMessage(ctx, 1002, domain.SendChannelMessageRequest{ChannelID: created.Channel.ID, RandomID: 2, Message: "first", Date: 100}); err != nil {
		t.Fatalf("first member send: %v", err)
	}
	if _, err := service.SendMessage(ctx, 1002, domain.SendChannelMessageRequest{ChannelID: created.Channel.ID, RandomID: 3, Message: "too soon", Date: 110}); err == nil {
		t.Fatalf("second member send err = nil, want slow mode wait")
	} else if seconds, ok := domain.SlowModeWaitSeconds(err); !ok || seconds != 20 {
		t.Fatalf("second member send err = %v, want slow mode wait 20", err)
	}
	if _, err := service.SendMessage(ctx, 1002, domain.SendChannelMessageRequest{ChannelID: created.Channel.ID, RandomID: 4, Message: "after wait", Date: 130}); err != nil {
		t.Fatalf("third member send after slow mode: %v", err)
	}
	if _, err := service.SendMessage(ctx, 1001, domain.SendChannelMessageRequest{ChannelID: created.Channel.ID, RandomID: 5, Message: "owner one", Date: 131}); err != nil {
		t.Fatalf("owner send with slow mode: %v", err)
	}
	if _, err := service.SendMessage(ctx, 1001, domain.SendChannelMessageRequest{ChannelID: created.Channel.ID, RandomID: 6, Message: "owner two", Date: 132}); err != nil {
		t.Fatalf("owner second send with slow mode: %v", err)
	}
}

func TestImportInviteRespectsPreHistoryHidden(t *testing.T) {
	ctx := context.Background()
	service := NewService(memory.NewChannelStore())
	created, err := service.CreateChannel(ctx, 1001, domain.CreateChannelRequest{
		Title:     "Private Invite",
		Megagroup: true,
		Date:      10,
	})
	if err != nil {
		t.Fatalf("CreateChannel: %v", err)
	}
	if _, err := service.SetPreHistoryHidden(ctx, 1001, created.Channel.ID, true); err != nil {
		t.Fatalf("SetPreHistoryHidden: %v", err)
	}
	hiddenMsg, err := service.SendMessage(ctx, 1001, domain.SendChannelMessageRequest{
		ChannelID: created.Channel.ID,
		RandomID:  501,
		Message:   "hidden before invite link",
		Date:      11,
	})
	if err != nil {
		t.Fatalf("SendMessage hidden: %v", err)
	}
	invite, err := service.ExportInvite(ctx, 1001, domain.ExportChannelInviteRequest{
		ChannelID: created.Channel.ID,
		Title:     "join",
		Date:      12,
	})
	if err != nil {
		t.Fatalf("ExportInvite: %v", err)
	}
	joined, err := service.ImportInvite(ctx, 1002, domain.ImportChannelInviteRequest{
		Hash: invite.Invite.Hash,
		Date: 13,
	})
	if err != nil {
		t.Fatalf("ImportInvite: %v", err)
	}
	if joined.Members[0].AvailableMinID != hiddenMsg.Message.ID || joined.Members[0].ReadInboxMaxID != joined.Message.ID {
		t.Fatalf("imported member watermarks = %+v, want hidden top %d and read at join service %d", joined.Members[0], hiddenMsg.Message.ID, joined.Message.ID)
	}
	diff, err := service.GetDifference(ctx, 1002, domain.ChannelDifferenceRequest{
		ChannelID: created.Channel.ID,
		Pts:       0,
		Limit:     100,
	})
	if err != nil {
		t.Fatalf("GetDifference: %v", err)
	}
	for _, msg := range diff.NewMessages {
		if msg.ID <= hiddenMsg.Message.ID {
			t.Fatalf("diff includes hidden message id %d <= available_min_id %d", msg.ID, hiddenMsg.Message.ID)
		}
	}
}

func TestImportInviteInitialReadWatermarkSkipsExistingHistory(t *testing.T) {
	ctx := context.Background()
	service := NewService(memory.NewChannelStore())
	created, err := service.CreateMegagroupFromCreateChat(ctx, 1001, domain.CreateChannelRequest{
		Title: "Import Watermark",
		Date:  10,
	})
	if err != nil {
		t.Fatalf("CreateMegagroupFromCreateChat: %v", err)
	}
	first, err := service.SendMessage(ctx, 1001, domain.SendChannelMessageRequest{
		ChannelID: created.Channel.ID,
		RandomID:  1,
		Message:   "before import",
		Date:      11,
	})
	if err != nil {
		t.Fatalf("send existing message: %v", err)
	}
	invite, err := service.ExportInvite(ctx, 1001, domain.ExportChannelInviteRequest{
		ChannelID: created.Channel.ID,
		Title:     "join",
		Date:      12,
	})
	if err != nil {
		t.Fatalf("ExportInvite: %v", err)
	}
	joined, err := service.ImportInvite(ctx, 1002, domain.ImportChannelInviteRequest{
		Hash: invite.Invite.Hash,
		Date: 13,
	})
	if err != nil {
		t.Fatalf("ImportInvite: %v", err)
	}
	if joined.Members[0].ReadInboxMaxID != joined.Message.ID || joined.Members[0].ReadOutboxMaxID != joined.Message.ID {
		t.Fatalf("joined member read watermarks = %+v message=%+v, want self join service read/outbox", joined.Members[0], joined.Message)
	}
	view, err := service.GetChannel(ctx, 1002, created.Channel.ID)
	if err != nil {
		t.Fatalf("GetChannel imported: %v", err)
	}
	if view.Dialog.UnreadCount != 0 || view.Self.ReadInboxMaxID != joined.Message.ID {
		t.Fatalf("imported dialog/self = %+v / %+v, want no unread and read at join service", view.Dialog, view.Self)
	}
	readers, err := service.GetMessageReadParticipants(ctx, 1001, domain.ChannelReadParticipantsRequest{
		ChannelID: created.Channel.ID,
		MessageID: first.Message.ID,
		Limit:     domain.MaxChannelReadParticipants,
		Date:      14,
	})
	if err != nil {
		t.Fatalf("GetMessageReadParticipants existing message: %v", err)
	}
	if len(readers.Participants) != 0 {
		t.Fatalf("existing message readers after import = %+v, want none from initial watermark", readers.Participants)
	}
	future, err := service.SendMessage(ctx, 1001, domain.SendChannelMessageRequest{
		ChannelID: created.Channel.ID,
		RandomID:  2,
		Message:   "after import",
		Date:      14,
	})
	if err != nil {
		t.Fatalf("send future message: %v", err)
	}
	after, err := service.GetChannel(ctx, 1002, created.Channel.ID)
	if err != nil {
		t.Fatalf("GetChannel after future: %v", err)
	}
	if after.Dialog.TopMessageID != future.Message.ID || after.Dialog.UnreadCount != 1 {
		t.Fatalf("imported dialog after future = %+v, want top %d unread 1", after.Dialog, future.Message.ID)
	}
}

func TestImportInviteRequestNeededAndUsageLimitErrors(t *testing.T) {
	ctx := context.Background()
	service := NewService(memory.NewChannelStore())
	created, err := service.CreateMegagroupFromCreateChat(ctx, 1001, domain.CreateChannelRequest{
		Title: "Invite Errors",
		Date:  10,
	})
	if err != nil {
		t.Fatalf("CreateMegagroupFromCreateChat: %v", err)
	}
	requested, err := service.ExportInvite(ctx, 1001, domain.ExportChannelInviteRequest{
		ChannelID:     created.Channel.ID,
		Title:         "approval",
		RequestNeeded: true,
		Date:          11,
	})
	if err != nil {
		t.Fatalf("ExportInvite request needed: %v", err)
	}
	if _, err := service.ImportInvite(ctx, 1002, domain.ImportChannelInviteRequest{Hash: requested.Invite.Hash, Date: 12}); !errors.Is(err, domain.ErrInviteRequestSent) {
		t.Fatalf("ImportInvite request-needed err = %v, want ErrInviteRequestSent", err)
	}
	limited, err := service.ExportInvite(ctx, 1001, domain.ExportChannelInviteRequest{
		ChannelID:  created.Channel.ID,
		Title:      "one",
		UsageLimit: 1,
		Date:       13,
	})
	if err != nil {
		t.Fatalf("ExportInvite limited: %v", err)
	}
	if _, err := service.ImportInvite(ctx, 1002, domain.ImportChannelInviteRequest{Hash: limited.Invite.Hash, Date: 14}); err != nil {
		t.Fatalf("ImportInvite first limited: %v", err)
	}
	if _, err := service.ImportInvite(ctx, 1003, domain.ImportChannelInviteRequest{Hash: limited.Invite.Hash, Date: 15}); !errors.Is(err, domain.ErrUsersTooMuch) {
		t.Fatalf("ImportInvite usage-limit err = %v, want ErrUsersTooMuch", err)
	}
}

func TestInviteInitialReadWatermarkSkipsExistingHistory(t *testing.T) {
	ctx := context.Background()
	service := NewService(memory.NewChannelStore())
	created, err := service.CreateMegagroupFromCreateChat(ctx, 1001, domain.CreateChannelRequest{
		Title: "Invite Watermark",
		Date:  10,
	})
	if err != nil {
		t.Fatalf("CreateMegagroupFromCreateChat: %v", err)
	}
	first, err := service.SendMessage(ctx, 1001, domain.SendChannelMessageRequest{
		ChannelID: created.Channel.ID,
		RandomID:  1,
		Message:   "already there",
		Date:      11,
	})
	if err != nil {
		t.Fatalf("send existing message: %v", err)
	}
	if _, err := service.InviteToChannel(ctx, 1001, created.Channel.ID, []int64{1002}, 12); err != nil {
		t.Fatalf("InviteToChannel: %v", err)
	}
	view, err := service.GetChannel(ctx, 1002, created.Channel.ID)
	if err != nil {
		t.Fatalf("GetChannel invited: %v", err)
	}
	if view.Self.ReadInboxMaxID != first.Message.ID || view.Dialog.ReadInboxMaxID != first.Message.ID {
		t.Fatalf("invited read watermark self/dialog = %d/%d, want existing top %d", view.Self.ReadInboxMaxID, view.Dialog.ReadInboxMaxID, first.Message.ID)
	}
	if view.Dialog.UnreadCount != 1 {
		t.Fatalf("invited unread = %d, want only invite service message unread", view.Dialog.UnreadCount)
	}
	readers, err := service.GetMessageReadParticipants(ctx, 1001, domain.ChannelReadParticipantsRequest{
		ChannelID: created.Channel.ID,
		MessageID: first.Message.ID,
		Limit:     domain.MaxChannelReadParticipants,
		Date:      13,
	})
	if err != nil {
		t.Fatalf("GetMessageReadParticipants existing message: %v", err)
	}
	if len(readers.Participants) != 0 {
		t.Fatalf("existing message readers after invite = %+v, want none from initial watermark", readers.Participants)
	}
}

func TestJoinChannelInitialReadWatermarkSkipsExistingHistory(t *testing.T) {
	ctx := context.Background()
	service := NewService(memory.NewChannelStore())
	created, err := service.CreateMegagroupFromCreateChat(ctx, 1001, domain.CreateChannelRequest{
		Title: "Join Watermark",
		Date:  10,
	})
	if err != nil {
		t.Fatalf("CreateMegagroupFromCreateChat: %v", err)
	}
	first, err := service.SendMessage(ctx, 1001, domain.SendChannelMessageRequest{
		ChannelID: created.Channel.ID,
		RandomID:  1,
		Message:   "before join",
		Date:      11,
	})
	if err != nil {
		t.Fatalf("send existing message: %v", err)
	}
	joined, err := service.JoinChannel(ctx, 1002, created.Channel.ID, 12)
	if err != nil {
		t.Fatalf("JoinChannel: %v", err)
	}
	if joined.Members[0].ReadInboxMaxID != joined.Message.ID {
		t.Fatalf("joined read watermark = %d, want self join service %d", joined.Members[0].ReadInboxMaxID, joined.Message.ID)
	}
	view, err := service.GetChannel(ctx, 1002, created.Channel.ID)
	if err != nil {
		t.Fatalf("GetChannel joined: %v", err)
	}
	if view.Dialog.UnreadCount != 0 || view.Self.ReadInboxMaxID != joined.Message.ID {
		t.Fatalf("joined dialog/self = %+v / %+v, want no unread and read at join service", view.Dialog, view.Self)
	}
	readers, err := service.GetMessageReadParticipants(ctx, 1001, domain.ChannelReadParticipantsRequest{
		ChannelID: created.Channel.ID,
		MessageID: first.Message.ID,
		Limit:     domain.MaxChannelReadParticipants,
		Date:      13,
	})
	if err != nil {
		t.Fatalf("GetMessageReadParticipants existing message: %v", err)
	}
	if len(readers.Participants) != 0 {
		t.Fatalf("existing message readers after join = %+v, want none from initial watermark", readers.Participants)
	}
	future, err := service.SendMessage(ctx, 1001, domain.SendChannelMessageRequest{
		ChannelID: created.Channel.ID,
		RandomID:  2,
		Message:   "after join",
		Date:      13,
	})
	if err != nil {
		t.Fatalf("send future message: %v", err)
	}
	after, err := service.GetChannel(ctx, 1002, created.Channel.ID)
	if err != nil {
		t.Fatalf("GetChannel after future: %v", err)
	}
	if after.Dialog.TopMessageID != future.Message.ID || after.Dialog.UnreadCount != 1 {
		t.Fatalf("joined dialog after future = %+v, want top %d unread 1", after.Dialog, future.Message.ID)
	}
}
