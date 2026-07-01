package domain

import "strings"

// ChannelBannedRightsBlockMessage reports whether a non-admin channel member is
// blocked by fine-grained chatBannedRights for this concrete message payload.
func ChannelBannedRightsBlockMessage(req SendChannelMessageRequest, channel Channel, member ChannelMember, selfBoostsApplied int) bool {
	if req.Action != nil || channelBannedRightsBypassed(channel, member) {
		return false
	}
	if req.Media.IsZero() {
		if strings.TrimSpace(req.Message) == "" {
			return false
		}
		return channelBannedRightsBlockWithBoost(channel, member.BannedRights.SendPlain, channel.DefaultBannedRights.SendPlain, selfBoostsApplied)
	}
	return ChannelBannedRightsBlockMedia(channel, member, req.Media, selfBoostsApplied)
}

// ChannelBannedRightsBlockMedia applies both legacy send_media and modern
// per-media banned rights to one message media payload.
func ChannelBannedRightsBlockMedia(channel Channel, member ChannelMember, media *MessageMedia, selfBoostsApplied int) bool {
	if media.IsZero() || channelBannedRightsBypassed(channel, member) {
		return false
	}
	return channelBannedRightsBlockWithBoost(
		channel,
		channelBannedRightsBlockMediaKind(member.BannedRights, media),
		channelBannedRightsBlockMediaKind(channel.DefaultBannedRights, media),
		selfBoostsApplied,
	)
}

// ChannelBannedRightsBlockReactions applies send_reactions. Empty reaction
// vectors are handled by callers as removals and should remain allowed.
func ChannelBannedRightsBlockReactions(channel Channel, member ChannelMember, selfBoostsApplied int) bool {
	if channelBannedRightsBypassed(channel, member) {
		return false
	}
	return channelBannedRightsBlockWithBoost(channel, member.BannedRights.SendReactions, channel.DefaultBannedRights.SendReactions, selfBoostsApplied)
}

// ChannelBannedRightsBlockManageTopics applies manage_topics to topic create,
// edit, and closed-topic reply bypasses.
func ChannelBannedRightsBlockManageTopics(channel Channel, member ChannelMember, selfBoostsApplied int) bool {
	if channelBannedRightsBypassed(channel, member) {
		return false
	}
	return channelBannedRightsBlockWithBoost(channel, member.BannedRights.ManageTopics, channel.DefaultBannedRights.ManageTopics, selfBoostsApplied)
}

func channelBannedRightsBypassed(channel Channel, member ChannelMember) bool {
	return channel.Broadcast || member.Role == ChannelRoleCreator || member.Role == ChannelRoleAdmin
}

func channelBannedRightsBlockWithBoost(channel Channel, memberBlocked, defaultBlocked bool, selfBoostsApplied int) bool {
	if memberBlocked {
		return true
	}
	if !defaultBlocked {
		return false
	}
	return channel.BoostsUnrestrict == 0 || selfBoostsApplied < channel.BoostsUnrestrict
}

func channelBannedRightsBlockMediaKind(rights ChannelBannedRights, media *MessageMedia) bool {
	if rights.SendMedia {
		return true
	}
	switch media.Kind {
	case MessageMediaKindPhoto:
		return rights.SendPhotos
	case MessageMediaKindDocument:
		return channelBannedRightsBlockDocument(rights, media)
	case MessageMediaKindPoll:
		return rights.SendPolls
	case MessageMediaKindDice:
		return rights.SendGames
	case MessageMediaKindWebPage:
		return rights.EmbedLinks
	default:
		return rights.SendMedia
	}
}

func channelBannedRightsBlockDocument(rights ChannelBannedRights, media *MessageMedia) bool {
	if media == nil || media.Document == nil {
		return rights.SendDocs
	}
	if media.Document.IsSticker() {
		return rights.SendStickers
	}
	if media.Document.IsGif() {
		return rights.SendGifs
	}
	if media.Round {
		return rights.SendRoundvideos
	}
	if media.Voice {
		return rights.SendVoices
	}
	if media.Video {
		return rights.SendVideos
	}
	if media.IsMusic() {
		return rights.SendAudios
	}
	return rights.SendDocs
}
