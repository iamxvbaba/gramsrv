package contacts

import (
	"context"
	"errors"
	"reflect"
	"testing"

	privacyapp "telesrv/internal/app/privacy"
	"telesrv/internal/domain"
	"telesrv/internal/store"
	"telesrv/internal/store/memory"
)

type serviceCountingContactStore struct {
	store.ContactStore
	listCalls int
}

func (s *serviceCountingContactStore) ListByUser(ctx context.Context, userID int64) (domain.ContactList, error) {
	s.listCalls++
	return s.ContactStore.ListByUser(ctx, userID)
}

type serviceCountingUserStore struct {
	store.UserStore
	byIDsCalls int
}

func (s *serviceCountingUserStore) ByIDs(ctx context.Context, ids []int64) ([]domain.User, error) {
	s.byIDsCalls++
	return s.UserStore.ByIDs(ctx, ids)
}

type fakeReadModelVersions struct {
	hash  int64
	found bool
}

func (f *fakeReadModelVersions) ReadModelHash(_ context.Context, _ string, _ int64, _ domain.PeerType, _ int64) (int64, bool, error) {
	return f.hash, f.found, nil
}

func (f *fakeReadModelVersions) ReadModelHashes(ctx context.Context, keys []store.ReadModelKey) (map[store.ReadModelKey]int64, error) {
	out := make(map[store.ReadModelKey]int64, len(keys))
	for _, key := range keys {
		hash, found, err := f.ReadModelHash(ctx, key.Model, key.OwnerUserID, key.PeerType, key.PeerID)
		if err != nil {
			return nil, err
		}
		if found {
			out[key] = hash
		}
	}
	return out, nil
}

func TestGetContactsReturnsNotModifiedFromReadModelHashWithoutLoadingList(t *testing.T) {
	ctx := context.Background()
	base := memory.NewContactStore()
	counting := &serviceCountingContactStore{ContactStore: base}
	versions := &fakeReadModelVersions{hash: 99101, found: true}
	svc := NewService(counting).Configure(WithReadModelVersions(versions))

	list, notModified, err := svc.GetContacts(ctx, 1, versions.hash)
	if err != nil {
		t.Fatalf("GetContacts: %v", err)
	}
	if !notModified {
		t.Fatalf("notModified = false, want true")
	}
	if list.Hash != versions.hash {
		t.Fatalf("notModified list hash = %d, want %d", list.Hash, versions.hash)
	}
	if counting.listCalls != 0 {
		t.Fatalf("ListByUser calls = %d, want 0 on hash hit", counting.listCalls)
	}
}

func TestGetContactsCachesProjectedReadModelAndRejectsStaleHash(t *testing.T) {
	ctx := context.Background()
	users := memory.NewUserStore()
	owner, err := users.Create(ctx, domain.User{Phone: "100", FirstName: "Owner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	target, err := users.Create(ctx, domain.User{Phone: "101", FirstName: "Target"})
	if err != nil {
		t.Fatalf("create target: %v", err)
	}
	base := memory.NewContactStore()
	if _, err := base.Upsert(ctx, owner.ID, domain.ContactInput{ContactUserID: target.ID, FirstName: "Saved"}); err != nil {
		t.Fatalf("upsert contact: %v", err)
	}
	counting := &serviceCountingContactStore{ContactStore: base}
	countingUsers := &serviceCountingUserStore{UserStore: users}
	versions := &fakeReadModelVersions{hash: 12345, found: true}
	svc := NewService(counting, countingUsers).Configure(WithReadModelVersions(versions))

	first, notModified, err := svc.GetContacts(ctx, owner.ID, 0)
	if err != nil || notModified {
		t.Fatalf("first GetContacts notModified=%v err=%v", notModified, err)
	}
	if first.Hash != versions.hash || len(first.Contacts) != 1 {
		t.Fatalf("first result hash=%d contacts=%d, want hash %d and one contact", first.Hash, len(first.Contacts), versions.hash)
	}
	second, notModified, err := svc.GetContacts(ctx, owner.ID, 0)
	if err != nil || notModified {
		t.Fatalf("second GetContacts notModified=%v err=%v", notModified, err)
	}
	if second.Hash != versions.hash || counting.listCalls != 1 {
		t.Fatalf("second result hash=%d listCalls=%d, want cached hash %d and one load", second.Hash, counting.listCalls, versions.hash)
	}

	versions.hash = 67890
	third, notModified, err := svc.GetContacts(ctx, owner.ID, 12345)
	if err != nil || notModified {
		t.Fatalf("third GetContacts notModified=%v err=%v", notModified, err)
	}
	if third.Hash != versions.hash {
		t.Fatalf("third hash = %d, want new read-model hash %d", third.Hash, versions.hash)
	}
	if counting.listCalls != 2 {
		t.Fatalf("ListByUser calls after hash change = %d, want 2", counting.listCalls)
	}
	if countingUsers.byIDsCalls != 0 {
		t.Fatalf("Users.ByIDs calls = %d, want 0; presence must stay out of contact read model", countingUsers.byIDsCalls)
	}
}

func TestImportContactsBatchesPhonesAndDedupesUpserts(t *testing.T) {
	ctx := context.Background()
	users := memory.NewUserStore()
	contactsStore := memory.NewContactStore()
	owner, err := users.Create(ctx, domain.User{Phone: "100", FirstName: "Owner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	target, err := users.Create(ctx, domain.User{Phone: "15551234567", FirstName: "Alice"})
	if err != nil {
		t.Fatalf("create target: %v", err)
	}
	svc := NewService(contactsStore, users).Configure(WithPhotoProvider(contactProfilePhotos{
		target.ID: {PhotoID: 9400, DCID: 2},
	}))

	res, err := svc.ImportContacts(ctx, owner.ID, []domain.ContactInput{
		{ClientID: 11, Phone: "+1 (555) 123-4567", FirstName: "A"},
		{ClientID: 12, Phone: "15551234567", FirstName: "Alice Final"},
	})
	if err != nil {
		t.Fatalf("ImportContacts: %v", err)
	}
	if len(res.Imported) != 2 {
		t.Fatalf("imported = %d, want 2", len(res.Imported))
	}
	if res.Imported[0].UserID != target.ID || res.Imported[1].UserID != target.ID {
		t.Fatalf("imported user ids = %+v, want target %d", res.Imported, target.ID)
	}
	if len(res.Contacts) != 1 {
		t.Fatalf("contacts = %d, want 1 deduped upsert", len(res.Contacts))
	}
	if res.Contacts[0].FirstName != "Alice Final" {
		t.Fatalf("contact first name = %q, want final input", res.Contacts[0].FirstName)
	}
	if res.Contacts[0].User.PhotoID != 9400 || res.Contacts[0].User.PhotoDCID != 2 {
		t.Fatalf("imported contact photo = id %d dc %d, want 9400/2", res.Contacts[0].User.PhotoID, res.Contacts[0].User.PhotoDCID)
	}
}

func TestAddContactWithoutPhoneDoesNotBackfillTargetPhone(t *testing.T) {
	ctx := context.Background()
	users := memory.NewUserStore()
	contactsStore := memory.NewContactStore()
	owner, err := users.Create(ctx, domain.User{Phone: "100", FirstName: "Owner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	target, err := users.Create(ctx, domain.User{Phone: "15551234567", FirstName: "Target"})
	if err != nil {
		t.Fatalf("create target: %v", err)
	}
	privacySvc := privacyapp.NewService(memory.NewPrivacyStore(), contactsStore)
	svc := NewService(contactsStore, users).Configure(WithPrivacyEvaluator(privacySvc))

	contact, err := svc.AddContact(ctx, owner.ID, domain.ContactInput{
		ContactUserID: target.ID,
		FirstName:     "Saved",
		Phone:         "",
	})
	if err != nil {
		t.Fatalf("AddContact: %v", err)
	}
	if contact.Phone != "" || contact.User.Phone != "" {
		t.Fatalf("projected contact phone = local %q user %q, want both empty", contact.Phone, contact.User.Phone)
	}
	stored, found, err := contactsStore.Get(ctx, owner.ID, target.ID)
	if err != nil || !found {
		t.Fatalf("stored contact found=%v err=%v", found, err)
	}
	if stored.Phone != "" || stored.User.Phone != "" {
		t.Fatalf("stored contact phone = local %q user %q, want both empty", stored.Phone, stored.User.Phone)
	}
}

func TestImportContactsHonorsAddedByPhoneInOneBatch(t *testing.T) {
	ctx := context.Background()
	users := memory.NewUserStore()
	contactsStore := memory.NewContactStore()
	owner, err := users.Create(ctx, domain.User{Phone: "100", FirstName: "Owner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	target, err := users.Create(ctx, domain.User{Phone: "15551234567", FirstName: "Target"})
	if err != nil {
		t.Fatalf("create target: %v", err)
	}
	privacySvc := privacyapp.NewService(memory.NewPrivacyStore(), contactsStore)
	if _, err := privacySvc.SetRules(ctx, target.ID, domain.PrivacyKeyAddedByPhone, []domain.PrivacyRule{{Kind: domain.PrivacyRuleAllowContacts}}); err != nil {
		t.Fatalf("set AddedByPhone: %v", err)
	}
	svc := NewService(contactsStore, users).Configure(WithPrivacyEvaluator(privacySvc))
	input := []domain.ContactInput{{ClientID: 1, Phone: target.Phone, FirstName: "Saved"}}

	hidden, err := svc.ImportContacts(ctx, owner.ID, input)
	if err != nil {
		t.Fatalf("ImportContacts hidden: %v", err)
	}
	if len(hidden.Imported) != 0 || len(hidden.Contacts) != 0 {
		t.Fatalf("hidden import = %+v, want no resolved target", hidden)
	}

	if _, err := contactsStore.Upsert(ctx, target.ID, domain.ContactInput{
		ContactUserID: owner.ID,
		FirstName:     owner.FirstName,
	}); err != nil {
		t.Fatalf("target add owner: %v", err)
	}
	visible, err := svc.ImportContacts(ctx, owner.ID, input)
	if err != nil {
		t.Fatalf("ImportContacts visible: %v", err)
	}
	if len(visible.Imported) != 1 || visible.Imported[0].UserID != target.ID || len(visible.Contacts) != 1 {
		t.Fatalf("visible import = %+v, want target %d", visible, target.ID)
	}
}

func TestPhoneSearchHonorsAddedByPhone(t *testing.T) {
	ctx := context.Background()
	users := memory.NewUserStore()
	contactsStore := memory.NewContactStore()
	owner, err := users.Create(ctx, domain.User{Phone: "100", FirstName: "Owner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	target, err := users.Create(ctx, domain.User{Phone: "15551234567", FirstName: "Target"})
	if err != nil {
		t.Fatalf("create target: %v", err)
	}
	privacySvc := privacyapp.NewService(memory.NewPrivacyStore(), contactsStore)
	if _, err := privacySvc.SetRules(ctx, target.ID, domain.PrivacyKeyAddedByPhone, []domain.PrivacyRule{{Kind: domain.PrivacyRuleAllowContacts}}); err != nil {
		t.Fatalf("set AddedByPhone: %v", err)
	}
	svc := NewService(contactsStore, users).Configure(WithPrivacyEvaluator(privacySvc))

	hidden, err := svc.Search(ctx, owner.ID, "+1 (555) 123-4567", 50)
	if err != nil {
		t.Fatalf("Search hidden: %v", err)
	}
	if len(hidden.Results) != 0 {
		t.Fatalf("hidden phone search results = %+v, want empty", hidden.Results)
	}
	if _, err := contactsStore.Upsert(ctx, owner.ID, domain.ContactInput{
		ContactUserID: target.ID,
		FirstName:     target.FirstName,
		Phone:         "",
	}); err != nil {
		t.Fatalf("owner add target without phone: %v", err)
	}
	stillHidden, err := svc.Search(ctx, owner.ID, target.Phone, 50)
	if err != nil {
		t.Fatalf("Search owner-only contact: %v", err)
	}
	if len(stillHidden.MyResults)+len(stillHidden.Results) != 0 {
		t.Fatalf("owner-only empty-phone contact search = %+v, want hidden", stillHidden)
	}
	if _, err := contactsStore.Upsert(ctx, owner.ID, domain.ContactInput{
		ContactUserID: target.ID,
		FirstName:     target.FirstName,
		Phone:         target.Phone,
	}); err != nil {
		t.Fatalf("owner save target phone: %v", err)
	}
	knownLocally, err := svc.Search(ctx, owner.ID, target.Phone, 50)
	if err != nil {
		t.Fatalf("Search locally known phone: %v", err)
	}
	if len(knownLocally.MyResults)+len(knownLocally.Results) != 1 {
		t.Fatalf("locally known phone search = %+v, want one local contact", knownLocally)
	}
	if _, err := contactsStore.Upsert(ctx, target.ID, domain.ContactInput{
		ContactUserID: owner.ID,
		FirstName:     owner.FirstName,
	}); err != nil {
		t.Fatalf("target add owner: %v", err)
	}
	visible, err := svc.Search(ctx, owner.ID, target.Phone, 50)
	if err != nil {
		t.Fatalf("Search visible: %v", err)
	}
	if len(visible.Results) != 1 || visible.Results[0].ID != target.ID {
		t.Fatalf("visible phone search = %+v, want target %d", visible.Results, target.ID)
	}
}

func TestGetContactsProjectsCurrentProfilePhoto(t *testing.T) {
	ctx := context.Background()
	users := memory.NewUserStore()
	contactsStore := memory.NewContactStore()
	owner, err := users.Create(ctx, domain.User{Phone: "100", FirstName: "Owner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	target, err := users.Create(ctx, domain.User{Phone: "15551234567", FirstName: "Alice"})
	if err != nil {
		t.Fatalf("create target: %v", err)
	}
	if _, err := contactsStore.Upsert(ctx, owner.ID, domain.ContactInput{
		ContactUserID: target.ID,
		Phone:         "1111",
		FirstName:     "Alice",
		LastName:      "Saved",
	}); err != nil {
		t.Fatalf("upsert contact: %v", err)
	}
	svc := NewService(contactsStore, users).Configure(WithPhotoProvider(contactProfilePhotos{
		target.ID: {PhotoID: 9401, DCID: 2, Stripped: []byte{11, 12}},
	}))

	list, notModified, err := svc.GetContacts(ctx, owner.ID, 0)
	if err != nil {
		t.Fatalf("GetContacts: %v", err)
	}
	if notModified || len(list.Contacts) != 1 {
		t.Fatalf("contacts notModified=%v len=%d, want one full contact", notModified, len(list.Contacts))
	}
	contact := list.Contacts[0]
	if contact.User.PhotoID != 9401 || contact.User.PhotoDCID != 2 || string(contact.User.PhotoStripped) != string([]byte{11, 12}) {
		t.Fatalf("contact user photo = id %d dc %d stripped %v, want 9401/2/[11 12]", contact.User.PhotoID, contact.User.PhotoDCID, contact.User.PhotoStripped)
	}
	if contact.User.FirstName != "Alice" || contact.User.LastName != "Saved" || contact.User.Phone != "1111" {
		t.Fatalf("contact user projection = %+v, want contact name/phone", contact.User)
	}
}

func TestEditCloseFriendsReplacesOwnerContactFlags(t *testing.T) {
	ctx := context.Background()
	users := memory.NewUserStore()
	contactsStore := memory.NewContactStore()
	owner, err := users.Create(ctx, domain.User{Phone: "100", FirstName: "Owner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	bob, err := users.Create(ctx, domain.User{Phone: "101", FirstName: "Bob"})
	if err != nil {
		t.Fatalf("create bob: %v", err)
	}
	carol, err := users.Create(ctx, domain.User{Phone: "102", FirstName: "Carol"})
	if err != nil {
		t.Fatalf("create carol: %v", err)
	}
	bot, err := users.Create(ctx, domain.User{Phone: "103", FirstName: "Bot", Bot: true, BotInfoVersion: 1})
	if err != nil {
		t.Fatalf("create bot: %v", err)
	}
	for _, user := range []domain.User{bob, carol, bot} {
		if _, err := contactsStore.Upsert(ctx, owner.ID, domain.ContactInput{ContactUserID: user.ID, FirstName: user.FirstName}); err != nil {
			t.Fatalf("upsert contact %d: %v", user.ID, err)
		}
	}
	svc := NewService(contactsStore, users)

	result, err := svc.EditCloseFriends(ctx, owner.ID, []int64{bob.ID, bob.ID, 0, owner.ID, bot.ID, 999999})
	if err != nil {
		t.Fatalf("EditCloseFriends first: %v", err)
	}
	if got, want := result.AddedUserIDs, []int64{bob.ID}; !reflect.DeepEqual(got, want) {
		t.Fatalf("first added = %v, want %v", got, want)
	}
	if len(result.RemovedUserIDs) != 0 {
		t.Fatalf("first removed = %v, want empty", result.RemovedUserIDs)
	}
	list, notModified, err := svc.GetContacts(ctx, owner.ID, 0)
	if err != nil || notModified {
		t.Fatalf("GetContacts after first edit notModified=%v err=%v", notModified, err)
	}
	if !contactByID(t, list, bob.ID).CloseFriend || !contactByID(t, list, bob.ID).User.CloseFriend {
		t.Fatalf("bob close friend projection = %+v, want true", contactByID(t, list, bob.ID))
	}
	if contactByID(t, list, carol.ID).CloseFriend || contactByID(t, list, bot.ID).CloseFriend {
		t.Fatalf("carol/bot close friend flags = %+v / %+v, want false", contactByID(t, list, carol.ID), contactByID(t, list, bot.ID))
	}

	result, err = svc.EditCloseFriends(ctx, owner.ID, []int64{carol.ID})
	if err != nil {
		t.Fatalf("EditCloseFriends replace: %v", err)
	}
	if got, want := result.AddedUserIDs, []int64{carol.ID}; !reflect.DeepEqual(got, want) {
		t.Fatalf("replace added = %v, want %v", got, want)
	}
	if got, want := result.RemovedUserIDs, []int64{bob.ID}; !reflect.DeepEqual(got, want) {
		t.Fatalf("replace removed = %v, want %v", got, want)
	}
	replaced, _, err := svc.GetContacts(ctx, owner.ID, 0)
	if err != nil {
		t.Fatalf("GetContacts after replace: %v", err)
	}
	if contactByID(t, replaced, bob.ID).CloseFriend || !contactByID(t, replaced, carol.ID).CloseFriend {
		t.Fatalf("replace flags bob=%+v carol=%+v, want bob false carol true", contactByID(t, replaced, bob.ID), contactByID(t, replaced, carol.ID))
	}
}

func TestAcceptContactSharesPhoneAndClearsShareContact(t *testing.T) {
	ctx := context.Background()
	users := memory.NewUserStore()
	contactsStore := memory.NewContactStore()
	alice, err := users.Create(ctx, domain.User{Phone: "15550000001", FirstName: "Alice", LastName: "A"})
	if err != nil {
		t.Fatalf("create alice: %v", err)
	}
	bob, err := users.Create(ctx, domain.User{Phone: "15550000002", FirstName: "Bob", LastName: "B"})
	if err != nil {
		t.Fatalf("create bob: %v", err)
	}
	svc := NewService(contactsStore, users)

	if _, err := svc.AddContact(ctx, alice.ID, domain.ContactInput{
		ContactUserID: bob.ID,
		Phone:         bob.Phone,
		FirstName:     "Bobby",
		LastName:      "Remark",
	}); err != nil {
		t.Fatalf("alice add bob: %v", err)
	}
	settings, err := svc.GetPeerSettings(ctx, alice.ID, domain.Peer{Type: domain.PeerTypeUser, ID: bob.ID})
	if err != nil {
		t.Fatalf("alice peer settings before accept: %v", err)
	}
	if !settings.ShareContact {
		t.Fatalf("alice settings before accept = %+v, want share contact", settings)
	}

	contact, err := svc.AcceptContact(ctx, alice.ID, bob.ID)
	if err != nil {
		t.Fatalf("AcceptContact: %v", err)
	}
	if !contact.Mutual || !contact.User.Mutual {
		t.Fatalf("accepted contact = %+v, want mutual", contact)
	}
	aliceSettings, err := svc.GetPeerSettings(ctx, alice.ID, domain.Peer{Type: domain.PeerTypeUser, ID: bob.ID})
	if err != nil {
		t.Fatalf("alice peer settings after accept: %v", err)
	}
	if aliceSettings.ShareContact || aliceSettings.AddContact {
		t.Fatalf("alice settings after accept = %+v, want no share/add", aliceSettings)
	}
	bobSettings, err := svc.GetPeerSettings(ctx, bob.ID, domain.Peer{Type: domain.PeerTypeUser, ID: alice.ID})
	if err != nil {
		t.Fatalf("bob peer settings after accept: %v", err)
	}
	if bobSettings.ShareContact || bobSettings.AddContact {
		t.Fatalf("bob settings after accept = %+v, want no share/add", bobSettings)
	}
	reverse, found, err := contactsStore.Get(ctx, bob.ID, alice.ID)
	if err != nil || !found {
		t.Fatalf("bob contact alice found=%v err=%v", found, err)
	}
	if reverse.Phone != alice.Phone || reverse.FirstName != alice.FirstName || reverse.LastName != alice.LastName || !reverse.Mutual {
		t.Fatalf("bob contact alice = %+v, want alice phone/name and mutual", reverse)
	}

	repeated, err := svc.AcceptContact(ctx, alice.ID, bob.ID)
	if err != nil {
		t.Fatalf("AcceptContact repeat: %v", err)
	}
	if !repeated.Mutual {
		t.Fatalf("repeated accept = %+v, want mutual", repeated)
	}
}

func TestAddContactNormalizesPhoneToDigits(t *testing.T) {
	ctx := context.Background()
	users := memory.NewUserStore()
	contactsStore := memory.NewContactStore()
	alice, err := users.Create(ctx, domain.User{Phone: "15550060301", FirstName: "Alice", LastName: "A"})
	if err != nil {
		t.Fatalf("create alice: %v", err)
	}
	bob, err := users.Create(ctx, domain.User{Phone: "15550060302", FirstName: "Bob", LastName: "B"})
	if err != nil {
		t.Fatalf("create bob: %v", err)
	}
	svc := NewService(contactsStore, users)

	contact, err := svc.AddContact(ctx, alice.ID, domain.ContactInput{
		ContactUserID: bob.ID,
		Phone:         "+1 555-006-0302",
		FirstName:     "Bob B",
	})
	if err != nil {
		t.Fatalf("AddContact: %v", err)
	}
	if contact.Phone != "15550060302" {
		t.Fatalf("contact phone = %q, want digits-only 15550060302", contact.Phone)
	}
	if contact.User.Phone != "15550060302" {
		t.Fatalf("projected user phone = %q, want digits-only 15550060302", contact.User.Phone)
	}
	stored, found, err := contactsStore.Get(ctx, alice.ID, bob.ID)
	if err != nil || !found {
		t.Fatalf("stored contact found=%v err=%v", found, err)
	}
	if stored.Phone != "15550060302" {
		t.Fatalf("stored contact phone = %q, want digits-only 15550060302", stored.Phone)
	}

	emptied, err := svc.AddContact(ctx, alice.ID, domain.ContactInput{
		ContactUserID: bob.ID,
		Phone:         "+",
		FirstName:     "Bob B",
	})
	if err != nil {
		t.Fatalf("AddContact digitless phone: %v", err)
	}
	if emptied.Phone != "" || emptied.User.Phone != "" {
		t.Fatalf("digitless phone contact = local %q user %q, want empty without account-phone fallback", emptied.Phone, emptied.User.Phone)
	}
}

func TestAddContactPhonePrivacyExceptionPeerSettings(t *testing.T) {
	ctx := context.Background()
	users := memory.NewUserStore()
	contactsStore := memory.NewContactStore()
	privacySvc := privacyapp.NewService(memory.NewPrivacyStore(), contactsStore)
	alice, err := users.Create(ctx, domain.User{Phone: "15550000101", FirstName: "Alice", LastName: "A"})
	if err != nil {
		t.Fatalf("create alice: %v", err)
	}
	bob, err := users.Create(ctx, domain.User{Phone: "15550000102", FirstName: "Bob", LastName: "B"})
	if err != nil {
		t.Fatalf("create bob: %v", err)
	}
	carol, err := users.Create(ctx, domain.User{Phone: "15550000103", FirstName: "Carol", LastName: "C"})
	if err != nil {
		t.Fatalf("create carol: %v", err)
	}
	dave, err := users.Create(ctx, domain.User{Phone: "15550000104", FirstName: "Dave", LastName: "D"})
	if err != nil {
		t.Fatalf("create dave: %v", err)
	}
	svc := NewService(contactsStore, users).Configure(WithPrivacyEvaluator(privacySvc))

	bobSettings, err := svc.GetPeerSettings(ctx, alice.ID, domain.Peer{Type: domain.PeerTypeUser, ID: bob.ID})
	if err != nil {
		t.Fatalf("bob peer settings before add: %v", err)
	}
	if !bobSettings.AddContact || bobSettings.ShareContact || !bobSettings.NeedContactsException {
		t.Fatalf("bob settings before add = %+v, want add + need exception only", bobSettings)
	}
	if _, err := svc.AddContact(ctx, alice.ID, domain.ContactInput{
		ContactUserID: bob.ID,
		Phone:         bob.Phone,
		FirstName:     "Bobby",
	}); err != nil {
		t.Fatalf("alice add bob: %v", err)
	}
	bobSettings, err = svc.GetPeerSettings(ctx, alice.ID, domain.Peer{Type: domain.PeerTypeUser, ID: bob.ID})
	if err != nil {
		t.Fatalf("bob peer settings after add without exception: %v", err)
	}
	if bobSettings.AddContact || !bobSettings.ShareContact || !bobSettings.NeedContactsException {
		t.Fatalf("bob settings after add without exception = %+v, want share + need exception", bobSettings)
	}
	if allowed, err := privacySvc.CanSee(ctx, alice.ID, bob.ID, domain.PrivacyKeyPhoneNumber); err != nil {
		t.Fatalf("bob can see alice phone: %v", err)
	} else if allowed {
		t.Fatalf("bob can see alice phone = true, want false before exception")
	}

	if _, err := svc.AddContact(ctx, alice.ID, domain.ContactInput{
		ContactUserID:            carol.ID,
		Phone:                    carol.Phone,
		FirstName:                "Carol",
		AddPhonePrivacyException: true,
	}); err != nil {
		t.Fatalf("alice add carol with exception: %v", err)
	}
	carolSettings, err := svc.GetPeerSettings(ctx, alice.ID, domain.Peer{Type: domain.PeerTypeUser, ID: carol.ID})
	if err != nil {
		t.Fatalf("carol peer settings after exception: %v", err)
	}
	if carolSettings.AddContact || carolSettings.ShareContact || carolSettings.NeedContactsException {
		t.Fatalf("carol settings after exception = %+v, want no add/share/need exception", carolSettings)
	}
	if allowed, err := privacySvc.CanSee(ctx, alice.ID, carol.ID, domain.PrivacyKeyPhoneNumber); err != nil {
		t.Fatalf("carol can see alice phone: %v", err)
	} else if !allowed {
		t.Fatalf("carol can see alice phone = false, want true after exception")
	}

	if _, err := contactsStore.Upsert(ctx, dave.ID, domain.ContactInput{
		ContactUserID: alice.ID,
		Phone:         alice.Phone,
		FirstName:     alice.FirstName,
		LastName:      alice.LastName,
	}); err != nil {
		t.Fatalf("dave add alice: %v", err)
	}
	daveSettings, err := svc.GetPeerSettings(ctx, alice.ID, domain.Peer{Type: domain.PeerTypeUser, ID: dave.ID})
	if err != nil {
		t.Fatalf("dave peer settings before alice add: %v", err)
	}
	if !daveSettings.AddContact || daveSettings.ShareContact || daveSettings.NeedContactsException {
		t.Fatalf("dave settings with reverse contact = %+v, want add only", daveSettings)
	}
}

func TestAcceptContactRequiresExistingContactRequest(t *testing.T) {
	ctx := context.Background()
	users := memory.NewUserStore()
	contactsStore := memory.NewContactStore()
	alice, err := users.Create(ctx, domain.User{Phone: "15550000001", FirstName: "Alice"})
	if err != nil {
		t.Fatalf("create alice: %v", err)
	}
	bob, err := users.Create(ctx, domain.User{Phone: "15550000002", FirstName: "Bob"})
	if err != nil {
		t.Fatalf("create bob: %v", err)
	}
	svc := NewService(contactsStore, users)

	if _, err := svc.AcceptContact(ctx, alice.ID, bob.ID); !errors.Is(err, ErrContactReqMissing) {
		t.Fatalf("AcceptContact without contact err = %v, want ErrContactReqMissing", err)
	}
}

func contactByID(t *testing.T, list domain.ContactList, id int64) domain.Contact {
	t.Helper()
	for _, contact := range list.Contacts {
		if contact.User.ID == id {
			return contact
		}
	}
	t.Fatalf("contact %d not found in %+v", id, list.Contacts)
	return domain.Contact{}
}

type contactProfilePhotos map[int64]domain.ProfilePhotoRef

func (p contactProfilePhotos) CurrentProfilePhotos(_ context.Context, _ domain.PeerType, ids []int64) (map[int64]domain.ProfilePhotoRef, error) {
	out := make(map[int64]domain.ProfilePhotoRef, len(ids))
	for _, id := range ids {
		if ref, ok := p[id]; ok {
			out[id] = ref
		}
	}
	return out, nil
}
