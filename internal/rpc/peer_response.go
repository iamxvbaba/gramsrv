package rpc

import (
	"context"

	"github.com/iamxvbaba/td/tg"
	"telesrv/internal/domain"
)

// finalizePeerResponse covers supplemental peer lists as well as dedicated
// profile queries. A full peer object replaces cached optional identity flags.
func (r *Router) finalizePeerResponse(ctx context.Context, viewer int64, value any) {
	var users []tg.UserClass
	var chats []tg.ChatClass
	if box, ok := value.(interface{ GetUsers() []tg.UserClass }); ok {
		users = box.GetUsers()
	}
	if box, ok := value.(interface{ GetChats() []tg.ChatClass }); ok {
		chats = box.GetChats()
	}
	switch v := value.(type) {
	case []tg.UserClass:
		users = v
	case *tg.User:
		users = []tg.UserClass{v}
	case *tg.Channel:
		chats = []tg.ChatClass{v}
	}
	r.applyBotOwnership(ctx, viewer, users)
	r.applyPeerIdentitiesToPeerObjects(ctx, users, chats)
}

// Ownership is viewer-specific and must never be stored on a shared bot profile.
func (r *Router) applyBotOwnership(ctx context.Context, viewer int64, users []tg.UserClass) {
	if viewer == 0 || r.deps.Bots == nil {
		return
	}
	ids := []int64{}
	seen := map[int64]bool{}
	for _, item := range users {
		if u, ok := item.(*tg.User); ok && u != nil && u.Bot && !u.Deleted && !seen[u.ID] {
			seen[u.ID] = true
			ids = append(ids, u.ID)
		}
	}
	if len(ids) == 0 {
		return
	}
	profiles := map[int64]domain.BotProfile{}
	if batch, ok := r.deps.Bots.(botProfileBatchResolver); ok {
		loaded, err := batch.BotInfos(ctx, ids)
		if err != nil {
			return
		}
		profiles = loaded
	} else {
		for _, id := range ids {
			profile, found, err := r.deps.Bots.BotInfo(ctx, id)
			if err == nil && found {
				profiles[id] = profile
			}
		}
	}
	for _, item := range users {
		if u, ok := item.(*tg.User); ok && u != nil && u.Bot && !u.Deleted {
			if profile, found := profiles[u.ID]; found {
				u.Min = false
				u.SetBotCanEdit(profile.OwnerUserID == viewer)
			}
		}
	}
}
