package rpc

import (
	"context"
	"testing"

	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"

	"telesrv/internal/domain"
)

func sendTestTodo(t *testing.T, r *Router, fromID int64, peer tg.InputPeerClass, randomID int64) *tg.Message {
	return sendTestTodoWithFlags(t, r, fromID, peer, randomID, false, true)
}

func sendTestTodoWithFlags(t *testing.T, r *Router, fromID int64, peer tg.InputPeerClass, randomID int64, othersCanAppend, othersCanComplete bool) *tg.Message {
	t.Helper()
	updates, err := r.onMessagesSendMedia(WithUserID(context.Background(), fromID), &tg.MessagesSendMediaRequest{
		Peer: peer,
		Media: &tg.InputMediaTodo{Todo: tg.TodoList{
			OthersCanAppend:   othersCanAppend,
			OthersCanComplete: othersCanComplete,
			Title:             tg.TextWithEntities{Text: "groceries", Entities: []tg.MessageEntityClass{}},
			List: []tg.TodoItem{
				{ID: 1, Title: tg.TextWithEntities{Text: "milk", Entities: []tg.MessageEntityClass{}}},
				{ID: 2, Title: tg.TextWithEntities{Text: "bread", Entities: []tg.MessageEntityClass{}}},
			},
		}},
		RandomID: randomID,
	})
	if err != nil {
		t.Fatalf("sendMedia todo: %v", err)
	}
	return newMessageFromUpdates(t, updates)
}

func TestSendMediaTodoEcho(t *testing.T) {
	r, owner, friend := newMediaTestRouter(t)
	msg := sendTestTodo(t, r, owner.ID, &tg.InputPeerUser{UserID: friend.ID, AccessHash: friend.AccessHash}, 7001)
	media, ok := msg.Media.(*tg.MessageMediaToDo)
	if !ok {
		t.Fatalf("expected MessageMediaToDo, got %T", msg.Media)
	}
	if media.Todo.Title.Text != "groceries" || len(media.Todo.List) != 2 {
		t.Fatalf("todo definition mangled: %+v", media.Todo)
	}
	if !media.Todo.OthersCanComplete {
		t.Error("others_can_complete flag lost")
	}
	if _, has := media.GetCompletions(); has {
		t.Error("fresh todo should have no completions")
	}
}

func TestSendMediaTodoWithEmptyReplyToTreatsReplyAsAbsent(t *testing.T) {
	r, owner, friend := newMediaTestRouter(t)
	req := &tg.MessagesSendMediaRequest{
		Peer: &tg.InputPeerUser{UserID: friend.ID, AccessHash: friend.AccessHash},
		Media: &tg.InputMediaTodo{Todo: tg.TodoList{
			Title: tg.TextWithEntities{Text: "web checklist", Entities: []tg.MessageEntityClass{}},
			List: []tg.TodoItem{
				{ID: 1, Title: tg.TextWithEntities{Text: "send item", Entities: []tg.MessageEntityClass{}}},
			},
		}},
		RandomID: 7005,
	}
	req.SetReplyTo(&tg.InputReplyToMessage{})

	updates, err := r.onMessagesSendMedia(WithUserID(context.Background(), owner.ID), req)
	if err != nil {
		t.Fatalf("sendMedia todo with empty reply: %v", err)
	}
	msg := newMessageFromUpdates(t, updates)
	if msg.ReplyTo != nil {
		t.Fatalf("reply_to = %T, want nil for empty input reply", msg.ReplyTo)
	}
	if media, ok := msg.Media.(*tg.MessageMediaToDo); !ok || media.Todo.Title.Text != "web checklist" {
		t.Fatalf("media = %#v, want todo checklist", msg.Media)
	}
}

func TestSendMediaTodoAcceptsSparseLargeItemIDs(t *testing.T) {
	r, owner, friend := newMediaTestRouter(t)
	updates, err := r.onMessagesSendMedia(WithUserID(context.Background(), owner.ID), &tg.MessagesSendMediaRequest{
		Peer: &tg.InputPeerUser{UserID: friend.ID, AccessHash: friend.AccessHash},
		Media: &tg.InputMediaTodo{Todo: tg.TodoList{
			Title: tg.TextWithEntities{Text: "web sparse ids", Entities: []tg.MessageEntityClass{}},
			List: []tg.TodoItem{
				{ID: 56151290, Title: tg.TextWithEntities{Text: "first", Entities: []tg.MessageEntityClass{}}},
				{ID: 56151305, Title: tg.TextWithEntities{Text: "second", Entities: []tg.MessageEntityClass{}}},
			},
		}},
		RandomID: 7006,
	})
	if err != nil {
		t.Fatalf("sendMedia todo sparse ids: %v", err)
	}
	msg := newMessageFromUpdates(t, updates)
	media, ok := msg.Media.(*tg.MessageMediaToDo)
	if !ok {
		t.Fatalf("media = %T, want MessageMediaToDo", msg.Media)
	}
	if got := media.Todo.List[0].ID; got != 56151290 {
		t.Fatalf("first todo id = %d, want 56151290", got)
	}
}

func TestToggleTodoCompletedAndAppend(t *testing.T) {
	ctx := context.Background()
	r, owner, friend := newMediaTestRouter(t)
	ownerPeer := &tg.InputPeerUser{UserID: friend.ID, AccessHash: friend.AccessHash}
	ownerCtx := WithUserID(ctx, owner.ID)

	msg := sendTestTodo(t, r, owner.ID, ownerPeer, 7002)

	// 勾选 1 项。
	updates, err := r.onMessagesToggleTodoCompleted(ownerCtx, &tg.MessagesToggleTodoCompletedRequest{
		Peer:      ownerPeer,
		MsgID:     msg.ID,
		Completed: []int{1},
	})
	if err != nil {
		t.Fatalf("toggleTodoCompleted: %v", err)
	}
	edited := editedMessageFromUpdates(t, updates)
	todo := edited.Media.(*tg.MessageMediaToDo)
	completions, has := todo.GetCompletions()
	if !has || len(completions) != 1 || completions[0].ID != 1 {
		t.Fatalf("completions = %+v, want item 1 completed", completions)
	}
	if peerUser, ok := completions[0].CompletedBy.(*tg.PeerUser); !ok || peerUser.UserID != owner.ID {
		t.Fatalf("completed_by = %#v, want owner", completions[0].CompletedBy)
	}

	// 取消勾选。
	updates, err = r.onMessagesToggleTodoCompleted(ownerCtx, &tg.MessagesToggleTodoCompletedRequest{
		Peer:        ownerPeer,
		MsgID:       msg.ID,
		Incompleted: []int{1},
	})
	if err != nil {
		t.Fatalf("toggle incompleted: %v", err)
	}
	edited = editedMessageFromUpdates(t, updates)
	todo = edited.Media.(*tg.MessageMediaToDo)
	if completions, has := todo.GetCompletions(); has && len(completions) != 0 {
		t.Fatalf("completions after untoggle = %+v, want empty", completions)
	}

	// 空变更 → TODO_NOT_MODIFIED。
	if _, err := r.onMessagesToggleTodoCompleted(ownerCtx, &tg.MessagesToggleTodoCompletedRequest{
		Peer:        ownerPeer,
		MsgID:       msg.ID,
		Incompleted: []int{2},
	}); err == nil || !tgerr.Is(err, "TODO_NOT_MODIFIED") {
		t.Fatalf("no-op toggle err = %v, want TODO_NOT_MODIFIED", err)
	}
	// 不存在的项。
	if _, err := r.onMessagesToggleTodoCompleted(ownerCtx, &tg.MessagesToggleTodoCompletedRequest{
		Peer:      ownerPeer,
		MsgID:     msg.ID,
		Completed: []int{9},
	}); err == nil || !tgerr.Is(err, "MESSAGE_ID_INVALID") {
		t.Fatalf("unknown item toggle err = %v, want MESSAGE_ID_INVALID", err)
	}

	// 追加两项。
	updates, err = r.onMessagesAppendTodoList(ownerCtx, &tg.MessagesAppendTodoListRequest{
		Peer:  ownerPeer,
		MsgID: msg.ID,
		List: []tg.TodoItem{
			{ID: 3, Title: tg.TextWithEntities{Text: "eggs", Entities: []tg.MessageEntityClass{}}},
			{ID: 4, Title: tg.TextWithEntities{Text: "tea", Entities: []tg.MessageEntityClass{}}},
		},
	})
	if err != nil {
		t.Fatalf("appendTodoList: %v", err)
	}
	edited = editedMessageFromUpdates(t, updates)
	todo = edited.Media.(*tg.MessageMediaToDo)
	if len(todo.Todo.List) != 4 {
		t.Fatalf("items after append = %d, want 4", len(todo.Todo.List))
	}
	// 重复 id 追加被拒。
	if _, err := r.onMessagesAppendTodoList(ownerCtx, &tg.MessagesAppendTodoListRequest{
		Peer:  ownerPeer,
		MsgID: msg.ID,
		List:  []tg.TodoItem{{ID: 3, Title: tg.TextWithEntities{Text: "dup", Entities: []tg.MessageEntityClass{}}}},
	}); err == nil || !tgerr.Is(err, "TODO_ITEM_DUPLICATE") {
		t.Fatalf("duplicate append err = %v, want TODO_ITEM_DUPLICATE", err)
	}
}

func TestTodoMutationParticipantAllowed(t *testing.T) {
	ctx := context.Background()
	r, owner, friend := newMediaTestRouter(t)
	ownerPeer := &tg.InputPeerUser{UserID: friend.ID, AccessHash: friend.AccessHash}
	friendPeer := &tg.InputPeerUser{UserID: owner.ID, AccessHash: owner.AccessHash}

	ownerMsg := sendTestTodo(t, r, owner.ID, ownerPeer, 7003)
	friendMsg := privateMessageForPeer(t, r, friend.ID, domain.Peer{Type: domain.PeerTypeUser, ID: owner.ID})

	updates, err := r.onMessagesToggleTodoCompleted(WithUserID(ctx, friend.ID), &tg.MessagesToggleTodoCompletedRequest{
		Peer:      friendPeer,
		MsgID:     friendMsg.ID,
		Completed: []int{1},
	})
	if err != nil {
		t.Fatalf("participant toggleTodoCompleted: %v", err)
	}
	edited := editedMessageFromUpdates(t, updates)
	todo := edited.Media.(*tg.MessageMediaToDo)
	completions, has := todo.GetCompletions()
	if !has || len(completions) != 1 || completions[0].ID != 1 {
		t.Fatalf("participant response completions = %+v, want item 1", completions)
	}
	if peerUser, ok := completions[0].CompletedBy.(*tg.PeerUser); !ok || peerUser.UserID != friend.ID {
		t.Fatalf("participant completed_by = %#v, want friend", completions[0].CompletedBy)
	}

	ownerView, err := r.deps.Messages.GetMessages(ctx, owner.ID, []int{ownerMsg.ID})
	if err != nil {
		t.Fatalf("owner get messages: %v", err)
	}
	if len(ownerView.Messages) != 1 || ownerView.Messages[0].Media == nil || ownerView.Messages[0].Media.Todo == nil {
		t.Fatalf("owner todo view = %+v, want todo media", ownerView.Messages)
	}
	if got := ownerView.Messages[0].Media.Todo.Completions; len(got) != 1 || got[0].ID != 1 || got[0].CompletedBy != friend.ID {
		t.Fatalf("owner durable completions = %+v, want friend completed item 1", got)
	}
}

func TestTodoMutationParticipantRequiresFlag(t *testing.T) {
	ctx := context.Background()
	r, owner, friend := newMediaTestRouter(t)
	ownerPeer := &tg.InputPeerUser{UserID: friend.ID, AccessHash: friend.AccessHash}
	friendPeer := &tg.InputPeerUser{UserID: owner.ID, AccessHash: owner.AccessHash}

	sendTestTodoWithFlags(t, r, owner.ID, ownerPeer, 7004, false, false)
	friendMsg := privateMessageForPeer(t, r, friend.ID, domain.Peer{Type: domain.PeerTypeUser, ID: owner.ID})
	if _, err := r.onMessagesToggleTodoCompleted(WithUserID(ctx, friend.ID), &tg.MessagesToggleTodoCompletedRequest{
		Peer:      friendPeer,
		MsgID:     friendMsg.ID,
		Completed: []int{1},
	}); err == nil || !tgerr.Is(err, "MESSAGE_AUTHOR_REQUIRED") {
		t.Fatalf("participant toggle without flag err = %v, want MESSAGE_AUTHOR_REQUIRED", err)
	}
}

func TestChannelTodoMutationParticipantAllowedEmitsEditAndService(t *testing.T) {
	f := newRPCChannelFixture(t)
	owner := f.user(8101, "15550008101", "TodoOwner")
	member := f.user(8102, "15550008102", "TodoMember")
	channel := createTodoMegagroup(t, f, owner, member)
	peer := inputPeerChannel(channel)

	msg := sendTestTodoWithFlags(t, f.router, owner.ID, peer, 81001, true, true)
	updates, err := f.router.onMessagesToggleTodoCompleted(f.userCtx(member), &tg.MessagesToggleTodoCompletedRequest{
		Peer:      peer,
		MsgID:     msg.ID,
		Completed: []int{1},
	})
	if err != nil {
		t.Fatalf("channel participant toggleTodoCompleted: %v", err)
	}
	edit := findUpdate[*tg.UpdateEditChannelMessage](t, updates)
	service := findUpdate[*tg.UpdateNewChannelMessage](t, updates)
	if service.Pts != edit.Pts+1 {
		t.Fatalf("todo service pts = %d, edit pts = %d, want contiguous", service.Pts, edit.Pts)
	}
	edited, ok := edit.Message.(*tg.Message)
	if !ok {
		t.Fatalf("edit message = %T, want *tg.Message", edit.Message)
	}
	todo, ok := edited.Media.(*tg.MessageMediaToDo)
	if !ok {
		t.Fatalf("edit media = %T, want MessageMediaToDo", edited.Media)
	}
	completions, has := todo.GetCompletions()
	if !has || len(completions) != 1 || completions[0].ID != 1 {
		t.Fatalf("channel completions = %+v, want item 1 completed", completions)
	}
	if peerUser, ok := completions[0].CompletedBy.(*tg.PeerUser); !ok || peerUser.UserID != member.ID {
		t.Fatalf("channel completed_by = %#v, want member", completions[0].CompletedBy)
	}
	if !todoUpdatesHaveUser(updates, member.ID) {
		t.Fatalf("channel todo updates users missing completer %d", member.ID)
	}
	svcMsg, ok := service.Message.(*tg.MessageService)
	if !ok {
		t.Fatalf("service message = %T, want *tg.MessageService", service.Message)
	}
	action, ok := svcMsg.Action.(*tg.MessageActionTodoCompletions)
	if !ok {
		t.Fatalf("service action = %T, want MessageActionTodoCompletions", svcMsg.Action)
	}
	if len(action.Completed) != 1 || action.Completed[0] != 1 || len(action.Incompleted) != 0 {
		t.Fatalf("service action payload = %+v, want completed item 1", action)
	}
	reply, ok := svcMsg.GetReplyTo()
	if !ok {
		t.Fatal("todo service message missing reply_to")
	}
	header, ok := reply.(*tg.MessageReplyHeader)
	if !ok || header.ReplyToMsgID != msg.ID {
		t.Fatalf("todo service reply = %#v, want original msg %d", reply, msg.ID)
	}

	history, err := f.router.deps.Channels.GetMessages(f.ctx, member.ID, channel.ID, []int{msg.ID})
	if err != nil {
		t.Fatalf("get channel todo: %v", err)
	}
	if len(history.Messages) != 1 || history.Messages[0].Media == nil || history.Messages[0].Media.Todo == nil {
		t.Fatalf("channel stored todo = %+v, want todo media", history.Messages)
	}
	if got := history.Messages[0].Media.Todo.Completions; len(got) != 1 || got[0].ID != 1 || got[0].CompletedBy != member.ID {
		t.Fatalf("stored channel completions = %+v, want member completed item 1", got)
	}

	diff, err := f.router.deps.Channels.GetDifference(f.ctx, member.ID, domain.ChannelDifferenceRequest{
		ChannelID: channel.ID,
		Pts:       edit.Pts - 1,
		Limit:     10,
	})
	if err != nil {
		t.Fatalf("channel difference after todo mutation: %v", err)
	}
	if len(diff.OtherUpdates) != 1 || diff.OtherUpdates[0].Type != domain.ChannelUpdateEditMessage {
		t.Fatalf("difference other updates = %+v, want edit update", diff.OtherUpdates)
	}
	if len(diff.NewMessages) != 1 || diff.NewMessages[0].Action == nil || diff.NewMessages[0].Action.Type != domain.ChannelActionTodoCompletions {
		t.Fatalf("difference new messages = %+v, want todo completions service", diff.NewMessages)
	}
}

func TestChannelTodoAppendParticipantAllowedEmitsService(t *testing.T) {
	f := newRPCChannelFixture(t)
	owner := f.user(8111, "15550008111", "TodoOwner")
	member := f.user(8112, "15550008112", "TodoMember")
	channel := createTodoMegagroup(t, f, owner, member)
	peer := inputPeerChannel(channel)

	msg := sendTestTodoWithFlags(t, f.router, owner.ID, peer, 81101, true, true)
	updates, err := f.router.onMessagesAppendTodoList(f.userCtx(member), &tg.MessagesAppendTodoListRequest{
		Peer:  peer,
		MsgID: msg.ID,
		List: []tg.TodoItem{
			{ID: 3, Title: tg.TextWithEntities{Text: "eggs", Entities: []tg.MessageEntityClass{}}},
		},
	})
	if err != nil {
		t.Fatalf("channel participant appendTodoList: %v", err)
	}
	edit := findUpdate[*tg.UpdateEditChannelMessage](t, updates)
	edited := edit.Message.(*tg.Message)
	todo := edited.Media.(*tg.MessageMediaToDo)
	if len(todo.Todo.List) != 3 {
		t.Fatalf("channel todo items after append = %d, want 3", len(todo.Todo.List))
	}
	service := findUpdate[*tg.UpdateNewChannelMessage](t, updates)
	svcMsg := service.Message.(*tg.MessageService)
	action, ok := svcMsg.Action.(*tg.MessageActionTodoAppendTasks)
	if !ok {
		t.Fatalf("service action = %T, want MessageActionTodoAppendTasks", svcMsg.Action)
	}
	if len(action.List) != 1 || action.List[0].ID != 3 || action.List[0].Title.Text != "eggs" {
		t.Fatalf("append service payload = %+v, want appended task 3", action.List)
	}
}

func TestChannelTodoMutationParticipantRequiresFlag(t *testing.T) {
	f := newRPCChannelFixture(t)
	owner := f.user(8121, "15550008121", "TodoOwner")
	member := f.user(8122, "15550008122", "TodoMember")
	channel := createTodoMegagroup(t, f, owner, member)
	peer := inputPeerChannel(channel)

	msg := sendTestTodoWithFlags(t, f.router, owner.ID, peer, 81201, false, false)
	if _, err := f.router.onMessagesToggleTodoCompleted(f.userCtx(member), &tg.MessagesToggleTodoCompletedRequest{
		Peer:      peer,
		MsgID:     msg.ID,
		Completed: []int{1},
	}); err == nil || !tgerr.Is(err, "MESSAGE_AUTHOR_REQUIRED") {
		t.Fatalf("channel participant toggle without flag err = %v, want MESSAGE_AUTHOR_REQUIRED", err)
	}
}

func todoUpdatesHaveUser(updates tg.UpdatesClass, userID int64) bool {
	full, ok := updates.(*tg.Updates)
	if !ok {
		return false
	}
	for _, item := range full.Users {
		user, ok := item.(*tg.User)
		if ok && user.ID == userID {
			return true
		}
	}
	return false
}

func createTodoMegagroup(t *testing.T, f *rpcChannelFixture, owner domain.User, members ...domain.User) *tg.Channel {
	t.Helper()
	created, err := f.router.onChannelsCreateChannel(f.userCtx(owner), &tg.ChannelsCreateChannelRequest{
		Title:     "Todo Group",
		Megagroup: true,
	})
	if err != nil {
		t.Fatalf("create todo megagroup: %v", err)
	}
	channel := created.(*tg.Updates).Chats[0].(*tg.Channel)
	if len(members) > 0 {
		users := make([]tg.InputUserClass, 0, len(members))
		for _, member := range members {
			users = append(users, inputUser(member))
		}
		if _, err := f.router.onChannelsInviteToChannel(f.userCtx(owner), &tg.ChannelsInviteToChannelRequest{
			Channel: inputChannel(channel),
			Users:   users,
		}); err != nil {
			t.Fatalf("invite todo members: %v", err)
		}
	}
	return channel
}

func privateMessageForPeer(t *testing.T, r *Router, userID int64, peer domain.Peer) domain.Message {
	t.Helper()
	list, err := r.deps.Messages.GetHistory(context.Background(), userID, domain.MessageFilter{
		HasPeer: true,
		Peer:    peer,
		Limit:   10,
	})
	if err != nil {
		t.Fatalf("get history: %v", err)
	}
	for _, msg := range list.Messages {
		if msg.Peer == peer {
			return msg
		}
	}
	t.Fatalf("no message for user %d peer %+v", userID, peer)
	return domain.Message{}
}
