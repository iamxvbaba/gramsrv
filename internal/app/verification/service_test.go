package verification

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

// ---------------------------------------------------------------------------
// Fakes
// ---------------------------------------------------------------------------

func targetKey(targetType domain.VerificationTargetType, targetID int64) string {
	return fmt.Sprintf("%s:%d", targetType, targetID)
}

type fakeStore struct {
	apps       map[int64]domain.VerificationApplication
	events     map[int64][]domain.VerificationApplicationEvent
	rejections map[string]domain.VerificationApplication
	pending    []store.VerificationNotification
	nextID     int64

	decideCalls    int
	revokeCalls    int
	deliveredIDs   []int64
	failedIDs      []int64
	failedReasons  []string
	createRequests []domain.SubmitVerificationApplicationRequest
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		apps:       map[int64]domain.VerificationApplication{},
		events:     map[int64][]domain.VerificationApplicationEvent{},
		rejections: map[string]domain.VerificationApplication{},
	}
}

func (f *fakeStore) put(app domain.VerificationApplication) domain.VerificationApplication {
	if app.ID == 0 {
		f.nextID++
		app.ID = f.nextID
	}
	if app.ID > f.nextID {
		f.nextID = app.ID
	}
	if app.Version == 0 {
		app.Version = 1
	}
	f.apps[app.ID] = app
	return app
}

func (f *fakeStore) CreateVerificationDraft(_ context.Context, req domain.SubmitVerificationApplicationRequest) (domain.VerificationApplication, bool, error) {
	f.createRequests = append(f.createRequests, req)
	for _, app := range f.apps {
		if app.Status.Active() && app.TargetType == req.TargetType && app.TargetID == req.TargetID {
			return domain.VerificationApplication{}, false, domain.ErrVerificationApplicationExists
		}
		if app.Status == domain.VerificationStatusDraft && app.ApplicantUserID == req.ApplicantUserID {
			return app, false, nil
		}
	}
	app := f.put(domain.VerificationApplication{
		ApplicantUserID: req.ApplicantUserID,
		TargetType:      req.TargetType,
		TargetID:        req.TargetID,
		TargetTitle:     req.TargetTitle,
		TargetUsername:  req.TargetUsername,
		Category:        req.Draft.Category,
		Description:     req.Draft.Description,
		OfficialWebsite: req.Draft.OfficialWebsite,
		SocialLinks:     req.Draft.SocialLinks,
		PressLinks:      req.Draft.PressLinks,
		AdditionalNote:  req.Draft.AdditionalNote,
		Status:          domain.VerificationStatusDraft,
		CorrelationID:   req.CorrelationID,
		Version:         1,
	})
	return app, true, nil
}

func (f *fakeStore) SaveVerificationDraft(_ context.Context, applicationID, version int64, draft domain.VerificationDraftInput) (domain.VerificationApplication, error) {
	app, ok := f.apps[applicationID]
	if !ok {
		return domain.VerificationApplication{}, domain.ErrVerificationApplicationNotFound
	}
	if app.Version != version {
		return domain.VerificationApplication{}, domain.ErrVerificationVersionConflict
	}
	app.Category = draft.Category
	app.Description = draft.Description
	app.OfficialWebsite = draft.OfficialWebsite
	app.SocialLinks = draft.SocialLinks
	app.PressLinks = draft.PressLinks
	app.AdditionalNote = draft.AdditionalNote
	app.Version++
	f.apps[applicationID] = app
	return app, nil
}

func (f *fakeStore) SubmitVerificationApplication(_ context.Context, applicationID, version int64) (domain.VerificationApplication, error) {
	app, ok := f.apps[applicationID]
	if !ok {
		return domain.VerificationApplication{}, domain.ErrVerificationApplicationNotFound
	}
	if app.Version != version {
		return domain.VerificationApplication{}, domain.ErrVerificationVersionConflict
	}
	app.Status = domain.VerificationStatusSubmitted
	app.Version++
	f.apps[applicationID] = app
	return app, nil
}

func (f *fakeStore) CancelVerificationApplication(_ context.Context, applicationID, version int64, reason string) (domain.VerificationApplication, error) {
	app, ok := f.apps[applicationID]
	if !ok {
		return domain.VerificationApplication{}, domain.ErrVerificationApplicationNotFound
	}
	if app.Version != version {
		return domain.VerificationApplication{}, domain.ErrVerificationVersionConflict
	}
	app.Status = domain.VerificationStatusCancelled
	app.DecisionReason = reason
	app.Version++
	f.apps[applicationID] = app
	return app, nil
}

func (f *fakeStore) ClaimVerificationApplication(_ context.Context, decision domain.VerificationDecision) (domain.VerificationApplication, error) {
	app, ok := f.apps[decision.ApplicationID]
	if !ok {
		return domain.VerificationApplication{}, domain.ErrVerificationApplicationNotFound
	}
	if app.Version != decision.Version {
		return domain.VerificationApplication{}, domain.ErrVerificationVersionConflict
	}
	if !domain.CanTransitionVerificationStatus(app.Status, domain.VerificationStatusInReview) {
		return domain.VerificationApplication{}, domain.ErrVerificationStatusInvalid
	}
	app.Status = domain.VerificationStatusInReview
	app.ReviewerAdminID = decision.Reviewer
	app.Version++
	f.apps[decision.ApplicationID] = app
	return app, nil
}

func (f *fakeStore) DecideVerificationApplication(ctx context.Context, decision domain.VerificationDecision, approve bool, applyVerified func(ctx context.Context, app domain.VerificationApplication) error) (domain.VerificationApplication, bool, error) {
	f.decideCalls++
	app, ok := f.apps[decision.ApplicationID]
	if !ok {
		return domain.VerificationApplication{}, false, domain.ErrVerificationApplicationNotFound
	}
	target := domain.VerificationStatusRejected
	if approve {
		target = domain.VerificationStatusApproved
	}
	if app.Status == target {
		return app, false, nil
	}
	if app.Version != decision.Version {
		return domain.VerificationApplication{}, false, domain.ErrVerificationVersionConflict
	}
	next := app
	next.Status = target
	next.ReviewerAdminID = decision.Reviewer
	next.DecisionReason = decision.Reason
	next.InternalNote = decision.InternalNote
	next.ReviewedAt = time.Now().UTC()
	next.Version++
	if approve && applyVerified != nil {
		if err := applyVerified(ctx, next); err != nil {
			return domain.VerificationApplication{}, false, err
		}
	}
	f.apps[decision.ApplicationID] = next
	kind := NoticeKindRejected
	if approve {
		kind = NoticeKindApproved
	} else {
		f.rejections[targetKey(next.TargetType, next.TargetID)] = next
	}
	f.pending = append(f.pending, store.VerificationNotification{
		ID:              int64(len(f.pending) + 1),
		ApplicationID:   next.ID,
		RecipientUserID: next.ApplicantUserID,
		Kind:            kind,
		Application:     next,
	})
	return next, true, nil
}

func (f *fakeStore) RevokeVerification(ctx context.Context, req domain.VerificationRevocation, clearVerified func(ctx context.Context, target domain.Peer) error) (domain.VerificationApplication, bool, error) {
	f.revokeCalls++
	var found domain.VerificationApplication
	for _, app := range f.apps {
		if app.Status == domain.VerificationStatusApproved && app.TargetType == req.TargetType && app.TargetID == req.TargetID {
			if app.ID > found.ID {
				found = app
			}
		}
	}
	if found.ID == 0 {
		return domain.VerificationApplication{}, false, domain.ErrVerificationApplicationNotFound
	}
	if clearVerified != nil {
		if err := clearVerified(ctx, found.Target()); err != nil {
			return domain.VerificationApplication{}, false, err
		}
	}
	f.pending = append(f.pending, store.VerificationNotification{
		ID:              int64(len(f.pending) + 1),
		ApplicationID:   found.ID,
		RecipientUserID: found.ApplicantUserID,
		Kind:            NoticeKindRevoked,
		Application:     found,
	})
	return found, true, nil
}

func (f *fakeStore) VerificationApplication(_ context.Context, applicationID int64) (domain.VerificationApplication, error) {
	app, ok := f.apps[applicationID]
	if !ok {
		return domain.VerificationApplication{}, domain.ErrVerificationApplicationNotFound
	}
	return app, nil
}

func (f *fakeStore) ActiveVerificationApplicationForTarget(_ context.Context, targetType domain.VerificationTargetType, targetID int64) (domain.VerificationApplication, error) {
	for _, app := range f.apps {
		if app.Status.Active() && app.TargetType == targetType && app.TargetID == targetID {
			return app, nil
		}
	}
	return domain.VerificationApplication{}, domain.ErrVerificationApplicationNotFound
}

func (f *fakeStore) VerificationDraftForApplicant(_ context.Context, applicantUserID int64) (domain.VerificationApplication, error) {
	for _, app := range f.apps {
		if app.Status == domain.VerificationStatusDraft && app.ApplicantUserID == applicantUserID {
			return app, nil
		}
	}
	return domain.VerificationApplication{}, domain.ErrVerificationApplicationNotFound
}

func (f *fakeStore) ListVerificationApplications(_ context.Context, filter domain.VerificationApplicationFilter) ([]domain.VerificationApplication, error) {
	out := make([]domain.VerificationApplication, 0, len(f.apps))
	for _, app := range f.apps {
		out = append(out, app)
	}
	if filter.Limit > 0 && len(out) > filter.Limit {
		out = out[:filter.Limit]
	}
	return out, nil
}

func (f *fakeStore) VerificationApplicationsForApplicant(_ context.Context, applicantUserID int64, limit int) ([]domain.VerificationApplication, error) {
	out := make([]domain.VerificationApplication, 0, len(f.apps))
	for _, app := range f.apps {
		if app.ApplicantUserID == applicantUserID {
			out = append(out, app)
		}
	}
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (f *fakeStore) VerificationStatusCounts(_ context.Context) (domain.VerificationStatusCounts, error) {
	counts := domain.VerificationStatusCounts{}
	for _, app := range f.apps {
		counts[app.Status]++
	}
	return counts, nil
}

func (f *fakeStore) VerificationApplicationEvents(_ context.Context, applicationID int64, limit int) ([]domain.VerificationApplicationEvent, error) {
	events := f.events[applicationID]
	if limit > 0 && len(events) > limit {
		events = events[:limit]
	}
	return events, nil
}

func (f *fakeStore) LastVerificationRejection(_ context.Context, _ int64, targetType domain.VerificationTargetType, targetID int64) (domain.VerificationApplication, error) {
	app, ok := f.rejections[targetKey(targetType, targetID)]
	if !ok {
		return domain.VerificationApplication{}, domain.ErrVerificationApplicationNotFound
	}
	return app, nil
}

func (f *fakeStore) PendingVerificationNotifications(_ context.Context, limit int) ([]store.VerificationNotification, error) {
	out := append([]store.VerificationNotification(nil), f.pending...)
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (f *fakeStore) MarkVerificationNotificationDelivered(_ context.Context, id int64) error {
	f.deliveredIDs = append(f.deliveredIDs, id)
	kept := f.pending[:0]
	for _, item := range f.pending {
		if item.ID != id {
			kept = append(kept, item)
		}
	}
	f.pending = kept
	return nil
}

func (f *fakeStore) MarkVerificationNotificationFailed(_ context.Context, id int64, reason string) error {
	f.failedIDs = append(f.failedIDs, id)
	f.failedReasons = append(f.failedReasons, reason)
	for i := range f.pending {
		if f.pending[i].ID == id {
			f.pending[i].Attempts++
		}
	}
	return nil
}

var _ store.VerificationStore = (*fakeStore)(nil)

type fakeDirectory struct {
	users           map[int64]domain.User
	channels        map[int64]domain.Channel
	ownedBots       map[int64][]int64
	adminedChannels map[int64][]int64
}

func (d *fakeDirectory) AdminUser(_ context.Context, userID int64) (domain.User, bool, error) {
	user, ok := d.users[userID]
	return user, ok, nil
}

func (d *fakeDirectory) ListOwnedBots(_ context.Context, ownerUserID int64) ([]domain.User, error) {
	out := make([]domain.User, 0, len(d.ownedBots[ownerUserID]))
	for _, botID := range d.ownedBots[ownerUserID] {
		if user, ok := d.users[botID]; ok {
			out = append(out, user)
		}
	}
	return out, nil
}

func (d *fakeDirectory) OwnsBot(_ context.Context, ownerUserID, botUserID int64) (bool, error) {
	for _, id := range d.ownedBots[ownerUserID] {
		if id == botUserID {
			return true, nil
		}
	}
	return false, nil
}

func (d *fakeDirectory) GetChannelByID(_ context.Context, channelID int64) (domain.Channel, error) {
	channel, ok := d.channels[channelID]
	if !ok {
		return domain.Channel{}, domain.ErrChannelInvalid
	}
	return channel, nil
}

func (d *fakeDirectory) ListAdminedPublicChannels(_ context.Context, userID int64) ([]domain.Channel, error) {
	out := make([]domain.Channel, 0, len(d.adminedChannels[userID]))
	for _, channelID := range d.adminedChannels[userID] {
		if channel, ok := d.channels[channelID]; ok && channel.Username != "" && !channel.Deleted {
			out = append(out, channel)
		}
	}
	return out, nil
}

var _ PeerDirectory = (*fakeDirectory)(nil)

type fakeVerifier struct {
	userCalls    []int64
	channelCalls []int64
	values       []bool
	err          error
}

func (v *fakeVerifier) SetUserVerified(_ context.Context, userID int64, verified bool) error {
	if v.err != nil {
		return v.err
	}
	v.userCalls = append(v.userCalls, userID)
	v.values = append(v.values, verified)
	return nil
}

func (v *fakeVerifier) SetChannelVerified(_ context.Context, channelID int64, verified bool) error {
	if v.err != nil {
		return v.err
	}
	v.channelCalls = append(v.channelCalls, channelID)
	v.values = append(v.values, verified)
	return nil
}

func (v *fakeVerifier) calls() int { return len(v.userCalls) + len(v.channelCalls) }

type fakePeerNotifier struct {
	peers []domain.Peer
	err   error
}

func (n *fakePeerNotifier) NotifyPeerVerified(_ context.Context, peer domain.Peer) error {
	n.peers = append(n.peers, peer)
	return n.err
}

type sentNotice struct {
	recipient int64
	kind      string
	appID     int64
}

type fakeApplicantNotifier struct {
	sent []sentNotice
	err  error
}

func (n *fakeApplicantNotifier) SendVerificationNotice(_ context.Context, recipientUserID int64, app domain.VerificationApplication, kind string) error {
	if n.err != nil {
		return n.err
	}
	n.sent = append(n.sent, sentNotice{recipient: recipientUserID, kind: kind, appID: app.ID})
	return nil
}

// fakeLimiter allows `budget` calls and refuses everything after that.
type fakeLimiter struct {
	budget int
	calls  int
	err    error
}

func (l *fakeLimiter) Allow(ctx context.Context, key string, limit int, window time.Duration) (bool, int, error) {
	return l.AllowN(ctx, key, 1, limit, window)
}

func (l *fakeLimiter) AllowN(_ context.Context, _ string, cost, _ int, _ time.Duration) (bool, int, error) {
	if l.err != nil {
		return false, 0, l.err
	}
	l.calls += cost
	if l.calls > l.budget {
		return false, 60, nil
	}
	return true, 0, nil
}

type fakeFreezes struct {
	frozen map[int64]bool
}

func (f *fakeFreezes) AccountFreeze(_ context.Context, userID int64) (domain.AccountFreeze, bool, error) {
	frozen, ok := f.frozen[userID]
	if !ok {
		return domain.AccountFreeze{}, false, nil
	}
	return domain.AccountFreeze{UserID: userID, Frozen: frozen}, true, nil
}

// ---------------------------------------------------------------------------
// Fixture
// ---------------------------------------------------------------------------

const (
	applicantID = int64(1000)
	strangerID  = int64(1001)
	botID       = int64(5000)
	channelID   = int64(7000)
	groupID     = int64(7001)
)

type fixture struct {
	svc      *Service
	st       *fakeStore
	dir      *fakeDirectory
	verifier *fakeVerifier
	peers    *fakePeerNotifier
	notifier *fakeApplicantNotifier
	limiter  *fakeLimiter
	freezes  *fakeFreezes
	now      time.Time
}

func newFixture(t *testing.T, opts ...Option) *fixture {
	t.Helper()
	fx := &fixture{
		st: newFakeStore(),
		dir: &fakeDirectory{
			users: map[int64]domain.User{
				applicantID:            {ID: applicantID, AccessHash: 11, FirstName: "App", LastName: "Licant", Username: "applicant"},
				strangerID:             {ID: strangerID, FirstName: "Stranger", Username: "stranger"},
				botID:                  {ID: botID, AccessHash: 22, FirstName: "My Bot", Username: "mybot", Bot: true, BotInfoVersion: 1},
				domain.VerifyBotUserID: domain.VerifyBotUser(),
			},
			channels: map[int64]domain.Channel{
				channelID: {ID: channelID, AccessHash: 33, Title: "My Channel", Username: "mychannel", Broadcast: true, CreatorUserID: applicantID},
				groupID:   {ID: groupID, AccessHash: 34, Title: "My Group", Username: "mygroup", Megagroup: true, CreatorUserID: applicantID},
			},
			ownedBots:       map[int64][]int64{applicantID: {botID}},
			adminedChannels: map[int64][]int64{applicantID: {channelID, groupID}},
		},
		verifier: &fakeVerifier{},
		peers:    &fakePeerNotifier{},
		notifier: &fakeApplicantNotifier{},
		limiter:  &fakeLimiter{budget: 100},
		freezes:  &fakeFreezes{frozen: map[int64]bool{}},
		now:      time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC),
	}
	base := []Option{
		WithStore(fx.st),
		WithPeerDirectory(fx.dir),
		WithAccountFreezeProvider(fx.freezes),
		WithPeerVerifier(fx.verifier),
		WithPeerNotifier(fx.peers),
		WithApplicantNotifier(fx.notifier),
		WithRateLimiter(fx.limiter, 3, 24*time.Hour),
		WithClock(func() time.Time { return fx.now }),
	}
	fx.svc = NewService(append(base, opts...)...)
	return fx
}

func validDraft() domain.VerificationDraftInput {
	return domain.VerificationDraftInput{
		Category:        "media",
		Description:     "An independent regional newsroom publishing daily coverage since 2014.",
		OfficialWebsite: "https://example.org",
		SocialLinks:     []string{"https://example.org/social"},
		PressLinks:      []string{"https://press.example.org/a", "https://press.example.com/b"},
	}
}

func botRequest() domain.SubmitVerificationApplicationRequest {
	return domain.SubmitVerificationApplicationRequest{
		ApplicantUserID: applicantID,
		TargetType:      domain.VerificationTargetBot,
		TargetID:        botID,
		Draft:           validDraft(),
	}
}

func channelRequest() domain.SubmitVerificationApplicationRequest {
	return domain.SubmitVerificationApplicationRequest{
		ApplicantUserID: applicantID,
		TargetType:      domain.VerificationTargetChannel,
		TargetID:        channelID,
		Draft:           validDraft(),
	}
}

// submitted files and submits an application, returning the submitted record.
func (fx *fixture) submitted(t *testing.T, req domain.SubmitVerificationApplicationRequest) domain.VerificationApplication {
	t.Helper()
	ctx := context.Background()
	app, created, err := fx.svc.StartDraft(ctx, req)
	if err != nil {
		t.Fatalf("StartDraft: %v", err)
	}
	if !created {
		t.Fatalf("StartDraft created = false, want a fresh draft")
	}
	submitted, err := fx.svc.Submit(ctx, req.ApplicantUserID, app.ID, app.Version)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	return submitted
}

// ---------------------------------------------------------------------------
// Security checks, one test per refusal
// ---------------------------------------------------------------------------

func TestStartDraftAcceptsOwnedPublicBot(t *testing.T) {
	fx := newFixture(t)
	app, created, err := fx.svc.StartDraft(context.Background(), botRequest())
	if err != nil {
		t.Fatalf("StartDraft: %v", err)
	}
	if !created {
		t.Fatal("created = false, want true")
	}
	// The stored snapshot must come from the resolved peer, not from the request.
	if app.TargetUsername != "mybot" || app.TargetTitle != "My Bot" {
		t.Fatalf("target snapshot = %q/%q, want My Bot/mybot", app.TargetTitle, app.TargetUsername)
	}
}

func TestStartDraftRejectsMissingTarget(t *testing.T) {
	fx := newFixture(t)
	req := botRequest()
	req.TargetID = 999999
	if _, _, err := fx.svc.StartDraft(context.Background(), req); !errors.Is(err, domain.ErrVerificationTargetInvalid) {
		t.Fatalf("err = %v, want ErrVerificationTargetInvalid", err)
	}
}

func TestStartDraftRejectsUserTargetWhenDisabled(t *testing.T) {
	fx := newFixture(t)
	req := botRequest()
	req.TargetType = domain.VerificationTargetUser
	req.TargetID = applicantID
	if _, _, err := fx.svc.StartDraft(context.Background(), req); !errors.Is(err, domain.ErrVerificationUserTargetsDisabled) {
		t.Fatalf("err = %v, want ErrVerificationUserTargetsDisabled", err)
	}
}

func TestStartDraftAcceptsUserTargetWhenEnabled(t *testing.T) {
	fx := newFixture(t, WithAllowUserTargets(true))
	req := botRequest()
	req.TargetType = domain.VerificationTargetUser
	req.TargetID = applicantID
	app, created, err := fx.svc.StartDraft(context.Background(), req)
	if err != nil || !created {
		t.Fatalf("StartDraft = %v, created=%v", err, created)
	}
	if app.TargetUsername != "applicant" {
		t.Fatalf("target username = %q", app.TargetUsername)
	}
}

func TestStartDraftRejectsNonBotFiledAsBot(t *testing.T) {
	// A plain account filed under the bot kind would bypass the user-targets
	// switch, so the namespaces are pinned.
	fx := newFixture(t)
	req := botRequest()
	req.TargetID = applicantID
	if _, _, err := fx.svc.StartDraft(context.Background(), req); !errors.Is(err, domain.ErrVerificationTargetInvalid) {
		t.Fatalf("err = %v, want ErrVerificationTargetInvalid", err)
	}
}

func TestStartDraftRejectsTargetWithoutUsername(t *testing.T) {
	fx := newFixture(t)
	channel := fx.dir.channels[channelID]
	channel.Username = ""
	fx.dir.channels[channelID] = channel
	if _, _, err := fx.svc.StartDraft(context.Background(), channelRequest()); !errors.Is(err, domain.ErrVerificationTargetNotPublic) {
		t.Fatalf("err = %v, want ErrVerificationTargetNotPublic", err)
	}
}

func TestStartDraftRejectsForeignBot(t *testing.T) {
	fx := newFixture(t)
	req := botRequest()
	req.ApplicantUserID = strangerID
	if _, _, err := fx.svc.StartDraft(context.Background(), req); !errors.Is(err, domain.ErrVerificationNotOwner) {
		t.Fatalf("err = %v, want ErrVerificationNotOwner", err)
	}
}

func TestStartDraftRejectsChannelTheApplicantDoesNotAdminister(t *testing.T) {
	fx := newFixture(t)
	fx.dir.adminedChannels[applicantID] = nil
	if _, _, err := fx.svc.StartDraft(context.Background(), channelRequest()); !errors.Is(err, domain.ErrVerificationNotOwner) {
		t.Fatalf("err = %v, want ErrVerificationNotOwner", err)
	}
}

func TestStartDraftRejectsAlreadyVerifiedTarget(t *testing.T) {
	fx := newFixture(t)
	channel := fx.dir.channels[channelID]
	channel.Verified = true
	fx.dir.channels[channelID] = channel
	if _, _, err := fx.svc.StartDraft(context.Background(), channelRequest()); !errors.Is(err, domain.ErrVerificationTargetAlreadyVerified) {
		t.Fatalf("err = %v, want ErrVerificationTargetAlreadyVerified", err)
	}
}

func TestStartDraftRejectsRestrictedTarget(t *testing.T) {
	t.Run("scam channel", func(t *testing.T) {
		fx := newFixture(t)
		channel := fx.dir.channels[channelID]
		channel.Scam = true
		fx.dir.channels[channelID] = channel
		if _, _, err := fx.svc.StartDraft(context.Background(), channelRequest()); !errors.Is(err, domain.ErrVerificationTargetRestricted) {
			t.Fatalf("err = %v, want ErrVerificationTargetRestricted", err)
		}
	})
	t.Run("fake channel", func(t *testing.T) {
		fx := newFixture(t)
		channel := fx.dir.channels[channelID]
		channel.Fake = true
		fx.dir.channels[channelID] = channel
		if _, _, err := fx.svc.StartDraft(context.Background(), channelRequest()); !errors.Is(err, domain.ErrVerificationTargetRestricted) {
			t.Fatalf("err = %v, want ErrVerificationTargetRestricted", err)
		}
	})
	t.Run("deleted bot", func(t *testing.T) {
		fx := newFixture(t)
		bot := fx.dir.users[botID]
		bot.Deleted = true
		fx.dir.users[botID] = bot
		if _, _, err := fx.svc.StartDraft(context.Background(), botRequest()); !errors.Is(err, domain.ErrVerificationTargetRestricted) {
			t.Fatalf("err = %v, want ErrVerificationTargetRestricted", err)
		}
	})
	t.Run("frozen bot", func(t *testing.T) {
		fx := newFixture(t)
		fx.freezes.frozen[botID] = true
		if _, _, err := fx.svc.StartDraft(context.Background(), botRequest()); !errors.Is(err, domain.ErrVerificationTargetRestricted) {
			t.Fatalf("err = %v, want ErrVerificationTargetRestricted", err)
		}
	})
}

func TestStartDraftRejectsSystemTarget(t *testing.T) {
	fx := newFixture(t)
	fx.dir.ownedBots[applicantID] = append(fx.dir.ownedBots[applicantID], domain.VerifyBotUserID)
	req := botRequest()
	req.TargetID = domain.VerifyBotUserID
	if _, _, err := fx.svc.StartDraft(context.Background(), req); !errors.Is(err, domain.ErrVerificationTargetSystem) {
		t.Fatalf("err = %v, want ErrVerificationTargetSystem", err)
	}
}

func TestStartDraftRejectsSecondApplicationForTarget(t *testing.T) {
	fx := newFixture(t)
	// Another applicant already holds the target's single active slot.
	fx.st.put(domain.VerificationApplication{
		ApplicantUserID: strangerID,
		TargetType:      domain.VerificationTargetChannel,
		TargetID:        channelID,
		Status:          domain.VerificationStatusSubmitted,
		Version:         1,
	})
	if _, _, err := fx.svc.StartDraft(context.Background(), channelRequest()); !errors.Is(err, domain.ErrVerificationApplicationExists) {
		t.Fatalf("err = %v, want ErrVerificationApplicationExists", err)
	}
}

func TestStartDraftEnforcesRejectCooldown(t *testing.T) {
	fx := newFixture(t, WithRejectCooldown(720*time.Hour))
	fx.st.rejections[targetKey(domain.VerificationTargetChannel, channelID)] = domain.VerificationApplication{
		ID:              99,
		ApplicantUserID: applicantID,
		TargetType:      domain.VerificationTargetChannel,
		TargetID:        channelID,
		Status:          domain.VerificationStatusRejected,
		ReviewedAt:      fx.now.Add(-24 * time.Hour),
	}
	if _, _, err := fx.svc.StartDraft(context.Background(), channelRequest()); !errors.Is(err, domain.ErrVerificationCooldown) {
		t.Fatalf("err = %v, want ErrVerificationCooldown", err)
	}
	// Past the cooldown the same pair is accepted again.
	fx.now = fx.now.Add(721 * time.Hour)
	if _, created, err := fx.svc.StartDraft(context.Background(), channelRequest()); err != nil || !created {
		t.Fatalf("StartDraft after cooldown = %v, created=%v", err, created)
	}
}

func TestStartDraftRateLimitsCreation(t *testing.T) {
	fx := newFixture(t)
	fx.limiter.budget = 0
	if _, _, err := fx.svc.StartDraft(context.Background(), channelRequest()); !errors.Is(err, domain.ErrVerificationRateLimited) {
		t.Fatalf("err = %v, want ErrVerificationRateLimited", err)
	}
	if len(fx.st.createRequests) != 0 {
		t.Fatalf("store was written despite the rate limit: %d creates", len(fx.st.createRequests))
	}
}

func TestStartDraftDoesNotSpendBudgetOnRefusedTarget(t *testing.T) {
	fx := newFixture(t)
	req := botRequest()
	req.ApplicantUserID = strangerID
	if _, _, err := fx.svc.StartDraft(context.Background(), req); !errors.Is(err, domain.ErrVerificationNotOwner) {
		t.Fatalf("err = %v, want ErrVerificationNotOwner", err)
	}
	if fx.limiter.calls != 0 {
		t.Fatalf("limiter calls = %d, want 0: probing must not cost budget", fx.limiter.calls)
	}
}

func TestStartDraftEnforcesActiveApplicationCap(t *testing.T) {
	fx := newFixture(t, WithMaxActivePerUser(1))
	fx.st.put(domain.VerificationApplication{
		ApplicantUserID: applicantID,
		TargetType:      domain.VerificationTargetSupergroup,
		TargetID:        groupID,
		Status:          domain.VerificationStatusSubmitted,
		Version:         1,
	})
	if _, _, err := fx.svc.StartDraft(context.Background(), channelRequest()); !errors.Is(err, domain.ErrVerificationRateLimited) {
		t.Fatalf("err = %v, want ErrVerificationRateLimited", err)
	}
}

func TestStartDraftResumesExistingDraftWithoutBudget(t *testing.T) {
	fx := newFixture(t)
	ctx := context.Background()
	first, created, err := fx.svc.StartDraft(ctx, channelRequest())
	if err != nil || !created {
		t.Fatalf("first StartDraft = %v created=%v", err, created)
	}
	spent := fx.limiter.calls
	again, created, err := fx.svc.StartDraft(ctx, channelRequest())
	if err != nil {
		t.Fatalf("resume StartDraft: %v", err)
	}
	if created {
		t.Fatal("created = true on resume, want false")
	}
	if again.ID != first.ID {
		t.Fatalf("resumed application %d, want %d", again.ID, first.ID)
	}
	if fx.limiter.calls != spent {
		t.Fatalf("limiter calls = %d, want %d: resuming must not cost budget", fx.limiter.calls, spent)
	}
}

func TestStartDraftRejectsPrivateLink(t *testing.T) {
	// Link validation is the domain's; the service must call it and never fetch.
	fx := newFixture(t)
	req := channelRequest()
	req.Draft.OfficialWebsite = "http://169.254.169.254/latest/meta-data/"
	if _, _, err := fx.svc.StartDraft(context.Background(), req); !errors.Is(err, domain.ErrVerificationURLInvalid) {
		t.Fatalf("err = %v, want ErrVerificationURLInvalid", err)
	}
}

func TestSubmitRequiresCompletePayload(t *testing.T) {
	fx := newFixture(t)
	ctx := context.Background()
	req := channelRequest()
	req.Draft.PressLinks = []string{"https://press.example.org/only-one"}
	app, _, err := fx.svc.StartDraft(ctx, req)
	if err != nil {
		t.Fatalf("StartDraft: %v", err)
	}
	if _, err := fx.svc.Submit(ctx, applicantID, app.ID, app.Version); !errors.Is(err, domain.ErrVerificationApplicationInvalid) {
		t.Fatalf("err = %v, want ErrVerificationApplicationInvalid", err)
	}
}

func TestSubmitRechecksTargetState(t *testing.T) {
	fx := newFixture(t)
	ctx := context.Background()
	app, _, err := fx.svc.StartDraft(ctx, channelRequest())
	if err != nil {
		t.Fatalf("StartDraft: %v", err)
	}
	channel := fx.dir.channels[channelID]
	channel.Scam = true
	fx.dir.channels[channelID] = channel
	if _, err := fx.svc.Submit(ctx, applicantID, app.ID, app.Version); !errors.Is(err, domain.ErrVerificationTargetRestricted) {
		t.Fatalf("err = %v, want ErrVerificationTargetRestricted", err)
	}
}

func TestApplicantPathsScopeByApplicant(t *testing.T) {
	fx := newFixture(t)
	ctx := context.Background()
	app, _, err := fx.svc.StartDraft(ctx, channelRequest())
	if err != nil {
		t.Fatalf("StartDraft: %v", err)
	}
	if _, err := fx.svc.Submit(ctx, strangerID, app.ID, app.Version); !errors.Is(err, domain.ErrVerificationApplicationNotFound) {
		t.Fatalf("err = %v, want ErrVerificationApplicationNotFound", err)
	}
	if _, err := fx.svc.SaveDraft(ctx, strangerID, app.ID, app.Version, validDraft()); !errors.Is(err, domain.ErrVerificationApplicationNotFound) {
		t.Fatalf("SaveDraft err = %v, want ErrVerificationApplicationNotFound", err)
	}
	if _, err := fx.svc.SaveDraft(ctx, applicantID, app.ID, app.Version+7, validDraft()); !errors.Is(err, domain.ErrVerificationVersionConflict) {
		t.Fatalf("SaveDraft err = %v, want ErrVerificationVersionConflict", err)
	}
}

// ---------------------------------------------------------------------------
// Reviewer side
// ---------------------------------------------------------------------------

func decision(app domain.VerificationApplication) domain.VerificationDecision {
	return domain.VerificationDecision{
		ApplicationID: app.ID,
		Version:       app.Version,
		Reviewer:      "admin@example.org",
	}
}

func TestApproveSetsFlagAndNotifiesExactlyOnce(t *testing.T) {
	fx := newFixture(t)
	ctx := context.Background()
	app := fx.submitted(t, channelRequest())

	stored, changed, err := fx.svc.Approve(ctx, decision(app))
	if err != nil {
		t.Fatalf("Approve: %v", err)
	}
	if !changed {
		t.Fatal("changed = false, want true")
	}
	if stored.Status != domain.VerificationStatusApproved {
		t.Fatalf("status = %s", stored.Status)
	}
	if fx.verifier.calls() != 1 || len(fx.verifier.channelCalls) != 1 || fx.verifier.channelCalls[0] != channelID {
		t.Fatalf("verifier calls = %+v/%+v", fx.verifier.userCalls, fx.verifier.channelCalls)
	}
	if len(fx.verifier.values) != 1 || !fx.verifier.values[0] {
		t.Fatalf("verifier values = %v, want [true]", fx.verifier.values)
	}
	if len(fx.peers.peers) != 1 || fx.peers.peers[0] != (domain.Peer{Type: domain.PeerTypeChannel, ID: channelID}) {
		t.Fatalf("peer notifications = %+v", fx.peers.peers)
	}
}

func TestApproveRepeatIsIdempotent(t *testing.T) {
	fx := newFixture(t)
	ctx := context.Background()
	app := fx.submitted(t, channelRequest())
	approved, _, err := fx.svc.Approve(ctx, decision(app))
	if err != nil {
		t.Fatalf("Approve: %v", err)
	}
	verifierCalls, pushCalls, decideCalls := fx.verifier.calls(), len(fx.peers.peers), fx.st.decideCalls

	stored, changed, err := fx.svc.Approve(ctx, decision(approved))
	if err != nil {
		t.Fatalf("second Approve: %v", err)
	}
	if changed {
		t.Fatal("changed = true on repeat, want false")
	}
	if stored.ID != approved.ID {
		t.Fatalf("stored id = %d, want %d", stored.ID, approved.ID)
	}
	if fx.verifier.calls() != verifierCalls {
		t.Fatalf("verifier called again: %d -> %d", verifierCalls, fx.verifier.calls())
	}
	if len(fx.peers.peers) != pushCalls {
		t.Fatalf("peer notifier called again: %d -> %d", pushCalls, len(fx.peers.peers))
	}
	if fx.st.decideCalls != decideCalls {
		t.Fatalf("store decide called again: %d -> %d", decideCalls, fx.st.decideCalls)
	}
}

func TestApproveRefusesTargetThatBecameScam(t *testing.T) {
	fx := newFixture(t)
	ctx := context.Background()
	app := fx.submitted(t, channelRequest())
	// The target is flagged between submission and review.
	channel := fx.dir.channels[channelID]
	channel.Scam = true
	fx.dir.channels[channelID] = channel

	if _, _, err := fx.svc.Approve(ctx, decision(app)); !errors.Is(err, domain.ErrVerificationTargetRestricted) {
		t.Fatalf("err = %v, want ErrVerificationTargetRestricted", err)
	}
	if fx.verifier.calls() != 0 || len(fx.peers.peers) != 0 || fx.st.decideCalls != 0 {
		t.Fatalf("approve reached the write path: verifier=%d push=%d decide=%d",
			fx.verifier.calls(), len(fx.peers.peers), fx.st.decideCalls)
	}
}

func TestApproveRefusesTargetVerifiedByAnotherRoute(t *testing.T) {
	fx := newFixture(t)
	ctx := context.Background()
	app := fx.submitted(t, channelRequest())
	channel := fx.dir.channels[channelID]
	channel.Verified = true
	fx.dir.channels[channelID] = channel
	if _, _, err := fx.svc.Approve(ctx, decision(app)); !errors.Is(err, domain.ErrVerificationTargetAlreadyVerified) {
		t.Fatalf("err = %v, want ErrVerificationTargetAlreadyVerified", err)
	}
}

func TestApproveRefusesTargetTheApplicantLost(t *testing.T) {
	fx := newFixture(t)
	ctx := context.Background()
	app := fx.submitted(t, channelRequest())
	fx.dir.adminedChannels[applicantID] = nil
	if _, _, err := fx.svc.Approve(ctx, decision(app)); !errors.Is(err, domain.ErrVerificationNotOwner) {
		t.Fatalf("err = %v, want ErrVerificationNotOwner", err)
	}
}

func TestApproveRefusesStaleVersion(t *testing.T) {
	fx := newFixture(t)
	app := fx.submitted(t, channelRequest())
	stale := decision(app)
	stale.Version = app.Version - 1
	if _, _, err := fx.svc.Approve(context.Background(), stale); !errors.Is(err, domain.ErrVerificationVersionConflict) {
		t.Fatalf("err = %v, want ErrVerificationVersionConflict", err)
	}
	if fx.verifier.calls() != 0 {
		t.Fatalf("verifier called on a stale decision")
	}
}

func TestApproveRollsBackWhenFlagWriteFails(t *testing.T) {
	fx := newFixture(t)
	fx.verifier.err = errors.New("peer store down")
	app := fx.submitted(t, channelRequest())
	if _, _, err := fx.svc.Approve(context.Background(), decision(app)); err == nil {
		t.Fatal("Approve succeeded despite a failing flag write")
	}
	if len(fx.peers.peers) != 0 {
		t.Fatalf("pushed an update for a rolled-back decision: %+v", fx.peers.peers)
	}
	stored, err := fx.svc.Application(context.Background(), app.ID)
	if err != nil {
		t.Fatalf("Application: %v", err)
	}
	if stored.Status == domain.VerificationStatusApproved {
		t.Fatal("application approved even though the flag write failed")
	}
}

func TestApproveTolerantOfPushFailure(t *testing.T) {
	fx := newFixture(t)
	fx.peers.err = errors.New("router offline")
	app := fx.submitted(t, channelRequest())
	stored, changed, err := fx.svc.Approve(context.Background(), decision(app))
	if err != nil {
		t.Fatalf("Approve: %v", err)
	}
	if !changed || stored.Status != domain.VerificationStatusApproved {
		t.Fatalf("changed=%v status=%s, want a landed decision", changed, stored.Status)
	}
}

func TestRejectRequiresReason(t *testing.T) {
	fx := newFixture(t)
	app := fx.submitted(t, channelRequest())
	if _, _, err := fx.svc.Reject(context.Background(), decision(app)); !errors.Is(err, domain.ErrVerificationReasonRequired) {
		t.Fatalf("err = %v, want ErrVerificationReasonRequired", err)
	}
	if fx.st.decideCalls != 0 {
		t.Fatalf("store decide called for a reasonless rejection")
	}
}

func TestRejectRecordsReasonAndTouchesNoPeer(t *testing.T) {
	fx := newFixture(t)
	app := fx.submitted(t, channelRequest())
	dec := decision(app)
	dec.Reason = "no independent coverage"
	stored, changed, err := fx.svc.Reject(context.Background(), dec)
	if err != nil || !changed {
		t.Fatalf("Reject = %v changed=%v", err, changed)
	}
	if stored.Status != domain.VerificationStatusRejected || stored.DecisionReason != dec.Reason {
		t.Fatalf("stored = %s/%q", stored.Status, stored.DecisionReason)
	}
	if fx.verifier.calls() != 0 || len(fx.peers.peers) != 0 {
		t.Fatal("a rejection must not touch the peer record")
	}
}

func TestClaimMovesApplicationIntoReview(t *testing.T) {
	fx := newFixture(t)
	app := fx.submitted(t, channelRequest())
	claimed, err := fx.svc.Claim(context.Background(), decision(app))
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if claimed.Status != domain.VerificationStatusInReview || claimed.ReviewerAdminID != "admin@example.org" {
		t.Fatalf("claimed = %s/%q", claimed.Status, claimed.ReviewerAdminID)
	}
	if _, err := fx.svc.Claim(context.Background(), domain.VerificationDecision{ApplicationID: app.ID, Version: app.Version}); !errors.Is(err, domain.ErrVerificationApplicationInvalid) {
		t.Fatalf("Claim without reviewer err = %v, want ErrVerificationApplicationInvalid", err)
	}
}

func TestRevokeClearsFlagAndRequiresReason(t *testing.T) {
	fx := newFixture(t)
	ctx := context.Background()
	app := fx.submitted(t, channelRequest())
	if _, _, err := fx.svc.Approve(ctx, decision(app)); err != nil {
		t.Fatalf("Approve: %v", err)
	}
	if _, _, err := fx.svc.Revoke(ctx, domain.VerificationRevocation{
		TargetType: domain.VerificationTargetChannel,
		TargetID:   channelID,
		Reviewer:   "admin@example.org",
	}); !errors.Is(err, domain.ErrVerificationReasonRequired) {
		t.Fatalf("err = %v, want ErrVerificationReasonRequired", err)
	}
	stored, changed, err := fx.svc.Revoke(ctx, domain.VerificationRevocation{
		TargetType: domain.VerificationTargetChannel,
		TargetID:   channelID,
		Reviewer:   "admin@example.org",
		Reason:     "impersonation report upheld",
	})
	if err != nil || !changed {
		t.Fatalf("Revoke = %v changed=%v", err, changed)
	}
	// The application stays approved: it is history.
	if stored.Status != domain.VerificationStatusApproved {
		t.Fatalf("application status = %s, want approved history", stored.Status)
	}
	if len(fx.verifier.values) != 2 || fx.verifier.values[1] {
		t.Fatalf("verifier values = %v, want the second write to clear the flag", fx.verifier.values)
	}
	if len(fx.peers.peers) != 2 {
		t.Fatalf("peer notifications = %d, want one per committed change", len(fx.peers.peers))
	}
}

func TestRevokeRefusesSystemTarget(t *testing.T) {
	fx := newFixture(t)
	_, _, err := fx.svc.Revoke(context.Background(), domain.VerificationRevocation{
		TargetType: domain.VerificationTargetBot,
		TargetID:   domain.VerifyBotUserID,
		Reviewer:   "admin@example.org",
		Reason:     "testing",
	})
	if !errors.Is(err, domain.ErrVerificationTargetSystem) {
		t.Fatalf("err = %v, want ErrVerificationTargetSystem", err)
	}
	if fx.st.revokeCalls != 0 {
		t.Fatal("store revoke called for a system target")
	}
}

// ---------------------------------------------------------------------------
// Target picker and snapshot
// ---------------------------------------------------------------------------

func TestEligibleTargetsExplainsIneligibleCandidates(t *testing.T) {
	fx := newFixture(t)
	channel := fx.dir.channels[channelID]
	channel.Verified = true
	fx.dir.channels[channelID] = channel

	targets, err := fx.svc.EligibleTargets(context.Background(), applicantID)
	if err != nil {
		t.Fatalf("EligibleTargets: %v", err)
	}
	if len(targets) != 3 {
		t.Fatalf("targets = %d (%+v), want bot + channel + supergroup", len(targets), targets)
	}
	byID := map[int64]domain.VerificationTarget{}
	for _, target := range targets {
		byID[target.ID] = target
	}
	if got := byID[botID]; !got.Eligible || got.Type != domain.VerificationTargetBot {
		t.Fatalf("bot target = %+v, want eligible bot", got)
	}
	if got := byID[groupID]; !got.Eligible || got.Type != domain.VerificationTargetSupergroup {
		t.Fatalf("group target = %+v, want eligible supergroup", got)
	}
	if got := byID[channelID]; got.Eligible || got.Reason != domain.ErrVerificationTargetAlreadyVerified.Error() {
		t.Fatalf("channel target = %+v, want ineligible with the already-verified reason", got)
	}
}

func TestEligibleTargetsOffersOwnAccountOnlyWhenEnabled(t *testing.T) {
	fx := newFixture(t)
	targets, err := fx.svc.EligibleTargets(context.Background(), applicantID)
	if err != nil {
		t.Fatalf("EligibleTargets: %v", err)
	}
	for _, target := range targets {
		if target.Type == domain.VerificationTargetUser {
			t.Fatalf("user target offered while disabled: %+v", target)
		}
	}
	fx = newFixture(t, WithAllowUserTargets(true))
	targets, err = fx.svc.EligibleTargets(context.Background(), applicantID)
	if err != nil {
		t.Fatalf("EligibleTargets: %v", err)
	}
	found := false
	for _, target := range targets {
		if target.Type == domain.VerificationTargetUser && target.ID == applicantID && target.Eligible {
			found = true
		}
	}
	if !found {
		t.Fatalf("own account not offered with user targets enabled: %+v", targets)
	}
}

func TestEligibleTargetsReportsActiveApplicationAndCap(t *testing.T) {
	fx := newFixture(t)
	fx.st.put(domain.VerificationApplication{
		ApplicantUserID: strangerID,
		TargetType:      domain.VerificationTargetChannel,
		TargetID:        channelID,
		Status:          domain.VerificationStatusSubmitted,
		Version:         1,
	})
	targets, err := fx.svc.EligibleTargets(context.Background(), applicantID)
	if err != nil {
		t.Fatalf("EligibleTargets: %v", err)
	}
	for _, target := range targets {
		if target.ID != channelID {
			continue
		}
		if target.Eligible || target.Reason != domain.ErrVerificationApplicationExists.Error() {
			t.Fatalf("channel target = %+v, want the already-active reason", target)
		}
	}

	capped := newFixture(t, WithMaxActivePerUser(1))
	capped.st.put(domain.VerificationApplication{
		ApplicantUserID: applicantID,
		TargetType:      domain.VerificationTargetSupergroup,
		TargetID:        groupID,
		Status:          domain.VerificationStatusInReview,
		Version:         1,
	})
	targets, err = capped.svc.EligibleTargets(context.Background(), applicantID)
	if err != nil {
		t.Fatalf("EligibleTargets: %v", err)
	}
	for _, target := range targets {
		if target.Eligible {
			t.Fatalf("target %+v eligible while the applicant is at the cap", target)
		}
		if target.Reason != domain.ErrVerificationRateLimited.Error() {
			t.Fatalf("target %+v reason = %q, want the cap reason", target, target.Reason)
		}
	}
}

func TestTargetSnapshotReportsCurrentState(t *testing.T) {
	fx := newFixture(t)
	snapshot, err := fx.svc.TargetSnapshot(context.Background(), domain.VerificationTargetChannel, channelID)
	if err != nil {
		t.Fatalf("TargetSnapshot: %v", err)
	}
	if !snapshot.Eligible || snapshot.Username != "mychannel" || snapshot.Title != "My Channel" || snapshot.AccessHash != 33 {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	channel := fx.dir.channels[channelID]
	channel.Scam = true
	fx.dir.channels[channelID] = channel
	snapshot, err = fx.svc.TargetSnapshot(context.Background(), domain.VerificationTargetChannel, channelID)
	if err != nil {
		t.Fatalf("TargetSnapshot: %v", err)
	}
	if snapshot.Eligible || snapshot.Reason != domain.ErrVerificationTargetRestricted.Error() {
		t.Fatalf("snapshot = %+v, want ineligible/restricted", snapshot)
	}
}

// ---------------------------------------------------------------------------
// Notification delivery
// ---------------------------------------------------------------------------

func TestRunNotificationCycleDeliversPending(t *testing.T) {
	fx := newFixture(t)
	ctx := context.Background()
	app := fx.submitted(t, channelRequest())
	if _, _, err := fx.svc.Approve(ctx, decision(app)); err != nil {
		t.Fatalf("Approve: %v", err)
	}
	delivered, err := fx.svc.RunNotificationCycle(ctx, 10)
	if err != nil {
		t.Fatalf("RunNotificationCycle: %v", err)
	}
	if delivered != 1 {
		t.Fatalf("delivered = %d, want 1", delivered)
	}
	if len(fx.notifier.sent) != 1 || fx.notifier.sent[0].recipient != applicantID || fx.notifier.sent[0].kind != NoticeKindApproved {
		t.Fatalf("sent = %+v", fx.notifier.sent)
	}
	if len(fx.st.deliveredIDs) != 1 {
		t.Fatalf("delivered ids = %v", fx.st.deliveredIDs)
	}
	// A second cycle has nothing left to do.
	delivered, err = fx.svc.RunNotificationCycle(ctx, 10)
	if err != nil || delivered != 0 {
		t.Fatalf("second cycle = %d/%v, want 0/nil", delivered, err)
	}
}

func TestRunNotificationCycleMarksFailureAndRetries(t *testing.T) {
	fx := newFixture(t)
	ctx := context.Background()
	app := fx.submitted(t, channelRequest())
	if _, _, err := fx.svc.Approve(ctx, decision(app)); err != nil {
		t.Fatalf("Approve: %v", err)
	}
	fx.notifier.err = errors.New("bot blocked by the user")

	delivered, err := fx.svc.RunNotificationCycle(ctx, 10)
	if err != nil {
		t.Fatalf("RunNotificationCycle: %v", err)
	}
	if delivered != 0 {
		t.Fatalf("delivered = %d, want 0", delivered)
	}
	if len(fx.st.failedIDs) != 1 || len(fx.st.deliveredIDs) != 0 {
		t.Fatalf("failed = %v delivered = %v", fx.st.failedIDs, fx.st.deliveredIDs)
	}
	if !strings.Contains(fx.st.failedReasons[0], "bot blocked") {
		t.Fatalf("failure reason = %q", fx.st.failedReasons[0])
	}
	// The row stays queued, so the next cycle retries it and can succeed.
	fx.notifier.err = nil
	delivered, err = fx.svc.RunNotificationCycle(ctx, 10)
	if err != nil {
		t.Fatalf("retry cycle: %v", err)
	}
	if delivered != 1 || len(fx.st.deliveredIDs) != 1 {
		t.Fatalf("retry delivered = %d, ids = %v", delivered, fx.st.deliveredIDs)
	}
}

func TestRunNotificationCycleFailsRowWithoutRecipient(t *testing.T) {
	fx := newFixture(t)
	fx.st.pending = append(fx.st.pending, store.VerificationNotification{ID: 7, ApplicationID: 3, Kind: NoticeKindApproved})
	delivered, err := fx.svc.RunNotificationCycle(context.Background(), 10)
	if err != nil {
		t.Fatalf("RunNotificationCycle: %v", err)
	}
	if delivered != 0 || len(fx.st.failedIDs) != 1 || fx.st.failedIDs[0] != 7 {
		t.Fatalf("delivered=%d failed=%v", delivered, fx.st.failedIDs)
	}
	if len(fx.notifier.sent) != 0 {
		t.Fatalf("sent a notice with no recipient: %+v", fx.notifier.sent)
	}
}

func TestRunNotificationCycleWithoutNotifierReportsConfigError(t *testing.T) {
	fx := newFixture(t)
	svc := NewService(WithStore(fx.st), WithPeerDirectory(fx.dir))
	if _, err := svc.RunNotificationCycle(context.Background(), 10); err == nil {
		t.Fatal("RunNotificationCycle succeeded without an applicant notifier")
	}
}

// ---------------------------------------------------------------------------
// Feature switch and wiring guards
// ---------------------------------------------------------------------------

func TestDisabledServiceRefusesEverything(t *testing.T) {
	fx := newFixture(t, WithEnabled(false))
	ctx := context.Background()
	if fx.svc.Enabled() || fx.svc.Ready() {
		t.Fatal("service reports enabled while disabled")
	}
	if _, err := fx.svc.EligibleTargets(ctx, applicantID); !errors.Is(err, ErrDisabled) {
		t.Fatalf("EligibleTargets err = %v", err)
	}
	if _, _, err := fx.svc.StartDraft(ctx, channelRequest()); !errors.Is(err, ErrDisabled) {
		t.Fatalf("StartDraft err = %v", err)
	}
	if _, _, err := fx.svc.Approve(ctx, domain.VerificationDecision{ApplicationID: 1, Version: 1, Reviewer: "a"}); !errors.Is(err, ErrDisabled) {
		t.Fatalf("Approve err = %v", err)
	}
	if _, err := fx.svc.TargetSnapshot(ctx, domain.VerificationTargetChannel, channelID); !errors.Is(err, ErrDisabled) {
		t.Fatalf("TargetSnapshot err = %v", err)
	}
	// The worker cycle is a cadence, not a user action: it stays silent.
	if delivered, err := fx.svc.RunNotificationCycle(ctx, 10); err != nil || delivered != 0 {
		t.Fatalf("RunNotificationCycle = %d/%v, want 0/nil", delivered, err)
	}
}

func TestApproveWithoutVerifierRefuses(t *testing.T) {
	fx := newFixture(t)
	svc := NewService(WithStore(fx.st), WithPeerDirectory(fx.dir), WithRateLimiter(fx.limiter, 3, time.Hour))
	app := fx.submitted(t, channelRequest())
	if _, _, err := svc.Approve(context.Background(), decision(app)); err == nil {
		t.Fatal("Approve succeeded without a peer verifier")
	}
	if fx.st.decideCalls != 0 {
		t.Fatal("store decide called without a peer verifier")
	}
}

func TestNewServiceDefaults(t *testing.T) {
	svc := NewService()
	if !svc.Enabled() {
		t.Fatal("verification must default to enabled")
	}
	if svc.AllowsUserTargets() {
		t.Fatal("user targets must default to off")
	}
	if svc.rejectCooldown != defaultRejectCooldown || svc.maxActivePerUser != defaultMaxActivePerUser {
		t.Fatalf("cooldown=%v maxActive=%d", svc.rejectCooldown, svc.maxActivePerUser)
	}
	if svc.Ready() {
		t.Fatal("a store-less service must not report ready")
	}
	if _, err := svc.Counts(context.Background()); err == nil {
		t.Fatal("Counts succeeded without a store")
	}
}

func TestNotificationWorkerStopsWhenNotReady(t *testing.T) {
	// Run must return immediately instead of ticking over a no-op.
	worker := NewNotificationWorker(NewService(WithEnabled(false)), nil, 0, 0)
	done := make(chan struct{})
	go func() {
		worker.Run(context.Background())
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not exit for a disabled service")
	}
}
