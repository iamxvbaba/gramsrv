package bots

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	verificationapp "telesrv/internal/app/verification"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

// ---------------------------------------------------------------------------
// Fake verification service
// ---------------------------------------------------------------------------

// fakeVerification is an in-memory stand-in for app/verification.Service. It
// keeps the properties the bot dialog actually leans on: one draft per applicant,
// StartDraft resuming instead of duplicating, optimistic-locking versions, and
// the domain validation of the payload.
type fakeVerification struct {
	targets    []domain.VerificationTarget
	apps       map[int64]domain.VerificationApplication
	nextID     int64
	starts     int
	submits    int
	targetsErr error
	startErr   error
}

func newFakeVerification(targets ...domain.VerificationTarget) *fakeVerification {
	return &fakeVerification{
		targets: targets,
		apps:    make(map[int64]domain.VerificationApplication),
		nextID:  100,
	}
}

func (f *fakeVerification) EligibleTargets(_ context.Context, applicantUserID int64) ([]domain.VerificationTarget, error) {
	if f.targetsErr != nil {
		return nil, f.targetsErr
	}
	if applicantUserID <= 0 {
		return nil, domain.ErrVerificationApplicationInvalid
	}
	return append([]domain.VerificationTarget(nil), f.targets...), nil
}

func (f *fakeVerification) draftFor(applicantUserID int64) (domain.VerificationApplication, bool) {
	for _, app := range f.apps {
		if app.ApplicantUserID == applicantUserID && app.Status == domain.VerificationStatusDraft {
			return app, true
		}
	}
	return domain.VerificationApplication{}, false
}

func (f *fakeVerification) StartDraft(_ context.Context, req domain.SubmitVerificationApplicationRequest) (domain.VerificationApplication, bool, error) {
	f.starts++
	if app, found := f.draftFor(req.ApplicantUserID); found {
		return app, false, nil
	}
	if f.startErr != nil {
		return domain.VerificationApplication{}, false, f.startErr
	}
	var target domain.VerificationTarget
	for _, candidate := range f.targets {
		if candidate.Type == req.TargetType && candidate.ID == req.TargetID {
			target = candidate
		}
	}
	if target.ID == 0 {
		return domain.VerificationApplication{}, false, domain.ErrVerificationTargetInvalid
	}
	if !target.Eligible {
		return domain.VerificationApplication{}, false, domain.ErrVerificationTargetAlreadyVerified
	}
	f.nextID++
	app := domain.VerificationApplication{
		ID:              f.nextID,
		ApplicantUserID: req.ApplicantUserID,
		TargetType:      target.Type,
		TargetID:        target.ID,
		TargetTitle:     target.Title,
		TargetUsername:  target.Username,
		Status:          domain.VerificationStatusDraft,
		CreatedAt:       time.Date(2026, 7, 26, 10, 0, 0, 0, time.UTC),
		Version:         1,
	}
	f.apps[app.ID] = app
	return app, true, nil
}

func (f *fakeVerification) SaveDraft(_ context.Context, applicantUserID, applicationID, version int64, draft domain.VerificationDraftInput) (domain.VerificationApplication, error) {
	app, found := f.apps[applicationID]
	if !found || app.ApplicantUserID != applicantUserID {
		return domain.VerificationApplication{}, domain.ErrVerificationApplicationNotFound
	}
	if app.Version != version {
		return domain.VerificationApplication{}, domain.ErrVerificationVersionConflict
	}
	if app.Status != domain.VerificationStatusDraft {
		return domain.VerificationApplication{}, domain.ErrVerificationStatusInvalid
	}
	if err := draft.ValidateDraft(); err != nil {
		return domain.VerificationApplication{}, err
	}
	draft = draft.Normalize()
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

func (f *fakeVerification) Submit(_ context.Context, applicantUserID, applicationID, version int64) (domain.VerificationApplication, error) {
	app, found := f.apps[applicationID]
	if !found || app.ApplicantUserID != applicantUserID {
		return domain.VerificationApplication{}, domain.ErrVerificationApplicationNotFound
	}
	if app.Version != version {
		return domain.VerificationApplication{}, domain.ErrVerificationVersionConflict
	}
	if !domain.CanTransitionVerificationStatus(app.Status, domain.VerificationStatusSubmitted) {
		return domain.VerificationApplication{}, domain.ErrVerificationStatusInvalid
	}
	if err := (domain.VerificationDraftInput{
		Category:        app.Category,
		Description:     app.Description,
		OfficialWebsite: app.OfficialWebsite,
		SocialLinks:     app.SocialLinks,
		PressLinks:      app.PressLinks,
		AdditionalNote:  app.AdditionalNote,
	}).ValidateForSubmission(); err != nil {
		return domain.VerificationApplication{}, err
	}
	f.submits++
	app.Status = domain.VerificationStatusSubmitted
	app.SubmittedAt = time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	app.Version++
	f.apps[applicationID] = app
	return app, nil
}

func (f *fakeVerification) Cancel(_ context.Context, applicantUserID, applicationID, version int64, reason string) (domain.VerificationApplication, error) {
	app, found := f.apps[applicationID]
	if !found || app.ApplicantUserID != applicantUserID {
		return domain.VerificationApplication{}, domain.ErrVerificationApplicationNotFound
	}
	if app.Version != version {
		return domain.VerificationApplication{}, domain.ErrVerificationVersionConflict
	}
	if !domain.CanTransitionVerificationStatus(app.Status, domain.VerificationStatusCancelled) {
		return domain.VerificationApplication{}, domain.ErrVerificationStatusInvalid
	}
	app.Status = domain.VerificationStatusCancelled
	app.DecisionReason = reason
	app.Version++
	f.apps[applicationID] = app
	return app, nil
}

func (f *fakeVerification) Draft(_ context.Context, applicantUserID int64) (domain.VerificationApplication, error) {
	if app, found := f.draftFor(applicantUserID); found {
		return app, nil
	}
	return domain.VerificationApplication{}, domain.ErrVerificationApplicationNotFound
}

func (f *fakeVerification) ApplicantApplications(_ context.Context, applicantUserID int64, limit int) ([]domain.VerificationApplication, error) {
	out := make([]domain.VerificationApplication, 0, len(f.apps))
	for _, app := range f.apps {
		if app.ApplicantUserID == applicantUserID {
			out = append(out, app)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (f *fakeVerification) Application(_ context.Context, applicationID int64) (domain.VerificationApplication, error) {
	if app, found := f.apps[applicationID]; found {
		return app, nil
	}
	return domain.VerificationApplication{}, domain.ErrVerificationApplicationNotFound
}

var _ verificationApplications = (*fakeVerification)(nil)

// ---------------------------------------------------------------------------
// Harness
// ---------------------------------------------------------------------------

func verifyChannelTarget() domain.VerificationTarget {
	return domain.VerificationTarget{
		Type: domain.VerificationTargetChannel, ID: 7001,
		Title: "Example News", Username: "examplenews", AccessHash: 42, Eligible: true,
	}
}

func verifyBotTarget() domain.VerificationTarget {
	return domain.VerificationTarget{
		Type: domain.VerificationTargetBot, ID: 8002,
		Title: "Example Bot", Username: "examplebot", Eligible: true,
	}
}

func newVerifyBotTestService(t *testing.T, verification verificationApplications, opts ...Option) (*Service, *memory.UserStore, *memory.MessageStore) {
	t.Helper()
	users := memory.NewUserStore()
	bots := memory.NewBotStore(users)
	dialogs := memory.NewDialogStore()
	messages := memory.NewMessageStore(dialogs)
	all := append([]Option{WithVerification(verification)}, opts...)
	return NewService(users, bots, messages, all...), users, messages
}

// verifyBotReplies returns every @verifybot message in the user's box, oldest
// first.
func verifyBotReplies(t *testing.T, messages *memory.MessageStore, userID int64) []domain.Message {
	t.Helper()
	list, err := messages.ListByUser(context.Background(), userID, domain.MessageFilter{
		HasPeer: true,
		Peer:    domain.Peer{Type: domain.PeerTypeUser, ID: domain.VerifyBotUserID},
		Limit:   200,
	})
	if err != nil {
		t.Fatalf("list @verifybot history: %v", err)
	}
	out := make([]domain.Message, 0, len(list.Messages))
	for _, msg := range list.Messages {
		if msg.From.ID == domain.VerifyBotUserID {
			out = append(out, msg)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func latestVerifyReply(t *testing.T, messages *memory.MessageStore, userID int64) domain.Message {
	t.Helper()
	replies := verifyBotReplies(t, messages, userID)
	if len(replies) == 0 {
		t.Fatal("no @verifybot reply")
	}
	latest := replies[len(replies)-1]
	// Every keyboard the bot renders must be a valid, persistable markup: the send
	// path validates before storing, so an invalid one would silently vanish.
	if err := domain.ValidateReplyMarkup(latest.ReplyMarkup); err != nil {
		t.Fatalf("reply markup invalid: %v (%+v)", err, latest.ReplyMarkup)
	}
	for _, row := range verifyInlineRows(latest) {
		for _, button := range row {
			if len(button.Data) > domain.MaxCallbackDataLen {
				t.Fatalf("callback data %q is %d bytes, limit is %d", button.Data, len(button.Data), domain.MaxCallbackDataLen)
			}
		}
	}
	return latest
}

func verifyInlineRows(msg domain.Message) [][]domain.MarkupButton {
	if msg.ReplyMarkup == nil {
		return nil
	}
	return msg.ReplyMarkup.Inline
}

// sendToVerifyBot drives the responder synchronously, bypassing the
// OnPrivateMessage goroutine dispatch for determinism (the same shortcut the
// BotFather and @Stickers tests take).
func sendToVerifyBot(t *testing.T, svc *Service, messages *memory.MessageStore, userID int64, text string) domain.Message {
	t.Helper()
	svc.respondAsVerify(userID, domain.Message{
		From: domain.Peer{Type: domain.PeerTypeUser, ID: userID},
		Peer: domain.Peer{Type: domain.PeerTypeUser, ID: domain.VerifyBotUserID},
		Body: text,
	})
	return latestVerifyReply(t, messages, userID)
}

func verifyButtonData(msg domain.Message, label string) ([]byte, bool) {
	for _, row := range verifyInlineRows(msg) {
		for _, button := range row {
			if button.Type == domain.MarkupButtonCallback && strings.Contains(button.Text, label) {
				return append([]byte(nil), button.Data...), true
			}
		}
	}
	return nil, false
}

// pressVerifyCallbackData drives the internal callback path with raw data, the
// way rpc.Router does once it has validated the click.
func pressVerifyCallbackData(t *testing.T, svc *Service, userID int64, msg domain.Message, data []byte) domain.BotCallbackAnswer {
	t.Helper()
	if len(data) > domain.MaxCallbackDataLen {
		t.Fatalf("callback data too long: %d bytes", len(data))
	}
	answer, handled, err := svc.OnCallbackQuery(context.Background(), domain.BotCallbackQuery{
		ID:        1,
		BotUserID: domain.VerifyBotUserID,
		UserID:    userID,
		Peer:      domain.Peer{Type: domain.PeerTypeUser, ID: userID},
		MessageID: msg.ID,
		Data:      data,
	})
	if err != nil {
		t.Fatalf("callback query: %v", err)
	}
	if !handled {
		t.Fatal("callback query reported unhandled for @verifybot")
	}
	return answer
}

func pressVerifyButton(t *testing.T, svc *Service, userID int64, msg domain.Message, label string) domain.BotCallbackAnswer {
	t.Helper()
	data, found := verifyButtonData(msg, label)
	if !found {
		t.Fatalf("button %q is not in the keyboard of message %d: %+v", label, msg.ID, msg.ReplyMarkup)
	}
	return pressVerifyCallbackData(t, svc, userID, msg, data)
}

const (
	verifyTestDescription = "Example News is the daily newsroom of the Example Foundation, publishing since 2015."
	verifyTestWebsite     = "https://news.example.com"
	verifyTestPressLinks  = "https://press.example.org/story-one\nhttps://media.example.net/story-two"
)

// runVerifyApplication walks the whole dialog up to (but not including) Submit and
// returns the summary message.
func runVerifyApplication(t *testing.T, svc *Service, messages *memory.MessageStore, userID int64) domain.Message {
	t.Helper()
	intro := sendToVerifyBot(t, svc, messages, userID, "/start")
	pressVerifyButton(t, svc, userID, intro, verifyApplyButtonText)
	picker := latestVerifyReply(t, messages, userID)

	pressVerifyButton(t, svc, userID, picker, "@examplenews")
	categories := latestVerifyReply(t, messages, userID)

	pressVerifyButton(t, svc, userID, categories, "Media outlet")
	if got := latestVerifyReply(t, messages, userID); !strings.Contains(got.Body, "describe the subject") {
		t.Fatalf("after category, reply = %q", got.Body)
	}

	sendToVerifyBot(t, svc, messages, userID, verifyTestDescription)
	social := sendToVerifyBot(t, svc, messages, userID, verifyTestWebsite)
	if !strings.Contains(social.Body, "social media") {
		t.Fatalf("after website, reply = %q", social.Body)
	}

	pressVerifyButton(t, svc, userID, social, verifySkipButtonText)
	if got := latestVerifyReply(t, messages, userID); !strings.Contains(got.Body, "press coverage") {
		t.Fatalf("after skipping social links, reply = %q", got.Body)
	}

	note := sendToVerifyBot(t, svc, messages, userID, verifyTestPressLinks)
	pressVerifyButton(t, svc, userID, note, verifySkipButtonText)
	return latestVerifyReply(t, messages, userID)
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestVerifyBotStartExplainsAndOffersApplyButton(t *testing.T) {
	fake := newFakeVerification(verifyChannelTarget())
	svc, users, messages := newVerifyBotTestService(t, fake)
	owner := newOwner(t, users, "+7100")

	if !svc.HandlesBot(domain.VerifyBotUserID) {
		t.Fatal("service should handle @verifybot")
	}
	reply := sendToVerifyBot(t, svc, messages, owner.ID, "/start")
	for _, want := range []string{"official", "public @username", "/new", "/help"} {
		if !strings.Contains(reply.Body, want) {
			t.Fatalf("/start reply missing %q: %q", want, reply.Body)
		}
	}
	data, found := verifyButtonData(reply, verifyApplyButtonText)
	if !found {
		t.Fatalf("/start reply has no apply button: %+v", reply.ReplyMarkup)
	}
	if !strings.HasPrefix(string(data), verifyCallbackDataPrefix) {
		t.Fatalf("callback data %q is not a @verifybot token", data)
	}
	if fake.starts != 0 {
		t.Fatalf("StartDraft called %d times on /start, want 0", fake.starts)
	}
}

func TestVerifyBotFullApplicationFlowFilesExactlyOneApplication(t *testing.T) {
	fake := newFakeVerification(verifyChannelTarget(), verifyBotTarget())
	svc, users, messages := newVerifyBotTestService(t, fake)
	owner := newOwner(t, users, "+7101")

	summary := runVerifyApplication(t, svc, messages, owner.ID)
	for _, want := range []string{"Example News", "Media outlet", verifyTestWebsite, "press.example.org/story-one", verifySubmitButtonText} {
		if !strings.Contains(summary.Body, want) {
			t.Fatalf("summary missing %q: %q", want, summary.Body)
		}
	}

	pressVerifyButton(t, svc, owner.ID, summary, verifySubmitButtonText)
	filed := latestVerifyReply(t, messages, owner.ID)
	if !strings.Contains(filed.Body, "#101") || !strings.Contains(filed.Body, "/status") {
		t.Fatalf("submitted reply = %q", filed.Body)
	}
	if fake.submits != 1 || len(fake.apps) != 1 {
		t.Fatalf("submits=%d applications=%d, want exactly one of each", fake.submits, len(fake.apps))
	}
	app := fake.apps[101]
	if app.Status != domain.VerificationStatusSubmitted {
		t.Fatalf("application status = %q", app.Status)
	}
	if app.Category != "media" || app.OfficialWebsite != verifyTestWebsite || len(app.PressLinks) != 2 {
		t.Fatalf("stored application = %+v", app)
	}
	if app.TargetID != 7001 || app.TargetType != domain.VerificationTargetChannel {
		t.Fatalf("stored target = %s/%d", app.TargetType, app.TargetID)
	}
}

// The target buttons must not leak the peer they stand for: the whole point of the
// token table is that a click cannot name a peer at all.
func TestVerifyBotCallbackDataCarriesNoTargetIdentity(t *testing.T) {
	target := verifyChannelTarget()
	fake := newFakeVerification(target)
	svc, users, messages := newVerifyBotTestService(t, fake)
	owner := newOwner(t, users, "+7102")

	intro := sendToVerifyBot(t, svc, messages, owner.ID, "/start")
	pressVerifyButton(t, svc, owner.ID, intro, verifyApplyButtonText)
	picker := latestVerifyReply(t, messages, owner.ID)

	buttons := 0
	for _, row := range verifyInlineRows(picker) {
		for _, button := range row {
			buttons++
			data := string(button.Data)
			// Structural assertion rather than a substring hunt: the data is the
			// prefix plus an opaque hex token and nothing else, so it is incapable of
			// encoding a peer id, an access hash, a username or a peer type.
			token, ok := strings.CutPrefix(data, verifyCallbackDataPrefix)
			if !ok || len(token) != 2*verifyOptionTokenBytes {
				t.Fatalf("callback data %q is not <prefix><token>", data)
			}
			for _, c := range token {
				if !strings.ContainsRune("0123456789abcdef", c) {
					t.Fatalf("callback data %q carries non-token bytes", data)
				}
			}
			for _, forbidden := range []string{target.Username, string(target.Type)} {
				if strings.Contains(data, forbidden) {
					t.Fatalf("callback data %q leaks %q", data, forbidden)
				}
			}
		}
	}
	if buttons == 0 {
		t.Fatal("target picker has no buttons")
	}

	// The token is minted per render, so the same target never has a stable,
	// guessable identifier on the wire.
	firstData, _ := verifyButtonData(picker, "@"+target.Username)
	sendToVerifyBot(t, svc, messages, owner.ID, "/new")
	secondData, found := verifyButtonData(latestVerifyReply(t, messages, owner.ID), "@"+target.Username)
	if !found {
		t.Fatal("re-rendered picker has no target button")
	}
	if string(firstData) == string(secondData) {
		t.Fatalf("token %q is stable across renders", firstData)
	}
}

func TestVerifyBotRepeatedButtonPressIsIdempotent(t *testing.T) {
	fake := newFakeVerification(verifyChannelTarget())
	svc, users, messages := newVerifyBotTestService(t, fake)
	owner := newOwner(t, users, "+7103")

	intro := sendToVerifyBot(t, svc, messages, owner.ID, "/start")
	pressVerifyButton(t, svc, owner.ID, intro, verifyApplyButtonText)
	picker := latestVerifyReply(t, messages, owner.ID)

	pressVerifyButton(t, svc, owner.ID, picker, "@examplenews")
	first := latestVerifyReply(t, messages, owner.ID)
	pressVerifyButton(t, svc, owner.ID, picker, "@examplenews")
	second := latestVerifyReply(t, messages, owner.ID)

	if first.Body != second.Body {
		t.Fatalf("repeat target press changed the answer:\nfirst  = %q\nsecond = %q", first.Body, second.Body)
	}
	if len(fake.apps) != 1 {
		t.Fatalf("applications = %d after pressing the same target twice, want 1", len(fake.apps))
	}
}

// The same must hold for the terminal action: a double-tapped Submit files one
// application and repeats the same confirmation.
func TestVerifyBotRepeatedSubmitFilesOneApplication(t *testing.T) {
	fake := newFakeVerification(verifyChannelTarget())
	svc, users, messages := newVerifyBotTestService(t, fake)
	owner := newOwner(t, users, "+7121")

	summary := runVerifyApplication(t, svc, messages, owner.ID)
	pressVerifyButton(t, svc, owner.ID, summary, verifySubmitButtonText)
	firstFiled := latestVerifyReply(t, messages, owner.ID)
	pressVerifyButton(t, svc, owner.ID, summary, verifySubmitButtonText)
	secondFiled := latestVerifyReply(t, messages, owner.ID)

	if firstFiled.Body != secondFiled.Body {
		t.Fatalf("repeat submit changed the answer:\nfirst  = %q\nsecond = %q", firstFiled.Body, secondFiled.Body)
	}
	if fake.submits != 1 || len(fake.apps) != 1 {
		t.Fatalf("submits=%d applications=%d after double submit, want 1/1", fake.submits, len(fake.apps))
	}
}

func TestVerifyBotForgedCallbackTokenIsRefused(t *testing.T) {
	fake := newFakeVerification(verifyChannelTarget())
	svc, users, messages := newVerifyBotTestService(t, fake)
	owner := newOwner(t, users, "+7104")

	intro := sendToVerifyBot(t, svc, messages, owner.ID, "/start")
	before := len(verifyBotReplies(t, messages, owner.ID))

	// A token that was never minted for this user, and a plausible-looking
	// hand-written one: both resolve only through the user's own state, so both are
	// refused without any side effect.
	for _, data := range [][]byte{
		[]byte(verifyCallbackDataPrefix + "deadbeefcafe"),
		[]byte("tgt:channel:7001"),
		[]byte(verifyCallbackDataPrefix),
	} {
		answer := pressVerifyCallbackData(t, svc, owner.ID, intro, data)
		if !answer.Alert || !strings.Contains(answer.Message, "no longer active") {
			t.Fatalf("forged data %q answered %+v, want an explaining alert", data, answer)
		}
	}
	if got := len(verifyBotReplies(t, messages, owner.ID)); got != before {
		t.Fatalf("forged callbacks produced %d new messages", got-before)
	}
	if fake.starts != 0 || len(fake.apps) != 0 {
		t.Fatalf("forged callbacks touched the service: starts=%d apps=%d", fake.starts, len(fake.apps))
	}
}

// A token minted for one applicant must be meaningless for another: resolution
// goes through the clicking user's own chat state only.
func TestVerifyBotTokenFromAnotherUserIsRefused(t *testing.T) {
	fake := newFakeVerification(verifyChannelTarget())
	svc, users, messages := newVerifyBotTestService(t, fake)
	victim := newOwner(t, users, "+7105")
	attacker := newOwner(t, users, "+7106")

	intro := sendToVerifyBot(t, svc, messages, victim.ID, "/start")
	pressVerifyButton(t, svc, victim.ID, intro, verifyApplyButtonText)
	picker := latestVerifyReply(t, messages, victim.ID)
	stolen, found := verifyButtonData(picker, "@examplenews")
	if !found {
		t.Fatal("victim picker has no target button")
	}

	sendToVerifyBot(t, svc, messages, attacker.ID, "/start")
	attackerIntro := latestVerifyReply(t, messages, attacker.ID)
	answer := pressVerifyCallbackData(t, svc, attacker.ID, attackerIntro, stolen)
	if !answer.Alert {
		t.Fatalf("stolen token answered %+v, want an alert", answer)
	}
	for _, app := range fake.apps {
		if app.ApplicantUserID == attacker.ID {
			t.Fatalf("stolen token created an application for the attacker: %+v", app)
		}
	}
}

func TestVerifyBotPressLinkMinimumIsEnforced(t *testing.T) {
	fake := newFakeVerification(verifyChannelTarget())
	svc, users, messages := newVerifyBotTestService(t, fake)
	owner := newOwner(t, users, "+7107")

	intro := sendToVerifyBot(t, svc, messages, owner.ID, "/start")
	pressVerifyButton(t, svc, owner.ID, intro, verifyApplyButtonText)
	pressVerifyButton(t, svc, owner.ID, latestVerifyReply(t, messages, owner.ID), "@examplenews")
	pressVerifyButton(t, svc, owner.ID, latestVerifyReply(t, messages, owner.ID), "Media outlet")
	sendToVerifyBot(t, svc, messages, owner.ID, verifyTestDescription)
	social := sendToVerifyBot(t, svc, messages, owner.ID, verifyTestWebsite)
	pressVerifyButton(t, svc, owner.ID, social, verifySkipButtonText)

	tooFew := sendToVerifyBot(t, svc, messages, owner.ID, "https://press.example.org/story-one")
	if !strings.Contains(tooFew.Body, strconv.Itoa(domain.MinVerificationPressLinks)) {
		t.Fatalf("single press link accepted or unexplained: %q", tooFew.Body)
	}
	if len(fake.apps[101].PressLinks) != 0 {
		t.Fatalf("press links stored despite refusal: %+v", fake.apps[101].PressLinks)
	}

	accepted := sendToVerifyBot(t, svc, messages, owner.ID, verifyTestPressLinks)
	if !strings.Contains(accepted.Body, "reviewers should know") {
		t.Fatalf("two press links did not advance the dialog: %q", accepted.Body)
	}
	if len(fake.apps[101].PressLinks) != 2 {
		t.Fatalf("press links = %+v, want two stored", fake.apps[101].PressLinks)
	}
}

func TestVerifyBotRejectsInvalidLinksWithAReason(t *testing.T) {
	fake := newFakeVerification(verifyChannelTarget())
	svc, users, messages := newVerifyBotTestService(t, fake)
	owner := newOwner(t, users, "+7108")

	intro := sendToVerifyBot(t, svc, messages, owner.ID, "/start")
	pressVerifyButton(t, svc, owner.ID, intro, verifyApplyButtonText)
	pressVerifyButton(t, svc, owner.ID, latestVerifyReply(t, messages, owner.ID), "@examplenews")
	pressVerifyButton(t, svc, owner.ID, latestVerifyReply(t, messages, owner.ID), "Media outlet")
	sendToVerifyBot(t, svc, messages, owner.ID, verifyTestDescription)

	// Not a URL, a non-web scheme, and an address the domain refuses as
	// non-public (which is also what keeps a submitted link from becoming an SSRF
	// probe).
	for _, bad := range []string{"my site", "ftp://example.com", "http://127.0.0.1/admin", "https://localhost/x"} {
		reply := sendToVerifyBot(t, svc, messages, owner.ID, bad)
		if !strings.Contains(reply.Body, "http:// or https://") {
			t.Fatalf("website %q answered %q, want the link rules", bad, reply.Body)
		}
		if fake.apps[101].OfficialWebsite != "" {
			t.Fatalf("website %q was stored", bad)
		}
	}
	// A short description is refused with the actual bar, not a generic error.
	shortDesc := sendToVerifyBot(t, svc, messages, owner.ID, verifyTestWebsite)
	if !strings.Contains(shortDesc.Body, "social media") {
		t.Fatalf("valid website not accepted: %q", shortDesc.Body)
	}
}

func TestVerifyBotDescriptionMinimumIsExplained(t *testing.T) {
	fake := newFakeVerification(verifyChannelTarget())
	svc, users, messages := newVerifyBotTestService(t, fake)
	owner := newOwner(t, users, "+7109")

	intro := sendToVerifyBot(t, svc, messages, owner.ID, "/start")
	pressVerifyButton(t, svc, owner.ID, intro, verifyApplyButtonText)
	pressVerifyButton(t, svc, owner.ID, latestVerifyReply(t, messages, owner.ID), "@examplenews")
	pressVerifyButton(t, svc, owner.ID, latestVerifyReply(t, messages, owner.ID), "Media outlet")

	reply := sendToVerifyBot(t, svc, messages, owner.ID, "a newsroom")
	if !strings.Contains(reply.Body, strconv.Itoa(domain.MinVerificationDescriptionLength)) {
		t.Fatalf("short description answered %q, want the minimum length", reply.Body)
	}
	if fake.apps[101].Description != "" {
		t.Fatalf("short description was stored: %q", fake.apps[101].Description)
	}
}

func TestVerifyBotGlobalCommandsWorkMidStep(t *testing.T) {
	fake := newFakeVerification(verifyChannelTarget())
	svc, users, messages := newVerifyBotTestService(t, fake)
	owner := newOwner(t, users, "+7110")

	intro := sendToVerifyBot(t, svc, messages, owner.ID, "/start")
	pressVerifyButton(t, svc, owner.ID, intro, verifyApplyButtonText)
	pressVerifyButton(t, svc, owner.ID, latestVerifyReply(t, messages, owner.ID), "@examplenews")
	pressVerifyButton(t, svc, owner.ID, latestVerifyReply(t, messages, owner.ID), "Media outlet")

	// /help in the middle of the description step answers help and keeps the step.
	help := sendToVerifyBot(t, svc, messages, owner.ID, "/help")
	if help.Body != verifyBotHelpText {
		t.Fatalf("/help mid-step = %q", help.Body)
	}
	status := sendToVerifyBot(t, svc, messages, owner.ID, "/status")
	if !strings.Contains(status.Body, "#101") {
		t.Fatalf("/status mid-step = %q", status.Body)
	}
	resumed := sendToVerifyBot(t, svc, messages, owner.ID, verifyTestDescription)
	if !strings.Contains(resumed.Body, "official website") {
		t.Fatalf("description not accepted after global commands: %q", resumed.Body)
	}
	if fake.apps[101].Description != verifyTestDescription {
		t.Fatalf("description = %q, want the step to have survived", fake.apps[101].Description)
	}
	// An unknown command is never swallowed as a field value.
	unknown := sendToVerifyBot(t, svc, messages, owner.ID, "/nope")
	if !strings.Contains(unknown.Body, "do not know that command") {
		t.Fatalf("unknown command = %q", unknown.Body)
	}
	if fake.apps[101].OfficialWebsite != "" {
		t.Fatalf("unknown command stored as a website: %q", fake.apps[101].OfficialWebsite)
	}
}

func TestVerifyBotStatusListsApplicationsWithoutInternalNotes(t *testing.T) {
	fake := newFakeVerification(verifyChannelTarget())
	svc, users, messages := newVerifyBotTestService(t, fake)
	owner := newOwner(t, users, "+7111")

	if empty := sendToVerifyBot(t, svc, messages, owner.ID, "/status"); empty.Body != verifyNoApplicationsText {
		t.Fatalf("/status without applications = %q", empty.Body)
	}

	fake.apps[500] = domain.VerificationApplication{
		ID: 500, ApplicantUserID: owner.ID,
		TargetType: domain.VerificationTargetChannel, TargetID: 7001,
		TargetTitle: "Example News", TargetUsername: "examplenews",
		Status:         domain.VerificationStatusRejected,
		DecisionReason: "the linked coverage does not mention the channel",
		InternalNote:   "applicant argued with the reviewer",
		ReviewedAt:     time.Date(2026, 7, 20, 9, 0, 0, 0, time.UTC),
		Version:        4,
	}
	fake.apps[501] = domain.VerificationApplication{
		ID: 501, ApplicantUserID: owner.ID,
		TargetType: domain.VerificationTargetBot, TargetID: 8002, TargetUsername: "examplebot",
		Status:      domain.VerificationStatusSubmitted,
		SubmittedAt: time.Date(2026, 7, 25, 9, 0, 0, 0, time.UTC),
		Version:     2,
	}

	reply := sendToVerifyBot(t, svc, messages, owner.ID, "/status")
	for _, want := range []string{"#500", "#501", "@examplebot", "does not mention the channel", "2026-07-20"} {
		if !strings.Contains(reply.Body, want) {
			t.Fatalf("/status missing %q: %q", want, reply.Body)
		}
	}
	if strings.Contains(reply.Body, "argued with the reviewer") {
		t.Fatalf("/status leaked the internal note: %q", reply.Body)
	}
}

func TestVerifyBotCancelWithdrawsTheOpenApplication(t *testing.T) {
	fake := newFakeVerification(verifyChannelTarget())
	svc, users, messages := newVerifyBotTestService(t, fake)
	owner := newOwner(t, users, "+7112")

	if nothing := sendToVerifyBot(t, svc, messages, owner.ID, "/cancel"); nothing.Body != verifyNothingToCancelText {
		t.Fatalf("/cancel with nothing open = %q", nothing.Body)
	}

	intro := sendToVerifyBot(t, svc, messages, owner.ID, "/start")
	pressVerifyButton(t, svc, owner.ID, intro, verifyApplyButtonText)
	pressVerifyButton(t, svc, owner.ID, latestVerifyReply(t, messages, owner.ID), "@examplenews")

	cancelled := sendToVerifyBot(t, svc, messages, owner.ID, "/cancel")
	if !strings.Contains(cancelled.Body, "#101") || !strings.Contains(cancelled.Body, "withdrawn") {
		t.Fatalf("/cancel = %q", cancelled.Body)
	}
	if fake.apps[101].Status != domain.VerificationStatusCancelled {
		t.Fatalf("application status = %q after /cancel", fake.apps[101].Status)
	}
	// The dialog is gone with it, so a stale button cannot revive it.
	idle := sendToVerifyBot(t, svc, messages, owner.ID, "still here?")
	if idle.Body != verifyBotIdleText {
		t.Fatalf("after /cancel, plain text = %q", idle.Body)
	}
}

func TestVerifyBotCancelButtonWithdrawsFromInsideTheForm(t *testing.T) {
	fake := newFakeVerification(verifyChannelTarget())
	svc, users, messages := newVerifyBotTestService(t, fake)
	owner := newOwner(t, users, "+7113")

	intro := sendToVerifyBot(t, svc, messages, owner.ID, "/start")
	pressVerifyButton(t, svc, owner.ID, intro, verifyApplyButtonText)
	pressVerifyButton(t, svc, owner.ID, latestVerifyReply(t, messages, owner.ID), "@examplenews")
	categories := latestVerifyReply(t, messages, owner.ID)

	pressVerifyButton(t, svc, owner.ID, categories, verifyCancelButtonText)
	if reply := latestVerifyReply(t, messages, owner.ID); !strings.Contains(reply.Body, "withdrawn") {
		t.Fatalf("cancel button = %q", reply.Body)
	}
	if fake.apps[101].Status != domain.VerificationStatusCancelled {
		t.Fatalf("application status = %q after the cancel button", fake.apps[101].Status)
	}
}

func TestVerifyBotHelpAndIdleText(t *testing.T) {
	fake := newFakeVerification()
	svc, users, messages := newVerifyBotTestService(t, fake)
	owner := newOwner(t, users, "+7114")

	help := sendToVerifyBot(t, svc, messages, owner.ID, "/help")
	for _, want := range []string{"/new", "/status", "/cancel", "/help"} {
		if !strings.Contains(help.Body, want) {
			t.Fatalf("/help missing %q: %q", want, help.Body)
		}
	}
	assertReplyEntityText(t, help, domain.MessageEntityBotCommand, "/new")

	// Nothing to verify: the requirement is stated instead of an empty picker.
	if reply := sendToVerifyBot(t, svc, messages, owner.ID, "/new"); reply.Body != verifyNoTargetsText {
		t.Fatalf("/new with no candidates = %q", reply.Body)
	}
}

func TestVerifyBotShowsIneligibleTargetsWithTheirReason(t *testing.T) {
	verified := verifyChannelTarget()
	verified.Eligible = false
	verified.Verified = true
	verified.Reason = domain.ErrVerificationTargetAlreadyVerified.Error()
	fake := newFakeVerification(verified)
	svc, users, messages := newVerifyBotTestService(t, fake)
	owner := newOwner(t, users, "+7115")

	picker := sendToVerifyBot(t, svc, messages, owner.ID, "/new")
	if !strings.Contains(picker.Body, "cannot be filed") && !strings.Contains(picker.Body, verifyNoEligibleText) {
		t.Fatalf("picker with only ineligible candidates = %q", picker.Body)
	}
	answer := pressVerifyButton(t, svc, owner.ID, picker, "unavailable")
	if !answer.Alert || !strings.Contains(answer.Message, "already verified") {
		t.Fatalf("ineligible button answered %+v, want the reason", answer)
	}
	if fake.starts != 0 || len(fake.apps) != 0 {
		t.Fatalf("ineligible button reached the service: starts=%d apps=%d", fake.starts, len(fake.apps))
	}
}

func TestVerifyBotNewResumesTheOpenDraft(t *testing.T) {
	fake := newFakeVerification(verifyChannelTarget())
	svc, users, messages := newVerifyBotTestService(t, fake)
	owner := newOwner(t, users, "+7116")

	intro := sendToVerifyBot(t, svc, messages, owner.ID, "/start")
	pressVerifyButton(t, svc, owner.ID, intro, verifyApplyButtonText)
	pressVerifyButton(t, svc, owner.ID, latestVerifyReply(t, messages, owner.ID), "@examplenews")
	pressVerifyButton(t, svc, owner.ID, latestVerifyReply(t, messages, owner.ID), "Media outlet")
	sendToVerifyBot(t, svc, messages, owner.ID, verifyTestDescription)

	resumed := sendToVerifyBot(t, svc, messages, owner.ID, "/new")
	if !strings.Contains(resumed.Body, "#101") || !strings.Contains(resumed.Body, "official website") {
		t.Fatalf("/new mid-draft = %q, want a resume at the website step", resumed.Body)
	}
	if len(fake.apps) != 1 {
		t.Fatalf("applications = %d after /new mid-draft, want 1", len(fake.apps))
	}
}

func TestVerifyBotWithoutServiceReportsUnavailable(t *testing.T) {
	svc, users, messages := newVerifyBotTestService(t, nil)
	owner := newOwner(t, users, "+7117")

	if reply := sendToVerifyBot(t, svc, messages, owner.ID, "/new"); reply.Body != verifyUnavailableText {
		t.Fatalf("/new without a verification service = %q", reply.Body)
	}
	if reply := sendToVerifyBot(t, svc, messages, owner.ID, "/help"); reply.Body != verifyBotHelpText {
		t.Fatalf("/help without a verification service = %q", reply.Body)
	}
}

func TestVerifyBotCallbackForForeignBotIsNotClaimed(t *testing.T) {
	fake := newFakeVerification(verifyChannelTarget())
	svc, _, _ := newVerifyBotTestService(t, fake)

	if _, handled, err := svc.OnCallbackQuery(context.Background(), domain.BotCallbackQuery{
		BotUserID: 555111, UserID: 900, Data: []byte("vb:whatever"),
	}); handled || err != nil {
		t.Fatalf("foreign bot callback handled=%v err=%v, want (false, nil)", handled, err)
	}
	// A built-in bot with no keyboards is claimed but answered empty, so the click
	// cannot hang for the whole callback timeout.
	answer, handled, err := svc.OnCallbackQuery(context.Background(), domain.BotCallbackQuery{
		BotUserID: domain.BotFatherUserID, UserID: 900, Data: []byte("x"),
	})
	if !handled || err != nil || answer.Message != "" {
		t.Fatalf("BotFather callback = (%+v, %v, %v)", answer, handled, err)
	}
}

func TestVerifyBotSendVerificationNoticeNeverLeaksInternalNote(t *testing.T) {
	fake := newFakeVerification()
	svc, users, messages := newVerifyBotTestService(t, fake)
	owner := newOwner(t, users, "+7118")
	ctx := context.Background()

	app := domain.VerificationApplication{
		ID: 4242, ApplicantUserID: owner.ID,
		TargetType: domain.VerificationTargetChannel, TargetID: 7001,
		TargetTitle: "Example News", TargetUsername: "examplenews",
		DecisionReason: "the coverage you linked does not mention the channel",
		InternalNote:   "reviewer note: applicant is a repeat filer, escalate next time",
	}

	if err := svc.SendVerificationNotice(ctx, owner.ID, app, verificationapp.NoticeKindApproved); err != nil {
		t.Fatalf("approved notice: %v", err)
	}
	approved := latestVerifyReply(t, messages, owner.ID)
	for _, want := range []string{"#4242", "Example News", "@examplenews", "approved"} {
		if !strings.Contains(approved.Body, want) {
			t.Fatalf("approved notice missing %q: %q", want, approved.Body)
		}
	}
	if strings.Contains(approved.Body, "repeat filer") {
		t.Fatalf("approved notice leaked the internal note: %q", approved.Body)
	}

	if err := svc.SendVerificationNotice(ctx, owner.ID, app, verificationapp.NoticeKindRejected); err != nil {
		t.Fatalf("rejected notice: %v", err)
	}
	rejected := latestVerifyReply(t, messages, owner.ID)
	if !strings.Contains(rejected.Body, "#4242") || !strings.Contains(rejected.Body, "does not mention the channel") {
		t.Fatalf("rejected notice = %q", rejected.Body)
	}
	if strings.Contains(rejected.Body, "repeat filer") || strings.Contains(rejected.Body, "escalate") {
		t.Fatalf("rejected notice leaked the internal note: %q", rejected.Body)
	}

	if err := svc.SendVerificationNotice(ctx, owner.ID, app, verificationapp.NoticeKindRevoked); err != nil {
		t.Fatalf("revoked notice: %v", err)
	}
	revoked := latestVerifyReply(t, messages, owner.ID)
	if !strings.Contains(revoked.Body, "revoked") || strings.Contains(revoked.Body, "repeat filer") {
		t.Fatalf("revoked notice = %q", revoked.Body)
	}

	// An unknown kind is reported rather than delivered as an empty message: the
	// outbox row must stay pending instead of being marked delivered.
	before := len(verifyBotReplies(t, messages, owner.ID))
	if err := svc.SendVerificationNotice(ctx, owner.ID, app, "teleported"); err == nil {
		t.Fatal("unknown notice kind reported success")
	}
	if got := len(verifyBotReplies(t, messages, owner.ID)); got != before {
		t.Fatalf("unknown notice kind sent %d messages", got-before)
	}
	if err := svc.SendVerificationNotice(ctx, 0, app, verificationapp.NoticeKindApproved); err == nil {
		t.Fatal("empty recipient reported success")
	}
}

func TestVerifyBotSubmitBouncesAnIncompleteApplication(t *testing.T) {
	fake := newFakeVerification(verifyChannelTarget())
	svc, users, messages := newVerifyBotTestService(t, fake)
	owner := newOwner(t, users, "+7119")

	summary := runVerifyApplication(t, svc, messages, owner.ID)
	// Simulate a payload that lost a required field between rendering the summary
	// and the press: Submit must send the applicant back, not file a broken record.
	app := fake.apps[101]
	app.PressLinks = nil
	app.Version++
	fake.apps[101] = app

	pressVerifyButton(t, svc, owner.ID, summary, verifySubmitButtonText)
	bounced := latestVerifyReply(t, messages, owner.ID)
	if !strings.Contains(bounced.Body, "press coverage") {
		t.Fatalf("incomplete submit = %q, want the press step", bounced.Body)
	}
	if fake.submits != 0 {
		t.Fatalf("submits = %d for an incomplete application", fake.submits)
	}
}

func TestVerifyBotPolicyRefusalsAreExplained(t *testing.T) {
	fake := newFakeVerification(verifyChannelTarget())
	fake.startErr = domain.ErrVerificationRateLimited
	svc, users, messages := newVerifyBotTestService(t, fake)
	owner := newOwner(t, users, "+7120")

	picker := sendToVerifyBot(t, svc, messages, owner.ID, "/new")
	pressVerifyButton(t, svc, owner.ID, picker, "@examplenews")
	if reply := latestVerifyReply(t, messages, owner.ID); !strings.Contains(reply.Body, "limit on open applications") {
		t.Fatalf("rate-limited StartDraft = %q", reply.Body)
	}

	fake.targetsErr = verificationapp.ErrDisabled
	if reply := sendToVerifyBot(t, svc, messages, owner.ID, "/new"); reply.Body != verifyUnavailableText {
		t.Fatalf("disabled verification = %q", reply.Body)
	}
	if !errors.Is(fake.targetsErr, verificationapp.ErrDisabled) {
		t.Fatal("test setup lost the sentinel")
	}
}
