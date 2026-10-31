package privacy

import (
	"context"
	"errors"
	"testing"
	"time"

	"telesrv/internal/domain"
	"telesrv/internal/store"
	"telesrv/internal/store/memory"
)

type countingBaseUsers struct {
	calls int
	users map[int64]domain.User
}

func (p *countingBaseUsers) PrivacyBaseUsers(_ context.Context, userIDs []int64) ([]domain.User, error) {
	p.calls++
	out := make([]domain.User, 0, len(userIDs))
	for _, userID := range userIDs {
		if user, ok := p.users[userID]; ok {
			out = append(out, user)
		}
	}
	return out, nil
}

type countingMemberships struct {
	calls         int
	batchCalls    int
	batchRequests []map[int64][]int64
	active        map[int64]map[int64]bool
}

type countingSparseContacts struct {
	store.ContactStore
	sparseCalls int
	getMany     int
	requested   map[int64][]int64
}

func (c *countingSparseContacts) GetMany(ctx context.Context, ownerUserID int64, viewerUserIDs []int64) (map[int64]domain.Contact, error) {
	c.getMany++
	return c.ContactStore.GetMany(ctx, ownerUserID, viewerUserIDs)
}

func (c *countingSparseContacts) ContactProjectionForViewerUserIDs(ctx context.Context, requested map[int64][]int64) (domain.ContactProjectionBatch, error) {
	c.sparseCalls++
	c.requested = make(map[int64][]int64, len(requested))
	for viewerID, targetIDs := range requested {
		c.requested[viewerID] = append([]int64(nil), targetIDs...)
	}
	return c.ContactStore.(store.SparseContactProjectionStore).ContactProjectionForViewerUserIDs(ctx, requested)
}

func (p *countingMemberships) FilterActiveChannelMemberPairs(_ context.Context, requested map[int64][]int64) (map[int64][]int64, error) {
	p.batchCalls++
	cloned := make(map[int64][]int64, len(requested))
	out := make(map[int64][]int64, len(requested))
	for channelID, userIDs := range requested {
		cloned[channelID] = append([]int64(nil), userIDs...)
		for _, userID := range userIDs {
			if p.active[channelID][userID] {
				out[channelID] = append(out[channelID], userID)
			}
		}
	}
	p.batchRequests = append(p.batchRequests, cloned)
	return out, nil
}

func (p *countingMemberships) FilterActiveChannelMemberIDs(_ context.Context, channelID int64, userIDs []int64) ([]int64, error) {
	p.calls++
	out := make([]int64, 0, len(userIDs))
	for _, userID := range userIDs {
		if p.active[channelID][userID] {
			out = append(out, userID)
		}
	}
	return out, nil
}

func TestDefaultPrivacyRules(t *testing.T) {
	ctx := context.Background()
	svc := NewService(memory.NewPrivacyStore(), memory.NewContactStore())
	phone, err := svc.GetRules(ctx, 1001, domain.PrivacyKeyPhoneNumber)
	if err != nil {
		t.Fatalf("phone rules: %v", err)
	}
	if len(phone.Rules) != 1 || phone.Rules[0].Kind != domain.PrivacyRuleDisallowAll {
		t.Fatalf("phone default = %+v, want disallow all", phone.Rules)
	}
	birthday, err := svc.GetRules(ctx, 1001, domain.PrivacyKeyBirthday)
	if err != nil {
		t.Fatalf("birthday rules: %v", err)
	}
	if len(birthday.Rules) != 1 || birthday.Rules[0].Kind != domain.PrivacyRuleAllowContacts {
		t.Fatalf("birthday default = %+v, want allow contacts", birthday.Rules)
	}
	profile, err := svc.GetRules(ctx, 1001, domain.PrivacyKeyProfilePhoto)
	if err != nil {
		t.Fatalf("profile rules: %v", err)
	}
	if len(profile.Rules) != 1 || profile.Rules[0].Kind != domain.PrivacyRuleAllowAll {
		t.Fatalf("profile default = %+v, want allow all", profile.Rules)
	}
	invites, err := svc.GetRules(ctx, 1001, domain.PrivacyKeyChatInvite)
	if err != nil {
		t.Fatalf("chat invite rules: %v", err)
	}
	if len(invites.Rules) != 1 || invites.Rules[0].Kind != domain.PrivacyRuleAllowContacts {
		t.Fatalf("chat invite default = %+v, want allow contacts", invites.Rules)
	}
}

func TestCanSeeAnonymousHonorsPublicOnlyRules(t *testing.T) {
	ctx := context.Background()
	store := memory.NewPrivacyStore()
	svc := NewService(store, nil)
	const ownerID int64 = 1001

	if visible, err := svc.CanSeeAnonymous(ctx, ownerID, domain.PrivacyKeyAbout); err != nil || !visible {
		t.Fatalf("default anonymous about visibility = %v, err=%v; want true", visible, err)
	}
	if _, err := svc.SetRules(ctx, ownerID, domain.PrivacyKeyProfilePhoto, []domain.PrivacyRule{{Kind: domain.PrivacyRuleAllowContacts}}); err != nil {
		t.Fatalf("set contacts-only profile photo: %v", err)
	}
	if visible, err := svc.CanSeeAnonymous(ctx, ownerID, domain.PrivacyKeyProfilePhoto); err != nil || visible {
		t.Fatalf("contacts-only anonymous photo visibility = %v, err=%v; want false", visible, err)
	}
	if _, err := svc.SetRules(ctx, ownerID, domain.PrivacyKeyProfilePhoto, []domain.PrivacyRule{
		{Kind: domain.PrivacyRuleDisallowUsers, UserIDs: []int64{2002}},
		{Kind: domain.PrivacyRuleAllowAll},
	}); err != nil {
		t.Fatalf("set public profile photo: %v", err)
	}
	if visible, err := svc.CanSeeAnonymous(ctx, ownerID, domain.PrivacyKeyProfilePhoto); err != nil || !visible {
		t.Fatalf("allow-all anonymous photo visibility = %v, err=%v; want true", visible, err)
	}
}

func TestAddAllowUserOverridesDisallowAll(t *testing.T) {
	ctx := context.Background()
	svc := NewService(memory.NewPrivacyStore(), memory.NewContactStore())
	if _, err := svc.SetRules(ctx, 1001, domain.PrivacyKeyPhoneNumber, []domain.PrivacyRule{{Kind: domain.PrivacyRuleDisallowAll}}); err != nil {
		t.Fatalf("set rules: %v", err)
	}
	allowed, err := svc.CanSee(ctx, 1001, 1002, domain.PrivacyKeyPhoneNumber)
	if err != nil {
		t.Fatalf("can see before: %v", err)
	}
	if allowed {
		t.Fatal("viewer should not see phone before exception")
	}
	if _, changed, err := svc.AddAllowUser(ctx, 1001, domain.PrivacyKeyPhoneNumber, 1002); err != nil {
		t.Fatalf("add allow: %v", err)
	} else if !changed {
		t.Fatal("first add allow should report changed")
	}
	allowed, err = svc.CanSee(ctx, 1001, 1002, domain.PrivacyKeyPhoneNumber)
	if err != nil {
		t.Fatalf("can see after: %v", err)
	}
	if !allowed {
		t.Fatal("viewer should see phone after allow-user exception")
	}
}

func TestExplicitDisallowUserWins(t *testing.T) {
	rules := domain.PrivacyRules{
		Key: domain.PrivacyKeyProfilePhoto,
		Rules: []domain.PrivacyRule{
			{Kind: domain.PrivacyRuleAllowAll},
			{Kind: domain.PrivacyRuleDisallowUsers, UserIDs: []int64{1002}},
		},
	}
	if Evaluate(rules, domain.PrivacyContext{OwnerUserID: 1001, ViewerUserID: 1002}) {
		t.Fatal("explicit disallow user should win over allow all")
	}
}

// TestCanSeeBatchEquivalentToCanSee locks in that batch privacy evaluation is byte-equivalent
// to per-call CanSee (the correctness premise of the projectBatch fan-out N+1 optimization):
// covers default rules/allow-all/disallow-all/allow-contacts (with a contact)/self.
func TestCanSeeBatchEquivalentToCanSee(t *testing.T) {
	ctx := context.Background()
	contacts := memory.NewContactStore()
	svc := NewService(memory.NewPrivacyStore(), contacts)
	const viewer = int64(1002)
	owners := []int64{1001, 1003, 1004, 1005, viewer}

	if _, err := svc.SetRules(ctx, 1003, domain.PrivacyKeyPhoneNumber, []domain.PrivacyRule{{Kind: domain.PrivacyRuleAllowAll}}); err != nil {
		t.Fatalf("set 1003 phone: %v", err)
	}
	if _, err := svc.SetRules(ctx, 1004, domain.PrivacyKeyStatusTimestamp, []domain.PrivacyRule{{Kind: domain.PrivacyRuleDisallowAll}}); err != nil {
		t.Fatalf("set 1004 status: %v", err)
	}
	if _, err := svc.SetRules(ctx, 1005, domain.PrivacyKeyPhoneNumber, []domain.PrivacyRule{{Kind: domain.PrivacyRuleAllowContacts}}); err != nil {
		t.Fatalf("set 1005 phone: %v", err)
	}
	// owner 1005 adds the viewer as a contact (GetReverseContacts(viewer,[1005]) hits → allow-contacts is visible).
	if _, err := contacts.Upsert(ctx, 1005, domain.ContactInput{ContactUserID: viewer}); err != nil {
		t.Fatalf("upsert contact: %v", err)
	}

	keys := []domain.PrivacyKey{domain.PrivacyKeyPhoneNumber, domain.PrivacyKeyStatusTimestamp, domain.PrivacyKeyProfilePhoto}
	batch, err := svc.CanSeeBatch(ctx, owners, viewer, keys)
	if err != nil {
		t.Fatalf("CanSeeBatch: %v", err)
	}
	for _, owner := range owners {
		for _, k := range keys {
			want, err := svc.CanSee(ctx, owner, viewer, k)
			if err != nil {
				t.Fatalf("CanSee(%d,%d,%v): %v", owner, viewer, k, err)
			}
			got, ok := batch[owner][k]
			if !ok {
				t.Fatalf("CanSeeBatch missing owner=%d key=%v", owner, k)
			}
			if got != want {
				t.Fatalf("CanSeeBatch[%d][%v]=%v != CanSee=%v (must be equivalent)", owner, k, got, want)
			}
		}
	}
}

// TestCanSeeMatrixEquivalentToCanSee locks in that the owners×viewers×keys matrix evaluation
// is byte-equivalent to per-call CanSee (the correctness premise of the ForViewers fan-out
// templating reducing privacy queries to O(owner)). Covers multiple owners and viewers:
// different rules, contact direction (allow-contacts hits only when the owner has added the
// viewer), self (owner==viewer).
func TestCanSeeMatrixEquivalentToCanSee(t *testing.T) {
	ctx := context.Background()
	contacts := memory.NewContactStore()
	svc := NewService(memory.NewPrivacyStore(), contacts)
	owners := []int64{6001, 6002, 6003, 6004}
	viewers := []int64{7001, 7002, 6002} // 6002 is both an owner and a viewer → hits the self branch

	if _, err := svc.SetRules(ctx, 6002, domain.PrivacyKeyPhoneNumber, []domain.PrivacyRule{{Kind: domain.PrivacyRuleAllowAll}}); err != nil {
		t.Fatalf("set 6002 phone: %v", err)
	}
	if _, err := svc.SetRules(ctx, 6003, domain.PrivacyKeyStatusTimestamp, []domain.PrivacyRule{{Kind: domain.PrivacyRuleDisallowAll}}); err != nil {
		t.Fatalf("set 6003 status: %v", err)
	}
	if _, err := svc.SetRules(ctx, 6004, domain.PrivacyKeyPhoneNumber, []domain.PrivacyRule{{Kind: domain.PrivacyRuleAllowContacts}}); err != nil {
		t.Fatalf("set 6004 phone: %v", err)
	}
	// owner 6004 adds viewer 7001 as a contact (owner→viewer direction = privacy's ViewerIsContact).
	if _, err := contacts.Upsert(ctx, 6004, domain.ContactInput{ContactUserID: 7001}); err != nil {
		t.Fatalf("upsert contact: %v", err)
	}

	keys := []domain.PrivacyKey{domain.PrivacyKeyPhoneNumber, domain.PrivacyKeyStatusTimestamp, domain.PrivacyKeyProfilePhoto}
	matrix, err := svc.CanSeeMatrix(ctx, owners, viewers, keys)
	if err != nil {
		t.Fatalf("CanSeeMatrix: %v", err)
	}
	for _, owner := range owners {
		for _, viewer := range viewers {
			for _, k := range keys {
				want, err := svc.CanSee(ctx, owner, viewer, k)
				if err != nil {
					t.Fatalf("CanSee(%d,%d,%v): %v", owner, viewer, k, err)
				}
				got, ok := matrix[owner][viewer][k]
				if !ok {
					t.Fatalf("CanSeeMatrix missing owner=%d viewer=%d key=%v", owner, viewer, k)
				}
				if got != want {
					t.Fatalf("CanSeeMatrix[%d][%d][%v]=%v != CanSee=%v (must be equivalent)", owner, viewer, k, got, want)
				}
			}
		}
	}
}

func TestCanSeeMatrixLoadsOwnerViewerContactsInOneSparseBatch(t *testing.T) {
	ctx := context.Background()
	inner := memory.NewContactStore()
	contacts := &countingSparseContacts{ContactStore: inner}
	svc := NewService(memory.NewPrivacyStore(), contacts)
	owners := []int64{6101, 6102}
	viewers := []int64{7101, 7102}
	for _, owner := range owners {
		if _, err := svc.SetRules(ctx, owner, domain.PrivacyKeyPhoneNumber, []domain.PrivacyRule{
			{Kind: domain.PrivacyRuleAllowContacts},
			{Kind: domain.PrivacyRuleDisallowAll},
		}); err != nil {
			t.Fatalf("set owner %d rules: %v", owner, err)
		}
	}
	if _, err := inner.Upsert(ctx, owners[0], domain.ContactInput{ContactUserID: viewers[0]}); err != nil {
		t.Fatalf("upsert contact: %v", err)
	}

	matrix, err := svc.CanSeeMatrix(ctx, owners, viewers, []domain.PrivacyKey{domain.PrivacyKeyPhoneNumber})
	if err != nil {
		t.Fatalf("CanSeeMatrix: %v", err)
	}
	if contacts.sparseCalls != 1 || contacts.getMany != 0 {
		t.Fatalf("contact reads = sparse %d / GetMany %d, want 1 / 0", contacts.sparseCalls, contacts.getMany)
	}
	for _, owner := range owners {
		if got := len(contacts.requested[owner]); got != len(viewers) {
			t.Fatalf("requested owner %d viewers = %v, want %v", owner, contacts.requested[owner], viewers)
		}
	}
	if !matrix[owners[0]][viewers[0]][domain.PrivacyKeyPhoneNumber] {
		t.Fatal("owner contact relation was not applied")
	}
	if matrix[owners[0]][viewers[1]][domain.PrivacyKeyPhoneNumber] ||
		matrix[owners[1]][viewers[0]][domain.PrivacyKeyPhoneNumber] ||
		matrix[owners[1]][viewers[1]][domain.PrivacyKeyPhoneNumber] {
		t.Fatalf("unexpected non-contact visibility: %+v", matrix)
	}
}

func TestViewerFactsReadModelBatchesCachesAndInvalidates(t *testing.T) {
	ctx := context.Background()
	rules := memory.NewPrivacyStore()
	users := &countingBaseUsers{users: map[int64]domain.User{
		2001: {ID: 2001, PremiumUntil: 2000},
		2002: {ID: 2002, Bot: true},
	}}
	svc := NewService(rules, memory.NewContactStore()).ConfigureReadModels(users, nil)
	svc.now = func() time.Time { return time.Unix(1000, 0) }

	if _, err := svc.SetRules(ctx, 1001, domain.PrivacyKeyNoPaidMessages, []domain.PrivacyRule{
		{Kind: domain.PrivacyRuleAllowPremium},
		{Kind: domain.PrivacyRuleDisallowAll},
	}); err != nil {
		t.Fatalf("set premium rules: %v", err)
	}
	if _, err := svc.SetRules(ctx, 1002, domain.PrivacyKeyNoPaidMessages, []domain.PrivacyRule{
		{Kind: domain.PrivacyRuleAllowBots},
		{Kind: domain.PrivacyRuleDisallowAll},
	}); err != nil {
		t.Fatalf("set bot rules: %v", err)
	}

	got, err := svc.CanSeeMatrix(
		ctx,
		[]int64{1001, 1002},
		[]int64{2001, 2002},
		[]domain.PrivacyKey{domain.PrivacyKeyNoPaidMessages},
	)
	if err != nil {
		t.Fatalf("CanSeeMatrix: %v", err)
	}
	if !got[1001][2001][domain.PrivacyKeyNoPaidMessages] ||
		got[1001][2002][domain.PrivacyKeyNoPaidMessages] ||
		got[1002][2001][domain.PrivacyKeyNoPaidMessages] ||
		!got[1002][2002][domain.PrivacyKeyNoPaidMessages] {
		t.Fatalf("unexpected premium/bot visibility matrix: %+v", got)
	}
	if users.calls != 1 {
		t.Fatalf("base user cold loads = %d, want one batched load", users.calls)
	}

	if premium, err := svc.ViewerIsPremium(ctx, 2001); err != nil || !premium {
		t.Fatalf("warm ViewerIsPremium = %v, err=%v; want true", premium, err)
	}
	if users.calls != 1 {
		t.Fatalf("warm viewer facts hit called backend: calls=%d", users.calls)
	}

	users.users[2001] = domain.User{ID: 2001}
	svc.InvalidateViewerFacts(2001)
	if premium, err := svc.ViewerIsPremium(ctx, 2001); err != nil || premium {
		t.Fatalf("invalidated ViewerIsPremium = %v, err=%v; want false", premium, err)
	}
	if users.calls != 2 {
		t.Fatalf("invalidated viewer facts cold loads = %d, want 2", users.calls)
	}
}

func TestMembershipReadModelCachesNegativeFactsAndInvalidatesPair(t *testing.T) {
	ctx := context.Background()
	rules := memory.NewPrivacyStore()
	memberships := &countingMemberships{active: map[int64]map[int64]bool{
		9001: {2001: true},
		9002: {},
	}}
	svc := NewService(rules, memory.NewContactStore()).ConfigureReadModels(nil, memberships)
	if _, err := svc.SetRules(ctx, 1001, domain.PrivacyKeyChatInvite, []domain.PrivacyRule{
		{Kind: domain.PrivacyRuleAllowChatParticipants, ChatIDs: []int64{9001, 9002}},
		{Kind: domain.PrivacyRuleDisallowAll},
	}); err != nil {
		t.Fatalf("set participant rules: %v", err)
	}

	got, err := svc.CanSeeMatrix(
		ctx,
		[]int64{1001},
		[]int64{2001, 2002},
		[]domain.PrivacyKey{domain.PrivacyKeyChatInvite},
	)
	if err != nil {
		t.Fatalf("CanSeeMatrix: %v", err)
	}
	if !got[1001][2001][domain.PrivacyKeyChatInvite] ||
		got[1001][2002][domain.PrivacyKeyChatInvite] {
		t.Fatalf("unexpected membership visibility matrix: %+v", got)
	}
	if memberships.batchCalls != 1 || memberships.calls != 0 {
		t.Fatalf("membership cold loads = batch %d scalar %d, want batch=1 scalar=0", memberships.batchCalls, memberships.calls)
	}

	if allowed, err := svc.CanSee(ctx, 1001, 2002, domain.PrivacyKeyChatInvite); err != nil || allowed {
		t.Fatalf("warm negative membership = %v, err=%v; want false", allowed, err)
	}
	if memberships.batchCalls != 1 || memberships.calls != 0 {
		t.Fatalf("negative cache missed: batch=%d scalar=%d", memberships.batchCalls, memberships.calls)
	}

	memberships.active[9002][2002] = true
	svc.InvalidateMembership(9002, 2002)
	if allowed, err := svc.CanSee(ctx, 1001, 2002, domain.PrivacyKeyChatInvite); err != nil || !allowed {
		t.Fatalf("invalidated membership = %v, err=%v; want true", allowed, err)
	}
	if memberships.batchCalls != 2 || memberships.calls != 0 {
		t.Fatalf("pair invalidation reloads = batch %d scalar %d, want batch=2 scalar=0", memberships.batchCalls, memberships.calls)
	}
}

func TestSparsePrivacyMembershipUsesOneExactPairBatch(t *testing.T) {
	ctx := context.Background()
	const (
		ownerA  = int64(1001)
		ownerB  = int64(1002)
		viewerA = int64(2001)
		viewerB = int64(2002)
		chatA   = int64(9001)
		chatB   = int64(9002)
	)
	rules := memory.NewPrivacyStore()
	memberships := &countingMemberships{active: map[int64]map[int64]bool{
		chatA: {viewerA: true, viewerB: true},
		chatB: {viewerA: true, viewerB: true},
	}}
	svc := NewService(rules, memory.NewContactStore()).ConfigureReadModels(nil, memberships)
	for ownerID, chatID := range map[int64]int64{ownerA: chatA, ownerB: chatB} {
		if _, err := svc.SetRules(ctx, ownerID, domain.PrivacyKeyProfilePhoto, []domain.PrivacyRule{
			{Kind: domain.PrivacyRuleAllowChatParticipants, ChatIDs: []int64{chatID}},
			{Kind: domain.PrivacyRuleDisallowAll},
		}); err != nil {
			t.Fatalf("SetRules(%d): %v", ownerID, err)
		}
	}
	got, err := svc.CanSeeForViewerUserIDs(ctx, map[int64][]int64{
		viewerA: {ownerA},
		viewerB: {ownerB},
	}, []domain.PrivacyKey{domain.PrivacyKeyProfilePhoto}, map[int64]map[int64]domain.Contact{})
	if err != nil {
		t.Fatalf("CanSeeForViewerUserIDs: %v", err)
	}
	if !got[ownerA][viewerA][domain.PrivacyKeyProfilePhoto] || !got[ownerB][viewerB][domain.PrivacyKeyProfilePhoto] {
		t.Fatalf("visibility = %+v, want both exact pairs visible", got)
	}
	if memberships.batchCalls != 1 || memberships.calls != 0 {
		t.Fatalf("membership loads = batch %d scalar %d, want batch=1 scalar=0", memberships.batchCalls, memberships.calls)
	}
	requested := memberships.batchRequests[0]
	if len(requested) != 2 || len(requested[chatA]) != 1 || requested[chatA][0] != viewerA || len(requested[chatB]) != 1 || requested[chatB][0] != viewerB {
		t.Fatalf("membership request = %+v, want only (%d,%d) and (%d,%d)", requested, chatA, viewerA, chatB, viewerB)
	}
}

func TestSparsePrivacyMembershipRejectsDerivedPairOverflowBeforeLoad(t *testing.T) {
	ctx := context.Background()
	rules := memory.NewPrivacyStore()
	memberships := &countingMemberships{active: map[int64]map[int64]bool{}}
	svc := NewService(rules, memory.NewContactStore()).ConfigureReadModels(nil, memberships)
	owners := []int64{1001, 1002, 1003, 1004, 1005}
	for ownerIndex, ownerID := range owners {
		chatIDs := make([]int64, 5000)
		for i := range chatIDs {
			chatIDs[i] = int64(100000 + ownerIndex*10000 + i)
		}
		if _, err := svc.SetRules(ctx, ownerID, domain.PrivacyKeyProfilePhoto, []domain.PrivacyRule{
			{Kind: domain.PrivacyRuleAllowChatParticipants, ChatIDs: chatIDs},
			{Kind: domain.PrivacyRuleDisallowAll},
		}); err != nil {
			t.Fatalf("SetRules(%d): %v", ownerID, err)
		}
	}
	_, err := svc.CanSeeForViewerUserIDs(ctx, map[int64][]int64{
		2001: owners,
		2002: owners,
		2003: owners,
	}, []domain.PrivacyKey{domain.PrivacyKeyProfilePhoto}, map[int64]map[int64]domain.Contact{})
	if !errors.Is(err, store.ErrActiveChannelMemberPairsLimit) {
		t.Fatalf("CanSeeForViewerUserIDs error = %v, want ErrActiveChannelMemberPairsLimit", err)
	}
	if memberships.batchCalls != 0 || memberships.calls != 0 {
		t.Fatalf("membership loads = batch %d scalar %d, want fail before load", memberships.batchCalls, memberships.calls)
	}
}

func TestDensePrivacyMembershipRejectsDerivedPairOverflowBeforeLoad(t *testing.T) {
	memberships := &countingMemberships{active: map[int64]map[int64]bool{}}
	svc := NewService(memory.NewPrivacyStore(), memory.NewContactStore()).ConfigureReadModels(nil, memberships)
	chatIDs := make([]int64, 257)
	viewerIDs := make([]int64, 256)
	for i := range chatIDs {
		chatIDs[i] = int64(i + 1)
	}
	for i := range viewerIDs {
		viewerIDs[i] = int64(1000 + i)
	}
	_, err := svc.loadMembershipFacts(context.Background(), chatIDs, viewerIDs)
	if !errors.Is(err, store.ErrActiveChannelMemberPairsLimit) {
		t.Fatalf("loadMembershipFacts error = %v, want ErrActiveChannelMemberPairsLimit", err)
	}
	if memberships.batchCalls != 0 || memberships.calls != 0 {
		t.Fatalf("membership loads = batch %d scalar %d, want fail before load", memberships.batchCalls, memberships.calls)
	}
}

func TestLargeMembershipBatchBypassesLRUAdmissionWithoutEvictingHotPair(t *testing.T) {
	ctx := context.Background()
	memberships := &countingMemberships{active: map[int64]map[int64]bool{}}
	svc := NewService(memory.NewPrivacyStore(), memory.NewContactStore()).ConfigureReadModels(nil, memberships)
	hot := membershipKey{ChatID: 9001, UserID: 2001}
	if _, err := svc.loadMembershipFactsForKeys(ctx, []membershipKey{hot}); err != nil {
		t.Fatalf("warm hot membership pair: %v", err)
	}
	large := make([]membershipKey, store.MaxActiveChannelMemberPairs)
	for i := range large {
		large[i] = membershipKey{ChatID: 9002, UserID: int64(100000 + i)}
	}
	if _, err := svc.loadMembershipFactsForKeys(ctx, large); err != nil {
		t.Fatalf("load large membership batch: %v", err)
	}
	if memberships.batchCalls != 2 || memberships.calls != 0 {
		t.Fatalf("loads after large batch = batch %d scalar %d, want batch=2 scalar=0", memberships.batchCalls, memberships.calls)
	}
	if _, err := svc.loadMembershipFactsForKeys(ctx, []membershipKey{hot}); err != nil {
		t.Fatalf("reload hot membership pair: %v", err)
	}
	if memberships.batchCalls != 2 || memberships.calls != 0 {
		t.Fatalf("hot pair was evicted by non-admitted batch: batch=%d scalar=%d", memberships.batchCalls, memberships.calls)
	}
}

func TestP2PAllowedBetween(t *testing.T) {
	ctx := context.Background()
	const (
		aliceID int64 = 11
		bobID   int64 = 22
		carolID int64 = 33
	)
	allowAll := []domain.PrivacyRule{{Kind: domain.PrivacyRuleAllowAll}}
	disallowAll := []domain.PrivacyRule{{Kind: domain.PrivacyRuleDisallowAll}}
	addContact := func(t *testing.T, contacts store.ContactStore, owner, contact int64) {
		t.Helper()
		if _, err := contacts.Upsert(ctx, owner, domain.ContactInput{ContactUserID: contact, FirstName: "Contact"}); err != nil {
			t.Fatalf("upsert contact %d->%d: %v", owner, contact, err)
		}
	}

	t.Run("mutual contacts default allow", func(t *testing.T) {
		contacts := memory.NewContactStore()
		svc := NewService(memory.NewPrivacyStore(), contacts)
		addContact(t, contacts, aliceID, bobID)
		addContact(t, contacts, bobID, aliceID)
		allowed, err := svc.P2PAllowedBetween(ctx, aliceID, bobID)
		if err != nil || !allowed {
			t.Fatalf("P2PAllowedBetween(alice,bob) = %v err=%v, want true (mutual contacts, default rules)", allowed, err)
		}
	})

	t.Run("one-sided contact denied", func(t *testing.T) {
		contacts := memory.NewContactStore()
		svc := NewService(memory.NewPrivacyStore(), contacts)
		addContact(t, contacts, aliceID, bobID)
		allowed, err := svc.P2PAllowedBetween(ctx, aliceID, bobID)
		if err != nil || allowed {
			t.Fatalf("P2PAllowedBetween(alice,bob) = %v err=%v, want false (one-sided)", allowed, err)
		}
	})

	t.Run("strangers with stored allow-all still denied", func(t *testing.T) {
		svc := NewService(memory.NewPrivacyStore(), memory.NewContactStore())
		if _, err := svc.SetRules(ctx, aliceID, domain.PrivacyKeyPhoneP2P, allowAll); err != nil {
			t.Fatalf("set alice phone_p2p allow-all: %v", err)
		}
		if _, err := svc.SetRules(ctx, bobID, domain.PrivacyKeyPhoneP2P, allowAll); err != nil {
			t.Fatalf("set bob phone_p2p allow-all: %v", err)
		}
		allowed, err := svc.P2PAllowedBetween(ctx, aliceID, bobID)
		if err != nil || allowed {
			t.Fatalf("P2PAllowedBetween(alice,bob) = %v err=%v, want false (gate overrides stored allow-all for non-contacts)", allowed, err)
		}
	})

	t.Run("mutual contacts with stored allow-all allowed", func(t *testing.T) {
		contacts := memory.NewContactStore()
		svc := NewService(memory.NewPrivacyStore(), contacts)
		addContact(t, contacts, aliceID, bobID)
		addContact(t, contacts, bobID, aliceID)
		if _, err := svc.SetRules(ctx, aliceID, domain.PrivacyKeyPhoneP2P, allowAll); err != nil {
			t.Fatalf("set alice phone_p2p allow-all: %v", err)
		}
		if _, err := svc.SetRules(ctx, bobID, domain.PrivacyKeyPhoneP2P, allowAll); err != nil {
			t.Fatalf("set bob phone_p2p allow-all: %v", err)
		}
		allowed, err := svc.P2PAllowedBetween(ctx, aliceID, bobID)
		if err != nil || !allowed {
			t.Fatalf("P2PAllowedBetween(alice,bob) = %v err=%v, want true (mutual contacts, allow-all)", allowed, err)
		}
	})

	t.Run("mutual contacts but one side disallows all denied", func(t *testing.T) {
		contacts := memory.NewContactStore()
		svc := NewService(memory.NewPrivacyStore(), contacts)
		addContact(t, contacts, aliceID, bobID)
		addContact(t, contacts, bobID, aliceID)
		if _, err := svc.SetRules(ctx, bobID, domain.PrivacyKeyPhoneP2P, disallowAll); err != nil {
			t.Fatalf("set bob phone_p2p disallow-all: %v", err)
		}
		allowed, err := svc.P2PAllowedBetween(ctx, aliceID, bobID)
		if err != nil || allowed {
			t.Fatalf("P2PAllowedBetween(alice,bob) = %v err=%v, want false (callee disallows)", allowed, err)
		}
	})

	t.Run("self call denied", func(t *testing.T) {
		svc := NewService(memory.NewPrivacyStore(), memory.NewContactStore())
		allowed, err := svc.P2PAllowedBetween(ctx, aliceID, aliceID)
		if err != nil || allowed {
			t.Fatalf("P2PAllowedBetween(self) = %v err=%v, want false", allowed, err)
		}
	})
}
