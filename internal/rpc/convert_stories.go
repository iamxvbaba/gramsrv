package rpc

import (
	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
)

func tgStoriesAllStories(viewerUserID int64, list domain.StoryList) tg.StoriesAllStoriesClass {
	return &tg.StoriesAllStories{
		HasMore:     list.HasMore,
		Count:       list.Count,
		State:       list.State,
		PeerStories: tgPeerStoriesList(list.Peers),
		Chats:       tgChannels(viewerUserID, list.Channels),
		Users:       tgUsersForViewer(viewerUserID, list.Users),
		StealthMode: tg.StoriesStealthMode{},
	}
}

func tgStoriesAllStoriesNotModified(state string) tg.StoriesAllStoriesClass {
	return &tg.StoriesAllStoriesNotModified{
		State:       state,
		StealthMode: tg.StoriesStealthMode{},
	}
}

func tgStoriesStories(viewerUserID int64, list domain.StoryList) *tg.StoriesStories {
	out := &tg.StoriesStories{
		Count:   list.Count,
		Stories: tgStoryItems(list.Stories),
		Chats:   tgChannels(viewerUserID, list.Channels),
		Users:   tgUsersForViewer(viewerUserID, list.Users),
	}
	if len(list.PinnedToTop) > 0 {
		out.SetPinnedToTop(append([]int(nil), list.PinnedToTop...))
	}
	return out
}

func tgStoriesPeerStories(viewerUserID int64, peerStories domain.PeerStories) *tg.StoriesPeerStories {
	return &tg.StoriesPeerStories{
		Stories: tgPeerStories(peerStories),
		Chats:   tgChannels(viewerUserID, peerStories.Channels),
		Users:   tgUsersForViewer(viewerUserID, peerStories.Users),
	}
}

func tgPeerStoriesList(items []domain.PeerStories) []tg.PeerStories {
	out := make([]tg.PeerStories, 0, len(items))
	for _, item := range items {
		out = append(out, tgPeerStories(item))
	}
	return out
}

func tgPeerStories(item domain.PeerStories) tg.PeerStories {
	out := tg.PeerStories{
		Peer:    tgPeer(item.Peer),
		Stories: tgStoryItems(item.Stories),
	}
	if item.MaxReadID > 0 {
		out.SetMaxReadID(item.MaxReadID)
	}
	return out
}

func tgStoryItems(stories []domain.Story) []tg.StoryItemClass {
	out := make([]tg.StoryItemClass, 0, len(stories))
	for _, story := range stories {
		out = append(out, tgStoryItem(story))
	}
	return out
}

func tgStoryItem(story domain.Story) tg.StoryItemClass {
	if story.Deleted {
		return &tg.StoryItemDeleted{ID: story.ID}
	}
	media := tgMessageMedia(story.Media)
	out := &tg.StoryItem{
		Pinned:           story.Pinned,
		Public:           story.Public,
		CloseFriends:     story.CloseFriends,
		Noforwards:       story.NoForwards,
		Edited:           story.Edited,
		Contacts:         story.Contacts,
		SelectedContacts: story.SelectedContacts,
		Out:              story.Out,
		ID:               story.ID,
		Date:             story.Date,
		ExpireDate:       story.ExpireDate,
		Media:            media,
	}
	if peer := tgPeer(story.Owner); peer != nil {
		out.SetFromID(peer)
	}
	if story.Caption != "" {
		out.SetCaption(story.Caption)
	}
	if len(story.Entities) > 0 {
		out.SetEntities(tgMessageEntities(story.Entities))
	}
	if len(story.MediaAreas) > 0 {
		out.SetMediaAreas(tgStoryMediaAreas(story.MediaAreas))
	}
	if forward := tgStoryForward(story.Forward); forward != nil {
		out.SetFwdFrom(*forward)
	}
	if story.Out && len(story.PrivacyRules) > 0 {
		out.SetPrivacy(tgPrivacyRules(story.PrivacyRules))
	}
	if !storyViewsEmpty(story.Views) {
		out.SetViews(tgStoryViews(story.Views))
	}
	if story.SentReaction != nil {
		out.SetSentReaction(tgMessageReaction(*story.SentReaction))
	}
	return out
}

func tgStoryForward(in *domain.StoryForward) *tg.StoryFwdHeader {
	if in == nil {
		return nil
	}
	out := &tg.StoryFwdHeader{
		Modified: in.Modified,
	}
	if peer := tgPeer(in.From); peer != nil {
		out.SetFrom(peer)
	}
	if in.FromName != "" {
		out.SetFromName(in.FromName)
	}
	if in.StoryID > 0 {
		out.SetStoryID(in.StoryID)
	}
	if out.From == nil && out.FromName == "" && out.StoryID == 0 && !out.Modified {
		return nil
	}
	return out
}

func tgStoryMediaAreas(in []domain.StoryMediaArea) []tg.MediaAreaClass {
	out := make([]tg.MediaAreaClass, 0, len(in))
	for _, area := range in {
		switch area.Kind {
		case domain.StoryMediaAreaSuggestedReaction:
			if area.Reaction == nil {
				continue
			}
			reaction := tgMessageReaction(*area.Reaction)
			if reaction == nil {
				continue
			}
			out = append(out, &tg.MediaAreaSuggestedReaction{
				Dark:        area.Dark,
				Flipped:     area.Flipped,
				Coordinates: tgStoryMediaAreaCoordinates(area.Coordinates),
				Reaction:    reaction,
			})
		case domain.StoryMediaAreaURL:
			if area.URL == "" {
				continue
			}
			out = append(out, &tg.MediaAreaURL{
				Coordinates: tgStoryMediaAreaCoordinates(area.Coordinates),
				URL:         area.URL,
			})
		case domain.StoryMediaAreaGeoPoint:
			if area.Geo == nil {
				continue
			}
			geo := &tg.MediaAreaGeoPoint{
				Coordinates: tgStoryMediaAreaCoordinates(area.Coordinates),
				Geo:         tgGeoPoint(*area.Geo),
			}
			if address := tgStoryGeoPointAddress(area.GeoAddress); address != nil {
				geo.SetAddress(*address)
			}
			out = append(out, geo)
		case domain.StoryMediaAreaVenue:
			if area.Venue == nil {
				continue
			}
			out = append(out, &tg.MediaAreaVenue{
				Coordinates: tgStoryMediaAreaCoordinates(area.Coordinates),
				Geo:         tgGeoPoint(area.Venue.Geo),
				Title:       area.Venue.Title,
				Address:     area.Venue.Address,
				Provider:    area.Venue.Provider,
				VenueID:     area.Venue.VenueID,
				VenueType:   area.Venue.VenueType,
			})
		case domain.StoryMediaAreaWeather:
			if area.WeatherEmoji == "" {
				continue
			}
			out = append(out, &tg.MediaAreaWeather{
				Coordinates:  tgStoryMediaAreaCoordinates(area.Coordinates),
				Emoji:        area.WeatherEmoji,
				TemperatureC: area.TemperatureC,
				Color:        area.Color,
			})
		case domain.StoryMediaAreaChannelPost:
			if area.ChannelID <= 0 || area.MsgID <= 0 {
				continue
			}
			out = append(out, &tg.MediaAreaChannelPost{
				Coordinates: tgStoryMediaAreaCoordinates(area.Coordinates),
				ChannelID:   area.ChannelID,
				MsgID:       area.MsgID,
			})
		case domain.StoryMediaAreaStarGift:
			if area.StarGiftSlug == "" {
				continue
			}
			out = append(out, &tg.MediaAreaStarGift{
				Coordinates: tgStoryMediaAreaCoordinates(area.Coordinates),
				Slug:        area.StarGiftSlug,
			})
		}
	}
	return out
}

func tgStoryMediaAreaCoordinates(in domain.StoryMediaAreaCoordinates) tg.MediaAreaCoordinates {
	out := tg.MediaAreaCoordinates{
		X:        in.X,
		Y:        in.Y,
		W:        in.W,
		H:        in.H,
		Rotation: in.Rotation,
	}
	if in.HasRadius {
		out.SetRadius(in.Radius)
	}
	return out
}

func tgStoryGeoPointAddress(in *domain.StoryGeoPointAddress) *tg.GeoPointAddress {
	if in == nil || in.CountryISO2 == "" {
		return nil
	}
	out := &tg.GeoPointAddress{CountryISO2: in.CountryISO2}
	if in.State != "" {
		out.SetState(in.State)
	}
	if in.City != "" {
		out.SetCity(in.City)
	}
	if in.Street != "" {
		out.SetStreet(in.Street)
	}
	return out
}

func tgStoryViews(views domain.StoryViews) tg.StoryViews {
	out := tg.StoryViews{
		HasViewers: views.HasViewers,
		ViewsCount: views.ViewsCount,
	}
	if views.ForwardsCount > 0 {
		out.SetForwardsCount(views.ForwardsCount)
	}
	if len(views.Reactions) > 0 {
		out.SetReactions(tgStoryReactionCounts(views.Reactions))
	}
	if views.ReactionsCount > 0 {
		out.SetReactionsCount(views.ReactionsCount)
	}
	if len(views.RecentViewers) > 0 {
		out.SetRecentViewers(append([]int64(nil), views.RecentViewers...))
	}
	return out
}

func storyViewsEmpty(views domain.StoryViews) bool {
	return !views.HasViewers &&
		views.ViewsCount == 0 &&
		views.ForwardsCount == 0 &&
		views.ReactionsCount == 0 &&
		len(views.Reactions) == 0 &&
		len(views.RecentViewers) == 0
}

func tgStoryReactionCounts(in []domain.ChannelMessageReactionCount) []tg.ReactionCount {
	out := make([]tg.ReactionCount, 0, len(in))
	for _, count := range in {
		if count.Count <= 0 {
			continue
		}
		item := tg.ReactionCount{
			Reaction: tgMessageReaction(count.Reaction),
			Count:    count.Count,
		}
		if count.ChosenOrder > 0 {
			item.SetChosenOrder(count.ChosenOrder)
		}
		out = append(out, item)
	}
	return out
}

func tgRecentStories(in []domain.RecentStory) []tg.RecentStory {
	out := make([]tg.RecentStory, len(in))
	for i, story := range in {
		out[i] = tg.RecentStory{Live: story.Live}
		if story.MaxID > 0 {
			out[i].SetMaxID(story.MaxID)
		}
	}
	return out
}

func tgReadStoryUpdates(states []domain.StoryReadState, date int) tg.UpdatesClass {
	updates := make([]tg.UpdateClass, 0, len(states))
	for _, state := range states {
		if state.MaxReadID <= 0 {
			continue
		}
		peer := tgPeer(state.Peer)
		if peer == nil {
			continue
		}
		updates = append(updates, &tg.UpdateReadStories{Peer: peer, MaxID: state.MaxReadID})
	}
	return &tg.Updates{
		Updates: updates,
		Users:   []tg.UserClass{},
		Chats:   []tg.ChatClass{},
		Date:    date,
		Seq:     0,
	}
}

func tgStoryViewsList(viewerUserID int64, list domain.StoryViewList, users []domain.User) *tg.StoriesStoryViewsList {
	out := &tg.StoriesStoryViewsList{
		Count:          list.Count,
		ViewsCount:     list.ViewsCount,
		ForwardsCount:  list.ForwardsCount,
		ReactionsCount: list.ReactionsCount,
		Views:          tgStoryViewItems(viewerUserID, list.Views),
		Chats:          []tg.ChatClass{},
		Users:          tgUsersForViewer(viewerUserID, users),
	}
	if list.NextOffset != "" {
		out.SetNextOffset(list.NextOffset)
	}
	return out
}

func tgStoryViewItems(viewerUserID int64, views []domain.StoryView) []tg.StoryViewClass {
	out := make([]tg.StoryViewClass, 0, len(views))
	for _, view := range views {
		if view.PublicForward != nil {
			msg := tgChannelMessage(viewerUserID, view.PublicForward.Message)
			if msg == nil {
				continue
			}
			out = append(out, &tg.StoryViewPublicForward{
				Blocked:              view.Blocked,
				BlockedMyStoriesFrom: view.BlockedMyStoriesFrom,
				Message:              msg,
			})
			continue
		}
		if view.Repost != nil {
			item := &tg.StoryViewPublicRepost{
				Blocked:              view.Blocked,
				BlockedMyStoriesFrom: view.BlockedMyStoriesFrom,
				PeerID:               tgPeer(view.Repost.Owner),
				Story:                tgStoryItem(*view.Repost),
			}
			out = append(out, item)
			continue
		}
		item := &tg.StoryView{
			Blocked:              view.Blocked,
			BlockedMyStoriesFrom: view.BlockedMyStoriesFrom,
			UserID:               view.ViewerID,
			Date:                 view.Date,
		}
		if view.Reaction != nil {
			if reaction := tgMessageReaction(*view.Reaction); reaction != nil {
				item.SetReaction(reaction)
			}
		}
		out = append(out, item)
	}
	return out
}

func tgStoryReactionsList(viewerUserID int64, list domain.StoryReactionList, users []domain.User) *tg.StoriesStoryReactionsList {
	out := &tg.StoriesStoryReactionsList{
		Count:     list.Count,
		Reactions: tgStoryReactionItems(viewerUserID, list.Reactions),
		Chats:     []tg.ChatClass{},
		Users:     tgUsersForViewer(viewerUserID, users),
	}
	if list.NextOffset != "" {
		out.SetNextOffset(list.NextOffset)
	}
	return out
}

func tgStoryReactionItems(viewerUserID int64, views []domain.StoryView) []tg.StoryReactionClass {
	out := make([]tg.StoryReactionClass, 0, len(views))
	for _, view := range views {
		if view.PublicForward != nil {
			msg := tgChannelMessage(viewerUserID, view.PublicForward.Message)
			if msg == nil {
				continue
			}
			out = append(out, &tg.StoryReactionPublicForward{Message: msg})
			continue
		}
		if view.Repost != nil {
			out = append(out, &tg.StoryReactionPublicRepost{
				PeerID: tgPeer(view.Repost.Owner),
				Story:  tgStoryItem(*view.Repost),
			})
			continue
		}
		if view.Reaction == nil {
			continue
		}
		reaction := tgMessageReaction(*view.Reaction)
		if reaction == nil {
			continue
		}
		out = append(out, &tg.StoryReaction{
			PeerID:   &tg.PeerUser{UserID: view.ViewerID},
			Date:     view.Date,
			Reaction: reaction,
		})
	}
	return out
}
