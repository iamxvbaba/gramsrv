package privacy

import (
	"context"
	"testing"

	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

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
