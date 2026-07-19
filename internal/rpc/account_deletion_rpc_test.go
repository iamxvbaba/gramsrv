package rpc

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/proto"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"go.uber.org/zap/zaptest"

	appaccount "telesrv/internal/app/account"
	"telesrv/internal/domain"
	"telesrv/internal/postresponse"
	"telesrv/internal/store/memory"
)

func TestAccountDeleteRPCDeliversResultBeforeClosingCurrentSession(t *testing.T) {
	current := [8]byte{1}
	other := [8]byte{2}
	accountSvc := &rpcDeletionAccountService{
		Service: appaccount.NewService(memory.NewPasswordStore()),
		outcome: domain.AccountDeleteOutcome{
			Kind: domain.AccountDeleteImmediate,
			Deletion: domain.AccountDeletionResult{Changed: true, RevokedAuthorizations: []domain.Authorization{
				{AuthKeyID: current, UserID: 42},
				{AuthKeyID: other, UserID: 42},
			}},
		},
	}
	sessions := &deletionCaptureSessions{}
	r := New(Config{}, Deps{Account: accountSvc, Sessions: sessions}, zaptest.NewLogger(t), clock.System)
	ctx := postresponse.WithCallbacks(WithSessionID(WithAuthKeyID(WithUserID(context.Background(), 42), current), 77))
	ok, err := r.onAccountDeleteAccount(ctx, &tg.AccountDeleteAccountRequest{Reason: "manual"})
	if err != nil || !ok {
		t.Fatalf("delete account ok=%v err=%v", ok, err)
	}
	if sessions.wasClosed(current) {
		t.Fatal("current auth key closed before rpc_result delivery")
	}
	if !sessions.wasClosed(other) {
		t.Fatal("other auth key was not revoked immediately")
	}
	postresponse.Run(ctx)
	if !sessions.wasClosed(current) {
		t.Fatal("current auth key not closed after rpc_result delivery")
	}
}

func TestAccountDeleteRPCMapsDelayedTwoFAWait(t *testing.T) {
	accountSvc := &rpcDeletionAccountService{
		Service: appaccount.NewService(memory.NewPasswordStore()),
		outcome: domain.AccountDeleteOutcome{Kind: domain.AccountDeleteDelayed, WaitSeconds: 604800},
	}
	r := New(Config{}, Deps{Account: accountSvc}, zaptest.NewLogger(t), clock.System)
	ctx := WithAuthKeyID(WithUserID(context.Background(), 42), [8]byte{1})
	ok, err := r.onAccountDeleteAccount(ctx, &tg.AccountDeleteAccountRequest{Reason: "Forgot password"})
	if ok || !tgerr.Is(err, "2FA_CONFIRM_WAIT") || !strings.Contains(err.Error(), "604800") {
		t.Fatalf("delayed delete ok=%v err=%v", ok, err)
	}
}

func TestDeleteAccountAllowedWithoutFullAuthorization(t *testing.T) {
	if !rpcAllowedWithoutAuthorization(tg.AccountDeleteAccountRequestTypeID) {
		t.Fatal("account.deleteAccount must reach the narrow password_pending identity resolver")
	}
	if rpcAllowedWithoutAuthorization(tg.AccountConfirmPhoneRequestTypeID) || rpcAllowedWithoutAuthorization(tg.AccountSendConfirmPhoneCodeRequestTypeID) {
		t.Fatal("confirm-phone methods must remain fully authorized")
	}
}

func TestAccountDeletionNotificationCompletesForOfflineTarget(t *testing.T) {
	sessions := &offlineDeletionSessions{}
	svc := &deletionWorkerService{}
	r := New(Config{}, Deps{Sessions: sessions}, zaptest.NewLogger(t), clock.System)
	r.dispatchAccountDeletionNotification(context.Background(), svc, domain.AccountDeletionNotification{
		ID: 9, TargetUserID: 42, DeletedUserID: 77, Attempts: 1,
	})
	if len(svc.completed) != 1 || svc.completed[0] != 9 {
		t.Fatalf("completed notifications = %v, want [9]", svc.completed)
	}
}

func TestAccountLifecyclePartialSweepFinishesCommittedDeletion(t *testing.T) {
	revoked := [8]byte{3}
	svc := &rpcDeletionAccountService{
		Service: appaccount.NewService(memory.NewPasswordStore()),
		sweepResults: []domain.AccountDeletionResult{{
			Changed:               true,
			User:                  domain.User{ID: 42, Deleted: true},
			RevokedAuthorizations: []domain.Authorization{{AuthKeyID: revoked, UserID: 42}},
		}},
		sweepErr: errors.New("later candidate failed"),
	}
	sessions := &deletionCaptureSessions{}
	r := New(Config{}, Deps{Account: svc, Sessions: sessions}, zaptest.NewLogger(t), clock.System)
	r.runAccountLifecycleOnce(context.Background(), 10)
	if !sessions.wasClosed(revoked) {
		t.Fatal("committed deletion authorization was not closed after partial sweep failure")
	}
}

type rpcDeletionAccountService struct {
	*appaccount.Service
	outcome      domain.AccountDeleteOutcome
	err          error
	sweepResults []domain.AccountDeletionResult
	sweepErr     error
}

func (s *rpcDeletionAccountService) DeleteAccount(context.Context, int64, [8]byte, string, *domain.PasswordCheck, time.Time) (domain.AccountDeleteOutcome, error) {
	return s.outcome, s.err
}

func (*rpcDeletionAccountService) SendConfirmPhoneCode(context.Context, int64, [8]byte, int64, string) (string, domain.AuthCodeDelivery, error) {
	return "hash", domain.AuthCodeDelivery{Kind: domain.AuthCodeDeliverySMS, Length: 5}, nil
}

func (*rpcDeletionAccountService) ConfirmPhone(context.Context, int64, [8]byte, string, string, time.Time) ([]domain.Authorization, error) {
	return nil, nil
}

func (*rpcDeletionAccountService) ResendConfirmPhoneCode(context.Context, int64, [8]byte, int64, string, string) (string, domain.AuthCodeDelivery, bool, error) {
	return "", domain.AuthCodeDelivery{}, false, nil
}

func (*rpcDeletionAccountService) CancelConfirmPhoneCode(context.Context, int64, [8]byte, string, string) (bool, error) {
	return false, nil
}

func (s *rpcDeletionAccountService) SweepDueAccountDeletions(context.Context, time.Time, int) ([]domain.AccountDeletionResult, error) {
	return s.sweepResults, s.sweepErr
}

type deletionCaptureSessions struct {
	captureSessions
	closed [][8]byte
}

type offlineDeletionSessions struct{ captureSessions }

func (*offlineDeletionSessions) PushToUserExceptAuthKeySession(context.Context, int64, [8]byte, int64, proto.MessageType, tg.UpdatesClass) (int, error) {
	return 0, nil
}

type deletionWorkerService struct{ completed []int64 }

func (*deletionWorkerService) SweepDueAccountDeletions(context.Context, time.Time, int) ([]domain.AccountDeletionResult, error) {
	return nil, nil
}

func (*deletionWorkerService) ClaimAccountDeletionNotifications(context.Context, time.Time, int, time.Duration) ([]domain.AccountDeletionNotification, error) {
	return nil, nil
}

func (s *deletionWorkerService) CompleteAccountDeletionNotification(_ context.Context, id int64, _ time.Time) error {
	s.completed = append(s.completed, id)
	return nil
}

func (s *deletionCaptureSessions) CloseSessionsForBusinessAuthKey(id [8]byte) int {
	s.closed = append(s.closed, id)
	return 1
}

func (s *deletionCaptureSessions) wasClosed(id [8]byte) bool {
	for _, closed := range s.closed {
		if closed == id {
			return true
		}
	}
	return false
}
