package rpc

import (
	"github.com/iamxvbaba/td/tg"
	"strings"
	apptelemetry "telesrv/internal/app/clienttelemetry"
	appmoderation "telesrv/internal/app/moderation"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
	"testing"
	"time"
)

func TestTDesktopPassiveChannelStubs(t *testing.T) {
	f := newRPCChannelFixture(t)
	r := f.router
	moderationReports := memory.NewModerationReportStore()
	telemetryEvents := memory.NewClientTelemetryStore()
	r.deps.ClientTelemetry = apptelemetry.NewService(telemetryEvents)
	r.deps.Moderation = appmoderation.NewService(
		moderationReports,
		appmoderation.WithMessageReaders(nil, r.deps.Channels),
		appmoderation.WithPeerReaders(r.deps.Users, r.deps.Channels),
	)
	owner := f.user(41, "15550002101", "Owner")
	friend := f.user(42, "15550002102", "Friend")
	invited := f.user(43, "15550002103", "Invited")
	ownerCtx := f.userCtx(owner)
	friendCtx := f.userCtx(friend)
	channel := f.createLegacyMegagroup(owner, "RPC Passive Group", friend)

	sendAs, err := r.onChannelsGetSendAs(ownerCtx, &tg.ChannelsGetSendAsRequest{
		Peer: inputPeerChannel(channel),
	})
	if err != nil {
		t.Fatalf("get send as: %v", err)
	}
	if len(sendAs.Peers) != 2 || len(sendAs.Users) != 1 || len(sendAs.Chats) != 1 {
		t.Fatalf("send as = %+v, want self + current channel peers with current channel context", sendAs)
	}
	if peer, ok := sendAs.Peers[0].Peer.(*tg.PeerUser); !ok || peer.UserID != owner.ID {
		t.Fatalf("send as peer = %#v, want owner user", sendAs.Peers[0].Peer)
	}
	if peer, ok := sendAs.Peers[1].Peer.(*tg.PeerChannel); !ok || peer.ChannelID != channel.ID {
		t.Fatalf("send as peer[1] = %#v, want current channel", sendAs.Peers[1].Peer)
	}
	friendSendAs, err := r.onChannelsGetSendAs(friendCtx, &tg.ChannelsGetSendAsRequest{
		Peer: inputPeerChannel(channel),
	})
	if err != nil {
		t.Fatalf("friend get send as: %v", err)
	}
	if len(friendSendAs.Peers) != 1 {
		t.Fatalf("friend send as peers = %+v, want only self", friendSendAs.Peers)
	}
	if ok, err := r.onMessagesSaveDefaultSendAs(ownerCtx, &tg.MessagesSaveDefaultSendAsRequest{
		Peer:   inputPeerChannel(channel),
		SendAs: &tg.InputPeerSelf{},
	}); err != nil || !ok {
		t.Fatalf("save default send as self = ok %v err %v, want true", ok, err)
	}
	if ok, err := r.onMessagesSaveDefaultSendAs(ownerCtx, &tg.MessagesSaveDefaultSendAsRequest{
		Peer:   inputPeerChannel(channel),
		SendAs: inputPeerChannel(channel),
	}); err != nil || !ok {
		t.Fatalf("save default send as channel = ok %v err %v, want true", ok, err)
	}
	fullWithDefault, err := r.onChannelsGetFullChannel(ownerCtx, inputChannel(channel))
	if err != nil {
		t.Fatalf("get full channel with default send as: %v", err)
	}
	channelFull := fullWithDefault.FullChat.(*tg.ChannelFull)
	defaultSendAs, ok := channelFull.GetDefaultSendAs()
	if !ok {
		t.Fatalf("full channel default_send_as missing")
	}
	if peer, ok := defaultSendAs.(*tg.PeerChannel); !ok || peer.ChannelID != channel.ID {
		t.Fatalf("full channel default_send_as = %#v, want current channel %d", defaultSendAs, channel.ID)
	}
	sentWithDefault, err := r.onMessagesSendMessage(ownerCtx, &tg.MessagesSendMessageRequest{
		Peer:     inputPeerChannel(channel),
		Message:  "default send as fallback",
		RandomID: 80102,
	})
	if err != nil {
		t.Fatalf("send message with default send as: %v", err)
	}
	defaultMsg := sentWithDefault.(*tg.Updates).Updates[1].(*tg.UpdateNewChannelMessage).Message.(*tg.Message)
	if from, ok := defaultMsg.FromID.(*tg.PeerChannel); !ok || from.ChannelID != channel.ID {
		t.Fatalf("default send_as message from = %#v, want channel %d", defaultMsg.FromID, channel.ID)
	}
	forwardedWithDefault, err := r.onMessagesForwardMessages(ownerCtx, &tg.MessagesForwardMessagesRequest{
		FromPeer: inputPeerChannel(channel),
		ToPeer:   inputPeerChannel(channel),
		ID:       []int{defaultMsg.ID},
		RandomID: []int64{80103},
	})
	if err != nil {
		t.Fatalf("forward with default send as: %v", err)
	}
	defaultForward := forwardedWithDefault.(*tg.Updates).Updates[1].(*tg.UpdateNewChannelMessage).Message.(*tg.Message)
	if from, ok := defaultForward.FromID.(*tg.PeerChannel); !ok || from.ChannelID != channel.ID {
		t.Fatalf("default forward send_as from = %#v, want channel %d", defaultForward.FromID, channel.ID)
	}
	if ok, err := r.onMessagesSaveDefaultSendAs(ownerCtx, &tg.MessagesSaveDefaultSendAsRequest{
		Peer:   inputPeerChannel(channel),
		SendAs: &tg.InputPeerSelf{},
	}); err != nil || !ok {
		t.Fatalf("clear default send as self = ok %v err %v, want true", ok, err)
	}
	fullAfterClear, err := r.onChannelsGetFullChannel(ownerCtx, inputChannel(channel))
	if err != nil {
		t.Fatalf("get full channel after clearing default send as: %v", err)
	}
	if _, ok := fullAfterClear.FullChat.(*tg.ChannelFull).GetDefaultSendAs(); ok {
		t.Fatalf("full channel default_send_as still set after saving self")
	}
	if _, err := r.onMessagesSaveDefaultSendAs(ownerCtx, &tg.MessagesSaveDefaultSendAsRequest{
		Peer:   inputPeerChannel(channel),
		SendAs: &tg.InputPeerUser{UserID: friend.ID, AccessHash: friend.AccessHash},
	}); err == nil || !strings.Contains(err.Error(), "SEND_AS_PEER_INVALID") {
		t.Fatalf("save default send as friend err = %v, want SEND_AS_PEER_INVALID", err)
	}

	sponsored, err := r.onMessagesGetSponsoredMessages(ownerCtx, &tg.MessagesGetSponsoredMessagesRequest{
		Peer: inputPeerChannel(channel),
	})
	if err != nil {
		t.Fatalf("get sponsored messages: %v", err)
	}
	if _, ok := sponsored.(*tg.MessagesSponsoredMessagesEmpty); !ok {
		t.Fatalf("sponsored = %T, want empty", sponsored)
	}
	historyTTL, err := r.onMessagesGetDefaultHistoryTTL(ownerCtx)
	if err != nil {
		t.Fatalf("get default history ttl: %v", err)
	}
	if historyTTL.Period != 0 {
		t.Fatalf("default history ttl = %+v, want disabled", historyTTL)
	}
	accountTTL, err := r.onAccountGetAccountTTL(ownerCtx)
	if err != nil {
		t.Fatalf("get account ttl: %v", err)
	}
	if accountTTL.Days <= 0 {
		t.Fatalf("account ttl = %+v, want positive days", accountTTL)
	}
	preview, err := r.onMessagesGetWebPagePreview(ownerCtx, &tg.MessagesGetWebPagePreviewRequest{
		Message: "https://example.com",
	})
	if err != nil {
		t.Fatalf("messages.getWebPagePreview: %v", err)
	}
	if _, ok := preview.Media.(*tg.MessageMediaEmpty); !ok || len(preview.Chats) != 0 || len(preview.Users) != 0 {
		t.Fatalf("messages.getWebPagePreview = %+v, want empty media preview", preview)
	}
	if _, err := r.onMessagesGetWebPagePreview(ownerCtx, &tg.MessagesGetWebPagePreviewRequest{}); err == nil || !strings.Contains(err.Error(), "MESSAGE_EMPTY") {
		t.Fatalf("messages.getWebPagePreview empty err = %v, want MESSAGE_EMPTY", err)
	}
	if media, err := r.onMessagesUploadMedia(ownerCtx, &tg.MessagesUploadMediaRequest{
		Peer:  inputPeerChannel(channel),
		Media: &tg.InputMediaEmpty{},
	}); err != nil {
		t.Fatalf("messages.uploadMedia empty: %v", err)
	} else if _, ok := media.(*tg.MessageMediaEmpty); !ok {
		t.Fatalf("messages.uploadMedia empty = %#v, want messageMediaEmpty", media)
	}
	if _, err := r.onMessagesUploadMedia(ownerCtx, &tg.MessagesUploadMediaRequest{
		Peer:  inputPeerChannel(channel),
		Media: &tg.InputMediaUploadedPhoto{},
	}); err == nil || !strings.Contains(err.Error(), "MEDIA_INVALID") {
		t.Fatalf("messages.uploadMedia unsupported err = %v, want MEDIA_INVALID", err)
	}
	if updates, err := r.onMessagesSendMedia(ownerCtx, &tg.MessagesSendMediaRequest{
		Peer:     inputPeerChannel(channel),
		Media:    &tg.InputMediaWebPage{URL: "https://example.com"},
		Message:  "https://example.com",
		RandomID: 91001,
	}); err != nil {
		t.Fatalf("messages.sendMedia webpage-as-text: %v", err)
	} else if len(updates.(*tg.Updates).Updates) == 0 {
		t.Fatalf("messages.sendMedia webpage-as-text = %+v, want channel message updates", updates)
	}
	if _, err := r.onMessagesSendMedia(ownerCtx, &tg.MessagesSendMediaRequest{
		Peer:     inputPeerChannel(channel),
		Media:    &tg.InputMediaUploadedPhoto{},
		Message:  "photo",
		RandomID: 91002,
	}); err == nil || !strings.Contains(err.Error(), "MEDIA_INVALID") {
		t.Fatalf("messages.sendMedia unsupported err = %v, want MEDIA_INVALID", err)
	}
	if _, err := r.onMessagesSendMultiMedia(ownerCtx, &tg.MessagesSendMultiMediaRequest{
		Peer: inputPeerChannel(channel),
		MultiMedia: []tg.InputSingleMedia{{
			Media:    &tg.InputMediaUploadedPhoto{},
			RandomID: 91003,
			Message:  "photo",
		}},
	}); err == nil || !strings.Contains(err.Error(), "MEDIA_INVALID") {
		t.Fatalf("messages.sendMultiMedia unsupported err = %v, want MEDIA_INVALID", err)
	}
	savedHistoryReq := &tg.MessagesGetSavedHistoryRequest{
		Peer:  &tg.InputPeerSelf{},
		Limit: 20,
	}
	savedHistoryReq.SetParentPeer(inputPeerChannel(channel))
	savedHistory, err := r.onMessagesGetSavedHistory(ownerCtx, savedHistoryReq)
	if err != nil {
		t.Fatalf("messages.getSavedHistory: %v", err)
	}
	if len(savedHistory.(*tg.MessagesMessages).Messages) != 0 || len(savedHistory.(*tg.MessagesMessages).Chats) != 1 {
		t.Fatalf("messages.getSavedHistory = %+v, want empty with parent channel context", savedHistory)
	}
	badSavedParent := &tg.MessagesGetSavedHistoryRequest{
		Peer:  &tg.InputPeerSelf{},
		Limit: 20,
	}
	badSavedParent.SetParentPeer(inputPeerChannelWithHash(channel, channel.AccessHash+1))
	if _, err := r.onMessagesGetSavedHistory(ownerCtx, badSavedParent); err == nil || !strings.Contains(err.Error(), "PARENT_PEER_INVALID") {
		t.Fatalf("messages.getSavedHistory bad parent err = %v, want PARENT_PEER_INVALID", err)
	}
	if _, err := r.onMessagesGetSavedHistory(ownerCtx, &tg.MessagesGetSavedHistoryRequest{
		Peer:  inputPeerChannelWithHash(channel, channel.AccessHash+1),
		Limit: 20,
	}); err == nil || !strings.Contains(err.Error(), "CHANNEL_PRIVATE") {
		t.Fatalf("messages.getSavedHistory bad peer err = %v, want CHANNEL_PRIVATE", err)
	}
	if _, err := r.onMessagesGetSavedHistory(ownerCtx, &tg.MessagesGetSavedHistoryRequest{
		Peer:  &tg.InputPeerSelf{},
		Limit: maxSearchResultsLimit + 1,
	}); err == nil || !strings.Contains(err.Error(), "LIMIT_INVALID") {
		t.Fatalf("messages.getSavedHistory limit err = %v, want LIMIT_INVALID", err)
	}
	if ok, err := r.onMessagesReadSavedHistory(ownerCtx, &tg.MessagesReadSavedHistoryRequest{
		ParentPeer: inputPeerChannel(channel),
		Peer:       &tg.InputPeerSelf{},
		MaxID:      0,
	}); err != nil || !ok {
		t.Fatalf("messages.readSavedHistory = ok %v err %v, want true", ok, err)
	}
	if _, err := r.onMessagesReadSavedHistory(ownerCtx, &tg.MessagesReadSavedHistoryRequest{
		ParentPeer: &tg.InputPeerSelf{},
		Peer:       &tg.InputPeerSelf{},
	}); err == nil || !strings.Contains(err.Error(), "PARENT_PEER_INVALID") {
		t.Fatalf("messages.readSavedHistory bad parent err = %v, want PARENT_PEER_INVALID", err)
	}
	if _, err := r.onMessagesReadSavedHistory(ownerCtx, &tg.MessagesReadSavedHistoryRequest{
		ParentPeer: inputPeerChannel(channel),
		Peer:       &tg.InputPeerSelf{},
		MaxID:      domain.MaxMessageBoxID + 1,
	}); err == nil || !strings.Contains(err.Error(), "MESSAGE_ID_INVALID") {
		t.Fatalf("messages.readSavedHistory max_id err = %v, want MESSAGE_ID_INVALID", err)
	}
	deleteSavedReq := &tg.MessagesDeleteSavedHistoryRequest{
		Peer: &tg.InputPeerSelf{},
	}
	deleteSavedReq.SetParentPeer(inputPeerChannel(channel))
	deletedSaved, err := r.onMessagesDeleteSavedHistory(ownerCtx, deleteSavedReq)
	if err != nil {
		t.Fatalf("messages.deleteSavedHistory: %v", err)
	}
	if deletedSaved.Offset != 0 || deletedSaved.PtsCount != 0 {
		t.Fatalf("messages.deleteSavedHistory = %+v, want empty affected history", deletedSaved)
	}
	badDeleteDate := &tg.MessagesDeleteSavedHistoryRequest{
		Peer: &tg.InputPeerSelf{},
	}
	badDeleteDate.SetMinDate(-1)
	if _, err := r.onMessagesDeleteSavedHistory(ownerCtx, badDeleteDate); err == nil || !strings.Contains(err.Error(), "LIMIT_INVALID") {
		t.Fatalf("messages.deleteSavedHistory min_date err = %v, want LIMIT_INVALID", err)
	}
	scheduledHistory, err := r.onMessagesGetScheduledHistory(ownerCtx, &tg.MessagesGetScheduledHistoryRequest{
		Peer: inputPeerChannel(channel),
	})
	if err != nil {
		t.Fatalf("messages.getScheduledHistory: %v", err)
	}
	if len(scheduledHistory.(*tg.MessagesMessages).Messages) != 0 || len(scheduledHistory.(*tg.MessagesMessages).Chats) != 1 {
		t.Fatalf("messages.getScheduledHistory = %+v, want empty with channel context", scheduledHistory)
	}
	if _, err := r.onMessagesGetScheduledHistory(ownerCtx, &tg.MessagesGetScheduledHistoryRequest{
		Peer: inputPeerChannelWithHash(channel, channel.AccessHash+1),
	}); err == nil || !strings.Contains(err.Error(), "CHANNEL_PRIVATE") {
		t.Fatalf("messages.getScheduledHistory bad hash err = %v, want CHANNEL_PRIVATE", err)
	}
	scheduled, err := r.onMessagesGetScheduledMessages(ownerCtx, &tg.MessagesGetScheduledMessagesRequest{
		Peer: inputPeerChannel(channel),
		ID:   []int{1},
	})
	if err != nil {
		t.Fatalf("messages.getScheduledMessages: %v", err)
	}
	if len(scheduled.(*tg.MessagesMessages).Messages) != 0 || len(scheduled.(*tg.MessagesMessages).Chats) != 1 {
		t.Fatalf("messages.getScheduledMessages = %+v, want empty with channel context", scheduled)
	}
	if _, err := r.onMessagesGetScheduledMessages(ownerCtx, &tg.MessagesGetScheduledMessagesRequest{
		Peer: inputPeerChannel(channel),
		ID:   make([]int, maxGetMessagesIDs+1),
	}); err == nil || !strings.Contains(err.Error(), "LIMIT_INVALID") {
		t.Fatalf("messages.getScheduledMessages id cap err = %v, want LIMIT_INVALID", err)
	}
	deletedScheduled, err := r.onMessagesDeleteScheduledMessages(ownerCtx, &tg.MessagesDeleteScheduledMessagesRequest{
		Peer: inputPeerChannel(channel),
		ID:   []int{7, 8},
	})
	if err != nil {
		t.Fatalf("messages.deleteScheduledMessages: %v", err)
	}
	if got := deletedScheduled.(*tg.Updates).Updates; len(got) != 1 {
		t.Fatalf("messages.deleteScheduledMessages updates = %+v, want one update", got)
	} else if update, ok := got[0].(*tg.UpdateDeleteScheduledMessages); !ok || len(update.Messages) != 2 || update.Messages[0] != 7 {
		t.Fatalf("messages.deleteScheduledMessages update = %#v, want requested ids", got[0])
	}
	if _, err := r.onMessagesSendScheduledMessages(ownerCtx, &tg.MessagesSendScheduledMessagesRequest{
		Peer: inputPeerChannel(channel),
		ID:   []int{7},
	}); err == nil || !strings.Contains(err.Error(), "MESSAGE_ID_INVALID") {
		t.Fatalf("messages.sendScheduledMessages err = %v, want MESSAGE_ID_INVALID", err)
	}
	if _, err := r.onMessagesCreateForumTopic(ownerCtx, &tg.MessagesCreateForumTopicRequest{
		Peer:     inputPeerChannel(channel),
		Title:    "Topic",
		RandomID: 91004,
	}); err == nil || !strings.Contains(err.Error(), "CHANNEL_FORUM_MISSING") {
		t.Fatalf("messages.createForumTopic err = %v, want CHANNEL_FORUM_MISSING", err)
	}
	if _, err := r.onMessagesCreateForumTopic(ownerCtx, &tg.MessagesCreateForumTopicRequest{
		Peer:     inputPeerChannel(channel),
		RandomID: 91005,
	}); err == nil || !strings.Contains(err.Error(), "TOPIC_TITLE_EMPTY") {
		t.Fatalf("messages.createForumTopic empty title err = %v, want TOPIC_TITLE_EMPTY", err)
	}
	if _, err := r.onMessagesEditForumTopic(ownerCtx, &tg.MessagesEditForumTopicRequest{
		Peer:    inputPeerChannel(channel),
		TopicID: 1,
	}); err == nil || !strings.Contains(err.Error(), "CHANNEL_FORUM_MISSING") {
		t.Fatalf("messages.editForumTopic err = %v, want CHANNEL_FORUM_MISSING", err)
	}
	if _, err := r.onMessagesUpdatePinnedForumTopic(ownerCtx, &tg.MessagesUpdatePinnedForumTopicRequest{
		Peer:    inputPeerChannel(channel),
		TopicID: 1,
		Pinned:  true,
	}); err == nil || !strings.Contains(err.Error(), "CHANNEL_FORUM_MISSING") {
		t.Fatalf("messages.updatePinnedForumTopic err = %v, want CHANNEL_FORUM_MISSING", err)
	}
	if _, err := r.onMessagesReorderPinnedForumTopics(ownerCtx, &tg.MessagesReorderPinnedForumTopicsRequest{
		Peer:  inputPeerChannel(channel),
		Order: []int{1, 2},
	}); err == nil || !strings.Contains(err.Error(), "CHANNEL_FORUM_MISSING") {
		t.Fatalf("messages.reorderPinnedForumTopics err = %v, want CHANNEL_FORUM_MISSING", err)
	}
	if _, err := r.onMessagesDeleteTopicHistory(ownerCtx, &tg.MessagesDeleteTopicHistoryRequest{
		Peer:     inputPeerChannel(channel),
		TopMsgID: 1,
	}); err == nil || !strings.Contains(err.Error(), "CHANNEL_FORUM_MISSING") {
		t.Fatalf("messages.deleteTopicHistory err = %v, want CHANNEL_FORUM_MISSING", err)
	}
	chats, err := r.onMessagesGetChats(ownerCtx, []int64{channel.ID})
	if err != nil {
		t.Fatalf("messages.getChats legacy wrapper: %v", err)
	}
	if len(chats.(*tg.MessagesChats).Chats) != 1 {
		t.Fatalf("messages.getChats = %+v, want current megagroup channel", chats)
	}
	migrated, err := r.onMessagesMigrateChat(ownerCtx, channel.ID)
	if err != nil {
		t.Fatalf("messages.migrateChat legacy mapping: %v", err)
	}
	migratedUpdates := migrated.(*tg.Updates)
	if len(migratedUpdates.Chats) != 1 || len(migratedUpdates.Updates) != 1 {
		t.Fatalf("messages.migrateChat = %+v, want updateChannel with current megagroup", migratedUpdates)
	}
	if _, err := r.onMessagesMigrateChat(friendCtx, channel.ID); err == nil {
		t.Fatalf("messages.migrateChat by non-admin = nil err, want CHAT_ADMIN_REQUIRED")
	}
	full, err := r.onMessagesGetFullChat(ownerCtx, channel.ID)
	if err != nil {
		t.Fatalf("messages.getFullChat legacy wrapper: %v", err)
	}
	if got := full.FullChat.(*tg.ChannelFull).ID; got != channel.ID {
		t.Fatalf("messages.getFullChat id = %d, want %d", got, channel.ID)
	}
	viewedID := defaultMsg.ID
	views, err := r.onMessagesGetMessagesViews(ownerCtx, &tg.MessagesGetMessagesViewsRequest{
		Peer: inputPeerChannel(channel),
		ID:   []int{viewedID},
	})
	if err != nil {
		t.Fatalf("messages.getMessagesViews: %v", err)
	}
	if len(views.Views) != 1 || len(views.Chats) != 1 {
		t.Fatalf("messages.getMessagesViews = %+v, want one view with channel context", views)
	}
	if got, ok := views.Views[0].GetViews(); !ok || got != 0 {
		t.Fatalf("messages.getMessagesViews views = %d ok %v, want explicit zero before increment", got, ok)
	}
	incremented, err := r.onMessagesGetMessagesViews(ownerCtx, &tg.MessagesGetMessagesViewsRequest{
		Peer:      inputPeerChannel(channel),
		ID:        []int{viewedID},
		Increment: true,
	})
	if err != nil {
		t.Fatalf("messages.getMessagesViews increment: %v", err)
	}
	if got, ok := incrementalViewCount(incremented); !ok || got != 1 {
		t.Fatalf("messages.getMessagesViews increment count = %d ok %v, want 1", got, ok)
	}
	repeated, err := r.onMessagesGetMessagesViews(ownerCtx, &tg.MessagesGetMessagesViewsRequest{
		Peer:      inputPeerChannel(channel),
		ID:        []int{viewedID},
		Increment: true,
	})
	if err != nil {
		t.Fatalf("messages.getMessagesViews repeat increment: %v", err)
	}
	if got, ok := incrementalViewCount(repeated); !ok || got != 1 {
		t.Fatalf("messages.getMessagesViews repeated count = %d ok %v, want still 1", got, ok)
	}
	friendIncremented, err := r.onMessagesGetMessagesViews(friendCtx, &tg.MessagesGetMessagesViewsRequest{
		Peer:      inputPeerChannel(channel),
		ID:        []int{viewedID},
		Increment: true,
	})
	if err != nil {
		t.Fatalf("messages.getMessagesViews friend increment: %v", err)
	}
	if got, ok := incrementalViewCount(friendIncremented); !ok || got != 2 {
		t.Fatalf("messages.getMessagesViews friend count = %d ok %v, want 2", got, ok)
	}
	if _, err := r.onMessagesReadMessageContents(ownerCtx, []int{1}); err != nil {
		t.Fatalf("messages.readMessageContents: %v", err)
	}
	mentions, err := r.onMessagesGetUnreadMentions(ownerCtx, &tg.MessagesGetUnreadMentionsRequest{
		Peer:  inputPeerChannel(channel),
		Limit: 10,
	})
	if err != nil {
		t.Fatalf("messages.getUnreadMentions: %v", err)
	}
	_, mentionChats, _ := searchMessagesPayload(t, mentions)
	if len(mentionChats) != 1 {
		t.Fatalf("messages.getUnreadMentions = %+v, want channel context", mentions)
	}
	if _, err := r.onMessagesReadMentions(ownerCtx, &tg.MessagesReadMentionsRequest{
		Peer: inputPeerChannel(channel),
	}); err != nil {
		t.Fatalf("messages.readMentions: %v", err)
	}
	if ok, err := r.onMessagesReportSpam(ownerCtx, inputPeerChannel(channel)); err != nil || !ok {
		t.Fatalf("messages.reportSpam = ok %v err %v, want true nil", ok, err)
	}
	if _, err := r.onMessagesReport(ownerCtx, &tg.MessagesReportRequest{
		Peer: inputPeerChannel(channel),
	}); err == nil || !strings.Contains(err.Error(), "MESSAGE_ID_REQUIRED") {
		t.Fatalf("desktop messages.report without ids err = %v, want MESSAGE_ID_REQUIRED", err)
	}
	androidReportOptions, err := r.onMessagesReport(
		WithClientInfo(ownerCtx, ClientInfo{
			Type:       ClientTypeAndroid,
			AppVersion: "12.9.0 (69669) pbeta",
		}),
		&tg.MessagesReportRequest{Peer: inputPeerChannel(channel)},
	)
	if err != nil {
		t.Fatalf("android messages.report initial options: %v", err)
	}
	if choices, ok := androidReportOptions.(*tg.ReportResultChooseOption); !ok || len(choices.Options) == 0 {
		t.Fatalf("android messages.report initial options = %#v, want chooseOption", androidReportOptions)
	}
	if got := moderationReports.Reports(); len(got) != 1 {
		t.Fatalf("android initial report option discovery persisted reports = %+v, want only earlier reportSpam", got)
	}
	if _, err := r.onMessagesReport(
		WithClientInfo(ownerCtx, ClientInfo{Type: ClientTypeAndroid}),
		&tg.MessagesReportRequest{
			Peer:   inputPeerChannel(channel),
			Option: []byte("spam"),
		},
	); err == nil || !strings.Contains(err.Error(), "MESSAGE_ID_REQUIRED") {
		t.Fatalf("android selected report option without ids err = %v, want MESSAGE_ID_REQUIRED", err)
	}
	reportOptions, err := r.onMessagesReport(ownerCtx, &tg.MessagesReportRequest{
		Peer: inputPeerChannel(channel),
		ID:   []int{1},
	})
	if err != nil {
		t.Fatalf("messages.report options: %v", err)
	}
	if choices, ok := reportOptions.(*tg.ReportResultChooseOption); !ok || len(choices.Options) == 0 {
		t.Fatalf("messages.report options = %#v, want chooseOption", reportOptions)
	}
	reportComment, err := r.onMessagesReport(ownerCtx, &tg.MessagesReportRequest{
		Peer:   inputPeerChannel(channel),
		ID:     []int{1},
		Option: []byte("other"),
	})
	if err != nil {
		t.Fatalf("messages.report other: %v", err)
	}
	if _, ok := reportComment.(*tg.ReportResultAddComment); !ok {
		t.Fatalf("messages.report other = %#v, want addComment", reportComment)
	}
	reported, err := r.onMessagesReport(ownerCtx, &tg.MessagesReportRequest{
		Peer:   inputPeerChannel(channel),
		ID:     []int{1},
		Option: []byte("spam"),
	})
	if err != nil {
		t.Fatalf("messages.report spam: %v", err)
	}
	if _, ok := reported.(*tg.ReportResultReported); !ok {
		t.Fatalf("messages.report spam = %#v, want reported", reported)
	}
	if got := moderationReports.Reports(); len(got) != 2 {
		t.Fatalf("moderation reports = %+v, want peer-spam and message reports", got)
	}
	if _, err := r.onMessagesReport(ownerCtx, &tg.MessagesReportRequest{
		Peer:   inputPeerChannel(channel),
		ID:     []int{1},
		Option: []byte("bogus"),
	}); err == nil || !strings.Contains(err.Error(), "OPTION_INVALID") {
		t.Fatalf("messages.report invalid option err = %v, want OPTION_INVALID", err)
	}
	if ok, err := r.onMessagesReportMessagesDelivery(ownerCtx, &tg.MessagesReportMessagesDeliveryRequest{
		Peer: inputPeerChannel(channel),
		ID:   []int{1},
	}); err != nil || !ok {
		t.Fatalf("messages.reportMessagesDelivery = ok %v err %v, want true nil", ok, err)
	}
	if ok, err := r.onMessagesReportReadMetrics(ownerCtx, &tg.MessagesReportReadMetricsRequest{
		Peer: inputPeerChannel(channel),
		Metrics: []tg.InputMessageReadMetric{{
			MsgID:                         1,
			ViewID:                        99,
			TimeInViewMs:                  10,
			ActiveTimeInViewMs:            10,
			HeightToViewportRatioPermille: 1000,
			SeenRangeRatioPermille:        1000,
		}},
	}); err != nil || !ok {
		t.Fatalf("messages.reportReadMetrics = ok %v err %v, want true nil", ok, err)
	}
	if events := telemetryEvents.Events(); len(events) != 2 ||
		events[0].Kind != domain.ClientTelemetryMessageDelivery ||
		events[1].Kind != domain.ClientTelemetryReadMetrics ||
		len(moderationReports.Reports()) != 2 {
		t.Fatalf("telemetry=%+v moderation=%+v, want separate durable streams",
			events, moderationReports.Reports())
	}
	if ok, err := r.onMessagesReportMusicListen(ownerCtx, &tg.MessagesReportMusicListenRequest{
		ID:               &tg.InputDocument{ID: 1, AccessHash: 2},
		ListenedDuration: 1,
	}); err == nil || ok || !strings.Contains(err.Error(), "DOCUMENT_INVALID") {
		t.Fatalf("messages.reportMusicListen = ok %v err %v, want DOCUMENT_INVALID", ok, err)
	}
	if _, err := r.onMessagesReportSponsoredMessage(ownerCtx, &tg.MessagesReportSponsoredMessageRequest{
		RandomID: []byte("unseen-ad"),
	}); err == nil || !strings.Contains(err.Error(), "RANDOM_ID_INVALID") {
		t.Fatalf("unseen sponsored report err=%v, want RANDOM_ID_INVALID", err)
	}
	impression, err := domain.NewSponsoredMessageImpression(
		owner.ID, []byte("ad"),
		domain.Peer{Type: domain.PeerTypeChannel, ID: channel.ID},
		0, []byte(`{"schema_version":1,"text":"test sponsored message"}`),
		time.Now().UTC(), time.Now().UTC().Add(time.Hour),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := moderationReports.CreateSponsoredMessageImpression(ownerCtx, impression); err != nil {
		t.Fatal(err)
	}
	sponsoredOptions, err := r.onMessagesReportSponsoredMessage(ownerCtx, &tg.MessagesReportSponsoredMessageRequest{
		RandomID: []byte("ad"),
	})
	if err != nil {
		t.Fatalf("messages.reportSponsoredMessage options: %v", err)
	}
	if choices, ok := sponsoredOptions.(*tg.ChannelsSponsoredMessageReportResultChooseOption); !ok ||
		len(choices.Options) == 0 {
		t.Fatalf("sponsored options=%#v, want chooseOption", sponsoredOptions)
	}
	sponsoredReport, err := r.onMessagesReportSponsoredMessage(ownerCtx, &tg.MessagesReportSponsoredMessageRequest{
		RandomID: []byte("ad"),
		Option:   []byte("spam"),
	})
	if err != nil {
		t.Fatalf("messages.reportSponsoredMessage: %v", err)
	}
	if _, ok := sponsoredReport.(*tg.ChannelsSponsoredMessageReportResultReported); !ok {
		t.Fatalf("messages.reportSponsoredMessage = %#v, want reported", sponsoredReport)
	}
	sendReactionReq := &tg.MessagesSendReactionRequest{
		Peer:     inputPeerChannel(channel),
		MsgID:    viewedID,
		Reaction: []tg.ReactionClass{&tg.ReactionEmoji{Emoticon: "\U0001f44d"}},
	}
	sendReactionReq.SetReaction(sendReactionReq.Reaction)
	sendReactionReq.SetAddToRecent(true)
	if updates, err := r.onMessagesSendReaction(ownerCtx, sendReactionReq); err != nil {
		t.Fatalf("messages.sendReaction: %v", err)
	} else if got := updates.(*tg.Updates).Updates; len(got) != 1 {
		t.Fatalf("messages.sendReaction updates = %+v, want one reaction update", got)
	} else if update, ok := got[0].(*tg.UpdateMessageReactions); !ok || update.MsgID != viewedID || len(update.Reactions.Results) != 1 || update.Reactions.Results[0].Count != 1 || update.Reactions.Results[0].ChosenOrder != 1 {
		t.Fatalf("messages.sendReaction update = %#v, want one chosen reaction", got[0])
	}
	tooManyReactionsReq := &tg.MessagesSendReactionRequest{
		Peer:     inputPeerChannel(channel),
		MsgID:    viewedID,
		Reaction: make([]tg.ReactionClass, maxReactionVector+1),
	}
	tooManyReactionsReq.SetReaction(tooManyReactionsReq.Reaction)
	if _, err := r.onMessagesSendReaction(ownerCtx, tooManyReactionsReq); err == nil || !strings.Contains(err.Error(), "LIMIT_INVALID") {
		t.Fatalf("messages.sendReaction huge reaction vector err = %v, want LIMIT_INVALID", err)
	}
	reactionUpdates, err := r.onMessagesGetMessagesReactions(ownerCtx, &tg.MessagesGetMessagesReactionsRequest{
		Peer: inputPeerChannel(channel),
		ID:   []int{viewedID},
	})
	if err != nil {
		t.Fatalf("messages.getMessagesReactions: %v", err)
	}
	if got := reactionUpdates.(*tg.Updates).Updates; len(got) != 1 {
		t.Fatalf("messages.getMessagesReactions updates = %+v, want one reaction update", got)
	} else if update, ok := got[0].(*tg.UpdateMessageReactions); !ok || update.MsgID != viewedID || len(update.Reactions.Results) != 1 || update.Reactions.Results[0].Count != 1 {
		t.Fatalf("messages.getMessagesReactions update = %#v, want one reaction", got[0])
	}
	reactionList, err := r.onMessagesGetMessageReactionsList(ownerCtx, &tg.MessagesGetMessageReactionsListRequest{
		Peer:  inputPeerChannel(channel),
		ID:    viewedID,
		Limit: 10,
	})
	if err != nil {
		t.Fatalf("messages.getMessageReactionsList: %v", err)
	}
	if reactionList.Count != 1 || len(reactionList.Reactions) != 1 || len(reactionList.Chats) != 1 || len(reactionList.Users) != 1 {
		t.Fatalf("messages.getMessageReactionsList = %+v, want one reaction with channel context", reactionList)
	}
	if peer, ok := reactionList.Reactions[0].PeerID.(*tg.PeerUser); !ok || peer.UserID != owner.ID || !reactionList.Reactions[0].My {
		t.Fatalf("messages.getMessageReactionsList reaction = %+v, want current user reaction", reactionList.Reactions[0])
	}
	friendReactionReq := &tg.MessagesSendReactionRequest{
		Peer:     inputPeerChannel(channel),
		MsgID:    viewedID,
		Reaction: []tg.ReactionClass{&tg.ReactionEmoji{Emoticon: "\U0001f44d"}},
	}
	friendReactionReq.SetReaction(friendReactionReq.Reaction)
	if _, err := r.onMessagesSendReaction(friendCtx, friendReactionReq); err != nil {
		t.Fatalf("messages.sendReaction by friend: %v", err)
	}
	if ok, err := r.onMessagesReportReaction(ownerCtx, &tg.MessagesReportReactionRequest{
		Peer:         inputPeerChannel(channel),
		ID:           viewedID,
		ReactionPeer: &tg.InputPeerUser{UserID: friend.ID, AccessHash: friend.AccessHash},
	}); err != nil || !ok {
		t.Fatalf("messages.reportReaction = ok %v err %v, want true nil", ok, err)
	}
	unreadReactions, err := r.onMessagesGetUnreadReactions(ownerCtx, &tg.MessagesGetUnreadReactionsRequest{
		Peer:  inputPeerChannel(channel),
		Limit: 10,
	})
	if err != nil {
		t.Fatalf("messages.getUnreadReactions: %v", err)
	}
	unreadMessages, unreadChats, unreadUsers := searchMessagesPayload(t, unreadReactions)
	if len(unreadMessages) != 1 || len(unreadChats) != 1 || len(unreadUsers) == 0 {
		t.Fatalf("messages.getUnreadReactions = %+v, want one unread reaction with channel context", unreadReactions)
	}
	unreadMessage, ok := unreadMessages[0].(*tg.Message)
	if !ok || unreadMessage.ID != viewedID {
		t.Fatalf("messages.getUnreadReactions message = %#v, want message %d", unreadMessages[0], viewedID)
	}
	reactions, ok := unreadMessage.GetReactions()
	hasUnreadRecent := false
	for _, recent := range reactions.RecentReactions {
		if recent.Unread {
			hasUnreadRecent = true
			break
		}
	}
	if !ok || !hasUnreadRecent {
		t.Fatalf("messages.getUnreadReactions reactions = %+v ok %v, want unread recent reaction", reactions, ok)
	}
	if _, err := r.onMessagesReadReactions(ownerCtx, &tg.MessagesReadReactionsRequest{
		Peer: inputPeerChannel(channel),
	}); err != nil {
		t.Fatalf("messages.readReactions: %v", err)
	}
	unreadAfterRead, err := r.onMessagesGetUnreadReactions(ownerCtx, &tg.MessagesGetUnreadReactionsRequest{
		Peer:  inputPeerChannel(channel),
		Limit: 10,
	})
	if err != nil {
		t.Fatalf("messages.getUnreadReactions after read: %v", err)
	}
	unreadAfterReadMessages, _, _ := searchMessagesPayload(t, unreadAfterRead)
	if len(unreadAfterReadMessages) != 0 {
		t.Fatalf("messages.getUnreadReactions after read = %+v, want empty messages", unreadAfterRead)
	}
	common, err := r.onMessagesGetCommonChats(ownerCtx, &tg.MessagesGetCommonChatsRequest{
		UserID: &tg.InputUser{UserID: friend.ID, AccessHash: friend.AccessHash},
		Limit:  40,
	})
	if err != nil {
		t.Fatalf("messages.getCommonChats: %v", err)
	}
	commonChats := common.(*tg.MessagesChats).Chats
	if len(commonChats) != 1 || commonChats[0].(*tg.Channel).ID != channel.ID {
		t.Fatalf("messages.getCommonChats = %+v, want current shared megagroup", common)
	}
	commonNext, err := r.onMessagesGetCommonChats(ownerCtx, &tg.MessagesGetCommonChatsRequest{
		UserID: &tg.InputUser{UserID: friend.ID, AccessHash: friend.AccessHash},
		MaxID:  channel.ID,
		Limit:  40,
	})
	if err != nil {
		t.Fatalf("messages.getCommonChats next page: %v", err)
	}
	if len(commonNext.(*tg.MessagesChats).Chats) != 0 {
		t.Fatalf("messages.getCommonChats next page = %+v, want empty after max id", commonNext)
	}
	fullFriend, err := r.onUsersGetFullUser(ownerCtx, &tg.InputUser{UserID: friend.ID, AccessHash: friend.AccessHash})
	if err != nil {
		t.Fatalf("users.getFullUser friend: %v", err)
	}
	if fullFriend.FullUser.CommonChatsCount != 1 {
		t.Fatalf("users.getFullUser commonChatsCount = %d, want 1", fullFriend.FullUser.CommonChatsCount)
	}
	if _, err := r.onMessagesGetCommonChats(ownerCtx, &tg.MessagesGetCommonChatsRequest{
		UserID: &tg.InputUserSelf{},
		Limit:  1,
	}); err == nil || !strings.Contains(err.Error(), "USER_ID_INVALID") {
		t.Fatalf("messages.getCommonChats self err = %v, want USER_ID_INVALID", err)
	}
	if _, err := r.onMessagesGetAttachedStickers(ownerCtx, nil); err == nil || !strings.Contains(err.Error(), "MEDIA_EMPTY") {
		t.Fatalf("messages.getAttachedStickers nil err = %v, want MEDIA_EMPTY", err)
	}
	attached, err := r.onMessagesGetAttachedStickers(ownerCtx, &tg.InputStickeredMediaDocument{
		ID: &tg.InputDocument{ID: 1, AccessHash: 2},
	})
	if err != nil || len(attached) != 0 {
		t.Fatalf("messages.getAttachedStickers = %+v err=%v, want empty", attached, err)
	}
	customEmojiDocs, err := r.onMessagesGetCustomEmojiDocuments(ownerCtx, []int64{1, 2})
	if err != nil || len(customEmojiDocs) != 0 {
		t.Fatalf("messages.getCustomEmojiDocuments = %+v err=%v, want empty", customEmojiDocs, err)
	}
	if _, err := r.onMessagesGetCustomEmojiDocuments(ownerCtx, make([]int64, maxEmojiDocuments+1)); err == nil || !strings.Contains(err.Error(), "LIMIT_INVALID") {
		t.Fatalf("messages.getCustomEmojiDocuments too many err = %v, want LIMIT_INVALID", err)
	}
	stickerSets, err := r.onMessagesSearchStickerSets(ownerCtx, &tg.MessagesSearchStickerSetsRequest{Q: "cat"})
	if err != nil {
		t.Fatalf("messages.searchStickerSets: %v", err)
	}
	if found, ok := stickerSets.(*tg.MessagesFoundStickerSets); !ok || len(found.Sets) != 0 {
		t.Fatalf("messages.searchStickerSets = %#v, want empty found sets", stickerSets)
	}
	stickers, err := r.onMessagesSearchStickers(ownerCtx, &tg.MessagesSearchStickersRequest{
		Q:        "cat",
		LangCode: []string{"en"},
		Limit:    50,
	})
	if err != nil {
		t.Fatalf("messages.searchStickers: %v", err)
	}
	if found, ok := stickers.(*tg.MessagesFoundStickers); !ok || len(found.Stickers) != 0 {
		t.Fatalf("messages.searchStickers = %#v, want empty found stickers", stickers)
	}
	if _, err := r.onMessagesSearchStickers(ownerCtx, &tg.MessagesSearchStickersRequest{
		Limit: maxSearchResultsLimit + 1,
	}); err == nil || !strings.Contains(err.Error(), "LIMIT_INVALID") {
		t.Fatalf("messages.searchStickers huge limit err = %v, want LIMIT_INVALID", err)
	}
	// emoji 关键词已从官方 seed(catalog),返回真实词典而非空 stub。
	keywords, err := r.onMessagesGetEmojiKeywords(ownerCtx, "en")
	if err != nil || keywords.LangCode != "en" || keywords.Version == 0 || len(keywords.Keywords) == 0 {
		t.Fatalf("messages.getEmojiKeywords lang=%q version=%d count=%d err=%v, want seeded en keywords",
			keywords.LangCode, keywords.Version, len(keywords.Keywords), err)
	}
	// 客户端版本(7)落后于固化版本 → 下发全量,Version 推进到固化版本。
	keywordsDiff, err := r.onMessagesGetEmojiKeywordsDifference(ownerCtx, &tg.MessagesGetEmojiKeywordsDifferenceRequest{
		LangCode:    "en",
		FromVersion: 7,
	})
	if err != nil || keywordsDiff.FromVersion != 7 || keywordsDiff.Version <= 7 || len(keywordsDiff.Keywords) == 0 {
		t.Fatalf("messages.getEmojiKeywordsDifference from=%d version=%d count=%d err=%v, want full diff to seeded version",
			keywordsDiff.FromVersion, keywordsDiff.Version, len(keywordsDiff.Keywords), err)
	}
	extended, err := r.onMessagesGetExtendedMedia(ownerCtx, &tg.MessagesGetExtendedMediaRequest{
		Peer: inputPeerChannel(channel),
		ID:   []int{1},
	})
	if err != nil {
		t.Fatalf("messages.getExtendedMedia: %v", err)
	}
	if len(extended.(*tg.Updates).Updates) != 0 {
		t.Fatalf("messages.getExtendedMedia = %+v, want empty updates", extended)
	}
	topReactions, err := r.onMessagesGetTopReactions(ownerCtx, &tg.MessagesGetTopReactionsRequest{Limit: 3})
	if err != nil {
		t.Fatalf("messages.getTopReactions: %v", err)
	}
	topPage, ok := topReactions.(*tg.MessagesReactions)
	if !ok || topPage.Hash == 0 || len(topPage.Reactions) != 3 {
		t.Fatalf("messages.getTopReactions = %#v, want three hashable reactions", topReactions)
	}
	if emoji, ok := topPage.Reactions[0].(*tg.ReactionEmoji); !ok || emoji.Emoticon != "\U0001f44d" {
		t.Fatalf("messages.getTopReactions first reaction = %#v, want account top thumb", topPage.Reactions[0])
	}
	if emoji, ok := topPage.Reactions[1].(*tg.ReactionEmoji); !ok || emoji.Emoticon != "\u2764\ufe0f" {
		t.Fatalf("messages.getTopReactions fallback reaction = %#v, want catalog heart", topPage.Reactions[1])
	}
	topNotModified, err := r.onMessagesGetTopReactions(ownerCtx, &tg.MessagesGetTopReactionsRequest{Limit: 3, Hash: topPage.Hash})
	if err != nil {
		t.Fatalf("messages.getTopReactions hash: %v", err)
	}
	if _, ok := topNotModified.(*tg.MessagesReactionsNotModified); !ok {
		t.Fatalf("messages.getTopReactions hash = %#v, want notModified", topNotModified)
	}
	recentReactions, err := r.onMessagesGetRecentReactions(ownerCtx, &tg.MessagesGetRecentReactionsRequest{Limit: 40})
	if err != nil {
		t.Fatalf("messages.getRecentReactions: %v", err)
	}
	recentPage, ok := recentReactions.(*tg.MessagesReactions)
	if !ok || recentPage.Hash == 0 || len(recentPage.Reactions) != 1 {
		t.Fatalf("messages.getRecentReactions = %#v, want one reaction with non-zero hash", recentReactions)
	}
	if emoji, ok := recentPage.Reactions[0].(*tg.ReactionEmoji); !ok || emoji.Emoticon != "\U0001f44d" {
		t.Fatalf("messages.getRecentReactions reaction = %#v, want thumb emoji", recentPage.Reactions[0])
	}
	recentNotModified, err := r.onMessagesGetRecentReactions(ownerCtx, &tg.MessagesGetRecentReactionsRequest{Limit: 40, Hash: recentPage.Hash})
	if err != nil {
		t.Fatalf("messages.getRecentReactions hash: %v", err)
	}
	if _, ok := recentNotModified.(*tg.MessagesReactionsNotModified); !ok {
		t.Fatalf("messages.getRecentReactions hash = %#v, want notModified", recentNotModified)
	}
	if ok, err := r.onMessagesClearRecentReactions(ownerCtx); err != nil || !ok {
		t.Fatalf("messages.clearRecentReactions = ok %v err %v, want true nil", ok, err)
	}
	recentAfterClear, err := r.onMessagesGetRecentReactions(ownerCtx, &tg.MessagesGetRecentReactionsRequest{Limit: 40, Hash: recentPage.Hash})
	if err != nil {
		t.Fatalf("messages.getRecentReactions after clear: %v", err)
	}
	clearedPage, ok := recentAfterClear.(*tg.MessagesReactions)
	if !ok || clearedPage.Hash != 0 || len(clearedPage.Reactions) != 0 {
		t.Fatalf("messages.getRecentReactions after clear = %#v, want empty page hash 0", recentAfterClear)
	}
	savedTagsReq := &tg.MessagesGetSavedReactionTagsRequest{
		Peer: inputPeerChannel(channel),
	}
	savedTagsReq.SetPeer(savedTagsReq.Peer)
	savedTags, err := r.onMessagesGetSavedReactionTags(ownerCtx, savedTagsReq)
	if err != nil {
		t.Fatalf("messages.getSavedReactionTags: %v", err)
	}
	if got := savedTags.(*tg.MessagesSavedReactionTags).Tags; len(got) != 0 {
		t.Fatalf("messages.getSavedReactionTags = %+v, want empty", got)
	}
	staleEmptyTags, err := r.onMessagesGetSavedReactionTags(ownerCtx, &tg.MessagesGetSavedReactionTagsRequest{Hash: 123})
	if err != nil {
		t.Fatalf("messages.getSavedReactionTags stale empty hash: %v", err)
	}
	staleEmptyPage, ok := staleEmptyTags.(*tg.MessagesSavedReactionTags)
	if !ok || staleEmptyPage.Hash != 0 || len(staleEmptyPage.Tags) != 0 {
		t.Fatalf("messages.getSavedReactionTags stale empty hash = %#v, want empty page hash 0", staleEmptyTags)
	}
	updateTagReq := &tg.MessagesUpdateSavedReactionTagRequest{Reaction: &tg.ReactionEmoji{Emoticon: "ok"}}
	updateTagReq.SetTitle("Work")
	if ok, err := r.onMessagesUpdateSavedReactionTag(ownerCtx, updateTagReq); err != nil || !ok {
		t.Fatalf("messages.updateSavedReactionTag = ok %v err %v, want true nil", ok, err)
	}
	globalTags, err := r.onMessagesGetSavedReactionTags(ownerCtx, &tg.MessagesGetSavedReactionTagsRequest{})
	if err != nil {
		t.Fatalf("messages.getSavedReactionTags global: %v", err)
	}
	globalPage, ok := globalTags.(*tg.MessagesSavedReactionTags)
	if !ok || globalPage.Hash == 0 || len(globalPage.Tags) != 1 {
		t.Fatalf("messages.getSavedReactionTags global = %#v, want one hashable tag", globalTags)
	}
	if emoji, ok := globalPage.Tags[0].Reaction.(*tg.ReactionEmoji); !ok || emoji.Emoticon != "ok" || globalPage.Tags[0].Title != "Work" || globalPage.Tags[0].Count != 0 {
		t.Fatalf("messages.getSavedReactionTags tag = %+v, want ok/Work/count0", globalPage.Tags[0])
	}
	globalNotModified, err := r.onMessagesGetSavedReactionTags(ownerCtx, &tg.MessagesGetSavedReactionTagsRequest{Hash: globalPage.Hash})
	if err != nil {
		t.Fatalf("messages.getSavedReactionTags hash: %v", err)
	}
	if _, ok := globalNotModified.(*tg.MessagesSavedReactionTagsNotModified); !ok {
		t.Fatalf("messages.getSavedReactionTags hash = %#v, want notModified", globalNotModified)
	}
	peerTagsAfterUpdate, err := r.onMessagesGetSavedReactionTags(ownerCtx, savedTagsReq)
	if err != nil {
		t.Fatalf("messages.getSavedReactionTags peer after update: %v", err)
	}
	if got := peerTagsAfterUpdate.(*tg.MessagesSavedReactionTags).Tags; len(got) != 0 {
		t.Fatalf("messages.getSavedReactionTags peer after update = %+v, want empty until saved-message tag store exists", got)
	}
	longTagReq := &tg.MessagesUpdateSavedReactionTagRequest{Reaction: &tg.ReactionEmoji{Emoticon: "ok"}}
	longTagReq.SetTitle("abcdefghijklmnop")
	if _, err := r.onMessagesUpdateSavedReactionTag(ownerCtx, longTagReq); err == nil || !strings.Contains(err.Error(), "LIMIT_INVALID") {
		t.Fatalf("messages.updateSavedReactionTag long title err = %v, want LIMIT_INVALID", err)
	}
	if _, err := r.onMessagesUpdateSavedReactionTag(ownerCtx, &tg.MessagesUpdateSavedReactionTagRequest{
		Reaction: &tg.ReactionCustomEmoji{DocumentID: 1},
	}); err == nil || !strings.Contains(err.Error(), "REACTION_INVALID") {
		t.Fatalf("messages.updateSavedReactionTag custom emoji err = %v, want REACTION_INVALID", err)
	}
	tagReactions, err := r.onMessagesGetDefaultTagReactions(ownerCtx, 0)
	if err != nil {
		t.Fatalf("messages.getDefaultTagReactions: %v", err)
	}
	if got := tagReactions.(*tg.MessagesReactions).Reactions; len(got) != 0 {
		t.Fatalf("messages.getDefaultTagReactions = %+v, want empty", got)
	}
	// poll 链路已是真实现：对非 poll 消息一律 MESSAGE_ID_INVALID（与官方一致）。
	if _, err := r.onMessagesSendVote(ownerCtx, &tg.MessagesSendVoteRequest{
		Peer:    inputPeerChannel(channel),
		MsgID:   1,
		Options: [][]byte{[]byte("a")},
	}); err == nil || !strings.Contains(err.Error(), "MESSAGE_ID_INVALID") {
		t.Fatalf("messages.sendVote err = %v, want MESSAGE_ID_INVALID for non-poll message", err)
	}
	if _, err := r.onMessagesSendVote(ownerCtx, &tg.MessagesSendVoteRequest{
		Peer:    inputPeerChannel(channel),
		MsgID:   1,
		Options: make([][]byte, maxPollVoteOptions+1),
	}); err == nil || !strings.Contains(err.Error(), "OPTIONS_TOO_MUCH") {
		t.Fatalf("messages.sendVote too many options err = %v, want OPTIONS_TOO_MUCH", err)
	}
	if _, err := r.onMessagesGetPollResults(ownerCtx, &tg.MessagesGetPollResultsRequest{
		Peer:  inputPeerChannel(channel),
		MsgID: 1,
	}); err == nil || !strings.Contains(err.Error(), "MESSAGE_ID_INVALID") {
		t.Fatalf("messages.getPollResults err = %v, want MESSAGE_ID_INVALID for non-poll message", err)
	}
	pollVotesReq := &tg.MessagesGetPollVotesRequest{
		Peer:   inputPeerChannel(channel),
		ID:     1,
		Option: []byte("a"),
		Limit:  10,
	}
	pollVotesReq.SetOption(pollVotesReq.Option)
	if _, err := r.onMessagesGetPollVotes(ownerCtx, pollVotesReq); err == nil || !strings.Contains(err.Error(), "MESSAGE_ID_INVALID") {
		t.Fatalf("messages.getPollVotes err = %v, want MESSAGE_ID_INVALID for non-poll message", err)
	}
	if _, err := r.onMessagesGetPollVotes(ownerCtx, &tg.MessagesGetPollVotesRequest{
		Peer:  inputPeerChannel(channel),
		ID:    1,
		Limit: maxSearchResultsLimit + 1,
	}); err == nil || !strings.Contains(err.Error(), "LIMIT_INVALID") {
		t.Fatalf("messages.getPollVotes huge limit err = %v, want LIMIT_INVALID", err)
	}
	if _, err := r.onMessagesAddPollAnswer(ownerCtx, &tg.MessagesAddPollAnswerRequest{
		Peer:   inputPeerChannel(channel),
		MsgID:  1,
		Answer: &tg.InputPollAnswer{Text: tg.TextWithEntities{Text: "extra"}},
	}); err == nil || !strings.Contains(err.Error(), "MESSAGE_ID_INVALID") {
		t.Fatalf("messages.addPollAnswer err = %v, want MESSAGE_ID_INVALID without poll store", err)
	}
	if _, err := r.onMessagesDeletePollAnswer(ownerCtx, &tg.MessagesDeletePollAnswerRequest{
		Peer:   inputPeerChannel(channel),
		MsgID:  1,
		Option: []byte("a"),
	}); err == nil || !strings.Contains(err.Error(), "MESSAGE_ID_INVALID") {
		t.Fatalf("messages.deletePollAnswer err = %v, want MESSAGE_ID_INVALID without poll store", err)
	}
	unreadPollVotes, err := r.onMessagesGetUnreadPollVotes(ownerCtx, &tg.MessagesGetUnreadPollVotesRequest{
		Peer:  inputPeerChannel(channel),
		Limit: 10,
	})
	if err != nil {
		t.Fatalf("messages.getUnreadPollVotes: %v", err)
	}
	if len(unreadPollVotes.(*tg.MessagesMessages).Messages) != 0 {
		t.Fatalf("messages.getUnreadPollVotes = %+v, want empty messages", unreadPollVotes)
	}
	if _, err := r.onMessagesReadPollVotes(ownerCtx, &tg.MessagesReadPollVotesRequest{
		Peer: inputPeerChannel(channel),
	}); err != nil {
		t.Fatalf("messages.readPollVotes: %v", err)
	}
	if _, err := r.onMessagesAppendTodoList(ownerCtx, &tg.MessagesAppendTodoListRequest{
		Peer:  inputPeerChannel(channel),
		MsgID: 1,
	}); err == nil || !strings.Contains(err.Error(), "TODO_NOT_MODIFIED") {
		t.Fatalf("messages.appendTodoList empty err = %v, want TODO_NOT_MODIFIED", err)
	}
	if _, err := r.onMessagesAppendTodoList(ownerCtx, &tg.MessagesAppendTodoListRequest{
		Peer:  inputPeerChannel(channel),
		MsgID: 1,
		List:  []tg.TodoItem{{Title: tg.TextWithEntities{Text: "item"}}},
	}); err == nil || !strings.Contains(err.Error(), "MESSAGE_ID_INVALID") {
		t.Fatalf("messages.appendTodoList err = %v, want MESSAGE_ID_INVALID without todo store", err)
	}
	if _, err := r.onMessagesToggleTodoCompleted(ownerCtx, &tg.MessagesToggleTodoCompletedRequest{
		Peer:  inputPeerChannel(channel),
		MsgID: 1,
	}); err == nil || !strings.Contains(err.Error(), "TODO_NOT_MODIFIED") {
		t.Fatalf("messages.toggleTodoCompleted empty err = %v, want TODO_NOT_MODIFIED", err)
	}
	if _, err := r.onMessagesToggleTodoCompleted(ownerCtx, &tg.MessagesToggleTodoCompletedRequest{
		Peer:      inputPeerChannel(channel),
		MsgID:     1,
		Completed: []int{1},
	}); err == nil || !strings.Contains(err.Error(), "MESSAGE_ID_INVALID") {
		t.Fatalf("messages.toggleTodoCompleted err = %v, want MESSAGE_ID_INVALID without todo store", err)
	}
	counters, err := r.onMessagesGetSearchCounters(ownerCtx, &tg.MessagesGetSearchCountersRequest{
		Peer:    inputPeerChannel(channel),
		Filters: []tg.MessagesFilterClass{&tg.InputMessagesFilterEmpty{}},
	})
	if err != nil {
		t.Fatalf("messages.getSearchCounters: %v", err)
	}
	if len(counters) != 1 || counters[0].Count != 0 {
		t.Fatalf("messages.getSearchCounters = %+v, want one zero counter", counters)
	}
	calendar, err := r.onMessagesGetSearchResultsCalendar(ownerCtx, &tg.MessagesGetSearchResultsCalendarRequest{
		Peer:       inputPeerChannel(channel),
		Filter:     &tg.InputMessagesFilterPhotos{},
		OffsetID:   7,
		OffsetDate: 1700000000,
	})
	if err != nil {
		t.Fatalf("messages.getSearchResultsCalendar: %v", err)
	}
	if calendar.Count != 0 || calendar.MinDate != 1700000000 || calendar.MinMsgID != 7 || len(calendar.Messages) != 0 {
		t.Fatalf("messages.getSearchResultsCalendar = %+v, want empty stable-offset result", calendar)
	}
	positions, err := r.onMessagesGetSearchResultsPositions(ownerCtx, &tg.MessagesGetSearchResultsPositionsRequest{
		Peer:     inputPeerChannel(channel),
		Filter:   &tg.InputMessagesFilterPhotos{},
		OffsetID: 7,
		Limit:    10,
	})
	if err != nil {
		t.Fatalf("messages.getSearchResultsPositions: %v", err)
	}
	if positions.Count != 0 || len(positions.Positions) != 0 {
		t.Fatalf("messages.getSearchResultsPositions = %+v, want empty positions", positions)
	}
	if _, err := r.onMessagesGetSearchResultsPositions(ownerCtx, &tg.MessagesGetSearchResultsPositionsRequest{
		Peer:     inputPeerChannel(channel),
		Filter:   &tg.InputMessagesFilterPhotos{},
		OffsetID: 7,
		Limit:    maxSearchResultsLimit + 1,
	}); err == nil || !strings.Contains(err.Error(), "LIMIT_INVALID") {
		t.Fatalf("messages.getSearchResultsPositions huge limit err = %v, want LIMIT_INVALID", err)
	}
	replies, err := r.onMessagesGetReplies(ownerCtx, &tg.MessagesGetRepliesRequest{
		Peer:  inputPeerChannel(channel),
		MsgID: 1,
		Limit: 10,
	})
	if err != nil {
		t.Fatalf("messages.getReplies: %v", err)
	}
	_, replyChats, _ := searchMessagesPayload(t, replies)
	if len(replyChats) != 1 {
		t.Fatalf("messages.getReplies = %+v, want channel context", replies)
	}
	discussion, err := r.onMessagesGetDiscussionMessage(ownerCtx, &tg.MessagesGetDiscussionMessageRequest{
		Peer:  inputPeerChannel(channel),
		MsgID: 1,
	})
	if err != nil {
		t.Fatalf("messages.getDiscussionMessage: %v", err)
	}
	if len(discussion.Messages) != 1 || len(discussion.Chats) != 1 || discussion.UnreadCount != 0 {
		t.Fatalf("messages.getDiscussionMessage = %+v, want root message with channel context", discussion)
	}
	if _, ok := discussion.Messages[0].(*tg.MessageService); !ok {
		t.Fatalf("messages.getDiscussionMessage message = %T, want service root", discussion.Messages[0])
	}
	if _, err := r.onMessagesReadDiscussion(ownerCtx, &tg.MessagesReadDiscussionRequest{
		Peer:      inputPeerChannel(channel),
		MsgID:     1,
		ReadMaxID: 1,
	}); err != nil {
		t.Fatalf("messages.readDiscussion err = %v, want nil", err)
	}
	if _, err := r.onMessagesGetDiscussionMessage(ownerCtx, &tg.MessagesGetDiscussionMessageRequest{
		Peer:  inputPeerChannelWithHash(channel, channel.AccessHash+1),
		MsgID: 1,
	}); err == nil || !strings.Contains(err.Error(), "CHANNEL_PRIVATE") {
		t.Fatalf("messages.getDiscussionMessage bad hash err = %v, want CHANNEL_PRIVATE", err)
	}
	if _, err := r.onMessagesReadDiscussion(ownerCtx, &tg.MessagesReadDiscussionRequest{
		Peer:      inputPeerChannel(channel),
		MsgID:     1,
		ReadMaxID: domain.MaxMessageBoxID + 1,
	}); err == nil || !strings.Contains(err.Error(), "MESSAGE_ID_INVALID") {
		t.Fatalf("messages.readDiscussion huge read_max_id err = %v, want MESSAGE_ID_INVALID", err)
	}
	if _, err := r.onMessagesGetForumTopics(ownerCtx, &tg.MessagesGetForumTopicsRequest{
		Peer:  inputPeerChannel(channel),
		Limit: 10,
	}); err == nil || !strings.Contains(err.Error(), "CHANNEL_FORUM_MISSING") {
		t.Fatalf("messages.getForumTopics non-forum err = %v, want CHANNEL_FORUM_MISSING", err)
	}
	if _, err := r.onMessagesGetForumTopicsByID(ownerCtx, &tg.MessagesGetForumTopicsByIDRequest{
		Peer:   inputPeerChannel(channel),
		Topics: []int{1},
	}); err == nil || !strings.Contains(err.Error(), "CHANNEL_FORUM_MISSING") {
		t.Fatalf("messages.getForumTopicsByID non-forum err = %v, want CHANNEL_FORUM_MISSING", err)
	}
	onlines, err := r.onMessagesGetOnlines(ownerCtx, inputPeerChannel(channel))
	if err != nil {
		t.Fatalf("messages.getOnlines: %v", err)
	}
	if onlines.Onlines != 1 {
		t.Fatalf("messages.getOnlines = %+v, want count 1", onlines)
	}
	stats, err := r.onStatsGetMegagroupStats(ownerCtx, &tg.StatsGetMegagroupStatsRequest{
		Channel: inputChannel(channel),
	})
	if err != nil {
		t.Fatalf("stats.getMegagroupStats: %v", err)
	}
	if stats.GrowthGraph == nil || stats.MembersGraph == nil {
		t.Fatalf("megagroup stats = %+v, want non-nil graph stubs", stats)
	}
	if _, err := r.onStatsGetBroadcastStats(ownerCtx, &tg.StatsGetBroadcastStatsRequest{
		Channel: inputChannel(channel),
	}); err == nil || !strings.Contains(err.Error(), "BROADCAST_REQUIRED") {
		t.Fatalf("stats.getBroadcastStats on megagroup err = %v, want BROADCAST_REQUIRED", err)
	}
	messageStats, err := r.onStatsGetMessageStats(ownerCtx, &tg.StatsGetMessageStatsRequest{
		Channel: inputChannel(channel),
		MsgID:   1,
	})
	if err != nil {
		t.Fatalf("stats.getMessageStats: %v", err)
	}
	if messageStats.ViewsGraph == nil || messageStats.ReactionsByEmotionGraph == nil {
		t.Fatalf("message stats = %+v, want non-nil graph stubs", messageStats)
	}
	forwards, err := r.onStatsGetMessagePublicForwards(ownerCtx, &tg.StatsGetMessagePublicForwardsRequest{
		Channel: inputChannel(channel),
		MsgID:   1,
		Limit:   10,
	})
	if err != nil {
		t.Fatalf("stats.getMessagePublicForwards: %v", err)
	}
	if forwards.Count != 0 || len(forwards.Forwards) != 0 {
		t.Fatalf("message public forwards = %+v, want empty", forwards)
	}
	if _, err := r.onStatsGetMessagePublicForwards(ownerCtx, &tg.StatsGetMessagePublicForwardsRequest{
		Channel: inputChannel(channel),
		MsgID:   1,
		Limit:   maxStatsPublicForwardsLimit + 1,
	}); err == nil || !strings.Contains(err.Error(), "LIMIT_INVALID") {
		t.Fatalf("stats.getMessagePublicForwards huge limit err = %v, want LIMIT_INVALID", err)
	}
	graph, err := r.onStatsLoadAsyncGraph(ownerCtx, &tg.StatsLoadAsyncGraphRequest{Token: "stale"})
	if err != nil {
		t.Fatalf("stats.loadAsyncGraph: %v", err)
	}
	if _, ok := graph.(*tg.StatsGraphError); !ok {
		t.Fatalf("stats.loadAsyncGraph = %T, want statsGraphError", graph)
	}
	boostStatus, err := r.onPremiumGetBoostsStatus(ownerCtx, inputPeerChannel(channel))
	if err != nil {
		t.Fatalf("premium.getBoostsStatus: %v", err)
	}
	if boostStatus.Level != 0 || boostStatus.CurrentLevelBoosts != 0 || boostStatus.Boosts != 0 {
		t.Fatalf("premium.getBoostsStatus = level %d current %d boosts %d, want empty real state", boostStatus.Level, boostStatus.CurrentLevelBoosts, boostStatus.Boosts)
	}
	if boostStatus.BoostURL == "" {
		t.Fatalf("premium.getBoostsStatus missing boost_url")
	}
	if next, ok := boostStatus.GetNextLevelBoosts(); !ok || next <= boostStatus.CurrentLevelBoosts {
		t.Fatalf("premium.getBoostsStatus real NextLevelBoosts set=%v val=%d, want > CurrentLevelBoosts %d", ok, next, boostStatus.CurrentLevelBoosts)
	}
	boostStatusTDesktop, err := r.onPremiumGetBoostsStatus(WithClientInfo(ownerCtx, ClientInfo{
		Type:       ClientTypeTDesktop,
		AppVersion: "6.8.4 x64",
	}), inputPeerChannel(channel))
	if err != nil {
		t.Fatalf("premium.getBoostsStatus TDesktop: %v", err)
	}
	if boostStatusTDesktop.Level != boostStatus.Level || boostStatusTDesktop.CurrentLevelBoosts != boostStatus.CurrentLevelBoosts || boostStatusTDesktop.Boosts != boostStatus.Boosts {
		t.Fatalf("premium.getBoostsStatus TDesktop = %+v, want same real status as generic client %+v", boostStatusTDesktop, boostStatus)
	}
	boosts, err := r.onPremiumGetBoostsList(ownerCtx, &tg.PremiumGetBoostsListRequest{
		Peer:  inputPeerChannel(channel),
		Limit: 10,
	})
	if err != nil {
		t.Fatalf("premium.getBoostsList: %v", err)
	}
	if boosts.Count != 0 || len(boosts.Boosts) != 0 || len(boosts.Users) != 0 {
		t.Fatalf("premium.getBoostsList = %+v, want empty", boosts)
	}
	if _, err := r.onPremiumGetBoostsList(ownerCtx, &tg.PremiumGetBoostsListRequest{
		Peer:  inputPeerChannel(channel),
		Limit: domain.MaxPremiumBoostsListLimit + 1,
	}); err == nil || !strings.Contains(err.Error(), "LIMIT_INVALID") {
		t.Fatalf("premium.getBoostsList huge limit err = %v, want LIMIT_INVALID", err)
	}
	if _, err := f.users.SetPremiumUntil(f.ctx, owner.ID, int(time.Now().Add(time.Hour).Unix())); err != nil {
		t.Fatalf("grant owner premium: %v", err)
	}
	myBoosts, err := r.onPremiumGetMyBoosts(ownerCtx)
	if err != nil {
		t.Fatalf("premium.getMyBoosts: %v", err)
	}
	if len(myBoosts.MyBoosts) != 1 || myBoosts.MyBoosts[0].Slot != domain.DefaultPremiumBoostSlotID || myBoosts.MyBoosts[0].Date != 0 || myBoosts.MyBoosts[0].Expires != 0 {
		t.Fatalf("premium.getMyBoosts = %+v, want one free base slot", myBoosts)
	}
	if _, ok := myBoosts.MyBoosts[0].GetPeer(); ok || len(myBoosts.Chats) != 0 || len(myBoosts.Users) != 0 {
		t.Fatalf("premium.getMyBoosts free slot refs = %+v, want no peer refs", myBoosts)
	}
	applyReq := &tg.PremiumApplyBoostRequest{
		Peer: inputPeerChannel(channel),
	}
	applyReq.SetSlots([]int{1})
	applied, err := r.onPremiumApplyBoost(ownerCtx, applyReq)
	if err != nil {
		t.Fatalf("premium.applyBoost: %v", err)
	}
	if len(applied.MyBoosts) != 1 {
		t.Fatalf("premium.applyBoost = %+v, want assigned base slot", applied)
	}
	appliedPeer, ok := applied.MyBoosts[0].GetPeer()
	if !ok {
		t.Fatalf("premium.applyBoost assigned slot missing peer: %+v", applied.MyBoosts[0])
	}
	if peer, ok := appliedPeer.(*tg.PeerChannel); !ok || peer.ChannelID != channel.ID {
		t.Fatalf("premium.applyBoost peer = %#v, want channel %d", appliedPeer, channel.ID)
	}
	boostStatusAfterApply, err := r.onPremiumGetBoostsStatus(ownerCtx, inputPeerChannel(channel))
	if err != nil {
		t.Fatalf("premium.getBoostsStatus after apply: %v", err)
	}
	if boostStatusAfterApply.Boosts != 1 || !boostStatusAfterApply.MyBoost {
		t.Fatalf("premium.getBoostsStatus after apply = %+v, want one self boost", boostStatusAfterApply)
	}
	if _, err := r.onPremiumApplyBoost(ownerCtx, applyReq); err == nil || !strings.Contains(err.Error(), "BOOST_NOT_MODIFIED") {
		t.Fatalf("premium.applyBoost same peer err = %v, want BOOST_NOT_MODIFIED", err)
	}
	if _, err := r.onPremiumApplyBoost(ownerCtx, &tg.PremiumApplyBoostRequest{
		Peer: inputPeerChannel(channel),
	}); err == nil || !strings.Contains(err.Error(), "BOOSTS_EMPTY") {
		t.Fatalf("premium.applyBoost missing slots err = %v, want BOOSTS_EMPTY", err)
	}
	badSlotReq := &tg.PremiumApplyBoostRequest{Peer: inputPeerChannel(channel)}
	badSlotReq.SetSlots([]int{2})
	if _, err := r.onPremiumApplyBoost(ownerCtx, badSlotReq); err == nil || !strings.Contains(err.Error(), "SLOTS_INVALID") {
		t.Fatalf("premium.applyBoost unsupported slot err = %v, want SLOTS_INVALID", err)
	}
	userBoosts, err := r.onPremiumGetUserBoosts(ownerCtx, &tg.PremiumGetUserBoostsRequest{
		Peer:   inputPeerChannel(channel),
		UserID: &tg.InputUser{UserID: invited.ID, AccessHash: invited.AccessHash},
	})
	if err != nil {
		t.Fatalf("premium.getUserBoosts: %v", err)
	}
	if userBoosts.Count != 0 || len(userBoosts.Boosts) != 0 {
		t.Fatalf("premium.getUserBoosts invited = %+v, want empty", userBoosts)
	}
	ownerBoosts, err := r.onPremiumGetUserBoosts(ownerCtx, &tg.PremiumGetUserBoostsRequest{
		Peer:   inputPeerChannel(channel),
		UserID: inputUser(owner),
	})
	if err != nil {
		t.Fatalf("premium.getUserBoosts owner: %v", err)
	}
	if ownerBoosts.Count != 1 || len(ownerBoosts.Boosts) != 1 {
		t.Fatalf("premium.getUserBoosts owner = %+v, want one boost", ownerBoosts)
	}
	if boostedList, err := r.onPremiumGetBoostsList(ownerCtx, &tg.PremiumGetBoostsListRequest{
		Peer:  inputPeerChannel(channel),
		Limit: 10,
	}); err != nil {
		t.Fatalf("premium.getBoostsList after apply: %v", err)
	} else if boostedList.Count != 1 || len(boostedList.Boosts) != 1 || len(boostedList.Users) != 1 {
		t.Fatalf("premium.getBoostsList after apply = %+v, want one boost and user", boostedList)
	}
	if _, err := r.onMessagesAddChatUser(ownerCtx, &tg.MessagesAddChatUserRequest{
		ChatID: channel.ID,
		UserID: &tg.InputUser{UserID: invited.ID, AccessHash: invited.AccessHash},
	}); err != nil {
		t.Fatalf("messages.addChatUser legacy wrapper: %v", err)
	}
	seedPhoto := domain.Photo{ID: 7701, AccessHash: 7702, DCID: 2}
	if _, err := f.channels.SetChannelPhoto(f.ctx, owner.ID, channel.ID, &seedPhoto, 1700007701); err != nil {
		t.Fatalf("seed channel photo before legacy clear: %v", err)
	}
	if _, err := r.onMessagesEditChatPhoto(ownerCtx, &tg.MessagesEditChatPhotoRequest{
		ChatID: channel.ID,
		Photo:  &tg.InputChatPhotoEmpty{},
	}); err != nil {
		t.Fatalf("messages.editChatPhoto legacy wrapper: %v", err)
	}
	if _, err := r.onChannelsEditPhoto(ownerCtx, &tg.ChannelsEditPhotoRequest{
		Channel: inputChannel(channel),
		Photo:   &tg.InputChatUploadedPhoto{},
	}); err == nil || !strings.Contains(err.Error(), "PHOTO_INVALID") {
		t.Fatalf("channels.editPhoto uploaded photo err = %v, want PHOTO_INVALID", err)
	}
	if _, err := r.onChannelsEditPhoto(ownerCtx, &tg.ChannelsEditPhotoRequest{
		Channel: inputChannel(channel),
		Photo:   &tg.InputChatPhoto{ID: &tg.InputPhoto{ID: 1, AccessHash: 2, FileReference: []byte{3}}},
	}); err == nil || !strings.Contains(err.Error(), "PHOTO_INVALID") {
		t.Fatalf("channels.editPhoto existing photo err = %v, want PHOTO_INVALID", err)
	}
	if ok, err := r.onMessagesEditChatAdmin(ownerCtx, &tg.MessagesEditChatAdminRequest{
		ChatID:  channel.ID,
		UserID:  &tg.InputUser{UserID: invited.ID, AccessHash: invited.AccessHash},
		IsAdmin: true,
	}); err != nil || !ok {
		t.Fatalf("messages.editChatAdmin legacy wrapper = %v, %v", ok, err)
	}
	if _, err := r.onMessagesEditChatParticipantRank(ownerCtx, &tg.MessagesEditChatParticipantRankRequest{
		Peer:        &tg.InputPeerChat{ChatID: channel.ID},
		Participant: &tg.InputPeerUser{UserID: invited.ID, AccessHash: invited.AccessHash},
		Rank:        "ops",
	}); err != nil {
		t.Fatalf("messages.editChatParticipantRank legacy wrapper: %v", err)
	}
	if ok, err := r.onMessagesEditChatAbout(ownerCtx, &tg.MessagesEditChatAboutRequest{
		Peer:  &tg.InputPeerChat{ChatID: channel.ID},
		About: "legacy about",
	}); err != nil || !ok {
		t.Fatalf("messages.editChatAbout legacy wrapper = %v, %v", ok, err)
	}
	fullAfterAbout, err := r.onMessagesGetFullChat(ownerCtx, channel.ID)
	if err != nil {
		t.Fatalf("messages.getFullChat after editChatAbout: %v", err)
	}
	if got := fullAfterAbout.FullChat.(*tg.ChannelFull).About; got != "legacy about" {
		t.Fatalf("full chat about = %q, want legacy about", got)
	}
	if _, err := r.onMessagesEditChatCreator(ownerCtx, &tg.MessagesEditChatCreatorRequest{
		Peer:   &tg.InputPeerChat{ChatID: channel.ID},
		UserID: &tg.InputUser{UserID: invited.ID, AccessHash: invited.AccessHash},
	}); err == nil {
		t.Fatalf("messages.editChatCreator = nil error, want explicit password/2FA error")
	}
	permUpdates, err := r.onMessagesEditChatDefaultBannedRights(ownerCtx, &tg.MessagesEditChatDefaultBannedRightsRequest{
		Peer: inputPeerChannel(channel),
		BannedRights: tg.ChatBannedRights{
			SendMessages: true,
		},
	})
	if err != nil {
		t.Fatalf("messages.editChatDefaultBannedRights: %v", err)
	}
	if len(permUpdates.(*tg.Updates).Updates) == 0 {
		t.Fatalf("messages.editChatDefaultBannedRights updates = %+v, want updateChannel", permUpdates)
	}
	if _, err := r.onMessagesDeleteChatUser(ownerCtx, &tg.MessagesDeleteChatUserRequest{
		ChatID: channel.ID,
		UserID: &tg.InputUser{UserID: invited.ID, AccessHash: invited.AccessHash},
	}); err != nil {
		t.Fatalf("messages.deleteChatUser legacy wrapper: %v", err)
	}
}
