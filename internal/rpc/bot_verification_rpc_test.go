package rpc

import (
	"context"
	"testing"

	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"go.uber.org/zap/zaptest"

	botsapp "telesrv/internal/app/bots"
	appchannels "telesrv/internal/app/channels"
	appusers "telesrv/internal/app/users"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

// fakeBotVerifications is an in-memory BotVerificationService. It enforces the same
// domain rules the real service must (verifier status, the operator kill switch,
// domain.BotVerifierSettings.DescriptionFor, the per-verifier bound and the
// "nothing changed" answer), so the RPC tests exercise the real error mapping
// instead of hand-rolled sentinels.
type fakeBotVerifications struct {
	marks    map[domain.Peer]domain.CustomVerification
	settings map[int64]domain.BotVerifierSettings
	// limit, when positive, bounds how many peers one verifier may mark.
	limit int
	// err, when set, fails every read. Used for the degradation tests.
	err error
	// Call counters let the tests assert the batch fan-out (no N+1).
	peerCalls          int
	batchCalls         int
	settingsCalls      int
	settingsBatchCalls int
	setCalls           int
	// notifier mirrors production wiring: the application service owns the badge
	// push for all three drivers (this RPC, the bot dialog, the admin panel), so the
	// fake pushes here exactly where the real service does.
	notifier interface {
		NotifyPeerBotVerification(ctx context.Context, peer domain.Peer) error
	}
}

func newFakeBotVerifications() *fakeBotVerifications {
	return &fakeBotVerifications{
		marks:    make(map[domain.Peer]domain.CustomVerification),
		settings: make(map[int64]domain.BotVerifierSettings),
	}
}

func (f *fakeBotVerifications) PeerVerification(_ context.Context, peer domain.Peer) (domain.CustomVerification, error) {
	f.peerCalls++
	if f.err != nil {
		return domain.CustomVerification{}, f.err
	}
	mark, ok := f.marks[peer]
	if !ok {
		return domain.CustomVerification{}, domain.ErrCustomVerificationNotFound
	}
	return mark, nil
}

func (f *fakeBotVerifications) PeerVerificationBatch(_ context.Context, peers []domain.Peer) (map[domain.Peer]domain.CustomVerification, error) {
	f.batchCalls++
	if f.err != nil {
		return nil, f.err
	}
	out := make(map[domain.Peer]domain.CustomVerification, len(peers))
	for _, peer := range peers {
		if mark, ok := f.marks[peer]; ok {
			out[peer] = mark
		}
	}
	return out, nil
}

func (f *fakeBotVerifications) VerifierSettings(_ context.Context, botID int64) (domain.BotVerifierSettings, error) {
	f.settingsCalls++
	if f.err != nil {
		return domain.BotVerifierSettings{}, f.err
	}
	settings, ok := f.settings[botID]
	if !ok {
		return domain.BotVerifierSettings{}, domain.ErrVerifierNotFound
	}
	return settings, nil
}

func (f *fakeBotVerifications) VerifierSettingsBatch(_ context.Context, botIDs []int64) (map[int64]domain.BotVerifierSettings, error) {
	f.settingsBatchCalls++
	if f.err != nil {
		return nil, f.err
	}
	out := make(map[int64]domain.BotVerifierSettings, len(botIDs))
	for _, id := range botIDs {
		if settings, ok := f.settings[id]; ok {
			out[id] = settings
		}
	}
	return out, nil
}

func (f *fakeBotVerifications) SetCustomVerification(_ context.Context, req domain.SetCustomVerificationRequest) (bool, error) {
	f.setCalls++
	if f.err != nil {
		return false, f.err
	}
	if err := req.Validate(); err != nil {
		return false, err
	}
	settings, ok := f.settings[req.VerifierBotID]
	if !ok {
		return false, domain.ErrVerifierNotFound
	}
	if !settings.Enabled {
		return false, domain.ErrVerifierForbidden
	}
	existing, marked := f.marks[req.Peer]
	if !req.Enabled {
		// A revoke only touches this verifier's own mark, and a repeated revoke is a
		// no-op rather than an error.
		if !marked || existing.VerifierBotID != req.VerifierBotID {
			return false, nil
		}
		delete(f.marks, req.Peer)
		f.notify(req.Peer)
		return true, nil
	}
	description, err := settings.DescriptionFor(req.CustomDescription)
	if err != nil {
		return false, err
	}
	if marked && existing.VerifierBotID == req.VerifierBotID &&
		existing.IconDocumentID == settings.IconDocumentID && existing.Description == description {
		return false, nil
	}
	if f.limit > 0 && !marked && f.countFor(req.VerifierBotID) >= f.limit {
		return false, domain.ErrCustomVerificationLimit
	}
	f.marks[req.Peer] = domain.CustomVerification{
		VerifierBotID:   req.VerifierBotID,
		Peer:            req.Peer,
		IconDocumentID:  settings.IconDocumentID,
		Description:     description,
		GrantedByUserID: req.CallerUserID,
	}
	f.notify(req.Peer)
	return true, nil
}

// notify reproduces the service's post-commit push.
func (f *fakeBotVerifications) notify(peer domain.Peer) {
	if f.notifier == nil {
		return
	}
	_ = f.notifier.NotifyPeerBotVerification(context.Background(), peer)
}

func (f *fakeBotVerifications) countFor(verifierBotID int64) int {
	n := 0
	for _, mark := range f.marks {
		if mark.VerifierBotID == verifierBotID {
			n++
		}
	}
	return n
}

var _ BotVerificationService = (*fakeBotVerifications)(nil)

// botVerificationFixture wires the real users/bots/channels services next to the
// fake verification service, so the ownership checks bots.setCustomVerification
// depends on are the production ones.
type botVerificationFixture struct {
	router   *Router
	verify   *fakeBotVerifications
	bots     *botsapp.Service
	owner    domain.User
	stranger domain.User
	target   domain.User
	bot      domain.User
	foreign  domain.User
}

func newBotVerificationFixture(t *testing.T, verify BotVerificationService) botVerificationFixture {
	t.Helper()
	ctx := context.Background()
	userStore := memory.NewUserStore()
	botStore := memory.NewBotStore(userStore)
	dialogs := memory.NewDialogStore()
	messageStore := memory.NewMessageStore(dialogs)
	bots := botsapp.NewService(userStore, botStore, messageStore)
	channelStore := memory.NewChannelStore()
	channels := appchannels.NewService(channelStore, appchannels.WithBotProfileResolver(bots))

	owner, err := userStore.Create(ctx, domain.User{AccessHash: 9101, Phone: "15550009101", FirstName: "Owner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	stranger, err := userStore.Create(ctx, domain.User{AccessHash: 9102, Phone: "15550009102", FirstName: "Stranger"})
	if err != nil {
		t.Fatalf("create stranger: %v", err)
	}
	target, err := userStore.Create(ctx, domain.User{AccessHash: 9103, Phone: "15550009103", FirstName: "Target", Username: "target_shop"})
	if err != nil {
		t.Fatalf("create target: %v", err)
	}
	bot, _, err := bots.CreateBot(ctx, owner.ID, "Verifier Bot", "verifier_shape_bot")
	if err != nil {
		t.Fatalf("create verifier bot: %v", err)
	}
	foreign, _, err := bots.CreateBot(ctx, stranger.ID, "Foreign Bot", "foreign_shape_bot")
	if err != nil {
		t.Fatalf("create foreign bot: %v", err)
	}
	fake, _ := verify.(*fakeBotVerifications)
	deps := Deps{
		Users:    appusers.NewService(userStore),
		Bots:     bots,
		Channels: channels,
	}
	if verify != nil {
		deps.BotVerifications = verify
	}
	return botVerificationFixture{
		router:   New(Config{DC: 2, IP: "127.0.0.1", Port: 2398}, deps, zaptest.NewLogger(t), clock.System),
		verify:   fake,
		bots:     bots,
		owner:    owner,
		stranger: stranger,
		target:   target,
		bot:      bot,
		foreign:  foreign,
	}
}

// enableVerifier grants the fake verifier status the way the operator would.
func (f botVerificationFixture) enableVerifier(botID, icon int64, canModifyDescription bool) {
	f.verify.settings[botID] = domain.BotVerifierSettings{
		BotID:                      botID,
		IconDocumentID:             icon,
		CompanyName:                "Acme Trust",
		DefaultDescription:         "Verified by Acme Trust",
		CanModifyCustomDescription: canModifyDescription,
		Enabled:                    true,
	}
}

func setCustomVerificationRequest(peer tg.InputPeerClass, bot tg.InputUserClass, enabled bool, description string) *tg.BotsSetCustomVerificationRequest {
	req := &tg.BotsSetCustomVerificationRequest{Peer: peer}
	if bot != nil {
		req.SetBot(bot)
	}
	req.SetEnabled(enabled)
	if description != "" {
		req.SetCustomDescription(description)
	}
	return req
}

// TestBotsSetCustomVerificationOwnerGrantsAndRevokes is the main happy path: the
// owner of a verifier bot marks a peer, the repeat call reports "nothing changed",
// and the revoke removes exactly that mark.
//
// The Bool result carries that distinction, which is why it is asserted rather than
// just err == nil: a retrying client must not read "already applied" as a failure.
func TestBotsSetCustomVerificationOwnerGrantsAndRevokes(t *testing.T) {
	f := newBotVerificationFixture(t, newFakeBotVerifications())
	f.enableVerifier(f.bot.ID, 5550001, true)
	ownerCtx := WithUserID(context.Background(), f.owner.ID)
	peer := domain.Peer{Type: domain.PeerTypeUser, ID: f.target.ID}

	ok, err := f.router.onBotsSetCustomVerification(ownerCtx,
		setCustomVerificationRequest(inputPeerUser(f.target), inputUser(f.bot), true, ""))
	if err != nil || !ok {
		t.Fatalf("owner grant = %v,%v, want true,nil", ok, err)
	}
	mark, exists := f.verify.marks[peer]
	if !exists {
		t.Fatalf("grant stored nothing: %+v", f.verify.marks)
	}
	if mark.VerifierBotID != f.bot.ID || mark.IconDocumentID != 5550001 {
		t.Fatalf("stored mark = %+v, want verifier %d icon 5550001", mark, f.bot.ID)
	}
	if mark.Description != "Verified by Acme Trust" {
		t.Fatalf("stored description = %q, want the verifier default", mark.Description)
	}
	if mark.GrantedByUserID != f.owner.ID {
		t.Fatalf("granted by = %d, want calling owner %d", mark.GrantedByUserID, f.owner.ID)
	}

	// Same state again: applied already, so Bool false and no error.
	repeat, err := f.router.onBotsSetCustomVerification(ownerCtx,
		setCustomVerificationRequest(inputPeerUser(f.target), inputUser(f.bot), true, ""))
	if err != nil || repeat {
		t.Fatalf("repeated grant = %v,%v, want false,nil", repeat, err)
	}

	revoked, err := f.router.onBotsSetCustomVerification(ownerCtx,
		setCustomVerificationRequest(inputPeerUser(f.target), inputUser(f.bot), false, ""))
	if err != nil || !revoked {
		t.Fatalf("revoke = %v,%v, want true,nil", revoked, err)
	}
	if _, exists := f.verify.marks[peer]; exists {
		t.Fatalf("revoke left the mark behind: %+v", f.verify.marks)
	}
	repeatRevoke, err := f.router.onBotsSetCustomVerification(ownerCtx,
		setCustomVerificationRequest(inputPeerUser(f.target), inputUser(f.bot), false, ""))
	if err != nil || repeatRevoke {
		t.Fatalf("repeated revoke = %v,%v, want false,nil", repeatRevoke, err)
	}
}

// TestBotsSetCustomVerificationBotCallerActsAsItself covers the other caller the TL
// constructor allows: a bot invoking without bot:flags.0, which must resolve to the
// calling bot's own id.
func TestBotsSetCustomVerificationBotCallerActsAsItself(t *testing.T) {
	f := newBotVerificationFixture(t, newFakeBotVerifications())
	f.enableVerifier(f.bot.ID, 5550002, true)
	botCtx := WithUserID(context.Background(), f.bot.ID)

	ok, err := f.router.onBotsSetCustomVerification(botCtx,
		setCustomVerificationRequest(inputPeerUser(f.target), nil, true, ""))
	if err != nil || !ok {
		t.Fatalf("bot self grant = %v,%v, want true,nil", ok, err)
	}
	mark := f.verify.marks[domain.Peer{Type: domain.PeerTypeUser, ID: f.target.ID}]
	if mark.VerifierBotID != f.bot.ID {
		t.Fatalf("verifier id = %d, want the calling bot %d", mark.VerifierBotID, f.bot.ID)
	}
	if mark.GrantedByUserID != f.bot.ID {
		t.Fatalf("granted by = %d, want the calling bot %d", mark.GrantedByUserID, f.bot.ID)
	}
}

// TestBotsSetCustomVerificationRejectsForeignBot pins the ownership boundary: naming
// somebody else's bot is BOT_INVALID, and nothing reaches the service.
func TestBotsSetCustomVerificationRejectsForeignBot(t *testing.T) {
	f := newBotVerificationFixture(t, newFakeBotVerifications())
	f.enableVerifier(f.foreign.ID, 5550003, true)
	ownerCtx := WithUserID(context.Background(), f.owner.ID)

	ok, err := f.router.onBotsSetCustomVerification(ownerCtx,
		setCustomVerificationRequest(inputPeerUser(f.target), inputUser(f.foreign), true, ""))
	if ok || !tgerr.Is(err, "BOT_INVALID") {
		t.Fatalf("foreign bot = %v,%v, want false,BOT_INVALID", ok, err)
	}
	if f.verify.setCalls != 0 {
		t.Fatalf("foreign bot reached the service %d times", f.verify.setCalls)
	}
	if len(f.verify.marks) != 0 {
		t.Fatalf("foreign bot minted a mark: %+v", f.verify.marks)
	}
}

// TestBotsSetCustomVerificationRejectsNonVerifier covers a bot the operator never
// granted verifier status, and a verifier the operator switched off: both are
// BOT_VERIFIER_FORBIDDEN, because in both cases the bot may not verify anything.
func TestBotsSetCustomVerificationRejectsNonVerifier(t *testing.T) {
	f := newBotVerificationFixture(t, newFakeBotVerifications())
	ownerCtx := WithUserID(context.Background(), f.owner.ID)

	ok, err := f.router.onBotsSetCustomVerification(ownerCtx,
		setCustomVerificationRequest(inputPeerUser(f.target), inputUser(f.bot), true, ""))
	if ok || !tgerr.Is(err, "BOT_VERIFIER_FORBIDDEN") {
		t.Fatalf("non-verifier = %v,%v, want false,BOT_VERIFIER_FORBIDDEN", ok, err)
	}

	disabled := f.verify.settings
	f.enableVerifier(f.bot.ID, 5550004, true)
	settings := disabled[f.bot.ID]
	settings.Enabled = false
	disabled[f.bot.ID] = settings
	ok, err = f.router.onBotsSetCustomVerification(ownerCtx,
		setCustomVerificationRequest(inputPeerUser(f.target), inputUser(f.bot), true, ""))
	if ok || !tgerr.Is(err, "BOT_VERIFIER_FORBIDDEN") {
		t.Fatalf("disabled verifier = %v,%v, want false,BOT_VERIFIER_FORBIDDEN", ok, err)
	}
	if len(f.verify.marks) != 0 {
		t.Fatalf("forbidden verifier minted a mark: %+v", f.verify.marks)
	}
}

// TestBotsSetCustomVerificationRejectsInvalidPeer covers both invalid-target paths:
// an unresolvable inputPeer at the edge, and a peer the domain refuses to verify.
// Both must read PEER_ID_INVALID to a client -- the verifier is fine, the target is
// not.
func TestBotsSetCustomVerificationRejectsInvalidPeer(t *testing.T) {
	f := newBotVerificationFixture(t, newFakeBotVerifications())
	f.enableVerifier(f.bot.ID, 5550005, true)
	ownerCtx := WithUserID(context.Background(), f.owner.ID)

	ok, err := f.router.onBotsSetCustomVerification(ownerCtx,
		setCustomVerificationRequest(&tg.InputPeerEmpty{}, inputUser(f.bot), true, ""))
	if ok || !tgerr.Is(err, "PEER_ID_INVALID") {
		t.Fatalf("empty peer = %v,%v, want false,PEER_ID_INVALID", ok, err)
	}
	if f.verify.setCalls != 0 {
		t.Fatalf("empty peer reached the service %d times", f.verify.setCalls)
	}

	// A stored-state rejection maps to the same code, so a client cannot tell the
	// two apart and cannot probe which peers exist.
	f.verify.err = domain.ErrCustomVerificationTargetInvalid
	ok, err = f.router.onBotsSetCustomVerification(ownerCtx,
		setCustomVerificationRequest(inputPeerUser(f.target), inputUser(f.bot), true, ""))
	if ok || !tgerr.Is(err, "PEER_ID_INVALID") {
		t.Fatalf("domain-rejected peer = %v,%v, want false,PEER_ID_INVALID", ok, err)
	}
}

// TestBotsSetCustomVerificationRejectsForbiddenDescription pins the
// can_modify_custom_description permission: a verifier that may only apply the
// operator default is refused when it supplies its own text, and the refusal is a
// 400 (drop the description and the same call succeeds), not the 403 that means the
// bot may not verify at all.
func TestBotsSetCustomVerificationRejectsForbiddenDescription(t *testing.T) {
	f := newBotVerificationFixture(t, newFakeBotVerifications())
	f.enableVerifier(f.bot.ID, 5550006, false)
	ownerCtx := WithUserID(context.Background(), f.owner.ID)

	ok, err := f.router.onBotsSetCustomVerification(ownerCtx,
		setCustomVerificationRequest(inputPeerUser(f.target), inputUser(f.bot), true, "Hand-written blurb"))
	if ok || !tgerr.Is(err, "BOT_VERIFIER_DESCRIPTION_FORBIDDEN") {
		t.Fatalf("forbidden description = %v,%v, want false,BOT_VERIFIER_DESCRIPTION_FORBIDDEN", ok, err)
	}
	if code, _ := tgerr.AsType(err, "BOT_VERIFIER_DESCRIPTION_FORBIDDEN"); code == nil || code.Code != 400 {
		t.Fatalf("forbidden description error = %+v, want code 400", code)
	}
	if len(f.verify.marks) != 0 {
		t.Fatalf("rejected description still minted a mark: %+v", f.verify.marks)
	}

	// Without the description the very same call goes through with the default text.
	ok, err = f.router.onBotsSetCustomVerification(ownerCtx,
		setCustomVerificationRequest(inputPeerUser(f.target), inputUser(f.bot), true, ""))
	if err != nil || !ok {
		t.Fatalf("default description = %v,%v, want true,nil", ok, err)
	}
	if got := f.verify.marks[domain.Peer{Type: domain.PeerTypeUser, ID: f.target.ID}].Description; got != "Verified by Acme Trust" {
		t.Fatalf("stored description = %q, want the operator default", got)
	}

	// A verifier that IS allowed to override stores its own text.
	f.enableVerifier(f.bot.ID, 5550006, true)
	ok, err = f.router.onBotsSetCustomVerification(ownerCtx,
		setCustomVerificationRequest(inputPeerUser(f.target), inputUser(f.bot), true, "Official reseller"))
	if err != nil || !ok {
		t.Fatalf("custom description = %v,%v, want true,nil", ok, err)
	}
	if got := f.verify.marks[domain.Peer{Type: domain.PeerTypeUser, ID: f.target.ID}].Description; got != "Official reseller" {
		t.Fatalf("stored description = %q, want the verifier text", got)
	}
}

// TestBotsSetCustomVerificationRejectsRevokeWithDescription pins the edge-side shape
// validation: a revoke that still carries a description is a caller bug (usually a
// forgotten enabled flag) and is reported rather than silently reinterpreted.
func TestBotsSetCustomVerificationRejectsRevokeWithDescription(t *testing.T) {
	f := newBotVerificationFixture(t, newFakeBotVerifications())
	f.enableVerifier(f.bot.ID, 5550007, true)
	ownerCtx := WithUserID(context.Background(), f.owner.ID)

	ok, err := f.router.onBotsSetCustomVerification(ownerCtx,
		setCustomVerificationRequest(inputPeerUser(f.target), inputUser(f.bot), false, "why"))
	if ok || !tgerr.Is(err, "BOT_VERIFIER_DESCRIPTION_INVALID") {
		t.Fatalf("revoke with description = %v,%v, want false,BOT_VERIFIER_DESCRIPTION_INVALID", ok, err)
	}
	if f.verify.setCalls != 0 {
		t.Fatalf("malformed request reached the service %d times", f.verify.setCalls)
	}
}

// TestBotsSetCustomVerificationReportsLimit maps the per-verifier bound onto
// LIMIT_INVALID: verifier status is granted per deployment, so an unbounded verifier
// would be an unbounded badge printer.
func TestBotsSetCustomVerificationReportsLimit(t *testing.T) {
	f := newBotVerificationFixture(t, newFakeBotVerifications())
	f.enableVerifier(f.bot.ID, 5550008, true)
	f.verify.limit = 1
	ownerCtx := WithUserID(context.Background(), f.owner.ID)

	if ok, err := f.router.onBotsSetCustomVerification(ownerCtx,
		setCustomVerificationRequest(inputPeerUser(f.target), inputUser(f.bot), true, "")); err != nil || !ok {
		t.Fatalf("first grant = %v,%v, want true,nil", ok, err)
	}
	ok, err := f.router.onBotsSetCustomVerification(ownerCtx,
		setCustomVerificationRequest(inputPeerUser(f.stranger), inputUser(f.bot), true, ""))
	if ok || !tgerr.Is(err, "LIMIT_INVALID") {
		t.Fatalf("over-limit grant = %v,%v, want false,LIMIT_INVALID", ok, err)
	}
}

// TestBotsSetCustomVerificationWithoutServiceStaysForbidden is the degradation half
// of the RPC: with no verification service wired no bot can be a verifier, so the
// answer must be exactly what it was before the feature existed.
func TestBotsSetCustomVerificationWithoutServiceStaysForbidden(t *testing.T) {
	f := newBotVerificationFixture(t, nil)
	if f.router.deps.BotVerifications != nil {
		t.Fatal("fixture wired a verification service, want nil")
	}
	ownerCtx := WithUserID(context.Background(), f.owner.ID)
	botCtx := WithUserID(context.Background(), f.bot.ID)

	if ok, err := f.router.onBotsSetCustomVerification(ownerCtx,
		setCustomVerificationRequest(inputPeerUser(f.target), inputUser(f.bot), true, "")); ok ||
		!tgerr.Is(err, "BOT_VERIFIER_FORBIDDEN") {
		t.Fatalf("owner call without service = %v,%v, want false,BOT_VERIFIER_FORBIDDEN", ok, err)
	}
	if ok, err := f.router.onBotsSetCustomVerification(botCtx,
		setCustomVerificationRequest(inputPeerUser(f.target), nil, true, "")); ok ||
		!tgerr.Is(err, "BOT_VERIFIER_FORBIDDEN") {
		t.Fatalf("bot call without service = %v,%v, want false,BOT_VERIFIER_FORBIDDEN", ok, err)
	}
	// The ownership and peer checks still run first, so their codes are unchanged too.
	if ok, err := f.router.onBotsSetCustomVerification(ownerCtx,
		setCustomVerificationRequest(inputPeerUser(f.target), inputUser(f.foreign), true, "")); ok ||
		!tgerr.Is(err, "BOT_INVALID") {
		t.Fatalf("foreign bot without service = %v,%v, want false,BOT_INVALID", ok, err)
	}
	if ok, err := f.router.onBotsSetCustomVerification(ownerCtx,
		setCustomVerificationRequest(&tg.InputPeerEmpty{}, inputUser(f.bot), true, "")); ok ||
		!tgerr.Is(err, "PEER_ID_INVALID") {
		t.Fatalf("empty peer without service = %v,%v, want false,PEER_ID_INVALID", ok, err)
	}
}
