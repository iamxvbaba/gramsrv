package rpc

import (
	"strconv"
	"strings"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
	"telesrv/internal/push"
)

// pushTarget couples the built notification with the peer/category used for the
// mute check.
type pushTarget struct {
	notif    push.Notification
	peer     domain.Peer
	category domain.NotifyScopeKind
}

// buildPushTarget converts the viewer's already-built TL updates into a push
// notification target, returning false when the event is not a notifiable
// inbound message.
func buildPushTarget(userID int64, updates *tg.Updates) (pushTarget, bool) {
	if updates == nil {
		return pushTarget{}, false
	}
	for _, u := range updates.Updates {
		switch m := u.(type) {
		case *tg.UpdateNewMessage:
			if msg, ok := m.Message.(*tg.Message); ok {
				return messageTarget(userID, msg, updates)
			}
		case *tg.UpdateNewChannelMessage:
			if msg, ok := m.Message.(*tg.Message); ok {
				return messageTarget(userID, msg, updates)
			}
		}
	}
	return pushTarget{}, false
}

func messageTarget(userID int64, msg *tg.Message, updates *tg.Updates) (pushTarget, bool) {
	if msg == nil || msg.Out || msg.Silent {
		return pushTarget{}, false
	}
	custom := map[string]any{"msg_id": msg.ID}
	if msg.Mentioned {
		custom["mention"] = 1
	}
	switch peer := msg.PeerID.(type) {
	case *tg.PeerUser:
		senderID := peerUserID(msg.FromID)
		if senderID == 0 || senderID == userID {
			// Self-chat / Saved Messages must not notify the user.
			return pushTarget{}, false
		}
		name := userNameByID(updates, senderID)
		custom["from_id"] = senderID
		notif := composeMessage(push.Notification{}, "", []string{name}, msg, custom, userID)
		notif.Title = name
		notif.Description = pushDescription(msg, "")
		return pushTarget{
			notif:    notif,
			peer:     domain.Peer{Type: domain.PeerTypeUser, ID: senderID},
			category: domain.NotifyScopeUsers,
		}, true
	case *tg.PeerChannel:
		channelID := peer.ChannelID
		title, megagroup := channelByID(updates, channelID)
		custom["channel_id"] = channelID
		if megagroup {
			senderName := title
			if fromUserID := peerUserID(msg.FromID); fromUserID != 0 {
				senderName = userNameByID(updates, fromUserID)
				custom["chat_from_id"] = fromUserID
			} else if fromChannelID := peerChannelID(msg.FromID); fromChannelID != 0 {
				senderName = channelTitleByID(updates, fromChannelID)
				custom["chat_from_broadcast_id"] = fromChannelID
			}
			notif := composeMessage(push.Notification{}, "CHAT_", []string{senderName, title}, msg, custom, userID)
			notif.Title = title
			notif.Description = pushDescription(msg, senderName+": ")
			return pushTarget{
				notif:    notif,
				peer:     domain.Peer{Type: domain.PeerTypeChannel, ID: channelID},
				category: domain.NotifyScopeChats,
			}, true
		}
		notif := composeMessage(push.Notification{}, "CHANNEL_", []string{title}, msg, custom, userID)
		notif.Title = title
		notif.Description = pushDescription(msg, "")
		return pushTarget{
			notif:    notif,
			peer:     domain.Peer{Type: domain.PeerTypeChannel, ID: channelID},
			category: domain.NotifyScopeBroadcasts,
		}, true
	case *tg.PeerChat:
		chatID := peer.ChatID
		title, _ := channelByID(updates, chatID)
		custom["chat_id"] = chatID
		base := []string{title}
		senderName := title
		if senderID := peerUserID(msg.FromID); senderID != 0 {
			senderName = userNameByID(updates, senderID)
			base = []string{senderName, title}
		}
		notif := composeMessage(push.Notification{}, "CHAT_", base, msg, custom, userID)
		notif.Title = title
		notif.Description = pushDescription(msg, senderName+": ")
		return pushTarget{
			notif:    notif,
			peer:     domain.Peer{Type: domain.PeerTypeChannel, ID: chatID},
			category: domain.NotifyScopeChats,
		}, true
	}
	return pushTarget{}, false
}

// composeMessage picks the loc_key/args for a private (MESSAGE_), broadcast
// channel (CHANNEL_) or group (CHAT_) notification from the base (non-text)
// arguments.
func composeMessage(n push.Notification, prefix string, base []string, msg *tg.Message, custom map[string]any, userID int64) push.Notification {
	args := append([]string{}, base...)
	locKey := prefix + "MESSAGE_TEXT"
	if text := strings.TrimSpace(msg.Message); text != "" {
		args = append(args, text)
	} else if suffix := mediaLocSuffix(msg.Media); suffix != "" {
		locKey = prefix + "MESSAGE_" + suffix
	} else {
		locKey = prefix + "MESSAGE_NOTEXT"
	}
	n.LocKey = locKey
	n.LocArgs = args
	n.Custom = custom
	n.UserID = userID
	return n
}

// pushDescription is the human-readable body the web service worker renders
// directly (Android derives it from loc_key/loc_args). prefix, when set, is the
// "Sender: " group prefix.
func pushDescription(msg *tg.Message, prefix string) string {
	if text := strings.TrimSpace(msg.Message); text != "" {
		return prefix + text
	}
	switch mediaLocSuffix(msg.Media) {
	case "PHOTO":
		return prefix + "Photo"
	case "DOC":
		return prefix + "Document"
	case "CONTACT":
		return prefix + "Contact"
	case "POLL":
		return prefix + "Poll"
	case "GEO":
		return prefix + "Location"
	case "GEOLIVE":
		return prefix + "Live location"
	default:
		return prefix + "Message"
	}
}

func mediaLocSuffix(media tg.MessageMediaClass) string {
	switch media.(type) {
	case *tg.MessageMediaPhoto:
		return "PHOTO"
	case *tg.MessageMediaDocument:
		return "DOC"
	case *tg.MessageMediaContact:
		return "CONTACT"
	case *tg.MessageMediaPoll:
		return "POLL"
	case *tg.MessageMediaGeo, *tg.MessageMediaVenue:
		return "GEO"
	case *tg.MessageMediaGeoLive:
		return "GEOLIVE"
	default:
		return ""
	}
}

func peerUserID(p tg.PeerClass) int64 {
	if u, ok := p.(*tg.PeerUser); ok {
		return u.UserID
	}
	return 0
}

func peerChannelID(p tg.PeerClass) int64 {
	if c, ok := p.(*tg.PeerChannel); ok {
		return c.ChannelID
	}
	return 0
}

func userNameByID(updates *tg.Updates, id int64) string {
	for _, u := range updates.Users {
		if user, ok := u.(*tg.User); ok && user.ID == id {
			if name := strings.TrimSpace(user.FirstName + " " + user.LastName); name != "" {
				return name
			}
			if user.Username != "" {
				return user.Username
			}
		}
	}
	return strconv.FormatInt(id, 10)
}

func channelTitleByID(updates *tg.Updates, id int64) string {
	title, _ := channelByID(updates, id)
	if title == "" {
		return strconv.FormatInt(id, 10)
	}
	return title
}

func channelByID(updates *tg.Updates, id int64) (title string, megagroup bool) {
	for _, c := range updates.Chats {
		switch chat := c.(type) {
		case *tg.Channel:
			if chat.ID == id {
				return chat.Title, chat.Megagroup
			}
		case *tg.Chat:
			if chat.ID == id {
				return chat.Title, false
			}
		}
	}
	return "", false
}
