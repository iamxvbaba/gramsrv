package users

import (
	"context"
	"errors"
	"strings"
	"testing"

	privacyapp "telesrv/internal/app/privacy"
	"telesrv/internal/domain"
	"telesrv/internal/store"
	"telesrv/internal/store/memory"
)

type resolvePhoneCollectibleStore struct {
	store.CollectiblePhoneStore
	asset domain.CollectiblePhone
}

func (s resolvePhoneCollectibleStore) CollectiblePhone(_ context.Context, phone string) (domain.CollectiblePhone, error) {
	if phone == s.asset.Phone {
		return s.asset, nil
	}
	return domain.CollectiblePhone{}, domain.ErrCollectiblePhoneNotFound
}

func (s resolvePhoneCollectibleStore) OwnedCollectiblePhones(_ context.Context, userIDs []int64) (map[int64]domain.CollectiblePhone, error) {
	out := make(map[int64]domain.CollectiblePhone)
	for _, userID := range userIDs {
		if userID == s.asset.OwnerUserID {
			out[userID] = s.asset
		}
	}
	return out, nil
}

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
	registry := memory.NewCollectibleUsernameStore()
	store.AttachUsernameRegistry(registry)
	peer := domain.Peer{Type: domain.PeerTypeUser, ID: owner.ID}
	if _, err := registry.SetEditableUsername(ctx, peer, updated.Username); err != nil {
		t.Fatalf("seed editable username registry: %v", err)
	}
	if _, created, err := registry.MintCollectibleUsername(ctx, domain.MintCollectibleUsernameRequest{
		Username: "nft4",
		Owner:    peer,
		Currency: domain.CollectibleCurrencyStars,
		Amount:   1,
		Actor:    "test",
	}); err != nil || !created {
		t.Fatalf("mint four-character collectible: created=%v err=%v", created, err)
	}
	resolved, found, err = svc.ResolveUsername(ctx, other.ID, "@NFT4")
	if err != nil || !found || resolved.ID != owner.ID {
		t.Fatalf("ResolveUsername collectible = user %+v found %v err %v, want owner", resolved, found, err)
	}
	if _, err := svc.UpdateUsername(ctx, owner.ID, "nft4"); !errors.Is(err, domain.ErrUsernameInvalid) {
		t.Fatalf("four-character editable username err = %v, want username invalid", err)
	}
	if changed, err := registry.SetUsernameActive(ctx, peer, "nft4", false); err != nil || !changed {
		t.Fatalf("deactivate collectible: changed=%v err=%v", changed, err)
	}
	if _, found, err := svc.ResolveUsername(ctx, other.ID, "nft4"); err != nil || found {
		t.Fatalf("inactive collectible found=%v err=%v, want hidden", found, err)
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

func TestByIDsForViewersRejectsOwnerSetAboveBound(t *testing.T) {
	svc := NewService(memory.NewUserStore())
	ids := make([]int64, maxBatchUsers+1)
	for i := range ids {
		ids[i] = int64(i + 1)
	}
	if _, err := svc.ByIDsForViewers(context.Background(), []int64{1}, ids); !errors.Is(err, ErrBatchUsersLimit) {
		t.Fatalf("ByIDsForViewers err = %v, want ErrBatchUsersLimit", err)
	}
}

func TestByIDsRejectsOwnerSetAboveBoundInsteadOfTruncating(t *testing.T) {
	svc := NewService(memory.NewUserStore())
	ids := make([]int64, maxBatchUsers+1)
	for i := range ids {
		ids[i] = int64(i + 1)
	}
	if _, err := svc.ByIDs(context.Background(), 1, ids); !errors.Is(err, ErrBatchUsersLimit) {
		t.Fatalf("ByIDs err = %v, want ErrBatchUsersLimit", err)
	}
}

func TestBotStatusReadsViewerIndependentBaseFact(t *testing.T) {
	ctx := context.Background()
	base := memory.NewUserStore()
	bot, err := base.Create(ctx, domain.User{AccessHash: 1, Phone: "15550000077", FirstName: "Bot", Bot: true})
	if err != nil {
		t.Fatal(err)
	}
	svc := NewService(base)
	got, found, err := svc.BotStatus(ctx, bot.ID)
	if err != nil || !found || !got {
		t.Fatalf("BotStatus = %v, found=%v, err=%v", got, found, err)
	}
	if got, found, err := svc.BotStatus(ctx, bot.ID+1000); err != nil || found || got {
		t.Fatalf("missing BotStatus = %v, found=%v, err=%v", got, found, err)
	}
}

func TestPrivacyBaseUsersRejectsViewerSetAboveBoundInsteadOfNegativeCachingTruncation(t *testing.T) {
	svc := NewService(memory.NewUserStore())
	ids := make([]int64, maxBatchUsers+1)
	for i := range ids {
		ids[i] = int64(i + 1)
	}
	if _, err := svc.PrivacyBaseUsers(context.Background(), ids); !errors.Is(err, ErrBatchUsersLimit) {
		t.Fatalf("PrivacyBaseUsers err = %v, want ErrBatchUsersLimit", err)
	}
}

func TestByIDsForViewersRejectsDenseCellSetAboveBound(t *testing.T) {
	svc := NewService(memory.NewUserStore())
	owners := make([]int64, maxBatchUsers)
	for i := range owners {
		owners[i] = int64(i + 1)
	}
	viewers := make([]int64, maxBatchViewerProjectionCells/maxBatchUsers+1)
	for i := range viewers {
		viewers[i] = int64(10_000 + i)
	}
	if _, err := svc.ByIDsForViewers(context.Background(), viewers, owners); !errors.Is(err, ErrBatchViewerCells) {
		t.Fatalf("ByIDsForViewers err = %v, want ErrBatchViewerCells", err)
	}
	if !batchViewerProjectionCellsAllowed(1, maxBatchViewerProjectionCells) {
		t.Fatal("cell limit rejected exact boundary")
	}
	if batchViewerProjectionCellsAllowed(2, maxBatchViewerProjectionCells) {
		t.Fatal("cell limit accepted overflow boundary")
	}
}

func TestByIDsForViewersRejectsMissingReferencedUser(t *testing.T) {
	svc := NewService(memory.NewUserStore())
	if _, err := svc.ByIDsForViewers(context.Background(), []int64{1001}, []int64{2001}); !errors.Is(err, ErrBatchUserMissing) {
		t.Fatalf("ByIDsForViewers err = %v, want ErrBatchUserMissing", err)
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

func TestResolvePhoneKeepsCollectibleAliasSeparateFromE164Identity(t *testing.T) {
	ctx := context.Background()
	users := memory.NewUserStore()
	viewer, err := users.Create(ctx, domain.User{AccessHash: 1, Phone: "15550001011", FirstName: "Viewer"})
	if err != nil {
		t.Fatalf("create viewer: %v", err)
	}
	target, err := users.Create(ctx, domain.User{AccessHash: 2, Phone: "15550001012", FirstName: "Target"})
	if err != nil {
		t.Fatalf("create target: %v", err)
	}
	asset := domain.CollectiblePhone{
		Phone:       "8881111",
		Tier:        domain.CollectiblePhoneTierExclusive,
		Status:      domain.CollectibleUsernameStatusOwned,
		OwnerUserID: target.ID,
	}
	svc := NewService(users, WithCollectiblePhoneStore(resolvePhoneCollectibleStore{asset: asset}))

	got, found, err := svc.ResolvePhone(ctx, viewer.ID, "+888 1111")
	if err != nil || !found || got.ID != target.ID {
		t.Fatalf("ResolvePhone collectible = %+v found=%v err=%v, want target", got, found, err)
	}
	if got.Phone != asset.Phone {
		t.Fatalf("projected phone = %q, want collectible %q", got.Phone, asset.Phone)
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

	// Set a birthday with a year.
	u, err := svc.UpdateBirthday(ctx, owner.ID, domain.Birthday{Day: 14, Month: 2, Year: 1990})
	if err != nil {
		t.Fatalf("UpdateBirthday: %v", err)
	}
	if u.Birthday != (domain.Birthday{Day: 14, Month: 2, Year: 1990}) {
		t.Fatalf("birthday = %+v, want 14/2/1990", u.Birthday)
	}
	// An invalid month is refused.
	if _, err := svc.UpdateBirthday(ctx, owner.ID, domain.Birthday{Day: 1, Month: 13}); !errors.Is(err, domain.ErrBirthdayInvalid) {
		t.Fatalf("invalid month err = %v, want birthday invalid", err)
	}
	// After clearing (zero value), IsSet=false.
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

func TestServiceUpdateLastSeenBatchIsMonotonicAndInvalidatesOnce(t *testing.T) {
	ctx := context.Background()
	base := memory.NewUserStore()
	first, err := base.Create(ctx, domain.User{AccessHash: 1, Phone: "15550000881", FirstName: "First"})
	if err != nil {
		t.Fatalf("create first: %v", err)
	}
	second, err := base.Create(ctx, domain.User{AccessHash: 2, Phone: "15550000882", FirstName: "Second"})
	if err != nil {
		t.Fatalf("create second: %v", err)
	}
	cache := newMemoryBaseUserCache()
	if err := cache.PutMany(ctx, []domain.User{first, second}); err != nil {
		t.Fatalf("prime cache: %v", err)
	}
	svc := NewService(base, WithBaseUserCache(cache))
	if err := svc.UpdateLastSeenBatch(ctx, []store.UserLastSeenUpdate{
		{UserID: second.ID, LastSeenAt: 20},
		{UserID: first.ID, LastSeenAt: 9},
		{UserID: first.ID, LastSeenAt: 17},
	}); err != nil {
		t.Fatalf("UpdateLastSeenBatch: %v", err)
	}
	loadedFirst, found, err := base.ByID(ctx, first.ID)
	if err != nil || !found || loadedFirst.LastSeenAt != 17 {
		t.Fatalf("first last seen = %d found=%v err=%v, want 17", loadedFirst.LastSeenAt, found, err)
	}
	loadedSecond, found, err := base.ByID(ctx, second.ID)
	if err != nil || !found || loadedSecond.LastSeenAt != 20 {
		t.Fatalf("second last seen = %d found=%v err=%v, want 20", loadedSecond.LastSeenAt, found, err)
	}
	if cache.deleteCalls != 1 {
		t.Fatalf("cache delete calls = %d, want one batch invalidation", cache.deleteCalls)
	}
	if _, ok := cache.users[first.ID]; ok {
		t.Fatal("first user remained cached")
	}
	if _, ok := cache.users[second.ID]; ok {
		t.Fatal("second user remained cached")
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
	if _, err := svc.SetVerified(ctx, domain.PremiumBotConfiguredUserID(), false); !errors.Is(err, ErrSystemUserImmutable) {
		t.Fatalf("clear Premium bot verified err=%v, want ErrSystemUserImmutable", err)
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
	users       map[int64]domain.User
	deleteCalls int
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
	c.deleteCalls++
	for _, id := range ids {
		delete(c.users, id)
	}
	return nil
}

type fakeUserFreezeProvider struct {
	items map[int64]domain.AccountFreeze
}

func (f *fakeUserFreezeProvider) AccountFreezes(_ context.Context, ids []int64) (map[int64]domain.AccountFreeze, error) {
	out := make(map[int64]domain.AccountFreeze, len(ids))
	for _, id := range ids {
		if freeze, ok := f.items[id]; ok && freeze.Frozen {
			out[id] = freeze
		}
	}
	return out, nil
}

// TestFrozenAccountUsernamesHiddenFromResolutionButStayOccupied locks the frozen
// username semantics for every kind of name (editable slot + collectible NFT):
//   - contacts.resolveUsername must NOT resolve a frozen account's usernames.
//   - a claimer updating to one of those names must get USERNAME_OCCUPIED.
//   - unfreezing restores resolution instantly (projection-only, no writes).
func TestFrozenAccountUsernamesHiddenFromResolutionButStayOccupied(t *testing.T) {
	ctx := context.Background()
	store := memory.NewUserStore()
	owner, err := store.Create(ctx, domain.User{AccessHash: 1, Phone: "15550000991", FirstName: "Frozen Owner", Username: "frozen_name"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	claimer, err := store.Create(ctx, domain.User{AccessHash: 2, Phone: "15550000992", FirstName: "Claimer"})
	if err != nil {
		t.Fatalf("create claimer: %v", err)
	}
	registry := memory.NewCollectibleUsernameStore()
	store.AttachUsernameRegistry(registry)
	peer := domain.Peer{Type: domain.PeerTypeUser, ID: owner.ID}
	if _, err := registry.SetEditableUsername(ctx, peer, owner.Username); err != nil {
		t.Fatalf("seed editable registry: %v", err)
	}
	if _, created, err := registry.MintCollectibleUsername(ctx, domain.MintCollectibleUsernameRequest{
		Username: "frozen_nft",
		Owner:    peer,
		Currency: domain.CollectibleCurrencyStars,
		Amount:   1,
		Actor:    "test",
	}); err != nil || !created {
		t.Fatalf("mint collectible: created=%v err=%v", created, err)
	}

	freeze := &fakeUserFreezeProvider{items: map[int64]domain.AccountFreeze{
		owner.ID: {UserID: owner.ID, Frozen: true},
	}}
	svc := NewService(store, WithAccountFreezeProvider(freeze))

	// During the freeze: both the regular and NFT usernames stop resolving.
	if _, found, err := svc.ResolveUsername(ctx, claimer.ID, "frozen_name"); err != nil || found {
		t.Fatalf("frozen editable resolve found=%v err=%v, want hidden", found, err)
	}
	if _, found, err := svc.ResolveUsername(ctx, claimer.ID, "frozen_nft"); err != nil || found {
		t.Fatalf("frozen NFT resolve found=%v err=%v, want hidden", found, err)
	}
	// During the freeze: the occupancy check stays occupied (not released as available).
	if ok, err := svc.CheckUsername(ctx, claimer.ID, "frozen_name"); err != nil || ok {
		t.Fatalf("frozen editable check ok=%v err=%v, want occupied", ok, err)
	}
	if _, err := svc.UpdateUsername(ctx, claimer.ID, "frozen_name"); !errors.Is(err, domain.ErrUsernameOccupied) {
		t.Fatalf("claim frozen editable err=%v, want username occupied", err)
	}

	// Unfreezing restores it: no data write-back, and all usernames resolve again.
	delete(freeze.items, owner.ID)
	resolved, found, err := svc.ResolveUsername(ctx, claimer.ID, "frozen_name")
	if err != nil || !found || resolved.ID != owner.ID {
		t.Fatalf("unfrozen editable resolve = user %+v found=%v err=%v, want owner", resolved, found, err)
	}
	resolved, found, err = svc.ResolveUsername(ctx, claimer.ID, "frozen_nft")
	if err != nil || !found || resolved.ID != owner.ID {
		t.Fatalf("unfrozen NFT resolve = user %+v found=%v err=%v, want owner", resolved, found, err)
	}
}
