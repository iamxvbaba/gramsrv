package rpc

import (
	"context"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
)

// This file centralizes the poll tg<->domain conversion and updateMessagePoll
// assembly. The definition snapshot (MessagePoll) carries no secrets; secrets go
// only into PollDefinition (the polls authoritative table).

// resolvePollAuxMedia resolves the poll illustration (answer image / question
// image): only referencing or uploading photo/document is allowed; all other
// InputMedia (including nested poll/todo -- which would first create an orphan
// authoritative row) is explicitly rejected.
func (r *Router) resolvePollAuxMedia(ctx context.Context, userID int64, input tg.InputMediaClass) (*domain.MessageMedia, error) {
	switch input.(type) {
	case *tg.InputMediaPhoto, *tg.InputMediaDocument, *tg.InputMediaUploadedPhoto, *tg.InputMediaUploadedDocument:
	default:
		return nil, mediaInvalidErr()
	}
	media, err := r.resolveInputMedia(ctx, userID, input)
	if err != nil {
		return nil, err
	}
	if media == nil || (media.Kind != domain.MessageMediaKindPhoto && media.Kind != domain.MessageMediaKindDocument) {
		return nil, mediaInvalidErr()
	}
	return media, nil
}

// domainPollFromInputMedia validates InputMediaPoll and produces (render snapshot,
// authoritative definition). pollID is assigned by the caller; now is used for the
// close_period → close_date conversion.
func (r *Router) domainPollFromInputMedia(ctx context.Context, in *tg.InputMediaPoll, creatorUserID, pollID int64, now int) (*domain.MessagePoll, domain.PollDefinition, error) {
	var zero domain.PollDefinition
	poll := in.Poll
	if poll.SubscribersOnly || len(poll.CountriesISO2) > 0 {
		// subscribers-only / country restriction are new commercial capabilities with no business model here yet.
		return nil, zero, mediaInvalidErr()
	}
	// open_answers (open answers, allowing others to add options) is checked by
	// default in the TDesktop creation panel (kDefaultPollCreateFlags) and cannot be
	// rejected; the addPollAnswer/deletePollAnswer it depends on are not wired up, so
	// it is silently stripped and not echoed back -- the client renders no "add
	// answer" entry point, which is self-consistent (matrix todo).
	question := poll.Question.Text
	if strings.TrimSpace(question) == "" {
		return nil, zero, mediaEmptyErr()
	}
	if utf8.RuneCountInString(question) > domain.MaxPollQuestionLength {
		return nil, zero, mediaInvalidErr()
	}
	if len(poll.Question.Entities) > maxMessageEntityCount {
		return nil, zero, limitInvalidErr()
	}
	if len(poll.Answers) < domain.MinPollAnswers {
		return nil, zero, optionInvalidErr()
	}
	if len(poll.Answers) > domain.MaxPollAnswers {
		return nil, zero, optionsTooMuchErr()
	}
	snapshot := &domain.MessagePoll{
		ID:                    pollID,
		Question:              question,
		QuestionEntities:      domainMessageEntities(poll.Question.Entities),
		PublicVoters:          poll.PublicVoters,
		MultipleChoice:        poll.MultipleChoice,
		Quiz:                  poll.Quiz,
		RevotingDisabled:      poll.RevotingDisabled,
		ShuffleAnswers:        poll.ShuffleAnswers,
		HideResultsUntilClose: poll.HideResultsUntilClose,
	}
	def := domain.PollDefinition{
		ID:                    pollID,
		CreatorUserID:         creatorUserID,
		PublicVoters:          poll.PublicVoters,
		MultipleChoice:        poll.MultipleChoice,
		Quiz:                  poll.Quiz,
		RevotingDisabled:      poll.RevotingDisabled,
		HideResultsUntilClose: poll.HideResultsUntilClose,
	}
	// Two answer shapes: pollAnswer carries its own option key (DrKLO Android path);
	// inputPollAnswer has no option (TDesktop creation path) -- the option key is
	// assigned by the server.
	seen := make(map[string]struct{}, len(poll.Answers))
	pendingOption := make([]bool, 0, len(poll.Answers))
	for _, answerClass := range poll.Answers {
		var text tg.TextWithEntities
		var option []byte
		var answerMedia *domain.MessageMedia
		switch answer := answerClass.(type) {
		case *tg.PollAnswer:
			if answer.Media != nil {
				// pollAnswer.media is a MessageMedia (server output shape), not a valid input.
				return nil, zero, mediaInvalidErr()
			}
			if len(answer.Option) == 0 || len(answer.Option) > maxPollOptionBytes {
				return nil, zero, pollOptionInvalidErr()
			}
			text = answer.Text
			option = append([]byte(nil), answer.Option...)
		case *tg.InputPollAnswer:
			// Answer image (the TDesktop creation panel can attach a photo/document to each answer).
			if input, ok := answer.GetMedia(); ok && input != nil {
				media, err := r.resolvePollAuxMedia(ctx, creatorUserID, input)
				if err != nil {
					return nil, zero, err
				}
				answerMedia = media
			}
			text = answer.Text
		default:
			return nil, zero, pollAnswerInvalidErr()
		}
		if strings.TrimSpace(text.Text) == "" || utf8.RuneCountInString(text.Text) > domain.MaxPollAnswerTextLength {
			return nil, zero, pollAnswerInvalidErr()
		}
		if len(text.Entities) > maxMessageEntityCount {
			return nil, zero, limitInvalidErr()
		}
		if option != nil {
			key := string(option)
			if _, dup := seen[key]; dup {
				return nil, zero, pollOptionInvalidErr()
			}
			seen[key] = struct{}{}
		}
		pendingOption = append(pendingOption, option == nil)
		snapshot.Answers = append(snapshot.Answers, domain.MessagePollAnswer{
			Text:     text.Text,
			Entities: domainMessageEntities(text.Entities),
			Option:   option,
			Media:    answerMedia,
		})
	}
	// Assign a server option key for inputPollAnswer: take the smallest single byte not taken by an explicit key.
	next := 0
	for i := range snapshot.Answers {
		if !pendingOption[i] {
			continue
		}
		for next <= 0xFF {
			key := []byte{byte(next)}
			next++
			if _, taken := seen[string(key)]; !taken {
				seen[string(key)] = struct{}{}
				snapshot.Answers[i].Option = key
				break
			}
		}
		if snapshot.Answers[i].Option == nil {
			return nil, zero, pollOptionInvalidErr()
		}
	}
	for _, answer := range snapshot.Answers {
		def.Options = append(def.Options, answer.Option)
	}
	// quiz secrets: correct_answers (an answer index in Layer 224+) must be exactly 1
	// and point at a valid option; solution is only allowed for quiz.
	correct, hasCorrect := in.GetCorrectAnswers()
	solution, hasSolution := in.GetSolution()
	if poll.Quiz {
		if poll.MultipleChoice {
			return nil, zero, mediaInvalidErr()
		}
		if !hasCorrect || len(correct) != 1 {
			return nil, zero, quizCorrectAnswersInvalidErr()
		}
		for _, index := range correct {
			if index < 0 || index >= len(def.Options) {
				return nil, zero, quizCorrectAnswersInvalidErr()
			}
			def.CorrectOptions = append(def.CorrectOptions, append([]byte(nil), def.Options[index]...))
		}
		if hasSolution {
			if strings.TrimSpace(solution) == "" || utf8.RuneCountInString(solution) > domain.MaxPollSolutionLength {
				return nil, zero, mediaInvalidErr()
			}
			entities, _ := in.GetSolutionEntities()
			if len(entities) > maxMessageEntityCount {
				return nil, zero, limitInvalidErr()
			}
			def.Solution = solution
			def.SolutionEntities = domainMessageEntities(entities)
		}
	} else if hasCorrect || hasSolution {
		return nil, zero, mediaInvalidErr()
	}
	if _, ok := in.GetSolutionMedia(); ok {
		// The quiz explanation image depends on extending the polls authoritative table (the secret side) and is not wired up yet (matrix todo).
		return nil, zero, mediaInvalidErr()
	}
	if input, ok := in.GetAttachedMedia(); ok && input != nil {
		// Poll question image (messageMediaPoll.attached_media), persisted with the snapshot.
		media, err := r.resolvePollAuxMedia(ctx, creatorUserID, input)
		if err != nil {
			return nil, zero, err
		}
		snapshot.AttachedMedia = media
	}
	// close_period / close_date are mutually exclusive; period is converted to close_date for server-side expiry.
	closePeriod, hasPeriod := poll.GetClosePeriod()
	closeDate, hasDate := poll.GetCloseDate()
	if hasPeriod && hasDate {
		return nil, zero, mediaInvalidErr()
	}
	if hasPeriod {
		if closePeriod < domain.MinPollClosePeriod || closePeriod > domain.MaxPollClosePeriod {
			return nil, zero, mediaInvalidErr()
		}
		snapshot.ClosePeriod = closePeriod
		snapshot.CloseDate = now + closePeriod
		def.ClosePeriod = closePeriod
		def.CloseDate = now + closePeriod
	} else if hasDate {
		if closeDate <= now || closeDate > now+domain.MaxPollClosePeriod+60 {
			return nil, zero, mediaInvalidErr()
		}
		snapshot.CloseDate = closeDate
		def.CloseDate = closeDate
	}
	return snapshot, def, nil
}

// tgPoll converts the definition snapshot into tg.Poll (Closed/Results are decided by read-path enrichment).
func tgPoll(p domain.MessagePoll) tg.Poll {
	out := tg.Poll{
		ID:                    p.ID,
		Closed:                p.Closed,
		PublicVoters:          p.PublicVoters,
		MultipleChoice:        p.MultipleChoice,
		Quiz:                  p.Quiz,
		RevotingDisabled:      p.RevotingDisabled,
		ShuffleAnswers:        p.ShuffleAnswers,
		HideResultsUntilClose: p.HideResultsUntilClose,
		Question:              tgTextWithEntities(p.Question, p.QuestionEntities),
		Answers:               make([]tg.PollAnswerClass, 0, len(p.Answers)),
	}
	for _, answer := range p.Answers {
		item := &tg.PollAnswer{
			Text:   tgTextWithEntities(answer.Text, answer.Entities),
			Option: answer.Option,
		}
		if answer.Media != nil {
			item.SetMedia(tgMessageMedia(answer.Media))
		}
		out.Answers = append(out.Answers, item)
	}
	if p.ClosePeriod > 0 {
		out.SetClosePeriod(p.ClosePeriod)
	}
	if p.CloseDate > 0 {
		out.SetCloseDate(p.CloseDate)
	}
	return out
}

// tgPollResults emits the viewer's results; when not enriched (Results==nil) it falls back to min (the client keeps its local cache).
func tgPollResults(p domain.MessagePoll) tg.PollResults {
	out := tg.PollResults{}
	results := p.Results
	if results == nil {
		out.Min = true
		return out
	}
	voters := make([]tg.PollAnswerVoters, 0, len(results.Voters))
	for _, item := range results.Voters {
		entry := tg.PollAnswerVoters{Chosen: item.Chosen, Correct: item.Correct, Option: item.Option}
		entry.SetVoters(item.Voters)
		voters = append(voters, entry)
	}
	out.SetResults(voters)
	out.SetTotalVoters(results.TotalVoters)
	if len(results.RecentVoters) > 0 {
		peers := make([]tg.PeerClass, 0, len(results.RecentVoters))
		for _, userID := range results.RecentVoters {
			peers = append(peers, &tg.PeerUser{UserID: userID})
		}
		out.SetRecentVoters(peers)
	}
	if results.Solution != "" {
		out.SetSolution(results.Solution)
		out.SetSolutionEntities(tgMessageEntities(results.SolutionEntities))
	}
	return out
}

func tgTextWithEntities(text string, entities []domain.MessageEntity) tg.TextWithEntities {
	converted := tgMessageEntities(entities)
	if converted == nil {
		converted = []tg.MessageEntityClass{}
	}
	return tg.TextWithEntities{Text: text, Entities: converted}
}

// tgUpdateMessagePoll assembles an updateMessagePoll (the poll is always inlined to avoid a client cache miss).
func tgUpdateMessagePoll(peer domain.Peer, msgID int, poll *domain.MessagePoll) *tg.UpdateMessagePoll {
	if poll == nil {
		return nil
	}
	update := &tg.UpdateMessagePoll{
		PollID:  poll.ID,
		Results: tgPollResults(*poll),
	}
	update.SetPoll(tgPoll(*poll))
	if tgp := tgPeer(peer); tgp != nil && msgID > 0 {
		update.SetPeer(tgp)
		update.SetMsgID(msgID)
	}
	return update
}

// pollVotesNextOffset encodes/decodes the getPollVotes pagination token ("<date>,<user_id>").
func pollVotesNextOffset(votes []domain.PollVote) string {
	if len(votes) == 0 {
		return ""
	}
	last := votes[len(votes)-1]
	return strconv.Itoa(last.Date) + "," + strconv.FormatInt(last.UserID, 10)
}

func decodePollVotesOffset(offset string) (date int, userID int64, ok bool) {
	if offset == "" {
		return 0, 0, true
	}
	parts := strings.SplitN(offset, ",", 2)
	if len(parts) != 2 {
		return 0, 0, false
	}
	d, err1 := strconv.Atoi(parts[0])
	u, err2 := strconv.ParseInt(parts[1], 10, 64)
	if err1 != nil || err2 != nil || d < 0 || u < 0 {
		return 0, 0, false
	}
	return d, u, true
}
