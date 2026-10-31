package dialogs

import (
	"context"
	"errors"
	"testing"
	"time"

	appchannels "telesrv/internal/app/channels"
	"telesrv/internal/domain"
	"telesrv/internal/store"
	"telesrv/internal/store/memory"
)

type countingDialogStore struct {
	store.DialogStore
	listByUserCalls          int
	listByPeersCalls         int
	listByPeersBatches       [][]domain.Peer
	listDraftsByPeersCalls   int
	listDraftsByPeersBatches [][]domain.Peer
	listDraftsByPeersErr     error
}

type snapshotDialogStore struct {
	store.DialogStore
	list             domain.DialogList
	snapshotCalls    int
	listByPeersCalls int
	listDraftsCalls  int
	privatePeerCalls int
	onListByPeers    func()
	onListDrafts     func()
}

func (s *snapshotDialogStore) ListAllBuiltinDialogSnapshotHeaders(_ context.Context, _ int64) (domain.DialogList, error) {
	s.snapshotCalls++
	return cloneDialogList(s.list), nil
}

func (s *snapshotDialogStore) ListByPeers(_ context.Context, _ int64, peers []domain.Peer) (domain.DialogList, error) {
	s.listByPeersCalls++
	if s.onListByPeers != nil {
		fn := s.onListByPeers
		s.onListByPeers = nil
		fn()
	}
	wanted := make(map[domain.Peer]struct{}, len(peers))
	for _, peer := range peers {
		wanted[peer] = struct{}{}
	}
	out := domain.DialogList{}
	for _, dialog := range s.list.Dialogs {
		if _, ok := wanted[dialog.Peer]; ok {
			out.Dialogs = append(out.Dialogs, cloneDialog(dialog))
		}
	}
	for _, message := range s.list.Messages {
		if _, ok := wanted[message.Peer]; ok {
			out.Messages = append(out.Messages, cloneMessageForDialogCache(message))
		}
	}
	for _, user := range s.list.Users {
		if _, ok := wanted[domain.Peer{Type: domain.PeerTypeUser, ID: user.ID}]; ok {
			out.Users = append(out.Users, cloneDialogUser(user))
		}
	}
	out.Count = len(out.Dialogs)
	return out, nil
}

func (s *snapshotDialogStore) ListDrafts(ctx context.Context, userID int64, limit int) ([]domain.DialogDraft, error) {
	s.listDraftsCalls++
	drafts, err := s.DialogStore.ListDrafts(ctx, userID, limit)
	if s.onListDrafts != nil {
		fn := s.onListDrafts
		s.onListDrafts = nil
		fn()
	}
	return drafts, err
}

func (s *snapshotDialogStore) ListPrivateDialogPeerIDs(ctx context.Context, userID int64, limit int) ([]int64, error) {
	s.privatePeerCalls++
	return s.DialogStore.(privateDialogPeerIDStore).ListPrivateDialogPeerIDs(ctx, userID, limit)
}

func TestGetDialogsSnapshotReusesOwnerProjectionAcrossCursorPages(t *testing.T) {
	ctx := context.Background()
	const ownerID int64 = 1001
	peers := []domain.Peer{
		{Type: domain.PeerTypeUser, ID: 2003},
		{Type: domain.PeerTypeUser, ID: 2002},
		{Type: domain.PeerTypeUser, ID: 2001},
	}
	base := memory.NewDialogStore()
	snapshots := &snapshotDialogStore{DialogStore: base, list: domain.DialogList{
		Dialogs: []domain.Dialog{
			{Peer: peers[0], TopMessage: 30, TopMessageDate: 300},
			{Peer: peers[1], TopMessage: 20, TopMessageDate: 200},
			{Peer: peers[2], TopMessage: 10, TopMessageDate: 100},
		},
		Messages: []domain.Message{
			{ID: 30, Peer: peers[0], From: peers[0], Date: 300, Body: "first"},
			{ID: 20, Peer: peers[1], From: peers[1], Date: 200, Body: "second"},
			{ID: 10, Peer: peers[2], From: peers[2], Date: 100, Body: "third"},
		},
		Users: []domain.User{{ID: 2003}, {ID: 2002}, {ID: 2001}},
		Count: 3,
		Hash:  77,
	}}
	service := NewService(snapshots)
	first, err := service.GetDialogs(ctx, ownerID, domain.DialogFilter{ExcludePinned: true, Limit: 1})
	if err != nil {
		t.Fatalf("first page: %v", err)
	}
	if len(first.Dialogs) != 1 || first.Dialogs[0].Peer != peers[0] || len(first.Messages) != 1 || len(first.Users) != 1 || first.Count != 3 {
		t.Fatalf("first page = %+v, messages=%d users=%d count=%d", first.Dialogs, len(first.Messages), len(first.Users), first.Count)
	}
	second, err := service.GetDialogs(ctx, ownerID, domain.DialogFilter{
		ExcludePinned: true,
		Limit:         1,
		OffsetDate:    first.Dialogs[0].TopMessageDate,
		OffsetID:      first.Dialogs[0].TopMessage,
		HasOffsetPeer: true,
		OffsetPeer:    first.Dialogs[0].Peer,
	})
	if err != nil {
		t.Fatalf("second page: %v", err)
	}
	if len(second.Dialogs) != 1 || second.Dialogs[0].Peer != peers[1] || len(second.Messages) != 1 || len(second.Users) != 1 || second.Count != 3 {
		t.Fatalf("second page = %+v, messages=%d users=%d count=%d", second.Dialogs, len(second.Messages), len(second.Users), second.Count)
	}
	if snapshots.snapshotCalls != 1 {
		t.Fatalf("snapshot calls = %d, want one owner load across pages", snapshots.snapshotCalls)
	}

	service.InvalidateDialog(ownerID, peers[0])
	if _, err := service.GetDialogs(ctx, ownerID, domain.DialogFilter{ExcludePinned: true, Limit: 1}); err != nil {
		t.Fatalf("reload after owner invalidation: %v", err)
	}
	if snapshots.snapshotCalls != 2 {
		t.Fatalf("snapshot calls after invalidation = %d, want 2", snapshots.snapshotCalls)
	}
}

func TestGetDialogsSnapshotMaterializesDraftsOnceAcrossCursorPages(t *testing.T) {
	ctx := context.Background()
	const ownerID int64 = 1101
	firstPeer := domain.Peer{Type: domain.PeerTypeUser, ID: 2101}
	secondPeer := domain.Peer{Type: domain.PeerTypeUser, ID: 2102}
	base := memory.NewDialogStore()
	if err := base.SaveDraft(ctx, ownerID, domain.DialogDraft{
		Peer: secondPeer, Date: 22, Message: "second-page draft",
	}); err != nil {
		t.Fatal(err)
	}
	snapshots := &snapshotDialogStore{DialogStore: base, list: domain.DialogList{
		Dialogs: []domain.Dialog{
			{Peer: firstPeer, TopMessage: 2, TopMessageDate: 20},
			{Peer: secondPeer, TopMessage: 1, TopMessageDate: 10},
		},
		Users: []domain.User{{ID: firstPeer.ID}, {ID: secondPeer.ID}},
	}}
	service := NewService(snapshots)
	first, err := service.GetDialogs(ctx, ownerID, domain.DialogFilter{ExcludePinned: true, Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.GetDialogs(ctx, ownerID, domain.DialogFilter{
		ExcludePinned: true, Limit: 1,
		OffsetDate: first.Dialogs[0].TopMessageDate, OffsetID: first.Dialogs[0].TopMessage,
		HasOffsetPeer: true, OffsetPeer: first.Dialogs[0].Peer,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Dialogs) != 1 || second.Dialogs[0].Draft == nil || second.Dialogs[0].Draft.Message != "second-page draft" {
		t.Fatalf("second page draft = %+v", second.Dialogs)
	}
	if snapshots.snapshotCalls != 1 || snapshots.listDraftsCalls != 1 {
		t.Fatalf("snapshot/draft loads = %d/%d, want 1/1 across pages", snapshots.snapshotCalls, snapshots.listDraftsCalls)
	}
}

func TestDialogSnapshotChannelDependencyInvalidatesOwnerProjection(t *testing.T) {
	const ownerID int64 = 1001
	channelPeer := domain.Peer{Type: domain.PeerTypeChannel, ID: 77}
	service := NewService(memory.NewDialogStore())
	key, ok := dialogSnapshotKey(ownerID, domain.DialogFilter{ExcludePinned: true})
	if !ok {
		t.Fatal("standard main-folder snapshot key was rejected")
	}
	service.listCache.cache.Store(key, newDialogListSnapshot(domain.DialogList{
		Dialogs:  []domain.Dialog{{Peer: channelPeer, TopMessage: 1, TopMessageDate: 10}},
		Channels: []domain.Channel{{ID: channelPeer.ID, Title: "before"}},
		Count:    1,
	}))
	service.InvalidateDialogListsForChannel(channelPeer.ID)
	if got := service.listCache.cache.Len(); got != 0 {
		t.Fatalf("snapshot cache entries = %d, want channel dependency invalidation", got)
	}
}

func TestDialogSnapshotCacheBoundsAggregateHeaderWeight(t *testing.T) {
	cache := newDialogListSnapshotCache(10, 3, time.Hour)
	first := dialogListSnapshotKey{userID: 1001}
	second := dialogListSnapshotKey{userID: 1002}
	cache.cache.Store(first, newDialogListSnapshot(domain.DialogList{Dialogs: []domain.Dialog{
		{Peer: domain.Peer{Type: domain.PeerTypeChannel, ID: 1}},
		{Peer: domain.Peer{Type: domain.PeerTypeChannel, ID: 2}},
	}}))
	cache.cache.Store(second, newDialogListSnapshot(domain.DialogList{Dialogs: []domain.Dialog{
		{Peer: domain.Peer{Type: domain.PeerTypeChannel, ID: 3}},
		{Peer: domain.Peer{Type: domain.PeerTypeChannel, ID: 4}},
	}}))
	if _, ok := cache.cache.Peek(first); ok {
		t.Fatal("old owner snapshot should be evicted by aggregate header budget")
	}
	if _, ok := cache.cache.Peek(second); !ok {
		t.Fatal("new owner snapshot should remain within aggregate header budget")
	}
}

func TestDialogSnapshotDependencyIndexTracksLRUEvictionAndReplacement(t *testing.T) {
	cache := newDialogListSnapshotCache(1, 10, time.Hour)
	first := dialogListSnapshotKey{userID: 1001}
	second := dialogListSnapshotKey{userID: 1002}
	cache.cache.Store(first, newDialogListSnapshot(domain.DialogList{Dialogs: []domain.Dialog{
		{Peer: domain.Peer{Type: domain.PeerTypeChannel, ID: 7}},
	}}))
	cache.cache.Store(second, newDialogListSnapshot(domain.DialogList{Dialogs: []domain.Dialog{
		{Peer: domain.Peer{Type: domain.PeerTypeChannel, ID: 8}},
	}}))
	cache.indexMu.Lock()
	_, staleEvicted := cache.channelKeys[7]
	_, retained := cache.channelKeys[8]
	cache.indexMu.Unlock()
	if staleEvicted || !retained {
		t.Fatalf("dependency index after LRU eviction: channel7=%v channel8=%v", staleEvicted, retained)
	}
	cache.cache.Store(second, newDialogListSnapshot(domain.DialogList{Dialogs: []domain.Dialog{
		{Peer: domain.Peer{Type: domain.PeerTypeChannel, ID: 9}},
	}}))
	cache.indexMu.Lock()
	_, staleReplaced := cache.channelKeys[8]
	_, replaced := cache.channelKeys[9]
	cache.indexMu.Unlock()
	if staleReplaced || !replaced {
		t.Fatalf("dependency index after replacement: channel8=%v channel9=%v", staleReplaced, replaced)
	}
}

func (s *countingDialogStore) ListByUser(ctx context.Context, userID int64, filter domain.DialogFilter) (domain.DialogList, error) {
	s.listByUserCalls++
	return s.DialogStore.ListByUser(ctx, userID, filter)
}

func (s *countingDialogStore) ListByPeers(ctx context.Context, userID int64, peers []domain.Peer) (domain.DialogList, error) {
	s.listByPeersCalls++
	s.listByPeersBatches = append(s.listByPeersBatches, append([]domain.Peer(nil), peers...))
	return s.DialogStore.ListByPeers(ctx, userID, peers)
}

func (s *countingDialogStore) ListDraftsByPeers(ctx context.Context, userID int64, peers []domain.Peer) ([]domain.DialogDraft, error) {
	s.listDraftsByPeersCalls++
	s.listDraftsByPeersBatches = append(s.listDraftsByPeersBatches, append([]domain.Peer(nil), peers...))
	if s.listDraftsByPeersErr != nil {
		return nil, s.listDraftsByPeersErr
	}
	return s.DialogStore.ListDraftsByPeers(ctx, userID, peers)
}

type fakeDialogReadModelVersions struct {
	hashes map[store.ReadModelKey]int64
}

func (f *fakeDialogReadModelVersions) ReadModelHash(_ context.Context, model string, ownerUserID int64, peerType domain.PeerType, peerID int64) (int64, bool, error) {
	hash := f.hashes[store.ReadModelKey{Model: model, OwnerUserID: ownerUserID, PeerType: peerType, PeerID: peerID}]
	return hash, hash != 0, nil
}

func (f *fakeDialogReadModelVersions) ReadModelHashes(_ context.Context, keys []store.ReadModelKey) (map[store.ReadModelKey]int64, error) {
	out := make(map[store.ReadModelKey]int64, len(keys))
	for _, key := range keys {
		if hash := f.hashes[key]; hash != 0 {
			out[key] = hash
		}
	}
	return out, nil
}

type countingDialogChannelStore struct {
	*memory.ChannelStore
	getChannelCalls        int
	getChannelsCalls       int
	getChannelDialogsCalls int
	listHistoryCalls       int
}

func (s *countingDialogChannelStore) GetChannel(ctx context.Context, viewerUserID, channelID int64) (domain.ChannelView, error) {
	s.getChannelCalls++
	return s.ChannelStore.GetChannel(ctx, viewerUserID, channelID)
}

func (s *countingDialogChannelStore) GetChannels(ctx context.Context, viewerUserID int64, channelIDs []int64) ([]domain.ChannelView, error) {
	s.getChannelsCalls++
	return s.ChannelStore.GetChannels(ctx, viewerUserID, channelIDs)
}

func (s *countingDialogChannelStore) GetChannelDialogs(ctx context.Context, viewerUserID int64, channelIDs []int64) (domain.ChannelDialogList, error) {
	s.getChannelDialogsCalls++
	return s.ChannelStore.GetChannelDialogs(ctx, viewerUserID, channelIDs)
}

func (s *countingDialogChannelStore) ListChannelHistory(ctx context.Context, viewerUserID int64, filter domain.ChannelHistoryFilter) (domain.ChannelHistory, error) {
	s.listHistoryCalls++
	return s.ChannelStore.ListChannelHistory(ctx, viewerUserID, filter)
}

func TestGetDialogsHashUsesWarmStableHashCacheAndInvalidatesOnWrite(t *testing.T) {
	ctx := context.Background()
	const ownerID int64 = 1001
	peer := domain.Peer{Type: domain.PeerTypeUser, ID: 1002}
	base := memory.NewDialogStore()
	if err := base.SaveList(ctx, ownerID, domain.DialogList{
		Dialogs: []domain.Dialog{{
			Peer:           peer,
			TopMessage:     7,
			TopMessageDate: 70,
			UnreadCount:    1,
		}},
		Messages: []domain.Message{{
			ID:          7,
			OwnerUserID: ownerID,
			Peer:        peer,
			From:        peer,
			Date:        70,
			Body:        "cached top",
		}},
		Users: []domain.User{{ID: peer.ID, AccessHash: 22, FirstName: "Peer"}},
	}); err != nil {
		t.Fatalf("SaveList: %v", err)
	}
	counting := &countingDialogStore{DialogStore: base}
	dialogs := NewService(counting)
	filter := domain.DialogFilter{ExcludePinned: true, Limit: 10}
	list, err := dialogs.GetDialogs(ctx, ownerID, filter)
	if err != nil {
		t.Fatalf("GetDialogs warm: %v", err)
	}
	if list.Hash == 0 {
		t.Fatal("warmed list hash = 0, want stable non-zero hash")
	}

	check, err := dialogs.GetDialogsHash(ctx, ownerID, domain.DialogFilter{ExcludePinned: true, Limit: 10, Hash: list.Hash})
	if err != nil {
		t.Fatalf("GetDialogsHash: %v", err)
	}
	if !check.Known || !check.Matched || check.Count != list.Count {
		t.Fatalf("hash check = %+v, want known matched count %d", check, list.Count)
	}
	if counting.listByUserCalls != 1 {
		t.Fatalf("ListByUser calls = %d, want only warm load", counting.listByUserCalls)
	}

	if _, err := dialogs.SaveDraft(ctx, ownerID, domain.DialogDraft{Peer: peer, Date: 71, Message: "new draft"}); err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}
	check, err = dialogs.GetDialogsHash(ctx, ownerID, domain.DialogFilter{ExcludePinned: true, Limit: 10, Hash: list.Hash})
	if err != nil {
		t.Fatalf("GetDialogsHash after invalidation: %v", err)
	}
	if check.Known {
		t.Fatalf("hash check after invalidation = %+v, want unknown", check)
	}
}

func TestSaveDraftNoopsWhenOnlyDateChanges(t *testing.T) {
	ctx := context.Background()
	const ownerID int64 = 1001
	peer := domain.Peer{Type: domain.PeerTypeUser, ID: 1002}
	base := memory.NewDialogStore()
	dialogs := NewService(base)

	changed, err := dialogs.SaveDraft(ctx, ownerID, domain.DialogDraft{Peer: peer, Date: 71, Message: "draft"})
	if err != nil {
		t.Fatalf("SaveDraft first: %v", err)
	}
	if !changed {
		t.Fatalf("SaveDraft first changed = false, want true")
	}
	changed, err = dialogs.SaveDraft(ctx, ownerID, domain.DialogDraft{Peer: peer, Date: 72, Message: "draft"})
	if err != nil {
		t.Fatalf("SaveDraft same content: %v", err)
	}
	if changed {
		t.Fatalf("SaveDraft same content changed = true, want false")
	}
	got, found, err := base.GetDraft(ctx, ownerID, peer, 0)
	if err != nil || !found {
		t.Fatalf("GetDraft = found %v err %v, want stored draft", found, err)
	}
	if got.Date != 71 {
		t.Fatalf("draft date = %d, want original 71", got.Date)
	}

	changed, err = dialogs.SaveDraft(ctx, ownerID, domain.DialogDraft{Peer: peer, Date: 73, Message: "updated"})
	if err != nil {
		t.Fatalf("SaveDraft updated: %v", err)
	}
	if !changed {
		t.Fatalf("SaveDraft updated changed = false, want true")
	}
}

func TestGetDialogsLoadsDraftsOnlyForCurrentPage(t *testing.T) {
	ctx := context.Background()
	const ownerID int64 = 1001
	firstPeer := domain.Peer{Type: domain.PeerTypeUser, ID: 1002}
	secondPeer := domain.Peer{Type: domain.PeerTypeUser, ID: 1003}
	base := memory.NewDialogStore()
	if err := base.SaveList(ctx, ownerID, domain.DialogList{
		Dialogs: []domain.Dialog{
			{Peer: firstPeer, TopMessage: 11, TopMessageDate: 200},
			{Peer: secondPeer, TopMessage: 12, TopMessageDate: 100},
		},
		Messages: []domain.Message{
			{ID: 11, OwnerUserID: ownerID, Peer: firstPeer, From: firstPeer, Date: 200, Body: "first"},
			{ID: 12, OwnerUserID: ownerID, Peer: secondPeer, From: secondPeer, Date: 100, Body: "second"},
		},
		Users: []domain.User{
			{ID: firstPeer.ID, FirstName: "First"},
			{ID: secondPeer.ID, FirstName: "Second"},
		},
	}); err != nil {
		t.Fatalf("SaveList: %v", err)
	}
	if err := base.SaveDraft(ctx, ownerID, domain.DialogDraft{Peer: firstPeer, Date: 201, Message: "first draft"}); err != nil {
		t.Fatalf("save first draft: %v", err)
	}
	if err := base.SaveDraft(ctx, ownerID, domain.DialogDraft{Peer: secondPeer, Date: 202, Message: "second draft"}); err != nil {
		t.Fatalf("save second draft: %v", err)
	}
	counting := &countingDialogStore{DialogStore: base}

	list, err := NewService(counting).GetDialogs(ctx, ownerID, domain.DialogFilter{Limit: 1})
	if err != nil {
		t.Fatalf("GetDialogs: %v", err)
	}
	if len(list.Dialogs) != 1 || list.Dialogs[0].Peer != firstPeer || list.Dialogs[0].Draft == nil || list.Dialogs[0].Draft.Message != "first draft" {
		t.Fatalf("dialogs = %+v, want first page with its draft", list.Dialogs)
	}
	if counting.listDraftsByPeersCalls != 1 || len(counting.listDraftsByPeersBatches) != 1 {
		t.Fatalf("ListDraftsByPeers calls/batches = %d/%d, want 1/1", counting.listDraftsByPeersCalls, len(counting.listDraftsByPeersBatches))
	}
	if got := counting.listDraftsByPeersBatches[0]; len(got) != 1 || got[0] != firstPeer {
		t.Fatalf("draft peer batch = %+v, want current-page peer %+v", got, firstPeer)
	}
}

func TestGetPeerDialogsCachesPrivatePeerReadModelByHash(t *testing.T) {
	ctx := context.Background()
	const ownerID int64 = 1001
	peer := domain.Peer{Type: domain.PeerTypeUser, ID: 1002}
	base := memory.NewDialogStore()
	if err := base.SaveList(ctx, ownerID, domain.DialogList{
		Dialogs: []domain.Dialog{{
			Peer:           peer,
			TopMessage:     7,
			TopMessageDate: 70,
			UnreadCount:    1,
		}},
		Messages: []domain.Message{{
			ID:          7,
			OwnerUserID: ownerID,
			Peer:        peer,
			From:        peer,
			Date:        70,
			Body:        "cached top",
		}},
		Users: []domain.User{{ID: peer.ID, AccessHash: 22, FirstName: "Peer"}},
	}); err != nil {
		t.Fatalf("SaveList: %v", err)
	}
	if err := base.SaveDraft(ctx, ownerID, domain.DialogDraft{Peer: peer, Date: 71, Message: "draft"}); err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}
	counting := &countingDialogStore{DialogStore: base}
	versions := &fakeDialogReadModelVersions{hashes: map[store.ReadModelKey]int64{
		{Model: dialogLightReadModel, OwnerUserID: ownerID, PeerType: peer.Type, PeerID: peer.ID}: 101,
	}}
	dialogs := NewService(counting).Configure(WithReadModelVersions(versions))

	first, err := dialogs.GetPeerDialogs(ctx, ownerID, []domain.Peer{peer})
	if err != nil {
		t.Fatalf("first GetPeerDialogs: %v", err)
	}
	if len(first.Dialogs) != 1 || first.Dialogs[0].Draft == nil || first.Dialogs[0].Draft.Message != "draft" {
		t.Fatalf("first dialog = %+v, want cached draft attached", first.Dialogs)
	}
	second, err := dialogs.GetPeerDialogs(ctx, ownerID, []domain.Peer{peer})
	if err != nil {
		t.Fatalf("second GetPeerDialogs: %v", err)
	}
	if len(second.Dialogs) != 1 || second.Dialogs[0].TopMessage != 7 {
		t.Fatalf("second dialog = %+v, want cached top message", second.Dialogs)
	}
	if counting.listByPeersCalls != 1 || counting.listDraftsByPeersCalls != 1 {
		t.Fatalf("store calls ListByPeers/ListDraftsByPeers = %d/%d, want 1/1 after cache hit", counting.listByPeersCalls, counting.listDraftsByPeersCalls)
	}
	if got := counting.listDraftsByPeersBatches[0]; len(got) != 1 || got[0] != peer {
		t.Fatalf("draft peer batch = %+v, want only %+v", got, peer)
	}

	versions.hashes[store.ReadModelKey{Model: dialogLightReadModel, OwnerUserID: ownerID, PeerType: peer.Type, PeerID: peer.ID}] = 202
	if _, err := dialogs.GetPeerDialogs(ctx, ownerID, []domain.Peer{peer}); err != nil {
		t.Fatalf("third GetPeerDialogs after hash bump: %v", err)
	}
	if counting.listByPeersCalls != 2 || counting.listDraftsByPeersCalls != 2 {
		t.Fatalf("store calls after hash bump = %d/%d, want 2/2", counting.listByPeersCalls, counting.listDraftsByPeersCalls)
	}

	if _, err := dialogs.SaveDraft(ctx, ownerID, domain.DialogDraft{Peer: peer, Date: 72, Message: "new draft"}); err != nil {
		t.Fatalf("service SaveDraft: %v", err)
	}
	if _, err := dialogs.GetPeerDialogs(ctx, ownerID, []domain.Peer{peer}); err != nil {
		t.Fatalf("GetPeerDialogs after service invalidation: %v", err)
	}
	if counting.listByPeersCalls != 3 || counting.listDraftsByPeersCalls != 3 {
		t.Fatalf("store calls after explicit invalidation = %d/%d, want 3/3", counting.listByPeersCalls, counting.listDraftsByPeersCalls)
	}
}

func TestGetPeerDialogsReloadsOnlyReadModelCacheMisses(t *testing.T) {
	ctx := context.Background()
	const ownerID int64 = 1001
	firstPeer := domain.Peer{Type: domain.PeerTypeUser, ID: 1002}
	secondPeer := domain.Peer{Type: domain.PeerTypeUser, ID: 1003}
	base := memory.NewDialogStore()
	if err := base.SaveList(ctx, ownerID, domain.DialogList{
		Dialogs: []domain.Dialog{
			{Peer: firstPeer, TopMessage: 7, TopMessageDate: 70},
			{Peer: secondPeer, TopMessage: 8, TopMessageDate: 80},
		},
		Messages: []domain.Message{
			{ID: 7, OwnerUserID: ownerID, Peer: firstPeer, From: firstPeer, Date: 70, Body: "first"},
			{ID: 8, OwnerUserID: ownerID, Peer: secondPeer, From: secondPeer, Date: 80, Body: "second"},
		},
		Users: []domain.User{
			{ID: firstPeer.ID, AccessHash: 22, FirstName: "First"},
			{ID: secondPeer.ID, AccessHash: 33, FirstName: "Second"},
		},
	}); err != nil {
		t.Fatalf("SaveList: %v", err)
	}
	counting := &countingDialogStore{DialogStore: base}
	versions := &fakeDialogReadModelVersions{hashes: map[store.ReadModelKey]int64{
		{Model: dialogLightReadModel, OwnerUserID: ownerID, PeerType: firstPeer.Type, PeerID: firstPeer.ID}:   101,
		{Model: dialogLightReadModel, OwnerUserID: ownerID, PeerType: secondPeer.Type, PeerID: secondPeer.ID}: 202,
	}}
	dialogs := NewService(counting).Configure(WithReadModelVersions(versions))
	peers := []domain.Peer{firstPeer, secondPeer}

	if _, err := dialogs.GetPeerDialogs(ctx, ownerID, peers); err != nil {
		t.Fatalf("first GetPeerDialogs: %v", err)
	}
	versions.hashes[store.ReadModelKey{Model: dialogLightReadModel, OwnerUserID: ownerID, PeerType: secondPeer.Type, PeerID: secondPeer.ID}] = 303
	if _, err := dialogs.GetPeerDialogs(ctx, ownerID, peers); err != nil {
		t.Fatalf("second GetPeerDialogs after one hash bump: %v", err)
	}
	if counting.listByPeersCalls != 2 {
		t.Fatalf("ListByPeers calls = %d, want 2", counting.listByPeersCalls)
	}
	lastBatch := counting.listByPeersBatches[len(counting.listByPeersBatches)-1]
	if len(lastBatch) != 1 || lastBatch[0] != secondPeer {
		t.Fatalf("last ListByPeers batch = %+v, want only %+v", lastBatch, secondPeer)
	}
}

func TestGetPeerDialogsUsesStoreChannelProjectionAndVersionedDraftCache(t *testing.T) {
	ctx := context.Background()
	const ownerID int64 = 1001
	dialogStore := &countingDialogStore{DialogStore: memory.NewDialogStore()}
	channelStore := &countingDialogChannelStore{ChannelStore: memory.NewChannelStore()}
	channels := appchannels.NewService(channelStore)
	created, err := channels.CreateChannel(ctx, ownerID, domain.CreateChannelRequest{
		Title:     "Cached Channel Dialog",
		Megagroup: true,
		Date:      1700003200,
	})
	if err != nil {
		t.Fatalf("CreateChannel: %v", err)
	}
	sent, err := channels.SendMessage(ctx, ownerID, domain.SendChannelMessageRequest{
		ChannelID: created.Channel.ID,
		RandomID:  11,
		Message:   "cached channel top",
		Date:      1700003210,
	})
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	peer := domain.Peer{Type: domain.PeerTypeChannel, ID: created.Channel.ID}
	versions := &fakeDialogReadModelVersions{hashes: map[store.ReadModelKey]int64{
		{Model: dialogLightReadModel, OwnerUserID: ownerID, PeerType: peer.Type, PeerID: peer.ID}: 33,
	}}
	dialogs := NewService(dialogStore, channelStore).Configure(WithReadModelVersions(versions))

	first, err := dialogs.GetPeerDialogs(ctx, ownerID, []domain.Peer{peer})
	if err != nil {
		t.Fatalf("first GetPeerDialogs: %v", err)
	}
	if len(first.Dialogs) != 1 || first.Dialogs[0].TopMessage != sent.Message.ID {
		t.Fatalf("first dialogs = %+v, want channel top %d", first.Dialogs, sent.Message.ID)
	}
	if len(first.ChannelMessages) != 1 || first.ChannelMessages[0].Body != "cached channel top" {
		t.Fatalf("first channel messages = %+v, want cached top message", first.ChannelMessages)
	}
	if _, err := dialogs.GetPeerDialogs(ctx, ownerID, []domain.Peer{peer}); err != nil {
		t.Fatalf("second GetPeerDialogs: %v", err)
	}
	if channelStore.getChannelDialogsCalls != 2 || dialogStore.listDraftsByPeersCalls != 1 {
		t.Fatalf("store calls GetChannelDialogs/ListDraftsByPeers = %d/%d, want 2/1 without duplicate Service channel cache",
			channelStore.getChannelDialogsCalls, dialogStore.listDraftsByPeersCalls)
	}

	versions.hashes[store.ReadModelKey{Model: dialogLightReadModel, OwnerUserID: ownerID, PeerType: peer.Type, PeerID: peer.ID}] = 44
	if _, err := dialogs.GetPeerDialogs(ctx, ownerID, []domain.Peer{peer}); err != nil {
		t.Fatalf("third GetPeerDialogs after dialog hash bump: %v", err)
	}
	if channelStore.getChannelDialogsCalls != 3 || dialogStore.listDraftsByPeersCalls != 2 {
		t.Fatalf("store calls after dialog hash bump = %d/%d, want 3/2",
			channelStore.getChannelDialogsCalls, dialogStore.listDraftsByPeersCalls)
	}

	if _, err := dialogs.SaveDraft(ctx, ownerID, domain.DialogDraft{Peer: peer, Date: 1700003220, Message: "channel draft"}); err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}
	if _, err := dialogs.GetPeerDialogs(ctx, ownerID, []domain.Peer{peer}); err != nil {
		t.Fatalf("GetPeerDialogs after draft invalidation: %v", err)
	}
	if channelStore.getChannelDialogsCalls != 4 || dialogStore.listDraftsByPeersCalls != 3 {
		t.Fatalf("store calls after draft invalidation = %d/%d, want 4/3",
			channelStore.getChannelDialogsCalls, dialogStore.listDraftsByPeersCalls)
	}
}

func TestChannelHydrationDoesNotEvictPrivatePeerStructure(t *testing.T) {
	ctx := context.Background()
	const ownerID int64 = 1001
	privatePeer := domain.Peer{Type: domain.PeerTypeUser, ID: 1002}
	base := memory.NewDialogStore()
	if err := base.SaveList(ctx, ownerID, domain.DialogList{
		Dialogs:  []domain.Dialog{{Peer: privatePeer, TopMessage: 7, TopMessageDate: 70}},
		Messages: []domain.Message{{ID: 7, OwnerUserID: ownerID, Peer: privatePeer, From: privatePeer, Body: "private"}},
		Users:    []domain.User{{ID: privatePeer.ID}},
	}); err != nil {
		t.Fatalf("SaveList: %v", err)
	}
	dialogStore := &countingDialogStore{DialogStore: base}
	channelStore := &countingDialogChannelStore{ChannelStore: memory.NewChannelStore()}
	channels := appchannels.NewService(channelStore)
	channelPeers := make([]domain.Peer, 0, 2)
	for i := 0; i < 2; i++ {
		created, err := channels.CreateChannel(ctx, ownerID, domain.CreateChannelRequest{Title: "channel", Megagroup: true, Date: 100 + i})
		if err != nil {
			t.Fatalf("CreateChannel(%d): %v", i, err)
		}
		channelPeers = append(channelPeers, domain.Peer{Type: domain.PeerTypeChannel, ID: created.Channel.ID})
	}
	versions := &fakeDialogReadModelVersions{hashes: map[store.ReadModelKey]int64{
		{Model: dialogLightReadModel, OwnerUserID: ownerID, PeerType: privatePeer.Type, PeerID: privatePeer.ID}: 1,
	}}
	dialogs := NewService(dialogStore, channelStore).Configure(
		WithReadModelVersions(versions),
		WithDialogHydrationCaches(1, 1<<20, 10, 1<<20),
	)
	if _, err := dialogs.GetPeerDialogs(ctx, ownerID, []domain.Peer{privatePeer}); err != nil {
		t.Fatalf("first private hydration: %v", err)
	}
	for _, peer := range channelPeers {
		if _, err := dialogs.GetPeerDialogs(ctx, ownerID, []domain.Peer{peer}); err != nil {
			t.Fatalf("channel hydration %+v: %v", peer, err)
		}
	}
	if _, err := dialogs.GetPeerDialogs(ctx, ownerID, []domain.Peer{privatePeer}); err != nil {
		t.Fatalf("second private hydration: %v", err)
	}
	if dialogStore.listByPeersCalls != 1 {
		t.Fatalf("private ListByPeers calls = %d, want 1 after channel churn", dialogStore.listByPeersCalls)
	}
}

func TestPrivatePeerCacheReprojectsCurrentViewerUserFacts(t *testing.T) {
	ctx := context.Background()
	const ownerID int64 = 1001
	peer := domain.Peer{Type: domain.PeerTypeUser, ID: 1002}
	base := memory.NewDialogStore()
	if err := base.SaveList(ctx, ownerID, domain.DialogList{
		Dialogs: []domain.Dialog{{Peer: peer, TopMessage: 7}},
		Users:   []domain.User{{ID: peer.ID, FirstName: "Peer"}},
	}); err != nil {
		t.Fatalf("SaveList: %v", err)
	}
	counting := &countingDialogStore{DialogStore: base}
	versions := &fakeDialogReadModelVersions{hashes: map[store.ReadModelKey]int64{
		{Model: dialogLightReadModel, OwnerUserID: ownerID, PeerType: peer.Type, PeerID: peer.ID}: 101,
	}}
	photos := dialogProfilePhotos{peer.ID: {PhotoID: 1}}
	dialogs := NewService(counting).Configure(WithReadModelVersions(versions), WithPhotoProvider(photos))
	first, err := dialogs.GetPeerDialogs(ctx, ownerID, []domain.Peer{peer})
	if err != nil {
		t.Fatalf("first GetPeerDialogs: %v", err)
	}
	photos[peer.ID] = domain.ProfilePhotoRef{PhotoID: 2}
	second, err := dialogs.GetPeerDialogs(ctx, ownerID, []domain.Peer{peer})
	if err != nil {
		t.Fatalf("second GetPeerDialogs: %v", err)
	}
	if len(first.Users) != 1 || first.Users[0].PhotoID != 1 || len(second.Users) != 1 || second.Users[0].PhotoID != 2 {
		t.Fatalf("projected photos first/second = %+v/%+v, want 1/2", first.Users, second.Users)
	}
	if counting.listByPeersCalls != 1 {
		t.Fatalf("ListByPeers calls = %d, want one structural load", counting.listByPeersCalls)
	}
}

func TestDraftReadErrorIsNotCachedAsNegative(t *testing.T) {
	ctx := context.Background()
	const ownerID int64 = 1001
	peer := domain.Peer{Type: domain.PeerTypeUser, ID: 1002}
	base := memory.NewDialogStore()
	if err := base.SaveList(ctx, ownerID, domain.DialogList{Dialogs: []domain.Dialog{{Peer: peer}}}); err != nil {
		t.Fatalf("SaveList: %v", err)
	}
	if err := base.SaveDraft(ctx, ownerID, domain.DialogDraft{Peer: peer, Message: "recovered"}); err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}
	counting := &countingDialogStore{DialogStore: base, listDraftsByPeersErr: errors.New("temporary draft read failure")}
	versions := &fakeDialogReadModelVersions{hashes: map[store.ReadModelKey]int64{
		{Model: dialogLightReadModel, OwnerUserID: ownerID, PeerType: peer.Type, PeerID: peer.ID}: 101,
	}}
	dialogs := NewService(counting).Configure(WithReadModelVersions(versions))
	if _, err := dialogs.GetPeerDialogs(ctx, ownerID, []domain.Peer{peer}); err == nil {
		t.Fatal("first GetPeerDialogs error = nil, want draft read failure")
	}
	counting.listDraftsByPeersErr = nil
	got, err := dialogs.GetPeerDialogs(ctx, ownerID, []domain.Peer{peer})
	if err != nil {
		t.Fatalf("recovered GetPeerDialogs: %v", err)
	}
	if len(got.Dialogs) != 1 || got.Dialogs[0].Draft == nil || got.Dialogs[0].Draft.Message != "recovered" {
		t.Fatalf("recovered dialogs = %+v", got.Dialogs)
	}
	if counting.listDraftsByPeersCalls != 2 {
		t.Fatalf("ListDraftsByPeers calls = %d, want retry after error", counting.listDraftsByPeersCalls)
	}
}

func TestDialogPeerReadModelCacheRejectsStaleFillAfterInvalidation(t *testing.T) {
	peer := domain.Peer{Type: domain.PeerTypeUser, ID: 1002}
	key := dialogPeerCacheKey{userID: 1001, peer: peer}
	cache := newDialogPeerReadModelCache(time.Hour)
	epoch := cache.cacheEpoch()
	cache.invalidate(key)
	cache.putIfEpoch(key, domain.DialogList{
		Dialogs: []domain.Dialog{{Peer: peer, TopMessage: 7}},
		Hash:    101,
	}, 101, epoch)
	if _, ok := cache.lookup(key, 101); ok {
		t.Fatalf("stale cache fill survived invalidation")
	}
}

func TestGetDialogsIncludesChannelReadOutboxAfterOfflineRead(t *testing.T) {
	ctx := context.Background()
	channelStore := memory.NewChannelStore()
	channels := appchannels.NewService(channelStore)
	dialogs := NewService(nil, channelStore)

	created, err := channels.CreateMegagroupFromCreateChat(ctx, 1001, domain.CreateChannelRequest{
		Title:         "Offline Read",
		MemberUserIDs: []int64{1002},
		Date:          10,
	})
	if err != nil {
		t.Fatalf("CreateMegagroupFromCreateChat: %v", err)
	}
	sent, err := channels.SendMessage(ctx, 1001, domain.SendChannelMessageRequest{
		ChannelID: created.Channel.ID,
		RandomID:  42,
		Message:   "restore read outbox",
		Date:      11,
	})
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if _, err := channels.ReadHistory(ctx, 1002, domain.ReadChannelHistoryRequest{
		ChannelID: created.Channel.ID,
		MaxID:     sent.Message.ID,
		Date:      12,
	}); err != nil {
		t.Fatalf("ReadHistory: %v", err)
	}

	list, err := dialogs.GetDialogs(ctx, 1001, domain.DialogFilter{Limit: 10})
	if err != nil {
		t.Fatalf("GetDialogs: %v", err)
	}
	dialog := findChannelDialog(t, list, created.Channel.ID)
	if dialog.ReadOutboxMaxID != sent.Message.ID {
		t.Fatalf("getDialogs read_outbox = %d, want %d", dialog.ReadOutboxMaxID, sent.Message.ID)
	}

	peerList, err := dialogs.GetPeerDialogs(ctx, 1001, []domain.Peer{
		{Type: domain.PeerTypeChannel, ID: created.Channel.ID},
	})
	if err != nil {
		t.Fatalf("GetPeerDialogs: %v", err)
	}
	peerDialog := findChannelDialog(t, peerList, created.Channel.ID)
	if peerDialog.ReadOutboxMaxID != sent.Message.ID {
		t.Fatalf("getPeerDialogs read_outbox = %d, want %d", peerDialog.ReadOutboxMaxID, sent.Message.ID)
	}
}

func TestGetDialogsProjectsUsersWithCurrentProfilePhoto(t *testing.T) {
	ctx := context.Background()
	const ownerID int64 = 1001
	const peerID int64 = 1002
	dialogStore := memory.NewDialogStore()
	if err := dialogStore.SaveList(ctx, ownerID, domain.DialogList{
		Dialogs: []domain.Dialog{{
			Peer:           domain.Peer{Type: domain.PeerTypeUser, ID: peerID},
			TopMessage:     1,
			TopMessageDate: 20,
		}},
		Users: []domain.User{{
			ID:         peerID,
			AccessHash: 22,
			Phone:      "15550000002",
			FirstName:  "Alice A",
		}},
	}); err != nil {
		t.Fatalf("SaveList: %v", err)
	}
	contacts := memory.NewContactStore()
	if _, err := contacts.Upsert(ctx, ownerID, domain.ContactInput{
		ContactUserID: peerID,
		Phone:         "1111",
		FirstName:     "Alice",
		LastName:      "Saved",
	}); err != nil {
		t.Fatalf("upsert contact: %v", err)
	}
	dialogs := NewService(dialogStore).Configure(
		WithContactStore(contacts),
		WithPhotoProvider(dialogProfilePhotos{
			peerID: {PhotoID: 9201, DCID: 2, Stripped: []byte{7, 8}},
		}),
	)

	list, err := dialogs.GetDialogs(ctx, ownerID, domain.DialogFilter{Limit: 10})
	if err != nil {
		t.Fatalf("GetDialogs: %v", err)
	}
	peer := findDialogUser(t, list.Users, peerID)
	if peer.PhotoID != 9201 || peer.PhotoDCID != 2 || string(peer.PhotoStripped) != string([]byte{7, 8}) {
		t.Fatalf("dialog user photo = id %d dc %d stripped %v, want 9201/2/[7 8]", peer.PhotoID, peer.PhotoDCID, peer.PhotoStripped)
	}
	if !peer.Contact || peer.FirstName != "Alice" || peer.LastName != "Saved" || peer.Phone != "1111" {
		t.Fatalf("dialog user projection = %+v, want contact view", peer)
	}

	peerList, err := dialogs.GetPeerDialogs(ctx, ownerID, []domain.Peer{{Type: domain.PeerTypeUser, ID: peerID}})
	if err != nil {
		t.Fatalf("GetPeerDialogs: %v", err)
	}
	peer = findDialogUser(t, peerList.Users, peerID)
	if peer.PhotoID != 9201 || peer.PhotoDCID != 2 {
		t.Fatalf("peer dialog user photo = id %d dc %d, want 9201/2", peer.PhotoID, peer.PhotoDCID)
	}
}

func TestChannelDialogSettingsPersistThroughUnifiedDialogService(t *testing.T) {
	ctx := context.Background()
	channelStore := memory.NewChannelStore()
	channels := appchannels.NewService(channelStore)
	dialogs := NewService(nil, channelStore)

	first, err := channels.CreateMegagroupFromCreateChat(ctx, 1001, domain.CreateChannelRequest{
		Title: "Pinned One",
		Date:  20,
	})
	if err != nil {
		t.Fatalf("CreateMegagroupFromCreateChat first: %v", err)
	}
	second, err := channels.CreateMegagroupFromCreateChat(ctx, 1001, domain.CreateChannelRequest{
		Title: "Pinned Two",
		Date:  21,
	})
	if err != nil {
		t.Fatalf("CreateMegagroupFromCreateChat second: %v", err)
	}
	firstPeer := domain.Peer{Type: domain.PeerTypeChannel, ID: first.Channel.ID}
	secondPeer := domain.Peer{Type: domain.PeerTypeChannel, ID: second.Channel.ID}

	if changed, _, err := dialogs.TogglePinned(ctx, 1001, firstPeer, true); err != nil || !changed {
		t.Fatalf("TogglePinned first = changed %v err %v, want changed", changed, err)
	}
	if changed, _, err := dialogs.TogglePinned(ctx, 1001, secondPeer, true); err != nil || !changed {
		t.Fatalf("TogglePinned second = changed %v err %v, want changed", changed, err)
	}
	if changed, err := dialogs.ReorderPinned(ctx, 1001, domain.DialogMainFolderID, []domain.Peer{secondPeer, firstPeer}, true); err != nil || changed {
		t.Fatalf("ReorderPinned same order = changed %v err %v, want no-op", changed, err)
	}
	if changed, err := dialogs.MarkUnread(ctx, 1001, firstPeer, true); err != nil || !changed {
		t.Fatalf("MarkUnread = changed %v err %v, want changed", changed, err)
	}
	if err := dialogs.EditPeerFolders(ctx, 1001, []domain.FolderPeerUpdate{
		{Peer: firstPeer, FolderID: domain.DialogArchiveFolderID},
	}); err != nil {
		t.Fatalf("EditPeerFolders: %v", err)
	}

	list, err := dialogs.GetDialogs(ctx, 1001, domain.DialogFilter{Limit: 10})
	if err != nil {
		t.Fatalf("GetDialogs: %v", err)
	}
	// An archived dialog no longer appears in the main list (the default folder is treated as
	// folder 0); the main list presents the archive state via the ArchiveSummary aggregate.
	for _, dialog := range list.Dialogs {
		if dialog.Peer.Type == domain.PeerTypeChannel && dialog.Peer.ID == first.Channel.ID {
			t.Fatalf("archived dialog leaked into main list: %+v", dialog)
		}
	}
	if list.ArchiveSummary == nil {
		t.Fatalf("main list archive summary = nil, want attached after archiving")
	}
	secondDialog := findChannelDialog(t, list, second.Channel.ID)
	if !secondDialog.Pinned || secondDialog.PinnedOrder != 2 {
		t.Fatalf("second dialog = %+v, want pinned order 2", secondDialog)
	}
	marks, err := dialogs.UnreadMarks(ctx, 1001)
	if err != nil {
		t.Fatalf("UnreadMarks: %v", err)
	}
	if len(marks) != 1 || marks[0] != firstPeer {
		t.Fatalf("unread marks = %+v, want first channel", marks)
	}
	archived, err := dialogs.GetDialogs(ctx, 1001, domain.DialogFilter{
		HasFolderID: true,
		FolderID:    domain.DialogArchiveFolderID,
		Limit:       10,
	})
	if err != nil {
		t.Fatalf("GetDialogs archive: %v", err)
	}
	got := findChannelDialog(t, archived, first.Channel.ID)
	if got.FolderID != domain.DialogArchiveFolderID {
		t.Fatalf("archived dialog = %+v, want archive folder", got)
	}
	// Archiving clears pinned (aligned with TDesktop History::setFolderPointer's local unpin)
	// while unread_mark is preserved.
	if got.Pinned || got.PinnedOrder != 0 || !got.UnreadMark {
		t.Fatalf("archived dialog = %+v, want unpinned with unread mark", got)
	}

	custom, err := dialogs.GetDialogs(ctx, 1001, domain.DialogFilter{
		HasFolderID: true,
		FolderID:    domain.DialogCustomFolderMinID,
		Folder:      &domain.DialogFolder{ID: domain.DialogCustomFolderMinID, Groups: true},
		Limit:       10,
	})
	if err != nil {
		t.Fatalf("GetDialogs custom groups: %v", err)
	}
	if got := findChannelDialog(t, custom, first.Channel.ID); got.Peer.ID != first.Channel.ID {
		t.Fatalf("custom group dialog = %+v, want first channel", got)
	}
}

func TestTogglePinnedPromotesNewestPinnedAcrossPrivateAndChannelDialogs(t *testing.T) {
	ctx := context.Background()
	const ownerID int64 = 1001
	dialogStore := memory.NewDialogStore()
	channelStore := memory.NewChannelStore()
	channels := appchannels.NewService(channelStore)
	dialogs := NewService(dialogStore, channelStore)
	privatePeer := domain.Peer{Type: domain.PeerTypeUser, ID: 1002}
	if err := dialogStore.SaveList(ctx, ownerID, domain.DialogList{
		Dialogs: []domain.Dialog{{
			Peer:           privatePeer,
			TopMessage:     30,
			TopMessageDate: 3000,
		}},
		Messages: []domain.Message{{
			ID:          30,
			OwnerUserID: ownerID,
			Peer:        privatePeer,
			From:        privatePeer,
			Date:        3000,
			Body:        "newer private top",
		}},
		Users: []domain.User{{ID: privatePeer.ID, AccessHash: 22, FirstName: "Private"}},
	}); err != nil {
		t.Fatalf("SaveList private dialog: %v", err)
	}
	created, err := channels.CreateChannel(ctx, ownerID, domain.CreateChannelRequest{
		Title:     "Older Channel",
		Megagroup: true,
		Date:      1000,
	})
	if err != nil {
		t.Fatalf("CreateChannel: %v", err)
	}
	channelPeer := domain.Peer{Type: domain.PeerTypeChannel, ID: created.Channel.ID}

	if changed, _, err := dialogs.TogglePinned(ctx, ownerID, privatePeer, true); err != nil || !changed {
		t.Fatalf("TogglePinned private = changed %v err %v, want changed", changed, err)
	}
	if changed, _, err := dialogs.TogglePinned(ctx, ownerID, channelPeer, true); err != nil || !changed {
		t.Fatalf("TogglePinned channel = changed %v err %v, want changed", changed, err)
	}

	list, err := dialogs.GetDialogs(ctx, ownerID, domain.DialogFilter{Limit: 10})
	if err != nil {
		t.Fatalf("GetDialogs: %v", err)
	}
	if len(list.Dialogs) < 2 {
		t.Fatalf("dialogs = %+v, want private and channel dialogs", list.Dialogs)
	}
	if list.Dialogs[0].Peer != channelPeer || list.Dialogs[0].PinnedOrder != 2 {
		t.Fatalf("first dialog = %+v, want newly pinned channel with highest order", list.Dialogs[0])
	}
	if list.Dialogs[1].Peer != privatePeer || list.Dialogs[1].PinnedOrder != 1 {
		t.Fatalf("second dialog = %+v, want older pinned private dialog", list.Dialogs[1])
	}
}

func TestGetDialogsAppliesChannelDialogOffset(t *testing.T) {
	ctx := context.Background()
	channelStore := memory.NewChannelStore()
	channels := appchannels.NewService(channelStore)
	dialogs := NewService(nil, channelStore)

	old, err := channels.CreateMegagroupFromCreateChat(ctx, 1001, domain.CreateChannelRequest{
		Title: "Older Channel",
		Date:  20,
	})
	if err != nil {
		t.Fatalf("CreateMegagroupFromCreateChat old: %v", err)
	}
	newer, err := channels.CreateMegagroupFromCreateChat(ctx, 1001, domain.CreateChannelRequest{
		Title: "Newer Channel",
		Date:  30,
	})
	if err != nil {
		t.Fatalf("CreateMegagroupFromCreateChat newer: %v", err)
	}

	first, err := dialogs.GetDialogs(ctx, 1001, domain.DialogFilter{Limit: 1})
	if err != nil {
		t.Fatalf("GetDialogs first: %v", err)
	}
	if len(first.Dialogs) != 1 || first.Dialogs[0].Peer.ID != newer.Channel.ID {
		t.Fatalf("first page dialogs = %+v, want newer channel", first.Dialogs)
	}

	next, err := dialogs.GetDialogs(ctx, 1001, domain.DialogFilter{
		OffsetDate:    first.Dialogs[0].TopMessageDate,
		OffsetID:      first.Dialogs[0].TopMessage,
		HasOffsetPeer: true,
		OffsetPeer:    first.Dialogs[0].Peer,
		Limit:         10,
	})
	if err != nil {
		t.Fatalf("GetDialogs next: %v", err)
	}
	if len(next.Dialogs) != 1 || next.Dialogs[0].Peer.ID != old.Channel.ID {
		t.Fatalf("next page dialogs = %+v, want only older channel", next.Dialogs)
	}
}

func TestGetPeerDialogsRejectsHugeVector(t *testing.T) {
	dialogs := NewService(nil, memory.NewChannelStore())
	peers := make([]domain.Peer, domain.MaxDialogFolderPeers+1)
	for i := range peers {
		peers[i] = domain.Peer{Type: domain.PeerTypeChannel, ID: int64(i + 1)}
	}
	if _, err := dialogs.GetPeerDialogs(context.Background(), 1001, peers); !errors.Is(err, domain.ErrChannelInvalid) {
		t.Fatalf("GetPeerDialogs huge vector err = %v, want ErrChannelInvalid", err)
	}
}

func TestGetPeerDialogsReturnsZeroTopBootstrapForPublicChannelPreview(t *testing.T) {
	ctx := context.Background()
	channelStore := memory.NewChannelStore()
	channels := appchannels.NewService(channelStore)
	dialogs := NewService(nil, channelStore)

	public, err := channels.CreateChannel(ctx, 1001, domain.CreateChannelRequest{
		Title:     "Public Peer Dialog",
		Broadcast: true,
		Date:      1700002000,
	})
	if err != nil {
		t.Fatalf("CreateChannel public: %v", err)
	}
	if _, err := channels.UpdateUsername(ctx, 1001, domain.UpdateChannelUsernameRequest{
		UserID:    1001,
		ChannelID: public.Channel.ID,
		Username:  "public_peer_dialog",
	}); err != nil {
		t.Fatalf("UpdateUsername public: %v", err)
	}
	sent, err := channels.SendMessage(ctx, 1001, domain.SendChannelMessageRequest{
		ChannelID: public.Channel.ID,
		RandomID:  99,
		Message:   "public peer dialog top",
		Date:      1700002010,
	})
	if err != nil {
		t.Fatalf("SendMessage public: %v", err)
	}
	private, err := channels.CreateChannel(ctx, 1001, domain.CreateChannelRequest{
		Title:     "Private Peer Dialog",
		Broadcast: true,
		Date:      1700002020,
	})
	if err != nil {
		t.Fatalf("CreateChannel private: %v", err)
	}

	list, err := dialogs.GetPeerDialogs(ctx, 1002, []domain.Peer{
		{Type: domain.PeerTypeChannel, ID: public.Channel.ID},
		{Type: domain.PeerTypeChannel, ID: private.Channel.ID},
	})
	if err != nil {
		t.Fatalf("GetPeerDialogs public preview: %v", err)
	}
	if len(list.Dialogs) != 1 || len(list.ChannelMessages) != 0 || len(list.Channels) != 1 || list.Count != 1 {
		t.Fatalf("peer dialogs = %+v, want one zero-top public preview bootstrap", list)
	}
	dialog := list.Dialogs[0]
	if dialog.Peer.Type != domain.PeerTypeChannel || dialog.Peer.ID != public.Channel.ID ||
		!dialog.ChannelLeft || dialog.TopMessage != 0 || dialog.TopMessageDate != 0 ||
		dialog.ReadInboxMaxID != 0 || dialog.ReadOutboxMaxID != 0 ||
		dialog.UnreadCount != 0 || dialog.UnreadMentions != 0 ||
		dialog.UnreadReactions != 0 || dialog.Pts != sent.Event.Pts {
		t.Fatalf("public preview bootstrap dialog = %+v", dialog)
	}
	if list.Channels[0].ID != public.Channel.ID {
		t.Fatalf("public preview channels = %+v, want channel %d", list.Channels, public.Channel.ID)
	}
}

func TestGetPeerDialogsBatchesMissingChannelVisibilityChecks(t *testing.T) {
	ctx := context.Background()
	channelStore := &countingDialogChannelStore{ChannelStore: memory.NewChannelStore()}
	channels := appchannels.NewService(channelStore)
	dialogs := NewService(nil, channelStore)

	first, err := channels.CreateChannel(ctx, 1001, domain.CreateChannelRequest{
		Title:     "Batch Preview One",
		Broadcast: true,
		Date:      1700002100,
	})
	if err != nil {
		t.Fatalf("CreateChannel first: %v", err)
	}
	if _, err := channels.UpdateUsername(ctx, 1001, domain.UpdateChannelUsernameRequest{
		UserID:    1001,
		ChannelID: first.Channel.ID,
		Username:  "batch_preview_one",
	}); err != nil {
		t.Fatalf("UpdateUsername first: %v", err)
	}
	if _, err := channels.SendMessage(ctx, 1001, domain.SendChannelMessageRequest{
		ChannelID: first.Channel.ID,
		RandomID:  101,
		Message:   "first public preview",
		Date:      1700002110,
	}); err != nil {
		t.Fatalf("SendMessage first: %v", err)
	}

	second, err := channels.CreateChannel(ctx, 1001, domain.CreateChannelRequest{
		Title:     "Batch Preview Two",
		Broadcast: true,
		Date:      1700002120,
	})
	if err != nil {
		t.Fatalf("CreateChannel second: %v", err)
	}
	if _, err := channels.UpdateUsername(ctx, 1001, domain.UpdateChannelUsernameRequest{
		UserID:    1001,
		ChannelID: second.Channel.ID,
		Username:  "batch_preview_two",
	}); err != nil {
		t.Fatalf("UpdateUsername second: %v", err)
	}
	if _, err := channels.SendMessage(ctx, 1001, domain.SendChannelMessageRequest{
		ChannelID: second.Channel.ID,
		RandomID:  102,
		Message:   "second public preview",
		Date:      1700002130,
	}); err != nil {
		t.Fatalf("SendMessage second: %v", err)
	}

	private, err := channels.CreateChannel(ctx, 1001, domain.CreateChannelRequest{
		Title:     "Batch Preview Private",
		Broadcast: true,
		Date:      1700002140,
	})
	if err != nil {
		t.Fatalf("CreateChannel private: %v", err)
	}

	channelStore.getChannelCalls = 0
	channelStore.getChannelsCalls = 0
	channelStore.listHistoryCalls = 0
	list, err := dialogs.GetPeerDialogs(ctx, 1002, []domain.Peer{
		{Type: domain.PeerTypeChannel, ID: first.Channel.ID},
		{Type: domain.PeerTypeChannel, ID: private.Channel.ID},
		{Type: domain.PeerTypeChannel, ID: second.Channel.ID},
		{Type: domain.PeerTypeChannel, ID: first.Channel.ID},
	})
	if err != nil {
		t.Fatalf("GetPeerDialogs batch previews: %v", err)
	}
	if channelStore.getChannelsCalls != 1 || channelStore.getChannelCalls != 0 {
		t.Fatalf("visibility channel calls: GetChannels=%d GetChannel=%d, want one batch call only", channelStore.getChannelsCalls, channelStore.getChannelCalls)
	}
	if channelStore.listHistoryCalls != 0 {
		t.Fatalf("public preview history calls = %d, want zero", channelStore.listHistoryCalls)
	}
	if len(list.Dialogs) != 2 || len(list.ChannelMessages) != 0 || len(list.Channels) != 2 || list.Count != 2 {
		t.Fatalf("peer dialogs = %+v, want two deduplicated zero-top public previews", list)
	}
	for _, dialog := range list.Dialogs {
		if !dialog.ChannelLeft || dialog.TopMessage != 0 || dialog.ReadInboxMaxID != 0 || dialog.ReadOutboxMaxID != 0 {
			t.Fatalf("public preview bootstrap dialog = %+v", dialog)
		}
	}
}

func findChannelDialog(t *testing.T, list domain.DialogList, channelID int64) domain.Dialog {
	t.Helper()
	for _, dialog := range list.Dialogs {
		if dialog.Peer.Type == domain.PeerTypeChannel && dialog.Peer.ID == channelID {
			return dialog
		}
	}
	t.Fatalf("channel dialog %d not found in %+v", channelID, list.Dialogs)
	return domain.Dialog{}
}

func findDialogUser(t *testing.T, users []domain.User, userID int64) domain.User {
	t.Helper()
	for _, user := range users {
		if user.ID == userID {
			return user
		}
	}
	t.Fatalf("user %d not found in %+v", userID, users)
	return domain.User{}
}

type dialogProfilePhotos map[int64]domain.ProfilePhotoRef

func (p dialogProfilePhotos) CurrentProfilePhotos(_ context.Context, _ domain.PeerType, ids []int64) (map[int64]domain.ProfilePhotoRef, error) {
	out := make(map[int64]domain.ProfilePhotoRef, len(ids))
	for _, id := range ids {
		if ref, ok := p[id]; ok {
			out[id] = ref
		}
	}
	return out, nil
}

func TestGetDialogsMainListAttachesArchiveSummary(t *testing.T) {
	ctx := context.Background()
	dialogStore := memory.NewDialogStore()
	dialogs := NewService(dialogStore)

	archivedPeer := domain.Peer{Type: domain.PeerTypeUser, ID: 2002}
	mainPeer := domain.Peer{Type: domain.PeerTypeUser, ID: 2003}
	if err := dialogStore.Upsert(ctx, 1001, domain.Dialog{
		Peer:           archivedPeer,
		FolderID:       domain.DialogArchiveFolderID,
		TopMessage:     7,
		TopMessageDate: 30,
		UnreadCount:    3,
	}); err != nil {
		t.Fatalf("upsert archived dialog: %v", err)
	}
	if err := dialogStore.Upsert(ctx, 1001, domain.Dialog{
		Peer:           mainPeer,
		TopMessage:     9,
		TopMessageDate: 40,
	}); err != nil {
		t.Fatalf("upsert main dialog: %v", err)
	}

	list, err := dialogs.GetDialogs(ctx, 1001, domain.DialogFilter{Limit: 10})
	if err != nil {
		t.Fatalf("GetDialogs main: %v", err)
	}
	if list.ArchiveSummary == nil {
		t.Fatalf("main list archive summary = nil, want attached")
	}
	if list.ArchiveSummary.TopPeer != archivedPeer || list.ArchiveSummary.TopMessage != 7 {
		t.Fatalf("archive summary top = %+v, want peer %+v message 7", list.ArchiveSummary, archivedPeer)
	}
	if list.ArchiveSummary.UnreadPeersCount != 1 || list.ArchiveSummary.UnreadMessagesCount != 3 {
		t.Fatalf("archive summary counts = %+v, want 1 peer / 3 messages", list.ArchiveSummary)
	}
	for _, dialog := range list.Dialogs {
		if dialog.Peer == archivedPeer {
			t.Fatalf("archived dialog leaked into main list: %+v", dialog)
		}
	}

	archived, err := dialogs.GetDialogs(ctx, 1001, domain.DialogFilter{
		HasFolderID: true,
		FolderID:    domain.DialogArchiveFolderID,
		Limit:       10,
	})
	if err != nil {
		t.Fatalf("GetDialogs archive: %v", err)
	}
	if archived.ArchiveSummary != nil {
		t.Fatalf("archive list summary = %+v, want nil", archived.ArchiveSummary)
	}

	paged, err := dialogs.GetDialogs(ctx, 1001, domain.DialogFilter{Limit: 10, OffsetID: 9})
	if err != nil {
		t.Fatalf("GetDialogs paged: %v", err)
	}
	if paged.ArchiveSummary != nil {
		t.Fatalf("paged list summary = %+v, want nil (first page only)", paged.ArchiveSummary)
	}
}

func TestGetDialogsMainListSkipsArchiveSummaryWhenEmpty(t *testing.T) {
	ctx := context.Background()
	dialogStore := memory.NewDialogStore()
	dialogs := NewService(dialogStore)
	if err := dialogStore.Upsert(ctx, 1001, domain.Dialog{
		Peer:           domain.Peer{Type: domain.PeerTypeUser, ID: 2003},
		TopMessage:     9,
		TopMessageDate: 40,
	}); err != nil {
		t.Fatalf("upsert main dialog: %v", err)
	}
	list, err := dialogs.GetDialogs(ctx, 1001, domain.DialogFilter{Limit: 10})
	if err != nil {
		t.Fatalf("GetDialogs: %v", err)
	}
	if list.ArchiveSummary != nil {
		t.Fatalf("archive summary = %+v, want nil when no archived dialogs", list.ArchiveSummary)
	}
}

func TestGetDialogsPinnedOnlyAttachesArchiveSummary(t *testing.T) {
	ctx := context.Background()
	dialogStore := memory.NewDialogStore()
	dialogs := NewService(dialogStore)
	archivedPeer := domain.Peer{Type: domain.PeerTypeUser, ID: 2002}
	if err := dialogStore.Upsert(ctx, 1001, domain.Dialog{
		Peer:           archivedPeer,
		FolderID:       domain.DialogArchiveFolderID,
		TopMessage:     7,
		TopMessageDate: 30,
		UnreadCount:    3,
	}); err != nil {
		t.Fatalf("upsert archived dialog: %v", err)
	}
	// getPinnedDialogs(folder_id=0) path: DrKLO's discovery of the archive row depends entirely
	// on the dialogFolder entry in this response.
	pinned, err := dialogs.GetDialogs(ctx, 1001, domain.DialogFilter{
		PinnedOnly:  true,
		HasFolderID: true,
		FolderID:    domain.DialogMainFolderID,
		Limit:       100,
	})
	if err != nil {
		t.Fatalf("GetDialogs pinned: %v", err)
	}
	if pinned.ArchiveSummary == nil || !pinned.ArchiveSummary.Pinned || pinned.ArchiveSummary.TopPeer != archivedPeer {
		t.Fatalf("pinned archive summary = %+v, want pinned archive entry", pinned.ArchiveSummary)
	}
	// Once the archive row is unpinned it no longer belongs to the pinned set.
	if _, err := dialogStore.SetArchivePinned(ctx, 1001, false); err != nil {
		t.Fatalf("set archive pinned: %v", err)
	}
	unpinned, err := dialogs.GetDialogs(ctx, 1001, domain.DialogFilter{
		PinnedOnly:  true,
		HasFolderID: true,
		FolderID:    domain.DialogMainFolderID,
		Limit:       100,
	})
	if err != nil {
		t.Fatalf("GetDialogs pinned after unpin: %v", err)
	}
	if unpinned.ArchiveSummary != nil {
		t.Fatalf("pinned archive summary after unpin = %+v, want nil", unpinned.ArchiveSummary)
	}
	// The main list's first page still outputs the entry (with the pinned flag at its true value), consistent with the official behavior.
	main, err := dialogs.GetDialogs(ctx, 1001, domain.DialogFilter{Limit: 10})
	if err != nil {
		t.Fatalf("GetDialogs main: %v", err)
	}
	if main.ArchiveSummary == nil || main.ArchiveSummary.Pinned {
		t.Fatalf("main archive summary = %+v, want entry with pinned=false", main.ArchiveSummary)
	}
	// exclude_pinned requests carry no entry per official semantics.
	excluded, err := dialogs.GetDialogs(ctx, 1001, domain.DialogFilter{ExcludePinned: true, Limit: 10})
	if err != nil {
		t.Fatalf("GetDialogs exclude pinned: %v", err)
	}
	if excluded.ArchiveSummary != nil {
		t.Fatalf("exclude_pinned archive summary = %+v, want nil", excluded.ArchiveSummary)
	}
}
