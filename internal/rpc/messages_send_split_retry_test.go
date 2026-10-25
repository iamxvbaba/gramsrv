package rpc

import (
	"context"
	"errors"
	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap/zaptest"
	"strings"
	appmessages "telesrv/internal/app/messages"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
	"testing"
)

type interruptedSplitMessages struct {
	*appmessages.Service
	calls  int
	failAt int
}

func (m *interruptedSplitMessages) SendPrivateText(ctx context.Context, userID int64, req domain.SendPrivateTextRequest) (domain.SendPrivateTextResult, error) {
	m.calls++
	if m.calls == m.failAt {
		return domain.SendPrivateTextResult{}, errors.New("injected connection failure")
	}
	return m.Service.SendPrivateText(ctx, userID, req)
}
func TestSplitMessageRetryResumesSuffixAndRejectsChangedIntent(t *testing.T) {
	ctx := WithUserID(context.Background(), 101)
	dialogs := memory.NewDialogStore()
	st := memory.NewMessageStore(dialogs)
	messages := &interruptedSplitMessages{Service: appmessages.NewService(st, dialogs), failAt: 2}
	r := New(Config{}, Deps{Messages: messages, Users: &countingMapUsersService{mapUsersService: mapUsersService{users: map[int64]domain.User{101: {ID: 101, AccessHash: 1}, 102: {ID: 102, AccessHash: 2}}}}}, zaptest.NewLogger(t), clock.System)
	req := &tg.MessagesSendMessageRequest{Peer: &tg.InputPeerUser{UserID: 102, AccessHash: 2}, RandomID: 9989, Message: strings.Repeat("x", 5000)}
	if _, err := r.onMessagesSendMessage(ctx, req); err == nil {
		t.Fatal("expected second-fragment failure")
	}
	list, err := st.GetByIDs(ctx, 101, []int{1, 2, 3})
	if err != nil || len(list.Messages) != 1 {
		t.Fatalf("first fragment: %+v %v", list, err)
	}
	firstPts := list.Messages[0].Pts
	for i := 0; i < 2; i++ {
		result, err := r.onMessagesSendMessage(ctx, req)
		if err != nil {
			t.Fatalf("retry %d: %v", i, err)
		}
		updates, ok := result.(*tg.Updates)
		if !ok {
			t.Fatalf("updates type %T", result)
		}
		count := 0
		for _, u := range updates.Updates {
			if message, ok := u.(*tg.UpdateNewMessage); ok {
				count++
				if message.Message == nil {
					t.Fatal("nil wire message")
				}
			}
		}
		if count != 2 {
			t.Fatalf("replayed fragments=%d", count)
		}
	}
	list, err = st.GetByIDs(ctx, 101, []int{1, 2, 3})
	if err != nil || len(list.Messages) != 2 {
		t.Fatalf("messages after replay: %+v %v", list, err)
	}
	for _, m := range list.Messages {
		if m.Pts > firstPts+1 {
			t.Fatalf("replay consumed PTS: %d", m.Pts)
		}
	}
	changed := *req
	changed.Message = strings.Repeat("x", 4999) + "z"
	if _, err := r.onMessagesSendMessage(ctx, &changed); err == nil {
		t.Fatal("same random id accepted changed suffix")
	}
}
func TestSplitMessagePreflightRejectsBeforeWriting(t *testing.T) {
	for _, req := range []*tg.MessagesSendMessageRequest{
		{Message: strings.Repeat("x", maxSendMessageTextLength*maxSendMessageChunks+1)},
		{Message: strings.Repeat("x", 5000), Entities: []tg.MessageEntityClass{&tg.MessageEntityBold{Offset: 4999, Length: 2}}},
	} {
		messages := &multiCaptureMessages{}
		r := New(Config{}, Deps{Messages: messages}, zaptest.NewLogger(t), clock.System)
		req.RandomID = 123
		if _, err := r.onMessagesSendMessage(WithUserID(context.Background(), 101), req); err == nil {
			t.Fatal("invalid request accepted")
		}
		if len(messages.requests) != 0 {
			t.Fatal("invalid request partially persisted")
		}
	}
}
