package botverification

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

// The fixtures are one deployment: a verifier bot owned by one account, a second
// bot owned by the same account, a stranger, a plain user peer and a channel that
// account created.
const (
	fixtureVerifierBot = int64(100)
	fixtureOwnedBot    = int64(101)
	fixtureOwner       = int64(7)
	fixtureStranger    = int64(8)
	fixtureUserPeer    = int64(500)
	fixtureChannel     = int64(900)
	fixtureIconDoc     = int64(4242)
	fixtureOtherDoc    = int64(4343)
)

// ---------------------------------------------------------------------------
// Fakes
// ---------------------------------------------------------------------------

type memberKey struct {
	channelID int64
	userID    int64
}

// fakeDirectory is the peer-resolution port. It answers exactly like the
// aggregates do: an unknown channel is domain.ErrChannelInvalid and a
// non-membership is domain.ErrUserNotParticipant, so the service's error folding
// is exercised rather than bypassed.
type fakeDirectory struct {
	users    map[int64]domain.User
	channels map[int64]domain.Channel
	owners   map[int64]int64
	members  map[memberKey]domain.ChannelMember
}

func (f *fakeDirectory) AdminUser(_ context.Context, userID int64) (domain.User, bool, error) {
	user, ok := f.users[userID]
	return user, ok, nil
}

func (f *fakeDirectory) OwnsBot(_ context.Context, ownerUserID, botUserID int64) (bool, error) {
	owner, ok := f.owners[botUserID]
	return ok && owner == ownerUserID, nil
}

func (f *fakeDirectory) GetChannelByID(_ context.Context, channelID int64) (domain.Channel, error) {
	channel, ok := f.channels[channelID]
	if !ok {
		return domain.Channel{}, domain.ErrChannelInvalid
	}
	return channel, nil
}

func (f *fakeDirectory) GetParticipant(_ context.Context, _, channelID, participantUserID int64) (domain.ChannelMember, error) {
	member, ok := f.members[memberKey{channelID: channelID, userID: participantUserID}]
	if !ok {
		return domain.ChannelMember{}, domain.ErrUserNotParticipant
	}
	return member, nil
}

type fakePeerNotifier struct {
	peers []domain.Peer
	err   error
}

func (f *fakePeerNotifier) NotifyPeerBotVerification(_ context.Context, peer domain.Peer) error {
	f.peers = append(f.peers, peer)
	return f.err
}

func (f *fakePeerNotifier) count() int { return len(f.peers) }

type fakeIconResolver struct {
	documents map[int64]domain.Document
	calls     int
	err       error
}

func (f *fakeIconResolver) GetDocuments(_ context.Context, ids []int64) ([]domain.Document, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	out := make([]domain.Document, 0, len(ids))
	for _, id := range ids {
		if document, ok := f.documents[id]; ok {
			out = append(out, document)
		}
	}
	return out, nil
}

type applicantCall struct {
	recipientUserID int64
	request         domain.CustomVerificationRequest
}

type fakeApplicantNotifier struct {
	calls []applicantCall
	err   error
}

func (f *fakeApplicantNotifier) SendVerificationDecision(_ context.Context, recipientUserID int64, req domain.CustomVerificationRequest) error {
	f.calls = append(f.calls, applicantCall{recipientUserID: recipientUserID, request: req})
	return f.err
}

type fakeLimiter struct {
	allow bool
	keys  []string
	err   error
}

func (f *fakeLimiter) Allow(_ context.Context, key string, _ int, _ time.Duration) (bool, int, error) {
	f.keys = append(f.keys, key)
	if f.err != nil {
		return false, 0, f.err
	}
	if f.allow {
		return true, 0, nil
	}
	return false, 60, nil
}

func (f *fakeLimiter) AllowN(ctx context.Context, key string, _, limit int, window time.Duration) (bool, int, error) {
	return f.Allow(ctx, key, limit, window)
}

// fakeMarkApplier stands in for the process's transaction-aware writer: it
// records that the decision callbacks went through the port and then delegates to
// the store, which is what a real adapter does when the context carries no
// transaction.
type fakeMarkApplier struct {
	store   *memory.BotVerificationStore
	grants  int
	revokes int
}

func (f *fakeMarkApplier) GrantCustomVerification(ctx context.Context, mark domain.CustomVerification) (domain.CustomVerification, bool, error) {
	f.grants++
	return f.store.GrantCustomVerification(ctx, mark)
}

func (f *fakeMarkApplier) RevokeCustomVerification(ctx context.Context, verifierBotID int64, peer domain.Peer) (bool, error) {
	f.revokes++
	return f.store.RevokeCustomVerification(ctx, verifierBotID, peer)
}

func customEmojiDocument(id int64) domain.Document {
	return domain.Document{
		ID:         id,
		AccessHash: id * 3,
		MimeType:   "application/x-tgsticker",
		Attributes: []domain.DocumentAttribute{{Kind: domain.DocAttrCustomEmoji, Alt: "✅"}},
	}
}

// ---------------------------------------------------------------------------
// Harness
// ---------------------------------------------------------------------------

type harness struct {
	t         *testing.T
	svc       *Service
	store     *memory.BotVerificationStore
	dir       *fakeDirectory
	peers     *fakePeerNotifier
	docs      *fakeIconResolver
	applicant *fakeApplicantNotifier
	limiter   *fakeLimiter
}

func newHarness(t *testing.T, opts ...Option) *harness {
	t.Helper()
	h := &harness{
		t:     t,
		store: memory.NewBotVerificationStore(),
		dir: &fakeDirectory{
			users: map[int64]domain.User{
				fixtureVerifierBot: {ID: fixtureVerifierBot, FirstName: "Acme Verifier", Username: "acmeverifierbot", Bot: true, BotInfoVersion: 1},
				fixtureOwnedBot:    {ID: fixtureOwnedBot, FirstName: "Acme Helper", Username: "acmehelperbot", Bot: true, BotInfoVersion: 1},
				fixtureOwner:       {ID: fixtureOwner, FirstName: "Ann", LastName: "Owner", Username: "annowner"},
				fixtureStranger:    {ID: fixtureStranger, FirstName: "Sam", LastName: "Stranger", Username: "samstranger"},
				fixtureUserPeer:    {ID: fixtureUserPeer, FirstName: "Target", LastName: "Account", Username: "targetaccount"},
			},
			channels: map[int64]domain.Channel{
				fixtureChannel: {ID: fixtureChannel, CreatorUserID: fixtureOwner, Title: "Acme Channel", Username: "acmechannel", Broadcast: true},
			},
			owners: map[int64]int64{
				fixtureVerifierBot: fixtureOwner,
				fixtureOwnedBot:    fixtureOwner,
			},
			members: map[memberKey]domain.ChannelMember{},
		},
		peers:     &fakePeerNotifier{},
		docs:      &fakeIconResolver{documents: map[int64]domain.Document{fixtureIconDoc: customEmojiDocument(fixtureIconDoc)}},
		applicant: &fakeApplicantNotifier{},
		limiter:   &fakeLimiter{allow: true},
	}
	base := []Option{
		WithStore(h.store),
		WithPeerDirectory(h.dir),
		WithPeerNotifier(h.peers),
		WithIconResolver(h.docs),
		WithApplicantNotifier(h.applicant),
		WithRateLimiter(h.limiter, 5, 24*time.Hour),
	}
	h.svc = NewService(append(base, opts...)...)
	return h
}

// seedIcon writes a catalogue entry straight to the store, so a test can set up
// an icon state the service itself would refuse to create.
func (h *harness) seedIcon(documentID, ownerBotID int64, active bool) domain.VerificationIcon {
	h.t.Helper()
	icon, err := h.store.UpsertVerificationIcon(context.Background(), domain.VerificationIcon{
		DocumentID: documentID,
		OwnerBotID: ownerBotID,
		Name:       "Acme badge",
		Active:     active,
	})
	if err != nil {
		h.t.Fatalf("seed icon: %v", err)
	}
	return icon
}

func verifierSettings(enabled, canModify bool) domain.BotVerifierSettings {
	return domain.BotVerifierSettings{
		BotID:                      fixtureVerifierBot,
		IconDocumentID:             fixtureIconDoc,
		CompanyName:                "Acme Inc",
		DefaultDescription:         "Verified by Acme",
		CanModifyCustomDescription: canModify,
		Enabled:                    enabled,
		GrantedBy:                  "operator",
		GrantReason:                "partner programme",
	}
}

// seedVerifier writes verifier status straight to the store, bypassing
// GrantVerifier's own checks.
func (h *harness) seedVerifier(settings domain.BotVerifierSettings) domain.BotVerifierSettings {
	h.t.Helper()
	stored, err := h.store.UpsertBotVerifierSettings(context.Background(), settings)
	if err != nil {
		h.t.Fatalf("seed verifier: %v", err)
	}
	return stored
}

func (h *harness) mark(verifierBotID int64, peer domain.Peer) (domain.CustomVerification, bool) {
	h.t.Helper()
	mark, err := h.store.CustomVerification(context.Background(), verifierBotID, peer)
	if errors.Is(err, domain.ErrCustomVerificationNotFound) {
		return domain.CustomVerification{}, false
	}
	if err != nil {
		h.t.Fatalf("read mark: %v", err)
	}
	return mark, true
}

func userPeer(id int64) domain.Peer { return domain.Peer{Type: domain.PeerTypeUser, ID: id} }
func chanPeer(id int64) domain.Peer { return domain.Peer{Type: domain.PeerTypeChannel, ID: id} }

// ---------------------------------------------------------------------------
// bots.setCustomVerification: one refusal per test
// ---------------------------------------------------------------------------

func TestSetCustomVerificationRefusesNonVerifier(t *testing.T) {
	h := newHarness(t)

	changed, err := h.svc.SetCustomVerification(context.Background(), domain.SetCustomVerificationRequest{
		VerifierBotID: fixtureVerifierBot,
		Peer:          userPeer(fixtureUserPeer),
		Enabled:       true,
		CallerUserID:  fixtureVerifierBot,
	})
	if !errors.Is(err, domain.ErrVerifierForbidden) {
		t.Fatalf("err = %v, want ErrVerifierForbidden", err)
	}
	if changed {
		t.Fatal("changed = true for a bot that is not a verifier")
	}
	if _, found := h.mark(fixtureVerifierBot, userPeer(fixtureUserPeer)); found {
		t.Fatal("a mark was written by a non-verifier")
	}
}

func TestSetCustomVerificationRefusesDisabledVerifier(t *testing.T) {
	h := newHarness(t)
	h.seedIcon(fixtureIconDoc, 0, true)
	h.seedVerifier(verifierSettings(false, true))

	_, err := h.svc.SetCustomVerification(context.Background(), domain.SetCustomVerificationRequest{
		VerifierBotID: fixtureVerifierBot,
		Peer:          userPeer(fixtureUserPeer),
		Enabled:       true,
		CallerUserID:  fixtureVerifierBot,
	})
	// The kill switch is indistinguishable from "never was a verifier", by design.
	if !errors.Is(err, domain.ErrVerifierForbidden) {
		t.Fatalf("err = %v, want ErrVerifierForbidden", err)
	}
	if _, found := h.mark(fixtureVerifierBot, userPeer(fixtureUserPeer)); found {
		t.Fatal("a disabled verifier granted a mark")
	}
}

func TestSetCustomVerificationRefusesForeignCaller(t *testing.T) {
	h := newHarness(t)
	h.seedIcon(fixtureIconDoc, 0, true)
	h.seedVerifier(verifierSettings(true, true))

	_, err := h.svc.SetCustomVerification(context.Background(), domain.SetCustomVerificationRequest{
		VerifierBotID: fixtureVerifierBot,
		Peer:          userPeer(fixtureUserPeer),
		Enabled:       true,
		// Neither the bot itself nor its owner.
		CallerUserID: fixtureStranger,
	})
	if !errors.Is(err, domain.ErrVerifierForbidden) {
		t.Fatalf("err = %v, want ErrVerifierForbidden", err)
	}
	if _, found := h.mark(fixtureVerifierBot, userPeer(fixtureUserPeer)); found {
		t.Fatal("a stranger granted a mark through somebody else's verifier")
	}
}

func TestSetCustomVerificationAcceptsOwnerCaller(t *testing.T) {
	h := newHarness(t)
	h.seedIcon(fixtureIconDoc, 0, true)
	h.seedVerifier(verifierSettings(true, true))

	changed, err := h.svc.SetCustomVerification(context.Background(), domain.SetCustomVerificationRequest{
		VerifierBotID: fixtureVerifierBot,
		Peer:          userPeer(fixtureUserPeer),
		Enabled:       true,
		CallerUserID:  fixtureOwner,
	})
	if err != nil || !changed {
		t.Fatalf("SetCustomVerification by owner = %v/%v, want true/nil", changed, err)
	}
	mark, found := h.mark(fixtureVerifierBot, userPeer(fixtureUserPeer))
	if !found {
		t.Fatal("owner call did not grant the mark")
	}
	if mark.GrantedByUserID != fixtureOwner {
		t.Fatalf("GrantedByUserID = %d, want the calling owner %d", mark.GrantedByUserID, fixtureOwner)
	}
}

func TestSetCustomVerificationRefusesForbiddenDescription(t *testing.T) {
	h := newHarness(t)
	h.seedIcon(fixtureIconDoc, 0, true)
	h.seedVerifier(verifierSettings(true, false))

	_, err := h.svc.SetCustomVerification(context.Background(), domain.SetCustomVerificationRequest{
		VerifierBotID:     fixtureVerifierBot,
		Peer:              userPeer(fixtureUserPeer),
		Enabled:           true,
		CustomDescription: "hand-written text",
		CallerUserID:      fixtureVerifierBot,
	})
	if !errors.Is(err, domain.ErrVerifierDescriptionForbidden) {
		t.Fatalf("err = %v, want ErrVerifierDescriptionForbidden", err)
	}
	if _, found := h.mark(fixtureVerifierBot, userPeer(fixtureUserPeer)); found {
		t.Fatal("the mark was written despite the forbidden description")
	}
}

func TestSetCustomVerificationRefusesInvalidPeer(t *testing.T) {
	for name, peer := range map[string]domain.Peer{
		"unknown user":    userPeer(4242),
		"unknown channel": chanPeer(4242),
		// A built-in account's identity is seeded, not granted by a third party.
		"system account": userPeer(domain.VerifyBotUserID),
		// Not a peer kind that can carry a mark at all (peer_type CHECK).
		"community peer": {Type: domain.PeerTypeCommunity, ID: 12},
		"zero id":        userPeer(0),
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			h.seedIcon(fixtureIconDoc, 0, true)
			h.seedVerifier(verifierSettings(true, true))

			_, err := h.svc.SetCustomVerification(context.Background(), domain.SetCustomVerificationRequest{
				VerifierBotID: fixtureVerifierBot,
				Peer:          peer,
				Enabled:       true,
				CallerUserID:  fixtureVerifierBot,
			})
			if !errors.Is(err, domain.ErrCustomVerificationTargetInvalid) {
				t.Fatalf("err = %v, want ErrCustomVerificationTargetInvalid", err)
			}
			if h.peers.count() != 0 {
				t.Fatalf("peer pushes = %d, want none", h.peers.count())
			}
		})
	}
}

// TestSetCustomVerificationGrantsRevokesAndRepeats walks the whole RPC contract:
// the icon comes from the settings, an identical re-apply reports changed=false,
// an edited description does move, and a revoke is idempotent.
func TestSetCustomVerificationGrantsRevokesAndRepeats(t *testing.T) {
	h := newHarness(t)
	h.seedIcon(fixtureIconDoc, 0, true)
	h.seedVerifier(verifierSettings(true, true))
	ctx := context.Background()
	peer := chanPeer(fixtureChannel)
	request := domain.SetCustomVerificationRequest{
		VerifierBotID: fixtureVerifierBot,
		Peer:          peer,
		Enabled:       true,
		CallerUserID:  fixtureVerifierBot,
	}

	changed, err := h.svc.SetCustomVerification(ctx, request)
	if err != nil || !changed {
		t.Fatalf("first grant = %v/%v, want true/nil", changed, err)
	}
	mark, found := h.mark(fixtureVerifierBot, peer)
	if !found {
		t.Fatal("first grant stored no mark")
	}
	if mark.IconDocumentID != fixtureIconDoc {
		t.Fatalf("IconDocumentID = %d, want the verifier's configured icon %d", mark.IconDocumentID, fixtureIconDoc)
	}
	if mark.Description != "Verified by Acme" {
		t.Fatalf("Description = %q, want the operator default", mark.Description)
	}
	if h.peers.count() != 1 || h.peers.peers[0] != peer {
		t.Fatalf("peer pushes = %v, want exactly one for %v", h.peers.peers, peer)
	}

	// Identical re-apply: nothing moved, so the RPC answers Bool false and no push
	// is emitted.
	changed, err = h.svc.SetCustomVerification(ctx, request)
	if err != nil {
		t.Fatalf("repeat grant: %v", err)
	}
	if changed {
		t.Fatal("repeat grant reported changed = true")
	}
	if h.peers.count() != 1 {
		t.Fatalf("peer pushes = %d after a no-op repeat, want 1", h.peers.count())
	}

	// A different description is a real change.
	edited := request
	edited.CustomDescription = "Acme partner since 2019"
	changed, err = h.svc.SetCustomVerification(ctx, edited)
	if err != nil || !changed {
		t.Fatalf("description edit = %v/%v, want true/nil", changed, err)
	}
	if mark, _ := h.mark(fixtureVerifierBot, peer); mark.Description != "Acme partner since 2019" {
		t.Fatalf("Description = %q after the edit", mark.Description)
	}
	if h.peers.count() != 2 {
		t.Fatalf("peer pushes = %d after the edit, want 2", h.peers.count())
	}

	revoke := domain.SetCustomVerificationRequest{
		VerifierBotID: fixtureVerifierBot,
		Peer:          peer,
		CallerUserID:  fixtureVerifierBot,
	}
	changed, err = h.svc.SetCustomVerification(ctx, revoke)
	if err != nil || !changed {
		t.Fatalf("revoke = %v/%v, want true/nil", changed, err)
	}
	if _, found := h.mark(fixtureVerifierBot, peer); found {
		t.Fatal("the mark survived the revoke")
	}
	if h.peers.count() != 3 {
		t.Fatalf("peer pushes = %d after the revoke, want 3", h.peers.count())
	}

	changed, err = h.svc.SetCustomVerification(ctx, revoke)
	if err != nil {
		t.Fatalf("repeat revoke: %v", err)
	}
	if changed {
		t.Fatal("repeat revoke reported changed = true")
	}
	if h.peers.count() != 3 {
		t.Fatalf("peer pushes = %d after the repeat revoke, want 3", h.peers.count())
	}
}

// TestSetCustomVerificationRevokesVanishedPeer pins the asymmetry between the two
// directions: granting needs a resolvable peer, stripping does not. A peer that was
// deleted after being marked is exactly the case where a verifier most needs to be
// able to take its badge back.
func TestSetCustomVerificationRevokesVanishedPeer(t *testing.T) {
	h := newHarness(t)
	h.seedIcon(fixtureIconDoc, 0, true)
	h.seedVerifier(verifierSettings(true, true))
	ctx := context.Background()

	if _, err := h.svc.SetCustomVerification(ctx, domain.SetCustomVerificationRequest{
		VerifierBotID: fixtureVerifierBot,
		Peer:          userPeer(fixtureUserPeer),
		Enabled:       true,
		CallerUserID:  fixtureVerifierBot,
	}); err != nil {
		t.Fatalf("grant: %v", err)
	}
	// The account is gone from the directory by the time the verifier revokes.
	delete(h.dir.users, fixtureUserPeer)

	changed, err := h.svc.SetCustomVerification(ctx, domain.SetCustomVerificationRequest{
		VerifierBotID: fixtureVerifierBot,
		Peer:          userPeer(fixtureUserPeer),
		CallerUserID:  fixtureVerifierBot,
	})
	if err != nil || !changed {
		t.Fatalf("revoke of a vanished peer = %v/%v, want true/nil", changed, err)
	}
	if _, found := h.mark(fixtureVerifierBot, userPeer(fixtureUserPeer)); found {
		t.Fatal("the mark survived")
	}
}

// TestSetCustomVerificationSurvivesPushFailure pins the "data first, push second"
// rule: a broken notifier must not turn a committed mark into an RPC error.
func TestSetCustomVerificationSurvivesPushFailure(t *testing.T) {
	h := newHarness(t)
	h.seedIcon(fixtureIconDoc, 0, true)
	h.seedVerifier(verifierSettings(true, true))
	h.peers.err = errors.New("router is gone")

	changed, err := h.svc.SetCustomVerification(context.Background(), domain.SetCustomVerificationRequest{
		VerifierBotID: fixtureVerifierBot,
		Peer:          userPeer(fixtureUserPeer),
		Enabled:       true,
		CallerUserID:  fixtureVerifierBot,
	})
	if err != nil || !changed {
		t.Fatalf("grant with a failing push = %v/%v, want true/nil", changed, err)
	}
	if _, found := h.mark(fixtureVerifierBot, userPeer(fixtureUserPeer)); !found {
		t.Fatal("the mark was not stored")
	}
}

func TestSetCustomVerificationEnforcesPerVerifierBound(t *testing.T) {
	h := newHarness(t, WithMaxPerVerifier(1))
	h.seedIcon(fixtureIconDoc, 0, true)
	h.seedVerifier(verifierSettings(true, true))
	ctx := context.Background()

	if _, err := h.svc.SetCustomVerification(ctx, domain.SetCustomVerificationRequest{
		VerifierBotID: fixtureVerifierBot,
		Peer:          userPeer(fixtureUserPeer),
		Enabled:       true,
		CallerUserID:  fixtureVerifierBot,
	}); err != nil {
		t.Fatalf("first grant: %v", err)
	}
	_, err := h.svc.SetCustomVerification(ctx, domain.SetCustomVerificationRequest{
		VerifierBotID: fixtureVerifierBot,
		Peer:          chanPeer(fixtureChannel),
		Enabled:       true,
		CallerUserID:  fixtureVerifierBot,
	})
	if !errors.Is(err, domain.ErrCustomVerificationLimit) {
		t.Fatalf("err = %v, want ErrCustomVerificationLimit", err)
	}
	// An existing mark stays re-describable at the bound: the budget is spent on
	// new marks only.
	if _, err := h.svc.SetCustomVerification(ctx, domain.SetCustomVerificationRequest{
		VerifierBotID:     fixtureVerifierBot,
		Peer:              userPeer(fixtureUserPeer),
		Enabled:           true,
		CustomDescription: "still a partner",
		CallerUserID:      fixtureVerifierBot,
	}); err != nil {
		t.Fatalf("re-describe at the bound: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Verifier status
// ---------------------------------------------------------------------------

// TestGrantVerifierRefusesBeforeWriting is the ordering guarantee: every check
// runs before the store is touched, so a refused grant leaves no verifier row and
// pushes nothing.
func TestGrantVerifierRefusesBeforeWriting(t *testing.T) {
	for name, test := range map[string]struct {
		setup    func(h *harness)
		settings domain.BotVerifierSettings
		want     error
	}{
		"not a bot": {
			setup: func(h *harness) { h.seedIcon(fixtureIconDoc, 0, true) },
			settings: func() domain.BotVerifierSettings {
				settings := verifierSettings(true, true)
				settings.BotID = fixtureUserPeer
				return settings
			}(),
			want: domain.ErrBotNotFound,
		},
		"unknown account": {
			setup: func(h *harness) { h.seedIcon(fixtureIconDoc, 0, true) },
			settings: func() domain.BotVerifierSettings {
				settings := verifierSettings(true, true)
				settings.BotID = 31337
				return settings
			}(),
			want: domain.ErrBotNotFound,
		},
		"built-in system bot": {
			setup: func(h *harness) { h.seedIcon(fixtureIconDoc, 0, true) },
			settings: func() domain.BotVerifierSettings {
				settings := verifierSettings(true, true)
				settings.BotID = domain.VerifyBotUserID
				return settings
			}(),
			want: domain.ErrVerificationTargetSystem,
		},
		"icon absent from the catalogue": {
			setup:    func(h *harness) {},
			settings: verifierSettings(true, true),
			want:     domain.ErrVerificationIconNotFound,
		},
		"icon retired": {
			setup:    func(h *harness) { h.seedIcon(fixtureIconDoc, 0, false) },
			settings: verifierSettings(true, true),
			want:     domain.ErrVerificationIconInactive,
		},
		"icon reserved for another verifier": {
			setup:    func(h *harness) { h.seedIcon(fixtureIconDoc, fixtureOwnedBot, true) },
			settings: verifierSettings(true, true),
			want:     domain.ErrVerificationIconNotFound,
		},
		"document does not exist": {
			setup: func(h *harness) {
				h.seedIcon(fixtureIconDoc, 0, true)
				// The catalogue entry is fine; the document it names is gone, so the
				// badge would render as nothing at all.
				h.docs.documents = map[int64]domain.Document{}
			},
			settings: verifierSettings(true, true),
			want:     domain.ErrVerificationIconInvalid,
		},
		"document is not a custom emoji": {
			setup: func(h *harness) {
				h.seedIcon(fixtureIconDoc, 0, true)
				h.docs.documents = map[int64]domain.Document{
					fixtureIconDoc: {ID: fixtureIconDoc, MimeType: "video/mp4"},
				}
			},
			settings: verifierSettings(true, true),
			want:     domain.ErrVerificationIconInvalid,
		},
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			test.setup(h)

			_, err := h.svc.GrantVerifier(context.Background(), test.settings)
			if !errors.Is(err, test.want) {
				t.Fatalf("err = %v, want %v", err, test.want)
			}
			if _, err := h.store.BotVerifierSettings(context.Background(), test.settings.BotID); !errors.Is(err, domain.ErrVerifierNotFound) {
				t.Fatalf("verifier row after a refused grant: %v, want ErrVerifierNotFound", err)
			}
			if h.peers.count() != 0 {
				t.Fatalf("peer pushes = %d after a refused grant, want none", h.peers.count())
			}
		})
	}
}

func TestGrantVerifierStoresAndPushesTheBot(t *testing.T) {
	h := newHarness(t)
	h.seedIcon(fixtureIconDoc, 0, true)
	ctx := context.Background()

	stored, err := h.svc.GrantVerifier(ctx, verifierSettings(true, true))
	if err != nil {
		t.Fatalf("GrantVerifier: %v", err)
	}
	if stored.BotID != fixtureVerifierBot || stored.Version != 1 || !stored.Enabled {
		t.Fatalf("stored = %+v, want the first version of an enabled verifier", stored)
	}
	if h.docs.calls != 1 {
		t.Fatalf("icon resolver calls = %d, want 1", h.docs.calls)
	}
	// Only the verifier's own botInfo is pushed; its marked peers converge on their
	// next authoritative read.
	if h.peers.count() != 1 || h.peers.peers[0] != userPeer(fixtureVerifierBot) {
		t.Fatalf("peer pushes = %v, want exactly one for the bot itself", h.peers.peers)
	}

	// The built-in @verifierbot is the one system account this status exists for.
	verifier := verifierSettings(true, true)
	verifier.BotID = domain.VerifierBotUserID
	if _, err := h.svc.GrantVerifier(ctx, verifier); err != nil {
		t.Fatalf("GrantVerifier(@verifierbot): %v", err)
	}
}

func TestSetVerifierEnabledAndRevoke(t *testing.T) {
	h := newHarness(t)
	h.seedIcon(fixtureIconDoc, 0, true)
	granted := h.seedVerifier(verifierSettings(true, true))
	ctx := context.Background()

	// A flip to the value already stored burns no version and pushes nothing.
	same, err := h.svc.SetVerifierEnabled(ctx, fixtureVerifierBot, true)
	if err != nil {
		t.Fatalf("SetVerifierEnabled(no-op): %v", err)
	}
	if same.Version != granted.Version || h.peers.count() != 0 {
		t.Fatalf("no-op flip: version %d->%d, pushes %d", granted.Version, same.Version, h.peers.count())
	}

	disabled, err := h.svc.SetVerifierEnabled(ctx, fixtureVerifierBot, false)
	if err != nil {
		t.Fatalf("SetVerifierEnabled(false): %v", err)
	}
	if disabled.Enabled || disabled.Version == granted.Version {
		t.Fatalf("disabled = %+v, want enabled=false at a new version", disabled)
	}
	if h.peers.count() != 1 || h.peers.peers[0] != userPeer(fixtureVerifierBot) {
		t.Fatalf("peer pushes = %v, want one for the bot", h.peers.peers)
	}

	removed, err := h.svc.RevokeVerifier(ctx, fixtureVerifierBot)
	if err != nil || !removed {
		t.Fatalf("RevokeVerifier = %v/%v, want true/nil", removed, err)
	}
	if _, err := h.store.BotVerifierSettings(ctx, fixtureVerifierBot); !errors.Is(err, domain.ErrVerifierNotFound) {
		t.Fatalf("verifier row after revoke: %v", err)
	}
	// A repeated revoke is a no-op rather than an error.
	removed, err = h.svc.RevokeVerifier(ctx, fixtureVerifierBot)
	if err != nil || removed {
		t.Fatalf("repeat RevokeVerifier = %v/%v, want false/nil", removed, err)
	}
	if h.peers.count() != 2 {
		t.Fatalf("peer pushes = %d, want 2 (disable + revoke)", h.peers.count())
	}
}

// ---------------------------------------------------------------------------
// Icon catalogue
// ---------------------------------------------------------------------------

func TestUpsertIconRequiresAnExistingDocument(t *testing.T) {
	h := newHarness(t)
	h.docs.documents = map[int64]domain.Document{}
	ctx := context.Background()

	_, err := h.svc.UpsertIcon(ctx, domain.VerificationIcon{DocumentID: fixtureIconDoc, Name: "Acme badge", Active: true})
	if !errors.Is(err, domain.ErrVerificationIconInvalid) {
		t.Fatalf("err = %v, want ErrVerificationIconInvalid", err)
	}
	icons, err := h.store.ListVerificationIcons(ctx, false, 0)
	if err != nil {
		t.Fatalf("ListVerificationIcons: %v", err)
	}
	if len(icons) != 0 {
		t.Fatalf("catalogue = %+v, want nothing written for an unresolvable document", icons)
	}

	h.docs.documents[fixtureIconDoc] = customEmojiDocument(fixtureIconDoc)
	stored, err := h.svc.UpsertIcon(ctx, domain.VerificationIcon{DocumentID: fixtureIconDoc, Name: "  Acme badge  ", Active: true})
	if err != nil {
		t.Fatalf("UpsertIcon: %v", err)
	}
	if stored.ID <= 0 || stored.Name != "Acme badge" || !stored.Active {
		t.Fatalf("stored = %+v, want a trimmed active entry", stored)
	}

	retired, err := h.svc.SetIconActive(ctx, stored.ID, false)
	if err != nil {
		t.Fatalf("SetIconActive: %v", err)
	}
	if retired.Active {
		t.Fatal("SetIconActive(false) left the entry active")
	}
	active, err := h.svc.Icons(ctx, true, 0)
	if err != nil {
		t.Fatalf("Icons: %v", err)
	}
	if len(active) != 0 {
		t.Fatalf("active icons = %+v, want none after retiring the only entry", active)
	}
}

func TestUpsertIconWithoutResolverIsAConfigurationError(t *testing.T) {
	svc := NewService(WithStore(memory.NewBotVerificationStore()))

	_, err := svc.UpsertIcon(context.Background(), domain.VerificationIcon{DocumentID: fixtureIconDoc, Name: "Acme", Active: true})
	if err == nil || !strings.Contains(err.Error(), "icon resolver is not configured") {
		t.Fatalf("err = %v, want the resolver configuration error", err)
	}
}

// ---------------------------------------------------------------------------
// Applications
// ---------------------------------------------------------------------------

func (h *harness) createRequest(applicantUserID int64, peer domain.Peer, description string) (domain.CustomVerificationRequest, error) {
	return h.svc.CreateRequest(context.Background(), domain.CustomVerificationRequest{
		VerifierBotID:        fixtureVerifierBot,
		ApplicantUserID:      applicantUserID,
		Peer:                 peer,
		PeerTitle:            "TITLE THE CLIENT MADE UP",
		PeerUsername:         "clientmadethisup",
		Reason:               "we are a partner",
		RequestedDescription: description,
	})
}

func TestCreateRequestChecks(t *testing.T) {
	t.Run("unknown verifier", func(t *testing.T) {
		h := newHarness(t)
		if _, err := h.createRequest(fixtureOwner, chanPeer(fixtureChannel), ""); !errors.Is(err, domain.ErrVerifierForbidden) {
			t.Fatalf("err = %v, want ErrVerifierForbidden", err)
		}
	})

	t.Run("disabled verifier", func(t *testing.T) {
		h := newHarness(t)
		h.seedIcon(fixtureIconDoc, 0, true)
		h.seedVerifier(verifierSettings(false, true))
		if _, err := h.createRequest(fixtureOwner, chanPeer(fixtureChannel), ""); !errors.Is(err, domain.ErrVerifierForbidden) {
			t.Fatalf("err = %v, want ErrVerifierForbidden", err)
		}
	})

	t.Run("invalid target", func(t *testing.T) {
		h := newHarness(t)
		h.seedIcon(fixtureIconDoc, 0, true)
		h.seedVerifier(verifierSettings(true, true))
		if _, err := h.createRequest(fixtureOwner, chanPeer(1234), ""); !errors.Is(err, domain.ErrCustomVerificationTargetInvalid) {
			t.Fatalf("err = %v, want ErrCustomVerificationTargetInvalid", err)
		}
	})

	t.Run("applicant does not control the channel", func(t *testing.T) {
		h := newHarness(t)
		h.seedIcon(fixtureIconDoc, 0, true)
		h.seedVerifier(verifierSettings(true, true))
		if _, err := h.createRequest(fixtureStranger, chanPeer(fixtureChannel), ""); !errors.Is(err, domain.ErrVerificationNotOwner) {
			t.Fatalf("err = %v, want ErrVerificationNotOwner", err)
		}
	})

	t.Run("plain member does not control the channel", func(t *testing.T) {
		h := newHarness(t)
		h.seedIcon(fixtureIconDoc, 0, true)
		h.seedVerifier(verifierSettings(true, true))
		h.dir.members[memberKey{channelID: fixtureChannel, userID: fixtureStranger}] = domain.ChannelMember{
			ChannelID: fixtureChannel,
			UserID:    fixtureStranger,
			Role:      domain.ChannelRoleMember,
			Status:    domain.ChannelMemberActive,
		}
		if _, err := h.createRequest(fixtureStranger, chanPeer(fixtureChannel), ""); !errors.Is(err, domain.ErrVerificationNotOwner) {
			t.Fatalf("err = %v, want ErrVerificationNotOwner", err)
		}
	})

	t.Run("administrator with change_info controls the channel", func(t *testing.T) {
		h := newHarness(t)
		h.seedIcon(fixtureIconDoc, 0, true)
		h.seedVerifier(verifierSettings(true, true))
		h.dir.members[memberKey{channelID: fixtureChannel, userID: fixtureStranger}] = domain.ChannelMember{
			ChannelID:   fixtureChannel,
			UserID:      fixtureStranger,
			Role:        domain.ChannelRoleAdmin,
			Status:      domain.ChannelMemberActive,
			AdminRights: domain.ChannelAdminRights{ChangeInfo: true},
		}
		if _, err := h.createRequest(fixtureStranger, chanPeer(fixtureChannel), ""); err != nil {
			t.Fatalf("CreateRequest by a change_info admin: %v", err)
		}
	})

	t.Run("bot owner controls the bot", func(t *testing.T) {
		h := newHarness(t)
		h.seedIcon(fixtureIconDoc, 0, true)
		h.seedVerifier(verifierSettings(true, true))
		if _, err := h.createRequest(fixtureOwner, userPeer(fixtureOwnedBot), ""); err != nil {
			t.Fatalf("CreateRequest for an owned bot: %v", err)
		}
	})

	t.Run("stranger does not control the bot", func(t *testing.T) {
		h := newHarness(t)
		h.seedIcon(fixtureIconDoc, 0, true)
		h.seedVerifier(verifierSettings(true, true))
		if _, err := h.createRequest(fixtureStranger, userPeer(fixtureOwnedBot), ""); !errors.Is(err, domain.ErrVerificationNotOwner) {
			t.Fatalf("err = %v, want ErrVerificationNotOwner", err)
		}
	})

	t.Run("user files for their own account", func(t *testing.T) {
		h := newHarness(t)
		h.seedIcon(fixtureIconDoc, 0, true)
		h.seedVerifier(verifierSettings(true, true))
		if _, err := h.createRequest(fixtureUserPeer, userPeer(fixtureUserPeer), ""); err != nil {
			t.Fatalf("CreateRequest for own account: %v", err)
		}
	})

	t.Run("peer already marked by this verifier", func(t *testing.T) {
		h := newHarness(t)
		h.seedIcon(fixtureIconDoc, 0, true)
		h.seedVerifier(verifierSettings(true, true))
		if _, _, err := h.store.GrantCustomVerification(context.Background(), domain.CustomVerification{
			VerifierBotID:  fixtureVerifierBot,
			Peer:           chanPeer(fixtureChannel),
			IconDocumentID: fixtureIconDoc,
			Description:    "Verified by Acme",
		}); err != nil {
			t.Fatalf("seed mark: %v", err)
		}
		if _, err := h.createRequest(fixtureOwner, chanPeer(fixtureChannel), ""); !errors.Is(err, domain.ErrCustomVerificationRequestExists) {
			t.Fatalf("err = %v, want ErrCustomVerificationRequestExists", err)
		}
	})

	t.Run("second pending application", func(t *testing.T) {
		h := newHarness(t)
		h.seedIcon(fixtureIconDoc, 0, true)
		h.seedVerifier(verifierSettings(true, true))
		if _, err := h.createRequest(fixtureOwner, chanPeer(fixtureChannel), ""); err != nil {
			t.Fatalf("first CreateRequest: %v", err)
		}
		if _, err := h.createRequest(fixtureOwner, chanPeer(fixtureChannel), ""); !errors.Is(err, domain.ErrCustomVerificationRequestExists) {
			t.Fatalf("err = %v, want ErrCustomVerificationRequestExists", err)
		}
	})

	t.Run("description this verifier may not apply", func(t *testing.T) {
		h := newHarness(t)
		h.seedIcon(fixtureIconDoc, 0, true)
		h.seedVerifier(verifierSettings(true, false))
		if _, err := h.createRequest(fixtureOwner, chanPeer(fixtureChannel), "our own wording"); !errors.Is(err, domain.ErrVerifierDescriptionForbidden) {
			t.Fatalf("err = %v, want ErrVerifierDescriptionForbidden", err)
		}
	})

	t.Run("rate limited", func(t *testing.T) {
		h := newHarness(t)
		h.seedIcon(fixtureIconDoc, 0, true)
		h.seedVerifier(verifierSettings(true, true))
		h.limiter.allow = false

		if _, err := h.createRequest(fixtureOwner, chanPeer(fixtureChannel), ""); !errors.Is(err, domain.ErrVerificationRateLimited) {
			t.Fatalf("err = %v, want ErrVerificationRateLimited", err)
		}
		if len(h.limiter.keys) != 1 || !strings.HasPrefix(h.limiter.keys[0], requestRateLimitKeyPrefix) {
			t.Fatalf("limiter keys = %v, want one namespaced per-applicant key", h.limiter.keys)
		}
		pending, err := h.store.ListCustomVerificationRequests(context.Background(), domain.CustomVerificationRequestFilter{})
		if err != nil {
			t.Fatalf("ListCustomVerificationRequests: %v", err)
		}
		if len(pending) != 0 {
			t.Fatalf("stored applications = %+v, want none when the budget is spent", pending)
		}
	})

	t.Run("a refused application spends no budget", func(t *testing.T) {
		h := newHarness(t)
		h.seedIcon(fixtureIconDoc, 0, true)
		h.seedVerifier(verifierSettings(true, true))

		if _, err := h.createRequest(fixtureStranger, chanPeer(fixtureChannel), ""); !errors.Is(err, domain.ErrVerificationNotOwner) {
			t.Fatalf("err = %v, want ErrVerificationNotOwner", err)
		}
		if len(h.limiter.keys) != 0 {
			t.Fatalf("limiter keys = %v, want the budget untouched by a refusal", h.limiter.keys)
		}
	})

	t.Run("snapshot comes from the directory", func(t *testing.T) {
		h := newHarness(t)
		h.seedIcon(fixtureIconDoc, 0, true)
		h.seedVerifier(verifierSettings(true, true))

		stored, err := h.createRequest(fixtureOwner, chanPeer(fixtureChannel), "")
		if err != nil {
			t.Fatalf("CreateRequest: %v", err)
		}
		if stored.PeerTitle != "Acme Channel" || stored.PeerUsername != "acmechannel" {
			t.Fatalf("snapshot = %q/%q, want the resolved channel identity", stored.PeerTitle, stored.PeerUsername)
		}
		if stored.Status != domain.CustomVerificationPending || stored.Version != 1 {
			t.Fatalf("stored = %+v, want a pending first version", stored)
		}
	})
}

// TestApproveGrantsMarkAndNotifiesOnce is the decision contract: the mark and the
// approval land together, each notifier fires exactly once, and a retry does
// neither again.
func TestApproveGrantsMarkAndNotifiesOnce(t *testing.T) {
	h := newHarness(t)
	h.seedIcon(fixtureIconDoc, 0, true)
	h.seedVerifier(verifierSettings(true, true))
	ctx := context.Background()

	filed, err := h.createRequest(fixtureOwner, chanPeer(fixtureChannel), "Acme partner")
	if err != nil {
		t.Fatalf("CreateRequest: %v", err)
	}

	decided, changed, err := h.svc.Approve(ctx, filed.ID, filed.Version, "operator", "checked the docs", "internal note")
	if err != nil || !changed {
		t.Fatalf("Approve = %v/%v, want true/nil", changed, err)
	}
	if decided.Status != domain.CustomVerificationApproved {
		t.Fatalf("status = %q, want approved", decided.Status)
	}
	mark, found := h.mark(fixtureVerifierBot, chanPeer(fixtureChannel))
	if !found {
		t.Fatal("approval did not grant the mark")
	}
	if mark.IconDocumentID != fixtureIconDoc || mark.Description != "Acme partner" {
		t.Fatalf("mark = %+v, want the verifier icon and the requested description", mark)
	}
	if h.peers.count() != 1 || h.peers.peers[0] != chanPeer(fixtureChannel) {
		t.Fatalf("peer pushes = %v, want exactly one for the target", h.peers.peers)
	}
	if len(h.applicant.calls) != 1 {
		t.Fatalf("applicant notifications = %d, want 1", len(h.applicant.calls))
	}
	if h.applicant.calls[0].recipientUserID != fixtureOwner ||
		h.applicant.calls[0].request.Status != domain.CustomVerificationApproved {
		t.Fatalf("applicant call = %+v, want the approved decision to the applicant", h.applicant.calls[0])
	}

	// Retried approval: already approved, so nothing is written and nobody is told
	// twice.
	again, changed, err := h.svc.Approve(ctx, filed.ID, decided.Version, "operator", "checked the docs", "")
	if err != nil {
		t.Fatalf("repeat Approve: %v", err)
	}
	if changed {
		t.Fatal("repeat Approve reported changed = true")
	}
	if again.Version != decided.Version {
		t.Fatalf("version %d -> %d on a repeat approval", decided.Version, again.Version)
	}
	if h.peers.count() != 1 || len(h.applicant.calls) != 1 {
		t.Fatalf("notifications after the repeat: %d peer / %d applicant, want 1/1", h.peers.count(), len(h.applicant.calls))
	}
}

func TestApproveRefusesDisabledVerifier(t *testing.T) {
	h := newHarness(t)
	h.seedIcon(fixtureIconDoc, 0, true)
	h.seedVerifier(verifierSettings(true, true))
	ctx := context.Background()

	filed, err := h.createRequest(fixtureOwner, chanPeer(fixtureChannel), "")
	if err != nil {
		t.Fatalf("CreateRequest: %v", err)
	}
	// The operator switches the verifier off while the application waits.
	if _, err := h.svc.SetVerifierEnabled(ctx, fixtureVerifierBot, false); err != nil {
		t.Fatalf("SetVerifierEnabled: %v", err)
	}

	if _, _, err := h.svc.Approve(ctx, filed.ID, filed.Version, "operator", "", ""); !errors.Is(err, domain.ErrVerifierForbidden) {
		t.Fatalf("err = %v, want ErrVerifierForbidden", err)
	}
	if _, found := h.mark(fixtureVerifierBot, chanPeer(fixtureChannel)); found {
		t.Fatal("a disabled verifier granted a mark through the review path")
	}
	if stored, err := h.store.CustomVerificationRequest(ctx, filed.ID); err != nil || stored.Status != domain.CustomVerificationPending {
		t.Fatalf("application = %+v / %v, want it still pending", stored, err)
	}
}

func TestApproveIsVersionChecked(t *testing.T) {
	h := newHarness(t)
	h.seedIcon(fixtureIconDoc, 0, true)
	h.seedVerifier(verifierSettings(true, true))
	ctx := context.Background()

	filed, err := h.createRequest(fixtureOwner, chanPeer(fixtureChannel), "")
	if err != nil {
		t.Fatalf("CreateRequest: %v", err)
	}
	if _, _, err := h.svc.Approve(ctx, filed.ID, filed.Version+1, "operator", "", ""); !errors.Is(err, domain.ErrCustomVerificationVersionConflict) {
		t.Fatalf("err = %v, want ErrCustomVerificationVersionConflict", err)
	}
	if _, found := h.mark(fixtureVerifierBot, chanPeer(fixtureChannel)); found {
		t.Fatal("a stale approval granted the mark")
	}
}

func TestRejectRequiresAReason(t *testing.T) {
	h := newHarness(t)
	h.seedIcon(fixtureIconDoc, 0, true)
	h.seedVerifier(verifierSettings(true, true))
	ctx := context.Background()

	filed, err := h.createRequest(fixtureOwner, chanPeer(fixtureChannel), "")
	if err != nil {
		t.Fatalf("CreateRequest: %v", err)
	}

	if _, _, err := h.svc.Reject(ctx, filed.ID, filed.Version, "operator", "   ", "note"); !errors.Is(err, domain.ErrVerificationReasonRequired) {
		t.Fatalf("err = %v, want ErrVerificationReasonRequired", err)
	}
	if stored, err := h.store.CustomVerificationRequest(ctx, filed.ID); err != nil || stored.Status != domain.CustomVerificationPending {
		t.Fatalf("application = %+v / %v, want it untouched", stored, err)
	}
	if len(h.applicant.calls) != 0 {
		t.Fatalf("applicant notifications = %d, want none", len(h.applicant.calls))
	}

	decided, changed, err := h.svc.Reject(ctx, filed.ID, filed.Version, "operator", "not a partner", "note")
	if err != nil || !changed {
		t.Fatalf("Reject = %v/%v, want true/nil", changed, err)
	}
	if decided.Status != domain.CustomVerificationRejected || decided.DecisionReason != "not a partner" {
		t.Fatalf("decided = %+v, want a rejection carrying its reason", decided)
	}
	// A rejection touches no peer state, so only the applicant hears about it.
	if h.peers.count() != 0 {
		t.Fatalf("peer pushes = %d, want none for a rejection", h.peers.count())
	}
	if len(h.applicant.calls) != 1 || h.applicant.calls[0].request.Status != domain.CustomVerificationRejected {
		t.Fatalf("applicant calls = %+v, want one rejection notice", h.applicant.calls)
	}
	if _, found := h.mark(fixtureVerifierBot, chanPeer(fixtureChannel)); found {
		t.Fatal("a rejection granted a mark")
	}
}

// TestDecisionsWriteThroughTheMarkApplier pins the wiring the atomicity of a
// decision depends on: the grant and the revocation both go through the injected
// writer, which in production is the one that picks the decision's transaction out
// of the context.
func TestDecisionsWriteThroughTheMarkApplier(t *testing.T) {
	h := newHarness(t)
	applier := &fakeMarkApplier{store: h.store}
	h.svc = NewService(
		WithStore(h.store),
		WithPeerDirectory(h.dir),
		WithPeerNotifier(h.peers),
		WithIconResolver(h.docs),
		WithApplicantNotifier(h.applicant),
		WithRateLimiter(h.limiter, 5, 24*time.Hour),
		WithMarkApplier(applier),
	)
	h.seedIcon(fixtureIconDoc, 0, true)
	h.seedVerifier(verifierSettings(true, true))
	ctx := context.Background()

	filed, err := h.createRequest(fixtureOwner, chanPeer(fixtureChannel), "")
	if err != nil {
		t.Fatalf("CreateRequest: %v", err)
	}
	approved, _, err := h.svc.Approve(ctx, filed.ID, filed.Version, "operator", "ok", "")
	if err != nil {
		t.Fatalf("Approve: %v", err)
	}
	if applier.grants != 1 {
		t.Fatalf("applier grants = %d, want 1", applier.grants)
	}
	if _, _, err := h.svc.RevokeRequest(ctx, approved.ID, approved.Version, "operator", "ended", ""); err != nil {
		t.Fatalf("RevokeRequest: %v", err)
	}
	if applier.revokes != 1 {
		t.Fatalf("applier revokes = %d, want 1", applier.revokes)
	}
	// The plain RPC path is not a decision, so it does not go through the applier.
	if _, err := h.svc.SetCustomVerification(ctx, domain.SetCustomVerificationRequest{
		VerifierBotID: fixtureVerifierBot,
		Peer:          userPeer(fixtureUserPeer),
		Enabled:       true,
		CallerUserID:  fixtureVerifierBot,
	}); err != nil {
		t.Fatalf("SetCustomVerification: %v", err)
	}
	if applier.grants != 1 {
		t.Fatalf("applier grants = %d after the RPC path, want it untouched", applier.grants)
	}
}

func TestRevokeRequestRemovesTheMark(t *testing.T) {
	h := newHarness(t)
	h.seedIcon(fixtureIconDoc, 0, true)
	h.seedVerifier(verifierSettings(true, true))
	ctx := context.Background()

	filed, err := h.createRequest(fixtureOwner, chanPeer(fixtureChannel), "")
	if err != nil {
		t.Fatalf("CreateRequest: %v", err)
	}
	approved, _, err := h.svc.Approve(ctx, filed.ID, filed.Version, "operator", "ok", "")
	if err != nil {
		t.Fatalf("Approve: %v", err)
	}

	if _, _, err := h.svc.RevokeRequest(ctx, approved.ID, approved.Version, "operator", "", ""); !errors.Is(err, domain.ErrVerificationReasonRequired) {
		t.Fatalf("err = %v, want ErrVerificationReasonRequired", err)
	}

	revoked, changed, err := h.svc.RevokeRequest(ctx, approved.ID, approved.Version, "operator", "partner agreement ended", "")
	if err != nil || !changed {
		t.Fatalf("RevokeRequest = %v/%v, want true/nil", changed, err)
	}
	if revoked.Status != domain.CustomVerificationRevoked {
		t.Fatalf("status = %q, want revoked", revoked.Status)
	}
	if _, found := h.mark(fixtureVerifierBot, chanPeer(fixtureChannel)); found {
		t.Fatal("the mark survived the revocation")
	}
	if h.peers.count() != 2 {
		t.Fatalf("peer pushes = %d, want 2 (approve + revoke)", h.peers.count())
	}
	if len(h.applicant.calls) != 2 || h.applicant.calls[1].request.Status != domain.CustomVerificationRevoked {
		t.Fatalf("applicant calls = %+v, want an approval and a revocation notice", h.applicant.calls)
	}
}

func TestRevokeMarkAndListings(t *testing.T) {
	h := newHarness(t)
	h.seedIcon(fixtureIconDoc, 0, true)
	h.seedVerifier(verifierSettings(true, true))
	ctx := context.Background()

	if _, err := h.svc.SetCustomVerification(ctx, domain.SetCustomVerificationRequest{
		VerifierBotID: fixtureVerifierBot,
		Peer:          userPeer(fixtureUserPeer),
		Enabled:       true,
		CallerUserID:  fixtureVerifierBot,
	}); err != nil {
		t.Fatalf("SetCustomVerification: %v", err)
	}

	marks, err := h.svc.Marks(ctx, domain.CustomVerificationFilter{VerifierBotID: fixtureVerifierBot})
	if err != nil {
		t.Fatalf("Marks: %v", err)
	}
	if len(marks) != 1 {
		t.Fatalf("Marks = %+v, want one", marks)
	}
	verifiers, err := h.svc.Verifiers(ctx, true, 0)
	if err != nil {
		t.Fatalf("Verifiers: %v", err)
	}
	if len(verifiers) != 1 || verifiers[0].BotID != fixtureVerifierBot {
		t.Fatalf("Verifiers = %+v, want the one enabled verifier", verifiers)
	}

	removed, err := h.svc.RevokeMark(ctx, fixtureVerifierBot, userPeer(fixtureUserPeer))
	if err != nil || !removed {
		t.Fatalf("RevokeMark = %v/%v, want true/nil", removed, err)
	}
	removed, err = h.svc.RevokeMark(ctx, fixtureVerifierBot, userPeer(fixtureUserPeer))
	if err != nil || removed {
		t.Fatalf("repeat RevokeMark = %v/%v, want false/nil", removed, err)
	}
	if _, err := h.svc.Marks(ctx, domain.CustomVerificationFilter{PeerType: domain.PeerTypeCommunity}); !errors.Is(err, domain.ErrCustomVerificationTargetInvalid) {
		t.Fatalf("Marks with an unmarkable peer type: %v", err)
	}
}

func TestProjectionAndQueueReads(t *testing.T) {
	h := newHarness(t)
	h.seedIcon(fixtureIconDoc, 0, true)
	h.seedVerifier(verifierSettings(true, true))
	ctx := context.Background()

	filed, err := h.createRequest(fixtureOwner, chanPeer(fixtureChannel), "")
	if err != nil {
		t.Fatalf("CreateRequest: %v", err)
	}
	if _, _, err := h.svc.Approve(ctx, filed.ID, filed.Version, "operator", "ok", ""); err != nil {
		t.Fatalf("Approve: %v", err)
	}

	mark, err := h.svc.PeerVerification(ctx, chanPeer(fixtureChannel))
	if err != nil {
		t.Fatalf("PeerVerification: %v", err)
	}
	if mark.Projection().Icon != fixtureIconDoc || mark.Projection().BotID != fixtureVerifierBot {
		t.Fatalf("projection = %+v", mark.Projection())
	}
	batch, err := h.svc.PeerVerificationBatch(ctx, []domain.Peer{chanPeer(fixtureChannel), userPeer(fixtureUserPeer)})
	if err != nil {
		t.Fatalf("PeerVerificationBatch: %v", err)
	}
	if len(batch) != 1 {
		t.Fatalf("batch = %+v, want only the marked peer", batch)
	}
	settings, err := h.svc.VerifierSettings(ctx, fixtureVerifierBot)
	if err != nil || settings.BotID != fixtureVerifierBot {
		t.Fatalf("VerifierSettings = %+v / %v", settings, err)
	}
	settingsBatch, err := h.svc.VerifierSettingsBatch(ctx, []int64{fixtureVerifierBot, fixtureOwnedBot})
	if err != nil {
		t.Fatalf("VerifierSettingsBatch: %v", err)
	}
	if len(settingsBatch) != 1 {
		t.Fatalf("settings batch = %+v, want only the verifier", settingsBatch)
	}

	counts, err := h.svc.RequestCounts(ctx)
	if err != nil {
		t.Fatalf("RequestCounts: %v", err)
	}
	if counts[domain.CustomVerificationApproved] != 1 {
		t.Fatalf("counts = %+v, want one approved", counts)
	}
	history, err := h.svc.ApplicantRequests(ctx, fixtureOwner, 0)
	if err != nil || len(history) != 1 {
		t.Fatalf("ApplicantRequests = %+v / %v", history, err)
	}
	if _, err := h.svc.Request(ctx, filed.ID); err != nil {
		t.Fatalf("Request: %v", err)
	}
	if _, err := h.svc.PendingRequest(ctx, fixtureVerifierBot, chanPeer(fixtureChannel)); !errors.Is(err, domain.ErrCustomVerificationRequestNotFound) {
		t.Fatalf("PendingRequest after the decision: %v, want not found", err)
	}
	if _, err := h.svc.Requests(ctx, domain.CustomVerificationRequestFilter{Statuses: []domain.CustomVerificationRequestStatus{"nonsense"}}); !errors.Is(err, domain.ErrCustomVerificationRequestInvalid) {
		t.Fatalf("Requests with a bogus status: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Configuration
// ---------------------------------------------------------------------------

// TestDisabledConfigRefusesMutations pins the flag's scope: every write refuses
// with ErrDisabled, while the marks already granted keep projecting so a peer's
// badge does not silently vanish when an operator turns the feature off.
func TestDisabledConfigRefusesMutations(t *testing.T) {
	h := newHarness(t, WithEnabled(false))
	h.seedIcon(fixtureIconDoc, 0, true)
	h.seedVerifier(verifierSettings(true, true))
	ctx := context.Background()
	if _, _, err := h.store.GrantCustomVerification(ctx, domain.CustomVerification{
		VerifierBotID:  fixtureVerifierBot,
		Peer:           userPeer(fixtureUserPeer),
		IconDocumentID: fixtureIconDoc,
		Description:    "Verified by Acme",
	}); err != nil {
		t.Fatalf("seed mark: %v", err)
	}

	if h.svc.Enabled() || h.svc.Ready() {
		t.Fatal("a disabled service reports itself enabled")
	}
	for name, call := range map[string]func() error{
		"SetCustomVerification": func() error {
			_, err := h.svc.SetCustomVerification(ctx, domain.SetCustomVerificationRequest{
				VerifierBotID: fixtureVerifierBot,
				Peer:          chanPeer(fixtureChannel),
				Enabled:       true,
				CallerUserID:  fixtureVerifierBot,
			})
			return err
		},
		"GrantVerifier": func() error {
			_, err := h.svc.GrantVerifier(ctx, verifierSettings(true, true))
			return err
		},
		"SetVerifierEnabled": func() error {
			_, err := h.svc.SetVerifierEnabled(ctx, fixtureVerifierBot, false)
			return err
		},
		"RevokeVerifier": func() error {
			_, err := h.svc.RevokeVerifier(ctx, fixtureVerifierBot)
			return err
		},
		"UpsertIcon": func() error {
			_, err := h.svc.UpsertIcon(ctx, domain.VerificationIcon{DocumentID: fixtureIconDoc, Name: "Acme", Active: true})
			return err
		},
		"SetIconActive": func() error {
			_, err := h.svc.SetIconActive(ctx, 1, false)
			return err
		},
		"RevokeMark": func() error {
			_, err := h.svc.RevokeMark(ctx, fixtureVerifierBot, userPeer(fixtureUserPeer))
			return err
		},
		"CreateRequest": func() error {
			_, err := h.createRequest(fixtureOwner, chanPeer(fixtureChannel), "")
			return err
		},
		"Approve": func() error {
			_, _, err := h.svc.Approve(ctx, 1, 1, "operator", "", "")
			return err
		},
		"Reject": func() error {
			_, _, err := h.svc.Reject(ctx, 1, 1, "operator", "no", "")
			return err
		},
		"RevokeRequest": func() error {
			_, _, err := h.svc.RevokeRequest(ctx, 1, 1, "operator", "no", "")
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := call(); !errors.Is(err, ErrDisabled) {
				t.Fatalf("err = %v, want ErrDisabled", err)
			}
		})
	}
	if h.peers.count() != 0 || len(h.applicant.calls) != 0 {
		t.Fatalf("a disabled service notified: %d peer / %d applicant", h.peers.count(), len(h.applicant.calls))
	}

	// Reads still answer: the badge already granted keeps rendering, and the panel
	// can still audit what exists.
	if _, err := h.svc.PeerVerification(ctx, userPeer(fixtureUserPeer)); err != nil {
		t.Fatalf("PeerVerification while disabled: %v", err)
	}
	if icons, err := h.svc.Icons(ctx, false, 0); err != nil || len(icons) != 1 {
		t.Fatalf("Icons while disabled = %+v / %v", icons, err)
	}
	if verifiers, err := h.svc.Verifiers(ctx, false, 0); err != nil || len(verifiers) != 1 {
		t.Fatalf("Verifiers while disabled = %+v / %v", verifiers, err)
	}
}

// TestNilAndUnconfiguredServiceAreSafe covers the two shapes a process can hand
// out by accident: no service at all, and a service with no store.
func TestNilAndUnconfiguredServiceAreSafe(t *testing.T) {
	ctx := context.Background()
	var nilService *Service

	if nilService.Enabled() || nilService.Ready() {
		t.Fatal("a nil service reports itself enabled")
	}
	if _, err := nilService.PeerVerification(ctx, userPeer(fixtureUserPeer)); !errors.Is(err, domain.ErrCustomVerificationNotFound) {
		t.Fatalf("nil PeerVerification: %v, want ErrCustomVerificationNotFound", err)
	}
	batch, err := nilService.PeerVerificationBatch(ctx, []domain.Peer{userPeer(fixtureUserPeer)})
	if err != nil || len(batch) != 0 {
		t.Fatalf("nil PeerVerificationBatch = %+v / %v, want an empty map", batch, err)
	}
	if _, err := nilService.VerifierSettings(ctx, fixtureVerifierBot); !errors.Is(err, domain.ErrVerifierNotFound) {
		t.Fatalf("nil VerifierSettings: %v, want ErrVerifierNotFound", err)
	}
	if settings, err := nilService.VerifierSettingsBatch(ctx, []int64{fixtureVerifierBot}); err != nil || len(settings) != 0 {
		t.Fatalf("nil VerifierSettingsBatch = %+v / %v", settings, err)
	}
	if _, err := nilService.SetCustomVerification(ctx, domain.SetCustomVerificationRequest{}); !errors.Is(err, ErrDisabled) {
		t.Fatalf("nil SetCustomVerification: %v, want ErrDisabled", err)
	}
	// Nil notifier hooks are ignored rather than panicking.
	nilService.SetPeerNotifier(&fakePeerNotifier{})
	nilService.SetApplicantNotifier(&fakeApplicantNotifier{})

	// Enabled but storeless: every entry point says what is missing instead of
	// dereferencing nil.
	bare := NewService(WithPeerDirectory(&fakeDirectory{}), WithIconResolver(&fakeIconResolver{}))
	if !bare.Enabled() || bare.Ready() {
		t.Fatal("a storeless service reports itself ready")
	}
	for name, call := range map[string]func() error{
		"SetCustomVerification": func() error {
			_, err := bare.SetCustomVerification(ctx, domain.SetCustomVerificationRequest{
				VerifierBotID: fixtureVerifierBot,
				Peer:          userPeer(fixtureUserPeer),
				Enabled:       true,
				CallerUserID:  fixtureVerifierBot,
			})
			return err
		},
		"GrantVerifier": func() error {
			_, err := bare.GrantVerifier(ctx, verifierSettings(true, true))
			return err
		},
		"Icons": func() error {
			_, err := bare.Icons(ctx, false, 0)
			return err
		},
		"Requests": func() error {
			_, err := bare.Requests(ctx, domain.CustomVerificationRequestFilter{})
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			err := call()
			if err == nil || !strings.Contains(err.Error(), "store is not configured") {
				t.Fatalf("err = %v, want the store configuration error", err)
			}
		})
	}
}

// TestSetCustomVerificationWithoutDirectoryFails pins that a security check which
// cannot run refuses instead of passing.
func TestSetCustomVerificationWithoutDirectoryFails(t *testing.T) {
	st := memory.NewBotVerificationStore()
	if _, err := st.UpsertVerificationIcon(context.Background(), domain.VerificationIcon{DocumentID: fixtureIconDoc, Name: "Acme", Active: true}); err != nil {
		t.Fatalf("seed icon: %v", err)
	}
	if _, err := st.UpsertBotVerifierSettings(context.Background(), verifierSettings(true, true)); err != nil {
		t.Fatalf("seed verifier: %v", err)
	}
	svc := NewService(WithStore(st))

	_, err := svc.SetCustomVerification(context.Background(), domain.SetCustomVerificationRequest{
		VerifierBotID: fixtureVerifierBot,
		Peer:          userPeer(fixtureUserPeer),
		Enabled:       true,
		// Not the bot itself, so ownership has to be resolved -- and cannot be.
		CallerUserID: fixtureOwner,
	})
	if err == nil || !strings.Contains(err.Error(), "bot directory is not configured") {
		t.Fatalf("err = %v, want the bot directory configuration error", err)
	}
}
