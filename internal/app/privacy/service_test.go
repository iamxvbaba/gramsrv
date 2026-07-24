package privacy

import (
	"context"
	"testing"
	"time"

	"telesrv/internal/domain"
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
	calls  int
	active map[int64]map[int64]bool
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

// TestCanSeeBatchEquivalentToCanSee 锁定批量 privacy 评估与逐 CanSee 字节等价（projectBatch
// fan-out N+1 优化的正确性前提）：覆盖默认规则/allow-all/disallow-all/allow-contacts(含联系人)/self。
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
	// owner 1005 把 viewer 加为联系人（GetReverseContacts(viewer,[1005]) 命中 → allow-contacts 可见）。
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

// TestCanSeeMatrixEquivalentToCanSee 锁定 owners×viewers×keys 矩阵评估与逐 CanSee 字节等价
// （ForViewers fan-out 模板化把 privacy 查询降到 O(owner) 的正确性前提）。覆盖多 owner 多 viewer：
// 不同规则、联系人方向（owner 把 viewer 加为联系人才命中 allow-contacts）、self（owner==viewer）。
func TestCanSeeMatrixEquivalentToCanSee(t *testing.T) {
	ctx := context.Background()
	contacts := memory.NewContactStore()
	svc := NewService(memory.NewPrivacyStore(), contacts)
	owners := []int64{6001, 6002, 6003, 6004}
	viewers := []int64{7001, 7002, 6002} // 6002 既是 owner 又是 viewer → 命中 self 分支

	if _, err := svc.SetRules(ctx, 6002, domain.PrivacyKeyPhoneNumber, []domain.PrivacyRule{{Kind: domain.PrivacyRuleAllowAll}}); err != nil {
		t.Fatalf("set 6002 phone: %v", err)
	}
	if _, err := svc.SetRules(ctx, 6003, domain.PrivacyKeyStatusTimestamp, []domain.PrivacyRule{{Kind: domain.PrivacyRuleDisallowAll}}); err != nil {
		t.Fatalf("set 6003 status: %v", err)
	}
	if _, err := svc.SetRules(ctx, 6004, domain.PrivacyKeyPhoneNumber, []domain.PrivacyRule{{Kind: domain.PrivacyRuleAllowContacts}}); err != nil {
		t.Fatalf("set 6004 phone: %v", err)
	}
	// owner 6004 把 viewer 7001 加为联系人（owner→viewer 方向 = privacy 的 ViewerIsContact）。
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
	if memberships.calls != 2 {
		t.Fatalf("membership cold loads = %d, want one batch per referenced chat", memberships.calls)
	}

	if allowed, err := svc.CanSee(ctx, 1001, 2002, domain.PrivacyKeyChatInvite); err != nil || allowed {
		t.Fatalf("warm negative membership = %v, err=%v; want false", allowed, err)
	}
	if memberships.calls != 2 {
		t.Fatalf("negative cache missed: calls=%d", memberships.calls)
	}

	memberships.active[9002][2002] = true
	svc.InvalidateMembership(9002, 2002)
	if allowed, err := svc.CanSee(ctx, 1001, 2002, domain.PrivacyKeyChatInvite); err != nil || !allowed {
		t.Fatalf("invalidated membership = %v, err=%v; want true", allowed, err)
	}
	if memberships.calls != 3 {
		t.Fatalf("pair invalidation reloads = %d, want 3", memberships.calls)
	}
}
