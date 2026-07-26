package bots

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"go.uber.org/zap"

	verificationapp "telesrv/internal/app/verification"
	"telesrv/internal/branding"
	"telesrv/internal/domain"
)

// Built-in @verifybot: the applicant front door for official platform
// verification.
//
// The dialog is button driven (inline keyboards) for every closed choice -- which
// peer to file, which category, confirm/skip/cancel -- and text driven for the
// free-form fields. It owns no verification rule whatsoever: ownership, public
// username, restrictions, already-verified, cooldown, rate limits and the status
// machine all live in app/verification, and this file only renders the answers
// that service gives. Every field is checked by the domain
// (ValidateVerificationURL, ValidateDraft, ValidateForSubmission) before it is
// offered to the service, so the applicant gets a specific reason instead of a
// generic refusal.
//
// Conversation state is domain.BotChatState (Command="verify"), read and written
// under the per-user serviceBotReplyLock, so the Get->modify->Upsert round trip
// is atomic and replies stay ordered. The application record itself is the
// durable truth: every completed step is pushed into it with SaveDraft, and the
// chat state is only a resumable cursor over it plus the button token table.

// Both ends of the contract are asserted here, so a signature change in
// app/verification breaks the build instead of silently disabling the bot or the
// decision notifications.
var (
	_ verificationApplications          = (*verificationapp.Service)(nil)
	_ verificationapp.ApplicantNotifier = (*Service)(nil)
)

const (
	// verifyBotCommand is the single BotChatState.Command of this dialog. There is
	// one flow, so the step is what distinguishes the phases.
	verifyBotCommand = "verify"

	verifyBotCmdNew    = "new"
	verifyBotCmdStatus = "status"

	verifyStepIntro       = "intro"
	verifyStepTarget      = "target"
	verifyStepCategory    = "category"
	verifyStepDescription = "description"
	verifyStepWebsite     = "website"
	verifyStepSocial      = "social"
	verifyStepPress       = "press"
	verifyStepNote        = "note"
	verifyStepConfirm     = "confirm"
	// verifyStepDone is a terminal state that is deliberately *kept*: it is what
	// makes a second press of the Submit button replay the same confirmation
	// instead of filing a second application or reporting an expired button.
	verifyStepDone = "done"

	verifyDraftAppID          = "app"
	verifyDraftVersion        = "ver"
	verifyDraftCategory       = "cat"
	verifyDraftDescription    = "desc"
	verifyDraftWebsite        = "site"
	verifyDraftSocial         = "social"
	verifyDraftPress          = "press"
	verifyDraftNote           = "note"
	verifyDraftTargetTitle    = "tgt_title"
	verifyDraftTargetUsername = "tgt_username"
	verifyDraftGeneration     = "gen"
	// verifyDraftOptionPrefix namespaces the button token table inside the draft
	// map, so a token can never collide with a payload field.
	verifyDraftOptionPrefix = "opt:"
	// verifyCallbackDataPrefix tags the bot's own callback data. It carries no
	// information beyond "this is a @verifybot button".
	verifyCallbackDataPrefix = "vb:"
	// verifyOptionTokenBytes is the entropy behind one button token.
	verifyOptionTokenBytes = 6
	// verifyOptionTokenMaxLen bounds the token part of callback data before the
	// table is even consulted.
	verifyOptionTokenMaxLen = 32

	// verifyTokenGenerations is how many keyboard renders' worth of button tokens
	// stay resolvable: the current render plus the two before it. Enough that
	// pressing the same button twice (or a button on the message just above) still
	// resolves, while the table stays a bounded handful of entries.
	verifyTokenGenerations = 3

	// verifyMaxTargetButtons bounds one target picker. An operator account can
	// administer far more peers than fit in an inline keyboard, and the token table
	// is per-user durable state, so the picker is truncated rather than unbounded.
	verifyMaxTargetButtons = 12
	// verifyStatusListLimit bounds the /status listing.
	verifyStatusListLimit = 10

	// Opaque button choices as stored in the per-user token table. These strings
	// never travel over the wire: only the random token that maps to them does.
	verifyChoiceApply          = "apply"
	verifyChoiceSkip           = "skip"
	verifyChoiceSubmit         = "submit"
	verifyChoiceAbort          = "abort"
	verifyChoiceTargetPrefix   = "tgt:"
	verifyChoiceCategoryPrefix = "cat:"
	verifyChoiceBlockedPrefix  = "no:"
)

const verifyBotStartText = `I collect applications for official ` + branding.ProductName + ` verification: the badge shown next to the name of a channel, supergroup or bot whose identity has been confirmed.

Before you apply, check that the subject of the application:
- is a channel, supergroup or bot with a public @username;
- is one you created or administer;
- stands for a real organisation, brand, media outlet, institution or public figure;
- has been written about by press you do not control.

This badge is never sold and never granted automatically. A person reads every application, and I message you here with the decision.

Tap the button below, or send /new, to start. Send /help for the full list of commands.`

const verifyBotHelpText = `I collect official ` + branding.ProductName + ` verification applications.

/new - file a verification application
/status - list your applications and their status
/cancel - withdraw the application you are working on
/help - show this message

One application asks for: the subject, a category, a description, the official website, optional social links, links to independent press coverage, and an optional comment for the reviewers. You can send /cancel at any point, and /status any time after filing.`

const verifyBotIdleText = `I only collect official verification applications. Send /new to file one, /status to check the ones you filed, or /help to see what I understand.`

const (
	verifyPromptTargetText   = `Which one do you want verified? Pick it below.`
	verifyPromptCategoryText = `What kind of project is it? Pick the closest category.`

	verifyPromptDescriptionTmpl = `Now describe the subject in one message: what it is, who is behind it and what it is publicly known for. Between %d and %d characters.`

	verifyPromptWebsiteText = `Send the official website of the subject, for example https://example.com

It has to be a plain public http(s) address. I never open the links you send: they are stored and shown to the reviewers as links, nothing more.`

	verifyPromptSocialTmpl = `Send links to the official social media accounts of the subject, one per line, up to %d of them. Tap Skip if there are none.`

	verifyPromptPressTmpl = `Send links to independent press coverage, one per line: at least %d of them, up to %d.

This is the part a reviewer actually checks, so it has to be coverage you do not control -- articles about the subject on news sites, not your own pages or social posts.`

	verifyPromptNoteText = `Anything else the reviewers should know? Send it in one message, or tap Skip.`

	verifyURLRejectText = `That is not a link I can accept. It has to be a plain http:// or https:// address of a public site, with no login credentials and no unusual port -- for example https://example.com/news/story.`

	verifyExpiredButtonText   = `That button is no longer active. Send /new to start an application, or /status to see the ones you filed.`
	verifyUnavailableText     = `Official verification is not available on this server right now.`
	verifyNoTargetsText       = `I could not find anything of yours to verify. Official verification covers a channel, supergroup or bot with a public @username that you created or administer, so give the subject a public username first and come back with /new.`
	verifyNoEligibleText      = `None of the ones you administer can be filed right now. Tap any of them to see why.`
	verifyPickButtonsText     = `Please use the buttons in my message above.`
	verifyNothingToCancelText = `There is nothing to cancel. Send /new to file an application.`
	verifyNoApplicationsText  = `You have not filed any verification applications yet. Send /new to file one.`
	verifyIncompleteText      = `The application is not complete yet, so let's fill the missing part first.`

	verifySubmitButtonText = `Submit application`
	verifySkipButtonText   = `Skip`
	verifyCancelButtonText = `Cancel application`
	verifyApplyButtonText  = `Apply for verification`
)

// verifyBotGlobalCommands are the commands honoured in every step, so an
// applicant is never trapped mid-form. Same contract as botFatherGlobalCommands.
var verifyBotGlobalCommands = map[string]bool{
	"start": true, "help": true, "cancel": true,
	verifyBotCmdNew: true, verifyBotCmdStatus: true,
}

// verifyCategoryLabels renders domain.VerificationCategories for humans. A
// missing entry falls back to the raw value, so adding a category cannot make the
// picker disappear.
var verifyCategoryLabels = map[string]string{
	"media":         "Media outlet",
	"government":    "Government",
	"company":       "Company",
	"brand":         "Brand",
	"sport":         "Sport",
	"culture":       "Culture",
	"education":     "Education",
	"nonprofit":     "Non-profit",
	"public_figure": "Public figure",
	"service":       "Service",
	"other":         "Other",
}

// verifyOption is one inline button before its token is minted.
type verifyOption struct {
	text   string
	choice string
	style  domain.MarkupButtonStyle
}

// ---------------------------------------------------------------------------
// Message entry point
// ---------------------------------------------------------------------------

// respondAsVerify generates and writes one @verifybot reply. It is called from
// OnPrivateMessage's goroutine, serialised per user by serviceBotReplyLock, on a
// context detached from the user's already-answered RPC.
func (s *Service) respondAsVerify(userID int64, msg domain.Message) {
	mu := s.serviceBotReplyLock(domain.VerifyBotUserID, userID)
	mu.Lock()
	defer mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if allowed, retryAfter := s.allowVerifyDialog(ctx, userID); !allowed {
		s.sendServiceBotReply(ctx, domain.VerifyBotUserID, userID, botReply{
			Text: verifyFloodText(retryAfter),
		})
		return
	}
	reply := s.handleVerify(ctx, userID, msg.Body)
	s.sendServiceBotReply(ctx, domain.VerifyBotUserID, userID, reply)
}

// allowVerifyDialog bounds dialog traffic per applicant. It is deliberately
// separate from the application-creation limit inside the verification service:
// that one bounds how many applications exist, this one bounds how hard the state
// machine itself can be driven, so a script cannot burn writes without ever
// submitting. A missing limiter means no bound, which is the pre-Redis behaviour.
func (s *Service) allowVerifyDialog(ctx context.Context, userID int64) (bool, int) {
	if s == nil || s.dialogLimiter == nil || s.dialogRateLimit <= 0 || s.dialogRateWindow <= 0 {
		return true, 0
	}
	key := fmt.Sprintf("bot-dialog:%d:%d", domain.VerifyBotUserID, userID)
	allowed, retryAfter, err := s.dialogLimiter.Allow(ctx, key, s.dialogRateLimit, s.dialogRateWindow)
	if err != nil {
		// A limiter outage must not silence the bot: fail open, log once.
		s.log.Warn("verifybot: dialog rate limit check failed",
			zap.Int64("user_id", userID), zap.Error(err))
		return true, 0
	}
	return allowed, retryAfter
}

func verifyFloodText(retryAfter int) string {
	if retryAfter > 0 {
		return fmt.Sprintf("Too many requests. Please wait %d seconds and try again.", retryAfter)
	}
	return "Too many requests. Please wait a moment and try again."
}

func (s *Service) handleVerify(ctx context.Context, userID int64, body string) botReply {
	text := strings.TrimSpace(body)
	state, found, err := s.bots.GetBotChatState(ctx, domain.VerifyBotUserID, userID)
	if err != nil {
		s.log.Error("verifybot: get chat state", zap.Int64("user_id", userID), zap.Error(err))
		return internalReply()
	}
	if found && state.Command != verifyBotCommand {
		// Unreachable dirty state (an older dialog shape): drop it rather than trap
		// the applicant in a step no handler owns.
		s.deleteVerifyState(ctx, userID)
		state, found = domain.BotChatState{}, false
	}
	if cmd, ok := parseBotCommand(text); ok {
		if verifyBotGlobalCommands[cmd] {
			return s.handleVerifyCommand(ctx, userID, cmd, state, found)
		}
		return botReply{Text: "I do not know that command. Send /help for the list."}
	}
	if !found {
		if text == "" {
			// Stickers and captionless media with no active dialog: stay silent
			// instead of answering every non-text message.
			return botReply{}
		}
		return botReply{Text: verifyBotIdleText}
	}
	if text == "" {
		return s.verifyStepReminder(state)
	}
	switch state.Step {
	case verifyStepDescription:
		return s.handleVerifyDescription(ctx, state, text)
	case verifyStepWebsite:
		return s.handleVerifyWebsite(ctx, state, text)
	case verifyStepSocial:
		return s.handleVerifySocial(ctx, state, text)
	case verifyStepPress:
		return s.handleVerifyPress(ctx, state, text)
	case verifyStepNote:
		return s.handleVerifyNote(ctx, state, text)
	case verifyStepIntro, verifyStepDone:
		return botReply{Text: verifyBotIdleText}
	case verifyStepTarget, verifyStepCategory, verifyStepConfirm:
		// Button-only steps. Answered deliberately without re-rendering the
		// keyboard: a fresh render mints a new token generation and would eventually
		// expire the very buttons the applicant is looking at.
		return botReply{Text: verifyPickButtonsText}
	default:
		s.deleteVerifyState(ctx, userID)
		return botReply{Text: "Something went wrong, I forgot what we were doing. Send /new to start again."}
	}
}

func (s *Service) handleVerifyCommand(ctx context.Context, userID int64, cmd string, state domain.BotChatState, found bool) botReply {
	if cmd == "help" {
		return botReply{Text: verifyBotHelpText}
	}
	if s.verification == nil {
		return botReply{Text: verifyUnavailableText}
	}
	switch cmd {
	case "start":
		return s.verifyIntro(ctx, userID, state, found)
	case verifyBotCmdNew:
		if !found {
			state = verifyNewState(userID)
		}
		return s.startVerifyApplication(ctx, state)
	case verifyBotCmdStatus:
		return s.verifyStatusReply(ctx, userID)
	case "cancel":
		return s.cancelVerifyApplication(ctx, userID, state, found)
	default:
		return botReply{Text: verifyBotHelpText}
	}
}

// ---------------------------------------------------------------------------
// Callback entry point
// ---------------------------------------------------------------------------

// OnCallbackQuery answers an inline-button click on a message sent by one of the
// built-in bots; it is the bots side of rpc.ServiceBotCallbacks. ok=false means
// "not one of my bots", which the edge reports as DATA_INVALID.
//
// The RPC edge calls this synchronously instead of pushing
// updateBotCallbackQuery, because an internal bot has no MTProto session and no
// Bot API queue: the ordinary path could only ever end in BOT_RESPONSE_TIMEOUT.
func (s *Service) OnCallbackQuery(ctx context.Context, query domain.BotCallbackQuery) (domain.BotCallbackAnswer, bool, error) {
	if s == nil || s.bots == nil || !s.HandlesBot(query.BotUserID) {
		return domain.BotCallbackAnswer{}, false, nil
	}
	if query.UserID <= 0 || query.UserID == query.BotUserID {
		return domain.BotCallbackAnswer{}, false, nil
	}
	if query.BotUserID != domain.VerifyBotUserID {
		// The other built-in bots never attach an inline keyboard, so there is
		// nothing to route. An empty answer still beats hanging the click for the
		// whole callback timeout.
		return domain.BotCallbackAnswer{}, true, nil
	}
	return s.onVerifyCallback(ctx, query)
}

func (s *Service) onVerifyCallback(ctx context.Context, query domain.BotCallbackQuery) (domain.BotCallbackAnswer, bool, error) {
	userID := query.UserID
	mu := s.serviceBotReplyLock(domain.VerifyBotUserID, userID)
	mu.Lock()
	defer mu.Unlock()

	if allowed, retryAfter := s.allowVerifyDialog(ctx, userID); !allowed {
		// Answering with an alert keeps the click from hanging for the whole callback
		// timeout, and the dialog state is left exactly as it was.
		return domain.BotCallbackAnswer{Message: verifyFloodText(retryAfter), Alert: true}, true, nil
	}

	state, found, err := s.bots.GetBotChatState(ctx, domain.VerifyBotUserID, userID)
	if err != nil {
		s.log.Error("verifybot: get chat state for callback", zap.Int64("user_id", userID), zap.Error(err))
		return domain.BotCallbackAnswer{}, true, err
	}
	if !found || state.Command != verifyBotCommand {
		return verifyAlert(verifyExpiredButtonText), true, nil
	}
	choice, ok := verifyResolveOption(state, query.Data)
	if !ok {
		// The only way to reach this is callback data that is not in *this* user's
		// own token table: a button from an expired generation, or data that was
		// replayed or fabricated. All of them are refused identically and change
		// nothing at all.
		return verifyAlert(verifyExpiredButtonText), true, nil
	}
	if s.verification == nil {
		return verifyAlert(verifyUnavailableText), true, nil
	}
	// The follow-up message is written on a context detached from the caller's
	// RPC: the answer unblocks the click, and the message must survive the client
	// hanging up immediately afterwards.
	sendCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	reply, answer := s.applyVerifyChoice(sendCtx, state, choice)
	s.sendServiceBotReply(sendCtx, domain.VerifyBotUserID, userID, reply)
	return answer, true, nil
}

// applyVerifyChoice executes one resolved button choice.
func (s *Service) applyVerifyChoice(ctx context.Context, state domain.BotChatState, choice string) (botReply, domain.BotCallbackAnswer) {
	switch {
	case choice == verifyChoiceApply:
		return s.startVerifyApplication(ctx, state), domain.BotCallbackAnswer{}
	case choice == verifyChoiceAbort:
		return s.cancelVerifyApplication(ctx, state.UserID, state, true), domain.BotCallbackAnswer{}
	case choice == verifyChoiceSubmit:
		return s.submitVerifyApplication(ctx, state), domain.BotCallbackAnswer{}
	case choice == verifyChoiceSkip:
		return s.skipVerifyStep(ctx, state), domain.BotCallbackAnswer{}
	case strings.HasPrefix(choice, verifyChoiceTargetPrefix):
		return s.chooseVerifyTarget(ctx, state, strings.TrimPrefix(choice, verifyChoiceTargetPrefix)), domain.BotCallbackAnswer{}
	case strings.HasPrefix(choice, verifyChoiceCategoryPrefix):
		return s.chooseVerifyCategory(ctx, state, strings.TrimPrefix(choice, verifyChoiceCategoryPrefix)), domain.BotCallbackAnswer{}
	case strings.HasPrefix(choice, verifyChoiceBlockedPrefix):
		// An ineligible candidate is offered as a button only so the reason can be
		// told on tap. It changes nothing.
		return botReply{}, verifyAlert(verifyIneligibleText(strings.TrimPrefix(choice, verifyChoiceBlockedPrefix)))
	default:
		s.log.Warn("verifybot: unknown button choice", zap.Int64("user_id", state.UserID), zap.String("choice", choice))
		return botReply{}, verifyAlert(verifyExpiredButtonText)
	}
}

// verifyAlert is a refusal shown as a modal on the clicking client. A refusal is
// deliberately an alert rather than a toast: it is the only feedback the applicant
// gets, because a refused click writes no message.
func verifyAlert(text string) domain.BotCallbackAnswer {
	return domain.BotCallbackAnswer{Alert: true, Message: verifyClampAnswer(text)}
}

func verifyClampAnswer(text string) string {
	return verifyTruncate(text, domain.MaxBotCallbackAnswerLen)
}

// ---------------------------------------------------------------------------
// Flow
// ---------------------------------------------------------------------------

// verifyIntro answers /start. It does not throw away an application in progress:
// the button it offers resumes the existing draft.
func (s *Service) verifyIntro(ctx context.Context, userID int64, state domain.BotChatState, found bool) botReply {
	if !found {
		state = verifyNewState(userID)
		state.Step = verifyStepIntro
	}
	markup := s.verifyOptionKeyboard(&state, [][]verifyOption{{
		{text: verifyApplyButtonText, choice: verifyChoiceApply, style: domain.MarkupButtonStylePrimary},
	}})
	if !s.saveVerifyState(ctx, state) {
		return internalReply()
	}
	return botReply{Text: verifyBotStartText, ReplyMarkup: markup}
}

// startVerifyApplication is /new and the Apply button. An applicant has at most
// one draft, so an existing one is resumed rather than replaced -- which is also
// what StartDraft does, so the dialog follows the record instead of guessing.
func (s *Service) startVerifyApplication(ctx context.Context, state domain.BotChatState) botReply {
	if s.verification == nil {
		return botReply{Text: verifyUnavailableText}
	}
	verifyEnsureDraft(&state)
	app, err := s.verification.Draft(ctx, state.UserID)
	switch {
	case err == nil && app.ID > 0:
		verifyHydrateState(&state, app)
		lead := fmt.Sprintf("You already have application #%d in progress for %s, so let's carry on. Send /cancel to drop it and start over.",
			app.ID, verifyTargetLabel(app.TargetTitle, app.TargetUsername))
		return s.verifyAdvance(ctx, state, verifyMissingStep(verifyDraftInputOf(app)), lead)
	case err != nil && !errors.Is(err, domain.ErrVerificationApplicationNotFound):
		return s.verifyErrorReply(state.UserID, "read draft", err)
	}
	return s.verifyAdvance(ctx, state, verifyStepTarget, "")
}

// chooseVerifyTarget opens the draft for the picked subject.
//
// The type and id come from the token table, i.e. from state this server wrote
// when it rendered the keyboard -- never from the callback data. StartDraft
// re-checks ownership and eligibility anyway, and it is idempotent: a second
// press returns the same draft with created=false, so the same reply is produced
// and no second application exists.
func (s *Service) chooseVerifyTarget(ctx context.Context, state domain.BotChatState, payload string) botReply {
	kind, rawID, split := strings.Cut(payload, ":")
	targetID, err := strconv.ParseInt(rawID, 10, 64)
	targetType := domain.VerificationTargetType(kind)
	if !split || err != nil || targetID <= 0 || !targetType.Valid() {
		s.log.Warn("verifybot: malformed target choice", zap.Int64("user_id", state.UserID), zap.String("payload", payload))
		return botReply{Text: verifyExpiredButtonText}
	}
	app, created, err := s.verification.StartDraft(ctx, domain.SubmitVerificationApplicationRequest{
		ApplicantUserID: state.UserID,
		TargetType:      targetType,
		TargetID:        targetID,
	})
	if err != nil {
		return s.verifyErrorReply(state.UserID, "start draft", err)
	}
	if !created && (app.TargetID != targetID || app.TargetType != targetType) {
		// One draft per applicant is the store's contract, so say so instead of
		// silently switching the subject under the applicant.
		return botReply{Text: fmt.Sprintf("You already have application #%d in progress for %s. Send /cancel to drop it, then /new to pick another subject.",
			app.ID, verifyTargetLabel(app.TargetTitle, app.TargetUsername))}
	}
	verifyHydrateState(&state, app)
	lead := fmt.Sprintf("Subject: %s. This is application #%d.", verifyTargetLabel(app.TargetTitle, app.TargetUsername), app.ID)
	return s.verifyAdvance(ctx, state, verifyMissingStep(verifyDraftInputOf(app)), lead)
}

func (s *Service) chooseVerifyCategory(ctx context.Context, state domain.BotChatState, payload string) botReply {
	if !domain.ValidVerificationCategory(payload) {
		s.log.Warn("verifybot: unknown category choice", zap.Int64("user_id", state.UserID), zap.String("payload", payload))
		return botReply{Text: verifyExpiredButtonText}
	}
	verifyEnsureDraft(&state)
	state.Draft[verifyDraftCategory] = payload
	if reply, ok := s.saveVerifyDraft(ctx, &state); !ok {
		return reply
	}
	return s.verifyAdvance(ctx, state, verifyStepDescription, "Category: "+verifyCategoryLabel(payload)+".")
}

func (s *Service) skipVerifyStep(ctx context.Context, state domain.BotChatState) botReply {
	verifyEnsureDraft(&state)
	switch state.Step {
	case verifyStepSocial:
		state.Draft[verifyDraftSocial] = ""
		if reply, ok := s.saveVerifyDraft(ctx, &state); !ok {
			return reply
		}
		return s.verifyAdvance(ctx, state, verifyStepPress, "No social links.")
	case verifyStepNote:
		state.Draft[verifyDraftNote] = ""
		if reply, ok := s.saveVerifyDraft(ctx, &state); !ok {
			return reply
		}
		return s.verifyAdvance(ctx, state, verifyStepConfirm, "No extra comment.")
	default:
		return s.verifyStepReminder(state)
	}
}

func (s *Service) handleVerifyDescription(ctx context.Context, state domain.BotChatState, text string) botReply {
	length := utf8.RuneCountInString(text)
	if length < domain.MinVerificationDescriptionLength {
		return botReply{Text: fmt.Sprintf("That is %d characters and I need at least %d. Say what the subject is, who is behind it and what it is publicly known for.",
			length, domain.MinVerificationDescriptionLength)}
	}
	if length > domain.MaxVerificationDescriptionLength {
		return botReply{Text: fmt.Sprintf("That is %d characters and the limit is %d. Please shorten it.",
			length, domain.MaxVerificationDescriptionLength)}
	}
	verifyEnsureDraft(&state)
	state.Draft[verifyDraftDescription] = text
	if reply, ok := s.saveVerifyDraft(ctx, &state); !ok {
		return reply
	}
	return s.verifyAdvance(ctx, state, verifyStepWebsite, "Description saved.")
}

func (s *Service) handleVerifyWebsite(ctx context.Context, state domain.BotChatState, text string) botReply {
	if err := domain.ValidateVerificationURL(text); err != nil {
		return botReply{Text: verifyURLRejectText}
	}
	verifyEnsureDraft(&state)
	state.Draft[verifyDraftWebsite] = text
	if reply, ok := s.saveVerifyDraft(ctx, &state); !ok {
		return reply
	}
	return s.verifyAdvance(ctx, state, verifyStepSocial, "Website saved.")
}

func (s *Service) handleVerifySocial(ctx context.Context, state domain.BotChatState, text string) botReply {
	if verifyIsSkipText(text) {
		return s.skipVerifyStep(ctx, state)
	}
	links := verifySplitLinks(text)
	if len(links) > domain.MaxVerificationSocialLinks {
		return botReply{Text: fmt.Sprintf("That is %d links and I can keep at most %d. Please send the most important ones.",
			len(links), domain.MaxVerificationSocialLinks)}
	}
	if reply, ok := verifyCheckLinks(links); !ok {
		return reply
	}
	verifyEnsureDraft(&state)
	state.Draft[verifyDraftSocial] = strings.Join(links, "\n")
	if reply, ok := s.saveVerifyDraft(ctx, &state); !ok {
		return reply
	}
	return s.verifyAdvance(ctx, state, verifyStepPress, fmt.Sprintf("Saved %d social link(s).", len(links)))
}

func (s *Service) handleVerifyPress(ctx context.Context, state domain.BotChatState, text string) botReply {
	links := verifySplitLinks(text)
	if len(links) < domain.MinVerificationPressLinks {
		return botReply{Text: fmt.Sprintf("I counted %d link(s) and the official bar is at least %d, from outlets you do not control. Send them in one message, one per line.",
			len(links), domain.MinVerificationPressLinks)}
	}
	if len(links) > domain.MaxVerificationPressLinks {
		return botReply{Text: fmt.Sprintf("That is %d links and I can keep at most %d. Please send the strongest ones.",
			len(links), domain.MaxVerificationPressLinks)}
	}
	if reply, ok := verifyCheckLinks(links); !ok {
		return reply
	}
	verifyEnsureDraft(&state)
	state.Draft[verifyDraftPress] = strings.Join(links, "\n")
	if reply, ok := s.saveVerifyDraft(ctx, &state); !ok {
		return reply
	}
	return s.verifyAdvance(ctx, state, verifyStepNote, fmt.Sprintf("Saved %d press link(s).", len(links)))
}

func (s *Service) handleVerifyNote(ctx context.Context, state domain.BotChatState, text string) botReply {
	if verifyIsSkipText(text) {
		return s.skipVerifyStep(ctx, state)
	}
	if utf8.RuneCountInString(text) > domain.MaxVerificationCommentLength {
		return botReply{Text: fmt.Sprintf("That comment is %d characters and the limit is %d. Please shorten it.",
			utf8.RuneCountInString(text), domain.MaxVerificationCommentLength)}
	}
	verifyEnsureDraft(&state)
	state.Draft[verifyDraftNote] = text
	if reply, ok := s.saveVerifyDraft(ctx, &state); !ok {
		return reply
	}
	return s.verifyAdvance(ctx, state, verifyStepConfirm, "Comment saved.")
}

// submitVerifyApplication files the draft.
//
// Pressing Submit twice is idempotent by construction: the first press moves the
// dialog to verifyStepDone, and a repeat press is answered from the stored
// application instead of calling Submit again.
func (s *Service) submitVerifyApplication(ctx context.Context, state domain.BotChatState) botReply {
	applicationID := verifyDraftInt(state, verifyDraftAppID)
	if applicationID <= 0 {
		return botReply{Text: verifyBotIdleText}
	}
	if state.Step == verifyStepDone {
		app, err := s.verification.Application(ctx, applicationID)
		if err != nil {
			return s.verifyErrorReply(state.UserID, "read application", err)
		}
		if app.Status == domain.VerificationStatusSubmitted || app.Status == domain.VerificationStatusInReview {
			return botReply{Text: verifySubmittedText(app)}
		}
		// It has moved on since it was filed (decided, or withdrawn from elsewhere):
		// report where it stands instead of repeating a stale confirmation.
		return botReply{Text: fmt.Sprintf("Application #%d for %s: %s. Send /status for the full list.",
			app.ID, verifyTargetLabel(app.TargetTitle, app.TargetUsername), verifyStatusLabel(app.Status))}
	}
	input := verifyDraftInput(state)
	if err := input.ValidateForSubmission(); err != nil {
		return s.verifyAdvance(ctx, state, verifyMissingStep(input), verifyIncompleteText)
	}
	app, err := s.verification.Submit(ctx, state.UserID, applicationID, verifyDraftInt(state, verifyDraftVersion))
	if errors.Is(err, domain.ErrVerificationVersionConflict) {
		if current, readErr := s.verification.Application(ctx, applicationID); readErr == nil {
			app, err = s.verification.Submit(ctx, state.UserID, applicationID, current.Version)
		}
	}
	if errors.Is(err, domain.ErrVerificationApplicationInvalid) {
		// The service enforces the full official bar at submission and the record is
		// the truth, not this dialog's cursor. Re-read it and put the applicant back
		// on the step that is actually missing instead of narrating a validation
		// error they cannot act on.
		if current, readErr := s.verification.Application(ctx, applicationID); readErr == nil && current.Editable() {
			verifyHydrateState(&state, current)
			return s.verifyAdvance(ctx, state, verifyMissingStep(verifyDraftInputOf(current)), verifyIncompleteText)
		}
	}
	if err != nil {
		return s.verifyErrorReply(state.UserID, "submit application", err)
	}
	state.Step = verifyStepDone
	verifyEnsureDraft(&state)
	state.Draft[verifyDraftVersion] = strconv.FormatInt(app.Version, 10)
	if !s.saveVerifyState(ctx, state) {
		return internalReply()
	}
	return botReply{Text: verifySubmittedText(app)}
}

// cancelVerifyApplication withdraws the applicant's open application, whether it
// is still a draft in this dialog or already in the review queue.
func (s *Service) cancelVerifyApplication(ctx context.Context, userID int64, state domain.BotChatState, haveState bool) botReply {
	if s.verification == nil {
		return botReply{Text: verifyUnavailableText}
	}
	app, found := s.verifyActiveApplication(ctx, userID, state, haveState)
	if haveState {
		s.deleteVerifyState(ctx, userID)
	}
	if !found {
		return botReply{Text: verifyNothingToCancelText}
	}
	cancelled, err := s.verification.Cancel(ctx, userID, app.ID, app.Version, "withdrawn by the applicant")
	if errors.Is(err, domain.ErrVerificationVersionConflict) {
		if current, readErr := s.verification.Application(ctx, app.ID); readErr == nil {
			cancelled, err = s.verification.Cancel(ctx, userID, app.ID, current.Version, "withdrawn by the applicant")
		}
	}
	if err != nil {
		return s.verifyErrorReply(userID, "cancel application", err)
	}
	return botReply{Text: fmt.Sprintf("Application #%d for %s is withdrawn. Send /new when you want to file another one.",
		cancelled.ID, verifyTargetLabel(cancelled.TargetTitle, cancelled.TargetUsername))}
}

// verifyActiveApplication finds the application /cancel should act on: the one
// this dialog is holding, else the open draft, else the newest still-active
// record.
func (s *Service) verifyActiveApplication(ctx context.Context, userID int64, state domain.BotChatState, haveState bool) (domain.VerificationApplication, bool) {
	if haveState {
		if id := verifyDraftInt(state, verifyDraftAppID); id > 0 {
			if app, err := s.verification.Application(ctx, id); err == nil &&
				app.ApplicantUserID == userID && app.Status.Active() {
				return app, true
			}
		}
	}
	if app, err := s.verification.Draft(ctx, userID); err == nil && app.ID > 0 {
		return app, true
	}
	apps, err := s.verification.ApplicantApplications(ctx, userID, verifyStatusListLimit)
	if err != nil {
		s.log.Warn("verifybot: list applications for cancel", zap.Int64("user_id", userID), zap.Error(err))
		return domain.VerificationApplication{}, false
	}
	for _, app := range apps {
		if app.Status.Active() {
			return app, true
		}
	}
	return domain.VerificationApplication{}, false
}

func (s *Service) verifyStatusReply(ctx context.Context, userID int64) botReply {
	apps, err := s.verification.ApplicantApplications(ctx, userID, verifyStatusListLimit)
	if err != nil {
		return s.verifyErrorReply(userID, "list applications", err)
	}
	if len(apps) == 0 {
		return botReply{Text: verifyNoApplicationsText}
	}
	var b strings.Builder
	b.WriteString("Your verification applications:")
	for _, app := range apps {
		b.WriteString("\n\n#")
		b.WriteString(strconv.FormatInt(app.ID, 10))
		b.WriteString(" - ")
		b.WriteString(verifyTargetLabel(app.TargetTitle, app.TargetUsername))
		b.WriteString("\nStatus: ")
		b.WriteString(verifyStatusLabel(app.Status))
		if date := verifyDateLabel(app); date != "" {
			b.WriteString(" (")
			b.WriteString(date)
			b.WriteString(")")
		}
		// Only DecisionReason is ever shown: it is the reason written to be read by
		// the applicant. app.InternalNote is the reviewers' private note and must
		// never appear in a bot message.
		if app.Status == domain.VerificationStatusRejected {
			if reason := strings.TrimSpace(app.DecisionReason); reason != "" {
				b.WriteString("\nReason: ")
				b.WriteString(reason)
			}
		}
	}
	b.WriteString("\n\nSend /new to file another application.")
	return botReply{Text: b.String()}
}

// verifyAdvance moves the dialog to step and renders that step's prompt. lead is
// an optional acknowledgement of what was just accepted.
func (s *Service) verifyAdvance(ctx context.Context, state domain.BotChatState, step, lead string) botReply {
	state.Step = step
	verifyEnsureDraft(&state)
	var reply botReply
	switch step {
	case verifyStepTarget:
		return s.verifyTargetPrompt(ctx, state, lead)
	case verifyStepCategory:
		markup := s.verifyOptionKeyboard(&state, verifyCategoryRows())
		reply = botReply{Text: verifyJoin(lead, verifyPromptCategoryText), ReplyMarkup: markup}
	case verifyStepDescription:
		reply = botReply{Text: verifyJoin(lead, fmt.Sprintf(verifyPromptDescriptionTmpl,
			domain.MinVerificationDescriptionLength, domain.MaxVerificationDescriptionLength))}
	case verifyStepWebsite:
		reply = botReply{Text: verifyJoin(lead, verifyPromptWebsiteText)}
	case verifyStepSocial:
		markup := s.verifyOptionKeyboard(&state, verifySkipRows())
		reply = botReply{
			Text:        verifyJoin(lead, fmt.Sprintf(verifyPromptSocialTmpl, domain.MaxVerificationSocialLinks)),
			ReplyMarkup: markup,
		}
	case verifyStepPress:
		reply = botReply{Text: verifyJoin(lead, fmt.Sprintf(verifyPromptPressTmpl,
			domain.MinVerificationPressLinks, domain.MaxVerificationPressLinks))}
	case verifyStepNote:
		markup := s.verifyOptionKeyboard(&state, verifySkipRows())
		reply = botReply{Text: verifyJoin(lead, verifyPromptNoteText), ReplyMarkup: markup}
	case verifyStepConfirm:
		markup := s.verifyOptionKeyboard(&state, [][]verifyOption{
			{{text: verifySubmitButtonText, choice: verifyChoiceSubmit, style: domain.MarkupButtonStyleSuccess}},
			{{text: verifyCancelButtonText, choice: verifyChoiceAbort, style: domain.MarkupButtonStyleDanger}},
		})
		reply = botReply{Text: verifyJoin(lead, verifySummaryText(state)), ReplyMarkup: markup}
	default:
		s.log.Error("verifybot: advance to unknown step", zap.Int64("user_id", state.UserID), zap.String("step", step))
		return internalReply()
	}
	if !s.saveVerifyState(ctx, state) {
		return internalReply()
	}
	return reply
}

// verifyTargetPrompt renders the subject picker. Ineligible candidates are shown
// too, as buttons that only explain themselves, so "why is my channel not
// listed" never needs asking.
func (s *Service) verifyTargetPrompt(ctx context.Context, state domain.BotChatState, lead string) botReply {
	targets, err := s.verification.EligibleTargets(ctx, state.UserID)
	if err != nil {
		return s.verifyErrorReply(state.UserID, "eligible targets", err)
	}
	if len(targets) == 0 {
		return botReply{Text: verifyJoin(lead, verifyNoTargetsText)}
	}
	rows := make([][]verifyOption, 0, verifyMaxTargetButtons+1)
	eligible := 0
	for _, wantEligible := range []bool{true, false} {
		for _, target := range targets {
			if target.Eligible != wantEligible || len(rows) >= verifyMaxTargetButtons {
				continue
			}
			label := verifyTargetButtonText(target)
			if wantEligible {
				eligible++
				rows = append(rows, []verifyOption{{
					text:   label,
					choice: verifyChoiceTargetPrefix + string(target.Type) + ":" + strconv.FormatInt(target.ID, 10),
				}})
				continue
			}
			rows = append(rows, []verifyOption{{
				text:   label + " (unavailable)",
				choice: verifyChoiceBlockedPrefix + target.Reason,
			}})
		}
	}
	rows = append(rows, []verifyOption{{
		text:   verifyCancelButtonText,
		choice: verifyChoiceAbort,
		style:  domain.MarkupButtonStyleDanger,
	}})
	prompt := verifyPromptTargetText
	if eligible == 0 {
		prompt = verifyNoEligibleText
	}
	markup := s.verifyOptionKeyboard(&state, rows)
	if !s.saveVerifyState(ctx, state) {
		return internalReply()
	}
	return botReply{Text: verifyJoin(lead, prompt), ReplyMarkup: markup}
}

func (s *Service) verifyStepReminder(state domain.BotChatState) botReply {
	switch state.Step {
	case verifyStepDescription:
		return botReply{Text: fmt.Sprintf(verifyPromptDescriptionTmpl,
			domain.MinVerificationDescriptionLength, domain.MaxVerificationDescriptionLength)}
	case verifyStepWebsite:
		return botReply{Text: verifyPromptWebsiteText}
	case verifyStepSocial:
		return botReply{Text: fmt.Sprintf(verifyPromptSocialTmpl, domain.MaxVerificationSocialLinks)}
	case verifyStepPress:
		return botReply{Text: fmt.Sprintf(verifyPromptPressTmpl,
			domain.MinVerificationPressLinks, domain.MaxVerificationPressLinks)}
	case verifyStepNote:
		return botReply{Text: verifyPromptNoteText}
	case verifyStepTarget, verifyStepCategory, verifyStepConfirm:
		return botReply{Text: verifyPickButtonsText}
	default:
		return botReply{Text: verifyBotIdleText}
	}
}

// ---------------------------------------------------------------------------
// Applicant notifications (verification.ApplicantNotifier)
// ---------------------------------------------------------------------------

// SendVerificationNotice delivers one queued outbox row as an ordinary
// @verifybot message, so the decision lands in the applicant's history and
// dialog list like any other message. Returning an error keeps the outbox row
// pending for the next cycle.
func (s *Service) SendVerificationNotice(ctx context.Context, recipientUserID int64, app domain.VerificationApplication, kind string) error {
	if s == nil || s.messages == nil {
		return fmt.Errorf("verification notice: bots service is not configured")
	}
	if recipientUserID <= 0 {
		return fmt.Errorf("verification notice: recipient is empty")
	}
	text, ok := verifyNoticeText(app, kind)
	if !ok {
		return fmt.Errorf("verification notice: unsupported kind %q", kind)
	}
	mu := s.serviceBotReplyLock(domain.VerifyBotUserID, recipientUserID)
	mu.Lock()
	defer mu.Unlock()
	if _, sent := s.sendServiceBotReplyResult(ctx, domain.VerifyBotUserID, recipientUserID, botReply{Text: text}); !sent {
		return fmt.Errorf("verification notice: deliver %q for application %d", kind, app.ID)
	}
	return nil
}

// verifyNoticeText renders one outbox row for the applicant.
//
// app.InternalNote is NEVER rendered here, under any kind: it is the reviewers'
// private note, kept for the audit trail and the admin panel only. The single
// field that reaches the applicant is app.DecisionReason, which exists precisely
// because it was written to be read by them.
func verifyNoticeText(app domain.VerificationApplication, kind string) (string, bool) {
	target := verifyTargetLabel(app.TargetTitle, app.TargetUsername)
	switch kind {
	case verificationapp.NoticeKindSubmitted:
		return fmt.Sprintf("Application #%d for %s is in the review queue. I will message you here as soon as it is decided.",
			app.ID, target), true
	case verificationapp.NoticeKindApproved:
		return fmt.Sprintf("Application #%d is approved: %s is now officially verified and carries the badge. Thank you for the paperwork.",
			app.ID, target), true
	case verificationapp.NoticeKindRejected:
		text := fmt.Sprintf("Application #%d for %s was not approved.", app.ID, target)
		if reason := strings.TrimSpace(app.DecisionReason); reason != "" {
			text += "\n\nReason: " + reason
		}
		return text + "\n\nYou can file again later with /new once the reason no longer applies.", true
	case verificationapp.NoticeKindCancelled:
		return fmt.Sprintf("Application #%d for %s is withdrawn. Send /new whenever you want to file it again.",
			app.ID, target), true
	case verificationapp.NoticeKindRevoked:
		text := fmt.Sprintf("The official verification of %s has been revoked, and the badge is no longer shown.", target)
		if reason := strings.TrimSpace(app.DecisionReason); reason != "" {
			text += "\n\nReason: " + reason
		}
		return text, true
	default:
		return "", false
	}
}

// ---------------------------------------------------------------------------
// Button tokens
// ---------------------------------------------------------------------------

// verifyOptionKeyboard renders one inline keyboard and records its buttons in the
// per-user token table.
//
// SECURITY: callback data never carries a target id, a peer type, an application
// id or anything else the server would have to trust. Every button's data is
// verifyCallbackDataPrefix plus a freshly generated random token, and that token
// is meaningful only as a key into the token table of *this* applicant's own
// domain.BotChatState. A press is therefore incapable of naming a peer at all:
// the server resolves the choice from state it wrote itself when it rendered the
// keyboard. A client that replays or fabricates callback data can at best hit a
// token absent from its own table, which is refused -- so forging a choice for
// somebody else's channel is impossible by construction, not by validation.
//
// Tokens are minted afresh on every render and kept for verifyTokenGenerations
// renders. That window is what makes a second press of the same button resolve to
// the same choice (the handlers are idempotent, so the reply repeats) while the
// table stays bounded.
func (s *Service) verifyOptionKeyboard(state *domain.BotChatState, rows [][]verifyOption) *domain.MessageReplyMarkup {
	if state == nil || len(rows) == 0 {
		return nil
	}
	verifyEnsureDraft(state)
	generation := verifyDraftInt(*state, verifyDraftGeneration) + 1
	state.Draft[verifyDraftGeneration] = strconv.FormatInt(generation, 10)
	verifyPruneOptions(state, generation)
	markup := &domain.MessageReplyMarkup{Type: domain.MessageReplyMarkupInline}
	for _, row := range rows {
		buttons := make([]domain.MarkupButton, 0, len(row))
		for _, option := range row {
			if option.text == "" || option.choice == "" {
				continue
			}
			token := s.verifyOptionToken()
			state.Draft[verifyDraftOptionPrefix+token] = strconv.FormatInt(generation, 10) + "|" + option.choice
			buttons = append(buttons, domain.MarkupButton{
				Type:  domain.MarkupButtonCallback,
				Text:  option.text,
				Style: option.style,
				Data:  []byte(verifyCallbackDataPrefix + token),
			})
		}
		if len(buttons) > 0 {
			markup.Inline = append(markup.Inline, buttons)
		}
	}
	if len(markup.Inline) == 0 {
		return nil
	}
	return markup
}

// verifyOptionToken mints one opaque button token.
func (s *Service) verifyOptionToken() string {
	var buf [verifyOptionTokenBytes]byte
	if _, err := rand.Read(buf[:]); err == nil {
		return hex.EncodeToString(buf[:])
	}
	// A crypto/rand failure must not brick the dialog. Unpredictability is defence
	// in depth here rather than the load-bearing property -- a token is only ever
	// resolved against the caller's own chat state, and the RPC edge additionally
	// requires the data to appear in a keyboard of a message in the caller's own
	// box -- so a monotonic fallback is safe.
	return strconv.FormatInt(s.now().UnixNano()+s.replySeq.Add(1), 36)
}

// verifyResolveOption maps callback data back onto the choice recorded for it.
// Anything that is not in this user's own table is refused.
func verifyResolveOption(state domain.BotChatState, data []byte) (string, bool) {
	raw := string(data)
	if !strings.HasPrefix(raw, verifyCallbackDataPrefix) {
		return "", false
	}
	token := raw[len(verifyCallbackDataPrefix):]
	if token == "" || len(token) > verifyOptionTokenMaxLen {
		return "", false
	}
	value, found := state.Draft[verifyDraftOptionPrefix+token]
	if !found {
		return "", false
	}
	_, choice, split := strings.Cut(value, "|")
	if !split || choice == "" {
		return "", false
	}
	return choice, true
}

// verifyPruneOptions drops tokens older than the retained window.
func verifyPruneOptions(state *domain.BotChatState, generation int64) {
	oldest := generation - verifyTokenGenerations + 1
	for key, value := range state.Draft {
		if !strings.HasPrefix(key, verifyDraftOptionPrefix) {
			continue
		}
		rawGen, _, _ := strings.Cut(value, "|")
		gen, err := strconv.ParseInt(rawGen, 10, 64)
		if err != nil || gen < oldest {
			delete(state.Draft, key)
		}
	}
}

func verifyCategoryRows() [][]verifyOption {
	rows := make([][]verifyOption, 0, len(domain.VerificationCategories)/2+2)
	var row []verifyOption
	for _, category := range domain.VerificationCategories {
		row = append(row, verifyOption{
			text:   verifyCategoryLabel(category),
			choice: verifyChoiceCategoryPrefix + category,
		})
		if len(row) == 2 {
			rows = append(rows, row)
			row = nil
		}
	}
	if len(row) > 0 {
		rows = append(rows, row)
	}
	return append(rows, []verifyOption{{
		text:   verifyCancelButtonText,
		choice: verifyChoiceAbort,
		style:  domain.MarkupButtonStyleDanger,
	}})
}

func verifySkipRows() [][]verifyOption {
	return [][]verifyOption{
		{{text: verifySkipButtonText, choice: verifyChoiceSkip}},
		{{text: verifyCancelButtonText, choice: verifyChoiceAbort, style: domain.MarkupButtonStyleDanger}},
	}
}

// ---------------------------------------------------------------------------
// State helpers
// ---------------------------------------------------------------------------

func verifyNewState(userID int64) domain.BotChatState {
	return domain.BotChatState{
		BotUserID: domain.VerifyBotUserID,
		UserID:    userID,
		Command:   verifyBotCommand,
		Step:      verifyStepIntro,
		Draft:     map[string]string{},
	}
}

func verifyEnsureDraft(state *domain.BotChatState) {
	if state.Draft == nil {
		state.Draft = map[string]string{}
	}
	if state.BotUserID == 0 {
		state.BotUserID = domain.VerifyBotUserID
	}
	if state.Command == "" {
		state.Command = verifyBotCommand
	}
}

func (s *Service) saveVerifyState(ctx context.Context, state domain.BotChatState) bool {
	verifyEnsureDraft(&state)
	state.BotUserID = domain.VerifyBotUserID
	clone := domain.BotChatState{
		BotUserID: state.BotUserID,
		UserID:    state.UserID,
		Command:   state.Command,
		Step:      state.Step,
		Draft:     make(map[string]string, len(state.Draft)),
	}
	for key, value := range state.Draft {
		clone.Draft[key] = value
	}
	if err := s.bots.UpsertBotChatState(ctx, clone); err != nil {
		s.log.Error("verifybot: save chat state", zap.Int64("user_id", state.UserID), zap.Error(err))
		return false
	}
	return true
}

func (s *Service) deleteVerifyState(ctx context.Context, userID int64) {
	if err := s.bots.DeleteBotChatState(ctx, domain.VerifyBotUserID, userID); err != nil {
		s.log.Error("verifybot: delete chat state", zap.Int64("user_id", userID), zap.Error(err))
	}
}

// saveVerifyDraft pushes the accumulated payload into the application record and
// refreshes the stored optimistic-locking version.
func (s *Service) saveVerifyDraft(ctx context.Context, state *domain.BotChatState) (botReply, bool) {
	verifyEnsureDraft(state)
	applicationID := verifyDraftInt(*state, verifyDraftAppID)
	if applicationID <= 0 {
		return botReply{Text: verifyBotIdleText}, false
	}
	input := verifyDraftInput(*state)
	app, err := s.verification.SaveDraft(ctx, state.UserID, applicationID, verifyDraftInt(*state, verifyDraftVersion), input)
	if errors.Is(err, domain.ErrVerificationVersionConflict) {
		// The record moved under us (a second device in the same dialog). Re-read the
		// authoritative version once and retry; a second conflict is reported.
		if current, readErr := s.verification.Application(ctx, applicationID); readErr == nil {
			app, err = s.verification.SaveDraft(ctx, state.UserID, applicationID, current.Version, input)
		}
	}
	if err != nil {
		return s.verifyErrorReply(state.UserID, "save draft", err), false
	}
	state.Draft[verifyDraftVersion] = strconv.FormatInt(app.Version, 10)
	return botReply{}, true
}

func verifyHydrateState(state *domain.BotChatState, app domain.VerificationApplication) {
	verifyEnsureDraft(state)
	state.Draft[verifyDraftAppID] = strconv.FormatInt(app.ID, 10)
	state.Draft[verifyDraftVersion] = strconv.FormatInt(app.Version, 10)
	state.Draft[verifyDraftCategory] = app.Category
	state.Draft[verifyDraftDescription] = app.Description
	state.Draft[verifyDraftWebsite] = app.OfficialWebsite
	state.Draft[verifyDraftSocial] = strings.Join(app.SocialLinks, "\n")
	state.Draft[verifyDraftPress] = strings.Join(app.PressLinks, "\n")
	state.Draft[verifyDraftNote] = app.AdditionalNote
	state.Draft[verifyDraftTargetTitle] = app.TargetTitle
	state.Draft[verifyDraftTargetUsername] = app.TargetUsername
}

func verifyDraftInput(state domain.BotChatState) domain.VerificationDraftInput {
	return domain.VerificationDraftInput{
		Category:        state.Draft[verifyDraftCategory],
		Description:     state.Draft[verifyDraftDescription],
		OfficialWebsite: state.Draft[verifyDraftWebsite],
		SocialLinks:     verifySplitLinks(state.Draft[verifyDraftSocial]),
		PressLinks:      verifySplitLinks(state.Draft[verifyDraftPress]),
		AdditionalNote:  state.Draft[verifyDraftNote],
	}.Normalize()
}

func verifyDraftInputOf(app domain.VerificationApplication) domain.VerificationDraftInput {
	return domain.VerificationDraftInput{
		Category:        app.Category,
		Description:     app.Description,
		OfficialWebsite: app.OfficialWebsite,
		SocialLinks:     app.SocialLinks,
		PressLinks:      app.PressLinks,
		AdditionalNote:  app.AdditionalNote,
	}.Normalize()
}

// verifyMissingStep is the first step whose field the official bar still needs.
// It is what /new resumes into and what an incomplete Submit is bounced to.
// The optional steps (social links, comment) are never resumed into, because a
// skipped optional field is indistinguishable from an unasked one and an
// applicant must not be forced back through it.
func verifyMissingStep(input domain.VerificationDraftInput) string {
	input = input.Normalize()
	switch {
	case !domain.ValidVerificationCategory(input.Category):
		return verifyStepCategory
	case utf8.RuneCountInString(input.Description) < domain.MinVerificationDescriptionLength:
		return verifyStepDescription
	case input.OfficialWebsite == "":
		return verifyStepWebsite
	case len(input.PressLinks) < domain.MinVerificationPressLinks:
		return verifyStepPress
	default:
		return verifyStepConfirm
	}
}

func verifyDraftInt(state domain.BotChatState, key string) int64 {
	value, err := strconv.ParseInt(strings.TrimSpace(state.Draft[key]), 10, 64)
	if err != nil {
		return 0
	}
	return value
}

// ---------------------------------------------------------------------------
// Rendering helpers
// ---------------------------------------------------------------------------

func verifySummaryText(state domain.BotChatState) string {
	input := verifyDraftInput(state)
	var b strings.Builder
	b.WriteString("Here is your application. Nothing reaches the reviewers until you tap ")
	b.WriteString(verifySubmitButtonText)
	b.WriteString(".\n\nSubject: ")
	b.WriteString(verifyTargetLabel(state.Draft[verifyDraftTargetTitle], state.Draft[verifyDraftTargetUsername]))
	b.WriteString("\nCategory: ")
	b.WriteString(verifyCategoryLabel(input.Category))
	b.WriteString("\nWebsite: ")
	b.WriteString(input.OfficialWebsite)
	b.WriteString("\n\nDescription:\n")
	b.WriteString(input.Description)
	b.WriteString("\n\nPress coverage:")
	for _, link := range input.PressLinks {
		b.WriteString("\n- ")
		b.WriteString(link)
	}
	if len(input.SocialLinks) > 0 {
		b.WriteString("\n\nSocial links:")
		for _, link := range input.SocialLinks {
			b.WriteString("\n- ")
			b.WriteString(link)
		}
	}
	if input.AdditionalNote != "" {
		b.WriteString("\n\nComment:\n")
		b.WriteString(input.AdditionalNote)
	}
	return b.String()
}

func verifySubmittedText(app domain.VerificationApplication) string {
	return fmt.Sprintf("Application #%d is filed for %s.\n\nA reviewer reads it by hand, and I message you here with the decision. Send /status any time to see where it stands, or /cancel to withdraw it while it is still open.",
		app.ID, verifyTargetLabel(app.TargetTitle, app.TargetUsername))
}

// verifyTargetLabel renders a subject for humans. The username is the identity
// that matters, the title is the context.
func verifyTargetLabel(title, username string) string {
	title = strings.TrimSpace(title)
	username = strings.TrimSpace(username)
	switch {
	case username != "" && title != "":
		return title + " (@" + username + ")"
	case username != "":
		return "@" + username
	case title != "":
		return title
	default:
		return "the selected subject"
	}
}

func verifyTargetButtonText(target domain.VerificationTarget) string {
	label := strings.TrimSpace(target.Username)
	if label != "" {
		label = "@" + label
	} else {
		label = strings.TrimSpace(target.Title)
	}
	if label == "" {
		label = "id " + strconv.FormatInt(target.ID, 10)
	}
	return verifyTargetKindLabel(target.Type) + ": " + verifyTruncate(label, 64)
}

func verifyTargetKindLabel(kind domain.VerificationTargetType) string {
	switch kind {
	case domain.VerificationTargetBot:
		return "Bot"
	case domain.VerificationTargetChannel:
		return "Channel"
	case domain.VerificationTargetSupergroup:
		return "Group"
	case domain.VerificationTargetUser:
		return "Account"
	default:
		return "Subject"
	}
}

func verifyCategoryLabel(category string) string {
	category = strings.TrimSpace(category)
	if label, ok := verifyCategoryLabels[category]; ok {
		return label
	}
	if category == "" {
		return "not chosen yet"
	}
	return category
}

func verifyStatusLabel(status domain.VerificationStatus) string {
	switch status {
	case domain.VerificationStatusDraft:
		return "draft, not filed yet"
	case domain.VerificationStatusSubmitted:
		return "waiting for a reviewer"
	case domain.VerificationStatusInReview:
		return "being reviewed"
	case domain.VerificationStatusApproved:
		return "approved, the badge is live"
	case domain.VerificationStatusRejected:
		return "not approved"
	case domain.VerificationStatusCancelled:
		return "withdrawn"
	default:
		return string(status)
	}
}

func verifyDateLabel(app domain.VerificationApplication) string {
	switch {
	case !app.ReviewedAt.IsZero():
		return "decided " + app.ReviewedAt.UTC().Format("2006-01-02")
	case !app.SubmittedAt.IsZero():
		return "filed " + app.SubmittedAt.UTC().Format("2006-01-02")
	case !app.CreatedAt.IsZero():
		return "started " + app.CreatedAt.UTC().Format("2006-01-02")
	default:
		return ""
	}
}

func verifyJoin(lead, body string) string {
	lead = strings.TrimSpace(lead)
	if lead == "" {
		return body
	}
	return lead + "\n\n" + body
}

// verifySplitLinks accepts links separated by newlines or spaces. A URL cannot
// contain whitespace, so this is lossless and forgiving of how the applicant's
// client wraps the message.
func verifySplitLinks(text string) []string {
	return strings.Fields(text)
}

// verifyCheckLinks validates every link with the domain rule and names the first
// one that fails, so the applicant knows which line to fix.
func verifyCheckLinks(links []string) (botReply, bool) {
	for _, link := range links {
		if err := domain.ValidateVerificationURL(link); err != nil {
			return botReply{Text: verifyURLRejectText + "\n\nI could not accept: " + verifyTruncate(link, 120)}, false
		}
	}
	return botReply{}, true
}

func verifyIsSkipText(text string) bool {
	switch strings.ToLower(strings.TrimSpace(text)) {
	case "skip", "-", "none", "no":
		return true
	default:
		return false
	}
}

func verifyTruncate(value string, max int) string {
	if max <= 0 || utf8.RuneCountInString(value) <= max {
		return value
	}
	runes := []rune(value)
	return string(runes[:max])
}

// ---------------------------------------------------------------------------
// Error mapping
// ---------------------------------------------------------------------------

func (s *Service) verifyErrorReply(userID int64, operation string, err error) botReply {
	if text, ok := verifyPolicyText(err); ok {
		return botReply{Text: text}
	}
	s.log.Error("verifybot: "+operation, zap.Int64("user_id", userID), zap.Error(err))
	return internalReply()
}

// verifyPolicyText turns a policy refusal into a sentence the applicant can act
// on. Anything that is not a policy error is an internal fault and is logged
// instead of being narrated.
func verifyPolicyText(err error) (string, bool) {
	switch {
	case err == nil:
		return "", false
	case errors.Is(err, verificationapp.ErrDisabled):
		return verifyUnavailableText, true
	case errors.Is(err, domain.ErrVerificationTargetAlreadyVerified):
		return "That one is already verified, so there is nothing to apply for.", true
	case errors.Is(err, domain.ErrVerificationTargetNotPublic):
		return "That one has no public @username. Official verification only covers a public presence, so set a username first.", true
	case errors.Is(err, domain.ErrVerificationTargetRestricted):
		return "That one carries a restriction, so it cannot be verified while the restriction stands.", true
	case errors.Is(err, domain.ErrVerificationTargetSystem):
		return "That is a built-in service account and cannot be filed.", true
	case errors.Is(err, domain.ErrVerificationNotOwner):
		return "You are not the creator or an administrator of that one, so you cannot file it.", true
	case errors.Is(err, domain.ErrVerificationApplicationExists):
		return "There is already an active application for that one. Send /status to see it.", true
	case errors.Is(err, domain.ErrVerificationCooldown):
		return "That one was reviewed recently. A rejected application can only be filed again after the cooldown has passed.", true
	case errors.Is(err, domain.ErrVerificationRateLimited):
		return "You have reached the limit on open applications. Finish or cancel one before filing another.", true
	case errors.Is(err, domain.ErrVerificationUserTargetsDisabled):
		return "Plain user accounts are not accepted for verification on this server.", true
	case errors.Is(err, domain.ErrVerificationApplicationNotFound):
		return "I cannot find that application any more. Send /new to start a fresh one.", true
	case errors.Is(err, domain.ErrVerificationStatusInvalid):
		return "That application has already been decided, so it cannot be changed. Send /status to see it.", true
	case errors.Is(err, domain.ErrVerificationVersionConflict):
		return "That application just changed somewhere else. Send /status to see where it stands now.", true
	case errors.Is(err, domain.ErrVerificationURLInvalid):
		return verifyURLRejectText, true
	case errors.Is(err, domain.ErrVerificationApplicationInvalid), errors.Is(err, domain.ErrVerificationTargetInvalid):
		return "I could not accept that. Send /help to see what an application needs.", true
	default:
		return "", false
	}
}

// verifyIneligibleReasons are the domain refusals that can arrive in
// domain.VerificationTarget.Reason. Mapping through the sentinel keeps the
// applicant-facing wording in one place (verifyPolicyText).
var verifyIneligibleReasons = []error{
	domain.ErrVerificationTargetAlreadyVerified,
	domain.ErrVerificationTargetNotPublic,
	domain.ErrVerificationTargetRestricted,
	domain.ErrVerificationTargetSystem,
	domain.ErrVerificationNotOwner,
	domain.ErrVerificationApplicationExists,
	domain.ErrVerificationCooldown,
	domain.ErrVerificationRateLimited,
	domain.ErrVerificationUserTargetsDisabled,
	domain.ErrVerificationTargetInvalid,
	domain.ErrVerificationApplicationInvalid,
}

func verifyIneligibleText(reason string) string {
	reason = strings.TrimSpace(reason)
	for _, sentinel := range verifyIneligibleReasons {
		if sentinel.Error() != reason {
			continue
		}
		if text, ok := verifyPolicyText(sentinel); ok {
			return text
		}
	}
	return "That one cannot be filed right now."
}
