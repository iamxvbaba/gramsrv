package rpc

import (
	"context"
	"testing"

	"github.com/iamxvbaba/td/tg"
)

// Regression: the real request shape of TDesktop creating a poll
// (kDefaultPollCreateFlags carries
// public_voters/multiple_choice/open_answers/shuffle_answers by default, and
// answers are inputPollAnswer with no option -- option keys are assigned by the
// server). Before the fix, open_answers was rejected outright with MEDIA_INVALID
// and inputPollAnswer with POLL_ANSWER_INVALID.
func TestSendMediaPollTDesktopShape(t *testing.T) {
	ctx := context.Background()
	r, owner, friend := newMediaTestRouter(t)
	// The first answer carries media (TDesktop's create panel can attach
	// photo/document to an answer in practice), referencing the document 555
	// preset by newMediaTestRouter.
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
	// Answer media must be echoed back with the snapshot.
	firstAnswer := poll.Answers[0].(*tg.PollAnswer)
	answerMedia, hasAnswerMedia := firstAnswer.GetMedia()
	if !hasAnswerMedia {
		t.Fatal("first answer media lost")
	}
	docMedia, ok := answerMedia.(*tg.MessageMediaDocument)
	if !ok {
		t.Fatalf("answer media = %T, want MessageMediaDocument", answerMedia)
	}
	if doc, ok := docMedia.Document.(*tg.Document); !ok || doc.ID != 555 {
		t.Fatalf("answer media document = %#v, want id 555", docMedia.Document)
	}

	// The server-assigned option must be directly usable for voting.
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

// Regression: a quiz likewise goes through inputPollAnswer + index-based
// correct_answers, and the index semantics must hold after the server assigns options.
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
