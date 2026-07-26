package bots

import (
	"context"
	"sort"
	"strings"
	"testing"
	"time"

	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

// ---------------------------------------------------------------------------
// Fake third-party verification service
// ---------------------------------------------------------------------------

// fakeCustomVerification is an in-memory stand-in for app/botverification.Service.
// It keeps the properties the dialog leans on: verifier status that an operator may
// or may not have granted, one pending application per (verifier, peer), marks that
// exist independently of the applications, and a revoke that reports whether it
// actually removed anything.
type fakeCustomVerification struct {
	settings      domain.BotVerifierSettings
	settingsFound bool
	settingsErr   error
	requests      map[int64]domain.CustomVerificationRequest
	marks         map[domain.Peer]domain.CustomVerification
	nextID        int64
	creates       int
	revokes       int
	revokedPeers  []domain.Peer
	createErr     error
}

func newFakeCustomVerification() *fakeCustomVerification {
	return &fakeCustomVerification{
		requests: make(map[int64]domain.CustomVerificationRequest),
		marks:    make(map[domain.Peer]domain.CustomVerification),
		nextID:   700,
	}
}

// activated grants the bot the verifier status an operator would grant in the admin
// panel.
func (f *fakeCustomVerification) activated() *fakeCustomVerification {
	f.settings = verifierTestSettings()
	f.settingsFound = true
	return f
}

func verifierTestSettings() domain.BotVerifierSettings {
	return domain.BotVerifierSettings{
		BotID:              domain.VerifierBotUserID,
		IconDocumentID:     990001,
		CompanyName:        "Example Trust",
		DefaultDescription: "Identity checked by Example Trust",
		Enabled:            true,
		GrantedBy:          "operator",
		GrantReason:        "reference verifier of this deployment",
		Version:            1,
	}
}

func (f *fakeCustomVerification) VerifierSettings(_ context.Context, botID int64) (domain.BotVerifierSettings, error) {
	if f.settingsErr != nil {
		return domain.BotVerifierSettings{}, f.settingsErr
	}
	if botID != domain.VerifierBotUserID || !f.settingsFound {
		return domain.BotVerifierSettings{}, domain.ErrVerifierNotFound
	}
	return f.settings, nil
}

func (f *fakeCustomVerification) CreateRequest(_ context.Context, req domain.CustomVerificationRequest) (domain.CustomVerificationRequest, error) {
	f.creates++
	if f.createErr != nil {
		return domain.CustomVerificationRequest{}, f.createErr
	}
	// The record the bot hands over must be a valid one: this is what keeps the dialog
	// from filing paperwork the service would have to reject.
	if err := req.Validate(); err != nil {
		return domain.CustomVerificationRequest{}, err
	}
	for _, existing := range f.requests {
		if existing.VerifierBotID == req.VerifierBotID && existing.Peer == req.Peer &&
			existing.Status == domain.CustomVerificationPending {
			return domain.CustomVerificationRequest{}, domain.ErrCustomVerificationRequestExists
		}
	}
	f.nextID++
	req.ID = f.nextID
	req.CreatedAt = time.Date(2026, 7, 26, 10, 0, 0, 0, time.UTC)
	req.Version = 1
	f.requests[req.ID] = req
	return req, nil
}

func (f *fakeCustomVerification) ApplicantRequests(_ context.Context, applicantUserID int64, limit int) ([]domain.CustomVerificationRequest, error) {
	out := make([]domain.CustomVerificationRequest, 0, len(f.requests))
	for _, req := range f.requests {
		if req.ApplicantUserID == applicantUserID {
			out = append(out, req)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (f *fakeCustomVerification) PendingRequest(_ context.Context, verifierBotID int64, peer domain.Peer) (domain.CustomVerificationRequest, error) {
	for _, req := range f.requests {
		if req.VerifierBotID == verifierBotID && req.Peer == peer && req.Status == domain.CustomVerificationPending {
			return req, nil
		}
	}
	return domain.CustomVerificationRequest{}, domain.ErrCustomVerificationRequestNotFound
}

func (f *fakeCustomVerification) RevokeMark(_ context.Context, verifierBotID int64, peer domain.Peer) (bool, error) {
	f.revokes++
	f.revokedPeers = append(f.revokedPeers, peer)
	mark, found := f.marks[peer]
	if !found || mark.VerifierBotID != verifierBotID {
		return false, nil
	}
	delete(f.marks, peer)
	for id, req := range f.requests {
		if req.Peer == peer && req.Status == domain.CustomVerificationApproved {
			req.Status = domain.CustomVerificationRevoked
			req.Version++
			f.requests[id] = req
		}
	}
	return true, nil
}

func (f *fakeCustomVerification) Marks(_ context.Context, filter domain.CustomVerificationFilter) ([]domain.CustomVerification, error) {
	out := make([]domain.CustomVerification, 0, len(f.marks))
	for peer, mark := range f.marks {
		if filter.VerifierBotID != 0 && mark.VerifierBotID != filter.VerifierBotID {
			continue
		}
		if filter.PeerType != "" && peer.Type != filter.PeerType {
			continue
		}
		if filter.PeerID != 0 && peer.ID != filter.PeerID {
			continue
		}
		out = append(out, mark)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Peer.ID < out[j].Peer.ID })
	if filter.Limit > 0 && len(out) > filter.Limit {
		out = out[:filter.Limit]
	}
	return out, nil
}

// grantMark is what an operator's approval leaves behind: an approved application
// and the mark itself.
func (f *fakeCustomVerification) grantMark(applicantUserID int64, peer domain.Peer, title, username string) domain.CustomVerificationRequest {
	f.nextID++
	req := domain.CustomVerificationRequest{
		ID:              f.nextID,
		VerifierBotID:   domain.VerifierBotUserID,
		ApplicantUserID: applicantUserID,
		Peer:            peer,
		PeerTitle:       title,
		PeerUsername:    username,
		Reason:          "the newsroom of the Example Foundation",
		Status:          domain.CustomVerificationApproved,
		DecidedBy:       "operator",
		InternalNote:    "operator note: applicant escalated by email, watch this one",
		CreatedAt:       time.Date(2026, 7, 18, 9, 0, 0, 0, time.UTC),
		ApprovedAt:      time.Date(2026, 7, 20, 9, 0, 0, 0, time.UTC),
		Version:         2,
	}
	f.requests[req.ID] = req
	f.marks[peer] = domain.CustomVerification{
		ID:              req.ID,
		VerifierBotID:   domain.VerifierBotUserID,
		Peer:            peer,
		IconDocumentID:  990001,
		Description:     "Identity checked by Example Trust",
		GrantedByUserID: 1,
		Version:         1,
	}
	return req
}

var _ customVerifications = (*fakeCustomVerification)(nil)

// fakeVerifierTargets is the directory of the applicant's own peers.
type fakeVerifierTargets struct {
	targets []domain.VerificationTarget
	err     error
	calls   int
}

func (f *fakeVerifierTargets) EligibleTargets(_ context.Context, applicantUserID int64) ([]domain.VerificationTarget, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	if applicantUserID <= 0 {
		return nil, domain.ErrCustomVerificationRequestInvalid
	}
	return append([]domain.VerificationTarget(nil), f.targets...), nil
}

var _ verifierBotTargets = (*fakeVerifierTargets)(nil)

// ---------------------------------------------------------------------------
// Harness
// ---------------------------------------------------------------------------

func verifierTestTargets(owner domain.User) *fakeVerifierTargets {
	return &fakeVerifierTargets{targets: []domain.VerificationTarget{
		// Eligible=false throughout on purpose: that flag answers "may this peer be
		// filed for the official checkmark", which is a different mechanism, and the
		// third-party picker must not be filtered by it.
		{Type: domain.VerificationTargetChannel, ID: 7001, Title: "Example News", Username: "examplenews"},
		{Type: domain.VerificationTargetBot, ID: 8002, Title: "Example Bot", Username: "examplebot"},
		{Type: domain.VerificationTargetUser, ID: owner.ID, Title: "Owner"},
	}}
}

const verifierNewsPeerID = 7001

func verifierNewsPeer() domain.Peer {
	return domain.Peer{Type: domain.PeerTypeChannel, ID: verifierNewsPeerID}
}

func newVerifierBotTestService(t *testing.T, cv customVerifications, opts ...Option) (*Service, *memory.UserStore, *memory.MessageStore) {
	t.Helper()
	users := memory.NewUserStore()
	bots := memory.NewBotStore(users)
	dialogs := memory.NewDialogStore()
	messages := memory.NewMessageStore(dialogs)
	all := append([]Option{WithCustomVerification(cv)}, opts...)
	return NewService(users, bots, messages, all...), users, messages
}

// verifierReplies returns every @verifierbot message in the user's box, oldest
// first.
func verifierReplies(t *testing.T, messages *memory.MessageStore, userID int64) []domain.Message {
	t.Helper()
	list, err := messages.ListByUser(context.Background(), userID, domain.MessageFilter{
		HasPeer: true,
		Peer:    domain.Peer{Type: domain.PeerTypeUser, ID: domain.VerifierBotUserID},
		Limit:   200,
	})
	if err != nil {
		t.Fatalf("list @verifierbot history: %v", err)
	}
	out := make([]domain.Message, 0, len(list.Messages))
	for _, msg := range list.Messages {
		if msg.From.ID == domain.VerifierBotUserID {
			out = append(out, msg)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func latestVerifierReply(t *testing.T, messages *memory.MessageStore, userID int64) domain.Message {
	t.Helper()
	replies := verifierReplies(t, messages, userID)
	if len(replies) == 0 {
		t.Fatal("no @verifierbot reply")
	}
	latest := replies[len(replies)-1]
	// Every keyboard the bot renders must be a valid, persistable markup: the send
	// path validates before storing, so an invalid one would silently vanish.
	if err := domain.ValidateReplyMarkup(latest.ReplyMarkup); err != nil {
		t.Fatalf("reply markup invalid: %v (%+v)", err, latest.ReplyMarkup)
	}
	for _, row := range verifierRowsOf(latest) {
		for _, button := range row {
			if len(button.Data) > domain.MaxCallbackDataLen {
				t.Fatalf("callback data %q is %d bytes, limit is %d", button.Data, len(button.Data), domain.MaxCallbackDataLen)
			}
		}
	}
	return latest
}

func verifierRowsOf(msg domain.Message) [][]domain.MarkupButton {
	if msg.ReplyMarkup == nil {
		return nil
	}
	return msg.ReplyMarkup.Inline
}

// sendToVerifierBot drives the responder synchronously, bypassing the
// OnPrivateMessage goroutine dispatch for determinism (the same shortcut the
// BotFather, @Stickers and @verifybot tests take).
func sendToVerifierBot(t *testing.T, svc *Service, messages *memory.MessageStore, userID int64, text string) domain.Message {
	t.Helper()
	svc.respondAsVerifier(userID, domain.Message{
		From: domain.Peer{Type: domain.PeerTypeUser, ID: userID},
		Peer: domain.Peer{Type: domain.PeerTypeUser, ID: domain.VerifierBotUserID},
		Body: text,
	})
	return latestVerifierReply(t, messages, userID)
}

func verifierDataOf(msg domain.Message, label string) ([]byte, bool) {
	for _, row := range verifierRowsOf(msg) {
		for _, button := range row {
			if button.Type == domain.MarkupButtonCallback && strings.Contains(button.Text, label) {
				return append([]byte(nil), button.Data...), true
			}
		}
	}
	return nil, false
}

// pressVerifierData drives the internal callback path with raw data, the way
// rpc.Router does once it has validated the click.
func pressVerifierData(t *testing.T, svc *Service, userID int64, msg domain.Message, data []byte) domain.BotCallbackAnswer {
	t.Helper()
	if len(data) > domain.MaxCallbackDataLen {
		t.Fatalf("callback data too long: %d bytes", len(data))
	}
	answer, handled, err := svc.OnCallbackQuery(context.Background(), domain.BotCallbackQuery{
		ID:        1,
		BotUserID: domain.VerifierBotUserID,
		UserID:    userID,
		Peer:      domain.Peer{Type: domain.PeerTypeUser, ID: userID},
		MessageID: msg.ID,
		Data:      data,
	})
	if err != nil {
		t.Fatalf("callback query: %v", err)
	}
	if !handled {
		t.Fatal("callback query reported unhandled for @verifierbot")
	}
	return answer
}

func pressVerifierButton(t *testing.T, svc *Service, userID int64, msg domain.Message, label string) domain.BotCallbackAnswer {
	t.Helper()
	data, found := verifierDataOf(msg, label)
	if !found {
		t.Fatalf("button %q is not in the keyboard of message %d: %+v", label, msg.ID, msg.ReplyMarkup)
	}
	return pressVerifierData(t, svc, userID, msg, data)
}

const verifierTestReason = "Example News is the daily newsroom of the Example Foundation and has published since 2015."

// runVerifierApplication walks the dialog up to (but not including) Confirm and
// returns the summary message.
func runVerifierApplication(t *testing.T, svc *Service, messages *memory.MessageStore, userID int64) domain.Message {
	t.Helper()
	intro := sendToVerifierBot(t, svc, messages, userID, "/start")
	pressVerifierButton(t, svc, userID, intro, verifierApplyButtonText)
	picker := latestVerifierReply(t, messages, userID)

	pressVerifierButton(t, svc, userID, picker, "@examplenews")
	prompt := latestVerifierReply(t, messages, userID)
	if !strings.Contains(prompt.Body, "why Example Trust should vouch for") {
		t.Fatalf("after picking the subject, reply = %q", prompt.Body)
	}
	return sendToVerifierBot(t, svc, messages, userID, verifierTestReason)
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

// With no verifier row granted by an operator the bot still explains the mechanism,
// but it must not pretend an application is possible.
func TestVerifierBotStartWithoutVerifierStatusIsHonest(t *testing.T) {
	cv := newFakeCustomVerification()
	svc, users, messages := newVerifierBotTestService(t, cv)
	owner := newOwner(t, users, "+7200")
	svc.SetVerifierTargets(verifierTestTargets(owner))

	if !svc.HandlesBot(domain.VerifierBotUserID) {
		t.Fatal("service should handle @verifierbot")
	}
	reply := sendToVerifierBot(t, svc, messages, owner.ID, "/start")
	for _, want := range []string{
		"THIRD-PARTY",
		"NOT the official",
		"cannot accept applications yet",
		"an operator has to give me an icon",
	} {
		if !strings.Contains(reply.Body, want) {
			t.Fatalf("/start without verifier status is missing %q: %q", want, reply.Body)
		}
	}
	if len(verifierRowsOf(reply)) != 0 {
		t.Fatalf("/start without verifier status offered buttons: %+v", reply.ReplyMarkup)
	}
	// /verify is refused the same way, and nothing is filed.
	verify := sendToVerifierBot(t, svc, messages, owner.ID, "/verify")
	if !strings.Contains(verify.Body, "cannot accept applications yet") {
		t.Fatalf("/verify without verifier status = %q", verify.Body)
	}
	if cv.creates != 0 {
		t.Fatalf("CreateRequest called %d times without verifier status", cv.creates)
	}
	// An operator kill switch on an otherwise complete row reads the same way.
	cv.activated()
	cv.settings.Enabled = false
	disabled := sendToVerifierBot(t, svc, messages, owner.ID, "/start")
	if !strings.Contains(disabled.Body, "cannot accept applications yet") || len(verifierRowsOf(disabled)) != 0 {
		t.Fatalf("/start with a disabled verifier row = %q (%+v)", disabled.Body, disabled.ReplyMarkup)
	}
}

func TestVerifierBotStartWithActiveVerifierShowsCompanyAndMark(t *testing.T) {
	cv := newFakeCustomVerification().activated()
	svc, users, messages := newVerifierBotTestService(t, cv)
	owner := newOwner(t, users, "+7201")
	svc.SetVerifierTargets(verifierTestTargets(owner))

	reply := sendToVerifierBot(t, svc, messages, owner.ID, "/start")
	for _, want := range []string{
		"THIRD-PARTY",
		"NOT the official",
		"Example Trust",
		"Identity checked by Example Trust",
		"/verify",
	} {
		if !strings.Contains(reply.Body, want) {
			t.Fatalf("/start missing %q: %q", want, reply.Body)
		}
	}
	data, found := verifierDataOf(reply, verifierApplyButtonText)
	if !found {
		t.Fatalf("/start has no apply button: %+v", reply.ReplyMarkup)
	}
	// The button is an opaque token and nothing else: it cannot name a peer at all.
	token, ok := strings.CutPrefix(string(data), verifierCallbackDataPrefix)
	if !ok || len(token) != 2*verifierOptionTokenBytes {
		t.Fatalf("callback data %q is not <prefix><token>", data)
	}
	if cv.creates != 0 {
		t.Fatalf("CreateRequest called %d times on /start", cv.creates)
	}
}

func TestVerifierBotFullFlowFilesExactlyOneRequest(t *testing.T) {
	cv := newFakeCustomVerification().activated()
	svc, users, messages := newVerifierBotTestService(t, cv)
	owner := newOwner(t, users, "+7202")
	targets := verifierTestTargets(owner)
	svc.SetVerifierTargets(targets)

	summary := runVerifierApplication(t, svc, messages, owner.ID)
	for _, want := range []string{
		"Example Trust",
		"Example News (@examplenews)",
		"Identity checked by Example Trust",
		verifierTestReason,
		"not the official",
		verifierConfirmButtonText,
	} {
		if !strings.Contains(summary.Body, want) {
			t.Fatalf("summary missing %q: %q", want, summary.Body)
		}
	}

	pressVerifierButton(t, svc, owner.ID, summary, verifierConfirmButtonText)
	filed := latestVerifierReply(t, messages, owner.ID)
	for _, want := range []string{"#701", "Example News", "operator", "/status"} {
		if !strings.Contains(filed.Body, want) {
			t.Fatalf("filed reply missing %q: %q", want, filed.Body)
		}
	}
	if cv.creates != 1 || len(cv.requests) != 1 {
		t.Fatalf("creates=%d requests=%d, want exactly one of each", cv.creates, len(cv.requests))
	}
	req := cv.requests[701]
	if req.VerifierBotID != domain.VerifierBotUserID || req.ApplicantUserID != owner.ID {
		t.Fatalf("stored request identity = %+v", req)
	}
	if req.Peer != verifierNewsPeer() {
		t.Fatalf("stored peer = %+v, want channel 7001", req.Peer)
	}
	if req.PeerUsername != "examplenews" || req.Reason != verifierTestReason {
		t.Fatalf("stored payload = %+v", req)
	}
	if req.Status != domain.CustomVerificationPending {
		t.Fatalf("stored status = %q, want pending", req.Status)
	}
	// The description is the verifier's own, resolved at grant time; the application
	// must not freeze a copy of it.
	if req.RequestedDescription != "" {
		t.Fatalf("request carries a requested description %q", req.RequestedDescription)
	}
	if req.CorrelationID == "" {
		t.Fatalf("request carries no correlation id: %+v", req)
	}
}

// Both the closed choices and the terminal action must survive a double tap: the
// same answer, and no second application.
func TestVerifierBotRepeatedPressesAreIdempotent(t *testing.T) {
	cv := newFakeCustomVerification().activated()
	svc, users, messages := newVerifierBotTestService(t, cv)
	owner := newOwner(t, users, "+7203")
	svc.SetVerifierTargets(verifierTestTargets(owner))

	intro := sendToVerifierBot(t, svc, messages, owner.ID, "/start")
	pressVerifierButton(t, svc, owner.ID, intro, verifierApplyButtonText)
	picker := latestVerifierReply(t, messages, owner.ID)

	pressVerifierButton(t, svc, owner.ID, picker, "@examplenews")
	first := latestVerifierReply(t, messages, owner.ID)
	pressVerifierButton(t, svc, owner.ID, picker, "@examplenews")
	second := latestVerifierReply(t, messages, owner.ID)
	if first.Body != second.Body {
		t.Fatalf("repeat subject press changed the answer:\nfirst  = %q\nsecond = %q", first.Body, second.Body)
	}

	summary := sendToVerifierBot(t, svc, messages, owner.ID, verifierTestReason)
	pressVerifierButton(t, svc, owner.ID, summary, verifierConfirmButtonText)
	firstFiled := latestVerifierReply(t, messages, owner.ID)
	pressVerifierButton(t, svc, owner.ID, summary, verifierConfirmButtonText)
	secondFiled := latestVerifierReply(t, messages, owner.ID)

	if firstFiled.Body != secondFiled.Body {
		t.Fatalf("repeat confirm changed the answer:\nfirst  = %q\nsecond = %q", firstFiled.Body, secondFiled.Body)
	}
	if cv.creates != 1 || len(cv.requests) != 1 {
		t.Fatalf("creates=%d requests=%d after a double confirm, want 1/1", cv.creates, len(cv.requests))
	}
	// Picking the same subject again once it is pending reports the open application
	// instead of filing a second one.
	again := sendToVerifierBot(t, svc, messages, owner.ID, "/verify")
	pressVerifierButton(t, svc, owner.ID, again, "@examplenews")
	pendingReply := latestVerifierReply(t, messages, owner.ID)
	if !strings.Contains(pendingReply.Body, "#701") || !strings.Contains(pendingReply.Body, "already with the operator") {
		t.Fatalf("re-picking a pending subject = %q", pendingReply.Body)
	}
	if cv.creates != 1 {
		t.Fatalf("creates=%d after re-picking a pending subject", cv.creates)
	}
}

func TestVerifierBotForgedCallbackTokenIsRefused(t *testing.T) {
	cv := newFakeCustomVerification().activated()
	svc, users, messages := newVerifierBotTestService(t, cv)
	owner := newOwner(t, users, "+7204")
	svc.SetVerifierTargets(verifierTestTargets(owner))

	intro := sendToVerifierBot(t, svc, messages, owner.ID, "/start")
	before := len(verifierReplies(t, messages, owner.ID))

	for _, data := range [][]byte{
		[]byte(verifierCallbackDataPrefix + "deadbeefcafe"),
		[]byte("tgt:channel:7001"),
		[]byte(verifierCallbackDataPrefix),
		[]byte(verifyCallbackDataPrefix + "deadbeefcafe"),
	} {
		answer := pressVerifierData(t, svc, owner.ID, intro, data)
		if !answer.Alert || !strings.Contains(answer.Message, "no longer active") {
			t.Fatalf("forged data %q answered %+v, want an explaining alert", data, answer)
		}
	}
	if got := len(verifierReplies(t, messages, owner.ID)); got != before {
		t.Fatalf("forged callbacks produced %d new messages", got-before)
	}
	if cv.creates != 0 || cv.revokes != 0 || len(cv.requests) != 0 {
		t.Fatalf("forged callbacks touched the service: creates=%d revokes=%d requests=%d",
			cv.creates, cv.revokes, len(cv.requests))
	}

	// A token minted for one applicant is meaningless for another.
	attacker := newOwner(t, users, "+7205")
	stolen, found := verifierDataOf(intro, verifierApplyButtonText)
	if !found {
		t.Fatal("intro has no apply button")
	}
	attackerIntro := sendToVerifierBot(t, svc, messages, attacker.ID, "/start")
	if answer := pressVerifierData(t, svc, attacker.ID, attackerIntro, stolen); !answer.Alert {
		t.Fatalf("stolen token answered %+v, want an alert", answer)
	}
	for _, req := range cv.requests {
		if req.ApplicantUserID == attacker.ID {
			t.Fatalf("stolen token created an application for the attacker: %+v", req)
		}
	}
}

func TestVerifierBotStatusListsRequestsAndMarksWithoutInternalNotes(t *testing.T) {
	cv := newFakeCustomVerification().activated()
	svc, users, messages := newVerifierBotTestService(t, cv)
	owner := newOwner(t, users, "+7206")
	svc.SetVerifierTargets(verifierTestTargets(owner))

	if empty := sendToVerifierBot(t, svc, messages, owner.ID, "/status"); empty.Body != verifierNoRequestsText {
		t.Fatalf("/status with nothing filed = %q", empty.Body)
	}

	cv.grantMark(owner.ID, verifierNewsPeer(), "Example News", "examplenews")
	cv.requests[810] = domain.CustomVerificationRequest{
		ID: 810, VerifierBotID: domain.VerifierBotUserID, ApplicantUserID: owner.ID,
		Peer: domain.Peer{Type: domain.PeerTypeUser, ID: 8002}, PeerUsername: "examplebot",
		Status: domain.CustomVerificationPending, CreatedAt: time.Date(2026, 7, 25, 9, 0, 0, 0, time.UTC),
	}
	cv.requests[811] = domain.CustomVerificationRequest{
		ID: 811, VerifierBotID: domain.VerifierBotUserID, ApplicantUserID: owner.ID,
		Peer: domain.Peer{Type: domain.PeerTypeChannel, ID: 7009}, PeerTitle: "Side Project",
		Status: domain.CustomVerificationRejected, DecisionReason: "the company behind it could not be confirmed",
		InternalNote: "operator note: obvious reseller, do not engage",
		RejectedAt:   time.Date(2026, 7, 22, 9, 0, 0, 0, time.UTC),
	}
	cv.requests[812] = domain.CustomVerificationRequest{
		ID: 812, VerifierBotID: domain.VerifierBotUserID, ApplicantUserID: owner.ID,
		Peer: domain.Peer{Type: domain.PeerTypeUser, ID: owner.ID}, PeerTitle: "Owner",
		Status: domain.CustomVerificationRevoked, DecisionReason: "the account changed hands",
		InternalNote: "operator note: internal escalation thread 4711",
		CreatedAt:    time.Date(2026, 7, 10, 9, 0, 0, 0, time.UTC),
	}

	reply := sendToVerifierBot(t, svc, messages, owner.ID, "/status")
	for _, want := range []string{
		"#701", "Example News (@examplenews)", "approved, the mark was granted", "Mark: live",
		"Identity checked by Example Trust", "granted 2026-07-20",
		"#810", "@examplebot", "waiting for an operator",
		"#811", "Side Project", "not approved", "the company behind it could not be confirmed",
		"#812", "the mark was taken away", "the account changed hands",
		"/revoke",
	} {
		if !strings.Contains(reply.Body, want) {
			t.Fatalf("/status missing %q: %q", want, reply.Body)
		}
	}
	for _, forbidden := range []string{"obvious reseller", "escalation thread", "operator note"} {
		if strings.Contains(reply.Body, forbidden) {
			t.Fatalf("/status leaked an internal note (%q): %q", forbidden, reply.Body)
		}
	}

	// An approved application whose mark is gone is reported as such rather than
	// claiming a badge the peer does not carry.
	delete(cv.marks, verifierNewsPeer())
	stale := sendToVerifierBot(t, svc, messages, owner.ID, "/status")
	if !strings.Contains(stale.Body, "Mark: not on the peer any more") {
		t.Fatalf("/status after the mark went away = %q", stale.Body)
	}
}

func TestVerifierBotRevokeAsksForConfirmationThenRemovesTheMark(t *testing.T) {
	cv := newFakeCustomVerification().activated()
	svc, users, messages := newVerifierBotTestService(t, cv)
	owner := newOwner(t, users, "+7207")
	svc.SetVerifierTargets(verifierTestTargets(owner))

	if nothing := sendToVerifierBot(t, svc, messages, owner.ID, "/revoke"); nothing.Body != verifierNothingToRevokeText {
		t.Fatalf("/revoke with no marks = %q", nothing.Body)
	}

	granted := cv.grantMark(owner.ID, verifierNewsPeer(), "Example News", "examplenews")
	picker := sendToVerifierBot(t, svc, messages, owner.ID, "/revoke")
	if !strings.Contains(picker.Body, "Which mark should I remove") {
		t.Fatalf("/revoke = %q", picker.Body)
	}
	pressVerifierButton(t, svc, owner.ID, picker, "@examplenews")
	confirm := latestVerifierReply(t, messages, owner.ID)
	rendered := confirm.Body + "\n" + strings.Join(buttonLabels(confirm), "\n")
	for _, want := range []string{"Remove my mark from", "Example News", verifierRevokeButtonText, verifierRevokeCancelButtonText} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("revoke confirmation missing %q: %q", want, rendered)
		}
	}
	// Nothing is removed until the confirmation is pressed.
	if cv.revokes != 0 {
		t.Fatalf("RevokeMark called %d times before confirmation", cv.revokes)
	}

	pressVerifierButton(t, svc, owner.ID, confirm, verifierRevokeButtonText)
	removed := latestVerifierReply(t, messages, owner.ID)
	if !strings.Contains(removed.Body, "removed from") || !strings.Contains(removed.Body, "Example News") {
		t.Fatalf("revoked reply = %q", removed.Body)
	}
	if cv.revokes != 1 {
		t.Fatalf("RevokeMark called %d times, want 1", cv.revokes)
	}
	// The store signature carries no actor or reason: 0154 has no revocation audit
	// column, so the dialog logs them instead. What must be true here is that the
	// mark of this verifier on this peer is gone.
	if len(cv.revokedPeers) != 1 {
		t.Fatalf("revoked peers = %+v, want exactly the picked peer", cv.revokedPeers)
	}
	if _, still := cv.marks[verifierNewsPeer()]; still {
		t.Fatal("the mark is still granted after a confirmed revoke")
	}
	if got := cv.requests[granted.ID].Status; got != domain.CustomVerificationRevoked {
		t.Fatalf("application status after revoke = %q", got)
	}

	// A second press of the same confirmation replays the same answer and does not
	// call the service again.
	pressVerifierButton(t, svc, owner.ID, confirm, verifierRevokeButtonText)
	repeat := latestVerifierReply(t, messages, owner.ID)
	if repeat.Body != removed.Body {
		t.Fatalf("repeat revoke changed the answer:\nfirst  = %q\nsecond = %q", removed.Body, repeat.Body)
	}
	if cv.revokes != 1 {
		t.Fatalf("RevokeMark called %d times after a double press, want 1", cv.revokes)
	}
}

// A confirmation button names the peer it was rendered for, so pressing an old one
// acts on that peer and never on whatever the dialog is pointing at now.
func TestVerifierBotStaleRevokeConfirmationActsOnItsOwnPeer(t *testing.T) {
	cv := newFakeCustomVerification().activated()
	svc, users, messages := newVerifierBotTestService(t, cv)
	owner := newOwner(t, users, "+7214")
	svc.SetVerifierTargets(verifierTestTargets(owner))

	botPeer := domain.Peer{Type: domain.PeerTypeUser, ID: 8002}
	cv.grantMark(owner.ID, verifierNewsPeer(), "Example News", "examplenews")
	cv.grantMark(owner.ID, botPeer, "Example Bot", "examplebot")

	// Confirm screen for the channel, then walk away and open the one for the bot.
	pressVerifierButton(t, svc, owner.ID, sendToVerifierBot(t, svc, messages, owner.ID, "/revoke"), "@examplenews")
	channelConfirm := latestVerifierReply(t, messages, owner.ID)
	pressVerifierButton(t, svc, owner.ID, sendToVerifierBot(t, svc, messages, owner.ID, "/revoke"), "@examplebot")

	// The dialog now points at the bot; the stale button still means the channel.
	pressVerifierButton(t, svc, owner.ID, channelConfirm, verifierRevokeButtonText)
	removed := latestVerifierReply(t, messages, owner.ID)
	if !strings.Contains(removed.Body, "@examplenews") || strings.Contains(removed.Body, "@examplebot") {
		t.Fatalf("stale confirmation removed the wrong peer: %q", removed.Body)
	}
	if len(cv.revokedPeers) != 1 || cv.revokedPeers[0] != verifierNewsPeer() {
		t.Fatalf("revoked peers = %+v, want only the channel", cv.revokedPeers)
	}
	if _, still := cv.marks[botPeer]; !still {
		t.Fatal("the bot lost its mark to a confirmation rendered for the channel")
	}
}

// A subject that already carries the mark is told so, and offered the only action
// that makes sense on it.
func TestVerifierBotAlreadyMarkedSubjectOffersRemoval(t *testing.T) {
	cv := newFakeCustomVerification().activated()
	svc, users, messages := newVerifierBotTestService(t, cv)
	owner := newOwner(t, users, "+7215")
	svc.SetVerifierTargets(verifierTestTargets(owner))

	cv.grantMark(owner.ID, verifierNewsPeer(), "Example News", "examplenews")
	picker := sendToVerifierBot(t, svc, messages, owner.ID, "/verify")
	pressVerifierButton(t, svc, owner.ID, picker, "@examplenews")
	reply := latestVerifierReply(t, messages, owner.ID)
	for _, want := range []string{"already carries my mark", "Identity checked by Example Trust", "/revoke"} {
		if !strings.Contains(reply.Body, want) {
			t.Fatalf("already-marked reply missing %q: %q", want, reply.Body)
		}
	}
	if cv.creates != 0 {
		t.Fatalf("creates=%d for an already-marked subject", cv.creates)
	}
	pressVerifierButton(t, svc, owner.ID, reply, verifierRevokeMenuButtonText)
	if got := latestVerifierReply(t, messages, owner.ID); !strings.Contains(got.Body, "Which mark should I remove") {
		t.Fatalf("removal button did not open the picker: %q", got.Body)
	}
}

// buttonLabels flattens a message's keyboard so a test can assert on the labels
// without caring about the row layout.
func buttonLabels(msg domain.Message) []string {
	var out []string
	for _, row := range verifierRowsOf(msg) {
		for _, button := range row {
			out = append(out, button.Text)
		}
	}
	return out
}

func TestVerifierBotHelpAndIdleText(t *testing.T) {
	// Nothing injected at all: this is the deployment that never wired third-party
	// verification.
	svc, users, messages := newVerifierBotTestService(t, nil)
	owner := newOwner(t, users, "+7208")

	// /help answers even with no verification service wired at all: it describes the
	// bot rather than reading any state.
	help := sendToVerifierBot(t, svc, messages, owner.ID, "/help")
	if help.Body != verifierBotHelpText {
		t.Fatalf("/help = %q", help.Body)
	}
	for _, want := range []string{"/start", "/verify", "/status", "/revoke", "/help", "not the official"} {
		if !strings.Contains(help.Body, want) {
			t.Fatalf("/help missing %q", want)
		}
	}
	if idle := sendToVerifierBot(t, svc, messages, owner.ID, "hello there"); idle.Body != verifierBotIdleText {
		t.Fatalf("idle chatter = %q", idle.Body)
	}
	unknown := sendToVerifierBot(t, svc, messages, owner.ID, "/teleport")
	if !strings.Contains(unknown.Body, "do not know that command") {
		t.Fatalf("unknown command = %q", unknown.Body)
	}

	// With no service wired, every stateful command says so instead of half-running.
	for _, cmd := range []string{"/start", "/verify", "/status", "/revoke"} {
		if reply := sendToVerifierBot(t, svc, messages, owner.ID, cmd); reply.Body != verifierUnavailableText {
			t.Fatalf("%s without a service = %q", cmd, reply.Body)
		}
	}
}

func TestVerifierBotGlobalCommandsWorkMidStep(t *testing.T) {
	cv := newFakeCustomVerification().activated()
	svc, users, messages := newVerifierBotTestService(t, cv)
	owner := newOwner(t, users, "+7209")
	svc.SetVerifierTargets(verifierTestTargets(owner))

	intro := sendToVerifierBot(t, svc, messages, owner.ID, "/start")
	pressVerifierButton(t, svc, owner.ID, intro, verifierApplyButtonText)
	pressVerifierButton(t, svc, owner.ID, latestVerifierReply(t, messages, owner.ID), "@examplenews")

	// /help and /status in the middle of the reason step answer and keep the step.
	if help := sendToVerifierBot(t, svc, messages, owner.ID, "/help"); help.Body != verifierBotHelpText {
		t.Fatalf("/help mid-step = %q", help.Body)
	}
	if status := sendToVerifierBot(t, svc, messages, owner.ID, "/status"); status.Body != verifierNoRequestsText {
		t.Fatalf("/status mid-step = %q", status.Body)
	}
	// An unknown command is never swallowed as the reason.
	if unknown := sendToVerifierBot(t, svc, messages, owner.ID, "/nope"); !strings.Contains(unknown.Body, "do not know that command") {
		t.Fatalf("unknown command mid-step = %q", unknown.Body)
	}
	// A too-short reason is explained rather than accepted.
	if short := sendToVerifierBot(t, svc, messages, owner.ID, "please"); !strings.Contains(short.Body, "at least") {
		t.Fatalf("short reason = %q", short.Body)
	}

	summary := sendToVerifierBot(t, svc, messages, owner.ID, verifierTestReason)
	if !strings.Contains(summary.Body, verifierTestReason) || !strings.Contains(summary.Body, "Example Trust") {
		t.Fatalf("reason not accepted after the global commands: %q", summary.Body)
	}
	// A button-only step says so instead of eating the text as a field.
	if chatter := sendToVerifierBot(t, svc, messages, owner.ID, "yes do it"); chatter.Body != verifierPickButtonsText {
		t.Fatalf("text at the confirm step = %q", chatter.Body)
	}
	// /cancel drops the dialog without filing anything.
	cancelled := sendToVerifierBot(t, svc, messages, owner.ID, "/cancel")
	if !strings.Contains(cancelled.Body, "nothing was filed") {
		t.Fatalf("/cancel mid-dialog = %q", cancelled.Body)
	}
	if cv.creates != 0 {
		t.Fatalf("creates=%d after cancelling", cv.creates)
	}
	if again := sendToVerifierBot(t, svc, messages, owner.ID, "/cancel"); again.Body != verifierNothingToCancelText {
		t.Fatalf("second /cancel = %q", again.Body)
	}
}

func TestVerifierBotWithoutTargetsExplainsInsteadOfFailing(t *testing.T) {
	cv := newFakeCustomVerification().activated()
	svc, users, messages := newVerifierBotTestService(t, cv, WithVerifierTargets(&fakeVerifierTargets{}))
	owner := newOwner(t, users, "+7210")

	if reply := sendToVerifierBot(t, svc, messages, owner.ID, "/verify"); reply.Body != verifierNoTargetsText {
		t.Fatalf("/verify with no candidates = %q", reply.Body)
	}
	// A directory outage is reported honestly, not as an empty picker.
	broken := &fakeVerifierTargets{err: context.DeadlineExceeded}
	svc.SetVerifierTargets(broken)
	if reply := sendToVerifierBot(t, svc, messages, owner.ID, "/verify"); reply.Body != verifierNoTargetSourceText {
		t.Fatalf("/verify with a broken directory = %q", reply.Body)
	}
	if cv.creates != 0 {
		t.Fatalf("creates=%d, want 0", cv.creates)
	}
}

// A built-in service account is never offered as a subject, and the enumeration's
// official-verification verdicts are ignored: they answer a different question.
func TestVerifierBotPickerFiltersSystemPeersAndIgnoresOfficialEligibility(t *testing.T) {
	cv := newFakeCustomVerification().activated()
	svc, users, messages := newVerifierBotTestService(t, cv, WithVerifierTargets(&fakeVerifierTargets{targets: []domain.VerificationTarget{
		{Type: domain.VerificationTargetBot, ID: domain.VerifyBotUserID, Username: "verifybot", Eligible: true},
		{Type: domain.VerificationTargetChannel, ID: 7001, Title: "Example News", Username: "examplenews",
			Eligible: false, Reason: domain.ErrVerificationTargetAlreadyVerified.Error()},
	}}))
	owner := newOwner(t, users, "+7211")

	picker := sendToVerifierBot(t, svc, messages, owner.ID, "/verify")
	labels := buttonLabels(picker)
	for _, label := range labels {
		if strings.Contains(label, "verifybot") {
			t.Fatalf("picker offered a built-in service account: %v", labels)
		}
	}
	if _, found := verifierDataOf(picker, "@examplenews"); !found {
		t.Fatalf("picker dropped an officially ineligible peer: %v", labels)
	}
}

func TestVerifierBotSendVerificationDecisionCoversEveryOutcome(t *testing.T) {
	cv := newFakeCustomVerification().activated()
	svc, users, messages := newVerifierBotTestService(t, cv)
	owner := newOwner(t, users, "+7212")
	ctx := context.Background()

	req := domain.CustomVerificationRequest{
		ID: 909, VerifierBotID: domain.VerifierBotUserID, ApplicantUserID: owner.ID,
		Peer: verifierNewsPeer(), PeerTitle: "Example News", PeerUsername: "examplenews",
		DecisionReason: "the company behind it could not be confirmed",
		InternalNote:   "operator note: repeat applicant, escalate next time",
	}

	req.Status = domain.CustomVerificationApproved
	if err := svc.SendVerificationDecision(ctx, owner.ID, req); err != nil {
		t.Fatalf("approved decision: %v", err)
	}
	approved := latestVerifierReply(t, messages, owner.ID)
	for _, want := range []string{"#909", "Example News", "@examplenews", "approved", "not the official", "/revoke"} {
		if !strings.Contains(approved.Body, want) {
			t.Fatalf("approved decision missing %q: %q", want, approved.Body)
		}
	}

	req.Status = domain.CustomVerificationRejected
	if err := svc.SendVerificationDecision(ctx, owner.ID, req); err != nil {
		t.Fatalf("rejected decision: %v", err)
	}
	rejected := latestVerifierReply(t, messages, owner.ID)
	for _, want := range []string{"#909", "not approved", "could not be confirmed", "/verify"} {
		if !strings.Contains(rejected.Body, want) {
			t.Fatalf("rejected decision missing %q: %q", want, rejected.Body)
		}
	}

	req.Status = domain.CustomVerificationRevoked
	if err := svc.SendVerificationDecision(ctx, owner.ID, req); err != nil {
		t.Fatalf("revoked decision: %v", err)
	}
	revoked := latestVerifierReply(t, messages, owner.ID)
	for _, want := range []string{"taken off", "Example News", "could not be confirmed"} {
		if !strings.Contains(revoked.Body, want) {
			t.Fatalf("revoked decision missing %q: %q", want, revoked.Body)
		}
	}

	// The operator's private note never reaches the applicant, under any outcome.
	for _, msg := range verifierReplies(t, messages, owner.ID) {
		for _, forbidden := range []string{"repeat applicant", "escalate next time", "operator note"} {
			if strings.Contains(msg.Body, forbidden) {
				t.Fatalf("decision message leaked the internal note (%q): %q", forbidden, msg.Body)
			}
		}
	}

	// Pending is not a decision, and neither is an unmodelled status: both are
	// reported so the queued notification stays pending instead of being marked
	// delivered as an empty message.
	before := len(verifierReplies(t, messages, owner.ID))
	for _, status := range []domain.CustomVerificationRequestStatus{domain.CustomVerificationPending, "teleported"} {
		req.Status = status
		if err := svc.SendVerificationDecision(ctx, owner.ID, req); err == nil {
			t.Fatalf("status %q reported a delivered decision", status)
		}
	}
	req.Status = domain.CustomVerificationApproved
	if err := svc.SendVerificationDecision(ctx, 0, req); err == nil {
		t.Fatal("empty recipient reported success")
	}
	if got := len(verifierReplies(t, messages, owner.ID)); got != before {
		t.Fatalf("undeliverable decisions sent %d messages", got-before)
	}
}

// The callback router must claim @verifierbot clicks and only those.
func TestVerifierBotCallbackRouting(t *testing.T) {
	cv := newFakeCustomVerification().activated()
	svc, users, messages := newVerifierBotTestService(t, cv)
	owner := newOwner(t, users, "+7213")
	svc.SetVerifierTargets(verifierTestTargets(owner))

	// No dialog state at all: the click is claimed and explained, never left hanging.
	answer, handled, err := svc.OnCallbackQuery(context.Background(), domain.BotCallbackQuery{
		BotUserID: domain.VerifierBotUserID, UserID: owner.ID,
		Data: []byte(verifierCallbackDataPrefix + "0011223344"),
	})
	if err != nil || !handled {
		t.Fatalf("callback without state: handled=%v err=%v", handled, err)
	}
	if !answer.Alert || !strings.Contains(answer.Message, "no longer active") {
		t.Fatalf("callback without state = %+v", answer)
	}
	if _, handled, err := svc.OnCallbackQuery(context.Background(), domain.BotCallbackQuery{
		BotUserID: 555111, UserID: owner.ID, Data: []byte(verifierCallbackDataPrefix + "x"),
	}); handled || err != nil {
		t.Fatalf("foreign bot callback handled=%v err=%v, want (false, nil)", handled, err)
	}
	// A click from the bot itself is not a dialog action.
	if _, handled, err := svc.OnCallbackQuery(context.Background(), domain.BotCallbackQuery{
		BotUserID: domain.VerifierBotUserID, UserID: domain.VerifierBotUserID,
		Data: []byte(verifierCallbackDataPrefix + "x"),
	}); handled || err != nil {
		t.Fatalf("self callback handled=%v err=%v, want (false, nil)", handled, err)
	}
	if len(verifierReplies(t, messages, owner.ID)) != 0 {
		t.Fatal("refused callbacks wrote messages")
	}
}
