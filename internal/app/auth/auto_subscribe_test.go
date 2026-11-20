package auth

import (
	"context"
	"sync"
	"testing"

	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

// recordingJoiner captures auto-subscribe join attempts. Err, when set, is returned for every call so
// the tests can prove the hook stays best effort.
type recordingJoiner struct {
	mu       sync.Mutex
	attempts []int64
	err      error
}

func (r *recordingJoiner) JoinChannel(_ context.Context, userID, channelID int64, _ int) (domain.CreateChannelResult, error) {
	if userID == 0 || channelID == 0 {
		return domain.CreateChannelResult{}, domain.ErrChannelInvalid
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.attempts = append(r.attempts, channelID)
	if r.err != nil {
		return domain.CreateChannelResult{}, r.err
	}
	return domain.CreateChannelResult{}, nil
}

func (r *recordingJoiner) calls() []int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]int64(nil), r.attempts...)
}

func TestAutoSubscribeJoinsNewRegistrations(t *testing.T) {
	ctx := context.Background()
	joiner := &recordingJoiner{}
	svc := NewService(memory.NewUserStore(), memory.NewAuthorizationStore(), memory.NewCodeStore(), nil, nil, "12345",
		WithAutoSubscribe(joiner, []int64{101, 202, 303}))
	phone := "15550009901"

	hash, err := svc.SendCode(ctx, phone)
	if err != nil {
		t.Fatalf("SendCode: %v", err)
	}
	verifyCodeForSignUp(t, svc, phone, hash, "12345")
	u, _, err := svc.SignUp(ctx, domain.Authorization{}, phone, hash, "Auto", "Subscribed")
	if err != nil {
		t.Fatalf("SignUp: %v", err)
	}
	if u.ID == 0 {
		t.Fatalf("SignUp returned empty user")
	}
	got := joiner.calls()
	if len(got) != 3 || got[0] != 101 || got[1] != 202 || got[2] != 303 {
		t.Fatalf("auto-subscribe joins = %v, want [101 202 303]", got)
	}
}

func TestAutoSubscribeFailureDoesNotFailRegistration(t *testing.T) {
	ctx := context.Background()
	joiner := &recordingJoiner{err: domain.ErrChannelInvalid}
	svc := NewService(memory.NewUserStore(), memory.NewAuthorizationStore(), memory.NewCodeStore(), nil, nil, "12345",
		WithAutoSubscribe(joiner, []int64{101}))
	phone := "15550009902"

	hash, err := svc.SendCode(ctx, phone)
	if err != nil {
		t.Fatalf("SendCode: %v", err)
	}
	verifyCodeForSignUp(t, svc, phone, hash, "12345")
	u, _, err := svc.SignUp(ctx, domain.Authorization{}, phone, hash, "Still", "Registers")
	if err != nil || u.ID == 0 {
		t.Fatalf("SignUp with failing joiner user=%+v err=%v, want success", u, err)
	}
	if len(joiner.calls()) != 1 {
		t.Fatalf("joiner attempts = %d, want 1", len(joiner.calls()))
	}
}

func TestAutoSubscribeDisabledWithoutOption(t *testing.T) {
	ctx := context.Background()
	joiner := &recordingJoiner{}
	svc := NewService(memory.NewUserStore(), memory.NewAuthorizationStore(), memory.NewCodeStore(), nil, nil, "12345")
	phone := "15550009903"

	hash, err := svc.SendCode(ctx, phone)
	if err != nil {
		t.Fatalf("SendCode: %v", err)
	}
	verifyCodeForSignUp(t, svc, phone, hash, "12345")
	if _, _, err := svc.SignUp(ctx, domain.Authorization{}, phone, hash, "No", "Channels"); err != nil {
		t.Fatalf("SignUp: %v", err)
	}
	if got := joiner.calls(); len(got) != 0 {
		t.Fatalf("auto-subscribe without option joined %v, want none", got)
	}
}

func TestAutoSubscribeIgnoresEmptyChannelListAndZeroUser(t *testing.T) {
	joiner := &recordingJoiner{}
	// The hook must be a no-op, not a panic, for zero inputs.
	svc := NewService(memory.NewUserStore(), memory.NewAuthorizationStore(), memory.NewCodeStore(), nil, nil, "12345",
		WithAutoSubscribe(joiner, nil))
	svc.autoSubscribe(context.Background(), 0)
	if got := joiner.calls(); len(got) != 0 {
		t.Fatalf("empty list joined %v, want none", got)
	}
}
