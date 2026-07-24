package moderation

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"time"

	"telesrv/internal/domain"
)

type privateMessageReader interface {
	GetMessages(ctx context.Context, userID int64, ids []int) (domain.MessageList, error)
	GetMessageReactions(ctx context.Context, userID int64, req domain.PrivateMessageReactionsRequest) (domain.PrivateMessageReactionsResult, error)
}

type channelMessageReader interface {
	GetMessages(ctx context.Context, userID, channelID int64, ids []int) (domain.ChannelHistory, error)
	FindMessageReaction(ctx context.Context, userID int64, req domain.ChannelMessageReactionLookupRequest) (domain.ChannelMessageReactionLookup, bool, error)
}

type storyReader interface {
	GetStoriesByID(ctx context.Context, viewerUserID int64, peer domain.Peer, ids []int, now int) (domain.StoryList, error)
}

type userReader interface {
	ByID(ctx context.Context, viewerUserID, userID int64) (domain.User, bool, error)
}

type channelPeerReader interface {
	ResolveChannel(ctx context.Context, viewerUserID, channelID int64) (domain.ChannelView, error)
}

type profilePhotoReader interface {
	GetProfilePhotos(ctx context.Context, ownerType domain.PeerType, ownerID int64, offset, limit int, maxID int64) ([]domain.Photo, int, error)
}

func (s *Service) ReportMessages(ctx context.Context, req domain.ModerationMessageReportRequest) (domain.ModerationReport, bool, error) {
	ids, err := canonicalPositiveIDs(req.MessageIDs, domain.MaxMessageBoxID)
	if err != nil || req.ReporterUserID <= 0 || req.Target.ID <= 0 {
		return domain.ModerationReport{}, false, domain.ErrModerationReportInvalid
	}
	items := make([]domain.ModerationReportItem, 0, len(ids))
	holds := make([]domain.ModerationMediaHold, 0)
	switch req.Target.Type {
	case domain.PeerTypeUser:
		if s == nil || s.privateMessages == nil {
			return domain.ModerationReport{}, false, fmt.Errorf("moderation private message reader is not configured")
		}
		list, err := s.privateMessages.GetMessages(ctx, req.ReporterUserID, ids)
		if err != nil {
			return domain.ModerationReport{}, false, err
		}
		byID := make(map[int]domain.Message, len(list.Messages))
		for _, message := range list.Messages {
			if message.Peer == req.Target {
				byID[message.ID] = message
			}
		}
		for _, id := range ids {
			message, found := byID[id]
			if !found {
				return domain.ModerationReport{}, false, domain.ErrModerationEvidenceNotFound
			}
			evidence, err := json.Marshal(privateMessageEvidence(message))
			if err != nil {
				return domain.ModerationReport{}, false, fmt.Errorf("marshal private message evidence: %w", err)
			}
			items = append(items, domain.ModerationReportItem{
				Kind: domain.ModerationItemMessage, Peer: req.Target,
				ItemID: int64(message.ID), AuthorUserID: message.From.ID,
				EvidenceSchemaVersion: 1, Evidence: evidence,
			})
			holds = append(holds, mediaHolds(len(items)-1, message.Media)...)
		}
	case domain.PeerTypeChannel:
		if s == nil || s.channelMessages == nil {
			return domain.ModerationReport{}, false, fmt.Errorf("moderation channel message reader is not configured")
		}
		history, err := s.channelMessages.GetMessages(ctx, req.ReporterUserID, req.Target.ID, ids)
		if err != nil {
			return domain.ModerationReport{}, false, err
		}
		byID := make(map[int]domain.ChannelMessage, len(history.Messages))
		for _, message := range history.Messages {
			if message.ChannelID == req.Target.ID && !message.Deleted {
				byID[message.ID] = message
			}
		}
		for _, id := range ids {
			message, found := byID[id]
			if !found {
				return domain.ModerationReport{}, false, domain.ErrModerationEvidenceNotFound
			}
			evidence, err := marshalChannelMessageEvidence(message)
			if err != nil {
				return domain.ModerationReport{}, false, fmt.Errorf("marshal channel message evidence: %w", err)
			}
			items = append(items, domain.ModerationReportItem{
				Kind: domain.ModerationItemMessage, Peer: req.Target,
				ItemID: int64(message.ID), AuthorUserID: message.SenderUserID,
				EvidenceSchemaVersion: 1, Evidence: evidence,
			})
			holds = append(holds, mediaHolds(len(items)-1, message.Media)...)
		}
	default:
		return domain.ModerationReport{}, false, domain.ErrModerationReportInvalid
	}
	return s.AcceptReport(ctx, domain.ModerationReportDraft{
		ReporterUserID: req.ReporterUserID,
		Source:         domain.ModerationSourceMessages,
		Target:         req.Target,
		Reason:         req.Reason,
		Option:         req.Option,
		Comment:        req.Comment,
		Items:          items,
		MediaHolds:     dedupeMediaHolds(holds),
		CreatedAt:      req.CreatedAt,
	})
}

func (s *Service) ReportPeer(ctx context.Context, reporterUserID int64, source domain.ModerationReportSource, target domain.Peer, reason domain.ModerationReason, option, comment string, createdAt time.Time) (domain.ModerationReport, bool, error) {
	if source != domain.ModerationSourceAccountPeer && source != domain.ModerationSourceMessagesSpam {
		return domain.ModerationReport{}, false, domain.ErrModerationReportInvalid
	}
	snapshot := peerEvidenceV1{SchemaVersion: 1, Target: target}
	switch target.Type {
	case domain.PeerTypeUser:
		if s == nil || s.users == nil {
			return domain.ModerationReport{}, false, fmt.Errorf("moderation user reader is not configured")
		}
		user, found, err := s.users.ByID(ctx, reporterUserID, target.ID)
		if err != nil {
			return domain.ModerationReport{}, false, err
		}
		if !found || user.Deleted {
			return domain.ModerationReport{}, false, domain.ErrModerationEvidenceNotFound
		}
		snapshot.User = &peerUserEvidenceV1{
			ID: user.ID, FirstName: user.FirstName, LastName: user.LastName,
			Username: user.Username, About: user.About, Bot: user.Bot,
			Verified: user.Verified, Scam: user.Scam, Fake: user.Fake,
			PhotoID: user.PhotoID,
		}
	case domain.PeerTypeChannel:
		if s == nil || s.channels == nil {
			return domain.ModerationReport{}, false, fmt.Errorf("moderation channel reader is not configured")
		}
		view, err := s.channels.ResolveChannel(ctx, reporterUserID, target.ID)
		if err != nil {
			return domain.ModerationReport{}, false, err
		}
		channel := view.Channel
		snapshot.Channel = &peerChannelEvidenceV1{
			ID: channel.ID, Title: channel.Title, About: channel.About,
			Username: channel.Username, Broadcast: channel.Broadcast,
			Megagroup: channel.Megagroup, Verified: channel.Verified,
			Scam: channel.Scam, Fake: channel.Fake, PhotoID: channel.PhotoID,
		}
	default:
		return domain.ModerationReport{}, false, domain.ErrModerationReportInvalid
	}
	evidence, err := json.Marshal(snapshot)
	if err != nil {
		return domain.ModerationReport{}, false, fmt.Errorf("marshal peer evidence: %w", err)
	}
	authorUserID := int64(0)
	if target.Type == domain.PeerTypeUser {
		authorUserID = target.ID
	}
	return s.AcceptReport(ctx, domain.ModerationReportDraft{
		ReporterUserID: reporterUserID, Source: source, Target: target,
		Reason: reason, Option: option, Comment: comment,
		Items: []domain.ModerationReportItem{{
			Kind: domain.ModerationItemPeer, Peer: target, ItemID: target.ID,
			AuthorUserID: authorUserID, EvidenceSchemaVersion: 1,
			Evidence: evidence,
		}},
		CreatedAt: createdAt,
	})
}

func (s *Service) ReportProfilePhoto(ctx context.Context, req domain.ModerationProfilePhotoReportRequest) (domain.ModerationReport, bool, error) {
	if req.ReporterUserID <= 0 || req.Target.ID <= 0 || req.PhotoID <= 0 ||
		!req.Reason.Valid() || s == nil || s.photos == nil {
		return domain.ModerationReport{}, false, domain.ErrModerationReportInvalid
	}
	photos, _, err := s.photos.GetProfilePhotos(ctx, req.Target.Type, req.Target.ID, -1, 1, req.PhotoID)
	if err != nil {
		return domain.ModerationReport{}, false, err
	}
	if len(photos) != 1 || photos[0].ID != req.PhotoID ||
		photos[0].AccessHash != req.AccessHash {
		return domain.ModerationReport{}, false, domain.ErrModerationEvidenceNotFound
	}
	photo := photos[0]
	if len(req.FileReference) > 0 && len(photo.FileReference) > 0 &&
		!bytes.Equal(req.FileReference, photo.FileReference) {
		return domain.ModerationReport{}, false, domain.ErrModerationEvidenceNotFound
	}
	evidence, err := json.Marshal(profilePhotoEvidenceV1{
		SchemaVersion: 1, Owner: req.Target, Photo: photo,
	})
	if err != nil {
		return domain.ModerationReport{}, false, fmt.Errorf("marshal profile photo evidence: %w", err)
	}
	authorUserID := int64(0)
	if req.Target.Type == domain.PeerTypeUser {
		authorUserID = req.Target.ID
	}
	return s.AcceptReport(ctx, domain.ModerationReportDraft{
		ReporterUserID: req.ReporterUserID, Source: domain.ModerationSourceProfilePhoto,
		Target: req.Target, Reason: req.Reason, Option: string(req.Reason),
		Comment: req.Comment, CreatedAt: req.CreatedAt,
		Items: []domain.ModerationReportItem{{
			Kind: domain.ModerationItemProfilePhoto, Peer: req.Target,
			ItemID: req.PhotoID, AuthorUserID: authorUserID,
			EvidenceSchemaVersion: 1, Evidence: evidence,
		}},
		MediaHolds: photoHolds(0, photo),
	})
}

func (s *Service) ReportChannelSpam(ctx context.Context, req domain.ModerationChannelSpamReportRequest) (domain.ModerationReport, bool, error) {
	if req.ReporterUserID <= 0 || req.ChannelID <= 0 || req.ParticipantUserID <= 0 ||
		s == nil || s.channelMessages == nil {
		return domain.ModerationReport{}, false, domain.ErrModerationReportInvalid
	}
	ids, err := canonicalPositiveIDs(req.MessageIDs, domain.MaxMessageBoxID)
	if err != nil {
		return domain.ModerationReport{}, false, err
	}
	history, err := s.channelMessages.GetMessages(ctx, req.ReporterUserID, req.ChannelID, ids)
	if err != nil {
		return domain.ModerationReport{}, false, err
	}
	byID := make(map[int]domain.ChannelMessage, len(history.Messages))
	for _, message := range history.Messages {
		if message.ChannelID == req.ChannelID && !message.Deleted {
			byID[message.ID] = message
		}
	}
	items := make([]domain.ModerationReportItem, 0, len(ids))
	holds := make([]domain.ModerationMediaHold, 0)
	target := domain.Peer{Type: domain.PeerTypeChannel, ID: req.ChannelID}
	for _, id := range ids {
		message, found := byID[id]
		if !found || message.SenderUserID != req.ParticipantUserID {
			return domain.ModerationReport{}, false, domain.ErrModerationEvidenceNotFound
		}
		evidence, err := marshalChannelMessageEvidence(message)
		if err != nil {
			return domain.ModerationReport{}, false, err
		}
		items = append(items, domain.ModerationReportItem{
			Kind: domain.ModerationItemMessage, Peer: target,
			ItemID: int64(message.ID), AuthorUserID: req.ParticipantUserID,
			EvidenceSchemaVersion: 1, Evidence: evidence,
		})
		holds = append(holds, mediaHolds(len(items)-1, message.Media)...)
	}
	return s.AcceptReport(ctx, domain.ModerationReportDraft{
		ReporterUserID: req.ReporterUserID, Source: domain.ModerationSourceChannelSpam,
		Target: target, Reason: domain.ModerationReasonSpam, Option: "spam",
		Items: items, MediaHolds: dedupeMediaHolds(holds), CreatedAt: req.CreatedAt,
	})
}

func (s *Service) ReportReaction(ctx context.Context, req domain.ModerationReactionReportRequest) (domain.ModerationReport, bool, error) {
	if req.ReporterUserID <= 0 || req.Target.ID <= 0 || req.MessageID <= 0 ||
		req.MessageID > domain.MaxMessageBoxID || req.ReactorUserID <= 0 {
		return domain.ModerationReport{}, false, domain.ErrModerationReportInvalid
	}
	var evidence []byte
	var err error
	switch req.Target.Type {
	case domain.PeerTypeUser:
		if s == nil || s.privateMessages == nil {
			return domain.ModerationReport{}, false, domain.ErrModerationReportInvalid
		}
		result, err := s.privateMessages.GetMessageReactions(ctx, req.ReporterUserID, domain.PrivateMessageReactionsRequest{
			OwnerUserID: req.ReporterUserID, Peer: req.Target, IDs: []int{req.MessageID},
		})
		if err != nil {
			return domain.ModerationReport{}, false, err
		}
		if len(result.Messages) != 1 || result.Messages[0].ID != req.MessageID ||
			result.Messages[0].Peer != req.Target {
			return domain.ModerationReport{}, false, domain.ErrModerationEvidenceNotFound
		}
		reactions := reactionRowsForUser(result.Messages[0].Reactions, req.ReactorUserID)
		if len(reactions) == 0 {
			return domain.ModerationReport{}, false, domain.ErrModerationEvidenceNotFound
		}
		evidence, err = json.Marshal(privateReactionEvidenceV1{
			SchemaVersion: 1, Message: privateMessageEvidence(result.Messages[0]),
			ReactorUserID: req.ReactorUserID, Reactions: reactionEvidenceRows(reactions),
		})
	case domain.PeerTypeChannel:
		if s == nil || s.channelMessages == nil {
			return domain.ModerationReport{}, false, domain.ErrModerationReportInvalid
		}
		lookup, found, lookupErr := s.channelMessages.FindMessageReaction(ctx, req.ReporterUserID, domain.ChannelMessageReactionLookupRequest{
			ViewerUserID: req.ReporterUserID, ChannelID: req.Target.ID,
			MessageID: req.MessageID, ReactorUserID: req.ReactorUserID,
		})
		if lookupErr != nil {
			return domain.ModerationReport{}, false, lookupErr
		}
		if !found {
			return domain.ModerationReport{}, false, domain.ErrModerationEvidenceNotFound
		}
		evidence, err = json.Marshal(channelReactionEvidenceV1{
			SchemaVersion: 1, Message: channelMessageEvidence(lookup.Message),
			ReactorUserID: req.ReactorUserID, Reactions: reactionEvidenceRows(lookup.Reactions),
		})
	default:
		return domain.ModerationReport{}, false, domain.ErrModerationReportInvalid
	}
	if err != nil {
		return domain.ModerationReport{}, false, fmt.Errorf("marshal reaction evidence: %w", err)
	}
	return s.AcceptReport(ctx, domain.ModerationReportDraft{
		ReporterUserID: req.ReporterUserID, Source: domain.ModerationSourceReaction,
		Target: req.Target, Reason: domain.ModerationReasonOther,
		Option: "reaction", CreatedAt: req.CreatedAt,
		Items: []domain.ModerationReportItem{{
			Kind: domain.ModerationItemReaction, Peer: req.Target,
			ItemID: int64(req.MessageID), SecondaryID: req.ReactorUserID,
			AuthorUserID: req.ReactorUserID, EvidenceSchemaVersion: 1,
			Evidence: evidence,
		}},
	})
}

func (s *Service) ReportEncryptedSpam(ctx context.Context, reporterUserID int64, chat domain.SecretChat, createdAt time.Time) (domain.ModerationReport, bool, error) {
	if reporterUserID <= 0 || !chat.HasParticipant(reporterUserID) || chat.ID <= 0 {
		return domain.ModerationReport{}, false, domain.ErrModerationPermissionDenied
	}
	offenderUserID := chat.PeerOf(reporterUserID)
	if offenderUserID <= 0 {
		return domain.ModerationReport{}, false, domain.ErrModerationEvidenceNotFound
	}
	target := domain.Peer{Type: domain.PeerTypeUser, ID: offenderUserID}
	evidence, err := json.Marshal(encryptedChatEvidenceV1{
		SchemaVersion: 1, ChatID: chat.ID, State: chat.State,
		AdminUserID: chat.AdminUserID, ParticipantUserID: chat.ParticipantUserID,
		Date: chat.Date,
	})
	if err != nil {
		return domain.ModerationReport{}, false, fmt.Errorf("marshal encrypted chat evidence: %w", err)
	}
	return s.AcceptReport(ctx, domain.ModerationReportDraft{
		ReporterUserID: reporterUserID, Source: domain.ModerationSourceEncryptedSpam,
		Target: target, Reason: domain.ModerationReasonSpam, Option: "spam",
		CreatedAt: createdAt,
		Items: []domain.ModerationReportItem{{
			Kind: domain.ModerationItemEncryptedChat, Peer: target,
			ItemID: int64(chat.ID), AuthorUserID: offenderUserID,
			EvidenceSchemaVersion: 1, Evidence: evidence,
		}},
	})
}

func (s *Service) ReportStories(ctx context.Context, req domain.ModerationStoryReportRequest) (domain.ModerationReport, bool, error) {
	ids, err := canonicalPositiveIDs(req.StoryIDs, domain.MaxStoryID)
	if err != nil || req.ReporterUserID <= 0 || req.Target.ID <= 0 {
		return domain.ModerationReport{}, false, domain.ErrModerationReportInvalid
	}
	if s == nil || s.stories == nil {
		return domain.ModerationReport{}, false, fmt.Errorf("moderation story reader is not configured")
	}
	list, err := s.stories.GetStoriesByID(ctx, req.ReporterUserID, req.Target, ids, int(req.CreatedAt.Unix()))
	if err != nil {
		return domain.ModerationReport{}, false, err
	}
	byID := make(map[int]domain.Story, len(list.Stories))
	for _, story := range list.Stories {
		if story.Owner == req.Target && !story.Deleted {
			byID[story.ID] = story
		}
	}
	items := make([]domain.ModerationReportItem, 0, len(ids))
	holds := make([]domain.ModerationMediaHold, 0)
	for _, id := range ids {
		story, found := byID[id]
		if !found {
			return domain.ModerationReport{}, false, domain.ErrModerationEvidenceNotFound
		}
		evidence, err := json.Marshal(storyEvidenceV1{
			SchemaVersion: 1, Owner: story.Owner, StoryID: story.ID,
			Date: story.Date, ExpireDate: story.ExpireDate, Pinned: story.Pinned,
			Public: story.Public, CloseFriends: story.CloseFriends,
			Contacts: story.Contacts, SelectedContacts: story.SelectedContacts,
			NoForwards: story.NoForwards, Edited: story.Edited,
			Caption: story.Caption, Entities: story.Entities, Media: story.Media,
			MediaAreas: story.MediaAreas, Forward: story.Forward,
		})
		if err != nil {
			return domain.ModerationReport{}, false, fmt.Errorf("marshal story evidence: %w", err)
		}
		authorUserID := int64(0)
		if story.Owner.Type == domain.PeerTypeUser {
			authorUserID = story.Owner.ID
		}
		items = append(items, domain.ModerationReportItem{
			Kind: domain.ModerationItemStory, Peer: story.Owner,
			ItemID: int64(story.ID), AuthorUserID: authorUserID,
			EvidenceSchemaVersion: 1, Evidence: evidence,
		})
		holds = append(holds, mediaHolds(len(items)-1, story.Media)...)
	}
	return s.AcceptReport(ctx, domain.ModerationReportDraft{
		ReporterUserID: req.ReporterUserID, Source: domain.ModerationSourceStory,
		Target: req.Target, Reason: req.Reason, Option: req.Option,
		Comment: req.Comment, Items: items,
		MediaHolds: dedupeMediaHolds(holds), CreatedAt: req.CreatedAt,
	})
}

func (s *Service) ReportEphemeral(ctx context.Context, reporterUserID int64, target domain.EphemeralMessage, reason domain.ModerationReason, option, comment string, createdAt time.Time) (domain.ModerationReport, bool, error) {
	legacy := domain.NewEphemeralAbuseReport(reporterUserID, option, comment, target, createdAt)
	if err := legacy.Validate(); err != nil {
		return domain.ModerationReport{}, false, err
	}
	evidence, err := json.Marshal(struct {
		SchemaVersion int                            `json:"schema_version"`
		Evidence      domain.EphemeralReportEvidence `json:"evidence"`
	}{SchemaVersion: 1, Evidence: legacy.Evidence})
	if err != nil {
		return domain.ModerationReport{}, false, fmt.Errorf("marshal ephemeral report evidence: %w", err)
	}
	holds := mediaHolds(0, target.Content.Media)
	return s.AcceptReport(ctx, domain.ModerationReportDraft{
		ReporterUserID: reporterUserID, Source: domain.ModerationSourceEphemeral,
		Target: target.Peer, Reason: reason, Option: option, Comment: comment,
		Items: []domain.ModerationReportItem{{
			Kind: domain.ModerationItemEphemeral, Peer: target.Peer,
			ItemID: int64(target.ID), AuthorUserID: target.SenderUserID,
			EvidenceSchemaVersion: 1, Evidence: evidence,
		}},
		MediaHolds: holds, CreatedAt: createdAt,
	})
}

type peerEvidenceV1 struct {
	SchemaVersion int                    `json:"schema_version"`
	Target        domain.Peer            `json:"target"`
	User          *peerUserEvidenceV1    `json:"user,omitempty"`
	Channel       *peerChannelEvidenceV1 `json:"channel,omitempty"`
}

type peerUserEvidenceV1 struct {
	ID        int64  `json:"id"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
	Username  string `json:"username"`
	About     string `json:"about"`
	Bot       bool   `json:"bot,omitempty"`
	Verified  bool   `json:"verified,omitempty"`
	Scam      bool   `json:"scam,omitempty"`
	Fake      bool   `json:"fake,omitempty"`
	PhotoID   int64  `json:"photo_id,omitempty"`
}

type peerChannelEvidenceV1 struct {
	ID        int64  `json:"id"`
	Title     string `json:"title"`
	About     string `json:"about"`
	Username  string `json:"username"`
	Broadcast bool   `json:"broadcast,omitempty"`
	Megagroup bool   `json:"megagroup,omitempty"`
	Verified  bool   `json:"verified,omitempty"`
	Scam      bool   `json:"scam,omitempty"`
	Fake      bool   `json:"fake,omitempty"`
	PhotoID   int64  `json:"photo_id,omitempty"`
}

type profilePhotoEvidenceV1 struct {
	SchemaVersion int          `json:"schema_version"`
	Owner         domain.Peer  `json:"owner"`
	Photo         domain.Photo `json:"photo"`
}

type privateReactionEvidenceV1 struct {
	SchemaVersion int                         `json:"schema_version"`
	Message       privateMessageEvidenceV1    `json:"message"`
	ReactorUserID int64                       `json:"reactor_user_id"`
	Reactions     []messageReactionEvidenceV1 `json:"reactions"`
}

type channelReactionEvidenceV1 struct {
	SchemaVersion int                         `json:"schema_version"`
	Message       channelMessageEvidenceV1    `json:"message"`
	ReactorUserID int64                       `json:"reactor_user_id"`
	Reactions     []messageReactionEvidenceV1 `json:"reactions"`
}

type messageReactionEvidenceV1 struct {
	UserID      int64                      `json:"user_id"`
	Type        domain.MessageReactionType `json:"type"`
	Value       string                     `json:"value"`
	Big         bool                       `json:"big,omitempty"`
	Unread      bool                       `json:"unread,omitempty"`
	ChosenOrder int                        `json:"chosen_order,omitempty"`
	Date        int                        `json:"date"`
}

type encryptedChatEvidenceV1 struct {
	SchemaVersion     int                    `json:"schema_version"`
	ChatID            int                    `json:"chat_id"`
	State             domain.SecretChatState `json:"state"`
	AdminUserID       int64                  `json:"admin_user_id"`
	ParticipantUserID int64                  `json:"participant_user_id"`
	Date              int                    `json:"date"`
}

type privateMessageEvidenceV1 struct {
	SchemaVersion int                             `json:"schema_version"`
	MessageID     int                             `json:"message_id"`
	UID           int64                           `json:"uid"`
	Peer          domain.Peer                     `json:"peer"`
	From          domain.Peer                     `json:"from"`
	Date          int                             `json:"date"`
	EditDate      int                             `json:"edit_date,omitempty"`
	Body          string                          `json:"body"`
	Entities      []domain.MessageEntity          `json:"entities,omitempty"`
	ReplyTo       *domain.MessageReply            `json:"reply_to,omitempty"`
	Forward       *domain.MessageForward          `json:"forward,omitempty"`
	Reactions     *domain.ChannelMessageReactions `json:"reactions,omitempty"`
	Media         *domain.MessageMedia            `json:"media,omitempty"`
	RichMessage   *domain.MessageRichMessage      `json:"rich_message,omitempty"`
	GroupedID     int64                           `json:"grouped_id,omitempty"`
}

type channelMessageEvidenceV1 struct {
	SchemaVersion int                             `json:"schema_version"`
	ChannelID     int64                           `json:"channel_id"`
	MessageID     int                             `json:"message_id"`
	SenderUserID  int64                           `json:"sender_user_id"`
	From          domain.Peer                     `json:"from"`
	SendAs        *domain.Peer                    `json:"send_as,omitempty"`
	Date          int                             `json:"date"`
	EditDate      int                             `json:"edit_date,omitempty"`
	Post          bool                            `json:"post,omitempty"`
	Body          string                          `json:"body"`
	Entities      []domain.MessageEntity          `json:"entities,omitempty"`
	ReplyTo       *domain.MessageReply            `json:"reply_to,omitempty"`
	Forward       *domain.MessageForward          `json:"forward,omitempty"`
	Reactions     *domain.ChannelMessageReactions `json:"reactions,omitempty"`
	Action        *domain.ChannelMessageAction    `json:"action,omitempty"`
	Media         *domain.MessageMedia            `json:"media,omitempty"`
	RichMessage   *domain.MessageRichMessage      `json:"rich_message,omitempty"`
	GroupedID     int64                           `json:"grouped_id,omitempty"`
}

type storyEvidenceV1 struct {
	SchemaVersion    int                     `json:"schema_version"`
	Owner            domain.Peer             `json:"owner"`
	StoryID          int                     `json:"story_id"`
	Date             int                     `json:"date"`
	ExpireDate       int                     `json:"expire_date"`
	Pinned           bool                    `json:"pinned,omitempty"`
	Public           bool                    `json:"public,omitempty"`
	CloseFriends     bool                    `json:"close_friends,omitempty"`
	Contacts         bool                    `json:"contacts,omitempty"`
	SelectedContacts bool                    `json:"selected_contacts,omitempty"`
	NoForwards       bool                    `json:"no_forwards,omitempty"`
	Edited           bool                    `json:"edited,omitempty"`
	Caption          string                  `json:"caption"`
	Entities         []domain.MessageEntity  `json:"entities,omitempty"`
	Media            *domain.MessageMedia    `json:"media,omitempty"`
	MediaAreas       []domain.StoryMediaArea `json:"media_areas,omitempty"`
	Forward          *domain.StoryForward    `json:"forward,omitempty"`
}

func privateMessageEvidence(message domain.Message) privateMessageEvidenceV1 {
	return privateMessageEvidenceV1{
		SchemaVersion: 1, MessageID: message.ID, UID: message.UID,
		Peer: message.Peer, From: message.From, Date: message.Date,
		EditDate: message.EditDate, Body: message.Body,
		Entities: message.Entities, ReplyTo: message.ReplyTo,
		Forward: message.Forward, Reactions: message.Reactions,
		Media: message.Media, RichMessage: message.RichMessage,
		GroupedID: message.GroupedID,
	}
}

func channelMessageEvidence(message domain.ChannelMessage) channelMessageEvidenceV1 {
	return channelMessageEvidenceV1{
		SchemaVersion: 1, ChannelID: message.ChannelID,
		MessageID: message.ID, SenderUserID: message.SenderUserID,
		From: message.From, SendAs: message.SendAs, Date: message.Date,
		EditDate: message.EditDate, Post: message.Post, Body: message.Body,
		Entities: message.Entities, ReplyTo: message.ReplyTo,
		Forward: message.Forward, Reactions: message.Reactions,
		Action: message.Action, Media: message.Media,
		RichMessage: message.RichMessage, GroupedID: message.GroupedID,
	}
}

func marshalChannelMessageEvidence(message domain.ChannelMessage) ([]byte, error) {
	evidence, err := json.Marshal(channelMessageEvidence(message))
	if err != nil {
		return nil, fmt.Errorf("marshal channel message evidence: %w", err)
	}
	return evidence, nil
}

func reactionRowsForUser(reactions *domain.ChannelMessageReactions, userID int64) []domain.ChannelMessagePeerReaction {
	if reactions == nil || userID <= 0 {
		return nil
	}
	rows := make([]domain.ChannelMessagePeerReaction, 0, len(reactions.Recent))
	for _, reaction := range reactions.Recent {
		if reaction.UserID == userID {
			rows = append(rows, reaction)
		}
	}
	return rows
}

func reactionEvidenceRows(rows []domain.ChannelMessagePeerReaction) []messageReactionEvidenceV1 {
	out := make([]messageReactionEvidenceV1, 0, len(rows))
	for _, row := range rows {
		out = append(out, messageReactionEvidenceV1{
			UserID: row.UserID, Type: row.Reaction.Type,
			Value: row.Reaction.Value(), Big: row.Big, Unread: row.Unread,
			ChosenOrder: row.ChosenOrder, Date: row.Date,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ChosenOrder != out[j].ChosenOrder {
			return out[i].ChosenOrder < out[j].ChosenOrder
		}
		if out[i].Type != out[j].Type {
			return out[i].Type < out[j].Type
		}
		return out[i].Value < out[j].Value
	})
	return out
}

func canonicalPositiveIDs(ids []int, max int) ([]int, error) {
	if len(ids) == 0 || len(ids) > domain.MaxModerationReportItems {
		return nil, domain.ErrModerationReportInvalid
	}
	seen := make(map[int]struct{}, len(ids))
	out := make([]int, 0, len(ids))
	for _, id := range ids {
		if id <= 0 || id > max {
			return nil, domain.ErrModerationReportInvalid
		}
		if _, duplicate := seen[id]; !duplicate {
			seen[id] = struct{}{}
			out = append(out, id)
		}
	}
	sort.Ints(out)
	return out, nil
}

func mediaHolds(itemIndex int, media *domain.MessageMedia) []domain.ModerationMediaHold {
	if media == nil {
		return nil
	}
	holds := make([]domain.ModerationMediaHold, 0, 8)
	addPhoto := func(photo *domain.Photo) {
		if photo == nil || photo.ID <= 0 {
			return
		}
		for _, size := range photo.Sizes {
			if size.Type != "" {
				holds = append(holds, domain.ModerationMediaHold{
					ItemIndex: itemIndex, Kind: domain.ModerationMediaPhoto,
					StorageKey: "photo:" + strconv.FormatInt(photo.ID, 10) + ":" + size.Type,
				})
			}
		}
	}
	addDocument := func(document *domain.Document) {
		if document == nil || document.ID <= 0 {
			return
		}
		prefix := "doc:" + strconv.FormatInt(document.ID, 10)
		holds = append(holds, domain.ModerationMediaHold{
			ItemIndex: itemIndex, Kind: domain.ModerationMediaDocument,
			StorageKey: prefix,
		})
		for _, thumb := range document.Thumbs {
			if thumb.Type != "" {
				holds = append(holds, domain.ModerationMediaHold{
					ItemIndex: itemIndex, Kind: domain.ModerationMediaDocument,
					StorageKey: prefix + ":" + thumb.Type,
				})
			}
		}
	}
	addPhoto(media.Photo)
	addDocument(media.Document)
	addDocument(media.LivePhotoVideo)
	return dedupeMediaHolds(holds)
}

func photoHolds(itemIndex int, photo domain.Photo) []domain.ModerationMediaHold {
	if photo.ID <= 0 {
		return nil
	}
	holds := make([]domain.ModerationMediaHold, 0, len(photo.Sizes))
	for _, size := range photo.Sizes {
		if size.Type == "" {
			continue
		}
		holds = append(holds, domain.ModerationMediaHold{
			ItemIndex: itemIndex, Kind: domain.ModerationMediaPhoto,
			StorageKey: "photo:" + strconv.FormatInt(photo.ID, 10) + ":" + size.Type,
		})
	}
	return dedupeMediaHolds(holds)
}

func dedupeMediaHolds(holds []domain.ModerationMediaHold) []domain.ModerationMediaHold {
	if len(holds) == 0 {
		return nil
	}
	seen := make(map[domain.ModerationMediaHold]struct{}, len(holds))
	out := make([]domain.ModerationMediaHold, 0, len(holds))
	for _, hold := range holds {
		if _, duplicate := seen[hold]; duplicate {
			continue
		}
		seen[hold] = struct{}{}
		out = append(out, hold)
	}
	return out
}
