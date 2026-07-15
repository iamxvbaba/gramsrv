package rpc

import (
	"context"
	"strings"
	"testing"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap/zaptest"

	appaccount "telesrv/internal/app/account"
	appmessages "telesrv/internal/app/messages"
	appusers "telesrv/internal/app/users"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

func TestSavedMusicRPCLifecycle(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, err := userStore.Create(ctx, domain.User{AccessHash: 11, Phone: "15550000011", FirstName: "Owner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	passwords := memory.NewPasswordStore()
	music1 := testMusicDocument(101, 1001, "First")
	music2 := testMusicDocument(102, 1002, "Second")
	voice := domain.Document{ID: 103, AccessHash: 1003, MimeType: "audio/ogg", Attributes: []domain.DocumentAttribute{{Kind: domain.DocAttrAudio, Voice: true, AudioDuration: 3}}}
	files := &fakeFiles{docs: map[int64]domain.Document{
		music1.ID: music1,
		music2.ID: music2,
		voice.ID:  voice,
	}}
	r := New(Config{}, Deps{
		Account: appaccount.NewService(passwords, appaccount.WithSavedMusic(passwords)),
		Files:   files,
		Users:   appusers.NewService(userStore),
	}, zaptest.NewLogger(t), clock.System)
	reqCtx := WithUserID(ctx, owner.ID)

	if ok, err := r.onAccountSaveMusic(reqCtx, &tg.AccountSaveMusicRequest{
		ID: &tg.InputDocument{ID: music1.ID, AccessHash: music1.AccessHash},
	}); err != nil || !ok {
		t.Fatalf("save first music = ok %v err %v, want true/nil", ok, err)
	}
	afterFirst := &tg.AccountSaveMusicRequest{
		ID: &tg.InputDocument{ID: music2.ID, AccessHash: music2.AccessHash},
	}
	afterFirst.SetAfterID(&tg.InputDocument{ID: music1.ID, AccessHash: music1.AccessHash})
	if ok, err := r.onAccountSaveMusic(reqCtx, afterFirst); err != nil || !ok {
		t.Fatalf("save second after first = ok %v err %v, want true/nil", ok, err)
	}
	idsBox, err := r.onAccountGetSavedMusicIDs(reqCtx, 0)
	if err != nil {
		t.Fatalf("get saved music ids: %v", err)
	}
	ids, ok := idsBox.(*tg.AccountSavedMusicIDs)
	if !ok || !int64SliceEqual(ids.IDs, []int64{music1.ID, music2.ID}) {
		t.Fatalf("ids = %T %+v, want [first second]", idsBox, idsBox)
	}
	if notModified, err := r.onAccountGetSavedMusicIDs(reqCtx, int64(tdesktopCountHash(ids.IDs))); err != nil {
		t.Fatalf("get saved music ids hash: %v", err)
	} else if _, ok := notModified.(*tg.AccountSavedMusicIDsNotModified); !ok {
		t.Fatalf("hash result = %T, want notModified", notModified)
	}

	page, err := r.onUsersGetSavedMusic(reqCtx, &tg.UsersGetSavedMusicRequest{ID: &tg.InputUserSelf{}, Limit: 2})
	if err != nil {
		t.Fatalf("users.getSavedMusic: %v", err)
	}
	saved, ok := page.(*tg.UsersSavedMusic)
	if !ok || saved.Count != 2 || len(saved.Documents) != 2 || documentClassID(saved.Documents[0]) != music1.ID {
		t.Fatalf("saved music page = %T %+v, want two docs with first first", page, page)
	}
	hashPage, err := r.onUsersGetSavedMusic(reqCtx, &tg.UsersGetSavedMusicRequest{
		ID:    &tg.InputUserSelf{},
		Limit: 2,
		Hash:  int64(tdesktopCountHash([]int64{music1.ID, music2.ID})),
	})
	if err != nil {
		t.Fatalf("users.getSavedMusic hash: %v", err)
	}
	if nm, ok := hashPage.(*tg.UsersSavedMusicNotModified); !ok || nm.Count != 2 {
		t.Fatalf("hash page = %T %+v, want notModified count=2", hashPage, hashPage)
	}

	full, err := r.onUsersGetFullUser(reqCtx, &tg.InputUserSelf{})
	if err != nil {
		t.Fatalf("users.getFullUser: %v", err)
	}
	if doc, ok := full.FullUser.GetSavedMusicAsNotEmpty(); !ok || doc.ID != music1.ID {
		t.Fatalf("full saved_music = %+v ok=%v, want first document", doc, ok)
	}
	byID, err := r.onUsersGetSavedMusicByID(reqCtx, &tg.UsersGetSavedMusicByIDRequest{
		ID:        &tg.InputUserSelf{},
		Documents: []tg.InputDocumentClass{&tg.InputDocument{ID: music2.ID, AccessHash: music2.AccessHash}},
	})
	if err != nil {
		t.Fatalf("users.getSavedMusicByID: %v", err)
	}
	byIDMusic, ok := byID.(*tg.UsersSavedMusic)
	if !ok || len(byIDMusic.Documents) != 1 || documentClassID(byIDMusic.Documents[0]) != music2.ID {
		t.Fatalf("by id = %T %+v, want second document", byID, byID)
	}

	if ok, err := r.onAccountSaveMusic(reqCtx, &tg.AccountSaveMusicRequest{
		ID: &tg.InputDocument{ID: music2.ID, AccessHash: music2.AccessHash},
	}); err != nil || !ok {
		t.Fatalf("move second to top = ok %v err %v, want true/nil", ok, err)
	}
	idsBox, err = r.onAccountGetSavedMusicIDs(reqCtx, 0)
	if err != nil {
		t.Fatalf("get reordered ids: %v", err)
	}
	ids = idsBox.(*tg.AccountSavedMusicIDs)
	if !int64SliceEqual(ids.IDs, []int64{music2.ID, music1.ID}) {
		t.Fatalf("reordered ids = %v, want [second first]", ids.IDs)
	}
	unsave := &tg.AccountSaveMusicRequest{ID: &tg.InputDocument{ID: music2.ID, AccessHash: music2.AccessHash}}
	unsave.SetUnsave(true)
	if ok, err := r.onAccountSaveMusic(reqCtx, unsave); err != nil || !ok {
		t.Fatalf("unsave second = ok %v err %v, want true/nil", ok, err)
	}
	idsBox, err = r.onAccountGetSavedMusicIDs(reqCtx, 0)
	if err != nil {
		t.Fatalf("get ids after unsave: %v", err)
	}
	ids = idsBox.(*tg.AccountSavedMusicIDs)
	if !int64SliceEqual(ids.IDs, []int64{music1.ID}) {
		t.Fatalf("ids after unsave = %v, want [first]", ids.IDs)
	}
	if _, err := r.onAccountSaveMusic(reqCtx, &tg.AccountSaveMusicRequest{
		ID: &tg.InputDocument{ID: voice.ID, AccessHash: voice.AccessHash},
	}); err == nil || !strings.Contains(err.Error(), "DOCUMENT_INVALID") {
		t.Fatalf("save voice err = %v, want DOCUMENT_INVALID", err)
	}
}

func TestMessagesSearchMusicFiltersAudioDocuments(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	alice, err := userStore.Create(ctx, domain.User{AccessHash: 21, Phone: "15550000021", FirstName: "Alice"})
	if err != nil {
		t.Fatalf("create alice: %v", err)
	}
	bob, err := userStore.Create(ctx, domain.User{AccessHash: 22, Phone: "15550000022", FirstName: "Bob"})
	if err != nil {
		t.Fatalf("create bob: %v", err)
	}
	dialogs := memory.NewDialogStore()
	messageStore := memory.NewMessageStore(dialogs)
	music := testMusicDocument(201, 2001, "Song")
	voice := domain.Document{ID: 202, AccessHash: 2002, Attributes: []domain.DocumentAttribute{{Kind: domain.DocAttrAudio, Voice: true, AudioDuration: 5}}}
	plain := domain.Document{ID: 203, AccessHash: 2003, Attributes: []domain.DocumentAttribute{{Kind: domain.DocAttrFilename, FileName: "file.bin"}}}
	for i, media := range []*domain.MessageMedia{
		{Kind: domain.MessageMediaKindDocument, Document: &plain},
		{Kind: domain.MessageMediaKindDocument, Document: &voice, Voice: true},
		{Kind: domain.MessageMediaKindDocument, Document: &music},
	} {
		if _, err := messageStore.SendPrivateText(ctx, domain.SendPrivateTextRequest{
			SenderUserID:    alice.ID,
			RecipientUserID: bob.ID,
			RandomID:        int64(300 + i),
			Message:         "",
			Media:           media,
			Date:            1700000000 + i,
		}); err != nil {
			t.Fatalf("send media %d: %v", i, err)
		}
	}
	r := New(Config{}, Deps{
		Users:    appusers.NewService(userStore),
		Messages: appmessages.NewService(messageStore, dialogs),
	}, zaptest.NewLogger(t), clock.System)

	var payload bin.Buffer
	if err := (&tg.MessagesSearchRequest{
		Peer:   &tg.InputPeerUser{UserID: alice.ID, AccessHash: alice.AccessHash},
		Filter: &tg.InputMessagesFilterMusic{},
		Limit:  10,
	}).Encode(&payload); err != nil {
		t.Fatalf("encode messages.search: %v", err)
	}
	got, err := r.Dispatch(WithUserID(ctx, bob.ID), [8]byte{}, 0, &payload)
	if err != nil {
		t.Fatalf("messages.search music: %v", err)
	}
	messages, _, _ := searchMessagesPayload(t, got)
	if len(messages) != 1 {
		t.Fatalf("music search messages = %d, want 1", len(messages))
	}
	msg, ok := messages[0].(*tg.Message)
	if !ok {
		t.Fatalf("music search message = %T, want *tg.Message", messages[0])
	}
	docMedia, ok := msg.Media.(*tg.MessageMediaDocument)
	if !ok || documentClassID(docMedia.Document) != music.ID {
		t.Fatalf("music search media = %T %+v, want music document", msg.Media, msg.Media)
	}
}

func TestMessagesSearchGlobalMusicAllowsEmptyQuery(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	alice, err := userStore.Create(ctx, domain.User{AccessHash: 31, Phone: "15550000031", FirstName: "Alice"})
	if err != nil {
		t.Fatalf("create alice: %v", err)
	}
	bob, err := userStore.Create(ctx, domain.User{AccessHash: 32, Phone: "15550000032", FirstName: "Bob"})
	if err != nil {
		t.Fatalf("create bob: %v", err)
	}
	dialogs := memory.NewDialogStore()
	messageStore := memory.NewMessageStore(dialogs)
	music := testMusicDocument(301, 3001, "Global Song")
	voice := domain.Document{ID: 302, AccessHash: 3002, Attributes: []domain.DocumentAttribute{{Kind: domain.DocAttrAudio, Voice: true, AudioDuration: 5}}}
	for i, media := range []*domain.MessageMedia{
		{Kind: domain.MessageMediaKindDocument, Document: &voice, Voice: true},
		{Kind: domain.MessageMediaKindDocument, Document: &music},
	} {
		if _, err := messageStore.SendPrivateText(ctx, domain.SendPrivateTextRequest{
			SenderUserID:    alice.ID,
			RecipientUserID: bob.ID,
			RandomID:        int64(400 + i),
			Media:           media,
			Date:            1700000100 + i,
		}); err != nil {
			t.Fatalf("send media %d: %v", i, err)
		}
	}
	r := New(Config{}, Deps{
		Users:    appusers.NewService(userStore),
		Messages: appmessages.NewService(messageStore, dialogs),
	}, zaptest.NewLogger(t), clock.System)
	reqCtx := WithUserID(ctx, bob.ID)

	if _, err := r.onMessagesSearchGlobal(reqCtx, &tg.MessagesSearchGlobalRequest{
		Filter:     &tg.InputMessagesFilterEmpty{},
		OffsetPeer: &tg.InputPeerEmpty{},
		Limit:      10,
	}); err == nil || !strings.Contains(err.Error(), "SEARCH_QUERY_EMPTY") {
		t.Fatalf("empty non-music searchGlobal err = %v, want SEARCH_QUERY_EMPTY", err)
	}
	got, err := r.onMessagesSearchGlobal(reqCtx, &tg.MessagesSearchGlobalRequest{
		Filter:     &tg.InputMessagesFilterMusic{},
		OffsetPeer: &tg.InputPeerEmpty{},
		Limit:      10,
	})
	if err != nil {
		t.Fatalf("empty music searchGlobal: %v", err)
	}
	messages := messagesFromMessagesClass(t, got)
	if len(messages) != 1 {
		t.Fatalf("global music messages = %d, want 1", len(messages))
	}
	msg, ok := messages[0].(*tg.Message)
	if !ok {
		t.Fatalf("global music message = %T, want *tg.Message", messages[0])
	}
	docMedia, ok := msg.Media.(*tg.MessageMediaDocument)
	if !ok || documentClassID(docMedia.Document) != music.ID {
		t.Fatalf("global music media = %T %+v, want music document", msg.Media, msg.Media)
	}
}

func testMusicDocument(id, accessHash int64, title string) domain.Document {
	return domain.Document{
		ID:         id,
		AccessHash: accessHash,
		MimeType:   "audio/mpeg",
		Attributes: []domain.DocumentAttribute{
			{Kind: domain.DocAttrAudio, AudioDuration: 180, Title: title, Performer: "Artist"},
		},
	}
}

func documentClassID(doc tg.DocumentClass) int64 {
	if d, ok := doc.(*tg.Document); ok {
		return d.ID
	}
	return 0
}

func int64SliceEqual(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func messagesFromMessagesClass(t *testing.T, got tg.MessagesMessagesClass) []tg.MessageClass {
	t.Helper()
	switch v := got.(type) {
	case *tg.MessagesMessages:
		return v.Messages
	case *tg.MessagesMessagesSlice:
		return v.Messages
	default:
		t.Fatalf("messages class = %T, want messages/messagesSlice", got)
		return nil
	}
}
