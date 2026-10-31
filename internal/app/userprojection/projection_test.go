package userprojection

import (
	"context"
	"reflect"
	"testing"

	privacyapp "telesrv/internal/app/privacy"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

type fakeCollectiblePhones map[int64]domain.CollectiblePhone

func (f fakeCollectiblePhones) OwnedCollectiblePhones(_ context.Context, ids []int64) (map[int64]domain.CollectiblePhone, error) {
	out := make(map[int64]domain.CollectiblePhone)
	for _, id := range ids {
		if phone, ok := f[id]; ok {
			out[id] = phone
		}
	}
	return out, nil
}

func TestCloneUsersDoesNotSharePhotoStripped(t *testing.T) {
	source := []domain.User{{ID: 1, PhotoStripped: []byte{1, 2, 3}}}
	cloned := cloneUsers(source)
	cloned[0].PhotoStripped[0] = 9
	if source[0].PhotoStripped[0] != 1 {
		t.Fatalf("cloneUsers shared PhotoStripped backing storage: source=%v clone=%v", source[0].PhotoStripped, cloned[0].PhotoStripped)
	}
}

func TestProjectorPhoneVisibleToStrangerUnderAllowAll(t *testing.T) {
	ctx := context.Background()
	const (
		viewerID = int64(8201)
		ownerID  = int64(8202)
	)
	contacts := memory.NewContactStore()
	rules := memory.NewPrivacyStore()
	privacy := privacyapp.NewService(rules, contacts)
	if _, err := privacy.SetRules(ctx, ownerID, domain.PrivacyKeyPhoneNumber, []domain.PrivacyRule{{Kind: domain.PrivacyRuleAllowAll}}); err != nil {
		t.Fatalf("set privacy: %v", err)
	}
	projector := New(WithContactStore(contacts), WithPrivacyEvaluator(privacy))
	base := []domain.User{{ID: ownerID, Phone: "15550008202", FirstName: "Owner"}}

	single, err := projector.ForViewer(ctx, viewerID, base)
	if err != nil {
		t.Fatalf("ForViewer: %v", err)
	}
	if owner := projectionUser(t, single, ownerID); owner.Phone != "15550008202" {
		t.Fatalf("allow-all stranger phone = %q, want visible", owner.Phone)
	}

	fanout, err := projector.ForViewers(ctx, []int64{viewerID}, base)
	if err != nil {
		t.Fatalf("ForViewers: %v", err)
	}
	if owner := projectionUser(t, fanout[viewerID], ownerID); owner.Phone != "15550008202" {
		t.Fatalf("allow-all fanout stranger phone = %q, want visible", owner.Phone)
	}
}

func TestProjectorCollectiblePhonePrivacyAndExclusiveOverride(t *testing.T) {
	ctx := context.Background()
	const viewerID int64 = 8101
	const standardID int64 = 8102
	const exclusiveID int64 = 8103
	contacts := memory.NewContactStore()
	rules := memory.NewPrivacyStore()
	privacy := privacyapp.NewService(rules, contacts)
	for _, ownerID := range []int64{standardID, exclusiveID} {
		if _, err := privacy.SetRules(ctx, ownerID, domain.PrivacyKeyPhoneNumber, []domain.PrivacyRule{{Kind: domain.PrivacyRuleDisallowAll}}); err != nil {
			t.Fatal(err)
		}
	}
	phones := fakeCollectiblePhones{
		standardID:  {Phone: "8881111", Tier: domain.CollectiblePhoneTierStandard, Status: domain.CollectibleUsernameStatusOwned, OwnerUserID: standardID},
		exclusiveID: {Phone: "8882222", Tier: domain.CollectiblePhoneTierExclusive, Status: domain.CollectibleUsernameStatusOwned, OwnerUserID: exclusiveID},
	}
	p := New(WithContactStore(contacts), WithPrivacyEvaluator(privacy), WithCollectiblePhoneProvider(phones))
	base := []domain.User{{ID: standardID, Phone: "155501"}, {ID: exclusiveID, Phone: "155502"}}
	projected, err := p.ForViewer(ctx, viewerID, base)
	if err != nil {
		t.Fatal(err)
	}
	if got := projectionUser(t, projected, standardID).Phone; got != "" {
		t.Fatalf("standard stranger phone=%q, want hidden", got)
	}
	if got := projectionUser(t, projected, exclusiveID).Phone; got != "8882222" {
		t.Fatalf("exclusive stranger phone=%q", got)
	}
	self, err := p.ForViewer(ctx, standardID, base[:1])
	if err != nil {
		t.Fatal(err)
	}
	if got := self[0].Phone; got != "8881111" {
		t.Fatalf("standard self phone=%q", got)
	}
	fanout, err := p.ForViewers(ctx, []int64{viewerID}, base)
	if err != nil {
		t.Fatal(err)
	}
	if got := projectionUser(t, fanout[viewerID], exclusiveID).Phone; got != "8882222" {
		t.Fatalf("fanout exclusive phone=%q", got)
	}
}

func TestProjectorCombinesProfilePhotosAndViewerContacts(t *testing.T) {
	ctx := context.Background()
	const viewerID int64 = 1001
	const friendID int64 = 1002
	const strangerID int64 = 1003
	contacts := memory.NewContactStore()
	if _, err := contacts.Upsert(ctx, viewerID, domain.ContactInput{
		ContactUserID: friendID,
		Phone:         "1111",
		FirstName:     "Alice",
		LastName:      "Contact",
		Note:          "private note",
		NoteEntities:  []domain.MessageEntity{{Type: domain.MessageEntityBold, Offset: 0, Length: 7}},
	}); err != nil {
		t.Fatalf("upsert contact: %v", err)
	}
	projector := New(
		WithContactStore(contacts),
		WithPhotoProvider(fakeProfilePhotos{
			profile: map[int64]domain.ProfilePhotoRef{
				friendID:   {PhotoID: 9001, DCID: 2, Stripped: []byte{1, 2}},
				strangerID: {PhotoID: 9002, DCID: 3, Stripped: []byte{3, 4}},
			},
		}),
	)

	users, err := projector.ForViewer(ctx, viewerID, []domain.User{
		{ID: viewerID, Phone: "15550000001", FirstName: "Owner"},
		{ID: friendID, AccessHash: 22, Phone: "15550000002", FirstName: "Public", LastName: "Name"},
		{ID: strangerID, AccessHash: 33, Phone: "15550000003", FirstName: "Stranger"},
	})
	if err != nil {
		t.Fatalf("ForViewer: %v", err)
	}

	friend := projectionUser(t, users, friendID)
	if friend.FirstName != "Alice" || friend.LastName != "Contact" || friend.Phone != "1111" || !friend.Contact {
		t.Fatalf("friend projection = %+v, want contact name/phone", friend)
	}
	if friend.ContactNote != "private note" || len(friend.ContactNoteEntities) != 1 || friend.ContactNoteEntities[0].Type != domain.MessageEntityBold {
		t.Fatalf("friend contact note = %q %+v, want owner-scoped note", friend.ContactNote, friend.ContactNoteEntities)
	}
	if friend.PhotoID != 9001 || friend.PhotoDCID != 2 || string(friend.PhotoStripped) != string([]byte{1, 2}) {
		t.Fatalf("friend photo = id %d dc %d stripped %v, want 9001/2/[1 2]", friend.PhotoID, friend.PhotoDCID, friend.PhotoStripped)
	}
	stranger := projectionUser(t, users, strangerID)
	if stranger.Phone != "" || stranger.Contact || stranger.ContactNote != "" || len(stranger.ContactNoteEntities) != 0 {
		t.Fatalf("stranger projection = %+v, want hidden phone and no contact note", stranger)
	}
	if stranger.PhotoID != 9002 || stranger.PhotoDCID != 3 {
		t.Fatalf("stranger photo = id %d dc %d, want 9002/3", stranger.PhotoID, stranger.PhotoDCID)
	}
}

func TestProjectorPersonalPhotoWinsOverProfile(t *testing.T) {
	ctx := context.Background()
	const viewerID int64 = 2001
	const friendID int64 = 2002
	contacts := memory.NewContactStore()
	if _, err := contacts.Upsert(ctx, viewerID, domain.ContactInput{ContactUserID: friendID, FirstName: "Friend"}); err != nil {
		t.Fatalf("upsert contact: %v", err)
	}
	if _, _, err := contacts.SetPersonalPhoto(ctx, viewerID, friendID, 9100, 100); err != nil {
		t.Fatalf("set personal photo: %v", err)
	}
	projector := New(
		WithContactStore(contacts),
		WithPhotoProvider(fakeProfilePhotos{
			profile: map[int64]domain.ProfilePhotoRef{friendID: {PhotoID: 9001, DCID: 2}},
		}),
	)
	users, err := projector.ForViewer(ctx, viewerID, []domain.User{{ID: friendID, FirstName: "Public"}})
	if err != nil {
		t.Fatalf("ForViewer: %v", err)
	}
	friend := projectionUser(t, users, friendID)
	if friend.PhotoID != 9100 || !friend.PhotoPersonal {
		t.Fatalf("friend photo = id %d personal %v, want personal 9100", friend.PhotoID, friend.PhotoPersonal)
	}
}

func TestProjectorUsesFallbackWhenProfilePhotoHidden(t *testing.T) {
	ctx := context.Background()
	const viewerID int64 = 3001
	const ownerID int64 = 3002
	contacts := memory.NewContactStore()
	rules := memory.NewPrivacyStore()
	privacy := privacyapp.NewService(rules, contacts)
	if _, err := privacy.SetRules(ctx, ownerID, domain.PrivacyKeyProfilePhoto, []domain.PrivacyRule{{Kind: domain.PrivacyRuleDisallowAll}}); err != nil {
		t.Fatalf("set privacy: %v", err)
	}
	projector := New(
		WithContactStore(contacts),
		WithPrivacyEvaluator(privacy),
		WithPhotoProvider(fakeProfilePhotos{
			profile:  map[int64]domain.ProfilePhotoRef{ownerID: {PhotoID: 9001, DCID: 2}},
			fallback: map[int64]domain.ProfilePhotoRef{ownerID: {PhotoID: 9002, DCID: 3}},
		}),
	)
	users, err := projector.ForViewer(ctx, viewerID, []domain.User{{ID: ownerID, Phone: "15550003002", FirstName: "Owner"}})
	if err != nil {
		t.Fatalf("ForViewer: %v", err)
	}
	owner := projectionUser(t, users, ownerID)
	if owner.PhotoID != 9002 || owner.PhotoDCID != 3 || owner.Phone != "" {
		t.Fatalf("owner projection = %+v, want fallback photo and hidden phone", owner)
	}
}

func TestProjectorContactWithoutKnownPhoneCannotBypassPhonePrivacy(t *testing.T) {
	ctx := context.Background()
	const (
		viewerID = int64(3101)
		ownerID  = int64(3102)
	)
	contacts := memory.NewContactStore()
	if _, err := contacts.Upsert(ctx, viewerID, domain.ContactInput{
		ContactUserID: ownerID,
		FirstName:     "Saved",
		Phone:         "",
	}); err != nil {
		t.Fatalf("upsert contact: %v", err)
	}
	privacy := privacyapp.NewService(memory.NewPrivacyStore(), contacts)
	projector := New(
		WithContactStore(contacts),
		WithPrivacyEvaluator(privacy),
	)
	users, err := projector.ForViewer(ctx, viewerID, []domain.User{{
		ID:        ownerID,
		Phone:     "15550003102",
		FirstName: "Owner",
	}})
	if err != nil {
		t.Fatalf("ForViewer: %v", err)
	}
	owner := projectionUser(t, users, ownerID)
	if !owner.Contact || owner.Phone != "" {
		t.Fatalf("owner projection = %+v, want contact=true with hidden phone", owner)
	}
	batch, err := projector.ForViewers(ctx, []int64{viewerID}, []domain.User{{
		ID:        ownerID,
		Phone:     "15550003102",
		FirstName: "Owner",
	}})
	if err != nil {
		t.Fatalf("ForViewers: %v", err)
	}
	batchOwner := projectionUser(t, batch[viewerID], ownerID)
	if !batchOwner.Contact || batchOwner.Phone != "" {
		t.Fatalf("batch owner projection = %+v, want contact=true with hidden phone", batchOwner)
	}
}

func TestProjectorAccountFreezeIsViewerScopedAndReversible(t *testing.T) {
	ctx := context.Background()
	const (
		frozenUserID = int64(4001)
		otherViewer  = int64(4002)
	)
	freezes := &fakeAccountFreezes{items: map[int64]domain.AccountFreeze{
		frozenUserID: {UserID: frozenUserID, Frozen: true, Version: 3},
	}}
	projector := New(WithAccountFreezeProvider(freezes))
	base := []domain.User{{
		ID:         frozenUserID,
		FirstName:  "Frozen",
		Status:     domain.UserStatus{Kind: domain.UserStatusOnline, Expires: 1700000100},
		LastSeenAt: 1700000000,
		// Viewer-scoped fields must never be trusted from a reused base object.
		RestrictionReasons: []domain.UserRestrictionReason{{Platform: "all", Reason: "stale", Text: "stale"}},
	}}

	otherView, err := projector.ForViewer(ctx, otherViewer, base)
	if err != nil {
		t.Fatalf("ForViewer(other): %v", err)
	}
	got := projectionUser(t, otherView, frozenUserID)
	if !got.Deleted {
		t.Fatalf("other-view Deleted = false, want true (frozen account shown as deleted to peers)")
	}
	if !got.FrozenForViewer {
		t.Fatalf("other-view FrozenForViewer = false, want true (mark flows to RPC boundary)")
	}
	if got.FirstName != "" || got.Username != "" || len(got.RestrictionReasons) != 0 {
		t.Fatalf("other-view kept persona fields: %+v, want tombstone", got)
	}
	if base[0].Deleted {
		t.Fatalf("projection mutated base user: %+v", base[0])
	}

	selfView, err := projector.ForViewer(ctx, frozenUserID, base)
	if err != nil {
		t.Fatalf("ForViewer(self): %v", err)
	}
	if self := projectionUser(t, selfView, frozenUserID); self.Deleted || self.FrozenForViewer || self.FirstName != "Frozen" || len(self.RestrictionReasons) != 0 || self.Status.Kind != domain.UserStatusEmpty || self.LastSeenAt != 0 {
		t.Fatalf("self-view = %+v, want live account with empty status and cleared last_seen", self)
	}

	batch, err := projector.ForViewers(ctx, []int64{otherViewer, frozenUserID}, base)
	if err != nil {
		t.Fatalf("ForViewers: %v", err)
	}
	if peer := projectionUser(t, batch[otherViewer], frozenUserID); !peer.Deleted || !peer.FrozenForViewer || len(peer.RestrictionReasons) != 0 {
		t.Fatalf("batch other-view = %+v, want deleted tombstone with frozen mark", peer)
	}
	if self := projectionUser(t, batch[frozenUserID], frozenUserID); self.Deleted || self.FrozenForViewer || self.FirstName != "Frozen" || len(self.RestrictionReasons) != 0 || self.Status.Kind != domain.UserStatusEmpty || self.LastSeenAt != 0 {
		t.Fatalf("batch self-view = %+v, want live account with empty status", self)
	}

	freezes.items = nil
	unfrozenView, err := projector.ForViewer(ctx, otherViewer, base)
	if err != nil {
		t.Fatalf("ForViewer(after unfreeze): %v", err)
	}
	if unfrozen := projectionUser(t, unfrozenView, frozenUserID); unfrozen.Deleted || unfrozen.FrozenForViewer || unfrozen.FirstName != "Frozen" || len(unfrozen.RestrictionReasons) != 0 || unfrozen.Status.Kind != domain.UserStatusOnline || unfrozen.LastSeenAt != 1700000000 {
		t.Fatalf("unfrozen projection = %+v, want live account with restored status", unfrozen)
	}
}

// TestForViewersEquivalentToForViewer pins the core safety net of fan-out templating: every
// viewer slice of ForViewers(viewers, users) must be byte-equivalent to the per-viewer
// ForViewer(viewer, users) (privacy/rename/photo projection must not drift into leakage
// because of batch templating). Covers: default-rule strangers / contact rename+phone /
// personal photo / status hidden / profile photo hidden falling back / self / bot / system
// account / the viewer itself appearing as an owner.
func TestForViewersEquivalentToForViewer(t *testing.T) {
	ctx := context.Background()
	const (
		v1  = int64(5001)
		v2  = int64(5002)
		o1  = int64(5101) // stranger, default rules
		o2  = int64(5102) // v1's contact (rename+phone), and v1 set a personal photo for o2
		o3  = int64(5103) // status hidden
		o4  = int64(5104) // profile photo hidden → falls back
		bot = int64(5105)
	)
	viewers := []int64{v1, v2}

	contacts := memory.NewContactStore()
	rules := memory.NewPrivacyStore()
	privacy := privacyapp.NewService(rules, contacts)

	if _, err := contacts.Upsert(ctx, v1, domain.ContactInput{ContactUserID: o2, Phone: "1111", FirstName: "Alice", LastName: "Friend"}); err != nil {
		t.Fatalf("upsert contact: %v", err)
	}
	// v1 set a personal photo for o2: ForViewers must surface the viewer-specific photo just like ForViewer.
	if _, _, err := contacts.SetPersonalPhoto(ctx, v1, o2, 9300, 300); err != nil {
		t.Fatalf("set personal photo: %v", err)
	}
	if _, err := privacy.SetRules(ctx, o3, domain.PrivacyKeyStatusTimestamp, []domain.PrivacyRule{{Kind: domain.PrivacyRuleDisallowAll}}); err != nil {
		t.Fatalf("set o3 status: %v", err)
	}
	if _, err := privacy.SetRules(ctx, o4, domain.PrivacyKeyProfilePhoto, []domain.PrivacyRule{{Kind: domain.PrivacyRuleDisallowAll}}); err != nil {
		t.Fatalf("set o4 photo: %v", err)
	}

	projector := New(
		WithContactStore(contacts),
		WithPrivacyEvaluator(privacy),
		WithPhotoProvider(fakeProfilePhotos{
			profile: map[int64]domain.ProfilePhotoRef{
				o1: {PhotoID: 9001, DCID: 1, Stripped: []byte{1}},
				o2: {PhotoID: 9002, DCID: 2, Stripped: []byte{2}},
				o3: {PhotoID: 9003, DCID: 3, Stripped: []byte{3}},
				o4: {PhotoID: 9004, DCID: 4, Stripped: []byte{4}},
				v1: {PhotoID: 9005, DCID: 5, Stripped: []byte{5}},
				v2: {PhotoID: 9006, DCID: 6, Stripped: []byte{6}},
			},
			fallback: map[int64]domain.ProfilePhotoRef{
				o4: {PhotoID: 9404, DCID: 4, Stripped: []byte{44}},
			},
		}),
	)

	users := []domain.User{
		{ID: o1, AccessHash: 11, Phone: "15550000001", FirstName: "Stranger", Status: domain.UserStatus{Kind: domain.UserStatusOnline}},
		{ID: o2, AccessHash: 12, Phone: "15550000002", FirstName: "PublicO2", Status: domain.UserStatus{Kind: domain.UserStatusOnline}},
		{ID: o3, AccessHash: 13, Phone: "15550000003", FirstName: "O3", Status: domain.UserStatus{Kind: domain.UserStatusOnline}, LastSeenAt: 123},
		{ID: o4, AccessHash: 14, Phone: "15550000004", FirstName: "O4"},
		{ID: bot, AccessHash: 15, FirstName: "Bot", Bot: true},
		{ID: domain.OfficialSystemUserID, FirstName: "System"},
		{ID: v1, AccessHash: 16, Phone: "15550000016", FirstName: "Viewer1"}, // the viewer itself also appears as an owner
	}

	batch, err := projector.ForViewers(ctx, viewers, users)
	if err != nil {
		t.Fatalf("ForViewers: %v", err)
	}
	if got := projectionUser(t, batch[v1], o2); got.PhotoID != 9300 || !got.PhotoPersonal {
		t.Fatalf("fanout personal photo = id %d personal %v, want personal 9300", got.PhotoID, got.PhotoPersonal)
	}
	for _, viewer := range viewers {
		want, err := projector.ForViewer(ctx, viewer, users)
		if err != nil {
			t.Fatalf("ForViewer(%d): %v", viewer, err)
		}
		got, ok := batch[viewer]
		if !ok {
			t.Fatalf("ForViewers missing viewer %d", viewer)
		}
		if len(got) != len(want) {
			t.Fatalf("viewer %d len(got)=%d len(want)=%d", viewer, len(got), len(want))
		}
		for i := range want {
			w, g := want[i], got[i]
			if w.ID != g.ID {
				t.Fatalf("viewer %d idx %d id mismatch got=%d want=%d", viewer, i, g.ID, w.ID)
			}
			if !reflect.DeepEqual(w, g) {
				t.Fatalf("viewer %d owner %d: ForViewers != ForViewer\n got=%+v\nwant=%+v", viewer, w.ID, g, w)
			}
		}
	}
}

func projectionUser(t *testing.T, users []domain.User, id int64) domain.User {
	t.Helper()
	for _, user := range users {
		if user.ID == id {
			return user
		}
	}
	t.Fatalf("user %d not found in %+v", id, users)
	return domain.User{}
}

type fakeProfilePhotos struct {
	profile  map[int64]domain.ProfilePhotoRef
	fallback map[int64]domain.ProfilePhotoRef
}

type fakeAccountFreezes struct {
	items map[int64]domain.AccountFreeze
}

func (f *fakeAccountFreezes) AccountFreezes(_ context.Context, ids []int64) (map[int64]domain.AccountFreeze, error) {
	out := make(map[int64]domain.AccountFreeze)
	for _, id := range ids {
		if freeze, ok := f.items[id]; ok {
			out[id] = freeze
		}
	}
	return out, nil
}

func (p fakeProfilePhotos) CurrentProfilePhotos(_ context.Context, _ domain.PeerType, ids []int64) (map[int64]domain.ProfilePhotoRef, error) {
	return p.CurrentProfilePhotosKind(context.Background(), domain.PeerTypeUser, ids, domain.ProfilePhotoKindProfile)
}

func (p fakeProfilePhotos) CurrentProfilePhotosKind(_ context.Context, _ domain.PeerType, ids []int64, kind domain.ProfilePhotoKind) (map[int64]domain.ProfilePhotoRef, error) {
	source := p.profile
	if kind == domain.ProfilePhotoKindFallback {
		source = p.fallback
	}
	out := make(map[int64]domain.ProfilePhotoRef, len(ids))
	for _, id := range ids {
		if ref, ok := source[id]; ok {
			out[id] = ref
		}
	}
	return out, nil
}
