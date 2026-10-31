package rpc

import (
	"testing"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
)

// injectPaidReaction requester view: ReactionPaid first, count = total stars,
// chosen set; TopReactors includes the viewer with My + PeerID.
func TestInjectPaidReactionRequesterView(t *testing.T) {
	paid := domain.ChannelMessagePaidReactions{
		TotalStars: 150,
		MyStars:    50,
		TopReactors: []domain.PaidReactor{
			{UserID: 2, Stars: 100, My: false},
			{UserID: 1, Stars: 50, My: true},
		},
	}
	mr := &tg.MessageReactions{Results: []tg.ReactionCount{}}
	injectPaidReaction(mr, clonePaidForViewer(paid, true))

	if len(mr.Results) != 1 {
		t.Fatalf("results = %d, want 1 paid", len(mr.Results))
	}
	if _, ok := mr.Results[0].Reaction.(*tg.ReactionPaid); !ok {
		t.Fatalf("results[0] reaction = %T, want *tg.ReactionPaid", mr.Results[0].Reaction)
	}
	if mr.Results[0].Count != 150 {
		t.Fatalf("paid count = %d, want 150", mr.Results[0].Count)
	}
	if order, ok := mr.Results[0].GetChosenOrder(); !ok || order <= 0 {
		t.Fatalf("requester chosen = %d ok %v, want set+positive (MyStars>0)", order, ok)
	}
	reactors, ok := mr.GetTopReactors()
	if !ok || len(reactors) != 2 {
		t.Fatalf("top reactors = %d ok %v, want 2", len(reactors), ok)
	}
	// The second entry is the viewer.
	me := reactors[1]
	if !me.My || me.Count != 50 {
		t.Fatalf("my reactor = %+v, want My count 50", me)
	}
	if peer, ok := me.GetPeerID(); !ok {
		t.Fatalf("my reactor peer = %v, want set", peer)
	}
	// The first entry is top (not the viewer).
	if !reactors[0].Top || reactors[0].My {
		t.Fatalf("top reactor = %+v, want Top not My", reactors[0])
	}
}

// Non-requester view: no chosen, My scrubbed entirely (min semantics, preventing view leakage).
func TestInjectPaidReactionOtherViewerScrubsMy(t *testing.T) {
	paid := domain.ChannelMessagePaidReactions{
		TotalStars: 150,
		MyStars:    50,
		TopReactors: []domain.PaidReactor{
			{UserID: 2, Stars: 100, My: false},
			{UserID: 1, Stars: 50, My: true},
		},
	}
	mr := &tg.MessageReactions{Results: []tg.ReactionCount{}}
	injectPaidReaction(mr, clonePaidForViewer(paid, false))

	if _, ok := mr.Results[0].GetChosenOrder(); ok {
		t.Fatalf("non-requester chosen must be unset")
	}
	reactors, _ := mr.GetTopReactors()
	for i, rr := range reactors {
		if rr.My {
			t.Fatalf("reactor[%d] My must be scrubbed for non-requester: %+v", i, rr)
		}
	}
}

// Anonymous reactor (not the viewer): Anonymous set, PeerID not exposed.
func TestInjectPaidReactionAnonymousHidesPeer(t *testing.T) {
	paid := domain.ChannelMessagePaidReactions{
		TotalStars:  100,
		TopReactors: []domain.PaidReactor{{UserID: 9, Stars: 100, Anonymous: true, My: false}},
	}
	mr := &tg.MessageReactions{Results: []tg.ReactionCount{}}
	injectPaidReaction(mr, clonePaidForViewer(paid, true))
	reactors, ok := mr.GetTopReactors()
	if !ok || len(reactors) != 1 {
		t.Fatalf("reactors = %d ok %v, want 1", len(reactors), ok)
	}
	if !reactors[0].Anonymous {
		t.Fatalf("reactor must be Anonymous: %+v", reactors[0])
	}
	if peer, ok := reactors[0].GetPeerID(); ok {
		t.Fatalf("anonymous non-self reactor must not expose peer, got %v", peer)
	}
}

// Empty paid state: inject no ReactionPaid.
func TestInjectPaidReactionEmpty(t *testing.T) {
	mr := &tg.MessageReactions{Results: []tg.ReactionCount{}}
	injectPaidReaction(mr, domain.ChannelMessagePaidReactions{})
	if len(mr.Results) != 0 {
		t.Fatalf("empty paid must inject nothing, got %d results", len(mr.Results))
	}
	if _, ok := mr.GetTopReactors(); ok {
		t.Fatalf("empty paid must not set top reactors")
	}
}
