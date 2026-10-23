package auth

import (
	"context"
	"testing"

	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

type fakeCollectiblePhones map[string]domain.CollectiblePhone

func (f fakeCollectiblePhones) CollectiblePhone(_ context.Context, phone string) (domain.CollectiblePhone, error) {
	cp, ok := f[phone]
	if !ok {
		return domain.CollectiblePhone{}, domain.ErrCollectiblePhoneNotFound
	}
	return cp, nil
}

func TestOwnedCollectiblePhoneSignsInToOwner(t *testing.T) {
	ctx := context.Background()
	users := memory.NewUserStore()
	owner, err := users.Create(ctx, domain.User{Phone: "380679517272", FirstName: "Owner", AccessHash: 1})
	if err != nil {
		t.Fatal(err)
	}
	phones := fakeCollectiblePhones{
		"88888888888": {Phone: "88888888888", Status: domain.CollectibleUsernameStatusOwned, OwnerUserID: owner.ID},
		"88800000001": {Phone: "88800000001", Status: domain.CollectibleUsernameStatusVault},
	}
	svc := NewService(users, memory.NewAuthorizationStore(), memory.NewCodeStore(), memory.NewAuthKeyStore(), nil, "12345",
		WithCollectiblePhoneLogin(phones))

	got, found, err := svc.currentPhoneOwner(ctx, "88888888888")
	if err != nil || !found || got.ID != owner.ID {
		t.Fatalf("NFT number owner = %+v found=%v err=%v, want account %d", got, found, err, owner.ID)
	}
	if _, found, err := svc.currentPhoneOwner(ctx, "88800000001"); err != nil || found {
		t.Fatalf("unowned NFT number resolved: found=%v err=%v", found, err)
	}
	if _, found, err := svc.currentPhoneOwner(ctx, "79990001122"); err != nil || found {
		t.Fatalf("unknown number resolved: found=%v err=%v", found, err)
	}
	if got, found, _ := svc.currentPhoneOwner(ctx, "380679517272"); !found || got.ID != owner.ID {
		t.Fatal("ordinary phone lookup broke")
	}
}
