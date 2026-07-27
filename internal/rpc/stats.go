package rpc

import (
	"context"
	"fmt"

	"github.com/iamxvbaba/td/tg"

	"github.com/iamxvbaba/td/tlprofile"
	"telesrv/internal/domain"
)

const (
	maxStatsPublicForwardsLimit = 100
	maxStatsOffsetLength        = 128
	maxStatsGraphTokenLength    = 128
)

func (r *Router) registerStats(d *tlprofile.Dispatcher) {
	registerRPC[*tg.StatsGetBroadcastStatsRequest](d, tlprofile.SemanticMethodStatsGetBroadcastStats, func(ctx context.Context, layerRequest *tg.StatsGetBroadcastStatsRequest) (any, error) {
		return r.onStatsGetBroadcastStats(ctx, layerRequest)
	})
	registerRPC[*tg.StatsGetMegagroupStatsRequest](d, tlprofile.SemanticMethodStatsGetMegagroupStats, func(ctx context.Context, layerRequest *tg.StatsGetMegagroupStatsRequest) (any, error) {
		return r.onStatsGetMegagroupStats(ctx, layerRequest)
	})
	registerRPC[*tg.StatsGetMessageStatsRequest](d, tlprofile.SemanticMethodStatsGetMessageStats, func(ctx context.Context, layerRequest *tg.StatsGetMessageStatsRequest) (any, error) {
		return r.onStatsGetMessageStats(ctx, layerRequest)
	})
	registerRPC[*tg.StatsGetMessagePublicForwardsRequest](d, tlprofile.SemanticMethodStatsGetMessagePublicForwards, func(ctx context.Context, layerRequest *tg.StatsGetMessagePublicForwardsRequest) (any, error) {
		return r.onStatsGetMessagePublicForwards(ctx, layerRequest)
	})
	registerRPC[*tg.StatsLoadAsyncGraphRequest](d, tlprofile.SemanticMethodStatsLoadAsyncGraph, func(ctx context.Context, layerRequest *tg.StatsLoadAsyncGraphRequest) (any, error) {
		return r.onStatsLoadAsyncGraph(ctx, layerRequest)
	})
	registerRPC[*tg.StatsGetStoryStatsRequest](d, tlprofile.SemanticMethodStatsGetStoryStats, func(ctx context.Context, layerRequest *tg.StatsGetStoryStatsRequest) (any, error) {
		return r.onStatsGetStoryStats(ctx, layerRequest)
	})
	registerRPC[*tg.StatsGetStoryPublicForwardsRequest](d, tlprofile.SemanticMethodStatsGetStoryPublicForwards, func(ctx context.Context, layerRequest *tg.StatsGetStoryPublicForwardsRequest) (any, error) {
		return r.onStatsGetStoryPublicForwards(ctx, layerRequest)
	})
	registerRPC[*tg.StatsGetPollStatsRequest](d, tlprofile.SemanticMethodStatsGetPollStats, func(ctx context.Context, layerRequest *tg.StatsGetPollStatsRequest) (any, error) {
		return r.onStatsGetPollStats(ctx, layerRequest)
	})
}

func (r *Router) onStatsGetBroadcastStats(ctx context.Context, req *tg.StatsGetBroadcastStatsRequest) (*tg.StatsBroadcastStats, error) {
	view, err := r.statsChannelView(ctx, req.Channel)
	if err != nil {
		return nil, err
	}
	if !view.Channel.Broadcast {
		return nil, tgerr400("BROADCAST_REQUIRED")
	}
	return r.emptyBroadcastStats(), nil
}

func (r *Router) onStatsGetMegagroupStats(ctx context.Context, req *tg.StatsGetMegagroupStatsRequest) (*tg.StatsMegagroupStats, error) {
	view, err := r.statsChannelView(ctx, req.Channel)
	if err != nil {
		return nil, err
	}
	if !view.Channel.Megagroup {
		return nil, tgerr400("MEGAGROUP_REQUIRED")
	}
	return r.emptyMegagroupStats(), nil
}

func (r *Router) onStatsGetMessageStats(ctx context.Context, req *tg.StatsGetMessageStatsRequest) (*tg.StatsMessageStats, error) {
	if req.MsgID <= 0 || req.MsgID > domain.MaxMessageBoxID {
		return nil, messageIDInvalidErr()
	}
	if _, err := r.statsChannelView(ctx, req.Channel); err != nil {
		return nil, err
	}
	return &tg.StatsMessageStats{
		ViewsGraph:              r.emptyStatsGraph("Views"),
		ReactionsByEmotionGraph: r.emptyStatsGraph("Reactions"),
	}, nil
}

func (r *Router) onStatsGetMessagePublicForwards(ctx context.Context, req *tg.StatsGetMessagePublicForwardsRequest) (*tg.StatsPublicForwards, error) {
	if req.MsgID <= 0 || req.MsgID > domain.MaxMessageBoxID {
		return nil, messageIDInvalidErr()
	}
	if req.Limit < 0 || req.Limit > maxStatsPublicForwardsLimit || len(req.Offset) > maxStatsOffsetLength {
		return nil, limitInvalidErr()
	}
	if _, err := r.statsChannelView(ctx, req.Channel); err != nil {
		return nil, err
	}
	return emptyStatsPublicForwards(), nil
}

func (r *Router) onStatsLoadAsyncGraph(_ context.Context, req *tg.StatsLoadAsyncGraphRequest) (tg.StatsGraphClass, error) {
	if len(req.Token) > maxStatsGraphTokenLength {
		return &tg.StatsGraphError{Error: "GRAPH_INVALID_RELOAD"}, nil
	}
	return &tg.StatsGraphError{Error: "GRAPH_INVALID_RELOAD"}, nil
}

func (r *Router) onStatsGetStoryStats(ctx context.Context, req *tg.StatsGetStoryStatsRequest) (*tg.StatsStoryStats, error) {
	if req.ID <= 0 || req.ID > domain.MaxStoryID {
		return nil, storyIDInvalidErr()
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	peer, err := r.checkedDomainPeerFromInputPeer(ctx, userID, req.Peer)
	if err != nil {
		return nil, err
	}
	if r.deps.Stories != nil {
		if err := r.deps.Stories.CanViewStoryStats(ctx, userID, peer); err != nil {
			return nil, storyErr(err)
		}
	}
	return &tg.StatsStoryStats{
		ViewsGraph:              r.emptyStatsGraph("Views"),
		ReactionsByEmotionGraph: r.emptyStatsGraph("Reactions"),
	}, nil
}

func (r *Router) onStatsGetStoryPublicForwards(ctx context.Context, req *tg.StatsGetStoryPublicForwardsRequest) (*tg.StatsPublicForwards, error) {
	if req.ID <= 0 || req.ID > domain.MaxStoryID {
		return nil, storyIDInvalidErr()
	}
	if req.Limit < 0 || req.Limit > maxStatsPublicForwardsLimit || len(req.Offset) > maxStatsOffsetLength {
		return nil, limitInvalidErr()
	}
	if err := domain.ValidateStoryInteractionOffset(req.Offset, false); err != nil {
		return nil, storyErr(err)
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	peer, err := r.checkedDomainPeerFromInputPeer(ctx, userID, req.Peer)
	if err != nil {
		return nil, err
	}
	if r.deps.Stories == nil || userID == 0 {
		return emptyStatsPublicForwards(), nil
	}
	limit := req.Limit
	if limit <= 0 || limit > domain.MaxStoryInteractionListLimit {
		limit = domain.MaxStoryInteractionListLimit
	}
	storyForwards, err := r.deps.Stories.GetStoryPublicForwards(ctx, userID, domain.StoryPublicForwardListRequest{
		ViewerUserID: userID,
		Owner:        peer,
		StoryID:      req.ID,
		Offset:       req.Offset,
		Limit:        limit,
	})
	if err != nil {
		return nil, storyErr(err)
	}
	messageForwards := domain.StoryMessageForwardList{}
	if r.deps.Channels != nil {
		messageForwards, err = r.deps.Channels.ListStoryMessageForwards(ctx, userID, domain.StoryMessageForwardListRequest{
			ViewerUserID:  userID,
			Owner:         peer,
			StoryID:       req.ID,
			Offset:        req.Offset,
			Limit:         limit,
			ForwardsFirst: true,
		})
		if err != nil {
			return nil, storyErr(err)
		}
	}
	hasMore := storyInteractionSourcesHaveMore(len(storyForwards.Forwards), len(messageForwards.Forwards), limit, storyForwards.NextOffset != "" || messageForwards.NextOffset != "")
	forwards := mergeStoryInteractionViews(storyForwards.Forwards, messageForwards.Forwards, limit, false, true)
	return r.tgStatsPublicForwards(ctx, userID, domain.StoryPublicForwardList{
		Count:      storyForwards.Count + messageForwards.Count,
		Forwards:   forwards,
		NextOffset: nextStoryInteractionOffset(forwards, limit, false, true, hasMore),
	}), nil
}

func (r *Router) onStatsGetPollStats(ctx context.Context, req *tg.StatsGetPollStatsRequest) (*tg.StatsPollStats, error) {
	if req.MsgID <= 0 || req.MsgID > domain.MaxMessageBoxID {
		return nil, messageIDInvalidErr()
	}
	if err := r.validateStatsPeer(ctx, req.Peer); err != nil {
		return nil, err
	}
	return &tg.StatsPollStats{VotesGraph: r.emptyStatsGraph("Votes")}, nil
}

func (r *Router) statsChannelView(ctx context.Context, input tg.InputChannelClass) (domain.ChannelView, error) {
	_, view, err := r.channelView(ctx, input)
	if err != nil {
		return domain.ChannelView{}, err
	}
	if view.Self.Role != domain.ChannelRoleCreator && view.Self.Role != domain.ChannelRoleAdmin {
		return domain.ChannelView{}, tgerr400("CHAT_ADMIN_REQUIRED")
	}
	return view, nil
}

func (r *Router) validateStatsPeer(ctx context.Context, peer tg.InputPeerClass) error {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return internalErr()
	}
	_, err = r.checkedDomainPeerFromInputPeer(ctx, userID, peer)
	return err
}

func (r *Router) emptyBroadcastStats() *tg.StatsBroadcastStats {
	return &tg.StatsBroadcastStats{
		Period:                       r.statsDateRange(),
		Followers:                    tg.StatsAbsValueAndPrev{},
		ViewsPerPost:                 tg.StatsAbsValueAndPrev{},
		SharesPerPost:                tg.StatsAbsValueAndPrev{},
		ReactionsPerPost:             tg.StatsAbsValueAndPrev{},
		ViewsPerStory:                tg.StatsAbsValueAndPrev{},
		SharesPerStory:               tg.StatsAbsValueAndPrev{},
		ReactionsPerStory:            tg.StatsAbsValueAndPrev{},
		EnabledNotifications:         tg.StatsPercentValue{},
		GrowthGraph:                  r.emptyStatsGraph("Growth"),
		FollowersGraph:               r.emptyStatsGraph("Followers"),
		MuteGraph:                    r.emptyStatsGraph("Muted"),
		TopHoursGraph:                r.emptyStatsGraph("Hours"),
		InteractionsGraph:            r.emptyStatsGraph("Interactions"),
		IvInteractionsGraph:          r.emptyStatsGraph("Instant Views"),
		ViewsBySourceGraph:           r.emptyStatsGraph("Views"),
		NewFollowersBySourceGraph:    r.emptyStatsGraph("Followers"),
		LanguagesGraph:               r.emptyStatsGraph("Languages"),
		ReactionsByEmotionGraph:      r.emptyStatsGraph("Reactions"),
		StoryInteractionsGraph:       r.emptyStatsGraph("Stories"),
		StoryReactionsByEmotionGraph: r.emptyStatsGraph("Story Reactions"),
	}
}

func (r *Router) emptyMegagroupStats() *tg.StatsMegagroupStats {
	return &tg.StatsMegagroupStats{
		Period:                  r.statsDateRange(),
		Members:                 tg.StatsAbsValueAndPrev{},
		Messages:                tg.StatsAbsValueAndPrev{},
		Viewers:                 tg.StatsAbsValueAndPrev{},
		Posters:                 tg.StatsAbsValueAndPrev{},
		GrowthGraph:             r.emptyStatsGraph("Growth"),
		MembersGraph:            r.emptyStatsGraph("Members"),
		NewMembersBySourceGraph: r.emptyStatsGraph("Members"),
		LanguagesGraph:          r.emptyStatsGraph("Languages"),
		MessagesGraph:           r.emptyStatsGraph("Messages"),
		ActionsGraph:            r.emptyStatsGraph("Actions"),
		TopHoursGraph:           r.emptyStatsGraph("Hours"),
		WeekdaysGraph:           r.emptyStatsGraph("Weekdays"),
		TopPosters:              []tg.StatsGroupTopPoster{},
		TopAdmins:               []tg.StatsGroupTopAdmin{},
		TopInviters:             []tg.StatsGroupTopInviter{},
	}
}

func (r *Router) statsDateRange() tg.StatsDateRangeDays {
	now := int(r.clock.Now().Unix())
	return tg.StatsDateRangeDays{MinDate: now - 86400, MaxDate: now}
}

func (r *Router) emptyStatsGraph(label string) *tg.StatsGraph {
	nowMillis := r.clock.Now().UnixMilli()
	prevMillis := nowMillis - 86400000
	data := fmt.Sprintf(
		`{"columns":[["x",%d,%d],["y0",0,0]],"types":{"x":"x","y0":"line"},"names":{"y0":%q},"colors":{"y0":"blue#4a90e2"}}`,
		prevMillis,
		nowMillis,
		label,
	)
	return &tg.StatsGraph{JSON: tg.DataJSON{Data: data}}
}

func emptyStatsPublicForwards() *tg.StatsPublicForwards {
	return &tg.StatsPublicForwards{
		Count:    0,
		Forwards: []tg.PublicForwardClass{},
		Chats:    []tg.ChatClass{},
		Users:    []tg.UserClass{},
	}
}

func (r *Router) tgStatsPublicForwards(ctx context.Context, viewerUserID int64, list domain.StoryPublicForwardList) *tg.StatsPublicForwards {
	users := r.domainUsersForIDs(ctx, viewerUserID, storyViewUserIDs(list.Forwards))
	peerUsers, peerChannels := r.storyPeerObjects(ctx, viewerUserID, storyViewPeers(list.Forwards))
	users = mergeDomainUsers(users, peerUsers...)
	out := &tg.StatsPublicForwards{
		Count:    list.Count,
		Forwards: tgPublicForwardItems(viewerUserID, list.Forwards),
		Chats:    tgChannels(viewerUserID, peerChannels),
		Users:    tgUsersForViewer(viewerUserID, users),
	}
	if list.NextOffset != "" {
		out.SetNextOffset(list.NextOffset)
	}
	r.applyStoryMaxIDsToPeerObjects(ctx, viewerUserID, out.Users, out.Chats)
	return out
}

func tgPublicForwardItems(viewerUserID int64, views []domain.StoryView) []tg.PublicForwardClass {
	out := make([]tg.PublicForwardClass, 0, len(views))
	for _, view := range views {
		if view.PublicForward != nil {
			msg := tgChannelMessage(viewerUserID, view.PublicForward.Message)
			if msg == nil {
				continue
			}
			out = append(out, &tg.PublicForwardMessage{Message: msg})
			continue
		}
		if view.Repost != nil {
			peer := tgPeer(view.Repost.Owner)
			if peer == nil {
				continue
			}
			out = append(out, &tg.PublicForwardStory{
				Peer:  peer,
				Story: tgStoryItem(*view.Repost),
			})
		}
	}
	return out
}
