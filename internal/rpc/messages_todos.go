package rpc

import (
	"context"
	"strings"
	"unicode/utf8"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"go.uber.org/zap"

	"telesrv/internal/domain"
)

// todo 清单：发送/渲染/转发全链路真实现；appendTodoList / toggleTodoCompleted
// 在私聊和超级群均支持 others_can_* 协作。超级群成功协作会同时更新原
// checklist media 快照，并生成 reply 到原消息的 todo 服务消息，二者各占
// 一个 channel pts。premium 创建入口依赖 premium 业务模型，记矩阵 todo。

// domainTodoFromInput 校验 InputMediaTodo 的清单定义并转为 domain 快照。
func domainTodoFromInput(in tg.TodoList) (*domain.MessageTodo, error) {
	title := in.Title.Text
	if strings.TrimSpace(title) == "" {
		return nil, mediaEmptyErr()
	}
	if utf8.RuneCountInString(title) > maxTodoTitleLength {
		return nil, limitInvalidErr()
	}
	if len(in.Title.Entities) > maxMessageEntityCount {
		return nil, limitInvalidErr()
	}
	if err := validateTodoItems(in.List); err != nil {
		return nil, err
	}
	out := &domain.MessageTodo{
		OthersCanAppend:   in.OthersCanAppend,
		OthersCanComplete: in.OthersCanComplete,
		Title:             title,
		TitleEntities:     domainMessageEntities(in.Title.Entities),
	}
	for _, item := range in.List {
		if item.ID <= 0 {
			return nil, messageIDInvalidErr()
		}
		out.Items = append(out.Items, domain.MessageTodoItem{
			ID:       item.ID,
			Title:    item.Title.Text,
			Entities: domainMessageEntities(item.Title.Entities),
		})
	}
	return out, nil
}

// tgTodoMedia 把 domain 快照转回 tg.MessageMediaToDo。
func tgTodoMedia(todo domain.MessageTodo) *tg.MessageMediaToDo {
	list := tg.TodoList{
		OthersCanAppend:   todo.OthersCanAppend,
		OthersCanComplete: todo.OthersCanComplete,
		Title:             tgTextWithEntities(todo.Title, todo.TitleEntities),
	}
	for _, item := range todo.Items {
		list.List = append(list.List, tg.TodoItem{
			ID:    item.ID,
			Title: tgTextWithEntities(item.Title, item.Entities),
		})
	}
	out := &tg.MessageMediaToDo{Todo: list}
	if len(todo.Completions) > 0 {
		completions := make([]tg.TodoCompletion, 0, len(todo.Completions))
		for _, c := range todo.Completions {
			completions = append(completions, tg.TodoCompletion{
				ID:          c.ID,
				CompletedBy: &tg.PeerUser{UserID: c.CompletedBy},
				Date:        c.Date,
			})
		}
		out.SetCompletions(completions)
	}
	return out
}

func tgTodoItems(items []domain.MessageTodoItem) []tg.TodoItem {
	if len(items) == 0 {
		return nil
	}
	out := make([]tg.TodoItem, 0, len(items))
	for _, item := range items {
		out = append(out, tg.TodoItem{
			ID:    item.ID,
			Title: tgTextWithEntities(item.Title, item.Entities),
		})
	}
	return out
}

func (r *Router) onMessagesAppendTodoList(ctx context.Context, req *tg.MessagesAppendTodoListRequest) (tg.UpdatesClass, error) {
	if req.MsgID <= 0 || req.MsgID > domain.MaxMessageBoxID {
		return nil, messageIDInvalidErr()
	}
	if len(req.List) == 0 {
		return nil, todoNotModifiedErr()
	}
	if err := validateTodoItems(req.List); err != nil {
		return nil, err
	}
	return r.mutateTodoMedia(ctx, req.Peer, req.MsgID, func(todo *domain.MessageTodo) bool {
		return todo.OthersCanAppend
	}, func(userID int64, todo *domain.MessageTodo, _ int) (*domain.ChannelMessageAction, error) {
		existing := make(map[int]struct{}, len(todo.Items))
		for _, item := range todo.Items {
			existing[item.ID] = struct{}{}
		}
		if len(todo.Items)+len(req.List) > maxTodoItems {
			return nil, limitInvalidErr()
		}
		appended := make([]domain.MessageTodoItem, 0, len(req.List))
		for _, item := range req.List {
			if item.ID <= 0 {
				return nil, messageIDInvalidErr()
			}
			if _, dup := existing[item.ID]; dup {
				return nil, tgerr.New(400, "TODO_ITEM_DUPLICATE")
			}
			existing[item.ID] = struct{}{}
			appended = append(appended, domain.MessageTodoItem{
				ID:       item.ID,
				Title:    item.Title.Text,
				Entities: domainMessageEntities(item.Title.Entities),
			})
		}
		todo.Items = append(todo.Items, appended...)
		return &domain.ChannelMessageAction{
			Type:      domain.ChannelActionTodoAppendTasks,
			TodoItems: appended,
		}, nil
	})
}

func (r *Router) onMessagesToggleTodoCompleted(ctx context.Context, req *tg.MessagesToggleTodoCompletedRequest) (tg.UpdatesClass, error) {
	if req.MsgID <= 0 || req.MsgID > domain.MaxMessageBoxID {
		return nil, messageIDInvalidErr()
	}
	if len(req.Completed) == 0 && len(req.Incompleted) == 0 {
		return nil, todoNotModifiedErr()
	}
	if err := validateTodoIDVector(req.Completed, req.Incompleted); err != nil {
		return nil, err
	}
	return r.mutateTodoMedia(ctx, req.Peer, req.MsgID, func(todo *domain.MessageTodo) bool {
		return todo.OthersCanComplete
	}, func(userID int64, todo *domain.MessageTodo, now int) (*domain.ChannelMessageAction, error) {
		valid := make(map[int]struct{}, len(todo.Items))
		for _, item := range todo.Items {
			valid[item.ID] = struct{}{}
		}
		done := make(map[int]domain.MessageTodoCompletion, len(todo.Completions))
		for _, c := range todo.Completions {
			done[c.ID] = c
		}
		changed := false
		for _, id := range req.Completed {
			if _, ok := valid[id]; !ok {
				return nil, messageIDInvalidErr()
			}
			if _, already := done[id]; !already {
				done[id] = domain.MessageTodoCompletion{ID: id, CompletedBy: userID, Date: now}
				changed = true
			}
		}
		for _, id := range req.Incompleted {
			if _, ok := valid[id]; !ok {
				return nil, messageIDInvalidErr()
			}
			if _, exists := done[id]; exists {
				delete(done, id)
				changed = true
			}
		}
		if !changed {
			return nil, todoNotModifiedErr()
		}
		todo.Completions = todo.Completions[:0]
		for _, item := range todo.Items {
			if c, ok := done[item.ID]; ok {
				todo.Completions = append(todo.Completions, c)
			}
		}
		return &domain.ChannelMessageAction{
			Type:        domain.ChannelActionTodoCompletions,
			Completed:   append([]int(nil), req.Completed...),
			Incompleted: append([]int(nil), req.Incompleted...),
		}, nil
	})
}

// mutateTodoMedia 加载目标 todo 消息、应用变更并经 editMessage 媒体替换链路落库推送。
func (r *Router) mutateTodoMedia(ctx context.Context, inputPeer tg.InputPeerClass, msgID int, participantAllowed func(todo *domain.MessageTodo) bool, mutate func(userID int64, todo *domain.MessageTodo, now int) (*domain.ChannelMessageAction, error)) (tg.UpdatesClass, error) {
	userID, peer, err := r.reactionPeer(ctx, inputPeer, nil)
	if err != nil {
		return nil, err
	}
	now := int(r.clock.Now().Unix())
	current, found := r.loadTodoMessage(ctx, userID, peer, msgID)
	if !found {
		return nil, messageIDInvalidErr()
	}
	todo := *current.media.Todo
	todo.Items = append([]domain.MessageTodoItem(nil), current.media.Todo.Items...)
	todo.Completions = append([]domain.MessageTodoCompletion(nil), current.media.Todo.Completions...)
	participantEdit := peer.Type == domain.PeerTypeUser && !current.out
	if participantEdit && (participantAllowed == nil || !participantAllowed(&todo)) {
		return nil, messageAuthorRequiredErr()
	}
	serviceAction, err := mutate(userID, &todo, now)
	if err != nil {
		return nil, err
	}
	newMedia := &domain.MessageMedia{Kind: domain.MessageMediaKindTodo, Todo: &todo}

	if peer.Type == domain.PeerTypeChannel {
		if r.deps.Channels == nil {
			return nil, peerIDInvalidErr()
		}
		res, err := r.deps.Channels.EditMessage(ctx, userID, domain.EditChannelMessageRequest{
			UserID:                       userID,
			ChannelID:                    peer.ID,
			ID:                           msgID,
			Message:                      current.body,
			Entities:                     current.entities,
			Media:                        newMedia,
			AllowTodoParticipantMutation: participantAllowed != nil && participantAllowed(&todo),
			TodoServiceAction:            serviceAction,
			EditDate:                     now,
		})
		if err != nil {
			return nil, channelEditErr(err)
		}
		updates := r.channelEditMessageUpdates(ctx, userID, res)
		r.enqueueChannelEditMessageFanout(ctx, userID, res)
		return updates, nil
	}
	if peer.Type != domain.PeerTypeUser || r.deps.Messages == nil {
		return nil, peerIDInvalidErr()
	}
	sessionID, _ := SessionIDFrom(ctx)
	res, err := r.deps.Messages.EditMessage(ctx, userID, domain.EditMessageRequest{
		OwnerUserID:                  userID,
		Peer:                         peer,
		ID:                           msgID,
		Message:                      current.body,
		Entities:                     current.entities,
		Media:                        newMedia,
		EditDate:                     now,
		OriginAuthKeyID:              rawAuthKeyIDForOrigin(ctx),
		OriginSessionID:              sessionID,
		AllowTodoParticipantMutation: participantEdit,
	})
	if err != nil {
		r.log.Warn("todo edit message failed",
			zap.Int64("user_id", userID),
			zap.String("peer_type", string(peer.Type)),
			zap.Int64("peer_id", peer.ID),
			zap.Int("msg_id", msgID),
			zap.Bool("participant_edit", participantEdit),
			zap.Error(err))
		return nil, messageEditErr(err)
	}
	self := res.Self()
	if self.Event.Pts == 0 || self.Message.ID == 0 {
		return nil, messageIDInvalidErr()
	}
	users := r.usersForMessageUpdate(ctx, userID, self.Message)
	chats := r.chatsForMessageUpdate(ctx, userID, self.Message)
	return tgEditMessageUpdates(self.Event, self.Message, users, chats), nil
}

type todoMessageTarget struct {
	media    *domain.MessageMedia
	body     string
	entities []domain.MessageEntity
	out      bool
}

func (r *Router) loadTodoMessage(ctx context.Context, userID int64, peer domain.Peer, msgID int) (todoMessageTarget, bool) {
	switch peer.Type {
	case domain.PeerTypeChannel:
		if r.deps.Channels == nil {
			return todoMessageTarget{}, false
		}
		history, err := r.deps.Channels.GetMessages(ctx, userID, peer.ID, []int{msgID})
		if err != nil {
			return todoMessageTarget{}, false
		}
		for _, msg := range history.Messages {
			if msg.ID == msgID && msg.Media != nil && msg.Media.Kind == domain.MessageMediaKindTodo && msg.Media.Todo != nil {
				return todoMessageTarget{media: msg.Media, body: msg.Body, entities: msg.Entities}, true
			}
		}
	case domain.PeerTypeUser:
		if r.deps.Messages == nil {
			return todoMessageTarget{}, false
		}
		list, err := r.deps.Messages.GetMessages(ctx, userID, []int{msgID})
		if err != nil {
			return todoMessageTarget{}, false
		}
		for _, msg := range list.Messages {
			if msg.ID == msgID && msg.Peer == peer && msg.Media != nil && msg.Media.Kind == domain.MessageMediaKindTodo && msg.Media.Todo != nil {
				return todoMessageTarget{media: msg.Media, body: msg.Body, entities: msg.Entities, out: msg.Out}, true
			}
		}
	}
	return todoMessageTarget{}, false
}

func validateTodoItems(items []tg.TodoItem) error {
	if len(items) == 0 {
		return todoItemsEmptyErr()
	}
	if len(items) > maxTodoItems {
		return limitInvalidErr()
	}
	seen := make(map[int]struct{}, len(items))
	for _, item := range items {
		if item.ID < 0 || item.ID > maxTodoItemID {
			return messageIDInvalidErr()
		}
		if item.ID != 0 {
			if _, ok := seen[item.ID]; ok {
				return tgerr.New(400, "TODO_ITEM_DUPLICATE")
			}
			seen[item.ID] = struct{}{}
		}
		if strings.TrimSpace(item.Title.Text) == "" || utf8.RuneCountInString(item.Title.Text) > maxTodoTitleLength {
			return limitInvalidErr()
		}
		if len(item.Title.Entities) > maxMessageEntityCount {
			return limitInvalidErr()
		}
	}
	return nil
}

func validateTodoIDVector(vectors ...[]int) error {
	total := 0
	seen := map[int]struct{}{}
	for _, ids := range vectors {
		total += len(ids)
		if total > maxTodoItems {
			return limitInvalidErr()
		}
		for _, id := range ids {
			if id <= 0 || id > maxTodoItemID {
				return messageIDInvalidErr()
			}
			if _, ok := seen[id]; ok {
				return todoNotModifiedErr()
			}
			seen[id] = struct{}{}
		}
	}
	return nil
}
