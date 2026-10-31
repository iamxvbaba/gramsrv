package postgres

import (
	"context"
	"reflect"
	"testing"

	contactsapp "telesrv/internal/app/contacts"
	"telesrv/internal/domain"
)

func TestContactProfilesOwnerScopedRoundTrip(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)

	users := NewUserStore(pool)
	owner := createTestUser(t, ctx, users, "+1900"+suffix+"01", "Owner", "One")
	altOwner := createTestUser(t, ctx, users, "+1900"+suffix+"02", "AltOwner", "Two")
	friend := createTestUser(t, ctx, users, "+1900"+suffix+"03", "Canonical", "Friend")
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = ANY($1::bigint[])", []int64{owner.ID, altOwner.ID, friend.ID})
	})

	contactStore := NewContactStore(pool)
	contacts := contactsapp.NewService(contactStore, users)
	if _, err := contacts.AddContact(ctx, owner.ID, domain.ContactInput{
		ContactUserID: friend.ID,
		Phone:         "10001",
		FirstName:     "OwnerRemark",
		LastName:      "A",
		Note:          "first note",
	}); err != nil {
		t.Fatalf("owner add contact: %v", err)
	}
	if _, err := contacts.AddContact(ctx, altOwner.ID, domain.ContactInput{
		ContactUserID: friend.ID,
		Phone:         "20002",
		FirstName:     "AltRemark",
		LastName:      "B",
		Note:          "alt note",
	}); err != nil {
		t.Fatalf("alt owner add contact: %v", err)
	}

	ownerList, _, err := contacts.GetContacts(ctx, owner.ID, 0)
	if err != nil {
		t.Fatalf("owner contacts: %v", err)
	}
	altList, _, err := contacts.GetContacts(ctx, altOwner.ID, 0)
	if err != nil {
		t.Fatalf("alt contacts: %v", err)
	}
	if got := ownerList.Contacts[0].User.FirstName; got != "OwnerRemark" {
		t.Fatalf("owner contact first name = %q, want owner remark", got)
	}
	if got := altList.Contacts[0].User.FirstName; got != "AltRemark" {
		t.Fatalf("alt contact first name = %q, want alt remark", got)
	}
	if ownerList.Hash == 0 || altList.Hash == 0 || ownerList.Hash == altList.Hash {
		t.Fatalf("contact hashes owner=%d alt=%d, want non-zero owner-specific hashes", ownerList.Hash, altList.Hash)
	}

	beforeHash := ownerList.Hash
	updated, err := contacts.UpdateContactNote(ctx, owner.ID, friend.ID, "fresh note", []domain.MessageEntity{
		{Type: domain.MessageEntityBold, Offset: 0, Length: 5},
	})
	if err != nil {
		t.Fatalf("update contact note: %v", err)
	}
	if updated.Note != "fresh note" || len(updated.NoteEntities) != 1 {
		t.Fatalf("updated contact = %+v, want note with entity", updated)
	}
	ownerList, _, err = contacts.GetContacts(ctx, owner.ID, 0)
	if err != nil {
		t.Fatalf("owner contacts after note: %v", err)
	}
	if ownerList.Hash == beforeHash {
		t.Fatalf("owner contact hash did not change after note update: %d", ownerList.Hash)
	}
	altList, _, err = contacts.GetContacts(ctx, altOwner.ID, 0)
	if err != nil {
		t.Fatalf("alt contacts after owner note: %v", err)
	}
	if altList.Contacts[0].Note != "alt note" {
		t.Fatalf("alt contact note = %q, want isolated alt note", altList.Contacts[0].Note)
	}

	beforeCloseHash := ownerList.Hash
	result, err := contacts.EditCloseFriends(ctx, owner.ID, []int64{friend.ID, friend.ID, owner.ID, 0, 999999})
	if err != nil {
		t.Fatalf("edit close friends: %v", err)
	}
	if got, want := result.AddedUserIDs, []int64{friend.ID}; !reflect.DeepEqual(got, want) {
		t.Fatalf("close friends added = %v, want %v", got, want)
	}
	if len(result.RemovedUserIDs) != 0 {
		t.Fatalf("close friends removed = %v, want empty", result.RemovedUserIDs)
	}
	ownerList, _, err = contacts.GetContacts(ctx, owner.ID, 0)
	if err != nil {
		t.Fatalf("owner contacts after close friends: %v", err)
	}
	if !ownerList.Contacts[0].CloseFriend || !ownerList.Contacts[0].User.CloseFriend {
		t.Fatalf("owner contact close friend = %+v, want true", ownerList.Contacts[0])
	}
	if ownerList.Hash == beforeCloseHash {
		t.Fatalf("owner contact hash did not change after close friend update: %d", ownerList.Hash)
	}
	altList, _, err = contacts.GetContacts(ctx, altOwner.ID, 0)
	if err != nil {
		t.Fatalf("alt contacts after close friends: %v", err)
	}
	if altList.Contacts[0].CloseFriend || altList.Contacts[0].User.CloseFriend {
		t.Fatalf("alt contact close friend = %+v, want false", altList.Contacts[0])
	}
	result, err = contacts.EditCloseFriends(ctx, owner.ID, nil)
	if err != nil {
		t.Fatalf("clear close friends: %v", err)
	}
	if len(result.AddedUserIDs) != 0 {
		t.Fatalf("clear close friends added = %v, want empty", result.AddedUserIDs)
	}
	if got, want := result.RemovedUserIDs, []int64{friend.ID}; !reflect.DeepEqual(got, want) {
		t.Fatalf("clear close friends removed = %v, want %v", got, want)
	}
	ownerList, _, err = contacts.GetContacts(ctx, owner.ID, 0)
	if err != nil {
		t.Fatalf("owner contacts after close friends clear: %v", err)
	}
	if ownerList.Contacts[0].CloseFriend || ownerList.Contacts[0].User.CloseFriend {
		t.Fatalf("owner contact after clear = %+v, want false", ownerList.Contacts[0])
	}

	if _, err := contacts.AddContact(ctx, friend.ID, domain.ContactInput{
		ContactUserID: owner.ID,
		FirstName:     "OwnerBack",
	}); err != nil {
		t.Fatalf("friend reciprocal add: %v", err)
	}
	ownerContact, found, err := contactStore.Get(ctx, owner.ID, friend.ID)
	if err != nil {
		t.Fatalf("get owner contact: %v", err)
	}
	if !found || !ownerContact.Mutual {
		t.Fatalf("owner contact = %+v found=%v, want mutual after reciprocal add", ownerContact, found)
	}
	friendContact, found, err := contactStore.Get(ctx, friend.ID, owner.ID)
	if err != nil {
		t.Fatalf("get friend contact: %v", err)
	}
	if !found || !friendContact.Mutual {
		t.Fatalf("friend contact = %+v found=%v, want mutual after reciprocal add", friendContact, found)
	}

	deleted, err := contacts.DeleteContacts(ctx, owner.ID, []int64{friend.ID})
	if err != nil {
		t.Fatalf("delete owner contact: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("deleted = %d, want 1", deleted)
	}
	if _, found, err := contactStore.Get(ctx, owner.ID, friend.ID); err != nil || found {
		t.Fatalf("owner contact after delete found=%v err=%v, want not found", found, err)
	}
	friendContact, found, err = contactStore.Get(ctx, friend.ID, owner.ID)
	if err != nil {
		t.Fatalf("get friend contact after delete: %v", err)
	}
	if !found || friendContact.Mutual {
		t.Fatalf("friend contact after delete = %+v found=%v, want reverse mutual cleared", friendContact, found)
	}
}

func TestDialogUserViewUsesContactProfileAndDialogFlags(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)

	users := NewUserStore(pool)
	ownerA := createTestUser(t, ctx, users, "+1910"+suffix+"01", "OwnerA", "")
	ownerB := createTestUser(t, ctx, users, "+1910"+suffix+"02", "OwnerB", "")
	friend := createTestUser(t, ctx, users, "+1910"+suffix+"03", "Shared", "Friend")
	other := createTestUser(t, ctx, users, "+1910"+suffix+"04", "Other", "Peer")
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = ANY($1::bigint[])", []int64{ownerA.ID, ownerB.ID, friend.ID, other.ID})
	})

	contacts := contactsapp.NewService(NewContactStore(pool), users)
	if _, err := contacts.AddContact(ctx, ownerA.ID, domain.ContactInput{ContactUserID: friend.ID, FirstName: "RemarkA"}); err != nil {
		t.Fatalf("owner A add contact: %v", err)
	}
	if _, err := contacts.AddContact(ctx, ownerB.ID, domain.ContactInput{ContactUserID: friend.ID, FirstName: "RemarkB"}); err != nil {
		t.Fatalf("owner B add contact: %v", err)
	}

	messages := NewMessageStore(pool)
	if _, err := messages.SendPrivateText(ctx, domain.SendPrivateTextRequest{
		SenderUserID:    friend.ID,
		RecipientUserID: ownerA.ID,
		RandomID:        301,
		Message:         "to owner A",
		Date:            1700000301,
	}); err != nil {
		t.Fatalf("send to owner A: %v", err)
	}
	if _, err := messages.SendPrivateText(ctx, domain.SendPrivateTextRequest{
		SenderUserID:    friend.ID,
		RecipientUserID: ownerB.ID,
		RandomID:        302,
		Message:         "to owner B",
		Date:            1700000302,
	}); err != nil {
		t.Fatalf("send to owner B: %v", err)
	}
	if _, err := messages.SendPrivateText(ctx, domain.SendPrivateTextRequest{
		SenderUserID:    other.ID,
		RecipientUserID: ownerA.ID,
		RandomID:        303,
		Message:         "second dialog",
		Date:            1700000303,
	}); err != nil {
		t.Fatalf("send second dialog: %v", err)
	}

	dialogs := NewDialogStore(pool)
	listA, err := dialogs.ListByUser(ctx, ownerA.ID, domain.DialogFilter{Limit: 10})
	if err != nil {
		t.Fatalf("list owner A dialogs: %v", err)
	}
	listB, err := dialogs.ListByUser(ctx, ownerB.ID, domain.DialogFilter{Limit: 10})
	if err != nil {
		t.Fatalf("list owner B dialogs: %v", err)
	}
	userA, ok := findDialogUserByID(listA.Users, friend.ID)
	if !ok || userA.FirstName != "RemarkA" || !userA.Contact {
		t.Fatalf("owner A dialog user = %+v found=%v, want RemarkA contact", userA, ok)
	}
	userB, ok := findDialogUserByID(listB.Users, friend.ID)
	if !ok || userB.FirstName != "RemarkB" || !userB.Contact {
		t.Fatalf("owner B dialog user = %+v found=%v, want RemarkB contact", userB, ok)
	}

	friendPeer := domain.Peer{Type: domain.PeerTypeUser, ID: friend.ID}
	otherPeer := domain.Peer{Type: domain.PeerTypeUser, ID: other.ID}
	if changed, _, err := dialogs.SetPinned(ctx, ownerA.ID, friendPeer, true); err != nil || !changed {
		t.Fatalf("pin friend changed=%v err=%v, want changed", changed, err)
	}
	if changed, _, err := dialogs.SetPinned(ctx, ownerA.ID, otherPeer, true); err != nil || !changed {
		t.Fatalf("pin other changed=%v err=%v, want changed", changed, err)
	}
	if changed, err := dialogs.ReorderPinned(ctx, ownerA.ID, domain.DialogMainFolderID, []domain.Peer{otherPeer, friendPeer}, true); err != nil || changed {
		t.Fatalf("reorder pinned = changed %v err %v, want no-op", changed, err)
	}
	pinned, err := dialogs.ListByUser(ctx, ownerA.ID, domain.DialogFilter{PinnedOnly: true, Limit: 10})
	if err != nil {
		t.Fatalf("list pinned dialogs: %v", err)
	}
	// The protocol contract only looks at return order (TL dialog does not expose pinned_order);
	// internally the first entry holds the largest pinned_order (reorder writes cardinality-pos+1
	// and reads DESC, consistent with SetPinned's MAX+1 "a new pin goes to the very front").
	if len(pinned.Dialogs) != 2 || pinned.Dialogs[0].Peer != otherPeer || pinned.Dialogs[0].PinnedOrder != 2 || pinned.Dialogs[1].Peer != friendPeer || pinned.Dialogs[1].PinnedOrder != 1 {
		t.Fatalf("pinned dialogs = %+v, want other then friend with descending internal order", pinned.Dialogs)
	}

	if changed, err := dialogs.SetUnreadMark(ctx, ownerA.ID, friendPeer, true); err != nil || !changed {
		t.Fatalf("mark unread changed=%v err=%v, want changed", changed, err)
	}
	marks, err := dialogs.ListUnreadMarked(ctx, ownerA.ID)
	if err != nil {
		t.Fatalf("list unread marks: %v", err)
	}
	if !containsPeer(marks, friendPeer) {
		t.Fatalf("unread marks = %+v, want friend peer", marks)
	}
	read, err := dialogs.MarkRead(ctx, ownerA.ID, friendPeer, domain.MaxMessageBoxID)
	if err != nil {
		t.Fatalf("mark read: %v", err)
	}
	peerDialogs, err := dialogs.ListByPeers(ctx, ownerA.ID, []domain.Peer{friendPeer})
	if err != nil {
		t.Fatalf("list peer dialogs after read: %v", err)
	}
	if len(peerDialogs.Dialogs) != 1 || peerDialogs.Dialogs[0].UnreadMark || peerDialogs.Dialogs[0].ReadInboxMaxID != peerDialogs.Dialogs[0].TopMessage || read.MaxID != peerDialogs.Dialogs[0].TopMessage {
		t.Fatalf("peer dialog after read = %+v read=%+v, want unread mark cleared and read clamped to top", peerDialogs.Dialogs, read)
	}

	if changed, err := dialogs.SetPeerSettingsBarHidden(ctx, ownerA.ID, friendPeer); err != nil || !changed {
		t.Fatalf("hide peer settings bar changed=%v err=%v, want changed", changed, err)
	}
	hidden, err := dialogs.PeerSettingsBarHidden(ctx, ownerA.ID, friendPeer)
	if err != nil {
		t.Fatalf("peer settings bar hidden: %v", err)
	}
	if !hidden {
		t.Fatal("peer settings bar hidden = false, want true")
	}
}

func TestDialogStoreMarkReadClampsAndRecomputesUnread(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)

	users := NewUserStore(pool)
	sender := createTestUser(t, ctx, users, "+1911"+suffix+"01", "DialogReadSender", "")
	recipient := createTestUser(t, ctx, users, "+1911"+suffix+"02", "DialogReadRecipient", "")
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = ANY($1::bigint[])", []int64{sender.ID, recipient.ID})
	})

	messages := NewMessageStore(pool)
	sent := make([]domain.SendPrivateTextResult, 0, 3)
	for i, body := range []string{"one", "two", "three"} {
		got, err := messages.SendPrivateText(ctx, domain.SendPrivateTextRequest{
			SenderUserID:    sender.ID,
			RecipientUserID: recipient.ID,
			RandomID:        739100 + int64(i),
			Message:         body,
			Date:            1700000390 + i,
		})
		if err != nil {
			t.Fatalf("SendPrivateText %q: %v", body, err)
		}
		sent = append(sent, got)
	}

	dialogs := NewDialogStore(pool)
	peer := domain.Peer{Type: domain.PeerTypeUser, ID: sender.ID}
	partial, err := dialogs.MarkRead(ctx, recipient.ID, peer, sent[1].RecipientMessage.ID)
	if err != nil {
		t.Fatalf("partial MarkRead: %v", err)
	}
	if partial.MaxID != sent[1].RecipientMessage.ID || partial.StillUnreadCount != 1 {
		t.Fatalf("partial read = %+v, want max second and one unread", partial)
	}
	list, err := dialogs.ListByPeers(ctx, recipient.ID, []domain.Peer{peer})
	if err != nil {
		t.Fatalf("ListByPeers partial: %v", err)
	}
	if len(list.Dialogs) != 1 || list.Dialogs[0].ReadInboxMaxID != sent[1].RecipientMessage.ID || list.Dialogs[0].UnreadCount != 1 {
		t.Fatalf("dialog after partial read = %+v, want read second and one unread", list.Dialogs)
	}

	full, err := dialogs.MarkRead(ctx, recipient.ID, peer, domain.MaxMessageBoxID)
	if err != nil {
		t.Fatalf("future MarkRead: %v", err)
	}
	if full.MaxID != sent[2].RecipientMessage.ID || full.StillUnreadCount != 0 {
		t.Fatalf("full read = %+v, want clamped to top and unread zero", full)
	}
	list, err = dialogs.ListByPeers(ctx, recipient.ID, []domain.Peer{peer})
	if err != nil {
		t.Fatalf("ListByPeers full: %v", err)
	}
	if len(list.Dialogs) != 1 || list.Dialogs[0].ReadInboxMaxID != sent[2].RecipientMessage.ID || list.Dialogs[0].UnreadCount != 0 {
		t.Fatalf("dialog after full read = %+v, want read top and unread zero", list.Dialogs)
	}
}

func createTestUser(t *testing.T, ctx context.Context, users *UserStore, phone, firstName, lastName string) domain.User {
	t.Helper()
	user, err := users.Create(ctx, domain.User{
		AccessHash: int64(len(phone) + len(firstName)*100 + len(lastName)*1000),
		Phone:      phone,
		FirstName:  firstName,
		LastName:   lastName,
	})
	if err != nil {
		t.Fatalf("create user %s: %v", phone, err)
	}
	return user
}

func findDialogUserByID(users []domain.User, id int64) (domain.User, bool) {
	for _, user := range users {
		if user.ID == id {
			return user, true
		}
	}
	return domain.User{}, false
}

func containsPeer(peers []domain.Peer, want domain.Peer) bool {
	for _, peer := range peers {
		if peer == want {
			return true
		}
	}
	return false
}
