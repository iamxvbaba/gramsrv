package admin

import (
	"context"
	"crypto/sha256"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"telesrv/internal/domain"
)

func TestSetAccountFrozenDryRunExecuteAndIdempotency(t *testing.T) {
	ctx := context.Background()
	repo := newMemoryCommandRepo()
	restrictions := &fakeRestrictionStore{}
	svc := NewService(Dependencies{
		Commands:     repo,
		Restrictions: restrictions,
		Now:          fixedNow,
	})

	dry, err := svc.SetAccountFrozen(ctx, SetAccountFrozenRequest{
		CommandMeta: CommandMeta{CommandID: "dry-freeze", Actor: "ops", Reason: "test", DryRun: true},
		UserID:      1001,
		Frozen:      true,
		Until:       fixedNow().Add(7 * 24 * time.Hour),
		AppealURL:   "https://appeals.example.test/account/1001",
	})
	if err != nil {
		t.Fatalf("dry-run freeze: %v", err)
	}
	if !dry.DryRun || dry.Status != string(domain.AdminCommandCompleted) || restrictions.setCalls != 0 {
		t.Fatalf("dry-run result=%+v setCalls=%d, want completed dry-run without mutation", dry, restrictions.setCalls)
	}

	execReq := SetAccountFrozenRequest{
		CommandMeta: CommandMeta{CommandID: "exec-freeze", Actor: "ops", Reason: "incident", DryRun: false},
		UserID:      1001,
		Frozen:      true,
		Until:       fixedNow().Add(7 * 24 * time.Hour),
		AppealURL:   "https://appeals.example.test/account/1001",
	}
	exec, err := svc.SetAccountFrozen(ctx, execReq)
	if err != nil {
		t.Fatalf("execute freeze: %v", err)
	}
	if exec.Status != string(domain.AdminCommandCompleted) || restrictions.setCalls != 1 {
		t.Fatalf("execute result=%+v setCalls=%d", exec, restrictions.setCalls)
	}
	if err := svc.CanSendMessages(ctx, 1001); !errors.Is(err, domain.ErrUserFrozen) {
		t.Fatalf("CanSendMessages err=%v, want ErrUserFrozen", err)
	}
	freeze, found, err := svc.AccountFreeze(ctx, 1001)
	if err != nil || !found || !freeze.Frozen || !freeze.Since.Equal(fixedNow()) || freeze.AppealURL != execReq.AppealURL {
		t.Fatalf("AccountFreeze = %+v found=%v err=%v", freeze, found, err)
	}

	again, err := svc.SetAccountFrozen(ctx, execReq)
	if err != nil {
		t.Fatalf("duplicate freeze: %v", err)
	}
	if !again.AlreadyExecuted || restrictions.setCalls != 1 {
		t.Fatalf("duplicate result=%+v setCalls=%d, want idempotent replay", again, restrictions.setCalls)
	}
}

func TestSetAccountFrozenRejectsIncompleteStateAndUnfreezeClearsOverlay(t *testing.T) {
	ctx := context.Background()
	restrictions := &fakeRestrictionStore{}
	svc := NewService(Dependencies{Commands: newMemoryCommandRepo(), Restrictions: restrictions, Now: fixedNow})
	for _, req := range []SetAccountFrozenRequest{
		{CommandMeta: CommandMeta{CommandID: "bad-until", Actor: "ops", Reason: "test"}, UserID: 1001, Frozen: true, Until: fixedNow(), AppealURL: "https://appeals.example.test"},
		{CommandMeta: CommandMeta{CommandID: "too-far", Actor: "ops", Reason: "test"}, UserID: 1001, Frozen: true, Until: time.Unix(1<<31, 0), AppealURL: "https://appeals.example.test"},
		{CommandMeta: CommandMeta{CommandID: "bad-url", Actor: "ops", Reason: "test"}, UserID: 1001, Frozen: true, Until: fixedNow().Add(time.Hour), AppealURL: "javascript:bad"},
		{CommandMeta: CommandMeta{CommandID: "long-url", Actor: "ops", Reason: "test"}, UserID: 1001, Frozen: true, Until: fixedNow().Add(time.Hour), AppealURL: "https://appeals.example.test/" + strings.Repeat("x", maxFreezeAppealURLLength)},
	} {
		if _, err := svc.SetAccountFrozen(ctx, req); err == nil {
			t.Fatalf("SetAccountFrozen(%+v) succeeded", req)
		}
	}
	freezeReq := SetAccountFrozenRequest{CommandMeta: CommandMeta{CommandID: "freeze", Actor: "ops", Reason: "test"}, UserID: 1001, Frozen: true, Until: fixedNow().Add(24 * time.Hour), AppealURL: "https://appeals.example.test"}
	if _, err := svc.SetAccountFrozen(ctx, freezeReq); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SetAccountFrozen(ctx, SetAccountFrozenRequest{CommandMeta: CommandMeta{CommandID: "unfreeze", Actor: "ops", Reason: "accepted"}, UserID: 1001}); err != nil {
		t.Fatal(err)
	}
	freeze, found, err := svc.AccountFreeze(ctx, 1001)
	if err != nil || !found || freeze.Frozen || !freeze.Since.IsZero() || !freeze.Until.IsZero() || freeze.AppealURL != "" {
		t.Fatalf("unfrozen state = %+v found=%v err=%v", freeze, found, err)
	}
}

func TestSetAccountFrozenUpdatePreservesOriginalSince(t *testing.T) {
	ctx := context.Background()
	now := fixedNow()
	restrictions := &fakeRestrictionStore{}
	svc := NewService(Dependencies{
		Commands:     newMemoryCommandRepo(),
		Restrictions: restrictions,
		Now:          func() time.Time { return now },
	})
	if _, err := svc.SetAccountFrozen(ctx, SetAccountFrozenRequest{
		CommandMeta: CommandMeta{CommandID: "freeze-initial", Actor: "ops", Reason: "review"},
		UserID:      1001,
		Frozen:      true,
		Until:       now.Add(24 * time.Hour),
		AppealURL:   "https://appeals.example.test/initial",
	}); err != nil {
		t.Fatal(err)
	}
	originalSince := now
	now = now.Add(2 * time.Hour)
	updatedUntil := now.Add(72 * time.Hour)
	if _, err := svc.SetAccountFrozen(ctx, SetAccountFrozenRequest{
		CommandMeta: CommandMeta{CommandID: "freeze-update", Actor: "ops", Reason: "extend review"},
		UserID:      1001,
		Frozen:      true,
		Until:       updatedUntil,
		AppealURL:   "https://appeals.example.test/updated",
	}); err != nil {
		t.Fatal(err)
	}
	freeze, found, err := svc.AccountFreeze(ctx, 1001)
	if err != nil || !found || !freeze.Since.Equal(originalSince) || !freeze.Until.Equal(updatedUntil) ||
		freeze.AppealURL != "https://appeals.example.test/updated" {
		t.Fatalf("updated freeze = %+v found=%v err=%v", freeze, found, err)
	}
}

func TestSetAccountFrozenReplayRemainsIdempotentAfterDeadline(t *testing.T) {
	ctx := context.Background()
	now := fixedNow()
	restrictions := &fakeRestrictionStore{}
	svc := NewService(Dependencies{
		Commands:     newMemoryCommandRepo(),
		Restrictions: restrictions,
		Now:          func() time.Time { return now },
	})
	req := SetAccountFrozenRequest{
		CommandMeta: CommandMeta{CommandID: "freeze-expiring", Actor: "ops", Reason: "review"},
		UserID:      1001,
		Frozen:      true,
		Until:       now.Add(time.Hour),
		AppealURL:   "https://appeals.example.test/expiring",
	}
	if _, err := svc.SetAccountFrozen(ctx, req); err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Hour)
	replayed, err := svc.SetAccountFrozen(ctx, req)
	if err != nil || !replayed.AlreadyExecuted || restrictions.setCalls != 1 {
		t.Fatalf("expired replay = %+v err=%v setCalls=%d", replayed, err, restrictions.setCalls)
	}
	stale := req
	stale.CommandID = "new-stale-freeze"
	if _, err := svc.SetAccountFrozen(ctx, stale); err == nil || restrictions.setCalls != 1 {
		t.Fatalf("new stale request err=%v setCalls=%d, want rejection without state mutation", err, restrictions.setCalls)
	}
}

func TestGrantPremiumDryRunExecuteAndIdempotency(t *testing.T) {
	ctx := context.Background()
	users := &fakeUsersService{users: map[int64]domain.User{
		1001: {ID: 1001, FirstName: "Alice"},
	}}
	notifier := &fakeUserNotifier{}
	svc := NewService(Dependencies{
		Commands:     newMemoryCommandRepo(),
		Users:        users,
		UserNotifier: notifier,
		Now:          fixedNow,
	})

	dry, err := svc.GrantPremium(ctx, GrantPremiumRequest{
		CommandMeta: CommandMeta{CommandID: "dry-premium", Actor: "ops", Reason: "test", DryRun: true},
		UserID:      1001,
		Months:      3,
	})
	if err != nil {
		t.Fatalf("dry-run premium: %v", err)
	}
	if !dry.DryRun || users.grantCalls != 0 || len(notifier.users) != 0 {
		t.Fatalf("dry=%+v grantCalls=%d notified=%v, want no mutation", dry, users.grantCalls, notifier.users)
	}

	req := GrantPremiumRequest{
		CommandMeta: CommandMeta{CommandID: "exec-premium", Actor: "ops", Reason: "grant"},
		UserID:      1001,
		Months:      2,
	}
	exec, err := svc.GrantPremium(ctx, req)
	if err != nil {
		t.Fatalf("execute premium: %v", err)
	}
	if exec.Status != string(domain.AdminCommandCompleted) || users.grantCalls != 1 || users.lastMonths != 2 || len(notifier.users) != 1 {
		t.Fatalf("exec=%+v grantCalls=%d months=%d notified=%v", exec, users.grantCalls, users.lastMonths, notifier.users)
	}
	again, err := svc.GrantPremium(ctx, req)
	if err != nil {
		t.Fatalf("duplicate premium: %v", err)
	}
	if !again.AlreadyExecuted || users.grantCalls != 1 || len(notifier.users) != 1 {
		t.Fatalf("again=%+v grantCalls=%d notified=%v, want idempotent replay", again, users.grantCalls, notifier.users)
	}
}

func TestGrantStarsDryRunExecuteAndIdempotency(t *testing.T) {
	ctx := context.Background()
	users := &fakeUsersService{users: map[int64]domain.User{
		1001: {ID: 1001, Phone: "1001", Username: "alice", FirstName: "Alice"},
	}}
	stars := &fakeStarsService{balances: map[int64]domain.StarsBalance{
		1001: {UserID: 1001, Balance: 1000, Granted: true},
	}}
	notifier := &fakeStarsNotifier{}
	svc := NewService(Dependencies{
		Commands:      newMemoryCommandRepo(),
		Users:         users,
		Stars:         stars,
		StarsNotifier: notifier,
		Now:           fixedNow,
	})

	dry, err := svc.GrantStars(ctx, GrantStarsRequest{
		CommandMeta: CommandMeta{CommandID: "dry-stars", Actor: "ops", Reason: "test", DryRun: true},
		UserID:      1001,
		Amount:      250,
	})
	if err != nil {
		t.Fatalf("dry-run stars: %v", err)
	}
	if !dry.DryRun || stars.creditCalls != 0 || len(notifier.balances) != 0 {
		t.Fatalf("dry=%+v creditCalls=%d notified=%v, want no mutation", dry, stars.creditCalls, notifier.balances)
	}

	req := GrantStarsRequest{
		CommandMeta: CommandMeta{CommandID: "exec-stars", Actor: "ops", Reason: "ops grant"},
		UserID:      1001,
		Amount:      250,
	}
	exec, err := svc.GrantStars(ctx, req)
	if err != nil {
		t.Fatalf("execute stars: %v", err)
	}
	if exec.Status != string(domain.AdminCommandCompleted) || stars.creditCalls != 1 || stars.lastAmount != 250 || stars.lastReason != domain.StarsReasonAdjust || len(notifier.balances) != 1 {
		t.Fatalf("exec=%+v creditCalls=%d amount=%d reason=%s notified=%v", exec, stars.creditCalls, stars.lastAmount, stars.lastReason, notifier.balances)
	}
	if exec.Details["updated_balance"] != int64(1250) {
		t.Fatalf("updated_balance=%v, want 1250", exec.Details["updated_balance"])
	}
	again, err := svc.GrantStars(ctx, req)
	if err != nil {
		t.Fatalf("duplicate stars: %v", err)
	}
	if !again.AlreadyExecuted || stars.creditCalls != 1 || len(notifier.balances) != 1 {
		t.Fatalf("again=%+v creditCalls=%d notified=%v, want idempotent replay", again, stars.creditCalls, notifier.balances)
	}
}

func TestSetVerifiedDryRunExecuteAndIdempotency(t *testing.T) {
	ctx := context.Background()
	users := &fakeUsersService{users: map[int64]domain.User{
		1001: {ID: 1001, FirstName: "Alice"},
	}}
	notifier := &fakeUserNotifier{}
	svc := NewService(Dependencies{
		Commands:     newMemoryCommandRepo(),
		Users:        users,
		UserNotifier: notifier,
		Now:          fixedNow,
	})

	dry, err := svc.SetVerified(ctx, SetVerifiedRequest{
		CommandMeta: CommandMeta{CommandID: "dry-verified", Actor: "ops", Reason: "test", DryRun: true},
		UserID:      1001,
		Verified:    true,
	})
	if err != nil {
		t.Fatalf("dry-run verified: %v", err)
	}
	if !dry.DryRun || users.verifiedCalls != 0 || len(notifier.users) != 0 {
		t.Fatalf("dry=%+v verifiedCalls=%d notified=%v, want no mutation", dry, users.verifiedCalls, notifier.users)
	}

	req := SetVerifiedRequest{
		CommandMeta: CommandMeta{CommandID: "exec-verified", Actor: "ops", Reason: "official"},
		UserID:      1001,
		Verified:    true,
	}
	exec, err := svc.SetVerified(ctx, req)
	if err != nil {
		t.Fatalf("execute verified: %v", err)
	}
	if exec.Status != string(domain.AdminCommandCompleted) || users.verifiedCalls != 1 || !users.users[1001].Verified || len(notifier.users) != 1 {
		t.Fatalf("exec=%+v verifiedCalls=%d user=%+v notified=%v", exec, users.verifiedCalls, users.users[1001], notifier.users)
	}
	again, err := svc.SetVerified(ctx, req)
	if err != nil {
		t.Fatalf("duplicate verified: %v", err)
	}
	if !again.AlreadyExecuted || users.verifiedCalls != 1 || len(notifier.users) != 1 {
		t.Fatalf("again=%+v verifiedCalls=%d notified=%v, want idempotent replay", again, users.verifiedCalls, notifier.users)
	}
}

func TestSetChannelVerifiedDryRunExecuteAndIdempotency(t *testing.T) {
	ctx := context.Background()
	channels := &fakeChannelsService{channels: map[int64]domain.Channel{
		2001: {ID: 2001, CreatorUserID: 1001, Title: "Ops Channel", Username: "ops", Broadcast: true},
	}}
	notifier := &fakeChannelNotifier{}
	svc := NewService(Dependencies{
		Commands:        newMemoryCommandRepo(),
		Channels:        channels,
		ChannelNotifier: notifier,
		Now:             fixedNow,
	})

	dry, err := svc.SetChannelVerified(ctx, SetChannelVerifiedRequest{
		CommandMeta: CommandMeta{CommandID: "dry-channel-verified", Actor: "ops", Reason: "test", DryRun: true},
		ChannelID:   2001,
		Verified:    true,
	})
	if err != nil {
		t.Fatalf("dry-run channel verified: %v", err)
	}
	if !dry.DryRun || channels.verifiedCalls != 0 || len(notifier.channels) != 0 {
		t.Fatalf("dry=%+v verifiedCalls=%d notified=%v, want no mutation", dry, channels.verifiedCalls, notifier.channels)
	}

	req := SetChannelVerifiedRequest{
		CommandMeta: CommandMeta{CommandID: "exec-channel-verified", Actor: "ops", Reason: "official"},
		ChannelID:   2001,
		Verified:    true,
	}
	exec, err := svc.SetChannelVerified(ctx, req)
	if err != nil {
		t.Fatalf("execute channel verified: %v", err)
	}
	if exec.Status != string(domain.AdminCommandCompleted) || channels.verifiedCalls != 1 || !channels.channels[2001].Verified || len(notifier.channels) != 1 {
		t.Fatalf("exec=%+v verifiedCalls=%d channel=%+v notified=%v", exec, channels.verifiedCalls, channels.channels[2001], notifier.channels)
	}
	if exec.TargetPeer.Type != domain.PeerTypeChannel || exec.TargetPeer.ID != 2001 || exec.TargetUserID != 0 {
		t.Fatalf("target user=%d peer=%+v, want channel target", exec.TargetUserID, exec.TargetPeer)
	}
	again, err := svc.SetChannelVerified(ctx, req)
	if err != nil {
		t.Fatalf("duplicate channel verified: %v", err)
	}
	if !again.AlreadyExecuted || channels.verifiedCalls != 1 || len(notifier.channels) != 1 {
		t.Fatalf("again=%+v verifiedCalls=%d notified=%v, want idempotent replay", again, channels.verifiedCalls, notifier.channels)
	}
}

func TestDeletePrivateMessagesUsesMessageServiceAndIdempotency(t *testing.T) {
	ctx := context.Background()
	repo := newMemoryCommandRepo()
	messages := &fakeMessagesService{
		byID: []domain.Message{
			{OwnerUserID: 1001, ID: 11, Peer: domain.Peer{Type: domain.PeerTypeUser, ID: 1002}},
			{OwnerUserID: 1001, ID: 12, Peer: domain.Peer{Type: domain.PeerTypeUser, ID: 1002}},
		},
	}
	svc := NewService(Dependencies{Commands: repo, Messages: messages, Now: fixedNow})
	req := DeletePrivateMessagesRequest{
		CommandMeta: CommandMeta{CommandID: "delete-1", Actor: "ops", Reason: "abuse"},
		OwnerUserID: 1001,
		Peer:        domain.Peer{Type: domain.PeerTypeUser, ID: 1002},
		IDs:         []int{12, 11},
		Revoke:      true,
	}

	if _, err := svc.DeletePrivateMessages(ctx, req); err != nil {
		t.Fatalf("delete messages: %v", err)
	}
	if messages.deleteCalls != 1 || !reflect.DeepEqual(messages.lastDelete.IDs, []int{11, 12}) || !messages.lastDelete.Revoke {
		t.Fatalf("delete calls=%d req=%+v", messages.deleteCalls, messages.lastDelete)
	}
	if _, err := svc.DeletePrivateMessages(ctx, req); err != nil {
		t.Fatalf("duplicate delete messages: %v", err)
	}
	if messages.deleteCalls != 1 {
		t.Fatalf("duplicate delete calls=%d, want 1", messages.deleteCalls)
	}
}

func TestDeletePrivateMessagesRejectsMissingOnExecute(t *testing.T) {
	ctx := context.Background()
	svc := NewService(Dependencies{Commands: newMemoryCommandRepo(), Messages: &fakeMessagesService{}, Now: fixedNow})
	_, err := svc.DeletePrivateMessages(ctx, DeletePrivateMessagesRequest{
		CommandMeta: CommandMeta{CommandID: "delete-missing", Actor: "ops", Reason: "test"},
		OwnerUserID: 1001,
		Peer:        domain.Peer{Type: domain.PeerTypeUser, ID: 1002},
		IDs:         []int{99},
	})
	if err == nil {
		t.Fatal("delete missing message err=nil, want error")
	}
}

func TestRevokeSessionsSpecifiedClosesRevokedAuthKey(t *testing.T) {
	ctx := context.Background()
	key := [8]byte{1, 2, 3}
	auth := &fakeAuthService{items: []domain.Authorization{
		{AuthKeyID: key, UserID: 1001, Hash: 555},
	}}
	revoker := &fakeRevoker{}
	svc := NewService(Dependencies{Commands: newMemoryCommandRepo(), Auth: auth, Revoker: revoker, Now: fixedNow})
	if _, err := svc.RevokeSessions(ctx, RevokeSessionsRequest{
		CommandMeta: CommandMeta{CommandID: "revoke-1", Actor: "ops", Reason: "lost device"},
		UserID:      1001,
		Hash:        555,
	}); err != nil {
		t.Fatalf("revoke sessions: %v", err)
	}
	if auth.resetHash != 555 || len(revoker.keys) != 1 || revoker.keys[0] != key {
		t.Fatalf("resetHash=%d revoked=%v", auth.resetHash, revoker.keys)
	}
}

func TestDeletePrivateHistoryLoopsUntilOffsetClears(t *testing.T) {
	ctx := context.Background()
	messages := &fakeMessagesService{historyOffsets: []int{1, 0}}
	svc := NewService(Dependencies{Commands: newMemoryCommandRepo(), Messages: messages, Now: fixedNow})
	res, err := svc.DeletePrivateHistory(ctx, DeletePrivateHistoryRequest{
		CommandMeta: CommandMeta{CommandID: "history-1", Actor: "ops", Reason: "clear"},
		OwnerUserID: 1001,
		Peer:        domain.Peer{Type: domain.PeerTypeUser, ID: 1002},
		MaxBatches:  5,
	})
	if err != nil {
		t.Fatalf("delete history: %v", err)
	}
	if messages.historyCalls != 2 || res.Details["has_more"] != false {
		t.Fatalf("historyCalls=%d result=%+v", messages.historyCalls, res)
	}
}

func fixedNow() time.Time {
	return time.Unix(1_700_000_000, 0).UTC()
}

type memoryCommandRepo struct {
	items map[string]domain.AdminCommand
}

func newMemoryCommandRepo() *memoryCommandRepo {
	return &memoryCommandRepo{items: map[string]domain.AdminCommand{}}
}

func (m *memoryCommandRepo) BeginCommand(_ context.Context, cmd domain.AdminCommand) (domain.AdminCommand, bool, error) {
	if existing, ok := m.items[cmd.CommandID]; ok {
		return existing, false, nil
	}
	m.items[cmd.CommandID] = cmd
	return cmd, true, nil
}

func (m *memoryCommandRepo) FinishCommand(_ context.Context, commandID string, status domain.AdminCommandStatus, resultJSON []byte, errorText string) (domain.AdminCommand, error) {
	cmd := m.items[commandID]
	cmd.Status = status
	cmd.ResultJSON = resultJSON
	cmd.Error = errorText
	m.items[commandID] = cmd
	return cmd, nil
}

type fakeRestrictionStore struct {
	items    map[int64]domain.AccountFreeze
	setCalls int
}

func (f *fakeRestrictionStore) GetAccountFreeze(_ context.Context, userID int64) (domain.AccountFreeze, bool, error) {
	if f.items == nil {
		return domain.AccountFreeze{}, false, nil
	}
	r, ok := f.items[userID]
	return r, ok, nil
}

func (f *fakeRestrictionStore) SetAccountFreeze(_ context.Context, r domain.AccountFreeze) (domain.AccountFreeze, error) {
	if f.items == nil {
		f.items = map[int64]domain.AccountFreeze{}
	}
	f.setCalls++
	r.UpdatedAt = fixedNow()
	f.items[r.UserID] = r
	return r, nil
}

type fakeMessagesService struct {
	byID           []domain.Message
	deleteCalls    int
	lastDelete     domain.DeleteMessagesRequest
	historyCalls   int
	historyOffsets []int
}

func (f *fakeMessagesService) GetMessages(_ context.Context, _ int64, _ []int) (domain.MessageList, error) {
	return domain.MessageList{Messages: f.byID}, nil
}

func (f *fakeMessagesService) GetHistory(_ context.Context, _ int64, _ domain.MessageFilter) (domain.MessageList, error) {
	return domain.MessageList{Messages: []domain.Message{{ID: 1}}}, nil
}

func (f *fakeMessagesService) DeleteMessages(_ context.Context, userID int64, req domain.DeleteMessagesRequest) (domain.DeleteMessagesResult, error) {
	f.deleteCalls++
	f.lastDelete = req
	return domain.DeleteMessagesResult{
		OwnerUserID: userID,
		Deleted: []domain.DeletedMessagesForUser{{
			UserID:     userID,
			MessageIDs: req.IDs,
			Event:      domain.UpdateEvent{Pts: 10, PtsCount: len(req.IDs)},
		}},
	}, nil
}

func (f *fakeMessagesService) DeleteHistory(_ context.Context, userID int64, _ domain.DeleteHistoryRequest) (domain.DeleteMessagesResult, error) {
	offset := 0
	if f.historyCalls < len(f.historyOffsets) {
		offset = f.historyOffsets[f.historyCalls]
	}
	f.historyCalls++
	return domain.DeleteMessagesResult{
		OwnerUserID: userID,
		Deleted: []domain.DeletedMessagesForUser{{
			UserID:     userID,
			MessageIDs: []int{f.historyCalls},
			Event:      domain.UpdateEvent{Pts: f.historyCalls, PtsCount: 1},
		}},
		Offset: offset,
	}, nil
}

type fakeAuthService struct {
	items     []domain.Authorization
	resetHash int64
}

func (f *fakeAuthService) ListAuthorizations(context.Context, int64) ([]domain.Authorization, error) {
	return f.items, nil
}

func (f *fakeAuthService) ResetAuthorization(_ context.Context, _ int64, hash int64) (domain.Authorization, bool, error) {
	f.resetHash = hash
	for _, a := range f.items {
		if a.Hash == hash {
			return a, true, nil
		}
	}
	return domain.Authorization{}, false, nil
}

func (f *fakeAuthService) ResetAuthorizations(_ context.Context, _ int64, keep [8]byte) ([]domain.Authorization, error) {
	out := make([]domain.Authorization, 0)
	for _, a := range f.items {
		if a.AuthKeyID != keep {
			out = append(out, a)
		}
	}
	return out, nil
}

type fakeRevoker struct {
	keys [][8]byte
}

func (f *fakeRevoker) RevokeAuthorizationAuthKey(_ context.Context, key [8]byte, _ int64) error {
	f.keys = append(f.keys, key)
	return nil
}

type fakeUsersService struct {
	users         map[int64]domain.User
	grantCalls    int
	lastMonths    int
	verifiedCalls int
}

func (f *fakeUsersService) AdminUser(_ context.Context, userID int64) (domain.User, bool, error) {
	u, ok := f.users[userID]
	return u, ok, nil
}

func (f *fakeUsersService) GrantPremium(_ context.Context, userID int64, months int) (domain.User, error) {
	f.grantCalls++
	f.lastMonths = months
	u, ok := f.users[userID]
	if !ok {
		return domain.User{}, domain.ErrUserNotFound
	}
	if u.Bot {
		return domain.User{}, domain.ErrPremiumBotUnsupported
	}
	if months <= 0 {
		u.PremiumUntil = 0
	} else {
		u.PremiumUntil = int(fixedNow().AddDate(0, months, 0).Unix())
	}
	f.users[userID] = u
	return u, nil
}

func (f *fakeUsersService) SetVerified(_ context.Context, userID int64, verified bool) (domain.User, error) {
	f.verifiedCalls++
	u, ok := f.users[userID]
	if !ok {
		return domain.User{}, domain.ErrUserNotFound
	}
	u.Verified = verified
	f.users[userID] = u
	return u, nil
}

type fakeStarsService struct {
	balances    map[int64]domain.StarsBalance
	creditCalls int
	lastUserID  int64
	lastAmount  int64
	lastReason  domain.StarsTransactionReason
	lastPeer    domain.Peer
	lastTitle   string
	lastDesc    string
}

func (f *fakeStarsService) Credit(_ context.Context, userID, amount int64, reason domain.StarsTransactionReason, peer domain.Peer, title, desc string) (domain.StarsBalance, error) {
	f.creditCalls++
	f.lastUserID = userID
	f.lastAmount = amount
	f.lastReason = reason
	f.lastPeer = peer
	f.lastTitle = title
	f.lastDesc = desc
	if amount <= 0 {
		return domain.StarsBalance{}, domain.ErrStarsInvalidAmount
	}
	if f.balances == nil {
		f.balances = map[int64]domain.StarsBalance{}
	}
	balance := f.balances[userID]
	balance.UserID = userID
	balance.Balance += amount
	f.balances[userID] = balance
	return balance, nil
}

type fakeStarsNotifier struct {
	balances []domain.StarsBalance
}

func (f *fakeStarsNotifier) NotifyStarsBalanceChanged(_ context.Context, balance domain.StarsBalance) error {
	f.balances = append(f.balances, balance)
	return nil
}

type fakeUserNotifier struct {
	users []int64
}

func (f *fakeUserNotifier) NotifyUserChanged(_ context.Context, u domain.User) error {
	f.users = append(f.users, u.ID)
	return nil
}

type fakeChannelsService struct {
	channels      map[int64]domain.Channel
	verifiedCalls int
}

func (f *fakeChannelsService) GetChannelByID(_ context.Context, channelID int64) (domain.Channel, error) {
	ch, ok := f.channels[channelID]
	if !ok {
		return domain.Channel{}, domain.ErrChannelInvalid
	}
	return ch, nil
}

func (f *fakeChannelsService) SetVerified(_ context.Context, channelID int64, verified bool) (domain.Channel, error) {
	f.verifiedCalls++
	ch, ok := f.channels[channelID]
	if !ok {
		return domain.Channel{}, domain.ErrChannelInvalid
	}
	ch.Verified = verified
	f.channels[channelID] = ch
	return ch, nil
}

type fakeChannelNotifier struct {
	channels []int64
}

func TestImportStarGiftDryRunThenConfirm(t *testing.T) {
	gifts := &fakeGiftsService{}
	svc := NewService(Dependencies{Commands: newMemoryCommandRepo(), Gifts: gifts, Now: fixedNow})
	base := ImportStarGiftRequest{
		Title: "Cake", Stars: 50, ConvertStars: 25, Enabled: true, SortOrder: 3,
		FileName: "cake.lottie", Data: []byte(`{"v":"5.7"}`),
	}
	base.CommandMeta = CommandMeta{CommandID: "dry-gift", Actor: "ops", Reason: "catalog", DryRun: true}
	preview, err := svc.ImportStarGift(context.Background(), base)
	if err != nil || gifts.createCalls != 0 || preview.Details["source_format"] != domain.StarGiftAnimationLottie {
		t.Fatalf("preview=%+v err=%v create=%d", preview, err, gifts.createCalls)
	}
	base.CommandMeta = CommandMeta{CommandID: "exec-gift", Actor: "ops", Reason: "catalog", DryRun: false}
	result, err := svc.ImportStarGift(context.Background(), base)
	if err != nil || gifts.createCalls != 1 || result.Details["revision_id"] != int64(22) {
		t.Fatalf("result=%+v err=%v create=%d", result, err, gifts.createCalls)
	}
}

func TestCommandIDConflictRejectsDifferentGiftBytes(t *testing.T) {
	gifts := &fakeGiftsService{}
	svc := NewService(Dependencies{Commands: newMemoryCommandRepo(), Gifts: gifts, Now: fixedNow})
	req := ImportStarGiftRequest{
		CommandMeta: CommandMeta{CommandID: "same", Actor: "ops", Reason: "catalog", DryRun: true},
		Title:       "Gift", Stars: 10, ConvertStars: 5, Enabled: true, FileName: "a.lottie", Data: []byte("one"),
	}
	if _, err := svc.ImportStarGift(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	req.Data = []byte("two")
	if _, err := svc.ImportStarGift(context.Background(), req); err == nil || err.Error() != "COMMAND_ID_CONFLICT" {
		t.Fatalf("conflict err=%v", err)
	}
}

func TestPublishStarGiftCollectiblesDryRunThenConfirm(t *testing.T) {
	gifts := &fakeGiftsService{}
	svc := NewService(Dependencies{Commands: newMemoryCommandRepo(), Gifts: gifts, Now: fixedNow})
	base := PublishStarGiftCollectiblesRequest{
		GiftID: 11, UpgradeStars: 125, SupplyTotal: 100, SlugPrefix: "cake",
		Models:    []StarGiftCollectibleAnimationUpload{{Name: "Ruby", RarityPermille: 1000, FileKey: "model-0", FileName: "ruby.lottie", Data: []byte("model")}},
		Patterns:  []StarGiftCollectibleAnimationUpload{{Name: "Stars", RarityPermille: 1000, FileKey: "pattern-0", FileName: "stars.tgs", Data: []byte("pattern")}},
		Backdrops: []StarGiftCollectibleBackdropInput{{Name: "Night", BackdropID: 1, CenterColor: 0x112233, EdgeColor: 0x223344, PatternColor: 0x334455, TextColor: 0xffffff, RarityPermille: 1000}},
	}
	base.CommandMeta = CommandMeta{CommandID: "dry-collectibles", Actor: "ops", Reason: "pool", DryRun: true}
	preview, err := svc.PublishStarGiftCollectibles(context.Background(), base)
	if err != nil || gifts.createCalls != 0 || preview.Details["models"] == nil {
		t.Fatalf("preview=%+v err=%v create=%d", preview, err, gifts.createCalls)
	}
	base.CommandMeta = CommandMeta{CommandID: "exec-collectibles", Actor: "ops", Reason: "pool", DryRun: false}
	result, err := svc.PublishStarGiftCollectibles(context.Background(), base)
	if err != nil || gifts.createCalls != 1 || result.Details["revision_id"] != int64(33) || result.Details["published"] != true {
		t.Fatalf("result=%+v err=%v create=%d", result, err, gifts.createCalls)
	}
}

type fakeGiftsService struct{ createCalls int }

func (f *fakeGiftsService) PrepareAnimation(name string, data []byte) (domain.StarGiftAnimation, error) {
	sum := sha256.Sum256(data)
	return domain.StarGiftAnimation{
		SourceName: name, SourceFormat: domain.StarGiftAnimationLottie,
		JSON: []byte(`{"v":"5.7"}`), TGS: []byte("tgs"), SHA256: sum[:], Width: 512, Height: 512, FrameRate: 30,
	}, nil
}
func (f *fakeGiftsService) CreateCatalogRevision(_ context.Context, write domain.StarGiftCatalogWrite) (domain.StarGiftCatalogEntry, error) {
	f.createCalls++
	return domain.StarGiftCatalogEntry{Gift: domain.StarGift{ID: 11, RevisionID: 22, Stars: write.Stars}, Revision: 1}, nil
}
func (*fakeGiftsService) SetCatalogEnabled(context.Context, int64, bool) (bool, error) {
	return true, nil
}
func (*fakeGiftsService) SetCatalogSortOrder(context.Context, int64, int) (bool, error) {
	return true, nil
}
func (*fakeGiftsService) AnimationJSON(context.Context, int64) ([]byte, bool, error) {
	return []byte(`{"v":"5.7"}`), true, nil
}
func (f *fakeGiftsService) CreateCollectibleRevision(_ context.Context, write domain.StarGiftCollectibleWrite) (domain.StarGiftCollectibleRevision, error) {
	f.createCalls++
	return domain.StarGiftCollectibleRevision{ID: 33, GiftID: write.GiftID, Revision: 2, Published: true}, nil
}
func (*fakeGiftsService) CollectiblePreview(context.Context, int64) (domain.StarGiftUpgradePreview, bool, error) {
	return domain.StarGiftUpgradePreview{}, false, nil
}
func (*fakeGiftsService) CollectibleAnimationJSON(context.Context, int64, domain.StarGiftCollectibleAttributeKind, int64) ([]byte, bool, error) {
	return []byte(`{"v":"5.7"}`), true, nil
}

func (f *fakeChannelNotifier) NotifyChannelChanged(_ context.Context, ch domain.Channel) error {
	f.channels = append(f.channels, ch.ID)
	return nil
}
