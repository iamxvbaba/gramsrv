package rpc

import (
	"testing"

	"telesrv/internal/domain"
)

func TestBotAPIMessageMediaProjectableReplyKeyboardResponses(t *testing.T) {
	validRequestedUsers := &domain.MessageRequestedPeerAction{
		ButtonID: 7, Peers: []domain.Peer{{Type: domain.PeerTypeUser, ID: 1001}, {Type: domain.PeerTypeUser, ID: 1002}},
	}
	tests := []struct {
		name  string
		media *domain.MessageMedia
		want  bool
	}{
		{"contact", &domain.MessageMedia{Kind: domain.MessageMediaKindContact, Contact: &domain.MessageContact{}}, true},
		{"geo", &domain.MessageMedia{Kind: domain.MessageMediaKindGeo, Geo: &domain.MessageGeoPoint{}}, true},
		{"venue", &domain.MessageMedia{Kind: domain.MessageMediaKindVenue, Venue: &domain.MessageVenue{}}, true},
		{"poll", &domain.MessageMedia{Kind: domain.MessageMediaKindPoll, Poll: &domain.MessagePoll{}}, true},
		{"live geo", &domain.MessageMedia{Kind: domain.MessageMediaKindGeoLive, GeoLive: &domain.MessageGeoLive{}}, true},
		{"web app", &domain.MessageMedia{Kind: domain.MessageMediaKindService, ServiceAction: &domain.MessageServiceAction{
			Kind: domain.MessageServiceActionWebViewDataSent, WebViewData: &domain.MessageWebViewDataAction{},
		}}, true},
		{"requested users", &domain.MessageMedia{Kind: domain.MessageMediaKindService, ServiceAction: &domain.MessageServiceAction{
			Kind: domain.MessageServiceActionRequestedPeer, RequestedPeer: validRequestedUsers,
		}}, true},
		{"requested disclosure without snapshot", &domain.MessageMedia{Kind: domain.MessageMediaKindService, ServiceAction: &domain.MessageServiceAction{
			Kind: domain.MessageServiceActionRequestedPeer, RequestedPeer: &domain.MessageRequestedPeerAction{
				ButtonID: 7, Peers: []domain.Peer{{Type: domain.PeerTypeUser, ID: 1001}}, NameRequested: true,
			},
		}}, false},
		{"mixed requested peers", &domain.MessageMedia{Kind: domain.MessageMediaKindService, ServiceAction: &domain.MessageServiceAction{
			Kind: domain.MessageServiceActionRequestedPeer, RequestedPeer: &domain.MessageRequestedPeerAction{
				ButtonID: 7, Peers: []domain.Peer{{Type: domain.PeerTypeUser, ID: 1001}, {Type: domain.PeerTypeChannel, ID: 55}},
			},
		}}, false},
		{"unrelated service", &domain.MessageMedia{Kind: domain.MessageMediaKindService, ServiceAction: &domain.MessageServiceAction{
			Kind: domain.MessageServiceActionPhoneCall, Call: &domain.MessagePhoneCallAction{},
		}}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := botAPIMessageMediaProjectable(tt.media); got != tt.want {
				t.Fatalf("projectable=%v want=%v media=%#v", got, tt.want, tt.media)
			}
		})
	}
}

func TestCollectMessagePeerRefsIncludesRequestedPeers(t *testing.T) {
	users := map[int64]struct{}{}
	channels := map[int64]struct{}{}
	collectMessagePeerRefs(domain.Message{Media: &domain.MessageMedia{
		Kind: domain.MessageMediaKindService,
		ServiceAction: &domain.MessageServiceAction{
			Kind: domain.MessageServiceActionRequestedPeer,
			RequestedPeer: &domain.MessageRequestedPeerAction{ButtonID: 1, Peers: []domain.Peer{
				{Type: domain.PeerTypeUser, ID: 1001}, {Type: domain.PeerTypeChannel, ID: 55},
			}},
		},
	}}, 0, users, channels)
	if _, ok := users[1001]; !ok {
		t.Fatalf("requested user refs=%v", users)
	}
	if _, ok := channels[55]; !ok {
		t.Fatalf("requested channel refs=%v", channels)
	}
}
