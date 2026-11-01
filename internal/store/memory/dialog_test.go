package memory

import (
	"context"
	"testing"

	"telesrv/internal/domain"
)

func TestDialogStoreFiltersAndPaginates(t *testing.T) {
	ctx := context.Background()
	store := NewDialogStore()
	userID := int64(100)
	list := domain.DialogList{
		Dialogs: []domain.Dialog{
			{Peer: domain.Peer{Type: domain.PeerTypeUser, ID: 1}, TopMessage: 10, TopMessageDate: 1000, Pinned: true},
			{Peer: domain.Peer{Type: domain.PeerTypeUser, ID: 2}, TopMessage: 9, TopMessageDate: 900},
			{Peer: domain.Peer{Type: domain.PeerTypeUser, ID: 3}, TopMessage: 8, TopMessageDate: 800},
		},
		Messages: []domain.Message{
			{ID: 10, Peer: domain.Peer{Type: domain.PeerTypeUser, ID: 1}, Body: "pinned"},
			{ID: 9, Peer: domain.Peer{Type: domain.PeerTypeUser, ID: 2}, Body: "first"},
			{ID: 8, Peer: domain.Peer{Type: domain.PeerTypeUser, ID: 3}, Body: "second"},
		},
	}
	if err := store.SaveList(ctx, userID, list); err != nil {
		t.Fatalf("SaveList: %v", err)
	}

	first, err := store.ListByUser(ctx, userID, domain.DialogFilter{ExcludePinned: true, Limit: 1})
	if err != nil {
		t.Fatalf("ListByUser first page: %v", err)
	}
	if first.Count != 2 || len(first.Dialogs) != 1 || first.Dialogs[0].Peer.ID != 2 || len(first.Messages) != 1 || first.Messages[0].ID != 9 {
		t.Fatalf("first page = %+v, want peer 2 with count 2 and top message", first)
	}

	next, err := store.ListByUser(ctx, userID, domain.DialogFilter{
		ExcludePinned: true,
		OffsetDate:    first.Dialogs[0].TopMessageDate,
		OffsetID:      first.Dialogs[0].TopMessage,
		HasOffsetPeer: true,
		OffsetPeer:    first.Dialogs[0].Peer,
		Limit:         10,
	})
	if err != nil {
		t.Fatalf("ListByUser next page: %v", err)
	}
	if next.Count != 2 || len(next.Dialogs) != 1 || next.Dialogs[0].Peer.ID != 3 || len(next.Messages) != 1 || next.Messages[0].ID != 8 {
		t.Fatalf("next page = %+v, want peer 3 with count 2 and top message", next)
	}
}

func TestDialogStorePinnedOrderSortsHighestFirst(t *testing.T) {
	ctx := context.Background()
	store := NewDialogStore()
	userID := int64(100)
	oldPinned := domain.Peer{Type: domain.PeerTypeUser, ID: 1}
	newPinned := domain.Peer{Type: domain.PeerTypeUser, ID: 2}
	if err := store.SaveList(ctx, userID, domain.DialogList{
		Dialogs: []domain.Dialog{
			{
				Peer:           oldPinned,
				TopMessage:     20,
				TopMessageDate: 2000,
				Pinned:         true,
				PinnedOrder:    1,
			},
			{
				Peer:           newPinned,
				TopMessage:     10,
				TopMessageDate: 1000,
				Pinned:         true,
				PinnedOrder:    2,
			},
		},
	}); err != nil {
		t.Fatalf("SaveList: %v", err)
	}

	got, err := store.ListByUser(ctx, userID, domain.DialogFilter{PinnedOnly: true, Limit: 10})
	if err != nil {
		t.Fatalf("ListByUser pinned: %v", err)
	}
	if len(got.Dialogs) != 2 || got.Dialogs[0].Peer != newPinned || got.Dialogs[1].Peer != oldPinned {
		t.Fatalf("pinned dialogs = %+v, want highest pinned_order first", got.Dialogs)
	}
}

func TestDialogStoreFoldersAndCustomFilters(t *testing.T) {
	ctx := context.Background()
	store := NewDialogStore()
	userID := int64(100)
	contactPeer := domain.Peer{Type: domain.PeerTypeUser, ID: 1}
	archivedPeer := domain.Peer{Type: domain.PeerTypeUser, ID: 2}
	strangerPeer := domain.Peer{Type: domain.PeerTypeUser, ID: 3}
	if err := store.SaveList(ctx, userID, domain.DialogList{
		Dialogs: []domain.Dialog{
			{Peer: contactPeer, TopMessage: 10, TopMessageDate: 1000},
			{Peer: archivedPeer, TopMessage: 9, TopMessageDate: 900, FolderID: domain.DialogArchiveFolderID},
			{Peer: strangerPeer, TopMessage: 8, TopMessageDate: 800, UnreadCount: 1},
		},
		Users: []domain.User{
			{ID: contactPeer.ID, Contact: true},
			{ID: archivedPeer.ID, Contact: true},
			{ID: strangerPeer.ID},
		},
	}); err != nil {
		t.Fatalf("SaveList: %v", err)
	}

	main, err := store.ListByUser(ctx, userID, domain.DialogFilter{HasFolderID: true, FolderID: domain.DialogMainFolderID, Limit: 10})
	if err != nil {
		t.Fatalf("ListByUser main: %v", err)
	}
	if len(main.Dialogs) != 2 || main.Dialogs[0].Peer != contactPeer || main.Dialogs[1].Peer != strangerPeer {
		t.Fatalf("main dialogs = %+v, want non-archived dialogs", main.Dialogs)
	}
	archive, err := store.ListByUser(ctx, userID, domain.DialogFilter{HasFolderID: true, FolderID: domain.DialogArchiveFolderID, Limit: 10})
	if err != nil {
		t.Fatalf("ListByUser archive: %v", err)
	}
	if len(archive.Dialogs) != 1 || archive.Dialogs[0].Peer != archivedPeer {
		t.Fatalf("archive dialogs = %+v, want archived peer", archive.Dialogs)
	}

	folder := domain.DialogFolder{
		ID:              2,
		Contacts:        true,
		ExcludeArchived: true,
		IncludePeers:    []domain.DialogFolderPeer{{Peer: strangerPeer}},
	}
	if err := store.UpsertFolder(ctx, userID, folder); err != nil {
		t.Fatalf("UpsertFolder: %v", err)
	}
	custom, err := store.ListByUser(ctx, userID, domain.DialogFilter{HasFolderID: true, FolderID: 2, Folder: &folder, Limit: 10})
	if err != nil {
		t.Fatalf("ListByUser custom: %v", err)
	}
	if len(custom.Dialogs) != 2 || custom.Dialogs[0].Peer != contactPeer || custom.Dialogs[1].Peer != strangerPeer {
		t.Fatalf("custom dialogs = %+v, want contact plus explicit stranger excluding archived", custom.Dialogs)
	}
}

func TestUserStoreStartsAtTimestampBase(t *testing.T) {
	ctx := context.Background()
	store := NewUserStore()
	u, err := store.Create(ctx, domain.User{
		AccessHash: 1,
		Phone:      "15550000001",
		FirstName:  "Test",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if u.ID != domain.UserIDSequenceBase {
		t.Fatalf("user id = %d, want base %d", u.ID, domain.UserIDSequenceBase)
	}
}

func TestDialogStoreOffsetDateOnlyKeepsEnterpriseCountAndHash(t *testing.T) {
	ctx := context.Background()
	store := NewDialogStore()
	userID := int64(100)
	if err := store.SaveList(ctx, userID, domain.DialogList{
		Dialogs: []domain.Dialog{
			{Peer: domain.Peer{Type: domain.PeerTypeUser, ID: 1}, TopMessage: 10, TopMessageDate: 1000},
			{Peer: domain.Peer{Type: domain.PeerTypeUser, ID: 2}, TopMessage: 9, TopMessageDate: 900},
			{Peer: domain.Peer{Type: domain.PeerTypeUser, ID: 3}, TopMessage: 8, TopMessageDate: 800},
		},
		Messages: []domain.Message{
			{ID: 10, Peer: domain.Peer{Type: domain.PeerTypeUser, ID: 1}, Body: "first"},
			{ID: 9, Peer: domain.Peer{Type: domain.PeerTypeUser, ID: 2}, Body: "second"},
			{ID: 8, Peer: domain.Peer{Type: domain.PeerTypeUser, ID: 3}, Body: "third"},
		},
	}); err != nil {
		t.Fatalf("SaveList: %v", err)
	}

	all, err := store.ListByUser(ctx, userID, domain.DialogFilter{Limit: 10})
	if err != nil {
		t.Fatalf("ListByUser all: %v", err)
	}
	page, err := store.ListByUser(ctx, userID, domain.DialogFilter{OffsetDate: 900, Limit: 10})
	if err != nil {
		t.Fatalf("ListByUser offset date: %v", err)
	}
	if page.Count != 3 || page.Hash != all.Hash {
		t.Fatalf("page summary = count %d hash %d, want full count/hash %d/%d", page.Count, page.Hash, all.Count, all.Hash)
	}
	if len(page.Dialogs) != 1 || page.Dialogs[0].Peer.ID != 3 || len(page.Messages) != 1 || page.Messages[0].ID != 8 {
		t.Fatalf("page = %+v, want only dialog after offset date", page)
	}
}

func TestDialogStoreEmptyPageKeepsEnterpriseCountAndHash(t *testing.T) {
	ctx := context.Background()
	store := NewDialogStore()
	userID := int64(100)
	if err := store.SaveList(ctx, userID, domain.DialogList{
		Dialogs: []domain.Dialog{
			{Peer: domain.Peer{Type: domain.PeerTypeUser, ID: 1}, TopMessage: 10, TopMessageDate: 1000},
			{Peer: domain.Peer{Type: domain.PeerTypeUser, ID: 2}, TopMessage: 9, TopMessageDate: 900},
		},
	}); err != nil {
		t.Fatalf("SaveList: %v", err)
	}

	all, err := store.ListByUser(ctx, userID, domain.DialogFilter{Limit: 10})
	if err != nil {
		t.Fatalf("ListByUser all: %v", err)
	}
	empty, err := store.ListByUser(ctx, userID, domain.DialogFilter{
		OffsetDate:    900,
		OffsetID:      9,
		HasOffsetPeer: true,
		OffsetPeer:    domain.Peer{Type: domain.PeerTypeUser, ID: 2},
		Limit:         10,
	})
	if err != nil {
		t.Fatalf("ListByUser empty page: %v", err)
	}
	if empty.Count != 2 || empty.Hash != all.Hash || len(empty.Dialogs) != 0 {
		t.Fatalf("empty page = %+v, want no page rows but full count/hash", empty)
	}
}

func TestDialogStoreListByPeersReturnsExistingAndPlaceholders(t *testing.T) {
	ctx := context.Background()
	store := NewDialogStore()
	userID := int64(100)
	official := domain.Peer{Type: domain.PeerTypeUser, ID: domain.OfficialSystemUserID}
	missing := domain.Peer{Type: domain.PeerTypeUser, ID: 1000000002}
	if err := store.SaveList(ctx, userID, domain.DialogList{
		Dialogs: []domain.Dialog{
			{Peer: official, TopMessage: 10, TopMessageDate: 1000, UnreadCount: 1},
		},
		Messages: []domain.Message{
			{ID: 10, Peer: official, From: official, Body: "login"},
		},
		Users: []domain.User{domain.OfficialSystemUser()},
	}); err != nil {
		t.Fatalf("SaveList: %v", err)
	}

	got, err := store.ListByPeers(ctx, userID, []domain.Peer{official, missing, official})
	if err != nil {
		t.Fatalf("ListByPeers: %v", err)
	}
	if got.Count != 2 || len(got.Dialogs) != 2 {
		t.Fatalf("dialogs = %+v, want existing official and missing placeholder", got)
	}
	if got.Dialogs[0].Peer != official || got.Dialogs[0].TopMessage != 10 {
		t.Fatalf("first dialog = %+v, want official top message", got.Dialogs[0])
	}
	if got.Dialogs[1].Peer != missing || got.Dialogs[1].TopMessage != 0 {
		t.Fatalf("second dialog = %+v, want missing placeholder", got.Dialogs[1])
	}
	if len(got.Messages) != 1 || got.Messages[0].ID != 10 {
		t.Fatalf("messages = %+v, want only official top message", got.Messages)
	}
	if len(got.Users) != 1 || got.Users[0].ID != domain.OfficialSystemUserID {
		t.Fatalf("users = %+v, want official user", got.Users)
	}
}

func TestDialogStoreSetUnreadMarkOnlyReportsRealChanges(t *testing.T) {
	ctx := context.Background()
	store := NewDialogStore()
	userID := int64(100)
	peer := domain.Peer{Type: domain.PeerTypeUser, ID: 200}
	missing := domain.Peer{Type: domain.PeerTypeUser, ID: 999}
	if err := store.SaveList(ctx, userID, domain.DialogList{
		Dialogs: []domain.Dialog{{Peer: peer, TopMessage: 10, TopMessageDate: 1000}},
	}); err != nil {
		t.Fatalf("SaveList: %v", err)
	}

	// Setting it the first time is a real change.
	if changed, err := store.SetUnreadMark(ctx, userID, peer, true); err != nil || !changed {
		t.Fatalf("first mark = (%v, %v), want (true, nil)", changed, err)
	}
	// Re-marking the same value must not count as changed (value guard; otherwise the caller records a ghost durable event and pushes an extra update).
	if changed, err := store.SetUnreadMark(ctx, userID, peer, true); err != nil || changed {
		t.Fatalf("repeat same value = (%v, %v), want (false, nil)", changed, err)
	}
	// Flipping back to the opposite value is a real change.
	if changed, err := store.SetUnreadMark(ctx, userID, peer, false); err != nil || !changed {
		t.Fatalf("flip value = (%v, %v), want (true, nil)", changed, err)
	}
	// A non-existent dialog row cannot be marked.
	if changed, err := store.SetUnreadMark(ctx, userID, missing, true); err != nil || changed {
		t.Fatalf("missing peer = (%v, %v), want (false, nil)", changed, err)
	}
}

func TestDialogStoreMarkReadClampsFutureMaxID(t *testing.T) {
	ctx := context.Background()
	store := NewDialogStore()
	userID := int64(100)
	peer := domain.Peer{Type: domain.PeerTypeUser, ID: 200}
	if err := store.SaveList(ctx, userID, domain.DialogList{
		Dialogs: []domain.Dialog{{
			Peer:            peer,
			TopMessage:      10,
			TopMessageDate:  1000,
			ReadInboxMaxID:  4,
			UnreadCount:     2,
			UnreadMentions:  1,
			UnreadReactions: 1,
			UnreadMark:      true,
		}},
	}); err != nil {
		t.Fatalf("SaveList: %v", err)
	}

	read, err := store.MarkRead(ctx, userID, peer, 8)
	if err != nil {
		t.Fatalf("MarkRead partial: %v", err)
	}
	if read.MaxID != 8 || read.StillUnreadCount != 2 {
		t.Fatalf("partial read = %+v, want max 8 with existing unread count preserved", read)
	}
	list, err := store.ListByPeers(ctx, userID, []domain.Peer{peer})
	if err != nil {
		t.Fatalf("ListByPeers partial: %v", err)
	}
	if len(list.Dialogs) != 1 || list.Dialogs[0].ReadInboxMaxID != 8 || list.Dialogs[0].UnreadCount != 2 {
		t.Fatalf("dialog after partial read = %+v, want read 8 and unread preserved", list.Dialogs)
	}

	read, err = store.MarkRead(ctx, userID, peer, domain.MaxMessageBoxID)
	if err != nil {
		t.Fatalf("MarkRead: %v", err)
	}
	if read.MaxID != 10 {
		t.Fatalf("read max = %d, want top message 10", read.MaxID)
	}
	list, err = store.ListByPeers(ctx, userID, []domain.Peer{peer})
	if err != nil {
		t.Fatalf("ListByPeers: %v", err)
	}
	if len(list.Dialogs) != 1 {
		t.Fatalf("dialogs = %+v, want one dialog", list.Dialogs)
	}
	dialog := list.Dialogs[0]
	if dialog.ReadInboxMaxID != 10 || dialog.UnreadCount != 0 || dialog.UnreadMentions != 0 || dialog.UnreadReactions != 0 || dialog.UnreadMark {
		t.Fatalf("dialog after read = %+v, want read clamped to top and unread cleared", dialog)
	}
}
