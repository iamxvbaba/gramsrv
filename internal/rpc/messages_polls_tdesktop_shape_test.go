package rpc

import (
	"context"
	"testing"

	"github.com/gotd/td/tg"
)

// 回归：TDesktop 创建 poll 的真实请求形状（kDefaultPollCreateFlags 默认带
// public_voters/multiple_choice/open_answers/shuffle_answers，答案为无 option 的
// inputPollAnswer——option 键由服务端分配）。修复前 open_answers 被一刀切
// MEDIA_INVALID，inputPollAnswer 被 POLL_ANSWER_INVALID。
func TestSendMediaPollTDesktopShape(t *testing.T) {
	ctx := context.Background()
	r, owner, friend := newMediaTestRouter(t)
	// 第一个答案带配图（实测 TDesktop 创建面板可给答案附 photo/document），
	// 引用 newMediaTestRouter 预置的 document 555。
	withMedia := &tg.InputPollAnswer{Text: tg.TextWithEntities{Text: "r", Entities: []tg.MessageEntityClass{}}}
	withMedia.SetMedia(&tg.InputMediaDocument{ID: &tg.InputDocument{ID: 555, AccessHash: 5}})
	media := &tg.InputMediaPoll{
		Poll: tg.Poll{
			ID:             0,
			PublicVoters:   true,
			MultipleChoice: true,
			OpenAnswers:    true,
			ShuffleAnswers: true,
			Question:       tg.TextWithEntities{Text: "color?", Entities: []tg.MessageEntityClass{}},
			Answers: []tg.PollAnswerClass{
				withMedia,
				&tg.InputPollAnswer{Text: tg.TextWithEntities{Text: "blue", Entities: []tg.MessageEntityClass{}}},
				&tg.InputPollAnswer{Text: tg.TextWithEntities{Text: "green", Entities: []tg.MessageEntityClass{}}},
			},
		},
	}
	updates, err := r.onMessagesSendMedia(WithUserID(ctx, owner.ID), &tg.MessagesSendMediaRequest{
		Peer:     &tg.InputPeerUser{UserID: friend.ID, AccessHash: friend.AccessHash},
		Media:    media,
		RandomID: 9001,
	})
	if err != nil {
		t.Fatalf("sendMedia TDesktop poll shape: %v", err)
	}
	msg := newMessageFromUpdates(t, updates)
	mediaPoll, ok := msg.Media.(*tg.MessageMediaPoll)
	if !ok {
		t.Fatalf("expected MessageMediaPoll, got %T", msg.Media)
	}
	poll := mediaPoll.Poll
	if !poll.PublicVoters || !poll.MultipleChoice || !poll.ShuffleAnswers {
		t.Fatalf("create flags lost: %+v", poll)
	}
	if poll.OpenAnswers {
		t.Fatal("open_answers must be stripped (addPollAnswer not wired, no add-answer UI entry)")
	}
	if len(poll.Answers) != 3 {
		t.Fatalf("answers = %d, want 3", len(poll.Answers))
	}
	seen := map[string]struct{}{}
	for i, answerClass := range poll.Answers {
		answer, ok := answerClass.(*tg.PollAnswer)
		if !ok {
			t.Fatalf("echoed answer %d type = %T, want pollAnswer", i, answerClass)
		}
		if len(answer.Option) == 0 {
			t.Fatalf("answer %d option not assigned server-side", i)
		}
		if _, dup := seen[string(answer.Option)]; dup {
			t.Fatalf("answer %d option duplicated", i)
		}
		seen[string(answer.Option)] = struct{}{}
	}
	// 答案配图必须随快照回 echo。
	firstAnswer := poll.Answers[0].(*tg.PollAnswer)
	answerMedia, hasAnswerMedia := firstAnswer.GetMedia()
	if !hasAnswerMedia {
		t.Fatal("first answer media lost")
	}
	docMedia, ok := answerMedia.(*tg.MessageMediaDocument)
	if !ok {
		t.Fatalf("answer media = %T, want MessageMediaDocument", answerMedia)
	}
	if doc, ok := docMedia.Document.(*tg.Document); !ok || doc.ID != clientDocumentIDFromServerID(555) {
		t.Fatalf("answer media document = %#v, want id %d", docMedia.Document, clientDocumentIDFromServerID(555))
	}

	// 服务端分配的 option 必须能直接用于投票。
	firstOption := poll.Answers[0].(*tg.PollAnswer).Option
	voteUpdates, err := r.onMessagesSendVote(WithUserID(ctx, friend.ID), &tg.MessagesSendVoteRequest{
		Peer:    &tg.InputPeerUser{UserID: owner.ID, AccessHash: owner.AccessHash},
		MsgID:   msg.ID,
		Options: [][]byte{firstOption},
	})
	if err != nil {
		t.Fatalf("vote with server-assigned option: %v", err)
	}
	_, results := pollFromUpdates(t, voteUpdates)
	if total, _ := results.GetTotalVoters(); total != 1 {
		t.Fatalf("total voters = %d, want 1", total)
	}
}

// 回归：quiz 同样走 inputPollAnswer + 下标 correct_answers，服务端分配 option 后下标语义必须成立。
func TestSendMediaQuizTDesktopShape(t *testing.T) {
	ctx := context.Background()
	r, owner, friend := newMediaTestRouter(t)
	media := &tg.InputMediaPoll{
		Poll: tg.Poll{
			Quiz:     true,
			Question: tg.TextWithEntities{Text: "2+2?", Entities: []tg.MessageEntityClass{}},
			Answers: []tg.PollAnswerClass{
				&tg.InputPollAnswer{Text: tg.TextWithEntities{Text: "3", Entities: []tg.MessageEntityClass{}}},
				&tg.InputPollAnswer{Text: tg.TextWithEntities{Text: "4", Entities: []tg.MessageEntityClass{}}},
			},
		},
	}
	media.SetCorrectAnswers([]int{1})
	updates, err := r.onMessagesSendMedia(WithUserID(ctx, owner.ID), &tg.MessagesSendMediaRequest{
		Peer:     &tg.InputPeerUser{UserID: friend.ID, AccessHash: friend.AccessHash},
		Media:    media,
		RandomID: 9002,
	})
	if err != nil {
		t.Fatalf("sendMedia quiz shape: %v", err)
	}
	msg := newMessageFromUpdates(t, updates)
	poll := msg.Media.(*tg.MessageMediaPoll).Poll
	correctOption := poll.Answers[1].(*tg.PollAnswer).Option

	voteUpdates, err := r.onMessagesSendVote(WithUserID(ctx, friend.ID), &tg.MessagesSendVoteRequest{
		Peer:    &tg.InputPeerUser{UserID: owner.ID, AccessHash: owner.AccessHash},
		MsgID:   msg.ID,
		Options: [][]byte{correctOption},
	})
	if err != nil {
		t.Fatalf("quiz vote: %v", err)
	}
	_, results := pollFromUpdates(t, voteUpdates)
	byOption := votersByOption(t, results)
	if v := byOption[correctOption[0]]; !v.Chosen || !v.Correct {
		t.Fatalf("correct answer entry = %+v, want chosen+correct", v)
	}
}
