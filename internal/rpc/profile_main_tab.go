package rpc

import (
	"context"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tlprofile"
	"go.uber.org/zap"

	"telesrv/internal/domain"
)

// MainTabStore keeps the profile tab a user or channel opens first.
type MainTabStore interface {
	SetMainTab(ctx context.Context, peer domain.Peer, tab string) error
	MainTab(ctx context.Context, peer domain.Peer) (string, error)
}

func (r *Router) registerProfileMainTab(d *tlprofile.Dispatcher) {
	registerRPC[*tg.AccountSetMainProfileTabRequest](d, tlprofile.SemanticMethodAccountSetMainProfileTab, func(ctx context.Context, req *tg.AccountSetMainProfileTabRequest) (any, error) {
		uid, ok, err := r.currentUserID(ctx)
		if err != nil {
			return nil, internalErr()
		}
		if !ok || uid <= 0 {
			return nil, authKeyUnregisteredErr()
		}
		name, valid := profileTabName(req.Tab)
		if !valid {
			return nil, inputRequestInvalidErr()
		}
		if r.deps.MainTabs != nil {
			if err := r.deps.MainTabs.SetMainTab(ctx, domain.Peer{Type: domain.PeerTypeUser, ID: uid}, name); err != nil {
				return nil, internalErr()
			}
			r.invalidateRPCProjectionForUser(uid)
		}
		return true, nil
	})

}

func (r *Router) saveChannelMainTab(ctx context.Context, channelID int64, tab tg.ProfileTabClass) error {
	if r.deps.MainTabs == nil {
		return nil
	}
	name, valid := profileTabName(tab)
	if !valid {
		return inputRequestInvalidErr()
	}
	if err := r.deps.MainTabs.SetMainTab(ctx, domain.Peer{Type: domain.PeerTypeChannel, ID: channelID}, name); err != nil {
		return internalErr()
	}
	r.invalidateRPCProjectionForChannel(channelID)
	return nil
}

func (r *Router) mainTabFor(ctx context.Context, peer domain.Peer) (tg.ProfileTabClass, bool) {
	if r.deps.MainTabs == nil {
		return nil, false
	}
	name, err := r.deps.MainTabs.MainTab(ctx, peer)
	if err != nil {
		if r.log != nil {
			r.log.Warn("main_tab_lookup_failed", zap.Error(err))
		}
		return nil, false
	}
	return profileTabFromName(name)
}

func profileTabName(tab tg.ProfileTabClass) (string, bool) {
	switch tab.(type) {
	case *tg.ProfileTabPosts:
		return "posts", true
	case *tg.ProfileTabGifts:
		return "gifts", true
	case *tg.ProfileTabMedia:
		return "media", true
	case *tg.ProfileTabFiles:
		return "files", true
	case *tg.ProfileTabMusic:
		return "music", true
	case *tg.ProfileTabVoice:
		return "voice", true
	case *tg.ProfileTabLinks:
		return "links", true
	case *tg.ProfileTabGifs:
		return "gifs", true
	}
	return "", false
}

func profileTabFromName(name string) (tg.ProfileTabClass, bool) {
	switch name {
	case "posts":
		return &tg.ProfileTabPosts{}, true
	case "gifts":
		return &tg.ProfileTabGifts{}, true
	case "media":
		return &tg.ProfileTabMedia{}, true
	case "files":
		return &tg.ProfileTabFiles{}, true
	case "music":
		return &tg.ProfileTabMusic{}, true
	case "voice":
		return &tg.ProfileTabVoice{}, true
	case "links":
		return &tg.ProfileTabLinks{}, true
	case "gifs":
		return &tg.ProfileTabGifs{}, true
	}
	return nil, false
}
