package users

import (
	"context"
	"errors"
	"strings"
	"testing"

	privacyapp "telesrv/internal/app/privacy"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

func TestServiceUsernameLifecycle(t *testing.T) {
	ctx := context.Background()
	store := memory.NewUserStore()
	owner, err := store.Create(ctx, domain.User{AccessHash: 1, Phone: "15550000001", FirstName: "Owner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	other, err := store.Create(ctx, domain.User{AccessHash: 2, Phone: "15550000002", FirstName: "Other", Username: "taken_name"})
	if err != nil {
		t.Fatalf("create other: %v", err)
	}
	svc := NewService(store)

	if ok, err := svc.CheckUsername(ctx, owner.ID, "123bad"); err == nil || ok || !errors.Is(err, domain.ErrUsernameInvalid) {
		t.Fatalf("CheckUsername invalid = ok %v err %v, want username invalid", ok, err)
	}
	if ok, err := svc.CheckUsername(ctx, owner.ID, "taken_name"); err != nil || ok {
		t.Fatalf("CheckUsername occupied = ok %v err %v, want false/nil", ok, err)
	}
	if ok, err := svc.CheckUsername(ctx, owner.ID, "owner_name"); err != nil || !ok {
		t.Fatalf("CheckUsername available = ok %v err %v, want true/nil", ok, err)
	}

	updated, err := svc.UpdateUsername(ctx, owner.ID, "@Owner_Name")
	if err != nil {
		t.Fatalf("UpdateUsername: %v", err)
	}
	if updated.Username != "Owner_Name" {
		t.Fatalf("updated username = %q, want Owner_Name", updated.Username)
	}
	resolved, found, err := svc.ResolveUsername(ctx, other.ID, "owner_name")
	if err != nil || !found || resolved.ID != owner.ID {
		t.Fatalf("ResolveUsername = user %+v found %v err %v, want owner", resolved, found, err)
	}
	if _, err := svc.UpdateUsername(ctx, owner.ID, "TAKEN_NAME"); !errors.Is(err, domain.ErrUsernameOccupied) {
		t.Fatalf("UpdateUsername duplicate err = %v, want username occupied", err)
	}
	phoneUser, found, err := svc.ResolvePhone(ctx, owner.ID, "+1 (555) 000-0002")
	if err != nil || !found || phoneUser.ID != other.ID {
		t.Fatalf("ResolvePhone = user %+v found %v err %v, want other", phoneUser, found, err)
	}
	cleared, err := svc.UpdateUsername(ctx, owner.ID, "")
	if err != nil {
		t.Fatalf("clear username: %v", err)
	}
	if cleared.Username != "" {
		t.Fatalf("cleared username = %q, want empty", cleared.Username)
	}
}

func TestResolvePhoneHonorsAddedByPhone(t *testing.T) {
	ctx := context.Background()
	users := memory.NewUserStore()
	contacts := memory.NewContactStore()
	viewer, err := users.Create(ctx, domain.User{AccessHash: 1, Phone: "15550001001", FirstName: "Viewer"})
	if err != nil {
		t.Fatalf("create viewer: %v", err)
	}
	target, err := users.Create(ctx, domain.User{AccessHash: 2, Phone: "15550001002", FirstName: "Target"})
	if err != nil {
		t.Fatalf("create target: %v", err)
	}
	privacy := privacyapp.NewService(memory.NewPrivacyStore(), contacts)
	if _, err := privacy.SetRules(ctx, target.ID, domain.PrivacyKeyAddedByPhone, []domain.PrivacyRule{{Kind: domain.PrivacyRuleAllowContacts}}); err != nil {
		t.Fatalf("set AddedByPhone: %v", err)
	}
	svc := NewService(users,
		WithContactStore(contacts),
		WithPrivacyEvaluator(privacy),
	)

	if _, found, err := svc.ResolvePhone(ctx, viewer.ID, target.Phone); !errors.Is(err, domain.ErrPhoneNotOccupied) || found {
		t.Fatalf("ResolvePhone stranger found=%v err=%v, want phone not occupied", found, err)
	}
	if _, err := contacts.Upsert(ctx, target.ID, domain.ContactInput{
		ContactUserID: viewer.ID,
		FirstName:     viewer.FirstName,
	}); err != nil {
		t.Fatalf("target add viewer: %v", err)
	}
	got, found, err := svc.ResolvePhone(ctx, viewer.ID, target.Phone)
	if err != nil || !found || got.ID != target.ID {
		t.Fatalf("ResolvePhone contact = %+v found=%v err=%v, want target", got, found, err)
	}
}

func TestServiceUpdateProfile(t *testing.T) {
	ctx := context.Background()
	store := memory.NewUserStore()
	owner, err := store.Create(ctx, domain.User{AccessHash: 1, Phone: "15550000001", FirstName: "Owner", LastName: "Old"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	svc := NewService(store)

	updated, err := svc.UpdateProfile(ctx, owner.ID, domain.UserProfileUpdate{
		FirstName:    " New ",
		HasFirstName: true,
		LastName:     "Name",
		HasLastName:  true,
		About:        "bio",
		HasAbout:     true,
	})
	if err != nil {
		t.Fatalf("UpdateProfile: %v", err)
	}
	if updated.FirstName != "New" || updated.LastName != "Name" || updated.About != "bio" {
		t.Fatalf("updated profile = %+v, want trimmed names and about", updated)
	}
	if _, err := svc.UpdateProfile(ctx, owner.ID, domain.UserProfileUpdate{FirstName: " ", HasFirstName: true}); !errors.Is(err, domain.ErrFirstNameInvalid) {
		t.Fatalf("empty first name err = %v, want first name invalid", err)
	}
	if _, err := svc.UpdateProfile(ctx, owner.ID, domain.UserProfileUpdate{About: strings.Repeat("x", 71), HasAbout: true}); !errors.Is(err, domain.ErrAboutTooLong) {
		t.Fatalf("long about err = %v, want about too long", err)
	}
}

func TestServiceUpdateBirthday(t *testing.T) {
	ctx := context.Background()
	store := memory.NewUserStore()
	owner, err := store.Create(ctx, domain.User{AccessHash: 1, Phone: "15550000010", FirstName: "Owner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	svc := NewService(store)

	// 设置带年份的生日。
	u, err := svc.UpdateBirthday(ctx, owner.ID, domain.Birthday{Day: 14, Month: 2, Year: 1990})
	if err != nil {
		t.Fatalf("UpdateBirthday: %v", err)
	}
	if u.Birthday != (domain.Birthday{Day: 14, Month: 2, Year: 1990}) {
		t.Fatalf("birthday = %+v, want 14/2/1990", u.Birthday)
	}
	// 非法月份被拒。
	if _, err := svc.UpdateBirthday(ctx, owner.ID, domain.Birthday{Day: 1, Month: 13}); !errors.Is(err, domain.ErrBirthdayInvalid) {
		t.Fatalf("invalid month err = %v, want birthday invalid", err)
	}
	// 清除（零值）后 IsSet=false。
	u, err = svc.UpdateBirthday(ctx, owner.ID, domain.Birthday{})
	if err != nil {
		t.Fatalf("clear birthday: %v", err)
	}
	if u.Birthday.IsSet() {
		t.Fatalf("birthday after clear = %+v, want unset", u.Birthday)
	}
}

func TestServiceUpdatePersonalChannel(t *testing.T) {
	ctx := context.Background()
	store := memory.NewUserStore()
	owner, err := store.Create(ctx, domain.User{AccessHash: 1, Phone: "15550000011", FirstName: "Owner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	svc := NewService(store)

	u, err := svc.UpdatePersonalChannel(ctx, owner.ID, 4242)
	if err != nil {
		t.Fatalf("UpdatePersonalChannel: %v", err)
	}
	if u.PersonalChannelID != 4242 {
		t.Fatalf("personal channel = %d, want 4242", u.PersonalChannelID)
	}
	u, err = svc.UpdatePersonalChannel(ctx, owner.ID, 0)
	if err != nil {
		t.Fatalf("clear personal channel: %v", err)
	}
	if u.PersonalChannelID != 0 {
		t.Fatalf("personal channel after clear = %d, want 0", u.PersonalChannelID)
	}
}

func TestServiceByIDDoesNotReloadSelf(t *testing.T) {
	ctx := context.Background()
	base := memory.NewUserStore()
	owner, err := base.Create(ctx, domain.User{AccessHash: 1, Phone: "15550000001", FirstName: "Owner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	target, err := base.Create(ctx, domain.User{AccessHash: 2, Phone: "15550000002", FirstName: "Target"})
	if err != nil {
		t.Fatalf("create target: %v", err)
	}
	store := &countingUserStore{UserStore: base}
	svc := NewService(store)

	got, found, err := svc.ByID(ctx, owner.ID, target.ID)
	if err != nil || !found || got.ID != target.ID {
		t.Fatalf("ByID = %+v found %v err %v, want target", got, found, err)
	}
	if store.byIDCalls != 0 {
		t.Fatalf("store ByID calls = %d, want 0 because service uses batch lookup", store.byIDCalls)
	}
	if store.byIDsCalls != 1 {
		t.Fatalf("store ByIDs calls = %d, want 1 target lookup only", store.byIDsCalls)
	}
	if len(store.lastByIDs) != 1 || store.lastByIDs[0] != target.ID {
		t.Fatalf("last ByIDs ids = %v, want target %d only", store.lastByIDs, target.ID)
	}
}

func TestServiceUsesBaseCacheWithoutCachingViewerOverlay(t *testing.T) {
	ctx := context.Background()
	base := memory.NewUserStore()
	contacts := memory.NewContactStore()
	owner, err := base.Create(ctx, domain.User{AccessHash: 1, Phone: "15550000001", FirstName: "Owner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	target, err := base.Create(ctx, domain.User{AccessHash: 2, Phone: "15550000002", FirstName: "Target"})
	if err != nil {
		t.Fatalf("create target: %v", err)
	}
	store := &countingUserStore{UserStore: base}
	cache := newMemoryBaseUserCache()
	svc := NewService(store, WithBaseUserCache(cache), WithContactStore(contacts))

	first, found, err := svc.ByID(ctx, owner.ID, target.ID)
	if err != nil || !found {
		t.Fatalf("first ByID found=%v err=%v", found, err)
	}
	if first.Contact || first.Phone != "" || first.FirstName != "Target" {
		t.Fatalf("first projected user = %+v, want non-contact base projection", first)
	}
	if store.byIDsCalls != 1 {
		t.Fatalf("store ByIDs calls after first read = %d, want 1", store.byIDsCalls)
	}
	if _, err := contacts.Upsert(ctx, owner.ID, domain.ContactInput{
		ContactUserID: target.ID,
		Phone:         "15550000002",
		FirstName:     "Remark",
		LastName:      "Friend",
	}); err != nil {
		t.Fatalf("upsert contact: %v", err)
	}
	second, found, err := svc.ByID(ctx, owner.ID, target.ID)
	if err != nil || !found {
		t.Fatalf("second ByID found=%v err=%v", found, err)
	}
	if !second.Contact || second.FirstName != "Remark" || second.LastName != "Friend" || second.Phone != "15550000002" {
		t.Fatalf("second projected user = %+v, want fresh contact overlay from cached base", second)
	}
	if store.byIDsCalls != 1 {
		t.Fatalf("store ByIDs calls after cached read = %d, want still 1", store.byIDsCalls)
	}
}

func TestServiceAuthoritativeUserReloadReplacesStaleBaseCache(t *testing.T) {
	ctx := context.Background()
	base := memory.NewUserStore()
	viewer, err := base.Create(ctx, domain.User{AccessHash: 1, Phone: "15550000011", FirstName: "Viewer"})
	if err != nil {
		t.Fatalf("create viewer: %v", err)
	}
	target, err := base.Create(ctx, domain.User{AccessHash: 2, Phone: "15550000012", FirstName: "Target"})
	if err != nil {
		t.Fatalf("create target: %v", err)
	}
	store := &countingUserStore{UserStore: base}
	cache := newMemoryBaseUserCache()
	svc := NewService(store, WithBaseUserCache(cache))

	primed, err := svc.ByIDs(ctx, viewer.ID, []int64{target.ID})
	if err != nil || len(primed) != 1 || primed[0].Scam {
		t.Fatalf("prime ByIDs users=%+v err=%v", primed, err)
	}
	if _, err := base.SetScamFake(ctx, target.ID, true, false); err != nil {
		t.Fatalf("commit moderation flags behind cache: %v", err)
	}
	stale, err := svc.ByIDs(ctx, viewer.ID, []int64{target.ID})
	if err != nil || len(stale) != 1 || stale[0].Scam {
		t.Fatalf("ordinary cached ByIDs users=%+v err=%v, want stale scam=false", stale, err)
	}

	fresh, err := svc.ByIDsAuthoritative(ctx, viewer.ID, []int64{target.ID})
	if err != nil || len(fresh) != 1 || !fresh[0].Scam || fresh[0].Fake {
		t.Fatalf("authoritative ByIDs users=%+v err=%v, want scam=true fake=false", fresh, err)
	}
	if store.byIDsCalls != 2 {
		t.Fatalf("store ByIDs calls=%d, want prime + authoritative reload", store.byIDsCalls)
	}
	cached, err := svc.ByIDs(ctx, viewer.ID, []int64{target.ID})
	if err != nil || len(cached) != 1 || !cached[0].Scam || cached[0].Fake {
		t.Fatalf("replaced cache ByIDs users=%+v err=%v, want scam=true fake=false", cached, err)
	}
	if store.byIDsCalls != 2 {
		t.Fatalf("store ByIDs calls after cached read=%d, want unchanged", store.byIDsCalls)
	}
}

func TestServiceRefreshesBaseCacheAfterProfileUpdate(t *testing.T) {
	ctx := context.Background()
	base := memory.NewUserStore()
	owner, err := base.Create(ctx, domain.User{AccessHash: 1, Phone: "15550000001", FirstName: "Owner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	target, err := base.Create(ctx, domain.User{AccessHash: 2, Phone: "15550000002", FirstName: "Before"})
	if err != nil {
		t.Fatalf("create target: %v", err)
	}
	store := &countingUserStore{UserStore: base}
	cache := newMemoryBaseUserCache()
	svc := NewService(store, WithBaseUserCache(cache))

	if _, found, err := svc.ByID(ctx, owner.ID, target.ID); err != nil || !found {
		t.Fatalf("prime cache found=%v err=%v", found, err)
	}
	updated, err := svc.UpdateProfile(ctx, target.ID, domain.UserProfileUpdate{FirstName: "After", HasFirstName: true})
	if err != nil {
		t.Fatalf("UpdateProfile: %v", err)
	}
	if updated.FirstName != "After" {
		t.Fatalf("updated first name = %q, want After", updated.FirstName)
	}
	got, found, err := svc.ByID(ctx, owner.ID, target.ID)
	if err != nil || !found {
		t.Fatalf("ByID after update found=%v err=%v", found, err)
	}
	if got.FirstName != "After" {
		t.Fatalf("cached user first name = %q, want After", got.FirstName)
	}
}

func TestServiceSetVerifiedRefreshesBaseCache(t *testing.T) {
	ctx := context.Background()
	base := memory.NewUserStore()
	owner, err := base.Create(ctx, domain.User{AccessHash: 1, Phone: "15550000021", FirstName: "Owner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	target, err := base.Create(ctx, domain.User{AccessHash: 2, Phone: "15550000022", FirstName: "Target"})
	if err != nil {
		t.Fatalf("create target: %v", err)
	}
	store := &countingUserStore{UserStore: base}
	cache := newMemoryBaseUserCache()
	svc := NewService(store, WithBaseUserCache(cache))

	if _, found, err := svc.ByID(ctx, owner.ID, target.ID); err != nil || !found {
		t.Fatalf("prime cache found=%v err=%v", found, err)
	}
	updated, err := svc.SetVerified(ctx, target.ID, true)
	if err != nil {
		t.Fatalf("SetVerified: %v", err)
	}
	if !updated.Verified {
		t.Fatalf("updated verified = false, want true")
	}
	got, found, err := svc.ByID(ctx, owner.ID, target.ID)
	if err != nil || !found {
		t.Fatalf("ByID after verified found=%v err=%v", found, err)
	}
	if !got.Verified {
		t.Fatalf("cached verified = false, want true")
	}
	cleared, err := svc.SetVerified(ctx, target.ID, false)
	if err != nil {
		t.Fatalf("clear verified: %v", err)
	}
	if cleared.Verified {
		t.Fatalf("cleared verified = true, want false")
	}
}

func TestServiceRefreshesBaseCacheAfterColorUpdate(t *testing.T) {
	ctx := context.Background()
	base := memory.NewUserStore()
	owner, err := base.Create(ctx, domain.User{AccessHash: 1, Phone: "15550000001", FirstName: "Owner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	store := &countingUserStore{UserStore: base}
	cache := newMemoryBaseUserCache()
	svc := NewService(store, WithBaseUserCache(cache))

	if _, found, err := svc.ByID(ctx, owner.ID, owner.ID); err != nil || !found {
		t.Fatalf("prime cache found=%v err=%v", found, err)
	}
	if store.byIDsCalls != 1 {
		t.Fatalf("store ByIDs calls after prime = %d, want 1", store.byIDsCalls)
	}
	updated, err := svc.UpdateColor(ctx, owner.ID, true, domain.PeerColor{
		HasColor:          true,
		Color:             0,
		BackgroundEmojiID: 123456,
	})
	if err != nil {
		t.Fatalf("UpdateColor: %v", err)
	}
	if !updated.ProfileColor.HasColor || updated.ProfileColor.Color != 0 || updated.ProfileColor.BackgroundEmojiID != 123456 {
		t.Fatalf("updated profile color = %+v, want explicit color=0 bg=123456", updated.ProfileColor)
	}
	got, found, err := svc.ByID(ctx, owner.ID, owner.ID)
	if err != nil || !found {
		t.Fatalf("ByID after color update found=%v err=%v", found, err)
	}
	if store.byIDsCalls != 1 {
		t.Fatalf("store ByIDs calls after cached color read = %d, want still 1", store.byIDsCalls)
	}
	if !got.ProfileColor.HasColor || got.ProfileColor.Color != 0 || got.ProfileColor.BackgroundEmojiID != 123456 {
		t.Fatalf("cached profile color = %+v, want explicit color=0 bg=123456", got.ProfileColor)
	}
}

func TestServiceProjectsUsersForViewerContacts(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	contacts := memory.NewContactStore()
	owner, err := userStore.Create(ctx, domain.User{AccessHash: 1, Phone: "15550000001", FirstName: "Owner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	friend, err := userStore.Create(ctx, domain.User{AccessHash: 2, Phone: "15550000002", FirstName: "Public", LastName: "Name"})
	if err != nil {
		t.Fatalf("create friend: %v", err)
	}
	stranger, err := userStore.Create(ctx, domain.User{AccessHash: 3, Phone: "15550000003", FirstName: "Stranger"})
	if err != nil {
		t.Fatalf("create stranger: %v", err)
	}
	if _, err := contacts.Upsert(ctx, owner.ID, domain.ContactInput{
		ContactUserID: friend.ID,
		Phone:         "15550000002",
		FirstName:     "Remark",
		LastName:      "Friend",
	}); err != nil {
		t.Fatalf("upsert contact: %v", err)
	}
	svc := NewService(userStore, WithContactStore(contacts))

	contactUser, found, err := svc.ByID(ctx, owner.ID, friend.ID)
	if err != nil || !found {
		t.Fatalf("ByID contact found=%v err=%v", found, err)
	}
	if !contactUser.Contact || contactUser.FirstName != "Remark" || contactUser.LastName != "Friend" || contactUser.Phone != "15550000002" {
		t.Fatalf("projected contact = %+v, want contact remark and phone", contactUser)
	}
	nonContact, found, err := svc.ByID(ctx, owner.ID, stranger.ID)
	if err != nil || !found {
		t.Fatalf("ByID non-contact found=%v err=%v", found, err)
	}
	if nonContact.Contact || nonContact.Phone != "" || nonContact.FirstName != "Stranger" {
		t.Fatalf("projected non-contact = %+v, want name with hidden phone", nonContact)
	}
}

type countingUserStore struct {
	*memory.UserStore
	byIDCalls  int
	byIDsCalls int
	lastByID   int64
	lastByIDs  []int64
}

func (s *countingUserStore) ByID(ctx context.Context, id int64) (domain.User, bool, error) {
	s.byIDCalls++
	s.lastByID = id
	return s.UserStore.ByID(ctx, id)
}

func (s *countingUserStore) ByIDs(ctx context.Context, ids []int64) ([]domain.User, error) {
	s.byIDsCalls++
	s.lastByIDs = append([]int64(nil), ids...)
	return s.UserStore.ByIDs(ctx, ids)
}

type memoryBaseUserCache struct {
	users map[int64]domain.User
}

func newMemoryBaseUserCache() *memoryBaseUserCache {
	return &memoryBaseUserCache{users: map[int64]domain.User{}}
}

func (c *memoryBaseUserCache) GetByIDs(_ context.Context, ids []int64) (map[int64]domain.User, error) {
	out := make(map[int64]domain.User, len(ids))
	for _, id := range ids {
		if u, ok := c.users[id]; ok {
			out[id] = u
		}
	}
	return out, nil
}

func (c *memoryBaseUserCache) PutMany(_ context.Context, users []domain.User) error {
	for _, u := range users {
		if u.ID != 0 {
			c.users[u.ID] = u
		}
	}
	return nil
}

func (c *memoryBaseUserCache) Delete(_ context.Context, ids []int64) error {
	for _, id := range ids {
		delete(c.users, id)
	}
	return nil
}
