package rpc

import (
	"testing"

	"telesrv/internal/domain"
)

func TestRemoveKnownChannelRefs(t *testing.T) {
	refs := map[int64]struct{}{
		1001: {},
		1002: {},
		1003: {},
	}

	removeKnownChannelRefs(refs, []domain.Channel{
		{ID: 1002},
		{ID: 0},
		{ID: 1004},
	})

	if _, ok := refs[1002]; ok {
		t.Fatalf("known channel ref was not removed: %+v", refs)
	}
	for _, id := range []int64{1001, 1003} {
		if _, ok := refs[id]; !ok {
			t.Fatalf("unexpectedly removed channel %d from refs %+v", id, refs)
		}
	}
}

func TestCollectMessagePeerRefsIncludesStarGiftServiceActions(t *testing.T) {
	users := map[int64]struct{}{}
	channels := map[int64]struct{}{}
	collectMessagePeerRefs(domain.Message{Media: &domain.MessageMedia{
		Kind: domain.MessageMediaKindService,
		ServiceAction: &domain.MessageServiceAction{
			Kind: domain.MessageServiceActionStarGift,
			StarGift: &domain.MessageStarGiftAction{
				FromUserID: 1001, PeerChannelID: 55,
			},
		},
	}}, 0, users, channels)
	if _, ok := users[1001]; !ok {
		t.Fatalf("ordinary star-gift user refs=%v, missing sender", users)
	}
	if _, ok := channels[55]; !ok {
		t.Fatalf("ordinary star-gift channel refs=%v", channels)
	}
	collectMessagePeerRefs(domain.Message{Media: &domain.MessageMedia{
		Kind: domain.MessageMediaKindService,
		ServiceAction: &domain.MessageServiceAction{
			Kind:     domain.MessageServiceActionStarGift,
			StarGift: &domain.MessageStarGiftAction{PeerUserID: 1002},
		},
	}}, 0, users, channels)
	if _, ok := users[1002]; !ok {
		t.Fatalf("ordinary star-gift recipient refs=%v", users)
	}

	collectMessagePeerRefs(domain.Message{Media: &domain.MessageMedia{
		Kind: domain.MessageMediaKindService,
		ServiceAction: &domain.MessageServiceAction{
			Kind: domain.MessageServiceActionStarGiftUnique,
			StarGiftUnique: &domain.MessageStarGiftUniqueAction{
				FromUserID: 2001,
				Peer:       domain.Peer{Type: domain.PeerTypeChannel, ID: 56},
				Gift: domain.UniqueStarGift{
					Owner:              domain.Peer{Type: domain.PeerTypeChannel, ID: 56},
					OriginalFromUserID: 2002,
					OriginalOwner:      domain.Peer{Type: domain.PeerTypeUser, ID: 2003},
					ReleasedBy:         domain.Peer{Type: domain.PeerTypeUser, ID: 2004},
					ThemePeer:          domain.Peer{Type: domain.PeerTypeChannel, ID: 57},
					Host:               domain.Peer{Type: domain.PeerTypeUser, ID: 2005},
				},
			},
		},
	}}, 0, users, channels)
	for _, id := range []int64{2001, 2002, 2003, 2004, 2005} {
		if _, ok := users[id]; !ok {
			t.Fatalf("unique star-gift user refs=%v, missing %d", users, id)
		}
	}
	for _, id := range []int64{56, 57} {
		if _, ok := channels[id]; !ok {
			t.Fatalf("unique star-gift channel refs=%v, missing %d", channels, id)
		}
	}
}

func TestCollectMessagePeerRefsHidesStarGiftSenderDetails(t *testing.T) {
	users := map[int64]struct{}{}
	channels := map[int64]struct{}{}
	collectMessagePeerRefs(domain.Message{Media: &domain.MessageMedia{
		Kind: domain.MessageMediaKindService,
		ServiceAction: &domain.MessageServiceAction{
			Kind: domain.MessageServiceActionStarGift,
			StarGift: &domain.MessageStarGiftAction{
				FromUserID: 1001, NameHidden: true, PeerChannelID: 55,
			},
		},
	}}, 0, users, channels)
	collectMessagePeerRefs(domain.Message{Media: &domain.MessageMedia{
		Kind: domain.MessageMediaKindService,
		ServiceAction: &domain.MessageServiceAction{
			Kind: domain.MessageServiceActionStarGiftUnique,
			StarGiftUnique: &domain.MessageStarGiftUniqueAction{
				Gift: domain.UniqueStarGift{
					OriginalFromUserID: 2001,
					OriginalNameHidden: true,
				},
			},
		},
	}}, 0, users, channels)
	for _, id := range []int64{1001, 2001} {
		if _, ok := users[id]; ok {
			t.Fatalf("hidden star-gift sender %d leaked into refs=%v", id, users)
		}
	}
	if _, ok := channels[55]; !ok {
		t.Fatalf("hidden ordinary gift lost recipient channel ref=%v", channels)
	}
}
