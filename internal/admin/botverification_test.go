package admin

import (
	"context"
	"errors"
	"strings"
	"testing"

	botverificationapp "telesrv/internal/app/botverification"
	"telesrv/internal/domain"
)

// Compile-time proof that the shipped use-case service satisfies the admin port.
// cmd/telesrv wires *botverification.Service into Dependencies.BotVerification
// directly, so a drifting method set has to fail here rather than at integration
// time.
var _ BotVerificationService = (*botverificationapp.Service)(nil)

type fakeBotVerificationService struct {
	icons    []domain.VerificationIcon
	verifier domain.BotVerifierSettings
	hasRow   bool
	marks    []domain.CustomVerification
	request  domain.CustomVerificationRequest
	counts   map[domain.CustomVerificationRequestStatus]int64

	iconsErr    error
	verifierErr error
	requestErr  error
	marksErr    error
	decideErr   error
	writeErr    error

	upsertIconCalls   int
	setIconCalls      int
	grantCalls        int
	setEnabledCalls   int
	revokeVerifCalls  int
	revokeMarkCalls   int
	approveCalls      int
	rejectCalls       int
	revokeReqCalls    int
	granted           domain.BotVerifierSettings
	upsertedIcon      domain.VerificationIcon
	setIconActive     bool
	setEnabledValue   bool
	revokedMarkPeer   domain.Peer
	revokedMarkBotID  int64
	decidedReason     string
	decidedNote       string
	decidedBy         string
	decidedVersion    int64
	decidedRequestID  int64
	markFilterCapture domain.CustomVerificationFilter
}

func (f *fakeBotVerificationService) Icons(context.Context, bool, int) ([]domain.VerificationIcon, error) {
	if f.iconsErr != nil {
		return nil, f.iconsErr
	}
	return f.icons, nil
}

func (f *fakeBotVerificationService) UpsertIcon(_ context.Context, icon domain.VerificationIcon) (domain.VerificationIcon, error) {
	f.upsertIconCalls++
	f.upsertedIcon = icon
	if f.writeErr != nil {
		return domain.VerificationIcon{}, f.writeErr
	}
	icon.ID = 501
	return icon, nil
}

func (f *fakeBotVerificationService) SetIconActive(_ context.Context, iconID int64, active bool) (domain.VerificationIcon, error) {
	f.setIconCalls++
	f.setIconActive = active
	if f.writeErr != nil {
		return domain.VerificationIcon{}, f.writeErr
	}
	return domain.VerificationIcon{ID: iconID, DocumentID: 900, Name: "blue check", Active: active}, nil
}

func (f *fakeBotVerificationService) Verifiers(context.Context, bool, int) ([]domain.BotVerifierSettings, error) {
	if !f.hasRow {
		return nil, nil
	}
	return []domain.BotVerifierSettings{f.verifier}, nil
}

func (f *fakeBotVerificationService) VerifierSettings(_ context.Context, botID int64) (domain.BotVerifierSettings, error) {
	if f.verifierErr != nil {
		return domain.BotVerifierSettings{}, f.verifierErr
	}
	if !f.hasRow || f.verifier.BotID != botID {
		return domain.BotVerifierSettings{}, domain.ErrVerifierNotFound
	}
	return f.verifier, nil
}

func (f *fakeBotVerificationService) GrantVerifier(_ context.Context, settings domain.BotVerifierSettings) (domain.BotVerifierSettings, error) {
	f.grantCalls++
	f.granted = settings
	if f.writeErr != nil {
		return domain.BotVerifierSettings{}, f.writeErr
	}
	settings.Version++
	if settings.Version == 0 {
		settings.Version = 1
	}
	f.verifier = settings
	f.hasRow = true
	return settings, nil
}

func (f *fakeBotVerificationService) SetVerifierEnabled(_ context.Context, botID int64, enabled bool) (domain.BotVerifierSettings, error) {
	f.setEnabledCalls++
	f.setEnabledValue = enabled
	if f.writeErr != nil {
		return domain.BotVerifierSettings{}, f.writeErr
	}
	f.verifier.BotID = botID
	f.verifier.Enabled = enabled
	f.verifier.Version++
	return f.verifier, nil
}

func (f *fakeBotVerificationService) RevokeVerifier(context.Context, int64) (bool, error) {
	f.revokeVerifCalls++
	if f.writeErr != nil {
		return false, f.writeErr
	}
	removed := f.hasRow
	f.hasRow = false
	return removed, nil
}

func (f *fakeBotVerificationService) Marks(_ context.Context, filter domain.CustomVerificationFilter) ([]domain.CustomVerification, error) {
	f.markFilterCapture = filter
	if f.marksErr != nil {
		return nil, f.marksErr
	}
	out := make([]domain.CustomVerification, 0, len(f.marks))
	for _, mark := range f.marks {
		if filter.VerifierBotID != 0 && mark.VerifierBotID != filter.VerifierBotID {
			continue
		}
		if filter.PeerID != 0 && mark.Peer.ID != filter.PeerID {
			continue
		}
		out = append(out, mark)
	}
	return out, nil
}

func (f *fakeBotVerificationService) RevokeMark(_ context.Context, verifierBotID int64, peer domain.Peer) (bool, error) {
	f.revokeMarkCalls++
	f.revokedMarkBotID = verifierBotID
	f.revokedMarkPeer = peer
	if f.writeErr != nil {
		return false, f.writeErr
	}
	return len(f.marks) > 0, nil
}

func (f *fakeBotVerificationService) Requests(context.Context, domain.CustomVerificationRequestFilter) ([]domain.CustomVerificationRequest, error) {
	return []domain.CustomVerificationRequest{f.request}, nil
}

func (f *fakeBotVerificationService) Request(_ context.Context, requestID int64) (domain.CustomVerificationRequest, error) {
	if f.requestErr != nil {
		return domain.CustomVerificationRequest{}, f.requestErr
	}
	if f.request.ID != requestID {
		return domain.CustomVerificationRequest{}, domain.ErrCustomVerificationRequestNotFound
	}
	return f.request, nil
}

func (f *fakeBotVerificationService) RequestCounts(context.Context) (map[domain.CustomVerificationRequestStatus]int64, error) {
	return f.counts, nil
}

func (f *fakeBotVerificationService) decide(status domain.CustomVerificationRequestStatus, requestID, version int64, by, reason, note string) (domain.CustomVerificationRequest, bool, error) {
	f.decidedRequestID = requestID
	f.decidedVersion = version
	f.decidedBy = by
	f.decidedReason = reason
	f.decidedNote = note
	if f.decideErr != nil {
		return domain.CustomVerificationRequest{}, false, f.decideErr
	}
	f.request.Status = status
	f.request.DecidedBy = by
	f.request.DecisionReason = reason
	f.request.InternalNote = note
	f.request.Version++
	return f.request, true, nil
}

func (f *fakeBotVerificationService) Approve(_ context.Context, requestID, version int64, by, reason, note string) (domain.CustomVerificationRequest, bool, error) {
	f.approveCalls++
	return f.decide(domain.CustomVerificationApproved, requestID, version, by, reason, note)
}

func (f *fakeBotVerificationService) Reject(_ context.Context, requestID, version int64, by, reason, note string) (domain.CustomVerificationRequest, bool, error) {
	f.rejectCalls++
	return f.decide(domain.CustomVerificationRejected, requestID, version, by, reason, note)
}

func (f *fakeBotVerificationService) RevokeRequest(_ context.Context, requestID, version int64, by, reason, note string) (domain.CustomVerificationRequest, bool, error) {
	f.revokeReqCalls++
	return f.decide(domain.CustomVerificationRevoked, requestID, version, by, reason, note)
}

func pendingCustomVerificationRequest() domain.CustomVerificationRequest {
	return domain.CustomVerificationRequest{
		ID:                   88,
		VerifierBotID:        3003,
		ApplicantUserID:      1001,
		Peer:                 domain.Peer{Type: domain.PeerTypeChannel, ID: 5005},
		PeerTitle:            "Example News",
		PeerUsername:         "examplenews",
		Reason:               "we are the outlet",
		RequestedDescription: "verified partner",
		Status:               domain.CustomVerificationPending,
		Version:              3,
		CreatedAt:            fixedNow(),
	}
}

func enabledVerifierSettings() domain.BotVerifierSettings {
	return domain.BotVerifierSettings{
		BotID:                      3003,
		IconDocumentID:             900,
		CompanyName:                "Example Trust",
		DefaultDescription:         "verified by Example Trust",
		CanModifyCustomDescription: true,
		Enabled:                    true,
		Version:                    4,
		CreatedAt:                  fixedNow(),
		UpdatedAt:                  fixedNow(),
	}
}

func newBotVerificationFixture() (*Service, *fakeBotVerificationService, *memoryCommandRepo) {
	repo := newMemoryCommandRepo()
	fake := &fakeBotVerificationService{
		verifier: enabledVerifierSettings(),
		hasRow:   true,
		request:  pendingCustomVerificationRequest(),
		icons: []domain.VerificationIcon{
			{ID: 501, DocumentID: 900, Name: "blue check", Active: true},
			{ID: 502, DocumentID: 901, Name: "retired", Active: false},
			{ID: 503, DocumentID: 902, Name: "reserved", Active: true, OwnerBotID: 4004},
		},
		counts: map[domain.CustomVerificationRequestStatus]int64{domain.CustomVerificationPending: 3},
	}
	svc := NewService(Dependencies{Commands: repo, BotVerification: fake, Now: fixedNow})
	return svc, fake, repo
}

func meta(commandID string, dryRun bool) CommandMeta {
	return CommandMeta{CommandID: commandID, Actor: "alice", Reason: "operator decision", DryRun: dryRun}
}

// ---------------------------------------------------------------------------
// Verifier status
// ---------------------------------------------------------------------------

func TestGrantBotVerifierDryRunExecuteAndIdempotentReplay(t *testing.T) {
	ctx := context.Background()
	svc, fake, repo := newBotVerificationFixture()

	dry, err := svc.GrantBotVerifier(ctx, GrantBotVerifierRequest{
		CommandMeta:    meta("dry-grant", true),
		BotID:          3003,
		IconDocumentID: 900,
		CompanyName:    "Example Trust",
		Version:        4,
	})
	if err != nil {
		t.Fatalf("dry-run grant: %v", err)
	}
	if !dry.DryRun || dry.Status != string(domain.AdminCommandCompleted) || fake.grantCalls != 0 {
		t.Fatalf("dry-run result=%+v grantCalls=%d, want a completed dry run without mutation", dry, fake.grantCalls)
	}
	if dry.Details["bot_id"] != "3003" || dry.Details["previous_version"] != "4" ||
		dry.Details["created"] != false || dry.Details["icon_active"] != true ||
		dry.Details["correlation_id"] != "dry-grant" {
		t.Fatalf("dry-run details=%+v, want the audit facts seeded before execution", dry.Details)
	}

	execReq := GrantBotVerifierRequest{
		CommandMeta:                meta("exec-grant", false),
		BotID:                      3003,
		IconDocumentID:             900,
		CompanyName:                "Example Trust",
		DefaultDescription:         "verified by Example Trust",
		CanModifyCustomDescription: true,
		Version:                    4,
	}
	exec, err := svc.GrantBotVerifier(ctx, execReq)
	if err != nil {
		t.Fatalf("execute grant: %v", err)
	}
	if fake.grantCalls != 1 || exec.Details["version"] != "5" {
		t.Fatalf("execute result=%+v grantCalls=%d", exec, fake.grantCalls)
	}
	// The operator identity and the reason are journalled onto the verifier row, so
	// the grant is attributable without joining the command log.
	if fake.granted.GrantedBy != "alice" || fake.granted.GrantReason != "operator decision" {
		t.Fatalf("granted settings=%+v, want the actor and reason recorded", fake.granted)
	}
	if _, ok := repo.items["exec-grant"]; !ok {
		t.Fatal("grant was not journalled")
	}

	replay, err := svc.GrantBotVerifier(ctx, execReq)
	if err != nil {
		t.Fatalf("replay grant: %v", err)
	}
	if !replay.AlreadyExecuted || fake.grantCalls != 1 {
		t.Fatalf("replay result=%+v grantCalls=%d, want one execution", replay, fake.grantCalls)
	}
}

// Reconfiguring a switched-off verifier must not switch it back on: the kill
// switch is its own action, and a grant form that silently re-enabled would undo a
// deliberate operator decision.
func TestGrantBotVerifierPreservesTheKillSwitch(t *testing.T) {
	ctx := context.Background()
	svc, fake, _ := newBotVerificationFixture()
	fake.verifier.Enabled = false

	result, err := svc.GrantBotVerifier(ctx, GrantBotVerifierRequest{
		CommandMeta:    meta("exec-grant", false),
		BotID:          3003,
		IconDocumentID: 900,
		CompanyName:    "Example Trust",
		Version:        4,
	})
	if err != nil {
		t.Fatalf("grant: %v", err)
	}
	if fake.granted.Enabled {
		t.Fatalf("granted settings=%+v, want the disabled state preserved", fake.granted)
	}
	if result.Details["previous_enabled"] != false || result.Details["enabled"] != false {
		t.Fatalf("details=%+v", result.Details)
	}
}

func TestGrantBotVerifierVersionRules(t *testing.T) {
	ctx := context.Background()

	// An existing row demands the version the operator read.
	svc, fake, _ := newBotVerificationFixture()
	_, err := svc.GrantBotVerifier(ctx, GrantBotVerifierRequest{
		CommandMeta:    meta("stale", false),
		BotID:          3003,
		IconDocumentID: 900,
		CompanyName:    "Example Trust",
		Version:        3,
	})
	if err == nil || !strings.Contains(err.Error(), CodeCustomVerificationConflict) {
		t.Fatalf("stale version err=%v, want %s", err, CodeCustomVerificationConflict)
	}
	if fake.grantCalls != 0 {
		t.Fatalf("grantCalls=%d, want no write on a lost race", fake.grantCalls)
	}

	// version=0 on an existing row is "I did not read this row": also a conflict.
	svc, _, _ = newBotVerificationFixture()
	if _, err := svc.GrantBotVerifier(ctx, GrantBotVerifierRequest{
		CommandMeta: meta("fresh-on-existing", false), BotID: 3003,
		IconDocumentID: 900, CompanyName: "Example Trust", Version: 0,
	}); err == nil || !strings.Contains(err.Error(), CodeCustomVerificationConflict) {
		t.Fatalf("version=0 on an existing row err=%v, want a conflict", err)
	}

	// A positive version against no row is an edit of something that is gone.
	svc, fake, _ = newBotVerificationFixture()
	fake.hasRow = false
	if _, err := svc.GrantBotVerifier(ctx, GrantBotVerifierRequest{
		CommandMeta: meta("edit-missing", false), BotID: 3003,
		IconDocumentID: 900, CompanyName: "Example Trust", Version: 4,
	}); err == nil || !strings.Contains(err.Error(), CodeBotVerifierNotFound) {
		t.Fatalf("edit of a missing row err=%v, want %s", err, CodeBotVerifierNotFound)
	}

	// A fresh grant with version=0 goes through and is marked as a creation.
	svc, fake, _ = newBotVerificationFixture()
	fake.hasRow = false
	result, err := svc.GrantBotVerifier(ctx, GrantBotVerifierRequest{
		CommandMeta: meta("fresh", false), BotID: 3003,
		IconDocumentID: 900, CompanyName: "Example Trust", Version: 0,
	})
	if err != nil {
		t.Fatalf("fresh grant: %v", err)
	}
	if result.Details["created"] != true || fake.grantCalls != 1 || !fake.granted.Enabled {
		t.Fatalf("fresh grant details=%+v granted=%+v", result.Details, fake.granted)
	}
}

// The dry run refuses an icon the catalogue definitely rejects, so the operator is
// told before anything is written.
func TestGrantBotVerifierRefusesAnUnusableIcon(t *testing.T) {
	ctx := context.Background()

	svc, fake, _ := newBotVerificationFixture()
	fake.hasRow = false
	if _, err := svc.GrantBotVerifier(ctx, GrantBotVerifierRequest{
		CommandMeta: meta("retired-icon", true), BotID: 3003,
		IconDocumentID: 901, CompanyName: "Example Trust",
	}); err == nil || !strings.Contains(err.Error(), CodeVerificationIconInactive) {
		t.Fatalf("retired icon err=%v, want %s", err, CodeVerificationIconInactive)
	}

	// A reserved entry belongs to one verifier; for anybody else it does not exist.
	svc, fake, _ = newBotVerificationFixture()
	fake.hasRow = false
	if _, err := svc.GrantBotVerifier(ctx, GrantBotVerifierRequest{
		CommandMeta: meta("reserved-icon", true), BotID: 3003,
		IconDocumentID: 902, CompanyName: "Example Trust",
	}); err == nil || !strings.Contains(err.Error(), CodeVerificationIconNotFound) {
		t.Fatalf("reserved icon err=%v, want %s", err, CodeVerificationIconNotFound)
	}

	// An icon the catalogue page does not cover is left to the use-case layer: a
	// dry run must never refuse more than the command it rehearses.
	svc, fake, _ = newBotVerificationFixture()
	fake.hasRow = false
	result, err := svc.GrantBotVerifier(ctx, GrantBotVerifierRequest{
		CommandMeta: meta("unknown-icon", true), BotID: 3003,
		IconDocumentID: 999, CompanyName: "Example Trust",
	})
	if err != nil {
		t.Fatalf("uncovered icon: %v", err)
	}
	if result.Details["icon_catalogue_checked"] != false {
		t.Fatalf("details=%+v, want the unchecked icon reported rather than refused", result.Details)
	}
}

func TestGrantBotVerifierValidatesShapeBeforeTheJournal(t *testing.T) {
	ctx := context.Background()
	svc, _, repo := newBotVerificationFixture()
	cases := []struct {
		name string
		req  GrantBotVerifierRequest
		code string
	}{
		{"no bot", GrantBotVerifierRequest{CommandMeta: meta("c1", false), IconDocumentID: 900, CompanyName: "x"}, CodeBotVerifierNotFound},
		{"no icon", GrantBotVerifierRequest{CommandMeta: meta("c2", false), BotID: 3003, CompanyName: "x"}, CodeBotVerifierInvalid},
		{"no company", GrantBotVerifierRequest{CommandMeta: meta("c3", false), BotID: 3003, IconDocumentID: 900}, CodeBotVerifierInvalid},
		{"negative version", GrantBotVerifierRequest{CommandMeta: meta("c4", false), BotID: 3003, IconDocumentID: 900, CompanyName: "x", Version: -1}, CodeBotVerifierInvalid},
		{"company too long", GrantBotVerifierRequest{CommandMeta: meta("c5", false), BotID: 3003, IconDocumentID: 900,
			CompanyName: strings.Repeat("x", domain.MaxVerifierCompanyLength+1)}, CodeBotVerifierInvalid},
	}
	for _, item := range cases {
		if _, err := svc.GrantBotVerifier(ctx, item.req); err == nil || !strings.Contains(err.Error(), item.code) {
			t.Fatalf("%s err=%v, want %s", item.name, err, item.code)
		}
	}
	// Nothing reached the journal: a malformed command is not an audit subject.
	if len(repo.items) != 0 {
		t.Fatalf("journal=%+v, want a refusal before BeginCommand", repo.items)
	}
}

func TestSetBotVerifierEnabledNoOpAndMissingRow(t *testing.T) {
	ctx := context.Background()

	// A no-op flip is reported as unchanged rather than burning a version.
	svc, fake, _ := newBotVerificationFixture()
	result, err := svc.SetBotVerifierEnabled(ctx, SetBotVerifierEnabledRequest{
		CommandMeta: meta("noop", false), BotID: 3003, Enabled: true,
	})
	if err != nil {
		t.Fatalf("no-op flip: %v", err)
	}
	if result.Details["changed"] != false || !strings.Contains(result.Message, "already") {
		t.Fatalf("no-op result=%+v", result)
	}

	// A real flip writes once and reports the previous state.
	svc, fake, _ = newBotVerificationFixture()
	result, err = svc.SetBotVerifierEnabled(ctx, SetBotVerifierEnabledRequest{
		CommandMeta: meta("disable", false), BotID: 3003, Enabled: false,
	})
	if err != nil {
		t.Fatalf("flip: %v", err)
	}
	if fake.setEnabledCalls != 1 || fake.setEnabledValue {
		t.Fatalf("setEnabledCalls=%d value=%v", fake.setEnabledCalls, fake.setEnabledValue)
	}
	if result.Details["previous_enabled"] != true || result.Details["changed"] != true {
		t.Fatalf("details=%+v", result.Details)
	}

	// The dry run does not mutate.
	svc, fake, _ = newBotVerificationFixture()
	if _, err := svc.SetBotVerifierEnabled(ctx, SetBotVerifierEnabledRequest{
		CommandMeta: meta("dry", true), BotID: 3003, Enabled: false,
	}); err != nil {
		t.Fatalf("dry flip: %v", err)
	}
	if fake.setEnabledCalls != 0 {
		t.Fatalf("setEnabledCalls=%d, want no mutation on a dry run", fake.setEnabledCalls)
	}

	// A bot that is not a verifier has no switch to flip.
	svc, fake, _ = newBotVerificationFixture()
	fake.hasRow = false
	if _, err := svc.SetBotVerifierEnabled(ctx, SetBotVerifierEnabledRequest{
		CommandMeta: meta("missing", false), BotID: 3003, Enabled: false,
	}); err == nil || !strings.Contains(err.Error(), CodeBotVerifierNotFound) {
		t.Fatalf("missing verifier err=%v, want %s", err, CodeBotVerifierNotFound)
	}
}

func TestRevokeBotVerifierIsIdempotentAndStatesTheCascade(t *testing.T) {
	ctx := context.Background()
	svc, fake, _ := newBotVerificationFixture()
	fake.marks = []domain.CustomVerification{
		{ID: 1, VerifierBotID: 3003, Peer: domain.Peer{Type: domain.PeerTypeChannel, ID: 5005}},
	}

	dry, err := svc.RevokeBotVerifier(ctx, RevokeBotVerifierRequest{CommandMeta: meta("dry", true), BotID: 3003})
	if err != nil {
		t.Fatalf("dry revoke: %v", err)
	}
	// The marks that would cascade away are stated before the operator confirms.
	if dry.Details["present"] != true || dry.Details["mark_count"] != "1" || fake.revokeVerifCalls != 0 {
		t.Fatalf("dry details=%+v revokeCalls=%d", dry.Details, fake.revokeVerifCalls)
	}

	exec, err := svc.RevokeBotVerifier(ctx, RevokeBotVerifierRequest{CommandMeta: meta("exec", false), BotID: 3003})
	if err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if exec.Details["changed"] != true || fake.revokeVerifCalls != 1 {
		t.Fatalf("details=%+v revokeCalls=%d", exec.Details, fake.revokeVerifCalls)
	}

	// A bot that is not a verifier answers changed=false rather than an error, so a
	// panel retry after a lost response is harmless.
	svc, fake, _ = newBotVerificationFixture()
	fake.hasRow = false
	again, err := svc.RevokeBotVerifier(ctx, RevokeBotVerifierRequest{CommandMeta: meta("gone", false), BotID: 3003})
	if err != nil {
		t.Fatalf("revoke of a non-verifier: %v", err)
	}
	if again.Details["changed"] != false || !strings.Contains(again.Message, "not a verifier") {
		t.Fatalf("result=%+v", again)
	}
}

// ---------------------------------------------------------------------------
// Icon catalogue
// ---------------------------------------------------------------------------

func TestUpsertVerificationIconValidationAndDryRun(t *testing.T) {
	ctx := context.Background()
	svc, fake, repo := newBotVerificationFixture()

	for _, item := range []struct {
		name string
		req  UpsertVerificationIconRequest
	}{
		{"no document", UpsertVerificationIconRequest{CommandMeta: meta("c1", false), Name: "x"}},
		{"no name", UpsertVerificationIconRequest{CommandMeta: meta("c2", false), DocumentID: 900}},
		{"blank name", UpsertVerificationIconRequest{CommandMeta: meta("c3", false), DocumentID: 900, Name: "   "}},
		{"negative owner", UpsertVerificationIconRequest{CommandMeta: meta("c4", false), DocumentID: 900, Name: "x", OwnerBotID: -1}},
		{"name too long", UpsertVerificationIconRequest{CommandMeta: meta("c5", false), DocumentID: 900,
			Name: strings.Repeat("x", domain.MaxVerificationIconNameLength+1)}},
	} {
		if _, err := svc.UpsertVerificationIcon(ctx, item.req); err == nil ||
			!strings.Contains(err.Error(), CodeVerificationIconInvalid) {
			t.Fatalf("%s err=%v, want %s", item.name, err, CodeVerificationIconInvalid)
		}
	}
	if len(repo.items) != 0 {
		t.Fatalf("journal=%+v, want a refusal before BeginCommand", repo.items)
	}

	if _, err := svc.UpsertVerificationIcon(ctx, UpsertVerificationIconRequest{
		CommandMeta: meta("dry", true), DocumentID: 900, Name: "  blue check  ",
	}); err != nil {
		t.Fatalf("dry upsert: %v", err)
	}
	if fake.upsertIconCalls != 0 {
		t.Fatalf("upsertIconCalls=%d, want no mutation on a dry run", fake.upsertIconCalls)
	}

	result, err := svc.UpsertVerificationIcon(ctx, UpsertVerificationIconRequest{
		CommandMeta: meta("exec", false), DocumentID: 900, Name: "  blue check  ", OwnerBotID: 3003,
	})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	// A new catalogue entry is active: retiring one is its own action.
	if fake.upsertedIcon.Name != "blue check" || !fake.upsertedIcon.Active || fake.upsertedIcon.OwnerBotID != 3003 {
		t.Fatalf("upserted icon=%+v, want a trimmed active entry", fake.upsertedIcon)
	}
	if result.Details["icon_id"] != "501" {
		t.Fatalf("details=%+v", result.Details)
	}
}

func TestSetVerificationIconActiveRequiresAnIconAndHonoursDryRun(t *testing.T) {
	ctx := context.Background()
	svc, fake, _ := newBotVerificationFixture()

	if _, err := svc.SetVerificationIconActive(ctx, SetVerificationIconActiveRequest{
		CommandMeta: meta("c1", false), IconID: 0, Active: false,
	}); err == nil || !strings.Contains(err.Error(), CodeVerificationIconNotFound) {
		t.Fatalf("missing icon err=%v, want %s", err, CodeVerificationIconNotFound)
	}

	if _, err := svc.SetVerificationIconActive(ctx, SetVerificationIconActiveRequest{
		CommandMeta: meta("dry", true), IconID: 501, Active: false,
	}); err != nil {
		t.Fatalf("dry switch: %v", err)
	}
	if fake.setIconCalls != 0 {
		t.Fatalf("setIconCalls=%d, want no mutation on a dry run", fake.setIconCalls)
	}

	result, err := svc.SetVerificationIconActive(ctx, SetVerificationIconActiveRequest{
		CommandMeta: meta("exec", false), IconID: 501, Active: false,
	})
	if err != nil {
		t.Fatalf("switch: %v", err)
	}
	if fake.setIconCalls != 1 || fake.setIconActive || result.Details["active"] != false {
		t.Fatalf("setIconCalls=%d active=%v details=%+v", fake.setIconCalls, fake.setIconActive, result.Details)
	}
	if !strings.Contains(result.Message, "retired") {
		t.Fatalf("message=%q", result.Message)
	}
}

// ---------------------------------------------------------------------------
// Granted marks
// ---------------------------------------------------------------------------

func TestRevokeCustomVerificationShapeDryRunAndIdempotence(t *testing.T) {
	ctx := context.Background()
	svc, fake, repo := newBotVerificationFixture()

	for _, item := range []struct {
		name string
		req  RevokeCustomVerificationRequest
		code string
	}{
		{"no verifier", RevokeCustomVerificationRequest{CommandMeta: meta("c1", false),
			PeerType: domain.PeerTypeChannel, PeerID: 5005}, CodeBotVerifierNotFound},
		{"no peer id", RevokeCustomVerificationRequest{CommandMeta: meta("c2", false),
			VerifierBotID: 3003, PeerType: domain.PeerTypeChannel}, CodeCustomVerificationTargetInvalid},
		{"unmodelled peer", RevokeCustomVerificationRequest{CommandMeta: meta("c3", false),
			VerifierBotID: 3003, PeerType: domain.PeerType("chat"), PeerID: 5005}, CodeCustomVerificationTargetInvalid},
	} {
		if _, err := svc.RevokeCustomVerification(ctx, item.req); err == nil || !strings.Contains(err.Error(), item.code) {
			t.Fatalf("%s err=%v, want %s", item.name, err, item.code)
		}
	}
	if len(repo.items) != 0 {
		t.Fatalf("journal=%+v, want a refusal before BeginCommand", repo.items)
	}

	fake.marks = []domain.CustomVerification{
		{ID: 1, VerifierBotID: 3003, Peer: domain.Peer{Type: domain.PeerTypeChannel, ID: 5005}},
	}
	dry, err := svc.RevokeCustomVerification(ctx, RevokeCustomVerificationRequest{
		CommandMeta: meta("dry", true), VerifierBotID: 3003, PeerType: domain.PeerTypeChannel, PeerID: 5005,
	})
	if err != nil {
		t.Fatalf("dry revoke: %v", err)
	}
	if dry.Details["mark_present"] != true || fake.revokeMarkCalls != 0 {
		t.Fatalf("dry details=%+v revokeMarkCalls=%d", dry.Details, fake.revokeMarkCalls)
	}

	exec, err := svc.RevokeCustomVerification(ctx, RevokeCustomVerificationRequest{
		CommandMeta: meta("exec", false), VerifierBotID: 3003, PeerType: domain.PeerTypeChannel, PeerID: 5005,
	})
	if err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if exec.Details["changed"] != true || fake.revokedMarkBotID != 3003 ||
		fake.revokedMarkPeer != (domain.Peer{Type: domain.PeerTypeChannel, ID: 5005}) {
		t.Fatalf("details=%+v revokedBot=%d peer=%+v", exec.Details, fake.revokedMarkBotID, fake.revokedMarkPeer)
	}

	// A peer that carries no mark from this verifier is a no-op, not an error: the
	// operator must be able to retry a revoke whose response was lost.
	svc, fake, _ = newBotVerificationFixture()
	absent, err := svc.RevokeCustomVerification(ctx, RevokeCustomVerificationRequest{
		CommandMeta: meta("absent", false), VerifierBotID: 3003, PeerType: domain.PeerTypeUser, PeerID: 7007,
	})
	if err != nil {
		t.Fatalf("revoke of an absent mark: %v", err)
	}
	if absent.Details["changed"] != false || absent.Details["mark_present"] != false {
		t.Fatalf("details=%+v", absent.Details)
	}
}

// ---------------------------------------------------------------------------
// Queue decisions
// ---------------------------------------------------------------------------

func TestApproveBotVerificationDryRunExecuteAndReplay(t *testing.T) {
	ctx := context.Background()
	svc, fake, repo := newBotVerificationFixture()

	dry, err := svc.ApproveBotVerification(ctx, ApproveBotVerificationRequest{
		CommandMeta: meta("dry-approve", true), RequestID: 88, Version: 3, InternalNote: "checked the outlet",
	})
	if err != nil {
		t.Fatalf("dry approve: %v", err)
	}
	if !dry.DryRun || fake.approveCalls != 0 {
		t.Fatalf("dry result=%+v approveCalls=%d", dry, fake.approveCalls)
	}
	if dry.Details["previous_status"] != string(domain.CustomVerificationPending) ||
		dry.Details["next_status"] != string(domain.CustomVerificationApproved) ||
		dry.Details["request_id"] != "88" || dry.Details["verifier_bot_id"] != "3003" ||
		dry.Details["peer_type"] != "channel" || dry.Details["peer_id"] != "5005" ||
		dry.Details["applicant_user_id"] != "1001" ||
		dry.Details["internal_note"] != "checked the outlet" ||
		dry.Details["verifier_enabled"] != true {
		t.Fatalf("dry details=%+v, want the audit facts seeded before execution", dry.Details)
	}

	execReq := ApproveBotVerificationRequest{
		CommandMeta: meta("exec-approve", false), RequestID: 88, Version: 3, InternalNote: "checked the outlet",
	}
	exec, err := svc.ApproveBotVerification(ctx, execReq)
	if err != nil {
		t.Fatalf("approve: %v", err)
	}
	if fake.approveCalls != 1 || exec.Details["status"] != string(domain.CustomVerificationApproved) ||
		exec.Details["version"] != "4" || exec.Details["changed"] != true {
		t.Fatalf("exec result=%+v approveCalls=%d", exec, fake.approveCalls)
	}
	// The internal note travels in its own field and never in the reason, which is
	// what the applicant is shown.
	if fake.decidedNote != "checked the outlet" || fake.decidedReason != "operator decision" ||
		fake.decidedBy != "alice" || fake.decidedVersion != 3 || fake.decidedRequestID != 88 {
		t.Fatalf("forwarded decision by=%q reason=%q note=%q version=%d id=%d",
			fake.decidedBy, fake.decidedReason, fake.decidedNote, fake.decidedVersion, fake.decidedRequestID)
	}
	if _, ok := repo.items["exec-approve"]; !ok {
		t.Fatal("approval was not journalled")
	}

	replay, err := svc.ApproveBotVerification(ctx, execReq)
	if err != nil {
		t.Fatalf("replay approve: %v", err)
	}
	if !replay.AlreadyExecuted || fake.approveCalls != 1 {
		t.Fatalf("replay result=%+v approveCalls=%d, want one execution", replay, fake.approveCalls)
	}
}

// An already-approved application is a replay the use-case layer answers as a
// no-op, so the dry run must not re-run the verifier gate and must not demand the
// version again.
func TestApproveBotVerificationReplayOfADecidedApplicationSkipsTheVerifierGate(t *testing.T) {
	ctx := context.Background()
	svc, fake, _ := newBotVerificationFixture()
	fake.request.Status = domain.CustomVerificationApproved
	fake.request.Version = 9
	// The verifier was switched off after the approval; that must not make a replay
	// fail.
	fake.verifier.Enabled = false

	result, err := svc.ApproveBotVerification(ctx, ApproveBotVerificationRequest{
		CommandMeta: meta("dry", true), RequestID: 88, Version: 3,
	})
	if err != nil {
		t.Fatalf("dry replay: %v", err)
	}
	if result.Details["previous_status"] != string(domain.CustomVerificationApproved) {
		t.Fatalf("details=%+v", result.Details)
	}
}

// An application can sit in the queue for days: a verifier switched off in the
// meantime must not be able to grant through the review path what the RPC path
// would refuse.
func TestApproveBotVerificationRefusesADisabledOrMissingVerifier(t *testing.T) {
	ctx := context.Background()

	svc, fake, _ := newBotVerificationFixture()
	fake.verifier.Enabled = false
	if _, err := svc.ApproveBotVerification(ctx, ApproveBotVerificationRequest{
		CommandMeta: meta("disabled", true), RequestID: 88, Version: 3,
	}); err == nil || !strings.Contains(err.Error(), CodeBotVerifierForbidden) {
		t.Fatalf("disabled verifier err=%v, want %s", err, CodeBotVerifierForbidden)
	}

	// "Never was a verifier" answers the same code on purpose: that is the
	// distinction BOTVERIFIER_FORBIDDEN deliberately hides.
	svc, fake, _ = newBotVerificationFixture()
	fake.hasRow = false
	if _, err := svc.ApproveBotVerification(ctx, ApproveBotVerificationRequest{
		CommandMeta: meta("missing", true), RequestID: 88, Version: 3,
	}); err == nil || !strings.Contains(err.Error(), CodeBotVerifierForbidden) {
		t.Fatalf("missing verifier err=%v, want %s", err, CodeBotVerifierForbidden)
	}
}

func TestBotVerificationDecisionsDemandTheVersionAndAValidTransition(t *testing.T) {
	ctx := context.Background()

	// A stale version loses the race.
	svc, fake, _ := newBotVerificationFixture()
	if _, err := svc.ApproveBotVerification(ctx, ApproveBotVerificationRequest{
		CommandMeta: meta("stale", false), RequestID: 88, Version: 2,
	}); err == nil || !strings.Contains(err.Error(), CodeCustomVerificationConflict) {
		t.Fatalf("stale version err=%v, want %s", err, CodeCustomVerificationConflict)
	}
	if fake.approveCalls != 0 {
		t.Fatalf("approveCalls=%d, want no write on a lost race", fake.approveCalls)
	}

	// A missing version never read the row at all, so the optimistic lock could not
	// have protected anything.
	svc, _, repo := newBotVerificationFixture()
	if _, err := svc.ApproveBotVerification(ctx, ApproveBotVerificationRequest{
		CommandMeta: meta("noversion", false), RequestID: 88,
	}); err == nil || !strings.Contains(err.Error(), CodeCustomVerificationInvalid) {
		t.Fatalf("missing version err=%v, want %s", err, CodeCustomVerificationInvalid)
	}
	if len(repo.items) != 0 {
		t.Fatalf("journal=%+v, want a refusal before BeginCommand", repo.items)
	}

	// A rejected application cannot be revoked: revoked is reachable only from
	// approved, which keeps it meaning "was verified once".
	svc, fake, _ = newBotVerificationFixture()
	fake.request.Status = domain.CustomVerificationRejected
	if _, err := svc.RevokeBotVerification(ctx, RevokeBotVerificationRequest{
		CommandMeta: meta("badedge", false), RequestID: 88, Version: 3,
	}); err == nil || !strings.Contains(err.Error(), CodeCustomVerificationStatusInvalid) {
		t.Fatalf("illegal transition err=%v, want %s", err, CodeCustomVerificationStatusInvalid)
	}

	// A note nobody could store is refused before the journal.
	svc, _, repo = newBotVerificationFixture()
	if _, err := svc.ApproveBotVerification(ctx, ApproveBotVerificationRequest{
		CommandMeta: meta("longnote", false), RequestID: 88, Version: 3,
		InternalNote: strings.Repeat("x", domain.MaxCustomVerificationNoteLength+1),
	}); err == nil || !strings.Contains(err.Error(), CodeCustomVerificationInvalid) {
		t.Fatalf("long note err=%v, want %s", err, CodeCustomVerificationInvalid)
	}
	if len(repo.items) != 0 {
		t.Fatalf("journal=%+v", repo.items)
	}

	// A missing application is a 404-shaped failure.
	svc, fake, _ = newBotVerificationFixture()
	fake.request.ID = 99
	if _, err := svc.ApproveBotVerification(ctx, ApproveBotVerificationRequest{
		CommandMeta: meta("missing", false), RequestID: 88, Version: 3,
	}); err == nil || !strings.Contains(err.Error(), CodeCustomVerificationRequestNotFound) {
		t.Fatalf("missing application err=%v, want %s", err, CodeCustomVerificationRequestNotFound)
	}
}

// A decision nobody can explain must never reach the audit trail, so reject and
// revoke refuse an empty reason before the journal is touched.
func TestRejectAndRevokeBotVerificationRequireAReason(t *testing.T) {
	ctx := context.Background()
	svc, fake, repo := newBotVerificationFixture()

	for _, reason := range []string{"", "   "} {
		if _, err := svc.RejectBotVerification(ctx, RejectBotVerificationRequest{
			CommandMeta: CommandMeta{CommandID: "c1", Actor: "alice", Reason: reason}, RequestID: 88, Version: 3,
		}); err == nil || !strings.Contains(err.Error(), CodeCustomVerificationReasonRequired) {
			t.Fatalf("reject with reason %q err=%v, want %s", reason, err, CodeCustomVerificationReasonRequired)
		}
		if _, err := svc.RevokeBotVerification(ctx, RevokeBotVerificationRequest{
			CommandMeta: CommandMeta{CommandID: "c2", Actor: "alice", Reason: reason}, RequestID: 88, Version: 3,
		}); err == nil || !strings.Contains(err.Error(), CodeCustomVerificationReasonRequired) {
			t.Fatalf("revoke with reason %q err=%v, want %s", reason, err, CodeCustomVerificationReasonRequired)
		}
	}
	if fake.rejectCalls != 0 || fake.revokeReqCalls != 0 || len(repo.items) != 0 {
		t.Fatalf("rejectCalls=%d revokeCalls=%d journal=%+v, want nothing attempted",
			fake.rejectCalls, fake.revokeReqCalls, repo.items)
	}

	// With a reason it goes through, and the reason is what the applicant is told.
	result, err := svc.RejectBotVerification(ctx, RejectBotVerificationRequest{
		CommandMeta: meta("exec-reject", false), RequestID: 88, Version: 3, InternalNote: "self-published only",
	})
	if err != nil {
		t.Fatalf("reject: %v", err)
	}
	if fake.rejectCalls != 1 || fake.decidedReason != "operator decision" || fake.decidedNote != "self-published only" {
		t.Fatalf("rejectCalls=%d reason=%q note=%q", fake.rejectCalls, fake.decidedReason, fake.decidedNote)
	}
	if result.Details["status"] != string(domain.CustomVerificationRejected) {
		t.Fatalf("details=%+v", result.Details)
	}
}

// Revoking through the application reports whether the mark was still there: the
// operator may have stripped it directly, and the application still has to reach
// "revoked".
func TestRevokeBotVerificationReportsTheMarkStateAndDoesNotMutateOnDryRun(t *testing.T) {
	ctx := context.Background()
	svc, fake, _ := newBotVerificationFixture()
	fake.request.Status = domain.CustomVerificationApproved
	fake.marks = []domain.CustomVerification{
		{ID: 1, VerifierBotID: 3003, Peer: domain.Peer{Type: domain.PeerTypeChannel, ID: 5005}},
	}

	dry, err := svc.RevokeBotVerification(ctx, RevokeBotVerificationRequest{
		CommandMeta: meta("dry", true), RequestID: 88, Version: 3,
	})
	if err != nil {
		t.Fatalf("dry revoke: %v", err)
	}
	if dry.Details["mark_present"] != true || fake.revokeReqCalls != 0 {
		t.Fatalf("dry details=%+v revokeCalls=%d", dry.Details, fake.revokeReqCalls)
	}

	fake.marks = nil
	exec, err := svc.RevokeBotVerification(ctx, RevokeBotVerificationRequest{
		CommandMeta: meta("exec", false), RequestID: 88, Version: 3,
	})
	if err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if exec.Details["mark_present"] != false || fake.revokeReqCalls != 1 ||
		exec.Details["status"] != string(domain.CustomVerificationRevoked) {
		t.Fatalf("details=%+v revokeCalls=%d", exec.Details, fake.revokeReqCalls)
	}
}

// ---------------------------------------------------------------------------
// Reads, wiring and error mapping
// ---------------------------------------------------------------------------

func TestBotVerificationReadsRequireTheDependency(t *testing.T) {
	ctx := context.Background()
	svc := NewService(Dependencies{Commands: newMemoryCommandRepo(), Now: fixedNow})
	if _, err := svc.BotVerifiers(ctx, false, 0); !errors.Is(err, errBotVerificationNotConfigured) {
		t.Fatalf("verifiers err=%v", err)
	}
	if _, err := svc.VerificationIcons(ctx, false, 0); !errors.Is(err, errBotVerificationNotConfigured) {
		t.Fatalf("icons err=%v", err)
	}
	if _, err := svc.CustomVerificationRequestCounts(ctx); !errors.Is(err, errBotVerificationNotConfigured) {
		t.Fatalf("counts err=%v", err)
	}
	if _, err := svc.ApproveBotVerification(ctx, ApproveBotVerificationRequest{
		CommandMeta: meta("c1", false), RequestID: 1, Version: 1,
	}); !errors.Is(err, errBotVerificationNotConfigured) {
		t.Fatalf("approve err=%v", err)
	}
}

func TestCustomVerificationMarkActiveFiltersByTheExactPair(t *testing.T) {
	ctx := context.Background()
	svc, fake, _ := newBotVerificationFixture()
	fake.marks = []domain.CustomVerification{
		{ID: 1, VerifierBotID: 3003, Peer: domain.Peer{Type: domain.PeerTypeChannel, ID: 5005}},
	}
	active, err := svc.CustomVerificationMarkActive(ctx, 3003, domain.Peer{Type: domain.PeerTypeChannel, ID: 5005})
	if err != nil || !active {
		t.Fatalf("active=%v err=%v", active, err)
	}
	if fake.markFilterCapture.VerifierBotID != 3003 || fake.markFilterCapture.PeerID != 5005 ||
		fake.markFilterCapture.PeerType != domain.PeerTypeChannel {
		t.Fatalf("filter=%+v, want the exact pair queried", fake.markFilterCapture)
	}
	// A different verifier's mark on the same peer is not this verifier's mark.
	active, err = svc.CustomVerificationMarkActive(ctx, 4004, domain.Peer{Type: domain.PeerTypeChannel, ID: 5005})
	if err != nil || active {
		t.Fatalf("foreign verifier active=%v err=%v", active, err)
	}
	// A shapeless pair is answered false rather than queried.
	if active, err := svc.CustomVerificationMarkActive(ctx, 0, domain.Peer{}); err != nil || active {
		t.Fatalf("empty pair active=%v err=%v", active, err)
	}
}

func TestBotVerificationErrorCodeMapping(t *testing.T) {
	cases := map[error]string{
		domain.ErrVerifierNotFound:                  CodeBotVerifierNotFound,
		domain.ErrVerifierForbidden:                 CodeBotVerifierForbidden,
		domain.ErrVerifierSettingsInvalid:           CodeBotVerifierInvalid,
		domain.ErrVerifierDescriptionForbidden:      CodeBotVerifierDescriptionForbidden,
		domain.ErrVerificationIconNotFound:          CodeVerificationIconNotFound,
		domain.ErrVerificationIconInactive:          CodeVerificationIconInactive,
		domain.ErrVerificationIconInvalid:           CodeVerificationIconInvalid,
		domain.ErrCustomVerificationNotFound:        CodeCustomVerificationNotFound,
		domain.ErrCustomVerificationRequestNotFound: CodeCustomVerificationRequestNotFound,
		domain.ErrCustomVerificationRequestExists:   CodeCustomVerificationRequestExists,
		domain.ErrCustomVerificationVersionConflict: CodeCustomVerificationConflict,
		domain.ErrCustomVerificationLimit:           CodeCustomVerificationLimit,
		domain.ErrCustomVerificationTargetInvalid:   CodeCustomVerificationTargetInvalid,
		domain.ErrCustomVerificationRequestInvalid:  CodeCustomVerificationInvalid,
		domain.ErrVerificationReasonRequired:        CodeCustomVerificationReasonRequired,
		domain.ErrVerificationTargetSystem:          CodeCustomVerificationTargetSystem,
		domain.ErrBotNotFound:                       CodeBotVerifierBotNotFound,
		errors.New("something nobody has modelled"): "",
	}
	for err, want := range cases {
		if got := BotVerificationErrorCode(err); got != want {
			t.Fatalf("BotVerificationErrorCode(%v) = %q, want %q", err, got, want)
		}
	}
	if BotVerificationErrorCode(nil) != "" {
		t.Fatal("nil error carries a code")
	}
	// The third-party codes are distinct tokens from the official ones: the panel
	// renders the two mechanisms in separate sections.
	if CodeCustomVerificationConflict == CodeVerificationConflict ||
		CodeCustomVerificationReasonRequired == CodeVerificationReasonRequired {
		t.Fatal("third-party codes collide with the official verification ones")
	}
}

func TestBotVerificationErrorsFromTheServiceAreCoded(t *testing.T) {
	ctx := context.Background()
	svc, fake, _ := newBotVerificationFixture()
	fake.writeErr = domain.ErrCustomVerificationLimit
	fake.request.Status = domain.CustomVerificationPending
	fake.decideErr = domain.ErrCustomVerificationLimit

	_, err := svc.ApproveBotVerification(ctx, ApproveBotVerificationRequest{
		CommandMeta: meta("limit", false), RequestID: 88, Version: 3,
	})
	if err == nil || !strings.Contains(err.Error(), CodeCustomVerificationLimit) {
		t.Fatalf("err=%v, want the per-verifier bound code", err)
	}
}
