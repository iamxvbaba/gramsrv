package rpc

import (
	"context"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
)

// 本文件集中 poll 的 tg↔domain 转换与 updateMessagePoll 组装。
// 定义快照（MessagePoll）不含机密；机密只进 PollDefinition（polls 权威表）。

// resolvePollAuxMedia 解析 poll 配图（答案图/题干图）：只允许引用或上传 photo/document，
// 其余 InputMedia（含嵌套 poll/todo——会先建孤儿权威行）显式拒绝。
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

// domainPollFromInputMedia 校验 InputMediaPoll 并产出 (渲染快照, 权威定义)。
// pollID 由调用方分配；now 用于 close_period→close_date 折算。
func (r *Router) domainPollFromInputMedia(ctx context.Context, in *tg.InputMediaPoll, creatorUserID, pollID int64, now int) (*domain.MessagePoll, domain.PollDefinition, error) {
	var zero domain.PollDefinition
	poll := in.Poll
	if poll.SubscribersOnly || len(poll.CountriesISO2) > 0 {
		// subscribers-only / 国家限定是新版商业能力，当前无业务模型。
		return nil, zero, mediaInvalidErr()
	}
	// open_answers（开放式答案，允许他人添加选项）在 TDesktop 创建面板默认勾选
	//（kDefaultPollCreateFlags），不能拒；其依赖的 addPollAnswer/deletePollAnswer
	// 未接入，故静默剥离不回 echo——客户端不渲染"添加答案"入口，行为自洽（矩阵 todo）。
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
	// 答案两种形态：pollAnswer 自带 option 键（DrKLO Android 路径）；
	// inputPollAnswer 无 option（TDesktop 创建路径）——option 键由服务端分配。
	seen := make(map[string]struct{}, len(poll.Answers))
	pendingOption := make([]bool, 0, len(poll.Answers))
	for _, answerClass := range poll.Answers {
		var text tg.TextWithEntities
		var option []byte
		var answerMedia *domain.MessageMedia
		switch answer := answerClass.(type) {
		case *tg.PollAnswer:
			if answer.Media != nil {
				// pollAnswer.media 是 MessageMedia（服务端输出形态），不是合法输入。
				return nil, zero, mediaInvalidErr()
			}
			if len(answer.Option) == 0 || len(answer.Option) > maxPollOptionBytes {
				return nil, zero, pollOptionInvalidErr()
			}
			text = answer.Text
			option = append([]byte(nil), answer.Option...)
		case *tg.InputPollAnswer:
			// 答案配图（TDesktop 创建面板可给每个答案附 photo/document）。
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
	// 为 inputPollAnswer 分配服务端 option 键：取未被显式键占用的最小单字节。
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
	// quiz 机密：correct_answers（Layer 224+ 为答案下标）必须恰好 1 个且指向合法选项；
	// solution 仅 quiz 可带。
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
		// quiz 解释配图依赖 polls 权威表扩列（机密侧），当前未接入（矩阵 todo）。
		return nil, zero, mediaInvalidErr()
	}
	if input, ok := in.GetAttachedMedia(); ok && input != nil {
		// poll 题干配图（messageMediaPoll.attached_media），随快照落库。
		media, err := r.resolvePollAuxMedia(ctx, creatorUserID, input)
		if err != nil {
			return nil, zero, err
		}
		snapshot.AttachedMedia = media
	}
	// close_period / close_date 互斥；period 折算出 close_date 供服务端到点判定。
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

// tgPoll 把定义快照转成 tg.Poll（Closed/Results 由读路径 enrichment 决定）。
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

// tgPollResults 输出 viewer 视角结果；未 enrich（Results==nil）时回退 min（客户端保留本地缓存）。
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

// tgUpdateMessagePoll 组装一条 updateMessagePoll（poll 总是内联，避免客户端缓存缺失）。
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

// pollVotesNextOffset 编解码 getPollVotes 翻页 token（"<date>,<user_id>"）。
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
