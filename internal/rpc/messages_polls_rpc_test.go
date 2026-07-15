package rpc

import (
	"context"
	"testing"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
)

// sendTestPoll 用 sendMedia 发一个 poll，返回消息与 poll id。
func sendTestPoll(t *testing.T, r *Router, fromID int64, peer tg.InputPeerClass, randomID int64, mutate func(*tg.InputMediaPoll)) *tg.Message {
	t.Helper()
	media := &tg.InputMediaPoll{
		Poll: tg.Poll{
			Question: tg.TextWithEntities{Text: "favorite color?", Entities: []tg.MessageEntityClass{}},
			Answers: []tg.PollAnswerClass{
				&tg.PollAnswer{Text: tg.TextWithEntities{Text: "red", Entities: []tg.MessageEntityClass{}}, Option: []byte{0}},
				&tg.PollAnswer{Text: tg.TextWithEntities{Text: "blue", Entities: []tg.MessageEntityClass{}}, Option: []byte{1}},
				&tg.PollAnswer{Text: tg.TextWithEntities{Text: "green", Entities: []tg.MessageEntityClass{}}, Option: []byte{2}},
			},
		},
	}
	if mutate != nil {
		mutate(media)
	}
	updates, err := r.onMessagesSendMedia(WithUserID(context.Background(), fromID), &tg.MessagesSendMediaRequest{
		Peer:     peer,
		Media:    media,
		RandomID: randomID,
	})
	if err != nil {
		t.Fatalf("sendMedia poll: %v", err)
	}
	return newMessageFromUpdates(t, updates)
}

func pollFromUpdates(t *testing.T, updates tg.UpdatesClass) (*tg.Poll, *tg.PollResults) {
	t.Helper()
	upd, ok := updates.(*tg.Updates)
	if !ok {
		t.Fatalf("expected *tg.Updates, got %T", updates)
	}
	for _, u := range upd.Updates {
		if mp, ok := u.(*tg.UpdateMessagePoll); ok {
			poll, _ := mp.GetPoll()
			return &poll, &mp.Results
		}
	}
	t.Fatalf("no updateMessagePoll in %#v", upd.Updates)
	return nil, nil
}

func votersByOption(t *testing.T, results *tg.PollResults) map[byte]tg.PollAnswerVoters {
	t.Helper()
	out := map[byte]tg.PollAnswerVoters{}
	list, _ := results.GetResults()
	for _, item := range list {
		if len(item.Option) != 1 {
			t.Fatalf("unexpected option key %v", item.Option)
		}
		out[item.Option[0]] = item
	}
	return out
}

func TestSendMediaPollEcho(t *testing.T) {
	ctx := context.Background()
	_ = ctx
	r, owner, friend := newMediaTestRouter(t)

	msg := sendTestPoll(t, r, owner.ID, &tg.InputPeerUser{UserID: friend.ID, AccessHash: friend.AccessHash}, 4001, nil)
	media, ok := msg.Media.(*tg.MessageMediaPoll)
	if !ok {
		t.Fatalf("expected MessageMediaPoll, got %T", msg.Media)
	}
	if media.Poll.ID == 0 {
		t.Error("poll id should be allocated server-side")
	}
	if media.Poll.Question.Text != "favorite color?" || len(media.Poll.Answers) != 3 {
		t.Fatalf("poll definition mangled: %+v", media.Poll)
	}
	if media.Poll.Closed {
		t.Error("fresh poll should be open")
	}
}

func TestSendVotePrivateChooseChangeRetract(t *testing.T) {
	ctx := context.Background()
	r, owner, friend := newMediaTestRouter(t)
	peerForFriend := &tg.InputPeerUser{UserID: owner.ID, AccessHash: owner.AccessHash}

	msg := sendTestPoll(t, r, owner.ID, &tg.InputPeerUser{UserID: friend.ID, AccessHash: friend.AccessHash}, 4002, nil)
	pollID := msg.Media.(*tg.MessageMediaPoll).Poll.ID

	// friend 的 box id 与 owner 的相同（同一会话双方各自 box 计数都从 1 开始且只有这一条）。
	friendCtx := WithUserID(ctx, friend.ID)
	updates, err := r.onMessagesSendVote(friendCtx, &tg.MessagesSendVoteRequest{
		Peer:    peerForFriend,
		MsgID:   msg.ID,
		Options: [][]byte{{1}},
	})
	if err != nil {
		t.Fatalf("sendVote: %v", err)
	}
	poll, results := pollFromUpdates(t, updates)
	if poll.ID != pollID {
		t.Fatalf("update poll id = %d, want %d", poll.ID, pollID)
	}
	byOption := votersByOption(t, results)
	if total, _ := results.GetTotalVoters(); total != 1 {
		t.Fatalf("total voters = %d, want 1", total)
	}
	if v := byOption[1]; !v.Chosen || mustVoters(t, v) != 1 {
		t.Fatalf("option 1 = %+v, want chosen with 1 voter", v)
	}
	if v := byOption[0]; v.Chosen || mustVoters(t, v) != 0 {
		t.Fatalf("option 0 = %+v, want not chosen 0 voters", v)
	}

	// 改票：单选 poll 重投另一项。
	updates, err = r.onMessagesSendVote(friendCtx, &tg.MessagesSendVoteRequest{
		Peer:    peerForFriend,
		MsgID:   msg.ID,
		Options: [][]byte{{2}},
	})
	if err != nil {
		t.Fatalf("sendVote change: %v", err)
	}
	_, results = pollFromUpdates(t, updates)
	byOption = votersByOption(t, results)
	if v := byOption[2]; !v.Chosen || mustVoters(t, v) != 1 {
		t.Fatalf("after change option 2 = %+v, want chosen 1", v)
	}
	if v := byOption[1]; v.Chosen || mustVoters(t, v) != 0 {
		t.Fatalf("after change option 1 = %+v, want cleared", v)
	}

	// 撤票：空 options。
	updates, err = r.onMessagesSendVote(friendCtx, &tg.MessagesSendVoteRequest{
		Peer:  peerForFriend,
		MsgID: msg.ID,
	})
	if err != nil {
		t.Fatalf("sendVote retract: %v", err)
	}
	_, results = pollFromUpdates(t, updates)
	if total, _ := results.GetTotalVoters(); total != 0 {
		t.Fatalf("total after retract = %d, want 0", total)
	}

	// 非法选项。
	if _, err := r.onMessagesSendVote(friendCtx, &tg.MessagesSendVoteRequest{
		Peer:    peerForFriend,
		MsgID:   msg.ID,
		Options: [][]byte{{9}},
	}); err == nil || !tgerr.Is(err, "OPTION_INVALID") {
		t.Fatalf("invalid option err = %v, want OPTION_INVALID", err)
	}
	// 单选投两项。
	if _, err := r.onMessagesSendVote(friendCtx, &tg.MessagesSendVoteRequest{
		Peer:    peerForFriend,
		MsgID:   msg.ID,
		Options: [][]byte{{0}, {1}},
	}); err == nil || !tgerr.Is(err, "OPTION_INVALID") {
		t.Fatalf("multi options on single choice err = %v, want OPTION_INVALID", err)
	}
}

func mustVoters(t *testing.T, v tg.PollAnswerVoters) int {
	t.Helper()
	count, _ := v.GetVoters()
	return count
}

func TestSendVoteQuizRevealsAndLocks(t *testing.T) {
	ctx := context.Background()
	r, owner, friend := newMediaTestRouter(t)
	peerForFriend := &tg.InputPeerUser{UserID: owner.ID, AccessHash: owner.AccessHash}

	msg := sendTestPoll(t, r, owner.ID, &tg.InputPeerUser{UserID: friend.ID, AccessHash: friend.AccessHash}, 4003, func(in *tg.InputMediaPoll) {
		in.Poll.Quiz = true
		in.SetCorrectAnswers([]int{1})
		in.SetSolution("blue is correct")
		in.SetSolutionEntities([]tg.MessageEntityClass{})
	})
	friendCtx := WithUserID(ctx, friend.ID)

	// 投票前（owner 自己查结果）不得泄漏 correct/solution。
	ownerCtx := WithUserID(ctx, owner.ID)
	preUpdates, err := r.onMessagesGetPollResults(ownerCtx, &tg.MessagesGetPollResultsRequest{
		Peer:  &tg.InputPeerUser{UserID: friend.ID, AccessHash: friend.AccessHash},
		MsgID: msg.ID,
	})
	if err != nil {
		t.Fatalf("getPollResults pre-vote: %v", err)
	}
	_, preResults := pollFromUpdates(t, preUpdates)
	if _, has := preResults.GetSolution(); has {
		t.Fatal("solution must not leak before voting/closing")
	}
	for _, v := range votersByOption(t, preResults) {
		if v.Correct {
			t.Fatal("correct flag must not leak before voting/closing")
		}
	}

	// friend 答错：correct 标在正确选项上，solution 揭示。
	updates, err := r.onMessagesSendVote(friendCtx, &tg.MessagesSendVoteRequest{
		Peer:    peerForFriend,
		MsgID:   msg.ID,
		Options: [][]byte{{0}},
	})
	if err != nil {
		t.Fatalf("quiz vote: %v", err)
	}
	_, results := pollFromUpdates(t, updates)
	byOption := votersByOption(t, results)
	if !byOption[0].Chosen || byOption[0].Correct {
		t.Fatalf("wrong answer entry = %+v, want chosen non-correct", byOption[0])
	}
	if !byOption[1].Correct {
		t.Fatalf("correct answer entry = %+v, want correct flag", byOption[1])
	}
	if solution, has := results.GetSolution(); !has || solution != "blue is correct" {
		t.Fatalf("solution = %q (%v), want revealed after voting", solution, has)
	}

	// quiz 不可改票/撤票。
	if _, err := r.onMessagesSendVote(friendCtx, &tg.MessagesSendVoteRequest{
		Peer:    peerForFriend,
		MsgID:   msg.ID,
		Options: [][]byte{{1}},
	}); err == nil || !tgerr.Is(err, "REVOTE_NOT_ALLOWED") {
		t.Fatalf("quiz revote err = %v, want REVOTE_NOT_ALLOWED", err)
	}
	if _, err := r.onMessagesSendVote(friendCtx, &tg.MessagesSendVoteRequest{
		Peer:  peerForFriend,
		MsgID: msg.ID,
	}); err == nil || !tgerr.Is(err, "REVOTE_NOT_ALLOWED") {
		t.Fatalf("quiz retract err = %v, want REVOTE_NOT_ALLOWED", err)
	}
}

func TestEditMessageClosePoll(t *testing.T) {
	ctx := context.Background()
	r, owner, friend := newMediaTestRouter(t)
	ownerPeer := &tg.InputPeerUser{UserID: friend.ID, AccessHash: friend.AccessHash}
	friendPeer := &tg.InputPeerUser{UserID: owner.ID, AccessHash: owner.AccessHash}

	msg := sendTestPoll(t, r, owner.ID, ownerPeer, 4004, nil)
	pollID := msg.Media.(*tg.MessageMediaPoll).Poll.ID
	ownerCtx := WithUserID(ctx, owner.ID)
	friendCtx := WithUserID(ctx, friend.ID)

	closeReq := func() *tg.MessagesEditMessageRequest {
		req := &tg.MessagesEditMessageRequest{Peer: ownerPeer, ID: msg.ID}
		closed := tg.Poll{ID: pollID, Closed: true}
		req.SetMedia(&tg.InputMediaPoll{Poll: closed})
		return req
	}

	// 非创建者不能关闭。
	friendClose := &tg.MessagesEditMessageRequest{Peer: friendPeer, ID: msg.ID}
	friendClose.SetMedia(&tg.InputMediaPoll{Poll: tg.Poll{ID: pollID, Closed: true}})
	if _, err := r.onMessagesEditMessage(friendCtx, friendClose); err == nil || !tgerr.Is(err, "MESSAGE_AUTHOR_REQUIRED") {
		t.Fatalf("non-creator close err = %v, want MESSAGE_AUTHOR_REQUIRED", err)
	}

	updates, err := r.onMessagesEditMessage(ownerCtx, closeReq())
	if err != nil {
		t.Fatalf("close poll: %v", err)
	}
	poll, _ := pollFromUpdates(t, updates)
	if !poll.Closed {
		t.Fatal("poll should be closed after edit")
	}

	// 关闭后投票必须被拒。
	if _, err := r.onMessagesSendVote(friendCtx, &tg.MessagesSendVoteRequest{
		Peer:    friendPeer,
		MsgID:   msg.ID,
		Options: [][]byte{{0}},
	}); err == nil || !tgerr.Is(err, "MESSAGE_POLL_CLOSED") {
		t.Fatalf("vote on closed poll err = %v, want MESSAGE_POLL_CLOSED", err)
	}

	// 对端视角读取也必须看到 closed（权威覆盖快照）。
	res, err := r.onMessagesGetPollResults(friendCtx, &tg.MessagesGetPollResultsRequest{Peer: friendPeer, MsgID: msg.ID})
	if err != nil {
		t.Fatalf("getPollResults after close: %v", err)
	}
	peerPoll, _ := pollFromUpdates(t, res)
	if !peerPoll.Closed {
		t.Fatal("peer view should see closed poll (authority overrides snapshot)")
	}
}

func TestGetPollVotesPublicOnly(t *testing.T) {
	ctx := context.Background()
	r, owner, friend := newMediaTestRouter(t)
	ownerPeer := &tg.InputPeerUser{UserID: friend.ID, AccessHash: friend.AccessHash}
	friendPeer := &tg.InputPeerUser{UserID: owner.ID, AccessHash: owner.AccessHash}
	ownerCtx := WithUserID(ctx, owner.ID)
	friendCtx := WithUserID(ctx, friend.ID)

	// 非公开 poll：getPollVotes 拒绝。
	anonymous := sendTestPoll(t, r, owner.ID, ownerPeer, 4005, nil)
	if _, err := r.onMessagesGetPollVotes(ownerCtx, &tg.MessagesGetPollVotesRequest{
		Peer: ownerPeer, ID: anonymous.ID, Limit: 10,
	}); err == nil || !tgerr.Is(err, "POLL_VOTE_REQUIRED") {
		t.Fatalf("anonymous poll votes err = %v, want POLL_VOTE_REQUIRED", err)
	}

	public := sendTestPoll(t, r, owner.ID, ownerPeer, 4006, func(in *tg.InputMediaPoll) {
		in.Poll.PublicVoters = true
	})
	if _, err := r.onMessagesSendVote(friendCtx, &tg.MessagesSendVoteRequest{
		Peer: friendPeer, MsgID: public.ID, Options: [][]byte{{1}},
	}); err != nil {
		t.Fatalf("public vote: %v", err)
	}
	votes, err := r.onMessagesGetPollVotes(ownerCtx, &tg.MessagesGetPollVotesRequest{
		Peer: ownerPeer, ID: public.ID, Limit: 10,
	})
	if err != nil {
		t.Fatalf("getPollVotes: %v", err)
	}
	if votes.Count != 1 || len(votes.Votes) != 1 {
		t.Fatalf("votes = %+v, want exactly friend's vote", votes)
	}
	single, ok := votes.Votes[0].(*tg.MessagePeerVote)
	if !ok {
		t.Fatalf("vote entry type = %T, want MessagePeerVote", votes.Votes[0])
	}
	if peerUser, ok := single.Peer.(*tg.PeerUser); !ok || peerUser.UserID != friend.ID {
		t.Fatalf("vote peer = %#v, want friend", single.Peer)
	}
	if len(single.Option) != 1 || single.Option[0] != 1 {
		t.Fatalf("vote option = %v, want [1]", single.Option)
	}
	foundFriend := false
	for _, u := range votes.Users {
		if got, ok := u.(*tg.User); ok && got.ID == friend.ID {
			foundFriend = true
		}
	}
	if !foundFriend {
		t.Fatal("votes.users should contain the voter entity")
	}
}

func TestSendMediaPollValidation(t *testing.T) {
	ctx := context.Background()
	r, owner, friend := newMediaTestRouter(t)
	peer := &tg.InputPeerUser{UserID: friend.ID, AccessHash: friend.AccessHash}
	ownerCtx := WithUserID(ctx, owner.ID)

	send := func(mutate func(*tg.InputMediaPoll)) error {
		media := &tg.InputMediaPoll{
			Poll: tg.Poll{
				Question: tg.TextWithEntities{Text: "q", Entities: []tg.MessageEntityClass{}},
				Answers: []tg.PollAnswerClass{
					&tg.PollAnswer{Text: tg.TextWithEntities{Text: "a", Entities: []tg.MessageEntityClass{}}, Option: []byte{0}},
					&tg.PollAnswer{Text: tg.TextWithEntities{Text: "b", Entities: []tg.MessageEntityClass{}}, Option: []byte{1}},
				},
			},
		}
		if mutate != nil {
			mutate(media)
		}
		_, err := r.onMessagesSendMedia(ownerCtx, &tg.MessagesSendMediaRequest{Peer: peer, Media: media, RandomID: int64(5000 + len(media.Poll.Answers))})
		return err
	}

	if err := send(func(in *tg.InputMediaPoll) {
		in.Poll.Answers = in.Poll.Answers[:1]
	}); err == nil || !tgerr.Is(err, "OPTION_INVALID") {
		t.Fatalf("single answer err = %v, want OPTION_INVALID", err)
	}
	if err := send(func(in *tg.InputMediaPoll) {
		in.Poll.Question.Text = "   "
	}); err == nil || !tgerr.Is(err, "MEDIA_EMPTY") {
		t.Fatalf("empty question err = %v, want MEDIA_EMPTY", err)
	}
	if err := send(func(in *tg.InputMediaPoll) {
		in.Poll.Answers[1].(*tg.PollAnswer).Option = []byte{0}
	}); err == nil || !tgerr.Is(err, "POLL_OPTION_INVALID") {
		t.Fatalf("duplicate option err = %v, want POLL_OPTION_INVALID", err)
	}
	if err := send(func(in *tg.InputMediaPoll) {
		in.Poll.Quiz = true
	}); err == nil || !tgerr.Is(err, "QUIZ_CORRECT_ANSWERS_INVALID") {
		t.Fatalf("quiz without correct err = %v, want QUIZ_CORRECT_ANSWERS_INVALID", err)
	}
	if err := send(func(in *tg.InputMediaPoll) {
		in.Poll.Quiz = true
		in.SetCorrectAnswers([]int{5})
	}); err == nil || !tgerr.Is(err, "QUIZ_CORRECT_ANSWERS_INVALID") {
		t.Fatalf("quiz bad index err = %v, want QUIZ_CORRECT_ANSWERS_INVALID", err)
	}
	if err := send(func(in *tg.InputMediaPoll) {
		in.SetCorrectAnswers([]int{0})
	}); err == nil || !tgerr.Is(err, "MEDIA_INVALID") {
		t.Fatalf("non-quiz with correct err = %v, want MEDIA_INVALID", err)
	}
}
