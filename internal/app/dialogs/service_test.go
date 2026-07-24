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
	listByUserCalls    int
	listByPeersCalls   int
	listByPeersBatches [][]domain.Peer
	listDraftsCalls    int
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

func (s *countingDialogStore) ListDrafts(ctx context.Context, userID int64, limit int) ([]domain.DialogDraft, error) {
	s.listDraftsCalls++
	return s.DialogStore.ListDrafts(ctx, userID, limit)
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
	if counting.listByPeersCalls != 1 || counting.listDraftsCalls != 1 {
		t.Fatalf("store calls ListByPeers/ListDrafts = %d/%d, want 1/1 after cache hit", counting.listByPeersCalls, counting.listDraftsCalls)
	}

	versions.hashes[store.ReadModelKey{Model: dialogLightReadModel, OwnerUserID: ownerID, PeerType: peer.Type, PeerID: peer.ID}] = 202
	if _, err := dialogs.GetPeerDialogs(ctx, ownerID, []domain.Peer{peer}); err != nil {
		t.Fatalf("third GetPeerDialogs after hash bump: %v", err)
	}
	if counting.listByPeersCalls != 2 || counting.listDraftsCalls != 2 {
		t.Fatalf("store calls after hash bump = %d/%d, want 2/2", counting.listByPeersCalls, counting.listDraftsCalls)
	}

	if _, err := dialogs.SaveDraft(ctx, ownerID, domain.DialogDraft{Peer: peer, Date: 72, Message: "new draft"}); err != nil {
		t.Fatalf("service SaveDraft: %v", err)
	}
	if _, err := dialogs.GetPeerDialogs(ctx, ownerID, []domain.Peer{peer}); err != nil {
		t.Fatalf("GetPeerDialogs after service invalidation: %v", err)
	}
	if counting.listByPeersCalls != 3 || counting.listDraftsCalls != 3 {
		t.Fatalf("store calls after explicit invalidation = %d/%d, want 3/3", counting.listByPeersCalls, counting.listDraftsCalls)
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

func TestGetPeerDialogsCachesChannelPeerReadModelByCompositeHash(t *testing.T) {
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
		{Model: channelBaseReadModel, OwnerUserID: 0, PeerType: peer.Type, PeerID: peer.ID}:         11,
		{Model: channelMemberReadModel, OwnerUserID: ownerID, PeerType: peer.Type, PeerID: peer.ID}: 22,
		{Model: dialogLightReadModel, OwnerUserID: ownerID, PeerType: peer.Type, PeerID: peer.ID}:   33,
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
	if channelStore.getChannelDialogsCalls != 1 || dialogStore.listDraftsCalls != 1 {
		t.Fatalf("store calls GetChannelDialogs/ListDrafts = %d/%d, want 1/1 after channel cache hit",
			channelStore.getChannelDialogsCalls, dialogStore.listDraftsCalls)
	}

	versions.hashes[store.ReadModelKey{Model: channelMemberReadModel, OwnerUserID: ownerID, PeerType: peer.Type, PeerID: peer.ID}] = 44
	if _, err := dialogs.GetPeerDialogs(ctx, ownerID, []domain.Peer{peer}); err != nil {
		t.Fatalf("third GetPeerDialogs after member hash bump: %v", err)
	}
	if channelStore.getChannelDialogsCalls != 2 || dialogStore.listDraftsCalls != 2 {
		t.Fatalf("store calls after member hash bump = %d/%d, want 2/2",
			channelStore.getChannelDialogsCalls, dialogStore.listDraftsCalls)
	}

	if _, err := dialogs.SaveDraft(ctx, ownerID, domain.DialogDraft{Peer: peer, Date: 1700003220, Message: "channel draft"}); err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}
	if _, err := dialogs.GetPeerDialogs(ctx, ownerID, []domain.Peer{peer}); err != nil {
		t.Fatalf("GetPeerDialogs after draft invalidation: %v", err)
	}
	if channelStore.getChannelDialogsCalls != 3 || dialogStore.listDraftsCalls != 3 {
		t.Fatalf("store calls after draft invalidation = %d/%d, want 3/3",
			channelStore.getChannelDialogsCalls, dialogStore.listDraftsCalls)
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
	// 归档对话不再出现在主列表（缺省 folder 视为 folder 0），主列表以
	// ArchiveSummary 聚合呈现归档状态。
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
	// 归档清除 pinned（对齐 TDesktop History::setFolderPointer 的本地 unpin），
	// unread_mark 保留。
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
	// getPinnedDialogs(folder_id=0) 路径：DrKLO 的 archive 行发现完全依赖
	// 该响应里的 dialogFolder 条目。
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
	// archive 行被 unpin 后不属于 pinned 集合。
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
	// 主列表第一页仍输出条目（pinned flag 用真值），与官方一致。
	main, err := dialogs.GetDialogs(ctx, 1001, domain.DialogFilter{Limit: 10})
	if err != nil {
		t.Fatalf("GetDialogs main: %v", err)
	}
	if main.ArchiveSummary == nil || main.ArchiveSummary.Pinned {
		t.Fatalf("main archive summary = %+v, want entry with pinned=false", main.ArchiveSummary)
	}
	// exclude_pinned 请求按官方语义不带条目。
	excluded, err := dialogs.GetDialogs(ctx, 1001, domain.DialogFilter{ExcludePinned: true, Limit: 10})
	if err != nil {
		t.Fatalf("GetDialogs exclude pinned: %v", err)
	}
	if excluded.ArchiveSummary != nil {
		t.Fatalf("exclude_pinned archive summary = %+v, want nil", excluded.ArchiveSummary)
	}
}
