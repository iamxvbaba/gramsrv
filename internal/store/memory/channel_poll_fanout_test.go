package memory

import (
	"context"
	"reflect"
	"testing"

	"telesrv/internal/domain"
)

// TestChannelPollFanoutViewsByteEquivalentToGetMessages is the safety net locking the
// Phase 4 poll templating: the enriched poll batch ChannelPollFanoutViews produces for each
// viewer must be byte-equivalent to per-viewer GetChannelMessages (the old fan-out path),
// and visibility decisions must match (an invisible viewer gets nil in the batch and
// cannot fetch the message on the old path). Covers: author (creator), voted non-author,
// unvoted non-author, non-creator during hide_results, late-joiner (AvailableMinID),
// banned member, ViewMessages-restricted member, non-member.
func TestChannelPollFanoutViewsByteEquivalentToGetMessages(t *testing.T) {
	ctx := context.Background()
	const (
		channelID  = int64(8000000001)
		msgID      = 50
		pollID     = int64(9000000001)
		creator    = int64(7001)
		votedA     = int64(7002)
		votedB     = int64(7003)
		unvoted    = int64(7004)
		late       = int64(7005) // AvailableMinID >= msgID (pre-history hidden)
		banned     = int64(7006)
		restricted = int64(7007) // active but BannedRights.ViewMessages
		nonMember  = int64(7008)
	)
	opt1, opt2, opt3 := []byte{1}, []byte{2}, []byte{3}

	for _, hideResults := range []bool{false, true} {
		t.Run(map[bool]string{false: "visible_counts", true: "hide_results"}[hideResults], func(t *testing.T) {
			channels := NewChannelStore()
			polls := NewPollStore()
			channels.AttachPollStore(polls)

			channels.channels[channelID] = domain.Channel{ID: channelID, CreatorUserID: creator, Megagroup: true, PreHistoryHidden: true}
			channels.members[channelID] = map[int64]domain.ChannelMember{
				creator:    {UserID: creator, Status: domain.ChannelMemberActive, Role: domain.ChannelRoleCreator},
				votedA:     {UserID: votedA, Status: domain.ChannelMemberActive},
				votedB:     {UserID: votedB, Status: domain.ChannelMemberActive},
				unvoted:    {UserID: unvoted, Status: domain.ChannelMemberActive},
				late:       {UserID: late, Status: domain.ChannelMemberActive, AvailableMinID: msgID + 10},
				banned:     {UserID: banned, Status: domain.ChannelMemberBanned},
				restricted: {UserID: restricted, Status: domain.ChannelMemberActive, BannedRights: domain.ChannelBannedRights{ViewMessages: true}},
			}

			def := domain.PollDefinition{
				ID:                    pollID,
				CreatorUserID:         creator,
				Options:               [][]byte{opt1, opt2, opt3},
				PublicVoters:          true,
				MultipleChoice:        false,
				HideResultsUntilClose: hideResults,
			}
			if err := polls.CreatePoll(ctx, def); err != nil {
				t.Fatalf("create poll: %v", err)
			}
			basePoll := &domain.MessagePoll{
				ID:                    pollID,
				Question:              "Q?",
				Answers:               []domain.MessagePollAnswer{{Text: "A", Option: opt1}, {Text: "B", Option: opt2}, {Text: "C", Option: opt3}},
				PublicVoters:          true,
				HideResultsUntilClose: hideResults,
			}
			channels.messages[channelID] = []domain.ChannelMessage{{
				ChannelID:    channelID,
				ID:           msgID,
				SenderUserID: creator,
				Media:        &domain.MessageMedia{Kind: domain.MessageMediaKindPoll, Poll: basePoll},
			}}
			if err := polls.Vote(pollID, votedA, [][]byte{opt1}, 100); err != nil {
				t.Fatalf("vote A: %v", err)
			}
			if err := polls.Vote(pollID, votedB, [][]byte{opt2}, 101); err != nil {
				t.Fatalf("vote B: %v", err)
			}

			viewers := []int64{creator, votedA, votedB, unvoted, late, banned, restricted, nonMember}
			views, err := channels.ChannelPollFanoutViews(ctx, channelID, msgID, viewers, 200)
			if err != nil {
				t.Fatalf("ChannelPollFanoutViews: %v", err)
			}
			if !views.Found {
				t.Fatal("poll fan-out views Found=false, want true")
			}

			for _, viewer := range viewers {
				got, evaluated := views.Polls[viewer]
				if !evaluated {
					t.Fatalf("viewer %d not evaluated in batch (all passed viewers must be evaluated)", viewer)
				}
				// Old-path reference: GetChannelMessages fetches the message per viewer (including visibility + per-viewer poll enrichment).
				ref, err := channels.GetChannelMessages(ctx, viewer, channelID, []int{msgID})
				var refPoll *domain.MessagePoll
				if err == nil {
					for _, m := range ref.Messages {
						if m.ID == msgID && m.Media != nil {
							refPoll = m.Media.Poll
						}
					}
				}
				if (got != nil) != (refPoll != nil) {
					t.Fatalf("viewer %d visibility mismatch: batch nil=%v, GetMessages nil=%v (err=%v)", viewer, got == nil, refPoll == nil, err)
				}
				if got != nil && !reflect.DeepEqual(got.Results, refPoll.Results) {
					t.Fatalf("viewer %d poll Results mismatch:\n batch=%+v\n  old=%+v", viewer, got.Results, refPoll.Results)
				}
			}

			// Explicitly assert the visibility classification, guarding against both GetMessages and the batch missing it at once.
			if views.Polls[creator] == nil || views.Polls[votedA] == nil || views.Polls[votedB] == nil || views.Polls[unvoted] == nil {
				t.Fatalf("active members should be visible: creator/votedA/votedB/unvoted")
			}
			if views.Polls[late] != nil || views.Polls[banned] != nil || views.Polls[restricted] != nil || views.Polls[nonMember] != nil {
				t.Fatalf("late/banned/restricted/nonMember must be invisible (nil)")
			}
			// During hide_results: a non-creator voter should have the count hidden (Voters=0), while the creator sees the real count.
			if hideResults {
				for _, v := range views.Polls[votedA].Results.Voters {
					if v.Voters != 0 {
						t.Fatalf("hide_results: non-creator votedA should see Voters=0, got %d", v.Voters)
					}
				}
				sawCount := false
				for _, v := range views.Polls[creator].Results.Voters {
					if v.Voters > 0 {
						sawCount = true
					}
				}
				if !sawCount {
					t.Fatal("hide_results: creator should see real counts")
				}
			}
		})
	}
}
